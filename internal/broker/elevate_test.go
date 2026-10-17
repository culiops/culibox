package broker

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

const writeRole = "arn:aws:iam::123456789012:role/agentbox-agent-write"

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

func withTimers(b *Broker) *[]*fakeTimer {
	ts := &[]*fakeTimer{}
	b.after = func(d time.Duration, f func()) stopper {
		t := &fakeTimer{d: d, f: f}
		*ts = append(*ts, t)
		return t
	}
	return ts
}

func elevBnd() *config.AWSBinding {
	b := binding()
	b.Label = "sandbox"
	b.Elevate = &config.AWSElevate{RoleARN: writeRole}
	return b
}

func liveElev() SessionView {
	return SessionView{Agent: "aws", Nonce: nonceA, Boxed: true, Live: true, Exclusive: true, AWS: elevBnd()}
}

func cookie(min int) ElevateRequest { return ElevateRequest{Minutes: min, Cred: CredCookie} }

func TestElevateGrantsAndAudits(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	v, err := b.Elevate("aws-1", ElevateRequest{Minutes: 60, Reason: "apply vpc", Cred: CredCookie})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Active || !v.Until.Equal(now.Add(time.Hour)) || !v.GrantedAt.Equal(*now) || v.Reason != "apply vpc" {
		t.Fatalf("view %+v", v)
	}
	if len(*ts) != 1 || (*ts)[0].d != time.Hour {
		t.Fatalf("timer %+v", *ts)
	}
	if len(rec.appr) != 1 {
		t.Fatalf("approvals %+v", rec.appr)
	}
	r := rec.appr[0]
	if r.Action != ActionGranted || r.Cred != CredCookie || r.Minutes != 60 || r.RoleARN != writeRole ||
		r.AccountID != "123456789012" || r.Profile != "aws" || !r.Until.Equal(v.Until) || r.Reason != "apply vpc" {
		t.Fatalf("row %+v", r)
	}
}

func TestElevateRefusals(t *testing.T) {
	notLive, host, unbound, noElev, shared, prod := liveElev(), liveElev(), liveElev(), liveElev(), liveElev(), liveElev()
	notLive.Live = false
	host.Boxed = false
	unbound.AWS = nil
	noElev.AWS = binding()
	shared.Exclusive = false
	prod.AWS = elevBnd()
	prod.AWS.Prod = true
	max30 := liveElev()
	max30.AWS = elevBnd()
	max30.AWS.Elevate.MaxMinutes = 30
	b, _, rec, now := setup(t, map[string]SessionView{"dead": notLive, "host": host, "plain": unbound,
		"noelev": noElev, "shared": shared, "prod": prod, "max30": max30, "ok": liveElev()})
	withTimers(b)
	cases := []struct {
		name, sid string
		req       ElevateRequest
		want      string
	}{
		{"bearer", "ok", ElevateRequest{Minutes: 60, Cred: CredBearer}, "signed-in browser"},
		{"unknown", "nope", cookie(60), "not running"},
		{"not live", "dead", cookie(60), "not running"},
		{"host tier", "host", cookie(60), "no AWS binding in a box"},
		{"unbound", "plain", cookie(60), "no AWS binding in a box"},
		{"no elevate", "noelev", cookie(60), "did not allow elevation"},
		{"shared box", "shared", cookie(60), "box is shared; restart it"},
		{"45 min", "ok", cookie(45), "30 or 60"},
		{"60 over max 30", "max30", cookie(60), "at most 30"},
		{"long reason", "ok", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: strings.Repeat("x", 201)}, "one line"},
		{"newline reason", "ok", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: "a\nb"}, "one line"},
		{"prod no label", "prod", cookie(30), "type the binding label"},
		{"prod wrong label", "prod", ElevateRequest{Minutes: 30, Cred: CredCookie, ConfirmLabel: "Sandbox"}, "type the binding label"},
	}
	for i, c := range cases {
		*now = now.Add(2 * time.Second) // step past the refusal throttle
		_, err := b.Elevate(c.sid, c.req)
		if !errors.Is(err, ErrElevateRefused) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if _, on := b.Elevation(c.sid); on {
			t.Errorf("%s: an overlay was set", c.name)
		}
		if got := rec.appr[len(rec.appr)-1]; got.Action != ActionRefused || got.Cred != c.req.Cred || !strings.Contains(got.Error, c.want) {
			t.Errorf("%s (#%d): refused row %+v", c.name, i, got)
		}
	}
	if _, err := b.Elevate("prod", ElevateRequest{Minutes: 30, Cred: CredCookie, ConfirmLabel: "sandbox"}); err != nil {
		t.Fatalf("the right label grants: %v", err)
	}
}

