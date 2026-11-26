package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/handback"
)

// The opt-in merge of a returned box/X into X (#370). Box-written commits are
// untrusted and this step puts them in the host's work tree, where a human or a
// host tool may run them, so it is a fast-forward only, of a clean tree, with
// no hooks, after a guard that refuses commits touching files host tools load
// by themselves, commits carrying any handback flag, and paths a git filter
// would process. When it does not merge, box/X stays for the user to review.

// MergeResult reasons, mirrored verbatim by the web UI.
const (
	MergeDetached = "detached" // HEAD is not on a branch
	MergeDiverged = "diverged" // X and box/X both have commits the other lacks
	MergeDirty    = "dirty"    // tracked files have uncommitted changes
	MergeGuarded  = "guarded"  // the commits touch a file host tools trust, or a submodule
	MergeFlagged  = "flagged"  // the commits carry a handback flag, or could not be inspected
	MergeFilter   = "filter"   // a path the commits change has a git filter attribute
	MergeMoved    = "moved"    // X changed while it was being checked
	MergeFailed   = "failed"   // a git call failed, the audit failed, or git refused the merge
)

// MergeResult is what one MergeHome call did or why it did not.
type MergeResult struct {
	At      time.Time `json:"at"`
	Branch  string    `json:"branch"`            // X
	From    string    `json:"from,omitempty"`    // full SHA of X before
	To      string    `json:"to,omitempty"`      // full SHA of box/X checked
	Merged  bool      `json:"merged,omitempty"`  // X is now To
	Commits int       `json:"commits,omitempty"` // in From..To
	Files   int       `json:"files,omitempty"`   // distinct paths in From..To
	Reason  string    `json:"reason,omitempty"`  // "" when Merged
	// Attempted is true only when `git merge` ran and git refused it: the one
	// failure the retry window holds. A failed pre-check or audit is not.
	Attempted bool     `json:"attempted,omitempty"`
	Detail    string   `json:"detail,omitempty"` // sanitized, <= maxErrRunes
	Paths     []string `json:"paths,omitempty"`  // <= 3 examples for guarded/flagged/filter, sanitized
}

// MergeRequest is one MergeHome call.
type MergeRequest struct {
	Of    string          // the host repo (clone_of)
	Home  string          // the user's home, for their global git config; "" skips it
	Lim   handback.Limits // for the handback inspection of the range
	Prev  *MergeResult    // the last persisted result, for the retry rule
	Audit func(action, detail string) error
	Now   func() time.Time // nil: time.Now
	// CloneTips is this clone's own listing, branch name -> commit. box/X in
	// r.Of is shared by every clone of the repo and written by `culibox clone
	// fetch` too, so only a box/X at this clone's tip for X is merged: another
	// profile's work (or a manual fetch) never is. A missing entry merges nothing.
	CloneTips map[string]string
}

// MergeDenyNames are file names, matched case-insensitively against the
// basename at any depth, that a box's commits must not touch for the merge to
// happen: files host tools (an agent, direnv, mise, git) load and act on by
// themselves. Entries may be added, never dropped.
var MergeDenyNames = append(box.InPlaceGuardFiles(),
	".envrc", ".gitattributes", ".gitmodules", "mise.toml", ".mise.toml",
	"mise.local.toml", ".mise.local.toml",
	".pre-commit-config.yaml", ".pre-commit-hooks.yaml", ".overcommit.yml",
	"lefthook.yml", "lefthook.yaml", ".lefthook.yml", ".lefthook.yaml",
	"lefthook-local.yml", "lefthook-local.yaml", ".lefthook-local.yml", ".lefthook-local.yaml",
	// direnv's dotenv, nix-direnv and devenv load these on the next prompt in the
	// folder, node and yarn run what these name on every call.
	".env", "flake.nix", "flake.lock", "shell.nix", "default.nix", "devenv.nix", "devenv.yaml",
	"devenv.lock", "devbox.json", ".npmrc", ".yarnrc.yml", ".yarnrc")

