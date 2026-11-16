package main

import (
	"errors"
	"os/exec"

	"github.com/culiops/culibox/internal/boxvm"
)

// boxUnavailableReason is the sentence the profile page shows when the
// containment layer could not be built ("Boxes are unavailable on this machine:
// <reason>"). It maps only what the build error identifies: docker not found,
// and (macOS) colima not found. Anything else points at `agent-box doctor`.
// It never returns err.Error(): that text can hold a path, and this one goes
// to the browser.
//
// A stopped colima VM is not an error here (the layer builds, and the first
// boxed spawn starts the VM). A docker that is installed but whose daemon is
// not running or not permitted does not fail the build either, because it can
// change while CuliBox runs: it is reported live as /api/meta box_daemon_reason
// and as the refusal of a boxed spawn (boxdaemon.go, #154).
func boxUnavailableReason(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "Docker is not installed"
	case errors.Is(err, boxvm.ErrColimaNotFound):
		return "colima is not installed"
	}
	return "run `culibox doctor` for details"
}
