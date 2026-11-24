package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/linktrust"
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
		LinkTrust: linktrust.NewService(t.TempDir(), nil, nil),
		Profiles:  NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})}
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
		{"POST", "/api/fs/newfile", http.StatusBadRequest},
		{"GET", "/api/fs/newfile", http.StatusMethodNotAllowed},
		{"POST", "/api/fs/rename", http.StatusBadRequest},
		{"POST", "/api/fs/delete", http.StatusBadRequest},
		{"GET", "/api/fs/delete", http.StatusMethodNotAllowed},
		{"POST", "/api/bugreport", http.StatusServiceUnavailable}, // no builder wired
		{"GET", "/api/bugreport", http.StatusMethodNotAllowed},
		{"GET", "/api/link-trust", http.StatusOK},
		{"POST", "/api/link-trust", http.StatusForbidden}, // no same-origin header: browserOnly
		{"PUT", "/api/link-trust", http.StatusForbidden},
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
	// desktop.css's @font-face for the dock's code font (P5) names these unversioned paths: served as fonts, revalidated by ETag.
	for _, f := range []string{"latin", "latin-ext", "vietnamese"} {
		rec := get("/vendor/jetbrains-mono/jetbrains-mono-" + f + "-wght-normal.woff2")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/woff2" || rec.Header().Get("ETag") == "" {
			t.Fatalf("%s: code=%d Content-Type=%q ETag=%q", f, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("ETag"))
		}
	}
	if cc := get("/xterm/xterm.js").Header().Get("Cache-Control"); cc == "no-cache" {
		t.Fatal("/xterm/xterm.js should stay cacheable, got no-cache")
	}
	// The page names its build: its vendor and xterm URLs carry /v/<id>/, which is cached for a year.
	index := get("/").Body.String()
	m := regexp.MustCompile(`<meta name="culibox-asset-base" content="(/v/[0-9a-f]{16})">`).FindStringSubmatch(index)
	if m == nil {
		t.Fatal("index.html carries no asset base")
	}
	if strings.Contains(index, `="/vendor/`) || strings.Contains(index, `="/xterm/`) {
		t.Fatal("index.html still names an unversioned vendor or xterm URL")
	}
	if !strings.Contains(index, `<script src="`+m[1]+`/vendor/marked.min.js"></script>`) || !strings.Contains(index, `<script src="/app.js"></script>`) {
		t.Fatal("index.html: vendor scripts versioned, app scripts as they are")
	}
	for _, p := range []string{m[1] + "/vendor/marked.min.js", m[1] + "/xterm/xterm.js", m[1] + "/vendor/monaco/vs/loader.js"} {
		rec := get(p)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
			t.Fatalf("%s: code=%d Cache-Control=%q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	if rec := get("/v/0123456789abcdef/vendor/marked.min.js"); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("another build's URL: Cache-Control=%q, want no-cache", rec.Header().Get("Cache-Control"))
	}
	if rec := get("/v/0123456789abcdef/app.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("/v/ serves only vendor and xterm: code=%d", rec.Code)
	}
	// An app file is gzipped for a client that takes it, and revalidates with a 304.
	req := httptest.NewRequest("GET", "/app.js", nil)
	req.AddCookie(ck)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" {
		t.Fatalf("/app.js: Content-Encoding=%q ETag=%q", rec.Header().Get("Content-Encoding"), rec.Header().Get("ETag"))
	}
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("/app.js revalidation: code=%d, want 304", rec.Code)
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
	// desktop.css is linked by login.html (desktop-only login styling), and its
	// @font-face names the Inter file, so both must be public too.
	for p, ct := range map[string]string{"/desktop.css": "text/css", "/vendor/inter/InterVariable.woff2": "font/woff2"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200 — the login page cannot use the desktop styling", p, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, ct) {
			t.Fatalf("%s: Content-Type=%q, want %s", p, got, ct)
		}
		// The CSS must revalidate; the font has no ETag (embed.FS), so it is
		// cached for a day instead of re-downloaded on every login view.
		wantCC := "no-cache"
		if ct == "font/woff2" {
			wantCC = "public, max-age=86400"
		}
		if cc := rec.Header().Get("Cache-Control"); cc != wantCC {
			t.Fatalf("%s: Cache-Control=%q, want %q", p, cc, wantCC)
		}
	}
	// The public set is exact files, not a prefix or subtree: nothing else under
	// vendor/inter, no directory listing and no trailing-slash alias may be
	// reachable without a cookie.
	// The code font (P5) is not the login page's: desktop.css names it, but only the dock uses it, so it stays behind the guard.
	for _, p := range []string{"/vendor/inter/LICENSE.txt", "/vendor/inter/", "/vendor/", "/desktop.css/", "/vendor/jetbrains-mono/jetbrains-mono-latin-wght-normal.woff2"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("%s served without a cookie; only the exact public files may be", p)
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
	s := &Server{AllowedHosts: []string{"example.com"}, EnvLabel: "PROD", EnvColor: "#b45309", ShellProfile: "zsh", ShellBin: "/usr/bin/zsh", Profiles: pm, Version: "v0.1.0"}
	got := metaGet(t, s)
	if got["version"] != "v0.1.0" {
		t.Fatalf("version: got %v", got["version"])
	}
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

// box_unavailable_reason says why boxes are off: present only when there is no
// box runtime AND the layer failed to build; absent when boxes work.
func TestHandleMetaBoxUnavailableReason(t *testing.T) {
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxUnavailableReason: "Docker is not installed"})
	if v, ok := got["box_unavailable_reason"]; !ok || v != "Docker is not installed" {
		t.Errorf("no runtime: box_unavailable_reason = %#v (present %v)", v, ok)
	}
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxRuntime: fixedRuntime("runc"), BoxUnavailableReason: "Docker is not installed"})
	if v, ok := got["box_unavailable_reason"]; ok {
		t.Errorf("a runtime is present, yet box_unavailable_reason = %#v", v)
	}
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})
	if v, ok := got["box_unavailable_reason"]; ok {
		t.Errorf("no failed layer, yet box_unavailable_reason = %#v", v)
	}
}

