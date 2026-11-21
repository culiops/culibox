package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/culiops/culibox/internal/wsroots"
)

// BoxTrees is the box-writable host trees the fs API must not trust.
type BoxTrees struct {
	Denied    []string // refused outright: the data dir, the box state root
	Contained []string // boxed workspace roots; served only through os.Root
}

// fsTarget is where an fs request lands. A host target (Strict false) is fully
// resolved below a served root, as the old EvalSymlinks check did. A strict target is inside a
// boxed workspace: Rel is the UNRESOLVED remainder and must only be opened
// through os.Root(Root), which refuses any box-planted link that leaves it.
type fsTarget struct {
	Root   string // resolved dir the os.Root opens (the served root for host paths, W for strict)
	Rel    string // below Root, "." for Root; host: fully resolved; strict: unresolved remainder
	Abs    string // filepath.Join(Root, Rel), returned to the client
	Strict bool
	// ReadOnly is set once the walk touched a scratch root (the folder itself, a
	// path under it, or a link through it), wherever links led afterwards. Every
	// mutation refuses it, so a scratch path can never be used to write — not
	// into the scratch folder and not into $HOME through a link in it.
	ReadOnly bool
}

// fileKey is a directory's identity. Tree membership is decided by identity, not
// by path string: on macOS (APFS, case- and normalization-insensitive by default)
// two different strings can name the same directory, and a prefix check would let
// one of them past.
type fileKey struct {
	dev uint64
	ino uint64
}

func keyOf(fi os.FileInfo) (fileKey, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, fmt.Errorf("no stat identity for %s", fi.Name())
	}
	// Dev is int32 on darwin and uint64 on linux.
	return fileKey{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// statKey is the identity of path after following links; ok is false when the
// path cannot be stat'ed.
func statKey(path string) (fileKey, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileKey{}, false
	}
	k, err := keyOf(fi)
	return k, err == nil
}

// fsPolicy is a snapshot of the boxed trees, keyed by identity, for one request
// (or one batch, see handleFsSearch). It is not the boundary for strict paths —
// os.Root is — so a box racing the snapshot gains nothing.
type fsPolicy struct {
	// roots is every served root, resolved, home first. Anything outside all of
	// them (and the scratch folders) is refused; see rootOf.
	roots     []string
	denied    map[fileKey]struct{}
	contained map[fileKey]struct{}
	// scratch is the accepted Claude Code temp folders (claude-<uid>), identity
	// to resolved path. Readable, never writable.
	scratch map[fileKey]string
	// guarded is every kept denied or contained root plus all of its ancestors.
	// Removing or renaming one of those directories would change what the policy
	// sees (a renamed workspace is no longer contained, a renamed data dir no
	// longer denied), so rename and delete refuse them; see guardsTree.
	guarded map[fileKey]struct{}
	// deniedOrAbove is every denied root plus all of its ancestors, by
	// identity: a directory in it would carry a denied tree along if it were
	// handed out whole (the git status mounts, see gitapi.go).
	deniedOrAbove map[fileKey]struct{}
}

