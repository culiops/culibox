package analytics

import (
	"math"
	"testing"
)

const M = 1_000_000

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCostUSDPerFamily(t *testing.T) {
	cases := []struct {
		name                        string
		model                       string
		in, out, cacheRead, cacheCr int64
		want                        float64
	}{
		// Base input/output rates (per 1M tokens).
		{"opus input", "claude-opus-5", M, 0, 0, 0, 5},
		{"opus output", "claude-opus-4-8", 0, M, 0, 0, 25},
		{"sonnet input", "claude-sonnet-5", M, 0, 0, 0, 3},
		{"sonnet output", "claude-sonnet-5", 0, M, 0, 0, 15},
		{"haiku input", "claude-haiku-4-5-20251001", M, 0, 0, 0, 1},
		{"haiku output", "claude-haiku-4-5", 0, M, 0, 0, 5},
		{"fable input", "claude-fable-5", M, 0, 0, 0, 10},
		{"fable output", "claude-fable-5", 0, M, 0, 0, 50},
		// Cache derived from input: read = 0.1x, write(creation) = 1.25x.
		{"opus cache read", "claude-opus-5", 0, 0, M, 0, 0.5},
		{"opus cache write", "claude-opus-5", 0, 0, 0, M, 6.25},
		{"sonnet cache read", "claude-sonnet-5", 0, 0, M, 0, 0.3},
		// Combined, opus, one of each token type.
		{"opus combined", "claude-opus-5", M, M, M, M, 5 + 25 + 0.5 + 6.25},
		// Unknown / synthetic model families price to zero (row still shown w/ tokens).
		{"unknown", "gpt-4o", M, M, M, M, 0},
		{"empty model", "", M, M, M, M, 0},
		{"synthetic", "<synthetic>", M, M, M, M, 0},
	}
	for _, c := range cases {
		got := CostUSD(c.model, c.in, c.out, c.cacheRead, c.cacheCr)
		if !approx(got, c.want) {
			t.Errorf("%s: CostUSD = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCostUSDScalesLinearly(t *testing.T) {
	// Half a million input tokens on opus = half of $5.
	if got := CostUSD("claude-opus-5", M/2, 0, 0, 0); !approx(got, 2.5) {
		t.Errorf("half input = %v, want 2.5", got)
	}
	// Real-ish magnitude: 24.2B cache-read on opus should be a sane number, not overflow.
	if got := CostUSD("claude-opus-5", 0, 0, 24_200_000_000, 0); !approx(got, 24_200_000_000.0/M*0.5) {
		t.Errorf("big cache-read = %v", got)
	}
}
