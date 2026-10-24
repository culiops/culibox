package session

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/boxread"
)

const (
	// boxPidfileMaxBytes bounds the home pidfile: a decimal pid and a newline.
	boxPidfileMaxBytes = 32
	// boxPidMax is Linux's pid_max ceiling (2^22). A pid outside 1..boxPidMax
	// cannot be a real claude and is refused before any path is built from it.
	boxPidMax = 1 << 22
	// boxSessionsRel is the box-home-relative registry directory.
	boxSessionsRel = ".claude/sessions"
)

// cwdFormsOf is the forms of a host-chosen directory the box may know it by:
// Clean(p), plus its symlink-resolved form when that differs. The box runs
// claude with -w <resolved>, so a spawn dir reached through a symlink (macOS
// /tmp -> /private/tmp) shows up in the registry and the transcript slug in its
// resolved form. Only ever call this with a path the HOST chose (Session.Cwd,
// a spawn spec's cwd), never with anything the box wrote.
func cwdFormsOf(p string) []string {
	c := filepath.Clean(p)
	forms := []string{c}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != c {
		forms = append(forms, r)
	}
	return forms
}

// cwdForms is cwdFormsOf(s.Cwd), computed once and reused: Cwd is immutable, so
// the symlink walk does not run on every poll.
func (s *Session) cwdForms() []string {
	s.mu.Lock()
	f := s.cwdFormsCache
	s.mu.Unlock()
	if f != nil {
		return f
	}
	f = cwdFormsOf(s.Cwd)
	s.mu.Lock()
	s.cwdFormsCache = f
	s.mu.Unlock()
	return f
}

// boxCwdMatches is the boxed cwd guard. LEXICAL ONLY: regCwd is box-written, so
// there is no filesystem call on it. It must be absolute and, once cleaned, at
// or under one of the host-chosen spawn forms (a claude worktree lives in
// <cwd>/.claude/worktrees/).
func boxCwdMatches(regCwd string, cwds []string) bool {
	if !filepath.IsAbs(regCwd) {
		return false
	}
	rc := filepath.Clean(regCwd)
	for _, c := range cwds {
		if within(rc, filepath.Clean(c)) {
			return true
		}
	}
	return false
}

// readBoxedRegistry returns the live-registry entry for a boxed claude session:
// the wrapper-written home pidfile names the in-box pid, and
// .claude/sessions/<pid>.json is that process's entry. Everything is box-written
// and so hostile; every read goes through boxread, and any surprise is ok=false
// (the caller falls back to the mtime heuristic, silently).
//
// Freshness instead of procStart: the registry file must not be older than the
// pidfile, which rejects a <pid>.json left by an earlier container whose claude
// had the same small pid. Both files come from the box's one clock.
func readBoxedRegistry(boxDir, sessionID string, cwds []string) (claudeRegEntry, bool) {
	rel, ok := box.HomePidRel(sessionID)
	if !ok || boxDir == "" {
		return claudeRegEntry{}, false
	}
	pb, pfi, ok := boxread.ReadFile(boxDir, rel, boxPidfileMaxBytes)
	if !ok {
		return claudeRegEntry{}, false
	}
	txt := strings.TrimSpace(string(pb))
	pid, err := strconv.Atoi(txt)
	// Canonical decimal only: no sign, no leading zeros, nothing else.
	if err != nil || pid < 1 || pid > boxPidMax || strconv.Itoa(pid) != txt {
		return claudeRegEntry{}, false
	}
	// boxread only checks the final component, and a symlink that stays inside
	// the box is followed by os.Root, so refuse a sessions dir that is one.
	if _, ok := boxread.ReadDir(boxDir, boxSessionsRel, 1); !ok {
		return claudeRegEntry{}, false
	}
	rb, rfi, ok := boxread.ReadFile(boxDir, boxSessionsRel+"/"+strconv.Itoa(pid)+".json", registryMaxBytes)
	if !ok {
		return claudeRegEntry{}, false
	}
	var e claudeRegEntry
	if json.Unmarshal(rb, &e) != nil || e.Pid != pid || !sessionIDRe.MatchString(e.SessionID) {
		return claudeRegEntry{}, false
	}
	if !boxCwdMatches(e.Cwd, cwds) {
		return claudeRegEntry{}, false
	}
	if rfi.ModTime().Before(pfi.ModTime()) {
		return claudeRegEntry{}, false
	}
	return e, true
}

// boxedTranscriptPath is the validated entry's transcript, or "" when none of
// the candidates opens as a regular file inside the box: the registry cwd's
// project dir first (2.1.286 moves the transcript into a worktree's), then each
// spawn form's. Named by sessionId, so never a sibling's file. The slug is
// derived from a cleaned absolute path by a character-class replace, so it can
// never traverse.
func boxedTranscriptPath(boxDir string, e claudeRegEntry, cwds []string) string {
	dirs := append([]string{filepath.Clean(e.Cwd)}, cwds...)
	for _, d := range dirs {
		rel := filepath.Join(".claude", "projects", projectSlug(filepath.Clean(d)), e.SessionID+".jsonl")
		if f, _, ok := boxread.Open(boxDir, rel); ok {
			f.Close()
			return filepath.Join(boxDir, rel)
		}
	}
	return ""
}
