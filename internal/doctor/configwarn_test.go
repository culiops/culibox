package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

func configWarnIn(t *testing.T, mk func(home, newRoot, legacy string)) Input {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	newRoot, legacy := config.DefaultRoots(home)
	if err := os.MkdirAll(filepath.Dir(newRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	mk(home, newRoot, legacy)
	cfg, src, err := config.Resolve("", false, home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return Input{Config: cfg, Source: src, Home: home}
}

func writeXDG(t *testing.T, home, name string) {
	t.Helper()
	p := filepath.Join(home, ".config", name, "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"a","bin":"a"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFileWarnLegacyRoot(t *testing.T) {
	in := configWarnIn(t, func(home, _, legacy string) {
		_ = os.MkdirAll(legacy, 0o700)
		writeXDG(t, home, "culibox")
	})
	got := checkConfigFileWarn(in)
	file := filepath.Join(in.Home, ".config", "culibox", "config.json")
	if len(got) != 1 || got[0].Status != Warn || got[0].Check != "config" || !strings.Contains(got[0].Detail, file) {
		t.Fatalf("%+v", got)
	}
	// The same row reaches the full list, after the config PASS row.
	var seen bool
	for _, r := range Checks(in) {
		if r.Status == Warn && r.Check == "config" && strings.Contains(r.Detail, file) {
			seen = true
		}
	}
	if !seen {
		t.Error("Checks does not carry the warning")
	}
}

func TestConfigFileWarnQuietCases(t *testing.T) {
	for name, mk := range map[string]func(home, newRoot, legacy string){
		"fresh culibox root": func(home, newRoot, _ string) {
			_ = os.MkdirAll(newRoot, 0o700)
			writeXDG(t, home, "culibox")
		},
		"agent-box file": func(home, _, legacy string) {
			_ = os.MkdirAll(legacy, 0o700)
			writeXDG(t, home, "agent-box")
		},
		"config in the data root": func(home, _, legacy string) {
			_ = os.MkdirAll(legacy, 0o700)
			_ = os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"bind_addr":"127.0.0.1:8899","agents":[{"name":"a","bin":"a"}]}`), 0o600)
			writeXDG(t, home, "culibox")
		},
		"no config": func(_, _, legacy string) { _ = os.MkdirAll(legacy, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			if got := checkConfigFileWarn(configWarnIn(t, mk)); len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		})
	}
}
