package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
)

const ssoRoleARN = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/ap-southeast-1/AWSReservedSSO_ReadOnly_0123456789abcdef"

type gateCall struct {
	creds           Creds
	region, account string
	budget          time.Duration // the ctx deadline left when the gate was called
	source          string
}

// fakeGate returns its results in order, then Unverified (error). With
// release set, each call signals entered and blocks until release is closed,
// then records whether its ctx was cancelled meanwhile.
type fakeGate struct {
	mu        sync.Mutex
	results   []awsprobe.GateResult
	calls     []gateCall
	entered   chan struct{}
	release   chan struct{}
	cancelled bool
}

func (g *fakeGate) callCount() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.calls) }

func (g *fakeGate) fn(ctx context.Context, c Creds, region, account, source string) awsprobe.GateResult {
	if g.release != nil {
		g.entered <- struct{}{}
		<-g.release
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		g.cancelled = true
	}
	var budget time.Duration
	if d, ok := ctx.Deadline(); ok {
		budget = time.Until(d)
	}
	g.calls = append(g.calls, gateCall{c, region, account, budget, source})
	if len(g.results) == 0 {
		return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError}
	}
	r := g.results[0]
	g.results = g.results[1:]
	return r
}

func ssoDirectBinding() *config.AWSBinding {
	return &config.AWSBinding{SourceProfile: "sso-ro", Region: "ap-southeast-1", Mode: config.AWSModeSSODirect}
}

func ssoDirectLive() SessionView {
	return SessionView{Agent: "c", Nonce: nonceA, Boxed: true, Live: true, AWS: ssoDirectBinding()}
}

// ssoDirectSetup is a broker serving one sso_direct session "s-1" whose SSO
// credentials last an hour; gate nil leaves the broker without a gate.
func ssoDirectSetup(t *testing.T, gate *fakeGate) (*Broker, *fakeMinter, *memRec, *time.Time) {
	t.Helper()
	ssoDirectFiles(t)
	b, fm, rec, now := setup(t, map[string]SessionView{"s-1": ssoDirectLive()})
	fm.exp = func(MintInput) time.Time { return now.Add(time.Hour) }
	if gate != nil {
		b.SetGate(gate.fn)
	}
	return b, fm, rec, now
}

func readOnly() awsprobe.GateResult {
	return awsprobe.GateResult{State: awsprobe.GateReadOnly, RoleARN: ssoRoleARN}
}

func lastRow(rec *memRec) Row {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.rows[len(rec.rows)-1]
}

func TestServeSSODirectGatesEveryMint(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly(), {State: awsprobe.GateNotReadOnly, RoleARN: ssoRoleARN,
		Refused: []awsprobe.Verdict{{Pattern: "ec2:GetPasswordData", Why: "returns a credential"},
			{Pattern: "sqs:Receive*", Why: "side effect: sqs:ReceiveMessage is allowed"}}}}}
	b, fm, rec, now := ssoDirectSetup(t, g)
	tok := tokA("s-1")
	w := call(b, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	r := lastRow(rec)
	if len(rec.rows) != 1 || r.Outcome != OutcomeMinted || r.Mode != config.AWSModeSSODirect || r.RoleARN != ssoRoleARN ||
		r.SourceIdentity != "" || r.AccountID != "123456789012" || !r.Expires.Equal(now.Add(time.Hour)) ||
		r.ScopeHash != ssoDirectBinding().Hash() {
		t.Fatalf("minted row %+v", rec.rows)
	}
	if len(g.calls) != 1 || g.calls[0].source != "sso-ro" {
		t.Fatalf("gate did not get the binding's source profile: %+v", g.calls)
	}
	if len(g.calls) != 1 || g.calls[0].account != "123456789012" || g.calls[0].region != "ap-southeast-1" ||
		g.calls[0].creds.AccessKeyID != "ASIAFAKE" || g.calls[0].budget <= 0 || g.calls[0].budget > 15*time.Second {
		t.Fatalf("gate calls %+v", g.calls)
	}

	// A still-valid cached credential is served as it is (no mint, no gate).
	*now = now.Add(50 * time.Minute)
	if w = call(b, tok); w.Code != 200 || fm.callCount() != 1 || len(g.calls) != 1 {
		t.Fatalf("cache: status %d mints %d gates %d", w.Code, fm.callCount(), len(g.calls))
	}

	// Past refreshMargin the refresh mints and gates again, and the gate now refuses.
	*now = now.Add(6 * time.Minute) // 4 min left
	w = call(b, tok)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if fm.callCount() != 2 || len(g.calls) != 2 {
		t.Fatalf("mints %d gates %d", fm.callCount(), len(g.calls))
	}
	r = lastRow(rec)
	if r.Outcome != OutcomeDeniedNotReadOnly || r.Mode != config.AWSModeSSODirect || r.RoleARN != ssoRoleARN ||
		!strings.Contains(r.Error, "ec2:GetPasswordData: returns a credential") ||
		!strings.Contains(r.Error, "sqs:Receive*: side effect: sqs:ReceiveMessage is allowed") || !r.Expires.IsZero() {
		t.Fatalf("refused row %+v", r)
	}
	for _, x := range rec.rows {
		if x.Outcome == OutcomeMinted && x.Time.Equal(*now) {
			t.Fatalf("a minted row was written for the refused credential: %+v", rec.rows)
		}
	}
	// The refused credential was not cached: the next request mints (and gates) again.
	if w = call(b, tok); w.Code == 200 || fm.callCount() != 3 || len(g.calls) != 3 {
		t.Fatalf("after refusal: status %d mints %d gates %d", w.Code, fm.callCount(), len(g.calls))
	}
}

