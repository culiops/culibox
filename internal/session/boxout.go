package session

import "github.com/culiops/agent-box/internal/box"

// BoxOutputDir is the host directory a boxed session's Playwright output is
// bind-mounted from (box.OutputDir). ok is false for an unknown id, a host
// session (no box), or a manager with no state dir: those have nothing the
// file endpoint may serve. The directory is written by the box, so a reader
// must open files in it through boxread.
func (m *Manager) BoxOutputDir(id string) (string, bool) {
	m.mu.Lock()
	s := m.sessions[id]
	stateDir := m.stateDir
	var boxName string
	if s != nil {
		boxName = s.Box
	}
	m.mu.Unlock()
	if boxName == "" || stateDir == "" {
		return "", false
	}
	return box.OutputDir(stateDir, boxName), true
}
