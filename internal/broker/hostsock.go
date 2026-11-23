package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"sync"

	"github.com/culiops/culibox/internal/netlimit"
)

// hostsRel holds one directory per bound Host session, a SIBLING of the box
// directories (broker/b) and of the shared socket's. The layout is
// <stateRoot>/broker/h/<12 hex>/s, as long as a per-box socket's path.
const (
	hostsRel     = "broker/h"
	hostSockName = "s"
)

// maxSockPathFor is the longest absolute unix socket path that can be bound on
// goos. Linux sun_path holds 108 bytes including the NUL and Go refuses 108, so
// 107. darwin sun_path holds 104 bytes including the NUL, so 103.
func maxSockPathFor(goos string) int {
	if goos == "darwin" {
		return 103
	}
	return maxSockPath
}

// HostDirName is the directory name of a Host session's socket directory: the
// first 12 hex characters of sha256(session id). A hash, so any session id is a
// safe path component and the path length does not depend on the id.
func HostDirName(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:12]
}

// hostListener is the unix listener of one Host session: every connection
// accepted on it is the session that owns it (identity by construction, as with
// a boxListener).
type hostListener struct {
	net.Listener
	sid string
}

// HostSockets keeps one unix socket per bound Host session, with a reference
// count. It exists on both OSes: a Host session runs on the host itself, so the
// socket does not have to cross the colima VM.
type HostSockets struct {
	stateRoot string
	max       int

	mu     sync.Mutex
	serve  func(net.Listener)
	socks  map[string]*hostSock
	closed bool
	logf   func(format string, args ...any)
}

type hostSock struct {
	l    *hostListener
	dir  string
	refs int
}

func newHostSockets(stateRoot, goos string) *HostSockets {
	return &HostSockets{stateRoot: stateRoot, max: maxSockPathFor(goos), socks: map[string]*hostSock{}, logf: log.Printf}
}

// Start installs the function that serves a new listener. It must not block; it
// is called once per new listener, under the registry's lock. Acquire fails
// until it has been called.
func (s *HostSockets) Start(serve func(net.Listener)) {
	s.mu.Lock()
	s.serve = serve
	s.mu.Unlock()
}

// Acquire takes a reference on the session's socket and returns the directory
// that holds it (<state>/broker/h/<HostDirName(id)>). The first reference binds
// the listener. On error no reference is held. A path over the OS's limit is
// refused with a message naming it; nothing is truncated.
func (s *HostSockets) Acquire(sessionID string) (dir string, err error) {
	if sessionID == "" {
		return "", errors.New("broker: a host socket needs a session id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("broker: host sockets are closed")
	}
	if s.serve == nil {
		return "", errors.New("broker: host sockets are not started")
	}
	if hs := s.socks[sessionID]; hs != nil {
		hs.refs++
		return hs.dir, nil
	}
	l, dir, err := listenSocket(s.stateRoot, hostsRel+"/"+HostDirName(sessionID), hostSockName, s.max)
	if err != nil {
		return "", err
	}
	logf := s.logf
	hl := &hostListener{Listener: netlimit.ListenerWithNotice(l, maxSockConns, func() {
		logf("broker: host session %s is at its limit of %d open connections on its broker socket; further connections wait until one closes", sessionID, maxSockConns)
	}), sid: sessionID}
	s.socks[sessionID] = &hostSock{l: hl, dir: dir, refs: 1}
	s.serve(hl)
	return dir, nil
}

// Release drops one reference. At zero the listener closes (Go unlinks the
// socket file); the directory stays, for the session layer to remove with the
// rest of the session's files. A session with no references is a no-op.
func (s *HostSockets) Release(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hs := s.socks[sessionID]
	if hs == nil {
		return
	}
	if hs.refs--; hs.refs > 0 {
		return
	}
	_ = hs.l.Close()
	delete(s.socks, sessionID)
}

// Close closes every listener; later Acquires fail.
func (s *HostSockets) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id, hs := range s.socks {
		_ = hs.l.Close()
		delete(s.socks, id)
	}
}

// withHost marks ctx as presented from Host session sid's own socket.
func withHost(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, presenterKey{}, presenter{host: sid})
}
