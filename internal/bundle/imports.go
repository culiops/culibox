package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/culiops/agent-box/internal/box"
)

// importRe matches an @-import: "@" at line start or after whitespace, then a
// token running to the next unescaped whitespace (`\ ` is an escaped space).
var importRe = regexp.MustCompile(`(^|\s)@((?:\\ |\S)+)`)

// parseImports returns the @-import spellings in a CLAUDE.md, in document
// order, without the "@" and with "\ " unescaped. It skips fenced code blocks,
// inline code spans, HTML comments (which may span lines) and tokens that start
// with a quote, the way Claude Code does. It only extracts spellings; resolving
// them to files is the caller's job.
func parseImports(md []byte) []string {
	out := []string{}
	var (
		fenceChar byte // 0 when outside a fence
		fenceLen  int
		inComment bool
	)
	for _, line := range strings.Split(string(md), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !inComment {
			if c, n, rest, ok := fenceMarker(line); ok {
				switch {
				case fenceChar == 0:
					fenceChar, fenceLen = c, n
					continue
				case c == fenceChar && n >= fenceLen && strings.TrimSpace(rest) == "":
					fenceChar = 0
					continue
				}
			}
			if fenceChar != 0 {
				continue
			}
		}
		var clean string
		clean, inComment = stripCodeAndComments(line, inComment)
		for _, m := range importRe.FindAllStringSubmatch(clean, -1) {
			tok := strings.ReplaceAll(m[2], `\ `, " ")
			if tok[0] == '"' || tok[0] == '\'' {
				continue
			}
			out = append(out, tok)
		}
	}
	return out
}

// fenceMarker reports whether line opens or closes a fenced block: up to three
// spaces of indent, then three or more backticks or tildes. rest is the text
// after the marker run.
func fenceMarker(line string) (char byte, n int, rest string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0, "", false
	}
	c := t[0]
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	return c, n, t[n:], true
}

// stripCodeAndComments blanks inline code spans and HTML comments in one line,
// scanning left to right. inComment carries an open comment in from the
// previous line; the returned bool carries one out. An unmatched backtick run
// is literal text.
func stripCodeAndComments(line string, inComment bool) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(line); {
		if inComment {
			j := strings.Index(line[i:], "-->")
			if j < 0 {
				return b.String(), true
			}
			i += j + len("-->")
			inComment = false
			b.WriteByte(' ')
			continue
		}
		switch {
		case strings.HasPrefix(line[i:], "<!--"):
			inComment = true
			i += len("<!--")
		case line[i] == '`':
			n := 0
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			if end := closingRun(line, i+n, n); end >= 0 {
				i = end
				b.WriteByte(' ')
			} else {
				b.WriteString(line[i : i+n])
				i += n
			}
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String(), inComment
}

// closingRun finds a backtick run of exactly n starting at or after from, and
// returns the index just past it, or -1.
func closingRun(line string, from, n int) int {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		m := 0
		for i+m < len(line) && line[i+m] == '`' {
			m++
		}
		if m == n {
			return i + m
		}
		i += m
	}
	return -1
}

// KindImport is the Drop.Kind for a refused import.
const KindImport = "import"

// Caps on what a CLAUDE.md's imports can pull into a box. Hops count from the
// root CLAUDE.md: its own imports are hop 1, and a file at maxImportHops is
// bundled but its imports are not followed. maxImportFiles also bounds the
// refusal rows.
const (
	maxImportHops  = 4
	maxImportFiles = 64
	maxImportBytes = 256 << 10 // per file
	maxImportTotal = 4 << 20   // per set
)

