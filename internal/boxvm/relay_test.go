package boxvm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
}

func (f *fakeDocker) Run(ctx context.Context, args []string) (string, error) {
	if args[0] == "rm" {
		f.mu.Lock()
		f.rmCtxErr = append(f.rmCtxErr, ctx.Err())
		f.mu.Unlock()
	}
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

func testRelay(vm fakeVM, d *fakeDocker) *Relay {
	return &Relay{VM: vm, Docker: d, Image: "agent-box/box:test", Instance: "prod", EgressPort: 9072, BrokerPort: 9071,
		UID: 501, GID: 20, ReadyTimeout: 300 * time.Millisecond}
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
		" --pull=never ", " --user 501:20 ",
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
	h := hashArgs(args)
	if r.configHash(gw) != h {
		t.Fatal("configHash is not the hash of runArgs")
	}
	for i := range args {
		mod := append([]string(nil), args...)
		mod[i] += "x"
		if hashArgs(mod) == h {
			t.Errorf("changing arg %d (%q) leaves the hash unchanged", i, args[i])
		}
		if hashArgs(append(append([]string(nil), args[:i]...), args[i+1:]...)) == h {
			t.Errorf("dropping arg %d (%q) leaves the hash unchanged", i, args[i])
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, relayConfigLabel) {
			t.Fatalf("runArgs carries the hash label: %q", a)
		}
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

func TestRelayScriptSendsProxyHeader(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	tport := target.Addr().(*net.TCPAddr).Port

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lport := free.Addr().(*net.TCPAddr).Port
	free.Close()

	cmd := exec.Command(node, "-e", relayScript, "127.0.0.1", strconv.Itoa(lport), "127.0.0.1", strconv.Itoa(tport))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	want := fmt.Sprintf("listening 127.0.0.1:%d", lport)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != want {
		t.Fatalf("relay stdout = %q, %v; want %q", line, err, want)
	}

	type result struct {
		src, dst netip.AddrPort
		body     string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		c, err := target.Accept()
		if err != nil {
			done <- result{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		br := bufio.NewReader(c)
		hdr, err := br.ReadBytes('\n')
		if err != nil {
			done <- result{err: err}
			return
		}
		src, dst, err := proxyline.ParseV1(hdr)
		if err != nil {
			done <- result{err: err}
			return
		}
		body := make([]byte, 4)
		if _, err := io.ReadFull(br, body); err != nil {
			done <- result{err: err}
			return
		}
		_, err = c.Write([]byte("pong"))
		done <- result{src: src, dst: dst, body: string(body), err: err}
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
	reply := make([]byte, 4)
	if _, err := io.ReadFull(cl, reply); err != nil || string(reply) != "pong" {
		t.Fatalf("client reply = %q, %v; want pong", reply, err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("target side: %v", r.err)
	}
	if r.src.Addr() != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("header src = %v, want 127.0.0.1", r.src)
	}
	if want := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", lport)); r.dst != want {
		t.Errorf("header dst = %v, want %v", r.dst, want)
	}
	if want := cl.LocalAddr().(*net.TCPAddr).Port; int(r.src.Port()) != want {
		t.Errorf("header src port = %d, want the client's %d", r.src.Port(), want)
	}
	if r.body != "ping" {
		t.Errorf("payload after header = %q, want ping", r.body)
	}
}

// preProxyLineRelayScript is the relay script before #71 (no PROXY line).
const preProxyLineRelayScript = `const net=require("net");const a=process.argv.slice(1);const bind=a[0];for(let i=1;i+2<a.length;i+=3){const lp=+a[i],th=a[i+1],tp=+a[i+2];net.createServer(c=>{const u=net.connect(tp,th);c.pipe(u);u.pipe(c);c.on("error",()=>u.destroy());u.on("error",()=>c.destroy())}).on("error",e=>{console.log("ERR "+lp+" "+e.code);process.exit(1)}).listen(lp,bind,()=>console.log("listening "+bind+":"+lp))}`

// A relay started before the PROXY line existed carries the old config hash, so
// the next Ready replaces it (the host listeners now refuse its header-less
// connections). In-flight tunnels drop once when that happens.
func TestDarwinRelayReplacedWhenScriptChanges(t *testing.T) {
	r := testRelay(fakeVM{}, nil)
	old := r.runArgs(gw)
	n := 0
	for i, a := range old {
		if a == relayScript {
			old[i] = preProxyLineRelayScript
			n++
		}
	}
	if n != 1 {
		t.Fatalf("relayScript appears %d times in the run argv", n)
	}
	if hashArgs(old) == r.configHash(gw) {
		t.Fatal("the pre-#71 relay has the same config hash as the current one; it would not be replaced")
	}
}
