package broker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

// fakeTokenBroker is a unix-socket broker that answers /v1/github/token with a
// token naming the owner it was asked for, or with a fixed error status.
type fakeTokenBroker struct {
	mu     sync.Mutex
	bodies []map[string]string
	auths  []string
	status int // 0 = 200
	sock   string
	// static answers /v1/static/token per entry name; a name not in the map is
	// 403. hang makes the route never answer (until the test ends).
	static      map[string]staticReply
	hang        bool
	staticMu    sync.Mutex
	stopped     chan struct{}
	statics     []string // names asked for, in order
	staticAuths []string
}

type staticReply struct {
	Value     string            `json:"value"`
	Env       []string          `json:"env"`
	ConfigEnv map[string]string `json:"config_env"`
}

func newFakeTokenBroker(t *testing.T) *fakeTokenBroker {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir, err := os.MkdirTemp("", "abx") // short: sun_path is ~107 bytes, GOTMPDIR is long
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeTokenBroker{sock: filepath.Join(dir, "b.sock"), stopped: make(chan struct{})}
	l, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/github/token", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "owner not allowed"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "ghs_tok_" + body["owner"], "expires_at": "2027-01-15T08:15:00Z"})
	})
	mux.HandleFunc("/v1/static/token", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		f.staticMu.Lock()
		f.statics = append(f.statics, body["name"])
		f.staticAuths = append(f.staticAuths, r.Header.Get("Authorization"))
		rep, ok := f.static[body["name"]]
		hang := f.hang
		f.staticMu.Unlock()
		if hang {
			select {
			case <-f.stopped:
			case <-r.Context().Done():
			}
			return
		}
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "token " + body["name"] + " is not bound to this session"})
			return
		}
		_ = json.NewEncoder(w).Encode(rep)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	t.Cleanup(func() { close(f.stopped) }) // runs before Shutdown, so a hung request cannot stall it
	return f
}

func (f *fakeTokenBroker) calls() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.bodies...)
}

