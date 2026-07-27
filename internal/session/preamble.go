package session

import "bytes"

// attachPreamble is the alternate-screen switch `abduco -a` writes as the very
// first bytes of an attach stream. It is abduco's, not the agent's: it leaves
// every session in xterm.js's alternate buffer, which has no scrollback — and a
// buffer with no scrollback makes xterm translate the mouse wheel into cursor
// keys, typing `^[[B` into whatever is reading (a terraform prompt, say).
const attachPreamble = "\x1b[?1049h"

// preambleTrimmer removes one attachPreamble from the start of a stream and
// then gets out of the way. Anchoring at offset 0 is what keeps an inner
// program's own alternate-screen switch (vim, less) working: that one arrives
// later in the stream, by which point the trimmer is done.
//
// The zero value is ready to use. Only the pump goroutine touches it.
type preambleTrimmer struct {
	done bool
	held []byte
}

// filter maps one PTY read to the bytes that should be appended to the ring. It
// returns no bytes while the stream so far is still a proper prefix of the
// preamble — a read can split those 8 bytes anywhere, so the decision has to
// wait for enough of them.
func (t *preambleTrimmer) filter(p []byte) []byte {
	if t.done {
		return p
	}
	t.held = append(t.held, p...)
	if len(t.held) < len(attachPreamble) {
		if bytes.HasPrefix([]byte(attachPreamble), t.held) {
			return nil
		}
		return t.take() // diverged early: this stream has no preamble
	}
	out := t.take()
	if bytes.HasPrefix(out, []byte(attachPreamble)) {
		return out[len(attachPreamble):]
	}
	return out
}

// flush returns the bytes still held when the stream ends, so a session that
// dies mid-preamble loses nothing.
func (t *preambleTrimmer) flush() []byte {
	if t.done {
		return nil
	}
	return t.take()
}

// take marks the trimmer finished and hands over whatever it was holding.
func (t *preambleTrimmer) take() []byte {
	t.done = true
	out := t.held
	t.held = nil
	return out
}
