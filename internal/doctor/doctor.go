// Package doctor checks whether this machine can run agent-box and prints a
// report meant to be pasted into a chat or an issue. It is the darwin test
// harness too: each check exercises the real code path (procinfo, abduco) live.
//
// Two hard rules. It is READ-ONLY toward live state: it never binds the port,
// takes the instance lock, starts a manager, sweeps orphan attachers or
// reconciles — any of those would kill or adopt a running instance's sessions.
// And it never prints a secret: no password, token or env value; $HOME is "~"
// and the hostname is a length.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/abduco"
	"github.com/culiops/culibox/internal/auth"
	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/procinfo"
	"github.com/culiops/culibox/internal/rootmove"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/store"
	"github.com/culiops/culibox/internal/storerefs"
)

const (
	Pass = "PASS"
	Warn = "WARN"
	Fail = "FAIL"
)

// probeRoot is where the roundtrip probe makes its throwaway dir. /tmp keeps
// the socket path short (darwin allows 103 bytes); a variable so a test can
// observe that nothing is left behind.
var probeRoot = "/tmp"

// liveSessions is abduco.LiveSessions (it reads only, creating no dir); a
// variable so the data-root row can be tested without a real abduco.
var liveSessions = abduco.LiveSessions

type Input struct {
	Config    config.Config
	Source    config.Source
	ConfigErr error
	Home      string // "" when $HOME was empty, relative or unreadable
	Commit    string
	// CredErrors describes what was wrong with the web-defined credentials file
	// ("<kind> <name>: <err>" per skipped entry, or the file-level problem).
	CredErrors []string
	// ConfigFiles are the config files read on the next start (-config, the
	// ~/.config fallback): their directories are protected from VM shares.
	ConfigFiles []string
	// EnvNotice is config.HomeEnv's deprecation notice ("" when none); EnvErr is
	// its refusal (the two root env vars disagree, or a relative value). Both are
	// rows: a WARN and a FAIL.
	EnvNotice string
	EnvErr    error
	// RootNote is why a running serve kept the old data root in place ("" for the
	// CLI, which only resolves and never tries the move).
	RootNote string
	// Start is what a running serve's start did before it resolved the config
	// (zero for the CLI): the in-app doctor warns on the start that moved the root.
	Start config.StartFacts
}

// protectedFor is the protected-directory list serve and the CLI build
// (box.ProtectedDirsFor), from what doctor knows.
func protectedFor(in Input, dataDir string) []box.ProtectedDir {
	c := in.Config
	return box.ProtectedDirsFor(dataDir, in.ConfigFiles, c.ResolvedProfilesPath(dataDir), bundle.DefaultSource(), bundle.SourceOf(c.BundleSource))
}

type Result struct{ Status, Check, Detail string }

// Run prints every check and returns the exit code: 1 if any check failed.
func Run(w io.Writer, in Input) int {
	host, _ := os.Hostname()
	return runWithHost(w, in, host)
}

// runWithHost is Run with the machine's hostname injected, for the tests.
func runWithHost(w io.Writer, in Input, host string) int {
	code := 0
	for _, r := range Checks(in) {
		fmt.Fprintf(w, "%s %s: %s\n", r.Status, r.Check, Redact(r.Detail, in.Home, host))
		if r.Status == Fail {
			code = 1
		}
	}
	return code
}

// Redact is the one redaction every rendering of a check detail goes through
// (doctor's own output and the bug-report bundle): home becomes "~", then the
// hostname becomes "<host>". The order matters, see redactHost.
func Redact(detail, home, host string) string {
	return redactHost(tilde(detail, home), host)
}

