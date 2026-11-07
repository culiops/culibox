package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/culiops/culibox/internal/auth"
)

type credKind int

const (
	credNone credKind = iota
	credCookie
	credBearer
)

type ctxKey int

const credKindKey ctxKey = 0

// credKindOf reports how the request authenticated (credNone if unauthenticated
// or not yet through authGuard).
func credKindOf(r *http.Request) credKind {
	if k, ok := r.Context().Value(credKindKey).(credKind); ok {
		return k
	}
	return credNone
}

// secureCookie reports whether the Secure cookie attribute should be set. Behind
// the Cloudflare tunnel the Go server speaks plain http and terminates TLS at the
// edge, so r.TLS is nil in production — trust X-Forwarded-Proto there. On
// plain-http dev neither is set, so Secure stays off (a Secure cookie over http
// is silently dropped by the browser).
func secureCookie(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// wantsLoginRedirect reports whether an unauthenticated request is a browser
// page navigation that should land on /login rather than see a raw 401. It is
// deliberately narrow: a WebSocket handshake (also a GET) must 401, never 303 —
// a redirected upgrade is a confusing failure. Modern browsers set
// Sec-Fetch-Mode: navigate; the Accept fallback covers older ones that don't.
func wantsLoginRedirect(r *http.Request) bool {
	if r.Method != http.MethodGet || isWebSocketUpgrade(r) {
		return false
	}
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	return r.Header.Get("Sec-Fetch-Mode") == "" &&
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

// wantsHTMLResponse reports whether the caller is a browser expecting HTML (a
// form submit or navigation), as opposed to a fetch/XHR client. Unlike
// wantsLoginRedirect it is not method-gated: a login form POST failure should
// bounce back to the HTML form, while an XHR login gets a JSON/plain 401.
func wantsHTMLResponse(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Mode") == "navigate" ||
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

// authGuard admits a request bearing EITHER a valid session cookie OR a valid
// bearer token, tags it with the credential kind, and slides the cookie forward
// when it is old enough. It never sets WWW-Authenticate.
func (s *Server) authGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := bearerToken(r); ok && s.Tokens != nil {
			if _, ok := s.Tokens.Verify(tok); ok {
				serveWithKind(next, w, r, credBearer)
				return
			}
		}
		if s.Session != nil {
			if valid, refresh := s.Session.Verify(r); valid {
				if refresh {
					s.Session.Issue(w, secureCookie(r))
				}
				serveWithKind(next, w, r, credCookie)
				return
			}
		}
		// Unauthenticated. Send a browser navigation to the login page; answer
		// API/fetch/WS callers with a plain 401. Never WWW-Authenticate.
		if wantsLoginRedirect(r) {
			http.Redirect(w, r, loginRedirectTarget(r), http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// loginRedirectTarget is where an unauthenticated browser navigation lands. A
// request that carried a session cookie the guard just refused (past its expiry,
// or signed with another secret) gets ?reason=expired, so the page can say the
// login expired rather than look like a first visit. No cookie, or the cleared
// (empty) one a logout leaves behind, is a plain /login. The login page maps the
// parameter to one fixed line and never shows its value.
func loginRedirectTarget(r *http.Request) string {
	if ck, err := r.Cookie(auth.CookieName); err == nil && ck.Value != "" {
		return "/login?reason=expired"
	}
	return "/login"
}

func serveWithKind(next http.Handler, w http.ResponseWriter, r *http.Request, k credKind) {
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), credKindKey, k)))
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):]), true
	}
	return "", false
}
