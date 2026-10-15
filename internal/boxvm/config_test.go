package boxvm

import (
	"fmt"
	"strings"
	"testing"
)

func testCfg() Config {
	return Config{Root: "/Users/a/.local/share/agent-box", Home: "/Users/a",
		ColimaBin: "/opt/homebrew/bin/colima", DockerBin: "/opt/homebrew/bin/docker", CPUs: 4, MemoryGiB: 4}
}

func TestPathsLiveUnderTheDataRoot(t *testing.T) {
	c := testCfg()
	for _, p := range []string{c.ColimaHome(), c.DockerConfigDir(), c.ProfileDir(), c.YAMLPath(), c.StampPath(), c.DockerSocket()} {
		if !strings.HasPrefix(p, c.Root+"/") {
			t.Fatalf("%q escapes the data root", p)
		}
	}
	if c.DockerSocket() != c.Root+"/vm/agent-box/docker.sock" {
		t.Fatalf("socket = %q", c.DockerSocket())
	}
}

func TestDockerArgsPinTheVMAndConfig(t *testing.T) {
	c := testCfg()
	got := strings.Join(c.DockerArgs(), " ")
	if got != "--host unix://"+c.DockerSocket()+" --config "+c.DockerConfigDir() {
		t.Fatalf("DockerArgs = %q", got)
	}
}

func TestMountsAreHomeAndAnOutsideRoot(t *testing.T) {
	c := testCfg()
	if m := c.Mounts(); len(m) != 1 || m[0] != "/Users/a" {
		t.Fatalf("root inside home must not add a mount: %v", m)
	}
	c.Root = "/Volumes/fast/ab"
	if m := c.Mounts(); len(m) != 2 || m[1] != "/Volumes/fast/ab" {
		t.Fatalf("root outside home must be shared too: %v", m)
	}
}

func TestYAMLSettingsAndProvision(t *testing.T) {
	y, err := testCfg().YAML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(y)
	for _, want := range []string{
		"vmType: vz\n", "mountType: virtiofs\n", "autoActivate: false\n", "sshConfig: false\n",
		"forwardAgent: false\n", "rosetta: false\n", "cpu: 4\n", "memory: 4\n", "runtime: docker\n",
		"  - location: \"/Users/a\"\n    writable: true\n",
		"  - mode: system\n", "DOCKER-USER", "host.lima.internal", "-j DROP",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("yaml missing %q:\n%s", want, s)
		}
	}
}

func TestYAMLRefusesUnsafeMountPaths(t *testing.T) {
	for _, bad := range []string{`/Users/a"b`, `/Users/a\b`, "/Users/a\nb"} {
		c := testCfg()
		c.Home = bad
		if _, err := c.YAML(); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestEnvPinsColimaAndDropsUserDockerEnv(t *testing.T) {
	c := testCfg()
	env := c.Env([]string{"HOME=/Users/a", "DOCKER_HOST=tcp://x", "DOCKER_CONTEXT=y", "COLIMA_HOME=/z",
		"LIMA_HOME=/l", "DOCKER_CONFIG=/q", "PATH=/usr/bin"})
	s := strings.Join(env, "\n")
	count := map[string]int{}
	for _, kv := range env {
		count[kv[:strings.Index(kv, "=")]]++
	}
	for _, gone := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "LIMA_HOME"} {
		if count[gone] != 0 {
			t.Fatalf("env must not keep %s:\n%s", gone, s)
		}
	}
	for _, once := range []string{"COLIMA_HOME", "DOCKER_CONFIG", "PATH", "HOME"} {
		if count[once] != 1 {
			t.Fatalf("env must carry %s exactly once (got %d):\n%s", once, count[once], s)
		}
	}
	for _, want := range []string{"HOME=/Users/a", "COLIMA_HOME=" + c.ColimaHome(), "DOCKER_CONFIG=" + c.DockerConfigDir(), "PATH=/opt/homebrew/bin:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("env missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "=/z") || strings.Contains(s, "=/q") || strings.Contains(s, "PATH=/usr/bin\n") {
		t.Fatalf("env kept a user value:\n%s", s)
	}
	path := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv
		}
	}
	if strings.Contains(path, "/opt/homebrew/bin:/opt/homebrew/bin") || strings.Count(path, "/opt/homebrew/bin") != 1 {
		t.Fatalf("PATH has duplicate directories: %s", path)
	}
}

func TestStartArgsNeverTouchUserState(t *testing.T) {
	got := strings.Join(testCfg().StartArgs(), " ")
	if got != "start --profile agent-box --activate=false --ssh-config=false --save-config=false" {
		t.Fatalf("StartArgs = %q", got)
	}
}

func TestSocketPathLimit(t *testing.T) {
	if err := testCfg().CheckSocketPaths(); err != nil {
		t.Fatalf("a normal root must fit: %v", err)
	}
	c := testCfg()
	c.Root = "/Users/a/" + strings.Repeat("x", 80)
	if err := c.CheckSocketPaths(); err == nil || !strings.Contains(err.Error(), "AGENT_BOX_HOME") {
		t.Fatalf("an over-long root must be refused with a remedy: %v", err)
	}
}

func TestValidateRefusesPathsDockerOrYAMLCannotCarry(t *testing.T) {
	for _, bad := range []string{"/Users/a,b", `/Users/a"b`, `/Users/a\b`, "/Users/a\nb", "/Users/a\x7fb"} {
		for _, field := range []string{"root", "home"} {
			c := testCfg()
			if field == "root" {
				c.Root = bad + "/agent-box"
			} else {
				c.Home = bad
			}
			err := c.Validate()
			if err == nil {
				t.Fatalf("%s %q must be refused", field, bad)
			}
			offender := c.Home
			if field == "root" {
				offender = c.Root
			}
			if !strings.Contains(err.Error(), "AGENT_BOX_HOME") || !strings.Contains(err.Error(), fmt.Sprintf("%q", offender)) {
				t.Fatalf("%s %q: error must name the path and the remedy: %v", field, bad, err)
			}
			if _, yerr := c.YAML(); yerr == nil {
				t.Fatalf("YAML must refuse %s %q too", field, bad)
			}
		}
	}
}

func TestValidateAcceptsOrdinaryConfigAndRefusesIncomplete(t *testing.T) {
	if err := testCfg().Validate(); err != nil {
		t.Fatalf("ordinary config refused: %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"relative root": func(c *Config) { c.Root = "rel/ab" },
		"relative home": func(c *Config) { c.Home = "Users/a" },
		"zero cpus":     func(c *Config) { c.CPUs = 0 },
		"zero memory":   func(c *Config) { c.MemoryGiB = 0 },
	} {
		c := testCfg()
		mut(&c)
		if c.Validate() == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
