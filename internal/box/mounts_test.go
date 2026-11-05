package box

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The bundle a claude session needs, as the bundle provider hands it over: the
// root its flags point into (required) and a config overlay it runs without.
var (
	bundleRoot    = Mount{Host: "/b/h1", Box: "/opt/agent-bundle"}
	bundleSkills  = Mount{Host: "/b/h1/skills", Box: "/home/agent/.claude/skills", Optional: true}
	bundleAgents  = Mount{Host: "/b/h1/agents", Box: "/home/agent/.claude/agents", Optional: true}
	claudeMounts  = []Mount{bundleRoot, bundleSkills}
	errInspectBad = errors.New("docker inspect: daemon unreachable")
)

func hasDst(args []string, dst string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--mount" && strings.Contains(args[i+1], ",dst="+dst) {
			return true
		}
	}
	return false
}

func mountDsts(ms []Mount) []string {
	out := make([]string, 0, len(ms))
	for _, mt := range ms {
		out = append(out, mt.Box)
	}
	return out
}

// THE bug (brain #131): a box created without the bundle — by a bash session,
// or before this fix — is reused by name for claude, whose flags then point
// into a directory the container never mounted ("Settings file not found:
// /opt/agent-bundle/settings.json"). While the bash session runs, the box must
// not be touched, so the spawn is refused with a remedy instead of crashing.
func TestLiveBoxMissingARequiredMountRefusesTheSpawn(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable for a live box without the bundle root, got %v", err)
	}
	if !strings.Contains(err.Error(), "/opt/agent-bundle") || !strings.Contains(err.Error(), name) {
		t.Fatalf("the refusal must name the box and the missing mount: %v", err)
	}
	if f.stops() != 0 || f.runs() != 1 || !f.running[name] {
		t.Fatalf("a box with a live session must never be removed or replaced: stops=%d runs=%d", f.stops(), f.runs())
	}
	m.mu.Lock()
	live := m.active[name]
	m.mu.Unlock()
	if live != 1 {
		t.Fatalf("the refused spawn must not count as a session: active=%d", live)
	}
}

// Idle, the same box is simply recreated with the mounts this spawn needs. Its
// name, and so its home, stay the same.
func TestIdleBoxMissingAMountIsRecreatedWithIt(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	_, _, name2, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if err != nil {
		t.Fatal(err)
	}
	if name2 != name {
		t.Fatalf("recreating must keep the box name (its home is keyed by it): %s != %s", name2, name)
	}
	if f.stops() != 1 || f.runs() != 2 {
		t.Fatalf("want the idle box removed and recreated: stops=%d runs=%d", f.stops(), f.runs())
	}
	last := f.runArgs[len(f.runArgs)-1]
	for _, mt := range claudeMounts {
		if !hasDst(last, mt.Box) {
			t.Fatalf("recreated box lacks %s: %v", mt.Box, last)
		}
	}
	m.mu.Lock()
	_, armed := m.timers[name]
	live := m.active[name]
	m.mu.Unlock()
	if armed || live != 1 {
		t.Fatalf("the recreated box serves the new session: timer armed=%v active=%d", armed, live)
	}
}

// A config overlay that appeared after the box started (the user created
// ~/.claude/agents) is not worth refusing a spawn next to a live session: the
// agent runs without it, exactly as the sessions already in the box do.
func TestLiveBoxMissingOnlyAnOptionalMountIsReused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	if _, _, _, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	more := append(append([]Mount(nil), claudeMounts...), bundleAgents)
	if _, _, _, err := m.Wrap(ctx, dir, "claude-2", BoxKey{}, []string{"claude"}, nil, more); err != nil {
		t.Fatalf("a missing optional overlay must not refuse the spawn: %v", err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("want the live box reused: stops=%d runs=%d", f.stops(), f.runs())
	}
}

// An idle box is reused only if it is exactly what this spawn would create, so
// a mount this spawn does not ask for is dropped too: turning an account's
// personal config off must take it out of the box once nothing uses it.
func TestIdleBoxWithAnExtraMountIsRecreatedWithoutIt(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "claude-2", BoxKey{}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 2 {
		t.Fatalf("want the idle box recreated: stops=%d runs=%d", f.stops(), f.runs())
	}
	if last := f.runArgs[len(f.runArgs)-1]; hasDst(last, bundleRoot.Box) {
		t.Fatalf("the recreated box still mounts the bundle: %v", last)
	}
}

