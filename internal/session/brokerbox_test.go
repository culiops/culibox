package session

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/proxyline"
)

// A bound box gets its broker socket from the broker, per box: the session layer
// names the box before it builds the mounts, takes a reference on the box's
// socket, and gives that reference back on every failure and when the session
// ends. These tests pin that lifecycle.

func awsLauncherFor(b *config.AWSBinding) awsLauncher {
	return awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: b}
}

// boundContextLauncher is an AWS-bound profile that also has context paths.
type boundContextLauncher struct {
	awsLauncher
	paths []string
}

func (l boundContextLauncher) ContextPathsFor(string) ([]string, error) {
	return slices.Clone(l.paths), nil
}

// brokerBoxManager is a manager over the fakes, with one call log shared by the
// box layer and the broker so the order of calls can be pinned.
func brokerBoxManager(t *testing.T, l launcher.Builder, retention time.Duration) (*Manager, *fakeBoxes, *fakeBrokerHooks, *fakeEgressHooks) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, retention)
	order := &callLog{}
	boxes := &fakeBoxes{order: order}
	hooks := &fakeBrokerHooks{order: order}
	eg := &fakeEgressHooks{}
	m.SetBoxes(boxes)
	m.SetBroker(hooks)
	m.SetEgress(eg)
	m.SetContextCheck(func(string) (*box.FileID, error) { return nil, nil })
	return m, boxes, hooks, eg
}

func spawnAWS(m *Manager, cwd string) (*Session, error) {
	return m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: cwd})
}

func TestBoundSpawnNamesItsBoxBeforeTheBrokerMount(t *testing.T) {
	elevate := testBinding()
	elevate.Elevate = &config.AWSElevate{RoleARN: "arn:aws:iam::226198813800:role/agentbox-write"}
	cases := []struct {
		name  string
		l     launcher.Builder
		check func(t *testing.T, k box.BoxKey, id string)
	}{
		{"binding", awsLauncherFor(testBinding()), func(t *testing.T, k box.BoxKey, _ string) {
			if k.Binding == "" || k.Session != "" || k.Context != "" {
				t.Fatalf("key = %+v, want the binding only", k)
			}
		}},
		{"elevate: a box of its own", awsLauncherFor(elevate), func(t *testing.T, k box.BoxKey, id string) {
			if k.Binding == "" || k.Session != id {
				t.Fatalf("key = %+v, want the binding and the session %s", k, id)
			}
		}},
		{"context paths", boundContextLauncher{awsLauncherFor(testBinding()), []string{"/srv/b/docs"}}, func(t *testing.T, k box.BoxKey, _ string) {
			if k.Binding == "" || k.Context == "" {
				t.Fatalf("key = %+v, want the binding and the context hash", k)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, boxes, hooks, _ := brokerBoxManager(t, tc.l, time.Minute)
			s, err := spawnAWS(m, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })

			got := boxes.order.got()
			at := func(c string) int { return slices.Index(got, c) }
			if !(at("boxname") >= 0 && at("boxname") < at("boxfor") && at("boxfor") < at("acquire") && at("acquire") < at("wrap")) {
				t.Fatalf("call order %v: want boxname < boxfor < acquire < wrap", got)
			}
			// The name was asked about the SAME key Wrap got: complete before the name.
			// Wrap's key also carries the name the broker mount was prepared for, so
			// Wrap can refuse to create any other box with it.
			if boxes.lastKey.Expect != "ab-box-test" {
				t.Fatalf("Wrap's key Expect = %q, want the planned box ab-box-test", boxes.lastKey.Expect)
			}
			wrapKey := boxes.lastKey
			wrapKey.Expect = ""
			if !reflect.DeepEqual(boxes.nameKey, wrapKey) {
				t.Fatalf("BoxName was asked about %+v but Wrap got %+v", boxes.nameKey, wrapKey)
			}
			tc.check(t, boxes.nameKey, s.ID)
			if acq, _, rel := hooks.boxSockets(); !slices.Equal(acq, []string{"ab-box-test"}) || len(rel) != 0 {
				t.Fatalf("acquired %v released %v, want the one box held", acq, rel)
			}
			if !slices.Contains(boxes.lastRO, box.Mount{Host: "/state/broker/b/ab-box-test", Box: "/opt/agentbox-broker"}) {
				t.Fatalf("extraRO %+v lacks the box's broker socket mount", boxes.lastRO)
			}
			if s.Box != "ab-box-test" {
				t.Fatalf("session box = %q", s.Box)
			}
		})
	}
}

