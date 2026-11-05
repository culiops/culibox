package clone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/config"
)

// BoxOwned reports whether cwd is at or under any clone's dir, with the message
// a refused host-tier spawn shows (the caller wraps box.ErrBoxOwned). A clone
// belongs to its box, which can rewrite anything in it (its .git config, hooks,
// build files), so host tools must never run there (spec correction 11).
// Nothing else is box-owned: clone_of and the share sources are host trees the
// box can only read.
//
// Both paths are compared as spelled and resolved (symlinks followed even when
// dangling; a not-yet-existing tail joined on unchanged), so a symlink to the
// clone, a dangling link into it, or a path under it that does not exist yet is
// still inside. A path that cannot be resolved for any other reason (EACCES,
// ELOOP, ENOTDIR) is reported as owned: this check fails closed.
//
// An existing clone dir is also compared by identity with cwd and each of its
// existing ancestors: on a case-insensitive filesystem (macOS APFS by default)
// ~/Box-Clones/x is ~/box-clones/x, a spelling no string compare matches.
func BoxOwned(cwd string, clones []config.Clone) (string, bool) {
	return boxOwned(cwd, clones, os.Stat)
}

// boxOwned is BoxOwned with the stat it compares identities by.
func boxOwned(cwd string, clones []config.Clone, stat func(string) (fs.FileInfo, error)) (string, bool) {
	cwdSp, cwdErr := spellings(cwd)
	var ids []fs.FileInfo // cwd's existing ancestors, read once, on first need
	var idsErr error
	idsRead := false
	for _, c := range clones {
		if c.Dir == "" {
			continue
		}
		// Fail closed: a path that cannot be resolved may reach the clone through
		// a link nobody can read, so it is owned until proven otherwise.
		if cwdErr != nil {
			return cwd + " cannot be resolved (" + cwdErr.Error() + "); refusing a host session there in case it is inside the box-owned clone " +
				c.Dir + " (use agent-box clone fetch)", true
		}
		dirSp, err := spellings(c.Dir)
		if err != nil {
			return "the box-owned clone " + c.Dir + " cannot be resolved (" + err.Error() + "); refusing a host session at " +
				cwd + " in case it is inside it (use agent-box clone fetch)", true
		}
		inside := false
		for _, p := range cwdSp {
			for _, d := range dirSp {
				inside = inside || under(p, d)
			}
		}
		if !inside {
			if !idsRead {
				ids, idsErr = ancestorIDs(cwdSp[len(cwdSp)-1], stat)
				idsRead = true
			}
			if idsErr != nil {
				return cwd + " cannot be resolved (" + idsErr.Error() + "); refusing a host session there in case it is inside the box-owned clone " +
					c.Dir + " (use agent-box clone fetch)", true
			}
			id, err := stat(c.Dir)
			switch {
			case err == nil:
				for _, a := range ids {
					inside = inside || os.SameFile(a, id)
				}
			case !errors.Is(err, fs.ErrNotExist): // a clone not made yet has no identity
				return "the box-owned clone " + c.Dir + " cannot be resolved (" + err.Error() + "); refusing a host session at " +
					cwd + " in case it is inside it (use agent-box clone fetch)", true
			}
		}
		if inside {
			return cwd + " is inside the box-owned clone " + c.Dir +
				"; host tools must never run there (use agent-box clone fetch)", true
		}
	}
	return "", false
}

// ancestorIDs is the identity of p (absolute, symlinks resolved) and of each of
// its ancestors that exists. A missing one is skipped (a tail not created yet);
// any other stat failure is an error, so the caller fails closed.
func ancestorIDs(p string, stat func(string) (fs.FileInfo, error)) ([]fs.FileInfo, error) {
	var out []fs.FileInfo
	for {
		fi, err := stat(p)
		switch {
		case err == nil:
			out = append(out, fi)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return out, nil
		}
		p = parent
	}
}

// spellings is p cleaned, plus its resolved form when that differs.
func spellings(p string) ([]string, error) {
	clean := filepath.Clean(p)
	real, err := resolvePath(clean)
	if err != nil {
		return nil, err
	}
	if real != clean {
		return []string{clean, real}, nil
	}
	return []string{clean}, nil
}

// maxLinkHops bounds symlink resolution, as the kernel's MAXSYMLINKS does.
const maxLinkHops = 40

// resolvePath resolves every symlink on p component by component, Readlink-ing
// even a dangling one, so a link whose target does not exist yet still resolves
// to where it points. Only a missing component ends the walk: the rest of the
// path is joined on unchanged (nothing absent can be a link). Any other failure
// (EACCES, ELOOP, a component that is not a directory, too many hops) is an
// error: the caller must not guess where the path leads.
func resolvePath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	todo := strings.Split(p, "/")
	cur, hops := "/", 0
	for len(todo) > 0 {
		part := todo[0]
		todo = todo[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		st, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(append([]string{next}, todo...)...), nil
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			if hops++; hops > maxLinkHops {
				return "", fmt.Errorf("%s: too many levels of symbolic links", p)
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				cur = "/"
			}
			todo = append(strings.Split(target, "/"), todo...)
			continue
		}
		if !st.IsDir() && len(todo) > 0 {
			return "", fmt.Errorf("%s: %s is not a directory", p, next)
		}
		cur = next
	}
	return cur, nil
}

// under reports whether p is parent or below it, by whole components. Both
// must be clean.
func under(p, parent string) bool {
	if p == parent || parent == "/" {
		return true
	}
	return strings.HasPrefix(p, parent+"/")
}
