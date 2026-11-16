package box

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// In-place guards (spec §5). A boxed agent writes the user's real repository,
// and some of what it could write there is later run or loaded by the HOST
// without anyone choosing to: git hooks and config-driven commands on routine
// git calls, CLAUDE.md / .claude/ / .mcp.json when a host-tier agent opens the
// same folder. inPlaceMounts derives the paths to bind read-only onto
// themselves, each a mount point the box can neither write nor rename away.
//
// The derivation fails closed: whatever it cannot read exactly (a symlink a
// box could re-point, a git config include, a submodule, an unreadable config)
// refuses in-place boxing instead of guessing.

// inPlaceFold is whether host paths compare ignoring letter case: macOS volumes
// are case-insensitive by default, so a hooks or git path spelled in another case
// is the same folder (#299). A variable so Linux tests can stand in for a Mac.
var inPlaceFold = runtime.GOOS == "darwin"

// sameHostDir reports whether a and b are one directory on disk. A variable so a
// Linux test can stand in for a case-insensitive volume, where two spellings
// of a folder are one directory.
var sameHostDir = func(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// sameDirAsWS reports whether the directory at p (fi, its Lstat) is the
// workspace on disk (wsFi, its Stat), however p is spelled. A variable so a
// Linux test can stand in for a host bind mount of the workspace.
var sameDirAsWS = func(p string, fi, wsFi fs.FileInfo) bool { return os.SameFile(fi, wsFi) }

// underWS reports whether the host path p is at or below the workspace ws. Every
// caller refuses or guards on "inside", so folding case only adds refusals.
func underWS(p, ws string) bool { return underMount(p, []string{ws}, inPlaceFold) }

// inPlaceTrustedHost is the way out of a refusal that has no local fix.
const inPlaceTrustedHost = "Run it on the trusted host instead (clone-mode profiles are coming)"

// inPlaceGuardFiles are guarded only when present: binding a file that does not
// exist would make docker create it.
var inPlaceGuardFiles = []string{"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", ".mcp.json"}

const (
	maxGitFile        = 1 << 20 // config, gitfile, commondir: larger is not a real repo's
	maxSymlinkFollows = 40      // as the kernel's ELOOP limit
)

func refuseInPlace(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUnboxableWorkspace}, a...)...)
}

// inPlaceMounts returns the read-only self-binds for the in-place workspace ws
// (absolute, symlinks resolved). Every Mount has Host == Box and is required.
// Every error wraps ErrUnboxableWorkspace and names the path and the way out.
// All access inside ws goes through an os.Root, so a symlink the box planted
// is never followed out of it. Each carries the identity of the file or
// directory it was derived from (the Lstat of its top-level name, taken here)
// and is marked for the in-box check, so the box docker creates must show these
// very files at these paths (Mount.CheckID, kind mountGuard).
func inPlaceMounts(ws string) ([]Mount, error) {
	return inPlaceMountsAt(workspace{Given: ws, Path: ws})
}

// inPlaceMountsAt is inPlaceMounts for a workspace Wrap resolved once. The
// resolution recorded the host's reading of its identity (HostID, on every OS):
// the folder opened here must be that directory. Opening ws.Path again follows
// whatever its components are now, so a component re-pointed since the
// resolution would otherwise have the guards derived (and a .claude created) in a
// folder that was never checked. Both are host readings, so a Mac's inode
// numbers do not matter here.
func inPlaceMountsAt(wsp workspace) ([]Mount, error) {
	out, _, err := inPlaceGuardsAt(wsp)
	return out, err
}

// guardFile is one guarded single file of an in-place workspace: Name directly
// in it, Info the Lstat taken while the guards were derived (the identity the
// macOS lock must find again, see guardLocks.hold).
type guardFile struct {
	Name string
	Info fs.FileInfo
}

// inPlaceGuardsAt is inPlaceMountsAt that also returns the guarded single
// files: the instruction and MCP files present, and .git when it is a gitfile.
// Directories are not among them: their read-only bind holds under every
// spelling of the name on a case-insensitive Mac volume, a file's does not (#96).
func inPlaceGuardsAt(wsp workspace) ([]Mount, []guardFile, error) {
	var files []guardFile
	out, err := inPlaceDerive(wsp, &files)
	if err != nil {
		return nil, nil, err
	}
	return out, files, nil
}

