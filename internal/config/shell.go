package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// detectShell resolves the shell in order: $SHELL, then the login shell from
// /etc/passwd, then /bin/bash.
//
// The passwd step is not redundant: $SHELL is set by login shells, NOT by
// non-login contexts — a systemd --user service (how this console runs in
// production), cron, or a bare exec all start without it. Trusting $SHELL alone
// would silently hand those a /bin/bash "detected" shell.
func detectShell(envShell, passwdPath string, uid int) string {
	sh, _ := detectShellSrc(envShell, passwdPath, uid)
	return sh
}

// detectShellSrc is detectShell plus where the answer came from ("$SHELL",
// "passwd" or "fallback"), so a diagnostic can report it.
func detectShellSrc(envShell, passwdPath string, uid int) (shell, source string) {
	if usableShell(envShell) {
		return envShell, "$SHELL"
	}
	if sh := shellFromPasswd(passwdPath, uid); usableShell(sh) {
		return sh, "passwd"
	}
	return "/bin/bash", "fallback"
}

// detectShellDarwin resolves the shell on macOS: $SHELL, then the account's
// UserShell from Directory Services (macOS does not list real users in
// /etc/passwd), then /bin/zsh, the default since macOS 10.15.
func detectShellDarwin(envShell string, fromDS func() string) string {
	sh, _ := detectShellDarwinSrc(envShell, fromDS)
	return sh
}

// detectShellDarwinSrc is detectShellDarwin plus where the answer came from
// ("$SHELL", "dscl" or "fallback"), so a diagnostic can report it.
func detectShellDarwinSrc(envShell string, fromDS func() string) (shell, source string) {
	if usableShell(envShell) {
		return envShell, "$SHELL"
	}
	if sh := fromDS(); usableShell(sh) {
		return sh, "dscl"
	}
	return "/bin/zsh", "fallback"
}

// parseDsclUserShell extracts the value from `dscl . -read /Users/<u> UserShell`.
func parseDsclUserShell(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "UserShell:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
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
