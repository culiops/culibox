package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
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
		m.SetContextCheck(func(p string) (*box.FileID, error) { return nil, check(p) })
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

	want := box.Mount{Host: "/srv/b/docs", Box: "/srv/b/docs", CheckID: true}
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
	want := []box.Mount{backing[0], {Host: "/srv/b/docs", Box: "/srv/b/docs", CheckID: true}, {Host: "/srv/b/notes", Box: "/srv/b/notes", CheckID: true}}
	if !slices.Equal(boxes.lastRO, want) {
		t.Fatalf("Wrap extraRO = %+v, want %+v", boxes.lastRO, want)
	}
}

// Each context mount carries the identity the last check returned (its own
// no-symlink walk), so the box layer can compare it inside the running box.
func TestSpawnContextMountsCarryCheckedIdentity(t *testing.T) {
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs", "/srv/b/notes"), nil)
	calls := 0
	m.SetContextCheck(func(p string) (*box.FileID, error) {
		calls++
		// A different identity per call: the mount must carry the LAST one.
		return &box.FileID{Dev: uint64(len(p)), Ino: uint64(calls)}, nil
	})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := []box.Mount{
		{Host: "/srv/b/docs", Box: "/srv/b/docs", ID: &box.FileID{Dev: 11, Ino: 3}, CheckID: true},
		{Host: "/srv/b/notes", Box: "/srv/b/notes", ID: &box.FileID{Dev: 12, Ino: 4}, CheckID: true},
	}
	if len(boxes.lastRO) != len(want) {
		t.Fatalf("Wrap extraRO = %+v, want %+v", boxes.lastRO, want)
	}
	for i, mt := range boxes.lastRO {
		if mt.Host != want[i].Host || mt.Box != want[i].Box || mt.CheckID != want[i].CheckID || mt.ID == nil || *mt.ID != *want[i].ID {
			t.Fatalf("mount %d = %+v (ID %+v), want %+v (ID %+v)", i, mt, mt.ID, want[i], want[i].ID)
		}
	}
}

// The box layer's mount refusal reaches the user like the other context
// refusals: ErrBoxUnavailable, naming the profile.
func TestSpawnContextMountChangedRefused(t *testing.T) {
	m, boxes := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs"), func(string) error { return nil })
	boxes.wrapErr = fmt.Errorf("context_paths /srv/b/docs: the mounted path is not the checked one: %w", box.ErrContextMountChanged)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawned although Wrap refused the mount")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) || !errors.Is(err, box.ErrContextMountChanged) ||
		!strings.Contains(err.Error(), `profile "ctx": context_paths /srv/b/docs: the mounted path is not the checked one`) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "to run on the host instead") {
		t.Fatalf("err = %v: a context refusal must not suggest the host tier", err)
	}
}

func TestSpawnContextLogNamesProfile(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	m, _ := contextManager(t, testContextLauncher(config.RuntimeBox, "/srv/b/docs"), func(string) error { return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "ctx", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := "session " + s.ID + " (profile ctx): context paths [/srv/b/docs]"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("log = %q, want %q", buf.String(), want)
	}
}

// snapLauncher is a cloneLauncher whose context_paths can be edited after a
// spawn, like a profile edit; the clone source is edited through l.clone.
type snapLauncher struct {
	cloneLauncher
	paths *[]string
}

func (l snapLauncher) ContextPathsFor(agent string) ([]string, error) {
	if agent != "clone" {
		return nil, nil
	}
	return slices.Clone(*l.paths), nil
}

func TestContextPathsSnapshotAtSpawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	cl, dir := testCloneLauncher(t, config.RuntimeBox)
	paths := []string{"/srv/b/docs", "/srv/b/notes"}
	l := snapLauncher{cloneLauncher: cl, paths: &paths}
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetClone(&fakeCloneHooks{})
	m.SetContextCheck(func(string) (*box.FileID, error) { return nil, nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "clone", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	// A profile edit after the spawn does not change the running session.
	paths[0] = "/srv/b/other"
	cl.clone.Of = "/home/u/other-repo"
	in := s.Info()
	if want := []string{"/srv/b/docs", "/srv/b/notes"}; !slices.Equal(in.ContextPaths, want) {
		t.Fatalf("Info.ContextPaths = %v, want the spawn-time %v", in.ContextPaths, want)
	}
	if in.CloneOf != "/home/u/repo" {
		t.Fatalf("Info.CloneOf = %q, want the spawn-time /home/u/repo", in.CloneOf)
	}
	// Info hands out a copy.
	in.ContextPaths[0] = "changed"
	if got := s.Info().ContextPaths[0]; got != "/srv/b/docs" {
		t.Fatalf("Info aliases the session's list: %q", got)
	}
	b, err := json.Marshal(s.Info())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"context_paths":["/srv/b/docs","/srv/b/notes"]`, `"clone_of":"/home/u/repo"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("wire shape = %s, want it to contain %s", b, want)
		}
	}

	// It survives a sidecar round trip, still the spawn-time values.
	md := sidecarEntry(t, path, s.ID)
	if !slices.Equal(md.ContextPaths, []string{"/srv/b/docs", "/srv/b/notes"}) || md.CloneOf != "/home/u/repo" {
		t.Fatalf("sidecar = %v / %q", md.ContextPaths, md.CloneOf)
	}
	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	ri := r.Info()
	if !slices.Equal(ri.ContextPaths, []string{"/srv/b/docs", "/srv/b/notes"}) || ri.CloneOf != "/home/u/repo" {
		t.Fatalf("restored = %v / %q", ri.ContextPaths, ri.CloneOf)
	}
}

// A session that has neither keeps its wire shape, and a sidecar record written
// before the fields existed still loads.
func TestNoContextPathsKeepsWireShape(t *testing.T) {
	var md sessionMeta
	if err := json.Unmarshal([]byte(`{"agent":"claude","cwd":"/w","created":"2026-01-01T00:00:00Z"}`), &md); err != nil {
		t.Fatal(err)
	}
	if md.ContextPaths != nil || md.CloneOf != "" {
		t.Fatalf("old record decoded to %v / %q", md.ContextPaths, md.CloneOf)
	}
	b, err := json.Marshal(Info{ID: "claude-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "context_paths") || strings.Contains(string(b), "clone_of") {
		t.Fatalf("wire shape = %s", b)
	}
}
