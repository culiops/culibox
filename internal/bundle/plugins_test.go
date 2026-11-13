package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pluginTree writes a host plugins/ dir the way Claude Code leaves it: the four
// entries a box needs, plus the ones it must never see.
func pluginTree(t *testing.T, src string) {
	t.Helper()
	pdir := filepath.Join(src, "plugins")
	for _, d := range []string{"cache/official/demo/1.0.0", "marketplaces/official", "data/demo-official", "store"} {
		if err := os.MkdirAll(filepath.Join(pdir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"installed_plugins.json", "known_marketplaces.json", "plugin-catalog-cache.json", "plugin-directory-cache-v2.json"} {
		if err := os.WriteFile(filepath.Join(pdir, f), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

var pluginBoxPaths = []string{
	"/home/agent/.claude/plugins/installed_plugins.json",
	"/home/agent/.claude/plugins/known_marketplaces.json",
	"/home/agent/.claude/plugins/cache",
	"/home/agent/.claude/plugins/marketplaces",
}

// snap runs the plugin verdict against src with a fresh snapshot root.
func snap(t *testing.T, src string) (ps pluginSet, root string) {
	t.Helper()
	root = t.TempDir()
	return pluginSnapshot(src, root, testGuard()), root
}

// hostOf is the snapshot path the mount for entry name points at.
func hostOf(t *testing.T, ps pluginSet, name string) string {
	t.Helper()
	for _, mt := range ps.mounts {
		if filepath.Base(mt.Box) == name {
			return mt.Host
		}
	}
	t.Fatalf("no mount for %s in %+v", name, ps.mounts)
	return ""
}

// setPaths is every path the mounts expose, keyed entry/rel the way the box
// sees it under .claude/plugins.
func setPaths(t *testing.T, ps pluginSet) map[string]os.FileMode {
	t.Helper()
	out := map[string]os.FileMode{}
	for _, mt := range ps.mounts {
		for rel, m := range snapshotPaths(t, mt.Host) {
			out[filepath.Join(filepath.Base(mt.Box), rel)] = m
		}
	}
	return out
}

// slotPrefix is the snapshot dir each entry lives in: the two small json files
// share one, and each big tree has its own so it is recopied only when it
// changes.
var slotPrefix = map[string]string{
	"installed_plugins.json":  "plugins-json-",
	"known_marketplaces.json": "plugins-json-",
	"cache":                   "plugins-cache-",
	"marketplaces":            "plugins-marketplaces-",
}

// Measured 2026-10-19: claude rebases the absolute installPath in
// installed_plugins.json onto its own config dir, so all plugins load with only
// the home mounts. A second mount at the host's own path would be redundant and
// would leak the host layout into the box.
func TestPluginMountsMountEachEntryOnce(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	ps, root := snap(t, src)
	if len(ps.drops) != 0 {
		t.Fatalf("a complete set has no drops: %+v", ps.drops)
	}
	if len(ps.mounts) != 4 {
		t.Fatalf("want exactly 4 mounts, got %+v", ps.mounts)
	}
	for i, mt := range ps.mounts {
		if mt.Box != pluginBoxPaths[i] || !mt.Optional {
			t.Fatalf("mount %d = %+v, want optional %s", i, mt, pluginBoxPaths[i])
		}
		// Mounts point into the entry's own snapshot dir, never into the host
		// config dir.
		name, slot := filepath.Base(mt.Box), filepath.Dir(mt.Host)
		if filepath.Base(mt.Host) != name || filepath.Dir(slot) != root || !strings.HasPrefix(filepath.Base(slot), slotPrefix[name]) {
			t.Fatalf("mount %d host %q is not %s<hash>/%s under %s", i, mt.Host, slotPrefix[name], name, root)
		}
		if strings.HasPrefix(mt.Host, src) || strings.HasPrefix(mt.Box, src) {
			t.Fatalf("%+v reaches the host config dir %s", mt, src)
		}
		for _, never := range []string{"data", "store", "plugin-catalog-cache.json", "plugin-directory-cache-v2.json"} {
			if filepath.Base(mt.Box) == never {
				t.Fatalf("%q must never be mounted: %+v", never, mt)
			}
		}
	}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		for _, never := range []string{"data", "store", "plugin-catalog-cache.json", "plugin-directory-cache-v2.json"} {
			if err == nil && d.Name() == never {
				t.Errorf("%q must never be copied into a snapshot: %s", never, p)
			}
		}
		return nil
	})
}

// No installed_plugins.json means the user has no plugins: that is not a fault
// worth a drop row.
func TestPluginMountsNoInstalledJSONIsSilent(t *testing.T) {
	src := t.TempDir()
	if ps, _ := snap(t, src); ps.mounts != nil || ps.drops != nil {
		t.Fatalf("absent plugins/ must be silent, got %+v", ps)
	}
	pluginTree(t, src)
	if err := os.Remove(filepath.Join(src, "plugins", "installed_plugins.json")); err != nil {
		t.Fatal(err)
	}
	if ps, _ := snap(t, src); ps.mounts != nil || ps.drops != nil {
		t.Fatalf("plugins/ without installed_plugins.json must be silent, got %+v", ps)
	}
}

// Measured: a partial set "failed to load", and only all four entries enable
// plugins. So it is all or nothing, and the one drop says why.
func TestPluginMountsIncompleteSetDropsAll(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	if err := os.RemoveAll(filepath.Join(src, "plugins", "marketplaces")); err != nil {
		t.Fatal(err)
	}
	ps, _ := snap(t, src)
	mounts, drops := ps.mounts, ps.drops
	if len(mounts) != 0 {
		t.Fatalf("an incomplete set must mount nothing: %+v", mounts)
	}
	want := Drop{Kind: "plugins", Name: "*", Reason: "the host plugin dir is incomplete; plugins load only with installed_plugins.json, known_marketplaces.json, cache/ and marketplaces/ all present"}
	if len(drops) != 1 || drops[0] != want {
		t.Fatalf("want exactly %+v, got %+v", want, drops)
	}
}

// Docker resolves a symlinked bind source on the HOST side, so a planted
// cache -> ~/.ssh would publish its target. Nothing may be mounted then.
func TestPluginMountsRefuseSymlinks(t *testing.T) {
	const reason = "is a symlink on the host; CuliBox mounts only real files and directories"
	t.Run("entry", func(t *testing.T) {
		src := t.TempDir()
		pluginTree(t, src)
		secret := t.TempDir()
		cache := filepath.Join(src, "plugins", "cache")
		if err := os.RemoveAll(cache); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, cache); err != nil {
			t.Fatal(err)
		}
		ps, _ := snap(t, src)
		mounts, drops := ps.mounts, ps.drops
		if len(mounts) != 0 {
			t.Fatalf("a symlinked entry must void the whole set: %+v", mounts)
		}
		if len(drops) != 1 || drops[0] != (Drop{Kind: "plugins", Name: "cache", Reason: reason}) {
			t.Fatalf("want the cache symlink drop, got %+v", drops)
		}
	})
	t.Run("plugins dir", func(t *testing.T) {
		src := t.TempDir()
		real := t.TempDir()
		pluginTree(t, real)
		if err := os.Symlink(filepath.Join(real, "plugins"), filepath.Join(src, "plugins")); err != nil {
			t.Fatal(err)
		}
		ps, _ := snap(t, src)
		mounts, drops := ps.mounts, ps.drops
		if len(mounts) != 0 {
			t.Fatalf("a symlinked plugins/ must publish nothing: %+v", mounts)
		}
		if len(drops) != 1 || drops[0] != (Drop{Kind: "plugins", Name: "plugins", Reason: reason}) {
			t.Fatalf("want the plugins symlink drop, got %+v", drops)
		}
	})
}

func settingsOf(t *testing.T, b *Built) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func sourceWithEnabledPlugins(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"),
		[]byte(`{"model":"opus","enabledPlugins":{"superpowers@official":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return src
}

// Measured 2026-10-19: enabledPlugins WITHOUT the plugin mounts makes the box
// git-clone the official marketplace and install unpinned plugins from GitHub.
// The key and the mounts are therefore decided together.
func TestEnabledPluginsTravelOnlyWithTheMountedSet(t *testing.T) {
	withSet := sourceWithEnabledPlugins(t)
	pluginTree(t, withSet)
	bWith := buildForTest(t, withSet)
	if _, ok := settingsOf(t, bWith)["enabledPlugins"]; !ok {
		t.Fatalf("enabledPlugins must survive when the full set is mounted: %v", settingsOf(t, bWith))
	}

	broken := sourceWithEnabledPlugins(t)
	pluginTree(t, broken)
	if err := os.RemoveAll(filepath.Join(broken, "plugins", "marketplaces")); err != nil {
		t.Fatal(err)
	}
	bBroken := buildForTest(t, broken)
	if _, ok := settingsOf(t, bBroken)["enabledPlugins"]; ok {
		t.Fatal("enabledPlugins must be removed when the set is not mounted")
	}
	if settingsOf(t, bBroken)["model"] != "opus" {
		t.Fatal("only enabledPlugins is stripped; the rest of the settings stay")
	}
	for _, d := range bBroken.Lock.Dropped {
		if d.Name == "enabledPlugins" {
			t.Fatalf("no extra drop row for enabledPlugins; the plugins drop already says why: %+v", d)
		}
	}
	if len(bBroken.pluginDrops) != 1 || bBroken.pluginDrops[0].Kind != "plugins" {
		t.Fatalf("the plugin drop must be carried for the provider: %+v", bBroken.pluginDrops)
	}
}

// The verdict is part of the source hash, so a cached bundle cannot keep
// enabledPlugins after the set breaks, nor lack it after the set is fixed.
func TestPluginVerdictChangesTheBundleHash(t *testing.T) {
	src := sourceWithEnabledPlugins(t)
	pluginTree(t, src)
	mk := func() *Manager {
		m, err := New(Options{Source: src, Root: rootFor(t, src), MCPFile: src + "/../.claude.json", Prober: fakeProber{}})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	good, err := mk().Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(src, "plugins", "marketplaces")); err != nil {
		t.Fatal(err)
	}
	bad, err := mk().Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if good.Hash == bad.Hash {
		t.Fatal("the plugin verdict must be folded into the bundle hash")
	}
	if _, ok := settingsOf(t, bad)["enabledPlugins"]; ok {
		t.Fatal("a cached bundle kept enabledPlugins after the set broke")
	}
	if err := os.MkdirAll(filepath.Join(src, "plugins", "marketplaces", "official"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixed, err := mk().Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Hash != good.Hash {
		t.Fatal("restoring the set must restore the original hash")
	}
	if _, ok := settingsOf(t, fixed)["enabledPlugins"]; !ok {
		t.Fatal("enabledPlugins must return once the set is whole again")
	}
}

// writePluginFile writes under src/plugins, creating parents.
func writePluginFile(t *testing.T, src, rel, content string) {
	t.Helper()
	p := filepath.Join(src, "plugins", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitConfig(url string) string {
	return "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = " + url + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
}

// The rule, stated exactly, for a URL in a json file: ANY non-empty userinfo is
// a credential for every scheme, except ssh and git+ssh with no password (there
// git@ is just the account name). scp-style git@github.com:o/r.git has no
// scheme, so it is never userinfo. An @ after the first / ? or # is not userinfo.
func TestHasURLCredentialsRule(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"url = https://tok@github.com/o/r", true},
		{"url = http://tok@github.com/o/r", true},
		{"url = HTTPS://user:pw@github.com/o/r.git", true},
		{`{"url":"https://tok@github.com/o/r"}`, true},
		{"git+https://tok@h/o/r", true},
		{"ftp://u@h/o/r", true},
		{"git://u@h/o/r", true},
		{"ssh://git:pw@host/o/r", true},
		{"git+ssh://git:pw@host/o/r", true},
		{"ssh://git@host/o/r", false},
		{"git+ssh://git@host/o/r", false},
		{"git@github.com:o/r.git", false},
		{"https://github.com/o/r", false},
		{"https://github.com/o/r?e=a@b", false},
		{"https://github.com/o/r#a@b", false},
		{"https://github.com/o/blob/x@y", false},
		{"https://@github.com/o/r", false},
	} {
		if got := hasURLCredentials([]byte(c.in)); got != c.want {
			t.Errorf("hasURLCredentials(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Only the two small json files are checked, and in the snapshot copy.
func TestPluginMountsRefuseCredentialedJSON(t *testing.T) {
	for _, c := range []struct{ name, file, url string }{
		{"known https", "known_marketplaces.json", "https://tok@github.com/o/private-marketplace"},
		{"known git+https", "known_marketplaces.json", "git+https://tok@h/r"},
		{"known ftp", "known_marketplaces.json", "ftp://u@h/r"},
		{"installed", "installed_plugins.json", "https://tok@github.com/o/r"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := t.TempDir()
			pluginTree(t, src)
			writePluginFile(t, src, c.file, `{"m":{"source":{"url":"`+c.url+`"}}}`)
			ps, _ := snap(t, src)
			want := Drop{Kind: "plugins", Name: c.file, Reason: "a plugin marketplace URL carries credentials; CuliBox never mounts credentials into a box"}
			if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0] != want {
				t.Fatalf("want no mounts and exactly %+v, got %+v", want, ps)
			}
			if strings.Contains(ps.drops[0].Reason+ps.drops[0].Name, "tok") {
				t.Fatalf("the drop must never carry the URL: %+v", ps.drops[0])
			}
		})
	}
}

func TestPluginMountsAllowCredentialFreeURLs(t *testing.T) {
	for _, url := range []string{"git@github.com:o/r.git", "https://github.com/o/r", "ssh://git@github.com/o/r"} {
		src := t.TempDir()
		pluginTree(t, src)
		writePluginFile(t, src, "known_marketplaces.json", `{"m":{"source":{"url":"`+url+`"}}}`)
		if ps, _ := snap(t, src); len(ps.mounts) != 4 || len(ps.drops) != 0 {
			t.Fatalf("%s must mount normally: %+v", url, ps)
		}
	}
}

// A non-regular file where a json belongs is refused by Lstat, before any open
// that would block on it.
func TestPluginMountsRefuseAFifoForAJSONEntry(t *testing.T) {
	for _, name := range []string{"installed_plugins.json", "known_marketplaces.json"} {
		t.Run(name, func(t *testing.T) { fifoForJSONEntry(t, name) })
	}
}

func fifoForJSONEntry(t *testing.T, name string) {
	src := t.TempDir()
	pluginTree(t, src)
	p := filepath.Join(src, "plugins", name)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	root := t.TempDir()
	done := make(chan pluginSet, 1)
	go func() { done <- pluginSnapshot(src, root, testGuard()) }() // no t.* off the test goroutine
	select {
	case ps := <-done:
		if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0].Reason != ReasonPluginsIncomplete {
			t.Fatalf("a FIFO entry must be refused as incomplete: %+v", ps)
		}
	case <-timeoutAfter(10):
		t.Fatal("a FIFO entry hung the verdict")
	}
}

// Walk the snapshot and report every path.
func snapshotPaths(t *testing.T, dir string) map[string]os.FileMode {
	t.Helper()
	out := map[string]os.FileMode{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fi.Mode()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 1: no git metadata reaches a box. A marketplace clone made with a token keeps
// it in .git/config (a URL, an extraheader), a submodule in .git/modules, a
// worktree or submodule checkout has a .git FILE. All are skipped outright.
func TestSnapshotHasNoGitMetadataAnywhere(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	writePluginFile(t, src, "marketplaces/m/.git/config", gitConfig("https://tok@github.com/o/r")+"[http]\n\textraheader = AUTHORIZATION: basic c2VjcmV0\n")
	writePluginFile(t, src, "marketplaces/m/real.txt", "kept")
	writePluginFile(t, src, "cache/a/.git/modules/x/config", gitConfig("https://tok2@h/x"))
	writePluginFile(t, src, "cache/a/b/c/d/e/f/.git/config", "[credential]\n\thelper = store\n")
	writePluginFile(t, src, "cache/a/sub/.git", "gitdir: ../../.git/modules/x\n")
	writePluginFile(t, src, "cache/a/keep.txt", "kept")
	// APFS and NTFS are case-insensitive: .GIT is the same directory there.
	writePluginFile(t, src, "marketplaces/m/.GIT/config", gitConfig("https://tok3@h/y"))
	writePluginFile(t, src, "cache/a/.Git", "gitdir: elsewhere\n")

	ps, root := snap(t, src)
	if len(ps.mounts) != 4 || len(ps.drops) != 0 {
		t.Fatalf("a git-bearing set still mounts (the git data is simply absent): %+v", ps)
	}
	paths := snapshotPaths(t, root)
	for rel := range paths {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.EqualFold(part, ".git") {
				t.Fatalf("snapshot contains git metadata: %s", rel)
			}
		}
	}
	exposed := setPaths(t, ps)
	for _, keep := range []string{"marketplaces/m/real.txt", "cache/a/keep.txt", "cache/a/sub"} {
		if _, ok := exposed[filepath.FromSlash(keep)]; !ok {
			t.Fatalf("%s must be copied", keep)
		}
	}
	// Nothing secret anywhere in the bytes either.
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "tok") || strings.Contains(string(b), "c2VjcmV0") {
				t.Errorf("secret leaked into %s", p)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 2: a symlink INSIDE the tree is never followed, so a secret behind it never
// enters the snapshot. It is recreated, with the same target string, only when
// it stays inside its own entry: relative, resolving lexically under that
// entry, and not walking through another link (a link it went through would
// make the lexical answer wrong). Anything else is skipped rather than handed
// to a file-sharing layer that might follow it on the host (colima on macOS was
// never checked). A skipped link is not a drop: the set still mounts.
func TestSnapshotRecreatesSymlinksWithoutFollowing(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePluginFile(t, src, "cache/official/demo/1.0.0/CLAUDE.md", "# plugin")
	writePluginFile(t, src, "cache/official/demo/1.0.0/sub/x.txt", "x")
	writePluginFile(t, src, "marketplaces/official/m.txt", "m")
	cache := filepath.Join(src, "plugins", "cache")
	links := [][2]string{
		{"official/link", outside},                                    // absolute, to a secret
		{"abs", "/home/user/.claude/plugins/cache/x"},                 // absolute
		{"official/demo/1.0.0/AGENTS.md", "CLAUDE.md"},                // the real tree's one link: kept
		{"official/demo/1.0.0/up", "../../demo/1.0.0/sub/x.txt"},      // relative, in entry: kept
		{"official/demo/1.0.0/hop", "../../.."},                       // resolves to cache/ itself: kept
		{"official/demo/1.0.0/escape", "../../../../plugins/cache"},   // climbs out of cache
		{"official/other-entry", "../../marketplaces/official/m.txt"}, // into another entry
		{"official/demo/1.0.0/toroot", "../../../.."},                 // the plugins dir itself
		{"official/demo/1.0.0/chained", "hop/../../x"},                // lexically in cache; via hop it climbs out
	}
	for _, l := range links {
		if err := os.Symlink(l[1], filepath.Join(cache, l[0])); err != nil {
			t.Fatal(err)
		}
	}
	ps, _ := snap(t, src)
	if len(ps.mounts) != 4 || len(ps.drops) != 0 {
		t.Fatalf("a link inside the tree does not void the set and is not a drop: %+v", ps)
	}
	snapCache := hostOf(t, ps, "cache")
	for rel, want := range map[string]string{
		"official/demo/1.0.0/AGENTS.md": "CLAUDE.md",
		"official/demo/1.0.0/up":        "../../demo/1.0.0/sub/x.txt",
		"official/demo/1.0.0/hop":       "../../..",
	} {
		got, err := os.Readlink(filepath.Join(snapCache, rel))
		if err != nil || got != want {
			t.Fatalf("in-entry link %s must keep its target string %q: %q %v", rel, want, got, err)
		}
	}
	for _, rel := range []string{"official/link", "abs", "official/demo/1.0.0/escape", "official/other-entry",
		"official/demo/1.0.0/toroot", "official/demo/1.0.0/chained"} {
		if _, err := os.Lstat(filepath.Join(snapCache, rel)); err == nil {
			t.Errorf("link %s leaves its entry and must be skipped", rel)
		}
	}
	filepath.WalkDir(filepath.Dir(snapCache), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "TOP-SECRET-CONTENT") {
				t.Errorf("a symlink target's content was copied into %s", p)
			}
		}
		return nil
	})
}

// 3: a FIFO inside the tree is skipped (and does not hang the copy).
func TestSnapshotSkipsFifosInsideTheTree(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	writePluginFile(t, src, "cache/a/ok.txt", "ok")
	if err := syscall.Mkfifo(filepath.Join(src, "plugins", "cache", "a", "pipe"), 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	root := t.TempDir()
	done := make(chan pluginSet, 1)
	go func() { done <- pluginSnapshot(src, root, testGuard()) }() // no t.* off the test goroutine
	select {
	case ps := <-done:
		if len(ps.mounts) != 4 {
			t.Fatalf("the set must still mount: %+v", ps)
		}
		var ok, pipe bool
		for p := range setPaths(t, ps) {
			ok = ok || p == filepath.Join("cache", "a", "ok.txt")
			pipe = pipe || filepath.Base(p) == "pipe"
		}
		if !ok || pipe {
			t.Fatalf("want ok.txt copied and pipe skipped (ok=%v pipe=%v)", ok, pipe)
		}
	case <-timeoutAfter(10):
		t.Fatal("a FIFO hung the snapshot")
	}
}

// Permission bits: executable survives, setuid/setgid/sticky never do.
func TestSnapshotNormalizesFileModes(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	writePluginFile(t, src, "cache/a/run.sh", "#!/bin/sh\n")
	writePluginFile(t, src, "cache/a/data.txt", "x")
	if err := os.Chmod(filepath.Join(src, "plugins", "cache", "a", "run.sh"), 0o4755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "plugins", "cache", "a", "data.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	ps, _ := snap(t, src)
	paths := setPaths(t, ps)
	if m := paths[filepath.Join("cache", "a", "run.sh")]; m != 0o755 {
		t.Fatalf("run.sh mode %v, want 0755 with no setuid", m)
	}
	if m := paths[filepath.Join("cache", "a", "data.txt")]; m != 0o644 {
		t.Fatalf("data.txt mode %v, want 0644", m)
	}
}

// 5: editing the plugin tree makes a new snapshot; editing only a skill reuses
// the existing one (no 38 MB recopy per skill edit).
func TestSnapshotIsKeyedByThePluginTreeOnly(t *testing.T) {
	src := realisticSource(t)
	pluginTree(t, src)
	root := t.TempDir()
	first := pluginSnapshot(src, root, testGuard())
	before := slotStats(t, first)

	if err := os.WriteFile(filepath.Join(src, "skills", "demo", "SKILL.md"), []byte("# edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := pluginSnapshot(src, root, testGuard())
	for name, fi := range slotStats(t, again) {
		if !sameSlot(before[name], fi) {
			t.Fatalf("editing a skill must reuse the %s snapshot, not recopy it", name)
		}
	}
}

// slotStats Lstats the completion marker of the snapshot each entry's mount lives
// in. The marker and not the dir, whose date every reuse refreshes for the GC.
func slotStats(t *testing.T, ps pluginSet) map[string]os.FileInfo {
	t.Helper()
	out := map[string]os.FileInfo{}
	for _, mt := range ps.mounts {
		fi, err := os.Lstat(filepath.Join(filepath.Dir(mt.Host), snapshotMarker))
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(mt.Box)] = fi
	}
	if len(out) != 4 {
		t.Fatalf("want 4 mounted entries, got %+v", ps)
	}
	return out
}

// sameSlot is true when the snapshot is the very same copy, given slotStats: same
// marker inode and mtime (a recopy into it, or a replacement, changes one).
func sameSlot(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.ModTime().Equal(b.ModTime())
}

// Host claude rewrites known_marketplaces.json (its lastUpdated) on routine
// marketplace checks even when nothing changed. That must cost a copy of the two
// small json files, never a recopy of cache/ and marketplaces/ (tens of MB), and
// never a new bundle dir: the bundle folds in only whether plugins are mounted.
func TestKnownMarketplacesRewriteRecopiesOnlyTheJSON(t *testing.T) {
	for _, c := range []struct{ name, doc string }{
		{"same bytes, new mtime", `{}`},
		{"new bytes", `{"official":{"lastUpdated":"2026-10-19T05:25:45Z"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := sourceWithEnabledPlugins(t)
			pluginTree(t, src)
			writePluginFile(t, src, "cache/official/demo/1.0.0/big.txt", "plugin body")
			writePluginFile(t, src, "marketplaces/official/catalog.json", "{}")
			b1 := buildForTest(t, src)
			ps1 := pluginSet{mounts: b1.pluginMounts}
			before := slotStats(t, ps1)

			p := filepath.Join(src, "plugins", "known_marketplaces.json")
			writePluginFile(t, src, "known_marketplaces.json", c.doc)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(p, later, later); err != nil {
				t.Fatal(err)
			}
			b2 := buildForTest(t, src)
			ps2 := pluginSet{mounts: b2.pluginMounts}
			after := slotStats(t, ps2)

			for _, big := range []string{"cache", "marketplaces"} {
				if hostOf(t, ps1, big) != hostOf(t, ps2, big) || !sameSlot(before[big], after[big]) {
					t.Fatalf("a json rewrite must not recopy %s: %s -> %s", big, hostOf(t, ps1, big), hostOf(t, ps2, big))
				}
			}
			if hostOf(t, ps1, "known_marketplaces.json") == hostOf(t, ps2, "known_marketplaces.json") {
				t.Fatal("the json snapshot must follow the rewrite")
			}
			if got, _ := os.ReadFile(hostOf(t, ps2, "known_marketplaces.json")); string(got) != c.doc {
				t.Fatalf("the new json snapshot holds %q, want %q", got, c.doc)
			}
			if b1.Dir != b2.Dir || b1.Hash != b2.Hash {
				t.Fatalf("marketplace churn must not make a new bundle: %s -> %s", b1.Dir, b2.Dir)
			}
			if !b2.Reused {
				t.Fatal("the bundle must be reused, not rebuilt")
			}
			for prefix, want := range map[string]int{"plugins-cache-": 1, "plugins-marketplaces-": 1, "plugins-json-": 2} {
				if n := countPrefix(t, rootFor(t, src), prefix); n != want {
					t.Fatalf("%d %s* dirs, want %d", n, prefix, want)
				}
			}
		})
	}
}

// A change under cache/ recopies cache/ alone.
func TestCacheChangeRecopiesOnlyTheCache(t *testing.T) {
	src := sourceWithEnabledPlugins(t)
	pluginTree(t, src)
	writePluginFile(t, src, "cache/official/demo/1.0.0/big.txt", "plugin body")
	b1 := buildForTest(t, src)
	ps1 := pluginSet{mounts: b1.pluginMounts}
	before := slotStats(t, ps1)

	writePluginFile(t, src, "cache/official/demo/1.0.0/new.txt", "new")
	b2 := buildForTest(t, src)
	ps2 := pluginSet{mounts: b2.pluginMounts}
	after := slotStats(t, ps2)

	if hostOf(t, ps1, "cache") == hostOf(t, ps2, "cache") {
		t.Fatal("editing a file under cache must produce a new cache snapshot")
	}
	if _, err := os.Stat(filepath.Join(hostOf(t, ps2, "cache"), "official", "demo", "1.0.0", "new.txt")); err != nil {
		t.Fatal("the new cache snapshot must hold the new file")
	}
	for _, other := range []string{"installed_plugins.json", "known_marketplaces.json", "marketplaces"} {
		if hostOf(t, ps1, other) != hostOf(t, ps2, other) || !sameSlot(before[other], after[other]) {
			t.Fatalf("a cache change must not recopy %s", other)
		}
	}
	if b1.Hash != b2.Hash {
		t.Fatal("a cache change keeps the plugin verdict, so the bundle stays")
	}
}

func countPrefix(t *testing.T, dir, prefix string) int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			n++
		}
	}
	return n
}

