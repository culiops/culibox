package web

import (
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
