package session

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// dropsLauncher is a profile that carries everything a box setup can: an AWS,
// a GitHub and a token binding, context paths, an egress policy and a clone. It
// answers the optional resolvers like the real launcher, including
// HostDropsFor. Its Build env is clean, so whatever reaches a host process's
// env came from the session layer.
type dropsLauncher struct {
	tieredLauncher
	hostBlock string   // HostBlockFor's answer
	drops     []string // HostDropsFor's answer
	guardrail []string // HostGuardrailFor's answer
	path      string   // Build's PATH ("" = none, so node is never found)
	extraEnv  []string // more of Build's env, after PATH
	boxOpt    string   // BoxOnlyOptionFor's answer
	bound     bool     // AWS + GitHub + token bindings, context paths, egress
	badConn   bool     // AWSConnectionsFor errors (an undefined connection name)
	clone     *config.Clone
}

func (l dropsLauncher) HostBlockFor(string) (string, error)   { return l.hostBlock, nil }
func (l dropsLauncher) HostDropsFor(string) ([]string, error) { return slices.Clone(l.drops), nil }
func (l dropsLauncher) HostGuardrailFor(string) ([]string, error) {
	return slices.Clone(l.guardrail), nil
}
func (l dropsLauncher) BoxOnlyOptionFor(launcher.SpawnSpec) (string, error) { return l.boxOpt, nil }

func (l dropsLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	argv, env, err := l.tieredLauncher.Build(spec)
	env = slices.Clone(env)
	if l.path != "" {
		env = append(env, "PATH="+l.path)
	}
	return argv, append(env, l.extraEnv...), err
}

func (l dropsLauncher) AWSFor(string) *config.AWSBinding {
	if !l.bound {
		return nil
	}
	return testBinding()
}

func (l dropsLauncher) GitHubFor(string) *config.GitHubBinding {
	if !l.bound {
		return nil
	}
	return testGitHub()
}

func (l dropsLauncher) AWSConnectionsFor(string) ([]config.AWSConnection, error) {
	if l.badConn {
		return nil, errors.New(`profile "p" names AWS connection "gone", which is not defined`)
	}
	return nil, nil
}

func (l dropsLauncher) TokensFor(string) ([]config.StaticToken, []config.StaticToken, error) {
	if !l.bound {
		return nil, nil, nil
	}
	return []config.StaticToken{grafanaEntry()}, []config.StaticToken{patEntry()}, nil
}

func (l dropsLauncher) EgressFor(string) ([]string, bool) {
	if !l.bound {
		return nil, false
	}
	return []string{"example.com"}, false
}

func (l dropsLauncher) ContextPathsFor(string) ([]string, error) {
	if !l.bound {
		return nil, nil
	}
	return []string{"/srv/b/docs"}, nil
}

func (l dropsLauncher) CloneFor(string) (*config.Clone, error) {
	if l.clone == nil {
		return nil, nil
	}
	c := *l.clone
	return &c, nil
}

var _ HostDropsResolver = dropsLauncher{}
var _ HostDropsResolver = (*launcher.AgentLauncher)(nil)

// threeGuard is what a Host run of the bound profile keeps as its default
// credentials (config.HostGuardrail); ctxDrops what it still drops.
var (
	threeGuard = []string{"AWS binding", "GitHub binding", "token binding"}
	ctxDrops   = []string{"context paths"}
	// nodeDropped is HostDropped when node is not on the session PATH: the
	// bindings with the reason (one item, so they are named once), then the drops.
	nodeDropped = []string{nodeMissingItem, "context paths"}
)

const nodeMissingItem = "AWS, GitHub and token bindings (node was not found on this session's PATH; CuliBox's credential helpers need it)"

// The node item names the bindings once, whatever their number.
func TestHostNodeMissingItemNamesBindingsOnce(t *testing.T) {
	const why = " (node was not found on this session's PATH; CuliBox's credential helpers need it)"
	for _, tc := range []struct {
		guard []string
		want  string
	}{
		{[]string{"AWS binding"}, "an AWS binding" + why},
		{[]string{"token binding"}, "a token binding" + why},
		{[]string{"AWS binding", "GitHub binding"}, "AWS and GitHub bindings" + why},
		{threeGuard, nodeMissingItem},
	} {
		if got := hostNodeMissingItem(tc.guard); got != tc.want {
			t.Errorf("hostNodeMissingItem(%q) = %q, want %q", tc.guard, got, tc.want)
		}
	}
}

// boundDropsLauncher's Build env has no PATH, so a Host run finds no node and
// drops the bindings (see hostbroker_test.go for a Host run that keeps them).
func boundDropsLauncher(argv []string, tier string) dropsLauncher {
	return dropsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: tier}, bound: true, drops: ctxDrops, guardrail: threeGuard}
}

