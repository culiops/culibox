package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/egress"
	"github.com/culiops/culibox/internal/session"
)

const egressNonce = "0123456789abcdef0123456789abcdef"

func noSessions(string) (*session.Session, bool) { return nil, false }

// A docker that is not there: network ls fails, so the network is "absent" and
// boot must not treat that as fatal.
func absentLayer() *boxLayer { return &boxLayer{cli: box.CLIClient{Bin: "/bin/false"}} }

func TestStartEgressPersistsSecret(t *testing.T) {
	dir := t.TempDir()
	a, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	tok := a.Svc.Proxy().Token("claude-3", egressNonce)
	a.Stop(context.Background())

	fi, err := os.Stat(filepath.Join(dir, "egress.secret"))
	if err != nil || fi.Size() != 32 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("egress.secret: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.db")); err != nil {
		t.Fatalf("audit.db: %v", err)
	}

	b, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop(context.Background())
	if b.Svc.Proxy().Token("claude-3", egressNonce) != tok {
		t.Fatal("tokens changed across a restart: a reattached box would lose its proxy access")
	}
	if _, ok := b.Stats("claude-3"); ok {
		t.Error("Stats reports a session that never opened a tunnel")
	}
}

func TestStartEgressSecretIsNotTheBrokerSecret(t *testing.T) {
	dir := shortRoot(t) // holds the broker socket
	if _, err := startBroker(dir, dir, "linux", 0, noSessions, func(string) (session.Info, bool) { return session.Info{}, false }, nil, nil); err != nil {
		t.Fatal(err)
	}
	e, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())
	b, _ := os.ReadFile(filepath.Join(dir, "broker.secret"))
	g, _ := os.ReadFile(filepath.Join(dir, "egress.secret"))
	if len(g) != 32 || string(b) == string(g) {
		t.Fatalf("egress secret must be its own 32 bytes (broker %d, egress %d, equal %v)", len(b), len(g), string(b) == string(g))
	}
}

func TestStartEgressRefusesABadSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "egress.secret"), 0o700); err != nil { // a directory: unreadable as a file
		t.Fatal(err)
	}
	if _, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions); err == nil || !strings.Contains(err.Error(), "egress secret") {
		t.Fatalf("err = %v, want an egress secret error", err)
	}
}

// statefulEgressDocker is a docker whose `network create` makes the network
// exist (as docker would: non-internal here, on the subnet it was given), so a
// first Ready can create it and a later one find it. `exec` prints the file
// "exec" in its directory.
func statefulEgressDocker(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
d=` + dir + `
echo "$*" >> "$d/log"
case "$1 $2" in
"network ls") [ -f "$d/net" ] && echo agentbox-egress-default ;;
"network create") shift 2
	while [ $# -gt 0 ]; do case "$1" in --subnet) echo "$2" > "$d/net"; shift ;; --internal) echo internal >> "$d/flags" ;; -o) echo "$2" >> "$d/opts"; shift ;; esac; shift; done
	: > "$d/created" ;;
"network inspect") icc=true; grep -q 'enable_icc=false' "$d/opts" 2>/dev/null && icc=false
	int=false; [ -f "$d/flags" ] && int=true
	printf '{"Internal":'$int',"Options":{"com.docker.network.bridge.enable_icc":"%s"},"IPAM":{"Config":[{"Subnet":"%s","Gateway":"172.29.72.1"}]}}' "$icc" "$(cat "$d/net")" ;;