// inPlaceDerive derives the guards, appending each guarded single file to files.
func inPlaceDerive(wsp workspace, files *[]guardFile) ([]Mount, error) {
	ws := wsp.Path
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, refuseInPlace("%s cannot be opened to derive its read-only guards (%v). %s", ws, err, inPlaceTrustedHost)
	}
	defer root.Close()
	if wsp.HostID != nil {
		fi, err := root.Stat(".")
		if err != nil {
			return nil, wsChanged(ws, fmt.Sprintf("the folder opened to derive its guards cannot be read (%v)", err))
		}
		if got := hostFileID(fi); got == nil || *got != *wsp.HostID {
			return nil, wsChanged(ws, "the folder opened to derive its guards is not the one that was checked")
		}
	}

	// 1. Submodules: a submodule's .git file sits outside the guarded .git, and
	// host git recurses into it (D11).
	if _, err := root.Lstat(".gitmodules"); err == nil {
		return nil, refuseInPlace("%s has submodules (.gitmodules); a box could redirect a submodule's git directory "+
			"and host git would run what it plants. %s", ws, inPlaceTrustedHost)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, uncheckable(ws, ".gitmodules", err)
	}

	// 2. A symlink at a guarded name is a redirect the box can re-point.
	info := map[string]fs.FileInfo{}
	for _, name := range append([]string{".git", ".claude"}, inPlaceGuardFiles...) {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, uncheckable(ws, name, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, symlinked(filepath.Join(ws, name))
		}
		info[name] = fi
	}

	var out []Mount
	// fi is the Lstat of p taken while it was derived, so its identity is that of
	// what was checked. Off Linux the host records none (fileInfoID): the VM
	// reads it (see Manager.vmView).
	guard := func(p string, fi fs.FileInfo) {
		out = append(out, Mount{Host: p, Box: p, ID: fileInfoID(fi), CheckID: true, kind: mountGuard})
	}

	// 3. .git: a directory, or a gitfile (worktree / separate git dir) whose
	// gitdir: line the box would otherwise rewrite.
	gitFi := info[".git"]
	if gitFi != nil {
		if !gitFi.IsDir() && !gitFi.Mode().IsRegular() {
			return nil, refuseInPlace("%s is neither a directory nor a gitfile, so it cannot be guarded. Remove it, or %s",
				filepath.Join(ws, ".git"), lowerFirst(inPlaceTrustedHost))
		}
		guard(filepath.Join(ws, ".git"), gitFi)
		if gitFi.Mode().IsRegular() {
			*files = append(*files, guardFile{".git", gitFi})
		}
	}

	// 4. .claude always: created here when absent, as the daemon user. Left to
	// docker, the mountpoint would be root-owned and a host-tier agent could
	// never write its settings there again.
	claude := info[".claude"]
	if claude == nil {
		if claude, err = ensureClaudeDir(root, ws); err != nil {
			return nil, err
		}
	}
	if !claude.IsDir() {
		return nil, refuseInPlace("%s is not a directory, so it cannot be guarded. Remove or rename it, or %s",
			filepath.Join(ws, ".claude"), lowerFirst(inPlaceTrustedHost))
	}
	guard(filepath.Join(ws, ".claude"), claude)

	// 5. Instruction and MCP files, when present.
	for _, name := range inPlaceGuardFiles {
		fi := info[name]
		if fi == nil {
			continue
		}
		if !fi.Mode().IsRegular() {
			return nil, refuseInPlace("%s is not a regular file, so it cannot be guarded. Remove or rename it, or %s",
				filepath.Join(ws, name), lowerFirst(inPlaceTrustedHost))
		}
		guard(filepath.Join(ws, name), fi)
		*files = append(*files, guardFile{name, fi})
	}

	// 6 + 7. The hooks directory git uses, when it lies in the workspace.
	hooks, err := hooksGuard(root, ws, gitFi)
	if err != nil {
		return nil, err
	}
	if hooks != "" {
		// guardHooksDir returns <ws>/<first>: one name directly under ws.
		fi, err := root.Lstat(filepath.Base(hooks))
		if err != nil {
			return nil, uncheckable(ws, filepath.Base(hooks), err)
		}
		guard(hooks, fi)
	}
	return out, nil
}

func uncheckable(ws, name string, err error) error {
	return refuseInPlace("%s cannot be checked (%v), so its read-only guards cannot be derived. %s",
		filepath.Join(ws, name), err, inPlaceTrustedHost)
}

func symlinked(p string) error {
	return refuseInPlace("%s is a symlink; a box could re-point it at a file it controls, and host git or a "+
		"host-tier agent would follow it. Replace the link with what it points to, or %s", p, lowerFirst(inPlaceTrustedHost))
}

func lowerFirst(s string) string { return strings.ToLower(s[:1]) + s[1:] }

