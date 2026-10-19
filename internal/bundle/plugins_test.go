package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
	return pluginSnapshot(src, root), root
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
		// Mounts point into the snapshot, never into the host config dir.
		snapDir := filepath.Join(root, "plugins-"+ps.token)
		if mt.Host != filepath.Join(snapDir, filepath.Base(mt.Box)) {
			t.Fatalf("mount %d host %q is not inside %s", i, mt.Host, snapDir)
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
	for _, never := range []string{"data", "store", "plugin-catalog-cache.json", "plugin-directory-cache-v2.json"} {
		if _, err := os.Lstat(filepath.Join(root, "plugins-"+ps.token, never)); err == nil {
			t.Fatalf("%q must never be copied into the snapshot", never)
		}
	}
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
	const reason = "is a symlink on the host; agent-box mounts only real files and directories"
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
			want := Drop{Kind: "plugins", Name: c.file, Reason: "a plugin marketplace URL carries credentials; agent-box never mounts credentials into a box"}
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
	done := make(chan pluginSet, 1)
	go func() { ps, _ := snap(t, src); done <- ps }()
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

	ps, root := snap(t, src)
	if len(ps.mounts) != 4 || len(ps.drops) != 0 {
		t.Fatalf("a git-bearing set still mounts (the git data is simply absent): %+v", ps)
	}
	paths := snapshotPaths(t, filepath.Join(root, "plugins-"+ps.token))
	for rel := range paths {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if part == ".git" {
				t.Fatalf("snapshot contains git metadata: %s", rel)
			}
		}
	}
	for _, keep := range []string{"marketplaces/m/real.txt", "cache/a/keep.txt", "cache/a/sub"} {
		if _, ok := paths[filepath.FromSlash(keep)]; !ok {
			t.Fatalf("%s must be copied", keep)
		}
	}
	// Nothing secret anywhere in the bytes either.
	err := filepath.WalkDir(filepath.Join(root, "plugins-"+ps.token), func(p string, d os.DirEntry, err error) error {
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

// 2: a symlink INSIDE the tree is recreated as a symlink with the same target
// string and never followed, so a secret behind it never enters the snapshot.
func TestSnapshotRecreatesSymlinksWithoutFollowing(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(src, "plugins", "cache", "official", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/home/claude/.claude/plugins/cache/x", filepath.Join(src, "plugins", "cache", "abs")); err != nil {
		t.Fatal(err)
	}
	ps, root := snap(t, src)
	if len(ps.mounts) != 4 {
		t.Fatalf("a link inside the tree does not void the set: %+v", ps)
	}
	snapDir := filepath.Join(root, "plugins-"+ps.token)
	got, err := os.Readlink(filepath.Join(snapDir, "cache", "official", "link"))
	if err != nil || got != outside {
		t.Fatalf("link must keep its target string: %q %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(snapDir, "cache", "abs")); err != nil || got != "/home/claude/.claude/plugins/cache/x" {
		t.Fatalf("absolute link must keep its target string: %q %v", got, err)
	}
	filepath.WalkDir(snapDir, func(p string, d os.DirEntry, err error) error {
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
	done := make(chan []string, 1)
	go func() {
		ps, root := snap(t, src)
		var got []string
		if len(ps.mounts) == 4 {
			for p := range snapshotPaths(t, filepath.Join(root, "plugins-"+ps.token)) {
				got = append(got, p)
			}
		}
		done <- got
	}()
	select {
	case got := <-done:
		var ok, pipe bool
		for _, p := range got {
			ok = ok || p == filepath.Join("cache", "a", "ok.txt")
			pipe = pipe || filepath.Base(p) == "pipe"
		}
		if !ok || pipe {
			t.Fatalf("want ok.txt copied and pipe skipped, got %v", got)
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
	ps, root := snap(t, src)
	paths := snapshotPaths(t, filepath.Join(root, "plugins-"+ps.token))
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
	first := pluginSnapshot(src, root)
	ino := func(token string) os.FileInfo {
		fi, err := os.Stat(filepath.Join(root, "plugins-"+token, "installed_plugins.json"))
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	before := ino(first.token)

	if err := os.WriteFile(filepath.Join(src, "skills", "demo", "SKILL.md"), []byte("# edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := pluginSnapshot(src, root)
	if again.token != first.token {
		t.Fatal("editing a skill must not change the plugin snapshot")
	}
	if !os.SameFile(before, ino(again.token)) {
		t.Fatal("the snapshot must be reused, not recopied")
	}

	writePluginFile(t, src, "cache/official/demo/1.0.0/new.txt", "new")
	changed := pluginSnapshot(src, root)
	if changed.token == first.token {
		t.Fatal("editing a file under cache must produce a new snapshot")
	}
	if changed.mounts[2].Host == first.mounts[2].Host {
		t.Fatal("the new snapshot must be a new dir")
	}
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
