package box

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClient records docker calls so every unit test runs without a daemon.
type fakeClient struct {
	mu         sync.Mutex
	runCount   int
	runArgs    [][]string
	stopCount  int
	stopped    []string
	existing   []string // names ListByLabel reports
	running    map[string]bool
	exited     map[string]bool // present but not running
	inspectErr error
	sysbox     bool

	// inspectHook runs inside Inspect, before it answers and with no lock held,
	// so a test can land an event exactly in the window between the sweep's
	// snapshot and its re-check without racing real goroutines.
	inspectHook func(name string)
}

func newFake() *fakeClient { return &fakeClient{running: map[string]bool{}, exited: map[string]bool{}} }

func (f *fakeClient) Run(_ context.Context, args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runCount++
	f.runArgs = append(f.runArgs, args)
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

func (f *fakeClient) ListBoxes(_ context.Context, _ string) ([]BoxInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []BoxInfo
	for _, n := range f.existing {
		st := "exited"
		if f.running[n] {
			st = "running"
		}
		out = append(out, BoxInfo{Name: n, State: st})
	}
	for n := range f.running {
		if !contains(f.existing, n) {
			out = append(out, BoxInfo{Name: n, State: "running"})
		}
	}
	for n := range f.exited {
		if !contains(f.existing, n) && !f.running[n] {
			out = append(out, BoxInfo{Name: n, State: "exited"})
		}
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

func (f *fakeClient) ListByLabel(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.existing...), nil
}

func (f *fakeClient) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCount++
	f.stopped = append(f.stopped, name)
	delete(f.running, name)
	delete(f.exited, name)
	return nil
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
	n, err := m.boxName(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
