package doctor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

// fakeAWS answers the aws CLI calls checkAWSConns makes. profiles is what
// `aws configure list-profiles` prints; sso maps a profile to its sso_session;
// expired lists profiles whose sts call fails.
type fakeAWS struct {
	mu       sync.Mutex
	profiles []string
	sso      map[string]string
	expired  map[string]bool
	calls    []string
}

func (f *fakeAWS) Run(_ context.Context, _ []string, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case len(args) >= 2 && args[0] == "configure" && args[1] == "list-profiles":
		return strings.Join(f.profiles, "\n") + "\n", nil
	case len(args) >= 5 && args[0] == "configure" && args[1] == "get":
		if args[2] == "sso_session" {
			if s, ok := f.sso[args[4]]; ok {
				return s + "\n", nil
			}
		}
		return "", errors.New("exit status 1")
	case len(args) >= 3 && args[0] == "sts":
		if f.expired[args[3]] {
			return "", errors.New("The SSO session associated with this profile has expired")
		}
		return "111111111111\n", nil
	}
	return "", errors.New("unexpected: " + strings.Join(args, " "))
}

func (f *fakeAWS) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func conn(name, src string, elevate bool) config.AWSConnection {
	c := config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{
		SourceProfile: src, RoleARN: "arn:aws:iam::111111111111:role/agent", Region: "ap-southeast-1"}}
	if elevate {
		c.Elevate = &config.AWSElevate{RoleARN: "arn:aws:iam::111111111111:role/agent-write"}
	}
	return c
}

func TestDoctorAWSConnLines(t *testing.T) {
	f := &fakeAWS{
		profiles: []string{"corp-sso", "legacy"},
		sso:      map[string]string{"corp-sso": "corp"},
	}
	c := config.Config{AWSConnections: []config.AWSConnection{
		conn("staging", "corp-sso", false),
		conn("prod", "corp-sso", true),
		conn("gone", "no-such-profile", false),
		conn("old", "legacy", false),
	}}
	got := map[string]Result{}
	for _, r := range noBase(checkAWSConns(context.Background(), c, nil, nil, f, noneProbe)) {
		got[r.Check] = r
	}
	if len(got) != 4 {
		t.Fatalf("want one line per connection, got %v", got)
	}
	want := map[string][2]string{
		"aws-connection staging": {Pass, "source_profile corp-sso ok; sso valid; elevate no"},
		"aws-connection prod":    {Pass, "source_profile corp-sso ok; sso valid; elevate yes"},
		"aws-connection gone":    {Fail, "source_profile no-such-profile missing; sso n/a; elevate no"},
		"aws-connection old":     {Pass, "source_profile legacy ok; sso n/a; elevate no"},
	}
	for k, w := range want {
		if got[k].Status != w[0] || got[k].Detail != w[1] {
			t.Errorf("%s = %s %q, want %s %q", k, got[k].Status, got[k].Detail, w[0], w[1])
		}
	}
	// staging and prod share the sso_session "corp": one sign-in check.
	if n := f.count("sts "); n != 1 {
		t.Errorf("sts calls = %d, want 1 (SSO check deduped by sourceKey); calls=%v", n, f.calls)
	}
}

func TestDoctorAWSConnExpiredSSO(t *testing.T) {
	f := &fakeAWS{
		profiles: []string{"a", "b"},
		sso:      map[string]string{"a": "corp", "b": "corp"},
		expired:  map[string]bool{"a": true},
	}
	c := config.Config{AWSConnections: []config.AWSConnection{conn("one", "a", false), conn("two", "b", false)}}
	for _, r := range noBase(checkAWSConns(context.Background(), c, nil, nil, f, noneProbe)) {
		if r.Status != Warn || !strings.Contains(r.Detail, "sso expired") {
			t.Errorf("%s = %s %q, want WARN with sso expired for both on one session", r.Check, r.Status, r.Detail)
		}
	}
}

