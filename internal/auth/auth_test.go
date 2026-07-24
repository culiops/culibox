package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/culiops/culiops-agent-console/internal/config"
)

func TestBasicAuth(t *testing.T) {
	called := false
	h := BasicAuth("me", "secret", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("me", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != 200 {
		t.Fatalf("correct creds failed: code=%d called=%v", rec.Code, called)
	}

	called = false
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("me", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called || rec.Code != 401 {
		t.Fatalf("wrong creds: code=%d called=%v", rec.Code, called)
	}
}

func TestResolveCred(t *testing.T) {
	t.Setenv("AC_PASS", "s3cr3t")
	u, p, err := ResolveCred(config.Config{BasicAuthUser: "me", BasicAuthPassEnv: "AC_PASS"})
	if err != nil || u != "me" || p != "s3cr3t" {
		t.Fatalf("resolve: %v %q %q", err, u, p)
	}
	if _, _, err := ResolveCred(config.Config{BasicAuthUser: "me", BasicAuthPassEnv: "MISSING_VAR_XYZ"}); err == nil {
		t.Fatal("missing env should error")
	}
}

func TestCookieAuth(t *testing.T) {
	ca, err := NewCookieAuth("")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ca.Issue(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie issued")
	}

	req := httptest.NewRequest("GET", "/ws/x", nil)
	req.AddCookie(cookies[0])
	if !ca.Valid(req) {
		t.Fatal("issued cookie should be valid")
	}
	if ca.Valid(httptest.NewRequest("GET", "/ws/x", nil)) {
		t.Fatal("no cookie should be invalid")
	}
}

// A cookie minted before a daemon restart must stay valid after it: the WS
// route is cookie-only gated, so an ephemeral per-process secret would 401 the
// auto-reconnect forever. A persisted secret (same path across "processes")
// fixes it.
func TestCookieAuthPersistedSecretSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.secret")

	ca1, err := NewCookieAuth(path) // first process
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	ca1.Issue(rec)
	cookie := rec.Result().Cookies()[0]

	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("secret not persisted: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file perms = %o, want 600", fi.Mode().Perm())
	}

	ca2, err := NewCookieAuth(path) // simulated restart: same path
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/ws/x", nil)
	req.AddCookie(cookie)
	if !ca2.Valid(req) {
		t.Fatal("cookie minted before restart should stay valid after restart (persisted secret)")
	}

	// A different secret must not validate the cookie.
	other, err := NewCookieAuth(filepath.Join(t.TempDir(), "cookie.secret"))
	if err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest("GET", "/ws/x", nil)
	req2.AddCookie(cookie)
	if other.Valid(req2) {
		t.Fatal("a different secret must not validate the cookie")
	}
}

// A present-but-malformed secret file (torn write / hand-edit) is regenerated
// to a working 32-byte secret rather than bricking startup.
func TestCookieAuthMalformedSecretRegenerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.secret")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	ca, err := NewCookieAuth(path)
	if err != nil {
		t.Fatalf("malformed secret should regenerate, got error: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 32 {
		t.Fatalf("secret not regenerated to 32 bytes: len=%d err=%v", len(b), err)
	}
	rec := httptest.NewRecorder()
	ca.Issue(rec)
	req := httptest.NewRequest("GET", "/ws/x", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	if !ca.Valid(req) {
		t.Fatal("regenerated secret should mint valid cookies")
	}
}
