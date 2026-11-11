package config

import (
	"slices"
	"strings"
	"testing"
)

func validCfg() Config {
	return Config{BindAddr: "127.0.0.1:8899", Agents: []AgentConfig{{Name: "claude", Bin: "claude"}}}
}

func TestHostAllowlist_Loopback(t *testing.T) {
	got := validCfg().HostAllowlist()
	for _, want := range []string{"127.0.0.1:8899", "localhost:8899", "[::1]:8899"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestHostAllowlist_NonLoopbackAndExtras(t *testing.T) {
	c := validCfg()
	c.BindAddr = "192.0.2.10:9000"
	c.AllowedHosts = []string{"Agent-Box.Culiops.dev"}
	got := c.HostAllowlist()
	if !slices.Equal(got, []string{"192.0.2.10:9000", "agent-box.culiops.dev"}) {
		t.Errorf("got %q (no loopback aliases for a non-loopback bind; extras lowercased)", got)
	}
}

func TestValidate_AllowedHosts(t *testing.T) {
	for _, bad := range []string{"", "https://x.dev", "x.dev/path", "*.x.dev", "a b", "user@x.dev"} {
		c := validCfg()
		c.AllowedHosts = []string{bad}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
			t.Errorf("entry %q: want an allowed_hosts error, got %v", bad, err)
		}
	}
	c := validCfg()
	c.AllowedHosts = []string{"agent-box.culiops.dev", "box.lan:8443", "[::1]:9"}
	if err := c.Validate(); err != nil {
		t.Errorf("good entries refused: %v", err)
	}
}

// Review focus #4.
func TestValidate_RelativeSocketDir(t *testing.T) {
	c := validCfg()
	c.AbducoSocketDir = "sock"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "abduco_socket_dir") {
		t.Errorf("relative abduco_socket_dir: want error, got %v", err)
	}
	c.AbducoSocketDir = "/tmp/sock"
	if err := c.Validate(); err != nil {
		t.Errorf("absolute abduco_socket_dir refused: %v", err)
	}
}

func TestResolvedDataDir(t *testing.T) {
	if got := validCfg().ResolvedDataDir("/home/u"); got != "/home/u/.local/share/agent-box" {
		t.Errorf("got %q", got)
	}
	c := validCfg()
	c.DataDir = "/srv/ab"
	if got := c.ResolvedDataDir("/home/u"); got != "/srv/ab" {
		t.Errorf("got %q", got)
	}
}
