//go:build upgrade

// Package upgradetest is the vN-1 → vN upgrade test (#229 part B). It starts
// the previous release's binary on a throwaway HOME, fills its data root through
// that binary's API, upgrades to the current build, and fails on any file that
// changed, appeared or vanished outside an explicit allowlist, or on user data
// the current build cannot read. It also runs the previous binary again on the
// data the current one wrote (binary-only rollback).
//
// Every file carries the "upgrade" build tag, so go test ./... never builds it.
// The harness is test code, so it lives in _test.go files (*_harness_test.go):
// the repo's source guards (cmd/culibox paths_guard_test.go and
// clonedir_guard_test.go), which pin where product code writes and deletes,
// skip test files.
// Run it through scripts/upgrade-test.sh, which resolves and builds both
// binaries; go test -tags upgrade alone runs only the unit tests and skips the
// end-to-end test.
package upgradetest
