package handback

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"time"
)

// readBatch is how many directory entries one ReadDir call returns. The caps
// and the context are checked between batches, so one huge directory cannot
// run past them by more than a batch.
const readBatch = 1024

// InspectDir walks root in place and flags what the box wrote there since
// since. root is a folder an untrusted box had write access to, so every step
// assumes it is hostile: nothing is followed (a symlink is read as text, never
// entered), nothing but a regular file is opened (a FIFO would block, a device
// has side effects), at most HeadBytes of a file are read to classify it, and
// the walk stops at the caps in lim, saying which one in Report.Truncated.
//
// A file is a candidate when its ctime is at or after since. ctime, unlike
// mtime, cannot be set from user space, so a box that touches a file back to
// the year 2000 is still a candidate. The window applies to symlinks too, so
// links that were there before the session are not reported. A file whose
// ctime is unavailable is always a candidate. A zero field in lim falls back
// to its default.
//
// Nothing a box does may hide a signal without the report saying so: every
// entry that could not be read (chmod 000, a swap mid-scan) is counted in
// Truncated, a file that cannot be read is still flagged from the mode lstat
// shows (setuid, setgid), and once the candidate cap stops head reads the walk
// carries on with the signals that need no read (outward symlinks, set-id
// bits) until the entry, time or depth cap. When the flag cap is full the
// strongest flags win: a new flag evicts the weakest kept one, and Truncated
// says how many were left out.
//
// The error is non-nil only when root cannot be opened. Inspected counts the
// files whose head was read plus the symlinks examined.
func InspectDir(ctx context.Context, root string, since time.Time, lim Limits) (Report, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return Report{}, err
	}
	defer r.Close()
	return InspectRoot(ctx, r, since, lim)
}

// InspectRoot is InspectDir over a root the caller has opened (and may have
// checked: the identity of what it opened) itself. It does not close r.
func InspectRoot(ctx context.Context, r *os.Root, since time.Time, lim Limits) (Report, error) {
	lim = withDefaults(lim)
	s := &scanner{ctx: ctx, r: r, lim: lim, since: since, start: time.Now(), hashLeft: lim.HashTotal, flags: NewFlagSet(lim.MaxFlags)}
	s.walkDir(".", 0)

	rep := Report{Flags: []Flag{}, Inspected: s.inspected}
	reasons := s.reasons
	if s.tooDeep > 0 {
		reasons = append(reasons, fmt.Sprintf("depth cap of %d levels reached; directories at that depth were not read (%d)", lim.Depth, s.tooDeep))
	}
	if s.unreadable > 0 {
		reasons = append(reasons, fmt.Sprintf("%d entries could not be read", s.unreadable))
	}
	if d := s.flags.Dropped(); d > 0 {
		reasons = append(reasons, fmt.Sprintf("flag cap of %d reached; %d more flagged files are not listed", lim.MaxFlags, d))
	}
	rep.Truncated = strings.Join(reasons, "; ")
	rep.Flags = s.flags.Sorted()
	return rep, nil
}

// flagLess is SortFlags' order: true when a is stronger (shown sooner) than b.
func flagLess(a, b Flag) bool {
	if ra, rb := bestRank(a), bestRank(b); ra != rb {
		return ra < rb
	}
	return a.Path < b.Path
}

// flagHeap keeps the weakest flag at index 0 so a full record can evict it.
type flagHeap []Flag

