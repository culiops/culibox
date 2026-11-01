package session

import (
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

// M4: the box runs claude in the resolved dir, so the transcript sits under the
// slug of the resolved path, not of the (symlinked) Session.Cwd.
func TestBoxedSIDLookupUsesResolvedCwd(t *testing.T) {
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
	wantUnbound(t, m, "s1") // no sid yet: the file is there, but not provably s1's
	bindSID(m, "s1", "res-1")
	if got := peekText(t, m, "s1"); got != "from resolved" {
		t.Errorf("sid peek=%q want %q", got, "from resolved")
	}
}

// A directory named <sid>.jsonl, and a sid that is not a session id, never bind.
func TestBoxedSIDLookupSkipsOddEntries(t *testing.T) {
	m, bd := boxedLocManager(t)
	now := time.Now()
	writeTx(t, bd, locCwd, "good-1", "good", now.Add(-time.Minute))
	dir := projDirIn(bd)
	if err := os.Mkdir(filepath.Join(dir, "dir.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBoxFile(t, filepath.Join(dir, "bad name.jsonl"), `{"message":{"role":"assistant","content":[{"type":"text","text":"odd"}]}}`+"\n", now)
	wantUnbound(t, m, "s1")
	for _, sid := range []string{"dir", "bad name"} {
		bindSID(m, "s1", sid)
		if p, ok := m.PeekFor("s1"); ok {
			t.Errorf("sid %q bound an odd entry: %q", sid, p.Text)
		}
	}
	bindSID(m, "s1", "good-1")
	if got := peekText(t, m, "s1"); got != "good" {
		t.Errorf("good-1 peek=%q want %q", got, "good")
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
	// A transcript no sibling claims is not s2's either: s2 has no sid, and the
	// newest-unclaimed guess is gone (#28).
	writeTx(t, bd, locCwd, "cccc-3", "unclaimed", now.Add(-time.Second))
	wantUnbound(t, m, "s2")
}
