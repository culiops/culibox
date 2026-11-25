package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// maxEstimateEntries and estimateBudget bound the walk of <repo>/.git: past
	// either the sum so far is returned as a lower bound (Partial).
	maxEstimateEntries = 200_000
	estimateBudget     = 5 * time.Second
	// copyHeadroom is the space kept free on top of the copy's own size.
	copyHeadroom = 512 << 20
	// maxGitAttributes bounds the .gitattributes read for the LFS hint.
	maxGitAttributes = 64 << 10
)

// Estimate is what a private copy of a repo would take, and what the disk has.
type Estimate struct {
	// GitBytes is the size of the repo's .git (a lower bound when Partial).
	GitBytes int64 `json:"git_bytes"`
	// TreeBytes is the size of HEAD's files: what the copy's work tree holds.
	TreeBytes int64 `json:"tree_bytes"`
	// FreeBytes is the free space where the copy would go.
	FreeBytes int64 `json:"free_bytes"`
	// Enough is whether FreeBytes holds the copy with a margin (see enoughFor).
	Enough bool `json:"enough"`
	// Partial says the .git walk stopped at its bounds: the sizes are a lower
	// bound.
	Partial bool `json:"partial"`
	// LFS says the repo's top-level .gitattributes names filter=lfs: the copy
	// holds pointers, and the objects may need more space.
	LFS bool `json:"lfs"`
}

// enoughFor is the free-space rule: the copy (git plus tree) with 10% on top and
// 512 MiB kept free.
func enoughFor(git, tree, free int64) bool {
	return free >= (git+tree)*11/10+copyHeadroom
}

// EstimateCopy sizes a private copy of the repo of and checks the free space
// where dir would be made. of must already have passed the caller's host-owned
// check. It reads nothing outside of's .git and HEAD tree, follows no symlink,
// and is bounded.
//
// g nil means host git must not run in of (the caller's boxed-workspace record
// says a box ran there, #371): the tree size is then unknown, and the estimate
// is the .git walk alone, Partial. Git is not run either when of's config
// declares a promisor remote (a partial clone): listing HEAD would need the
// objects it lacks. Otherwise git runs in of, hardened, with the clean
// environment, neither of which lets it fetch.
func EstimateCopy(ctx context.Context, g Git, of, dir string) (Estimate, error) {
	var e Estimate
	e.GitBytes, e.Partial = walkGitBytes(ctx, filepath.Join(of, ".git"), maxEstimateEntries, estimateBudget)
	if g == nil || declaresPromisor(of) {
		e.Partial = true
	} else {
		tree, partial, err := treeBytes(ctx, g, of)
		if err != nil {
			return Estimate{}, err
		}
		e.TreeBytes = tree
		e.Partial = e.Partial || partial
	}
	var err error
	if e.FreeBytes, err = freeBytes(dir); err != nil {
		return Estimate{}, err
	}
	e.Enough = enoughFor(e.GitBytes, e.TreeBytes, e.FreeBytes)
	e.LFS = usesLFS(of)
	return e, nil
}

// FreeFor is the free-space half of an estimate, for sizes an earlier
// EstimateCopy of the same repo measured: the copy dialog asks only this when
// the copy location is edited (#371 C4). Nothing in the repo is read. The sizes
// come back as given; Partial and LFS are the earlier estimate's to keep.
func FreeFor(dir string, gitBytes, treeBytes int64) (Estimate, error) {
	free, err := freeBytes(dir)
	if err != nil {
		return Estimate{}, err
	}
	return Estimate{GitBytes: gitBytes, TreeBytes: treeBytes, FreeBytes: free, Enough: enoughFor(gitBytes, treeBytes, free)}, nil
}

// walkGitBytes sums the sizes of the regular files under root. Symlinks are
// never followed (and never counted), and anything unreadable is skipped. It
// stops after maxEntries entries or the budget and then reports partial.
// root/lfs, Git LFS's object store, is not counted: a clone does not copy it
// (its files arrive as pointers).
func walkGitBytes(ctx context.Context, root string, maxEntries int, budget time.Duration) (sum int64, partial bool) {
	deadline := time.Now().Add(budget)
	n := 0
	stop := errors.New("stop")
	lfs := filepath.Join(root, "lfs")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			partial = true // unreadable: what is below is not counted
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == lfs && d.IsDir() {
			return fs.SkipDir
		}
		if n++; n > maxEntries || (n%256 == 0 && (time.Now().After(deadline) || ctx.Err() != nil)) {
			partial = true
			return stop
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				sum += fi.Size()
			} else {
				partial = true
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		partial = true
	}
	return sum, partial
}

