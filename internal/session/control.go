package session

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/boxread"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// Operator control actions: restart an exited session, interrupt a running
// one, answer a permission prompt. Each is gated here, in the session layer,
// so no caller can reach the PTY around the checks.

var (
	ErrNotExited     = errors.New("session has not exited")
	ErrRestarting    = errors.New("session is already being restarted")
	ErrNotLive       = errors.New("session is not running")
	ErrNotBlocked    = errors.New("session is not waiting on a prompt")
	ErrPromptChanged = errors.New("the prompt changed")
	ErrBadChoice     = errors.New("choice must be yes or no")
	// ErrTooSoon is escGate's refusal: a second Esc to the same session
	// within escDebounceWindow of the previous one. Shared by Interrupt and
	// Answer("no") — see escGate.
	ErrTooSoon = errors.New("wait a moment before sending another Esc")
	// ErrBinUnresolved is Interrupt's refusal when a session's Bin has not
	// been backfilled yet (ResolveBins runs post-reconcile). Interrupt picks
	// its key from isAgentBin(bin); an unresolved "" bin must never be
	// treated as "not an agent" and fall through to Ctrl+C — the session
	// could be a claude whose SECOND Ctrl+C exits it rather than merely
	// interrupting it. Refuse instead of guessing; nothing is written.
	ErrBinUnresolved = errors.New("session's agent is not yet known")
)

// resumeFor is the conversation a restart continues: a claude with a known id,
// on the host or in a box that is not exclusive. An exclusive (elevate-bound)
// box's key includes the session id, so a restart lands in a new box and the
// conversation is not there; codex has no equivalent. Both restart fresh. For a
// boxed claude this only says resume may be tried: spawn still checks that the
// transcript is in the target box before the flag is passed.
func resumeFor(runtime, bin, sid string, exclusive bool) string {
	if runtime != config.RuntimeHost && (runtime != config.RuntimeBox || exclusive) {
		return ""
	}
	if !isClaudeBin(bin) || !sessionIDRe.MatchString(sid) {
		return ""
	}
	return sid
}

// dropResume is argv without the first adjacent `--resume <sid>` pair that comes
// before any "--" token (what follows "--" is the prompt, never a flag). The
// result is always a fresh slice; the input is not touched. ok is false, and the
// result an unchanged copy, when there was no such pair.
func dropResume(argv []string, sid string) ([]string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		if argv[i] == "--resume" && argv[i+1] == sid {
			out := make([]string, 0, len(argv)-2)
			out = append(out, argv[:i]...)
			return append(out, argv[i+2:]...), true
		}
	}
	return slices.Clone(argv), false
}

// boxedTranscriptPresent reports whether sid's transcript is in the box home
// boxDir for one of the spawn cwd forms (C1: that project dir only). The only
// read is boxread.Open, which refuses a symlink, a non-regular file and any path
// out of boxDir; the file is closed again at once. An empty boxDir is "no".
func boxedTranscriptPresent(boxDir, sid string, cwds []string) bool {
	if boxDir == "" || !sessionIDRe.MatchString(sid) {
		return false
	}
	for _, c := range cwds {
		rel := filepath.Join(".claude", "projects", projectSlug(filepath.Clean(c)), sid+".jsonl")
		if f, _, ok := boxread.Open(boxDir, rel); ok {
			f.Close()
			return true
		}
	}
	return false
}

// resumeSkippedNote is Info.ResumeSkipped's text.
const resumeSkippedNote = "the previous conversation is not in this box's home"

// Restart replaces an exited session (still in retention) with a new one
// spawned from the same profile, cwd, runtime, options and name — resuming the
// same conversation where resumeFor allows — then drops the old one from the
// list. Replies start at the profile default, never inherited: an opt-in was
// given to that run.
func (m *Manager) Restart(id string) (*Session, error) {
	m.mu.Lock()
	s := m.sessions[id]
	var bin, acct string
	if s != nil {
		bin = s.Bin // guarded by m.mu: ResolveBins backfills it post-reconcile
		acct = s.Account
	}
	m.mu.Unlock()
	if s == nil {
		return nil, ErrNoSession
	}
	s.mu.Lock()
	switch {
	case s.status != statusExited:
		s.mu.Unlock()
		return nil, ErrNotExited
	case s.restarting:
		s.mu.Unlock()
		return nil, ErrRestarting
	}
	s.restarting = true
	name, sid := s.name, s.claudeSessionID
	oldIDs, oldComplete := slices.Clone(s.claudeSessionIDs), s.costComplete
	s.mu.Unlock()

	// Always explicit: "" would fall back to the profile's CURRENT account, and
	// a resume under another account has the wrong credentials and cannot find
	// the transcript. A default session says so by name.
	if acct == "" {
		acct = account.Default
	}

	var opts map[string]any
	if len(s.options) > 0 {
		opts = make(map[string]any, len(s.options))
		for k, v := range s.options {
			opts[k] = v
		}
	}
	ns, err := m.Spawn(launcher.SpawnSpec{
		Agent: s.Agent, Name: name, Cwd: s.Cwd, Runtime: s.Runtime, Options: opts, Account: acct,
		Resume: resumeFor(s.Runtime, bin, sid, s.Exclusive), ParentID: s.ParentID,
	})
	if err != nil {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
		return nil, err
	}
	// The replacement is a new session that resumes the old conversation: its
	// cost is everything the old one spent plus its own.
	ids, complete := ns.inheritConversations(oldIDs, oldComplete)
	m.mu.Lock()
	backend, sc := m.backend, m.sidecar
	m.mu.Unlock()
	if sc != nil {
		sc.setClaudeSessionID(ns.ID, ns.claudeSID(), ids, complete)
	}
	if backend != nil {
		_ = backend.Kill(id) // idempotent: the pidfile + socket of an agent that already exited
	}
	m.removeSession(id, s)
	return ns, nil
}

