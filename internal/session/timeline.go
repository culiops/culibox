package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/boxread"
	"github.com/culiops/agent-box/internal/mask"
)

const (
	// timelineMaxScanBytes bounds one parse pass: a transcript larger than this
	// (or growing by more than this between polls) is read from its last 64 MiB
	// only. The transcript is written by an untrusted agent, so no pass may be
	// proportional to the file's size.
	timelineMaxScanBytes = 64 << 20
	// timelineMaxEvents is how many events a session's timeline keeps.
	timelineMaxEvents = 1000
	// timelineDetailMax is the rune budget of TimelineEvent.Detail.
	timelineDetailMax = 300
	// timelineRawDetailMax is how much of a field is masked and cleaned before
	// truncation: far above timelineDetailMax so a secret near the cut is
	// still masked whole, far below a 16 MiB command.
	timelineRawDetailMax = 8 << 10
	timelineToolNameMax  = 64
)

// TimelineEvent is one row of a session's activity: a tool call, something the
// human typed, or a /clear divider. It carries descriptions only — never a
// tool result, file content, an edit body or a prompt. Read's Lines is a count.
type TimelineEvent struct {
	TS     int64  `json:"ts"`              // unix ms
	Kind   string `json:"kind"`            // "tool" | "you" | "clear"
	Tool   string `json:"tool,omitempty"`  // Bash, Read, Edit, Write, MultiEdit, NotebookEdit, Grep, Glob, WebFetch, WebSearch, Task, mcp__…
	Detail string `json:"detail"`          // masked, ≤300 runes, path only for file tools
	State  string `json:"state,omitempty"` // "ok" | "error" | "pending"
	MS     int64  `json:"ms,omitempty"`    // result ts − use ts
	Exit   *int   `json:"exit,omitempty"`  // Bash "Exit code N" on error
	Add    *int   `json:"add,omitempty"`   // lines added by an edit
	Del    *int   `json:"del,omitempty"`   // lines removed by an edit
	Lines  *int   `json:"lines,omitempty"` // Read: line count of the result (count only)
}

// FileChange is the running line total of one file the agent edited.
type FileChange struct {
	Path string `json:"path"`
	Add  int    `json:"add"`
	Del  int    `json:"del"`
}

// Timeline is a session's parsed activity.
type Timeline struct {
	Claude bool
	Events []TimelineEvent // newest first
	// More: older events exist beyond this page.
	More bool
	// Partial: the daemon has not seen the whole conversation (a huge
	// transcript was read from its tail, or the ring dropped old events).
	Partial      bool
	Files        []FileChange // ≤200, most recent first
	ToolCalls    int
	WaitingSince int64 // unix ms the live registry has said "waiting" since; 0 = not waiting
}

