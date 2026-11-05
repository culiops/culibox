package web

import (
	"encoding/json"
	"math"
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
	// ByModel is the latest reading per model family, written by agent-box's own
	// statusLine script (which also records model and model_family for diagnosis;
	// nothing here reads those). The quota may be per model bucket, so the client
	// treats every bucket as one more reading and shows the highest live one.
	ByModel map[string]BucketLimits `json:"by_model,omitempty"`
	// Seen is filled by the daemon, never read from a file: the readings it has
	// seen for each window that has not reset yet, one per resets_at (the highest
	// percentage), so the client can show the highest of them even when the file
	// only ever holds the last writer's numbers.
	Seen *SeenLimits `json:"seen,omitempty"`
}

// SeenReading is one remembered reading of a window.
type SeenReading struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
	UpdatedAt      int64   `json:"updated_at,omitempty"`
}

// SeenLimits holds the remembered readings of each window.
type SeenLimits struct {
	FiveHour []SeenReading `json:"five_hour,omitempty"`
	SevenDay []SeenReading `json:"seven_day,omitempty"`
}

// BucketLimits is one model family's reading inside RateLimits.ByModel.
type BucketLimits struct {
	UpdatedAt int64            `json:"updated_at,omitempty"`
	FiveHour  *RateLimitWindow `json:"five_hour,omitempty"`
	SevenDay  *RateLimitWindow `json:"seven_day,omitempty"`
}

const (
	rateLimitsCacheMax   = 64 // sources remembered (see rlLastGood)
	rateLimitsMaxBuckets = 8  // by_model entries kept per snapshot
	rateLimitsSeenMax    = 4  // remembered readings per window (one per resets_at)
)

// rlEntry is what is remembered about one snapshot source: a file path (its last
// good snapshot, and for the default account's file the readings seen), a box's
// snapshot (default account only), or a non-default account's pooled readings.
type rlEntry struct {
	last    RateLimits
	hasLast bool
	seen    [2]map[int64]SeenReading // five_hour, seven_day: by resets_at
	acct    string                   // the account this source belongs to ("" = none)
	box     bool                     // a box's snapshot: evicted before the rest
	used    uint64                   // rlLastGood.tick when last used (LRU)
}

// windowSpan is how far from now each window's reset can plausibly be: five_hour,
// seven_day. The windows are 5h and 7d long; five_hour gets an hour more in case
// Claude rounds the window start up, so a legitimate reading is not dropped.
var windowSpan = [2]time.Duration{5*time.Hour + time.Hour, 7 * 24 * time.Hour}

// rateLimitsClock is the clock of the quota code; tests replace it.
var rateLimitsClock = time.Now

// rlAcctKey is the memory key of a non-default account's pooled readings.
func rlAcctKey(name string) string { return "acct\x00" + name }

// rlLastGood is the daemon's memory of the quota sources, in process only (a
// restart clears it). Keys: a file path (the default account's file, and the file
// of each non-default account); "acct\x00<name>" for a non-default account's
// readings pooled over all its sources, so such an account uses two entries (its
// file and its pool); "box\x00<box>\x00default" for a default-account box. At
// most rateLimitsCacheMax entries, each holding at most 2 windows x
// rateLimitsSeenMax readings.
//
// Known limit: the default account's remembered readings cannot be tied to a
// credential. A host /login to another subscription is invisible to the daemon,
// so the old subscription's readings stay until their window resets (at most 7
// days) or the daemon restarts. A non-default account's are forgotten when it is
// edited or deleted (forgetAccountQuota).
var rlLastGood = struct {
	mu   sync.Mutex
	m    map[string]*rlEntry
	tick uint64
	// cut is, per non-default account, the unix time its token last changed: a
	// reading stamped at or before it belongs to the old token and is ignored
	// wherever it turns up (the old file still on disk, buckets copied from it, a
	// retained box's file, a poll that was already in flight). Entries older than
	// the longest window can no longer matter and are pruned.
	cut map[string]int64
}{m: map[string]*rlEntry{}, cut: map[string]int64{}}