// ensureClaudeDir creates <ws>/.claude (0755) and returns what is there now. A
// concurrent creator wins the race harmlessly: whatever exists is judged like a
// .claude that was there all along, and a symlink is refused.
func ensureClaudeDir(root *os.Root, ws string) (fs.FileInfo, error) {
	p := filepath.Join(ws, ".claude")
	if err := root.Mkdir(".claude", 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, refuseInPlace("%s does not exist and cannot be created (%v); a box must not be able to create it. "+
			"Create it, or %s", p, err, lowerFirst(inPlaceTrustedHost))
	}
	fi, err := root.Lstat(".claude")
	if err != nil {
		return nil, uncheckable(ws, ".claude", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, symlinked(p)
	}
	return fi, nil
}

// hooksGuard reads the config git uses for this checkout (rule 6) and returns
// the directory to bind read-only so the hooks git runs stay out of the box's
// reach, "" for none (rule 7).
//
// Without <ws>/.git the workspace may be a subdirectory of a repository, whose
// rules then still apply to it: host git at that repository's root recurses
// into submodules under ws (D11) and runs hooks from a core.hooksPath under ws.
func hooksGuard(root *os.Root, ws string, gitFi fs.FileInfo) (string, error) {
	var (
		top      string // the worktree root core.hooksPath is relative to
		cfgs     [][]byte
		defHooks string
	)
	switch {
	case gitFi == nil:
		var err error
		if top, err = enclosingRepo(ws); err != nil || top == "" {
			return "", err
		}
		if cfgs, defHooks, err = enclosingConfig(ws, top); err != nil {
			return "", err
		}
	case gitFi.IsDir():
		top = ws
		// A commondir makes git read config and hooks from wherever it points,
		// not from .git/config. A main checkout never has one.
		if _, err := root.Lstat(filepath.Join(".git", "commondir")); err == nil {
			return "", refuseInPlace("%s has a commondir file, so git reads its config and hooks from elsewhere. "+
				"Remove it, or %s", filepath.Join(ws, ".git"), lowerFirst(inPlaceTrustedHost))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", uncheckable(ws, filepath.Join(".git", "commondir"), err)
		}
		for _, name := range []string{"config", "config.worktree"} {
			b, err := readInRoot(root, ws, filepath.Join(".git", name))
			if err != nil {
				return "", err
			}
			cfgs = append(cfgs, b)
		}
		defHooks = filepath.Join(ws, ".git", "hooks")
	default:
		top = ws
		b, err := readInRoot(root, ws, ".git")
		if err != nil {
			return "", err
		}
		gitdir, common, err := resolveGitfile(ws, ws, b)
		if err != nil {
			return "", err
		}
		if cfgs, err = outsideConfigs(ws, gitdir, common); err != nil {
			return "", err
		}
		defHooks = filepath.Join(common, "hooks")
	}

	// config.worktree is read after config, as git does when
	// extensions.worktreeConfig is on. Reading it regardless can only guard
	// more, never less.
	var core gitCore
	for _, b := range cfgs {
		c := parseGitCore(b)
		if c.bad {
			return "", refuseInPlace("the git config of %s cannot be parsed, so its core.hooksPath cannot be known. "+
				"Fix it (git config --list shows where), or %s", top, lowerFirst(inPlaceTrustedHost))
		}
		core.include = core.include || c.include
		core.worktree = core.worktree || c.worktree
		core.bare = core.bare || c.bare
		if c.hooksSet {
			core.hooksPath, core.hooksSet = c.hooksPath, true
		}
	}
	if core.include {
		return "", refuseInPlace("the git config of %s has an [include] or [includeIf] section, which can point git at "+
			"a file a box can write. Remove it, or %s", top, lowerFirst(inPlaceTrustedHost))
	}
	// git resolves a relative core.hooksPath against core.worktree, and a bare
	// repository has no worktree: neither is modelled, so neither is guessed at.
	if core.worktree {
		return "", refuseInPlace("the git config of %s sets core.worktree, so where git runs hooks from cannot be "+
			"derived exactly. %s", top, inPlaceTrustedHost)
	}
	if core.bare {
		return "", refuseInPlace("the git config of %s sets core.bare to something other than false, so where git runs "+
			"hooks from cannot be derived exactly. %s", top, inPlaceTrustedHost)
	}
	target := defHooks
	if core.hooksSet {
		var err error
		if target, err = hooksTarget(top, core.hooksPath); err != nil {
			return "", err
		}
		if err := dotDotUnder(target, ws, core.hooksPath); err != nil {
			return "", err
		}
	}
	return guardHooksDir(root, ws, target)
}

// enclosingRepo is the nearest ancestor of ws holding a .git (directory, file
// or link): the repository root host git finds from inside ws. "" when there
// is none up to /.
func enclosingRepo(ws string) (string, error) {
	for dir := filepath.Dir(ws); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", unreadable(filepath.Join(dir, ".git"), err)
		}
		if dir == filepath.Dir(dir) {
			return "", nil
		}
	}
}

// enclosingConfig applies the repository rules to top, the repository ws is a
// subdirectory of: submodules refuse, and its config is read as git reads it.
// Everything here is outside ws, so plain reads; anything leading back into ws
// is refused by gitPathOutside and readOutside.
func enclosingConfig(ws, top string) (cfgs [][]byte, defHooks string, err error) {
	if _, err := os.Lstat(filepath.Join(top, ".gitmodules")); err == nil {
		return nil, "", refuseInPlace("%s is inside the repository %s, which has submodules (.gitmodules); a box could "+
			"redirect a submodule's git directory and host git would run what it plants. %s", ws, top, inPlaceTrustedHost)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", unreadable(filepath.Join(top, ".gitmodules"), err)
	}
	dotgit := filepath.Join(top, ".git")
	real, err := gitPathOutside(ws, top, dotgit, dotgit, false)
	if err != nil {
		return nil, "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, "", unreadable(dotgit, err)
	}
	var gitdir, common string
	if fi.IsDir() {
		if _, err := os.Lstat(filepath.Join(real, "commondir")); err == nil {
			return nil, "", refuseInPlace("%s has a commondir file, so git reads its config and hooks from elsewhere. "+
				"Remove it, or %s", dotgit, lowerFirst(inPlaceTrustedHost))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", unreadable(filepath.Join(real, "commondir"), err)
		}
		gitdir, common = real, real
	} else {
		b, err := readOutside(ws, dotgit)
		if err != nil {
			return nil, "", err
		}
		if gitdir, common, err = resolveGitfile(ws, top, b); err != nil {
			return nil, "", err
		}
	}
	// A workspace inside the git directory itself writes its config and hooks.
	for _, d := range []string{gitdir, common} {
		if underWS(ws, d) {
			return nil, "", refuseInPlace("%s is inside the git directory %s, where a box could write its config and hooks. "+
				"Pick the repository's working tree instead, or %s", ws, d, lowerFirst(inPlaceTrustedHost))
		}
	}
	if cfgs, err = outsideConfigs(ws, gitdir, common); err != nil {
		return nil, "", err
	}
	return cfgs, filepath.Join(common, "hooks"), nil
}

