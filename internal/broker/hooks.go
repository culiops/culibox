package broker

import (
	"slices"
	"time"

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
func (h Hooks) BoxFor(b config.AWSBinding) ([]box.Mount, []string, error) {
	dir, err := EnsureBindingDir(h.StateRoot, b)
	if err != nil {
		return nil, nil, err
	}
	mounts := append([]box.Mount{{Host: dir, Box: BoxDir}}, h.EP.Mounts...)
	return mounts, BoxEnv(b, h.EP.BoxURL), nil
}

// StripEnv is every variable a bound session must not inherit.
func (h Hooks) StripEnv() []string { return slices.Clone(StripEnv()) }

// Forget drops the broker's per-session state.
func (h Hooks) Forget(id string) { h.B.Forget(id) }

// AWSState is what a session list shows of the broker's per-session state: the
// end of the live write window (zero when none) and the sign-in key awaiting a
// human ("" when none).
func (h Hooks) AWSState(id string) (until time.Time, ssoExpired string) {
	if v, ok := h.B.Elevation(id); ok && v.Active {
		until = v.Until
	}
	return until, h.B.SSOExpired(id)
}
