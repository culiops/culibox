package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/ssologin"
	"github.com/culiops/agent-box/internal/web"
)

// recMinter records every mint and hands out a throwaway credential. It also
// names its SSO session, like STSMinter.
type recMinter struct {
	mu  sync.Mutex
	in  []broker.MintInput
	key string
}

func (m *recMinter) Mint(_ context.Context, in broker.MintInput) (broker.Creds, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in = append(m.in, in)
	return broker.Creds{AccessKeyID: "AKIAFAKE", SecretAccessKey: "x", SessionToken: "t"}, nil
}

func (m *recMinter) SourceKey(_ context.Context, profile string) (string, error) {
	if m.key != "" {
		return m.key, nil
	}
	return profile, nil
}

type denySim struct{}

func (denySim) Simulate(_ context.Context, _ aws.Credentials, _, _ string, actions []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range actions {
		out[a] = "implicitDeny"
	}
	return out, nil
}

func TestAWSCredServiceCheckSessionName(t *testing.T) {
	m := &recMinter{}
	s := &awsCredService{minter: m, sim: denySim{}}
	target := broker.ProbeTarget{SourceProfile: "p", Region: "us-east-1", RoleARN: "arn:aws:iam::123456789012:role/R"}
	r1 := s.Check(context.Background(), target)
	r2 := s.Check(context.Background(), target)
	if r1.State != awsprobe.NoneFound || r2.State != awsprobe.NoneFound {
		t.Fatalf("%+v %+v", r1, r2)
	}
	if len(m.in) != 2 {
		t.Fatalf("mints %d", len(m.in))
	}
	for _, in := range m.in {
		// Same as a spawn: the session policy is attached and the name is a
		// valid RoleSessionName.
		if !strings.HasPrefix(in.SessionName, "agentbox-check-") || len(in.SessionName) != len("agentbox-check-")+8 {
			t.Fatalf("session name %q", in.SessionName)
		}
		if in.Policy == "" || in.RoleARN != target.RoleARN || in.SourceProfile != "p" || in.Region != "us-east-1" {
			t.Fatalf("mint input %+v", in)
		}
	}
	if m.in[0].SessionName == m.in[1].SessionName {
		t.Fatal("two checks share a session name")
	}
}

func TestAWSCredServiceCheckTimesOut(t *testing.T) {
	s := &awsCredService{minter: blockMinter{}, sim: denySim{}, checkTimeout: 20 * time.Millisecond}
	res := s.Check(context.Background(), broker.ProbeTarget{SourceProfile: "p", Region: "us-east-1", RoleARN: "arn:aws:iam::123456789012:role/R"})
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonTimeout {
		t.Fatalf("%+v", res)
	}
}

type blockMinter struct{}

func (blockMinter) Mint(ctx context.Context, _ broker.MintInput) (broker.Creds, error) {
	<-ctx.Done()
	return broker.Creds{}, ctx.Err()
}

// fakeIAM serves canned pages of roles, then the canned error.
type fakeIAM struct {
	pages [][]string
	trust map[string]string // role name -> AssumeRolePolicyDocument (URL-encoded, as IAM returns it)
	err   error
	calls int
	got   []*iam.ListRolesInput
}

func (f *fakeIAM) ListRoles(_ context.Context, in *iam.ListRolesInput, _ ...func(*iam.Options)) (*iam.ListRolesOutput, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	i := f.calls
	f.calls++
	out := &iam.ListRolesOutput{}
	for _, n := range f.pages[i] {
		role := iamtypes.Role{RoleName: aws.String(n), Arn: aws.String("arn:aws:iam::123456789012:role/" + n)}
		if d, ok := f.trust[n]; ok {
			role.AssumeRolePolicyDocument = aws.String(d)
		}
		out.Roles = append(out.Roles, role)
	}
	if i+1 < len(f.pages) {
		out.IsTruncated = true
		out.Marker = aws.String("m" + string(rune('0'+i)))
	}
	return out, nil
}

// hostConfig points the SDK at a config file with one profile and no real
// credentials anywhere, so a test can never reach the host's AWS config.
func hostConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("[profile p]\naws_access_key_id = AKIAFAKE\naws_secret_access_key = fake\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "")
}

func rolesSvc(f *fakeIAM) *awsCredService {
	return &awsCredService{newIAM: func(aws.Config) iamListRoles { return f }}
}

