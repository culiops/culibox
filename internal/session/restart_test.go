package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/launcher"
)

func claudeSIDOf(s *Session) string { return s.claudeSID() }

func TestLocateRecordsClaudeSessionID(t *testing.T) {
	m, home := newLocatorManager(t)
	sc, err := openSidecar(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	sc.put("s1", sessionMeta{Agent: "claude", Cwd: locCwd})
	m.sidecar = sc
	_ = peekText(t, m, "s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "aaaa-1" {
		t.Fatalf("claudeSessionID=%q want aaaa-1", got)
	}
	if got := sc.all()["s1"].ClaudeSessionID; got != "aaaa-1" {
		t.Fatalf("sidecar ClaudeSessionID=%q want aaaa-1", got)
	}
	// /clear starts a new conversation: the id follows it.
	writeTx(t, home, locCwd, "cccc-3", "from C", time.Now().Add(-20*time.Minute))
	writeReg(t, home, 101, regJSON(101, "cccc-3", "5000", "idle"))
	_ = peekText(t, m, "s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "cccc-3" {
		t.Fatalf("after /clear claudeSessionID=%q want cccc-3", got)
	}
}

func TestLocateNeverRecordsForAnExitedSession(t *testing.T) {
	m, _ := newLocatorManager(t)
	m.sessions["s1"].status = statusExited
	m.PeekFor("s1")
	if got := claudeSIDOf(m.sessions["s1"]); got != "" {
		t.Fatalf("an exited session recorded %q from a pid that may be reused", got)
	}
}

func TestReconcileRestoresClaudeSessionID(t *testing.T) {
	for _, c := range []struct{ stored, want string }{{"abcd-1234", "abcd-1234"}, {"../../etc", ""}} {
		fb := newFakeBackend()
		fb.seedLive("claude-2")
		path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
			"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), ClaudeSessionID: c.stored},
		})
		m := newReconcileManager(t, fb, path)
		if err := m.Reconcile(); err != nil {
			t.Fatal(err)
		}
		s, ok := m.Get("claude-2")
		if !ok {
			t.Fatal("survivor not re-attached")
		}
		if got := claudeSIDOf(s); got != c.want {
			t.Errorf("stored %q restored as %q, want %q", c.stored, got, c.want)
		}
		_ = m.Kill("claude-2")
	}
}

func TestResumeFor(t *testing.T) {
	cases := []struct {
		runtime, bin, sid string
		exclusive         bool
		want              string
	}{
		{config.RuntimeHost, "/usr/bin/claude", "abcd-1", false, "abcd-1"},
		{config.RuntimeBox, "/usr/bin/claude", "abcd-1", false, "abcd-1"},
		{config.RuntimeBox, "/usr/bin/claude", "abcd-1", true, ""}, // an elevate-bound box is new on restart
		{config.RuntimeBox, "codex", "abcd-1", false, ""},          // codex restarts fresh
		{config.RuntimeBox, "/usr/bin/claude", "a/b", false, ""},   // never a path-ish id
		{config.RuntimeHost, "codex", "abcd-1", false, ""},         // codex restarts fresh
		{config.RuntimeHost, "/usr/bin/claude", "", false, ""},     // no id known
		{config.RuntimeHost, "/usr/bin/claude", "a/b", false, ""},  // never a path-ish id
	}
	for _, c := range cases {
		if got := resumeFor(c.runtime, c.bin, c.sid, c.exclusive); got != c.want {
			t.Errorf("resumeFor(%q,%q,%q,%v)=%q want %q", c.runtime, c.bin, c.sid, c.exclusive, got, c.want)
		}
	}
}

func TestDropResume(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"present", []string{"claude", "--resume", "abcd-1", "--model", "x"}, []string{"claude", "--model", "x"}, true},
		{"absent", []string{"claude", "--model", "x"}, []string{"claude", "--model", "x"}, false},
		{"other id", []string{"claude", "--resume", "zzzz-9"}, []string{"claude", "--resume", "zzzz-9"}, false},
		{"after the prompt marker", []string{"claude", "--", "--resume", "abcd-1"}, []string{"claude", "--", "--resume", "abcd-1"}, false},
		{"both sides of the marker", []string{"claude", "--resume", "abcd-1", "--", "--resume", "abcd-1"}, []string{"claude", "--", "--resume", "abcd-1"}, true},
		{"dangling flag", []string{"claude", "--resume"}, []string{"claude", "--resume"}, false},
	}
	for _, c := range cases {
		in := append([]string(nil), c.in...)
		got, ok := dropResume(in, "abcd-1")
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v,%v want %v,%v", c.name, got, ok, c.want, c.ok)
		}
		if !slices.Equal(in, c.in) {
			t.Errorf("%s: the input was mutated: %v", c.name, in)
		}
	}
}

