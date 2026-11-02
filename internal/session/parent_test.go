package session

import (
	"testing"
	"time"

	"github.com/culiops/culibox/internal/launcher"
)

func TestSpawnRecordsParentID(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/bash"}
	m, path := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: t.TempDir(), ParentID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	var got *Info
	for _, in := range m.List() {
		if in.ID == s.ID {
			in := in
			got = &in
		}
	}
	if got == nil || got.ParentID != "p1" {
		t.Fatalf("List() entry = %+v, want ParentID p1", got)
	}
	if md := sidecarEntry(t, path, s.ID); md.ParentID != "p1" {
		t.Fatalf("sidecar ParentID = %q, want p1", md.ParentID)
	}
}

func TestReconcileRestoresParentID(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-1")
	fb.seedLive("bash-1")
	now := time.Now()
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-1": {Agent: "claude", Cwd: "/w", Created: now.Add(-time.Minute)},
		"bash-1":   {Agent: "bash", Cwd: "/w", Created: now, ParentID: "claude-1"},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("bash-1"); _ = m.Kill("claude-1") })
	s, ok := m.Get("bash-1")
	if !ok {
		t.Fatal("not reattached")
	}
	if got := s.Info().ParentID; got != "claude-1" {
		t.Fatalf("restored ParentID = %q, want claude-1", got)
	}
}

// A parent that did not survive the restart frees its id: the counter is
// reseeded from live ids only, so the next session of that agent may reuse it.
// The surviving shell must not keep a link a stranger could inherit.
func TestReconcileDropsParentIDOfDeadParent(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("bash-1")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-1": {Agent: "claude", Cwd: "/w", Created: time.Now().Add(-time.Minute)},
		"bash-1":   {Agent: "bash", Cwd: "/w", Created: time.Now(), ParentID: "claude-1"},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("bash-1") })
	s, ok := m.Get("bash-1")
	if !ok {
		t.Fatal("not reattached")
	}
	if got := s.Info().ParentID; got != "" {
		t.Fatalf("ParentID of a dead parent = %q, want \"\"", got)
	}
	if md := sidecarEntry(t, path, "bash-1"); md.ParentID != "" {
		t.Fatalf("sidecar ParentID = %q, want \"\"", md.ParentID)
	}
}

func TestRestartKeepsParentID(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/bash"}
	m, _ := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "bash", Cwd: t.TempDir(), ParentID: "p1"})
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if got := l.last().ParentID; got != "p1" {
		t.Fatalf("restart spec ParentID = %q, want p1", got)
	}
	if got := ns.Info().ParentID; got != "p1" {
		t.Fatalf("restarted session ParentID = %q, want p1", got)
	}
}
