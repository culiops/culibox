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
	m.peekStore(s.ID, "/x/t.jsonl", time.Now(), 42, Peek{Text: "a whole assistant message"})
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
	m.peekStore(s.ID, "/x/t.jsonl", time.Now(), 42, Peek{Text: "a whole assistant message"})
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

// The cache key is (id, path, mtime) — but mtime is coarse on this kernel
// (measured), so a real edit landing in the same tick as the cached read
// keeps the SAME mtime while the file's content, and therefore its size,
// changed. Without size in the key, PeekFor would keep serving the stale
// parse forever (nothing ever invalidates it again, since mtime never
// moves). Adding file size to the key catches this.
func TestPeekCacheKeyIncludesFileSize(t *testing.T) {
	m, home := newLocatorManager(t)
	fixed := time.Now()
	writeTxLines(t, home, locCwd, "aaaa-1", fixed, txUser("go"), txRec("assistant", txText("first")))
	p1, ok := m.PeekFor("s1")
	if !ok || p1.Text != "first" {
		t.Fatalf("first read: %+v ok=%v", p1, ok)
	}
	// Same mtime (the coarse-mtime scenario), a longer body → different size.
	writeTxLines(t, home, locCwd, "aaaa-1", fixed, txUser("go"), txRec("assistant", txText("second, and longer than before")))
	p2, ok := m.PeekFor("s1")
	if !ok || p2.Text != "second, and longer than before" {
		t.Fatalf("a same-mtime size change was served from a stale cache: %+v ok=%v", p2, ok)
	}
}