// exitedSession spawns a session whose process exits at once and waits for it.
func exitedSession(t *testing.T, m *Manager, spec launcher.SpawnSpec) *Session {
	t.Helper()
	s, err := m.Spawn(spec)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, statusExited, 5*time.Second)
	return s
}

func TestRestartRespawnsWithTheSameSpec(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude", replyDefault: false}
	m, _ := newCtlManager(t, l)
	yes := true
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Name: "api", Cwd: cwd,
		Options: map[string]any{"effort": "low"}, Replyable: &yes})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	got := l.last()
	if got.Agent != "claude" || got.Name != "api" || got.Cwd != cwd || got.Runtime != config.RuntimeHost ||
		got.Options["effort"] != "low" || got.Resume != "abcd-1234" || got.Replyable != nil || got.Prompt != "" {
		t.Fatalf("restart spec = %+v", got)
	}
	if ns.ID == old.ID {
		t.Fatal("restart must spawn a NEW session")
	}
	if _, ok := m.Get(old.ID); ok {
		t.Fatal("the old session is still listed")
	}
	if ns.Info().Replyable {
		t.Fatal("replies were inherited; a restart starts at the profile default")
	}
}

func TestRestartRefusesLiveAndUnknown(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	if _, err := m.Restart(s.ID); !errors.Is(err, ErrNotExited) {
		t.Fatalf("live: err=%v want ErrNotExited", err)
	}
	if _, err := m.Restart("nope-1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown: err=%v want ErrNoSession", err)
	}
}

func TestRestartTwiceStartsOneReplacement(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ns, err := m.Restart(old.ID); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
				t.Cleanup(func() { _ = m.Kill(ns.ID) })
			} else if !errors.Is(err, ErrRestarting) && !errors.Is(err, ErrNoSession) {
				t.Errorf("loser err=%v, want ErrRestarting or ErrNoSession", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d replacements started, want exactly 1", wins)
	}
}

func TestListMarksResumable(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, _ := newCtlManager(t, l)
	s := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	find := func() Info {
		for _, i := range m.List() {
			if i.ID == s.ID {
				return i
			}
		}
		t.Fatal("session not listed")
		return Info{}
	}
	if find().Resumable {
		t.Fatal("resumable without a known conversation id")
	}
	s.mu.Lock()
	s.claudeSessionID = "abcd-1234"
	s.mu.Unlock()
	if !find().Resumable {
		t.Fatal("an exited host claude with an id must be resumable")
	}
}

// resumeLauncher honours spec.Resume the way the real launcher does, so the
// argv the box wrapper sees carries `--resume <sid>` exactly when asked.
type resumeLauncher struct{ ctlLauncher }

func (l *resumeLauncher) Build(spec launcher.SpawnSpec) ([]string, []string, error) {
	argv, env, err := l.ctlLauncher.Build(spec)
	if spec.Resume != "" {
		argv = append(argv, "--resume", spec.Resume)
	}
	return argv, env, err
}

// boxedRestartManager: a manager whose boxed spawns land in the fake box
// "ab-box-test", with its state dir under a temp data dir. Every spawned
// process writes the argv it was REALLY started with (after the box wrapper
// and after any --resume drop) to the returned file, one argument per line.
func boxedRestartManager(t *testing.T) (*Manager, string, string) {
	t.Helper()
	argvFile := filepath.Join(t.TempDir(), "argv.txt")
	l := &resumeLauncher{ctlLauncher{
		argv: []string{"sh", "-c", `printf '%s\n' "$@" > "` + argvFile + `"`, "sh"},
		bin:  "/usr/bin/claude", runtime: config.RuntimeBox}}
	m, _ := newCtlManager(t, &l.ctlLauncher)
	m.launcher = l
	data := t.TempDir()
	m.SetPaths(t.TempDir(), data)
	m.SetBoxes(&fakeBoxes{})
	return m, filepath.Join(data, "boxstate", "ab-box-test"), argvFile
}

