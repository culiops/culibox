package box

import (
	"os"
	"runtime"
	"testing"
)

// Off Linux the host records no workspace identity (a Mac's inode numbers are
// not the ones the box sees); the VM read in the box layer supplies it. Runs
// only off Linux, and is vet-compiled for GOOS=darwin on a Linux host.
func TestWorkspaceIDOffLinuxIsNil(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux records the identity (contextid_linux_test.go)")
	}
	d := ctxTmp(t)
	id, err := workspaceID(d)
	if id != nil || err != nil {
		t.Fatalf("workspaceID = %+v, %v; want nil, nil", id, err)
	}
	fi, err := os.Lstat(d)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfoID(fi); got != nil {
		t.Fatalf("fileInfoID = %+v, want nil", got)
	}
}
