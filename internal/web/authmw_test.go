package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		AllowedHosts: []string{"example.com"},
		Session:      auth.NewSession(secret, 30*24*time.Hour),
		Tokens:       ts,
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

// An aged-but-valid cookie run through authGuard must re-emit a fresh Set-Cookie
// (the sliding-renewal wiring — the integration point most likely to regress).
// The cookie is crafted directly (valid HMAC over an exp whose implied issue
// time is well past the 24h refresh threshold) since the Session clock is not
// injectable from this package.
func agedSessionCookie(secret []byte, exp int64) *http.Cookie {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	val := strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(mac.Sum(nil))
	return &http.Cookie{Name: auth.CookieName, Value: val}
}

func TestAuthGuard_SlidingRefreshEmitsCookie(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ts, _ := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
	s := &Server{Session: auth.NewSession(secret, 30*24*time.Hour), Tokens: ts}

	// exp = now + 10d, so with a 30d TTL the implied issue time is ~20d ago —
	// valid (not expired) but well past the 24h refresh threshold.
	exp := time.Now().Add(10 * 24 * time.Hour).Unix()
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.AddCookie(agedSessionCookie(secret, exp))
	w := httptest.NewRecorder()
	s.authGuard(okHandler()).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("aged valid cookie: code=%d want 200", w.Code)
	}
	var reissued *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			reissued = c
		}
	}
	if reissued == nil {
		t.Fatal("authGuard did not re-issue the cookie past the refresh threshold")
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
