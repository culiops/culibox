package box

import (
	"os"
	"path/filepath"
	"testing"
)

// caseTwins makes <tmp>/X and returns it with its other spelling <tmp>/x. It
// skips when the volume is case-sensitive: there the two are different folders.
// The tmp dir is symlink-resolved (/var -> /private/var on macOS) so the test
// measures case and nothing else.
func caseTwins(t *testing.T) (upper, lower string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upper, lower = filepath.Join(tmp, "X"), filepath.Join(tmp, "x")
	if err := os.Mkdir(upper, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lower); err != nil {
		t.Skipf("volume is case-sensitive (%v): X and x are different folders", err)
	}
	return upper, lower
}

// TestOverlapsCaseInsensitiveVolume measures #298: on case-insensitive APFS the
// two spellings are one folder, so Overlaps must say so.
func TestOverlapsCaseInsensitiveVolume(t *testing.T) {
	upper, lower := caseTwins(t)
	if !Overlaps(upper, lower) {
		t.Errorf("Overlaps(%q, %q) = false on a case-insensitive volume", upper, lower)
	}
	if !Overlaps(filepath.Join(upper, "sub"), lower) {
		t.Errorf("Overlaps(%q, %q) = false on a case-insensitive volume", filepath.Join(upper, "sub"), lower)
	}
}

// TestClaimsCaseInsensitiveVolume measures the claims built on Overlaps with the
// other spelling of the folder.
func TestClaimsCaseInsensitiveVolume(t *testing.T) {
	upper, lower := caseTwins(t)
	boxes := []BoxInfo{{Name: "x", State: "running", Workspace: upper, Instance: "PROD"}}
	if _, busy := OtherInstanceHolds(boxes, lower, "DEV"); !busy {
		t.Errorf("OtherInstanceHolds passed %q while another instance holds %q", lower, upper)
	}
}
