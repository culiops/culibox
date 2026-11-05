package boxvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/proxyline"
)

// goodRules is `iptables -S` (the whole filter table) on a VM provisioned for
// option (c) after #113, in iptables' own output format.
const goodRules = `-P INPUT ACCEPT
-P FORWARD DROP
-P OUTPUT ACCEPT
-N DOCKER
-N DOCKER-USER
-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9072 -j ACCEPT
-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9071 -j ACCEPT
-A INPUT -s 172.29.72.0/24 -j DROP
-A INPUT -d 172.29.72.1/32 -j DROP
-A INPUT -i lo -j ACCEPT
-A FORWARD -j DOCKER-USER
-A DOCKER-USER -s 172.29.72.0/24 -j DROP
-A DOCKER-USER -d 192.168.5.2/32 -p udp -m udp --dport 53 -j RETURN
-A DOCKER-USER -d 192.168.5.2/32 -j DROP
-A DOCKER -d 172.17.0.2/32 ! -i docker0 -o docker0 -p tcp -m tcp --dport 8080 -j ACCEPT
`

// staleBrokerReturn is the DOCKER-USER rule VMs provisioned before #113 carry.
const staleBrokerReturn = "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 9071 -j RETURN\n"

const optionARules = `-P INPUT ACCEPT
-A INPUT -s 172.29.72.0/24 -j DROP
`

type fakeVM struct {
	out string
	err error
}

func (f fakeVM) FilterRules(context.Context) (string, error) { return f.out, f.err }

// fakeDocker records argv and answers by subcommand. Zero values mean success
// with empty output; inspect defaults to "no such container".
type fakeDocker struct {
	mu      sync.Mutex
	calls   [][]string
	inspect string // reply; "" = No such container
	inspErr error  // overrides inspect when set
	logs    string
	logsErr error
	rmErr   error
	// state answers the readiness poll's `inspect {{.State.Running}}`; "" =
	// "true". stateErr overrides it.
	state    string
	stateErr error
	// rmCtxErr is ctx.Err() as each rm saw it.
	rmCtxErr []error
	// warn simulates a docker CLI warning on stderr: Run (combined output)
	// prefixes it to every answer, Stdout never sees it.
	warn string
	// envs is the env of each RunEnv call, in call order; its argv is in calls.
	// runViaRun is every `run` that went through plain Run instead: the relay
	// key must never be one of them.
	envs      [][]string
	runViaRun [][]string
}

func (f *fakeDocker) Run(ctx context.Context, args []string) (string, error) {
	switch args[0] {
	case "rm":
		f.mu.Lock()
		f.rmCtxErr = append(f.rmCtxErr, ctx.Err())
		f.mu.Unlock()
	case "run":
		f.mu.Lock()
		f.runViaRun = append(f.runViaRun, append([]string(nil), args...))
		f.mu.Unlock()
	}
	out, err := f.answer(args)
	if err == nil && f.warn != "" {
		out = f.warn + "\n" + out
	}
	return out, err
}

// RunEnv is Run with the child's extra environment, which is recorded apart
// from argv.
func (f *fakeDocker) RunEnv(ctx context.Context, args, env []string) (string, error) {
	f.mu.Lock()
	f.envs = append(f.envs, append([]string(nil), env...))
	f.mu.Unlock()
	out, err := f.answer(args)
	if err == nil && f.warn != "" {
		out = f.warn + "\n" + out
	}
	return out, err
}

func (f *fakeDocker) Stdout(ctx context.Context, args []string) (string, error) {
	return f.answer(args)
}

func (f *fakeDocker) answer(args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "inspect":
		if isStateInspect(args) {
			if f.stateErr != nil {
				return "", f.stateErr
			}
			if f.state == "" {
				return "true\n", nil
			}
			return f.state, nil
		}
		if f.inspErr != nil {
			return "", f.inspErr
		}
		if f.inspect == "" {
			return "", errors.New("Error: No such container: " + args[len(args)-1])
		}
		return f.inspect, nil
	case "logs":
		return f.logs, f.logsErr
	case "rm":
		return "", f.rmErr
	}
	return "", nil
}

// isStateInspect tells the readiness poll's inspect from Ensure's first one.
func isStateInspect(args []string) bool {
	for i, a := range args {
		if a == "--format" && i+1 < len(args) {
			return args[i+1] == "{{.State.Running}}"
		}
	}
	return false
}

func (f *fakeDocker) subcommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, c := range f.calls {
		if isStateInspect(c) {
			s = append(s, "state")
			continue
		}
		s = append(s, c[0])
	}
	return s
}

func (f *fakeDocker) find(sub string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

// relayKeyOf is a test key; no two seeds give the same one.
func relayKeyOf(seed byte) proxyline.Key {
	var raw [32]byte
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	k, err := proxyline.KeyFromBytes(raw[:])
	if err != nil {
		panic(err)
	}
	return k
}

var (
	relayTestKey  = relayKeyOf(0x41)
	otherRelayKey = relayKeyOf(0x42)
)

func testRelay(vm fakeVM, d *fakeDocker) *Relay {
	return &Relay{VM: vm, Docker: d, Image: "agent-box/box:test", Instance: "prod", EgressPort: 9072, BrokerPort: 9071,
		UID: 501, GID: 20, Key: relayTestKey, ReadyTimeout: 300 * time.Millisecond}
}

var gw = netip.MustParseAddr(EgressGateway)

const bothListening = "listening 172.29.72.1:9072\nlistening 172.29.72.1:9071\n"

func TestCheckInputRulesAcceptsTheFourOnTop(t *testing.T) {
	if err := CheckInputRules(goodRules, 9072, 9071); err != nil {
		t.Fatal(err)
	}
}

func TestCheckInputRulesRefuses(t *testing.T) {
	acc := func(port int) string {
		return fmt.Sprintf("-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport %d -j ACCEPT\n", port)
	}
	subDrop := "-A INPUT -s 172.29.72.0/24 -j DROP\n"
	gwDrop := "-A INPUT -d 172.29.72.1/32 -j DROP\n"
	cases := map[string]string{
		"option (a) VM":         optionARules,
		"empty":                 "-P INPUT ACCEPT\n",
		"accepts below DROP":    "-P INPUT ACCEPT\n" + subDrop + gwDrop + acc(9072) + acc(9071),
		"missing gateway DROP":  "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + subDrop,
		"wrong port":            "-P INPUT ACCEPT\n" + acc(9072) + acc(9099) + subDrop + gwDrop,
		"ports swapped":         "-P INPUT ACCEPT\n" + acc(9071) + acc(9072) + subDrop + gwDrop,
		"foreign rule on top":   "-P INPUT ACCEPT\n-A INPUT -j ACCEPT\n" + acc(9072) + acc(9071) + subDrop + gwDrop,
		"extra match on ours":   "-P INPUT ACCEPT\n-A INPUT -i eth0 -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9072 -j ACCEPT\n" + acc(9071) + subDrop + gwDrop,
		"wider subnet DROP":     "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + "-A INPUT -s 172.29.0.0/16 -j DROP\n" + gwDrop,
		"gateway DROP is /24":   "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + subDrop + "-A INPUT -d 172.29.72.0/24 -j DROP\n",
		"accept for other dest": "-P INPUT ACCEPT\n-A INPUT -s 172.29.72.0/24 -d 172.29.72.9/32 -p tcp -m tcp --dport 9072 -j ACCEPT\n" + acc(9071) + subDrop + gwDrop,
		"negated source ACCEPT": "-P INPUT ACCEPT\n-A INPUT ! -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9072 -j ACCEPT\n" + acc(9071) + subDrop + gwDrop,
		"negated subnet DROP":   "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + "-A INPUT ! -s 172.29.72.0/24 -j DROP\n" + gwDrop,
		"subnet RETURN":         "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + "-A INPUT -s 172.29.72.0/24 -j RETURN\n" + gwDrop,
		"subnet LOG":            "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + "-A INPUT -s 172.29.72.0/24 -j LOG\n" + gwDrop,
		"gateway RETURN":        "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + subDrop + "-A INPUT -d 172.29.72.1/32 -j RETURN\n",
		"gateway LOG":           "-P INPUT ACCEPT\n" + acc(9072) + acc(9071) + subDrop + "-A INPUT -d 172.29.72.1/32 -j LOG\n",
		"udp ACCEPT":            "-P INPUT ACCEPT\n-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p udp -m udp --dport 9072 -j ACCEPT\n" + acc(9071) + subDrop + gwDrop,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckInputRules(out, 9072, 9071)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "box setup -recreate") {
				t.Fatalf("error does not say how to fix it: %v", err)
			}
		})
	}
}

