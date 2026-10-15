package boxvm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
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
	for _, bad := range []string{"/Users/a,b", `/Users/a"b`, `/Users/a\b`, "/Users/a\nb", "/Users/a\x7fb",
		"/Users/a\u0085b", "/Users/a\u0080b", "/Users/a\u009bb", "/Users/a\u2028b", "/Users/a\u2029b", "/Users/a\xffb"} {
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
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", offender)) {
				t.Fatalf("%s %q: error must name the path: %v", field, bad, err)
			}
			// AGENT_BOX_HOME moves the data root, not the home.
			if has := strings.Contains(err.Error(), "AGENT_BOX_HOME"); has != (field == "root") {
				t.Fatalf("%s %q: AGENT_BOX_HOME remedy present=%v, want %v: %v", field, bad, has, field == "root", err)
			}
			if field == "home" && !strings.Contains(err.Error(), "host tier") {
				t.Fatalf("home %q: error must offer renaming the home or the host tier: %v", bad, err)
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
		"empty colima":  func(c *Config) { c.ColimaBin = "" },
		"rel colima":    func(c *Config) { c.ColimaBin = "colima" },
		"empty docker":  func(c *Config) { c.DockerBin = "" },
		"rel docker":    func(c *Config) { c.DockerBin = "bin/docker" },
	} {
		c := testCfg()
		mut(&c)
		if c.Validate() == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestConfigForTakesSizeFromTheInstanceConfig(t *testing.T) {
	got := ConfigFor(config.Config{BoxVMCPUs: 2, BoxVMMemoryGiB: 8}, "/data", "/home/a", "/c", "/d")
	want := Config{Root: "/data", Home: "/home/a", ColimaBin: "/c", DockerBin: "/d", CPUs: 2, MemoryGiB: 8}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if d := ConfigFor(config.Config{}, "/data", "/home/a", "/c", "/d"); d.CPUs != 4 || d.MemoryGiB != 4 {
		t.Fatalf("defaults: %+v", d)
	}
}
