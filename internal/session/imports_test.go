package session

import (
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// importsLauncher is an acctLauncher whose profile "imp" answers ImportsOffFor.
type importsLauncher struct {
	acctLauncher
	off bool
}

func (l importsLauncher) ImportsOffFor(agent string) bool { return agent == "imp" && l.off }

func importsManager(t *testing.T, l importsLauncher, b *fakeBundles) (*Manager, *fakeBoxes) {
	t.Helper()
	m := NewManager(l, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	m.SetBundles(b)
	return m, boxes
}

func importsLauncherFor(off bool, inherit bool, nonDefault bool) importsLauncher {
	l := importsLauncher{
		acctLauncher: acctLauncher{
			tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		},
		off: off,
	}
	l.acct = account.DefaultResolved("/home/u/.claude")
	if nonDefault {
		l.acct = workAccount(inherit)
	}
	return l
}

func TestSpawnPassesImportsSwitchToBundle(t *testing.T) {
	for _, off := range []bool{true, false} {
		bundles := &fakeBundles{imports: 2}
		m, boxes := importsManager(t, importsLauncherFor(off, false, false), bundles)
		s, err := m.Spawn(launcher.SpawnSpec{Agent: "imp", Runtime: config.RuntimeBox})
		if err != nil {
			t.Fatal(err)
		}
		_ = m.Kill(s.ID)
		if bundles.calls != 1 {
			t.Fatalf("off=%v: bundle consulted %d times, want 1", off, bundles.calls)
		}
		if bundles.withImports == off {
			t.Fatalf("off=%v: bundle asked withImports=%v", off, bundles.withImports)
		}
		if boxes.lastKey.NoImports != off {
			t.Fatalf("off=%v: key.NoImports=%v", off, boxes.lastKey.NoImports)
		}
	}
}

// A profile that has no switch (or an unknown one) keeps the imports.
func TestSpawnWithoutImportsSwitchKeepsImports(t *testing.T) {
	bundles := &fakeBundles{}
	m, boxes := importsManager(t, importsLauncherFor(true, false, false), bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "other", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if !bundles.withImports || boxes.lastKey.NoImports {
		t.Fatalf("withImports=%v NoImports=%v, want true/false", bundles.withImports, boxes.lastKey.NoImports)
	}
}

// A non-default account that does not inherit the personal config never asks
// the bundle, so the switch has nothing to turn off: the key stays as it was.
func TestSpawnNoBundleLeavesNoImportsUnset(t *testing.T) {
	bundles := &fakeBundles{}
	m, boxes := importsManager(t, importsLauncherFor(true, false, true), bundles)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "imp", Runtime: config.RuntimeBox})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	if bundles.calls != 0 {
		t.Fatalf("bundle consulted %d times for a non-inheriting account", bundles.calls)
	}
	if boxes.lastKey.NoImports {
		t.Fatal("key.NoImports set although no bundle was asked")
	}
}
