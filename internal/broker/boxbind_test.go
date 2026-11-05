package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/config"
)

// A session's token is honoured only from the box the session runs in. These
// tests present requests from explicit boxes; the session "s" runs in ownBox.
const (
	ownBox   = "ab-box-aaaaaaaaaaaa"
	otherBox = "ab-box-bbbbbbbbbbbb"
)

// credPath is one of the three paths that hand out a credential.
type credPath struct {
	name, path, body, provider string
	uniform                    string // the 403 text the box sees for every refusal on this path
}

var credPaths = []credPath{
	{"aws", "/v1/credentials", "", "", "this session is not a live boxed session with an AWS binding"},
	{"github", "/v1/github/token", ghBody, ProviderGitHub, "this session is not a live boxed session with a GitHub binding"},
	{"static", "/v1/static/token", grafanaBody, ProviderStatic, "this session is not a live boxed session with a token binding"},
}

// boxedSession is a live boxed session in ownBox bound for all three providers.
func boxedSession() SessionView {
	v := live()
	v.Box = ownBox
	v.GitHub = ghBinding()
	v.Tokens = []config.StaticToken{grafanaEntry()}
	return v
}

type boxEnv struct {
	b       *Broker
	fm      *fakeMinter
	fg      *fakeGH
	fr      *fakeResolver
	rec     *memRec
	now     *time.Time
	lookups *atomic.Int32
}

// boxSetup builds a broker whose lookup does NOT default views into testBox:
// the box on each view is the one the test wrote.
func boxSetup(t *testing.T, sessions map[string]SessionView) boxEnv {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	e := boxEnv{now: &now, lookups: new(atomic.Int32)}
	e.fm = &fakeMinter{now: func() time.Time { return now }}
	e.rec = &memRec{}
	e.b = New(secret, func(id string) (SessionView, bool) {
		e.lookups.Add(1)
		s, ok := sessions[id]
		return s, ok
	}, e.fm, e.rec)
	e.b.now = func() time.Time { return now }
	e.fg = &fakeGH{life: time.Hour, now: func() time.Time { return now }}
	e.b.SetGitHub(e.fg)
	e.fr = &fakeResolver{}
	e.b.SetResolver(e.fr)
	return e
}

func (e boxEnv) minted() int { return len(e.fm.calls) + len(e.fg.calls) + e.fr.count() }

// present sends cp's request with tok, its context shaped by ctx.
func present(b *Broker, cp credPath, tok string, ctx func(context.Context) context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, cp.path, strings.NewReader(cp.body))
	if ctx != nil {
		r = r.WithContext(ctx(r.Context()))
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	return w
}

func fromBox(name string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context { return withBox(ctx, name) }
}

func errorText(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("reply %q: %v", w.Body, err)
	}
	return out["error"]
}

// oneRow is the only audit row, checked for outcome, session, provider, error.
func oneRow(t *testing.T, rec *memRec, sid string, o Outcome, provider, why string) {
	t.Helper()
	if len(rec.rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one", rec.rows)
	}
	if r := rec.rows[0]; r.SessionID != sid || r.Outcome != o || r.Provider != provider || r.Error != why {
		t.Fatalf("row = %+v, want session %q outcome %s provider %q error %q", r, sid, o, provider, why)
	}
}

// fakeBoxSource answers SourceOK from ok and records what it was asked.
type fakeBoxSource struct {
	mu    sync.Mutex
	asked []srcAsk
	ok    func(box string, src netip.Addr) bool
}

type srcAsk struct {
	box string
	src netip.Addr
}

func (f *fakeBoxSource) SourceOK(_ context.Context, box string, src netip.Addr) bool {
	f.mu.Lock()
	f.asked = append(f.asked, srcAsk{box, src})
	f.mu.Unlock()
	return f.ok(box, src)
}

func (f *fakeBoxSource) calls() []srcAsk {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]srcAsk(nil), f.asked...)
}

var allowAll = &fakeBoxSource{ok: func(string, netip.Addr) bool { return true }}

func TestTokenFromAnotherBoxIsRefusedOnEveryPath(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			w := present(e.b, cp, tokA("s"), fromBox(otherBox))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := errorText(t, w); got != cp.uniform {
				t.Fatalf("reply %q, want the path's uniform text %q (no oracle to the box)", got, cp.uniform)
			}
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched for a token from another box (%d calls)", n)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whyOtherBox)
		})
	}
}

func TestOwnBoxIsServedOnEveryPath(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			if w := present(e.b, cp, tokA("s"), fromBox(ownBox)); w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

// Fail closed: with no per-box listener and nothing to learn the box from, the
// request is refused. A box source (darwin) is what makes it servable.
func TestNoPresentingBoxIsRefused(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			w := present(e.b, cp, tokA("s"), nil)
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whyOtherBox)
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched with no presenting box (%d calls)", n)
			}

			e2 := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			e2.b.SetBoxSource(allowAll)
			if w := present(e2.b, cp, tokA("s"), nil); w.Code != http.StatusOK {
				t.Fatalf("with a box source that vouches: status %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestSessionWithoutABoxIsRefused(t *testing.T) {
	v := boxedSession()
	v.Box = ""
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": v})
			w := present(e.b, cp, tokA("s"), fromBox(ownBox))
			if w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whyOtherBox)

			// Not even a box source that vouches for everything: no box to compare.
			src := &fakeBoxSource{ok: func(string, netip.Addr) bool { return true }}
			e2 := boxSetup(t, map[string]SessionView{"s": v})
			e2.b.SetBoxSource(src)
			if w := present(e2.b, cp, tokA("s"), nil); w.Code != http.StatusForbidden {
				t.Fatalf("a session with no box was served by the address check: status %d", w.Code)
			}
			if len(src.calls()) != 0 {
				t.Fatalf("the address check ran for a session with no box: %v", src.calls())
			}
		})
	}
}

