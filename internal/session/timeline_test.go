package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/boxread"
)

// tlBase is the fixture clock; tlT(n) is n ms after it as the RFC3339 stamp a
// transcript record carries.
var tlBase = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

func tlT(ms int64) string {
	return tlBase.Add(time.Duration(ms) * time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}
func tlMS(ms int64) int64 { return tlBase.UnixMilli() + ms }

func tlJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func tlUse(t *testing.T, ms int64, id, name string, input map[string]any) string {
	return tlJSON(t, map[string]any{"type": "assistant", "timestamp": tlT(ms), "message": map[string]any{
		"role": "assistant", "id": "msg_" + id,
		"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
}

func tlResult(t *testing.T, ms int64, id string, isErr bool, text string) string {
	return tlJSON(t, map[string]any{"type": "user", "timestamp": tlT(ms), "message": map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isErr, "content": text}}}})
}

func tlYou(t *testing.T, ms int64, text string) string {
	return tlJSON(t, map[string]any{"type": "user", "timestamp": tlT(ms), "message": map[string]any{"role": "user", "content": text}})
}

// tlWrite puts body at the transcript path of sid under the fixture cwd and
// returns it (the home is the locator manager's).
func tlWrite(t *testing.T, home, sid, body string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", projectSlug(locCwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tlAppend(t *testing.T, p, body string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}

func tlGet(t *testing.T, m *Manager, id string) Timeline {
	t.Helper()
	tl, ok := m.TimelineFor(id, 0, 1000)
	if !ok {
		t.Fatalf("TimelineFor(%s): unknown session", id)
	}
	return tl
}

func TestTimelineJoinsToolResult(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlUse(t, 0, "t1", "Bash", map[string]any{"command": "ls -la"})+
		tlResult(t, 1400, "t1", false, "total 0")+
		tlUse(t, 2000, "t2", "Bash", map[string]any{"command": "sleep 99"}))
	tl := tlGet(t, m, "s1")
	if !tl.Claude || len(tl.Events) != 2 {
		t.Fatalf("timeline = %+v", tl)
	}
	if e := tl.Events[1]; e.Tool != "Bash" || e.Detail != "ls -la" || e.State != "ok" || e.MS != 1400 || e.TS != tlMS(0) || e.Kind != "tool" {
		t.Errorf("answered call = %+v", e)
	}
	if e := tl.Events[0]; e.State != "pending" || e.Detail != "sleep 99" {
		t.Errorf("unanswered call = %+v", e)
	}
	if tl.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d", tl.ToolCalls)
	}
}

func TestTimelineBashExitCode(t *testing.T) {
	m, home := newLocatorManager(t)
	const secret = "SECRET-RESULT-BODY"
	tlWrite(t, home, "aaaa-1", tlUse(t, 0, "t1", "Bash", map[string]any{"command": "false"})+
		tlResult(t, 10, "t1", true, "Exit code 254\n"+secret))
	e := tlGet(t, m, "s1").Events[0]
	if e.State != "error" || e.Exit == nil || *e.Exit != 254 {
		t.Fatalf("event = %+v", e)
	}
	if b, _ := json.Marshal(e); strings.Contains(string(b), secret) || strings.Contains(string(b), "Exit code") {
		t.Errorf("result text leaked: %s", b)
	}
}

