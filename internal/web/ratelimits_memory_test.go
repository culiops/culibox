package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/account"
	"github.com/culiops/culibox/internal/session"
)

func rlBody(updated int64, five float64, fiveReset int64, week float64, weekReset int64) []byte {
	return []byte(fmt.Sprintf(`{"updated_at":%d,"five_hour":{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}`,
		updated, five, fiveReset, week, weekReset))
}

func rlSnap(updated int64, five float64, fiveReset int64, week float64, weekReset int64) RateLimits {
	return RateLimits{UpdatedAt: updated, FiveHour: &RateLimitWindow{UsedPercentage: five, ResetsAt: fiveReset}, SevenDay: &RateLimitWindow{UsedPercentage: week, ResetsAt: weekReset}}
}

// --- non-default accounts: every source feeds one memory (review I-2) ----------

// A non-default account's host file and its boxes use one credential, so one
// quota. Each source used to keep its own readings and the freshest source's
// readings were shown, which still flipped between models. They share one memory.
func TestMergeBoxRateLimits_hostAndBoxShareOneMemory(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	acct := "hostbox-" + t.Name()
	s := &Server{}
	merge := func(host RateLimits, box []byte) RateLimits {
		s.BoxFiles = boxFilesOf(session.BoxFile{Box: "b1", Account: acct, Data: box})
		out := map[string]RateLimits{acct: host}
		s.mergeBoxRateLimits(out, now)
		return out[acct]
	}
	// poll 1: the box (94%, window B) is fresher than the host file (76%, window A)
	rl := merge(rlSnap(now.Unix()-30, 9, fiveR, 76, weekA), rlBody(now.Unix(), 0, fiveR, 94, weekB))
	if got := seenWeek(rl); got[weekA] != 76 || got[weekB] != 94 {
		t.Fatalf("poll 1 seen = %v, want both sources' readings", got)
	}
	// poll 2: now the host file is the fresher source and wins; both are still known
	rl = merge(rlSnap(now.Unix()+5, 9, fiveR, 77, weekA), rlBody(now.Unix()-100, 0, fiveR, 94, weekB))
	if rl.SevenDay.UsedPercentage != 77 {
		t.Fatalf("setup: the host snapshot should have won, got %+v", rl.SevenDay)
	}
	if got := seenWeek(rl); got[weekA] != 77 || got[weekB] != 94 {
		t.Fatalf("poll 2 seen = %v, want {A:77 B:94}", got)
	}
}

func TestMergeBoxRateLimits_twoBoxesShareOneMemory(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	acct := "twoboxes-" + t.Name()
	s := &Server{BoxFiles: boxFilesOf(
		session.BoxFile{Box: "b1", Account: acct, Data: rlBody(now.Unix(), 9, fiveR, 76, weekA)},
		session.BoxFile{Box: "b2", Account: acct, Data: rlBody(now.Unix()-60, 0, fiveR, 94, weekB)},
	)}
	out := map[string]RateLimits{}
	s.mergeBoxRateLimits(out, now)
	if got := seenWeek(out[acct]); got[weekA] != 76 || got[weekB] != 94 {
		t.Fatalf("seen = %v, want both boxes' readings", got)
	}
}

// The default account's sources can be different subscriptions (host login vs a
// profile's token), so they are NOT pooled: the chosen box shows only its own.
func TestMergeBoxRateLimits_defaultAccountBoxesStayApart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	s := &Server{BoxFiles: boxFilesOf(
		session.BoxFile{Box: "defbox-a-" + t.Name(), Account: account.Default, Data: rlBody(now.Unix(), 9, fiveR, 76, weekA)},
		session.BoxFile{Box: "defbox-b-" + t.Name(), Account: account.Default, Data: rlBody(now.Unix(), 0, fiveR, 94, weekB)},
	)}
	out := map[string]RateLimits{}
	s.mergeBoxRateLimits(out, now)
	got := seenWeek(out[account.Default])
	if got[weekA] != 76 || len(got) != 1 {
		t.Fatalf("default account seen = %v, want only the chosen box's reading", got)
	}
}

