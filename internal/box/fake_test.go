package box

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/extraca"
)

// fakeClient records docker calls so every unit test runs without a daemon.
type fakeClient struct {
	mu         sync.Mutex
	runCount   int
	runArgs    [][]string
	stopCount  int
	stopped    []string
	existing   []string // names ListByLabel reports
	scratch    []string // names ListByLabel reports for the CA-scratch label
	running    map[string]bool
	exited     map[string]bool   // present but not running
	states     map[string]string // a listed state other than running/exited ("paused", "restarting"), by name
	inspectErr error
	stopFails  int               // the next N Stop calls fail and leave the container running
	stopHook   func(name string) // runs after Stop removed the box, with no lock held
	sysbox     bool
	// runtimeErr fails HasRuntime (a daemon that cannot be asked); runtimeCalls
	// counts the asks; runtimeHook runs first, with no lock held, so a test can
	// hold a probe open (it answers its own error to end the probe early).
	runtimeErr   error
	runtimeCalls int
	runtimeHook  func(ctx context.Context) error

	// inspectHook runs inside Inspect, before it answers and with no lock held,
	// so a test can land an event exactly in the window between the sweep's
	// snapshot and its re-check without racing real goroutines.
	inspectHook func(name string)

	// labels holds every --label k=v of each Run, keyed by --name, so ListBoxes
	// reports the workspace/instance/exclusive/claims labels docker would.
	labels map[string]map[string]string
	// others are boxes of another instance (or foreign rows carrying the
	// instance label). ListBoxes returns them only for the bare LabelInstance
	// filter, as docker would for `--filter label=<key>`.
	others  []BoxInfo
	listErr error

	// mounts holds the bind destinations of each box, as `docker inspect`
	// reports them: recorded from every --mount of a Run, or set by a test to
	// stand for a box created by an older build. mountsErr fails InspectMounts
	// for a running box.
	mounts    map[string][]string
	mountsErr error
	bindErr   error // fails BindSource alone (InspectMounts still works)
	// sources is, per box, each bind's host path by in-box destination, recorded
	// from every --mount of a Run (a test overrides one to stand for a box made
	// by an older build). BindSource reads it.
	sources map[string]map[string]string
	// mountIDs is, per box and in-box destination, the identity of what each
	// bind's source was when Run ran: os.Stat follows symlinks, as docker does at
	// that moment, so a path flipped to a link afterwards does not change it. A
	// source that cannot be stat'ed records nothing. inBoxStat reads it.
	mountIDs map[string]map[string]FileID

	// runHook answers a Run before it is recorded or counted (handled=true),
	// so a test can serve the image/create/cp/rm calls of caBundle without
	// disturbing the run counters.
	runHook func(args []string) (out string, handled bool, err error)
	// stdoutHook answers a Stdout; an unhandled Stdout fails the test's
	// expectations loudly (empty output, error) rather than reaching Run.
	stdoutHook func(args []string) (out string, handled bool, err error)
}

func newFake() *fakeClient {
	return &fakeClient{running: map[string]bool{}, exited: map[string]bool{}, labels: map[string]map[string]string{},
		mounts: map[string][]string{}, sources: map[string]map[string]string{}, mountIDs: map[string]map[string]FileID{}}
}

func (f *fakeClient) Stdout(_ context.Context, args []string) (string, error) {
	f.mu.Lock()
	hook := f.stdoutHook
	f.mu.Unlock()
	if hook != nil {
		if out, handled, err := hook(args); handled {
			return out, err
		}
	}
	// The in-box identity check (checkMountIDs).
	if len(args) > 2 && args[0] == "exec" && args[2] == "/usr/bin/stat" {
		return f.inBoxStat(args)
	}
	// docker info: a host with 8 GiB, so spawning tests take the normal path and
	// do not log the unreadable-MemTotal fallback. A test overrides it via stdoutHook.
	if strings.Join(args, " ") == "info --format {{.MemTotal}}" {
		return "8589934592", nil
	}
	return "", errors.New("fake: unexpected Stdout " + strings.Join(args, " "))
}

