package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
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
	// The listener is testBox's own, as a per-box socket is.
	srv := &http.Server{Handler: b.Handler(), BaseContext: BaseContext}
	go srv.Serve(&boxListener{Listener: l, box: testBox})
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
	if err == nil || stderr != "agentbox: not a CuliBox session\n" {
		t.Fatalf("bad token: %v %q", err, stderr)
	}
	if strings.Contains(stderr, "garbage") {
		t.Fatal("the token was printed")
	}
	out, stderr, err = run("AGENTBOX_BROKER=unix://"+filepath.Join(t.TempDir(), "none.sock"), "AGENTBOX_BROKER_TOKEN=tok-secret-xyz")
	if err == nil || out != "" || !strings.HasPrefix(stderr, "agentbox: CuliBox broker unreachable") || strings.Contains(stderr, "tok-secret-xyz") {
		t.Fatalf("unreachable: %v out=%q stderr=%q", err, out, stderr)
	}
	_, stderr, err = run()
	if err == nil || !strings.Contains(stderr, "not a CuliBox AWS session") {
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
		"GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1", "GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2", "AGENTBOX_GH_OWNERS",
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

// config.TokenEnvDenied keeps its own copy of StaticCredEnv (config cannot
// import broker); this keeps the two in step.
func TestTokenEnvDeniedCoversStripEnv(t *testing.T) {
	for _, n := range StaticCredEnv {
		if !config.TokenEnvDenied(n) {
			t.Errorf("config.TokenEnvDenied(%q) = false; add it to the deny-list in config/statictoken.go", n)
		}
	}
	for _, n := range []string{"AWS_CONFIG_FILE", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", TokenEnv} {
		if !config.TokenEnvDenied(n) {
			t.Errorf("config.TokenEnvDenied(%q) = false", n)
		}
	}
}

func tokenEntries() []config.StaticToken {
	return []config.StaticToken{
		{Name: "grafana-cloud", Provider: "grafana", Label: "Grafana", Prod: true,
			Value:     config.KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"},
			Env:       []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"},
			ConfigEnv: map[string]string{"GRAFANA_URL": "https://acme.grafana.net"},
			Egress:    []config.TokenEgress{{Host: "acme.grafana.net"}, {Host: "a.corp.example", Port: 8443}, {Host: "b.corp.example", Port: 443}},
			MCP:       &config.TokenMCP{Server: "grafana", Command: "mcp-grafana"}},
		{Name: "atlassian", Value: config.KeyRef{Type: "file", Path: "/home/u/.config/agent-box/atlassian.token"},
			Env: []string{"JIRA_API_TOKEN"}, Egress: []config.TokenEgress{{Host: "acme.atlassian.net"}}},
	}
}

func fileModes(t *testing.T, dir string) map[string]os.FileMode {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]os.FileMode{}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = fi.Mode().Perm()
	}
	return m
}