// ranArgv waits for ns to exit and returns the argv its process was given.
func ranArgv(t *testing.T, ns *Session, argvFile string) []string {
	t.Helper()
	waitStatus(t, ns, statusExited, 5*time.Second)
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestRestartBoxedResumesWhenTranscriptPresent(t *testing.T) {
	m, boxHome, argvFile := boxedRestartManager(t)
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	writeTx(t, boxHome, cwd, "abcd-1234", "hello", time.Now())

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	argv := ranArgv(t, ns, argvFile)
	if i := slices.Index(argv, "--resume"); i < 0 || i+1 >= len(argv) || argv[i+1] != "abcd-1234" {
		t.Fatalf("argv=%v, want --resume abcd-1234", argv)
	}
	if ns.Info().ResumeSkipped != "" {
		t.Fatalf("ResumeSkipped=%q on a resumed restart", ns.Info().ResumeSkipped)
	}
}

func TestRestartBoxedStartsFreshWhenTranscriptMissing(t *testing.T) {
	m, boxHome, argvFile := boxedRestartManager(t)
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	// A sibling conversation and a different project dir must not count.
	writeTx(t, boxHome, cwd, "other-1", "x", time.Now())
	writeTx(t, boxHome, filepath.Join(cwd, "elsewhere"), "abcd-1234", "x", time.Now())

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	argv := ranArgv(t, ns, argvFile)
	if slices.Contains(argv, "--resume") {
		t.Fatalf("argv=%v must not carry --resume", argv)
	}
	if got := ns.Info().ResumeSkipped; got != "the previous conversation is not in this box's home" {
		t.Fatalf("ResumeSkipped=%q", got)
	}
}

// A transcript that is a symlink the box planted is not a transcript.
func TestRestartBoxedDoesNotTrustASymlinkedTranscript(t *testing.T) {
	m, boxHome, argvFile := boxedRestartManager(t)
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	dir := filepath.Join(boxHome, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "real.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "abcd-1234.jsonl")); err != nil {
		t.Fatal(err)
	}
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	argv := ranArgv(t, ns, argvFile)
	if slices.Contains(argv, "--resume") {
		t.Fatalf("argv=%v resumed through a symlink", argv)
	}
}

// No state dir means no way to look: never resume on a guess.
func TestRestartBoxedWithoutStateDirStartsFresh(t *testing.T) {
	m, _, argvFile := boxedRestartManager(t)
	m.SetPaths("", "")
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	argv := ranArgv(t, ns, argvFile)
	if slices.Contains(argv, "--resume") || ns.Info().ResumeSkipped == "" {
		t.Fatalf("argv=%v skipped=%q", argv, ns.Info().ResumeSkipped)
	}
}

func TestListResumableBoxed(t *testing.T) {
	m, _, _ := boxedRestartManager(t)
	s := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	s.mu.Lock()
	s.claudeSessionID = "abcd-1234"
	s.mu.Unlock()
	find := func() Info {
		for _, i := range m.List() {
			if i.ID == s.ID {
				return i
			}
		}
		t.Fatal("session not listed")
		return Info{}
	}
	if !find().Resumable {
		t.Fatal("an exited boxed claude with an id must be resumable")
	}
	s.Exclusive = true
	if find().Resumable {
		t.Fatal("an exclusive box never offers resume")
	}
}

// Seeded at spawn, so the first locate does not walk symlinks.
func TestBoxedSpawnSeedsCwdForms(t *testing.T) {
	m, _, _ := boxedRestartManager(t)
	cwd := t.TempDir()
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	s.mu.Lock()
	got := s.cwdFormsCache
	s.mu.Unlock()
	if !slices.Equal(got, cwdFormsOf(cwd)) || len(got) == 0 {
		t.Fatalf("cwdFormsCache=%v want %v", got, cwdFormsOf(cwd))
	}
}

// wrapShapeBoxes is a Boxes whose Wrap reshapes the fake's wrapped argv.
type wrapShapeBoxes struct {
	*fakeBoxes
	reshape func(wrapped []string) []string
}

func (w wrapShapeBoxes) Wrap(ctx context.Context, cwd, sid string, key box.BoxKey, argv, env []string, ro []box.Mount) ([]string, []string, string, error) {
	wrapped, env, name, err := w.fakeBoxes.Wrap(ctx, cwd, sid, key, argv, env, ro)
	if err != nil {
		return nil, nil, "", err
	}
	return w.reshape(wrapped), env, name, nil
}

// A "--" in the wrapper's own prefix (docker args, a profile's args) must not
// stop the resume from being dropped, nor make the skip note a lie.
func TestRestartBoxedDropsResumeDespiteDashDashInWrapperPrefix(t *testing.T) {
	m, _, argvFile := boxedRestartManager(t)
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	m.SetBoxes(wrapShapeBoxes{&fakeBoxes{}, func(w []string) []string {
		// /usr/bin/env -- <real wrapped argv...>: env accepts "--", so it still runs.
		return append([]string{w[0], "--"}, w[1:]...)
	}})
	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if argv := ranArgv(t, ns, argvFile); slices.Contains(argv, "--resume") {
		t.Fatalf("argv=%v still carries --resume", argv)
	}
	if ns.Info().ResumeSkipped == "" {
		t.Fatal("ResumeSkipped not set though the resume was dropped")
	}
}

// If the wrapper did not leave the agent argv as its tail the drop cannot be
// trusted: fail closed, release the box, start nothing.
func TestRestartBoxedFailsClosedWhenWrapperTailIsNotTheAgentArgv(t *testing.T) {
	m, _, _ := boxedRestartManager(t)
	cwd := t.TempDir()
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: cwd})
	old.mu.Lock()
	old.claudeSessionID = "abcd-1234"
	old.mu.Unlock()
	// The old session's box release is asynchronous: status turns "exited"
	// first, and the retention goroutine releases once Done closes. Count on ONE
	// fake across the whole test and wait for that release, so the baseline is
	// settled before the restart and the assertion is "exactly one more".
	m.mu.Lock()
	fb := m.boxes.(*fakeBoxes)
	m.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, releases, _ := fb.counts(); releases == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the exited old session never released its box")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.SetBoxes(wrapShapeBoxes{fb, func(w []string) []string {
		return append(slices.Clone(w), "extra-trailing-arg")
	}})
	if _, err := m.Restart(old.ID); err == nil {
		t.Fatal("restart must fail when the drop cannot be done safely")
	}
	if wraps, releases, _ := fb.counts(); wraps != 2 || releases != 2 {
		t.Fatalf("wraps=%d releases=%d, want 2 and 2: the failed restart acquired one box and must release exactly that one", wraps, releases)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("%d sessions listed, want only the old one: nothing may have started", got)
	}
	if _, ok := m.Get(old.ID); !ok {
		t.Fatal("the old session must survive a failed restart")
	}
}