func TestTimelineEditCounts(t *testing.T) {
	m, home := newLocatorManager(t)
	body := strings.Repeat("line\n", 18)
	tlWrite(t, home, "aaaa-1",
		tlUse(t, 0, "e1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "", "new_string": body})+
			tlResult(t, 1, "e1", false, "ok")+
			tlUse(t, 10, "w1", "Write", map[string]any{"file_path": "/w/b.go", "content": "1\n2\n3"})+
			tlResult(t, 11, "w1", false, "ok")+
			tlUse(t, 20, "m1", "MultiEdit", map[string]any{"file_path": "/w/a.go", "edits": []any{
				map[string]any{"old_string": "a\nb", "new_string": "c"},
				map[string]any{"old_string": "d", "new_string": "e\nf\ng"}}})+
			tlResult(t, 21, "m1", false, "ok")+
			tlUse(t, 30, "e2", "Edit", map[string]any{"file_path": "/w/c.go", "old_string": "x", "new_string": "y"})+
			tlResult(t, 40, "e2", true, "String to replace not found"))
	tl := tlGet(t, m, "s1")
	byTool := map[string]TimelineEvent{}
	for _, e := range tl.Events {
		byTool[e.Tool+e.Detail] = e
	}
	if e := byTool["Edit/w/a.go"]; e.Add == nil || *e.Add != 18 || e.Del == nil || *e.Del != 0 {
		t.Errorf("Edit = %+v", e)
	}
	if e := byTool["Write/w/b.go"]; e.Add == nil || *e.Add != 3 || e.Del == nil || *e.Del != 0 {
		t.Errorf("Write = %+v", e)
	}
	if e := byTool["MultiEdit/w/a.go"]; e.Add == nil || *e.Add != 4 || e.Del == nil || *e.Del != 3 {
		t.Errorf("MultiEdit = %+v", e)
	}
	got := map[string]FileChange{}
	for _, f := range tl.Files {
		if _, dup := got[f.Path]; dup {
			t.Errorf("%s listed twice", f.Path)
		}
		got[f.Path] = f
	}
	if f := got["/w/a.go"]; f.Add != 22 || f.Del != 3 {
		t.Errorf("a.go total = %+v, want +22 -3", f)
	}
	if f := got["/w/b.go"]; f.Add != 3 || f.Del != 0 {
		t.Errorf("b.go total = %+v", f)
	}
	if _, listed := got["/w/c.go"]; listed {
		t.Errorf("a failed edit must not list its file: %+v", tl.Files)
	}
	if len(tl.Files) != 2 || tl.Files[0].Path != "/w/a.go" {
		t.Errorf("Files not most-recent first: %+v", tl.Files)
	}
}

// A file counts as changed only once its edit has an ok result: an edit still
// waiting on a permission prompt, a denied one and a failed one changed
// nothing, so none of them is listed (and none moves summary.files_changed).
func TestTimelineFilesCountOnlyAppliedEdits(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1",
		tlUse(t, 0, "p1", "Edit", map[string]any{"file_path": "/w/pending.go", "old_string": "a", "new_string": "b"})+
			tlUse(t, 10, "d1", "Write", map[string]any{"file_path": "/w/denied.go", "content": "x\n"})+
			tlResult(t, 11, "d1", true, "The user doesn't want to proceed with this tool use."+" "+"[Request interrupted]")+
			tlUse(t, 20, "n1", "NotebookEdit", map[string]any{"notebook_path": "/w/nb.ipynb"})+
			tlUse(t, 30, "n2", "NotebookEdit", map[string]any{"notebook_path": "/w/nb2.ipynb"})+
			tlResult(t, 31, "n2", false, "ok"))
	tl := tlGet(t, m, "s1")
	if len(tl.Files) != 1 || tl.Files[0].Path != "/w/nb2.ipynb" || tl.Files[0].Add != 0 || tl.Files[0].Del != 0 {
		t.Fatalf("Files = %+v, want only the applied notebook edit", tl.Files)
	}
	if tl.ToolCalls != 4 {
		t.Errorf("ToolCalls = %d: every call still counts, only Files is gated", tl.ToolCalls)
	}
	// The pending edit's result arrives later (the user allowed it).
	p := m.timelinePath("s1")
	if p == "" {
		t.Fatal("no bound path")
	}
	tlAppend(t, p, tlResult(t, 50, "p1", false, "ok"))
	tl = tlGet(t, m, "s1")
	if len(tl.Files) != 2 || tl.Files[0].Path != "/w/pending.go" {
		t.Errorf("after the ok result: %+v", tl.Files)
	}
}

