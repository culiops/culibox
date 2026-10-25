package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

func bugServer(f func(ctx context.Context) (string, string)) *Server {
	return &Server{BugReport: f}
}

func TestBugReportCookieOnly(t *testing.T) {
	called := false
	s := bugServer(func(context.Context) (string, string) { called = true; return "m", "u" })
	r := httptest.NewRequest("GET", "/api/bugreport", nil)
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

func TestBugReportGetOnly(t *testing.T) {
	s := bugServer(func(context.Context) (string, string) { return "m", "u" })
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(httptest.NewRequest("POST", "/api/bugreport", nil)))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code=%d want 405", w.Code)
	}
}

func TestBugReportReturnsJSON(t *testing.T) {
	s := bugServer(func(context.Context) (string, string) { return "# report", "https://x/new" })
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(httptest.NewRequest("GET", "/api/bugreport", nil)))
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

func TestBugReportOneInFlight(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s := bugServer(func(context.Context) (string, string) {
		close(started)
		<-release
		return "m", "u"
	})
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handleBugReport(w, asCookie(httptest.NewRequest("GET", "/api/bugreport", nil)))
		done <- w.Code
	}()
	<-started
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(httptest.NewRequest("GET", "/api/bugreport", nil)))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request code=%d want 429", w.Code)
	}
	if !strings.Contains(w.Body.String(), "a report is already being built") {
		t.Errorf("body=%q", w.Body.String())
	}
	close(release)
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("first request code=%d want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request never finished")
	}
}

func TestBugReportSlotFreedAfterReturn(t *testing.T) {
	s := bugServer(func(context.Context) (string, string) { return "m", "u" })
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.handleBugReport(w, asCookie(httptest.NewRequest("GET", "/api/bugreport", nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d code=%d want 200", i, w.Code)
		}
	}
}

func TestBugReportNilIs503(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleBugReport(w, asCookie(httptest.NewRequest("GET", "/api/bugreport", nil)))
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
