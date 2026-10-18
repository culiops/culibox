package box

import (
	"context"
	"errors"
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
