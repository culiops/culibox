package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// awsLauncher is a tieredLauncher whose profile "aws" carries an AWS binding
// and whose built env carries what a careless profile might set: static keys
// and its own AWS profile selector.
type awsLauncher struct {
	tieredLauncher
	binding  *config.AWSBinding
	extraEnv []string // appended to the built env (e.g. a profile's own proxy settings)
}

func (l awsLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return l.argv, append([]string{"TERM=xterm", "AWS_ACCESS_KEY_ID=AKIASTATIC", "AWS_PROFILE=host-admin",
		"AGENTBOX_BROKER_TOKEN=forged"}, l.extraEnv...), nil
}

func (l awsLauncher) AWSFor(agent string) *config.AWSBinding {
	if agent != "aws" || l.binding == nil {
		return nil
	}
	c := *l.binding
	c.S3Read = slices.Clone(l.binding.S3Read)
	return &c
}

func testBinding() *config.AWSBinding {
	return &config.AWSBinding{SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent",
		Region: "ap-southeast-1", S3Read: []string{"culishop-tfstate"}, Label: "sandbox"}
}

const fakeNonce = "0123456789abcdef0123456789abcdef"

type fakeBrokerHooks struct {
	mu          sync.Mutex
	nonces      int
	tokens      int
	boxForCalls int
	checkErr    error // CheckGitHub's answer
	checkCalls  int
	lastBS      config.Bindings // what BoxFor was last asked for
	forgot      []string
	revoked     []string // RevokeGitHub calls, in order
	mcpKeys     []string // WriteSessionMCP calls: the key and the document
	mcpDocs     []string
	mcpErr      error
	next        int
	state       map[string]awsState
	storeLocked map[string]bool // StoreLockedFor's answer per session

	// Base-role probe: base answers ProbeBase per key (a key it lacks is
	// Disabled, the broker's no-simulator answer); the rest records calls.
	base       map[string]awsprobe.Result
	probeCalls []probeCall
	lastBase   map[string]awsprobe.Result // what BoxFor was last given
	watched    map[string][]string        // WatchBase: session id -> keys
	notify     func(sessionID, key string, r awsprobe.Result)
	listings   []listingCall // WriteAWSListing calls
	listErr    error
	order      *callLog // optional: shared with fakeBoxes to pin call order

	// The box's own broker socket (AcquireBox/AdoptBox/ReleaseBox), recorded in
	// call order. A nil-error Acquire or Adopt must be paired with one Release.
	acquired   []string
	adopted    []string
	released   []string
	acquireErr error
	adoptErr   error
}

type probeCall struct {
	id, agent string
	bs        config.Bindings
}

type listingCall struct {
	bs   config.Bindings
	base map[string]awsprobe.Result
}

// probeKeys are a binding set's probe keys: "" for the legacy binding, else
// the connection names.
func probeKeys(bs config.Bindings) []string {
	if bs.AWS != nil {
		return []string{""}
	}
	var keys []string
	for _, c := range bs.AWSConns {
		keys = append(keys, c.Name)
	}
	return keys
}

func (f *fakeBrokerHooks) ProbeBase(id, agent string, bs config.Bindings) map[string]awsprobe.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeCalls = append(f.probeCalls, probeCall{id: id, agent: agent, bs: bs})
	f.order.add("probe")
	out := map[string]awsprobe.Result{}
	for _, k := range probeKeys(bs) {
		if r, ok := f.base[k]; ok {
			out[k] = r
		} else {
			out[k] = awsprobe.Disabled()
		}
	}
	return out
}

func (f *fakeBrokerHooks) WatchBase(id string, keys []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watched == nil {
		f.watched = map[string][]string{}
	}
	f.watched[id] = append(f.watched[id], keys...)
	f.order.add("watch")
}

func (f *fakeBrokerHooks) SetBaseNotify(fn func(sessionID, key string, r awsprobe.Result)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notify = fn
}

func (f *fakeBrokerHooks) WriteAWSListing(bs config.Bindings, base map[string]awsprobe.Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listings = append(f.listings, listingCall{bs: bs, base: maps.Clone(base)})
	return f.listErr
}

func (f *fakeBrokerHooks) notifyFn() func(sessionID, key string, r awsprobe.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notify
}

func (f *fakeBrokerHooks) setState(id string, until time.Time, sso string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]awsState{}
	}
	f.state[id] = awsState{until: until, sso: sso}
}

func (f *fakeBrokerHooks) setConnState(id string, st awsState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]awsState{}
	}
	f.state[id] = st
}

func (f *fakeBrokerHooks) AWSState(id string) (time.Time, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.state[id]
	return st.until, st.conn, st.sso
}

func (f *fakeBrokerHooks) StoreLockedFor(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.storeLocked[id]
}

func (f *fakeBrokerHooks) setStoreLocked(id string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeLocked == nil {
		f.storeLocked = map[string]bool{}
	}
	f.storeLocked[id] = v
}

func (f *fakeBrokerHooks) AWSConnState(id string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[id].conns
}

