package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/auth"
)

func testServerWithAuth(t *testing.T) (*Server, string) {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	ts, err := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := ts.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Session: auth.NewSession(secret, 30*24*time.Hour),
		Tokens:  ts,
	}
	return s, plain
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

func TestAuthGuard_RejectsNoCredential(t *testing.T) {
	s, _ := testServerWithAuth(t)
	w := httptest.NewRecorder()
	s.authGuard(okHandler()).ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("WWW-Authenticate must never be set")
	}
}

func TestAuthGuard_AcceptsBearer(t *testing.T) {
	s, plain := testServerWithAuth(t)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	s.authGuard(okHandler()).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("bearer code=%d want 200", w.Code)
	}
}

func TestAuthGuard_AcceptsCookieAndTagsKind(t *testing.T) {
	s, _ := testServerWithAuth(t)
	iw := httptest.NewRecorder()
	s.Session.Issue(iw, false)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.AddCookie(iw.Result().Cookies()[0])

	var seen credKind
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = credKindOf(r)
		w.WriteHeader(200)
	})
	w := httptest.NewRecorder()
	s.authGuard(h).ServeHTTP(w, r)
	if w.Code != 200 || seen != credCookie {
		t.Fatalf("code=%d kind=%d want 200,credCookie", w.Code, seen)
	}
}

func TestAuthGuard_RedirectsBrowserNavToLogin(t *testing.T) {
	s, _ := testServerWithAuth(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.authGuard(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("code=%d loc=%q want 303 /login", w.Code, w.Header().Get("Location"))
	}
}

// A WebSocket handshake is a GET; even with an Accept: text/html it must 401,
// never 303 — a redirected upgrade is a confusing failure (review finding #3).
func TestAuthGuard_WSUpgradeNotRedirected(t *testing.T) {
	s, _ := testServerWithAuth(t)
	r := httptest.NewRequest("GET", "/ws/x", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.authGuard(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ws upgrade anon: code=%d want 401 (not a redirect)", w.Code)
	}
}

func TestSecureCookie_ForwardedProto(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if secureCookie(r) {
		t.Fatal("plain http should not be secure")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !secureCookie(r) {
		t.Fatal("XFP=https should be secure")
	}
}
