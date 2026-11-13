package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/secretstore"
)

// initSteps is the number of steps `culibox init` prints in "step N/8".
const initSteps = 8

// initDeps are the seams `culibox init` is tested through: everything that
// looks at the machine, runs a program, reads a secret or reaches into another
// command. realInitDeps wires the real ones.
type initDeps struct {
	goos       string
	getenv     func(string) string // CULIBOX_HOME, AGENT_BOX_HOME, XDG_CONFIG_HOME
	lookPath   func(string) (string, error)
	run        func(name string, args ...string) error // terminal attached (brew, claude setup-token)
	readSecret func() ([]byte, error)                  // no-echo line read
	hostSize   func() (cpus int, memBytes uint64)      // boxvm.HostSize
	macOSMajor func() int                              // 0 = unknown/not darwin
	osRelease  func() string                           // /etc/os-release ID ("" if none)
	vmSetUp    func(cfg config.Config, dataDir string) bool
	boxReady   func(cfg config.Config, dataDir string) bool // darwin: vmSetUp && image present && ClaudeBoxToken Pass; linux: image present && token Pass
	boxSetup   func(cfg config.Config, cfgFiles []string, w io.Writer) int
	shareAdd   func(cfg config.Config, dataDir, path string, w io.Writer) (covered bool, err error)
	doctor     func(cfg config.Config, src config.Source, cfgFiles []string, w io.Writer) int
	newKeeper  func(dataDir string) secretstore.Keeper
}

// realInitDeps wires init to this machine.
func realInitDeps() initDeps {
	return initDeps{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		run: func(name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
		readSecret: func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) },
		hostSize:   boxvm.HostSize,
		macOSMajor: func() int {
			if runtime.GOOS != "darwin" {
				return 0
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return boxvm.MacOSMajor(ctx) // sw_vers -productVersion; 0 on error
		},
		osRelease: osReleaseID,
		// Task 7 replaces these five with the real wiring; init steps 1-2 never call them.
		vmSetUp:  func(config.Config, string) bool { return false },
		boxReady: func(config.Config, string) bool { return false },
		boxSetup: func(_ config.Config, _ []string, w io.Writer) int {
			fmt.Fprintln(w, "box setup is not wired into init yet")
			return 1
		},
		shareAdd: func(config.Config, string, string, io.Writer) (bool, error) {
			return false, errors.New("box share is not wired into init yet")
		},
		doctor:    func(config.Config, config.Source, []string, io.Writer) int { return 1 },
		newKeeper: func(dataDir string) secretstore.Keeper { return newKeeper(dataDir) },
	}
}

// osReleaseID is the ID= of /etc/os-release, unquoted; "" when unreadable.
func osReleaseID() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ID="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// initUI prints the step lines and reads the answers. The reader is shared by
// every step so one buffered reader never loses input to another.
type initUI struct {
	br          *bufio.Reader
	out         io.Writer
	step, total int
}

// ask prints q with "[Y/n]" (def true) or "[y/N]" and returns the answer. An
// empty line is the default; anything but yes/no asks again. End of input or a
// read error is "no", so a closed terminal never installs or writes anything.
func (u *initUI) ask(q string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for {
		fmt.Fprintf(u.out, "  %s %s ", q, hint)
		s, ok := u.readLine()
		if !ok {
			fmt.Fprintln(u.out)
			return false
		}
		switch strings.ToLower(s) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Fprintln(u.out, "  please answer y or n")
	}
}

// line prints q (with the default in brackets when there is one) and returns
// the trimmed answer, or def when the answer is empty. ok is false at end of
// input.
func (u *initUI) line(q, def string) (string, bool) {
	if def != "" {
		fmt.Fprintf(u.out, "  %s [%s] ", q, def)
	} else {
		fmt.Fprintf(u.out, "  %s ", q)
	}
	s, ok := u.readLine()
	if !ok {
		fmt.Fprintln(u.out)
		return "", false
	}
	if s == "" {
		return def, true
	}
	return s, true
}

