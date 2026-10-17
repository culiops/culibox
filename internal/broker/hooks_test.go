package broker

import (
	"slices"
	"testing"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/sesstoken"
)

func testHooks(t *testing.T) Hooks {
	t.Helper()
	return Hooks{B: New(secret, nil, nil, &memRec{}), StateRoot: t.TempDir(),
		EP: Endpoint{BoxURL: "unix:///opt/agentbox-sock/broker.sock", Mounts: []box.Mount{{Host: "/s", Box: SockBoxDir}}}}
}

func TestHooksBoxFor(t *testing.T) {
	h := testHooks(t)
	mounts, env, err := h.BoxFor(*binding())
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 || mounts[0].Box != BoxDir || mounts[1].Box != SockBoxDir {
		t.Fatalf("mounts %+v", mounts)
	}
	if !slices.Contains(env, "AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock") {
		t.Fatalf("env %q", env)
	}
	// BoxFor must not write into the endpoint's own mount slice.
	if len(h.EP.Mounts) != 1 {
		t.Fatalf("endpoint mounts changed: %+v", h.EP.Mounts)
	}
}

func TestHooksBoxForRefusesAnInvalidBinding(t *testing.T) {
	h := testHooks(t)
	b := *binding()
	b.Region = "x\n[evil]"
	if _, _, err := h.BoxFor(b); err == nil {
		t.Fatal("an invalid binding was prepared")
	}
}

func TestHooksTokenNonceStripForget(t *testing.T) {
	h := testHooks(t)
	n := h.NewNonce()
	if !sesstoken.ValidNonce(n) || n == h.NewNonce() {
		t.Fatalf("nonce %q is malformed or repeated", n)
	}
	if h.Token("claude-3", n) != Token(secret, "claude-3", n) {
		t.Fatal("hooks token differs from the broker's")
	}
	if !slices.Equal(h.StripEnv(), StripEnv()) || !slices.Contains(h.StripEnv(), "AWS_ACCESS_KEY_ID") {
		t.Fatal("strip list")
	}
	h.B.stats["claude-3"] = &Stats{Mints: 1}
	h.Forget("claude-3")
	if _, ok := h.B.Stats("claude-3"); ok {
		t.Fatal("Forget left the session's stats behind")
	}
}
