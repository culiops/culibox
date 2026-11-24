package session

import (
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// liveLauncher: "gh" and "gh2" carry the same GitHub binding (so they share a
// box), "plain" carries none; each profile's egress policy can be edited, or the
// profile removed, while sessions run.
type liveLauncher struct {
	ghLauncher
	mu  sync.Mutex
	pol map[string]egressPolicy // a missing name is a removed profile
}

func newLiveLauncher() *liveLauncher {
	return &liveLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox), pol: map[string]egressPolicy{
		"gh": {Allow: []string{"pkg.example"}}, "gh2": {Allow: []string{"pkg.example"}}, "plain": {Allow: []string{"pkg.example"}},
	}}
}

func (l *liveLauncher) GitHubFor(agent string) *config.GitHubBinding {
	if agent == "gh" || agent == "gh2" {
		return testGitHub()
	}
	return nil
}

func (l *liveLauncher) EgressFor(agent string) ([]string, bool) {
	a, o, _ := l.LiveEgressFor(agent)
	return a, o
}

func (l *liveLauncher) LiveEgressFor(agent string) ([]string, bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.pol[agent]
	return slices.Clone(p.Allow), p.Open, ok
}

func (l *liveLauncher) set(agent string, allow []string, open bool) {
	l.mu.Lock()
	l.pol[agent] = egressPolicy{Allow: allow, Open: open}
	l.mu.Unlock()
}

func (l *liveLauncher) remove(agent string) {
	l.mu.Lock()
	delete(l.pol, agent)
	l.mu.Unlock()
}

type policyCall struct{ id, box, summary string }

// liveEgressHooks records PolicyChanged calls and answers a set number of
// closed tunnels.
type liveEgressHooks struct {
	fakeEgressHooks
	pmu    sync.Mutex
	calls  []policyCall
	closed int
}

func (f *liveEgressHooks) PolicyChanged(id, box, summary string) int {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	f.calls = append(f.calls, policyCall{id, box, summary})
	return f.closed
}

func (f *liveEgressHooks) changes() []policyCall {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	return slices.Clone(f.calls)
}

func (f *liveEgressHooks) reset() {
	f.pmu.Lock()
	f.calls = nil
	f.pmu.Unlock()
}

