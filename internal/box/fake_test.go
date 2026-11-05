package box

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	exited     map[string]bool // present but not running
	inspectErr error
	stopFails  int               // the next N Stop calls fail and leave the container running
	stopHook   func(name string) // runs after Stop removed the box, with no lock held
	sysbox     bool

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
	// sources is, per box, each bind's host path by in-box destination, recorded
	// from every --mount of a Run (a test overrides one to stand for a box made
	// by an older build). BindSource reads it.
	sources map[string]map[string]string

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
		mounts: map[string][]string{}, sources: map[string]map[string]string{}}
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
	// docker info: a host with 8 GiB, so spawning tests take the normal path and
	// do not log the unreadable-MemTotal fallback. A test overrides it via stdoutHook.
	if strings.Join(args, " ") == "info --format {{.MemTotal}}" {
		return "8589934592", nil
	}
	return "", errors.New("fake: unexpected Stdout " + strings.Join(args, " "))
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

func (f *fakeClient) InspectMounts(ctx context.Context, name string) (State, []string, string, error) {
	st, err := f.Inspect(ctx, name)
	if err != nil || st != StateRunning {
		return st, nil, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mountsErr != nil {
		return StateMissing, nil, "", f.mountsErr
	}
	return st, append([]string(nil), f.mounts[name]...), f.labels[name][extraca.Label], nil
}

func (f *fakeClient) BindSource(_ context.Context, name, dst string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mountsErr != nil {
		return "", f.mountsErr
	}
	return f.sources[name][dst], nil
}

func (f *fakeClient) HasRuntime(_ context.Context, name string) bool {
	return f.sysbox && name == runtimeSysbox
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
