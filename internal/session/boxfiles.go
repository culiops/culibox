package session

import (
	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/boxread"
)

// BoxFile is one file a boxed session wrote, as read from its box home.
// Box is the box's name (host-chosen), a stable key to order boxes by. Account is the account the box's session runs under (account.Default for a
// default session). Data is untrusted: the caller must validate it.
type BoxFile struct {
	Box     string
	Account string
	Data    []byte
}

// BoxFiles reads rel (relative to the box home) from every box known to the
// manager, once per distinct (box, account) pair. Live and retained (exited)
// sessions both count. A miss, a symlink, a non-regular file or an oversize file
// is skipped silently (boxread refuses them). The pairs are snapshotted under
// m.mu and all file I/O happens after unlocking.
func (m *Manager) BoxFiles(rel string, max int64) []BoxFile {
	type pair struct{ box, acct string }
	m.mu.Lock()
	stateDir := m.stateDir
	seen := map[pair]bool{}
	var pairs []pair
	for _, s := range m.sessions {
		if s.Box == "" {
			continue
		}
		a := s.Account
		if a == "" {
			a = account.Default
		}
		p := pair{s.Box, a}
		if !seen[p] {
			seen[p] = true
			pairs = append(pairs, p)
		}
	}
	m.mu.Unlock()
	if stateDir == "" {
		return nil
	}
	var out []BoxFile
	for _, p := range pairs {
		b, _, ok := boxread.ReadFile(boxHome(stateDir, p.box), rel, max)
		if !ok {
			continue
		}
		out = append(out, BoxFile{Box: p.box, Account: p.acct, Data: b})
	}
	return out
}