// Review Focus 5: an unverified gate (the gate's own IAM calls failed with
// expired credentials, missing permissions, a timeout) refuses, never passes.
func TestServeSSODirectUnverifiedRefuses(t *testing.T) {
	for _, res := range []awsprobe.GateResult{
		{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSSOExpired},
		{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms, MissingPerms: []string{"iam:GetRole", "iam:ListRolePolicies"}},
		{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonTimeout},
		{}, // no state at all
	} {
		g := &fakeGate{results: []awsprobe.GateResult{res}}
		b, _, rec, _ := ssoDirectSetup(t, g)
		w := call(b, tokA("s-1"))
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "ASIAFAKE") {
			t.Fatalf("%+v: status %d body %s", res, w.Code, w.Body)
		}
		r := lastRow(rec)
		if len(rec.rows) != 1 || r.Outcome != OutcomeDeniedNotReadOnly || !strings.Contains(r.Error, "unverified") {
			t.Fatalf("%+v: rows %+v", res, rec.rows)
		}
		if res.Reason != "" && !strings.Contains(r.Error, string(res.Reason)) {
			t.Fatalf("%+v: error %q", res, r.Error)
		}
		for _, p := range res.MissingPerms {
			if !strings.Contains(r.Error, p) {
				t.Fatalf("%+v: error %q lacks %s", res, r.Error, p)
			}
		}
		if st, _ := b.Stats("s-1"); st.Mints != 0 || st.LastError == "" {
			t.Fatalf("stats %+v", st)
		}
	}
}

// Without a gate nothing is minted at all: no SSO credential is even fetched.
func TestServeSSODirectNilGateRefuses(t *testing.T) {
	b, fm, rec, _ := ssoDirectSetup(t, nil)
	w := call(b, tokA("s-1"))
	if w.Code != http.StatusServiceUnavailable || fm.callCount() != 0 {
		t.Fatalf("status %d mints %d", w.Code, fm.callCount())
	}
	if r := lastRow(rec); r.Outcome != OutcomeDeniedNotReadOnly || r.Mode != config.AWSModeSSODirect {
		t.Fatalf("row %+v", r)
	}
}

