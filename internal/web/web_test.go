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
	ca, _ := auth.NewCookieAuth()
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
	ca, _ := auth.NewCookieAuth()
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