func (f *fakeBrokerHooks) NewNonce() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonces++
	f.next++
	if f.next == 1 {
		return fakeNonce
	}
	return strings.Repeat(string(rune('0'+f.next%10)), 32)
}

func (f *fakeBrokerHooks) Token(id, nonce string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens++
	return "tok-" + id + "." + nonce
}

func (f *fakeBrokerHooks) BoxFor(bs config.Bindings, base map[string]awsprobe.Result) ([]box.Mount, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxForCalls++
	f.lastBS = bs
	f.lastBase = base
	f.order.add("boxfor")
	env := []string{"AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock"}
	if bs.AWS != nil {
		env = append([]string{"AWS_PROFILE=agentbox"}, env...)
	}
	if bs.GitHub != nil {
		// The real GitHubEnv, so the git config count and indexes are the ones a
		// push rewrite has to extend.
		env = append(env, broker.GitHubEnv(*bs.GitHub)...)
	}
	return []box.Mount{{Host: "/state/broker/binding-x", Box: "/opt/agentbox"}}, env, nil
}

func (f *fakeBrokerHooks) AcquireBox(name string) ([]box.Mount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order.add("acquire")
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	f.acquired = append(f.acquired, name)
	return []box.Mount{{Host: "/state/broker/b/" + name, Box: "/opt/agentbox-broker"}}, nil
}

func (f *fakeBrokerHooks) AdoptBox(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.adoptErr != nil {
		return f.adoptErr
	}
	f.adopted = append(f.adopted, name)
	return nil
}

func (f *fakeBrokerHooks) ReleaseBox(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, name)
}

// boxSockets is a snapshot of the broker-socket calls.
func (f *fakeBrokerHooks) boxSockets() (acquired, adopted, released []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.acquired), slices.Clone(f.adopted), slices.Clone(f.released)
}

func (f *fakeBrokerHooks) CheckGitHub(config.GitHubBinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls++
	return f.checkErr
}

func (f *fakeBrokerHooks) StripEnv() []string {
	return broker.StripEnv()
}

// StripGitHubEnv is the real list, like StripEnv: the guard tests below are
// about what the real lists do to a profile's env.
func (f *fakeBrokerHooks) StripGitHubEnv() []string { return broker.StripGitHubEnv() }

// WriteSessionMCP records what it was asked to write and answers with the path
// the real hook would: the file's name in the box's binding dir.
func (f *fakeBrokerHooks) WriteSessionMCP(bs config.Bindings, key string, doc []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mcpErr != nil {
		return "", f.mcpErr
	}
	f.mcpKeys = append(f.mcpKeys, key)
	f.mcpDocs = append(f.mcpDocs, string(doc))
	return "/opt/agentbox/mcp-" + key + ".json", nil
}

func (f *fakeBrokerHooks) Forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
}

func (f *fakeBrokerHooks) RevokeGitHub(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
}

func (f *fakeBrokerHooks) revokes(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.revoked {
		if r == id {
			n++
		}
	}
	return n
}

func (f *fakeBrokerHooks) calls() (nonces, tokens, boxFor int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonces, f.tokens, f.boxForCalls
}

func (f *fakeBrokerHooks) forgotten(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.forgot, id)
}

func boundManager(t *testing.T, argv []string, runtime string, retention time.Duration) (*Manager, *fakeBoxes, *fakeBrokerHooks, *fakeEgressHooks) {
	return boundManagerEnv(t, argv, runtime, retention, nil)
}

func boundManagerEnv(t *testing.T, argv []string, runtime string, retention time.Duration, extraEnv []string) (*Manager, *fakeBoxes, *fakeBrokerHooks, *fakeEgressHooks) {
	t.Helper()
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: runtime}, binding: testBinding(), extraEnv: extraEnv},
		nil, 1<<16, retention)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	eg := &fakeEgressHooks{}
	m.SetEgress(eg)
	return m, boxes, hooks, eg
}

// unboundEgressManager is boundManagerEnv with no AWS binding anywhere and
// egress required for every boxed spawn (what Linux boot configures).
func unboundEgressManager(t *testing.T, argv []string, runtime string, extraEnv []string) (*Manager, *fakeBoxes, *fakeEgressHooks) {
	t.Helper()
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: runtime}, binding: nil, extraEnv: extraEnv},
		nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetBroker(&fakeBrokerHooks{})
	eg := &fakeEgressHooks{}
	m.SetEgress(eg)
	m.SetEgressAll(true)
	return m, boxes, eg
}

type fakeEgressHooks struct {
	mu     sync.Mutex
	calls  int
	forgot []string
	err    error
	// liveAtForget, when set, is asked (with the id) at Forget time; the answer
	// is recorded so a test can pin "not live before Forget".
	liveAtForget func(id string) bool
	wasLive      []bool
}