// Review Focus 5: an expired SSO login at mint time is the AssumeRole path:
// the same box message, 503, sso_expired row and sign-in flag. No gate runs.
func TestServeSSODirectSSOExpired(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, fm, rec, _ := ssoDirectSetup(t, g)
	fm.err = fmt.Errorf("%w: token expired", ErrSSOExpired)
	w := call(b, tokA("s-1"))
	var body struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	const want = `SSO session for profile "sso-ro" expired. agent-box has asked a human to sign in; retry after they do (or run "aws sso login --profile sso-ro" on the host).`
	if w.Code != 503 || body.Error != want {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if r := lastRow(rec); r.Outcome != OutcomeSSOExpired || r.Mode != config.AWSModeSSODirect {
		t.Fatalf("row %+v", r)
	}
	if b.SSOExpired("s-1") == "" {
		t.Fatal("the sign-in prompt was not raised")
	}
	if len(g.calls) != 0 {
		t.Fatalf("gate ran on a failed mint: %+v", g.calls)
	}
	// A later good mint clears it, as for AssumeRole.
	fm.err = nil
	if w = call(b, tokA("s-1")); w.Code != 200 || b.SSOExpired("s-1") != "" {
		t.Fatalf("status %d flag %q", w.Code, b.SSOExpired("s-1"))
	}
}

func TestServeSSODirectNoSessionPolicy(t *testing.T) {
	b, fm, _, _ := ssoDirectSetup(t, &fakeGate{results: []awsprobe.GateResult{readOnly()}})
	if w := call(b, tokA("s-1")); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	in := fm.calls[0]
	if in.Policy != "" || in.Duration != 0 || in.RoleARN != "" || in.Mode != config.AWSModeSSODirect ||
		in.SourceProfile != "sso-ro" || in.Region != "ap-southeast-1" {
		t.Fatalf("mint input %+v", in)
	}
}

// A mint error (e.g. a non-SSO profile) refuses with its code and no gate.
func TestServeSSODirectMintErrorRefuses(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, fm, rec, _ := ssoDirectSetup(t, g)
	fm.err = &MintError{Code: "NotSSOProfile", Detail: `profile "static" is not an IAM Identity Center profile`}
	w := call(b, tokA("s-1"))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "NotSSOProfile") || strings.Contains(w.Body.String(), "AssumeRole") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if r := lastRow(rec); r.Outcome != OutcomeSTSError || r.Mode != config.AWSModeSSODirect || len(g.calls) != 0 {
		t.Fatalf("row %+v gates %d", r, len(g.calls))
	}
}

// A profile edited on the host to lose its sso_account_id after the mint
// refuses: the gate needs the account to compare.
func TestServeSSODirectNoAccountRefuses(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, _, rec, _ := ssoDirectSetup(t, g)
	sv := ssoDirectLive()
	sv.AWS.SourceProfile = "static"
	b.lookup = func(string) (SessionView, bool) { return sv, true }
	w := call(b, tokA("s-1"))
	if w.Code == 200 || len(g.calls) != 0 {
		t.Fatalf("status %d gates %d", w.Code, len(g.calls))
	}
	if r := lastRow(rec); r.Outcome != OutcomeDeniedNotReadOnly || w.Code != http.StatusForbidden {
		t.Fatalf("status %d row %+v", w.Code, r)
	}
}

// A profile the host file does not (or no longer) have is a read failure, not
// a verdict: 503 with the unverified message, so the box retries.
func TestServeSSODirectAccountReadFailureIsUnverified(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, _, rec, _ := ssoDirectSetup(t, g)
	sv := ssoDirectLive()
	sv.AWS.SourceProfile = "gone-from-the-file"
	b.lookup = func(string) (SessionView, bool) { return sv, true }
	w := call(b, tokA("s-1"))
	var body struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusServiceUnavailable || body.Error != boxUnverified || len(g.calls) != 0 {
		t.Fatalf("status %d body %s gates %d", w.Code, w.Body, len(g.calls))
	}
	if r := lastRow(rec); r.Outcome != OutcomeDeniedNotReadOnly || !strings.Contains(r.Error, "unverified") {
		t.Fatalf("row %+v", r)
	}
}

// Every refused item is in the row, but the row stays bounded.
func TestServeSSODirectRefusalRowIsBounded(t *testing.T) {
	var v []awsprobe.Verdict
	for i := range 2000 {
		v = append(v, awsprobe.Verdict{Pattern: fmt.Sprintf("svc%04d:Put*", i), Why: "not a read verb"})
	}
	b, _, rec, _ := ssoDirectSetup(t, &fakeGate{results: []awsprobe.GateResult{{State: awsprobe.GateNotReadOnly, Refused: v}}})
	call(b, tokA("s-1"))
	r := lastRow(rec)
	if len(r.Error) > maxGateErrLen+64 || !strings.Contains(r.Error, "svc0000:Put*: not a read verb") ||
		!strings.Contains(r.Error, "more") {
		t.Fatalf("error (%d bytes) %q", len(r.Error), r.Error[:min(len(r.Error), 200)])
	}
	st, _ := b.Stats("s-1")
	if len(st.LastError) > maxGateErrLen+200 {
		t.Fatalf("LastError %d bytes", len(st.LastError))
	}
}

