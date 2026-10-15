package boxvm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// CheckUserDockerContext fails if the USER's docker CLI (their env, their
// ~/.docker) points at agent-box's VM. colima's defaults would have switched
// it; StartArgs and autoActivate:false prevent that, and this proves it.
func CheckUserDockerContext(ctx context.Context, dockerBin string) error {
	out, err := exec.CommandContext(ctx, dockerBin, "context", "show").Output()
	if err != nil {
		return nil // no context support or no config: nothing was switched
	}
	if strings.TrimSpace(string(out)) == "colima-"+Profile {
		return errors.New("your docker CLI's current context is agent-box's box VM; run `docker context use default` (or your usual context)")
	}
	return nil
}

// MacOSMajor is the running macOS major version, or 0 when it cannot be read.
func MacOSMajor(ctx context.Context) int {
	out, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return 0
	}
	major, _, _ := strings.Cut(strings.TrimSpace(string(out)), ".")
	n, _ := strconv.Atoi(major)
	return n
}

// ColimaVersion parses `colima version` ("colima version 0.8.1 …"), "" when
// unknown. Colima runs with cfg's pinned environment, never the daemon's own:
// even a version query must not touch the user's ~/.colima.
func ColimaVersion(ctx context.Context, cfg Config) string {
	if cfg.Validate() != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, cfg.ColimaBin, "version")
	cmd.Env = cfg.Env(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(firstLine(string(out)))
	if len(f) >= 3 && f[0] == "colima" && f[1] == "version" {
		return strings.TrimPrefix(f[2], "v")
	}
	return ""
}

// VersionAtLeast compares the major.minor of a dotted version.
func VersionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}
