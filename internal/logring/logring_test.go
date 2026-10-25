package logring

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestKeepsLastN(t *testing.T) {
	r := New(200, 2048)
	for i := 0; i < 250; i++ {
		fmt.Fprintf(r, "line %d\n", i)
	}
	got := r.Lines()
	if len(got) != 200 {
		t.Fatalf("len = %d, want 200", len(got))
	}
	if got[0] != "line 50" || got[199] != "line 249" {
		t.Fatalf("window = %q .. %q", got[0], got[199])
	}
}

func TestPartialLineJoinsAcrossWrites(t *testing.T) {
	r := New(10, 100)
	r.Write([]byte("ab"))
	r.Write([]byte("c\nd"))
	if got := r.Lines(); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("Lines = %q, want [abc]", got)
	}
	r.Write([]byte("\n"))
	if got := r.Lines(); len(got) != 2 || got[0] != "abc" || got[1] != "d" {
		t.Fatalf("Lines = %q, want [abc d]", got)
	}
}

func TestWriteReportsFullLength(t *testing.T) {
	r := New(2, 4)
	p := []byte(strings.Repeat("x", 1000))
	n, err := r.Write(p)
	if n != len(p) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

func TestLongLineCapped(t *testing.T) {
	const maxLine = 2048
	r := New(10, maxLine)
	big := []byte(strings.Repeat("x", 1<<20))
	r.Write(big)
	if len(r.pending) > maxLine {
		t.Fatalf("pending = %d, exceeds %d", len(r.pending), maxLine)
	}
	r.Write(big)
	if len(r.pending) > maxLine {
		t.Fatalf("pending grew to %d after a second write", len(r.pending))
	}
	r.Write([]byte("\n"))
	got := r.Lines()
	if len(got) != 1 {
		t.Fatalf("lines = %d, want 1", len(got))
	}
	if len(got[0]) > maxLine+len("…") || !strings.HasSuffix(got[0], "…") {
		t.Fatalf("line len %d, suffix ok=%v", len(got[0]), strings.HasSuffix(got[0], "…"))
	}
	// the drop must not leak into the next line
	r.Write([]byte("next\n"))
	if got := r.Lines(); got[len(got)-1] != "next" {
		t.Fatalf("next line = %q", got[len(got)-1])
	}
}

func TestExactlyMaxLineIsNotMarked(t *testing.T) {
	r := New(10, 5)
	r.Write([]byte("12345\n123456\n"))
	got := r.Lines()
	if got[0] != "12345" || got[1] != "12345…" {
		t.Fatalf("Lines = %q", got)
	}
}

func TestLinesIsACopy(t *testing.T) {
	r := New(3, 10)
	r.Write([]byte("a\n"))
	l := r.Lines()
	l[0] = "mutated"
	if r.Lines()[0] != "a" {
		t.Fatal("Lines exposed internal storage")
	}
}

func TestConcurrentWrites(t *testing.T) {
	r := New(200, 2048)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				fmt.Fprintf(r, "g%d line %d\n", g, i)
				r.Lines()
			}
		}(g)
	}
	wg.Wait()
	if n := len(r.Lines()); n != 200 {
		t.Fatalf("len = %d, want 200", n)
	}
}