func (f *fakeEgressHooks) SpawnEnv(_ context.Context, id, nonce string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	u := "http://" + id + ":etok-" + nonce + "@172.21.0.1:9072"
	return []string{"HTTPS_PROXY=" + u, "https_proxy=" + u, "CHECKPOINT_DISABLE=1"}, nil
}

func (f *fakeEgressHooks) StripEnv() []string {
	return []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY"}
}

func (f *fakeEgressHooks) Forget(id string) {
	var live bool
	if f.liveAtForget != nil {
		live = f.liveAtForget(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
	f.wasLive = append(f.wasLive, live)
}

func (f *fakeEgressHooks) forgotten(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.forgot, id)
}

func (f *fakeEgressHooks) spawnCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func envNamed(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func TestBoundSpawnGetsBrokerEnvMountsTokenAndItsOwnBox(t *testing.T) {
	m, boxes, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	if want := testBinding().Hash(); boxes.lastKey.Binding != want {
		t.Fatalf("box key binding = %q, want %q", boxes.lastKey.Binding, want)
	}
	if !slices.Contains(boxes.lastRO, box.Mount{Host: "/state/broker/binding-x", Box: "/opt/agentbox"}) {
		t.Fatalf("extraRO %+v lacks the binding dir", boxes.lastRO)
	}
	env := boxes.lastEnv
	if got := envNamed(env, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, []string{"AGENTBOX_BROKER_TOKEN=tok-" + s.ID + "." + fakeNonce}) {
		t.Fatalf("token env = %q (the profile's forged token must be stripped, the spawn's own set once)", got)
	}
	if got := envNamed(env, "AWS_PROFILE"); !reflect.DeepEqual(got, []string{"AWS_PROFILE=agentbox"}) {
		t.Fatalf("AWS_PROFILE env = %q, want only the broker's", got)
	}
	if got := envNamed(env, "AWS_ACCESS_KEY_ID"); len(got) != 0 {
		t.Fatalf("static key reached the box: %q", got)
	}
	if !slices.Contains(env, "AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock") || !slices.Contains(env, "TERM=xterm") {
		t.Fatalf("env %q lacks the broker URL or lost an unrelated var", env)
	}
	if s.AWS == nil || !reflect.DeepEqual(*s.AWS, *testBinding()) || s.BrokerNonce != fakeNonce {
		t.Fatalf("session AWS=%+v nonce=%q", s.AWS, s.BrokerNonce)
	}
	info := s.Info()
	want := &AWSInfo{Label: "sandbox", AccountID: "123456789012", RoleARN: testBinding().RoleARN, Prod: false, Fields: awsprobe.Disabled().Fields()}
	if !reflect.DeepEqual(info.AWS, want) {
		t.Fatalf("Info.AWS = %+v, want %+v", info.AWS, want)
	}
	b, _ := json.Marshal(info)
	if !strings.Contains(string(b), `"aws":{"label":"sandbox","account_id":"123456789012"`) {
		t.Fatalf("Info JSON = %s", b)
	}
	if strings.Contains(string(b), fakeNonce) || strings.Contains(string(b), "tok-") {
		t.Fatalf("Info leaks the nonce or token: %s", b)
	}
	if n, tk, bf := hooks.calls(); n != 1 || tk != 1 || bf != 1 {
		t.Fatalf("hooks calls nonce=%d token=%d boxFor=%d, want 1 each", n, tk, bf)
	}
}

// An unbound profile on the same manager keeps today's box key, env and wire
// shape, and never touches the broker.
func TestUnboundSpawnIgnoresTheBroker(t *testing.T) {
	m, boxes, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey != (box.BoxKey{}) {
		t.Fatalf("unbound box key = %+v, want zero", boxes.lastKey)
	}
	if !slices.Contains(boxes.lastEnv, "AWS_ACCESS_KEY_ID=AKIASTATIC") {
		t.Fatal("an unbound session's env must not be rewritten")
	}
	if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
		t.Fatalf("unbound spawn called the broker: nonce=%d token=%d boxFor=%d", n, tk, bf)
	}
	if s.AWS != nil || s.BrokerNonce != "" {
		t.Fatalf("unbound session AWS=%+v nonce=%q", s.AWS, s.BrokerNonce)
	}
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), `"aws"`) {
		t.Fatalf("unbound Info grew an aws key: %s", b)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if hooks.forgotten(s.ID) {
		t.Fatal("an unbound session has no broker state to forget")
	}
}

func TestBoundProfileRefusedOnHostTier(t *testing.T) {
	cases := []struct {
		name     string
		tier     string // the profile's own tier
		override string // the per-spawn runtime
		noBoxes  bool   // containment unavailable on this daemon
	}{
		{"per-spawn host override", config.RuntimeBox, config.RuntimeHost, false},
		{"host-tier profile", config.RuntimeHost, "", false},
		{"containment unavailable", config.RuntimeBox, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, boxes, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, c.tier, time.Minute)
			if c.noBoxes {
				m.SetBoxes(nil)
			}
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir(), Runtime: c.override})
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a bound profile spawned outside a box")
			}
			if !strings.Contains(err.Error(), "AWS binding") || !strings.Contains(err.Error(), "box") {
				t.Fatalf("err = %v, want the AWS-binding/box refusal", err)
			}
			if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
				t.Fatalf("refused spawn called the broker: nonce=%d token=%d boxFor=%d", n, tk, bf)
			}
			if boxes.prepares != 0 || boxes.wraps != 0 {
				t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
			}
			if len(m.List()) != 0 {
				t.Fatal("refused spawn left a session behind")
			}
		})
	}
}

