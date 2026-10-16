package account

import "path/filepath"

// provision makes sure the account's directories exist, all through the
// Store's dataDir root so a planted symlink cannot redirect them. Idempotent:
// an existing dir (and the login inside it) is reused, never clobbered.
// Task 2 extends this with the statusLine script, settings and inherit links.
func (s *Store) provision(r Resolved) error {
	if err := s.root.MkdirAll(accountsDir, 0o700); err != nil {
		return err
	}
	if err := s.root.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	return s.root.MkdirAll(filepath.Join(accountsDir, r.Name, "claude"), 0o700)
}
