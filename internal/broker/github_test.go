package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/ghapp"
)

type fakeGH struct {
	mu    sync.Mutex
	calls []ghapp.MintInput
	err   error
	life  time.Duration // life of each token handed out
	now   func() time.Time
	n     int

	revoked   []string                                    // tokens Revoke was called with
	revokeErr error                                       // Revoke fails with this while set
	revokeFn  func(ctx context.Context, tok string) error // overrides revokeErr when set
	entered   chan struct{}                               // Mint signals here when it starts (if set)
	block     chan struct{}                               // Mint waits for this to close (if set)
}

func (f *fakeGH) Check(string) error { return nil }
func (f *fakeGH) Mint(_ context.Context, in ghapp.MintInput) (ghapp.Minted, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return ghapp.Minted{}, f.err
	}
	f.n++
	return ghapp.Minted{Token: ghapp.Token{Value: fmt.Sprintf("ghs_FAKETOKEN%02d", f.n), ExpiresAt: f.now().Add(f.life)},
		InstallationID: 777}, nil
}
func (f *fakeGH) Revoke(ctx context.Context, tok string) error {
	f.mu.Lock()
	f.revoked = append(f.revoked, tok)
	fn, err := f.revokeFn, f.revokeErr
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, tok)
	}
	return err
}
func (f *fakeGH) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *fakeGH) revokedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func ghBinding() *config.GitHubBinding {
	return &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", App: "bot", Repos: []string{"agent-box", "tech-advisor"}}}}
}

func ghLive() SessionView {
	return SessionView{Agent: "c", Nonce: nonceA, Boxed: true, Live: true, GitHub: ghBinding()}
}

func ghSetup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeGH, *memRec, *time.Time) {
	t.Helper()
	b, _, rec, now := setup(t, sessions)
	fg := &fakeGH{life: time.Hour, now: func() time.Time { return *now }}
	b.SetGitHub(fg)
	return b, fg, rec, now
}

func ghCall(b *Broker, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/github/token", strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

const ghBody = `{"owner":"culiops","repo":"agent-box"}`

func ghReply(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply: %v: %s", err, w.Body)
	}
	return out["token"], out["expires_at"]
}

func TestGitHubMintHappyPath(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"claude-3": ghLive()})
	w := ghCall(b, tokA("claude-3"), ghBody)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	tok, exp := ghReply(t, w)
	if tok != "ghs_FAKETOKEN01" || exp != "2027-01-15T09:00:00Z" {
		t.Fatalf("reply: %s", w.Body)
	}
	in := fg.calls[0]
	if in.App != "bot" || in.Owner != "culiops" || len(in.Repos) != 2 || in.Permissions["contents"] != "read" || len(in.Permissions) == 0 {
		t.Fatalf("mint input: %+v", in)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("rows: %+v", rec.rows)
	}
	r := rec.rows[0]
	if r.Outcome != OutcomeMinted || r.Provider != ProviderGitHub || r.Subject != "culiops" || r.Installation != 777 ||
		r.Repos != 2 || r.SessionID != "claude-3" || r.ScopeHash != ghBinding().Hash() || r.Expires.IsZero() {
		t.Fatalf("row: %+v", r)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
	if iss := b.ghIssued["claude-3"]; len(iss) != 1 || iss[0].value != tok || iss[0].owner != "culiops" || iss[0].installation != 777 {
		t.Fatalf("issued: %+v", iss)
	}
}

func TestGitHubOwnerCaseInsensitive(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	w := ghCall(b, tokA("s"), `{"owner":"CuliOps","repo":"agent-box"}`)
	if w.Code != 200 || fg.callCount() != 1 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w2 := ghCall(b, tokA("s"), `{"owner":"culiops"}`); w2.Code != 200 || fg.callCount() != 1 { // same cache key, repo optional
		t.Fatalf("second: %d %s calls=%d", w2.Code, w2.Body, fg.callCount())
	}
}

func TestGitHubDeniedScopeNoCall(t *testing.T) {
	for name, body := range map[string]string{
		"unknown owner": `{"owner":"other","repo":"agent-box"}`,
		"unknown repo":  `{"owner":"culiops","repo":"secret-repo"}`,
		"bad owner":     `{"owner":"../x","repo":"agent-box"}`,
		"bad repo":      `{"owner":"culiops","repo":".."}`,
	} {
		t.Run(name, func(t *testing.T) {
			b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
			w := ghCall(b, tokA("s"), body)
			if w.Code != 403 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if fg.callCount() != 0 {
				t.Fatal("GitHub was called")
			}
			if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope || rec.rows[0].Provider != ProviderGitHub {
				t.Fatalf("rows: %+v", rec.rows)
			}
		})
	}
}

