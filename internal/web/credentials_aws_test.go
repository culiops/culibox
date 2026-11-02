package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/ssologin"
)

// fakeAWSSvc is an AWSCredService that records its calls.
type fakeAWSSvc struct {
	mu        sync.Mutex
	check     awsprobe.Result
	roles     RolesResult
	sso       ssologin.State
	ssoErr    error
	ssoStatus map[string]ssologin.State
	calls     []string
	targets   []broker.ProbeTarget
	onCheck   func()

	// The sso_direct gate: gate and gateExp are answered to every call; gated
	// records each call's connection (profile and region for the check route).
	gate    awsprobe.GateResult
	gateExp time.Time
	gated   []config.AWSConnection
	onGate  func()
}

func (f *fakeAWSSvc) runGate(c config.AWSConnection) (awsprobe.GateResult, time.Time) {
	f.note("gate")
	f.mu.Lock()
	f.gated = append(f.gated, c)
	hook, g, exp := f.onGate, f.gate, f.gateExp
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return g, exp
}

func (f *fakeAWSSvc) CheckSSODirect(_ context.Context, profile, region string) (awsprobe.GateResult, time.Time) {
	return f.runGate(config.AWSConnection{AWSBinding: config.AWSBinding{SourceProfile: profile, Region: region, Mode: config.AWSModeSSODirect}})
}

func (f *fakeAWSSvc) CheckSSODirectConn(_ context.Context, c config.AWSConnection) (awsprobe.GateResult, time.Time) {
	return f.runGate(c)
}

func (f *fakeAWSSvc) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeAWSSvc) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAWSSvc) Check(_ context.Context, t broker.ProbeTarget) awsprobe.Result {
	f.note("check")
	f.mu.Lock()
	f.targets = append(f.targets, t)
	hook := f.onCheck
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return f.check
}

func (f *fakeAWSSvc) Roles(_ context.Context, profile string) RolesResult {
	f.note("roles:" + profile)
	return f.roles
}

func (f *fakeAWSSvc) SSOStart(profile string) (ssologin.State, error) {
	f.note("sso:" + profile)
	return f.sso, f.ssoErr
}

func (f *fakeAWSSvc) SSOStatus(profile string) (ssologin.State, bool) {
	f.note("status:" + profile)
	st, ok := f.ssoStatus[profile]
	return st, ok
}

const (
	credOrigin = "http://" + host
	pathAWS    = "/api/credentials/aws"
)

type awsRoutes struct {
	srv *Server
	svc *fakeAWSSvc
	fx  *credFixture
}

func newAWSRoutes(t *testing.T) *awsRoutes {
	t.Helper()
	// GET host-profiles must never read the real ~/.aws/config in a test.
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "no-such-config"))
	fx := newCredFixture(t, config.Config{}, nil)
	svc := &fakeAWSSvc{check: awsprobe.Result{State: awsprobe.NoneFound}}
	fx.cm.chk = svc
	return &awsRoutes{srv: &Server{Creds: fx.cm, CredAWS: svc, CredAudit: fx.rec}, svc: svc, fx: fx}
}

func (a *awsRoutes) call(method, path, body string) (int, map[string]any, http.Header) {
	w := do(a.srv.handleCredAWS, storeReq(method, path, body, credOrigin, credCookie))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Header()
}

// everyAWSRoute is each route of the AWS credential API with a body that would
// be valid for it.
var everyAWSRoute = []struct{ method, path, body string }{
	{"GET", pathAWS + "/host-profiles", ""},
	{"POST", pathAWS + "/roles", `{"source_profile":"p"}`},
	{"POST", pathAWS + "/check", `{"source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`},
	{"POST", pathAWS + "/sso-login", `{"source_profile":"p"}`},
	{"GET", pathAWS + "/sso-login?profile=p", ""},
	{"POST", pathAWS, `{"connection":{"name":"c","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`},
	{"PUT", pathAWS + "/c", `{"connection":{"name":"c","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`},
	{"DELETE", pathAWS + "/c", ""},
}

func TestCredAWSRoutesRefuseBearer(t *testing.T) {
	a := newAWSRoutes(t)
	for _, rt := range everyAWSRoute {
		w := do(a.srv.handleCredAWS, storeReq(rt.method, rt.path, rt.body, credOrigin, credBearer))
		if w.Code != 403 {
			t.Errorf("%s %s with a bearer: %d", rt.method, rt.path, w.Code)
		}
	}
	if a.svc.n() != 0 || len(a.fx.rec.events) != 0 {
		t.Fatalf("a refused request reached AWS or the audit: %v", a.svc.calls)
	}
}

func TestCredAWSGetRefusesBearer(t *testing.T) {
	a := newAWSRoutes(t)
	w := do(a.srv.handleCredAWS, storeReq("GET", pathAWS+"/host-profiles", "", "", credBearer))
	if w.Code != 403 {
		t.Fatalf("got %d", w.Code)
	}
	w = do(a.srv.handleCredAWS, storeReq("GET", pathAWS+"/host-profiles", "", "", credNone))
	if w.Code != 403 {
		t.Fatalf("no credential: %d", w.Code)
	}
}

