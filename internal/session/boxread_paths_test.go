package session

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// boxedLocManager: one claude session "s1" running in box "box-1". Returns the
// manager and the box's state dir (<state>/boxstate/box-1).
func boxedLocManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m, _ := newLocatorManager(t)
	state := t.TempDir()
	m.stateDir = state
	m.sessions["s1"].Box = "box-1"
	bd := filepath.Join(state, "boxstate", "box-1")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	return m, bd
}

// projDirIn is <home>/.claude/projects/<slug of locCwd>.
func projDirIn(home string) string {
	return filepath.Join(home, ".claude", "projects", projectSlug(locCwd))
}

// bindSID gives a session the persisted conversation id that binds it to
// <sid>.jsonl (boundTranscript's only route when no registry validated).
func bindSID(m *Manager, id, sid string) {
	s := m.sessions[id]
	s.mu.Lock()
	s.claudeSessionID = sid
	s.mu.Unlock()
}

// within2s fails the test if f does not return in time: a read that blocks on
// a FIFO is the failure mode these tests exist for.
func within2s(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked for 2s (a FIFO transcript was opened)", what)
	}
}

func mkFifo(t *testing.T, p string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestBoxedPeekIgnoresFIFOTranscript(t *testing.T) {
	m, bd := boxedLocManager(t)
	now := time.Now()
	writeTx(t, bd, locCwd, "older-1", "from regular", now.Add(-10*time.Minute))
	mkFifo(t, filepath.Join(projDirIn(bd), "x.jsonl"), now)

	// The heuristic still ranks past the FIFO to the regular file.
	var l located
	within2s(t, "locate", func() { l = m.locate("s1") })
	if got := filepath.Base(l.path); got != "older-1.jsonl" {
		t.Errorf("locate path=%q, want the FIFO skipped and older-1.jsonl chosen", got)
	}
	// A session bound to the FIFO's own name (its persisted sid) must miss
	// without ever opening it.
	bindSID(m, "s1", "x")
	var ok bool
	within2s(t, "PeekFor", func() { _, ok = m.PeekFor("s1") })
	if ok {
		t.Error("PeekFor hit a FIFO transcript")
	}
}

func TestBoxedPeekOnlyFIFOMisses(t *testing.T) {
	m, bd := boxedLocManager(t)
	mkFifo(t, filepath.Join(projDirIn(bd), "x.jsonl"), time.Now())
	bindSID(m, "s1", "x") // bound, so transcriptPathIn's regular-file Lstat check is what refuses
	var ok bool
	within2s(t, "PeekFor", func() { _, ok = m.PeekFor("s1") })
	if ok {
		t.Error("PeekFor hit with only a FIFO transcript")
	}
}

func TestBoxedPeekIgnoresSymlinkedTranscript(t *testing.T) {
	m, bd := boxedLocManager(t)
	host := writeTx(t, t.TempDir(), locCwd, "hostfile", "SECRET", time.Now())
	if err := os.MkdirAll(projDirIn(bd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, filepath.Join(projDirIn(bd), "x.jsonl")); err != nil {
		t.Fatal(err)
	}
	bindSID(m, "s1", "x") // bound, so transcriptPathIn's regular-file Lstat check (not "unbound") is what refuses
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek hit through a symlinked transcript: %q", p.Text)
	}
	if u, ok := m.UsageFor("s1"); ok {
		t.Errorf("usage hit through a symlinked transcript: %+v", u)
	}
}

func TestBoxedPeekIgnoresSymlinkedProjectsDir(t *testing.T) {
	m, bd := boxedLocManager(t)
	hostHome := t.TempDir()
	writeTx(t, hostHome, locCwd, "hostfile", "SECRET", time.Now())
	if err := os.MkdirAll(filepath.Dir(projDirIn(bd)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projDirIn(hostHome), projDirIn(bd)); err != nil {
		t.Fatal(err)
	}
	bindSID(m, "s1", "hostfile") // bound; the slug dir is the symlink, so transcriptPathIn's directory check (root.Lstat + IsDir) refuses
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek hit through a symlinked projects dir: %q", p.Text)
	}
}

func TestBoxedReadsRegularTranscript(t *testing.T) {
	m, bd := boxedLocManager(t)
	writeTx(t, bd, locCwd, "ok-1", "from box", time.Now())
	bindSID(m, "s1", "ok-1") // bound by its persisted conversation id
	if got := peekText(t, m, "s1"); got != "from box" {
		t.Errorf("peek=%q want %q", got, "from box")
	}
	if u, ok := m.UsageFor("s1"); !ok || u.Model != "model-ok-1" {
		t.Errorf("usage=%+v ok=%v", u, ok)
	}
	if _, ok := m.AttributionFor("s1"); !ok {
		t.Error("attribution missed a regular boxed transcript")
	}
}

func TestAttributionBoxedRefusesFIFO(t *testing.T) {
	m, bd := boxedLocManager(t)
	mkFifo(t, filepath.Join(projDirIn(bd), "x.jsonl"), time.Now())
	bindSID(m, "s1", "x") // bound, so transcriptPathIn's regular-file Lstat check is what refuses
	var ok bool
	within2s(t, "AttributionFor", func() { _, ok = m.AttributionFor("s1") })
	if ok {
		t.Error("AttributionFor hit with only a FIFO transcript")
	}
}

func TestAttributionBoxedRefusesSymlink(t *testing.T) {
	m, bd := boxedLocManager(t)
	host := writeTx(t, t.TempDir(), locCwd, "hostfile", "SECRET", time.Now())
	if err := os.MkdirAll(projDirIn(bd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, filepath.Join(projDirIn(bd), "x.jsonl")); err != nil {
		t.Fatal(err)
	}
	bindSID(m, "s1", "x") // bound, so transcriptPathIn's regular-file Lstat check (not "unbound") is what refuses
	if _, ok := m.AttributionFor("s1"); ok {
		t.Error("AttributionFor followed a symlinked transcript")
	}
}

func TestOpenTranscriptRefusesPathOutsideBoxDir(t *testing.T) {
	bd := t.TempDir()
	outside := writeTx(t, t.TempDir(), locCwd, "o", "x", time.Now())
	l := located{claude: true, path: outside, boxDir: bd}
	if f, _, ok := l.openTranscript(); ok {
		f.Close()
		t.Error("openTranscript opened a path outside boxDir")
	}
}

func TestOpenTranscriptHost(t *testing.T) {
	p := writeTx(t, t.TempDir(), locCwd, "h", "x", time.Now())
	l := located{claude: true, path: p}
	f, fi, ok := l.openTranscript()
	if !ok {
		t.Fatal("host open failed")
	}
	f.Close()
	if fi.Size() == 0 {
		t.Error("host fi has no size")
	}
}

func TestBoxedEmptyStateDirYieldsNoTranscript(t *testing.T) {
	m, home := newLocatorManager(t) // host transcripts exist under home/.claude
	m.stateDir = ""
	m.sessions["s1"].Box = "box-1"
	m.sessions["s1"].ConfigDir = filepath.Join(home, ".claude")
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek hit with no state dir: %q", p.Text)
	}
	if u, ok := m.UsageFor("s1"); ok {
		t.Errorf("usage hit with no state dir: %+v", u)
	}
	if _, ok := m.AttributionFor("s1"); ok {
		t.Error("attribution hit with no state dir")
	}
}

// The final-open guard: a path inside boxDir that is (or was swapped to) a FIFO
// or a symlink out must not be opened, whatever the lister said earlier.
func TestOpenTranscriptRefusesFIFOInsideBoxDir(t *testing.T) {
	bd := t.TempDir()
	p := filepath.Join(bd, ".claude", "projects", "x", "t.jsonl")
	mkFifo(t, p, time.Now())
	l := located{claude: true, boxDir: bd, path: p}
	var ok bool
	within2s(t, "openTranscript", func() {
		var f *os.File
		f, _, ok = l.openTranscript()
		if f != nil {
			f.Close()
		}
	})
	if ok {
		t.Error("openTranscript opened a FIFO")
	}
}

func TestOpenTranscriptRefusesSymlinkInsideBoxDir(t *testing.T) {
	bd := t.TempDir()
	host := writeTx(t, t.TempDir(), locCwd, "hostfile", "SECRET", time.Now())
	p := filepath.Join(bd, "t.jsonl")
	if err := os.Symlink(host, p); err != nil {
		t.Fatal(err)
	}
	l := located{claude: true, boxDir: bd, path: p}
	var ok bool
	within2s(t, "openTranscript", func() {
		var f *os.File
		f, _, ok = l.openTranscript()
		if f != nil {
			f.Close()
		}
	})
	if ok {
		t.Error("openTranscript followed a symlink out of boxDir")
	}
}

func TestBoxedPeekIgnoresSlugDirSymlinkedInsideBox(t *testing.T) {
	m, bd := boxedLocManager(t)
	other := filepath.Join(bd, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x.jsonl"), []byte(`{"message":{"role":"assistant","content":[{"type":"text","text":"inside"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(projDirIn(bd)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, projDirIn(bd)); err != nil {
		t.Fatal(err)
	}
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek hit through a slug dir symlinked inside the box: %q", p.Text)
	}
}
