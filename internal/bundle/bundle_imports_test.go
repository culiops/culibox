package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
)

// importsManager builds a Manager over <tmp>/home/.claude whose CLAUDE.md imports
// context/a.md, plus a refused import (an absolute path outside the home).
func importsManager(t *testing.T, extra ...string) (m *Manager, home, src string) {
	t.Helper()
	home, src = impFixture(t, t.TempDir())
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "# rules\n@~/.claude/context/a.md\n"+strings.Join(extra, "\n")+"\n")
	impWrite(t, filepath.Join(src, "context", "a.md"), "AAA")
	var err error
	m, err = New(Options{
		Source:   src,
		Root:     t.TempDir(),
		MCPFile:  filepath.Join(home, ".claude.json"),
		Prober:   fakeProber{"npx": true, "node": true},
		HostHome: home,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, home, src
}

func TestEnsureBundlesImports(t *testing.T) {
	m, _, _ := importsManager(t, "@/etc/passwd")
	b, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lock.Imports) != 1 || b.Lock.Imports[0] != (LockImport{Box: "/home/agent/.claude/context/a.md", SHA256: impSum("AAA")}) {
		t.Fatalf("lock imports = %+v", b.Lock.Imports)
	}
	got, err := os.ReadFile(filepath.Join(importsDir(b.Dir), "home/agent/.claude/context/a.md"))
	if err != nil || string(got) != "AAA" {
		t.Fatalf("imported bytes = %q, %v", got, err)
	}
	fi, err := os.Stat(filepath.Join(importsDir(b.Dir), "home/agent/.claude/context/a.md"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi, err)
	}
	// The imports are beside the bundle, never in the dir mounted at /opt/agent-bundle.
	filepath.Walk(b.Dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "a.md" {
			t.Errorf("import file inside the bundle dir: %s", p)
		}
		return nil
	})
	var drop *Drop
	for i, d := range b.Lock.Dropped {
		if d.Kind == KindImport {
			drop = &b.Lock.Dropped[i]
		}
	}
	if drop == nil || drop.Name != "/etc/passwd" || drop.Reason != ReasonImportOutsideHome {
		t.Fatalf("drops = %+v", b.Lock.Dropped)
	}
}

func TestEnsureRebuildsWhenAnImportChanges(t *testing.T) {
	m, _, src := importsManager(t)
	a, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Ensure()
	if !b.Reused || b.Dir != a.Dir {
		t.Fatalf("untouched source must reuse: %v %s %s", b.Reused, a.Dir, b.Dir)
	}
	impWrite(t, filepath.Join(src, "context", "a.md"), "BBBB")
	c, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if c.Reused || c.Hash == a.Hash || c.Dir == a.Dir {
		t.Fatalf("a changed import must make a new bundle: %+v", c)
	}
	if got, _ := os.ReadFile(filepath.Join(importsDir(c.Dir), "home/agent/.claude/context/a.md")); string(got) != "BBBB" {
		t.Fatalf("new import bytes = %q", got)
	}
	// The old bundle's imports stay: a live box keeps its old mounts, and no
	// bundle dir is ever removed.
	if got, _ := os.ReadFile(filepath.Join(importsDir(a.Dir), "home/agent/.claude/context/a.md")); string(got) != "AAA" {
		t.Fatalf("old import bytes = %q", got)
	}
}

func TestEnsureRebuildsWhenImportsDirMissing(t *testing.T) {
	m, _, _ := importsManager(t)
	a, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(importsDir(a.Dir)); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if b.Reused {
		t.Fatal("a lock whose imports dir is gone must not be reused")
	}
	if _, err := os.Stat(importsDir(b.Dir)); err != nil {
		t.Fatalf("imports dir not rebuilt: %v", err)
	}
}

func TestEnsureWithoutImportsMakesNoImportsDir(t *testing.T) {
	b := buildForTest(t, realisticSource(t))
	if len(b.Lock.Imports) != 0 {
		t.Fatalf("imports = %+v", b.Lock.Imports)
	}
	if _, err := os.Lstat(importsDir(b.Dir)); !os.IsNotExist(err) {
		t.Fatalf("imports dir should not exist: %v", err)
	}
}