func TestBoundProfileRefusedWithoutBroker(t *testing.T) {
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		binding: testBinding()}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a bound profile spawned with no credential broker")
	}
	if !strings.Contains(err.Error(), "credential broker") {
		t.Fatalf("err = %v, want the credential-broker refusal", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 {
		t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
	}
}

// The binding and the spawn's nonce survive a daemon restart (spec §6, R11.1):
// without the nonce the box's token would stop verifying after a restart.
func TestSidecarRestoresTheBinding(t *testing.T) {
	l := awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		binding: testBinding()}
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	md := sidecarEntry(t, path, s.ID)
	if md.AWS == nil || !reflect.DeepEqual(*md.AWS, *testBinding()) || md.BrokerNonce != fakeNonce {
		t.Fatalf("sidecar entry AWS=%+v nonce=%q", md.AWS, md.BrokerNonce)
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
	if r.AWS == nil || !reflect.DeepEqual(*r.AWS, *testBinding()) {
		t.Fatalf("restored AWS = %+v, want %+v", r.AWS, testBinding())
	}
	if r.BrokerNonce != fakeNonce {
		t.Fatalf("restored nonce = %q, want %q", r.BrokerNonce, fakeNonce)
	}
	if r.Info().AWS == nil || r.Info().AWS.AccountID != "123456789012" {
		t.Fatalf("restored Info.AWS = %+v", r.Info().AWS)
	}
}

// A killed bound session's broker state (cache, stats, throttle) is dropped,
// and so is one pruned after its retention.
func TestRemovedBoundSessionIsForgottenByTheBroker(t *testing.T) {
	t.Run("kill", func(t *testing.T) {
		m, _, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if hooks.forgotten(s.ID) {
			t.Fatal("forgotten while still live")
		}
		if err := m.Kill(s.ID); err != nil {
			t.Fatal(err)
		}
		if !hooks.forgotten(s.ID) {
			t.Fatal("Kill did not forget the session's broker state")
		}
	})
	t.Run("retention prune", func(t *testing.T) {
		m, _, hooks, _ := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, 20*time.Millisecond)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !hooks.forgotten(s.ID) {
			if time.Now().After(deadline) {
				t.Fatal("pruned session was never forgotten")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, ok := m.Get(s.ID); ok {
			t.Fatal("forgotten before it was pruned")
		}
	})
}

// Restart re-spawns through Spawn, so the replacement gets its own nonce and a
// token for its own id; the old session's state is forgotten.
func TestRestartOfABoundSessionGetsAFreshToken(t *testing.T) {
	m, boxes, hooks, _ := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, time.Minute)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if ns.ID == old.ID || ns.BrokerNonce == old.BrokerNonce || ns.BrokerNonce == "" {
		t.Fatalf("restart reused id/nonce: old %s/%s new %s/%s", old.ID, old.BrokerNonce, ns.ID, ns.BrokerNonce)
	}
	want := []string{"AGENTBOX_BROKER_TOKEN=tok-" + ns.ID + "." + ns.BrokerNonce}
	if got := envNamed(boxes.lastEnv, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, want) {
		t.Fatalf("restarted token env = %q, want %q", got, want)
	}
	if !hooks.forgotten(old.ID) {
		t.Fatal("the replaced session's broker state was not forgotten")
	}
}

// A profile that sets its own proxy (or exempts hosts) must not steer a bound
// box: the egress names are stripped, then set exactly once with this spawn's
// token.
func TestBoundSpawnGetsProxyEnvOnce(t *testing.T) {
	m, boxes, _, eg := boundManagerEnv(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute,
		[]string{"HTTPS_PROXY=http://evil:1", "https_proxy=http://evil:1", "NO_PROXY=*", "ALL_PROXY=socks5://evil:1", "CHECKPOINT_DISABLE=0"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	u := "http://" + s.ID + ":etok-" + s.EgressNonce + "@172.21.0.1:9072"
	if got := envNamed(boxes.lastEnv, "HTTPS_PROXY"); !reflect.DeepEqual(got, []string{"HTTPS_PROXY=" + u}) {
		t.Fatalf("HTTPS_PROXY env = %q, want exactly this spawn's", got)
	}
	if got := envNamed(boxes.lastEnv, "https_proxy"); !reflect.DeepEqual(got, []string{"https_proxy=" + u}) {
		t.Fatalf("https_proxy env = %q", got)
	}
	for _, n := range []string{"NO_PROXY", "no_proxy", "ALL_PROXY", "HTTP_PROXY"} {
		if got := envNamed(boxes.lastEnv, n); len(got) != 0 {
			t.Fatalf("%s survived into a bound box: %q", n, got)
		}
	}
	if got := envNamed(boxes.lastEnv, "CHECKPOINT_DISABLE"); !reflect.DeepEqual(got, []string{"CHECKPOINT_DISABLE=1"}) {
		t.Fatalf("CHECKPOINT_DISABLE = %q, want the egress hooks' value only", got)
	}
	if !slices.Contains(boxes.lastEnv, "TERM=xterm") {
		t.Fatal("an unrelated var was lost")
	}
	if eg.spawnCalls() != 1 {
		t.Fatalf("SpawnEnv calls = %d, want 1", eg.spawnCalls())
	}
	// The proxy credential rides the env only, never the argv.
	for _, a := range boxes.lastArgv {
		if strings.Contains(a, "etok-") {
			t.Fatalf("egress token in argv: %q", a)
		}
	}
}

func TestBoundSpawnRefusedWithoutEgress(t *testing.T) {
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		binding: testBinding()}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks) // the broker is up; only egress is missing
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a bound profile spawned with no egress control")
	}
	if !strings.Contains(err.Error(), "egress control is not running") {
		t.Fatalf("err = %v, want the egress refusal", err)
	}
	// The UI shows only ErrBoxUnavailable-class refusals verbatim; any other
	// error becomes "could not start session" with no hint.
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to wrap box.ErrBoxUnavailable so the UI shows it", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 {
		t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
	}
	if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
		t.Fatalf("refused spawn minted broker state: nonce=%d token=%d boxFor=%d", n, tk, bf)
	}
	if len(m.List()) != 0 {
		t.Fatal("refused spawn left a session behind")
	}
}

