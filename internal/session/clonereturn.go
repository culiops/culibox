package session

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"slices"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// What started a return: the session's agent exiting, the next spawn of the
// clone, the clone going idle after a turn, a host-tier spawn in the clone's
// repo, or the user's "Bring work home now".
const (
	ReturnOnSpawn = "spawn"
	ReturnOnExit  = "exit"
	ReturnOnIdle  = "idle"
	ReturnOnHost  = "host"
	ReturnNow     = "now"
)

// cloneSpawnReturnBudget bounds the spawn-time return, run before the refresh
// under the same per-clone lock. cloneExitReturnTimeout bounds one exit or "now"
// return: it runs after the agent is gone, so nothing waits on it.
const (
	cloneSpawnReturnBudget = 60 * time.Second
	// hostReturnBudget bounds the return run before a Host spawn on a repo that
	// has clones; what it does not reach continues in the background.
	hostReturnBudget = 20 * time.Second
	// hostReturnGrace is how long a Host spawn waits past the budget for the
	// return to hand back its summaries (a run killed at the deadline cleans up).
	hostReturnGrace        = 2 * time.Second
	cloneExitReturnTimeout = 15 * time.Minute
	// cloneIdleReturnTimeout bounds one idle-triggered return: the agent may be
	// working again by then, so it is shorter than an exit return.
	cloneIdleReturnTimeout = 5 * time.Minute
)

var (
	// ErrNotClone is BringHome on a session that has no clone to bring home.
	ErrNotClone = errors.New("not a clone-mode session")
	// ErrReturning is BringHome while a "now" return for the session still runs.
	ErrReturning = errors.New("already bringing this session's work home")
)

// ReturnOpts is what a trigger tells ReturnWork.
type ReturnOpts struct {
	Trigger string
	// LiveSession reports whether another boxed session still uses the clone.
	// Like RefreshOpts.LiveSession, the hooks call it only after taking the
	// per-clone lock. nil means false.
	LiveSession func() bool
}

// NotHomeBranch is one clone branch that did not come home, and why. Strings
// are sanitized by the clone layer.
type NotHomeBranch struct {
	Branch string `json:"branch"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// MergeNote is the last automatic merge of a returned box/X into the host's
// checked-out X (#370): what it did, or the reason it did not. It mirrors
// clone.MergeResult field for field (session does not import clone). Strings
// are sanitized by the clone layer.
type MergeNote struct {
	At      time.Time `json:"at"`
	Branch  string    `json:"branch"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
	Merged  bool      `json:"merged,omitempty"`
	Commits int       `json:"commits,omitempty"`
	Files   int       `json:"files,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Paths   []string  `json:"paths,omitempty"`
}

// copy returns an independent MergeNote, nil for nil.
func (m *MergeNote) copy() *MergeNote {
	if m == nil {
		return nil
	}
	out := *m
	out.Paths = slices.Clone(m.Paths)
	return &out
}

// ReturnSummary is the outcome of one return. Skipped is why nothing was
// attempted ("in-use", "other-instance", "unavailable"). Immutable once stored
// on a session; Info hands out copies.
type ReturnSummary struct {
	Trigger  string          `json:"trigger"`
	At       time.Time       `json:"at"`
	Skipped  string          `json:"skipped,omitempty"`
	Base     string          `json:"base,omitempty"`
	Home     []string        `json:"home,omitempty"`
	NotHome  []NotHomeBranch `json:"not_home,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Error    string          `json:"error,omitempty"`
	// Flagged lists the branches that came home with files flagged for review
	// (#346): one note per branch, with the count and the record to read them
	// from. Absent when nothing was flagged.
	Flagged []HandbackNote `json:"flagged,omitempty"`
	// Uncommitted is how many files the clone's work tree holds that no commit
	// has: nil means unknown (never zero), UncommittedCut that the count is a
	// floor. Profile names the profile the clone belongs to.
	Uncommitted    *int   `json:"uncommitted,omitempty"`
	UncommittedCut bool   `json:"uncommitted_cut,omitempty"`
	Profile        string `json:"profile,omitempty"`
	// Merge is what the automatic merge into the checked-out branch did; absent
	// when the profile does not opt in or the return did not reach it.
	Merge *MergeNote `json:"merge,omitempty"`
}

// copy returns an independent ReturnSummary, nil for nil.
func (r *ReturnSummary) copy() *ReturnSummary {
	if r == nil {
		return nil
	}
	out := *r
	out.Home = slices.Clone(r.Home)
	out.NotHome = slices.Clone(r.NotHome)
	out.Warnings = slices.Clone(r.Warnings)
	out.Flagged = slices.Clone(r.Flagged)
	if r.Uncommitted != nil {
		n := *r.Uncommitted
		out.Uncommitted = &n
	}
	out.Merge = r.Merge.copy()
	return &out
}

