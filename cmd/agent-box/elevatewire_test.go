package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/ssologin"
	"github.com/culiops/culibox/internal/web"
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
	if _, err := e.SSOLogin("aws-1", ""); err != nil {
		t.Fatal(err)
	}
	if r.profile != "sandbox-admin" || r.key == "" {
		t.Fatalf("runner got %q/%q", r.key, r.profile)
	}
	if _, err := e.SSOLogin("nope", ""); !errors.Is(err, web.ErrElevationUnknown) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSSOLoginWithoutTheCLIOffersTheCommand(t *testing.T) {
	e := elevationService{brk: testBroker(t), get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	_, err := e.SSOLogin("aws-1", "")
	var le *web.SSOLoginError
	if !errors.As(err, &le) || le.Command != "aws sso login --profile sandbox-admin" {
		t.Fatalf("got %v", err)
	}
}

func TestSSOLoginRunnerFailureOffersTheCommand(t *testing.T) {
	e := elevationService{brk: testBroker(t), runner: &fakeRunner{err: ssologin.ErrUnparsed},
		get: func(id string) (*session.Session, bool) { return boundSess(id), true }}
	_, err := e.SSOLogin("aws-1", "")
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
		if _, err := e.Grant(id, broker.ElevateRequest{Minutes: 30, Reason: "x", Cred: broker.CredCookie}); !errors.Is(err, web.ErrElevationUnknown) {
			t.Errorf("Grant(%s): %v", id, err)
		}
		if _, err := e.Drop(id, broker.CredCookie); !errors.Is(err, web.ErrElevationUnknown) {
			t.Errorf("Drop(%s): %v", id, err)
		}
		if _, err := e.SSOLogin(id, ""); !errors.Is(err, web.ErrElevationUnknown) {
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

// An exited session that is still listed: no new grant, no sign-in, but Drop
// still lowers power (the overlay outlives the session until it expires).
func TestDropWorksForAnExitedSessionButNothingElseDoes(t *testing.T) {
	sess := boundSess("aws-1")
	exited := true
	e := elevationService{brk: testBroker(t), get: func(string) (*session.Session, bool) { return sess, true },
		exited: func(*session.Session) bool { return exited }}
	if _, err := e.Drop("aws-1", broker.CredCookie); err != nil {
		t.Fatalf("Drop of an exited session: %v", err)
	}
	if _, err := e.Grant("aws-1", broker.ElevateRequest{Minutes: 30, Reason: "x", Cred: broker.CredCookie}); !errors.Is(err, web.ErrElevationUnknown) {
		t.Errorf("Grant of an exited session: %v", err)
	}
	if _, err := e.SSOLogin("aws-1", ""); !errors.Is(err, web.ErrElevationUnknown) {
		t.Errorf("SSOLogin of an exited session: %v", err)
	}
	host := boundSess("h-1")
	host.Runtime = config.RuntimeHost
	e.get = func(string) (*session.Session, bool) { return host, true }
	if _, err := e.Drop("h-1", broker.CredCookie); !errors.Is(err, web.ErrElevationUnknown) {
		t.Errorf("Drop on a host-tier session: %v", err)
	}
}

// The command a human pastes into a shell is built only from a name the sign-in
// accepts: a sidecar-restored binding is not re-validated at load.
func TestSignInCommandOnlyForAValidProfile(t *testing.T) {
	bad := boundSess("aws-1")
	bad.AWS.SourceProfile = "x; rm -rf ~"
	r := &fakeRunner{status: &ssologin.State{State: "waiting", Code: "ABCD-EFGH"}}
	e := elevationService{brk: testBroker(t), runner: r, get: func(string) (*session.Session, bool) { return bad, true }}
	_, err := e.SSOLogin("aws-1", "")
	var le *web.SSOLoginError
	if !errors.As(err, &le) || le.Command != "" {
		t.Fatalf("an invalid profile must carry no command: %v", err)
	}
	if r.profile != "" {
		t.Errorf("the runner was started with %q", r.profile)
	}
	e.runner = nil
	if _, err := e.SSOLogin("aws-1", ""); !errors.As(err, &le) || le.Command != "" {
		t.Fatalf("no CLI and an invalid profile: %v", err)
	}
	e.runner = r
	if st, ok := e.ssoStatus("aws-1"); ok && st.Command != "" {
		t.Errorf("status carries %q for an invalid profile", st.Command)
	}
	for _, p := range []string{"-x", "a b", "a$(b)", ""} {
		if hostLoginCommand(p) != "" {
			t.Errorf("hostLoginCommand(%q) built a command", p)
		}
	}
	if hostLoginCommand("sandbox-admin") != "aws sso login --profile sandbox-admin" {
		t.Error("a valid profile must build the command")
	}
}

// GET .../aws polls grantContext every 3 s during a sign-in: it must not list
// boxes for a session nothing can be granted on.
func TestGrantContextSkipsTheBoxListingWhereNothingCanBeGranted(t *testing.T) {
	calls := 0
	blocker := func(context.Context, string, string) (string, error) { calls++; return "", nil }
	exitedSess, noElev, host := boundSess("e-1"), boundSess("n-1"), boundSess("h-1")
	noElev.AWS.Elevate = nil
	host.Runtime = config.RuntimeHost
	sessions := map[string]*session.Session{"e-1": exitedSess, "n-1": noElev, "h-1": host, "ok": boundSess("ok")}
	e := elevationService{get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok },
		blocker: blocker, exited: func(s *session.Session) bool { return s == exitedSess }}
	for _, id := range []string{"e-1", "n-1", "h-1", "missing"} {
		if gc := e.grantContext(id); calls != 0 || gc.HostSessions == nil || gc.Blocked != "" {
			t.Fatalf("%s: listed boxes (%d calls) or returned %+v", id, calls, gc)
		}
	}
	e.grantContext("ok")
	if calls != 1 {
		t.Fatalf("a live elevatable session must be checked once, got %d", calls)
	}
}

func connSess(id string) *session.Session {
	c := boundSess(id)
	a := config.AWSConnection{Name: "a", AWSBinding: *c.AWS}
	a.SourceProfile = "prof-a"
	b := config.AWSConnection{Name: "b", AWSBinding: *c.AWS}
	b.SourceProfile, b.Elevate = "prof-b", nil
	c.AWSConns = []config.AWSConnection{a, b}
	c.AWS = nil
	return c
}

// Sign-in by name resolves source_profile from the session's RECORDED
// connection, never from the request; a missing or unbound name is refused
// before any process starts.
func TestSSOLoginByNameUsesRecordedProfile(t *testing.T) {
	r := &fakeRunner{}
	sessions := map[string]*session.Session{"c-1": connSess("c-1"), "aws-1": boundSess("aws-1")}
	e := elevationService{brk: testBroker(t), runner: r, get: func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok }}
	if _, err := e.SSOLogin("c-1", "b"); err != nil || r.profile != "prof-b" || r.key != "prof-b" {
		t.Fatalf("b: %v runner %q/%q", err, r.key, r.profile)
	}
	if _, err := e.SSOLogin("c-1", "a"); err != nil || r.profile != "prof-a" {
		t.Fatalf("a: %v runner %q", err, r.profile)
	}
	r.profile = ""
	for _, c := range []struct{ id, name string }{{"c-1", ""}, {"c-1", "zzz"}, {"c-1", "Bad Name!"}, {"aws-1", "a"}} {
		_, err := e.SSOLogin(c.id, c.name)
		if !errors.Is(err, web.ErrConnectionName) || r.profile != "" {
			t.Errorf("%s/%q: %v (runner %q)", c.id, c.name, err, r.profile)
		}
	}
	// The completion is audited against that connection.
	r.done(true, "")
}

// A session bound by named connections is elevatable and signs in per
// connection: grant and drop reach the broker, the status and grant panel
// answer for it.
func TestConnectionBoundSessionElevatesPerConnection(t *testing.T) {
	c := connSess("c-1")
	r := &fakeRunner{}
	calls := 0
	e := elevationService{brk: testBroker(t), runner: r, get: func(id string) (*session.Session, bool) { return c, id == "c-1" },
		blocker: func(context.Context, string, string) (string, error) { calls++; return "", nil }}
	if _, ok := e.bound("c-1"); !ok {
		t.Fatal("a connection-bound live boxed session must count as bound")
	}
	if _, ok := e.ssoStatus("c-1"); ok {
		t.Error("nothing expired and no flow: no sso object")
	}
	r.status = &ssologin.State{State: "waiting", Code: "ABCD-EFGH"}
	st, ok := e.ssoStatus("c-1")
	if !ok || len(st.Sources) != 2 {
		t.Fatalf("one entry per source: %+v %v", st, ok)
	}
	if s0 := st.Sources[0]; s0.Source != "prof-a" || !slices.Equal(s0.Names, []string{"a"}) || s0.Command != "aws sso login --profile prof-a" ||
		s0.Login == nil || s0.Expired {
		t.Fatalf("source a: %+v", s0)
	}
	gc := e.grantContext("c-1")
	if calls != 1 || gc.HostSessions == nil || gc.Blocked != "" {
		t.Fatalf("a session with a write-capable connection fills the panel: %+v (%d calls)", gc, calls)
	}
	if _, err := e.Drop("c-1", broker.CredCookie); err != nil {
		t.Errorf("Drop: %v", err)
	}
}