// The ordinary case must stay cheap and untouched: same mounts, same box,
// whichever bin asks — the reverse order of the bug (claude first, then bash
// with the same box mounts) included.
func TestBoxWithExactlyTheRequestedMountsIsReused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "bash-2", BoxKey{}, []string{"bash"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("a matching box is reused, live or idle: stops=%d runs=%d", f.stops(), f.runs())
	}
}

// After a daemon restart nothing in memory knows what a box was created with,
// so the answer comes from docker. A box with the right mounts is reused; one
// from an older build without them, kept alive by an adopted session, refuses.
func TestAdoptedBoxMountsAreReadFromDocker(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ctx := context.Background()
	dir := t.TempDir()
	name := mustBoxName(t, m, dir)
	f.running[name] = true
	f.mounts[name] = append([]string{dir, boxHome}, mountDsts(claudeMounts)...)
	m.AdoptBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "claude-2", BoxKey{}, []string{"claude"}, nil, claudeMounts); err != nil {
		t.Fatalf("an adopted box with the right mounts is reused: %v", err)
	}

	old := t.TempDir()
	oldName := mustBoxName(t, m, old)
	f.running[oldName] = true
	f.mounts[oldName] = []string{old, boxHome} // created by a bash session before this fix
	m.AdoptBox(oldName)
	if _, _, _, err := m.Wrap(ctx, old, "claude-3", BoxKey{}, []string{"claude"}, nil, claudeMounts); !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable for an adopted box without the bundle, got %v", err)
	}
	if f.runs() != 0 || f.stops() != 0 {
		t.Fatalf("adopted boxes must never be replaced under their sessions: runs=%d stops=%d", f.runs(), f.stops())
	}
}

// What a box carries is unknown when docker will not say, and guessing either
// way is wrong (a crash, or a removal), so the spawn is refused and the box left
// alone.
func TestMountsLookupFailureRefusesTheSpawn(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	f.mountsErr = errInspectBad
	if _, _, _, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts); err == nil {
		t.Fatal("a failed mounts lookup must refuse the spawn")
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("the box must be left alone: stops=%d runs=%d", f.stops(), f.runs())
	}
	m.mu.Lock()
	_, armed := m.timers[name]
	m.mu.Unlock()
	if !armed {
		t.Fatal("the idle timer must be re-armed, or the box is never stopped")
	}
}

// Only binds are what runArgs creates; a tmpfs or an image volume is not a
// mount agent-box asked for and must not read as one.
func TestParseMountsKeepsBindDestinationsOnly(t *testing.T) {
	out := "bind|/w\nbind|/home/agent\nvolume|/var/lib/x\ntmpfs|/tmp\n\nbind|/opt/agent-bundle\n"
	got := parseMounts(out)
	want := []string{"/w", "/home/agent", "/opt/agent-bundle"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseMounts = %v, want %v", got, want)
	}
}

// Review round 1: what a shell asks for, the bundle root included, is optional:
// a shell never reads it. A second shell next to a live box without the bundle
// (a box from before the fix, or one started while the bundle failed) must keep
// working exactly as it did before.
func TestShellNextToALiveBoxWithoutTheBundleIsReused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	if _, _, _, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	shell := []Mount{{Host: bundleRoot.Host, Box: bundleRoot.Box, Optional: true}, bundleSkills}
	if _, _, _, err := m.Wrap(ctx, dir, "bash-2", BoxKey{}, []string{"bash"}, nil, shell); err != nil {
		t.Fatalf("a shell must not be refused for a mount it never reads: %v", err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("want the live box reused: stops=%d runs=%d", f.stops(), f.runs())
	}
}

// Review round 1: a spawn whose bundle failed to build asks for no bundle
// mounts, but that is "unknown", not "none". Stripping the bundle from an idle
// box on that basis would make every later claude spawn in it refuse while this
// session lives.
func TestPartialMountsNeverStripAnIdleBox(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "claude-2", BoxKey{PartialMounts: true}, []string{"claude"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("an idle box must not be recreated over mounts the caller could not compute: stops=%d runs=%d", f.stops(), f.runs())
	}
	if n, _ := m.boxName(dir, BoxKey{PartialMounts: true}); n != name {
		t.Fatalf("PartialMounts must not change the box name: %s != %s", n, name)
	}
}