// The provisioned DOCKER-USER chain passes, with or without the bare RETURN
// some docker versions append; a TCP ACCEPT in another chain (docker's own
// DOCKER chain, the relay's INPUT rules) is not DOCKER-USER's business.
func TestCheckDockerUserRulesAcceptsTheProvisionedChain(t *testing.T) {
	for name, out := range map[string]string{
		"provisioned":            goodRules,
		"docker's bare RETURN":   goodRules + "-A DOCKER-USER -j RETURN\n",
		"a tcp RETURN elsewhere": goodRules + "-A FORWARD -d 192.168.5.2/32 -p tcp -m tcp --dport 9071 -j RETURN\n",
	} {
		if err := CheckDockerUserRules(out); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Any DOCKER-USER rule that lets TCP through is refused: the broker rule of a
// VM provisioned before #113 (whatever broker port or gateway address it was
// written for, wherever it sits) and anything shaped like it.
func TestCheckDockerUserRulesRefusesATCPPassThrough(t *testing.T) {
	for name, out := range map[string]string{
		"broker rule before #113": staleBrokerReturn + goodRules,
		"after the gateway DROP":  goodRules + staleBrokerReturn,
		"another broker port":     goodRules + "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 9100 -j RETURN\n",
		"another gateway address": goodRules + "-A DOCKER-USER -d 192.168.5.3/32 -p tcp -m tcp --dport 9071 -j RETURN\n",
		"ACCEPT target":           goodRules + "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 9071 -j ACCEPT\n",
	} {
		err := CheckDockerUserRules(out)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "box setup -recreate") || !strings.Contains(err.Error(), "-p tcp") {
			t.Errorf("%s: the error must name the rule and the fix: %v", name, err)
		}
	}
}

// The gateway DROP itself must be there, for the address the relay dials
// (LimaHostGateway): without it nothing stops the VM's default network from
// reaching the Mac's loopback.
func TestCheckDockerUserRulesRequiresTheGatewayDrop(t *testing.T) {
	noDrop := strings.Replace(goodRules, "-A DOCKER-USER -d 192.168.5.2/32 -j DROP\n", "", 1)
	for name, out := range map[string]string{
		"chain not created yet":     "-P INPUT ACCEPT\n",
		"no gateway DROP":           noDrop,
		"DROP for another address":  noDrop + "-A DOCKER-USER -d 192.168.5.3/32 -j DROP\n",
		"DROP narrowed to one port": noDrop + "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 22 -j DROP\n",
	} {
		err := CheckDockerUserRules(out)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "box setup -recreate") || !strings.Contains(err.Error(), "192.168.5.2") {
			t.Errorf("%s: the error must name the gateway and the fix: %v", name, err)
		}
	}
}

// A VM that still has the old broker rule gets no new relay and no boxed
// spawn. A relay that already runs is kept: the direct path does not go
// through it, so removing it would only cut live sessions.
func TestEnsureRefusesAStaleBrokerReturnAndKeepsTheRelay(t *testing.T) {
	stale := goodRules + staleBrokerReturn
	r := testRelay(fakeVM{out: stale}, nil)
	d := &fakeDocker{inspect: "true\t" + r.configHash(gw) + "\n", logs: bothListening}
	r.Docker = d
	err := r.Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Fatalf("running relay: err = %v", err)
	}
	if len(d.find("rm")) != 0 || len(d.find("run")) != 0 {
		t.Fatalf("running relay: churned: %v", d.subcommands())
	}

	d = &fakeDocker{logs: bothListening}
	err = testRelay(fakeVM{out: stale}, d).Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Fatalf("no relay: err = %v", err)
	}
	if len(d.find("run")) != 0 || len(d.find("rm")) != 0 {
		t.Fatalf("no relay: docker calls %v", d.subcommands())
	}
}

// A relay that is not current may be one from before #118, which does not
// authenticate the listener, so a refusal that keeps a relay keeps only a current
// one: any other is removed, and nothing is started in its place.
func TestEnsureRemovesAStaleRelayOnAStaleBrokerReturn(t *testing.T) {
	r := testRelay(fakeVM{out: goodRules + staleBrokerReturn}, nil)
	d := &fakeDocker{inspect: "true\t" + v1RelayHash(t, r) + "\n", logs: bothListening}
	r.Docker = d
	err := r.Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect,rm" {
		t.Fatalf("calls = %s, want the stale relay inspected and removed, nothing started", got)
	}
	if rm := d.find("rm"); strings.Join(rm[0], " ") != "rm -f agentbox-relay-prod" {
		t.Fatalf("rm = %v", rm)
	}
}

func TestEnsureChecksRulesBeforeTheRelay(t *testing.T) {
	d := &fakeDocker{}
	err := testRelay(fakeVM{out: "-P INPUT ACCEPT\n"}, d).Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Fatalf("err = %v", err)
	}
	if n := len(d.find("run")); n != 0 {
		t.Fatalf("%d run calls with no INPUT rules", n)
	}
	rm := d.find("rm")
	if len(rm) != 1 || strings.Join(rm[0], " ") != "rm -f agentbox-relay-prod" {
		t.Fatalf("rm calls = %v", rm)
	}
}

func TestEnsureRefusesWhenTheRulesCannotBeRead(t *testing.T) {
	d := &fakeDocker{}
	err := testRelay(fakeVM{err: errors.New("ssh: connection refused")}, d).Ensure(context.Background(), gw)
	if err == nil || len(d.find("run")) != 0 {
		t.Fatalf("err = %v, calls = %v", err, d.calls)
	}
	if len(d.find("rm")) != 0 {
		t.Fatalf("a rules read error removed the relay: %v", d.subcommands())
	}
	if !strings.Contains(err.Error(), "could not be read") || !strings.Contains(err.Error(), "ssh: connection refused") ||
		strings.Contains(err.Error(), "recreate") {
		t.Errorf("err = %v", err)
	}
}

// #113: a transient ssh failure must not cut live sessions, so a relay that is
// current (the label equals the config hash: same script, flags and key) is kept
// on a rules read error. It is looked at, though: that is what makes it kept
// rather than merely left.
func TestEnsureKeepsACurrentRelayWhenTheRulesCannotBeRead(t *testing.T) {
	r := testRelay(fakeVM{err: errors.New("ssh: connection refused")}, nil)
	d := &fakeDocker{inspect: "true\t" + r.configHash(gw) + "\n", logs: bothListening}
	r.Docker = d
	err := r.Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "could not be read") || !strings.Contains(err.Error(), "ssh: connection refused") ||
		strings.Contains(err.Error(), "recreate") {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect" {
		t.Fatalf("calls = %s, want the relay inspected and nothing else", got)
	}
}

