package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/boxvm"
)

// The reason is a sentence for the profile page. It names the missing tool or
// points at `agent-box doctor`; it never carries the error's own text, which can
// hold a path.
func TestBoxUnavailableReason(t *testing.T) {
	const doctor = "run `culibox doctor` for details"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"docker missing", fmt.Errorf("box: docker binary %q not found on PATH: %w", "docker", &exec.Error{Name: "docker", Err: exec.ErrNotFound}), "Docker is not installed"},
		{"docker missing, bare sentinel", exec.ErrNotFound, "Docker is not installed"},
		{"colima missing", fmt.Errorf("layer: %w", boxvm.ErrColimaNotFound), "colima is not installed"},
		{"anything else", errors.New("box: no image /x/y"), doctor},
	} {
		got := boxUnavailableReason(tc.err)
		if got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "/") && tc.want == doctor {
			t.Errorf("%s: reason %q carries a path", tc.name, got)
		}
	}
	if got := boxUnavailableReason(errors.New("box: no image /x/y")); strings.Contains(got, "/") || strings.Contains(got, "x/y") {
		t.Errorf("the generic reason leaks the error text: %q", got)
	}
}

// The real producers of the two errors map to their sentences: a missing docker
// from LookDocker, and (OS-neutral: LookColima is called with a name that is on
// no PATH) a missing colima from LookColima. The darwin branch of the layer
// builder returns that error unchanged.
func TestBoxUnavailableReasonFromTheLookups(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := box.LookDocker("no-such-docker-xyz"); boxUnavailableReason(err) != "Docker is not installed" {
		t.Errorf("LookDocker error %v maps to %q", err, boxUnavailableReason(err))
	}
	_, err := boxvm.LookColima("no-such-colima-xyz")
	if err == nil {
		t.Fatal("LookColima found a binary that does not exist")
	}
	if boxUnavailableReason(err) != "colima is not installed" {
		t.Errorf("LookColima error %v maps to %q", err, boxUnavailableReason(err))
	}
	if !strings.Contains(err.Error(), "brew install colima") {
		t.Errorf("LookColima message lost its install hint: %v", err)
	}
}