func TestCredAWSRoutesRefuseCrossOrigin(t *testing.T) {
	a := newAWSRoutes(t)
	for _, rt := range everyAWSRoute {
		w := do(a.srv.handleCredAWS, storeReq(rt.method, rt.path, rt.body, "http://evil.example", credCookie))
		if w.Code != 403 {
			t.Errorf("%s %s cross-origin: %d", rt.method, rt.path, w.Code)
		}
	}
	// A mutation or an AWS call needs an Origin that matches, not just one that is not foreign.
	for _, rt := range everyAWSRoute {
		if rt.method == "GET" {
			continue
		}
		w := do(a.srv.handleCredAWS, storeReq(rt.method, rt.path, rt.body, "", credCookie))
		if w.Code != 403 {
			t.Errorf("%s %s without an Origin: %d", rt.method, rt.path, w.Code)
		}
	}
	// A same-origin GET carries no Origin header and must work.
	if w := do(a.srv.handleCredAWS, storeReq("GET", pathAWS+"/host-profiles", "", "", credCookie)); w.Code != 200 {
		t.Fatalf("same-origin GET without Origin: %d", w.Code)
	}
	if a.svc.n() != 0 {
		t.Fatalf("a refused request reached AWS: %v", a.svc.calls)
	}
}

func TestRolesAccessDenied(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.roles = RolesResult{AccessDenied: true}
	code, body, _ := a.call("POST", pathAWS+"/roles", `{"source_profile":"p"}`)
	if code != 200 || len(body) != 1 || body["access_denied"] != true {
		t.Fatalf("%d %v", code, body)
	}
	if !slices.Equal(a.fx.rec.actions(), []string{broker.CredRolesListed}) || a.fx.rec.events[0].Detail != "p" {
		t.Fatalf("audit %+v", a.fx.rec.events)
	}
}

func TestRolesFiltersByAccount(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.roles = RolesResult{Roles: []RoleRef{
		{Name: "A", ARN: "arn:aws:iam::123456789012:role/A"},
		{Name: "B", ARN: "arn:aws:iam::210987654321:role/B"},
	}}
	_, body, _ := a.call("POST", pathAWS+"/roles", `{"source_profile":"p","account_id":"210987654321"}`)
	roles, _ := body["roles"].([]any)
	if len(roles) != 1 || roles[0].(map[string]any)["name"] != "B" {
		t.Fatalf("%v", body)
	}
	if code, _, _ := a.call("POST", pathAWS+"/roles", `{"source_profile":"p","account_id":"12"}`); code != 400 {
		t.Fatalf("bad account id: %d", code)
	}
}

func TestRolesBadProfile400(t *testing.T) {
	a := newAWSRoutes(t)
	for _, bad := range []string{"a b", "", strings.Repeat("a", 65), "a/b", "a;b", "p\n"} {
		b, _ := json.Marshal(map[string]string{"source_profile": bad})
		code, body, _ := a.call("POST", pathAWS+"/roles", string(b))
		if code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
		if s, _ := body["error"].(string); strings.Contains(s, bad) && bad != "" {
			t.Errorf("error echoes the profile: %q", s)
		}
	}
	if a.svc.n() != 0 || len(a.fx.rec.events) != 0 {
		t.Fatalf("invalid input reached AWS or the audit: %v", a.svc.calls)
	}
}

func TestCredAWSBodyRules(t *testing.T) {
	a := newAWSRoutes(t)
	cases := map[string]string{
		"unknown field":   `{"source_profile":"p","extra":1}`,
		"trailing data":   `{"source_profile":"p"} {"x":1}`,
		"not json":        `source_profile=p`,
		"wrong type":      `{"source_profile":5}`,
		"empty":           ``,
		"trailing garbag": `{"source_profile":"p"}x`,
	}
	for name, body := range cases {
		code, resp, _ := a.call("POST", pathAWS+"/roles", body)
		if code != 400 {
			t.Errorf("%s: %d", name, code)
		}
		if s, _ := resp["error"].(string); strings.Contains(s, "extra") || strings.Contains(s, "garbag") {
			t.Errorf("%s: error echoes the body: %q", name, s)
		}
	}
	big := `{"source_profile":"` + strings.Repeat("a", 70<<10) + `"}`
	if code, _, _ := a.call("POST", pathAWS+"/roles", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize: %d", code)
	}
	if a.svc.n() != 0 {
		t.Fatalf("a bad body reached AWS: %v", a.svc.calls)
	}
}

const checkBody = `{"source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`

func TestCheckMapsResult(t *testing.T) {
	a := newAWSRoutes(t)
	at := time.Unix(1_800_000_000, 0).UTC()
	a.svc.check = awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject", "ec2:RunInstances"}, CheckedAt: at}
	code, body, _ := a.call("POST", pathAWS+"/check", checkBody)
	acts, _ := body["actions"].([]any)
	if code != 200 || body["verdict"] != "writes" || body["assume"] != "ok" || len(acts) != 2 {
		t.Fatalf("%d %v", code, body)
	}
	if v := a.fx.cm.Verdict("p", "arn:aws:iam::123456789012:role/R"); v.State != awsprobe.Writes {
		t.Fatalf("verdict not kept for the entries list: %+v", v)
	}
	if got := a.svc.targets[0]; got.SourceProfile != "p" || got.Region != "us-east-1" || got.RoleARN != "arn:aws:iam::123456789012:role/R" {
		t.Fatalf("target %+v", got)
	}

	cases := []struct {
		res    awsprobe.Result
		assume string
		state  string
		reason string
	}{
		{awsprobe.Result{State: awsprobe.NoneFound}, "ok", "none_found", ""},
		{awsprobe.Fail(awsprobe.ReasonAssumeDenied, at), "denied", "unverified", "assume_denied"},
		{awsprobe.Fail(awsprobe.ReasonSSOExpired, at), "sso_expired", "unverified", "sso_expired"},
		{awsprobe.Fail(awsprobe.ReasonTimeout, at), "error", "unverified", "timeout"},
		{awsprobe.Fail(awsprobe.ReasonError, at), "error", "unverified", "error"},
		{awsprobe.Fail(awsprobe.ReasonSimulateDenied, at), "ok", "unverified", "simulate_denied"},
	}
	for _, c := range cases {
		a.svc.check = c.res
		_, body, _ := a.call("POST", pathAWS+"/check", checkBody)
		reason, _ := body["reason"].(string)
		if body["assume"] != c.assume || body["verdict"] != c.state || reason != c.reason {
			t.Errorf("%s: %v", c.reason, body)
		}
	}
}