// Any relay that is not current is removed on that refusal: during an upgrade it
// can be one from before #118 that does not authenticate the listener (D7). One
// that has stopped forwards nothing, and the next good Ensure replaces it, so it
// goes too.
func TestEnsureRemovesAStaleRelayWhenTheRulesCannotBeRead(t *testing.T) {
	other := testRelay(fakeVM{}, nil)
	other.Key = otherRelayKey
	for name, inspect := range map[string]func(r *Relay) string{
		"a v1 relay":          func(r *Relay) string { return "true\t" + v1RelayHash(t, r) },
		"another key":         func(r *Relay) string { return "true\t" + other.configHash(gw) },
		"stopped but current": func(r *Relay) string { return "false\t" + r.configHash(gw) },
	} {
		t.Run(name, func(t *testing.T) {
			r := testRelay(fakeVM{err: errors.New("ssh: connection refused")}, nil)
			d := &fakeDocker{inspect: inspect(r) + "\n", logs: bothListening}
			r.Docker = d
			err := r.Ensure(context.Background(), gw)
			if err == nil || !strings.Contains(err.Error(), "could not be read") || !strings.Contains(err.Error(), "ssh: connection refused") {
				t.Fatalf("err = %v", err)
			}
			if got := strings.Join(d.subcommands(), ","); got != "inspect,rm" {
				t.Fatalf("calls = %s, want the relay inspected and removed, nothing started", got)
			}
			if rm := d.find("rm"); strings.Join(rm[0], " ") != "rm -f agentbox-relay-prod" {
				t.Fatalf("rm = %v", rm)
			}
		})
	}
}

// When docker cannot say what runs, nothing is removed (there is no telling a
// current relay from a stale one) and the operator is told both things: why the
// Ready was refused and that the relay could not be checked.
func TestEnsureReportsAnInspectFailureOnARefusal(t *testing.T) {
	inspErr := errors.New("Cannot connect to the Docker daemon")
	for name, tc := range map[string]struct {
		vm   fakeVM
		want string
	}{
		"rules unreadable":  {fakeVM{err: errors.New("ssh: connection refused")}, "ssh: connection refused"},
		"stale broker rule": {fakeVM{out: goodRules + staleBrokerReturn}, "box setup -recreate"},
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDocker{inspErr: inspErr}
			err := testRelay(tc.vm, d).Ensure(context.Background(), gw)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "inspecting the relay") ||
				!errors.Is(err, inspErr) {
				t.Fatalf("err = %v, want the refusal (%q) and the failed inspect", err, tc.want)
			}
			if got := strings.Join(d.subcommands(), ","); got != "inspect" {
				t.Fatalf("calls = %s, want one inspect: nothing removed, nothing started", got)
			}
		})
	}
}

func TestEnsureReportsAFailedRemovalAfterARuleMismatch(t *testing.T) {
	d := &fakeDocker{rmErr: errors.New("permission denied")}
	err := testRelay(fakeVM{out: "-P INPUT ACCEPT\n"}, d).Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") ||
		!strings.Contains(err.Error(), "removing the relay") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureReadsTheInspectFromStdoutOnly(t *testing.T) {
	r := testRelay(fakeVM{out: goodRules}, nil)
	d := &fakeDocker{inspect: "true\t" + r.configHash(gw) + "\n", warn: "WARNING: current context is not the default", logs: bothListening}
	r.Docker = d
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if len(d.find("rm")) != 0 || len(d.find("run")) != 0 {
		t.Fatalf("a stderr warning churned the relay: %v", d.subcommands())
	}
}

func TestEnsureRefusesAnOptionAVM(t *testing.T) {
	d := &fakeDocker{}
	err := testRelay(fakeVM{out: optionARules}, d).Ensure(context.Background(), gw)
	if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
		t.Fatalf("err = %v", err)
	}
	if n := len(d.find("run")); n != 0 {
		t.Fatalf("%d run calls on an option (a) VM", n)
	}
	if rm := d.find("rm"); len(rm) != 1 || strings.Join(rm[0], " ") != "rm -f agentbox-relay-prod" {
		t.Fatalf("rm calls = %v, want the relay removed", rm)
	}
}

func TestEnsureRefusesAForeignGateway(t *testing.T) {
	d := &fakeDocker{}
	err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), netip.MustParseAddr("172.29.73.1"))
	if err == nil {
		t.Fatal("accepted a foreign gateway")
	}
	if len(d.calls) != 0 {
		t.Fatalf("docker called: %v", d.calls)
	}
}

func TestEnsureStartsTheRelay(t *testing.T) {
	d := &fakeDocker{logs: bothListening}
	r := testRelay(fakeVM{out: goodRules}, d)
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	runs := d.find("run")
	if len(runs) != 1 {
		t.Fatalf("run calls = %v", runs)
	}
	argv := runs[0]
	joined := " " + strings.Join(argv, " ") + " "
	for _, want := range []string{
		" -d ", " --init ", " --name agentbox-relay-prod ", " --network host ", " --entrypoint node ",
		" --cap-drop=ALL ", " --security-opt no-new-privileges ", " --read-only ", " --pids-limit 64 ",
		" --pull=never ", " --user 501:20 ", " -e " + RelayKeyEnv + " ",
		" io.culiops.agent-box.relay=prod ", " io.culiops.agent-box.relay-config=" + r.configHash(gw) + " ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q:\n%s", want, joined)
		}
	}
	tail := []string{"agent-box/box:test", "-e", relayScript, "172.29.72.1", "9072", "192.168.5.2", "9072", "9071", "192.168.5.2", "9071"}
	if got := argv[len(argv)-len(tail):]; strings.Join(got, "\x00") != strings.Join(tail, "\x00") {
		t.Errorf("argv tail = %q, want %q", got, tail)
	}
	if strings.Contains(joined, "--restart") {
		t.Error("the relay must not restart on its own: Ensure owns its lifecycle")
	}
	for _, c := range d.calls {
		for _, a := range c {
			if strings.Contains(a, r.Key.Hex()) {
				t.Fatalf("a docker argv (%s) carries the relay key", c[0])
			}
		}
	}
	if strings.Contains(joined, "io.culiops.agent-box.instance") {
		t.Error("the instance label would let box reconcile reap the relay")
	}
}

func TestEnsureKeepsAMatchingRelay(t *testing.T) {
	r := testRelay(fakeVM{out: goodRules}, nil)
	d := &fakeDocker{inspect: "true\t" + r.configHash(gw) + "\n", logs: bothListening}
	r.Docker = d
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if len(d.find("rm")) != 0 || len(d.find("run")) != 0 {
		t.Fatalf("calls = %v", d.subcommands())
	}
}

func TestEnsureReplacesAStoppedRelay(t *testing.T) {
	r := testRelay(fakeVM{out: goodRules}, nil)
	d := &fakeDocker{inspect: "false\t" + r.configHash(gw) + "\n", logs: bothListening}
	r.Docker = d
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect,rm,run,logs" {
		t.Fatalf("calls = %s", got)
	}
}

func TestEnsureReplacesAChangedRelay(t *testing.T) {
	d := &fakeDocker{inspect: "true\tdeadbeef\n", logs: bothListening}
	if err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect,rm,run,logs" {
		t.Fatalf("calls = %s", got)
	}
}

func TestEnsureFailsClosedOnAnInspectError(t *testing.T) {
	d := &fakeDocker{inspErr: errors.New("Cannot connect to the Docker daemon")}
	err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw)
	if err == nil || len(d.find("run")) != 0 {
		t.Fatalf("err = %v, calls = %v", err, d.subcommands())
	}
}

func TestEnsureIgnoresAnAbsentContainerOnRm(t *testing.T) {
	d := &fakeDocker{inspect: "true\tdeadbeef\n", logs: bothListening, rmErr: errors.New("Error: No such container: x")}
	if err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReturnsARealRmError(t *testing.T) {
	d := &fakeDocker{inspect: "true\tdeadbeef\n", rmErr: errors.New("permission denied")}
	err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw)
	if err == nil || len(d.find("run")) != 0 {
		t.Fatalf("err = %v, calls = %v", err, d.subcommands())
	}
}

func TestEnsureRemovesARelayThatDidNotListen(t *testing.T) {
	for name, logs := range map[string]string{
		"listen error": "ERR 9072 EADDRINUSE\n",
		"one port":     "listening 172.29.72.1:9072\n",
		"no output":    "",
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDocker{logs: logs}
			err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw)
			if err == nil {
				t.Fatal("reported ready")
			}
			calls := d.subcommands()
			if calls[len(calls)-1] != "rm" {
				t.Fatalf("calls = %v, want a final rm", calls)
			}
			if logs != "" && !strings.Contains(err.Error(), strings.TrimSpace(logs)) {
				t.Errorf("error does not carry the log: %v", err)
			}
		})
	}
}

