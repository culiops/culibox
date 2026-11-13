package gitview

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// psLine is one row of the sweep's docker ps format.
func psLine(name, state string, created time.Time) string {
	return name + "\t" + state + "\t" + created.UTC().Format("2006-01-02 15:04:05 -0700 MST")
}

// isPs reports whether args is the sweep's listing, not a git run.
func isPs(args []string) bool { return len(args) > 0 && args[0] == "ps" }

// afterFuncs captures what a Runner schedules instead of starting timers.
type afterFuncs struct {
	mu     sync.Mutex
	delays []time.Duration
	fns    []func()
}

func (a *afterFuncs) add(d time.Duration, fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.delays = append(a.delays, d)
	a.fns = append(a.fns, fn)
}

func (a *afterFuncs) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.fns)
}

// fire runs the i-th scheduled func, as its timer would.
func (a *afterFuncs) fire(i int) {
	a.mu.Lock()
	fn := a.fns[i]
	a.mu.Unlock()
	fn()
}

const (
	leakOld   = "agent-box-git-0123456789abcdef"
	leakExit  = "agent-box-git-fedcba9876543210"
	leakYoung = "agent-box-git-00112233445566ff"
)

func TestSweepRemovesOnlyOldStoppedGitContainers(t *testing.T) {
	c := newClock()
	now := c.now()
	list := strings.Join([]string{
		psLine(leakOld, "created", now.Add(-10*time.Minute)),
		psLine(leakExit, "exited", now.Add(-3*time.Minute)),
		psLine("agent-box-git-dead00000000beef", "dead", now.Add(-time.Hour)),
		// Young: a live run may be between create and start.
		psLine(leakYoung, "created", now.Add(-30*time.Second)),
		// Running: never touched, whatever the filters let through.
		psLine("agent-box-git-aaaaaaaaaaaaaaaa", "running", now.Add(-time.Hour)),
		// Not a gitview name: a prefix match is not enough.
		psLine("agent-box-git-0123456789abcdef-mine", "created", now.Add(-time.Hour)),
		psLine("agent-box-git-XYZ", "created", now.Add(-time.Hour)),
		psLine("someone-else", "exited", now.Add(-time.Hour)),
		// An age it cannot read is kept.
		"agent-box-git-1111111111111111\tcreated\tyesterday",
		"agent-box-git-2222222222222222\tcreated",
	}, "\n") + "\n"
	f := &fakeDocker{run: func(_ context.Context, args []string, w io.Writer) error {
		_, err := io.WriteString(w, list)
		return err
	}}
	n, err := newRunner(f, c).Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{leakOld, leakExit, "agent-box-git-dead00000000beef"}
	if n != len(want) || !slices.Equal(f.removed, want) {
		t.Fatalf("Sweep removed %d %q, want %q", n, f.removed, want)
	}
	args := f.args[0]
	for _, a := range []string{"name=^agent-box-git-", "status=created", "status=exited", "status=dead"} {
		if i := slices.Index(args, a); i < 1 || args[i-1] != "--filter" {
			t.Errorf("ps argv %q lacks --filter %s", args, a)
		}
	}
	if !isPs(args) || !slices.Contains(args, "-a") {
		t.Errorf("ps argv %q, want ps -a", args)
	}
}

func TestSweepListingFailureRemovesNothing(t *testing.T) {
	f := &fakeDocker{run: func(_ context.Context, _ []string, w io.Writer) error {
		io.WriteString(w, psLine(leakOld, "created", time.Unix(0, 0))+"\n")
		return errors.New("docker ps: daemon unreachable")
	}}
	if _, err := newRunner(f, newClock()).Sweep(context.Background()); err == nil || len(f.removed) != 0 {
		t.Fatalf("err %v, removed %q: a failed listing must remove nothing", err, f.removed)
	}
}

// A docker CLI killed while the daemon is still creating the container: the
// run's own rm -f finds nothing ("No such container" is not an error), then the
// container appears in Created and nothing starts it, so --rm never fires. The
// failed run schedules a sweep that removes it once it is old enough.
func TestFailedStatusRunSchedulesSweepThatRemovesLateContainer(t *testing.T) {
	c := newClock()
	var after afterFuncs
	var mu sync.Mutex
	var runName string
	var createdAt time.Time
	f := &fakeDocker{}
	f.run = func(_ context.Context, args []string, w io.Writer) error {
		if isPs(args) {
			mu.Lock()
			defer mu.Unlock()
			_, err := io.WriteString(w, psLine(runName, "created", createdAt)+"\n")
			return err
		}
		mu.Lock()
		runName, createdAt = args[slices.Index(args, "--name")+1], c.now()
		mu.Unlock()
		return errors.New("docker run: signal: terminated")
	}
	r := newRunner(f, c)
	r.AfterFunc = after.add
	if st := r.Status(context.Background(), tgt); st.State != "unavailable" {
		t.Fatalf("status = %+v", st)
	}
	if after.count() != 1 || after.delays[0] < sweepGrace {
		t.Fatalf("scheduled %v, want one sweep at least %v later", after.delays, sweepGrace)
	}
	c.add(after.delays[0])
	after.fire(0)
	f.mu.Lock()
	defer f.mu.Unlock()
	// The run's own rm -f, then the sweep's.
	if len(f.removed) != 2 || f.removed[1] != runName {
		t.Fatalf("removed %q, want the run's own rm then the sweep removing %s", f.removed, runName)
	}
}

func TestFailedDiffRunSchedulesSweep(t *testing.T) {
	var after afterFuncs
	f := &fakeDocker{run: func(context.Context, []string, io.Writer) error { return errors.New("docker run: boom") }}
	r := newRunner(f, newClock())
	r.AfterFunc = after.add
	r.Diff(context.Background(), tgt, "a.go", false)
	if after.count() != 1 {
		t.Fatalf("scheduled %d sweeps, want 1", after.count())
	}
}

func TestSweepsCoalesceUntilTheScheduledOneRuns(t *testing.T) {
	var after afterFuncs
	f := &fakeDocker{run: func(_ context.Context, args []string, _ io.Writer) error {
		if isPs(args) {
			return nil
		}
		return errors.New("docker run: boom")
	}}
	r := newRunner(f, newClock())
	r.AfterFunc = after.add
	r.Diff(context.Background(), tgt, "a.go", false)
	r.Diff(context.Background(), tgt, "b.go", false)
	if after.count() != 1 {
		t.Fatalf("scheduled %d sweeps while one is pending, want 1", after.count())
	}
	after.fire(0)
	r.Diff(context.Background(), tgt, "c.go", false)
	if after.count() != 2 {
		t.Fatalf("scheduled %d sweeps after the first ran, want 2", after.count())
	}
}

func TestSuccessfulRunSchedulesNoSweep(t *testing.T) {
	var after afterFuncs
	f := &fakeDocker{run: okStatus()}
	r := newRunner(f, newClock())
	r.AfterFunc = after.add
	r.Status(context.Background(), tgt)
	r.Diff(context.Background(), tgt, "a.go", false)
	if after.count() != 0 {
		t.Fatalf("scheduled %d sweeps after clean runs", after.count())
	}
}

func TestSweepLaterSchedulesTheGivenDelay(t *testing.T) {
	var after afterFuncs
	r := newRunner(&fakeDocker{}, newClock())
	r.AfterFunc = after.add
	r.SweepLater(0)
	if after.count() != 1 || after.delays[0] != 0 {
		t.Fatalf("scheduled %v, want one sweep now", after.delays)
	}
}
