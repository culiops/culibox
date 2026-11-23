package broker

import (
	"context"
	"slices"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
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
// a bound box needs. base is the spawn's base-role probe results (nil without
// AWS), which aws.json carries per connection. The box's own broker socket is
// not here: AcquireBox hands it out once the box's name is known.
func (h Hooks) BoxFor(bs config.Bindings, base map[string]awsprobe.Result) ([]box.Mount, []string, error) {
	dir, err := EnsureBindingDirWith(h.StateRoot, bs, base)
	if err != nil {
		return nil, nil, err
	}
	return []box.Mount{{Host: dir, Box: BoxDir}}, BoxEnv(bs, h.EP.BoxURL), nil
}

// AcquireBox takes a reference on box boxName's own broker socket and returns
// the read-only mount that carries it into the box. Linux: the per-box socket
// directory. darwin: nil, nil (the box is identified per request; there is
// nothing to mount or hold). Every call that returns a nil error must be paired
// with exactly one ReleaseBox.
func (h Hooks) AcquireBox(boxName string) ([]box.Mount, error) {
	if h.EP.Boxes == nil {
		return nil, nil
	}
	m, err := h.EP.Boxes.Acquire(boxName)
	if err != nil {
		return nil, err
	}
	return []box.Mount{m}, nil
}

// AdoptBox is AcquireBox for a live session found again after a restart: the
// socket comes back in the directory the running box already mounts, so the
// mount is not needed. Paired with ReleaseBox like AcquireBox.
func (h Hooks) AdoptBox(boxName string) error {
	if h.EP.Boxes == nil {
		return nil
	}
	_, err := h.EP.Boxes.Acquire(boxName)
	return err
}

// ReleaseBox drops one reference taken by AcquireBox or AdoptBox. At zero the
// box's socket closes.
func (h Hooks) ReleaseBox(boxName string) {
	if h.EP.Boxes != nil {
		h.EP.Boxes.Release(boxName)
	}
}

// WriteAWSListing rewrites aws.json in the binding's existing dir with new
// base-role probe results, and in the dir of every live Host session with the
// same binding; a no-op without named connections. With no such Host session
// it is the box's WriteAWSListing, unchanged (a missing binding dir is an
// error); with one, a missing binding dir only means no box holds the binding.
func (h Hooks) WriteAWSListing(bs config.Bindings, base map[string]awsprobe.Result) error {
	if len(bs.AWSConns) == 0 {
		return nil
	}
	var names []string
	if h.EP.Hosts != nil {
		names = h.EP.Hosts.dirsOf(bs.Hash())
	}
	if len(names) == 0 {
		return WriteAWSListing(h.StateRoot, bs, base)
	}
	return writeAWSListingHosts(h.StateRoot, bs, base, names)
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

// CheckSSODirect gates an sso_direct binding of bs at spawn: key "" is the
// inline binding, else a connection name. It is the session layer's read-only
// check (session.SSODirectChecker). A key that is not an sso_direct binding is
// unverified, so the gate can never vouch for an AssumeRole binding.
func (h Hooks) CheckSSODirect(ctx context.Context, bs config.Bindings, key string) (awsprobe.GateResult, time.Time) {
	if key == "" {
		if bs.AWS != nil && bs.AWS.SSODirect() {
			return h.B.CheckSSODirect(ctx, *bs.AWS)
		}
	} else {
		for _, c := range bs.AWSConns {
			if c.Name == key && c.SSODirect() {
				return h.B.CheckSSODirectConn(ctx, c)
			}
		}
	}
	return awsprobe.GateResult{State: awsprobe.GateUnverified, Reason: awsprobe.ReasonError, CheckedAt: h.B.now()}, time.Time{}
}

// WatchBase re-probes the session's unverified base roles (keys as in
// ProbeBase) after their next successful base mint.
func (h Hooks) WatchBase(id string, keys []string) { h.B.WatchBase(id, keys) }

// SetBaseNotify installs the sink for re-probe results; call it once at boot
// (may be after serving).
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

// StoreLockedFor reports whether the session's last store read was refused as locked.
func (h Hooks) StoreLockedFor(id string) bool { return h.B.StoreLockedFor(id) }

// AWSConnState maps each named connection of the session waiting on a human
// sign-in to its sourceKey; connections that can mint are not in it.
func (h Hooks) AWSConnState(id string) map[string]string { return h.B.SSOExpiredConns(id) }
