package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DetectShell returns a usable interactive shell. This is the single
// shell-detection implementation — the launcher's base=login wrapper uses it too.
func DetectShell() string {
	return detectShell(os.Getenv("SHELL"), "/etc/passwd", os.Getuid())
}

// detectShell resolves the shell in order: $SHELL, then the login shell from
// /etc/passwd, then /bin/bash.
//
// The passwd step is not redundant: $SHELL is set by login shells, NOT by
// non-login contexts — a systemd --user service (how this console runs in
// production), cron, or a bare exec all start without it. Trusting $SHELL alone
// would silently hand those a /bin/bash "detected" shell.
func detectShell(envShell, passwdPath string, uid int) string {
	if usableShell(envShell) {
		return envShell
	}
	if sh := shellFromPasswd(passwdPath, uid); usableShell(sh) {
		return sh
	}
	return "/bin/bash"
}

// usableShell rejects empty or relative paths, nologin/false stubs, and anything
// that isn't an executable file. The path must be absolute: it becomes a profile's
// Bin, which is resolved later against a composed PATH, not the daemon's cwd.
func usableShell(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	switch base := filepath.Base(p); {
	case strings.Contains(base, "nologin"), base == "false":
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// shellFromPasswd returns field 7 of the passwd line for uid, or "".
func shellFromPasswd(path string, uid int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	want := strconv.Itoa(uid)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) >= 7 && fields[2] == want {
			return fields[6]
		}
	}
	return ""
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