// redactHost is the backstop for the hostname rule: abduco's own error text can
// echo a socket path ("doctor-1@<host>"), and bind / allowed_hosts may name the
// machine. It runs AFTER tilde: a host that is a substring of $HOME ("mac" in
// /Users/macuser) would otherwise stop ~ from matching and leak the home path.
// An empty or "localhost" host is left alone (it would mangle 127.0.0.1-style
// output like "localhost:8899").
func redactHost(detail, host string) string {
	if host == "" || strings.EqualFold(host, "localhost") {
		return detail
	}
	return strings.ReplaceAll(detail, host, "<host>")
}

// tilde shows the home dir as "~". It only matches a whole path component, so
// /home/ab is not rewritten when home is /home/a.
func tilde(s, home string) string {
	if home == "" || home == "/" {
		return s
	}
	home = filepath.Clean(home) // "/Users/a/" must still match "/Users/a/.local"
	if home == "/" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, home)
		if i < 0 {
			break
		}
		end := i + len(home)
		if end < len(s) && isPathChar(s[end]) && s[end] != '/' {
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteByte('~')
		s = s[end:]
	}
	b.WriteString(s)
	return b.String()
}

func isPathChar(c byte) bool {
	return c == '/' || c == '.' || c == '_' || c == '-' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func Checks(in Input) []Result {
	out := []Result{{Pass, "platform", fmt.Sprintf("%s/%s, %s, culibox %s",
		runtime.GOOS, runtime.GOARCH, runtime.Version(), in.Commit)}}
	if in.Home == "" {
		// serve refuses this too; every path below would be built from it.
		return append(out, Result{Fail, "home", "the home directory is empty or not an absolute path; set $HOME"})
	}
	if in.EnvErr != nil {
		return append(out, Result{Fail, "env", in.EnvErr.Error()})
	}
	if in.EnvNotice != "" {
		out = append(out, Result{Warn, "env", in.EnvNotice})
	}
	if in.ConfigErr != nil {
		return append(out, Result{Fail, "config", in.Source.String() + ": " + in.ConfigErr.Error()})
	}
	if err := in.Config.Validate(); err != nil {
		return append(out, Result{Fail, "config", in.Source.String() + ": " + err.Error()})
	}
	out = append(out, Result{Pass, "config", in.Source.String()})
	out = append(out, checkConfigFileWarn(in)...)
	out = append(out, checkExtraCA(in.Config, time.Now())...)
	out = append(out, checkCredFile(in))
	c := in.Config
	dataDir := c.ResolvedDataDir(in.Home)
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	out = append(out, checkDataDir(in, dataDir, runtime.GOOS), checkAuth(c, dataDir), checkBind(c), checkPort(c))
	bin := c.AbducoBin
	if bin == "" {
		bin = "abduco"
	}
	ab := checkAbducoBinary(bin)
	out = append(out, ab)
	if ab.Status != Fail {
		out = append(out, checkRoundtrip(bin))
	}
	out = append(out, checkProcinfoSelf(), checkProcTable())
	socketDir := abduco.SocketDirFor(c.AbducoSocketDir, runtime.GOOS, dataDir, os.Getuid())
	out = append(out, checkSocketLength(c, socketDir))
	if runtime.GOOS == "darwin" {
		out = append(out, checkSocketDir(socketDir))
	}
	sh, src := config.DetectShellSource()
	out = append(out, Result{Pass, "shell", fmt.Sprintf("%s (from %s)", sh, src)})
	out = append(out, checkAgents(c)...)
	vst := StoreFor(c, dataDir)
	out = append(out, checkStaticTokens(c, defaultResolver(vst))...)
	if ost := openStoreForCheck(c, dataDir); ost != nil {
		out = append(out, checkStore(c, ost, storerefs.ReadAccounts(dataDir), os.LookupEnv, runtime.GOOS, dockerSeccomp(c))...)
	}
	var stored []config.AgentConfig
	st, storeErr := store.Open(c.ResolvedProfilesPath(dataDir))
	if storeErr == nil {
		stored = st.List()
	}
	out = append(out, checkAWSConnsGate(context.Background(), c, stored, storeErr, boxvm.ExecRunner{},
		newAWSProber(broker.STSMinter{}, awsprobe.IAMSimulator{}), newSSOGate(broker.STSMinter{}))...)
	bf := gatherBoxFacts(c, dataDir, in.Home, runtime.GOOS, protectedFor(in, dataDir))
	out = append(out, boxTierResults(runtime.GOOS, bf, ClaudeBoxToken(c, vst))...)
	out = append(out, guardLockRows(runtime.GOOS, bf)...)
	if _, err := box.LookDocker(c.DockerBin); err == nil && runtime.GOOS == "linux" {
		out = append(out, CheckHostListeners())
	}
	out = append(out, checkClaudeRegistry(in.Home)...)
	return out
}

func checkDataDir(in Input, dir, goos string) Result {
	fi, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return Result{Pass, "data dir", dir + " (created on first serve)"}
	}
	if err != nil {
		return Result{Fail, "data dir", err.Error()}
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if st == nil || int(st.Uid) != os.Getuid() {
		return Result{Fail, "data dir", dir + " is not owned by you"}
	}
	story := rootStory(in, dir, goos)
	if fi.Mode().Perm()&0o077 != 0 {
		return Result{Warn, "data dir", fmt.Sprintf("%s has mode %o; want 700 (it holds the login and cookie secrets)", dir, fi.Mode().Perm()) + story}
	}
	return Result{Pass, "data dir", dir + story}
}

// rootStory says which data root is in use and why (spec D2 "doctor"), as a
// suffix. doctor never tries the move or its preconditions: it states the rule,
// plus the one darwin fact it can read (a box VM in the old root, precondition
// (f)) and, from a running serve, the reason it kept the old root. Nothing for
// a data_dir elsewhere or a root named by the env var.
func rootStory(in Input, dir, goos string) string {
	root := in.Source.Root
	if root.Path == "" || root.Kind == config.RootEnv || filepath.Clean(root.Path) != filepath.Clean(dir) {
		return ""
	}
	if fi, err := os.Lstat(filepath.Join(dir, rootmove.JournalName)); err == nil && fi.Mode().IsRegular() {
		return " (a move was interrupted; the next serve start finishes it)"
	}
	newRoot, legacy := config.DefaultRoots(in.Home)
	switch root.Kind {
	case config.RootLegacy:
		if goos == "darwin" {
			if _, err := os.Lstat(filepath.Join(dir, "vm")); err == nil {
				return " (kept in place: the box VM lives here; it cannot follow a move without box setup -recreate)"
			}
		}
		if in.RootNote != "" {
			return " (kept in place: " + in.RootNote + ")"
		}
		// Two preconditions doctor can read without changing anything (R17):
		// (b) a real directory, and (d) no live session, counted from the
		// socket dir Checks uses for this data dir.
		if !rootmove.RealDir(legacy) {
			return " (kept in place: not a real directory)"
		}
		sock := abduco.SocketDirFor(in.Config.AbducoSocketDir, goos, dir, os.Getuid())
		if n, err := liveSessions(sock, filepath.Join(legacy, "sessions", "pids")); err == nil && n > 0 {
			return fmt.Sprintf(" (kept in place: %d session(s) running)", n)
		}
		return " (from before the rename; the next serve start moves it to " + newRoot + ")"
	case config.RootLinked:
		return " (moved from " + legacy + "; a link remains at the old path)"
	}
	return ""
}

func checkAuth(c config.Config, dataDir string) Result {
	if c.PasswordEnv != "" {
		v := os.Getenv(c.PasswordEnv)
		switch {
		case v == "":
			return Result{Fail, "auth", "password mode: env " + c.PasswordEnv + " is empty"}
		case auth.WeakPassword(v):
			return Result{Warn, "auth", "password mode: password is shorter than 12 characters"}
		}
		return Result{Pass, "auth", "password mode (env " + c.PasswordEnv + ")"}
	}
	p := filepath.Join(dataDir, auth.LoginTokenFile)
	fi, err := os.Lstat(p) // never follow: the token path is only stat'ed, never read
	if os.IsNotExist(err) {
		return Result{Pass, "auth", "token mode; token generated on first serve"}
	}
	if err != nil {
		return Result{Fail, "auth", "token mode: " + err.Error()}
	}
	if fi.Mode().Perm() != 0o600 {
		return Result{Warn, "auth", fmt.Sprintf("token mode: %s has mode %o, want 600", p, fi.Mode().Perm())}
	}
	return Result{Pass, "auth", "token mode (" + p + ")"}
}

func checkBind(c config.Config) Result {
	host, _, _ := net.SplitHostPort(c.BindAddr)
	ip := net.ParseIP(host)
	detail := fmt.Sprintf("%s; allowed hosts: %s", c.BindAddr, strings.Join(c.HostAllowlist(), ", "))
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return Result{Warn, "bind", detail + " — not loopback: reachable from the network"}
	}
	return Result{Pass, "bind", detail}
}

