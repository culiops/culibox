package box

// Discovery and screen of the app folders a box wrote in its home (~/.culiops,
// ~/.config/<app>, ~/.local/share/<app>, ~/.local/state/<app>), which a later
// pass keeps for the profile. Reads only: nothing here moves or writes.
//
// Everything in a home is written by the box as the daemon's own uid, so it is
// hostile input: any name may be a symlink (out of the home or inside it), a
// FIFO, a device or a socket. Nothing is followed. Every component is Lstat'ed,
// a directory is opened with openDirIn and checked to be the one Lstat saw, and
// below a candidate every entry is read with fstatat(AT_SYMLINK_NOFOLLOW) and
// opened from its parent's descriptor with O_NOFOLLOW (os.Root would follow a
// link that stays inside the root). Only regular files are ever read, and only
// the first keptScreenHead bytes of each.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/culibox/internal/secretshape"
	"golang.org/x/sys/unix"
)

// keptOwn are the top-level home names CuliBox itself creates or mounts:
// .claude (Claude Code's state, the conversations mountpoint, the bundle and
// plugin mounts) and .agent-box (HomePidDir). The .claude.json family are
// files, so never candidates. TestOwnPathsCoverSeeded pins this list to what
// ensure seeds.
var keptOwn = []string{".claude", ".agent-box"}

// keptLevels are the parent directories whose children are candidates, beside
// the home's own top-level dot directories. They are never candidates.
var keptLevels = []string{".config", ".local/share", ".local/state"}

// keptCacheNames are caches and toolchains: skipped silently, never kept.
var keptCacheNames = []string{
	".cache", ".npm", "go", ".cargo", ".rustup", ".pyenv", ".nvm", ".bun", ".deno",
	".gradle", ".m2", ".terraform.d", ".vscode-server", ".local/share/pnpm",
	".local/share/virtualenvs",
}

// keptCredentialNames are known credential locations, browser profiles
// included: never kept, with the reason "credentials".
var keptCredentialNames = []string{
	".aws", ".ssh", ".gnupg", ".docker", ".kube", ".azure", ".oci", ".password-store", ".pki",
	".config/gcloud", ".config/gh", ".config/hub", ".config/op", ".config/doctl",
	".local/share/keyrings",
	".mozilla", ".config/chromium", ".config/google-chrome",
}

// Screen caps, per candidate. Variables so tests can lower them. Any cap hit
// means the folder is not kept.
var (
	keptScreenMaxEntries = 20000 // files, links and directories below the candidate
	keptScreenMaxDepth   = 8     // an entry directly in the candidate is depth 1
	keptScreenHead       = 64 << 10
	keptScreenTimeout    = 10 * time.Second
)

// Reasons a candidate is not kept, as they are shown.
const (
	keptReasonCredentials = "credentials"
	keptReasonLooksSecret = "looks like credentials: "
	keptReasonTooMany     = "too many files to check"
	keptReasonUnreadable  = "could not read"
	keptReasonLink        = "is a link"
	keptReasonMount       = "holds a file CuliBox mounts from the host"
	keptReasonName        = "name not allowed"
)

// keptOpenHook, when set (tests only), runs right before the screen opens an
// entry below a candidate, with its root-relative path.
var keptOpenHook func(rel string)

// keptCandidate is a home folder that may be kept: Rel is home-relative, and
// Dev and Ino are what Lstat saw, for the re-check before a move.
type keptCandidate struct {
	Rel      string
	Dev, Ino uint64
}

// keptRefusal is a home folder that is not kept, and why. Rel is the name as
// found, which may hold any byte the box chose: display it escaped.
type keptRefusal struct{ Rel, Reason string }

// keptNameOK reports whether rel can name a kept folder: docker --mount and
// the macOS VM's YAML can carry it (MountUnsafe on its in-box path), and no
// component holds a control character, ':', ',' or '"'.
func keptNameOK(rel string) bool {
	if rel == "" || MountUnsafe(boxHome+"/"+rel) != "" {
		return false
	}
	return !strings.ContainsFunc(rel, func(r rune) bool {
		return unicode.IsControl(r) || r == ':' || r == ',' || r == '"'
	})
}

