package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/launcher"
	"github.com/culiops/culibox/internal/secretstore"
)

// ghLauncher adds GitHub bindings to awsLauncher: profile "gh" is GitHub-only,
// "both" carries both bindings, "aws" stays AWS-only.
type ghLauncher struct {
	awsLauncher
	gh *config.GitHubBinding
}

func (l ghLauncher) AWSFor(agent string) *config.AWSBinding {
	if agent == "both" {
		return l.awsLauncher.AWSFor("aws")
	}
	return l.awsLauncher.AWSFor(agent)
}

func (l ghLauncher) GitHubFor(agent string) *config.GitHubBinding {
	if (agent != "gh" && agent != "both") || l.gh == nil {
		return nil
	}
	return l.gh.Clone()
}

func testGitHub() *config.GitHubBinding {
	return &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "acme", App: "main", Repos: []string{"api", "web"}},
		{Owner: "other", App: "main", Repos: []string{"lib"}},
	}}
}

// The profile also sets its own GitHub variables (as a secret would): an
// AWS-only session must keep them, a GitHub-bound one must lose them.
func ghLauncherFor(runtime string) ghLauncher {
	return ghLauncher{awsLauncher: awsLauncher{tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: runtime},
		binding: testBinding(), extraEnv: []string{"GH_TOKEN=ghp_profile", "GIT_TERMINAL_PROMPT=1"}}, gh: testGitHub()}
}

var etokRe = regexp.MustCompile(`etok-[0-9a-f]+@`)

func ghManager(t *testing.T, runtime string) (*Manager, *fakeBoxes, *fakeBrokerHooks, *fakeEgressHooks) {
	t.Helper()
	m := NewManager(ghLauncherFor(runtime), nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	hooks := &fakeBrokerHooks{}
	m.SetBroker(hooks)
	eg := &fakeEgressHooks{}
	m.SetEgress(eg)
	return m, boxes, hooks, eg
}

func TestSpawnGitHubOnlyIsBound(t *testing.T) {
	m, boxes, hooks, eg := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })

	g := testGitHub()
	if want := (config.Bindings{GitHub: g}).Hash(); boxes.lastKey.Binding != want || want == "" {
		t.Fatalf("box key binding = %q, want %q", boxes.lastKey.Binding, want)
	}
	if boxes.lastKey.Session != "" {
		t.Fatalf("a GitHub binding shares a box per scope, got Session=%q", boxes.lastKey.Session)
	}
	if hooks.lastBS.AWS != nil || hooks.lastBS.GitHub == nil || !reflect.DeepEqual(*hooks.lastBS.GitHub, *g) {
		t.Fatalf("BoxFor got %+v", hooks.lastBS)
	}
	if hooks.checkCalls != 1 {
		t.Fatalf("CheckGitHub calls = %d, want 1", hooks.checkCalls)
	}
	env := boxes.lastEnv
	if got := envNamed(env, "AGENTBOX_BROKER_TOKEN"); !reflect.DeepEqual(got, []string{"AGENTBOX_BROKER_TOKEN=tok-" + s.ID + "." + fakeNonce}) {
		t.Fatalf("token env = %q", got)
	}
	if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || !slices.Contains(env, "AGENTBOX_GH_OWNERS=acme,other") {
		t.Fatalf("env %q lacks the GitHub helper env", env)
	}
	if got := envNamed(env, "HTTPS_PROXY"); len(got) != 1 {
		t.Fatalf("egress env missing: %q", env)
	}
	if got := envNamed(env, "AWS_ACCESS_KEY_ID"); len(got) != 0 {
		t.Fatalf("static key reached a bound box: %q", got)
	}
	if !slices.Contains(boxes.lastRO, box.Mount{Host: "/state/broker/binding-x", Box: "/opt/agentbox"}) {
		t.Fatalf("extraRO %+v lacks the binding dir", boxes.lastRO)
	}
	if eg.spawnCalls() != 1 {
		t.Fatalf("egress SpawnEnv calls = %d", eg.spawnCalls())
	}
	if s.AWS != nil || s.GitHub == nil || !reflect.DeepEqual(*s.GitHub, *g) || s.BrokerNonce != fakeNonce || s.EgressNonce == "" {
		t.Fatalf("session AWS=%+v GitHub=%+v nonce=%q egress=%q", s.AWS, s.GitHub, s.BrokerNonce, s.EgressNonce)
	}
	if !reflect.DeepEqual(s.Bindings(), config.Bindings{GitHub: s.GitHub}) {
		t.Fatalf("Bindings() = %+v", s.Bindings())
	}
	if s.Info().Egress != "allowlist" {
		t.Fatalf("Info.Egress = %q", s.Info().Egress)
	}
}

func TestSpawnGitHubRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *Manager)
		spec  launcher.SpawnSpec
		agent string
		want  string
	}{
		{"host tier", func(*Manager) {}, launcher.SpawnSpec{Runtime: config.RuntimeHost}, "gh", "a GitHub binding"},
		{"no broker", func(m *Manager) { m.SetBroker(nil) }, launcher.SpawnSpec{}, "gh", "a GitHub binding"},
		{"no egress", func(m *Manager) { m.SetEgress(nil) }, launcher.SpawnSpec{}, "gh", "a GitHub binding"},
		{"both kinds", func(m *Manager) { m.SetEgress(nil) }, launcher.SpawnSpec{}, "both", "AWS and GitHub bindings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, boxes, hooks, _ := ghManager(t, config.RuntimeBox)
			c.setup(m)
			c.spec.Agent, c.spec.Cwd = c.agent, t.TempDir()
			s, err := m.Spawn(c.spec)
			if err == nil {
				_ = m.Kill(s.ID)
				t.Fatal("a GitHub-bound profile spawned")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to name %q", err, c.want)
			}
			if n, tk, bf := hooks.calls(); n+tk+bf != 0 {
				t.Fatalf("refused spawn called the broker: nonce=%d token=%d boxFor=%d", n, tk, bf)
			}
			if boxes.prepares != 0 || boxes.wraps != 0 {
				t.Fatalf("refused spawn touched the box layer: prepares=%d wraps=%d", boxes.prepares, boxes.wraps)
			}
		})
	}
}

func TestSpawnAWSRefusalNamesAWS(t *testing.T) {
	m, _, _, _ := ghManager(t, config.RuntimeBox)
	m.SetBroker(nil)
	if _, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "an AWS binding") {
		t.Fatalf("err = %v, want it to name an AWS binding", err)
	}
}

func TestSpawnGitHubCheckFails(t *testing.T) {
	m, boxes, hooks, eg := ghManager(t, config.RuntimeBox)
	hooks.checkErr = errors.New(`GitHub App "main" is not installed on acme`)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawn survived a failing GitHub check")
	}
	if !strings.Contains(err.Error(), `GitHub App "main" is not installed on acme`) {
		t.Fatalf("err = %v, want the check's message", err)
	}
	if _, tk, bf := hooks.calls(); tk+bf != 0 {
		t.Fatalf("broker used after a failed check: token=%d boxFor=%d", tk, bf)
	}
	if eg.spawnCalls() != 0 || boxes.wraps != 0 || len(m.List()) != 0 {
		t.Fatalf("failed check left state: egress=%d wraps=%d sessions=%d", eg.spawnCalls(), boxes.wraps, len(m.List()))
	}
}

// A GitHub App key that lives in the locked store refuses the spawn as
// ErrStoreLocked (so the API answers 423 store_locked), not as a generic
// box-unavailable refusal.
func TestSpawnGitHubCheckLockedStore(t *testing.T) {
	m, _, hooks, eg := ghManager(t, config.RuntimeBox)
	hooks.checkErr = fmt.Errorf("GitHub App %q: %w", "main", credref.ErrLocked)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawn survived a locked store")
	}
	if !errors.Is(err, launcher.ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreLocked", err)
	}
	if errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("a locked store is not a containment failure: %v", err)
	}
	if eg.spawnCalls() != 0 || len(m.List()) != 0 {
		t.Fatalf("locked refusal left state: egress=%d sessions=%d", eg.spawnCalls(), len(m.List()))
	}
}