func TestGitHubBadRequests(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"not json":   {`nope`, 400},
		"no owner":   {`{"repo":"agent-box"}`, 400},
		"oversize":   {`{"owner":"culiops","pad":"` + strings.Repeat("x", 5000) + `"}`, 413},
		"empty body": {``, 400},
	} {
		if w := ghCall(b, tokA("s"), c.body); w.Code != c.want {
			t.Errorf("%s: status %d want %d: %s", name, w.Code, c.want, w.Body)
		}
	}
	if fg.callCount() != 0 {
		t.Fatal("GitHub was called")
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/github/token", nil)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := ghCall(b, "", ghBody); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
}

func TestGitHubErrorOutcomes(t *testing.T) {
	for name, c := range map[string]struct {
		err     error
		status  int
		outcome Outcome
	}{
		"not installed": {ghapp.ErrNotInstalled, 403, OutcomeNotInstalled},
		"scope":         {&ghapp.ScopeError{Msg: "repo x not accessible"}, 403, OutcomeScopeError},
		"generic":       {errors.New("boom"), 502, OutcomeGitHubError},
	} {
		t.Run(name, func(t *testing.T) {
			b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
			fg.err = fmt.Errorf("wrapped: %w", c.err)
			w := ghCall(b, tokA("s"), ghBody)
			if w.Code != c.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(rec.rows) != 1 || rec.rows[0].Outcome != c.outcome || rec.rows[0].Subject != "culiops" || rec.rows[0].Provider != ProviderGitHub {
				t.Fatalf("rows: %+v", rec.rows)
			}
			if len(b.ghIssued["s"]) != 0 || len(b.ghCache) != 0 {
				t.Fatal("failed mint tracked or cached")
			}
			switch c.outcome {
			case OutcomeNotInstalled:
				if !strings.Contains(w.Body.String(), "install the GitHub App on culiops") {
					t.Fatalf("body: %s", w.Body)
				}
			case OutcomeScopeError:
				if !strings.Contains(w.Body.String(), "repo x not accessible") {
					t.Fatalf("body: %s", w.Body)
				}
			}
		})
	}
}