// #154: box_daemon_reason is the sibling of box_unavailable_reason for a box layer
// that exists but whose docker daemon cannot be used right now. box_runtime stays
// as it is (a transient daemon outage must not flip the page to "no boxes", whose
// new profiles open on the host tier). Read per request, so it follows the daemon.
func TestHandleMetaBoxDaemonReason(t *testing.T) {
	reason := "Docker is not running"
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxRuntime: fixedRuntime("runc"), BoxDaemonReason: func() string { return reason }}
	got := metaGet(t, s)
	if v := got["box_daemon_reason"]; v != "Docker is not running" {
		t.Errorf("box_daemon_reason = %#v", v)
	}
	if got["box_runtime"] != "runc" {
		t.Errorf("box_runtime = %#v, want it untouched", got["box_runtime"])
	}
	reason = ""
	if v, ok := metaGet(t, s)["box_daemon_reason"]; ok {
		t.Errorf("the daemon answers again, yet box_daemon_reason = %#v", v)
	}
	// No layer: box_unavailable_reason explains that, and the daemon is not asked.
	asked := false
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxDaemonReason: func() string { asked = true; return "x" }})
	if _, ok := got["box_daemon_reason"]; ok || asked {
		t.Errorf("no layer, yet box_daemon_reason is published (%v) or asked (%v)", ok, asked)
	}
	if _, ok := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxRuntime: fixedRuntime("runc")})["box_daemon_reason"]; ok {
		t.Error("no probe wired, yet box_daemon_reason is published")
	}
}

func fixedRuntime(rt string) func() string { return func() string { return rt } }

// #334: box_runtime is read on every request, not cached at boot: the runtime a
// boot with Docker down could not read is detected later, and the badge must
// show the detected value, not the boot placeholder.
func TestHandleMetaBoxRuntimeIsReadPerRequest(t *testing.T) {
	rt := "runc"
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{}, BoxRuntime: func() string { return rt }}
	if got := metaGet(t, s)["box_runtime"]; got != "runc" {
		t.Fatalf("box_runtime = %#v, want runc", got)
	}
	rt = "sysbox-runc"
	if got := metaGet(t, s)["box_runtime"]; got != "sysbox-runc" {
		t.Fatalf("after re-detection box_runtime = %#v, want sysbox-runc", got)
	}
	if got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})["box_runtime"]; got != "" {
		t.Fatalf("no layer: box_runtime = %#v, want \"\"", got)
	}
}

// bundle_imports tells the page whether /api/bundle/imports exists at all, so it
// does not request a route that answers 404 on every load.
func TestHandleMetaBundleImports(t *testing.T) {
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})
	if v, ok := got["bundle_imports"]; !ok || v != false {
		t.Errorf("nil Imports: bundle_imports = %#v (present %v), want false", v, ok)
	}
	got = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, Imports: func() bundle.ImportSet { return bundle.ImportSet{} }})
	if v, ok := got["bundle_imports"]; !ok || v != true {
		t.Errorf("Imports set: bundle_imports = %#v (present %v), want true", v, ok)
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

// TestMetaSecretSources: /api/meta names the configured secret sources by type
// (and the file path), in order, and nothing else; the key is absent when none
// are configured.
func TestMetaSecretSources(t *testing.T) {
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, SecretSources: []config.SecretSource{
		{Type: "store"}, {Type: "file", Path: "/etc/cb/s.env"}, {Type: "env"},
	}})
	var want any
	if err := json.Unmarshal([]byte(`[{"type":"store"},{"type":"file","path":"/etc/cb/s.env"},{"type":"env"}]`), &want); err != nil {
		t.Fatalf("want: %v", err)
	}
	if !reflect.DeepEqual(got["secret_sources"], want) {
		t.Fatalf("secret_sources = %v, want %v", got["secret_sources"], want)
	}
	if _, ok := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})["secret_sources"]; ok {
		t.Fatal("secret_sources present with no sources configured")
	}
}
