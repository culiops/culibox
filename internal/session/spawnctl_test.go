package session

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// ctlLauncher is a Builder that answers every optional launcher interface and
// records each spec it is asked to build.
type ctlLauncher struct {
	mu           sync.Mutex
	specs        []launcher.SpawnSpec
	argv         []string
	bin          string
	runtime      string // RuntimeFor's answer when the spec names none ("" → host)
	replyDefault bool
}

func (l *ctlLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	l.mu.Lock()
	l.specs = append(l.specs, spec)
	l.mu.Unlock()
	return append([]string{}, l.argv...), []string{"TERM=xterm"}, nil
}
func (l *ctlLauncher) BinFor(launcher.SpawnSpec) (string, error) { return l.bin, nil }
func (l *ctlLauncher) RuntimeFor(spec launcher.SpawnSpec) (string, error) {
	if spec.Runtime != "" {
		return spec.Runtime, nil
	}
	if l.runtime != "" {
		return l.runtime, nil
	}
	return config.RuntimeHost, nil
}
func (l *ctlLauncher) ReplyableFor(spec launcher.SpawnSpec) bool {
	if spec.Replyable != nil {
		return *spec.Replyable
	}
	return l.replyDefault
}
func (l *ctlLauncher) AcceptedOptions(spec launcher.SpawnSpec) map[string]any {
	if len(spec.Options) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range spec.Options {
		out[k] = v
	}
	return out
}
func (l *ctlLauncher) last() launcher.SpawnSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.specs[len(l.specs)-1]
}

// newCtlManager: an ephemeral (no backend) manager with a real sidecar file.
func newCtlManager(t *testing.T, l *ctlLauncher) (*Manager, string) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetRepliesEnabled(true) // spawn/restart tests assume the feature is on; the off switch has its own tests
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	return m, path
}

func sidecarEntry(t *testing.T, path, id string) sessionMeta {
	t.Helper()
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	return sc.all()[id]
}

func TestSpawnWithPromptStartsSubmitted(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, path := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir(), Prompt: "fix the build please"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if !submittedOf(s) {
		t.Fatal("a session spawned with a first message must start submitted")
	}
	if got := l.last().Prompt; got != "fix the build please" {
		t.Fatalf("Build saw Prompt=%q", got)
	}
	if md := sidecarEntry(t, path, s.ID); md.Submitted == nil || !*md.Submitted {
		t.Fatalf("sidecar Submitted=%v, want true", md.Submitted)
	}
}

func TestSpawnWithoutPromptIsNotSubmitted(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if submittedOf(s) {
		t.Fatal("no first message, yet the session starts submitted")
	}
}

func TestSpawnReplyable(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		bin      string
		def      bool
		override *bool
		want     bool
	}{
		{"profile default off", "/usr/bin/claude", false, nil, false},
		{"profile default on", "/usr/bin/claude", true, nil, true},
		{"override off beats default on", "/usr/bin/claude", true, &no, false},
		{"override on beats default off", "/usr/bin/claude", false, &yes, true},
		{"non-claude is never replyable", "/bin/bash", true, &yes, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: c.bin, replyDefault: c.def}
			m, path := newCtlManager(t, l)
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Cwd: t.TempDir(), Replyable: c.override})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			if got := s.Info().Replyable; got != c.want {
				t.Errorf("Info().Replyable=%v want %v", got, c.want)
			}
			if got := sidecarEntry(t, path, s.ID).Replyable; got != c.want {
				t.Errorf("sidecar Replyable=%v want %v", got, c.want)
			}
		})
	}
}

func TestSpawnKeepsAcceptedOptions(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, path := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir(), Options: map[string]any{"effort": "low"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if got := s.Info().Options["effort"]; got != "low" {
		t.Fatalf("Info().Options[effort]=%v want low", got)
	}
	if got := sidecarEntry(t, path, s.ID).Options["effort"]; got != "low" {
		t.Fatalf("sidecar Options[effort]=%v want low", got)
	}
}

func TestReconcileRestoresOptions(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: time.Now(), Options: map[string]any{"effort": "low", "skip": true}},
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
	o := s.Info().Options
	if o["effort"] != "low" || o["skip"] != true {
		t.Fatalf("restored Options=%v, want effort=low skip=true", o)
	}
}

// Decision P5: restore keeps the PERSISTED opt-in (TestReconcileRestoresReplyable);
// the new profile default must not leak into a reattached session.
func TestProfileReplyDefaultDoesNotLeakIntoRestore(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/work/proj", Created: time.Now(), Replyable: false},
	})
	m := NewManager(&ctlLauncher{argv: []string{"x"}, bin: "claude", replyDefault: true}, fb, 1<<16, time.Minute)
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
	if s.Info().Replyable {
		t.Fatal("a reattached session picked up the profile's reply default")
	}
}

// Decision P3: the bundle's flags are handed to Build (which places them before
// the "--"), never appended after its argv.
func TestBoxedSpawnHandsBundleFlagsToBuild(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "sleep 30"}, bin: "claude", runtime: config.RuntimeBox}
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	m.SetBundles(&fakeBundles{mounts: []box.Mount{{Host: "/b", Box: "/home/agent/.claude/skills"}}, flags: []string{"--strict-mcp-config"}})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := l.last().ExtraArgs; len(got) != 1 || got[0] != "--strict-mcp-config" {
		t.Fatalf("Build saw ExtraArgs=%q, want the bundle flags", got)
	}
	h, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(h.ID)
	if got := l.last().ExtraArgs; len(got) != 0 {
		t.Fatalf("a host spawn got bundle flags %q", got)
	}
}

// The kill-switch beats both the profile default and the spawn override, and
// what lands in the sidecar is the effective value.
func TestSpawnNeverReplyableWhenRepliesDisabled(t *testing.T) {
	yes := true
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude", replyDefault: true}
	m, path := newCtlManager(t, l)
	m.SetRepliesEnabled(false)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "a", Cwd: t.TempDir(), Replyable: &yes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Info().Replyable {
		t.Error("Info().Replyable=true with the kill-switch off")
	}
	if sidecarEntry(t, path, s.ID).Replyable {
		t.Error("sidecar Replyable=true with the kill-switch off")
	}
}