func TestGitHubAPIErrorRecorded(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	fg.err = &ghapp.APIError{Status: 0, Msg: "dial tcp: lookup api.github.com: no such host"}
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 502 {
		t.Fatalf("status %d", w.Code)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeGitHubError || !strings.Contains(rec.rows[0].Error, "no such host") ||
		strings.Contains(rec.rows[0].Error, "ghs_") || strings.Contains(w.Body.String(), "ghs_") {
		t.Fatalf("rows: %+v body %s", rec.rows, w.Body)
	}
	fg.err = &ghapp.APIError{Status: 500, Msg: strings.Repeat("e", 2000)}
	b.mu.Lock()
	b.mintTimes = map[bucketKey][]time.Time{}
	b.mu.Unlock()
	ghCall(b, tokA("s"), ghBody)
	if n := len(rec.rows[len(rec.rows)-1].Error); n > maxErrLen+8 {
		t.Fatalf("error row not capped: %d", n)
	}
}

func TestGitHubMintedRowFailClosed(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	rec.recErr = errors.New("disk full")
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 503 || strings.Contains(w.Body.String(), "ghs_") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(b.ghCache) != 0 || len(b.ghIssued["s"]) != 0 {
		t.Fatalf("cache %v issued %v", b.ghCache, b.ghIssued)
	}
	b.revokeWG.Wait()
	if got := fg.revokedList(); len(got) != 1 || got[0] != "ghs_FAKETOKEN01" {
		t.Fatalf("the withheld token must be revoked: %v", got)
	}
	rec.recErr = nil
	if w := ghCall(b, tokA("s"), ghBody); w.Code != 200 || fg.callCount() != 2 {
		t.Fatalf("retry: %d calls=%d", w.Code, fg.callCount())
	}
}

func TestGitHubCacheReuseAndRefresh(t *testing.T) {
	b, fg, _, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
	t1, _ := ghReply(t, ghCall(b, tokA("s"), ghBody))
	*now = now.Add(10 * time.Minute) // 50 min left
	t2, _ := ghReply(t, ghCall(b, tokA("s"), ghBody))
	if t1 != t2 || fg.callCount() != 1 {
		t.Fatalf("expected reuse: %s %s calls=%d", t1, t2, fg.callCount())
	}
	*now = now.Add(41 * time.Minute) // 9 min left
	t3, _ := ghReply(t, ghCall(b, tokA("s"), ghBody))
	if t3 == t1 || fg.callCount() != 2 {
		t.Fatalf("expected re-mint: %s calls=%d", t3, fg.callCount())
	}
	if iss := b.ghIssued["s"]; len(iss) != 2 {
		t.Fatalf("superseded token must stay tracked: %+v", iss)
	}
}

func TestGitHubCacheKeyedOnNonceAndScope(t *testing.T) {
	sessions := map[string]SessionView{"s": ghLive()}
	b, fg, _, _ := ghSetup(t, sessions)
	ghCall(b, tokA("s"), ghBody)
	sv := sessions["s"]
	sv.Nonce = nonceB // the id was reused by a new spawn
	sessions["s"] = sv
	if w := ghCall(b, Token(secret, "s", nonceB), ghBody); w.Code != 200 || fg.callCount() != 2 {
		t.Fatalf("new spawn reused the old token: %d calls=%d", w.Code, fg.callCount())
	}
	nb := *ghBinding()
	nb.Owners = []config.GitHubOwner{{Owner: "culiops", App: "bot", Repos: []string{"agent-box"}}}
	sv.GitHub = &nb // binding edited
	sessions["s"] = sv
	if w := ghCall(b, Token(secret, "s", nonceB), ghBody); w.Code != 200 || fg.callCount() != 3 {
		t.Fatalf("edited binding reused the old token: %d calls=%d", w.Code, fg.callCount())
	}
}

func TestGitHubConcurrentMintsAllTracked(t *testing.T) {
	b, _, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	const n = 8
	toks := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := ghCall(b, tokA("s"), ghBody)
			if w.Code != 200 {
				t.Errorf("status %d: %s", w.Code, w.Body)
				return
			}
			toks[i], _ = ghReply(t, w)
		}()
	}
	wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	have := map[string]bool{}
	for _, it := range b.ghIssued["s"] {
		have[it.value] = true
	}
	for _, tk := range toks {
		if tk == "" || !have[tk] {
			t.Fatalf("token %q returned but not tracked: %+v", tk, b.ghIssued["s"])
		}
	}
}

func TestGitHubRateLimitShared(t *testing.T) {
	b, fg, rec, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
	fg.life = time.Minute // never cached: every call mints
	for range mintsPerMinute {
		if w := ghCall(b, tokA("s"), ghBody); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
	}
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 429 || fg.callCount() != mintsPerMinute {
		t.Fatalf("status %d calls=%d", w.Code, fg.callCount())
	}
	if last := rec.rows[len(rec.rows)-1]; last.Outcome != OutcomeRateLimited || last.Provider != ProviderGitHub {
		t.Fatalf("row: %+v", last)
	}
	*now = now.Add(2 * time.Minute)
	if w := ghCall(b, tokA("s"), ghBody); w.Code != 200 {
		t.Fatalf("after window: %d", w.Code)
	}
}

func TestGitHubRefusesAWSOnlySession(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": live()})
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 403 || fg.callCount() != 0 {
		t.Fatalf("status %d", w.Code)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedSession || rec.rows[0].Provider != ProviderGitHub {
		t.Fatalf("rows: %+v", rec.rows)
	}
}

func TestCredentialsRefusesGitHubOnlySession(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"s": ghLive()})
	w := call(b, tokA("s"))
	if w.Code != 403 || len(fm.calls) != 0 {
		t.Fatalf("status %d", w.Code)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedSession || rec.rows[0].Error != "no binding" {
		t.Fatalf("rows: %+v", rec.rows)
	}
	if !strings.Contains(w.Body.String(), "AWS binding") {
		t.Fatalf("AWS refusal text changed: %s", w.Body)
	}
}

