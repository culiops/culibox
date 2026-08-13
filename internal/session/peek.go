package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrNoSession    = errors.New("no such session")
	ErrNotReplyable = errors.New("session has not opted in to messages")
	ErrNotWaiting   = errors.New("session is not waiting for input")
	ErrPending      = errors.New("session is inside a tool call")
	ErrBadText      = errors.New("message text is empty, multi-line, or contains control characters")
	// ErrNotClaude is the isClaudeBin seam the design names, enforced here in
	// the session layer rather than in the HTTP handler or the UI: the reply
	// path is the one place in this daemon that types into a live agent's
	// terminal, so its scope must not depend on which caller reached it.
	ErrNotClaude = errors.New("only claude sessions accept messages")
	// ErrNoTranscript is the fail-closed answer when PeekFor misses. The
	// pending guard's ground truth lives in the transcript; with no transcript
	// there is no way to know whether the agent is sitting on a permission
	// prompt, and "unknown" must not read as "not pending".
	ErrNoTranscript = errors.New("session transcript is not readable, so a message cannot be authorized")
)

// Peek is a per-session snapshot of what the agent last said, read from the
// JSONL transcript. It answers the two questions the sidebar cannot answer from
// PTY output alone: what is this session saying, and is it mid-tool.
type Peek struct {
	Text string `json:"text"`
	// Pending reports that the last record in the transcript is an assistant
	// turn ending on a tool_use with no result yet — the session is inside a
	// tool call, which includes sitting on a permission prompt. This is ground
	// truth from the transcript, unlike activityOf's silence heuristic.
	Pending bool `json:"pending,omitempty"`
}

// summaryMax is the rune budget for the one-line sidebar summary. Measured at
// the real 279px sidebar width; longer text is elided rather than wrapped.
const summaryMax = 120

// peekRec is the subset of a transcript record we decode. content is kept raw
// because it is an array of blocks on assistant records but may be a bare
// string on user records — decoding it eagerly would reject the latter.
type peekRec struct {
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentBlocks decodes message.content as a block array. A bare string (or any
// other shape) yields no blocks rather than an error: callers only ever ask
// "does this record contain blocks of interest".
func contentBlocks(raw json.RawMessage) []contentBlock {
	var bs []contentBlock
	if json.Unmarshal(raw, &bs) == nil {
		return bs
	}
	return nil
}

// parsePeekTail scans a byte tail backwards for the last assistant text block,
// and reports whether the final record is an unresolved tool_use.
//
// ok reports that at least one record parsed — i.e. the transcript is readable
// and non-empty. Text may still be empty ("no message yet"), which the UI shows
// differently from "transcript unreadable". A line that fails to unmarshal is
// skipped: the tail almost always begins mid-record.
func parsePeekTail(b []byte) (Peek, bool) {
	lines := strings.Split(string(b), "\n")
	var p Peek
	var parsedAny, sawLast, foundText bool
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec peekRec
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		parsedAny = true
		bs := contentBlocks(rec.Message.Content)
		// The first record we reach walking backwards IS the last record in the
		// file. If a tool_result had arrived, it would be that record instead,
		// so "last block is tool_use" is sufficient — no forward scan needed.
		if !sawLast {
			sawLast = true
			if rec.Message.Role == "assistant" && len(bs) > 0 && bs[len(bs)-1].Type == "tool_use" {
				p.Pending = true
			}
		}
		if !foundText && rec.Message.Role == "assistant" {
			for j := len(bs) - 1; j >= 0; j-- {
				if bs[j].Type == "text" && strings.TrimSpace(bs[j].Text) != "" {
					p.Text = bs[j].Text
					foundText = true
					break
				}
			}
		}
		if foundText {
			break
		}
	}
	return p, parsedAny
}

// summaryLine reduces peek text to the single line the sidebar shows: the first
// non-empty line, elided to summaryMax runes. Runes, not bytes — a byte-sliced
// multi-byte character renders as a replacement glyph.
func summaryLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		r := []rune(ln)
		if len(r) <= summaryMax {
			return ln
		}
		return string(r[:summaryMax-1]) + "…"
	}
	return ""
}

// canReply is the whole authorization decision for sending a message into a
// session, extracted so the handler stays a thin caller and every combination
// is table-testable. The reasons are codes; the HTTP layer maps them to
// statuses and the UI phrases them.
//
// The pending refusal is deliberate and counter-intuitive: a session sitting on
// a permission prompt is the one an operator most wants to answer, and the one
// where typing text is most dangerous, because that dialog reads single keys.
//
// Order matters for the WORDS, never for the outcome: pending refuses either
// way. It is tested before activity because a permission prompt redraws itself,
// which keeps resetting activityOf's idle timer — so the session that is most
// often pending reads as `working`, and testing activity first meant the
// operator was told "wait until it stops" about a session that had already
// stopped and was waiting on them. peek.js's sendState carries the same order;
// the two must not drift or the client's explanation contradicts the server's.
func canReply(replyable bool, activity string, pending bool) (bool, string) {
	if !replyable {
		return false, "not_replyable"
	}
	if pending {
		return false, "pending"
	}
	if activity != "waiting" {
		return false, "not_waiting"
	}
	return true, ""
}

