package launcher

import (
	"errors"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
)

// tokenLauncher: a launcher whose secret_sources chain knows chainVals and whose
// store-only resolver (the account-token fallback) knows storeVals.
func tokenLauncher(acct account.Resolved, chainVals, storeVals map[string]string) *AgentLauncher {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean"}},
		fakeResolver{vals: chainVals}, nil)
	l.SetAccounts(fakeAccounts{acct.Name: acct})
	if storeVals != nil {
		l.SetAccountTokenStore(fakeResolver{vals: storeVals})
	}
	return l
}

func TestBuild_OwnStoreTokenResolvesWithoutStoreSource(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = account.TokenID("work")
	l := tokenLauncher(acct, nil, map[string]string{"ACCT_WORK_TOKEN": "sk-own"})
	_, env, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if envMap(env)[account.TokenEnv] != "sk-own" {
		t.Fatalf("own store token not injected: %q", envMap(env)[account.TokenEnv])
	}
}

func TestBuild_ChainWinsOverStoreFallback(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = account.TokenID("work")
	l := tokenLauncher(acct, map[string]string{"ACCT_WORK_TOKEN": "from-chain"}, map[string]string{"ACCT_WORK_TOKEN": "from-store"})
	_, env, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap(env)[account.TokenEnv]; got != "from-chain" {
		t.Fatalf("token = %q, want the secret_sources chain's value (config order is honoured)", got)
	}
}

// A token_secret that is not the account's own id must never be read straight
// from the store: that would let an account be pointed at any stored value.
func TestBuild_ForeignStoreIDNeverFallsBack(t *testing.T) {
	acct := workAcct("/d")
	for _, id := range []string{"GH_CI_KEY", "ACCT_OTHER_TOKEN"} {
		acct.TokenSecret = id
		l := tokenLauncher(acct, nil, map[string]string{id: "secret"})
		if _, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost}); err == nil {
			t.Fatalf("token_secret %q resolved from the store without a store secret_source", id)
		}
	}
}

func TestBuild_OwnStoreTokenLockedIsStoreLocked(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = account.TokenID("work")
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean"}}, fakeResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	l.SetAccountTokenStore(errResolver{credref.ErrLocked})
	_, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreLocked", err)
	}
}

func TestBuild_OwnStoreTokenMissingFails(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = account.TokenID("work")
	l := tokenLauncher(acct, nil, map[string]string{})
	_, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if err == nil || !strings.Contains(err.Error(), "no token is saved") || strings.Contains(err.Error(), "secret_sources") {
		t.Fatalf("a missing own token must fail the spawn and point at Settings, not secret_sources: %v", err)
	}
	// No store wired at all (a launcher built without one) behaves the same.
	l2 := tokenLauncher(acct, nil, nil)
	if _, _, err := l2.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost}); err == nil {
		t.Fatal("no store fallback: the spawn must fail")
	}
}