func liveManager(t *testing.T, l *liveLauncher, sidecar string) (*Manager, *fakeBackend, *liveEgressHooks) {
	t.Helper()
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if sidecar != "" {
		if err := m.SetSidecarPath(sidecar); err != nil {
			t.Fatal(err)
		}
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	eg := &liveEgressHooks{}
	m.SetEgress(eg)
	return m, fb, eg
}

func spawnLive(t *testing.T, m *Manager, agent string) *Session {
	t.Helper()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: agent, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return s
}

func assertPolicy(t *testing.T, s *Session, allow []string, open bool) {
	t.Helper()
	a, o := s.EgressPolicy()
	if !reflect.DeepEqual(a, allow) || o != open {
		t.Fatalf("%s live policy = %v open=%v, want %v open=%v", s.Agent, a, o, allow, open)
	}
}

func TestRefreshEgressFollowsTheProfileEdit(t *testing.T) {
	l := newLiveLauncher()
	path := filepath.Join(t.TempDir(), "sessions.json")
	m, _, eg := liveManager(t, l, path)
	m.RefreshEgress()
	s := spawnLive(t, m, "gh")
	spawnHash := s.Bindings().Hash()
	assertPolicy(t, s, []string{"pkg.example"}, false)
	if c := s.Info().EgressChange; c != nil {
		t.Fatalf("change shown before any edit: %+v", c)
	}

	l.set("gh", []string{"pypi.org"}, false)
	m.RefreshEgress()
	assertPolicy(t, s, []string{"pypi.org"}, false)
	// The spawn-time policy, and so the box key and the broker's binding
	// directory, never move.
	if !reflect.DeepEqual(s.EgressAllow, []string{"pkg.example"}) || s.Bindings().Hash() != spawnHash {
		t.Fatalf("spawn-time policy changed: %v %q", s.EgressAllow, s.Bindings().Hash())
	}
	got := eg.changes()
	if len(got) != 1 || got[0] != (policyCall{s.ID, s.Box, "+pypi.org -pkg.example"}) {
		t.Fatalf("PolicyChanged calls = %+v", got)
	}
	in := s.Info()
	if in.Egress != "allowlist" || in.EgressChange == nil {
		t.Fatalf("info = %q %+v", in.Egress, in.EgressChange)
	}
	c := in.EgressChange
	if !reflect.DeepEqual(c.Added, []string{"pypi.org"}) || !reflect.DeepEqual(c.Removed, []string{"pkg.example"}) ||
		c.At.IsZero() || c.Reason != "" || len(c.Held) != 0 {
		t.Fatalf("change = %+v", c)
	}
	md := sidecarEntry(t, path, s.ID)
	if md.EgressLive == nil || !reflect.DeepEqual(md.EgressLive.Allow, []string{"pypi.org"}) || md.EgressLive.Open {
		t.Fatalf("sidecar live policy = %+v", md.EgressLive)
	}
	if !reflect.DeepEqual(md.EgressAllow, []string{"pkg.example"}) {
		t.Fatalf("sidecar spawn policy = %v", md.EgressAllow)
	}

	// The same policy again is no change: no row, nothing closed.
	eg.reset()
	m.RefreshEgress()
	if got := eg.changes(); len(got) != 0 {
		t.Fatalf("a no-op refresh reported %+v", got)
	}

	// Opening it is a change too, and the label follows.
	l.set("gh", []string{"pypi.org"}, true)
	m.RefreshEgress()
	if got := eg.changes(); len(got) != 1 || got[0].summary != "open:on" {
		t.Fatalf("open change = %+v", got)
	}
	if in := s.Info(); in.Egress != "open" || !in.EgressChange.Open {
		t.Fatalf("info after open = %q %+v", in.Egress, in.EgressChange)
	}

	// Back to the spawn-time policy: nothing to show any more.
	l.set("gh", []string{"pkg.example"}, false)
	m.RefreshEgress()
	if c := s.Info().EgressChange; c != nil {
		t.Fatalf("change shown at the spawn policy: %+v", c)
	}
}

func TestRefreshEgressRecordsClosedTunnels(t *testing.T) {
	l := newLiveLauncher()
	m, _, eg := liveManager(t, l, "")
	m.RefreshEgress()
	s := spawnLive(t, m, "gh")
	eg.closed = 2
	l.set("gh", nil, false)
	m.RefreshEgress()
	if c := s.Info().EgressChange; c == nil || c.Closed != 2 || !reflect.DeepEqual(c.Removed, []string{"pkg.example"}) {
		t.Fatalf("change = %+v", c)
	}
}

// A deleted (or renamed) profile, or one that no longer runs in a box, leaves
// only the spawn-time bindings' hosts.
func TestRefreshEgressFailsClosedWhenTheProfileIsGone(t *testing.T) {
	l := newLiveLauncher()
	l.set("gh", []string{"pkg.example"}, true)
	m, _, _ := liveManager(t, l, "")
	m.RefreshEgress()
	s := spawnLive(t, m, "gh")
	l.remove("gh")
	m.RefreshEgress()
	assertPolicy(t, s, nil, false)
	c := s.Info().EgressChange
	if c == nil || c.Reason != "profile_gone" || c.Open || !reflect.DeepEqual(c.Removed, []string{"pkg.example"}) {
		t.Fatalf("change = %+v", c)
	}
	if s.Info().Egress != "allowlist" {
		t.Fatalf("egress = %q", s.Info().Egress)
	}
}

// An unbound session is open and has no policy to follow: an edit never
// touches it.
func TestRefreshEgressLeavesUnboundSessionsAlone(t *testing.T) {
	l := newLiveLauncher()
	m, _, eg := liveManager(t, l, "")
	m.SetEgressAll(true)
	m.RefreshEgress()
	s := spawnLive(t, m, "plain")
	l.set("plain", []string{"pypi.org"}, true)
	m.RefreshEgress()
	l.remove("plain")
	m.RefreshEgress()
	assertPolicy(t, s, nil, false)
	if got := eg.changes(); len(got) != 0 {
		t.Fatalf("unbound session changed: %+v", got)
	}
	if in := s.Info(); in.Egress != "open" || in.EgressChange != nil {
		t.Fatalf("info = %q %+v", in.Egress, in.EgressChange)
	}
}

// Two profiles share a box when their bindings and policy match, and sessions
// in a box can read each other's proxy token, so the box runs the meet of its
// members' policies: a widening is held back while another profile shares the
// box, and a narrowing reaches every session in it.
func TestRefreshEgressAppliesTheMeetOfTheBox(t *testing.T) {
	l := newLiveLauncher()
	m, _, eg := liveManager(t, l, "")
	m.RefreshEgress()
	a := spawnLive(t, m, "gh")
	b := spawnLive(t, m, "gh2")
	if a.Box == "" || a.Box != b.Box {
		t.Fatalf("boxes %q %q: the test needs them shared", a.Box, b.Box)
	}

	l.set("gh", []string{"pkg.example", "pypi.org"}, true)
	m.RefreshEgress()
	assertPolicy(t, a, []string{"pkg.example"}, false)
	assertPolicy(t, b, []string{"pkg.example"}, false)
	if got := eg.changes(); len(got) != 0 {
		t.Fatalf("held widening reported as a change: %+v", got)
	}
	c := a.Info().EgressChange
	if c == nil || c.Reason != "shared_box" || !reflect.DeepEqual(c.Held, []string{"pypi.org"}) || !c.HeldOpen {
		t.Fatalf("held change = %+v", c)
	}
	if c := b.Info().EgressChange; c != nil {
		t.Fatalf("the unedited profile shows %+v", c)
	}

	// Narrowing gh reaches gh2's session too: gh's agent could use its token.
	l.set("gh", nil, false)
	m.RefreshEgress()
	assertPolicy(t, a, nil, false)
	assertPolicy(t, b, nil, false)
	if c := b.Info().EgressChange; c == nil || c.Reason != "shared_box" || !reflect.DeepEqual(c.Held, []string{"pkg.example"}) {
		t.Fatalf("co-tenant change = %+v", c)
	}
	if got := eg.changes(); len(got) != 2 {
		t.Fatalf("narrowing should reach both sessions: %+v", got)
	}

	// Once gh's session leaves, gh2's own policy applies again.
	eg.reset()
	if err := m.Kill(a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "gh2 back on its own policy", func() bool {
		x, _ := b.EgressPolicy()
		return reflect.DeepEqual(x, []string{"pkg.example"})
	})
	if c := b.Info().EgressChange; c != nil {
		t.Fatalf("gh2 still shows %+v", c)
	}
}

// A session spawned into a box whose policy has narrowed joins the narrowed
// policy, and pulls the box's meet down if its own is narrower.
func TestSpawnIntoANarrowedBoxJoinsTheMeet(t *testing.T) {
	l := newLiveLauncher()
	m, _, _ := liveManager(t, l, "")
	m.RefreshEgress()
	a := spawnLive(t, m, "gh")
	l.set("gh", nil, false)
	m.RefreshEgress()
	b := spawnLive(t, m, "gh2") // gh2 still asks for pkg.example
	if a.Box != b.Box {
		t.Fatalf("boxes %q %q", a.Box, b.Box)
	}
	assertPolicy(t, b, nil, false)
	if c := b.Info().EgressChange; c == nil || c.Reason != "shared_box" {
		t.Fatalf("newcomer change = %+v", c)
	}
}

// Before the first profile-set apply nothing is recomputed: at boot the
// launcher may not know the stored profiles yet, and a restore must not fail
// every session closed.
func TestNoBoxRefreshBeforeTheFirstApply(t *testing.T) {
	l := newLiveLauncher()
	m, _, eg := liveManager(t, l, "")
	a := spawnLive(t, m, "gh")
	l.remove("gh")
	b := spawnLive(t, m, "gh2")
	assertPolicy(t, a, []string{"pkg.example"}, false)
	assertPolicy(t, b, []string{"pkg.example"}, false)
	if got := eg.changes(); len(got) != 0 {
		t.Fatalf("changes before the first apply: %+v", got)
	}
}

func TestRestoreKeepsTheLivePolicy(t *testing.T) {
	l := newLiveLauncher()
	path := filepath.Join(t.TempDir(), "sessions.json")
	m, fb, _ := liveManager(t, l, path)
	m.RefreshEgress()
	s := spawnLive(t, m, "gh")
	l.set("gh", []string{"pypi.org"}, false)
	m.RefreshEgress()
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
		t.Fatal("not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	assertPolicy(t, r, []string{"pypi.org"}, false)
	if !reflect.DeepEqual(r.EgressAllow, []string{"pkg.example"}) || r.Bindings().Hash() != s.Bindings().Hash() {
		t.Fatalf("restored spawn-time policy = %v", r.EgressAllow)
	}
	if c := r.Info().EgressChange; c == nil || !reflect.DeepEqual(c.Added, []string{"pypi.org"}) || c.At.IsZero() {
		t.Fatalf("restored change = %+v", c)
	}
}

func TestMeet(t *testing.T) {
	p := func(open bool, allow ...string) egressPolicy { return egressPolicy{Allow: allow, Open: open} }
	cases := []struct {
		name string
		in   []egressPolicy
		want egressPolicy
	}{
		{"one", []egressPolicy{p(false, "a.example")}, p(false, "a.example")},
		{"same", []egressPolicy{p(false, "a.example"), p(false, "a.example")}, p(false, "a.example")},
		{"disjoint", []egressPolicy{p(false, "a.example"), p(false, "b.example")}, p(false)},
		{"open and listed", []egressPolicy{p(true, "x.example"), p(false, "a.example")}, p(false, "a.example")},
		{"both open", []egressPolicy{p(true, "x.example"), p(true)}, p(true, "x.example")},
		{"one open", []egressPolicy{p(true, "x.example")}, p(true, "x.example")},
		{"order and duplicates", []egressPolicy{p(false, "b.example", "a.example"), p(false, "a.example", "b.example", "a.example")}, p(false, "a.example", "b.example")},
		{"exact under wildcard", []egressPolicy{p(false, "*.x.example"), p(false, "api.x.example")}, p(false, "api.x.example")},
		{"wildcard under wildcard", []egressPolicy{p(false, "*.x.example"), p(false, "*.a.x.example")}, p(false, "*.a.x.example")},
		{"apex is not under its wildcard", []egressPolicy{p(false, "*.x.example"), p(false, "x.example")}, p(false)},
		{"none", nil, p(false)},
	}
	for _, c := range cases {
		if got := meet(c.in); !got.equal(c.want) {
			t.Errorf("%s: meet = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// I1: the newcomer is never wider than its box between publication and the
// refresh that follows it. The refresh is held off by holding egressMu.
func TestSpawnIsNeverWiderThanItsBoxBeforeTheRefresh(t *testing.T) {
	l := newLiveLauncher()
	path := filepath.Join(t.TempDir(), "sessions.json")
	m, _, eg := liveManager(t, l, path)
	m.RefreshEgress()
	a := spawnLive(t, m, "gh")
	l.set("gh", nil, false)
	m.RefreshEgress()
	eg.reset()

	m.egressMu.Lock()
	var unlock sync.Once
	release := func() { unlock.Do(m.egressMu.Unlock) }
	t.Cleanup(release) // runs before the Kill cleanups, so a failure here never deadlocks them
	done := make(chan *Session, 1)
	go func() {
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh2", Cwd: t.TempDir()})
		if err != nil {
			t.Error(err)
		}
		done <- s
	}()
	var b *Session
	waitFor(t, "gh2 published", func() bool {
		for _, in := range m.List() {
			if in.Agent == "gh2" {
				b, _ = m.Get(in.ID)
				return b != nil
			}
		}
		return false
	})
	assertPolicy(t, b, nil, false) // the box's policy, not gh2's pkg.example
	if b.Box != a.Box {
		t.Fatalf("boxes %q %q", a.Box, b.Box)
	}
	release()
	if s := <-done; s != nil {
		t.Cleanup(func() { _ = m.Kill(s.ID) })
	}
	if md := sidecarEntry(t, path, b.ID); md.EgressLive == nil || md.EgressLive.Allow != nil || md.EgressLive.Open {
		t.Fatalf("sidecar live policy = %+v, want the box's", md.EgressLive)
	}
	assertPolicy(t, b, nil, false)
	if c := b.Info().EgressChange; c == nil || c.Reason != "shared_box" || !reflect.DeepEqual(c.Held, []string{"pkg.example"}) {
		t.Fatalf("change = %+v", c)
	}
}

// M2: a co-tenant whose agent exits stops holding the box back at once, not
// when the retention prunes it.
func TestExitedCoTenantStopsHoldingTheBox(t *testing.T) {
	l := newLiveLauncher()
	// Ephemeral (no backend): the agent is the daemon's own child, so killing
	// it is an exit, not a detach.
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&liveEgressHooks{})
	m.RefreshEgress()
	a := spawnLive(t, m, "gh")
	b := spawnLive(t, m, "gh2")
	l.set("gh", nil, false)
	m.RefreshEgress()
	assertPolicy(t, b, nil, false)
	if err := a.signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "gh2 back on its own policy", func() bool {
		x, _ := b.EgressPolicy()
		return reflect.DeepEqual(x, []string{"pkg.example"})
	})
	if _, ok := m.Get(a.ID); !ok {
		t.Fatal("the exited session should still be retained")
	}
}
