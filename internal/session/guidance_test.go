package session

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/config"
)

func TestCloneGuidance(t *testing.T) {
	const head = "CuliBox: you are working in a private copy of /home/u/repo. "
	const pushSentences = `Commit on a branch, push it with "git push -u origin <branch>" and open a PR with "gh pr create"; pushing to main may be rejected by branch protection. `
	const commit = " CuliBox never commits for you: commit your work on a branch when you finish a task; uncommitted files do not come home."
	cases := []struct {
		name             string
		push, autoReturn bool
		want             string
	}{
		{"push, auto", true, true, head + pushSentences +
			"Your branches also come home to /home/u/repo as box/<branch> after each turn and when this session ends." + commit},
		{"push, no auto", true, false, head + pushSentences +
			"Your branches reach /home/u/repo as box/<branch> when the user brings them home." + commit},
		{"no push, auto", false, true, head +
			"It cannot push to GitHub from here: commit on a branch, and your branches come home to /home/u/repo as box/<branch> after each turn and when this session ends." + commit},
		{"no push, no auto", false, false, head +
			"It cannot push to GitHub from here: commit on a branch, and your branches reach /home/u/repo as box/<branch> when the user brings them home." + commit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cloneGuidance("/home/u/repo", c.push, c.autoReturn); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestCloneGuidanceCommitLine(t *testing.T) {
	const commit = " CuliBox never commits for you: commit your work on a branch when you finish a task; uncommitted files do not come home."
	for _, push := range []bool{true, false} {
		for _, auto := range []bool{true, false} {
			got := cloneGuidance("/r", push, auto)
			if !strings.HasSuffix(got, commit) {
				t.Errorf("push=%v auto=%v: %q does not end with the commit sentence", push, auto, got)
			}
			if auto != strings.Contains(got, "after each turn") {
				t.Errorf("push=%v auto=%v: %q: \"after each turn\" must appear exactly for auto-return", push, auto, got)
			}
		}
	}
}

func TestCredGuidance(t *testing.T) {
	const head = "CuliBox credentials for this session, as set up when it started (every use goes through CuliBox's credential broker: never ask the user to paste a key, and if one you need is missing, tell the user to bind it in CuliBox):"
	const list = " Run agentbox-env --list for the details, including live AWS sign-in state."
	const tail = " If a credential error says a human must sign in, stop and tell the user."
	conn := func(name string, prod bool) config.AWSConnection {
		return config.AWSConnection{Name: name, Description: "desc of " + name, AWSBinding: config.AWSBinding{
			SourceProfile: "sso-src", RoleARN: "arn:aws:iam::111122223333:role/agent-read", Region: "eu-west-1",
			Label: "Label " + name, Prod: prod}}
	}

	t.Run("no binding, no text", func(t *testing.T) {
		if got := credGuidance(config.Bindings{}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("one connection", func(t *testing.T) {
		got := credGuidance(config.Bindings{AWSConns: []config.AWSConnection{conn("sandbox", false)}})
		want := head + " AWS profiles: sandbox (use aws --profile <name>, never aws configure)." + list + tail
		if got != want {
			t.Fatalf("got  %q\nwant %q", got, want)
		}
	})
	t.Run("prod connection is marked", func(t *testing.T) {
		got := credGuidance(config.Bindings{AWSConns: []config.AWSConnection{conn("a", false), conn("b", true)}})
		if !strings.Contains(got, "AWS profiles: a, b (PROD) (") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("legacy binding is profile agentbox and has no --list", func(t *testing.T) {
		got := credGuidance(config.Bindings{AWS: &config.AWSBinding{SourceProfile: "p",
			RoleARN: "arn:aws:iam::111122223333:role/r", Region: "eu-west-1", Prod: true, Label: "x"}})
		if !strings.Contains(got, "AWS profiles: agentbox (PROD) (") || strings.Contains(got, "agentbox-env --list") {
			t.Fatalf("got %q", got)
		}
		if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("github owners, app and PAT, with write keys", func(t *testing.T) {
		got := credGuidance(config.Bindings{GitHub: &config.GitHubBinding{
			Owners: []config.GitHubOwner{
				{Owner: "culiops", App: "main", Repos: []string{"culibox"}},
				{Owner: "acme", Token: "acme-pat", Repos: []string{"infra"}},
			},
			Permissions: map[string]string{"issues": "write", "contents": "read", "metadata": "read"},
		}})
		// A PAT is not narrowed by CuliBox: its owner never gets the App's
		// permissions, so "read only" can never be said of it.
		want := " GitHub owners: culiops (git and gh; write: issues). GitHub owners with a personal access token: acme (git and gh; the token's own scopes, not narrowed by CuliBox)."
		if !strings.Contains(got, want) {
			t.Fatalf("got  %q\nwant %q", got, want)
		}
		for _, bad := range []string{"acme-pat", "main", "culibox", "infra"} {
			if strings.Contains(got, bad) {
				t.Errorf("note shows %q: %q", bad, got)
			}
		}
		if !strings.Contains(got, " Run agentbox-env --list for the details.") {
			t.Fatalf("a GitHub binding has a --list (no AWS sign-in state to show): %q", got)
		}
	})
	t.Run("a PAT-only owner is never called read only", func(t *testing.T) {
		got := credGuidance(config.Bindings{GitHub: &config.GitHubBinding{
			Owners: []config.GitHubOwner{{Owner: "acme", Token: "acme-pat", Repos: []string{"infra"}}}}})
		if strings.Contains(got, "read only") || strings.Contains(got, "GitHub owners: acme") ||
			!strings.Contains(got, "GitHub owners with a personal access token: acme (") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("legacy binding with a token: --list does not cover profile agentbox", func(t *testing.T) {
		got := credGuidance(config.Bindings{AWS: &config.AWSBinding{SourceProfile: "p",
			RoleARN: "arn:aws:iam::111122223333:role/r", Region: "eu-west-1"},
			Tokens: []config.StaticToken{{Name: "grafana", Env: []string{"G"}}}})
		if !strings.Contains(got, " Run agentbox-env --list for the details; profile agentbox is not in it.") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("github default permissions are read only", func(t *testing.T) {
		got := credGuidance(config.Bindings{GitHub: &config.GitHubBinding{
			Owners: []config.GitHubOwner{{Owner: "culiops", App: "main", Repos: []string{"culibox"}}}}})
		if !strings.Contains(got, " GitHub owners: culiops (git and gh; read only).") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("tokens show names and prod, never label, description or env", func(t *testing.T) {
		got := credGuidance(config.Bindings{Tokens: []config.StaticToken{{Name: "sentry-prod", Prod: true,
			Label: "Sentry", Description: "secret-ish desc", Env: []string{"SENTRY_TOKEN"}}}})
		if !strings.Contains(got, " Tokens: sentry-prod (PROD) (run agentbox-env <name> -- <command>).") {
			t.Fatalf("got %q", got)
		}
		for _, bad := range []string{"Sentry", "secret-ish", "SENTRY_TOKEN"} {
			if strings.Contains(got, bad) {
				t.Errorf("note shows %q: %q", bad, got)
			}
		}
	})
	t.Run("PATs are never listed as tokens", func(t *testing.T) {
		got := credGuidance(config.Bindings{
			GitHub: &config.GitHubBinding{Owners: []config.GitHubOwner{{Owner: "acme", Token: "acme-pat", Repos: []string{"infra"}}}},
			PATs:   []config.StaticToken{{Name: "acme-pat", Env: []string{"GH_TOKEN"}}},
		})
		if strings.Contains(got, "Tokens:") || strings.Contains(got, "acme-pat") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("at most 8 names per kind", func(t *testing.T) {
		var cs []config.AWSConnection
		for i := range 10 {
			cs = append(cs, conn(fmt.Sprintf("c%d", i), false))
		}
		got := credGuidance(config.Bindings{AWSConns: cs})
		if !strings.Contains(got, "AWS profiles: c0, c1, c2, c3, c4, c5, c6, c7, and 2 more (use") || strings.Contains(got, "c8") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("never an account, role, label or description", func(t *testing.T) {
		got := credGuidance(config.Bindings{AWSConns: []config.AWSConnection{conn("sandbox", true)}})
		for _, bad := range []string{"111122223333", "arn:", "agent-read", "Label", "desc of", "sso-src", "eu-west-1"} {
			if strings.Contains(got, bad) {
				t.Errorf("note shows %q: %q", bad, got)
			}
		}
	})
}

// #360 (D5): a Host session is told about its credentials only when they
// really are in place, with one extra line saying it is on the host.
func TestHostNoteOnlyWhenCredsReady(t *testing.T) {
	t.Run("bound, node present", func(t *testing.T) {
		h := newHostFlagHarness(t, true, nil)
		s, args := spawnHostFlags(t, h)
		want := credGuidance(s.Bindings()) + " " + hostCredLine
		if got := argAfter(args, "--append-system-prompt"); got != want {
			t.Fatalf("note = %q\nwant   %q", got, want)
		}
		if n := strings.Count(strings.Join(args, "\n"), "--append-system-prompt"); n != 1 {
			t.Fatalf("%d notes in %q", n, args)
		}
	})
	t.Run("bound, node missing: no bindings, no note", func(t *testing.T) {
		h := newHostFlagHarness(t, false, nil, "mcp-grafana")
		s, args := spawnHostFlags(t, h)
		if s.HostBroker || slices.Contains(args, "--append-system-prompt") || slices.Contains(args, "--mcp-config") {
			t.Fatalf("host broker=%v args=%q, want a plain Host run", s.HostBroker, args)
		}
		if _, ok := h.hooks.hostMCPDoc(s.ID); ok {
			t.Fatal("an mcp.json was written for a session without bindings")
		}
		if *h.builds != 1 {
			t.Fatalf("Build ran %d times for a session with nothing to add", *h.builds)
		}
	})
	t.Run("box: the note is as before, with no Host line", func(t *testing.T) {
		m, boxes, _ := mcpManager(t, "claude", account.DefaultResolved(""), grafanaEntry())
		spawnTok(t, m)
		if got, want := argAfter(boxes.lastArgv, "--append-system-prompt"), credGuidance(config.Bindings{Tokens: []config.StaticToken{grafanaEntry()}}); got != want {
			t.Fatalf("note = %q\nwant   %q", got, want)
		}
		if slices.ContainsFunc(boxes.lastArgv, func(a string) bool { return strings.Contains(a, "runs on the host") }) {
			t.Fatalf("a box was told it runs on the host: %q", boxes.lastArgv)
		}
	})
	t.Run("the line", func(t *testing.T) {
		if hostCredLine != "This session runs on the host, not in a box: these are the default credentials, not a limit. Use them, not this machine's own logins." {
			t.Fatalf("hostCredLine = %q", hostCredLine)
		}
	})
}
