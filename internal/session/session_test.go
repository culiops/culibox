package session

import (
	"errors"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/tmux"
)

type mockTmux struct {
	list    []tmux.Session
	newErr  error
	newArgs struct {
		name, cwd string
		argv, env []string
	}
	killed string
}

func (m *mockTmux) List() ([]tmux.Session, error) { return m.list, nil }
func (m *mockTmux) New(name, cwd string, argv, env []string) error {
	m.newArgs.name, m.newArgs.cwd, m.newArgs.argv, m.newArgs.env = name, cwd, argv, env
	return m.newErr
}
func (m *mockTmux) Kill(name string) error   { m.killed = name; return nil }
func (m *mockTmux) Has(string) (bool, error) { return false, nil }

type mockBuilder struct {
	argv, env []string
	err       error
}

func (b mockBuilder) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return b.argv, b.env, b.err
}

func TestSpawn(t *testing.T) {
	mt := &mockTmux{}
	m := &Manager{
		Tmux:     mt,
		Launcher: mockBuilder{argv: []string{"claude"}, env: []string{"K=V"}},
		NameFn:   func(s launcher.SpawnSpec) string { return "agent-" + s.Agent },
	}
	sess, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Name != "agent-claude" {
		t.Fatalf("name %q", sess.Name)
	}
	if mt.newArgs.name != "agent-claude" || mt.newArgs.cwd != "/w" {
		t.Fatalf("tmux.New got %+v", mt.newArgs)
	}
	if len(mt.newArgs.argv) != 1 || mt.newArgs.argv[0] != "claude" {
		t.Fatalf("argv passed wrong: %v", mt.newArgs.argv)
	}
	if len(mt.newArgs.env) != 1 || mt.newArgs.env[0] != "K=V" {
		t.Fatalf("env not carried through: %v", mt.newArgs.env)
	}
}

func TestSpawn_BuildErrorPropagates(t *testing.T) {
	m := &Manager{
		Tmux:     &mockTmux{},
		Launcher: mockBuilder{err: errors.New("boom")},
		NameFn:   func(launcher.SpawnSpec) string { return "x" },
	}
	if _, err := m.Spawn(launcher.SpawnSpec{}); err == nil {
		t.Fatal("build error should propagate")
	}
}

func TestKill(t *testing.T) {
	mt := &mockTmux{}
	m := &Manager{Tmux: mt}
	if err := m.Kill("foo"); err != nil {
		t.Fatal(err)
	}
	if mt.killed != "foo" {
		t.Fatalf("killed %q", mt.killed)
	}
}

func TestGet(t *testing.T) {
	mt := &mockTmux{list: []tmux.Session{{Name: "a"}, {Name: "b"}}}
	m := &Manager{Tmux: mt}
	if _, ok, _ := m.Get("b"); !ok {
		t.Fatal("b should be found")
	}
	if _, ok, _ := m.Get("z"); ok {
		t.Fatal("z should not be found")
	}
}
