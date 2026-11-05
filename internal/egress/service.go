package egress

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/netlimit"
	"github.com/culiops/culibox/internal/proxyline"
)

// proxyHeaderTimeout bounds how long a darwin connection may take to finish the
// whole relay handshake and send its PROXY line; the relay does both before any
// other byte.
const proxyHeaderTimeout = 5 * time.Second

// ServiceOptions configures the lazy "network + listener" pair for one instance.
type ServiceOptions struct {
	Proxy     *Proxy
	Docker    Docker
	GOOS      string
	Instance  string
	Port      int
	Want      Want
	ProxyHost string                                           // the host boxes use in HTTPS_PROXY; "" = the gateway IP (Linux and darwin)
	NoProxy   string                                           // hosts boxes reach directly, not through the proxy
	ExtraEnv  []string                                         // from Task 1's verdict (Task 11 fills it)
	Listen    func(network, addr string) (net.Listener, error) // nil → net.Listen; tests inject
	HostAddrs func() ([]netip.Addr, error)                     // the host's own addresses the proxy never dials; nil → HostAddrs
	// StaticTokens are the instance's token entries. Ready warns about each
	// private cidr that overlaps the live box subnet (Docker picks the subnet
	// on Linux, so config load cannot check it; the proxy never dials that
	// subnet regardless).
	StaticTokens []config.StaticToken
	// AfterNetwork (darwin only; nil on Linux) runs on every Ready call, right
	// after the network is ensured and before anything else, so a failure keeps
	// boxes off an egress path whose VM firewall rules no longer hold.
	AfterNetwork func(ctx context.Context, n Net) error
	// RelayKey (darwin only; zero on Linux) is the key CuliBox's relay and this
	// listener prove to each other before the relay's PROXY line is read (#114,
	// #118). On darwin a zero key makes Ready refuse to listen: the PROXY line
	// is never trusted on its own.
	RelayKey proxyline.Key
}

// Endpoint is where a box reaches the proxy.
type Endpoint struct {
	Net       Net
	ProxyHost string
	Port      int
}

// Service owns the egress network and the proxy's listener for one instance.
type Service struct {
	o            ServiceOptions
	maxConns     int
	maxPerSource int

	mu     sync.Mutex
	ep     Endpoint
	ln     net.Listener
	closed bool
}

func NewService(o ServiceOptions) *Service {
	if o.Listen == nil {
		o.Listen = net.Listen
	}
	if o.HostAddrs == nil {
		o.HostAddrs = HostAddrs
	}
	if o.Proxy != nil && o.Docker != nil {
		// Each principal is bound to its own box: the proxy learns a box's
		// address on this instance's egress network from docker.
		d, network := o.Docker, NetworkName(o.Instance)
		o.Proxy.setBoxAddr(func(ctx context.Context, box string) (netip.Addr, error) {
			return BoxAddr(ctx, d, box, network)
		})
	}
	return &Service{o: o, maxConns: MaxProxyConns, maxPerSource: MaxProxyConnsPerSource}
}

// RefreshHostAddrs reads the host's interface addresses and hands them to the
// proxy. It fails when the read fails or yields no address: without them the
// proxy cannot keep a box off the host's own interfaces.
func (s *Service) RefreshHostAddrs() error {
	a, err := s.o.HostAddrs()
	if err != nil {
		return fmt.Errorf("egress: read host interface addresses: %w", err)
	}
	if len(a) == 0 {
		return errors.New("egress: host has no interface addresses; refusing to serve")
	}
	s.o.Proxy.SetHostAddrs(a)
	return nil
}

// Network is the docker network's name.
func (s *Service) Network() string { return NetworkName(s.o.Instance) }

// Proxy is the proxy this service serves.
func (s *Service) Proxy() *Proxy { return s.o.Proxy }

// Exists reports whether the network is already there (a restored daemon brings
// the listener up at once only if it is).
func (s *Service) Exists(ctx context.Context) bool {
	_, ok, err := InspectNetwork(ctx, s.o.Docker, s.Network())
	return err == nil && ok
}

