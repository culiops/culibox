package clone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/handback"
)

// What a clone box's branch brings home is inspected here, in clone_of, after
// the fetch checked it. The scan reads blobs, never working files: nothing is
// checked out, executed, decompressed or parsed, and every git call is the
// hardened one with no replace refs and the program from gitexec.go.

const (
	maxRawToken     = 1 << 20 // one NUL-terminated token of the raw log (a path, or a meta line)
	maxBatchHeader  = 512     // one "<id> <type> <size>" line of cat-file --batch
	symlinkOutByGit = "symlink points outside the repository"
)

// blobIDRE is a full object id: SHA-1 (40) or SHA-256 (64) hex.
var blobIDRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// errStop ends a stream on purpose: the reader has what it needs.
var errStop = errors.New("stream stopped by the reader")

// branchEntry is one file a commit of the branch added or changed.
type branchEntry struct {
	path string
	id   string
	mode fs.FileMode // 0o644, 0o755 or fs.ModeSymlink
}

// blobInfo is what the scan knows about one blob. head is the first HeadBytes
// (or the whole blob when smaller); read says head is complete.
type blobInfo struct {
	size     int64
	known    bool // cat-file --batch-check found it
	head     []byte
	sum      string
	note     string // why sum is empty, when it is
	read     bool
	linkOnly bool // every entry using it is a symlink: its text is a target, not content to hash
}

// InspectBranch flags what the commits base..ref added or changed in the repo
// at of: executables, disguised files, archives and outward symlinks, judged
// from each blob's first bytes. It looks at every commit of the range, not
// only the tip, so a file added then deleted inside the branch is still there:
// a push would publish it. Host history is excluded by the range, so a change
// the host made after the box forked is not the box's.
//
// It never returns an error and never fails the caller. Whatever it could not
// do shows in Truncated: a git failure ("could not inspect: ..."), a cap, a
// file that could not be read, the deadline. g must be a StreamGit, else the
// report says "inspection unavailable". When the flag cap is full the
// strongest flags are kept (handback.SortFlags order), as in handback.InspectDir.
//
// Every blob is read once. Blobs that fit the per-file and scan-wide hash caps
// go through one cat-file --batch and are hashed on the way; a blob over the
// budget is still classified, from its head, with a note; a blob over the
// per-file cap is read by its own cat-file that stops after the head, so a
// 100 MiB blob costs 8 KiB of reading. One blob at two paths is classified at
// each path, because the extension is part of the verdict.
func InspectBranch(ctx context.Context, g Git, of, base, ref string, lim handback.Limits) handback.Report {
	rep := handback.Report{Flags: []handback.Flag{}}
	sg, ok := g.(StreamGit)
	if !ok {
		rep.Truncated = "inspection unavailable"
		return rep
	}
	lim = lim.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Budget)
	defer cancel()
	s := &branchScan{ctx: ctx, sg: sg, of: of, lim: lim, blobs: map[string]*blobInfo{}, hashLeft: lim.HashTotal,
		flags: handback.NewFlagSet(lim.MaxFlags)}
	s.run(base, ref)
	rep.Inspected = s.inspected
	rep.Flags = s.flags.Sorted()
	if d := s.flags.Dropped(); d > 0 {
		s.reasons = append(s.reasons, fmt.Sprintf("flag cap of %d reached; %d more flagged files are not listed", lim.MaxFlags, d))
	}
	rep.Truncated = strings.Join(s.reasons, "; ")
	return rep
}

type branchScan struct {
	ctx      context.Context
	sg       StreamGit
	of       string
	lim      handback.Limits
	hashLeft int64

	entries []branchEntry
	blobs   map[string]*blobInfo
	order   []string // blob ids, first seen first (the newest commit's)

	flags      *handback.FlagSet
	reasons    []string
	inspected  int
	unreadable int
}

