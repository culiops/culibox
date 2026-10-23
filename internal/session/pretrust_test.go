package session

import (
	"errors"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// Claude Code's trust dialog is pre-accepted only for boxes whose sessions run
// with the config bundle (--setting-sources user, which keeps a repo's own
// settings and hooks from loading). Without the bundle the dialog is the only
// gate before a repo hook can read the agent's credentials, so it stays.
func TestBoxedSpawnPreTrustsOnlyWithTheBundle(t *testing.T) {
	for _, c := range []struct {
		name    string
		acct    *account.Resolved // nil = the default account
		bundles *fakeBundles      // nil = bundle layer disabled
		want    bool
	}{
		{"default account, bundle ok", nil, &fakeBundles{}, true},
		{"default account, bundle failed", nil, &fakeBundles{err: errors.New("probe failed")}, false},
		{"bundle layer disabled", nil, nil, false},
		{"account without personal config", ptr(workAccount(false)), &fakeBundles{}, false},
		{"account inheriting personal config", ptr(workAccount(true)), &fakeBundles{}, true},
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

func ptr[T any](v T) *T { return &v }
