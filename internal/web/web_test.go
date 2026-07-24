package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/auth"
	"github.com/culiops/culiops-agent-console/internal/config"
)

// TestWSRouteRequiresCookieNotBasicAuth verifies the B2 fix: the /ws/ route is
// NOT behind basic-auth (browsers can't set Authorization on a WebSocket) and
// instead requires the signed cookie. Without the cookie it must 401 before
// touching the terminal handler.
func TestWSRouteRequiresCookieNotBasicAuth(t *testing.T) {
	ca, _ := auth.NewCookieAuth("")
	termHit := false
	s := &Server{
		API:      &API{Sessions: &mockSvc{}},
		Cookie:   ca,
		Terminal: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { termHit = true }),
	}
	h := s.Handler("u", "p")

	// No cookie, no basic-auth (the browser WS case) -> 401, terminal untouched.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ws/demo", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ws without cookie: want 401, got %d", rec.Code)
	}
	if termHit {
		t.Fatal("terminal handler must not run without a valid cookie")
	}
}

func TestAPIRouteRequiresBasicAuth(t *testing.T) {
	ca, _ := auth.NewCookieAuth("")
	s := &Server{API: &API{Sessions: &mockSvc{}}, Cookie: ca, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, newFakeStore(), func([]config.AgentConfig) {})}
	h := s.Handler("u", "p")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api without basic-auth: want 401, got %d", rec.Code)
	}

	req := httptest.NewRequest("GET", "/api/profiles", nil)
	req.SetBasicAuth("u", "p")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api with basic-auth: want 200, got %d", rec.Code)
	}
}

// TestStaticNoCacheOnAppFiles: the app's own files (index.html, app.js) must
// carry Cache-Control: no-cache so a normal reload always shows the latest;
// the vendored /xterm/* bundle stays cacheable.
func TestStaticNoCacheOnAppFiles(t *testing.T) {
	ca, _ := auth.NewCookieAuth("")
	s := &Server{API: &API{Sessions: &mockSvc{}}, Cookie: ca, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, newFakeStore(), func([]config.AgentConfig) {})}
	h := s.Handler("u", "p")

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth("u", "p")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, p := range []string{"/", "/app.js"} {
		if cc := get(p).Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s Cache-Control=%q, want no-cache", p, cc)
		}
	}
	if cc := get("/xterm/xterm.js").Header().Get("Cache-Control"); cc == "no-cache" {
		t.Fatal("/xterm/xterm.js should stay cacheable, got no-cache")
	}
}
