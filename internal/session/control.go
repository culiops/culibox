package session

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

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
)

// resumeFor is the conversation a restart continues: only a host-tier claude
// with a known id. A boxed claude's conversation lives in its box's state
// dir, and codex has no equivalent — both restart fresh.
func resumeFor(runtime, bin, sid string) string {
	if runtime != config.RuntimeHost || !isClaudeBin(bin) || !sessionIDRe.MatchString(sid) {
		return ""
	}
	return sid
}

// Restart replaces an exited session (still in retention) with a new one
// spawned from the same profile, cwd, runtime, options and name — resuming the
// same conversation where resumeFor allows — then drops the old one from the
// list. Replies start at the profile default, never inherited: an opt-in was
// given to that run.
func (m *Manager) Restart(id string) (*Session, error) {
	m.mu.Lock()
	s := m.sessions[id]
	var bin string
	if s != nil {
		bin = s.Bin // guarded by m.mu: ResolveBins backfills it post-reconcile
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
	s.mu.Unlock()

	var opts map[string]any
	if len(s.options) > 0 {
		opts = make(map[string]any, len(s.options))
		for k, v := range s.options {
			opts[k] = v
		}
	}
	ns, err := m.Spawn(launcher.SpawnSpec{
		Agent: s.Agent, Name: name, Cwd: s.Cwd, Runtime: s.Runtime, Options: opts,
		Resume: resumeFor(s.Runtime, bin, sid),
	})
	if err != nil {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	backend := m.backend
	m.mu.Unlock()
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

// Interrupt stops what a live session is doing. It needs no reply opt-in: it
// can only stop work, never start it (End session is equally unguarded).
// Session.Write marks submitted only on \r, which neither key contains.
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
	_, err := s.Write(interruptKey(isAgentBin(bin)))
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

// Answer types one key into a permission dialog. The gates run in this order,
// each with its own error, and all are required:
//  1. the choice is yes|no (checked first, so a malformed call probes nothing);
//  2. a claude session (ErrNotClaude);
//  3. the operator opted in to replies (ErrNotReplyable);
//  4. Claude Code's live registry says it is blocked on a prompt RIGHT NOW
//     (ErrNotBlocked) — never inferred from silence or the transcript alone;
//  5. the current AskFull exists (exactly one pending call, short enough) and
//     its SHA-256 is the one the operator reviewed (ErrPromptChanged).
//
// The window between the gates and the Write is the same narrow TOCTOU
// SendMessage accepts; the worst case is a key into a dialog that replaced the
// reviewed one within that window.
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
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	if !isClaudeBin(bin) {
		return ErrNotClaude
	}
	s.mu.Lock()
	replyable := s.replyable
	s.mu.Unlock()
	if !replyable {
		return ErrNotReplyable
	}
	p, havePeek, blocked := m.peekAndBlocked(id)
	if !blocked {
		return ErrNotBlocked
	}
	if !havePeek || p.AskFull == "" || !askHashMatches(p.AskFull, askSHA256) {
		return ErrPromptChanged
	}
	_, err = s.Write(key)
	return err
}