exec*) cat "$d/exec" 2>/dev/null ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// darwin option (c): an internal network on the fixed subnet and gateway, the
// proxy on the Mac's 127.0.0.1, and boxes dialling the gateway (the VM relay
// carries them to it), with no --add-host.
func TestStartEgressDarwinUsesLoopbackAndFixedSubnet(t *testing.T) {
	bin, dir := statefulEgressDocker(t)
	port := freePort(t)
	svc, err := startEgress(t.TempDir(), &boxLayer{goos: "darwin", cli: box.CLIClient{Bin: bin}, relayKey: testRelayKey}, config.Config{EgressPort: port}, "darwin", noSessions)
	if err != nil {
		t.Fatalf("darwin must start egress now: %v", err)
	}
	defer svc.Stop(context.Background())
	if b, _ := os.ReadFile(filepath.Join(dir, "created")); b != nil {
		t.Error("boot created the network (the first bound spawn does)")
	}
	env, err := svc.Svc.SpawnEnv(context.Background(), "claude-3", egressNonce)
	if err != nil {
		t.Fatalf("first bound spawn: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if !strings.Contains(string(log), "--subnet "+boxvm.EgressSubnet) {
		t.Errorf("network create lacks --subnet %s:\n%s", boxvm.EgressSubnet, log)
	}
	if o, _ := os.ReadFile(filepath.Join(dir, "opts")); !strings.Contains(string(o), "com.docker.network.bridge.enable_icc=false") {
		t.Errorf("darwin's network must be created with inter-container traffic off (boxes must not reach each other): opts %q", o)
	}
	if !strings.Contains(string(log), "--internal") || !strings.Contains(string(log), "--gateway "+boxvm.EgressGateway) {
		t.Errorf("darwin's network must be --internal with --gateway %s:\n%s", boxvm.EgressGateway, log)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "@"+boxvm.EgressGateway+":"+strconv.Itoa(port)) {
		t.Errorf("HTTPS_PROXY must name the gateway %s:%d:\n%s", boxvm.EgressGateway, port, redactTokens(joined))
	}
	if strings.Contains(joined, "host.lima.internal") {
		t.Errorf("no box env may name host.lima.internal any more:\n%s", redactTokens(joined))
	}
	// The listener is on the Mac's loopback and nowhere wider.
	ep, err := svc.Svc.Ready(context.Background())
	if err != nil || ep.Port != port || ep.ProxyHost != boxvm.EgressGateway || ep.Net.Subnet.String() != boxvm.EgressSubnet {
		t.Fatalf("endpoint = %+v, %v", ep, err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("proxy not listening on 127.0.0.1:%d: %v", port, err)
	}
	c.Close()
	if ips, _ := net.InterfaceAddrs(); len(ips) > 0 {
		for _, a := range ips {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			if c, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)), time.Second); err == nil {
				c.Close()
				t.Errorf("proxy also answers on %s: it must bind 127.0.0.1 only", ip)
			}
		}
	}
}

// redactTokens keeps a failing assertion from printing the proxy credential.
func redactTokens(s string) string {
	return regexp.MustCompile(`://[^@\s]*@`).ReplaceAllString(s, "://[redacted]@")
}

func TestEgressServiceOptionsPerPlatform(t *testing.T) {
	cfg := config.Config{}
	lin := egressServiceOptions(cfg, "linux", nil, nil)
	if !lin.Want.Internal || !lin.Want.NoICC || lin.Want.Subnet != "" || lin.ProxyHost != "" {
		t.Errorf("linux options changed: %+v", lin)
	}
	dar := egressServiceOptions(cfg, "darwin", nil, nil)
	if lin.Want.Gateway != "" || lin.AfterNetwork != nil {
		t.Errorf("linux must have no pinned gateway and no network hook: %+v", lin)
	}
	if want := (egress.Want{Internal: true, NoICC: true, Subnet: boxvm.EgressSubnet, Gateway: boxvm.EgressGateway}); dar.Want != want {
		t.Errorf("darwin wants %+v, got %+v", want, dar.Want)
	}
	if dar.ProxyHost != "" || dar.NoProxy != lin.NoProxy || dar.GOOS != "darwin" {
		t.Errorf("darwin dials the gateway like Linux: proxy host / no_proxy: %+v", dar)
	}
	if dar.Port != cfg.EgressPortOrDefault() {
		t.Errorf("darwin port = %d", dar.Port)
	}
}

