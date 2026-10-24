// Package statusline holds agent-box's own Claude Code statusLine script. It is
// a leaf package so both internal/account (host accounts) and internal/bundle
// (boxes) can ship the same bytes without importing each other.
package statusline

import (
	_ "embed"
	"strings"
)

// Script is the embedded statusLine script. It harvests the account's rate
// limits to <config dir>/agent-box-rate-limits.json and prints a footer.
//
//go:embed statusline-ratelimits.sh
var Script []byte

// Name is the script's file name wherever it is installed.
const Name = "statusline-ratelimits.sh"

// IsOwnCommand reports whether a statusLine command runs this script. It is a
// name match, the same one the script applies to a delegate, because a caller
// that does not know where the script was installed (the bundle builder reading
// a host settings.json) cannot compare full paths.
func IsOwnCommand(command string) bool { return strings.Contains(command, Name) }
