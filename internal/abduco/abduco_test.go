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
	// Short /tmp path: t.TempDir() is ~150 bytes on macOS, which would put every
	// session socket over the 103-byte limit Create enforces.
	dir, err := os.MkdirTemp("/tmp", "ab-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
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

// The sweep kills processes, so misclassifying one is worse than missing one.
// The two failure modes that matter: killing an attach client belonging to a
// DIFFERENT console instance (session ids like "zsh-1" collide across
// instances), and killing an abduco SERVER, which is the user's actual agent.
func TestIsAttacher(t *testing.T) {
	ours := "/tmp/ours"
	env := []string{"PATH=/bin", "ABDUCO_SOCKET_DIR=" + ours}
	cases := []struct {
		name string
		argv []string
		env  []string
		want bool
	}{
		{"attach client for our dir", []string{"abduco", "-e", "^^", "-a", "zsh-1"}, env, true},
		{"absolute abduco path", []string{"/opt/homebrew/bin/abduco", "-a", "zsh-1"}, env, true},
		{"-A also attaches", []string{"abduco", "-A", "zsh-1"}, env, true},
		{"server -n is a session, never a client", []string{"abduco", "-n", "zsh-1", "sh"}, env, false},
		{"server -c", []string{"abduco", "-c", "zsh-1"}, env, false},
		{"other socket dir", []string{"abduco", "-a", "zsh-1"}, []string{"ABDUCO_SOCKET_DIR=/tmp/theirs"}, false},
		{"prefix of our dir is not ours", []string{"abduco", "-a", "zsh-1"}, []string{"ABDUCO_SOCKET_DIR=" + ours + "x"}, false},
		{"not abduco", []string{"bash", "-a"}, env, false},
		{"no args", nil, env, false},
		{"env unreadable", []string{"abduco", "-a", "zsh-1"}, nil, false},
	}
	for _, c := range cases {
		if got := isAttacher(c.argv, c.env, ours); got != c.want {
			t.Errorf("%s: isAttacher = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInnerPid(t *testing.T) {
	dir := t.TempDir()
	a := &Adapter{PidDir: dir}
	if err := os.WriteFile(filepath.Join(dir, "claude-1.pid"), []byte("inner=4242 srv=4241"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, ok := a.InnerPid("claude-1"); !ok || pid != 4242 {
		t.Fatalf("InnerPid=%d,%v want 4242,true", pid, ok)
	}
	if _, ok := a.InnerPid("missing"); ok {
		t.Error("InnerPid ok=true for a missing pidfile")
	}
	if err := os.WriteFile(filepath.Join(dir, "half.pid"), []byte("inner=7"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.InnerPid("half"); ok {
		t.Error("InnerPid accepted an incomplete pidfile")
	}
}