// The darwin boot asks docker about the network only while the VM runs: a
// stopped VM has no docker to ask, and the first bound spawn (after prepare
// has started the VM) brings the listener up.
func TestStartEgressDarwinBootLeavesAStoppedVMAlone(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	svc, err := startEgress(t.TempDir(), l, config.Config{}, "darwin", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if fc.called("start") {
		t.Fatal("egress boot started the VM")
	}
	if strings.Contains(d.log(), "network") {
		t.Fatalf("egress boot reached docker with the VM stopped:\n%s", d.log())
	}
}

func TestStartEgressDarwinBootChecksTheNetworkWhenTheVMRuns(t *testing.T) {
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	svc, err := startEgress(t.TempDir(), l, config.Config{}, "darwin", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if !strings.Contains(d.log(), "network ls") {
		t.Fatalf("a running VM's egress network must be looked up at boot:\n%s", d.log())
	}
}

// goodFilterRules is `iptables -S` on a VM provisioned for option (c) after #113.
func goodFilterRules(egressPort, brokerPort int) string {
	return fmt.Sprintf(`-P INPUT ACCEPT
-P FORWARD DROP
-N DOCKER-USER
-A INPUT -s %[1]s -d %[2]s/32 -p tcp -m tcp --dport %[3]d -j ACCEPT
-A INPUT -s %[1]s -d %[2]s/32 -p tcp -m tcp --dport %[4]d -j ACCEPT
-A INPUT -s %[1]s -j DROP
-A INPUT -d %[2]s/32 -j DROP
-A FORWARD -j DOCKER-USER
-A DOCKER-USER -s %[1]s -j DROP
-A DOCKER-USER -d %[5]s/32 -p udp -m udp --dport 53 -j RETURN
-A DOCKER-USER -d %[5]s/32 -j DROP
`, boxvm.EgressSubnet, boxvm.EgressGateway, egressPort, brokerPort, boxvm.LimaHostGateway)
}

// relayDarwin is a darwin layer whose VM runs and whose egress network exists,
// with a docker that records every call and answers the relay's: no relay
// container yet, and a log that reports both listeners.
func relayDarwin(t *testing.T, rules string, egressPort, brokerPort int) (*boxLayer, *fakeColima, *fakeDocker) {
	t.Helper()
	l, fc, d := darwinLayer(t, false)
	fc.running = true
	fc.rules = rules
	fc.onSSH = func() {
		f, err := os.OpenFile(filepath.Join(d.dir, "log"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if err == nil {
			fmt.Fprintln(f, "COLIMA ssh iptables")
			f.Close()
		}
	}
	script := `#!/bin/sh
d=` + d.dir + `
echo "$*" >> "$d/log"
while [ "$1" = --host ] || [ "$1" = --config ]; do shift 2; done
case "$1 $2" in
"network ls") echo agentbox-egress-default ;;
"network inspect") printf '{"Internal":true,"Options":{"com.docker.network.bridge.enable_icc":"false"},"IPAM":{"Config":[{"Subnet":"` + boxvm.EgressSubnet + `","Gateway":"` + boxvm.EgressGateway + `"}]}}' ;;
"inspect --type") echo "Error: No such object" >&2; exit 1 ;;
"logs agentbox-relay-default") echo "listening ` + boxvm.EgressGateway + `:` + strconv.Itoa(egressPort) + `"; echo "listening ` + boxvm.EgressGateway + `:` + strconv.Itoa(brokerPort) + `" ;;
esac
exit 0
`
	if err := os.WriteFile(d.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return l, fc, d
}

// darwin option (c): the boot Ready of an existing network verifies the VM's
// firewall rules over colima ssh before it starts the relay container.
func TestStartEgressDarwinWiresTheRelay(t *testing.T) {
	ep, bp := freePort(t), freePort(t)
	l, _, d := relayDarwin(t, goodFilterRules(ep, bp), ep, bp)
	svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: ep, BrokerPort: bp}, "darwin", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	log := d.log()
	rules, run := strings.Index(log, "COLIMA ssh iptables"), strings.Index(log, "run -d --init --name agentbox-relay-default")
	if rules < 0 || run < 0 || rules > run {
		t.Fatalf("the INPUT rules must be read before the relay runs (rules at %d, run at %d):\n%s", rules, run, log)
	}
	if !strings.Contains(log, "--user "+strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid())) {
		t.Errorf("the relay must run as the daemon's uid:gid:\n%s", log)
	}
}

// Rules that do not match leave no relay: the daemon still starts (the first
// bound spawn is refused instead), and the reason is logged.
func TestStartEgressDarwinBadInputRulesStartNoRelay(t *testing.T) {
	ep, bp := freePort(t), freePort(t)
	l, _, d := relayDarwin(t, "-P INPUT ACCEPT\n", ep, bp)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: ep, BrokerPort: bp}, "darwin", noSessions)
	if err != nil {
		t.Fatalf("a refused relay must not fail the daemon: %v", err)
	}
	defer svc.Stop(context.Background())
	if strings.Contains(d.log(), "run -d") {
		t.Fatalf("a relay started without the INPUT rules:\n%s", d.log())
	}
	if !strings.Contains(buf.String(), "INPUT rules are missing") {
		t.Errorf("the refusal must be logged:\n%s", buf.String())
	}
	if _, err := svc.Svc.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "INPUT rules") {
		t.Errorf("a bound spawn must be refused while the rules are wrong: %v", err)
	}
}