// MergeDenyPatterns are path.Match patterns for more file names, matched like
// MergeDenyNames (any depth, case-insensitive, by the basename).
var MergeDenyPatterns = []string{".env.*", ".envrc*", "mise.*.toml", ".mise.*.toml", "*.code-workspace", ".aider*"}

// MergeDenyDirs are directory names, matched case-insensitively against every
// path component, under which a box's commits block the merge: an agent's
// project settings and hooks live there.
var MergeDenyDirs = []string{".claude"}

// MergeDenyTop are top-level directories (case-insensitive) under which a box's
// commits block the merge: git hook managers, editor tasks, dev containers and
// tool configs (mise reads mise/ and .config/mise/), which run commands when the
// host opens or commits in the repo. clone_of's own core.hooksPath is added at
// run time when it is inside the repo.
var MergeDenyTop = []string{".husky", ".githooks", ".vscode", ".devcontainer", ".mise", "mise", ".config",
	".idea", ".zed", ".cursor", ".gemini", ".codex", ".windsurf", ".continue"}

// MergeDenyTopPaths are repo-relative paths (a file or a folder, matched by
// whole components, case-insensitive) a box's commits must not touch: other
// agents' instructions that load by themselves. CI definitions are not here:
// they run on the user's next push.
var MergeDenyTopPaths = []string{".github/copilot-instructions.md", ".github/instructions", ".github/prompts"}

const (
	mergeRetryAfter = 30 * time.Minute
	mergeTimeout    = 60 * time.Second
	maxMergePaths   = 3
	mergeReflog     = "GIT_REFLOG_ACTION=culibox auto-merge"
	// mergeFlaggedDetail starts the Detail of a flagged result that found flags.
	mergeFlaggedDetail = "the commits carry flagged files"
)

// MergeFlaggedFound reports whether the Detail of a flagged result says the
// inspection found flagged files, a settled answer for its range. A flagged
// result without it is a scan that could not finish (a deadline, a git failure),
// which is run again.
func MergeFlaggedFound(detail string) bool { return strings.HasPrefix(detail, mergeFlaggedDetail) }

// MergeHome fast-forwards X, the branch checked out in r.Of, to box/X when box/X
// is this clone's own tip for X (r.CloneTips) and strictly ahead, the tree is
// clean and the guard passes. It returns nil when there is nothing to do or
// report: no box/X, a box/X this clone did not write, or box/X not ahead of X.
//
// Every git call is the hardened one in r.Of with CleanEnv plus
// GIT_NO_REPLACE_OBJECTS, so the checks and the merge read the same objects the
// handback inspection reads (a refs/replace/ entry cannot make the merge check
// out a commit other than the one checked). The merge is of the To SHA, not the
// ref, so what lands is exactly what was checked.
func MergeHome(ctx context.Context, g Git, r MergeRequest) (res *MergeResult) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	m := &merger{ctx: ctx, g: g, of: r.Of}
	defer func() {
		// Parsing what a box wrote must never take the daemon down; a panic
		// before the merge call means nothing was merged.
		if p := recover(); p != nil {
			log.Printf("clone: merging into %s stopped with an internal error: %v", r.Of, p)
			res = m.result(MergeFailed, "internal error", nil)
			res.At = now()
		}
	}()
	res = m.check(r, now)
	if res != nil && res.At.IsZero() {
		res.At = now()
	}
	return res
}

type merger struct {
	ctx            context.Context
	g              Git
	of             string
	branch         string // X, as git printed it
	from, to       string
	commits, files int
}

func (m *merger) env() []string { return noReplaceEnv() }

func (m *merger) run(env []string, args ...string) ([]byte, error) {
	return m.g.Run(m.ctx, m.of, env, hardened(args...)...)
}

func (m *merger) result(reason, detail string, paths []string) *MergeResult {
	res := &MergeResult{Branch: sanitizeN(m.branch, maxErrRunes), From: m.from, To: m.to,
		Commits: m.commits, Files: m.files, Reason: reason, Detail: sanitizeN(detail, maxErrRunes)}
	for _, p := range paths {
		if len(res.Paths) == maxMergePaths {
			break
		}
		res.Paths = append(res.Paths, sanitizeN(p, maxErrRunes))
	}
	return res
}

