package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportWarnings(t *testing.T) {
	nameWarn := func(n string) string {
		return n + " has a name that looks like a secret; the box can read and send it"
	}
	for _, n := range []string{"secrets.zsh", "api_key", ".env.local", "gh_key", "cf_key", "db-password.md", "my_token.txt"} {
		got := importWarnings(n, []byte("hello\n"))
		if len(got) != 1 || got[0] != nameWarn(n) {
			t.Errorf("name %q: got %q", n, got)
		}
	}
	if got := importWarnings("projects.md", []byte("# projects\n")); len(got) != 0 {
		t.Errorf("projects.md: unexpected %q", got)
	}

	tok := "ghp_" + strings.Repeat("a1B2", 9)
	contentCases := []struct {
		name, body, want string
	}{
		{"tok", "a\nb\nkey " + tok + "\n", "tok.md line 3 looks like a credential (a value shaped like an API token); the box can read and send it"},
		{"pem", "x\n-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n", "pem.md line 2 looks like a credential (a private key); the box can read and send it"},
		{"asg", "export CF_TOKEN=abcd1234efgh5678\n", "asg.md line 1 looks like a credential (an assignment to CF_TOKEN); the box can read and send it"},
		{"plain", "  PASSWORD=hunter2hunter2\n", "plain.md line 1 looks like a credential (an assignment to PASSWORD); the box can read and send it"},
	}
	for _, c := range contentCases {
		got := importWarnings(c.name+".md", []byte(c.body))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		for _, w := range got {
			if strings.Contains(w, tok) || strings.Contains(w, "abcd1234efgh5678") || strings.Contains(w, "hunter2") {
				t.Errorf("%s: warning echoes the value", c.name)
			}
		}
	}

	// At most one content warning per file: the first hit wins.
	two := "export A_TOKEN=abcd1234efgh5678\nexport B_TOKEN=zzzz1234efgh5678\n"
	if got := importWarnings("two.md", []byte(two)); len(got) != 1 || !strings.Contains(got[0], "line 1") {
		t.Errorf("two hits: got %q", got)
	}
	// A secret name and a secret body give both warnings, name first.
	if got := importWarnings("secrets.zsh", []byte("export CF_TOKEN=abcd1234efgh5678\n")); len(got) != 2 {
		t.Errorf("name+content: got %q", got)
	}

	for _, body := range []string{
		"uses `$CLOUDFLARE_API_TOKEN`\n",
		"export X=$Y\n",
		"export TOKEN_FILE=~/.config/t\n",
		"export API_TOKEN=$OTHER\n",
		"export API_TOKEN=~/.config/culi/token\n",
		"a risk-free-tier-limits-apply note\n",
		"see task-" + strings.Repeat("a", 30) + "\n",
		"PATH=/usr/bin\n",
	} {
		if got := importWarnings("note.md", []byte(body)); len(got) != 0 {
			t.Errorf("body %q: unexpected %q", body, got)
		}
	}
}

func TestImportWarningsRealInfra(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "context", "infra.md"))
	if err != nil {
		t.Skip("no ~/.claude/context/infra.md")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "infra.md")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := importWarnings("infra.md", fixture); len(got) != 0 {
		t.Errorf("real infra.md warns: %d warning(s), first %q", len(got), got[0])
	}
}

func TestResolveImportsFillsWarnings(t *testing.T) {
	home, src := impFixture(t, t.TempDir())
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@secrets.zsh\n@ok.md\n")
	impWrite(t, filepath.Join(src, "secrets.zsh"), "export X=1\n")
	impWrite(t, filepath.Join(src, "ok.md"), "fine\n")
	s := resolveImports(importOpts{Source: src, HostHome: home})
	var got = map[string]int{}
	for _, f := range s.Files {
		got[f.Name] = len(f.Warnings)
	}
	if len(s.Files) != 2 || got["secrets.zsh"] != 1 || got["ok.md"] != 0 {
		t.Errorf("files/warnings: %v (files %d, refused %v)", got, len(s.Files), s.Refused)
	}
}