// A VM provisioned before #113 still lets its default network past the relay
// to the broker port: the daemon starts, starts no relay, and refuses bound
// spawns with the fix in the message.
func TestStartEgressDarwinStaleBrokerRuleStartsNoRelay(t *testing.T) {
	ep, bp := freePort(t), freePort(t)
	stale := goodFilterRules(ep, bp) + fmt.Sprintf("-A DOCKER-USER -d %s/32 -p tcp -m tcp --dport %d -j RETURN\n", boxvm.LimaHostGateway, bp)
	l, _, d := relayDarwin(t, stale, ep, bp)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: ep, BrokerPort: bp}, "darwin", noSessions)
	if err != nil {
		t.Fatalf("a refused relay must not fail the daemon: %v", err)
	}
	defer svc.Stop(context.Background())
	if strings.Contains(d.log(), "run -d") {
		t.Fatalf("a relay started on a VM with the old broker rule:\n%s", d.log())
	}
	if _, err := svc.Svc.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Errorf("a bound spawn must be refused until the VM is recreated: %v", err)
	}
}

func TestEgressLookup(t *testing.T) {
	sessions := map[string]*session.Session{
		"claude-3": {ID: "claude-3", Box: "agent-box-x", Runtime: config.RuntimeBox, AWS: wireBinding(), BrokerNonce: "broker-only", EgressNonce: egressNonce},
		"claude-4": {ID: "claude-4", Runtime: config.RuntimeHost, AWS: wireBinding(), EgressNonce: egressNonce},
		"claude-5": {ID: "claude-5", Box: "agent-box-y", Runtime: config.RuntimeBox, EgressNonce: egressNonce},
		"claude-7": {ID: "claude-7", Box: "agent-box-w", Runtime: config.RuntimeBox, EgressNonce: egressNonce,
			AWSConns: []config.AWSConnection{{Name: "sbx-a", AWSBinding: *wireBinding()}}},
		"claude-6": {ID: "claude-6", Box: "agent-box-z", Runtime: config.RuntimeBox, EgressNonce: egressNonce,
			Tokens: []config.StaticToken{{Name: "sentry"}}, PATs: []config.StaticToken{{Name: "gh-pat-acme"}}},
	}
	look := egressLookup(func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok })

	v, ok := look("claude-3")
	if !ok || v.Nonce != egressNonce || v.Box != "agent-box-x" || !v.Boxed || !v.Live || v.AWS == nil {
		t.Fatalf("live boxed session: %+v %v", v, ok)
	}
	if v, ok := look("claude-4"); !ok || v.Boxed {
		t.Errorf("host-tier session must not read as boxed: %+v %v", v, ok)
	}
	// An unbound boxed session is open mode: no AWS binding, still boxed and live.
	if v, ok := look("claude-5"); !ok || !v.Boxed || !v.Live || v.AWS != nil || v.Nonce != egressNonce {
		t.Errorf("unbound boxed session: %+v %v", v, ok)
	}
	// The spawn-time token entries reach the proxy, which binds on them.
	if v, ok := look("claude-6"); !ok || len(v.Tokens) != 1 || v.Tokens[0].Name != "sentry" || len(v.PATs) != 1 || v.PATs[0].Name != "gh-pat-acme" {
		t.Errorf("token session: %+v %v", v, ok)
	}
	// Named AWS connections reach the proxy too: without them a connection-bound
	// session would read as unbound and get open egress.
	if v, ok := look("claude-7"); !ok || v.AWS != nil || len(v.AWSConns) != 1 || v.AWSConns[0].Name != "sbx-a" {
		t.Errorf("connection session: %+v %v", v, ok)
	}
	// Left the registry: unknown, so the proxy refuses it (no stale admit).
	delete(sessions, "claude-3")
	if _, ok := look("claude-3"); ok {
		t.Error("a session that left the registry must be unknown")
	}
}

