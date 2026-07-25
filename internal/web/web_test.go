package web

import (
	"encoding/json"
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

	// The file endpoints read the user's home directory, so registering one on
	// the wrong mux would expose it unauthenticated. Assert the newest of them
	// is gated, not just that it exists.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/fs/search?q=x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("fs search without basic-auth: want 401, got %d", rec.Code)
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

func TestHandleMeta(t *testing.T) {
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, newFakeStore(), func([]config.AgentConfig) {},
	)
	s := &Server{EnvLabel: "PROD", EnvColor: "#b45309", ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["env_label"] != "PROD" || got["env_color"] != "#b45309" {
		t.Fatalf("got %v", got)
	}
	if got["shell_profile"] != "zsh" {
		t.Fatalf("shell_profile = %q, want zsh", got["shell_profile"])
	}
}

// A stored profile overrides a same-named seed (see ProfileManager.merged), so the
// quick-shell name alone is not a guarantee. If the name no longer resolves to the
// detected shell binary the console must offer no quick shell rather than silently
// launching whatever took the name.
func TestHandleMetaShellProfileShadowed(t *testing.T) {
	fs := newFakeStore()
	if err := fs.Save(config.AgentConfig{Name: "zsh", Bin: "python3"}); err != nil {
		t.Fatal(err)
	}
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, fs, func([]config.AgentConfig) {},
	)
	s := &Server{ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty (shadowed by a profile running python3)", got["shell_profile"])
	}
}

// /api/meta re-issues the WS cookie. The /ws route is cookie-only gated and the
// cookie was previously minted only on a full dashboard load, so a tab whose cookie
// went stale (regenerated secret) could never reconnect — it retried forever with no
// way back short of a manual reload. /api/meta is behind the same basic auth as "/",
// so minting here is the same trust level and lets a live tab self-heal.
func TestHandleMetaReissuesWSCookie(t *testing.T) {
	ca, err := auth.NewCookieAuth("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Cookie: ca}
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			got = c
		}
	}
	if got == nil {
		t.Fatalf("no %s cookie set; cookies = %v", auth.CookieName, rec.Result().Cookies())
	}
	// The minted cookie must actually authenticate /ws.
	probe := httptest.NewRequest("GET", "/ws/x", nil)
	probe.AddCookie(got)
	if !ca.Valid(probe) {
		t.Fatal("the cookie issued by /api/meta does not validate")
	}
}

// Without the pieces needed to verify the profile, the answer must be "no quick
// shell" — never an unverified name.
func TestHandleMetaFailsClosedWithoutProfiles(t *testing.T) {
	s := &Server{ShellProfile: "zsh"} // no Profiles, no ShellBin
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty (unverifiable)", got["shell_profile"])
	}
}

// The same check must not disable a legitimate quick shell.
func TestHandleMetaShellProfileIntact(t *testing.T) {
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, newFakeStore(), func([]config.AgentConfig) {},
	)
	s := &Server{ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["shell_profile"] != "zsh" {
		t.Fatalf("shell_profile = %q, want zsh", got["shell_profile"])
	}
}

// The key must always be present: the frontend treats an empty shell_profile as
// "no quick shell available" and hides the +sh button, so a missing key (rather
// than an empty one) would be an ambiguous signal.
func TestHandleMetaNoShellProfile(t *testing.T) {
	s := &Server{}
	h := s.Handler("admin", "pw")
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.SetBasicAuth("admin", "pw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["shell_profile"]; !ok {
		t.Fatal("shell_profile key must always be present (empty = no quick shell)")
	}
	if got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty", got["shell_profile"])
	}
}
