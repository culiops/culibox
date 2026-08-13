package session

import (
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/launcher"
)

// peekCacheHas reports whether id still holds a cached transcript parse.
func peekCacheHas(m *Manager, id string) bool {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	_, ok := m.peekCache[id]
	return ok
}

// The cache is keyed by session id and its only writer is PeekFor. Nothing
// stale is ever served (ids are monotonic within a daemon run), so this is not
// a correctness bug — it is a full assistant message retained per session that
// ever existed, for as long as the daemon runs. That is the exact shape of the
// boxstate leak this repo already paid for once.
func TestKillReclaimsThePeekCache(t *testing.T) {
	m := NewManager(mockBuilder{argv: []string{"sh", "-c", "sleep 30"}}, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	m.peekStore(s.ID, time.Now(), Peek{Text: "a whole assistant message"})
	if !peekCacheHas(m, s.ID) {
		t.Fatal("precondition: the entry was not stored")
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if peekCacheHas(m, s.ID) {
		t.Error("peekCache still holds the killed session's parse")
	}
}

// The other delete(m.sessions, id) site: a session that exits on its own is
// pruned by watchRetention after the retention window, not by Kill.
func TestRetentionPruneReclaimsThePeekCache(t *testing.T) {
	m := NewManager(mockBuilder{argv: []string{"sh", "-c", "exit 0"}}, nil, 1<<16, 10*time.Millisecond)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	m.peekStore(s.ID, time.Now(), Peek{Text: "a whole assistant message"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.Get(s.ID); !ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("session was never pruned; the cache assertion below would be meaningless")
	}
	if peekCacheHas(m, s.ID) {
		t.Error("peekCache still holds the pruned session's parse")
	}
}
