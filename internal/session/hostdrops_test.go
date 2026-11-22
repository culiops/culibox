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
	boxOpt    string   // BoxOnlyOptionFor's answer
	bound     bool     // AWS + GitHub + token bindings, context paths, egress
	badConn   bool     // AWSConnectionsFor errors (an undefined connection name)
	clone     *config.Clone
}

func (l dropsLauncher) HostBlockFor(string) (string, error)                 { return l.hostBlock, nil }
func (l dropsLauncher) HostDropsFor(string) ([]string, error)               { return slices.Clone(l.drops), nil }
func (l dropsLauncher) BoxOnlyOptionFor(launcher.SpawnSpec) (string, error) { return l.boxOpt, nil }

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

var fourDrops = []string{"AWS binding", "GitHub binding", "token binding", "context paths"}

func boundDropsLauncher(argv []string, tier string) dropsLauncher {
	return dropsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: tier}, bound: true, drops: fourDrops}
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
	want := `profile "p": on the host this profile runs without its AWS binding, GitHub binding, token binding and context paths; confirm to start it there (host_drops_bindings: true)`
	if err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err, want)
	}
	h.untouched(t)
}

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
	if !slices.Equal(in.HostDropped, fourDrops) {
		t.Fatalf("Info.HostDropped = %q, want %q", in.HostDropped, fourDrops)
	}
	md := sidecarEntry(t, h.path, s.ID)
	if !slices.Equal(md.HostDropped, fourDrops) {
		t.Fatalf("sidecar host_dropped = %q, want %q", md.HostDropped, fourDrops)
	}
	if md.AWS != nil || md.GitHub != nil || len(md.Tokens) != 0 || len(md.ContextPaths) != 0 || md.BrokerNonce != "" {
		t.Fatalf("sidecar carries bindings: %+v", md)
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

func TestHostSpawnAckSkipsResolverErrors(t *testing.T) {
	l := boundDropsLauncher(envDumpArgv, config.RuntimeBox)
	l.badConn = true
	h := newDropsHarness(t, l)

	// Without the ack the refusal is the drops one, not the resolver's.
	_, err := h.m.Spawn(hostSpec(t))
	if err == nil || !strings.Contains(err.Error(), "host_drops_bindings") {
		t.Fatalf("err = %v, want the drops refusal", err)
	}
	spec := hostSpec(t)
	spec.HostDropsAck = true
	s, err := h.m.Spawn(spec)
	if err != nil {
		t.Fatalf("an unresolvable binding must not block a host run that drops it: %v", err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
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
	if !slices.Equal(old.HostDropped, fourDrops) {
		t.Fatalf("old HostDropped = %q", old.HostDropped)
	}
	ns, err := h.m.Restart(old.ID)
	if err != nil {
		t.Fatalf("a host session of a bound profile did not restart: %v", err)
	}
	t.Cleanup(func() { _ = h.m.Kill(ns.ID) })
	if ns.Runtime != config.RuntimeHost || !slices.Equal(ns.HostDropped, fourDrops) || ns.Bindings().Any() {
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
	if !slices.Equal(r.HostDropped, fourDrops) || !slices.Equal(r.Info().HostDropped, fourDrops) {
		t.Fatalf("restored HostDropped = %q", r.Info().HostDropped)
	}
}

func TestHostSpawnCloneProfileInPlace(t *testing.T) {
	of := t.TempDir()
	l := boundDropsLauncher(envDumpArgv, config.RuntimeBox)
	l.clone = &config.Clone{Of: of, Dir: filepath.Join(t.TempDir(), "clone"), Shares: []string{"CLAUDE.md"}}
	l.drops = []string{"GitHub binding"}
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