// What a partial request does name is still checked: a bound spawn whose bundle
// failed still needs its broker mounts.
func TestPartialMountsStillRecreateAnIdleBoxMissingOne(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "claude-1", BoxKey{PartialMounts: true}, []string{"claude"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 2 {
		t.Fatalf("a missing mount is still a mismatch: stops=%d runs=%d", f.stops(), f.runs())
	}
}

// Review round 1: removing our own idle box takes a docker call, and DEV and
// PROD share the daemon, so another instance can start an exclusive box over
// the folder in that window. The workspace check runs again before the new box
// starts.
func TestRecreateRechecksTheWorkspaceAfterRemoving(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	real, _ := filepath.EvalSymlinks(dir)
	f.stopHook = func(stopped string) {
		if stopped == name {
			f.mu.Lock()
			f.others = []BoxInfo{{Name: "ab-box-prod1", Workspace: real, State: "running", Instance: "PROD", Exclusive: true, Claims: true}}
			f.mu.Unlock()
		}
	}
	if _, _, _, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts); !errors.Is(err, ErrWorkspaceHeld) {
		t.Fatalf("want ErrWorkspaceHeld once another instance holds the folder, got %v", err)
	}
	if f.runs() != 1 {
		t.Fatalf("no box may start over another instance's exclusive box: runs=%d", f.runs())
	}
}