func TestDoctorAWSConnNone(t *testing.T) {
	f := &fakeAWS{}
	if got := checkAWSConns(context.Background(), config.Config{}, nil, nil, f, noneProbe); len(got) != 0 {
		t.Errorf("no connections should print nothing, got %v", got)
	}
	if len(f.calls) != 0 {
		t.Errorf("no connections should not run aws, calls=%v", f.calls)
	}
}

func TestDoctorAWSConnNoCLI(t *testing.T) {
	c := config.Config{AWSConnections: []config.AWSConnection{conn("x", "p", false)}}
	rs := noBase(checkAWSConns(context.Background(), c, nil, nil, failRunner{}, noneProbe))
	if len(rs) != 1 || rs[0].Status != Warn || !strings.Contains(rs[0].Detail, "cannot check") {
		t.Errorf("got %v, want one WARN 'cannot check'", rs)
	}
}

type failRunner struct{}

func (failRunner) Run(context.Context, []string, string, ...string) (string, error) {
	return "", errors.New("aws: executable file not found in $PATH")
}

// noBase drops the base-role lines, so the older tests keep asserting only on
// the connection lines.
func noBase(rs []Result) []Result {
	var out []Result
	for _, r := range rs {
		if !strings.HasSuffix(r.Check, " base role") {
			out = append(out, r)
		}
	}
	return out
}

// fakeProbe answers by role ARN and counts calls.
type fakeProbe struct {
	mu    sync.Mutex
	byARN map[string]awsprobe.Result
	calls []broker.ProbeTarget
}

func (f *fakeProbe) probe(_ context.Context, t broker.ProbeTarget) awsprobe.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, t)
	if r, ok := f.byARN[t.RoleARN]; ok {
		return r
	}
	return awsprobe.Result{State: awsprobe.NoneFound}
}

func noneProbe(context.Context, broker.ProbeTarget) awsprobe.Result {
	return awsprobe.Result{State: awsprobe.NoneFound}
}

func roleARN(name string) string { return "arn:aws:iam::111111111111:role/" + name }

func connRole(name, src, role string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{
		SourceProfile: src, RoleARN: roleARN(role), Region: "ap-southeast-1"}}
}

func byCheck(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.Check] = r
	}
	return m
}

func TestDoctorBaseRoleLines(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}}
	fp := &fakeProbe{byARN: map[string]awsprobe.Result{
		roleARN("agent-w"): {State: awsprobe.Writes, Actions: []string{"s3:PutObject", "ec2:RunInstances"}},
		roleARN("agent-n"): {State: awsprobe.NoneFound},
		roleARN("agent-u"): awsprobe.Fail(awsprobe.ReasonSimulateDenied, time.Time{}),
	}}
	c := config.Config{AWSConnections: []config.AWSConnection{
		connRole("w", "p", "agent-w"), connRole("n", "p", "agent-n"), connRole("u", "p", "agent-u")}}
	rs := checkAWSConns(context.Background(), c, nil, nil, f, fp.probe)
	got := byCheck(rs)
	want := map[string][2]string{
		"aws-connection w base role": {Warn, `base role agent-w can write (s3:PutObject, ec2:RunInstances); read-only must come from the role — see docs/AWS.md "Several AWS accounts in one session"`},
		"aws-connection n base role": {Pass, "base role agent-n: no broad write grant found (scoped grants are not detected)"},
		"aws-connection u base role": {Warn, "could not check base role agent-u: role cannot simulate itself (grant the role iam:SimulatePrincipalPolicy on itself)"},
	}
	for k, w := range want {
		if got[k].Status != w[0] || got[k].Detail != w[1] {
			t.Errorf("%s = %s %q, want %s %q", k, got[k].Status, got[k].Detail, w[0], w[1])
		}
	}
	for _, r := range rs {
		if r.Status == Fail {
			t.Errorf("%s is FAIL; the probe must never fail doctor", r.Check)
		}
	}
	// each connection's line is followed by its base role line.
	order := []string{"aws-connection w", "aws-connection w base role", "aws-connection n", "aws-connection n base role",
		"aws-connection u", "aws-connection u base role"}
	if len(rs) != len(order) {
		t.Fatalf("got %d lines, want %d: %v", len(rs), len(order), rs)
	}
	for i, r := range rs {
		if r.Check != order[i] {
			t.Errorf("line %d = %q, want %q", i, r.Check, order[i])
		}
	}
}

