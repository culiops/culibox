package web

import (
	"log"
	"net/http"
	"strings"
	"sync"
)

// maxLoggedHosts bounds the rejected-host log: a rebinding page can send
// requests with endless Host values.
const maxLoggedHosts = 64

// maxLoggedHostLen bounds one logged Host value; the header is attacker-chosen
// and the dedup set holds up to maxLoggedHosts of them.
const maxLoggedHostLen = 128

// hostGuard refuses any request whose Host is not on the allowlist. It is the
// DNS-rebinding defence: a hostile page whose name resolves to 127.0.0.1 makes
// the browser send Host and Origin that agree with each other, so the existing
// Origin == Host checks pass. Only pinning Host to known names stops it. An
// empty allowlist refuses everything — the guard must never fail open.
type hostGuard struct {
	allowed map[string]bool
	logf    func(string, ...any)

	mu         sync.Mutex
	logged     map[string]bool
	suppressed bool
}

func newHostGuard(allowed []string, logf func(string, ...any)) *hostGuard {
	if logf == nil {
		logf = log.Printf
	}
	g := &hostGuard{allowed: map[string]bool{}, logf: logf, logged: map[string]bool{}}
	for _, h := range allowed {
		g.allowed[strings.ToLower(h)] = true
	}
	return g
}

func (g *hostGuard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.allowed[strings.ToLower(r.Host)] {
			next.ServeHTTP(w, r)
			return
		}
		g.logReject(r.Host)
		// Never echo r.Host: it is attacker-chosen.
		http.Error(w, "host not allowed; add it to allowed_hosts in the culibox config", http.StatusForbidden)
	})
}

func (g *hostGuard) logReject(host string) {
	if len(host) > maxLoggedHostLen {
		host = host[:maxLoggedHostLen] + "..."
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.logged[host] {
		return
	}
	if len(g.logged) >= maxLoggedHosts {
		if !g.suppressed {
			g.suppressed = true
			g.logf("host guard: more rejected Host values suppressed")
		}
		return
	}
	g.logged[host] = true
	// %q escapes control characters, so a hostile Host cannot forge log lines.
	g.logf("host guard: refused Host %q (not in allowed_hosts)", host)
}