func TestEgressServiceOptionsLinux(t *testing.T) {
	o := egressServiceOptions(config.Config{EnvLabel: "DEV"}, "linux", nil, nil)
	if o.NoProxy != "localhost,127.0.0.1,::1" || !o.Want.Internal || !o.Want.NoICC || o.GOOS != "linux" || o.Instance != "DEV" {
		t.Fatalf("options: %+v", o)
	}
	// Gate G2: plain Node fetch needs NODE_USE_ENV_PROXY=1 to honour HTTPS_PROXY.
	if want := []string{"NODE_USE_ENV_PROXY=1"}; !reflect.DeepEqual(o.ExtraEnv, want) {
		t.Fatalf("ExtraEnv = %q, want %q", o.ExtraEnv, want)
	}
	if o.Want.Gateway != "" || o.AfterNetwork != nil {
		t.Fatalf("linux must keep docker's gateway and have no network hook: %+v", o)
	}
}

func TestBridgeBoxes(t *testing.T) {
	out := "ab-box-1\tbridge\nab-box-2\tagentbox-egress-DEV\nab-box-3\tbridge,agentbox-egress-DEV\n\n"
	got := bridgeBoxes(out, "agentbox-egress-DEV")
	if want := []string{"ab-box-1", "ab-box-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bridgeBoxes = %q, want %q", got, want)
	}
	if got := bridgeBoxes("", "n"); len(got) != 0 {
		t.Errorf("empty listing: %q", got)
	}
}

func TestWarnBridgeBoxesQueriesThisInstanceOnly(t *testing.T) {
	bin, argv := fakeDockerScript(t, `printf 'ab-box-1\tbridge\n'`)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	warnBridgeBoxes(context.Background(), box.CLIClient{Bin: bin}, "DEV", "agentbox-egress-DEV")
	b, _ := os.ReadFile(argv)
	if !strings.Contains(string(b), "--filter label=io.culiops.agent-box.instance=DEV") {
		t.Errorf("argv %q must filter to this instance", b)
	}
	if !strings.Contains(buf.String(), "egress: boxes still outside agentbox-egress-DEV") || !strings.Contains(buf.String(), "ab-box-1") {
		t.Errorf("log %q", buf.String())
	}
}

func TestWarnBridgeBoxesDockerErrorIsNotFatal(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	warnBridgeBoxes(context.Background(), box.CLIClient{Bin: "/bin/false"}, "DEV", "agentbox-egress-DEV")
	if strings.Contains(buf.String(), "boxes still outside") {
		t.Errorf("nothing to warn about on a docker error: %q", buf.String())
	}
}

