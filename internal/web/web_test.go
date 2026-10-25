package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/config"
)

// issueCookie ensures s has a Session and returns a valid session cookie for it.
// Call before s.Handler() so the handler captures the Session.
func issueCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	if s.Session == nil {
		s.Session = auth.NewSession([]byte("0123456789abcdef0123456789abcdef"), 30*24*time.Hour)
	}
	iw := httptest.NewRecorder()
	s.Session.Issue(iw, false)
	return iw.Result().Cookies()[0]
}

// TestWSRouteRejectsAnonAcceptsCred: /ws/ is behind authGuard — no credential
// 401s before the terminal runs; a valid session cookie reaches it.
func TestWSRouteRejectsAnonAcceptsCred(t *testing.T) {
	termHit := false
	s := &Server{
		AllowedHosts: []string{"example.com"},
		API:          &API{Sessions: &mockSvc{}},
		Terminal:     http.HandlerFunc(func(http.ResponseWriter, *http.Request) { termHit = true }),
	}
	ck := issueCookie(t, s)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ws/demo", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ws without credential: want 401, got %d", rec.Code)
	}
	if termHit {
		t.Fatal("terminal handler must not run without a credential")
	}

	req := httptest.NewRequest("GET", "/ws/demo", nil)
	req.AddCookie(ck)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !termHit {
		t.Fatal("terminal handler should run with a valid session cookie")
	}
}

func TestProtectedRoutesGatedAndRegistered(t *testing.T) {
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
	ck := issueCookie(t, s)
	h := s.Handler()

	// No credential → 401, and never a basic-auth challenge.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api without credential: want 401, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("WWW-Authenticate must never be set")
	}

	req := httptest.NewRequest("GET", "/api/profiles", nil)
	req.AddCookie(ck)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api with session cookie: want 200, got %d", rec.Code)
	}

	// The file endpoints read AND WRITE the user's home, so a route on the wrong
	// mux would be unauthenticated. Assert each is gated (401 anon) AND that a
	// real handler answers when authed (not a mux 404).
	for _, probe := range []struct {
		method, path string
		authed       int
	}{
		{"GET", "/api/fs/search?q=x", http.StatusOK},
		{"GET", "/api/fs/archive?path=/tmp", http.StatusForbidden}, // outside home
		{"POST", "/api/fs/upload?dir=/tmp", http.StatusForbidden},  // outside home
		{"GET", "/api/fs/upload?dir=/tmp", http.StatusMethodNotAllowed},
		{"POST", "/api/fs/mkdir", http.StatusBadRequest},
		{"POST", "/api/fs/rename", http.StatusBadRequest},
		{"POST", "/api/fs/delete", http.StatusBadRequest},
		{"GET", "/api/fs/delete", http.StatusMethodNotAllowed},
		{"POST", "/api/bugreport", http.StatusServiceUnavailable}, // no builder wired
		{"GET", "/api/bugreport", http.StatusMethodNotAllowed},
	} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(probe.method, probe.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without credential: want 401, got %d", probe.method, probe.path, rec.Code)
		}

		authedReq := httptest.NewRequest(probe.method, probe.path, nil)
		authedReq.AddCookie(ck)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, authedReq)
		if rec.Code != probe.authed {
			t.Errorf("%s %s authed: want %d, got %d (route registered?)",
				probe.method, probe.path, probe.authed, rec.Code)
		}
	}
}

// TestStaticNoCacheOnAppFiles: the app's own files (index.html, app.js) must
// carry Cache-Control: no-cache; the vendored /xterm/* bundle stays cacheable.
func TestStaticNoCacheOnAppFiles(t *testing.T) {
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
	ck := issueCookie(t, s)
	h := s.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(ck)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, p := range []string{"/", "/app.js", "/links.js", "/app.css"} {
		if cc := get(p).Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s Cache-Control=%q, want no-cache", p, cc)
		}
	}
	if cc := get("/xterm/xterm.js").Header().Get("Cache-Control"); cc == "no-cache" {
		t.Fatal("/xterm/xterm.js should stay cacheable, got no-cache")
	}
}

// TestSharedStylesheetsArePublic: login.html is served unauthenticated, so the
// stylesheets it links must be reachable unauthenticated too — otherwise
// authGuard answers the <link> with a redirect and the page renders unstyled.
// Only tokens.css and primitives.css are exposed: a colour palette, which the
// login page's own appearance already reveals. app.css stays protected.
func TestSharedStylesheetsArePublic(t *testing.T) {
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
	h := s.Handler()

	// A per-file sentinel unique to that file's body, so this test also
	// catches the two routes accidentally serving the same file (still 200).
	sentinels := map[string]string{
		"/tokens.css":     "--bg:",
		"/primitives.css": ":focus-visible",
	}
	for _, p := range []string{"/tokens.css", "/primitives.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil)) // no cookie
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200 — the login page cannot style itself", p, rec.Code)
		}
		// A strict browser refuses to apply a stylesheet served with the
		// wrong Content-Type, so a wrong MIME type breaks the login page
		// just as much as a wrong status code would.
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Fatalf("%s: Content-Type=%q, want text/css", p, ct)
		}
		if want := sentinels[p]; !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s: body missing %q — wrong file served", p, want)
		}
		// The palette must not cache indefinitely — see TestStaticNoCacheOnAppFiles.
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control=%q, want no-cache", p, cc)
		}
	}
	// app.css is not part of the login page and must stay behind the guard.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/app.css", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("/app.css served without a cookie; it should stay protected")
	}
}

