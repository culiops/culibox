package web

import (
	"encoding/json"
	"os"
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
	b, err := os.ReadFile(path)
	if err != nil {
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