// Named connections in sso_direct mint and gate the same way.
func TestServeSSODirectNamedConnection(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, fm, rec, _ := ssoDirectSetup(t, g)
	sv := live()
	sv.AWS = nil
	sv.AWSConns = []config.AWSConnection{{Name: "ro", AWSBinding: *ssoDirectBinding()}}
	b.lookup = func(string) (SessionView, bool) { return sv, true }
	r := callBody(b, tokA("s-1"), `{"name":"ro"}`)
	if r.Code != 200 || fm.callCount() != 1 || len(g.calls) != 1 {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	if row := lastRow(rec); row.Entry != "ro" || row.Mode != config.AWSModeSSODirect || row.Outcome != OutcomeMinted {
		t.Fatalf("row %+v", row)
	}
}

func TestCheckSSODirect(t *testing.T) {
	bnd := *ssoDirectBinding()

	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, fm, rec, now := ssoDirectSetup(t, g)
	res, exp := b.CheckSSODirect(context.Background(), bnd)
	if res.State != awsprobe.GateReadOnly || !exp.Equal(now.Add(time.Hour)) || len(g.calls) != 1 ||
		g.calls[0].account != "123456789012" || g.calls[0].source != "sso-ro" || g.calls[0].budget > 15*time.Second {
		t.Fatalf("read-only: %+v %v %+v", res, exp, g.calls)
	}
	if r := lastRow(rec); r.Outcome != OutcomeProbeMinted || r.Mode != config.AWSModeSSODirect || r.RoleARN != ssoRoleARN ||
		r.AccountID != "123456789012" || r.SessionID != "" {
		t.Fatalf("row %+v", r)
	}
	if fm.calls[0].Mode != config.AWSModeSSODirect || fm.calls[0].Policy != "" {
		t.Fatalf("mint input %+v", fm.calls[0])
	}

	// A refusal is returned as the gate gave it, and audited.
	g.results = []awsprobe.GateResult{{State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{{Pattern: "s3:*", Why: "not a read verb"}}}}
	if res, _ = b.CheckSSODirect(context.Background(), bnd); res.State != awsprobe.GateNotReadOnly {
		t.Fatalf("refusal: %+v", res)
	}
	if r := lastRow(rec); r.Outcome != OutcomeProbeMinted || !strings.Contains(r.Error, "s3:*") {
		t.Fatalf("row %+v", r)
	}

	// An expired login: unverified (sso_expired), zero expiration, probe_failed.
	fm.err = &MintError{Code: "SSOExpired", Detail: "expired", cause: ErrSSOExpired}
	res, exp = b.CheckSSODirect(context.Background(), bnd)
	if res.State != awsprobe.GateUnverified || res.Reason != awsprobe.ReasonSSOExpired || !exp.IsZero() {
		t.Fatalf("expired: %+v %v", res, exp)
	}
	if r := lastRow(rec); r.Outcome != OutcomeProbeFailed || r.Mode != config.AWSModeSSODirect {
		t.Fatalf("row %+v", r)
	}

	// A non-SSO profile is a definite refusal.
	fm.err = &MintError{Code: "NotSSOProfile", Detail: "static keys"}
	if res, exp = b.CheckSSODirect(context.Background(), bnd); res.State != awsprobe.GateNotReadOnly || len(res.Refused) == 0 || !exp.IsZero() {
		t.Fatalf("not sso: %+v", res)
	}

	// No gate: unverified, and no credential fetched.
	b2, fm2, _, _ := ssoDirectSetup(t, nil)
	if res, exp = b2.CheckSSODirect(context.Background(), bnd); res.State != awsprobe.GateUnverified || !exp.IsZero() || fm2.callCount() != 0 {
		t.Fatalf("no gate: %+v mints %d", res, fm2.callCount())
	}
}

// callCtx is call with the request's context.
func callCtx(ctx context.Context, b *Broker, tok string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/credentials", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

// R21: the box's helper gives up after 10 s. A slow but read-only gate must
// not be cancelled with the request: the flight finishes on its own contexts,
// caches the passing credential, and the retry is served from the cache.
func TestServeSSODirectSurvivesTheRequestEnding(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, fm, rec, _ := ssoDirectSetup(t, g)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *httptest.ResponseRecorder)
	go func() { got <- callCtx(ctx, b, tokA("s-1")) }()
	<-g.entered
	cancel() // the helper's deadline, or a dropped connection
	w := <-got
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("ended request: status %d body %s", w.Code, w.Body)
	}
	close(g.release)
	b.ssoWG.Wait()
	if g.cancelled {
		t.Fatal("the gate's context was cancelled with the request")
	}
	if r := lastRow(rec); r.Outcome != OutcomeMinted || r.Mode != config.AWSModeSSODirect {
		t.Fatalf("row %+v", r)
	}
	if w = call(b, tokA("s-1")); w.Code != 200 || fm.callCount() != 1 || g.callCount() != 1 {
		t.Fatalf("retry: status %d mints %d gates %d", w.Code, fm.callCount(), g.callCount())
	}
	if st, _ := b.Stats("s-1"); st.Mints != 1 || st.CacheHits != 1 {
		t.Fatalf("stats %+v", st)
	}
	b.mu.Lock()
	n := len(b.ssoFlights)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d flights left in the table", n)
	}
}

