package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/session"
)

// shellReq builds POST /api/sessions/{id}/shell as the given credential kind;
// origin "" sends no Origin header.
func shellReq(id, origin, body string, kind credKind) *http.Request {
	r := httptest.NewRequest("POST", "/api/sessions/"+id+"/shell", strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r.WithContext(context.WithValue(r.Context(), credKindKey, kind))
}

func shellAPI(svc *mockSvc, qs string) *API {
	return &API{Sessions: svc, DefaultCwd: "/tmp", QuickShell: func() string { return qs }}
}

func liveParent() session.Info {
	return session.Info{ID: "p1", Cwd: "/work/proj", Runtime: "box", Agent: "claude"}
}

func TestShellSpawnRequiresCookie(t *testing.T) {
	svc := &mockSvc{list: []session.Info{liveParent()}, spawnInfo: session.Info{ID: "s1"}}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("p1", "http://"+host, "", credBearer))
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "shells are started from the web UI") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if svc.spawnedSpec.Agent != "" {
		t.Fatalf("spawned despite 403: %+v", svc.spawnedSpec)
	}
}

func TestShellSpawnRequiresSameOrigin(t *testing.T) {
	for _, origin := range []string{"", "http://evil.example"} {
		svc := &mockSvc{list: []session.Info{liveParent()}, spawnInfo: session.Info{ID: "s1"}}
		rec := httptest.NewRecorder()
		shellAPI(svc, "sh").handleSession(rec, shellReq("p1", origin, "", credCookie))
		if rec.Code != 403 || svc.spawnedSpec.Agent != "" {
			t.Fatalf("origin %q: got %d, spawned %+v", origin, rec.Code, svc.spawnedSpec)
		}
	}
}

func TestShellSpawnMethod(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/sessions/p1/shell", nil)
	rec := httptest.NewRecorder()
	shellAPI(&mockSvc{}, "sh").handleSession(rec, r)
	if rec.Code != 405 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestShellSpawnUnknownParent(t *testing.T) {
	svc := &mockSvc{list: []session.Info{liveParent()}}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("nope", "http://"+host, "", credCookie))
	if rec.Code != 404 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestShellSpawnExitedParent(t *testing.T) {
	p := liveParent()
	p.Exited = true
	svc := &mockSvc{list: []session.Info{p}}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("p1", "http://"+host, "", credCookie))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "parent session has ended") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if svc.spawnedSpec.Agent != "" {
		t.Fatalf("spawned under an exited parent")
	}
}

func TestShellSpawnNoShellProfile(t *testing.T) {
	svc := &mockSvc{list: []session.Info{liveParent()}}
	for _, a := range []*API{shellAPI(svc, ""), {Sessions: svc}} { // empty name, and QuickShell unwired
		rec := httptest.NewRecorder()
		a.handleSession(rec, shellReq("p1", "http://"+host, "", credCookie))
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "no shell profile") {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	}
}

func TestShellSpawnInheritsCwdAndTier(t *testing.T) {
	for _, rt := range []string{"box", "host"} {
		p := liveParent()
		p.Runtime = rt
		svc := &mockSvc{list: []session.Info{p}, spawnInfo: session.Info{ID: "s1", ParentID: "p1"}}
		rec := httptest.NewRecorder()
		shellAPI(svc, "sh").handleSession(rec, shellReq("p1", "http://"+host, "", credCookie))
		if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"parent_id":"p1"`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		sp := svc.spawnedSpec
		if sp.Agent != "sh" || sp.Cwd != "/work/proj" || sp.Runtime != rt || sp.Name != "sh:proj" || sp.ParentID != "p1" {
			t.Fatalf("runtime %s: spec = %+v", rt, sp)
		}
	}
}

func TestShellSpawnFlattensToGrandparent(t *testing.T) {
	child := session.Info{ID: "s1", Cwd: "/elsewhere", Runtime: "host", ParentID: "p1"}
	svc := &mockSvc{list: []session.Info{liveParent(), child}, spawnInfo: session.Info{ID: "s2"}}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("s1", "http://"+host, "", credCookie))
	if rec.Code != 201 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	sp := svc.spawnedSpec
	if sp.ParentID != "p1" || sp.Cwd != "/work/proj" || sp.Runtime != "box" {
		t.Fatalf("not flattened onto the grandparent: %+v", sp)
	}
}

func TestShellSpawnFlattenedParentGone(t *testing.T) {
	child := session.Info{ID: "s1", Cwd: "/x", ParentID: "gone"}
	svc := &mockSvc{list: []session.Info{child}}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("s1", "http://"+host, "", credCookie))
	if rec.Code != 409 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestShellSpawnIgnoresBody(t *testing.T) {
	svc := &mockSvc{list: []session.Info{liveParent()}, spawnInfo: session.Info{ID: "s1"}}
	rec := httptest.NewRecorder()
	body := `{"cwd":"/etc","runtime":"host","parent_id":"x","agent":"claude","options":{"skip":true}}`
	shellAPI(svc, "sh").handleSession(rec, shellReq("p1", "http://"+host, body, credCookie))
	if rec.Code != 201 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	sp := svc.spawnedSpec
	if sp.Cwd != "/work/proj" || sp.Runtime != "box" || sp.ParentID != "p1" || sp.Agent != "sh" || sp.Options != nil {
		t.Fatalf("body leaked into the spec: %+v", sp)
	}
}

func TestShellSpawnErrorUsesSpawnErr(t *testing.T) {
	svc := &mockSvc{list: []session.Info{liveParent()}, spawnErr: errors.New("boom")}
	rec := httptest.NewRecorder()
	shellAPI(svc, "sh").handleSession(rec, shellReq("p1", "http://"+host, "", credCookie))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "could not start session") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
