package boxread

import (
	"io"
	"strings"
	"testing"
)

func TestLineReader(t *testing.T) {
	in := "ab\n" + strings.Repeat("x", 20) + "\ncd\nlast"
	lr := NewLineReader(strings.NewReader(in), 10)
	type res struct {
		line string
		n    int
		over bool
		eof  bool
	}
	var got []res
	for {
		line, n, over, err := lr.Next()
		got = append(got, res{string(line), n, over, err == io.EOF})
		if err != nil {
			break
		}
	}
	want := []res{{"ab\n", 3, false, false}, {"", 21, true, false}, {"cd\n", 3, false, false}, {"last", 4, false, true}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
