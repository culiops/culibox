package secretstore

import (
	"fmt"
	"strings"
)

const maxKeychainReason = 200

// keychainReason turns security(1)'s stderr into a short, safe reason: the
// first line, without the "security: " prefix, printable ASCII only, at most
// 200 characters. Empty falls back to the exit code. Only stderr is passed in;
// stdout (the key) is never read on an error path. Untagged so it is testable
// on every platform.
func keychainReason(stderr []byte, exitCode int) string {
	line := string(stderr)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimPrefix(strings.TrimSpace(line), "security: ")
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if c := line[i]; c >= 0x20 && c <= 0x7e {
			b.WriteByte(c)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxKeychainReason {
		out = strings.TrimRight(out[:maxKeychainReason], " ")
	}
	if out == "" {
		return fmt.Sprintf("security exited %d", exitCode)
	}
	return out
}
