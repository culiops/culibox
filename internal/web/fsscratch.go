package web

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// scratchUID is the uid a scratch root must belong to. A variable so a test can
// stand in for "owned by someone else" without running as root.
var scratchUID = os.Getuid

// ScratchCandidates lists where Claude Code may keep its per-user temp folder,
// claude-<uid>: under $CLAUDE_CODE_TMPDIR when that is set, else the OS temp
// dir. Agents spawned with the clean env policy get no TMPDIR, so /tmp is
// always a candidate, and the server's own temp dir (macOS: $TMPDIR under
// /var/folders) covers agents that inherited it. These are only candidates:
// newFsPolicy accepts one per request only after checking what is there.
func ScratchCandidates(getenv func(string) string, uid int, tempDir string) []string {
	name := fmt.Sprintf("claude-%d", uid)
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		c := filepath.Clean(filepath.Join(dir, name))
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	add(getenv("CLAUDE_CODE_TMPDIR"))
	add("/tmp")
	add(tempDir)
	return out
}

// scratchRoot checks one candidate and returns its resolved path and identity.
// The PARENT may be a link (macOS /tmp is /private/tmp); the folder itself must
// be a real directory, ours, and writable by nobody else — the same bar Claude
// Code sets before it uses the folder. In a shared /tmp anyone can create
// claude-<our uid> first; such a folder is never ours and never served.
func scratchRoot(cand string) (string, fileKey, bool) {
	if cand == "" || !filepath.IsAbs(cand) {
		return "", fileKey{}, false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(cand)))
	if err != nil {
		return "", fileKey{}, false
	}
	real := filepath.Join(parent, filepath.Base(cand))
	fi, err := os.Lstat(real)
	if err != nil || !fi.IsDir() {
		return "", fileKey{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int64(st.Uid) != int64(scratchUID()) || fi.Mode().Perm()&0o022 != 0 {
		return "", fileKey{}, false
	}
	k, err := keyOf(fi)
	if err != nil {
		return "", fileKey{}, false
	}
	return real, k, true
}

// underScratch reports the scratch root path is under (or is), if any. path is
// already resolved by the walk, so a string match against the resolved root is
// exact here.
func (p fsPolicy) underScratch(path string) (string, bool) {
	for _, root := range p.scratch {
		if path == root || len(path) > len(root) && path[:len(root)] == root && path[len(root)] == os.PathSeparator {
			return root, true
		}
	}
	return "", false
}