func TestTimelineDetailRules(t *testing.T) {
	m, home := newLocatorManager(t)
	long := strings.Repeat("é", 1000)
	tok := "ghp_" + strings.Repeat("a", 36)
	const body = "BODY-NEVER-SHOWN"
	tlWrite(t, home, "aaaa-1",
		tlUse(t, 0, "1", "Bash", map[string]any{"command": long})+
			tlUse(t, 1, "2", "Bash", map[string]any{"command": "curl -H " + tok})+
			tlUse(t, 2, "3", "Read", map[string]any{"file_path": "/w/r.txt"})+
			tlUse(t, 3, "4", "Edit", map[string]any{"file_path": "/w/e.txt", "old_string": body, "new_string": body})+
			tlUse(t, 4, "5", "Write", map[string]any{"file_path": "/w/w.txt", "content": body})+
			tlUse(t, 5, "6", "WebFetch", map[string]any{"url": "https://x.test/p", "prompt": body})+
			tlUse(t, 6, "7", "WebSearch", map[string]any{"query": "go json"})+
			tlUse(t, 7, "8", "Grep", map[string]any{"pattern": "foo.*bar", "path": "/secret"})+
			tlUse(t, 8, "9", "Glob", map[string]any{"pattern": "**/*.go"})+
			tlUse(t, 9, "10", "Task", map[string]any{"description": "audit", "prompt": body})+
			tlUse(t, 10, "11", "mcp__aws__list", map[string]any{"region": "x", "prompt": body})+
			tlUse(t, 11, "12", "TodoWrite", map[string]any{"todos": body}))
	tl := tlGet(t, m, "s1")
	want := map[string]string{"Bash": "", "Read": "/w/r.txt", "Edit": "/w/e.txt", "Write": "/w/w.txt", "WebFetch": "https://x.test/p",
		"WebSearch": "go json", "Grep": "foo.*bar", "Glob": "**/*.go", "Task": "audit", "mcp__aws__list": "", "TodoWrite": ""}
	for _, e := range tl.Events {
		if e.Tool == "Bash" {
			continue
		}
		if e.Detail != want[e.Tool] {
			t.Errorf("%s detail = %q, want %q", e.Tool, e.Detail, want[e.Tool])
		}
	}
	// events are newest first: Bash(token) is the second to last, Bash(long) the last
	if d := tl.Events[len(tl.Events)-2].Detail; d != "curl -H [masked:github-token]" {
		t.Errorf("token detail = %q", d)
	}
	d := tl.Events[len(tl.Events)-1].Detail
	if r := []rune(d); len(r) != 300 || r[299] != '…' {
		t.Errorf("long detail = %d runes, last %q", len(r), r[len(r)-1])
	}
	b, _ := json.Marshal(tl)
	if bytes.Contains(b, []byte(body)) || bytes.Contains(b, []byte("/secret")) {
		t.Errorf("body text leaked: %s", b)
	}
}

func TestTimelineUserTurns(t *testing.T) {
	m, home := newLocatorManager(t)
	meta := tlJSON(t, map[string]any{"type": "user", "isMeta": true, "timestamp": tlT(2), "message": map[string]any{"role": "user", "content": "caveat"}})
	side := tlJSON(t, map[string]any{"type": "user", "isSidechain": true, "timestamp": tlT(3), "message": map[string]any{"role": "user", "content": "subagent prompt"}})
	blocks := tlJSON(t, map[string]any{"type": "user", "timestamp": tlT(5), "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "from blocks"}}}})
	tlWrite(t, home, "aaaa-1",
		tlYou(t, 0, "fix the build "+strings.Repeat("z", 400))+
			tlResult(t, 1, "zz", false, "orphan")+meta+side+
			tlYou(t, 4, "<command-name>/clear</command-name>\n<command-message>clear</command-message>")+blocks+
			tlJSON(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "no timestamp"}}))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 2 {
		t.Fatalf("events = %+v", tl.Events)
	}
	if e := tl.Events[0]; e.Kind != "you" || e.Detail != "from blocks" || e.Tool != "" {
		t.Errorf("newest = %+v", e)
	}
	if e := tl.Events[1]; e.Kind != "you" || len([]rune(e.Detail)) != 300 || !strings.HasPrefix(e.Detail, "fix the build") {
		t.Errorf("oldest = %+v", e)
	}
}

func TestTimelineIncremental(t *testing.T) {
	m, home := newLocatorManager(t)
	p := tlWrite(t, home, "aaaa-1", tlUse(t, 0, "t1", "Bash", map[string]any{"command": "one"})+tlYou(t, 10, "hi"))
	if n := len(tlGet(t, m, "s1").Events); n != 2 {
		t.Fatalf("first parse = %d", n)
	}
	st := m.timelineFor("s1")
	fi, _ := os.Stat(p)
	if st.offset != fi.Size() {
		t.Fatalf("offset %d, size %d", st.offset, fi.Size())
	}
	before := st.offset
	tlAppend(t, p, tlResult(t, 20, "t1", false, "ok")+tlUse(t, 30, "t2", "Bash", map[string]any{"command": "two"}))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 3 || tl.Events[0].Detail != "two" || tl.Events[2].State != "ok" || tl.Events[2].MS != 20 {
		t.Fatalf("after append = %+v", tl.Events)
	}
	if st.offset <= before {
		t.Errorf("offset did not advance: %d -> %d", before, st.offset)
	}
	// An unterminated line is left for the next pass, not consumed.
	half := tlUse(t, 40, "t3", "Bash", map[string]any{"command": "three"})
	tlAppend(t, p, half[:len(half)-1])
	if n := len(tlGet(t, m, "s1").Events); n != 3 {
		t.Fatalf("partial line parsed: %d events", n)
	}
	tlAppend(t, p, "\n")
	if n := len(tlGet(t, m, "s1").Events); n != 4 {
		t.Fatalf("completed line not parsed: %d events", n)
	}
	// A truncated file starts over rather than duplicating.
	if err := os.WriteFile(p, []byte(tlYou(t, 50, "fresh")), 0o600); err != nil {
		t.Fatal(err)
	}
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 1 || tl.Events[0].Detail != "fresh" {
		t.Fatalf("after truncate = %+v", tl.Events)
	}
}

