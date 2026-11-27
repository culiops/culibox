package box

// The store of kept folders: one per conversations key, at
// boxstate/.kept/<key>/, beside the homes. It holds
//
//	home/<rel>   the kept folders, mounted read-write into the profile's boxes
//	clash/<box>/ the home copies of names the store already had, never mounted
//	state.json   host-owned: what is kept, what is not and why, what is forgotten
//
// and boxstate/.kept/.boxes/<box> maps a box name to its key. Neither name is a
// box name, so reclaim never takes them for a home.
//
// The host writes all of it, but on macOS the VM shares boxstate read-write,
// so nothing here is trusted when it is read back: every directory is walked
// one descriptor at a time without following a link (keptMkdirAt,
// keptOpenPath), every file is read no-follow with a size cap, and every path
// read from state.json or a box record is checked again (validKeptRel).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	keptDirName      = ".kept"
	keptBoxesDirName = ".boxes"
	// keptRetriesDirName holds, per box, how many reclaim passes kept its home
	// for a folder too slow to check (keptReclaimRetries).
	keptRetriesDirName = ".retries"
	keptHomeDirName    = "home"
	keptClashDirName   = "clash"
	keptStateName      = "state.json"

	keptStatusKept = "kept" // mounted into the profile's boxes
	keptStatusHeld = "held" // a re-screen refused it: kept on disk, not mounted
)

// Store caps. Variables so tests can lower them.
var (
	keptMaxPaths   = 32 // kept paths per store: each is a docker mount
	keptMaxNotKept = 50 // not-kept rows per store, the latest per path
)

// Read caps: a file past them is refused, never read whole.
const (
	keptStateMax  = 1 << 20
	keptRecordMax = 64 << 10
)

// KeptState is a store's state.json.
type KeptState struct {
	Profile   string           `json:"profile"`
	Workspace string           `json:"workspace"`
	Account   string           `json:"account"`
	Kept      []KeptEntry      `json:"kept"`
	NotKept   []KeptRefusalRow `json:"not_kept"`
	Forgotten []string         `json:"forgotten"` // never kept again in this store
}

// KeptEntry is one kept folder. Path is home-relative (validKeptRel). Bytes and
// Files come from the screen walk, so a reader never walks the folder.
type KeptEntry struct {
	Path       string    `json:"path"`
	KeptAt     time.Time `json:"kept_at"`
	Bytes      int64     `json:"bytes"`
	Files      int64     `json:"files"`
	FromBox    string    `json:"from_box"`
	Status     string    `json:"status"` // keptStatusKept or keptStatusHeld
	HeldReason string    `json:"held_reason,omitempty"`
	Announced  bool      `json:"announced"`
}

// KeptRefusalRow is a folder that was not kept, for display only: Path is the
// name a box chose, never used as a path, and may name a level itself
// (".config" as a link).
type KeptRefusalRow struct {
	Path   string    `json:"path"`
	Reason string    `json:"reason"`
	Box    string    `json:"box"`
	SeenAt time.Time `json:"seen_at"`
}

// keptBoxRecord is .kept/.boxes/<box>: the store key of the box and the
// targets of the mounts CuliBox adds to it, home-relative, as ensure found them
// when it created the box (prepareKept). Profile, Workspace and Account are
// whose spawn that was, for the store's state (keptAttribute: believed only
// when they hash to Key); Refused are the kept folders that create left out, as
// not-kept rows (keptPass.mountRows). Those four are display data: a record is
// valid without them.
type keptBoxRecord struct {
	Key       string           `json:"key"`
	Mounts    []string         `json:"mounts,omitempty"`
	Profile   string           `json:"profile,omitempty"`
	Workspace string           `json:"workspace,omitempty"`
	Account   string           `json:"account,omitempty"`
	Refused   []KeptRefusalRow `json:"refused,omitempty"`
}

// KeptStoreDir is the store of key under stateRoot. key must pass
// ValidConversationsKey.
func KeptStoreDir(stateRoot, key string) string {
	return filepath.Join(stateRoot, stateDirName, keptDirName, key)
}

