package launcher

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/secretstore"
)

// lockedResolver answers like a chain whose store is locked.
type lockedResolver struct{}

func (lockedResolver) Resolve(name string) (string, bool, error) {
	return "", false, credref.ErrLocked
}

func TestBuildLockedStoreRefusesBySecretName(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean", Secrets: []string{"A"}}},
		lockedResolver{}, nil)
	_, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreLocked", err)
	}
	if !strings.Contains(err.Error(), "A") {
		t.Errorf("the log-bound error should name the secret: %v", err)
	}
}

func TestBuildLockedStoreRefusesAccountToken(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = "WORK_TOK"
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean"}}, lockedResolver{}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	_, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreLocked", err)
	}
}

func TestBuildOtherResolveErrorIsNotStoreLocked(t *testing.T) {
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean", Secrets: []string{"A"}}},
		errResolver{fmt.Errorf("disk on fire")}, nil)
	_, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if err == nil || errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want a plain resolve error", err)
	}
}

type errResolver struct{ err error }

func (e errResolver) Resolve(string) (string, bool, error) { return "", false, e.err }

func unavailableErr() *secretstore.UnavailableError {
	return &secretstore.UnavailableError{Kind: secretstore.KindAccess, Reason: "User interaction is not allowed"}
}

// A Keychain failure refuses the spawn as ErrStoreUnavailable, never as the
// locked store: the operator has no passphrase to type.
func TestBuildUnavailableStoreRefuses(t *testing.T) {
	ue := unavailableErr()
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean", Secrets: []string{"A"}}},
		errResolver{fmt.Errorf("store A: %w", ue)}, nil)
	_, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreUnavailable only", err)
	}
	var got *secretstore.UnavailableError
	if !errors.As(err, &got) || got.Fix() == "" {
		t.Fatalf("the UnavailableError (and its fix) must survive the wrap: %v", err)
	}
	if !strings.Contains(err.Error(), "A") {
		t.Errorf("the log-bound error should name the secret: %v", err)
	}
}

func TestBuildUnavailableStoreRefusesAccountToken(t *testing.T) {
	acct := workAcct("/d")
	acct.TokenSecret = "WORK_TOK"
	l := New([]config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean"}}, errResolver{unavailableErr()}, nil)
	l.SetAccounts(fakeAccounts{"work": acct})
	_, _, err := l.Build(SpawnSpec{Agent: "c", Account: "work", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreUnavailable only", err)
	}
}

// storedOutsideResolver resolves nothing, and says which names the store holds
// while it is not a secret source (secretchain.Chain.StoredOutsideSources).
type storedOutsideResolver struct{ stored map[string]bool }

func (storedOutsideResolver) Resolve(string) (string, bool, error)    { return "", false, nil }
func (r storedOutsideResolver) StoredOutsideSources(name string) bool { return r.stored[name] }

// #336: an unresolved profile secret is a shown refusal that names the secret
// and the fix, and a Stored value the chain never reads says so.
func TestBuildUnresolvedSecretSaysWhy(t *testing.T) {
	cfgs := []config.AgentConfig{{Name: "c", Bin: "/usr/bin/claude", Base: "clean", Secrets: []string{"MY_TOKEN"}}}

	l := New(cfgs, storedOutsideResolver{stored: map[string]bool{"MY_TOKEN": true}}, nil)
	_, _, err := l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrSecretUnresolved) {
		t.Fatalf("err = %v, want ErrSecretUnresolved", err)
	}
	for _, want := range []string{`"MY_TOKEN"`, "Stored value", `{"type": "store"}`, "secret_sources", "service token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("stored-outside refusal should say %q: %v", want, err)
		}
	}

	l = New(cfgs, storedOutsideResolver{}, nil)
	_, _, err = l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost})
	if !errors.Is(err, ErrSecretUnresolved) {
		t.Fatalf("err = %v, want ErrSecretUnresolved", err)
	}
	if !strings.Contains(err.Error(), `"MY_TOKEN"`) || strings.Contains(err.Error(), "Stored value") {
		t.Errorf("a name no source has should be named, without the store sentence: %v", err)
	}

	// A resolver without the store query (secrets.New's) gets the plain sentence.
	l = New(cfgs, fakeResolver{}, nil)
	if _, _, err = l.Build(SpawnSpec{Agent: "c", Runtime: config.RuntimeHost}); !errors.Is(err, ErrSecretUnresolved) {
		t.Fatalf("err = %v, want ErrSecretUnresolved", err)
	}
}
