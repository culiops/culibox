package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/ssologin"
	"github.com/culiops/agent-box/internal/web"
)

// noMinter stands in for the STS minter: it fails every mint locally, so no
// test here reads the host's AWS config or reaches AWS.
type noMinter struct{}

func (noMinter) Mint(context.Context, broker.MintInput) (broker.Creds, error) {
	return broker.Creds{}, errors.New("no minting in tests")
}

// testBroker is a broker over an empty session table, a minter that cannot
// mint, and a throwaway audit store.
func testBroker(t *testing.T) *broker.Broker {
	t.Helper()
	audit, err := broker.OpenAudit(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	return broker.New([]byte(strings.Repeat("s", 32)),
		func(string) (broker.SessionView, bool) { return broker.SessionView{}, false }, noMinter{}, audit)
}

type fakeRunner struct {
	key, profile string
	err          error
	done         ssologin.Done
	status       *ssologin.State
}

func (f *fakeRunner) Start(key, profile string, done ssologin.Done) (ssologin.State, error) {
	f.key, f.profile, f.done = key, profile, done
	return ssologin.State{State: "waiting", Code: "ABCD-EFGH"}, f.err
}

func (f *fakeRunner) Status(string) (ssologin.State, bool) {
	if f.status == nil {
		return ssologin.State{}, false
	}
	return *f.status, true
}

func boundSess(id string) *session.Session {
	b := &config.AWSBinding{SourceProfile: "sandbox-admin", RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent",
		Region: "ap-southeast-1", Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::226198813800:role/agentbox-agent-write"}}
	return &session.Session{ID: id, Agent: "aws", Cwd: "/w/proj", Runtime: config.RuntimeBox, Box: "ab-box-1", AWS: b, Exclusive: true}
}

func TestSSOLoginUsesTheRecordedProfileOnly(t *testing.T) {
	r := &fakeRunner{}
	e := elevationService{brk: testBroker(t), runner: r, get: func(id string) (*session.Session, bool) { return boundSess(id), id == "aws-1" }}
	if _, err := e.SSOLogin("aws-1"); err != nil {
		t.Fatal(err)
	}
	if r.profile != "sandbox-admin" || r.key == "" {
		t.Fatalf("runner got %q/%q", r.key, r.profile)
	}
	if _, err := e.SSOLogin("nope"); !errors.Is(err, web.ErrElevationUnknown) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSSOLoginWithoutTheCLIOffersTheCommand(t *testing.T) {
	e := elevationService{brk: testBroker(t), get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	_, err := e.SSOLogin("aws-1")
	var le *web.SSOLoginError
	if !errors.As(err, &le) || le.Command != "aws sso login --profile sandbox-admin" {
		t.Fatalf("got %v", err)
	}
}

func TestSSOLoginRunnerFailureOffersTheCommand(t *testing.T) {
	e := elevationService{brk: testBroker(t), runner: &fakeRunner{err: ssologin.ErrUnparsed},
		get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	_, err := e.SSOLogin("aws-1")
	var le *web.SSOLoginError
	if !errors.As(err, &le) || le.Command != "aws sso login --profile sandbox-admin" || !errors.Is(err, ssologin.ErrUnparsed) {
		t.Fatalf("got %v", err)
	}
}

func TestElevationOnlyAppliesToLiveBoxedBoundSessions(t *testing.T) {
	host := boundSess("h-1")
	host.Runtime = config.RuntimeHost
	unbound := boundSess("u-1")
	unbound.AWS = nil
	sessions := map[string]*session.Session{"h-1": host, "u-1": unbound}
	e := elevationService{brk: testBroker(t), get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }}
	for _, id := range []string{"h-1", "u-1", "missing"} {
		if _, err := e.Grant(id, 30, "x", "", broker.CredCookie); !errors.Is(err, web.ErrElevationUnknown) {
			t.Errorf("Grant(%s): %v", id, err)
		}
		if _, err := e.Drop(id, broker.CredCookie); !errors.Is(err, web.ErrElevationUnknown) {
			t.Errorf("Drop(%s): %v", id, err)
		}
		if _, err := e.SSOLogin(id); !errors.Is(err, web.ErrElevationUnknown) {
			t.Errorf("SSOLogin(%s): %v", id, err)
		}
		if _, ok := e.ssoStatus(id); ok {
			t.Errorf("ssoStatus(%s) answered for a session the card does not apply to", id)
		}
	}
}

func TestDropWithNoWindowIsTheInactiveView(t *testing.T) {
	e := elevationService{brk: testBroker(t), get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	v, err := e.Drop("aws-1", broker.CredCookie)
	if err != nil || v.Active {
		t.Fatalf("got %+v, %v", v, err)
	}
}

func TestClaimCheckUsesTheSessionsCwdAndBox(t *testing.T) {
	var got []string
	e := elevationService{get: func(id string) (*session.Session, bool) { return boundSess(id), true },
		blocker: func(_ context.Context, cwd, self string) (string, error) {
			got = []string{cwd, self}
			return "another box (instance PROD) uses /w/proj", nil
		}}
	if err := e.claimCheck("aws-1"); err == nil || !strings.Contains(err.Error(), "instance PROD") {
		t.Fatalf("got %v", err)
	}
	if !slices.Equal(got, []string{"/w/proj", "ab-box-1"}) {
		t.Fatalf("blocker args %v", got)
	}
	e.blocker = func(context.Context, string, string) (string, error) { return "", errors.New("docker down") }
	err := e.claimCheck("aws-1")
	if err == nil {
		t.Fatal("a listing failure must refuse the grant")
	}
	if strings.Contains(err.Error(), "docker down") {
		t.Errorf("the refusal shown to the user carries the internal error: %v", err)
	}
	e.blocker = func(context.Context, string, string) (string, error) { return "", nil }
	if err := e.claimCheck("aws-1"); err != nil {
		t.Fatalf("nothing blocking: %v", err)
	}
	e.blocker = nil
	if err := e.claimCheck("aws-1"); err == nil {
		t.Fatal("no box manager must refuse, not allow")
	}
}

func TestGrantContextListsLiveHostSessions(t *testing.T) {
	e := elevationService{get: func(id string) (*session.Session, bool) { return boundSess(id), true },
		list: func() []session.Info {
			return []session.Info{{ID: "zsh-1", Name: "zsh", Runtime: "host"}, {ID: "old-1", Runtime: "host", Exited: true},
				{ID: "aws-1", Runtime: "box"}}
		},
		blocker: func(context.Context, string, string) (string, error) { return "", nil }}
	gc := e.grantContext("aws-1")
	if len(gc.HostSessions) != 1 || gc.HostSessions[0].ID != "zsh-1" || gc.Blocked != "" {
		t.Fatalf("%+v", gc)
	}
}

func TestGrantContextReportsABlockerAndALookupFailure(t *testing.T) {
	e := elevationService{get: func(id string) (*session.Session, bool) { return boundSess(id), true },
		blocker: func(context.Context, string, string) (string, error) { return "another box uses /w/proj", nil }}
	if gc := e.grantContext("aws-1"); gc.Blocked != "another box uses /w/proj" || gc.HostSessions == nil {
		t.Fatalf("%+v", gc)
	}
	e.blocker = func(context.Context, string, string) (string, error) { return "", errors.New("docker down") }
	if gc := e.grantContext("aws-1"); gc.Blocked != "could not check this folder's boxes; try again" {
		t.Fatalf("%+v", gc)
	}
}

func TestSSOStatusReportsTheFlowAndTheCommand(t *testing.T) {
	r := &fakeRunner{}
	e := elevationService{brk: testBroker(t), runner: r, get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	if _, ok := e.ssoStatus("aws-1"); ok {
		t.Fatal("nothing expired and no flow: no sso object")
	}
	r.status = &ssologin.State{State: "waiting", Code: "ABCD-EFGH"}
	st, ok := e.ssoStatus("aws-1")
	if !ok || st.Login == nil || st.Login.Code != "ABCD-EFGH" || st.Command != "aws sso login --profile sandbox-admin" || st.Expired {
		t.Fatalf("%+v %v", st, ok)
	}
}
