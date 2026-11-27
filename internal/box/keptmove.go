package box

// The move of a kept folder from a box home into its store, merging into what
// the store already holds without replacing or losing anything.
//
// Home and store are both under boxstate, so every step is a rename on one
// filesystem: atomic, no copy. Nothing is resolved by path: the home folder's
// parent is reached one descriptor at a time without following a link
// (keptOpenPath), the store side is made and opened the same way
// (keptMkdirPath), and every rename is relative to those directory
// descriptors (renameNoReplace: renameat2 RENAME_NOREPLACE on Linux,
// renameatx_np RENAME_EXCL on macOS). A link is moved as a link, never
// followed or descended.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// errKeptChanged is mergeMove's refusal when the home folder is not the one
// discovery saw: gone, replaced, or reached through a link. Nothing moved into
// the store's home/ (a folder swapped in right before the rename goes back to
// the home, or to clash/ when it cannot).
var errKeptChanged = errors.New("the folder changed since it was screened")

// keptMoveHook, when set (tests only), runs right before every rename
// mergeMove makes, with the home-relative path of what it moves.
var keptMoveHook func(rel string)

// keptClashTries is how many names a clash copy may try in clash/<box>/: the
// name itself, then name~2, name~3 and so on.
const keptClashTries = 100

// mergeMove moves the folder rel of home into store's home/<rel>, the folder
// discovery found as want (rel must be want.Rel): it is refused with
// errKeptChanged, nothing kept, when what is at rel now is not that directory
// (another dev/ino, a link, gone) or when a parent of rel is a link.
//
// When the store has no home/<rel>, the folder is renamed there whole (moved
// 1). Otherwise the two are merged, walking both by directory descriptor: a
// directory on both sides is merged in turn, a name the store lacks is renamed
// there, and any other name on both sides (a clash: a file or a link on either
// side, or a directory against a non-directory) moves the home's entry to
// clashBase/<its home-relative path> (clashBase is clash/<box>), under a
// numbered name when an earlier copy is there. moved counts the renames into
// home/, clashed those into clash/. A merged home directory, emptied, is
// removed.
//
// The merge has the screen's caps (keptScreenMaxEntries, keptScreenMaxDepth,
// keptScreenTimeout). A cap hit or any error stops it with an error: what moved
// stays moved (each rename is atomic), the rest stays in the home, and nothing
// is lost. A nil error means nothing is left at rel in the home.
func mergeMove(home, store *os.Root, rel, clashBase string, want keptCandidate) (moved, clashed int, err error) {
	if !validKeptRel(rel) || want.Rel != rel {
		return 0, 0, fmt.Errorf("box: %q is not a folder that can be kept", rel)
	}
	if !validKeptClash(clashBase) {
		return 0, 0, fmt.Errorf("box: %q is not a clash folder (clash/<box name>)", clashBase)
	}
	m := keptMerge{store: store, clash: clashBase, deadline: time.Now().Add(keptScreenTimeout)}
	err = m.top(home, rel, want)
	return m.moved, m.clashed, err
}

// validKeptClash reports whether c is clash/<box name>.
func validKeptClash(c string) bool {
	dir, box, ok := strings.Cut(c, "/")
	return ok && dir == keptClashDirName && isBoxName(box)
}

type keptMerge struct {
	store          *os.Root
	clash          string
	deadline       time.Time
	entries        int
	moved, clashed int
}

// keptChanged reports whether err from resolving the home folder says it is
// not what it was: a link or a non-directory on the way, a name gone, or a
// file swapped between its stat and its open.
func keptChanged(err error) bool {
	return errors.Is(err, errKeptLink) || errors.Is(err, errKeptNotDir) || errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, unix.ESTALE) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

func changedErr(rel string, cause error) error {
	if cause == nil {
		return fmt.Errorf("box: %s: %w", rel, errKeptChanged)
	}
	return fmt.Errorf("box: %s: %w (%v)", rel, errKeptChanged, cause)
}

