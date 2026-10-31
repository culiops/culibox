package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/bundle"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// tieredLauncher is a builder that also answers RuntimeFor, like the real one.
type tieredLauncher struct {
	argv    []string
	runtime string
}

func (l tieredLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return l.argv, []string{"TERM=xterm"}, nil
}

func (l tieredLauncher) BinFor(launcher.SpawnSpec) (string, error) { return "claude", nil }

func (l tieredLauncher) RuntimeFor(spec launcher.SpawnSpec) (string, error) {
	if spec.Runtime != "" {
		return spec.Runtime, nil
	}
	return l.runtime, nil
}

type fakeBoxes struct {
	mu       sync.Mutex
	wraps    int
	lastAcct string // account the last Wrap was called with
	lastKey  box.BoxKey
	lastEnv  []string
	lastArgv []string
	lastRO   []box.Mount
	releases int
	adopts   int
	kills    int
	adopted  []string
	marked   []string // MarkExclusive calls

	wrapErr    error // returned by Wrap when set
	prepares   int
	prepareErr error
	order      *callLog // optional: shared with fakeBundles to pin call order
}

// callLog records the order of calls across fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) add(s string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s)
}

func (c *callLog) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (f *fakeBoxes) Prepare(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	f.order.add("prepare")
	return f.prepareErr
}

func (f *fakeBoxes) Wrap(_ context.Context, _ string, _ string, key box.BoxKey, argv, env []string, extraRO []box.Mount) ([]string, []string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wraps++
	f.lastAcct = key.Account
	f.lastKey = key
	f.lastEnv = append([]string(nil), env...)
	f.lastArgv = append([]string(nil), argv...)
	f.lastRO = append([]box.Mount(nil), extraRO...)
	f.order.add("wrap")
	if f.wrapErr != nil {
		return nil, nil, "", f.wrapErr
	}
	// /usr/bin/env, not docker: it exists on Linux and macOS alike and really
	// runs argv, so the kill/exit tests drive a live agent instead of a
	// `docker exec` that fails at once.
	return append([]string{"/usr/bin/env"}, argv...), env, "ab-box-test", nil
}

func (f *fakeBoxes) AdoptBox(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adopts++
	f.adopted = append(f.adopted, name)
}

func (f *fakeBoxes) MarkExclusive(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, name)
}

func (f *fakeBoxes) ReleaseBox(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
}

func (f *fakeBoxes) KillSession(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills++
	return nil
}

func (f *fakeBoxes) Runtime() string { return "runc" }

func (f *fakeBoxes) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wraps, f.releases, f.adopts
}

func (f *fakeBoxes) adoptedBox(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.adopted {
		if a == name {
			return true
		}
	}
	return false
}

func (f *fakeBoxes) killCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kills
}

func managerWithBoxes(t *testing.T, argv []string) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(tieredLauncher{argv: argv, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	b := &fakeBoxes{}
	m.SetBoxes(b)
	return m, b
}

func TestSpawnWithHostRuntimeSkipsTheBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if wraps, _, _ := boxes.counts(); wraps != 0 {
		t.Fatalf("trusted-host spawn must not touch the box layer, got %d wraps", wraps)
	}
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("session runtime = %q, want host", s.Runtime)
	}
}

func TestSpawnWithBoxRuntimeWrapsArgvOnce(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if wraps, _, _ := boxes.counts(); wraps != 1 {
		t.Fatalf("boxed spawn must wrap argv exactly once, got %d", wraps)
	}
	if s.Info().Runtime != config.RuntimeBox {
		t.Fatalf("Info().Runtime = %q, want box", s.Info().Runtime)
	}
}

// Without a box layer installed, containment is never assumed.
func TestSpawnWithoutBoxesFallsBackToHost(t *testing.T) {
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if s.Runtime != config.RuntimeHost {
		t.Fatalf("runtime = %q, want host when no box layer is installed", s.Runtime)
	}
}

// A session that asks for box on an instance without a box layer is refused,
// and the message names the way out (the editor's tier, or the request's runtime).
func TestSpawnAskingBoxWithoutBoxesIsRefusedActionably(t *testing.T) {
	m := NewManager(tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}, nil, 1<<16, time.Minute)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	want := `this session asks for runtime box, and this instance has no box layer (see the boot log); set the profile's tier to host (or send runtime "host") or fix containment`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("message = %q, want it to contain %q", err.Error(), want)
	}
}

func TestKilledBoxedSessionReleasesItsBoxExactlyOnce(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, releases, _ := boxes.counts(); releases == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, releases, _ := boxes.counts(); releases != 1 {
		t.Fatalf("killed boxed session released its box %d times, want exactly 1", releases)
	}
}

