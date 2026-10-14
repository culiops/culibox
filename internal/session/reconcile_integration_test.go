package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/abduco"
)

// TestReconcile_RealAbduco is the restart-survival proof against real abduco: a
// session that already exists in abduco + sidecar (as if it outlived a daemon
// restart) is re-attached by a fresh Manager and is still interactive.
func TestReconcile_RealAbduco(t *testing.T) {
	if _, err := exec.LookPath("abduco"); err != nil {
		t.Skip("abduco not installed")
	}
	// Short /tmp path: t.TempDir() is ~150 bytes on macOS, which would put every
	// session socket over the 103-byte limit Create enforces.
	dir, err := os.MkdirTemp("/tmp", "rc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ad, err := abduco.New("abduco", filepath.Join(dir, "sock"), filepath.Join(dir, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	// A surviving abduco session running `cat` (echoes stdin → lets us prove the
	// re-attached stream is bidirectional).
	if err := ad.Create("bash-1", "", []string{"cat"}, os.Environ()); err != nil {
		t.Fatal(err)
	}
	scPath := filepath.Join(dir, "sessions.json")
	sc, err := openSidecar(scPath)
	if err != nil {
		t.Fatal(err)
	}
	sc.put("bash-1", sessionMeta{Agent: "bash", Created: time.Now()})

	// Fresh daemon: a new Manager reconciles the surviving session.
	m := NewManager(fakeLauncher{}, ad, 1<<16, time.Minute)
	if err := m.SetSidecarPath(scPath); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("bash-1") })

	s, ok := m.Get("bash-1")
	if !ok {
		t.Fatal("surviving session was not re-attached after reconcile")
	}
	if s.Info().Status != statusLive {
		t.Fatalf("status=%q, want live", s.Info().Status)
	}

	out, cancel, _ := s.Subscribe()
	defer cancel()
	if _, err := s.Write([]byte("PING_RECON\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUntil(out, "PING_RECON", 3*time.Second); !ok {
		t.Fatal("re-attached session is not interactive (no echo through abduco)")
	}
}
