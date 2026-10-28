package analytics

import (
	"math"
	"testing"
)

const M = 1_000_000

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// bundleCost prices a pre-summed token bundle as one message, treating
// cacheCr as 5m writes — the shape the per-family rate tests are written in.
func bundleCost(model string, input, output, cacheRead, cacheCr int64) float64 {
	return MessageCostUSD(model, MsgUsage{Input: input, Output: output, CacheRead: cacheRead, CacheWrite5m: cacheCr})
}

func TestBundleCostPerFamily(t *testing.T) {
	cases := []struct {
		name                        string
		model                       string
		in, out, cacheRead, cacheCr int64
		want                        float64
	}{
		// Base input/output rates (per 1M tokens).
		{"opus input", "claude-opus-5", M, 0, 0, 0, 5},
		{"opus output", "claude-opus-4-8", 0, M, 0, 0, 25},
		// Opus 5.5 is cheaper than the rest of the Opus line and must not fall
		// through to the generic "opus" rate.
		{"opus-5-5 input", "claude-opus-5-5", M, 0, 0, 0, 4},
		{"opus-5-5 output", "claude-opus-5-5", 0, M, 0, 0, 20},
		{"sonnet input", "claude-sonnet-4-6", M, 0, 0, 0, 3},
		{"sonnet-5 input", "claude-sonnet-5", M, 0, 0, 0, 2},
		{"sonnet output", "claude-sonnet-4-6", 0, M, 0, 0, 15},
		{"sonnet-5 output", "claude-sonnet-5", 0, M, 0, 0, 10},
		{"haiku input", "claude-haiku-4-5-20251001", M, 0, 0, 0, 1},
		{"haiku output", "claude-haiku-4-5", 0, M, 0, 0, 5},
		{"fable input", "claude-fable-5", M, 0, 0, 0, 10},
		{"fable output", "claude-fable-5", 0, M, 0, 0, 50},
		// Fable 5.1 keeps Fable 5's base rates; only its cache read differs.
		{"fable-5-1 input", "claude-fable-5-1", M, 0, 0, 0, 10},
		{"fable-5-1 output", "claude-fable-5-1", 0, M, 0, 0, 50},
		// Cache derived from input: read = 0.1x, write(creation) = 1.25x.
		{"opus cache read", "claude-opus-5", 0, 0, M, 0, 0.5},
		{"opus cache write", "claude-opus-5", 0, 0, 0, M, 6.25},
		{"sonnet cache read", "claude-sonnet-4-6", 0, 0, M, 0, 0.3},
		{"sonnet-5 cache read", "claude-sonnet-5", 0, 0, M, 0, 0.2},
		// Opus 5.5 overrides the shared 0.1x read multiplier: $0.20 on a $4
		// input rate is 0.05x, so the generic derivation would double it.
		{"opus-5-5 cache read", "claude-opus-5-5", 0, 0, M, 0, 0.2},
		{"opus-5-5 cache write", "claude-opus-5-5", 0, 0, 0, M, 5},
		{"opus-5-5 combined", "claude-opus-5-5", M, M, M, M, 4 + 20 + 0.2 + 5},
		// Fable 5.1 publishes $0.25 on a $10 input rate — 0.025x, so the shared
		// 0.1x derivation would price a cache read 4x too high. Fable 5 itself
		// has no published exception and keeps the derived $1.00.
		{"fable-5-1 cache read", "claude-fable-5-1", 0, 0, M, 0, 0.25},
		{"fable-5 cache read", "claude-fable-5", 0, 0, M, 0, 1},
		{"fable-5-1 cache write", "claude-fable-5-1", 0, 0, 0, M, 12.5},
		// Combined, opus, one of each token type.
		{"opus combined", "claude-opus-5", M, M, M, M, 5 + 25 + 0.5 + 6.25},
		// Unknown / synthetic model families price to zero (row still shown w/ tokens).
		{"unknown", "gpt-4o", M, M, M, M, 0},
		{"empty model", "", M, M, M, M, 0},
		{"synthetic", "<synthetic>", M, M, M, M, 0},
	}
	for _, c := range cases {
		got := bundleCost(c.model, c.in, c.out, c.cacheRead, c.cacheCr)
		if !approx(got, c.want) {
			t.Errorf("%s: bundleCost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBundleCostScalesLinearly(t *testing.T) {
	// Half a million input tokens on opus = half of $5.
	if got := bundleCost("claude-opus-5", M/2, 0, 0, 0); !approx(got, 2.5) {
		t.Errorf("half input = %v, want 2.5", got)
	}
	// Real-ish magnitude: 24.2B cache-read on opus should be a sane number, not overflow.
	if got := bundleCost("claude-opus-5", 0, 0, 24_200_000_000, 0); !approx(got, 24_200_000_000.0/M*0.5) {
		t.Errorf("big cache-read = %v", got)
	}
}

// The family table is matched most-specific-first: "claude-opus-5-5" contains
// the substring "opus" and "claude-fable-5-1" contains "fable", so a
// generic-first order would silently price either as its parent family. This
// test pins the ordering guarantee, not just the rates.
func TestBundleCostPrefersMostSpecificFamily(t *testing.T) {
	// Each pair is a specific id and the generic id it would fall through to,
	// with the token bundle that must tell them apart.
	pairs := []struct {
		specific, generic           string
		in, out, cacheRead, cacheCr int64
	}{
		{"claude-opus-5-5", "claude-opus-5", M, 0, 0, 0},
		{"claude-opus-5-5", "claude-opus-5", 0, 0, M, 0},
		{"claude-fable-5-1", "claude-fable-5", 0, 0, M, 0},
	}
	for _, p := range pairs {
		sp := bundleCost(p.specific, p.in, p.out, p.cacheRead, p.cacheCr)
		gn := bundleCost(p.generic, p.in, p.out, p.cacheRead, p.cacheCr)
		if approx(sp, gn) {
			t.Errorf("%s priced as %s (%v); most-specific match lost", p.specific, p.generic, sp)
		}
	}
	// Every other member of the Opus line keeps the generic rate.
	for _, m := range []string{"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7"} {
		if got := bundleCost(m, M, 0, 0, 0); !approx(got, 5) {
			t.Errorf("%s input = %v, want 5", m, got)
		}
	}
}

func TestMessageCostPerFamily(t *testing.T) {
	cases := []struct {
		name  string
		model string
		u     MsgUsage
		want  float64
	}{
		{"sonnet-5-5 input", "claude-sonnet-5-5", MsgUsage{Input: M}, 2},
		{"sonnet-5 output", "claude-sonnet-5", MsgUsage{Output: M}, 10},
		{"sonnet-5-5 cache read", "claude-sonnet-5-5", MsgUsage{CacheRead: M}, 0.2},
		{"sonnet-4-6 input", "claude-sonnet-4-6", MsgUsage{Input: M}, 3},
		{"opus-5-5 cache read", "claude-opus-5-5", MsgUsage{CacheRead: M}, 0.2},
		{"opus-5-5 1h write", "claude-opus-5-5", MsgUsage{CacheWrite1h: M}, 8},
		{"opus-5-5 5m write", "claude-opus-5-5", MsgUsage{CacheWrite5m: M}, 5},
		{"opus-5 input", "claude-opus-5", MsgUsage{Input: M}, 5},
		{"opus-4-6 output", "claude-opus-4-6", MsgUsage{Output: M}, 25},
		// A 1M-token Haiku 5.5 prompt is above the 100K threshold: the upper tier.
		{"haiku-5-5 big prompt", "claude-haiku-5-5", MsgUsage{Input: M}, 0.50},
		{"haiku-5-5 small prompt", "claude-haiku-5-5", MsgUsage{Input: 50_000, Output: 1_000},
			50_000*0.10/1e6 + 1_000*0.50/1e6},
		// The tier counts every prompt token of the message, not just Input.
		{"haiku-5-5 cache read pushes tier", "claude-haiku-5-5", MsgUsage{Input: 10_000, CacheRead: 100_000},
			10_000*0.50/1e6 + 100_000*0.05/1e6},
		{"haiku-5-5 cache writes push tier", "claude-haiku-5-5", MsgUsage{CacheWrite5m: 60_000, CacheWrite1h: 41_000, Output: 1_000},
			60_000*0.50*1.25/1e6 + 41_000*0.50*2/1e6 + 1_000*2.5/1e6},
		{"haiku-5-5 exactly 100K stays low", "claude-haiku-5-5", MsgUsage{Input: 100_000}, 100_000 * 0.10 / 1e6},
		{"haiku-4-5 input", "claude-haiku-4-5-20251001", MsgUsage{Input: M}, 1},
		{"haiku-4-5 big prompt untiered", "claude-haiku-4-5", MsgUsage{Input: 200_000}, 0.2},
		{"fable cache read", "claude-fable-5-1", MsgUsage{CacheRead: M}, 0.25},
		{"fable 1h write", "claude-fable-5", MsgUsage{CacheWrite1h: M}, 20},
		{"synthetic", "<synthetic>", MsgUsage{Input: M, Output: M, CacheRead: M, CacheWrite5m: M, CacheWrite1h: M}, 0},
		{"empty", "", MsgUsage{Input: M}, 0},
	}
	for _, c := range cases {
		if got := MessageCostUSD(c.model, c.u); !approx(got, c.want) {
			t.Errorf("%s: MessageCostUSD = %v, want %v", c.name, got, c.want)
		}
	}
}

// A transcript with only the total cache_creation value (no 5m/1h split) is
// priced as a 5m write, i.e. 1.25x input.
func TestMessageCostLegacyCacheWrite(t *testing.T) {
	got := MessageCostUSD("claude-opus-5", MsgUsage{CacheWrite5m: M})
	if !approx(got, 6.25) {
		t.Errorf("legacy cache write = %v, want 6.25", got)
	}
}
