package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culiops-agent-console/internal/launcher"
)

func writeSidecar(t *testing.T, dir string, m map[string]sessionMeta) string {
	t.Helper()
	path := filepath.Join(dir, "sessions.json")
	b, _ := json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newReconcileManager(t *testing.T, fb *fakeBackend, sidecarPath string) *Manager {
	t.Helper()
	m := NewManager(fakeLauncher{}, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(sidecarPath); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestReconcileReattachesLiveSurvivor: a live socket + matching sidecar entry is
// re-attached, listed live, and keeps its original created time and cwd.
func TestReconcileReattachesLiveSurvivor(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	created := time.Now().Add(-time.Hour).Round(time.Second)
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: created},
	})
	m := newReconcileManager(t, fb, path)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("claude-2") })

	s, ok := m.Get("claude-2")
	if !ok {
		t.Fatal("survivor not re-attached")
	}
	info := s.Info()
	if info.Status != statusLive {
		t.Fatalf("status=%q, want live", info.Status)
	}
	if info.Agent != "claude" || info.Cwd != "/work/proj" {
		t.Fatalf("metadata lost: %+v", info)
	}
	if !info.Created.Equal(created) {
		t.Fatalf("created=%v, want %v (should survive restart)", info.Created, created)
	}
}

// TestReconcileDropsDeadSidecarEntry: a sidecar entry with no live socket (agent
// died during downtime) is dropped and not listed.
func TestReconcileDropsDeadSidecarEntry(t *testing.T) {
	fb := newFakeBackend() // no live sessions
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"bash-1": {Agent: "bash", Created: time.Now()},
	})
	m := newReconcileManager(t, fb, path)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("bash-1"); ok {
		t.Fatal("dead session should not be re-attached")
	}
	// sidecar entry pruned
	sc, _ := openSidecar(path)
	if _, ok := sc.all()["bash-1"]; ok {
		t.Fatal("dead sidecar entry not pruned")
	}
}

// TestReconcileAdoptsOrphan: a live socket with no sidecar entry is adopted with
// the agent name parsed from the id.
func TestReconcileAdoptsOrphan(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-code-5") // agent name contains a dash
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := newReconcileManager(t, fb, path)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("claude-code-5") })

	s, ok := m.Get("claude-code-5")
	if !ok {
		t.Fatal("orphan not adopted")
	}
	if s.Info().Agent != "claude-code" {
		t.Fatalf("agent=%q, want claude-code (parsed from id)", s.Info().Agent)
	}
}

// TestReconcileInitsCounter: after recovering claude-3, the next spawn is
// claude-4 (no id collision).
func TestReconcileInitsCounter(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-3")
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := newReconcileManager(t, fb, path)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("claude-3") })

	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.ID != "claude-4" {
		t.Fatalf("next id=%q, want claude-4 (counter must skip recovered ids)", s.ID)
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		id    string
		agent string
		n     int
		ok    bool
	}{
		{"bash-1", "bash", 1, true},
		{"claude-code-3", "claude-code", 3, true},
		{"claude-42", "claude", 42, true},
		{"noNumber-x", "", 0, false},
		{"trailing-", "", 0, false},
		{"-1", "", 0, false},
		{"nodigits", "", 0, false},
		{"zero-0", "", 0, false},
	}
	for _, c := range cases {
		agent, n, ok := parseID(c.id)
		if agent != c.agent || n != c.n || ok != c.ok {
			t.Errorf("parseID(%q) = (%q,%d,%v), want (%q,%d,%v)", c.id, agent, n, ok, c.agent, c.n, c.ok)
		}
	}
}
