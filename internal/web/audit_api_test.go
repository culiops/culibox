package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/auditlog"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/secretstore"
)

// auditFixture is a temp audit.db seeded through the real writers, read back
// through a real auditlog.Reader.
type auditFixture struct {
	t    *testing.T
	b    *broker.Audit
	e    *egress.Audit
	rd   *auditlog.Reader
	base time.Time
}

func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	b, err := broker.OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := egress.OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := auditlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rd.Close(); b.Close(); e.Close() })
	return &auditFixture{t: t, b: b, e: e, rd: rd, base: time.Now().Add(-time.Hour).Truncate(time.Second)}
}

func (f *auditFixture) approval(i int, reason string) {
	f.t.Helper()
	err := f.b.RecordApproval(broker.ApprovalRow{Time: f.base.Add(time.Duration(i) * time.Second), SessionID: "s1",
		Action: broker.ActionGranted, Entry: "prod", Minutes: 30, Reason: reason, Cred: "cookie"})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *auditFixture) egressRow(i int) {
	f.t.Helper()
	err := f.e.Record(egress.Row{Time: f.base.Add(time.Duration(i) * time.Second), SessionID: "s1",
		Host: "pypi.org", Port: 443, Outcome: egress.Outcome("allowed")})
	if err != nil {
		f.t.Fatal(err)
	}
}

// server is a routed Server with the fixture's reader, plus a session cookie.
func (f *auditFixture) server(t *testing.T) (http.Handler, *http.Cookie, string) {
	s, plain := routedServer(t)
	s.Audit = f.rd
	s.AuditRetentionDays = 90
	ck := issueCookie(t, s)
	return s.Handler(), ck, plain
}

