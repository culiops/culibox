// Package gitview builds the docker argv that runs git for the file tree's
// status badges and diff view, and parses what comes back. Git never runs on
// the host: the repository is agent-shaped, so its config and attributes are
// untrusted, and git only ever runs inside a throwaway container with no
// network, no capabilities and read-only mounts. The output is untrusted too.
package gitview

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Label marks the throwaway containers so they can be found and reaped.
const Label = "io.culiops.agent-box.gitstatus"

const tmpDir = "/tmp"

// Target is one repository: Top is the checkout top-level, Common the common
// git dir (the same as Top/.git unless Top is a linked worktree).
type Target struct{ Top, Common string }

// mounts are the read-only bind mounts: Top, plus Common when it lies outside Top.
func (t Target) mounts() []string {
	m := []string{t.Top}
	if t.Common != "" && !within(t.Top, t.Common) {
		m = append(m, t.Common)
	}
	return m
}

// within reports whether p is dir or lies below it, by path, not by string prefix.
func within(dir, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// argv is the docker argv (after "docker") that runs git with the given
// subcommand in t. It has the isolation of clone.ListArgs.
func argv(t Target, image, name string, uid, gid int, sub ...string) []string {
	a := []string{
		"run", "--rm", "--init", "--name", name, "--pull=never", "--network", "none", "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--tmpfs", tmpDir + ":rw,nosuid,nodev,noexec,size=64m", "--pids-limit", "128", "--memory", "512m",
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--label", Label + "=1",
		"-e", "HOME=" + tmpDir, "-e", "GIT_CONFIG_NOSYSTEM=1", "-e", "GIT_OPTIONAL_LOCKS=0",
	}
	for _, m := range t.mounts() {
		a = append(a, "--mount", "type=bind,src="+m+",dst="+m+",readonly")
	}
	a = append(a, "--entrypoint", "git", image,
		"-C", t.Top, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "safe.directory=*")
	return append(a, sub...)
}

// StatusArgs is the argv of the status container.
func StatusArgs(t Target, image, name string, uid, gid int) []string {
	return argv(t, image, name, uid, gid,
		"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal", "--no-renames", "--ignore-submodules=all")
}

// DiffArgs is the argv of the diff container for one file, rel being its path
// relative to t.Top. Tracked files diff against HEAD (staged and unstaged);
// untracked ones diff against /dev/null so the whole file reads as added.
// rel follows "--" so it can never be taken for an option.
func DiffArgs(t Target, image, name string, uid, gid int, rel string, untracked bool) []string {
	if untracked {
		return argv(t, image, name, uid, gid,
			"diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", rel)
	}
	return argv(t, image, name, uid, gid,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", rel)
}
