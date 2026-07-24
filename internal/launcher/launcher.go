// Package launcher composes the command + env used to start an agent in a
// new tmux session. In M1 it only resolves the agent binary; MCP/skills
// toggles (M3) plug in here via the same Build interface.
package launcher

import (
	"fmt"

	"github.com/culiops/culiops-agent-console/internal/config"
)

// SpawnSpec is the request to launch an agent. It lives here (not in
// session) so both session and launcher can reference it without an
// import cycle.
type SpawnSpec struct {
	Agent string
	Cwd   string
}

// Builder composes the argv + env for a spawn.
type Builder interface {
	Build(spec SpawnSpec) (argv []string, env []string, err error)
}

// AgentLauncher builds commands from configured agents.
type AgentLauncher struct {
	Agents map[string]config.AgentConfig
}

// New indexes the configured agents by name.
func New(agents []config.AgentConfig) *AgentLauncher {
	m := make(map[string]config.AgentConfig, len(agents))
	for _, a := range agents {
		m[a.Name] = a
	}
	return &AgentLauncher{Agents: m}
}

// Build returns the argv for the named agent. env is empty in M1.
func (l *AgentLauncher) Build(spec SpawnSpec) ([]string, []string, error) {
	cfg, ok := l.Agents[spec.Agent]
	if !ok {
		return nil, nil, fmt.Errorf("launcher: unknown agent %q", spec.Agent)
	}
	argv := append([]string{cfg.Bin}, cfg.Args...)
	return argv, nil, nil
}
