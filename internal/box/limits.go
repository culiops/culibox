package box

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Defaults for the per-box limits. Options zero values mean these.
const (
	DefaultPidsLimit = 512
	DefaultTmpSize   = "512m"
	// DefaultMemory caps a box's RAM when docker's own total cannot be read (see
	// memoryCap); --memory-swap is set equal so a box over the cap is OOM-killed
	// instead of swapping the host. Measured 2026-10-19: peaks were 363-503 MiB
	// for the browser scenario and 1866 MiB for a cold scripts/test.sh, so
	// ceil_GiB(1.5 x (1866 + 512) MiB) = 4.
	DefaultMemory = "4g"
	// MemoryMax is the box_memory value that means all of docker's memory.
	MemoryMax = "max"
	// minMemoryBytes is the smallest box_memory accepted (1g).
	minMemoryBytes = 1 << 30
	// CPU bounds for box_cpus; 0 means no cap.
	minCPUs = 0.01
	maxCPUs = 1024
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
	if memory != "" && memory != MemoryMax {
		if !tmpSizeRE.MatchString(memory) {
			return fmt.Errorf("box_memory %q invalid (want a number and k, m or g, e.g. 4g, or max)", memory)
		}
		if memorySizeBytes(memory) < minMemoryBytes {
			return fmt.Errorf("box_memory %q too small (minimum 1g)", memory)
		}
	}
	return nil
}

// ValidateCPUs checks box_cpus: 0 means no cap, otherwise 0.01 to 1024 with at
// most two decimals. A value above docker's CPU count passes here; docker
// refuses it at spawn with its own message.
func ValidateCPUs(cpus float64) error {
	if cpus == 0 {
		return nil
	}
	if math.IsNaN(cpus) || cpus < minCPUs || cpus > maxCPUs || math.Abs(cpus*100-math.Round(cpus*100)) > 1e-6 {
		return fmt.Errorf("box_cpus %v invalid (want 0.01-1024 with at most 2 decimals, or 0 for no cap)", cpus)
	}
	return nil
}

// memoryCap is the --memory value for a new box. An explicit size is used as
// is. Unset is half of docker's MemTotal (on macOS that is the VM), at least
// 1g, so one runaway box cannot take the host's other services down with it;
// "max" is all of it. Either way --memory-swap equals --memory, so a box never
// swaps the host.
//
// Unset falls back to DefaultMemory, logged once, when MemTotal cannot be read.
// "max" fails closed instead: the operator asked for the machine's size, and
// silently running with some other cap (or none) would not be that. A good read
// is cached; a failure is not, because the macOS VM may simply not be up yet.
func (m *Manager) memoryCap(ctx context.Context) (string, error) {
	if m.opts.Memory != "" && m.opts.Memory != MemoryMax {
		return m.opts.Memory, nil
	}
	total, err := m.dockerMemTotal(ctx)
	if err != nil {
		if m.opts.Memory == MemoryMax {
			return "", fmt.Errorf("box_memory %q: cannot read the docker host's memory: %v", MemoryMax, err)
		}
		m.mu.Lock()
		first := !m.memFallbackLogged
		m.memFallbackLogged = true
		m.mu.Unlock()
		if first {
			log.Printf("box: cannot read the docker host's memory (%v); box_memory unset, so boxes get %s", err, DefaultMemory)
		}
		return DefaultMemory, nil
	}
	mib := total >> 20
	if m.opts.Memory != MemoryMax {
		mib = max(mib/2, minMemoryBytes>>20)
	}
	return strconv.FormatInt(mib, 10) + "m", nil
}

// dockerMemTotal is the docker daemon's MemTotal in bytes, cached once read.
func (m *Manager) dockerMemTotal(ctx context.Context) (int64, error) {
	m.mu.Lock()
	cached := m.memTotal
	m.mu.Unlock()
	if cached > 0 {
		return cached, nil
	}
	out, err := m.opts.Client.Stdout(ctx, []string{"info", "--format", "{{.MemTotal}}"})
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil || n < 1<<20 {
		return 0, fmt.Errorf("docker info reported MemTotal %q", strings.TrimSpace(out))
	}
	m.mu.Lock()
	m.memTotal = n
	m.mu.Unlock()
	return n, nil
}