// ensureKeptStore creates key's store (.kept, <key>, home and clash) as 0700
// directories owned by the daemon user, and returns an os.Root on it. Each
// directory is made and opened from its parent's descriptor, so a link or a
// file where one belongs is refused and never followed, wherever it points
// (inside boxstate too). What is already in the store stays.
func ensureKeptStore(stateRoot, key string) (*os.Root, error) {
	if !ValidConversationsKey(key) {
		return nil, fmt.Errorf("box: kept store key %q is not 12 lowercase hex digits", key)
	}
	state := filepath.Join(stateRoot, stateDirName)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, fmt.Errorf("box: creating %q: %w", state, err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return nil, fmt.Errorf("box: opening %q: %w", state, err)
	}
	defer root.Close()
	rel := keptDirName + "/" + key
	d, st, err := keptMkdirPath(root, rel)
	if err != nil {
		return nil, fmt.Errorf("box: kept store %s: %w", rel, err)
	}
	defer d.Close()
	for _, sub := range []string{keptHomeDirName, keptClashDirName} {
		s, _, err := keptMkdirAt(d, rel, sub)
		if err != nil {
			return nil, fmt.Errorf("box: kept store %s: %w", rel+"/"+sub, err)
		}
		s.Close()
	}
	// os.Root follows a link in a middle component: check it opened the very
	// directory just walked.
	store, err := root.OpenRoot(rel)
	if err != nil {
		return nil, fmt.Errorf("box: opening kept store %s: %w", rel, err)
	}
	if !keptRootIs(store, st) {
		store.Close()
		return nil, fmt.Errorf("box: kept store %s changed while it was opened", rel)
	}
	return store, nil
}

// keptRootIs reports whether root is the directory st describes.
func keptRootIs(root *os.Root, st *unix.Stat_t) bool {
	d, err := openDirIn(root, ".")
	if err != nil {
		return false
	}
	defer d.Close()
	var got unix.Stat_t
	return unix.Fstat(int(d.Fd()), &got) == nil && got.Dev == st.Dev && got.Ino == st.Ino
}

// keptMkdirAt makes the directory name (0700) in the open directory parent,
// whose root-relative path is prel, unless it is there, and opens it. A link or
// anything but a directory there is refused, never followed.
func keptMkdirAt(parent *os.File, prel, name string) (*os.File, *unix.Stat_t, error) {
	pfd := int(parent.Fd())
	p := name
	if prel != "" && prel != "." {
		p = prel + "/" + name
	}
	if err := unix.Mkdirat(pfd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, nil, &fs.PathError{Op: "mkdir", Path: p, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstatat(pfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, nil, &fs.PathError{Op: "lstat", Path: p, Err: err}
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return nil, nil, &keptLinkError{rel: p}
	default:
		return nil, nil, fmt.Errorf("%s: %w", p, errKeptNotDir)
	}
	d, err := keptOpenAt(pfd, p, name, &st, unix.O_DIRECTORY)
	if err != nil {
		return nil, nil, err
	}
	return d, &st, nil
}

// keptMkdirPath is keptMkdirAt for each component of rel (clean, local) under
// root, from the root's own descriptor down: the last directory, open.
func keptMkdirPath(root *os.Root, rel string) (*os.File, *unix.Stat_t, error) {
	d, err := openDirIn(root, ".")
	if err != nil {
		return nil, nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(d.Fd()), &st); err != nil {
		d.Close()
		return nil, nil, err
	}
	if rel == "." || rel == "" {
		return d, &st, nil
	}
	if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		d.Close()
		return nil, nil, fmt.Errorf("box: %q is not a clean relative path", rel)
	}
	done := ""
	for _, c := range strings.Split(rel, "/") {
		next, nst, err := keptMkdirAt(d, done, c)
		d.Close()
		if err != nil {
			return nil, nil, err
		}
		d, st, done = next, *nst, path.Join(done, c)
	}
	return d, &st, nil
}

// keptReadFileAt reads the regular file name in the open directory d without
// following a link and without blocking. A missing file is fs.ErrNotExist;
// anything but a regular file, or one larger than limit, is an error.
func keptReadFileAt(d *os.File, name string, limit int64) ([]byte, error) {
	dfd := int(d.Fd())
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if st.Size > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	f, err := keptOpenAt(dfd, name, name, &st, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return b, nil
}

// keptWriteAtomic writes data as name in the open directory d, 0600: to a
// fresh temp name (O_EXCL, no link followed), synced, then renamed over name.
// A link at name is replaced, never followed. The temp file never stays.
func keptWriteAtomic(d *os.File, name string, data []byte) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	dfd := int(d.Fd())
	tmp := "." + name + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &fs.PathError{Op: "create", Path: tmp, Err: err}
	}
	f := os.NewFile(uintptr(fd), tmp)
	done := false
	defer func() {
		if !done {
			_ = unix.Unlinkat(dfd, tmp, 0)
		}
	}()
	err = f.Chmod(0o600) // whatever the umask
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := unix.Renameat(dfd, tmp, dfd, name); err != nil {
		return &fs.PathError{Op: "rename", Path: name, Err: err}
	}
	done = true
	return nil
}