func TestTimelineClearSwitchesFile(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlYou(t, 0, "before clear"))
	if n := len(tlGet(t, m, "s1").Events); n != 1 {
		t.Fatalf("events = %d", n)
	}
	tlWrite(t, home, "cccc-3", tlYou(t, 5000, "after clear"))
	writeReg(t, home, 101, regJSON(101, "cccc-3", "5000", "idle"))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 3 {
		t.Fatalf("events = %+v", tl.Events)
	}
	if tl.Events[0].Detail != "after clear" || tl.Events[1].Kind != "clear" || tl.Events[2].Detail != "before clear" {
		t.Errorf("order = %+v", tl.Events)
	}
	if ts := tl.Events[1].TS; ts < tl.Events[2].TS || ts > tl.Events[0].TS {
		t.Errorf("divider ts %d not between %d and %d", ts, tl.Events[2].TS, tl.Events[0].TS)
	}
}

// tlFiller writes n bytes of newline-terminated junk lines (each < 1 MiB).
func tlFiller(t *testing.T, f *os.File, n int) {
	t.Helper()
	line := bytes.Repeat([]byte("j"), 1<<20-1)
	line = append(line, '\n')
	for n > 0 {
		c := min(n, len(line))
		if c < len(line) {
			chunk := bytes.Repeat([]byte("j"), c)
			chunk[c-1] = '\n'
			if _, err := f.Write(chunk); err != nil {
				t.Fatal(err)
			}
			return
		}
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
		n -= c
	}
}

func TestTimelineBoundedScan(t *testing.T) {
	m, home := newLocatorManager(t)
	e0 := tlUse(t, 0, "t0", "Bash", map[string]any{"command": "too old"})
	e1 := tlUse(t, 1, "t1", "Bash", map[string]any{"command": "first of the window"})
	e2 := tlUse(t, 2, "t2", "Bash", map[string]any{"command": "last"})
	// [e0][junk][e1][junk][e2] with the last 64 MiB starting exactly at e1.
	window := timelineMaxScanBytes
	rest := window - len(e1) - len(e2)
	p := tlWrite(t, home, "aaaa-1", e0)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	tlFiller(t, f, 5<<20)
	f.WriteString(e1)
	tlFiller(t, f, rest)
	f.WriteString(e2)
	f.Close()

	tl := tlGet(t, m, "s1")
	if !tl.Partial {
		t.Error("Partial not set for a file past the scan cap")
	}
	var got []string
	for _, e := range tl.Events {
		got = append(got, e.Detail)
	}
	// e0 is outside the window; e1 is where the window lands, i.e. the first
	// (possibly partial) line, which is dropped.
	if len(got) != 1 || got[0] != "last" {
		t.Errorf("events = %v, want only [last]", got)
	}
	if st := m.timelineFor("s1"); st.offset != int64(5<<20+len(e0)+len(e1)+rest+len(e2)) {
		t.Errorf("offset = %d", st.offset)
	}
}

func TestTimelineRingBound(t *testing.T) {
	m, home := newLocatorManager(t)
	var sb strings.Builder
	for i := range timelineMaxEvents + 500 {
		sb.WriteString(tlYou(t, int64(i), fmt.Sprintf("msg %d", i)))
	}
	tlWrite(t, home, "aaaa-1", sb.String())
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != timelineMaxEvents || !tl.Partial {
		t.Fatalf("events=%d partial=%v", len(tl.Events), tl.Partial)
	}
	if tl.Events[0].Detail != "msg 1499" || tl.Events[999].Detail != "msg 500" {
		t.Errorf("window = %q .. %q", tl.Events[0].Detail, tl.Events[999].Detail)
	}
	// Past the compaction point (2x) the state itself stays bounded too.
	for i := range 3 * timelineMaxEvents {
		sb.Reset()
		sb.WriteString(tlYou(t, int64(2000+i), "x"))
		tlAppend(t, filepath.Join(home, ".claude", "projects", projectSlug(locCwd), "aaaa-1.jsonl"), sb.String())
		if i%500 == 0 {
			tlGet(t, m, "s1")
		}
	}
	tlGet(t, m, "s1")
	if n := len(m.timelineFor("s1").evs); n >= 2*timelineMaxEvents {
		t.Errorf("state holds %d events", n)
	}
}

