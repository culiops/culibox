package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// BoxTrees is the box-writable host trees the fs API must not trust.
type BoxTrees struct {
	Denied    []string // refused outright: the data dir, the box state root
	Contained []string // boxed workspace roots; served only through os.Root
}

// fsTarget is where an fs request lands. A host target (Strict false) is fully
// resolved below $HOME, as containedPath always did. A strict target is inside a
// boxed workspace: Rel is the UNRESOLVED remainder and must only be opened
// through os.Root(Root), which refuses any box-planted link that leaves it.
type fsTarget struct {
	Root   string // resolved dir the os.Root opens ($HOME for host paths, W for strict)
	Rel    string // below Root, "." for Root; host: fully resolved; strict: unresolved remainder
	Abs    string // filepath.Join(Root, Rel), returned to the client
	Strict bool
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
	rhome     string
	denied    map[fileKey]struct{}
	contained map[fileKey]struct{}
}

// newFsPolicy builds the policy for home and trees. Entries that do not exist are
// skipped. A contained entry that is $HOME, an ancestor of $HOME, or that holds a
// denied tree (or is one) is ignored: treating it as a workspace root would make
// everything under it strict, or would serve the denied tree through os.Root.
func newFsPolicy(home string, trees BoxTrees) (fsPolicy, error) {
	rhome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fsPolicy{}, err
	}
	p := fsPolicy{rhome: rhome, denied: map[fileKey]struct{}{}, contained: map[fileKey]struct{}{}}

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
	}
	return p, nil
}

// fsPolicy snapshots the server's boxed trees. A nil BoxTrees means none: every
// path resolves as a host path.
func (s *Server) fsPolicy() (fsPolicy, error) {
	var trees BoxTrees
	if s.BoxTrees != nil {
		trees = s.BoxTrees()
	}
	return newFsPolicy(s.Home, trees)
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
// final path must be under $HOME. The moment the walk stands on a denied tree it
// fails with errOutsideHome. The moment it stands on a contained root W — whether
// the request named W or a host link's target led into it — it stops: whatever
// is left, including components still pending from that link's target, is
// returned unresolved as Rel for os.Root(W) to resolve. The first contained root
// met is the outermost one, so nested roots are opened through the outer root.
//
// Errors: errBadPath (empty, relative, too many links), os.ErrNotExist (a missing
// component, or a non-directory with components after it), errOutsideHome
// (outside $HOME, denied, or a remainder that climbs out of W).
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
	for {
		if _, ok := p.denied[curKey]; ok {
			return fsTarget{}, errOutsideHome
		}
		if _, ok := p.contained[curKey]; ok {
			rel := filepath.Clean(filepath.Join(pending...))
			if rel != "." && !filepath.IsLocal(rel) {
				return fsTarget{}, errOutsideHome
			}
			if !p.underHome(cur) {
				return fsTarget{}, errOutsideHome
			}
			return fsTarget{Root: cur, Rel: rel, Abs: filepath.Join(cur, rel), Strict: true}, nil
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
	if !p.underHome(cur) {
		return fsTarget{}, errOutsideHome
	}
	rel, err := filepath.Rel(p.rhome, cur)
	if err != nil {
		return fsTarget{}, errOutsideHome
	}
	return fsTarget{Root: p.rhome, Rel: rel, Abs: cur, Strict: false}, nil
}

func (p fsPolicy) underHome(path string) bool {
	return path == p.rhome || strings.HasPrefix(path, p.rhome+string(os.PathSeparator))
}

func lstatKey(path string) (fileKey, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return fileKey{}, err
	}
	return keyOf(fi)
}
