package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type pidBackend struct {
	*fakeBackend
	pids map[string]int
}

func (b pidBackend) InnerPid(id string) (int, bool) { p, ok := b.pids[id]; return p, ok }

func writeReg(t *testing.T, home string, pid int, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func regJSON(pid int, sid, procStart, status string) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/work/proj","procStart":%q,"status":%q,"version":"2.1.284"}`,
		pid, sid, procStart, status)
}

// writeTx writes a one-record transcript whose text is `text` and whose model
// is "model-<sid>", then pins its mtime.
func writeTx(t *testing.T, home, cwd, sid, text string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	body := fmt.Sprintf(`{"message":{"role":"assistant","model":%q,"content":[{"type":"text","text":%q}],"usage":{"input_tokens":1}}}`+"\n",
		"model-"+sid, text)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

const locCwd = "/work/proj"

// newLocatorManager: two prompted claude sessions in ONE cwd (the misattribution
// case), pids 101/202 with procStart 5000/6000. Transcripts A (s1) and B (s2);
// B is newer, so the old heuristic hands B to both.
func newLocatorManager(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	m := &Manager{
		home:     home,
		sessions: map[string]*Session{},
		backend:  pidBackend{newFakeBackend(), map[string]int{"s1": 101, "s2": 202}},
		procStart: func(pid int) (string, bool) {
			v, ok := map[int]string{101: "5000", 202: "6000"}[pid]
			return v, ok
		},
	}
	m.SetRepliesEnabled(true) // the reply tests exercise the feature; the off switch has its own tests
	for _, id := range []string{"s1", "s2"} {
		m.sessions[id] = &Session{ID: id, Agent: "claude", Cwd: locCwd, Bin: "/usr/bin/claude",
			Created: time.Now().Add(-time.Hour), agentKind: true, submitted: true}
	}
	now := time.Now()
	writeTx(t, home, locCwd, "aaaa-1", "from A", now.Add(-10*time.Minute))
	writeTx(t, home, locCwd, "bbbb-2", "from B", now.Add(-5*time.Minute))
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "idle"))
	writeReg(t, home, 202, regJSON(202, "bbbb-2", "6000", "idle"))
	return m, home
}

// wantHeuristicButUnbound pins both halves of #21 for a session with no
// validated registry entry and no persisted conversation id: locate's cwd
// heuristic still names wantBase (other callers rely on it), but peek, usage
// and attribution refuse to read it because it is not proven to be this
// session's.
func wantHeuristicButUnbound(t *testing.T, m *Manager, id, wantBase string) {
	t.Helper()
	if got := filepath.Base(m.locate(id).path); got != wantBase {
		t.Errorf("locate(%s) heuristic path=%q want %q", id, got, wantBase)
	}
	if p, ok := m.PeekFor(id); ok {
		t.Errorf("PeekFor(%s) read an unbound transcript: %q", id, p.Text)
	}
	if u, ok := m.UsageFor(id); ok {
		t.Errorf("UsageFor(%s) read an unbound transcript: %+v", id, u)
	}
	if _, ok := m.AttributionFor(id); ok {
		t.Errorf("AttributionFor(%s) read an unbound transcript", id)
	}
}

func peekText(t *testing.T, m *Manager, id string) string {
	t.Helper()
	p, ok := m.PeekFor(id)
	if !ok {
		t.Fatalf("PeekFor(%s) missed", id)
	}
	return p.Text
}

func TestLocateUsesRegistryOverNewerSibling(t *testing.T) {
	m, _ := newLocatorManager(t)
	if got := peekText(t, m, "s1"); got != "from A" {
		t.Errorf("s1 peek=%q want its own %q (not the newer sibling's)", got, "from A")
	}
	if got := peekText(t, m, "s2"); got != "from B" {
		t.Errorf("s2 peek=%q want %q", got, "from B")
	}
	if u, ok := m.UsageFor("s1"); !ok || u.Model != "model-aaaa-1" {
		t.Errorf("s1 usage model=%q ok=%v want model-aaaa-1", u.Model, ok)
	}
}

func TestLocateFollowsClear(t *testing.T) {
	m, home := newLocatorManager(t)
	_ = peekText(t, m, "s1") // prime the cache on A
	writeTx(t, home, locCwd, "cccc-3", "from C", time.Now().Add(-20*time.Minute))
	writeReg(t, home, 101, regJSON(101, "cccc-3", "5000", "idle")) // what /clear does
	if got := peekText(t, m, "s1"); got != "from C" {
		t.Errorf("after /clear s1 peek=%q want %q", got, "from C")
	}
}