// spawnClaude spawns a live (sleeping) claude session on a sidecar-backed
// manager, the path every new session takes.
func spawnClaude(t *testing.T) (*Manager, *Session, string) {
	t.Helper()
	l := &ctlLauncher{argv: []string{"sleep", "30"}, bin: "/usr/bin/claude"}
	m, path := newCtlManager(t, l)
	s, err := m.Spawn(launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(s.ID) })
	return m, s, path
}

func TestNoteAppendsConversationHistory(t *testing.T) {
	m, s, path := spawnClaude(t)
	if ids, complete := s.conversations(); len(ids) != 0 || !complete {
		t.Fatalf("a new spawn: ids=%v complete=%v, want [] true", ids, complete)
	}
	for _, sid := range []string{"aaaa-1", "bbbb-2", "aaaa-1"} {
		m.noteClaudeSessionID(s, s.ID, sid)
	}
	ids, complete := s.conversations()
	if !slices.Equal(ids, []string{"aaaa-1", "bbbb-2"}) || !complete {
		t.Fatalf("conversations() = %v,%v want [aaaa-1 bbbb-2],true", ids, complete)
	}
	md := sidecarEntry(t, path, s.ID)
	if !slices.Equal(md.ClaudeSessionIDs, []string{"aaaa-1", "bbbb-2"}) || !md.CostComplete || md.ClaudeSessionID != "aaaa-1" {
		t.Fatalf("sidecar ids=%v complete=%v sid=%q", md.ClaudeSessionIDs, md.CostComplete, md.ClaudeSessionID)
	}
	// conversations() hands out a copy: mutating it must not reach the session.
	ids[0] = "zzzz-9"
	if again, _ := s.conversations(); again[0] != "aaaa-1" {
		t.Fatalf("conversations() leaked its backing array: %v", again)
	}
}

