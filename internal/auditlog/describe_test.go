package auditlog

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/mask"
)

// TestDescribe pins the exact What strings by writing real rows through the
// real writers and reading them back.
func TestDescribe(t *testing.T) {
	s := newSeeder(t)
	rec := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ts := func(n int) int64 { return int64(1_800_000_000 + n) }
	at := func(n int) time.Time { return time.Unix(ts(n), 0) }

	rec(s.b.Record(broker.Row{Time: at(1), SessionID: "s1", AccountID: "210987654321", RoleARN: "arn:aws:iam::210987654321:role/team/agent-ops",
		Outcome: broker.OutcomeMinted, Elevated: true, Entry: "culipulse-prod"}))
	rec(s.b.Record(broker.Row{Time: at(2), SessionID: "s1", AccountID: "210987654321", Outcome: broker.OutcomeMinted})) // no entry, no role
	rec(s.b.Record(broker.Row{Time: at(3), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 3, Outcome: broker.OutcomeMinted}))
	rec(s.b.Record(broker.Row{Time: at(4), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 1, Outcome: broker.OutcomeMinted}))
	rec(s.b.Record(broker.Row{Time: at(5), SessionID: "s1", Provider: broker.ProviderStatic, Entry: "datadog-read", Outcome: broker.OutcomeFetched}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(6), SessionID: "s1", Action: broker.ActionGranted, Entry: "culipulse-prod", Minutes: 30, Reason: "rotate the key", Cred: "cookie"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(7), SessionID: "s1", Action: broker.ActionExtended, AccountID: "210987654321", Minutes: 15, Cred: "bearer"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(8), SessionID: "s1", Action: broker.ActionExpired, Entry: "culipulse-prod", Cred: "none"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(9), Action: broker.ActionSSOLoginOK, Entry: "sandbox", Cred: "cookie"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(10), Action: broker.ActionSSOLoginFailed, Entry: "sandbox"}))
	rec(s.e.Record(egress.Row{Time: at(11), SessionID: "s1", Host: "pypi.org", Port: 443, Outcome: egress.OutcomeAllowed}))
	rec(s.e.Record(egress.Row{Time: at(12), SessionID: "s1", Host: "registry.npmjs.org", Port: 443, Outcome: egress.OutcomeAllowed, BytesDown: 14 << 20}))
	rec(s.e.Record(egress.Row{Time: at(13), SessionID: "s1", Host: "evil.example", Port: 80, Outcome: egress.OutcomeDenied, Suppressed: 3, BytesDown: 1<<20 - 1}))
	rec(s.e.Record(egress.Row{Time: at(14), SessionID: "s1", Host: "dl.example", Port: 443, Outcome: egress.OutcomeAllowed, BytesDown: 3 << 29}))
	_, err := s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor, detail) VALUES (?, 'created', 'token', 'cloudflare-dns', 'web', 'ignored')`, ts(15))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor) VALUES (?, 'aws_checked', 'aws', 'sandbox', 'cli')`, ts(16))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'unlocked', 'web', '')`, ts(17))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'value_set', 'daemon', 'cf-token')`, ts(18))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'unlocked', 'web', 'ttl=168h0m0s')`, ts(19))
	rec(err)

	p, err := s.open().Query(Query{From: ts(1), To: ts(19), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byTS := map[int64]Event{}
	for _, e := range p.Events {
		byTS[e.TS] = e
	}
	cases := []struct {
		n                                  int
		kind, what, outcome, actor, reason string
		count                              int
		elevated                           bool
	}{
		{1, "credential", "aws culipulse-prod · role agent-ops · elevated", "minted", "", "", 0, true},
		{2, "credential", "aws 210987654321", "minted", "", "", 0, false},
		{3, "credential", "github culiops · 3 repos", "minted", "", "", 0, false},
		{4, "credential", "github culiops · 1 repo", "minted", "", "", 0, false},
		{5, "credential", "token datadog-read", "fetched", "", "", 0, false},
		{6, "elevation", "write on culipulse-prod · 30 min", "granted", "you", "rotate the key", 0, false},
		{7, "elevation", "write on 210987654321 · 15 min", "extended", "API token", "", 0, false},
		{8, "elevation", "write on culipulse-prod", "expired", "", "", 0, false},
		{9, "signin", "SSO sign-in sandbox", "ok", "you", "", 0, false},
		{10, "signin", "SSO sign-in sandbox", "failed", "", "", 0, false},
		{11, "egress", "pypi.org:443", "allowed", "", "", 0, false},
		{12, "egress", "registry.npmjs.org:443 · 14 MB down", "allowed", "", "", 0, false},
		{13, "egress", "evil.example:80", "denied", "", "", 4, false},
		{14, "egress", "dl.example:443 · 1.5 GB down", "allowed", "", "", 0, false},
		{15, "settings", "token cloudflare-dns created", "created", "you", "", 0, false},
		{16, "settings", "aws sandbox aws checked", "aws_checked", "cli", "", 0, false},
		{17, "store", "encrypted store unlocked", "unlocked", "you", "", 0, false},
		{18, "store", "encrypted store value set · cf-token", "value_set", "daemon", "", 0, false},
		{19, "store", "encrypted store unlocked · ttl=168h0m0s", "unlocked", "you", "", 0, false},
	}
	for _, c := range cases {
		e, ok := byTS[ts(c.n)]
		if !ok {
			t.Errorf("case %d: no event", c.n)
			continue
		}
		got := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%v", e.Kind, e.What, e.Outcome, e.Actor, e.Reason, e.Count, e.Elevated)
		want := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%v", c.kind, c.what, c.outcome, c.actor, c.reason, c.count, c.elevated)
		if got != want {
			t.Errorf("case %d:\n got %s\nwant %s", c.n, got, want)
		}
		if (c.kind == "settings" || c.kind == "store") && e.Session != "" {
			t.Errorf("case %d: session %q on a sessionless row", c.n, e.Session)
		}
	}
	if len(p.Events) != len(cases) {
		t.Errorf("%d events, want %d", len(p.Events), len(cases))
	}
}

func TestDescribeMasks(t *testing.T) {
	s := newSeeder(t)
	at := func(n int) time.Time { return time.Unix(int64(1_800_000_000+n), 0) }
	if err := s.b.RecordApproval(broker.ApprovalRow{Time: at(1), SessionID: "s1", Action: broker.ActionGranted, Entry: "prod",
		Reason: "use AKIAIOSFODNN7EXAMPLE now", Cred: "cookie"}); err != nil {
		t.Fatal(err)
	}
	const xss = `<img src=x onerror=alert(1)>`
	if err := s.b.RecordApproval(broker.ApprovalRow{Time: at(2), SessionID: "s1", Action: broker.ActionGranted, Entry: "prod",
		Reason: xss, Cred: "cookie"}); err != nil {
		t.Fatal(err)
	}
	// A credential-shaped entry name is masked in What too.
	if _, err := s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'value_set', 'you', 'AKIAIOSFODNN7EXAMPLE')`, 1_800_000_003); err != nil {
		t.Fatal(err)
	}
	p, err := s.open().Query(Query{})
	if err != nil || len(p.Events) != 3 {
		t.Fatalf("%v %+v", err, p)
	}
	for _, e := range p.Events {
		switch e.Kind {
		case "store":
			if want := "encrypted store value set · [masked:aws-key-id]"; e.What != want {
				t.Errorf("store What = %q, want %q", e.What, want)
			}
		case "elevation":
			if e.TS == 1_800_000_002 {
				if e.Reason != xss {
					t.Errorf("markup reason = %q, want verbatim", e.Reason)
				}
			} else if e.Reason != "use [masked:aws-key-id] now" {
				t.Errorf("reason = %q", e.Reason)
			}
		}
	}
}

