package session

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/culiops/culibox/internal/boxread"
	"github.com/culiops/culibox/internal/procinfo"
)

// PidResolver is implemented by persistence backends that know the agent's own
// pid (abduco's pidfile `inner=`). Optional: without it, no registry entry
// validates and a session's transcript is found only by its persisted sid.
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

// Registry statuses. waiting: Claude Code sits on a permission / choice prompt
// (measured 2026-09-29, 2.1.284). busy / idle: a turn is running / has ended;
// they flip exactly at turn boundaries (busy 0.16 s after Enter, idle 0.1 s
// after the turn ends) and viewer focus, mouse and typing never change them
// (measured 2026-11-02, 2.1.316, #77). shell: the turn ended with a background
// shell still running; Claude starts a new turn on its own when it finishes.
const (
	regStatusWaiting = "waiting"
	regStatusBusy    = "busy"
	regStatusIdle    = "idle"
	regStatusShell   = "shell"
)

// regStaleAfter: a busy or shell status is distrusted once the PTY has been
// silent this long, and the PTY rule decides instead. A busy turn repaints its
// spinner every 0.1-0.33 s (measured, #77), so a minute of silence means the
// status is stale: an exit path that never rewrote it (Esc-interrupt, an API
// error and /compact are unmeasured) or format drift. Past it, a long shell
// reads as waiting: Claude sits at its prompt and accepts input.
const regStaleAfter = 60 * time.Second

// regActivity is the activity of a live session once Claude Code's registry
// status is known. act is what Info computed from PTY recency; quiet is what it
// would read with a silent PTY (waiting or idle, see quietActivity); status is
// the validated registry entry's ("" when there is none).
//
// The registry is the primary signal because a TUI repaints for reasons that
// are not work (viewer focus/mouse/keys, its own idle redraws). PTY recency is
// the fallback only with no entry (codex, an unreadable or drifted registry) or
// a status we have not seen yet. Exit and detach always dominate.
//
// shell reads "working", not "your turn": the agent resumes by itself when the
// background shell finishes, so a needs-you there is false and would fire a
// second time at the real end of the turn. The UI has no distinct "background"
// state, and adding one is a product call, not part of this fix (#77).
//
// src is what decided: srcAgent (the registry) or srcPTY (the fallback); ""
// for an exited or detached session.
func regActivity(act, quiet, status string, silent time.Duration) (_ string, src string) {
	if act != "working" && act != "waiting" && act != "idle" {
		return act, ""
	}
	switch status {
	case regStatusBusy, regStatusShell:
		if silent > regStaleAfter {
			return act, srcPTY
		}
		return "working", srcAgent
	case regStatusWaiting:
		return "waiting", srcAgent
	case regStatusIdle:
		return quiet, srcAgent
	}
	return act, srcPTY
}

// Info.ActivitySource values. Provider-neutral: any agent that reports its own
// live status is "agent".
const (
	srcAgent = "agent"
	srcPTY   = "pty"
)

// replyActivity is the activity SendMessage's guard judges a reply by. Unlike
// regActivity (what the dot shows), the registry may only TIGHTEN the PTY
// verdict here, never loosen it: the agent can write its own registry (a boxed
// one always can), so a forged "idle" must not let a line be typed into a
// prompt the PTY shows animating, which reads single keys. busy refuses; idle,
// shell and waiting leave the PTY verdict alone (waiting refuses through the
// pending guard instead). shell with a quiet PTY therefore accepts a reply:
// Claude is at its prompt and takes input while the background shell runs.
func replyActivity(act, status string, silent time.Duration) string {
	if status == regStatusBusy && act == "waiting" && silent <= regStaleAfter {
		return "working"
	}
	return act
}

// sessionIDRe bounds what a registry sessionId may look like before it is ever
// joined into a path: a uuid-ish token, nothing that can traverse. The first
// character must be alphanumeric — real ids are UUIDs, and a leading "-"
// would let a downstream `--resume <id>` be misread as a flag (see
// launcher.resumeIDRe).
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// procStartFunc returns a pid's start-time token. Injected so tests need no
// real processes.
type procStartFunc func(pid int) (string, bool)

// boxHome is a boxed session's home on the host: <stateDir>/boxstate/<box>. The
// one place this path is built; everything under it is written by the box.
func boxHome(stateDir, box string) string {
	return filepath.Join(stateDir, "boxstate", box)
}

// procStartOf is the pid's start time as procinfo reports it. On Linux this is
// /proc stat field 22, the same value Claude Code writes as procStart. On darwin
// it is "<sec>.<usec>" while Claude Code writes a UTC ctime string; StartMatches
// bridges the two.
func procStartOf(pid int) (string, bool) {
	s, err := procinfo.StartTime(pid)
	return s, err == nil
}