// validKeptRel reports whether rel can name a kept folder, as anything read
// back from state.json must before it is mounted or opened: clean and local,
// at one of the four candidate levels (~/.<name>, ~/.config/<name>,
// ~/.local/share/<name>, ~/.local/state/<name>, never a level itself), passing
// keptNameOK, and none of the names discovery never keeps (CuliBox's own, the
// image's own,
// caches and toolchains, credential stores).
func validKeptRel(rel string) bool {
	if rel == "." || !keptNameOK(rel) || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return false
	}
	if keptFoldIn(rel, keptOwn) || keptFoldIn(rel, keptImageOwn) || keptFoldIn(rel, keptCacheNames) || keptFoldIn(rel, keptCredentialNames) {
		return false
	}
	for _, lvl := range keptLevels {
		if name, ok := strings.CutPrefix(rel, lvl+"/"); ok {
			return name != "" && !strings.Contains(name, "/")
		}
	}
	if !strings.HasPrefix(rel, ".") || strings.Contains(rel, "/") {
		return false
	}
	for _, lvl := range keptLevels {
		if top, _, _ := strings.Cut(lvl, "/"); strings.EqualFold(rel, top) {
			return false // .config, .local: their children are the candidates
		}
	}
	return true
}

// loadKeptState reads the store's state.json: an empty state when there is
// none. The file is not trusted: a link, a FIFO, a file over keptStateMax or
// broken JSON is an error, and what it holds is cleaned as cleanKeptState
// says (every kept or forgotten path that fails validKeptRel is dropped).
func loadKeptState(store *os.Root) (KeptState, error) {
	d, err := openDirIn(store, ".")
	if err != nil {
		return KeptState{}, fmt.Errorf("box: opening the kept store: %w", err)
	}
	defer d.Close()
	b, err := keptReadFileAt(d, keptStateName, keptStateMax)
	if errors.Is(err, fs.ErrNotExist) {
		return KeptState{}, nil
	}
	if err != nil {
		return KeptState{}, fmt.Errorf("box: reading the kept state: %w", err)
	}
	var st KeptState
	if err := json.Unmarshal(b, &st); err != nil {
		return KeptState{}, fmt.Errorf("box: reading the kept state: %w", err)
	}
	return cleanKeptState(st), nil
}

// saveKeptState writes st as the store's state.json, atomically and 0600. It
// is cleaned first, as loadKeptState cleans it, so the file never holds what a
// load would drop.
func saveKeptState(store *os.Root, st KeptState) error {
	b, err := json.MarshalIndent(cleanKeptState(st), "", "  ")
	if err != nil {
		return err
	}
	d, err := openDirIn(store, ".")
	if err != nil {
		return fmt.Errorf("box: opening the kept store: %w", err)
	}
	defer d.Close()
	if err := keptWriteAtomic(d, keptStateName, append(b, '\n')); err != nil {
		return fmt.Errorf("box: writing the kept state: %w", err)
	}
	return nil
}