// Refusal reasons, shown to the user as "<import> <reason>".
const (
	ReasonImportSymlinkEscape = "is a symlink that leads out of its folder; only files that stay in the folder they are imported from are given to the box"
	ReasonImportNotRegular    = "is not a regular file (a directory, socket, named pipe or device)"
	ReasonImportTooLarge      = "is larger than 256 KiB"
	ReasonImportTooMany       = "the imports exceed 64 files or 4 MiB; the rest are left out"
	ReasonImportDenied        = "is inside a directory that holds host sockets, keys or agent-box state"
	ReasonImportOutsideHome   = "is an absolute path outside your home directory"
	ReasonImportLeavesHome    = "resolves outside the box's home directory"
	ReasonImportReserved      = "would land on a path the box reserves"
	ReasonImportChanged       = "changed while it was read"
	ReasonImportConflict      = "resolves to the same in-box path as another import from a different file"
	ReasonImportNotFollowed   = "imports inside files outside your ~/.claude folder are not followed"
)

// reservedBoxFiles are in-box files the box itself provides (the bundle's
// CLAUDE.md mount, and the agent's own config and credentials). An import may
// never land on one. BoxBundleDir and everything under it is reserved too.
var reservedBoxFiles = []string{
	filepath.Join(boxHome, ".claude", "CLAUDE.md"),
	filepath.Join(boxHome, ".claude.json"),
	filepath.Join(boxHome, ".claude", "settings.json"),
	filepath.Join(boxHome, ".claude", ".credentials.json"),
}

// ImportFile is one file a CLAUDE.md imports, resolved and read. content is
// exactly the bytes that were hashed, so what is materialized is what was
// checked, never a second read of a file the box may have swapped since.
type ImportFile struct {
	Import   string   `json:"import"` // as written, without "@"
	Name     string   `json:"name"`   // filepath.Base(Box)
	Box      string   `json:"box"`    // absolute, clean in-box path
	Host     string   `json:"host"`   // resolved host file
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	Warnings []string `json:"warnings,omitempty"`
	content  []byte
}

// ImportRefusal is an import that exists (or was refused from its spelling
// alone) but is not given to the box. A missing import is not a refusal.
type ImportRefusal struct {
	Import string `json:"import"`
	Reason string `json:"reason"`
}

// ImportSet is everything a CLAUDE.md's imports resolve to.
type ImportSet struct {
	Files   []ImportFile    `json:"files"`
	Refused []ImportRefusal `json:"refused,omitempty"`
}

type importOpts struct {
	Source   string   // config dir holding CLAUDE.md
	HostHome string   // resolved host home
	Deny     []string // host dirs (or files) no import may come from
}

// importNode is the file whose imports are being resolved: where a relative
// import is joined on each side, whether it was itself mounted at its host path
// (an absolute import), and its hop count.
type importNode struct {
	hostDir, boxDir string
	absFrame        bool
	hop             int
}

// importIDs are the identities readImport's walk compares every directory
// (and the leaf) against. Identity, not spelling: on a case-insensitive
// filesystem ~/.SSH is ~/.ssh, and a deny dir reached through any alias is
// still the same directory.
type importIDs struct {
	deny         []fs.FileInfo
	home, source fs.FileInfo // nil when unresolved: never seen
}

// importSeen reports which anchors the walk passed through.
type importSeen struct{ home, source bool }

type importWalk struct {
	o        importOpts
	home     string // EvalSymlinks(HostHome), "" when unresolved
	denySpel []string
	ids      importIDs
	seen     map[string]string // in-box path -> host path it was mapped from
	refused  map[ImportRefusal]bool
	total    int64
	stopped  bool
	set      ImportSet
}

var (
	errImportChanged  = errors.New("bundle: import changed while it was read")
	errImportTooLarge = errors.New("bundle: import is too large")
	errImportDenied   = errors.New("bundle: import is in a denied directory")
)

