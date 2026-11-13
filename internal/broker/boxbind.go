package broker

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

// A session's token is honoured only from the box the session runs in
// (SessionView.Box). Who presents a request is decided by the transport, never
// by the request: on Linux, by the per-box listener it arrived on (see
// presenterOf); on macOS, by the source address the relay's PROXY line gives
// (BoxSource). Anything undecidable is refused.

// sharedSocketReply is what a box from before the per-box sockets is told over
// the shared socket it still mounts. Fixed and path-free. It is the one reply
// that says more than the path's uniform refusal, and only the shared socket
// sends it: nothing there is a credential or an oracle for another box.
const sharedSocketReply = "this box was started before CuliBox tied credentials to the box that holds them; respawn this session to get credentials again"

// Audit reasons for denied_box rows (the box is never told which).
const (
	whySharedSocket = "box from before per-box broker sockets; respawn the session"
	whyOtherBox     = "not presented from the session's box"
)

// BoxSource tells whether src is the current address of box on the egress
// network: the macOS way to learn which box a request came from. *boxaddr.Cache
// satisfies it. Every failure is false.
type BoxSource interface {
	SourceOK(ctx context.Context, box string, src netip.Addr) bool
}

// SetBoxSource installs the per-request box check for requests that arrive on
// no per-box listener (macOS). Call it before serving. Without one such a
// request is refused.
func (b *Broker) SetBoxSource(s BoxSource) { b.boxSrc = s }

// refusal is the one point that judges a request for a credential: the session
// (live, boxed, bound, this spawn's nonce) first, then the box. "" means serve.
// The session reasons come first so a stale token from another box is still a
// denied_session "nonce mismatch". The three handlers and the GitHub post-mint
// re-check all call it, so they cannot diverge.
func (b *Broker) refusal(r *http.Request, sv SessionView, found bool, nonce string, k bindingKind) (Outcome, string) {
	if why := refusalReason(sv, found, nonce, k); why != "" {
		return OutcomeDeniedSession, why
	}
	if !b.fromBox(r, sv.Box) {
		return OutcomeDeniedBox, whyOtherBox
	}
	return "", ""
}

// fromBox: the request was presented from box want. Fail closed at every step.
func (b *Broker) fromBox(r *http.Request, want string) bool {
	if want == "" { // the session's box is unknown: nothing to compare
		return false
	}
	box, shared := presenterOf(r.Context())
	switch {
	case shared:
		return false
	case box != "":
		return box == want
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || b.boxSrc == nil {
		return false
	}
	return b.boxSrc.SourceOK(r.Context(), want, ap.Addr())
}

// serveShared answers every request that arrives on the shared socket: a box
// from before the per-box sockets mounts it, and nothing on it is served. The
// refusal is audited as denied_box (with the session id only when the token's
// MAC verifies), and the reply tells the box to respawn.
func (b *Broker) serveShared(w http.ResponseWriter, r *http.Request) {
	provider := ""
	switch r.URL.Path {
	case "/v1/github/token":
		provider = ProviderGitHub
	case "/v1/static/token":
		provider = ProviderStatic
	}
	var sid string
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if id, _, ok := ParseToken(b.secret, tok); ok {
			sid = id
		}
	}
	b.recordDenied(Row{Time: b.now(), SessionID: sid, Provider: provider, Outcome: OutcomeDeniedBox, Error: whySharedSocket})
	replyErr(w, http.StatusForbidden, sharedSocketReply)
}