func TestGitHubRefusalReasons(t *testing.T) {
	b, _, rec, _ := ghSetup(t, map[string]SessionView{
		"unboxed": {Nonce: nonceA, Live: true, GitHub: ghBinding()},
		"dead":    {Nonce: nonceA, Boxed: true, GitHub: ghBinding()},
	})
	for _, id := range []string{"unboxed", "dead", "ghost"} {
		if w := ghCall(b, tokA(id), ghBody); w.Code != 403 {
			t.Fatalf("%s: %d", id, w.Code)
		}
	}
	if w := ghCall(b, Token(secret, "unboxed", nonceB), ghBody); w.Code != 403 {
		t.Fatalf("nonce: %d", w.Code)
	}
	if len(rec.rows) < 3 {
		t.Fatalf("rows: %+v", rec.rows)
	}
}

func TestGitHubNotConfigured(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"s": ghLive()})
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "GitHub is not configured") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if err := b.CheckGitHub(*ghBinding()); err == nil {
		t.Fatal("CheckGitHub with no minter must fail")
	}
}

type checkGH struct {
	fakeGH
	checked []string
}

func (c *checkGH) Check(app string) error {
	c.checked = append(c.checked, app)
	if app == "bad" {
		return errors.New("no key")
	}
	return nil
}

func TestCheckGitHubDistinctApps(t *testing.T) {
	b, _, _, _ := setup(t, nil)
	c := &checkGH{}
	b.SetGitHub(c)
	g := config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "a", App: "x"}, {Owner: "b", App: "x"}, {Owner: "c", App: "y"}}}
	if err := b.CheckGitHub(g); err != nil || len(c.checked) != 2 {
		t.Fatalf("err %v checked %v", err, c.checked)
	}
	g.Owners = append(g.Owners, config.GitHubOwner{Owner: "d", App: "bad"})
	if err := b.CheckGitHub(g); err == nil {
		t.Fatal("want error")
	}
}

func TestGitHubUnusableTokenNotHandedOut(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	fg.life = -time.Minute // already expired
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 502 || strings.Contains(w.Body.String(), "ghs_") || len(b.ghIssued["s"]) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeGitHubError {
		t.Fatalf("rows: %+v", rec.rows)
	}
}

func TestGitHubForgetDropsState(t *testing.T) {
	b, _, _, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	ghCall(b, tokA("s"), ghBody)
	b.Forget("s")
	if len(b.ghCache) != 0 || len(b.ghIssued) != 0 {
		t.Fatalf("cache %v issued %v", b.ghCache, b.ghIssued)
	}
}

func TestGitHubTokenNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	b, fg, rec, now := ghSetup(t, map[string]SessionView{"s": ghLive(), "aws": live()})
	ghCall(b, tokA("s"), ghBody)
	ghCall(b, tokA("s"), `{"owner":"nobody"}`)
	ghCall(b, tokA("aws"), ghBody)
	ghCall(b, "garbage", ghBody)
	*now = now.Add(55 * time.Minute)
	rec.recErr = errors.New("disk full") // the fail-closed path logs
	ghCall(b, tokA("s"), ghBody)
	b.revokeWG.Wait() // the withheld token's revoke also touches rec
	rec.recErr = nil
	fg.err = &ghapp.APIError{Status: 500, Msg: "bad"}
	ghCall(b, tokA("s"), ghBody)
	fg.err = nil
	b.Forget("s")
	if fg.n == 0 {
		t.Fatal("no token was minted")
	}
	if strings.Contains(buf.String(), "ghs_") {
		t.Fatalf("a token reached the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "audit write failed") {
		t.Fatalf("the fail-closed path should have logged: %q", buf.String())
	}
}

// mintN mints n distinct tokens for sid by letting the cached one go stale.
func mintN(t *testing.T, b *Broker, now *time.Time, sid string, n int) []string {
	t.Helper()
	var toks []string
	start := *now
	defer func() { *now = start }() // back to the start: no listed token is past its expiry yet
	for range n {
		w := ghCall(b, tokA(sid), ghBody)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		tk, _ := ghReply(t, w)
		toks = append(toks, tk)
		*now = now.Add(55 * time.Minute) // under the refresh margin: the next call re-mints
	}
	return toks
}

func revokeRows(rec *memRec) []Row {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []Row
	for _, r := range rec.rows {
		if r.Outcome == OutcomeRevoked || r.Outcome == OutcomeRevokeFailed {
			out = append(out, r)
		}
	}
	return out
}

