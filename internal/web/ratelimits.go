package web

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/session"
)

// RateLimitWindow is one rolling usage window (Claude's 5-hour or 7-day).
// UsedPercentage is a float: the producer (abtop) emits fractional values like
// 7.000000000000001, which an int field would refuse to unmarshal — dropping the
// whole snapshot. The client rounds it for display.
type RateLimitWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"` // unix seconds
}

// RateLimits is the account-global usage snapshot produced by a statusLine hook
// (e.g. abtop). Both windows are pointers so an absent one is omitted, not zero.
type RateLimits struct {
	UpdatedAt int64            `json:"updated_at,omitempty"`
	FiveHour  *RateLimitWindow `json:"five_hour,omitempty"`
	SevenDay  *RateLimitWindow `json:"seven_day,omitempty"`
}

// readRateLimits parses the rate-limits JSON file. ok=false when the file is
// absent, unreadable, or malformed, or carries neither window — the caller then
// simply shows nothing rather than a broken widget.
func readRateLimits(path string) (RateLimits, bool) {
	if path == "" {
		return RateLimits{}, false
	}
	// The file lives in a directory a host-tier agent can write to: read it with
	// the no-follow, FIFO-safe, size-capped reader so a planted FIFO or symlink
	// cannot hang /api/meta.
	b, ok := session.ReadRegistryFile(path)
	if !ok {
		return RateLimits{}, false
	}
	var rl RateLimits
	if err := json.Unmarshal(b, &rl); err != nil {
		return RateLimits{}, false
	}
	if rl.FiveHour == nil && rl.SevenDay == nil {
		return RateLimits{}, false
	}
	return rl, true
}

// rateLimitsByAccount returns each account's 5h/weekly windows. default reads
// RateLimitsFile (abtop or the opt-in script); every other account reads the
// file its own provisioned statusLine writes. Accounts with no data are absent.
// The account list is read on every call, so an account created while the daemon
// runs shows its quota without a restart.
func (s *Server) rateLimitsByAccount() map[string]RateLimits {
	out := map[string]RateLimits{}
	if rl, ok := readRateLimits(s.RateLimitsFile); ok {
		out[account.Default] = rl
	}
	if s.Accounts != nil {
		for _, a := range s.Accounts.List() {
			if a.IsDefault {
				continue
			}
			if rl, ok := readRateLimits(filepath.Join(a.ConfigDir, account.RateLimitsName)); ok {
				out[a.Name] = rl
			}
		}
	}
	s.mergeBoxRateLimits(out, time.Now())
	return out
}

const (
	// boxRateLimitsRel is where a boxed session's statusLine writes its quota,
	// relative to the box home. boxRateLimitsMax caps what is read of it.
	boxRateLimitsRel = ".claude/agent-box-rate-limits.json"
	boxRateLimitsMax = 4096

	// rateLimitsFutureSlack tolerates VM clock drift: a snapshot stamped further
	// ahead of now than this is dropped, so a box cannot pin its value forever.
	rateLimitsFutureSlack = 5 * time.Minute
	// rateLimitsMaxReset bounds a plausible resets_at.
	rateLimitsMaxReset = 400 * 24 * time.Hour
	rateLimitsMinReset = 1e9 // unix seconds
)

// mergeBoxRateLimits folds the quota snapshots boxed sessions wrote into out:
// per account, the freshest UpdatedAt across the existing entry and the
// sanitized box snapshots wins (a tie keeps the existing one). With no box files
// out is untouched.
func (s *Server) mergeBoxRateLimits(out map[string]RateLimits, now time.Time) {
	if s.BoxFiles == nil {
		return
	}
	nowUnix := now.Unix()
	for _, f := range s.BoxFiles(boxRateLimitsRel, boxRateLimitsMax) {
		var rl RateLimits
		if err := json.Unmarshal(f.Data, &rl); err != nil {
			continue
		}
		rl, ok := sanitizeRateLimits(rl, now)
		if !ok {
			continue
		}
		// Compare on min(UpdatedAt, now): a box stamping inside the future slack
		// must not always beat a genuinely fresh snapshot. A tie keeps the
		// existing source.
		if cur, have := out[f.Account]; !have || min(rl.UpdatedAt, nowUnix) > min(cur.UpdatedAt, nowUnix) {
			out[f.Account] = rl
		}
	}
}

// sanitizeRateLimits validates a snapshot a boxed session wrote (untrusted).
// ok=false when updated_at is more than rateLimitsFutureSlack ahead of now, or
// when no window survives. used_percentage is clamped to [0,100]; a window whose
// resets_at is not a plausible unix time (>= 1e9 and <= now+400d) is dropped.
func sanitizeRateLimits(rl RateLimits, now time.Time) (RateLimits, bool) {
	if rl.UpdatedAt > now.Add(rateLimitsFutureSlack).Unix() {
		return RateLimits{}, false
	}
	if rl.UpdatedAt < 0 {
		rl.UpdatedAt = 0
	}
	maxReset := now.Add(rateLimitsMaxReset).Unix()
	clean := func(w *RateLimitWindow) *RateLimitWindow {
		if w == nil || w.ResetsAt < rateLimitsMinReset || w.ResetsAt > maxReset {
			return nil
		}
		c := *w
		// !(x > 0) catches NaN, negatives and -0 (normalized to +0).
		if !(c.UsedPercentage > 0) {
			c.UsedPercentage = 0
		} else if c.UsedPercentage > 100 {
			c.UsedPercentage = 100
		}
		return &c
	}
	rl.FiveHour, rl.SevenDay = clean(rl.FiveHour), clean(rl.SevenDay)
	if rl.FiveHour == nil && rl.SevenDay == nil {
		return RateLimits{}, false
	}
	return rl, true
}
