package box

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedConversation writes one Claude Code transcript into a home the way Claude
// Code lays them out (.claude/projects/<workspace slug>/<session>.jsonl), and
// puts the home's mtime back like seedCaches does.
func seedConversation(t *testing.T, dir, rel string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), `{"type":"user"}`+"\n")
	if err := os.Chtimes(dir, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
}

const convRel = ".claude/projects/-work-repo/0b1c2d3e.jsonl"

// #330: a home past the TTL that still holds a conversation stays, so /resume
// can reach it again; only its regenerable caches go.
func TestReclaimKeepsAHomePastTheTTLThatHoldsAConversation(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-aaaaaaaaaaaa"
	dir := seedHome(t, m, name, 40*24*time.Hour)
	seedCaches(t, dir)
	seedConversation(t, dir, convRel)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Box != name || !done[0].Kept || !done[0].Caches || done[0].Bytes != 40 {
		t.Fatalf("want one kept home with a 40-byte cache prune, got %+v", done)
	}
	if done[0].Age < 39*24*time.Hour {
		t.Fatalf("age %v looks wrong", done[0].Age)
	}
	mustExist(t, filepath.Join(dir, filepath.FromSlash(convRel)))
	mustExist(t, filepath.Join(dir, ".claude", "keep"))
	mustExist(t, m.markerPath(name))
	for _, p := range wantCachePaths {
		mustBeGone(t, filepath.Join(dir, p))
	}
	// Keeping is not a use: the home keeps its age, so the next pass says the same.
	if age, ok := m.homeAge(name); !ok || age < 39*24*time.Hour {
		t.Fatalf("keeping the home reset its age to %v", age)
	}
}

// Every pass reports a kept home, with nothing left to prune too, so the caller
// always knows the whole kept set.
func TestReclaimReportsAKeptHomeWithNothingToPrune(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-bbbbbbbbbbbb"
	dir := seedHome(t, m, name, 40*24*time.Hour)
	if err := os.RemoveAll(filepath.Join(dir, ".npm")); err != nil {
		t.Fatal(err)
	}
	seedConversation(t, dir, convRel)
	for pass := 0; pass < 2; pass++ {
		done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
		if err != nil {
			t.Fatal(err)
		}
		if len(done) != 1 || done[0].Box != name || !done[0].Kept || done[0].Caches || done[0].Bytes != 0 {
			t.Fatalf("pass %d: want one kept home and no prune, got %+v", pass, done)
		}
	}
	mustExist(t, dir)
}

// With the prune turned off (cacheTTL >= ttl) a kept home keeps its caches too.
func TestReclaimKeepsTheCachesOfAKeptHomeWhenThePruneIsOff(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	const name = "ab-box-cccccccccccc"
	dir := seedHome(t, m, name, 40*24*time.Hour)
	seedCaches(t, dir)
	seedConversation(t, dir, convRel)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || !done[0].Kept || done[0].Caches {
		t.Fatalf("want one kept home and no prune, got %+v", done)
	}
	for _, p := range wantCachePaths {
		mustExist(t, filepath.Join(dir, p, "blob"))
	}
}