// A reason is counted in characters, not bytes: 200 Vietnamese characters are
// accepted, 201 are not, and the stored row keeps the whole valid reason.
func TestReasonIsCountedInCharactersAndStoredWhole(t *testing.T) {
	a, err := OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Unix(1_800_000_000, 0)
	b := New(secret, func(string) (SessionView, bool) { return liveElev(), true }, &fakeMinter{now: func() time.Time { return now }}, auditRec{a})
	b.now = func() time.Time { return now }
	withTimers(b)
	reason := strings.Repeat("ệ", 200) // 3 bytes each: 600 bytes
	if _, err := b.Elevate("aws-1", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: reason + "ệ"}); !errors.Is(err, ErrElevateRefused) {
		t.Fatalf("201 characters: %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := b.Elevate("aws-1", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: reason}); err != nil {
		t.Fatalf("200 characters: %v", err)
	}
	var got string
	if err := a.db.QueryRow(`SELECT reason FROM approval_audit WHERE action = 'granted'`).Scan(&got); err != nil || got != reason {
		t.Fatalf("stored reason truncated: %d bytes, %v", len(got), err)
	}
}

// auditRec adapts the SQLite store to Recorder+ApprovalRecorder for the test above.
type auditRec struct{ *Audit }

func TestCredOnARowIsAlwaysAFixedLabel(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", ElevateRequest{Minutes: 30, Cred: "Bearer s3cr3t-token"})
	b.RefuseElevation("aws-1", "free text from a caller", "bad origin")
	b.Drop("aws-1", "x")
	for _, r := range rec.appr {
		if r.Cred != credNone && r.Cred != CredCookie && r.Cred != CredBearer {
			t.Fatalf("free text in cred: %+v", r)
		}
	}
	if len(rec.appr) != 1 || rec.appr[0].Cred != credNone {
		t.Fatalf("%+v", rec.appr)
	}
}

func TestClaimCheckRefusesTheGrant(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.SetClaimCheck(func(sid string) error { return errors.New("another box (instance PROD) uses /w") })
	if _, err := b.Elevate("aws-1", cookie(60)); !errors.Is(err, ErrElevateRefused) || !strings.Contains(err.Error(), "instance PROD") {
		t.Fatalf("got %v", err)
	}
	if rec.appr[0].Action != ActionRefused {
		t.Fatalf("%+v", rec.appr)
	}
}

func TestRefusedRowsAreThrottledPerSessionAndCred(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	for i := 0; i < 50; i++ {
		b.RefuseElevation("aws-1", CredBearer, "needs a browser session")
	}
	b.RefuseElevation("aws-1", "none", "needs a browser session")
	if len(rec.appr) != 2 {
		t.Fatalf("want one row per (session, cred) per second, got %d", len(rec.appr))
	}
}

func TestRefusalThrottleMapIsPrunedAndForgotten(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"a": liveElev(), "b": liveElev(), "c": liveElev(), "d": liveElev(), "e": liveElev()})
	for _, sid := range []string{"a", "b", "c", "d"} {
		b.RefuseElevation(sid, CredBearer, "x")
	}
	*now = now.Add(2 * time.Second)
	b.RefuseElevation("e", CredBearer, "x") // the sweep runs on the next refusal
	b.mu.Lock()
	n := len(b.refused)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("throttle map holds %d keys after the sweep, want 1", n)
	}
	b.Forget("e")
	b.mu.Lock()
	n = len(b.refused)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("Forget left %d throttle keys", n)
	}
}

func TestExtendMovesUntilKeepsGrantedAtAndRearms(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	first, _ := b.Elevate("aws-1", cookie(30))
	*now = now.Add(15 * time.Minute)
	v, err := b.Elevate("aws-1", cookie(60))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Until.Equal(now.Add(time.Hour)) || !v.GrantedAt.Equal(first.GrantedAt) {
		t.Fatalf("extend %+v (first %+v)", v, first)
	}
	if !(*ts)[0].stopped || (*ts)[1].stopped || (*ts)[1].d != time.Hour {
		t.Fatalf("timers %+v %+v", (*ts)[0], (*ts)[1])
	}
	if rec.appr[1].Action != ActionExtended {
		t.Fatalf("%+v", rec.appr)
	}
	(*ts)[0].f() // the old timer firing late must not end the new window
	if _, on := b.Elevation("aws-1"); !on {
		t.Fatal("a stale timer ended an extended window")
	}
}

