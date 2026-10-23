//go:build !linux

package box

// leafID is Linux only: off Linux the in-box comparison is not made, because
// inode numbers seen through the box VM's file share do not match the host's.
func leafID(string) (*FileID, error) { return nil, nil }
