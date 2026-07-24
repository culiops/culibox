package session

import (
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/culiops/culiops-agent-console/internal/launcher"
)

// fakeBackend simulates abduco with real cheap processes on PTYs, so reap's
// cmd.Wait() behaves like a real attach. InnerAlive is settable to drive the
// exit-vs-detach classification without a real agent.
type fakeBackend struct {
	mu       sync.Mutex
	alive    map[string]bool      // InnerAlive result per id
	attached map[string]*exec.Cmd // attach process per id (so we can crash/kill it)
	created  []string
	killed   []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{alive: map[string]bool{}, attached: map[string]*exec.Cmd{}}
}

func (f *fakeBackend) Create(id, cwd string, argv, env []string) error {
	f.mu.Lock()
	f.created = append(f.created, id)
	f.alive[id] = true
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) Attach(id string, rows, cols uint16) (*exec.Cmd, *os.File, error) {
	cmd := exec.Command("sleep", "300") // stand-in for `abduco -a`; long-lived
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	f.attached[id] = cmd
	f.mu.Unlock()
	return cmd, ptmx, nil
}

func (f *fakeBackend) Alive(id string) bool { return f.InnerAlive(id) }

func (f *fakeBackend) InnerAlive(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[id]
}

func (f *fakeBackend) Kill(id string) error {
	f.mu.Lock()
	f.killed = append(f.killed, id)
	f.alive[id] = false
	c := f.attached[id]
	f.mu.Unlock()
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
	}
	return nil
}

func (f *fakeBackend) List() ([]string, error) { return nil, nil }

// crashAttach simulates the attach process dying while the agent stays alive
// (alive[id] remains true) — the case that must classify as `detached`.
func (f *fakeBackend) crashAttach(id string) {
	f.mu.Lock()
	c := f.attached[id]
	f.mu.Unlock()
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
	}
}

func (f *fakeBackend) wasKilled(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.killed {
		if k == id {
			return true
		}
	}
	return false
}

type fakeLauncher struct{}

func (fakeLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return []string{"sleep", "300"}, []string{}, nil
}

func waitStatus(t *testing.T, s *Session, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.Info().Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status = %q, want %q", s.Info().Status, want)
}

// TestReaperExitedFinalizes: with a backend reporting the agent dead, an attach
// exit finalizes the session (status exited, Done closed).
func TestReaperExitedFinalizes(t *testing.T) {
	fb := newFakeBackend()
	cmd := exec.Command("sh", "-c", "exit 0") // attach exits immediately
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	fb.alive["s-1"] = false // agent already gone
	s := newSession("s-1", "test", "", cmd, ptmx, 1<<16, fb)
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("exited session did not finalize (Done not closed)")
	}
	if s.Info().Status != statusExited {
		t.Fatalf("status = %q, want exited", s.Info().Status)
	}
}

// TestReaperDetachedDoesNotFinalize: attach dies but agent alive → detached,
// Done stays open, session not finalized.
func TestReaperDetachedDoesNotFinalize(t *testing.T) {
	fb := newFakeBackend()
	fb.alive["s-1"] = true // agent alive
	cmd := exec.Command("sleep", "300")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession("s-1", "test", "", cmd, ptmx, 1<<16, fb)
	_ = cmd.Process.Kill() // attach dies
	waitStatus(t, s, statusDetached, 2*time.Second)
	select {
	case <-s.Done():
		t.Fatal("detached session was finalized (Done closed) — agent is still alive")
	case <-time.After(300 * time.Millisecond):
	}
	_ = ptmx.Close()
}

// TestKillDetachedDoesNotHang guards B1: Kill of a detached session (no live
// reaper) must return promptly and finalize, not block on Done forever.
func TestKillDetachedDoesNotHang(t *testing.T) {
	fb := newFakeBackend()
	m := NewManager(fakeLauncher{}, fb, 1<<16, 5*time.Second)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	fb.crashAttach(s.ID) // attach dies, agent stays alive → detached
	waitStatus(t, s, statusDetached, 2*time.Second)

	done := make(chan error, 1)
	go func() { done <- m.Kill(s.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Kill returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Kill hung on a detached session (B1)")
	}
	if !fb.wasKilled(s.ID) {
		t.Fatal("Kill did not call backend.Kill")
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Kill")
	}
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("session still present after Kill")
	}
}

// TestSpawnUsesBackend confirms Spawn goes through Create+Attach, not the direct
// PTY path, when a backend is present.
func TestSpawnUsesBackend(t *testing.T) {
	fb := newFakeBackend()
	m := NewManager(fakeLauncher{}, fb, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	fb.mu.Lock()
	n := len(fb.created)
	fb.mu.Unlock()
	if n != 1 || fb.created[0] != s.ID {
		t.Fatalf("backend.Create not used: created=%v", fb.created)
	}
	if s.Info().Status != statusLive {
		t.Fatalf("status=%q, want live", s.Info().Status)
	}
}