func TestBoundSpawnEgressErrorRefuses(t *testing.T) {
	m, boxes, _, eg := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	eg.err = errors.New("egress network missing")
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a bound profile spawned although egress was not ready")
	}
	if !strings.Contains(err.Error(), "preparing egress") || !strings.Contains(err.Error(), "egress network missing") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to wrap box.ErrBoxUnavailable so the UI shows the remedy", err)
	}
	if boxes.wraps != 0 {
		t.Fatalf("wraps = %d, want 0", boxes.wraps)
	}
	if len(m.List()) != 0 {
		t.Fatal("refused spawn left a session behind")
	}
}

// D3: an unbound session's env is byte-identical to today's, including a
// profile that sets its own proxy.
func TestUnboundSpawnWithoutEgressAllNeverTouchesEgress(t *testing.T) {
	profile := []string{"HTTPS_PROXY=http://corp:3128", "NO_PROXY=internal", "CHECKPOINT_DISABLE=0"}
	m, boxes, _, eg := boundManagerEnv(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute, profile)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	want := append([]string{"TERM=xterm", "AWS_ACCESS_KEY_ID=AKIASTATIC", "AWS_PROFILE=host-admin", "AGENTBOX_BROKER_TOKEN=forged"}, profile...)
	if !reflect.DeepEqual(boxes.lastEnv, want) {
		t.Fatalf("unbound env changed:\n got %q\nwant %q", boxes.lastEnv, want)
	}
	if eg.spawnCalls() != 0 {
		t.Fatalf("unbound spawn called SpawnEnv %d times", eg.spawnCalls())
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if len(eg.forgot) != 0 {
		t.Fatalf("an unbound session has no egress state to forget: %v", eg.forgot)
	}
}

// Forget runs on every way a session ends, and only once Lookup would already
// refuse it (the Task 7 ordering): a CONNECT racing the end finds no session.
func TestBoundSessionGoneForgetsEgress(t *testing.T) {
	t.Run("kill", func(t *testing.T) {
		m, _, _, eg := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
		eg.liveAtForget = func(id string) bool { _, ok := m.Get(id); return ok }
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if eg.forgotten(s.ID) {
			t.Fatal("forgotten while still live")
		}
		if err := m.Kill(s.ID); err != nil {
			t.Fatal(err)
		}
		if !eg.forgotten(s.ID) {
			t.Fatal("Kill did not forget the session at the proxy")
		}
		if slices.Contains(eg.wasLive, true) {
			t.Fatal("Forget ran while the session was still registered")
		}
	})
	t.Run("retention prune", func(t *testing.T) {
		m, _, _, eg := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, 20*time.Millisecond)
		eg.liveAtForget = func(id string) bool { _, ok := m.Get(id); return ok }
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !eg.forgotten(s.ID) {
			if time.Now().After(deadline) {
				t.Fatal("pruned session was never forgotten")
			}
			time.Sleep(10 * time.Millisecond)
		}
		eg.mu.Lock()
		defer eg.mu.Unlock()
		if slices.Contains(eg.wasLive, true) {
			t.Fatal("Forget ran while the session was still registered")
		}
	})
	t.Run("restart", func(t *testing.T) {
		m, _, _, eg := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, time.Minute)
		old := exitedSession(t, m, launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
		ns, err := m.Restart(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(ns.ID) })
		if !eg.forgotten(old.ID) {
			t.Fatal("the replaced session was not forgotten at the proxy")
		}
	})
}

