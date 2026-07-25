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