// resolveImports resolves the @-imports of Source/CLAUDE.md, recursively, to
// host files and the in-box paths they are mounted at. Refusals are decided in
// three stages per spelling: from the spelling alone (so they show even for a
// file that does not exist), then against the other imports (one host file per
// in-box path), then on the filesystem, where a box may have planted links,
// FIFOs or devices in anything it can write. A missing file is skipped.
//
// Only files under Source have their own imports followed: a file elsewhere
// (a workspace the box can write, say) could otherwise gain an "@" line that
// pulls a host secret into the next box.
func resolveImports(o importOpts) ImportSet {
	w := &importWalk{
		o: o, seen: map[string]string{}, refused: map[ImportRefusal]bool{},
		set: ImportSet{Files: []ImportFile{}},
	}
	deny := slices.Clone(o.Deny)
	if o.HostHome != "" {
		if h, err := filepath.EvalSymlinks(o.HostHome); err == nil {
			w.home = h
			w.ids.home, _ = os.Stat(h)
			deny = append(deny, box.HostSocketDirs(h)...)
		}
		deny = append(deny, box.HostSocketDirs(o.HostHome)...)
	}
	for _, d := range deny {
		w.denySpel = append(w.denySpel, filepath.Clean(d))
		if fi, err := os.Stat(d); err == nil {
			w.ids.deny = append(w.ids.deny, fi)
		}
	}
	if s, err := filepath.EvalSymlinks(o.Source); err == nil {
		w.ids.source, _ = os.Stat(s)
	}
	// The root is read the way the bundle copies it (resolveIn): links
	// followed. It is the user's own config, but the read still never blocks
	// and never follows a link swapped in after the resolve.
	root, err := filepath.EvalSymlinks(filepath.Join(o.Source, "CLAUDE.md"))
	if err != nil {
		return w.set
	}
	md, _, _, err := readImport(root, maxImportTotal, nil)
	if err != nil {
		return w.set
	}
	w.walk(parseImports(md), importNode{hostDir: o.Source, boxDir: filepath.Join(boxHome, ".claude")})
	return w.set
}

// walk resolves spellings depth-first in document order: a file's own imports
// come right after it.
func (w *importWalk) walk(spellings []string, n importNode) {
	for _, sp := range spellings {
		if w.stopped {
			return
		}
		host, bx, absFrame, reason := w.mapImport(sp, n)
		if reason != "" {
			w.refuse(sp, reason)
			continue
		}
		if prev, ok := w.seen[bx]; ok {
			if prev != host {
				w.refuse(sp, ReasonImportConflict)
			}
			continue
		}
		w.seen[bx] = host
		f, underSource, reason := w.load(sp, host, bx)
		if reason != "" {
			w.refuse(sp, reason)
			continue
		}
		if f == nil {
			continue
		}
		if len(w.set.Files) >= maxImportFiles || w.total+f.Size > maxImportTotal {
			w.refuse(sp, ReasonImportTooMany)
			return
		}
		w.set.Files = append(w.set.Files, *f)
		w.total += f.Size
		hop := n.hop + 1
		if hop >= maxImportHops {
			continue
		}
		nested := parseImports(f.content)
		if !underSource {
			if len(nested) > 0 {
				w.refuse(sp, ReasonImportNotFollowed)
			}
			continue
		}
		w.walk(nested, importNode{
			hostDir: filepath.Dir(host), boxDir: filepath.Dir(bx), absFrame: absFrame, hop: hop,
		})
	}
}

// refuse records one refusal row, once per (import, reason). The rows are
// bounded like the files: at maxImportFiles rows the walk stops with one
// TooMany row, so a CLAUDE.md of a thousand bad spellings cannot grow the set
// (or the bundle lock it is written to) without limit.
func (w *importWalk) refuse(sp, reason string) {
	r := ImportRefusal{Import: sp, Reason: reason}
	if w.refused[r] {
		return
	}
	w.refused[r] = true
	w.set.Refused = append(w.set.Refused, r)
	if reason == ReasonImportTooMany {
		w.stopped = true
		return
	}
	if len(w.set.Refused) >= maxImportFiles {
		for _, x := range w.set.Refused {
			if x.Reason == ReasonImportTooMany {
				w.stopped = true
				return
			}
		}
		w.refuse(sp, ReasonImportTooMany)
	}
}

