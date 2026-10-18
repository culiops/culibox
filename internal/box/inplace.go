package box

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
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
// is never followed out of it.
func inPlaceMounts(ws string) ([]Mount, error) {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, refuseInPlace("%s cannot be opened to derive its read-only guards (%v). %s", ws, err, inPlaceTrustedHost)
	}
	defer root.Close()

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
	guard := func(p string) { out = append(out, Mount{Host: p, Box: p}) }

	// 3. .git: a directory, or a gitfile (worktree / separate git dir) whose
	// gitdir: line the box would otherwise rewrite.
	gitFi := info[".git"]
	if gitFi != nil {
		if !gitFi.IsDir() && !gitFi.Mode().IsRegular() {
			return nil, refuseInPlace("%s is neither a directory nor a gitfile, so it cannot be guarded. Remove it, or %s",
				filepath.Join(ws, ".git"), lowerFirst(inPlaceTrustedHost))
		}
		guard(filepath.Join(ws, ".git"))
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
	guard(filepath.Join(ws, ".claude"))

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
		guard(filepath.Join(ws, name))
	}

	// 6 + 7. The hooks directory git uses, when it lies in the workspace.
	hooks, err := hooksGuard(root, ws, gitFi)
	if err != nil {
		return nil, err
	}
	if hooks != "" {
		guard(hooks)
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
	real, err := gitPathOutside(ws, dotgit, dotgit, false)
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
		if UnderMount(ws, []string{d}) {
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
// absolute path, or "" for a place that can never be in a workspace. Relative
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
		return filepath.Join(home, hp[1:]), nil
	case strings.HasPrefix(hp, "~"):
		name, rest, _ := strings.Cut(hp[1:], "/")
		u, err := user.Lookup(name)
		if err != nil {
			return "", refuseInPlace("core.hooksPath of %s is %q, and that user's home is unknown (%v). "+
				"Spell it out in full, or %s", top, hp, err, lowerFirst(inPlaceTrustedHost))
		}
		return filepath.Join(u.HomeDir, rest), nil
	case filepath.IsAbs(hp):
		return filepath.Clean(hp), nil
	default:
		return filepath.Join(top, hp), nil
	}
}

// guardHooksDir decides about the hooks directory target (absolute; "" none)
// and returns the directory to bind read-only, "" for none.
//
// Inside the workspace, every component from ws down must be a real directory:
// a symlink is a redirect the box could re-point, and a missing one is a
// directory the box could create. Under .git or .claude it is already read-only
// and needs no mount of its own, but a symlink there still leads out into what
// the box can write. Outside, it is resolved: a host symlink (which the box
// cannot touch) leading back in is judged at the real path.
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
	target = filepath.Clean(target)
	if !UnderMount(target, []string{ws}) {
		real, err := whereCreated(target)
		if err != nil {
			return "", refuseInPlace("the git hooks directory %s of %s cannot be resolved (%v). Fix or unset "+
				"core.hooksPath, or %s", target, ws, err, lowerFirst(inPlaceTrustedHost))
		}
		if !UnderMount(real, []string{ws}) {
			return "", nil
		}
		target = real
	}
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
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	if gitdir, err = gitPathOutside(ws, filepath.Clean(dir), gf+" (gitdir)", true); err != nil {
		return "", "", err
	}
	common = gitdir
	cb, err := readOutside(ws, filepath.Join(gitdir, "commondir"))
	if err != nil {
		return "", "", err
	}
	if cb != nil {
		c := strings.TrimRight(string(cb), "\r\n")
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		if common, err = gitPathOutside(ws, filepath.Clean(c), filepath.Join(gitdir, "commondir"), true); err != nil {
			return "", "", err
		}
	}
	return gitdir, common, nil
}

// gitPathOutside resolves p, a git path named by what, and refuses it when it
// is in the workspace (as spelled, or once resolved), missing, or (wantDir) not
// a directory.
func gitPathOutside(ws, p, what string, wantDir bool) (string, error) {
	if UnderMount(p, []string{ws}) {
		return "", refuseInPlace("%s points git at %s, inside the workspace, where a box can write its config and hooks. %s",
			what, p, inPlaceTrustedHost)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", refuseInPlace("%s points git at %s, which cannot be resolved (%v). Fix it, or %s",
			what, p, err, lowerFirst(inPlaceTrustedHost))
	}
	if UnderMount(real, []string{ws}) {
		return "", refuseInPlace("%s points git at %s, which leads into the workspace, where a box can write its config and hooks. %s",
			what, p, inPlaceTrustedHost)
	}
	if fi, err := os.Stat(real); wantDir && (err != nil || !fi.IsDir()) {
		return "", refuseInPlace("%s points git at %s, which is not a directory. Fix it, or %s", what, p, lowerFirst(inPlaceTrustedHost))
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
// Nothing in it is the box's to write, unless a symlink leads back in, which is
// refused.
func readOutside(ws, p string) ([]byte, error) {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, unreadable(p, err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, unreadable(p, err)
	}
	if UnderMount(real, []string{ws}) {
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

// whereCreated resolves p's symlinks as far as p exists and appends the
// missing rest: where p would be if it were created now. A dangling symlink is
// followed to what it names, since creating p would create that.
func whereCreated(p string) (string, error) {
	p = filepath.Clean(p)
	var tail []string
	for follows := 0; ; {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			slices.Reverse(tail)
			return filepath.Join(append([]string{real}, tail...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			if follows++; follows > maxSymlinkFollows {
				return "", fmt.Errorf("%s: too many levels of symbolic links", p)
			}
			t, err := os.Readlink(p)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(t) {
				dir, err := filepath.EvalSymlinks(filepath.Dir(p))
				if err != nil {
					return "", err
				}
				t = filepath.Join(dir, t)
			}
			p = filepath.Clean(t)
			continue
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
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

// parseGitCore follows git's own config parser (config.c, git 2.43): section and
// key names case-insensitive, a subsection in quotes (case kept) or after a dot,
// # and ; comments outside quotes, double quotes, \-escapes (\t \b \n \\ \" and
// line continuation), and runs of unquoted whitespace inside a value kept as
// spaces. A key may follow its section header on the same line. Anything git
// would reject marks the file bad rather than being skipped.
func parseGitCore(data []byte) (c gitCore) {
	s := &cfgScanner{s: strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\xef\xbb\xbf")}
	section := ""
	comment := false
	for {
		ch, eof := s.next()
		if ch == '\n' {
			if eof {
				return c
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
				c.bad = true
				return c
			}
			section = name
			if base, _, _ := strings.Cut(name, "."); base == "include" || base == "includeif" {
				c.include = true
			}
			continue
		}
		if !cfgAlpha(ch) {
			c.bad = true
			return c
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
				c.bad = true
				return c
			}
			v, ok := s.value()
			if !ok {
				c.bad = true
				return c
			}
			val, hasVal = v, true
		}
		if section == "core" && key.String() == "worktree" {
			c.worktree = true
		}
		if section == "core" && key.String() == "bare" && !(hasVal && gitFalse(val)) {
			c.bare = true // a bare key is boolean true
		}
		if section == "core" && key.String() == "hookspath" {
			if !hasVal {
				c.bad = true // a bare key is boolean true: git dies on it
				return c
			}
			c.hooksPath, c.hooksSet = val, true
		}
	}
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
