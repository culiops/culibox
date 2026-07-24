package session

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// startProc starts a real process on a PTY and wraps it in a Session (bypassing
// Manager, so Session logic can be tested directly). Hermetic: coreutils only.
func startProc(t *testing.T, ringCap int, name string, args ...string) *Session {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	return newSession("test-1", "test", "", cmd, ptmx, ringCap, nil)
}

func readUntil(out <-chan []byte, want string, d time.Duration) (string, bool) {
	var b strings.Builder
	deadline := time.After(d)
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				return b.String(), strings.Contains(b.String(), want)
			}
			b.Write(chunk)
			if strings.Contains(b.String(), want) {
				return b.String(), true
			}
		case <-deadline:
			return b.String(), false
		}
	}
}

func TestSession_OutputReachesSubscriber(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf HELLO; sleep 2")
	out, cancel := s.Subscribe()
	defer cancel()
	if _, ok := readUntil(out, "HELLO", 3*time.Second); !ok {
		t.Fatal("did not receive HELLO")
	}
}

func TestSession_ReplayOnResubscribe(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf HELLO; sleep 3")
	out1, cancel1 := s.Subscribe()
	if _, ok := readUntil(out1, "HELLO", 3*time.Second); !ok {
		t.Fatal("first subscribe missed HELLO")
	}
	cancel1()
	// A fresh subscriber must replay the ring (HELLO is still buffered).
	out2, cancel2 := s.Subscribe()
	defer cancel2()
	if _, ok := readUntil(out2, "HELLO", 2*time.Second); !ok {
		t.Fatal("resubscribe did not replay HELLO")
	}
}

func TestSession_SecondSubscribeCancelsFirst(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	out1, _ := s.Subscribe()
	out2, cancel2 := s.Subscribe()
	defer cancel2()
	// out1 must close (its range ends) without panic.
	select {
	case _, ok := <-out1:
		if ok {
			// may deliver a final chunk before closing; drain to close
			for range out1 {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first subscription channel was not closed after a second Subscribe")
	}
	_ = out2
}

func TestSession_ExitClosesOutAndDone(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf BYE")
	out, cancel := s.Subscribe()
	defer cancel()
	got, ok := readUntil(out, "BYE", 3*time.Second)
	if !ok {
		t.Fatalf("missed BYE, got %q", got)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done not closed after child exit")
	}
	if !s.Exited() {
		t.Fatal("Exited() should be true")
	}
}

// TestSession_FinalOutputDeliveredBeforeClose guards the review's finding #2:
// a live subscriber must receive the child's final bytes before `out` closes
// (delivery terminates on pump-drained, not merely on process-reaped).
func TestSession_FinalOutputDeliveredBeforeClose(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf FINAL_MARKER; exit 0")
	out, cancel := s.Subscribe()
	defer cancel()
	var b strings.Builder
	deadline := time.After(3 * time.Second)
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				if !strings.Contains(b.String(), "FINAL_MARKER") {
					t.Fatalf("final output lost before close; got %q", b.String())
				}
				return
			}
			b.Write(chunk)
		case <-deadline:
			t.Fatalf("out never closed; got %q", b.String())
		}
	}
}

func TestSession_WriteReachesChild(t *testing.T) {
	s := startProc(t, 1<<16, "cat")
	out, cancel := s.Subscribe()
	defer cancel()
	if _, err := s.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUntil(out, "ping", 3*time.Second); !ok {
		t.Fatal("input did not echo back through the child")
	}
}
