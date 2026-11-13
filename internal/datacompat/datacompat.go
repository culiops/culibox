// Package datacompat is the one place that says "this file was written by a
// newer CuliBox". An older CuliBox started on data a newer one wrote must refuse
// that file with a message that names it, never overwrite, regenerate or hide
// it (#229 part C). Every loader that can meet such a file returns Newer, so the
// same words and the same errors.Is check serve the startup log, the doctor and
// the tests.
package datacompat

import (
	"errors"
	"fmt"
)

// ErrNewer marks a file written by a newer CuliBox than this one.
var ErrNewer = errors.New("written by a newer CuliBox")

// Hint tells the user how to get out: the data is not lost, only unreadable here.
const Hint = "run the CuliBox that wrote it, or restore the backup taken before the upgrade (see docs/UPGRADING.md)"

// Newer reports file as written by a newer CuliBox. detail says what this
// version saw that it does not understand. errors.Is(err, ErrNewer) holds.
func Newer(file, detail string) error {
	return fmt.Errorf("%s was %w (%s); %s", file, ErrNewer, detail, Hint)
}