// outsideConfigs reads <common>/config and <gitdir>/config.worktree.
func outsideConfigs(ws, gitdir, common string) ([][]byte, error) {
	var cfgs [][]byte
	for _, p := range []string{filepath.Join(common, "config"), filepath.Join(gitdir, "config.worktree")} {
		b, err := readOutside(ws, p)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, b)
	}
	return cfgs, nil
}

// hooksTarget is where git looks for hooks given core.hooksPath hp, as an
// absolute path, NOT cleaned (the .. are still in it, see dotDotUnder), or "" for a place that can never be in a workspace. Relative
// paths are relative to top, the worktree root.
func hooksTarget(top, hp string) (string, error) {
	switch {
	case hp == "":
		return "", nil // git then looks in "/" (measured, git 2.43)
	case strings.HasPrefix(hp, "%(prefix)/"):
		return "", nil // git's own install prefix
	case hp == "~" || strings.HasPrefix(hp, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", refuseInPlace("core.hooksPath of %s is %q, and the home directory it names is unknown (%v). "+
				"Spell it out in full, or %s", top, hp, err, lowerFirst(inPlaceTrustedHost))
		}
		return home + hp[1:], nil
	case strings.HasPrefix(hp, "~"):
		name, rest, _ := strings.Cut(hp[1:], "/")
		u, err := user.Lookup(name)
		if err != nil {
			return "", refuseInPlace("core.hooksPath of %s is %q, and that user's home is unknown (%v). "+
				"Spell it out in full, or %s", top, hp, err, lowerFirst(inPlaceTrustedHost))
		}
		return u.HomeDir + "/" + rest, nil
	case filepath.IsAbs(hp):
		return hp, nil
	default:
		return top + "/" + hp, nil
	}
}

// dotDotUnder refuses raw (the hooks path made absolute, not cleaned) when a
// ".." follows a component below the workspace (#304). Git resolves ".." on the
// real filesystem, the guard cleans it as text, and a box can swap any
// component below ws for a symlink, so the two could name different directories.
// A ".." right after ws itself is safe: ws is a mount point the box cannot
// replace, so ws/.. is the parent either way. A ".." wholly outside ws is host
// territory and stays accepted.
func dotDotUnder(raw, ws, hp string) error {
	nws := len(splitPath(ws))
	cur := string(filepath.Separator)
	for _, c := range strings.Split(raw, string(filepath.Separator)) {
		switch c {
		case "", ".":
		case "..":
			if len(splitPath(cur)) > nws && underWS(cur, ws) {
				return refuseInPlace("core.hooksPath of %s is %q, and its \"..\" follows %s, below the workspace: git resolves "+
					"\"..\" through the real directory, so a box could swap that component for a symlink and redirect "+
					"it. Write the path without \"..\", or %s", ws, hp, cur, lowerFirst(inPlaceTrustedHost))
			}
			cur = filepath.Dir(cur)
		default:
			cur = filepath.Join(cur, c)
		}
	}
	return nil
}

// respellUnder spells p, which is at or below ws (compared with inPlaceFold),
// with ws's own spelling for the part they share. filepath.Rel is case-sensitive:
// ws /T/Repo and p /T/repo/hooks would give ../repo/hooks, not hooks.
func respellUnder(p, ws string) string {
	if !inPlaceFold {
		return p
	}
	return filepath.Join(append([]string{string(filepath.Separator) + filepath.Join(splitPath(ws)...)},
		splitPath(p)[len(splitPath(ws)):]...)...)
}

// guardHooksDir decides about the hooks directory target (absolute; "" none)
// and returns the directory to bind read-only, "" for none.
//
// Inside the workspace, every component from ws down must be a real directory:
// a symlink is a redirect the box could re-point, and a missing one is a
// directory the box could create. Under .git or .claude it is already read-only
// and needs no mount of its own, but a symlink there still leads out into what
// the box can write. The path is walked like the kernel walks it
// (walkGitPath): a host symlink (which the box cannot touch) leading in is
// judged where it leads, and one that leaves ws again on the way is refused.
//
// What is bound is the FIRST component under ws, not the leaf. A mount point
// cannot be renamed (EBUSY), but its parent can: with only tools/hooks bound,
// `mv tools tools2 && mkdir -p tools/hooks` plants hooks at the very path host
// git runs (measured in a box, fix round 1). A direct child of ws is safe
// because ws itself is a mount point.
func guardHooksDir(root *os.Root, ws, target string) (string, error) {
	if target == "" {
		return "", nil
	}
	w, err := walkGitPath(string(filepath.Separator), target, ws)
	var dd *dotDotBelowError
	switch {
	case errors.As(err, &dd):
		return "", refuseInPlace("the git hooks directory %s of %s has a \"..\" after %s, below the workspace: git resolves "+
			"\"..\" through the real directory, so a box could swap that component for a symlink and redirect it. "+
			"Write the path without \"..\", or %s", target, ws, dd.at, lowerFirst(inPlaceTrustedHost))
	case err != nil:
		return "", refuseInPlace("the git hooks directory %s of %s cannot be resolved (%v). Fix or unset "+
			"core.hooksPath, or %s", target, ws, err, lowerFirst(inPlaceTrustedHost))
	case !w.below && w.end != ws:
		if underWS(w.end, ws) { // never: the walk spells the workspace as ws
			return "", refuseInPlace("the git hooks directory %s of %s cannot be placed. %s", target, ws, inPlaceTrustedHost)
		}
		return "", nil // outside, and never below ws on the way: host territory
	}
	target = w.end
	target = respellUnder(target, ws)
	rel, err := filepath.Rel(ws, target)
	if err != nil {
		return "", refuseInPlace("the git hooks directory %s of %s cannot be placed (%v). %s", target, ws, err, inPlaceTrustedHost)
	}
	if rel == "." {
		return "", refuseInPlace("core.hooksPath of %s is the workspace itself, where a box can write every hook. "+
			"Point it at a subdirectory, or unset core.hooksPath", ws)
	}
	if reason := MountUnsafe(target); reason != "" {
		return "", refuseInPlace("the git hooks directory %s cannot be guarded: %s. Rename it, or unset core.hooksPath", target, reason)
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	covered := first == ".git" || first == ".claude"
	cur := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := root.Lstat(cur)
		switch {
		case err == nil && fi.Mode()&fs.ModeSymlink != 0:
			return "", refuseInPlace("the git hooks directory %s is reached through the symlink %s; a box could re-point it. "+
				"Replace the link with a directory, or %s", target, filepath.Join(ws, cur), lowerFirst(inPlaceTrustedHost))
		case err == nil && fi.IsDir():
			continue
		case covered && (err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)):
			// Inside a read-only guard: the box cannot create or replace it.
			return "", nil
		case errors.Is(err, fs.ErrNotExist):
			return "", refuseInPlace("the git hooks directory %s (core.hooksPath) does not exist, so a box could create "+
				"it and fill it with hooks. Create it, or unset core.hooksPath", target)
		case err != nil:
			return "", uncheckable(ws, cur, err)
		default:
			return "", refuseInPlace("the git hooks directory %s (core.hooksPath) is not a directory, so a box could "+
				"replace it with one. Fix or unset core.hooksPath", filepath.Join(ws, cur))
		}
	}
	if covered {
		return "", nil
	}
	return filepath.Join(ws, first), nil
}

