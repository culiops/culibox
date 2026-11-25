package session

import (
	"sync"
	"testing"
	"time"
)

// fakeAuto is an autoReturner with a fake clock, fake timers, a scripted
// fingerprint and a recording run. Timers never fire on their own: tests call
// fire.
type fakeAuto struct {
	t      *testing.T
	a      *autoReturner
	mu     sync.Mutex
	clock  time.Time
	timers []*fakeTimer
	fp     string
	fpOK   bool
	fpN    int // fingerprint calls: one per turnEnded and one per fire
	ran    chan *Session
	block  chan struct{} // when set, a run waits for it to close
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (f *fakeTimer) Stop() bool {
	was := !f.stopped
	f.stopped = true
	return was
}

func newFakeAuto(t *testing.T) *fakeAuto {
	f := &fakeAuto{t: t, clock: time.Unix(1_000_000, 0), fp: "a", fpOK: true, ran: make(chan *Session, 16)}
	f.a = newAutoReturner(
		func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock },
		func(d time.Duration, fn func()) stopper {
			f.mu.Lock()
			defer f.mu.Unlock()
			tm := &fakeTimer{d: d, f: fn}
			f.timers = append(f.timers, tm)
			return tm
		},
		func(string) (string, bool) { f.mu.Lock(); defer f.mu.Unlock(); f.fpN++; return f.fp, f.fpOK },
		func(s *Session) {
			f.ran <- s
			f.mu.Lock()
			b := f.block
			f.mu.Unlock()
			if b != nil {
				<-b
			}
		},
	)
	return f
}

func (f *fakeAuto) advance(d time.Duration) { f.mu.Lock(); f.clock = f.clock.Add(d); f.mu.Unlock() }
func (f *fakeAuto) setFP(fp string, ok bool) {
	f.mu.Lock()
	f.fp, f.fpOK = fp, ok
	f.mu.Unlock()
}

// armed returns the timers that are neither stopped nor fired.
func (f *fakeAuto) armed() []*fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeTimer
	for _, tm := range f.timers {
		if !tm.stopped {
			out = append(out, tm)
		}
	}
	return out
}

// fire advances the clock by the timer's delay and runs its callback.
func (f *fakeAuto) fire(tm *fakeTimer) {
	f.advance(tm.d)
	tm.stopped = true
	tm.f()
}

func (f *fakeAuto) expectRun(want *Session) {
	f.t.Helper()
	select {
	case got := <-f.ran:
		if got != want {
			f.t.Fatalf("run for %p, want %p", got, want)
		}
	case <-time.After(2 * time.Second):
		f.t.Fatal("run did not happen")
	}
}

func (f *fakeAuto) expectNoRun() {
	f.t.Helper()
	select {
	case <-f.ran:
		f.t.Fatal("unexpected run")
	case <-time.After(20 * time.Millisecond):
	}
}

func autoSess(cwd string) *Session { return &Session{ID: cwd, Cwd: cwd, status: statusLive} }

func TestAutoReturnChangedFingerprintDebounce(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	arm := f.armed()
	if len(arm) != 1 || arm[0].d != 10*time.Second {
		t.Fatalf("armed = %+v, want one timer at +10s", arm)
	}
	f.fire(arm[0])
	f.expectRun(s)
	f.expectNoRun()
}

func TestAutoReturnThrottle30s(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	f.fire(f.armed()[0])
	f.expectRun(s)
	f.setFP("b", true)
	f.advance(5 * time.Second)
	f.a.turnEnded(s)
	arm := f.armed()
	// lastRun was t+10s (fire time); now is t+15s; due = lastRun+30s = +25s from now.
	if len(arm) != 1 || arm[0].d != 25*time.Second {
		t.Fatalf("armed = %+v, want one timer 25s out (lastRun+30s)", arm)
	}
}

func TestAutoReturnUnchangedWaits5m(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	f.fire(f.armed()[0]) // records fp "a" and lastRun
	f.expectRun(s)
	f.advance(time.Minute)
	f.a.turnEnded(s) // fp still "a"
	arm := f.armed()
	// lastRun+5m = 4m from now.
	if len(arm) != 1 || arm[0].d != 4*time.Minute {
		t.Fatalf("armed = %+v, want one timer 4m out", arm)
	}
}

func TestAutoReturnUnknownFingerprintIsChanged(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.setFP("", false)
	f.a.turnEnded(s)
	f.fire(f.armed()[0])
	f.expectRun(s)
	f.advance(time.Minute)
	f.a.turnEnded(s) // still unknown: must not take the 5 min branch
	arm := f.armed()
	if len(arm) != 1 || arm[0].d != 10*time.Second {
		t.Fatalf("armed = %+v, want the short throttle (10s: lastRun+30s already past)", arm)
	}
}