// Claude Code's OAuth credentials do not match the credentials* secret glob (the
// leading dot), so the manager denies them by name. The ~/.claude spelling is
// already refused by spelling (the in-box path is reserved); the absolute
// spelling lands on a path that is not reserved, so only the deny catches it.
func TestImportsNeverBundleClaudeCredentials(t *testing.T) {
	m, home, src := importsManager(t)
	cred := filepath.Join(src, ".credentials.json")
	impWrite(t, cred, `{"claudeAiOauth":{"accessToken":"x"}}`)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.claude/.credentials.json\n@"+cred+"\n@"+filepath.Join(home, ".claude", "..", ".claude", ".credentials.json")+"\n")
	set := m.Imports()
	if len(set.Files) != 0 {
		t.Fatalf("credentials bundled: %v", impBoxes(set))
	}
	if got := impRefusal(set, "~/.claude/.credentials.json"); got == "" {
		t.Fatalf("tilde spelling not refused: %+v", set.Refused)
	}
	for _, imp := range []string{cred, filepath.Join(home, ".claude", "..", ".claude", ".credentials.json")} {
		if got := impRefusal(set, imp); got != ReasonImportDenied {
			t.Fatalf("%s: reason = %q, refused = %+v", imp, got, set.Refused)
		}
	}
	b, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lock.Imports) != 0 {
		t.Fatalf("lock imports = %+v", b.Lock.Imports)
	}
}

func TestImportsHonoursImportDeny(t *testing.T) {
	m, home, _ := importsManager(t, "@~/state/x.md")
	impWrite(t, filepath.Join(home, "state", "x.md"), "S")
	m.opts.ImportDeny = []string{filepath.Join(home, "state")}
	if got := impRefusal(m.Imports(), "~/state/x.md"); got != ReasonImportDenied {
		t.Fatalf("reason = %q", got)
	}
}

func importMount(dir string) box.Mount {
	return box.Mount{
		Host:     filepath.Join(importsDir(dir), "home/agent/.claude/context/a.md"),
		Box:      "/home/agent/.claude/context/a.md",
		Optional: true,
		Yield:    true,
	}
}

func hasMount(g Grant, want box.Mount) bool {
	for _, mt := range g.Mounts {
		if mt == want {
			return true
		}
	}
	return false
}

func TestProviderForImports(t *testing.T) {
	m, _, _ := importsManager(t, "@/etc/passwd")
	p := NewProvider(m)
	built, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	want := importMount(built.Dir)

	g, err := p.For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMount(g, want) || g.Imports != 1 {
		t.Fatalf("imports=%d mounts=%+v", g.Imports, g.Mounts)
	}
	drops := 0
	for _, d := range g.Drops {
		if d.Kind == KindImport {
			drops++
		}
	}
	if drops != 1 {
		t.Fatalf("import drops = %d: %+v", drops, g.Drops)
	}

	// Mounts belong to the box, not the bin.
	gb, err := p.For("bash", true)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMount(gb, want) || gb.Imports != 1 {
		t.Fatalf("bash: imports=%d mounts=%+v", gb.Imports, gb.Mounts)
	}

	off, err := p.For("claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if hasMount(off, want) || off.Imports != 0 {
		t.Fatalf("imports off but mounted: %+v", off)
	}
	for _, mt := range off.Mounts {
		if strings.HasPrefix(mt.Host, importsDir(built.Dir)) {
			t.Fatalf("imports off but %+v", mt)
		}
	}
	for _, d := range off.Drops {
		if d.Kind == KindImport {
			t.Fatalf("imports off but drop %+v", d)
		}
	}
	if off.Hash != g.Hash {
		t.Fatal("the switch must not change the bundle hash")
	}
}

func TestProviderForSkipsMalformedLockImport(t *testing.T) {
	m, _, _ := importsManager(t)
	built, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative/x.md", "/home/agent/../etc/x.md", "/home/agent//x.md", "", "/"} {
		built.Lock.Imports = []LockImport{{Box: bad, SHA256: "x"}}
		if err := rewriteLock(built); err != nil {
			t.Fatal(err)
		}
		g, err := NewProvider(m).For("claude", true)
		if err != nil {
			t.Fatal(err)
		}
		if g.Imports != 0 {
			t.Errorf("%q: imports = %d", bad, g.Imports)
		}
		for _, mt := range g.Mounts {
			if strings.HasPrefix(mt.Host, importsDir(built.Dir)) {
				t.Errorf("%q: mounted %+v", bad, mt)
			}
		}
	}
}

