package box

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnderMountFold(t *testing.T) {
	cases := []struct {
		path, mount string
		fold, want  bool
	}{
		{"/a/Repo/x", "/a/repo", true, true},
		{"/a/Repo/x", "/a/repo", false, false},
		{"/a/repox", "/a/repo", true, false},
		{"/a/repox", "/a/repo", false, false},
		{"/a/Repo", "/a/repo", true, true},
		{"/a/repo", "/a/repo", false, true},
	}
	for _, c := range cases {
		if got := underMount(c.path, []string{c.mount}, c.fold); got != c.want {
			t.Errorf("underMount(%q, %q, fold=%v) = %v, want %v", c.path, c.mount, c.fold, got, c.want)
		}
	}
}

// withFold sets the host-path case folding for one test (macOS volumes are
// case-insensitive, so there the two spellings are one folder).
func withFold(t *testing.T, v bool) {
	t.Helper()
	old := inPlaceFold
	inPlaceFold = v
	t.Cleanup(func() { inPlaceFold = old })
}

// On Linux the twin spellings are different folders, so <tmp>/repo does not
// exist: the seam is driven with fold on to stand in for a case-insensitive Mac.
func TestInPlaceGuardFoldsCase(t *testing.T) {
	setup := func(t *testing.T, hooksPath string) (ws string, other string) {
		tmp := realTemp(t)
		ws = filepath.Join(tmp, "Repo")
		other = filepath.Join(tmp, "repo")
		gitRepo(t, ws, "[core]\n\thooksPath = "+strings.ReplaceAll(hooksPath, "<o>", other)+"\n")
		mkdirs(t, filepath.Join(ws, "hooks"))
		return ws, other
	}
	t.Run("hooks dir in other case is bound spelled under ws", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>/hooks")
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude", "hooks")
	})
	t.Run("nested hooks dir in other case binds its top dir under ws", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>/tools/hooks")
		mkdirs(t, filepath.Join(ws, "tools", "hooks"))
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude", "tools")
	})
	t.Run("without fold the other case is outside", func(t *testing.T) {
		withFold(t, false)
		ws, _ := setup(t, "<o>/hooks")
		got, err := inPlaceMounts(ws)
		if err != nil {
			t.Fatal(err)
		}
		wantMounts(t, ws, got, ".git", ".claude")
	})
	t.Run("hooksPath is the workspace in other case", func(t *testing.T) {
		withFold(t, true)
		ws, _ := setup(t, "<o>")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "workspace itself") {
			t.Fatalf("want a workspace-itself refusal, got %v", err)
		}
	})
	t.Run("gitdir in other case inside ws is refused", func(t *testing.T) {
		withFold(t, true)
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "Repo")
		mkdirs(t, ws)
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.Join(tmp, "repo", ".gitx")+"\n")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "inside the workspace") {
			t.Fatalf("want an inside-the-workspace refusal, got %v", err)
		}
	})
	t.Run("workspace inside the git dir in other case is refused", func(t *testing.T) {
		withFold(t, true)
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "wsq")
		mkdirs(t, ws, filepath.Join(tmp, "WSQ", "g"))
		// The git dir is spelled WSQ/g, so the workspace wsq is not inside it as
		// spelled, but it is the folder that holds it: refused as inside ws.
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.Join(tmp, "WSQ", "g")+"\n")
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "inside the workspace") {
			t.Fatalf("want an inside-the-workspace refusal, got %v", err)
		}
	})
}
