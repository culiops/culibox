package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// acctLauncher is a tieredLauncher that also answers AccountFor.
type acctLauncher struct {
	tieredLauncher
	acct account.Resolved
}

func (l acctLauncher) AccountFor(launcher.SpawnSpec) (account.Resolved, error) { return l.acct, nil }

func workAccount(inherit bool) account.Resolved {
	return account.Resolved{
		Account:   account.Account{Name: "work", Provider: account.ProviderClaude, InheritPersonal: inherit},
		ConfigDir: "/d/work/claude",
	}
}

func TestSpawnRecordsAccount(t *testing.T) {
	l := acctLauncher{
		tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost},
		acct:           workAccount(false),
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	fb := newFakeBackend()
	m := NewManager(l, fb, 1<<16, time.Minute)
	if err := m.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir(), Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if s.Account != "work" || s.ConfigDir != "/d/work/claude" || s.Info().Account != "work" {
		t.Fatalf("Account=%q ConfigDir=%q Info.Account=%q", s.Account, s.ConfigDir, s.Info().Account)
	}
	md := sidecarEntry(t, path, s.ID)
	if md.Account != "work" || md.ConfigDir != "/d/work/claude" {
		t.Fatalf("sidecar entry = %+v", md)
	}

	// A restarted daemon reattaches the surviving session with the same account.
	fb.seedLive(s.ID)
	m2 := NewManager(l, fb, 1<<16, time.Minute)
	if err := m2.SetSidecarPath(path); err != nil {
		t.Fatal(err)
	}
	if err := m2.Reconcile(); err != nil {
		t.Fatal(err)
	}
	r, ok := m2.Get(s.ID)
	if !ok {
		t.Fatal("session not reattached")
	}
	t.Cleanup(func() { _ = m2.Kill(r.ID) })
	if r.Account != "work" || r.ConfigDir != "/d/work/claude" || r.Info().Account != "work" {
		t.Fatalf("reattached Account=%q ConfigDir=%q Info.Account=%q", r.Account, r.ConfigDir, r.Info().Account)
	}
}

// The default account must leave no trace: nothing new in the sidecar or the
// wire shape, and no config dir (the default account's dir is never ours to set).
func TestDefaultAccountSessionIsUnchanged(t *testing.T) {
	cases := map[string]launcher.Builder{
		"no resolver": tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost},
		"default resolved": acctLauncher{
			tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeHost},
			acct:           account.DefaultResolved("/home/u/.claude"),
		},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			m := NewManager(l, nil, 1<<16, time.Minute)
			if err := m.SetSidecarPath(path); err != nil {
				t.Fatal(err)
			}
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir(), Runtime: config.RuntimeHost})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			if s.Account != "" || s.ConfigDir != "" {
				t.Fatalf("Account=%q ConfigDir=%q, want both empty", s.Account, s.ConfigDir)
			}
			for what, v := range map[string]any{"info": s.Info(), "sidecar": sidecarEntry(t, path, s.ID)} {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "account") || strings.Contains(string(b), "config_dir") {
					t.Fatalf("%s grew an account key: %s", what, b)
				}
			}
		})
	}
}

func TestSpawnBoxedPassesAccountAndGatesBundle(t *testing.T) {
	cases := []struct {
		name       string
		acct       account.Resolved
		wantAcct   string
		wantBundle bool
	}{
		{"non-default, inherit off", workAccount(false), "work", false},
		{"non-default, inherit on", workAccount(true), "work", true},
		{"default", account.DefaultResolved(""), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := acctLauncher{
				tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
				acct:           c.acct,
			}
			m := NewManager(l, nil, 1<<16, time.Minute)
			boxes := &fakeBoxes{}
			bundles := &fakeBundles{}
			m.SetBoxes(boxes)
			m.SetBundles(bundles)
			s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(s.ID) })
			if boxes.lastAcct != c.wantAcct {
				t.Errorf("Wrap acct = %q, want %q", boxes.lastAcct, c.wantAcct)
			}
			if got := bundles.askedBin != ""; got != c.wantBundle {
				t.Errorf("bundle consulted = %v, want %v", got, c.wantBundle)
			}
			if s.ConfigDir != "" {
				t.Errorf("a boxed session must not record ConfigDir, got %q", s.ConfigDir)
			}
			if s.Account != c.wantAcct {
				t.Errorf("Account = %q, want %q", s.Account, c.wantAcct)
			}
		})
	}
}

// failingAcctLauncher refuses every account resolution.
type failingAcctLauncher struct {
	tieredLauncher
	err error
}

func (l failingAcctLauncher) AccountFor(launcher.SpawnSpec) (account.Resolved, error) {
	return account.Resolved{}, l.err
}

func TestSpawnPropagatesAccountRefusalBeforeTouchingTheBox(t *testing.T) {
	m := NewManager(failingAcctLauncher{
		tieredLauncher: tieredLauncher{argv: []string{"sh", "-c", "sleep 30"}, runtime: config.RuntimeBox},
		err:            launcher.ErrCodexAccount,
	}, nil, 1<<16, time.Minute)
	boxes := &fakeBoxes{}
	m.SetBoxes(boxes)
	if _, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Runtime: config.RuntimeBox}); err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v, want the codex refusal", err)
	}
	if boxes.prepares != 0 {
		t.Fatalf("a refused spawn prepared the box %d times", boxes.prepares)
	}
}

// specLauncher wraps ctlLauncher with AccountFor, answering from spec.Account
// and falling back to a mutable profile account, like the real launcher.
type specLauncher struct {
	*ctlLauncher
	pmu     sync.Mutex
	profile string
}

func (l *specLauncher) setProfile(p string) { l.pmu.Lock(); l.profile = p; l.pmu.Unlock() }

func (l *specLauncher) AccountFor(spec launcher.SpawnSpec) (account.Resolved, error) {
	l.pmu.Lock()
	name := spec.Account
	if name == "" {
		name = l.profile
	}
	l.pmu.Unlock()
	if name == "" || name == account.Default {
		return account.DefaultResolved(""), nil
	}
	return account.Resolved{Account: account.Account{Name: name, Provider: account.ProviderClaude}, ConfigDir: "/d/" + name + "/claude"}, nil
}

// A restart resumes under the account the session ran under, not whatever the
// profile says now: the credentials and the transcript dir both depend on it.
func TestRestartKeepsTheSessionsAccount(t *testing.T) {
	for _, c := range []struct{ name, profileAtSpawn, profileAfter, wantAcct, wantSpec string }{
		{"non-default stays", "work", "other", "work", "work"},
		{"default stays default", "", "work", "", "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &specLauncher{
				ctlLauncher: &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"},
				profile:     c.profileAtSpawn,
			}
			m := NewManager(l, nil, 1<<16, time.Minute)
			if err := m.SetSidecarPath(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
				t.Fatal(err)
			}
			old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
			if old.Account != c.wantAcct {
				t.Fatalf("spawned Account=%q want %q", old.Account, c.wantAcct)
			}
			l.setProfile(c.profileAfter) // the profile moves to another account

			ns, err := m.Restart(old.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Kill(ns.ID) })
			if got := l.last().Account; got != c.wantSpec {
				t.Fatalf("restart spec Account=%q want %q", got, c.wantSpec)
			}
			if ns.Account != c.wantAcct {
				t.Fatalf("restarted Account=%q want %q", ns.Account, c.wantAcct)
			}
		})
	}
}
