package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/agent-box/internal/procinfo"
)

// PidResolver is implemented by persistence backends that know the agent's own
// pid (abduco's pidfile `inner=`). Optional: without it, transcripts are located
// by the cwd+mtime heuristic alone.
type PidResolver interface {
	InnerPid(id string) (int, bool)
}

// claudeRegEntry is the subset of Claude Code's live-session registry,
// ~/.claude/sessions/<pid>.json, this daemon reads. The registry is
// undocumented upstream (observed on 2.1.231-2.1.284): every field is treated
// as optional and any surprise means "no entry", never an error.
type claudeRegEntry struct {
	Pid       int             `json:"pid"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	ProcStart json.RawMessage `json:"procStart"` // a JSON string today; tolerate a number
	Status    string          `json:"status"`    // busy | idle | shell | waiting (observed)
}

// regStatusWaiting is the registry status Claude Code reports while it sits on
// a permission / choice prompt (measured 2026-09-29, 2.1.284).
const regStatusWaiting = "waiting"

// sessionIDRe bounds what a registry sessionId may look like before it is ever
// joined into a path: a uuid-ish token, nothing that can traverse. The first
// character must be alphanumeric — real ids are UUIDs, and a leading "-"
// would let a downstream `--resume <id>` be misread as a flag (see
// launcher.resumeIDRe).
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// procStartFunc returns a pid's start-time token. Injected so tests need no
// real processes.
type procStartFunc func(pid int) (string, bool)

// procStartOf is the pid's start time as procinfo reports it. On Linux this is
// /proc stat field 22, the same value Claude Code writes as procStart. On darwin
// Claude Code's format is unmeasured; a mismatch only makes readClaudeRegistry
// miss, and the caller falls back to the cwd heuristic.
func procStartOf(pid int) (string, bool) {
	s, err := procinfo.StartTime(pid)
	return s, err == nil
}

// registryMaxBytes bounds how much of a registry file we will ever read. The
// registry directory is untrusted (any local process can write into it), so an
// oversized file is rejected outright rather than silently truncated — a
// truncated read of a huge file could parse as some OTHER
// syntactically-valid-but-wrong record instead of failing closed.
const registryMaxBytes = 64 << 10

// readRegistryFile opens path defensively: O_NOFOLLOW refuses a symlink (the
// registry directory could have been planted with one pointing anywhere
// readable), O_NONBLOCK keeps a FIFO/device from wedging the daemon, and the
// result must be a regular file no larger than registryMaxBytes. Any failure
// is ok=false, exactly like every other guard here — never an error.
func readRegistryFile(path string) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > registryMaxBytes {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, registryMaxBytes))
	if err != nil {
		return nil, false
	}
	return b, true
}

// readClaudeRegistry returns the registry entry for pid, only if it is provably
// THAT process's, for THIS session: the pid field matches, procStart equals the
// live process's starttime (a recycled pid has a different one), and the
// entry's cwd matches the session's own cwd (cleaned) — pid alone is not
// enough once a session is known to be exited (see locate), so this is a
// second, independent guard against a pid that has been reused for an
// unrelated claude invocation in a different directory. Any failure is
// ok=false — the registry may improve attribution, never break it.
func readClaudeRegistry(home string, pid int, procStart procStartFunc, cwd string) (claudeRegEntry, bool) {
	if home == "" || pid <= 0 || procStart == nil {
		return claudeRegEntry{}, false
	}
	b, ok := readRegistryFile(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json"))
	if !ok {
		return claudeRegEntry{}, false
	}
	var e claudeRegEntry
	if json.Unmarshal(b, &e) != nil || e.Pid != pid || !sessionIDRe.MatchString(e.SessionID) {
		return claudeRegEntry{}, false
	}
	if !regCwdMatches(e.Cwd, cwd) {
		return claudeRegEntry{}, false
	}
	want, ok := procStart(pid)
	if !ok || strings.Trim(string(e.ProcStart), `"`) != want {
		return claudeRegEntry{}, false
	}
	return e, true
}

// regCwdMatches is readClaudeRegistry's cwd guard. Up to 2.1.285 the registry
// cwd stays the spawn dir; from 2.1.286 EnterWorktree rewrites it to the
// worktree. So it accepts the spawn dir itself or any checkout of the SAME
// repository (by common git dir) — still rejecting a reused pid now running a
// claude in an unrelated directory or another repository.
func regCwdMatches(regCwd, cwd string) bool {
	if filepath.Clean(regCwd) == filepath.Clean(cwd) {
		return true
	}
	return filepath.IsAbs(regCwd) && sameRepository(filepath.Clean(regCwd), filepath.Clean(cwd))
}