func TestRolesPagesAndCaps(t *testing.T) {
	hostConfig(t)
	f := &fakeIAM{pages: [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}}}
	res := rolesSvc(f).Roles(context.Background(), "p")
	if len(res.Roles) != 6 || res.Roles[0].Name != "a" || res.Roles[5].ARN != "arn:aws:iam::123456789012:role/f" || res.AccessDenied || res.SSOExpired || res.Failed {
		t.Fatalf("%+v", res)
	}
	if f.calls != 3 || f.got[1].Marker == nil || *f.got[1].Marker != "m0" {
		t.Fatalf("paging: calls=%d", f.calls)
	}

	if res.Capped {
		t.Fatal("a whole listing is marked capped")
	}

	f = &fakeIAM{pages: [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}}}
	s := rolesSvc(f)
	s.rolesCap = 3
	res = s.Roles(context.Background(), "p")
	if len(res.Roles) != 3 || res.Roles[2].Name != "c" || !res.Capped {
		t.Fatalf("cap: %+v", res)
	}
	if f.calls != 3 {
		t.Fatalf("the listing stopped at the cap instead of scanning on: %d calls", f.calls)
	}

	// The scan itself is bounded: it stops at scanCap and says so.
	f = &fakeIAM{pages: [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}}}
	s = rolesSvc(f)
	s.scanCap = 4
	res = s.Roles(context.Background(), "p")
	if f.calls != 2 || len(res.Roles) != 4 || !res.Capped {
		t.Fatalf("scan cap: calls=%d %+v", f.calls, res)
	}
}

// The agent role must survive the cap: in a company account it can sit on
// page three behind a thousand service roles.
func TestRolesPutsAgentRolesFirstBeforeTheCap(t *testing.T) {
	hostConfig(t)
	f := &fakeIAM{
		pages: [][]string{{"Zeta", "aws-ecs-task", "beta"}, {"svc-1", "AgentBox-x"}, {"agentbox-agent", "alpha"}},
		trust: map[string]string{"beta": url.QueryEscape(ssoTrustDoc)},
	}
	s := rolesSvc(f)
	s.rolesCap = 3
	res := s.Roles(context.Background(), "p")
	var names []string
	for _, r := range res.Roles {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "agentbox-agent,AgentBox-x,beta" {
		t.Fatalf("order = %s, want agentbox-* first, then SSO-trusting, then by name", got)
	}
	if !res.Roles[2].SSOTrust || res.Roles[0].SSOTrust || res.Roles[1].SSOTrust {
		t.Fatalf("sso_trust = %+v", res.Roles)
	}
	if !res.Capped {
		t.Fatal("not marked capped")
	}
}

const ssoTrustDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},` +
	`"Action":["sts:AssumeRole","sts:SetSourceIdentity"],"Condition":{"ArnLike":{"aws:PrincipalArn":` +
	`["arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/*AWSReservedSSO_Lab_*"]}}}]}`

func TestSSOTrustHint(t *testing.T) {
	const acct = "123456789012"
	stmt := func(effect, principal string) string {
		return `{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `","Principal":` + principal + `,"Action":"sts:AssumeRole"}]}`
	}
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{"agent role (root + SSO condition)", ssoTrustDoc, true},
		{"SSO role principal", stmt("Allow", `{"AWS":["arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/ap-southeast-1/AWSReservedSSO_Admin_abc"]}`), true},
		{"account root", stmt("Allow", `{"AWS":"arn:aws:iam::123456789012:root"}`), true},
		{"bare account id", stmt("Allow", `{"AWS":"123456789012"}`), true},
		{"service role", stmt("Allow", `{"Service":"ecs-tasks.amazonaws.com"}`), false},
		{"other account root", stmt("Allow", `{"AWS":"arn:aws:iam::999999999999:root"}`), false},
		{"deny only", stmt("Deny", `{"AWS":"arn:aws:iam::123456789012:root"}`), false},
		{"SAML federation", stmt("Allow", `{"Federated":"arn:aws:iam::123456789012:saml-provider/x"}`), false},
		{"empty", "", false},
		{"not JSON", "%%%", false},
	}
	for _, c := range cases {
		if got := ssoTrust(url.QueryEscape(c.doc), acct); got != c.want {
			t.Errorf("%s: ssoTrust = %v, want %v", c.name, got, c.want)
		}
	}
	// IAM returns the document URL-encoded; a plain one must parse too.
	if !ssoTrust(ssoTrustDoc, acct) {
		t.Error("an unencoded document was not read")
	}
}

