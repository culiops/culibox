package box

import (
	"fmt"
	"regexp"
	"strconv"
)

// Defaults for the per-box limits. Options zero values mean these.
const (
	DefaultPidsLimit = 512
	DefaultTmpSize   = "512m"
	// DefaultMemory caps a box's RAM; --memory-swap is set equal so a box over
	// the cap is OOM-killed instead of swapping the host. Measured 2026-10-19:
	// peaks were 363-503 MiB for the browser scenario and 1866 MiB for a cold
	// scripts/test.sh, so ceil_GiB(1.5 x (1866 + 512) MiB) = 4.
	DefaultMemory = "4g"
	// minMemoryBytes is the smallest box_memory accepted (1g).
	minMemoryBytes = 1 << 30
)

// tmpSizeRE is deliberately strict: the value is spliced into the --tmpfs
// option list, where a comma would add mount options (suid, mode=...).
var tmpSizeRE = regexp.MustCompile(`^[1-9][0-9]{0,5}[kmg]$`)

// memorySizeBytes converts a validated size ("2048m") to bytes; k=1024, m=1024^2, g=1024^3.
func memorySizeBytes(s string) int64 {
	n, _ := strconv.ParseInt(s[:len(s)-1], 10, 64)
	switch s[len(s)-1] {
	case 'k':
		return n << 10
	case 'm':
		return n << 20
	default:
		return n << 30
	}
}

// ValidateLimits checks a box's pids limit, /tmp size and memory cap. 0 and ""
// mean the defaults. The error names the config key it came from.
func ValidateLimits(pids int, tmpSize, memory string) error {
	if pids != 0 && (pids < 64 || pids > 65536) {
		return fmt.Errorf("box_pids_limit %d out of range (want 64-65536, or 0 for the default)", pids)
	}
	if tmpSize != "" && !tmpSizeRE.MatchString(tmpSize) {
		return fmt.Errorf("box_tmp_size %q invalid (want a number and k, m or g, e.g. 512m)", tmpSize)
	}
	if memory != "" {
		if !tmpSizeRE.MatchString(memory) {
			return fmt.Errorf("box_memory %q invalid (want a number and k, m or g, e.g. 4g)", memory)
		}
		if memorySizeBytes(memory) < minMemoryBytes {
			return fmt.Errorf("box_memory %q too small (minimum 1g)", memory)
		}
	}
	return nil
}
