//go:build dockerit

package clone

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

// These tests prove spec §6.4 against a real docker daemon: git works inside a
// clone box (and the read-only mounts hold), and the bundle return path imports
// nothing from other repositories and runs nothing a box planted. The unit tests
// stub Git and Docker; only a real container shows what the bundle command does
// with a hostile .git.
//
// Everything docker creates here is labelled with the instance A2AIT-UNIT, which
// no real agent-box instance uses, and t.Cleanup removes exactly those
// containers (boxes and bundle containers).

const citInstance = "A2AIT-UNIT"

// citDocker is the usable docker binary and the image to run, or the test is
// skipped (docker unusable) or failed (image missing).
func citDocker(t *testing.T) (bin, image string) {
	t.Helper()
	bin, err := box.LookDocker("docker")
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
		t.Fatalf("image %s not present: %v: %s", image, err, bytes.TrimSpace(out))
	}
	return bin, image
}

// citRemoveContainers removes every container of the test instance, box or
// bundle container, and nothing else.
func citRemoveContainers(bin string) {
	for _, label := range []string{box.LabelInstance, "io.culiops.agent-box.clonefetch"} {
		out, err := exec.Command(bin, "ps", "-aq", "--filter", "label="+label+"="+citInstance).Output()
		if err != nil {
			continue
		}
		if ids := strings.Fields(string(out)); len(ids) > 0 {
			exec.Command(bin, append([]string{"rm", "-f"}, ids...)...).Run()
		}
	}
}

func citLeftovers(t *testing.T, bin string) []string {
	t.Helper()
	var all []string
	for _, label := range []string{box.LabelInstance, "io.culiops.agent-box.clonefetch"} {
		out, err := exec.Command(bin, "ps", "-aq", "--filter", "label="+label+"="+citInstance).Output()
		if err != nil {
			t.Fatalf("docker ps: %v", err)
		}
		all = append(all, strings.Fields(string(out))...)
	}
	return all
}

// citGit runs host git in dir with no global or system config and a fixed
// identity. It is test setup and checking, never the code under test.
func citGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=it", "-c", "user.email=it@example.com",
		"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func citMustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := citGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return out
}

func citWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// citRealTemp is a fresh temp dir with symlinks resolved: the box and its
// guards are derived from the resolved path.
func citRealTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// citOf creates the host repo clone_of: commits, a .gitignore with
// docs/handover/, a CLAUDE.md, and docs/sp with a file.
func citOf(t *testing.T) string {
	t.Helper()
	of := filepath.Join(citRealTemp(t), "of")
	if err := os.MkdirAll(of, 0o755); err != nil {
		t.Fatal(err)
	}
	citMustGit(t, of, "init", "-q")
	citMustGit(t, of, "config", "user.name", "Host User")
	citMustGit(t, of, "config", "user.email", "host@example.com")
	citWrite(t, filepath.Join(of, ".gitignore"), "docs/handover/\n")
	citWrite(t, filepath.Join(of, "CLAUDE.md"), "# rules\n")
	citWrite(t, filepath.Join(of, "docs", "sp", "spec.md"), "the spec\n")
	citWrite(t, filepath.Join(of, "main.go"), "package main\n")
	citMustGit(t, of, "add", "-A")
	citMustGit(t, of, "commit", "-q", "-m", "init")
	citWrite(t, filepath.Join(of, "main.go"), "package main\n// two\n")
	citMustGit(t, of, "commit", "-q", "-am", "two")
	return of
}

// citSnapshot is a listing of every file under dirs (relative to base) with its
// contents or link target, to compare before and after a box has had its way.
func citSnapshot(t *testing.T, base string, rels ...string) string {
	t.Helper()
	var lines []string
	for _, rel := range rels {
		root := filepath.Join(base, rel)
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(base, p)
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				tgt, _ := os.Readlink(p)
				lines = append(lines, "L "+r+" -> "+tgt)
			case info.IsDir():
				lines = append(lines, "D "+r)
			default:
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				lines = append(lines, "F "+r+" "+info.Mode().String()+" "+string(b))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// citExec runs a command inside the box with the clone as cwd and returns the
// exit code and combined output.
func citExec(t *testing.T, bin, boxName, dir, cmdline string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "exec", "-w", dir, boxName, "sh", "-c", cmdline).CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("docker exec %q: %v", cmdline, err)
	}
	return ee.ExitCode(), string(out)
}