func TestDoctorBaseRoleSkipped(t *testing.T) {
	f := &fakeAWS{
		profiles: []string{"corp"},
		sso:      map[string]string{"corp": "corp"},
		expired:  map[string]bool{"corp": true},
	}
	fp := &fakeProbe{}
	c := config.Config{AWSConnections: []config.AWSConnection{
		connRole("gone", "no-such-profile", "r1"), connRole("exp", "corp", "r2")}}
	got := byCheck(checkAWSConns(context.Background(), c, nil, nil, f, fp.probe))
	if g := got["aws-connection gone base role"]; g.Status != Warn || g.Detail != "base role not checked: source_profile no-such-profile missing" {
		t.Errorf("gone = %s %q", g.Status, g.Detail)
	}
	if g := got["aws-connection exp base role"]; g.Status != Warn || g.Detail != "base role not checked: sso expired" {
		t.Errorf("exp = %s %q", g.Status, g.Detail)
	}
	if len(fp.calls) != 0 {
		t.Errorf("a skipped base role must not be probed, calls=%v", fp.calls)
	}
}

func TestDoctorBaseRoleProbesWithoutCLI(t *testing.T) {
	fp := &fakeProbe{byARN: map[string]awsprobe.Result{
		roleARN("r"): {State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}}}
	c := config.Config{AWSConnections: []config.AWSConnection{connRole("x", "p", "r")}}
	got := byCheck(checkAWSConns(context.Background(), c, nil, nil, failRunner{}, fp.probe))
	if len(fp.calls) != 1 {
		t.Fatalf("probe calls = %d, want 1 even without the aws CLI", len(fp.calls))
	}
	if g := got["aws-connection x base role"]; g.Status != Warn || !strings.Contains(g.Detail, "can write (s3:PutObject)") {
		t.Errorf("base line = %s %q", g.Status, g.Detail)
	}
	if g := got["aws-connection x"]; g.Status != Warn || !strings.Contains(g.Detail, "cannot check") {
		t.Errorf("connection line changed: %s %q", g.Status, g.Detail)
	}
}

func TestDoctorBaseRoleDedupe(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}}
	fp := &fakeProbe{}
	c := config.Config{AWSConnections: []config.AWSConnection{connRole("a", "p", "r"), connRole("b", "p", "r")}}
	stored := []config.AgentConfig{{Name: "legacy", AWS: &config.AWSBinding{SourceProfile: "p", RoleARN: roleARN("r"), Region: "ap-southeast-1"}}}
	rs := checkAWSConns(context.Background(), c, stored, nil, f, fp.probe)
	if len(fp.calls) != 1 {
		t.Errorf("probe calls = %d, want 1 for one (source_profile, role_arn)", len(fp.calls))
	}
	got := byCheck(rs)
	for _, k := range []string{"aws-connection a base role", "aws-connection b base role", "aws-binding legacy"} {
		if got[k].Detail != "base role r: no broad write grant found (scoped grants are not detected)" {
			t.Errorf("%s = %s %q", k, got[k].Status, got[k].Detail)
		}
	}
}

func awsAgent(name, src, role string) config.AgentConfig {
	return config.AgentConfig{Name: name, AWS: &config.AWSBinding{SourceProfile: src, RoleARN: roleARN(role), Region: "ap-southeast-1"}}
}

