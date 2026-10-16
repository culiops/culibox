package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// awsLauncher is a tieredLauncher whose profile "aws" carries an AWS binding
// and whose built env carries what a careless profile might set: static keys
// and its own AWS profile selector.
type awsLauncher struct {
	tieredLauncher
	binding *config.AWSBinding
}

func (l awsLauncher) Build(launcher.SpawnSpec) ([]string, []string, error) {
	return l.argv, []string{"TERM=xterm", "AWS_ACCESS_KEY_ID=AKIASTATIC", "AWS_PROFILE=host-admin",
		"AGENTBOX_BROKER_TOKEN=forged"}, nil
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
	return &config.AWSBinding{SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent",
		Region: "ap-southeast-1", S3Read: []string{"culishop-tfstate"}, Label: "sandbox"}
}

const fakeNonce = "0123456789abcdef0123456789abcdef"

type fakeBrokerHooks struct {
	mu          sync.Mutex
	nonces      int
	tokens      int
	boxForCalls int
	forgot      []string
	next        int
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

func (f *fakeBrokerHooks) BoxFor(config.AWSBinding) ([]box.Mount, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxForCalls++
	return []box.Mount{{Host: "/state/broker/binding-x", Box: "/opt/agentbox"}},
		[]string{"AWS_PROFILE=agentbox", "AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock"}, nil
}

func (f *fakeBrokerHooks) StripEnv() []string {
	return []string{"AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN"}
}

func (f *fakeBrokerHooks) Forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
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

func boundManager(t *testing.T, argv []string, runtime string, retention time.Duration) (*Manager, *fakeBoxes, *fakeBrokerHooks) {
	t.Helper()
	m := NewManager(awsLauncher{tieredLauncher: tieredLauncher{argv: argv, runtime: runtime}, binding: testBinding()},
		nil, 1<<16, retention)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	return m, boxes, hooks
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
	m, boxes, hooks := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
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
	if s.AWS == nil || !reflect.DeepEqual(*s.AWS, *testBinding()) || s.AWSNonce != fakeNonce {
		t.Fatalf("session AWS=%+v nonce=%q", s.AWS, s.AWSNonce)
	}
	info := s.Info()
	want := &AWSInfo{Label: "sandbox", AccountID: "226198813800", RoleARN: testBinding().RoleARN, Prod: false}
	if !reflect.DeepEqual(info.AWS, want) {
		t.Fatalf("Info.AWS = %+v, want %+v", info.AWS, want)
	}
	b, _ := json.Marshal(info)
	if !strings.Contains(string(b), `"aws":{"label":"sandbox","account_id":"226198813800"`) {
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
	m, boxes, hooks := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
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
	if s.AWS != nil || s.AWSNonce != "" {
		t.Fatalf("unbound session AWS=%+v nonce=%q", s.AWS, s.AWSNonce)
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
			m, boxes, hooks := boundManager(t, []string{"sh", "-c", "sleep 30"}, c.tier, time.Minute)
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
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	md := sidecarEntry(t, path, s.ID)
	if md.AWS == nil || !reflect.DeepEqual(*md.AWS, *testBinding()) || md.AWSNonce != fakeNonce {
		t.Fatalf("sidecar entry AWS=%+v nonce=%q", md.AWS, md.AWSNonce)
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
	if r.AWSNonce != fakeNonce {
		t.Fatalf("restored nonce = %q, want %q", r.AWSNonce, fakeNonce)
	}
	if r.Info().AWS == nil || r.Info().AWS.AccountID != "226198813800" {
		t.Fatalf("restored Info.AWS = %+v", r.Info().AWS)
	}
}

// A killed bound session's broker state (cache, stats, throttle) is dropped,
// and so is one pruned after its retention.
func TestRemovedBoundSessionIsForgottenByTheBroker(t *testing.T) {
	t.Run("kill", func(t *testing.T) {
		m, _, hooks := boundManager(t, []string{"sh", "-c", "sleep 30"}, config.RuntimeBox, time.Minute)
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
		m, _, hooks := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, 20*time.Millisecond)
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
	m, boxes, hooks := boundManager(t, []string{"sh", "-c", "exit 0"}, config.RuntimeBox, time.Minute)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if ns.ID == old.ID || ns.AWSNonce == old.AWSNonce || ns.AWSNonce == "" {
		t.Fatalf("restart reused id/nonce: old %s/%s new %s/%s", old.ID, old.AWSNonce, ns.ID, ns.AWSNonce)
	}
	want := []string{"AGENTBOX_BROKER_TOKEN=tok-" + ns.ID + "." + ns.AWSNonce}
	if got := envNamed(boxes.lastEnv, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, want) {
		t.Fatalf("restarted token env = %q, want %q", got, want)
	}
	if !hooks.forgotten(old.ID) {
		t.Fatal("the replaced session's broker state was not forgotten")
	}
}