// newFsPolicy builds the policy for home, the extra workspace roots and trees.
// An extra root is dropped when it does not resolve, is not a directory, equals
// home, or wsroots.Refusal names a reason (the file API never serves what the
// workspace picker would refuse). Entries of trees that do not exist are
// skipped. A contained entry that is $HOME, an ancestor of $HOME, or that holds a
// denied tree (or is one) is ignored: treating it as a workspace root would make
// everything under it strict, or would serve the denied tree through os.Root.
//
// scratch lists candidate Claude Code temp folders; each is checked here, per
// request, because Claude Code creates the folder lazily after the server
// started. One is kept only if scratchRoot accepts it and it is not $HOME, not
// inside $HOME (already served), not an ancestor of $HOME or of a denied tree.
func newFsPolicy(home string, roots []string, trees BoxTrees, scratch ...string) (fsPolicy, error) {
	rhome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fsPolicy{}, err
	}
	p := fsPolicy{roots: []string{rhome}, denied: map[fileKey]struct{}{}, contained: map[fileKey]struct{}{},
		scratch: map[fileKey]string{}, guarded: map[fileKey]struct{}{}, deniedOrAbove: map[fileKey]struct{}{}}

	forbidden := map[fileKey]struct{}{}
	addSelfAndAncestors := func(dir string) {
		for {
			if k, ok := statKey(dir); ok {
				forbidden[k] = struct{}{}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
	addSelfAndAncestors(rhome)
	for _, r := range roots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(rr); err != nil || !fi.IsDir() {
			continue
		}
		if wsroots.Refusal(rr, wsroots.Env{Home: rhome}) != "" {
			continue
		}
		// Home already serves everything under it, so such a root adds no reach.
		if rr == rhome || strings.HasPrefix(rr, rhome+string(os.PathSeparator)) {
			continue
		}
		if p.hasRoot(rr) {
			continue
		}
		// Contained trees are unchanged under every root: a box workspace that
		// is, holds, or sits in an extra root stays contained (and guarded), so
		// the walk never follows its box-planted links in host mode.
		p.roots = append(p.roots, rr)
	}
	for _, d := range trees.Denied {
		rd, err := filepath.EvalSymlinks(d)
		if err != nil {
			continue
		}
		k, ok := statKey(rd)
		if !ok {
			continue
		}
		p.denied[k] = struct{}{}
		addSelfAndAncestors(rd)
		addGuarded(p.guarded, rd)
		addGuarded(p.deniedOrAbove, rd)
	}
	for _, c := range trees.Contained {
		fi, err := os.Stat(c)
		if err != nil || !fi.IsDir() {
			continue
		}
		k, err := keyOf(fi)
		if err != nil {
			continue
		}
		if _, bad := forbidden[k]; bad {
			continue
		}
		if _, bad := p.denied[k]; bad {
			continue
		}
		p.contained[k] = struct{}{}
		// Ancestors are walked on the resolved path: the lexical parents of a
		// path that crosses a link are not the directory's real ancestors.
		rc, err := filepath.EvalSymlinks(c)
		if err != nil {
			rc = c
		}
		addGuarded(p.guarded, rc)
	}
	for _, c := range scratch {
		real, k, ok := scratchRoot(c)
		if !ok {
			continue
		}
		if _, bad := forbidden[k]; bad {
			continue
		}
		if _, bad := p.denied[k]; bad {
			continue
		}
		if _, served := p.rootOf(real); served {
			continue
		}
		p.scratch[k] = real
		addGuarded(p.guarded, real)
	}
	return p, nil
}

// addGuarded adds dir and every ancestor of dir up to "/" to set, by identity.
func addGuarded(set map[fileKey]struct{}, dir string) {
	for {
		if k, ok := statKey(dir); ok {
			set[k] = struct{}{}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// guardsTree reports whether fi (a directory's Lstat info) is a denied or
// contained root, or holds one. A symlink's Lstat info is the link's own, never
// its target's, so a link pointing at a guarded tree is not guarded.
func (p fsPolicy) guardsTree(fi os.FileInfo) bool {
	if !fi.IsDir() {
		return false
	}
	k, err := keyOf(fi)
	if err != nil {
		return false
	}
	_, ok := p.guarded[k]
	return ok
}

// treeOf reports whether fi (a directory's info) is a denied tree or a boxed
// workspace root, by identity. A walk that starts outside the boxed trees uses it
// to notice when it is about to enter one.
func (p fsPolicy) treeOf(fi os.FileInfo) (denied, contained bool) {
	k, err := keyOf(fi)
	if err != nil {
		return false, false
	}
	_, denied = p.denied[k]
	_, contained = p.contained[k]
	return denied, contained
}

// fsPolicy snapshots the server's boxed trees. A nil BoxTrees means none: every
// path resolves as a host path.
func (s *Server) fsPolicy() (fsPolicy, error) {
	var trees BoxTrees
	if s.BoxTrees != nil {
		trees = s.BoxTrees()
	}
	var roots []string
	if s.Roots != nil {
		roots = s.Roots.Paths()
	}
	return newFsPolicy(s.Home, roots, trees, s.ScratchDirs...)
}

// resolveFs resolves one request path against the server's current boxed trees.
func (s *Server) resolveFs(req string) (fsTarget, error) {
	p, err := s.fsPolicy()
	if err != nil {
		return fsTarget{}, err
	}
	return p.resolve(req)
}

// maxLinkHops matches the kernel's symlink limit (MAXSYMLINKS).
const maxLinkHops = 40

// resolve walks the clean absolute req one component at a time with its own
// Lstat/Readlink loop instead of EvalSymlinks, because EvalSymlinks would follow a
// box-planted link out of a box-writable tree to any host file it names.
//
// Outside the boxed trees, links are host-owned and followed as before, and the
// final path must be under a served root ($HOME or a workspace root) or a scratch
// root (served ReadOnly). The moment the walk stands on a denied tree it
// fails with errDenied. The moment it stands on a contained root W — whether
// the request named W or a host link's target led into it — it stops: whatever
// is left, including components still pending from that link's target, is
// returned unresolved as Rel for os.Root(W) to resolve. The first contained root
// met is the outermost one, so nested roots are opened through the outer root.
//
// Errors: errBadPath (empty, relative, too many links), os.ErrNotExist (a missing
// component, or a non-directory with components after it), errDenied (a denied
// tree), errOutsideHome (outside every served root and every scratch root, or a remainder
// that climbs out of W).
func (p fsPolicy) resolve(req string) (fsTarget, error) {
	if req == "" || !filepath.IsAbs(req) {
		return fsTarget{}, errBadPath
	}
	clean := filepath.Clean(req)

	cur := "/"
	curKey, err := lstatKey(cur)
	if err != nil {
		return fsTarget{}, err
	}
	pending := strings.Split(clean[1:], "/")
	hops := 0
	readOnly := false
	// passed holds the scratch roots the walk stood on, by identity. Only
	// those may claim the final path: a path STRING under a root proves
	// nothing if another directory was moved into that path after the snapshot.
	var passed map[string]bool
	for {
		if _, ok := p.denied[curKey]; ok {
			return fsTarget{}, errDenied
		}
		if root, ok := p.scratch[curKey]; ok {
			readOnly = true
			if passed == nil {
				passed = map[string]bool{}
			}
			passed[root] = true
		}
		if _, ok := p.contained[curKey]; ok {
			rel := filepath.Clean(filepath.Join(pending...))
			if rel != "." && !filepath.IsLocal(rel) {
				return fsTarget{}, errOutsideHome
			}
			// W is served when it is under a root, or when the target inside it
			// is (a root nested in W, W being only an ancestor of that root).
			// The target stays Strict under W either way.
			_, inRoot := p.rootOf(cur)
			if !inRoot {
				_, inRoot = p.rootOf(filepath.Join(cur, rel))
			}
			if !inRoot {
				if s, inScratch := p.underScratch(cur); !inScratch || !passed[s] {
					return fsTarget{}, errOutsideHome
				}
			}
			return fsTarget{Root: cur, Rel: rel, Abs: filepath.Join(cur, rel), Strict: true, ReadOnly: readOnly}, nil
		}
		if len(pending) == 0 {
			break
		}
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			// cur never contains a link, so its lexical parent is its real parent.
			cur = filepath.Dir(cur)
			if curKey, err = lstatKey(cur); err != nil {
				return fsTarget{}, err
			}
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fsTarget{}, os.ErrNotExist
			}
			return fsTarget{}, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxLinkHops {
				return fsTarget{}, errBadPath
			}
			t, err := os.Readlink(next)
			if err != nil {
				return fsTarget{}, err
			}
			pending = append(strings.Split(t, "/"), pending...)
			if filepath.IsAbs(t) {
				cur = "/"
				if curKey, err = lstatKey(cur); err != nil {
					return fsTarget{}, err
				}
			}
			continue
		}
		if !fi.IsDir() && len(pending) > 0 {
			return fsTarget{}, os.ErrNotExist
		}
		cur = next
		if curKey, err = keyOf(fi); err != nil {
			return fsTarget{}, err
		}
	}
	root, inRoot := p.rootOf(cur)
	if !inRoot {
		s, ok := p.underScratch(cur)
		if !ok || !passed[s] {
			return fsTarget{}, errOutsideHome
		}
		root, readOnly = s, true
	}
	rel, err := filepath.Rel(root, cur)
	if err != nil {
		return fsTarget{}, errOutsideHome
	}
	return fsTarget{Root: root, Rel: rel, Abs: cur, Strict: false, ReadOnly: readOnly}, nil
}

// rootOf returns the served root holding path (the root itself counts), the
// longest one when roots nest, matched by whole components. path must be clean
// and resolved.
func (p fsPolicy) rootOf(path string) (string, bool) {
	best, found := "", false
	for _, r := range p.roots {
		if path == r || strings.HasPrefix(path, r+string(os.PathSeparator)) {
			if !found || len(r) > len(best) {
				best, found = r, true
			}
		}
	}
	return best, found
}

// hasRoot reports whether r is already a served root, exactly.
func (p fsPolicy) hasRoot(r string) bool {
	for _, x := range p.roots {
		if x == r {
			return true
		}
	}
	return false
}

func lstatKey(path string) (fileKey, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return fileKey{}, err
	}
	return keyOf(fi)
}
