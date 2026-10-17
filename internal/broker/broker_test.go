package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

type fakeMinter struct {
	mu    sync.Mutex
	calls []MintInput
	exps  []time.Time // the Expiration handed out for each call (zero when it failed)
	err   error
	now   func() time.Time
	exp   func(MintInput) time.Time // overrides the returned Expiration
	hook  func(MintInput)           // runs inside Mint after the call is recorded, without mu held
}

func (f *fakeMinter) Mint(_ context.Context, in MintInput) (Creds, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.exps = append(f.exps, time.Time{})
	i, err, hook := len(f.calls)-1, f.err, f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(in)
	}
	if err != nil {
		return Creds{}, err
	}
	exp := f.now().Add(in.Duration)
	if f.exp != nil {
		exp = f.exp(in)
	}
	f.mu.Lock()
	f.exps[i] = exp
	f.mu.Unlock()
	return Creds{AccessKeyID: "ASIAFAKE", SecretAccessKey: "s", SessionToken: "t", Expiration: exp}, nil
}

// expOf is the Expiration handed out for call i; calls0Exp for the first call.
func (f *fakeMinter) expOf(i int) time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.exps[i] }
func (f *fakeMinter) calls0Exp() time.Time  { return f.expOf(0) }

type memRec struct {
	mu      sync.Mutex
	rows    []Row
	appr    []ApprovalRow
	apprErr error
}

func (m *memRec) RecordApproval(r ApprovalRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.apprErr != nil {
		return m.apprErr
	}
	m.appr = append(m.appr, r)
	return nil
}

func (m *memRec) Record(r Row) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r)
	return nil
}

var secret = []byte("0123456789abcdef0123456789abcdef")

const nonceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const nonceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func binding() *config.AWSBinding {
	return &config.AWSBinding{SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
		Region: "ap-southeast-1", S3Read: []string{"culishop-tfstate"}}
}

func setup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeMinter, *memRec, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	fm := &fakeMinter{now: func() time.Time { return now }}
	rec := &memRec{}
	b := New(secret, func(id string) (SessionView, bool) { s, ok := sessions[id]; return s, ok }, fm, rec)
	b.now = func() time.Time { return now }
	return b, fm, rec, &now
}