func TestEnsureRefusesAListenerOnTheWrongAddress(t *testing.T) {
	d := &fakeDocker{logs: "listening 0.0.0.0:9072\nlistening 0.0.0.0:9071\n"}
	r := testRelay(fakeVM{out: goodRules}, d)
	r.ReadyTimeout = 30 * time.Second
	start := time.Now()
	err := r.Ensure(context.Background(), gw)
	if err == nil {
		t.Fatal("accepted a wildcard listener")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a wrong address should fail at once, not wait out the timeout")
	}
	if calls := d.subcommands(); calls[len(calls)-1] != "rm" {
		t.Fatalf("calls = %v, want a final rm", calls)
	}
}

func TestEnsureHonoursContextCancellation(t *testing.T) {
	d := &fakeDocker{}
	r := testRelay(fakeVM{out: goodRules}, d)
	r.ReadyTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := r.Ensure(ctx, gw); err == nil {
		t.Fatal("reported ready")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Ensure ignored its context")
	}
	if calls := d.subcommands(); calls[len(calls)-1] != "rm" {
		t.Fatalf("calls = %v, want a final rm", calls)
	}
	if len(d.rmCtxErr) != 1 || d.rmCtxErr[0] != nil {
		t.Fatalf("rm ran on a cancelled context: %v", d.rmCtxErr)
	}
}

func TestEnsureFailsFastOnARelayThatExited(t *testing.T) {
	for name, logs := range map[string]string{
		"no output": "",
		"one port":  "listening 172.29.72.1:9072\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDocker{logs: logs, state: "false\n"}
			r := testRelay(fakeVM{out: goodRules}, d)
			r.ReadyTimeout = 30 * time.Second
			start := time.Now()
			err := r.Ensure(context.Background(), gw)
			if err == nil {
				t.Fatal("reported ready")
			}
			if time.Since(start) > 5*time.Second {
				t.Errorf("an exited relay waited %s instead of failing at once", time.Since(start))
			}
			if !strings.Contains(err.Error(), "exited") {
				t.Errorf("err = %v, want it to say the relay exited", err)
			}
			if calls := d.subcommands(); calls[len(calls)-1] != "rm" {
				t.Fatalf("calls = %v, want a final rm", calls)
			}
		})
	}
}

func TestEnsureFailsOnARelayThatVanished(t *testing.T) {
	d := &fakeDocker{stateErr: errors.New("Error: No such container: agentbox-relay-prod")}
	r := testRelay(fakeVM{out: goodRules}, d)
	r.ReadyTimeout = 30 * time.Second
	start := time.Now()
	if err := r.Ensure(context.Background(), gw); err == nil {
		t.Fatal("reported ready")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a vanished relay should fail at once")
	}
	if calls := d.subcommands(); calls[len(calls)-1] != "rm" {
		t.Fatalf("calls = %v, want a final rm", calls)
	}
}

func TestRelayName(t *testing.T) {
	if got := RelayName("prod"); got != "agentbox-relay-prod" {
		t.Fatal(got)
	}
	if got := RelayName("a b/c:d.e_f-g"); got != "agentbox-relay-a-b-c-d.e_f-g" {
		t.Fatal(got)
	}
}

func TestConfigHashCoversWhatTheRelayDoes(t *testing.T) {
	base := testRelay(fakeVM{}, nil)
	h := base.configHash(gw)
	if other := base.configHash(netip.MustParseAddr("172.29.73.1")); other == h {
		t.Error("gateway change leaves the hash unchanged")
	}
	for name, mod := range map[string]func(*Relay){
		"image":  func(r *Relay) { r.Image = "other" },
		"egress": func(r *Relay) { r.EgressPort = 1 },
		"broker": func(r *Relay) { r.BrokerPort = 1 },
		"uid":    func(r *Relay) { r.UID = 1 },
		"gid":    func(r *Relay) { r.GID = 1 },
	} {
		r := testRelay(fakeVM{}, nil)
		mod(r)
		if r.configHash(gw) == h {
			t.Errorf("%s change leaves the hash unchanged", name)
		}
	}
}

// Every element of the run argv (flags, script, ports) feeds the hash, so a
// change to any of them alone replaces a running relay.
func TestConfigHashCoversEveryRunArg(t *testing.T) {
	r := testRelay(fakeVM{}, nil)
	args := r.runArgs(gw)
	hashOf := func(a []string) string { return hashWithKeyID(r, a) }
	h := hashOf(args)
	if r.configHash(gw) != h {
		t.Fatal("configHash is not the hash of runArgs plus the key id")
	}
	for i := range args {
		mod := append([]string(nil), args...)
		mod[i] += "x"
		if hashOf(mod) == h {
			t.Errorf("changing arg %d (%q) leaves the hash unchanged", i, args[i])
		}
		if hashOf(append(append([]string(nil), args[:i]...), args[i+1:]...)) == h {
			t.Errorf("dropping arg %d (%q) leaves the hash unchanged", i, args[i])
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, relayConfigLabel) {
			t.Fatalf("runArgs carries the hash label: %q", a)
		}
	}
}

// hashWithKeyID is the config hash of an argv for r: the argv plus the id of
// r's key, which is what configHash adds to runArgs.
func hashWithKeyID(r *Relay, args []string) string {
	return hashArgs(append(append([]string(nil), args...), "relay-key-id="+r.Key.ID()))
}

// The key reaches the relay through the docker child's environment, by name:
// argv is world-readable, so neither it nor a label may carry the value.
func TestRunArgsNeverCarryTheKey(t *testing.T) {
	r := testRelay(fakeVM{}, nil)
	args := r.runArgs(gw, "--label", relayConfigLabel+"="+r.configHash(gw))
	image := slices.Index(args, r.Image)
	if image < 0 {
		t.Fatal("no image in the run argv")
	}
	named := false
	for i := 0; i+1 < image; i++ {
		if args[i] == "-e" && args[i+1] == RelayKeyEnv {
			named = true
		}
	}
	if !named {
		t.Errorf("the run flags lack the adjacent pair -e %s", RelayKeyEnv)
	}
	for i, a := range args {
		if strings.Contains(a, r.Key.Hex()) {
			t.Errorf("argv element %d carries the relay key", i)
		}
		if strings.Contains(a, "relay-key-id") {
			t.Errorf("argv element %d carries the key id; it belongs in the hash only", i)
		}
	}
}

func TestRelayKeyEnvIsTheDocumentedName(t *testing.T) {
	if RelayKeyEnv != "AGENTBOX_RELAY_KEY" {
		t.Fatalf("RelayKeyEnv = %q", RelayKeyEnv)
	}
	if !strings.Contains(relayScript, "process.env."+RelayKeyEnv) {
		t.Fatal("the relay script does not read the variable Ensure sets")
	}
}

// A relay started with another key must be replaced, so the key's id feeds the
// config hash even though the run argv (which carries only the variable's
// name) is the same.
func TestConfigHashChangesWithTheKey(t *testing.T) {
	a, b := testRelay(fakeVM{}, nil), testRelay(fakeVM{}, nil)
	b.Key = otherRelayKey
	if !slices.Equal(a.runArgs(gw), b.runArgs(gw)) {
		t.Fatal("the key changed the run argv: it must not be in it")
	}
	if a.configHash(gw) == b.configHash(gw) {
		t.Fatal("two relays with different keys share a config hash; a relay on the old key would be kept")
	}
	if a.configHash(gw) != testRelay(fakeVM{}, nil).configHash(gw) {
		t.Fatal("the config hash is not stable for one key")
	}
	if h := a.configHash(gw); strings.Contains(h, a.Key.Hex()) || len(h) != 16 {
		t.Fatalf("config hash %q is not a 16-character hash", h)
	}
}