// A token-shaped free-text field must be masked before any underscore or space
// normalisation: "ghp_..." must not become "ghp ..." and escape the masker.
func TestDescribeMasksTokenShapedFreeText(t *testing.T) {
	s := newSeeder(t)
	const (
		ghp = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
		pat = "github_pat_" + "11ABCDEFG0abcdefghijkl_mnopqrstuvwxyz0123456789ABCDEFGHIJ"
		aws = "AKIAIOSFODNN7EXAMPLE"
	)
	ins := func(q string, args ...any) {
		t.Helper()
		if _, err := s.raw.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ins(`INSERT INTO cred_audit (ts, action, kind, name, actor, detail) VALUES (1800000001, 'created', 'token', ?, 'web', '')`, ghp)
	ins(`INSERT INTO cred_audit (ts, action, kind, name, actor, detail) VALUES (1800000002, 'created', 'token', ?, 'web', '')`, pat)
	ins(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (1800000003, 'value_set', 'web', ?)`, pat)
	ins(`INSERT INTO credential_audit (ts, session_id, outcome, provider, entry) VALUES (1800000004, 's1', 'fetched', 'static', ?)`, ghp)
	ins(`INSERT INTO credential_audit (ts, session_id, outcome, provider, subject) VALUES (1800000005, 's1', 'minted', 'github', ?)`, pat)
	ins(`INSERT INTO approval_audit (ts, session_id, action, entry, cred) VALUES (1800000006, 's1', 'granted', ?, 'cookie')`, ghp)
	ins(`INSERT INTO egress_audit (ts, session_id, host, port, outcome) VALUES (1800000007, 's1', ?, 443, 'allowed')`, ghp)

	p, err := s.open().Query(Query{})
	if err != nil || len(p.Events) != 7 {
		t.Fatalf("%v %+v", err, p)
	}
	for _, e := range p.Events {
		for _, bad := range []string{"ghp", "github pat", "github_pat", "11ABC", "abcdefghij"} {
			if strings.Contains(e.What, bad) {
				t.Errorf("%s What leaks %q: %q", e.Kind, bad, e.What)
			}
		}
		if !strings.Contains(e.What, "[masked:github-token]") {
			t.Errorf("%s What not masked: %q", e.Kind, e.What)
		}
	}
}

// An sso_direct row says so; a refused one counts as refused.
func TestDescribeSSODirect(t *testing.T) {
	s := newSeeder(t)
	const role = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_ReadOnly_0123456789abcdef"
	for i, o := range []broker.Outcome{broker.OutcomeMinted, broker.OutcomeDeniedNotReadOnly} {
		if err := s.b.Record(broker.Row{Time: time.Unix(int64(1_800_000_001+i), 0), SessionID: "s1", AccountID: "123456789012",
			RoleARN: role, Outcome: o, Entry: "company-ro", Mode: "sso_direct"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.b.Record(broker.Row{Time: time.Unix(1_800_000_003, 0), SessionID: "s1", Outcome: broker.OutcomeMinted, Entry: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	r := s.open()
	p, err := r.Query(Query{From: 1_800_000_000, To: 1_800_000_010, Limit: 10})
	if err != nil || len(p.Events) != 3 {
		t.Fatalf("%+v %v", p, err)
	}
	const what = "aws company-ro · role AWSReservedSSO_ReadOnly_0123456789abcdef · SSO role directly"
	if p.Events[2].What != what || p.Events[1].What != what || p.Events[1].Outcome != "denied_not_read_only" ||
		p.Events[0].What != "aws sandbox" {
		t.Fatalf("events %+v", p.Events)
	}
	sum, err := r.Summary(1_800_000_000, 1_800_000_010)
	if err != nil || sum.Issued != 2 || sum.Refused != 1 {
		t.Fatalf("summary %+v %v", sum, err)
	}
}

func TestHumanDuration(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{168 * time.Hour, "7 days"},
		{24 * time.Hour, "1 day"},
		{90 * time.Minute, "1 h 30 min"},
		{30 * time.Minute, "30 min"},
		{time.Hour, "1 h"},
		{36*time.Hour + 30*time.Minute, "1 day 12 h 30 min"},
		{720 * time.Hour, "30 days"},
		{61 * time.Second, "1 min"},
		{30 * time.Second, "30 s"},
		{0, "0 s"},
		{-5 * time.Second, "0 s"},
	} {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// tp and ip are the expected text and identifier parts.
func tp(s string) Part { return Part{Text: s} }
func ip(s string) Part { return Part{Text: s, ID: true} }

// TestDescribeParts pins the parts of one row of every shape, read back from
// rows the real writers wrote. Joined, the parts are always the What.
func TestDescribeParts(t *testing.T) {
	s := newSeeder(t)
	rec := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ts := func(n int) int64 { return int64(1_800_000_000 + n) }
	at := func(n int) time.Time { return time.Unix(ts(n), 0) }
	const ssoRole = "arn:aws:iam::111111111111:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_ReadOnly_0123456789abcdef"

	rec(s.b.Record(broker.Row{Time: at(1), SessionID: "s1", AccountID: "111111111111", RoleARN: "arn:aws:iam::111111111111:role/team/agent-ops-write",
		Outcome: broker.OutcomeMinted, Elevated: true, Entry: "culipulse-prod"}))
	rec(s.b.Record(broker.Row{Time: at(2), SessionID: "s1", AccountID: "111111111111", RoleARN: ssoRole,
		Outcome: broker.OutcomeMinted, Entry: "company-ro", Mode: "sso_direct"}))
	rec(s.b.Record(broker.Row{Time: at(3), SessionID: "s1", Outcome: broker.OutcomeMinted})) // no entry, no account
	rec(s.b.Record(broker.Row{Time: at(4), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 3, Outcome: broker.OutcomeMinted}))
	rec(s.b.Record(broker.Row{Time: at(5), SessionID: "s1", Provider: broker.ProviderStatic, Entry: "datadog-read", Outcome: broker.OutcomeFetched}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(6), SessionID: "s1", Action: broker.ActionGranted, Entry: "culipulse-prod", Minutes: 30, Reason: "rotate the key", Cred: "cookie"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(7), Action: broker.ActionSSOLoginOK, Entry: "culipulse-prod", Cred: "cookie"}))
	rec(s.e.Record(egress.Row{Time: at(8), SessionID: "s1", Host: "registry.example.test", Port: 443, Outcome: egress.OutcomeAllowed}))
	rec(s.e.Record(egress.Row{Time: at(9), SessionID: "s1", Host: "files.example.test", Port: 443, Outcome: egress.OutcomeAllowed, BytesDown: 148 << 20}))
	_, err := s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor) VALUES (?, 'created', 'token', 'e2e-pat', 'web')`, ts(10))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor) VALUES (?, 'created', 'token', '', 'web')`, ts(11))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'value_set', 'web', 'e2e-pat')`, ts(12))
	rec(err)
	_, err = s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, 'locked', 'web', '')`, ts(13))
	rec(err)

	p, err := s.open().Query(Query{From: ts(1), To: ts(13), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byTS := map[int64]Event{}
	for _, e := range p.Events {
		byTS[e.TS] = e
	}
	cases := []struct {
		name  string
		n     int
		parts []Part
	}{
		{"aws elevated", 1, []Part{tp("aws "), ip("culipulse-prod"), tp(" · role "), ip("agent-ops-write"), tp(" · elevated")}},
		{"aws sso direct", 2, []Part{tp("aws "), ip("company-ro"), tp(" · role "), ip("AWSReservedSSO_ReadOnly_0123456789abcdef"), tp(" · SSO role directly")}},
		{"aws no entry or account", 3, []Part{tp("aws")}},
		{"github", 4, []Part{tp("github "), ip("culiops"), tp(" · 3 repos")}},
		{"static", 5, []Part{tp("token "), ip("datadog-read")}},
		{"elevation", 6, []Part{tp("write on "), ip("culipulse-prod"), tp(" · 30 min")}},
		{"signin", 7, []Part{tp("SSO sign-in "), ip("culipulse-prod")}},
		{"egress", 8, []Part{ip("registry.example.test:443")}},
		{"egress with size", 9, []Part{ip("files.example.test:443"), tp(" · 148 MB down")}},
		{"settings", 10, []Part{tp("token "), ip("e2e-pat"), tp(" created")}},
		{"settings with an empty name", 11, []Part{tp("token created")}},
		{"store value_set", 12, []Part{tp("encrypted store value set · "), ip("e2e-pat")}},
		{"store locked", 13, []Part{tp("encrypted store locked")}},
	}
	for _, c := range cases {
		e, ok := byTS[ts(c.n)]
		if !ok {
			t.Errorf("%s: no event", c.name)
			continue
		}
		if !slices.Equal(e.Parts, c.parts) {
			t.Errorf("%s: parts\n got %+v\nwant %+v", c.name, e.Parts, c.parts)
		}
		if got := joinParts(e.Parts); got != e.What {
			t.Errorf("%s: parts join to %q, What is %q", c.name, got, e.What)
		}
		if e.Kind == KindElevation && e.Reason != "rotate the key" {
			t.Errorf("%s: reason = %q", c.name, e.Reason)
		}
	}
	if len(p.Events) != len(cases) {
		t.Errorf("%d events, want %d", len(p.Events), len(cases))
	}
}

// A store unlock reads its ttl in plain words in the parts only; What (and so
// the CSV) keeps the stored detail. Anything that is not a positive duration,
// and any free-text detail, stays plain text.
func TestDescribeStoreTTL(t *testing.T) {
	s := newSeeder(t)
	for i, c := range []struct{ action, detail string }{
		{"unlocked", "ttl=168h0m0s"},
		{"unlocked", "ttl=1h30m0s"},
		{"unlocked", "ttl=bogus"},
		{"unlocked", "ttl=-5s"},
		{"unlocked", "ttl=0s"},
		{"unlocked", ""},
		{"passwd_changed", "rotated by hand"},
	} {
		if _, err := s.raw.Exec(`INSERT INTO store_audit (ts, action, actor, detail) VALUES (?, ?, 'web', ?)`,
			1_800_000_001+i, c.action, c.detail); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.open().Query(Query{From: 1_800_000_000, To: 1_800_000_010, Limit: 100})
	if err != nil || len(p.Events) != 7 {
		t.Fatalf("%+v %v", p, err)
	}
	byTS := map[int64]Event{}
	for _, e := range p.Events {
		byTS[e.TS] = e
	}
	for i, want := range []struct{ what, parts string }{
		{"encrypted store unlocked · ttl=168h0m0s", "encrypted store unlocked · for 7 days"},
		{"encrypted store unlocked · ttl=1h30m0s", "encrypted store unlocked · for 1 h 30 min"},
		{"encrypted store unlocked · ttl=bogus", "encrypted store unlocked · ttl=bogus"},
		{"encrypted store unlocked · ttl=-5s", "encrypted store unlocked · ttl=-5s"},
		{"encrypted store unlocked · ttl=0s", "encrypted store unlocked · ttl=0s"},
		{"encrypted store unlocked", "encrypted store unlocked"},
		{"encrypted store passwd changed · rotated by hand", "encrypted store passwd changed · rotated by hand"},
	} {
		e := byTS[int64(1_800_000_001+i)]
		if e.What != want.what {
			t.Errorf("row %d: What = %q, want %q", i, e.What, want.what)
		}
		if got := joinParts(e.Parts); got != want.parts {
			t.Errorf("row %d: parts join to %q, want %q", i, got, want.parts)
		}
		for _, part := range e.Parts {
			if part.ID {
				t.Errorf("row %d: id part %+v on a row with no identifier", i, part)
			}
		}
	}
}

// The backstop masker runs on the joined What. When it changes anything the
// parts are dropped, so no part can carry text that What no longer shows.
func TestDescribePartsDroppedWhenBackstopMasks(t *testing.T) {
	s := newSeeder(t)
	// A secret-key assignment split across two slots: the kind names the key,
	// the name holds "=<40 characters>". Neither slot is masked alone; only the
	// joined line is.
	kind, name := "secretaccesskey", "="+strings.Repeat("x", 40)
	if mask.Display(kind) != kind || mask.Display(name) != name {
		t.Fatal("test shape is wrong: a single slot is already masked")
	}
	if _, err := s.raw.Exec(`INSERT INTO cred_audit (ts, action, kind, name, actor) VALUES (1800000001, 'created', ?, ?, 'web')`, kind, name); err != nil {
		t.Fatal(err)
	}
	p, err := s.open().Query(Query{})
	if err != nil || len(p.Events) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	e := p.Events[0]
	if want := "secretaccesskey =[masked:aws-secret] created"; e.What != want {
		t.Errorf("What = %q, want %q", e.What, want)
	}
	if e.Parts != nil {
		t.Errorf("Parts = %+v, want nil", e.Parts)
	}
}

// A store unlock the keeper refused (#288) reads as a failure beside the
// unlocked row, in words, with the fixed reason class and no ttl rewrite.
func TestDescribeStoreUnlockNotKept(t *testing.T) {
	ev := describe(rawRow{src: srcStore, a: "unlock_failed", b: "web", c: "not kept: the key store refused the unlock"})
	if want := "encrypted store unlock failed · not kept: the key store refused the unlock"; ev.What != want {
		t.Fatalf("What = %q, want %q", ev.What, want)
	}
	if ev.Kind != KindStore || ev.Outcome != "unlock_failed" {
		t.Fatalf("%+v", ev)
	}
}
