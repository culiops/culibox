package clone

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// A protected name that is a symbolic link in the host's work tree sends the
// host's tools to another file: a CLAUDE.md that links to docs/agent.md is the
// agent's instructions there, a .claude that links to tooling/claude holds its
// settings, a hooks dir behind a linked parent is another folder (#370). The
// guard compares the spelled path of a changed file, so it would let a box
// change the file the link points to. This file builds the concrete set S of
// protected paths from the host's work tree, follows each one component by
// component inside the repo, and returns every in-repo path a protected name
// really leads to, which the merge then refuses like the name itself.
//
// Reads the host's work tree only (the side the box has not written), and only
// adds refusals. It fails closed: a loop, a chain longer than the kernel
// follows, an unexpected Lstat error or a repo too big for the caps below
// refuses the merge.

var (
	mergeLinkSetMax   = 4096  // protected paths followed per check
	mergeLinkLstatMax = 16384 // Lstat calls per check
)

const (
	mergeLinkHops = 40   // symbolic links followed per path, as the kernel's ELOOP limit
	mergeLinkTop  = 4096 // entries read of the repo top
)

var (
	errLinkLoop = errors.New("it follows more than 40 symbolic links, or loops")
	errLinkCap  = errors.New("too many protected paths or files to check for symbolic links")
)

// ---- the resolver

type linkKind int

const (
	linkMissing linkKind = iota // nothing there (or below a file)
	linkSymlink
	linkOther // a directory, a file, anything else
)

// linkWalker resolves repo-relative paths through the host's work tree. Every
// path it asks the OS about is made of components it has already found not to
// be links, so a link is never followed by the OS, only by the walker.
type linkWalker struct {
	of     string
	root   *os.Root
	memo   map[string]linkKind
	lstats int
	links  []string // the links met, repo-relative, in the order met
	seen   map[string]bool
}

func newLinkWalker(of string) (*linkWalker, error) {
	of = filepath.Clean(of)
	root, err := os.OpenRoot(of)
	if err != nil {
		return nil, err
	}
	return &linkWalker{of: of, root: root, memo: map[string]linkKind{}, seen: map[string]bool{}}, nil
}

func (w *linkWalker) close() { w.root.Close() }

// linkPaths returns the links met so far.
func (w *linkWalker) linkPaths() []string { return w.links }

// kind classifies the repo-relative path p, whose parents are not links.
func (w *linkWalker) kind(p string) (linkKind, error) {
	if k, ok := w.memo[p]; ok {
		return k, nil
	}
	if w.lstats >= mergeLinkLstatMax {
		return 0, errLinkCap
	}
	w.lstats++
	fi, err := w.root.Lstat(filepath.FromSlash(p))
	k := linkOther
	switch {
	case err == nil:
		if fi.Mode()&fs.ModeSymlink != 0 {
			k = linkSymlink
		}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		k = linkMissing
	default:
		return 0, err
	}
	w.memo[p] = k
	return k, nil
}

// resolve follows the repo-relative spelling s one component at a time: a
// symbolic link is read and the walk goes on from its target (at most
// mergeLinkHops), a missing tail is kept as spelled. It returns the components
// of the real repo-relative path (none for the repo top), or outside when the
// walk leaves the repo. The links met on the way, in the repo, are in
// linkPaths whatever the end. An error is errLinkLoop, errLinkCap or the Lstat
// error.
func (w *linkWalker) resolve(s string) (real []string, outside bool, err error) {
	todo := reverse(strings.Split(s, "/"))
	var cur []string
	hops := 0
	missing := false
	for len(todo) > 0 {
		c := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return nil, true, nil
			}
			cur = cur[:len(cur)-1]
			continue
		}
		if missing {
			cur = append(cur, c)
			continue
		}
		p := strings.Join(append(slices.Clone(cur), c), "/")
		k, err := w.kind(p)
		if err != nil {
			return nil, false, err
		}
		switch k {
		case linkMissing:
			missing = true
			cur = append(cur, c)
		case linkOther:
			cur = append(cur, c)
		case linkSymlink:
			if hops++; hops > mergeLinkHops {
				return nil, false, errLinkLoop
			}
			w.addLink(p)
			t, err := w.root.Readlink(filepath.FromSlash(p))
			if err != nil {
				return nil, false, err
			}
			t = filepath.ToSlash(t)
			if filepath.IsAbs(filepath.FromSlash(t)) {
				rel := absInside(w.of, filepath.FromSlash(t))
				if rel == "" {
					return nil, true, nil
				}
				cur, t = nil, rel
			}
			todo = append(todo, reverse(strings.Split(t, "/"))...)
		}
	}
	return cur, false, nil
}

