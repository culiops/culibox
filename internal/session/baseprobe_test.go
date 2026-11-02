package session

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

var probeAt = time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)

func writesResult() awsprobe.Result {
	return awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject", "ec2:RunInstances"}, CheckedAt: probeAt}
}

func noneResult() awsprobe.Result {
	return awsprobe.Result{State: awsprobe.NoneFound, CheckedAt: probeAt}
}

func (f *fakeBrokerHooks) watchedKeys(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.watched[id])
}

// probeState is what the fake recorded of the probe and BoxFor, read under its lock.
func (f *fakeBrokerHooks) probeState() (calls []probeCall, lastBase map[string]awsprobe.Result, boxFor int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probeCalls), maps.Clone(f.lastBase), f.boxForCalls
}

func (f *fakeBrokerHooks) listingCalls() []listingCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.listings)
}

func connInfo(t *testing.T, in Info, name string) AWSConnInfo {
	t.Helper()
	for _, c := range in.AWSConnections {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("Info has no connection %q: %+v", name, in.AWSConnections)
	return AWSConnInfo{}
}

// managerWith is a bound manager over l, with a sidecar at path (none when
// empty), the backend fb (nil = ephemeral) and the given broker fake.
func managerWith(t *testing.T, l launcher.Builder, path string, fb Backend, hooks BrokerHooks) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if path != "" {
		if err := m.SetSidecarPath(path); err != nil {
			t.Fatal(err)
		}
	}
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetBroker(hooks)
	m.SetEgress(&fakeEgressHooks{})
	return m, boxes
}

func spawnT(t *testing.T, m *Manager, agent string) *Session {
	t.Helper()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: agent, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return s
}

func legacyLauncher() awsLauncher {
	return awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: testBinding()}
}

// The probe runs at spawn, before the binding dir is written, and its result
// reaches BoxFor (aws.json) and Info.
func TestSpawnProbesBeforeBoxFor(t *testing.T) {
	hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"sbx-a": writesResult(), "sbx-b": noneResult()}, order: &callLog{}}
	m, boxes := managerWith(t, connLauncherFor(connA(), connB()), "", nil, hooks)
	boxes.order = hooks.order
	s := spawnT(t, m, "conn")

	calls, lastBase, _ := hooks.probeState()
	if len(calls) != 1 || calls[0].id != s.ID || calls[0].agent != "conn" || calls[0].bs.Hash() != s.Bindings().Hash() {
		t.Fatalf("probe calls %+v", calls)
	}
	if !reflect.DeepEqual(lastBase, hooks.base) {
		t.Fatalf("BoxFor got base %+v, want %+v", lastBase, hooks.base)
	}
	if got := hooks.order.got(); !reflect.DeepEqual(got, []string{"prepare", "probe", "boxfor", "wrap"}) {
		t.Fatalf("call order %q", got)
	}
	a, b := connInfo(t, s.Info(), "sbx-a"), connInfo(t, s.Info(), "sbx-b")
	if a.BaseAccess != "writes" || !reflect.DeepEqual(a.BaseWriteActions, []string{"s3:PutObject", "ec2:RunInstances"}) ||
		a.BaseCheckedAt == nil || !a.BaseCheckedAt.Equal(probeAt) {
		t.Fatalf("sbx-a fields %+v", a.Fields)
	}
	if b.BaseAccess != "none_found" || b.BaseAccessReason != "" || len(b.BaseWriteActions) != 0 {
		t.Fatalf("sbx-b fields %+v", b.Fields)
	}
	if k := hooks.watchedKeys(s.ID); len(k) != 0 {
		t.Fatalf("definite results were watched: %q", k)
	}
}

func TestSpawnLegacyProbe(t *testing.T) {
	hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"": writesResult()}}
	m, _ := managerWith(t, legacyLauncher(), "", nil, hooks)
	s := spawnT(t, m, "aws")
	if got, want := s.Info().AWS.Fields, writesResult().Fields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Info.AWS fields %+v, want %+v", got, want)
	}
	if _, lastBase, _ := hooks.probeState(); !reflect.DeepEqual(lastBase, hooks.base) {
		t.Fatalf("BoxFor got base %+v", lastBase)
	}
}

// An unverified key is watched for a re-probe, and the watch is registered
// before the box starts, so it is in place before the box's first mint.
func TestSpawnUnverifiedIsWatched(t *testing.T) {
	hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"sbx-a": noneResult(),
		"sbx-b": awsprobe.Fail(awsprobe.ReasonTimeout, probeAt)}, order: &callLog{}}
	m, boxes := managerWith(t, connLauncherFor(connA(), connB()), "", nil, hooks)
	boxes.order = hooks.order
	s := spawnT(t, m, "conn")
	if k := hooks.watchedKeys(s.ID); !reflect.DeepEqual(k, []string{"sbx-b"}) {
		t.Fatalf("watched %q, want [sbx-b]", k)
	}
	got := hooks.order.got()
	if w, wr := slices.Index(got, "watch"), slices.Index(got, "wrap"); w < 0 || wr < 0 || w > wr {
		t.Fatalf("call order %q: the watch must precede the box start", got)
	}
	b := connInfo(t, s.Info(), "sbx-b")
	if b.BaseAccess != "unverified" || b.BaseAccessReason != "timeout" {
		t.Fatalf("sbx-b fields %+v", b.Fields)
	}
}

