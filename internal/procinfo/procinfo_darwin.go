//go:build darwin

package procinfo

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// kinfo fetches the kernel's process record. A missing pid makes sysctl return
// a zero-length record, which x/sys reports as EIO; the pid check below also
// rejects a record for some other process.
func kinfo(pid int) (*unix.KinfoProc, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nil, fmt.Errorf("procinfo: pid %d: %w", pid, err)
	}
	if int(k.Proc.P_pid) != pid {
		return nil, fmt.Errorf("procinfo: pid %d not found", pid)
	}
	return k, nil
}

func Ppid(pid int) (int, error) {
	k, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return int(k.Eproc.Ppid), nil
}

// StartTime is the process start as "<sec>.<usec>". Claude Code's registry
// writes the same instant as a UTC ctime string; session.StartMatches compares
// the two.
func StartTime(pid int) (string, error) {
	k, err := kinfo(pid)
	if err != nil {
		return "", err
	}
	tv := k.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), nil
}

func procArgs(pid int) ([]string, []string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, fmt.Errorf("procinfo: procargs2 pid %d: %w", pid, err)
	}
	return parseProcArgs2(b)
}

func Args(pid int) ([]string, error) {
	a, _, err := procArgs(pid)
	return a, err
}

// Env is readable only for processes of our own uid; the kernel refuses others.
func Env(pid int) ([]string, error) {
	_, e, err := procArgs(pid)
	return e, err
}

func PIDs() ([]int, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(ks))
	for i := range ks {
		if p := int(ks[i].Proc.P_pid); p > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}