func (m *merger) failed(what string, err error) *MergeResult {
	return m.result(MergeFailed, what+": "+err.Error(), nil)
}

// headBranch returns X when HEAD is a branch ("" when detached).
func (m *merger) headBranch() (string, error) {
	out, err := m.run(m.env(), "symbolic-ref", "-q", "HEAD")
	if err != nil {
		if code, ok := exitCode(err); ok && code == 1 {
			return "", nil
		}
		return "", err
	}
	ref := strings.TrimSpace(string(out))
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) == len("refs/heads/") {
		return "", nil
	}
	return strings.TrimPrefix(ref, "refs/heads/"), nil
}

// boxWorkWaiting reports whether some branch x of this clone's own listing has
// refs/heads/box/x at the clone's tip for x and different from refs/heads/x: box
// work is waiting to be merged, so a detached HEAD is worth saying.
//
// One for-each-ref lists every local branch and the answer is compared here, so
// the cost does not grow with the number of branches. The branch names in tips
// and in the listing are box-controlled text: they are only compared as
// strings, never passed back to git, and a line that is not exactly
// "refs/heads/<name> <type> <id>" is an error (fail closed).
func (m *merger) boxWorkWaiting(tips map[string]string) (bool, error) {
	want := false
	for x, tip := range tips {
		if x != "" && tip != "" {
			want = true
			break
		}
	}
	if !want {
		return false, nil
	}
	out, err := m.run(m.env(), "for-each-ref", "--format=%(refname) %(objecttype) %(objectname)", "refs/heads/")
	if err != nil {
		return false, err
	}
	if len(out) > 0 && out[len(out)-1] != '\n' {
		return false, errors.New("the branch list is cut short")
	}
	heads := map[string]string{} // branch name -> commit id
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" && len(out) == 0 {
			break
		}
		f := strings.Split(line, " ")
		name, ok := strings.CutPrefix(f[0], "refs/heads/")
		if len(f) != 3 || !ok || name == "" || !blobIDRE.MatchString(f[2]) {
			return false, errors.New("unexpected line in the branch list")
		}
		if f[1] == "commit" {
			heads[name] = f[2]
		}
	}
	for x, tip := range tips {
		if x == "" || tip == "" {
			continue
		}
		if box := heads["box/"+x]; box != "" && box == tip && heads[x] != box {
			return true, nil
		}
	}
	return false, nil
}

// commitOf returns the full id of the commit ref names, "" when there is none.
func (m *merger) commitOf(ref string) (string, error) {
	out, err := m.run(m.env(), "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		if code, ok := exitCode(err); ok && code == 1 {
			return "", nil
		}
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if !blobIDRE.MatchString(id) {
		return "", fmt.Errorf("rev-parse printed no object id for %s", ref)
	}
	return id, nil
}

