package boxvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/box"
)

// ExecFunc runs `node -e <script>` inside a box and returns its stdout.
type ExecFunc func(ctx context.Context, script string) (string, error)

// ThrowawayProbe is the ExecFunc doctor and `box setup` hand to ProbeLoopback:
// each script runs in a fresh, removed-on-exit container of image, with the same
// lockdown flags a real box gets and no mounts. It goes through cli, so the
// global --host/--config args pin it to agent-box's own VM.
func ThrowawayProbe(cli box.CLIClient, image string) ExecFunc {
	return func(ctx context.Context, script string) (string, error) {
		return cli.Run(ctx, []string{"run", "--rm", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
			"--read-only", "--pull=never", "--entrypoint", "node", image, "-e", script})
	}
}

// acceptDrain is how long the probe keeps accepting after the box script has
// returned. The kernel completes a TCP handshake before Accept runs, so a
// connection can be queued (even already closed by the client) when exec
// returns; draining synchronously counts every one of them.
const acceptDrain = 200 * time.Millisecond

// probeScript tries the host gateway by name and by address (2 s each, run
// together so a DROP rule costs one timeout) after a DNS positive control.
// Without the control, a box with no network at all would "pass" by blocking
// everything. The port is an integer from our own listener.
func probeScript(port int) string {
	return probeHead(port) +
		`dns.lookup('api.anthropic.com',{family:4},e1=>{console.log(e1?'DNS fail '+e1.code:'DNS ok');` +
		probeTail + `});`
}

// probeBoundScript is probeScript for a bound box, which sits on an internal
// network: no DNS (closed on purpose; the egress probe's ALLOW 200 is the
// positive control) and no host.lima.internal (it does not resolve there), so
// both targets are dialled by address. The first is the lima host gateway, past
// the internal network (spike row c6). The second is the egress gateway on the
// listener's port (spike row c6c): the relay forwards only the egress and
// broker ports, which are already bound on the Mac's 127.0.0.1, so the
// listener's port is never one of them. Both must be BLOCKED.
func probeBoundScript(port int) string {
	return probeHead(port) +
		"Promise.all(['" + LimaHostGateway + "','" + EgressGateway + "'].map(tryc)).then(r=>r.forEach(l=>console.log(l)));"
}

func probeHead(port int) string {
	return `const net=require('net'),dns=require('dns');const port=` + strconv.Itoa(port) + `;` +
		`function tryc(h){return new Promise(r=>{const s=net.connect({host:h,port,timeout:2000});` +
		`s.on('connect',()=>{s.destroy();r('REACHED '+h)});s.on('timeout',()=>{s.destroy();r('BLOCKED '+h+' ETIMEDOUT')});` +
		`s.on('error',e=>r('BLOCKED '+h+' '+e.code))})}`
}

const probeTail = `dns.lookup('host.lima.internal',{family:4},async(e,a)=>{const t=['host.lima.internal'];` +
	`t.push(!e&&a?a:'` + LimaHostGateway + `');(await Promise.all(t.map(tryc))).forEach(l=>console.log(l));})`

// ProbeLoopback proves a box cannot reach a listener on the Mac's 127.0.0.1.
// It passes only when the box's network is alive (DNS ok), at least one attempt
// was BLOCKED, none REACHED, and the listener accepted nothing. Anything else,
// including a probe that could not run, is a failure: the caller refuses the box.
func ProbeLoopback(ctx context.Context, exec ExecFunc) error {
	return probeLoopback(ctx, exec, probeScript, true)
}

// ProbeLoopbackBound is ProbeLoopback for a box on the egress network, whose
// DNS is closed: no DNS control is required (the egress probe's ALLOW 200 is
// the positive control and runs first). It dials the lima host gateway and the
// egress gateway by address, since the box cannot resolve host.lima.internal.
// It still fails on any REACHED, on any accept at the listener, on a probe that
// could not run, and on no BLOCKED.
func ProbeLoopbackBound(ctx context.Context, exec ExecFunc) error {
	return probeLoopback(ctx, exec, probeBoundScript, false)
}

func probeLoopback(ctx context.Context, exec ExecFunc, script func(port int) string, needDNS bool) error {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return fmt.Errorf("containment check: opening a loopback listener: %w", err)
	}
	defer ln.Close()
	out, execErr := exec(ctx, script(ln.Addr().(*net.TCPAddr).Port))
	// Count accepts after exec returns, on this goroutine: no race with the
	// listener's close, and queued handshakes are still returned by Accept.
	// Fail closed: without the deadline the drain cannot be trusted to count.
	if err := ln.SetDeadline(time.Now().Add(acceptDrain)); err != nil {
		return fmt.Errorf("containment check: setting the accept deadline: %w", err)
	}
	accepted := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			break
		}
		accepted++
		c.Close()
	}
	if accepted > 0 {
		return errors.New("containment check failed: the box can reach the Mac's loopback services; run `agent-box doctor`")
	}
	if execErr != nil {
		return fmt.Errorf("containment check could not run in the box: %w", execErr)
	}
	var dnsOK bool
	var blocked, reached int
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "DNS ok"):
			dnsOK = true
		case strings.HasPrefix(line, "BLOCKED "):
			blocked++
		case strings.HasPrefix(line, "REACHED "):
			reached++
		}
	}
	switch {
	case reached > 0:
		return errors.New("containment check failed: the box can reach the Mac's loopback services; run `agent-box doctor`")
	case needDNS && !dnsOK:
		return errors.New("containment check inconclusive: the box has no working DNS, so a blocked loopback proves nothing; run `agent-box doctor`")
	case blocked == 0:
		return errors.New("containment check inconclusive: the probe produced no result")
	}
	return nil
}