func TestTimelinePendingBound(t *testing.T) {
	m, home := newLocatorManager(t)
	var sb strings.Builder
	for i := range timelinePendingMax + 50 {
		sb.WriteString(tlUse(t, int64(i), fmt.Sprintf("p%d", i), "Bash", map[string]any{"command": "x"}))
	}
	tlWrite(t, home, "aaaa-1", sb.String())
	tlGet(t, m, "s1")
	if n := len(m.timelineFor("s1").pending); n != timelinePendingMax {
		t.Errorf("pending = %d, want %d", n, timelinePendingMax)
	}
}

func TestTimelineSkipsOverlongLine(t *testing.T) {
	m, home := newLocatorManager(t)
	over := `{"timestamp":"` + tlT(0) + `","pad":"` + strings.Repeat("a", boxread.MaxLineBytes) + "\"}\n"
	p := tlWrite(t, home, "aaaa-1", "not json at all\n"+over+tlYou(t, 5, "after the big one"))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 1 || tl.Events[0].Detail != "after the big one" {
		t.Fatalf("events = %+v", tl.Events)
	}
	// An overlong line still being written is consumed, not re-read forever.
	tlAppend(t, p, `{"pad":"`+strings.Repeat("b", boxread.MaxLineBytes))
	tlGet(t, m, "s1")
	st := m.timelineFor("s1")
	fi, _ := os.Stat(p)
	if st.offset != fi.Size() || !st.skip {
		t.Errorf("offset=%d size=%d skip=%v", st.offset, fi.Size(), st.skip)
	}
	tlAppend(t, p, "tail of it\n"+tlYou(t, 9, "next"))
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 2 || tl.Events[0].Detail != "next" {
		t.Errorf("after the discarded line ended: %+v", tl.Events)
	}
}

// tlBoxed turns s1 into a boxed session whose box home is bd, exited so the
// live registry is not consulted.
func tlBoxed(t *testing.T) (m *Manager, bd string) {
	t.Helper()
	m, _ = newLocatorManager(t)
	m.stateDir = t.TempDir()
	bd = filepath.Join(m.stateDir, "boxstate", "box-1")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	s := m.sessions["s1"]
	s.Box = "box-1"
	s.status = statusExited
	s.claudeSessionID = "aaaa-1"
	return m, bd
}

func TestTimelineBoxSymlinkRefused(t *testing.T) {
	m, bd := tlBoxed(t)
	outside := t.TempDir()
	tlWrite(t, outside, "aaaa-1", tlYou(t, 0, "host secret"))
	if err := os.Symlink(filepath.Join(outside, ".claude"), filepath.Join(bd, ".claude")); err != nil {
		t.Fatal(err)
	}
	tl := tlGet(t, m, "s1")
	if !tl.Claude || len(tl.Events) != 0 {
		t.Errorf("symlinked .claude read: %+v", tl)
	}

	// A symlinked transcript, even one staying inside the box, is refused too.
	if err := os.Remove(filepath.Join(bd, ".claude")); err != nil {
		t.Fatal(err)
	}
	real := tlWrite(t, bd, "real", tlYou(t, 0, "linked"))
	if err := os.Symlink(real, filepath.Join(filepath.Dir(real), "aaaa-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 0 {
		t.Errorf("symlinked transcript read: %+v", tl.Events)
	}

	// Control: the same box with a regular file reads fine.
	if err := os.Remove(filepath.Join(filepath.Dir(real), "aaaa-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	tlWrite(t, bd, "aaaa-1", tlYou(t, 0, "plain"))
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 1 || tl.Events[0].Detail != "plain" {
		t.Errorf("regular box transcript: %+v", tl.Events)
	}
}

func TestBoundTranscriptNamesNoPathUnvalidated(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlYou(t, 0, "from A"))
	tlWrite(t, home, "bbbb-2", tlYou(t, 0, "from B"))
	newer := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(home, ".claude", "projects", projectSlug(locCwd), "bbbb-2.jsonl"), newer, newer); err != nil {
		t.Fatal(err)
	}
	s := m.sessions["s1"]
	s.status = statusExited // exited: no registry, and a newer sibling's file sits in the dir

	if l := m.locate("s1"); l.path != "" {
		t.Fatalf("locate must not pick a file by cwd+mtime, got %q", l.path)
	}
	if _, ok := m.boundTranscript("s1"); ok {
		t.Error("bound without a claudeSessionID")
	}
	tl := tlGet(t, m, "s1")
	if !tl.Claude || len(tl.Events) != 0 {
		t.Errorf("heuristic leaked: %+v", tl)
	}

	s.claudeSessionID = "aaaa-1"
	l, ok := m.boundTranscript("s1")
	want := filepath.Join(home, ".claude", "projects", projectSlug(locCwd), "aaaa-1.jsonl")
	if !ok || l.path != want {
		t.Errorf("bound = %q %v, want %q", l.path, ok, want)
	}
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 1 || tl.Events[0].Detail != "from A" {
		t.Errorf("events = %+v", tl.Events)
	}

	s.claudeSessionID = "../../etc/passwd"
	if _, ok := m.boundTranscript("s1"); ok {
		t.Error("an unvalidated sid was bound")
	}
}

func TestBoundTranscriptLiveRegistryMissingFile(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "bbbb-2", tlYou(t, 0, "sibling")) // s1's own file (aaaa-1) is gone
	if err := os.Remove(filepath.Join(home, ".claude", "projects", projectSlug(locCwd), "aaaa-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.boundTranscript("s1"); ok {
		t.Error("validated registry with no file must not be bound to a sibling")
	}
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 0 {
		t.Errorf("events = %+v", tl.Events)
	}
}

func TestTimelineNotClaudeAndUnknown(t *testing.T) {
	m, _ := newLocatorManager(t)
	m.sessions["s1"].Bin = "/bin/bash"
	if tl, ok := m.TimelineFor("s1", 0, 10); !ok || tl.Claude || len(tl.Events) != 0 {
		t.Errorf("non-claude = %+v %v", tl, ok)
	}
	if _, ok := m.TimelineFor("nope", 0, 10); ok {
		t.Error("unknown session reported known")
	}
}

func TestTimelineWaitingSince(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlYou(t, 0, "hi"))
	regPath := filepath.Join(home, ".claude", "sessions", "101.json")
	at := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "waiting"))
	if err := os.Chtimes(regPath, at, at); err != nil {
		t.Fatal(err)
	}
	if got := tlGet(t, m, "s1").WaitingSince; got != at.UnixMilli() {
		t.Errorf("WaitingSince = %d, want %d", got, at.UnixMilli())
	}
	writeReg(t, home, 101, regJSON(101, "aaaa-1", "5000", "idle"))
	if got := tlGet(t, m, "s1").WaitingSince; got != 0 {
		t.Errorf("idle WaitingSince = %d", got)
	}
}

