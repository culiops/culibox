package web

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
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