func TestTimerExpiresTheWindowAndAudits(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	b.Elevate("aws-1", cookie(30))
	*now = now.Add(30 * time.Minute)
	(*ts)[0].f()
	if v, on := b.Elevation("aws-1"); on || v.Active {
		t.Fatalf("still elevated: %+v", v)
	}
	if got := rec.appr[len(rec.appr)-1]; got.Action != ActionExpired || got.Cred != "" {
		t.Fatalf("%+v", got)
	}
}

func TestDropReturnsLastValidAndAuditsTheCred(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	b.Elevate("aws-1", cookie(60))
	exp := now.Add(15 * time.Minute)
	b.mu.Lock()
	b.noteElevatedExp("aws-1", exp) // what a mint does (Task 11), under mu
	b.mu.Unlock()
	v, ok := b.Drop("aws-1", CredBearer)
	if !ok || v.Active || !v.LastValid.Equal(exp) || !(*ts)[0].stopped {
		t.Fatalf("drop %+v %v", v, ok)
	}
	if got := rec.appr[len(rec.appr)-1]; got.Action != ActionDropped || got.Cred != CredBearer {
		t.Fatalf("%+v", got)
	}
	if v, _ := b.Elevation("aws-1"); v.Active || !v.LastValid.Equal(exp) {
		t.Fatalf("after drop the view keeps last_valid until it passes: %+v", v)
	}
	*now = exp.Add(time.Second)
	if v, on := b.Elevation("aws-1"); on || !v.LastValid.IsZero() {
		t.Fatalf("a passed last_valid is gone: %+v", v)
	}
	if _, ok := b.Drop("aws-1", CredCookie); ok {
		t.Fatal("dropping nothing reports ok=false")
	}
}

func TestNoteElevatedExpOnlyRaises(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(60))
	hi, lo := now.Add(15*time.Minute), now.Add(5*time.Minute)
	b.mu.Lock()
	b.noteElevatedExp("aws-1", hi)
	b.noteElevatedExp("aws-1", lo)
	b.noteElevatedExp("nobody", hi) // no overlay: no effect, no panic
	got := b.elev["aws-1"].lastExp
	b.mu.Unlock()
	if !got.Equal(hi) {
		t.Fatalf("lastExp %v, want %v", got, hi)
	}
}

func TestExtendCarriesLastExpAndANewGrantClearsDropped(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(60))
	exp := now.Add(15 * time.Minute)
	b.mu.Lock()
	b.noteElevatedExp("aws-1", exp)
	b.mu.Unlock()
	b.Elevate("aws-1", cookie(30)) // extend
	b.Drop("aws-1", CredCookie)
	if v, _ := b.Elevation("aws-1"); !v.LastValid.Equal(exp) {
		t.Fatalf("an extend lost lastExp: %+v", v)
	}
	b.Elevate("aws-1", cookie(30)) // a new grant clears the dropped note
	if v, _ := b.Elevation("aws-1"); !v.Active || !v.LastValid.IsZero() {
		t.Fatalf("%+v", v)
	}
}

func TestForgetStopsTimerAndClearsEverything(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	b.Elevate("aws-1", cookie(60))
	b.Forget("aws-1")
	if !(*ts)[0].stopped {
		t.Fatal("Forget leaked the timer")
	}
	if _, on := b.Elevation("aws-1"); on {
		t.Fatal("Forget left the overlay")
	}
}

func TestUnauditedGrantDoesNotStand(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	rec.apprErr = errors.New("disk full")
	if _, err := b.Elevate("aws-1", cookie(60)); !errors.Is(err, ErrElevateRefused) || !strings.Contains(err.Error(), "could not be audited") {
		t.Fatalf("got %v", err)
	}
	if _, on := b.Elevation("aws-1"); on || !(*ts)[0].stopped {
		t.Fatal("an unaudited grant stayed in force")
	}
}

