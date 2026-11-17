package gitview

import (
	"bytes"
	"context"
	"encoding/json"
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

const (
	headOID  = "1111111111111111111111111111111111111111"
	head2OID = "3333333333333333333333333333333333333333"
	baseOID  = "2222222222222222222222222222222222222222"
)

// gitSub is the git subcommand of a docker argv: the word after the guard flags.
func gitSub(a []string) string {
	i := slices.Index(a, "safe.directory=*")
	if i < 0 || i+1 >= len(a) {
		return ""
	}
	return a[i+1]
}

// runsOf counts the docker runs of one git subcommand.
func runsOf(f *fakeDocker, sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.args {
		if gitSub(a) == sub {
			n++
		}
	}
	return n
}

// lastRun is the argv of the latest run of one git subcommand, or nil.
func lastRun(f *fakeDocker, sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.args) - 1; i >= 0; i-- {
		if gitSub(f.args[i]) == sub {
			return f.args[i]
		}
	}
	return nil
}

// refLine is one for-each-ref record of BaseArgs' format.
func refLine(ref, symref string) string { return ref + "\x00" + symref + "\x00\n" }

// baseState is a repository that status, for-each-ref and merge-base answer
// about. An empty head is a repository with no commits.
type baseState struct {
	mu        sync.Mutex
	head      string
	branch    string
	refs      string
	mergeBase string
	mergeErr  error
}

func (b *baseState) run(_ context.Context, a []string, w io.Writer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out string
	switch gitSub(a) {
	case "status":
		oid := b.head
		if oid == "" {
			oid = "(initial)"
		}
		out = "# branch.oid " + oid + "\x00# branch.head " + b.branch + "\x00"
	case "for-each-ref":
		out = b.refs
	case "merge-base":
		if b.mergeErr != nil {
			return b.mergeErr
		}
		out = b.mergeBase + "\n"
	default:
		return errors.New("unexpected git subcommand " + gitSub(a))
	}
	_, err := io.WriteString(w, out)
	return err
}

func (b *baseState) set(f func(*baseState)) { b.mu.Lock(); f(b); b.mu.Unlock() }

// newBase is a runner over a repository on branch "feat" at headOID, whose
// merge base with whatever base it picks is baseOID.
func newBase(refs ...string) (*Runner, *fakeDocker, *baseState, *fakeClock) {
	bs := &baseState{head: headOID, branch: "feat", refs: strings.Join(refs, ""), mergeBase: baseOID}
	f := &fakeDocker{run: bs.run}
	c := newClock()
	return newRunner(f, c), f, bs, c
}

func TestStatusCarriesBranchAndHead(t *testing.T) {
	r, _, bs, _ := newBase()
	st := r.Status(context.Background(), tgt)
	if st.State != "ok" || st.Branch != "feat" || st.Head != headOID {
		t.Fatalf("status = %+v", st)
	}
	// Detached: a head and no branch. No commits: a branch and no head.
	bs.set(func(b *baseState) { b.branch = "(detached)" })
	if st := r.StatusFresh(context.Background(), tgt); st.Branch != "" || st.Head != headOID {
		t.Fatalf("detached status = %+v", st)
	}
	bs.set(func(b *baseState) { b.head, b.branch = "", "main" })
	if st := r.StatusFresh(context.Background(), tgt); st.Branch != "main" || st.Head != "" || !st.NoHead {
		t.Fatalf("unborn status = %+v", st)
	}
}

func TestStatusTooManyKeepsBranchAndHead(t *testing.T) {
	hdr := "# branch.oid " + headOID + "\x00# branch.head feat\x00"
	for name, run := range map[string]func(context.Context, []string, io.Writer) error{
		"entry cap": func(_ context.Context, _ []string, w io.Writer) error {
			var b bytes.Buffer
			b.WriteString(hdr)
			for i := 0; i < maxStatusEntries+1; i++ {
				fmt.Fprintf(&b, "? f%d\x00", i)
			}
			_, err := w.Write(b.Bytes())
			return err
		},
		"byte cap": func(_ context.Context, _ []string, w io.Writer) error {
			io.WriteString(w, hdr)
			_, err := w.Write(bytes.Repeat([]byte("x"), maxStatusBytes+1))
			if err != nil {
				return fmt.Errorf("docker run: writing its output: %w", err)
			}
			return nil
		},
	} {
		st := newRunner(&fakeDocker{run: run}, newClock()).Status(context.Background(), tgt)
		if st.State != "toomany" || st.Branch != "feat" || st.Head != headOID {
			t.Errorf("%s: status = %+v", name, st)
		}
	}
}