func TestCheckBadInputs(t *testing.T) {
	a := newAWSRoutes(t)
	for name, body := range map[string]string{
		"profile": `{"source_profile":"a b","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`,
		"arn":     `{"source_profile":"p","role_arn":"arn:aws:iam::12:role/R","region":"us-east-1"}`,
		"region":  `{"source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"mars"}`,
		"missing": `{"source_profile":"p"}`,
	} {
		if code, _, _ := a.call("POST", pathAWS+"/check", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if a.svc.n() != 0 {
		t.Fatal("invalid input reached AWS")
	}
}

func TestCheckAudited(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.check = awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}
	// The row that says the signer is about to be used exists before it is used.
	var before []broker.CredEvent
	a.svc.onCheck = func() { before = slices.Clone(a.fx.rec.events) }
	a.call("POST", pathAWS+"/check", checkBody)
	if len(before) != 1 || before[0].Action != broker.CredChecked || !strings.HasSuffix(before[0].Detail, " started") {
		t.Fatalf("no audit row before the AWS call: %+v", before)
	}
	if len(a.fx.rec.events) != 2 {
		t.Fatalf("%+v", a.fx.rec.events)
	}
	e := a.fx.rec.events[1]
	if e.Action != broker.CredChecked || e.Kind != "aws" || !strings.Contains(e.Detail, "arn:aws:iam::123456789012:role/R") || !strings.Contains(e.Detail, "writes") {
		t.Fatalf("%+v", e)
	}
}

// A check that cannot be audited is not made: the host signer is not used.
func TestCheckRefusedWhenAuditFails(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.rec.err = errors.New("disk full")
	code, body, _ := a.call("POST", pathAWS+"/check", checkBody)
	if code != 500 || body["verdict"] != nil || a.svc.n() != 0 {
		t.Fatalf("%d %v calls=%v", code, body, a.svc.calls)
	}
	// Same for the other two calls that use the signer.
	if code, _, _ := a.call("POST", pathAWS+"/roles", `{"source_profile":"p"}`); code != 500 {
		t.Fatalf("roles: %d", code)
	}
	if code, _, _ := a.call("POST", pathAWS+"/sso-login", `{"source_profile":"p"}`); code != 500 {
		t.Fatalf("sso-login: %d", code)
	}
	if a.svc.n() != 0 {
		t.Fatalf("the signer was used unaudited: %v", a.svc.calls)
	}
}

// No audit recorder wired fails closed, not open.
func TestNoAuditRecorderFailsClosed(t *testing.T) {
	a := newAWSRoutes(t)
	a.srv.CredAudit = nil
	for _, rt := range []struct{ path, body string }{
		{"/roles", `{"source_profile":"p"}`}, {"/check", checkBody}, {"/sso-login", `{"source_profile":"p"}`},
	} {
		code, body, _ := a.call("POST", pathAWS+rt.path, rt.body)
		if code != 500 || body["error"] != ErrCredPersist.Error() {
			t.Errorf("%s: %d %v", rt.path, code, body)
		}
	}
	if a.svc.n() != 0 {
		t.Fatalf("the signer was used unaudited: %v", a.svc.calls)
	}
}

func TestAWSCreateNeedsAck409(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.check = awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}
	conn := `{"name":"prod","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/Admin","region":"us-east-1"}`
	code, body, _ := a.call("POST", pathAWS, `{"connection":`+conn+`}`)
	v, _ := body["verdict"].(map[string]any)
	if code != 409 || body["code"] != "needs_ack" || v["state"] != "writes" {
		t.Fatalf("%d %v", code, body)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("saved without an acknowledgement")
	}
	code, body, _ = a.call("POST", pathAWS, `{"connection":`+conn+`,"ack_write":true}`)
	if code != 201 || body["name"] != "prod" {
		t.Fatalf("%d %v", code, body)
	}
	if len(a.fx.file(t).AWSConnections) != 1 {
		t.Fatal("not saved")
	}
	// A second create of the same name is a conflict, not an overwrite.
	code, body, _ = a.call("POST", pathAWS, `{"connection":`+conn+`,"ack_write":true}`)
	if code != 409 || body["code"] != "exists" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestAWSPutAndDelete(t *testing.T) {
	a := newAWSRoutes(t)
	conn := func(desc string) string {
		return `{"connection":{"name":"c","description":"` + desc + `","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`
	}
	if code, _, _ := a.call("PUT", pathAWS+"/c", conn("x")); code != 404 {
		t.Fatalf("replace of a missing entry: %d", code)
	}
	if code, _, _ := a.call("POST", pathAWS, conn("a")); code != 201 {
		t.Fatalf("create: %d", code)
	}
	n := a.svc.n()
	if code, _, _ := a.call("PUT", pathAWS+"/c", conn("b")); code != 200 {
		t.Fatalf("replace: %d", code)
	}
	if a.svc.n() != n {
		t.Fatal("a description edit re-checked the base role")
	}
	if got := a.fx.file(t).AWSConnections[0].Description; got != "b" {
		t.Fatalf("description %q", got)
	}
	// The path names the entry: a body that names another is refused.
	other := strings.Replace(conn("b"), `"name":"c"`, `"name":"d"`, 1)
	if code, _, _ := a.call("PUT", pathAWS+"/c", other); code != 400 {
		t.Fatalf("name mismatch: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathAWS+"/c", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathAWS+"/c", ""); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathAWS+"/Bad%20Name", ""); code != 400 && code != 404 {
		t.Fatalf("bad name: %d", code)
	}
}

func TestAWSCreateReservedRouteName(t *testing.T) {
	a := newAWSRoutes(t)
	for _, n := range []string{"roles", "check", "sso-login", "host-profiles"} {
		body := `{"connection":{"name":"` + n + `","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`
		if code, _, _ := a.call("POST", pathAWS, body); code != 400 {
			t.Errorf("%s: %d", n, code)
		}
	}
}

func TestAWSDeleteInUse409(t *testing.T) {
	base := config.Config{AWSConnections: []config.AWSConnection{awsConn("seed", "p", arnRead)}}
	fx := newCredFixture(t, base, nil)
	srv := &Server{Creds: fx.cm, CredAWS: &fakeAWSSvc{}, CredAudit: fx.rec}
	w := do(srv.handleCredAWS, storeReq("DELETE", pathAWS+"/seed", "", credOrigin, credCookie))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 409 || body["code"] != "seed" {
		t.Fatalf("%d %v", w.Code, body)
	}
}

func TestNoBroker409(t *testing.T) {
	a := newAWSRoutes(t)
	a.srv.CredAWS = nil
	a.fx.cm.chk = nil
	for _, rt := range everyAWSRoute {
		switch {
		case rt.path == pathAWS+"/roles", rt.path == pathAWS+"/check", rt.path == pathAWS+"/sso-login", rt.method == "POST" && rt.path == pathAWS:
		default:
			continue
		}
		code, body, _ := a.call(rt.method, rt.path, rt.body)
		if code != 409 || body["code"] != "no_broker" {
			t.Errorf("%s %s: %d %v", rt.method, rt.path, code, body)
		}
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("saved without a broker")
	}
	// Reads that need no broker still work.
	if code, _, _ := a.call("GET", pathAWS+"/host-profiles", ""); code != 200 {
		t.Fatalf("host-profiles: %d", code)
	}
}

func TestCredAWSRateLimit(t *testing.T) {
	a := newAWSRoutes(t)
	for i := 0; i < 10; i++ {
		if code, _, _ := a.call("POST", pathAWS+"/check", checkBody); code != 200 {
			t.Fatalf("call %d: %d", i+1, code)
		}
	}
	code, _, h := a.call("POST", pathAWS+"/check", checkBody)
	if code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("11th: %d %v", code, h)
	}
	// roles and sso-login share the same budget.
	if code, _, _ := a.call("POST", pathAWS+"/roles", `{"source_profile":"p"}`); code != 429 {
		t.Fatalf("roles: %d", code)
	}
	if code, _, _ := a.call("POST", pathAWS+"/sso-login", `{"source_profile":"p"}`); code != 429 {
		t.Fatalf("sso-login: %d", code)
	}
	// A refused call did not reach AWS or the audit.
	if a.svc.n() != 10 {
		t.Fatalf("calls %d", a.svc.n())
	}
	// Reads of state and invalid input do not spend the budget.
	if code, _, _ := a.call("GET", pathAWS+"/sso-login?profile=p", ""); code != 404 {
		t.Fatalf("status: %d", code)
	}
}

func TestCredAWSRateLimitWindowResets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := &callLimiter{max: 2, window: time.Minute, now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		if _, ok := l.allow(); !ok {
			t.Fatalf("call %d refused", i+1)
		}
	}
	if wait, ok := l.allow(); ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("third: %v %v", wait, ok)
	}
	now = now.Add(time.Minute)
	if _, ok := l.allow(); !ok {
		t.Fatal("window did not reset")
	}
}

