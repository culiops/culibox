package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/session"
	"github.com/culiops/culibox/internal/ssologin"
	"github.com/culiops/culibox/internal/web"
)

// grantCheckTimeout bounds the box listing a grant (or the grant panel) waits on.
const grantCheckTimeout = 5 * time.Second

// errClaimUnchecked is what a grant is refused with when the workspace check
// itself failed: fixed text, the cause goes to the daemon log.
var errClaimUnchecked = errors.New("could not check this folder's boxes; try again")

// ssoRunner is the sign-in runner as the service uses it (*ssologin.Runner).
type ssoRunner interface {
	Start(key, profile string, done ssologin.Done) (ssologin.State, error)
	Status(key string) (ssologin.State, bool)
}

// elevationService is the daemon's side of write elevation and the in-UI SSO
// sign-in: the broker holds the state, this resolves ids to sessions and
// supplies the host-side checks. It holds only pointers and funcs.
type elevationService struct {
	brk     *broker.Broker
	runner  ssoRunner // nil = no aws CLI on the daemon's PATH
	get     func(id string) (*session.Session, bool)
	list    func() []session.Info
	blocker boxCheck                    // box.Manager.GrantBlocker: the grant panel's read-only view
	claim   boxCheck                    // box.Manager.ClaimForGrant: the grant's own check
	exited  func(*session.Session) bool // nil = (*session.Session).Exited; a seam for tests
}

// boxCheck is the box layer's answer to "what stands in the way of a grant in
// this folder".
type boxCheck func(ctx context.Context, cwd, self string) (box.GrantBlock, error)

var _ web.ElevationService = elevationService{}

// bound is the session with that id when it is a live boxed session with an
// AWS binding (inline or named connections): the only kind elevation and the
// sign-in card apply to.
func (e elevationService) bound(id string) (*session.Session, bool) {
	s, ok := e.boundOrExited(id)
	if !ok || e.isExited(s) {
		return nil, false
	}
	return s, true
}

func (e elevationService) isExited(s *session.Session) bool {
	if e.exited != nil {
		return e.exited(s)
	}
	return s.Exited()
}

// boundOrExited is bound without the liveness test: a boxed session with an AWS
// binding (inline or named connections) that may already have exited but is
// still listed.
func (e elevationService) boundOrExited(id string) (*session.Session, bool) {
	s, ok := e.get(id)
	if !ok || s == nil || (s.AWS == nil && len(s.AWSConns) == 0) || s.Runtime != config.RuntimeBox {
		return nil, false
	}
	return s, true
}

// Grant asks the broker for a write window. The broker resolves req.Name
// against the session's own connections and applies every refusal.
func (e elevationService) Grant(id string, req broker.ElevateRequest) (broker.ElevationView, error) {
	if _, ok := e.bound(id); !ok {
		return broker.ElevationView{}, web.ErrElevationUnknown
	}
	return e.brk.Elevate(id, req)
}

// Drop ends the window. Dropping when none is active is still a success: the
// reply is the inactive view. It also works for an exited session that is still
// listed: dropping only lowers power, and an overlay outlives its session.
func (e elevationService) Drop(id, cred string) (broker.ElevationView, error) {
	if _, ok := e.boundOrExited(id); !ok {
		return broker.ElevationView{}, web.ErrElevationUnknown
	}
	v, _ := e.brk.Drop(id, cred)
	return v, nil
}

func (e elevationService) Refused(id, cred, reason string) {
	e.brk.RefuseElevation(id, cred, reason)
}

// SSOLogin starts (or joins) the sign-in for the session's recorded source
// profile: the inline binding's (name ""), or that of the connection name
// names. The profile comes from the session, never from the request; a name
// the session does not have, or a missing one where it has connections, is
// web.ErrConnectionName. Any other failure, or no aws CLI on this host, is an
// SSOLoginError carrying the command the human can run instead.
func (e elevationService) SSOLogin(id, name string) (ssologin.State, error) {
	s, ok := e.bound(id)
	if !ok {
		return ssologin.State{}, web.ErrElevationUnknown
	}
	p, ok := recordedProfile(s, name)
	if !ok {
		return ssologin.State{}, web.ErrConnectionName
	}
	command := hostLoginCommand(p)
	if command == "" { // a restored binding is not re-validated at load: do it here
		return ssologin.State{}, &web.SSOLoginError{Err: errors.New("the session's source profile name is not valid")}
	}
	if e.runner == nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: errors.New("the aws CLI is not on the daemon's PATH")}
	}
	key := e.brk.SourceKeyOf(p)
	st, err := e.runner.Start(key, p, func(ok bool, detail string) { e.brk.SSOLoginDone(key, id, name, ok, detail) })
	if err != nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: err}
	}
	return st, nil
}