// located is where a session's transcript lives, plus what Claude Code's live
// registry said about the session (nil when it could not be read and
// validated). claude=false means no transcript feature applies at all.
type located struct {
	claude bool
	path   string
	reg    *claudeRegEntry
	// created is the session's own Session.Created instant — peekAndBlocked
	// passes it to parsePeekTailCut so a pending tool_use left over from a
	// PRIOR run in the same transcript file (Restart --resume reuses it)
	// never offers AskFull.
	created time.Time
	// cwd and boxed are the session's spawn dir and tier, for UsageFor's
	// liveCwd vetting of the transcript's own cwd.
	cwd   string
	boxed bool
}

// locate resolves a claude session's transcript, in order:
//  1. host tier, live session: the live registry by pid names the exact
//     session file, and follows /clear (which starts a new sessionId). Once an
//     entry VALIDATES it is trusted outright — if its named transcript does not
//     exist, that is reported as "no transcript" (path=""), never papered over
//     by falling back to a sibling's file;
//  2. boxed tier: the per-box home already isolates the session;
//  3. otherwise (no validated registry entry, live or boxed) the cwd + mtime
//     heuristic (transcriptPath).
//
// The registry step is skipped entirely for an already-exited session: abduco's
// pidfile `inner=` pid is the agent's LAST known pid and is never updated after
// the agent exits, so for an exited session it can point at a since-reused pid
// now running an unrelated claude in some other cwd. Checking exited first
// avoids doing the (uncached) file I/O for a case that can never be current;
// the cwd guard in readClaudeRegistry is a second, independent line of defense
// for the same reused-pid hazard.
//
// Locking mirrors the old per-caller code: capture everything under m.mu (Bin
// is backfilled by ResolveBins), then do file I/O unlocked. status is guarded
// by the session's OWN mutex, so it is read under a nested s.mu — the same
// m.mu-then-s.mu order ResolveBins already uses.
func (m *Manager) locate(id string) located {
	m.mu.Lock()
	s := m.sessions[id]
	home, stateDir, backend, procStart := m.home, m.stateDir, m.backend, m.procStart
	var bin, cwd, box string
	var created time.Time
	var exited bool
	if s != nil {
		bin, cwd, box, created = s.Bin, s.Cwd, s.Box, s.Created
		s.mu.Lock()
		exited = s.status == statusExited
		s.mu.Unlock()
	}
	m.mu.Unlock()
	if s == nil || !isClaudeBin(bin) {
		return located{}
	}
	l := located{claude: true, created: created, cwd: cwd, boxed: box != ""}
	if box == "" && home != "" && !exited {
		if pr, ok := backend.(PidResolver); ok {
			if pid, ok := pr.InnerPid(id); ok {
				if e, ok := readClaudeRegistry(home, pid, procStart, cwd); ok {
					l.reg = &e
					m.noteClaudeSessionID(s, id, e.SessionID)
					// 2.1.286 moves the transcript into the worktree's project dir
					// along with the registry cwd, so look where the registry says
					// the session is now, then in the spawn dir. Named by sessionId
					// either way, so neither can be a sibling's file.
					for _, dir := range []string{e.Cwd, cwd} {
						p := filepath.Join(home, ".claude", "projects", projectSlug(filepath.Clean(dir)), e.SessionID+".jsonl")
						if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
							l.path = p
							break
						}
					}
					// A validated entry is trusted as-is, whether or not its
					// transcript exists — never fall through to the heuristic,
					// which is exactly the newer-sibling misattribution this
					// locator exists to fix.
					return l
				}
			}
		}
	}
	base := home
	if box != "" && stateDir != "" {
		base = filepath.Join(stateDir, "boxstate", box)
	}
	if base == "" {
		return l
	}
	if p, ok := transcriptPath(cwd, base, created); ok {
		l.path = p
	}
	return l
}

// noteClaudeSessionID remembers the conversation a validated registry entry
// names, so an exited session can be restarted into it. Only a change costs a
// sidecar write. Called by locate with no lock held; e.SessionID already
// matched sessionIDRe inside readClaudeRegistry.
func (m *Manager) noteClaudeSessionID(s *Session, id, sid string) {
	s.mu.Lock()
	changed := s.claudeSessionID != sid
	s.claudeSessionID = sid
	s.mu.Unlock()
	if !changed {
		return
	}
	m.mu.Lock()
	sc := m.sidecar
	m.mu.Unlock()
	if sc != nil {
		sc.setClaudeSessionID(id, sid)
	}
}
