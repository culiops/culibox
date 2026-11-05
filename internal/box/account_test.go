package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBoxNameAccount(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	legacy, err := m.boxName(dir, BoxKey{})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := m.boxName(dir, BoxKey{Account: "default"})
	work, _ := m.boxName(dir, BoxKey{Account: "work"})
	work2, _ := m.boxName(dir, BoxKey{Account: "work"})
	other, _ := m.boxName(dir, BoxKey{Account: "other"})
	if def != legacy {
		t.Fatal("default account must keep today's box name")
	}
	if work == legacy || work == other || work != work2 {
		t.Fatalf("names: legacy=%s work=%s other=%s", legacy, work, other)
	}
	if !isBoxName(work) {
		t.Fatal("account box name must keep the prefix+12hex format (reclaim depends on it)")
	}
}

func TestBoxNameBindingKeepsUnboundNamesStable(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	plain, _ := m.boxName(dir, BoxKey{})
	legacy, _ := m.boxName(dir, BoxKey{Account: "default"})
	bound, _ := m.boxName(dir, BoxKey{Binding: "abc123abc123"})
	bound2, _ := m.boxName(dir, BoxKey{Binding: "ffffffffffff"})
	if plain != legacy {
		t.Fatal("default account must hash as before")
	}
	if bound == plain || bound == bound2 {
		t.Fatal("a binding must get its own box, and different bindings different boxes")
	}
	again, _ := m.boxName(dir, BoxKey{Binding: "abc123abc123"})
	if again != bound {
		t.Fatal("the same binding must share a box")
	}
	work, _ := m.boxName(dir, BoxKey{Account: "work"})
	workBound, _ := m.boxName(dir, BoxKey{Account: "work", Binding: "abc123abc123"})
	if workBound == work || workBound == bound {
		t.Fatal("account and binding both key the box")
	}
	if !isBoxName(bound) || !isBoxName(workBound) {
		t.Fatal("bound box names must keep the prefix+12hex format (reclaim depends on it)")
	}
}

// The default account must hash exactly what boxName hashed before accounts
// existed. This recomputes the old formula independently, so it cannot be
// satisfied by two runs of the new code agreeing with each other.
func TestBoxNameDefaultMatchesPreAccountFormula(t *testing.T) {
	m := newTestManagerWithInstance(t, "PROD")
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("PROD" + "\x00" + abs))
	want := namePrefix + hex.EncodeToString(sum[:6])
	for _, acct := range []string{"", "default"} {
		got, err := m.boxName(dir, BoxKey{Account: acct})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("account %q: box name %s, pre-account formula gives %s", acct, got, want)
		}
	}
}

func TestHostOnlyEnvBlocksConfigDirs(t *testing.T) {
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if !hostOnlyEnv[k] {
			t.Errorf("%s must not be forwarded into a box", k)
		}
	}
}

// The config dir is a host path and stays out of the container; the account's
// token is forwarded by name like any other secret (spec: "TokenSecret
// forwarded by name, as today").
func TestForwardableEnvDropsConfigDirKeepsToken(t *testing.T) {
	got := forwardableEnvNames([]string{
		"CLAUDE_CONFIG_DIR=/data/accounts/work/claude",
		"CODEX_HOME=/data/accounts/x/codex",
		"CLAUDE_CODE_OAUTH_TOKEN=secret",
		"TERM=xterm",
	})
	want := []string{"CLAUDE_CODE_OAUTH_TOKEN", "TERM"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forwarded %v, want %v", got, want)
	}
}

func TestWrapGivesEachAccountItsOwnBox(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	ws := t.TempDir()
	names := map[string]string{}
	for _, acct := range []string{"", "default", "work", "other"} {
		_, _, name, err := m.Wrap(context.Background(), ws, "s-"+acct, BoxKey{Account: acct}, []string{"claude"}, nil, nil)
		if err != nil {
			t.Fatalf("account %q: %v", acct, err)
		}
		names[acct] = name
	}
	if names[""] != names["default"] {
		t.Fatalf("default spellings diverged: %v", names)
	}
	if names["work"] == names[""] || names["work"] == names["other"] {
		t.Fatalf("accounts share a box: %v", names)
	}
}

// A non-default account's box home is an ordinary box home to the sweeper.
func TestReclaimRemovesAnAccountHomeLikeADefaultOne(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	name := mustAcctBoxName(t, m, t.TempDir(), "work")
	dir := seedHome(t, m, name, 40*24*time.Hour)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != name {
		t.Fatalf("expected one reclaim of %s, got %+v", name, done)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("home still present: %v", err)
	}
	if _, err := os.Stat(m.markerPath(name)); !os.IsNotExist(err) {
		t.Fatalf("marker outlived its home: %v", err)
	}
}

func mustAcctBoxName(t *testing.T, m *Manager, cwd, acct string) string {
	t.Helper()
	n, err := m.boxName(cwd, BoxKey{Account: acct})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