func call(b *Broker, tok string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/credentials", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

func live() SessionView {
	return SessionView{Agent: "c", Nonce: nonceA, Boxed: true, Live: true, AWS: binding()}
}

func tokA(id string) string { return Token(secret, id, nonceA) }

func TestMintsDownscopedPerSessionCredentials(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	w := call(b, tokA("claude-3"))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["Version"] != float64(1) || out["AccessKeyId"] != "ASIAFAKE" || out["Expiration"] != "2027-01-15T08:15:00Z" {
		t.Fatalf("credential_process JSON: %s", w.Body)
	}
	in := fm.calls[0]
	if in.SessionName != "agentbox-claude-3" || in.Duration != 15*time.Minute || in.RoleARN != binding().RoleARN ||
		in.SourceProfile != "sandbox-admin" || in.Region != "ap-southeast-1" || !strings.Contains(in.Policy, "culishop-tfstate") ||
		!strings.Contains(in.Policy, "arn:aws:s3:::culishop-tfstate") {
		t.Fatalf("mint input: %+v", in)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeMinted || rec.rows[0].AccountID != "123456789012" ||
		rec.rows[0].SessionID != "claude-3" || rec.rows[0].SourceIdentity != "agentbox-claude-3" ||
		rec.rows[0].ScopeHash != binding().Hash() || rec.rows[0].Expires.IsZero() {
		t.Fatalf("audit: %+v", rec.rows)
	}
}

func TestCacheUntilFiveMinutesBeforeExpiry(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": live()})
	tok := tokA("claude-3")
	call(b, tok)
	*now = now.Add(9 * time.Minute) // 6 min left
	call(b, tok)
	if len(fm.calls) != 1 {
		t.Fatalf("expected a cache hit, minted %d times", len(fm.calls))
	}
	*now = now.Add(2 * time.Minute) // 4 min left
	call(b, tok)
	if len(fm.calls) != 2 {
		t.Fatalf("expected a refresh, minted %d times", len(fm.calls))
	}
	st, _ := b.Stats("claude-3")
	if st.Mints != 2 || st.CacheHits != 1 || len(rec.rows) != 2 {
		t.Fatalf("stats %+v rows %d", st, len(rec.rows))
	}
}

func TestRefusals(t *testing.T) {
	host := live()
	host.Boxed = false
	dead := live()
	dead.Live = false
	unbound := live()
	unbound.AWS = nil
	b, fm, rec, _ := setup(t, map[string]SessionView{"host-1": host, "dead-1": dead, "plain-1": unbound, "ok-1": live()})
	for _, c := range []struct {
		name, tok string
		want      int
	}{
		{"no header", "", 401},
		{"garbage", "garbage", 401},
		{"other secret", Token([]byte("wrong"), "ok-1", nonceA), 401},
		{"host session", tokA("host-1"), 403},
		{"dead session", tokA("dead-1"), 403},
		{"no binding", tokA("plain-1"), 403},
		{"unknown session", tokA("missing-1"), 403},
	} {
		if w := call(b, c.tok); w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
	if len(fm.calls) != 0 {
		t.Fatal("a refused request reached STS")
	}
	for _, r := range rec.rows {
		if r.Outcome == OutcomeMinted {
			t.Fatal("refusal audited as minted")
		}
	}
}

func TestBearerSchemeIsRequired(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	r := httptest.NewRequest(http.MethodPost, "/v1/credentials", nil)
	r.Header.Set("Authorization", tokA("claude-3")) // valid token, no "Bearer " scheme
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != 401 || len(fm.calls) != 0 {
		t.Fatalf("status %d, mints %d", w.Code, len(fm.calls))
	}
}

func TestNonceMismatchIsRefusedBeforeSTS(t *testing.T) {
	// A token minted for an earlier spawn that had the same id (ids are reused)
	// must not work for the live session: the nonce differs.
	sv := live()
	sv.Nonce = nonceB
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": sv})
	w := call(b, tokA("claude-3"))
	if w.Code != 403 {
		t.Fatalf("status %d, want 403", w.Code)
	}
	if len(fm.calls) != 0 {
		t.Fatal("a stale-nonce request reached STS")
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedSession || rec.rows[0].SessionID != "claude-3" {
		t.Fatalf("audit: %+v", rec.rows)
	}
	if w := call(b, Token(secret, "claude-3", nonceB)); w.Code != 200 {
		t.Fatalf("the live spawn's own token: %d", w.Code)
	}
}

func TestLiveSessionWithoutNonceIsRefused(t *testing.T) {
	sv := live()
	sv.Nonce = ""
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": sv})
	if w := call(b, tokA("claude-3")); w.Code != 403 || len(fm.calls) != 0 {
		t.Fatalf("status %d, mints %d", w.Code, len(fm.calls))
	}
}

func TestCacheDoesNotCrossASessionIDReuse(t *testing.T) {
	sessions := map[string]SessionView{"claude-3": live()}
	b, fm, _, _ := setup(t, sessions)
	call(b, tokA("claude-3"))
	// The session is replaced by a new spawn under the same id and another binding.
	nb := binding()
	nb.RoleARN = "arn:aws:iam::210987654321:role/other"
	sessions["claude-3"] = SessionView{Agent: "c", Nonce: nonceB, Boxed: true, Live: true, AWS: nb}
	if w := call(b, Token(secret, "claude-3", nonceB)); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if len(fm.calls) != 2 || fm.calls[1].RoleARN != nb.RoleARN {
		t.Fatalf("the new spawn was served the old spawn's cached credentials: %+v", fm.calls)
	}
	// Same spawn, binding edited: also a miss.
	nb2 := *nb
	nb2.S3Read = []string{"other-bucket"}
	sessions["claude-3"] = SessionView{Agent: "c", Nonce: nonceB, Boxed: true, Live: true, AWS: &nb2}
	call(b, Token(secret, "claude-3", nonceB))
	if len(fm.calls) != 3 {
		t.Fatalf("a changed binding reused cached credentials (%d mints)", len(fm.calls))
	}
}

func TestDeniedRowsAreThrottled(t *testing.T) {
	b, _, rec, now := setup(t, nil)
	for i := 0; i < 50; i++ {
		call(b, "garbage")
	}
	if len(rec.rows) != 1 {
		t.Fatalf("50 bad tokens in one second wrote %d audit rows, want 1", len(rec.rows))
	}
	if rec.rows[0].SessionID != "" {
		t.Fatalf("an unverified claimed id was stored: %+v", rec.rows[0])
	}
	*now = now.Add(time.Second)
	call(b, "garbage")
	if len(rec.rows) != 2 {
		t.Fatalf("next second should record again, rows=%d", len(rec.rows))
	}
}

func TestMintRateLimit(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": live()})
	tok := tokA("claude-3")
	fm.err = errors.New("boom") // nothing cached, so every call tries to mint
	for i := 0; i < 12; i++ {
		call(b, tok)
	}
	if len(fm.calls) != 10 {
		t.Fatalf("minted %d times in a minute, want 10", len(fm.calls))
	}
	if w := call(b, tok); w.Code != 429 {
		t.Fatalf("status %d, want 429", w.Code)
	}
	limited := 0
	for _, r := range rec.rows {
		if r.Outcome == OutcomeRateLimited {
			limited++
		}
	}
	if limited != 1 {
		t.Fatalf("rate-limited rows %d, want exactly 1 (throttled to 1/s)", limited)
	}
	*now = now.Add(61 * time.Second)
	fm.err = nil
	if w := call(b, tok); w.Code != 200 {
		t.Fatalf("after the window: %d", w.Code)
	}
}

func TestCacheHitsAreNotRateLimited(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	tok := tokA("claude-3")
	for i := 0; i < 50; i++ {
		if w := call(b, tok); w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	if len(fm.calls) != 1 {
		t.Fatalf("minted %d times", len(fm.calls))
	}
	if st, _ := b.Stats("claude-3"); st.CacheHits != 49 {
		t.Fatalf("cache hits %d", st.CacheHits)
	}
}

func TestSSOExpiredIsActionable(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	fm.err = fmt.Errorf("%w: token expired", ErrSSOExpired)
	w := call(b, tokA("claude-3"))
	var body struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	const want = `SSO session for profile "sandbox-admin" expired. agent-box has asked a human to sign in; retry after they do (or run "aws sso login --profile sandbox-admin" on the host).`
	if w.Code != 503 || body.Error != want {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if rec.rows[len(rec.rows)-1].Outcome != OutcomeSSOExpired {
		t.Fatal("not audited as sso_expired")
	}
	st, _ := b.Stats("claude-3")
	if !strings.Contains(st.LastError, "aws sso login") {
		t.Fatalf("stats.LastError %q", st.LastError)
	}
}

func TestSSOExpiredAuditKeepsTheDetailButTheBoxDoesNot(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	const detail = "refresh cached SSO token failed\nfor /home/alice/.aws/sso/cache/x.json: InvalidGrantException"
	fm.err = fmt.Errorf("mint: %w", &MintError{Code: "SSOExpired", Detail: detail, cause: ErrSSOExpired})
	w := call(b, tokA("claude-3"))
	body := w.Body.String()
	if w.Code != 503 || !strings.Contains(body, `aws sso login --profile sandbox-admin`) {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	for _, leak := range []string{"alice", "/home/", "InvalidGrant", "cached SSO"} {
		if strings.Contains(body, leak) {
			t.Fatalf("box body leaks %q: %s", leak, body)
		}
	}
	row := rec.rows[len(rec.rows)-1]
	if row.Outcome != OutcomeSSOExpired || !strings.Contains(row.Error, "InvalidGrantException") ||
		strings.Contains(row.Error, "\n") {
		t.Fatalf("audit row %+v: want sso_expired with the sanitized detail", row)
	}
	if st, _ := b.Stats("claude-3"); !strings.Contains(st.LastError, "InvalidGrantException") {
		t.Fatalf("stats.LastError %q lacks the detail", st.LastError)
	}
}

func TestSTSFailureShowsTheBoxOnlyTheCodeAndAuditsTheDetail(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	const detail = "User: arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin/alice@example.com\nis not authorized to perform sts:AssumeRole; cache /home/alice/.aws/sso/cache/x.json"
	fm.err = fmt.Errorf("mint: %w", &MintError{Code: "AccessDenied", Detail: detail})
	w := call(b, tokA("claude-3"))
	body := w.Body.String()
	if w.Code != 502 || !strings.Contains(body, "AccessDenied") || !strings.Contains(body, "trust policy") {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	for _, leak := range []string{"alice", "arn:aws:sts", "/home/", "not authorized"} {
		if strings.Contains(body, leak) {
			t.Fatalf("box body leaks %q: %s", leak, body)
		}
	}
	last := rec.rows[len(rec.rows)-1]
	if last.Outcome != OutcomeSTSError || strings.ContainsAny(last.Error, "\n\r") ||
		!strings.Contains(last.Error, "AccessDenied") || !strings.Contains(last.Error, "alice@example.com") {
		t.Fatalf("audit: %+v", last)
	}
	st, _ := b.Stats("claude-3")
	if st.Mints != 0 || !strings.Contains(st.LastError, "AccessDenied") || !strings.Contains(st.LastError, "alice@example.com") {
		t.Fatalf("stats %+v", st)
	}
}

func TestUntypedMinterErrorNeverReachesTheBox(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	fm.err = errors.New("open /home/alice/.aws/config: permission denied")
	w := call(b, tokA("claude-3"))
	if w.Code != 502 || strings.Contains(w.Body.String(), "alice") || !strings.Contains(w.Body.String(), "MintFailed") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if !strings.Contains(rec.rows[len(rec.rows)-1].Error, "permission denied") {
		t.Fatalf("audit lost the detail: %+v", rec.rows[len(rec.rows)-1])
	}
}

func TestMinterCodeIsConstrainedToAnIdentifier(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	fm.err = &MintError{Code: "AccessDenied for alice@example.com", Detail: "d"}
	if w := call(b, tokA("claude-3")); strings.Contains(w.Body.String(), "alice") {
		t.Fatalf("free text in the code reached the box: %s", w.Body)
	}
}

func TestLongMinterErrorsAreTruncated(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	fm.err = errors.New(strings.Repeat("x", 5000))
	w := call(b, tokA("claude-3"))
	if len(w.Body.String()) > 1000 || len(rec.rows[0].Error) > 1000 {
		t.Fatalf("unbounded error text: body %d, row %d", w.Body.Len(), len(rec.rows[0].Error))
	}
}

func TestOnlyPostToTheOnePath(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	r := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	r.Header.Set("Authorization", "Bearer "+tokA("claude-3"))
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/v1/approve", nil)
	w = httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown path: %d", w.Code)
	}
}

func TestNothingSecretLeaksIntoErrorsOrAuditRows(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	tok := tokA("claude-3")
	mac := tok[strings.LastIndexByte(tok, '.')+1:]
	fm.err = errors.New("AccessDenied: not authorized to perform sts:AssumeRole")
	bodies := []string{call(b, tok).Body.String(), call(b, "garbage."+nonceA+"."+mac).Body.String()}
	fm.err = nil
	call(b, tok)
	bodies = append(bodies, call(b, Token(secret, "claude-3", nonceB)).Body.String())
	// An elevated mint (and its approval row) must leak nothing either.
	eb, efm, erec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(eb)
	if _, err := eb.Elevate("aws-1", cookie(60)); err != nil {
		t.Fatal(err)
	}
	if w := call(eb, tokA("aws-1")); w.Code != 200 || efm.calls[0].RoleARN != writeRole {
		t.Fatalf("elevated mint: %d %+v", w.Code, efm.calls)
	}
	etok := tokA("aws-1")
	emac := etok[strings.LastIndexByte(etok, '.')+1:]
	var elevRows []any
	for _, r := range erec.rows {
		elevRows = append(elevRows, r)
	}
	for _, r := range erec.appr {
		elevRows = append(elevRows, r)
	}
	if len(erec.rows) == 0 || len(erec.appr) == 0 {
		t.Fatalf("want mint and approval rows, got %d and %d", len(erec.rows), len(erec.appr))
	}
	for _, r := range elevRows {
		j, _ := json.Marshal(r)
		for _, bad := range []string{emac, nonceA, "ASIAFAKE", `"s"`, `"t"`} {
			if strings.Contains(string(j), bad) {
				t.Fatalf("elevated audit row leaked %q: %s", bad, j)
			}
		}
	}
	for _, body := range bodies {
		if strings.Contains(body, mac) || strings.Contains(body, nonceA) {
			t.Fatalf("error body echoed the token: %s", body)
		}
	}
	for _, r := range rec.rows {
		j, _ := json.Marshal(r)
		for _, bad := range []string{mac, nonceA, "ASIAFAKE", `"s"`} {
			if strings.Contains(string(j), bad) {
				t.Fatalf("audit row leaked %q: %s", bad, j)
			}
		}
	}
}

func TestForgetDropsPerSessionState(t *testing.T) {
	sessions := map[string]SessionView{"claude-3": live()}
	b, _, _, _ := setup(t, sessions)
	call(b, tokA("claude-3"))
	if _, ok := b.Stats("claude-3"); !ok {
		t.Fatal("no stats after a mint")
	}
	b.Forget("claude-3")
	if _, ok := b.Stats("claude-3"); ok {
		t.Fatal("stats survived Forget")
	}
	if len(b.cache) != 0 || len(b.mintTimes) != 0 {
		t.Fatalf("state survived Forget: %d cache, %d mintTimes", len(b.cache), len(b.mintTimes))
	}
}

func TestEndedSessionStateIsDroppedOnItsNextRequest(t *testing.T) {
	sessions := map[string]SessionView{"claude-3": live()}
	b, _, _, _ := setup(t, sessions)
	tok := tokA("claude-3")
	call(b, tok)
	delete(sessions, "claude-3")
	if w := call(b, tok); w.Code != 403 {
		t.Fatalf("status %d", w.Code)
	}
	if _, ok := b.Stats("claude-3"); ok || len(b.cache) != 0 {
		t.Fatal("per-session state survived the session")
	}
}

func TestStaleNonceDoesNotWipeTheLiveSessionsState(t *testing.T) {
	// A holder of an old spawn's token must not be able to reset the live
	// session's rate-limit window by presenting it.
	sv := live()
	sv.Nonce = nonceB
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": sv})
	liveTok := Token(secret, "claude-3", nonceB)
	fm.err = errors.New("boom")
	for i := 0; i < 10; i++ {
		call(b, liveTok)
	}
	call(b, tokA("claude-3")) // stale nonce
	if w := call(b, liveTok); w.Code != 429 {
		t.Fatalf("rate-limit window was reset by a stale token: %d", w.Code)
	}
}

func TestDeniedThrottleIsPerSession(t *testing.T) {
	// Two different sessions refused in the same second each get their own row,
	// and a flood on one cannot mask the other.
	b, _, rec, _ := setup(t, map[string]SessionView{"a-1": live(), "b-1": live()})
	for i := 0; i < 20; i++ {
		call(b, Token(secret, "a-1", nonceB)) // stale nonce for a-1
	}
	call(b, Token(secret, "b-1", nonceB)) // stale nonce for b-1
	got := map[string]int{}
	for _, r := range rec.rows {
		if r.Outcome != OutcomeDeniedSession {
			t.Fatalf("unexpected row %+v", r)
		}
		got[r.SessionID]++
	}
	if len(rec.rows) != 2 || got["a-1"] != 1 || got["b-1"] != 1 {
		t.Fatalf("rows by session: %v (total %d)", got, len(rec.rows))
	}
}

func TestSuppressedRefusalsAreCountedOnTheNextRow(t *testing.T) {
	b, _, rec, now := setup(t, nil)
	const n = 7
	for i := 0; i < n; i++ {
		call(b, "garbage")
	}
	if len(rec.rows) != 1 || rec.rows[0].Error != "" {
		t.Fatalf("burst rows: %+v", rec.rows)
	}
	*now = now.Add(time.Second)
	call(b, "garbage")
	if len(rec.rows) != 2 || rec.rows[1].Error != fmt.Sprintf("+%d similar refusals suppressed", n-1) {
		t.Fatalf("rows after the window: %+v", rec.rows)
	}
	*now = now.Add(time.Second)
	call(b, "garbage") // nothing suppressed in between: no note
	if len(rec.rows) != 3 || rec.rows[2].Error != "" {
		t.Fatalf("count was not reset: %+v", rec.rows)
	}
}

func TestSuppressedCountOnAQuietKeyIsFlushed(t *testing.T) {
	b, _, rec, now := setup(t, nil)
	for i := 0; i < 4; i++ {
		call(b, "garbage")
	}
	*now = now.Add(5 * time.Second)
	// a different refusal arrives later; the quiet key's count must not be lost
	call(b, tokA("missing-1"))
	var note string
	for _, r := range rec.rows {
		if r.Outcome == OutcomeDeniedToken && r.Error != "" {
			note = r.Error
		}
	}
	if note != "+3 similar refusals suppressed" {
		t.Fatalf("flush note %q, rows %+v", note, rec.rows)
	}
}

func TestThrottleStateIsPruned(t *testing.T) {
	sessions := map[string]SessionView{}
	b, _, _, now := setup(t, sessions)
	for i := 0; i < 50; i++ {
		call(b, Token(secret, fmt.Sprintf("s-%d", i), nonceA)) // 50 distinct unknown sessions
	}
	*now = now.Add(2 * time.Second)
	call(b, "garbage")
	if len(b.denied) > 2 {
		t.Fatalf("throttle map holds %d keys after the window", len(b.denied))
	}
}

func TestRefusalReasonsAreAuditedButTheReplyIsUniform(t *testing.T) {
	notLive, host, unbound := live(), live(), live()
	notLive.Live, host.Boxed, unbound.AWS = false, false, nil
	stale := live()
	stale.Nonce = nonceB
	b, _, rec, now := setup(t, map[string]SessionView{"dead-1": notLive, "host-1": host, "plain-1": unbound, "stale-1": stale})
	cases := []struct{ id, want string }{
		{"missing-1", "unknown session"}, {"dead-1", "not live"}, {"host-1", "not boxed"},
		{"plain-1", "no binding"}, {"stale-1", "nonce mismatch"},
	}
	var bodies []string
	for _, c := range cases {
		w := call(b, tokA(c.id))
		if w.Code != 403 {
			t.Fatalf("%s: %d", c.id, w.Code)
		}
		bodies = append(bodies, w.Body.String())
		*now = now.Add(2 * time.Second)
	}
	for i, c := range cases {
		if rec.rows[i].SessionID != c.id || rec.rows[i].Error != c.want {
			t.Errorf("%s: row %+v, want reason %q", c.id, rec.rows[i], c.want)
		}
		if bodies[i] != bodies[0] {
			t.Errorf("%s: 403 body differs from the others: %q vs %q", c.id, bodies[i], bodies[0])
		}
	}
	if len(rec.rows) != len(cases) {
		t.Fatalf("rows %d", len(rec.rows))
	}
}

// The socket the boxes can reach serves credentials and nothing else: elevation
// and sign-in are web-only, behind a signed-in browser session.
func TestBrokerSocketHasNoElevationRoute(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"s": live()})
	for _, p := range []string{"/v1/elevate", "/v1/credentials/elevate", "/v1/sso-login", "/v1/elevation", "/api/sessions/s/aws/elevate", "/"} {
		for _, m := range []string{http.MethodPost, http.MethodDelete, http.MethodGet} {
			r := httptest.NewRequest(m, p, strings.NewReader(`{"minutes":60}`))
			r.Header.Set("Authorization", "Bearer x")
			w := httptest.NewRecorder()
			b.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d, want 404", m, p, w.Code)
			}
		}
	}
}
