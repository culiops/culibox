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

	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/config"
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

// callCount is the number of Mint calls so far, safe while mints run.
func (f *fakeMinter) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type memRec struct {
	mu      sync.Mutex
	rows    []Row
	appr    []ApprovalRow
	apprErr error
	recErr  error // fails every Record while set
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
	if m.recErr != nil {
		return m.recErr
	}
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

// testBox is the box the test sessions run in and the test requests come from.
const testBox = "ab-box-0123456789ab"

// withTestBox puts a view in testBox unless the test chose a box for it.
func withTestBox(l Lookup) Lookup {
	return func(id string) (SessionView, bool) {
		sv, ok := l(id)
		if ok && sv.Box == "" {
			sv.Box = testBox
		}
		return sv, ok
	}
}

func setup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeMinter, *memRec, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	fm := &fakeMinter{now: func() time.Time { return now }}
	rec := &memRec{}
	b := New(secret, withTestBox(func(id string) (SessionView, bool) { s, ok := sessions[id]; return s, ok }), fm, rec)
	b.now = func() time.Time { return now }
	return b, fm, rec, &now
}

func call(b *Broker, tok string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/credentials", nil)
	r = r.WithContext(withBox(r.Context(), testBox)) // arrives on testBox's own socket
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

// A credential with no audit row is never handed out: the minted row is
// written before the credential leaves, and a failed write fails closed
// without caching, so the next request mints and audits afresh.
func TestUnauditedMintIsNotHandedOut(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	rec.recErr = errors.New("disk full")
	w := call(b, tokA("claude-3"))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	st, _ := b.Stats("claude-3")
	if st.Mints != 0 || st.LastError == "" {
		t.Fatalf("stats %+v", st)
	}
	rec.recErr = nil
	w = call(b, tokA("claude-3"))
	if w.Code != 200 || len(fm.calls) != 2 || len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeMinted {
		t.Fatalf("after recovery: status %d, mints %d, rows %+v", w.Code, len(fm.calls), rec.rows)
	}
	if st, _ = b.Stats("claude-3"); st.Mints != 1 || st.LastError != "" {
		t.Fatalf("stats after recovery %+v", st)
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

// --- #155: per-connection credentials ---------------------------------------

const (
	connRoleA  = "arn:aws:iam::111111111111:role/agentbox-a"
	connWriteA = "arn:aws:iam::111111111111:role/agentbox-a-write"
	connRoleB  = "arn:aws:iam::222222222222:role/agentbox-b"
)

func connA() config.AWSConnection {
	return config.AWSConnection{Name: "a", AWSBinding: config.AWSBinding{SourceProfile: "prof-a", RoleARN: connRoleA,
		Region: "ap-southeast-1", S3Read: []string{"bucket-a"}, Elevate: &config.AWSElevate{RoleARN: connWriteA}}}
}

func connB() config.AWSConnection {
	return config.AWSConnection{Name: "b", AWSBinding: config.AWSBinding{SourceProfile: "prof-b", RoleARN: connRoleB,
		Region: "us-east-1", S3Read: []string{"bucket-b"}}}
}

func liveConns() SessionView {
	return SessionView{Agent: "c", Nonce: nonceA, Boxed: true, Live: true, Exclusive: true,
		AWSConns: []config.AWSConnection{connA(), connB()}}
}

func callBody(b *Broker, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/credentials", strings.NewReader(body))
	r = r.WithContext(withBox(r.Context(), testBox))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

func TestCredentialsLegacyEmptyBodyUnchanged(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": live()})
	for _, body := range []string{"", "  \n"} {
		if w := callBody(b, tokA("claude-3"), body); w.Code != 200 {
			t.Fatalf("body %q: status %d: %s", body, w.Code, w.Body)
		}
	}
	if len(fm.calls) != 1 { // the second was a cache hit under the legacy key
		t.Fatalf("minted %d times", len(fm.calls))
	}
	// The legacy key, bucket and scope are what they were before connections.
	c, ok := b.cache[connKey{"claude-3", ""}]
	if !ok || len(b.cache) != 1 || c.scopeKey != binding().Hash() {
		t.Fatalf("cache %+v", b.cache)
	}
	if got := len(b.mintTimes[bucketKey{"claude-3", bucketMint, ""}]); got != 1 || len(b.mintTimes) != 1 {
		t.Fatalf("buckets %+v", b.mintTimes)
	}
	if rec.rows[0].ScopeHash != binding().Hash() || rec.rows[0].Entry != "" {
		t.Fatalf("audit %+v", rec.rows[0])
	}
	if cs := b.ConnStats("claude-3"); len(cs) != 0 {
		t.Fatalf("legacy mint filled connStats: %+v", cs)
	}
}

func TestCredentialsConnByName(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": liveConns()})
	for _, n := range []string{"a", "b"} {
		if w := callBody(b, tokA("claude-3"), `{"name":"`+n+`"}`); w.Code != 200 {
			t.Fatalf("%s: status %d: %s", n, w.Code, w.Body)
		}
	}
	if len(fm.calls) != 2 {
		t.Fatalf("calls %d", len(fm.calls))
	}
	for i, c := range []config.AWSConnection{connA(), connB()} {
		in := fm.calls[i]
		pol, _ := awsscope.SessionPolicy("aws", c.S3Read)
		if in.RoleARN != c.RoleARN || in.Region != c.Region || in.SourceProfile != c.SourceProfile ||
			in.SessionName != "agentbox-claude-3" || in.Policy != pol || in.Duration != credTTL {
			t.Fatalf("%s mint input: %+v", c.Name, in)
		}
		row := rec.rows[i]
		if row.Entry != c.Name || row.Outcome != OutcomeMinted || row.RoleARN != c.RoleARN ||
			row.AccountID != awsscope.AccountOf(c.RoleARN) || row.SourceIdentity != "agentbox-claude-3" ||
			row.ScopeHash != c.DefHash() || row.Elevated {
			t.Fatalf("%s audit: %+v", c.Name, row)
		}
	}
	st, _ := b.Stats("claude-3")
	cs := b.ConnStats("claude-3")
	if st.Mints != 2 || cs["a"].Mints != 1 || cs["b"].Mints != 1 || len(cs) != 2 {
		t.Fatalf("stats %+v conn %+v", st, cs)
	}
	cs["a"] = Stats{Mints: 99} // a copy: writing it changes nothing in the broker
	if b.ConnStats("claude-3")["a"].Mints != 1 {
		t.Fatal("ConnStats returned the live map")
	}
}

func TestCredentialsConnEmptyBodyRefused(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": liveConns()})
	w := callBody(b, tokA("claude-3"), "")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "agentbox-env --list") || len(fm.calls) != 0 {
		t.Fatalf("status %d body %s calls %d", w.Code, w.Body, len(fm.calls))
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope {
		t.Fatalf("audit %+v", rec.rows)
	}
}

func TestCredentialsConnUnboundName(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": liveConns(), "old-1": live()})
	w := callBody(b, tokA("claude-3"), `{"name":"zz"}`)
	if w.Code != 403 || len(fm.calls) != 0 {
		t.Fatalf("status %d calls %d", w.Code, len(fm.calls))
	}
	if r := rec.rows[len(rec.rows)-1]; r.Outcome != OutcomeDeniedScope || r.Entry != "zz" || r.SessionID != "claude-3" {
		t.Fatalf("audit %+v", r)
	}
	*now = now.Add(2 * time.Second) // past the denied-row throttle window
	w = callBody(b, tokA("claude-3"), `{"name":"../x"}`)
	if w.Code != 403 || len(fm.calls) != 0 {
		t.Fatalf("status %d calls %d", w.Code, len(fm.calls))
	}
	r := rec.rows[len(rec.rows)-1]
	if r.Outcome != OutcomeDeniedScope || r.Entry != "(invalid)" {
		t.Fatalf("audit %+v", r)
	}
	if j, _ := json.Marshal(r); strings.Contains(string(j), "../x") {
		t.Fatalf("raw name stored: %s", j)
	}
	// A legacy session has no connections: any name is unbound.
	*now = now.Add(2 * time.Second)
	w = callBody(b, tokA("old-1"), `{"name":"a"}`)
	if w.Code != 403 || len(fm.calls) != 0 || rec.rows[len(rec.rows)-1].Outcome != OutcomeDeniedScope {
		t.Fatalf("legacy session with a name: status %d calls %d", w.Code, len(fm.calls))
	}
}

func TestCredentialsUnknownFieldRefused(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": liveConns()})
	for _, body := range []string{`{"name":"a","role_arn":"arn:aws:iam::1:role/x"}`, `{"name":`, `{"name":"a"} {"name":"b"}`, `[]`} {
		if w := callBody(b, tokA("claude-3"), body); w.Code != 400 || len(fm.calls) != 0 {
			t.Fatalf("%s: status %d calls %d", body, w.Code, len(fm.calls))
		}
	}
	big := `{"name":"a","x":"` + strings.Repeat("y", maxStaticBody) + `"}`
	if w := callBody(b, tokA("claude-3"), big); w.Code != http.StatusRequestEntityTooLarge || len(fm.calls) != 0 {
		t.Fatalf("oversized: status %d calls %d", w.Code, len(fm.calls))
	}
}

func TestConnCacheIsolated(t *testing.T) {
	b, fm, _, _ := setup(t, map[string]SessionView{"claude-3": liveConns()})
	for _, n := range []string{"a", "b", "a", "b"} {
		if w := callBody(b, tokA("claude-3"), `{"name":"`+n+`"}`); w.Code != 200 {
			t.Fatalf("%s: %d", n, w.Code)
		}
	}
	if len(fm.calls) != 2 {
		t.Fatalf("minted %d times; want 2 (a, b; the repeats are cache hits)", len(fm.calls))
	}
	st, _ := b.Stats("claude-3")
	if st.CacheHits != 2 || b.ConnStats("claude-3")["a"].CacheHits != 1 {
		t.Fatalf("stats %+v %+v", st, b.ConnStats("claude-3"))
	}
}

// Review Focus 1: an elevated entry in one connection's cache is never served
// to the other, and the other mints with its own role and read-only policy.
func TestConnCacheIsolatedUnderElevation(t *testing.T) {
	sv := liveConns()
	// b may be elevated too (it has a write role): the window is a's, so b must
	// still mint read-only. Without that, only b's missing role would save it.
	sv.AWSConns[1].Elevate = &config.AWSElevate{RoleARN: "arn:aws:iam::222222222222:role/agentbox-b-write"}
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": sv})
	b.elev["claude-3"] = &elevation{until: now.Add(time.Hour), grantedAt: *now, gen: 1, conn: "a", timer: &fakeTimer{}}
	if w := callBody(b, tokA("claude-3"), `{"name":"a"}`); w.Code != 200 {
		t.Fatalf("a: %d %s", w.Code, w.Body)
	}
	if w := callBody(b, tokA("claude-3"), `{"name":"b"}`); w.Code != 200 {
		t.Fatalf("b: %d %s", w.Code, w.Body)
	}
	if len(fm.calls) != 2 {
		t.Fatalf("calls %d", len(fm.calls))
	}
	ca, cb := connA(), connB()
	elevPol, _ := awsscope.ElevatedPolicy("aws", ca.S3Read, false)
	readPol, _ := awsscope.SessionPolicy("aws", cb.S3Read)
	if fm.calls[0].RoleARN != connWriteA || fm.calls[0].Policy != elevPol || fm.calls[0].SourceProfile != "prof-a" {
		t.Fatalf("a minted %+v", fm.calls[0])
	}
	if fm.calls[1].RoleARN != connRoleB || fm.calls[1].Policy != readPol || fm.calls[1].Region != "us-east-1" {
		t.Fatalf("b minted %+v", fm.calls[1])
	}
	if !rec.rows[0].Elevated || rec.rows[1].Elevated {
		t.Fatalf("elevated flags: %+v", rec.rows)
	}
	// Both are now cached under their own keys; repeats mint nothing.
	callBody(b, tokA("claude-3"), `{"name":"b"}`)
	callBody(b, tokA("claude-3"), `{"name":"a"}`)
	if len(fm.calls) != 2 {
		t.Fatalf("repeat minted: %d calls", len(fm.calls))
	}
	if ka, kb := b.cache[connKey{"claude-3", "a"}], b.cache[connKey{"claude-3", "b"}]; ka.scopeKey == kb.scopeKey ||
		!strings.Contains(ka.scopeKey, "/e") || strings.Contains(kb.scopeKey, "/e") {
		t.Fatalf("scope keys a=%q b=%q", ka.scopeKey, kb.scopeKey)
	}
}

func TestConnRateBucketPerName(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": liveConns()})
	fm.exp = func(MintInput) time.Time { return now.Add(time.Minute) } // never a cache hit
	for i := 0; i < mintsPerMinute; i++ {
		if w := callBody(b, tokA("claude-3"), `{"name":"a"}`); w.Code != 200 {
			t.Fatalf("mint %d: %d", i, w.Code)
		}
	}
	if w := callBody(b, tokA("claude-3"), `{"name":"a"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th a: %d", w.Code)
	}
	if r := rec.rows[len(rec.rows)-1]; r.Outcome != OutcomeRateLimited || r.Entry != "a" {
		t.Fatalf("audit %+v", r)
	}
	if w := callBody(b, tokA("claude-3"), `{"name":"b"}`); w.Code != 200 {
		t.Fatalf("b after a is limited: %d", w.Code)
	}
	if len(b.mintTimes[bucketKey{"claude-3", bucketAWSConn, "a"}]) != mintsPerMinute || len(b.mintTimes[bucketKey{"claude-3", bucketMint, ""}]) != 0 {
		t.Fatalf("buckets %+v", b.mintTimes)
	}
}

func TestConnMintFailureIsAttributed(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"claude-3": liveConns()})
	fm.err = fmt.Errorf("%w: token expired", ErrSSOExpired)
	if w := callBody(b, tokA("claude-3"), `{"name":"b"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
	if r := rec.rows[0]; r.Outcome != OutcomeSSOExpired || r.Entry != "b" {
		t.Fatalf("audit %+v", r)
	}
	if _, ok := b.ssoNeed[connKey{"claude-3", "b"}]; !ok || len(b.ssoNeed) != 1 {
		t.Fatalf("ssoNeed %+v", b.ssoNeed)
	}
	if b.ConnStats("claude-3")["b"].LastError == "" || b.ConnStats("claude-3")["a"].LastError != "" {
		t.Fatalf("conn stats %+v", b.ConnStats("claude-3"))
	}
}

func TestForgetClearsConnState(t *testing.T) {
	b, fm, _, now := setup(t, map[string]SessionView{"claude-3": liveConns(), "other-1": liveConns()})
	fm.exp = func(MintInput) time.Time { return now.Add(time.Hour) }
	for _, sid := range []string{"claude-3", "other-1"} {
		for _, n := range []string{"a", "b"} {
			callBody(b, tokA(sid), `{"name":"`+n+`"}`)
		}
	}
	b.mu.Lock()
	b.ssoNeed[connKey{"claude-3", "a"}] = "k"
	b.mu.Unlock()
	b.Forget("claude-3")
	if len(b.cache) != 2 || len(b.connStats) != 2 || len(b.mintTimes) != 2 || len(b.ssoNeed) != 0 {
		t.Fatalf("after Forget: cache %d connStats %d mintTimes %d ssoNeed %d", len(b.cache), len(b.connStats), len(b.mintTimes), len(b.ssoNeed))
	}
	for k := range b.cache {
		if k.sid == "claude-3" {
			t.Fatalf("cache kept %+v", k)
		}
	}
	if len(b.ConnStats("claude-3")) != 0 || len(b.ConnStats("other-1")) != 2 {
		t.Fatal("ConnStats after Forget")
	}
}

// Two connections that both hit their own rate limit inside one throttle window
// each get their own row: a row must say which connection it was for.
func TestRateLimitedRowsKeepTheirOwnConnection(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"claude-3": liveConns()})
	fm.exp = func(MintInput) time.Time { return now.Add(time.Minute) } // never a cache hit
	for _, name := range []string{"a", "b"} {
		for i := 0; i < mintsPerMinute; i++ {
			if w := callBody(b, tokA("claude-3"), `{"name":"`+name+`"}`); w.Code != 200 {
				t.Fatalf("%s mint %d: %d", name, i, w.Code)
			}
		}
	}
	for _, name := range []string{"a", "b", "a", "b"} {
		if w := callBody(b, tokA("claude-3"), `{"name":"`+name+`"}`); w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s over the limit: %d", name, w.Code)
		}
	}
	var entries []string
	for _, r := range rec.rows {
		if r.Outcome == OutcomeRateLimited {
			entries = append(entries, r.Entry)
		}
	}
	if len(entries) != 2 || entries[0] != "a" || entries[1] != "b" {
		t.Fatalf("rate-limited rows name %v, want [a b]", entries)
	}
	*now = now.Add(time.Second)
	callBody(b, tokA("claude-3"), `{"name":"a"}`)
	callBody(b, tokA("claude-3"), `{"name":"b"}`)
	noted := map[string]int{} // each suppressed count rides on its own connection's row
	for _, r := range rec.rows[2:] {
		if strings.Contains(r.Error, "+1 similar refusals suppressed") {
			noted[r.Entry]++
		}
	}
	if noted["a"] != 1 || noted["b"] != 1 {
		t.Fatalf("suppressed notes by entry %v, rows %+v", noted, rec.rows)
	}
}

// Refusals of names that are not bound are not keyed by name: an agent cycling
// made-up names must not be able to flood the audit log.
func TestUnboundNameRefusalsStillCollapse(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"claude-3": liveConns()})
	for _, name := range []string{"x1", "x2", "x3", "x4"} {
		if w := callBody(b, tokA("claude-3"), `{"name":"`+name+`"}`); w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope {
		t.Fatalf("burst rows: %+v", rec.rows)
	}
	*now = now.Add(time.Second)
	callBody(b, tokA("claude-3"), `{"name":"x5"}`)
	if len(rec.rows) != 2 || !strings.Contains(rec.rows[1].Error, "+3 similar refusals suppressed") {
		t.Fatalf("rows after the window: %+v", rec.rows)
	}
}
