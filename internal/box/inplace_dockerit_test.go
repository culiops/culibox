//go:build dockerit

package box

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file proves spec §5 against a real docker daemon: with GuardInPlace, the
// in-place attacks (planting what host git or a host-tier agent later runs or
// loads) fail from INSIDE a box, and the host's view of the repository is
// untouched afterwards. The unit tests in inplace_test.go check which mounts are
// derived; only a real mount namespace shows that a bind-mounted file cannot be
// renamed, removed or written.
//
// Everything it creates is labelled with the instance A1IT-UNIT, which no real
// agent-box instance uses, and t.Cleanup removes exactly those containers.

const itInstance = "A1IT-UNIT"

// itDocker is the usable docker binary, or the test is skipped.
func itDocker(t *testing.T) (bin, image string) {
	t.Helper()
	bin, err := LookDocker("docker")
	if err != nil {
		t.Skipf("docker unusable: %v", err)
	}
	if out, err := exec.Command(bin, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker unusable: %v: %s", err, bytes.TrimSpace(out))
	}
	image = os.Getenv("AGENTBOX_IT_IMAGE")
	if image == "" {
		image = "culiops/agent-box:base"
	}
	if out, err := exec.Command(bin, "image", "inspect", image, "--format", "{{.Id}}").CombinedOutput(); err != nil {
		t.Skipf("image %s not present: %v: %s", image, err, bytes.TrimSpace(out))
	}
	return bin, image
}

// itRemoveBoxes removes every container of the test instance and nothing else.
func itRemoveBoxes(bin string) {
	out, err := exec.Command(bin, "ps", "-aq", "--filter", "label="+LabelInstance+"="+itInstance).Output()
	if err != nil {
		return
	}
	if ids := strings.Fields(string(out)); len(ids) > 0 {
		exec.Command(bin, append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

func itBoxCount(t *testing.T, bin string) []string {
	t.Helper()
	out, err := exec.Command(bin, "ps", "-aq", "--filter", "label="+LabelInstance+"="+itInstance).Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return strings.Fields(string(out))
}

func itManager(t *testing.T, bin, image string) *Manager {
	t.Helper()
	m, err := New(Options{
		DockerBin:    bin,
		Image:        image,
		Instance:     itInstance,
		StateRoot:    t.TempDir(),
		Client:       CLIClient{Bin: bin},
		GuardInPlace: true,
		EgressAll:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// itGit runs host git in dir with no global or system config, and a fixed identity.
func itGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=it", "-c", "user.email=it@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func itMustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := itGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return out
}

func itWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// itRepo creates a committed repository in a fresh temp dir: core.hooksPath set
// to hooks (a committed directory), plus CLAUDE.md.
func itRepo(t *testing.T, hooks string) string {
	t.Helper()
	// EvalSymlinks: the box and its guards are derived from the resolved path.
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	itMustGit(t, ws, "init", "-q")
	itMustGit(t, ws, "config", "core.hooksPath", hooks)
	itWrite(t, filepath.Join(ws, hooks, ".gitkeep"), "")
	itWrite(t, filepath.Join(ws, "CLAUDE.md"), "# rules\n")
	itMustGit(t, ws, "add", "-A")
	itMustGit(t, ws, "commit", "-q", "-m", "init")
	return ws
}

// itBox wraps ws and returns the box's name. The session argv is never run: the
// test drives the box with docker exec directly.
func itBox(t *testing.T, m *Manager, ws string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _, name, err := m.Wrap(ctx, ws, "it-"+filepath.Base(ws), BoxKey{}, []string{"sleep", "1"}, nil, nil)
	if err != nil {
		t.Fatalf("Wrap(%s): %v", ws, err)
	}
	return name
}

// itExec runs an attack inside the box with the workspace as cwd and returns the
// exit code and combined output. A git identity is supplied so a refused commit
// is refused by the guard, not for want of a name.
func itExec(t *testing.T, bin, box, ws, attack string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec", "-w", ws,
		"-e", "GIT_AUTHOR_NAME=it", "-e", "GIT_AUTHOR_EMAIL=it@example.com",
		"-e", "GIT_COMMITTER_NAME=it", "-e", "GIT_COMMITTER_EMAIL=it@example.com",
		box, "sh", "-c", attack)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("docker exec %q: %v", attack, err)
	}
	return ee.ExitCode(), string(out)
}

// itMustFail: the attack exits non-zero. 125-127 are docker's own failures and
// "command not found", which would pass for the wrong reason.
func itMustFail(t *testing.T, bin, box, ws, attack string) {
	t.Helper()
	code, out := itExec(t, bin, box, ws, attack)
	switch code {
	case 0:
		t.Errorf("ATTACK SUCCEEDED: %q (output: %s)", attack, strings.TrimSpace(out))
	case 125, 126, 127:
		t.Errorf("attack %q did not run (exit %d): %s", attack, code, strings.TrimSpace(out))
	default:
		// A refusal must be the guard's. ENOENT means the thing attacked is not
		// there (Wrap stopped creating .claude, say) and proves nothing; and the
		// text must be one of the kernel's refusals for a read-only bind mount
		// (EROFS for writes, EBUSY for rename/remove of a mount point, EACCES).
		out = strings.TrimSpace(out)
		if strings.Contains(out, "No such file or directory") {
			t.Errorf("attack %q failed vacuously (exit %d), the target is missing: %s", attack, code, out)
			return
		}
		if !strings.Contains(out, "Read-only file system") && !strings.Contains(out, "Device or resource busy") &&
			!strings.Contains(out, "Permission denied") {
			t.Errorf("attack %q failed (exit %d) for a reason that is not the guard: %s", attack, code, out)
			return
		}
		t.Logf("refused (exit %d): %-60s %s", code, attack, out)
	}
}

func itMustSucceed(t *testing.T, bin, box, ws, control string) {
	t.Helper()
	if code, out := itExec(t, bin, box, ws, control); code != 0 {
		t.Errorf("control %q failed (exit %d): %s", control, code, strings.TrimSpace(out))
	}
}

// itEntries lists the names in dir (excluding .gitkeep, the committed placeholder).
func itEntries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		if de.Name() != ".gitkeep" {
			names = append(names, de.Name())
		}
	}
	return names
}

func itAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists on the host (err=%v)", path, err)
	}
}