func TestForgetRevokesEveryIssuedToken(t *testing.T) {
	b, fg, rec, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
	toks := mintN(t, b, now, "s", 3)
	b.Forget("s")
	b.revokeWG.Wait()
	got := fg.revokedList()
	if len(got) != 3 {
		t.Fatalf("revoked %v want %v", got, toks)
	}
	for _, tk := range toks {
		found := false
		for _, g := range got {
			found = found || g == tk
		}
		if !found {
			t.Fatalf("%s not revoked: %v", tk, got)
		}
	}
	rows := revokeRows(rec)
	if len(rows) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	for _, r := range rows {
		if r.Outcome != OutcomeRevoked || r.Subject != "culiops" || r.SessionID != "s" || r.Provider != ProviderGitHub || r.Installation != 777 || r.Error != "" {
			t.Fatalf("row: %+v", r)
		}
	}
	if len(b.ghIssued) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("state left: %v %v", b.ghIssued, b.ghCache)
	}
}

func TestRevokeFailureRecorded(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	ghCall(b, tokA("s"), ghBody)
	fg.revokeErr = errors.New("boom ghs_FAKETOKEN01 " + strings.Repeat("e", 1000))
	b.RevokeGitHub("s")
	b.revokeWG.Wait()
	rows := revokeRows(rec)
	if len(rows) != 1 || rows[0].Outcome != OutcomeRevokeFailed || rows[0].Subject != "culiops" || rows[0].Error == "" {
		t.Fatalf("rows: %+v", rows)
	}
	if strings.Contains(rows[0].Error, "ghs_") || len(rows[0].Error) > maxErrLen+len("…") {
		t.Fatalf("error row: %q", rows[0].Error)
	}
}

func TestRevokeBoundedNoLeak(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"s": ghLive()})
	ghCall(b, tokA("s"), ghBody)
	fg.revokeFn = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	b.ghRevokeTimeout = 50 * time.Millisecond
	base := runtime.NumGoroutine()
	start := time.Now()
	b.RevokeGitHub("s")
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Fatalf("RevokeGitHub blocked %v", d)
	}
	done := make(chan struct{})
	go func() { b.revokeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("revoke goroutine outlived its timeout")
	}
	if rows := revokeRows(rec); len(rows) != 1 || rows[0].Outcome != OutcomeRevokeFailed {
		t.Fatalf("rows: %+v", rows)
	}
	time.Sleep(50 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > base+1 {
		t.Fatalf("goroutines %d, baseline %d", n, base)
	}
}

func TestRevokeAllGitHubRespectsCtx(t *testing.T) {
	b, fg, _, _ := ghSetup(t, map[string]SessionView{"s1": ghLive(), "s2": ghLive()})
	ghCall(b, tokA("s1"), ghBody)
	ghCall(b, tokA("s2"), ghBody)
	fg.revokeFn = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	b.RevokeAllGitHub(ctx)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("RevokeAllGitHub took %v", d)
	}
	if len(b.ghIssued) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("state left: %v %v", b.ghIssued, b.ghCache)
	}
}

func TestRevokeTwiceIsNoop(t *testing.T) {
	b, fg, _, now := ghSetup(t, map[string]SessionView{"s": ghLive()})
	mintN(t, b, now, "s", 2)
	b.RevokeGitHub("s")
	b.Forget("s")
	b.RevokeGitHub("s")
	b.revokeWG.Wait()
	if got := fg.revokedList(); len(got) != 2 || got[0] == got[1] {
		t.Fatalf("each token exactly once: %v", got)
	}
}

// inFlightSetup starts a request for session "s" and returns once the fake
// Mint is blocked (outside b.mu); finish releases it and returns the reply.
func inFlightSetup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeGH, *memRec, func() *httptest.ResponseRecorder) {
	b, fg, rec, _ := ghSetup(t, sessions)
	fg.entered, fg.block = make(chan struct{}), make(chan struct{})
	var w *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() { w = ghCall(b, tokA("s"), ghBody); close(done) }()
	<-fg.entered
	finish := func() *httptest.ResponseRecorder { close(fg.block); <-done; b.revokeWG.Wait(); return w }
	return b, fg, rec, finish
}

func TestInFlightMintAfterForgetIsRevoked(t *testing.T) {
	b, fg, _, finish := inFlightSetup(t, map[string]SessionView{"s": ghLive()})
	b.Forget("s")
	w := finish()
	if w.Code != 403 || strings.Contains(w.Body.String(), "ghs_") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if got := fg.revokedList(); len(got) != 1 || got[0] != "ghs_FAKETOKEN01" {
		t.Fatalf("revoked %v", got)
	}
	if len(b.ghIssued["s"]) != 0 || len(b.ghCache) != 0 || len(b.ghFlight) != 0 {
		t.Fatalf("state: %v %v %v", b.ghIssued, b.ghCache, b.ghFlight)
	}
}

