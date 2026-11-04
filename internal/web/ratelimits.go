package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/session"
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
	// Model and ModelFamily name the model whose session wrote this snapshot;
	// ByModel keeps the latest reading per model family. The quota can be
	// reported per model bucket, so a reader that wants a stable number picks one
	// bucket instead of interleaving whichever session rendered last.
	Model       string                  `json:"model,omitempty"`
	ModelFamily string                  `json:"model_family,omitempty"`
	ByModel     map[string]BucketLimits `json:"by_model,omitempty"`
}

// BucketLimits is one model family's reading inside RateLimits.ByModel.
type BucketLimits struct {
	UpdatedAt int64            `json:"updated_at,omitempty"`
	FiveHour  *RateLimitWindow `json:"five_hour,omitempty"`
	SevenDay  *RateLimitWindow `json:"seven_day,omitempty"`
}

const (
	rateLimitsCacheMax   = 64 // paths remembered by the last-good cache
	rateLimitsMaxBuckets = 8  // by_model entries kept per snapshot
)

// rlLastGood remembers the last good snapshot read from each path.
var rlLastGood = struct {
	mu sync.Mutex
	m  map[string]RateLimits
}{m: map[string]RateLimits{}}

// hasWindows reports whether a snapshot carries any reading at all.
func (rl RateLimits) hasWindows() bool {
	if rl.FiveHour != nil || rl.SevenDay != nil {
		return true
	}
	for _, b := range rl.ByModel {
		if b.FiveHour != nil || b.SevenDay != nil {
			return true
		}
	}
	return false
}

// boundBuckets keeps the rateLimitsMaxBuckets most recently updated buckets (the
// lexically smaller name wins a tie, so the choice does not depend on map order).
func boundBuckets(m map[string]BucketLimits) map[string]BucketLimits {
	if len(m) <= rateLimitsMaxBuckets {
		return m
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if m[names[i]].UpdatedAt != m[names[j]].UpdatedAt {
			return m[names[i]].UpdatedAt > m[names[j]].UpdatedAt
		}
		return names[i] < names[j]
	})
	out := make(map[string]BucketLimits, rateLimitsMaxBuckets)
	for _, k := range names[:rateLimitsMaxBuckets] {
		out[k] = m[k]
	}
	return out
}

// readRateLimits parses the rate-limits JSON file. ok=false when the file is
// absent, or unreadable/malformed/empty with no earlier good reading, or carries
// no window — the caller then simply shows nothing rather than a broken widget.
//
// A writer that is not atomic (abtop, an old agent-box script, the user's own
// statusLine) leaves the file empty or half-written for a moment, and the old
// agent-box script wrote window-less snapshots. Any of those would make an
// account's meters vanish until the next poll, so the last good snapshot read
// from each path is served instead. A deleted file is the exception: it is gone,
// not torn, so its cached reading is dropped. The cache holds at most
// rateLimitsCacheMax paths (the account count plus the default file).
func readRateLimits(path string) (RateLimits, bool) {
	if path == "" {
		return RateLimits{}, false
	}
	// The file lives in a directory a host-tier agent can write to: read it with
	// the no-follow, FIFO-safe, size-capped reader so a planted FIFO or symlink
	// cannot hang /api/meta.
	var rl RateLimits
	b, ok := session.ReadRegistryFile(path)
	if ok && json.Unmarshal(b, &rl) != nil {
		ok = false
	}
	if ok {
		rl.ByModel = sanitizeBuckets(rl.ByModel, time.Now())
	}
	if ok && rl.hasWindows() {
		rlLastGood.mu.Lock()
		if _, have := rlLastGood.m[path]; !have && len(rlLastGood.m) >= rateLimitsCacheMax {
			rlLastGood.m = map[string]RateLimits{}
		}
		rlLastGood.m[path] = rl
		rlLastGood.mu.Unlock()
		return rl, true
	}
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		delete(rlLastGood.m, path)
		return RateLimits{}, false
	}
	last, have := rlLastGood.m[path]
	return last, have
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
	// rateLimitsRecent is how fresh a box's default-account snapshot must be to
	// compete on box name (see mergeBoxRateLimits).
	rateLimitsRecent = 15 * time.Minute
)

