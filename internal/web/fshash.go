package web

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashText is the version token for optimistic-locked writes: the lowercase hex
// sha256 of a file's bytes. A hash (not mtime) is used so an agent rewriting a
// file to the same size and second is still detected as a change.
func hashText(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
