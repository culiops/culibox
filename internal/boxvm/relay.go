package boxvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// relayScript is the whole relay: one TCP listener per (listen port, target
// host, target port) triple on the bind address, piped straight through. It
// prints "listening BIND:PORT" per port, and "ERR PORT CODE" then exits 1 on a
// listen error, which is all Ensure reads (never a dial: the INPUT rules also
// drop the VM's own connections to the gateway). Run as
// `node -e <script> BIND LPORT THOST TPORT ...`, where argv.slice(1) is the
// arguments after the script (measured on the Mac). Verbatim from the spike.
const relayScript = `const net=require("net");const a=process.argv.slice(1);const bind=a[0];for(let i=1;i+2<a.length;i+=3){const lp=+a[i],th=a[i+1],tp=+a[i+2];net.createServer(c=>{const u=net.connect(tp,th);c.pipe(u);u.pipe(c);c.on("error",()=>u.destroy());u.on("error",()=>c.destroy())}).on("error",e=>{console.log("ERR "+lp+" "+e.code);process.exit(1)}).listen(lp,bind,()=>console.log("listening "+bind+":"+lp))}`

const (
	relayLabel       = "io.culiops.agent-box.relay"
	relayConfigLabel = "io.culiops.agent-box.relay-config"

	relayDockerTimeout = 30 * time.Second
	relayPollEvery     = 100 * time.Millisecond
	relayReadyDefault  = 10 * time.Second
)

// RelayName is the per-instance relay container name; every byte outside
// [A-Za-z0-9_.-] becomes '-' (the same rule as the egress network's name). The
// relay carries no instance label, so box reconcile never reaps it.
func RelayName(instance string) string {
	b := []byte(instance)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			b[i] = '-'
		}
	}
	return "agentbox-relay-" + string(b)
}

// errRulesMissing is the one message for a VM whose INPUT chain is not the one
// provisioning writes. Option (a) VMs and VMs provisioned before the egress
// rules existed end up here.
func errRulesMissing(why string) error {
	return fmt.Errorf("the box VM's egress INPUT rules are missing or out of order (%s); run `agent-box box setup -recreate`", why)
}

// CheckInputRules verifies that the first four rules of `iptables -S INPUT`
// are exactly the ones provisioning writes, in order: the egress and broker
// ports for the egress subnet to the gateway, then the subnet to anywhere
// DROP, then anyone to the gateway DROP. Exact, not "present": a foreign rule
// above ours (an ACCEPT) or an extra match on one of ours changes what they do.
func CheckInputRules(out string, egressPort, brokerPort int) error {
	want := []string{
		fmt.Sprintf("-s %s -d %s -p tcp --dport %d -j ACCEPT", EgressSubnet, EgressGateway, egressPort),
		fmt.Sprintf("-s %s -d %s -p tcp --dport %d -j ACCEPT", EgressSubnet, EgressGateway, brokerPort),
		fmt.Sprintf("-s %s -j DROP", EgressSubnet),
		fmt.Sprintf("-d %s -j DROP", EgressGateway),
	}
	var got []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "-A" || f[1] != "INPUT" {
			continue
		}
		got = append(got, normaliseRule(f[2:]))
	}
	for i, w := range want {
		if i >= len(got) {
			return errRulesMissing(fmt.Sprintf("rule %d of %d is absent", i+1, len(want)))
		}
		if got[i] != w {
			return errRulesMissing(fmt.Sprintf("INPUT rule %d is %q, want %q", i+1, got[i], w))
		}
	}
	return nil
}

// normaliseRule joins a rule's fields after dropping what iptables adds on
// output: "-m tcp" (implied by --dport) and the /32 on a single address.
func normaliseRule(f []string) string {
	var out []string
	for i := 0; i < len(f); i++ {
		switch {
		case f[i] == "-m" && i+1 < len(f) && f[i+1] == "tcp":
			i++
		case (f[i] == "-s" || f[i] == "-d") && i+1 < len(f):
			out = append(out, f[i], strings.TrimSuffix(f[i+1], "/32"))
			i++
		default:
			out = append(out, f[i])
		}
	}
	return strings.Join(out, " ")
}

