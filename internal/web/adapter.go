package web

import (
	"log"
	"slices"

	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/egress"
	"github.com/culiops/agent-box/internal/launcher"
	"github.com/culiops/agent-box/internal/session"
	"github.com/culiops/agent-box/internal/ssologin"
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

func (a ManagerService) BoxOutputDir(id string) (string, bool) { return a.M.BoxOutputDir(id) }

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

// staticRecentRows is how many static-token rows a status reply carries.
const staticRecentRows = 5

// AWSStatusAdapter answers GET /api/sessions/{id}/aws from the session
// registry, the broker's counters and the audit store. It exposes the binding's
// identifiers and outcomes only: never credentials, tokens or nonces (none of
// which Info, Stats or Row carry).
type AWSStatusAdapter struct {
	Get   func(id string) (session.Info, bool)
	Stats func(id string) (broker.Stats, bool)
	// ConnStats is the per-connection counters of a session with named AWS
	// connections, by connection name; nil = every connection reads as zeros.
	ConnStats func(id string) map[string]broker.Stats
	// Recent is the session's rows of every provider but static tokens, so a
	// chatty token cannot crowd the AWS and GitHub history out of the reply.
	Recent func(id string, limit int) ([]broker.Row, error)
	// RecentStatic is the session's static-token rows (Subject = entry name);
	// nil = none are read.
	RecentStatic func(id string, limit int) ([]broker.Row, error)
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

// AWSStatusFor is ok only for a known session that has an AWS (inline or named
// connections), a GitHub or a token binding (the credential broker's audit rows
// and counters serve them). The
// device sign-in code and URL are in the reply only when cookie is true: they
// are shown to the browser session that asked for the flow, not to a token.
func (a AWSStatusAdapter) AWSStatusFor(id string, cookie bool) (any, bool) {
	info, ok := a.Get(id)
	hasConns := len(info.AWSConnections) > 0
	var conns []connStatus // the entries of out["connections"]; overlaid below
	if !ok || (info.AWS == nil && !hasConns && info.GitHub == nil && len(info.Tokens) == 0) {
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
	if hasConns {
		// Sorted by name already; never a source profile (AWSConnInfo has none).
		var cs map[string]broker.Stats
		if a.ConnStats != nil {
			cs = a.ConnStats(id)
		}
		conns = make([]connStatus, 0, len(info.AWSConnections))
		for _, c := range info.AWSConnections {
			conns = append(conns, connStatus{AWSConnInfo: c, Stats: cs[c.Name]})
		}
		out["connections"] = conns
	}
	if len(info.Tokens) > 0 {
		out["tokens"] = info.Tokens
	}
	if a.RecentStatic != nil && len(info.Tokens) > 0 {
		srows, err := a.RecentStatic(id, staticRecentRows)
		if err != nil {
			log.Printf("aws status %s: reading static token rows: %v", id, err)
		}
		if srows == nil {
			srows = []broker.Row{}
		}
		out["static_recent"] = srows
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
	if (info.AWS != nil && info.AWS.Elevate != nil) || connsCanElevate(info.AWSConnections) {
		gc := GrantContext{}
		if a.Grant != nil {
			gc = a.Grant(id)
		}
		if gc.HostSessions == nil {
			gc.HostSessions = []HostSession{} // [] on the wire, never null
		}
		el := map[string]any{"host_sessions": gc.HostSessions}
		if e := elevateOf(info); e != nil {
			el["role_arn"], el["max_minutes"], el["allow_iam"] = e.RoleARN, e.MaxMinutes, e.AllowIAM
		}
		if gc.Blocked != "" {
			el["blocked"] = gc.Blocked
		}
		if len(gc.ConnBlocked) > 0 {
			el["conn_blocked"] = gc.ConnBlocked
		}
		out["elevate"] = el
	}
	if a.Elevation != nil && (info.AWS != nil || hasConns) {
		if v, ok := a.Elevation(id); ok {
			out["elevation"] = v
			// The status reads the session without the list's AWS state, so the
			// window is overlaid here: on the connection it is for, while active.
			if v.Active && !v.Until.IsZero() && !info.Exited {
				for i := range conns {
					if conns[i].Name == v.Name {
						u := v.Until
						conns[i].ElevatedUntil = &u
					}
				}
			}
		}
	}
	if a.SSO != nil && (info.AWS != nil || hasConns) {
		if s, ok := a.SSO(id); ok {
			if !cookie {
				// Copies throughout: the callback's state is not ours to change.
				if s.Login != nil {
					s.Login = redactedLogin(s.Login)
				}
				if len(s.Sources) > 0 {
					srcs := make([]SSOSource, len(s.Sources))
					copy(srcs, s.Sources)
					for i := range srcs {
						if srcs[i].Login != nil {
							srcs[i].Login = redactedLogin(srcs[i].Login)
						}
					}
					s.Sources = srcs
				}
			}
			// A connection that signs in through an expired source is flagged with
			// that source's key, as the session list does.
			for _, src := range s.Sources {
				if !src.Expired {
					continue
				}
				for i := range conns {
					if slices.Contains(src.Names, conns[i].Name) {
						conns[i].SSOExpired = src.Source
					}
				}
			}
			out["sso"] = s
		}
	}
	return out, true
}

// redactedLogin is a copy of l without the device code and URL, which only the
// browser session that asked for the flow may see.
func redactedLogin(l *ssologin.State) *ssologin.State {
	c := *l
	c.URI, c.Code, c.URITrusted = "", "", false
	return &c
}

// connStatus is one entry of "connections": the session's view of the
// connection plus the broker's counters for it.
type connStatus struct {
	session.AWSConnInfo
	Stats broker.Stats `json:"stats"`
}

// connsCanElevate: at least one connection names an elevate role.
func connsCanElevate(cs []session.AWSConnInfo) bool {
	for _, c := range cs {
		if c.Elevate != nil {
			return true
		}
	}
	return false
}

// elevateOf is the inline binding's elevate block, nil for a session with
// connections (each carries its own under connections[].elevate).
func elevateOf(info session.Info) *session.ElevateInfo {
	if info.AWS != nil {
		return info.AWS.Elevate
	}
	return nil
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