// recordedProfile is the source profile recorded for name on session s: the
// inline binding's for "", a named connection's otherwise. ok is false when
// the name does not fit the session (a name on the legacy binding, none or an
// unbound one where it has connections).
func recordedProfile(s *session.Session, name string) (string, bool) {
	if len(s.AWSConns) > 0 {
		if name == "" {
			return "", false
		}
		c, ok := config.AWSConnByName(s.AWSConns, name)
		return c.SourceProfile, ok
	}
	if s.AWS == nil || name != "" {
		return "", false
	}
	return s.AWS.SourceProfile, true
}

// hostLoginCommand is the command a human can run on the host to sign in
// for profile p, or "" when p is not a name the sign-in accepts: the string is
// pasted into a shell, so it is only built from a validated name.
func hostLoginCommand(p string) string {
	if !ssologin.ValidProfile(p) {
		return ""
	}
	return "aws sso login --profile " + p
}

// claimCheck is the broker's grant-time check (spec correction 8): no other
// running box, of any instance, may use this session's workspace. It runs
// after the broker has started the session's write hold, so the box layer's
// ClaimForGrant either sees a spawn racing the grant or that spawn sees the
// hold (#325). A check that could not run refuses the grant.
func (e elevationService) claimCheck(id string) error {
	s, ok := e.get(id)
	if !ok || s == nil {
		return web.ErrElevationUnknown
	}
	msg, err := e.blockerFor(s, e.claim)
	if err != nil {
		log.Printf("grant claim check for %s: %v", id, err)
		return errClaimUnchecked
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

// blockerFor runs check for session s and words what it found: a box of this
// instance is named by the live sessions in it (#325), anything else by the
// box layer's own sentence.
func (e elevationService) blockerFor(s *session.Session, check boxCheck) (string, error) {
	if check == nil {
		return "", errors.New("no box manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), grantCheckTimeout)
	defer cancel()
	gb, err := check(ctx, s.Cwd, s.Box)
	if err != nil || gb.Why == "" {
		return "", err
	}
	if gb.Box == "" {
		return gb.Why, nil
	}
	var labels []string
	if e.list != nil {
		for _, in := range e.list() {
			if in.Exited || in.ID == s.ID {
				continue
			}
			if o, ok := e.get(in.ID); ok && o != nil && o.Box == gb.Box {
				labels = append(labels, sessionLabel(in))
			}
		}
	}
	switch len(labels) {
	case 0:
		return gb.Why, nil
	case 1:
		return labels[0] + " uses this folder in another box; end it first, then grant write access " +
			"(no other box may share the folder while a write window is open)", nil
	}
	return strings.Join(labels, ", ") + " use this folder in another box; end them first, then grant write access " +
		"(no other box may share the folder while a write window is open)", nil
}

// sessionLabel names a session in a message: its display name and id, or the
// id alone when it has no name.
func sessionLabel(in session.Info) string {
	if in.Name == "" {
		return "session " + in.ID
	}
	return fmt.Sprintf("session %q (%s)", in.Name, in.ID)
}

// writeHold is the box layer's write-hold hook (box.WriteHold, #325): the
// broker's answer for the session, led by its name. It runs under the box
// layer's opMu, so it takes as little as it can: the broker's lock and one
// session lookup (the manager's lock, then that session's own), neither of
// which ever waits on opMu. Never the full session list.
func (e elevationService) writeHold(sid string, adoptedAt time.Time) string {
	why := e.brk.WriteHold(sid, adoptedAt)
	if why == "" {
		return ""
	}
	if sid == "" { // a box reaped at boot: its session is not known (#333)
		return "a session " + why
	}
	in := session.Info{ID: sid}
	if e.get != nil {
		if s, ok := e.get(sid); ok && s != nil {
			in.Name = s.Name()
		}
	}
	return sessionLabel(in) + " " + why
}

// ssoStatus is the sso object of GET .../aws: whether the session waits on a
// sign-in, the host command, and the flow in progress. ok is false when there
// is nothing to say. A session with connections gets one Sources entry per
// sign-in source that is expired or has a flow.
func (e elevationService) ssoStatus(id string) (web.SSOStatus, bool) {
	s, ok := e.bound(id)
	if !ok {
		return web.SSOStatus{}, false
	}
	if len(s.AWSConns) > 0 {
		return e.connSSOStatus(id, s.AWSConns)
	}
	p := s.AWS.SourceProfile
	key := e.brk.SSOExpired(id)
	out := web.SSOStatus{Expired: key != "", Source: key}
	if e.runner != nil && ssologin.ValidProfile(p) {
		runKey := key
		if runKey == "" {
			runKey = e.brk.SourceKeyOf(p)
		}
		if st, found := e.runner.Status(runKey); found {
			out.Login = &st
			out.Source = runKey
		}
	}
	if !out.Expired && out.Login == nil {
		return web.SSOStatus{}, false
	}
	out.Command = hostLoginCommand(p) // "" when the recorded name is not valid
	return out, true
}

// connSSOStatus groups a session's connections (sorted by name) by sign-in
// source. A connection whose mint failed is keyed on the source it failed on;
// the others on their profile's current source. The command names the first
// connection's recorded profile in each group.
func (e elevationService) connSSOStatus(id string, conns []config.AWSConnection) (web.SSOStatus, bool) {
	expired := e.brk.SSOExpiredConns(id)
	var out web.SSOStatus
	at := map[string]int{} // source -> its index in out.Sources
	for _, c := range conns {
		key, isExpired := expired[c.Name]
		if !isExpired {
			key = e.brk.SourceKeyOf(c.SourceProfile)
		}
		i, seen := at[key]
		if !seen {
			i = len(out.Sources)
			at[key] = i
			out.Sources = append(out.Sources, web.SSOSource{Source: key, Command: hostLoginCommand(c.SourceProfile)})
		}
		src := &out.Sources[i]
		src.Names = append(src.Names, c.Name)
		src.Expired = src.Expired || isExpired
	}
	kept := out.Sources[:0]
	for _, src := range out.Sources {
		if e.runner != nil && src.Command != "" { // a valid recorded profile, so a flow may exist
			if st, found := e.runner.Status(src.Source); found {
				src.Login = &st
			}
		}
		if src.Expired || src.Login != nil {
			kept = append(kept, src)
		}
	}
	if len(kept) == 0 {
		return web.SSOStatus{}, false
	}
	out.Sources = kept
	out.Source = kept[0].Source
	for _, src := range kept {
		out.Expired = out.Expired || src.Expired
	}
	return out, true
}

// grantContext fills the grant panel: the live host-tier sessions (which are
// not contained and can reach the same credentials the box cannot) and why a
// grant would be refused right now. For a session with connections it also
// says, per write-capable connection, what another connection's window or
// leftover write credentials block.
func (e elevationService) grantContext(id string) web.GrantContext {
	gc := web.GrantContext{HostSessions: []web.HostSession{}}
	s, ok := e.bound(id)
	if !ok || !elevatable(s) {
		// Nothing can be granted here (exited, unbound, not elevatable), so
		// there is no panel to fill: skip the box listing that would otherwise
		// run on every poll of GET .../aws.
		return gc
	}
	for _, c := range s.AWSConns {
		if c.Elevate == nil {
			continue
		}
		if msg := e.brk.GrantConflict(id, c.Name); msg != "" {
			if gc.ConnBlocked == nil {
				gc.ConnBlocked = map[string]string{}
			}
			gc.ConnBlocked[c.Name] = msg
		}
	}
	if e.list != nil {
		for _, in := range e.list() {
			if in.Runtime == config.RuntimeHost && !in.Exited {
				gc.HostSessions = append(gc.HostSessions, web.HostSession{ID: in.ID, Name: in.Name})
			}
		}
	}
	msg, err := e.blockerFor(s, e.blocker)
	if err != nil {
		log.Printf("grant context for %s: %v", id, err)
		gc.Blocked = errClaimUnchecked.Error()
		return gc
	}
	gc.Blocked = msg
	return gc
}

// elevatable: the session's inline binding, or at least one of its
// connections, names an elevate role.
func elevatable(s *session.Session) bool {
	if s.AWS != nil {
		return s.AWS.Elevate != nil
	}
	for _, c := range s.AWSConns {
		if c.Elevate != nil {
			return true
		}
	}
	return false
}
