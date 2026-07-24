// Package session is the domain core: an agent session is a named tmux
// session. It reads live state from tmux and composes spawns via launcher.
package session

import (
	"fmt"
	"time"

	"github.com/culiops/culiops-agent-console/internal/launcher"
	"github.com/culiops/culiops-agent-console/internal/tmux"
)

// Manager lists/spawns/kills sessions.
type Manager struct {
	Tmux     tmux.Client
	Launcher launcher.Builder
	NameFn   func(launcher.SpawnSpec) string
}

// DefaultNameFn derives a unique-ish session name from the agent + a timestamp.
func DefaultNameFn(s launcher.SpawnSpec) string {
	return fmt.Sprintf("%s-%d", s.Agent, time.Now().Unix())
}

// List returns live sessions from tmux.
func (m *Manager) List() ([]tmux.Session, error) {
	return m.Tmux.List()
}

// Spawn composes the agent command and starts a new tmux session for it.
func (m *Manager) Spawn(spec launcher.SpawnSpec) (tmux.Session, error) {
	argv, env, err := m.Launcher.Build(spec)
	if err != nil {
		return tmux.Session{}, err
	}
	name := m.NameFn(spec)
	if err := m.Tmux.New(name, spec.Cwd, argv, env); err != nil {
		return tmux.Session{}, err
	}
	return tmux.Session{Name: name}, nil
}

// Kill terminates a session by name.
func (m *Manager) Kill(name string) error {
	return m.Tmux.Kill(name)
}

// Get looks up a single live session.
func (m *Manager) Get(name string) (tmux.Session, bool, error) {
	sessions, err := m.List()
	if err != nil {
		return tmux.Session{}, false, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return s, true, nil
		}
	}
	return tmux.Session{}, false, nil
}
