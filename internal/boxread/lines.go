package boxread

import (
	"bufio"
	"io"
)

// MaxLineBytes is the longest JSONL line the daemon will buffer. A box can
// create a sparse multi-GiB file with no newline; reading it with
// bufio.Reader.ReadBytes would buffer all of it.
const MaxLineBytes = 16 << 20

// LineReader reads newline-terminated lines with a hard cap on how much of one
// line is ever held in memory.
type LineReader struct {
	br  *bufio.Reader
	buf []byte
	max int
}

// NewLineReader returns a LineReader over r that buffers at most max bytes of
// any single line (the newline counts).
func NewLineReader(r io.Reader, max int) *LineReader {
	return &LineReader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// Next returns the next line, including its trailing newline when it has one.
//   - n is every byte consumed from the underlying reader for this line,
//     whether or not it was kept.
//   - over reports a line longer than the cap: line is nil, and the rest of it
//     was discarded through the next newline (or EOF) without being buffered.
//   - err is nil for a newline-terminated line, otherwise the error that ended
//     the line (io.EOF for an unterminated tail).
//
// The returned slice is only valid until the next call.
func (lr *LineReader) Next() (line []byte, n int, over bool, err error) {
	lr.buf = lr.buf[:0]
	for {
		frag, e := lr.br.ReadSlice('\n')
		n += len(frag)
		if !over {
			if len(lr.buf)+len(frag) > lr.max {
				over = true
			} else {
				lr.appendFrag(frag)
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if over {
			return nil, n, true, e
		}
		return lr.buf, n, false, e
	}
}

// appendFrag grows the buffer by doubling, so one maximal line allocates about
// twice the cap in total rather than the several times append's slow growth
// costs at large sizes.
func (lr *LineReader) appendFrag(frag []byte) {
	need := len(lr.buf) + len(frag)
	if need > cap(lr.buf) {
		nc := 2 * cap(lr.buf)
		if nc < 64<<10 {
			nc = 64 << 10
		}
		if nc < need {
			nc = need
		}
		if nc > lr.max {
			nc = lr.max
		}
		nb := make([]byte, len(lr.buf), nc)
		copy(nb, lr.buf)
		lr.buf = nb
	}
	lr.buf = append(lr.buf, frag...)
}