// Plan correction 17 also covers an extend: the overlay is removed.
func TestUnauditedExtendRemovesTheOverlay(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	if _, err := b.Elevate("aws-1", cookie(30)); err != nil {
		t.Fatal(err)
	}
	rec.apprErr = errors.New("disk full")
	if _, err := b.Elevate("aws-1", cookie(60)); !errors.Is(err, ErrElevateRefused) {
		t.Fatalf("got %v", err)
	}
	if _, on := b.Elevation("aws-1"); on || !(*ts)[0].stopped || len(*ts) != 1 {
		t.Fatal("a failed extend left the window in force or a timer running")
	}
}

func TestNoApprovalRecorderRefusesEveryGrant(t *testing.T) {
	b := New(secret, func(string) (SessionView, bool) { return liveElev(), true }, &fakeMinter{now: time.Now}, onlyRows{})
	if _, err := b.Elevate("aws-1", cookie(60)); !errors.Is(err, ErrElevateRefused) {
		t.Fatalf("got %v", err)
	}
}

type onlyRows struct{}

func (onlyRows) Record(Row) error { return nil }

// blockRec holds RecordApproval until released, so a test can look at the
// broker while a grant's row is still being written.
type blockRec struct {
	*memRec
	entered chan struct{}
	release chan struct{}
}

func (r *blockRec) RecordApproval(a ApprovalRow) error {
	if a.Action == ActionGranted || a.Action == ActionExtended {
		r.entered <- struct{}{}
		<-r.release
	}
	return r.memRec.RecordApproval(a)
}

func blockedBroker(t *testing.T) (*Broker, *blockRec, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	rec := &blockRec{memRec: &memRec{}, entered: make(chan struct{}), release: make(chan struct{})}
	b := New(secret, func(string) (SessionView, bool) { return liveElev(), true }, &fakeMinter{now: func() time.Time { return now }}, rec)
	b.now = func() time.Time { return now }
	withTimers(b)
	return b, rec, &now
}

func elevatedNow(b *Broker, sid string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.elevatedLocked(sid, now)
	return ok
}

// A grant is pending until its row is written: the mint predicate says no, the
// view reports no window, and a second grant is refused rather than racing it.
func TestPendingGrantIsNotElevatedUntilItsRowIsWritten(t *testing.T) {
	b, rec, now := blockedBroker(t)
	done := make(chan error, 1)
	go func() { _, err := b.Elevate("aws-1", cookie(60)); done <- err }()
	<-rec.entered
	b.mu.Lock()
	pending := b.elev["aws-1"] != nil && b.elev["aws-1"].pending
	b.mu.Unlock()
	if !pending || elevatedNow(b, "aws-1", *now) {
		t.Fatalf("pending=%v elevated=%v; want pending and not elevated", pending, elevatedNow(b, "aws-1", *now))
	}
	if v, on := b.Elevation("aws-1"); on || v.Active {
		t.Fatalf("a pending grant is reported as a window: %+v", v)
	}
	if _, err := b.Elevate("aws-1", cookie(30)); !errors.Is(err, ErrElevateRefused) || !strings.Contains(err.Error(), "still being recorded") {
		t.Fatalf("a second grant while pending: %v", err)
	}
	rec.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !elevatedNow(b, "aws-1", *now) {
		t.Fatal("the written grant is not elevated")
	}
	if v, on := b.Elevation("aws-1"); !on || !v.Active {
		t.Fatalf("%+v", v)
	}
}

// An extend leaves the old window exactly as it was until the extended row is
// written; only then do the new until and generation apply.
func TestExtendTakesEffectOnlyAfterItsRowIsWritten(t *testing.T) {
	b, rec, now := blockedBroker(t)
	var wg sync.WaitGroup
	grant := func(min int) {
		wg.Add(1)
		go func() { defer wg.Done(); b.Elevate("aws-1", cookie(min)) }()
		<-rec.entered
	}
	grant(30)
	rec.release <- struct{}{}
	wg.Wait()
	first, _ := b.Elevation("aws-1")
	grant(60) // extend, blocked in the audit write
	if v, _ := b.Elevation("aws-1"); !v.Until.Equal(first.Until) || !elevatedNow(b, "aws-1", *now) {
		t.Fatalf("the old window changed before the extended row was written: %+v", v)
	}
	rec.release <- struct{}{}
	wg.Wait()
	if v, _ := b.Elevation("aws-1"); !v.Until.Equal(now.Add(time.Hour)) || !v.GrantedAt.Equal(first.GrantedAt) {
		t.Fatalf("after the row: %+v", v)
	}
}

