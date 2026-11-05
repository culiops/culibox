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

	"github.com/culiops/culibox/internal/proxyline"
)

// relayScript is the whole relay: one TCP listener per (listen port, target
// host, target port) triple on the bind address, piped through once the host
// listener has proven it holds the relay key. Per connection it dials the
// target, writes the hello ("AGENTBOX-RELAY2 <64 hex nonce>\r\n", 82 bytes),
// reads exactly one proof line (131 bytes) and checks its MAC under the key for
// the target port, and only then writes its own proof, the PROXY v1 line
// ("PROXY TCP4 <box-ip> <bind> <box-port> <listen-port>\r\n") naming the box
// that connected, in one write, and pipes the box through. The wire format and
// both MACs are proxyline/handshake.go's (its RelayHandshake is this half in Go,
// and the end-to-end tests run the two against each other). The key never
// crosses the wire, so a process that holds the target port while CuliBox does
// not learns nothing it can reuse, and the host listeners tell boxes apart (#114)
// while the relay makes every box arrive from loopback (#71).
//
// The box socket is created paused (pauseOnConnect), so its bytes stay in the
// kernel until the proof verifies: before that the target receives the hello and
// nothing else, no PROXY line and no box byte. A proof that is wrong, longer than
// 131 bytes or malformed destroys both sockets, as does an upstream that closes
// or errors first and a 5 s timer from accept that the proof has not stopped
// (a late proof is a wrong one). Hostile upstream bytes never throw: the regexp
// fixes both fields to 64 hex characters before they are decoded and compared
// with timingSafeEqual. Nothing is logged per connection: Ensure reads this
// process's stdout and treats any "ERR " line as fatal. The key comes from the
// environment (RelayKeyEnv), never argv; without a well-formed one the script
// prints "ERR KEY missing or malformed" and exits 1 before it listens. A peer
// that is not a plain IPv4 address is dropped. It prints "listening BIND:PORT"
// per port, and "ERR PORT CODE" then exits 1 on a listen error, which is all
// Ensure reads (never a dial: the INPUT rules also drop the VM's own connections
// to the gateway). Run as `node -e <script> BIND LPORT THOST TPORT ...`, where
// argv.slice(1) is the arguments after the script (measured on the Mac).
const relayScript = `const net=require("net"),C=require("crypto");const k=process.env.AGENTBOX_RELAY_KEY||"";if(!/^[0-9a-f]{64}$/.test(k)){console.log("ERR KEY missing or malformed");process.exit(1)}const K=Buffer.from(k,"hex");const mac=(l,p,r,n)=>{const b=Buffer.alloc(2);b.writeUInt16BE(p);return C.createHmac("sha256",K).update(Buffer.concat([Buffer.from("culibox relay2 "+l+"\0"),b,r,n])).digest()};const a=process.argv.slice(1);const bind=a[0];for(let i=1;i+2<a.length;i+=3){const lp=+a[i],th=a[i+1],tp=+a[i+2];net.createServer({pauseOnConnect:true},c=>{let s=String(c.remoteAddress||"");if(s.startsWith("::ffff:"))s=s.slice(7);if(!/^\d{1,3}(\.\d{1,3}){3}$/.test(s)||!c.remotePort){c.destroy();return}const r=C.randomBytes(32),u=net.connect(tp,th);let g=Buffer.alloc(0),ok=false;const end=()=>{clearTimeout(t);c.destroy();u.destroy()};const t=setTimeout(end,5e3);c.on("error",end);u.on("error",end);u.on("close",()=>{if(!ok)end()});const h=d=>{g=Buffer.concat([g,d]);if(g.length<131)return;const m=/^([0-9a-f]{64}) ([0-9a-f]{64})\r\n$/.exec(g.toString("latin1"));if(g.length>131||!m)return end();const n=Buffer.from(m[1],"hex");if(!C.timingSafeEqual(Buffer.from(m[2],"hex"),mac("listener",tp,r,n)))return end();ok=true;clearTimeout(t);u.removeListener("data",h);u.write(mac("relay",tp,r,n).toString("hex")+"\r\nPROXY TCP4 "+s+" "+bind+" "+c.remotePort+" "+lp+"\r\n");c.pipe(u);u.pipe(c)};u.on("data",h);u.write("AGENTBOX-RELAY2 "+r.toString("hex")+"\r\n")}).on("error",e=>{console.log("ERR "+lp+" "+e.code);process.exit(1)}).listen(lp,bind,()=>console.log("listening "+bind+":"+lp))}`

