package config

import (
	"os"
	"path/filepath"
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
	root := filepath.Join(home, ".local", "share", "agent-box")
	writeCfg(t, filepath.Join(home, ".config", "agent-box", "config.json"), "127.0.0.1:1")
	writeCfg(t, filepath.Join(root, "config.json"), "127.0.0.1:4")
	c, src, err := Resolve("", false, home, "", "")
	if err != nil || c.BindAddr != "127.0.0.1:4" || src.Path != filepath.Join(root, "config.json") {
		t.Fatalf("root config not preferred: %+v %+v %v", c, src, err)
	}
}

func TestResolve_DataDirDefaultsToTheRoot(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "agent-box")
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

// ConfigDirs is every directory Resolve reads a config file from on the next
// start, whether or not the file exists yet: a box that could write one could
// plant a config naming the binaries agent-box runs.
func TestConfigDirs(t *testing.T) {
	home := "/h"
	got, err := ConfigDirs("", false, home, "", "")
	if err != nil || len(got) != 2 || got[0] != "/h/.local/share/agent-box" || got[1] != "/h/.config/agent-box" {
		t.Fatalf("default: %v %v", got, err)
	}
	got, _ = ConfigDirs("", false, home, "/x", "")
	if got[1] != "/x/agent-box" {
		t.Fatalf("xdg: %v", got)
	}
	got, _ = ConfigDirs("", false, home, "/x", "/ab")
	if len(got) != 1 || got[0] != "/ab" {
		t.Fatalf("relocated: %v", got)
	}
	got, _ = ConfigDirs("/etc/ab/c.json", true, home, "", "")
	if len(got) != 1 || got[0] != "/etc/ab" {
		t.Fatalf("explicit: %v", got)
	}
	got, _ = ConfigDirs("c.json", true, home, "", "")
	if wd, _ := os.Getwd(); len(got) != 1 || got[0] != wd {
		t.Fatalf("a relative explicit path must resolve against the cwd: %v", got)
	}
	if _, err := ConfigDirs("", false, home, "", "rel"); err == nil {
		t.Fatal("a relative AGENT_BOX_HOME must be an error")
	}
}
