package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
)

// ServiceOptions configures the lazy "network + listener" pair for one instance.
type ServiceOptions struct {
	Proxy     *Proxy
	Docker    Docker
	GOOS      string
	Instance  string
	Port      int
	Want      Want
	ProxyHost string                                           // the host boxes use in HTTPS_PROXY; "" on Linux = the gateway IP; darwin: "host.lima.internal"
	NoProxy   string                                           // darwin: "host.lima.internal"
	ExtraEnv  []string                                         // from Task 1's verdict (Task 11 fills it)
	Listen    func(network, addr string) (net.Listener, error) // nil → net.Listen; tests inject
	HostAddrs func() ([]netip.Addr, error)                     // the host's own addresses the proxy never dials; nil → HostAddrs
}

// Endpoint is where a box reaches the proxy.
type Endpoint struct {
	Net       Net
	ProxyHost string
	Port      int
}

// Service owns the egress network and the proxy's listener for one instance.
type Service struct {
	o        ServiceOptions
	maxConns int

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
	return &Service{o: o, maxConns: MaxProxyConns}
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

// Ready is idempotent: it ensures the network (refusing a wrong one), refreshes
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
	s.o.Proxy.SetNetwork(n.Subnet, n.Gateway, s.o.GOOS == "linux")
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
	l = limitListener(l, s.maxConns)
	host := s.o.ProxyHost
	if host == "" {
		host = n.Gateway.String()
	}
	s.ln = l
	s.ep = Endpoint{Net: n, ProxyHost: host, Port: port}
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