type dropsHarness struct {
	m     *Manager
	boxes *fakeBoxes
	hooks *fakeBrokerHooks
	eg    *fakeEgressHooks
	path  string
}

func newDropsHarness(t *testing.T, l dropsLauncher) *dropsHarness {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	h := &dropsHarness{m: m, boxes: &fakeBoxes{}, hooks: &fakeBrokerHooks{}, eg: &fakeEgressHooks{},
		path: filepath.Join(t.TempDir(), "sessions.json")}
	if err := m.SetSidecarPath(h.path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(h.boxes)
	m.SetBroker(h.hooks)
	m.SetEgress(h.eg)
	m.SetEgressAll(true)
	m.SetContextCheck(func(string) (*box.FileID, error) { return nil, nil })
	return h
}

func (h *dropsHarness) untouched(t *testing.T) {
	t.Helper()
	if n, tk, bf := h.hooks.calls(); n+tk+bf != 0 {
		t.Fatalf("broker called: nonce=%d token=%d boxFor=%d", n, tk, bf)
	}
	if h.eg.spawnCalls() != 0 || h.boxes.prepares != 0 || h.boxes.wraps != 0 {
		t.Fatalf("egress=%d prepares=%d wraps=%d", h.eg.spawnCalls(), h.boxes.prepares, h.boxes.wraps)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

// envOfHostProcess is the env names the started process really has: the spawn
// runs `env`, so this reads the process, not the launcher's answer.
func envOfHostProcess(t *testing.T, s *Session) []string {
	t.Helper()
	out, cancel, _ := s.Subscribe()
	defer cancel()
	var b strings.Builder
	deadline := time.After(5 * time.Second)
	for !strings.Contains(b.String(), "ENDOFENV") {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatalf("session ended; output %q", b.String())
			}
			b.WriteString(string(chunk))
		case <-deadline:
			t.Fatalf("no env output; got %q", b.String())
		}
	}
	var names []string
	for _, line := range strings.Split(b.String(), "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			names = append(names, k)
		}
	}
	return names
}

var envDumpArgv = []string{"sh", "-c", "env; echo ENDOFENV; sleep 30"}

func hostSpec(t *testing.T) launcher.SpawnSpec {
	return launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Runtime: config.RuntimeHost}
}

func TestHostSpawnWithDropsNeedsAck(t *testing.T) {
	h := newDropsHarness(t, boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox))
	_, err := h.m.Spawn(hostSpec(t))
	if err == nil {
		t.Fatal("a host spawn of a bound profile without the ack was accepted")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "host_drops_bindings") {
		t.Fatalf("err = %v, want ErrUnboxableWorkspace naming host_drops_bindings", err)
	}
	want := `profile "p" runs on Host with AWS, GitHub and token bindings as the default credentials, not a limit, and without its context paths; confirm to start it there (host_drops_bindings: true)`
	if err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err, want)
	}
	h.untouched(t)

	// Drops alone keep today's wording.
	l := dropsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, drops: ctxDrops}
	h = newDropsHarness(t, l)
	_, err = h.m.Spawn(hostSpec(t))
	want = `profile "p": on the host this profile runs without its context paths; confirm to start it there (host_drops_bindings: true)`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %q", err, want)
	}
	h.untouched(t)
}

// Without node on the session PATH the Host run drops the bindings, as before
// #360, and says why.
func TestHostSpawnWithAckDropsBindings(t *testing.T) {
	h := newDropsHarness(t, boundDropsLauncher(envDumpArgv, config.RuntimeBox))
	spec := hostSpec(t)
	spec.HostDropsAck = true
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })

	names := envOfHostProcess(t, s)
	for _, bad := range []string{brokerTokenEnv, "GH_REPO", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "NO_PROXY", config.ContextPathsEnv} {
		if slices.Contains(names, bad) {
			t.Fatalf("host process has %s in its env: %v", bad, names)
		}
	}
	h.noBoxSideEffects(t)

	in := s.Info()
	if in.AWS != nil || in.GitHub != nil || len(in.Tokens) != 0 || len(in.AWSConnections) != 0 || len(in.ContextPaths) != 0 || in.Egress != "" {
		t.Fatalf("host session carries bindings: %+v", in)
	}
	if s.BrokerNonce != "" || s.EgressNonce != "" || s.Bindings().Any() {
		t.Fatalf("host session holds broker state: nonce=%q egress=%q bindings=%+v", s.BrokerNonce, s.EgressNonce, s.Bindings())
	}
	if !slices.Equal(in.HostDropped, nodeDropped) {
		t.Fatalf("Info.HostDropped = %q, want %q", in.HostDropped, nodeDropped)
	}
	md := sidecarEntry(t, h.path, s.ID)
	if !slices.Equal(md.HostDropped, nodeDropped) {
		t.Fatalf("sidecar host_dropped = %q, want %q", md.HostDropped, nodeDropped)
	}
	if md.AWS != nil || md.GitHub != nil || len(md.Tokens) != 0 || len(md.ContextPaths) != 0 || md.BrokerNonce != "" || md.HostBroker {
		t.Fatalf("sidecar carries bindings: %+v", md)
	}
	if s.HostBroker || in.HostBroker {
		t.Fatal("a Host session without node is marked as having its bindings")
	}
}

