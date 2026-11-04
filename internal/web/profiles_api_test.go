package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
)

func TestProfilesAPI_CreateListDelete(t *testing.T) {
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "bash", Bin: "bash"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {},
	)
	s := &Server{Profiles: pm}

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles",
		strings.NewReader(`{"name":"myclaude","bin":"claude","base":"clean"}`)))
	if rec.Code != 201 {
		t.Fatalf("create code %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	if !strings.Contains(rec.Body.String(), `"myclaude"`) || !strings.Contains(rec.Body.String(), `"editable":true`) {
		t.Fatalf("list missing created editable profile: %s", rec.Body)
	}

	// invalid create → 400
	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles",
		strings.NewReader(`{"name":"bad","bin":"x","base":"nope"}`)))
	if rec.Code != 400 {
		t.Fatalf("invalid create want 400, got %d", rec.Code)
	}

	// delete a stored profile → 204
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("DELETE", "/api/profiles/myclaude", nil))
	if rec.Code != 204 {
		t.Fatalf("delete stored code %d", rec.Code)
	}

	// deleting a config seed → 404
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("DELETE", "/api/profiles/bash", nil))
	if rec.Code != 404 {
		t.Fatalf("delete config seed want 404, got %d", rec.Code)
	}
}

func TestProfileReplyableRoundTripsThroughTheAPI(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles",
		strings.NewReader(`{"name":"rc","bin":"claude","runtime":"host","replyable":true}`)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/rc", nil))
	if !strings.Contains(rec.Body.String(), `"replyable":true`) {
		t.Fatalf("GET lost replyable: %s", rec.Body)
	}
	for _, v := range pm.Views() {
		if v.Name == "rc" && !v.Replyable {
			t.Fatal("the view does not report the reply default")
		}
	}
}

// The editor sends the binding as profile.aws and reads it back from
// GET /api/profiles/{name}; an empty runtime is refused at save, which is why
// the editor sends "box" explicitly when it sends a binding.
func TestProfileAWSBindingRoundTripsThroughTheAPI(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	body := func(runtime string) string {
		return `{"name":"infra","bin":"claude",` + runtime + `"aws":{"source_profile":"sandbox-admin",` +
			`"role_arn":"arn:aws:iam::123456789012:role/agentbox-agent","region":"ap-southeast-1",` +
			`"s3_read":["culishop-tfstate","culishop-logs/AWSLogs/"],"label":"sandbox","prod":true}}`
	}

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body(""))))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "runtime: box") {
		t.Fatalf("empty runtime with a binding: code %d body %s, want 400 naming runtime: box", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body(`"runtime":"box",`))))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/infra", nil))
	for _, want := range []string{
		`"runtime":"box"`, `"source_profile":"sandbox-admin"`, `"role_arn":"arn:aws:iam::123456789012:role/agentbox-agent"`,
		`"region":"ap-southeast-1"`, `"s3_read":["culishop-tfstate","culishop-logs/AWSLogs/"]`, `"label":"sandbox"`, `"prod":true`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET lost %s: %s", want, rec.Body)
		}
	}
}