// dropBefore removes from rl every reading stamped at or before cut (the top
// level by its updated_at, each bucket by its own); ok=false when none is left.
// The caller holds rlLastGood.mu when cut comes from rlLastGood.cut.
func dropBefore(cut int64, rl RateLimits) (RateLimits, bool) {
	if cut <= 0 {
		return rl, true
	}
	if rl.UpdatedAt <= cut {
		rl.FiveHour, rl.SevenDay = nil, nil
	}
	var kept map[string]BucketLimits
	for k, b := range rl.ByModel {
		if b.UpdatedAt > cut {
			if kept == nil {
				kept = map[string]BucketLimits{}
			}
			kept[k] = b
		}
	}
	rl.ByModel = kept
	return rl, rl.hasWindows()
}

// rlEntryFor returns the entry for key, creating it and marking it used. The
// caller holds rlLastGood.mu. When the cache is full it evicts the least recently
// used box entry (dead boxes go first, and a flood of boxes cannot push out the
// host file); with no box entry, the least recently used of the rest. The evicted
// source simply starts remembering again on its next read.
func rlEntryFor(key string, box bool) *rlEntry {
	rlLastGood.tick++
	if e, ok := rlLastGood.m[key]; ok {
		e.used = rlLastGood.tick
		return e
	}
	if len(rlLastGood.m) >= rateLimitsCacheMax {
		victim, anyBox := "", false
		for k, e := range rlLastGood.m {
			switch {
			case victim == "":
			case e.box && !anyBox:
			case e.box == anyBox && e.used < rlLastGood.m[victim].used:
			default:
				continue
			}
			victim, anyBox = k, e.box
		}
		delete(rlLastGood.m, victim)
	}
	e := &rlEntry{box: box, used: rlLastGood.tick}
	rlLastGood.m[key] = e
	return e
}

// forgetAccountQuota drops everything remembered for a non-default account: its
// pooled readings and the last good snapshot of its file. Called when the
// account's token changes or the account is deleted.
func forgetAccountQuota(name string, now time.Time) {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	for k, e := range rlLastGood.m {
		if e.acct == name || k == rlAcctKey(name) {
			delete(rlLastGood.m, k)
		}
	}
	rlLastGood.cut[name] = now.Unix()
	for n, c := range rlLastGood.cut {
		if c < now.Add(-windowSpan[1]-rateLimitsFutureSlack).Unix() {
			delete(rlLastGood.cut, n)
		}
	}
}

// accountTokenChanged is called when a non-default account's token really
// changed (or the account was deleted): the new credential has its own quota, so
// the old token's readings must not linger for up to 7 days. It forgets what the
// daemon remembered and removes the account's snapshot file, best effort: the
// file still holds the old token's numbers (and other-model buckets the script
// would copy into the next write) until a session on the new token replies, and
// a daemon restart would otherwise bring them back.
func (s *Server) accountTokenChanged(name, configDir string) {
	forgetAccountQuota(name, rateLimitsClock())
	if configDir != "" {
		os.Remove(filepath.Join(configDir, account.RateLimitsName)) // ENOENT and the rest ignored
	}
}

// storeValueChanged is called after a stored value is set, replaced or removed:
// when its id is some account's token the account's token changed (the "Replace
// token" flow only ever PUTs the value, never the account).
func (s *Server) storeValueChanged(id string) {
	if s.Accounts == nil {
		return
	}
	for _, a := range s.Accounts.List() {
		if !a.IsDefault && a.TokenSecret == id {
			s.accountTokenChanged(a.Name, a.ConfigDir)
		}
	}
}

// remember records every live reading of rl: its top-level windows and each
// bucket's. Per window and resets_at only the highest percentage is kept (within
// one window usage only rises, so the highest is never an overstatement). Readings
// whose window has reset, or whose reset time is further away than the window is
// long, are dropped, and at most rateLimitsSeenMax remain per window: the oldest
// resets_at are evicted first.
func (e *rlEntry) remember(rl RateLimits, now time.Time) {
	cut := rlLastGood.cut[e.acct] // 0 for the default account and for no change
	add := func(i int, w *RateLimitWindow, updated int64) {
		w = cleanWindow(w, now, windowSpan[i])
		if w == nil || (cut > 0 && updated <= cut) {
			return
		}
		if e.seen[i] == nil {
			e.seen[i] = map[int64]SeenReading{}
		}
		cur, have := e.seen[i][w.ResetsAt]
		if !have || w.UsedPercentage > cur.UsedPercentage || (w.UsedPercentage == cur.UsedPercentage && updated > cur.UpdatedAt) {
			e.seen[i][w.ResetsAt] = SeenReading{UsedPercentage: w.UsedPercentage, ResetsAt: w.ResetsAt, UpdatedAt: updated}
		}
	}
	add(0, rl.FiveHour, rl.UpdatedAt)
	add(1, rl.SevenDay, rl.UpdatedAt)
	for _, b := range rl.ByModel {
		add(0, b.FiveHour, b.UpdatedAt)
		add(1, b.SevenDay, b.UpdatedAt)
	}
	for i := range e.seen {
		e.prune(i, now)
	}
}

