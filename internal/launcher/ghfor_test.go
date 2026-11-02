package launcher

import (
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
