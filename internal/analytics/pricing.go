package analytics

import "strings"

// Model pricing, USD per 1,000,000 tokens. Rates per the claude-api skill
// (first-party Claude API rates), refreshed 2026-10-28:
//
//	opus-5-5                         : input $4,  output $20, cache read $0.20
//	opus-5 / opus-4-8/4-7/4-6        : input $5,  output $25
//	sonnet-5-5 / sonnet-5            : input $2,  output $10, cache read $0.20
//	sonnet-4-6 and other sonnet      : input $3,  output $15
//	haiku-5-5, prompt <= 100K tokens : input $0.10, output $0.50
//	haiku-5-5, prompt >  100K tokens : input $0.50, output $2.50
//	haiku-4-5 and other haiku        : input $1,  output $5
//	fable-5-1 / fable                : input $10, output $50, fable-5-1 cache read $0.25
//
// Cache is normally derived from the input rate: a cache READ costs 0.1x input,
// a 5-minute cache WRITE costs 1.25x input and a 1-hour cache WRITE costs 2x
// input. A family whose published cache-read rate does not follow the 0.1x rule
// sets cacheRead explicitly (Opus 5.5, Sonnet 5.x, Fable 5.1), so deriving those
// would overcharge them. Cache reads dominate real usage, so this is the most
// consequential number in this file.
//
// Haiku 5.5 is tiered by prompt size: a message whose prompt (input + cache read
// + both cache writes) exceeds 100K tokens is priced at the upper tier, with
// cache reads and writes derived from that tier's input rate.
//
// The table is keyed by a substring of the model id and is matched
// MOST-SPECIFIC-FIRST: "claude-opus-5-5" contains "opus" and
// "claude-haiku-5-5" contains "haiku", so the order of familyRates below is
// load-bearing: a new, more specific key must be listed above the generic
// family it would otherwise fall through to. An id that matches no family
// prices to zero (never a guessed number): the row is still shown with its raw
// token counts; only its dollar figure is 0.
type modelRate struct {
	input  float64 // $ per 1M input tokens
	output float64 // $ per 1M output tokens

	// cacheRead overrides the derived 0.1x input rate when non-zero.
	cacheRead float64 // $ per 1M cache-read tokens

	// tier, when set, replaces this rate for any message whose prompt exceeds
	// tierAbove tokens (Haiku 5.5 only).
	tierAbove int64
	tier      *modelRate
}

// familyRates is matched in order, most specific key first. Keys are NOT
// mutually exclusive (see "opus-5-5" vs "opus"), so order decides the result.
var familyRates = []struct {
	key  string
	rate modelRate
}{
	{"opus-5-5", modelRate{input: 4, output: 20, cacheRead: 0.2}},
	{"opus", modelRate{input: 5, output: 25}},
	{"sonnet-5-5", modelRate{input: 2, output: 10, cacheRead: 0.2}},
	{"sonnet-5", modelRate{input: 2, output: 10, cacheRead: 0.2}},
	{"sonnet", modelRate{input: 3, output: 15}},
	{"haiku-5-5", modelRate{input: 0.10, output: 0.50, tierAbove: 100_000,
		tier: &modelRate{input: 0.50, output: 2.50}}},
	{"haiku", modelRate{input: 1, output: 5}},
	{"fable-5-1", modelRate{input: 10, output: 50, cacheRead: 0.25}},
	{"fable", modelRate{input: 10, output: 50}},
}

// Not tabled: the Mythos ids (claude-mythos-5, claude-mythos-5-1). They share
// Fable's per-token rates but are Project Glasswing only and their cache-read
// rate is unconfirmed, so they price to $0 and show raw token counts rather
// than a number we would be guessing.

const (
	cacheReadMult  = 0.1  // cache_read costs 0.1x the input rate, unless overridden
	cacheWriteMult = 1.25 // cache_creation (5m) costs 1.25x the input rate

	cacheWrite1hMult = 2.0 // cache_creation (1h) costs 2x the input rate
)

// rateFor returns the pricing for a model id, and whether a family matched.
// The first (most specific) matching key wins.
func rateFor(model string) (modelRate, bool) {
	for _, f := range familyRates {
		if strings.Contains(model, f.key) {
			return f.rate, true
		}
	}
	return modelRate{}, false
}

// cacheReadRate returns the per-1M cache-read price: the family's explicit rate
// when it publishes one, else the 0.1x derivation off input.
func (r modelRate) cacheReadRate() float64 {
	if r.cacheRead > 0 {
		return r.cacheRead
	}
	return r.input * cacheReadMult
}

// MsgUsage is the token usage of one assistant message.
type MsgUsage struct {
	Input, Output, CacheRead, CacheWrite5m, CacheWrite1h int64
}

// MessageCostUSD returns the dollar cost of one message. Pricing is per message
// because the Haiku 5.5 tier depends on that message's own prompt size. Unknown
// model families (including "" and "<synthetic>") return 0.
func MessageCostUSD(model string, u MsgUsage) float64 {
	r, ok := rateFor(model)
	if !ok {
		return 0
	}
	if r.tier != nil && u.Input+u.CacheRead+u.CacheWrite5m+u.CacheWrite1h > r.tierAbove {
		r = *r.tier
	}
	perTok := func(n int64, ratePerM float64) float64 {
		return float64(n) / 1_000_000 * ratePerM
	}
	return perTok(u.Input, r.input) +
		perTok(u.Output, r.output) +
		perTok(u.CacheRead, r.cacheReadRate()) +
		perTok(u.CacheWrite5m, r.input*cacheWriteMult) +
		perTok(u.CacheWrite1h, r.input*cacheWrite1hMult)
}

// CostUSD prices a pre-summed token bundle, treating cacheCreation as 5m writes.
// Kept for the aggregate callers in store.go until they price per message.
func CostUSD(model string, input, output, cacheRead, cacheCreation int64) float64 {
	return MessageCostUSD(model, MsgUsage{Input: input, Output: output, CacheRead: cacheRead, CacheWrite5m: cacheCreation})
}