func TestInfoNeverCarriesTheEgressToken(t *testing.T) {
	m, _, _, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), "etok-") || strings.Contains(string(b), "@172.21.0.1") {
		t.Fatalf("Info leaks the egress token or proxy URL: %s", b)
	}
}

// Task 5: every Linux boxed session (egressAll) gets its own egress nonce and
// the proxy env, bound or not. An unbound one is "open": the proxy admits any
// host but still authenticates the session.
func TestUnboundSpawnGetsOpenEgressEnv(t *testing.T) {
	profile := []string{"HTTPS_PROXY=http://corp:3128", "NO_PROXY=internal"}
	m, boxes, eg := unboundEgressManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, profile)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.EgressNonce == "" {
		t.Fatal("no egress nonce")
	}
	u := "http://" + s.ID + ":etok-" + s.EgressNonce + "@172.21.0.1:9072"
	if got := envNamed(boxes.lastEnv, "HTTPS_PROXY"); !reflect.DeepEqual(got, []string{"HTTPS_PROXY=" + u}) {
		t.Fatalf("HTTPS_PROXY env = %q, want exactly this spawn's", got)
	}
	if got := envNamed(boxes.lastEnv, "NO_PROXY"); len(got) != 0 {
		t.Fatalf("the profile's NO_PROXY survived: %q", got)
	}
	if !slices.Contains(boxes.lastEnv, "TERM=xterm") {
		t.Fatal("an unrelated var was lost")
	}
	if eg.spawnCalls() != 1 {
		t.Fatalf("SpawnEnv calls = %d, want 1", eg.spawnCalls())
	}
	if got := s.Info().Egress; got != "open" {
		t.Fatalf("Info.Egress = %q, want open", got)
	}
	// No broker token is minted for an unbound box; the profile's forged one is
	// still stripped only for bound spawns, so the count of names must not grow.
	if got := envNamed(boxes.lastEnv, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, []string{"AGENTBOX_BROKER_TOKEN=forged"}) {
		t.Fatalf("broker token env changed for an unbound spawn: %q", got)
	}
}

func TestUnboundBoxedSpawnRefusedWithoutEgress(t *testing.T) {
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}},
		nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetEgressAll(true)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("a boxed spawn started with no egress control")
	}
	if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "egress control is not running") {
		t.Fatalf("err = %v, want an ErrBoxUnavailable egress refusal", err)
	}
	if boxes.prepares != 0 || boxes.wraps != 0 {
		t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
	}
	if len(m.List()) != 0 {
		t.Fatal("refused spawn left a session behind")
	}
}

func TestBoundSpawnEgressNonceIsNotTheAWSNonce(t *testing.T) {
	m, boxes, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.EgressNonce == "" || s.EgressNonce == s.BrokerNonce {
		t.Fatalf("EgressNonce=%q BrokerNonce=%q: must be set and distinct", s.EgressNonce, s.BrokerNonce)
	}
	if got := envNamed(boxes.lastEnv, "HTTPS_PROXY"); len(got) != 1 || !strings.Contains(got[0], "etok-"+s.EgressNonce+"@") {
		t.Fatalf("HTTPS_PROXY = %q, want the egress nonce", got)
	}
	if got := envNamed(boxes.lastEnv, brokerTokenEnv); len(got) != 1 || got[0] != brokerTokenEnv+"="+hooks.Token(s.ID, s.BrokerNonce) {
		t.Fatalf("broker token = %q, want one carrying BrokerNonce", got)
	}
	if got := s.Info().Egress; got != "allowlist" {
		t.Fatalf("Info.Egress = %q, want allowlist", got)
	}
}

func TestHostTierSpawnGetsNoEgress(t *testing.T) {
	m, _, eg := unboundEgressManager(t, []string{"sh", "-c", `printf '<%s>\n' "$HTTPS_PROXY"; sleep 30`},
		config.RuntimeHost, []string{"HTTPS_PROXY=http://corp:3128"})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	// The profile's own proxy reaches the host process untouched.
	out, cancel, _ := s.Subscribe()
	defer cancel()
	var b strings.Builder
	deadline := time.After(3 * time.Second)
	for !strings.Contains(b.String(), ">") {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatalf("session ended; output %q", b.String())
			}
			b.WriteString(string(chunk))
		case <-deadline:
			t.Fatalf("no output; got %q", b.String())
		}
	}
	if !strings.Contains(b.String(), "<http://corp:3128>") {
		t.Fatalf("host session's HTTPS_PROXY: output %q, want the profile's http://corp:3128", b.String())
	}
	if eg.spawnCalls() != 0 {
		t.Fatalf("host spawn called SpawnEnv %d times", eg.spawnCalls())
	}
	if s.EgressNonce != "" || s.Info().Egress != "" {
		t.Fatalf("host session has egress state: nonce=%q egress=%q", s.EgressNonce, s.Info().Egress)
	}
}

