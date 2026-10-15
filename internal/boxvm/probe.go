package boxvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// ExecFunc runs `node -e <script>` inside a box and returns its stdout.
type ExecFunc func(ctx context.Context, script string) (string, error)

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
	return `const net=require('net'),dns=require('dns');const port=` + strconv.Itoa(port) + `;` +
		`function tryc(h){return new Promise(r=>{const s=net.connect({host:h,port,timeout:2000});` +
		`s.on('connect',()=>{s.destroy();r('REACHED '+h)});s.on('timeout',()=>{s.destroy();r('BLOCKED '+h+' ETIMEDOUT')});` +
		`s.on('error',e=>r('BLOCKED '+h+' '+e.code))})}` +
		`dns.lookup('api.anthropic.com',{family:4},e1=>{console.log(e1?'DNS fail '+e1.code:'DNS ok');` +
		`dns.lookup('host.lima.internal',{family:4},async(e,a)=>{const t=['host.lima.internal'];` +
		`t.push(!e&&a?a:'192.168.5.2');(await Promise.all(t.map(tryc))).forEach(l=>console.log(l));})});`
}

// ProbeLoopback proves a box cannot reach a listener on the Mac's 127.0.0.1.
// It passes only when the box's network is alive (DNS ok), at least one attempt
// was BLOCKED, none REACHED, and the listener accepted nothing. Anything else,
// including a probe that could not run, is a failure: the caller refuses the box.
func ProbeLoopback(ctx context.Context, exec ExecFunc) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("containment check: opening a loopback listener: %w", err)
	}
	defer ln.Close()
	out, execErr := exec(ctx, probeScript(ln.Addr().(*net.TCPAddr).Port))
	// Count accepts after exec returns, on this goroutine: no race with the
	// listener's close, and queued handshakes are still returned by Accept.
	accepted := 0
	if tl, ok := ln.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(acceptDrain))
		for {
			c, err := tl.Accept()
			if err != nil {
				break
			}
			accepted++
			c.Close()
		}
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
	case !dnsOK:
		return errors.New("containment check inconclusive: the box has no working DNS, so a blocked loopback proves nothing; run `agent-box doctor`")
	case blocked == 0:
		return errors.New("containment check inconclusive: the probe produced no result")
	}
	return nil
}
