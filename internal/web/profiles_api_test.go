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
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/store"
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
	rec = deleteProfile(s, "myclaude")
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

// The server half of "every save writes the tier the page shows" (profilepage.js pfvSaveBlock). A stored profile that names no
// runtime is listed under the instance's default tier, which is what the page shows; but ValidateProfile sees only an explicit
// runtime, so a binding saved without one is refused even though the list says "box". The page therefore sends the tier it shows,
// and the server accepts exactly that.
func TestSaveOnAProfileThatStoresNoRuntime(t *testing.T) {
	tok := config.StaticToken{Name: "sentry-corp", Value: config.KeyRef{Type: "env", Var: "S"},
		Env: []string{"SENTRY_ACCESS_TOKEN"}, Egress: []config.TokenEgress{{Host: "sentry.corp.example"}}}
	newServer := func(def string) *Server {
		pm := NewProfileManager(nil, nil, def, nil, newFakeStore(), func([]config.AgentConfig) {})
		pm.SetStaticTokens([]config.StaticToken{tok})
		return &Server{Profiles: pm}
	}
	get := func(s *Server) string {
		rec := httptest.NewRecorder()
		s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/p", nil))
		return rec.Body.String()
	}

	t.Run("an instance with no default: the list says box, and a Cloud and tokens save must carry it", func(t *testing.T) {
		s := newServer("")
		if rec := postProfile(s, `{"name":"p","bin":"claude"}`); rec.Code != 201 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		if got := s.Profiles.Views()[0].Runtime; got != config.RuntimeBox {
			t.Fatalf("the list shows %q for a profile that stores no runtime, want box", got)
		}
		// What a Cloud and tokens save sent before the page wrote the tier: refused, whatever the list says.
		rec := putProfile(t, s, "p", `{"name":"p","bin":"claude","tokens":["sentry-corp"]}`)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "runtime: box") {
			t.Fatalf("a binding with no explicit runtime: code %d body %s, want 400 naming runtime: box", rec.Code, rec.Body)
		}
		rec = putProfile(t, s, "p", `{"name":"p","bin":"claude","runtime":"box","tokens":["sentry-corp"]}`)
		if !savedClean(rec) {
			t.Fatalf("the shown tier with a token: code %d body %s, want 200 and a rev", rec.Code, rec.Body)
		}
		for _, want := range []string{`"runtime":"box"`, `"tokens":["sentry-corp"]`} {
			if !strings.Contains(get(s), want) {
				t.Errorf("GET lost %s: %s", want, get(s))
			}
		}
	})

	t.Run("an instance that defaults to host: the list says host, and an Agent save pins it", func(t *testing.T) {
		s := newServer(config.RuntimeHost)
		if rec := postProfile(s, `{"name":"p","bin":"claude"}`); rec.Code != 201 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		if got := s.Profiles.Views()[0].Runtime; got != config.RuntimeHost {
			t.Fatalf("the list shows %q for a profile that stores no runtime, want host", got)
		}
		rec := putProfile(t, s, "p", `{"name":"p","bin":"claude","runtime":"host"}`)
		if !savedClean(rec) {
			t.Fatalf("the shown tier host: code %d body %s, want 200 and a rev", rec.Code, rec.Body)
		}
		if !strings.Contains(get(s), `"runtime":"host"`) {
			t.Errorf("GET lost the tier: %s", get(s))
		}
	})
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

	rec = putProfile(t, s, "cl", body(`,"clone_share":[".env"]`))
	var updated struct {
		Warnings []string `json:"warnings"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &updated) != nil || len(updated.Warnings) != 1 || updated.Warnings[0] != created.Warnings[0] {
		t.Fatalf("PUT with the share: code %d body %s, want 200 with the same warning", rec.Code, rec.Body)
	}

	rec = putProfile(t, s, "cl", body(`,"clone_share":["docs"]`))
	if !savedClean(rec) {
		t.Fatalf("PUT without a warning: code %d body %q, want 200 with a rev and no warnings", rec.Code, rec.Body)
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
	rec = putProfile(t, s, "p", `{"name":"p","bin":"bash","box":{"image":"x:1"}}`)
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
	rec = putProfile(t, s, "p", `{"name":"p","bin":"bash","env":{"SSL_CERT_FILE":"/x"}}`)
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
	if w.Code != 500 || strings.Contains(w.Body.String(), "disk full") {
		t.Fatalf("%d %s, want 500 that does not echo the cause", w.Code, w.Body)
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

// contextTempDir is a fresh directory that checkContextPath accepts. t.TempDir
// follows GOTMPDIR/TMPDIR, which can land under a path checkContextPath
// refuses: the box image sets GOTMPDIR under /home/agent, and macOS puts
// TMPDIR under /var.
func contextTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "ctxw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestProfilesAPIContextWarnings(t *testing.T) {
	dir := contextTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	clean := contextTempDir(t)
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

	rec = putProfile(t, s, "c1", body("c1", dir))
	var updated struct {
		Warnings []string `json:"warnings"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &updated) != nil || len(updated.Warnings) != 1 || !strings.Contains(updated.Warnings[0], "look like secrets") {
		t.Fatalf("PUT: code %d body %s, want 200 with a secrets warning", rec.Code, rec.Body)
	}

	rec = putProfile(t, s, "c1", body("c1", clean))
	if !savedClean(rec) {
		t.Fatalf("PUT clean: code %d body %q, want 200 with a rev and no warnings", rec.Code, rec.Body)
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
			rec = putProfile(t, s, "e1", body)
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
	if !savedClean(rec) {
		t.Fatalf("bound: code %d body %s, want 200 with a rev and no warnings", rec.Code, rec.Body)
	}
}