func TestDoctorLegacyBindings(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}}
	fp := &fakeProbe{byARN: map[string]awsprobe.Result{
		roleARN("seed-role"):  {State: awsprobe.Writes, Actions: []string{"s3:PutObject"}},
		roleARN("store-role"): {State: awsprobe.NoneFound},
	}}
	c := config.Config{Agents: []config.AgentConfig{
		awsAgent("a", "p", "seed-role"), {Name: "c", Bin: "x"}}}
	stored := []config.AgentConfig{awsAgent("b", "gone", "other"), awsAgent("a", "p", "store-role")}
	rs := checkAWSConns(context.Background(), c, stored, nil, f, fp.probe)
	if len(rs) != 2 {
		t.Fatalf("want lines for a and b only, got %v", rs)
	}
	if rs[0].Check != "aws-binding a" || rs[0].Status != Pass || !strings.HasPrefix(rs[0].Detail, "base role store-role:") {
		t.Errorf("a = %v, want the store's role (store overrides the seed)", rs[0])
	}
	if rs[1].Check != "aws-binding b" || rs[1].Status != Warn || rs[1].Detail != "base role not checked: source_profile gone missing" {
		t.Errorf("b = %v", rs[1])
	}
}

func TestDoctorStoreUnreadable(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}}
	fp := &fakeProbe{}
	c := config.Config{Agents: []config.AgentConfig{awsAgent("a", "p", "r")}}
	rs := checkAWSConns(context.Background(), c, []config.AgentConfig{awsAgent("ignored", "p", "x")},
		errors.New("store: parse /x: bad\nmore"), f, fp.probe)
	got := byCheck(rs)
	g := got["aws-binding profiles"]
	if g.Status != Warn || g.Detail != "could not read the profile store (store: parse /x: bad); its profiles were not checked" {
		t.Errorf("store line = %s %q", g.Status, g.Detail)
	}
	if _, ok := got["aws-binding a"]; !ok {
		t.Errorf("config agent a was not checked: %v", rs)
	}
	if _, ok := got["aws-binding ignored"]; ok {
		t.Errorf("profiles from an unreadable store must not be checked")
	}
}

func TestDoctorAWSNoBindingsNoCalls(t *testing.T) {
	f := &fakeAWS{}
	fp := &fakeProbe{}
	c := config.Config{Agents: []config.AgentConfig{{Name: "c", Bin: "x"}}}
	if got := checkAWSConns(context.Background(), c, []config.AgentConfig{{Name: "d"}}, nil, f, fp.probe); len(got) != 0 {
		t.Errorf("want no output, got %v", got)
	}
	if len(f.calls) != 0 || len(fp.calls) != 0 {
		t.Errorf("want no runner or probe calls, got %v / %v", f.calls, fp.calls)
	}
}

// recMinter records the MintInput and fails, so the probe stops after the mint.
type recMinter struct {
	in  broker.MintInput
	dl  bool
	err error
}

func (m *recMinter) Mint(ctx context.Context, in broker.MintInput) (broker.Creds, error) {
	m.in = in
	_, m.dl = ctx.Deadline()
	return broker.Creds{}, m.err
}

type panicSim struct{}

func (panicSim) Simulate(context.Context, aws.Credentials, string, string, []string) (map[string]string, error) {
	return nil, errors.New("not reached")
}

func TestAWSProberMintsAsDoctorWithDeadline(t *testing.T) {
	m := &recMinter{err: broker.ErrSSOExpired}
	p := newAWSProber(m, panicSim{})
	res := p(context.Background(), broker.ProbeTarget{SourceProfile: "p", Region: "ap-southeast-1", RoleARN: roleARN("r")})
	if m.in.SessionName != "agentbox-doctor" {
		t.Errorf("session name = %q, want agentbox-doctor", m.in.SessionName)
	}
	if !m.dl {
		t.Errorf("the mint ctx must carry the %v probe timeout", awsProbeTimeout)
	}
	if res.State != awsprobe.Unverified || res.Reason != awsprobe.ReasonSSOExpired {
		t.Errorf("result = %+v, want unverified sso_expired", res)
	}
}