// BackgroundReturnTimeout bounds a return that continues in the background after
// a host spawn's budget ran out: it runs under no spawn, so it gets the exit
// return's time.
const BackgroundReturnTimeout = cloneExitReturnTimeout

// copyReturns is an independent copy of a list of summaries, nil for empty.
func copyReturns(in []ReturnSummary) []ReturnSummary {
	if len(in) == 0 {
		return nil
	}
	out := make([]ReturnSummary, len(in))
	for i := range in {
		out[i] = *in[i].copy()
	}
	return out
}

// NotHomeState is the profile row's view of a clone's state file: what was
// not home at the last return.
type NotHomeState struct {
	At       time.Time       `json:"at"`
	Base     string          `json:"base,omitempty"`
	Branches []NotHomeBranch `json:"branches,omitempty"`
	Error    string          `json:"error,omitempty"`
	// Uncommitted and UncommittedCut are as on ReturnSummary.
	Uncommitted    *int `json:"uncommitted,omitempty"`
	UncommittedCut bool `json:"uncommitted_cut,omitempty"`
	// Merge is the last automatic merge result, as on ReturnSummary.
	Merge *MergeNote `json:"merge,omitempty"`
}

// cloneLive returns the scan "does a non-exited boxed session use this clone
// dir". except is a session that is exiting: Kill finalizes it before its
// status turns exited, so it would count itself and no kill would bring work
// home. The scan takes m.mu, so the hooks call it only once they hold the
// per-clone lock.
func (m *Manager) cloneLive(dir string, except *Session) func() bool {
	return func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, o := range m.sessions {
			if o == except || o.Runtime != config.RuntimeBox || !box.Overlaps(o.Cwd, dir) {
				continue
			}
			o.mu.Lock()
			alive := o.status != statusExited
			o.mu.Unlock()
			if alive {
				return true
			}
		}
		return false
	}
}

// returnWork brings a clone session's work home and stores the result on the
// session. It runs synchronously; callers choose the goroutine. The profile is
// re-resolved because it may have been edited since spawn: the return runs only
// when the profile still names this session's clone, never on the strength of a
// stale spawn-time snapshot.
func (m *Manager) returnWork(sess *Session, trigger string) {
	m.returnWorkTimeout(sess, trigger, cloneExitReturnTimeout)
}

// returnWorkTimeout is returnWork with the return's time limit chosen by the
// caller: exit and "now" use cloneExitReturnTimeout, idle a shorter one.
func (m *Manager) returnWorkTimeout(sess *Session, trigger string, timeout time.Duration) {
	m.mu.Lock()
	h := m.clone
	m.mu.Unlock()
	if h == nil || sess.Runtime != config.RuntimeBox || sess.CloneOf == "" {
		return
	}
	var cl *config.Clone
	if cr, ok := m.launcher.(CloneResolver); ok {
		var err error
		if cl, err = cr.CloneFor(sess.Agent); err != nil {
			log.Printf("session %s: no return, profile %q: %v", sess.ID, sess.Agent, err)
			cl = nil
		}
	}
	if cl == nil || filepath.Clean(cl.Dir) != filepath.Clean(sess.Cwd) || cl.Of != sess.CloneOf {
		log.Printf("session %s: no return, profile %q no longer names this session's clone", sess.ID, sess.Agent)
		if trigger == ReturnNow {
			// The user asked: say why nothing happened rather than do nothing.
			sess.setReturn(&ReturnSummary{Trigger: trigger, At: time.Now(), Skipped: "unavailable"})
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sum := h.ReturnWork(ctx, *cl, ReturnOpts{Trigger: trigger, LiveSession: m.cloneLive(cl.Dir, sess)})
	sess.setReturn(&sum)
}

func (s *Session) setReturn(r *ReturnSummary) {
	s.mu.Lock()
	s.ret = r
	s.mu.Unlock()
}

// BringHome starts a "now" return for a clone session and returns at once; the
// result lands on Info.Return. It works on an exited, retained session too.
func (m *Manager) BringHome(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	h := m.clone
	m.mu.Unlock()
	if !ok {
		return ErrNoSession
	}
	if h == nil || s.Runtime != config.RuntimeBox || s.CloneOf == "" {
		return ErrNotClone
	}
	s.mu.Lock()
	if s.returning {
		s.mu.Unlock()
		return ErrReturning
	}
	s.returning = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.returning = false
			s.mu.Unlock()
		}()
		m.returnWork(s, ReturnNow)
	}()
	return nil
}