func TestSpawnAWSOnlyUnchanged(t *testing.T) {
	m, boxes, hooks, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "aws", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if want := testBinding().Hash(); boxes.lastKey.Binding != want {
		t.Fatalf("AWS-only box key binding = %q, want the AWS hash %q", boxes.lastKey.Binding, want)
	}
	if hooks.checkCalls != 0 || hooks.lastBS.GitHub != nil {
		t.Fatalf("an AWS-only spawn touched GitHub: checks=%d bs=%+v", hooks.checkCalls, hooks.lastBS)
	}
	// Literals captured from the pre-N1 code (main 3ecdc9e, same fixture, the
	// real StripEnv list): the whole env and mounts, byte for byte. The profile's
	// own GH_TOKEN and GIT_TERMINAL_PROMPT survive an AWS-only session. The only
	// normalised part is the egress token's random nonce.
	wantEnv := []string{"TERM=xterm", "GH_TOKEN=ghp_profile", "GIT_TERMINAL_PROMPT=1", "AWS_PROFILE=agentbox",
		"AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock",
		"HTTPS_PROXY=http://aws-1:etok-N@172.21.0.1:9072", "https_proxy=http://aws-1:etok-N@172.21.0.1:9072",
		"CHECKPOINT_DISABLE=1",
		"AGENTBOX_BROKER_TOKEN=tok-aws-1.0123456789abcdef0123456789abcdef"}
	gotEnv := slices.Clone(boxes.lastEnv)
	for i, kv := range gotEnv {
		gotEnv[i] = etokRe.ReplaceAllString(kv, "etok-N@")
	}
	if !slices.Equal(gotEnv, wantEnv) {
		t.Fatalf("AWS-only env changed:\n got %#v\nwant %#v", gotEnv, wantEnv)
	}
	if want := []box.Mount{{Host: "/state/broker/binding-x", Box: "/opt/agentbox"}}; !slices.Equal(boxes.lastRO, want) {
		t.Fatalf("AWS-only mounts = %#v, want %#v", boxes.lastRO, want)
	}
	if boxes.lastKey.Session != "" || boxes.lastKey.Account != "" || boxes.lastKey.Binding != "1130ffad2979" {
		t.Fatalf("AWS-only box key = %+v", boxes.lastKey)
	}
	if s.GitHub != nil || s.Info().GitHub != nil {
		t.Fatal("an AWS-only session grew a GitHub view")
	}
	b, _ := json.Marshal(s.Info())
	if strings.Contains(string(b), `"github"`) {
		t.Fatalf("AWS-only Info grew a github key: %s", b)
	}
}

// The mirror of the guard above: a GitHub-bound session loses the profile's own
// GitHub variables, and gets the broker's instead.
func TestSpawnGitHubBoundStripsInheritedGitHubEnv(t *testing.T) {
	m, boxes, _, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	env := boxes.lastEnv
	if got := envNamed(env, "GH_TOKEN"); len(got) != 0 {
		t.Fatalf("a GitHub-bound box kept the profile's token: %q", got)
	}
	if got := envNamed(env, "GIT_TERMINAL_PROMPT"); !reflect.DeepEqual(got, []string{"GIT_TERMINAL_PROMPT=0"}) {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want only the broker's", got)
	}
}