// TestSaveRefusalCarriesFieldPath pins the wire shape of a 400 save refusal: the
// sentence an older client matches on, unchanged, plus the "field" path the
// refusal is about. A refusal that names no field has no "field" key.
func TestSaveRefusalCarriesFieldPath(t *testing.T) {
	cwdReason, bad := UnboxableDefaultCwd("/", "/home/nobody", nil, nil)
	if !bad || cwdReason == "" {
		t.Fatalf("UnboxableDefaultCwd(/) = %q, %v; the default_cwd row needs a refused directory", cwdReason, bad)
	}
	rows := []struct {
		name  string
		body  string
		setup func(*ProfileManager)
		want  string // the sentence, as today's code words it
		field string // "" = the refusal names no field
	}{
		{
			name:  "relative context path at index 1",
			body:  `{"name":"p","bin":"claude","runtime":"box","context_paths":["/srv/ok","relative/dir"]}`,
			want:  `profile "p": context_paths "relative/dir" must be an absolute path`,
			field: "context_paths[1]",
		},
		{
			name:  "aws_connections on the host tier",
			body:  `{"name":"p","bin":"claude","runtime":"host","aws_connections":["acme-read"]}`,
			want:  `profile "p": aws_connections require runtime: box (set it explicitly); the host tier cannot contain credentials`,
			field: "aws_connections",
		},
		{
			name:  "undefined token at index 0",
			body:  `{"name":"p","bin":"claude","runtime":"box","tokens":["nope"]}`,
			want:  `profile "p": tokens[0] "nope" is not defined in static_tokens`,
			field: "tokens[0]",
		},
		{
			name:  "bad clone_share entry at index 0",
			body:  `{"name":"p","bin":"claude","runtime":"box","clone_of":"/srv/repo","default_cwd":"/srv/clone","clone_share":["/abs"]}`,
			want:  `profile "p": clone_share "/abs": must be relative to clone_of`,
			field: "clone_share[0]",
		},
		{
			name:  "clone_of on the host tier",
			body:  `{"name":"p","bin":"claude","runtime":"host","clone_of":"/srv/repo","default_cwd":"/srv/clone"}`,
			want:  `profile "p": clone mode runs only in a box; remove runtime "host" or clone_of`,
			field: "clone_of",
		},
		{
			name:  "egress_allow on the host tier",
			body:  `{"name":"p","bin":"claude","runtime":"host","egress_allow":["example.com"]}`,
			want:  `profile "p": egress_allow and egress_open need runtime: box (set it explicitly)`,
			field: "egress_allow",
		},
		{
			name:  "context_paths on an instance that defaults to host",
			body:  `{"name":"p","bin":"claude","context_paths":["/srv/ok"]}`,
			setup: func(pm *ProfileManager) { pm.defaultRuntime = config.RuntimeHost },
			want:  config.ErrContextNeedsBox.Error(),
			field: "context_paths",
		},
		{
			name: "disk check of the second context path",
			body: `{"name":"p","bin":"claude","runtime":"box","context_paths":["/srv/ok","/srv/bad"]}`,
			setup: func(pm *ProfileManager) {
				pm.SetContextCheck(func(p string) error {
					if p == "/srv/bad" {
						return errors.New(`context_paths "/srv/bad": does not exist`)
					}
					return nil
				})
			},
			want:  `context_paths "/srv/bad": does not exist`,
			field: "context_paths[1]",
		},
		{
			name: "disk check of the clone's repo",
			body: `{"name":"p","bin":"claude","runtime":"box","clone_of":"/srv/repo","default_cwd":"/srv/clone"}`,
			setup: func(pm *ProfileManager) {
				pm.SetCloneCheck(func(config.Clone) error { return errors.New(`clone_of "/srv/repo" is not a git repository`) })
			},
			want:  `clone_of "/srv/repo" is not a git repository`,
			field: "clone_of",
		},
		{
			name:  "clone on an instance that defaults to host",
			body:  `{"name":"p","bin":"claude","clone_of":"/srv/repo","default_cwd":"/srv/clone"}`,
			setup: func(pm *ProfileManager) { pm.defaultRuntime = config.RuntimeHost },
			want:  `clone mode needs runtime "box" (this instance defaults to host)`,
			field: "clone_of",
		},
		{
			name:  "default_cwd a box cannot hold",
			body:  `{"name":"p","bin":"claude","runtime":"box","default_cwd":"/"}`,
			want:  "default working dir cannot be contained: " + cwdReason,
			field: "default_cwd",
		},
		{
			name:  "undefined GitHub App on owner 0",
			body:  `{"name":"p","bin":"claude","runtime":"box","github":{"owners":[{"owner":"acme","app":"missing","repos":["r"]}]}}`,
			want:  `profile "p": github.owners[0].app "missing" is not defined in github_apps`,
			field: "github.owners[0].app",
		},
		{
			name:  "agent account that is not configured",
			body:  `{"name":"p","bin":"claude","account":"work"}`,
			want:  `profile "p": agent accounts are not configured`,
			field: "account",
		},
		{
			name:  "env the account owns",
			body:  `{"name":"p","bin":"claude","env":{"CLAUDE_CONFIG_DIR":"/x"}}`,
			want:  `profile "p": CLAUDE_CONFIG_DIR is set by the agent account, not the profile`,
			field: "env",
		},
		{
			name: "a refusal that names no field",
			body: `{"name":"p","bin":"bash","box":{"image":"x:1"}}`,
			want: `profile "p": box.image is not per-profile; every box runs the instance-wide box_image (set it in the config)`,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "profiles.json"))
			if err != nil {
				t.Fatal(err)
			}
			pm := NewProfileManager(nil, nil, "", nil, st, func([]config.AgentConfig) {})
			pm.home = "/home/nobody"
			s := &Server{Profiles: pm}
			if rec := postProfile(s, `{"name":"p","bin":"bash"}`); rec.Code != 201 {
				t.Fatalf("create: %d %s", rec.Code, rec.Body)
			}
			if row.setup != nil {
				row.setup(pm)
			}
			rec := putProfile(t, s, "p", row.body)
			if rec.Code != 400 {
				t.Fatalf("PUT: code %d body %s, want 400", rec.Code, rec.Body)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not a JSON object of strings: %s", rec.Body)
			}
			if got["error"] != row.want {
				t.Errorf("error = %q\nwant    %q", got["error"], row.want)
			}
			if f, has := got["field"]; has != (row.field != "") || f != row.field {
				t.Errorf("field = %q (present %v), want %q", f, has, row.field)
			}
			wantKeys := 1
			if row.field != "" {
				wantKeys = 2
			}
			if len(got) != wantKeys {
				t.Errorf("body has %d keys, want %d: %s", len(got), wantKeys, rec.Body)
			}
		})
	}
}

