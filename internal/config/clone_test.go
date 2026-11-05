package config

import (
	"strings"
	"testing"
)

func TestParseShare(t *testing.T) {
	ok := map[string]string{
		"CLAUDE.md":        "CLAUDE.md",
		"docs/superpowers": "docs/superpowers",
	}
	for in, want := range ok {
		got, err := ParseShare(in)
		if err != nil || got != want {
			t.Errorf("ParseShare(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseShare("docs/superpowers:rw"); err == nil || !strings.Contains(err.Error(), "read-only (D12)") {
		t.Errorf("rw suffix: want the D12 message, got %v", err)
	}
	for _, in := range []string{"", "/abs", "a/../b", "./a", "a/", ".", ".git", ".git/hooks", "a:ro", "a:rw:rw", "\xff"} {
		if got, err := ParseShare(in); err == nil {
			t.Errorf("ParseShare(%q) = %q, nil; want an error", in, got)
		}
	}
}

func cloneAgent() AgentConfig {
	return AgentConfig{
		Name: "p", Bin: "claude",
		CloneOf:    "/r/agent-box",
		DefaultCwd: "/c/agent-box",
		CloneShare: []string{"CLAUDE.md", "docs/superpowers"},
	}
}

func TestValidateClone(t *testing.T) {
	if err := validateClone(cloneAgent()); err != nil {
		t.Fatalf("valid clone profile refused: %v", err)
	}
	if err := validateClone(AgentConfig{Name: "p", Bin: "claude"}); err != nil {
		t.Fatalf("non-clone profile refused: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*AgentConfig)
		want string
	}{
		{"share without of", func(a *AgentConfig) { a.CloneOf = "" }, "clone_share"},
		{"relative of", func(a *AgentConfig) { a.CloneOf = "r/agent-box" }, "clone_of"},
		{"unclean of", func(a *AgentConfig) { a.CloneOf = "/r/../r/agent-box" }, "clone_of"},
		{"missing default_cwd", func(a *AgentConfig) { a.DefaultCwd = "" }, "default_cwd"},
		{"relative default_cwd", func(a *AgentConfig) { a.DefaultCwd = "c/x" }, "default_cwd"},
		{"cwd == of", func(a *AgentConfig) { a.DefaultCwd = "/r/agent-box" }, "overlap"},
		{"cwd inside of", func(a *AgentConfig) { a.DefaultCwd = "/r/agent-box/clone" }, "overlap"},
		{"cwd contains of", func(a *AgentConfig) { a.DefaultCwd = "/r" }, "overlap"},
		{"runtime host", func(a *AgentConfig) { a.Runtime = "host" }, "clone mode runs only in a box"},
		{"nine shares", func(a *AgentConfig) {
			a.CloneShare = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "at most 8"},
		{"duplicate", func(a *AgentConfig) { a.CloneShare = []string{"a", "a"} }, "duplicate"},
		{"nested", func(a *AgentConfig) { a.CloneShare = []string{"docs", "docs/superpowers"} }, "nest"},
		{"nested reversed", func(a *AgentConfig) { a.CloneShare = []string{"docs/superpowers", "docs"} }, "nest"},
		{"rw suffix", func(a *AgentConfig) { a.CloneShare = []string{"docs/superpowers:rw"} }, "read-only (D12)"},
		{"bad share", func(a *AgentConfig) { a.CloneShare = []string{".git"} }, "clone_share"},
	}
	for _, c := range cases {
		a := cloneAgent()
		c.mut(&a)
		err := validateClone(a)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v; want error containing %q", c.name, err, c.want)
		}
	}
	// A sibling that merely shares a name prefix is not an overlap.
	a := cloneAgent()
	a.DefaultCwd = "/r/agent-box-clone"
	if err := validateClone(a); err != nil {
		t.Errorf("prefix-sibling dirs refused: %v", err)
	}
	a = cloneAgent()
	a.CloneShare = []string{"docs", "docs2"}
	if err := validateClone(a); err != nil {
		t.Errorf("prefix-sibling shares refused: %v", err)
	}
}

func TestCloneAccessor(t *testing.T) {
	if c, err := (AgentConfig{Name: "p", Bin: "claude"}).Clone(); c != nil || err != nil {
		t.Fatalf("no clone_of: got %v, %v; want nil, nil", c, err)
	}
	c, err := cloneAgent().Clone()
	if err != nil || c == nil {
		t.Fatalf("Clone: %v, %v", c, err)
	}
	if c.Of != "/r/agent-box" || c.Dir != "/c/agent-box" || len(c.Shares) != 2 || c.Shares[1] != "docs/superpowers" {
		t.Errorf("Clone = %+v", c)
	}
	bad := cloneAgent()
	bad.Runtime = "host"
	if c, err := bad.Clone(); err == nil || c != nil {
		t.Errorf("invalid clone profile: got %v, %v; want an error", c, err)
	}
}

func TestCloneHash(t *testing.T) {
	base := Clone{Of: "/r/a", Dir: "/c/a", Shares: []string{"x", "y"}}
	h := base.Hash()
	if len(h) != 16 {
		t.Fatalf("hash %q: want 16 hex chars", h)
	}
	if got := (Clone{Of: "/r/a", Dir: "/c/a", Shares: []string{"y", "x"}}).Hash(); got != h {
		t.Errorf("share order changed the hash: %s vs %s", got, h)
	}
	if base.Shares[0] != "x" {
		t.Errorf("Hash reordered the caller's slice")
	}
	for name, c := range map[string]Clone{
		"added share": {Of: "/r/a", Dir: "/c/a", Shares: []string{"x", "y", "z"}},
		"other of":    {Of: "/r/b", Dir: "/c/a", Shares: []string{"x", "y"}},
		"other dir":   {Of: "/r/a", Dir: "/c/b", Shares: []string{"x", "y"}},
	} {
		if c.Hash() == h {
			t.Errorf("%s did not change the hash", name)
		}
	}
}

func TestShareWarnings(t *testing.T) {
	for _, s := range []string{".env.prod", "certs/x.pem", "id.key", "config.prod.json"} {
		w := ShareWarnings([]string{s})
		if len(w) != 1 || !strings.Contains(w[0], `clone_share "`+s+`" looks like a secret or config file; the box will be able to read it`) {
			t.Errorf("%s: warnings = %v", s, w)
		}
	}
	if w := ShareWarnings([]string{"docs", "CLAUDE.md", "docs/superpowers"}); len(w) != 0 {
		t.Errorf("benign shares warned: %v", w)
	}
}

func TestValidateRunsCloneChecksForSeedsAndProfiles(t *testing.T) {
	a := cloneAgent()
	a.Runtime = "host"
	if err := ValidateProfile(a, nil); err == nil || !strings.Contains(err.Error(), "clone mode runs only in a box") {
		t.Errorf("ValidateProfile: %v", err)
	}
	if err := (Config{Agents: []AgentConfig{a}}).Validate(); err == nil || !strings.Contains(err.Error(), "clone mode runs only in a box") {
		t.Errorf("Config.Validate: %v", err)
	}
}

func TestCloneRefusesControlCharacters(t *testing.T) {
	for _, in := range []string{"a\nb", "a\x00b", "a\tb", "a\x1bb", "a\x7fb", "\n"} {
		if _, err := ParseShare(in); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Errorf("ParseShare(%q) = %v; want a control-character error", in, err)
		}
	}
	for _, field := range []string{"clone_of", "default_cwd"} {
		for _, bad := range []string{"/r/a\nb", "/r/a\x00b", "/r/a\x7fb", "/r/a\rb"} {
			a := cloneAgent()
			if field == "clone_of" {
				a.CloneOf = bad
			} else {
				a.DefaultCwd = bad
			}
			err := validateClone(a)
			if err == nil || !strings.Contains(err.Error(), "control character") {
				t.Errorf("%s %q: want a control-character error, got %v", field, bad, err)
			}
		}
	}
}

func TestCloneCarriesNoAutoReturn(t *testing.T) {
	a := AgentConfig{Name: "p", CloneOf: "/src/of", DefaultCwd: "/work/c", CloneNoAutoReturn: true}
	c, err := a.Clone()
	if err != nil || c == nil || !c.NoAutoReturn {
		t.Fatalf("Clone() = %+v, %v", c, err)
	}
}

func TestNoAutoReturnDoesNotChangeTheBoxHash(t *testing.T) {
	a := Clone{Of: "/src/of", Dir: "/work/c"}
	b := a
	b.NoAutoReturn = true
	if a.Hash() != b.Hash() {
		t.Fatal("NoAutoReturn changed Clone.Hash")
	}
}

func TestNoAutoReturnRequiresCloneOf(t *testing.T) {
	err := validateClone(AgentConfig{Name: "p", CloneNoAutoReturn: true})
	if err == nil || !strings.Contains(err.Error(), "clone_no_auto_return requires clone_of") {
		t.Fatalf("err = %v", err)
	}
}

// On macOS APFS ignores case, so /Users/me/App/box is inside /Users/me/app.
// A plain string compare let a clone land inside the repo it copies.
func TestPathOverlapFoldsCase(t *testing.T) {
	if !pathOverlapFold("/Users/me/app", "/Users/me/App/box", true) {
		t.Error("fold: case-variant child not seen as overlap")
	}
	if !pathOverlapFold("/Users/me/App", "/users/me/app", true) {
		t.Error("fold: case-variant equal paths not seen as overlap")
	}
	if pathOverlapFold("/Users/me/app", "/Users/me/App-box", true) {
		t.Error("fold: prefix sibling seen as overlap")
	}
	if pathOverlapFold("/Users/me/app", "/Users/me/App/box", false) {
		t.Error("no fold: case-variant paths must stay distinct on a case-sensitive host")
	}
}

func TestValidateCloneRefusesCaseVariantOverlapWhenFolding(t *testing.T) {
	old := foldPaths
	foldPaths = true
	t.Cleanup(func() { foldPaths = old })
	a := cloneAgent()
	a.DefaultCwd = strings.ToUpper(a.CloneOf) + "/clone"
	if err := validateClone(a); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("case-variant clone inside clone_of: got %v; want overlap refusal", err)
	}
}