// prune drops the readings of window i that have reset, then the oldest resets_at
// beyond rateLimitsSeenMax.
func (e *rlEntry) prune(i int, now time.Time) {
	for r := range e.seen[i] {
		if r <= now.Unix() {
			delete(e.seen[i], r)
		}
	}
	for len(e.seen[i]) > rateLimitsSeenMax {
		oldest := int64(math.MaxInt64)
		for r := range e.seen[i] {
			oldest = min(oldest, r)
		}
		delete(e.seen[i], oldest)
	}
}

// seenLimits returns the readings still live at now, oldest resets_at first; nil
// when there are none.
func (e *rlEntry) seenLimits(now time.Time) *SeenLimits {
	var out SeenLimits
	for i, dst := range []*[]SeenReading{&out.FiveHour, &out.SevenDay} {
		e.prune(i, now)
		for _, r := range e.seen[i] {
			*dst = append(*dst, r)
		}
		sort.Slice(*dst, func(a, b int) bool { return (*dst)[a].ResetsAt < (*dst)[b].ResetsAt })
	}
	if len(out.FiveHour) == 0 && len(out.SevenDay) == 0 {
		return nil
	}
	return &out
}

// withSeen is rl carrying the entry's remembered readings.
func (e *rlEntry) withSeen(rl RateLimits, now time.Time) RateLimits {
	rl.Seen = e.seenLimits(now)
	return rl
}

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
// not torn, so its cached reading is dropped. The memory is bounded by
// rateLimitsCacheMax entries in all (see rlLastGood).
func readRateLimits(path string) (RateLimits, bool) { return readRateLimitsAt(path, rateLimitsClock()) }

// readRateLimitsAt is readRateLimits with the clock passed in. The file's own
// entry remembers the readings seen (the default account's host file).
func readRateLimitsAt(path string, now time.Time) (RateLimits, bool) {
	return readSnapshot(path, "", true, now)
}

// readSnapshot is the reader behind readRateLimits. acct names the non-default
// account the file belongs to ("" for the default account's file); remember says
// whether the file's own entry keeps the readings seen. A non-default account's
// readings are pooled over all its sources instead (mergeBoxRateLimits), so its
// file is read with remember=false.
func readSnapshot(path, acct string, remember bool, now time.Time) (RateLimits, bool) {
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
		rl.Seen = nil // daemon-filled only: never trust one from the file
		rl.ByModel = sanitizeBuckets(rl.ByModel, now)
	}
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	if ok && acct != "" {
		rl, ok = dropBefore(rlLastGood.cut[acct], rl)
	}
	if ok && rl.hasWindows() {
		e := rlEntryFor(path, false)
		e.acct = acct
		e.last, e.hasLast = rl, true
		if !remember {
			return rl, true
		}
		e.remember(rl, now)
		return e.withSeen(rl, now), true
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		delete(rlLastGood.m, path)
		return RateLimits{}, false
	}
	if e, have := rlLastGood.m[path]; have && e.hasLast {
		e.used = rlLastGood.tick
		if !remember {
			return e.last, true
		}
		return e.withSeen(e.last, now), true
	}
	return RateLimits{}, false
}

