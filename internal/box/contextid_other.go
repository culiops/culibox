//go:build !linux

package box

// leafID is Linux only: off Linux the host records no identity, because inode
// numbers on the host (a Mac's APFS) are not the ones the box sees through the
// box VM's file share. On macOS the box layer reads the identity inside the VM
// instead (Options.VMIDs, Mount.CheckID), so the in-box comparison is still made.
func leafID(string) (*FileID, error) { return nil, nil }