// A spawn that fails after its watch was registered forgets the id, so the
// broker's watch list never keeps a session that does not exist.
func TestSpawnFailureAfterWatchForgets(t *testing.T) {
	hooks := &fakeBrokerHooks{} // default answer: probe_disabled, so every key is watched
	m, boxes := managerWith(t, connLauncherFor(connA()), "", nil, hooks)
	boxes.wrapErr = os.ErrPermission
	if s, err := m.Spawn(launcher.SpawnSpec{Agent: "conn", Cwd: t.TempDir()}); err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawn succeeded with a failing Wrap")
	}
	hooks.mu.Lock()
	var ids []string
	for id := range hooks.watched {
		ids = append(ids, id)
	}
	hooks.mu.Unlock()
	if len(ids) != 1 || !hooks.forgotten(ids[0]) {
		t.Fatalf("watched %q, forgot %q", ids, hooks.forgot)
	}
}

func TestSpawnWithoutAWSDoesNotProbe(t *testing.T) {
	m, _, hooks, _ := ghManager(t, config.RuntimeBox)
	s := spawnT(t, m, "gh")
	if calls, lastBase, boxFor := hooks.probeState(); len(calls) != 0 || lastBase != nil || boxFor != 1 {
		t.Fatalf("probe calls %d, BoxFor base %+v, BoxFor calls %d", len(calls), lastBase, boxFor)
	}
	if k := hooks.watchedKeys(s.ID); len(k) != 0 {
		t.Fatalf("watched %q", k)
	}
}

func TestSidecarPersistsBaseAccess(t *testing.T) {
	base := map[string]awsprobe.Result{"sbx-a": writesResult(), "sbx-b": awsprobe.Fail(awsprobe.ReasonSSOExpired, probeAt)}
	l := connLauncherFor(connA(), connB())
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m, _ := managerWith(t, l, path, fb, &fakeBrokerHooks{base: base})
	s := spawnT(t, m, "conn")
	if md := sidecarEntry(t, path, s.ID); !reflect.DeepEqual(md.BaseAccess, base) {
		t.Fatalf("sidecar base_access %+v, want %+v", md.BaseAccess, base)
	}

	fb.seedLive(s.ID)
	hooks2 := &fakeBrokerHooks{}
	m2, _ := managerWith(t, l, path, fb, hooks2)
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	if !reflect.DeepEqual(r.Info().AWSConnections, s.Info().AWSConnections) {
		t.Fatalf("restored %+v\nwant %+v", r.Info().AWSConnections, s.Info().AWSConnections)
	}
	if k := hooks2.watchedKeys(s.ID); !reflect.DeepEqual(k, []string{"sbx-b"}) {
		t.Fatalf("restore watched %q, want [sbx-b]", k)
	}
	if len(hooks2.probeCalls) != 0 {
		t.Fatal("a restore re-probed at once")
	}
}