// checkPort dials instead of binding: doctor must not take the port.
func checkPort(c config.Config) Result {
	conn, err := net.DialTimeout("tcp", c.BindAddr, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		return Result{Warn, "port", c.BindAddr + " is in use (a CuliBox may already be running)"}
	}
	return Result{Pass, "port", c.BindAddr + " is free"}
}

func checkAbducoBinary(bin string) Result {
	p, err := exec.LookPath(bin)
	if err != nil {
		hint := "install it (macOS: brew install abduco)"
		return Result{Fail, "abduco binary", fmt.Sprintf("%q not found on PATH; %s", bin, hint)}
	}
	// `abduco -v` prints its version and exits non-zero; only the text matters.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "-v")
	cmd.WaitDelay = time.Second // a forked child holding stdout must not outlast the cap
	v, _ := cmd.CombinedOutput()
	return Result{Pass, "abduco binary", p + " " + strings.TrimSpace(firstLine(string(v)))}
}

// checkRoundtrip drives the real adapter through create → liveness → kill in a
// fresh short temp dir. Create succeeding proves procinfo's Ppid and Args work
// (it polls InnerAlive, which needs both).
func checkRoundtrip(bin string) Result {
	dir, err := os.MkdirTemp(probeRoot, "abdr-")
	if err != nil {
		return Result{Fail, "abduco roundtrip", err.Error()}
	}
	defer os.RemoveAll(dir)
	a, err := abduco.New(bin, filepath.Join(dir, "s"), filepath.Join(dir, "p"))
	if err != nil {
		return Result{Fail, "abduco roundtrip", "adapter: " + err.Error()}
	}
	const id = "doctor-1"
	// Kill is idempotent; the deferred call is the guarantee that no session of
	// ours outlives the probe, whichever step fails.
	defer func() { _ = a.Kill(id) }()
	if err := a.Create(id, dir, []string{"sleep", "30"}, os.Environ()); err != nil {
		detail := "create: " + err.Error()
		if inner, ok := a.InnerPid(id); ok {
			detail += "; " + procinfoStep(inner, id, procinfo.Ppid, procinfo.Args)
		}
		return Result{Fail, "abduco roundtrip", detail}
	}
	inner, _ := a.InnerPid(id)
	sock := abduco.SocketPath(a.SocketDir, a.User(), id, a.Host())
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return Result{Fail, "abduco roundtrip", "socket is not where SocketPath says — the length check is miscounting"}
	}
	if err := a.Kill(id); err != nil {
		return Result{Fail, "abduco roundtrip", "kill: " + err.Error()}
	}
	if syscall.Kill(inner, 0) == nil {
		return Result{Fail, "abduco roundtrip", "kill: the session's process is still alive"}
	}
	return Result{Pass, "abduco roundtrip", "create, liveness, socket path and kill all work"}
}