// An agent that exits on its own must release too, or the box lingers past its
// TTL forever.
func TestExitedBoxedSessionReleasesItsBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "exit 0"})
	if _, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, releases, _ := boxes.counts(); releases == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, releases, _ := boxes.counts()
	t.Fatalf("exited boxed session released %d times, want 1", releases)
}

// Signals do not cross `docker exec`. Killing only the host-side client would
// leave the agent running inside a box other sessions keep alive — the same
// orphan class as the abduco attach clients that once pegged a core.
func TestKillingABoxedSessionAlsoKillsTheAgentInsideTheBox(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if got := boxes.killCount(); got != 1 {
		t.Fatalf("in-box kill called %d times, want exactly 1", got)
	}
}

// A host-tier session has no box to reach into.
func TestKillingAHostSessionDoesNotTouchTheBoxLayer(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if got := boxes.killCount(); got != 0 {
		t.Fatalf("host session triggered %d in-box kills, want 0", got)
	}
}

// fakeBundles records what the session asked for.
type fakeBundles struct {
	order    *callLog // optional: shared with fakeBoxes to pin call order
	askedBin string
	calls    int
	// withImports is the switch of the last For call.
	withImports bool
	// imports is the Grant.Imports it answers.
	imports int
	mounts  []box.Mount
	flags   []string
	drops   []bundle.Drop
	err     error // returned, with no mounts or flags, when set

	// SessionMCP: what it was asked, and what it answers (a zero smcp answers a
	// plain document whose drops are the ones it was given).
	smcpCalls       int
	smcpIncludeUser bool
	smcpDrops       []bundle.Drop
	smcpEntries     []bundle.EntryServer
	smcp            bundle.SessionMCP
	smcpErr         error
	mountKeys       []string // SessionMCPMount calls: the binding key
	writeKeys       []string // WriteSessionMCP calls: "<binding key>/<key>"
	writeDocs       []string
	writeErr        error
}

func (f *fakeBundles) SessionMCPMount(bindingKey string) (box.Mount, error) {
	f.mountKeys = append(f.mountKeys, bindingKey)
	return box.Mount{Host: "/b/session-mcp/" + bindingKey, Box: bundle.BoxSessionMCPDir}, nil
}

func (f *fakeBundles) WriteSessionMCP(bindingKey, key string, doc []byte) (string, error) {
	if f.writeErr != nil {
		return "", f.writeErr
	}
	f.writeKeys = append(f.writeKeys, bindingKey+"/"+key)
	f.writeDocs = append(f.writeDocs, string(doc))
	return bundle.BoxSessionMCPDir + "/mcp-" + key + ".json", nil
}

func (f *fakeBundles) SessionMCP(includeUser bool, drops []bundle.Drop, entries []bundle.EntryServer) (bundle.SessionMCP, error) {
	f.smcpCalls++
	f.smcpIncludeUser, f.smcpDrops, f.smcpEntries = includeUser, drops, entries
	if f.smcpErr != nil {
		return bundle.SessionMCP{}, f.smcpErr
	}
	if f.smcp.Key != "" {
		return f.smcp, nil
	}
	key := "deadbeef-i"
	if includeUser {
		key = "deadbeef-u"
	}
	return bundle.SessionMCP{Doc: []byte(`{"mcpServers":{}}`), Key: key, Drops: drops}, nil
}

func (f *fakeBundles) For(bin string, withImports bool) (bundle.Grant, error) {
	f.askedBin = bin
	f.withImports = withImports
	f.calls++
	if f.err != nil {
		f.order.add("bundle")
		return bundle.Grant{}, f.err
	}
	f.order.add("bundle")
	return bundle.Grant{Mounts: f.mounts, Flags: f.flags, Hash: "deadbeef", Drops: f.drops, Imports: f.imports}, nil
}

