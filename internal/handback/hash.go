package handback

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const (
	noteTooLarge = "not hashed: larger than 64 MiB"
	noteBudget   = "not hashed: hash budget for this scan used up"
	noteUnread   = "not hashed: could not be read"
)

// hashCapped returns the hex SHA-256 of r, reading at most perFile bytes and
// never more than what is left in *budget (nil means no scan-wide budget). A
// longer stream gets no sum and a note saying which cap stopped it. The bytes
// read are taken off *budget either way, so one hostile file cannot make a
// scan hash more than its budget. It reads at most one byte past the cap.
func hashCapped(r io.Reader, perFile int64, budget *int64) (sum, note string) {
	limit := perFile
	byBudget := false
	if budget != nil {
		if *budget <= 0 {
			return "", noteBudget
		}
		if *budget < limit {
			limit, byBudget = *budget, true
		}
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, limit+1))
	if budget != nil {
		*budget -= min(n, limit)
	}
	switch {
	case err != nil:
		return "", noteUnread
	case n > limit && byBudget:
		return "", noteBudget
	case n > limit:
		return "", noteTooLarge
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}