func TestProfilesAPIShareWarnings(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = t.TempDir()
	s := &Server{Profiles: pm}
	body := func(share string) string {
		return `{"name":"cl","bin":"claude","runtime":"box","clone_of":"/home/u/repo","default_cwd":"/home/u/clones/repo"` + share + `}`
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body(`,"clone_share":[".env"]`))))
	if rec.Code != 201 {
		t.Fatalf("POST code %d body %s", rec.Code, rec.Body)
	}
	var created struct {
		Name     string   `json:"name"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Name != "cl" || len(created.Warnings) != 1 {
		t.Fatalf("POST body %s (%v), want name cl and 1 warning", rec.Body, err)
	}
	if !strings.Contains(logs.String(), created.Warnings[0]) {
		t.Errorf("the warning must also be logged, log = %q", logs.String())
	}

	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/cl", strings.NewReader(body(`,"clone_share":[".env"]`))))
	var updated struct {
		Warnings []string `json:"warnings"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &updated) != nil || len(updated.Warnings) != 1 || updated.Warnings[0] != created.Warnings[0] {
		t.Fatalf("PUT with the share: code %d body %s, want 200 with the same warning", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/cl", strings.NewReader(body(`,"clone_share":["docs"]`))))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("PUT without a warning: code %d body %q, want 204 and no body", rec.Code, rec.Body)
	}

	// The editor loads GET /api/profiles/{name}, which carries the clone fields
	// pfLoadedClone keeps (the list view does not).
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/cl", nil))
	for _, want := range []string{`"clone_of":"/home/u/repo"`, `"clone_share":["docs"]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET lost %s: %s", want, rec.Body)
		}
	}

	// POST without a warning omits the field.
	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(strings.Replace(body(""), `"cl"`, `"cl2"`, 1))))
	if rec.Code != 201 || strings.Contains(rec.Body.String(), "warnings") {
		t.Fatalf("POST without a warning: code %d body %s, want 201 and no warnings key", rec.Code, rec.Body)
	}
}

func TestPUTProfileWithBoxImageIs400(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(`{"name":"p","bin":"bash"}`)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/p", strings.NewReader(`{"name":"p","bin":"bash","box":{"image":"x:1"}}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "box_image") {
		t.Fatalf("PUT with box.image: code %d body %s, want 400 naming box_image", rec.Code, rec.Body)
	}
}

