package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/config"
)

// sessionMeta is the metadata abduco cannot recover on its own. The agent name
// is derivable from the id (<agent>-<n>), but cwd and the original created time
// are not — so they live in the sidecar.
type sessionMeta struct {
	Agent   string    `json:"agent"`
	Name    string    `json:"name,omitempty"` // mutable display label
	Cwd     string    `json:"cwd,omitempty"`
	Created time.Time `json:"created"`
	// Runtime is the tier the session was spawned under (box|host). It must
	// survive a restart: without it, reconcile cannot tell which reattached
	// sessions still own a box, and the orphan reaper would stop boxes out from
	// under live sessions. An empty value means a pre-containment entry, which
	// is host by definition.
	Runtime string `json:"runtime,omitempty"`
	// Box is the container name for a boxed session. Stored rather than
	// recomputed: the workspace directory can be moved or deleted while the
	// agent is still running, and a name that cannot be recomputed means a box
	// that leaks or gets reaped out from under a live session.
	Box string `json:"box,omitempty"`
	// Replyable is the operator's opt-in to accepting messages from the peek
	// panel. It is agent-box's own permission, not a flag passed to the agent,
	// and it defaults to false — including for a record written before this
	// field existed. A session must never become repliable by surviving a
	// restart.
	Replyable bool `json:"replyable,omitempty"`
	// Submitted records that the session has had at least one Enter-submitted
	// input — the gate that lets a silent agent read as "needs you" rather than
	// "idle". A pointer so a record written before this field existed decodes
	// to nil ("unknown"), which reconcile restores as TRUE for agents: showing a
	// stale amber once beats hiding an agent that is waiting on you.
	Submitted *bool `json:"submitted,omitempty"`
	// Options are the per-spawn option overrides the launcher accepted
	// (bools and enum choices, never secrets): what Restart re-applies.
	Options map[string]any `json:"options,omitempty"`
	// ClaudeSessionID is Claude Code's own conversation id, as last validated
	// through its live registry (follows /clear). Restart resumes it.
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	// Account is the agent account the session runs under; empty = default.
	// Restart re-spawns under it, so its credentials and transcripts match.
	Account string `json:"account,omitempty"`
	// ConfigDir is that account's config dir, recorded only for a host-tier
	// session under a non-default account (usage and peek read transcripts
	// from it). Stored rather than recomputed so it survives the account
	// being edited or removed while the session still runs.
	ConfigDir string `json:"config_dir,omitempty"`
	// AWS is the binding the session was spawned with, so a profile edit never
	// changes a running session's scope, and AWSNonce is that spawn's token
	// nonce: without it the box's token stops verifying after a restart.
	AWS      *config.AWSBinding `json:"aws,omitempty"`
	AWSNonce string             `json:"aws_nonce,omitempty"`
}

// sidecar is a tiny JSON store (id -> sessionMeta) written atomically. It carries
// only enough to rebuild a Session on reconcile; the abduco socket list is the
// source of truth for which sessions are actually alive.
type sidecar struct {
	path string
	mu   sync.Mutex
	m    map[string]sessionMeta
}

// openSidecar loads the sidecar (a missing/empty path yields an empty store).
func openSidecar(path string) (*sidecar, error) {
	sc := &sidecar{path: path, m: map[string]sessionMeta{}}
	if path == "" {
		return sc, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sc, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return sc, nil
	}
	if err := json.Unmarshal(b, &sc.m); err != nil {
		return nil, fmt.Errorf("sidecar: parse %s: %w", path, err)
	}
	return sc, nil
}

// put records/overwrites an entry (best-effort persist: a write failure only
// costs restart recovery, never the running session).
func (sc *sidecar) put(id string, md sessionMeta) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.m[id] = md
	_ = sc.flushLocked()
}

// remove drops an entry (best-effort persist).
func (sc *sidecar) remove(id string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if _, ok := sc.m[id]; !ok {
		return
	}
	delete(sc.m, id)
	_ = sc.flushLocked()
}

// rename updates an entry's label. No-op if the id is absent, so a concurrent
// retention-prune can never be resurrected into a stale entry (TOCTOU-safe).
func (sc *sidecar) rename(id, name string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok {
		return
	}
	md.Name = name
	sc.m[id] = md
	_ = sc.flushLocked()
}

// setReplyable updates an entry's opt-in. No-op if the id is absent.
func (sc *sidecar) setReplyable(id string, v bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok {
		return
	}
	md.Replyable = v
	sc.m[id] = md
	_ = sc.flushLocked()
}

// setSubmitted records the session's first submitted input. No-op if the id is
// absent, so a concurrent retention-prune can never be resurrected.
func (sc *sidecar) setSubmitted(id string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok {
		return
	}
	md.Submitted = boolPtr(true)
	sc.m[id] = md
	_ = sc.flushLocked()
}

// setClaudeSessionID records the conversation id. No-op if the id is absent,
// so a concurrent retention-prune can never be resurrected.
func (sc *sidecar) setClaudeSessionID(id, sid string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok || md.ClaudeSessionID == sid {
		return
	}
	md.ClaudeSessionID = sid
	sc.m[id] = md
	_ = sc.flushLocked()
}

func boolPtr(b bool) *bool { return &b }

// all returns a copy of the current map.
func (sc *sidecar) all() map[string]sessionMeta {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make(map[string]sessionMeta, len(sc.m))
	for k, v := range sc.m {
		out[k] = v
	}
	return out
}

func (sc *sidecar) flushLocked() error {
	if sc.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(sc.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := sc.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, sc.path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(sc.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