func TestTimelinePaging(t *testing.T) {
	m, home := newLocatorManager(t)
	var sb strings.Builder
	for i := range 250 {
		sb.WriteString(tlYou(t, int64(i+1), fmt.Sprintf("m%d", i)))
	}
	tlWrite(t, home, "aaaa-1", sb.String())
	p1, _ := m.TimelineFor("s1", 0, 100)
	if len(p1.Events) != 100 || !p1.More || p1.Events[0].Detail != "m249" || p1.Events[99].Detail != "m150" {
		t.Fatalf("page 1: %d events more=%v", len(p1.Events), p1.More)
	}
	p2, _ := m.TimelineFor("s1", p1.Events[99].TS, 100)
	if len(p2.Events) != 100 || !p2.More || p2.Events[0].Detail != "m149" || p2.Events[99].Detail != "m50" {
		t.Fatalf("page 2: %d events more=%v first=%q", len(p2.Events), p2.More, p2.Events[0].Detail)
	}
	p3, _ := m.TimelineFor("s1", p2.Events[99].TS, 100)
	if len(p3.Events) != 50 || p3.More {
		t.Fatalf("page 3: %d events more=%v", len(p3.Events), p3.More)
	}
}

func TestTimelineForgottenWithSession(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlYou(t, 0, "hi"))
	tlGet(t, m, "s1")
	m.timelineForget("s1")
	if _, ok := m.timelines["s1"]; ok {
		t.Error("state survived timelineForget")
	}
}

func TestTimelineIgnoresUnboundedToolIDs(t *testing.T) {
	m, home := newLocatorManager(t)
	huge := strings.Repeat("a", 1<<20)
	tlWrite(t, home, "aaaa-1",
		tlUse(t, 0, huge, "Bash", map[string]any{"command": "bomb"})+
			tlUse(t, 1, "has space", "Bash", map[string]any{"command": "bad charset"})+
			tlUse(t, 2, "", "Bash", map[string]any{"command": "no id"})+
			tlUse(t, 3, "toolu_ok-1", "Bash", map[string]any{"command": "fine"})+
			tlResult(t, 4, huge, false, "x")+
			tlResult(t, 5, "toolu_ok-1", false, "x"))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 1 || tl.Events[0].Detail != "fine" || tl.Events[0].State != "ok" || tl.Events[0].MS != 2 {
		t.Fatalf("events = %+v", tl.Events)
	}
	st := m.timelineFor("s1")
	if n := len(st.pending) + st.order.Len(); n != 0 {
		t.Errorf("pending holds %d entries", n)
	}
	// A hostile id that is merely unanswered is not remembered either.
	tlAppend(t, st.path, tlUse(t, 6, huge, "Bash", map[string]any{"command": "bomb2"}))
	tlGet(t, m, "s1")
	if len(st.pending) != 0 {
		t.Errorf("pending grew on a 1 MiB id: %d", len(st.pending))
	}
}

