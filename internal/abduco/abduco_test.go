package abduco

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	if err := a.Create(id, []string{"sh", "-c", "sleep 300"}, os.Environ()); err != nil {
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
	if err := a.Create(id, []string{"sh", "-c", script}, os.Environ()); err != nil {
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
