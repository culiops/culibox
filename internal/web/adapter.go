package web

import (
	"log"

	"github.com/culiops/agent-box/internal/broker"
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
}

// AWSStatus is ok only for a known session that has an AWS binding.
func (a AWSStatusAdapter) AWSStatus(id string) (any, bool) {
	info, ok := a.Get(id)
	if !ok || info.AWS == nil {
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
	return map[string]any{
		"label": info.AWS.Label, "account_id": info.AWS.AccountID, "role_arn": info.AWS.RoleARN,
		"prod": info.AWS.Prod, "stats": stats, "recent": rows,
	}, true
}
