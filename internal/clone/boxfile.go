package clone

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

const (
	// maxHeadFile bounds .git/HEAD and a loose ref: both hold one line.
	maxHeadFile = 4 << 10
	// maxPackedRefs bounds .git/packed-refs, which grows with the ref count.
	maxPackedRefs = 8 << 20
)

// openBoxFile opens name under root for reading, for a file a box owns and so
// may have swapped for anything. O_NONBLOCK keeps a FIFO from blocking the
// open, O_NOFOLLOW refuses a symlink where the kernel is asked directly, and
// the fstat on the fd refuses everything that is not a regular file. Every
// host read of a box-owned file goes through here.
//
// os.Root.OpenFile still follows an in-root symlink on the last component even
// with O_NOFOLLOW (Go's root_unix.go retries through checkSymlink), so a
// caller that must refuse links also compares an Lstat, as readBoxFile does.
func openBoxFile(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", name)
	}
	return f, st, nil
}

// readBoxFile reads name under root, refusing anything but a regular file (not
// a symlink, not a FIFO, not a directory) of at most max bytes. The Lstat and
// the SameFile check are what refuse a symlink: see openBoxFile.
func readBoxFile(root *os.Root, name string, max int64) ([]byte, error) {
	lst, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, fst, err := openBoxFile(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !os.SameFile(lst, fst) {
		return nil, fmt.Errorf("%s changed while it was read", name)
	}
	if fst.Size() > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return data, nil
}

// ReadHead reports what the clone at cloneDir has checked out, read from its
// files with no git exec: branch is the branch HEAD names ("" when HEAD is
// detached), sha the commit it points at. ok is false when anything is
// missing, malformed, oversized or not a plain file; the clone is box-owned,
// so this is display data and never an input to a security decision.
func ReadHead(cloneDir string) (branch, sha string, ok bool) {
	root, err := os.OpenRoot(cloneDir)
	if err != nil {
		return "", "", false
	}
	defer root.Close()
	data, err := readBoxFile(root, ".git/HEAD", maxHeadFile)
	if err != nil {
		return "", "", false
	}
	head := strings.TrimSuffix(string(data), "\n")
	ref, symbolic := strings.CutPrefix(head, "ref: refs/heads/")
	if !symbolic {
		if !shaRE.MatchString(head) {
			return "", "", false
		}
		return "", head, true
	}
	if ValidBranch(ref) != nil {
		return "", "", false
	}
	sha, ok = branchSHA(root, ref)
	if !ok {
		return "", "", false
	}
	return ref, sha, true
}

// branchSHA resolves refs/heads/<branch> from the loose ref file, else from
// packed-refs. Only an absent loose ref falls through: one that exists but is
// not a plain file is a refusal, not a cue to look elsewhere.
func branchSHA(root *os.Root, branch string) (string, bool) {
	data, err := readBoxFile(root, ".git/refs/heads/"+branch, maxHeadFile)
	if err == nil {
		sha := strings.TrimSuffix(string(data), "\n")
		return sha, shaRE.MatchString(sha)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	packed, err := readBoxFile(root, ".git/packed-refs", maxPackedRefs)
	if err != nil {
		return "", false
	}
	want := "refs/heads/" + branch
	sc := bufio.NewScanner(bytes.NewReader(packed))
	sc.Buffer(nil, maxPackedRefs)
	for sc.Scan() {
		line := sc.Text() // "# pack-refs ..." header and "^<sha>" peeled lines never match
		if sha, name, found := strings.Cut(line, " "); found && name == want {
			return sha, shaRE.MatchString(sha)
		}
	}
	return "", false
}

// maxFingerprintEntries bounds the loose refs RefsFingerprint walks.
const maxFingerprintEntries = 2000

var errTooManyRefs = errors.New("too many refs to fingerprint")

// RefsFingerprint is a hex sha256 over the metadata (name, size, mode, mtime
// in ns) of .git/HEAD, .git/packed-refs, .git/index and every entry under
// .git/refs/heads of the clone at cloneDir: a cheap "did the refs move" test
// with no git exec and no file read. The three files are each optional (absent
// hashes as "absent"). Every entry is taken with Lstat semantics and a symlink
// is recorded, never followed, so a box cannot make the host look outside the
// clone. ok is false when cloneDir cannot be opened, .git is not a directory,
// or the walk passes 2000 entries.
//
// The clone is box-owned, so the box can forge any of this: the result is a
// scheduling hint (skip a return when nothing changed) and never a security
// input.
func RefsFingerprint(cloneDir string) (fp string, ok bool) {
	root, err := os.OpenRoot(cloneDir)
	if err != nil {
		return "", false
	}
	defer root.Close()
	if st, err := root.Lstat(".git"); err != nil || !st.IsDir() {
		return "", false
	}
	h := sha256.New()
	add := func(name string, st fs.FileInfo) {
		fmt.Fprintf(h, "%q %d %d %d\n", name, st.Size(), uint32(st.Mode()), st.ModTime().UnixNano())
	}
	for _, name := range []string{".git/HEAD", ".git/packed-refs", ".git/index"} {
		st, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(h, "%q absent\n", name)
			continue
		}
		if err != nil {
			return "", false
		}
		add(name, st)
	}
	const heads = ".git/refs/heads"
	st, err := root.Lstat(heads)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(h, "%q absent\n", heads)
	} else if err != nil {
		return "", false
	} else if !st.IsDir() {
		add(heads, st) // a symlink or file in its place: recorded, not walked
	} else {
		n := 0
		err := fs.WalkDir(root.FS(), heads, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if name == heads {
				return nil
			}
			if n++; n > maxFingerprintEntries {
				return errTooManyRefs
			}
			info, err := d.Info() // from the directory listing: Lstat, never the link target
			if err != nil {
				return err
			}
			add(name, info)
			return nil
		})
		if err != nil {
			return "", false
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}