func TestBasePicksOriginHEADTarget(t *testing.T) {
	// origin/HEAD names a branch BaseArgs does not list: the symref is the answer.
	r, f, _, _ := newBase(
		refLine("refs/remotes/origin/HEAD", "refs/remotes/origin/develop"),
		refLine("refs/remotes/origin/main", ""),
		refLine("refs/heads/main", ""))
	bi := r.Base(context.Background(), tgt)
	want := BaseInfo{State: "ok", Branch: "feat", BaseRef: "refs/remotes/origin/develop", BaseName: "origin/develop", MergeBase: baseOID, Ahead: true}
	if bi != want {
		t.Fatalf("base = %+v, want %+v", bi, want)
	}
	if n := runsOf(f, "for-each-ref"); n != 1 {
		t.Fatalf("for-each-ref ran %d times, want 1", n)
	}
	a := lastRun(f, "merge-base")
	i := slices.Index(a, "--name")
	if !slices.Equal(a, MergeBaseArgs(tgt, "img:1", a[i+1], 1000, 1000, "refs/remotes/origin/develop")) {
		t.Fatalf("merge-base argv = %q", a)
	}
	if a := lastRun(f, "for-each-ref"); !slices.Equal(a, BaseArgs(tgt, "img:1", a[slices.Index(a, "--name")+1], 1000, 1000)) {
		t.Fatalf("for-each-ref argv = %q", a)
	}

	// The tip of a branch HEAD has not left is its own merge base: not ahead.
	r, _, bs, _ := newBase(refLine("refs/remotes/origin/HEAD", "refs/remotes/origin/main"), refLine("refs/remotes/origin/main", ""))
	bs.set(func(b *baseState) { b.mergeBase = headOID })
	if bi := r.Base(context.Background(), tgt); bi.State != "ok" || bi.Ahead || bi.BaseName != "origin/main" || bi.MergeBase != headOID {
		t.Fatalf("not ahead: %+v", bi)
	}
}

func TestBaseFallsBackToMain(t *testing.T) {
	om, oma := refLine("refs/remotes/origin/main", ""), refLine("refs/remotes/origin/master", "")
	hm, hma := refLine("refs/heads/main", ""), refLine("refs/heads/master", "")
	dangling := refLine("refs/remotes/origin/HEAD", "") // not a symref
	for _, c := range []struct {
		name string
		refs []string
		ref  string
		disp string
	}{
		{"all four", []string{om, oma, hm, hma}, "refs/remotes/origin/main", "origin/main"},
		{"listing order does not matter", []string{hma, hm, oma, om}, "refs/remotes/origin/main", "origin/main"},
		{"origin master before local main", []string{hma, hm, oma}, "refs/remotes/origin/master", "origin/master"},
		{"local main before local master", []string{hma, hm}, "refs/heads/main", "main"},
		{"local master", []string{hma}, "refs/heads/master", "master"},
		{"origin/HEAD that is not a symref", []string{dangling, hm}, "refs/heads/main", "main"},
		// Hostile or unusable symref targets are skipped, never run.
		{"symref to a tag", []string{refLine("refs/remotes/origin/HEAD", "refs/tags/v1"), hm}, "refs/heads/main", "main"},
		{"symref that looks like an option", []string{refLine("refs/remotes/origin/HEAD", "--upload-pack=touch /tmp/x"), hm}, "refs/heads/main", "main"},
		{"symref with dots", []string{refLine("refs/remotes/origin/HEAD", "refs/heads/../x"), hm}, "refs/heads/main", "main"},
		{"symref with a space", []string{refLine("refs/remotes/origin/HEAD", "refs/heads/a b"), hm}, "refs/heads/main", "main"},
		{"over-long symref", []string{refLine("refs/remotes/origin/HEAD", "refs/heads/"+strings.Repeat("a", 300)), hm}, "refs/heads/main", "main"},
		// Only the five names count: a ref below main (listed when main is not a ref) is not main.
		{"nested under main", []string{refLine("refs/heads/main/x", ""), hma}, "refs/heads/master", "master"},
		// Records without git's two NUL terminators are not git's, whatever they name.
		{"junk lines", []string{"garbage\n", "\n", "refs/heads/main\n", "refs/heads/main\x00\n", "\x00\x00\n", hma}, "refs/heads/master", "master"},
	} {
		r, f, _, _ := newBase(c.refs...)
		bi := r.Base(context.Background(), tgt)
		if bi.State != "ok" || bi.BaseRef != c.ref || bi.BaseName != c.disp {
			t.Errorf("%s: base = %+v, want %s", c.name, bi, c.ref)
		}
		// Whatever the output said, only a valid ref ever reached a merge-base argv.
		a := lastRun(f, "merge-base")
		if a == nil || a[len(a)-1] != c.ref || !ValidBaseRef(a[len(a)-1]) || a[len(a)-2] != "HEAD" {
			t.Errorf("%s: merge-base argv = %q", c.name, a)
		}
	}
}

