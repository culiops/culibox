package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/culiops/agent-box/internal/box"
)

// regBoxJSON is a registry entry as Claude Code writes it in the box. procStart
// is present but the host never checks it for a boxed session.
func regBoxJSON(pid int, sid, cwd, status string) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"procStart":"1","status":%q,"version":"2.1.292"}`,
		pid, sid, cwd, status)
}

func boxPidPath(bd, id string) string {
	rel, ok := box.HomePidRel(id)
	if !ok {
		panic("bad session id " + id)
	}
	return filepath.Join(bd, rel)
}

func writeBoxFile(t *testing.T, p, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func boxRegPath(bd string, pid int) string {
	return filepath.Join(bd, ".claude", "sessions", strconv.Itoa(pid)+".json")
}

// boxedRegManager: s1 and s2 are claude sessions in the shared box "box-1", cwd
// /work/proj. Transcripts A (older, s1's) and B (newer, s2's) live in the box's
// .claude; home pidfiles say s1=39, s2=40; registry files 39/40 name A/B. The
// newest transcript is B, so a valid registry is told apart from a guess by s1
// reading "from A".
func boxedRegManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m, _ := newLocatorManager(t)
	state := t.TempDir()
	m.stateDir = state
	bd := filepath.Join(state, "boxstate", "box-1")
	now := time.Now()
	writeTx(t, bd, locCwd, "aaaa-1", "from A", now.Add(-10*time.Minute))
	writeTx(t, bd, locCwd, "bbbb-2", "from B", now.Add(-5*time.Minute))
	for id, pid := range map[string]int{"s1": 39, "s2": 40} {
		m.sessions[id].Box = "box-1"
		writeBoxFile(t, boxPidPath(bd, id), strconv.Itoa(pid)+"\n", now.Add(-time.Minute))
	}
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", locCwd, "idle"), now)
	writeBoxFile(t, boxRegPath(bd, 40), regBoxJSON(40, "bbbb-2", locCwd, "idle"), now)
	return m, bd
}

func TestBoxedLocateUsesRegistry(t *testing.T) {
	m, _ := boxedRegManager(t)
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("s1 peek=%q want its own %q (not the newer sibling's)", got, "from A")
	}
	if got := peekText(t, m, "s2"); got != "from B" {
		t.Errorf("s2 peek=%q want %q", got, "from B")
	}
	if u, ok := m.UsageFor("s1"); !ok || u.Model != "model-aaaa-1" {
		t.Errorf("s1 usage model=%q ok=%v want model-aaaa-1", u.Model, ok)
	}
	if l := m.locate("s1"); l.reg == nil || l.reg.SessionID != "aaaa-1" {
		t.Errorf("s1 reg=%+v, want the validated entry", l.reg)
	}
}

func TestBoxedLocateReportsWaiting(t *testing.T) {
	m, bd := boxedRegManager(t)
	writeTxLines(t, bd, locCwd, "aaaa-1", time.Now().Add(-10*time.Minute), txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("touch /tmp/a")))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", locCwd, "waiting"), time.Now())
	p, ok, blocked := m.peekAndBlocked("s1")
	if !ok || !blocked || p.Ask != s1Ask {
		t.Fatalf("peek ok=%v blocked=%v ask=%q, want a blocked ask %q", ok, blocked, p.Ask, s1Ask)
	}
	m.sessions["s1"].lastActivityAt = time.Now() // the prompt animates: PTY says "working"
	for _, i := range m.List() {
		if i.ID == "s1" && (i.Need != "ask" || i.Ask != s1Ask) {
			t.Errorf("List s1 need=%q ask=%q, want ask/%q", i.Need, i.Ask, s1Ask)
		}
		if i.ID == "s2" && i.Need == "ask" {
			t.Errorf("List s2 need=ask: s1's prompt leaked to its sibling")
		}
	}
}

func TestBoxedAnswerWritesOwnPTY(t *testing.T) {
	m, bd := boxedRegManager(t)
	writeTxLines(t, bd, locCwd, "aaaa-1", time.Now().Add(-10*time.Minute), txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("touch /tmp/a")))
	writeTxLines(t, bd, locCwd, "bbbb-2", time.Now().Add(-5*time.Minute), txUser("go"), txRec("assistant", txThinking()), txRec("assistant", txBash("rm -rf /tmp/b")))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", locCwd, "waiting"), time.Now())
	writeBoxFile(t, boxRegPath(bd, 40), regBoxJSON(40, "bbbb-2", locCwd, "waiting"), time.Now())
	pipes := map[string]*os.File{}
	for _, id := range []string{"s1", "s2"} {
		s := m.sessions[id]
		s.replyable = true
		s.status = statusLive
		pipes[id] = pipeTo(t, s)
	}
	if err := m.Answer("s1", "yes", hexSHA(s1Ask)); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if got := written(t, pipes["s1"]); got != "1" {
		t.Errorf("s1 PTY got %q want exactly %q", got, "1")
	}
	if got := written(t, pipes["s2"]); got != "" {
		t.Errorf("s2 PTY got %q, want nothing", got)
	}
}

func TestBoxedLocateFollowsClear(t *testing.T) {
	m, bd := boxedRegManager(t)
	_ = peekText(t, m, "s1") // prime the cache on A
	writeTx(t, bd, locCwd, "cccc-3", "from C", time.Now().Add(-20*time.Minute))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "cccc-3", locCwd, "idle"), time.Now()) // what /clear does
	if got := peekText(t, m, "s1"); got != "from C" {
		t.Errorf("after /clear s1 peek=%q want %q", got, "from C")
	}
	if got := peekText(t, m, "s2"); got != "from B" {
		t.Errorf("s2 peek=%q want %q (/clear in s1 must not move s2)", got, "from B")
	}
}

func TestBoxedLocateNotesSessionID(t *testing.T) {
	m, _ := boxedRegManager(t)
	m.locate("s1")
	m.locate("s2")
	if got := m.sessions["s1"].claudeSID(); got != "aaaa-1" {
		t.Errorf("s1 claudeSID=%q want aaaa-1", got)
	}
	if got := m.sessions["s2"].claudeSID(); got != "bbbb-2" {
		t.Errorf("s2 claudeSID=%q want bbbb-2", got)
	}
}

// Review Focus 1: a box adopted after an upgrade has no home pidfile.
func TestBoxedLocateNamesNoPathWithoutPidfile(t *testing.T) {
	m, bd := boxedRegManager(t)
	if err := os.Remove(boxPidPath(bd, "s1")); err != nil {
		t.Fatal(err)
	}
	if l := m.locate("s1"); l.reg != nil {
		t.Errorf("reg=%+v with no pidfile", l.reg)
	}
	wantUnbound(t, m, "s1")
	if _, _, blocked := m.peekAndBlocked("s1"); blocked {
		t.Error("blocked without a registry")
	}
}

// Review Focus 2: claude still on the trust dialog has only <pid>.<hash>.key.
func TestBoxedLocateIgnoresKeyFileOnly(t *testing.T) {
	m, bd := boxedRegManager(t)
	if err := os.Remove(boxRegPath(bd, 39)); err != nil {
		t.Fatal(err)
	}
	// Even a perfectly valid, "waiting" body in a .key file must never be parsed.
	writeBoxFile(t, filepath.Join(bd, ".claude", "sessions", "39.0a1b2c.key"),
		regBoxJSON(39, "aaaa-1", locCwd, "waiting"), time.Now())
	if l := m.locate("s1"); l.reg != nil {
		t.Errorf("reg=%+v from a key file", l.reg)
	}
	if _, _, blocked := m.peekAndBlocked("s1"); blocked {
		t.Error("blocked from a key file")
	}
	wantUnbound(t, m, "s1")
}

func TestBoxedLocateExitedSkipsRegistry(t *testing.T) {
	m, _ := boxedRegManager(t)
	m.sessions["s1"].mu.Lock()
	m.sessions["s1"].status = statusExited
	m.sessions["s1"].mu.Unlock()
	if l := m.locate("s1"); l.reg != nil {
		t.Errorf("exited session used the registry; reg=%+v", l.reg)
	}
	wantUnbound(t, m, "s1")
}

// A validated entry is trusted as is: its transcript missing means "no
// transcript", never a sibling's file.
func TestBoxedLocateMissingTranscriptDoesNotFallBack(t *testing.T) {
	m, bd := boxedRegManager(t)
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "gone-9", locCwd, "idle"), time.Now())
	if _, ok := m.PeekFor("s1"); ok {
		t.Error("PeekFor ok=true though the registry-named transcript does not exist")
	}
	if l := m.locate("s1"); l.reg == nil || l.path != "" {
		t.Errorf("reg=%v path=%q, want a validated entry and no path", l.reg, l.path)
	}
}

// 2.1.286 moves the registry cwd into a worktree under the spawn dir; the
// transcript lives in that worktree's project dir.
func TestBoxedLocateWorktreeCwd(t *testing.T) {
	m, bd := boxedRegManager(t)
	wt := locCwd + "/.claude/worktrees/feat"
	writeTx(t, bd, wt, "aaaa-1", "from worktree", time.Now().Add(-10*time.Minute))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", wt, "idle"), time.Now())
	if got := peekText(t, m, "s1"); got != "from worktree" {
		t.Errorf("peek=%q want %q", got, "from worktree")
	}
}

// Review Focus 4: the spawn cwd is a symlink (macOS /tmp -> /private/tmp); the
// box runs claude in the resolved dir, so the registry names that.
func TestBoxedLocateResolvedCwd(t *testing.T) {
	m, bd := boxedRegManager(t)
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	m.sessions["s1"].Cwd = link
	now := time.Now()
	writeTx(t, bd, real, "aaaa-1", "from resolved", now.Add(-10*time.Minute))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", real, "idle"), now)
	if l := m.locate("s1"); l.reg == nil {
		t.Fatal("registry rejected although its cwd is the resolved spawn cwd")
	}
	if got := peekText(t, m, "s1"); got != "from resolved" {
		t.Errorf("peek=%q want %q", got, "from resolved")
	}
	// The raw (unresolved) form is accepted too.
	writeTx(t, bd, link, "aaaa-1", "from raw", now.Add(-10*time.Minute))
	writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", link, "idle"), now.Add(time.Second))
	if l := m.locate("s1"); l.reg == nil {
		t.Error("registry rejected although its cwd is the raw spawn cwd")
	}
}

func TestCwdFormsOf(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := cwdFormsOf(link + "/"); len(got) != 2 || got[0] != link || got[1] != real {
		t.Errorf("symlink: %v want [%s %s]", got, link, real)
	}
	if got := cwdFormsOf(real); len(got) != 1 || got[0] != real {
		t.Errorf("plain dir: %v want [%s]", got, real)
	}
	if got := cwdFormsOf("/no/such/dir/"); len(got) != 1 || got[0] != "/no/such/dir" {
		t.Errorf("missing dir: %v", got)
	}
	s := &Session{Cwd: link}
	if a, b := s.cwdForms(), s.cwdForms(); len(a) != 2 || len(b) != 2 {
		t.Errorf("cwdForms %v %v", a, b)
	}
}

func TestBoxCwdMatchesIsLexical(t *testing.T) {
	cwds := []string{"/work/proj"}
	for _, c := range []struct {
		cwd  string
		want bool
	}{
		{"/work/proj", true},
		{"/work/proj/", true},
		{"/work/proj/.claude/worktrees/x", true},
		{"/work/proj/sub/../sub2", true},
		{"/work/projx", false},
		{"/work/proj/../other", false},
		{"/etc", false},
		{"work/proj", false},
		{"", false},
	} {
		if got := boxCwdMatches(c.cwd, cwds); got != c.want {
			t.Errorf("boxCwdMatches(%q)=%v want %v", c.cwd, got, c.want)
		}
	}
	if boxCwdMatches("/work/proj", nil) {
		t.Error("matched with no spawn forms")
	}
}

// The security core: everything the box writes is hostile. Each row must fall
// leave s1 unbound (no path, no read), never use the registry, and never block
// on a FIFO.
func TestBoxedRegistryForgeries(t *testing.T) {
	now := time.Now()
	pid := func(bd string) string { return boxPidPath(bd, "s1") }
	symlinkReg := func(t *testing.T, bd, target string) {
		t.Helper()
		writeBoxFile(t, target, regBoxJSON(39, "aaaa-1", locCwd, "idle"), now)
		_ = os.Remove(boxRegPath(bd, 39))
		if err := os.Symlink(target, boxRegPath(bd, 39)); err != nil {
			t.Fatal(err)
		}
	}
	symlinkPid := func(t *testing.T, bd, target string) {
		t.Helper()
		writeBoxFile(t, target, "39\n", now.Add(-time.Minute))
		_ = os.Remove(pid(bd))
		if err := os.Symlink(target, pid(bd)); err != nil {
			t.Fatal(err)
		}
	}
	symlinkSessions := func(t *testing.T, bd, dir string) {
		t.Helper()
		writeBoxFile(t, filepath.Join(dir, "39.json"), regBoxJSON(39, "aaaa-1", locCwd, "idle"), now)
		sessions := filepath.Join(bd, ".claude", "sessions")
		if err := os.RemoveAll(sessions); err != nil {
			t.Fatal(err)
		}
		// A relative target stays inside the box, which os.Root would follow.
		target := dir
		if rel, err := filepath.Rel(filepath.Dir(sessions), dir); err == nil && strings.HasPrefix(dir, bd) {
			target = rel
		}
		if err := os.Symlink(target, sessions); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		mut  func(t *testing.T, bd string)
	}{
		{"pidfile garbage", func(t *testing.T, bd string) { writeBoxFile(t, pid(bd), "abc\n", now.Add(-time.Minute)) }},
		{"pidfile with trailing junk", func(t *testing.T, bd string) { writeBoxFile(t, pid(bd), "39 40\n", now.Add(-time.Minute)) }},
		{"pidfile 33 bytes", func(t *testing.T, bd string) {
			writeBoxFile(t, pid(bd), "39"+strings.Repeat(" ", 31), now.Add(-time.Minute))
		}},
		{"pidfile FIFO", func(t *testing.T, bd string) {
			_ = os.Remove(pid(bd))
			mkFifo(t, pid(bd), now.Add(-time.Minute))
		}},
		{"pidfile symlink inside the box", func(t *testing.T, bd string) {
			symlinkPid(t, bd, filepath.Join(bd, "real.pid"))
		}},
		{"pidfile symlink out of the box", func(t *testing.T, bd string) {
			symlinkPid(t, bd, filepath.Join(t.TempDir(), "host.pid"))
		}},
		{"pidfile +39 (sign)", func(t *testing.T, bd string) {
			writeBoxFile(t, pid(bd), "+39\n", now.Add(-time.Minute))
		}},
		{"pidfile 039 (leading zero)", func(t *testing.T, bd string) {
			writeBoxFile(t, pid(bd), "039\n", now.Add(-time.Minute))
		}},
		{"pid 0", func(t *testing.T, bd string) {
			writeBoxFile(t, pid(bd), "0\n", now.Add(-time.Minute))
			writeBoxFile(t, boxRegPath(bd, 0), regBoxJSON(0, "aaaa-1", locCwd, "idle"), now)
		}},
		{"pid 9999999", func(t *testing.T, bd string) {
			writeBoxFile(t, pid(bd), "9999999\n", now.Add(-time.Minute))
			writeBoxFile(t, boxRegPath(bd, 9999999), regBoxJSON(9999999, "aaaa-1", locCwd, "idle"), now)
		}},
		{"registry pid mismatch", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(41, "aaaa-1", locCwd, "idle"), now)
		}},
		{"registry symlink inside the box", func(t *testing.T, bd string) {
			symlinkReg(t, bd, filepath.Join(bd, "real.json"))
		}},
		{"registry symlink out of the box", func(t *testing.T, bd string) {
			symlinkReg(t, bd, filepath.Join(t.TempDir(), "host.json"))
		}},
		{"registry FIFO", func(t *testing.T, bd string) {
			_ = os.Remove(boxRegPath(bd, 39))
			mkFifo(t, boxRegPath(bd, 39), now)
		}},
		{"registry over 64 KiB", func(t *testing.T, bd string) {
			body := regBoxJSON(39, "aaaa-1", locCwd, "idle") + strings.Repeat(" ", registryMaxBytes)
			writeBoxFile(t, boxRegPath(bd, 39), body, now)
		}},
		{"registry sid ../x", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "../x", locCwd, "idle"), now)
		}},
		{"registry sid -x", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "-x", locCwd, "idle"), now)
		}},
		{"registry cwd /etc", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", "/etc", "idle"), now)
		}},
		{"registry cwd with ..", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", locCwd+"/../other", "idle"), now)
		}},
		{"registry relative cwd", func(t *testing.T, bd string) {
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", "work/proj", "idle"), now)
		}},
		{"registry older than the pidfile", func(t *testing.T, bd string) {
			// A <pid>.json left by an earlier container whose claude had the same pid.
			writeBoxFile(t, boxRegPath(bd, 39), regBoxJSON(39, "aaaa-1", locCwd, "idle"), now.Add(-time.Hour))
		}},
		{"sessions dir symlinked out of the box", func(t *testing.T, bd string) {
			symlinkSessions(t, bd, filepath.Join(t.TempDir(), "sessions"))
		}},
		{"sessions dir symlinked inside the box", func(t *testing.T, bd string) {
			symlinkSessions(t, bd, filepath.Join(bd, "elsewhere"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, bd := boxedRegManager(t)
			c.mut(t, bd)
			var l located
			within2s(t, "locate", func() { l = m.locate("s1") })
			if l.reg != nil {
				t.Fatalf("forged entry accepted: %+v", l.reg)
			}
			var p Peek
			var ok, blocked bool
			within2s(t, "peekAndBlocked", func() { p, ok, blocked = m.peekAndBlocked("s1") })
			if blocked {
				t.Error("blocked from a forged registry")
			}
			if ok {
				t.Errorf("peek=%q read a transcript for a session with only a forged registry; it is unbound", p.Text)
			}
			if l.path != "" {
				t.Errorf("locate path=%q, want none: a forged registry must not trigger a project-dir scan", l.path)
			}
			if got := m.sessions["s1"].claudeSID(); got != "" {
				t.Errorf("claudeSID=%q noted from a forged entry", got)
			}
		})
	}
}

// A registry that validates but names a transcript that is a FIFO must neither
// block nor fall back to a sibling's file: the candidates go through boxread.
func TestBoxedRegistryTranscriptCandidatesAreGuarded(t *testing.T) {
	m, bd := boxedRegManager(t)
	p := filepath.Join(projDirIn(bd), "aaaa-1.jsonl")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	mkFifo(t, p, time.Now())
	var ok bool
	within2s(t, "PeekFor", func() { _, ok = m.PeekFor("s1") })
	if ok {
		t.Error("PeekFor hit through a FIFO transcript named by the registry")
	}
}

// .claude/projects symlinked OUT of the box, with an otherwise valid registry:
// nothing outside may be read, and nothing may hang.
func TestBoxedProjectsDirSymlinkedOutIsNeverRead(t *testing.T) {
	m, bd := boxedRegManager(t)
	outside := t.TempDir()
	writeTx(t, outside, locCwd, "aaaa-1", "SECRET from outside", time.Now())
	projects := filepath.Join(bd, ".claude", "projects")
	if err := os.RemoveAll(projects); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, ".claude", "projects"), projects); err != nil {
		t.Fatal(err)
	}
	var l located
	within2s(t, "locate", func() { l = m.locate("s1") })
	if l.reg == nil {
		t.Fatal("registry should still validate; only the projects dir is hostile")
	}
	if l.path != "" {
		t.Errorf("locate yielded a transcript path %q through a projects dir symlinked out of the box", l.path)
	}
	var p Peek
	var ok bool
	within2s(t, "PeekFor", func() { p, ok = m.PeekFor("s1") })
	if ok || strings.Contains(p.Text, "SECRET") {
		t.Errorf("peek ok=%v text=%q, outside content must never be returned", ok, p.Text)
	}
	if u, ok := m.UsageFor("s1"); ok {
		t.Errorf("usage hit through an outside projects dir: %+v", u)
	}
	if _, ok := m.AttributionFor("s1"); ok {
		t.Error("attribution hit through an outside projects dir")
	}
}

func TestLiveCwdBoxedAcceptsResolvedSpawn(t *testing.T) {
	main, wt, _ := mkCheckouts(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(main, link); err != nil {
		t.Fatal(err)
	}
	if got := liveCwd(wt, []string{link}, true); got != "" {
		t.Errorf("only the raw symlink form: got %q want empty", got)
	}
	if got := liveCwd(wt, []string{link, main}, true); got != wt {
		t.Errorf("raw+resolved forms: got %q want %q", got, wt)
	}
	// Host tier keeps its single-form behaviour.
	if got := liveCwd(wt, []string{main}, false); got != wt {
		t.Errorf("host: got %q want %q", got, wt)
	}
}
