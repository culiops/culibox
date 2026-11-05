package broker

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/netlimit"
	"github.com/culiops/culibox/internal/proxyline"
)

// SockBoxDir is where a box's own socket directory is mounted (read-only)
// inside the box. It is deliberately NOT the path boxes used before the
// per-box sockets (/opt/agentbox-sock, the shared broker/sock dir):
// box.reuseMounts compares in-box destinations only, so a changed destination
// is what stops a box from before the change from being reused silently for a
// new bound spawn (an idle one is recreated, a live one refuses the spawn and
// names the missing mount).
const SockBoxDir = box.BrokerMountDest

// proxyHeaderTimeout bounds how long a darwin connection may take to send its
// PROXY line; the relay writes it before any other byte.
const proxyHeaderTimeout = 5 * time.Second

// maxDarwinConns caps the raw connections the darwin listener holds at once
// (inferred, not measured): requests are short (5 s for the PROXY line, 5 s for
// the headers, 30 s to write), and mints are rate-limited per session (10 a
// minute for AWS, 60 for static tokens), so this is half the egress proxy's cap,
// which also has to carry long tunnels. Without a cap any box on the egress
// network could open connections until the daemon ran out of file descriptors.
const maxDarwinConns = 256

// maxDarwinPerSource is how many of those one source address (one box) may hold
// at once (decided: 64, a quarter of the total). The relay forwards every box's
// connections, bound or not, so without it one box that opens maxDarwinConns
// idle connections stalls every other box's credential calls.
const maxDarwinPerSource = 64

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
// without one that names an address inside the box subnet (not the gateway), so
// RemoteAddr is the box's address. The line is not authenticated: any process on
// the Mac that dials the loopback port can write one (docs/MACOS.md, Token
// binding), which Linux's per-box sockets do not allow. The token guards the rest.
func Listen(goos, stateRoot string, port int) (Endpoint, error) {
	if goos == "darwin" {
		return listenDarwin(port, maxDarwinConns, maxDarwinPerSource)
	}
	return listenLinux(stateRoot)
}

// listenDarwin is the Mac's broker listener: TCP on the loopback, at most
// maxConns raw connections open, then the PROXY wrapper, then at most
// maxPerSource open connections for any one source address. The total cap goes
// UNDER the wrapper so a connection that never sends its PROXY line still holds
// its slot (until the header timeout) instead of being free to pile up.
func listenDarwin(port, maxConns, maxPerSource int) (Endpoint, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return Endpoint{}, fmt.Errorf("broker: listening on 127.0.0.1:%d: %w", port, err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l = netlimit.Listener(l, maxConns)
	l = proxyline.Wrap(l, proxyline.InSubnetNotGateway(netip.MustParsePrefix(boxvm.EgressSubnet), netip.MustParseAddr(boxvm.EgressGateway)), proxyHeaderTimeout)
	// Above the wrapper: the source is the PROXY header's, known only once the
	// header was read (in the conn's own goroutine). A conn that has not shown it
	// yet is held by the total cap alone.
	l = netlimit.PerSource(l, maxPerSource)
	return Endpoint{Listener: l, BoxURL: fmt.Sprintf("http://%s:%d", boxvm.EgressGateway, p)}, nil
}

func listenLinux(stateRoot string) (Endpoint, error) {
	// Fail closed at startup: a state root too long for per-box sockets would
	// leave bound sessions with no way to reach the broker.
	if n := len(longestBoxSock(stateRoot)); n > maxSockPath {
		return Endpoint{}, fmt.Errorf("broker: the data directory %s is too long for per-box sockets (%d bytes; a unix socket path holds at most %d). Use a shorter data directory", stateRoot, n, maxSockPath)
	}
	l, _, err := listenSocket(stateRoot, sharedRel, sharedSockName)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Listener: &sharedListener{netlimit.Listener(l, maxSockConns)}, BoxURL: "unix://" + SockBoxDir + "/" + boxSockName,
		Boxes: newBoxSockets(stateRoot)}, nil
}