// Session reasons come first: a stale token (a new spawn took the id) from
// another box is a denied_session "nonce mismatch", not a box refusal.
func TestStaleTokenFromAnotherBoxIsDeniedSession(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			w := present(e.b, cp, Token(secret, "s", nonceB), fromBox(otherBox))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d", w.Code)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedSession, cp.provider, "nonce mismatch")
		})
	}
}

func TestDeniedBoxRowsAreThrottled(t *testing.T) {
	e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
	cp := credPaths[0]
	for i := 0; i < 5; i++ {
		if w := present(e.b, cp, tokA("s"), fromBox(otherBox)); w.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: status %d", i, w.Code)
		}
	}
	if len(e.rec.rows) != 1 {
		t.Fatalf("5 refusals at one instant wrote %d rows, want 1", len(e.rec.rows))
	}
	*e.now = e.now.Add(deniedWindow)
	present(e.b, cp, tokA("s"), fromBox(otherBox))
	if len(e.rec.rows) != 2 {
		t.Fatalf("rows after the window = %d, want 2", len(e.rec.rows))
	}
	if want := whyOtherBox + "; +4 similar refusals suppressed"; e.rec.rows[1].Error != want {
		t.Fatalf("second row error %q, want %q", e.rec.rows[1].Error, want)
	}
}

// A mint that finished after the session moved to another box (no request in
// hand to re-judge it by anything but the presenter) is revoked, not handed out.
func TestInFlightMintFromAnotherBoxIsRevoked(t *testing.T) {
	sessions := map[string]SessionView{"s": ghLive()} // setup puts it in testBox; ghCall presents testBox
	b, fg, _, finish := inFlightSetup(t, sessions)
	sv := sessions["s"]
	sv.Box = otherBox // the same nonce, another box
	sessions["s"] = sv
	w := finish()
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "ghs_") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if got := fg.revokedList(); len(got) != 1 {
		t.Fatalf("revoked %v, want the one minted token", got)
	}
	if len(b.ghIssued["s"]) != 0 || len(b.ghCache) != 0 {
		t.Fatalf("state kept the token: %v %v", b.ghIssued, b.ghCache)
	}
}

// darwin: no per-box listener, so the box is the request's source address (the
// relay's PROXY line), judged by the box source.
func TestDarwinSourceDecidesTheBox(t *testing.T) {
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			src := &fakeBoxSource{ok: func(_ string, a netip.Addr) bool { return a == netip.MustParseAddr("172.29.72.9") }}
			e.b.SetBoxSource(src)

			serve := func(remote string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, cp.path, strings.NewReader(cp.body))
				r.RemoteAddr = remote
				r.Header.Set("Authorization", "Bearer "+tokA("s"))
				w := httptest.NewRecorder()
				e.b.Handler().ServeHTTP(w, r)
				return w
			}
			if w := serve("172.29.72.9:40000"); w.Code != http.StatusOK {
				t.Fatalf("the box's own address: status %d: %s", w.Code, w.Body)
			}
			// More than one ask is fine (GitHub judges again after the mint): every
			// ask is about the session's box and the request's own source.
			got := src.calls()
			if len(got) == 0 {
				t.Fatal("the source check was never asked")
			}
			for _, a := range got {
				if a.box != ownBox || a.src != netip.MustParseAddr("172.29.72.9") {
					t.Fatalf("the source check was asked %v", got)
				}
			}
			e.rec.rows = nil
			if w := serve("172.29.72.10:40000"); w.Code != http.StatusForbidden || errorText(t, w) != cp.uniform {
				t.Fatalf("another box's address: status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whyOtherBox)

			n := len(src.calls())
			if w := serve("garbage"); w.Code != http.StatusForbidden {
				t.Fatalf("an unparseable source: status %d", w.Code)
			}
			if len(src.calls()) != n {
				t.Fatal("the source check ran for an address that does not parse")
			}
		})
	}
}

// Boxes from before the per-box sockets mount the shared socket: every request
// on it is refused with a message that says why, and nothing is looked up.
func TestSharedSocketRefusesEveryRequest(t *testing.T) {
	shared := func(ctx context.Context) context.Context { return withShared(ctx) }
	for _, cp := range credPaths {
		t.Run(cp.name, func(t *testing.T) {
			e := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			w := present(e.b, cp, tokA("s"), shared)
			if w.Code != http.StatusForbidden || errorText(t, w) != sharedSocketReply {
				t.Fatalf("status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e.rec, "s", OutcomeDeniedBox, cp.provider, whySharedSocket)
			if n := e.lookups.Load(); n != 0 {
				t.Fatalf("the session was looked up %d times for a request on the shared socket", n)
			}
			if n := e.minted(); n != 0 {
				t.Fatalf("a credential was fetched over the shared socket (%d calls)", n)
			}

			// Even a box source that vouches for everything does not help.
			e3 := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			e3.b.SetBoxSource(allowAll)
			if w := present(e3.b, cp, tokA("s"), shared); w.Code != http.StatusForbidden || errorText(t, w) != sharedSocketReply {
				t.Fatalf("with a box source: status %d body %s", w.Code, w.Body)
			}

			// A token that does not verify gets the same reply and no session id.
			e2 := boxSetup(t, map[string]SessionView{"s": boxedSession()})
			w = present(e2.b, cp, "garbage", shared)
			if w.Code != http.StatusForbidden || errorText(t, w) != sharedSocketReply {
				t.Fatalf("garbage token: status %d body %s", w.Code, w.Body)
			}
			oneRow(t, e2.rec, "", OutcomeDeniedBox, cp.provider, whySharedSocket)
		})
	}
}