func TestSSOLoginRoutes(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.sso = ssologin.State{State: ssologin.StateWaiting, URI: "https://device.sso.us-east-1.amazonaws.com/", URITrusted: true, Code: "ABCD-EFGH"}
	code, body, _ := a.call("POST", pathAWS+"/sso-login", `{"source_profile":"p"}`)
	if code != 200 || body["state"] != "waiting" || body["code"] != "ABCD-EFGH" {
		t.Fatalf("%d %v", code, body)
	}
	if !slices.Equal(a.fx.rec.actions(), []string{broker.CredSSOStarted}) || a.fx.rec.events[0].Detail != "p" {
		t.Fatalf("audit %+v", a.fx.rec.events)
	}
	if code, _, _ := a.call("POST", pathAWS+"/sso-login", `{"source_profile":"-p"}`); code != 400 {
		t.Fatalf("leading dash: %d", code)
	}
	if code, _, _ := a.call("GET", pathAWS+"/sso-login?profile=a%20b", ""); code != 400 {
		t.Fatalf("bad status profile: %d", code)
	}
	if code, _, _ := a.call("GET", pathAWS+"/sso-login", ""); code != 400 {
		t.Fatalf("missing profile: %d", code)
	}
	if code, _, _ := a.call("GET", pathAWS+"/sso-login?profile=q", ""); code != 404 {
		t.Fatalf("no flow: %d", code)
	}
	a.svc.ssoStatus = map[string]ssologin.State{"p": {State: ssologin.StateOK}}
	if code, body, _ := a.call("GET", pathAWS+"/sso-login?profile=p", ""); code != 200 || body["state"] != "ok" {
		t.Fatalf("status: %d %v", code, body)
	}
}