// resolveGitfile parses a gitfile's contents b (the .git file of the worktree
// at base) and returns the real git directory and common directory it leads
// to. Either one inside the workspace ws is box-writable, so it is refused,
// however it is spelled.
func resolveGitfile(ws, base string, b []byte) (gitdir, common string, err error) {
	gf := filepath.Join(base, ".git")
	line := strings.TrimRight(string(b), "\r\n")
	dir, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || dir == "" || strings.ContainsAny(dir, "\n\x00") {
		return "", "", refuseInPlace("%s is not a gitfile (one \"gitdir: <path>\" line). Fix it, or %s", gf, lowerFirst(inPlaceTrustedHost))
	}
	if gitdir, err = gitPathOutside(ws, base, dir, gf+" (gitdir)", true); err != nil {
		return "", "", err
	}
	common = gitdir
	cb, err := readOutside(ws, filepath.Join(gitdir, "commondir"))
	if err != nil {
		return "", "", err
	}
	if cb != nil {
		c := strings.TrimRight(string(cb), "\r\n")
		if common, err = gitPathOutside(ws, gitdir, c, filepath.Join(gitdir, "commondir"), true); err != nil {
			return "", "", err
		}
	}
	return gitdir, common, nil
}

// gitPathOutside resolves p, a git path named by what (relative to base when
// not absolute), and refuses it when it is in the workspace (as spelled, or once
// resolved), passes below it on the way, is missing, or (wantDir) is not a
// directory. It is walked like git and the kernel walk it (walkGitPath), not
// cleaned as text: a ".." after a component the box can swap for a link lands
// wherever the box wants.
func gitPathOutside(ws, base, p, what string, wantDir bool) (string, error) {
	shown := p // as git reads it, the ".." kept
	if !filepath.IsAbs(p) {
		shown = strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator) + p
	}
	if spelled := filepath.Clean(shown); underWS(spelled, ws) {
		return "", refuseInPlace("%s points git at %s, inside the workspace, where a box can write its config and hooks. %s",
			what, spelled, inPlaceTrustedHost)
	}
	w, err := walkGitPath(base, p, ws)
	var dd *dotDotBelowError
	if errors.As(err, &dd) || err == nil && (w.below || underWS(w.end, ws)) {
		return "", refuseInPlace("%s points git at %s, which leads into the workspace, where a box can write its config and hooks. %s",
			what, shown, inPlaceTrustedHost)
	}
	if err != nil {
		return "", refuseInPlace("%s points git at %s, which cannot be resolved (%v). Fix it, or %s",
			what, shown, err, lowerFirst(inPlaceTrustedHost))
	}
	real, err := filepath.EvalSymlinks(w.end)
	if err != nil {
		return "", refuseInPlace("%s points git at %s, which cannot be resolved (%v). Fix it, or %s",
			what, shown, err, lowerFirst(inPlaceTrustedHost))
	}
	if underWS(real, ws) {
		return "", refuseInPlace("%s points git at %s, which leads into the workspace, where a box can write its config and hooks. %s",
			what, shown, inPlaceTrustedHost)
	}
	if fi, err := os.Stat(real); wantDir && (err != nil || !fi.IsDir()) {
		return "", refuseInPlace("%s points git at %s, which is not a directory. Fix it, or %s", what, shown, lowerFirst(inPlaceTrustedHost))
	}
	return real, nil
}