func TestBaseNoneIsNobase(t *testing.T) {
	r, f, _, _ := newBase()
	bi := r.Base(context.Background(), tgt)
	if bi.State != "nobase" || bi.Branch != "feat" || bi.Reason == "" || bi.BaseRef != "" || bi.MergeBase != "" || bi.Ahead {
		t.Fatalf("base = %+v", bi)
	}
	if n := runsOf(f, "merge-base"); n != 0 {
		t.Fatalf("merge-base ran %d times without a base", n)
	}
	// Refs that are not one of the five candidates are no base either.
	r, f, _, _ = newBase(refLine("refs/heads/develop", ""), refLine("refs/tags/main", ""))
	if bi := r.Base(context.Background(), tgt); bi.State != "nobase" || runsOf(f, "merge-base") != 0 {
		t.Fatalf("other refs: %+v", bi)
	}
	// No commits: nothing to compare, and no point asking git for refs.
	r, f, bs, _ := newBase(refLine("refs/heads/main", ""))
	bs.set(func(b *baseState) { b.head = "" })
	if bi := r.Base(context.Background(), tgt); bi.State != "nobase" || bi.Branch != "feat" || bi.Reason == "" {
		t.Fatalf("no commits: %+v", bi)
	}
	if runsOf(f, "for-each-ref") != 0 || runsOf(f, "merge-base") != 0 {
		t.Fatalf("no commits still ran git for refs: %v", f.args)
	}
}

func TestBaseNoCommonHistoryIsNobase(t *testing.T) {
	// merge-base exits 1 with no output when the histories are unrelated.
	r, _, bs, _ := newBase(refLine("refs/heads/main", ""))
	bs.set(func(b *baseState) { b.mergeErr = exitErr(t, 1) })
	bi := r.Base(context.Background(), tgt)
	if bi.State != "nobase" || bi.Reason == "" || bi.BaseRef != "" || bi.Branch != "feat" {
		t.Fatalf("base = %+v", bi)
	}
}