func TestSSOLoginUnavailable(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.ssoErr = &SSOLoginError{Command: "aws sso login --profile p", Err: errors.New("the aws CLI is not on the daemon's PATH")}
	code, body, _ := a.call("POST", pathAWS+"/sso-login", `{"source_profile":"p"}`)
	if code != http.StatusBadGateway || body["command"] != "aws sso login --profile p" {
		t.Fatalf("%d %v", code, body)
	}
	if s, _ := body["error"].(string); strings.Contains(s, "PATH") {
		t.Fatalf("internal text shown: %q", s)
	}
}

func TestHostProfilesRoute(t *testing.T) {
	a := newAWSRoutes(t) // first: it points AWS_CONFIG_FILE at nothing, this test then overrides it
	cfg := t.TempDir() + "/config"
	if err := os.WriteFile(cfg, []byte("[profile dev]\nsso_account_id = 123456789012\nsso_role_name = Dev\nregion = us-east-1\naws_secret_access_key = SHOULDNEVERAPPEAR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	w := do(a.srv.handleCredAWS, storeReq("GET", pathAWS+"/host-profiles", "", "", credCookie))
	if w.Code != 200 || strings.Contains(w.Body.String(), "SHOULDNEVERAPPEAR") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Profiles []struct{ Name, Kind string } `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Profiles) != 1 || got.Profiles[0].Name != "dev" || got.Profiles[0].Kind != "sso" {
		t.Fatalf("%v %s", err, w.Body)
	}
	// No file: an empty list, not an error, and `[]` not `null`.
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/missing")
	w = do(a.srv.handleCredAWS, storeReq("GET", pathAWS+"/host-profiles", "", "", credCookie))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"profiles":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestCredAWSUnknownRouteAndMethod(t *testing.T) {
	a := newAWSRoutes(t)
	if code, _, _ := a.call("GET", pathAWS+"/roles", ""); code != 405 {
		t.Errorf("GET roles: %d", code)
	}
	if code, _, _ := a.call("POST", pathAWS+"/host-profiles", `{}`); code != 405 {
		t.Errorf("POST host-profiles: %d", code)
	}
	if code, _, _ := a.call("PATCH", pathAWS+"/c", `{}`); code != 405 {
		t.Errorf("PATCH: %d", code)
	}
	if code, _, _ := a.call("PUT", pathAWS, `{}`); code != 405 {
		t.Errorf("PUT collection: %d", code)
	}
	if code, _, _ := a.call("GET", pathAWS+"/a/b", ""); code != 404 {
		t.Errorf("nested: %d", code)
	}
}

func TestAWSSaveAuditFailureIsFixedSentence(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.rec.err = errors.New("open /home/someone/.local/share/agent-box/audit.db: disk full")
	body := `{"connection":{"name":"c","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`
	code, resp, _ := a.call("POST", pathAWS, body)
	if code != 500 || strings.Contains(resp["error"].(string), "/home") {
		t.Fatalf("%d %v", code, resp)
	}
	if a.fx.fileBytes(t) != "" {
		t.Fatal("saved without an audit row")
	}
	if code, _, _ := a.call("POST", pathAWS, `{"connection":{"name":"c","bogus":1}}`); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
}

// Through the full handler chain: the routes exist only with a manager, a
// bearer is refused, and the signed-in browser reaches them.
func TestCredAWSThroughHandler(t *testing.T) {
	get := func(s *Server, bearer string, cookie *http.Cookie) int {
		r := httptest.NewRequest("GET", pathAWS+"/host-profiles", nil)
		r.Host = "example.com"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/missing")
	s, plain := routedServer(t)
	iw := httptest.NewRecorder()
	s.Session.Issue(iw, false)
	cookie := iw.Result().Cookies()[0]
	if code := get(s, "", cookie); code == 200 {
		t.Fatalf("route served without a manager: %d", code)
	}
	fx := newCredFixture(t, config.Config{}, nil)
	s.Creds = fx.cm
	if code := get(s, plain, nil); code != 403 {
		t.Fatalf("bearer: %d", code)
	}
	if code := get(s, "", cookie); code != 200 {
		t.Fatalf("cookie: %d", code)
	}
}

func saveBody(name, desc string) string {
	return fmt.Sprintf(`{"connection":{"name":%q,"description":%q,"source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`, name, desc)
}

// The create/replace path charges only the saves that run a check, to its own
// budget of 60 a minute; the 10/min bucket of roles, check and sso-login is
// separate and unchanged.
func TestAWSSaveCheckBudget(t *testing.T) {
	a := newAWSRoutes(t)
	now := time.Unix(1_800_000_000, 0)
	a.fx.cm.checkLimit.now = func() time.Time { return now }
	for i := 0; i < 60; i++ {
		if code, body, _ := a.call("POST", pathAWS, saveBody(fmt.Sprintf("c%d", i), "")); code != 201 {
			t.Fatalf("create %d: %d %v", i, code, body)
		}
	}
	if a.svc.n() != 60 {
		t.Fatalf("checks %d", a.svc.n())
	}
	code, _, h := a.call("POST", pathAWS, saveBody("c60", ""))
	if code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("61st check-triggering create: %d %v", code, h)
	}
	if a.svc.n() != 60 || len(a.fx.file(t).AWSConnections) != 60 {
		t.Fatalf("the refused save reached AWS or the file: checks=%d", a.svc.n())
	}
	// Saves that run no check are not charged, nor refused.
	if code, _, _ := a.call("PUT", pathAWS+"/c0", saveBody("c0", "new label")); code != 200 {
		t.Fatalf("label-only edit: %d", code)
	}
	if code, _, _ := a.call("DELETE", pathAWS+"/c1", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if a.svc.n() != 60 {
		t.Fatal("an edit that changes nothing the check measures ran a check")
	}
	// The other bucket is untouched by all of this.
	if code, _, _ := a.call("POST", pathAWS+"/check", checkBody); code != 200 {
		t.Fatalf("check bucket: %d", code)
	}
	// A new window opens the budget again.
	now = now.Add(time.Minute)
	if code, _, _ := a.call("POST", pathAWS, saveBody("c60", "")); code != 201 {
		t.Fatalf("next window: %d", code)
	}
}

// An un-acknowledged save against a writes role runs a check each time, so it
// is charged too.
func TestAWSUnackedLoopIsLimited(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.check = awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}
	for i := 0; i < 60; i++ {
		if code, _, _ := a.call("POST", pathAWS, saveBody("w", "")); code != 409 {
			t.Fatalf("call %d: %d", i+1, code)
		}
	}
	if code, _, _ := a.call("POST", pathAWS, saveBody("w", "")); code != 429 {
		t.Fatalf("61st: %d", code)
	}
	if a.svc.n() != 60 {
		t.Fatalf("checks %d", a.svc.n())
	}
}

// --- sso_direct: check and save --------------------------------------------

const ssoCheckBody = `{"mode":"sso_direct","source_profile":"p","region":"us-east-1"}`

func ssoConnJSON(name string) string {
	return `{"connection":{"name":"` + name + `","mode":"sso_direct","source_profile":"p","region":"us-east-1"}}`
}

func readOnlyGate() awsprobe.GateResult {
	return awsprobe.GateResult{State: awsprobe.GateReadOnly, RoleARN: "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_X_1"}
}

func TestCheckSSODirect(t *testing.T) {
	a := newAWSRoutes(t)
	exp := time.Unix(1_800_000_000, 0).UTC()
	a.svc.gate, a.svc.gateExp = readOnlyGate(), exp
	code, body, _ := a.call("POST", pathAWS+"/check", ssoCheckBody)
	gate, _ := body["gate"].(map[string]any)
	if code != 200 || gate["state"] != "read_only" || body["expires"] != exp.Format(time.RFC3339) {
		t.Fatalf("%d %v", code, body)
	}
	if body["verdict"] != nil || body["assume"] != nil {
		t.Fatalf("an AssumeRole verdict leaked into the sso_direct answer: %v", body)
	}
	if len(a.svc.gated) != 1 || a.svc.gated[0].SourceProfile != "p" || a.svc.gated[0].Region != "us-east-1" || !a.svc.gated[0].SSODirect() {
		t.Fatalf("gate called with %+v", a.svc.gated)
	}
	if slices.Contains(a.svc.calls, "check") {
		t.Fatal("the AssumeRole check ran for an sso_direct check")
	}
	// Nothing for the entries list: no verdict is kept for this mode.
	if v := a.fx.cm.Verdict("p", ""); v.State != awsprobe.NotChecked().State {
		t.Fatalf("verdict kept: %+v", v)
	}
}

func TestCheckSSODirectNotReadOnlyAndUnverified(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = awsprobe.GateResult{State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{{Pattern: "s3:*", Why: "allows writes"}}}
	code, body, _ := a.call("POST", pathAWS+"/check", ssoCheckBody)
	gate, _ := body["gate"].(map[string]any)
	if code != 200 || gate["state"] != "not_read_only" || body["expires"] != "" {
		t.Fatalf("%d %v", code, body)
	}
	a.svc.gate = awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSSOExpired}
	code, body, _ = a.call("POST", pathAWS+"/check", ssoCheckBody)
	gate, _ = body["gate"].(map[string]any)
	if code != 200 || gate["state"] != "unverified" || gate["reason"] != "sso_expired" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestCheckSSODirectBadInputs(t *testing.T) {
	a := newAWSRoutes(t)
	for name, body := range map[string]string{
		"role_arn present": `{"mode":"sso_direct","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`,
		"profile":          `{"mode":"sso_direct","source_profile":"a b","region":"us-east-1"}`,
		"region":           `{"mode":"sso_direct","source_profile":"p","region":"mars"}`,
		"no region":        `{"mode":"sso_direct","source_profile":"p"}`,
		"unknown mode":     `{"mode":"admin","source_profile":"p","region":"us-east-1"}`,
	} {
		if code, _, _ := a.call("POST", pathAWS+"/check", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if a.svc.n() != 0 || len(a.fx.rec.events) != 0 {
		t.Fatalf("invalid input reached AWS or the audit: %v", a.svc.calls)
	}
	// "assume_role" spelled out is the same as no mode.
	body := `{"mode":"assume_role","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`
	if code, _, _ := a.call("POST", pathAWS+"/check", body); code != 200 {
		t.Fatalf("assume_role spelled out: %d", code)
	}
}

func TestCheckSSODirectAudited(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = readOnlyGate()
	var before []broker.CredEvent
	a.svc.onGate = func() { before = slices.Clone(a.fx.rec.events) }
	a.call("POST", pathAWS+"/check", ssoCheckBody)
	if len(before) != 1 || before[0].Action != broker.CredChecked || !strings.Contains(before[0].Detail, "sso_direct") || !strings.HasSuffix(before[0].Detail, " started") {
		t.Fatalf("no audit row before the gate ran: %+v", before)
	}
	if len(a.fx.rec.events) != 2 {
		t.Fatalf("%+v", a.fx.rec.events)
	}
	if e := a.fx.rec.events[1]; e.Action != broker.CredChecked || e.Kind != "aws" || !strings.Contains(e.Detail, "sso_direct") || !strings.Contains(e.Detail, "read_only") {
		t.Fatalf("%+v", e)
	}
}

func TestCheckSSODirectRefusedWhenAuditFails(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.rec.err = errors.New("disk full")
	if code, body, _ := a.call("POST", pathAWS+"/check", ssoCheckBody); code != 500 || body["gate"] != nil || a.svc.n() != 0 {
		t.Fatalf("%d %v", code, body)
	}
}

func TestCheckSSODirectNoBroker(t *testing.T) {
	a := newAWSRoutes(t)
	a.srv.CredAWS = nil
	if code, m, _ := a.call("POST", pathAWS+"/check", ssoCheckBody); code != 409 || m["code"] != "no_broker" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestSaveSSODirectReadOnly(t *testing.T) {
	a := newAWSRoutes(t)
	exp := time.Unix(1_800_000_000, 0).UTC()
	a.svc.gate, a.svc.gateExp = readOnlyGate(), exp
	code, body, _ := a.call("POST", pathAWS, ssoConnJSON("ro"))
	gate, _ := body["gate"].(map[string]any)
	if code != 201 || body["name"] != "ro" || gate["state"] != "read_only" || body["expires"] != exp.Format(time.RFC3339) {
		t.Fatalf("%d %v", code, body)
	}
	got := a.fx.file(t).AWSConnections
	if len(got) != 1 || got[0].Mode != config.AWSModeSSODirect || got[0].RoleARN != "" {
		t.Fatalf("file: %+v", got)
	}
	if len(a.svc.gated) != 1 || a.svc.gated[0].Name != "ro" {
		t.Fatalf("gate saw %+v", a.svc.gated)
	}
	if slices.Contains(a.svc.calls, "check") {
		t.Fatal("AssumeRole check ran")
	}
	acts := a.fx.rec.actions()
	if len(acts) != 3 || acts[0] != broker.CredChecked || acts[1] != broker.CredChecked || acts[2] != broker.CredCreated {
		t.Fatalf("audit %v", acts)
	}
	// Edit (a label change) gates again: the permission set may have changed.
	put := `{"connection":{"name":"ro","mode":"sso_direct","source_profile":"p","region":"us-east-1","label":"RO"}}`
	if code, _, _ = a.call("PUT", pathAWS+"/ro", put); code != 200 || len(a.svc.gated) != 2 {
		t.Fatalf("edit: %d gated=%d", code, len(a.svc.gated))
	}
}

func TestSaveSSODirectRefusedAndNothingWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate awsprobe.GateResult
		code string
	}{
		{"not read only", awsprobe.GateResult{State: awsprobe.GateNotReadOnly, Refused: []awsprobe.Verdict{{Pattern: "s3:*", Why: "allows writes"}}}, "not_read_only"},
		{"unverified", awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonSSOExpired}, "unverified"},
		{"read only with no role ARN", awsprobe.GateResult{State: awsprobe.GateReadOnly}, "unverified"},
		{"junk state", awsprobe.GateResult{State: "yes"}, "unverified"},
	} {
		for _, ack := range []string{"", `,"ack_write":true`} {
			a := newAWSRoutes(t)
			a.svc.gate = tc.gate
			body := strings.TrimSuffix(ssoConnJSON("ro"), "}") + ack + "}"
			code, m, _ := a.call("POST", pathAWS, body)
			g, _ := m["gate"].(map[string]any)
			if code != 409 || m["code"] != tc.code || g == nil {
				t.Errorf("%s ack=%q: %d %v", tc.name, ack, code, m)
			}
			if a.fx.fileBytes(t) != "" || a.fx.applyCount() != 0 {
				t.Errorf("%s ack=%q: something was saved", tc.name, ack)
			}
			if slices.Contains(a.fx.rec.actions(), broker.CredWriteAck) || slices.Contains(a.fx.rec.actions(), broker.CredCreated) {
				t.Errorf("%s ack=%q: audit %v", tc.name, ack, a.fx.rec.actions())
			}
		}
	}
}

func TestSaveSSODirectAckWriteIgnoredWhenReadOnly(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = readOnlyGate()
	body := strings.TrimSuffix(ssoConnJSON("ro"), "}") + `,"ack_write":true}`
	if code, _, _ := a.call("POST", pathAWS, body); code != 201 {
		t.Fatalf("%d", code)
	}
	if slices.Contains(a.fx.rec.actions(), broker.CredWriteAck) {
		t.Fatalf("an acknowledgement was recorded for a mode that has none: %v", a.fx.rec.actions())
	}
}

func TestSaveSSODirectStaleAssumeRoleFieldsRefused(t *testing.T) {
	a := newAWSRoutes(t)
	create := `{"connection":{"name":"c","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}}`
	if code, _, _ := a.call("POST", pathAWS, create); code != 201 {
		t.Fatal(code)
	}
	before := a.fx.fileBytes(t)
	for name, conn := range map[string]string{
		"role_arn": `{"name":"c","mode":"sso_direct","source_profile":"p","role_arn":"arn:aws:iam::123456789012:role/R","region":"us-east-1"}`,
		"elevate":  `{"name":"c","mode":"sso_direct","source_profile":"p","region":"us-east-1","elevate":{"role_arn":"arn:aws:iam::123456789012:role/W"}}`,
		"s3_read":  `{"name":"c","mode":"sso_direct","source_profile":"p","region":"us-east-1","s3_read":["b"]}`,
	} {
		a.svc.gate = readOnlyGate()
		n := len(a.svc.gated)
		code, m, _ := a.call("PUT", pathAWS+"/c", `{"connection":`+conn+`}`)
		if code != 400 {
			t.Errorf("%s: %d %v", name, code, m)
		}
		if len(a.svc.gated) != n {
			t.Errorf("%s: the gate ran for a binding that cannot be valid", name)
		}
	}
	if a.fx.fileBytes(t) != before {
		t.Fatal("the file changed")
	}
}

// A cached AssumeRole verdict for the same profile is not shown for, or used
// by, an sso_direct connection.
func TestSSODirectIgnoresAssumeRoleVerdict(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.cm.RecordVerdict("p", "", awsprobe.Result{State: awsprobe.NoneFound, CheckedAt: time.Now()})
	a.svc.gate = awsprobe.GateResult{State: awsprobe.GateNotReadOnly}
	if code, _, _ := a.call("POST", pathAWS, ssoConnJSON("ro")); code != 409 {
		t.Fatalf("a stored AssumeRole verdict let an sso_direct save through: %d", code)
	}
	a.svc.gate = readOnlyGate()
	if code, _, _ := a.call("POST", pathAWS, ssoConnJSON("ro")); code != 201 {
		t.Fatal(code)
	}
	a.fx.cm.RecordVerdict("p", "", awsprobe.Result{State: awsprobe.Writes, Actions: []string{"s3:PutObject"}, CheckedAt: time.Now()})
	snap := a.fx.cm.snapshot()
	if len(snap.aws) != 1 || snap.aws[0].base.State != awsprobe.NotChecked().State {
		t.Fatalf("entries list shows an AssumeRole verdict for an sso_direct connection: %+v", snap.aws)
	}
}

func TestSaveSSODirectNoBrokerOrNoGate(t *testing.T) {
	a := newAWSRoutes(t)
	a.fx.cm.chk = nil
	if code, m, _ := a.call("POST", pathAWS, ssoConnJSON("ro")); code != 409 || m["code"] != "no_broker" {
		t.Fatalf("no checker: %d %v", code, m)
	}
	// A checker that cannot gate is unverified, never a pass.
	a.fx.cm.chk = a.fx.chk
	code, m, _ := a.call("POST", pathAWS, ssoConnJSON("ro"))
	if code != 409 || m["code"] != "unverified" || a.fx.fileBytes(t) != "" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestSaveSSODirectGateAuditFailure(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = readOnlyGate()
	a.fx.rec.err = errors.New("disk full")
	code, m, _ := a.call("POST", pathAWS, ssoConnJSON("ro"))
	if code != 500 || m["gate"] != nil || a.svc.n() != 0 || a.fx.fileBytes(t) != "" {
		t.Fatalf("%d %v", code, m)
	}
}

// The entries list and the profile editor's picker carry the mode, so an edit
// form can send it back and a picker can mark the connection.
func TestSSODirectModeIsShown(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = readOnlyGate()
	if code, _, _ := a.call("POST", pathAWS, ssoConnJSON("ro")); code != 201 {
		t.Fatal(code)
	}
	body := a.srv.credView()
	if len(body.AWS) != 1 || body.AWS[0].Mode != config.AWSModeSSODirect {
		t.Fatalf("%+v", body.AWS)
	}
	b, _ := json.Marshal(body.AWS[0])
	if !strings.Contains(string(b), `"mode":"sso_direct"`) {
		t.Fatalf("%s", b)
	}
	// An assume_role entry does not grow a mode field.
	ar := awsConn("c", "p", arnRead)
	if _, err := a.fx.cm.PutAWS(context.Background(), ar, true, false); err != nil {
		t.Fatal(err)
	}
	for _, e := range a.srv.credView().AWS {
		b, _ = json.Marshal(e)
		if e.Name == "c" && strings.Contains(string(b), `"mode"`) {
			t.Fatalf("%s", b)
		}
	}
	metas := a.fx.pm.AWSConnectionMetaList()
	modes := map[string]string{}
	for _, m := range metas {
		modes[m.Name] = m.Mode
	}
	if modes["ro"] != config.AWSModeSSODirect || modes["c"] != "" {
		t.Fatalf("%+v", metas)
	}
}

// PutAWS itself cannot be used to skip the gate, whatever ackWrite says.
func TestPutAWSRoutesSSODirectThroughTheGate(t *testing.T) {
	a := newAWSRoutes(t)
	a.svc.gate = awsprobe.GateResult{State: awsprobe.GateNotReadOnly}
	conn := config.AWSConnection{Name: "ro", AWSBinding: config.AWSBinding{SourceProfile: "p", Region: "us-east-1", Mode: config.AWSModeSSODirect}}
	_, err := a.fx.cm.PutAWS(context.Background(), conn, true, true)
	var g ErrSSOGate
	if !errors.As(err, &g) || g.Code() != "not_read_only" || a.fx.fileBytes(t) != "" {
		t.Fatalf("%v", err)
	}
}