func TestLegacySessionNeverCostComplete(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), ClaudeSessionID: "abcd-1234"},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	s, ok := m.Get("claude-2")
	if !ok {
		t.Fatal("survivor not re-attached")
	}
	t.Cleanup(func() { _ = m.Kill("claude-2") })
	if ids, complete := s.conversations(); !slices.Equal(ids, []string{"abcd-1234"}) || complete {
		t.Fatalf("legacy restore: ids=%v complete=%v want [abcd-1234] false", ids, complete)
	}
	m.noteClaudeSessionID(s, "claude-2", "efgh-5678")
	if ids, complete := s.conversations(); !slices.Equal(ids, []string{"abcd-1234", "efgh-5678"}) || complete {
		t.Fatalf("legacy after a note: ids=%v complete=%v want both ids, false", ids, complete)
	}
	if md := sidecarEntry(t, path, "claude-2"); md.CostComplete {
		t.Fatal("a note upgraded a legacy session to cost_complete")
	}
}

func TestRestoreDropsHistoryWithABadEntry(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), CostComplete: true,
			ClaudeSessionID: "abcd-1234", ClaudeSessionIDs: []string{"abcd-1234", "../../etc"}},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("claude-2")
	t.Cleanup(func() { _ = m.Kill("claude-2") })
	// The list is dropped whole, but the current conversation (the singular
	// field, validated separately) is still known, so it alone survives.
	if ids, complete := s.conversations(); !slices.Equal(ids, []string{"abcd-1234"}) || complete {
		t.Fatalf("a bad entry: ids=%v complete=%v want [abcd-1234] false", ids, complete)
	}
}

// A singular id that is not a plain id is dropped, and with it the proof that
// the history is whole: the cost must read partial, not complete.
func TestRestoreBadSingularSIDMarksIncomplete(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), CostComplete: true,
			ClaudeSessionID: "../../etc", ClaudeSessionIDs: []string{"aaaa-1"}},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("claude-2")
	t.Cleanup(func() { _ = m.Kill("claude-2") })
	if _, complete := s.conversations(); complete {
		t.Fatal("a dropped singular sid left the history complete")
	}
	if got := claudeSIDOf(s); got != "" {
		t.Fatalf("bad singular sid kept: %q", got)
	}
}

func TestRestoreKeepsACompleteHistory(t *testing.T) {
	fb := newFakeBackend()
	fb.seedLive("claude-2")
	path := writeSidecar(t, t.TempDir(), map[string]sessionMeta{
		"claude-2": {Agent: "claude", Cwd: "/w", Created: time.Now(), CostComplete: true,
			ClaudeSessionID: "bbbb-2", ClaudeSessionIDs: []string{"aaaa-1", "bbbb-2"}},
	})
	m := newReconcileManager(t, fb, path)
	if err := m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get("claude-2")
	t.Cleanup(func() { _ = m.Kill("claude-2") })
	if ids, complete := s.conversations(); !slices.Equal(ids, []string{"aaaa-1", "bbbb-2"}) || !complete {
		t.Fatalf("ids=%v complete=%v want [aaaa-1 bbbb-2] true", ids, complete)
	}
}

func TestHistoryCapMarksIncomplete(t *testing.T) {
	m, s, path := spawnClaude(t)
	for i := 0; i < maxConversationIDs+1; i++ {
		m.noteClaudeSessionID(s, s.ID, fmt.Sprintf("conv-%d", i))
	}
	ids, complete := s.conversations()
	if len(ids) != maxConversationIDs || complete {
		t.Fatalf("len=%d complete=%v want %d,false", len(ids), complete, maxConversationIDs)
	}
	if got := claudeSIDOf(s); got != fmt.Sprintf("conv-%d", maxConversationIDs) {
		t.Fatalf("the cap must not stop the current sid following: %q", got)
	}
	md := sidecarEntry(t, path, s.ID)
	if len(md.ClaudeSessionIDs) != maxConversationIDs || md.CostComplete {
		t.Fatalf("sidecar len=%d complete=%v", len(md.ClaudeSessionIDs), md.CostComplete)
	}
}

