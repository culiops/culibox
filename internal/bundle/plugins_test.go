package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
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

func wantPluginMounts(src string) []box.Mount {
	var out []box.Mount
	for _, e := range []string{"installed_plugins.json", "known_marketplaces.json", "cache", "marketplaces"} {
		out = append(out, box.Mount{
			Host:     filepath.Join(src, "plugins", e),
			Box:      "/home/agent/.claude/plugins/" + e,
			Optional: true,
		})
	}
	return out
}

// Measured 2026-10-19: claude rebases the absolute installPath in
// installed_plugins.json onto its own config dir, so all plugins load with only
// the home mounts. A second mount at the host's own path would be redundant and
// would leak the host layout into the box.
func TestPluginMountsMountEachEntryOnce(t *testing.T) {
	src := t.TempDir()
	pluginTree(t, src)
	mounts, drops := pluginMounts(src)
	if len(drops) != 0 {
		t.Fatalf("a complete set has no drops: %+v", drops)
	}
	want := wantPluginMounts(src)
	if len(mounts) != len(want) {
		t.Fatalf("want exactly 4 mounts, got %+v", mounts)
	}
	for i := range want {
		if mounts[i] != want[i] {
			t.Fatalf("mount %d = %+v, want %+v", i, mounts[i], want[i])
		}
	}
	for _, mt := range mounts {
		if strings.HasPrefix(mt.Box, src) {
			t.Fatalf("%+v mounts at the host path; only the box home destination is wanted", mt)
		}
		for _, never := range []string{"data", "store", "plugin-catalog-cache.json", "plugin-directory-cache-v2.json"} {
			if filepath.Base(mt.Box) == never || filepath.Base(mt.Host) == never {
				t.Fatalf("%q must never be mounted: %+v", never, mt)
			}
		}
	}
}

// No installed_plugins.json means the user has no plugins: that is not a fault
// worth a drop row.
func TestPluginMountsNoInstalledJSONIsSilent(t *testing.T) {
	src := t.TempDir()
	if m, d := pluginMounts(src); m != nil || d != nil {
		t.Fatalf("absent plugins/ must be silent, got %+v %+v", m, d)
	}
	pluginTree(t, src)
	if err := os.Remove(filepath.Join(src, "plugins", "installed_plugins.json")); err != nil {
		t.Fatal(err)
	}
	if m, d := pluginMounts(src); m != nil || d != nil {
		t.Fatalf("plugins/ without installed_plugins.json must be silent, got %+v %+v", m, d)
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
	mounts, drops := pluginMounts(src)
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
		mounts, drops := pluginMounts(src)
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
		mounts, drops := pluginMounts(src)
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
	if err := os.MkdirAll(filepath.Join(src, "plugins", "marketplaces"), 0o700); err != nil {
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

// The rule, stated exactly: a URL whose authority carries userinfo. For http and
// https any userinfo (user or user:pass) is a credential, because that is how a
// token is spelled (https://<token>@github.com/...). For every other scheme only
// a password part (user:pass@) counts, so ssh://git@host/... is fine. scp-style
// git@github.com:o/r.git has no scheme, so it is never userinfo. An @ after the
// first / ? or # is not userinfo.
func TestHasURLCredentialsRule(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"url = https://tok@github.com/o/r", true},
		{"url = http://tok@github.com/o/r", true},
		{"url = HTTPS://user:pw@github.com/o/r.git", true},
		{`{"url":"https://tok@github.com/o/r"}`, true},
		{"url = ssh://git:pw@host/o/r", true},
		{"url = ssh://git@host/o/r", false},
		{"url = git@github.com:o/r.git", false},
		{"url = https://github.com/o/r", false},
		{"url = https://github.com/o/r?e=a@b", false},
		{"url = https://github.com/o/r#a@b", false},
		{"url = https://github.com/o/blob/x@y", false},
		{"url = https://@github.com/o/r", false},
	} {
		if got := hasURLCredentials([]byte(c.in)); got != c.want {
			t.Errorf("hasURLCredentials(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A marketplace cloned with a token leaves it in .git/config. Mounting that
// would publish a credential to a box with network egress, breaking the
// bundle's secret-free rule, so the WHOLE set is refused.
func TestPluginMountsRefuseCredentialedURLs(t *testing.T) {
	const url = "https://tok@github.com/o/private-marketplace"
	for _, c := range []struct {
		name, rel, content, entry string
	}{
		{"marketplace .git/config", "marketplaces/x/.git/config", gitConfig(url), "marketplaces"},
		{"nested cache .git/config", "cache/a/b/c/.git/config", gitConfig(url), "cache"},
		{"known_marketplaces.json", "known_marketplaces.json", `{"m":{"source":{"source":"git","url":"` + url + `"}}}`, "known_marketplaces.json"},
		{"installed_plugins.json", "installed_plugins.json", `{"version":2,"plugins":{"p@m":[{"gitUrl":"` + url + `"}]}}`, "installed_plugins.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := t.TempDir()
			pluginTree(t, src)
			writePluginFile(t, src, c.rel, c.content)
			mounts, drops := pluginMounts(src)
			if len(mounts) != 0 {
				t.Fatalf("a credentialed URL must void the whole set: %+v", mounts)
			}
			want := Drop{Kind: "plugins", Name: c.entry, Reason: "a plugin marketplace URL carries credentials; agent-box never mounts credentials into a box"}
			if len(drops) != 1 || drops[0] != want {
				t.Fatalf("want exactly %+v, got %+v", want, drops)
			}
			if strings.Contains(drops[0].Reason+drops[0].Name, "tok") || strings.Contains(drops[0].Reason+drops[0].Name, "github") {
				t.Fatalf("the drop must never carry the URL: %+v", drops[0])
			}
		})
	}
}

// Credential-free remotes, including scp-style ssh, mount normally.
func TestPluginMountsAllowCredentialFreeURLs(t *testing.T) {
	for _, url := range []string{"git@github.com:o/r.git", "https://github.com/o/r", "ssh://git@github.com/o/r"} {
		src := t.TempDir()
		pluginTree(t, src)
		writePluginFile(t, src, "marketplaces/x/.git/config", gitConfig(url))
		writePluginFile(t, src, "known_marketplaces.json", `{"m":{"source":{"url":"`+url+`"}}}`)
		mounts, drops := pluginMounts(src)
		if len(mounts) != 4 || len(drops) != 0 {
			t.Fatalf("%s must mount normally: %+v %+v", url, mounts, drops)
		}
	}
}

// The scan is bounded and never follows a symlink: a .git/config past the depth
// bound, or one reached only through a link, is not read.
func TestPluginCredentialScanIsBoundedAndDoesNotFollowLinks(t *testing.T) {
	const url = "https://tok@github.com/o/r"
	src := t.TempDir()
	pluginTree(t, src)
	writePluginFile(t, src, "cache/a/b/c/d/e/f/.git/config", gitConfig(url))
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, ".git", "config"), []byte(gitConfig(url)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "plugins", "marketplaces", "linked")); err != nil {
		t.Fatal(err)
	}
	if mounts, drops := pluginMounts(src); len(mounts) != 4 || len(drops) != 0 {
		t.Fatalf("out-of-bound and linked configs are not scanned: %+v %+v", mounts, drops)
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
	writePluginFile(t, src, "marketplaces/x/.git/config", gitConfig("https://tok@github.com/o/r"))
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