// rateLimitsByAccount returns each account's 5h/weekly windows. default reads
// RateLimitsFile (abtop or the opt-in script); every other account reads the
// file its own provisioned statusLine writes. Accounts with no data are absent.
// The account list is read on every call, so an account created while the daemon
// runs shows its quota without a restart.
func (s *Server) rateLimitsByAccount() map[string]RateLimits {
	now := rateLimitsClock()
	out := map[string]RateLimits{}
	if rl, ok := readRateLimitsAt(s.RateLimitsFile, now); ok {
		out[account.Default] = rl
	}
	if s.Accounts != nil {
		for _, a := range s.Accounts.List() {
			if a.IsDefault {
				continue
			}
			if rl, ok := readSnapshot(filepath.Join(a.ConfigDir, account.RateLimitsName), a.Name, false, now); ok {
				out[a.Name] = rl
			}
		}
	}
	s.mergeBoxRateLimits(out, now)
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
	// rateLimitsMinReset bounds a plausible resets_at from below (unix seconds).
	// From above, a window cannot reset later than its own length from now
	// (windowSpan, plus rateLimitsFutureSlack): a box cannot pin a reading for the
	// whole account by claiming a reset months away.
	rateLimitsMinReset = 1e9
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
	// Non-default accounts: every source of one account (its host file and each of
	// its boxes) shares one memory, attached to whichever snapshot wins below.
	pooled := map[string]bool{}
	for name, rl := range out {
		if name != account.Default {
			rememberAccountReadings(name, rl, now)
			pooled[name] = true
		}
	}
	s.mergeBoxFiles(out, now, pooled)
	for name := range pooled {
		if rl, ok := out[name]; ok {
			rl.Seen = accountSeen(name, now)
			out[name] = rl
		}
	}
}

// mergeBoxFiles is the box part of mergeBoxRateLimits; pooled collects the
// non-default accounts whose readings were pooled.
func (s *Server) mergeBoxFiles(out map[string]RateLimits, now time.Time, pooled map[string]bool) {
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
		if f.Account != account.Default {
			// a retained box still holds the old token's snapshot after a change
			if rl, ok = dropBefore(accountCutoff(f.Account), rl); !ok {
				continue
			}
		}
		if f.Account == account.Default { // no host default snapshot here
			rl = rememberBoxReadings("box\x00"+f.Box+"\x00"+f.Account, rl, now)
			defCands, defRLs = append(defCands, f), append(defRLs, rl)
			continue
		}
		rememberAccountReadings(f.Account, rl, now)
		pooled[f.Account] = true
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
	rl.Seen = nil // daemon-filled only (rememberBoxReadings)
	rl.FiveHour, rl.SevenDay = cleanWindow(rl.FiveHour, now, windowSpan[0]), cleanWindow(rl.SevenDay, now, windowSpan[1])
	rl.ByModel = sanitizeBuckets(rl.ByModel, now)
	if !rl.hasWindows() {
		return RateLimits{}, false
	}
	return rl, true
}

// cleanWindow clamps used_percentage to [0,100] and drops (nil) a window whose
// resets_at is not plausible: before 1e9, or further from now than the window is
// long (span) plus rateLimitsFutureSlack.
func cleanWindow(w *RateLimitWindow, now time.Time, span time.Duration) *RateLimitWindow {
	if w == nil || w.ResetsAt < rateLimitsMinReset || w.ResetsAt > now.Add(span+rateLimitsFutureSlack).Unix() {
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
		b.FiveHour, b.SevenDay = cleanWindow(b.FiveHour, now, windowSpan[0]), cleanWindow(b.SevenDay, now, windowSpan[1])
		if b.FiveHour != nil || b.SevenDay != nil {
			out[clip(name, 40)] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	return boundBuckets(out)
}

// rememberBoxReadings records a sanitized default-account box snapshot's
// readings under key (kept apart per box: the default account's sources can be
// different subscriptions) and returns it carrying what is remembered for it.
func rememberBoxReadings(key string, rl RateLimits, now time.Time) RateLimits {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	e := rlEntryFor(key, true)
	e.acct = account.Default
	e.remember(rl, now)
	return e.withSeen(rl, now)
}

// accountCutoff is the unix time of the account's last token change (0: none).
func accountCutoff(name string) int64 {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	return rlLastGood.cut[name]
}

// rememberAccountReadings records a snapshot of a non-default account (from its
// host file or a box) into the account's one pooled memory.
func rememberAccountReadings(name string, rl RateLimits, now time.Time) {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	e := rlEntryFor(rlAcctKey(name), false)
	e.acct = name
	e.remember(rl, now)
}

// accountSeen is the readings remembered for a non-default account.
func accountSeen(name string, now time.Time) *SeenLimits {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	return rlEntryFor(rlAcctKey(name), false).seenLimits(now)
}

// clip truncates s to at most n bytes.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
