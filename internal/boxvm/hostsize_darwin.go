package boxvm

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// HostSize is the Mac's logical CPU count and RAM in bytes, for sizing the box
// VM when the config leaves it unset. Memory is 0 if the kernel will not say.
func HostSize() (cpus int, memBytes uint64) {
	mem, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		mem = 0
	}
	return runtime.NumCPU(), mem
}
