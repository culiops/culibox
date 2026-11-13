package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/procinfo"
	"github.com/culiops/culibox/internal/rootmove"
)

func input(t *testing.T) Input {
	t.Helper()
	data, err := os.MkdirTemp("/tmp", "abdoc-") // short: the socket-length check uses it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	_ = os.Chmod(data, 0o700)
	return Input{
		Config: config.Config{
			BindAddr: "127.0.0.1:1", DataDir: data,
			AbducoSocketDir: filepath.Join(data, "s"),
			Agents:          []config.AgentConfig{{Name: "sh", Bin: "/bin/sh"}},
		},
		Source: config.Source{Path: "/x/config.json"},
		Home:   filepath.Dir(data), Commit: "abc123",
	}
}

func run(in Input) (string, int) {
	var b bytes.Buffer
	code := Run(&b, in)
	return b.String(), code
}

func needAbduco(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("abduco"); err != nil {
		// CI sets AGENT_BOX_REQUIRE_ABDUCO=1: a missing abduco must fail there, not skip.
		if os.Getenv("AGENT_BOX_REQUIRE_ABDUCO") == "1" {
			t.Fatal("abduco not installed and AGENT_BOX_REQUIRE_ABDUCO=1")
		}
		t.Skip("abduco is not on PATH")
	}
}

func TestDoctor_NeverPrintsSecrets(t *testing.T) {
	in := input(t)
	const pass = "S3cret-Passw0rd-value"
	t.Setenv("AB_DOCTOR_PASS", pass)
	in.Config.PasswordEnv = "AB_DOCTOR_PASS"
	out, _ := run(in)
	if strings.Contains(out, pass) {
		t.Fatalf("password leaked:\n%s", out)
	}

	in = input(t)
	tok := strings.Repeat("A", 43)
	_ = os.WriteFile(filepath.Join(in.Config.DataDir, "login.token"), []byte(tok), 0o600)
	out, _ = run(in)
	if strings.Contains(out, tok) {
		t.Fatalf("login token leaked:\n%s", out)
	}
	if h, _ := os.Hostname(); h != "" && strings.Contains(out, h) {
		t.Fatalf("hostname printed (only its length may be):\n%s", out)
	}
	if strings.Contains(out, in.Home+"/") {
		t.Fatalf("home path not shown as ~:\n%s", out)
	}
}

func TestDoctor_ConfigErrorFails(t *testing.T) {
	in := input(t)
	in.ConfigErr = errors.New("parse config: boom")
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL config") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

func TestDoctor_EmptyHomeFailsCleanly(t *testing.T) {
	in := input(t)
	in.Home = ""
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL home:") {
		t.Fatalf("code=%d\n%s", code, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "PASS data dir") {
			t.Errorf("must not derive paths from an empty home:\n%s", out)
		}
	}
}

func TestDoctor_HealthyLinuxBoxPasses(t *testing.T) {
	needAbduco(t)
	in := input(t)
	out, code := run(in)
	if code != 0 {
		t.Fatalf("expected no FAIL on this box, got:\n%s", out)
	}
	for _, check := range []string{"platform", "config", "data dir", "auth", "bind", "abduco binary",
		"abduco roundtrip", "procinfo", "socket length", "shell"} {
		if !strings.Contains(out, " "+check+":") {
			t.Errorf("missing check %q in:\n%s", check, out)
		}
	}
}

func TestDoctor_MissingAbducoFails(t *testing.T) {
	in := input(t)
	in.Config.AbducoBin = "abduco-does-not-exist"
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL abduco binary") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// Doctor must not PASS a config that serve refuses: a profile name that blows
// the socket-path limit fails the same helper serve uses.
func TestDoctor_SocketLengthMatchesServe(t *testing.T) {
	in := input(t)
	in.Config.Agents = append(in.Config.Agents, config.AgentConfig{Name: strings.Repeat("x", 120), Bin: "/bin/sh"})
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL socket length") {
		t.Fatalf("code=%d\n%s", code, out)
	}
}