// 6: a snapshot is a copy. A file the host writes after Ensure never appears in
// what is mounted: a live bind would have shown it.
func TestHostChangeAfterEnsureDoesNotReachTheSnapshot(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	ps, _ := snap(t, src)
	writePluginFile(t, src, "cache/official/demo/1.0.0/late.txt", "late")
	writePluginFile(t, src, "marketplaces/official/.git/config", gitConfig("https://tok@h/r"))
	cache := ps.mounts[2].Host
	if _, err := os.Stat(filepath.Join(cache, "official", "demo", "1.0.0", "late.txt")); err == nil {
		t.Fatal("a host write after Ensure must not appear in the mounted snapshot")
	}
	if _, err := os.Lstat(filepath.Join(ps.mounts[3].Host, "official", ".git")); err == nil {
		t.Fatal("late git metadata must not appear in the mounted snapshot")
	}
}

// 8: the size caps fail closed: nothing mounted, the incomplete drop, and no
// snapshot or temp dir left behind.
func TestSnapshotCapFailsClosedAndLeavesNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func()
		reset func()
	}{
		{"entries", func() { pluginCapEntries = 3 }, func() { pluginCapEntries = 100_000 }},
		{"bytes", func() { pluginCapBytes = 1 }, func() { pluginCapBytes = 512 << 20 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := t.TempDir()
			pluginTree(t, src)
			writePluginFile(t, src, "cache/official/demo/1.0.0/big.txt", "0123456789")
			c.set()
			defer c.reset()
			ps, root := snap(t, src)
			if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0].Name != "*" || ps.drops[0].Reason != ReasonPluginsIncomplete {
				t.Fatalf("a set over the cap must be dropped as incomplete: %+v", ps)
			}
			ents, _ := os.ReadDir(root)
			if len(ents) != 0 {
				t.Fatalf("nothing may be left behind, got %v", ents)
			}
		})
	}
}

