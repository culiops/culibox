package broker

import (
	"encoding/json"
	"io"
	"io/fs"
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

// hostHooks is a Hooks whose host sockets live under root and are served by
// serve (nil: the presenter-echo serveLog).
func hostHooks(t *testing.T, root string, serve func(net.Listener)) Hooks {
	t.Helper()
	hs := newHostSockets(root, "linux")
	if serve == nil {
		serve = (&serveLog{}).serve
	}
	hs.Start(serve)
	t.Cleanup(hs.Close)
	return Hooks{StateRoot: root, EP: Endpoint{Hosts: hs}}
}

// allKinds is a binding with named AWS connections, GitHub and static tokens.
func allKinds() config.Bindings {
	return config.Bindings{AWSConns: twoConns(), GitHub: ghTwoOwners(), Tokens: listingTokens()}
}

// tree lists every path under dir (relative, slash-separated) with its mode.
func tree(t *testing.T, dir string) map[string]fs.FileMode {
	t.Helper()
	out := map[string]fs.FileMode{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = fi.Mode()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// snapshot is every regular file and dir under dir outside skip, with its
// content (files) and mode.
func snapshot(t *testing.T, dir, skip string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skip {
			return filepath.SkipDir
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := fi.Mode().String()
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += ":" + string(b)
		}
		out[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHostForWritesOnlyUnderItsDir(t *testing.T) {
	home := shortDir(t)
	t.Setenv("HOME", home)
	for _, d := range []string{".aws", ".config/gh", ".local/share/culibox"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{".aws/config": "[profile sso-acme]\nregion = eu-west-1\n",
		".config/gh/hosts.yml": "github.com:\n  user: me\n", ".gitconfig": "[user]\n  name = me\n"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(home, ".local/share/culibox")
	h := hostHooks(t, root, nil)
	before := snapshot(t, home, root)
	_, dir, err := h.HostFor("s1", allKinds(), nil, "/usr/bin/gh")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "broker", "h", HostDirName("s1")); dir != want {
		t.Fatalf("dir = %s, want %s", dir, want)
	}
	h.ReleaseHost("s1", true)
	if after := snapshot(t, home, root); !maps2Equal(before, after) {
		t.Fatalf("HostFor touched a file outside the state root:\nbefore %v\nafter  %v", before, after)
	}
	// Inside the state root, only broker/h/ was created.
	got := tree(t, root)
	for p := range got {
		if p != "broker" && p != "broker/h" {
			t.Errorf("state root holds %s after the session ended", p)
		}
	}
}

func maps2Equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestHostForFilesPerKind(t *testing.T) {
	legacy := config.Bindings{AWS: binding()}
	cases := []struct {
		name  string
		bs    config.Bindings
		files []string // regular files and shims, besides the socket
	}{
		{"legacy aws", legacy, []string{"agentbox-cred.js", "aws-config"}},
		{"aws connections", config.Bindings{AWSConns: twoConns()},
			[]string{"agentbox-cred.js", "agentbox-env.js", "aws-config", "aws.json", "bin/agentbox-env"}},
		{"github", config.Bindings{GitHub: ghTwoOwners()},
			[]string{"agentbox-cred.js", "agentbox-env.js", "agentbox-gh.js", "github.json", "bin/agentbox-env", "bin/gh"}},
		{"tokens", config.Bindings{Tokens: listingTokens()},
			[]string{"agentbox-cred.js", "agentbox-env.js", "tokens.json", "bin/agentbox-env"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := shortDir(t)
			h := hostHooks(t, root, nil)
			_, dir, err := h.HostFor("s1", c.bs, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			got := tree(t, dir)
			want := map[string]bool{"s": true, "bin": true}
			if c.bs.GitHub != nil {
				want["gh"] = true
			}
			for _, f := range c.files {
				want[f] = true
			}
			for p := range got {
				if !want[p] {
					t.Errorf("unexpected %s", p)
				}
			}
			for p := range want {
				if _, ok := got[p]; !ok {
					t.Errorf("missing %s", p)
				}
			}
			if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
				t.Fatalf("dir %v %v", di, err)
			}
			for p, m := range got {
				switch {
				case p == "s":
				case p == "bin" || p == "gh":
					if !m.IsDir() || m.Perm() != 0o700 {
						t.Errorf("%s mode %v, want a 0700 dir", p, m)
					}
				case strings.HasPrefix(p, "bin/"):
					if !m.IsRegular() || m.Perm() != 0o700 {
						t.Errorf("%s mode %v, want 0700", p, m)
					}
					b, _ := os.ReadFile(filepath.Join(dir, p))
					script := map[string]string{"bin/gh": "agentbox-gh.js", "bin/agentbox-env": "agentbox-env.js"}[p]
					if want := "#!/bin/sh\nexec node '" + dir + "/" + script + "' \"$@\"\n"; string(b) != want {
						t.Errorf("%s = %q, want %q", p, b, want)
					}
				default:
					if !m.IsRegular() || m.Perm() != 0o600 {
						t.Errorf("%s mode %v, want 0600", p, m)
					}
				}
			}
			if c.bs.AWS != nil {
				b, _ := os.ReadFile(filepath.Join(dir, "aws-config"))
				want := "[profile agentbox]\ncredential_process = node '" + dir + "/agentbox-cred.js'\nregion = ap-southeast-1\n"
				if string(b) != want {
					t.Errorf("aws-config = %q, want %q", b, want)
				}
			}
			// The listings are the box's, byte for byte.
			for name, want := range map[string][]byte{AWSListFile: AWSListing(c.bs.AWSConns, nil), TokensFile: TokensListing(c.bs.Tokens)} {
				if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && string(b) != string(want) {
					t.Errorf("%s = %s, want %s", name, b, want)
				}
			}
		})
	}
}

// A session id is reused: a respawn with fewer kinds leaves no listing or
// shim of the earlier binding behind.
func TestHostForDropsStaleFiles(t *testing.T) {
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	if _, _, err := h.HostFor("s1", allKinds(), nil, ""); err != nil {
		t.Fatal(err)
	}
	h.ReleaseHost("s1", false)
	_, dir, err := h.HostFor("s1", config.Bindings{AWS: binding()}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"agentbox-env.js", "agentbox-gh.js", "aws.json", "github.json", "tokens.json", "bin/gh", "bin/agentbox-env"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s survived a respawn without it (%v)", p, err)
		}
	}
}

func TestHostForEnvHasNoSecretAndExactNames(t *testing.T) {
	t.Setenv("GRAFANA_SA_TOKEN", fakeSecret)
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	env, dir, err := h.HostFor("s1", allKinds(), nil, "/usr/bin/gh")
	if err != nil {
		t.Fatal(err)
	}
	helper := "!node '" + dir + "/agentbox-cred.js' github-git"
	want := map[string]string{
		"AGENTBOX_BROKER":     "unix://" + dir + "/s",
		"AGENTBOX_DIR":        dir,
		"AWS_CONFIG_FILE":     dir + "/aws-config",
		"AWS_REGION":          "eu-west-1",
		"AWS_DEFAULT_REGION":  "eu-west-1",
		"TF_CLI_ARGS_plan":    "-lock=false",
		"GIT_CONFIG_COUNT":    "4",
		"GIT_CONFIG_KEY_0":    "credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0":  helper,
		"GIT_CONFIG_KEY_1":    "credential.https://github.com.useHttpPath",
		"GIT_CONFIG_VALUE_1":  "true",
		"GIT_CONFIG_KEY_2":    "url.https://github.com/.insteadOf",
		"GIT_CONFIG_VALUE_2":  "git@github.com:",
		"GIT_CONFIG_KEY_3":    "url.https://github.com/.insteadOf",
		"GIT_CONFIG_VALUE_3":  "ssh://git@github.com/",
		"GIT_TERMINAL_PROMPT": "0",
		"GH_CONFIG_DIR":       dir + "/gh",
		"GH_PROMPT_DISABLED":  "1",
		"AGENTBOX_GH_OWNERS":  "culiops,CuliOps2",
		"AGENTBOX_GH_REAL":    "/usr/bin/gh",
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("bad env entry %q", kv)
		}
		if _, dup := got[k]; dup {
			t.Errorf("%s is set twice", k)
		}
		got[k] = v
		for _, bad := range []string{fakeSecret, staticSecret, "PRIVATE KEY", "GRAFANA_SA_TOKEN", "atlassian.token"} {
			if strings.Contains(v, bad) {
				t.Errorf("%s carries %q", k, bad)
			}
		}
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected %s=%q", k, got[k])
		}
	}
	// One connection: a profile selector, as in a box. No gh: no AGENTBOX_GH_REAL.
	env, _, err = h.HostFor("s2", config.Bindings{AWSConns: twoConns()[:1], GitHub: ghTwoOwners()}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := envHas(env, "AWS_PROFILE"); !ok || v != "acme-prod-read" {
		t.Errorf("AWS_PROFILE = %q (%v)", v, ok)
	}
	if _, ok := envHas(env, "AGENTBOX_GH_REAL"); ok {
		t.Error("AGENTBOX_GH_REAL set without a real gh")
	}
	// Every name HostFor sets is in the strip list, so a profile cannot shadow one.
	strip := h.HostStripEnv(allKinds())
	for k := range want {
		if !slices.Contains(strip, k) {
			t.Errorf("HostStripEnv lacks %s", k)
		}
	}
	for _, k := range []string{"AWS_PROFILE", TokenEnv, "GH_TOKEN", "GITHUB_TOKEN", "GIT_CONFIG_PARAMETERS"} {
		if !slices.Contains(strip, k) {
			t.Errorf("HostStripEnv lacks %s", k)
		}
	}
	if s := h.HostStripEnv(config.Bindings{AWSConns: twoConns()}); slices.Contains(s, "GH_TOKEN") {
		t.Error("an AWS-only session strips GH_TOKEN")
	}
	// A relative gh would run the shim again: refused.
	if _, _, err := h.HostFor("s3", allKinds(), nil, "gh"); err == nil {
		t.Error("a relative AGENTBOX_GH_REAL was accepted")
	}
}

func TestHostFilesQuotePathsWithSpaces(t *testing.T) {
	if got, want := shq(`it's a b`), `'it'\''s a b'`; got != want {
		t.Fatalf("shq = %s, want %s", got, want)
	}
	if out, err := exec.Command("sh", "-c", "printf %s "+shq(`it's "a" $b`)).Output(); err != nil || string(out) != `it's "a" $b` {
		t.Fatalf("sh read shq back as %q (%v)", out, err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	base := shortDir(t)
	root := filepath.Join(base, "a b", "st")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var (
		mu   sync.Mutex
		seen []string
	)
	serve := func(l net.Listener) {
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
			_, host, _ := presenterOf(r.Context())
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			seen = append(seen, host+" "+string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"Version":1,"AccessKeyId":"AKIAHOST","SecretAccessKey":"s","SessionToken":"t"}`))
		})
		srv := &http.Server{Handler: mux, BaseContext: BaseContext}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Close() })
	}
	h := hostHooks(t, root, serve)
	env, dir, err := h.HostFor("s1", config.Bindings{AWSConns: twoConns()}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dir, "a b") {
		t.Fatalf("dir %s has no space", dir)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "aws-config"))
	if err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, l := range strings.Split(string(cfg), "\n") {
		if strings.HasPrefix(l, "credential_process = ") && strings.HasSuffix(l, " acme-prod-read") {
			line = strings.TrimPrefix(l, "credential_process = ")
		}
	}
	if want := "node '" + dir + "/agentbox-cred.js' aws acme-prod-read"; line != want {
		t.Fatalf("credential_process = %q, want %q", line, want)
	}
	// What the AWS SDKs do with credential_process on a POSIX host: sh -c.
	cmd := exec.Command("sh", "-c", line)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + base, TokenEnv + "=sess-tok"}, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("credential_process failed: %v", err)
	}
	var creds map[string]any
	if err := json.Unmarshal(out, &creds); err != nil || creds["AccessKeyId"] != "AKIAHOST" {
		t.Fatalf("credential_process printed %q (%v)", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != `s1 {"name":"acme-prod-read"}` {
		t.Fatalf("broker saw %q, want one request on s1's own socket", seen)
	}
}

func TestHostSSHRemoteRewrittenToHTTPS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	env, _, err := h.HostFor("s1", config.Bindings{GitHub: ghTwoOwners()}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"git@github.com:o/r.git", "ssh://git@github.com/o/r.git"} {
		cmd := exec.Command("git", "ls-remote", "--get-url", in)
		cmd.Dir = t.TempDir()
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null"}, env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git ls-remote --get-url %s: %v", in, err)
		}
		if got := strings.TrimSpace(string(out)); got != "https://github.com/o/r.git" {
			t.Errorf("%s -> %s, want https://github.com/o/r.git", in, got)
		}
	}
}

func TestReleaseHostRemovesDir(t *testing.T) {
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	_, dir, err := h.HostFor("s1", allKinds(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Daemon shutdown: the socket closes, the files stay for the reattach.
	h.ReleaseHost("s1", false)
	if _, err := os.Lstat(filepath.Join(dir, "s")); !os.IsNotExist(err) {
		t.Fatalf("socket still there after release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aws-config")); err != nil {
		t.Fatalf("files gone after a keep release: %v", err)
	}
	// Two references: the first end keeps the dir, the last removes it.
	for range 2 {
		if _, _, err := h.HostFor("s1", allKinds(), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	h.ReleaseHost("s1", true)
	if _, err := os.Stat(filepath.Join(dir, "s")); err != nil {
		t.Fatalf("a held session lost its socket dir: %v", err)
	}
	h.ReleaseHost("s1", true)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still there after the session ended: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "broker", "h")); err != nil {
		t.Fatalf("broker/h went with the session: %v", err)
	}
	// Ending a session never seen is a no-op.
	h.ReleaseHost("nope", true)
}

func TestSweepHostDirsKeepsLive(t *testing.T) {
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	_, d1, err := h.HostFor("s1", allKinds(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_, d2, err := h.HostFor("s2", allKinds(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	h.ReleaseHost("s1", false) // reattached below by keep only
	hostsDir := filepath.Join(root, "broker", "h")
	stale := filepath.Join(hostsDir, HostDirName("gone"))
	if err := os.MkdirAll(filepath.Join(stale, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "aws-config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(hostsDir, "not-ours")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.SweepHostDirs([]string{"s1"})
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("stale dir survived the sweep: %v", err)
	}
	for _, d := range []string{d1, d2, other} {
		if _, err := os.Lstat(d); err != nil {
			t.Errorf("%s was swept: %v", d, err)
		}
	}
	// No broker/h at all: nothing to do.
	hostHooks(t, shortDir(t), nil).SweepHostDirs(nil)
}

func TestWriteAWSListingRefreshesHostDirs(t *testing.T) {
	root := shortDir(t)
	h := hostHooks(t, root, nil)
	bs := config.Bindings{AWSConns: twoConns()}
	other := config.Bindings{AWSConns: twoConns()[:1]}
	_, d1, err := h.HostFor("s1", bs, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_, d2, err := h.HostFor("s2", other, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]awsprobe.Result{"acme-prod-read": {State: awsprobe.Writes, Actions: []string{"s3:PutObject"},
		CheckedAt: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}}
	// Only Host sessions hold this binding: no binding dir, and that is fine.
	if err := h.WriteAWSListing(bs, base); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(d1, AWSListFile)); string(b) != string(AWSListing(bs.AWSConns, base)) {
		t.Errorf("host aws.json not refreshed: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(d2, AWSListFile)); string(b) != string(AWSListing(other.AWSConns, nil)) {
		t.Errorf("another binding's host aws.json changed: %s", b)
	}
	// With a box on the same binding, its dir is refreshed too.
	bdir, err := EnsureBindingDirWith(root, bs, nil)
	if err != nil {
		t.Fatal(err)
	}
	base["acme-stg-read"] = awsprobe.Result{State: awsprobe.NoneFound}
	if err := h.WriteAWSListing(bs, base); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{d1, bdir} {
		if b, _ := os.ReadFile(filepath.Join(d, AWSListFile)); string(b) != string(AWSListing(bs.AWSConns, base)) {
			t.Errorf("%s/aws.json not refreshed: %s", d, b)
		}
	}
	// An ended session's dir is not recreated.
	h.ReleaseHost("s1", true)
	if err := h.WriteAWSListing(bs, base); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d1); !os.IsNotExist(err) {
		t.Errorf("an ended session's dir came back: %v", err)
	}
	// No box and no Host session: the box's error, unchanged.
	if err := h.WriteAWSListing(config.Bindings{AWSConns: twoConns()[1:]}, nil); err == nil {
		t.Error("no dir at all, no error")
	}
}
