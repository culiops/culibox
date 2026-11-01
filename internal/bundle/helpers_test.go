package bundle

import (
	"os"
	"sync"
	"testing"
	"time"
)

func buildForTest(t *testing.T, src string) *Built {
	t.Helper()
	m, err := New(Options{
		Source:  src,
		Root:    rootFor(t, src),
		MCPFile: src + "/../.claude.json",
		Prober:  fakeProber{"npx": true, "node": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rootFor keeps one bundle root per source dir across calls in a test, so cache
// reuse is actually exercised.
var roots = map[string]string{}

func rootFor(t *testing.T, src string) string {
	t.Helper()
	if r, ok := roots[src]; ok {
		return r
	}
	r := t.TempDir()
	roots[src] = r
	return r
}

type fakeProber map[string]bool

func (f fakeProber) Resolves(cmds []string) map[string]bool {
	out := map[string]bool{}
	for _, c := range cmds {
		out[c] = f[c]
	}
	return out
}

// testGuard is a guard no GC shares.
func testGuard() snapshotGuard {
	return snapshotGuard{mu: &sync.Mutex{}, touch: func(p string, t time.Time) error { return os.Chtimes(p, t, t) }}
}

func timeoutAfter(sec int) <-chan time.Time { return time.After(time.Duration(sec) * time.Second) }