// The bundle's flags must reach the agent, and its mounts must reach the box.
func TestBoxedSpawnAppliesTheConfigBundle(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	bundles := &fakeBundles{
		mounts: []box.Mount{{Host: "/b/skills", Box: "/home/agent/.claude/skills"}},
		flags:  []string{"--strict-mcp-config"},
	}
	m.SetBundles(bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.askedBin == "" {
		t.Fatal("the session never asked the bundle layer which layout applies")
	}
	if wraps, _, _ := boxes.counts(); wraps != 1 {
		t.Fatalf("expected one wrap, got %d", wraps)
	}
}

// unresolvedBinLauncher cannot name the agent binary.
type unresolvedBinLauncher struct{ tieredLauncher }

func (unresolvedBinLauncher) BinFor(launcher.SpawnSpec) (string, error) {
	return "", errors.New("no such profile bin")
}

// The bundle's mounts belong to the box, not to the agent (brain #131): a box is
// shared by every session with its key, whatever bin each runs, so the bundle is
// consulted even when the bin is unknown, and its mounts reach Wrap.
func TestBoxedSpawnWithAnUnknownBinStillGetsTheBoxMounts(t *testing.T) {
	m := NewManager(unresolvedBinLauncher{tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}},
		nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	want := box.Mount{Host: "/b", Box: "/opt/agent-bundle"}
	bundles := &fakeBundles{mounts: []box.Mount{want}}
	m.SetBundles(bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.calls != 1 {
		t.Fatalf("bundle consulted %d times, want 1", bundles.calls)
	}
	boxes.mu.Lock()
	ro := boxes.lastRO
	boxes.mu.Unlock()
	if len(ro) != 1 || ro[0] != want {
		t.Fatalf("Wrap got mounts %+v, want the bundle's %+v", ro, want)
	}
}

// Review round 1: a bundle that failed to build yields no mounts, which means
// "unknown", not "none". The box layer must be told, or it strips the bundle
// from an idle box it could have reused.
func TestBundleFailureMarksTheMountsPartial(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{{"bundle ok", nil, false}, {"bundle failed", errors.New("walking ~/.claude: permission denied"), true}} {
		t.Run(c.name, func(t *testing.T) {
			m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
			m.SetBundles(&fakeBundles{err: c.err, mounts: []box.Mount{{Host: "/b", Box: "/opt/agent-bundle"}}})
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Kill(s.ID)
			boxes.mu.Lock()
			got := boxes.lastKey.PartialMounts
			boxes.mu.Unlock()
			if got != c.want {
				t.Fatalf("PartialMounts = %v, want %v", got, c.want)
			}
		})
	}
}

// A host-tier session gets no bundle: there is no box to mount anything into.
func TestHostSpawnDoesNotConsultTheBundle(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	bundles := &fakeBundles{}
	m.SetBundles(bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.askedBin != "" {
		t.Fatalf("host tier must not consult the bundle layer, asked for %q", bundles.askedBin)
	}
}

// Prepare runs before the bundle is built and before Wrap, and a Prepare
// failure stops both: the bundle's MCP probe needs the daemon Prepare readies.
func TestBoxedSpawnPreparesBeforeBundleAndWrap(t *testing.T) {
	order := &callLog{}
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.order = order
	m.SetBundles(&fakeBundles{order: order})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got, want := order.got(), []string{"prepare", "bundle", "wrap"}; len(got) != 3 ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("call order = %v, want %v", got, want)
	}

	order = &callLog{}
	m2, boxes2 := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes2.order = order
	boxes2.prepareErr = fmt.Errorf("%w: box VM not set up", box.ErrBoxUnavailable)
	bundles := &fakeBundles{order: order}
	m2.SetBundles(bundles)
	if _, err := m2.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox}); !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if got := order.got(); len(got) != 1 || got[0] != "prepare" {
		t.Fatalf("a failed Prepare must stop the bundle and Wrap, calls = %v", got)
	}
}

// A host-tier spawn never prepares the box layer.
func TestHostSpawnDoesNotPrepare(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if boxes.prepares != 0 {
		t.Fatalf("host tier must not Prepare, got %d", boxes.prepares)
	}
}

// A containment-check failure from Wrap reaches the user exactly as the box
// layer worded it: no session prefix and no hint to run on the host instead.
func TestBoxUnavailableFromWrapPassesThroughUnwrapped(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.wrapErr = fmt.Errorf("%w: new box reached the host loopback", box.ErrBoxUnavailable)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if err.Error() != boxes.wrapErr.Error() {
		t.Fatalf("error = %q, want it passed through unchanged (%q)", err, boxes.wrapErr)
	}
	if strings.Contains(err.Error(), "spawn with runtime") {
		t.Fatalf("must not suggest the host tier: %v", err)
	}
}

// The sweep judges a box by what the registry says is still running: a boxed
// session counts until it exits, a host session never does.
func TestLiveBoxCountsCountsOnlyUnexitedBoxedSessions(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	a, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(a.ID)
	b, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(h.ID)

	if got := m.LiveBoxCounts(); got["ab-box-test"] != 2 || len(got) != 1 {
		t.Fatalf("want {ab-box-test:2}, got %v", got)
	}
	if err := m.Kill(b.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("killed session never exited")
	}
	if got := m.LiveBoxCounts(); got["ab-box-test"] != 1 || len(got) != 1 {
		t.Fatalf("want {ab-box-test:1} after one exits, got %v", got)
	}
}

// A grant's drops are what the box's config bundle left out; Info carries them
// in order so the UI can say so.
func TestBoxedSpawnInfoCarriesBundleDrops(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	want := []bundle.Drop{
		{Kind: "mcp", Name: "github", Reason: "carries a credential"},
		{Kind: "plugins", Name: "*", Reason: bundle.ReasonPluginsIncomplete},
	}
	m.SetBundles(&fakeBundles{drops: want})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := s.Info().BundleDrops; !slices.Equal(got, want) {
		t.Fatalf("Info.BundleDrops = %+v, want %+v", got, want)
	}
	// The listing's copy must not alias the session's own.
	s.Info().BundleDrops[0].Name = "mutated"
	if got := s.Info().BundleDrops[0].Name; got != "github" {
		t.Fatalf("Info shares the session's drop slice: name now %q", got)
	}
}

