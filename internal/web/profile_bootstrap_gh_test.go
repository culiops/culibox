package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func TestProfileBootstrapGitHubAppNamesOnly(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetGitHubApps([]config.GitHubApp{
		{Name: "main", ClientID: "Iv1.secretish", PrivateKey: config.KeyRef{Type: "file", Path: "/home/x/keys/app.pem"}},
		{Name: "org", ClientID: "Iv1.other", PrivateKey: config.KeyRef{Type: "file", Path: "/home/x/keys/org.pem"}},
	})
	s := &Server{AllowedHosts: []string{"example.com"}, Profiles: pm}
	got := metaGet(t, s)
	apps, _ := got["github_apps"].([]any)
	if len(apps) != 2 || apps[0] != "main" || apps[1] != "org" {
		t.Fatalf("github_apps = %v, want the names in order", got["github_apps"])
	}
	raw, _ := json.Marshal(got)
	for _, banned := range []string{"private_key", "/home/x/keys", ".pem", "Iv1."} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the bootstrap payload carries %q: %s", banned, raw)
		}
	}

	// No apps defined: an empty list on the wire, never null.
	pm2 := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: pm2})
	if apps, ok := got["github_apps"].([]any); !ok || len(apps) != 0 {
		t.Fatalf("github_apps with none defined = %v, want []", got["github_apps"])
	}
}