// top moves the folder rel itself.
func (m *keptMerge) top(home *os.Root, rel string, want keptCandidate) error {
	prel, name := path.Split(rel)
	prel = strings.TrimSuffix(prel, "/")
	var hp *os.File
	var err error
	if prel == "" {
		hp, err = openDirIn(home, ".")
	} else {
		hp, _, err = keptOpenPath(home, prel)
	}
	if err != nil {
		if keptChanged(err) {
			return changedErr(rel, err)
		}
		return fmt.Errorf("box: %s: opening its parent in the home: %w", rel, err)
	}
	defer hp.Close()
	hfd := int(hp.Fd())
	var hst unix.Stat_t
	if err := unix.Fstatat(hfd, name, &hst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return changedErr(rel, err)
		}
		return fmt.Errorf("box: %s: %w", rel, err)
	}
	if hst.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(hst.Dev) != want.Dev || uint64(hst.Ino) != want.Ino {
		return changedErr(rel, nil)
	}
	// The home side is resolved first: a refusal there creates nothing in the store.
	sp, _, err := keptMkdirPath(m.store, path.Join(keptHomeDirName, prel))
	if err != nil {
		return fmt.Errorf("box: %s: the store's folder: %w", rel, err)
	}
	defer sp.Close()
	sfd := int(sp.Fd())
	for try := 0; ; try++ {
		var sst unix.Stat_t
		err := unix.Fstatat(sfd, name, &sst, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case errors.Is(err, unix.ENOENT):
			err := m.rename(hfd, name, sfd, name, rel)
			if err == nil {
				return m.landed(hfd, sfd, name, rel, want)
			}
			if keptTaken(err) && try == 0 {
				continue // made meanwhile: merge into it
			}
			if errors.Is(err, unix.ENOENT) {
				return changedErr(rel, err)
			}
			return fmt.Errorf("box: %s: moving it into the store: %w", rel, err)
		case err != nil:
			return fmt.Errorf("box: %s: in the store: %w", rel, err)
		case sst.Mode&unix.S_IFMT == unix.S_IFDIR:
			hd, err := keptOpenAt(hfd, rel, name, &hst, unix.O_DIRECTORY)
			if err != nil {
				if keptChanged(err) {
					return changedErr(rel, err)
				}
				return fmt.Errorf("box: %s: %w", rel, err)
			}
			defer hd.Close()
			return m.pair(hd, hfd, sfd, name, rel, &hst, &sst, 0)
		default:
			// The store has a file or a link there: the whole folder is a clash.
			return m.toClash(hfd, name, rel)
		}
	}
}

