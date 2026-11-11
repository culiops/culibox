package session

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/bundle"
	"github.com/culiops/culibox/internal/config"
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
	// ClaudeSessionIDs is every Claude conversation this session has had since
	// spawn, oldest first (a /clear starts a new one), capped at
	// maxConversationIDs. Per-session cost is the sum over these.
	ClaudeSessionIDs []string `json:"claude_session_ids,omitempty"`
	// CostComplete says ClaudeSessionIDs is the whole history: true only for a
	// claude session created by a spawn that has recorded every conversation
	// since. A record from before this field, a dropped bad entry or a hit cap
	// leaves it false, so a cost summed from the list is shown as partial.
	CostComplete bool `json:"cost_complete,omitempty"`
	// Account is the agent account the session runs under; empty = default.
	// Restart re-spawns under it, so its credentials and transcripts match.
	Account string `json:"account,omitempty"`
	// ConfigDir is that account's config dir, recorded only for a host-tier
	// session under a non-default account (usage and peek read transcripts
	// from it). Stored rather than recomputed so it survives the account
	// being edited or removed while the session still runs.
	ConfigDir string `json:"config_dir,omitempty"`
	// AWS and GitHub are the bindings the session was spawned with, so a profile
	// edit never changes a running session's scope, and BrokerNonce is that
	// spawn's token nonce: without it the box's token stops verifying after a
	// restart. The JSON key stays "aws_nonce": records written before GitHub
	// bindings existed must still restore.
	AWS    *config.AWSBinding    `json:"aws,omitempty"`
	GitHub *config.GitHubBinding `json:"github,omitempty"`
	// AWSConns are the named AWS connections the session was spawned with, as
	// the launcher cloned them (sorted by name; the sidecar keeps them, not the
	// host config, so a removed connection keeps working for a running session).
	// Records from before aws_connections existed lack the key and read as none.
	AWSConns []config.AWSConnection `json:"aws_connections,omitempty"`
	// Tokens and PATs are the static-token entries the session was spawned
	// with, as the launcher cloned them (the value reference, never the value).
	// Records from before N3 lack both and read as none.
	Tokens []config.StaticToken `json:"tokens,omitempty"`
	PATs   []config.StaticToken `json:"pats,omitempty"`
	// EgressAllow and EgressOpen are the profile's network policy as spawned;
	// a record from before them restores with none.
	EgressAllow []string `json:"egress_allow,omitempty"`
	EgressOpen  bool     `json:"egress_open,omitempty"`
	BrokerNonce string   `json:"aws_nonce,omitempty"`
	// EgressNonce is the spawn's egress-token nonce (any boxed session with
	// egress). Records from before A1 lack it; restore backfills BrokerNonce.
	EgressNonce string `json:"egress_nonce,omitempty"`
	// BundleDrops is what the config bundle left out for this boxed session,
	// kept so the UI still says so after a daemon restart. Records from before
	// A3 lack it and read as none.
	BundleDrops []bundle.Drop `json:"bundle_drops,omitempty"`
	// Clone is the clone refresh result of a clone-mode session, kept so the UI
	// still shows it after a daemon restart. Records from before the refresh
	// lack it and read as none.
	Clone *CloneInfo `json:"clone,omitempty"`
	// ContextPaths and CloneOf are the profile's context_paths and clone source
	// as of the spawn, so the UI still shows what the box could read after a
	// restart or a profile edit. Records from before lack both and read as none.
	ContextPaths []string `json:"context_paths,omitempty"`
	CloneOf      string   `json:"clone_of,omitempty"`
	// LockedFiles are the files the box layer locked on the host for this
	// session's box at spawn (macOS, Info.LockedFiles).
	LockedFiles []string `json:"locked_files,omitempty"`
	// Exclusive: the session's box is its own (an elevate binding). Records
	// written before M3b have no key and read as false.
	Exclusive bool `json:"exclusive,omitempty"`
	// BaseAccess is the base-role probe result per AWS binding key ("" = the
	// legacy binding, else the connection name), updated by a re-probe.
	// Records from before the probe lack it: every key reads "not checked".
	BaseAccess map[string]awsprobe.Result `json:"base_access,omitempty"`
	// SSODirect is what the sso_direct gate left per AWS binding key at spawn
	// (hours of validity, account). Absent for assume_role-only sessions.
	SSODirect map[string]ssoSpawnInfo `json:"sso_direct,omitempty"`
	// ParentID is the session this one is a companion shell of; empty for a
	// top-level session. Written once at spawn, restored by Reconcile.
	ParentID string `json:"parent_id,omitempty"`
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

// maxConversationIDs bounds a session's conversation history so a hostile or
// runaway /clear loop cannot grow the sidecar without limit.
const maxConversationIDs = 256

// setClaudeSessionID records the current conversation id together with the
// whole history and its completeness. No-op if the id is absent, so a
// concurrent retention-prune can never be resurrected, and if nothing changed.
//
// The write is monotonic: ids already stored are never dropped (a stale
// snapshot arriving late keeps them, after the snapshot's own, in order), and
// the result is complete only if both the stored record and the snapshot are.
// A union past maxConversationIDs is cut there and marked incomplete.
func (sc *sidecar) setClaudeSessionID(id, sid string, ids []string, complete bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok {
		return
	}
	merged := slices.Clone(ids)
	for _, old := range md.ClaudeSessionIDs {
		if !slices.Contains(merged, old) {
			merged = append(merged, old)
		}
	}
	complete = complete && md.CostComplete
	if len(merged) > maxConversationIDs {
		merged, complete = merged[:maxConversationIDs], false
	}
	if md.ClaudeSessionID == sid && md.CostComplete == complete && slices.Equal(md.ClaudeSessionIDs, merged) {
		return
	}
	md.ClaudeSessionID, md.ClaudeSessionIDs, md.CostComplete = sid, merged, complete
	sc.m[id] = md
	_ = sc.flushLocked()
}

// setBaseAccess records the session's base-role results after a re-probe.
// No-op if the id is absent, so a concurrent retention-prune can never be
// resurrected.
func (sc *sidecar) setBaseAccess(id string, base map[string]awsprobe.Result) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	md, ok := sc.m[id]
	if !ok {
		return
	}
	md.BaseAccess = maps.Clone(base)
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
