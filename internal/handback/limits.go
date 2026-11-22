package handback

import "time"

// HeadBytes is how much of a file detection reads. Every magic and header
// field the classifier looks at lives in the first 8 KiB (the furthest is the
// tar "ustar" marker at offset 257).
const HeadBytes = 8 << 10

// Caps on one scan. A hostile tree (deep chains, millions of empty files)
// must not stall the host or fill a record, so each cap ends the scan early
// and the report says which one.
const (
	// DefaultEntries bounds directory entries visited by one walk.
	DefaultEntries = 500_000
	// DefaultDepth bounds directory depth; os.Root walks stall on deep chains.
	DefaultDepth = 64
	// DefaultCandidates bounds files opened for a head read per scan.
	DefaultCandidates = 50_000
	// DefaultMaxFlags bounds flags stored per record.
	DefaultMaxFlags = 5_000
	// DefaultBudget bounds the wall-clock time of one walk.
	DefaultBudget = 30 * time.Second
	// DefaultHashPerFile: a larger file is classified but not hashed.
	DefaultHashPerFile = 64 << 20
	// DefaultHashTotal bounds the bytes hashed in one scan.
	DefaultHashTotal = 512 << 20
)

// Limits are the caps one scan honours.
type Limits struct {
	Entries, Depth, Candidates, MaxFlags int
	Budget                               time.Duration
	HashPerFile, HashTotal               int64
}

// DefaultLimits returns the shipped caps.
func DefaultLimits() Limits {
	return Limits{
		Entries:     DefaultEntries,
		Depth:       DefaultDepth,
		Candidates:  DefaultCandidates,
		MaxFlags:    DefaultMaxFlags,
		Budget:      DefaultBudget,
		HashPerFile: DefaultHashPerFile,
		HashTotal:   DefaultHashTotal,
	}
}