// noBoxSideEffects is untouched for a session that exists: the broker, the egress and
// the box layer were never asked for anything.
func (h *dropsHarness) noBoxSideEffects(t *testing.T) {
	t.Helper()
	if n, tk, bf := h.hooks.calls(); n+tk+bf != 0 {
		t.Fatalf("broker called for a host spawn: nonce=%d token=%d boxFor=%d", n, tk, bf)
	}
	if h.eg.spawnCalls() != 0 || h.boxes.prepares != 0 || h.boxes.wraps != 0 {
		t.Fatalf("egress=%d prepares=%d wraps=%d for a host spawn", h.eg.spawnCalls(), h.boxes.prepares, h.boxes.wraps)
	}
}

func TestHostSpawnWithoutDropsHasNoHostDropped(t *testing.T) {
	l := dropsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}}
	h := newDropsHarness(t, l)
	s, err := h.m.Spawn(hostSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.HostDropped != nil || s.Info().HostDropped != nil {
		t.Fatalf("HostDropped = %q, want nil so omitempty hides it", s.Info().HostDropped)
	}
	if md := sidecarEntry(t, h.path, s.ID); md.HostDropped != nil {
		t.Fatalf("sidecar host_dropped = %q, want none", md.HostDropped)
	}
}

// #360: a Host run keeps its bindings, so one that no longer resolves blocks it
// as it blocks a box. The ack refusal still comes first.
func TestHostSpawnResolverErrorsBlockAfterAck(t *testing.T) {
	l := boundDropsLauncher(envDumpArgv, config.RuntimeBox)
	l.badConn = true
	h := newDropsHarness(t, l)

	// Without the ack the refusal is the ack one, not the resolver's.
	_, err := h.m.Spawn(hostSpec(t))
	if err == nil || !strings.Contains(err.Error(), "host_drops_bindings") {
		t.Fatalf("err = %v, want the drops refusal", err)
	}
	spec := hostSpec(t)
	spec.HostDropsAck = true
	// The user chose Host: a profile refusal, never "containment unavailable".
	if s, err := h.m.Spawn(spec); err == nil || errors.Is(err, box.ErrBoxUnavailable) || !errors.Is(err, box.ErrUnboxableWorkspace) ||
		strings.Contains(err.Error(), "containment") || !strings.Contains(err.Error(), `"gone"`) {
		if err == nil {
			_ = h.m.Kill(s.ID)
		}
		t.Fatalf("host spawn err = %v, want the unresolvable connection as a profile refusal", err)
	}
	h.untouched(t)
	// The same profile in a box still fails on the bad connection.
	if _, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), Runtime: config.RuntimeBox}); !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("box spawn err = %v, want ErrBoxUnavailable", err)
	}
}

func TestHostSpawnBoxOnlyLockBeatsAck(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	l.hostBlock = "this profile is set to box only"
	h := newDropsHarness(t, l)
	spec := hostSpec(t)
	spec.HostDropsAck = true
	_, err := h.m.Spawn(spec)
	if err == nil {
		t.Fatal("the box_only lock was lifted by the ack")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "set to box only") {
		t.Fatalf("err = %v, want the lock refusal", err)
	}
	h.untouched(t)
}

func TestHostSpawnPerSpawnBoxOnlyOptionRefused(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	l.boxOpt = `the "Skip the critical-rm prompt" option is on`
	h := newDropsHarness(t, l)
	spec := hostSpec(t)
	spec.HostDropsAck = true
	spec.Options = map[string]any{config.CriticalRmKey: true}
	_, err := h.m.Spawn(spec)
	if err == nil {
		t.Fatal("a per-spawn box-only option ran on the host")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) || err.Error() != `profile "p": `+l.boxOpt {
		t.Fatalf("err = %q, want today's wording", err)
	}
	h.untouched(t)
}

