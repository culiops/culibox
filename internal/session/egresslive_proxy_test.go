package session

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/egress"
)

// proxyHooks is the manager's egress hooks over a real proxy: Forget and
// PolicyChanged reach it, spawn env comes from the fake.
type proxyHooks struct {
	fakeEgressHooks
	p *egress.Proxy
}

func (h *proxyHooks) Forget(id string) { h.p.Forget(id) }
func (h *proxyHooks) PolicyChanged(id, box, summary string) int {
	return h.p.PolicyChanged(id, box, summary)
}

type rowSink struct{ rows chan egress.Row }

func (r rowSink) Record(row egress.Row) error {
	select {
	case r.rows <- row:
	default:
	}
	return nil
}

// #95 end to end, short of docker: a profile edit, through RefreshEgress and
// the session's live policy, decides what the proxy admits on the next CONNECT,
// and a narrowing cuts the tunnel already open to the removed host.
func TestProxyFollowsTheProfileEdit(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	l := newLiveLauncher()
	m, _, _ := liveManager(t, l, "")
	sink := rowSink{rows: make(chan egress.Row, 64)}
	var p *egress.Proxy
	p = egress.New(egress.Options{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
		// The daemon's lookup (cmd/culibox egressLookup), over this manager.
		Lookup: func(id string) (egress.SessionView, bool) {
			s, ok := m.Get(id)
			if !ok {
				return egress.SessionView{}, false
			}
			allow, open := s.EgressPolicy()
			return egress.SessionView{Nonce: s.EgressNonce, Box: s.Box, Boxed: s.Runtime == config.RuntimeBox, Live: !s.Exited(),
				AWS: s.AWS, AWSConns: s.AWSConns, GitHub: s.GitHub, Tokens: s.Tokens, PATs: s.PATs, EgressAllow: allow, EgressOpen: open}, true
		},
		Audit: sink,
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
		},
		Dial: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
		},
	})
	pl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(pl)
	defer p.Close()
	m.SetEgress(&proxyHooks{p: p})
	m.RefreshEgress()

	s := spawnLive(t, m, "gh")
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.ID+":"+p.Token(s.ID, s.EgressNonce)))
	connect := func(host string) (int, net.Conn) {
		t.Helper()
		c, err := net.DialTimeout("tcp", pl.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		c.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(c, "CONNECT "+host+":443 HTTP/1.1\r\nHost: "+host+":443\r\nProxy-Authorization: "+auth+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Time{})
		return resp.StatusCode, c
	}

	if code, _ := connect("pypi.org"); code != http.StatusForbidden {
		t.Fatalf("before the edit: %d, want 403", code)
	}
	l.set("gh", []string{"pkg.example", "pypi.org"}, false)
	m.RefreshEgress()
	code, tun := connect("pypi.org")
	if code != 200 {
		t.Fatalf("after adding the host: %d, want 200", code)
	}
	tun.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(tun, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(tun, make([]byte, 1)); err != nil {
		t.Fatalf("tunnel not working: %v", err)
	}
	_, keep := connect("pkg.example")

	l.set("gh", []string{"pkg.example"}, false)
	m.RefreshEgress()
	tun.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := tun.Read(make([]byte, 1)); err == nil {
		t.Fatal("tunnel to the removed host survived")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("tunnel to the removed host was not closed")
	}
	keep.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(keep, "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(keep, make([]byte, 1)); err != nil {
		t.Fatalf("tunnel to a kept host was cut: %v", err)
	}
	if code, _ := connect("pypi.org"); code != http.StatusForbidden {
		t.Fatalf("after removing the host: %d, want 403", code)
	}
	if c := s.Info().EgressChange; c != nil {
		t.Fatalf("back at the spawn policy, change = %+v", c)
	}
	var policyRows []string
	deadline := time.After(2 * time.Second)
	for len(policyRows) < 2 {
		select {
		case r := <-sink.rows:
			if r.Outcome == egress.OutcomePolicyChanged && r.SessionID == s.ID {
				policyRows = append(policyRows, r.Host)
			}
		case <-deadline:
			t.Fatalf("policy rows = %q", policyRows)
		}
	}
	if policyRows[0] != "+pypi.org" || policyRows[1] != "-pypi.org closed:1" {
		t.Fatalf("policy rows = %q", policyRows)
	}
}