func ghScriptDir(t *testing.T) string {
	t.Helper()
	dir, err := EnsureBindingDir(t.TempDir(), config.Bindings{GitHub: ghTwoOwners()})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// runNode runs node with a minimal env plus extra, returning stdout, stderr and the exit code.
func runNode(t *testing.T, cwd, stdin string, args []string, env ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("node", args...)
	cmd.Dir = cwd
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func brokerEnv(f *fakeTokenBroker) []string {
	return []string{"AGENTBOX_BROKER=unix://" + f.sock, "AGENTBOX_BROKER_TOKEN=sess-tok"}
}

func TestGitHelperPathForms(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	for _, tc := range []struct{ path, owner string }{
		{"CuliOps/agent-box", "CuliOps"},
		{"culiops/agent-box.git", "culiops"},
		{"culiops/agent-box/", "culiops"},
		{"culiops/agent-box.git/", "culiops"},
	} {
		before := len(f.calls())
		in := "protocol=https\nhost=github.com\npath=" + tc.path + "\n\n"
		out, stderr, code := runNode(t, "", in, []string{js, "github-git", "get"}, brokerEnv(f)...)
		want := "username=x-access-token\npassword=ghs_tok_" + tc.owner + "\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("%s: code=%d out=%q stderr=%q", tc.path, code, out, stderr)
		}
		calls := f.calls()
		if len(calls) != before+1 || calls[before]["owner"] != tc.owner || calls[before]["repo"] != "agent-box" {
			t.Fatalf("%s: broker saw %v", tc.path, calls)
		}
	}
	if f.auths[0] != "Bearer sess-tok" {
		t.Fatalf("auth header %q", f.auths[0])
	}
}

func TestGitHelperIgnoresOtherHosts(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	for _, in := range []string{
		"protocol=https\nhost=gitlab.com\npath=culiops/x\n\n",
		"protocol=http\nhost=github.com\npath=culiops/x\n\n",
		"protocol=https\nhost=github.com.evil.example\npath=culiops/x\n\n",
		"protocol=https\nhost=github.com\n\n", // no path: not a repo
	} {
		out, stderr, code := runNode(t, "", in, []string{js, "github-git", "get"}, brokerEnv(f)...)
		if code != 0 || out != "" || stderr != "" {
			t.Fatalf("%q: code=%d out=%q stderr=%q", in, code, out, stderr)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("broker called %d times for a request that is not a github.com repo", n)
	}
}

func TestGitHelperStoreEraseNoop(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	for _, op := range []string{"store", "erase", ""} {
		args := []string{js, "github-git"}
		if op != "" {
			args = append(args, op)
		}
		out, stderr, code := runNode(t, "", "protocol=https\nhost=github.com\npath=culiops/x\n\n", args, brokerEnv(f)...)
		if code != 0 || out != "" || stderr != "" {
			t.Fatalf("op %q: code=%d out=%q stderr=%q", op, code, out, stderr)
		}
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("broker called %d times", n)
	}
}

func TestGitHelperBrokerErrorNoStdout(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.status = 403
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	out, stderr, code := runNode(t, "", "protocol=https\nhost=github.com\npath=other/x\n\n", []string{js, "github-git", "get"}, brokerEnv(f)...)
	if code != 0 || out != "" || stderr != "agentbox: owner not allowed\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	// An unreachable broker is the same: git fails normally, nothing on stdout.
	out, stderr, code = runNode(t, "", "protocol=https\nhost=github.com\npath=a/x\n\n", []string{js, "github-git", "get"},
		"AGENTBOX_BROKER=unix://"+filepath.Join(t.TempDir(), "none.sock"), "AGENTBOX_BROKER_TOKEN=tok-secret-xyz")
	if code != 0 || out != "" || !strings.HasPrefix(stderr, "agentbox: ") || strings.Contains(stderr, "tok-secret-xyz") {
		t.Fatalf("unreachable: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

// The AWS credential_process mode is untouched by the new mode.
func TestCredScriptAWSModeStillRefusesWithoutToken(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	_, stderr, code := runNode(t, "", "", []string{js}, "AGENTBOX_BROKER=unix://"+f.sock)
	if code != 1 || !strings.Contains(stderr, "not an agent-box AWS session") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// fakeGh is a stand-in for the real gh: it prints the token it was given and its argv.
func fakeGh(t *testing.T, exit string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	body := "#!/bin/sh\necho \"TOKEN=$GH_TOKEN\"\necho \"ARGV=$*\"\nexit " + exit + "\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGhWrapperOwnerResolution(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	real := fakeGh(t, "0")
	for _, tc := range []struct {
		name  string
		args  []string
		env   []string
		owner string
		repo  string
		argv  string
	}{
		{"-R sep", []string{"-R", "a/b", "pr", "list"}, nil, "a", "b", "-R a/b pr list"},
		{"-R glued", []string{"pr", "list", "-Ra/b"}, nil, "a", "b", "pr list -Ra/b"},
		{"--repo sep", []string{"issue", "list", "--repo", "a/b"}, nil, "a", "b", "issue list --repo a/b"},
		{"--repo=", []string{"--repo=a/b", "run", "list"}, nil, "a", "b", "--repo=a/b run list"},
		{"flag beats env", []string{"-R", "a/b"}, []string{"GH_REPO=c/d"}, "a", "b", "-R a/b"},
		{"GH_REPO", []string{"pr", "list"}, []string{"GH_REPO=c/d"}, "c", "d", "pr list"},
		{"GH_REPO with host", []string{"pr", "list"}, []string{"GH_REPO=github.com/c/d"}, "c", "d", "pr list"},
		{"first owner", []string{"api", "user"}, nil, "culiops", "", "api user"},
	} {
		before := len(f.calls())
		env := append(append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=culiops,CuliOps2"), tc.env...)
		out, stderr, code := runNode(t, t.TempDir(), "", append([]string{js}, tc.args...), env...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: code=%d stderr=%q", tc.name, code, stderr)
		}
		if !strings.Contains(out, "TOKEN=ghs_tok_"+tc.owner+"\n") || !strings.Contains(out, "ARGV="+tc.argv+"\n") {
			t.Fatalf("%s: out=%q", tc.name, out)
		}
		calls := f.calls()
		if len(calls) != before+1 || calls[before]["owner"] != tc.owner || calls[before]["repo"] != tc.repo {
			t.Fatalf("%s: broker saw %v", tc.name, calls)
		}
	}
}

// URL-form repos (what gh accepts and people paste) are parsed; a value that
// is present but cannot be read fails closed instead of falling back to the
// first owner, which for a PAT owner would be the wrong token.
func TestGhWrapperURLFormRepo(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	real := fakeGh(t, "0")
	for _, tc := range []struct {
		name  string
		args  []string
		env   []string
		owner string
		repo  string
	}{
		{"-R https", []string{"-R", "https://github.com/CuliOps/agent-box", "pr", "list"}, nil, "CuliOps", "agent-box"},
		{"-R https .git slash", []string{"-R", "https://github.com/o/r.git/"}, nil, "o", "r"},
		{"--repo= https", []string{"--repo=https://github.com/o/r"}, nil, "o", "r"},
		{"GH_REPO scp", []string{"pr", "list"}, []string{"GH_REPO=git@github.com:culiops/agent-box.git"}, "culiops", "agent-box"},
		{"HOST/O/R", []string{"-R", "github.com/o/r"}, nil, "o", "r"},
	} {
		before := len(f.calls())
		env := append(append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=other"), tc.env...)
		out, stderr, code := runNode(t, t.TempDir(), "", append([]string{js}, tc.args...), env...)
		calls := f.calls()
		if code != 0 || stderr != "" || len(calls) != before+1 || calls[before]["owner"] != tc.owner || calls[before]["repo"] != tc.repo {
			t.Fatalf("%s: code=%d out=%q stderr=%q calls=%v", tc.name, code, out, stderr, calls)
		}
	}
	// Another host in URL form: the real gh runs unminted.
	before := len(f.calls())
	env := append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=other")
	out, stderr, code := runNode(t, t.TempDir(), "", []string{js, "-R", "https://gitlab.com/a/b", "pr", "list"}, env...)
	if code != 0 || stderr != "" || !strings.Contains(out, "TOKEN=\n") || len(f.calls()) != before {
		t.Fatalf("other host: code=%d out=%q stderr=%q calls=%v", code, out, stderr, f.calls())
	}
	// Unparseable: fail closed, no broker call, no fallback to AGENTBOX_GH_OWNERS.
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{"-R no repo", []string{"-R", "https://github.com/a"}, nil},
		{"--repo= no repo", []string{"--repo=https://github.com/a/"}, nil},
		{"GH_REPO garbage", []string{"pr", "list"}, []string{"GH_REPO=https://github.com/"}},
	} {
		before := len(f.calls())
		env := append(append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=other"), tc.env...)
		out, stderr, code := runNode(t, t.TempDir(), "", append([]string{js}, tc.args...), env...)
		if code != 1 || out != "" || !strings.Contains(stderr, "cannot read the repository in") || !strings.Contains(stderr, "use -R OWNER/REPO") || len(f.calls()) != before {
			t.Fatalf("%s: code=%d out=%q stderr=%q calls=%v", tc.name, code, out, stderr, f.calls())
		}
	}
}

// A target on another host than github.com must never get a github.com token:
// the real gh runs unminted (no broker call, no GH_TOKEN).
func TestGhWrapperOtherHostRunsUnminted(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	real := fakeGh(t, "0")
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{"-R host", []string{"-R", "ghe.example.org/a/b", "pr", "list"}, nil},
		{"-R glued host", []string{"pr", "list", "-Rghe.example.org/a/b"}, nil},
		{"--repo= host", []string{"--repo=ghe.example.org/a/b", "pr", "list"}, nil},
		{"GH_REPO host", []string{"pr", "list"}, []string{"GH_REPO=ghe.example.org/a/b"}},
		{"GH_HOST", []string{"pr", "list"}, []string{"GH_HOST=ghe.example.org"}},
		{"GH_HOST with -R", []string{"-R", "a/b", "pr", "list"}, []string{"GH_HOST=ghe.example.org"}},
		{"--hostname", []string{"api", "--hostname", "ghe.example.org", "user"}, nil},
		{"--hostname=", []string{"api", "--hostname=ghe.example.org", "user"}, nil},
	} {
		before := len(f.calls())
		env := append(append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=culiops,CuliOps2"), tc.env...)
		out, stderr, code := runNode(t, t.TempDir(), "", append([]string{js}, tc.args...), env...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: code=%d stderr=%q", tc.name, code, stderr)
		}
		if !strings.Contains(out, "TOKEN=\n") || !strings.Contains(out, "ARGV="+strings.Join(tc.args, " ")+"\n") {
			t.Fatalf("%s: out=%q (want the real gh run with an empty GH_TOKEN)", tc.name, out)
		}
		if n := len(f.calls()); n != before {
			t.Fatalf("%s: broker was called (%v)", tc.name, f.calls())
		}
	}
	// GH_HOST=github.com is the default host: still minted.
	before := len(f.calls())
	env := append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=culiops", "GH_HOST=github.com")
	out, _, code := runNode(t, t.TempDir(), "", []string{js, "-R", "a/b", "pr", "list"}, env...)
	if code != 0 || !strings.Contains(out, "TOKEN=ghs_tok_a\n") || len(f.calls()) != before+1 {
		t.Fatalf("GH_HOST=github.com: code=%d out=%q", code, out)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
}

func TestGhWrapperOriginRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	real := fakeGh(t, "0")
	for _, tc := range []struct{ remote, owner, repo string }{
		{"https://github.com/o1/r1.git", "o1", "r1"},
		{"https://github.com/o2/r2", "o2", "r2"},
		{"git@github.com:o3/r3.git", "o3", "r3"},
		{"ssh://git@github.com/o4/r4.git", "o4", "r4"},
		{"https://x-access-token@github.com/o5/r5.git", "o5", "r5"},
		{"https://gitlab.com/o6/r6.git", "culiops", ""}, // not GitHub: first owner
		{"https://github.com.evil.example/o7/r7.git", "culiops", ""},
	} {
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q")
		gitIn(t, dir, "remote", "add", "origin", tc.remote)
		before := len(f.calls())
		env := append(brokerEnv(f), "AGENTBOX_GH_REAL="+real, "AGENTBOX_GH_OWNERS=culiops,CuliOps2")
		out, stderr, code := runNode(t, dir, "", []string{js, "pr", "list"}, env...)
		calls := f.calls()
		if code != 0 || len(calls) != before+1 || calls[before]["owner"] != tc.owner || calls[before]["repo"] != tc.repo {
			t.Fatalf("%s: code=%d out=%q stderr=%q calls=%v", tc.remote, code, out, stderr, calls)
		}
	}
}

func TestGhWrapperExitCode(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	env := append(brokerEnv(f), "AGENTBOX_GH_REAL="+fakeGh(t, "3"), "AGENTBOX_GH_OWNERS=culiops")
	if _, _, code := runNode(t, t.TempDir(), "", []string{js, "pr", "list"}, env...); code != 3 {
		t.Fatalf("exit code %d, want the child's 3", code)
	}
}

func TestGhWrapperBrokerErrorExits1(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.status = 403
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	env := append(brokerEnv(f), "AGENTBOX_GH_REAL="+fakeGh(t, "0"), "AGENTBOX_GH_OWNERS=culiops")
	out, stderr, code := runNode(t, t.TempDir(), "", []string{js, "pr", "list"}, env...)
	if code != 1 || out != "" || stderr != "agentbox: owner not allowed\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	// No owner known at all: nothing to ask for.
	env = append(brokerEnv(f), "AGENTBOX_GH_REAL="+fakeGh(t, "0"))
	_, stderr, code = runNode(t, t.TempDir(), "", []string{js, "pr", "list"}, env...)
	if code != 1 || !strings.HasPrefix(stderr, "agentbox: ") {
		t.Fatalf("no owner: code=%d stderr=%q", code, stderr)
	}
}

// A signal reaches the child, and the wrapper exits 128+n.
func TestGhWrapperForwardsSignals(t *testing.T) {
	f := newFakeTokenBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-gh.js")
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho ready\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", js, "pr", "list")
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, append(brokerEnv(f), "AGENTBOX_GH_REAL="+p, "AGENTBOX_GH_OWNERS=culiops")...)
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(pipe, buf); err != nil || string(buf) != "ready\n" {
		t.Fatalf("child never started: %q %v", buf, err)
	}
	_ = cmd.Process.Signal(os.Interrupt)
	err := cmd.Wait()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 130 {
		t.Fatalf("wait: %v, want exit 130", err)
	}
}

// ---- agentbox-env ----

const fakeSecret = "tok-SECRET-1234"

// listingTokens are two entries that exercise the listing: one with a label,
// prod flag, description, config, a port and a private range, one with a file
// value and a bare host. Neither value reference may reach the listing.
func listingTokens() []config.StaticToken {
	return []config.StaticToken{
		{Name: "grafana-cloud", Provider: "grafana", Label: "Grafana", Prod: true, Description: "Dashboards and alerts",
			Value:     config.KeyRef{Type: "env", Var: "GRAFANA_SA_TOKEN"},
			Env:       []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"},
			ConfigEnv: map[string]string{"GRAFANA_URL": "https://acme.grafana.net"},
			Egress:    []config.TokenEgress{{Host: "acme.grafana.net"}, {Host: "sentry.corp.example", Port: 8443, Private: true, CIDR: "10.20.0.0/16"}},
			MCP:       &config.TokenMCP{Server: "grafana", Command: "mcp-grafana"}},
		{Name: "atlassian", Value: config.KeyRef{Type: "file", Path: "/home/u/.config/agent-box/atlassian.token"},
			Env: []string{"JIRA_API_TOKEN"}, Egress: []config.TokenEgress{{Host: "acme.atlassian.net"}}},
	}
}

func envScriptDir(t *testing.T) string {
	t.Helper()
	dir, err := EnsureBindingDir(t.TempDir(), config.Bindings{Tokens: listingTokens()})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func grafanaReply() staticReply {
	return staticReply{Value: fakeSecret, Env: []string{"GRAFANA_SERVICE_ACCOUNT_TOKEN"},
		ConfigEnv: map[string]string{"GRAFANA_URL": "https://acme.grafana.net"}}
}

func TestAgentboxEnvRunsChildWithEnv(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{"grafana-cloud": grafanaReply()}
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	out, stderr, code := runNode(t, "", "", []string{js, "grafana-cloud", "--", "node", "-e",
		"console.log(process.env.GRAFANA_SERVICE_ACCOUNT_TOKEN + ' ' + process.env.GRAFANA_URL)"}, brokerEnv(f)...)
	if code != 0 || out != fakeSecret+" https://acme.grafana.net\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(stderr, fakeSecret) || strings.Contains(stderr, "SECRET") {
		t.Fatalf("wrapper leaked the value: %q", stderr)
	}
	if got := f.staticAuths; len(got) != 1 || got[0] != "Bearer sess-tok" {
		t.Fatalf("auth headers %v", got)
	}
	if got := f.statics; len(got) != 1 || got[0] != "grafana-cloud" {
		t.Fatalf("broker saw %v", got)
	}
}

func TestAgentboxEnvAbortsBeforeExec(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{"grafana-cloud": grafanaReply()}
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	marker := filepath.Join(t.TempDir(), "ran")
	_, stderr, code := runNode(t, "", "", []string{js, "grafana-cloud", "other", "--", "node", "-e",
		"require('fs').writeFileSync(process.argv[1], 'x')", marker}, brokerEnv(f)...)
	if code != 1 || stderr != "agentbox-env: other: token other is not bound to this session\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child ran although a fetch failed")
	}
	if strings.Contains(stderr, fakeSecret) {
		t.Fatal("leaked")
	}
}

func TestAgentboxEnvRefusesSharedName(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{
		"sentry-staging": {Value: fakeSecret, Env: []string{"SENTRY_AUTH_TOKEN"}},
		"sentry-prod":    {Value: "tok-OTHER-5678", Env: []string{"SENTRY_AUTH_TOKEN"}},
	}
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	marker := filepath.Join(t.TempDir(), "ran")
	_, stderr, code := runNode(t, "", "", []string{js, "sentry-staging", "sentry-prod", "--", "node", "-e",
		"require('fs').writeFileSync(process.argv[1], 'x')", marker}, brokerEnv(f)...)
	if code != 1 || stderr != "agentbox-env: sentry-staging and sentry-prod both set SENTRY_AUTH_TOKEN; name one\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child ran")
	}
	// A config_env clash counts too.
	f.staticMu.Lock()
	f.static = map[string]staticReply{
		"a": {Value: "x", Env: []string{"A_TOKEN"}, ConfigEnv: map[string]string{"SHARED_URL": "1"}},
		"b": {Value: "y", Env: []string{"B_TOKEN"}, ConfigEnv: map[string]string{"SHARED_URL": "2"}},
	}
	f.staticMu.Unlock()
	_, stderr, code = runNode(t, "", "", []string{js, "a", "b", "--", "node", "-e", "0"}, brokerEnv(f)...)
	if code != 1 || stderr != "agentbox-env: a and b both set SHARED_URL; name one\n" {
		t.Fatalf("config clash: code=%d stderr=%q", code, stderr)
	}
}

func TestAgentboxEnvUsage(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{"a": grafanaReply()}
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	for _, args := range [][]string{
		{},                                  // nothing
		{"a", "node", "-e", "0"},            // no --
		{"--", "node", "-e", "0"},           // no names
		{"a", "--"},                         // no command
		{"a", "a", "--", "node", "-e", "0"}, // a name twice
	} {
		_, stderr, code := runNode(t, "", "", append([]string{js}, args...), brokerEnv(f)...)
		if code != 2 || !strings.HasPrefix(stderr, "usage: agentbox-env NAME [NAME...] -- CMD [ARGS...]") {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
	}
	if n := len(f.statics); n != 0 {
		t.Fatalf("a usage error still called the broker %d times", n)
	}
}

func TestAgentboxEnvExitAndSignal(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{"grafana-cloud": grafanaReply()}
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	_, _, code := runNode(t, "", "", []string{js, "grafana-cloud", "--", "node", "-e", "process.exit(7)"}, brokerEnv(f)...)
	if code != 7 {
		t.Fatalf("child exit 7: wrapper exit %d", code)
	}
	// The child kills itself, so the test does not race a signal sent from outside.
	_, _, code = runNode(t, "", "", []string{js, "grafana-cloud", "--", "node", "-e", "process.kill(process.pid, 'SIGTERM')"}, brokerEnv(f)...)
	if code != 143 {
		t.Fatalf("child killed by SIGTERM: wrapper exit %d, want 143", code)
	}
	// A command that does not exist is a spawn error: 127, and no value printed.
	_, stderr, code := runNode(t, "", "", []string{js, "grafana-cloud", "--", "/nonexistent/agentbox-cmd"}, brokerEnv(f)...)
	if code != 127 || strings.Contains(stderr, fakeSecret) || !strings.HasPrefix(stderr, "agentbox-env: cannot run ") {
		t.Fatalf("spawn error: code=%d stderr=%q", code, stderr)
	}
}

func TestAgentboxEnvList(t *testing.T) {
	dir := envScriptDir(t)
	f := newFakeTokenBroker(t)
	f.static = map[string]staticReply{"grafana-cloud": grafanaReply()}
	out, stderr, code := runNode(t, "", "", []string{filepath.Join(dir, "agentbox-env.js"), "--list"},
		"AGENTBOX_ENV_LIST_FILE="+filepath.Join(dir, TokensFile)) // no broker env: --list makes no call
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"grafana-cloud", "[Grafana]", "[prod]", "grafana", "Dashboards and alerts",
		"GRAFANA_SERVICE_ACCOUNT_TOKEN", "GRAFANA_URL=https://acme.grafana.net",
		"acme.grafana.net", "sentry.corp.example:8443 (private)", "mcp: grafana", "atlassian", "acme.atlassian.net"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{fakeSecret, "GRAFANA_SA_TOKEN", "/home/u/.config", "atlassian.token"} {
		if strings.Contains(out, bad) {
			t.Errorf("listing shows %q:\n%s", bad, out)
		}
	}
	if n := len(f.statics); n != 0 {
		t.Fatalf("--list called the broker %d times", n)
	}
}

func TestAgentboxEnvListMissingFile(t *testing.T) {
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	_, stderr, code := runNode(t, "", "", []string{js, "--list"}, "AGENTBOX_ENV_LIST_FILE="+filepath.Join(t.TempDir(), "none.json"))
	if code != 1 || !strings.HasPrefix(stderr, "agentbox-env: cannot read the token list") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// The wrapper's own timeout is 10 s; AGENTBOX_ENV_TIMEOUT_MS shortens it for
// this test only, so the suite does not wait the real 10 s.
func TestAgentboxEnvTimeout(t *testing.T) {
	f := newFakeTokenBroker(t)
	f.hang = true
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	marker := filepath.Join(t.TempDir(), "ran")
	start := time.Now()
	_, stderr, code := runNode(t, "", "", []string{js, "grafana-cloud", "--", "node", "-e",
		"require('fs').writeFileSync(process.argv[1], 'x')", marker},
		append(brokerEnv(f), "AGENTBOX_ENV_TIMEOUT_MS=500")...)
	if code != 1 || !strings.Contains(stderr, "timed out") || !strings.HasPrefix(stderr, "agentbox-env: grafana-cloud: ") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Fatalf("took %s", d)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the child ran")
	}
}

// The override can only shorten the 10 s default: a non-finite, huge or junk value
// must not become a timer that fires at once (Node clamps an overflowing delay to
// 1 ms and warns). A broker that answers promptly is enough to see that.
func TestAgentboxEnvTimeoutOverrideIsClamped(t *testing.T) {
	for _, v := range []string{"Infinity", "1e20", "NaN", "-5", "abc", "5000"} {
		t.Run(v, func(t *testing.T) {
			f := newFakeTokenBroker(t)
			f.static = map[string]staticReply{"grafana-cloud": grafanaReply()}
			js := filepath.Join(envScriptDir(t), "agentbox-env.js")
			out, stderr, code := runNode(t, "", "", []string{js, "grafana-cloud", "--", "node", "-e",
				"console.log(process.env.GRAFANA_URL)"}, append(brokerEnv(f), "AGENTBOX_ENV_TIMEOUT_MS="+v)...)
			if code != 0 || out != "https://acme.grafana.net\n" || stderr != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestAgentboxEnvNoBrokerEnv(t *testing.T) {
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	_, stderr, code := runNode(t, "", "", []string{js, "a", "--", "node", "-e", "0"})
	if code != 1 || !strings.Contains(stderr, "AGENTBOX_BROKER") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// --- #155 task 7: AWS connections in the in-box scripts ---

// fakeCredBroker answers /v1/credentials and records each raw body.
type fakeCredBroker struct {
	mu     sync.Mutex
	bodies []string
	ctypes []string
	sock   string
}

func newFakeCredBroker(t *testing.T) *fakeCredBroker {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir, err := os.MkdirTemp("", "abx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeCredBroker{sock: filepath.Join(dir, "b.sock")}
	l, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(raw))
		f.ctypes = append(f.ctypes, r.Header.Get("Content-Type"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"Version":1,"AccessKeyId":"AKIA","SecretAccessKey":"s","SessionToken":"t"}`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return f
}

func (f *fakeCredBroker) env() []string {
	return []string{"AGENTBOX_BROKER=unix://" + f.sock, "AGENTBOX_BROKER_TOKEN=sess-tok"}
}

func (f *fakeCredBroker) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...), append([]string(nil), f.ctypes...)
}

func TestCredAwsNoNameGuidance(t *testing.T) {
	f := newFakeCredBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	out, stderr, code := runNode(t, "", "", []string{js, "aws"}, f.env()...)
	want := "agentbox: several AWS connections are bound — pick one with --profile <name> or AWS_PROFILE; run agentbox-env --list\n"
	if code != 1 || stderr != want || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if b, _ := f.seen(); len(b) != 0 {
		t.Fatalf("guidance made %d broker calls", len(b))
	}
	// Even with no broker env at all, the guidance is the answer (no network, no token needed).
	_, stderr, code = runNode(t, "", "", []string{js, "aws"})
	if code != 1 || stderr != want {
		t.Fatalf("no env: code=%d stderr=%q", code, stderr)
	}
}

func TestCredAwsNameBody(t *testing.T) {
	f := newFakeCredBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	out, stderr, code := runNode(t, "", "", []string{js, "aws", "a"}, f.env()...)
	if code != 0 || stderr != "" || !strings.Contains(out, "AKIA") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	b, ct := f.seen()
	if len(b) != 1 || b[0] != `{"name":"a"}` || ct[0] != "application/json" {
		t.Fatalf("bodies=%q ctypes=%q", b, ct)
	}
}

func TestCredLegacyNoArgsEmptyBody(t *testing.T) {
	f := newFakeCredBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	out, stderr, code := runNode(t, "", "", []string{js}, f.env()...)
	if code != 0 || stderr != "" || !strings.Contains(out, "AKIA") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	b, ct := f.seen()
	if len(b) != 1 || b[0] != "" || ct[0] != "" {
		t.Fatalf("legacy request changed: bodies=%q ctypes=%q", b, ct)
	}
}

func TestCredAwsBadNameLocal(t *testing.T) {
	f := newFakeCredBroker(t)
	js := filepath.Join(ghScriptDir(t), "agentbox-cred.js")
	for _, n := range []string{"A", "-a", "a b", "a/b", strings.Repeat("a", 64), `a"}`} {
		_, stderr, code := runNode(t, "", "", []string{js, "aws", n}, f.env()...)
		if code != 1 || !strings.HasPrefix(stderr, "agentbox: ") {
			t.Errorf("%q: code=%d stderr=%q", n, code, stderr)
		}
	}
	if b, _ := f.seen(); len(b) != 0 {
		t.Fatalf("bad names reached the broker: %q", b)
	}
}

// awsListDir binds two connections and a token entry; the env script and both
// listings are in the returned dir.
func awsListDir(t *testing.T, withTokens bool) string {
	t.Helper()
	bs := config.Bindings{AWSConns: twoConns()}
	if withTokens {
		bs.Tokens = listingTokens()
	}
	dir, err := EnsureBindingDir(t.TempDir(), bs)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnvListAWSSection(t *testing.T) {
	dir := awsListDir(t, true)
	js := filepath.Join(dir, "agentbox-env.js")
	out, stderr, code := runNode(t, "", "", []string{js, "--list"},
		"AGENTBOX_ENV_LIST_FILE="+filepath.Join(dir, TokensFile), "AGENTBOX_ENV_AWS_FILE="+filepath.Join(dir, AWSListFile))
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	want := "AWS connections\n" +
		"acme-prod-read [Acme prod] [prod] [write-capable]\n" +
		"  account 111111111111 · role agent-read · region eu-west-1\n" +
		"  Prod, read only\n" +
		"  use: aws --profile acme-prod-read …   terraform: provider \"aws\" { profile = \"acme-prod-read\" }\n" +
		"\n" +
		"acme-stg-read\n" +
		"  account 222222222222 · role agent-read · region eu-west-1\n" +
		"  use: aws --profile acme-stg-read …   terraform: provider \"aws\" { profile = \"acme-stg-read\" }\n" +
		"\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("listing:\n%s\nwant prefix:\n%s", out, want)
	}
	rest := out[len(want):]
	if !strings.HasPrefix(rest, "grafana-cloud") || !strings.Contains(rest, "atlassian") {
		t.Fatalf("token section missing after the AWS one:\n%s", rest)
	}
	for _, bad := range []string{"sso-acme", "source_profile", "agent-write"} {
		if strings.Contains(out, bad) {
			t.Errorf("listing shows %q", bad)
		}
	}
}

func TestEnvListAWSOnly(t *testing.T) {
	dir := awsListDir(t, false)
	js := filepath.Join(dir, "agentbox-env.js")
	out, stderr, code := runNode(t, "", "", []string{js, "--list"},
		"AGENTBOX_ENV_LIST_FILE="+filepath.Join(dir, TokensFile), "AGENTBOX_ENV_AWS_FILE="+filepath.Join(dir, AWSListFile))
	if code != 0 || stderr != "" || !strings.HasPrefix(out, "AWS connections\n") || strings.Contains(out, "no tokens") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestEnvListNoTokensLineOnlyWhenNothing(t *testing.T) {
	js := filepath.Join(envScriptDir(t), "agentbox-env.js")
	empty := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(empty, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := filepath.Join(t.TempDir(), "none.json")
	// Nothing bound at all: the line is printed.
	out, _, code := runNode(t, "", "", []string{js, "--list"}, "AGENTBOX_ENV_LIST_FILE="+empty, "AGENTBOX_ENV_AWS_FILE="+none)
	if code != 0 || out != "no tokens are bound to this session\n" {
		t.Fatalf("nothing bound: code=%d out=%q", code, out)
	}
	// Connections but no tokens (no tokens.json at all, as EnsureBindingDir writes it): no such line, no error.
	dir := awsListDir(t, false)
	out, stderr, code := runNode(t, "", "", []string{filepath.Join(dir, "agentbox-env.js"), "--list"},
		"AGENTBOX_ENV_LIST_FILE="+none, "AGENTBOX_ENV_AWS_FILE="+filepath.Join(dir, AWSListFile))
	if code != 0 || stderr != "" || strings.Contains(out, "no tokens") || !strings.Contains(out, "acme-stg-read") {
		t.Fatalf("aws only: code=%d out=%q stderr=%q", code, out, stderr)
	}
	// Connections and an empty token list: still no line.
	out, _, code = runNode(t, "", "", []string{filepath.Join(dir, "agentbox-env.js"), "--list"},
		"AGENTBOX_ENV_LIST_FILE="+empty, "AGENTBOX_ENV_AWS_FILE="+filepath.Join(dir, AWSListFile))
	if code != 0 || strings.Contains(out, "no tokens") {
		t.Fatalf("empty tokens: code=%d out=%q", code, out)
	}
}
