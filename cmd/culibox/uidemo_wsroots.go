//go:build uidemo

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// A UI demo of the picker's "Other places" group: the scan reads these folders
// instead of /srv, /opt and the rest, so a browser check needs no real
// project outside home. Never part of a release build.
//
//	CULIBOX_WSROOTS_SCAN_BASES = colon-separated folders to scan (unset: the usual ones)

func init() {
	if v := os.Getenv("CULIBOX_WSROOTS_SCAN_BASES"); v != "" {
		uidemoScanBases = func() []string { return uidemoSplitBases(v) }
	}
}

// uidemoSplitBases is the folders in a colon-separated list, cleaned; blanks are dropped.
func uidemoSplitBases(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ":") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}
