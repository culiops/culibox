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

func TestResolve_Order(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Chdir(work)

	// 3. nothing anywhere → built-in defaults
	c, src, err := Resolve("", false, home, "")
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
	c, src, _ = Resolve("", false, home, "")
	if c.BindAddr != "127.0.0.1:1" || src.Default {
		t.Errorf("home config not used: %+v %+v", c, src)
	}

	// 2'. $XDG_CONFIG_HOME wins over ~/.config when absolute
	xdg := t.TempDir()
	writeCfg(t, filepath.Join(xdg, "agent-box", "config.json"), "127.0.0.1:2")
	c, _, _ = Resolve("", false, home, xdg)
	if c.BindAddr != "127.0.0.1:2" {
		t.Errorf("XDG config not used: %+v", c)
	}
	c, _, _ = Resolve("", false, home, "relative/xdg")
	if c.BindAddr != "127.0.0.1:1" {
		t.Errorf("a relative XDG_CONFIG_HOME must be ignored: %+v", c)
	}

	// A ./config.json in the cwd is IGNORED: running agent-box inside a cloned
	// hostile repo must not load that repo's config.
	writeCfg(t, filepath.Join(work, "config.json"), "127.0.0.1:3")
	c, src, _ = Resolve("", false, home, xdg)
	if c.BindAddr != "127.0.0.1:2" || src.Path == "config.json" {
		t.Errorf("cwd config must be ignored: %+v %+v", c, src)
	}
	os.RemoveAll(filepath.Join(home, ".config"))
	os.RemoveAll(xdg)
	c, src, _ = Resolve("", false, home, "")
	if !src.Default || c.BindAddr != "127.0.0.1:8899" {
		t.Errorf("only a cwd config present: want built-in defaults, got %+v %+v", c, src)
	}
}

func TestResolve_ExplicitMissingIsFatal(t *testing.T) {
	if _, _, err := Resolve(filepath.Join(t.TempDir(), "nope.json"), true, t.TempDir(), ""); err == nil {
		t.Fatal("an explicit -config that does not exist must be an error")
	}
}
