package session

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/abduco"
)

// TestReconcile_RealAbduco is the restart-survival proof against real abduco: a
// session that already exists in abduco + sidecar (as if it outlived a daemon
// restart) is re-attached by a fresh Manager and is still interactive.
func TestReconcile_RealAbduco(t *testing.T) {
	if _, err := exec.LookPath("abduco"); err != nil {
		// CI sets AGENT_BOX_REQUIRE_ABDUCO=1: a missing abduco must fail there, not skip.
		if os.Getenv("AGENT_BOX_REQUIRE_ABDUCO") == "1" {
			t.Fatal("abduco not installed and AGENT_BOX_REQUIRE_ABDUCO=1")
		}
		t.Skip("abduco not installed")
	}
	// Short /tmp path: t.TempDir() is ~150 bytes on macOS, which would put every
	// session socket over the 103-byte limit Create enforces.
	dir := shortTempDir(t)
	ad, err := abduco.New("abduco", dir, filepath.Join(dir, "pids"))
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

// shortTempDir (the same helper as in internal/abduco's tests) makes /tmp/<5 hex digits> (mode 0700, 10 bytes) and removes it when
// the test ends. abduco names its socket <dir>/abduco/<user>/<id>@<hostname> and
// darwin's sun_path holds 103 bytes: a GitHub-hosted Mac has a 67-byte hostname, so
// t.TempDir() (~150 bytes) and even os.MkdirTemp("/tmp", "ab-") (22 bytes) put the
// path over the limit before the behaviour under test runs. Use it for any
// directory a socket path is built from.
func shortTempDir(t *testing.T) string {
	t.Helper()
	for range 64 {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		dir := "/tmp/" + hex.EncodeToString(b[:])[:5]
		err := os.Mkdir(dir, 0o700)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	}
	t.Fatal("no free /tmp name after 64 tries")
	return ""
}
