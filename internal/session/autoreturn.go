package session

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/clone"
	"github.com/culiops/culibox/internal/config"
)

// The auto-return schedule: a return runs autoReturnDebounce after an agent
// turn ends, at least autoReturnThrottle after the previous one when the clone's
// refs moved, and at least autoReturnQuietGap after it when they did not (the
// return then only refreshes the uncommitted counts).
const (
	autoReturnPoll     = 5 * time.Second
	autoReturnDebounce = 10 * time.Second
	autoReturnThrottle = 30 * time.Second
	autoReturnQuietGap = 5 * time.Minute
)

// stopper is the part of *time.Timer the scheduler uses.
type stopper interface{ Stop() bool }

// autoReturner schedules idle returns, one timer per clone however many
// sessions use it. Its dependencies are injected so tests run on a fake clock.
// mu guards clones and never stays held across run or fingerprint.
type autoReturner struct {
	now         func() time.Time
	afterFunc   func(time.Duration, func()) stopper
	fingerprint func(dir string) (string, bool)
	run         func(sess *Session)

	mu     sync.Mutex
	clones map[string]*cloneSched // by filepath.Clean(sess.Cwd)
}

// cloneSched is one clone's schedule.
type cloneSched struct {
	lastRun time.Time
	lastFP  string
	haveFP  bool // false: the last fingerprint was unknown
	timer   stopper
	due     time.Time
	gen     uint64 // names the armed timer; a callback of an older one is stale
	running bool   // a run is in flight
	rerun   bool   // a timer fired while running: go again when it ends

	sessions map[*Session]struct{} // sessions of this clone being watched
	sess     *Session              // the session that last ended a turn
}

func newAutoReturner(now func() time.Time, afterFunc func(time.Duration, func()) stopper,
	fingerprint func(string) (string, bool), run func(*Session)) *autoReturner {
	return &autoReturner{now: now, afterFunc: afterFunc, fingerprint: fingerprint, run: run,
		clones: map[string]*cloneSched{}}
}

// stateLocked returns the clone's schedule, creating it. Caller holds a.mu.
func (a *autoReturner) stateLocked(key string) *cloneSched {
	st := a.clones[key]
	if st == nil {
		st = &cloneSched{sessions: map[*Session]struct{}{}}
		a.clones[key] = st
	}
	return st
}

// attach records that sess is being watched, so a timer outlives any one of the
// clone's sessions and dies with the last.
func (a *autoReturner) attach(sess *Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.stateLocked(filepath.Clean(sess.Cwd))
	st.sessions[sess] = struct{}{}
	if st.sess == nil {
		st.sess = sess
	}
}

// turnEnded schedules a return after sess finished an agent turn. Refs that
// moved (or cannot be read: unknown counts as moved) wait the debounce and the
// throttle since the last run; refs that did not move wait the longer quiet gap.
// An armed timer that is due no later than the new time is kept.
func (a *autoReturner) turnEnded(sess *Session) {
	key := filepath.Clean(sess.Cwd)
	fp, ok := a.fingerprint(key) // before the lock: it walks the disk
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.stateLocked(key)
	st.sessions[sess] = struct{}{}
	st.sess = sess
	now := a.now()
	gap := autoReturnThrottle
	if ok && st.haveFP && fp == st.lastFP {
		gap = autoReturnQuietGap
	}
	due := now.Add(autoReturnDebounce)
	if earliest := st.lastRun.Add(gap); earliest.After(due) {
		due = earliest
	}
	if st.timer != nil && !st.due.After(due) {
		return
	}
	a.armLocked(key, st, due, now)
}

// armLocked (re)arms the clone's one timer. Caller holds a.mu.
func (a *autoReturner) armLocked(key string, st *cloneSched, due, now time.Time) {
	if st.timer != nil {
		st.timer.Stop()
	}
	st.gen++
	gen := st.gen
	st.due = due
	st.timer = a.afterFunc(due.Sub(now), func() { a.fire(key, st, gen) })
}