// Ready is idempotent: it ensures the network (refusing a wrong one), runs
// AfterNetwork on every call right after that, before anything else (under the
// service lock, so Close may wait for it), refreshes
// the host's own addresses (every call, so an address that appears later is
// refused too), points the proxy at the network, and starts listening once. Any failure leaves nothing listening
// that was not already, and a later call retries.
func (s *Service) Ready(ctx context.Context) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Endpoint{}, errors.New("egress: service is closed")
	}
	n, err := EnsureNetwork(ctx, s.o.Docker, s.Network(), s.o.Instance, s.o.Want)
	if err != nil {
		return Endpoint{}, err
	}
	if s.o.AfterNetwork != nil {
		if err := s.o.AfterNetwork(ctx, n); err != nil {
			return Endpoint{}, fmt.Errorf("egress: network %s: %w", n.Name, err)
		}
	}
	if err := s.RefreshHostAddrs(); err != nil {
		return Endpoint{}, err
	}
	if s.ln != nil {
		if n.Gateway != s.ep.Net.Gateway || n.Subnet != s.ep.Net.Subnet {
			return Endpoint{}, fmt.Errorf("egress network %s changed from %s (%s) to %s (%s) while the proxy was listening; restart agent-box",
				n.Name, s.ep.Net.Gateway, s.ep.Net.Subnet, n.Gateway, n.Subnet)
		}
		return s.ep, nil
	}
	addr, err := listenAddr(s.o.GOOS, n, s.o.Port)
	if err != nil {
		return Endpoint{}, err
	}
	// macOS trusts a PROXY line only from a relay that proved the key, so no key
	// means no listener (fail closed and loud), never a header-only fallback.
	if s.o.GOOS == "darwin" && s.o.RelayKey.IsZero() {
		return Endpoint{}, errors.New("egress: no relay key; refusing to listen")
	}
	// The source check is on for both OSes. On macOS every box reaches the proxy
	// through the relay, so the TCP peer is always loopback; the box address is
	// the relay's PROXY line, which the wrapper below requires (after the relay
	// proved the key) and vets.
	s.o.Proxy.SetNetwork(n.Subnet, n.Gateway, true)
	l, err := s.o.Listen("tcp", addr)
	if err != nil {
		return Endpoint{}, fmt.Errorf("egress: listen %s: %w", addr, err)
	}
	// Whatever Listen did, the socket must be where listenAddr said: never wider.
	got, perr := netip.ParseAddrPort(l.Addr().String())
	want := netip.MustParseAddrPort(addr)
	if perr != nil || got.Addr().Unmap() != want.Addr() || got.Addr().IsUnspecified() {
		l.Close()
		return Endpoint{}, fmt.Errorf("egress: listener is bound to %s, not %s; refusing to serve", l.Addr(), addr)
	}
	port := int(got.Port())
	// Both caps apply on both OSes. The total cap sits under the PROXY wrapper
	// and counts raw connections before any header is read, so a silent client
	// cannot hold more than one slot, and only until the header timeout. The
	// per-box cap sits above the wrapper, keyed on the source it vets: the PROXY
	// line's address on macOS (the TCP peer is always the relay) and the peer's
	// own address on Linux. One box cannot then take every slot of the total.
	l = netlimit.Listener(l, s.maxConns)
	if s.o.GOOS == "darwin" {
		l = proxyline.Wrap(l, proxyline.Options{
			Key:        s.o.RelayKey,
			Allow:      proxyline.InSubnetNotGateway(n.Subnet, n.Gateway),
			Timeout:    proxyHeaderTimeout,
			KeyRefused: proxyline.NewRefusalLog("egress proxy", time.Minute, time.Now, log.Printf).Refused,
		})
	}
	l = netlimit.PerSource(l, s.maxPerSource)
	host := s.o.ProxyHost
	if host == "" {
		host = n.Gateway.String()
	}
	s.ln = l
	s.ep = Endpoint{Net: n, ProxyHost: host, Port: port}
	s.warnCIDROverlap(n.Subnet) // once: a later Ready returns early above
	go func() {
		if err := s.o.Proxy.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The accept loop died on its own: forget the listener so a later Ready retries.
			s.mu.Lock()
			if s.ln == l {
				s.ln = nil
			}
			s.mu.Unlock()
			l.Close()
		}
	}()
	return s.ep, nil
}

// warnCIDROverlap logs one line per entry host whose private cidr overlaps the
// box subnet. Those addresses are never dialled; the line tells the operator
// why part of the declared range does not answer.
func (s *Service) warnCIDROverlap(subnet netip.Prefix) {
	if !subnet.IsValid() {
		return
	}
	for _, e := range s.o.StaticTokens {
		for _, h := range e.Egress {
			if c, err := netip.ParsePrefix(h.CIDR); err == nil && c.Overlaps(subnet) {
				log.Printf("egress: token entry %q private cidr %s overlaps the box subnet %s; those addresses are never dialled", e.Name, c, subnet)
			}
		}
	}
}

// Close stops the listener and every tunnel, and waits until each has been
// audited or ctx ends. The docker network stays: a restarted daemon finds it.
func (s *Service) Close(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.o.Proxy.Close(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// listenAddr is the one address the proxy may bind. Linux: the egress network's
// gateway IP, refused when it is invalid, unspecified (0.0.0.0, ::, or the
// 4-in-6 form), loopback, multicast, or outside the network's own subnet; the
// host has a public IP and no firewall. darwin: 127.0.0.1, whatever the gateway.
func listenAddr(goos string, n Net, port int) (string, error) {
	if port < 0 || port > 65535 {
		return "", fmt.Errorf("egress: port %d out of range", port)
	}
	switch goos {
	case "darwin":
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	case "linux":
		gw := n.Gateway
		if !gw.IsValid() {
			return "", errors.New("egress: network has no gateway address; refusing to listen")
		}
		gw = gw.Unmap()
		if gw.IsUnspecified() || gw.IsLoopback() || gw.IsMulticast() || !gw.Is4() {
			return "", fmt.Errorf("egress: gateway %s is not a usable listen address; refusing to listen", gw)
		}
		if n.Subnet.IsValid() && !n.Subnet.Contains(gw) {
			return "", fmt.Errorf("egress: gateway %s is outside the network's subnet %s; refusing to listen", gw, n.Subnet)
		}
		return net.JoinHostPort(gw.String(), strconv.Itoa(port)), nil
	}
	return "", fmt.Errorf("egress: unsupported platform %q", goos)
}
