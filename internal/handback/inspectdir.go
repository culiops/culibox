package handback

import (
	"bytes"
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
// The error is non-nil only when root cannot be opened; everything that goes
// wrong below it (an unreadable directory, a file that vanished) skips that
// entry. Inspected counts the files whose head was read plus the symlinks
// examined. If the flag cap is hit, the first MaxFlags found in walk order are
// kept (then sorted), and Truncated says how many more were left out.
func InspectDir(ctx context.Context, root string, since time.Time, lim Limits) (Report, error) {
	lim = withDefaults(lim)
	r, err := os.OpenRoot(root)
	if err != nil {
		return Report{}, err
	}
	defer r.Close()

	start := time.Now()
	rep := Report{Flags: []Flag{}}
	var reasons []string
	var entries, candidates, extra, tooDeep int
	hashLeft := lim.HashTotal

	// full reports whether the record already holds MaxFlags flags.
	full := func() bool { return len(rep.Flags) >= lim.MaxFlags }

	walk := func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			reasons = append(reasons, "scan cancelled before it finished")
			return fs.SkipAll
		}
		if time.Since(start) > lim.Budget {
			reasons = append(reasons, fmt.Sprintf("time budget of %s used up", lim.Budget))
			return fs.SkipAll
		}
		if err != nil || p == "." {
			return nil // unreadable entry or the root itself
		}
		entries++
		if entries > lim.Entries {
			reasons = append(reasons, fmt.Sprintf("entry cap of %d entries reached", lim.Entries))
			return fs.SkipAll
		}

		t := d.Type()
		if t.IsDir() {
			if path.Base(p) == "objects" && path.Base(path.Dir(p)) == ".git" {
				return fs.SkipDir // git's object store: compressed blobs, huge
			}
			// os.Root walks stall on deep chains, so a directory this deep is
			// not read at all.
			if strings.Count(p, "/")+1 >= lim.Depth {
				tooDeep++
				return fs.SkipDir
			}
			return nil
		}
		isLink := t&fs.ModeSymlink != 0
		if !isLink && !t.IsRegular() {
			return nil // FIFO, socket, device: never opened
		}

		lst, err := r.Lstat(p)
		if err != nil {
			return nil
		}
		if ct, ok := ctimeOf(lst); ok && ct.Before(since) {
			return nil
		}

		if isLink {
			target, err := r.Readlink(p)
			if err != nil {
				return nil
			}
			rep.Inspected++
			if !OutsideRoot(p, target) {
				return nil
			}
			if full() {
				extra++
				return nil
			}
			rep.Flags = append(rep.Flags, Flag{
				Path:    SanitizePath(p),
				Classes: []Class{ClassSymlinkOut},
				Detail:  "symlink points outside the workspace",
				Target:  SanitizePath(target),
			})
			return nil
		}

		candidates++
		if candidates > lim.Candidates {
			reasons = append(reasons, fmt.Sprintf("candidate cap of %d files to read reached", lim.Candidates))
			return fs.SkipAll
		}
		f, head, mode, ok := readHead(r, p, lst)
		if !ok {
			return nil
		}
		defer f.Close()
		rep.Inspected++
		classes, detail := Classify(path.Base(p), mode, head)
		if len(classes) == 0 {
			return nil
		}
		if full() {
			extra++
			return nil
		}
		fl := Flag{Path: SanitizePath(p), Classes: classes, Detail: SanitizePath(detail)}
		// The head is already read, so hash head + rest of the same open file.
		fl.SHA256, fl.HashNote = hashOpen(io.MultiReader(bytes.NewReader(head), f), lst.Size(), lim.HashPerFile, &hashLeft)
		rep.Flags = append(rep.Flags, fl)
		return nil
	}
	_ = fs.WalkDir(r.FS(), ".", walk)

	if tooDeep > 0 {
		reasons = append(reasons, fmt.Sprintf("depth cap of %d levels reached; directories at that depth were not read (%d)", lim.Depth, tooDeep))
	}
	if extra > 0 {
		reasons = append(reasons, fmt.Sprintf("flag cap of %d reached; %d more flagged files are not listed", lim.MaxFlags, extra))
	}
	rep.Truncated = strings.Join(reasons, "; ")
	SortFlags(rep.Flags)
	return rep, nil
}

// hashOpen hashes rd, which holds a file of the given size. A file already
// known to be over a cap is not read at all.
func hashOpen(rd io.Reader, size, perFile int64, budget *int64) (sum, note string) {
	switch {
	case size > perFile:
		return "", noteTooLarge
	case size > *budget:
		return "", noteBudget
	}
	return hashCapped(rd, perFile, budget)
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
