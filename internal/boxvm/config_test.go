package boxvm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

func testCfg() Config {
	return Config{Root: "/Users/a/.local/share/agent-box", Home: "/Users/a",
		ColimaBin: "/opt/homebrew/bin/colima", DockerBin: "/opt/homebrew/bin/docker", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}
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
	got := ConfigFor(config.Config{BoxVMCPUs: 2, BoxVMMemoryGiB: 8}, "/data", "/home/a", "/c", "/d", 10, 32<<30)
	want := Config{Root: "/data", Home: "/home/a", ColimaBin: "/c", DockerBin: "/d", CPUs: 2, MemoryGiB: 8, BrokerPort: 9071, EgressPort: 9072}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if d := ConfigFor(config.Config{}, "/data", "/home/a", "/c", "/d", 0, 0); d.CPUs != 4 || d.MemoryGiB != 4 || d.BrokerPort != 9071 || d.EgressPort != 9072 {
		t.Fatalf("defaults: %+v", d)
	}
	// Unset size follows the host facts it is given: all cores, half the RAM.
	if d := ConfigFor(config.Config{}, "/data", "/home/a", "/c", "/d", 10, 32<<30); d.CPUs != 10 || d.MemoryGiB != 16 {
		t.Fatalf("host-derived defaults: %+v", d)
	}
	if c := ConfigFor(config.Config{BrokerPort: 9100}, "/data", "/home/a", "/c", "/d", 0, 0); c.BrokerPort != 9100 {
		t.Fatalf("broker_port from config: %+v", c)
	}
	if c := ConfigFor(config.Config{EgressPort: 9200}, "/data", "/home/a", "/c", "/d", 0, 0); c.EgressPort != 9200 {
		t.Fatalf("egress_port from config: %+v", c)
	}
}

// fakeIptables keeps the DOCKER-USER chain in a file ($CHAIN) and the INPUT
// chain in another ($CHAIN.input), one rule per line, so the provision script
// runs for real and its rule ORDER can be checked. It knows only what the
// script uses: -N, -C, -D and -I at position 1, always with -w (wait for the
// xtables lock docker may hold while it starts). Rules are stored as the text
// of their arguments, so -s/-d/-p/--dport/-j all travel through.
const fakeIptables = `#!/bin/sh
chain="$CHAIN"
touch "$chain"
[ "$1" = -w ] || exit 6
shift
op=$1; shift
case "$1" in
DOCKER-USER) f="$chain" ;;
INPUT) f="$chain.input" ;;
*) exit 3 ;;
esac
shift
touch "$f"
case "$op" in
-N) exit 0 ;;
-C) grep -qxF -- "$*" "$f" ;;
-D) grep -qxF -- "$*" "$f" || exit 1
	awk -v r="$*" '!d && $0 == r { d = 1; next } { print }' "$f" > "$f.t" && mv "$f.t" "$f" ;;
-I) [ "$1" = 1 ] || exit 4; shift
	{ printf '%s\n' "$*"; cat "$f"; } > "$f.t" && mv "$f.t" "$f" ;;
*) exit 5 ;;
esac
`

