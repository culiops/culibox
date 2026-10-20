package broker

import (
	"slices"
	"time"

	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

// Hooks is what the session layer needs from the broker, without importing it
// (it satisfies session.BrokerHooks; cmd/agent-box asserts that at compile time).
type Hooks struct {
	B         *Broker
	StateRoot string
	EP        Endpoint
}

// NewNonce is a fresh per-spawn secret for a token.
func (h Hooks) NewNonce() string { return NewNonce() }

// Token is the bearer token for one spawn of a session.
func (h Hooks) Token(id, nonce string) string { return h.B.Token(id, nonce) }

// BoxFor writes the binding's per-binding files and returns the mounts and env
// a bound box needs. It never modifies the endpoint's own mount slice.
func (h Hooks) BoxFor(bs config.Bindings) ([]box.Mount, []string, error) {
	dir, err := EnsureBindingDir(h.StateRoot, bs)
	if err != nil {
		return nil, nil, err
	}
	mounts := append([]box.Mount{{Host: dir, Box: BoxDir}}, h.EP.Mounts...)
	return mounts, BoxEnv(bs, h.EP.BoxURL), nil
}

// WriteSessionMCP writes a session's MCP config file into its binding dir and
// returns the path the box sees it at.
func (h Hooks) WriteSessionMCP(bs config.Bindings, key string, doc []byte) (string, error) {
	return WriteSessionMCP(h.StateRoot, bs, key, doc)
}

// CheckGitHub is the broker's doctor-style check that a GitHub binding's Apps
// are usable: each App is configured and its key loads. It makes no network
// call and never returns key bytes.
func (h Hooks) CheckGitHub(g config.GitHubBinding) error { return h.B.CheckGitHub(g) }

// StripEnv is every variable a bound session must not inherit.
func (h Hooks) StripEnv() []string { return slices.Clone(StripEnv()) }

// StripGitHubEnv is what a session with a GitHub binding must also not inherit.
func (h Hooks) StripGitHubEnv() []string { return StripGitHubEnv() }

// Forget drops the broker's per-session state.
func (h Hooks) Forget(id string) { h.B.Forget(id) }

// RevokeGitHub revokes the session's GitHub tokens at GitHub, in the background.
func (h Hooks) RevokeGitHub(id string) { h.B.RevokeGitHub(id) }

// ProbeBase checks, at spawn, whether each bound AWS base role can write.
func (h Hooks) ProbeBase(id, agent string, bs config.Bindings) map[string]awsprobe.Result {
	return h.B.ProbeBase(id, agent, bs)
}

// WatchBase re-probes the session's unverified base roles (keys as in
// ProbeBase) after their next successful base mint.
func (h Hooks) WatchBase(id string, keys []string) { h.B.WatchBase(id, keys) }

// SetBaseNotify installs the sink for re-probe results; call it once at boot.
func (h Hooks) SetBaseNotify(f func(id, key string, r awsprobe.Result)) { h.B.SetBaseNotify(f) }

// AWSState is what a session list shows of the broker's per-session state: the
// end of the live write window (zero when none), the connection it is for (""
// for the legacy binding or none) and the sign-in key awaiting a human (""
// when none; for a session with connections, the first by name).
func (h Hooks) AWSState(id string) (until time.Time, conn string, ssoExpired string) {
	if v, ok := h.B.Elevation(id); ok && v.Active {
		until, conn = v.Until, v.Name
	}
	return until, conn, h.B.SSOExpired(id)
}

// AWSConnState maps each named connection of the session waiting on a human
// sign-in to its sourceKey; connections that can mint are not in it.
func (h Hooks) AWSConnState(id string) map[string]string { return h.B.SSOExpiredConns(id) }
