package auditlog

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
)

// seeder writes rows into one audit.db through the real writers (credential,
// approval, egress) and by SQL for store/cred, whose writers stamp time.Now().
type seeder struct {
	t    *testing.T
	path string
	b    *broker.Audit
	e    *egress.Audit
	raw  *sql.DB
	next map[int]int64 // per-src id counter, mirrors INTEGER PRIMARY KEY
}

func newSeeder(t *testing.T) *seeder {
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
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(); e.Close(); raw.Close() })
	return &seeder{t: t, path: path, b: b, e: e, raw: raw, next: map[int]int64{}}
}

func (s *seeder) id(src int) int64 { s.next[src]++; return s.next[src] }

func (s *seeder) credential(ts int64, session string, o broker.Outcome) {
	s.t.Helper()
	s.id(srcCredential)
	if err := s.b.Record(broker.Row{Time: time.Unix(ts, 0), SessionID: session, Outcome: o, Entry: "prod"}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seeder) approval(ts int64, session string, a broker.Action) {
	s.t.Helper()
	s.id(srcApproval)
	if err := s.b.RecordApproval(broker.ApprovalRow{Time: time.Unix(ts, 0), SessionID: session, Action: a,
		Entry: "prod", Minutes: 30, Reason: "fix", Cred: "cookie"}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seeder) egress(ts int64, session string, o egress.Outcome, suppressed int) {
	s.t.Helper()
	s.id(srcEgress)
	if err := s.e.Record(egress.Row{Time: time.Unix(ts, 0), SessionID: session, Host: "pypi.org", Port: 443, Outcome: o, Suppressed: suppressed}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seeder) cred(ts int64) {
	s.t.Helper()
	s.id(srcCred)
	if _, err := s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor) VALUES (?, 'created', 'token', 'cf', 'you')`, ts); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seeder) store(ts int64) {
	s.t.Helper()
	s.id(srcStore)
	if _, err := s.raw.Exec(`INSERT INTO store_audit (ts, action, actor) VALUES (?, 'unlocked', 'you')`, ts); err != nil {
		s.t.Fatal(err)
	}
}

func (s *seeder) open() *Reader {
	s.t.Helper()
	r, err := Open(s.path)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { r.Close() })
	return r
}

type key struct {
	ts  int64
	src int
	id  int64
}

func (k key) cursor() string { return formatCursor(k.ts, k.src, k.id) }

func TestQueryKeysetNoDupNoGap(t *testing.T) {
	s := newSeeder(t)
	const base = 1_800_000_000
	var want []key
	for i := 0; i < 1200; i++ {
		ts := int64(base + i)
		if i < 300 {
			ts = base + 5000 // 300 rows share one second, spread over all five tables
		}
		src := []int{srcCredential, srcApproval, srcEgress, srcCred, srcStore}[i%5]
		switch src {
		case srcCredential:
			s.credential(ts, "s1", broker.OutcomeMinted)
		case srcApproval:
			s.approval(ts, "s1", broker.ActionGranted)
		case srcEgress:
			s.egress(ts, "s1", egress.OutcomeAllowed, 0)
		case srcCred:
			s.cred(ts)
		case srcStore:
			s.store(ts)
		}
		want = append(want, key{ts, src, s.next[src]})
	}
	sort.Slice(want, func(i, j int) bool {
		a, b := want[i], want[j]
		if a.ts != b.ts {
			return a.ts > b.ts
		}
		if a.src != b.src {
			return a.src > b.src
		}
		return a.id > b.id
	})
	r := s.open()

	var got []string
	before, pages := "", 0
	for {
		p, err := r.Query(Query{Limit: 97, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if p.Total != 1200 {
			t.Fatalf("page %d total %d, want 1200", pages, p.Total)
		}
		for _, e := range p.Events {
			got = append(got, e.Cursor)
		}
		if p.Next == "" {
			break
		}
		if p.Next != p.Events[len(p.Events)-1].Cursor {
			t.Fatalf("Next %q is not the last cursor", p.Next)
		}
		before = p.Next
	}
	if pages != 13 {
		t.Errorf("pages = %d, want 13", pages)
	}
	if len(got) != 1200 {
		t.Fatalf("got %d rows, want 1200", len(got))
	}
	for i, k := range want {
		if got[i] != k.cursor() {
			t.Fatalf("row %d: got %s, want %s", i, got[i], k.cursor())
		}
	}
}

func TestQueryFilters(t *testing.T) {
	s := newSeeder(t)
	s.credential(100, "a", broker.OutcomeMinted)
	s.credential(101, "b", broker.OutcomeDeniedToken)
	s.approval(102, "a", broker.ActionGranted)
	s.approval(103, "a", broker.ActionSSOLoginOK)
	s.approval(104, "b", broker.ActionRefused)
	s.egress(105, "a", egress.OutcomeAllowed, 0)
	s.egress(106, "b", egress.OutcomeDenied, 2)
	s.cred(107)
	s.store(108)
	r := s.open()

	kinds := func(q Query) []string {
		t.Helper()
		p, err := r.Query(q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		if p.Total != len(p.Events) {
			t.Errorf("%+v: total %d != %d events", q, p.Total, len(p.Events))
		}
		var out []string
		for _, e := range p.Events {
			out = append(out, e.Kind)
		}
		slices.Sort(out)
		return out
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	eq("all", kinds(Query{}), []string{"credential", "credential", "elevation", "elevation", "signin", "egress", "egress", "settings", "store"})
	eq("credentials", kinds(Query{Filter: "credentials"}), []string{"credential", "credential", "signin"})
	eq("elevations", kinds(Query{Filter: "elevations"}), []string{"elevation", "elevation"})
	eq("egress", kinds(Query{Filter: "egress"}), []string{"egress", "egress"})
	eq("settings", kinds(Query{Filter: "settings"}), []string{"settings", "store"})
	// A session filter drops settings and store, and other sessions' egress.
	eq("session a", kinds(Query{Session: "a"}), []string{"credential", "elevation", "signin", "egress"})
	eq("session a + settings", kinds(Query{Session: "a", Filter: "settings"}), nil)
	eq("session b egress", kinds(Query{Session: "b", Filter: "egress"}), []string{"egress"})

	// From and To are inclusive.
	p, err := r.Query(Query{From: 102, To: 104})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || len(p.Events) != 3 || p.Events[0].TS != 104 || p.Events[2].TS != 102 {
		t.Errorf("range: %+v", p)
	}
	// To 0 is open-ended.
	if p, _ := r.Query(Query{From: 107}); p.Total != 2 {
		t.Errorf("open range total %d, want 2", p.Total)
	}
	// An empty page is [] not null.
	p, _ = r.Query(Query{From: 5000})
	if p.Events == nil || len(p.Events) != 0 {
		t.Errorf("empty page events = %#v", p.Events)
	}
}

func TestQueryCaps(t *testing.T) {
	s := newSeeder(t)
	for i := 0; i < 650; i++ {
		s.egress(int64(1000+i), "a", egress.OutcomeAllowed, 0)
	}
	r := s.open()
	for _, c := range []struct{ limit, want int }{{0, 100}, {100_000, 500}, {7, 7}} {
		p, err := r.Query(Query{Limit: c.limit})
		if err != nil || len(p.Events) != c.want {
			t.Errorf("limit %d: %d events, err %v; want %d", c.limit, len(p.Events), err, c.want)
		}
		if p.Next == "" {
			t.Errorf("limit %d: no Next with 650 rows", c.limit)
		}
	}
	bad := []Query{
		{From: -1},
		{From: 10, To: 5},
		{Before: "nope"},
		{Before: "1.6.1"},
		{Before: "1.0.1"},
		{Before: "1.3.1; DROP TABLE x"},
		{Before: "1234567890123.3.1"},
		{Filter: "everything"},
		{Session: "has space"},
		{Session: "a'b"},
		{Session: string(make([]byte, 81))},
		{Limit: -3},
	}
	for _, q := range bad {
		if _, err := r.Query(q); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v: err = %v, want ErrBadQuery", q, err)
		}
	}
	if _, err := r.Summary(-1, 0); !errors.Is(err, ErrBadQuery) {
		t.Errorf("Summary(-1): %v", err)
	}
	if _, err := r.Summary(10, 5); !errors.Is(err, ErrBadQuery) {
		t.Errorf("Summary(10,5): %v", err)
	}
	if err := r.Each(Query{Filter: "x"}, 10, func(Event) error { return nil }); !errors.Is(err, ErrBadQuery) {
		t.Errorf("Each bad filter: %v", err)
	}
}

func TestEach(t *testing.T) {
	s := newSeeder(t)
	for i := 0; i < 1300; i++ {
		s.egress(int64(1000+i/4), "a", egress.OutcomeAllowed, 0)
	}
	r := s.open()
	var got []string
	if err := r.Each(Query{}, 1100, func(e Event) error { got = append(got, e.Cursor); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1100 || len(slices.Compact(slices.Clone(got))) != 1100 {
		t.Fatalf("Each yielded %d rows (%d distinct), want 1100", len(got), len(slices.Compact(slices.Clone(got))))
	}
	n := 0
	if err := r.Each(Query{}, 100_000, func(Event) error { n++; return nil }); err != nil || n != 1300 {
		t.Errorf("all: n=%d err=%v, want 1300", n, err)
	}
	stop := errors.New("stop")
	if err := r.Each(Query{}, 50, func(Event) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("fn error not returned: %v", err)
	}
}

func TestSummaryCounts(t *testing.T) {
	s := newSeeder(t)
	const ts = 5000
	for _, o := range []broker.Outcome{broker.OutcomeMinted, broker.OutcomeMinted, broker.OutcomeFetched,
		broker.OutcomeDeniedToken, broker.OutcomeDeniedSession, broker.OutcomeDeniedScope,
		broker.OutcomeSSOExpired, broker.OutcomeProbeMinted, broker.OutcomeRevoked} {
		s.credential(ts, "c1", o)
	}
	s.approval(ts, "c1", broker.ActionGranted)
	s.approval(ts, "c1", broker.ActionGranted)
	s.approval(ts, "c1", broker.ActionExtended)
	s.approval(ts, "c1", broker.ActionRefused)
	s.approval(ts, "c1", broker.ActionSSOLoginOK)
	s.egress(ts, "c1", egress.OutcomeAllowed, 0)
	s.egress(ts, "c1", egress.OutcomeAllowedPrivate, 0)
	s.egress(ts, "c1", egress.OutcomeDenied, 3) // counts 4
	s.egress(ts, "c1", egress.OutcomeDialError, 0)
	s.egress(ts, "", egress.OutcomeBadAuth, 0) // unauthenticated: counts, but no session
	// Outside the range: ignored.
	s.credential(ts+100, "late", broker.OutcomeMinted)
	s.egress(ts-100, "early", egress.OutcomeDenied, 9)
	r := s.open()

	got, err := r.Summary(ts, ts+10)
	if err != nil {
		t.Fatal(err)
	}
	want := Summary{Issued: 3, Refused: 4, Elevations: 2, EgressAllowed: 2, EgressDenied: 6, Sessions: []string{"c1"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// An empty range is zeros and [] sessions.
	if z, err := r.Summary(1, 2); err != nil || z.Issued != 0 || z.EgressDenied != 0 || z.Sessions == nil || len(z.Sessions) != 0 {
		t.Errorf("empty: %+v %v", z, err)
	}
}

func TestSummarySessionsCappedNewestFirst(t *testing.T) {
	s := newSeeder(t)
	for i := 0; i < 250; i++ {
		s.credential(int64(1000+i), fmt.Sprintf("s%03d", i), broker.OutcomeMinted)
	}
	s.egress(2000, "s000", egress.OutcomeAllowed, 0) // oldest session becomes the newest
	r := s.open()
	got, err := r.Summary(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != MaxSessions {
		t.Fatalf("sessions = %d, want %d", len(got.Sessions), MaxSessions)
	}
	if got.Sessions[0] != "s000" || got.Sessions[1] != "s249" || got.Sessions[MaxSessions-1] != "s051" {
		t.Errorf("order: %v ... %v", got.Sessions[:2], got.Sessions[MaxSessions-1])
	}
}

func TestTsIndexesExist(t *testing.T) {
	s := newSeeder(t)
	s.b.Close()
	s.e.Close()
	r := s.open()
	for _, table := range []string{"credential_audit", "approval_audit", "egress_audit", "cred_audit", "store_audit"} {
		want := "idx_" + map[string]string{"credential_audit": "credential", "approval_audit": "approval",
			"egress_audit": "egress", "cred_audit": "cred", "store_audit": "store"}[table] + "_ts"
		var n int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list(?) WHERE name = ?`, table, want).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: index %s missing (n=%d, err=%v)", table, want, n, err)
		}
	}
}

// The ts range must be served from the ts index, not a table scan.
func TestQueryUsesTsIndex(t *testing.T) {
	s := newSeeder(t)
	r := s.open()
	var plan string
	rows, err := r.db.Query(`EXPLAIN QUERY PLAN SELECT ts, id FROM credential_audit WHERE ts BETWEEN 1 AND 2 ORDER BY ts DESC, id DESC LIMIT 5`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d + "\n"
	}
	if !strings.Contains(plan, "idx_credential_ts") {
		t.Errorf("plan does not use idx_credential_ts:\n%s", plan)
	}
}