// procinfoStep names which procinfo call broke when a session could not be
// confirmed: Ppid of the agent, then Args of that parent (the abduco server,
// which must mention abduco and the session id). Booleans only: argv itself is
// never printed.
func procinfoStep(inner int, id string, ppid func(int) (int, error), args func(int) ([]string, error)) string {
	p, perr := ppid(inner)
	msg := fmt.Sprintf("Ppid(inner)=%d err=%v", p, perr)
	if perr != nil {
		return msg + "; Args skipped"
	}
	argv, aerr := args(p)
	joined := strings.Join(argv, " ")
	return msg + fmt.Sprintf("; Args(%d) has abduco=%v id=%v err=%v",
		p, strings.Contains(joined, "abduco"), strings.Contains(joined, id), aerr)
}

func checkProcinfoSelf() Result {
	self := os.Getpid()
	var bad []string
	if p, err := procinfo.Ppid(self); err != nil || p != os.Getppid() {
		bad = append(bad, fmt.Sprintf("Ppid=%d err=%v want %d", p, err, os.Getppid()))
	}
	st, err := procinfo.StartTime(self)
	if err != nil || st == "" {
		bad = append(bad, fmt.Sprintf("StartTime err=%v", err))
	}
	if a, err := procinfo.Args(self); err != nil || len(a) == 0 {
		bad = append(bad, fmt.Sprintf("Args err=%v", err))
	}
	env, err := procinfo.Env(self)
	if err != nil || len(env) == 0 {
		bad = append(bad, fmt.Sprintf("Env err=%v", err))
	}
	pids, err := procinfo.PIDs()
	if err != nil || len(pids) == 0 {
		bad = append(bad, fmt.Sprintf("PIDs err=%v", err))
	}
	if len(bad) > 0 {
		return Result{Fail, "procinfo", strings.Join(bad, "; ")}
	}
	return Result{Pass, "procinfo", fmt.Sprintf("ppid, start time (%s), argv, env (%d vars), %d pids", st, len(env), len(pids))}
}