// fakeGate answers by connection name ("" = an inline binding) and counts calls.
type fakeGate struct {
	mu    sync.Mutex
	by    map[string]awsprobe.GateResult
	calls []string
}

func (f *fakeGate) gate(_ context.Context, _ config.AWSBinding, conn string) awsprobe.GateResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, conn)
	return f.by[conn]
}

func ssoDirectConn(name, src string) config.AWSConnection {
	return config.AWSConnection{Name: name, AWSBinding: config.AWSBinding{SourceProfile: src, Region: "ap-southeast-1", Mode: config.AWSModeSSODirect}}
}

func TestDoctorSSODirectGateLines(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}, sso: map[string]string{"p": "corp"}}
	fg := &fakeGate{by: map[string]awsprobe.GateResult{
		"clean": {State: awsprobe.GateReadOnly, RoleARN: "arn:aws:iam::1:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_RO_x"},
		"bad": {State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{
			{Pattern: "s3:*", Why: "allows every S3 action"}, {Pattern: "ssm:SendCommand", Why: "runs commands"}}},
		"unk":  {State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSSOExpired},
		"perm": {State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms, MissingPerms: []string{"iam:GetRolePolicy"}},
		// A read-only verdict that names no role is not a proof.
		"norole": {State: awsprobe.GateReadOnly},
	}}
	fp := &fakeProbe{}
	c := config.Config{AWSConnections: []config.AWSConnection{
		ssoDirectConn("clean", "p"), ssoDirectConn("bad", "p"), ssoDirectConn("unk", "p"), ssoDirectConn("perm", "p"),
		ssoDirectConn("norole", "p"), connRole("ar", "p", "agent")}}
	got := byCheck(checkAWSConnsGate(context.Background(), c, nil, nil, f, fp.probe, fg.gate))

	pass := got["aws-connection clean read-only gate"]
	if pass.Status != Pass || !strings.Contains(pass.Detail, "read-only (sso_direct)") {
		t.Errorf("clean = %s %q", pass.Status, pass.Detail)
	}
	bad := got["aws-connection bad read-only gate"]
	if bad.Status != Fail || !strings.Contains(bad.Detail, "not read-only") || !strings.Contains(bad.Detail, "s3:*: allows every S3 action") ||
		!strings.Contains(bad.Detail, "ssm:SendCommand") {
		t.Errorf("bad = %s %q", bad.Status, bad.Detail)
	}
	unk := got["aws-connection unk read-only gate"]
	if unk.Status != Fail || !strings.Contains(unk.Detail, "could not verify") || !strings.Contains(unk.Detail, "SSO expired") {
		t.Errorf("unverified = %s %q", unk.Status, unk.Detail)
	}
	if p := got["aws-connection perm read-only gate"]; p.Status != Fail || !strings.Contains(p.Detail, "iam:GetRolePolicy") {
		t.Errorf("missing perms = %s %q", p.Status, p.Detail)
	}
	if n := got["aws-connection norole read-only gate"]; n.Status != Fail {
		t.Errorf("read-only without a role = %s %q", n.Status, n.Detail)
	}
	// The assume_role connection keeps its base-role line, and sso_direct ones have none.
	if _, ok := got["aws-connection ar base role"]; !ok {
		t.Error("assume_role connection lost its base-role line")
	}
	for _, name := range []string{"clean", "bad", "unk", "perm", "norole"} {
		if _, ok := got["aws-connection "+name+" base role"]; ok {
			t.Errorf("%s has a base-role line", name)
		}
	}
	if len(fp.calls) != 1 || fp.calls[0].RoleARN != roleARN("agent") {
		t.Errorf("base-role probes = %+v: an sso_direct target was probed as an assumed role", fp.calls)
	}
}