// A retry that arrives while the flight is still running joins it.
func TestServeSSODirectRetryJoinsTheFlight(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, fm, _, _ := ssoDirectSetup(t, g)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- callCtx(ctx, b, tokA("s-1")) }()
	<-g.entered
	cancel()
	<-first
	second := make(chan *httptest.ResponseRecorder)
	go func() { second <- call(b, tokA("s-1")) }()
	waitFor(t, func() bool { return fm.callCount() == 1 && flightWaiters(b) == 2 })
	close(g.release)
	if w := <-second; w.Code != 200 || !strings.Contains(w.Body.String(), "ASIAFAKE") {
		t.Fatalf("joined request: status %d body %s", w.Code, w.Body)
	}
	b.ssoWG.Wait()
	if fm.callCount() != 1 || g.callCount() != 1 {
		t.Fatalf("mints %d gates %d", fm.callCount(), g.callCount())
	}
}

// Concurrent requests run exactly one mint and one gate, and share its verdict.
func TestServeSSODirectConcurrentRequestsShareOneGate(t *testing.T) {
	for _, res := range []awsprobe.GateResult{readOnly(), {State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{{Pattern: "s3:*", Why: "x"}}}} {
		g := &fakeGate{results: []awsprobe.GateResult{res}, entered: make(chan struct{}, 1), release: make(chan struct{})}
		b, fm, rec, _ := ssoDirectSetup(t, g)
		const n = 6
		codes := make(chan int, n)
		for range n {
			go func() { codes <- call(b, tokA("s-1")).Code }()
		}
		<-g.entered
		waitFor(t, func() bool { return flightWaiters(b) == n })
		close(g.release)
		want := 200
		if res.State != awsprobe.GateReadOnly {
			want = http.StatusForbidden
		}
		for range n {
			if c := <-codes; c != want {
				t.Fatalf("%s: status %d, want %d", res.State, c, want)
			}
		}
		b.ssoWG.Wait()
		if fm.callCount() != 1 || g.callCount() != 1 || len(rec.rows) != 1 {
			t.Fatalf("%s: mints %d gates %d rows %+v", res.State, fm.callCount(), g.callCount(), rec.rows)
		}
		if res.State != awsprobe.GateReadOnly { // a refusal is never cached: the next request runs a new flight
			g.entered, g.release = nil, nil
			g.results = []awsprobe.GateResult{res}
			if w := call(b, tokA("s-1")); w.Code != http.StatusForbidden || g.callCount() != 2 || fm.callCount() != 2 {
				t.Fatalf("after refusal: status %d gates %d mints %d", w.Code, g.callCount(), fm.callCount())
			}
		}
	}
}

// A session forgotten while its flight runs gets nothing cached.
func TestServeSSODirectForgetDuringFlightCachesNothing(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	b, _, _, _ := ssoDirectSetup(t, g)
	done := make(chan struct{})
	go func() { call(b, tokA("s-1")); close(done) }()
	<-g.entered
	b.Forget("s-1")
	close(g.release)
	<-done
	b.ssoWG.Wait()
	b.mu.Lock()
	_, cachedIt := b.cache[connKey{"s-1", ""}]
	_, hasStats := b.stats["s-1"]
	b.mu.Unlock()
	if cachedIt || hasStats {
		t.Fatalf("cached %v stats %v after Forget", cachedIt, hasStats)
	}
}

// flightWaiters is the waiter count of the one registered flight (0 if none).
func flightWaiters(b *Broker) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range b.ssoFlights {
		return f.waiters
	}
	return 0
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not reached")
}

