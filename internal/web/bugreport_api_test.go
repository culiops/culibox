package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func bugServer(f func(ctx context.Context) (string, string, error)) *Server {
	return &Server{BugReport: f}
}

func bugPost() *http.Request { return httptest.NewRequest("POST", "/api/bugreport", nil) }

func TestBugReportCookieOnly(t *testing.T) {
	called := false
	s := bugServer(func(context.Context) (string, string, error) { called = true; return "m", "u", nil })
	r := bugPost()
	r = r.WithContext(context.WithValue(r.Context(), credKindKey, credBearer))
	w := httptest.NewRecorder()
	s.handleBugReport(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bearer code=%d want 403", w.Code)
	}
	if called {
		t.Fatal("the bundle was built for a bearer token")
	}
}

// The build is side-effecting, so only POST is served: a cross-site GET
// navigation would carry the Lax session cookie past csrfGuard.
func TestBugReportPostOnly(t *testing.T) {
	called := false
	s := bugServer(func(context.Context) (string, string, error) { called = true; return "m", "u", nil })
	for _, m := range []string{"GET", "HEAD", "PUT", "DELETE", "PATCH"} {
		w := httptest.NewRecorder()
		s.handleBugReport(w, asCookie(httptest.NewRequest(m, "/api/bugreport", nil)))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s code=%d want 405", m, w.Code)
		}
	}
	if called {
		t.Fatal("the bundle was built for a non-POST request")
	}
}

// Through the full handler chain: a cookie POST from another origin is refused
// by csrfGuard before the builder runs; a same-origin one is served; GET is 405.
func TestBugReportCrossSiteRefusedThroughHandlerChain(t *testing.T) {
	called := 0
	s := &Server{AllowedHosts: []string{"example.com"}, API: &API{Sessions: &mockSvc{}}, Terminal: http.NotFoundHandler(),
		Profiles:  NewProfileManager(nil, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {}),
		BugReport: func(context.Context) (string, string, error) { called++; return "m", "u", nil }}
	ck := issueCookie(t, s)
	h := s.Handler()
	do := func(method string, hdr [2]string) int {
		r := httptest.NewRequest(method, "/api/bugreport", nil)
		r.AddCookie(ck)
		if hdr[0] != "" {
			r.Header.Set(hdr[0], hdr[1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := do("POST", [2]string{"Sec-Fetch-Site", "cross-site"}); got != http.StatusForbidden {
		t.Errorf("cross-site cookie POST: got %d, want 403", got)
	}
	if got := do("POST", [2]string{"Origin", "http://evil.example"}); got != http.StatusForbidden {
		t.Errorf("foreign-origin cookie POST: got %d, want 403", got)
	}
	if called != 0 {
		t.Fatalf("builder ran %d times for refused requests", called)
	}
	if got := do("GET", [2]string{"Sec-Fetch-Site", "cross-site"}); got != http.StatusMethodNotAllowed {
		t.Errorf("cross-site GET: got %d, want 405", got)
	}
	if got := do("POST", [2]string{"Sec-Fetch-Site", "same-origin"}); got != http.StatusOK {
		t.Errorf("same-origin POST: got %d, want 200", got)
	}
	if called != 1 {
		t.Fatalf("builder ran %d times, want 1", called)
	}
}

func TestBugReportReturnsJSON(t *testing.T) {
	s := bugServer(func(context.Context) (string, string, error) { return "# report", "https://x/new", nil })
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(bugPost()))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control=%q want no-store", cc)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["markdown"] != "# report" || got["issue_url"] != "https://x/new" {
		t.Fatalf("got %v", got)
	}
}

// The builder owns the in-flight state: ErrBugReportBusy is a 429, whether the
// earlier build is another live request or one whose request was cancelled.
func TestBugReportBusyIs429(t *testing.T) {
	s := bugServer(func(context.Context) (string, string, error) { return "", "", ErrBugReportBusy })
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(bugPost()))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429", w.Code)
	}
	if !strings.Contains(w.Body.String(), "a report is already being built") {
		t.Errorf("body=%q", w.Body.String())
	}
}

func TestBugReportBuilderErrorIs500(t *testing.T) {
	s := bugServer(func(context.Context) (string, string, error) { return "", "", errors.New("disk on fire") })
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(bugPost()))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "disk on fire") {
		t.Errorf("internal error text leaked: %q", w.Body.String())
	}
}

func TestBugReportNilIs503(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(bugPost()))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", w.Code)
	}
}

func TestMetaHasCommit(t *testing.T) {
	pm := NewProfileManager([]config.AgentConfig{{Name: "zsh", Bin: "/usr/bin/zsh"}}, nil, "", nil, newFakeStore(), func([]config.AgentConfig) {})
	got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, Profiles: pm, Commit: "abc1234"})
	if got["commit"] != "abc1234" {
		t.Fatalf("commit = %v, want abc1234", got["commit"])
	}
}
