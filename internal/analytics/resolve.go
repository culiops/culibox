package analytics

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/culiops/culibox/internal/gitrepo"
)

// ProfileRef is the part of a configured profile that workspace resolution reads.
type ProfileRef struct{ Name, DefaultCwd, CloneOf, Account string }

// ResolveEnv is the live config Resolve judges a session against. The daemon
// builds it; Resolve itself reads nothing but what it is given.
type ResolveEnv struct {
	Profiles  []ProfileRef
	CloneDirs []string // <dataDir>/clone-dirs lines
	// ConvKey is the key of the in-place conversations dir a profile would give a
	// workspace (box.Manager.ConversationsKeyFor). nil, or "" for a profile, means
	// that profile cannot be matched.
	ConvKey func(workspace, account, profile string) string
	// RepoRoot is the host repository root of a host-runtime folder; "" when it is
	// not in a repository. nil means identity. Never called for a box runtime.
	RepoRoot func(cwd string) string
	FoldCase bool // darwin: path compares ignore case
}

// DimIn is what a session carries into resolution: its slug, its first cwd, its
// runtime and its root_key ("<box>" or ".conversations/<x>").
type DimIn struct{ Project, Cwd, Runtime, RootKey string }

// Resolved is a session's workspace, the box profile it ran under ("" for host
// and unnamed box sessions), how it was found (clone|worktree|host|inplace|legacy),
// the clone or worktree folder when Via names one, and whether a legacy session
// found no single workspace.
type Resolved struct {
	Workspace, Profile, Via, ViaPath string
	Unmatched                        bool
}

// slugNonAlnum is session.slugNonAlnum, copied so this package does not import
// session: Claude Code's transcript-dir encoder, every non-alphanumeric becomes "-".
var slugNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// projectSlug mirrors session.projectSlug (internal/session/usage.go).
func projectSlug(cwd string) string { return slugNonAlnum.ReplaceAllString(cwd, "-") }

// worktreeMarkers are the folders a worktree of a repository lives under.
var worktreeMarkers = []string{"/.claude/worktrees/", "/.worktrees/"}

const convRootPrefix = ".conversations/"

// Resolve applies spec §6.3 rules 1 to 5, first match wins. It is pure string
// work plus config: a box-supplied path (cwd, any prefix of it, a known
// candidate) is only cleaned, never stat'ed or resolved through links. Only
// config-derived paths and the host-runtime fallback go through gitrepo.Normalize,
// and RepoRoot is called only for a host runtime.
func Resolve(d DimIn, env ResolveEnv, known []string) Resolved {
	if d.Cwd == "" {
		return resolveLegacy(d, env, known)
	}
	cwd := filepath.Clean(d.Cwd)
	if prefix, wt, ok := splitWorktree(cwd, env.FoldCase); ok {
		r := resolveFolder(prefix, d, env)
		r.Via, r.ViaPath = "worktree", wt
		return r
	}
	return resolveFolder(cwd, d, env)
}

// resolveFolder runs rules 1, 3 and 4 on one folder.
func resolveFolder(dir string, d DimIn, env ResolveEnv) Resolved {
	// Rule 1: a clone box. The workspace is the repo it clones.
	if p, ok := cloneProfile(dir, env); ok {
		r := Resolved{Workspace: gitrepo.Normalize(p.CloneOf), Via: "clone", ViaPath: filepath.Clean(p.DefaultCwd)}
		if d.Runtime != "host" {
			r.Profile = p.Name
		}
		return r
	}
	// Rule 3: the host runtime. The only place a path is read from disk.
	if d.Runtime == "host" {
		ws := dir
		if env.RepoRoot != nil {
			if ws = env.RepoRoot(dir); ws == "" {
				ws = gitrepo.Normalize(dir)
			}
		}
		return Resolved{Workspace: ws, Via: "host"}
	}
	// Rule 4: a box runtime that is not a clone. The cwd is the host path as-is.
	return Resolved{Workspace: dir, Profile: inplaceProfile(dir, d.RootKey, env), Via: "inplace"}
}

// cloneProfile is the profile whose default_cwd, with a clone_of, holds dir. The
// innermost default_cwd wins; the first profile wins a tie.
func cloneProfile(dir string, env ResolveEnv) (ProfileRef, bool) {
	var best ProfileRef
	bestLen, found := -1, false
	for _, p := range env.Profiles {
		if p.DefaultCwd == "" || p.CloneOf == "" {
			continue
		}
		dc := filepath.Clean(p.DefaultCwd)
		if len(dc) > bestLen && within(dc, dir, env.FoldCase) {
			best, bestLen, found = p, len(dc), true
		}
	}
	return best, found
}

