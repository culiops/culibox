//go:build darwin

package config

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"time"
)

// DetectShell returns a usable interactive shell. This is the single
// shell-detection implementation — the launcher's base=login wrapper uses it too.
func DetectShell() string {
	return detectShellDarwin(os.Getenv("SHELL"), dsclUserShell)
}

// dsclUserShell asks Directory Services for the login shell; any failure is "".
func dsclUserShell() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
	if err != nil {
		return ""
	}
	return parseDsclUserShell(string(out))
}
