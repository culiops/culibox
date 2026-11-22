package session

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/culiops/culibox/internal/config"
)

// handbackScanTimeout bounds one exit scan of an in-place workspace: the walk's
// own 30 s budget plus the hashing of what it flagged.
const handbackScanTimeout = 45 * time.Second

// handbackSkew is how far before a session's recorded start the scan window
// opens, so a file written in the first moments (the clock read and the box
// start are not the same instant) is not missed.
const handbackSkew = 2 * time.Second

// HandbackNote says that a scan of a workspace (or the return of a clone
// branch) flagged files for the user's review, without the files themselves:
// ID is the record to read them from. Provider-neutral; it carries nothing a
// box wrote beyond the branch name, which the clone layer has sanitized.
type HandbackNote struct {
	ID      string `json:"id"`
	Count   int    `json:"count"`
	Branch  string `json:"branch,omitempty"`
	InPlace bool   `json:"in_place,omitempty"`
}

// HandbackHooks is what scanning an in-place workspace needs from the handback
// layer, kept as an interface so this package does not import it. A nil
// HandbackHooks means no scan runs.
type HandbackHooks interface {
	// ScanInPlace inspects workspace (symlinks resolved) for what a box wrote
	// there since `since`, records the result and returns the note. sinceKnown
	// is false when since is a guess (the session's start was lost); the hooks
	// then prefer the last scan they recorded for the workspace.
	ScanInPlace(ctx context.Context, workspace string, since time.Time, sinceKnown bool) (HandbackNote, error)
}

// SetHandback installs the handback hooks. Call once at boot. Nil means no
// scan of an in-place workspace.
func (m *Manager) SetHandback(h HandbackHooks) {
	m.mu.Lock()
	m.handback = h
	m.mu.Unlock()
}

// copy returns an independent HandbackNote, nil for nil.
func (n *HandbackNote) copy() *HandbackNote {
	if n == nil {
		return nil
	}
	out := *n
	return &out
}

// handbackWork scans the workspace of a boxed in-place session once it has
// ended and stores the note on the session. It runs synchronously; callers
// choose the goroutine. It acts on a capability, not on a kind of agent: a
// session that runs in a box over the user's own folder (no clone, not a
// companion shell) wrote into that folder, whatever agent it ran. Failures are
// logged, never fatal: the session's exit has completed either way.
func (m *Manager) handbackWork(sess *Session) {
	m.mu.Lock()
	h := m.handback
	m.mu.Unlock()
	if h == nil || sess.Runtime != config.RuntimeBox || sess.CloneOf != "" || sess.ParentID != "" || sess.Cwd == "" {
		return
	}
	ws, err := filepath.EvalSymlinks(sess.Cwd)
	if err != nil {
		log.Printf("session %s: no handback scan, workspace %q: %v", sess.ID, sess.Cwd, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handbackScanTimeout)
	defer cancel()
	note, err := h.ScanInPlace(ctx, ws, sess.Created.Add(-handbackSkew), !sess.createdGuessed)
	if err != nil {
		log.Printf("session %s: handback scan of %q: %v", sess.ID, ws, err)
		return
	}
	sess.setHandback(&note)
}

func (s *Session) setHandback(n *HandbackNote) {
	s.mu.Lock()
	s.handback = n
	s.mu.Unlock()
}
