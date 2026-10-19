package web

import (
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
