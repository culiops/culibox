package web

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/session"
)

type mockSvc struct {
	list       []session.Info
	getOK      bool
	killed     string
	spawnInfo  session.Info
	spawnErr   error
	spawnedCwd string
}

func (m *mockSvc) List() []session.Info { return m.list }
func (m *mockSvc) Spawn(s launcher.SpawnSpec) (session.Info, error) {
	m.spawnedCwd = s.Cwd
	if m.spawnErr != nil {
		return session.Info{}, m.spawnErr
	}
	return m.spawnInfo, nil
}
func (m *mockSvc) Kill(id string) error { m.killed = id; return nil }
func (m *mockSvc) Get(id string) (session.Info, bool) {
	return session.Info{ID: id}, m.getOK
}

func TestAPI_ListSpawnDelete(t *testing.T) {
	svc := &mockSvc{
		list:      []session.Info{{ID: "bash-1", Agent: "bash"}},
		getOK:     true,
		spawnInfo: session.Info{ID: "bash-2", Agent: "bash"},
	}
	a := &API{Sessions: svc}

	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("GET", "/api/sessions", nil))
	if rec.Code != 200 {
		t.Fatalf("GET code %d", rec.Code)
	}
	var got []session.Info
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].ID != "bash-1" {
		t.Fatalf("list %+v", got)
	}

	rec = httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash"}`)))
	if rec.Code != 201 {
		t.Fatalf("POST code %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("DELETE", "/api/sessions/bash-1", nil))
	if rec.Code != 204 {
		t.Fatalf("DELETE code %d", rec.Code)
	}
	if svc.killed != "bash-1" {
		t.Fatalf("killed %q", svc.killed)
	}
}

func TestAPI_DefaultsCwd(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/home/me"}

	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash"}`)))
	if svc.spawnedCwd != "/home/me" {
		t.Fatalf("empty cwd should default to DefaultCwd, got %q", svc.spawnedCwd)
	}

	rec = httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"bash","cwd":"/tmp/x"}`)))
	if svc.spawnedCwd != "/tmp/x" {
		t.Fatalf("explicit cwd should win, got %q", svc.spawnedCwd)
	}
}

func TestAPI_DeleteUnknownIs404(t *testing.T) {
	a := &API{Sessions: &mockSvc{getOK: false}}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("DELETE", "/api/sessions/ghost", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404 got %d", rec.Code)
	}
}

func TestAPI_SpawnErrorIsStructured(t *testing.T) {
	a := &API{Sessions: &mockSvc{spawnErr: errors.New("unknown agent")}}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"x"}`)))
	if rec.Code != 400 {
		t.Fatalf("want 400 got %d", rec.Code)
	}
	var body map[string]string
	if json.Unmarshal(rec.Body.Bytes(), &body); body["error"] == "" {
		t.Fatalf("expected structured error body, got %s", rec.Body)
	}
}