func TestAutoReturnKeepsEarlierTimer(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	first := f.armed()[0]
	f.advance(2 * time.Second)
	f.a.turnEnded(s) // new due is now+10s = later than the armed one: keep
	arm := f.armed()
	if len(arm) != 1 || arm[0] != first || first.stopped {
		t.Fatalf("the earlier timer was not kept: %+v", arm)
	}
}

func TestAutoReturnReplacesLaterTimer(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	f.fire(f.armed()[0])
	f.expectRun(s)
	f.advance(time.Second)
	f.a.turnEnded(s) // fp unchanged: due lastRun+5m
	late := f.armed()[0]
	f.setFP("b", true)
	f.advance(time.Second)
	f.a.turnEnded(s) // changed now: due lastRun+30s, earlier
	arm := f.armed()
	if !late.stopped || len(arm) != 1 || arm[0] == late {
		t.Fatalf("later timer not replaced: late.stopped=%v armed=%d", late.stopped, len(arm))
	}
}

func TestAutoReturnOneTimerPerClone(t *testing.T) {
	f := newFakeAuto(t)
	s1, s2 := autoSess("/c"), autoSess("/c/")
	f.a.turnEnded(s1)
	f.a.turnEnded(s2)
	if n := len(f.armed()); n != 1 {
		t.Fatalf("%d timers armed for one clone, want 1", n)
	}
	f.fire(f.armed()[0])
	f.expectRun(s2) // the session that last ended a turn
	f.expectNoRun()
	// A different clone has its own timer.
	f.a.turnEnded(autoSess("/d"))
	if n := len(f.armed()); n != 1 {
		t.Fatalf("armed = %d, want the other clone's timer only", n)
	}
}

func TestAutoReturnForgetStops(t *testing.T) {
	f := newFakeAuto(t)
	s1, s2 := autoSess("/c"), autoSess("/c")
	f.a.turnEnded(s1)
	f.a.turnEnded(s2)
	tm := f.armed()[0]
	f.a.forget(s1)
	if tm.stopped {
		t.Fatal("timer stopped while another session of the clone remains")
	}
	f.a.forget(s2)
	if !tm.stopped {
		t.Fatal("timer not stopped when the last session of the clone went")
	}
	// A stale callback that fires anyway must not run.
	tm.f()
	f.expectNoRun()
}

func TestAutoReturnFireRunsRemainingSession(t *testing.T) {
	f := newFakeAuto(t)
	s1, s2 := autoSess("/c"), autoSess("/c")
	f.a.turnEnded(s1)
	f.a.attach(s2)
	f.a.forget(s1) // the arming session is gone; s2 remains
	f.fire(f.armed()[0])
	f.expectRun(s2)
}

func TestAutoReturnNoOverlappingRuns(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	gate := make(chan struct{})
	f.mu.Lock()
	f.block = gate
	f.mu.Unlock()
	f.a.turnEnded(s)
	f.fire(f.armed()[0])
	f.expectRun(s) // blocked: the run is in flight
	f.a.turnEnded(s)
	arm := f.armed()
	if len(arm) != 1 {
		t.Fatalf("armed = %d", len(arm))
	}
	f.fire(arm[0])
	f.expectNoRun() // skipped while the previous run is in flight
	close(gate)
	deadline := time.Now().Add(2 * time.Second)
	for len(f.armed()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the skipped return was not re-armed when the run ended")
		}
		time.Sleep(time.Millisecond)
	}
}

