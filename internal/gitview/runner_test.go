package gitview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeDocker stands in for the docker CLI: it records argv and removals and
// answers each RunStream through run.
type fakeDocker struct {
	mu      sync.Mutex
	calls   int
	args    [][]string
	removed []string
	run     func(ctx context.Context, args []string, w io.Writer) error
}

func (f *fakeDocker) RunStream(ctx context.Context, args []string, w io.Writer) error {
	f.mu.Lock()
	f.calls++
	f.args = append(f.args, slices.Clone(args))
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return nil
	}
	return run(ctx, args, w)
}

func (f *fakeDocker) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeDocker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeClock is a settable Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

var tgt = Target{Top: "/repo", Common: "/repo/.git"}

// porcelain is one modified and one untracked path.
const porcelain = "# branch.oid abc\x00# branch.head main\x00" +
	"1 .M N... 100644 100644 100644 a b src/a.go\x00? new.txt\x00"

func okStatus() func(context.Context, []string, io.Writer) error {
	return func(_ context.Context, _ []string, w io.Writer) error {
		_, err := io.WriteString(w, porcelain)
		return err
	}
}

func newRunner(f *fakeDocker, c *fakeClock) *Runner {
	return &Runner{Docker: f, Image: "img:1", UID: 1000, GID: 1000, Now: c.now}
}

func TestStatusParses(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	r := newRunner(f, newClock())
	st := r.Status(context.Background(), tgt)
	if st.State != "ok" || st.Top != "/repo" || st.NoHead {
		t.Fatalf("status = %+v", st)
	}
	want := []Entry{{P: "src/a.go", X: "M"}, {P: "new.txt", X: "U"}}
	if !slices.Equal(st.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", st.Entries, want)
	}
	// The argv is the Task 2 builder's, with a random name.
	a := f.args[0]
	i := slices.Index(a, "--name")
	if i < 0 || !strings.HasPrefix(a[i+1], "agent-box-git-") || len(a[i+1]) != len("agent-box-git-")+16 {
		t.Fatalf("container name in %q", a)
	}
	if !slices.Equal(a, StatusArgs(tgt, "img:1", a[i+1], 1000, 1000)) {
		t.Fatalf("argv is not StatusArgs: %q", a)
	}
}

func TestStatusEntriesNeverNil(t *testing.T) {
	cases := map[string]struct {
		run   func(context.Context, []string, io.Writer) error
		state string
	}{
		"clean": {func(context.Context, []string, io.Writer) error { return nil }, "ok"},
		"notrepo": {func(context.Context, []string, io.Writer) error {
			return errors.New("docker run: exit status 128: fatal: not a git repository (or any of the parent directories): .git")
		}, "notrepo"},
		"unavailable": {func(context.Context, []string, io.Writer) error { return errors.New("docker run: boom") }, "unavailable"},
		"toomany": {func(_ context.Context, _ []string, w io.Writer) error {
			var b bytes.Buffer
			for i := 0; i < maxStatusEntries+1; i++ {
				fmt.Fprintf(&b, "? f%d\x00", i)
			}
			_, err := w.Write(b.Bytes())
			return err
		}, "toomany"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRunner(&fakeDocker{run: c.run}, newClock())
			st := r.Status(context.Background(), tgt)
			if st.State != c.state {
				t.Fatalf("state = %q (%s), want %q", st.State, st.Reason, c.state)
			}
			if st.Entries == nil {
				t.Fatalf("Entries is nil for %s", name)
			}
			if c.state != "ok" && len(st.Entries) != 0 {
				t.Fatalf("Entries = %d for %s", len(st.Entries), name)
			}
		})
	}
}

func TestStatusRefusesUnsafeTarget(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	r := newRunner(f, newClock())
	bad := Target{Top: "/repo,readonly=false", Common: "/repo/.git"}
	st := r.Status(context.Background(), bad)
	if st.State != "unavailable" || st.Reason == "" || st.Entries == nil {
		t.Fatalf("status = %+v", st)
	}
	d := r.Diff(context.Background(), bad, "a.go", false)
	if d.State != "unavailable" || d.Reason == "" {
		t.Fatalf("diff = %+v", d)
	}
	if f.callCount() != 0 {
		t.Fatalf("docker called %d times for an unsafe target", f.callCount())
	}
}

