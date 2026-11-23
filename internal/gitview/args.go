// Package gitview builds the docker argv that runs git for the file tree's
// status badges and diff view, and parses what comes back. Git never runs on
// the host: the repository is agent-shaped, so its config and attributes are
// untrusted, and git only ever runs inside a throwaway container with no
// network, no capabilities and read-only mounts. The output is untrusted too.
package gitview

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/box"
)

// Label marks the throwaway containers so they can be found and reaped.
const Label = "io.culiops.agent-box.gitstatus"

const tmpDir = "/tmp"

// Target is one repository: Top is the checkout top-level, Common the common
// git dir (the same as Top/.git unless Top is a linked worktree). Root, when
// set, is a boxed workspace root holding both: it is the only mount, so a link
// the box plants inside (even one swapped in after the caller's checks)
// resolves in the container's namespace, never on the host. Empty Root (a host
// checkout) mounts Top, plus Common when it lies outside Top.
type Target struct{ Root, Top, Common string }

// Unsafe names why t cannot be mounted, or "" when it can. Top and Common come
// from an agent-shaped checkout, and docker parses --mount as CSV, so a path
// holding a comma or quote could add mount options of its own. Both must be
// absolute and clean, and pass box.MountUnsafe. Callers MUST check it before
// StatusArgs, DiffArgs, BaseArgs, MergeBaseArgs, NumstatArgs or ShowArgs and treat a
// refusal as state "unavailable"; the builders do not check, they only build.
func (t Target) Unsafe() string {
	for _, p := range []struct{ name, v string }{{"workspace", t.Root}, {"checkout", t.Top}, {"git dir", t.Common}} {
		if p.v == "" && p.name != "checkout" {
			continue // no workspace root / no separate common dir
		}
		switch {
		case !filepath.IsAbs(p.v):
			return p.name + " path is not absolute"
		case filepath.Clean(p.v) != p.v:
			return p.name + " path is not clean"
		}
		if why := box.MountUnsafe(p.v); why != "" {
			return p.name + ": " + why
		}
	}
	if t.Root != "" {
		if !within(t.Root, t.Top) {
			return "checkout is outside the workspace"
		}
		if t.Common != "" && !within(t.Root, t.Common) {
			return "git dir is outside the workspace"
		}
	}
	return ""
}

// mounts are the read-only bind mounts: Root alone when set; else Top, plus
// Common when it lies outside Top.
func (t Target) mounts() []string {
	if t.Root != "" {
		return []string{t.Root}
	}
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
		"-e", "GIT_CONFIG_GLOBAL=/dev/null", "-e", "GIT_ATTR_NOSYSTEM=1",
		// A file named "*.go" or ":(top)*" names itself, never a pattern.
		"-e", "GIT_LITERAL_PATHSPECS=1",
	}
	for _, m := range t.mounts() {
		a = append(a, "--mount", "type=bind,src="+m+",dst="+m+",readonly")
	}
	a = append(a, "--entrypoint", "git", image,
		"--no-pager", "-C", t.Top, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "core.attributesFile=/dev/null", "-c", "safe.directory=*")
	return append(a, sub...)
}

// StatusArgs is the argv of the status container. The caller MUST have checked
// t.Unsafe() == "" first.
func StatusArgs(t Target, image, name string, uid, gid int) []string {
	return argv(t, image, name, uid, gid,
		"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal", "--no-renames", "--ignore-submodules=all",
		// The tree dims gitignored rows from the same run. "matching" names an ignored folder once and does not walk into it
		// (measured: 50,000 ignored files cost nothing, where the default mode walked them), and git prints these records
		// after every other one (ParseIgnored).
		"--ignored=matching")
}

// DiffArgs is the argv of the diff container for one file, rel being its path
// relative to t.Top. Tracked files diff against HEAD (staged and unstaged);
// untracked ones diff against /dev/null so the whole file reads as added.
// The caller MUST have checked t.Unsafe() == "" first. rel follows "--" so it can never be taken for an option.
func DiffArgs(t Target, image, name string, uid, gid int, rel string, untracked bool) []string {
	if untracked {
		return argv(t, image, name, uid, gid,
			"diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", rel)
	}
	return argv(t, image, name, uid, gid,
		"diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", rel)
}

// originHead is the ref a clone points at the remote's default branch.
const originHead = "refs/remotes/origin/HEAD"

// baseCandidates are the refs a base branch is looked for in when origin/HEAD
// names none, best first: the remote's main and master, then local ones.
var baseCandidates = []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}

