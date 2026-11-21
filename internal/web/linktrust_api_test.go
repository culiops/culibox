package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/linktrust"
)

// linkTrustRig is a Server over a real linktrust.Service in a temp dir.
type linkTrustRig struct {
	s   *Server
	dir string
	rec *fakeCredRec
}

func newLinkTrustRig(t *testing.T) *linkTrustRig {
	t.Helper()
	dir := t.TempDir()
	rec := &fakeCredRec{}
	svc := linktrust.NewService(dir, LinkTrustAudit(rec), nil)
	return &linkTrustRig{s: &Server{LinkTrust: svc}, dir: dir, rec: rec}
}

func (g *linkTrustRig) call(method, body string) *httptest.ResponseRecorder {
	return do(g.s.handleLinkTrust, storeReq(method, "/api/link-trust", body, "http://"+host, credCookie))
}

type ltView struct {
	Hosts []struct {
		Host       string `json:"host"`
		Subdomains bool   `json:"subdomains"`
		Added      string `json:"added"`
		Actor      string `json:"actor"`
	} `json:"hosts"`
	Corrupt bool `json:"corrupt"`
}

func decodeLT(t *testing.T, w *httptest.ResponseRecorder) ltView {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var v ltView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLinkTrustAPIListAddRemove(t *testing.T) {
	g := newLinkTrustRig(t)
	v := decodeLT(t, g.call("GET", ""))
	if v.Hosts == nil || len(v.Hosts) != 0 || v.Corrupt {
		t.Fatalf("initial = %+v (hosts nil=%v), want [] and false", v, v.Hosts == nil)
	}
	v = decodeLT(t, g.call("POST", `{"host":"github.com","subdomains":true}`))
	if len(v.Hosts) != 1 || v.Hosts[0].Host != "github.com" || !v.Hosts[0].Subdomains || v.Hosts[0].Added == "" {
		t.Fatalf("after add = %+v", v.Hosts)
	}
	if v.Hosts[0].Actor != "" {
		t.Errorf("the actor is not part of the reply: %q", v.Hosts[0].Actor)
	}
	// No "subdomains" member means false.
	v = decodeLT(t, g.call("POST", `{"host":"docs.example.com"}`))
	if len(v.Hosts) != 2 || v.Hosts[1].Host != "docs.example.com" || v.Hosts[1].Subdomains {
		t.Fatalf("after second add = %+v", v.Hosts)
	}
	// Same host, other flag: an update in place, not a second row.
	v = decodeLT(t, g.call("POST", `{"host":"docs.example.com","subdomains":true}`))
	if len(v.Hosts) != 2 || !v.Hosts[1].Subdomains {
		t.Fatalf("after update = %+v", v.Hosts)
	}
	if v = decodeLT(t, g.call("GET", "")); len(v.Hosts) != 2 {
		t.Fatalf("a fresh GET lost a host: %+v", v.Hosts)
	}
	v = decodeLT(t, g.call("DELETE", `{"host":"github.com"}`))
	if len(v.Hosts) != 1 || v.Hosts[0].Host != "docs.example.com" {
		t.Fatalf("after delete = %+v", v.Hosts)
	}
	w := g.call("DELETE", `{"host":"github.com"}`)
	if code, _ := errCode(t, w); w.Code != 404 || code != "not_found" {
		t.Fatalf("delete of a missing host: %d %s", w.Code, w.Body)
	}
	if w := g.call("PUT", ""); w.Code != 405 {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := g.call("PATCH", `{"host":"github.com"}`); w.Code != 405 {
		t.Fatalf("PATCH: %d", w.Code)
	}
}

// Rows are kept per actor: the handler passes storeActor, and another actor's
// rows in the same file are never listed, changed or removed.
func TestLinkTrustAPIOnlyOwnRows(t *testing.T) {
	g := newLinkTrustRig(t)
	body := `{"version":1,"hosts":[{"actor":"someone-else","host":"theirs.example.com","subdomains":false,"added":"2026-01-01T00:00:00Z"},` +
		`{"actor":"someone-else","host":"shared.example.com","subdomains":true,"added":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(g.dir, linktrust.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := decodeLT(t, g.call("GET", "")); len(v.Hosts) != 0 {
		t.Fatalf("listed another actor's rows: %+v", v.Hosts)
	}
	if w := g.call("DELETE", `{"host":"theirs.example.com"}`); w.Code != 404 {
		t.Fatalf("removed another actor's row: %d %s", w.Code, w.Body)
	}
	v := decodeLT(t, g.call("POST", `{"host":"shared.example.com"}`))
	if len(v.Hosts) != 1 || v.Hosts[0].Subdomains {
		t.Fatalf("own row = %+v", v.Hosts)
	}
	list, err := linktrust.NewService(g.dir, nil, nil).List("someone-else")
	if err != nil || len(list) != 2 || !list[1].Subdomains {
		t.Fatalf("the other actor's rows changed: %+v, %v", list, err)
	}
	if len(g.rec.events) != 1 || g.rec.events[0].Actor != storeActor {
		t.Fatalf("audit rows = %+v, want one by %q", g.rec.events, storeActor)
	}
}

func TestLinkTrustAPIBearerRefused(t *testing.T) {
	g := newLinkTrustRig(t)
	if _, err := g.s.LinkTrust.Add("keep.example.com", false, storeActor); err != nil {
		t.Fatal(err)
	}
	g.rec.events = nil
	for _, m := range []string{"POST", "DELETE"} {
		w := do(g.s.handleLinkTrust, storeReq(m, "/api/link-trust", `{"host":"keep.example.com"}`, "http://"+host, credBearer))
		if w.Code != 403 {
			t.Errorf("%s with a bearer: %d %s", m, w.Code, w.Body)
		}
		if _, msg := errCode(t, w); msg != "changing trusted link hosts needs this page's signed-in browser session" {
			t.Errorf("%s message = %q", m, msg)
		}
	}
	if list, _ := g.s.LinkTrust.List(storeActor); len(list) != 1 || len(g.rec.events) != 0 {
		t.Fatal("a refused write reached the service")
	}
	w := do(g.s.handleLinkTrust, storeReq("GET", "/api/link-trust", "", "", credBearer))
	if v := decodeLT(t, w); len(v.Hosts) != 1 {
		t.Fatalf("a bearer may read: %+v", v)
	}
}

func TestLinkTrustAPICrossOriginRefused(t *testing.T) {
	g := newLinkTrustRig(t)
	for _, m := range []string{"POST", "DELETE"} {
		for _, origin := range []string{"https://evil.example", ""} {
			w := do(g.s.handleLinkTrust, storeReq(m, "/api/link-trust", `{"host":"github.com"}`, origin, credCookie))
			if w.Code != 403 {
				t.Errorf("%s origin %q: %d %s", m, origin, w.Code, w.Body)
			}
		}
	}
	if list, _ := g.s.LinkTrust.List(storeActor); len(list) != 0 || len(g.rec.events) != 0 {
		t.Fatal("a cross-origin write reached the service")
	}
}

func TestLinkTrustAPIBodyStrict(t *testing.T) {
	g := newLinkTrustRig(t)
	for _, b := range []string{
		`{"host":"github.com","extra":1}`, `{"host":"github.com"} {}`, `nope`, ``, `{"host":1}`,
		`{"host":"` + strings.Repeat("a", 17<<10) + `"}`,
	} {
		for _, m := range []string{"POST", "DELETE"} {
			if w := g.call(m, b); w.Code != 400 {
				t.Errorf("%s body %.30q: %d", m, b, w.Code)
			}
		}
	}
	// DELETE takes {host} only; a stray "subdomains" is as strict as any unknown member.
	if w := g.call("DELETE", `{"host":"github.com","subdomains":true}`); w.Code != 400 {
		t.Errorf("DELETE with subdomains: %d", w.Code)
	}
	if list, _ := g.s.LinkTrust.List(storeActor); len(list) != 0 || len(g.rec.events) != 0 {
		t.Fatal("a bad body reached the service")
	}
}

func TestLinkTrustAPIRefusedHosts(t *testing.T) {
	g := newLinkTrustRig(t)
	for name, h := range map[string]string{
		"ipv4":         "10.0.0.1",
		"ipv6":         "[::1]",
		"punycode":     "xn--gthub-zsa.com",
		"port":         "example.com:8080",
		"uppercase":    "Example.com",
		"single label": "localhost",
		"trailing dot": "example.com.",
		"over 253":     strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 62),
		"label over":   strings.Repeat("a", 64) + ".com",
		"empty":        "",
	} {
		b, _ := json.Marshal(map[string]string{"host": h})
		w := g.call("POST", string(b))
		code, msg := errCode(t, w)
		if w.Code != 400 || code != "refused" || msg == "" {
			t.Errorf("%s (%q): %d code %q error %q, want 400 refused", name, h, w.Code, code, msg)
		}
		if strings.Contains(msg, h) && h != "" {
			t.Errorf("%s: the reply echoes the input: %q", name, msg)
		}
	}
	if w := g.call("POST", `{}`); w.Code != 400 {
		t.Errorf("no host: %d", w.Code)
	}
	if list, _ := g.s.LinkTrust.List(storeActor); len(list) != 0 || len(g.rec.events) != 0 {
		t.Fatal("a refused host was saved or audited")
	}
}

func TestLinkTrustAPICap(t *testing.T) {
	g := newLinkTrustRig(t)
	for i := 0; i < linktrust.MaxPerActor; i++ {
		if _, err := g.s.LinkTrust.Add(strings.Repeat("a", 1+i/26)+string(rune('a'+i%26))+".example.com", false, storeActor); err != nil {
			t.Fatal(err)
		}
	}
	w := g.call("POST", `{"host":"one-too-many.example.com"}`)
	if code, _ := errCode(t, w); w.Code != 409 || code != "cap" {
		t.Fatalf("cap: %d %s", w.Code, w.Body)
	}
}

func TestLinkTrustAPIAuditFailureNoHost(t *testing.T) {
	g := newLinkTrustRig(t)
	if w := g.call("POST", `{"host":"keep.example.com"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	g.rec.err = errors.New("audit store: disk full")
	for _, c := range []struct{ method, body string }{
		{"POST", `{"host":"new.example.com"}`},
		{"POST", `{"host":"keep.example.com","subdomains":true}`},
		{"DELETE", `{"host":"keep.example.com"}`},
	} {
		w := g.call(c.method, c.body)
		if w.Code < 500 {
			t.Fatalf("%s %s: a failed audit must fail the change: %d %s", c.method, c.body, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "disk full") {
			t.Fatalf("raw error text reached the page: %s", w.Body)
		}
	}
	g.rec.err = nil
	v := decodeLT(t, g.call("GET", ""))
	if len(v.Hosts) != 1 || v.Hosts[0].Host != "keep.example.com" || v.Hosts[0].Subdomains {
		t.Fatalf("a change was saved without its audit row: %+v", v.Hosts)
	}
}

func TestLinkTrustAPICorrupt(t *testing.T) {
	g := newLinkTrustRig(t)
	if err := os.WriteFile(filepath.Join(g.dir, linktrust.FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := decodeLT(t, g.call("GET", ""))
	if !v.Corrupt || v.Hosts == nil || len(v.Hosts) != 0 {
		t.Fatalf("GET on a damaged file = %+v", v)
	}
	for _, c := range []struct{ method, body string }{
		{"POST", `{"host":"github.com"}`},
		{"DELETE", `{"host":"github.com"}`},
	} {
		w := g.call(c.method, c.body)
		code, msg := errCode(t, w)
		if w.Code != 409 || code != "list_corrupt" {
			t.Errorf("%s: %d %s", c.method, w.Code, w.Body)
		}
		if strings.Contains(msg, g.dir) {
			t.Errorf("the data folder path reached the page: %s", msg)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(g.dir, linktrust.FileName)); string(b) != "{nope" {
		t.Errorf("a damaged file was rewritten: %q", b)
	}
	if len(g.rec.events) != 0 {
		t.Errorf("audited on a damaged file: %+v", g.rec.events)
	}
}

func TestLinkTrustAPINewerFile(t *testing.T) {
	g := newLinkTrustRig(t)
	if err := os.WriteFile(filepath.Join(g.dir, linktrust.FileName), []byte(`{"version":2,"hosts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := decodeLT(t, g.call("GET", "")); !v.Corrupt {
		t.Fatalf("a newer file must read as unusable: %+v", v)
	}
	for _, c := range []struct{ method, body string }{
		{"POST", `{"host":"github.com"}`},
		{"DELETE", `{"host":"github.com"}`},
	} {
		w := g.call(c.method, c.body)
		code, msg := errCode(t, w)
		if w.Code != 409 || code != "list_corrupt" {
			t.Fatalf("%s on a newer file: %d %s", c.method, w.Code, w.Body)
		}
		// Deleting a newer CuliBox's file would throw its data away: say update, never delete.
		if msg != "link-trust.json was written by a newer CuliBox. Update CuliBox to change trusted hosts." {
			t.Errorf("%s newer-file text = %q", c.method, msg)
		}
		if strings.Contains(strings.ToLower(msg), "delete") || strings.Contains(msg, "damaged") {
			t.Errorf("%s: the newer-file text tells the user to delete or calls it damaged: %q", c.method, msg)
		}
	}
}

// A list that cannot be read reaches the log, not only the page's corrupt flag.
func TestLinkTrustAPIListErrorIsLogged(t *testing.T) {
	g := newLinkTrustRig(t)
	if err := os.WriteFile(filepath.Join(g.dir, linktrust.FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if v := decodeLT(t, g.call("GET", "")); !v.Corrupt {
		t.Fatalf("view = %+v", v)
	}
	if !strings.Contains(buf.String(), "link trust") || !strings.Contains(buf.String(), linktrust.FileName) {
		t.Errorf("log = %q, want the list error with the file name", buf.String())
	}
}

func TestLinkTrustAuditRow(t *testing.T) {
	g := newLinkTrustRig(t)
	g.call("POST", `{"host":"github.com","subdomains":true}`)
	g.call("POST", `{"host":"github.com","subdomains":false}`)
	g.call("POST", `{"host":"github.com","subdomains":false}`) // no-op: no row
	g.call("POST", `{"host":"docs.example.com"}`)
	g.call("DELETE", `{"host":"github.com"}`)
	g.call("DELETE", `{"host":"docs.example.com"}`)
	want := []broker.CredEvent{
		{Action: broker.CredCreated, Kind: "trusted_link_host", Name: "github.com", Actor: storeActor, Detail: "and its subdomains"},
		{Action: broker.CredUpdated, Kind: "trusted_link_host", Name: "github.com", Actor: storeActor},
		{Action: broker.CredCreated, Kind: "trusted_link_host", Name: "docs.example.com", Actor: storeActor},
		{Action: broker.CredDeleted, Kind: "trusted_link_host", Name: "github.com", Actor: storeActor},
		{Action: broker.CredDeleted, Kind: "trusted_link_host", Name: "docs.example.com", Actor: storeActor},
	}
	if len(g.rec.events) != len(want) {
		t.Fatalf("audit rows = %+v, want %+v", g.rec.events, want)
	}
	for i := range want {
		if g.rec.events[i] != want[i] {
			t.Errorf("audit row %d = %+v, want %+v", i, g.rec.events[i], want[i])
		}
	}
}

// A deleted row says whether it covered subdomains, like the row that created it.
func TestLinkTrustAuditRowDeleteKeepsFlag(t *testing.T) {
	g := newLinkTrustRig(t)
	g.call("POST", `{"host":"github.com","subdomains":true}`)
	g.call("DELETE", `{"host":"github.com"}`)
	got := g.rec.events[len(g.rec.events)-1]
	if got.Action != broker.CredDeleted || got.Detail != "and its subdomains" {
		t.Errorf("delete row = %+v", got)
	}
}

// The route exists only with a service wired (like /api/workspace-roots), and
// is behind the same auth as the rest of /api.
func TestLinkTrustRouteOnlyWithService(t *testing.T) {
	get := func(s *Server, tok string) int {
		r := httptest.NewRequest("GET", "/api/link-trust", nil)
		r.Host = "example.com"
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	with, tok := routedServer(t)
	with.LinkTrust = linktrust.NewService(t.TempDir(), nil, nil)
	if c := get(with, tok); c != 200 {
		t.Errorf("with a service: %d", c)
	}
	without, tok := routedServer(t)
	if c := get(without, tok); c != 404 {
		t.Errorf("without a service: %d", c)
	}
	// Without a credential the route is behind the login, not open.
	anon := httptest.NewRequest("GET", "/api/link-trust", nil)
	anon.Host = "example.com"
	w := httptest.NewRecorder()
	with.Handler().ServeHTTP(w, anon)
	if w.Code != 401 {
		t.Errorf("anonymous: %d, want 401", w.Code)
	}
}

func TestLinkTrustHandlerWithoutServiceIs404(t *testing.T) {
	s := &Server{}
	w := do(s.handleLinkTrust, storeReq("GET", "/api/link-trust", "", "", credCookie))
	if w.Code != 404 {
		t.Errorf("no service: %d", w.Code)
	}
}
