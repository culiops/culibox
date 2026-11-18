package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/boxvm"
)

// Platform is the user's supervisor and where its files live. Both platforms
// compile everywhere; GOOS picks one, so the darwin path is tested on Linux.
type Platform struct {
	GOOS      string
	UID       int
	User      string // login name: the linger file and the loginctl hint
	Home      string
	XDGConfig string // $XDG_CONFIG_HOME as set; used only when absolute
	LingerDir string // /var/lib/systemd/linger
	Run       boxvm.Runner
	Sleep     func(time.Duration)
}

// Real is this machine's platform, with the home dir and $XDG_CONFIG_HOME
// main resolved once. On Linux the unit dir follows the user manager's
// XDG_CONFIG_HOME when it can be asked, not this shell's: a value exported
// only in a shell rc file would put the unit where systemd never looks.
func Real(home, xdgConfig string) Platform {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	p := Platform{GOOS: runtime.GOOS, UID: os.Getuid(), User: name, Home: home,
		XDGConfig: xdgConfig, LingerDir: "/var/lib/systemd/linger",
		Run: boxvm.ExecRunner{}, Sleep: time.Sleep}
	p.XDGConfig = p.managerXDG(xdgConfig)
	return p
}

// managerXDG is the systemd user manager's XDG_CONFIG_HOME ("" when it has
// none), or fallback when the manager cannot be asked or this is not Linux.
func (p Platform) managerXDG(fallback string) string {
	if p.GOOS != "linux" {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := p.Run.Run(ctx, nil, "systemctl", "--user", "show-environment")
	if err != nil {
		return fallback
	}
	return envFromShow(out, "XDG_CONFIG_HOME")
}

// envFromShow reads one variable from `systemctl show-environment` output. A
// value systemd had to quote ($'...') is not a plain path: "".
func envFromShow(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok && !strings.HasPrefix(v, "$'") {
			return v
		}
	}
	return ""
}

func (p Platform) Supported() bool { return p.GOOS == "linux" || p.GOOS == "darwin" }

// UnitPath is where install writes: the systemd --user unit dir (systemd
// honours only an absolute $XDG_CONFIG_HOME), or ~/Library/LaunchAgents.
func (p Platform) UnitPath() string {
	if p.GOOS == "darwin" {
		return filepath.Join(p.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	}
	base := filepath.Join(p.Home, ".config")
	if filepath.IsAbs(p.XDGConfig) {
		base = p.XDGConfig
	}
	return filepath.Join(base, "systemd", "user", SystemdUnit)
}

// Linger reports whether systemd keeps this user's manager (and so the
// service) running with no login session. ok is false off Linux. It reads the
// file loginctl itself checks: no exec, no privilege.
func (p Platform) Linger() (on, ok bool) {
	if p.GOOS != "linux" || p.User == "" {
		return false, false
	}
	_, err := os.Stat(filepath.Join(p.LingerDir, p.User))
	return err == nil, true
}

// State is what status, doctor and install know about the service.
type State struct {
	Path    string
	File    FileState
	Exe     string // the program the file runs (ours only)
	Running bool
	Detail  string // systemd: active, inactive, failed, ...; launchd: running, not running, not loaded
	PID     string
	// Linger is meaningful only when LingerKnown (Linux).
	Linger, LingerKnown bool
}

func (p Platform) domain() string { return fmt.Sprintf("gui/%d", p.UID) }
func (p Platform) target() string { return p.domain() + "/" + LaunchdLabel }

func (p Platform) unsupported() error {
	return fmt.Errorf("culibox service is not supported on %s (only Linux with systemd and macOS)", p.GOOS)
}

// systemdStates are the words `systemctl is-active` answers with; anything
// else (a bus error) means the user manager could not be asked.
var systemdStates = []string{"active", "activating", "reloading", "inactive", "failed", "deactivating", "maintenance", "refreshing"}

// Status reads the file and asks the supervisor; it never starts or stops
// anything. The supervisor is asked only about a file that is ours. The error
// is "could not tell": an unsupported OS or an unreachable supervisor.
func (p Platform) Status(ctx context.Context) (State, error) {
	st := State{Path: p.UnitPath()}
	st.Linger, st.LingerKnown = p.Linger()
	if !p.Supported() {
		return st, p.unsupported()
	}
	file, err := Inspect(st.Path)
	st.File = file
	if err != nil {
		return st, err
	}
	if file != Ours {
		return st, nil
	}
	if b, err := readHead(st.Path); err == nil {
		st.Exe, _ = ParseExe(p.GOOS, b)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if p.GOOS == "darwin" {
		out, err := p.Run.Run(ctx, nil, "launchctl", "print", p.target())
		if err != nil {
			if strings.Contains(out, "Could not find service") {
				st.Detail = "not loaded"
				return st, nil
			}
			return st, fmt.Errorf("launchctl print %s: %v", p.target(), err)
		}
		st.Detail = "not running"
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "state = "); ok && v == "running" {
				st.Running, st.Detail = true, "running"
			}
			if v, ok := strings.CutPrefix(line, "pid = "); ok {
				st.PID = v
			}
		}
		return st, nil
	}
	out, err := p.Run.Run(ctx, nil, "systemctl", "--user", "is-active", SystemdUnit)
	word := strings.TrimSpace(firstLine(out))
	known := false
	for _, s := range systemdStates {
		known = known || word == s
	}
	if !known {
		if err == nil {
			err = errors.New(word)
		}
		return st, fmt.Errorf("systemctl --user is not reachable: %v", err)
	}
	st.Detail, st.Running = word, word == "active"
	if st.Running {
		if pid, err := p.Run.Run(ctx, nil, "systemctl", "--user", "show", "-p", "MainPID", "--value", SystemdUnit); err == nil {
			st.PID = strings.TrimSpace(pid)
		}
	}
	return st, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// readHead reads up to 64 KiB of the file (a unit is far smaller), refusing a
// symlink.
func readHead(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}
