package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/clone"
	"github.com/culiops/agent-box/internal/config"
)

// cloneDirsFile is the durable record of every directory agent-box has made (or
// tried to make) a clone in, under the data dir. Box-ownership must outlive the
// profile that created the clone: editing the profile (dropping clone_of,
// changing default_cwd) or deleting it must not hand a box-written tree to a
// host-tier session. One absolute clean path per line, mode 0600. There is no
// release command: only removing the line releases a dir. Deleting the
// directory does not, because the box-owned check also covers paths that do not
// exist yet.
const cloneDirsFile = "clone-dirs"

// cloneHooks is the daemon's session.CloneHooks: it hands the session manager
// the clone package's Prepare and BoxOwned over the daemon's own paths. clones
// is read on every call, so a profile saved after boot is covered.
type cloneHooks struct {
	env    clone.Env
	clones func() []config.Clone

	// unavailable is why Prepare cannot run (no containment layer, no host git);
	// "" when it can. BoxOwned needs neither, so it always works.
	unavailable string

	file    string // <dataDir>/clone-dirs
	mu      sync.Mutex
	prepare func(context.Context, clone.Env, config.Clone, bool) (box.CloneMounts, error) // swappable for tests
}

func (h *cloneHooks) Prepare(ctx context.Context, c config.Clone, withMemory bool) (box.CloneMounts, error) {
	if h.unavailable != "" {
		return box.CloneMounts{}, fmt.Errorf("%w: clone mode is unavailable (%s)", box.ErrBoxUnavailable, h.unavailable)
	}
	if !usableDir(c.Dir) {
		return box.CloneMounts{}, fmt.Errorf("%w: clone dir %q is not an absolute clean path", box.ErrUnboxableWorkspace, c.Dir)
	}
	// Record BEFORE creating anything, so a crash between creation and record
	// cannot leave a box-written clone that nothing remembers. A record that
	// cannot be written fails the spawn.
	if err := h.record(c.Dir); err != nil {
		return box.CloneMounts{}, fmt.Errorf("%w: cannot record the clone dir in %s: %v", box.ErrBoxUnavailable, h.file, err)
	}
	return h.prepare(ctx, h.env, c, withMemory)
}

// BoxOwned checks the live profiles' clones plus every recorded dir. An
// unreadable or corrupt record fails closed: everything is reported owned.
func (h *cloneHooks) BoxOwned(cwd string) (string, bool) {
	dirs, err := h.recorded()
	if err != nil {
		return fmt.Sprintf("the clone-dir record %s cannot be read (%v); refusing a host session at %s in case it is inside a clone (fix or delete the file)",
			h.file, err, cwd), true
	}
	all := h.clones()
	for _, d := range dirs {
		all = append(all, config.Clone{Dir: d})
	}
	return clone.BoxOwned(cwd, all)
}

func usableDir(d string) bool { return filepath.IsAbs(d) && filepath.Clean(d) == d }

// recorded reads the record. A missing file is an empty record; a symlink at
// the path, a read error or a malformed line is an error.
func (h *cloneHooks) recorded() ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readLocked()
}

func (h *cloneHooks) readLocked() ([]string, error) {
	f, err := os.OpenFile(h.file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !usableDir(line) {
			return nil, fmt.Errorf("malformed line %q", line)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// record adds dir to the record if absent, by writing a fresh 0600 file beside
// it and renaming over it (the rename replaces, never follows, a symlink at the
// final path).
func (h *cloneHooks) record(dir string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	dirs, err := h.readLocked()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if d == dir {
			return nil
		}
	}
	dirs = append(dirs, dir)
	tmp, err := os.CreateTemp(filepath.Dir(h.file), cloneDirsFile+".tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(dirs, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), h.file); err != nil {
		return err
	}
	ok = true
	return nil
}

// newCloneHooks builds the clone-mode hooks. Clone mode is Linux-only in
// Phase A, so it returns nil, nil elsewhere. On Linux it ALWAYS returns hooks:
// the host-tier box-owned guard (BoxOwned) needs neither the box layer nor git,
// and must stay on when docker is down or git is missing. When Prepare cannot
// run (no layer, no git) the hooks are still returned, with a non-nil error
// saying why, and Prepare refuses with box.ErrBoxUnavailable. memRoot is the
// config bundle source ("" with the bundle off), absolute when set.
func newCloneHooks(goos string, layer *boxLayer, home, memRoot, dataDir string, protected []box.ProtectedDir, clones func() []config.Clone) (*cloneHooks, error) {
	if goos != "linux" {
		return nil, nil
	}
	h := &cloneHooks{
		env:     clone.Env{Home: home, Protected: protected, MemoryRoot: memRoot},
		clones:  clones,
		file:    filepath.Join(dataDir, cloneDirsFile),
		prepare: clone.Prepare,
	}
	if layer == nil {
		h.unavailable = "no containment layer"
		return h, errors.New(h.unavailable)
	}
	g, err := clone.NewExecGit()
	if err != nil {
		h.unavailable = err.Error()
		return h, err
	}
	h.env.Git = g
	return h, nil
}
