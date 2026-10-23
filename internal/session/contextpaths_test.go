package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// contextLauncher is a tieredLauncher whose profile "ctx" has context_paths,
// and whose Build env can carry a profile-set AGENTBOX_CONTEXT_PATHS.
type contextLauncher struct {
	tieredLauncher
	paths  []string
	ctxErr error
	env    []string // Build's env; nil = the tieredLauncher default
}

func (l contextLauncher) ContextPathsFor(agent string) ([]string, error) {
	if agent != "ctx" {
		return nil, nil
	}
	if l.ctxErr != nil {
		return nil, l.ctxErr
	}
	return slices.Clone(l.paths), nil
}

func (l contextLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	argv, env, err := l.tieredLauncher.Build(spec)
	if l.env != nil {
		env = slices.Clone(l.env)
	}
	return argv, env, err
}

func contextManager(t *testing.T, l contextLauncher, check func(string) error) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	if check != nil {
		m.SetContextCheck(check)
	}
	return m, boxes
}

func testContextLauncher(runtime string, paths ...string) contextLauncher {
	return contextLauncher{
		tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: runtime},
		paths:          paths,
	}
}

func countEnv(env []string, name string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			n++
		}
	}
	return n
}

func TestSpawnContextPathsMounts(t *testing.T) {
	paths := []string{"/srv/b/docs"}
	var checked []string
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, paths...), func(p string) error {
		checked = append(checked, p)
		return nil
	})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	want := box.Mount{Host: "/srv/b/docs", Box: "/srv/b/docs"}
	if !slices.Contains(boxes.lastRO, want) {
		t.Fatalf("Wrap extraRO = %+v, want it to hold %+v", boxes.lastRO, want)
	}
	if want := []string{"/srv/b/docs", "/srv/b/docs"}; !slices.Equal(checked, want) { // early and before Wrap
		t.Fatalf("the check ran on %v, want %v", checked, want)
	}
	if boxes.lastKey.Context != config.ContextHash(paths) {
		t.Fatalf("key.Context = %q, want %q", boxes.lastKey.Context, config.ContextHash(paths))
	}
	if !slices.Contains(boxes.lastEnv, "AGENTBOX_CONTEXT_PATHS=/srv/b/docs") {
		t.Fatalf("env = %v, want AGENTBOX_CONTEXT_PATHS=/srv/b/docs", boxes.lastEnv)
	}
}

func TestSpawnContextPathsHostRefused(t *testing.T) {
	for _, c := range []struct{ name, tier, override string }{
		{"override", config.RuntimeBox, config.RuntimeHost},
		{"profile tier", config.RuntimeHost, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, boxes := contextManager(t, testContextLauncher(c.tier, "/srv/b/docs"), func(string) error { return nil })
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir(), Runtime: c.override})
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a context_paths profile spawned on the host tier")
			}
			if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), `needs runtime "box"`) {
				t.Fatalf("err = %v", err)
			}
			if w, _, _ := boxes.counts(); w != 0 {
				t.Fatalf("Wrap ran %d times for a refused spawn", w)
			}
		})
	}
}

func TestSpawnContextPathMissingRefused(t *testing.T) {
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs"), func(p string) error {
		return errors.New("context_paths " + p + ": does not exist")
	})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a missing context path spawned")
	}
	if !strings.Contains(err.Error(), "context_paths /srv/b/docs: does not exist") {
		t.Fatalf("err = %v, want the check's message naming the path", err)
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to reach the UI as ErrBoxUnavailable", err)
	}
	if w, _, _ := boxes.counts(); w != 0 {
		t.Fatalf("Wrap ran %d times before the check refused", w)
	}
}

// Without a check func (macOS before wiring, tests) a context spawn must fail
// closed rather than mount unchecked paths.
func TestSpawnContextPathsWithoutCheckRefused(t *testing.T) {
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs"), nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("context_paths spawned with no check installed")
	}
	if w, _, _ := boxes.counts(); w != 0 {
		t.Fatalf("Wrap ran %d times", w)
	}
}

func TestSpawnContextPathsInvalidStoredListRefused(t *testing.T) {
	l := testContextLauncher(config.RuntimeBox)
	l.ctxErr = errors.New(`context_paths "rel": must be an absolute path`)
	m, boxes := contextManager(t, l, func(string) error { return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("an invalid stored list spawned")
	}
	if !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("err = %v", err)
	}
	if w, _, _ := boxes.counts(); w != 0 {
		t.Fatalf("Wrap ran %d times", w)
	}
}