// Fail closed: the socket was prepared for the box BoxName named. If Wrap used
// another name the mount belongs to a different box, so the spawn is refused
// and everything it took is given back.
func TestBoundSpawnAbortsWhenWrapUsesAnotherBox(t *testing.T) {
	m, boxes, hooks, _ := brokerBoxManager(t, awsLauncherFor(testBinding()), time.Minute)
	boxes.wrapName = "ab-box-other"
	_, err := spawnAWS(m, t.TempDir())
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want ErrBoxUnavailable", err)
	}
	if !strings.Contains(err.Error(), "ab-box-other") || !strings.Contains(err.Error(), "ab-box-test") {
		t.Fatalf("the refusal must name both boxes: %v", err)
	}
	if _, releases, _ := boxes.counts(); releases != 1 {
		t.Fatalf("the box layer's release count = %d, want 1 (Wrap's box)", releases)
	}
	if acq, _, rel := hooks.boxSockets(); !slices.Equal(acq, []string{"ab-box-test"}) || !slices.Equal(rel, []string{"ab-box-test"}) {
		t.Fatalf("acquired %v released %v, want the reference taken and given back", acq, rel)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("%d sessions listed after a refused spawn", n)
	}
}

// Every way a spawn can fail after the box's socket was taken gives the
// reference back, exactly once.
func TestBoundSpawnFailureAfterAcquireReleasesTheBrokerBox(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, m *Manager, boxes *fakeBoxes, eg *fakeEgressHooks) error
	}{
		{"egress setup fails", func(t *testing.T, m *Manager, _ *fakeBoxes, eg *fakeEgressHooks) error {
			eg.err = errors.New("network gone")
			_, err := spawnAWS(m, t.TempDir())
			return err
		}},
		{"boxed workspace cannot be recorded", func(t *testing.T, m *Manager, _ *fakeBoxes, _ *fakeEgressHooks) error {
			m.SetBoxedRecorder(func(string) error { return errors.New("disk full") })
			_, err := spawnAWS(m, t.TempDir())
			return err
		}},
		{"wrap fails", func(t *testing.T, m *Manager, boxes *fakeBoxes, _ *fakeEgressHooks) error {
			boxes.wrapErr = errors.New("docker run failed")
			_, err := spawnAWS(m, t.TempDir())
			return err
		}},
		{"the process cannot start", func(t *testing.T, m *Manager, _ *fakeBoxes, _ *fakeEgressHooks) error {
			notADir := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := spawnAWS(m, notADir)
			return err
		}},
		{"resume cannot be dropped", func(t *testing.T, m *Manager, boxes *fakeBoxes, _ *fakeEgressHooks) error {
			m.SetBoxes(wrapShapeBoxes{boxes, func(w []string) []string { return append(slices.Clone(w), "trailing-junk") }})
			_, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir(), Resume: "no-such-conversation"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, boxes, hooks, eg := brokerBoxManager(t, awsLauncherFor(testBinding()), time.Minute)
			if err := tc.run(t, m, boxes, eg); err == nil {
				t.Fatal("the spawn must fail")
			}
			acq, _, rel := hooks.boxSockets()
			if len(acq) != 1 || len(rel) != 1 || acq[0] != rel[0] {
				t.Fatalf("acquired %v released %v, want one reference taken and one given back", acq, rel)
			}
			if n := len(m.List()); n != 0 {
				t.Fatalf("%d sessions listed after a failed spawn", n)
			}
		})
	}
}

func TestAcquireFailureFailsTheSpawnAndHoldsNothing(t *testing.T) {
	m, boxes, hooks, _ := brokerBoxManager(t, awsLauncherFor(testBinding()), time.Minute)
	hooks.acquireErr = errors.New("socket path too long")
	_, err := spawnAWS(m, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "socket path too long") {
		t.Fatalf("err = %v, want the broker's reason", err)
	}
	if wraps, _, _ := boxes.counts(); wraps != 0 {
		t.Fatalf("Wrap ran %d times for a spawn whose socket could not be prepared", wraps)
	}
	if acq, _, rel := hooks.boxSockets(); len(acq) != 0 || len(rel) != 0 {
		t.Fatalf("acquired %v released %v, want neither: nothing was held", acq, rel)
	}
}

