package config

import (
	"os"
	"path/filepath"
	"strings"
)

// DetectShell returns a usable interactive shell: $SHELL when it is an existing
// executable file that isn't a nologin stub, else /bin/bash (so a service account
// with SHELL=/usr/sbin/nologin still gets a working shell). This is the single
// shell-detection implementation — the launcher's base=login wrapper uses it too.
func DetectShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" || strings.Contains(filepath.Base(sh), "nologin") || !isExecutableFile(sh) {
		return "/bin/bash"
	}
	return sh
}

func isExecutableFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// WithDetectedShell returns the agent list to launch with, plus the name of the
// profile the UI's quick-shell button should spawn ("" = offer no quick shell).
//
// The detected shell is a VIRTUAL seed: recomputed every boot, never written to
// the profile store, and never shadowing a user profile — if any configured agent
// already runs this shell binary, that profile is used as-is.
func WithDetectedShell(agents []AgentConfig) ([]AgentConfig, string) {
	sh := DetectShell()
	base := filepath.Base(sh)

	names := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Bin == sh || filepath.Base(a.Bin) == base {
			return agents, a.Name // the user's own profile wins
		}
		names[a.Name] = true
	}
	for _, cand := range []string{base, "shell", base + "-shell"} {
		if names[cand] {
			continue
		}
		out := make([]AgentConfig, len(agents), len(agents)+1)
		copy(out, agents)
		return append(out, AgentConfig{Name: cand, Bin: sh, Base: BaseInherit}), cand
	}
	return agents, "" // every candidate name is taken by a different bin
}
