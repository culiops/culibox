package broker

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/proxyline"
	"github.com/culiops/culibox/internal/sesstoken"
)

func testHooks(t *testing.T) Hooks {
	t.Helper()
	return Hooks{B: New(secret, nil, nil, &memRec{}), StateRoot: t.TempDir(),
		EP: Endpoint{BoxURL: "unix:///opt/agentbox-broker/s"}}
}

func TestHooksBoxFor(t *testing.T) {
	h := testHooks(t)
	mounts, env, err := h.BoxFor(awsOnly(*binding()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Box != BoxDir {
		t.Fatalf("mounts %+v: BoxFor returns the binding dir only; the box's own socket comes from AcquireBox", mounts)
	}
	if !slices.Contains(env, "AGENTBOX_BROKER=unix:///opt/agentbox-broker/s") {
		t.Fatalf("env %q", env)
	}
}

// A bound spawn takes its box's own socket: Linux hands out the mount and a
// reference, and every Acquire is paired with one Release.
func TestHooksAcquireBoxLinux(t *testing.T) {
	root := shortDir(t)
	ep, err := Listen("linux", root, 0, proxyline.Key{})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Listener.Close()
	ep.Boxes.Start(func(net.Listener) {})
	defer ep.Boxes.Close()
	h := Hooks{StateRoot: root, EP: ep}
	ms, err := h.AcquireBox(bx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Box != SockBoxDir || ms[0].Host != filepath.Dir(perBoxSock(root, bx)) {
		t.Fatalf("mounts %+v", ms)
	}
	if err := h.AdoptBox(bx); err != nil {
		t.Fatal(err)
	}
	h.ReleaseBox(bx)
	if _, err := os.Lstat(perBoxSock(root, bx)); err != nil {
		t.Fatalf("the socket closed with one reference left: %v", err)
	}
	h.ReleaseBox(bx)
	if _, err := os.Lstat(perBoxSock(root, bx)); err == nil {
		t.Fatal("the socket survived its last release")
	}
	if _, err := h.AcquireBox("not-a-box"); err == nil {
		t.Fatal("AcquireBox accepted a name that is not a box")
	}
	if err := h.AdoptBox("not-a-box"); err == nil {
		t.Fatal("AdoptBox accepted a name that is not a box")
	}
}

// On macOS the box is identified per request (the relay's PROXY source), so
// there is nothing to mount and nothing to hold.
func TestHooksAcquireBoxDarwinIsNoop(t *testing.T) {
	h := Hooks{EP: Endpoint{}} // Boxes == nil
	ms, err := h.AcquireBox(bx)
	if ms != nil || err != nil {
		t.Fatalf("AcquireBox = %v, %v; want nil, nil", ms, err)
	}
	if err := h.AdoptBox(bx); err != nil {
		t.Fatalf("AdoptBox = %v", err)
	}
	h.ReleaseBox(bx) // must not panic
}

func TestHooksBoxForRefusesAnInvalidBinding(t *testing.T) {
	h := testHooks(t)
	b := *binding()
	b.Region = "x\n[evil]"
	if _, _, err := h.BoxFor(awsOnly(b), nil); err == nil {
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

func TestHooksAWSState(t *testing.T) {
	b, _, _, now := setup(t, map[string]SessionView{"aws-1": liveElev(), "c-1": elevConns()})
	withTimers(b)
	h := Hooks{B: b}
	if until, conn, sso := h.AWSState("aws-1"); !until.IsZero() || conn != "" || sso != "" {
		t.Fatalf("a quiet session reports %v %q %q", until, conn, sso)
	}
	v, err := b.Elevate("aws-1", cookie(30))
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.ssoNeed[connKey{"aws-1", ""}] = "example-sso"
	b.mu.Unlock()
	until, conn, sso := h.AWSState("aws-1")
	if !until.Equal(v.Until) || !until.Equal(now.Add(30*time.Minute)) || conn != "" || sso != "example-sso" {
		t.Fatalf("AWSState = %v %q %q", until, conn, sso)
	}
	if cs := h.AWSConnState("aws-1"); len(cs) != 0 {
		t.Fatalf("a legacy session has no connection state: %v", cs)
	}
	h.Forget("aws-1")
	if until, _, sso := h.AWSState("aws-1"); !until.IsZero() || sso != "" {
		t.Fatalf("Forget left %v %q", until, sso)
	}
	// A connection's window names its connection.
	cv, err := b.Elevate("c-1", connReq("a", 30))
	if err != nil {
		t.Fatal(err)
	}
	if until, conn, _ := h.AWSState("c-1"); !until.Equal(cv.Until) || conn != "a" {
		t.Fatalf("connection AWSState = %v %q", until, conn)
	}
}

func TestHooksBoxForGitHub(t *testing.T) {
	h := testHooks(t)
	mounts, env, err := h.BoxFor(config.Bindings{GitHub: ghTwoOwners()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Box != BoxDir {
		t.Fatalf("mounts %+v", mounts)
	}
	if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") || !slices.Contains(env, "AGENTBOX_GH_OWNERS=culiops,CuliOps2") {
		t.Fatalf("env %q", env)
	}
}

func TestHooksWatchBaseAndNotify(t *testing.T) {
	b, _, _, _ := setup(t, map[string]SessionView{"s-1": live()})
	b.SetSimulator(&fakeSim{dec: allowsPut})
	h := Hooks{B: b}
	nr := &notifyRec{}
	h.SetBaseNotify(nr.f)
	h.WatchBase("s-1", []string{""})
	if w := call(b, tokA("s-1")); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	b.probeWG.Wait()
	if got := nr.list(); len(got) != 1 || got[0].sid != "s-1" || got[0].key != "" {
		t.Fatalf("notified %+v", got)
	}
}

// BoxFor hands the spawn's probe results to the binding dir: aws.json carries
// them per connection.
func TestHooksBoxForWritesBaseIntoAWSListing(t *testing.T) {
	h := testHooks(t)
	cs := twoConns()
	base := map[string]awsprobe.Result{"acme-prod-read": {State: awsprobe.Writes, Actions: []string{"s3:PutObject"}}}
	mounts, _, err := h.BoxFor(config.Bindings{AWSConns: cs}, base)
	if err != nil {
		t.Fatal(err)
	}
	rows := readAWSRows(t, filepath.Join(mounts[0].Host, AWSListFile))
	if rows["acme-prod-read"].BaseAccess != "writes" || rows["acme-stg-read"].BaseAccess != "unverified" ||
		rows["acme-stg-read"].BaseAccessReason != "not_checked" {
		t.Fatalf("aws.json rows %+v", rows)
	}
}

// WriteAWSListing rewrites aws.json in the dir BoxFor made, with new results.
func TestHooksWriteAWSListing(t *testing.T) {
	h := testHooks(t)
	bs := config.Bindings{AWSConns: twoConns()}
	mounts, _, err := h.BoxFor(bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]awsprobe.Result{"acme-stg-read": {State: awsprobe.NoneFound}}
	if err := h.WriteAWSListing(bs, base); err != nil {
		t.Fatal(err)
	}
	rows := readAWSRows(t, filepath.Join(mounts[0].Host, AWSListFile))
	if rows["acme-stg-read"].BaseAccess != "none_found" || rows["acme-prod-read"].BaseAccess != "unverified" {
		t.Fatalf("aws.json rows %+v", rows)
	}
}

func readAWSRows(t *testing.T, path string) map[string]awsprobe.Fields {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Name string `json:"name"`
		awsprobe.Fields
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("aws.json %s: %v", raw, err)
	}
	out := map[string]awsprobe.Fields{}
	for _, r := range rows {
		out[r.Name] = r.Fields
	}
	return out
}
