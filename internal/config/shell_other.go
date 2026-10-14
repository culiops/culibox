//go:build !darwin

package config

import "os"

// DetectShell returns a usable interactive shell. This is the single
// shell-detection implementation — the launcher's base=login wrapper uses it too.
func DetectShell() string {
	return detectShell(os.Getenv("SHELL"), "/etc/passwd", os.Getuid())
}
