package launcher

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credref"
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
