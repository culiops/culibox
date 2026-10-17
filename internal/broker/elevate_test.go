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
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev()})
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
