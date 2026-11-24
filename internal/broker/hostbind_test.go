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
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.hostUniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched on another session's host socket (%d calls)", n)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, "not presented on the session's own host socket")
		})
	}
}

func TestHostTokenOnABoxSocketIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			w := present(e.b, cp, tokA("s1"), fromBox(ownBox))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.hostUniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, "not presented on the session's own host socket")
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
			if w := present(e.b, cp, tokA("s1"), nil); w.Code != http.StatusForbidden || errorText(t, w) != cp.hostUniform {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, "not presented on the session's own host socket")
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

// A Host session's fetches stop with the session: once it is no longer live,
// its own socket refuses every path, says Host in the reply, and hands out and
// caches nothing.
func TestHostFetchAfterTheSessionEndsIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			v := hostSession()
			sessions := map[string]SessionView{"s1": v}
			e := boxSetup(t, sessions)
			if w := present(e.b, cp, tokA("s1"), onHostSocket("s1")); w.Code != http.StatusOK {
				t.Fatalf("while live: status %d: %s", w.Code, w.Body)
			}
			e.rec.reset()
			minted := e.minted()

			v.Live = false // the session ended; the token is still well formed
			sessions["s1"] = v
			w := present(e.b, cp, tokA("s1"), onHostSocket("s1"))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.hostUniform {
				t.Fatalf("after the end: status %d body %s", w.Code, w.Body)
			}
			// Ending the session revokes its cached GitHub tokens in the
			// background (revokeAsync), which records its own row: wait for it,
			// then check the refusal by its outcome rather than by count.
			e.b.revokeWG.Wait()
			var denied []Row
			for _, r := range e.rec.snapshot() {
				if r.Outcome != OutcomeRevoked && r.Outcome != OutcomeRevokeFailed {
					denied = append(denied, r)
				}
			}
			if len(denied) != 1 {
				t.Fatalf("rows besides revokes = %+v, want exactly the refusal", denied)
			}
			if r := denied[0]; r.SessionID != "s1" || r.Outcome != OutcomeDeniedSession || r.Provider != cp.provider || r.Error != "not live" || r.Tier != TierHost {
				t.Fatalf("refusal row = %+v, want session s1, %s, %q, \"not live\", tier %q", r, OutcomeDeniedSession, cp.provider, TierHost)
			}
			if n := e.minted(); n != minted {
				t.Fatalf("a credential was fetched after the session ended (%d new calls)", n-minted)
			}
		})
	}
}

// A Host token on the shared box socket is a Host refusal, audited as host:
// the box wording and the respawn advice are for boxes.
func TestHostTokenOnTheSharedSocketIsAHostRefusal(t *testing.T) {
	shared := func(ctx context.Context) context.Context { return withShared(ctx) }
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s1": hostSession()})
			w := present(e.b, cp, tokA("s1"), shared)
			if w.Code != http.StatusForbidden || errorText(t, w) != "this Host session gets credentials only on its own socket" {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s1", OutcomeDeniedBox, cp.provider, "Host session token presented on the shared box socket")
			if e.rec.rows[0].Tier != TierHost {
				t.Fatalf("row tier = %q, want %q", e.rec.rows[0].Tier, TierHost)
			}
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched over the shared socket (%d calls)", n)
			}
		})
	}
}