// keptCandidates lists the folders of home that may be kept, and those that
// are not kept with a reason. Folders skipped silently (CuliBox's own, caches
// and toolchains, empty ones, those holding CACHEDIR.TAG, and those under a
// CuliBox mount) are in neither list.
//
// cbMounts are the targets of every mount CuliBox adds to the box, home-relative
// (an in-box path under /home/agent is read as its home-relative one; anything
// else is outside the home and ignored). A candidate that holds one is not
// kept; one that is a mount target or lies below one is CuliBox's own.
//
// Letter case is ignored in every name comparison, on every OS: on macOS the
// home is case-insensitive, so .Claude is CuliBox's .claude and .AWS is .aws.
// On Linux the cost is that a .Claude folder is not kept.
//
// The error is for a home whose top cannot be listed. Both lists are sorted by
// Rel.
func keptCandidates(home *os.Root, cbMounts []string) ([]keptCandidate, []keptRefusal, error) {
	s := keptScan{home: home, mounts: keptMountRels(cbMounts)}
	top, err := keptReadNames(home, ".", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("box: listing the home for folders to keep: %w", err)
	}
	for _, n := range top {
		if !strings.HasPrefix(n, ".") || keptFoldIn(n, []string{".config", ".local"}) {
			continue // only dot directories, and the levels are walked below
		}
		s.consider(n)
	}
	for _, lvl := range keptLevels {
		s.level(lvl)
	}
	slices.SortFunc(s.cands, func(a, b keptCandidate) int { return strings.Compare(a.Rel, b.Rel) })
	slices.SortFunc(s.refs, func(a, b keptRefusal) int { return strings.Compare(a.Rel, b.Rel) })
	return s.cands, s.refs, nil
}

type keptScan struct {
	home   *os.Root
	mounts []string
	cands  []keptCandidate
	refs   []keptRefusal
}

func (s *keptScan) refuse(rel, reason string) {
	r := keptRefusal{Rel: rel, Reason: reason}
	if !slices.Contains(s.refs, r) { // .local as a link refuses once, not per level
		s.refs = append(s.refs, r)
	}
}

// level considers every child of the level directory lvl. A level that is not
// there, or not a directory, holds nothing; one that is a link is refused
// without being followed; one under a CuliBox mount is CuliBox's.
func (s *keptScan) level(lvl string) {
	if keptMountCovers(lvl, s.mounts) {
		return
	}
	fi, err := keptLstatDirs(s.home, lvl)
	switch {
	case err == nil:
	case errors.Is(err, errKeptLink):
		s.refuse(keptLinkRel(lvl, err), keptReasonLink)
		return
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKeptNotDir):
		return
	default:
		s.refuse(lvl, keptReasonUnreadable)
		return
	}
	names, err := keptReadNames(s.home, lvl, fi)
	if err != nil {
		reason := keptReasonUnreadable
		if errors.Is(err, errKeptTooMany) {
			reason = keptReasonTooMany
		}
		s.refuse(lvl, reason)
		return
	}
	for _, n := range names {
		s.consider(lvl + "/" + n)
	}
}

// consider decides about one directory at a candidate level.
func (s *keptScan) consider(rel string) {
	if keptFoldIn(rel, keptOwn) || keptFoldIn(rel, keptCacheNames) {
		return
	}
	fi, err := s.home.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		s.refuse(rel, keptReasonUnreadable)
		return
	}
	link := fi.Mode()&fs.ModeSymlink != 0
	if !link && !fi.IsDir() {
		return // only directories count
	}
	cred := keptFoldIn(rel, keptCredentialNames)
	switch {
	case !keptNameOK(rel):
		s.refuse(rel, keptReasonName)
		return
	case link && cred:
		s.refuse(rel, keptReasonCredentials)
		return
	case link:
		// Docker resolves a linked bind source on the host: a kept link would
		// mount wherever it points, read-write.
		s.refuse(rel, keptReasonLink)
		return
	case keptMountCovers(rel, s.mounts):
		return // a CuliBox mountpoint, or below one: the box never wrote it
	case keptMountInside(rel, s.mounts):
		s.refuse(rel, keptReasonMount)
		return
	}
	d, err := keptOpenSame(s.home, rel, fi)
	if err != nil {
		s.refuse(rel, keptReasonUnreadable)
		return
	}
	defer d.Close()
	names, err := d.Readdirnames(1)
	if len(names) == 0 {
		if err != nil && !errors.Is(err, io.EOF) {
			s.refuse(rel, keptReasonUnreadable)
		}
		return // empty, or a mountpoint a kept folder left
	}
	if cred {
		s.refuse(rel, keptReasonCredentials)
		return
	}
	var st unix.Stat_t
	if unix.Fstatat(int(d.Fd()), "CACHEDIR.TAG", &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		st.Mode&unix.S_IFMT == unix.S_IFREG {
		return
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		s.refuse(rel, keptReasonUnreadable)
		return
	}
	s.cands = append(s.cands, keptCandidate{Rel: rel, Dev: uint64(sys.Dev), Ino: uint64(sys.Ino)})
}

