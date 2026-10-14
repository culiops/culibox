package abduco

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
)

// SocketPathLimit is the longest socket path the kernel accepts: sun_path is
// 104 bytes on darwin and 108 on Linux, one of which is the terminating NUL.
// Over the limit, abduco's bind() fails and the spawn surfaces only as
// "session not alive" — hence the explicit checks below.
func SocketPathLimit() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}

// SocketPath is where abduco binds a session socket when ABDUCO_SOCKET_DIR is
// dir: a non-personal dir gets "abduco/<user>/", and the name gets "@<host>".
func SocketPath(dir, user, id, host string) string {
	return filepath.Join(dir, "abduco", user, id+"@"+host)
}

// CheckSocketPath refuses an id whose socket path would exceed the limit. It
// counts bytes, as the kernel does.
func CheckSocketPath(dir, user, id, host string) error {
	p := SocketPath(dir, user, id, host)
	if n, lim := len(p), SocketPathLimit(); n > lim {
		return fmt.Errorf("abduco socket path for session %q is %d bytes, over the %d-byte limit on %s; "+
			"use a shorter profile name or set abduco_socket_dir to a shorter directory", id, n, lim, runtime.GOOS)
	}
	return nil
}

// RepresentativeID is the longest session id the configured agents produce in
// practice (ids are "<agent>-<n>"), for the startup check. Profiles added later
// are covered by the spawn-time check in Create.
func RepresentativeID(names []string) string {
	longest := "agent"
	if len(names) > 0 {
		longest = ""
	}
	for _, n := range names {
		if len(n) > len(longest) {
			longest = n
		}
	}
	return longest + "-9999"
}

// CheckProfileSocketPaths is the startup socket-length check, shared by serve
// and doctor: the longest profile name (in bytes) with a four-digit session
// counter must fit under the limit. names should include every profile serve
// starts with, the detected quick-shell profile among them.
func CheckProfileSocketPaths(dir, user, host string, names []string) error {
	return CheckSocketPath(dir, user, RepresentativeID(names), host)
}

// DefaultSocketDir is the socket dir when abduco_socket_dir is unset.
//
// Linux keeps <dataDir>/abduco: changing it would orphan the sessions PROD is
// running. darwin cannot afford that path: with a corporate home, a long login
// name and a hostname like "Firstname-Lastnames-MacBook-Pro.local", the 103
// usable bytes leave room for a session id of about seven characters. /tmp is
// short; the data-dir hash keeps two instances from adopting each other's
// sessions; PrepareSocketDir defends the world-writable parent.
func DefaultSocketDir(goos, dataDir string, uid int) string {
	if goos != "darwin" {
		return filepath.Join(dataDir, "abduco")
	}
	sum := sha256.Sum256([]byte(dataDir))
	return fmt.Sprintf("/tmp/agent-box-%d-%x", uid, sum[:4])
}

// SocketDirFor is the configured dir when set, else DefaultSocketDir.
func SocketDirFor(configured, goos, dataDir string, uid int) string {
	if configured != "" {
		return configured
	}
	return DefaultSocketDir(goos, dataDir, uid)
}

// CheckSocketDir refuses a socket dir that is not a real directory owned by uid
// with mode exactly 0700. In /tmp another local user could have created it first
// (and would then own every session socket); it never creates anything.
func CheckSocketDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("socket dir %s is not a plain directory (symlink or file); remove it and restart", dir)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("socket dir %s has mode %o, want 700; remove it and restart", dir, perm)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != uid {
		return fmt.Errorf("socket dir %s is not owned by uid %d; remove it and restart", dir, uid)
	}
	return nil
}

// PrepareSocketDir creates dir (0700) when absent, then applies CheckSocketDir.
func PrepareSocketDir(dir string, uid int) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return CheckSocketDir(dir, uid)
}

// CurrentUser is the login name abduco puts in the socket path (it uses
// getpwuid; $USER is the fallback when the lookup is unavailable).
func CurrentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