// interruptKey is chosen here, never by a caller. Esc ends an agent's turn
// without exiting it (measured on Claude Code 2.1.284: the process stays, the
// turn is cancelled); Ctrl+C stops a shell's foreground command.
func interruptKey(agent bool) []byte {
	if agent {
		return []byte{0x1b}
	}
	return []byte{0x03}
}

// escDebounceWindow is the minimum spacing between two Esc sent to the same
// session, shared by Interrupt (agent Esc) and Answer("no") via escGate.
// Claude Code opens its rewind menu on a second Esc once a session has gone
// idle (P9) — how short its own double-press window actually is was NOT
// measured, so 1s is a deliberately conservative, unmeasured choice: long
// enough to absorb an interrupt immediately followed by declining a prompt,
// or two rapid declines, without risking that window; short enough not to
// block a deliberate second interrupt. Ctrl+C (shells) has no such hazard
// and is never debounced.
const escDebounceWindow = 1 * time.Second

// nowFunc is the clock escGate reads. A var so a test can inject the clock
// instead of sleeping escDebounceWindow.
var nowFunc = time.Now

// escGate reports whether s may be sent an Esc right now. On success it
// records the instant, so the session's NEXT Esc — from either Interrupt or
// Answer("no") — is debounced against this one.
func (s *Session) escGate() error {
	s.escMu.Lock()
	defer s.escMu.Unlock()
	now := nowFunc()
	if !s.lastEscAt.IsZero() && now.Sub(s.lastEscAt) < escDebounceWindow {
		return ErrTooSoon
	}
	s.lastEscAt = now
	return nil
}

// Interrupt stops what a live session is doing. It needs no reply opt-in: it
// can only stop work, never start it (End session is equally unguarded).
// Session.Write marks submitted only on \r, which neither key contains. An
// agent's Esc goes through escGate; a shell's Ctrl+C never does.
func (m *Manager) Interrupt(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	var bin string
	if s != nil {
		bin = s.Bin // guarded by m.mu: ResolveBins backfills it post-reconcile
	}
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	s.mu.Lock()
	live := s.status == statusLive
	s.mu.Unlock()
	if !live {
		return ErrNotLive
	}
	if bin == "" {
		return ErrBinUnresolved
	}
	key := interruptKey(isAgentBin(bin))
	if key[0] == 0x1b {
		if err := s.escGate(); err != nil {
			return err
		}
	}
	_, err := s.Write(key)
	return err
}

// answerKey maps an answer to the ONE key Claude Code's permission dialog reads
// (measured on 2.1.284: `1` alone selects Yes, Esc alone rejects). Never a \r:
// a stray Enter would land on whatever the agent shows next. There is no
// "always" — that is a standing grant, not an answer.
func answerKey(choice string) ([]byte, error) {
	switch choice {
	case "yes":
		return []byte("1"), nil
	case "no":
		return []byte{0x1b}, nil
	}
	return nil, ErrBadChoice
}

// askHashMatches compares a client's hex SHA-256 with the current AskFull's.
func askHashMatches(askFull, want string) bool {
	sum := sha256.Sum256([]byte(askFull))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(want))) == 1
}

// answerState identifies one specific pending prompt: the transcript path
// plus the pending tool_use's own id (Peek.askID). A REAL id (the normal
// case — Claude Code always writes one) is unique on its own, so path+id
// alone is the call's identity; mtime/size are carried only for the rare
// synthetic-id fallback (see equal) and must never be required for a real
// id. Claude Code keeps appending role-less bookkeeping records
// (last-prompt, mode, ai-title, permission-mode, …) to the transcript while
// a call sits unresolved — parsePeekTailCut skips them (askID/AskFull don't
// move), but they still bump the file's mtime and size, so requiring those
// to match too used to make Answer's own successful write look like a
// changed prompt on the very next read. The zero value never matches a real
// prompt: askSrcPath is always non-empty whenever AskFull is (see
// peekAndBlocked).
type answerState struct {
	path  string
	id    string
	mtime time.Time
	size  int64
}

