//go:build !darwin

package box

// platformFlagOps: the guard locks are macOS only (#96). Linux needs none: its
// file systems compare names exactly, so the read-only bind of a guarded file
// covers every name that reaches it.
func platformFlagOps() flagOps { return nil }
