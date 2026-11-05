package broker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/culiops/culibox/internal/box"
)

// maxSockPath is the longest absolute unix socket path that can be bound. Linux
// sun_path is 108 bytes including the NUL; Go refuses a 108 byte path
// (syscall_linux.go), so 107 is the most that works.
const maxSockPath = 107

const (
	// sharedRel is the one socket every box used before the per-box sockets;
	// boxesRel holds one directory per box, a SIBLING of sharedRel and never
	// inside it: a box from before the change mounts the whole shared dir and
	// runs as the daemon's uid, so anything under it would be reachable from
	// every such box.
	sharedRel      = "broker/sock"
	sharedSockName = "broker.sock"
	// The per-box layout is <stateRoot>/broker/b/<12 hex>/s: as short as the
	// shared socket's path (also 24 bytes after the state root), so a state root
	// that worked before the per-box sockets still works.
	boxesRel    = "broker/b"
	boxSockName = "s"
)

// longestBoxSock is the per-box socket path of the longest possible name, so a
// state root that cannot hold one is refused at startup.
func longestBoxSock(stateRoot string) string {
	return filepath.Join(stateRoot, filepath.FromSlash(boxesRel), "000000000000", boxSockName)
}

// The presenter of a request is the box it came from, learned from the
// listener it arrived on (identity by construction, no peer inspection).
type presenterKey struct{}

type presenter struct {
	box    string // the per-box listener's box
	shared bool   // the shared socket
}

func withBox(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, presenterKey{}, presenter{box: name})
}

func withShared(ctx context.Context) context.Context {
	return context.WithValue(ctx, presenterKey{}, presenter{shared: true})
}

// presenterOf is who presented the request: a box (its per-box listener), the
// shared socket, or neither (a plain listener: darwin, which identifies the box
// by the request's source address instead).
func presenterOf(ctx context.Context) (box string, shared bool) {
	p, _ := ctx.Value(presenterKey{}).(presenter)
	return p.box, p.shared
}

// boxListener is the unix listener of one box: every connection accepted on it
// comes from a container that mounts that box's socket directory.
type boxListener struct {
	net.Listener
	box string
}

// BoxName is the box this listener serves (cmd/agent-box logs a listener that
// stops by it).
func (l *boxListener) BoxName() string { return l.box }

// sharedListener is the pre-#73 shared socket. Boxes started before the change
// still mount its directory; the broker answers them with a respawn message.
type sharedListener struct{ net.Listener }

// BaseContext is http.Server.BaseContext: it puts the listener's identity into
// every request context served from it.
func BaseContext(l net.Listener) context.Context {
	switch v := l.(type) {
	case *boxListener:
		return withBox(context.Background(), v.box)
	case *sharedListener:
		return withShared(context.Background())
	}
	return context.Background()
}

// BoxSockets keeps one unix socket per bound box that has a session, with a
// reference count. Linux only; nil on darwin.
type BoxSockets struct {
	stateRoot string

	mu     sync.Mutex
	serve  func(net.Listener)
	socks  map[string]*boxSock
	closed bool
}

type boxSock struct {
	l    *boxListener
	dir  string
	refs int
}

func newBoxSockets(stateRoot string) *BoxSockets {
	return &BoxSockets{stateRoot: stateRoot, socks: map[string]*boxSock{}}
}

// Start installs the function that serves a new listener. It must not block;
// it is called once per new listener, under the registry's lock. Acquire fails
// until it has been called.
func (s *BoxSockets) Start(serve func(net.Listener)) {
	s.mu.Lock()
	s.serve = serve
	s.mu.Unlock()
}

// Acquire takes a reference on box's socket and returns the directory mount a
// box gets (read-only, at SockBoxDir). The first reference creates the
// listener. On error no reference is held. The name must be a box name before
// it becomes a path component.
func (s *BoxSockets) Acquire(boxName string) (box.Mount, error) {
	if !box.IsBoxName(boxName) {
		return box.Mount{}, fmt.Errorf("broker: %q is not a box name", boxName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return box.Mount{}, errors.New("broker: per-box sockets are closed")
	}
	if s.serve == nil {
		return box.Mount{}, errors.New("broker: per-box sockets are not started")
	}
	if bs := s.socks[boxName]; bs != nil {
		bs.refs++
		return box.Mount{Host: bs.dir, Box: SockBoxDir}, nil
	}
	l, dir, err := listenSocket(s.stateRoot, boxesRel+"/"+boxName[len(boxName)-12:], boxSockName)
	if err != nil {
		return box.Mount{}, err
	}
	bl := &boxListener{Listener: l, box: boxName}
	s.socks[boxName] = &boxSock{l: bl, dir: dir, refs: 1}
	s.serve(bl)
	return box.Mount{Host: dir, Box: SockBoxDir}, nil
}

// Release drops one reference. At zero the listener closes (Go unlinks the
// socket file) and the DIRECTORY STAYS: a box that is still running mounts it,
// and a recreated directory would not be visible through that bind mount. The
// next Acquire for the same box binds a new socket in the same directory.
// A box with no references is a no-op.
func (s *BoxSockets) Release(boxName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bs := s.socks[boxName]
	if bs == nil {
		return
	}
	if bs.refs--; bs.refs > 0 {
		return
	}
	_ = bs.l.Close()
	delete(s.socks, boxName)
}

// Close closes every listener; later Acquires fail.
func (s *BoxSockets) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for name, bs := range s.socks {
		_ = bs.l.Close()
		delete(s.socks, name)
	}
}

// listenSocket binds a unix socket at <stateRoot>/<rel>/<name>: the
// directories under stateRoot are created 0700 (each verified to be a plain
// directory, so a planted symlink is refused instead of followed), a stale
// socket is replaced, and the socket is 0600. Every write goes through os.Root.
// It returns the directory (what a box mounts).
func listenSocket(stateRoot, rel, name string) (l net.Listener, dir string, err error) {
	dir = filepath.Join(stateRoot, filepath.FromSlash(rel))
	sock := filepath.Join(dir, name)
	// Fail closed before anything is created: sun_path is short and the state
	// root is user-configurable. Nothing is truncated or bound elsewhere.
	if n := len(sock); n > maxSockPath {
		return nil, "", fmt.Errorf("broker: the socket path %s is %d bytes; a unix socket path holds at most %d. Use a shorter data directory", sock, n, maxSockPath)
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, "", fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	cur := ""
	for _, part := range strings.Split(rel, "/") {
		cur = path.Join(cur, part)
		if err := root.Mkdir(cur, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, "", fmt.Errorf("broker: %w", err)
		}
		if fi, err := root.Lstat(cur); err != nil || !fi.IsDir() {
			return nil, "", fmt.Errorf("broker: %s is not a plain directory", cur)
		}
		if cur != "broker" {
			if err := root.Chmod(cur, 0o700); err != nil {
				return nil, "", fmt.Errorf("broker: %w", err)
			}
		}
	}
	sockRel := rel + "/" + name
	// A stale socket from a crash is replaced; the instance lock rules out a live peer.
	if err := root.Remove(sockRel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("broker: removing stale socket: %w", err)
	}
	// bind(2) cannot go through an os.Root, so this is the one path resolved by
	// name. It is only built after every directory was created or verified
	// through the Root above, and each is 0700 and ours.
	l, err = net.Listen("unix", sock)
	if err != nil {
		return nil, "", fmt.Errorf("broker: listening on %s: %w", sock, err)
	}
	if err := root.Chmod(sockRel, 0o600); err != nil {
		l.Close()
		return nil, "", fmt.Errorf("broker: %w", err)
	}
	return l, dir, nil
}