// isAncestor reports whether commit a is an ancestor of (or equal to) b.
func (m *merger) isAncestor(a, b string) (bool, error) {
	_, err := m.run(m.env(), "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, err
}

func (m *merger) check(r MergeRequest, now func() time.Time) *MergeResult {
	// 1. The checked-out branch.
	x, err := m.headBranch()
	if err != nil {
		return m.failed("cannot read HEAD", err)
	}
	if x == "" {
		waiting, err := m.boxWorkWaiting(r.CloneTips)
		if err != nil {
			return m.failed("cannot read box branches", err)
		}
		if !waiting {
			return nil // nothing of this profile's to merge, so nothing to report
		}
		return m.result(MergeDetached, "HEAD is not on a branch", nil)
	}
	m.branch = x

	// 2. Tips: only box/X, and only when it is strictly ahead of X.
	if m.to, err = m.commitOf("refs/heads/box/" + x); err != nil {
		return m.failed("cannot read box/"+x, err)
	}
	if m.to == "" || r.CloneTips[x] != m.to {
		return nil // no box/X, or not this clone's: nothing of this profile's to merge
	}
	if m.from, err = m.commitOf("refs/heads/" + x); err != nil {
		return m.failed("cannot read "+x, err)
	}
	if m.from == "" { // an unborn branch: nothing to fast-forward from
		return nil
	}
	if m.from == m.to {
		return nil
	}
	if behind, err := m.isAncestor(m.to, m.from); err != nil {
		return m.failed("cannot compare "+x+" and box/"+x, err)
	} else if behind {
		return nil // the host is ahead: nothing to bring
	}
	if ahead, err := m.isAncestor(m.from, m.to); err != nil {
		return m.failed("cannot compare "+x+" and box/"+x, err)
	} else if !ahead {
		return m.result(MergeDiverged, x+" and box/"+x+" both have commits the other lacks", nil)
	}

	// A merge git refused for this same range is retried at most every 30 min,
	// so a persistent refusal does not write audit rows on every pass. Only that
	// failure holds the window (a pre-check or audit failure may have cleared),
	// and it is checked before the scan: nothing merges inside the window, so
	// the dirty check and the scan would only repeat.
	if p := r.Prev; p != nil && p.Reason == MergeFailed && p.Attempted && p.From == m.from && p.To == m.to &&
		now().Sub(p.At) < mergeRetryAfter {
		return p
	}

	// Flags found in this same range are not looked for again, and nothing else
	// is either: they come from its blobs, which the range fixes, and a flagged
	// range does not merge whatever the tree, the config or the guard now say, so
	// every scan below would only repeat a refusal (the same reasoning as the
	// retry window above). A scan that found no flag but could not finish (a
	// deadline, a git failure) is not held: it runs again, in full.
	if p := r.Prev; p != nil && p.Reason == MergeFlagged && p.From == m.from && p.To == m.to &&
		MergeFlaggedFound(p.Detail) {
		return p
	}

	// 3. A clean tree (tracked files; git itself refuses to overwrite an untracked one).
	if res := m.clean(); res != nil {
		return res
	}

	// 4a. Every path and mode of every commit in the range.
	out, err := m.run(m.env(), "log", "--root", "--no-show-signature", "--no-ext-diff", "--no-textconv",
		"--format=%H", "--raw", "-r", "-z", "--no-renames", "--no-abbrev", "--diff-merges=first-parent",
		m.from+".."+m.to, "--")
	if err != nil {
		return m.failed("cannot list the commits", err)
	}
	n, entries, err := parseMergeLog(out)
	if err != nil {
		return m.failed("cannot read the commit list", err)
	}
	m.commits = n
	// The first-parent log misses what a box merge commit takes from a parent
	// other than its first: with an older host commit as first parent and From
	// as second, the merge restores that commit's version of a file and the log
	// lists none of it. The From..To tree diff is what the checkout writes, so
	// its entries join the log's (which still show an add-then-delete).
	out, err = m.run(m.env(), "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev",
		"--no-ext-diff", "--no-textconv", m.from, m.to, "--")
	if err != nil {
		return m.failed("cannot diff the range", err)
	}
	tree, err := parseDiffTree(out)
	if err != nil {
		return m.failed("cannot read the range's diff", err)
	}
	entries = append(entries, tree...)
	var paths []string // distinct, first seen first
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.path] {
			seen[e.path] = true
			paths = append(paths, e.path)
		}
	}
	m.files = len(paths)
	gc, err := m.gitConfig(r.Home)
	if err != nil {
		return m.failed("cannot read git config", err)
	}
	agentDenied, importPaths, err := m.agentImportDenies(r.Home)
	if err != nil {
		return m.failed("cannot list the CLAUDE.md files", err)
	}
	refDenied, refPaths, err := m.configRefDenies(r.Home, gc.hooks)
	if err != nil {
		return m.failed("cannot read the Claude Code config", err)
	}
	var denied []string
	dseen := map[string]bool{}
	for _, e := range entries {
		if (e.gitlink || mergeDenied(e.path, gc.hooks) || importDenied(e.path, gc.denied) || agentDenied(e.path) || refDenied(e.path)) && !dseen[e.path] {
			dseen[e.path] = true
			denied = append(denied, e.path)
		}
	}
	if len(denied) > 0 {
		return m.result(MergeGuarded, "the commits touch files host tools load or run by themselves "+
			"(agent, direnv and mise settings and the files they name, hooks, attributes, editor tasks, git config) or a submodule", denied)
	}

	// 4a'. The files those names really lead to: a protected name that is a
	// symbolic link in the host's work tree sends its tools to another file.
	// Only when nothing above refused, so a refused merge costs no more.
	links, lres := m.linkDenies(gc.hooks, gc.denied, importPaths, refPaths)
	if lres != nil {
		return lres
	}
	for _, e := range entries {
		if links.denied(e.path) && !dseen[e.path] {
			dseen[e.path] = true
			denied = append(denied, e.path)
		}
	}
	if len(denied) > 0 {
		return m.result(MergeGuarded, "the commits touch a file a protected name links to "+
			"(agent, direnv and mise settings and the files they name, hooks, git config)", denied)
	}

	// 4b. No path a git filter would process on checkout (one cheap check-attr,
	// before the full scan).
	filtered, err := m.filtered(paths)
	if err != nil {
		return m.failed("cannot check git attributes", err)
	}
	if len(filtered) > 0 {
		return m.result(MergeFilter, "a file the commits change has a git filter attribute (e.g. Git LFS)", filtered)
	}

	// 4c. The handback inspection of the range; anything short of a clean,
	// complete scan refuses. A scan that found none but could not finish (a
	// deadline, a git failure) is run again; flags found are held above.
	rep := InspectBranch(m.ctx, m.g, m.of, m.from, m.to, r.Lim)
	if len(rep.Flags) > 0 || rep.Truncated != "" || rep.Incomplete {
		var fp []string
		for _, f := range rep.Flags {
			fp = append(fp, f.Path)
		}
		detail := mergeFlaggedDetail
		if len(rep.Flags) == 0 {
			detail = "the commits could not be fully inspected"
		}
		if rep.Truncated != "" {
			detail += ": " + rep.Truncated
		}
		return m.result(MergeFlagged, detail, fp)
	}

	// 5. Nothing moved while it was being checked.
	if again, err := m.headBranch(); err != nil {
		return m.failed("cannot read HEAD", err)
	} else if again != x {
		return m.result(MergeMoved, "the checked-out branch changed while it was being checked", nil)
	}
	if tip, err := m.commitOf("refs/heads/" + x); err != nil {
		return m.failed("cannot read "+x, err)
	} else if tip != m.from {
		return m.result(MergeMoved, x+" moved while it was being checked", nil)
	}
	if res := m.clean(); res != nil { // never merge over an edit made while the guard ran
		return res
	}

	// 6. Audit, fail closed, then merge.
	detail := fmt.Sprintf("%s %s..%s %d commits", sanitizeN(x, maxErrRunes), m.from[:7], m.to[:7], m.commits)
	if r.Audit == nil || r.Audit("clone_merge_home", detail) != nil {
		return m.result(MergeFailed, "audit unavailable", nil)
	}
	// Detached from the caller's budget, so git is never killed half way
	// through a checkout, with its own cap.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(m.ctx), mergeTimeout)
	defer cancel()
	_, err = m.g.Run(mctx, m.of, append(m.env(), mergeReflog),
		hardened("-c", "submodule.recurse=false", "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", "--no-stat", "-q", m.to)...)
	if err != nil {
		_ = r.Audit("clone_merge_home_failed", detail)
		// ExecGit names the first argument ("-c" of hardenFlags) in its error.
		res := m.result(MergeFailed, "git merge: "+strings.TrimPrefix(err.Error(), "git -c: "), nil)
		res.Attempted = true
		return res
	}
	res := m.result("", "", nil)
	res.Merged = true
	return res
}