// runProvisionBoth runs provisionScript against fakeIptables and a getent that
// resolves host.lima.internal to gw, starting from the DOCKER-USER chain in
// seed and the INPUT chain in inputSeed, and returns both chains afterwards.
func runProvisionBoth(t *testing.T, gw string, seed, inputSeed []string, runs int) (dockerUser, input []string) {
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
	if len(inputSeed) > 0 {
		if err := os.WriteFile(chain+".input", []byte(strings.Join(inputSeed, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"iptables": fakeIptables, "getent": getent, "provision.sh": provisionScript(9071, 9072)} {
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
	read := func(p string) []string {
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}
	return read(chain), read(chain + ".input")
}

// runProvisionFull is the whole DOCKER-USER chain.
func runProvisionFull(t *testing.T, gw string, seed []string, runs int) []string {
	t.Helper()
	c, _ := runProvisionBoth(t, gw, seed, nil, runs)
	return c
}

// runProvision is the DOCKER-USER chain as the M2 gateway rules left it: the
// egress subnet's one forward DROP (-s EgressSubnet) is left out, so the M2
// order tests keep describing the gateway/DNS/broker rules and nothing else.
// TestProvisionKeepsAForwardDropForTheEgressSubnet covers that rule.
func runProvision(t *testing.T, gw string, seed []string, runs int) []string {
	t.Helper()
	var out []string
	for _, r := range runProvisionFull(t, gw, seed, runs) {
		if !strings.Contains(r, "-s "+EgressSubnet) {
			out = append(out, r)
		}
	}
	return out
}

// Measured on the founder's Mac (2026-10-16): docker gives boxes the lima
// gateway as their nameserver, so a DROP of everything to it broke DNS in
// every box and in the image build. udp/53 passes; every other port to the
// gateway (the Mac's loopback), tcp/53 included, stays dropped, whatever state
// the chain was in.
func TestProvisionLetsDNSThroughBeforeTheGatewayDrop(t *testing.T) {
	gw := "192.168.5.9"
	// No TCP port passes: the broker is reached through the relay only (#113).
	want := []string{
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

// VMs provisioned before #113 let the broker's TCP port through the gateway
// DROP for every container on the VM's default network, which reached the
// broker directly instead of through the relay and could write its own PROXY
// line. The relay does not need the rule (its dial is the VM's own OUTPUT, never
// DOCKER-USER), so the script only deletes it now, wherever it sits and however
// often it was inserted.
func TestProvisionDeletesTheOldBrokerReturn(t *testing.T) {
	gw := "192.168.5.2"
	broker := "-d " + gw + " -p tcp --dport 9071 -j RETURN"
	dns := "-d " + gw + " -p udp --dport 53 -j RETURN"
	drop := "-d " + gw + " -j DROP"
	for name, seed := range map[string][]string{
		"fresh boot":             nil,
		"provisioned before M3":  {dns, drop},
		"provisioned before 113": {broker, dns, drop},
		"broker rule below DROP": {dns, drop, broker},
		"broker rule twice":      {broker, broker, dns, drop},
	} {
		for _, runs := range []int{1, 2} {
			got := runProvision(t, gw, seed, runs)
			want := []string{dns, drop}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("%s, %d run(s):\n got %q\nwant %q", name, runs, got, want)
			}
			for _, port := range []int{9071, 9072, 53, 443} {
				if v := verdict(t, got, pkt{"172.17.0.5", gw, "tcp", port}); v != "DROP" {
					t.Errorf("%s: a default-bridge container reaches the gateway on tcp/%d: %s", name, port, v)
				}
			}
		}
	}
	if strings.Contains(provisionScript(9071, 9072), "tcp --dport 53") {
		t.Fatal("tcp/53 must stay dropped")
	}
}

// The delete carries the configured broker port, and nothing inserts a TCP
// pass-through any more.
func TestProvisionDeletesTheBrokerReturnForTheConfiguredPort(t *testing.T) {
	y, err := Config{Root: "/Users/a/.local/share/agent-box", Home: "/Users/a", ColimaBin: "/c", DockerBin: "/d",
		CPUs: 4, MemoryGiB: 4, BrokerPort: 9100, EgressPort: 9072}.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(y), `iptables -w -D DOCKER-USER -d "$gw" -p tcp --dport 9100 -j RETURN`) || strings.Contains(string(y), "9071") {
		t.Fatalf("yaml must delete the rule for the configured port:\n%s", y)
	}
	for _, line := range strings.Split(string(y), "\n") {
		if strings.Contains(line, "-I DOCKER-USER") && strings.Contains(line, "tcp") {
			t.Fatalf("a TCP pass-through is still inserted: %q", line)
		}
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

// pkt is one packet as the DOCKER-USER chain sees it.
type pkt struct {
	src, dst, proto string
	dport           int
}

// ruleMatches reports whether one stored rule ("-s a -d b -p tcp --dport 53 -j
// DROP") matches p. -s/-d take an IP or a CIDR; a field the rule does not name
// matches anything.
func ruleMatches(t *testing.T, rule string, p pkt) (matches bool, target string) {
	t.Helper()
	f := strings.Fields(rule)
	in := func(spec, addr string) bool {
		if strings.Contains(spec, "/") {
			_, n, err := net.ParseCIDR(spec)
			if err != nil {
				t.Fatalf("rule %q: bad cidr %q", rule, spec)
			}
			return n.Contains(net.ParseIP(addr))
		}
		return net.ParseIP(spec).Equal(net.ParseIP(addr))
	}
	matches = true
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "-s", "-d", "-p", "--dport", "-j":
			if i+1 >= len(f) {
				t.Fatalf("rule %q: %s has no value", rule, f[i])
			}
		default:
			t.Fatalf("rule %q: the evaluator does not know %q", rule, f[i])
		}
		v := f[i+1]
		switch f[i] {
		case "-s":
			matches = matches && in(v, p.src)
		case "-d":
			matches = matches && in(v, p.dst)
		case "-p":
			matches = matches && v == p.proto
		case "--dport":
			matches = matches && v == fmt.Sprint(p.dport)
		case "-j":
			target = v
		}
		i++
	}
	return matches, target
}

// verdict evaluates a chain the way netfilter does: the first matching rule
// decides. No match is ACCEPT here: docker's own rules, which come after
// DOCKER-USER, then allow the packet.
func verdict(t *testing.T, chain []string, p pkt) string {
	t.Helper()
	for _, r := range chain {
		if m, target := ruleMatches(t, r, p); m {
			return target
		}
	}
	return "ACCEPT"
}

// inputCases are the packets the INPUT chain must judge: the VM's own listeners
// (the relay on the egress network's gateway, sshd, the bridge gateway) as seen
// from a bound box on the internal egress network and from an unbound box. No
// match is ACCEPT, the VM's INPUT policy. Traffic a bound box sends to the lima
// gateway is forwarded, never INPUT; the --internal network closes it (spike
// row c6), so it has no row here.
func inputCases() []struct {
	p    pkt
	want string
} {
	eg, egw, unbound := "172.29.72.5", EgressGateway, "172.17.0.5"
	return []struct {
		p    pkt
		want string
	}{
		{pkt{eg, egw, "tcp", 9072}, "ACCEPT"},
		{pkt{eg, egw, "tcp", 9071}, "ACCEPT"},
		{pkt{eg, egw, "tcp", 22}, "DROP"},
		{pkt{eg, egw, "tcp", 18080}, "DROP"},
		{pkt{eg, egw, "udp", 53}, "DROP"},
		{pkt{eg, "172.17.0.1", "tcp", 22}, "DROP"},
		{pkt{unbound, egw, "tcp", 9072}, "DROP"},
		{pkt{unbound, egw, "tcp", 9071}, "DROP"},
		// Unchanged: unbound boxes reach VM listeners on the default bridge (open
		// item in the spec).
		{pkt{unbound, "172.17.0.1", "tcp", 22}, "ACCEPT"},
	}
}

// wantInput is the INPUT chain the script must leave, top down.
func wantInput(egressPort, brokerPort int) []string {
	return []string{
		fmt.Sprintf("-s %s -d %s -p tcp --dport %d -j ACCEPT", EgressSubnet, EgressGateway, egressPort),
		fmt.Sprintf("-s %s -d %s -p tcp --dport %d -j ACCEPT", EgressSubnet, EgressGateway, brokerPort),
		"-s " + EgressSubnet + " -j DROP",
		"-d " + EgressGateway + " -j DROP",
	}
}

func checkInput(t *testing.T, label string, chain []string) {
	t.Helper()
	for _, c := range inputCases() {
		if got := verdict(t, chain, c.p); got != c.want {
			t.Errorf("%s %+v: %s, want %s\nchain:\n%s", label, c.p, got, c.want, strings.Join(chain, "\n"))
		}
	}
	want := wantInput(9072, 9071)
	if strings.Join(chain, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: INPUT =\n%s\nwant exactly, in order:\n%s", label, strings.Join(chain, "\n"), strings.Join(want, "\n"))
	}
}

// Boxes on the internal egress network reach the relay on the network's gateway
// (broker and egress ports) and nothing else on the VM; no other box reaches
// the relay. One run or two, the chain is the same four rules.
func TestProvisionInputOrder(t *testing.T) {
	for _, runs := range []int{1, 2} {
		_, input := runProvisionBoth(t, "192.168.5.2", nil, nil, runs)
		checkInput(t, fmt.Sprintf("runs=%d", runs), input)
	}
}

// The evaluator is not a rubber stamp: a chain with the DROPs above the ACCEPTs
// (what an insert that did not re-order would leave) fails the bound box's
// egress port.
func TestProvisionInputEvaluatorSeesAWrongOrder(t *testing.T) {
	want := wantInput(9072, 9071)
	bad := []string{want[2], want[3], want[0], want[1]}
	if got := verdict(t, bad, pkt{"172.29.72.5", EgressGateway, "tcp", 9072}); got != "DROP" {
		t.Fatalf("a DROP above the ACCEPTs must drop the egress port (that is the bug): %s", got)
	}
}

// A VM provisioned by option (a) has the subnet DROP at the top of INPUT and no
// ACCEPTs above it; one boot repairs it to the four-rule order, no duplicates.
func TestProvisionInputRepairsAnOptionASeed(t *testing.T) {
	want := wantInput(9072, 9071)
	seed := []string{"-s " + EgressSubnet + " -j DROP", want[0], want[1]}
	_, input := runProvisionBoth(t, "192.168.5.2", nil, seed, 1)
	checkInput(t, "repaired", input)
}

// The one DOCKER-USER rule for the subnet is a forward-path DROP: a backstop
// that does not depend on the network having been created --internal. It drops
// forwarded traffic only; the relay path is INPUT, and the relay's own traffic
// to the lima gateway is the VM's OUTPUT, so neither is touched.
func TestProvisionKeepsAForwardDropForTheEgressSubnet(t *testing.T) {
	gw := "192.168.5.2"
	chain := runProvisionFull(t, gw, nil, 2)
	n := 0
	for _, r := range chain {
		if strings.Contains(r, EgressSubnet) {
			if r != "-s "+EgressSubnet+" -j DROP" {
				t.Errorf("unexpected egress-subnet rule in DOCKER-USER: %q", r)
			}
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one egress-subnet rule in DOCKER-USER, got %d:\n%s", n, strings.Join(chain, "\n"))
	}
	for _, c := range []struct {
		p    pkt
		want string
	}{
		{pkt{"172.29.72.5", "1.1.1.1", "tcp", 443}, "DROP"},
		{pkt{"172.29.72.5", gw, "tcp", 9072}, "DROP"},
		{pkt{"172.17.0.5", gw, "tcp", 9071}, "DROP"},
	} {
		if got := verdict(t, chain, c.p); got != c.want {
			t.Errorf("%+v: %s, want %s\nchain:\n%s", c.p, got, c.want, strings.Join(chain, "\n"))
		}
	}
}

func TestProvisionUsesTheConfiguredEgressPort(t *testing.T) {
	s := provisionScript(9071, 9300)
	if !strings.Contains(s, "--dport 9300 -j ACCEPT") || strings.Contains(s, "9072") {
		t.Fatalf("the egress port must come from the config:\n%s", s)
	}
	if !strings.Contains(s, "sub="+EgressSubnet+"\n") || !strings.Contains(s, "egw="+EgressGateway+"\n") {
		t.Fatal("the subnet and its gateway are the fixed EgressSubnet and EgressGateway")
	}
	if strings.Contains(s, "%!") {
		t.Fatalf("a format verb leaked into the script:\n%s", s)
	}
}

func TestValidateRefusesAnEgressPortOutOfRange(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		c := testCfg()
		c.EgressPort = p
		if c.Validate() == nil {
			t.Errorf("egress port %d accepted", p)
		}
	}
}

// The egress rules change the provision script, so every VM set up before M3c
// reads as drifted and doctor sends it to `box setup -recreate`.
func TestEgressRulesChangeTheYAMLHash(t *testing.T) {
	y, err := testCfg().YAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-s \"$sub\" -j DROP", EgressSubnet, "--dport 9072 -j ACCEPT", "iptables -w -I INPUT 1"} {
		if !strings.Contains(string(y), want) {
			t.Errorf("yaml missing %q:\n%s", want, y)
		}
	}
}

// scriptFromYAML is the provision script as lima runs it: the YAML block scalar
// with its six-space indent stripped.
func scriptFromYAML(t *testing.T, y []byte) string {
	t.Helper()
	const head = "    script: |\n"
	i := strings.Index(string(y), head)
	if i < 0 {
		t.Fatalf("no provision script in the yaml:\n%s", y)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(y)[i+len(head):], "\n"), "\n") {
		if !strings.HasPrefix(l, "      ") {
			t.Fatalf("script line %q is not indented under the block scalar", l)
		}
		out = append(out, strings.TrimPrefix(l, "      "))
	}
	return strings.Join(out, "\n") + "\n"
}

const testCAPEM = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUQ0FfVEVTVF9ST09UAAAAAAAAAAAwCgYIKoZIzj0EAwIw\nEjEQMA4GA1UEAwwHdGVzdC1jYTAeFw0yNjEwMjIwMDAwMDBaFw0zNjEwMjIwMDAw\n-----END CERTIFICATE-----\n"

// Off means off: with no CA the YAML bytes, and so the drift stamp, are exactly
// what they were before extra_ca_certs existed. testdata/colima-no-ca.yaml was
// captured from the code at 41f3081, before any change.
func TestYAMLWithoutCAUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/colima-no-ca.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := testCfg().YAML()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("YAML with no CA changed:\n%s\nwant\n%s", got, want)
	}
	if h := yamlHash(got); h != "d01514795989199211b2e3389c79ce910a53942f372974c683248a44b9884e2f" {
		t.Fatalf("hash %s is not the pre-feature hash", h)
	}
	c := testCfg()
	c.ExtraCAPEM = []byte{}
	if g, _ := c.YAML(); string(g) != string(want) {
		t.Fatal("an empty, non-nil ExtraCAPEM must read as no CA")
	}
}

