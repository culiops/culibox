package broker

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"github.com/culiops/agent-box/internal/box"
)

// SockBoxDir is where the socket directory is mounted (read-only) inside a box.
const SockBoxDir = "/opt/agentbox-sock"

// Endpoint is how boxes reach the broker on this OS.
type Endpoint struct {
	Listener net.Listener
	BoxURL   string      // AGENTBOX_BROKER as seen from inside a box
	Mounts   []box.Mount // extra read-only mounts a bound box needs
}

// Listen opens the broker's listener. Linux: a unix socket in a 0700 dir that
// boxes get read-only (the DIR is mounted, so a socket recreated after a restart
// stays visible). darwin: unix sockets do not cross the colima VM (measured in
// M2), so TCP on the Mac's loopback, reached from boxes via host.lima.internal
// through the provision script's single RETURN exception. The token guards it.
func Listen(goos, stateRoot string, port int) (Endpoint, error) {
	if goos == "darwin" {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return Endpoint{}, fmt.Errorf("broker: listening on 127.0.0.1:%d: %w", port, err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		return Endpoint{Listener: l, BoxURL: fmt.Sprintf("http://host.lima.internal:%d", p)}, nil
	}
	return listenUnix(stateRoot)
}

func listenUnix(stateRoot string) (Endpoint, error) {
	const (
		rel     = "broker/sock"
		sockRel = rel + "/broker.sock"
	)
	// Every write into the data dir goes through os.Root, so a symlink planted
	// at "broker" or "broker/sock" is refused instead of followed.
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return Endpoint{}, fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	for _, d := range []string{"broker", rel} {
		if err := root.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return Endpoint{}, fmt.Errorf("broker: %w", err)
		}
		if fi, err := root.Lstat(d); err != nil || !fi.IsDir() {
			return Endpoint{}, fmt.Errorf("broker: %s is not a plain directory", d)
		}
	}
	if err := root.Chmod(rel, 0o700); err != nil {
		return Endpoint{}, fmt.Errorf("broker: %w", err)
	}
	// A stale socket from a crash is replaced; the instance lock rules out a live peer.
	if err := root.Remove(sockRel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Endpoint{}, fmt.Errorf("broker: removing stale socket: %w", err)
	}
	// bind(2) cannot go through an os.Root, so this is the one path resolved by
	// name. It is only built after both directories were created or verified
	// through the Root above, and the parent is 0700 and ours.
	dir := filepath.Join(stateRoot, filepath.FromSlash(rel))
	sock := filepath.Join(dir, "broker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return Endpoint{}, fmt.Errorf("broker: listening on %s: %w", sock, err)
	}
	if err := root.Chmod(sockRel, 0o600); err != nil {
		l.Close()
		return Endpoint{}, fmt.Errorf("broker: %w", err)
	}
	return Endpoint{Listener: l, BoxURL: "unix://" + SockBoxDir + "/broker.sock",
		Mounts: []box.Mount{{Host: dir, Box: SockBoxDir}}}, nil
}