// Docker is the docker CLI as the relay needs it: args after "docker".
// box.CLIClient satisfies it. Stdout returns stdout alone: a CLI or context
// warning on stderr must not be read as the answer.
type Docker interface {
	Run(ctx context.Context, args []string) (string, error)
	Stdout(ctx context.Context, args []string) (string, error)
}

// Relay is the container on the VM's host network that listens on the egress
// network's gateway and forwards exactly two ports (egress proxy, credential
// broker) to lima's host gateway, which lima NATs to the Mac's loopback. Boxes
// on the internal network reach nothing else.
type Relay struct {
	VM interface {
		InputRules(context.Context) (string, error)
	}
	Docker          Docker
	Image, Instance string
	EgressPort      int
	BrokerPort      int
	UID, GID        int
	ReadyTimeout    time.Duration // default 10s
}

// runArgs is the relay's whole `docker run` argv; extra flags go in after the
// fixed ones, before the image. Without extras it is what configHash hashes;
// Ensure adds the config label that carries that hash.
func (r *Relay) runArgs(gw netip.Addr, extra ...string) []string {
	args := []string{"run", "-d", "--init", "--name", RelayName(r.Instance), "--network", "host", "--entrypoint", "node",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only", "--pids-limit", "64", "--pull=never",
		"--user", fmt.Sprintf("%d:%d", r.UID, r.GID),
		"--label", relayLabel + "=" + r.Instance}
	args = append(args, extra...)
	args = append(args, r.Image, "-e", relayScript, gw.String())
	for _, p := range []int{r.EgressPort, r.BrokerPort} {
		args = append(args, strconv.Itoa(p), LimaHostGateway, strconv.Itoa(p))
	}
	return args
}

// hashArgs is a short hash of an argv, each element length-prefixed so no two
// argvs share an encoding.
func hashArgs(args []string) string {
	h := sha256.New()
	for _, s := range args {
		fmt.Fprintf(h, "%d:%s\n", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// configHash identifies everything the running relay was built from: a relay
// whose label differs (any run flag, image, script, port or user) is replaced.
func (r *Relay) configHash(gw netip.Addr) string {
	return hashArgs(r.runArgs(gw))
}

func (r *Relay) docker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, relayDockerTimeout)
	defer cancel()
	return r.Docker.Run(ctx, args)
}

func (r *Relay) dockerStdout(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, relayDockerTimeout)
	defer cancel()
	return r.Docker.Stdout(ctx, args)
}

// removeAlso is the failure-path removal: it returns err, joined with the
// removal's own error when that failed, so the operator learns a relay may
// still be up.
func (r *Relay) removeAlso(ctx context.Context, err error) error {
	if rmErr := r.remove(ctx); rmErr != nil {
		return errors.Join(err, fmt.Errorf("removing the relay: %w", rmErr))
	}
	return err
}

// remove is `rm -f` that tolerates an absent container. It runs on a context
// that outlives the caller's cancellation: a relay left behind by a cancelled
// Ensure is exactly what must not survive.
func (r *Relay) remove(ctx context.Context) error {
	_, err := r.docker(context.WithoutCancel(ctx), "rm", "-f", RelayName(r.Instance))
	if err != nil && !strings.Contains(err.Error(), "No such") {
		return err
	}
	return nil
}

