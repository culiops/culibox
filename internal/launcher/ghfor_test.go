package launcher

import (
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func TestLauncherGitHubForClones(t *testing.T) {
	g := &config.GitHubBinding{
		Owners:      []config.GitHubOwner{{Owner: "acme", App: "main", Repos: []string{"api"}}},
		Permissions: map[string]string{"contents": "read"},
		EgressAllow: []string{"registry.npmjs.org"},
	}
	l := New([]config.AgentConfig{{Name: "c", Bin: "claude", GitHub: g}, {Name: "plain", Bin: "claude"}}, fakeResolver{}, nil)
	got := l.GitHubFor("c")
	if got == nil || got.Owners[0].Owner != "acme" {
		t.Fatalf("got %+v", got)
	}
	if l.GitHubFor("plain") != nil || l.GitHubFor("missing") != nil {
		t.Fatal("unbound/missing must be nil")
	}
	got.Owners[0].Owner = "mutated"
	got.Owners[0].Repos[0] = "mutated"
	got.Permissions["contents"] = "mutated"
	got.EgressAllow[0] = "mutated"
	again := l.GitHubFor("c")
	if again.Owners[0].Owner != "acme" || again.Owners[0].Repos[0] != "api" || again.Permissions["contents"] != "read" || again.EgressAllow[0] != "registry.npmjs.org" {
		t.Fatalf("GitHubFor must return a deep copy, got %+v", again)
	}
}

func TestLauncherEgressForClones(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "claude", EgressAllow: []string{"pkg.example"}, EgressOpen: true}, {Name: "plain", Bin: "claude"}}, fakeResolver{}, nil)
	allow, open := l.EgressFor("c")
	if !reflect.DeepEqual(allow, []string{"pkg.example"}) || !open {
		t.Fatalf("got %v %v", allow, open)
	}
	allow[0] = "mutated"
	if again, _ := l.EgressFor("c"); again[0] != "pkg.example" {
		t.Fatalf("EgressFor must return a copy, got %v", again)
	}
	for _, name := range []string{"plain", "missing"} {
		if a, o := l.EgressFor(name); a != nil || o {
			t.Fatalf("%s: got %v %v", name, a, o)
		}
	}
}

// #95: a running session's profile policy, or ok=false (fail closed) for a
// profile that is gone or no longer runs in a box.
func TestLauncherLiveEgressFor(t *testing.T) {
	l := New([]config.AgentConfig{
		{Name: "c", Bin: "claude", Runtime: config.RuntimeBox, EgressAllow: []string{"pkg.example"}, EgressOpen: true},
		{Name: "dflt", Bin: "claude"},
		{Name: "h", Bin: "claude", Runtime: config.RuntimeHost},
	}, fakeResolver{}, nil)
	allow, open, ok := l.LiveEgressFor("c")
	if !ok || !reflect.DeepEqual(allow, []string{"pkg.example"}) || !open {
		t.Fatalf("c: %v %v %v", allow, open, ok)
	}
	allow[0] = "mutated"
	if again, _, _ := l.LiveEgressFor("c"); again[0] != "pkg.example" {
		t.Fatalf("LiveEgressFor must return a copy, got %v", again)
	}
	if a, o, ok := l.LiveEgressFor("dflt"); !ok || a != nil || o {
		t.Fatalf("a box profile with no policy: %v %v %v", a, o, ok)
	}
	for _, name := range []string{"h", "missing"} {
		if a, o, ok := l.LiveEgressFor(name); ok || a != nil || o {
			t.Fatalf("%s: %v %v %v, want fail closed", name, a, o, ok)
		}
	}
	// Renamed: the old name is gone.
	l.SetAgents([]config.AgentConfig{{Name: "c2", Bin: "claude", EgressAllow: []string{"pkg.example"}}})
	if _, _, ok := l.LiveEgressFor("c"); ok {
		t.Fatal("a renamed profile's old name must fail closed")
	}
}