// The quick-shell profile serve appends is part of the length check.
func TestDoctor_SocketLengthIncludesDetectedShell(t *testing.T) {
	in := input(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("s", 90))
	if err := os.WriteFile(long, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", long)
	in.Config.Agents = []config.AgentConfig{{Name: "a", Bin: "/bin/true"}}
	out, _ := run(in)
	if !strings.Contains(out, "FAIL socket length") {
		t.Fatalf("detected shell profile name ignored:\n%s", out)
	}
}

func TestDoctor_ShellSourceReported(t *testing.T) {
	in := input(t)
	t.Setenv("SHELL", "/bin/sh")
	out, _ := run(in)
	if !strings.Contains(out, "shell: /bin/sh (from $SHELL)") {
		t.Fatalf("shell source missing:\n%s", out)
	}
}

// The doctor probe must leave nothing behind: no temp dir, no live process.
func TestDoctor_LeavesNoSessionBehind(t *testing.T) {
	needAbduco(t)
	root, err := os.MkdirTemp("/tmp", "abdp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	old := probeRoot
	probeRoot = root
	t.Cleanup(func() { probeRoot = old })

	in := input(t)
	out, code := run(in)
	if code != 0 || !strings.Contains(out, "PASS abduco roundtrip") {
		t.Fatalf("probe did not run:\n%s", out)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("probe left files behind: %v", left)
	}
	pids, _ := procinfo.PIDs()
	for _, p := range pids {
		args, err := procinfo.Args(p)
		if err == nil && strings.Contains(strings.Join(args, " "), root) {
			t.Errorf("probe left process %d running: %v", p, args)
		}
	}
}

// A planted FIFO or symlink in ~/.claude/sessions must neither hang doctor nor
// be followed.
func TestDoctor_RegistryIsReadDefensively(t *testing.T) {
	in := input(t)
	in.Home = t.TempDir()
	dir := filepath.Join(in.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.json")
	body := fmt.Sprintf(`{"pid":%d,"procStart":"TOPSECRET-LINK-TARGET"}`, os.Getpid())
	_ = os.WriteFile(secret, []byte(body), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "2.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { out, _ := run(in); done <- out }()
	select {
	case out := <-done:
		if strings.Contains(out, "TOPSECRET") {
			t.Errorf("followed a symlink:\n%s", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("doctor hung on a planted FIFO")
	}
}

func TestDoctor_RegistryCompareShowsBothValues(t *testing.T) {
	in := input(t)
	in.Home = t.TempDir()
	dir := filepath.Join(in.Home, ".claude", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	st, err := procinfo.StartTime(os.Getpid())
	if err != nil {
		t.Skip(err)
	}
	body := fmt.Sprintf(`{"pid":%d,"procStart":%q,"cwd":"/very/private/cwd"}`, os.Getpid(), st)
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid())), []byte(body), 0o600)
	out, _ := run(in)
	if !strings.Contains(out, "PASS claude registry: pid") || !strings.Contains(out, "match=true") {
		t.Fatalf("registry compare missing:\n%s", out)
	}
	if strings.Contains(out, "/very/private/cwd") {
		t.Fatalf("registry cwd leaked:\n%s", out)
	}
}

func TestTilde(t *testing.T) {
	for _, c := range []struct{ in, home, want string }{
		{"/home/a/x and /home/a", "/home/a", "~/x and ~"},
		{"/home/ab/x", "/home/a", "/home/ab/x"},
		{"x", "", "x"},
		{"/home/a: err", "/home/a", "~: err"},
		{"/Users/a/.local/share/x", "/Users/a/", "~/.local/share/x"},
		{"/Users/a", "/Users/a/", "~"},
	} {
		if got := tilde(c.in, c.home); got != c.want {
			t.Errorf("tilde(%q,%q) = %q want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestProcinfoStep_NamesTheBrokenStep(t *testing.T) {
	okArgs := func(int) ([]string, error) { return []string{"abduco", "-n", "doctor-1", "SECRET-ARGV"}, nil }
	got := procinfoStep(10, "doctor-1", func(int) (int, error) { return 9, nil }, okArgs)
	if got != "Ppid(inner)=9 err=<nil>; Args(9) has abduco=true id=true err=<nil>" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "SECRET-ARGV") {
		t.Errorf("argv printed: %q", got)
	}
	bad := procinfoStep(10, "doctor-1", func(int) (int, error) { return 1, nil },
		func(int) ([]string, error) { return []string{"launchd"}, nil })
	if bad != "Ppid(inner)=1 err=<nil>; Args(1) has abduco=false id=false err=<nil>" {
		t.Errorf("got %q", bad)
	}
	noPpid := procinfoStep(10, "doctor-1", func(int) (int, error) { return 0, errors.New("sysctl: boom") }, okArgs)
	if noPpid != "Ppid(inner)=0 err=sysctl: boom; Args skipped" {
		t.Errorf("got %q", noPpid)
	}
	noArgs := procinfoStep(10, "doctor-1", func(int) (int, error) { return 9, nil },
		func(int) ([]string, error) { return nil, errors.New("kern.procargs2: boom") })
	if noArgs != "Ppid(inner)=9 err=<nil>; Args(9) has abduco=false id=false err=kern.procargs2: boom" {
		t.Errorf("got %q", noArgs)
	}
}

func TestRedactHost(t *testing.T) {
	if got := redactHost("create: bind /tmp/x/doctor-1@Mac-Pro.local failed", "Mac-Pro.local"); got != "create: bind /tmp/x/doctor-1@<host> failed" {
		t.Errorf("got %q", got)
	}
	if got := redactHost("abc", ""); got != "abc" {
		t.Errorf("empty host must not rewrite: %q", got)
	}
	for _, h := range []string{"localhost", "LocalHost"} {
		if got := redactHost("localhost:1", h); got != "localhost:1" {
			t.Errorf("host %q must be left alone: %q", h, got)
		}
	}
}

func TestDoctor_RunRedactsHostnameFromAbducoErrors(t *testing.T) {
	// A fake abduco that fails and echoes a socket path with the hostname.
	dir := t.TempDir()
	host, _ := os.Hostname()
	fake := filepath.Join(dir, "abduco")
	script := "#!/bin/sh\necho \"cannot bind /x/doctor-1@" + host + "\" >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := input(t)
	in.Config.AbducoBin = fake
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL abduco roundtrip") {
		t.Fatalf("code=%d\n%s", code, out)
	}
	if strings.Contains(out, host) || !strings.Contains(out, "doctor-1@<host>") {
		t.Fatalf("hostname not redacted:\n%s", out)
	}
}

func TestDoctor_BindLocalhostIsCaseInsensitive(t *testing.T) {
	in := input(t)
	in.Config.BindAddr = "LocalHost:1"
	out, _ := run(in)
	if !strings.Contains(out, "PASS bind:") {
		t.Fatalf("LocalHost must count as loopback:\n%s", out)
	}
}

// A binary that forks a child holding stdout must not hang doctor past the cap.
func TestDoctor_AgentVersionCannotHang(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30 &\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := input(t)
	in.Config.Agents = []config.AgentConfig{{Name: "slow", Bin: bin}}
	start := time.Now()
	out, _ := run(in)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("doctor took %v\n%s", d, out)
	}
}

// The hostname is redacted AFTER ~ is applied: a host that is a substring of
// $HOME ("mac" in /Users/macuser) must not break the ~ rewrite.
func TestDoctor_HostInsideHomeStillTilded(t *testing.T) {
	in := input(t)
	in.Home = "/Users/macuser"
	in.Config.DataDir = "/Users/macuser/.local/share/agent-box"
	in.Config.AbducoSocketDir = ""
	var b bytes.Buffer
	runWithHost(&b, in, "mac")
	out := b.String()
	if !strings.Contains(out, "~/.local/share/agent-box") {
		t.Fatalf("home not shown as ~:\n%s", out)
	}
	if strings.Contains(out, "/Users/") {
		t.Fatalf("home leaked (host redaction ran before ~):\n%s", out)
	}
}

// bind and port echo the user's configured address, which may well be the
// machine's hostname: it must be redacted like everything else. A hostname of
// "" or "localhost" is not redacted (it would mangle the loopback address).
func TestDoctor_BindHostnameRedacted(t *testing.T) {
	in := input(t)
	in.Config.BindAddr = "alices-mbp.local:8899"
	in.Config.AllowedHosts = []string{"alices-mbp.local:8899"}
	var b bytes.Buffer
	runWithHost(&b, in, "alices-mbp")
	if strings.Contains(b.String(), "alices-mbp") {
		t.Fatalf("hostname printed:\n%s", b.String())
	}
	for _, h := range []string{"", "localhost", "LocalHost"} {
		in := input(t)
		in.Config.BindAddr = "localhost:8899"
		var b bytes.Buffer
		runWithHost(&b, in, h)
		if !strings.Contains(b.String(), "localhost:8899") {
			t.Errorf("host %q must not mangle the bind line:\n%s", h, b.String())
		}
	}
}

func TestProcTableResult(t *testing.T) {
	cases := []struct {
		name   string
		ts     procinfo.TableStats
		status string
		want   []string
	}{
		{"mac measured full", procinfo.TableStats{Procs: 3991, Max: 4000, Zombies: 3377, Own: 600, MaxPerUID: 2666},
			Warn, []string{"3991 of 4000 processes", "3377 zombies", "own uid 600 of 2666", "EAGAIN", "ps -axo ppid=,stat="}},
		{"mac healthy", procinfo.TableStats{Procs: 600, Max: 4000, Own: 400, MaxPerUID: 2666},
			Pass, []string{"600 of 4000 processes", "own uid 400 of 2666"}},
		{"just under 5% headroom", procinfo.TableStats{Procs: 3801, Max: 4000},
			Warn, []string{"3801 of 4000"}},
		{"exactly 5% headroom", procinfo.TableStats{Procs: 3800, Max: 4000},
			Pass, []string{"3800 of 4000"}},
		{"only the per-uid limit is near", procinfo.TableStats{Procs: 2700, Max: 4000, Own: 2600, MaxPerUID: 2666},
			Warn, []string{"own uid 2600 of 2666"}},
		{"no limit known (linux)", procinfo.TableStats{Procs: 412},
			Pass, []string{"412 processes"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := procTableResult(c.ts)
			if r.Status != c.status || r.Check != "process table" {
				t.Fatalf("got %+v, want status %s", r, c.status)
			}
			for _, w := range c.want {
				if !strings.Contains(r.Detail, w) {
					t.Errorf("detail %q missing %q", r.Detail, w)
				}
			}
			if c.status == Pass && strings.Contains(r.Detail, "EAGAIN") {
				t.Errorf("a passing line carries the warning hint: %q", r.Detail)
			}
		})
	}
}

func TestDoctor_ReportsProcessTable(t *testing.T) {
	out, _ := run(input(t))
	if !strings.Contains(out, " process table: ") {
		t.Fatalf("no process table line:\n%s", out)
	}
}

// --- data root rows (#255) ---

func dataRootIn(t *testing.T, kind config.RootKind, legacy bool) (in Input, newRoot, legacyRoot string) {
	t.Helper()
	home := t.TempDir()
	newRoot, legacyRoot = config.DefaultRoots(home)
	path := newRoot
	if legacy {
		path = legacyRoot
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	in = Input{Home: home, Source: config.Source{Root: config.RootInfo{Path: path, Kind: kind}}}
	return in, newRoot, legacyRoot
}

func TestDataDirRowLegacy(t *testing.T) {
	in, newRoot, legacy := dataRootIn(t, config.RootLegacy, true)
	r := checkDataDir(in, legacy, "linux")
	if r.Status != Pass || !strings.Contains(r.Detail, "next serve start moves it to "+newRoot) {
		t.Fatalf("%+v", r)
	}
}

func TestDataDirRowLegacyKeptInPlace(t *testing.T) {
	in, _, legacy := dataRootIn(t, config.RootLegacy, true)
	in.RootNote = "2 session(s) running"
	r := checkDataDir(in, legacy, "linux")
	if !strings.Contains(r.Detail, "kept in place: 2 session(s) running") || strings.Contains(r.Detail, "moves it") {
		t.Fatalf("%+v", r)
	}
}

// R1: on darwin a legacy root that holds the box VM stays; doctor says why.
func TestDataDirRowLegacyDarwinVM(t *testing.T) {
	in, _, legacy := dataRootIn(t, config.RootLegacy, true)
	if err := os.Mkdir(filepath.Join(legacy, "vm"), 0o700); err != nil {
		t.Fatal(err)
	}
	const want = "kept in place: the box VM lives here; it cannot follow a move without box setup -recreate"
	r := checkDataDir(in, legacy, "darwin")
	if !strings.Contains(r.Detail, want) || strings.Contains(r.Detail, "moves it") {
		t.Fatalf("darwin with a VM: %+v", r)
	}
	// Linux has no precondition (f): the same layout is simply pending.
	if r := checkDataDir(in, legacy, "linux"); !strings.Contains(r.Detail, "next serve start moves it to") {
		t.Fatalf("linux with vm dir: %+v", r)
	}
	// darwin without a VM moves like Linux.
	if err := os.Remove(filepath.Join(legacy, "vm")); err != nil {
		t.Fatal(err)
	}
	if r := checkDataDir(in, legacy, "darwin"); !strings.Contains(r.Detail, "next serve start moves it to") {
		t.Fatalf("darwin without a VM: %+v", r)
	}
}

// R17: a legacy root that is a symlink (or a mount point) is never moved, so
// doctor does not promise a move.
func TestDataDirRowLegacyNotARealDirectory(t *testing.T) {
	in, _, legacy := dataRootIn(t, config.RootLegacy, true)
	real := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(legacy, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, legacy); err != nil {
		t.Fatal(err)
	}
	r := checkDataDir(in, legacy, "linux")
	if !strings.Contains(r.Detail, "(kept in place: not a real directory)") || strings.Contains(r.Detail, "moves it") {
		t.Fatalf("%+v", r)
	}
}

// R17: live sessions on the legacy root keep it in place; doctor counts them
// from the socket dir Checks uses and <legacy>/sessions/pids.
func TestDataDirRowLegacyLiveSessions(t *testing.T) {
	in, _, legacy := dataRootIn(t, config.RootLegacy, true)
	var gotSock, gotPids string
	real := liveSessions
	liveSessions = func(sock, pids string) (int, error) { gotSock, gotPids = sock, pids; return 2, nil }
	t.Cleanup(func() { liveSessions = real })
	r := checkDataDir(in, legacy, "linux")
	if !strings.Contains(r.Detail, "(kept in place: 2 session(s) running)") || strings.Contains(r.Detail, "moves it") {
		t.Fatalf("%+v", r)
	}
	if want := abduco.SocketDirFor("", "linux", legacy, os.Getuid()); gotSock != want {
		t.Errorf("socket dir = %q, want %q", gotSock, want)
	}
	if want := filepath.Join(legacy, "sessions", "pids"); gotPids != want {
		t.Errorf("pid dir = %q, want %q", gotPids, want)
	}
	// None running: the move is still promised.
	liveSessions = func(string, string) (int, error) { return 0, nil }
	if r := checkDataDir(in, legacy, "linux"); !strings.Contains(r.Detail, "next serve start moves it to") {
		t.Fatalf("no sessions: %+v", r)
	}
}

// Doctor stays read-only: counting sessions creates no directory.
func TestDataDirRowLegacyCreatesNothing(t *testing.T) {
	in, _, legacy := dataRootIn(t, config.RootLegacy, true)
	checkDataDir(in, legacy, "linux")
	for _, d := range []string{filepath.Join(legacy, "abduco"), filepath.Join(legacy, "sessions")} {
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s was created (%v)", d, err)
		}
	}
}

func TestDataDirRowLinked(t *testing.T) {
	in, newRoot, legacy := dataRootIn(t, config.RootLinked, false)
	r := checkDataDir(in, newRoot, "linux")
	if r.Status != Pass || !strings.Contains(r.Detail, "moved from "+legacy) || !strings.Contains(r.Detail, "a link remains at the old path") {
		t.Fatalf("%+v", r)
	}
}

func TestDataDirRowInterruptedMove(t *testing.T) {
	in, newRoot, _ := dataRootIn(t, config.RootNew, false)
	if err := os.WriteFile(filepath.Join(newRoot, rootmove.JournalName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := checkDataDir(in, newRoot, "linux")
	if !strings.Contains(r.Detail, "a move was interrupted; the next serve start finishes it") {
		t.Fatalf("%+v", r)
	}
}

// A data_dir elsewhere is not the root: no root story is told about it.
func TestDataDirRowNoStoryForCustomDataDir(t *testing.T) {
	in, _, _ := dataRootIn(t, config.RootLegacy, true)
	other := t.TempDir()
	if err := os.Chmod(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := checkDataDir(in, other, "linux"); r.Detail != other {
		t.Fatalf("%+v", r)
	}
}

func TestEnvNoticeRow(t *testing.T) {
	in := input(t)
	in.EnvNotice = "AGENT_BOX_HOME is deprecated; rename it to CULIBOX_HOME"
	var got *Result
	for _, r := range Checks(in) {
		if r.Check == "env" {
			r := r
			got = &r
		}
	}
	if got == nil || got.Status != Warn || !strings.Contains(got.Detail, "CULIBOX_HOME") {
		t.Fatalf("env row: %+v", got)
	}
}

func TestEnvErrorFails(t *testing.T) {
	in := input(t)
	in.EnvErr = errors.New("CULIBOX_HOME (/a) and AGENT_BOX_HOME (/b) disagree; unset one")
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL env: CULIBOX_HOME (/a) and AGENT_BOX_HOME (/b) disagree") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

// The two-roots refusal reaches doctor as the config FAIL.
func TestRootRefusalFails(t *testing.T) {
	in := input(t)
	in.ConfigErr = errors.New("both /a and /b exist with different content; set CULIBOX_HOME to the one to use, or move one aside")
	in.Source = config.Source{Path: "data root"}
	out, code := run(in)
	if code != 1 || !strings.Contains(out, "FAIL config: data root: both /a and /b exist") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}
