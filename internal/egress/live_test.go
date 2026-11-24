package egress

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

// liveView is s1 bound with the profile hosts allow and egress_open open.
func liveView(allow []string, open bool) SessionView {
	return SessionView{Nonce: nonceA, Box: "box-1", Boxed: true, Live: true, AWS: bound(), EgressAllow: allow, EgressOpen: open}
}

func assertCut(t *testing.T, c net.Conn, what string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatalf("%s: tunnel still open", what)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("%s: tunnel not closed", what)
	}
}

// #95: the policy a CONNECT sees is the session's current view, so an edit
// reaches the next CONNECT in both directions.
func TestPolicyFollowsTheViewAtTheNextConnect(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.tok("s1", nonceA)
	h.setView("s1", liveView([]string{"pypi.org"}, false))
	if code, _ := h.connect(t, "pypi.org:443", "s1", tok); code != 200 {
		t.Fatalf("allowed host: %d", code)
	}
	h.setView("s1", liveView(nil, false))
	if code, _ := h.connect(t, "pypi.org:443", "s1", tok); code != http.StatusForbidden {
		t.Fatalf("removed host: %d, want 403", code)
	}
	if code, _ := h.connect(t, "example.com:443", "s1", tok); code != http.StatusForbidden {
		t.Fatalf("closed session to an unlisted host: %d", code)
	}
	h.setView("s1", liveView(nil, true))
	if code, _ := h.connect(t, "example.com:443", "s1", tok); code != 200 {
		t.Fatalf("opened session: %d", code)
	}
	h.setView("s1", liveView([]string{"*.pythonhosted.org"}, false))
	if code, _ := h.connect(t, "files.pythonhosted.org:443", "s1", tok); code != 200 {
		t.Fatalf("added wildcard: %d", code)
	}
}

func TestReconcileClosesOnlyTunnelsTheNewPolicyRefuses(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.tok("s1", nonceA)
	h.setView("s1", liveView([]string{"pypi.org", "files.pythonhosted.org"}, false))
	_, gone := h.connect(t, "pypi.org:443", "s1", tok)
	_, kept := h.connect(t, "files.pythonhosted.org:443", "s1", tok)
	_, base := h.connect(t, "api.anthropic.com:443", "s1", tok)
	_, other := h.connect(t, "extra.example.org:443", "s2", h.tok("s2", nonceB))
	echoRoundTrip(t, gone, "before")

	h.setView("s1", liveView([]string{"files.pythonhosted.org"}, false))
	if n := h.p.Reconcile("s1"); n != 1 {
		t.Fatalf("Reconcile closed %d tunnels, want 1", n)
	}
	assertCut(t, gone, "tunnel to the removed host")
	echoRoundTrip(t, kept, "still-allowed")
	echoRoundTrip(t, base, "binding-host")
	echoRoundTrip(t, other, "other-session")
	if n := h.p.Reconcile("s1"); n != 0 {
		t.Fatalf("a second Reconcile closed %d", n)
	}
}

func TestReconcileClosesOpenModeTunnelsWhenOpenTurnsOff(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.tok("s1", nonceA)
	h.setView("s1", liveView(nil, true))
	_, c := h.connect(t, "example.com:443", "s1", tok)
	_, base := h.connect(t, "api.anthropic.com:443", "s1", tok)
	h.setView("s1", liveView(nil, false))
	if n := h.p.Reconcile("s1"); n != 1 {
		t.Fatalf("closed %d, want 1", n)
	}
	assertCut(t, c, "open-mode tunnel")
	echoRoundTrip(t, base, "binding-host")
}

// A session the lookup no longer knows, or that has ended, keeps no tunnel.
func TestReconcileOfAGoneSessionClosesEverything(t *testing.T) {
	h := newHarness(t, nil)
	_, c := h.connect(t, "api.anthropic.com:443", "s1", h.tok("s1", nonceA))
	h.mu.Lock()
	delete(h.views, "s1")
	h.mu.Unlock()
	if n := h.p.Reconcile("s1"); n != 1 {
		t.Fatalf("closed %d, want 1", n)
	}
	assertCut(t, c, "gone session")
}

// A CONNECT that authenticated under the old policy and is still dialling when
// the policy narrows gets no tunnel: the proxy re-reads the policy once the
// tunnel is registered, before the 200.
func TestNarrowedDuringDialGetsNoTunnel(t *testing.T) {
	h := newHarness(t, nil)
	h.setView("s1", liveView([]string{"pypi.org"}, false))
	entered, release := make(chan struct{}), make(chan struct{})
	h.dialErr = func(netip.AddrPort) error {
		close(entered)
		<-release
		return nil
	}
	res := make(chan int, 1)
	tok := h.tok("s1", nonceA)
	go func() {
		c, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
		if err != nil {
			res <- -1
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, "CONNECT pypi.org:443 HTTP/1.1\r\nHost: pypi.org:443\r\nProxy-Authorization: "+basic("s1", tok)+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		if err != nil {
			res <- -2
			return
		}
		res <- resp.StatusCode
	}()
	<-entered
	h.setView("s1", liveView(nil, false))
	h.p.Reconcile("s1")
	close(release)
	if code := <-res; code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: the host was removed before the tunnel opened", code)
	}
	row := h.onlyRow(t)
	if row.Outcome != OutcomeDenied || row.SessionID != "s1" || row.Host != "pypi.org" {
		t.Fatalf("row = %+v", row)
	}
	h.p.mu.Lock()
	open := len(h.p.tunnels)
	h.p.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d tunnels registered", open)
	}
	if st, _ := h.p.Stats("s1"); st.Allowed != 0 || st.Denied != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPolicyChangedAuditsAndCloses(t *testing.T) {
	h := newHarness(t, nil)
	tok := h.tok("s1", nonceA)
	h.setView("s1", liveView([]string{"pypi.org"}, false))
	_, c := h.connect(t, "pypi.org:443", "s1", tok)
	h.setView("s1", liveView(nil, false))
	if n := h.p.PolicyChanged("s1", "box-1", "-pypi.org"); n != 1 {
		t.Fatalf("closed %d", n)
	}
	assertCut(t, c, "removed host")
	var got Row
	waitFor(t, "the policy row", func() bool {
		for _, r := range h.rows.all() {
			if r.Outcome == OutcomePolicyChanged {
				got = r
				return true
			}
		}
		return false
	})
	if got.SessionID != "s1" || got.Box != "box-1" || got.Host != "-pypi.org closed:1" || got.Port != 0 {
		t.Fatalf("row = %+v", got)
	}
	// Widening closes nothing and still writes its row.
	h.setView("s1", liveView([]string{"pypi.org"}, true))
	if n := h.p.PolicyChanged("s1", "box-1", "+pypi.org open:on"); n != 0 {
		t.Fatalf("widening closed %d", n)
	}
	waitFor(t, "the widening row", func() bool {
		for _, r := range h.rows.all() {
			if r.Outcome == OutcomePolicyChanged && r.Host == "+pypi.org open:on" {
				return true
			}
		}
		return false
	})
}
