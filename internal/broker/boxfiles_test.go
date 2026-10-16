package broker

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBindingDirWritesConfigAndClient(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	dir, err := EnsureBindingDir(root, b)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "broker", "binding-"+b.Hash()) {
		t.Fatalf("dir %s", dir)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "aws-config"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[profile agentbox]\ncredential_process = node /opt/agentbox/agentbox-cred.js\nregion = ap-southeast-1\n"
	if string(cfg) != want {
		t.Fatalf("aws-config:\n%s", cfg)
	}
	js, err := os.ReadFile(filepath.Join(dir, "agentbox-cred.js"))
	if err != nil || !strings.Contains(string(js), "/v1/credentials") {
		t.Fatalf("client script missing: %v", err)
	}
	if _, err := EnsureBindingDir(root, b); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestEnsureBindingDirRefusesAnInvalidBinding(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	b.Region = "ap-southeast-1\n[profile evil]" // would inject a second INI section
	if _, err := EnsureBindingDir(root, b); err == nil {
		t.Fatal("accepted a region that is not a region")
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Fatal("created files for an invalid binding")
	}
}

func TestEnsureBindingDirRefusesAPlantedSymlink(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	if err := os.MkdirAll(filepath.Join(root, "broker"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "broker", "binding-"+b.Hash())); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBindingDir(root, b); err == nil {
		t.Fatal("followed a planted symlink out of the state root")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("wrote through the symlink")
	}
}

func TestBoxEnv(t *testing.T) {
	env := strings.Join(BoxEnv(*binding(), "unix:///opt/agentbox-sock/broker.sock"), "\n")
	for _, kv := range []string{"AWS_CONFIG_FILE=/opt/agentbox/aws-config", "AWS_PROFILE=agentbox",
		"AWS_REGION=ap-southeast-1", "AWS_DEFAULT_REGION=ap-southeast-1", "TF_CLI_ARGS_plan=-lock=false",
		"AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock"} {
		if !strings.Contains(env, kv) {
			t.Errorf("missing %s", kv)
		}
	}
	if strings.Contains(env, "AGENTBOX_BROKER_TOKEN") {
		t.Fatal("BoxEnv must not carry the token (the session layer adds it per session)")
	}
}

// The client against a real unix-socket broker, end to end.
func TestClientScriptAgainstBroker(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	b, _, _, _ := setup(t, map[string]SessionView{"claude-3": live()})
	sock := filepath.Join(t.TempDir(), "b.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: b.Handler()}
	go srv.Serve(l)
	defer srv.Shutdown(context.Background())
	dir, err := EnsureBindingDir(t.TempDir(), *binding())
	if err != nil {
		t.Fatal(err)
	}

	run := func(env ...string) (string, string, error) {
		cmd := exec.Command("node", filepath.Join(dir, "agentbox-cred.js"))
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
	out, _, err := run("AGENTBOX_BROKER=unix://"+sock, "AGENTBOX_BROKER_TOKEN="+tokA("claude-3"))
	if err != nil || !strings.Contains(out, `"AccessKeyId":"ASIAFAKE"`) {
		t.Fatalf("good token: %v %s", err, out)
	}
	_, stderr, err := run("AGENTBOX_BROKER=unix://"+sock, "AGENTBOX_BROKER_TOKEN=garbage")
	if err == nil || stderr != "agentbox: not an agent-box session\n" {
		t.Fatalf("bad token: %v %q", err, stderr)
	}
	if strings.Contains(stderr, "garbage") {
		t.Fatal("the token was printed")
	}
	out, stderr, err = run("AGENTBOX_BROKER=unix://"+filepath.Join(t.TempDir(), "none.sock"), "AGENTBOX_BROKER_TOKEN=tok-secret-xyz")
	if err == nil || out != "" || !strings.HasPrefix(stderr, "agentbox: agent-box broker unreachable") || strings.Contains(stderr, "tok-secret-xyz") {
		t.Fatalf("unreachable: %v out=%q stderr=%q", err, out, stderr)
	}
	_, stderr, err = run()
	if err == nil || !strings.Contains(stderr, "not an agent-box AWS session") {
		t.Fatalf("unset env: %v %q", err, stderr)
	}
}