// inplaceProfile names the profile whose conversations key for this workspace is
// the session's root_key. "" when root_key is not a .conversations dir or none match.
func inplaceProfile(workspace, rootKey string, env ResolveEnv) string {
	if env.ConvKey == nil || !strings.HasPrefix(rootKey, convRootPrefix) {
		return ""
	}
	for _, p := range env.Profiles {
		if k := env.ConvKey(workspace, p.Account, p.Name); k != "" && convRootPrefix+k == rootKey {
			return p.Name
		}
	}
	return ""
}

// splitWorktree splits a clean path at the first /.claude/worktrees/<name> or
// /.worktrees/<name>: the folder before it, and the worktree's own folder.
func splitWorktree(cwd string, fold bool) (prefix, worktree string, ok bool) {
	hay := cwd
	if fold {
		hay = asciiLower(cwd)
	}
	at, mlen := -1, 0
	for _, m := range worktreeMarkers {
		if i := strings.Index(hay, m); i >= 0 && (at < 0 || i < at) {
			at, mlen = i, len(m)
		}
	}
	if at < 0 || at+mlen >= len(cwd) {
		return "", "", false
	}
	end := len(cwd)
	if i := strings.IndexByte(cwd[at+mlen:], '/'); i >= 0 {
		end = at + mlen + i
	}
	prefix = cwd[:at]
	if prefix == "" {
		prefix = "/"
	}
	return prefix, cwd[:end], true
}

// resolveLegacy is rule 5: a session with no cwd is matched to a workspace by
// its project slug against every folder the config and earlier sessions know.
func resolveLegacy(d DimIn, env ResolveEnv, known []string) Resolved {
	var found []string
	seen := map[string]bool{}
	add := func(ws string) {
		key := ws
		if env.FoldCase {
			key = asciiLower(ws)
		}
		if !seen[key] {
			seen[key] = true
			found = append(found, ws)
		}
	}
	matches := func(c string) bool { return c != "" && foldEq(projectSlug(c), d.Project, env.FoldCase) }

	for _, k := range known { // may be box-supplied: cleaned, never resolved
		if k == "" {
			continue
		}
		if c := filepath.Clean(k); matches(c) {
			if isCloneDir(c, env) {
				add(cloneDirWorkspace(c, env))
			} else {
				add(c)
			}
		}
	}
	for _, p := range env.Profiles {
		if p.DefaultCwd != "" {
			if c := filepath.Clean(p.DefaultCwd); matches(c) {
				add(cloneDirWorkspace(c, env))
			}
		}
		if p.CloneOf != "" {
			if c := filepath.Clean(p.CloneOf); matches(c) {
				add(gitrepo.Normalize(c))
			}
		}
	}
	for _, cd := range env.CloneDirs {
		if cd == "" {
			continue
		}
		if c := filepath.Clean(cd); matches(c) {
			add(cloneDirWorkspace(c, env))
		}
	}
	if len(found) == 1 {
		return Resolved{Workspace: found[0], Via: "legacy"}
	}
	return Resolved{Workspace: d.Project, Via: "legacy", Unmatched: true}
}

// isCloneDir reports whether c is a clone dir: a clone-dirs entry or a profile's default_cwd.
func isCloneDir(c string, env ResolveEnv) bool {
	for _, cd := range env.CloneDirs {
		if cd != "" && foldEq(filepath.Clean(cd), c, env.FoldCase) {
			return true
		}
	}
	for _, p := range env.Profiles {
		if p.DefaultCwd != "" && foldEq(filepath.Clean(p.DefaultCwd), c, env.FoldCase) {
			return true
		}
	}
	return false
}

// cloneDirWorkspace is the repo a clone dir stands for: the clone_of of a profile
// whose default_cwd it is, else the clone dir itself. Both are config paths.
func cloneDirWorkspace(c string, env ResolveEnv) string {
	for _, p := range env.Profiles {
		if p.CloneOf != "" && p.DefaultCwd != "" && foldEq(filepath.Clean(p.DefaultCwd), c, env.FoldCase) {
			return gitrepo.Normalize(p.CloneOf)
		}
	}
	return gitrepo.Normalize(c)
}

// within reports whether p is dir or lies under it, on a path boundary
// ("/a/b" does not contain "/a/bc"). Both must be clean.
func within(dir, p string, fold bool) bool {
	if fold {
		dir, p = asciiLower(dir), asciiLower(p)
	}
	if p == dir {
		return true
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	return strings.HasPrefix(p, dir)
}

func foldEq(a, b string, fold bool) bool {
	if fold {
		return asciiLower(a) == asciiLower(b)
	}
	return a == b
}

// asciiLower lowercases A-Z only, so byte offsets in the result are valid in the
// original (strings.ToLower can change a string's length).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
