package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/launcher"
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

// regLauncher only knows agents that have been "registered" — mirroring how
// runtime profiles reach the launcher only after startup, AFTER Reconcile.
type regLauncher struct{ known map[string]string }

func (l *regLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return []string{"x"}, []string{}, nil
}
func (l *regLauncher) BinFor(s launcher.SpawnSpec) (string, error) {
	if b, ok := l.known[s.Agent]; ok {
		return b, nil
	}
	return "", fmt.Errorf("unknown agent %q", s.Agent)
}

// TestResolveBinsAfterProfilesRegistered: a profile-based session reconciled
// before its profile is registered reattaches with an empty Bin; ResolveBins,
// called once the profile is known, backfills it so usage works after a restart.
func TestResolveBinsAfterProfilesRegistered(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("proj-1")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"proj-1": {Agent: "proj", Cwd: "/w", Created: time.Now()},
	})
	lnch := &regLauncher{known: map[string]string{}} // "proj" not registered yet
	m := NewManager(lnch, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("proj-1") })
	s, ok := m.Get("proj-1")
	if !ok {
		t.Fatal("not reattached")
	}
	if s.Bin != "" {
		t.Fatalf("Bin=%q, want empty before the profile is registered", s.Bin)
	}
	lnch.known["proj"] = "claude" // profiles now loaded into the launcher
	m.ResolveBins()
	if s.Bin != "claude" {
		t.Fatalf("Bin=%q after ResolveBins, want claude", s.Bin)
	}
}

// TestReconcileResolvesBin: a reconciled session re-resolves its agent bin from
// the name (the sidecar doesn't store it), so claude-only features like usage keep
// working after a daemon restart. tieredLauncher.BinFor returns "claude".
func TestReconcileResolvesBin(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: time.Now()},
	})
	m := NewManager(tieredLauncher{}, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("claude-2") })
	s, ok := m.Get("claude-2")
	if !ok {
		t.Fatal("survivor not re-attached")
	}
	if s.Bin != "claude" {
		t.Fatalf("Bin=%q, want claude (reconcile must re-resolve it)", s.Bin)
	}
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

// TestReconcileRestoresReplyable: an operator's opt-in must survive a daemon
// restart, not just a decode-into-a-bare-struct check. A sidecar entry written
// with Replyable: true must come back true after Reconcile re-attaches it —
// the actual cross-restart path the task exists to deliver, not merely the
// zero-value default the other Replyable tests pin.
func TestReconcileRestoresReplyable(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: time.Now(), Replyable: true},
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
	if !s.Info().Replyable {
		t.Fatal("Replyable=false after Reconcile, want true: the sidecar said true and must be restored, not silently reverted")
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

// TestReconcileCleansLingeringDead guards S1: abduco keeps a socket after the
// agent exits, so a session that died during downtime is still listed. Reconcile
// must NOT resurrect it — it detects the dead inner, tears the socket down, and
// drops the sidecar entry.
func TestReconcileCleansLingeringDead(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLingering("bash-1") // listed, but InnerAlive == false
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"bash-1": {Agent: "bash", Created: time.Now()},
	})
	m := newReconcileManager(t, fb, path)

	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("bash-1"); ok {
		t.Fatal("lingering-dead session must not be re-attached")
	}
	if !fb.wasKilled("bash-1") {
		t.Fatal("lingering-dead socket not torn down (backend.Kill not called)")
	}
	sc, _ := openSidecar(path)
	if _, ok := sc.all()["bash-1"]; ok {
		t.Fatal("sidecar entry for dead session not dropped")
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

// ResolveBins backfills Bin after profiles load; the activity gate must follow,
// or every reconciled profile session would read "idle" forever.
func TestResolveBinsMarksAgentKind(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("my-project-1")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"my-project-1": {Agent: "my-project", Cwd: "/work", Created: time.Now()},
	})
	l := &regLauncher{known: map[string]string{}}
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("my-project-1") })
	s, ok := m.Get("my-project-1")
	if !ok {
		t.Fatal("survivor not re-attached")
	}
	s.mu.Lock()
	s.lastActivityAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if got := s.Info().Activity; got != "idle" {
		t.Fatalf("before ResolveBins: %q, want idle (bin unknown, so not an agent)", got)
	}
	l.known["my-project"] = "/usr/local/bin/claude"
	m.ResolveBins()
	if got := s.Info().Activity; got != "waiting" {
		t.Fatalf("after ResolveBins: %q, want waiting (claude, pre-feature sidecar => submitted)", got)
	}
}
