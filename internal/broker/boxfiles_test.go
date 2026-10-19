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

	"github.com/culiops/agent-box/internal/config"
)

func awsOnly(b config.AWSBinding) config.Bindings { return config.Bindings{AWS: &b} }

func ghTwoOwners() *config.GitHubBinding {
	return &config.GitHubBinding{Owners: []config.GitHubOwner{
		{Owner: "culiops", App: "a", Repos: []string{"agent-box"}}, {Owner: "CuliOps2", App: "a", Repos: []string{"agent-box"}}}}
}

func TestEnsureBindingDirWritesConfigAndClient(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	dir, err := EnsureBindingDir(root, awsOnly(b))
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
	if _, err := EnsureBindingDir(root, awsOnly(b)); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestEnsureBindingDirRefusesAnInvalidBinding(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	b.Region = "ap-southeast-1\n[profile evil]" // would inject a second INI section
	if _, err := EnsureBindingDir(root, awsOnly(b)); err == nil {
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
	if _, err := EnsureBindingDir(root, awsOnly(b)); err == nil {
		t.Fatal("followed a planted symlink out of the state root")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("wrote through the symlink")
	}
}

func TestBoxEnv(t *testing.T) {
	env := strings.Join(BoxEnv(awsOnly(*binding()), "unix:///opt/agentbox-sock/broker.sock"), "\n")
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
			_, err := EnsureBindingDir(root, awsOnly(b))
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
	for _, kv := range BoxEnv(awsOnly(*binding()), "unix:///x") {
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
	dir, err := EnsureBindingDir(t.TempDir(), awsOnly(*binding()))
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

// The AWS-only env is pinned byte for byte: existing sessions must not change.
func TestBoxEnvAWSOnlyUnchanged(t *testing.T) {
	got := BoxEnv(awsOnly(*binding()), "unix:///opt/agentbox-sock/broker.sock")
	want := []string{
		"AWS_CONFIG_FILE=/opt/agentbox/aws-config",
		"AWS_PROFILE=agentbox",
		"AWS_REGION=ap-southeast-1",
		"AWS_DEFAULT_REGION=ap-southeast-1",
		"AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock",
		"TF_CLI_ARGS_plan=-lock=false",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("BoxEnv = %q\nwant    %q", got, want)
	}
}

func TestEnsureBindingDirAWSOnlySamePath(t *testing.T) {
	root := t.TempDir()
	b := *binding()
	dir, err := EnsureBindingDir(root, awsOnly(b))
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "broker", "binding-"+b.Hash()) {
		t.Fatalf("dir %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "agentbox-gh.js")); err == nil {
		t.Fatal("an AWS-only binding got the gh wrapper")
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "aws-config"))
	if err != nil || !strings.Contains(string(cfg), "credential_process = node /opt/agentbox/agentbox-cred.js\n") {
		t.Fatalf("aws-config %q %v", cfg, err)
	}
}

func TestEnsureBindingDirGitHubOnly(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{GitHub: ghTwoOwners()}
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "broker", "binding-"+bs.Hash()) {
		t.Fatalf("dir %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "aws-config")); err == nil {
		t.Fatal("a GitHub-only binding got an aws-config")
	}
	for name, want := range map[string][]byte{"agentbox-cred.js": clientScript, "agentbox-gh.js": ghScript} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestEnsureBindingDirBothBindings(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWS: binding(), GitHub: ghTwoOwners()}
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if dir == filepath.Join(root, "broker", "binding-"+binding().Hash()) {
		t.Fatal("a GitHub binding must change the binding dir")
	}
	for _, n := range []string{"aws-config", "agentbox-cred.js", "agentbox-gh.js"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("missing %s", n)
		}
	}
}

func TestEnsureBindingDirRefusesNoBindingAndInvalidGitHub(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureBindingDir(root, config.Bindings{}); err == nil {
		t.Fatal("prepared a dir for no binding")
	}
	bad := config.Bindings{GitHub: &config.GitHubBinding{}}
	if _, err := EnsureBindingDir(root, bad); err == nil {
		t.Fatal("accepted an invalid GitHub binding")
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Fatal("created files for an invalid binding")
	}
}

func TestBoxEnvGitHub(t *testing.T) {
	got := BoxEnv(config.Bindings{GitHub: ghTwoOwners()}, "unix:///opt/agentbox-sock/broker.sock")
	want := []string{
		"AGENTBOX_BROKER=unix:///opt/agentbox-sock/broker.sock",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0=!node /opt/agentbox/agentbox-cred.js github-git",
		"GIT_CONFIG_KEY_1=credential.https://github.com.useHttpPath",
		"GIT_CONFIG_VALUE_1=true",
		"GIT_TERMINAL_PROMPT=0",
		"GH_CONFIG_DIR=/tmp/agentbox-gh",
		"GH_PROMPT_DISABLED=1",
		"AGENTBOX_GH_OWNERS=culiops,CuliOps2",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("BoxEnv = %q\nwant    %q", got, want)
	}
	if !slices.Equal(GitHubEnv(*ghTwoOwners()), want[1:]) {
		t.Fatal("GitHubEnv differs from the tail of BoxEnv")
	}
}

func TestBoxEnvBothBindings(t *testing.T) {
	got := BoxEnv(config.Bindings{AWS: binding(), GitHub: ghTwoOwners()}, "unix:///x")
	aws := BoxEnv(awsOnly(*binding()), "unix:///x")
	if !slices.Equal(got[:len(aws)], aws) || !slices.Equal(got[len(aws):], GitHubEnv(*ghTwoOwners())) {
		t.Fatalf("BoxEnv = %q", got)
	}
}

func TestStripGitHubEnvCoversGitHub(t *testing.T) {
	got := StripGitHubEnv()
	for _, n := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GH_CONFIG_DIR", "GH_REPO", "GH_HOST", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1", "AGENTBOX_GH_OWNERS",
		"GIT_CONFIG_PARAMETERS", "GIT_ASKPASS", "SSH_ASKPASS"} {
		if !slices.Contains(got, n) {
			t.Errorf("StripGitHubEnv misses %s", n)
		}
	}
	// Every name a GitHub-bound box gets from the broker is stripped first,
	// by StripEnv and StripGitHubEnv together.
	all := slices.Concat(StripEnv(), got)
	for _, kv := range BoxEnv(config.Bindings{GitHub: ghTwoOwners()}, "unix:///x") {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(all, name) {
			t.Errorf("BoxEnv sets %s for a GitHub binding but nothing strips it", name)
		}
	}
	// And StripEnv alone leaves a profile's own GitHub variables alone.
	for _, n := range got {
		if slices.Contains(StripEnv(), n) {
			t.Errorf("StripEnv strips %s: an AWS-only session would lose it", n)
		}
	}
	if len(got) > 0 {
		got[0] = "MUTATED"
		if StripGitHubEnv()[0] == "MUTATED" {
			t.Fatal("StripGitHubEnv must return a fresh slice")
		}
	}
}
