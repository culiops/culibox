package session

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/launcher"
)

type mockBuilder struct {
	argv []string
	env  []string
	err  error
}

func (b mockBuilder) Build(launcher.SpawnSpec) ([]string, []string, error) {
	env := b.env
	if env == nil { // give the child a PATH so `sh`/`sleep` work in tests
		env = []string{"PATH=" + os.Getenv("PATH")}
	}
	return b.argv, env, b.err
}

func TestManager_SpawnListGetKill(t *testing.T) {
	m := NewManager(mockBuilder{argv: []string{"sh", "-c", "sleep 30"}}, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "sh-1" {
		t.Fatalf("id=%q, want sh-1", s.ID)
	}
	if list := m.List(); len(list) != 1 || list[0].ID != "sh-1" {
		t.Fatalf("list=%+v", list)
	}
	if _, ok := m.Get("sh-1"); !ok {
		t.Fatal("Get should find sh-1")
	}
	if err := m.Kill("sh-1"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if _, ok := m.Get("sh-1"); ok {
		t.Fatal("session should be gone after kill")
	}
}

// TestManager_KillTerminatesProcessGroup proves the negative-pid kill reaches a
// backgrounded grandchild — i.e. claude's children won't orphan.
func TestManager_KillTerminatesProcessGroup(t *testing.T) {
	m := NewManager(mockBuilder{argv: []string{"sh", "-c", "sleep 30 & echo $!; wait"}}, nil, 1<<16, time.Minute)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	out, cancel, _ := s.Subscribe()
	defer cancel()

	// Read the backgrounded PID; the tty CRLF-translates and may split reads.
	var b strings.Builder
	pid := 0
	deadline := time.After(3 * time.Second)
readloop:
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				break readloop
			}
			b.WriteString(strings.ReplaceAll(string(chunk), "\r", ""))
			if i := strings.IndexByte(b.String(), '\n'); i >= 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(b.String()[:i])); err == nil {
					pid = n
					break readloop
				}
			}
		case <-deadline:
			t.Fatalf("did not read backgrounded PID; got %q", b.String())
		}
	}
	if pid == 0 {
		t.Fatalf("no PID parsed from %q", b.String())
	}

	if err := m.Kill("sh-1"); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Teardown after SIGKILL is async — poll until the grandchild is gone.
	gone := false
	for i := 0; i < 40; i++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		t.Fatalf("backgrounded process %d survived Kill — orphaned (process-group kill failed)", pid)
	}
}