// clean returns nil when no tracked file has staged or unstaged changes, the
// dirty (or failed) result otherwise.
func (m *merger) clean() *MergeResult {
	out, err := m.run(append(m.env(), "GIT_OPTIONAL_LOCKS=0"), "status", "--porcelain=v1", "-z", "--untracked-files=no")
	if err != nil {
		return m.failed("cannot read the work tree's status", err)
	}
	if len(out) > 0 {
		return m.result(MergeDirty, "the work tree has uncommitted changes to tracked files", nil)
	}
	return nil
}

// mergeEntry is one raw-log line of the range: a path and whether either side
// of the change is a gitlink.
type mergeEntry struct {
	path    string
	gitlink bool
}

// parseMergeLog reads `log --format=%H --raw -z --no-renames --no-abbrev`
// output: commit ids, each followed by zero or more ":<old mode> <new mode>
// <old id> <new id> <status>" lines (the first after a newline) each followed
// by one path. Anything else is an error: the caller then does not merge.
func parseMergeLog(out []byte) (commits int, entries []mergeEntry, err error) {
	commits, entries, err = parseRaw(out, true)
	if err == nil && commits == 0 {
		return 0, nil, errors.New("the log lists no commits")
	}
	return commits, entries, err
}

// parseDiffTree reads `diff-tree -r -z --raw --no-renames --no-abbrev A B`
// output: change lines each followed by one path, no commit ids. Empty output
// (the same tree) is no entries.
func parseDiffTree(out []byte) ([]mergeEntry, error) {
	if len(out) == 0 {
		return nil, nil
	}
	_, entries, err := parseRaw(out, false)
	return entries, err
}

