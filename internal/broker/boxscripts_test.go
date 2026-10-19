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
	f := &fakeTokenBroker{sock: filepath.Join(dir, "b.sock")}
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
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
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
