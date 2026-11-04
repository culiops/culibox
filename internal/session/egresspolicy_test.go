package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// egLauncher gives the GitHub-bound profile "gh" a network policy.
type egLauncher struct {
	ghLauncher
	allow []string
	open  bool
}

func (l egLauncher) EgressFor(agent string) ([]string, bool) {
	if agent == "gh" || agent == "plain" {
		return append([]string(nil), l.allow...), l.open
	}
	return nil, false
}

func TestSpawnCopiesEgressPolicy(t *testing.T) {
	l := egLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox), allow: []string{"pkg.example"}, open: true}
	path := filepath.Join(t.TempDir(), "sessions.json")
	m := NewManager(l, newFakeBackend(), 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if !reflect.DeepEqual(s.EgressAllow, []string{"pkg.example"}) || !s.EgressOpen {
		t.Fatalf("session policy = %v %v", s.EgressAllow, s.EgressOpen)
	}
	md := sidecarEntry(t, path, s.ID)
	if !reflect.DeepEqual(md.EgressAllow, []string{"pkg.example"}) || !md.EgressOpen {
		t.Fatalf("sidecar policy = %v %v", md.EgressAllow, md.EgressOpen)
	}
	want := (config.Bindings{GitHub: testGitHub(), EgressAllow: []string{"pkg.example"}, EgressOpen: true}).Hash()
	if boxes.lastKey.Binding != want || want == (config.Bindings{GitHub: testGitHub()}).Hash() {
		t.Fatalf("box key binding = %q, want %q (and not the policy-free hash)", boxes.lastKey.Binding, want)
	}
	if !reflect.DeepEqual(s.Bindings().EgressAllow, []string{"pkg.example"}) || !s.Bindings().EgressOpen {
		t.Fatalf("Bindings() = %+v", s.Bindings())
	}
}

// A profile without a binding is open already: its policy is dropped, so it
// neither shows on the session nor changes the box key.
func TestSpawnUnboundDropsEgressPolicy(t *testing.T) {
	l := egLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox), allow: []string{"pkg.example"}, open: true}
	m := NewManager(l, nil, 1<<16, time.Minute)
	m.SetBoxes(&fakeBoxes{})
	m.SetBroker(&fakeBrokerHooks{})
	m.SetEgress(&fakeEgressHooks{})
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "plain", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.EgressAllow != nil || s.EgressOpen {
		t.Fatalf("unbound session kept policy %v %v", s.EgressAllow, s.EgressOpen)
	}
}

// A session spawned before the policy existed: its sidecar entry has neither
// key and it must restore with today's allowlist (no policy).
func TestRestoreWithoutEgressPolicy(t *testing.T) {
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
	raw, _ := json.Marshal(sidecarEntry(t, path, s.ID))
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	if _, ok := generic["egress_allow"]; ok {
		t.Fatalf("an unset policy is written to the sidecar: %s", raw)
	}
	if _, ok := generic["egress_open"]; ok {
		t.Fatalf("an unset policy is written to the sidecar: %s", raw)
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
	if r.EgressAllow != nil || r.EgressOpen {
		t.Fatalf("restored policy = %v %v, want none", r.EgressAllow, r.EgressOpen)
	}
	if r.GitHub == nil {
		t.Fatal("binding lost")
	}
}

func TestRestoreKeepsEgressPolicy(t *testing.T) {
	l := egLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox), allow: []string{"pkg.example"}, open: true}
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
	fb.seedLive(s.ID)
	// The profile changes after the spawn: the restored session keeps its own copy.
	l2 := egLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox)}
	m2 := NewManager(l2, fb, 1<<16, time.Minute)
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
	if !reflect.DeepEqual(r.EgressAllow, []string{"pkg.example"}) || !r.EgressOpen {
		t.Fatalf("restored policy = %v %v", r.EgressAllow, r.EgressOpen)
	}
}

// An open bound session reports the open posture, not "allowlist"; a bound
// non-open one still reports allowlist.
func TestInfoEgressReflectsOpenPolicy(t *testing.T) {
	for _, open := range []bool{true, false} {
		l := egLauncher{ghLauncher: ghLauncherFor(config.RuntimeBox), open: open}
		m := NewManager(l, nil, 1<<16, time.Minute)
		m.SetBoxes(&fakeBoxes{})
		m.SetBroker(&fakeBrokerHooks{})
		m.SetEgress(&fakeEgressHooks{})
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "gh", Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Kill(s.ID) })
		want := "allowlist"
		if open {
			want = "open"
		}
		if got := s.Info().Egress; got != want {
			t.Errorf("open=%v: Info.Egress = %q, want %q", open, got, want)
		}
	}
}