func TestEnsurePassesTheKeyOnlyInTheEnvironment(t *testing.T) {
	d := &fakeDocker{logs: bothListening}
	r := testRelay(fakeVM{out: goodRules}, d)
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if len(d.runViaRun) != 0 {
		t.Fatal("the relay was started through Run, which carries no environment")
	}
	if len(d.find("run")) != 1 {
		t.Fatalf("run calls = %v", d.find("run"))
	}
	want := RelayKeyEnv + "=" + r.Key.Hex()
	if len(d.envs) != 1 || len(d.envs[0]) != 1 || d.envs[0][0] != want {
		t.Fatalf("RunEnv environments = %d call(s), want exactly one holding only %s (values are not printed)", len(d.envs), RelayKeyEnv)
	}
}

func TestEnsureReplacesARelayStartedWithAnotherKey(t *testing.T) {
	old := testRelay(fakeVM{}, nil)
	old.Key = otherRelayKey
	d := &fakeDocker{inspect: "true\t" + old.configHash(gw) + "\n", logs: bothListening}
	if err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect,rm,run,logs" {
		t.Fatalf("calls = %s, want the old relay removed and a new one started", got)
	}
}

// countingVM counts the firewall reads Ensure makes.
type countingVM struct{ reads int }

func (c *countingVM) FilterRules(context.Context) (string, error) {
	c.reads++
	return goodRules, nil
}

// D7: with no key Ensure removes whatever relay runs, then refuses, and it never
// reads the VM's rules or starts anything. During the upgrade the running relay
// can be one from before #118, which does not authenticate the listener, so
// leaving it up would let whatever holds the egress port act as the boxes'
// proxy. No listener binds with a zero key, so the removal cuts nothing that
// works.
func TestEnsureWithoutAKeyRemovesAnyRelay(t *testing.T) {
	vm := &countingVM{}
	d := &fakeDocker{logs: bothListening}
	r := &Relay{VM: vm, Docker: d, Image: "agent-box/box:test", Instance: "prod", EgressPort: 9072, BrokerPort: 9071,
		UID: 501, GID: 20, ReadyTimeout: 300 * time.Millisecond}
	// What runs is a keyed relay from before #118, the case D7 is about: its label
	// is the v1 hash of a keyed twin of r.
	d.inspect = "true\t" + v1RelayHash(t, testRelay(fakeVM{}, nil)) + "\n"
	err := r.Ensure(context.Background(), gw)
	want := "relay: no relay key; CuliBox could not load relay2.secret at start (its log says why)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if vm.reads != 0 {
		t.Errorf("the VM's firewall was read %d time(s) before the key check", vm.reads)
	}
	var got []string
	for _, c := range d.calls {
		got = append(got, strings.Join(c, " "))
	}
	if !slices.Equal(got, []string{"rm -f agentbox-relay-prod"}) {
		t.Errorf("docker calls = %q, want exactly the relay's removal", got)
	}
	if len(d.envs) != 0 || len(d.runViaRun) != 0 {
		t.Errorf("a relay was started: %d RunEnv call(s), %d run via Run", len(d.envs), len(d.runViaRun))
	}
}

// The removal needs a name and a docker to do it with. Without either, the zero
// key is only refused: no docker call, and never an `rm -f agentbox-relay-` for a
// relay that has no name.
func TestEnsureWithoutAKeyOrNothingToRemoveJustRefuses(t *testing.T) {
	want := "relay: no relay key; CuliBox could not load relay2.secret at start (its log says why)"
	d := &fakeDocker{}
	for name, r := range map[string]*Relay{
		"no docker":   {VM: &countingVM{}, Instance: "prod", EgressPort: 9072, BrokerPort: 9071},
		"no instance": {VM: &countingVM{}, Docker: d, EgressPort: 9072, BrokerPort: 9071},
	} {
		if err := r.Ensure(context.Background(), gw); err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	if len(d.calls) != 0 {
		t.Errorf("docker was called: %v", d.subcommands())
	}
}

// A missing image, instance or port keeps its own message: the zero-key text
// points at relay2.secret, which would send the reader to the wrong place.
func TestEnsureNamesMissingFieldsApartFromTheKey(t *testing.T) {
	r := &Relay{VM: &countingVM{}, Docker: &fakeDocker{logs: bothListening}, Instance: "prod", EgressPort: 9072, BrokerPort: 9071,
		UID: 501, GID: 20, Key: relayTestKey, ReadyTimeout: 300 * time.Millisecond}
	err := r.Ensure(context.Background(), gw)
	if err == nil || err.Error() != "relay: image, instance and both ports are required" {
		t.Fatalf("err = %v, want the missing-fields refusal", err)
	}
}

func TestRelayScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	f := filepath.Join(t.TempDir(), "relay.js")
	if err := os.WriteFile(f, []byte(relayScript), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("relay script does not parse: %v\n%s", err, out)
	}
}

func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH: the relay script's end-to-end tests did not run")
	}
	return node
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// relayScriptEnv is this process's environment without the relay key variable,
// plus the variable set to value when set is true.
func relayScriptEnv(value string, set bool) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, RelayKeyEnv+"=") {
			env = append(env, e)
		}
	}
	if set {
		env = append(env, RelayKeyEnv+"="+value)
	}
	return env
}

// lockedBuf is a bytes.Buffer a child process's output can be copied into while
// the test reads it.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startRelayScript runs script (relayScript, or an older relay) under node with
// key in its environment, in one process with one free loopback listen port per
// target port (the same order), each forwarding to 127.0.0.1:<target port>. It
// returns the listen ports once the relay has said it listens on every one. The
// key is never in argv. When the test ends the relay is killed, and it must
// have printed nothing but its "listening" lines and nothing at all on stderr:
// Ensure treats any "ERR " line as fatal, and a per-connection line or an
// uncaught exception would mean the relay does something it must not.
//
// The relay is a separate process, so its listen ports cannot be held by the
// test until it binds them: they are picked free and bound a moment later, and a
// parallel test can take one in between. The relay then says so loudly ("ERR
// <port> EADDRINUSE", and exits), and the start is repeated on fresh ports, up
// to five times. Any other output, or a start that never finishes, fails.
func startRelayScript(t *testing.T, node, script string, key proxyline.Key, tports ...int) []int {
	t.Helper()
	const attempts = 5
	for attempt := 1; ; attempt++ {
		if lports, ok := tryStartRelayScript(t, node, script, key, tports, attempt < attempts); ok {
			return lports
		}
	}
}

// tryStartRelayScript is one start of startRelayScript. It reports ok=false,
// with the process gone, only when retry is true and the relay lost the race for
// one of its listen ports.
func tryStartRelayScript(t *testing.T, node, script string, key proxyline.Key, tports []int, retry bool) (lports []int, ok bool) {
	t.Helper()
	args := []string{"-e", script, "127.0.0.1"}
	lports = make([]int, len(tports))
	want := make([]string, len(tports))
	used := map[int]bool{}
	for i, tp := range tports {
		lp := freeTCPPort(t)
		for used[lp] {
			lp = freeTCPPort(t)
		}
		used[lp] = true
		lports[i] = lp
		want[i] = fmt.Sprintf("listening 127.0.0.1:%d", lp)
		args = append(args, strconv.Itoa(lp), "127.0.0.1", strconv.Itoa(tp))
	}
	sort.Strings(want)
	cmd := exec.Command(node, args...)
	cmd.Env = relayScriptEnv(key.Hex(), true)
	var stdout, stderr lockedBuf
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := false // set once every port is listening: only then is the output judged
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if !started {
			return
		}
		if got := outputLines(stdout.String()); !slices.Equal(got, want) {
			t.Errorf("relay stdout lines = %q, want exactly %q", got, want)
		}
		if e := stderr.String(); e != "" {
			t.Errorf("relay stderr = %q, want nothing", e)
		}
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		got := outputLines(stdout.String())
		if slices.Equal(got, want) {
			started = true
			return lports, true
		}
		if retry && strings.Contains(stdout.String(), " EADDRINUSE") && stderr.String() == "" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, false
		}
		if time.Now().After(deadline) || strings.Contains(stdout.String(), "ERR ") {
			t.Fatalf("relay stdout = %q (stderr %q); want %q", stdout.String(), stderr.String(), want)
		}
	}
}

