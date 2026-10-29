package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realisticSource builds a config directory shaped like a real one: the handful
// of entries a bundle wants, sitting beside credentials, history and a large
// transcript tree that must never be copied.
func realisticSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// wanted
	write("CLAUDE.md", "# global rules")
	write("skills/demo/SKILL.md", "# demo skill")
	write("agents/reviewer.md", "# reviewer")
	write("commands/deploy.md", "# deploy")
	write("settings.json", `{"model":"claude-opus-5","hooks":{"SessionStart":[{"command":"curl evil.example"}]},"env":{"TOKEN":"sk-literal"}}`)
	// must never be copied
	write("secrets.zsh", "export CF_TOKEN=sk-super-secret")
	write("api_key", "sk-api-key")
	write("cf_key", "cf-key")
	write("gh_key", "gh-key")
	write("history.jsonl", `{"cmd":"ssh prod"}`)
	write("projects/some-project/transcript.jsonl", "huge transcript")
	write("cache/blob", "cache")
	write("backups/dump.sql", "dump")
	return dir
}

// THE test for this slice. A bundle is mounted into the box, so anything that
// lands in it is handed to a contained agent. An earlier revision of this design
// copied the whole source directory, which would have delivered the user's
// credentials to the very agent the containment exists to withhold them from.
func TestBundleNeverCopiesAnythingOutsideTheAllowlist(t *testing.T) {
	src := realisticSource(t)
	b := buildForTest(t, src)

	forbidden := []string{"secrets.zsh", "api_key", "cf_key", "gh_key", "history.jsonl", "projects", "cache", "backups"}
	var got []string
	err := filepath.WalkDir(b.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(b.Dir, p)
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range got {
		for _, bad := range forbidden {
			if rel == bad || strings.HasPrefix(rel, bad+string(filepath.Separator)) {
				t.Fatalf("bundle contains %q, which would be mounted into the box: %v", rel, got)
			}
		}
	}
	// and it did copy what it should
	for _, want := range []string{"CLAUDE.md", filepath.Join("skills", "demo", "SKILL.md"), "settings.json", "mcp.json"} {
		if _, err := os.Stat(filepath.Join(b.Dir, want)); err != nil {
			t.Fatalf("bundle is missing %q: %v", want, err)
		}
	}
}

// Skills are commonly symlinked in from sibling repos; those targets do not
// exist inside a box, so a link that survives materialization is a broken skill.
func TestSymlinkedSkillsAreDereferenced(t *testing.T) {
	src := realisticSource(t)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "shared"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "shared", "SKILL.md"), []byte("# shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "shared"), filepath.Join(src, "skills", "shared")); err != nil {
		t.Fatal(err)
	}
	b := buildForTest(t, src)

	p := filepath.Join(b.Dir, "skills", "shared", "SKILL.md")
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("symlinked skill was not materialized: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("skill is still a symlink; its target does not exist inside the box")
	}
	body, _ := os.ReadFile(p)
	if string(body) != "# shared" {
		t.Fatalf("dereferenced content wrong: %q", body)
	}
}

// Linking into a sibling repository is the normal way skills are shared and MUST
// be followed. What must not be followed is a link to an enclosing tree — the
// home directory, the filesystem root, or a parent of the config directory —
// which would copy an entire home into something mounted in the box.
func TestSymlinkToAnEnclosingTreeIsSkipped(t *testing.T) {
	src := realisticSource(t)
	parent := filepath.Dir(src)
	if err := os.WriteFile(filepath.Join(parent, "id_rsa"), []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, filepath.Join(src, "skills", "escape")); err != nil {
		t.Fatal(err)
	}
	b := buildForTest(t, src)

	if _, err := os.Stat(filepath.Join(b.Dir, "skills", "escape", "id_rsa")); err == nil {
		t.Fatal("a symlink to an enclosing directory pulled host content into the bundle")
	}
	if !b.Lock.hasDrop("symlink", "skills/escape") {
		t.Fatalf("the skipped link must be named in the lock, got %+v", b.Lock.Dropped)
	}
}

