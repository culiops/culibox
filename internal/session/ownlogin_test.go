package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// ownLoginHarness is the harness the host_own_login tests run on. It is
// newDropsHarness today; a later task makes it install the fake audit.
func ownLoginHarness(t *testing.T, l dropsLauncher) *dropsHarness {
	t.Helper()
	return newDropsHarness(t, l)
}

func ownSpec(t *testing.T, kinds ...string) launcher.SpawnSpec {
	spec := hostAckSpec(t)
	spec.HostOwnLogin = kinds
	return spec
}

func TestHostOwnLoginGitHubDropsOnlyGitHub(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.GitHub != nil || s.PATs != nil || s.AWS == nil || len(s.Tokens) == 0 || !s.HostBroker {
		t.Fatalf("session: github=%v pats=%v aws=%v tokens=%d host_broker=%v", s.GitHub, s.PATs, s.AWS, len(s.Tokens), s.HostBroker)
	}
	calls, _, _ := h.hooks.hostState()
	if len(calls) != 1 || calls[0].bs.GitHub != nil || calls[0].bs.AWS == nil || len(calls[0].bs.PATs) != 0 {
		t.Fatalf("HostFor calls = %+v, want one without GitHub and with AWS", calls)
	}
	env := envOfHostProcessKV(t, s)
	for _, gone := range []string{"GH_CONFIG_DIR", "GIT_CONFIG_COUNT", "AGENTBOX_GH_OWNERS"} {
		if v, ok := env[gone]; ok {
			t.Fatalf("%s=%q reached a session whose GitHub is on the machine login", gone, v)
		}
	}
	if dir := h.hooks.hostDirFor(s.ID); !strings.HasPrefix(env["AWS_CONFIG_FILE"], dir) {
		t.Fatalf("AWS_CONFIG_FILE = %q, want under %s", env["AWS_CONFIG_FILE"], dir)
	}
}

func TestHostOwnLoginAWSDropsOnlyAWS(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.AWS != nil || len(s.AWSConns) != 0 || s.GitHub == nil {
		t.Fatalf("session: aws=%v conns=%d github=%v", s.AWS, len(s.AWSConns), s.GitHub)
	}
	if in := s.Info(); in.AWS != nil || len(in.AWSConnections) != 0 {
		t.Fatalf("Info shows AWS: %+v %+v", in.AWS, in.AWSConnections)
	}
	env := envOfHostProcessKV(t, s)
	if env["GH_CONFIG_DIR"] == "" {
		t.Fatal("GH_CONFIG_DIR missing: GitHub should still go through the broker")
	}
	for _, gone := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		if v, ok := env[gone]; ok {
			t.Fatalf("%s=%q set by CuliBox for a session whose AWS is on the machine login", gone, v)
		}
	}
}

func TestHostOwnLoginAllKindsIsAPlainHostRun(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.noTokens = true
	h := ownLoginHarness(t, l)
	h.m.SetBroker(nil)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.HostBroker || s.Bindings().Any() {
		t.Fatalf("not a plain host run: host_broker=%v bindings=%+v", s.HostBroker, s.Bindings())
	}
	if calls, _, _ := h.hooks.hostState(); len(calls) != 0 {
		t.Fatalf("HostFor called for a plain host run: %+v", calls)
	}
}

func TestHostOwnLoginUnboundKindRefused(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	l.noAWS = true
	h := ownLoginHarness(t, l)
	_, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	want := `profile "p": host_own_login names aws, which this profile does not bind`
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if n := len(h.m.List()); n != 0 {
		t.Fatalf("%d sessions after a refused spawn", n)
	}
}

func TestHostOwnLoginUnknownKindRefusedInSpawn(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv)
	h := ownLoginHarness(t, l)
	_, err := h.m.Spawn(ownSpec(t, "gitlab"))
	if err == nil || !errors.Is(err, box.ErrUnboxableWorkspace) || !strings.Contains(err.Error(), `"gitlab"`) {
		t.Fatalf("err = %v, want a refusal naming gitlab", err)
	}
	h.untouched(t)
}

func TestBoxSpawnIgnoresHostOwnLogin(t *testing.T) {
	l := boundDropsLauncher([]string{"sh", "-c", "sleep 30"}, config.RuntimeBox)
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(launcher.SpawnSpec{Agent: "p", Cwd: t.TempDir(), HostDropsAck: true,
		HostOwnLogin: []string{launcher.OwnLoginGitHub, launcher.OwnLoginAWS}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	if s.Runtime != config.RuntimeBox || s.GitHub == nil || (s.AWS == nil && len(s.AWSConns) == 0) {
		t.Fatalf("a box spawn lost a binding: runtime=%s github=%v aws=%v", s.Runtime, s.GitHub, s.AWS)
	}
}

func TestHostOwnLoginAWSKeepsTheUsersAWSEnv(t *testing.T) {
	l, _ := hostBoundLauncher(t, envDumpArgv, "AWS_PROFILE=mine", "AWS_ACCESS_KEY_ID=AKIAEXAMPLEONLY")
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginAWS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	if env["AWS_PROFILE"] != "mine" || env["AWS_ACCESS_KEY_ID"] != "AKIAEXAMPLEONLY" {
		t.Fatalf("the user's AWS env was stripped: AWS_PROFILE=%q AWS_ACCESS_KEY_ID=%q", env["AWS_PROFILE"], env["AWS_ACCESS_KEY_ID"])
	}
	if v, ok := env["AWS_CONFIG_FILE"]; ok {
		t.Fatalf("AWS_CONFIG_FILE=%q set by CuliBox for a session whose AWS is on the machine login", v)
	}
}

func TestHostOwnLoginGitHubKeepsTheUsersGitHubEnvAndTokens(t *testing.T) {
	l, tools := hostBoundLauncher(t, envDumpArgv, "GH_TOKEN=user-own", "GH_CONFIG_DIR=/home/u/.config/gh")
	h := ownLoginHarness(t, l)
	s, err := h.m.Spawn(ownSpec(t, launcher.OwnLoginGitHub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.m.Kill(s.ID) })
	env := envOfHostProcessKV(t, s)
	if env["GH_TOKEN"] != "user-own" || env["GH_CONFIG_DIR"] != "/home/u/.config/gh" {
		t.Fatalf("the user's GitHub env was stripped: GH_TOKEN=%q GH_CONFIG_DIR=%q", env["GH_TOKEN"], env["GH_CONFIG_DIR"])
	}
	if v, ok := env["GIT_CONFIG_COUNT"]; ok {
		t.Fatalf("GIT_CONFIG_COUNT=%q set for a session whose GitHub is on the machine login", v)
	}
	dir := h.hooks.hostDirFor(s.ID)
	if env["AGENTBOX_BROKER"] == "" || !strings.HasPrefix(env["PATH"], dir+"/bin") {
		t.Fatalf("the token binding was not delivered: AGENTBOX_BROKER=%q PATH=%q", env["AGENTBOX_BROKER"], env["PATH"])
	}
	_ = tools
	if _, err := os.Stat(filepath.Join(dir, "bin", "gh")); err == nil {
		t.Fatal("a gh shim was written for a session whose GitHub is on the machine login")
	}
}