// validReplyText accepts exactly one line of printable text. Single-line is
// what keeps bracketed paste (\x1b[200~ … \x1b[201~ plus a separate \r) out of
// this feature entirely. Every C0 control is rejected, not only newline: a tab
// triggers completion in a TUI and an escape sequence is a loss of control over
// the session.
func validReplyText(s string) error {
	if strings.TrimSpace(s) == "" {
		return ErrBadText
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ErrBadText
		}
	}
	return nil
}

// peekEntry is one cached parse, valid while the transcript's mtime is unchanged.
type peekEntry struct {
	mtime time.Time
	peek  Peek
}

// peekCached returns the cached parse when it still matches mtime.
func (m *Manager) peekCached(id string, mtime time.Time) (Peek, bool) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	e, ok := m.peekCache[id]
	if !ok || !e.mtime.Equal(mtime) {
		return Peek{}, false
	}
	return e.peek, true
}

func (m *Manager) peekStore(id string, mtime time.Time, p Peek) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	if m.peekCache == nil {
		m.peekCache = map[string]peekEntry{}
	}
	m.peekCache[id] = peekEntry{mtime: mtime, peek: p}
}

// peekForget drops a dead session's cached parse. Called beside every
// delete(m.sessions, id): without it the cache holds a full assistant message
// per session that ever existed, for the daemon's whole lifetime. Nothing stale
// is ever SERVED (ids are monotonic within a run), so this is purely about not
// growing forever — the same shape as the boxstate leak.
//
// Takes only peekMu, and never while m.mu is held: callers do this after
// releasing m.mu, which keeps the two locks unordered with respect to each
// other.
func (m *Manager) peekForget(id string) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	delete(m.peekCache, id)
}

// PeekFor reads the latest transcript peek for a claude session. ok=false for a
// non-claude session, one whose transcript cannot be located or read, or one
// whose tail holds no parseable record.
//
// The sidebar polls this for every session, so the hot path is one os.Stat: the
// tail is re-read only when mtime moved. A waiting session's transcript is
// static, so the sessions whose summaries anyone reads are served from cache.
//
// Locking mirrors UsageFor: capture Bin/Cwd/Box/Created in one critical section
// (Bin is backfilled post-reconcile by ResolveBins, so a lock-free read races),
// then work without the lock.
func (m *Manager) PeekFor(id string) (Peek, bool) {
	m.mu.Lock()
	s := m.sessions[id]
	home, stateDir := m.home, m.stateDir
	var bin, cwd, box string
	var created time.Time
	if s != nil {
		bin, cwd, box, created = s.Bin, s.Cwd, s.Box, s.Created
	}
	m.mu.Unlock()
	if s == nil || !isClaudeBin(bin) {
		return Peek{}, false
	}
	base := home
	if box != "" && stateDir != "" {
		base = filepath.Join(stateDir, "boxstate", box)
	}
	if base == "" {
		return Peek{}, false
	}
	path, ok := transcriptPath(cwd, base, created)
	if !ok {
		return Peek{}, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Peek{}, false
	}
	if p, ok := m.peekCached(id, fi.ModTime()); ok {
		return p, true
	}
	b, err := readUsageTail(path)
	if err != nil {
		return Peek{}, false
	}
	p, ok := parsePeekTail(b)
	if !ok {
		return Peek{}, false
	}
	m.peekStore(id, fi.ModTime(), p)
	return p, true
}

// SendMessage types one line into a session's PTY, followed by Enter.
//
// Text is validated FIRST, before any guard, so a malformed payload cannot be
// used to probe which sessions exist or what state they are in.
//
// There is a small TOCTOU window between the guards and the Write. It is
// accepted: the window is narrow and the worst outcome is a line arriving one
// beat late. No acknowledgement is produced — when the message lands, the agent
// writes a user record, the transcript's mtime moves, and the next poll shows a
// changed summary. That is the confirmation.
func (m *Manager) SendMessage(id, text string) error {
	if err := validReplyText(text); err != nil {
		return err
	}
	m.mu.Lock()
	s := m.sessions[id]
	var bin string
	if s != nil {
		bin = s.Bin // guarded by m.mu: ResolveBins backfills it post-reconcile
	}
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	// Claude-only, enforced here rather than at the HTTP layer or in the UI.
	// Every guard below is derived from a claude transcript; a raw $SHELL
	// session has none, so without this gate the pending guard could not fail
	// closed and the whole authorization reduced to the opt-in flag.
	if !isClaudeBin(bin) {
		return ErrNotClaude
	}
	info := s.Info()
	// Fail CLOSED on a PeekFor miss. A miss used to yield the zero Peek, whose
	// pending=false PASSED the third guard — so the one state the guard exists
	// to catch (no ground truth about the agent's turn) was also the state that
	// waved the message through.
	p, havePeek := m.PeekFor(id)
	if !havePeek {
		return ErrNoTranscript
	}
	if ok, why := canReply(info.Replyable, info.Activity, p.Pending); !ok {
		switch why {
		case "not_replyable":
			return ErrNotReplyable
		case "not_waiting":
			return ErrNotWaiting
		case "pending":
			return ErrPending
		default:
			// An authorization reason this switch does not know about is a
			// refusal, not a pass: a future fourth reason must not inherit
			// whichever error happened to sit in the default arm.
			return ErrNotReplyable
		}
	}
	if _, err := s.Write([]byte(text + "\r")); err != nil {
		return err
	}
	return nil
}