// mergeBoxRateLimits folds the quota snapshots boxed sessions wrote into out.
// With no box files out is untouched.
//
// The merge differs per account, because of which credential each source uses:
//   - account.Default: the host default file is written by ANY host claude on the
//     machine (including the user's own interactive one), while boxes
//     authenticate with the profile's CLAUDE_CODE_OAUTH_TOKEN, so the two can be
//     different subscriptions. Freshest-wins would flip the default quota
//     between identities on every poll. So the host snapshot wins whenever it
//     is present (the pre-box behaviour); box snapshots fill default only when
//     there is no host default snapshot at all. Several boxes can then carry
//     different tokens too (the box key does not include the profile token), so
//     freshest-wins would flip between them as well: the valid snapshot of the
//     lexically smallest box name among the snapshots updated within
//     rateLimitsRecent is used, which does not depend on file order or on which
//     box wrote last, and an idle or dead box's stale snapshot cannot hold the
//     slot. If none is that recent, the freshest wins. A tie keeps the first.
//   - every other account: the per-account host file and the box snapshots all
//     use that account's own credential, so they share an identity and the
//     freshest UpdatedAt wins, compared on min(UpdatedAt, now) so a box stamping
//     inside the future slack cannot always beat a fresh snapshot. A tie keeps
//     the existing source.
func (s *Server) mergeBoxRateLimits(out map[string]RateLimits, now time.Time) {
	if s.BoxFiles == nil {
		return
	}
	nowUnix := now.Unix()
	_, hostDefault := out[account.Default]
	var defCands []session.BoxFile // box files behind defRLs, same index
	var defRLs []RateLimits        // valid default snapshots
	for _, f := range s.BoxFiles(boxRateLimitsRel, boxRateLimitsMax) {
		if f.Account == account.Default && hostDefault {
			continue
		}
		var rl RateLimits
		if err := json.Unmarshal(f.Data, &rl); err != nil {
			continue
		}
		rl, ok := sanitizeRateLimits(rl, now)
		if !ok {
			continue
		}
		if f.Account == account.Default { // no host default snapshot here
			defCands, defRLs = append(defCands, f), append(defRLs, rl)
			continue
		}
		if cur, have := out[f.Account]; !have || min(rl.UpdatedAt, nowUnix) > min(cur.UpdatedAt, nowUnix) {
			out[f.Account] = rl
		}
	}
	if best := pickDefaultBox(defCands, defRLs, now); best >= 0 {
		out[account.Default] = defRLs[best]
	}
}

// pickDefaultBox chooses which box's snapshot fills the default account when
// the host has none: the smallest box name among the snapshots updated within
// rateLimitsRecent of now, else the freshest (compared on min(UpdatedAt, now)).
// The first of equals wins. -1 when there are no candidates.
func pickDefaultBox(files []session.BoxFile, rls []RateLimits, now time.Time) int {
	nowUnix := now.Unix()
	cutoff := now.Add(-rateLimitsRecent).Unix()
	best, bestRecent := -1, false
	for i := range files {
		recent := rls[i].UpdatedAt >= cutoff
		switch {
		case best < 0:
		case recent && !bestRecent:
		case recent && bestRecent && files[i].Box < files[best].Box:
		case !recent && !bestRecent && min(rls[i].UpdatedAt, nowUnix) > min(rls[best].UpdatedAt, nowUnix):
		default:
			continue
		}
		best, bestRecent = i, recent
	}
	return best
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
	rl.FiveHour, rl.SevenDay = cleanWindow(rl.FiveHour, now), cleanWindow(rl.SevenDay, now)
	// The model strings only ever select a bucket, so cap their length.
	rl.Model, rl.ModelFamily = clip(rl.Model, 80), clip(rl.ModelFamily, 40)
	rl.ByModel = sanitizeBuckets(rl.ByModel, now)
	if !rl.hasWindows() {
		return RateLimits{}, false
	}
	return rl, true
}

// cleanWindow clamps used_percentage to [0,100] and drops (nil) a window whose
// resets_at is not a plausible unix time (>= 1e9 and <= now+400d).
func cleanWindow(w *RateLimitWindow, now time.Time) *RateLimitWindow {
	if w == nil || w.ResetsAt < rateLimitsMinReset || w.ResetsAt > now.Add(rateLimitsMaxReset).Unix() {
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

// sanitizeBuckets applies the snapshot checks to each per-model bucket: one
// stamped more than rateLimitsFutureSlack ahead of now, or with no plausible
// window left, is dropped; the rest are clamped, name-capped and bounded. nil
// when none survive. Used for box files and host files alike, since either is
// written by a process the daemon does not control.
func sanitizeBuckets(in map[string]BucketLimits, now time.Time) map[string]BucketLimits {
	futureCut := now.Add(rateLimitsFutureSlack).Unix()
	out := map[string]BucketLimits{}
	for name, b := range in {
		if b.UpdatedAt > futureCut {
			continue
		}
		b.UpdatedAt = max(b.UpdatedAt, 0)
		b.FiveHour, b.SevenDay = cleanWindow(b.FiveHour, now), cleanWindow(b.SevenDay, now)
		if b.FiveHour != nil || b.SevenDay != nil {
			out[clip(name, 40)] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	return boundBuckets(out)
}

// clip truncates s to at most n bytes.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