// Ensure makes the relay run for gateway gw, or returns an error and leaves no
// relay behind. The INPUT rules are checked live first, every time: they are
// what keeps an unbound box off the relay, so a VM without them gets no relay.
//
// Rules read and wrong: the relay is removed. Rules that could not be read
// (colima ssh error, timeout): this Ready is refused but a running relay is
// left alone. The relay only ever started after the rules were verified this
// boot; a VM reboot or dockerd restart stops it (no restart policy); and a
// transient ssh failure must not cut every live bound session's proxy and
// broker.
func (r *Relay) Ensure(ctx context.Context, gw netip.Addr) error {
	if r.Image == "" || r.Instance == "" || r.EgressPort <= 0 || r.BrokerPort <= 0 {
		return fmt.Errorf("relay: image, instance and both ports are required")
	}
	if gw.String() != EgressGateway {
		return fmt.Errorf("relay: network gateway %s is not the provisioned %s", gw, EgressGateway)
	}
	rules, err := r.VM.InputRules(ctx)
	if err != nil {
		return fmt.Errorf("the box VM's firewall rules could not be read (%w); refusing to start the relay on them", err)
	}
	if err := CheckInputRules(rules, r.EgressPort, r.BrokerPort); err != nil {
		// Read and wrong: nothing may keep relaying without the rules.
		return r.removeAlso(ctx, err)
	}

	name, hash := RelayName(r.Instance), r.configHash(gw)
	out, err := r.dockerStdout(ctx, "inspect", "--type", "container", "--format",
		`{{.State.Running}}`+"\t"+`{{index .Config.Labels "`+relayConfigLabel+`"}}`, name)
	switch {
	case err == nil:
		if strings.TrimSpace(out) == "true\t"+hash {
			return nil
		}
		if err := r.remove(ctx); err != nil {
			return fmt.Errorf("removing the old relay: %w", err)
		}
	case strings.Contains(err.Error(), "No such"):
	default:
		return fmt.Errorf("inspecting the relay: %w", err)
	}

	if _, err := r.docker(ctx, r.runArgs(gw, "--label", relayConfigLabel+"="+hash)...); err != nil {
		return r.removeAlso(ctx, fmt.Errorf("starting the relay: %w", err))
	}
	if err := r.waitListening(ctx, name, gw.String()); err != nil {
		return r.removeAlso(ctx, err)
	}
	return nil
}

// waitListening reads the relay's own log until it reports both listeners on
// the gateway address. It never dials: the INPUT rules drop that too. While
// not ready it also asks whether the relay still runs, so one that exited
// without an ERR line (say, an image without node) fails at once instead of
// waiting out the timeout.
func (r *Relay) waitListening(ctx context.Context, name, bind string) error {
	want := map[string]bool{
		"listening " + bind + ":" + strconv.Itoa(r.EgressPort): false,
		"listening " + bind + ":" + strconv.Itoa(r.BrokerPort): false,
	}
	timeout := r.ReadyTimeout
	if timeout <= 0 {
		timeout = relayReadyDefault
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(relayPollEvery)
	defer tick.Stop()
	var logs string
	for {
		var err error
		logs, err = r.docker(ctx, "logs", name)
		if err != nil && ctx.Err() == nil {
			return fmt.Errorf("reading the relay's log: %w", err)
		}
		ready := 0
		for _, line := range strings.Split(logs, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "ERR "):
				return fmt.Errorf("the relay could not listen: %s", line)
			case strings.HasPrefix(line, "listening "):
				if _, ok := want[line]; !ok {
					return fmt.Errorf("the relay listens somewhere unexpected (%s); expected %s only", line, bind)
				}
				want[line] = true
			}
		}
		for _, seen := range want {
			if seen {
				ready++
			}
		}
		if ready == len(want) {
			return nil
		}
		running, err := r.dockerStdout(ctx, "inspect", "--type", "container", "--format", "{{.State.Running}}", name)
		switch {
		case err != nil && ctx.Err() == nil:
			return fmt.Errorf("checking that the relay runs: %w", err)
		case err == nil && strings.TrimSpace(running) != "true":
			// The log is complete once the relay has exited: read it again for
			// whatever it printed after the read above.
			if final, lerr := r.docker(ctx, "logs", name); lerr == nil {
				logs = final
			}
			return fmt.Errorf("the relay exited before reporting both listeners; log: %q", strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the relay: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("the relay did not report both listeners within %s; log: %q", timeout, strings.TrimSpace(logs))
		case <-tick.C:
		}
	}
}