// cleanKeptState keeps what a state may hold:
//   - kept entries whose Path passes validKeptRel and whose Status is kept or
//     held, the first per path, at most keptMaxPaths; negative counts are 0, a
//     FromBox that is not a box name is "", and HeldReason is shown escaped;
//   - not-kept rows with a Path, the latest per path, at most keptMaxNotKept
//     (the latest); they are display data, so a path is never dropped for its
//     level or its characters, only escaped (keptShow), and a Box that is not a
//     box name is "";
//   - forgotten paths that pass validKeptRel, once each.
//
// Order is kept. Profile, Workspace and Account are display data, as they are.
func cleanKeptState(st KeptState) KeptState {
	out := KeptState{Profile: st.Profile, Workspace: st.Workspace, Account: st.Account}
	seen := map[string]bool{}
	for _, e := range st.Kept {
		if len(out.Kept) == keptMaxPaths {
			break
		}
		if !validKeptRel(e.Path) || seen[e.Path] || (e.Status != keptStatusKept && e.Status != keptStatusHeld) {
			continue
		}
		seen[e.Path] = true
		e.Bytes, e.Files = max(e.Bytes, 0), max(e.Files, 0)
		if !isBoxName(e.FromBox) {
			e.FromBox = ""
		}
		e.HeldReason = keptShow(e.HeldReason)
		out.Kept = append(out.Kept, e)
	}
	out.NotKept = cleanKeptRows(st.NotKept)
	seen = map[string]bool{}
	for _, p := range st.Forgotten {
		if validKeptRel(p) && !seen[p] {
			seen[p] = true
			out.Forgotten = append(out.Forgotten, p)
		}
	}
	return out
}

// cleanKeptRows is cleanKeptState's not-kept part.
func cleanKeptRows(rows []KeptRefusalRow) []KeptRefusalRow {
	var clean []KeptRefusalRow
	for _, r := range rows {
		if r.Path == "" {
			continue
		}
		r.Path, r.Reason = keptShow(r.Path), keptShow(r.Reason)
		if !isBoxName(r.Box) {
			r.Box = ""
		}
		clean = append(clean, r)
	}
	latest := map[string]int{} // path → index in clean of its latest row
	for i, r := range clean {
		if j, ok := latest[r.Path]; !ok || !r.SeenAt.Before(clean[j].SeenAt) {
			latest[r.Path] = i
		}
	}
	var idx []int
	for i, r := range clean {
		if latest[r.Path] == i {
			idx = append(idx, i)
		}
	}
	if len(idx) > keptMaxNotKept {
		byAge := slices.Clone(idx)
		slices.SortStableFunc(byAge, func(a, b int) int { return clean[b].SeenAt.Compare(clean[a].SeenAt) })
		keep := map[int]bool{}
		for _, i := range byAge[:keptMaxNotKept] {
			keep[i] = true
		}
		idx = slices.DeleteFunc(idx, func(i int) bool { return !keep[i] })
	}
	var out []KeptRefusalRow
	for _, i := range idx {
		out = append(out, clean[i])
	}
	return out
}

// validKeptRecord reports whether r can be a box record: a store key, and
// mount targets that are clean, home-relative and docker-safe (they are only
// compared with candidates, never mounted from here).
func validKeptRecord(r keptBoxRecord) bool {
	if !ValidConversationsKey(r.Key) {
		return false
	}
	return !slices.ContainsFunc(r.Mounts, func(m string) bool {
		return m == "." || !filepath.IsLocal(m) || filepath.Clean(m) != m || MountUnsafe(boxHome+"/"+m) != ""
	})
}