// fakeDocker writes a shell script standing in for the docker CLI. body runs
// with the arguments in "$@"; every call's argv (one line) is appended to log.
func fakeDockerScript(t *testing.T, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestStartEgressBootWithExistingNetworkDoesNotFail(t *testing.T) {
	// The network exists but is not internal: boot tries to bring the listener up
	// (spec correction 10), the network check refuses it, and boot only logs.
	bin, log := fakeDockerScript(t, `case "$1 $2" in
"network ls") echo agentbox-egress-X ;;
"network inspect") echo '{"Internal":false,"IPAM":{"Config":[{"Subnet":"172.30.0.0/16","Gateway":"172.30.0.1"}]}}' ;;
*) exit 1 ;;
esac`)
	l := &boxLayer{cli: box.CLIClient{Bin: bin}}
	svc, err := startEgress(t.TempDir(), l, config.Config{EnvLabel: "X"}, "linux", noSessions)
	if err != nil {
		t.Fatalf("boot must not fail on a bad existing network: %v", err)
	}
	defer svc.Stop(context.Background())
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "network inspect") {
		t.Errorf("boot never looked at the existing network: %q", b)
	}
	if strings.Contains(string(b), "network create") {
		t.Errorf("boot must not create the network (first bound spawn does): %q", b)
	}
}

func TestStartEgressBootWithoutNetworkCreatesNothing(t *testing.T) {
	bin, log := fakeDockerScript(t, `exit 0`) // network ls prints nothing: absent
	svc, err := startEgress(t.TempDir(), &boxLayer{cli: box.CLIClient{Bin: bin}}, config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "network create") {
		t.Errorf("boot created the network: %q", b)
	}
}

func TestEgressExecPassesTheCredentialOnStdinOnly(t *testing.T) {
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	bin, log := fakeDockerScript(t, "cat > "+stdinFile+"\necho ok")
	l := &boxLayer{cli: box.CLIClient{Bin: bin}}
	out, err := l.egressExec("agent-box-x")(context.Background(), "SCRIPT-BODY", "user:pass-secret")
	if err != nil || out != "ok" {
		t.Fatalf("out %q err %v", out, err)
	}
	argv, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(argv)); got != "exec -i agent-box-x node -e SCRIPT-BODY" {
		t.Errorf("argv = %q", got)
	}
	if strings.Contains(string(argv), "pass-secret") {
		t.Error("credential leaked into argv")
	}
	if in, _ := os.ReadFile(stdinFile); string(in) != "user:pass-secret" {
		t.Errorf("stdin = %q", in)
	}
}

func TestEgressVerifyFailsClosedWhenTheProxyCannotStart(t *testing.T) {
	l := absentLayer() // docker always fails, so Ready fails
	svc, err := startEgress(t.TempDir(), l, config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	err = l.egressVerify(svc.Svc)(context.Background(), "agent-box-x")
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to wrap box.ErrBoxUnavailable", err)
	}
}