// screenResult is what screenKept found: the size and file count (regular
// files and links) of a folder, and an empty Reason when it may be kept. On a
// refusal the counts are only what was seen before it.
type screenResult struct {
	Bytes, Files int64
	Reason       string
}

// screenKept screens the folder rel under root (a box home, or a store's home
// folder) for what looks like a credential. It fails closed: a cap hit, a read
// error, a FIFO, socket or device, or a link anywhere on the way to rel gives a
// Reason. Links below rel are counted and never followed.
//
// Rules: a file name in SecretFilePatterns (except config*.json, whose content is
// screened instead), and the content rules of secretshape on the first
// keptScreenHead bytes of every regular file.
func screenKept(ctx context.Context, root *os.Root, rel string) screenResult {
	ctx, cancel := context.WithTimeout(ctx, keptScreenTimeout)
	defer cancel()
	w := keptWalk{ctx: ctx, buf: make([]byte, keptScreenHead)}
	if rel == "." || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || ctx.Err() != nil {
		w.res.Reason = keptReasonUnreadable
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			w.res.Reason = keptReasonTooMany
		}
		return w.res
	}
	fi, err := keptLstatDirs(root, rel)
	if err != nil {
		w.res.Reason = keptReasonUnreadable
		if errors.Is(err, errKeptLink) {
			w.res.Reason = keptReasonLink
		}
		return w.res
	}
	d, err := keptOpenSame(root, rel, fi)
	if err != nil {
		w.res.Reason = keptReasonUnreadable
		return w.res
	}
	w.dir(d, rel, 0)
	d.Close()
	return w.res
}

type keptWalk struct {
	ctx     context.Context
	buf     []byte
	entries int
	res     screenResult
}

func (w *keptWalk) stop(reason string) {
	if w.res.Reason == "" {
		w.res.Reason = reason
	}
}

// dir screens the entries of the open directory d, found at rel (root-relative)
// and depth below the candidate. It does not close d.
func (w *keptWalk) dir(d *os.File, rel string, depth int) {
	dfd := int(d.Fd())
	for w.res.Reason == "" {
		names, err := d.Readdirnames(readDirChunk)
		for _, n := range names {
			if w.entry(dfd, rel, n, depth+1); w.res.Reason != "" {
				return
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			w.stop(keptReasonUnreadable)
			return
		}
	}
}

// entry screens the name n of the directory dfd at rel; n is at depth.
func (w *keptWalk) entry(dfd int, rel, n string, depth int) {
	if err := w.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			w.stop(keptReasonTooMany)
		} else {
			w.stop(keptReasonUnreadable)
		}
		return
	}
	if w.entries++; w.entries > keptScreenMaxEntries || depth > keptScreenMaxDepth {
		w.stop(keptReasonTooMany)
		return
	}
	p := rel + "/" + n
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		w.stop(keptReasonUnreadable)
		return
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		// Kept as it is: a rename never follows it, and neither does any reader.
		w.res.Files++
	case unix.S_IFDIR:
		f, err := keptOpenAt(dfd, p, n, &st, unix.O_DIRECTORY)
		if err != nil {
			w.stop(keptReasonUnreadable)
			return
		}
		w.dir(f, p, depth)
		f.Close()
	case unix.S_IFREG:
		w.res.Files++
		w.res.Bytes += st.Size
		if keptSecretName(n) {
			w.stop(keptReasonLooksSecret + keptShow(p))
			return
		}
		f, err := keptOpenAt(dfd, p, n, &st, 0)
		if err != nil {
			w.stop(keptReasonUnreadable)
			return
		}
		k, err := io.ReadFull(f, w.buf)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			w.stop(keptReasonUnreadable)
			return
		}
		if _, hit := secretshape.Find(w.buf[:k]); hit {
			w.stop(keptReasonLooksSecret + keptShow(p))
		}
	default:
		// A FIFO, socket or device: never opened, and not something to keep.
		w.stop(keptReasonUnreadable)
	}
}

// keptOpenAt opens n in the directory dfd without following a link and without
// blocking, and checks it is the very file st describes (a swap since the
// fstatat fails). p is its root-relative path, for the test hook.
func keptOpenAt(dfd int, p, n string, st *unix.Stat_t, flags int) (*os.File, error) {
	if keptOpenHook != nil {
		keptOpenHook(p)
	}
	fd, err := unix.Openat(dfd, n, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|flags, 0)
	if err != nil {
		return nil, err
	}
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil || got.Dev != st.Dev || got.Ino != st.Ino ||
		got.Mode&unix.S_IFMT != st.Mode&unix.S_IFMT {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: p, Err: unix.ESTALE}
	}
	return os.NewFile(uintptr(fd), p), nil
}

