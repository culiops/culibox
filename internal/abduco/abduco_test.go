package abduco

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testAdapter(t *testing.T) *Adapter {
	t.Helper()
	if _, err := exec.LookPath("abduco"); err != nil {
		t.Skip("abduco not installed")
	}
	dir := t.TempDir()
	a, err := New("abduco", filepath.Join(dir, "sock"), filepath.Join(dir, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCreateListAttachKill(t *testing.T) {
	a := testAdapter(t)
	id := "bash-1"
	if err := a.Create(id, "", []string{"sh", "-c", "sleep 300"}, os.Environ()); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = a.Kill(id) })

	if !a.Alive(id) {
		t.Fatal("session not alive after create")
	}
	if !a.InnerAlive(id) {
		t.Fatal("inner not alive after create")
	}
	ls, _ := a.List()
	if len(ls) != 1 || ls[0] != id {
		t.Fatalf("List = %v, want [%s]", ls, id)
	}

	_, f, err := a.Attach(id, 24, 80)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	_ = f.Close()

	if err := a.Kill(id); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// converge: socket gone, no orphan
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && a.Alive(id) {
		time.Sleep(50 * time.Millisecond)
	}
	if a.Alive(id) {
		t.Fatal("session still alive after kill")
	}
	if a.InnerAlive(id) {
		t.Fatal("inner still alive after kill (orphan)")
	}
}

func TestKillNoOrphanGrandchild(t *testing.T) {
	a := testAdapter(t)
	id := "bash-1"
	// inner backgrounds a grandchild; kill of the inner GROUP must reap it too.
	marker := filepath.Join(t.TempDir(), "gc.pid")
	script := "sleep 300 & echo $! > " + marker + "; wait"
	if err := a.Create(id, "", []string{"sh", "-c", script}, os.Environ()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Kill(id) })
	time.Sleep(300 * time.Millisecond)
	gcb, _ := os.ReadFile(marker)
	gc := strings.TrimSpace(string(gcb))
	if gc == "" {
		t.Fatal("grandchild pid not recorded")
	}
	if err := a.Kill(id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := exec.Command("kill", "-0", gc).Run(); err == nil {
		t.Fatalf("grandchild %s still alive — orphan", gc)
	}
}

func TestKillMissingPidfileIdempotent(t *testing.T) {
	a := testAdapter(t)
	if err := a.Kill("nope-1"); err != ErrNotFound {
		t.Fatalf("Kill of unknown = %v, want ErrNotFound", err)
	}
}

func TestGuardRefusesForeignPid(t *testing.T) {
	a := testAdapter(t)
	// A real process we own, recorded with a bogus srv so the guard (inner's
	// ppid must equal srv AND srv must be our abduco server) refuses to kill it.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	if err := os.MkdirAll(a.PidDir, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte(fmt.Sprintf("inner=%d srv=999999", pid))
	if err := os.WriteFile(filepath.Join(a.PidDir, "x-1.pid"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if a.InnerAlive("x-1") {
		t.Fatal("InnerAlive true for foreign pid — guard failed")
	}
	_ = a.Kill("x-1")
	if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
		t.Fatal("guard let us kill a foreign process we do not own")
	}
}

// TestKillRefusesWhenServerNotOurs guards B1: even when the recorded inner's
// ppid matches the recorded srv, if srv is NOT our abduco server for this id the
// whole teardown must be refused (pids may have been reused).
func TestKillRefusesWhenServerNotOurs(t *testing.T) {
	a := testAdapter(t)
	// A real process that IS its own group leader (as abduco's setsid makes the
	// inner), whose parent is this test process. We record srv = the test process
	// pid — a real, live pid whose cmdline is NOT our abduco server for "x-1".
	// A broken guard (ppid==srv without srvIsOurs) would `kill -inner` and reap it;
	// the correct guard refuses because srv isn't ours.
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // inner leads its own group
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	inner := child.Process.Pid
	defer func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() }()
	srv := os.Getpid() // ppid of the sleep == this test process; not an abduco server

	if err := os.MkdirAll(a.PidDir, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte(fmt.Sprintf("inner=%d srv=%d", inner, srv))
	if err := os.WriteFile(filepath.Join(a.PidDir, "x-1.pid"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if a.InnerAlive("x-1") {
		t.Fatal("InnerAlive true though srv is not our abduco server")
	}
	_ = a.Kill("x-1")
	if err := exec.Command("kill", "-0", strconv.Itoa(inner)).Run(); err != nil {
		t.Fatal("Kill terminated a process group whose server is not ours (B1)")
	}
}

func TestListEmptyForeignDir(t *testing.T) {
	a := testAdapter(t)
	ls, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 0 {
		t.Fatalf("List on fresh dir = %v, want empty", ls)
	}
}

// writeProc fakes one /proc/<pid> entry.
func writeProc(t *testing.T, root, pid string, argv, env []string) string {
	t.Helper()
	d := filepath.Join(root, pid)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	nulJoin := func(ss []string) []byte {
		var b []byte
		for _, s := range ss {
			b = append(b, []byte(s)...)
			b = append(b, 0)
		}
		return b
	}
	if err := os.WriteFile(filepath.Join(d, "cmdline"), nulJoin(argv), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "environ"), nulJoin(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

// The sweep kills processes, so misclassifying one is worse than missing one.
// The two failure modes that matter: killing an attach client belonging to a
// DIFFERENT console instance (session ids like "zsh-1" collide across
// instances), and killing an abduco SERVER, which is the user's actual agent.
func TestProcIsAttacher(t *testing.T) {
	root := t.TempDir()
	ours := "/home/u/.local/share/agent-box/abduco"
	theirs := "/tmp/other-instance/abduco"

	cases := []struct {
		name string
		argv []string
		env  []string
		want bool
	}{
		{"our attach client", []string{"abduco", "-e", "^\\", "-a", "zsh-1"},
			[]string{"PATH=/usr/bin", "ABDUCO_SOCKET_DIR=" + ours}, true},
		{"attach client of another instance, same session name",
			[]string{"abduco", "-e", "^\\", "-a", "zsh-1"},
			[]string{"ABDUCO_SOCKET_DIR=" + theirs}, false},
		{"our SERVER — this is the user's agent, never kill it",
			[]string{"abduco", "-n", "zsh-1", "/usr/bin/zsh"},
			[]string{"ABDUCO_SOCKET_DIR=" + ours}, false},
		{"absolute path to the abduco binary still counts",
			[]string{"/usr/local/bin/abduco", "-e", "^\\", "-a", "claude-2"},
			[]string{"ABDUCO_SOCKET_DIR=" + ours}, true},
		{"not abduco at all", []string{"grep", "-a", "zsh-1"},
			[]string{"ABDUCO_SOCKET_DIR=" + ours}, false},
		{"no socket dir in env", []string{"abduco", "-e", "^\\", "-a", "zsh-1"},
			[]string{"PATH=/usr/bin"}, false},
		{"socket dir is a prefix of ours, not ours",
			[]string{"abduco", "-e", "^\\", "-a", "zsh-1"},
			[]string{"ABDUCO_SOCKET_DIR=" + ours + "-2"}, false},
	}
	for _, c := range cases {
		d := writeProc(t, root, strings.ReplaceAll(c.name, " ", "_"), c.argv, c.env)
		if got := procIsAttacher(d, ours); got != c.want {
			t.Errorf("%s: procIsAttacher = %v, want %v", c.name, got, c.want)
		}
	}

	if procIsAttacher(filepath.Join(root, "does-not-exist"), ours) {
		t.Error("missing proc entry must not be classified as an attacher")
	}
}