func TestStatusSingleFlight(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	var wg sync.WaitGroup
	res := make([]Status, 10)
	for i := range res {
		wg.Add(1)
		go func() { defer wg.Done(); res[i] = r.Status(context.Background(), tgt) }()
	}
	// Let every caller reach the flight before the run finishes.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := f.callCount(); n != 1 {
		t.Fatalf("RunStream called %d times, want 1", n)
	}
	for i, s := range res {
		if s.State != "ok" || len(s.Entries) != 2 {
			t.Fatalf("caller %d got %+v", i, s)
		}
	}
}

func TestStatusReuseWindow(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	c := newClock()
	r := newRunner(f, c)
	r.Status(context.Background(), tgt)
	c.add(1900 * time.Millisecond)
	r.Status(context.Background(), tgt)
	if n := f.callCount(); n != 1 {
		t.Fatalf("within the reuse window: %d runs, want 1", n)
	}
	c.add(200 * time.Millisecond) // 2.1 s after the run
	r.Status(context.Background(), tgt)
	if n := f.callCount(); n != 2 {
		t.Fatalf("after the reuse window: %d runs, want 2", n)
	}
}

func TestStatusReuseResultIsACopy(t *testing.T) {
	r := newRunner(&fakeDocker{run: okStatus()}, newClock())
	a := r.Status(context.Background(), tgt)
	a.Entries[0].P = "mutated"
	b := r.Status(context.Background(), tgt)
	if b.Entries[0].P != "src/a.go" {
		t.Fatalf("a caller's edit reached the cache: %+v", b.Entries)
	}
}

func TestStatusIdleSweep(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	c := newClock()
	r := newRunner(f, c)
	other := Target{Top: "/other", Common: "/other/.git"}
	r.Status(context.Background(), tgt)
	r.Status(context.Background(), other)
	// Keep tgt warm, let other idle past 5 min.
	for i := 0; i < 6; i++ {
		c.add(time.Minute)
		r.Status(context.Background(), tgt)
	}
	r.mu.Lock()
	_, keptTgt := r.cache[tgt.key()]
	_, keptOther := r.cache[other.key()]
	r.mu.Unlock()
	if !keptTgt || keptOther {
		t.Fatalf("cache after idle: tgt=%v other=%v, want true/false", keptTgt, keptOther)
	}
}

func TestStatusTimeoutUnavailable(t *testing.T) {
	f := &fakeDocker{run: func(ctx context.Context, _ []string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	r := newRunner(f, newClock())
	r.Timeout = 50 * time.Millisecond
	st := r.Status(context.Background(), tgt)
	if st.State != "unavailable" || st.Reason == "" || st.Entries == nil {
		t.Fatalf("status = %+v", st)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.Index(f.args[0], "--name")
	if len(f.removed) != 1 || f.removed[0] != f.args[0][i+1] {
		t.Fatalf("Remove calls %q, want the run's container %q", f.removed, f.args[0][i+1])
	}
}

func TestStatusCallerCancelDoesNotPoisonFlight(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan Status)
	go func() { done <- r.Status(cctx, tgt) }()
	time.Sleep(50 * time.Millisecond)
	second := make(chan Status)
	go func() { second <- r.Status(context.Background(), tgt) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if st := <-done; st.State != "unavailable" {
		t.Fatalf("cancelled caller got %+v", st)
	}
	close(release)
	if st := <-second; st.State != "ok" {
		t.Fatalf("other caller got %+v", st)
	}
}

func TestStatusParallelCap(t *testing.T) {
	var cur, peak atomic.Int32
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		cur.Add(-1)
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	r.MaxParallel = 2
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		top := fmt.Sprintf("/repo%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st := r.Status(context.Background(), Target{Top: top, Common: top + "/.git"}); st.State != "ok" {
				t.Errorf("%s: %+v", top, st)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p < 1 || p > 2 {
		t.Fatalf("peak concurrency = %d, want 1..2", p)
	}
	if n := f.callCount(); n != 3 {
		t.Fatalf("runs = %d, want 3", n)
	}
}

func TestDiffTruncates(t *testing.T) {
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		_, err := w.Write(bytes.Repeat([]byte("a"), 3<<20))
		if err != nil {
			// Like ExecDocker: a refusing writer stops the container and the error surfaces.
			return fmt.Errorf("docker run: writing its output: %w", err)
		}
		return nil
	}}
	r := newRunner(f, newClock())
	d := r.Diff(context.Background(), tgt, "big.txt", false)
	if d.State != "ok" || !d.Truncated || len(d.Diff) != 2<<20 || d.Kind != "text" {
		t.Fatalf("diff state=%q truncated=%v len=%d kind=%q reason=%q", d.State, d.Truncated, len(d.Diff), d.Kind, d.Reason)
	}
	// Not a failure, but the cut-off docker CLI leaves its container running.
	if len(f.removed) != 1 {
		t.Fatalf("cap hit: Remove calls %q, want the run's container", f.removed)
	}
}

func TestDiffTruncatesAcrossWrites(t *testing.T) {
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		chunk := bytes.Repeat([]byte("b"), 1<<20)
		for i := 0; i < 3; i++ {
			if _, err := w.Write(chunk); err != nil {
				return fmt.Errorf("docker run: writing its output: %w", err)
			}
		}
		return nil
	}}
	d := newRunner(f, newClock()).Diff(context.Background(), tgt, "big.txt", false)
	if !d.Truncated || len(d.Diff) != 2<<20 || d.State != "ok" {
		t.Fatalf("diff truncated=%v len=%d state=%q", d.Truncated, len(d.Diff), d.State)
	}
}