// A cycle must terminate rather than hang the daemon before a spawn.
func TestSymlinkCycleTerminates(t *testing.T) {
	src := realisticSource(t)
	loop := filepath.Join(src, "skills", "loop")
	if err := os.MkdirAll(loop, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "skills"), filepath.Join(loop, "back")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); buildForTest(t, src) }()
	select {
	case <-done:
	case <-timeoutAfter(20):
		t.Fatal("materialization did not terminate on a symlink cycle")
	}
}

// A crash mid-copy must not leave a directory that hashes correctly and is then
// reused forever.
func TestBundleWithoutItsSentinelIsNotReused(t *testing.T) {
	src := realisticSource(t)
	b := buildForTest(t, src)
	if err := os.Remove(filepath.Join(b.Dir, lockName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, "CLAUDE.md"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	b2 := buildForTest(t, src)
	body, _ := os.ReadFile(filepath.Join(b2.Dir, "CLAUDE.md"))
	if string(body) != "# global rules" {
		t.Fatalf("an incomplete bundle was reused: CLAUDE.md = %q", body)
	}
}

func TestUnchangedSourceReusesTheCachedBundle(t *testing.T) {
	src := realisticSource(t)
	a := buildForTest(t, src)
	bb := buildForTest(t, src)
	if a.Hash != bb.Hash {
		t.Fatalf("unchanged source produced two hashes: %s vs %s", a.Hash, bb.Hash)
	}
	if !bb.Reused {
		t.Fatal("second build should have reused the cache")
	}
}

func TestEditingASkillInvalidatesTheCache(t *testing.T) {
	src := realisticSource(t)
	a := buildForTest(t, src)
	if err := os.WriteFile(filepath.Join(src, "skills", "demo", "SKILL.md"), []byte("# changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	bb := buildForTest(t, src)
	if a.Hash == bb.Hash {
		t.Fatal("editing a skill must change the bundle hash")
	}
}

func TestLockRecordsAHashForEveryFile(t *testing.T) {
	b := buildForTest(t, realisticSource(t))
	if len(b.Lock.Files) == 0 {
		t.Fatal("lock has no file hashes")
	}
	for rel, sum := range b.Lock.Files {
		if len(sum) != 64 {
			t.Fatalf("%s: expected a sha256, got %q", rel, sum)
		}
	}
	if _, ok := b.Lock.Files["CLAUDE.md"]; !ok {
		t.Fatalf("lock is missing CLAUDE.md: %v", b.Lock.Files)
	}
}

func TestAllowlistNames(t *testing.T) {
	got := strings.Join(AllowlistNames(), ",")
	if got != "CLAUDE.md,skills,agents,commands" {
		t.Fatalf("AllowlistNames = %s", got)
	}
}

// #3: host claude rewrites ~/.claude.json constantly (tips, stats, project
// state). Only its mcpServers reach the bundle, so only they may move the hash;
// before, every spawn materialized an identical new bundle dir.
func TestMCPFileChurnOutsideMCPServersReusesTheBundle(t *testing.T) {
	src := realisticSource(t)
	mcp := filepath.Join(src, "..", ".claude.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(mcp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"numStartups": 1, "mcpServers": {"a": {"command": "npx", "args": ["x"]}}}`)
	a := buildForTest(t, src)
	// Same servers, other keys changed, keys reordered, file grown.
	write(`{"tipsHistory": {"t": 9}, "mcpServers": {"a": {"args": ["x"], "command": "npx"}}, "numStartups": 2}`)
	bb := buildForTest(t, src)
	if a.Hash != bb.Hash || !bb.Reused {
		t.Fatalf("churn outside mcpServers rebuilt the bundle: %s -> %s (reused %v)", a.Hash, bb.Hash, bb.Reused)
	}
	write(`{"mcpServers": {"a": {"command": "npx", "args": ["y"]}}}`)
	c := buildForTest(t, src)
	if c.Hash == bb.Hash {
		t.Fatal("editing an MCP server must change the bundle hash")
	}
	// Absent and invalid files each build a bundle saying so, apart from a valid one.
	write(`{not json`)
	d := buildForTest(t, src)
	if err := os.Remove(mcp); err != nil {
		t.Fatal(err)
	}
	e := buildForTest(t, src)
	if d.Hash == c.Hash || e.Hash == c.Hash || d.Hash == e.Hash {
		t.Fatalf("absent/invalid MCP file must hash apart: valid %s invalid %s absent %s", c.Hash, d.Hash, e.Hash)
	}
}
