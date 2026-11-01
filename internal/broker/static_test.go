package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
	"github.com/culiops/agent-box/internal/secretstore"
)

const staticSecret = "tok-SECRET-1234"

// fakeResolver counts resolutions and returns staticSecret, or err.
type fakeResolver struct {
	mu    sync.Mutex
	calls int
	refs  []config.KeyRef
	err   error
}

func (f *fakeResolver) Resolve(ref config.KeyRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.refs = append(f.refs, ref)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(staticSecret), nil
}

func (f *fakeResolver) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func grafanaEntry() config.StaticToken {
	return config.StaticToken{Name: "grafana-prod", Value: config.KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"},
		Env:       []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"},
		ConfigEnv: map[string]string{"GRAFANA_URL": "https://grafana.example.com"},
		Egress:    []config.TokenEgress{{Host: "grafana.example.com"}}}
}

func staticSession(toks ...config.StaticToken) SessionView {
	v := live()
	v.AWS = nil
	v.Tokens = toks
	return v
}

func staticSetup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeResolver, *memRec, *time.Time) {
	t.Helper()
	b, _, rec, now := setup(t, sessions)
	fr := &fakeResolver{}
	b.SetResolver(fr)
	return b, fr, rec, now
}

func staticCall(b *Broker, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/static/token", strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

const grafanaBody = `{"name":"grafana-prod"}`

func TestStaticFetchHappyPath(t *testing.T) {
	e := grafanaEntry()
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(e)})
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d cache %q: %s", w.Code, w.Header().Get("Cache-Control"), w.Body)
	}
	var out struct {
		Value     string            `json:"value"`
		Env       []string          `json:"env"`
		ConfigEnv map[string]string `json:"config_env"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Value != staticSecret || len(out.Env) != 1 || out.Env[0] != e.Env[0] || out.ConfigEnv["GRAFANA_URL"] != e.ConfigEnv["GRAFANA_URL"] {
		t.Fatalf("reply: %s", w.Body)
	}
	if fr.count() != 1 || fr.refs[0] != e.Value {
		t.Fatalf("resolver calls %d refs %v", fr.count(), fr.refs)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("rows: %+v", rec.rows)
	}
	r := rec.rows[0]
	if r.Outcome != OutcomeFetched || r.Provider != ProviderStatic || r.Subject != "grafana-prod" || r.Entry != "" ||
		r.SessionID != "claude-3" || r.Profile != "c" || r.ScopeHash != e.DefHash() {
		t.Fatalf("row: %+v", r)
	}
	j, _ := json.Marshal(r)
	if strings.Contains(string(j), staticSecret) {
		t.Fatalf("the audit row holds the value: %s", j)
	}
}

func TestStaticDeniedScopeNoResolve(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	w := staticCall(b, tokA("claude-3"), `{"name":"sentry-prod"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `this session has no token entry named \"sentry-prod\"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if fr.count() != 0 {
		t.Fatalf("resolved %d times for an unbound name", fr.count())
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope || rec.rows[0].Provider != ProviderStatic {
		t.Fatalf("rows: %+v", rec.rows)
	}
}

func TestStaticUnresolvedNoContents(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	msg := `env var "GRAFANA_SA_TOKEN" is not set in the agent-box daemon's environment`
	fr.err = errors.New(msg)
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "GRAFANA_SA_TOKEN") ||
		strings.Contains(w.Body.String(), `"value"`) || strings.Contains(w.Body.String(), staticSecret) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeUnresolved || rec.rows[0].Error != msg ||
		rec.rows[0].Provider != ProviderStatic || rec.rows[0].Subject != "grafana-prod" {
		t.Fatalf("rows: %+v", rec.rows)
	}
}

func TestStaticFetchedRowFailClosed(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	rec.recErr = errors.New("disk full")
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), staticSecret) ||
		!strings.Contains(w.Body.String(), "could not audit this token fetch") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	rec.recErr = nil
	w = staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != 200 || fr.count() != 2 || len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeFetched {
		t.Fatalf("after recovery: status %d, resolves %d, rows %+v", w.Code, fr.count(), rec.rows)
	}
}