// keptSecretName is LooksSecret without config*.json: too many app folders have
// one, so its content is screened instead.
func keptSecretName(name string) bool {
	for _, pat := range SecretFilePatterns {
		if pat == "config*.json" {
			continue
		}
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// keptShow is p as it can be shown in a reason: as it is when it is valid UTF-8
// with no control or line-separator character, else quoted.
func keptShow(p string) string {
	if utf8.ValidString(p) && !strings.ContainsFunc(p, func(r rune) bool {
		return unicode.IsControl(r) || r == ' ' || r == ' '
	}) {
		return p
	}
	return strconv.QuoteToASCII(p)
}

var (
	errKeptLink    = errors.New("is a link")
	errKeptNotDir  = errors.New("not a directory")
	errKeptTooMany = errors.New("too many entries")
)

// keptLinkError names the component of a path that is a link.
type keptLinkError struct{ rel string }

func (e *keptLinkError) Error() string { return e.rel + " is a link" }
func (e *keptLinkError) Unwrap() error { return errKeptLink }

// keptLinkRel is the component a keptLstatDirs link error names, else rel.
func keptLinkRel(rel string, err error) string {
	var le *keptLinkError
	if errors.As(err, &le) {
		return le.rel
	}
	return rel
}

// keptLstatDirs checks with Lstat that every component of rel under root is a
// real directory, and returns the last one's info: a link anywhere is
// errKeptLink (naming it), anything else errKeptNotDir, a missing one
// fs.ErrNotExist.
func keptLstatDirs(root *os.Root, rel string) (fs.FileInfo, error) {
	var fi fs.FileInfo
	parts := strings.Split(rel, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		var err error
		if fi, err = root.Lstat(p); err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, &keptLinkError{rel: p}
		}
		if !fi.IsDir() {
			return nil, errKeptNotDir
		}
	}
	return fi, nil
}

// keptOpenSame opens the directory rel under root with openDirIn and checks it
// is the one fi (its Lstat) describes: os.Root follows a link that stays inside
// the root, so a swap since the Lstat fails here.
func keptOpenSame(root *os.Root, rel string, fi fs.FileInfo) (*os.File, error) {
	d, err := openDirIn(root, rel)
	if err != nil {
		return nil, err
	}
	got, err := d.Stat()
	if err != nil || !os.SameFile(fi, got) {
		d.Close()
		return nil, &fs.PathError{Op: "open", Path: rel, Err: unix.ESTALE}
	}
	return d, nil
}

// keptReadNames lists the directory rel under root, at most
// keptScreenMaxEntries names: "." (fi nil), or a directory that fi, from
// keptLstatDirs, describes.
func keptReadNames(root *os.Root, rel string, fi fs.FileInfo) ([]string, error) {
	var d *os.File
	var err error
	if fi == nil {
		d, err = openDirIn(root, rel)
	} else {
		d, err = keptOpenSame(root, rel, fi)
	}
	if err != nil {
		return nil, err
	}
	defer d.Close()
	var out []string
	for {
		names, err := d.Readdirnames(readDirChunk)
		out = append(out, names...)
		if len(out) > keptScreenMaxEntries {
			return nil, errKeptTooMany
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// keptMountRels turns mount targets into clean home-relative paths, dropping
// those outside the home and the home itself.
func keptMountRels(ms []string) []string {
	var out []string
	for _, m := range ms {
		m = filepath.ToSlash(filepath.Clean(m))
		if rest, ok := strings.CutPrefix(m, boxHome+"/"); ok {
			m = rest
		}
		if m == "." || !filepath.IsLocal(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// keptMountCovers reports whether rel is a mount target or lies below one.
func keptMountCovers(rel string, mounts []string) bool {
	return slices.ContainsFunc(mounts, func(m string) bool { return pathWithin(rel, m, true) })
}

// keptMountInside reports whether a mount target lies strictly below rel.
func keptMountInside(rel string, mounts []string) bool {
	return slices.ContainsFunc(mounts, func(m string) bool {
		return pathWithin(m, rel, true) && !pathWithin(rel, m, true)
	})
}

// keptFoldIn reports whether rel is one of names, ignoring letter case.
func keptFoldIn(rel string, names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(rel, n) })
}
