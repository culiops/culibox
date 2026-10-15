package box

import (
	"context"
	"testing"
	"time"
)

func sweepManager(t *testing.T, reachable bool) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/d", Image: "i", Instance: "T", StateRoot: t.TempDir(), Client: f,
		IdleTTL: time.Hour, Reachable: func(context.Context) bool { return reachable }})
	if err != nil {
		t.Fatal(err)
	}
	return m, f
}

func TestSweepRemovesStoppedBoxes(t *testing.T) {
	m, f := sweepManager(t, true)
	f.exited["ab-box-dead"] = true
	rep, err := m.Sweep(context.Background(), nil)
	if err != nil || len(rep.Removed) != 1 || rep.Removed[0].Box != "ab-box-dead" {
		t.Fatalf("stopped box must be removed: %+v %v", rep, err)
	}
}

func TestSweepRemovesRunningOrphans(t *testing.T) {
	m, f := sweepManager(t, true)
	f.running["ab-box-orphan"] = true
	rep, _ := m.Sweep(context.Background(), map[string]int{})
	if len(rep.Removed) != 1 {
		t.Fatalf("a running box with no session, counter or timer is an orphan: %+v", rep)
	}
}

func TestSweepKeepsLiveAndIdleWindowBoxes(t *testing.T) {
	m, f := sweepManager(t, true)
	f.running["ab-box-live"] = true
	f.running["ab-box-idle"] = true
	m.acquire("ab-box-idle")
	m.ReleaseBox("ab-box-idle") // arms the idle timer
	rep, _ := m.Sweep(context.Background(), map[string]int{"ab-box-live": 1})
	if len(rep.Removed) != 0 {
		t.Fatalf("live and idle-window boxes must stay: %+v", rep)
	}
}

// Wrap acquires before the session is registered, so one observation of
// "counter > 0, registry 0" can be a spawn in flight. Only a second consecutive
// sweep may correct it.
func TestSweepCorrectsCounterDriftOnlyOnSecondObservation(t *testing.T) {
	m, f := sweepManager(t, true)
	f.running["ab-box-leak"] = true
	m.acquire("ab-box-leak")
	rep1, _ := m.Sweep(context.Background(), map[string]int{})
	if len(rep1.Corrected) != 0 || len(rep1.Removed) != 0 {
		t.Fatalf("first observation must not act: %+v", rep1)
	}
	rep2, _ := m.Sweep(context.Background(), map[string]int{})
	if len(rep2.Corrected) != 1 {
		t.Fatalf("second consecutive observation must correct: %+v", rep2)
	}
	m.mu.Lock()
	n, armed := m.active["ab-box-leak"], m.timers["ab-box-leak"] != nil
	m.mu.Unlock()
	if n != 0 || !armed {
		t.Fatalf("corrected counter must be 0 with the idle timer armed: n=%d armed=%v", n, armed)
	}
}

func TestSweepDriftResetsWhenNotConsecutive(t *testing.T) {
	m, f := sweepManager(t, true)
	f.running["ab-box-x"] = true
	m.acquire("ab-box-x")
	_, _ = m.Sweep(context.Background(), map[string]int{})              // drift seen
	_, _ = m.Sweep(context.Background(), map[string]int{"ab-box-x": 1}) // registered now
	rep, _ := m.Sweep(context.Background(), map[string]int{})           // seen again, but not consecutively
	if len(rep.Corrected) != 0 {
		t.Fatalf("drift must be consecutive: %+v", rep)
	}
}

func TestSweepSkipsWhenDaemonUnreachable(t *testing.T) {
	m, f := sweepManager(t, false)
	f.exited["ab-box-dead"] = true
	rep, err := m.Sweep(context.Background(), nil)
	if err != nil || !rep.Skipped || len(rep.Removed) != 0 || f.stops() != 0 {
		t.Fatalf("an unreachable daemon means skip, never reap: %+v %v", rep, err)
	}
}

// A failed removal must not abort the pass, must not drop the box's bookkeeping,
// and is reported.
func TestSweepContinuesPastAFailedRemoval(t *testing.T) {
	m, f := sweepManager(t, true)
	f.exited["ab-box-a"] = true
	f.exited["ab-box-b"] = true
	f.stopFails = 1
	rep, err := m.Sweep(context.Background(), nil)
	if err == nil {
		t.Fatal("the failed removal must be reported")
	}
	if len(rep.Removed) != 1 || f.stops() != 2 {
		t.Fatalf("the other box must still be removed: %+v stops=%d", rep, f.stops())
	}
}

// A stopped box whose sessions are still registered keeps its count, exactly as
// ensure's stopped-box path does. Dropping it would let a stale session's late
// release take the count of the re-created box to zero and arm the idle timer
// under a live session.
func TestSweepOfAStoppedBoxKeepsItsCounterAgainstAStaleRelease(t *testing.T) {
	m, f := sweepManager(t, true)
	dir := t.TempDir()
	_, _, name, err := m.Wrap(context.Background(), dir, "d-1", []string{"claude"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A docker restart leaves the container exited; the detached session D is
	// still registered.
	f.mu.Lock()
	delete(f.running, name)
	f.exited[name] = true
	f.mu.Unlock()

	rep, err := m.Sweep(context.Background(), map[string]int{name: 1})
	if err != nil || len(rep.Removed) != 1 {
		t.Fatalf("the stopped box must be removed: %+v %v", rep, err)
	}
	if _, _, _, err := m.Wrap(context.Background(), dir, "n-1", []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 2 {
		t.Fatalf("ensure must re-create the box, runs=%d", f.runs())
	}
	m.ReleaseBox(name) // the stale session D is killed

	m.mu.Lock()
	n, armed := m.active[name], m.timers[name] != nil
	m.mu.Unlock()
	if n != 1 || armed {
		t.Fatalf("the new session must keep the box: active=%d idleTimerArmed=%v", n, armed)
	}
}
