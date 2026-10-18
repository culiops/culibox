package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/ssologin"
	"github.com/culiops/agent-box/internal/web"
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
	blocker func(ctx context.Context, cwd, self string) (string, error) // box.Manager.GrantBlocker
	exited  func(*session.Session) bool                                 // nil = (*session.Session).Exited; a seam for tests
}

var _ web.ElevationService = elevationService{}

// bound is the session with that id when it is a live boxed session with an
// AWS binding: the only kind elevation and the sign-in card apply to.
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
// binding that may already have exited but is still listed.
func (e elevationService) boundOrExited(id string) (*session.Session, bool) {
	s, ok := e.get(id)
	if !ok || s == nil || s.AWS == nil || s.Runtime != config.RuntimeBox {
		return nil, false
	}
	return s, true
}

func (e elevationService) Grant(id string, minutes int, reason, label, cred string) (broker.ElevationView, error) {
	if _, ok := e.bound(id); !ok {
		return broker.ElevationView{}, web.ErrElevationUnknown
	}
	return e.brk.Elevate(id, broker.ElevateRequest{Minutes: minutes, Reason: reason, ConfirmLabel: label, Cred: cred})
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
// profile. The profile comes from the binding, never from the request. Any
// failure, or no aws CLI on this host, is an SSOLoginError carrying the command
// the human can run instead.
func (e elevationService) SSOLogin(id string) (ssologin.State, error) {
	s, ok := e.bound(id)
	if !ok {
		return ssologin.State{}, web.ErrElevationUnknown
	}
	p := s.AWS.SourceProfile
	command := hostLoginCommand(p)
	if command == "" { // a restored binding is not re-validated at load: do it here
		return ssologin.State{}, &web.SSOLoginError{Err: errors.New("the session's source profile name is not valid")}
	}
	if e.runner == nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: errors.New("the aws CLI is not on the daemon's PATH")}
	}
	key := e.brk.SourceKeyOf(p)
	st, err := e.runner.Start(key, p, func(ok bool, detail string) { e.brk.SSOLoginDone(key, id, ok, detail) })
	if err != nil {
		return ssologin.State{}, &web.SSOLoginError{Command: command, Err: err}
	}
	return st, nil
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
// running box, of any instance, may use this session's workspace. A check that
// could not run refuses the grant.
func (e elevationService) claimCheck(id string) error {
	s, ok := e.get(id)
	if !ok || s == nil {
		return web.ErrElevationUnknown
	}
	msg, err := e.blockerFor(s)
	if err != nil {
		log.Printf("grant claim check for %s: %v", id, err)
		return errClaimUnchecked
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

func (e elevationService) blockerFor(s *session.Session) (string, error) {
	if e.blocker == nil {
		return "", errors.New("no box manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), grantCheckTimeout)
	defer cancel()
	return e.blocker(ctx, s.Cwd, s.Box)
}

// ssoStatus is the sso object of GET .../aws: whether the session waits on a
// sign-in, the host command, and the flow in progress. ok is false when there
// is nothing to say.
func (e elevationService) ssoStatus(id string) (web.SSOStatus, bool) {
	s, ok := e.bound(id)
	if !ok {
		return web.SSOStatus{}, false
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

// grantContext fills the grant panel: the live host-tier sessions (which are
// not contained and can reach the same credentials the box cannot) and why a
// grant would be refused right now.
func (e elevationService) grantContext(id string) web.GrantContext {
	gc := web.GrantContext{HostSessions: []web.HostSession{}}
	s, ok := e.bound(id)
	if !ok || s.AWS.Elevate == nil {
		// Nothing can be granted here (exited, unbound, not elevatable), so
		// there is no panel to fill: skip the box listing that would otherwise
		// run on every poll of GET .../aws.
		return gc
	}
	if e.list != nil {
		for _, in := range e.list() {
			if in.Runtime == config.RuntimeHost && !in.Exited {
				gc.HostSessions = append(gc.HostSessions, web.HostSession{ID: in.ID, Name: in.Name})
			}
		}
	}
	msg, err := e.blockerFor(s)
	if err != nil {
		log.Printf("grant context for %s: %v", id, err)
		gc.Blocked = errClaimUnchecked.Error()
		return gc
	}
	gc.Blocked = msg
	return gc
}