// savedClean: a successful PUT answered 200 with a revision and no warnings.
func savedClean(rec *httptest.ResponseRecorder) bool {
	var got map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		return false
	}
	rev, _ := got["rev"].(string)
	_, warned := got["warnings"]
	return rev != "" && !warned
}

func TestProfilesListHostBlock(t *testing.T) {
	seeds := []config.AgentConfig{
		{Name: "aws", Bin: "claude", Runtime: "box", AWSConnections: []string{"prod"}},
		{Name: "plain", Bin: "claude", Runtime: "box"},
	}
	pm := NewProfileManager(seeds, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	if rec := postProfile(s, `{"name":"locked","bin":"claude","runtime":"box","box_only":true}`); rec.Code != 201 {
		t.Fatalf("create box_only profile: %d %s", rec.Code, rec.Body)
	}

	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("list is not JSON: %v: %s", err, rec.Body)
	}
	byName := map[string]map[string]any{}
	for _, p := range raw {
		byName[p["name"].(string)] = p
	}
	for name, want := range map[string]string{
		"aws":    "this profile has an AWS binding, so it runs only in a box",
		"locked": "this profile is set to box only",
	} {
		if got, _ := byName[name]["host_block"].(string); got != want {
			t.Errorf("%s: host_block = %q, want %q", name, got, want)
		}
	}
	if _, present := byName["plain"]["host_block"]; present {
		t.Errorf("plain: host_block must be absent, got %v", byName["plain"]["host_block"])
	}
	if byName["locked"]["box_only"] != true {
		t.Errorf("locked: box_only = %v, want true", byName["locked"]["box_only"])
	}
	for _, name := range []string{"aws", "plain"} {
		if _, present := byName[name]["box_only"]; present {
			t.Errorf("%s: box_only must be absent, got %v", name, byName[name]["box_only"])
		}
	}

	// The single-profile GET (the editor's read) carries box_only too, and a PUT
	// that keeps it leaves the lock on.
	rec = httptest.NewRecorder()
	s.handleProfile(rec, httptest.NewRequest("GET", "/api/profiles/locked", nil))
	if !strings.Contains(rec.Body.String(), `"box_only":true`) {
		t.Fatalf("GET lost box_only: %s", rec.Body)
	}
	if rec := putProfile(t, s, "locked", `{"name":"locked","bin":"claude","runtime":"box","box_only":true}`); !savedClean(rec) {
		t.Fatalf("PUT keeping box_only: %d %s", rec.Code, rec.Body)
	}
	if p, _ := pm.Get("locked"); !p.BoxOnly {
		t.Fatal("a PUT that sends box_only dropped it")
	}
}

