package broker

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/proxyline"
)

// SockBoxDir is where a box's own socket directory is mounted (read-only)
// inside the box. It is deliberately NOT the path boxes used before the
// per-box sockets (/opt/agentbox-sock, the shared broker/sock dir):
// box.reuseMounts compares in-box destinations only, so a changed destination
// is what stops a box from before the change from being reused silently for a
// new bound spawn (an idle one is recreated, a live one refuses the spawn and
// names the missing mount).
const SockBoxDir = "/opt/agentbox-broker"

// proxyHeaderTimeout bounds how long a darwin connection may take to send its
// PROXY line; the relay writes it before any other byte.
const proxyHeaderTimeout = 5 * time.Second

// Endpoint is how boxes reach the broker on this OS.
type Endpoint struct {
	// Listener: darwin, the relay-facing TCP listener. Linux, the shared socket
	// (a *sharedListener) that boxes from before the per-box sockets mount.
	Listener net.Listener
	BoxURL   string // AGENTBOX_BROKER as seen from inside a box
	// Boxes are the Linux per-box sockets; nil on darwin, where the box is
	// identified per request.
	Boxes *BoxSockets
}

// Listen opens the broker's listeners. Linux: one unix socket per bound box
// (BoxSockets, created as sessions need them: the box that mounts a socket's
// directory is the box every request on it comes from) plus the old shared
// socket, which only answers "respawn". darwin: unix sockets do not cross the
// colima VM (measured in M2), so TCP on the Mac's loopback. Only bound sessions
// get AGENTBOX_BROKER (session.go calls BoxFor under bs.Any()), and on darwin
// every bound box sits on the internal egress network, where host.lima.internal
// does not resolve: the relay on the network's gateway forwards the broker port
// to the Mac's loopback, so boxes are handed the gateway address. The relay
// prepends a PROXY line naming the box, and the listener refuses any connection
// without one that names an address inside the box subnet (not the gateway):
// anything else on the Mac that dials the loopback port is closed unanswered,
// and RemoteAddr is the box's address, as on Linux. The token guards the rest.
func Listen(goos, stateRoot string, port int) (Endpoint, error) {
	if goos == "darwin" {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return Endpoint{}, fmt.Errorf("broker: listening on 127.0.0.1:%d: %w", port, err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l = proxyline.Wrap(l, proxyline.InSubnetNotGateway(netip.MustParsePrefix(boxvm.EgressSubnet), netip.MustParseAddr(boxvm.EgressGateway)), proxyHeaderTimeout)
		return Endpoint{Listener: l, BoxURL: fmt.Sprintf("http://%s:%d", boxvm.EgressGateway, p)}, nil
	}
	return listenLinux(stateRoot)
}

func listenLinux(stateRoot string) (Endpoint, error) {
	// Fail closed at startup: a state root too long for per-box sockets would
	// leave bound sessions with no way to reach the broker.
	if n := len(longestBoxSock(stateRoot)); n > maxSockPath {
		return Endpoint{}, fmt.Errorf("broker: the data directory %s is too long for per-box sockets (%d bytes; a unix socket path holds at most %d). Use a shorter data directory", stateRoot, n, maxSockPath)
	}
	l, _, err := listenSocket(stateRoot, sharedRel)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Listener: &sharedListener{l}, BoxURL: "unix://" + SockBoxDir + "/" + sockName,
		Boxes: newBoxSockets(stateRoot)}, nil
}
