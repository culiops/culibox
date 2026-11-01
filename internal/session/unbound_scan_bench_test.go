package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fillProjectDir plants n transcripts in dir (valid session-id stems, so the
// boxed ranking considers them too).
func fillProjectDir(tb testing.TB, dir string, n int) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("s-%05d.jsonl", i))
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		mt := now.Add(time.Duration(i) * time.Second)
		_ = os.Chtimes(p, mt, mt)
	}
}

// BenchmarkLocateUnboundHost is the polled cost of one unbound host claude
// session (no validated registry entry) whose project dir holds many
// transcripts: what every PollConversations tick paid before #28.
func BenchmarkLocateUnboundHost(b *testing.B) {
	m, home := newLocatorManager(b)
	if err := os.Remove(filepath.Join(home, ".claude", "sessions", "101.json")); err != nil {
		b.Fatal(err)
	}
	fillProjectDir(b, projDirIn(home), 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.locate("s1")
	}
}

// BenchmarkBoundTranscriptUnboundHost is the same through boundTranscript, the
// caller every peek/usage/timeline/attribution poll goes through.
func BenchmarkBoundTranscriptUnboundHost(b *testing.B) {
	m, home := newLocatorManager(b)
	if err := os.Remove(filepath.Join(home, ".claude", "sessions", "101.json")); err != nil {
		b.Fatal(err)
	}
	fillProjectDir(b, projDirIn(home), 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := m.boundTranscript("s1"); ok {
			b.Fatal("unbound session reported bound")
		}
	}
}

// BenchmarkBoundTranscriptUnboundBoxed: a boxed session with no registry and no
// sid; the box ranking reads up to 4096 entries.
func BenchmarkBoundTranscriptUnboundBoxed(b *testing.B) {
	m, bd := boxedLocManager(b)
	fillProjectDir(b, projDirIn(bd), 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := m.boundTranscript("s1"); ok {
			b.Fatal("unbound session reported bound")
		}
	}
}