// parseRaw reads NUL-terminated raw-diff tokens; with log, change lines must
// follow a commit id. The read is a strict alternation of change line and
// path, so a path shaped like a change line or an id is never taken for one.
func parseRaw(out []byte, log bool) (commits int, entries []mergeEntry, err error) {
	if len(out) == 0 || out[len(out)-1] != 0 {
		return 0, nil, errors.New("the raw diff output is empty or cut short")
	}
	toks := bytes.Split(out[:len(out)-1], []byte{0})
	for i := 0; i < len(toks); i++ {
		tok := bytes.TrimPrefix(toks[i], []byte("\n"))
		if log && blobIDRE.Match(tok) {
			commits++
			continue
		}
		if len(tok) == 0 || tok[0] != ':' || (log && commits == 0) {
			return 0, nil, errors.New("unexpected token in the raw diff output")
		}
		f := strings.Fields(string(tok[1:]))
		if len(f) != 5 || !octalMode(f[0]) || !octalMode(f[1]) || !blobIDRE.MatchString(f[2]) ||
			!blobIDRE.MatchString(f[3]) || len(f[4]) != 1 || !strings.Contains("ADMT", f[4]) {
			return 0, nil, errors.New("unexpected change line in the log output")
		}
		i++
		if i >= len(toks) || len(toks[i]) == 0 {
			return 0, nil, errors.New("a change line without a path in the log output")
		}
		entries = append(entries, mergeEntry{path: string(toks[i]), gitlink: f[0] == "160000" || f[1] == "160000"})
	}
	return commits, entries, nil
}

func octalMode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

// mergeDenied reports whether p (a repo-relative path from the range) is a
// file host tools trust: MergeDenyNames, MergeDenyPatterns, MergeDenyDirs,
// MergeDenyTop, MergeDenyTopPaths, or at,
// under or a parent of one of hookDirs (repo-relative directories, "." for the
// whole repo). Case-insensitive throughout, by strings.EqualFold per path
// component: on a case-insensitive file system (macOS) .Claude/ is .claude/,
// and folding only adds refusals elsewhere.
func mergeDenied(p string, hookDirs []string) bool {
	comps := strings.Split(p, "/")
	for _, c := range comps {
		for _, d := range MergeDenyDirs {
			if strings.EqualFold(c, d) {
				return true
			}
		}
	}
	base := comps[len(comps)-1]
	for _, n := range MergeDenyNames {
		if strings.EqualFold(base, n) {
			return true
		}
	}
	fb := foldKey(base)
	for _, pat := range MergeDenyPatterns {
		if ok, _ := path.Match(foldKey(pat), fb); ok {
			return true
		}
	}
	for _, tp := range MergeDenyTopPaths {
		if foldPrefix(strings.Split(tp, "/"), comps) {
			return true
		}
	}
	for _, d := range MergeDenyTop {
		if strings.EqualFold(comps[0], d) {
			return true
		}
	}
	for _, hooks := range hookDirs {
		if hooks == "." {
			return true
		}
		// At, under, or above it: a symlink at a parent (tools -> evil)
		// re-points the hooks dir tools/hk at evil/hk.
		if hooks != "" {
			hc := strings.Split(hooks, "/")
			if foldPrefix(hc, comps) || foldPrefix(comps, hc) {
				return true
			}
		}
	}
	return false
}