// fire is the timer callback: it takes the fingerprint (before the run, so a
// change the run itself causes is seen as new), stamps the schedule and starts
// the run in its own goroutine. It does nothing for a stale timer, a clone with
// no session left, or while the previous run is still going (it marks a rerun).
func (a *autoReturner) fire(key string, st *cloneSched, gen uint64) {
	a.mu.Lock()
	if a.clones[key] != st || st.gen != gen || st.timer == nil {
		a.mu.Unlock()
		return
	}
	st.timer = nil
	sess := st.sess
	if _, known := st.sessions[sess]; !known {
		sess = nil
		for s := range st.sessions {
			sess = s
			break
		}
	}
	if sess == nil {
		a.mu.Unlock()
		return
	}
	if st.running {
		st.rerun = true
		a.mu.Unlock()
		return
	}
	st.running = true
	a.mu.Unlock()

	fp, ok := a.fingerprint(key)
	a.mu.Lock()
	st.lastFP, st.haveFP = fp, ok
	st.lastRun = a.now()
	a.mu.Unlock()
	go func() {
		defer a.finish(key, st)
		// The session may have ended since the timer fired; its exit return
		// owns the summary then, so an idle one must not start (or overwrite).
		if !sess.isLive() {
			return
		}
		a.run(sess)
	}()
}

// finish marks st's run over; a return skipped meanwhile is armed again.
func (a *autoReturner) finish(key string, st *cloneSched) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st.running = false
	again := st.rerun
	st.rerun = false
	if len(st.sessions) == 0 {
		if a.clones[key] == st {
			delete(a.clones, key)
		}
		return
	}
	if again && st.timer == nil {
		now := a.now()
		due := now.Add(autoReturnDebounce)
		if earliest := st.lastRun.Add(autoReturnThrottle); earliest.After(due) {
			due = earliest
		}
		a.armLocked(key, st, due, now)
	}
}

// forget drops sess from its clone's schedule. When no watched session of the
// clone remains the timer is stopped and the schedule is dropped (kept only
// while a run is in flight, which finish then cleans up).
func (a *autoReturner) forget(sess *Session) {
	key := filepath.Clean(sess.Cwd)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.clones[key]
	if st == nil {
		return
	}
	delete(st.sessions, sess)
	if st.sess == sess {
		st.sess = nil
		for s := range st.sessions {
			st.sess = s
			break
		}
	}
	if len(st.sessions) > 0 {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.gen++ // a callback already on its way is stale
	if !st.running {
		delete(a.clones, key)
	}
}

func (s *Session) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status == statusLive
}

// newAutoReturner wires the real clock, timers and fingerprint, and a run that
// brings the clone home with the idle timeout.
func (m *Manager) newAutoReturner() *autoReturner {
	return newAutoReturner(time.Now,
		func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		clone.RefsFingerprint,
		func(sess *Session) { m.returnWorkTimeout(sess, ReturnOnIdle, cloneIdleReturnTimeout) })
}

// startAutoReturn watches a clone-mode box session's turns.
func (m *Manager) startAutoReturn(sess *Session) {
	if m.autoRet != nil && sess.Runtime == config.RuntimeBox && sess.CloneOf != "" {
		go m.watchTurns(sess)
	}
}

// watchTurns polls the session's activity and schedules a return each time a
// turn ends. It exits, and forgets the session, when the session finalizes or
// detaches (a recovered session gets a new watcher).
func (m *Manager) watchTurns(sess *Session) {
	t := time.NewTicker(autoReturnPoll)
	defer t.Stop()
	watchTurnsWith(m.autoRet, sess, t.C, func() bool { return m.sessionWorking(sess) })
}

// watchTurnsWith is watchTurns with its tick source and activity read injected.
func watchTurnsWith(a *autoReturner, sess *Session, tick <-chan time.Time, working func() bool) {
	a.attach(sess)
	defer a.forget(sess)
	prev := false
	for {
		select {
		case <-sess.Done():
			return
		case <-sess.detachedCh:
			return
		case <-tick:
			w := working()
			if prev && !w && sess.isLive() {
				a.turnEnded(sess)
			}
			prev = w
		}
	}
}

// sessionWorking is List's activity for one session, true iff it reads
// "working": the PTY verdict folded with the registry's status. It reads the
// registry entry only (boundTranscript), never the transcript, and takes m.mu
// only inside boundTranscript, never while a session lock is held.
func (m *Manager) sessionWorking(sess *Session) bool {
	act := sess.Info().Activity
	quiet, silent := sess.quietActivity()
	var reg string
	if l, _ := m.boundTranscript(sess.ID); l.reg != nil {
		reg = l.reg.Status
	}
	act, _ = regActivity(act, quiet, reg, silent)
	return act == "working"
}