// tlUses is one assistant record carrying several tool_use blocks (parallel
// calls share one timestamp).
func tlUses(t *testing.T, ms int64, ids ...string) string {
	var bs []any
	for _, id := range ids {
		bs = append(bs, map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "c-" + id}})
	}
	return tlJSON(t, map[string]any{"type": "assistant", "timestamp": tlT(ms), "message": map[string]any{"role": "assistant", "content": bs}})
}

func TestTimelinePagingKeepsSameMillisecondGroupWhole(t *testing.T) {
	m, home := newLocatorManager(t)
	tlWrite(t, home, "aaaa-1", tlYou(t, 1, "old1")+tlYou(t, 2, "old2")+
		tlUses(t, 10, "g1", "g2", "g3", "g4")+tlYou(t, 11, "newest"))
	p1, _ := m.TimelineFor("s1", 0, 3) // newest, then 2 of the 4 same-ms calls
	if len(p1.Events) != 5 || !p1.More {
		t.Fatalf("page 1: %d events more=%v (want the whole ms group: 5)", len(p1.Events), p1.More)
	}
	p2, _ := m.TimelineFor("s1", p1.Events[len(p1.Events)-1].TS, 3)
	if len(p2.Events) != 2 || p2.More || p2.Events[0].Detail != "old2" || p2.Events[1].Detail != "old1" {
		t.Fatalf("page 2: %+v more=%v", p2.Events, p2.More)
	}
	seen := map[string]int{}
	for _, e := range append(p1.Events, p2.Events...) {
		seen[e.Detail]++
	}
	if len(seen) != 7 {
		t.Errorf("lost or duplicated events: %v", seen)
	}
	for d, n := range seen {
		if n != 1 {
			t.Errorf("%s returned %d times", d, n)
		}
	}
}

func TestBoundTranscriptKeepsTheWorktreeFileAfterExit(t *testing.T) {
	m, home, main, wt := newWorktreeLocator(t)
	writeTxLines(t, home, wt, "aaaa-1", time.Now().Add(-time.Minute),
		strings.TrimSuffix(tlYou(t, 0, "in the worktree"), "\n"))
	writeTxLines(t, home, main, "zzzz-9", time.Now(), strings.TrimSuffix(tlYou(t, 0, "sibling"), "\n"))
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 1 || tl.Events[0].Detail != "in the worktree" {
		t.Fatalf("live: %+v", tl.Events)
	}
	// The session exits: its registry entry is gone and locate stops consulting it.
	if err := os.Remove(filepath.Join(home, ".claude", "sessions", "101.json")); err != nil {
		t.Fatal(err)
	}
	m.sessions["s1"].status = statusExited
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 1 || tl.Events[0].Detail != "in the worktree" {
		t.Fatalf("after exit: %+v", tl.Events)
	}
	// A different state path (not this conversation's file) is not reused.
	st := m.timelineFor("s1")
	st.mu.Lock()
	st.path = filepath.Join(home, ".claude", "projects", projectSlug(main), "zzzz-9.jsonl")
	st.mu.Unlock()
	if l, _ := m.boundTranscript("s1"); l.path == st.path {
		t.Error("reused a cached path that is not <sid>.jsonl")
	}
}

// moveTranscript moves sid's transcript from one project dir to another the
// way Claude Code's EnterWorktree/ExitWorktree does (same file name, new dir).
func moveTranscript(t *testing.T, home, fromCwd, toCwd, sid string) {
	t.Helper()
	from := filepath.Join(home, ".claude", "projects", projectSlug(fromCwd), sid+".jsonl")
	toDir := filepath.Join(home, ".claude", "projects", projectSlug(toCwd))
	if err := os.MkdirAll(toDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, filepath.Join(toDir, sid+".jsonl")); err != nil {
		t.Fatal(err)
	}
}

