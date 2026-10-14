//go:build linux

package procinfo

import (
	"fmt"
	"os"
	"strconv"
)

func stat(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return string(b), err
}

// Ppid is /proc/<pid>/stat field 4.
func Ppid(pid int) (int, error) {
	s, err := stat(pid)
	if err != nil {
		return 0, err
	}
	f, ok := statField(s, 4)
	if !ok {
		return 0, fmt.Errorf("procinfo: malformed stat for pid %d", pid)
	}
	return strconv.Atoi(f)
}

// StartTime is /proc/<pid>/stat field 22 (starttime, clock ticks since boot).
func StartTime(pid int) (string, error) {
	s, err := stat(pid)
	if err != nil {
		return "", err
	}
	f, ok := statField(s, 22)
	if !ok {
		return "", fmt.Errorf("procinfo: malformed stat for pid %d", pid)
	}
	return f, nil
}

// Args is /proc/<pid>/cmdline.
func Args(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	return splitNul(b), nil
}

// Env is /proc/<pid>/environ — readable only for our own processes.
func Env(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	return splitNul(b), nil
}

// PIDs lists every numeric entry of /proc.
func PIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, pid)
		}
	}
	return out, nil
}
