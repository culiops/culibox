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

	var p Peek
	var ok bool
	within2s(t, "PeekFor", func() { p, ok = m.PeekFor("s1") })
	if ok && p.Text != "from regular" {
		t.Errorf("peek=%q, want the older regular transcript or a miss", p.Text)
	}
}

func TestBoxedPeekOnlyFIFOMisses(t *testing.T) {
	m, bd := boxedLocManager(t)
	mkFifo(t, filepath.Join(projDirIn(bd), "x.jsonl"), time.Now())
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
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek hit through a symlinked projects dir: %q", p.Text)
	}
}

func TestBoxedReadsRegularTranscript(t *testing.T) {
	m, bd := boxedLocManager(t)
	writeTx(t, bd, locCwd, "ok-1", "from box", time.Now())
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