// Review focus 1: a record written before the probe existed has no
// base_access. It restores as "not checked", never as a clean result, it is
// watched, and a later re-probe fills it in (the map starts nil).
func TestOldSidecarWithoutBaseAccessIsNotChecked(t *testing.T) {
	old := `{"agent":"aws","created":"2026-10-01T00:00:00Z","runtime":"box","box":"ab-box-test","replyable":false,` +
		`"aws":{"source_profile":"sandbox-admin","role_arn":"arn:aws:iam::226198813800:role/agentbox-agent","region":"ap-southeast-1","s3_read":["culishop-tfstate"],"label":"sandbox"},` +
		`"aws_nonce":"` + fakeNonce + `","egress_nonce":"` + fakeNonce + `"}`
	var md sessionMeta
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.BaseAccess != nil {
		t.Fatalf("old record decoded base_access %+v", md.BaseAccess)
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("oldsess1", md)
	fb := newFakeBackend()
	fb.seedLive("oldsess1")
	hooks := &fakeBrokerHooks{}
	m, _ := managerWith(t, legacyLauncher(), path, fb, hooks)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m.Get("oldsess1")
	if !ok {
		t.Fatal("old session not reattached")
	}
	t.Cleanup(func() { _ = m.Kill(r.ID) })
	in := r.Info().AWS
	if in.BaseAccess != "unverified" || in.BaseAccessReason != "not_checked" || in.BaseCheckedAt != nil {
		t.Fatalf("old record reads %+v", in.Fields)
	}
	if k := hooks.watchedKeys("oldsess1"); !reflect.DeepEqual(k, []string{""}) {
		t.Fatalf("watched %q, want [\"\"]", k)
	}

	hooks.notifyFn()("oldsess1", "", writesResult())
	if in := r.Info().AWS; in.BaseAccess != "writes" {
		t.Fatalf("after a re-probe the old record reads %+v", in.Fields)
	}
	if got := sidecarEntry(t, path, "oldsess1").BaseAccess; !reflect.DeepEqual(got, map[string]awsprobe.Result{"": writesResult()}) {
		t.Fatalf("sidecar base_access %+v", got)
	}
}

// Review focus 1: a corrupt record (an unknown state string) restores as "not
// checked", never as a clean result, and is watched.
func TestSidecarUnknownBaseStateIsNotChecked(t *testing.T) {
	rec := `{"agent":"aws","created":"2026-10-01T00:00:00Z","runtime":"box","box":"ab-box-test","replyable":false,` +
		`"aws":{"source_profile":"sandbox-admin","role_arn":"arn:aws:iam::226198813800:role/agentbox-agent","region":"ap-southeast-1","s3_read":["culishop-tfstate"],"label":"sandbox"},` +
		`"aws_nonce":"` + fakeNonce + `","egress_nonce":"` + fakeNonce + `","base_access":{"":{"state":"clean"}}}`
	var md sessionMeta
	if err := json.Unmarshal([]byte(rec), &md); err != nil {
		t.Fatal(err)
	}
	if md.BaseAccess[""].State != "clean" {
		t.Fatalf("record decoded base_access %+v", md.BaseAccess)
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	sc, err := openSidecar(path)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("oddsess1", md)
	fb := newFakeBackend()
	fb.seedLive("oddsess1")
	hooks := &fakeBrokerHooks{}
	m, _ := managerWith(t, legacyLauncher(), path, fb, hooks)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m.Get("oddsess1")
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m.Kill(r.ID) })
	if in := r.Info().AWS; in.BaseAccess != "unverified" || in.BaseAccessReason != "not_checked" {
		t.Fatalf("an unknown state reads %+v", in.Fields)
	}
	if k := hooks.watchedKeys("oddsess1"); !reflect.DeepEqual(k, []string{""}) {
		t.Fatalf("watched %q, want [\"\"]", k)
	}
}

func TestBaseProbedUpdatesInfoSidecarAndListing(t *testing.T) {
	t.Run("connections", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sessions.json")
		hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"sbx-a": noneResult(), "sbx-b": awsprobe.Fail(awsprobe.ReasonTimeout, probeAt)}}
		m, _ := managerWith(t, connLauncherFor(connA(), connB()), path, newFakeBackend(), hooks)
		s := spawnT(t, m, "conn")

		hooks.notifyFn()(s.ID, "sbx-b", writesResult())
		if b := connInfo(t, s.Info(), "sbx-b"); b.BaseAccess != "writes" || len(b.BaseWriteActions) != 2 {
			t.Fatalf("sbx-b after re-probe %+v", b.Fields)
		}
		want := map[string]awsprobe.Result{"sbx-a": noneResult(), "sbx-b": writesResult()}
		if got := sidecarEntry(t, path, s.ID).BaseAccess; !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecar base_access %+v, want %+v", got, want)
		}
		ls := hooks.listingCalls()
		if len(ls) != 1 || ls[0].bs.Hash() != s.Bindings().Hash() || !reflect.DeepEqual(ls[0].base, want) {
			t.Fatalf("listings %+v", ls)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sessions.json")
		hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"": awsprobe.Fail(awsprobe.ReasonAssumeDenied, probeAt)}}
		m, _ := managerWith(t, legacyLauncher(), path, newFakeBackend(), hooks)
		s := spawnT(t, m, "aws")

		hooks.notifyFn()(s.ID, "", noneResult())
		if in := s.Info().AWS; in.BaseAccess != "none_found" {
			t.Fatalf("Info.AWS after re-probe %+v", in.Fields)
		}
		if got := sidecarEntry(t, path, s.ID).BaseAccess; !reflect.DeepEqual(got, map[string]awsprobe.Result{"": noneResult()}) {
			t.Fatalf("sidecar base_access %+v", got)
		}
		if ls := hooks.listingCalls(); len(ls) != 0 {
			t.Fatalf("a legacy session has no aws.json, yet WriteAWSListing ran: %+v", ls)
		}
	})
}

func TestBaseProbedUnknownSessionIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"sbx-a": noneResult()}}
	m, _ := managerWith(t, connLauncherFor(connA()), path, newFakeBackend(), hooks)
	s := spawnT(t, m, "conn")
	before := sidecarEntry(t, path, s.ID).BaseAccess

	hooks.notifyFn()("conn-999", "sbx-a", writesResult()) // no such session
	hooks.notifyFn()(s.ID, "not-a-conn", writesResult())  // no such key on this session
	if ls := hooks.listingCalls(); len(ls) != 0 {
		t.Fatalf("listings %+v", ls)
	}
	if got := sidecarEntry(t, path, s.ID).BaseAccess; !reflect.DeepEqual(got, before) {
		t.Fatalf("sidecar changed: %+v", got)
	}
	sc, _ := openSidecar(path)
	if _, ok := sc.all()["conn-999"]; ok {
		t.Fatal("a notify for an unknown session created a sidecar entry")
	}
	if a := connInfo(t, s.Info(), "sbx-a"); a.BaseAccess != "none_found" {
		t.Fatalf("Info changed: %+v", a.Fields)
	}
}

func TestSetBrokerRegistersNotify(t *testing.T) {
	_, _, hooks := connManager(t, connLauncherFor(connA()))
	if hooks.notifyFn() == nil {
		t.Fatal("SetBroker did not register the re-probe sink")
	}
}

func TestInfoJSONHasBaseAccess(t *testing.T) {
	hooks := &fakeBrokerHooks{base: map[string]awsprobe.Result{"sbx-a": noneResult()}}
	m, _ := managerWith(t, connLauncherFor(connA()), "", nil, hooks)
	s := spawnT(t, m, "conn")
	raw, err := json.Marshal(s.Info())
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Conns []map[string]any `json:"aws_connections"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Conns) != 1 || got.Conns[0]["base_access"] != "none_found" || got.Conns[0]["base_checked_at"] == nil {
		t.Fatalf("Info JSON %s", raw)
	}
	if strings.Contains(string(raw), "source_profile") || strings.Contains(string(raw), "sandbox-admin") {
		t.Fatalf("Info JSON leaks the source profile: %s", raw)
	}
}

// gatedFilesHooks is realFilesHooks whose first WriteAWSListing signals entered
// and blocks until release is closed; later calls pass straight through (an
// atomic flag, not a sync.Once: Once would hold them until the first returns).
type gatedFilesHooks struct {
	realFilesHooks
	taken   *atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (h gatedFilesHooks) WriteAWSListing(bs config.Bindings, base map[string]awsprobe.Result) error {
	if h.taken.CompareAndSwap(false, true) {
		close(h.entered)
		<-h.release
	}
	return h.realFilesHooks.WriteAWSListing(bs, base)
}

// Two re-probes of one session landing together each persist the merged map,
// never an older snapshot over a newer one. Deterministic: re-probe A is held
// inside its aws.json write (its snapshot taken) while re-probe B runs; without
// the per-session persist lock B finishes first and A's older snapshot lands
// last.
func TestConcurrentReprobesPersistBothKeys(t *testing.T) {
	stateRoot := t.TempDir()
	path := filepath.Join(t.TempDir(), "sessions.json")
	fake := &fakeBrokerHooks{}
	h := gatedFilesHooks{realFilesHooks: realFilesHooks{fakeBrokerHooks: fake, root: stateRoot},
		taken: &atomic.Bool{}, entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := managerWith(t, connLauncherFor(connA(), connB()), path, newFakeBackend(), h)
	s := spawnT(t, m, "conn")
	notify := fake.notifyFn()
	listPath := filepath.Join(stateRoot, "broker", "binding-"+s.Bindings().Hash(), broker.AWSListFile)
	atA, atB := probeAt.Add(time.Minute), probeAt.Add(2*time.Minute)

	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(doneA)
		notify(s.ID, "sbx-a", awsprobe.Result{State: awsprobe.NoneFound, CheckedAt: atA})
	}()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("re-probe A never reached WriteAWSListing")
	}
	go func() {
		defer close(doneB)
		notify(s.ID, "sbx-b", awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}, CheckedAt: atB})
	}()
	select { // with the lock B waits for A; without it B runs to completion
	case <-doneB:
	case <-time.After(100 * time.Millisecond):
	}
	close(h.release)
	<-doneA
	<-doneB

	md := sidecarEntry(t, path, s.ID)
	if !md.BaseAccess["sbx-a"].CheckedAt.Equal(atA) || !md.BaseAccess["sbx-b"].CheckedAt.Equal(atB) {
		t.Fatalf("sidecar kept an older snapshot: %+v", md.BaseAccess)
	}
	raw, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name string `json:"name"`
		awsprobe.Fields
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{"sbx-a": atA, "sbx-b": atB}
	for _, r := range rows {
		if r.BaseCheckedAt == nil || !r.BaseCheckedAt.Equal(want[r.Name]) {
			t.Fatalf("aws.json kept an older snapshot: %s", raw)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("aws.json rows: %s", raw)
	}
}
