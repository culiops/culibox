package session

import (
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// revokeManager is a box-tier manager whose agent exits at once, with a long
// retention: any revoke seen must come from the exit, not from the prune.
func revokeManager(t *testing.T, be Backend) (*Manager, *fakeBrokerHooks) {
	t.Helper()
	l := ghLauncherFor(config.RuntimeBox)
	l.argv = []string{"sh", "-c", "exit 0"}
	m := NewManager(l, be, 1<<16, time.Hour)
	m.SetBoxes(&fakeBoxes{})
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	return m, hooks
}

func waitRevokes(t *testing.T, hooks *fakeBrokerHooks, id string, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for hooks.revokes(id) < want {
		if time.Now().After(deadline) {
			t.Fatalf("RevokeGitHub(%s) calls = %d, want %d", id, hooks.revokes(id), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExitRevokesBeforeRetention(t *testing.T) {
	m, hooks := revokeManager(t, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	waitRevokes(t, hooks, s.ID, 1, time.Second)
	if hooks.forgotten(s.ID) {
		t.Fatal("forgotten during retention: revoke must not wait for the prune")
	}
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("session left the registry: retention should still hold it")
	}
}

func TestAWSOnlyExitNoRevoke(t *testing.T) {
	m, hooks := revokeManager(t, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	waitStatus(t, s, statusExited, 5*time.Second)
	time.Sleep(200 * time.Millisecond) // the exit watcher has long since run
	if n := hooks.revokes(s.ID); n != 0 {
		t.Fatalf("AWS-only session revoked GitHub tokens %d times", n)
	}
}

func TestDetachDoesNotRevoke(t *testing.T) {
	be := newFakeBackend()
	l := ghLauncherFor(config.RuntimeBox) // sleep 30: the agent stays alive
	m := NewManager(l, be, 1<<16, time.Hour)
	m.SetBoxes(&fakeBoxes{})
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	be.crashAttach(s.ID) // attach dies, agent stays alive: detached
	waitStatus(t, s, statusDetached, 2*time.Second)
	time.Sleep(200 * time.Millisecond)
	if n := hooks.revokes(s.ID); n != 0 {
		t.Fatalf("detached session revoked GitHub tokens %d times; the agent is still alive", n)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
}
