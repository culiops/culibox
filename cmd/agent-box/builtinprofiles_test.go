package main

import (
	"reflect"
	"testing"

	"github.com/culiops/culibox/internal/config"
)

// #15: which profiles the first-run welcome counts as built in. A config.json
// agent is the user's own, so only what CuliBox adds itself is built in.
func TestBuiltinProfileNames(t *testing.T) {
	agent := func(name, bin string) config.AgentConfig { return config.AgentConfig{Name: name, Bin: bin} }
	claude, zsh, ops := agent("claude", "claude"), agent("zsh", "/bin/zsh"), agent("ops", "claude")

	cases := []struct {
		name       string
		src        config.Source
		configured []config.AgentConfig
		effective  []config.AgentConfig
		want       []string
	}{
		{"zero-config default: every effective agent", config.Source{Default: true},
			[]config.AgentConfig{claude}, []config.AgentConfig{claude, zsh}, []string{"claude", "zsh"}},
		{"file config: only the detected-shell seed", config.Source{Path: "/h/config.json"},
			[]config.AgentConfig{claude}, []config.AgentConfig{claude, zsh}, []string{"zsh"}},
		{"file config whose agent already runs the shell: nothing", config.Source{Path: "/h/config.json"},
			[]config.AgentConfig{claude, zsh}, []config.AgentConfig{claude, zsh}, nil},
		{"file config, shell names taken: nothing", config.Source{Path: "/h/config.json"},
			[]config.AgentConfig{claude, ops}, []config.AgentConfig{claude, ops}, nil},
	}
	for _, c := range cases {
		got := builtinProfileNames(c.src, c.configured, c.effective)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
