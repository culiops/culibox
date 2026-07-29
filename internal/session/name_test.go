package session

import (
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
	s, _ := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "editor", Cwd: "/work/proj"})
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.ID != "bash-1" || s.Info().Name != "editor" {
		t.Fatalf("id=%q name=%q want bash-1/editor", s.ID, s.Info().Name)
	}
}

func TestSpawn_DuplicateLabelsAllowed(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	a, _ := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "dup"})
	b, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "dup"})
	if err != nil {
		t.Fatalf("dup label should be allowed: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill(a.ID); _ = m.Kill(b.ID) })
	if a.ID == b.ID {
		t.Fatal("ids must differ")
	}
	if a.Info().Name != "dup" || b.Info().Name != "dup" {
		t.Fatal("both labels should be dup")
	}
}

func TestRename(t *testing.T) {
	m := NewManager(fakeLauncher{}, newFakeBackend(), 1<<16, time.Minute)
	s, _ := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "old"})
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
	s, _ := m.Spawn(launcher.SpawnSpec{Agent: "bash", Name: "old", Cwd: "/x"})
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	_ = m.Rename(s.ID, "renamed")
	sc, _ := openSidecar(path)
	if sc.all()[s.ID].Name != "renamed" {
		t.Fatalf("sidecar name=%q want renamed", sc.all()[s.ID].Name)
	}
}
