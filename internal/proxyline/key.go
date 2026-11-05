package proxyline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Key is the secret CuliBox's own relay presents, ahead of the PROXY line, to
// the host listeners it dials. A listener that wants a key trusts a PROXY line
// only from a connection that began with it, so another process on the Mac
// cannot claim a box's address just by reaching the loopback port.
//
// The key is 32 random bytes. Only Hex and Preamble yield the material. Format
// writes the redacted ID (String, or GoString for %#v) for every verb, so a Key
// that is the operand itself, or that sits in an exported struct field, slice
// element or map value, never prints its bytes under %v, %s, %q, %x, %d, %o, %b
// or %c. fmt cannot call a method on an unexported struct field, so a Key held in
// one is printed as its raw bytes: never format a struct that keeps a Key in an
// unexported field.
type Key [32]byte

// PreambleLen is the length of Key.Preamble: the prefix, 64 hex characters and
// CRLF.
const PreambleLen = 81

const preamblePrefix = "AGENTBOX-RELAY "

// KeyFromBytes returns the key held by b. It errors unless b is exactly 32
// bytes and not all zero (the zero Key is the "no key" value, which listeners
// refuse).
func KeyFromBytes(b []byte) (Key, error) {
	var k Key
	if len(b) != len(k) {
		return Key{}, errors.New("proxyline: relay key must be 32 bytes")
	}
	copy(k[:], b)
	if k.IsZero() {
		return Key{}, errors.New("proxyline: relay key must not be all zero")
	}
	return k, nil
}

// IsZero reports whether k is the zero Key, which means "no key".
func (k Key) IsZero() bool { return k == Key{} }

// Hex returns the key as 64 lowercase hex characters: the value the relay
// receives in its environment.
func (k Key) Hex() string { return hex.EncodeToString(k[:]) }

// Preamble returns the 81 bytes the relay writes before the PROXY line:
// "AGENTBOX-RELAY <64 hex>\r\n".
func (k Key) Preamble() []byte {
	return []byte(preamblePrefix + k.Hex() + "\r\n")
}

// ID returns a short, domain-separated fingerprint of the key: the first 16
// hex characters of sha256("culibox relay key id\x00" || key). It identifies
// which key a relay was started with without revealing it, so it is safe in a
// label, a config hash or a log line.
func (k Key) ID() string {
	h := sha256.New()
	h.Write([]byte("culibox relay key id\x00"))
	h.Write(k[:])
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// String redacts the key. Printing a Key with fmt goes through Format, which
// uses it.
func (k Key) String() string { return "proxyline.Key(id=" + k.ID() + ")" }

// GoString redacts the key for %#v.
func (k Key) GoString() string { return k.String() }

// Format makes every fmt verb print the redacted ID. Without it fmt would only
// call String for the verbs that print text, and %d, %o, %b and %c would spell
// out the raw bytes.
func (k Key) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		io.WriteString(f, k.GoString())
		return
	}
	io.WriteString(f, k.String())
}

// RefusalLog counts connections a listener refused for lacking the relay key
// and logs a summary at most once per interval. A refused connection is closed
// silently (no audit row: it has no session), so this one line is how a
// probing local process, or a relay still holding an old key, becomes visible.
// It never sees what the refused connection sent.
type RefusalLog struct {
	listener string
	every    time.Duration
	now      func() time.Time
	logf     func(format string, args ...any)

	mu    sync.Mutex
	count int       // refusals not yet reported
	since time.Time // when the oldest unreported refusal happened
	last  time.Time // when the last line was logged; zero before the first
}

// NewRefusalLog returns a log for the named listener that writes at most one
// line per every through logf, reading the time from now.
func NewRefusalLog(listener string, every time.Duration, now func() time.Time, logf func(format string, args ...any)) *RefusalLog {
	return &RefusalLog{listener: listener, every: every, now: now, logf: logf}
}

// Refused counts one refusal. It logs when it is the first refusal or when the
// interval has passed since the last line; the count then starts again at 0.
func (r *RefusalLog) Refused() {
	t := r.now()
	r.mu.Lock()
	if r.count == 0 {
		r.since = t
	}
	r.count++
	if !r.last.IsZero() && t.Sub(r.last) < r.every {
		r.mu.Unlock()
		return
	}
	n, since := r.count, r.since
	r.count = 0
	r.last = t
	r.mu.Unlock()
	r.logf("%s: refused %d connection(s) without CuliBox's relay key since %s (a process on this Mac that is not the relay, or a relay started with an old key)",
		r.listener, n, since.Format("15:04:05"))
}
