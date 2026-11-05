package box

import (
	"errors"
	"testing"
)

// The refusal for a workspace that is not the checked path is both
// ErrBoxUnavailable (shown to the user as is) and ErrWorkspaceMountChanged
// (what the tests and callers tell it apart by), and its text is fixed.
func TestWsChangedIsBothErrorsAndReadsAsFixedText(t *testing.T) {
	err := wsChanged("/w/p", "it is now a symlink")
	if !errors.Is(err, ErrBoxUnavailable) || !errors.Is(err, ErrWorkspaceMountChanged) {
		t.Fatalf("err = %v, want ErrBoxUnavailable and ErrWorkspaceMountChanged", err)
	}
	if errors.Is(err, ErrUnboxableWorkspace) {
		t.Fatalf("err = %v must not be ErrUnboxableWorkspace: the folder is fine, the start raced", err)
	}
	if want := "workspace /w/p: it is now a symlink; refusing"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}
