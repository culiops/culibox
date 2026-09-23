package analytics

import "strings"

// Model pricing, USD per 1,000,000 tokens. Prices verified 2026-09-23 against the
// claude-api skill (first-party Claude API rates):
//
//	opus-5.5                 : input $4,  output $20, cache read $0.20
//	opus / opus-5 / opus-4.x : input $5,  output $25
//	sonnet / sonnet-5        : input $3,  output $15
//	haiku                    : input $1,  output $5
//	fable                    : input $10, output $50
//
// Cache is normally derived from the input rate: a cache READ costs 0.1x input,
// a cache WRITE (cache_creation, 5-minute TTL) costs 1.25x input. A family whose
// published cache rate does not follow the 0.1x rule sets cacheRead explicitly —
// Opus 5.5 reads at $0.20 on a $4 input rate, which is 0.05x, so deriving it
// would double the figure. Cache reads dominate real usage, so that is the most
// consequential number in this file.
//
// Sonnet 5's introductory $2/$10 (through 2026-08-31) is deliberately IGNORED —
// v1 uses the standard $3/$15 for every day. Refreshing rates is a one-file edit.
//
// The table is keyed by a substring of the model id and is matched
// MOST-SPECIFIC-FIRST: "claude-opus-5-5" contains "opus", so the order of
// familyRates below is load-bearing — a new, more specific key must be listed
// above the generic family it would otherwise fall through to. An id that
// matches no family prices to zero — the row is still shown with its raw token
// counts; only its dollar figure is 0.
type modelRate struct {
	input  float64 // $ per 1M input tokens
	output float64 // $ per 1M output tokens

	// cacheRead overrides the derived 0.1x input rate when non-zero.
	cacheRead float64 // $ per 1M cache-read tokens
}

// familyRates is matched in order, most specific key first. Keys are NOT
// mutually exclusive (see "opus-5-5" vs "opus"), so order decides the result.
var familyRates = []struct {
	key  string
	rate modelRate
}{
	{"opus-5-5", modelRate{input: 4, output: 20, cacheRead: 0.2}},
	{"opus", modelRate{input: 5, output: 25}},
	{"sonnet", modelRate{input: 3, output: 15}},
	{"haiku", modelRate{input: 1, output: 5}},
	{"fable", modelRate{input: 10, output: 50}},
}

const (
	cacheReadMult  = 0.1  // cache_read costs 0.1x the input rate, unless overridden
	cacheWriteMult = 1.25 // cache_creation (5m) costs 1.25x the input rate
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

// CostUSD returns the dollar cost of a token bundle for the given model. Cache
// read is the family's published rate (or 0.1x input), cache write is 1.25x
// input. Unknown model families (including "" and "<synthetic>") return 0.
func CostUSD(model string, input, output, cacheRead, cacheCreation int64) float64 {
	r, ok := rateFor(model)
	if !ok {
		return 0
	}
	perTok := func(n int64, ratePerM float64) float64 {
		return float64(n) / 1_000_000 * ratePerM
	}
	return perTok(input, r.input) +
		perTok(output, r.output) +
		perTok(cacheRead, r.cacheReadRate()) +
		perTok(cacheCreation, r.input*cacheWriteMult)
}
