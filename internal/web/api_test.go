package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/session"
)

type mockSvc struct {
	list           []session.Info
	getOK          bool
	killed         string
	spawnInfo      session.Info
	spawnErr       error
	spawnedCwd     string
	spawnedOptions map[string]any
	spawnedRuntime string
	renamedID      string
	renamedName    string
	renameErr      error
}

func (m *mockSvc) List() []session.Info { return m.list }
func (m *mockSvc) Spawn(s launcher.SpawnSpec) (session.Info, error) {
	m.spawnedCwd = s.Cwd
	m.spawnedOptions = s.Options
	m.spawnedRuntime = s.Runtime
	if m.spawnErr != nil {
		return session.Info{}, m.spawnErr
	}
	return m.spawnInfo, nil
}

func TestAPI_ForwardsOptions(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"claude","options":{"skip":true,"effort":"high"}}`)))
	if rec.Code != 201 {
		t.Fatalf("code %d", rec.Code)
	}
	if svc.spawnedOptions["skip"] != true || svc.spawnedOptions["effort"] != "high" {
		t.Fatalf("options not forwarded: %v", svc.spawnedOptions)
	}
}
func (m *mockSvc) Kill(id string) error { m.killed = id; return nil }
func (m *mockSvc) Get(id string) (session.Info, bool) {
	return session.Info{ID: id, Name: m.renamedName}, m.getOK
}
func (m *mockSvc) Rename(id, name string) error {
	m.renamedID, m.renamedName = id, name
	return m.renameErr
}

func TestAPI_Rename(t *testing.T) {
	svc := &mockSvc{getOK: true}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/x-1",
		strings.NewReader(`{"name":"new"}`)))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if svc.renamedID != "x-1" || svc.renamedName != "new" {
		t.Fatalf("rename not forwarded: id=%q name=%q", svc.renamedID, svc.renamedName)
	}
	var info session.Info
	_ = json.NewDecoder(rec.Body).Decode(&info)
	if info.Name != "new" {
		t.Fatalf("body name=%q want new", info.Name)
	}
}

func TestAPI_RenameUnknown(t *testing.T) {
	svc := &mockSvc{renameErr: session.ErrNotFound}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/nope",
		strings.NewReader(`{"name":"x"}`)))
	if rec.Code != 404 {
		t.Fatalf("code %d want 404", rec.Code)
	}
}

func TestAPI_RenameEmpty(t *testing.T) {
	svc := &mockSvc{renameErr: session.ErrInvalidName}
	a := &API{Sessions: svc}
	rec := httptest.NewRecorder()
	a.handleSession(rec, httptest.NewRequest("PATCH", "/api/sessions/x-1",
		strings.NewReader(`{"name":"  "}`)))
	if rec.Code != 400 {
		t.Fatalf("code %d want 400", rec.Code)
	}
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

// An unrecognised tier must be rejected outright. Falling through to a default
// would let a typo silently de-contain a session.
func TestSpawnRejectsUnknownRuntime(t *testing.T) {
	a := &API{Sessions: &mockSvc{spawnInfo: session.Info{ID: "x"}}, DefaultCwd: "/tmp"}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"bash","runtime":"vm"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body)
	}
}

func TestSpawnAcceptsTheHostTierOverride(t *testing.T) {
	svc := &mockSvc{spawnInfo: session.Info{ID: "x"}}
	a := &API{Sessions: svc, DefaultCwd: "/tmp"}
	rec := httptest.NewRecorder()
	a.handleSessions(rec, httptest.NewRequest("POST", "/api/sessions",
		strings.NewReader(`{"agent":"bash","runtime":"host"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	if svc.spawnedRuntime != "host" {
		t.Fatalf("spec.Runtime = %q, want host", svc.spawnedRuntime)
	}
}