func TestSidecarRestoresGitHubBindingAndNonce(t *testing.T) {
	l := ghLauncherFor(config.RuntimeBox)
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	md := sidecarEntry(t, path, s.ID)
	if md.GitHub == nil || !reflect.DeepEqual(*md.GitHub, *testGitHub()) || md.BrokerNonce != fakeNonce {
		t.Fatalf("sidecar entry GitHub=%+v nonce=%q", md.GitHub, md.BrokerNonce)
	}
	raw, _ := json.Marshal(md)
	if !strings.Contains(string(raw), `"aws_nonce":"`+fakeNonce+`"`) || !strings.Contains(string(raw), `"github":`) {
		t.Fatalf("sidecar JSON keys changed: %s", raw)
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
	if r.GitHub == nil || !reflect.DeepEqual(*r.GitHub, *testGitHub()) || r.BrokerNonce != fakeNonce {
		t.Fatalf("restored GitHub=%+v nonce=%q", r.GitHub, r.BrokerNonce)
	}
	if r.Info().Egress != "allowlist" || r.Info().GitHub == nil {
		t.Fatalf("restored Info = %+v", r.Info())
	}
}

// A record written before GitHub bindings carries only aws_nonce and aws.
func TestSidecarOldRecordRestoresBrokerNonce(t *testing.T) {
	var md sessionMeta
	old := `{"agent":"aws","created":"2026-10-01T00:00:00Z","runtime":"box","aws":{"source_profile":"p","role_arn":"arn:aws:iam::226198813800:role/a","region":"ap-southeast-1"},"aws_nonce":"n1"}`
	if err := json.Unmarshal([]byte(old), &md); err != nil {
		t.Fatal(err)
	}
	if md.BrokerNonce != "n1" || md.AWS == nil || md.GitHub != nil {
		t.Fatalf("old record decoded to %+v", md)
	}
}

func TestInfoGitHub(t *testing.T) {
	m, _, _, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	in := s.Info()
	if want := (&GitHubInfo{Owners: []string{"acme", "other"}, Repos: 3}); !reflect.DeepEqual(in.GitHub, want) {
		t.Fatalf("Info.GitHub = %+v, want %+v", in.GitHub, want)
	}
	if in.Egress != "allowlist" || in.AWS != nil {
		t.Fatalf("Info egress=%q aws=%+v", in.Egress, in.AWS)
	}
	b, _ := json.Marshal(in)
	if !strings.Contains(string(b), `"github":{"owners":["acme","other"],"repos":3}`) {
		t.Fatalf("Info JSON = %s", b)
	}
	if strings.Contains(string(b), fakeNonce) || strings.Contains(string(b), "tok-") {
		t.Fatalf("Info leaks the nonce or token: %s", b)
	}
}

func TestKilledGitHubSessionIsForgottenByTheBroker(t *testing.T) {
	m, _, hooks, _ := ghManager(t, config.RuntimeBox)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill(s.ID); err != nil {
		t.Fatal(err)
	}
	if !hooks.forgotten(s.ID) {
		t.Fatal("a GitHub-bound session's broker state was not forgotten")
	}
}

// A GitHub App key behind a failing Keychain refuses the spawn as
// ErrStoreUnavailable (503 store_unavailable), not as a locked store (423) and
// not as a containment failure.
func TestSpawnGitHubCheckUnavailableStore(t *testing.T) {
	m, _, hooks, eg := ghManager(t, config.RuntimeBox)
	ue := &secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "denied"}
	hooks.checkErr = fmt.Errorf("GitHub App %q: %w", "main", ue)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err == nil {
		_ = m.Kill(s.ID)
		t.Fatal("spawn survived an unavailable Keychain")
	}
	var got *secretstore.UnavailableError
	if !errors.Is(err, launcher.ErrStoreUnavailable) || !errors.As(err, &got) {
		t.Fatalf("err = %v, want ErrStoreUnavailable wrapping the UnavailableError", err)
	}
	if errors.Is(err, launcher.ErrStoreLocked) || errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("not a locked store nor a containment failure: %v", err)
	}
	if eg.spawnCalls() != 0 || len(m.List()) != 0 {
		t.Fatalf("refusal left state: egress=%d sessions=%d", eg.spawnCalls(), len(m.List()))
	}
}
