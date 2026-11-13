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
// boxed spawn starts the VM), and a docker that is installed but not running
// or not permitted only shows up on the first call to it, after this boot-time
// check; #154 tracks telling those apart.
func boxUnavailableReason(err error) string {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "Docker is not installed"
	case errors.Is(err, boxvm.ErrColimaNotFound):
		return "colima is not installed"
	}
	return "run `agent-box doctor` for details"
}