func TestBaseFailuresAreUnavailableAndNotCached(t *testing.T) {
	r, f, bs, c := newBase(refLine("refs/heads/main", ""))
	bs.set(func(b *baseState) { b.mergeErr = exitErr(t, 128) })
	bi := r.Base(context.Background(), tgt)
	if bi.State != "unavailable" || bi.Reason == "" || bi.Branch != "feat" {
		t.Fatalf("base = %+v", bi)
	}
	if len(f.removed) != 1 {
		t.Fatalf("failed run: Remove calls %q, want 1", f.removed)
	}
	// A transient failure must not stick: the next call asks again.
	bs.set(func(b *baseState) { b.mergeErr = nil })
	c.add(3 * time.Second)
	if bi := r.Base(context.Background(), tgt); bi.State != "ok" || bi.MergeBase != baseOID {
		t.Fatalf("after recovery: %+v", bi)
	}
	// So does a for-each-ref that fails, or a merge-base that prints nonsense.
	r, _, bs, _ = newBase(refLine("refs/heads/main", ""))
	bs.set(func(b *baseState) { b.mergeBase = "not-an-oid" })
	if bi := r.Base(context.Background(), tgt); bi.State != "unavailable" || bi.MergeBase != "" {
		t.Fatalf("nonsense merge-base: %+v", bi)
	}
	f = &fakeDocker{run: func(_ context.Context, a []string, w io.Writer) error {
		if gitSub(a) == "status" {
			return (&baseState{head: headOID, branch: "feat"}).run(context.Background(), a, w)
		}
		return errors.New("docker run: boom")
	}}
	if bi := newRunner(f, newClock()).Base(context.Background(), tgt); bi.State != "unavailable" || bi.Reason == "" {
		t.Fatalf("for-each-ref failure: %+v", bi)
	}
}

func TestBaseCachedPerHead(t *testing.T) {
	r, f, bs, c := newBase(refLine("refs/remotes/origin/main", ""))
	ctx := context.Background()
	bi := r.Base(ctx, tgt)
	if bi.State != "ok" || bi.Branch != "feat" || !bi.Ahead {
		t.Fatalf("first = %+v", bi)
	}
	if runsOf(f, "for-each-ref") != 1 || runsOf(f, "merge-base") != 1 {
		t.Fatalf("first call ran %v", f.args)
	}
	// Past status's reuse window the status runs again, the base does not.
	c.add(3 * time.Second)
	if again := r.Base(ctx, tgt); again != bi {
		t.Fatalf("second = %+v, want %+v", again, bi)
	}
	if runsOf(f, "status") != 2 || runsOf(f, "for-each-ref") != 1 || runsOf(f, "merge-base") != 1 {
		t.Fatalf("same head recomputed: status=%d for-each-ref=%d merge-base=%d", runsOf(f, "status"), runsOf(f, "for-each-ref"), runsOf(f, "merge-base"))
	}
	// A renamed branch at the same commit keeps the cached base, not the old name.
	bs.set(func(b *baseState) { b.branch = "renamed" })
	c.add(3 * time.Second)
	if bi := r.Base(ctx, tgt); bi.Branch != "renamed" || bi.BaseRef != "refs/remotes/origin/main" {
		t.Fatalf("renamed = %+v", bi)
	}
	if runsOf(f, "for-each-ref") != 1 {
		t.Fatal("a branch rename recomputed the base")
	}
	// A new commit is a new key: recomputed, and now ahead of a moved merge base.
	bs.set(func(b *baseState) { b.head = head2OID })
	c.add(3 * time.Second)
	if bi := r.Base(ctx, tgt); bi.State != "ok" || !bi.Ahead {
		t.Fatalf("new head = %+v", bi)
	}
	if runsOf(f, "for-each-ref") != 2 || runsOf(f, "merge-base") != 2 {
		t.Fatalf("a changed HEAD did not recompute: %v", f.args)
	}
	// The base ref itself can move without HEAD moving (a fetch), so an entry
	// is only trusted for a short while.
	bs.set(func(b *baseState) { b.mergeBase = head2OID })
	c.add(10 * time.Second)
	if bi := r.Base(ctx, tgt); !bi.Ahead {
		t.Fatalf("inside the window: %+v", bi)
	}
	c.add(25 * time.Second)
	if bi := r.Base(ctx, tgt); bi.Ahead || bi.MergeBase != head2OID {
		t.Fatalf("after the window: %+v", bi)
	}
	if runsOf(f, "for-each-ref") != 3 {
		t.Fatalf("an old entry was served: %d for-each-ref runs", runsOf(f, "for-each-ref"))
	}
}