// StartMatches reports whether Claude Code's recorded procStart names the same
// process start as ours (procinfo.StartTime). Doctor uses it too, so its
// match= line agrees with attribution.
func StartMatches(recorded, ours string) bool {
	return startMatches(runtime.GOOS, recorded, ours)
}

// ctimeLayout is Claude Code's darwin procStart once runs of spaces are
// collapsed ("Mon Oct  5" pads the day).
const ctimeLayout = "Mon Jan 2 15:04:05 2006"

// startMatches: on Linux both sides are /proc stat field 22, compared exactly.
// On darwin Claude Code writes a ctime string in UTC (measured on a +0700 Mac,
// 5/5 live pids): it matches when it parses as UTC to the same second as ours.
// Anything that does not parse is a mismatch, never a guess.
func startMatches(goos, recorded, ours string) bool {
	if recorded == ours {
		return true
	}
	if goos != "darwin" {
		return false
	}
	secStr, usec, ok := strings.Cut(ours, ".")
	if !ok || len(usec) != 6 {
		return false
	}
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return false
	}
	if _, err := strconv.Atoi(usec); err != nil {
		return false
	}
	f := strings.Fields(recorded)
	t, err := time.Parse(ctimeLayout, strings.Join(f, " "))
	if err != nil || len(f) == 0 || f[0] != t.Weekday().String()[:3] {
		return false // time.Parse ignores the weekday; a wrong one is not this start
	}
	return t.Unix() == sec
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

// ReadRegistryFile is readRegistryFile for diagnostics outside this package
// (agent-box doctor): the same no-follow, FIFO-safe, size-capped read.
func ReadRegistryFile(path string) ([]byte, bool) { return readRegistryFile(path) }

