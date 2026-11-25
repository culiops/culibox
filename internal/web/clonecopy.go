package web

import "path/filepath"

// copyNameOK is whether s can be the one path element of a suggested copy: a
// clean name of [A-Za-z0-9._-], not starting with a dot (no "." or "..", no
// hidden folder).
func copyNameOK(s string) bool {
	if s == "" || s[0] == '.' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// suggestCopyDir is where a git repo's private copy goes by default (#371):
// <home>/box-clones/<profile>, or <home>/box-clones/<repo's folder name> when
// the profile name is not usable as a folder name, else "copy". The user can
// change it; the save-time checks still judge whatever is chosen.
func suggestCopyDir(home, profile, repo string) string {
	name := "copy"
	switch base := filepath.Base(repo); {
	case copyNameOK(profile):
		name = profile
	case repo != "" && copyNameOK(base):
		name = base
	}
	return filepath.Join(home, "box-clones", name)
}