func auditReq(h http.Handler, method, target string, ck *http.Cookie, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if ck != nil {
		r.AddCookie(ck)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

var auditRoutes = []string{"/api/audit", "/api/audit/summary", "/api/audit.csv"}

func TestAuditRoutesCookieOnly(t *testing.T) {
	f := newAuditFixture(t)
	f.approval(1, "fix")
	h, ck, bearer := f.server(t)
	for _, p := range auditRoutes {
		if rec := auditReq(h, "GET", p, nil, bearer); rec.Code != 403 || !strings.Contains(rec.Body.String(), "audit is read from the web UI") {
			t.Fatalf("%s bearer: got %d %s", p, rec.Code, rec.Body)
		}
		rec := auditReq(h, "GET", p, ck, "")
		if rec.Code != 200 {
			t.Fatalf("%s cookie: got %d %s", p, rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", p, cc)
		}
	}
	// No credential at all never reaches the handler through the router (401);
	// called directly it is still refused.
	s := &Server{Audit: f.rd}
	for name, fn := range map[string]http.HandlerFunc{"list": s.handleAudit, "summary": s.handleAuditSummary, "csv": s.handleAuditCSV} {
		r := httptest.NewRequest("GET", "/x", nil)
		r = r.WithContext(context.WithValue(r.Context(), credKindKey, credNone))
		rec := httptest.NewRecorder()
		fn(rec, r)
		if rec.Code != 403 {
			t.Fatalf("%s no credential: got %d", name, rec.Code)
		}
	}
}

func TestAuditListAndSummaryShape(t *testing.T) {
	f := newAuditFixture(t)
	f.approval(1, "fix")
	f.egressRow(2)
	h, ck, _ := f.server(t)

	var page auditlog.Page
	rec := auditReq(h, "GET", "/api/audit", ck, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Total != 2 || len(page.Events) != 2 {
		t.Fatalf("page = %+v err=%v body=%s", page, err, rec.Body)
	}
	rec = auditReq(h, "GET", "/api/audit?kind=egress", ck, "")
	page = auditlog.Page{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Total != 1 || page.Events[0].Kind != auditlog.KindEgress {
		t.Fatalf("kind=egress page = %+v err=%v", page, err)
	}
	// An old window finds nothing: from/to are honoured, not ignored.
	old := time.Now().AddDate(0, 0, -30).Unix()
	rec = auditReq(h, "GET", "/api/audit?from="+strconv.FormatInt(old, 10)+"&to="+strconv.FormatInt(old+60, 10), ck, "")
	page = auditlog.Page{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Total != 0 {
		t.Fatalf("old window = %+v err=%v", page, err)
	}

	var sum map[string]any
	rec = auditReq(h, "GET", "/api/audit/summary", ck, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum["retention_days"] != float64(90) || sum["elevations"] != float64(1) || sum["egress_allowed"] != float64(1) {
		t.Fatalf("summary = %v", sum)
	}
}

func TestAuditBadParams(t *testing.T) {
	f := newAuditFixture(t)
	f.approval(1, "fix")
	h, ck, _ := f.server(t)
	long := strings.Repeat("a", 100)
	bad := []string{
		"limit=abc", "from=-1", "to=-5", "from=abc", "from=100&to=50", "before=1.9.1",
		"kind=../x", "session=" + long, "session=a%2Fb",
	}
	for _, q := range bad {
		for _, p := range auditRoutes {
			if p == "/api/audit/summary" && !strings.HasPrefix(q, "from") && !strings.HasPrefix(q, "to") {
				continue // the summary takes only from and to
			}
			if rec := auditReq(h, "GET", p+"?"+q, ck, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad audit query") {
				t.Fatalf("%s?%s: got %d %s", p, q, rec.Code, rec.Body)
			}
		}
	}
	rec := auditReq(h, "GET", "/api/audit?limit=100000", ck, "")
	var page auditlog.Page
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Events) > auditlog.MaxLimit {
		t.Fatalf("limit=100000: %d (%d events)", rec.Code, len(page.Events))
	}
}

func TestAuditPostRefused(t *testing.T) {
	f := newAuditFixture(t)
	h, ck, _ := f.server(t)
	for _, p := range auditRoutes {
		if rec := auditReq(h, "POST", p, ck, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST: got %d", p, rec.Code)
		}
	}
}

func csvRecords(t *testing.T, body string) ([][]string, string) {
	t.Helper()
	trailer := ""
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if n := len(lines); n > 0 && strings.HasPrefix(lines[n-1], "# ") {
		trailer = lines[n-1]
		body = strings.Join(lines[:n-1], "\n") + "\n"
	}
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v\n%s", err, body)
	}
	return recs, trailer
}

func TestAuditCSVNeutralises(t *testing.T) {
	f := newAuditFixture(t)
	f.approval(1, `=HYPERLINK("x")`)
	f.approval(2, "key AKIAABCDEFGHIJKLMNOP")
	h, ck, _ := f.server(t)
	rec := auditReq(h, "GET", "/api/audit.csv", ck, "")
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	d := time.Now().UTC().Format("20060102")
	wantPrefix := `attachment; filename="culibox-audit-`
	if !strings.HasPrefix(cd, wantPrefix) || !strings.Contains(cd, "-"+d+`.csv"`) {
		t.Fatalf("content-disposition = %q", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"'=HYPERLINK(""x"")"`) {
		t.Fatalf("formula not neutralised and csv-quoted:\n%s", body)
	}
	if strings.Contains(body, "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(body, "[masked:aws-key-id]") {
		t.Fatalf("key not masked:\n%s", body)
	}
	recs, trailer := csvRecords(t, body)
	if got := strings.Join(recs[0], ","); got != "time_utc,session,kind,what,outcome,count,actor,reason" {
		t.Fatalf("header = %q", got)
	}
	if len(recs) != 3 || trailer != "" {
		t.Fatalf("rows = %d trailer %q", len(recs), trailer)
	}
	if _, err := time.Parse(time.RFC3339, recs[1][0]); err != nil {
		t.Fatalf("time_utc %q: %v", recs[1][0], err)
	}
}

// everyKind seeds one row of each shape through the real writers, the last
// being a store unlock with a ttl, and returns the exact What of each.
func (f *auditFixture) everyKind() []string {
	f.t.Helper()
	at := func(i int) time.Time { return f.base.Add(time.Duration(i) * time.Second) }
	for _, err := range []error{
		f.b.Record(broker.Row{Time: at(1), SessionID: "s1", AccountID: "111111111111", RoleARN: "arn:aws:iam::111111111111:role/team/agent-ops-write",
			Outcome: broker.OutcomeMinted, Elevated: true, Entry: "culipulse-prod"}),
		f.b.Record(broker.Row{Time: at(2), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 3, Outcome: broker.OutcomeMinted}),
		f.b.Record(broker.Row{Time: at(3), SessionID: "s1", Provider: broker.ProviderStatic, Entry: "datadog-read", Outcome: broker.OutcomeFetched}),
		f.b.RecordApproval(broker.ApprovalRow{Time: at(4), SessionID: "s1", Action: broker.ActionGranted, Entry: "culipulse-prod", Minutes: 30, Reason: "rotate the key", Cred: "cookie"}),
		f.b.RecordApproval(broker.ApprovalRow{Time: at(5), Action: broker.ActionSSOLoginOK, Entry: "culipulse-prod", Cred: "cookie"}),
		f.e.Record(egress.Row{Time: at(6), SessionID: "s1", Host: "registry.example.test", Port: 443, Outcome: egress.OutcomeAllowed}),
		f.e.Record(egress.Row{Time: at(7), SessionID: "s1", Host: "files.example.test", Port: 443, Outcome: egress.OutcomeAllowed, BytesDown: 148 << 20}),
		f.b.RecordCred(broker.CredEvent{Action: broker.CredCreated, Kind: "token", Name: "e2e-pat", Actor: "web"}),
		f.b.RecordStore(secretstore.Event{Action: "value_set", Actor: "web", Detail: "e2e-pat"}),
		f.b.RecordStore(secretstore.Event{Action: "locked", Actor: "web"}),
		f.b.RecordStore(secretstore.Event{Action: "unlocked", Actor: "web", Detail: "ttl=168h0m0s"}),
	} {
		if err != nil {
			f.t.Fatal(err)
		}
	}
	return []string{
		"aws culipulse-prod · role agent-ops-write · elevated",
		"github culiops · 3 repos",
		"token datadog-read",
		"write on culipulse-prod · 30 min",
		"SSO sign-in culipulse-prod",
		"registry.example.test:443",
		"files.example.test:443 · 148 MB down",
		"token e2e-pat created",
		"encrypted store value set · e2e-pat",
		"encrypted store locked",
		"encrypted store unlocked · ttl=168h0m0s",
	}
}

// The CSV export keeps today's What byte for byte: the plain-words ttl and the
// parts belong to the page, not to the file.
func TestAuditCSVWhatUnchanged(t *testing.T) {
	f := newAuditFixture(t)
	want := f.everyKind()
	h, ck, _ := f.server(t)
	rec := auditReq(h, "GET", "/api/audit.csv", ck, "")
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	recs, trailer := csvRecords(t, rec.Body.String())
	if got := strings.Join(recs[0], ","); got != auditCSVHeader {
		t.Fatalf("header = %q, want %q", got, auditCSVHeader)
	}
	if len(recs) != len(want)+1 || trailer != "" {
		t.Fatalf("rows = %d trailer %q, want %d rows", len(recs)-1, trailer, len(want))
	}
	var got []string
	for _, r := range recs[1:] {
		got = append(got, r[3])
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("what cells\n got %q\nwant %q", got, want)
	}
}

// /api/audit carries each row's parts: only "text" and "id" keys, id only when
// true, and a store unlock reads its ttl in plain words.
func TestAuditListCarriesParts(t *testing.T) {
	f := newAuditFixture(t)
	wantWhat := f.everyKind()
	h, ck, _ := f.server(t)
	rec := auditReq(h, "GET", "/api/audit", ck, "")
	if rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var page struct {
		Events []struct {
			What  string `json:"what"`
			Parts []map[string]any
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Events) != len(wantWhat) {
		t.Fatalf("events = %d err=%v body=%s", len(page.Events), err, rec.Body)
	}
	ids, unlock := 0, false
	for _, e := range page.Events {
		if len(e.Parts) == 0 {
			t.Errorf("%q: no parts", e.What)
		}
		joined := ""
		for _, p := range e.Parts {
			text, ok := p["text"].(string)
			if !ok || len(p) > 2 {
				t.Errorf("%q: part %v, want a text and nothing but an id", e.What, p)
			}
			if id, has := p["id"]; len(p) == 2 && (!has || id != true) {
				t.Errorf("%q: part %v, want id true or no id", e.What, p)
			}
			if p["id"] == true {
				ids++
			}
			joined += text
		}
		if e.What == "encrypted store unlocked · ttl=168h0m0s" {
			unlock = true
			if joined != "encrypted store unlocked · for 7 days" {
				t.Errorf("unlock parts join to %q", joined)
			}
		} else if joined != e.What {
			t.Errorf("parts join to %q, What is %q", joined, e.What)
		}
	}
	if !unlock || ids == 0 {
		t.Errorf("unlock row seen = %v, id parts = %d", unlock, ids)
	}
}

func TestAuditCSVCap(t *testing.T) {
	old := auditCSVMax
	auditCSVMax = 5
	t.Cleanup(func() { auditCSVMax = old })
	f := newAuditFixture(t)
	for i := 0; i < 7; i++ {
		f.approval(i, "r")
	}
	h, ck, _ := f.server(t)
	recs, trailer := csvRecords(t, auditReq(h, "GET", "/api/audit.csv", ck, "").Body.String())
	if len(recs) != 6 { // header + 5
		t.Fatalf("records = %d, want 6", len(recs))
	}
	if trailer != "# truncated at 5 rows" {
		t.Fatalf("trailer = %q", trailer)
	}
	// Exactly at the cap is not truncated.
	auditCSVMax = 7
	_, trailer = csvRecords(t, auditReq(h, "GET", "/api/audit.csv", ck, "").Body.String())
	if trailer != "" {
		t.Fatalf("at cap: trailer = %q", trailer)
	}
}

type failingAudit struct{ AuditReader }

func (failingAudit) Query(auditlog.Query) (auditlog.Page, error) {
	return auditlog.Page{}, context.DeadlineExceeded
}
func (failingAudit) Summary(int64, int64) (auditlog.Summary, error) {
	return auditlog.Summary{}, context.DeadlineExceeded
}
func (failingAudit) Each(auditlog.Query, int, func(auditlog.Event) error) error {
	return context.DeadlineExceeded
}

func TestAuditInternalErrorHasNoDetail(t *testing.T) {
	s, _ := routedServer(t)
	s.Audit = failingAudit{}
	ck := issueCookie(t, s)
	h := s.Handler()
	for _, p := range auditRoutes {
		rec := auditReq(h, "GET", p, ck, "")
		if rec.Code != 500 || !strings.Contains(rec.Body.String(), "query failed") || strings.Contains(rec.Body.String(), "deadline") {
			t.Fatalf("%s: got %d %s", p, rec.Code, rec.Body)
		}
		if p == "/api/audit.csv" && strings.Contains(rec.Body.String(), "time_utc") {
			t.Fatalf("csv header written before the first query succeeded: %s", rec.Body)
		}
	}
}

func TestMetaAuditFlag(t *testing.T) {
	f := newAuditFixture(t)
	if got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})["audit"]; got != false {
		t.Fatalf("without a reader audit = %v", got)
	}
	if got := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, Audit: f.rd})["audit"]; got != true {
		t.Fatalf("with a reader audit = %v", got)
	}
}

func TestCSVCell(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"=1+1", "'=1+1"},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\tx", "'\tx"},
		{"\rx", "'\rx"},
		{"", ""},
		{"write on prod", "write on prod"},
		{"a=b", "a=b"},
		{"s1", "s1"},
	} {
		if got := csvCell(c.in); got != c.want {
			t.Errorf("csvCell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every audit route refuses a window wider than 31 days (a cross-site GET with
// from=0 must not read the whole table) and accepts exactly 31 days.
func TestAuditWindowCap(t *testing.T) {
	f := newAuditFixture(t)
	f.approval(1, "fix")
	f.egressRow(2)
	h, ck, _ := f.server(t)
	to := time.Now().Unix()
	win := func(width int64) string {
		return "from=" + strconv.FormatInt(to-width, 10) + "&to=" + strconv.FormatInt(to, 10)
	}
	const day = int64(86400)
	for _, p := range auditRoutes {
		for name, q := range map[string]string{
			"from=0":      "from=0&to=" + strconv.FormatInt(to, 10),
			"from=0 only": "from=0",
			"31d+1s":      win(31*day + 1),
			"90d":         win(90 * day),
			"default to":  "from=1",
		} {
			if rec := auditReq(h, "GET", p+"?"+q, ck, ""); rec.Code != 400 || !strings.Contains(rec.Body.String(), "bad audit query") {
				t.Errorf("%s?%s (%s): got %d %s, want 400", p, q, name, rec.Code, rec.Body)
			}
		}
		for name, q := range map[string]string{
			"31d": win(31 * day), "30d": win(30 * day), "7d": win(7 * day), "24h": win(day), "default": "",
		} {
			if rec := auditReq(h, "GET", p+"?"+q, ck, ""); rec.Code != 200 {
				t.Errorf("%s?%s (%s): got %d %s, want 200", p, q, name, rec.Code, rec.Body)
			}
		}
	}
}

func TestMetaCloneUnavailable(t *testing.T) {
	m := metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}})
	if got, ok := m["clone_unavailable"]; !ok || got != "" {
		t.Fatalf("unset clone_unavailable = %#v (present %v), want the empty string", got, ok)
	}
	m = metaGet(t, &Server{AllowedHosts: []string{"example.com"}, API: &API{}, CloneUnavailable: "no containment layer"})
	if got := m["clone_unavailable"]; got != "no containment layer" {
		t.Fatalf("clone_unavailable = %#v", got)
	}
}