func TestBaseSingleFlight(t *testing.T) {
	release := make(chan struct{})
	r, f, bs, _ := newBase(refLine("refs/heads/main", ""))
	f.run = func(ctx context.Context, a []string, w io.Writer) error {
		if gitSub(a) == "for-each-ref" {
			<-release
		}
		return bs.run(ctx, a, w)
	}
	var wg sync.WaitGroup
	res := make([]BaseInfo, 8)
	for i := range res {
		wg.Add(1)
		go func() { defer wg.Done(); res[i] = r.Base(context.Background(), tgt) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if runsOf(f, "for-each-ref") != 1 || runsOf(f, "merge-base") != 1 {
		t.Fatalf("concurrent callers ran %d for-each-ref, %d merge-base, want 1 each", runsOf(f, "for-each-ref"), runsOf(f, "merge-base"))
	}
	for i, bi := range res {
		if bi.State != "ok" || bi.BaseRef != "refs/heads/main" {
			t.Fatalf("caller %d got %+v", i, bi)
		}
	}
}

func TestBaseCallerCancelDoesNotPoisonFlight(t *testing.T) {
	release := make(chan struct{})
	r, f, bs, _ := newBase(refLine("refs/heads/main", ""))
	f.run = func(ctx context.Context, a []string, w io.Writer) error {
		if gitSub(a) == "for-each-ref" {
			<-release
		}
		return bs.run(ctx, a, w)
	}
	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan BaseInfo)
	go func() { done <- r.Base(cctx, tgt) }()
	waitCalls(t, f, 2) // status, then for-each-ref blocking
	second := make(chan BaseInfo)
	go func() { second <- r.Base(context.Background(), tgt) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if bi := <-done; bi.State != "unavailable" {
		t.Fatalf("cancelled caller got %+v", bi)
	}
	close(release)
	if bi := <-second; bi.State != "ok" {
		t.Fatalf("other caller got %+v", bi)
	}
}

func TestBaseRefusesUnsafeTargetAndSkipsNonRepos(t *testing.T) {
	f := &fakeDocker{run: okStatus()}
	r := newRunner(f, newClock())
	bad := Target{Top: "/repo,readonly=false", Common: "/repo/.git"}
	if bi := r.Base(context.Background(), bad); bi.State != "unavailable" || bi.Reason == "" {
		t.Fatalf("unsafe target: %+v", bi)
	}
	if f.callCount() != 0 {
		t.Fatalf("docker called %d times for an unsafe target", f.callCount())
	}
	f = &fakeDocker{run: func(context.Context, []string, io.Writer) error {
		return errors.New("docker run: exit status 128: fatal: not a git repository (or any of the parent directories): .git")
	}}
	if bi := newRunner(f, newClock()).Base(context.Background(), tgt); bi.State != "notrepo" {
		t.Fatalf("not a repository: %+v", bi)
	}
	if f.callCount() != 1 {
		t.Fatalf("a non-repository ran %d containers, want only the status run", f.callCount())
	}
}

func TestBaseWorksWhenStatusIsTooMany(t *testing.T) {
	hdr := "# branch.oid " + headOID + "\x00# branch.head feat\x00"
	bs := &baseState{head: headOID, branch: "feat", refs: refLine("refs/heads/main", ""), mergeBase: baseOID}
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		if gitSub(a) != "status" {
			return bs.run(ctx, a, w)
		}
		var b bytes.Buffer
		b.WriteString(hdr)
		for i := 0; i < maxStatusEntries+1; i++ {
			fmt.Fprintf(&b, "? f%d\x00", i)
		}
		_, err := w.Write(b.Bytes())
		return err
	}}
	bi := newRunner(f, newClock()).Base(context.Background(), tgt)
	if bi.State != "ok" || bi.Branch != "feat" || bi.BaseRef != "refs/heads/main" {
		t.Fatalf("base = %+v", bi)
	}
}

func TestBaseCacheIsSweptWhenStale(t *testing.T) {
	r, _, _, c := newBase(refLine("refs/heads/main", ""))
	other := Target{Top: "/other", Common: "/other/.git"}
	r.Base(context.Background(), tgt)
	r.mu.Lock()
	n := len(r.baseCache)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("baseCache holds %d entries, want 1", n)
	}
	c.add(6 * time.Minute)
	r.Base(context.Background(), other)
	r.mu.Lock()
	_, keptTgt := r.baseCache[tgt.key()+"\x00"+headOID]
	r.mu.Unlock()
	if keptTgt {
		t.Fatal("a base entry nobody asked for within Idle was kept")
	}
}

