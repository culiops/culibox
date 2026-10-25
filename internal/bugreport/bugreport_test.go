package bugreport

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/doctor"
)

func baseInput() Input {
	return Input{
		Producer: "cli", Commit: "abc1234",
		Now:  time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
		Home: "/Users/alice", Host: "mbp-alice",
		Doctor: []doctor.Result{{Status: doctor.Pass, Check: "platform", Detail: "ok"}},
	}
}

func TestMarkdownSectionsInOrder(t *testing.T) {
	in := baseInput()
	in.LogHint = "LOGHINT-XYZ"
	md := Markdown(in)
	want := []string{
		"agent-box bug report v1", "## What happened", WhatHappenedMarker,
		"## Steps to reproduce", "## Expected vs actual", "## Doctor",
		"## Config summary", "## For the agent fixing this",
		"Security problems: do NOT file a public issue — report privately to the maintainer.",
	}
	pos := 0
	for _, w := range want {
		i := strings.Index(md[pos:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", w, md)
		}
		pos += i + len(w)
	}
	if strings.Contains(md, "## Live state") {
		t.Error("Live state present for the CLI")
	}
	if !strings.Contains(md, "LOGHINT-XYZ") {
		t.Error("LogHint missing for the CLI")
	}

	in.Live = &Live{Uptime: 90 * time.Second, Sessions: map[string]int{"/running": 1, "box/exited": 2}}
	in.LogHint = "LOGHINT-XYZ"
	md = Markdown(in)
	if !strings.Contains(md, "## Live state") {
		t.Error("Live state missing for the daemon")
	}
	if strings.Contains(md, "LOGHINT-XYZ") {
		t.Error("LogHint shown although Live is set")
	}
	if !strings.Contains(md, "host/running: 1") || !strings.Contains(md, "box/exited: 2") {
		t.Errorf("session counts missing:\n%s", md)
	}
}

func TestConfigSummaryIsAllowlist(t *testing.T) {
	in := baseInput()
	in.Config = config.Config{
		BindAddr:     "203.0.113.9:9000",
		PasswordEnv:  "AB_SECRET_PW",
		DefaultCwd:   "/Users/alice/work",
		AllowedHosts: []string{"corp.example.com"},
		EnvLabel:     "DEV",
		StaticTokens: []config.StaticToken{{Name: "gh", Value: config.KeyRef{Type: "env", Var: "GH_TOKEN_REF"}}},
		AWSConnections: []config.AWSConnection{{Name: "prod", AWSBinding: config.AWSBinding{
			RoleARN: "arn:aws:iam::123456789012:role/X"}}},
		Agents: []config.AgentConfig{{Name: "seed", Bin: "/opt/seed/claude"}},
	}
	in.Profiles = []config.AgentConfig{{
		Name: "p1", Bin: "/opt/tools/claude", Runtime: "box", Base: "clean",
		Env: map[string]string{"API_KEY": "hunter2"}, Args: []string{"--secret-flag"},
		DefaultCwd: "/Users/alice/work",
	}}
	md := Markdown(in)
	for _, bad := range []string{"AB_SECRET_PW", "alice", "corp.example.com", "203.0.113.9",
		"GH_TOKEN_REF", "123456789012", "hunter2", "--secret-flag", "/opt/tools", "/opt/seed"} {
		if strings.Contains(md, bad) {
			t.Errorf("bundle leaks %q:\n%s", bad, md)
		}
	}
	for _, good := range []string{"bind: public", "static tokens: 1", "aws connections: 1",
		"agents: 1", "profiles: 1", "claude", "env: DEV"} {
		if !strings.Contains(md, good) {
			t.Errorf("bundle missing %q:\n%s", good, md)
		}
	}
}

func TestDoctorLinesRedacted(t *testing.T) {
	in := baseInput()
	in.Doctor = []doctor.Result{{Status: doctor.Warn, Check: "store", Detail: "/Users/alice/.local/share on mbp-alice"}}
	in.DoctorNote = "doctor timed out after 30s; run `agent-box bug-report` in a terminal"
	md := Markdown(in)
	if !strings.Contains(md, "~/.local/share on <host>") {
		t.Errorf("detail not redacted:\n%s", md)
	}
	if strings.Contains(md, "alice") {
		t.Errorf("leaks alice:\n%s", md)
	}
	if !strings.Contains(md, in.DoctorNote) {
		t.Error("DoctorNote missing")
	}
}

func TestLiveLogsScrubbedAndRedacted(t *testing.T) {
	in := baseInput()
	in.Producer = "daemon"
	in.Live = &Live{Logs: []string{"token=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 from /Users/alice"}}
	md := Markdown(in)
	if strings.Contains(md, "ghp_ABC") || strings.Contains(md, "alice") {
		t.Errorf("log line leaks:\n%s", md)
	}
}

func TestBindClass(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8899": "loopback", "localhost:1": "loopback", "[::1]:1": "loopback",
		"100.76.1.2:9000": "tailnet",
		"192.168.1.5:1":   "private", "10.0.0.1:1": "private", "172.16.0.1:1": "private",
		"0.0.0.0:9000": "all interfaces", ":9000": "all interfaces", "[::]:1": "all interfaces",
		"203.0.113.9:1": "public", "garbage": "public", "": "public",
		"example.com:80": "public",
	} {
		if got := BindClass(addr); got != want {
			t.Errorf("BindClass(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestIssueURL(t *testing.T) {
	in := baseInput()
	in.Doctor = []doctor.Result{
		{Status: doctor.Pass, Check: "fine", Detail: "ALLGOOD"},
		{Status: doctor.Fail, Check: "abduco", Detail: "broken at /Users/alice"},
		{Status: doctor.Warn, Check: "port", Detail: "in use"},
	}
	u := IssueURL(in)
	const base = "https://github.com/culiops/agent-box/issues/new?"
	if !strings.HasPrefix(u, base) {
		t.Fatalf("url = %q", u)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(u, base))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q.Get("title"), "bug: ") || q.Get("labels") != "bug" {
		t.Errorf("title/labels = %q / %q", q.Get("title"), q.Get("labels"))
	}
	body := q.Get("body")
	for _, w := range []string{"agent-box bug report v1", "FAIL abduco", "WARN port", "full report: paste below", "broken at ~"} {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "ALLGOOD") || strings.Contains(body, "alice") {
		t.Errorf("body has PASS line or leak:\n%s", body)
	}

	in.Doctor = nil
	for i := 0; i < 500; i++ {
		in.Doctor = append(in.Doctor, doctor.Result{Status: doctor.Fail, Check: "c", Detail: strings.Repeat("détail é ", 20)})
	}
	u = IssueURL(in)
	if len(u) >= 7000 {
		t.Fatalf("url length %d", len(u))
	}
	q, _ = url.ParseQuery(strings.TrimPrefix(u, base))
	if !strings.HasSuffix(strings.TrimSpace(q.Get("body")), "full report: paste below") {
		t.Errorf("truncated body lost its ending:\n%s", q.Get("body"))
	}
}

// Replacing the hostname before scrubbing split tokens: Host "mac" turned
// ghp_ABCmacDEF... into ghp_ABC<host>DEF..., which no pattern matches.
func TestHostnameInsideTokenDoesNotDefeatScrub(t *testing.T) {
	in := baseInput()
	in.Host = "mac"
	in.Doctor = []doctor.Result{{Status: doctor.Fail, Check: "net",
		Detail: "ghp_ABCmacDEFGHIJKLMNOPQRSTUVWXYZ0123456 and Bearer eyJmac.payloadmac.sigmac"}}
	in.Live = &Live{Logs: []string{"retry with ghp_ABCmacDEFGHIJKLMNOPQRSTUVWXYZ0123456"}}
	for name, out := range map[string]string{"markdown": Markdown(in), "issue url": IssueURL(in)} {
		if u, err := url.QueryUnescape(out); err == nil {
			out = u
		}
		for _, bad := range []string{"ghp_ABC", "DEFGHIJKL", "eyJ", "payload", "sigmac"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s leaks %q:\n%s", name, bad, out)
			}
		}
	}
}

func TestBundleSourceAndBoxImageClasses(t *testing.T) {
	for src, want := range map[string]string{"": "bundle source: auto", "off": "bundle source: off", "/opt/secret-bundle": "bundle source: custom"} {
		in := baseInput()
		in.Config.BundleSource = src
		md := Markdown(in)
		if !strings.Contains(md, want) || strings.Contains(md, "/opt/secret-bundle") {
			t.Errorf("source %q: want %q, no path:\n%s", src, want, md)
		}
	}
	const digest = "sha256:0123456789abcdef0123456789abcdef"
	for img, want := range map[string]string{
		"":                                   "box image: culiops/agent-box:base",
		"ghcr.io/culiops/agent-box:base":     "box image: ghcr.io/culiops/agent-box:base",
		"docker.io/library/debian:12":        "box image: docker.io/library/debian:12",
		"registry.corp.example.com/team/x:1": "box image: custom\n",
		"registry.corp.example.com/team/x@" + digest: "box image: custom@0123456789ab\n",
		"localhost:5000/x":                           "box image: custom\n",
		"10.0.0.5:5000/x:1":                          "box image: custom\n",
	} {
		in := baseInput()
		in.Config.BoxImage = img
		md := Markdown(in)
		if !strings.Contains(md, want) {
			t.Errorf("image %q: want %q in:\n%s", img, want, md)
		}
		for _, bad := range []string{"corp.example.com", "10.0.0.5", "localhost:5000", "team/x"} {
			if strings.Contains(md, bad) {
				t.Errorf("image %q leaks %q", img, bad)
			}
		}
	}
}

func TestMachineIdentifiersRedacted(t *testing.T) {
	in := baseInput()
	in.Host = "Buildbox.corp.example.com"
	in.Config.BindAddr = "gw-internal.example.net:9000"
	in.Config.AllowedHosts = []string{"agent.example.org:8443", "tunnel.example.io"}
	in.Config.DataDir = "/srv/agent-data"
	in.Config.ProfilesPath = "/srv/agent-data/profiles.json"
	in.Doctor = []doctor.Result{{Status: doctor.Warn, Check: "x", Detail: strings.Join([]string{
		"store /srv/agent-data/profiles.json and /srv/agent-data/db",
		"dial 203.0.113.9:443 and 10.1.2.3 and 2001:db8::1 and fe80::1ff:fe23:4567:890a",
		"host BUILDBOX / buildbox.corp.example.com up; gw-internal.example.net reached",
		"agent.example.org:8443 tunnel.example.io",
		"loopback 127.0.0.1:8899 [::1]:8899 at 12:30:45",
	}, " ")}}
	md := Markdown(in)
	for _, bad := range []string{"/srv/agent-data", "203.0.113.9", "10.1.2.3", "2001:db8", "fe80", "BUILDBOX", "buildbox",
		"corp.example.com", "gw-internal", "agent.example.org", "tunnel.example.io"} {
		if strings.Contains(md, bad) {
			t.Errorf("leaks %q:\n%s", bad, md)
		}
	}
	for _, keep := range []string{"127.0.0.1:8899", "[::1]:8899", "12:30:45", "<path>", "<ip>", "<host>"} {
		if !strings.Contains(md, keep) {
			t.Errorf("lost %q:\n%s", keep, md)
		}
	}

	// A short host name (<3 chars) is not replaced: it would mangle ordinary text.
	in = baseInput()
	in.Host = "ab.example.com"
	in.Doctor = []doctor.Result{{Status: doctor.Pass, Check: "x", Detail: "abstract table about ab.example.com"}}
	md = Markdown(in)
	if !strings.Contains(md, "abstract table about <host>") {
		t.Errorf("full host not replaced:\n%s", md)
	}
}