// readClaudeRegistry returns the registry entry for pid (read from
// <claudeDir>/sessions, the .claude dir itself), only if it is provably
// THAT process's, for THIS session: the pid field matches, procStart names the
// live process's starttime (a recycled pid has a different one), and the
// entry's cwd matches the session's own cwd (cleaned) — pid alone is not
// enough once a session is known to be exited (see locate), so this is a
// second, independent guard against a pid that has been reused for an
// unrelated claude invocation in a different directory. Any failure is
// ok=false — the registry may improve attribution, never break it.
func readClaudeRegistry(claudeDir string, pid int, procStart procStartFunc, cwd string) (claudeRegEntry, bool) {
	if claudeDir == "" || pid <= 0 || procStart == nil {
		return claudeRegEntry{}, false
	}
	b, ok := readRegistryFile(filepath.Join(claudeDir, "sessions", strconv.Itoa(pid)+".json"))
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
	if !ok || !StartMatches(strings.Trim(string(e.ProcStart), `"`), want) {
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
	// boxDir is <stateDir>/boxstate/<box> for a boxed session, "" on the host
	// tier. Everything under it was written by the contained agent, so every
	// read of a path in it goes through boxread (openTranscript).
	boxDir string
	// roots are where a boxed session's transcripts may be (boxTranscriptRoots:
	// its conversations folder, then boxDir); nil on the host tier. Every read
	// of a transcript under one goes through boxread on that root.
	roots []transcriptRoot
	// cwds are the forms of the spawn dir a boxed session is known by (cleaned,
	// and symlink-resolved when different); nil on the host tier or when the
	// session has no usable state dir. Host-chosen, never box-supplied.
	cwds []string
	// claudeDir is the .claude dir the session's registry and transcripts live
	// in (home, account config dir, or the per-box one); "" when nothing is
	// known. The activity timeline stats the registry file under it.
	claudeDir string
}

// locate resolves a claude session's transcript, in order:
//  1. host tier, live session: the live registry by pid names the exact
//     session file, and follows /clear (which starts a new sessionId). Once an
//     entry VALIDATES it is trusted outright — if its named transcript does not
//     exist, that is reported as "no transcript" (path=""), never papered over
//     by falling back to a sibling's file;
//  2. boxed tier, live: the box-home pidfile + registry entry, read through
//     boxread;
//  3. otherwise (no validated registry entry, live or boxed, or an exited
//     session) l.path is "" and l.reg is nil. There is deliberately no
//     fallback that lists the project dir (#28): it cost an unbounded scan per
//     poll and its answer could be a sibling's conversation (#21). The caller
//     that wants a transcript for such a session, boundTranscript, looks only
//     for the exact <sid>.jsonl of the persisted conversation id.
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
	var bin, cwd, box, configDir, tkey string
	var created time.Time
	var exited bool
	if s != nil {
		bin, cwd, box, configDir, created, tkey = s.Bin, s.Cwd, s.Box, s.ConfigDir, s.Created, s.transcriptKey
		s.mu.Lock()
		exited = s.status == statusExited
		s.mu.Unlock()
	}
	m.mu.Unlock()
	if s == nil || !isClaudeBin(bin) {
		return located{}
	}
	l := located{claude: true, created: created, cwd: cwd, boxed: box != ""}
	// The .claude dir this session's registry and transcripts live in: the
	// default ~/.claude, an agent account's config dir (host tier only), or
	// the per-box .claude. Empty only when nothing is known (no home, no
	// account dir, no box state); never a relative path.
	var claudeDir string
	if home != "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	if configDir != "" {
		claudeDir = configDir
	}
	if box != "" && stateDir == "" {
		// A boxed session with no state dir has nowhere safe to look: never
		// fall back to a host .claude (home or account config dir).
		return l
	}
	if box != "" {
		l.boxDir = boxHome(stateDir, box)
		l.roots = boxTranscriptRoots(stateDir, box, tkey)
		l.cwds = s.cwdForms()
		claudeDir = filepath.Join(l.boxDir, ".claude")
	}
	l.claudeDir = claudeDir
	if l.boxDir != "" && !exited {
		// Boxed tier, live: the box-home pidfile names the in-box pid and its
		// registry entry names the exact transcript. Every read is guarded
		// (boxread); any surprise silently leaves the session unbound.
		if e, ok := readBoxedRegistry(l.boxDir, id, l.cwds); ok {
			l.reg = &e
			m.noteClaudeSessionID(s, id, e.SessionID)
			// Trusted as is, whether or not the transcript exists: never fall
			// back to a sibling's file.
			l.path = boxedTranscriptPath(l.roots, e, l.cwds)
			return l
		}
	}
	if box == "" && claudeDir != "" && !exited {
		if pr, ok := backend.(PidResolver); ok {
			if pid, ok := pr.InnerPid(id); ok {
				if e, ok := readClaudeRegistry(claudeDir, pid, procStart, cwd); ok {
					l.reg = &e
					m.noteClaudeSessionID(s, id, e.SessionID)
					// 2.1.286 moves the transcript into the worktree's project dir
					// along with the registry cwd, so look where the registry says
					// the session is now, then in the spawn dir. Named by sessionId
					// either way, so neither can be a sibling's file.
					for _, dir := range []string{e.Cwd, cwd} {
						p := filepath.Join(claudeDir, "projects", projectSlug(filepath.Clean(dir)), e.SessionID+".jsonl")
						if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
							l.path = p
							break
						}
					}
					// A validated entry is trusted as-is, whether or not its
					// transcript exists — never fall back to a sibling's file,
					// which is exactly the misattribution this locator exists to
					// fix.
					return l
				}
			}
		}
	}
	// No validated registry entry: l.path stays "" and nothing here lists a
	// project dir. A transcript for such a session is only ever the exact
	// <sid>.jsonl of its persisted conversation id, which boundTranscript looks up.
	return l
}

// openTranscript opens the located transcript for reading and returns it with
// its Fstat info; the caller closes the file. Boxed: through boxread on the
// root the path is under (l.roots), so a symlink, a FIFO, or a path outside
// every root is "no data". Host: a plain open, exactly as before.
func (l located) openTranscript() (*os.File, os.FileInfo, bool) {
	if l.path == "" {
		return nil, nil, false
	}
	if l.boxDir != "" {
		dir, rel, ok := l.readRoot(l.path)
		if !ok {
			return nil, nil, false
		}
		return boxread.Open(dir, rel)
	}
	f, err := os.Open(l.path)
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}