// darwin: the egress probe (its ALLOW 200 is the positive control) runs first,
// then the loopback check without a DNS control. Both must pass.
func TestEgressVerifyDarwinRunsTheProbeThenTheLoopbackCheck(t *testing.T) {
	bin, dir := statefulEgressDocker(t)
	port := freePort(t)
	l := &boxLayer{goos: "darwin", cli: box.CLIClient{Bin: bin}, relayKey: testRelayKey}
	svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: port}, "darwin", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	var order []string
	var target egress.ProbeTarget
	l.egressProbe = func(_ context.Context, _ *egress.Proxy, name string, tg egress.ProbeTarget, _ egress.ExecInput) error {
		order = append(order, "probe")
		target = tg
		return nil
	}
	// DNS is closed in a bound box: "DNS fail" is the expected answer.
	if err := os.WriteFile(filepath.Join(dir, "exec"), []byte("DNS fail EAI_AGAIN\nBLOCKED a ETIMEDOUT\nBLOCKED b ETIMEDOUT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.egressVerify(svc.Svc)(context.Background(), "agent-box-x"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"probe"}) || target.ProxyHost != boxvm.EgressGateway || target.Port != port {
		t.Fatalf("probe order %v target %+v", order, target)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if !regexp.MustCompile(`(?m)^exec agent-box-x node -e `).Match(log) {
		t.Fatalf("the loopback check never ran in the box:\n%s", log)
	}
}

func TestEgressVerifyDarwinFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		probeErr error
		execOut  string
		wantExec bool
	}{
		"probe fails: the loopback check does not run": {errors.New("egress check failed: x"), "BLOCKED a\n", false},
		"loopback reached":      {nil, "REACHED host.lima.internal\nBLOCKED b\n", true},
		"loopback inconclusive": {nil, "", true},
	} {
		bin, dir := statefulEgressDocker(t)
		l := &boxLayer{goos: "darwin", cli: box.CLIClient{Bin: bin}, relayKey: testRelayKey}
		svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: freePort(t)}, "darwin", noSessions)
		if err != nil {
			t.Fatal(err)
		}
		probeErr := tc.probeErr
		l.egressProbe = func(context.Context, *egress.Proxy, string, egress.ProbeTarget, egress.ExecInput) error {
			return probeErr
		}
		_ = os.WriteFile(filepath.Join(dir, "exec"), []byte(tc.execOut), 0o600)
		err = l.egressVerify(svc.Svc)(context.Background(), "agent-box-x")
		svc.Stop(context.Background())
		if !errors.Is(err, box.ErrBoxUnavailable) {
			t.Errorf("%s: err = %v, want it to wrap box.ErrBoxUnavailable", name, err)
		}
		log, _ := os.ReadFile(filepath.Join(dir, "log"))
		ran := regexp.MustCompile(`(?m)^exec agent-box-x node -e `).Match(log)
		if ran != tc.wantExec {
			t.Errorf("%s: loopback check ran = %v, want %v", name, ran, tc.wantExec)
		}
	}
}

// A darwin refusal of a bound box names `box setup -recreate` when the VM's
// provision config has drifted (a VM provisioned before the egress subnet rules
// existed), and only then. The refusal itself is the same either way.
func TestEgressVerifyDarwinRefusalHintsRecreateOnlyWhenDrifted(t *testing.T) {
	const hint = "box setup -recreate"
	for name, tc := range map[string]struct {
		drifted bool
		novm    bool
		want    bool
	}{
		"drifted VM":    {drifted: true, want: true},
		"up-to-date VM": {drifted: false, want: false},
		"no VM (linux)": {novm: true, want: false},
	} {
		bin, _ := statefulEgressDocker(t)
		l := &boxLayer{goos: "darwin", cli: box.CLIClient{Bin: bin}, relayKey: testRelayKey}
		var vm *boxvm.VM
		if !tc.novm {
			dl, _, _ := darwinLayer(t, true)
			if !tc.drifted {
				if err := dl.vm.Provision(context.Background(), false, io.Discard); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(dl.vm.Config().ColimaHome(), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dl.vm.Config().StampPath(), []byte("old\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if dl.vm.Drifted() != tc.drifted {
				t.Fatalf("%s: setup Drifted = %v", name, dl.vm.Drifted())
			}
			vm = dl.vm
		}
		// The VM joins the layer after startEgress: this test is about the
		// refusal's hint, so Ready must not run the relay hook (a VM in the
		// layer at that point gets one; see TestStartEgressDarwinWiresTheRelay).
		svc, err := startEgress(t.TempDir(), l, config.Config{EgressPort: freePort(t)}, "darwin", noSessions)
		if err != nil {
			t.Fatal(err)
		}
		l.vm = vm
		l.egressProbe = func(context.Context, *egress.Proxy, string, egress.ProbeTarget, egress.ExecInput) error {
			return errors.New("egress check failed: the box can reach x directly; its network is not the egress network")
		}
		err = l.egressVerify(svc.Svc)(context.Background(), "agent-box-x")
		svc.Stop(context.Background())
		if !errors.Is(err, box.ErrBoxUnavailable) || !strings.Contains(err.Error(), "its network is not the egress network") {
			t.Fatalf("%s: err = %v, want the original refusal kept", name, err)
		}
		if got := strings.Contains(err.Error(), hint); got != tc.want {
			t.Errorf("%s: hint present = %v, want %v: %v", name, got, tc.want, err)
		}
	}
}