func checkProcTable() Result {
	ts, err := procinfo.Table()
	if err != nil {
		return Result{Warn, "process table", "could not read: " + err.Error()}
	}
	return procTableResult(ts)
}

// procTableResult warns when either limit has under 5% headroom: every fork on
// the machine (agent spawns, abduco, the shell) then fails with EAGAIN. Counts
// only — never a process name or user.
func procTableResult(ts procinfo.TableStats) Result {
	if ts.Max == 0 {
		return Result{Pass, "process table", fmt.Sprintf("%d processes (no single limit to check on %s)", ts.Procs, runtime.GOOS)}
	}
	detail := fmt.Sprintf("%d of %d processes (%d zombies)", ts.Procs, ts.Max, ts.Zombies)
	near := nearlyFull(ts.Procs, ts.Max)
	if ts.MaxPerUID > 0 {
		detail += fmt.Sprintf("; own uid %d of %d", ts.Own, ts.MaxPerUID)
		near = near || nearlyFull(ts.Own, ts.MaxPerUID)
	}
	if !near {
		return Result{Pass, "process table", detail}
	}
	return Result{Warn, "process table", detail + "; nearly full, new processes fail with EAGAIN. " +
		"Zombies: find the parent not reaping them with " +
		"`ps -axo ppid=,stat= | awk '$2~/Z/{print $1}' | sort | uniq -c | sort -rn | head`"}
}

// nearlyFull is under 5% headroom, in integers.
func nearlyFull(n, max int) bool { return (max-n)*20 < max }