func TestPOSTProfileWithCAEnvIs400(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(`{"name":"p","bin":"bash","env":{"NODE_EXTRA_CA_CERTS":"/x"}}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "NODE_EXTRA_CA_CERTS") {
		t.Fatalf("POST with a CA env var: code %d body %s, want 400 naming it", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(`{"name":"p","bin":"bash"}`)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/p", strings.NewReader(`{"name":"p","bin":"bash","env":{"SSL_CERT_FILE":"/x"}}`)))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "SSL_CERT_FILE") {
		t.Fatalf("PUT with a CA env var: code %d body %s, want 400 naming it", rec.Code, rec.Body)
	}
}

// convertRoutes is a Server with the credential manager wired to a profile
// manager that holds the given seed profiles.
type convertRoutes struct {
	srv *Server
	svc *fakeAWSSvc
	fx  *credFixture
}

func newConvertRoutes(t *testing.T, base config.Config, seeds []config.AgentConfig) *convertRoutes {
	t.Helper()
	fx := newCredFixture(t, base, seeds)
	svc := &fakeAWSSvc{check: awsprobe.Result{State: awsprobe.NoneFound}}
	fx.cm.chk = svc
	return &convertRoutes{srv: &Server{Profiles: fx.pm, Creds: fx.cm, CredAWS: svc, CredAudit: fx.rec}, svc: svc, fx: fx}
}

func (c *convertRoutes) convert(name, body string, kind credKind, origin string) *httptest.ResponseRecorder {
	return do(c.srv.handleProfile, storeReq("POST", "/api/profiles/"+name+"/convert-aws", body, origin, kind))
}

func inlineAWSProfile(name string) config.AgentConfig {
	return config.AgentConfig{Name: name, Bin: "bash", Runtime: config.RuntimeBox,
		AWS: &config.AWSBinding{SourceProfile: "p", RoleARN: arnRead, Region: "us-east-1", Label: "dev"}}
}

func convertBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not json: %s", w.Body)
	}
	return m
}

func TestConvertAWSHappy(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("Dev.Box")); err != nil {
		t.Fatal(err)
	}
	w := c.convert("Dev.Box", "", credCookie, credOrigin)
	if w.Code != 200 {
		t.Fatalf("code %d %s", w.Code, w.Body)
	}
	if got := convertBody(t, w)["connection"]; got != "dev-box" {
		t.Fatalf("connection = %v", got)
	}
	p, _ := c.fx.pm.Get("Dev.Box")
	if p.AWS != nil || len(p.AWSConnections) != 1 || p.AWSConnections[0] != "dev-box" {
		t.Fatalf("profile not swapped: aws=%v conns=%v", p.AWS, p.AWSConnections)
	}
	var conn *config.AWSConnection
	for _, e := range c.fx.file(t).AWSConnections {
		if e.Name == "dev-box" {
			conn = &e
		}
	}
	if conn == nil || conn.RoleARN != arnRead || conn.Region != "us-east-1" || conn.Label != "dev" ||
		conn.Description != "converted from profile Dev.Box" {
		t.Fatalf("connection wrong: %+v", conn)
	}
}

func TestConvertNameTaken(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.fx.cm.PutAWS(context.Background(), awsConn("dev", "p", arnRead), true, false); err != nil {
		t.Fatal(err)
	}
	w := c.convert("dev", `{}`, credCookie, credOrigin)
	if w.Code != 200 || convertBody(t, w)["connection"] != "dev-aws" {
		t.Fatalf("code %d %s", w.Code, w.Body)
	}
	p, _ := c.fx.pm.Get("dev")
	if p.AWS != nil || len(p.AWSConnections) != 1 || p.AWSConnections[0] != "dev-aws" {
		t.Fatalf("profile: %+v", p)
	}
	// the first connection is untouched
	if got := len(c.fx.file(t).AWSConnections); got != 2 {
		t.Fatalf("connections = %d", got)
	}
}

func TestConvertNameTakenBySeedAndFallbackTaken(t *testing.T) {
	base := config.Config{AWSConnections: []config.AWSConnection{awsConn("dev", "p", arnRead)}}
	c := newConvertRoutes(t, base, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	if w := c.convert("dev", "", credCookie, credOrigin); w.Code != 200 || convertBody(t, w)["connection"] != "dev-aws" {
		t.Fatalf("seed collision: %d %s", w.Code, w.Body)
	}
	// both names are now taken
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 409 || convertBody(t, w)["code"] != "exists" {
		t.Fatalf("both taken: %d %s", w.Code, w.Body)
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS == nil {
		t.Fatal("a refused convert changed the profile")
	}
}

func TestConvertReservedNameFallsBack(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("default")); err != nil {
		t.Fatal(err)
	}
	if w := c.convert("default", "", credCookie, credOrigin); w.Code != 200 || convertBody(t, w)["connection"] != "default-aws" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestConvertSlug(t *testing.T) {
	long := strings.Repeat("a", 70)
	for in, want := range map[string]string{
		"Dev.Box": "dev-box", "--x_y": "x-y", "___": "aws", "": "aws", long: strings.Repeat("a", 63),
	} {
		if got := convertSlug(in); got != want {
			t.Errorf("convertSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := convertFallback(strings.Repeat("a", 63)); len(got) != 63 || !strings.HasSuffix(got, "-aws") {
		t.Errorf("fallback = %q", got)
	}
}

func TestConvertNeedsAck(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	c.svc.check = awsprobe.Result{State: awsprobe.Writes}
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 409 || convertBody(t, w)["code"] != "needs_ack" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(c.fx.file(t).AWSConnections) != 0 {
		t.Fatal("a connection was created without the acknowledgement")
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS == nil {
		t.Fatal("the profile changed without the acknowledgement")
	}
	w = c.convert("dev", `{"ack_write":true}`, credCookie, credOrigin)
	if w.Code != 200 {
		t.Fatalf("acknowledged: %d %s", w.Code, w.Body)
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS != nil || len(p.AWSConnections) != 1 {
		t.Fatalf("profile: %+v", p)
	}
}

// failSaveStore is a profile store whose Save fails.
type failSaveStore struct{ profileStore }

func (failSaveStore) Save(config.AgentConfig) error { return errors.New("disk full") }

func TestConvertRollbackOnProfileSaveFailure(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.fx.pm.store = failSaveStore{c.fx.pm.store}
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "disk full") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := c.fx.file(t).AWSConnections; len(got) != 0 {
		t.Fatalf("the connection was left behind: %+v", got)
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS == nil {
		t.Fatal("the profile changed")
	}
	// the delete is audited, so the trail shows both the create and the undo
	var undone bool
	for _, e := range c.fx.rec.events {
		if e.Action == broker.CredDeleted && e.Name == "dev" {
			undone = true
		}
	}
	if !undone {
		t.Fatalf("no delete in the audit: %+v", c.fx.rec.events)
	}
}

func TestConvertSeedProfile(t *testing.T) {
	seed := inlineAWSProfile("seeded")
	c := newConvertRoutes(t, config.Config{}, []config.AgentConfig{seed})
	w := c.convert("seeded", "", credCookie, credOrigin)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	p, _ := c.fx.pm.Get("seeded")
	if p.AWS != nil || len(p.AWSConnections) != 1 || p.AWSConnections[0] != "seeded" {
		t.Fatalf("profile: %+v", p)
	}
	if _, ok := c.fx.pm.store.Get("seeded"); !ok {
		t.Fatal("the converted seed was not saved as a runtime override")
	}
	for _, v := range c.fx.pm.Views() {
		if v.Name == "seeded" && !v.Editable {
			t.Fatal("the override is not the editable copy")
		}
	}
}

func TestConvertRefusals(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	if err := c.fx.pm.Save(config.AgentConfig{Name: "plain", Bin: "bash"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, origin string
		kind               credKind
		want               int
	}{
		{"dev", "", credOrigin, credBearer, 403},
		{"dev", "", credOrigin, credNone, 403},
		{"dev", "", "", credCookie, 403},
		{"dev", "", "http://evil.example", credCookie, 403},
		{"nope", "", credOrigin, credCookie, 404},
		{"plain", "", credOrigin, credCookie, 400},
		{"dev", `{"ack_write":true,"extra":1}`, credOrigin, credCookie, 400},
		{"dev", `{"ack_write":true} {}`, credOrigin, credCookie, 400},
	} {
		if w := c.convert(tc.name, tc.body, tc.kind, tc.origin); w.Code != tc.want {
			t.Errorf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	if w := do(c.srv.handleProfile, storeReq("GET", "/api/profiles/dev/convert-aws", "", credOrigin, credCookie)); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
	if n := c.svc.n(); n != 0 {
		t.Fatalf("a refused request reached AWS: %v", c.svc.calls)
	}
	if len(c.fx.file(t).AWSConnections) != 0 {
		t.Fatal("a refused request created a connection")
	}
}

func TestConvertWithoutManagerOrBroker(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.srv.CredAWS = nil
	if w := c.convert("dev", "", credCookie, credOrigin); w.Code != 409 || convertBody(t, w)["code"] != "no_broker" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// onFirstCheck runs f during the first base-role check only, the way a second
// browser tab acts while a convert waits on AWS.
func (c *convertRoutes) onFirstCheck(f func()) {
	var done atomic.Bool // not sync.Once: the hook may start another convert, whose check re-enters it
	c.svc.onCheck = func() {
		if done.CompareAndSwap(false, true) {
			f()
		}
	}
}

func TestConvertKeepsAnEditMadeDuringTheCheck(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.onFirstCheck(func() {
		p, _ := c.fx.pm.Get("dev")
		p.Env = map[string]string{"EDITED": "yes"}
		if err := c.fx.pm.Save(p); err != nil {
			t.Error(err)
		}
	})
	if w := c.convert("dev", "", credCookie, credOrigin); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	p, _ := c.fx.pm.Get("dev")
	if p.Env["EDITED"] != "yes" || p.AWS != nil || len(p.AWSConnections) != 1 {
		t.Fatalf("the concurrent edit was lost or the swap missing: %+v", p)
	}
}

func TestConvertRefusesWhenTheBindingChangedDuringTheCheck(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.onFirstCheck(func() {
		p, _ := c.fx.pm.Get("dev")
		p.AWS = p.AWS.Clone()
		p.AWS.Region = "eu-west-1"
		if err := c.fx.pm.Save(p); err != nil {
			t.Error(err)
		}
	})
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 409 || convertBody(t, w)["code"] != "profile_changed" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := c.fx.file(t).AWSConnections; len(got) != 0 {
		t.Fatalf("connection left behind: %+v", got)
	}
	if p, _ := c.fx.pm.Get("dev"); p.AWS == nil || p.AWS.Region != "eu-west-1" {
		t.Fatalf("the newer binding was touched: %+v", p)
	}
}

func TestConvertDoesNotResurrectAProfileDeletedDuringTheCheck(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	c.onFirstCheck(func() {
		if err := c.fx.pm.Delete("dev"); err != nil {
			t.Error(err)
		}
	})
	w := c.convert("dev", "", credCookie, credOrigin)
	if w.Code != 409 || convertBody(t, w)["code"] != "profile_changed" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, ok := c.fx.pm.Get("dev"); ok {
		t.Fatal("the deleted profile came back")
	}
	if got := c.fx.file(t).AWSConnections; len(got) != 0 {
		t.Fatalf("connection left behind: %+v", got)
	}
}

// A second convert that runs to completion while the first waits on AWS (a
// double click): one connection stays, referenced; the other is taken back out.
func TestConvertConcurrentSecondConvertLeavesNoOrphan(t *testing.T) {
	c := newConvertRoutes(t, config.Config{}, nil)
	if err := c.fx.pm.Save(inlineAWSProfile("dev")); err != nil {
		t.Fatal(err)
	}
	var inner *httptest.ResponseRecorder
	c.onFirstCheck(func() { inner = c.convert("dev", "", credCookie, credOrigin) })
	outer := c.convert("dev", "", credCookie, credOrigin)
	if inner == nil || inner.Code != 200 {
		t.Fatalf("inner: %v", inner)
	}
	if outer.Code != 409 || convertBody(t, outer)["code"] != "profile_changed" {
		t.Fatalf("outer: %d %s", outer.Code, outer.Body)
	}
	p, _ := c.fx.pm.Get("dev")
	conns := c.fx.file(t).AWSConnections
	if p.AWS != nil || len(p.AWSConnections) != 1 || len(conns) != 1 || conns[0].Name != p.AWSConnections[0] {
		t.Fatalf("profile %+v, connections %+v", p, conns)
	}
}

func TestProfilesAPIContextWarnings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = t.TempDir()
	s := &Server{Profiles: pm}
	body := func(name, path string) string {
		return `{"name":"` + name + `","bin":"claude","runtime":"box","context_paths":["` + path + `"]}`
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body("c1", dir))))
	var created struct {
		Name     string   `json:"name"`
		Warnings []string `json:"warnings"`
	}
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &created) != nil || len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], "look like secrets") {
		t.Fatalf("POST: code %d body %s, want 201 with a secrets warning", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), created.Warnings[0]) {
		t.Errorf("the warning must also be logged, log = %q", logs.String())
	}

	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/c1", strings.NewReader(body("c1", dir))))
	var updated struct {
		Warnings []string `json:"warnings"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &updated) != nil || len(updated.Warnings) != 1 || !strings.Contains(updated.Warnings[0], "look like secrets") {
		t.Fatalf("PUT: code %d body %s, want 200 with a secrets warning", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/c1", strings.NewReader(body("c1", clean))))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("PUT clean: code %d body %q, want 204 and no body", rec.Code, rec.Body)
	}
}

func TestProfileImportsOffRoundTripsThroughTheAPI(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles",
		strings.NewReader(`{"name":"io","bin":"claude","runtime":"box","imports_off":true}`)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/io", nil))
	if !strings.Contains(rec.Body.String(), `"imports_off":true`) {
		t.Fatalf("GET lost imports_off: %s", rec.Body)
	}
	found := false
	for _, v := range pm.Views() {
		if v.Name == "io" {
			found = true
			if !v.ImportsOff {
				t.Fatal("the list view does not report imports_off")
			}
		}
	}
	if !found {
		t.Fatal("profile missing from the list view")
	}
}

func TestSaveWarningsEgressWithoutBinding(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.home = t.TempDir()
	s := &Server{Profiles: pm}
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleProfiles(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(body)))
		if rec.Code == 409 || rec.Code == 201 {
			rec = httptest.NewRecorder()
			s.handleProfile(rec, httptest.NewRequest("PUT", "/api/profiles/e1", strings.NewReader(body)))
		}
		return rec
	}
	rec := put(`{"name":"e1","bin":"claude","runtime":"box","egress_open":true}`)
	var out struct {
		Warnings []string `json:"warnings"`
	}
	want := config.AgentConfig{EgressOpen: true}.EgressWarning()
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Warnings) != 1 || out.Warnings[0] != want {
		t.Fatalf("unbound: code %d body %s", rec.Code, rec.Body)
	}
	rec = put(`{"name":"e1","bin":"claude","runtime":"box","egress_open":true,"aws":{"source_profile":"sandbox-admin",` +
		`"role_arn":"arn:aws:iam::123456789012:role/agentbox-agent","region":"ap-southeast-1","s3_read":["culishop-tfstate"]}}`)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("bound: code %d body %s, want 204", rec.Code, rec.Body)
	}
}
