package session

import (
	"errors"
	"log"
	"slices"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

// boundOwnLoginKinds is the host_own_login kinds a profile binds, in the fixed
// order github, aws. AWS is bound by the legacy inline binding or any named
// connection; GitHub by its binding or any PAT (a machine login for GitHub
// drops both).
func boundOwnLoginKinds(aws *config.AWSBinding, conns []config.AWSConnection, gh *config.GitHubBinding, pats []config.StaticToken) []string {
	var out []string
	if gh != nil || len(pats) > 0 {
		out = append(out, launcher.OwnLoginGitHub)
	}
	if aws != nil || len(conns) > 0 {
		out = append(out, launcher.OwnLoginAWS)
	}
	return out
}

// ownLoginStillBound is the recorded host_own_login kinds the profile still
// binds, in order. A Restart keeps the choice only for those: a kind the
// profile lost has nothing left to override, and refusing the restart (or
// flipping the kind back to the broker) would both be wrong. A kind the
// profile gained is not in kinds, so it goes through the broker. It resolves
// through the same launcher interfaces as Spawn; on a resolver error it
// returns kinds unchanged so Spawn reports that error.
func (m *Manager) ownLoginStillBound(agent string, kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	var aws *config.AWSBinding
	var gh *config.GitHubBinding
	var conns []config.AWSConnection
	var pats []config.StaticToken
	if r, ok := m.launcher.(AWSResolver); ok {
		aws = r.AWSFor(agent)
	}
	if r, ok := m.launcher.(GitHubResolver); ok {
		gh = r.GitHubFor(agent)
	}
	if r, ok := m.launcher.(AWSConnResolver); ok {
		var err error
		if conns, err = r.AWSConnectionsFor(agent); err != nil {
			return kinds
		}
	}
	if r, ok := m.launcher.(TokenResolver); ok {
		var err error
		if _, pats, err = r.TokensFor(agent); err != nil {
			return kinds
		}
	}
	bound := boundOwnLoginKinds(aws, conns, gh, pats)
	var out []string
	for _, k := range kinds {
		if slices.Contains(bound, k) {
			out = append(out, k)
		}
	}
	return out
}

// HostOwnLoginRecord is what a Host spawn that takes GitHub and/or AWS from
// this machine's own login asks to have audited: who, for which profile, in
// which directory, which kinds. Kinds are the effective ones.
type HostOwnLoginRecord struct {
	SessionID, Profile, Cwd, Actor string
	Kinds                          []string
}

// SpawnAudit writes the audit row of a host_own_login spawn. It is called
// before the process starts; an error refuses the spawn.
type SpawnAudit interface {
	RecordHostOwnLogin(HostOwnLoginRecord) error
}

// ErrSpawnAudit refuses a Host spawn that uses this machine's login when its
// audit row cannot be written (or no auditor is wired): nothing is started.
var ErrSpawnAudit = errors.New("CuliBox could not audit this Host session's use of this machine's login, so it was not started; retry shortly")

// SetSpawnAudit installs the auditor of host_own_login spawns. Without one a
// spawn that needs it is refused (fail closed).
func (m *Manager) SetSpawnAudit(a SpawnAudit) {
	m.mu.Lock()
	m.spawnAudit = a
	m.mu.Unlock()
}

// auditHostOwnLogin writes the row for a Host spawn with a non-empty choice.
func (m *Manager) auditHostOwnLogin(r HostOwnLoginRecord) error {
	m.mu.Lock()
	a := m.spawnAudit
	m.mu.Unlock()
	if a == nil {
		return ErrSpawnAudit
	}
	if err := a.RecordHostOwnLogin(r); err != nil {
		log.Printf("session %s: host_own_login audit: %v", r.SessionID, err)
		return ErrSpawnAudit
	}
	return nil
}
