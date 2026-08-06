package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/auth"
)

func routedServer(t *testing.T) (*Server, string) {
	t.Helper()
	ts, _ := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
	plain, _, _ := ts.Create("test")
	s := &Server{
		Session:  auth.NewSession([]byte("0123456789abcdef0123456789abcdef"), 30*24*time.Hour),
		Tokens:   ts,
		Password: "pw",
		Terminal: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }),
		API:      &API{},
	}
	return s, plain
}

// GET /login must be reachable unauthenticated and serve the actual form.
func TestRouter_LoginPageIsPublic(t *testing.T) {
	s, _ := routedServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /login want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `name="password"`) {
		t.Fatal("/login did not serve the login form")
	}
}

func TestRouter_ProtectedRejectsAnonAPI(t *testing.T) {
	s, _ := routedServer(t)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/workspaces", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon API code=%d want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("WWW-Authenticate leaked")
	}
}

func TestRouter_BearerReachesProtectedAPI(t *testing.T) {
	s, plain := routedServer(t)
	r := httptest.NewRequest("GET", "/api/workspaces", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bearer API code=%d want 200", w.Code)
	}
}

// A cross-site POST with a bearer token must be allowed (no cookie, no CSRF) —
// including through the fs-mutation handlers' own same-origin re-check.
func TestRouter_BearerSkipsCSRF(t *testing.T) {
	s, plain := routedServer(t)
	r := httptest.NewRequest("POST", "/api/fs/mkdir", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatal("bearer POST wrongly blocked by CSRF guard")
	}
}

func TestRouter_CookiePostCrossSiteBlocked(t *testing.T) {
	s, _ := routedServer(t)
	iw := httptest.NewRecorder()
	s.Session.Issue(iw, false)
	r := httptest.NewRequest("POST", "/api/fs/mkdir", nil)
	r.AddCookie(iw.Result().Cookies()[0])
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site cookie POST code=%d want 403", w.Code)
	}
}

func TestRouter_WSAcceptsBearer(t *testing.T) {
	s, plain := routedServer(t)
	r := httptest.NewRequest("GET", "/ws/", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ws bearer code=%d want 200", w.Code)
	}
}

// An unauthenticated WS handshake must 401, never redirect (review finding #3).
func TestRouter_WSRejectsAnon(t *testing.T) {
	s, _ := routedServer(t)
	r := httptest.NewRequest("GET", "/ws/", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon WS code=%d want 401", w.Code)
	}
}
