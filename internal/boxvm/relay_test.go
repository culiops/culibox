package boxvm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const goodRules = `-P INPUT ACCEPT
-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9072 -j ACCEPT
-A INPUT -s 172.29.72.0/24 -d 172.29.72.1/32 -p tcp -m tcp --dport 9071 -j ACCEPT
-A INPUT -s 172.29.72.0/24 -j DROP
-A INPUT -d 172.29.72.1/32 -j DROP
-A INPUT -i lo -j ACCEPT
`

const optionARules = `-P INPUT ACCEPT
-A INPUT -s 172.29.72.0/24 -j DROP
`

type fakeVM struct {
	out string
	err error
}

func (f fakeVM) InputRules(context.Context) (string, error) { return f.out, f.err }

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
	// warn simulates a docker CLI warning on stderr: Run (combined output)
	// prefixes it to every answer, Stdout never sees it.
	warn string
}

func (f *fakeDocker) Run(ctx context.Context, args []string) (string, error) {
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

func (f *fakeDocker) subcommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, c := range f.calls {
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
	start := time.Now()
	err := testRelay(fakeVM{out: goodRules}, d).Ensure(context.Background(), gw)
	if err == nil {
		t.Fatal("accepted a wildcard listener")
	}
	if time.Since(start) > 250*time.Millisecond {
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
	for name, mod := range map[string]func(*Relay){
		"image":  func(r *Relay) { r.Image = "other" },
		"egress": func(r *Relay) { r.EgressPort = 1 },
		"broker": func(r *Relay) { r.BrokerPort = 1 },
	} {
		r := testRelay(fakeVM{}, nil)
		mod(r)
		if r.configHash(gw) == h {
			t.Errorf("%s change leaves the hash unchanged", name)
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