// readInRoot reads rel inside the workspace: nil when absent; refused when it
// is a symlink, not a regular file, unreadable, or swapped while being opened.
func readInRoot(root *os.Root, ws, rel string) ([]byte, error) {
	p := filepath.Join(ws, rel)
	fi, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, uncheckable(ws, rel, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, symlinked(p)
	}
	if !fi.Mode().IsRegular() {
		return nil, refuseInPlace("%s is not a regular file, so git's view of it cannot be read. %s", p, inPlaceTrustedHost)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, unreadable(p, err)
	}
	defer f.Close()
	if fi2, err := f.Stat(); err != nil || !os.SameFile(fi, fi2) {
		return nil, refuseInPlace("%s changed while it was being read. Spawn again, or %s", p, lowerFirst(inPlaceTrustedHost))
	}
	return readCapped(f, p)
}

// readOutside reads p, a git file outside the workspace: nil when absent.
// Nothing in it is the box's to write, unless a symlink leads back in, or
// passes below ws on the way (walkGitPath), which is refused.
func readOutside(ws, p string) ([]byte, error) {
	w, err := walkGitPath(string(filepath.Separator), p, ws)
	var dd *dotDotBelowError
	if errors.As(err, &dd) || err == nil && (w.below || underWS(w.end, ws)) {
		return nil, refuseInPlace("%s leads into the workspace, where a box can write it. %s", p, inPlaceTrustedHost)
	}
	if err != nil {
		return nil, unreadable(p, err)
	}
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, unreadable(p, err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, unreadable(p, err)
	}
	if underWS(real, ws) {
		return nil, refuseInPlace("%s leads into the workspace, where a box can write it. %s", p, inPlaceTrustedHost)
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, unreadable(p, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, refuseInPlace("%s is not a regular file, so git's view of it cannot be read. %s", p, inPlaceTrustedHost)
	}
	return readCapped(f, p)
}

func readCapped(r io.Reader, p string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxGitFile+1))
	if err != nil {
		return nil, unreadable(p, err)
	}
	if len(b) > maxGitFile {
		return nil, refuseInPlace("%s is larger than %d bytes, so it is not read. %s", p, maxGitFile, inPlaceTrustedHost)
	}
	if b == nil {
		b = []byte{} // present but empty: not "absent"
	}
	return b, nil
}

func unreadable(p string, err error) error {
	return refuseInPlace("%s cannot be read (%v), so its read-only guards cannot be derived. %s", p, err, inPlaceTrustedHost)
}

// gitWalk is where a git path leads, walked as the kernel walks it.
type gitWalk struct {
	// end is the physical path (every symlink followed, ".." taken from the real
	// directory). A missing component and the rest are appended as written:
	// where it would be if created now. When the walk reaches the workspace, end
	// spells it as ws.
	end string
	// below is whether the walk went strictly below ws. From there on end is ws
	// joined with the rest as written, NOT walked: those components are the
	// box's, so they are the caller's to check (guardHooksDir through the
	// os.Root, which refuses a symlink) or to refuse outright.
	below bool
}

// dotDotBelowError is a ".." met strictly below the workspace: git takes it
// from whatever directory is there when it runs, and the box can make that any
// directory by swapping a component for a symlink.
type dotDotBelowError struct{ at string }

func (e *dotDotBelowError) Error() string {
	return fmt.Sprintf("\"..\" after %s, below the workspace", e.at)
}

// walkGitPath resolves the git path p (relative to base when not absolute) one
// component at a time, as the kernel (and git's own realpath) does: every
// symlink is followed (at most maxSymlinkFollows), its target's components
// taken next (an absolute target restarts at /, a relative one at the link's
// directory), and ".." goes to the parent of the physical current directory.
// Deciding from the end point alone is not enough (#304): a path can pass
// through ws, where the box can swap any component for a link, and still end
// outside it. So once the walk steps strictly below ws it stops following
// anything: a ".." from there on is refused (*dotDotBelowError), and the rest
// is returned unwalked with below set. A ".." taken at ws itself is safe: ws is
// a mount point the box can neither replace nor rename, so ws/.. is its parent.
//
// A directory entered that is a folder of ws under another path (a host bind
// mount of it, found through the mount table: wsAlias) is that folder, so the
// walk is below ws from there (#308). A ".." taken right at it is its host
// parent, as at ws: the first such directory on a path is a mount point on the
// host, which the box cannot replace.
func walkGitPath(base, p, ws string) (gitWalk, error) {
	sep := string(filepath.Separator)
	cur := filepath.Clean(base)
	if filepath.IsAbs(p) {
		cur = sep
	}
	queue := strings.Split(p, sep)
	nws := len(splitPath(ws))
	if UnderMount(cur, []string{ws}) && cur != ws {
		// Starts below ws (no caller does): all of it is the box's.
		rel, _ := filepath.Rel(ws, cur)
		return walkBelow(ws, append(strings.Split(rel, sep), queue...))
	}
	// ws on disk: a directory the walk enters that IS ws, however it is spelled
	// (another Unicode normalization on APFS, a host bind mount), is ws.
	wsFi, err := os.Stat(ws)
	if err != nil {
		return gitWalk{}, err
	}
	// at: where in the workspace cur is ("" outside it, "." the workspace).
	// Matched by spelling, cur is ws itself; matched by inode under another path
	// (a host bind mount of ws or of a folder in it), cur keeps that path, since a
	// ".." from there goes to ITS parent, as the kernel takes it.
	at := ""
	if cur == ws {
		at = "."
	}
	var mounts []mountEntry // the mount table, read once a directory needs it
	inWS := func(next string, fi fs.FileInfo) (string, error) {
		if fi.IsDir() && sameDirAsWS(next, fi, wsFi) {
			return ".", nil
		}
		if hostMountInfo == nil {
			return "", nil
		}
		if mounts == nil {
			b, err := hostMountInfo()
			if err == nil {
				mounts, err = parseMountInfo(b)
			}
			if err != nil {
				return "", fmt.Errorf("the mount table cannot be read: %w", err)
			}
		}
		return wsAlias(mounts, next, ws)
	}
	missing := 0 // trailing components of cur that do not exist
	for follows := 0; len(queue) > 0; {
		c := queue[0]
		queue = queue[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if missing > 0 {
				missing--
			}
			cur, at = filepath.Dir(cur), ""
			continue
		}
		if at != "" {
			return walkBelow(ws, append(append(relParts(at), c), queue...))
		}
		next := filepath.Join(cur, c)
		if len(splitPath(next)) == nws && isWS(next, ws) {
			// An other-case spelling: a plain directory has ws's parent, and a
			// symlink that is ws leads to ws, so ws stands for it.
			cur, at, missing = ws, ".", 0
			continue
		}
		if missing > 0 {
			cur, missing = next, missing+1
			continue
		}
		fi, err := os.Lstat(next)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			cur, missing = next, 1
			continue
		case err != nil:
			return gitWalk{}, err
		case fi.Mode()&fs.ModeSymlink == 0:
			cur = next
			if at, err = inWS(next, fi); err != nil {
				return gitWalk{}, err
			}
			continue
		}
		if follows++; follows > maxSymlinkFollows {
			return gitWalk{}, fmt.Errorf("%s: too many levels of symbolic links", next)
		}
		t, err := os.Readlink(next)
		if err != nil {
			return gitWalk{}, err
		}
		if t == "" {
			return gitWalk{}, fmt.Errorf("%s: empty symbolic link", next)
		}
		if filepath.IsAbs(t) {
			cur = sep
		}
		queue = append(strings.Split(t, sep), queue...)
	}
	switch at {
	case "":
		return gitWalk{end: cur}, nil
	case ".":
		return gitWalk{end: ws}, nil
	}
	return walkBelow(ws, relParts(at))
}

