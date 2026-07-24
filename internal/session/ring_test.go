package session

import "testing"

func TestRing_AppendAndRead(t *testing.T) {
	r := newRing(1024)
	r.append([]byte("hello"))
	if got := string(r.bytesFrom(0)); got != "hello" {
		t.Fatalf("bytesFrom(0)=%q", got)
	}
	if r.end() != 5 {
		t.Fatalf("end=%d, want 5", r.end())
	}
}

func TestRing_Evict(t *testing.T) {
	r := newRing(4)
	r.append([]byte("abcdef")) // retains last 4: "cdef", start=2, end=6
	if r.start != 2 || r.end() != 6 {
		t.Fatalf("start=%d end=%d, want 2/6", r.start, r.end())
	}
	if got := string(r.bytesFrom(2)); got != "cdef" {
		t.Fatalf("bytesFrom(2)=%q, want cdef", got)
	}
	// cursor before the window clamps to start (gap → resync)
	if got := string(r.bytesFrom(0)); got != "cdef" {
		t.Fatalf("bytesFrom(0)=%q, want cdef (clamped)", got)
	}
	// cursor at end returns nil
	if got := r.bytesFrom(6); got != nil {
		t.Fatalf("bytesFrom(6)=%v, want nil", got)
	}
}

func TestRing_IncrementalCursor(t *testing.T) {
	r := newRing(1024)
	r.append([]byte("abc"))
	cur := int64(0)
	if got := string(r.bytesFrom(cur)); got != "abc" {
		t.Fatalf("first read=%q", got)
	}
	cur = r.end()
	r.append([]byte("def"))
	if got := string(r.bytesFrom(cur)); got != "def" {
		t.Fatalf("incremental read=%q, want def", got)
	}
}