func TestSidecarRestoresEgressNonce(t *testing.T) {
	l := awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}}
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetEgress(&fakeEgressHooks{})
	m.SetEgressAll(true)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if md := sidecarEntry(t, path, s.ID); md.EgressNonce == "" || md.EgressNonce != s.EgressNonce {
		t.Fatalf("sidecar egress nonce = %q, want %q", md.EgressNonce, s.EgressNonce)
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
	if r.EgressNonce != s.EgressNonce {
		t.Fatalf("restored egress nonce = %q, want %q", r.EgressNonce, s.EgressNonce)
	}
	if r.Info().Egress != "open" {
		t.Fatalf("restored Info.Egress = %q", r.Info().Egress)
	}
}

// Spec correction 7: a bound session written before A1 minted its proxy token
// over BrokerNonce, so a restore with no egress_nonce adopts it.
func TestPreA1BoundSidecarBackfillsEgressNonce(t *testing.T) {
	l := awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: testBinding()}
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	// Rewrite the record the way a pre-A1 daemon left it: no egress_nonce.
	md := sidecarEntry(t, path, s.ID)
	md.EgressNonce = ""
	m.sidecar.put(s.ID, md)
	if got := sidecarEntry(t, path, s.ID); got.EgressNonce != "" || got.BrokerNonce == "" {
		t.Fatalf("setup: record = %+v", got)
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
	if r.EgressNonce == "" || r.EgressNonce != r.BrokerNonce {
		t.Fatalf("EgressNonce=%q BrokerNonce=%q, want the backfill", r.EgressNonce, r.BrokerNonce)
	}
	if r.Info().Egress != "allowlist" {
		t.Fatalf("Info.Egress = %q", r.Info().Egress)
	}
}

func TestUnboundSessionGoneForgetsEgress(t *testing.T) {
	m, _, eg := unboundEgressManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if !eg.forgotten(s.ID) {
		t.Fatal("Kill did not forget the unbound session at the proxy")
	}
}

func TestInfoNeverCarriesTheEgressNonce(t *testing.T) {
	m, _, _ := unboundEgressManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, nil)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), s.EgressNonce) || strings.Contains(string(b), "etok-") {
		t.Fatalf("Info leaks the egress nonce: %s", b)
	}
}

func elevTestBinding() *config.AWSBinding {
	b := testBinding()
	b.Elevate = &config.AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write"}
	return b
}

func elevLauncher() awsLauncher {
	return awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: elevTestBinding()}
}

func TestElevatableSpawnGetsItsOwnBoxAndIsExclusive(t *testing.T) {
	m, boxes, _, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	m.launcher = elevLauncher()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey.Session != s.ID || boxes.lastKey.Binding != elevTestBinding().Hash() || !s.Exclusive {
		t.Fatalf("key %+v exclusive %v", boxes.lastKey, s.Exclusive)
	}
}

func TestPlainBoundSpawnIsNotExclusive(t *testing.T) {
	m, boxes, _, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if boxes.lastKey.Session != "" || s.Exclusive {
		t.Fatalf("a binding without elevate shares boxes as before: %+v", boxes.lastKey)
	}
}

func TestWorkspaceHeldReachesTheUserVerbatim(t *testing.T) {
	m, boxes, _, _ := boundManager(t, []string{"sh"}, config.RuntimeBox, time.Minute)
	msg := "/w/x is in use by an AWS-write-capable session (or another session sits in this folder). Use a separate folder or worktree, or stop the other session."
	boxes.wrapErr = fmt.Errorf("%w: %s", box.ErrWorkspaceHeld, msg)
	_, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if !errors.Is(err, box.ErrWorkspaceHeld) || strings.Contains(err.Error(), "run on the host") || strings.Count(err.Error(), msg) != 1 {
		t.Fatalf("got %v", err)
	}
}

// The exclusive flag rides the sidecar so a restart re-marks the box (it
// would otherwise be treated as shared, idling for the TTL instead of being
// removed with its session) and the session keeps its identity. A record that
// predates the field reads as false.
func TestSidecarRestoresExclusiveAndMarksTheBox(t *testing.T) {
	l := elevLauncher()
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	raw, err := os.ReadFile(path)
	if err != nil || !regexp.MustCompile(`"exclusive":\s*true`).Match(raw) {
		t.Fatalf("sidecar lacks exclusive true: %v\n%s", err, raw)
	}

	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	boxes2 := &fakeBoxes{}
	m2.SetBoxes(boxes2)
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	if !r.Exclusive {
		t.Fatal("restored session lost Exclusive")
	}
	if !reflect.DeepEqual(boxes2.marked, []string{"ab-box-test"}) {
		t.Fatalf("marked = %v, want the session's box", boxes2.marked)
	}
}