func TestBoxNameFailureFailsTheSpawnBeforeAnythingIsPrepared(t *testing.T) {
	m, boxes, hooks, _ := brokerBoxManager(t, awsLauncherFor(testBinding()), time.Minute)
	boxes.nameErr = errors.New("cannot resolve workspace")
	_, err := spawnAWS(m, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "naming its box") {
		t.Fatalf("err = %v", err)
	}
	if hooks.boxForCalls != 0 {
		t.Fatalf("BoxFor ran %d times before the box had a name", hooks.boxForCalls)
	}
	if acq, _, rel := hooks.boxSockets(); len(acq) != 0 || len(rel) != 0 {
		t.Fatalf("acquired %v released %v", acq, rel)
	}
}

// An unbound box has no broker socket: no name is asked for early, nothing taken.
func TestUnboundSpawnTakesNoBrokerBox(t *testing.T) {
	m, boxes, hooks, _ := brokerBoxManager(t, awsLauncherFor(nil), time.Minute)
	s, err := spawnAWS(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	got := boxes.order.got()
	if slices.Contains(got, "boxname") || slices.Contains(got, "acquire") || !slices.Contains(got, "wrap") {
		t.Fatalf("call order %v: an unbound spawn takes no broker box", got)
	}
	if acq, _, _ := hooks.boxSockets(); len(acq) != 0 {
		t.Fatalf("acquired %v", acq)
	}
}

func TestBoundSessionEndReleasesItsBrokerBoxOnce(t *testing.T) {
	m, _, hooks, _ := brokerBoxManager(t, awsLauncherFor(testBinding()), 50*time.Millisecond)
	s, err := spawnAWS(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, rel := hooks.boxSockets(); len(rel) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // past retention: the pruning path must not release again
	if _, _, rel := hooks.boxSockets(); !slices.Equal(rel, []string{"ab-box-test"}) {
		t.Fatalf("released %v, want exactly one release", rel)
	}
}

// sockHooks is the fake broker with the REAL per-box sockets behind its Acquire,
// Adopt and Release, so the reference counting is the broker's own.
type sockHooks struct {
	*fakeBrokerHooks
	real broker.Hooks
}

func (h sockHooks) AcquireBox(n string) ([]box.Mount, error) { return h.real.AcquireBox(n) }
func (h sockHooks) AdoptBox(n string) error                  { return h.real.AdoptBox(n) }
func (h sockHooks) ReleaseBox(n string)                      { h.real.ReleaseBox(n) }

func realSockHooks(t *testing.T) (sockHooks, string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ep, err := broker.Listen("linux", root, 0, proxyline.Key{})
	if err != nil {
		t.Fatal(err)
	}
	ep.Boxes.Start(func(net.Listener) {})
	t.Cleanup(func() { ep.Boxes.Close(); ep.Listener.Close() })
	return sockHooks{&fakeBrokerHooks{}, broker.Hooks{StateRoot: root, EP: ep}}, root
}

func socketAccepts(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Review focus 1: two sessions share one box (same binding, no elevate) and one
// ends. The other's socket must keep serving; the last one out closes it.
func TestSecondSessionInTheBoxKeepsItsBrokerSocket(t *testing.T) {
	const name = "ab-box-0123456789ab"
	hooks, root := realSockHooks(t)
	m, boxes, _, _ := brokerBoxManager(t, awsLauncherFor(testBinding()), time.Minute)
	boxes.name = name
	m.SetBroker(hooks)
	first, err := spawnAWS(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := spawnAWS(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(second.ID) })
	sock := filepath.Join(root, "broker", "b", "0123456789ab", "s")
	if !socketAccepts(sock) {
		t.Fatal("the box's socket is not serving with two sessions in it")
	}
	if err := m.Kill(first.ID); err != nil {
		t.Fatal(err)
	}
	if !socketAccepts(sock) {
		t.Fatal("ending one session closed the socket the other still uses")
	}
	if err := m.Kill(second.ID); err != nil {
		t.Fatal(err)
	}
	if socketAccepts(sock) {
		t.Fatal("the socket outlived its last session")
	}
	if _, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("the directory must stay (a running box mounts it): %v", err)
	}
}

// reconcileManager is a manager over a fake backend whose sidecar holds one
// bound boxed session, one unbound boxed session and one host session, all
// alive, as after a daemon restart.
func reconcileManager(t *testing.T, be Backend) (*Manager, *fakeBoxes, *fakeBrokerHooks) {
	t.Helper()
	m := NewManager(tieredLauncher{argv: []string{"true"}, runtime: config.RuntimeBox}, be, 1<<16, time.Minute)
	if err := m.SetSidecarPath(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m.sidecar.put("aws-1", sessionMeta{Agent: "aws", Cwd: dir, Created: time.Now(), Runtime: config.RuntimeBox, Box: "ab-box-r",
		AWS: testBinding(), BrokerNonce: fakeNonce})
	m.sidecar.put("bash-2", sessionMeta{Agent: "bash", Cwd: dir, Created: time.Now(), Runtime: config.RuntimeBox, Box: "ab-box-u"})
	m.sidecar.put("bash-3", sessionMeta{Agent: "bash", Cwd: dir, Created: time.Now(), Runtime: config.RuntimeHost})
	boxes := &fakeBoxes{}
	hooks := &fakeBrokerHooks{}
	m.SetBoxes(boxes)
	m.SetBroker(hooks)
	return m, boxes, hooks
}

func aliveBackend() *fakeBackend {
	be := newFakeBackend()
	for _, id := range []string{"aws-1", "bash-2", "bash-3"} {
		be.seedLive(id)
	}
	return be
}

// adoptSpy reports, at the moment of the adopt, whether the session was already
// published: it must not be (an adopt after publication could be followed by an
// end that never released it).
type adoptSpy struct {
	*fakeBrokerHooks
	m         *Manager
	published []bool
}

func (s *adoptSpy) AdoptBox(name string) error {
	_, ok := s.m.Get("aws-1")
	s.published = append(s.published, ok)
	return s.fakeBrokerHooks.AdoptBox(name)
}

// Review focus 2: after a restart a live bound session's box gets its socket
// back, held for the session's life; unbound and host sessions get nothing.
func TestReconcileAdoptsTheBrokerBoxOfBoundSessions(t *testing.T) {
	m, _, hooks := reconcileManager(t, aliveBackend())
	spy := &adoptSpy{fakeBrokerHooks: hooks, m: m}
	m.SetBroker(spy)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if acq, adopted, _ := hooks.boxSockets(); !slices.Equal(adopted, []string{"ab-box-r"}) || len(acq) != 0 {
		t.Fatalf("adopted %v acquired %v, want only the bound session's box adopted", adopted, acq)
	}
	if !slices.Equal(spy.published, []bool{false}) {
		t.Fatalf("published at adopt time = %v, want the adopt before publication", spy.published)
	}
	if err := m.Kill("aws-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, rel := hooks.boxSockets(); !slices.Equal(rel, []string{"ab-box-r"}) {
		t.Fatalf("released %v, want the adopted box given back when its session ends", rel)
	}
	// The sessions that hold nothing end without releasing anything.
	_ = m.Kill("bash-2")
	_ = m.Kill("bash-3")
	if _, _, rel := hooks.boxSockets(); len(rel) != 1 {
		t.Fatalf("released %v: an unbound or host session released a broker box", rel)
	}
}

// A session that could not be re-attached is not published, so the broker
// refuses it anyway: nothing is adopted for it.
func TestReconcileFailedAttachAdoptsNoBrokerBox(t *testing.T) {
	m, boxes, hooks := reconcileManager(t, failingAttachBackend{aliveBackend()})
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, adopted, _ := hooks.boxSockets(); len(adopted) != 0 {
		t.Fatalf("adopted %v for sessions that were never published", adopted)
	}
	if !boxes.adoptedBox("ab-box-r") {
		t.Fatal("the box layer must still keep the box of a live agent")
	}
}

// Review focus 3: an adopt that failed leaves the session holding nothing, so
// its end must not close the socket another session in the box relies on.
func TestFailedAdoptReleasesNothingAtEnd(t *testing.T) {
	m, _, hooks := reconcileManager(t, aliveBackend())
	hooks.adoptErr = errors.New("socket path too long")
	if err := m.Reconcile(); err != nil {
		t.Fatalf("a failed adopt must not fail the reconcile: %v", err)
	}
	if _, ok := m.Get("aws-1"); !ok {
		t.Fatal("the session was not restored")
	}
	if err := m.Kill("aws-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, rel := hooks.boxSockets(); len(rel) != 0 {
		t.Fatalf("released %v for a session that holds no reference", rel)
	}
}

// With no broker (no containment, or it failed to start) there is nothing to
// adopt and nothing to release.
func TestReconcileWithoutABrokerAdoptsNothing(t *testing.T) {
	m, _, _ := reconcileManager(t, aliveBackend())
	m.SetBroker(nil)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill("aws-1"); err != nil {
		t.Fatal(err)
	}
}