func TestInFlightMintAfterRevokeAllIsRevoked(t *testing.T) {
	b, fg, rec, finish := inFlightSetup(t, map[string]SessionView{"s": ghLive()})
	b.RevokeAllGitHub(context.Background())
	w := finish()
	if w.Code != 503 || len(fg.revokedList()) != 1 || len(b.ghIssued) != 0 {
		t.Fatalf("status %d revoked %v", w.Code, fg.revokedList())
	}
	if rows := revokeRows(rec); len(rows) != 1 || rows[0].Outcome != OutcomeRevoked {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestInFlightMintAfterRespawnIsRevoked(t *testing.T) {
	sessions := map[string]SessionView{"s": ghLive()}
	b, fg, _, finish := inFlightSetup(t, sessions)
	sv := sessions["s"]
	sv.Nonce = nonceB // a new spawn took the id, with no Forget
	sessions["s"] = sv
	w := finish()
	if w.Code != 403 || len(fg.revokedList()) != 1 || len(b.ghIssued["s"]) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("status %d revoked %v", w.Code, fg.revokedList())
	}
}

func TestInFlightMintOfAnotherSessionUnaffected(t *testing.T) {
	b, fg, _, finish := inFlightSetup(t, map[string]SessionView{"s": ghLive(), "o": ghLive()})
	b.Forget("o")
	w := finish()
	if w.Code != 200 || len(fg.revokedList()) != 0 || len(b.ghIssued["s"]) != 1 {
		t.Fatalf("status %d revoked %v", w.Code, fg.revokedList())
	}
}

// patEntry is a token entry used only as a GitHub PAT: no egress, no env.
func patEntry() config.StaticToken {
	return config.StaticToken{Name: "gh-pat-acme", Value: config.KeyRef{Type: "env", Var: "ACME_GH_PAT"}}
}

func patBinding() *config.GitHubBinding {
	return &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", Token: "gh-pat-acme", Repos: []string{"widgets", "gadgets"}}}}
}

func patLive() SessionView {
	return SessionView{Agent: "c", Nonce: nonceA, Boxed: true, Live: true, GitHub: patBinding(), PATs: []config.StaticToken{patEntry()}}
}

const patBody = `{"owner":"acme","repo":"widgets"}`

// patSetup has a resolver and a GitHub minter, to show a PAT never touches the minter.
func patSetup(t *testing.T, sessions map[string]SessionView) (*Broker, *fakeResolver, *fakeGH, *memRec, *time.Time) {
	t.Helper()
	b, fg, rec, now := ghSetup(t, sessions)
	fr := &fakeResolver{}
	b.SetResolver(fr)
	return b, fr, fg, rec, now
}

func TestGitHubPATOwnerHappyPath(t *testing.T) {
	b, fr, fg, rec, _ := patSetup(t, map[string]SessionView{"claude-3": patLive()})
	w := ghCall(b, tokA("claude-3"), patBody)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, has := out["expires_at"]; has || len(out) != 1 || out["token"] != staticSecret {
		t.Fatalf("reply: %s", w.Body)
	}
	if fr.count() != 1 || fr.refs[0] != patEntry().Value || fg.callCount() != 0 {
		t.Fatalf("resolver calls %d refs %v mints %d", fr.count(), fr.refs, fg.callCount())
	}
	if len(rec.rows) != 1 {
		t.Fatalf("rows: %+v", rec.rows)
	}
	r := rec.rows[0]
	if r.Outcome != OutcomeFetched || r.Provider != ProviderGitHub || r.Subject != "acme" || r.Entry != "gh-pat-acme" ||
		r.Repos != 2 || r.SessionID != "claude-3" || r.ScopeHash != patBinding().Hash() || !r.Expires.IsZero() {
		t.Fatalf("row: %+v", r)
	}
	if j, _ := json.Marshal(r); strings.Contains(string(j), staticSecret) {
		t.Fatalf("the audit row holds the value: %s", j)
	}
	if len(b.ghIssued) != 0 || len(b.ghCache) != 0 || len(b.ghFlight) != 0 {
		t.Fatalf("a PAT must not be cached or listed: %v %v %v", b.ghIssued, b.ghCache, b.ghFlight)
	}
	// Every call fetches again: no cache.
	if w := ghCall(b, tokA("claude-3"), patBody); w.Code != 200 || fr.count() != 2 {
		t.Fatalf("second call: %d resolver %d", w.Code, fr.count())
	}
}

func TestGitHubPATOwnerWithoutApps(t *testing.T) {
	mixed := patLive()
	mixed.GitHub = &config.GitHubBinding{Owners: append(patBinding().Owners,
		config.GitHubOwner{Owner: "culiops", App: "bot", Repos: []string{"agent-box"}})}
	b, _, _, _ := setup(t, map[string]SessionView{"s": mixed})
	fr := &fakeResolver{}
	b.SetResolver(fr) // no SetGitHub
	if err := b.CheckGitHub(*patBinding()); err != nil {
		t.Fatalf("a PAT-only binding needs no minter: %v", err)
	}
	if err := b.CheckGitHub(*mixed.GitHub); err == nil {
		t.Fatal("an App owner still needs the minter")
	}
	if w := ghCall(b, tokA("s"), patBody); w.Code != 200 {
		t.Fatalf("PAT owner: %d: %s", w.Code, w.Body)
	}
	w := ghCall(b, tokA("s"), ghBody)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "GitHub is not configured on this agent-box host") {
		t.Fatalf("App owner: %d: %s", w.Code, w.Body)
	}
}