// A bundle that fails to build is one drop row, not silence, and the error
// text (which can carry host paths) stays in the log line.
func TestBundleFailureIsOneDrop(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	m.SetBundles(&fakeBundles{err: errors.New("read /home/claude/.claude/secret-path: boom")})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	in := s.Info()
	want := []bundle.Drop{{Kind: "bundle", Name: "*", Reason: bundle.ReasonBundleFailed}}
	if !slices.Equal(in.BundleDrops, want) {
		t.Fatalf("BundleDrops = %+v, want %+v", in.BundleDrops, want)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-path") || strings.Contains(string(raw), "boom") {
		t.Fatalf("the bundle error text reached Info: %s", raw)
	}
}

// A host session has no bundle and so nothing to drop, even if a bundle layer
// is installed.
func TestHostSpawnHasNoBundleDrops(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	m.SetBundles(&fakeBundles{drops: []bundle.Drop{{Kind: "mcp", Name: "x", Reason: "y"}}})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := s.Info().BundleDrops; len(got) != 0 {
		t.Fatalf("host session has BundleDrops %+v", got)
	}
	raw, _ := json.Marshal(s.Info())
	if strings.Contains(string(raw), "bundle_drops") {
		t.Fatalf("host session wire shape carries bundle_drops: %s", raw)
	}
}

// A non-default account that did not opt in skips the bundle on purpose. That
// is a policy, not a drop: nothing was consulted, so nothing is recorded.
func TestSkippedBundleRecordsNoDrops(t *testing.T) {
	l := acctLauncher{
		tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		acct:           workAccount(false),
	}
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	m.SetBundles(&fakeBundles{drops: []bundle.Drop{{Kind: "mcp", Name: "x", Reason: "y"}}})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if got := s.Info().BundleDrops; len(got) != 0 {
		t.Fatalf("skipped bundle recorded drops %+v", got)
	}
}

// Review focus 5: the drops ride the sidecar, as egress does, so a daemon
// restart does not make a box look as if it kept everything.
func TestBundleDropsSurviveRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	l := tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	want := []bundle.Drop{
		{Kind: "settings", Name: "apiKeyHelper", Reason: "runs a host command"},
		{Kind: "plugins", Name: "cache", Reason: bundle.ReasonPluginSymlink},
	}
	m.SetBundles(&fakeBundles{drops: want})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir(), Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if md := sidecarEntry(t, path, s.ID); !slices.Equal(md.BundleDrops, want) {
		t.Fatalf("sidecar BundleDrops = %+v, want %+v", md.BundleDrops, want)
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
	if got := r.Info().BundleDrops; !slices.Equal(got, want) {
		t.Fatalf("restored BundleDrops = %+v, want %+v", got, want)
	}
}

// The workspace is recorded as box-written right before Wrap and never after:
// the box must not start in a directory the host has not yet marked.
func TestBoxedSpawnRecordsWorkspaceBeforeWrap(t *testing.T) {
	order := &callLog{}
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	boxes.order = order
	cwd := t.TempDir()
	m.SetBoxedRecorder(func(c string) error {
		order.add("record:" + c)
		return nil
	})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	got := order.got()
	if len(got) != 3 || got[0] != "prepare" || got[1] != "record:"+cwd || got[2] != "wrap" {
		t.Fatalf("call order = %v, want [prepare record:%s wrap]", got, cwd)
	}
}

// A recorder failure refuses the spawn and the box never starts: an unrecorded
// boxed workspace is exactly the hole the record closes.
func TestBoxedSpawnRecorderErrorRefusesSpawn(t *testing.T) {
	m, boxes := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	m.SetBoxedRecorder(func(string) error { return errors.New("disk full") })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeBox, Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawn succeeded although the workspace could not be recorded")
	}
	if !strings.Contains(err.Error(), "record boxed workspace") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want it to name the record failure", err)
	}
	if w, _, _ := boxes.counts(); w != 0 {
		t.Fatalf("Wrap ran %d times after a failed record", w)
	}
}

// A host-tier spawn writes nothing the box layer cares about.
func TestHostSpawnDoesNotRecordWorkspace(t *testing.T) {
	m, _ := managerWithBoxes(t, []string{"sh", "-c", "sleep 30"})
	called := false
	m.SetBoxedRecorder(func(string) error { called = true; return nil })
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "bash", Runtime: config.RuntimeHost, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if called {
		t.Fatal("a host spawn recorded its workspace as boxed")
	}
}
