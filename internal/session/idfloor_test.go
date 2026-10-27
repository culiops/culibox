package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

func newIDTestManager(t *testing.T, statePath string) *Manager {
	t.Helper()
	l := tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}
	m := NewManager(l, newFakeBackend(), 1<<16, time.Minute)
	m.SetIDStatePath(statePath)
	return m
}

func spawnID(t *testing.T, m *Manager, agent string) string {
	t.Helper()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: agent, Cwd: t.TempDir(), Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return s.ID
}

func TestNextIDNeverReusedAcrossRestart(t *testing.T) {
	state := filepath.Join(t.TempDir(), "session-ids.json")
	a := newIDTestManager(t, state)
	if id := spawnID(t, a, "p"); id != "p-1" {
		t.Fatalf("first id = %s", id)
	}
	if id := spawnID(t, a, "p"); id != "p-2" {
		t.Fatalf("second id = %s", id)
	}
	if err := a.Kill("p-2"); err != nil {
		t.Fatal(err)
	}
	// A restarted daemon with no live sessions: p-2 is gone from memory, the
	// abduco list and the sidecar, yet its id must not come back.
	b := newIDTestManager(t, state)
	if id := spawnID(t, b, "p"); id != "p-3" {
		t.Fatalf("id after restart = %s, want p-3", id)
	}
}

func TestIDFloorFromAudit(t *testing.T) {
	state := filepath.Join(t.TempDir(), "session-ids.json")
	m := newIDTestManager(t, state)
	m.SeedIDFloor(map[string]int{"p": 7})
	if id := spawnID(t, m, "p"); id != "p-8" {
		t.Fatalf("id = %s, want p-8", id)
	}

	// A floor below the persisted mark changes nothing.
	if err := os.WriteFile(state, []byte(`{"q":10}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := newIDTestManager(t, state)
	m2.SeedIDFloor(map[string]int{"q": 7})
	if id := spawnID(t, m2, "q"); id != "q-11" {
		t.Fatalf("id = %s, want q-11", id)
	}
}

func TestIDStateWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "session-ids.json")

	// A corrupt file is ignored (logged), and the manager still allocates.
	if err := os.WriteFile(state, []byte(`{"p": 4`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newIDTestManager(t, state)
	m.SeedIDFloor(map[string]int{"p": 2})
	if id := spawnID(t, m, "p"); id != "p-3" {
		t.Fatalf("id = %s, want p-3 (floor from audit, corrupt file ignored)", id)
	}

	// The write replaced the corrupt file through tmp + rename.
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := json.Unmarshal(b, &got); err != nil || got["p"] != 3 {
		t.Fatalf("state = %q (%v), want {p:3}", b, err)
	}
	if _, err := os.Stat(state + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind: %v", err)
	}
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, %v", fi, err)
	}
}

func TestFloorFromIDs(t *testing.T) {
	got := FloorFromIDs([]string{"claude-3", "claude-12", "claude-code-5", "codex-1", "", "junk", "bash-0", "x-"})
	want := map[string]int{"claude": 12, "claude-code": 5, "codex": 1}
	if len(got) != len(want) {
		t.Fatalf("floor = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("floor = %v, want %v", got, want)
		}
	}
}