// outputLines is the non-empty lines of a relay's output, sorted.
func outputLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, l)
		}
	}
	sort.Strings(got)
	return got
}

// A relay without a well-formed key must not start: it would forward
// connections the listeners all refuse. It says why, on the line Ensure reads.
func TestRelayScriptRefusesToStartWithoutAKey(t *testing.T) {
	node := nodeOrSkip(t)
	for name, env := range map[string][]string{
		"unset":     relayScriptEnv("", false),
		"empty":     relayScriptEnv("", true),
		"malformed": relayScriptEnv("abc", true),
		"uppercase": relayScriptEnv(strings.Repeat("A", 64), true),
		"too long":  relayScriptEnv(strings.Repeat("a", 65), true),
	} {
		t.Run(name, func(t *testing.T) {
			lport, tport := freeTCPPort(t), freeTCPPort(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "-e", relayScript, "127.0.0.1", strconv.Itoa(lport), "127.0.0.1", strconv.Itoa(tport))
			cmd.Env = env
			out, err := cmd.Output()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("exit = %v, want exit code 1 (stdout %q)", err, out)
			}
			if first, _, _ := strings.Cut(string(out), "\n"); first != "ERR KEY missing or malformed" {
				t.Fatalf("first stdout line = %q, want %q", first, "ERR KEY missing or malformed")
			}
			// The process has exited, so a dial afterwards would prove nothing:
			// what shows it never listened is that it never said so.
			if strings.Contains(string(out), "listening") {
				t.Fatalf("a relay with no key reported a listener: %q", out)
			}
		})
	}
}

// v1RelayScript is the relay before #118: it writes the static key line and the
// PROXY line to the listener without waiting for anything.
const v1RelayScript = `const net=require("net");const k=process.env.AGENTBOX_RELAY_KEY||"";if(!/^[0-9a-f]{64}$/.test(k)){console.log("ERR KEY missing or malformed");process.exit(1)}const K="AGENTBOX-RELAY "+k+"\r\n";const a=process.argv.slice(1);const bind=a[0];for(let i=1;i+2<a.length;i+=3){const lp=+a[i],th=a[i+1],tp=+a[i+2];net.createServer(c=>{let s=String(c.remoteAddress||"");if(s.startsWith("::ffff:"))s=s.slice(7);if(!/^\d{1,3}(\.\d{1,3}){3}$/.test(s)||!c.remotePort){c.destroy();return}const u=net.connect(tp,th);u.write(K+"PROXY TCP4 "+s+" "+bind+" "+c.remotePort+" "+lp+"\r\n");c.pipe(u);u.pipe(c);c.on("error",()=>u.destroy());u.on("error",()=>c.destroy())}).on("error",e=>{console.log("ERR "+lp+" "+e.code);process.exit(1)}).listen(lp,bind,()=>console.log("listening "+bind+":"+lp))}`

// The v1 relay meets a v2 listener: its first 82 bytes are not a hello, so the
// listener refuses it (counted), answers nothing, and the box's client sees the
// connection close with no byte back. The v1 relay pipes the listener to the
// client, so zero bytes at the client also prove the listener wrote nothing.
func TestV1RelayIsRefusedByTheV2Listener(t *testing.T) {
	node := nodeOrSkip(t)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var refused atomic.Int32
	target := proxyline.Wrap(raw, proxyline.Options{
		Key:        relayTestKey,
		Allow:      func(netip.Addr) bool { return true },
		Timeout:    5 * time.Second,
		KeyRefused: func() { refused.Add(1) },
	})
	t.Cleanup(func() { target.Close() })
	tport := raw.Addr().(*net.TCPAddr).Port
	lport := startRelayScript(t, node, v1RelayScript, relayTestKey, tport)[0]

	read := make(chan error, 1)
	go func() {
		c, err := target.Accept()
		if err != nil {
			read <- err
			return
		}
		defer c.Close()
		_, err = c.Read(make([]byte, 16)) // runs the handshake
		read <- err
	}()

	cl, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", lport), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	_ = cl.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := cl.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-read:
		if !errors.Is(err, proxyline.ErrHeader) {
			t.Fatalf("the listener's Read = %v, want ErrHeader", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the listener never refused the v1 relay")
	}
	if n := refused.Load(); n != 1 {
		t.Errorf("refusals counted = %d, want 1", n)
	}
	got, err := io.ReadAll(cl)
	if len(got) != 0 {
		t.Errorf("the client received %d bytes from a refused relay, want none", len(got))
	}
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("client read err = %v, want EOF or a reset", err)
	}
}

// argsWithScript is r's run argv with relayScript replaced by script, and fails
// unless relayScript appears in it exactly once.
func argsWithScript(t *testing.T, r *Relay, script string) []string {
	t.Helper()
	args := r.runArgs(gw)
	n := 0
	for i, a := range args {
		if a == relayScript {
			args[i] = script
			n++
		}
	}
	if n != 1 {
		t.Fatalf("relayScript appears %d times in the run argv, want once", n)
	}
	return args
}

// v1RelayHash is the config label a relay started before #118 carries: r's own
// argv and key id with the v1 script in place of the current one.
func v1RelayHash(t *testing.T, r *Relay) string {
	t.Helper()
	return hashWithKeyID(r, argsWithScript(t, r, v1RelayScript))
}

// A relay started before #118 differs from the current one only in its script,
// so the next Ensure must replace it: it does not authenticate the listener and
// the v2 listeners refuse it. The key stays the same here on purpose, so the
// script is the one thing that decides.
func TestEnsureReplacesAV1Relay(t *testing.T) {
	r := testRelay(fakeVM{out: goodRules}, nil)
	d := &fakeDocker{inspect: "true\t" + v1RelayHash(t, r) + "\n", logs: bothListening}
	r.Docker = d
	if err := r.Ensure(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.subcommands(), ","); got != "inspect,rm,run,logs" {
		t.Fatalf("calls = %s, want the v1 relay removed and a new one started", got)
	}
}

// targetSeen is what a wrapped target saw on one accepted connection.
type targetSeen struct {
	remote  string // RemoteAddr, which runs the handshake
	payload string
	err     error
}

// wrappedTarget serves raw behind proxyline.Wrap with key, and returns raw's
// port, one targetSeen per accepted connection and the number of refusals the
// listener counted. Each connection calls RemoteAddr, reads n.Load() bytes
// (the test sets it before it dials), answers "pong" and reports. The listener
// is closed when the test ends. The result is not named seen: a result of that
// name would hide it in the body.
func wrappedTarget(t *testing.T, key proxyline.Key, raw net.Listener, n *atomic.Int32) (port int, seen <-chan targetSeen, refused *atomic.Int32) {
	t.Helper()
	refused = new(atomic.Int32)
	ln := proxyline.Wrap(raw, proxyline.Options{
		Key:        key,
		Allow:      func(netip.Addr) bool { return true },
		Timeout:    5 * time.Second,
		KeyRefused: func() { refused.Add(1) },
	})
	t.Cleanup(func() { ln.Close() })
	conns := make(chan targetSeen, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				s := targetSeen{remote: c.RemoteAddr().String()} // runs the handshake
				buf := make([]byte, n.Load())
				if _, err := io.ReadFull(c, buf); err != nil {
					s.err = err
					conns <- s
					return
				}
				s.payload = string(buf)
				_, s.err = c.Write([]byte("pong"))
				conns <- s
			}()
		}
	}()
	return raw.Addr().(*net.TCPAddr).Port, conns, refused
}

// listenerPort is the port a loopback listener holds.
func listenerPort(l net.Listener) int { return l.Addr().(*net.TCPAddr).Port }

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// slowWriteListener hands out connections whose Write leaves one byte at a
// time, 2 ms apart, so the relay sees the listener's proof in as many reads as
// it has bytes.
type slowWriteListener struct{ net.Listener }

