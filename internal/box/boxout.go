package box

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// PlaywrightOutBox is where the boxed Playwright MCP writes its screenshots
	// and snapshots. It must equal the --output-dir in the image's mcp.json
	// (docker/agent-box/Dockerfile); a test pins the two together. It sits under
	// the box's /tmp tmpfs, and the host out dir is bind-mounted over it, so what
	// the browser saves lands where the host can serve it.
	PlaywrightOutBox = "/tmp/playwright-mcp"

	// outDirName holds one directory per box, beside the homes in boxstate/. It
	// is not a box name, so the reclaim sweep's listing never mistakes it for a
	// home.
	outDirName = ".boxout"

	// outPruneAge is how long a file the browser saved is kept. A screenshot
	// older than this has no reader: the session that took it is long over.
	outPruneAge = 7 * 24 * time.Hour
)

// OutputDir is the HOST directory bound over PlaywrightOutBox for a box.
//
// It is deliberately NOT inside the box home (boxstate/<box>): the home is the
// agent's whole $HOME and a reader of it would be a general box-home reader.
// This directory is the one narrow place a box is meant to hand files to the
// host, so it is the only box-written path the file endpoint serves.
func OutputDir(stateRoot, boxName string) string {
	return filepath.Join(stateRoot, stateDirName, outDirName, boxName, "playwright-mcp")
}

// prepareOutputDir creates the box's out dir and drops what is older than
// outPruneAge, returning the path to mount. It runs only when a container is
// about to be created, while none exists for this box, so nothing is writing
// into the directory.
//
// The parents are ours alone — the box is only ever given the leaf — so a plain
// MkdirAll is fine for them. The leaf is the one path the box has been able to
// write under, so it is never trusted: a symlink or a file where the directory
// belongs is refused outright (Lstat does not follow), rather than followed by
// the mount or pruned through. Pruning goes through an os.Root anchored at the
// leaf for the same reason ensureStateDir does: the box writes there as the
// daemon user and can plant a symlink to a host path, which an os.Root removes
// as a link instead of following.
//
// Creating the directory is an error; pruning it is best effort, because a
// stale screenshot is no reason to fail a spawn.
func (m *Manager) prepareOutputDir(boxName string, now time.Time) (string, error) {
	leaf := OutputDir(m.opts.StateRoot, boxName)
	if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
		return "", fmt.Errorf("box: creating output dir parent %q: %w", filepath.Dir(leaf), err)
	}
	switch fi, err := os.Lstat(leaf); {
	case err == nil:
		if !fi.IsDir() {
			return "", fmt.Errorf("box: output dir %q is not a plain directory (%s)", leaf, fi.Mode().Type())
		}
	case os.IsNotExist(err):
		if err := os.Mkdir(leaf, 0o700); err != nil {
			return "", fmt.Errorf("box: creating output dir %q: %w", leaf, err)
		}
	default:
		return "", fmt.Errorf("box: checking output dir %q: %w", leaf, err)
	}
	pruneOutputDir(leaf, now)
	return leaf, nil
}

// pruneOutputDir removes the top-level entries of dir last modified more than
// outPruneAge before now. Best effort, every failure ignored. The age is the
// entry's own (Lstat), so a symlink is judged by the link, and RemoveAll on a
// root removes a link without following it.
func pruneOutputDir(dir string, now time.Time) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return
	}
	cutoff := now.Add(-outPruneAge)
	for _, e := range entries {
		fi, err := root.Lstat(e.Name())
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		_ = root.RemoveAll(e.Name())
	}
}
