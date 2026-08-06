package analytics

import "strings"

// Model pricing, USD per 1,000,000 tokens. Prices verified 2026-08-06 against the
// claude-api skill (first-party Claude API rates):
//
//	opus / opus-5 / opus-4.x : input $5,  output $25
//	sonnet / sonnet-5        : input $3,  output $15
//	haiku                    : input $1,  output $5
//	fable                    : input $10, output $50
//
// Cache is derived from the input rate: a cache READ costs 0.1x input, a cache
// WRITE (cache_creation, 5-minute TTL) costs 1.25x input. These multipliers are
// the same for every family, so only the base input/output pair is tabled.
//
// Sonnet 5's introductory $2/$10 (through 2026-08-31) is deliberately IGNORED —
// v1 uses the standard $3/$15 for every day. Refreshing rates is a one-file edit.
//
// The table is keyed by a substring of the model id (e.g. "opus" matches
// "claude-opus-5" and "claude-opus-4-8"). Families do not overlap. An id that
// matches no family prices to zero — the row is still shown with its raw token
// counts; only its dollar figure is 0.
type modelRate struct {
	input  float64 // $ per 1M input tokens
	output float64 // $ per 1M output tokens
}

// familyRates is ordered only for readability; matching is by substring and the
// families are mutually exclusive, so order does not affect the result.
var familyRates = []struct {
	key  string
	rate modelRate
}{
	{"opus", modelRate{input: 5, output: 25}},
	{"sonnet", modelRate{input: 3, output: 15}},
	{"haiku", modelRate{input: 1, output: 5}},
	{"fable", modelRate{input: 10, output: 50}},
}

const (
	cacheReadMult  = 0.1  // cache_read costs 0.1x the input rate
	cacheWriteMult = 1.25 // cache_creation (5m) costs 1.25x the input rate
)

// rateFor returns the pricing for a model id, and whether a family matched.
func rateFor(model string) (modelRate, bool) {
	for _, f := range familyRates {
		if strings.Contains(model, f.key) {
			return f.rate, true
		}
	}
	return modelRate{}, false
}

// CostUSD returns the dollar cost of a token bundle for the given model. Cache
// read/write are priced off the input rate (0.1x / 1.25x). Unknown model
// families (including "" and "<synthetic>") return 0.
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
		perTok(cacheRead, r.input*cacheReadMult) +
		perTok(cacheCreation, r.input*cacheWriteMult)
}
