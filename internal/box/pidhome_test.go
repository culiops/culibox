package box

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomePidRel(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want string
		ok   bool
	}{
		{"s1", ".agent-box/pids/s1.pid", true},
		{"claude-12", ".agent-box/pids/claude-12.pid", true},
		{"a.b_c-d", ".agent-box/pids/a.b_c-d.pid", true},
		{"../x", "", false},
		{"a/b", "", false},
		{"", "", false},
		{"-x", "", false},
		{".x", "", false},
		{strings.Repeat("a", 129), "", false},
	} {
		got, ok := HomePidRel(tc.id)
		if got != tc.want || ok != tc.ok {
			t.Errorf("HomePidRel(%q) = %q, %v; want %q, %v", tc.id, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := HomePidRel(strings.Repeat("a", 128)); !ok {
		t.Error("a 128-character id must be accepted")
	}
}

func TestExecArgvPassesHomePidfile(t *testing.T) {
	m := newTestManager(t)
	got := m.execArgv("ab-box-x", "/w", "s1", []string{"claude", "--model", "opus"}, nil)
	i := indexOf(got, inContainerWrap)
	if i < 0 {
		t.Fatalf("wrapper missing from %q", got)
	}
	want := []string{"/tmp/ac-session-s1.pid", "/home/agent/.agent-box/pids/s1.pid", "claude", "--model", "opus"}
	if rest := got[i+1:]; strings.Join(rest, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv after the wrapper = %q, want %q", rest, want)
	}
}

func TestExecArgvBadIDUsesDevNull(t *testing.T) {
	m := newTestManager(t)
	got := m.execArgv("ab-box-x", "/w", "../evil", []string{"claude"}, nil)
	i := indexOf(got, inContainerWrap)
	if i < 0 || len(got) < i+4 {
		t.Fatalf("unexpected argv %q", got)
	}
	if got[i+2] != "/dev/null" || got[i+3] != "claude" {
		t.Fatalf("a bad id must pass /dev/null as the home pidfile, got %q", got[i+1:])
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestInContainerWrapWritesBothPidfiles(t *testing.T) {
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	out, err := exec.Command("/bin/sh", "-c", inContainerWrap, a, b, "sh", "-c", "echo $$ > "+c).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	pa, pb, pc := readTrim(t, a), readTrim(t, b), readTrim(t, c)
	if pa == "" || pa != pb || pa != pc {
		t.Fatalf("pidfiles disagree: a=%q b=%q c=%q", pa, pb, pc)
	}
}

func TestInContainerWrapSurvivesUnwritableHomePid(t *testing.T) {
	dir := t.TempDir()
	a, c := filepath.Join(dir, "a"), filepath.Join(dir, "c")
	b := filepath.Join(dir, "missing", "b")
	out, err := exec.Command("/bin/sh", "-c", inContainerWrap, a, b, "sh", "-c", "echo $$ > "+c).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Fatalf("the home pidfile failure must be silent, got %q", out)
	}
	if pa, pc := readTrim(t, a), readTrim(t, c); pa == "" || pa != pc {
		t.Fatalf("agent did not run as the recorded pid: a=%q c=%q", pa, pc)
	}
	if _, err := os.Stat(b); err == nil {
		t.Fatal("home pidfile must not exist")
	}
}

func readTrim(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestPrepareHomePidRemovesStale(t *testing.T) {
	m := newTestManager(t)
	home := m.stateDir("ab-box-x")
	pids := filepath.Join(home, HomePidDir)
	if err := os.MkdirAll(pids, 0o700); err != nil {
		t.Fatal(err)
	}
	stale, other := filepath.Join(pids, "s1.pid"), filepath.Join(pids, "s2.pid")
	for _, p := range []string{stale, other} {
		if err := os.WriteFile(p, []byte("99\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.prepareHomePid("ab-box-x", "s1")
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile survived: %v", err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatalf("another session's pidfile was removed: %v", err)
	}
}

func TestPrepareHomePidCreatesDirWhenAbsent(t *testing.T) {
	m := newTestManager(t)
	home := m.stateDir("ab-box-x")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	m.prepareHomePid("ab-box-x", "s1")
	fi, err := os.Stat(filepath.Join(home, HomePidDir))
	if err != nil || !fi.IsDir() {
		t.Fatalf("pid dir not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("pid dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

func TestPrepareHomePidRefusesSymlinkedDir(t *testing.T) {
	m := newTestManager(t)
	home := m.stateDir("ab-box-x")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "pids", "s1.pid")
	if err := os.MkdirAll(filepath.Dir(victim), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".agent-box")); err != nil {
		t.Fatal(err)
	}
	m.prepareHomePid("ab-box-x", "s1") // must not panic or follow the link
	if got := readTrim(t, victim); got != "keep" {
		t.Fatalf("a file outside the home was touched: %q", got)
	}
	ents, _ := os.ReadDir(outside)
	if len(ents) != 1 {
		t.Fatalf("something was created outside the home: %v", ents)
	}
}

func TestPrepareHomePidNeverFailsOnMissingHome(t *testing.T) {
	m := newTestManager(t)
	m.prepareHomePid("ab-box-nope", "s1") // no state dir: logs, returns
	m.prepareHomePid("ab-box-nope", "../bad")
}