func TestElevatedPredicateNeedsSixteenMinutes(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(30))
	if !elevatedNow(b, "aws-1", now.Add(14*time.Minute)) || elevatedNow(b, "aws-1", now.Add(15*time.Minute)) {
		t.Fatal("elevated needs at least 16 minutes left: 30-14=16 yes, 30-15=15 no")
	}
	if elevatedNow(b, "nobody", *now) {
		t.Fatal("no window, elevated")
	}
}

// The window is measured from after the claim check, not before it.
func TestWindowStartsAfterTheClaimCheck(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	b.SetClaimCheck(func(string) error { *now = now.Add(10 * time.Second); return nil })
	start := *now
	v, err := b.Elevate("aws-1", cookie(30))
	if err != nil {
		t.Fatal(err)
	}
	want := start.Add(10*time.Second + 30*time.Minute)
	if !v.Until.Equal(want) || !v.GrantedAt.Equal(start.Add(10*time.Second)) || (*ts)[0].d != 30*time.Minute ||
		!rec.appr[0].Time.Equal(start.Add(10*time.Second)) {
		t.Fatalf("%+v timer %v row %v", v, (*ts)[0].d, rec.appr[0].Time)
	}
}

func TestReasonRejectsSeparatorsAndFormatCharactersAndIsShownAsStored(t *testing.T) {
	b, _, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	for name, r := range map[string]string{"line sep": "a\u2028b", "para sep": "a\u2029b", "bidi override": "a\u202eb", "zero-width joiner": "a\u200db"} {
		*now = now.Add(2 * time.Second)
		if _, err := b.Elevate("aws-1", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: r}); !errors.Is(err, ErrElevateRefused) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	v, err := b.Elevate("aws-1", ElevateRequest{Minutes: 30, Cred: CredCookie, Reason: "  apply   the vpc  "})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.appr[len(rec.appr)-1].Reason; v.Reason != "apply the vpc" || got != v.Reason {
		t.Fatalf("view %q, row %q", v.Reason, got)
	}
}

func TestDropAuditsAFixedLabelForFreeTextCred(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(30))
	if _, ok := b.Drop("aws-1", "Bearer s3cr3t free text"); !ok {
		t.Fatal("drop")
	}
	if got := rec.appr[len(rec.appr)-1]; got.Action != ActionDropped || got.Cred != credNone {
		t.Fatalf("%+v", got)
	}
}

// lastExpOf reads the overlay's lastExp (zero when there is no overlay); the
// active ElevationView does not expose it.
func lastExpOf(b *Broker, sid string) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.elev[sid]; e != nil {
		return e.lastExp
	}
	return time.Time{}
}

func mintOnce(t *testing.T, b *Broker) {
	t.Helper()
	if w := call(b, tokA("aws-1")); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestElevatedMintUsesTheWriteRoleAndPolicy(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(60))
	mintOnce(t, b)
	in := fm.calls[0]
	if in.RoleARN != writeRole || in.Duration != 15*time.Minute || in.SessionName != "agentbox-aws-1" ||
		!strings.Contains(in.Policy, "sts:AssumeRole*") || !strings.Contains(in.Policy, "iam:Create*") {
		t.Fatalf("mint input %+v", in)
	}
	r := rec.rows[len(rec.rows)-1]
	if !r.Elevated || r.RoleARN != writeRole || r.Outcome != OutcomeMinted || r.ScopeHash != elevBnd().Hash() {
		t.Fatalf("audit %+v", r)
	}
	if got := lastExpOf(b, "aws-1"); !got.Equal(fm.calls0Exp()) {
		t.Fatalf("lastExp %v, want %v", got, fm.calls0Exp())
	}
}

func TestReadOnlyWithoutAnOverlay(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	mintOnce(t, b)
	if fm.calls[0].RoleARN != elevBnd().RoleARN || strings.Contains(fm.calls[0].Policy, "AssumeRole") || rec.rows[0].Elevated {
		t.Fatalf("an elevatable but unelevated session must mint read-only: %+v", fm.calls[0])
	}
}

// Elevated iff at least 16 minutes remain (spec §5.2), even inside the window.
func TestElevatedOnlyWithSixteenMinutesLeft(t *testing.T) {
	b, fm, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(30))
	start := *now
	*now = start.Add(14 * time.Minute) // exactly 16 left
	mintOnce(t, b)
	if fm.calls[0].RoleARN != writeRole {
		t.Fatal("16 min left must still mint write")
	}
	*now = start.Add(14*time.Minute + time.Second) // 15m59s left: the read-only key misses the elevated cache entry
	mintOnce(t, b)
	if fm.calls[1].RoleARN == writeRole {
		t.Fatal("under 16 min left must mint read-only")
	}
}

