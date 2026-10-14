//go:build !darwin

package config

import "os"

// DetectShell returns a usable interactive shell. This is the single
// shell-detection implementation — the launcher's base=login wrapper uses it too.
func DetectShell() string {
	return detectShell(os.Getenv("SHELL"), "/etc/passwd", os.Getuid())
}

// DetectShellSource is DetectShell plus where the answer came from: "$SHELL",
// "passwd" or "fallback". For diagnostics (agent-box doctor).
func DetectShellSource() (shell, source string) {
	return detectShellSrc(os.Getenv("SHELL"), "/etc/passwd", os.Getuid())
}
