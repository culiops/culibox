package egress

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// readyTimeout bounds the network + listener bring-up a bound spawn waits for.
const readyTimeout = 30 * time.Second

// measuredEnv is the env spec 7.1 recorded as shrinking the allowlist without
// breaking the workload (Claude Code's non-essential traffic, terraform's
// version check). It is part of the policy, not of the wiring: DefaultAllow is
// exactly the set observed under it, so a spawn that omitted it would reach
// hosts the allowlist denies and fail noisily instead.
func measuredEnv() []string {
	return []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CHECKPOINT_DISABLE=1"}
}

// proxyEnvNames are every proxy setting a bound session must not inherit: ones
// that would point elsewhere, or exempt hosts. NODE_USE_ENV_PROXY is Node's own
// switch for honouring them.
var proxyEnvNames = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy",
	"NO_PROXY", "no_proxy", "NODE_USE_ENV_PROXY"}

func envName(kv string) string {
	if i := strings.IndexByte(kv, '='); i > 0 {
		return kv[:i]
	}
	return kv
}

// extraEnv is measuredEnv plus the configured ExtraEnv; a later entry for the
// same name wins, so each name is set once.
func (s *Service) extraEnv() []string {
	var out []string
	idx := map[string]int{}
	for _, kv := range append(measuredEnv(), s.o.ExtraEnv...) {
		n := envName(kv)
		if i, ok := idx[n]; ok {
			out[i] = kv
			continue
		}
		idx[n] = len(out)
		out = append(out, kv)
	}
	return out
}

// SpawnEnv readies the network and listener (idempotent) and returns the env
// that points a bound box's tools at the proxy, carrying this spawn's token in
// the HTTPS_PROXY userinfo (spec 7.1: all four tools send it, git after the
// proxy's 407 challenge). Only HTTPS_PROXY: the proxy takes CONNECT on 443 and
// nothing else, so HTTP_PROXY is deliberately left unset. Any failure returns no
// env at all, and the spawn is refused.
func (s *Service) SpawnEnv(ctx context.Context, sessionID, nonce string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	ep, err := s.Ready(ctx)
	if err != nil {
		return nil, err
	}
	u := url.URL{
		Scheme: "http",
		User:   url.UserPassword(sessionID, s.o.Proxy.Token(sessionID, nonce)),
		Host:   net.JoinHostPort(ep.ProxyHost, strconv.Itoa(ep.Port)),
	}
	env := []string{"HTTPS_PROXY=" + u.String(), "https_proxy=" + u.String()}
	if s.o.NoProxy != "" {
		env = append(env, "NO_PROXY="+s.o.NoProxy, "no_proxy="+s.o.NoProxy)
	}
	return append(env, s.extraEnv()...), nil
}

// StripEnv names every variable a bound session must not inherit: the proxy
// settings, plus each name SpawnEnv sets, so every one is set exactly once. A
// fresh slice each call.
func (s *Service) StripEnv() []string {
	out := append([]string(nil), proxyEnvNames...)
	for _, kv := range s.extraEnv() {
		out = append(out, envName(kv))
	}
	return out
}

// Forget drops a gone session's state at the proxy (counters, open tunnels).
func (s *Service) Forget(id string) { s.o.Proxy.Forget(id) }

// PolicyChanged closes the tunnels session id's new policy refuses and audits
// the change (#95); see Proxy.PolicyChanged.
func (s *Service) PolicyChanged(id, box, summary string) int {
	return s.o.Proxy.PolicyChanged(id, box, summary)
}