// A recreated box is a fresh box in every respect but its name: a bound,
// exclusive one gets its egress network, its exclusive label, its verify, and
// the exclusive mark that removes it when its session ends.
func TestRecreatedIdleBoundExclusiveBoxKeepsItsLabels(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	verifies := 0
	m.SetEgress(Egress{Network: "agentbox-egress-TEST", Verify: func(context.Context, string) error { verifies++; return nil }})
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	key := BoxKey{Binding: "abc123abc123", Session: "elev-1"}
	name, err := m.boxName(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	// Left over from before a restart, idle, created without the bundle.
	f.running[name] = true
	f.labels[name] = map[string]string{LabelWorkspace: real, LabelInstance: "TEST", LabelExclusive: "1", LabelClaims: "1"}
	f.mounts[name] = []string{real, boxHome}
	if _, _, _, err := m.Wrap(context.Background(), dir, "elev-1", key, []string{"claude"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 {
		t.Fatalf("want the idle box removed and recreated: stops=%d runs=%d", f.stops(), f.runs())
	}
	args := f.runArgs[0]
	if !containsPair(args, "--label", LabelExclusive+"=1") || !containsPair(args, "--network", "agentbox-egress-TEST") ||
		!hasDst(args, bundleRoot.Box) {
		t.Fatalf("recreated box lost its labels, network or mounts: %q", args)
	}
	if verifies != 1 {
		t.Fatalf("the recreated box must pass the egress check: verifies=%d", verifies)
	}
	m.mu.Lock()
	excl := m.exclusive[name]
	m.mu.Unlock()
	if !excl {
		t.Fatal("the recreated box must be marked exclusive again")
	}
}

// Review Focus 2: a box created before plugins were mounted (or while the
// plugin set was incomplete) has no plugin destinations. The plugin mounts are
// Optional, so a live box is reused and that session simply runs without
// plugins; an idle one is recreated with them.
func TestLiveBoxWithoutPluginMountsIsReused(t *testing.T) {
	plugins := []Mount{
		{Host: "/h/plugins/installed_plugins.json", Box: "/home/agent/.claude/plugins/installed_plugins.json", Optional: true},
		{Host: "/h/plugins/known_marketplaces.json", Box: "/home/agent/.claude/plugins/known_marketplaces.json", Optional: true},
		{Host: "/h/plugins/cache", Box: "/home/agent/.claude/plugins/cache", Optional: true},
		{Host: "/h/plugins/marketplaces", Box: "/home/agent/.claude/plugins/marketplaces", Optional: true},
	}
	withPlugins := append(append([]Mount(nil), claudeMounts...), plugins...)

	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	ctx := context.Background()
	if _, _, _, err := m.Wrap(ctx, dir, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Wrap(ctx, dir, "claude-2", BoxKey{}, []string{"claude"}, nil, withPlugins); err != nil {
		t.Fatalf("a live box without the plugin mounts must be reused, not refused: %v", err)
	}
	if f.stops() != 0 || f.runs() != 1 {
		t.Fatalf("want the live box reused: stops=%d runs=%d", f.stops(), f.runs())
	}

	m2, f2 := newTestManagerWithFake(t)
	dir2 := t.TempDir()
	_, _, name, err := m2.Wrap(ctx, dir2, "claude-1", BoxKey{}, []string{"claude"}, nil, claudeMounts)
	if err != nil {
		t.Fatal(err)
	}
	m2.ReleaseBox(name)
	if _, _, _, err := m2.Wrap(ctx, dir2, "claude-2", BoxKey{}, []string{"claude"}, nil, withPlugins); err != nil {
		t.Fatal(err)
	}
	if f2.stops() != 1 || f2.runs() != 2 {
		t.Fatalf("want the idle box recreated with the plugins: stops=%d runs=%d", f2.stops(), f2.runs())
	}
	last := f2.runArgs[len(f2.runArgs)-1]
	for _, mt := range plugins {
		if !hasDst(last, mt.Box) {
			t.Fatalf("recreated box lacks %s: %v", mt.Box, last)
		}
	}
}

// The out dir (PlaywrightOutBox) is an OPTIONAL mount every box created by this
// build has: a box without it still works, its screenshots just stay inside.

func TestBoxWithTheOutMountAndNothingExtraIsReused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	name := mustBoxName(t, m, dir)
	f.running[name] = true
	f.mounts[name] = []string{dir, boxHome, PlaywrightOutBox}
	m.AdoptBox(name)
	m.ReleaseBox(name) // idle: only an exact match is reused
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 0 || f.runs() != 0 {
		t.Fatalf("a box with the out mount is exactly what a fresh one is: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestIdleBoxWithoutTheOutMountIsRecreated(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	name := mustBoxName(t, m, dir)
	f.running[name] = true
	f.mounts[name] = []string{dir, boxHome} // from before the out dir existed
	m.AdoptBox(name)
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.stops() != 1 || f.runs() != 1 {
		t.Fatalf("want the idle box recreated with the out mount: stops=%d runs=%d", f.stops(), f.runs())
	}
	if last := f.runArgs[len(f.runArgs)-1]; !hasDst(last, PlaywrightOutBox) {
		t.Fatalf("the recreated box lacks the out mount: %v", last)
	}
}

func TestLiveBoxWithoutTheOutMountIsReused(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	dir := t.TempDir()
	name := mustBoxName(t, m, dir)
	f.running[name] = true
	f.mounts[name] = []string{dir, boxHome}
	m.AdoptBox(name) // a live session counts against it
	if _, _, _, err := m.Wrap(context.Background(), dir, "bash-1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatalf("a missing optional out mount must not refuse the spawn: %v", err)
	}
	if f.stops() != 0 || f.runs() != 0 {
		t.Fatalf("a box with a live session is never replaced: stops=%d runs=%d", f.stops(), f.runs())
	}
}

func TestYieldCoveredDropsImportsInsideOtherMounts(t *testing.T) {
	skills := Mount{Host: "/b/skills", Box: "/home/agent/.claude/skills", Optional: true}
	ctx := Mount{Host: "/srv/c", Box: "/c"}
	extras := []Mount{
		skills,
		ctx,
		{Host: "/h/notes.md", Box: "/w/notes.md", Yield: true},
		{Host: "/h/x.md", Box: "/c/x.md", Yield: true},
		{Host: "/h/s.md", Box: "/home/agent/.claude/skills/s/SKILL.md", Yield: true},
		{Host: "/h/a.md", Box: "/home/agent/.claude/context/a.md", Yield: true},
		{Host: "/h/same.md", Box: "/c", Yield: true}, // equality counts as covered
	}
	before := slices.Clone(extras)
	got := yieldCovered("/w", extras)
	want := []Mount{skills, ctx, {Host: "/h/a.md", Box: "/home/agent/.claude/context/a.md", Yield: true}}
	if !slices.EqualFunc(got, want, func(a, b Mount) bool { return a == b }) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !slices.EqualFunc(extras, before, func(a, b Mount) bool { return a == b }) {
		t.Fatalf("the input slice was written: %+v", extras)
	}
}

func TestYieldCoveredYieldMountsDoNotCoverEachOther(t *testing.T) {
	extras := []Mount{
		{Host: "/h/d", Box: "/d", Yield: true},
		{Host: "/h/d/e.md", Box: "/d/e.md", Yield: true},
	}
	if got := yieldCovered("/w", extras); len(got) != 2 {
		t.Fatalf("a yield mount must be checked against non-yield extras only: %+v", got)
	}
}

func TestWrapAppliesYieldAfterCloneAndGuardMounts(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	ws := realTemp(t)
	extras := []Mount{
		bundleRoot,
		{Host: "/h/notes.md", Box: filepath.Join(ws, "notes.md"), Optional: true, Yield: true},
		{Host: "/h/a.md", Box: "/home/agent/.claude/context/a.md", Optional: true, Yield: true},
		{Host: "/h/r.md", Box: "/r/of/.git/r.md", Optional: true, Yield: true}, // under the clone's .git mount
	}
	if _, _, _, err := m.Wrap(context.Background(), ws, "s1", cloneKey(ws, "h"), []string{"bash"}, nil, extras); err != nil {
		t.Fatal(err)
	}
	args := f.runArgs[0]
	if hasDst(args, filepath.Join(ws, "notes.md")) {
		t.Errorf("a yield mount inside the workspace must not be mounted: %q", args)
	}
	if hasDst(args, "/r/of/.git/r.md") {
		t.Errorf("a yield mount inside a clone mount must not be mounted: %q", args)
	}
	if !hasDst(args, "/home/agent/.claude/context/a.md") || !hasDst(args, bundleRoot.Box) {
		t.Errorf("an uncovered yield mount and the bundle must stay: %q", args)
	}
}

// #73: a bound spawn mounts its box's own broker socket directory at a NEW
// in-box path (broker.SockBoxDir). A box created before that change mounts the
// shared broker/sock directory at the old path. reuseMounts compares in-box
// destinations only, so the new path is what keeps a pre-upgrade box from being
// reused silently for a new bound spawn: a live one refuses the spawn and names
// the missing mount, an idle one is recreated.
func TestBoxFromBeforePerBoxSocketsIsNeverReusedSilently(t *testing.T) {
	// The pre-#73 and the current broker.SockBoxDir (internal/broker cannot be
	// imported from here).
	old := Mount{Host: "/s/broker/sock", Box: "/opt/agentbox-sock"}
	curFor := func(m *Manager, dir string) Mount {
		name, err := m.BoxName(dir, BoxKey{})
		if err != nil {
			t.Fatal(err)
		}
		return Mount{Host: BrokerDir(m.opts.StateRoot, name), Box: "/opt/agentbox-broker"}
	}

	t.Run("live", func(t *testing.T) {
		m, f := newTestManagerWithFake(t)
		dir := t.TempDir()
		ctx := context.Background()
		if _, _, _, err := m.Wrap(ctx, dir, "old-1", BoxKey{}, []string{"bash"}, nil, []Mount{old}); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := m.Wrap(ctx, dir, "new-1", BoxKey{}, []string{"bash"}, nil, []Mount{curFor(m, dir)})
		if !errors.Is(err, ErrBoxUnavailable) {
			t.Fatalf("want ErrBoxUnavailable for a live pre-#73 box, got %v", err)
		}
		if !strings.Contains(err.Error(), "/opt/agentbox-broker") {
			t.Fatalf("the refusal must name the missing mount: %v", err)
		}
		if f.stops() != 0 {
			t.Fatalf("a box with a live session must never be removed: stops=%d", f.stops())
		}
	})

	t.Run("idle", func(t *testing.T) {
		m, f := newTestManagerWithFake(t)
		dir := t.TempDir()
		ctx := context.Background()
		_, _, name, err := m.Wrap(ctx, dir, "old-1", BoxKey{}, []string{"bash"}, nil, []Mount{old})
		if err != nil {
			t.Fatal(err)
		}
		m.ReleaseBox(name)
		_, _, name2, err := m.Wrap(ctx, dir, "new-1", BoxKey{}, []string{"bash"}, nil, []Mount{curFor(m, dir)})
		if err != nil {
			t.Fatal(err)
		}
		if name2 != name {
			t.Fatalf("recreating must keep the box name: %s != %s", name2, name)
		}
		if f.stops() != 1 || f.runs() != 2 {
			t.Fatalf("want the idle pre-#73 box removed and recreated: stops=%d runs=%d", f.stops(), f.runs())
		}
		last := f.runArgs[len(f.runArgs)-1]
		if !hasDst(last, curFor(m, dir).Box) || hasDst(last, old.Box) {
			t.Fatalf("recreated box must mount %s and not %s: %v", curFor(m, dir).Box, old.Box, last)
		}
	})
}