// checkSocketLength asks the same helper serve does, over the same profile list
// (the detected quick shell included), so doctor cannot pass a config that
// serve refuses.
func checkSocketLength(c config.Config, socketDir string) Result {
	host, _ := os.Hostname()
	agents, _ := config.WithDetectedShell(c.Agents)
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	user := abduco.CurrentUser()
	id := abduco.RepresentativeID(names)
	n := len(abduco.SocketPath(socketDir, user, id, host))
	detail := fmt.Sprintf("%d of %d bytes for id %q under %s (hostname is %d bytes)",
		n, abduco.SocketPathLimit(), id, socketDir, len(host))
	if err := abduco.CheckProfileSocketPaths(socketDir, user, host, names); err != nil {
		return Result{Fail, "socket length", detail + "; set abduco_socket_dir to a shorter path or shorten the profile name"}
	}
	return Result{Pass, "socket length", detail}
}

func checkSocketDir(dir string) Result {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return Result{Pass, "socket dir", dir + " (created on first serve)"}
	}
	if err := abduco.CheckSocketDir(dir, os.Getuid()); err != nil {
		return Result{Fail, "socket dir", err.Error()}
	}
	return Result{Pass, "socket dir", dir}
}

func checkAgents(c config.Config) []Result {
	var out []Result
	for _, a := range c.Agents {
		p, err := exec.LookPath(a.Bin)
		if err != nil {
			out = append(out, Result{Warn, "agent " + a.Name, fmt.Sprintf("%q not on PATH", a.Bin)})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, p, "--version")
		cmd.WaitDelay = time.Second
		v, _ := cmd.Output()
		cancel()
		out = append(out, Result{Pass, "agent " + a.Name, p + " " + strings.TrimSpace(firstLine(string(v)))})
	}
	return out
}

// startRe bounds what a registry procStart may look like before it is printed:
// the file is another program's, and doctor output gets pasted around.
var startRe = regexp.MustCompile(`^[0-9A-Za-z :._-]{1,48}$`)

// checkClaudeRegistry compares Claude Code's recorded procStart with what
// procinfo reads, for live sessions, with the same session.StartMatches rule
// attribution uses (darwin's two formats name one instant). A mismatch only
// degrades transcript attribution. The files are read through the session
// package's defensive reader (no symlink following, FIFO-safe, size-capped).
func checkClaudeRegistry(home string) []Result {
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	var out []Result
	for _, f := range files {
		if len(out) == 5 {
			break
		}
		b, ok := session.ReadRegistryFile(f)
		if !ok {
			continue
		}
		var e struct {
			Pid       int             `json:"pid"`
			ProcStart json.RawMessage `json:"procStart"`
		}
		if json.Unmarshal(b, &e) != nil || e.Pid <= 0 {
			continue
		}
		ours, err := procinfo.StartTime(e.Pid)
		if err != nil {
			continue // not running
		}
		theirs := strings.Trim(string(e.ProcStart), `"`)
		match := session.StartMatches(theirs, ours)
		if !startRe.MatchString(theirs) {
			theirs = "<unprintable>"
		}
		st := Pass
		if !match {
			st = Warn
		}
		out = append(out, Result{st, "claude registry", fmt.Sprintf("pid %d: claude=%s culibox=%s match=%v", e.Pid, theirs, ours, match)})
	}
	if len(out) == 0 {
		return []Result{{Warn, "claude registry", "no running Claude Code session to compare; start `claude` in another terminal and rerun"}}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// checkExtraCA is one row per CA in extra_ca_certs, and none when it is unset.
// An expired or not-yet-valid CA warns. A file that no longer loads fails: the
// boxes refuse to start without it.
func checkExtraCA(c config.Config, now time.Time) []Result {
	set, err := c.ExtraCA()
	if err != nil {
		return []Result{{Fail, "extra-ca", err.Error()}}
	}
	var out []Result
	for _, ca := range set.CAs {
		valid, line := ca.Describe(now)
		if valid {
			out = append(out, Result{Pass, "extra-ca", line})
		} else {
			out = append(out, Result{Warn, "extra-ca", line + " (expired or not yet valid)"})
		}
	}
	return out
}