func TestDoctorSSODirectGateSkippedWhenSignInFails(t *testing.T) {
	f := &fakeAWS{profiles: []string{"a"}, sso: map[string]string{"a": "corp"}, expired: map[string]bool{"a": true}}
	fg := &fakeGate{}
	c := config.Config{AWSConnections: []config.AWSConnection{ssoDirectConn("x", "a"), ssoDirectConn("y", "gone")}}
	got := byCheck(checkAWSConnsGate(context.Background(), c, nil, nil, f, noneProbe, fg.gate))
	if r := got["aws-connection x read-only gate"]; r.Status != Warn || !strings.Contains(r.Detail, "sso expired") {
		t.Errorf("x = %s %q", r.Status, r.Detail)
	}
	if r := got["aws-connection y"]; r.Status != Fail {
		t.Errorf("y connection line = %s %q", r.Status, r.Detail)
	}
	if r := got["aws-connection y read-only gate"]; r.Status != Warn || !strings.Contains(r.Detail, "missing") {
		t.Errorf("y = %s %q", r.Status, r.Detail)
	}
	if len(fg.calls) != 0 {
		t.Errorf("gate ran with no usable sign-in: %v", fg.calls)
	}
}

func TestDoctorSSODirectInlineBindingAndNoGate(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}, sso: map[string]string{"p": "corp"}}
	stored := []config.AgentConfig{{Name: "ro-agent", AWS: &config.AWSBinding{SourceProfile: "p", Region: "r", Mode: config.AWSModeSSODirect}}}
	fg := &fakeGate{by: map[string]awsprobe.GateResult{"": {State: awsprobe.GateNotReadOnly}}}
	got := byCheck(checkAWSConnsGate(context.Background(), config.Config{}, stored, nil, f, noneProbe, fg.gate))
	if r := got["aws-binding ro-agent read-only gate"]; r.Status != Fail {
		t.Errorf("inline = %s %q", r.Status, r.Detail)
	}
	// The plain entry point has no gate: such a binding is reported, not skipped silently.
	got = byCheck(checkAWSConns(context.Background(), config.Config{}, stored, nil, f, noneProbe))
	if r := got["aws-binding ro-agent read-only gate"]; r.Status != Warn || !strings.Contains(r.Detail, "not run") {
		t.Errorf("no gate = %s %q", r.Status, r.Detail)
	}
}

// The real gate passes the check a deadline, so a hung mint cannot hang doctor.
func TestNewSSOGateBoundsTheCheck(t *testing.T) {
	var dl time.Duration
	g := newSSOGate(deadlineMinter{&dl})
	res := g(context.Background(), config.AWSBinding{SourceProfile: "p", Mode: config.AWSModeSSODirect}, "c")
	if res.State == awsprobe.GateReadOnly {
		t.Fatalf("a failing mint passed the gate: %+v", res)
	}
	if dl <= 0 || dl > awsGateTimeout {
		t.Fatalf("mint deadline %v, want within %v", dl, awsGateTimeout)
	}
}

type deadlineMinter struct{ dl *time.Duration }

func (m deadlineMinter) Mint(ctx context.Context, _ broker.MintInput) (broker.Creds, error) {
	if d, ok := ctx.Deadline(); ok {
		*m.dl = time.Until(d)
	}
	return broker.Creds{}, errors.New("no")
}

func ackedDoctorConn(name string, acked awsprobe.GateResult) config.AWSConnection {
	c := ssoDirectConn(name, "p")
	a := awsprobe.NewAck(acked, "alice", time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC))
	c.FullAccessAck = &a
	return c
}