// landed checks that what the rename put at the store's home/<rel> is the
// folder screened. If it is not (swapped right before the rename), it goes
// back to the home, or failing that to clash/, which is never mounted, and the
// move is refused.
func (m *keptMerge) landed(hfd, sfd int, name, rel string, want keptCandidate) error {
	var st unix.Stat_t
	err := unix.Fstatat(sfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && uint64(st.Dev) == want.Dev && uint64(st.Ino) == want.Ino {
		m.moved++
		return nil
	}
	if err != nil {
		return fmt.Errorf("box: %s: checking it in the store: %w", keptShow(rel), err)
	}
	if renameNoReplace(sfd, name, hfd, name) != nil {
		if cerr := m.toClash(sfd, name, rel); cerr != nil {
			return fmt.Errorf("box: %s: a folder swapped in before the move is in the store and could not be taken out: %w", keptShow(rel), cerr)
		}
	}
	return changedErr(rel, nil)
}

// pair merges the home directory hd (the entry name of the home directory
// hpfd, at rel, stat hst) into the store's directory name of sfd (stat sst),
// and removes it once emptied. Entries of hd are at depth+1.
func (m *keptMerge) pair(hd *os.File, hpfd, sfd int, name, rel string, hst, sst *unix.Stat_t, depth int) error {
	if hst.Dev == sst.Dev && hst.Ino == sst.Ino {
		return fmt.Errorf("box: %s: the home and the store hold the same directory", keptShow(rel))
	}
	sd, err := keptOpenAt(sfd, keptHomeDirName+"/"+rel, name, sst, unix.O_DIRECTORY)
	if err != nil {
		return fmt.Errorf("box: %s: opening it in the store: %w", keptShow(rel), err)
	}
	defer sd.Close()
	if err := m.dir(hd, sd, rel, depth); err != nil {
		return err
	}
	// Every name listed moved: what is left came in meanwhile and stays.
	if err := unix.Unlinkat(hpfd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("box: %s: not empty after the merge: %w", keptShow(rel), err)
	}
	return nil
}

// dir merges the entries of the home directory hd, at rel, into the store
// directory sd.
func (m *keptMerge) dir(hd, sd *os.File, rel string, depth int) error {
	// The names first, then the moves: the listing is not read while it changes.
	names, err := keptReadNames(hd)
	if err != nil {
		if errors.Is(err, errKeptTooMany) {
			return fmt.Errorf("box: %s: %w past %d", keptShow(rel), errKeptTooMany, keptScreenMaxEntries)
		}
		return fmt.Errorf("box: %s: listing it: %w", keptShow(rel), err)
	}
	hfd, sfd := int(hd.Fd()), int(sd.Fd())
	for _, n := range names {
		p := rel + "/" + n
		if err := m.tick(p, depth+1); err != nil {
			return err
		}
		var hst unix.Stat_t
		if err := unix.Fstatat(hfd, n, &hst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // gone from the home: nothing to keep
			}
			return fmt.Errorf("box: %s: %w", keptShow(p), err)
		}
		if err := m.entry(hfd, sfd, n, p, &hst, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// tick counts one entry against the caps.
func (m *keptMerge) tick(p string, depth int) error {
	if m.entries++; m.entries > keptScreenMaxEntries || depth > keptScreenMaxDepth {
		return fmt.Errorf("box: %s: %w to merge (at most %d entries, %d deep)", keptShow(p), errKeptTooMany, keptScreenMaxEntries, keptScreenMaxDepth)
	}
	if !time.Now().Before(m.deadline) {
		return fmt.Errorf("box: %s: the merge ran out of time (%s)", keptShow(p), keptScreenTimeout)
	}
	return nil
}

// entry merges the name n of the home directory hfd (at p, stat hst, depth)
// into the store directory sfd.
func (m *keptMerge) entry(hfd, sfd int, n, p string, hst *unix.Stat_t, depth int) error {
	for try := 0; ; try++ {
		var sst unix.Stat_t
		err := unix.Fstatat(sfd, n, &sst, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case errors.Is(err, unix.ENOENT):
			err := m.rename(hfd, n, sfd, n, p)
			switch {
			case err == nil:
				m.moved++
				return nil
			case errors.Is(err, unix.ENOENT):
				return nil // gone from the home meanwhile
			case keptTaken(err) && try == 0:
				continue // made in the store meanwhile: look again
			}
			return fmt.Errorf("box: %s: moving it into the store: %w", keptShow(p), err)
		case err != nil:
			return fmt.Errorf("box: %s: in the store: %w", keptShow(p), err)
		case hst.Mode&unix.S_IFMT == unix.S_IFDIR && sst.Mode&unix.S_IFMT == unix.S_IFDIR:
			hd, err := keptOpenAt(hfd, p, n, hst, unix.O_DIRECTORY)
			if err != nil {
				return fmt.Errorf("box: %s: %w", keptShow(p), err)
			}
			defer hd.Close()
			return m.pair(hd, hfd, sfd, n, p, hst, &sst, depth)
		default:
			return m.toClash(hfd, n, p)
		}
	}
}

// errKeptNoClashSlot: no name where clashIn looked can take a clash copy.
var errKeptNoClashSlot = errors.New("no free name for an older copy")

// toClash moves the entry name of the directory fromfd, whose home-relative
// path is rel, to clash/<box>/<rel>: under name itself, or the first free of
// name~2 .. name~keptClashTries. When none of those can take it (a file or a
// link where a folder of the path belongs, every name taken, or a name so long
// that a ~N suffix passes NAME_MAX), it goes to clash/<box>/~<N>/<rel>, for the
// first N up to keptClashTries that can: a home-relative path starts with ".",
// so a ~N folder is never one. Nothing there is ever replaced.
func (m *keptMerge) toClash(fromfd int, name, rel string) error {
	err := m.clashIn(fromfd, name, rel, path.Join(m.clash, path.Dir(rel)), keptClashTries)
	for n := 1; errors.Is(err, errKeptNoClashSlot) && n <= keptClashTries; n++ {
		err = m.clashIn(fromfd, name, rel, path.Join(m.clash, "~"+strconv.Itoa(n), path.Dir(rel)), 1)
	}
	if errors.Is(err, errKeptNoClashSlot) {
		return fmt.Errorf("box: %s: no free place for an older copy in %s", keptShow(rel), m.clash)
	}
	return err
}

// clashIn moves the entry name of fromfd into the store directory dir (made as
// needed, never through a link) under the first free of name, name~2 ..
// name~tries. errKeptNoClashSlot when dir cannot be made there or no name is
// free.
func (m *keptMerge) clashIn(fromfd int, name, rel, dir string, tries int) error {
	cd, _, err := keptMkdirPath(m.store, dir)
	if err != nil {
		if errors.Is(err, errKeptNotDir) || errors.Is(err, errKeptLink) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENAMETOOLONG) {
			return fmt.Errorf("%w: %v", errKeptNoClashSlot, err)
		}
		return fmt.Errorf("box: %s: the clash folder: %w", keptShow(rel), err)
	}
	defer cd.Close()
	cfd := int(cd.Fd())
	for i := 1; i <= tries; i++ {
		to := name
		if i > 1 {
			to = name + "~" + strconv.Itoa(i)
		}
		// Checked first for renameNoReplace's fallback, which can replace what
		// is there; the rename itself refuses any name taken since.
		var st unix.Stat_t
		switch err := unix.Fstatat(cfd, to, &st, unix.AT_SYMLINK_NOFOLLOW); {
		case errors.Is(err, unix.ENAMETOOLONG):
			return errKeptNoClashSlot // and so is every longer number
		case err == nil:
			continue
		case !errors.Is(err, unix.ENOENT):
			return fmt.Errorf("box: %s: in the clash folder: %w", keptShow(rel), err)
		}
		err := m.rename(fromfd, name, cfd, to, rel)
		switch {
		case err == nil:
			m.clashed++
			return nil
		case errors.Is(err, unix.ENOENT):
			return nil // gone meanwhile
		case errors.Is(err, unix.ENAMETOOLONG):
			return errKeptNoClashSlot
		case !keptTaken(err):
			return fmt.Errorf("box: %s: moving it to the clash folder: %w", keptShow(rel), err)
		}
	}
	return errKeptNoClashSlot
}

func (m *keptMerge) rename(fromfd int, from string, tofd int, to, rel string) error {
	if keptMoveHook != nil {
		keptMoveHook(rel)
	}
	return renameNoReplace(fromfd, from, tofd, to)
}

// keptTaken reports whether a rename failed because its target exists.
func keptTaken(err error) bool {
	return errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY)
}
