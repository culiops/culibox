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
	return newSession("test-1", "test", "test-1", "", time.Now(), cmd, ptmx, ringCap, nil)
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
	out, cancel, _ := s.Subscribe()
	defer cancel()
	if _, ok := readUntil(out, "HELLO", 3*time.Second); !ok {
		t.Fatal("did not receive HELLO")
	}
}

func TestSession_ReplayOnResubscribe(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf HELLO; sleep 3")
	out1, cancel1, _ := s.Subscribe()
	if _, ok := readUntil(out1, "HELLO", 3*time.Second); !ok {
		t.Fatal("first subscribe missed HELLO")
	}
	cancel1()
	// A fresh subscriber must replay the ring (HELLO is still buffered).
	out2, cancel2, _ := s.Subscribe()
	defer cancel2()
	if _, ok := readUntil(out2, "HELLO", 2*time.Second); !ok {
		t.Fatal("resubscribe did not replay HELLO")
	}
}

func TestSession_SecondSubscribeCancelsFirst(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	out1, _, _ := s.Subscribe()
	out2, cancel2, _ := s.Subscribe()
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

// drain reads until the channel closes. Bounded on purpose: a regression that
// stops closing it should fail here in seconds with a readable message, not hang
// the package until the test binary's panic timeout.
func drain(t *testing.T, out <-chan []byte, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription channel never closed")
		}
	}
}

// The evicted subscriber must be able to say WHY its channel closed: a viewer
// that was taken over has to stop reconnecting, while one whose session ended or
// whose server died must keep its existing behaviour.
func TestSession_SupersededMarksOnlyTheEvictedSubscription(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	out1, _, superseded1 := s.Subscribe()
	if superseded1() {
		t.Fatal("a lone subscription reports itself superseded")
	}
	_, cancel2, superseded2 := s.Subscribe()
	defer cancel2()
	drain(t, out1, 2*time.Second)
	if !superseded1() {
		t.Fatal("the evicted subscription does not report superseded")
	}
	if superseded2() {
		t.Fatal("the live subscription reports superseded")
	}
}

// A → B → C: every viewer that was pushed out must know it, not just the last one.
func TestSession_SupersededSurvivesAChainOfSubscribers(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	outA, _, supA := s.Subscribe()
	outB, _, supB := s.Subscribe()
	_, cancelC, supC := s.Subscribe()
	defer cancelC()
	drain(t, outA, 2*time.Second)
	drain(t, outB, 2*time.Second)
	if !supA() || !supB() {
		t.Fatalf("evicted subscribers misreport: A=%v B=%v", supA(), supB())
	}
	if supC() {
		t.Fatal("the surviving subscriber reports superseded")
	}
}

func TestSession_CancelIsNotSuperseded(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	out, cancel, superseded := s.Subscribe()
	cancel()
	drain(t, out, 2*time.Second)
	if superseded() {
		t.Fatal("a subscription the client cancelled must not read as superseded")
	}
}

// A finished subscription must stay finished. It is no longer the session's
// current one, so a LATER Subscribe must not retro-mark it: a viewer whose
// session merely ended would be told "another tab took over" and — because that
// state deliberately stops reconnecting — would never learn the truth.
func TestSession_ExitIsNotSuperseded(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf BYE")
	out, cancel, superseded := s.Subscribe()
	defer cancel()
	drain(t, out, 3*time.Second) // the session exits, closing the channel
	if superseded() {
		t.Fatal("a subscription outliving its session must not read as superseded")
	}
	_, cancel2, _ := s.Subscribe() // someone opens the dead session elsewhere
	defer cancel2()
	if superseded() {
		t.Fatal("a later Subscribe retro-marked an already-finished subscription")
	}
}

func TestSession_CancelledIsNotRetroMarkedSuperseded(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "sleep 5")
	out, cancel, superseded := s.Subscribe()
	cancel()
	drain(t, out, 2*time.Second)
	_, cancel2, _ := s.Subscribe()
	defer cancel2()
	if superseded() {
		t.Fatal("a later Subscribe retro-marked a cancelled subscription")
	}
}

func TestSession_ExitClosesOutAndDone(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", "printf BYE")
	out, cancel, _ := s.Subscribe()
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
	out, cancel, _ := s.Subscribe()
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
	out, cancel, _ := s.Subscribe()
	defer cancel()
	if _, err := s.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUntil(out, "ping", 3*time.Second); !ok {
		t.Fatal("input did not echo back through the child")
	}
}

// An abduco-attached session must not hand the attach preamble to the browser:
// it would park xterm in the alternate buffer for the session's whole life.
func TestSession_StripsAbducoAttachPreamble(t *testing.T) {
	cmd := exec.Command("sh", "-c", `printf '\033[?1049h\033[HREADY'; sleep 2`)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	s := newSession("s-1", "test", "s-1", "", time.Now(), cmd, ptmx, 1<<16, newFakeBackend())

	out, cancel, _ := s.Subscribe()
	defer cancel()
	got, ok := readUntil(out, "READY", 3*time.Second)
	if !ok {
		t.Fatalf("did not receive READY, got %q", got)
	}
	if strings.Contains(got, attachPreamble) {
		t.Fatalf("attach preamble survived: %q", got)
	}
	if !strings.Contains(got, "\x1b[H") {
		t.Fatalf("cursor-home after the preamble was dropped too: %q", got)
	}
}

// A session whose PTY the daemon owns directly has no abduco in front of it, so
// a leading alternate-screen switch is the agent's own and must survive.
func TestSession_EphemeralKeepsLeadingAltScreen(t *testing.T) {
	s := startProc(t, 1<<16, "sh", "-c", `printf '\033[?1049hREADY'; sleep 2`)
	out, cancel, _ := s.Subscribe()
	defer cancel()
	got, ok := readUntil(out, "READY", 3*time.Second)
	if !ok {
		t.Fatalf("did not receive READY, got %q", got)
	}
	if !strings.Contains(got, attachPreamble) {
		t.Fatalf("ephemeral session lost its alt-screen switch: %q", got)
	}
}