// EnterWorktree moves the SAME conversation's transcript to another project
// dir (and ExitWorktree moves it back): that is not a /clear. Nothing may be
// read twice, no divider appears, and lines appended afterwards still parse.
func TestTimelineWorktreeMoveOfTheSameConversationIsNotAClear(t *testing.T) {
	m, home, main, wt := newWorktreeLocator(t)
	// Registry cwd is the worktree; the file starts in the spawn dir.
	writeTxLines(t, home, main, "aaaa-1", time.Now(),
		strings.TrimSuffix(tlYou(t, 0, "first"), "\n"),
		strings.TrimSuffix(tlUse(t, 10, "e1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "a", "new_string": "b\nc"}), "\n"),
		strings.TrimSuffix(tlResult(t, 20, "e1", false, "ok"), "\n"),
		strings.TrimSuffix(tlUse(t, 30, "b1", "Bash", map[string]any{"command": "sleep 9"}), "\n"))
	check := func(stage string, events, calls int, files int) Timeline {
		t.Helper()
		tl := tlGet(t, m, "s1")
		if len(tl.Events) != events || tl.ToolCalls != calls || len(tl.Files) != files {
			t.Fatalf("%s: events=%d calls=%d files=%d, want %d/%d/%d: %+v", stage, len(tl.Events), tl.ToolCalls, len(tl.Files), events, calls, files, tl.Events)
		}
		for _, e := range tl.Events {
			if e.Kind == "clear" {
				t.Fatalf("%s: a clear divider appeared: %+v", stage, tl.Events)
			}
		}
		return tl
	}
	tl := check("before the move", 3, 2, 1)
	if tl.Events[0].State != "pending" {
		t.Fatalf("in-flight row = %+v", tl.Events[0])
	}
	if tl.Files[0].Add != 2 || tl.Files[0].Del != 1 {
		t.Fatalf("file total = %+v", tl.Files[0])
	}

	moveTranscript(t, home, main, wt, "aaaa-1") // EnterWorktree
	check("after the move (no growth)", 3, 2, 1)
	wtFile := filepath.Join(home, ".claude", "projects", projectSlug(wt), "aaaa-1.jsonl")
	tlAppend(t, wtFile, tlResult(t, 40, "b1", false, "done")+tlUse(t, 50, "b2", "Bash", map[string]any{"command": "ls"}))
	tl = check("after growth in the worktree", 4, 3, 1)
	if tl.Events[1].State != "ok" || tl.Events[0].State != "pending" {
		t.Fatalf("in-flight state lost across the move: %+v", tl.Events)
	}

	moveTranscript(t, home, wt, main, "aaaa-1") // ExitWorktree: A -> B -> A
	check("after moving back", 4, 3, 1)
	tlAppend(t, filepath.Join(home, ".claude", "projects", projectSlug(main), "aaaa-1.jsonl"),
		tlResult(t, 60, "b2", false, "ok")+tlYou(t, 70, "last"))
	tl = check("after growth back home", 5, 3, 1)
	if tl.Events[0].Detail != "last" || tl.Events[1].State != "ok" {
		t.Fatalf("newest rows = %+v", tl.Events[:2])
	}
}

// A moved transcript that is now SHORTER than what was read (rewritten, not
// just moved) is re-read from the start with the derived state reset: no
// double counting.
func TestTimelineWorktreeMoveToASmallerFileResetsInsteadOfAppending(t *testing.T) {
	m, home, main, wt := newWorktreeLocator(t)
	writeTxLines(t, home, main, "aaaa-1", time.Now(),
		strings.TrimSuffix(tlUse(t, 0, "e1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "a", "new_string": "b"}), "\n"),
		strings.TrimSuffix(tlResult(t, 1, "e1", false, "ok"), "\n"),
		strings.TrimSuffix(tlYou(t, 2, "a long enough line to make this file larger than the next one"), "\n"))
	if tl := tlGet(t, m, "s1"); len(tl.Events) != 2 || tl.ToolCalls != 1 {
		t.Fatalf("before: %+v", tl)
	}
	if err := os.Remove(filepath.Join(home, ".claude", "projects", projectSlug(main), "aaaa-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	writeTxLines(t, home, wt, "aaaa-1", time.Now(),
		strings.TrimSuffix(tlUse(t, 0, "e1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "a", "new_string": "b"}), "\n"),
		strings.TrimSuffix(tlResult(t, 1, "e1", false, "ok"), "\n"))
	tl := tlGet(t, m, "s1")
	if len(tl.Events) != 1 || tl.ToolCalls != 1 || len(tl.Files) != 1 || tl.Files[0].Add != 1 {
		t.Fatalf("after: %+v", tl)
	}
	for _, e := range tl.Events {
		if e.Kind == "clear" {
			t.Fatalf("a clear divider appeared: %+v", tl.Events)
		}
	}
}