func TestLocateFallsBack(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"procStart mismatch (pid reused)", regJSON(101, "aaaa-1", "9999", "idle")},
		{"malformed JSON", `{not json`},
		{"pid field disagrees with the file name", regJSON(999, "aaaa-1", "5000", "idle")},
		{"fields renamed by a future version", `{"id":101,"session":"aaaa-1","start":"5000"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, home := newLocatorManager(t)
			writeReg(t, home, 101, c.body)
			// locate falls back to the cwd+mtime heuristic = the newest
			// transcript (B), but nothing reads it: s1 is unbound.
			wantHeuristicButUnbound(t, m, "s1", "bbbb-2.jsonl")
		})
	}
}

// TestLocateMissingTranscriptDoesNotFallBack: once a registry entry VALIDATES,
// it is trusted outright. If its named transcript does not exist, that must
// read as "no transcript" for s1 — never as a fallback to a sibling's file
// (the exact misattribution this locator exists to fix).
func TestLocateMissingTranscriptDoesNotFallBack(t *testing.T) {
	m, home := newLocatorManager(t)
	writeReg(t, home, 101, regJSON(101, "gone-9", "5000", "idle"))
	if _, ok := m.PeekFor("s1"); ok {
		t.Error("PeekFor ok=true though the registry-named transcript does not exist")
	}
	if got := peekText(t, m, "s2"); got != "from B" {
		t.Errorf("s2 peek=%q want %q (must be unaffected by s1's missing transcript)", got, "from B")
	}
}

// TestLocateExitedSessionIgnoresRegistry: abduco's pidfile `inner=` pid is the
// agent's LAST known pid, never updated after exit — so for an exited session
// it can name a pid since reused by an unrelated claude. The registry step
// must be skipped entirely once a session is known exited.
func TestLocateExitedSessionIgnoresRegistry(t *testing.T) {
	m, _ := newLocatorManager(t)
	m.sessions["s1"].mu.Lock()
	m.sessions["s1"].status = statusExited
	m.sessions["s1"].mu.Unlock()
	if l := m.locate("s1"); l.reg != nil {
		t.Errorf("exited session used the registry; reg=%+v", l.reg)
	}
}

// TestLocateRejectsCwdMismatch: a registry entry whose cwd disagrees with the
// session's own cwd must never be trusted, even with a matching pid+procStart —
// this is the second, independent guard against a reused pid.
func TestLocateRejectsCwdMismatch(t *testing.T) {
	m, home := newLocatorManager(t)
	body := `{"pid":101,"sessionId":"aaaa-1","cwd":"/some/other/dir","procStart":"5000","status":"idle"}`
	writeReg(t, home, 101, body)
	if l := m.locate("s1"); l.reg != nil {
		t.Errorf("cwd mismatch accepted; reg=%+v", l.reg)
	}
	wantHeuristicButUnbound(t, m, "s1", "bbbb-2.jsonl")
}

// TestLocateRejectsSymlinkRegistryFile: a registry entry reached through a
// symlink must never be trusted — the registry directory is untrusted input,
// and a planted symlink could point anywhere readable by the daemon.
func TestLocateRejectsSymlinkRegistryFile(t *testing.T) {
	m, home := newLocatorManager(t)
	real := filepath.Join(home, ".claude", "elsewhere.json")
	if err := os.WriteFile(real, []byte(regJSON(101, "aaaa-1", "5000", "idle")), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".claude", "sessions", "101.json")
	_ = os.Remove(target)
	if err := os.Symlink(real, target); err != nil {
		t.Fatal(err)
	}
	if l := m.locate("s1"); l.reg != nil {
		t.Error("symlinked registry file accepted")
	}
	wantHeuristicButUnbound(t, m, "s1", "bbbb-2.jsonl") // symlink must be ignored
}

// TestLocateRejectsOversizedRegistryFile: a registry file over registryMaxBytes
// must be rejected outright, not silently truncated and parsed.
func TestLocateRejectsOversizedRegistryFile(t *testing.T) {
	m, home := newLocatorManager(t)
	big := strings.Repeat("a", 70<<10)
	body := fmt.Sprintf(`{"pid":101,"sessionId":"aaaa-1","cwd":"/work/proj","procStart":"5000","status":"idle","junk":%q}`, big)
	writeReg(t, home, 101, body)
	if l := m.locate("s1"); l.reg != nil {
		t.Error("oversized registry file accepted")
	}
	wantHeuristicButUnbound(t, m, "s1", "bbbb-2.jsonl") // oversized file must be ignored
}

// TestLocateMissingTranscriptBlocksWithoutPeek: a validated "waiting" registry
// entry whose transcript is missing must still report blocked=true even though
// PeekFor cannot read a transcript — Task 5's amber indicator must not go dark
// just because the transcript file is momentarily absent.
func TestLocateMissingTranscriptBlocksWithoutPeek(t *testing.T) {
	m, home := newLocatorManager(t)
	writeReg(t, home, 101, regJSON(101, "gone-9", "5000", "waiting"))
	p, ok, blocked := m.peekAndBlocked("s1")
	if ok {
		t.Errorf("peek ok=true for a session whose registry-named transcript does not exist; peek=%+v", p)
	}
	if !blocked {
		t.Error("blocked=false though the registry says waiting")
	}
}

func TestLocateRejectsTraversalSessionID(t *testing.T) {
	for _, sid := range []string{"../../../etc/passwd", "", strings.Repeat("a", 300), "a/b", "a b", "-abcd", "-p"} {
		m, home := newLocatorManager(t)
		writeReg(t, home, 101, regJSON(101, sid, "5000", "idle"))
		if l := m.locate("s1"); l.reg != nil {
			t.Errorf("sessionId %q accepted; a registry id must never be joined into a path", sid)
		}
	}
}

func TestLocateBoxedIgnoresHostRegistry(t *testing.T) {
	m, _ := newLocatorManager(t)
	state := t.TempDir()
	m.stateDir = state
	m.sessions["s1"].Box = "box-1"
	writeTx(t, filepath.Join(state, "boxstate", "box-1"), locCwd, "dddd-4", "from box", time.Now())
	wantHeuristicButUnbound(t, m, "s1", "dddd-4.jsonl") // the box's file, never the host registry's A
}

func TestLocateReportsRegistryStatus(t *testing.T) {
	m, home := newLocatorManager(t)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	if _, _, blocked := m.peekAndBlocked("s1"); !blocked {
		t.Error("blocked=false with registry status waiting")
	}
	if _, _, blocked := m.peekAndBlocked("s2"); blocked {
		t.Error("blocked=true for a session whose registry says idle")
	}
}

func TestProcStartOfSelf(t *testing.T) {
	v, ok := procStartOf(os.Getpid())
	if !ok || v == "" {
		t.Fatalf("procStartOf(self)=%q,%v want a non-empty start time", v, ok)
	}
}

func TestUsageForReportsTheAgentsLiveCwd(t *testing.T) {
	m, home := newLocatorManager(t)
	_, wt, _ := mkCheckouts(t)
	writeTxLines(t, home, locCwd, "aaaa-1", time.Now(),
		`{"cwd":"`+wt+`/sub","message":{"role":"assistant","model":"m","usage":{"input_tokens":1}}}`)
	if u, ok := m.UsageFor("s1"); !ok || u.Cwd != wt {
		t.Fatalf("usage=%+v ok=%v want Cwd %q", u, ok, wt)
	}
}

func regJSONCwd(pid int, sid, procStart, cwd string) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"procStart":%q,"status":"idle","version":"2.1.286"}`,
		pid, sid, cwd, procStart)
}

