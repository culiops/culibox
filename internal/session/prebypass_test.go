package session

import (
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// bypassLauncher is a tiered launcher that says whether the spawn runs with the
// bypass option, as launcher.AgentLauncher does.
type bypassLauncher struct {
	tieredLauncher
	bypass bool
}

func (l bypassLauncher) BypassesPermissions(launcher.SpawnSpec) bool { return l.bypass }

// A boxed spawn that runs with the bypass option asks the box layer to
// pre-accept Claude Code's "Bypass Permissions mode" warning in the box home it
// creates: the option is the consent, and nothing in an unattended session
// answers the dialog. It does not depend on the config bundle or the account,
// and a spawn without the option never asks.
func TestBoxedSpawnPreAcceptsBypassOnlyForTheBypassOption(t *testing.T) {
	tl := tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox}
	for _, c := range []struct {
		name    string
		l       launcher.Builder
		bundles *fakeBundles
		want    bool
	}{
		{"bypass option", bypassLauncher{tl, true}, nil, true},
		{"bypass option with the bundle", bypassLauncher{tl, true}, &fakeBundles{flags: userOnly}, true},
		{"bypass option, account without personal config",
			bypassAcctLauncher{bypassLauncher{tl, true}, workAccount(false)}, &fakeBundles{flags: userOnly}, true},
		{"no bypass option", bypassLauncher{tl, false}, nil, false},
		{"launcher that cannot say", tl, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := NewManager(c.l, nil, 1<<16, time.Minute)
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
			got := boxes.lastKey.PreAcceptBypass
			boxes.mu.Unlock()
			if got != c.want {
				t.Fatalf("PreAcceptBypass = %v, want %v", got, c.want)
			}
		})
	}
}

// bypassAcctLauncher is bypassLauncher under a named account.
type bypassAcctLauncher struct {
	bypassLauncher
	acct account.Resolved
}

func (l bypassAcctLauncher) AccountFor(launcher.SpawnSpec) (account.Resolved, error) {
	return l.acct, nil
}

// Host-tier sessions are unchanged: they never reach the box layer, so their
// home and the dialog stay as they were.
func TestHostSpawnNeverTouchesTheBoxLayerForBypass(t *testing.T) {
	l := bypassLauncher{tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost}, true}
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	boxes.mu.Lock()
	wraps, asked := boxes.wraps, boxes.lastKey.PreAcceptBypass
	boxes.mu.Unlock()
	if wraps != 0 || asked {
		t.Fatalf("a host-tier spawn reached the box layer: wraps=%d PreAcceptBypass=%v", wraps, asked)
	}
}

// The production launcher is what answers; a rename on either side would
// otherwise silently turn the seed off.
var _ BypassResolver = (*launcher.AgentLauncher)(nil)
