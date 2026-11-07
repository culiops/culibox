package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/auth"
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

// A navigation that carried a session cookie the guard rejected (expired, or
// signed with another secret) lands on /login?reason=expired, so the page can say
// the login expired instead of looking like a first visit (F73).
func TestAuthGuard_ExpiredCookieRedirectsWithReason(t *testing.T) {
	s, _ := testServerWithAuth(t)
	secret := []byte("0123456789abcdef0123456789abcdef") // testServerWithAuth's
	for name, val := range map[string]string{
		// Correctly signed, but past its expiry: the real "login expired" case.
		"expired": agedSessionCookie(secret, time.Now().Add(-time.Hour).Unix()).Value,
		"forged":  "not-a-session",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Accept", "text/html")
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: val})
		w := httptest.NewRecorder()
		s.authGuard(okHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?reason=expired" {
			t.Fatalf("%s cookie: code=%d loc=%q want 303 /login?reason=expired", name, w.Code, w.Header().Get("Location"))
		}
	}
}

// A first visit, or a logout (the cleared cookie is not sent, or is sent empty),
// is not an expiry: /login stays plain.
func TestAuthGuard_NoCookieRedirectsPlain(t *testing.T) {
	s, _ := testServerWithAuth(t)
	for name, ck := range map[string]*http.Cookie{
		"absent": nil,
		"empty":  {Name: auth.CookieName, Value: ""},
		"other":  {Name: "unrelated", Value: "x"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Accept", "text/html")
		if ck != nil {
			r.AddCookie(ck)
		}
		w := httptest.NewRecorder()
		s.authGuard(okHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
			t.Fatalf("%s cookie: code=%d loc=%q want 303 /login", name, w.Code, w.Header().Get("Location"))
		}
	}
}

// browserSends models the one browser rule this depends on: a cookie whose Expires
// has passed is dropped, never sent.
func browserSends(ck *http.Cookie, at time.Time) bool {
	return ck.Expires.IsZero() || ck.Expires.After(at)
}

// The real path: a session that times out naturally. The browser keeps the cookie
// past the signed expiry (Session.Issue), so the guard sees a refused cookie and says
// "expired"; only once the browser has dropped it does the visit look like a first one.
func TestAuthGuard_NaturalExpiryNamesItself(t *testing.T) {
	const ttl = 30 * 24 * time.Hour
	t0 := time.Unix(1_700_000_000, 0)
	now := t0
	ts, _ := auth.OpenTokenStore(t.TempDir() + "/tokens.json")
	s := &Server{
		Session: auth.NewSessionWithClock([]byte("0123456789abcdef0123456789abcdef"), ttl, func() time.Time { return now }),
		Tokens:  ts,
	}
	iw := httptest.NewRecorder()
	s.Session.Issue(iw, false)
	ck := iw.Result().Cookies()[0]

	nav := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Accept", "text/html")
		if browserSends(ck, now) {
			r.AddCookie(ck)
		}
		w := httptest.NewRecorder()
		s.authGuard(okHandler()).ServeHTTP(w, r)
		return w
	}
	check := func(label string, wantCode int, wantLoc string) {
		t.Helper()
		if w := nav(); w.Code != wantCode || w.Header().Get("Location") != wantLoc {
			t.Fatalf("%s: code=%d loc=%q want %d %q", label, w.Code, w.Header().Get("Location"), wantCode, wantLoc)
		}
	}
	now = t0.Add(ttl - time.Hour)
	check("still valid", http.StatusOK, "")
	now = t0.Add(ttl + time.Hour)
	if !browserSends(ck, now) {
		t.Fatal("the browser dropped the cookie the instant it expired: the guard cannot tell expiry from a first visit")
	}
	check("just expired", http.StatusSeeOther, "/login?reason=expired")
	now = t0.Add(ttl + 29*24*time.Hour)
	check("expired a month ago", http.StatusSeeOther, "/login?reason=expired")
	now = t0.Add(ttl + 31*24*time.Hour)
	if browserSends(ck, now) {
		t.Fatal("the cookie must not outlive its grace")
	}
	check("long gone", http.StatusSeeOther, "/login")
}

// A bearer token wins over a stale session cookie riding along with it.
func TestAuthGuard_BearerWithStaleCookieStillAdmitted(t *testing.T) {
	s, plain := testServerWithAuth(t)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	r.AddCookie(agedSessionCookie([]byte("0123456789abcdef0123456789abcdef"), time.Now().Add(-time.Hour).Unix()))
	var seen credKind
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = credKindOf(r)
		w.WriteHeader(200)
	})
	w := httptest.NewRecorder()
	s.authGuard(h).ServeHTTP(w, r)
	if w.Code != 200 || seen != credBearer {
		t.Fatalf("code=%d kind=%d want 200,credBearer", w.Code, seen)
	}
}

// Fetch and WebSocket callers with a stale cookie still get a bare 401: the reason
// is for a page navigation only, and a redirected upgrade is a confusing failure.
func TestAuthGuard_ExpiredCookieAPIStill401(t *testing.T) {
	s, _ := testServerWithAuth(t)
	stale := agedSessionCookie([]byte("0123456789abcdef0123456789abcdef"), time.Now().Add(-time.Hour).Unix())
	cases := map[string]func(*http.Request){
		"fetch": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Accept", "application/json")
		},
		"xhr": func(r *http.Request) { r.Header.Set("Accept", "*/*") },
		"ws":  func(r *http.Request) { r.Header.Set("Upgrade", "websocket"); r.Header.Set("Accept", "text/html") },
	}
	for name, mod := range cases {
		r := httptest.NewRequest("GET", "/api/sessions", nil)
		r.AddCookie(stale)
		mod(r)
		w := httptest.NewRecorder()
		s.authGuard(okHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" {
			t.Fatalf("%s: code=%d loc=%q want 401 with no Location", name, w.Code, w.Header().Get("Location"))
		}
	}
}

// GET /login is a static file: the reason parameter selects a fixed line in the
// page's own script, and the server never echoes the query string back.
func TestLoginPage_ReasonNotReflected(t *testing.T) {
	s, _ := routedServer(t)
	const payload = "<script>x</script>"
	for _, q := range []string{"reason=" + payload, "e=" + payload, "reason=expired"} {
		r := httptest.NewRequest("GET", "/login?"+q, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /login?%s: code=%d want 200", q, w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, payload) || strings.Contains(body, "x</script>") {
			t.Fatalf("GET /login?%s reflected the parameter into the page", q)
		}
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
