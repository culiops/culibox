package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func markExited(m *Manager, id string) {
	s := m.sessions[id]
	s.mu.Lock()
	s.status = statusExited
	s.mu.Unlock()
}

// F1: an exited boxed session whose conversation id is known looks ONLY for its
// own <sid>.jsonl. When it is absent the answer is "no transcript", never
// another session's file from the same box.
func TestBoxedExitedSessionKnownSidNoTranscriptShowsNothing(t *testing.T) {
	m, _ := boxedRegManager(t)
	s2 := m.sessions["s2"]
	s2.mu.Lock()
	s2.claudeSessionID = "dddd-4" // known, but no dddd-4.jsonl exists
	s2.mu.Unlock()
	markExited(m, "s2")

	if p, ok := m.PeekFor("s2"); ok {
		t.Errorf("s2 peek=%q, want a miss (its own transcript does not exist)", p.Text)
	}
	if u, ok := m.UsageFor("s2"); ok {
		t.Errorf("s2 usage=%+v, want a miss", u)
	}
	if l := m.locate("s2"); l.path != "" {
		t.Errorf("s2 path=%q, want none", l.path)
	}
	// s1 is unaffected and still reads its own.
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("s1 peek=%q want %q", got, "from A")
	}
}

// With a known sid and the file present, the exited session finds exactly it,
// even when a sibling's transcript is newer.
func TestBoxedExitedSessionKnownSidFindsOwnTranscript(t *testing.T) {
	m, _ := boxedRegManager(t)
	s1 := m.sessions["s1"]
	s1.mu.Lock()
	s1.claudeSessionID = "aaaa-1"
	s1.mu.Unlock()
	markExited(m, "s1")
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("peek=%q want %q", got, "from A")
	}
}

// With no sid, a transcript older than the session cannot be its own.
func TestBoxedUnknownSidIgnoresOlderTranscripts(t *testing.T) {
	m, bd := boxedLocManager(t)
	now := time.Now()
	m.sessions["s1"].Created = now
	writeTx(t, bd, locCwd, "older-1", "from before the session", now.Add(-10*time.Minute))
	if p, ok := m.PeekFor("s1"); ok {
		t.Errorf("peek=%q, want a miss: the only file predates the session", p.Text)
	}
	if u, ok := m.UsageFor("s1"); ok {
		t.Errorf("usage=%+v, want a miss", u)
	}
	// A file at or after Created is used.
	writeTx(t, bd, locCwd, "newer-2", "from during the session", now.Add(time.Minute))
	if got := peekText(t, m, "s1"); got != "from during the session" {
		t.Errorf("peek=%q want the post-Created file", got)
	}
}

// M4: the box runs claude in the resolved dir, so the transcript sits under the
// slug of the resolved path, not of the (symlinked) Session.Cwd.
func TestBoxedFallbackUsesResolvedCwd(t *testing.T) {
	m, bd := boxedLocManager(t)
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	m.sessions["s1"].Cwd = link
	writeTx(t, bd, real, "res-1", "from resolved", time.Now())
	if got := peekText(t, m, "s1"); got != "from resolved" {
		t.Errorf("heuristic peek=%q want %q", got, "from resolved")
	}
	// The same through the known-sid lookup.
	s := m.sessions["s1"]
	s.mu.Lock()
	s.claudeSessionID = "res-1"
	s.mu.Unlock()
	if got := peekText(t, m, "s1"); got != "from resolved" {
		t.Errorf("sid peek=%q want %q", got, "from resolved")
	}
}

// Non-regular entries and names that are not session ids are never ranked.
func TestBoxedFallbackSkipsOddEntries(t *testing.T) {
	m, bd := boxedLocManager(t)
	now := time.Now()
	writeTx(t, bd, locCwd, "good-1", "good", now.Add(-time.Minute))
	dir := projDirIn(bd)
	if err := os.Mkdir(filepath.Join(dir, "dir.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBoxFile(t, filepath.Join(dir, "bad name.jsonl"), `{"message":{"role":"assistant","content":[{"type":"text","text":"odd"}]}}`+"\n", now)
	if got := peekText(t, m, "s1"); got != "good" {
		t.Errorf("peek=%q want %q", got, "good")
	}
}

// M3: ranking a directory costs one os.Root, not one per entry.
func BenchmarkTranscriptPathIn1000(b *testing.B) {
	bd := b.TempDir()
	dir := filepath.Join(bd, ".claude", "projects", projectSlug(locCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("s-%04d.jsonl", i))
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			b.Fatal(err)
		}
		mt := now.Add(time.Duration(i) * time.Second)
		_ = os.Chtimes(p, mt, mt)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := transcriptPathIn(bd, []string{locCwd}, "", nil, now.Add(-time.Hour)); !ok {
			b.Fatal("no transcript")
		}
	}
}

// A fresh session still on the trust dialog has no registry and no sid. A busy
// sibling in the same box has a newer-than-Created transcript; that is the
// sibling's (its sid is known), so the fresh session must not show it.
func TestBoxedFreshUnknownSidSkipsSiblingTranscript(t *testing.T) {
	m, _ := newLocatorManager(t)
	state := t.TempDir()
	m.stateDir = state
	bd := filepath.Join(state, "boxstate", "box-1")
	now := time.Now()
	m.sessions["s1"].Box, m.sessions["s2"].Box = "box-1", "box-1"
	m.sessions["s1"].claudeSessionID = "aaaa-1"
	m.sessions["s2"].Created = now.Add(-time.Minute)
	writeTx(t, bd, locCwd, "aaaa-1", "from sibling", now)

	if p, ok := m.PeekFor("s2"); ok {
		t.Errorf("s2 peek=%q, want a miss: the only candidate is s1's transcript", p.Text)
	}
	if u, ok := m.UsageFor("s2"); ok {
		t.Errorf("s2 usage=%+v, want a miss", u)
	}
	if got := peekText(t, m, "s1"); got != "from sibling" {
		t.Errorf("s1 peek=%q want its own", got)
	}
	// A transcript no sibling claims is still found by the heuristic.
	writeTx(t, bd, locCwd, "cccc-3", "unclaimed", now.Add(-time.Second))
	if got := peekText(t, m, "s2"); got != "unclaimed" {
		t.Errorf("s2 peek=%q want the unclaimed file", got)
	}
}
