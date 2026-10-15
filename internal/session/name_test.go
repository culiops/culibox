package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/launcher"
)

func TestDeriveName_DotDot(t *testing.T) {
	if got := deriveName("", "..", "bash-1"); got != "bash-1" {
		t.Fatalf("deriveName('',..,bash-1)=%q want bash-1", got)
	}
}

func TestCapName_RuneBoundary(t *testing.T) {
	got := capName(strings.Repeat("á", 100)) // 100 multibyte runes
	if !utf8.ValidString(got) {
		t.Fatal("capName produced invalid UTF-8 (sliced mid-rune)")
	}
	if n := utf8.RuneCountInString(got); n != maxNameLen {
		t.Fatalf("rune count=%d want %d", n, maxNameLen)
	}
}

func TestDeriveName(t *testing.T) {
	cases := []struct{ custom, cwd, id, want string }{
		{"", "/home/claude/culiops-agent-box", "bash-1", "culiops-agent-box"},
		{"  ", "/work/proj", "bash-1", "proj"},
		{"myname", "/work/proj", "bash-1", "myname"},
		{"", "/", "bash-1", "bash-1"},
		{"", "", "bash-2", "bash-2"},
		{"  trimmed  ", "/x", "bash-1", "trimmed"},
	}
	for _, c := range cases {
		if got := deriveName(c.custom, c.cwd, c.id); got != c.want {
			t.Errorf("deriveName(%q,%q,%q)=%q want %q", c.custom, c.cwd, c.id, got, c.want)
		}
	}
}

func TestSpawn_BlankNameUsesDirBasename(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Cwd: "/work/myproj"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.ID != "bash-1" {
		t.Fatalf("id=%q want bash-1", s.ID)
	}
	if s.Info().Name != "myproj" {
		t.Fatalf("name=%q want myproj", s.Info().Name)
	}
}

func TestSpawn_CustomNameIsLabel(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "editor", Cwd: "/work/proj"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.ID != "bash-1" || s.Info().Name != "editor" {
		t.Fatalf("id=%q name=%q want bash-1/editor", s.ID, s.Info().Name)
	}
}

func TestSpawn_DuplicateCustomNamesGetSuffixed(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	a, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "dup"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "dup"})
	if err != nil {
		t.Fatalf("dup label should be allowed (suffixed): %v", err)
	}
	t.Cleanup(func() { _ = m.Kill(a.ID); _ = m.Kill(b.ID) })
	if a.ID == b.ID {
		t.Fatal("ids must differ")
	}
	if a.Info().Name != "dup" || b.Info().Name != "dup-2" {
		t.Fatalf("names = %q, %q; want dup, dup-2", a.Info().Name, b.Info().Name)
	}
}

// --- uniqueName helper ---

func TestUniqueName_NoCollision(t *testing.T) {
	got := uniqueName("archive", func(string) bool { return false })
	if got != "archive" {
		t.Fatalf("uniqueName=%q want archive", got)
	}
}

func TestUniqueName_One(t *testing.T) {
	taken := map[string]bool{"archive": true}
	got := uniqueName("archive", func(n string) bool { return taken[n] })
	if got != "archive-2" {
		t.Fatalf("uniqueName=%q want archive-2", got)
	}
}

func TestUniqueName_Several(t *testing.T) {
	taken := map[string]bool{"archive": true, "archive-2": true, "archive-3": true}
	got := uniqueName("archive", func(n string) bool { return taken[n] })
	if got != "archive-4" {
		t.Fatalf("uniqueName=%q want archive-4", got)
	}
}

func TestUniqueName_RuneCapTrimming(t *testing.T) {
	base := strings.Repeat("á", maxNameLen) // 64 multibyte runes, already at the cap
	taken := map[string]bool{base: true}
	got := uniqueName(base, func(n string) bool { return taken[n] })
	if !utf8.ValidString(got) {
		t.Fatal("uniqueName produced invalid UTF-8 (sliced mid-rune)")
	}
	if n := utf8.RuneCountInString(got); n != maxNameLen {
		t.Fatalf("rune count=%d want %d", n, maxNameLen)
	}
	if !strings.HasSuffix(got, "-2") {
		t.Fatalf("uniqueName=%q want a -2 suffix", got)
	}
}

// m6: when the rune-cap trim lands right after a space, the space must not
// survive into the rendered suffix ("foo -2") — trim it, same as a trailing
// hyphen ("foo--2") would be.
func TestUniqueName_TrimsTrailingSpaceBeforeSuffix(t *testing.T) {
	// suffix "-2" costs 2 runes, so the first candidate is cut to the first 62
	// runes of base: 61 "a"s followed by the space at index 61 — landing the
	// cut exactly on the space.
	base := strings.Repeat("a", 61) + " " + strings.Repeat("b", 10)
	taken := map[string]bool{base: true}
	got := uniqueName(base, func(n string) bool { return taken[n] })
	want := strings.Repeat("a", 61) + "-2"
	if got != want {
		t.Fatalf("uniqueName=%q want %q (no dangling space before the suffix)", got, want)
	}
}

// --- Spawn-level uniqueness ---

// archiveDir returns a real, existing directory whose basename is "archive"
// (deriveName needs a real cwd to fork/exec into, and a real basename to
// derive the "archive" label from).
func archiveDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSpawn_UniqueNameAmongLiveSessions(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	dir := archiveDir(t)
	a, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(a.ID); _ = m.Kill(b.ID) })
	if a.Info().Name != "archive" || b.Info().Name != "archive-2" {
		t.Fatalf("names = %q, %q; want archive, archive-2", a.Info().Name, b.Info().Name)
	}
}

func TestSpawn_ExitedNamesakeDoesNotCount(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	dir := archiveDir(t)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: dir})
	fresh, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(old.ID); _ = m.Kill(fresh.ID) })
	if fresh.Info().Name != "archive" {
		t.Fatalf("name=%q want archive (exited namesake must not count)", fresh.Info().Name)
	}
}

func TestRestart_KeepsTheSameName(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Name: "api", Cwd: t.TempDir()})
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if ns.Info().Name != "api" {
		t.Fatalf("restarted name=%q want api (the old exited session must not force a suffix)", ns.Info().Name)
	}
}

func TestRename(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "old"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if err := m.Rename(s.ID, "  new  "); err != nil {
		t.Fatal(err)
	}
	if s.Info().Name != "new" {
		t.Fatalf("name=%q want new", s.Info().Name)
	}
	if err := m.Rename(s.ID, "   "); err != ErrInvalidName {
		t.Fatalf("empty rename err=%v want ErrInvalidName", err)
	}
	if err := m.Rename("nope", "x"); err != ErrNotFound {
		t.Fatalf("unknown rename err=%v want ErrNotFound", err)
	}
}

func TestRename_PersistsToSidecar(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "old", Cwd: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	_ = m.Rename(s.ID, "renamed")
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	if sc.all()[s.ID].Name != "renamed" {
		t.Fatalf("sidecar name=%q want renamed", sc.all()[s.ID].Name)
	}
}