// numstatOut is what a repository with one modified and one binary file prints
// for git diff --raw --numstat -z: the raw records, then the numstat ones.
const numstatOut = ":100644 100644 aaaaaaa bbbbbbb M\x00src/a.go\x00:100644 100644 aaaaaaa bbbbbbb M\x00img.png\x00" +
	"3\t1\tsrc/a.go\x00-\t-\timg.png\x00"

func okNumstat() func(context.Context, []string, io.Writer) error {
	return func(_ context.Context, _ []string, w io.Writer) error {
		_, err := io.WriteString(w, numstatOut)
		return err
	}
}

func TestRunnerNumstatOK(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	r := newRunner(f, newClock())
	ns := r.Numstat(context.Background(), tgt, Scope{})
	want := []NumEntry{{P: "src/a.go", X: "M", A: 3, D: 1}, {P: "img.png", X: "M", Bin: true}}
	if ns.State != "ok" || ns.Reason != "" || !slices.Equal(ns.Entries, want) {
		t.Fatalf("numstat = %+v, want ok with %+v", ns, want)
	}
	a := f.args[0]
	i := slices.Index(a, "--name")
	if i < 0 || !slices.Equal(a, NumstatArgs(tgt, "img:1", a[i+1], 1000, 1000, Scope{})) || gitSub(a) != "diff" {
		t.Fatalf("argv is not NumstatArgs: %q", a)
	}
	// The wire shape the client reads.
	b, _ := json.Marshal(ns)
	if !strings.Contains(string(b), `{"p":"img.png","x":"M","a":0,"d":0,"bin":true}`) || !strings.Contains(string(b), `"rootRel":""`) || !strings.Contains(string(b), `"entries":[`) {
		t.Fatalf("json = %s", b)
	}
}

func TestRunnerNumstatStates(t *testing.T) {
	cases := map[string]struct {
		run   func(context.Context, []string, io.Writer) error
		state string
	}{
		"clean": {func(context.Context, []string, io.Writer) error { return nil }, "ok"},
		"notrepo": {func(context.Context, []string, io.Writer) error {
			return errors.New("docker run: exit status 128: fatal: not a git repository (or any of the parent directories): .git")
		}, "notrepo"},
		// A repository with no commits has no HEAD to diff against.
		"no HEAD": {func(context.Context, []string, io.Writer) error {
			return errors.New("docker run: exit status 128: fatal: bad revision 'HEAD'")
		}, "unavailable"},
		"docker down": {func(context.Context, []string, io.Writer) error { return errors.New("docker run: boom") }, "unavailable"},
		"too many entries": {func(_ context.Context, _ []string, w io.Writer) error {
			var b bytes.Buffer
			for i := 0; i < maxStatusEntries+1; i++ {
				fmt.Fprintf(&b, "1\t1\tf%d\x00", i)
			}
			_, err := w.Write(b.Bytes())
			return err
		}, "toomany"},
		"too many bytes": {func(_ context.Context, _ []string, w io.Writer) error {
			if _, err := w.Write(bytes.Repeat([]byte("x"), maxNumstatBytes+1)); err != nil {
				return fmt.Errorf("docker run: writing its output: %w", err)
			}
			return nil
		}, "toomany"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRunner(&fakeDocker{run: c.run}, newClock())
			ns := r.Numstat(context.Background(), tgt, Scope{})
			if ns.State != c.state {
				t.Fatalf("state = %q (%s), want %q", ns.State, ns.Reason, c.state)
			}
			if ns.Entries == nil {
				t.Fatalf("Entries is nil for %s", name)
			}
			if c.state != "ok" && len(ns.Entries) != 0 {
				t.Fatalf("Entries = %d for %s", len(ns.Entries), name)
			}
			if c.state == "unavailable" && ns.Reason == "" {
				t.Fatalf("unavailable without a reason for %s", name)
			}
		})
	}
}

