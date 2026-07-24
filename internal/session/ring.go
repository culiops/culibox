package session

// ring is a bounded byte buffer that tracks absolute byte offsets. It retains
// the most recent `capacity` bytes; older bytes are evicted and `start`
// advances. Offsets are monotonic so a subscriber cursor can detect eviction
// (cursor < start => it fell behind and must resync).
type ring struct {
	buf   []byte
	start int64 // absolute offset of buf[0]
	cap   int
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{cap: capacity}
}

// end is the absolute offset one past the last byte.
func (r *ring) end() int64 { return r.start + int64(len(r.buf)) }

func (r *ring) append(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		drop := len(r.buf) - r.cap
		// Re-slice into a fresh backing array so the evicted prefix can be GC'd
		// and the backing array can't grow without bound.
		r.buf = append([]byte(nil), r.buf[drop:]...)
		r.start += int64(drop)
	}
}

// bytesFrom returns a copy of retained bytes from absolute `cursor` to end.
// A cursor before the retained window is clamped to start (the caller treats a
// clamp as a gap and forces a client resync). Returns nil when cursor >= end.
func (r *ring) bytesFrom(cursor int64) []byte {
	if cursor < r.start {
		cursor = r.start
	}
	off := cursor - r.start
	if off >= int64(len(r.buf)) {
		return nil
	}
	return append([]byte(nil), r.buf[off:]...)
}