func TestDoctorSSODirectFullAccessLines(t *testing.T) {
	const caller = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_abc"
	const role = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_Admin_abc"
	wide := func(p ...string) awsprobe.GateResult {
		r := awsprobe.GateResult{State: awsprobe.GateNotReadOnly, RoleARN: role, Caller: caller}
		for _, x := range p {
			r.Refused = append(r.Refused, awsprobe.Verdict{Pattern: x, Why: "not a read verb"})
		}
		return r
	}
	perm := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonMissingPerms, Caller: caller,
		MissingPerms: []string{"iam:GetRolePolicy"}}
	transient := awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonTimeout, Caller: caller}
	identity := wide("account")
	f := &fakeAWS{profiles: []string{"p"}, sso: map[string]string{"p": "corp"}}
	fg := &fakeGate{by: map[string]awsprobe.GateResult{
		"acked": wide("s3:Put*"), "stale": wide("s3:Put*", "iam:Create*"), "noack": wide("s3:Put*"),
		"permack": perm, "timeout": transient, "ident": identity,
	}}
	c := config.Config{AWSConnections: []config.AWSConnection{
		ackedDoctorConn("acked", wide("s3:Put*")), ackedDoctorConn("stale", wide("s3:Put*")), ssoDirectConn("noack", "p"),
		ackedDoctorConn("permack", perm), ackedDoctorConn("timeout", wide("s3:Put*")), ackedDoctorConn("ident", wide("s3:Put*"))}}
	got := byCheck(checkAWSConnsGate(context.Background(), c, nil, nil, f, noneProbe, fg.gate))
	line := func(n string) Result { return got["aws-connection "+n+" read-only gate"] }

	if r := line("acked"); r.Status != Warn || !strings.Contains(r.Detail, "full access acknowledged by alice 2026-11-25T10:00:00Z (sso_direct): no read-only default, no write window") ||
		!strings.Contains(r.Detail, "s3:Put*") {
		t.Errorf("acked = %s %q", r.Status, r.Detail)
	}
	if r := line("permack"); r.Status != Warn || !strings.Contains(r.Detail, "full access acknowledged") || !strings.Contains(r.Detail, "iam:GetRolePolicy") {
		t.Errorf("permack = %s %q", r.Status, r.Detail)
	}
	if r := line("stale"); r.Status != Fail || !strings.Contains(r.Detail, "changed since its full-access acknowledgement") ||
		!strings.Contains(r.Detail, "acknowledge again on the Credentials screen") || !strings.Contains(r.Detail, "iam:Create*") {
		t.Errorf("stale = %s %q", r.Status, r.Detail)
	}
	if r := line("noack"); r.Status != Fail || !strings.Contains(r.Detail, "acknowledge full access on the Credentials screen") {
		t.Errorf("noack = %s %q", r.Status, r.Detail)
	}
	for _, n := range []string{"timeout", "ident"} {
		if r := line(n); r.Status != Fail || strings.Contains(r.Detail, "acknowledg") {
			t.Errorf("%s = %s %q", n, r.Status, r.Detail)
		}
	}
}

// An inline binding has no connection record: it never reads as acknowledged
// and never gets an acknowledge hint.
func TestDoctorSSODirectInlineBindingNeverFullAccess(t *testing.T) {
	f := &fakeAWS{profiles: []string{"p"}, sso: map[string]string{"p": "corp"}}
	stored := []config.AgentConfig{{Name: "ro-agent", AWS: &config.AWSBinding{SourceProfile: "p", Region: "r", Mode: config.AWSModeSSODirect}}}
	fg := &fakeGate{by: map[string]awsprobe.GateResult{"": {State: awsprobe.GateNotReadOnly, Caller: "arn:aws:sts::123456789012:assumed-role/X",
		RoleARN: "arn:aws:iam::123456789012:role/X", Refused: []awsprobe.Verdict{{Pattern: "s3:Put*", Why: "w"}}}}}
	got := byCheck(checkAWSConnsGate(context.Background(), config.Config{}, stored, nil, f, noneProbe, fg.gate))
	if r := got["aws-binding ro-agent read-only gate"]; r.Status != Fail || strings.Contains(r.Detail, "acknowledg") {
		t.Errorf("inline = %s %q", r.Status, r.Detail)
	}
}
