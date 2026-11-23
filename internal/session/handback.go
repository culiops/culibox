package session

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/culiops/culibox/internal/box"
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
	// Truncated is set when the scan was cut short, could not run or stopped
	// with an internal error, so a Count of 0 is not read as "nothing flagged".
	Truncated bool `json:"truncated,omitempty"`
}

// HandbackHooks is what scanning an in-place workspace needs from the handback
// layer, kept as an interface so this package does not import it. A nil
// HandbackHooks means no scan runs.
type HandbackHooks interface {
	// ScanInPlace inspects workspace (the real path the box was made over) for
	// what a box wrote there since `since`, records the result and returns the
	// note (Count is what this scan flagged). sinceKnown is false when since is
	// a guess (the session's start was lost); the hooks then prefer the last
	// scan they recorded for the workspace. Either way a recorded scan earlier
	// than since wins, so no gap between scans goes unchecked.
	ScanInPlace(ctx context.Context, workspace string, since time.Time, sinceKnown bool) (HandbackNote, error)
	// NotScanned records that the workspace of a session that ended was not
	// scanned, because the path the box was made over could not be confirmed at
	// spawn. cwd is the session's working directory as given (unresolved);
	// since is the window the scan would have covered.
	NotScanned(cwd string, since time.Time) (HandbackNote, error)
	// BeginExit says an exit scan of workspace is coming. It is called before
	// the session's box is released, so a box stop that follows at once knows
	// the record is on its way; end is called once the scan has finished.
	BeginExit(workspace string) (end func())
}

// SetHandback installs the handback hooks. Call once at boot. Nil means no
// scan of an in-place workspace.
func (m *Manager) SetHandback(h HandbackHooks) {
	m.mu.Lock()
	m.handback = h
	m.mu.Unlock()
}

// handbackBegin tells the hooks that sess's exit scan (or not-scanned record)
// is coming, before the box is released; the returned func ends it. A session
// handbackWork would skip gets a no-op.
func (m *Manager) handbackBegin(sess *Session) func() {
	m.mu.Lock()
	h := m.handback
	m.mu.Unlock()
	if !handbackEligible(h, sess) {
		return func() {}
	}
	ws := sess.workspace
	if ws == "" {
		ws = sess.Cwd
	}
	return h.BeginExit(ws)
}

// handbackEligible: the session ran in a box over the user's own folder (no
// clone, not a companion shell) and there are hooks to scan with.
func handbackEligible(h HandbackHooks, sess *Session) bool {
	return h != nil && sess.Runtime == config.RuntimeBox && sess.CloneOf == "" && sess.ParentID == ""
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
// companion shell) wrote into that folder, whatever agent it ran. It scans the
// workspace path the box was made over, never a fresh resolution of Cwd.
// A session whose workspace could not be vouched for at spawn is not walked;
// a record says so instead. Failures and panics are logged, never fatal: the
// session's exit has completed either way, and hostile input must not take
// the daemon down.
func (m *Manager) handbackWork(sess *Session) {
	m.mu.Lock()
	h := m.handback
	m.mu.Unlock()
	if !handbackEligible(h, sess) {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("session %s: handback scan of %q stopped with an internal error: %v", sess.ID, sess.workspace, r)
		}
	}()
	since := sess.Created.Add(-handbackSkew)
	if sess.workspace == "" {
		// spawnWorkspace could not vouch for the path: nothing is walked, but
		// the user is told, not only the log.
		if sess.Cwd == "" {
			log.Printf("session %s: no workspace on record; its files are not scanned", sess.ID)
			return
		}
		note, err := h.NotScanned(sess.Cwd, since)
		if err != nil {
			log.Printf("session %s: recording that %q was not scanned: %v", sess.ID, sess.Cwd, err)
			return
		}
		sess.setHandback(&note)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handbackScanTimeout)
	defer cancel()
	note, err := h.ScanInPlace(ctx, sess.workspace, since, !sess.createdGuessed)
	if err != nil {
		log.Printf("session %s: handback scan of %q: %v", sess.ID, sess.workspace, err)
		return
	}
	sess.setHandback(&note)
}

// setHandbackEnd keeps the end of the announced exit scan (handbackBegin).
func (s *Session) setHandbackEnd(end func()) {
	s.mu.Lock()
	s.handbackEnd = end
	s.mu.Unlock()
}

// takeHandbackEnd returns the end of the announced exit scan and forgets it,
// so it is called at most once; a no-op when none was announced.
func (s *Session) takeHandbackEnd() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.handbackEnd
	s.handbackEnd = nil
	if end == nil {
		return func() {}
	}
	return end
}

func (s *Session) setHandback(n *HandbackNote) {
	s.mu.Lock()
	s.handback = n
	s.mu.Unlock()
}

// spawnWorkspace is the real path of a workspace just wrapped in a box, "" when
// it cannot be vouched for. The box layer resolved cwd itself and named the box
// after the result, so the path counts only if naming the box after it gives
// the box Wrap returned (box.Manager.NamedFor): a symlink that moved between
// the box layer's resolution and this one makes the names differ, and then
// nothing is scanned for the session rather than something else.
func spawnWorkspace(boxes Boxes, cwd string, key box.BoxKey, boxName string) string {
	if cwd == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		log.Printf("handback: workspace %q cannot be resolved (%v); its files are not scanned at exit", cwd, err)
		return ""
	}
	// Optional: a Boxes without the check (a test fake) is taken at its word.
	if v, ok := boxes.(interface {
		NamedFor(real string, key box.BoxKey, name string) bool
	}); ok && !v.NamedFor(real, key, boxName) {
		log.Printf("handback: workspace %q changed while its box %s was made; its files are not scanned at exit", cwd, boxName)
		return ""
	}
	return real
}