func TestRolesErrorOutcomes(t *testing.T) {
	hostConfig(t)
	cases := []struct {
		name string
		err  error
		want web.RolesResult
	}{
		{"access denied", &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized: arn:aws:sts::1:assumed-role/Secret/person"}, web.RolesResult{AccessDenied: true}},
		{"access denied exception", &smithy.GenericAPIError{Code: "AccessDeniedException"}, web.RolesResult{AccessDenied: true}},
		{"sso expired", errors.New("failed to refresh cached SSO token: the SSO session has expired"), web.RolesResult{SSOExpired: true}},
		{"other", errors.New("dial tcp: lookup iam.amazonaws.com: no such host"), web.RolesResult{Failed: true}},
	}
	for _, c := range cases {
		got := rolesSvc(&fakeIAM{err: c.err}).Roles(context.Background(), "p")
		if got.AccessDenied != c.want.AccessDenied || got.SSOExpired != c.want.SSOExpired || got.Failed != c.want.Failed || len(got.Roles) != 0 {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// A profile the host config does not define is a failure, not a panic.
	if got := rolesSvc(&fakeIAM{}).Roles(context.Background(), "missing"); !got.Failed {
		t.Errorf("missing profile: %+v", got)
	}
}

func TestRolesNeverReadsAnotherProfile(t *testing.T) {
	hostConfig(t)
	var region string
	s := &awsCredService{newIAM: func(c aws.Config) iamListRoles { region = c.Region; return &fakeIAM{pages: [][]string{{"a"}}} }}
	if got := s.Roles(context.Background(), "p"); len(got.Roles) != 1 || region != "us-east-1" {
		t.Fatalf("%+v region=%q", got, region)
	}
}

type recCred struct {
	mu     sync.Mutex
	events []broker.CredEvent
}

func (r *recCred) RecordCred(e broker.CredEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func TestSSOStartKeyAndResultAudit(t *testing.T) {
	run := &fakeRunner{}
	rec := &recCred{}
	s := &awsCredService{minter: &recMinter{key: "corp"}, sso: run, rec: rec}
	st, err := s.SSOStart("dev")
	if err != nil || st.Code != "ABCD-EFGH" {
		t.Fatalf("%v %+v", err, st)
	}
	// Profiles on one sso_session share one flow key.
	if run.key != "corp" || run.profile != "dev" {
		t.Fatalf("runner got %q/%q", run.key, run.profile)
	}
	run.done(true, "signed in")
	if len(rec.events) != 1 {
		t.Fatalf("%+v", rec.events)
	}
	e := rec.events[0]
	if e.Action != broker.CredSSOResult || e.Kind != "aws" || !strings.Contains(e.Detail, "dev") || !strings.Contains(e.Detail, "signed in") {
		t.Fatalf("%+v", e)
	}
	run.status = &ssologin.State{State: ssologin.StateOK}
	if st, ok := s.SSOStatus("dev"); !ok || st.State != ssologin.StateOK {
		t.Fatalf("status %+v %v", st, ok)
	}
}

func TestSSOStartWithoutTheCLIOffersTheCommand(t *testing.T) {
	s := &awsCredService{minter: &recMinter{}}
	_, err := s.SSOStart("dev")
	var le *web.SSOLoginError
	if !errors.As(err, &le) || le.Command != "aws sso login --profile dev" {
		t.Fatalf("%v", err)
	}
	if _, ok := s.SSOStatus("dev"); ok {
		t.Fatal("status without a runner")
	}
	run := &fakeRunner{err: ssologin.ErrUnparsed}
	s.sso = run
	if _, err := s.SSOStart("dev"); !errors.As(err, &le) || le.Command == "" {
		t.Fatalf("%v", err)
	}
}

func TestAWSServiceForNoBrokerIsUntypedNil(t *testing.T) {
	chk, svc := awsServiceFor(nil, &recCred{})
	if chk != nil || svc != nil {
		t.Fatalf("a missing broker must read as nil interfaces: %v %v", chk, svc)
	}
	b := &brokerService{Minter: &recMinter{}, Sim: denySim{}}
	chk, svc = awsServiceFor(b, &recCred{})
	if chk == nil || svc == nil {
		t.Fatal("a running broker has a service")
	}
	// A broker whose aws CLI is missing: SSO stays a nil interface.
	if _, err := svc.SSOStart("dev"); err == nil {
		t.Fatal("no CLI, no sign-in")
	}
}
