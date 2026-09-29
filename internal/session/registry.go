package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	ProcStart json.RawMessage `json:"procStart"` // a JSON string today; tolerate a number
	Status    string          `json:"status"`    // busy | idle | shell | waiting (observed)
}

// regStatusWaiting is the registry status Claude Code reports while it sits on
// a permission / choice prompt (measured 2026-09-29, 2.1.284).
const regStatusWaiting = "waiting"

// sessionIDRe bounds what a registry sessionId may look like before it is ever
// joined into a path: a uuid-ish token, nothing that can traverse.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// procStartFunc returns field 22 (starttime) of /proc/<pid>/stat. Injected so
// tests need no real processes.
type procStartFunc func(pid int) (string, bool)

func procStartOf(pid int) (string, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	return statStartTime(string(b))
}

// statStartTime extracts starttime from a /proc/<pid>/stat line. comm (field 2)
// may contain spaces and ')', so scan past the LAST ')': the remainder starts
// at field 3, which puts field 22 at index 19.
func statStartTime(stat string) (string, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 {
		return "", false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return "", false
	}
	return f[19], true
}

// readClaudeRegistry returns the registry entry for pid, only if it is provably
// THAT process's: the pid field matches and procStart equals the live
// process's starttime (a recycled pid has a different one). Any failure is
// ok=false — the registry may improve attribution, never break it.
func readClaudeRegistry(home string, pid int, procStart procStartFunc) (claudeRegEntry, bool) {
	if home == "" || pid <= 0 || procStart == nil {
		return claudeRegEntry{}, false
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return claudeRegEntry{}, false
	}
	var e claudeRegEntry
	if json.Unmarshal(b, &e) != nil || e.Pid != pid || !sessionIDRe.MatchString(e.SessionID) {
		return claudeRegEntry{}, false
	}
	want, ok := procStart(pid)
	if !ok || strings.Trim(string(e.ProcStart), `"`) != want {
		return claudeRegEntry{}, false
	}
	return e, true
}

// located is where a session's transcript lives, plus what Claude Code's live
// registry said about the session (nil when it could not be read and
// validated). claude=false means no transcript feature applies at all.
type located struct {
	claude bool
	path   string
	reg    *claudeRegEntry
}

// locate resolves a claude session's transcript, in order:
//  1. host tier: the live registry by pid names the exact session file, and
//     follows /clear (which starts a new sessionId);
//  2. boxed tier: the per-box home already isolates the session;
//  3. otherwise the cwd + mtime heuristic (transcriptPath).
//
// Locking mirrors the old per-caller code: capture everything under m.mu (Bin
// is backfilled by ResolveBins), then do file I/O unlocked.
func (m *Manager) locate(id string) located {
	m.mu.Lock()
	s := m.sessions[id]
	home, stateDir, backend, procStart := m.home, m.stateDir, m.backend, m.procStart
	var bin, cwd, box string
	var created time.Time
	if s != nil {
		bin, cwd, box, created = s.Bin, s.Cwd, s.Box, s.Created
	}
	m.mu.Unlock()
	if s == nil || !isClaudeBin(bin) {
		return located{}
	}
	l := located{claude: true}
	if box == "" && home != "" {
		if pr, ok := backend.(PidResolver); ok {
			if pid, ok := pr.InnerPid(id); ok {
				if e, ok := readClaudeRegistry(home, pid, procStart); ok {
					l.reg = &e
					p := filepath.Join(home, ".claude", "projects", projectSlug(cwd), e.SessionID+".jsonl")
					if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
						l.path = p
						return l
					}
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
