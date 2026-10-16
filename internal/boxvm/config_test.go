package boxvm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/config"
)

func testCfg() Config {
	return Config{Root: "/Users/a/.local/share/agent-box", Home: "/Users/a",
		ColimaBin: "/opt/homebrew/bin/colima", DockerBin: "/opt/homebrew/bin/docker", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071}
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
	want := Config{Root: "/data", Home: "/home/a", ColimaBin: "/c", DockerBin: "/d", CPUs: 2, MemoryGiB: 8, BrokerPort: 9071}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if d := ConfigFor(config.Config{}, "/data", "/home/a", "/c", "/d"); d.CPUs != 4 || d.MemoryGiB != 4 || d.BrokerPort != 9071 {
		t.Fatalf("defaults: %+v", d)
	}
	if c := ConfigFor(config.Config{BrokerPort: 9100}, "/data", "/home/a", "/c", "/d"); c.BrokerPort != 9100 {
		t.Fatalf("broker_port from config: %+v", c)
	}
}

// fakeIptables keeps the DOCKER-USER chain in a file, one rule per line, so the
// provision script runs for real and its rule ORDER can be checked. It knows
// only what the script uses: -N, -C, -D and -I at position 1, always with -w
// (wait for the xtables lock docker may hold while it starts).
const fakeIptables = `#!/bin/sh
chain="$CHAIN"
touch "$chain"
[ "$1" = -w ] || exit 6
shift
op=$1; shift
[ "$1" = DOCKER-USER ] || exit 3
shift
case "$op" in
-N) exit 0 ;;
-C) grep -qxF -- "$*" "$chain" ;;
-D) grep -qxF -- "$*" "$chain" || exit 1
	awk -v r="$*" '!d && $0 == r { d = 1; next } { print }' "$chain" > "$chain.t" && mv "$chain.t" "$chain" ;;
-I) [ "$1" = 1 ] || exit 4; shift
	{ printf '%s\n' "$*"; cat "$chain"; } > "$chain.t" && mv "$chain.t" "$chain" ;;
*) exit 5 ;;
esac
`

// runProvision runs provisionScript against fakeIptables and a getent that
// resolves host.lima.internal to gw, starting from the chain in seed, and
// returns the chain afterwards.
func runProvision(t *testing.T, gw string, seed []string, runs int) []string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	chain := filepath.Join(dir, "chain")
	if len(seed) > 0 {
		if err := os.WriteFile(chain, []byte(strings.Join(seed, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	getent := "#!/bin/sh\n[ \"$2\" = host.lima.internal ] && echo '" + gw + "       STREAM host.lima.internal'\n"
	for name, body := range map[string]string{"iptables": fakeIptables, "getent": getent, "provision.sh": provisionScript(9071)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < runs; i++ {
		cmd := exec.Command(sh, filepath.Join(dir, "provision.sh"))
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "CHAIN=" + chain}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("provision run %d: %v\n%s", i+1, err, out)
		}
	}
	b, err := os.ReadFile(chain)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Measured on the founder's Mac (2026-10-16): docker gives boxes the lima
// gateway as their nameserver, so a DROP of everything to it broke DNS in
// every box and in the image build. udp/53 passes; every other port to the
// gateway (the Mac's loopback), tcp/53 included, stays dropped, whatever state
// the chain was in.
func TestProvisionLetsDNSThroughBeforeTheGatewayDrop(t *testing.T) {
	gw := "192.168.5.9"
	// The broker's TCP port is re-inserted last, so it sits above DNS and the DROP.
	want := []string{
		"-d " + gw + " -p tcp --dport 9071 -j RETURN",
		"-d " + gw + " -p udp --dport 53 -j RETURN",
		"-d " + gw + " -j DROP",
	}
	for name, tc := range map[string]struct {
		seed []string
		runs int
	}{
		"fresh boot":              {nil, 1},
		"run twice":               {nil, 2},
		"VM provisioned before":   {[]string{"-d " + gw + " -j DROP", "-j RETURN"}, 1},
		"DNS rule below the DROP": {[]string{"-d " + gw + " -j DROP", "-d " + gw + " -p udp --dport 53 -j RETURN"}, 1},
	} {
		got := runProvision(t, gw, tc.seed, tc.runs)
		exp := append([]string(nil), want...)
		if len(tc.seed) > 0 && tc.seed[len(tc.seed)-1] == "-j RETURN" {
			exp = append(exp, "-j RETURN")
		}
		if strings.Join(got, "\n") != strings.Join(exp, "\n") {
			t.Errorf("%s: chain\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(exp, "\n"))
		}
	}
}

func TestProvisionLetsTheBrokerPortThroughBeforeTheGatewayDrop(t *testing.T) {
	gw := "192.168.5.2"
	broker := "-d " + gw + " -p tcp --dport 9071 -j RETURN"
	dns := "-d " + gw + " -p udp --dport 53 -j RETURN"
	drop := "-d " + gw + " -j DROP"
	for name, seed := range map[string][]string{
		"fresh boot":             nil,
		"provisioned before M3":  {dns, drop},
		"broker rule below DROP": {dns, drop, broker},
	} {
		got := runProvision(t, gw, seed, 2)
		want := []string{broker, dns, drop}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	if strings.Contains(provisionScript(9071), "tcp --dport 53") {
		t.Fatal("tcp/53 must stay dropped")
	}
}

func TestProvisionUsesTheConfiguredBrokerPort(t *testing.T) {
	gw := "192.168.5.2"
	got := runProvision(t, gw, nil, 1)
	if got[0] != "-d "+gw+" -p tcp --dport 9071 -j RETURN" {
		t.Fatalf("default port: %q", got)
	}
	y, err := Config{Root: "/Users/a/.local/share/agent-box", Home: "/Users/a", ColimaBin: "/c", DockerBin: "/d",
		CPUs: 4, MemoryGiB: 4, BrokerPort: 9100}.YAML()
	if err != nil || !strings.Contains(string(y), "--dport 9100 -j RETURN") || strings.Contains(string(y), "9071") {
		t.Fatalf("yaml must carry the configured port: %v\n%s", err, y)
	}
}

func TestValidateRefusesABrokerPortOutOfRange(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		c := testCfg()
		c.BrokerPort = p
		if c.Validate() == nil {
			t.Errorf("broker port %d accepted", p)
		}
	}
}
