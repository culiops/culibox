package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

const testConvFolder = "aaaaaaaaaaaa" // a conversations folder name: 12 lowercase hex digits

// writeConvTx is writeTx for a conversations folder: <conv>/<slug>/<sid>.jsonl.
func writeConvTx(t *testing.T, conv, cwd, sid, text string, mtime time.Time) string {
	t.Helper()
	tmp := t.TempDir()
	src := writeTx(t, tmp, cwd, sid, text, mtime)
	dst := filepath.Join(conv, projectSlug(cwd), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

// A boxed spawn names its profile to the box layer, and keeps the key of the
// folder the box mounts, on the session and in its record.
func TestBoxedSpawnRecordsTheTranscriptKey(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	boxes.conv = testConvFolder
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if boxes.lastKey.Profile != "claude" {
		t.Fatalf("BoxKey.Profile = %q, want the profile name", boxes.lastKey.Profile)
	}
	if s.transcriptKey != testConvFolder {
		t.Fatalf("session key = %q", s.transcriptKey)
	}
	if md := sidecarEntry(t, path, s.ID); md.TranscriptKey != testConvFolder {
		t.Fatalf("record key = %q", md.TranscriptKey)
	}
	// A malformed key from the box layer is never kept: it is joined into a path.
	boxes.conv = "../../x"
	s2, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s2.ID)
	if s2.transcriptKey != "" {
		t.Fatalf("bad key kept: %q", s2.transcriptKey)
	}
}

// A restart reads the key back from the record, so a profile renamed or
// removed since does not lose the session's conversations.
func TestReconcileRestoresTheTranscriptKey(t *testing.T) {
	be := newFakeBackend()
	be.alive["claude-1"] = true
	be.list = []string{"claude-1"}
	m := NewManager(tieredLauncher{argv: []string{"true"}, runtime: config.RuntimeBox}, be, 1<<16, time.Minute)
	if err := m.SetSidecarPath(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	m.sidecar.put("claude-1", sessionMeta{Agent: "claude", Cwd: t.TempDir(), Created: time.Now(), Runtime: config.RuntimeBox,
		Box: "ab-box-live", TranscriptKey: testConvFolder})
	m.SetBoxes(&fakeBoxes{})
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	s := m.sessions["claude-1"]
	m.mu.Unlock()
	if s == nil || s.transcriptKey != testConvFolder {
		t.Fatalf("restored session %+v", s)
	}
}

// boxedConvManager is boxedRegManager with every transcript in the
// conversations folder instead of the home.
func boxedConvManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	m, bd := boxedRegManager(t)
	conv := box.ConversationsDir(m.stateDir, testConvFolder)
	for _, sid := range []string{"aaaa-1", "bbbb-2"} {
		home := filepath.Join(bd, ".claude", "projects", projectSlug(locCwd), sid+".jsonl")
		dst := filepath.Join(conv, projectSlug(locCwd), sid+".jsonl")
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(home, dst); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"s1", "s2"} {
		m.sessions[id].transcriptKey = testConvFolder
	}
	return m, bd, conv
}

// Every reader finds a transcript that is only in the conversations folder.
func TestBoxedReadersFindTheConversationsFolder(t *testing.T) {
	m, _, conv := boxedConvManager(t)
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("live peek = %q", got)
	}
	if u, ok := m.UsageFor("s2"); !ok || u.Model != "model-bbbb-2" {
		t.Errorf("usage = %+v %v", u, ok)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(conv, projectSlug(locCwd), "aaaa-1.jsonl"),
		txBytes(txRecTS("assistant", "m1", ts, txBash("ls")), txRecTS("assistant", "m2", ts, txText("from A"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if tl, ok := m.TimelineFor("s1", 0, 50); !ok || len(tl.Events) == 0 {
		t.Errorf("timeline = %+v %v", tl, ok)
	}
	// Exited: no registry, the exact <sid>.jsonl of the recorded conversation.
	s := m.sessions["s1"]
	s.mu.Lock()
	s.status = statusExited
	s.claudeSessionID = "aaaa-1"
	s.mu.Unlock()
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("exited peek = %q", got)
	}
}

// A session with no key (a box from before #330) reads its home, as before;
// one with a key still finds what was left in the home.
func TestBoxedReadersFallBackToTheHome(t *testing.T) {
	m, bd := boxedRegManager(t)
	m.sessions["s1"].transcriptKey = testConvFolder // folder empty: the home still has A
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("peek = %q", got)
	}
	_ = bd
}

// The folder is written by every box of the profile: a link there is never read.
func TestBoxedReadersRefuseALinkInTheFolder(t *testing.T) {
	m, _, conv := boxedConvManager(t)
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	if err := os.WriteFile(outside, []byte(`{"message":{"role":"assistant","content":[{"type":"text","text":"leak"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(conv, projectSlug(locCwd), "aaaa-1.jsonl")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if p, ok := m.PeekFor("s1"); ok && p.Text == "leak" {
		t.Fatal("read through a link in the conversations folder")
	}
}

// The resume guard looks where the box's claude will: the folder when the box
// mounts one, else the home.
func TestBoxedResumeGuardLooksInTheFolderTheBoxMounts(t *testing.T) {
	run := func(t *testing.T, key string, write func(m *Manager, home, cwd string)) []string {
		m, home, argvFile := boxedRestartManager(t)
		m.boxes.(*fakeBoxes).conv = key
		cwd := t.TempDir()
		old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
		old.mu.Lock()
		old.claudeSessionID = "abcd-1234"
		old.mu.Unlock()
		write(m, home, cwd)
		ns, err := m.Restart(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(ns.ID) })
		return ranArgv(t, ns, argvFile)
	}
	resumed := func(argv []string) bool {
		i := slices.Index(argv, "--resume")
		return i >= 0 && i+1 < len(argv) && argv[i+1] == "abcd-1234"
	}
	t.Run("in the folder", func(t *testing.T) {
		argv := run(t, testConvFolder, func(m *Manager, _, cwd string) {
			writeConvTx(t, box.ConversationsDir(m.stateDir, testConvFolder), cwd, "abcd-1234", "hi", time.Now())
		})
		if !resumed(argv) {
			t.Fatalf("argv = %v, want --resume", argv)
		}
	})
	t.Run("only in the home the folder hides", func(t *testing.T) {
		argv := run(t, testConvFolder, func(_ *Manager, home, cwd string) {
			writeTx(t, home, cwd, "abcd-1234", "hi", time.Now())
		})
		if resumed(argv) {
			t.Fatalf("argv = %v: claude would not find it", argv)
		}
	})
	t.Run("no folder: the home", func(t *testing.T) {
		argv := run(t, "", func(_ *Manager, home, cwd string) {
			writeTx(t, home, cwd, "abcd-1234", "hi", time.Now())
		})
		if !resumed(argv) {
			t.Fatalf("argv = %v, want --resume", argv)
		}
	})
}
