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

	"github.com/culiops/culibox/internal/analytics"
)

// analyticsTranscript is one session's transcript: a prompt, then one priced
// assistant message per model, all on 2026-08-05.
func analyticsTranscript(sid string, models ...string) string {
	var b strings.Builder
	b.WriteString(`{"type":"user","sessionId":"` + sid + `","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"hello"}}` + "\n")
	for i, m := range models {
		n := string(rune('1' + i))
		b.WriteString(`{"type":"assistant","sessionId":"` + sid + `","timestamp":"2026-08-05T10:0` + n + `:00Z","message":{"id":"msg_` + sid + n +
			`","role":"assistant","model":"` + m +
			`","usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n")
	}
	return b.String()
}

// analyticsServer is a routed Server over a real analytics service that has
// ingested two sessions ("abc" on one model, "def" on two), plus a cookie.
func analyticsServer(t *testing.T) (*Server, http.Handler, *http.Cookie) {
	t.Helper()
	projects := filepath.Join(t.TempDir(), "projects")
	dir := filepath.Join(projects, "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for sid, models := range map[string][]string{
		"abc": {"claude-opus-4-8"},
		"def": {"claude-opus-4-8", "claude-sonnet-4-6"},
	} {
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(analyticsTranscript(sid, models...)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := analytics.New(filepath.Join(t.TempDir(), "a.db"), []string{projects}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, err := svc.Ingest(); err != nil {
		t.Fatal(err)
	}
	s, _ := routedServer(t)
	s.Analytics = svc
	return s, s.Handler(), issueCookie(t, s)
}

func analyticsReq(h http.Handler, method, target string, ck *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "example.com"
	if ck != nil {
		r.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const aWin = "?from=2026-08-01&to=2026-08-31"

func analyticsJSON(t *testing.T, h http.Handler, ck *http.Cookie, target string) map[string]json.RawMessage {
	t.Helper()
	w := analyticsReq(h, "GET", target, ck)
	if w.Code != 200 {
		t.Fatalf("GET %s = %d: %s", target, w.Code, w.Body)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s: %v: %s", target, err, w.Body)
	}
	return m
}

func anWantKeys(t *testing.T, m map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("response has no %q: %v", k, m)
		}
	}
}

// anWantReadOnly checks the route refuses every method but GET, and a caller with
// no credential.
func anWantReadOnly(t *testing.T, h http.Handler, ck *http.Cookie, path string) {
	t.Helper()
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		if w := analyticsReq(h, method, path+aWin, ck); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", method, path, w.Code)
		}
	}
	if w := analyticsReq(h, "GET", path+aWin, nil); w.Code == http.StatusOK {
		t.Errorf("GET %s with no credential = 200, want it refused", path)
	}
}

func TestAnalyticsWorkspacesShape(t *testing.T) {
	_, h, ck := analyticsServer(t)
	m := analyticsJSON(t, h, ck, "/api/analytics/workspaces"+aWin)
	anWantKeys(t, m, "from", "to", "workspaces")
	var rows []map[string]any
	if err := json.Unmarshal(m["workspaces"], &rows); err != nil || len(rows) == 0 {
		t.Fatalf("workspaces = %s (%v), want a non-empty list", m["workspaces"], err)
	}
	for _, k := range []string{"workspace", "name", "costUsd", "sessions"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("workspace row has no %q: %v", k, rows[0])
		}
	}
	anWantReadOnly(t, h, ck, "/api/analytics/workspaces")
}

func TestAnalyticsDriversShape(t *testing.T) {
	_, h, ck := analyticsServer(t)
	m := analyticsJSON(t, h, ck, "/api/analytics/drivers"+aWin)
	anWantKeys(t, m, "from", "to", "total", "coveredTotal", "agent", "skill", "plugin", "mcp")
	for _, k := range []string{"agent", "skill", "plugin", "mcp"} {
		if string(m[k]) == "null" {
			t.Errorf("%s is null, want a list", k)
		}
	}
	anWantReadOnly(t, h, ck, "/api/analytics/drivers")
}

func TestAnalyticsWhereOptions(t *testing.T) {
	_, h, ck := analyticsServer(t)
	m := analyticsJSON(t, h, ck, "/api/analytics/where"+aWin)
	anWantKeys(t, m, "from", "to", "options")
	var opts []map[string]any
	if err := json.Unmarshal(m["options"], &opts); err != nil || len(opts) == 0 {
		t.Fatalf("options = %s (%v), want a non-empty list", m["options"], err)
	}
	anWantReadOnly(t, h, ck, "/api/analytics/where")
}

func TestAnalyticsWhereInvalidIsAll(t *testing.T) {
	_, h, ck := analyticsServer(t)
	long := "box:" + strings.Repeat("x", 300)
	for _, path := range []string{"summary", "models", "projects", "sessions", "session-costs", "tools", "workspaces", "drivers"} {
		base := analyticsReq(h, "GET", "/api/analytics/"+path+aWin, ck)
		if base.Code != 200 {
			t.Fatalf("%s without where = %d", path, base.Code)
		}
		for _, bad := range []string{"%27%3Bdrop", "HOST", long} {
			w := analyticsReq(h, "GET", "/api/analytics/"+path+aWin+"&where="+bad, ck)
			if w.Code != 200 || w.Body.String() != base.Body.String() {
				t.Errorf("%s where=%.20s: code %d, body differs from no where", path, bad, w.Code)
			}
		}
	}
}

type stubIndex map[string]string

func (s stubIndex) AgentSessionIndex() map[string]string { return s }

func TestSessionCostsLiveId(t *testing.T) {
	s, h, ck := analyticsServer(t)
	read := func() map[string]json.RawMessage {
		return analyticsJSON(t, h, ck, "/api/analytics/session-costs"+aWin)
	}
	liveIDs := func(m map[string]json.RawMessage) map[string]string {
		var rows []struct {
			SessionID string  `json:"sessionId"`
			LiveID    *string `json:"liveId"`
		}
		if err := json.Unmarshal(m["sessions"], &rows); err != nil || len(rows) != 2 {
			t.Fatalf("sessions = %s (%v), want 2 rows", m["sessions"], err)
		}
		out := map[string]string{}
		for _, r := range rows {
			if r.LiveID == nil {
				t.Fatalf("row %s has no liveId", r.SessionID)
			}
			out[r.SessionID] = *r.LiveID
		}
		return out
	}

	// No index wired: every liveId is "" and the field is still there.
	if got := liveIDs(read()); got["abc"] != "" || got["def"] != "" {
		t.Errorf("no index: liveIds = %v, want all empty", got)
	}

	s.AgentIndex = stubIndex{"abc": "s1"}
	m := read()
	if got := liveIDs(m); got["abc"] != "s1" || got["def"] != "" {
		t.Errorf("liveIds = %v, want abc=s1 def empty", got)
	}
	anWantKeys(t, m, "modelCosts")
	var mc []map[string]any
	if err := json.Unmarshal(m["modelCosts"], &mc); err != nil || len(mc) != 3 {
		t.Fatalf("modelCosts = %s (%v), want 3 (session, model) rows", m["modelCosts"], err)
	}
	if _, ok := mc[0]["costUsd"]; !ok {
		t.Errorf("modelCosts row has no costUsd: %v", mc[0])
	}

	// An empty window keeps both lists as [].
	e := analyticsJSON(t, h, ck, "/api/analytics/session-costs?from=2020-01-01&to=2020-01-02")
	if string(e["sessions"]) != "[]" || string(e["modelCosts"]) != "[]" {
		t.Errorf("empty window: sessions=%s modelCosts=%s, want []", e["sessions"], e["modelCosts"])
	}
}