// mapImport maps a spelling to its host file and in-box path, and refuses it
// from the spelling alone. "~/X" is X under the home on each side. An absolute
// path is accepted only under the host home and is mounted at the same path. A
// relative one joins the importer's dir on each side and stays in the
// importer's frame: under the box home, or under the host home for a file that
// was itself imported by absolute path. With no host home, "~" and absolute
// imports are refused rather than resolved against the daemon's cwd.
func (w *importWalk) mapImport(sp string, n importNode) (host, bx string, absFrame bool, reason string) {
	switch {
	case sp == "~" || strings.HasPrefix(sp, "~/"):
		if w.o.HostHome == "" {
			return "", "", false, ReasonImportOutsideHome
		}
		rest := strings.TrimPrefix(sp, "~")
		bx = filepath.Join(boxHome, rest)
		host = filepath.Join(w.o.HostHome, rest)
		if !box.UnderMount(bx, []string{boxHome}) {
			return "", "", false, ReasonImportLeavesHome
		}
	case filepath.IsAbs(sp):
		bx = filepath.Clean(sp)
		host = bx
		absFrame = true
		if w.o.HostHome == "" || !box.UnderMount(bx, []string{w.o.HostHome}) {
			return "", "", false, ReasonImportOutsideHome
		}
	default:
		bx = filepath.Join(n.boxDir, sp)
		host = filepath.Join(n.hostDir, sp)
		absFrame = n.absFrame
		bound := boxHome
		if absFrame {
			bound = w.o.HostHome
		}
		if bound == "" || !box.UnderMount(bx, []string{bound}) {
			return "", "", false, ReasonImportLeavesHome
		}
	}
	if r := box.MountUnsafe(bx); r != "" {
		return "", "", false, r
	}
	if box.UnderMount(bx, []string{BoxBundleDir}) || slices.Contains(reservedBoxFiles, bx) {
		return "", "", false, ReasonImportReserved
	}
	return host, bx, absFrame, ""
}

// load runs the filesystem checks and reads the file. (nil, _, "") is a silent
// skip: the file, or a directory on the way to it, does not exist or cannot be
// read. underSource is true when the read passed through Source, which is what
// lets the file's own imports be followed.
func (w *importWalk) load(sp, host, bx string) (f *ImportFile, underSource bool, reason string) {
	realDir, err := filepath.EvalSymlinks(filepath.Dir(host))
	if err != nil {
		return nil, false, ""
	}
	// Resolved, the file must still sit under the resolved home: a directory
	// link out of home would hand the box files the spelling never named.
	if w.home == "" || !box.UnderMount(realDir, []string{w.home}) {
		return nil, false, ReasonImportOutsideHome
	}
	real := filepath.Join(realDir, filepath.Base(host))
	li, err := os.Lstat(real)
	if err != nil {
		return nil, false, ""
	}
	if li.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(real)
		if err != nil {
			return nil, false, ""
		}
		if !box.UnderMount(target, []string{realDir}) {
			return nil, false, ReasonImportSymlinkEscape
		}
		real = target
	}
	fi, err := os.Lstat(real)
	if err != nil {
		return nil, false, ""
	}
	// A fast spelling pass; readImport's walk decides by identity.
	if box.UnderMount(real, w.denySpel) {
		return nil, false, ReasonImportDenied
	}
	if !fi.Mode().IsRegular() {
		return nil, false, ReasonImportNotRegular
	}
	if fi.Size() > maxImportBytes {
		return nil, false, ReasonImportTooLarge
	}
	b, rfi, seen, err := readImport(real, maxImportBytes, &w.ids)
	switch {
	case errors.Is(err, errImportDenied):
		return nil, false, ReasonImportDenied
	case errors.Is(err, errImportTooLarge):
		return nil, false, ReasonImportTooLarge
	case errors.Is(err, errImportChanged):
		return nil, false, ReasonImportChanged
	case err != nil:
		return nil, false, ""
	case !os.SameFile(rfi, fi):
		return nil, false, ReasonImportChanged
	case !seen.home:
		return nil, false, ReasonImportOutsideHome
	}
	sum := sha256.Sum256(b)
	return &ImportFile{
		Import: sp, Name: filepath.Base(bx), Box: bx, Host: real,
		Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:]), content: b,
	}, seen.source, ""
}