func TestProfileSaveBoxOnlyHostRefused(t *testing.T) {
	pm := NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	if rec := postProfile(s, `{"name":"p","bin":"claude","runtime":"box"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := putProfile(t, s, "p", `{"name":"p","bin":"claude","runtime":"host","box_only":true}`)
	if rec.Code != 400 {
		t.Fatalf("PUT box_only + host: code %d body %s, want 400", rec.Code, rec.Body)
	}
	body := apiBody(t, rec)
	if body["field"] != "box_only" {
		t.Errorf("field = %v, want box_only (body %s)", body["field"], rec.Body)
	}
	if e, _ := body["error"].(string); !strings.Contains(e, `box_only cannot be combined with runtime "host"; remove one`) {
		t.Errorf("error = %q, want the box_only/host sentence", e)
	}
	rec = postProfile(s, `{"name":"q","bin":"claude","runtime":"host","box_only":true}`)
	if rec.Code != 400 || apiBody(t, rec)["field"] != "box_only" {
		t.Errorf("POST box_only + host: code %d body %s, want 400 on box_only", rec.Code, rec.Body)
	}
}

// boxCredByName GETs /api/profiles and returns each profile's raw JSON object.
func boxCredByName(t *testing.T, s *Server) map[string]map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleProfiles(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("list is not JSON: %v: %s", err, rec.Body)
	}
	byName := map[string]map[string]any{}
	for _, p := range raw {
		byName[p["name"].(string)] = p
	}
	return byName
}

func boxCredSeeds() []config.AgentConfig {
	return []config.AgentConfig{
		{Name: "claude", Bin: "claude"},
		{Name: "ops", Bin: "/usr/local/bin/claude", Account: "claude-box"},
		{Name: "sh", Bin: "bash"},
	}
}

func TestProfilesListBoxClaudeCredential(t *testing.T) {
	pm := NewProfileManager(boxCredSeeds(), nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	byKey := func(m map[string]secretchain.Presence) func(config.AgentConfig) secretchain.Presence {
		return func(a config.AgentConfig) secretchain.Presence {
			p, ok := m[a.Name]
			if !ok {
				t.Errorf("hook called for %q", a.Name)
			}
			return p
		}
	}
	pm.SetBoxClaudeCredential(byKey(map[string]secretchain.Presence{
		"claude": secretchain.Absent, "ops": secretchain.StoredLocked, "sh": secretchain.Present,
	}))
	got := boxCredByName(t, s)
	for name, want := range map[string]string{"claude": "no", "ops": "locked"} {
		if v, _ := got[name]["box_claude_credential"].(string); v != want {
			t.Errorf("%s: box_claude_credential = %v, want %q", name, got[name]["box_claude_credential"], want)
		}
	}
	if v, present := got["sh"]["box_claude_credential"]; present {
		t.Errorf("sh: the key must be absent on a non-claude profile, got %v", v)
	}

	pm.SetBoxClaudeCredential(byKey(map[string]secretchain.Presence{
		"claude": secretchain.Present, "ops": secretchain.StoredUnavailable, "sh": secretchain.Absent,
	}))
	got = boxCredByName(t, s)
	for name, want := range map[string]string{"claude": "yes", "ops": "locked"} {
		if v, _ := got[name]["box_claude_credential"].(string); v != want {
			t.Errorf("%s: box_claude_credential = %v, want %q", name, got[name]["box_claude_credential"], want)
		}
	}
	if _, present := got["sh"]["box_claude_credential"]; present {
		t.Errorf("sh: the key must stay absent, got %v", got["sh"]["box_claude_credential"])
	}
}

func TestProfilesListNoBoxCredentialHook(t *testing.T) {
	pm := NewProfileManager(boxCredSeeds(), nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{Profiles: pm}
	for name, p := range boxCredByName(t, s) {
		if v, present := p["box_claude_credential"]; present {
			t.Errorf("%s: no hook set, but box_claude_credential = %v", name, v)
		}
	}
}

// The credential probe can start a process (the macOS keychain), so Views asks it
// with m.mu released: a hook that takes the lock must not deadlock.
func TestProfilesViewsAsksBoxCredentialWithoutTheLock(t *testing.T) {
	pm := NewProfileManager(boxCredSeeds(), nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	var calls atomic.Int32
	pm.SetBoxClaudeCredential(func(a config.AgentConfig) secretchain.Presence {
		calls.Add(1)
		if _, ok := pm.Get(a.Name); !ok { // takes m.mu
			t.Errorf("Get(%q) found nothing", a.Name)
		}
		return secretchain.Present
	})
	done := make(chan []ProfileView, 1)
	go func() { done <- pm.Views() }()
	select {
	case views := <-done:
		if n := calls.Load(); n != 2 {
			t.Errorf("hook called %d times, want 2 (the two claude profiles)", n)
		}
		for _, v := range views {
			want := ""
			if v.Claude {
				want = "yes"
			}
			if v.BoxClaudeCredential != want {
				t.Errorf("%s: BoxClaudeCredential = %q, want %q", v.Name, v.BoxClaudeCredential, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Views deadlocked: the box credential hook ran with m.mu held")
	}
}