func TestGitHubPATOwnerNeedsRepo(t *testing.T) {
	b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	w := ghCall(b, tokA("s"), `{"owner":"acme"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "name the repository: a personal access token is not scoped by agent-box") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if fr.count() != 0 || len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeDeniedScope ||
		rec.rows[0].Error != "repo required for a PAT owner" || rec.rows[0].Subject != "acme" ||
		rec.rows[0].Entry != "gh-pat-acme" {
		t.Fatalf("resolver %d rows %+v", fr.count(), rec.rows)
	}
}

func TestGitHubPATRepoNotBoundAndEntryUndefined(t *testing.T) {
	b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	if w := ghCall(b, tokA("s"), `{"owner":"acme","repo":"other"}`); w.Code != 403 {
		t.Fatalf("unbound repo: %d", w.Code)
	}
	gone := patLive()
	gone.PATs = nil // the entry was deleted from config
	b2, fr2, _, rec2, _ := patSetup(t, map[string]SessionView{"s": gone})
	if w := ghCall(b2, tokA("s"), patBody); w.Code != 403 {
		t.Fatalf("undefined entry: %d: %s", w.Code, w.Body)
	}
	if fr.count()+fr2.count() != 0 || rec.rows[0].Error != "repo not bound" || rec.rows[0].Entry != "gh-pat-acme" ||
		len(rec2.rows) != 1 || rec2.rows[0].Outcome != OutcomeDeniedScope || rec2.rows[0].Error != "token entry not defined" ||
		rec2.rows[0].Entry != "gh-pat-acme" {
		t.Fatalf("rows %+v %+v", rec.rows, rec2.rows)
	}
}

func TestGitHubPATFailClosed(t *testing.T) {
	b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	rec.recErr = errors.New("disk full")
	w := ghCall(b, tokA("s"), patBody)
	if w.Code != 503 || strings.Contains(w.Body.String(), staticSecret) {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	rec.recErr = nil
	if w := ghCall(b, tokA("s"), patBody); w.Code != 200 || fr.count() != 2 {
		t.Fatalf("retry: %d", w.Code)
	}
}

func TestGitHubPATUnresolved(t *testing.T) {
	b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	fr.err = errors.New("env var ACME_GH_PAT is not set")
	w := ghCall(b, tokA("s"), patBody)
	if w.Code != 503 || strings.Contains(w.Body.String(), staticSecret) {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeUnresolved || rec.rows[0].Subject != "acme" || rec.rows[0].Entry != "gh-pat-acme" {
		t.Fatalf("rows %+v", rec.rows)
	}
}

func TestGitHubPATUsesStaticBucket(t *testing.T) {
	b, fr, _, _, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	for i := range mintsPerMinute + 1 { // more than the 10/min mint bucket
		if w := ghCall(b, tokA("s"), patBody); w.Code != 200 {
			t.Fatalf("fetch %d: %d: %s", i, w.Code, w.Body)
		}
	}
	for range staticFetchesPerMinute - mintsPerMinute - 1 {
		ghCall(b, tokA("s"), patBody)
	}
	if w := ghCall(b, tokA("s"), patBody); w.Code != 429 || fr.count() != staticFetchesPerMinute {
		t.Fatalf("status %d resolved %d", w.Code, fr.count())
	}
}

func TestForgetDoesNotRevokePAT(t *testing.T) {
	b, _, fg, _, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	for range 3 {
		if w := ghCall(b, tokA("s"), patBody); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
	}
	b.Forget("s")
	b.revokeWG.Wait()
	if got := fg.revokedList(); len(got) != 0 {
		t.Fatalf("a PAT is never revoked: %v", got)
	}
}

func TestGitHubPATLockedStore(t *testing.T) {
	b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
	fr.err = fmt.Errorf("store value acme-pat: %w", credref.ErrLocked)
	w := ghCall(b, tokA("s"), patBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), wantStoreLockedMsg) || strings.Contains(w.Body.String(), "acme-pat") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeStoreLocked || rec.rows[0].Error != wantStoreLockedMsg ||
		rec.rows[0].Subject != "acme" || rec.rows[0].Entry != "gh-pat-acme" {
		t.Fatalf("rows %+v", rec.rows)
	}
	if !b.StoreLockedFor("s") {
		t.Fatal("flag not set")
	}
	fr.err = nil
	if w := ghCall(b, tokA("s"), patBody); w.Code != 200 || b.StoreLockedFor("s") {
		t.Fatalf("after unlock: %d flag %v", w.Code, b.StoreLockedFor("s"))
	}
}

func TestGitHubAppMintLockedStore(t *testing.T) {
	b, fg, rec, _ := ghSetup(t, map[string]SessionView{"claude-3": ghLive()})
	fg.err = fmt.Errorf("github app key: %w", credref.ErrLocked)
	w := ghCall(b, tokA("claude-3"), ghBody)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), wantStoreLockedMsg) || strings.Contains(w.Body.String(), "github app key") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if len(rec.rows) != 1 || rec.rows[0].Outcome != OutcomeStoreLocked || rec.rows[0].Error != wantStoreLockedMsg ||
		rec.rows[0].Provider != ProviderGitHub || rec.rows[0].Subject != "culiops" {
		t.Fatalf("rows %+v", rec.rows)
	}
	if !b.StoreLockedFor("claude-3") {
		t.Fatal("flag not set")
	}
	fg.err = nil
	if w := ghCall(b, tokA("claude-3"), ghBody); w.Code != 200 || b.StoreLockedFor("claude-3") {
		t.Fatalf("after unlock: %d flag %v", w.Code, b.StoreLockedFor("claude-3"))
	}
}

func TestGitHubPATUnavailableStore(t *testing.T) {
	for _, ue := range unavailableKinds() {
		b, fr, _, rec, _ := patSetup(t, map[string]SessionView{"s": patLive()})
		fr.err = fmt.Errorf("store value acme-pat: %w", ue)
		w := ghCall(b, tokA("s"), patBody)
		if strings.Contains(w.Body.String(), "acme-pat") || len(rec.rows) != 1 || rec.rows[0].Entry != "gh-pat-acme" {
			t.Fatalf("%s: %d %s %+v", ue.Kind, w.Code, w.Body, rec.rows)
		}
		assertBoxUnavailable(t, w, rec.rows[0], ue)
		if b.StoreLockedFor("s") {
			t.Fatal("an unavailable Keychain must not flag the session store-locked")
		}
	}
}

func TestGitHubAppMintUnavailableStore(t *testing.T) {
	for _, ue := range unavailableKinds() {
		b, fg, rec, _ := ghSetup(t, map[string]SessionView{"claude-3": ghLive()})
		fg.err = fmt.Errorf("github app key: %w", ue)
		w := ghCall(b, tokA("claude-3"), ghBody)
		if strings.Contains(w.Body.String(), "github app key") || len(rec.rows) != 1 || rec.rows[0].Provider != ProviderGitHub {
			t.Fatalf("%s: %d %s %+v", ue.Kind, w.Code, w.Body, rec.rows)
		}
		assertBoxUnavailable(t, w, rec.rows[0], ue)
		if b.StoreLockedFor("claude-3") {
			t.Fatal("an unavailable Keychain must not flag the session store-locked")
		}
	}
}