// writeKeptBoxRecord writes box's record, atomically and 0600, creating .kept
// and .kept/.boxes (0700) on the way without following a link.
func writeKeptBoxRecord(stateRoot, box string, r keptBoxRecord) error {
	if !isBoxName(box) {
		return fmt.Errorf("box: %q is not a box name", box)
	}
	if !validKeptRecord(r) {
		return fmt.Errorf("box: %s: not a valid kept-folder record: %+v", box, r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	state := filepath.Join(stateRoot, stateDirName)
	if err := os.MkdirAll(state, 0o700); err != nil {
		return fmt.Errorf("box: creating %q: %w", state, err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return fmt.Errorf("box: opening %q: %w", state, err)
	}
	defer root.Close()
	d, _, err := keptMkdirPath(root, keptDirName+"/"+keptBoxesDirName)
	if err != nil {
		return fmt.Errorf("box: %s: kept-folder records: %w", box, err)
	}
	defer d.Close()
	if err := keptWriteAtomic(d, box, append(b, '\n')); err != nil {
		return fmt.Errorf("box: %s: writing its kept-folder record: %w", box, err)
	}
	return nil
}

// readKeptBoxRecord reads box's record: false when there is none, or when it
// is not a plain file reached without a link, too large, or not valid
// (validKeptRecord). Only a whole valid record counts: dropping one bad mount
// would let a candidate holding it be kept.
func readKeptBoxRecord(stateRoot, box string) (keptBoxRecord, bool) {
	if !isBoxName(box) {
		return keptBoxRecord{}, false
	}
	root, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if err != nil {
		return keptBoxRecord{}, false
	}
	defer root.Close()
	d, _, err := keptOpenPath(root, keptDirName+"/"+keptBoxesDirName)
	if err != nil {
		return keptBoxRecord{}, false
	}
	defer d.Close()
	b, err := keptReadFileAt(d, box, keptRecordMax)
	if err != nil {
		return keptBoxRecord{}, false
	}
	var r keptBoxRecord
	if json.Unmarshal(b, &r) != nil || !validKeptRecord(r) {
		return keptBoxRecord{}, false
	}
	return r, true
}

// keptRetries is .kept/.retries/<box>: Count is how many reclaim passes in a
// row kept box's home because a folder could not be screened in time, and
// Slow the folders whose screens ran out of their own time cap in those
// passes, which a pass at the cap lets go without screening them again.
type keptRetries struct {
	Count int      `json:"count"`
	Slow  []string `json:"slow,omitempty"`
}

// readKeptRetries reads box's count, reached without a link. The zero value
// when there is none, or when it is not a small plain file holding one; Slow
// keeps only names validKeptRel allows. On macOS the VM can write it: the
// worst a forged one does is let a slow folder go sooner or later than
// keptReclaimRetries passes.
func readKeptRetries(stateRoot, box string) keptRetries {
	if !isBoxName(box) {
		return keptRetries{}
	}
	root, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if err != nil {
		return keptRetries{}
	}
	defer root.Close()
	d, _, err := keptOpenPath(root, keptDirName+"/"+keptRetriesDirName)
	if err != nil {
		return keptRetries{}
	}
	defer d.Close()
	b, err := keptReadFileAt(d, box, keptRecordMax)
	if err != nil {
		return keptRetries{}
	}
	var r keptRetries
	if json.Unmarshal(b, &r) != nil || r.Count < 0 {
		return keptRetries{}
	}
	r.Slow = slices.DeleteFunc(r.Slow, func(rel string) bool { return !validKeptRel(rel) })
	return r
}

// writeKeptRetries writes box's count r, atomically and 0600, creating
// .kept/.retries (0700) on the way without following a link.
func writeKeptRetries(stateRoot, box string, r keptRetries) error {
	if !isBoxName(box) {
		return fmt.Errorf("box: %q is not a box name", box)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	state := filepath.Join(stateRoot, stateDirName)
	root, err := os.OpenRoot(state)
	if err != nil {
		return fmt.Errorf("box: opening %q: %w", state, err)
	}
	defer root.Close()
	d, _, err := keptMkdirPath(root, keptDirName+"/"+keptRetriesDirName)
	if err != nil {
		return fmt.Errorf("box: %s: kept-folder retry counts: %w", box, err)
	}
	defer d.Close()
	return keptWriteAtomic(d, box, append(b, '\n'))
}
