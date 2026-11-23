package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxread"
)

// Where a boxed session's Claude Code transcripts are on the host (#330). A box
// created since #330 mounts its profile's conversations folder over the home's
// projects dir, so the transcripts are in the folder; a box from before writes
// in its home, and a home adopted only in part keeps the rest there. Every read
// of either goes through an os.Root on it (boxread): boxes write both.

// transcriptRoot is one place a boxed session's transcripts may be: dir is
// opened as the root of every read, rel is the projects dir inside it ("." for a
// conversations folder). The zero value is "nowhere".
type transcriptRoot struct{ dir, rel string }

// projectRel is the path of a workspace's project dir under r's root.
func (r transcriptRoot) projectRel(cwd string) string {
	return filepath.Join(r.rel, projectSlug(filepath.Clean(cwd)))
}

// boxTranscriptRoots are the places to look for the transcripts of a session in
// box boxName under stateDir, in order: the conversations folder of key (when it
// is one), then the box home. The first is the one the box's claude sees.
func boxTranscriptRoots(stateDir, boxName, key string) []transcriptRoot {
	home := transcriptRoot{boxHome(stateDir, boxName), filepath.Join(".claude", "projects")}
	if !box.ValidConversationsKey(key) {
		return []transcriptRoot{home}
	}
	return []transcriptRoot{{box.ConversationsDir(stateDir, key), "."}, home}
}

// boxTranscriptKey is the key of the conversations folder box name mounts, as
// the box layer reports it, or "" (none, a box layer that cannot tell, or an
// answer that is not a key).
func boxTranscriptKey(boxes Boxes, name string) string {
	ck, ok := boxes.(interface{ ConversationsKeyOf(string) string })
	if !ok {
		return ""
	}
	if k := ck.ConversationsKeyOf(name); box.ValidConversationsKey(k) {
		return k
	}
	return ""
}

// transcriptPathIn finds a boxed session's transcript by its conversation id.
// It never trusts what is in a root, and it never guesses across sessions: only
// <sid>.jsonl in the project dir of each cwd form c, in each root in order, and
// sid must be a valid session id. Not found means no transcript. The registry is
// skipped for an exited session, so this is what keeps a fresh or exited session
// from showing another session's file from the same box. It stats at most two
// paths per cwd form and root and never lists a directory.
//
// cwds are the session's spawn-dir forms (cwdFormsOf): the box runs claude with
// -w <resolved>, so the slug may be of the symlink-resolved path. One os.Root per
// root dir; both checks use root.Lstat (confined, no final-symlink follow, never
// blocks), and the winner is opened later, through boxread (openTranscript),
// which re-checks it with fstat. The returned path is absolute under that root.
func transcriptPathIn(roots []transcriptRoot, cwds []string, sid string) (string, bool) {
	if !sessionIDRe.MatchString(sid) {
		return "", false
	}
	for _, tr := range roots {
		if p, ok := transcriptPathInRoot(tr, cwds, sid); ok {
			return p, true
		}
	}
	return "", false
}

func transcriptPathInRoot(tr transcriptRoot, cwds []string, sid string) (string, bool) {
	root, err := os.OpenRoot(tr.dir)
	if err != nil {
		return "", false
	}
	defer root.Close()
	for _, c := range cwds {
		relDir := tr.projectRel(c)
		// A slug dir that is itself a symlink (even one staying inside the root)
		// is refused: Lstat does not follow the final component.
		if fi, err := root.Lstat(relDir); err != nil || !fi.IsDir() {
			continue
		}
		rel := filepath.Join(relDir, sid+".jsonl")
		if fi, err := root.Lstat(rel); err == nil && fi.Mode().IsRegular() {
			return filepath.Join(tr.dir, rel), true
		}
	}
	return "", false
}

// boxedTranscriptPath is the validated entry's transcript, or "" when none of
// the candidates opens as a regular file inside its root: for each root in
// order, the registry cwd's project dir first (2.1.286 moves the transcript into
// a worktree's), then each spawn form's. Named by sessionId, so never a
// sibling's file. The slug is derived from a cleaned absolute path by a
// character-class replace, so it can never traverse.
func boxedTranscriptPath(roots []transcriptRoot, e claudeRegEntry, cwds []string) string {
	dirs := append([]string{filepath.Clean(e.Cwd)}, cwds...)
	for _, tr := range roots {
		for _, d := range dirs {
			rel := filepath.Join(tr.projectRel(d), e.SessionID+".jsonl")
			if f, _, ok := boxread.Open(tr.dir, rel); ok {
				f.Close()
				return filepath.Join(tr.dir, rel)
			}
		}
	}
	return ""
}

// boxedTranscriptPresent reports whether sid's transcript is in tr for one of
// the spawn cwd forms (C1: that project dir only). The only read is
// boxread.Open, which refuses a symlink, a non-regular file and any path out of
// tr's root; the file is closed again at once. The zero tr is "no".
func boxedTranscriptPresent(tr transcriptRoot, sid string, cwds []string) bool {
	if tr.dir == "" || !sessionIDRe.MatchString(sid) {
		return false
	}
	for _, c := range cwds {
		rel := filepath.Join(tr.projectRel(c), sid+".jsonl")
		if f, _, ok := boxread.Open(tr.dir, rel); ok {
			f.Close()
			return true
		}
	}
	return false
}

// readRoot is the root dir of l.roots that path is under, and path relative to
// it; ok is false when it is under none of them.
func (l located) readRoot(path string) (dir, rel string, ok bool) {
	for _, tr := range l.roots {
		r, err := filepath.Rel(tr.dir, path)
		if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue
		}
		return tr.dir, r, true
	}
	return "", "", false
}
