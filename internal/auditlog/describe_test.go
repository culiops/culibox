package auditlog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
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

	rec(s.b.Record(broker.Row{Time: at(1), SessionID: "s1", AccountID: "226198813800", RoleARN: "arn:aws:iam::226198813800:role/team/agent-ops",
		Outcome: broker.OutcomeMinted, Elevated: true, Entry: "culipulse-prod"}))
	rec(s.b.Record(broker.Row{Time: at(2), SessionID: "s1", AccountID: "226198813800", Outcome: broker.OutcomeMinted})) // no entry, no role
	rec(s.b.Record(broker.Row{Time: at(3), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 3, Outcome: broker.OutcomeMinted}))
	rec(s.b.Record(broker.Row{Time: at(4), SessionID: "s1", Provider: broker.ProviderGitHub, Subject: "culiops", Repos: 1, Outcome: broker.OutcomeMinted}))
	rec(s.b.Record(broker.Row{Time: at(5), SessionID: "s1", Provider: broker.ProviderStatic, Entry: "datadog-read", Outcome: broker.OutcomeFetched}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(6), SessionID: "s1", Action: broker.ActionGranted, Entry: "culipulse-prod", Minutes: 30, Reason: "rotate the key", Cred: "cookie"}))
	rec(s.b.RecordApproval(broker.ApprovalRow{Time: at(7), SessionID: "s1", Action: broker.ActionExtended, AccountID: "226198813800", Minutes: 15, Cred: "bearer"}))
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

	p, err := s.open().Query(Query{From: ts(1), To: ts(18), Limit: 100})
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
		{2, "credential", "aws 226198813800", "minted", "", "", 0, false},
		{3, "credential", "github culiops · 3 repos", "minted", "", "", 0, false},
		{4, "credential", "github culiops · 1 repo", "minted", "", "", 0, false},
		{5, "credential", "token datadog-read", "fetched", "", "", 0, false},
		{6, "elevation", "write on culipulse-prod · 30 min", "granted", "you", "rotate the key", 0, false},
		{7, "elevation", "write on 226198813800 · 15 min", "extended", "API token", "", 0, false},
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