// newWorktreeLocator: one claude session spawned in a real main checkout whose
// registry entry, as Claude Code 2.1.286 writes it after EnterWorktree, names
// the worktree as its cwd.
func newWorktreeLocator(t *testing.T) (m *Manager, home, main, wt string) {
	t.Helper()
	home = t.TempDir()
	main, wt, _ = mkCheckouts(t)
	m = &Manager{
		home:      home,
		sessions:  map[string]*Session{},
		backend:   pidBackend{newFakeBackend(), map[string]int{"s1": 101}},
		procStart: func(pid int) (string, bool) { return "5000", pid == 101 },
	}
	m.sessions["s1"] = &Session{ID: "s1", Agent: "claude", Cwd: main, Bin: "/usr/bin/claude",
		Created: time.Now().Add(-time.Hour), agentKind: true, submitted: true}
	writeReg(t, home, 101, regJSONCwd(101, "aaaa-1", "5000", wt))
	return m, home, main, wt
}

// 2.1.286 moves the whole transcript to the worktree's project dir on
// EnterWorktree. The session's own file must be found there by its sessionId —
// never a newer sibling left in the spawn dir by the mtime heuristic.
func TestLocateFollowsATranscriptMovedIntoAWorktree(t *testing.T) {
	m, home, main, wt := newWorktreeLocator(t)
	writeTxLines(t, home, wt, "aaaa-1", time.Now().Add(-time.Minute),
		`{"cwd":"`+wt+`","message":{"role":"assistant","model":"model-moved","usage":{"input_tokens":1}}}`)
	writeTx(t, home, main, "zzzz-9", "sibling", time.Now())
	u, ok := m.UsageFor("s1")
	if !ok || u.Model != "model-moved" || u.Cwd != wt {
		t.Fatalf("usage=%+v ok=%v want the moved transcript (model-moved, cwd %q)", u, ok, wt)
	}
}

