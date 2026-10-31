package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

func TestSaveChecksGitHubApps(t *testing.T) {
	pm := NewProfileManager(nil, nil, config.RuntimeBox, nil, newFakeStore(), func([]config.AgentConfig) {})
	p := config.AgentConfig{Name: "p", Bin: "claude", Runtime: config.RuntimeBox,
		GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"agent-box"}}}}}

	if err := pm.Save(p); err == nil || !strings.Contains(err.Error(), `"main"`) {
		t.Fatalf("an unknown App must be refused naming it, got %v", err)
	}
	pm.SetGitHubApps([]config.GitHubApp{{Name: "main", ClientID: "x", PrivateKey: config.KeyRef{Type: "file", Path: "/k"}}})
	if err := pm.Save(p); err != nil {
		t.Fatalf("a defined App should save: %v", err)
	}
}

func TestProfileViewGitHubWriteFlags(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "pat", Bin: "claude", Runtime: config.RuntimeBox,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", Token: "gh-pat", Repos: []string{"agent-box"}}}}},
		{Name: "app-default", Bin: "claude", Runtime: config.RuntimeBox,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"agent-box"}}}}},
		{Name: "app-write", Bin: "claude", Runtime: config.RuntimeBox,
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"agent-box"}}},
				Permissions: map[string]string{"contents": "write", "metadata": "read"}}},
		{Name: "none", Bin: "claude", Runtime: config.RuntimeBox},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := map[string]ProfileView{}
	for _, v := range pm.Views() {
		got[v.Name] = v
	}
	for name, want := range map[string][2]bool{
		"pat": {true, true}, "app-default": {false, false}, "app-write": {true, false}, "none": {false, false},
	} {
		if v := got[name]; v.GitHubWrite != want[0] || v.GitHubPAT != want[1] {
			t.Errorf("%s: github_write=%v github_pat=%v, want %v", name, v.GitHubWrite, v.GitHubPAT, want)
		}
	}
	// Both flags are omitted when false, so an App-default profile's JSON is unchanged.
	b, err := json.Marshal(got["app-default"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "github_write") || strings.Contains(string(b), "github_pat") {
		t.Errorf("app-default view carries a write flag: %s", b)
	}
	b, _ = json.Marshal(got["pat"])
	if !strings.Contains(string(b), `"github_write":true`) || !strings.Contains(string(b), `"github_pat":true`) {
		t.Errorf("pat view = %s", b)
	}
}