// inBoxStat answers `exec <name> /usr/bin/stat -c %d:%i -- p...` as the box
// would: each p shows what Run mounted there (mountIDs), else the identity the
// path has now (a box a test seeded, or one an older build made). Any failure is
// an error, as a failing docker exec is.
func (f *fakeClient) inBoxStat(args []string) (string, error) {
	if len(args) < 7 || args[0] != "exec" || args[2] != "/usr/bin/stat" || args[3] != "-c" || args[4] != "%d:%i" || args[5] != "--" {
		return "", errors.New("fake: unexpected in-box stat " + strings.Join(args, " "))
	}
	name := args[1]
	var lines []string
	for _, p := range args[6:] {
		f.mu.Lock()
		id, ok := f.mountIDs[name][p]
		f.mu.Unlock()
		if !ok {
			var err error
			if id, err = hostID(p); err != nil {
				return "", fmt.Errorf("docker exec: exit status 1: stat: cannot statx '%s': %v", p, err)
			}
		}
		lines = append(lines, fmt.Sprintf("%d:%d", id.Dev, id.Ino))
	}
	return strings.Join(lines, "\n"), nil
}

func (f *fakeClient) Run(_ context.Context, args []string) (string, error) {
	f.mu.Lock()
	hook := f.runHook
	f.mu.Unlock()
	if hook != nil {
		if out, handled, err := hook(args); handled {
			return out, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCount++
	f.runArgs = append(f.runArgs, args)
	var name string
	lbl := map[string]string{}
	var dsts []string
	srcs := map[string]string{}
	ids := map[string]FileID{}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--name":
			name = args[i+1]
		case "--mount":
			var src, dst string
			for _, opt := range strings.Split(args[i+1], ",") {
				if d, ok := strings.CutPrefix(opt, "dst="); ok {
					dsts = append(dsts, d)
					dst = d
				}
				if s, ok := strings.CutPrefix(opt, "src="); ok {
					src = s
				}
			}
			if dst != "" {
				srcs[dst] = src
				if id, err := hostID(src); err == nil {
					ids[dst] = id
				}
			}
		case "--label":
			if k, v, ok := strings.Cut(args[i+1], "="); ok {
				lbl[k] = v
			}
		}
	}
	for i, a := range args {
		if a == "--name" && i+1 < len(args) {
			if f.exited[args[i+1]] {
				// What real docker answers when an exited box still holds the name.
				return "", errors.New("docker run: Conflict. The container name \"" + args[i+1] + "\" is already in use")
			}
			f.running[args[i+1]] = true
			delete(f.exited, args[i+1])
		}
	}
	if name != "" {
		f.labels[name] = lbl
		f.mounts[name] = dsts
		f.sources[name] = srcs
		f.mountIDs[name] = ids
	}
	return "container-id", nil
}

func (f *fakeClient) Inspect(_ context.Context, name string) (State, error) {
	f.mu.Lock()
	hook := f.inspectHook
	f.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.inspectErr != nil:
		return StateMissing, f.inspectErr
	case f.running[name]:
		return StateRunning, nil
	case f.exited[name]:
		return StateStopped, nil
	}
	return StateMissing, nil
}

func (f *fakeClient) ListBoxes(_ context.Context, filter string) ([]BoxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	info := func(n, st string) BoxInfo {
		b := BoxInfo{Name: n, State: st}
		if l := f.labels[n]; l != nil {
			b.Workspace, b.Instance = l[LabelWorkspace], l[LabelInstance]
			b.Exclusive, b.Claims = l[LabelExclusive] == "1", l[LabelClaims] == "1"
		}
		return b
	}
	var out []BoxInfo
	for _, n := range f.existing {
		st := "exited"
		if f.running[n] {
			st = "running"
		}
		if s := f.states[n]; s != "" {
			st = s
		}
		out = append(out, info(n, st))
	}
	for n := range f.running {
		if !contains(f.existing, n) {
			out = append(out, info(n, "running"))
		}
	}
	for n := range f.exited {
		if !contains(f.existing, n) && !f.running[n] {
			out = append(out, info(n, "exited"))
		}
	}
	if filter == LabelInstance {
		out = append(out, f.others...)
	}
	return out, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeClient) ListByLabel(_ context.Context, label string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(label, LabelCAScratch) {
		return append([]string(nil), f.scratch...), nil
	}
	return append([]string(nil), f.existing...), nil
}

func (f *fakeClient) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCount++
	f.stopped = append(f.stopped, name)
	if f.stopFails > 0 {
		f.stopFails--
		return errors.New("docker rm -f: daemon unreachable")
	}
	delete(f.running, name)
	delete(f.exited, name)
	if hook := f.stopHook; hook != nil {
		f.mu.Unlock()
		hook(name)
		f.mu.Lock()
	}
	return nil
}