func TestInPlaceAttacksFailInARealBox(t *testing.T) {
	bin, image := itDocker(t)
	itRemoveBoxes(bin) // leftovers of an aborted run of this test, and only those
	t.Cleanup(func() { itRemoveBoxes(bin) })
	m := itManager(t, bin, image)

	ws := itRepo(t, ".githooks")
	// MARK lives in its own temp dir, so only a planted fsmonitor/hook that ran
	// on the host could ever create it.
	mark := filepath.Join(t.TempDir(), "marker")
	box := itBox(t, m, ws)

	attacks := []string{
		`echo x > .git/hooks/post-checkout`,
		`git config core.fsmonitor "touch ` + mark + `"`,
		`echo {} > .claude/settings.json`,
		`mv .claude .c2`,
		`rmdir .claude`,
		`echo pwn >> CLAUDE.md`,
		`rm CLAUDE.md`,
		`echo x > .githooks/pre-commit`,
		`git commit --allow-empty -m x`,
		`echo ../evil > .git/commondir`, // after the commit: unguarded, it would break git for the rest
		`mv .git .git-old`,              // last: unguarded, it would hide every attack after it
	}
	for _, a := range attacks {
		itMustFail(t, bin, box, ws, a)
	}
	// Controls: the box is still a working place to do ordinary work, so the
	// failures above are the guard and not a broken box.
	for _, c := range []string{`git status`, `git log -1`, `echo ok > newfile`} {
		itMustSucceed(t, bin, box, ws, c)
	}

	// The host's view.
	if out, err := itGit(t, ws, "status"); err != nil {
		t.Errorf("host git status: %v: %s", err, out)
	}
	itAbsent(t, mark)
	if out, _ := itGit(t, ws, "config", "--get", "core.fsmonitor"); out != "" {
		t.Errorf("core.fsmonitor was planted: %q", out)
	}
	itAbsent(t, filepath.Join(ws, ".git", "commondir"))
	itAbsent(t, filepath.Join(ws, ".git-old"))
	itAbsent(t, filepath.Join(ws, ".git", "hooks", "post-checkout"))
	itAbsent(t, filepath.Join(ws, ".claude", "settings.json"))
	itAbsent(t, filepath.Join(ws, ".c2"))
	if b, err := os.ReadFile(filepath.Join(ws, "CLAUDE.md")); err != nil || string(b) != "# rules\n" {
		t.Errorf("CLAUDE.md changed: %q (%v)", b, err)
	}
	if got := itEntries(t, filepath.Join(ws, ".githooks")); len(got) != 0 {
		t.Errorf(".githooks is not empty: %v", got)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "newfile")); err != nil || string(b) != "ok\n" {
		t.Errorf("control write did not land: %q (%v)", b, err)
	}
	// Not fatal: a guard regression must still reach the subtests below.
	if n, err := itGit(t, ws, "rev-list", "--count", "HEAD"); err != nil || n != "1" {
		t.Errorf("a commit landed or history is unreadable: count=%q err=%v", n, err)
	}

	// A hooks directory below the workspace root: binding only the leaf could be
	// beaten by renaming its parent (fix round 1 of Task 4).
	t.Run("nestedHooks", func(t *testing.T) {
		ws := itRepo(t, "tools/hooks")
		box := itBox(t, m, ws)
		itMustFail(t, bin, box, ws, `mv tools tools2 && mkdir -p tools/hooks && echo x > tools/hooks/pre-commit`)
		itMustFail(t, bin, box, ws, `echo x > tools/hooks/pre-commit`)
		itMustSucceed(t, bin, box, ws, `echo ok > newfile`)
		if got := itEntries(t, filepath.Join(ws, "tools", "hooks")); len(got) != 0 {
			t.Errorf("tools/hooks is not empty: %v", got)
		}
		itAbsent(t, filepath.Join(ws, "tools2"))
		if out, err := itGit(t, ws, "status"); err != nil {
			t.Errorf("host git status: %v: %s", err, out)
		}
	})

	// A linked worktree: .git is a gitfile whose gitdir: line the box must not
	// be able to rewrite.
	t.Run("worktree", func(t *testing.T) {
		main := itRepo(t, ".githooks")
		parent, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		wt := filepath.Join(parent, "wt")
		itMustGit(t, main, "worktree", "add", "-q", "-b", "wt", wt)
		gitfile, err := os.ReadFile(filepath.Join(wt, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		box := itBox(t, m, wt)
		itMustFail(t, bin, box, wt, `printf 'gitdir: /tmp/evil\n' > .git`)
		itMustFail(t, bin, box, wt, `mv .git .git-old`)
		itMustFail(t, bin, box, wt, `echo pwn >> CLAUDE.md`)
		itMustFail(t, bin, box, wt, `echo x > .githooks/pre-commit`)
		itMustSucceed(t, bin, box, wt, `echo ok > newfile`)
		if b, err := os.ReadFile(filepath.Join(wt, ".git")); err != nil || !bytes.Equal(b, gitfile) {
			t.Errorf(".git gitfile changed: %q (%v)", b, err)
		}
		itAbsent(t, filepath.Join(wt, ".git-old"))
		if got := itEntries(t, filepath.Join(wt, ".githooks")); len(got) != 0 {
			t.Errorf(".githooks is not empty: %v", got)
		}
		if out, err := itGit(t, wt, "status"); err != nil {
			t.Errorf("host git status: %v: %s", err, out)
		}
	})

	// Submodules cannot be guarded exactly, so the workspace is refused before
	// any container exists.
	t.Run("submodules", func(t *testing.T) {
		itRemoveBoxes(bin) // the earlier cases' boxes: the refused one must leave none
		ws := itRepo(t, ".githooks")
		itWrite(t, filepath.Join(ws, ".gitmodules"), "[submodule \"x\"]\n\tpath = x\n\turl = https://example.invalid/x\n")
		_, _, _, err := m.Wrap(context.Background(), ws, "it-submodules", BoxKey{}, []string{"sleep", "1"}, nil, nil)
		if !errors.Is(err, ErrUnboxableWorkspace) {
			t.Fatalf("Wrap err = %v, want ErrUnboxableWorkspace", err)
		}
		if left := itBoxCount(t, bin); len(left) != 0 {
			t.Errorf("a container exists for a refused workspace: %v", left)
		}
	})
}