func TestSpawnContextEnvNotOverridable(t *testing.T) {
	l := testContextLauncher(config.RuntimeBox, "/srv/b/docs")
	l.env = []string{"TERM=xterm", "AGENTBOX_CONTEXT_PATHS=/etc"}
	m, boxes := contextManager(t, l, func(string) error { return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if n := countEnv(boxes.lastEnv, "AGENTBOX_CONTEXT_PATHS"); n != 1 {
		t.Fatalf("AGENTBOX_CONTEXT_PATHS appears %d times in %v, want once", n, boxes.lastEnv)
	}
	if !slices.Contains(boxes.lastEnv, "AGENTBOX_CONTEXT_PATHS=/srv/b/docs") {
		t.Fatalf("env = %v, want only agent-box's value", boxes.lastEnv)
	}
}

// A profile without context_paths must not carry the variable, even when its
// own env tries to set one: the name is agent-box's alone.
func TestSpawnNoContextStripsEnvAndKeepsKey(t *testing.T) {
	l := testContextLauncher(config.RuntimeBox)
	l.env = []string{"TERM=xterm", "AGENTBOX_CONTEXT_PATHS=/etc"}
	m, boxes := contextManager(t, l, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if n := countEnv(boxes.lastEnv, "AGENTBOX_CONTEXT_PATHS"); n != 0 {
		t.Fatalf("env = %v, want the variable stripped", boxes.lastEnv)
	}
	if boxes.lastKey.Context != "" {
		t.Fatalf("key.Context = %q for a profile without context_paths", boxes.lastKey.Context)
	}
	if len(boxes.lastRO) != 0 {
		t.Fatalf("extraRO = %+v, want none", boxes.lastRO)
	}
}

// The check runs once before Prepare and once right before Wrap. A path that
// passes the first and fails the second is refused, and no box is started.
func TestSpawnContextPathsRecheckedBeforeWrap(t *testing.T) {
	calls := map[string]int{}
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs", "/srv/b/notes"), func(p string) error {
		calls[p]++
		if calls[p] > 1 {
			return errors.New("context_paths " + p + ": is a symlink; list the real path")
		}
		return nil
	})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a path that failed the second check spawned")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "context_paths /srv/b/docs: is a symlink") {
		t.Fatalf("err = %v, want the check's message in the early check's shape", err)
	}
	if w, _, _ := boxes.counts(); w != 0 {
		t.Fatalf("Wrap ran %d times for a refused spawn", w)
	}
	if calls["/srv/b/docs"] != 2 {
		t.Fatalf("the check ran %d times on the first path, want 2 (early and before Wrap)", calls["/srv/b/docs"])
	}
}

func TestSpawnContextPathOverlapsWorkspaceRefused(t *testing.T) {
	ws := t.TempDir()
	for _, c := range []struct{ name, path string }{
		{"equal", ws},
		{"parent", filepath.Dir(ws)},
		{"child", filepath.Join(ws, "docs")},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, c.path), func(string) error { return nil })
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: ws})
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a context path overlapping the workspace spawned")
			}
			want := "context_paths " + c.path + " overlaps the session workspace " + ws + "; the workspace is already mounted"
			if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to contain %q", err, want)
			}
			if w, _, _ := boxes.counts(); w != 0 {
				t.Fatalf("Wrap ran %d times", w)
			}
		})
	}
}

// A sibling that merely shares a name prefix is not an overlap.
func TestSpawnContextPathSiblingPrefixAllowed(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "proj")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := contextManager(t, testContextLauncher(config.RuntimeBox, ws+"-docs"), func(string) error { return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: ws})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Kill(s.ID)
}

// The bundle hands over a slice with spare capacity; appending the context
// mounts must copy, never write into its backing array.
func TestSpawnContextMountsDoNotWriteIntoBundleArray(t *testing.T) {
	backing := make([]box.Mount, 1, 8)
	backing[0] = box.Mount{Host: "/b/skills", Box: "/home/agent/.claude/skills"}
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs", "/srv/b/notes"), func(string) error { return nil })
	m.SetBundles(&fakeBundles{mounts: backing})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	for i, mt := range backing[:cap(backing)][1:] {
		if mt != (box.Mount{}) {
			t.Fatalf("the bundle's backing array was written at %d: %+v", i+1, mt)
		}
	}
	want := []box.Mount{backing[0], {Host: "/srv/b/docs", Box: "/srv/b/docs"}, {Host: "/srv/b/notes", Box: "/srv/b/notes"}}
	if !slices.Equal(boxes.lastRO, want) {
		t.Fatalf("Wrap extraRO = %+v, want %+v", boxes.lastRO, want)
	}
}
