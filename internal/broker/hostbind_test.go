package broker

import (
	"context"
	"net/http"
	"net/netip"
	"testing"
)

// A Host session runs on the host, not in a box: its token is honoured only on
// the host socket that belongs to it. These tests present requests the way the
// transport would: withHost on its own listener, withBox on a box's.

// hostSession is a live Host session bound for all three providers. It has no
// box: the host listener is what identifies it.
func hostSession() SessionView {
	v := boxedSession()
	v.Boxed, v.Host, v.Box = false, true, ""
	return v
}

func onHostSocket(sid string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return withHost(ctx, sid) }
}

func TestHostSessionServedOnItsOwnSocket(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			if w := present(e.b, cp, tokA("s1"), onHostSocket("s1")); w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if e.minted() != 1 {
				t.Fatalf("minted %d, want 1", e.minted())
			}
		})
	}
}

func TestHostTokenOnAnotherHostSocketIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			w := present(e.b, cp, tokA("s1"), onHostSocket("s2"))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched on another session's host socket (%d calls)", n)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, whyOtherBox)
		})
	}
}

func TestHostTokenOnABoxSocketIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			w := present(e.b, cp, tokA("s1"), fromBox(ownBox))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, whyOtherBox)
			if n := e.minted(); n != 0 {
				t.Fatalf("a Host token was honoured on a box socket (%d calls)", n)
			}
		})
	}
}

// A box session's token on a host socket is refused, and so is a Host token on
// no listener at all, even when the box source would vouch for everything.
func TestBoxTokenOnAHostSocketIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			src := &fakeBoxSource{ok: func(string, netip.Addr) bool { return true }}
			e.b.SetBoxSource(src)
			w := present(e.b, cp, tokA("s"), onHostSocket("s"))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whyOtherBox)
			if n := e.minted(); n != 0 {
				t.Fatalf("a box token was honoured on a host socket (%d calls)", n)
			}
			if got := src.calls(); len(got) != 0 {
				t.Fatalf("the address check ran for a host-presented request: %v", got)
			}
		})
	}
}

func TestHostTokenWithNoPresenterIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			e.b.SetBoxSource(allowAll)
			if w := present(e.b, cp, tokA("s1"), nil); w.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, whyOtherBox)
		})
	}
}

func TestNeitherBoxedNorHostIsRefused(t *testing.T) {
	v := hostSession()
	v.Host = false
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": v})
			w := present(e.b, cp, tokA("s1"), onHostSocket("s1"))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedSession, cp.provider, "not boxed")
		})
	}
}