func (l slowWriteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return slowWriteConn{c}, nil
}

type slowWriteConn struct{ net.Conn }

func (c slowWriteConn) Write(p []byte) (int, error) {
	for i := range p {
		if i > 0 {
			time.Sleep(2 * time.Millisecond)
		}
		if _, err := c.Conn.Write(p[i : i+1]); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

// chunkConn is the conn an impostor's behaviour reads and writes: writes go to
// the real conn, reads come from the chunks the recorder took off it, so the
// recorder alone reads the socket and sees every byte.
type chunkConn struct {
	net.Conn
	in   <-chan []byte
	rest []byte
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if len(c.rest) == 0 {
		b, ok := <-c.in
		if !ok {
			return 0, io.EOF
		}
		c.rest = b
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

// impostorOn serves l, a loopback listener the test already holds where a
// relay's target would be (its port is passed to the relay), and for every
// connection records each byte received until EOF, an error or 10 s, while
// behave runs on the connection. behave returns when it is done; the connection
// stays open until the peer closes it or the 10 s end, so a silent behave holds
// the relay's connection like a peer that never answers. The channel gets one
// recording per connection, once the connection has ended. l is closed when the
// test ends. It is a listener, not a port number: a port closed and bound again
// can be taken by a parallel test in between.
func impostorOn(t *testing.T, l net.Listener, behave func(net.Conn)) <-chan []byte {
	t.Helper()
	t.Cleanup(func() { l.Close() })
	recs := make(chan []byte, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
				in := make(chan []byte, 256)
				done := make(chan struct{})
				go func() { defer close(done); behave(&chunkConn{Conn: c, in: in}) }()
				var got []byte
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						got = append(got, buf[:n]...)
						select {
						case in <- append([]byte(nil), buf[:n]...):
						default:
						}
					}
					if err != nil {
						break
					}
				}
				close(in)
				c.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
				recs <- got
			}()
		}
	}()
	return recs
}

// boxCall is a box's client: it connects to the relay's listen port, sends
// payload and reads until the connection ends (or 15 s), returning what came
// back and how long that took. A reset ends the read like an EOF does; the
// caller judges the bytes and the time.
func boxCall(t *testing.T, lport int, payload string) (reply []byte, elapsed time.Duration) {
	t.Helper()
	reply, _, elapsed = boxCallFrom(t, lport, payload)
	return reply, elapsed
}

// boxCallFrom is boxCall that also returns the client's own address.
func boxCallFrom(t *testing.T, lport int, payload string) (reply []byte, local string, elapsed time.Duration) {
	t.Helper()
	start := time.Now()
	cl, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", lport), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	_ = cl.SetDeadline(time.Now().Add(15 * time.Second))
	// A write error is not the test's to judge: a relay that has already shut
	// the connection is what an impostor case expects.
	_, _ = cl.Write([]byte(payload))
	reply, _ = io.ReadAll(cl)
	return reply, cl.LocalAddr().String(), time.Since(start)
}

func nextSeen(t *testing.T, ch <-chan targetSeen) targetSeen {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(15 * time.Second):
		t.Fatal("the target saw no connection")
		return targetSeen{}
	}
}

func nextRecording(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(15 * time.Second):
		t.Fatal("the impostor's connection never ended")
		return nil
	}
}

// helloOnly is all a peer that cannot prove the key may ever receive from the
// relay: the hello, exactly, with no PROXY line and no box byte after it.
var helloOnly = regexp.MustCompile(`^AGENTBOX-RELAY2 [0-9a-f]{64}\r\n$`)

// The script and the Go listener agree on the wire: the same hello, proofs and
// labels, with the listener's proof arriving in one segment or byte by byte,
// and the PROXY line the listener believes names the box that connected. Bytes
// a client sends that look like either key line and a PROXY header stay payload.
func TestRelayScriptHandshakesWithProxyline(t *testing.T) {
	node := nodeOrSkip(t)
	for _, tc := range []struct {
		name string
		wrap func(net.Listener) net.Listener
	}{
		{"plain", func(l net.Listener) net.Listener { return l }},
		{"one-byte segments", func(l net.Listener) net.Listener { return slowWriteListener{l} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			brokerPort, brokerSeen, brokerRefused := wrappedTarget(t, relayTestKey, tc.wrap(listenLoopback(t)), &n)
			egressPort, egressSeen, egressRefused := wrappedTarget(t, relayTestKey, tc.wrap(listenLoopback(t)), &n)
			ports := startRelayScript(t, node, relayScript, relayTestKey, brokerPort, egressPort)

			n.Store(4)
			for i, seen := range []<-chan targetSeen{brokerSeen, egressSeen} {
				reply, local, _ := boxCallFrom(t, ports[i], "ping")
				if string(reply) != "pong" {
					t.Fatalf("port %d: client reply = %q, want pong", i, reply)
				}
				if s := nextSeen(t, seen); s.err != nil || s.payload != "ping" || s.remote != local {
					t.Errorf("port %d: target saw payload %q from %s, err %v; want ping from the client's %s", i, s.payload, s.remote, s.err, local)
				}
			}

			// A client's own key lines and header are only its payload, and the
			// address the target sees is still the client's.
			forged := "AGENTBOX-RELAY " + otherRelayKey.Hex() + "\r\n" +
				"AGENTBOX-RELAY2 " + otherRelayKey.Hex() + "\r\n" +
				"PROXY TCP4 172.29.72.9 172.29.72.1 40000 9072\r\n"
			n.Store(int32(len(forged)))
			reply, local, _ := boxCallFrom(t, ports[0], forged)
			if string(reply) != "pong" {
				t.Fatalf("forged payload: client reply = %q, want pong", reply)
			}
			s := nextSeen(t, brokerSeen)
			if s.err != nil {
				t.Fatalf("forged payload: target side: %v", s.err)
			}
			if s.remote != local {
				t.Errorf("RemoteAddr = %s, want the client's %s: a forged header was believed", s.remote, local)
			}
			if s.payload != forged {
				t.Error("the forged bytes did not arrive as the payload, unchanged (bytes not printed)")
			}
			if a, b := brokerRefused.Load(), egressRefused.Load(); a != 0 || b != 0 {
				t.Errorf("refusals counted = %d and %d, want none for a relay with the right key", a, b)
			}
		})
	}
}

