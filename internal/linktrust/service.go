package linktrust

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxPerActor bounds one actor's rows: it bounds the file and the list the
// page loads, and nobody trusts 200 hosts by hand.
const MaxPerActor = 200

// maxHostLen and maxLabelLen are the DNS limits (RFC 1035).
const (
	maxHostLen  = 253
	maxLabelLen = 63
)

// Errors Add and Remove return. ErrCorrupt (store.go) is the other one.
var (
	// ErrRefused: the host is not one a link may be trusted by; see Validate.
	ErrRefused = errors.New("that host cannot be trusted")
	// ErrCap: the actor's list is full (MaxPerActor).
	ErrCap = errors.New("too many trusted hosts")
	// ErrNotFound: Remove found no such host for the actor.
	ErrNotFound = errors.New("no such trusted host")
)

// AuditFunc records a change before it is saved. action is "created",
// "updated" or "deleted"; subdomains is the row's flag (for "deleted", the
// flag of the row removed). A non-nil error stops the change (fail closed).
// It is called with the service lock held; it must not call the Service.
type AuditFunc func(action, host string, subdomains bool, actor string) error

// Service is the audited trust list. Every read and write re-reads the file
// when another process changed it, so a hand edit is seen without a restart.
// A damaged file is never overwritten: every call then returns ErrCorrupt.
type Service struct {
	dataDir string
	audit   AuditFunc
	now     func() time.Time

	mu      sync.Mutex
	loaded  bool
	list    []Host // every actor's rows, in the order they were added
	corrupt error
	file    fileStamp // link-trust.json as last read or written; zero = none
}

// NewService returns a Service over dataDir. audit and now may be nil.
func NewService(dataDir string, audit AuditFunc, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{dataDir: dataDir, audit: audit, now: now}
}

// fileStamp identifies one version of link-trust.json.
type fileStamp struct {
	mtime time.Time
	size  int64
	ino   uint64
	ok    bool
}

func statFile(dataDir string) fileStamp {
	fi, err := os.Lstat(filepath.Join(dataDir, FileName))
	if err != nil {
		return fileStamp{}
	}
	st := fileStamp{mtime: fi.ModTime(), size: fi.Size(), ok: true}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st
}

// reload re-reads the file on first use and whenever its mtime, size or inode
// differ from what this service last read or wrote. The caller holds s.mu.
// A missing file is an empty list; a damaged one makes the service refuse
// every call until it changes.
func (s *Service) reload() {
	now := statFile(s.dataDir)
	if s.loaded && now == s.file {
		return
	}
	list, _, err := Load(s.dataDir)
	switch {
	case errors.Is(err, ErrCorrupt):
		s.list, s.corrupt = nil, err
	case err != nil:
		// Unreadable for now: keep what we had, and read again next time.
		if !s.loaded {
			s.list, s.corrupt, s.loaded = nil, nil, true
		}
		return
	default:
		s.list, s.corrupt = list, nil
	}
	s.loaded, s.file = true, now
}

// Validate reports why host cannot be trusted, or nil. A host is lowercase
// ASCII letters, digits and hyphens in dot-separated labels (no label starts
// or ends with a hyphen), at most 253 bytes in all and 1 to 63 per label, with
// at least two labels. It is not an IP address (v4 or v6, bracketed or not),
// not an IPv4 spelling a URL parser would read as a number (a last label of
// digits only or 0x...), and has no label starting xn-- (punycode), port,
// scheme, path, trailing dot or wildcard. The page sends URL.hostname, which a
// URL parser has already lowercased and punycoded, so this is a second check,
// not a normaliser: an input it would have to change is refused.
func Validate(host string) error {
	refuse := func(why string) error { return fmt.Errorf("%w: %s", ErrRefused, why) }
	if host == "" {
		return refuse("empty")
	}
	if len(host) > maxHostLen {
		return refuse("longer than 253 bytes")
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return refuse("an IP address")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return refuse("a single label")
	}
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen {
			return refuse("a label is empty or longer than 63 bytes")
		}
		if strings.HasPrefix(l, "xn--") {
			return refuse("a punycode label")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return refuse("a label starts or ends with a hyphen")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return refuse("only lowercase ASCII letters, digits and hyphens")
			}
		}
	}
	if endsInNumber(labels[len(labels)-1]) {
		return refuse("the last label is a number")
	}
	return nil
}