// relParts splits rel, a path relative to the workspace, into its components.
func relParts(rel string) []string {
	return strings.Split(rel, string(filepath.Separator))
}

// walkBelow is the rest of a walk that stepped strictly below ws with rest.
func walkBelow(ws string, rest []string) (gitWalk, error) {
	at := ws
	for _, c := range rest {
		if c == ".." {
			return gitWalk{}, &dotDotBelowError{at: at}
		}
		at = filepath.Join(at, c)
	}
	return gitWalk{end: at, below: true}, nil
}

// isWS reports whether next, a path as deep as ws, is the workspace: spelled as
// ws, or (inPlaceFold) spelled in another case and the same directory on disk.
// An other-case spelling that is a symlink is a separate entry, so it is
// followed instead; one that is missing or a plain directory is judged as ws,
// as the guard did before the walk (folding only adds guards and refusals).
func isWS(next, ws string) bool {
	if next == ws {
		return true
	}
	if !inPlaceFold || !underWS(next, ws) {
		return false
	}
	if sameHostDir(next, ws) {
		return true
	}
	fi, err := os.Lstat(next)
	return err != nil || fi.Mode()&fs.ModeSymlink == 0
}

// gitCore is what the guard derivation needs from one git config file.
type gitCore struct {
	hooksPath string
	hooksSet  bool // core.hooksPath appears (last value wins)
	include   bool // an [include] or [includeIf …] section
	worktree  bool // core.worktree appears, with any value
	bare      bool // core.bare appears with anything but an explicit false
	bad       bool // git would reject the file; nothing after the error is read
}

// gitConfigCore is the minimal git-config reader: core.hooksPath (from [core]
// only, not [core "x"]; last value wins) and whether any include section exists.
func gitConfigCore(data []byte) (hooksPath string, include bool) {
	c := parseGitCore(data)
	return c.hooksPath, c.include
}