func TestNoteRejectsInvalidSID(t *testing.T) {
	m, s, _ := spawnClaude(t)
	m.noteClaudeSessionID(s, s.ID, "aaaa-1")
	m.noteClaudeSessionID(s, s.ID, "../../etc")
	ids, complete := s.conversations()
	if !slices.Equal(ids, []string{"aaaa-1"}) {
		t.Fatalf("ids=%v want [aaaa-1]", ids)
	}
	if complete {
		t.Fatal("a conversation we refused to record must make the history incomplete")
	}
}

func TestRestartKeepsHistory(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, path := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	m.noteClaudeSessionID(old, old.ID, "aaaa-1")
	m.noteClaudeSessionID(old, old.ID, "bbbb-2")

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if ids, complete := ns.conversations(); !slices.Equal(ids, []string{"aaaa-1", "bbbb-2"}) || !complete {
		t.Fatalf("restart: ids=%v complete=%v want [aaaa-1 bbbb-2] true", ids, complete)
	}
	md := sidecarEntry(t, path, ns.ID)
	if !slices.Equal(md.ClaudeSessionIDs, []string{"aaaa-1", "bbbb-2"}) || !md.CostComplete {
		t.Fatalf("restart sidecar: ids=%v complete=%v", md.ClaudeSessionIDs, md.CostComplete)
	}
}

func TestRestartOfAnIncompleteSessionStaysIncomplete(t *testing.T) {
	l := &ctlLauncher{argv: []string{"sh", "-c", "exit 0"}, bin: "/usr/bin/claude"}
	m, path := newCtlManager(t, l)
	old := exitedSession(t, m, launcher.SpawnSpec{Agent: "claude", Cwd: t.TempDir()})
	old.mu.Lock()
	old.claudeSessionIDs = []string{"aaaa-1"}
	old.costComplete = false
	old.mu.Unlock()

	ns, err := m.Restart(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill(ns.ID) })
	if ids, complete := ns.conversations(); !slices.Equal(ids, []string{"aaaa-1"}) || complete {
		t.Fatalf("ids=%v complete=%v want [aaaa-1] false", ids, complete)
	}
	if md := sidecarEntry(t, path, ns.ID); md.CostComplete {
		t.Fatal("the restarted session's sidecar claims completeness")
	}
}

// A snapshot taken before a newer one but written after it must not shorten
// the stored list or lift its completeness (the stale-write interleaving).
func TestSidecarNeverShortensStoredHistory(t *testing.T) {
	sc, err := openSidecar(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	sc.put("s1", sessionMeta{Agent: "claude", CostComplete: true})
	sc.setClaudeSessionID("s1", "bbbb-2", []string{"aaaa-1", "bbbb-2"}, true) // the newer snapshot, written first
	sc.setClaudeSessionID("s1", "aaaa-1", []string{"aaaa-1"}, true)           // the older one, arriving late
	md := sc.all()["s1"]
	if !slices.Equal(md.ClaudeSessionIDs, []string{"aaaa-1", "bbbb-2"}) || !md.CostComplete {
		t.Fatalf("stale write: ids=%v complete=%v want [aaaa-1 bbbb-2] true", md.ClaudeSessionIDs, md.CostComplete)
	}
	// An incomplete record is never lifted back to complete by a later write.
	sc.setClaudeSessionID("s1", "bbbb-2", []string{"aaaa-1", "bbbb-2"}, false)
	sc.setClaudeSessionID("s1", "bbbb-2", []string{"aaaa-1", "bbbb-2"}, true)
	if sc.all()["s1"].CostComplete {
		t.Fatal("a later write lifted cost_complete back to true")
	}
}

// Many concurrent notes must leave the sidecar equal to the in-memory history
// (run with -race): per-session ordering of snapshot+write.
func TestConcurrentNotesPersistTheWholeHistory(t *testing.T) {
	m, s, path := spawnClaude(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.noteClaudeSessionID(s, s.ID, fmt.Sprintf("conv-%d", i))
		}()
	}
	wg.Wait()
	ids, complete := s.conversations()
	md := sidecarEntry(t, path, s.ID)
	if len(ids) != 40 || !complete || !slices.Equal(md.ClaudeSessionIDs, ids) || !md.CostComplete || md.ClaudeSessionID != claudeSIDOf(s) {
		t.Fatalf("sidecar diverged: mem %d ids complete=%v, disk %d ids complete=%v sid=%q/%q",
			len(ids), complete, len(md.ClaudeSessionIDs), md.CostComplete, md.ClaudeSessionID, claudeSIDOf(s))
	}
}