// --- resets_at bounds (review I-2) ---------------------------------------------

// A reading's reset time cannot be further away than its window is long, so a box
// cannot pin a reading for the whole account by claiming a reset a year out.
func TestSanitizeRateLimits_boundsResetsAtByWindowLength(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) int64 { return now.Add(d).Unix() }
	cases := []struct {
		name     string
		five, wk int64
		keepFive bool
		keepWeek bool
	}{
		{"both inside", at(4 * time.Hour), at(6 * 24 * time.Hour), true, true},
		{"five hour window claiming 6h", at(6 * time.Hour), at(6 * 24 * time.Hour), false, true},
		{"five hour window claiming 24h", at(24 * time.Hour), at(6 * 24 * time.Hour), false, true},
		{"week window claiming 8 days", at(4 * time.Hour), at(8 * 24 * time.Hour), true, false},
		{"week window claiming 399 days", at(4 * time.Hour), at(399 * 24 * time.Hour), true, false},
		{"clock drift slack is tolerated", at(5*time.Hour + 4*time.Minute), at(7*24*time.Hour + 4*time.Minute), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rl := rlSnap(now.Unix(), 10, c.five, 20, c.wk)
			if !c.keepFive && !c.keepWeek {
				t.Fatal("bad case")
			}
			got, ok := sanitizeRateLimits(rl, now)
			if !ok {
				t.Fatal("snapshot rejected whole")
			}
			if (got.FiveHour != nil) != c.keepFive || (got.SevenDay != nil) != c.keepWeek {
				t.Fatalf("five=%v week=%v, want keep five=%v week=%v", got.FiveHour, got.SevenDay, c.keepFive, c.keepWeek)
			}
		})
	}
}

func TestMergeBoxRateLimits_farFutureResetIsNotRemembered(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fiveR := now.Add(2 * time.Hour).Unix()
	acct := "pin-" + t.Name()
	s := &Server{BoxFiles: boxFilesOf(session.BoxFile{Box: "evil", Account: acct,
		Data: rlBody(now.Unix(), 9, fiveR, 100, now.Add(399*24*time.Hour).Unix())})}
	out := map[string]RateLimits{}
	s.mergeBoxRateLimits(out, now)
	if got := seenWeek(out[acct]); len(got) != 0 {
		t.Fatalf("a reading claiming a reset 399 days out was remembered: %v", got)
	}
}

// --- forgetting an account's memory (review I-1) -------------------------------

func memoryHasAccount(name string) bool {
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	for k, e := range rlLastGood.m {
		if e.acct == name || k == rlAcctKey(name) {
			return true
		}
	}
	return false
}

func TestAccountTokenChangeForgetsRememberedReadings(t *testing.T) {
	s, _, _ := acctServer(t)
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"work","provider":"claude"}`); rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var file string
	for _, a := range s.Accounts.List() {
		if a.Name == "work" {
			file = filepath.Join(a.ConfigDir, account.RateLimitsName)
		}
	}
	os.MkdirAll(filepath.Dir(file), 0o700)
	now := time.Now()
	weekA, weekB := now.Add(5*24*time.Hour).Unix(), now.Add(3*24*time.Hour).Unix()
	fiveR := now.Add(2 * time.Hour).Unix()
	// the old token's two readings, both remembered
	os.WriteFile(file, rlBody(now.Unix(), 9, fiveR, 76, weekA), 0o600)
	s.rateLimitsByAccount()
	os.WriteFile(file, rlBody(now.Unix(), 0, fiveR, 94, weekB), 0o600)
	if got := seenWeek(s.rateLimitsByAccount()["work"]); got[weekA] != 76 || got[weekB] != 94 {
		t.Fatalf("setup: seen = %v", got)
	}

	if rec := doJSON(t, s.handleAccount, "PUT", "/api/accounts/work", `{"token_secret":"NEW_TOKEN","inherit_personal":false}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	if memoryHasAccount("work") {
		t.Fatal("the account's remembered readings survived a token change")
	}
	// the new token's session writes a low reading: the old 94% must not come back
	os.WriteFile(file, rlBody(now.Unix()+60, 1, fiveR, 3, weekA), 0o600)
	got := seenWeek(s.rateLimitsByAccount()["work"])
	if len(got) != 1 || got[weekA] != 3 {
		t.Fatalf("seen after the token change = %v, want only the new reading", got)
	}
}