func TestEnsureBindingDirTokens(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{Tokens: tokenEntries()}
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "broker", "binding-"+bs.Hash()) {
		t.Fatalf("dir %s", dir)
	}
	modes := fileModes(t, dir)
	for _, n := range []string{"agentbox-cred.js", "agentbox-env.js", TokensFile} {
		if m, ok := modes[n]; !ok || m != 0o600 {
			t.Errorf("%s: present=%v mode=%v", n, ok, m)
		}
	}
	if _, ok := modes["aws-config"]; ok {
		t.Error("a tokens-only binding got an aws-config")
	}
	if _, ok := modes["agentbox-gh.js"]; ok {
		t.Error("a tokens-only binding got the gh wrapper")
	}
	js, err := os.ReadFile(filepath.Join(dir, "agentbox-env.js"))
	if err != nil || !bytes.Equal(js, envScript) {
		t.Fatalf("agentbox-env.js: %v", err)
	}
	listing, err := os.ReadFile(filepath.Join(dir, TokensFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"GRAFANA_SA_TOKEN", "/home/u/.config", "atlassian.token", `"value"`, `"var"`, `"path"`} {
		if strings.Contains(string(listing), bad) {
			t.Errorf("tokens.json contains %q: %s", bad, listing)
		}
	}
	if !strings.Contains(string(listing), "https://acme.grafana.net") {
		t.Errorf("tokens.json lacks the config value: %s", listing)
	}
	var got []map[string]any
	if err := json.Unmarshal(listing, &got); err != nil || len(got) != 2 {
		t.Fatalf("tokens.json: %v %s", err, listing)
	}
	if got[0]["name"] != "grafana-cloud" || got[0]["label"] != "Grafana" || got[0]["prod"] != true ||
		got[0]["mcp_server"] != "grafana" || got[1]["name"] != "atlassian" {
		t.Fatalf("entry 0: %v", got[0])
	}
	eg := got[0]["egress"].([]any)
	if len(eg) != 3 {
		t.Fatalf("egress %v", eg)
	}
	if _, has := eg[0].(map[string]any)["port"]; has {
		t.Errorf("port 443 listed: %v", eg[0])
	}
	if eg[1].(map[string]any)["port"] != float64(8443) {
		t.Errorf("port 8443 missing: %v", eg[1])
	}
	if _, has := eg[2].(map[string]any)["port"]; has {
		t.Errorf("explicit 443 listed: %v", eg[2])
	}
	if _, err := EnsureBindingDir(root, bs); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestEnsureBindingDirTokensRefusesInvalidEntry(t *testing.T) {
	root := t.TempDir()
	bad := tokenEntries()
	bad[0].Env = []string{"PATH"}
	if _, err := EnsureBindingDir(root, config.Bindings{Tokens: bad}); err == nil {
		t.Fatal("accepted an entry that carries a token in PATH")
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Fatal("created files for an invalid entry")
	}
}

// A PAT-only GitHub binding goes through the gh/git helpers; it gets no env wrapper.
func TestEnsureBindingDirPATsOnlyNoEnvFiles(t *testing.T) {
	dir, err := EnsureBindingDir(t.TempDir(), config.Bindings{GitHub: ghTwoOwners(), PATs: tokenEntries()[1:]})
	if err != nil {
		t.Fatal(err)
	}
	for n := range fileModes(t, dir) {
		if n == "agentbox-env.js" || n == TokensFile {
			t.Errorf("PAT-only binding got %s", n)
		}
	}
}

func TestEnsureBindingDirNoTokensUnchanged(t *testing.T) {
	for name, bs := range map[string]config.Bindings{
		"aws":    awsOnly(*binding()),
		"github": {GitHub: ghTwoOwners()},
	} {
		dir, err := EnsureBindingDir(t.TempDir(), bs)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"agentbox-cred.js"}
		if bs.AWS != nil {
			want = append(want, "aws-config")
		}
		if bs.GitHub != nil {
			want = append(want, "agentbox-gh.js")
		}
		var got []string
		for n := range fileModes(t, dir) {
			got = append(got, n)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: files %v, want %v", name, got, want)
		}
	}
}

func TestTokensListingShape(t *testing.T) {
	if got := string(TokensListing(nil)); got != "[]\n" {
		t.Fatalf("empty listing %q", got)
	}
	got := string(TokensListing(tokenEntries()[1:]))
	want := `[{"name":"atlassian","env":["JIRA_API_TOKEN"],"egress":[{"host":"acme.atlassian.net"}]}]` + "\n"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A private entry says so, and never leaks its cidr.
	p := string(TokensListing([]config.StaticToken{{Name: "x", Env: []string{"X"},
		Egress: []config.TokenEgress{{Host: "h.corp.example", Private: true, CIDR: "10.0.0.0/8"}}}}))
	if !strings.Contains(p, `"private":true`) || strings.Contains(p, "10.0.0.0") {
		t.Fatalf("private entry: %s", p)
	}
}