func TestCheckSSODirectConnRowMatchesServe(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly(), readOnly()}}
	b, _, rec, _ := ssoDirectSetup(t, g)
	c := config.AWSConnection{Name: "ro", AWSBinding: *ssoDirectBinding()}
	if res, _ := b.CheckSSODirectConn(context.Background(), c); res.State != awsprobe.GateReadOnly {
		t.Fatalf("%+v", res)
	}
	if r := lastRow(rec); r.ScopeHash != c.DefHash() || r.Entry != "ro" {
		t.Fatalf("conn row %+v", r)
	}
	if _, _ = b.CheckSSODirect(context.Background(), *ssoDirectBinding()); lastRow(rec).ScopeHash != ssoDirectBinding().Hash() || lastRow(rec).Entry != "" {
		t.Fatalf("inline row %+v", lastRow(rec))
	}
	// A profile without sso_account_id is a definite refusal; an unreadable one is unverified.
	bad := *ssoDirectBinding()
	bad.SourceProfile = "static"
	if res, _ := b.CheckSSODirect(context.Background(), bad); res.State != awsprobe.GateNotReadOnly {
		t.Fatalf("static: %+v", res)
	}
	bad.SourceProfile = "gone-from-the-file"
	if res, _ := b.CheckSSODirect(context.Background(), bad); res.State != awsprobe.GateUnverified {
		t.Fatalf("gone: %+v", res)
	}
}

// Hooks.CheckSSODirect picks the inline binding for key "" and the named
// connection otherwise, and refuses (unverified) anything that is not an
// sso_direct binding, so the gate can never vouch for an AssumeRole one.
func TestHooksCheckSSODirect(t *testing.T) {
	g := &fakeGate{results: []awsprobe.GateResult{readOnly()}}
	b, fm, rec, now := ssoDirectSetup(t, g)
	h := Hooks{B: b}
	bnd := *ssoDirectBinding()

	res, exp := h.CheckSSODirect(context.Background(), config.Bindings{AWS: &bnd}, "")
	if res.State != awsprobe.GateReadOnly || !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("inline: %+v %v", res, exp)
	}
	if r := lastRow(rec); r.Entry != "" || r.ScopeHash != bnd.Hash() {
		t.Fatalf("inline row %+v", r)
	}

	conn := config.AWSConnection{Name: "ro", AWSBinding: bnd}
	g.results = []awsprobe.GateResult{readOnly()}
	res, _ = h.CheckSSODirect(context.Background(), config.Bindings{AWSConns: []config.AWSConnection{conn}}, "ro")
	if res.State != awsprobe.GateReadOnly {
		t.Fatalf("conn: %+v", res)
	}
	if r := lastRow(rec); r.Entry != "ro" || r.ScopeHash != conn.DefHash() {
		t.Fatalf("conn row %+v", r)
	}

	n := fm.callCount()
	ar := config.AWSConnection{Name: "ar", AWSBinding: config.AWSBinding{SourceProfile: "p", RoleARN: "arn:aws:iam::1:role/r", Region: "r1"}}
	for _, c := range []struct {
		bs  config.Bindings
		key string
	}{
		{config.Bindings{AWSConns: []config.AWSConnection{ar}}, "ar"},       // assume_role connection
		{config.Bindings{AWSConns: []config.AWSConnection{conn}}, "absent"}, // no such connection
		{config.Bindings{}, ""}, // no inline binding
	} {
		if res, exp = h.CheckSSODirect(context.Background(), c.bs, c.key); res.State != awsprobe.GateUnverified || !exp.IsZero() {
			t.Errorf("%q: %+v %v, want unverified", c.key, res, exp)
		}
	}
	if fm.callCount() != n {
		t.Fatalf("a non-sso_direct key fetched credentials (%d mints)", fm.callCount()-n)
	}
}

// The real gate hands the binding's source profile to the AWS client: a CA bundle
// that exists only on that profile fails the config load, before any network.
func TestIAMGatePassesTheSourceProfile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "config")
	body := "[profile src]\nca_bundle = " + filepath.Join(dir, "no-such-bundle.pem") + "\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", conf)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CA_BUNDLE", "")
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	res := IAMGate(context.Background(), Creds{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "TK"}, "eu-west-1", "123456789012", "src")
	if res.State != awsprobe.GateUnverified || !strings.Contains(logged.String(), "load AWS config") {
		t.Fatalf("state %v, log %q", res.State, logged.String())
	}
}