func (h flagHeap) Len() int           { return len(h) }
func (h flagHeap) Less(i, j int) bool { return flagLess(h[j], h[i]) }
func (h flagHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *flagHeap) Push(x any)        { *h = append(*h, x.(Flag)) }
func (h *flagHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// FlagSet stores at most max flags and, once full, lets only a stronger flag
// in, evicting the weakest kept one. A hostile tree full of weak flags (a
// thousand archives) therefore cannot push a mismatch or an outward symlink
// out of the record; what did not fit is counted in Dropped.
type FlagSet struct {
	h       flagHeap
	max     int
	dropped int
}

// NewFlagSet returns a set that keeps at most n flags (n < 1 means 1).
func NewFlagSet(n int) *FlagSet { return &FlagSet{max: max(n, 1)} }

// Offer stores fl if it fits or is stronger than the weakest kept flag. fill,
// if not nil, is called on fl only when it is kept, so a costly step (hashing)
// is skipped for a flag that is dropped.
func (s *FlagSet) Offer(fl Flag, fill func(*Flag)) {
	full := len(s.h) >= s.max
	if full && !flagLess(fl, s.h[0]) {
		s.dropped++
		return
	}
	if fill != nil {
		fill(&fl)
	}
	if !full {
		heap.Push(&s.h, fl)
		return
	}
	s.h[0] = fl
	heap.Fix(&s.h, 0)
	s.dropped++
}

// Dropped is how many offered flags are not in the set.
func (s *FlagSet) Dropped() int { return s.dropped }

// Sorted returns the kept flags in SortFlags order (never nil).
func (s *FlagSet) Sorted() []Flag {
	out := append([]Flag{}, s.h...)
	SortFlags(out)
	return out
}

// scanner holds the state of one InspectDir walk.
type scanner struct {
	ctx   context.Context
	r     *os.Root
	lim   Limits
	since time.Time
	start time.Time

	flags    *FlagSet
	reasons  []string
	hashLeft int64

	entries, candidates, inspected int
	tooDeep, unreadable            int
	headsCapped, stopped           bool
}

func (s *scanner) stop(why string) {
	s.reasons = append(s.reasons, why)
	s.stopped = true
}

// halt reports whether the walk must end: ctx done or the time budget used up.
func (s *scanner) halt() bool {
	if s.stopped {
		return true
	}
	if s.ctx.Err() != nil {
		s.stop("scan cancelled before it finished")
		return true
	}
	if time.Since(s.start) > s.lim.Budget {
		s.stop(fmt.Sprintf("time budget of %s used up", s.lim.Budget))
		return true
	}
	return false
}

// openDir opens the directory rel under the root. rel came from a directory
// listing, so the box may have swapped it since: the Lstat must still say
// directory and the opened file must be that same one.
func (s *scanner) openDir(rel string) (*os.File, bool) {
	if rel == "." {
		f, err := s.r.Open(".")
		return f, err == nil
	}
	lst, err := s.r.Lstat(rel)
	if err != nil || !lst.IsDir() {
		return nil, false
	}
	f, err := s.r.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, false
	}
	st, err := f.Stat()
	if err != nil || !st.IsDir() || !os.SameFile(lst, st) {
		f.Close()
		return nil, false
	}
	return f, true
}

// walkDir reads the directory rel (at the given depth) in batches and visits
// every entry; subdirectories are walked as they are met, so the recursion is
// at most the depth cap deep.
func (s *scanner) walkDir(rel string, depth int) {
	f, ok := s.openDir(rel)
	if !ok {
		s.unreadable++
		return
	}
	defer f.Close()
	for {
		if s.halt() {
			return
		}
		ents, err := f.ReadDir(readBatch)
		for _, d := range ents {
			s.entry(rel, d, depth)
			if s.stopped {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				s.unreadable++
			}
			return
		}
	}
}

// entry inspects one directory entry. parent is the directory's path.
func (s *scanner) entry(parent string, d fs.DirEntry, depth int) {
	if s.halt() {
		return
	}
	p := d.Name()
	if parent != "." {
		p = parent + "/" + p
	}
	s.entries++
	if s.entries > s.lim.Entries {
		s.stop(fmt.Sprintf("entry cap of %d entries reached", s.lim.Entries))
		return
	}

	t := d.Type()
	if t.IsDir() {
		// os.Root walks stall on deep chains, so a directory this deep is not
		// read at all.
		if depth+1 >= s.lim.Depth {
			s.tooDeep++
			return
		}
		s.walkDir(p, depth+1)
		return
	}
	isLink := t&fs.ModeSymlink != 0
	if !isLink && !t.IsRegular() {
		return // FIFO, socket, device: never opened
	}
	if !isLink && gitObjectStoreFile(p) {
		return // compressed blob or pack of a repo: cannot hold a runnable head
	}

	lst, err := s.r.Lstat(p)
	if err != nil {
		s.unreadable++
		return
	}
	if ct, ok := ctimeOf(lst); ok && ct.Before(s.since) {
		return
	}
	if isLink {
		s.symlink(p)
		return
	}
	if !lst.Mode().IsRegular() {
		s.unreadable++ // swapped for something else since the listing
		return
	}
	s.regular(p, lst)
}

func (s *scanner) symlink(p string) {
	target, err := s.r.Readlink(p)
	if err != nil {
		s.unreadable++
		return
	}
	s.inspected++
	if !OutsideRoot(p, target) {
		return
	}
	s.keep(Flag{
		Path:    SanitizePath(p),
		Classes: []Class{ClassSymlinkOut},
		Detail:  "symlink points outside the workspace",
		Target:  SanitizePath(target),
	}, nil, 0)
}