func TestStaticRateBucketSeparate(t *testing.T) {
	both := staticSession(grafanaEntry())
	both.AWS = binding()
	b, fr, _, _ := staticSetup(t, map[string]SessionView{"claude-3": both})
	tok := tokA("claude-3")
	for i := 0; i < staticFetchesPerMinute; i++ {
		if w := staticCall(b, tok, grafanaBody); w.Code != 200 {
			t.Fatalf("fetch %d: status %d: %s", i+1, w.Code, w.Body)
		}
	}
	w := staticCall(b, tok, grafanaBody)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "more than 60 token fetches in a minute; slow down") {
		t.Fatalf("61st fetch: status %d: %s", w.Code, w.Body)
	}
	if fr.count() != staticFetchesPerMinute {
		t.Fatalf("resolved %d times, want %d", fr.count(), staticFetchesPerMinute)
	}
	if w := call(b, tok); w.Code != 200 { // an AWS mint in the same minute is unaffected
		t.Fatalf("AWS mint after a static burst: status %d: %s", w.Code, w.Body)
	}

	// And the other way round: ten AWS mints leave static fetches alone.
	b2, fm2, _, _ := setup(t, map[string]SessionView{"claude-3": both})
	b2.SetResolver(&fakeResolver{})
	fm2.err = errors.New("boom") // nothing cached, so every call mints
	for i := 0; i < 12; i++ {
		call(b2, tok)
	}
	if len(fm2.calls) != mintsPerMinute {
		t.Fatalf("minted %d times, want %d", len(fm2.calls), mintsPerMinute)
	}
	if w := staticCall(b2, tok, grafanaBody); w.Code != 200 {
		t.Fatalf("static fetch after exhausting the mint bucket: status %d: %s", w.Code, w.Body)
	}
}

func TestForgetClearsBothBuckets(t *testing.T) {
	b, _, _, now := staticSetup(t, map[string]SessionView{})
	b.mu.Lock()
	for i := 0; i < mintsPerMinute; i++ {
		b.allowLocked("s", bucketMint, *now)
	}
	for i := 0; i < staticFetchesPerMinute; i++ {
		b.allowLocked("s", bucketStatic, *now)
	}
	full := !b.allowLocked("s", bucketMint, *now) && !b.allowLocked("s", bucketStatic, *now)
	b.mu.Unlock()
	if !full {
		t.Fatal("the buckets did not fill")
	}
	b.Forget("s")
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.allowLocked("s", bucketMint, *now) || !b.allowLocked("s", bucketStatic, *now) {
		t.Fatal("a bucket survived Forget")
	}
}

func TestStaticRefusesAWSOnlySession(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": live()})
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "this session is not a live boxed session with a token binding") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if fr.count() != 0 || len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedSession || rec.rows[0].Provider != ProviderStatic {
		t.Fatalf("resolves %d rows %+v", fr.count(), rec.rows)
	}
}

func TestStaticDeletedEntryDeniedScope(t *testing.T) {
	// The lookup filters entries deleted from config out of Tokens (it keeps the
	// session's other entries), so the name is simply absent.
	other := grafanaEntry()
	other.Name = "sentry-prod"
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(other)})
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != 403 || fr.count() != 0 || len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope {
		t.Fatalf("status %d resolves %d rows %+v", w.Code, fr.count(), rec.rows)
	}
}

