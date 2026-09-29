package session

import (
	"errors"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/launcher"
)

// Operator control actions: restart an exited session, interrupt a running
// one, answer a permission prompt. Each is gated here, in the session layer,
// so no caller can reach the PTY around the checks.

var (
	ErrNotExited  = errors.New("session has not exited")
	ErrRestarting = errors.New("session is already being restarted")
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