// regular classifies the in-window regular file p, which lst described.
func (s *scanner) regular(p string, lst fs.FileInfo) {
	base := path.Base(p)
	// An empty file has no head, and once the candidate cap has stopped head
	// reads the mode is all that is left: neither is opened or counted.
	if lst.Size() == 0 || s.headsCapped {
		s.flagMode(p, base, lst.Mode())
		return
	}
	if s.candidates >= s.lim.Candidates {
		s.headsCapped = true
		s.reasons = append(s.reasons, fmt.Sprintf("candidate cap of %d files to read reached; later files are checked by mode only", s.lim.Candidates))
		s.flagMode(p, base, lst.Mode())
		return
	}
	s.candidates++
	f, head, mode, ok := readHead(s.r, p, lst)
	if !ok {
		s.unreadable++
		s.flagMode(p, base, lst.Mode())
		return
	}
	defer f.Close()
	s.inspected++
	classes, detail := Classify(base, mode, head)
	if len(classes) == 0 {
		return
	}
	// The head is already read, so hash head + rest of the same open file.
	s.keep(Flag{Path: SanitizePath(p), Classes: classes, Detail: SanitizePath(detail)},
		io.MultiReader(bytes.NewReader(head), f), lst.Size())
}

// flagMode flags p from its mode alone (set-id bits), without reading it.
func (s *scanner) flagMode(p, base string, mode fs.FileMode) {
	if classes, detail := Classify(base, mode, nil); len(classes) > 0 {
		s.keep(Flag{Path: SanitizePath(p), Classes: classes, Detail: SanitizePath(detail)}, nil, 0)
	}
}

// keep stores fl, hashing rd (a file of the given size) if it is kept. Once
// the record is full a flag only gets in by evicting a weaker one.
func (s *scanner) keep(fl Flag, rd io.Reader, size int64) {
	var fill func(*Flag)
	if rd != nil {
		fill = func(f *Flag) { f.SHA256, f.HashNote = s.hashOpen(rd, size) }
	}
	s.flags.Offer(fl, fill)
}

// hashOpen hashes rd, which holds a file of the given size. A file already
// known to be over a cap is not read at all.
func (s *scanner) hashOpen(rd io.Reader, size int64) (sum, note string) {
	switch {
	case size > s.lim.HashPerFile:
		return "", noteTooLarge
	case size > s.hashLeft:
		return "", noteBudget
	}
	return hashCappedCtx(s.ctx, rd, s.lim.HashPerFile, &s.hashLeft)
}

// gitObjectStoreFile reports whether p (slash-relative) is a file of a git
// object store: a loose object ".git/objects/<2 hex>/<38 or 62 hex>" or a
// pack file ".git/objects/pack/pack-*.{pack,idx,rev,bitmap,mtimes,promisor,keep}".
// Those are compressed or indexed data that cannot start with a runnable head,
// and a big repo has hundreds of thousands of them. Anything else under
// .git/objects (alternates, a stray file) is not skipped.
func gitObjectStoreFile(p string) bool {
	parts := strings.Split(p, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != ".git" || parts[i+1] != "objects" {
			continue
		}
		rest := parts[i+2:]
		if len(rest) != 2 {
			return false
		}
		if rest[0] == "pack" {
			if !strings.HasPrefix(rest[1], "pack-") {
				return false
			}
			switch path.Ext(rest[1]) {
			case ".pack", ".idx", ".rev", ".bitmap", ".mtimes", ".promisor", ".keep":
				return true
			}
			return false
		}
		return isLowerHex(rest[0], 2) && (isLowerHex(rest[1], 38) || isLowerHex(rest[1], 62))
	}
	return false
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// readHead opens the regular file p under r and reads its first HeadBytes. It
// returns the open file (positioned after the head) and its mode, or ok false
// for anything that is not still the regular file lst described. O_NONBLOCK
// keeps a FIFO swapped in after the Lstat from blocking the open, and
// os.Root.OpenFile follows an in-root symlink on the last component even with
// O_NOFOLLOW, so the opened file must also be the one the Lstat saw.
func readHead(r *os.Root, p string, lst fs.FileInfo) (f *os.File, head []byte, mode fs.FileMode, ok bool) {
	f, err := r.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, 0, false
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(lst, st) {
		f.Close()
		return nil, nil, 0, false
	}
	head, err = io.ReadAll(io.LimitReader(f, HeadBytes))
	if err != nil {
		f.Close()
		return nil, nil, 0, false
	}
	return f, head, st.Mode(), true
}

// withDefaults fills every non-positive cap from DefaultLimits.
func withDefaults(l Limits) Limits {
	d := DefaultLimits()
	if l.Entries <= 0 {
		l.Entries = d.Entries
	}
	if l.Depth <= 0 {
		l.Depth = d.Depth
	}
	if l.Candidates <= 0 {
		l.Candidates = d.Candidates
	}
	if l.MaxFlags <= 0 {
		l.MaxFlags = d.MaxFlags
	}
	if l.Budget <= 0 {
		l.Budget = d.Budget
	}
	if l.HashPerFile <= 0 {
		l.HashPerFile = d.HashPerFile
	}
	if l.HashTotal <= 0 {
		l.HashTotal = d.HashTotal
	}
	return l
}