func TestWriteSessionMCP(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{Tokens: tokenEntries()}
	boxPath, err := WriteSessionMCP(root, bs, "sess-1", []byte(`{"mcpServers":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if boxPath != BoxDir+"/mcp-sess-1.json" {
		t.Fatalf("box path %s", boxPath)
	}
	p := filepath.Join(root, "broker", "binding-"+bs.Hash(), "mcp-sess-1.json")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"mcpServers":{}}` {
		t.Fatalf("content %q", b)
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil || !fi.IsDir() {
		t.Fatalf("binding dir was not ensured: %v", err)
	}
	if _, err := WriteSessionMCP(root, bs, "sess-1", []byte(`{"v":2}`)); err != nil { // replaces
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"v":2}` {
		t.Fatalf("not replaced: %q", b)
	}
}

func TestWriteSessionMCPRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{Tokens: tokenEntries()}
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "mcp-k.json")); err != nil {
		t.Fatal(err)
	}
	_, _ = WriteSessionMCP(root, bs, "k", []byte("replaced"))
	if b, _ := os.ReadFile(target); string(b) != "original" {
		t.Fatalf("wrote through the planted symlink: %q", b)
	}
	// A symlink planted in place of the binding dir itself is refused outright.
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, "broker"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root2, "broker", "binding-"+bs.Hash())); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSessionMCP(root2, bs, "k", []byte("x")); err == nil {
		t.Fatal("followed a planted binding-dir symlink")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("wrote through the dir symlink")
	}
}

func TestWriteSessionMCPBadKey(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{Tokens: tokenEntries()}
	for _, k := range []string{"../x", "", "A", "a_b", "a.b", "a/b", strings.Repeat("a", 41)} {
		if _, err := WriteSessionMCP(root, bs, k, []byte("x")); err == nil {
			t.Errorf("accepted key %q", k)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Error("a bad key still created the binding dir")
	}
	if _, err := WriteSessionMCP(root, bs, strings.Repeat("a", 40), []byte("x")); err != nil {
		t.Errorf("rejected a 40-char key: %v", err)
	}
}

func TestHooksWriteSessionMCP(t *testing.T) {
	root := t.TempDir()
	h := Hooks{StateRoot: root}
	p, err := h.WriteSessionMCP(config.Bindings{Tokens: tokenEntries()}, "k1", []byte("{}"))
	if err != nil || p != BoxDir+"/mcp-k1.json" {
		t.Fatalf("%q %v", p, err)
	}
}

// The image's agentbox-env shim execs the script at a path the Dockerfile writes
// by hand; the binding dir is mounted at BoxDir. Keep the two from drifting.
func TestImageShimPathIsTheMountedScript(t *testing.T) {
	df, err := os.ReadFile("../../docker/agent-box/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	want := "[ -f " + BoxDir + "/agentbox-env.js ] && exec node " + BoxDir + "/agentbox-env.js"
	if !strings.Contains(string(df), want) {
		t.Errorf("the Dockerfile's agentbox-env shim does not run %s/agentbox-env.js (want %q)", BoxDir, want)
	}
	if _, err := os.Stat("agentbox-env.js"); err != nil {
		t.Errorf("the script the box mounts as agentbox-env.js is not in this package: %v", err)
	}
}

// --- #155 task 7: several named AWS connections ---

func twoConns() []config.AWSConnection {
	return []config.AWSConnection{
		{Name: "acme-prod-read", Description: "Prod, read only", AWSBinding: config.AWSBinding{SourceProfile: "sso-acme",
			RoleARN: "arn:aws:iam::111111111111:role/agent-read", Region: "eu-west-1", Label: "Acme prod", Prod: true,
			Elevate: &config.AWSElevate{RoleARN: "arn:aws:iam::111111111111:role/agent-write"}}},
		{Name: "acme-stg-read", AWSBinding: config.AWSBinding{SourceProfile: "sso-acme",
			RoleARN: "arn:aws:iam::222222222222:role/agent-read", Region: "eu-west-1"}},
	}
}

func TestAWSConfigForGolden(t *testing.T) {
	want := "[default]\ncredential_process = node /opt/agentbox/agentbox-cred.js aws\n\n" +
		"[profile acme-prod-read]\ncredential_process = node /opt/agentbox/agentbox-cred.js aws acme-prod-read\nregion = eu-west-1\n\n" +
		"[profile acme-stg-read]\ncredential_process = node /opt/agentbox/agentbox-cred.js aws acme-stg-read\nregion = eu-west-1\n"
	cs := twoConns()
	if got := string(AWSConfigFor(cs)); got != want {
		t.Fatalf("aws-config:\n%q\nwant\n%q", got, want)
	}
	// Order of the input does not matter: profiles come out sorted by name.
	cs[0], cs[1] = cs[1], cs[0]
	if got := string(AWSConfigFor(cs)); got != want {
		t.Fatalf("unsorted input changed the output:\n%q", got)
	}
}

func envHas(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestBoxEnvConnsOne(t *testing.T) {
	env := BoxEnv(config.Bindings{AWSConns: twoConns()[:1]}, "unix:///x")
	for k, want := range map[string]string{"AWS_CONFIG_FILE": "/opt/agentbox/aws-config", "AWS_PROFILE": "acme-prod-read",
		"AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "eu-west-1", "TF_CLI_ARGS_plan": "-lock=false", "AGENTBOX_BROKER": "unix:///x"} {
		if v, ok := envHas(env, k); !ok || v != want {
			t.Errorf("%s = %q (set %v), want %q", k, v, ok, want)
		}
	}
}

func TestBoxEnvConnsMixedRegions(t *testing.T) {
	cs := twoConns()
	cs[1].Region = "us-east-1"
	env := BoxEnv(config.Bindings{AWSConns: cs}, "unix:///x")
	for _, k := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v, ok := envHas(env, k); ok {
			t.Errorf("%s=%q must not be set", k, v)
		}
	}
	if v, ok := envHas(env, "AWS_CONFIG_FILE"); !ok || v != "/opt/agentbox/aws-config" {
		t.Errorf("AWS_CONFIG_FILE = %q", v)
	}
}

func TestBoxEnvConnsSameRegion(t *testing.T) {
	env := BoxEnv(config.Bindings{AWSConns: twoConns()}, "unix:///x")
	if v, ok := envHas(env, "AWS_REGION"); !ok || v != "eu-west-1" {
		t.Errorf("AWS_REGION = %q", v)
	}
	if v, ok := envHas(env, "AWS_DEFAULT_REGION"); !ok || v != "eu-west-1" {
		t.Errorf("AWS_DEFAULT_REGION = %q", v)
	}
	if v, ok := envHas(env, "AWS_PROFILE"); ok {
		t.Errorf("AWS_PROFILE=%q must not be set with two connections", v)
	}
	for _, kv := range env { // nothing AWS_ beyond the three, and nothing from StripEnv's static routes
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "AWS_") && !slices.Contains([]string{"AWS_CONFIG_FILE", "AWS_REGION", "AWS_DEFAULT_REGION"}, k) {
			t.Errorf("unexpected %s", kv)
		}
	}
}

// The inline-binding box files and env are pinned to literals captured from
// the code before connections existed (HEAD 25d6cf9).
func TestLegacyBindingDirUnchanged(t *testing.T) {
	dir, err := EnsureBindingDir(t.TempDir(), awsOnly(*binding()))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "aws-config"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[profile agentbox]\ncredential_process = node /opt/agentbox/agentbox-cred.js\nregion = ap-southeast-1\n"; string(cfg) != want {
		t.Fatalf("aws-config = %q", cfg)
	}
	var files []string
	for n := range fileModes(t, dir) {
		files = append(files, n)
	}
	slices.Sort(files)
	if !slices.Equal(files, []string{"agentbox-cred.js", "aws-config"}) {
		t.Fatalf("files = %v", files)
	}
	want := []string{"AWS_CONFIG_FILE=/opt/agentbox/aws-config", "AWS_PROFILE=agentbox", "AWS_REGION=ap-southeast-1",
		"AWS_DEFAULT_REGION=ap-southeast-1", "AGENTBOX_BROKER=unix:///x", "TF_CLI_ARGS_plan=-lock=false"}
	if got := BoxEnv(awsOnly(*binding()), "unix:///x"); !slices.Equal(got, want) {
		t.Fatalf("BoxEnv = %q", got)
	}
	wantGH := append(slices.Clone(want), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0=!node /opt/agentbox/agentbox-cred.js github-git", "GIT_CONFIG_KEY_1=credential.https://github.com.useHttpPath",
		"GIT_CONFIG_VALUE_1=true", "GIT_TERMINAL_PROMPT=0", "GH_CONFIG_DIR=/tmp/agentbox-gh", "GH_PROMPT_DISABLED=1",
		"AGENTBOX_GH_OWNERS=culiops,CuliOps2")
	if got := BoxEnv(config.Bindings{AWS: binding(), GitHub: ghTwoOwners()}, "unix:///x"); !slices.Equal(got, wantGH) {
		t.Fatalf("BoxEnv with GitHub = %q", got)
	}
}

func TestEnsureBindingDirConnsWritesEnvScript(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWSConns: twoConns()}
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "broker", "binding-"+bs.Hash()) {
		t.Fatalf("dir %s", dir)
	}
	modes := fileModes(t, dir)
	var got []string
	for n := range modes {
		got = append(got, n)
	}
	slices.Sort(got)
	want := []string{"agentbox-cred.js", "agentbox-env.js", "aws-config", "aws.json"}
	if !slices.Equal(got, want) { // tokens.json only with tokens
		t.Fatalf("files %v, want %v", got, want)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "aws-config"))
	if !bytes.Equal(cfg, AWSConfigFor(bs.AWSConns)) {
		t.Fatalf("aws-config %q", cfg)
	}
	list, _ := os.ReadFile(filepath.Join(dir, AWSListFile))
	if !bytes.Equal(list, AWSListing(bs.AWSConns, nil)) {
		t.Fatalf("aws.json %q", list)
	}
	// With tokens as well, tokens.json joins them.
	bs.Tokens = tokenEntries()
	dir2, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fileModes(t, dir2)[TokensFile]; !ok {
		t.Fatal("tokens.json missing next to connections")
	}
}

func TestEnsureBindingDirConnsRevalidates(t *testing.T) {
	root := t.TempDir()
	for name, mut := range map[string]func(*config.AWSConnection){
		"region injection": func(c *config.AWSConnection) { c.Region = "eu-west-1\n[profile evil]" },
		"name injection":   func(c *config.AWSConnection) { c.Name = "a\n[profile evil]" },
		"reserved name":    func(c *config.AWSConnection) { c.Name = "default" },
	} {
		cs := twoConns()
		mut(&cs[0])
		if _, err := EnsureBindingDir(root, config.Bindings{AWSConns: cs}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Fatal("created files for an invalid connection")
	}
}

func TestAWSListingNoSourceProfile(t *testing.T) {
	b := AWSListing(twoConns(), nil)
	if strings.Contains(string(b), "source_profile") || strings.Contains(string(b), "sso-acme") {
		t.Fatalf("listing leaks the source profile: %s", b)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["name"] != "acme-prod-read" || rows[0]["account_id"] != "111111111111" ||
		rows[0]["role_arn"] != "arn:aws:iam::111111111111:role/agent-read" || rows[0]["region"] != "eu-west-1" ||
		rows[0]["prod"] != true || rows[0]["write_capable"] != true || rows[0]["label"] != "Acme prod" ||
		rows[0]["description"] != "Prod, read only" {
		t.Fatalf("row 0 = %v", rows[0])
	}
	if rows[1]["write_capable"] != false || rows[1]["prod"] != false {
		t.Fatalf("row 1 = %v", rows[1])
	}
	if _, ok := rows[1]["label"]; ok {
		t.Fatalf("empty label should be omitted: %v", rows[1])
	}
	for _, k := range []string{"s3_read", "egress_allow", "elevate"} {
		if strings.Contains(string(b), k) {
			t.Errorf("listing carries %s", k)
		}
	}
}

func baseResults() map[string]awsprobe.Result {
	at := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	return map[string]awsprobe.Result{
		"acme-prod-read": {State: awsprobe.Writes, Actions: []string{"s3:PutObject"}, CheckedAt: at},
	}
}

func TestAWSListingBaseFields(t *testing.T) {
	var rows []map[string]any
	if err := json.Unmarshal(AWSListing(twoConns(), baseResults()), &rows); err != nil {
		t.Fatal(err)
	}
	acts, _ := rows[0]["base_write_actions"].([]any)
	if rows[0]["base_access"] != "writes" || len(acts) != 1 || acts[0] != "s3:PutObject" ||
		rows[0]["base_checked_at"] != "2026-10-20T09:30:00Z" {
		t.Fatalf("row 0 = %v", rows[0])
	}
	if rows[1]["base_access"] != "unverified" || rows[1]["base_access_reason"] != "not_checked" {
		t.Fatalf("row 1 = %v", rows[1])
	}
	if _, ok := rows[1]["base_write_actions"]; ok {
		t.Fatalf("row 1 has actions: %v", rows[1])
	}
}

func TestWriteAWSListingRewrites(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWSConns: twoConns()}
	dir, err := EnsureBindingDirWith(root, bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, AWSListFile)
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"base_access":"unverified"`) {
		t.Fatalf("initial aws.json: %s", b)
	}
	if err := WriteAWSListing(root, bs, baseResults()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !bytes.Equal(b, AWSListing(bs.AWSConns, baseResults())) || !strings.Contains(string(b), `"base_access":"writes"`) {
		t.Fatalf("aws.json not rewritten: %s", b)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
}

// A box spawned before the probe shipped has an old agentbox-env.js; a re-probe
// must bring it up to date so `--list` shows the base-role state.
func TestWriteAWSListingRefreshesStaleEnvScript(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWSConns: twoConns()}
	dir, err := EnsureBindingDirWith(root, bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(dir, "agentbox-env.js")
	if err := os.WriteFile(js, []byte("// stale pre-probe script\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAWSListing(root, bs, baseResults()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(js)
	if err != nil || !bytes.Equal(b, envScript) {
		t.Fatalf("agentbox-env.js not refreshed (err %v, %d bytes)", err, len(b))
	}
	if fi, err := os.Stat(js); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
}

func TestWriteAWSListingRefusesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWSConns: twoConns()}
	dir, err := EnsureBindingDirWith(root, bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteAWSListing(root, bs, baseResults()); err == nil {
		t.Fatal("wrote through a symlinked binding dir")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("wrote into the symlink target: %v", ents)
	}
}

func TestWriteAWSListingMissingDirIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := WriteAWSListing(root, config.Bindings{AWSConns: twoConns()}, baseResults()); err == nil {
		t.Fatal("created or accepted a missing binding dir")
	}
	if _, err := os.Stat(filepath.Join(root, "broker")); err == nil {
		t.Fatal("created the binding dir")
	}
}

func TestWriteAWSListingNoConnsNoop(t *testing.T) {
	root := t.TempDir()
	bs := awsOnly(*binding())
	dir, err := EnsureBindingDir(root, bs)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAWSListing(root, bs, baseResults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, AWSListFile)); err == nil {
		t.Fatal("wrote aws.json for a legacy-only binding")
	}
}

// A re-probe result in aws.json must survive a later MCP write on the same dir.
func TestWriteSessionMCPKeepsAWSListing(t *testing.T) {
	root := t.TempDir()
	bs := config.Bindings{AWSConns: twoConns(), Tokens: tokenEntries()}
	dir, err := EnsureBindingDirWith(root, bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAWSListing(root, bs, baseResults()); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(filepath.Join(dir, AWSListFile))
	if _, err := WriteSessionMCP(root, bs, "sess-1", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, AWSListFile))
	if !bytes.Equal(got, want) || !strings.Contains(string(got), `"base_access":"writes"`) {
		t.Fatalf("WriteSessionMCP rewrote aws.json:\n%s\nwant\n%s", got, want)
	}
}