func rewriteLock(b *Built) error {
	_, err := writeJSON(filepath.Join(b.Dir, lockName), b.Lock)
	return err
}

// The resolver's reserved check sees only the in-box spelling, so a file the
// bundle sanitizes or replaces must be denied by identity.
func TestImportsDenySanitizedConfigFiles(t *testing.T) {
	m, home, src := importsManager(t)
	mcp := filepath.Join(home, ".claude.json")
	impWrite(t, mcp, `{"mcpServers":{"x":{"command":"npx","env":{"TOKEN":"sk-live"}}}}`)
	impWrite(t, filepath.Join(src, "settings.json"), `{"env":{"TOKEN":"sk-literal"}}`)
	impWrite(t, filepath.Join(src, "settings.local.json"), `{"env":{"TOKEN":"sk-local"}}`)
	// A directory link that leads to the config dir: the file's identity, not its
	// spelling, is what is denied.
	if err := os.Symlink(src, filepath.Join(home, "cfg")); err != nil {
		t.Fatal(err)
	}
	spell := []string{
		mcp,
		filepath.Join(src, "settings.json"),
		filepath.Join(src, "settings.local.json"),
		"~/cfg/settings.json",
	}
	var md strings.Builder
	for _, sp := range spell {
		md.WriteString("@" + sp + "\n")
	}
	impWrite(t, filepath.Join(src, "CLAUDE.md"), md.String())
	set := m.Imports()
	if len(set.Files) != 0 {
		t.Fatalf("bundled: %v", impBoxes(set))
	}
	for _, sp := range spell {
		if got := impRefusal(set, sp); got != ReasonImportDenied {
			t.Errorf("%s: reason %q", sp, got)
		}
	}
}

func TestImportsDenyHostCredentialDirs(t *testing.T) {
	m, home, src := importsManager(t)
	files := map[string]string{
		"~/.aws/config":          "[default]",
		"~/.config/gh/hosts.yml": "github.com:",
		"~/.kube/config":         "apiVersion: v1",
		"~/.gnupg/pubring.kbx":   "k",
		"~/.netrc":               "machine x",
		"~/.git-credentials":     "https://u:p@x",
	}
	var md strings.Builder
	for sp, body := range files {
		impWrite(t, filepath.Join(home, strings.TrimPrefix(sp, "~/")), body)
		md.WriteString("@" + sp + "\n")
	}
	impWrite(t, filepath.Join(src, "CLAUDE.md"), md.String())
	set := m.Imports()
	if len(set.Files) != 0 {
		t.Fatalf("bundled: %v", impBoxes(set))
	}
	for sp := range files {
		if got := impRefusal(set, sp); got != ReasonImportDenied {
			t.Errorf("%s: reason %q", sp, got)
		}
	}
}

func TestProviderForSkipsMissingImportFile(t *testing.T) {
	m, _, src := importsManager(t)
	impWrite(t, filepath.Join(src, "CLAUDE.md"), "@~/.claude/context/a.md\n@~/.claude/context/b.md\n")
	impWrite(t, filepath.Join(src, "context", "b.md"), "BBB")
	built, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Lock.Imports) != 2 {
		t.Fatalf("lock imports = %+v", built.Lock.Imports)
	}
	if err := os.Remove(filepath.Join(importsDir(built.Dir), "home/agent/.claude/context/a.md")); err != nil {
		t.Fatal(err)
	}
	g, err := NewProvider(m).For("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if g.Imports != 1 || hasMount(g, importMount(built.Dir)) {
		t.Fatalf("missing file still mounted: imports=%d %+v", g.Imports, g.Mounts)
	}
	b := box.Mount{
		Host:     filepath.Join(importsDir(built.Dir), "home/agent/.claude/context/b.md"),
		Box:      "/home/agent/.claude/context/b.md",
		Optional: true, Yield: true,
	}
	if !hasMount(g, b) {
		t.Fatalf("the other import must stay mounted: %+v", g.Mounts)
	}
}
