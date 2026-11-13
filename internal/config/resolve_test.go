package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, path, bind string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"bind_addr":"` + bind + `","agents":[{"name":"a","bin":"a"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// privateDir is a t.TempDir with mode 700: t.TempDir follows the umask, and a
// group-writable root is refused.
func privateDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestResolve_Order(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Chdir(work)

	// 3. nothing anywhere → built-in defaults
	c, src, err := Resolve("", false, home, "", "")
	if err != nil || !src.Default || c.BindAddr != "127.0.0.1:8899" {
		t.Fatalf("defaults: %+v %+v %v", c, src, err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if c.PasswordEnv != "" || c.DefaultRuntime != RuntimeHost {
		t.Errorf("defaults must be token mode + host tier: %+v", c)
	}

	// 2. ~/.config/agent-box/config.json
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:1")
	c, src, _ = Resolve("", false, home, "", "")
	if c.BindAddr != "127.0.0.1:1" || src.Default {
		t.Errorf("home config not used: %+v %+v", c, src)
	}

	// 2'. $XDG_CONFIG_HOME wins over ~/.config when absolute
	xdg := t.TempDir()
	writeCfg(t, filepath.Join(xdg, "agent-box", "config.json"), "127.0.0.1:2")
	c, _, _ = Resolve("", false, home, xdg, "")
	if c.BindAddr != "127.0.0.1:2" {
		t.Errorf("XDG config not used: %+v", c)
	}
	c, _, _ = Resolve("", false, home, "relative/xdg", "")
	if c.BindAddr != "127.0.0.1:1" {
		t.Errorf("a relative XDG_CONFIG_HOME must be ignored: %+v", c)
	}

	// A ./config.json in the cwd is IGNORED: running agent-box inside a cloned
	// hostile repo must not load that repo's config.
	writeCfg(t, filepath.Join(work, "config.json"), "127.0.0.1:3")
	c, src, _ = Resolve("", false, home, xdg, "")
	if c.BindAddr != "127.0.0.1:2" || src.Path == "config.json" {
		t.Errorf("cwd config must be ignored: %+v %+v", c, src)
	}
	os.RemoveAll(filepath.Join(home, ".config"))
	os.RemoveAll(xdg)
	c, src, _ = Resolve("", false, home, "", "")
	if !src.Default || c.BindAddr != "127.0.0.1:8899" {
		t.Errorf("only a cwd config present: want built-in defaults, got %+v %+v", c, src)
	}
}

func TestResolve_ExplicitMissingIsFatal(t *testing.T) {
	if _, _, err := Resolve(filepath.Join(t.TempDir(), "nope.json"), true, t.TempDir(), "", ""); err == nil {
		t.Fatal("an explicit -config that does not exist must be an error")
	}
}

// The data root holds everything agent-box writes, its config included, so one
// directory is the whole install: back it up, move it, or delete it.
func TestResolve_ConfigInsideTheRootWins(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "culibox")
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:1")
	writeCfg(t, filepath.Join(root, "config.json"), "127.0.0.1:4")
	c, src, err := Resolve("", false, home, "", "")
	if err != nil || c.BindAddr != "127.0.0.1:4" || src.Path != filepath.Join(root, "config.json") {
		t.Fatalf("root config not preferred: %+v %+v %v", c, src, err)
	}
}

func TestResolve_DataDirDefaultsToTheRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "culibox")
	c, _, err := Resolve("", false, home, "", "")
	if err != nil || c.DataDir != root {
		t.Fatalf("defaults: DataDir=%q err=%v, want %q", c.DataDir, err, root)
	}
	// ~/.config fallback, no data_dir in it: still the root.
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:1")
	if c, _, _ = Resolve("", false, home, "", ""); c.DataDir != root {
		t.Fatalf("xdg config: DataDir=%q, want %q", c.DataDir, root)
	}
	// An explicit -config without data_dir: the root.
	explicit := filepath.Join(t.TempDir(), "c.json")
	writeCfg(t, explicit, "127.0.0.1:5")
	if c, _, _ = Resolve(explicit, true, home, "", ""); c.DataDir != root {
		t.Fatalf("explicit config: DataDir=%q, want %q", c.DataDir, root)
	}
	// data_dir set in the file always wins.
	body := `{"bind_addr":"127.0.0.1:6","data_dir":"/srv/ab","agents":[{"name":"a","bin":"a"}]}`
	if err := os.WriteFile(explicit, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _, _ = Resolve(explicit, true, home, "", ""); c.DataDir != "/srv/ab" {
		t.Fatalf("data_dir from the file overridden: %q", c.DataDir)
	}
}

// AGENT_BOX_HOME moves the whole install. A second instance pointed elsewhere
// must not pick up the first one's ~/.config file.
func TestResolve_AgentBoxHome(t *testing.T) {
	home := t.TempDir()
	ab := privateDir(t)
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:1")
	c, src, err := Resolve("", false, home, "", ab)
	if err != nil || !src.Default || c.DataDir != ab {
		t.Fatalf("AGENT_BOX_HOME with no config in it: %+v %+v %v; want defaults rooted at %q", c, src, err, ab)
	}
	writeCfg(t, filepath.Join(ab, "config.json"), "127.0.0.1:7")
	c, src, err = Resolve("", false, home, "", ab)
	if err != nil || c.BindAddr != "127.0.0.1:7" || src.Path != filepath.Join(ab, "config.json") || c.DataDir != ab {
		t.Fatalf("AGENT_BOX_HOME config: %+v %+v %v", c, src, err)
	}
}

func TestResolve_RelativeAgentBoxHomeIsAnError(t *testing.T) {
	if _, _, err := Resolve("", false, t.TempDir(), "", "rel/ab"); err == nil {
		t.Fatal("a relative AGENT_BOX_HOME must be refused, not resolved against the cwd")
	}
}

// The config names the binaries agent-box runs, so it is only read from a root
// nobody else can write into (AGENT_BOX_HOME may point anywhere).
func TestResolve_RefusesAWritableRoot(t *testing.T) {
	ab := privateDir(t)
	writeCfg(t, filepath.Join(ab, "config.json"), "127.0.0.1:7")
	if err := os.Chmod(ab, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve("", false, t.TempDir(), "", ab); err == nil {
		t.Fatal("read a config from a world-writable root")
	}
	if err := os.Chmod(ab, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve("", false, t.TempDir(), "", ab); err == nil {
		t.Fatal("read a config from a group-writable root (macOS: group staff is every local user)")
	}
	if err := os.Chmod(ab, 0o755); err != nil {
		t.Fatal(err)
	}
	if c, _, err := Resolve("", false, t.TempDir(), "", ab); err != nil || c.BindAddr != "127.0.0.1:7" {
		t.Fatalf("a 0755 root of our own must be read: %+v %v", c, err)
	}
}

func TestRoot_RelativeHomeIsAnError(t *testing.T) {
	if _, err := Root("", ""); err == nil {
		t.Fatal("an empty home must not yield a relative root")
	}
	if _, err := Root("rel/home", ""); err == nil {
		t.Fatal("a relative home must not yield a relative root")
	}
}

// A config file others can edit is as bad as a root they can write into, in the
// root and in the ~/.config fallback alike.
func TestResolve_RefusesAWritableConfigFile(t *testing.T) {
	home := privateDir(t)
	for _, p := range []string{
		filepath.Join(home, ".local", "share", "agent-box", "config.json"),
		filepath.Join(home, ".config", "agent-box", "config.json"),
	} {
		writeCfg(t, p, "127.0.0.1:8")
		if err := os.Chmod(p, 0o666); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Resolve("", false, home, "", ""); err == nil {
			t.Fatalf("read a world-writable %s", p)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
}

// ConfigFiles is every file Resolve reads a config from on the next start,
// whether or not it exists yet: a box that could write one could plant a
// config naming the binaries agent-box runs.
func TestConfigFiles(t *testing.T) {
	home := "/h"
	got, err := ConfigFiles("", false, home, "", "")
	want := []string{"/h/.local/share/culibox/config.json", "/h/.config/culibox/config.json",
		"/h/.config/agent-box/config.json", "/h/.local/share/agent-box/config.json"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("default: %v %v, want %v", got, err, want)
	}
	got, _ = ConfigFiles("", false, home, "/x", "")
	if got[1] != "/x/culibox/config.json" || got[2] != "/x/agent-box/config.json" {
		t.Fatalf("xdg: %v", got)
	}
	got, _ = ConfigFiles("", false, home, "/x", "/ab")
	if len(got) != 1 || got[0] != "/ab/config.json" {
		t.Fatalf("relocated: %v", got)
	}
	got, _ = ConfigFiles("/etc/ab/c.json", true, home, "", "")
	if len(got) != 1 || got[0] != "/etc/ab/c.json" {
		t.Fatalf("explicit: %v", got)
	}
	got, _ = ConfigFiles("c.json", true, home, "", "")
	if wd, _ := os.Getwd(); len(got) != 1 || got[0] != filepath.Join(wd, "c.json") {
		t.Fatalf("a relative explicit path must resolve against the cwd: %v", got)
	}
	if _, err := ConfigFiles("", false, home, "", "rel"); err == nil {
		t.Fatal("a relative AGENT_BOX_HOME must be an error")
	}
}

// --- culibox root, legacy roots, env vars, XDG candidates (#255) ---

func rootsOf(t *testing.T) (home, newRoot, legacy string) {
	t.Helper()
	home = privateDir(t)
	newRoot, legacy = DefaultRoots(home)
	if err := os.MkdirAll(filepath.Dir(newRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	return home, newRoot, legacy
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRoots(t *testing.T) {
	n, l := DefaultRoots("/h")
	if n != "/h/.local/share/culibox" || l != "/h/.local/share/agent-box" {
		t.Fatalf("got %q %q", n, l)
	}
}

func TestResolveRootFreshUsesNew(t *testing.T) {
	home, n, _ := rootsOf(t)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: n, Kind: RootNew}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestResolveRootLegacyOnly(t *testing.T) {
	home, _, l := rootsOf(t)
	mkdir(t, l)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: l, Kind: RootLegacy}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestResolveRootNewOnly(t *testing.T) {
	home, n, _ := rootsOf(t)
	mkdir(t, n)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: n, Kind: RootNew}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// The link a migration leaves behind: legacy -> new.
func TestResolveRootLinkedLegacy(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, n)
	touch(t, filepath.Join(n, "config.json"))
	symlink(t, n, l)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: n, Kind: RootLinked}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// The same directory the other way round: the real directory's spelling wins
// so the hash-derived names stay stable.
func TestResolveRootNewIsLinkToLegacy(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	touch(t, filepath.Join(l, "config.json"))
	symlink(t, l, n)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: l, Kind: RootLegacy}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// Ruling R3: a legacy symlink to some other directory is used as spelled, not
// refused and not resolved. rootmove skips it later because it is not a real dir.
func TestResolveRootLegacySymlinkElsewhere(t *testing.T) {
	home, _, l := rootsOf(t)
	elsewhere := filepath.Join(home, "elsewhere")
	mkdir(t, elsewhere)
	symlink(t, elsewhere, l)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: l, Kind: RootLegacy}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestResolveRootEmptyNewUsesLegacy(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	touch(t, filepath.Join(l, "config.json"))
	mkdir(t, n)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: l, Kind: RootLegacy, EmptyNew: true}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// Both empty: deterministic, legacy with EmptyNew (it holds the original names).
func TestResolveRootBothEmptyPrefersLegacy(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	mkdir(t, n)
	got, err := ResolveRoot(home, "")
	if err != nil || got != (RootInfo{Path: l, Kind: RootLegacy, EmptyNew: true}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestResolveRootBothRefused(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	touch(t, filepath.Join(l, "a"))
	mkdir(t, n)
	touch(t, filepath.Join(n, "b"))
	_, err := ResolveRoot(home, "")
	if err == nil {
		t.Fatal("two different non-empty roots must be refused")
	}
	for _, want := range []string{n, l, "CULIBOX_HOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// A new root that is a symlink to some third directory is not "empty" either.
	if err := os.RemoveAll(n); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(home, "third")
	mkdir(t, third)
	symlink(t, third, n)
	if _, err := ResolveRoot(home, ""); err == nil {
		t.Fatal("a new-root symlink to another directory next to a legacy root must be refused")
	}
}

func TestResolveRootBrokenLegacyLinkRefused(t *testing.T) {
	home, n, l := rootsOf(t)
	symlink(t, filepath.Join(home, "missing"), l)
	for _, withNew := range []bool{false, true} {
		if withNew {
			mkdir(t, n)
		}
		_, err := ResolveRoot(home, "")
		if err == nil || !strings.Contains(err.Error(), l) {
			t.Fatalf("withNew=%v: want an error naming %s, got %v", withNew, l, err)
		}
	}
}

func TestResolveRootBrokenNewLinkRefused(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	touch(t, filepath.Join(l, "a"))
	symlink(t, filepath.Join(home, "missing"), n)
	_, err := ResolveRoot(home, "")
	if err == nil || !strings.Contains(err.Error(), n) || !strings.Contains(err.Error(), "broken link") {
		t.Fatalf("want a broken-link error naming %s, got %v", n, err)
	}
	if strings.Contains(err.Error(), "different content") {
		t.Fatalf("a dangling link is not a content clash: %v", err)
	}
}

// Resolve labels a root refusal with the env var only when one was used.
func TestResolveRootErrorLabel(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	touch(t, filepath.Join(l, "a"))
	mkdir(t, n)
	touch(t, filepath.Join(n, "b"))
	_, src, err := Resolve("", false, home, "", "")
	if err == nil || src.Path != "data root" {
		t.Fatalf("two roots: label %q err %v; want %q", src.Path, err, "data root")
	}
	_, src, err = Resolve("", false, home, "", "rel")
	if err == nil || src.Path != "$CULIBOX_HOME" {
		t.Fatalf("relative env: label %q err %v; want $CULIBOX_HOME", src.Path, err)
	}
}

func TestResolveRootEnvWins(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, n)
	mkdir(t, l)
	touch(t, filepath.Join(n, "a"))
	touch(t, filepath.Join(l, "b"))
	got, err := ResolveRoot(home, "/x/../x")
	if err != nil || got != (RootInfo{Path: "/x", Kind: RootEnv}) {
		t.Fatalf("got %+v %v", got, err)
	}
	_, err = ResolveRoot(home, "rel/x")
	if err == nil || !strings.Contains(err.Error(), "CULIBOX_HOME") {
		t.Fatalf("relative env: %v", err)
	}
	if _, err := ResolveRoot("rel/home", ""); err == nil {
		t.Fatal("a relative home must be refused")
	}
}

func TestRootKeepsItsSignature(t *testing.T) {
	home, _, l := rootsOf(t)
	mkdir(t, l)
	if got, err := Root(home, ""); err != nil || got != l {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestHomeEnv(t *testing.T) {
	env := func(c, a string) func(string) string {
		return func(k string) string {
			switch k {
			case "CULIBOX_HOME":
				return c
			case "AGENT_BOX_HOME":
				return a
			}
			return ""
		}
	}
	cases := []struct {
		name, c, a, val, notice string
		err                     string
	}{
		{"none", "", "", "", "", ""},
		{"new only", "/a", "", "/a", "", ""},
		{"legacy only", "", "/a", "/a", "AGENT_BOX_HOME is deprecated; rename it to CULIBOX_HOME", ""},
		{"same", "/a", "/a/", "/a", "AGENT_BOX_HOME is deprecated and set to the same path as CULIBOX_HOME; unset it", ""},
		{"differ", "/a", "/b", "", "", "CULIBOX_HOME (/a) and AGENT_BOX_HOME (/b) disagree; unset one"},
		{"relative new", "rel", "", "", "", `CULIBOX_HOME "rel" is not an absolute path`},
		{"relative legacy", "", "rel", "", "", `AGENT_BOX_HOME "rel" is not an absolute path`},
		{"relative both same", "rel", "rel", "", "", `CULIBOX_HOME "rel" is not an absolute path`},
	}
	for _, tc := range cases {
		v, n, err := HomeEnv(env(tc.c, tc.a))
		if v != tc.val || n != tc.notice {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, v, n, tc.val, tc.notice)
		}
		if (err == nil) != (tc.err == "") || (err != nil && err.Error() != tc.err) {
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.err)
		}
	}
}

func TestCandidatesOrder(t *testing.T) {
	got := candidates("/r", "/h", "", false)
	want := []string{"/r/config.json", "/h/.config/culibox/config.json", "/h/.config/agent-box/config.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	got = candidates("/r", "/h", "/x", false)
	want = []string{"/r/config.json", "/x/culibox/config.json", "/x/agent-box/config.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("xdg: got %v", got)
	}
	if got = candidates("/r", "/h", "/x", true); !reflect.DeepEqual(got, []string{"/r/config.json"}) {
		t.Fatalf("relocated: got %v", got)
	}
}

func TestLoadBothXDGRefused(t *testing.T) {
	home := privateDir(t)
	writeCfg(t, filepath.Join(home, ".config", "culibox", "config.json"), "127.0.0.1:1")
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:2")
	_, _, err := Resolve("", false, home, "", "")
	if err == nil || !strings.Contains(err.Error(), "keep one") ||
		!strings.Contains(err.Error(), filepath.Join(home, ".config", "culibox", "config.json")) ||
		!strings.Contains(err.Error(), filepath.Join(home, ".config", "agent-box", "config.json")) {
		t.Fatalf("got %v", err)
	}
	// A root config is reached first, so the pair is never looked at.
	writeCfg(t, filepath.Join(home, ".local", "share", "culibox", "config.json"), "127.0.0.1:3")
	if c, _, err := Resolve("", false, home, "", ""); err != nil || c.BindAddr != "127.0.0.1:3" {
		t.Fatalf("root config must win without looking at the XDG pair: %+v %v", c, err)
	}
}

func TestLoadXDGSameFileOK(t *testing.T) {
	home := privateDir(t)
	nf := filepath.Join(home, ".config", "culibox", "config.json")
	writeCfg(t, nf, "127.0.0.1:1")
	mkdir(t, filepath.Join(home, ".config", "agent-box"))
	symlink(t, nf, filepath.Join(home, ".config", "agent-box", "config.json"))
	c, src, err := Resolve("", false, home, "", "")
	if err != nil || c.BindAddr != "127.0.0.1:1" || src.Path != nf {
		t.Fatalf("got %+v %+v %v", c, src, err)
	}
}

func TestResolveFillsSourceRoot(t *testing.T) {
	home, _, l := rootsOf(t)
	mkdir(t, l)
	_, src, err := Resolve("", false, home, "", "")
	if err != nil || src.Root != (RootInfo{Path: l, Kind: RootLegacy}) {
		t.Fatalf("got %+v %v", src.Root, err)
	}
}

// Both default roots are protected whichever is in use.
func TestConfigFilesIncludesOtherDefaultRoot(t *testing.T) {
	home, n, l := rootsOf(t)
	mkdir(t, l)
	got, err := ConfigFiles("", false, home, "", "")
	if err != nil || got[len(got)-1] != filepath.Join(n, "config.json") {
		t.Fatalf("legacy in use: %v %v", got, err)
	}
	if got[0] != filepath.Join(l, "config.json") {
		t.Fatalf("root first: %v", got)
	}
	if err := os.RemoveAll(l); err != nil {
		t.Fatal(err)
	}
	got, _ = ConfigFiles("", false, home, "", "")
	if got[0] != filepath.Join(n, "config.json") || got[len(got)-1] != filepath.Join(l, "config.json") {
		t.Fatalf("new in use: %v", got)
	}
}

// --- #266: a ~/.config/culibox file read while the data root is the old one ---

func xdgFile(home, name string) string { return filepath.Join(home, ".config", name, "config.json") }

func TestUnvettedConfigWarning(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home, newRoot, legacy string)
		moved bool
		warn  bool
	}{
		{"culibox file, legacy root", func(t *testing.T, home, _, legacy string) {
			mkdir(t, legacy)
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, false, true},
		{"culibox file, legacy root moved this start", func(t *testing.T, home, newRoot, legacy string) {
			mkdir(t, newRoot)
			symlink(t, newRoot, legacy)
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, true, true},
		{"culibox file, root already moved earlier", func(t *testing.T, home, newRoot, legacy string) {
			mkdir(t, newRoot)
			symlink(t, newRoot, legacy)
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, false, false},
		{"fresh culibox root", func(t *testing.T, home, newRoot, _ string) {
			mkdir(t, newRoot)
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, false, false},
		{"nothing exists yet", func(t *testing.T, home, _, _ string) {
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, false, false},
		{"config in the data root wins", func(t *testing.T, home, _, legacy string) {
			writeCfg(t, filepath.Join(legacy, "config.json"), "127.0.0.1:2")
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
		}, false, false},
		{"only the agent-box file", func(t *testing.T, home, _, legacy string) {
			mkdir(t, legacy)
			writeCfg(t, xdgFile(home, "agent-box"), "127.0.0.1:1")
		}, false, false},
		{"agent-box file moved to culibox by serve (link left)", func(t *testing.T, home, _, legacy string) {
			mkdir(t, legacy)
			writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
			mkdir(t, filepath.Dir(xdgFile(home, "agent-box")))
			symlink(t, xdgFile(home, "culibox"), xdgFile(home, "agent-box"))
		}, false, false},
		{"no config at all, legacy root", func(t *testing.T, _, _, legacy string) { mkdir(t, legacy) }, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, newRoot, legacy := rootsOf(t)
			tc.setup(t, home, newRoot, legacy)
			_, src, err := Resolve("", false, home, "", "")
			if err != nil {
				t.Fatal(err)
			}
			got := src.UnvettedWarning(tc.moved)
			if (got != "") != tc.warn {
				t.Fatalf("warning %q, want warn=%v (source %+v)", got, tc.warn, src)
			}
			if tc.warn && !strings.Contains(got, xdgFile(home, "culibox")) {
				t.Errorf("warning must name the file: %q", got)
			}
		})
	}
}

// A relocated instance and an explicit -config never read the ~/.config file.
func TestUnvettedConfigWarningNotForEnvOrExplicit(t *testing.T) {
	home, _, legacy := rootsOf(t)
	mkdir(t, legacy)
	writeCfg(t, xdgFile(home, "culibox"), "127.0.0.1:1")
	env := privateDir(t)
	if _, src, err := Resolve("", false, home, "", env); err != nil || src.UnvettedWarning(false) != "" {
		t.Errorf("env root: %+v %v", src, err)
	}
	f := filepath.Join(privateDir(t), "c.json")
	writeCfg(t, f, "127.0.0.1:3")
	if _, src, err := Resolve(f, true, home, "", ""); err != nil || src.UnvettedWarning(false) != "" {
		t.Errorf("-config: %+v %v", src, err)
	}
}

// $XDG_CONFIG_HOME moves the file: the warning names the path actually read.
func TestUnvettedConfigWarningHonoursXDG(t *testing.T) {
	home, _, legacy := rootsOf(t)
	mkdir(t, legacy)
	xdg := t.TempDir()
	writeCfg(t, filepath.Join(xdg, "culibox", "config.json"), "127.0.0.1:1")
	_, src, err := Resolve("", false, home, xdg, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := src.UnvettedWarning(false); !strings.Contains(got, filepath.Join(xdg, "culibox", "config.json")) {
		t.Fatalf("got %q", got)
	}
}