func TestStaticBodyTooLarge(t *testing.T) {
	b, fr, _, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	body := `{"name":"` + strings.Repeat("a", maxStaticBody) + `"}`
	if w := staticCall(b, tokA("claude-3"), body); w.Code != http.StatusRequestEntityTooLarge || fr.count() != 0 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestStaticRejectsBadRequests(t *testing.T) {
	b, fr, _, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	for _, body := range []string{``, `not json`, `{}`, `{"name":"Grafana Prod"}`, `{"name":"../x"}`} {
		if w := staticCall(b, tokA("claude-3"), body); w.Code != 400 {
			t.Fatalf("body %q: status %d: %s", body, w.Code, w.Body)
		}
	}
	if w := staticCall(b, "", grafanaBody); w.Code != 401 {
		t.Fatalf("no token: status %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/static/token", nil)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || fr.count() != 0 {
		t.Fatalf("GET: status %d resolves %d", w.Code, fr.count())
	}
}

func TestStaticValueNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	tok := tokA("claude-3")
	staticCall(b, tok, grafanaBody)                                         // happy path
	staticCall(b, tok, `{"name":"sentry-prod"}`)                            // denied scope
	staticCall(b, "", grafanaBody)                                          // denied token
	staticCall(b, tok, `{"name":"`+strings.Repeat("a", maxStaticBody)+`"}`) // too large
	rec.recErr = errors.New("disk full")
	staticCall(b, tok, grafanaBody) // audit failure
	rec.recErr = nil
	fr.err = errors.New(`env var "GRAFANA_SA_TOKEN" is not set in the agent-box daemon's environment`)
	staticCall(b, tok, grafanaBody) // unresolved
	if buf.Len() == 0 {
		t.Fatal("expected the audit failure to be logged")
	}
	if strings.Contains(buf.String(), staticSecret) {
		t.Fatalf("the value reached the log: %s", buf.String())
	}
	for _, r := range rec.rows {
		if j, _ := json.Marshal(r); strings.Contains(string(j), staticSecret) {
			t.Fatalf("the value reached an audit row: %s", j)
		}
	}
}

const wantStoreLockedMsg = "agent-box store is locked — ask the operator to unlock it"

// A locked store is a fixed message to the box and a store_locked row, with the
// reference's own error text kept out of both. The flag is per session and the
// next successful fetch clears it.
func TestStaticLockedStore(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry()), "other": staticSession(grafanaEntry())})
	fr.err = fmt.Errorf("store value grafana-prod: %w", credref.ErrLocked)
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), wantStoreLockedMsg) || strings.Contains(w.Body.String(), "grafana-prod") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeStoreLocked || rec.rows[0].Error != wantStoreLockedMsg ||
		rec.rows[0].Provider != ProviderStatic || rec.rows[0].Subject != "grafana-prod" {
		t.Fatalf("rows: %+v", rec.rows)
	}
	if !b.StoreLockedFor("claude-3") || b.StoreLockedFor("other") {
		t.Fatalf("flag: claude-3=%v other=%v", b.StoreLockedFor("claude-3"), b.StoreLockedFor("other"))
	}
	fr.err = nil
	if w := staticCall(b, tokA("claude-3"), grafanaBody); w.Code != 200 {
		t.Fatalf("after unlock: %d %s", w.Code, w.Body)
	}
	if b.StoreLockedFor("claude-3") {
		t.Fatal("a successful fetch must clear the flag")
	}
}

// The flag is dropped with the session's state (no unbounded growth).
func TestStoreLockedFlagForgotten(t *testing.T) {
	b, fr, _, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	fr.err = credref.ErrLocked
	staticCall(b, tokA("claude-3"), grafanaBody)
	if !b.StoreLockedFor("claude-3") {
		t.Fatal("flag not set")
	}
	b.Forget("claude-3")
	if b.StoreLockedFor("claude-3") || len(b.storeLocked) != 0 {
		t.Fatalf("flag survived Forget: %v", b.storeLocked)
	}
}

// A refusal that lands after the session is gone must not leave a flag behind.
func TestStoreLockedFlagNotSetForGoneSession(t *testing.T) {
	sessions := map[string]SessionView{"claude-3": staticSession(grafanaEntry())}
	b, _, _, _ := staticSetup(t, sessions)
	b.SetResolver(resolverFunc(func(config.KeyRef) ([]byte, error) {
		delete(sessions, "claude-3") // the session ends while the store is being read
		return nil, credref.ErrLocked
	}))
	staticCall(b, tokA("claude-3"), grafanaBody)
	if len(b.storeLocked) != 0 {
		t.Fatalf("flag for a gone session: %v", b.storeLocked)
	}
}

type resolverFunc func(config.KeyRef) ([]byte, error)

func (f resolverFunc) Resolve(r config.KeyRef) ([]byte, error) { return f(r) }

func unavailableAccess() *secretstore.UnavailableError {
	return &secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "User interaction is not allowed"}
}

// A Keychain failure is its own outcome: the box gets the fix (503), the audit
// row says store_unavailable, and the session is NOT flagged as store-locked
// (nobody can type a passphrase).
func TestStaticUnavailableStore(t *testing.T) {
	b, fr, rec, _ := staticSetup(t, map[string]SessionView{"claude-3": staticSession(grafanaEntry())})
	ue := unavailableAccess()
	fr.err = fmt.Errorf("store value grafana-prod: %w", ue)
	w := staticCall(b, tokA("claude-3"), grafanaBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), ue.Fix()) || strings.Contains(w.Body.String(), "grafana-prod") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeStoreUnavailable || !strings.Contains(rec.rows[0].Error, ue.Fix()) ||
		rec.rows[0].Provider != ProviderStatic {
		t.Fatalf("rows: %+v", rec.rows)
	}
	if b.StoreLockedFor("claude-3") {
		t.Fatal("an unavailable Keychain must not flag the session store-locked")
	}
	fr.err = nil
	if w := staticCall(b, tokA("claude-3"), grafanaBody); w.Code != 200 {
		t.Fatalf("after fix: %d %s", w.Code, w.Body)
	}
}