// Any Lstat failure other than "does not exist" fails closed: never "no plugins".
func TestPluginSetUnreadableFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	src := t.TempDir()
	pluginTree(t, src)
	if err := os.Chmod(filepath.Join(src, "plugins"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(src, "plugins"), 0o700)
	ps, _ := snap(t, src)
	if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0].Reason != ReasonPluginsIncomplete {
		t.Fatalf("an unreadable plugins/ must be dropped as incomplete, not silent: %+v", ps)
	}
}

// The credential verdict rides the same path as every other refusal, so it is
// in the hash and enabledPlugins goes with the mounts.
func TestCredentialedMarketplaceChangesHashAndStripsEnabledPlugins(t *testing.T) {
	src := sourceWithEnabledPlugins(t)
	pluginTree(t, src)
	mk := func() *Built {
		m, err := New(Options{Source: src, Root: rootFor(t, src), MCPFile: src + "/../.claude.json", Prober: fakeProber{}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := m.Ensure()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	clean := mk()
	writePluginFile(t, src, "known_marketplaces.json", `{"m":{"url":"https://tok@github.com/o/r"}}`)
	dirty := mk()
	if clean.Hash == dirty.Hash {
		t.Fatal("the credential verdict must change the bundle hash")
	}
	if _, ok := settingsOf(t, clean)["enabledPlugins"]; !ok {
		t.Fatal("clean set keeps enabledPlugins")
	}
	if _, ok := settingsOf(t, dirty)["enabledPlugins"]; ok {
		t.Fatal("enabledPlugins must go with the refused mounts")
	}
	if len(dirty.pluginMounts) != 0 || len(dirty.pluginDrops) != 1 {
		t.Fatalf("grant must carry no mounts and one drop: %+v %+v", dirty.pluginMounts, dirty.pluginDrops)
	}
}

// The check runs on the DECODED json as well as the raw bytes: escapes decode to a
// credentialed URL but do not match a regex over the file's text.
func TestPluginMountsRefuseCredentialsHiddenByJSONEscapes(t *testing.T) {
	for _, c := range []struct{ name, doc string }{
		{"escaped slashes", `{"m":{"url":"https:\/\/tok@host/r"}}`},
		{"unicode escaped slashes", `{"m":{"url":"https:\u002f\u002ftok@host/r"}}`},
		{"unicode escaped scheme", `{"m":{"url":"\u0068ttps://tok@host/r"}}`},
		{"unicode escaped at", `{"m":{"url":"https://tok\u0040host/r"}}`},
		{"url as an object key", `{"https://tok@host/r":true}`},
		{"nested in an array", `{"a":[{"b":["x","git+https://u:p@h/r"]}]}`},
		{"not valid json", `{"m":{"url":"https://github.com/o/r"`},
		{"not json at all", `https://github.com/o/r`},
	} {
		for _, file := range credentialFiles {
			t.Run(c.name+" in "+file, func(t *testing.T) {
				src := t.TempDir()
				pluginTree(t, src)
				writePluginFile(t, src, file, c.doc)
				ps, _ := snap(t, src)
				want := Drop{Kind: "plugins", Name: file, Reason: ReasonPluginCredentials}
				if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0] != want {
					t.Fatalf("want no mounts and exactly %+v, got %+v", want, ps)
				}
			})
		}
	}
}

// A duplicate key decodes to only one of its values, but the raw file that is
// mounted into the box still holds both: a credentialed value is refused
// whichever position it is in.
func TestPluginMountsRefuseCredentialsShadowedByDuplicateKeys(t *testing.T) {
	for _, c := range []struct{ name, doc string }{
		{"credential first", `{"m":{"url":"https://tok@host/r","url":"https://host/r"}}`},
		{"credential last", `{"m":{"url":"https://host/r","url":"https://tok@host/r"}}`},
		{"escaped credential first", `{"m":{"url":"https:\/\/tok@host/r","url":"https://host/r"}}`},
		{"top-level duplicate", `{"u":"https://tok@h/r","u":"https://h/r"}`},
	} {
		for _, file := range credentialFiles {
			t.Run(c.name+" in "+file, func(t *testing.T) {
				src := t.TempDir()
				pluginTree(t, src)
				writePluginFile(t, src, file, c.doc)
				ps, _ := snap(t, src)
				want := Drop{Kind: "plugins", Name: file, Reason: ReasonPluginCredentials}
				if len(ps.mounts) != 0 || len(ps.drops) != 1 || ps.drops[0] != want {
					t.Fatalf("want no mounts and exactly %+v, got %+v", want, ps)
				}
			})
		}
	}
}

// Escapes that decode to a harmless URL must not be refused.
func TestPluginMountsAllowEscapedCredentialFreeJSON(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	writePluginFile(t, src, "known_marketplaces.json", `{"m":{"url":"https:\/\/github.com\/o\/r","ssh":"ssh://git\u0040h/o/r","n":3,"b":null}}`)
	if ps, _ := snap(t, src); len(ps.mounts) != 4 || len(ps.drops) != 0 {
		t.Fatalf("a credential-free escaped URL must mount: %+v", ps)
	}
}

// The dir opened must be the one that was checked: if plugins/ is swapped for
// another directory between the Lstat and the open, nothing is published.
func TestPublishSnapshotRefusesASwappedPluginsDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	pluginTree(t, a)
	pluginTree(t, b)
	checked, err := os.Lstat(filepath.Join(a, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := publishSnapshot(filepath.Join(b, "plugins"), checked, root, testGuard()); err == nil {
		t.Fatal("a swapped plugins dir must be refused")
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("nothing may be published: %v", ents)
	}
	same, _ := os.Lstat(filepath.Join(a, "plugins"))
	if _, err := publishSnapshot(filepath.Join(a, "plugins"), same, root, testGuard()); err != nil {
		t.Fatalf("the checked dir itself must publish: %v", err)
	}
}

// A sibling Ensure that publishes the same snapshot first must not make this
// one fail, and its snapshot must never be deleted by us.
func TestPublishSnapshotKeepsASiblingsCompleteSnapshot(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	root := t.TempDir()
	pdir := filepath.Join(src, "plugins")
	fi, _ := os.Lstat(pdir)
	first, err := publishSnapshot(pdir, fi, root, testGuard())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(first["cache"], "official", "sentinel")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same content again: every marker matches, so the existing complete dirs
	// are returned untouched.
	again, err := publishSnapshot(pdir, fi, root, testGuard())
	if err != nil || again["cache"] != first["cache"] {
		t.Fatalf("second publish: %v %q", err, again["cache"])
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("a complete snapshot was removed or replaced")
	}
	// An incomplete leftover (no marker) IS replaced, and only that slot.
	slot := filepath.Dir(first["cache"])
	if err := os.Remove(filepath.Join(slot, snapshotMarker)); err != nil {
		t.Fatal(err)
	}
	jsonSlot, _ := os.Lstat(filepath.Join(filepath.Dir(first["installed_plugins.json"]), snapshotMarker))
	healed, err := publishSnapshot(pdir, fi, root, testGuard())
	token := strings.TrimPrefix(filepath.Base(slot), "plugins-cache-")
	if err != nil || healed["cache"] != first["cache"] || !snapshotComplete(slot, token) {
		t.Fatalf("an incomplete leftover must be replaced: %v", err)
	}
	if now, _ := os.Lstat(filepath.Join(filepath.Dir(healed["installed_plugins.json"]), snapshotMarker)); !sameSlot(jsonSlot, now) {
		t.Fatal("healing the cache slot must not touch the json slot")
	}
}
