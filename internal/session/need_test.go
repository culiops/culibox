package session

import (
	"os"
	"testing"
	"time"
)

func TestNeedOf(t *testing.T) {
	ask := Peek{Pending: true, Ask: "Bash(ls)"}
	cases := []struct {
		name                       string
		act                        string
		claude                     bool
		p                          Peek
		have, blocked              bool
		wantAct, wantNeed, wantAsk string
	}{
		{"finished turn", "waiting", true, Peek{Text: "done"}, true, false, "waiting", "turn", ""},
		{"pending tool, silent", "waiting", true, ask, true, false, "waiting", "ask", "Bash(ls)"},
		{"registry blocked raises a working session", "working", true, ask, true, true, "waiting", "ask", "Bash(ls)"},
		{"registry blocked raises an idle session", "idle", true, Peek{}, true, true, "waiting", "ask", ""},
		{"registry blocked never raises an exited session", "done", true, ask, true, true, "done", "", ""},
		{"registry blocked never raises a detached session", "detached", true, ask, true, true, "detached", "", ""},
		{"working without the registry stays working", "working", true, ask, true, false, "working", "", ""},
		{"unreadable transcript: still amber, no need", "waiting", true, Peek{}, false, false, "waiting", "", ""},
		{"blocked with no transcript: ask without text", "working", true, Peek{}, false, true, "waiting", "ask", ""},
		{"codex waiting: amber, no need", "waiting", false, Peek{}, false, false, "waiting", "", ""},
		{"non-claude ignores a blocked flag", "working", false, Peek{}, false, true, "working", "", ""},
		{"idle claude, no registry: stays idle", "idle", true, Peek{Text: "hi"}, true, false, "idle", "", ""},
	}
	for _, c := range cases {
		a, n, k := needOf(c.act, c.claude, c.p, c.have, c.blocked)
		if a != c.wantAct || n != c.wantNeed || k != c.wantAsk {
			t.Errorf("%s: got (%q,%q,%q) want (%q,%q,%q)", c.name, a, n, k, c.wantAct, c.wantNeed, c.wantAsk)
		}
	}
}

func TestListCarriesNeedAndAsk(t *testing.T) {
	pending := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"rm -rf build"}}]}}` + "\n"
	m, _ := newPeekTestManager(t, "/tmp/proj", pending)
	got := m.List()[0]
	if got.Activity != "waiting" || got.Need != "ask" || got.Ask != "Bash(rm -rf build)" {
		t.Fatalf("got activity=%q need=%q ask=%q", got.Activity, got.Need, got.Ask)
	}

	done := `{"message":{"role":"assistant","content":[{"type":"text","text":"all green"}]}}` + "\n"
	m, _ = newPeekTestManager(t, "/tmp/proj", done)
	if got := m.List()[0]; got.Need != "turn" || got.Ask != "" {
		t.Fatalf("finished turn: need=%q ask=%q, want turn and empty", got.Need, got.Ask)
	}

	m, _ = newPeekTestManager(t, "/tmp/proj", done)
	m.sessions["s1"].Bin = "/bin/zsh"
	m.sessions["s1"].agentKind = false
	if got := m.List()[0]; got.Activity != "idle" || got.Need != "" {
		t.Fatalf("shell: activity=%q need=%q, want idle and no need", got.Activity, got.Need)
	}
}

func TestListRaisesARegistryBlockedSession(t *testing.T) {
	m, home := newLocatorManager(t)
	writeTx(t, home, locCwd, "aaaa-1", "unused", time.Now().Add(-10*time.Minute))
	// Overwrite A with a pending tool call, keeping its name.
	pending := `{"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"touch /tmp/x"}}]}}` + "\n"
	if err := os.WriteFile(home+"/.claude/projects/"+projectSlug(locCwd)+"/aaaa-1.jsonl", []byte(pending), 0o600); err != nil {
		t.Fatal(err)
	}
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	m.sessions["s1"].lastActivityAt = time.Now() // the prompt animates: PTY says "working"
	var s1 Info
	for _, i := range m.List() {
		if i.ID == "s1" {
			s1 = i
		}
	}
	if s1.Activity != "waiting" || s1.Need != "ask" || s1.Ask != "Bash(touch /tmp/x)" {
		t.Fatalf("got activity=%q need=%q ask=%q, want waiting/ask/Bash(touch /tmp/x)", s1.Activity, s1.Need, s1.Ask)
	}
}

// The registry says blocked but the transcript's last record is text (it lags,
// or a future format hides the tool_use). A reply must still be refused: the
// dialog reads single keys and "yes\r" would answer it.
func TestSendMessageRefusesARegistryBlockedSession(t *testing.T) {
	m, home := newLocatorManager(t)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	s := m.sessions["s1"]
	s.replyable = true
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	s.ptmx = devnull
	if err := m.SendMessage("s1", "yes"); err != ErrPending {
		t.Errorf("err=%v want ErrPending", err)
	}
}
