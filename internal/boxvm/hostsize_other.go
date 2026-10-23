//go:build !darwin

package boxvm

import "runtime"

// HostSize is only consulted on macOS, where the box VM exists. Elsewhere memory
// is reported unknown (0), which sizes by the old floor.
func HostSize() (cpus int, memBytes uint64) { return runtime.NumCPU(), 0 }