// The impostor cases hold the port where the listener should be. Whatever it
// answers, it receives the hello and nothing else, the box gets nothing, and
// the same relay serves the next good connection on its other port.
func TestRelayScriptSendsNothingToAnImpostor(t *testing.T) {
	node := nodeOrSkip(t)
	const sentinelCall = "ping Authorization: Bearer SENTINEL\r\n"
	// A well-formed-looking proof line: the right shape, no key behind it.
	shaped := strings.Repeat("a", 64) + " " + strings.Repeat("b", 64) + "\r\n"

	// Every case holds its listener from the moment it is bound until the test
	// ends, so no port is ever released and bound again.
	type setupFn func(t *testing.T) (l net.Listener, behave func(net.Conn), after func(t *testing.T))
	simple := func(behave func(net.Conn)) setupFn {
		return func(t *testing.T) (net.Listener, func(net.Conn), func(*testing.T)) {
			return listenLoopback(t), behave, func(*testing.T) {}
		}
	}
	cases := []struct {
		name  string
		slow  bool // the relay's 5 s limit ends it
		setup setupFn
	}{
		{"silent", true, simple(func(net.Conn) {})},
		{"garbage", false, simple(func(c net.Conn) { _, _ = c.Write([]byte(strings.Repeat("x", proxyline.ListenerProofLen))) })},
		{"too long", false, simple(func(c net.Conn) { _, _ = c.Write([]byte(shaped + "x")) })},
		{"short then silent", true, simple(func(c net.Conn) { _, _ = c.Write([]byte(strings.Repeat("x", 100))) })},
		// A listener's proof captured from an earlier handshake answers every
		// hello: the relay's nonce is fresh, so it cannot match.
		//
		// Phase 1 is a wrapped target on the impostor's own port that accepts one
		// connection, runs the handshake against it and keeps the listener's 131
		// bytes. It then simply stops accepting: the listener is never closed and
		// bound again, it is handed to the impostor, so the port is held from the
		// first bind to the last use.
		{"replay", false, func(t *testing.T) (net.Listener, func(net.Conn), func(*testing.T)) {
			l := listenLoopback(t)
			target := proxyline.Wrap(l, proxyline.Options{
				Key:     relayTestKey,
				Allow:   func(netip.Addr) bool { return true },
				Timeout: 5 * time.Second,
			})
			go func() {
				c, err := target.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.RemoteAddr() // runs the handshake
			}()
			cl, err := net.DialTimeout("tcp", l.Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_ = cl.SetDeadline(time.Now().Add(10 * time.Second))
			var tee bytes.Buffer
			err = proxyline.RelayHandshake(struct {
				io.Reader
				io.Writer
			}{io.TeeReader(cl, &tee), cl}, relayTestKey, uint16(listenerPort(l)))
			cl.Close()
			if err != nil || tee.Len() != proxyline.ListenerProofLen {
				t.Fatalf("phase 1: handshake = %v, captured %d bytes, want %d", err, tee.Len(), proxyline.ListenerProofLen)
			}
			captured := tee.Bytes()
			return l, func(c net.Conn) {
				if _, err := io.ReadFull(c, make([]byte, proxyline.HelloLen)); err == nil {
					_, _ = c.Write(captured)
				}
			}, func(*testing.T) {}
		}},
		// A real listener on the same key, on another port, answers the hello
		// the relay sent: its proof is for its own port, so the relay refuses it.
		{"reflect", false, func(t *testing.T) (net.Listener, func(net.Conn), func(*testing.T)) {
			var n atomic.Int32
			n.Store(4)
			realPort, realSeen, realRefused := wrappedTarget(t, relayTestKey, listenLoopback(t), &n)
			behave := func(c net.Conn) {
				up, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", realPort), 5*time.Second)
				if err != nil {
					return
				}
				defer up.Close()
				hello := make([]byte, proxyline.HelloLen)
				if _, err := io.ReadFull(c, hello); err != nil {
					return
				}
				if _, err := up.Write(hello); err != nil {
					return
				}
				proof := make([]byte, proxyline.ListenerProofLen)
				if _, err := io.ReadFull(up, proof); err != nil {
					return
				}
				_, _ = c.Write(proof)
				_, _ = io.Copy(up, c) // anything more, until the relay gives up
			}
			return listenLoopback(t), behave, func(t *testing.T) {
				if s := nextSeen(t, realSeen); !errors.Is(s.err, proxyline.ErrHeader) {
					t.Errorf("the real listener's conn ended with %v, want ErrHeader", s.err)
				}
				if got := realRefused.Load(); got != 1 {
					t.Errorf("the real listener counted %d refusals, want 1", got)
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, behave, after := tc.setup(t)
			ip := listenerPort(l)
			recs := impostorOn(t, l, behave)
			var n atomic.Int32
			gp, goodSeen, goodRefused := wrappedTarget(t, relayTestKey, listenLoopback(t), &n)
			ports := startRelayScript(t, node, relayScript, relayTestKey, ip, gp)

			reply, elapsed := boxCall(t, ports[0], sentinelCall)
			rec := nextRecording(t, recs)
			if !helloOnly.Match(rec) {
				t.Errorf("the impostor received %q, want the hello and nothing else", rec)
			}
			if len(reply) != 0 {
				t.Errorf("the box received %q, want nothing", reply)
			}
			if tc.slow {
				if elapsed < 4*time.Second || elapsed > 8*time.Second {
					t.Errorf("the box call ended after %v, want the relay's 5 s limit", elapsed)
				}
			} else if elapsed > 2*time.Second {
				t.Errorf("the box call ended after %v, want an immediate refusal", elapsed)
			}
			after(t)

			// The relay survived: its other port still serves.
			n.Store(4)
			reply, _ = boxCall(t, ports[1], "ping")
			if string(reply) != "pong" {
				t.Fatalf("after the impostor, a good call got %q, want pong", reply)
			}
			if s := nextSeen(t, goodSeen); s.err != nil || s.payload != "ping" {
				t.Errorf("good target saw payload %q, err %v", s.payload, s.err)
			}
			if got := goodRefused.Load(); got != 0 {
				t.Errorf("the good listener counted %d refusals, want 0", got)
			}
		})
	}
}

// Each connection gets its own nonce: three calls to a peer that answers
// nothing usable record three different hellos.
func TestRelayScriptNoncesAreFresh(t *testing.T) {
	node := nodeOrSkip(t)
	l := listenLoopback(t)
	ip := listenerPort(l)
	recs := impostorOn(t, l, func(c net.Conn) {
		_, _ = c.Write([]byte(strings.Repeat("x", proxyline.ListenerProofLen)))
	})
	lport := startRelayScript(t, node, relayScript, relayTestKey, ip)[0]
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		boxCall(t, lport, "ping")
		rec := nextRecording(t, recs)
		if !helloOnly.Match(rec) {
			t.Fatalf("call %d: the impostor received %q, want the hello and nothing else", i, rec)
		}
		seen[string(rec)] = true
	}
	if len(seen) != 3 {
		t.Errorf("three connections sent %d distinct hellos, want 3", len(seen))
	}
}

// A listener on another key cannot satisfy the relay, whichever side holds
// which key: the box gets nothing, and the listener counts the refusal.
func TestRelayScriptRefusesAListenerOnAnotherKey(t *testing.T) {
	node := nodeOrSkip(t)
	for _, tc := range []struct {
		name               string
		relayKey, listener proxyline.Key
	}{
		{"relay on the key, listener on another", relayTestKey, otherRelayKey},
		{"relay on another key, listener on the key", otherRelayKey, relayTestKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			n.Store(4)
			tport, seen, refused := wrappedTarget(t, tc.listener, listenLoopback(t), &n)
			lport := startRelayScript(t, node, relayScript, tc.relayKey, tport)[0]
			reply, elapsed := boxCall(t, lport, "ping")
			if len(reply) != 0 {
				t.Errorf("the box received %q, want nothing", reply)
			}
			if elapsed > 2*time.Second {
				t.Errorf("the box call ended after %v, want an immediate refusal", elapsed)
			}
			if s := nextSeen(t, seen); !errors.Is(s.err, proxyline.ErrHeader) {
				t.Errorf("the listener's conn ended with %v, want ErrHeader", s.err)
			}
			if got := refused.Load(); got != 1 {
				t.Errorf("the listener counted %d refusals, want 1", got)
			}
		})
	}
}

// preProxyLineRelayScript is the relay script before #71 (no PROXY line).
const preProxyLineRelayScript = `const net=require("net");const a=process.argv.slice(1);const bind=a[0];for(let i=1;i+2<a.length;i+=3){const lp=+a[i],th=a[i+1],tp=+a[i+2];net.createServer(c=>{const u=net.connect(tp,th);c.pipe(u);u.pipe(c);c.on("error",()=>u.destroy());u.on("error",()=>c.destroy())}).on("error",e=>{console.log("ERR "+lp+" "+e.code);process.exit(1)}).listen(lp,bind,()=>console.log("listening "+bind+":"+lp))}`

// A relay started before the PROXY line existed carries the old config hash, so
// the next Ready replaces it (the host listeners now refuse its header-less
// connections). In-flight tunnels drop once when that happens.
func TestDarwinRelayReplacedWhenScriptChanges(t *testing.T) {
	r := testRelay(fakeVM{}, nil)
	old := argsWithScript(t, r, preProxyLineRelayScript)
	if hashWithKeyID(r, old) == r.configHash(gw) {
		t.Fatal("the pre-#71 relay has the same config hash as the current one; it would not be replaced")
	}
}