func citMustSucceed(t *testing.T, bin, boxName, dir, cmdline string) string {
	t.Helper()
	code, out := citExec(t, bin, boxName, dir, cmdline)
	if code != 0 {
		t.Errorf("control %q failed (exit %d): %s", cmdline, code, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out)
}

// citMustFail: the command exits non-zero for a reason that is the box's
// containment. 125-127 are docker's own failures and "command not found", and
// ENOENT means the target is missing: either would pass for the wrong reason.
// refusal says what the failure text must contain; "" accepts the kernel's
// refusals for a read-only bind mount (EROFS, EBUSY, EACCES).
func citMustFail(t *testing.T, bin, boxName, dir, cmdline string, refusal ...string) {
	t.Helper()
	code, out := citExec(t, bin, boxName, dir, cmdline)
	out = strings.TrimSpace(out)
	switch code {
	case 0:
		t.Errorf("ATTACK SUCCEEDED: %q (output: %s)", cmdline, out)
		return
	case 125, 126, 127:
		t.Errorf("command %q did not run (exit %d): %s", cmdline, code, out)
		return
	}
	if strings.Contains(out, "No such file or directory") {
		t.Errorf("command %q failed vacuously (exit %d), the target is missing: %s", cmdline, code, out)
		return
	}
	want := refusal
	if len(want) == 0 {
		want = []string{"Read-only file system", "Device or resource busy", "Permission denied"}
	}
	for _, w := range want {
		if strings.Contains(out, w) {
			t.Logf("refused (exit %d): %-52s %s", code, cmdline, firstLine(out))
			return
		}
	}
	t.Errorf("command %q failed (exit %d) for a reason that is not the containment: %s", cmdline, code, out)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func citRefs(t *testing.T, of string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range strings.Split(citMustGit(t, of, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if l == "" {
			continue
		}
		name, sha, _ := strings.Cut(l, " ")
		m[name] = sha
	}
	return m
}

// TestCloneCreationAndBoxGit: a clone made by EnsureClone is a working git
// repository inside a real clone box, and everything the clone mode promises to
// keep out of the box's reach stays out of it.
func TestCloneCreationAndBoxGit(t *testing.T) {
	bin, image := citDocker(t)
	citRemoveContainers(bin) // leftovers of an aborted run of this test, and only those
	t.Cleanup(func() { citRemoveContainers(bin) })

	of := citOf(t)
	dir := filepath.Join(citRealTemp(t), "clone")
	home := citRealTemp(t)
	c := config.Clone{Of: of, Dir: dir, Shares: []string{"CLAUDE.md", "docs/sp"}}

	g, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cm, err := Prepare(ctx, Env{Git: g, Home: home}, c, false)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The clone really was created by EnsureClone, with the repo's content.
	if _, err := os.Stat(filepath.Join(dir, ".git", "config")); err != nil {
		t.Fatalf("the clone has no .git: %v", err)
	}

	m, err := box.New(box.Options{
		DockerBin:    bin,
		Image:        image,
		Instance:     citInstance,
		StateRoot:    t.TempDir(),
		Client:       box.CLIClient{Bin: bin},
		GuardInPlace: true,
		EgressAll:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, name, err := m.Wrap(ctx, dir, "it-clone", box.BoxKey{Clone: &cm}, []string{"sleep", "1"}, nil, nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// The host advances main after the clone was made: the clone lacks this commit.
	citMustGit(t, of, "commit", "-q", "--allow-empty", "-m", "host advance")
	hostMain := citMustGit(t, of, "rev-parse", "main")

	refsBefore := citRefs(t, of)
	claudeBefore := citSnapshot(t, of, "CLAUDE.md", "docs/sp")
	gitBefore := citSnapshot(t, of, ".git")

	// Ordinary work in the box.
	citMustSucceed(t, bin, name, dir, `git checkout -q -b feat && echo x > f && git add f && git commit -q -m feat`)
	if got := citMustSucceed(t, bin, name, dir, `git fetch -q origin && git rev-parse origin/main`); got != hostMain {
		t.Errorf("origin/main in the box = %q, want the host's new main %q", got, hostMain)
	}
	citMustSucceed(t, bin, name, dir, `mv docs docs2 && mv docs2 docs`) // moves the mountpoint inside the clone only
	citMustSucceed(t, bin, name, dir, `ls docs/sp/spec.md`)             // the share came back with it
	citMustSucceed(t, bin, name, dir, `mkdir -p docs/handover/box && echo y > docs/handover/box/n.md`)
	if got := citMustSucceed(t, bin, name, dir, `git status --porcelain`); got != "" {
		t.Errorf("git status --porcelain lists %q; docs/handover/ is gitignored", got)
	}

	// What the box must not be able to do.
	citMustFail(t, bin, name, dir, `git push origin feat`, "unable to create temporary object directory", "Read-only file system")
	citMustFail(t, bin, name, dir, `touch `+of+`/.git/x`)
	citMustFail(t, bin, name, dir, `mv `+of+`/.git /tmp/g`)
	citMustFail(t, bin, name, dir, `echo z >> CLAUDE.md`)
	citMustFail(t, bin, name, dir, `rm CLAUDE.md`)
	citMustFail(t, bin, name, dir, `echo y > docs/sp/box.md`)
	citMustFail(t, bin, name, dir, `mv docs/sp ds`)

	// The host's view afterwards.
	refsAfter := citRefs(t, of)
	if _, ok := refsAfter["refs/heads/feat"]; ok {
		t.Errorf("the box's branch feat reached the host repository: %v", refsAfter)
	}
	if len(refsAfter) != len(refsBefore) {
		t.Errorf("refs of clone_of changed: before %v, after %v", refsBefore, refsAfter)
	}
	for ref, sha := range refsBefore {
		if refsAfter[ref] != sha {
			t.Errorf("ref %s of clone_of changed: %s -> %s", ref, sha, refsAfter[ref])
		}
	}
	if got := citMustGit(t, of, "rev-parse", "main"); got != hostMain {
		t.Errorf("main of clone_of = %s, want %s", got, hostMain)
	}
	if after := citSnapshot(t, of, "CLAUDE.md", "docs/sp"); after != claudeBefore {
		t.Errorf("clone_of's CLAUDE.md or docs/sp changed:\nbefore:\n%s\nafter:\n%s", claudeBefore, after)
	}
	if after := citSnapshot(t, of, ".git"); after != gitBefore {
		t.Errorf("clone_of's .git changed:\nbefore:\n%s\nafter:\n%s", gitBefore, after)
	}
	if _, err := os.Lstat(filepath.Join(of, ".git", "x")); !os.IsNotExist(err) {
		t.Errorf("%s exists on the host (err=%v)", filepath.Join(of, ".git", "x"), err)
	}
}

// TestCloneFetchAttacksInARealContainer: with a hostile clone .git, Fetch (the
// bundle made in a real network-less container) imports nothing from other
// repositories and runs no planted hook or command.
func TestCloneFetchAttacksInARealContainer(t *testing.T) {
	bin, image := citDocker(t)
	citRemoveContainers(bin)
	t.Cleanup(func() { citRemoveContainers(bin) })

	g, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	d := ExecDocker{Bin: bin}

	// The secret repository S: another project the host owns. Its commit must
	// never reach clone_of. Branch feat too, so a host fetch that follows a
	// planted pointer into S finds the ref it asks for.
	secret := filepath.Join(citRealTemp(t), "S")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	citMustGit(t, secret, "init", "-q")
	citWrite(t, filepath.Join(secret, "secret.txt"), "the private repo's content\n")
	citMustGit(t, secret, "add", "-A")
	citMustGit(t, secret, "commit", "-q", "-m", "secret")
	citMustGit(t, secret, "branch", "feat")
	secretSHA := citMustGit(t, secret, "rev-parse", "HEAD")

	// Every hook name git knows, as a script that touches MARK.
	hookNames := []string{"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
		"prepare-commit-msg", "commit-msg", "post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push",
		"pre-receive", "update", "proc-receive", "post-receive", "post-update", "reference-transaction",
		"push-to-checkout", "pre-auto-gc", "post-rewrite", "sendemail-validate", "fsmonitor-watchman", "p4-changelist"}

	type plant func(t *testing.T, cloneDir, mark string)
	plants := []struct {
		name  string
		plant plant
	}{
		{"config", func(t *testing.T, dir, mark string) {
			hooks := filepath.Join(citRealTemp(t), "hooks")
			for _, h := range hookNames {
				p := filepath.Join(hooks, h)
				citWrite(t, p, "#!/bin/sh\ntouch "+mark+"\n")
				if err := os.Chmod(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := "touch " + mark
			f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString("[core]\n\tfsmonitor = " + cmd + "\n\thooksPath = " + hooks +
				"\n\tpager = " + cmd + "\n\tsshCommand = " + cmd + "\n\teditor = " + cmd +
				"\n[uploadpack]\n\tpackObjectsHook = " + cmd + "\n[credential]\n\thelper = !" + cmd + "\n"); err != nil {
				t.Fatal(err)
			}
		}},
		{"commondir", func(t *testing.T, dir, mark string) {
			citWrite(t, filepath.Join(dir, ".git", "commondir"), filepath.Join(secret, ".git")+"\n")
		}},
		{"alternates", func(t *testing.T, dir, mark string) {
			citWrite(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), filepath.Join(secret, ".git", "objects")+"\n")
			ref := filepath.Join(dir, ".git", "refs", "heads", "feat")
			os.Remove(ref)
			if err := os.Symlink(filepath.Join(secret, ".git", "refs", "heads", "main"), ref); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(dir, ".git", "packed-refs"))
		}},
		{"gitSymlink", func(t *testing.T, dir, mark string) {
			if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(secret, ".git"), filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"gitdirFile", func(t *testing.T, dir, mark string) {
			if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
				t.Fatal(err)
			}
			citWrite(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(secret, ".git")+"\n")
		}},
		{"control", func(t *testing.T, dir, mark string) {}},
	}

	for _, p := range plants {
		t.Run(p.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			of := citOf(t)
			dir := filepath.Join(citRealTemp(t), "clone")
			home := citRealTemp(t)
			mark := filepath.Join(citRealTemp(t), "MARK")
			c := config.Clone{Of: of, Dir: dir}

			if _, err := EnsureClone(ctx, g, c, home); err != nil {
				t.Fatalf("EnsureClone: %v", err)
			}
			// The box's work: one commit on feat, made before the plant.
			citMustGit(t, dir, "checkout", "-q", "-b", "feat")
			citWrite(t, filepath.Join(dir, "feat.txt"), "feat\n")
			citMustGit(t, dir, "add", "-A")
			citMustGit(t, dir, "commit", "-q", "-m", "feat")
			featSHA := citMustGit(t, dir, "rev-parse", "HEAD")
			p.plant(t, dir, mark)

			res, ferr := Fetch(ctx, g, d, FetchRequest{
				Clone:    c,
				Branch:   "feat",
				Base:     "main",
				Image:    image,
				Instance: citInstance,
				TempDir:  filepath.Join(citRealTemp(t), "tmp"),
				Home:     home,
			})
			t.Logf("Fetch: res=%+v err=%v", res, ferr)

			if _, err := os.Lstat(mark); !os.IsNotExist(err) {
				t.Errorf("a planted command or hook ran on the host: %s exists (err=%v)", mark, err)
			}
			if out, err := citGit(t, of, "cat-file", "-e", secretSHA); err == nil {
				t.Errorf("clone_of holds the secret repository's commit %s", secretSHA)
			} else {
				_ = out
			}
			if ferr == nil {
				got := citMustGit(t, of, "rev-list", "refs/heads/box/feat", "^main")
				for _, sha := range strings.Fields(got) {
					if sha != featSHA {
						t.Errorf("box/feat holds %s, which is not the box's own commit %s", sha, featSHA)
					}
				}
			}
			if p.name == "control" {
				if ferr != nil {
					t.Fatalf("the control fetch failed: %v", ferr)
				}
				if got := citMustGit(t, of, "rev-list", "refs/heads/box/feat", "^main"); got != featSHA {
					t.Errorf("control: box/feat adds %q over main, want exactly %s", got, featSHA)
				}
				if len(res.Commits) != 1 || !strings.HasSuffix(res.Commits[0], " feat") {
					t.Errorf("control: Commits = %q, want the one feat commit", res.Commits)
				}
			}
		})
	}

	if left := citLeftovers(t, bin); len(left) != 0 {
		// Cleanup will remove them; a bundle container uses --rm, so any left is a leak.
		t.Errorf("containers left behind by the fetches: %v", left)
	}
}