// A timer that fires as its session ends must not start a run: its idle summary
// could overwrite the exit return's.
func TestAutoReturnSkipsASessionThatIsNoLongerLive(t *testing.T) {
	for _, status := range []string{statusExited, statusDetached} {
		t.Run(status, func(t *testing.T) {
			f := newFakeAuto(t)
			s := autoSess("/c")
			f.a.turnEnded(s)
			arm := f.armed()[0]
			s.mu.Lock()
			s.status = status
			s.mu.Unlock()
			f.fire(arm)
			f.expectNoRun()
			deadline := time.Now().Add(2 * time.Second)
			for {
				f.a.mu.Lock()
				st := f.a.clones["/c"]
				running := st != nil && st.running
				f.a.mu.Unlock()
				if !running {
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the skipped run was never marked done")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// The session that last ended a turn is gone but another of the clone is live:
// the live one runs. With none live nothing runs and no run is stamped, so the
// next turn still gets its full debounce.
func TestAutoReturnFirePicksALiveSession(t *testing.T) {
	f := newFakeAuto(t)
	s1, s2 := autoSess("/c"), autoSess("/c")
	f.a.attach(s2)
	f.a.turnEnded(s1) // s1 is the one that last ended a turn
	s1.mu.Lock()
	s1.status = statusExited
	s1.mu.Unlock()
	f.fire(f.armed()[0])
	f.expectRun(s2)
}

func TestAutoReturnFireWithNoLiveSessionStampsNothing(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	s.mu.Lock()
	s.status = statusDetached
	s.mu.Unlock()
	f.fire(f.armed()[0])
	f.expectNoRun()
	f.a.mu.Lock()
	st := f.a.clones["/c"]
	stamped := !st.lastRun.IsZero() || st.haveFP || st.running
	f.a.mu.Unlock()
	if stamped {
		t.Fatal("a fire with no live session recorded a completed run")
	}
}

func TestAutoReturnRunWrapperEndsRun(t *testing.T) {
	// The goroutine fire starts must call finish itself.
	f := newFakeAuto(t)
	s := autoSess("/c")
	f.a.turnEnded(s)
	f.fire(f.armed()[0])
	f.expectRun(s)
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.a.mu.Lock()
		running := f.a.clones["/c"].running
		f.a.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never marked done")
		}
		time.Sleep(time.Millisecond)
	}
}

// runWatch drives watchTurnsWith with a scripted working sequence.
func runWatch(t *testing.T, f *fakeAuto, s *Session, seq []bool) {
	t.Helper()
	tick := make(chan time.Time)
	i := 0
	working := func() bool { v := seq[i]; i++; return v }
	exited := make(chan struct{})
	go func() {
		watchTurnsWith(f.a, s, tick, working)
		close(exited)
	}()
	for range seq {
		tick <- time.Time{}
	}
	// One more send returns only once the previous tick was fully handled.
	if s.status == statusLive {
		close(s.done)
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit on Done")
	}
}

func TestWatchTurnsFiresOnWorkingToIdle(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	s.done = make(chan struct{})
	s.detachedCh = make(chan struct{})
	runWatch(t, f, s, []bool{true, true, false, false})
	// turnEnded takes one fingerprint per call and a second call would keep the
	// armed timer, so the timer count alone cannot see a double fire.
	f.mu.Lock()
	calls := f.fpN
	f.mu.Unlock()
	if n := len(f.timers); n != 1 || calls != 1 {
		t.Fatalf("turnEnded fired %d times (fingerprints), %d timers armed; want exactly 1 and 1", calls, n)
	}
	// The watcher exited: the clone's timer is gone with the last session.
	if n := len(f.armed()); n != 0 {
		t.Fatalf("timer left armed after the session's watcher exited: %d", n)
	}
}

func TestWatchTurnsNoFireWhenNeverWorking(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	s.done = make(chan struct{})
	s.detachedCh = make(chan struct{})
	runWatch(t, f, s, []bool{false, false, false})
	if len(f.timers) != 0 {
		t.Fatalf("timers = %d, want 0", len(f.timers))
	}
}

func TestWatchTurnsNoFireWhenExited(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	s.done = make(chan struct{})
	s.detachedCh = make(chan struct{})
	tick := make(chan time.Time)
	seq := []bool{true, false}
	i := 0
	exited := make(chan struct{})
	go func() {
		watchTurnsWith(f.a, s, tick, func() bool {
			v := seq[i]
			i++
			if i == 2 { // the agent exits as its turn ends
				s.mu.Lock()
				s.status = statusExited
				s.mu.Unlock()
			}
			return v
		})
		close(exited)
	}()
	tick <- time.Time{}
	tick <- time.Time{}
	close(s.done)
	<-exited
	if len(f.timers) != 0 {
		t.Fatalf("an exited session armed a return: %d", len(f.timers))
	}
}

func TestWatchTurnsExitsOnDetach(t *testing.T) {
	f := newFakeAuto(t)
	s := autoSess("/c")
	s.done = make(chan struct{})
	s.detachedCh = make(chan struct{})
	exited := make(chan struct{})
	go func() {
		watchTurnsWith(f.a, s, make(chan time.Time), func() bool { return false })
		close(exited)
	}()
	close(s.detachedCh)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit on detach")
	}
}

func TestSessionWorkingFollowsPTYWithoutRegistry(t *testing.T) {
	m := &Manager{sessions: map[string]*Session{}}
	busy := &Session{ID: "a", status: statusLive, lastActivityAt: time.Now()}
	if !m.sessionWorking(busy) {
		t.Fatal("a session with fresh PTY output and no registry should read working")
	}
	quiet := &Session{ID: "b", status: statusLive, lastActivityAt: time.Now().Add(-time.Hour)}
	if m.sessionWorking(quiet) {
		t.Fatal("a silent session should not read working")
	}
	gone := &Session{ID: "c", status: statusExited, exited: true, lastActivityAt: time.Now()}
	if m.sessionWorking(gone) {
		t.Fatal("an exited session should not read working")
	}
}
