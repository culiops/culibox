package gitview

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// holdDocker answers every run from bs-like canned output, after blocking a run on /hold until release is closed.
func holdDocker(release <-chan struct{}) *fakeDocker {
	return &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		if slices.Contains(a, "/hold") {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		switch gitSub(a) {
		case "status":
			return okStatus()(ctx, a, w)
		case "cat-file":
			_, err := io.WriteString(w, "x\n")
			return err
		}
		return nil
	}}
}

var holdTgt = Target{Top: "/hold", Common: "/hold/.git"}

func subs(f *fakeDocker) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, a := range f.args {
		out = append(out, gitSub(a))
	}
	return out
}

func TestSlotsServeListBeforeView(t *testing.T) {
	release := make(chan struct{})
	f := holdDocker(release)
	r := newRunner(f, newClock())
	r.MaxParallel = 1
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.StatusFresh(context.Background(), holdTgt) }()
	waitCalls(t, f, 1)
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); r.Show(context.Background(), tgt, "HEAD", "a.go") }()
	}
	time.Sleep(50 * time.Millisecond) // the three shows are waiting for the slot
	wg.Add(1)
	go func() { defer wg.Done(); r.StatusFresh(context.Background(), tgt) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	want := []string{"status", "status", "cat-file", "cat-file", "cat-file"}
	if got := subs(f); !slices.Equal(got, want) {
		t.Fatalf("run order %v, want %v: a waiting status must take the next slot before the editor's shows", got, want)
	}
}

func TestRunTimeoutStartsAtTheSlot(t *testing.T) {
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		d := 200 * time.Millisecond
		if slices.Contains(a, "/hold") {
			d = 250 * time.Millisecond
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	r.MaxParallel = 1
	r.Timeout = 300 * time.Millisecond
	r.SlotWait = 2 * time.Second
	go r.StatusFresh(context.Background(), holdTgt)
	waitCalls(t, f, 1)
	// 250 ms waiting for the slot, then a 200 ms run: within its own 300 ms, which starts at the slot.
	if st := r.StatusFresh(context.Background(), tgt); st.State != "ok" {
		t.Fatalf("status = %+v: the wait for a slot was counted against the run's timeout", st)
	}
}

func TestSlotWaitIsBounded(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := holdDocker(release)
	r := newRunner(f, newClock())
	r.MaxParallel = 1
	r.SlotWait = 100 * time.Millisecond
	go r.StatusFresh(context.Background(), holdTgt)
	waitCalls(t, f, 1)
	t0 := time.Now()
	b := r.Show(context.Background(), tgt, "HEAD", "a.go")
	if b.State != "unavailable" || b.Reason != errNoSlot.Error() {
		t.Fatalf("show = %+v, want unavailable %q", b, errNoSlot.Error())
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("show waited %v for a slot, want about SlotWait", d)
	}
	if st := r.StatusFresh(context.Background(), tgt); st.State != "unavailable" || st.Reason != errNoSlot.Error() {
		t.Fatalf("status = %+v, want unavailable %q", st, errNoSlot.Error())
	}
	if n := f.callCount(); n != 1 {
		t.Fatalf("docker ran %d times, want only the held run", n)
	}
}

func TestSlotWaiterThatGivesUpFreesNothing(t *testing.T) {
	release := make(chan struct{})
	f := holdDocker(release)
	r := newRunner(f, newClock())
	r.MaxParallel = 2
	other := Target{Top: "/hold", Common: "/hold/other.git"} // a second key on the held top: its own run
	var wg sync.WaitGroup
	for _, h := range []Target{holdTgt, other} {
		wg.Add(1)
		go func() { defer wg.Done(); r.StatusFresh(context.Background(), h) }()
	}
	waitCalls(t, f, 2)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if b := r.Show(ctx, tgt, "HEAD", "a.go"); b.State != "unavailable" {
		t.Fatalf("cancelled waiter got %+v", b)
	}
	close(release)
	wg.Wait()
	// Both slots are free again, and only two: two held runs start at once, a third waits.
	release2 := make(chan struct{})
	f.run = holdDocker(release2).run
	n0 := f.callCount()
	for _, h := range []Target{holdTgt, other, {Top: "/hold", Common: "/hold/third.git"}} {
		wg.Add(1)
		go func() { defer wg.Done(); r.StatusFresh(context.Background(), h) }()
	}
	waitCalls(t, f, n0+2)
	time.Sleep(50 * time.Millisecond)
	if n := f.callCount(); n != n0+2 {
		t.Fatalf("%d runs started with 2 slots", n-n0)
	}
	close(release2)
	wg.Wait()
	if n := f.callCount(); n != n0+3 {
		t.Fatalf("runs = %d, want 3", n-n0)
	}
}