// readLine reads one line, trimmed. A last line without a newline still counts;
// ok is false only when there is nothing to read.
func (u *initUI) readLine() (string, bool) {
	s, err := u.br.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// begin starts the next step.
func (u *initUI) begin(what string) {
	u.step++
	fmt.Fprintf(u.out, "== step %d/%d: %s\n", u.step, u.total, what)
}

// say prints a detail line inside a step; the step's one result line follows.
func (u *initUI) say(format string, args ...any) {
	fmt.Fprintf(u.out, "  "+format+"\n", args...)
}

func (u *initUI) done()              { fmt.Fprintln(u.out, "  done") }
func (u *initUI) already(why string) { fmt.Fprintf(u.out, "  already done: %s\n", why) }
func (u *initUI) skipped(why string) { fmt.Fprintf(u.out, "  skipped: %s\n", why) }

// stopped ends the run: next is what to do by hand. Later lines of a multi-line
// next are indented under the first.
func (u *initUI) stopped(next string) {
	lines := strings.Split(strings.TrimRight(next, "\n"), "\n")
	fmt.Fprintf(u.out, "  stopped: %s\n", lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(u.out, "           %s\n", l)
	}
}

// runInit serves `culibox init`: a guided first-time setup, one step at a time,
// each printing one result line. Re-running continues from where the machine
// is. It needs a terminal on stdin (it asks questions): without one it says
// what it would check and exits 2. A step that stops exits 1 with the command
// to finish by hand.
func runInit(a cliArgs, home string, in io.Reader, inTTY bool, out io.Writer, d initDeps) int {
	if !inTTY {
		fmt.Fprintln(out, "culibox init checks, in order: the programs it needs (abduco, docker, colima on macOS, claude),")
		fmt.Fprintln(out, "the data folder, your workspace folder, the box VM size (macOS), the Claude token, the box, and doctor.")
		fmt.Fprintln(out, "culibox init needs a terminal: it asks questions. Run it in an interactive shell.")
		return 2
	}
	envHome, notice, err := config.HomeEnv(d.getenv)
	if err != nil {
		fmt.Fprintf(out, "culibox init: %v\n", err)
		return 1
	}
	if notice != "" {
		fmt.Fprintf(out, "culibox: %s\n", notice)
	}
	cfg, src, err := config.Resolve(a.ConfigPath, a.ConfigSet, home, d.getenv("XDG_CONFIG_HOME"), envHome)
	if err != nil {
		fmt.Fprintf(out, "culibox init: config: %v\n", err)
		return 1
	}
	u := &initUI{br: bufio.NewReader(in), out: out, total: initSteps}

	if !initDepsStep(u, cfg, d) {
		return 1
	}
	if _, ok := initDataRootStep(u, cfg, src, home); !ok {
		return 1
	}
	return 0
}

// initDataRootStep is step 2: the data root, resolved exactly as the other
// commands do (config.Resolve picked it; cfg.DataDir is where it points),
// created 0700 when absent. It returns the absolute data dir.
func initDataRootStep(u *initUI, cfg config.Config, src config.Source, home string) (string, bool) {
	u.begin("data root")
	dataDir, err := filepath.Abs(cfg.ResolvedDataDir(home))
	if err != nil {
		u.stopped(fmt.Sprintf("the data root cannot be resolved (%v); set CULIBOX_HOME to an absolute path, then run culibox init again", err))
		return "", false
	}
	why := dataRootWhy(cfg, src, dataDir)
	fi, err := os.Stat(dataDir)
	switch {
	case err == nil && fi.IsDir():
		u.already(fmt.Sprintf("%s (%s)", dataDir, why))
		return dataDir, true
	case err == nil:
		u.stopped(fmt.Sprintf("%s is not a folder; move it aside or set CULIBOX_HOME elsewhere, then run culibox init again", dataDir))
		return "", false
	case !errors.Is(err, os.ErrNotExist):
		u.stopped(fmt.Sprintf("%s cannot be checked (%v); fix that, then run culibox init again", dataDir, err))
		return "", false
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		u.stopped(fmt.Sprintf("%s cannot be created (%v); create it with `mkdir -m 700 %s`, then run culibox init again", dataDir, err, dataDir))
		return "", false
	}
	u.say("created %s (%s)", dataDir, why)
	u.done()
	return dataDir, true
}

// dataRootWhy says why dataDir is the data dir: the config's data_dir, or how
// the root was picked.
func dataRootWhy(cfg config.Config, src config.Source, dataDir string) string {
	if !src.Default && cfg.DataDir != "" && filepath.Clean(cfg.DataDir) != filepath.Clean(src.Root.Path) {
		return "data_dir in " + src.Path
	}
	switch src.Root.Kind {
	case config.RootEnv:
		return "from $CULIBOX_HOME (or the older $AGENT_BOX_HOME)"
	case config.RootLegacy:
		return "the folder from before the rename to culibox; serve moves it later"
	case config.RootLinked:
		return "the default location; the old agent-box path links to it"
	}
	return "the default location"
}