func TestDiffKinds(t *testing.T) {
	const text = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	const bin = "diff --git a/x b/x\nindex 1..2 100644\nBinary files a/x and b/x differ\n"
	for _, c := range []struct{ out, kind string }{{text, "text"}, {bin, "binary"}, {"", "unchanged"}} {
		f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
			_, err := io.WriteString(w, c.out)
			return err
		}}
		d := newRunner(f, newClock()).Diff(context.Background(), tgt, "x", false)
		if d.State != "ok" || d.Kind != c.kind || d.Diff != c.out || d.Truncated {
			t.Errorf("out %q: %+v, want kind %q", c.out, d, c.kind)
		}
	}
}

func TestDiffArgsAndPathRefusal(t *testing.T) {
	f := &fakeDocker{}
	r := newRunner(f, newClock())
	r.Diff(context.Background(), tgt, "src/a.go", true)
	a := f.args[0]
	i := slices.Index(a, "--name")
	if !slices.Equal(a, DiffArgs(tgt, "img:1", a[i+1], 1000, 1000, "src/a.go", true)) {
		t.Fatalf("argv is not DiffArgs: %q", a)
	}
	for _, rel := range []string{"", "/etc/passwd", "../x", "a/../../x", "a\x00b"} {
		before := f.callCount()
		if d := r.Diff(context.Background(), tgt, rel, false); d.State != "unavailable" {
			t.Errorf("rel %q: %+v", rel, d)
		}
		if f.callCount() != before {
			t.Errorf("rel %q reached docker", rel)
		}
	}
}

// exitErr is what ExecDocker surfaces for a container that exited with code.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("no ExitError: %v", err)
	}
	return fmt.Errorf("docker run: %w: ", err)
}

func TestDiffUntrackedExitOneIsSuccess(t *testing.T) {
	const out = "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+hi\n"
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		io.WriteString(w, out)
		return exitErr(t, 1)
	}}
	r := newRunner(f, newClock())
	d := r.Diff(context.Background(), tgt, "new.txt", true)
	if d.State != "ok" || d.Kind != "text" || d.Diff != out {
		t.Fatalf("untracked exit 1: %+v", d)
	}
	if len(f.removed) != 0 {
		t.Fatalf("exit 1 of --no-index with output: Remove calls %q, want none (--rm cleans up)", f.removed)
	}
	// The same exit on a tracked diff is a failure, and so is any other code.
	if d := r.Diff(context.Background(), tgt, "a.go", false); d.State != "unavailable" {
		t.Fatalf("tracked exit 1: %+v", d)
	}
	f.run = func(context.Context, []string, io.Writer) error { return exitErr(t, 2) }
	if d := r.Diff(context.Background(), tgt, "new.txt", true); d.State != "unavailable" {
		t.Fatalf("untracked exit 2: %+v", d)
	}
}