func (w *linkWalker) addLink(p string) {
	if !w.seen[p] {
		w.seen[p] = true
		w.links = append(w.links, p)
	}
}

// top returns the names in the repo top that are symbolic links.
func (w *linkWalker) top() ([]string, error) {
	d, err := w.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	ents, err := d.ReadDir(mergeLinkTop + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(ents) > mergeLinkTop {
		return nil, errLinkCap
	}
	var out []string
	for _, e := range ents {
		if e.Type()&fs.ModeSymlink != 0 {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func reverse(s []string) []string {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

// ---- what the links deny

// linkDenies is the set of repo paths the protected names really lead to.
type linkDenies struct {
	paths [][]string // components
	keys  map[string]bool
	all   bool // a protected name is a link to the repo top
}

func newLinkDenies() *linkDenies { return &linkDenies{keys: map[string]bool{}} }

// add denies the repo-relative path p (and what is under or above it); "" is
// the repo top, so everything.
func (d *linkDenies) add(p string) {
	if p == "" || p == "." {
		d.all = true
		return
	}
	if k := foldKey(p); !d.keys[k] {
		d.keys[k] = true
		d.paths = append(d.paths, strings.Split(p, "/"))
	}
}

// denied reports whether p is at, under or a parent of a denied path,
// case-folded like mergeDenied.
func (d *linkDenies) denied(p string) bool {
	if d.all {
		return true
	}
	pc := strings.Split(p, "/")
	for _, tc := range d.paths {
		if foldPrefix(pc, tc) || foldPrefix(tc, pc) {
			return true
		}
	}
	return false
}

// ---- the protected set S

// lsFilesEntry is one line of `ls-files -s -z`.
type lsFilesEntry struct{ mode, path string }

// parseLsFilesStage reads `ls-files -s -z` output: "<mode> <id> <stage>\t<path>"
// per NUL-terminated record. Anything else is an error, so a cut-off list never
// reads as a shorter one.
func parseLsFilesStage(out []byte) ([]lsFilesEntry, error) {
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("the file list is cut short")
	}
	var es []lsFilesEntry
	for _, rec := range strings.Split(string(out[:len(out)-1]), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || p == "" || len(f) != 3 || !octalMode(f[0]) {
			return nil, errors.New("unexpected entry in the file list")
		}
		es = append(es, lsFilesEntry{mode: f[0], path: p})
	}
	return es, nil
}

// protectedPathspecs are the pathspecs that list the tracked files the guard
// protects by name, so the listing never walks the whole repo.
func protectedPathspecs() []string {
	ps := []string{":(icase,glob)**/.claude", ":(icase,glob)**/.claude/**"}
	for _, n := range MergeDenyNames {
		ps = append(ps, ":(icase,glob)**/"+n)
	}
	for _, pat := range MergeDenyPatterns {
		ps = append(ps, ":(icase,glob)**/"+pat)
	}
	for _, d := range MergeDenyTop {
		ps = append(ps, ":(icase,glob)"+d, ":(icase,glob)"+d+"/**")
	}
	for _, tp := range MergeDenyTopPaths {
		ps = append(ps, ":(icase,glob)"+tp, ":(icase,glob)"+tp+"/**")
	}
	return ps
}

// linkFixedPaths are places at the repo top the guard (or the host's tools)
// reads config from, whether or not git tracks them: a link there leads the
// read, and the host's tools, to another file.
func linkFixedPaths() []string {
	ps := slices.Clone(refConfigFiles)
	ps = append(ps, miseNames...)
	return append(ps, ".claude/CLAUDE.md", ".claude/rules")
}

// protectedSet is S: the distinct spellings (case-folded), in order, capped.
type protectedSet struct {
	items []string
	seen  map[string]bool
}

// add keeps p; false when the set is full.
func (s *protectedSet) add(p string) bool {
	p = path.Clean(p)
	if p == "" {
		p = "."
	}
	k := foldKey(p)
	if s.seen[k] {
		return true
	}
	if len(s.items) >= mergeLinkSetMax {
		return false
	}
	s.seen[k] = true
	s.items = append(s.items, p)
	return true
}

// linkDenies builds S from the host's work tree and returns what its links
// deny: one pathspec-limited `ls-files -s` call, one read of the repo top, and
// at most mergeLinkLstatMax Lstat calls. S holds the tracked symbolic links at
// a protected name, the links at the top under a protected name, the fixed
// config places and every path the earlier checks found (hooks dirs, git
// config files, import targets, config refs). A tracked file that is not a
// link needs no walk: git tracks nothing below a link, so its path is real.
//
// A non-nil result is the refusal: guarded and naming the path for a loop or
// an Lstat error, failed when a cap is hit or the repo cannot be read.
func (m *merger) linkDenies(hooks, gitDenied, imports, refs []string) (*linkDenies, *MergeResult) {
	set := &protectedSet{seen: map[string]bool{}}
	full := func() *MergeResult {
		return m.result(MergeFailed, "cannot check symbolic links: "+errLinkCap.Error(), nil)
	}

	args := append([]string{"ls-files", "-s", "-z", "--"}, protectedPathspecs()...)
	out, err := m.run(m.env(), args...)
	if err != nil {
		return nil, m.failed("cannot list the protected files", err)
	}
	es, err := parseLsFilesStage(out)
	if err != nil {
		return nil, m.failed("cannot read the protected file list", err)
	}
	for _, e := range es {
		if e.mode == "120000" && mergeDenied(e.path, hooks) && !set.add(e.path) {
			return nil, full()
		}
	}

	w, err := newLinkWalker(m.of)
	if err != nil {
		return nil, m.failed("cannot open the repository", err)
	}
	defer w.close()
	tops, err := w.top()
	if errors.Is(err, errLinkCap) {
		return nil, full()
	} else if err != nil {
		return nil, m.failed("cannot list the top of the repository", err)
	}
	for _, name := range tops {
		if mergeDenied(name, nil) && !set.add(name) {
			return nil, full()
		}
	}
	for _, group := range [][]string{MergeDenyTopPaths, linkFixedPaths(), hooks, gitDenied, imports, refs} {
		for _, p := range group {
			if !set.add(p) {
				return nil, full()
			}
		}
	}

	// A path a config names that leads through a link to another real file: that
	// file's folder is refused too, as the folder of a named script is (the
	// script loads its neighbours). Not for other protected names, and never the
	// repo top.
	isRef := map[string]bool{}
	for _, r := range refs {
		isRef[foldKey(path.Clean(r))] = true
	}
	d := newLinkDenies()
	for _, s := range set.items {
		real, outside, err := w.resolve(s)
		switch {
		case errors.Is(err, errLinkCap):
			return nil, full()
		case err != nil:
			return nil, m.result(MergeGuarded, "a protected file or folder is a symbolic link the merge cannot follow ("+
				linkErrText(err)+"), so it cannot tell what the link leads to", []string{s})
		}
		if !outside && !sameComps(real, strings.Split(s, "/")) {
			d.add(strings.Join(real, "/"))
			if isRef[foldKey(s)] && len(real) > 1 {
				d.add(strings.Join(real[:len(real)-1], "/"))
			}
		}
	}
	// Each link met is protected too: replacing one with a file changes what
	// the protected name loads, even when its chain ends outside the repo.
	for _, l := range w.links {
		d.add(l)
	}
	return d, nil
}

func linkErrText(err error) string {
	if errors.Is(err, errLinkLoop) {
		return errLinkLoop.Error()
	}
	return fmt.Sprintf("%v", err)
}

// sameComps compares a resolved path with a spelling, ignoring "." and case.
func sameComps(real, spelled []string) bool {
	spelled = slices.DeleteFunc(slices.Clone(spelled), func(c string) bool { return c == "." || c == "" })
	return slices.EqualFunc(real, spelled, strings.EqualFold)
}
