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
