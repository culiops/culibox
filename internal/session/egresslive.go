package session

import (
	"slices"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/hostmatch"
)

// EgressLiveResolver is implemented by launchers that can say what a profile's
// egress policy is now (#95). ok is false when the profile no longer exists or
// no longer runs in a box: its running sessions then fail closed.
type EgressLiveResolver interface {
	LiveEgressFor(agent string) (allow []string, open, ok bool)
}

// EgressPolicyHooks is the optional part of EgressHooks that lets a running
// session's policy change (#95). PolicyChanged closes the session's tunnels its
// new policy refuses, audits the change (summary describes it) and reports how
// many tunnels it closed. The manager calls it after it has stored the policy.
type EgressPolicyHooks interface {
	PolicyChanged(sessionID, box, summary string) int
}

// egressPolicy is a profile's own part of a bound session's egress policy:
// egress_allow and egress_open. The bindings' hosts are not in it; they are
// fixed at spawn.
type egressPolicy struct {
	Allow []string `json:"allow,omitempty"`
	Open  bool     `json:"open,omitempty"`
}

// norm is p with Allow sorted and deduplicated (nil when empty), so two
// policies compare by what they allow, not by how a profile listed it.
func (p egressPolicy) norm() egressPolicy {
	a := slices.Clone(p.Allow)
	slices.Sort(a)
	a = slices.Compact(a)
	if len(a) == 0 {
		a = nil
	}
	return egressPolicy{Allow: a, Open: p.Open}
}

func (p egressPolicy) equal(q egressPolicy) bool {
	p, q = p.norm(), q.norm()
	return p.Open == q.Open && slices.Equal(p.Allow, q.Allow)
}

// covers reports whether p admits every host pattern pat admits.
func (p egressPolicy) covers(pat string) bool {
	if p.Open {
		return true
	}
	if base, wild := strings.CutPrefix(pat, "*."); wild {
		// *.x is covered by *.x itself or by a wildcard of one of x's parents
		// (*.a.x under *.x); an exact host never covers a wildcard.
		return slices.ContainsFunc(p.Allow, func(q string) bool {
			qb, ok := strings.CutPrefix(q, "*.")
			return ok && (base == qb || strings.HasSuffix(base, "."+qb))
		})
	}
	return slices.Contains(p.Allow, pat) || hostmatch.Match(p.Allow, pat)
}

// meet is the widest policy no wider than any of ps: open only when every one
// is, and the patterns of each that every other one covers. Sorted and
// deduplicated; none at all is the closed policy.
func meet(ps []egressPolicy) egressPolicy {
	if len(ps) == 0 {
		return egressPolicy{}
	}
	out := egressPolicy{Open: true}
	for i, p := range ps {
		out.Open = out.Open && p.Open
		for _, pat := range p.Allow {
			ok := true
			for j, q := range ps {
				if j != i && !q.covers(pat) {
					ok = false
					break
				}
			}
			if ok {
				out.Allow = append(out.Allow, pat)
			}
		}
	}
	return out.norm()
}

// Reasons a session's applied policy is narrower than its own profile's.
const (
	EgressHeldSharedBox   = "shared_box"   // another profile with a narrower policy has a session in the box
	EgressHeldProfileGone = "profile_gone" // the profile was removed or renamed, or no longer runs in a box
)

// egressLive is a bound boxed session's live egress policy (#95), guarded by
// the session's mu. applied is what the proxy enforces; want is what the
// session's own profile asks for (the closed policy when gone). They differ
// when the box is shared with a narrower profile.
type egressLive struct {
	set     bool // false: a Session built without Spawn or restore; the spawn-time policy applies
	applied egressPolicy
	want    egressPolicy
	gone    bool
	changed time.Time // the last change of applied; zero when never changed
	closed  int       // tunnels the last change closed
}