// metaGet builds a Server with the given fields, issues a session cookie, and
// returns the decoded /api/meta body.
func metaGet(t *testing.T, s *Server) map[string]any {
	t.Helper()
	ck := issueCookie(t, s)
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.AddCookie(ck)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestHandleMeta(t *testing.T) {
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, "", nil, newFakeStore(), func([]config.AgentConfig) {},
	)
	s := &Server{AllowedHosts: []string{"example.com"}, EnvLabel: "PROD", EnvColor: "#b45309", ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	got := metaGet(t, s)
	if got["env_label"] != "PROD" || got["env_color"] != "#b45309" {
		t.Fatalf("got %v", got)
	}
	if got["shell_profile"] != "zsh" {
		t.Fatalf("shell_profile = %q, want zsh", got["shell_profile"])
	}
}

// "replies" is always present and fails closed: false unless the Server was
// wired with the config kill-switch on.
func TestHandleMetaReplies(t *testing.T) {
	for _, on := range []bool{false, true} {
		pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
		got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: pm, RepliesEnabled: on})
		v, present := got["replies"]
		if !present || v != on {
			t.Errorf("RepliesEnabled=%v: replies = %v (present=%v), want %v", on, v, present, on)
		}
	}
}

func TestHandleMetaRateLimits(t *testing.T) {
	f := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(f, []byte(`{"five_hour":{"used_percentage":80,"resets_at":1785654600},"seven_day":{"used_percentage":42,"resets_at":1786107600}}`), 0o644)
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{AllowedHosts: []string{"example.com"}, Profiles: pm, RateLimitsFile: f}
	ck := issueCookie(t, s)
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/meta", nil)
	req.AddCookie(ck)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		RateLimits map[string]RateLimits `json:"rate_limits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if def := got.RateLimits["default"]; def.FiveHour == nil || def.FiveHour.UsedPercentage != 80 {
		t.Fatalf("rate_limits missing/wrong: %+v", got.RateLimits)
	}
}

func TestHandleMetaNoRateLimitsFile(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	s := &Server{AllowedHosts: []string{"example.com"}, Profiles: pm, RateLimitsFile: filepath.Join(t.TempDir(), "absent.json")}
	got := metaGet(t, s)
	if _, present := got["rate_limits"]; present {
		t.Fatal("rate_limits must be omitted when the file is absent")
	}
}

// A stored profile overrides a same-named seed, so the quick-shell name alone is
// not a guarantee; if it no longer resolves to the detected shell binary, offer
// no quick shell rather than launching whatever took the name.
func TestHandleMetaShellProfileShadowed(t *testing.T) {
	fs := newFakeStore()
	if err := fs.Save(config.AgentConfig{Name: "zsh", Bin: "python3"}); err != nil {
		t.Fatal(err)
	}
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, "", nil, fs, func([]config.AgentConfig) {},
	)
	s := &Server{AllowedHosts: []string{"example.com"}, ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	if got := metaGet(t, s); got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty (shadowed by a profile running python3)", got["shell_profile"])
	}
}

// Without the pieces needed to verify the profile, the answer must be "no quick
// shell" — never an unverified name.
func TestHandleMetaFailsClosedWithoutProfiles(t *testing.T) {
	s := &Server{AllowedHosts: []string{"example.com"}, ShellProfile: "zsh"} // no Profiles, no ShellBin
	if got := metaGet(t, s); got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty (unverifiable)", got["shell_profile"])
	}
}

// The same check must not disable a legitimate quick shell.
func TestHandleMetaShellProfileIntact(t *testing.T) {
	pm := NewProfileManager(
		[]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}},
		nil, "", nil, newFakeStore(), func([]config.AgentConfig) {},
	)
	s := &Server{AllowedHosts: []string{"example.com"}, ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm}
	if got := metaGet(t, s); got["shell_profile"] != "zsh" {
		t.Fatalf("shell_profile = %q, want zsh", got["shell_profile"])
	}
}

// The key must always be present: the frontend treats an empty shell_profile as
// "no quick shell available" and hides the +sh button, so a missing key would be
// ambiguous.
func TestHandleMetaNoShellProfile(t *testing.T) {
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}})
	if _, ok := got["shell_profile"]; !ok {
		t.Fatal("shell_profile key must always be present (empty = no quick shell)")
	}
	if got["shell_profile"] != "" {
		t.Fatalf("shell_profile = %q, want empty", got["shell_profile"])
	}
}

// Cookie-authenticated writes require same-origin (a session cookie is
// auto-attached cross-site). Bearer-authenticated writes are exempt (no
// auto-attach). Reads are never blocked.
func TestUnsafeMethodsRequireSameOrigin(t *testing.T) {
	ts, _ := auth.OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	tok, _, _ := ts.Create("cli")
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(), Tokens: ts,
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
	ck := issueCookie(t, s)
	h := s.Handler()

	cookieReq := func(method, path string, hdr [2]string) int {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(ck)
		if hdr[0] != "" {
			r.Header.Set(hdr[0], hdr[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	for _, m := range []string{"POST", "DELETE", "PATCH", "PUT"} {
		if got := cookieReq(m, "/api/sessions", [2]string{"Sec-Fetch-Site", "cross-site"}); got != http.StatusForbidden {
			t.Errorf("%s cross-site cookie: got %d, want 403", m, got)
		}
		if got := cookieReq(m, "/api/sessions", [2]string{"Origin", "http://evil.example"}); got != http.StatusForbidden {
			t.Errorf("%s foreign-origin cookie: got %d, want 403", m, got)
		}
	}

	// A bearer client may POST cross-site — no CSRF vector.
	br := httptest.NewRequest("POST", "/api/sessions", nil)
	br.Header.Set("Authorization", "Bearer "+tok)
	br.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, br)
	if rec.Code == http.StatusForbidden {
		t.Error("bearer cross-site POST wrongly blocked by CSRF guard")
	}

	// Reads are not blocked, and the page's own same-origin requests work.
	if got := cookieReq("GET", "/api/sessions", [2]string{"Sec-Fetch-Site", "cross-site"}); got == http.StatusForbidden {
		t.Error("cross-site GET should not be blocked by the CSRF guard")
	}
	if got := cookieReq("POST", "/api/sessions", [2]string{"Sec-Fetch-Site", "same-origin"}); got == http.StatusForbidden {
		t.Error("same-origin POST was blocked")
	}
	if got := cookieReq("POST", "/api/sessions", [2]string{"", ""}); got == http.StatusForbidden {
		t.Error("POST with no fetch-metadata headers (older browser) was blocked")
	}
}

// The page links /favicon.svg (and swaps to /favicon-needs.svg while an agent
// needs you); both must be served, as SVG, to a logged-in page.
func TestFaviconsAreServed(t *testing.T) {
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
	ck := issueCookie(t, s)
	h := s.Handler()
	for _, p := range []string{"/favicon.svg", "/favicon-needs.svg"} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(ck)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d, want 200", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
			t.Fatalf("%s: Content-Type=%q, want image/svg+xml", p, ct)
		}
	}
}

// The profile editor's AWS connections picker is fed by /api/meta. It may know
// a connection's name, label, account, region and whether it can write; never
// the source profile (an SSO profile name is the host's own).
func TestMetaListsAWSConnectionsWithoutSourceProfile(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	pm.SetAWSConnections([]config.AWSConnection{
		{Name: "sbx-a", Description: "sandbox a", AWSBinding: config.AWSBinding{
			SourceProfile: "corp-sso-secret", RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent", Region: "ap-southeast-1", Label: "Sandbox A",
			Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::123456789012:role/agentbox-agent-write"}}},
		{Name: "prd-b", AWSBinding: config.AWSBinding{
			SourceProfile: "other-sso-secret", RoleARN: "arn:aws:iam::210987654321:role/agentbox-agent", Region: "us-east-1", Prod: true}},
	})
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: pm})
	list, _ := got["aws_connections"].([]any)
	if len(list) != 2 {
		t.Fatalf("aws_connections = %v, want 2 entries", got["aws_connections"])
	}
	a, _ := list[0].(map[string]any)
	if a["name"] != "sbx-a" || a["label"] != "Sandbox A" || a["account_id"] != "123456789012" || a["region"] != "ap-southeast-1" ||
		a["description"] != "sandbox a" || a["write_capable"] != true || a["prod"] != false {
		t.Fatalf("first entry = %v", a)
	}
	b, _ := list[1].(map[string]any)
	if b["name"] != "prd-b" || b["account_id"] != "210987654321" || b["prod"] != true || b["write_capable"] != false {
		t.Fatalf("second entry = %v", b)
	}
	for _, e := range []map[string]any{a, b} {
		if _, ok := e["source_profile"]; ok {
			t.Errorf("entry %v carries source_profile", e)
		}
	}
	raw, _ := json.Marshal(got)
	for _, banned := range []string{"source_profile", "corp-sso-secret", "other-sso-secret", "role_arn", "agentbox-agent", "elevate"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the bootstrap payload carries %q: %s", banned, raw)
		}
	}

	// none defined: [] on the wire, never null
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})})
	if l, ok := got["aws_connections"].([]any); !ok || len(l) != 0 {
		t.Fatalf("aws_connections with none defined = %v, want []", got["aws_connections"])
	}
}