func TestRunnerNumstatFailIsUnavailable(t *testing.T) {
	f := &fakeDocker{run: func(context.Context, []string, io.Writer) error {
		return errors.New("docker run: exit status 128: fatal: bad revision 'HEAD'")
	}}
	ns := newRunner(f, newClock()).Numstat(context.Background(), tgt, Scope{})
	if ns.State != "unavailable" || !strings.Contains(ns.Reason, "bad revision") || ns.Entries == nil {
		t.Fatalf("numstat = %+v", ns)
	}
	// The failed container is removed, as for status.
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.Index(f.args[0], "--name")
	if len(f.removed) != 1 || f.removed[0] != f.args[0][i+1] {
		t.Fatalf("Remove calls %q, want the run's container", f.removed)
	}
}

func TestRunnerNumstatBranchScope(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	r := newRunner(f, newClock())
	base := Scope{Branch: true, BaseRef: "refs/remotes/origin/main"}
	if ns := r.Numstat(context.Background(), tgt, base); ns.State != "ok" || len(ns.Entries) != 2 {
		t.Fatalf("numstat = %+v", ns)
	}
	a := lastRun(f, "diff")
	i := slices.Index(a, "--merge-base")
	if i < 0 || a[i+1] != "refs/remotes/origin/main" || slices.Contains(a, "HEAD") {
		t.Fatalf("branch scope argv = %q", a)
	}
	// A ref that is not a ValidBaseRef reaches no docker run, whoever asks.
	n := f.callCount()
	for _, ref := range []string{"", "-x", "--output=/x", "HEAD", "refs/heads/../x", "refs/tags/v1", "refs/heads/a b"} {
		ns := r.Numstat(context.Background(), tgt, Scope{Branch: true, BaseRef: ref})
		if ns.State != "unavailable" || ns.Reason == "" || ns.Entries == nil || len(ns.Entries) != 0 {
			t.Errorf("ref %q: numstat = %+v", ref, ns)
		}
	}
	if f.callCount() != n {
		t.Fatalf("docker ran %d times for refused refs", f.callCount()-n)
	}
}

func TestRunnerNumstatScopeIsPartOfTheKey(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	c := newClock()
	r := newRunner(f, c)
	ctx := context.Background()
	main := Scope{Branch: true, BaseRef: "refs/heads/main"}
	other := Scope{Branch: true, BaseRef: "refs/remotes/origin/main"}
	r.Numstat(ctx, tgt, Scope{})
	r.Numstat(ctx, tgt, main)
	r.Numstat(ctx, tgt, other)
	if n := f.callCount(); n != 3 {
		t.Fatalf("three scopes ran %d times, want 3", n)
	}
	// Each is reused on its own; a ref given outside Branch scope is not read, so it is the head key.
	r.Numstat(ctx, tgt, Scope{})
	r.Numstat(ctx, tgt, Scope{BaseRef: "refs/heads/main"})
	r.Numstat(ctx, tgt, main)
	r.Numstat(ctx, tgt, other)
	if n := f.callCount(); n != 3 {
		t.Fatalf("reuse: %d runs, want 3", n)
	}
	// Status has its own cache: asking for one does not satisfy the other.
	r.Status(ctx, tgt)
	if n := f.callCount(); n != 4 {
		t.Fatalf("status after numstat: %d runs, want 4", n)
	}
}

func TestRunnerNumstatReuseWindow(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	c := newClock()
	r := newRunner(f, c)
	r.Numstat(context.Background(), tgt, Scope{})
	c.add(1900 * time.Millisecond)
	r.Numstat(context.Background(), tgt, Scope{})
	if n := f.callCount(); n != 1 {
		t.Fatalf("within the reuse window: %d runs, want 1", n)
	}
	c.add(200 * time.Millisecond)
	r.Numstat(context.Background(), tgt, Scope{})
	if n := f.callCount(); n != 2 {
		t.Fatalf("after the reuse window: %d runs, want 2", n)
	}
}

func TestRunnerNumstatResultIsACopy(t *testing.T) {
	r := newRunner(&fakeDocker{run: okNumstat()}, newClock())
	a := r.Numstat(context.Background(), tgt, Scope{})
	a.Entries[0].P = "mutated"
	a.RootRel = "sub"
	b := r.Numstat(context.Background(), tgt, Scope{})
	if b.Entries[0].P != "src/a.go" || b.RootRel != "" {
		t.Fatalf("a caller's edit reached the cache: %+v", b)
	}
}