// cfgEntries walks a git config file with git's own parser (config.c, git 2.43):
// section and key names case-insensitive, a subsection in quotes (case kept) or
// after a dot, # and ; comments outside quotes, double quotes, \-escapes (\t \b
// \n \\ \" and line continuation), and runs of unquoted whitespace inside a value
// kept as spaces. A key may follow its section header on the same line.
//
// It calls fn for every key, in order, with the section as git names it (the base
// lowercased, a subsection with its case kept: "remote.origin"), the lowercased
// key, and its value; hasVal is false for a bare key (boolean true). fn returns
// true to stop the walk early. include reports whether an [include] or
// [includeIf …] section was seen before the walk ended. ok is false when git
// would reject the file: nothing after the error is read, rather than skipped.
func cfgEntries(data []byte, fn func(section, key, val string, hasVal bool) (stop bool)) (include, ok bool) {
	s := &cfgScanner{s: strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\xef\xbb\xbf")}
	section := ""
	comment := false
	for {
		ch, eof := s.next()
		if ch == '\n' {
			if eof {
				return include, true
			}
			comment = false
			continue
		}
		if comment || cfgSpace(ch) {
			continue
		}
		if ch == '#' || ch == ';' {
			comment = true
			continue
		}
		if ch == '[' {
			name, ok := s.section()
			if !ok {
				return include, false
			}
			section = name
			if base, _, _ := strings.Cut(name, "."); base == "include" || base == "includeif" {
				include = true
			}
			continue
		}
		if !cfgAlpha(ch) {
			return include, false
		}
		var key strings.Builder
		key.WriteByte(cfgLower(ch))
		for {
			ch, eof = s.next()
			if eof || !cfgKeyChar(ch) {
				break
			}
			key.WriteByte(cfgLower(ch))
		}
		for ch == ' ' || ch == '\t' {
			ch, _ = s.next()
		}
		val, hasVal := "", false
		if ch != '\n' {
			if ch != '=' {
				return include, false
			}
			v, ok := s.value()
			if !ok {
				return include, false
			}
			val, hasVal = v, true
		}
		if fn(section, key.String(), val, hasVal) {
			return include, true
		}
	}
}

// parseGitCore reads what the guard derivation needs from a git config file
// (see cfgEntries for the parser). Anything git would reject marks it bad.
func parseGitCore(data []byte) (c gitCore) {
	include, ok := cfgEntries(data, func(section, key, val string, hasVal bool) bool {
		switch {
		case section == "core" && key == "worktree":
			c.worktree = true
		case section == "core" && key == "bare":
			if !(hasVal && gitFalse(val)) {
				c.bare = true // a bare key is boolean true
			}
		case section == "core" && key == "hookspath":
			if !hasVal {
				c.bad = true // a bare key is boolean true: git dies on it
				return true
			}
			c.hooksPath, c.hooksSet = val, true
		}
		return false
	})
	c.include = include
	if !ok {
		c.bad = true
	}
	return c
}

// GitConfigValues returns every value of section.key in one git config file, in
// order. section is named as git names it: the base lowercased, a quoted
// subsection with its case kept ("remote.origin"); key is lowercase. ok is
// false when the list cannot be trusted to be complete or valid: git would
// reject the file (nothing after the error is read, so a value could hide behind
// it), the file has an [include] or [includeIf …] section (git would also read
// values from the included file), or a matching key has no value at all (git
// dies reading it as a string).
func GitConfigValues(data []byte, section, key string) (vals []string, ok bool) {
	bare := false
	include, valid := cfgEntries(data, func(sec, k, val string, hasVal bool) bool {
		if sec != section || k != key {
			return false
		}
		if !hasVal {
			bare = true
			return true
		}
		vals = append(vals, val)
		return false
	})
	if !valid || include || bare {
		return nil, false
	}
	return vals, true
}

// gitFalse: v is one of git's spellings of boolean false. An empty value is
// false to git too, but is not accepted here: only a spelled-out false is.
func gitFalse(v string) bool {
	switch strings.ToLower(v) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}

type cfgScanner struct {
	s string
	i int
}

// next returns the next byte, or '\n' with eof at the end, as git's
// get_next_char does.
func (s *cfgScanner) next() (byte, bool) {
	if s.i >= len(s.s) {
		return '\n', true
	}
	s.i++
	return s.s[s.i-1], false
}

// section reads a header after its '['. The base name is lowercased; a quoted
// subsection keeps its case and is joined with a dot, as git names it.
func (s *cfgScanner) section() (string, bool) {
	var name strings.Builder
	for {
		ch, eof := s.next()
		if eof {
			return "", false
		}
		if ch == ']' {
			break
		}
		if cfgSpace(ch) {
			for cfgSpace(ch) {
				if ch == '\n' {
					return "", false
				}
				ch, _ = s.next()
			}
			if ch != '"' {
				return "", false
			}
			name.WriteByte('.')
			for {
				ch, _ = s.next()
				if ch == '\n' {
					return "", false
				}
				if ch == '"' {
					break
				}
				if ch == '\\' {
					if ch, _ = s.next(); ch == '\n' {
						return "", false
					}
				}
				name.WriteByte(ch)
			}
			if ch, _ = s.next(); ch != ']' {
				return "", false
			}
			break
		}
		if !cfgKeyChar(ch) && ch != '.' {
			return "", false
		}
		name.WriteByte(cfgLower(ch))
	}
	if name.Len() == 0 {
		return "", false
	}
	return name.String(), true
}

// value reads a value after its '=', up to and including the end of the line.
func (s *cfgScanner) value() (string, bool) {
	var b strings.Builder
	quote, comment, space := false, false, 0
	for {
		ch, _ := s.next()
		if ch == '\n' {
			return b.String(), !quote
		}
		if comment {
			continue
		}
		if cfgSpace(ch) && !quote {
			if b.Len() > 0 {
				space++
			}
			continue
		}
		if !quote && (ch == ';' || ch == '#') {
			comment = true
			continue
		}
		for ; space > 0; space-- {
			b.WriteByte(' ')
		}
		if ch == '\\' {
			ch, _ = s.next()
			switch ch {
			case '\n':
				continue
			case 't':
				ch = '\t'
			case 'b':
				ch = '\b'
			case 'n':
				ch = '\n'
			case '\\', '"':
			default:
				return "", false
			}
			b.WriteByte(ch)
			continue
		}
		if ch == '"' {
			quote = !quote
			continue
		}
		b.WriteByte(ch)
	}
}

// git's sane_ctype: isspace is exactly these four.
func cfgSpace(c byte) bool   { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func cfgAlpha(c byte) bool   { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func cfgKeyChar(c byte) bool { return cfgAlpha(c) || c >= '0' && c <= '9' || c == '-' }
func cfgLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