// What is not a conversation /resume lists does not keep a home: the home goes
// whole past the TTL, as before #330.
func TestReclaimStillRemovesAHomeWithNoConversation(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"no projects dir": func(t *testing.T, dir string) {},
		"empty workspace folder": func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-work-repo"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"not a jsonl": func(t *testing.T, dir string) {
			seedConversation(t, dir, ".claude/projects/-work-repo/notes.txt")
		},
		"jsonl straight under projects": func(t *testing.T, dir string) {
			seedConversation(t, dir, ".claude/projects/stray.jsonl")
		},
		"jsonl that is a directory": func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-work-repo", "x.jsonl"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"jsonl that is a symlink": func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), "real.jsonl")
			writeFile(t, outside, "{}\n")
			if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-work-repo"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, ".claude", "projects", "-work-repo", "x.jsonl")); err != nil {
				t.Fatal(err)
			}
		},
		"workspace folder that is a symlink": func(t *testing.T, dir string) {
			outside := t.TempDir()
			writeFile(t, filepath.Join(outside, "x.jsonl"), "{}\n")
			if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, ".claude", "projects", "-work-repo")); err != nil {
				t.Fatal(err)
			}
		},
		"projects dir that is a symlink": func(t *testing.T, dir string) {
			outside := t.TempDir()
			writeFile(t, filepath.Join(outside, "-work-repo", "x.jsonl"), "{}\n")
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, ".claude", "projects")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for label, seed := range cases {
		t.Run(label, func(t *testing.T) {
			m, _ := newTestManagerWithFake(t)
			const name = "ab-box-dddddddddddd"
			dir := seedHome(t, m, name, 40*24*time.Hour)
			seed(t, dir)
			when := time.Now().Add(-40 * 24 * time.Hour)
			if err := os.Chtimes(dir, when, when); err != nil {
				t.Fatal(err)
			}
			done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
			if err != nil {
				t.Fatal(err)
			}
			if len(done) != 1 || done[0].Kept || done[0].Caches {
				t.Fatalf("want the home removed whole, got %+v", done)
			}
			mustBeGone(t, dir)
		})
	}
}

// A home younger than the TTL is pruned as before and not reported as kept:
// "kept" means "reclaim would have deleted it".
func TestReclaimDoesNotCallAYoungHomeKept(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	dir := seedHome(t, m, "ab-box-eeeeeeeeeeee", 2*time.Hour)
	seedCaches(t, dir)
	seedConversation(t, dir, convRel)
	done, err := m.ReclaimState(context.Background(), DefaultStateTTL, DefaultCacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Kept || !done[0].Caches {
		t.Fatalf("want a plain cache prune, got %+v", done)
	}
}

// KeptHomes is the read-only view doctor uses: every home past the TTL that
// holds a conversation, with its age and size, and nothing else.
func TestKeptHomesListsOnlyOldHomesWithConversations(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	kept := seedHome(t, m, "ab-box-111111111111", 40*24*time.Hour)
	seedConversation(t, kept, convRel)
	seedConversation(t, seedHome(t, m, "ab-box-222222222222", 2*24*time.Hour), convRel) // young
	seedHome(t, m, "ab-box-333333333333", 40*24*time.Hour)                              // no conversation
	if err := os.MkdirAll(filepath.Join(m.opts.StateRoot, stateDirName, "not-a-box", ".claude", "projects", "-w"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := KeptHomes(context.Background(), m.opts.StateRoot, DefaultStateTTL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Box != "ab-box-111111111111" {
		t.Fatalf("want only the old home with a conversation, got %+v", got)
	}
	if got[0].Dir != kept {
		t.Fatalf("dir %q, want %q", got[0].Dir, kept)
	}
	if got[0].Age < 39*24*time.Hour || got[0].Age > 41*24*time.Hour {
		t.Fatalf("age %v looks wrong", got[0].Age)
	}
	// .npm/blob (10 bytes) plus the transcript (16 bytes).
	if got[0].Bytes != 26 || !got[0].Sized {
		t.Fatalf("size %d (sized %v), want 26", got[0].Bytes, got[0].Sized)
	}
}

func TestKeptHomesWithNoStateIsEmpty(t *testing.T) {
	got, err := KeptHomes(context.Background(), t.TempDir(), DefaultStateTTL)
	if err != nil || len(got) != 0 {
		t.Fatalf("want nothing, got %+v, %v", got, err)
	}
}

// Out of time, the size is reported as unknown rather than as a wrong number.
func TestKeptHomesLeavesTheSizeUnknownWhenOutOfTime(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	seedConversation(t, seedHome(t, m, "ab-box-444444444444", 40*24*time.Hour), convRel)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := KeptHomes(ctx, m.opts.StateRoot, DefaultStateTTL)
	if err == nil {
		t.Fatal("want the context's error")
	}
	for _, h := range got {
		if h.Sized {
			t.Fatalf("a home was sized with the context done: %+v", h)
		}
	}
}