// stream runs one hardened host git in of, handing stdout to w.
func (s *branchScan) stream(stdin io.Reader, w io.Writer, args ...string) error {
	err := s.sg.Stream(s.ctx, s.of, noReplaceEnv(), stdin, w, hardened(args...)...)
	if err == nil || errors.Is(err, errStop) {
		return err
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	// git's own text can name refs and paths a box wrote.
	return errors.New(sanitizeN(err.Error(), maxLongErr))
}

// stopped reports whether the scan ran out of time or was cancelled, and says so once.
func (s *branchScan) stopped() bool {
	if s.ctx.Err() == nil {
		return false
	}
	const why = "ran out of time or was cancelled before it finished"
	if len(s.reasons) == 0 || s.reasons[len(s.reasons)-1] != why {
		s.reasons = append(s.reasons, why)
	}
	return true
}

func (s *branchScan) run(base, ref string) {
	if err := s.collect(base, ref); err != nil {
		if !s.stopped() {
			s.reasons = append(s.reasons, "could not inspect: "+err.Error())
		}
		return
	}
	if len(s.entries) == 0 {
		return
	}
	if err := s.sizes(); err != nil {
		if !s.stopped() {
			s.reasons = append(s.reasons, "could not inspect: "+err.Error())
		}
		return
	}
	s.readBlobs()
	s.classify()
	if s.unreadable > 0 {
		s.reasons = append(s.reasons, fmt.Sprintf("%d files could not be read", s.unreadable))
	}
}

// collect lists the files the range added, modified or changed the type of,
// newest commit first, as (path, blob, mode). A deleted file is not a file the
// branch hands back, but its earlier versions are listed by the commits that
// added them. No rename detection and no external diff or text conversion.
func (s *branchScan) collect(base, ref string) error {
	seen := map[string]bool{}
	type meta struct {
		id   string
		mode fs.FileMode
	}
	var pending *meta
	capped := false
	split := &nulSplitter{max: maxRawToken, fn: func(tok []byte) error {
		if pending != nil {
			m := *pending
			pending = nil
			key := string(tok) + "\x00" + m.id + "\x00" + strconv.Itoa(int(m.mode))
			if seen[key] {
				return nil
			}
			if len(s.entries) >= s.lim.Candidates {
				capped = true
				return errStop
			}
			seen[key] = true
			s.entries = append(s.entries, branchEntry{path: string(tok), id: m.id, mode: m.mode})
			if s.blobs[m.id] == nil {
				s.blobs[m.id] = &blobInfo{linkOnly: true}
				s.order = append(s.order, m.id)
			}
			if m.mode != fs.ModeSymlink {
				s.blobs[m.id].linkOnly = false
			}
			return nil
		}
		if len(tok) == 0 || tok[0] != ':' {
			return nil
		}
		// ":<old mode> <new mode> <old id> <new id> <status>", then the path.
		f := strings.Fields(string(tok[1:]))
		if len(f) != 5 || !blobIDRE.MatchString(f[3]) || !strings.ContainsAny(f[4][:1], "AMT") {
			return nil // a deletion, a submodule, a tree, or a line this does not know
		}
		var mode fs.FileMode
		switch f[1] {
		case "100644":
			mode = 0o644
		case "100755":
			mode = 0o755
		case "120000":
			mode = fs.ModeSymlink
		default:
			return nil
		}
		pending = &meta{id: f[3], mode: mode}
		return nil
	}}
	err := s.stream(nil, split, "log", "--no-ext-diff", "--no-textconv", "--no-renames", "--format=", "--raw", "-r",
		"--no-abbrev", "-z", "--diff-merges=first-parent", base+".."+ref, "--")
	if capped && errors.Is(err, errStop) {
		s.reasons = append(s.reasons, fmt.Sprintf("candidate cap of %d files reached; the older files of the branch were not inspected", s.lim.Candidates))
		return nil
	}
	if err == nil {
		err = split.close()
	}
	return err
}

// sizes asks, in one cat-file --batch-check, how big every blob is.
func (s *branchScan) sizes() error {
	ids := strings.Join(s.order, "\n") + "\n"
	out := &capBuffer{max: maxGitStdout}
	if err := s.stream(strings.NewReader(ids), out, "cat-file", "--batch-check"); err != nil {
		return err
	}
	if out.over {
		return errors.New("cat-file --batch-check: output too large")
	}
	for _, line := range strings.Split(out.buf.String(), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			continue // "<id> missing", or an object that is not a blob
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if bi := s.blobs[f[0]]; bi != nil && err == nil && n >= 0 {
			bi.known, bi.size = true, n
		}
	}
	return nil
}

// readBlobs reads the head of every known blob, and hashes what the caps allow.
func (s *branchScan) readBlobs() {
	var hashed, headOnly, big []string
	for _, id := range s.order {
		bi := s.blobs[id]
		switch {
		case !bi.known:
		case bi.size == 0:
			sum := sha256.Sum256(nil)
			bi.read, bi.sum = true, hex.EncodeToString(sum[:])
		case bi.size > s.lim.HashPerFile:
			if !bi.linkOnly {
				bi.note = handback.NoteTooLarge
			}
			big = append(big, id)
		case bi.linkOnly:
			headOnly = append(headOnly, id)
		case bi.size <= s.hashLeft:
			s.hashLeft -= bi.size
			hashed = append(hashed, id)
		default:
			bi.note = handback.NoteBudget
			headOnly = append(headOnly, id)
		}
	}
	s.batch(hashed, true)
	s.batch(headOnly, false)
	for _, id := range big {
		if s.stopped() {
			break
		}
		s.bigHead(id)
	}
	// A hash that could not be made says why, so a flagged blob never has
	// neither a sum nor a note.
	for _, id := range hashed {
		if bi := s.blobs[id]; !bi.read || bi.sum == "" {
			bi.note = handback.NoteUnread
			if s.ctx.Err() != nil {
				bi.note = handback.NoteCancel
			}
		}
	}
}

// batch reads ids through one cat-file --batch, keeping each head and, when
// hash is set, hashing every byte. A blob the stream did not finish stays unread.
func (s *branchScan) batch(ids []string, withSum bool) {
	if len(ids) == 0 || s.stopped() {
		return
	}
	var cur *blobInfo
	var h hash.Hash
	var n int64
	p := &batchParser{
		start: func(id string, size int64) {
			cur, n = s.blobs[id], 0
			cur.head = make([]byte, 0, min(size, int64(handback.HeadBytes)))
			h = nil
			if withSum {
				h = sha256.New()
			}
		},
		data: func(b []byte) {
			if room := handback.HeadBytes - len(cur.head); room > 0 {
				cur.head = append(cur.head, b[:min(room, len(b))]...)
			}
			if h != nil {
				h.Write(b)
			}
			n += int64(len(b))
		},
		end: func(id string) {
			if n == cur.size {
				cur.read = true
				if h != nil {
					cur.sum = hex.EncodeToString(h.Sum(nil))
				}
			}
		},
		missing: func(string) {},
	}
	if err := s.stream(strings.NewReader(strings.Join(ids, "\n")+"\n"), p, "cat-file", "--batch"); err != nil && !s.stopped() {
		s.reasons = append(s.reasons, "could not read some files: "+err.Error())
	}
}

// bigHead reads only the first HeadBytes of a blob over the per-file cap: the
// writer stops git once it has them.
func (s *branchScan) bigHead(id string) {
	bi := s.blobs[id]
	var head []byte
	w := writerFunc(func(p []byte) (int, error) {
		head = append(head, p[:min(len(p), handback.HeadBytes-len(head))]...)
		if len(head) >= handback.HeadBytes {
			return len(p), errStop
		}
		return len(p), nil
	})
	err := s.stream(nil, w, "cat-file", "blob", id)
	if err == nil || errors.Is(err, errStop) {
		bi.head, bi.read = head, true
		return
	}
	if !s.stopped() {
		s.reasons = append(s.reasons, "could not read some files: "+err.Error())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// classify turns every listed file into a flag, or into a count of files that
// could not be read. It runs per entry, not per blob: one blob under two names
// is two verdicts.
func (s *branchScan) classify() {
	for _, e := range s.entries {
		bi := s.blobs[e.id]
		if bi == nil || !bi.read {
			s.unreadable++
			continue
		}
		s.inspected++
		if e.mode == fs.ModeSymlink {
			target, _, _ := strings.Cut(string(bi.head), "\x00")
			if handback.OutsideRoot(e.path, target) {
				s.flags.Offer(handback.Flag{
					Path:    handback.SanitizePath(e.path),
					Classes: []handback.Class{handback.ClassSymlinkOut},
					Detail:  symlinkOutByGit,
					Target:  handback.SanitizePath(target),
				}, nil)
			}
			continue
		}
		classes, detail := handback.Classify(path.Base(e.path), e.mode, bi.head)
		if len(classes) == 0 {
			continue
		}
		s.flags.Offer(handback.Flag{
			Path:     handback.SanitizePath(e.path),
			Classes:  classes,
			Detail:   handback.SanitizePath(detail),
			SHA256:   bi.sum,
			HashNote: bi.note,
		}, nil)
	}
}

// nulSplitter is a writer that calls fn for each NUL-terminated token written
// to it, across any split of the writes. A token over max bytes is an error.
type nulSplitter struct {
	max int
	fn  func(tok []byte) error
	buf []byte
}

func (n *nulSplitter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0)
		if i < 0 {
			if len(n.buf)+len(p) > n.max {
				return 0, errors.New("git log: a path is longer than the scan accepts")
			}
			n.buf = append(n.buf, p...)
			break
		}
		tok := p[:i]
		if len(n.buf) > 0 {
			if len(n.buf)+i > n.max {
				return 0, errors.New("git log: a path is longer than the scan accepts")
			}
			n.buf = append(n.buf, tok...)
			tok = n.buf
		}
		err := n.fn(tok)
		n.buf = n.buf[:0]
		if err != nil {
			return 0, err
		}
		p = p[i+1:]
	}
	return total, nil
}

// close reports a token cut off before its terminating NUL.
func (n *nulSplitter) close() error {
	if len(n.buf) > 0 {
		return errors.New("git log: output ended inside a token")
	}
	return nil
}

// batchParser reads the output of `git cat-file --batch`: for each requested
// object "<id> <type> <size>\n", the bytes, then "\n"; or "<id> missing\n". It
// is a writer, so the blobs are handed on as they arrive and never held whole.
type batchParser struct {
	start   func(id string, size int64)
	data    func(b []byte)
	end     func(id string)
	missing func(id string)

	header []byte
	id     string
	left   int64
	inBody bool
	skip   bool // a body that is not a blob: consumed, not reported
	wantNL bool
}

func (b *batchParser) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		switch {
		case b.inBody:
			n := int(min(int64(len(p)), b.left))
			if !b.skip && n > 0 {
				b.data(p[:n])
			}
			b.left -= int64(n)
			p = p[n:]
			if b.left == 0 {
				b.inBody, b.wantNL = false, true
				if !b.skip {
					b.end(b.id)
				}
			}
		case b.wantNL:
			if p[0] != '\n' {
				return 0, errors.New("git cat-file --batch: no newline after an object")
			}
			p, b.wantNL = p[1:], false
		default:
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				if len(b.header)+len(p) > maxBatchHeader {
					return 0, errors.New("git cat-file --batch: header line too long")
				}
				b.header = append(b.header, p...)
				p = nil
				break
			}
			if len(b.header)+i > maxBatchHeader {
				return 0, errors.New("git cat-file --batch: header line too long")
			}
			line := string(append(b.header, p[:i]...))
			b.header, p = b.header[:0], p[i+1:]
			f := strings.Fields(line)
			switch {
			case len(f) == 2 && f[1] == "missing":
				b.missing(f[0])
			case len(f) == 3:
				size, err := strconv.ParseInt(f[2], 10, 64)
				if err != nil || size < 0 {
					return 0, errors.New("git cat-file --batch: bad size")
				}
				b.id, b.left, b.inBody, b.skip = f[0], size, true, f[1] != "blob"
				if b.skip {
					b.missing(f[0])
				} else {
					b.start(f[0], size)
				}
				if size == 0 {
					b.inBody, b.wantNL = false, true
					if !b.skip {
						b.end(b.id)
					}
				}
			default:
				return 0, errors.New("git cat-file --batch: unexpected line")
			}
		}
	}
	return total, nil
}