// readImport reads the regular file at real, an absolute path with no links in
// it, and returns its bytes, the identity it was read from, and which of ids'
// anchors (home, source) it passed through. The path is walked from "/" one
// component at a time, each directory opened relative to the one before and
// checked to be the very directory Lstat saw, so a component swapped for a link
// after the caller resolved the path cannot redirect the read. Every directory
// opened, and the leaf, is compared by identity with ids.deny (errImportDenied)
// and the anchors. The open never blocks (O_NONBLOCK: a FIFO swapped in does
// not hang the daemon) and never follows a leaf link. ids may be nil.
func readImport(real string, limit int64, ids *importIDs) ([]byte, fs.FileInfo, importSeen, error) {
	var seen importSeen
	if ids == nil {
		ids = &importIDs{}
	}
	if !filepath.IsAbs(real) || filepath.Clean(real) != real || real == "/" {
		return nil, nil, seen, errImportChanged
	}
	cur, err := os.OpenRoot("/")
	if err != nil {
		return nil, nil, seen, err
	}
	defer func() { cur.Close() }()
	visit := func(fi fs.FileInfo) error {
		for _, d := range ids.deny {
			if os.SameFile(fi, d) {
				return errImportDenied
			}
		}
		if ids.home != nil && os.SameFile(fi, ids.home) {
			seen.home = true
		}
		if ids.source != nil && os.SameFile(fi, ids.source) {
			seen.source = true
		}
		return nil
	}
	st, err := cur.Stat(".")
	if err != nil {
		return nil, nil, seen, err
	}
	if err := visit(st); err != nil {
		return nil, nil, seen, err
	}
	parts := strings.Split(real[1:], "/")
	for _, c := range parts[:len(parts)-1] {
		di, err := cur.Lstat(c)
		if err != nil {
			return nil, nil, seen, err
		}
		if !di.IsDir() {
			return nil, nil, seen, errImportChanged
		}
		next, err := cur.OpenRoot(c)
		if err != nil {
			return nil, nil, seen, err
		}
		cur.Close()
		cur = next
		if st, err := cur.Stat("."); err != nil || !os.SameFile(st, di) {
			return nil, nil, seen, errImportChanged
		}
		if err := visit(di); err != nil {
			return nil, nil, seen, err
		}
	}
	leaf := parts[len(parts)-1]
	fi, err := cur.Lstat(leaf)
	if err != nil {
		return nil, nil, seen, err
	}
	if err := visit(fi); err != nil {
		return nil, nil, seen, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, seen, errImportChanged
	}
	if fi.Size() > limit {
		return nil, nil, seen, errImportTooLarge
	}
	f, err := cur.OpenFile(leaf, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, seen, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !os.SameFile(st, fi) || !st.Mode().IsRegular() {
		return nil, nil, seen, errImportChanged
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, nil, seen, err
	}
	if int64(len(b)) > limit {
		return nil, nil, seen, errImportTooLarge
	}
	if int64(len(b)) != fi.Size() {
		return nil, nil, seen, errImportChanged
	}
	return b, fi, seen, nil
}

// fingerprint identifies the set for the bundle hash: the in-box path and
// content of every file plus every refusal, independent of order.
func (s ImportSet) fingerprint() string {
	lines := make([]string, 0, len(s.Files)+len(s.Refused))
	for _, f := range s.Files {
		lines = append(lines, "file\x00"+f.Box+"\x00"+f.SHA256)
	}
	for _, r := range s.Refused {
		lines = append(lines, "refused\x00"+r.Import+"\x00"+r.Reason)
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		io.WriteString(h, l+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
