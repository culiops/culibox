package broker

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// Two spawns on one binding prepare its dir at the same time. Each must see a
// complete aws-config and client script, and neither may fail on the other's
// temp file (a shared temp name lost one writer's rename to ENOENT).
func TestEnsureBindingDirIsSafeConcurrently(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	const n = 32
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := EnsureBindingDir(root, b)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "broker", "binding-"+b.Hash())
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if want := []string{"agentbox-cred.js", "aws-config"}; !slices.Equal(names, want) {
		t.Fatalf("binding dir holds %q, want %q (no temp files left behind)", names, want)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "aws-config"))
	if !strings.HasSuffix(string(cfg), "region = ap-southeast-1\n") {
		t.Fatalf("aws-config truncated: %q", cfg)
	}
	if js, _ := os.ReadFile(filepath.Join(dir, "agentbox-cred.js")); !bytes.Equal(js, clientScript) {
		t.Fatal("client script truncated")
	}
}

// StripEnv is the one list of names a bound session must not inherit (R11.2):
// every static-credential route around credential_process, every name BoxEnv
// sets, and the token the session layer adds.
func TestStripEnvCoversEveryBypassAndEveryBrokerName(t *testing.T) {
	want := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_SHARED_CREDENTIALS_FILE",
		"AWS_CONFIG_FILE", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AGENTBOX_BROKER", "AGENTBOX_BROKER_TOKEN", "TF_CLI_ARGS_plan"}
	got := StripEnv()
	if !sameSet(got, want) {
		t.Fatalf("StripEnv = %q\nwant      %q", got, want)
	}
	for _, kv := range BoxEnv(*binding(), "unix:///x") {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(got, name) {
			t.Errorf("BoxEnv sets %s but StripEnv does not strip it", name)
		}
	}
	if !slices.Contains(got, TokenEnv) {
		t.Errorf("StripEnv misses %s", TokenEnv)
	}
	got[0] = "MUTATED"
	if StripEnv()[0] == "MUTATED" {
		t.Fatal("StripEnv must return a fresh slice")
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b) // a duplicate in either side fails too
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
