package web

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/tmux"
)

type mockSvc struct {
	list      []tmux.Session
	getOK     bool
	killed    string
	spawnName string
	spawnErr  error
}

func (m *mockSvc) List() ([]tmux.Session, error) { return m.list, nil }
func (m *mockSvc) Spawn(launcher.SpawnSpec) (tmux.Session, error) {
	if m.spawnErr != nil {
		return tmux.Session{}, m.spawnErr
	}
	return tmux.Session{Name: m.spawnName}, nil
}
func (m *mockSvc) Kill(name string) error { m.killed = name; return nil }
func (m *mockSvc) Get(name string) (tmux.Session, bool, error) {
	return tmux.Session{Name: name}, m.getOK, nil
}

func TestAPI_ListSpawnDelete(t *testing.T) {
	svc := &mockSvc{list: []tmux.Session{{Name: "a"}}, getOK: true, spawnName: "agent-claude"}
	a := &API{Sessions: svc}

	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("GET", "/api/sessions", nil))
	if rec.Code != 200 {
		t.Fatalf("GET code %d", rec.Code)
	}
	var got []sessionDTO
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("list %+v", got)
	}

	rec = httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions", strings.NewReader(`{"agent":"claude"}`)))
	if rec.Code != 201 {
		t.Fatalf("POST code %d body %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("DELETE", "/api/sessions/a", nil))
	if rec.Code != 204 {
		t.Fatalf("DELETE code %d", rec.Code)
	}
	if svc.killed != "a" {
		t.Fatalf("killed %q", svc.killed)
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
	a := &API{Sessions: &mockSvc{spawnErr: errors.New("boom")}}
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
