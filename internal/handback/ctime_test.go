package handback

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCtimeOf(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	ct, ok := ctimeOf(fi)
	if !ok {
		t.Fatal("ctimeOf reported no ctime for a real file")
	}
	if d := time.Since(ct); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("ctime %v is not within 5s of now (off by %v)", ct, d)
	}
}