// The core guarantee: across grant, extend, drop and expiry, no elevated
// credential handed out expires after the window that was current when it
// was minted. Fetches every minute for two hours, with transitions.
func TestNoElevatedExpirationAfterUntil(t *testing.T) {
	b, fm, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	ts := withTimers(b)
	start := *now
	type minted struct{ exp, until time.Time }
	var got []minted
	for m := 0; m <= 120; m++ {
		*now = start.Add(time.Duration(m) * time.Minute)
		switch m {
		case 0:
			b.Elevate("aws-1", cookie(60))
		case 41:
			b.Elevate("aws-1", cookie(30)) // extend with 19 left
		case 60:
			b.Drop("aws-1", CredCookie)
		case 70:
			b.Elevate("aws-1", cookie(30))
		case 100: // the 70-minute grant's timer fires
			for _, tm := range *ts {
				if !tm.stopped {
					tm.f()
				}
			}
		}
		v, on := b.Elevation("aws-1")
		before := len(fm.calls)
		mintOnce(t, b)
		if len(fm.calls) > before && fm.calls[before].RoleARN == writeRole {
			if !on || !v.Active {
				t.Fatalf("minute %d: elevated mint without an active window", m)
			}
			got = append(got, minted{fm.expOf(before), v.Until})
		}
	}
	if len(got) < 4 {
		t.Fatalf("too few elevated mints to mean anything: %d", len(got))
	}
	for i, g := range got {
		if g.exp.After(g.until) {
			t.Fatalf("elevated credential %d expires %s, after its window %s", i, g.exp, g.until)
		}
	}
}

func TestCacheKeyChangesOnGrantExtendDrop(t *testing.T) {
	b, fm, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	mintOnce(t, b) // 1: read-only
	mintOnce(t, b) //    cache hit
	b.Elevate("aws-1", cookie(60))
	mintOnce(t, b) // 2: elevated
	mintOnce(t, b) //    cache hit
	*now = now.Add(time.Minute)
	b.Elevate("aws-1", cookie(60)) // extend: new Until
	mintOnce(t, b)                 // 3: elevated again
	b.Drop("aws-1", CredCookie)
	mintOnce(t, b) // 4: read-only
	if len(fm.calls) != 4 || fm.calls[1].RoleARN != writeRole || fm.calls[2].RoleARN != writeRole || fm.calls[3].RoleARN == writeRole {
		t.Fatalf("mints %d: %+v", len(fm.calls), fm.calls)
	}
}

func TestBackstopDiscardsAnOverlongElevatedCredential(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(30))
	fm.exp = func(in MintInput) time.Time { return now.Add(31 * time.Minute) } // a lying or skewed STS
	w := call(b, tokA("aws-1"))
	if w.Code != 200 || len(fm.calls) != 2 || fm.calls[1].RoleARN == writeRole {
		t.Fatalf("want a read-only re-mint, got %d calls %+v", len(fm.calls), fm.calls)
	}
	var discarded bool
	for _, r := range rec.rows {
		discarded = discarded || (r.Outcome == OutcomeSTSError && r.Elevated && strings.Contains(r.Error, "outlive"))
	}
	if !discarded {
		t.Fatalf("the discarded credential must be audited: %+v", rec.rows)
	}
	if got := lastExpOf(b, "aws-1"); !got.IsZero() {
		t.Fatalf("a discarded credential must not count as handed out: %v", got)
	}
}

func TestWindowChangeDuringMintFallsBackToReadOnly(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(60))
	fm.hook = func(in MintInput) {
		if in.RoleARN == writeRole {
			b.Drop("aws-1", CredCookie)
		}
	}
	w := call(b, tokA("aws-1"))
	if w.Code != 200 || len(fm.calls) != 2 || fm.calls[1].RoleARN == writeRole {
		t.Fatalf("calls %+v", fm.calls)
	}
	if v, _ := b.Elevation("aws-1"); !v.LastValid.IsZero() {
		t.Fatalf("a discarded credential must not count as handed out: %+v", v)
	}
	if len(rec.rows) != 2 {
		t.Fatalf("want a discard row and a read-only mint row, got %+v", rec.rows)
	}
	d, m := rec.rows[0], rec.rows[1]
	if d.Outcome != OutcomeSTSError || !d.Elevated || d.RoleARN != writeRole || !strings.Contains(d.Error, "changed during the mint") {
		t.Fatalf("discard row %+v", d)
	}
	if m.Outcome != OutcomeMinted || m.Elevated || m.RoleARN == writeRole {
		t.Fatalf("fallback row %+v", m)
	}
}