func stateOf(p Peek) answerState {
	return answerState{path: p.askSrcPath, id: p.askID, mtime: p.askSrcMTime, size: p.askSrcSize}
}

// syntheticAskID reports whether id is toolUseKey's positional fallback
// (peek.go: "\x00%d:%d", used only when a tool_use block carries no id of
// its own — not a shape Claude Code actually writes, but not assumed away
// either). Only that case falls back to the mtime/size read-snapshot in
// answerState.equal: a bare line/block position is not guaranteed to name
// the same call across two separate reads.
func syntheticAskID(id string) bool { return strings.HasPrefix(id, "\x00") }

// equal compares two answerStates as "the same pending call". path+id always
// have to match. For a REAL id that is the whole comparison — the id alone
// already names the call uniquely, and requiring mtime/size too would make
// the memo stop matching on every bookkeeping-record append that leaves the
// call itself untouched (see answerState's doc comment). Only a synthetic id
// also requires mtime+size, since a bare position isn't trustworthy alone.
// Not ==: time.Time carries an optional monotonic reading that plain
// equality can trip on even for the same instant, so mtime is compared with
// Equal.
func (a answerState) equal(b answerState) bool {
	if a.id != b.id || a.path != b.path {
		return false
	}
	if !syntheticAskID(a.id) {
		return true
	}
	return a.size == b.size && a.mtime.Equal(b.mtime)
}

// Answer types one key into a permission dialog. The gates run in this order,
// each with its own error, and all are required:
//  1. the choice is yes|no (checked first, so a malformed call probes nothing);
//  2. a claude session (ErrNotClaude);
//  3. the operator opted in to replies (ErrNotReplyable);
//  4. the session is live (ErrNotLive) — mirrors Interrupt;
//  5. Claude Code's live registry says it is blocked on a prompt RIGHT NOW
//     (ErrNotBlocked) — never inferred from silence or the transcript alone;
//  6. the current AskFull exists (exactly one pending call, short enough) and
//     its SHA-256 is the one the operator reviewed (ErrPromptChanged);
//  7. that exact prompt has not already been answered (ErrPromptChanged) —
//     the per-session lastAnswered memo, below.
//
// Gate-check through Write is ONE critical section per session, under
// s.answerMu: two concurrent Answer calls, or a double-click that resubmits
// before the UI disables the button, both pass every gate above on an
// UNCHANGED prompt — AskFull only moves once the approved command's
// tool_result lands, which is after the first key is already written.
// Without answerMu + the memo, the worst case was a stray key landing after
// the dialog had already closed: a bare "1" typed as input text into
// whatever came next, or a second Esc opening Claude Code's rewind menu on a
// now-idle session (P9) — not a key reaching some *other*, replacement
// dialog, since the registry+AskFull gates already require a live, matching
// prompt. escGate closes the Esc half of that: Answer("no") shares Interrupt's
// debounce, so two Esc (from either action) within escDebounceWindow refuse
// as ErrTooSoon rather than risk the double-Esc rewind menu.
func (m *Manager) Answer(id, choice, askSHA256 string) error {
	key, err := answerKey(choice)
	if err != nil {
		return err
	}
	m.mu.Lock()
	s := m.sessions[id]
	var bin string
	if s != nil {
		bin = s.Bin
	}
	repliesOn := m.repliesOn
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	// Kill-switch first: remote Yes/No shares the reply opt-in, so a stale
	// replyable=true must not let a key reach the PTY while replies are off.
	if !repliesOn {
		return ErrRepliesDisabled
	}
	if !isClaudeBin(bin) {
		return ErrNotClaude
	}
	s.mu.Lock()
	replyable := s.replyable
	live := s.status == statusLive
	s.mu.Unlock()
	if !replyable {
		return ErrNotReplyable
	}
	if !live {
		return ErrNotLive
	}

	s.answerMu.Lock()
	defer s.answerMu.Unlock()

	p, havePeek, blocked := m.peekAndBlocked(id)
	if !blocked {
		return ErrNotBlocked
	}
	if !havePeek || p.AskFull == "" || !askHashMatches(p.AskFull, askSHA256) {
		return ErrPromptChanged
	}
	cur := stateOf(p)
	if cur.equal(s.lastAnswered) {
		return ErrPromptChanged
	}
	if choice == "no" {
		if err := s.escGate(); err != nil {
			return err
		}
	}
	if _, err := s.Write(key); err != nil {
		return err
	}
	s.lastAnswered = cur
	return nil
}