// tlRec is the subset of a transcript record the timeline reads.
type tlRec struct {
	Timestamp   string `json:"timestamp"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// tlBlock is contentBlock plus the two fields a tool_result needs. Content is
// kept raw: it is decoded only for the few results whose count we want.
type tlBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

// tlInput is every tool_use input field the timeline looks at. The body fields
// (old/new/content) are only ever counted.
type tlInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	URL          string `json:"url"`
	Query        string `json:"query"`
	Pattern      string `json:"pattern"`
	Description  string `json:"description"`
	OldString    string `json:"old_string"`
	NewString    string `json:"new_string"`
	Content      string `json:"content"`
	Edits        []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// tlToolIDRe bounds a tool_use id before it is ever kept as a map key: real
// ids are "toolu_…" tokens. The id is agent-written, so an unbounded one
// (16 MiB x 512 pending) would be a memory bomb.
var tlToolIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// tlSkipPrefixes mark user text that is Claude Code plumbing (slash commands
// and their echoes), not something the human said.
var tlSkipPrefixes = []string{"<command-name>", "<command-message>", "<command-args>", "<local-command-"}

// tlDetail makes transcript text safe and short for one timeline row: masked,
// reduced to one printable line, and cut to timelineDetailMax runes (the
// ellipsis included). Masking runs on up to timelineRawDetailMax bytes first,
// so a token straddling the visible cut is still replaced whole.
func tlDetail(s string) string {
	if len(s) > timelineRawDetailMax {
		s = s[:timelineRawDetailMax]
		for len(s) > 0 && !utf8.RuneStart(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
	}
	s = cleanArg(mask.Display(s))
	if utf8.RuneCountInString(s) <= timelineDetailMax {
		return s
	}
	return string([]rune(s)[:timelineDetailMax-1]) + "…"
}

// tlLines counts the lines of an edit body or a Read result: "" is 0, and a
// trailing newline does not start another line.
func tlLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// tlResultText joins a tool_result's text: a bare string, or text blocks.
func tlResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []tlBlock
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range bs {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// tlExitCode reads "Exit code N" from the start of a failed Bash result.
func tlExitCode(text string) (int, bool) {
	const p = "Exit code "
	if !strings.HasPrefix(text, p) {
		return 0, false
	}
	rest := text[len(p):]
	n := 0
	for n < len(rest) && n < 5 && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, false
	}
	v, err := strconv.Atoi(rest[:n])
	return v, err == nil
}

func intp(v int) *int { return &v }

// feed applies one transcript line. Anything unparseable, timestamp-less,
// sidechain or meta is skipped.
func (st *timelineState) feed(line []byte) {
	var rec tlRec
	if json.Unmarshal(line, &rec) != nil || rec.IsMeta || rec.IsSidechain {
		return
	}
	t, err := time.Parse(time.RFC3339, rec.Timestamp)
	if err != nil {
		return
	}
	ts := t.UnixMilli()
	switch rec.Message.Role {
	case "assistant":
		var bs []tlBlock
		if json.Unmarshal(rec.Message.Content, &bs) != nil {
			return
		}
		for i := range bs {
			if bs[i].Type == "tool_use" {
				st.toolUse(ts, &bs[i])
			}
		}
	case "user":
		var text string
		if json.Unmarshal(rec.Message.Content, &text) != nil {
			var bs []tlBlock
			if json.Unmarshal(rec.Message.Content, &bs) != nil {
				return
			}
			for i := range bs {
				switch bs[i].Type {
				case "tool_result":
					st.toolResult(ts, &bs[i])
				case "text":
					if text == "" {
						text = bs[i].Text
					}
				}
			}
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, p := range tlSkipPrefixes {
			if strings.HasPrefix(text, p) {
				return
			}
		}
		st.add(TimelineEvent{TS: ts, Kind: "you", Detail: tlDetail(text)})
	}
}

// toolUse records a tool_use as a pending event.
func (st *timelineState) toolUse(ts int64, b *tlBlock) {
	if !tlToolIDRe.MatchString(b.ID) {
		return // cannot be matched to a result, and must not be stored
	}
	var in tlInput
	_ = json.Unmarshal(b.Input, &in) // a mistyped field leaves its zero value
	name := cleanArg(b.Name)
	if utf8.RuneCountInString(name) > timelineToolNameMax {
		name = string([]rune(name)[:timelineToolNameMax])
	}
	ev := TimelineEvent{TS: ts, Kind: "tool", Tool: name, State: "pending"}
	var path string // set for an edit that changes the file totals
	var add, del int
	switch name {
	case "Bash":
		ev.Detail = tlDetail(in.Command)
	case "Read":
		ev.Detail = tlDetail(in.FilePath)
	case "Edit":
		ev.Detail = tlDetail(in.FilePath)
		path, add, del = ev.Detail, tlLines(in.NewString), tlLines(in.OldString)
	case "Write":
		ev.Detail = tlDetail(in.FilePath)
		path, add = ev.Detail, tlLines(in.Content)
	case "MultiEdit":
		ev.Detail = tlDetail(in.FilePath)
		path = ev.Detail
		for _, e := range in.Edits {
			add += tlLines(e.NewString)
			del += tlLines(e.OldString)
		}
	case "NotebookEdit":
		ev.Detail = tlDetail(in.NotebookPath)
		st.touch(ev.Detail, 0, 0)
	case "WebFetch":
		ev.Detail = tlDetail(in.URL)
	case "WebSearch":
		ev.Detail = tlDetail(in.Query)
	case "Grep", "Glob":
		ev.Detail = tlDetail(in.Pattern)
	case "Task":
		ev.Detail = tlDetail(in.Description)
	}
	if path != "" {
		ev.Add, ev.Del = intp(add), intp(del)
		st.touch(path, add, del)
	}
	st.toolCalls++
	seq := st.add(ev)
	st.addPending(&pendingTool{id: b.ID, seq: seq, ts: ts, tool: name, path: path, add: add, del: del})
}

// toolResult finishes the pending event a tool_result answers. Only its error
// flag, timing, a Bash exit code and a Read line count are taken from it.
func (st *timelineState) toolResult(ts int64, b *tlBlock) {
	if !tlToolIDRe.MatchString(b.ToolUseID) {
		return
	}
	p := st.takePending(b.ToolUseID)
	if p == nil {
		return
	}
	ev := st.at(p.seq)
	if ev == nil {
		return
	}
	ev.State = "ok"
	if b.IsError {
		ev.State = "error"
		if p.path != "" {
			st.untouch(p.path, p.add, p.del)
		}
	}
	if ts >= p.ts {
		ev.MS = ts - p.ts
	}
	switch {
	case p.tool == "Bash" && b.IsError:
		if code, ok := tlExitCode(tlResultText(b.Content)); ok {
			ev.Exit = intp(code)
		}
	case p.tool == "Read" && !b.IsError:
		ev.Lines = intp(tlLines(tlResultText(b.Content)))
	}
}

// advance reads whatever f gained since the last pass, bounded by
// timelineMaxScanBytes, and feeds every complete line to the parser.
func (st *timelineState) advance(f *os.File, size int64, path string) {
	if st.path != path {
		if st.path != "" {
			// /clear (or a resume) moved the session to a new conversation:
			// keep what we have and mark the seam.
			st.abandonPending()
			st.clearSeq = st.add(TimelineEvent{TS: time.Now().UnixMilli(), Kind: "clear"})
		}
		st.path, st.offset, st.skip = path, 0, false
	}
	if size < st.offset {
		st.resetContent() // truncated or replaced: start over
	}
	if size-st.offset > timelineMaxScanBytes {
		st.offset = size - timelineMaxScanBytes
		st.skip = true // we land mid-line; the first line is a fragment
		st.partial = true
	}
	if size <= st.offset {
		return
	}
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return
	}
	lr := boxread.NewLineReader(io.LimitReader(f, size-st.offset), boxread.MaxLineBytes)
	for {
		line, n, over, err := lr.Next()
		if err != nil {
			// An unterminated tail is a line still being written: leave it
			// for the next pass — unless it is overlong or the end of a line
			// already being discarded, which must not be re-read every poll.
			if over || st.skip {
				st.offset += int64(n)
				st.skip = true
			}
			return
		}
		st.offset += int64(n)
		if st.skip {
			st.skip = false
			continue
		}
		if over {
			continue
		}
		st.feed(line)
	}
}

// TimelineFor returns a page of the session's activity timeline: up to limit
// events strictly older than beforeMS (0 = from the newest), newest first.
// ok=false only for an unknown session. A session that is not Claude, or whose
// transcript cannot be PROVEN to be its own (boundTranscript), yields an empty
// Timeline — never a sibling's history.
func (m *Manager) TimelineFor(id string, beforeMS int64, limit int) (Timeline, bool) {
	m.mu.Lock()
	_, known := m.sessions[id]
	m.mu.Unlock()
	if !known {
		return Timeline{}, false
	}
	if limit <= 0 || limit > timelineMaxEvents {
		limit = timelineMaxEvents
	}
	l, bound := m.boundTranscript(id)
	tl := Timeline{Claude: l.claude, Events: []TimelineEvent{}, Files: []FileChange{}}
	if !l.claude {
		return tl, true
	}
	tl.WaitingSince = waitingSince(l)
	if !bound {
		return tl, true
	}
	f, fi, ok := l.openTranscript()
	if !ok {
		return tl, true
	}
	defer f.Close()

	st := m.timelineFor(id)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.advance(f, fi.Size(), l.path)

	evs := st.evs
	if len(evs) > timelineMaxEvents {
		evs = evs[len(evs)-timelineMaxEvents:]
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if beforeMS > 0 && evs[i].TS >= beforeMS {
			continue
		}
		// A page may run past limit to finish the millisecond it ends in:
		// the next page asks for events strictly older, so a same-ms event
		// left behind would be lost.
		if len(tl.Events) >= limit && evs[i].TS != tl.Events[len(tl.Events)-1].TS {
			tl.More = true
			break
		}
		tl.Events = append(tl.Events, evs[i])
	}
	tl.Partial = st.partial
	tl.Files = st.fileList()
	tl.ToolCalls = st.toolCalls
	return tl, true
}

// waitingSince is when Claude Code last rewrote the live registry file, if its
// status is "waiting": the registry is rewritten on each status change, so its
// mtime is when the prompt appeared. 0 when not waiting or unreadable.
func waitingSince(l located) int64 {
	if l.reg == nil || l.reg.Status != regStatusWaiting {
		return 0
	}
	name := strconv.Itoa(l.reg.Pid) + ".json"
	var fi os.FileInfo
	if l.boxDir != "" {
		f, bfi, ok := boxread.Open(l.boxDir, boxSessionsRel+"/"+name)
		if !ok {
			return 0
		}
		f.Close()
		fi = bfi
	} else {
		var err error
		if fi, err = os.Lstat(filepath.Join(l.claudeDir, "sessions", name)); err != nil {
			return 0
		}
	}
	return fi.ModTime().UnixMilli()
}
