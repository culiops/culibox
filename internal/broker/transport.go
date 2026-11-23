package broker

import (
	"fmt"
	"log"
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

// proxyHeaderTimeout bounds how long a darwin connection may take to finish the
// whole relay handshake and send its PROXY line; the relay does both before any
// other byte.
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
	// Hosts are the per-session sockets of bound Host sessions, on both OSes (a
	// Host session runs on the host itself, so the colima VM is not crossed).
	Hosts *HostSockets
}

// Listen opens the broker's listeners. Linux: one unix socket per bound box
// (BoxSockets, created as sessions need them: the box that mounts a socket's
// directory is the box every request on it comes from) plus the old shared
// socket, which only answers "respawn"; relayKey is ignored there (no relay).
// darwin: unix sockets do not cross the colima VM (measured in M2), so TCP on
// the Mac's loopback. Only bound sessions get AGENTBOX_BROKER (session.go calls
// BoxFor under bs.Any()), and on darwin every bound box sits on the internal
// egress network, where host.lima.internal does not resolve: the relay on the
// network's gateway forwards the broker port to the Mac's loopback, so boxes
// are handed the gateway address.
//
// The relay opens each connection with a handshake in which it and the listener
// prove they hold the key (relayKey; proxyline's handshake.go), and then sends a
// PROXY line naming the box. Only after the relay's proof matches is the PROXY
// line believed, and only if it names an address inside the box subnet (not the
// gateway), so RemoteAddr is the box's address. The listener writes nothing but
// its proof line before that, so a connection that cannot prove the key (a
// process that is not the relay, a relay from before #118) is closed unanswered,
// and a zero relayKey refuses to listen at all. The key keeps another process on
// the Mac from claiming a box's address; it is not a boundary against your own
// user, since a process running as you can read the key (docs/MACOS.md, Token
// binding). The token guards the rest.
func Listen(goos, stateRoot string, port int, relayKey proxyline.Key) (Endpoint, error) {
	if goos == "darwin" {
		ep, err := listenDarwin(port, relayKey, maxDarwinConns, maxDarwinPerSource)
		if err != nil {
			return Endpoint{}, err
		}
		ep.Hosts = newHostSockets(stateRoot, goos)
		return ep, nil
	}
	return listenLinux(stateRoot)
}

// listenDarwin is the Mac's broker listener: TCP on the loopback, at most
// maxConns raw connections open, then the relay-handshake and PROXY wrapper,
// then at most maxPerSource open connections for any one source address. The
// total cap goes UNDER the wrapper so a connection that never completes the
// handshake or sends its PROXY line still holds its slot (until the header
// timeout) instead of being free to pile up. A zero relayKey is refused before
// anything is bound.
func listenDarwin(port int, relayKey proxyline.Key, maxConns, maxPerSource int) (Endpoint, error) {
	if relayKey.IsZero() {
		return Endpoint{}, fmt.Errorf("broker: no relay key; refusing to listen on 127.0.0.1:%d", port)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return Endpoint{}, fmt.Errorf("broker: listening on 127.0.0.1:%d: %w", port, proxyline.ExplainBindError(port, err))
	}
	p := l.Addr().(*net.TCPAddr).Port
	l = netlimit.Listener(l, maxConns)
	l = proxyline.Wrap(l, proxyline.Options{
		Key:        relayKey,
		Allow:      proxyline.InSubnetNotGateway(netip.MustParsePrefix(boxvm.EgressSubnet), netip.MustParseAddr(boxvm.EgressGateway)),
		Timeout:    proxyHeaderTimeout,
		KeyRefused: proxyline.NewRefusalLog("broker", time.Minute, time.Now, log.Printf).Refused,
	})
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
	l, _, err := listenSocket(stateRoot, sharedRel, sharedSockName, maxSockPath)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Listener: &sharedListener{netlimit.Listener(l, maxSockConns)}, BoxURL: "unix://" + SockBoxDir + "/" + boxSockName,
		Boxes: newBoxSockets(stateRoot), Hosts: newHostSockets(stateRoot, "linux")}, nil
}