// EgressChangeInfo is Info.EgressChange: how the session's egress policy
// differs from the one it was spawned with, and what its profile asks for
// that its box holds back. Host patterns only, never a credential.
type EgressChangeInfo struct {
	At      time.Time `json:"at,omitempty"`      // the last change; zero when only a hold is shown
	Added   []string  `json:"added,omitempty"`   // profile hosts allowed now, not at spawn
	Removed []string  `json:"removed,omitempty"` // profile hosts allowed at spawn, not now
	Open    bool      `json:"open"`              // egress_open applies now
	// OpenChanged: Open differs from the spawn.
	OpenChanged bool `json:"open_changed,omitempty"`
	// Held and HeldOpen are what the session's profile asks for that does not
	// apply, and Reason why (EgressHeld*).
	Held     []string `json:"held,omitempty"`
	HeldOpen bool     `json:"held_open,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Closed   int      `json:"closed,omitempty"` // tunnels the last change closed
}

// minus is the patterns of a not in b (both normalised).
func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// egressChangeLocked builds Info.EgressChange; nil when the applied policy is
// the spawn-time one and nothing is held. s.mu must be held.
func (s *Session) egressChangeLocked() *EgressChangeInfo {
	if s.egressFixed() {
		return nil
	}
	l := s.egressLocked()
	spawn := egressPolicy{Allow: s.EgressAllow, Open: s.EgressOpen}.norm()
	ap, want := l.applied, l.want
	c := &EgressChangeInfo{At: l.changed, Added: minus(ap.Allow, spawn.Allow), Removed: minus(spawn.Allow, ap.Allow),
		Open: ap.Open, OpenChanged: ap.Open != spawn.Open, Closed: l.closed}
	if l.gone {
		c.Reason = EgressHeldProfileGone
	} else if !ap.equal(want) {
		c.Held, c.HeldOpen, c.Reason = minus(want.Allow, ap.Allow), want.Open && !ap.Open, EgressHeldSharedBox
	}
	if len(c.Added) == 0 && len(c.Removed) == 0 && !c.OpenChanged && c.Reason == "" {
		return nil
	}
	if len(c.Added) == 0 && len(c.Removed) == 0 && !c.OpenChanged {
		c.At, c.Closed = time.Time{}, 0 // back at the spawn policy: only the hold is news
	}
	return c
}

// egressFixed reports whether the session has no live policy: not bound at
// spawn, or not behind the proxy. Immutable fields only.
func (s *Session) egressFixed() bool {
	return s.EgressNonce == "" || !s.Bindings().Any()
}

// EgressPolicy is the profile part of the policy the proxy enforces for the
// session now: the spawn-time one until a profile edit changes it (#95).
func (s *Session) EgressPolicy() (allow []string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.egressLocked()
	return slices.Clone(l.applied.Allow), l.applied.Open
}

// egressLocked is the session's live policy; s.mu must be held.
func (s *Session) egressLocked() egressLive {
	if !s.egress.set {
		return newEgressLive(egressPolicy{Allow: s.EgressAllow, Open: s.EgressOpen}, nil)
	}
	return s.egress
}

// egressSummary describes the change from old to cur in the audit row:
// "+added -removed open:on|off".
func egressSummary(old, cur egressPolicy) string {
	old, cur = old.norm(), cur.norm()
	var parts []string
	for _, h := range minus(cur.Allow, old.Allow) {
		parts = append(parts, "+"+h)
	}
	for _, h := range minus(old.Allow, cur.Allow) {
		parts = append(parts, "-"+h)
	}
	if old.Open != cur.Open {
		if cur.Open {
			parts = append(parts, "open:on")
		} else {
			parts = append(parts, "open:off")
		}
	}
	return strings.Join(parts, " ")
}

// RefreshEgress re-reads every bound boxed session's profile policy and applies,
// per box, the meet of its members' policies (#95). The profile manager calls it
// after every change to the profile set, the first time at boot; until then the
// per-box refreshes on spawn and exit do nothing, because the launcher may not
// know the stored profiles yet.
func (m *Manager) RefreshEgress() { m.refreshEgress("", true) }

// refreshEgressBox recomputes the meet of one box after its membership changed
// (a session published into it or gone from the registry).
func (m *Manager) refreshEgressBox(s *Session) {
	if s.Runtime != config.RuntimeBox || s.Box == "" || s.egressFixed() {
		return
	}
	m.refreshEgress(s.Box, false)
}

func (m *Manager) refreshEgress(onlyBox string, all bool) {
	m.egressMu.Lock()
	defer m.egressMu.Unlock()
	m.mu.Lock()
	if all {
		m.egressLiveOn = true
	} else if !m.egressLiveOn {
		m.mu.Unlock()
		return
	}
	r, _ := m.launcher.(EgressLiveResolver)
	hooks, _ := m.egress.(EgressPolicyHooks)
	sc := m.sidecar
	var members []*Session
	for _, s := range m.sessions {
		if s.Runtime == config.RuntimeBox && s.Box != "" && !s.egressFixed() && (onlyBox == "" || s.Box == onlyBox) {
			members = append(members, s)
		}
	}
	m.mu.Unlock()
	if r == nil {
		return
	}
	type member struct {
		s    *Session
		want egressPolicy
		gone bool
	}
	boxes := map[string][]member{}
	for _, s := range members {
		if s.Exited() {
			continue // no process left to use a token
		}
		allow, open, ok := r.LiveEgressFor(s.Agent)
		want := egressPolicy{Allow: allow, Open: open}.norm()
		if !ok {
			want = egressPolicy{}
		}
		boxes[s.Box] = append(boxes[s.Box], member{s, want, !ok})
	}
	now := time.Now()
	for _, ms := range boxes {
		wants := make([]egressPolicy, len(ms))
		for i, x := range ms {
			wants[i] = x.want
		}
		applied := meet(wants)
		for _, x := range ms {
			s := x.s
			s.mu.Lock()
			l := s.egressLocked()
			old := l.applied
			l.want, l.gone = x.want, x.gone
			changed := !old.equal(applied)
			if changed {
				l.applied, l.changed, l.closed = applied, now, 0
			}
			s.egress = l
			s.mu.Unlock()
			if !changed {
				continue
			}
			if sc != nil {
				sc.setEgressLive(s.ID, &egressLiveMeta{egressPolicy: applied, Changed: now})
			}
			if hooks != nil {
				n := hooks.PolicyChanged(s.ID, s.Box, egressSummary(old, applied))
				s.mu.Lock()
				if s.egress.changed.Equal(now) {
					s.egress.closed = n
				}
				s.mu.Unlock()
			}
		}
	}
}

// newEgressLive is a session's live policy at spawn or restore: the spawn-time
// one, or the one a live change left in the sidecar.
func newEgressLive(spawn egressPolicy, md *egressLiveMeta) egressLive {
	l := egressLive{set: true, applied: spawn.norm(), want: spawn.norm()}
	if md != nil {
		l.applied, l.want, l.changed = md.egressPolicy.norm(), md.egressPolicy.norm(), md.Changed
	}
	return l
}

// joinBoxEgressLocked lowers a session about to be published into its box to
// the meet of its own policy and the policy every live member of that box
// applies now, so a co-tenant never borrows a policy wider than the box's
// through the newcomer's token in the moment before refreshEgressBox runs
// (#95). It returns the sidecar record when the policy was lowered, else nil.
// m.mu must be held (m.mu -> s.mu is the lock order).
func (m *Manager) joinBoxEgressLocked(sess *Session, now time.Time) *egressLiveMeta {
	if sess.Runtime != config.RuntimeBox || sess.Box == "" || sess.egressFixed() {
		return nil
	}
	own := sess.egress.applied
	ps := []egressPolicy{own}
	for _, s := range m.sessions {
		if s == sess || s.Box != sess.Box || s.Runtime != config.RuntimeBox || s.egressFixed() {
			continue
		}
		s.mu.Lock()
		if !s.exited {
			ps = append(ps, s.egressLocked().applied)
		}
		s.mu.Unlock()
	}
	applied := meet(ps)
	if applied.equal(own) {
		return nil
	}
	sess.egress.applied, sess.egress.changed = applied, now
	return &egressLiveMeta{egressPolicy: applied, Changed: now}
}

// egressLiveMeta is the sidecar's record of a session's applied policy once it
// has changed (#95); absent until then and in records from before.
type egressLiveMeta struct {
	egressPolicy
	Changed time.Time `json:"changed"`
}