func (f *fakeClient) InspectMounts(ctx context.Context, name string) (State, []string, BoxLabels, error) {
	st, err := f.Inspect(ctx, name)
	if err != nil || st != StateRunning {
		return st, nil, BoxLabels{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mountsErr != nil {
		return StateMissing, nil, BoxLabels{}, f.mountsErr
	}
	return st, append([]string(nil), f.mounts[name]...), BoxLabels{CA: f.labels[name][extraca.Label], Identity: f.labels[name][LabelIdentity] == "1"}, nil
}

func (f *fakeClient) BindSource(_ context.Context, name, dst string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindErr != nil {
		return "", f.bindErr
	}
	return f.sources[name][dst], nil
}

func (f *fakeClient) BindSources(_ context.Context, name string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	out := map[string]string{}
	for d, s := range f.sources[name] {
		out[d] = s
	}
	return out, nil
}

func (f *fakeClient) HasRuntime(ctx context.Context, name string) (bool, error) {
	f.mu.Lock()
	f.runtimeCalls++
	hook, err, sysbox := f.runtimeHook, f.runtimeErr, f.sysbox
	f.mu.Unlock()
	if hook != nil {
		if herr := hook(ctx); herr != nil {
			return false, herr
		}
	}
	if err != nil {
		return false, err
	}
	return sysbox && name == runtimeSysbox, nil
}

// runtimeProbes is how many times HasRuntime was asked.
func (f *fakeClient) runtimeProbes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runtimeCalls
}

// setDaemon sets what the next HasRuntime answers: err != nil stands for a
// daemon that cannot be asked (stopped, slow), sysbox for its runtime list.
func (f *fakeClient) setDaemon(sysbox bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sysbox, f.runtimeErr = sysbox, err
}

// runningBoxes lists the boxes the fake still runs.
func (f *fakeClient) runningBoxes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n, on := range f.running {
		if on {
			out = append(out, n)
		}
	}
	return out
}

func (f *fakeClient) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopCount
}

func (f *fakeClient) runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runCount
}

func (f *fakeClient) stoppedAny(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.stopped {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func newTestManagerWithFakeTTL(t *testing.T, ttl time.Duration) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{
		DockerBin: "/usr/bin/docker",
		Image:     "culiops/agent-box:base",
		Instance:  "TEST",
		IdleTTL:   ttl,
		StateRoot: t.TempDir(),
		Client:    f,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A box a test stops runs its keep pass in the background, which writes in
	// the state root once the box has a kept-folder record (any spawn with a
	// profile). Registered after the state root's TempDir, so it runs first.
	t.Cleanup(m.WaitKeptPasses)
	return m, f
}

func newTestManagerWithFake(t *testing.T) (*Manager, *fakeClient) {
	t.Helper()
	return newTestManagerWithFakeTTL(t, time.Hour)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, _ := newTestManagerWithFake(t)
	return m
}

func newTestManagerWithInstance(t *testing.T, instance string) *Manager {
	t.Helper()
	f := newFake()
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: instance, IdleTTL: time.Hour, StateRoot: t.TempDir(), Client: f})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newTestManagerWithSysbox(t *testing.T) *Manager {
	t.Helper()
	f := newFake()
	f.sysbox = true
	m, err := New(Options{DockerBin: "/usr/bin/docker", Image: "img", Instance: "TEST", IdleTTL: time.Hour, StateRoot: t.TempDir(), Client: f})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within " + limit.String())
}

func mustBoxName(t *testing.T, m *Manager, cwd string) string {
	t.Helper()
	n, err := m.boxName(cwd, BoxKey{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// hostID is the identity os.Stat gives p: what a bind mount of p shows inside a
// box. It follows symlinks, as docker does, and builds on darwin.
func hostID(p string) (FileID, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return FileID{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}, fmt.Errorf("no identity for %s", p)
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}

// What the box shows at a mount's destination is what the source resolved to
// when docker ran, not what it resolves to later.
func TestFakeInBoxStatShowsWhatRunResolved(t *testing.T) {
	root := t.TempDir()
	a, b, l := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "l")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, l); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	if _, err := f.Run(context.Background(), []string{"run", "-d", "--name", "x", "--mount", "type=bind,src=" + l + ",dst=/w"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(l); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, l); err != nil {
		t.Fatal(err)
	}
	wantA, err := hostID(a)
	if err != nil {
		t.Fatal(err)
	}
	wantB, err := hostID(b)
	if err != nil {
		t.Fatal(err)
	}
	if wantA == wantB {
		t.Fatal("test setup: a and b share an identity")
	}
	out, err := f.Stdout(context.Background(), []string{"exec", "x", "/usr/bin/stat", "-c", "%d:%i", "--", "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%d:%d", wantA.Dev, wantA.Ino); strings.TrimSpace(out) != want {
		t.Fatalf("in-box stat of /w = %q, want %q (a, what run resolved), not b's %d:%d", out, want, wantB.Dev, wantB.Ino)
	}
}