// RelayKeyEnv is the environment variable the relay container reads its key
// from. `docker run -e RelayKeyEnv` (the name alone) takes the value from the
// docker CLI child's environment, so the key is in no argv and no label.
const RelayKeyEnv = "AGENTBOX_RELAY_KEY"

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
// provisioning writes. Option (a) VMs, VMs provisioned before the egress rules
// existed, and VMs provisioned for other ports end up here.
func errRulesMissing(why string) error {
	return fmt.Errorf("the box VM's egress INPUT rules are missing or out of order (%s); this VM predates egress control for all boxes, or broker_port or egress_port changed since setup; run `agent-box box setup -recreate`", why)
}

// CheckInputRules verifies that the first four rules of the INPUT chain in
// `iptables -S` output are exactly the ones provisioning writes, in order: the egress and broker
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

// CheckDockerUserRules refuses a DOCKER-USER rule that lets TCP through. VMs
// provisioned before #113 carry one for the broker port, which lets every
// container on the VM's default network reach the broker on the Mac without the
// relay, and so write the PROXY line that names a box itself. Nothing CuliBox
// writes passes TCP there now and docker adds at most a bare RETURN, so any such
// rule (any port, any address, RETURN or ACCEPT) means the VM still runs an old
// provision script. The gateway DROP itself must be there too, for the address
// the relay dials (LimaHostGateway): the provision script drops the address
// host.lima.internal resolves to, which is that one (measured in the spike), and
// a VM where they differ must not pass. Rules in other chains are not looked at.
func CheckDockerUserRules(out string) error {
	drop := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "-A" || f[1] != "DOCKER-USER" {
			continue
		}
		if normaliseRule(f[2:]) == "-d "+LimaHostGateway+" -j DROP" {
			drop = true
		}
		var proto, target string
		for i := 2; i+1 < len(f); i++ {
			switch f[i] {
			case "-p":
				proto = f[i+1]
			case "-j":
				target = f[i+1]
			}
		}
		if proto == "tcp" && (target == "RETURN" || target == "ACCEPT") {
			return fmt.Errorf("the box VM's firewall lets containers on its default network reach the Mac's loopback without the relay (DOCKER-USER rule %q); this VM was set up before that rule was removed; run `agent-box box setup -recreate`",
				strings.Join(f[2:], " "))
		}
	}
	if !drop {
		return fmt.Errorf("the box VM's firewall has no DOCKER-USER DROP for the lima gateway %s, so containers on its default network may reach the Mac's loopback; run `agent-box box setup -recreate`", LimaHostGateway)
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
// warning on stderr must not be read as the answer. RunEnv is Run with extra
// environment variables for the docker child only: the relay's key goes in
// that way, never in argv.
type Docker interface {
	Run(ctx context.Context, args []string) (string, error)
	RunEnv(ctx context.Context, args, env []string) (string, error)
	Stdout(ctx context.Context, args []string) (string, error)
}

// Relay is the container on the VM's host network that listens on the egress
// network's gateway and forwards exactly two ports (egress proxy, credential
// broker) to lima's host gateway, which lima NATs to the Mac's loopback. Boxes
// on the internal network reach nothing else.
type Relay struct {
	VM interface {
		FilterRules(context.Context) (string, error)
	}
	Docker          Docker
	Image, Instance string
	EgressPort      int
	BrokerPort      int
	UID, GID        int
	// Key is the relay key the container proves to every host listener, and
	// requires the listener to prove, before it forwards a byte (the key itself
	// never crosses the wire). Required: Ensure removes any relay and refuses the
	// zero Key before it reads the VM's rules.
	Key          proxyline.Key
	ReadyTimeout time.Duration // default 10s
}

// runArgs is the relay's whole `docker run` argv; extra flags go in after the
// fixed ones, before the image. Without extras it is what configHash hashes
// (with the key's id, which is not in argv); Ensure adds the config label that
// carries that hash. It names the key's variable and never holds the key.
func (r *Relay) runArgs(gw netip.Addr, extra ...string) []string {
	args := []string{"run", "-d", "--init", "--name", RelayName(r.Instance), "--network", "host", "--entrypoint", "node",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only", "--pids-limit", "64", "--pull=never",
		"--user", fmt.Sprintf("%d:%d", r.UID, r.GID),
		"-e", RelayKeyEnv, // the name only: docker takes the value from its own environment
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
// whose label differs (any run flag, image, script, port or user, or the key) is
// replaced. The key enters as its ID, a domain-separated fingerprint, never the
// key: the hash goes in a container label.
func (r *Relay) configHash(gw netip.Addr) string {
	return hashArgs(append(r.runArgs(gw), "relay-key-id="+r.Key.ID()))
}

func (r *Relay) docker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, relayDockerTimeout)
	defer cancel()
	return r.Docker.Run(ctx, args)
}

// dockerEnv is docker with env added to the child's environment only.
func (r *Relay) dockerEnv(ctx context.Context, args, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, relayDockerTimeout)
	defer cancel()
	return r.Docker.RunEnv(ctx, args, env)
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

// inspectRelay is `docker inspect` of the relay container as
// "<running>\t<relay-config label>", or docker's error ("No such container" when
// the relay is absent).
func (r *Relay) inspectRelay(ctx context.Context) (string, error) {
	return r.dockerStdout(ctx, "inspect", "--type", "container", "--format",
		`{{.State.Running}}`+"\t"+`{{index .Config.Labels "`+relayConfigLabel+`"}}`, RelayName(r.Instance))
}

// removeUnlessCurrent is the refusal path that keeps a relay running only when
// it can be vouched for (D7): cause is returned either way (RemoveStale passes
// nil), and the relay is removed unless it runs with the current config hash
// (same script, flags and key). A relay with another hash may be one from before #118, which does not
// authenticate the listener, and the v2 listeners refuse it anyway. A current
// relay stays because the refusals that come here (rules unreadable, DOCKER-USER
// refused) can be a transient ssh failure that must not cut live sessions. When
// docker cannot say what runs, nothing is removed and the error says so. The
// inspect runs on a context that outlives the caller's cancellation, like the
// removal it gates (still bounded by relayDockerTimeout): an ssh timeout is what
// makes the rules unreadable, and at boot the same shared deadline has usually
// ended the caller's context by then, which must not leave a pre-#118 relay up.
func (r *Relay) removeUnlessCurrent(ctx context.Context, gw netip.Addr, cause error) error {
	out, err := r.inspectRelay(context.WithoutCancel(ctx))
	switch {
	case err == nil:
		if strings.TrimSpace(out) == "true\t"+r.configHash(gw) {
			return cause
		}
		return r.removeAlso(ctx, cause)
	case strings.Contains(err.Error(), "No such"):
		return cause
	default:
		return errors.Join(cause, fmt.Errorf("inspecting the relay: %w", err))
	}
}

// RemoveStale removes the relay unless it runs with the current config for the
// provisioned gateway (EgressGateway), the one Ensure accepts. An egress Ready
// that is not listening yet calls it before it ensures the network (#118): a
// Ready that fails there, or anywhere before Ensure, never reaches Ensure's
// refusals, and during an upgrade a relay from before #118 would keep
// forwarding the boxes' egress to a port CuliBox does not hold. A current relay
// is kept (#113: a transient failure must not cut live sessions), an absent one
// is fine, and the VM's rules are not read (Ensure still reads them on every
// Ready). Without a key it does nothing: Ensure's zero-key path removes any
// relay. Without the fields the hash is made of it asks docker nothing and
// refuses as Ensure does, since it could not tell a current relay from a stale
// one. An error means docker could not inspect or remove the relay.
func (r *Relay) RemoveStale(ctx context.Context) error {
	if r.Key.IsZero() {
		return nil
	}
	if r.Image == "" || r.Instance == "" || r.EgressPort <= 0 || r.BrokerPort <= 0 {
		return errors.New("relay: image, instance and both ports are required")
	}
	return r.removeUnlessCurrent(ctx, netip.MustParseAddr(EgressGateway), nil)
}

// Ensure makes the relay run for gateway gw, or returns an error. On the
// refusals below that judge the running relay (no key, INPUT rules wrong or
// unreadable, DOCKER-USER refused, a relay that failed to start or listen) the
// only relay left running is one it can vouch for, a current one on the two
// that keep it; a relay that is not current is removed, unless docker itself
// fails to inspect or remove it, and then the error says so. The other refusals
// (missing fields, a foreign gateway, a failed inspect or removal of the old
// relay on the main path) leave whatever runs; an egress Ready that is not
// listening sweeps it with RemoveStale before it gets here. Without a
// key it removes any relay and refuses, without reading the VM's rules: no
// listener binds without a key, so the removal cuts nothing that works, and a
// relay left running could be one from before #118. The INPUT rules are checked
// live first, every time: they are what keeps an unfenced box off the relay, so
// a VM without them gets no relay.
//
// INPUT rules read and wrong: the relay is removed. Rules that could not be
// read (colima ssh error, timeout): this Ready is refused, and a running relay
// is kept only if it is current (same script, flags and key: it proves itself to
// the listeners); any other is removed, because during an upgrade it may be a
// relay that does not authenticate the listener (#118). The relay only ever
// started after the rules were verified this boot, a VM reboot or dockerd
// restart stops it (no restart policy), and a transient ssh failure must not cut
// every live fenced session's proxy and broker, so a current relay stays. A
// DOCKER-USER chain that lets TCP past the relay or lacks the gateway DROP
// (CheckDockerUserRules) is refused the same way, a current relay kept and any
// other removed: that path does not go through the relay, so removing a current
// one would close nothing. Within one daemon run, live sessions keep their proxy
// and broker; a daemon that starts on such a VM never starts its egress listener
// (Ready fails before it), so they keep only the broker until the VM is
// recreated, which ends them anyway.
func (r *Relay) Ensure(ctx context.Context, gw netip.Addr) error {
	if r.Key.IsZero() {
		err := fmt.Errorf("relay: no relay key; CuliBox could not load relay2.secret at start (its log says why)")
		if r.Instance != "" && r.Docker != nil {
			return r.removeAlso(ctx, err)
		}
		return err
	}
	if r.Image == "" || r.Instance == "" || r.EgressPort <= 0 || r.BrokerPort <= 0 {
		return fmt.Errorf("relay: image, instance and both ports are required")
	}
	if gw.String() != EgressGateway {
		return fmt.Errorf("relay: network gateway %s is not the provisioned %s", gw, EgressGateway)
	}
	rules, err := r.VM.FilterRules(ctx)
	if err != nil {
		return r.removeUnlessCurrent(ctx, gw, fmt.Errorf("the box VM's firewall rules could not be read (%w); refusing to start the relay on them", err))
	}
	if err := CheckInputRules(rules, r.EgressPort, r.BrokerPort); err != nil {
		// Read and wrong: nothing may keep relaying without the rules.
		return r.removeAlso(ctx, err)
	}
	if err := CheckDockerUserRules(rules); err != nil {
		return r.removeUnlessCurrent(ctx, gw, err)
	}

	name, hash := RelayName(r.Instance), r.configHash(gw)
	out, err := r.inspectRelay(ctx)
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

	if _, err := r.dockerEnv(ctx, r.runArgs(gw, "--label", relayConfigLabel+"="+hash), []string{RelayKeyEnv + "=" + r.Key.Hex()}); err != nil {
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