func TestRunnerNumstatSingleFlight(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okNumstat()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	var wg sync.WaitGroup
	res := make([]Numstat, 10)
	for i := range res {
		wg.Add(1)
		go func() { defer wg.Done(); res[i] = r.Numstat(context.Background(), tgt, Scope{}) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := f.callCount(); n != 1 {
		t.Fatalf("RunStream called %d times, want 1", n)
	}
	for i, ns := range res {
		if ns.State != "ok" || len(ns.Entries) != 2 {
			t.Fatalf("caller %d got %+v", i, ns)
		}
	}
}

func TestRunnerNumstatCallerCancelDoesNotPoisonFlight(t *testing.T) {
	release := make(chan struct{})
	f := &fakeDocker{run: func(ctx context.Context, a []string, w io.Writer) error {
		<-release
		return okNumstat()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan Numstat)
	go func() { done <- r.Numstat(cctx, tgt, Scope{}) }()
	time.Sleep(50 * time.Millisecond)
	second := make(chan Numstat)
	go func() { second <- r.Numstat(context.Background(), tgt, Scope{}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if ns := <-done; ns.State != "unavailable" || ns.Entries == nil {
		t.Fatalf("cancelled caller got %+v", ns)
	}
	close(release)
	if ns := <-second; ns.State != "ok" {
		t.Fatalf("other caller got %+v", ns)
	}
}

func TestRunnerNumstatRefusesUnsafeTarget(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	ns := newRunner(f, newClock()).Numstat(context.Background(), Target{Top: "/repo,readonly=false", Common: "/repo/.git"}, Scope{})
	if ns.State != "unavailable" || ns.Reason == "" || ns.Entries == nil {
		t.Fatalf("numstat = %+v", ns)
	}
	if f.callCount() != 0 {
		t.Fatalf("docker called %d times for an unsafe target", f.callCount())
	}
}

func TestRunnerNumstatTimeoutUnavailable(t *testing.T) {
	f := &fakeDocker{run: func(ctx context.Context, _ []string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	r := newRunner(f, newClock())
	r.Timeout = 50 * time.Millisecond
	ns := r.Numstat(context.Background(), tgt, Scope{})
	if ns.State != "unavailable" || ns.Reason == "" || ns.Entries == nil {
		t.Fatalf("numstat = %+v", ns)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.Index(f.args[0], "--name")
	if len(f.removed) != 1 || f.removed[0] != f.args[0][i+1] {
		t.Fatalf("Remove calls %q, want the run's container", f.removed)
	}
}

func TestRunnerNumstatTakesAParallelSlot(t *testing.T) {
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
		return okNumstat()(ctx, a, w)
	}}
	r := newRunner(f, newClock())
	r.MaxParallel = 2
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		top := fmt.Sprintf("/repo%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Status and numstat of different repositories share the two slots.
			tg := Target{Top: top, Common: top + "/.git"}
			if i%2 == 0 {
				r.Status(context.Background(), tg)
			} else if ns := r.Numstat(context.Background(), tg, Scope{}); ns.State != "ok" {
				t.Errorf("%s: %+v", top, ns)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p < 1 || p > 2 {
		t.Fatalf("peak concurrency = %d, want 1..2", p)
	}
}

func TestRunnerNumstatCacheIsSwept(t *testing.T) {
	f := &fakeDocker{run: okNumstat()}
	c := newClock()
	r := newRunner(f, c)
	other := Target{Top: "/other", Common: "/other/.git"}
	r.Numstat(context.Background(), tgt, Scope{})
	r.Numstat(context.Background(), other, Scope{})
	for i := 0; i < 6; i++ {
		c.add(time.Minute)
		r.Numstat(context.Background(), tgt, Scope{})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, keptTgt := r.numCache[tgt.key()+"\x00head"]
	_, keptOther := r.numCache[other.key()+"\x00head"]
	if !keptTgt || keptOther {
		t.Fatalf("numstat cache after idle: tgt=%v other=%v (%d entries), want true/false", keptTgt, keptOther, len(r.numCache))
	}
}
