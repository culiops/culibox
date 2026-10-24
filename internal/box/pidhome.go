package box

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"regexp"
)

// HomePidDir is where, inside the box home, the wrapper records the agent's pid
// for the host to find. It is relative to the home (host: stateDir(box); in the
// box: boxHome).
const HomePidDir = ".agent-box/pids"

var homePidIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// HomePidRel is the home-relative path of a session's pidfile, or "", false
// when sessionID could not name a plain file there (a separator, a leading dot
// or dash, empty, too long). The id is checked, not escaped: a refused id gets
// no home pidfile at all.
func HomePidRel(sessionID string) (string, bool) {
	if !homePidIDRe.MatchString(sessionID) {
		return "", false
	}
	return HomePidDir + "/" + sessionID + ".pid", true
}

// homePidArg is the wrapper's $1: the in-box path of the home pidfile, or
// /dev/null when the id is refused, which makes the write a harmless no-op.
func homePidArg(sessionID string) string {
	if rel, ok := HomePidRel(sessionID); ok {
		return boxHome + "/" + rel
	}
	return "/dev/null"
}

// prepareHomePid makes the host side of the home pidfile ready before the
// session starts: the pid dir exists, and a stale pidfile left by an earlier run
// of the same session id is gone, so the host never reads a dead pid as live.
//
// Best effort, and it must never fail a spawn: the worst outcome is a session
// the host cannot link to its in-box claude, which falls back to what it did
// before. It logs at most once per call. Everything goes through an os.Root
// anchored at the box home, because the box can replace any path below it with
// a symlink; a path that leaves the home fails instead of being followed.
func (m *Manager) prepareHomePid(boxName, sessionID string) {
	rel, ok := HomePidRel(sessionID)
	if !ok {
		return
	}
	dir := m.stateDir(boxName)
	if err := prepareHomePidAt(dir, rel); err != nil {
		log.Printf("box: preparing the home pidfile for %s in %s: %v", sessionID, dir, err)
	}
}

func prepareHomePidAt(dir, rel string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(HomePidDir, 0o700); err != nil {
		return err
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
