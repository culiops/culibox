package box

import (
	"fmt"
	"regexp"
)

// Defaults for the per-box limits. Options zero values mean these.
const (
	DefaultPidsLimit = 512
	DefaultTmpSize   = "512m"
)

// tmpSizeRE is deliberately strict: the value is spliced into the --tmpfs
// option list, where a comma would add mount options (suid, mode=...).
var tmpSizeRE = regexp.MustCompile(`^[1-9][0-9]{0,5}[kmg]$`)

// ValidateLimits checks a box's pids limit and /tmp size. 0 and "" mean the
// defaults. The error names the config key it came from.
func ValidateLimits(pids int, tmpSize string) error {
	if pids != 0 && (pids < 64 || pids > 65536) {
		return fmt.Errorf("box_pids_limit %d out of range (want 64-65536, or 0 for the default)", pids)
	}
	if tmpSize != "" && !tmpSizeRE.MatchString(tmpSize) {
		return fmt.Errorf("box_tmp_size %q invalid (want a number and k, m or g, e.g. 512m)", tmpSize)
	}
	return nil
}