// Mixed versions: the registry already names the worktree but the transcript
// is still in the spawn dir (≤2.1.285 layout).
func TestLocateFindsTheSpawnDirTranscriptWhenTheRegistryNamesAWorktree(t *testing.T) {
	m, home, main, _ := newWorktreeLocator(t)
	writeTxLines(t, home, main, "aaaa-1", time.Now(),
		`{"message":{"role":"assistant","model":"model-stayed","usage":{"input_tokens":1}}}`)
	if u, ok := m.UsageFor("s1"); !ok || u.Model != "model-stayed" {
		t.Fatalf("usage=%+v ok=%v want model-stayed", u, ok)
	}
}

// The cwd guard is the second line against a reused pid (procStart is the
// first). It widens only to a checkout of the SAME repository.
func TestRegistryCwdGuardAcceptsOnlyTheSameRepository(t *testing.T) {
	home := t.TempDir()
	main, wt, _ := mkCheckouts(t)
	_, otherWt, _ := mkCheckouts(t)
	ps := func(pid int) (string, bool) { return "5000", true }
	cases := []struct {
		name, regCwd string
		want         bool
	}{
		{"spawn dir itself", main, true},
		{"worktree of the same repo", wt, true},
		{"subdir of a worktree of the same repo", filepath.Join(wt, "sub"), true},
		{"worktree of another repo", otherWt, false},
		{"unrelated dir", t.TempDir(), false},
	}
	for _, c := range cases {
		writeReg(t, home, 7, regJSONCwd(7, "aaaa-1", "5000", c.regCwd))
		if _, ok := readClaudeRegistry(filepath.Join(home, ".claude"), 7, ps, main); ok != c.want {
			t.Errorf("%s: accepted=%v want %v", c.name, ok, c.want)
		}
	}
}

// Claude Code on macOS writes procStart as a UTC ctime string; procinfo there
// reports "<sec>.<usec>". The pair below was read off a live Mac (TZ +0700):
// the same instant in two formats. Linux keeps its exact compare.
func TestStartMatches(t *testing.T) {
	const real, ours = "Wed Oct 14 08:03:58 2026", "1791965038.541726"
	cases := []struct {
		name, goos, recorded, ours string
		want                       bool
	}{
		{"linux exact", "linux", "123456", "123456", true},
		{"linux differs", "linux", "123456", "123457", false},
		{"linux never parses ctime", "linux", real, ours, false},
		{"darwin measured sample", "darwin", real, ours, true},
		{"darwin usec ignored", "darwin", real, "1791965038.999999", true},
		{"darwin one second later", "darwin", real, "1791965039.000000", false},
		{"darwin one second earlier", "darwin", real, "1791965037.999999", false},
		{"darwin local-time reading rejected", "darwin", real, "1791990238.541726", false},
		{"darwin padded day", "darwin", "Mon Oct  5 01:02:03 2026", "1791162123.000001", true},
		{"darwin unpadded day", "darwin", "Mon Oct 5 01:02:03 2026", "1791162123.000001", true},
		{"darwin wrong weekday", "darwin", "Thu Oct 14 08:03:58 2026", ours, false},
		{"darwin exact still matches", "darwin", ours, ours, true},
		{"darwin no year", "darwin", "Wed Oct 14 08:03:58", ours, false},
		{"darwin empty", "darwin", "", ours, false},
		{"darwin trailing junk", "darwin", real + " x", ours, false},
		{"darwin ours malformed", "darwin", real, "1791965038", false},
		{"darwin ours not a number", "darwin", real, "abc.541726", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := startMatches(c.goos, c.recorded, c.ours); got != c.want {
				t.Fatalf("startMatches(%q, %q, %q)=%v want %v", c.goos, c.recorded, c.ours, got, c.want)
			}
		})
	}
}

