package web

import (
	"net/http/httptest"
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