// noteClaudeSessionID remembers the conversation a validated registry entry
// names, so an exited session can be restarted into it. Only a change costs a
// sidecar write. Called by locate with no lock held; e.SessionID already
// matched sessionIDRe inside readClaudeRegistry.
//
// It also keeps the session's conversation history (every sid since spawn):
// all appending goes through addConversationLocked. The first note after a
// daemon restart also applies the downtime rule (see restoredUnchecked).
func (m *Manager) noteClaudeSessionID(s *Session, id, sid string) {
	s.mu.Lock()
	prev := s.claudeSessionID
	if prev == "" && len(s.claudeSessionIDs) > 0 {
		prev = s.claudeSessionIDs[len(s.claudeSessionIDs)-1]
	}
	changed := s.claudeSessionID != sid
	s.claudeSessionID = sid
	if s.addConversationLocked(sid) {
		changed = true
	}
	if s.restoredUnchecked {
		// First look since a daemon restart. A different conversation means
		// one or more /clears happened while nobody was watching, and the ones
		// in between cannot be recovered: absent beats wrong.
		s.restoredUnchecked = false
		if sid != prev && s.costComplete {
			s.costComplete = false
			changed = true
		}
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	m.persistConversations(s, id)
}

// persistConversations writes the session's current conversation id, history
// and completeness to the sidecar. Snapshot and write happen under one
// per-session lock, so two concurrent callers cannot land out of order and
// persist a shorter, older list over a newer one (the setter also refuses to
// drop ids already stored, as a second line of defence).
func (m *Manager) persistConversations(s *Session, id string) {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	s.mu.Lock()
	sid, ids, complete := s.claudeSessionID, slices.Clone(s.claudeSessionIDs), s.costComplete
	s.mu.Unlock()
	m.mu.Lock()
	sc := m.sidecar
	m.mu.Unlock()
	if sc != nil {
		sc.setClaudeSessionID(id, sid, ids, complete)
	}
}

// boundTranscript is locate for the activity timeline, which must show only a
// transcript PROVEN to be this session's. It returns locate's answer only when
// a live registry entry validated (l.reg != nil: the registry names the exact
// sessionId, and locate never falls back from a validated entry). Otherwise —
// an exited session, or a registry that failed validation — the only candidate
// is the exact <sid>.jsonl of the persisted claudeSessionID; a newest-by-cwd
// guess is never used, because it can hand back a sibling's conversation. ok=false means no bound transcript; l.claude still
// says whether this is a claude session at all (zero l = unknown session).
// Nothing here lists a project dir: locate names no path without a validated
// registry entry, and the sid lookup below stats exactly one file per cwd form.
func (m *Manager) boundTranscript(id string) (located, bool) {
	l := m.locate(id)
	if !l.claude {
		return l, false
	}
	if l.reg != nil {
		return l, l.path != ""
	}
	m.mu.Lock()
	s := m.sessions[id]
	home := m.home
	m.mu.Unlock()
	if s == nil {
		return l, false
	}
	sid := s.claudeSID()
	if !sessionIDRe.MatchString(sid) {
		return l, false
	}
	// The path the timeline already parsed while the session was live and
	// bound: a registry cwd that moved the transcript into a worktree's
	// project dir is gone with the registry, but the file is still ours. Only
	// when it is exactly this conversation's file; never a directory search.
	if p := m.timelinePath(id); p != "" && filepath.Base(p) == sid+".jsonl" {
		l.path = p
		return l, true
	}
	if l.boxDir != "" {
		if p, ok := transcriptPathIn(l.roots, l.cwds, sid); ok {
			l.path = p
			return l, true
		}
		return l, false
	}
	if l.boxed {
		return l, false // boxed with no state dir: never a host .claude
	}
	claudeDir := l.claudeDir
	if claudeDir == "" && home != "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	if claudeDir == "" {
		return l, false
	}
	l.path = filepath.Join(claudeDir, "projects", projectSlug(filepath.Clean(l.cwd)), sid+".jsonl")
	return l, true
}

// PollConversations keeps every live claude session's conversation history
// current with no browser open: each tick locates the session, and locate
// notes the registry's current sid as a side effect, so a /clear is recorded
// within one interval. It blocks until ctx is done; run it in its own
// goroutine. No lock is held across locate, which does its own file I/O.
func (m *Manager) PollConversations(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, id := range m.liveClaudeIDs() {
			if ctx.Err() != nil {
				return
			}
			m.locate(id)
		}
	}
}

// liveClaudeIDs returns the ids of the sessions worth polling: claude and not
// exited (locate would skip an exited one's registry anyway).
func (m *Manager) liveClaudeIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, s := range m.sessions {
		if !isClaudeBin(s.Bin) {
			continue
		}
		s.mu.Lock() // nested under m.mu, as locate does
		exited := s.status == statusExited
		s.mu.Unlock()
		if !exited {
			ids = append(ids, id)
		}
	}
	return ids
}

// AgentSessionIndex maps each agent conversation id of a live session to that
// session's culibox id, so a figure keyed by conversation (analytics) can link to
// the session that still holds it. Every id a session has had since spawn counts
// (a /clear starts a new one), plus its current one. An exited session is left
// out, as the session list marks it (Info.Exited): there is nothing to open. A
// fresh map on every call.
func (m *Manager) AgentSessionIndex() map[string]string {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	out := map[string]string{}
	for _, s := range ss {
		s.mu.Lock()
		if s.exited {
			s.mu.Unlock()
			continue
		}
		ids := slices.Clone(s.claudeSessionIDs)
		if s.claudeSessionID != "" {
			ids = append(ids, s.claudeSessionID)
		}
		s.mu.Unlock()
		for _, id := range ids {
			out[id] = s.ID
		}
	}
	return out
}