// TestLocateFollowsSessionConfigDir: a host session run under an agent account
// has its registry and transcripts under the account's config dir. A newer
// transcript for the same cwd under the default ~/.claude must not be
// attributed to it, via the registry path or the heuristic fallback.
func TestLocateFollowsSessionConfigDir(t *testing.T) {
	m, home := newLocatorManager(t) // decoy: default ~/.claude holds A (s1's registry) and a newer B
	cd := filepath.Join(t.TempDir(), "acct", "claude")
	sessions := filepath.Join(cd, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "101.json"), []byte(regJSON(101, "acct-1", "5000", "idle")), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(cd, "projects", projectSlug(locCwd))
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(proj, "acct-1.jsonl")
	body := `{"message":{"role":"assistant","model":"model-acct-1","content":[{"type":"text","text":"from account"}],"usage":{"input_tokens":1}}}` + "\n"
	if err := os.WriteFile(want, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(want, old, old); err != nil {
		t.Fatal(err)
	}
	// The decoys under <home>/.claude/projects/<slug>/ are newer than the account's transcript.
	writeTx(t, home, locCwd, "decoy-9", "from decoy", time.Now())

	m.sessions["s1"].ConfigDir = cd
	if l := m.locate("s1"); l.path != want || l.reg == nil {
		t.Fatalf("registry path: locate = %q reg=%v, want %q", l.path, l.reg, want)
	}
	if got := peekText(t, m, "s1"); got != "from account" {
		t.Errorf("s1 peek=%q want %q", got, "from account")
	}

	// No account registry entry: the heuristic must still stay inside ConfigDir.
	if err := os.Remove(filepath.Join(sessions, "101.json")); err != nil {
		t.Fatal(err)
	}
	if l := m.locate("s1"); l.reg != nil || l.path != want {
		t.Fatalf("heuristic path: locate = %q reg=%v, want %q", l.path, l.reg, want)
	}
}

// An unbound session (no live registry entry, nothing persisted) must show no
// usage, attribution or summary: the newest-by-cwd heuristic would hand it a
// sibling conversation's data (#21).
func TestUnboundSessionShowsNoSiblingData(t *testing.T) {
	m, home := newLocatorManager(t)
	if err := os.Remove(filepath.Join(home, ".claude", "sessions", "101.json")); err != nil {
		t.Fatal(err)
	}
	if u, ok := m.UsageFor("s1"); ok {
		t.Errorf("UsageFor(unbound s1) ok=true, usage=%+v; want ok=false", u)
	}
	if a, ok := m.AttributionFor("s1"); ok {
		t.Errorf("AttributionFor(unbound s1) ok=true, attribution=%+v; want ok=false", a)
	}
	var got, sib string
	var found bool
	for _, i := range m.List() {
		switch i.ID {
		case "s1":
			got, found = i.Summary, true
		case "s2":
			sib = i.Summary
		}
	}
	if !found {
		t.Fatal("s1 missing from List()")
	}
	if got != "" {
		t.Errorf("unbound s1 Summary=%q want empty (sibling's text must not leak)", got)
	}
	if sib != "from B" {
		t.Errorf("bound s2 Summary=%q want %q", sib, "from B")
	}
}

func TestBoundSessionUnchanged(t *testing.T) {
	m, _ := newLocatorManager(t)
	u, ok := m.UsageFor("s1")
	if !ok || u.Model != "model-aaaa-1" {
		t.Errorf("UsageFor(s1) model=%q ok=%v want model-aaaa-1", u.Model, ok)
	}
	if _, ok := m.AttributionFor("s1"); !ok {
		t.Error("AttributionFor(s1) ok=false for a bound session")
	}
	for _, i := range m.List() {
		if i.ID == "s1" && i.Summary != "from A" {
			t.Errorf("s1 Summary=%q want %q", i.Summary, "from A")
		}
	}
}

// An exited session has no live registry entry; its persisted claudeSessionID
// still binds it to its own <sid>.jsonl, never a newer sibling's.
func TestExitedSessionUsesPersistedSID(t *testing.T) {
	m, home := newLocatorManager(t)
	if err := os.Remove(filepath.Join(home, ".claude", "sessions", "101.json")); err != nil {
		t.Fatal(err)
	}
	s := m.sessions["s1"]
	s.mu.Lock()
	s.status = statusExited
	s.claudeSessionID = "aaaa-1"
	s.mu.Unlock()
	u, ok := m.UsageFor("s1")
	if !ok || u.Model != "model-aaaa-1" {
		t.Errorf("exited s1 usage model=%q ok=%v want its own model-aaaa-1", u.Model, ok)
	}
	if _, ok := m.AttributionFor("s1"); !ok {
		t.Error("exited s1 AttributionFor ok=false; want its own transcript")
	}
}