func TestBoxSpawnIgnoresAck(t *testing.T) {
	h := newDropsHarness(t, boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox))
	s, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), HostDropsAck: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.Runtime != config.RuntimeBox || s.AWS == nil || s.GitHub == nil || len(s.Tokens) == 0 || len(s.ContextPaths) == 0 {
		t.Fatalf("a box spawn lost its bindings: runtime=%s %+v", s.Runtime, s.Info())
	}
	if got := envNamed(h.boxes.lastEnv, brokerTokenEnv); len(got) != 1 {
		t.Fatalf("broker token env = %q, want one", got)
	}
	if s.HostDropped != nil || s.Info().HostDropped != nil {
		t.Fatalf("HostDropped = %q on a box session", s.Info().HostDropped)
	}
}

func TestRestartHostSessionOfBoundProfile(t *testing.T) {
	h := newDropsHarness(t, boundDropsLauncher([]string{"sh", "-c", "exit 0"}, config.RuntimeBox))
	spec := hostSpec(t)
	spec.HostDropsAck = true
	old := exitedSession(t, h.m, spec)
	if !slices.Equal(old.HostDropped, nodeDropped) {
		t.Fatalf("old HostDropped = %q", old.HostDropped)
	}
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatalf("a host session of a bound profile did not restart: %v", err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	if ns.Runtime != config.RuntimeHost || !slices.Equal(ns.HostDropped, nodeDropped) || ns.Bindings().Any() {
		t.Fatalf("restarted: runtime=%s dropped=%q bindings=%+v", ns.Runtime, ns.HostDropped, ns.Bindings())
	}
	h.noBoxSideEffects(t)
}

func TestRestartBoxSessionStaysBox(t *testing.T) {
	h := newDropsHarness(t, boundDropsLauncher([]string{"sh", "-c", "exit 0"}, config.RuntimeBox))
	old := exitedSession(t, h.m, launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir()})
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	if ns.Runtime != config.RuntimeBox || ns.AWS == nil || ns.HostDropped != nil || ns.BrokerNonce == "" {
		t.Fatalf("a box restart changed shape: runtime=%s %+v", ns.Runtime, ns.Info())
	}
}

// The sidecar record survives a daemon restart: the host card still says what
// it runs without.
func TestSidecarRestoresHostDropped(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	h := newDropsHarness(t, l)
	fb := newFakeBackend()
	h.m.backend = fb
	spec := hostSpec(t)
	spec.HostDropsAck = true
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(h.path); err != nil {
		t.Fatal(err)
	}
	m2.SetBoxes(&fakeBoxes{})
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not restored")
	}
	if !slices.Equal(r.HostDropped, nodeDropped) || !slices.Equal(r.Info().HostDropped, nodeDropped) {
		t.Fatalf("restored HostDropped = %q", r.Info().HostDropped)
	}
}

func TestHostSpawnCloneProfileInPlace(t *testing.T) {
	of := t.TempDir()
	l := boundDropsLauncher(envDumpArgv, config.RuntimeBox)
	l.clone = &config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone"), Shares: []string{"CLAUDE.md"}}
	l.drops, l.guardrail = nil, []string{"GitHub binding"}
	h := newDropsHarness(t, l)
	h.m.SetClone(&fakeCloneHooks{})
	no := false
	spec := launcher.SpawnSpec{Agent: "p", Cwd: of, Runtime: config.RuntimeHost, Clone: &no}

	if _, err := h.m.Spawn(spec); err == nil || !strings.Contains(err.Error(), "host_drops_bindings") {
		t.Fatalf("without the ack: err = %v, want the drops refusal", err)
	}
	spec.HostDropsAck = true
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.Runtime != config.RuntimeHost || s.Cwd != of || s.CloneOf != "" {
		t.Fatalf("runtime=%s cwd=%q cloneOf=%q, want host in %q with no clone", s.Runtime, s.Cwd, s.CloneOf, of)
	}
	if slices.Contains(envOfHostProcess(t, s), "GH_REPO") {
		t.Fatal("GH_REPO reached a host process")
	}
	h.noBoxSideEffects(t)
}

// A launcher that cannot list drops (no HostDropsResolver) still never hands a
// bound profile to the host: the binding and context backstops refuse it.
func TestHostSpawnBackstopsHoldWithoutDropsResolver(t *testing.T) {
	m, _, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir(), Runtime: config.RuntimeHost, HostDropsAck: true})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a bound profile spawned on the host with an ack and no drops resolver")
	}
	if !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "AWS binding") {
		t.Fatalf("err = %v", err)
	}
	if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
		t.Fatalf("refused spawn called the broker")
	}
}
