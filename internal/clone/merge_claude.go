package clone

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/culiops/culibox/internal/bundle"
)

// Claude Code reads the files a CLAUDE.md @-imports as it reads the CLAUDE.md
// itself, so a box commit that changes one is as trusted by the host's agent as
// a change to CLAUDE.md, which the merge refuses (#370). This file is the
// Claude-specific part of the merge guard; merge.go reaches it only through
// agentImportDenies. importTargets finds them the way Claude Code resolves them, from the host's side only: the start
// files and every file read are the work tree before the merge, which the box
// has not written.

const (
	mergeImportHops  = 5       // Claude Code's import depth
	mergeImportFiles = 200     // files read per check
	mergeImportBytes = 1 << 20 // read per file; an import past it is not seen
)

// agentImportDenies returns whether a repo-relative path from the range is at,
// under or a parent of a file a host CLAUDE.md imports (importTargets). The
// error is the ls-files call's; the merge then does not happen.
func (m *merger) agentImportDenies(home string) (func(p string) bool, error) {
	targets, err := m.importTargets(home)
	if err != nil {
		return nil, err
	}
	return func(p string) bool { return importDenied(p, targets) }, nil
}

// importTargets returns the repo-relative (slash-separated) paths of every
// file inside m.of that a host CLAUDE.md imports, directly or through up to
// mergeImportHops imports. The start files are each CLAUDE.md and
// CLAUDE.local.md git tracks at any depth (any case), an untracked
// CLAUDE.local.md at the top, and <home>/.claude/CLAUDE.md when home is set. A
// spelling "~/X" is X under home, an absolute one is itself, any other is
// relative to the importing file's directory. A target counts whether or not it
// exists. A file that cannot be read is skipped, and parsing never fails: only
// the ls-files call can.
func (m *merger) importTargets(home string) ([]string, error) {
	out, err := m.run(m.env(), "ls-files", "-z", "--", ":(icase,glob)**/CLAUDE.md", ":(icase,glob)**/CLAUDE.local.md")
	if err != nil {
		return nil, err
	}
	w := &importGuard{of: filepath.Clean(m.of), home: home, seen: map[string]int{}, found: map[string]bool{}}
	if root, err := os.OpenRoot(w.of); err == nil {
		w.root = root
		defer root.Close()
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			w.walk(filepath.Join(w.of, filepath.FromSlash(rel)), 0)
		}
	}
	w.walk(filepath.Join(w.of, "CLAUDE.local.md"), 0)
	if home != "" {
		w.walk(filepath.Join(home, ".claude", "CLAUDE.md"), 0)
	}
	return w.targets, nil
}

type importGuard struct {
	of, home string
	root     *os.Root       // of; nil when it cannot be opened (files in it are then read by path)
	seen     map[string]int // file -> the lowest hop it was walked at
	reads    int
	found    map[string]bool
	targets  []string
}

// walk reads file, at hop hops from a start file, and records its imports.
func (w *importGuard) walk(file string, hop int) {
	if h, ok := w.seen[file]; (ok && h <= hop) || w.reads >= mergeImportFiles {
		return
	}
	w.seen[file] = hop
	w.reads++
	md, err := w.read(file)
	if err != nil {
		return
	}
	for _, sp := range bundle.ParseImports(md) {
		t := w.resolve(sp, filepath.Dir(file))
		if t == "" {
			continue
		}
		if rel := absInside(w.of, t); rel != "" && rel != "." && !w.found[rel] {
			w.found[rel] = true
			w.targets = append(w.targets, rel)
		}
		if hop+1 < mergeImportHops {
			w.walk(t, hop+1)
		}
	}
}

// resolve maps an import spelling to a clean absolute path, "" when it names
// nothing this check can resolve (a ~ path with no home, another user's ~).
func (w *importGuard) resolve(sp, dir string) string {
	switch {
	case strings.HasPrefix(sp, "~/"):
		if w.home == "" {
			return ""
		}
		return filepath.Join(w.home, sp[2:])
	case strings.HasPrefix(sp, "~"):
		return ""
	case filepath.IsAbs(sp):
		return filepath.Clean(sp)
	}
	return filepath.Join(dir, sp)
}

// read returns up to mergeImportBytes of file when it is a regular file: in
// the repo through w.root (no link out of it is followed), elsewhere by path.
// The open never blocks on a FIFO.
func (w *importGuard) read(file string) ([]byte, error) {
	var f *os.File
	var err error
	if rel := relInside(w.of, file); rel != "" && w.root != nil {
		f, err = w.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	} else {
		f, err = os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(io.LimitReader(f, mergeImportBytes))
}

// importDenied reports whether p (a repo-relative path from the range) is at,
// under or a parent of one of targets: a symlink at a parent re-points the
// imported file. Case-insensitive, like mergeDenied.
func importDenied(p string, targets []string) bool {
	pc := strings.Split(p, "/")
	for _, t := range targets {
		tc := strings.Split(t, "/")
		if foldPrefix(pc, tc) || foldPrefix(tc, pc) {
			return true
		}
	}
	return false
}
