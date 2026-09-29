package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// pipeTo points s's PTY at a pipe and returns its read end.
func pipeTo(t *testing.T, s *Session) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	s.ptmx = w
	return r
}

// written drains what has been written so far.
func written(t *testing.T, r *os.File) string {
	t.Helper()
	var out []byte
	buf := make([]byte, 64)
	for {
		_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return string(out)
		}
	}
}

func hexSHA(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func TestInterruptPicksTheKeyServerSide(t *testing.T) {
	for _, c := range []struct{ bin, want string }{
		{"/usr/bin/claude", "\x1b"}, {"codex", "\x1b"}, {"/bin/bash", "\x03"}, {"", "\x03"},
	} {
		s := &Session{ID: "a", Bin: c.bin, status: statusLive}
		r := pipeTo(t, s)
		m := &Manager{sessions: map[string]*Session{"a": s}}
		if err := m.Interrupt("a"); err != nil {
			t.Fatalf("%s: %v", c.bin, err)
		}
		if got := written(t, r); got != c.want {
			t.Errorf("%s: wrote %q want %q", c.bin, got, c.want)
		}
		if submittedOf(s) {
			t.Errorf("%s: an interrupt marked the session submitted", c.bin)
		}
	}
}

func TestInterruptRefusesWhatIsNotRunning(t *testing.T) {
	for _, st := range []string{statusExited, statusDetached} {
		s := &Session{ID: "a", Bin: "/usr/bin/claude", status: st}
		r := pipeTo(t, s)
		m := &Manager{sessions: map[string]*Session{"a": s}}
		if err := m.Interrupt("a"); !errors.Is(err, ErrNotLive) {
			t.Errorf("%s: err=%v want ErrNotLive", st, err)
		}
		if got := written(t, r); got != "" {
			t.Errorf("%s: wrote %q", st, got)
		}
	}
	if err := (&Manager{sessions: map[string]*Session{}}).Interrupt("nope"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown: err=%v want ErrNoSession", err)
	}
}

const s1Ask = "Bash(touch /tmp/a)"

// answerManager: s1 is a live, replyable claude sitting on one Bash prompt,
// registry "waiting". Returns the manager, the PTY reader and the home dir.
func answerManager(t *testing.T) (*Manager, *os.File, string) {
	t.Helper()
	m, home := newLocatorManager(t)
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(), txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("touch /tmp/a")))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	s.status = statusLive
	return m, pipeTo(t, s), home
}

func TestAnswerWritesExactlyOneKey(t *testing.T) {
	for _, c := range []struct{ choice, want string }{{"yes", "1"}, {"no", "\x1b"}} {
		m, r, _ := answerManager(t)
		if err := m.Answer("s1", c.choice, hexSHA(s1Ask)); err != nil {
			t.Fatalf("%s: %v", c.choice, err)
		}
		if got := written(t, r); got != c.want {
			t.Errorf("%s: wrote %q want exactly %q (never a \\r)", c.choice, got, c.want)
		}
	}
}

func TestAnswerGates(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, m *Manager, home string)
		choice string
		sha    string
		want   error
	}{
		{"bad choice", nil, "always", hexSHA(s1Ask), ErrBadChoice},
		{"not claude", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].Bin = "/bin/bash" }, "yes", hexSHA(s1Ask), ErrNotClaude},
		{"not replyable", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].replyable = false }, "yes", hexSHA(s1Ask), ErrNotReplyable},
		{"registry not blocked", func(t *testing.T, _ *Manager, home string) {
			writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "idle"))
		}, "yes", hexSHA(s1Ask), ErrNotBlocked},
		{"exited", func(t *testing.T, m *Manager, _ string) { m.sessions["s1"].status = statusExited }, "no", hexSHA(s1Ask), ErrNotBlocked},
		{"two calls pending", func(t *testing.T, _ *Manager, home string) {
			// Real parallel tool calls share ONE message.id across separate
			// records (probe E: one assistant record per tool_use, same
			// message.id) — txRecID, not plain txRec's fresh id each call:
			// the Messages API requires every tool_use resolved before the
			// next assistant message, so two DIFFERENT message.ids each
			// left unresolved isn't a transcript shape that can occur.
			writeTxLines(t, home, locCwd, "aaaa-1", time.Now().Add(2*time.Second), txUser("go"),
				txRecID("assistant", "msg_parallel", txBash("touch /tmp/e1")), txRecID("assistant", "msg_parallel", txBash("touch /tmp/e2")))
		}, "yes", hexSHA("Bash(touch /tmp/e2)"), ErrPromptChanged},
		{"hash mismatch", nil, "yes", hexSHA("Bash(touch /tmp/other)"), ErrPromptChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, r, home := answerManager(t)
			if c.setup != nil {
				c.setup(t, m, home)
			}
			if err := m.Answer("s1", c.choice, c.sha); !errors.Is(err, c.want) {
				t.Fatalf("err=%v want %v", err, c.want)
			}
			if got := written(t, r); got != "" {
				t.Fatalf("a refused answer still wrote %q", got)
			}
		})
	}
	m, _, _ := answerManager(t)
	if err := m.Answer("nope", "yes", hexSHA(s1Ask)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown: err=%v want ErrNoSession", err)
	}
}

// The web client hashes with a pure-JS SHA-256 (attention.js sha256Hex); this
// is the same vector its test pins, so the two sides cannot drift.
func TestAnswerHashMatchesTheWebClientVector(t *testing.T) {
	const ask = "Bash(rm -rf build) — é ü 漢 😀"
	const vec = "b8c0c145bfbc2ff37428ec2d15dc9ac3c6bf196fa6e8340756f91ec23d84166a"
	if !askHashMatches(ask, vec) || !askHashMatches(ask, strings.ToUpper(vec)) {
		t.Fatal("the shared vector does not match")
	}
	if askHashMatches(ask, vec[:63]+"0") || askHashMatches(ask, "") {
		t.Fatal("a wrong hash matched")
	}
}
