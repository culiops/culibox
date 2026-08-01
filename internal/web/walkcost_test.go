package web

import (
	"os"
	"testing"
	"time"
)

// TestWalkCostAgainstRealHome is a MEASUREMENT, not an assertion about
// correctness. The directory set descends past the file fairness prune, which
// roughly doubles the walk — and that walk runs on a 60-second background
// refresh against indexBudget, on a box also running agents. Skipped unless
// AB_WALK_COST=1 so it never slows an ordinary test run.
func TestWalkCostAgainstRealHome(t *testing.T) {
	if os.Getenv("AB_WALK_COST") != "1" {
		t.Skip("set AB_WALK_COST=1 to measure the real walk")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	ix := newFileIndex(home)
	ix.budget = time.Minute // measure the real cost, do not let the budget cut it
	start := time.Now()
	files, filesTrunc := ix.snapshot(kindFiles)
	dirs, dirsTrunc := ix.snapshot(kindDirs)
	t.Logf("walk %s — files=%d truncated=%v, dirs=%d truncated=%v",
		time.Since(start), len(files), filesTrunc, len(dirs), dirsTrunc)
}