// A replaced overlay (an extend, here) is a different window even though one
// still exists: the credential minted under the old one is discarded.
func TestExtendDuringMintFallsBackToReadOnly(t *testing.T) {
	b, fm, rec, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(60))
	fm.hook = func(in MintInput) {
		if in.RoleARN == writeRole {
			*now = now.Add(time.Second)
			b.Elevate("aws-1", cookie(60))
		}
	}
	if w := call(b, tokA("aws-1")); w.Code != 200 || len(fm.calls) != 2 || fm.calls[1].RoleARN == writeRole {
		t.Fatalf("calls %+v", fm.calls)
	}
	if d := rec.rows[0]; d.Outcome != OutcomeSTSError || !d.Elevated || !strings.Contains(d.Error, "changed during the mint") {
		t.Fatalf("discard row %+v", d)
	}
	if got := lastExpOf(b, "aws-1"); !got.IsZero() {
		t.Fatalf("a discarded credential must not count as handed out: %v", got)
	}
}

// Drop leaves the cache alone; a later window that ends in the same second as
// the dropped one must not be served the dropped window's credential.
func TestNewWindowWithTheSameEndDoesNotReuseTheDroppedCredential(t *testing.T) {
	b, fm, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	start := *now
	b.Elevate("aws-1", cookie(60)) // ends start+60m
	*now = start.Add(30 * time.Minute)
	mintOnce(t, b) // valid until start+45m, so still fresh in the cache after the drop
	b.Drop("aws-1", CredCookie)
	b.Elevate("aws-1", cookie(30)) // also ends start+60m
	mintOnce(t, b)
	if len(fm.calls) != 2 || fm.calls[1].RoleARN != writeRole {
		t.Fatalf("want a fresh elevated mint for the new window, got %d calls", len(fm.calls))
	}
	if got := lastExpOf(b, "aws-1"); !got.Equal(fm.expOf(1)) {
		t.Fatalf("lastExp %v, want %v", got, fm.expOf(1))
	}
}

// A zero Expiration (sts.go maps a nil one to it) proves nothing about the bound.
func TestBackstopDiscardsAnElevatedCredentialWithoutAnExpiration(t *testing.T) {
	b, fm, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	withTimers(b)
	b.Elevate("aws-1", cookie(30))
	fm.exp = func(in MintInput) time.Time {
		if in.RoleARN == writeRole {
			return time.Time{}
		}
		return fm.now().Add(in.Duration)
	}
	if w := call(b, tokA("aws-1")); w.Code != 200 || len(fm.calls) != 2 || fm.calls[1].RoleARN == writeRole {
		t.Fatalf("want a read-only re-mint, got %d calls", len(fm.calls))
	}
	if d := rec.rows[0]; d.Outcome != OutcomeSTSError || !d.Elevated || !strings.Contains(d.Error, "expiration") {
		t.Fatalf("discard row %+v", d)
	}
	if got := lastExpOf(b, "aws-1"); !got.IsZero() {
		t.Fatalf("lastExp %v", got)
	}
}

func TestRefuseElevationIgnoresUnknownSessions(t *testing.T) {
	b, _, rec, _ := setup(t, map[string]SessionView{"aws-1": liveElev()})
	b.RefuseElevation("made-up-1", CredBearer, "x")
	b.RefuseElevation("made-up-2", CredBearer, "x")
	b.mu.Lock()
	n := len(b.refused)
	b.mu.Unlock()
	if len(rec.appr) != 0 || n != 0 {
		t.Fatalf("unknown ids wrote %d rows and %d throttle keys", len(rec.appr), n)
	}
	b.RefuseElevation("aws-1", CredBearer, "x")
	if len(rec.appr) != 1 || rec.appr[0].SessionID != "aws-1" || rec.appr[0].Action != ActionRefused {
		t.Fatalf("a known id writes one row: %+v", rec.appr)
	}
}
