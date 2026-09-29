package session

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

func claudeSIDOf(s *Session) string { return s.claudeSID() }

func TestLocateRecordsClaudeSessionID(t *testing.T) {
	m, home := newLocatorManager(t)
	sc, err := openSidecar(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	sc.put("s1", sessionMeta{Agent: "claude", Cwd: locCwd})
	m.sidecar = sc
	_ = peekText(t, m, "s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "aaaa-1" {
		t.Fatalf("claudeSessionID=%q want aaaa-1", got)
	}
	if got := sc.all()["s1"].ClaudeSessionID; got != "aaaa-1" {
		t.Fatalf("sidecar ClaudeSessionID=%q want aaaa-1", got)
	}
	// /clear starts a new conversation: the id follows it.
	writeTx(t, home, locCwd, "cccc-3", "from C", time.Now().Add(-20*time.Minute))
	writeReg(t, home, 101, regJSON(101, "cccc-3", "5000", "idle"))
	_ = peekText(t, m, "s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "cccc-3" {
		t.Fatalf("after /clear claudeSessionID=%q want cccc-3", got)
	}
}

func TestLocateNeverRecordsForAnExitedSession(t *testing.T) {
	m, _ := newLocatorManager(t)
	m.sessions["s1"].status = statusExited
	m.PeekFor("s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "" {
		t.Fatalf("an exited session recorded %q from a pid that may be reused", got)
	}
}

func TestReconcileRestoresClaudeSessionID(t *testing.T) {
	for _, c := range []struct{ stored, want string }{{"abcd-1234", "abcd-1234"}, {"../../etc", ""}} {
		fb := newFakeBackend()
		fb.seedLive("claude-2")
		path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
			"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), ClaudeSessionID: c.stored},
		})
		m := newReconcileManager(t, fb, path)
		if err := m.Reconcile(); err != nil {
			t.Fatal(err)
		}
		s, _ := m.Get("claude-2")
		if got := claudeSIDOf(s); got != c.want {
			t.Errorf("stored %q restored as %q, want %q", c.stored, got, c.want)
		}
		_ = m.Kill("claude-2")
	}
}

func TestResumeFor(t *testing.T) {
	cases := []struct{ runtime, bin, sid, want string }{
		{config.RuntimeHost, "/usr/bin/claude", "abcd-1", "abcd-1"},
		{config.RuntimeBox, "/usr/bin/claude", "abcd-1", ""}, // boxed restarts fresh
		{config.RuntimeHost, "codex", "abcd-1", ""},          // codex restarts fresh
		{config.RuntimeHost, "/usr/bin/claude", "", ""},      // no id known
		{config.RuntimeHost, "/usr/bin/claude", "a/b", ""},   // never a path-ish id
	}
	for _, c := range cases {
		if got := resumeFor(c.runtime, c.bin, c.sid); got != c.want {
			t.Errorf("resumeFor(%q,%q,%q)=%q want %q", c.runtime, c.bin, c.sid, got, c.want)
		}
	}
}

// exitedSession spawns a session whose process exits at once and waits for it.
func exitedSession(t *testing.T, m *Manager, spec launcher.SpawnSpec) *Session {
	t.Helper()
	s, err := m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, statusExited, 5*time.Second)
	return s
}

func TestRestartRespawnsWithTheSameSpec(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude", replyDefault: false}
	m, _ := newCtlManager(t, l)
	yes := true
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Name: "api", Cwd: cwd,
		Options: map[string]any{"effort": "low"}, Replyable: &yes})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	got := l.last()
	if got.Agent != "claude" || got.Name != "api" || got.Cwd != cwd || got.Runtime != config.RuntimeHost ||
		got.Options["effort"] != "low" || got.Resume != "abcd-1234" || got.Replyable != nil || got.Prompt != "" {
		t.Fatalf("restart spec = %+v", got)
	}
	if ns.ID == old.ID {
		t.Fatal("restart must spawn a NEW session")
	}
	if _, ok := m.Get(old.ID); ok {
		t.Fatal("the old session is still listed")
	}
	if ns.Info().Replyable {
		t.Fatal("replies were inherited; a restart starts at the profile default")
	}
}

func TestRestartRefusesLiveAndUnknown(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if _, err := m.Restart(s.ID); !errors.Is(err, ErrNotExited) {
		t.Fatalf("live: err=%v want ErrNotExited", err)
	}
	if _, err := m.Restart("nope-1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown: err=%v want ErrNoSession", err)
	}
}

func TestRestartTwiceStartsOneReplacement(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ns, err := m.Restart(old.ID); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
				t.Cleanup(func() { _ = m.Kill(ns.ID) })
			} else if !errors.Is(err, ErrRestarting) && !errors.Is(err, ErrNoSession) {
				t.Errorf("loser err=%v, want ErrRestarting or ErrNoSession", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d replacements started, want exactly 1", wins)
	}
}

func TestListMarksResumable(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	s := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	find := func() Info {
		for _, i := range m.List() {
			if i.ID == s.ID {
				return i
			}
		}
		t.Fatal("session not listed")
		return Info{}
	}
	if find().Resumable {
		t.Fatal("resumable without a known conversation id")
	}
	s.mu.Lock()
	s.claudeSessionID = "abcd-1234"
	s.mu.Unlock()
	if !find().Resumable {
		t.Fatal("an exited host claude with an id must be resumable")
	}
}