func TestAccountDeleteForgetsRememberedReadings(t *testing.T) {
	s, _, _ := acctServer(t)
	if rec := doJSON(t, s.handleAccounts, "POST", "/api/accounts", `{"name":"gone","provider":"claude"}`); rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var file string
	for _, a := range s.Accounts.List() {
		if a.Name == "gone" {
			file = filepath.Join(a.ConfigDir, account.RateLimitsName)
		}
	}
	os.MkdirAll(filepath.Dir(file), 0o700)
	now := time.Now()
	os.WriteFile(file, rlBody(now.Unix(), 9, now.Add(2*time.Hour).Unix(), 76, now.Add(5*24*time.Hour).Unix()), 0o600)
	s.rateLimitsByAccount()
	if !memoryHasAccount("gone") {
		t.Fatal("setup: nothing was remembered")
	}
	if rec := doJSON(t, s.handleAccount, "DELETE", "/api/accounts/gone", ""); rec.Code != 200 {
		t.Fatalf("delete %d %s", rec.Code, rec.Body)
	}
	if memoryHasAccount("gone") {
		t.Fatal("the deleted account's remembered readings survived")
	}
}

// --- memory bound: least recently used box goes first (review item 4) -----------

func TestRateLimitsMemory_evictsLeastRecentlyUsedBoxAndKeepsTheHostFile(t *testing.T) {
	rlLastGood.mu.Lock()
	rlLastGood.m = map[string]*rlEntry{} // other tests' sources must not count against the bound
	rlLastGood.mu.Unlock()
	now := time.Unix(1_800_000_000, 0)
	fiveR := now.Add(2 * time.Hour).Unix()
	host := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(host, rlBody(now.Unix(), 9, fiveR, 76, now.Add(5*24*time.Hour).Unix()), 0o600)
	readRateLimitsAt(host, now)

	snap := rlSnap(now.Unix(), 1, fiveR, 2, now.Add(24*time.Hour).Unix())
	key := func(i int) string { return fmt.Sprintf("box\x00evict-%s-%d\x00default", t.Name(), i) }
	for i := 0; i < rateLimitsCacheMax-1; i++ {
		rememberBoxReadings(key(i), snap, now)
	}
	rememberBoxReadings(key(0), snap, now) // box 0 is used again: no longer the oldest
	for i := rateLimitsCacheMax; i < rateLimitsCacheMax+5; i++ {
		rememberBoxReadings(key(i), snap, now)
	}
	rlLastGood.mu.Lock()
	defer rlLastGood.mu.Unlock()
	if n := len(rlLastGood.m); n > rateLimitsCacheMax {
		t.Fatalf("memory grew to %d sources (max %d)", n, rateLimitsCacheMax)
	}
	if _, have := rlLastGood.m[host]; !have {
		t.Fatal("a flood of boxes evicted the host file's memory")
	}
	if _, have := rlLastGood.m[key(0)]; !have {
		t.Fatal("the recently used box was evicted before older ones")
	}
	if _, have := rlLastGood.m[key(1)]; have {
		t.Fatal("the least recently used box was not the one evicted")
	}
	if !strings.Contains(key(1), "evict-") {
		t.Fatal("test key helper broken")
	}
}