func TestDiffFailureRemovesContainer(t *testing.T) {
	f := &fakeDocker{run: func(context.Context, []string, io.Writer) error { return errors.New("docker run: boom") }}
	d := newRunner(f, newClock()).Diff(context.Background(), tgt, "a.go", false)
	if d.State != "unavailable" || d.Reason == "" || len(f.removed) != 1 {
		t.Fatalf("diff %+v removed %q", d, f.removed)
	}
}

func TestDiffTruncationKeepsWholeRunes(t *testing.T) {
	// 2 MiB - 1 ASCII bytes, then a 3-byte rune that straddles the cap.
	out := strings.Repeat("a", maxDiffBytes-1) + "€€"
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		_, err := io.WriteString(w, out)
		return err
	}}
	d := newRunner(f, newClock()).Diff(context.Background(), tgt, "x", false)
	if !d.Truncated || !utf8.ValidString(d.Diff) || len(d.Diff) != maxDiffBytes-1 {
		t.Fatalf("truncated=%v valid=%v len=%d", d.Truncated, utf8.ValidString(d.Diff), len(d.Diff))
	}
}

func TestStatusToomanyViaCapRemovesContainer(t *testing.T) {
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		_, err := w.Write(bytes.Repeat([]byte("x"), maxStatusBytes+1))
		if err != nil {
			return fmt.Errorf("docker run: writing its output: %w", err)
		}
		return nil
	}}
	st := newRunner(f, newClock()).Status(context.Background(), tgt)
	if st.State != "toomany" || st.Entries == nil || len(st.Entries) != 0 {
		t.Fatalf("status = %+v", st)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.Index(f.args[0], "--name")
	if len(f.removed) != 1 || f.removed[0] != f.args[0][i+1] {
		t.Fatalf("Remove calls %q, want the run's container", f.removed)
	}
}

func TestStatusKeyedByTopAndCommon(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	r := newRunner(f, newClock())
	a := Target{Top: "/repo", Common: "/repo/.git"}
	b := Target{Top: "/repo", Common: "/elsewhere/.git"}
	r.Status(context.Background(), a)
	r.Status(context.Background(), b)
	if n := f.callCount(); n != 2 {
		t.Fatalf("different Common, same Top: %d runs, want 2", n)
	}
	r.Status(context.Background(), a)
	r.Status(context.Background(), b)
	if n := f.callCount(); n != 2 {
		t.Fatalf("repeat of each: %d runs, want 2 (both cached)", n)
	}
}

// waitCalls blocks until the fake docker has been called n times.
func waitCalls(t *testing.T, f *fakeDocker, n int) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f.callCount() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("docker called %d times, want %d", f.callCount(), n)
}

func TestStatusFreshIgnoresReuseWindow(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	c := newClock()
	r := newRunner(f, c)
	r.Status(context.Background(), tgt)
	c.add(100 * time.Millisecond) // well inside the 2 s reuse window
	r.Status(context.Background(), tgt)
	if n := f.callCount(); n != 1 {
		t.Fatalf("plain call in the window: %d runs, want 1", n)
	}
	if st := r.StatusFresh(context.Background(), tgt); st.State != "ok" {
		t.Fatalf("fresh status = %+v", st)
	}
	if n := f.callCount(); n != 2 {
		t.Fatalf("fresh call in the window: %d runs, want 2", n)
	}
	// Its result is cached for later plain callers.
	r.Status(context.Background(), tgt)
	if n := f.callCount(); n != 2 {
		t.Fatalf("plain call after a fresh run: %d runs, want 2", n)
	}
}

func TestStatusFreshDoesNotJoinEarlierRun(t *testing.T) {
	release := make(chan struct{})
	var phase atomic.Int32
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		if phase.Add(1) == 1 {
			<-release // the early run reads the tree "before the mutation"
			_, err := io.WriteString(w, "? old.txt\x00")
			return err
		}
		_, err := io.WriteString(w, "? new.txt\x00")
		return err
	}}
	c := newClock()
	r := newRunner(f, c)
	plain := make(chan Status)
	go func() { plain <- r.Status(context.Background(), tgt) }()
	waitCalls(t, f, 1)
	c.add(10 * time.Millisecond) // the mutation, then the fresh request
	fresh := r.StatusFresh(context.Background(), tgt)
	if n := f.callCount(); n != 2 {
		t.Fatalf("fresh joined the earlier run: %d runs, want 2", n)
	}
	if len(fresh.Entries) != 1 || fresh.Entries[0].P != "new.txt" {
		t.Fatalf("fresh got %+v, want the second run's result", fresh.Entries)
	}
	close(release)
	if st := <-plain; len(st.Entries) != 1 || st.Entries[0].P != "old.txt" {
		t.Fatalf("plain caller got %+v", st.Entries)
	}
	// The older run finishing last must not replace the newer result in the cache.
	if st := r.Status(context.Background(), tgt); st.Entries[0].P != "new.txt" {
		t.Fatalf("cache holds %+v, want the newer run", st.Entries)
	}
}

