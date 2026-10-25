// Package logring keeps the daemon's most recent log lines in memory so a
// bug-report bundle can include them without reading any file.
package logring

import (
	"bytes"
	"sync"
	"unicode/utf8"
)

// ellipsis marks a line that was cut at maxLine.
const ellipsis = "…"

// Ring is an io.Writer holding the last N complete lines. It is bounded in
// both directions: N lines, and at most maxLine bytes per line, so a runaway
// writer cannot grow it.
type Ring struct {
	mu      sync.Mutex
	buf     []string // fixed size, used circularly
	next    int      // slot the next line goes into
	count   int      // lines held, <= len(buf)
	maxLine int

	pending   []byte // partial line, never longer than maxLine
	truncated bool   // pending overflowed; drop bytes until the next \n
}

// New returns a Ring keeping the last lines lines, each capped at maxLine
// bytes. Non-positive arguments are raised to 1 rather than panicking.
func New(lines, maxLine int) *Ring {
	if lines < 1 {
		lines = 1
	}
	if maxLine < 1 {
		maxLine = 1
	}
	return &Ring{buf: make([]string, lines), maxLine: maxLine}
}

// Write implements io.Writer. It never fails and always reports len(p), so a
// logger writing here (e.g. via io.MultiWriter) is never disturbed.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rest := p
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			r.accumulate(rest)
			break
		}
		r.accumulate(rest[:i])
		r.commit()
		rest = rest[i+1:]
	}
	return len(p), nil
}

// accumulate appends to the partial line, keeping only what fits in maxLine.
func (r *Ring) accumulate(b []byte) {
	if r.truncated {
		return
	}
	room := r.maxLine - len(r.pending)
	if len(b) > room {
		b = b[:room]
		r.truncated = true
	}
	r.pending = append(r.pending, b...)
}

// commit turns the partial line into a stored line and resets the buffer.
func (r *Ring) commit() {
	line := r.pending
	if r.truncated {
		// Don't leave half a rune before the marker. Bounded so binary
		// garbage can't make this scan the whole line.
		for n := 0; n < utf8.UTFMax && len(line) > 0 && !utf8.Valid(line); n++ {
			line = line[:len(line)-1]
		}
		r.buf[r.next] = string(line) + ellipsis
	} else {
		r.buf[r.next] = string(line)
	}
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	r.pending = r.pending[:0]
	r.truncated = false
}

// Lines returns the stored complete lines, oldest first, as a fresh slice.
// A line still being written is not included.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, r.count)
	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