func TestYAMLWithCAInstallsIt(t *testing.T) {
	plain, _ := testCfg().YAML()
	c := testCfg()
	c.ExtraCAPEM = []byte(testCAPEM)
	y, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	s := scriptFromYAML(t, y)
	for _, want := range []string{
		testCAPEM, "<<'AGENT_BOX_CA'\n", "\nAGENT_BOX_CA\n", "update-ca-certificates",
		"/usr/local/share/ca-certificates/agent-box-extra.crt", "cmp -s",
		"systemctl is-active --quiet docker && {", "systemctl restart docker || echo \"agent-box: docker restart failed\" >&2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
	if yamlHash(y) == yamlHash(plain) {
		t.Error("adding a CA must change the YAML hash (drift)")
	}
	if !strings.HasPrefix(string(y), string(plain[:strings.Index(string(plain), "provision:")])) {
		t.Error("only the provision script may differ")
	}
}

// The block is appended after the sprintf-formatted script, never formatted.
func TestCABlockNotSprintfFormatted(t *testing.T) {
	const pemWithVerbs = "-----BEGIN CERTIFICATE-----\n%d %s %!x(MISSING)\n-----END CERTIFICATE-----\n"
	if got := provisionScript(1, 2) + caBlock([]byte(pemWithVerbs)); !strings.Contains(got, pemWithVerbs) {
		t.Fatalf("PEM changed by formatting:\n%s", got)
	}
	c := testCfg()
	c.ExtraCAPEM = []byte(pemWithVerbs)
	y, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if s := scriptFromYAML(t, y); !strings.Contains(s, pemWithVerbs) {
		t.Fatalf("PEM changed on the way into the yaml:\n%s", s)
	}
}

func TestValidateRefusesAPEMThatCouldEndTheHeredoc(t *testing.T) {
	c := testCfg()
	c.ExtraCAPEM = []byte("-----BEGIN CERTIFICATE-----\nAAAA\nAGENT_BOX_CA\ntouch /pwned\n-----END CERTIFICATE-----\n")
	if _, err := c.YAML(); err == nil {
		t.Fatal("a PEM carrying the heredoc terminator must be refused")
	}
}

// Runs the rendered script for real, with the CA directory redirected into a
// scratch dir and fake iptables/getent/update-ca-certificates/systemctl.
func TestCABlockRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	c := testCfg()
	c.ExtraCAPEM = []byte(testCAPEM)
	y, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	script := scriptFromYAML(t, y)
	type env struct{ dir, ca string }
	setup := func(t *testing.T, active, restartFails bool) env {
		dir := t.TempDir()
		caDir := filepath.Join(dir, "certs")
		sc := strings.ReplaceAll(script, "/usr/local/share/ca-certificates", caDir)
		systemctl := "#!/bin/sh\necho \"systemctl $*\" >> \"$LOG\"\ncase \"$1\" in\nis-active) " + map[bool]string{true: "exit 0", false: "exit 3"}[active] + " ;;\nrestart) " + map[bool]string{true: "exit 1", false: "exit 0"}[restartFails] + " ;;\nesac\n"
		for name, body := range map[string]string{
			"iptables":               fakeIptables,
			"getent":                 "#!/bin/sh\necho '192.168.5.2 STREAM host.lima.internal'\n",
			"update-ca-certificates": "#!/bin/sh\necho update-ca-certificates >> \"$LOG\"\n",
			"systemctl":              systemctl,
			"provision.sh":           sc,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command(sh, "-n", filepath.Join(dir, "provision.sh")).CombinedOutput(); err != nil {
			t.Fatalf("sh -n: %v\n%s", err, out)
		}
		return env{dir, filepath.Join(caDir, "agent-box-extra.crt")}
	}
	run := func(t *testing.T, e env) (log, stderr string, err error) {
		cmd := exec.Command(sh, filepath.Join(e.dir, "provision.sh"))
		cmd.Env = []string{"PATH=" + e.dir + ":/usr/bin:/bin", "CHAIN=" + filepath.Join(e.dir, "chain"), "LOG=" + filepath.Join(e.dir, "log")}
		var eb strings.Builder
		cmd.Stderr = &eb
		err = cmd.Run()
		b, _ := os.ReadFile(filepath.Join(e.dir, "log"))
		os.Remove(filepath.Join(e.dir, "log"))
		return string(b), eb.String(), err
	}

	t.Run("first start installs, updates, restarts a running docker", func(t *testing.T) {
		e := setup(t, true, false)
		log, _, err := run(t, e)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(e.ca); string(b) != testCAPEM {
			t.Fatalf("installed file = %q", b)
		}
		if log != "update-ca-certificates\nsystemctl is-active --quiet docker\nsystemctl restart docker\n" {
			t.Fatalf("log:\n%s", log)
		}
		if _, err := os.Stat(e.ca + ".new"); err == nil {
			t.Fatal(".new left behind")
		}
		// Second start: nothing changed, nothing runs.
		log, _, err = run(t, e)
		if err != nil || log != "" {
			t.Fatalf("unchanged start: err=%v log=%q", err, log)
		}
		if _, err := os.Stat(e.ca + ".new"); err == nil {
			t.Fatal(".new left behind on an unchanged start")
		}
	})
	t.Run("docker not running: no restart, script still succeeds", func(t *testing.T) {
		e := setup(t, false, false)
		log, _, err := run(t, e)
		if err != nil {
			t.Fatalf("script failed: %v", err)
		}
		if strings.Contains(log, "restart") || !strings.Contains(log, "update-ca-certificates") {
			t.Fatalf("log:\n%s", log)
		}
	})
	t.Run("restart failing never fails the script", func(t *testing.T) {
		e := setup(t, true, true)
		_, stderr, err := run(t, e)
		if err != nil {
			t.Fatalf("script failed: %v", err)
		}
		if !strings.Contains(stderr, "agent-box: docker restart failed") {
			t.Fatalf("stderr = %q", stderr)
		}
	})
}

func TestConfigForFillsTheCAFromExtraCACerts(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(f, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFor(config.Config{ExtraCACerts: []string{f}}, "/data", "/home/a", "/c", "/d", 0, 0); !strings.HasPrefix(string(got.ExtraCAPEM), "-----BEGIN CERTIFICATE-----\n") {
		t.Fatalf("ExtraCAPEM = %q", got.ExtraCAPEM)
	}
	if got := ConfigFor(config.Config{}, "/data", "/home/a", "/c", "/d", 0, 0); got.ExtraCAPEM != nil {
		t.Fatalf("no extra_ca_certs must leave ExtraCAPEM nil, got %q", got.ExtraCAPEM)
	}
}

func TestHostSizeReadsTheMachine(t *testing.T) {
	cpus, mem := HostSize()
	if cpus < 1 {
		t.Errorf("HostSize cpus = %d", cpus)
	}
	if runtime.GOOS == "darwin" && mem == 0 {
		t.Error("darwin must read hw.memsize")
	}
}

// provisioned makes a VM set up at 4 CPUs / 4 GiB under a fresh root, as an
// older agent-box would have left it, and returns the root.
func provisioned(t *testing.T) string {
	t.Helper()
	root := shortRoot(t)
	v := NewVM(Config{Root: root, Home: root, ColimaBin: "/c", DockerBin: "/d", CPUs: 4, MemoryGiB: 4, BrokerPort: 9071, EgressPort: 9072}, &fakeRunner{})
	if err := v.Provision(context.Background(), false, io.Discard); err != nil {
		t.Fatal(err)
	}
	return root
}

// An existing VM keeps the size it was created with: host-derived defaults
// apply at first setup or after -recreate, never to a VM that exists.
func TestConfigForKeepsAnExistingVMsSize(t *testing.T) {
	root := provisioned(t)
	got := ConfigFor(config.Config{}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 4 || got.MemoryGiB != 4 {
		t.Fatalf("an existing 4/4 VM with the size unset must stay 4/4, got %d/%d", got.CPUs, got.MemoryGiB)
	}
	if v := NewVM(got, &fakeRunner{}); v.Drifted() {
		t.Fatal("an upgraded 4/4 VM with the size unset must not read as drifted")
	}
	// each unset field is kept on its own; an explicit one wins
	got = ConfigFor(config.Config{BoxVMCPUs: 6}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 6 || got.MemoryGiB != 4 {
		t.Fatalf("explicit cpus + unset memory: got %d/%d, want 6/4", got.CPUs, got.MemoryGiB)
	}
	got = ConfigFor(config.Config{BoxVMMemoryGiB: 8}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 4 || got.MemoryGiB != 8 {
		t.Fatalf("unset cpus + explicit memory: got %d/%d, want 4/8", got.CPUs, got.MemoryGiB)
	}
}

func TestConfigForExplicitNewSizeDriftsAnExistingVM(t *testing.T) {
	root := provisioned(t)
	got := ConfigFor(config.Config{BoxVMCPUs: 8, BoxVMMemoryGiB: 16}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 8 || got.MemoryGiB != 16 {
		t.Fatalf("explicit values must win: %d/%d", got.CPUs, got.MemoryGiB)
	}
	if v := NewVM(got, &fakeRunner{}); !v.Drifted() {
		t.Fatal("an explicit new size on an existing VM must read as drift")
	}
}

func TestConfigForFirstSetupUsesHostSize(t *testing.T) {
	root := shortRoot(t) // no stamp: never provisioned
	got := ConfigFor(config.Config{}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 10 || got.MemoryGiB != 16 {
		t.Fatalf("first setup must use the host-derived size, got %d/%d", got.CPUs, got.MemoryGiB)
	}
	// a stale colima.yaml without a stamp (an interrupted first setup, or after
	// -recreate removed the stamp) is not a VM that was set up: host size again
	if err := os.MkdirAll(filepath.Join(root, "vm", Profile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vm", Profile, "colima.yaml"), []byte("cpu: 4\nmemory: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = ConfigFor(config.Config{}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 10 || got.MemoryGiB != 16 {
		t.Fatalf("a yaml with no stamp must not pin the size, got %d/%d", got.CPUs, got.MemoryGiB)
	}
}

func TestConfigForIgnoresAnUnreadableExistingYAML(t *testing.T) {
	root := provisioned(t)
	if err := os.WriteFile(filepath.Join(root, "vm", Profile, "colima.yaml"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ConfigFor(config.Config{}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 10 || got.MemoryGiB != 16 {
		t.Fatalf("an unparseable yaml falls back to the host-derived size, got %d/%d", got.CPUs, got.MemoryGiB)
	}
}

// -recreate deletes the VM, so it is the moment host-derived sizing applies
// again: ConfigForFresh ignores the VM that is about to be replaced.
func TestConfigForFreshIgnoresAnExistingVM(t *testing.T) {
	root := provisioned(t)
	got := ConfigForFresh(config.Config{}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 10 || got.MemoryGiB != 16 {
		t.Fatalf("recreate must size from the host, got %d/%d", got.CPUs, got.MemoryGiB)
	}
	got = ConfigForFresh(config.Config{BoxVMCPUs: 3}, root, root, "/c", "/d", 10, 32<<30)
	if got.CPUs != 3 || got.MemoryGiB != 16 {
		t.Fatalf("explicit values still win on recreate, got %d/%d", got.CPUs, got.MemoryGiB)
	}
}