func TestStatusFreshSharesOneNewRun(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	var wg sync.WaitGroup
	res := make([]Status, 6)
	for i := range res {
		wg.Add(1)
		go func() { defer wg.Done(); res[i] = r.StatusFresh(context.Background(), tgt) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := f.callCount(); n != 1 {
		t.Fatalf("concurrent fresh calls: %d runs, want 1", n)
	}
	for i, s := range res {
		if s.State != "ok" || len(s.Entries) != 2 {
			t.Fatalf("caller %d got %+v", i, s)
		}
	}
}

func TestStatusFreshJoinsRunStartedAtOrAfterRequest(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okStatus()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	plain := make(chan Status)
	go func() { plain <- r.Status(context.Background(), tgt) }()
	waitCalls(t, f, 1)
	fresh := make(chan Status)
	go func() { fresh <- r.StatusFresh(context.Background(), tgt) }() // same instant on the fake clock
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-plain
	<-fresh
	if n := f.callCount(); n != 1 {
		t.Fatalf("fresh request at the run's start instant: %d runs, want 1", n)
	}
}

func TestStatusFreshRespectsParallelCap(t *testing.T) {
	var cur, peak atomic.Int32
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		cur.Add(-1)
		return okStatus()(ctx, a, w)
	}}
	c := newClock()
	r := newRunner(f, c)
	r.MaxParallel = 1
	done := make(chan Status)
	go func() { done <- r.Status(context.Background(), tgt) }()
	waitCalls(t, f, 1)
	c.add(time.Millisecond)
	if st := r.StatusFresh(context.Background(), tgt); st.State != "ok" {
		t.Fatalf("fresh = %+v", st)
	}
	<-done
	if p := peak.Load(); p != 1 || f.callCount() != 2 {
		t.Fatalf("peak = %d runs = %d, want 1 and 2", p, f.callCount())
	}
}

func TestDiffUntrackedExitOneKeepsRemoveWhenNoOutputOrCapped(t *testing.T) {
	// Exit 1 without output is not "the files differ": remove as before.
	f := &fakeDocker{run: func(context.Context, []string, io.Writer) error { return exitErr(t, 1) }}
	newRunner(f, newClock()).Diff(context.Background(), tgt, "new.txt", true)
	if len(f.removed) != 1 {
		t.Fatalf("exit 1 without output: Remove calls %q, want 1", f.removed)
	}
	// A cap hit is removed too: the container may still be running.
	f = &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		_, err := w.Write(bytes.Repeat([]byte("x"), maxDiffBytes+1))
		return fmt.Errorf("docker run: %w", err)
	}}
	d := newRunner(f, newClock()).Diff(context.Background(), tgt, "new.txt", true)
	if !d.Truncated || len(f.removed) != 1 {
		t.Fatalf("capped untracked diff: truncated=%v removed=%q", d.Truncated, f.removed)
	}
}

func TestMissingImageIsAFixedReason(t *testing.T) {
	for _, stderr := range []string{
		"docker run: exit status 125: Unable to find image 'img:1' locally\ndocker: Error response from daemon: pull access denied",
		"docker run: exit status 125: docker: Error response from daemon: No such image: img:1.",
	} {
		f := &fakeDocker{run: func(context.Context, []string, io.Writer) error { return errors.New(stderr) }}
		r := newRunner(f, newClock())
		if st := r.Status(context.Background(), tgt); st.State != "unavailable" || st.Reason != ReasonNoImage {
			t.Fatalf("status = %+v for %q", st, stderr)
		}
		if d := r.Diff(context.Background(), tgt, "a.go", false); d.State != "unavailable" || d.Reason != ReasonNoImage {
			t.Fatalf("diff = %+v for %q", d, stderr)
		}
	}
}