func TestPlainSidecarRecordIsNotExclusive(t *testing.T) {
	var md sessionMeta
	if err := json.Unmarshal([]byte(`{"agent":"aws","runtime":"box","box":"ab-box-x"}`), &md); err != nil || md.Exclusive {
		t.Fatalf("old record: err=%v exclusive=%v", err, md.Exclusive)
	}
}

func TestApplyAWSState(t *testing.T) {
	until := time.Unix(1_800_003_600, 0)
	cases := []struct {
		name          string
		need          string
		until         time.Time
		sso, wantNeed string
	}{
		{"nothing", "", time.Time{}, "", ""},
		{"sso on a quiet session", "", time.Time{}, "example-sso", "sso"},
		{"sso beats turn", "turn", time.Time{}, "example-sso", "sso"},
		{"ask beats sso", "ask", time.Time{}, "example-sso", "ask"},
		{"elevated only", "", until, "", ""},
	}
	for _, c := range cases {
		in := Info{Activity: "working", Need: c.need, AWS: &AWSInfo{Label: "sandbox"}}
		applyAWSState(&in, awsState{until: c.until, sso: c.sso})
		if in.Need != c.wantNeed || in.Activity != "working" || in.AWS.SSOExpired != c.sso {
			t.Errorf("%s: need=%q activity=%q sso=%q", c.name, in.Need, in.Activity, in.AWS.SSOExpired)
		}
		if c.until.IsZero() != (in.AWS.ElevatedUntil == nil) || (in.AWS.ElevatedUntil != nil && !in.AWS.ElevatedUntil.Equal(c.until)) {
			t.Errorf("%s: until %v", c.name, in.AWS.ElevatedUntil)
		}
	}
	gone := Info{Exited: true, AWS: &AWSInfo{Label: "sandbox"}}
	applyAWSState(&gone, awsState{until: until})
	if gone.AWS.ElevatedUntil != nil {
		t.Fatalf("an exited session must not show a write window: %v", gone.AWS.ElevatedUntil)
	}
	plain := Info{Need: "turn"}
	applyAWSState(&plain, awsState{until: until, sso: "example-sso"}) // no binding: nothing to apply
	if plain.Need != "turn" || plain.AWS != nil {
		t.Fatalf("an unbound session is untouched: %+v", plain)
	}
}

// A connection session: the window shows on its connection only, and each
// connection carries its own expired sign-in.
func TestApplyAWSStatePerConnection(t *testing.T) {
	until := time.Unix(1_800_003_600, 0)
	in := Info{Need: "turn", AWSConnections: []AWSConnInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	applyAWSState(&in, awsState{until: until, conn: "b", sso: "sso-a", conns: map[string]string{"a": "sso-a"}})
	a, b, c := in.AWSConnections[0], in.AWSConnections[1], in.AWSConnections[2]
	if a.ElevatedUntil != nil || b.ElevatedUntil == nil || !b.ElevatedUntil.Equal(until) || c.ElevatedUntil != nil {
		t.Fatalf("elevated_until: a=%v b=%v c=%v", a.ElevatedUntil, b.ElevatedUntil, c.ElevatedUntil)
	}
	if a.SSOExpired != "sso-a" || b.SSOExpired != "" || c.SSOExpired != "" || in.Need != "sso" || in.AWS != nil {
		t.Fatalf("sso: %+v need %q", in.AWSConnections, in.Need)
	}
	quiet := Info{Need: "turn", Exited: true, AWSConnections: []AWSConnInfo{{Name: "a"}}}
	applyAWSState(&quiet, awsState{until: until, conn: "a"})
	if quiet.AWSConnections[0].ElevatedUntil != nil || quiet.Need != "turn" {
		t.Fatalf("an exited session shows no window and a quiet one keeps its need: %+v", quiet)
	}
}

func TestListCarriesTheBrokerState(t *testing.T) {
	m, _, hooks, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	hooks.setState(s.ID, time.Unix(1_800_003_600, 0), "example-sso")
	var got Info
	for _, i := range m.List() {
		if i.ID == s.ID {
			got = i
		}
	}
	if got.Need != "sso" || got.AWS.SSOExpired != "example-sso" || got.AWS.ElevatedUntil == nil {
		t.Fatalf("List: %+v aws=%+v", got, got.AWS)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"sso_expired":"example-sso"`) || !strings.Contains(string(b), `"elevated_until":`) {
		t.Fatalf("wire: %s", b)
	}
}

func TestInfoCarriesTheElevateBlockButNoSourceProfile(t *testing.T) {
	m, _, _, _ := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
	m.launcher = awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}, binding: elevTestBinding()}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	in := s.Info()
	if in.AWS.Elevate == nil || in.AWS.Elevate.RoleARN != elevTestBinding().Elevate.RoleARN || in.AWS.Elevate.MaxMinutes != 60 {
		t.Fatalf("elevate %+v", in.AWS.Elevate)
	}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "sandbox-admin") || strings.Contains(string(b), "source_profile") {
		t.Fatalf("Info names how it authenticates: %s", b)
	}
}
