package session

import (
	"errors"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// Claude Code's trust dialog is pre-accepted only when the spawn creating the
// box runs with the bundle's --setting-sources user, which keeps a repo's own
// settings and hooks from loading. Without it the dialog is the only gate
// before a repo hook can read the agent's credentials, so it stays: that
// includes a bundle without the flag (a shell, codex or wrapper-bin spawn).
func TestBoxedSpawnPreTrustsOnlyWithTheBundle(t *testing.T) {
	for _, c := range []struct {
		name    string
		acct    *account.Resolved // nil = the default account
		bundles *fakeBundles      // nil = bundle layer disabled
		want    bool
	}{
		{"default account, bundle ok", nil, &fakeBundles{flags: userOnly}, true},
		{"bundle ok, flag as one arg", nil, &fakeBundles{flags: []string{"--setting-sources=user"}}, true},
		{"bundle ok but no flag (shell, codex, wrapper bin)", nil, &fakeBundles{flags: []string{"--strict-mcp-config"}}, false},
		{"bundle ok, other setting sources", nil, &fakeBundles{flags: []string{"--setting-sources", "user,project"}}, false},
		{"default account, bundle failed", nil, &fakeBundles{err: errors.New("probe failed")}, false},
		{"bundle layer disabled", nil, nil, false},
		{"account without personal config", ptr(workAccount(false)), &fakeBundles{flags: userOnly}, false},
		{"account inheriting personal config", ptr(workAccount(true)), &fakeBundles{flags: userOnly}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tl := tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}
			var l launcher.Builder = tl
			if c.acct != nil {
				l = acctLauncher{tieredLauncher: tl, acct: *c.acct}
			}
			m := NewManager(l, nil, 1<<16, time.Minute)
			boxes := &fakeBoxes{}
			m.SetBoxes(boxes)
			if c.bundles != nil {
				m.SetBundles(c.bundles)
			}
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Kill(s.ID)
			boxes.mu.Lock()
			got := boxes.lastKey.PreTrust
			boxes.mu.Unlock()
			if got != c.want {
				t.Fatalf("PreTrust = %v, want %v", got, c.want)
			}
		})
	}
}

var userOnly = []string{"--mcp-config", "/b/mcp.json", "--setting-sources", "user", "--strict-mcp-config"}

func ptr[T any](v T) *T { return &v }
