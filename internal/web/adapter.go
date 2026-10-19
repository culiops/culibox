package web

import (
	"log"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/session"
)

// ManagerService adapts *session.Manager to the Info-based SessionService the
// API depends on (so handlers never hold a live *Session).
type ManagerService struct{ M *session.Manager }

func (a ManagerService) List() []session.Info { return a.M.List() }

func (a ManagerService) Spawn(spec launcher.SpawnSpec) (session.Info, error) {
	sess, err := a.M.Spawn(spec)
	if err != nil {
		return session.Info{}, err
	}
	return sess.Info(), nil
}

func (a ManagerService) Kill(id string) error { return a.M.Kill(id) }

func (a ManagerService) Rename(id, name string) error { return a.M.Rename(id, name) }

func (a ManagerService) Get(id string) (session.Info, bool) {
	sess, ok := a.M.Get(id)
	if !ok {
		return session.Info{}, false
	}
	return sess.Info(), true
}

func (a ManagerService) UsageFor(id string) (session.Usage, bool) { return a.M.UsageFor(id) }

func (a ManagerService) PeekFor(id string) (session.Peek, bool) { return a.M.PeekFor(id) }

func (a ManagerService) AttributionFor(id string) (session.Attribution, bool) {
	return a.M.AttributionFor(id)
}

func (a ManagerService) SetReplyable(id string, v bool) error { return a.M.SetReplyable(id, v) }

func (a ManagerService) SendMessage(id, text string) error { return a.M.SendMessage(id, text) }

func (a ManagerService) Interrupt(id string) error { return a.M.Interrupt(id) }

func (a ManagerService) Answer(id, choice, askSHA256 string) error {
	return a.M.Answer(id, choice, askSHA256)
}

func (a ManagerService) Restart(id string) (session.Info, error) {
	s, err := a.M.Restart(id)
	if err != nil {
		return session.Info{}, err
	}
	return s.Info(), nil
}

// awsRecentRows is how many audit rows a status reply carries. Fixed, not a
// query parameter: the audit store reads limit <= 0 as unlimited.
const awsRecentRows = 20

// AWSStatusAdapter answers GET /api/sessions/{id}/aws from the session
// registry, the broker's counters and the audit store. It exposes the binding's
// identifiers and outcomes only: never credentials, tokens or nonces (none of
// which Info, Stats or Row carry).
type AWSStatusAdapter struct {
	Get    func(id string) (session.Info, bool)
	Stats  func(id string) (broker.Stats, bool)
	Recent func(id string, limit int) ([]broker.Row, error)
	// Egress reports the session's proxy counters; nil = egress control is not
	// running, and the reply carries no egress object. ok is false until the
	// session's first tunnel or denial, which reads as zeros, never a hidden row.
	Egress func(id string) (egress.Stats, bool)
	// Elevation is the session's write window (ok false: none and nothing issued
	// still valid); nil = elevation is not wired, and the reply has no elevation.
	Elevation func(id string) (broker.ElevationView, bool)
	// SSO is the session's AWS sign-in state, with the host command and any
	// running flow; nil or ok false leaves the reply without an sso object.
	SSO func(id string) (SSOStatus, bool)
	// Grant fills the grant panel's host-session warning and any refusal reason.
	// It is called only for a binding that can elevate (it lists boxes).
	Grant func(id string) GrantContext
}

// AWSStatus is the status for a caller that is not a signed-in browser session:
// the sign-in code and URL are left out.
func (a AWSStatusAdapter) AWSStatus(id string) (any, bool) { return a.AWSStatusFor(id, false) }

// AWSStatusFor is ok only for a known session that has an AWS or a GitHub
// binding (the credential broker's audit rows and counters serve both). The
// device sign-in code and URL are in the reply only when cookie is true: they
// are shown to the browser session that asked for the flow, not to a token.
func (a AWSStatusAdapter) AWSStatusFor(id string, cookie bool) (any, bool) {
	info, ok := a.Get(id)
	if !ok || (info.AWS == nil && info.GitHub == nil) {
		return nil, false
	}
	stats, _ := a.Stats(id)
	rows, err := a.Recent(id, awsRecentRows)
	if err != nil {
		// The status is still useful without the history; the error text is
		// for the daemon log, not the reply.
		log.Printf("aws status %s: reading audit rows: %v", id, err)
	}
	if rows == nil {
		rows = []broker.Row{}
	}
	out := map[string]any{"stats": stats, "recent": rows}
	if info.AWS != nil {
		out["label"], out["account_id"], out["role_arn"], out["prod"] = info.AWS.Label, info.AWS.AccountID, info.AWS.RoleARN, info.AWS.Prod
	}
	if g := info.GitHub; g != nil {
		owners := g.Owners
		if owners == nil {
			owners = []string{} // [] on the wire, never null
		}
		out["github"] = map[string]any{"owners": owners, "repos": g.Repos}
	}
	if a.Egress != nil && info.Egress != "" {
		es, _ := a.Egress(id)
		out["egress"] = map[string]any{"mode": info.Egress, "allowed": es.Allowed, "denied": es.Denied}
	}
	if info.AWS != nil && info.AWS.Elevate != nil {
		e := info.AWS.Elevate
		gc := GrantContext{}
		if a.Grant != nil {
			gc = a.Grant(id)
		}
		if gc.HostSessions == nil {
			gc.HostSessions = []HostSession{} // [] on the wire, never null
		}
		el := map[string]any{
			"role_arn": e.RoleARN, "max_minutes": e.MaxMinutes, "allow_iam": e.AllowIAM,
			"host_sessions": gc.HostSessions,
		}
		if gc.Blocked != "" {
			el["blocked"] = gc.Blocked
		}
		out["elevate"] = el
	}
	if a.Elevation != nil && info.AWS != nil {
		if v, ok := a.Elevation(id); ok {
			out["elevation"] = v
		}
	}
	if a.SSO != nil && info.AWS != nil {
		if s, ok := a.SSO(id); ok {
			if s.Login != nil && !cookie {
				l := *s.Login
				l.URI, l.Code, l.URITrusted = "", "", false
				s.Login = &l
			}
			out["sso"] = s
		}
	}
	return out, true
}

// EgressStatusAdapter serves the egress line for a boxed session, bound to AWS
// or not. It is ok only for a known session that has egress control.
type EgressStatusAdapter struct {
	Get   func(id string) (session.Info, bool)
	Stats func(id string) (egress.Stats, bool)
}

// EgressStatus is ok only for a known session whose Info.Egress is set. Stats
// is false until the session has had a tunnel or a denial; that reads as zeros,
// not as a missing line.
func (a EgressStatusAdapter) EgressStatus(id string) (any, bool) {
	info, ok := a.Get(id)
	if !ok || info.Egress == "" {
		return nil, false
	}
	es, _ := a.Stats(id)
	return map[string]any{"egress": map[string]any{"mode": info.Egress, "allowed": es.Allowed, "denied": es.Denied}}, true
}
