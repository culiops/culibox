package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guardWith(allowed ...string) (http.Handler, *[]string) {
	var logs []string
	g := newHostGuard(allowed, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	return g.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })), &logs
}

func hit(h http.Handler, method, path, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHostGuard_AllowsListedHosts(t *testing.T) {
	h, _ := guardWith("127.0.0.1:8899", "localhost:8899", "[::1]:8899", "agent-box.culiops.dev")
	// Review focus #2: case-insensitive and IPv6 alias.
	for _, host := range []string{"127.0.0.1:8899", "LOCALHOST:8899", "[::1]:8899", "Agent-Box.Culiops.Dev"} {
		if w := hit(h, "GET", "/", host); w.Code != 200 {
			t.Errorf("Host %q: %d, want 200", host, w.Code)
		}
	}
}

// The DNS-rebinding case: the attacker's name resolves to 127.0.0.1, so the
// browser sends Host: evil.example:8899 with a matching Origin.
func TestHostGuard_RefusesRebinding(t *testing.T) {
	h, _ := guardWith("127.0.0.1:8899", "localhost:8899")
	for _, c := range []struct{ method, path, host string }{
		{"POST", "/login", "evil.example:8899"},
		{"GET", "/ws/claude-1", "evil.example:8899"},
		{"GET", "/api/sessions", "evil.example:8899"},
		{"GET", "/app.js", "evil.example:8899"},
		{"GET", "/", "localhost.:8899"}, // trailing dot: refused, fail closed
		{"GET", "/", "127.0.0.1:8900"},  // wrong port
		{"GET", "/", ""},                // HTTP/1.0 without Host
	} {
		w := hit(h, c.method, c.path, c.host)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s Host=%q: %d, want 403", c.method, c.path, c.host, w.Code)
		}
		if strings.Contains(w.Body.String(), "evil") {
			t.Errorf("403 body reflects the Host header: %q", w.Body.String())
		}
	}
}

func TestHostGuard_EmptyAllowlistRefusesAll(t *testing.T) {
	h, _ := guardWith()
	if w := hit(h, "GET", "/", "127.0.0.1:8899"); w.Code != http.StatusForbidden {
		t.Errorf("an unconfigured guard must fail closed, got %d", w.Code)
	}
}

func TestHostGuard_LogDedupAndCap(t *testing.T) {
	h, logs := guardWith("ok:1")
	hit(h, "GET", "/", "a:1")
	hit(h, "GET", "/", "a:1")
	if len(*logs) != 1 {
		t.Fatalf("same host logged %d times, want 1: %q", len(*logs), *logs)
	}
	for i := 0; i < maxLoggedHosts+10; i++ {
		hit(h, "GET", "/", fmt.Sprintf("h%d:1", i))
	}
	if n := len(*logs); n != maxLoggedHosts+1 {
		t.Errorf("logged %d lines, want %d (cap + one suppressed line)", n, maxLoggedHosts+1)
	}
	if !strings.Contains((*logs)[len(*logs)-1], "suppressed") {
		t.Errorf("last line should announce suppression: %q", (*logs)[len(*logs)-1])
	}
}

func TestServerHandler_GuardsLogin(t *testing.T) {
	s, _ := testServerWithAuth(t)
	h := s.Handler()
	if w := hit(h, "GET", "/login", "evil.example:8899"); w.Code != http.StatusForbidden {
		t.Errorf("GET /login via a foreign Host: %d, want 403", w.Code)
	}
	if w := hit(h, "GET", "/login", "example.com"); w.Code != 200 {
		t.Errorf("GET /login via an allowed Host: %d, want 200", w.Code)
	}
}