// endsInNumber: a last label a URL parser reads as part of an IPv4 address
// (digits only, or 0x followed by hex digits); no real top-level domain is one.
func endsInNumber(label string) bool {
	digits := func(s string, hex bool) bool {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if (c < '0' || c > '9') && !(hex && c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(label, "0x") {
		return digits(label[2:], true)
	}
	return digits(label, false)
}

func (s *Service) indexLocked(host, actor string) int {
	for i, h := range s.list {
		if h.Host == host && h.Actor == actor {
			return i
		}
	}
	return -1
}

// List is a copy of actor's trusted hosts, oldest first; other actors' rows are
// never in it. A damaged file returns nil, ErrCorrupt.
func (s *Service) List(actor string) ([]Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return nil, s.corrupt
	}
	out := []Host{}
	for _, h := range s.list {
		if h.Actor == actor {
			out = append(out, h)
		}
	}
	return out, nil
}

// Add trusts host for actor, and its subdomains when subdomains is set. It
// refuses, in order: a host Validate refuses (ErrRefused), a full list for the
// actor (ErrCap, unless the host is already there). A host already trusted with
// the same flag is a no-op: no audit, no write. With the other flag it is an
// update (audit "updated", Added kept). The audit is recorded before the save;
// if it fails, nothing is saved.
func (s *Service) Add(host string, subdomains bool, actor string) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return Host{}, s.corrupt
	}
	if err := Validate(host); err != nil {
		return Host{}, err
	}
	if i := s.indexLocked(host, actor); i >= 0 {
		cur := s.list[i]
		if cur.Subdomains == subdomains {
			return cur, nil
		}
		cur.Subdomains = subdomains
		next := append([]Host{}, s.list...)
		next[i] = cur
		if err := s.commitLocked("updated", cur, next); err != nil {
			return Host{}, err
		}
		return cur, nil
	}
	n := 0
	for _, h := range s.list {
		if h.Actor == actor {
			n++
		}
	}
	if n >= MaxPerActor {
		return Host{}, ErrCap
	}
	row := Host{Actor: actor, Host: host, Subdomains: subdomains, Added: s.now()}
	next := append(append(make([]Host, 0, len(s.list)+1), s.list...), row)
	if err := s.commitLocked("created", row, next); err != nil {
		return Host{}, err
	}
	return row, nil
}

// Remove stops trusting host for actor; another actor's row for the same host
// stays. A host that is not there is ErrNotFound. The audit is recorded before
// the save.
func (s *Service) Remove(host, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
	if s.corrupt != nil {
		return s.corrupt
	}
	idx := s.indexLocked(host, actor)
	if idx < 0 {
		return ErrNotFound
	}
	next := make([]Host, 0, len(s.list)-1)
	next = append(next, s.list[:idx]...)
	next = append(next, s.list[idx+1:]...)
	return s.commitLocked("deleted", s.list[idx], next)
}

// commitLocked audits, then saves next, then adopts it. A failed audit or
// save leaves the saved list as it was. The caller holds s.mu.
func (s *Service) commitLocked(action string, row Host, next []Host) error {
	if s.audit != nil {
		if err := s.audit(action, row.Host, row.Subdomains, row.Actor); err != nil {
			return err
		}
	}
	if err := Save(s.dataDir, next); err != nil {
		return err
	}
	s.list, s.file = next, statFile(s.dataDir)
	return nil
}