// maxRefLen is the longest ref ValidBaseRef accepts, in bytes.
const maxRefLen = 255

var baseRefRE = regexp.MustCompile(`^refs/(heads|remotes)/[A-Za-z0-9._/-]+$`)

// ValidBaseRef reports whether ref may name a base branch in a git argv: a
// full branch ref (local or remote-tracking) of at most 255 bytes, of plain
// characters and with no "..". The "refs/" it must start with means it can
// never be read as an option, whatever follows. Every ref that reaches an argv
// has passed it; the one a client could send never does, since the server
// derives the base itself.
func ValidBaseRef(ref string) bool {
	return len(ref) <= maxRefLen && baseRefRE.MatchString(ref) && !strings.Contains(ref, "..")
}

// BaseArgs is the argv of the container that lists the candidate base
// branches in one run: origin/HEAD with the ref it points at, and each of
// baseCandidates that exists, each with the commit it points at. The names are
// fixed, nothing comes from a caller. The caller MUST have checked
// t.Unsafe() == "" first.
func BaseArgs(t Target, image, name string, uid, gid int) []string {
	sub := []string{"for-each-ref", "--format=%(refname)%00%(symref)%00%(objectname)%00", originHead}
	return argv(t, image, name, uid, gid, append(sub, baseCandidates...)...)
}

// MergeBaseArgs is the argv of the container that prints the merge base of
// the commit head (a full object id: the HEAD a status printed, so the answer
// is for exactly the HEAD it is kept under) and baseRef. It is nil for a head
// that is not a full object id or a baseRef that is not a ValidBaseRef, so a
// caller that skips its own check runs nothing rather than a hostile argument.
// The caller MUST have checked t.Unsafe() == "" first.
func MergeBaseArgs(t Target, image, name string, uid, gid int, head, baseRef string) []string {
	if !oidRE.MatchString(head) || !ValidBaseRef(baseRef) {
		return nil
	}
	return argv(t, image, name, uid, gid, "merge-base", head, baseRef)
}

// Scope is what a numstat compares the working tree with: HEAD, or for Branch
// the merge base of HEAD and BaseRef. BaseRef is read only for Branch, and it
// is always one the server derived (Runner.Base), never a client's.
type Scope struct {
	Branch  bool
	BaseRef string
}

// NumstatArgs is the argv of the container that prints, for every tracked file
// that differs from the scope's side, its status letter (--raw) and its line
// counts (--numstat). Untracked files are not in it. A BaseRef that is not a
// ValidBaseRef builds no argv (nil) for Branch, so a caller that skips its own
// check runs nothing rather than a hostile ref. The trailing "--" ends the
// revisions: without it git refuses ("ambiguous argument") a repository that
// holds a file named like the revision, which an agent can create. The caller
// MUST have checked t.Unsafe() == "" first.
func NumstatArgs(t Target, image, name string, uid, gid int, sc Scope) []string {
	rev := []string{"HEAD"}
	if sc.Branch {
		if !ValidBaseRef(sc.BaseRef) {
			return nil
		}
		rev = []string{"--merge-base", sc.BaseRef}
	}
	sub := append([]string{"diff"}, rev...)
	sub = append(sub, "--raw", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--")
	return argv(t, image, name, uid, gid, sub...)
}

// validRev reports whether rev may name a commit in a git argv: HEAD, or a
// full object id (SHA-1 or SHA-256, lowercase hex) such as the merge base
// Runner.Base prints. Nothing else is a revision here (no "main", no "HEAD~1",
// no "-x"), so a rev can never be read as an option or as a lookup the
// repository could answer differently.
func validRev(rev string) bool {
	return rev == "HEAD" || oidRE.MatchString(rev)
}

// ShowArgs is the argv of the container that prints the content of the file rel
// (relative to t.Top) as it is in rev, which must be "HEAD" or a full object id
// (see validRev): anything else builds no argv (nil), so a caller that skips
// its own check runs nothing rather than a hostile rev. cat-file blob prints the
// stored bytes: it runs no clean or smudge filter and no textconv, which a
// repository's own attributes could otherwise point at a command. The path is
// everything after the first colon of one argument, so it can never be read as
// an option or a pathspec. The caller MUST have checked t.Unsafe() == "" first.
func ShowArgs(t Target, image, name string, uid, gid int, rev, rel string) []string {
	if !validRev(rev) {
		return nil
	}
	return argv(t, image, name, uid, gid, "cat-file", "blob", rev+":"+rel)
}