// foldKey maps every letter of s to the smallest member of its case-folding
// orbit, so two strings that strings.EqualFold match have the same key, and a
// pattern keyed the same way matches what EqualFold would.
func foldKey(s string) string {
	return strings.Map(func(r rune) rune {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
		return m
	}, s)
}

// foldPrefix reports whether the path components pre are the first components
// of p (or all of them), each compared with strings.EqualFold.
func foldPrefix(pre, p []string) bool {
	if len(pre) > len(p) {
		return false
	}
	for i := range pre {
		if !strings.EqualFold(pre[i], p[i]) {
			return false
		}
	}
	return true
}

// mergeSystemGitConfig is the system git config the user's git reads. A var so
// a test can point it elsewhere.
var mergeSystemGitConfig = "/etc/gitconfig"

// hooksDirOf is the repo-relative directory the hooksPath value v
// names when it is relative (to the work tree's top, where hooks run) or
// absolute inside of; "" otherwise.
func hooksDirOf(of, home, v string) string {
	if home != "" && (v == "~" || strings.HasPrefix(v, "~/")) {
		v = filepath.Join(home, v[1:])
	}
	if v == "" || strings.HasPrefix(v, "~") {
		return "" // another user's home: not one the repo's commits write
	}
	rel := ""
	if !filepath.IsAbs(v) {
		rel = path.Clean(filepath.ToSlash(v))
	} else {
		if rel = absInside(of, v); rel == "" {
			return ""
		}
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// absInside is the absolute path v relative to of, slash-separated, when v is
// at or below of as spelled, through symlinks (of v, or of its parent when v
// does not exist), or with case folded; "" otherwise.
func absInside(of, v string) string {
	if rel := relInside(of, v); rel != "" {
		return rel
	}
	if ro, err := filepath.EvalSymlinks(of); err == nil {
		rv, err := filepath.EvalSymlinks(v)
		if err != nil {
			if rd, err2 := filepath.EvalSymlinks(filepath.Dir(v)); err2 == nil {
				rv, err = filepath.Join(rd, filepath.Base(v)), nil
			}
		}
		if err == nil {
			if rel := relInside(ro, rv); rel != "" {
				return rel
			}
		}
	}
	return relInside(strings.ToLower(of), strings.ToLower(v))
}

// relInside is v relative to dir, slash-separated, when v is at or below dir;
// "" otherwise.
func relInside(dir, v string) string {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(v))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// filtered returns the paths that have a filter attribute in the clean work
// tree. g must be a StdinGit.
func (m *merger) filtered(paths []string) ([]string, error) {
	sg, ok := m.g.(StdinGit)
	if !ok {
		return nil, errors.New("attribute check unavailable")
	}
	if len(paths) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}
	out, err := sg.RunStdin(m.ctx, m.of, m.env(), &in, hardened("check-attr", "-z", "--stdin", "filter")...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || out[len(out)-1] != 0 {
		return nil, errors.New("check-attr output is cut short")
	}
	toks := strings.Split(string(out[:len(out)-1]), "\x00")
	if len(toks) != 3*len(paths) {
		return nil, fmt.Errorf("check-attr printed %d fields for %d paths", len(toks), len(paths))
	}
	var hit []string
	for i := 0; i < len(toks); i += 3 {
		if toks[i+1] != "filter" {
			return nil, errors.New("check-attr printed an unexpected attribute")
		}
		if v := toks[i+2]; v != "unspecified" && v != "unset" {
			hit = append(hit, toks[i])
		}
	}
	return hit, nil
}
