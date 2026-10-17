package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
)

const egressNonce = "0123456789abcdef0123456789abcdef"

func noSessions(string) (*session.Session, bool) { return nil, false }

// A docker that is not there: network ls fails, so the network is "absent" and
// boot must not treat that as fatal.
func absentLayer() *boxLayer { return &boxLayer{cli: box.CLIClient{Bin: "/bin/false"}} }

func TestStartEgressPersistsSecret(t *testing.T) {
	dir := t.TempDir()
	a, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	tok := a.Svc.Proxy().Token("claude-3", egressNonce)
	a.Stop(context.Background())

	fi, err := os.Stat(filepath.Join(dir, "egress.secret"))
	if err != nil || fi.Size() != 32 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("egress.secret: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.db")); err != nil {
		t.Fatalf("audit.db: %v", err)
	}

	b, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop(context.Background())
	if b.Svc.Proxy().Token("claude-3", egressNonce) != tok {
		t.Fatal("tokens changed across a restart: a reattached box would lose its proxy access")
	}
	if _, ok := b.Stats("claude-3"); ok {
		t.Error("Stats reports a session that never opened a tunnel")
	}
}

func TestStartEgressSecretIsNotTheBrokerSecret(t *testing.T) {
	dir := t.TempDir()
	if _, err := startBroker(dir, dir, "linux", 0, noSessions, func(string) (session.Info, bool) { return session.Info{}, false }); err != nil {
		t.Fatal(err)
	}
	e, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())
	b, _ := os.ReadFile(filepath.Join(dir, "broker.secret"))
	g, _ := os.ReadFile(filepath.Join(dir, "egress.secret"))
	if len(g) != 32 || string(b) == string(g) {
		t.Fatalf("egress secret must be its own 32 bytes (broker %d, egress %d, equal %v)", len(b), len(g), string(b) == string(g))
	}
}

func TestStartEgressRefusesABadSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "egress.secret"), 0o700); err != nil { // a directory: unreadable as a file
		t.Fatal(err)
	}
	if _, err := startEgress(dir, absentLayer(), config.Config{}, "linux", noSessions); err == nil || !strings.Contains(err.Error(), "egress secret") {
		t.Fatalf("err = %v, want an egress secret error", err)
	}
}

// darwin placeholder until Task 18: every bound spawn is refused (main leaves
// the hooks unset), and nothing is opened.
func TestStartEgressDarwinUnsupported(t *testing.T) {
	dir := t.TempDir()
	_, err := startEgress(dir, absentLayer(), config.Config{}, "darwin", noSessions)
	if !errors.Is(err, errEgressUnsupported) {
		t.Fatalf("err = %v, want errEgressUnsupported", err)
	}
	if !strings.Contains(err.Error(), "egress control for macOS boxes is not available yet") {
		t.Errorf("message %q must say plainly that macOS is not supported yet", err)
	}
	for _, f := range []string{"egress.secret", "audit.db"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s created for an unsupported platform", f)
		}
	}
}

func TestEgressLookup(t *testing.T) {
	sessions := map[string]*session.Session{
		"claude-3": {ID: "claude-3", Box: "agent-box-x", Runtime: config.RuntimeBox, AWS: wireBinding(), AWSNonce: egressNonce},
		"claude-4": {ID: "claude-4", Runtime: config.RuntimeHost, AWS: wireBinding(), AWSNonce: egressNonce},
	}
	look := egressLookup(func(id string) (*session.Session, bool) { s, ok := sessions[id]; return s, ok })

	v, ok := look("claude-3")
	if !ok || v.Nonce != egressNonce || v.Box != "agent-box-x" || !v.Boxed || !v.Live || v.AWS == nil {
		t.Fatalf("live boxed session: %+v %v", v, ok)
	}
	if v, ok := look("claude-4"); !ok || v.Boxed {
		t.Errorf("host-tier session must not read as boxed: %+v %v", v, ok)
	}
	// Left the registry: unknown, so the proxy refuses it (no stale admit).
	delete(sessions, "claude-3")
	if _, ok := look("claude-3"); ok {
		t.Error("a session that left the registry must be unknown")
	}
}

// fakeDocker writes a shell script standing in for the docker CLI. body runs
// with the arguments in "$@"; every call's argv (one line) is appended to log.
func fakeDockerScript(t *testing.T, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestStartEgressBootWithExistingNetworkDoesNotFail(t *testing.T) {
	// The network exists but is not internal: boot tries to bring the listener up
	// (spec correction 10), the network check refuses it, and boot only logs.
	bin, log := fakeDockerScript(t, `case "$1 $2" in
"network ls") echo agentbox-egress-X ;;
"network inspect") echo '{"Internal":false,"IPAM":{"Config":[{"Subnet":"172.30.0.0/16","Gateway":"172.30.0.1"}]}}' ;;
*) exit 1 ;;
esac`)
	l := &boxLayer{cli: box.CLIClient{Bin: bin}}
	svc, err := startEgress(t.TempDir(), l, config.Config{EnvLabel: "X"}, "linux", noSessions)
	if err != nil {
		t.Fatalf("boot must not fail on a bad existing network: %v", err)
	}
	defer svc.Stop(context.Background())
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "network inspect") {
		t.Errorf("boot never looked at the existing network: %q", b)
	}
	if strings.Contains(string(b), "network create") {
		t.Errorf("boot must not create the network (first bound spawn does): %q", b)
	}
}

func TestStartEgressBootWithoutNetworkCreatesNothing(t *testing.T) {
	bin, log := fakeDockerScript(t, `exit 0`) // network ls prints nothing: absent
	svc, err := startEgress(t.TempDir(), &boxLayer{cli: box.CLIClient{Bin: bin}}, config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "network create") {
		t.Errorf("boot created the network: %q", b)
	}
}

func TestEgressExecPassesTheCredentialOnStdinOnly(t *testing.T) {
	stdinFile := filepath.Join(t.TempDir(), "stdin")
	bin, log := fakeDockerScript(t, "cat > "+stdinFile+"\necho ok")
	l := &boxLayer{cli: box.CLIClient{Bin: bin}}
	out, err := l.egressExec("agent-box-x")(context.Background(), "SCRIPT-BODY", "user:pass-secret")
	if err != nil || out != "ok" {
		t.Fatalf("out %q err %v", out, err)
	}
	argv, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(argv)); got != "exec -i agent-box-x node -e SCRIPT-BODY" {
		t.Errorf("argv = %q", got)
	}
	if strings.Contains(string(argv), "pass-secret") {
		t.Error("credential leaked into argv")
	}
	if in, _ := os.ReadFile(stdinFile); string(in) != "user:pass-secret" {
		t.Errorf("stdin = %q", in)
	}
}

func TestEgressVerifyFailsClosedWhenTheProxyCannotStart(t *testing.T) {
	l := absentLayer() // docker always fails, so Ready fails
	svc, err := startEgress(t.TempDir(), l, config.Config{}, "linux", noSessions)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	err = l.egressVerify(svc.Svc)(context.Background(), "agent-box-x")
	if !errors.Is(err, box.ErrBoxUnavailable) {
		t.Fatalf("err = %v, want it to wrap box.ErrBoxUnavailable", err)
	}
}