// treeBytes is the sum of the blob sizes of HEAD's tree. A repo with no commit
// has no tree: 0. A submodule entry (size "-") counts nothing. partial says a
// blob's size could not be read (git prints "BAD" for an object the repo
// lacks, and still exits 0): the sum is then a lower bound.
func treeBytes(ctx context.Context, g Git, of string) (sum int64, partial bool, err error) {
	if _, err := g.Run(ctx, of, CleanEnv(), hardened("rev-parse", "--verify", "--quiet", "HEAD")...); err != nil {
		if code, ok := exitCode(err); ok && code == 1 {
			return 0, false, nil // --quiet: exit 1 and no message is "no such commit"
		}
		return 0, false, fmt.Errorf("reading HEAD of %s: %w", of, err)
	}
	args := hardened("ls-tree", "-r", "-l", "--full-tree", "HEAD")
	var s lsTreeSum
	if sg, ok := g.(StreamGit); ok {
		// A big repo lists more than Run's 16 MiB cap; the lines are summed as
		// they come.
		if err := sg.Stream(ctx, of, CleanEnv(), nil, &s, args...); err != nil {
			return 0, false, fmt.Errorf("listing HEAD of %s: %w", of, err)
		}
	} else {
		out, err := g.Run(ctx, of, CleanEnv(), args...)
		if err != nil {
			return 0, false, fmt.Errorf("listing HEAD of %s: %w", of, err)
		}
		s.Write(out)
	}
	s.flush()
	return s.total, s.bad, nil
}

// lsTreeSum adds up the size column of `ls-tree -r -l` lines
// (`<mode> <type> <hash> <size>\t<path>`) as they are written to it. bad is
// set by a size that is neither a number nor "-" (a submodule).
type lsTreeSum struct {
	total int64
	bad   bool
	rest  []byte // the unfinished last line
}

func (s *lsTreeSum) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(s.rest) < 8<<10 { // a line is a path plus ~60 bytes
				s.rest = append(s.rest, p...)
			}
			break
		}
		line := p[:i]
		if len(s.rest) > 0 {
			line = append(s.rest, line...)
			s.rest = nil
		}
		s.line(line)
		p = p[i+1:]
	}
	return n, nil
}

func (s *lsTreeSum) flush() {
	if len(s.rest) > 0 {
		s.line(s.rest)
		s.rest = nil
	}
}

func (s *lsTreeSum) line(l []byte) {
	meta, _, ok := bytes.Cut(l, []byte{'\t'})
	if !ok {
		return
	}
	f := strings.Fields(string(meta))
	if len(f) != 4 {
		return
	}
	n, err := strconv.ParseInt(f[3], 10, 64)
	switch {
	case err == nil && n > 0:
		s.total += n
	case err != nil && f[3] != "-":
		s.bad = true
	}
}

// declaresPromisor is whether of's .git/config may declare a promisor remote
// (extensions.partialClone, remote.<name>.promisor), read as a file through
// os.Root with the box-file discipline, never through git. It errs towards yes:
// the word anywhere in the file counts, and a config that is not a plain,
// bounded, readable file (a symlink, a FIFO) is a yes. Only an absent one is a
// no, left for git to report. A config an include pulls in is not read: the
// hardened flags and the clean environment are what keep git from fetching.
func declaresPromisor(of string) bool {
	root, err := os.OpenRoot(of)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	defer root.Close()
	data, err := readBoxFile(root, ".git/config", maxGitConfig)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	low := strings.ToLower(string(data))
	return strings.Contains(low, "partialclone") || strings.Contains(low, "promisor")
}

// usesLFS is whether the top-level .gitattributes of repo names filter=lfs. A
// hint for the user: a symlink, a file that is not regular, one over the bound
// or anything unreadable is "no".
func usesLFS(repo string) bool {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return false
	}
	defer root.Close()
	data, err := readBoxFile(root, ".gitattributes", maxGitAttributes)
	return err == nil && strings.Contains(string(data), "filter=lfs")
}
