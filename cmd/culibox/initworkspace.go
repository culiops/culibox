package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// workspaceTries is how many answers step 3 takes before it stops.
const workspaceTries = 3

// normalizeWorkspace turns what the user typed into the absolute, cleaned path
// the rules are checked against: a leading "~" is home, a relative path is
// taken from the current directory, and a path that exists has its symlinks
// resolved (so a link to $HOME is seen as $HOME).
func normalizeWorkspace(typed, home string) (string, error) {
	p := strings.TrimSpace(typed)
	if p == "" {
		return "", errors.New("no folder given")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home == "" {
			return "", errors.New("your home folder is unknown, so ~ cannot be expanded")
		}
		p = filepath.Join(home, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, err := os.Lstat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return abs, nil
		}
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// initWorkspaceStep is step 3: the folder the agents work in, which the box
// mounts. A default_cwd already in the config is the default answer; there is
// no guessed one (which folders are shared is the user's risk call). A folder
// `box` would refuse, or one that is not a folder, is asked again, up to three
// answers. It returns the absolute, symlink-resolved path.
func initWorkspaceStep(u *initUI, cfg config.Config, dataDir, home string, cfgFiles []string, d initDeps) (string, bool) {
	u.begin("workspace folder")
	protected := protectedDirsOf(cfg, dataDir, cfgFiles)
	for try := 0; try < workspaceTries; try++ {
		typed, ok := u.line("Workspace folder (where your agents work; it is mounted into the box):", cfg.DefaultCwd)
		if !ok {
			u.stopped("no workspace folder was given; run culibox init again and type one")
			return "", false
		}
		p, err := normalizeWorkspace(typed, home)
		if err != nil {
			u.say("%q cannot be used: %v", typed, err)
			continue
		}
		fi, err := os.Stat(p)
		exists := err == nil
		switch {
		case exists && !fi.IsDir():
			u.say("%s is not a folder", p)
			continue
		case err != nil && !errors.Is(err, os.ErrNotExist):
			u.say("%s cannot be checked: %v", p, err)
			continue
		}
		reason, err := box.WorkspaceRefusal(p, protected)
		if err != nil {
			reason = err.Error()
		}
		if reason != "" {
			u.say("%s cannot be the workspace: %s. Pick a project folder.", p, reason)
			continue
		}
		if !exists {
			if !u.ask(fmt.Sprintf("%s does not exist. Create it?", p), true) {
				continue
			}
			if err := os.MkdirAll(p, 0o755); err != nil {
				u.say("%s cannot be created: %v", p, err)
				continue
			}
			u.say("created %s", p)
			u.done()
			return p, true
		}
		if cfg.DefaultCwd != "" && cfg.DefaultCwd == p {
			u.already("default_cwd is " + p)
			return p, true
		}
		u.say("workspace: %s", p)
		u.done()
		return p, true
	}
	u.stopped(fmt.Sprintf("no usable workspace folder after %d tries; run culibox init again and pick a project folder, not your home or CuliBox's own folders", workspaceTries))
	return "", false
}

// hostGiB is the host RAM in whole GiB, rounded to the nearest (8 GB Macs
// report exactly 8 GiB; a few report a little under).
func hostGiB(memBytes uint64) int { return int((memBytes + 1<<29) >> 30) }

// defaultVMSize is what init proposes for the box VM: half the host cores, at
// least 2, and a quarter of the host RAM in whole GiB, clamped to 4-8 GiB (4
// when the RAM is unknown). Inferred, not measured.
func defaultVMSize(cpus int, memBytes uint64) (vmCPUs, vmGiB int) {
	return max(2, cpus/2), min(8, max(4, hostGiB(memBytes)/4))
}

// parseVMSize reads "CPUs GiB" and checks it against the host: at least 1 of
// each and no more than the host has (a host value of 0 is unknown and sets no
// upper bound).
func parseVMSize(s string, hostCPUs int, hostMemBytes uint64) (cpus, gib int, err error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0, errors.New(`type two whole numbers, CPUs then GiB, e.g. "6 8"`)
	}
	cpus, err1 := strconv.Atoi(f[0])
	gib, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return 0, 0, errors.New(`type two whole numbers, CPUs then GiB, e.g. "6 8"`)
	}
	if cpus < 1 || (hostCPUs > 0 && cpus > hostCPUs) {
		return 0, 0, fmt.Errorf("CPUs must be between 1 and %d (this Mac has %d)", max(hostCPUs, 1), hostCPUs)
	}
	if gib < 1 || (hostMemBytes > 0 && gib > hostGiB(hostMemBytes)) {
		return 0, 0, fmt.Errorf("GiB must be between 1 and %d (this Mac has %d GiB)", max(hostGiB(hostMemBytes), 1), hostGiB(hostMemBytes))
	}
	return cpus, gib, nil
}

// initVMSizeStep is step 4: the box VM's size, macOS only and only before the
// VM exists. (0, 0, true) means leave the config's size as it is. An existing VM
// is never touched: init only says how to change it.
func initVMSizeStep(u *initUI, cfg config.Config, dataDir string, d initDeps) (cpus, gib int, ok bool) {
	u.begin("VM size")
	if d.goos != "darwin" {
		u.skipped("the box VM exists only on macOS")
		return 0, 0, true
	}
	if d.vmSetUp(cfg, dataDir) {
		u.already("the box VM is set up")
		if cfg.BoxVMCPUs > 0 && cfg.BoxVMMemoryGiB > 0 {
			u.say("the config asks for %d CPUs, %d GiB RAM", cfg.BoxVMCPUs, cfg.BoxVMMemoryGiB)
		} else {
			u.say("the config sets no size, so it is colima's current size")
		}
		u.say("changing the size of an existing VM needs `culibox box setup -recreate` (it loses images and cache); init never runs it")
		return 0, 0, true
	}
	hostCPUs, hostMem := d.hostSize()
	defCPUs, defGiB := defaultVMSize(hostCPUs, hostMem)
	if cfg.BoxVMCPUs > 0 && cfg.BoxVMMemoryGiB > 0 {
		defCPUs, defGiB = cfg.BoxVMCPUs, cfg.BoxVMMemoryGiB
		u.say("keeping the size from your config")
	} else {
		u.say("the proposal is inferred from this Mac's size, not measured")
	}
	if hostMem > 0 && hostGiB(hostMem) <= 8 {
		u.say(`8 GB Macs will feel a 4 GiB VM; close it with "culibox box stop" when idle.`)
	}
	q := fmt.Sprintf(`VM size: %d CPUs, %d GiB RAM. Enter to accept, or type e.g. "6 8" (CPUs GiB):`, defCPUs, defGiB)
	for {
		s, got := u.line(q, "")
		if !got {
			u.stopped("no VM size was given; run culibox init again and press Enter to accept the proposal")
			return 0, 0, false
		}
		if s == "" {
			u.done()
			return defCPUs, defGiB, true
		}
		c, g, err := parseVMSize(s, hostCPUs, hostMem)
		if err != nil {
			u.say("%v", err)
			continue
		}
		u.done()
		return c, g, true
	}
}
