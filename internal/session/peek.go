package session

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unicode"
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
	// Ask is a one-line rendering of the tool call a Pending session is waiting
	// on — "Bash(rm -rf build)", "Edit(internal/web/app.js)", or the bare tool
	// name — elided to askMax runes. Empty unless Pending.
	Ask string `json:"ask,omitempty"`
	// AskFull is Ask untruncated (askRender), for the card's Yes/No — which
	// only this exact text may unlock (Manager.Answer hashes it). Empty unless
	// exactly ONE tool call is pending and askRender reports a primary
	// argument for it (only Bash/Read — see askRender), and the rendering fits
	// askFullMax. Never serialised: /peek does not check replyable, and the
	// full command must not reach a client that cannot act on it (List ships
	// it as Info.AskFull under those gates instead).
	AskFull string `json:"-"`
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
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`  // tool_use
	Input json.RawMessage `json:"input"` // tool_use
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
	// Parallel tool calls are written one tool_use per assistant record, and
	// Claude Code asks about the FIRST while the file ends on the LAST
	// (measured 2026-09-29). pendingTools counts the unresolved calls trailing
	// the transcript; counting stays true until a record that ends the run is
	// seen — a tail that runs out first cannot rule out more calls before it.
	var pendingTools int
	var counting bool
	var askFull string
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
		// Bookkeeping records (cost-state, last-prompt, ai-title, …) carry no
		// message. They are not "the last record" for pending purposes.
		if rec.Message.Role == "" {
			continue
		}
		bs := contentBlocks(rec.Message.Content)
		// The first role-bearing record we reach walking backwards IS the last
		// message in the file. If a tool_result had arrived, it would be that
		// record instead, so "last block is tool_use" is sufficient — no
		// forward scan needed.
		if !sawLast {
			sawLast = true
			if rec.Message.Role == "assistant" && len(bs) > 0 && bs[len(bs)-1].Type == "tool_use" {
				last := bs[len(bs)-1]
				p.Pending = true
				p.Ask = askLine(last.Name, last.Input)
				if full, hasArg := askRender(last.Name, last.Input); hasArg && len(full) <= askFullMax {
					askFull = full
				}
				counting = true
			}
		}
		if counting {
			n, all := trailingToolUses(bs)
			if rec.Message.Role != "assistant" {
				counting = false
			} else {
				pendingTools += n
				if !all {
					counting = false
				}
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
	if pendingTools == 1 && !counting {
		p.AskFull = askFull
	}
	return p, parsedAny
}

// summaryLine reduces peek text to the single line the sidebar shows: the first
// non-empty line, elided to summaryMax runes. Runes, not bytes — a byte-sliced
// multi-byte character renders as a replacement glyph.
//
// Unicode format characters (Cf: bidi overrides U+202A-202E, bidi isolates
// U+2066-2069, zero-width joiners/spaces/marks U+200B-200F, …) are mapped to a
// space before the length/emptiness checks. They are invisible or
// rendering-altering, not IsControl (askLine's comment below has the same
// note for the same reason), so without this a line could read differently
// than the bytes the sidebar/notification actually carries — or a bidi
// override could visually reorder the rest of the line.
func summaryLine(text string) string {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		ln = strings.Map(func(r rune) rune {
			if unicode.Is(unicode.Cf, r) {
				return ' '
			}
			return r
		}, ln)
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

// askMax is the rune budget for Peek.Ask, sized for the sidebar card and an OS
// notification body.
const askMax = 80

// askLine renders a pending tool call as one printable line: the tool name,
// plus the command (shell tools) or the path (file tools). Everything else is
// the name alone — its input is not something to read at a glance. Control
// characters become spaces and whitespace runs collapse, so a heredoc or an
// ANSI-laden echo still renders as one clean line. unicode.IsControl covers C0
// (< 0x20), DEL (0x7f) AND the C1 range (U+0080-U+009F, e.g. U+009B CSI,
// U+009D OSC) — strings.Fields alone would not collapse a C1 control, since
// unicode.IsSpace does not treat it as whitespace. unicode.Cf (format
// characters: bidi overrides/isolates, zero-width joiners/spaces, …) is
// mapped the same way — those are not IsControl either, but a bidi override
// or a zero-width character reaching a rendered tool-call line is the same
// class of problem: it changes what the line looks like without changing
// what it says.
func askLine(name string, input json.RawMessage) string {
	if name == "" {
		return ""
	}
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	arg := ""
	for _, k := range []string{"command", "file_path", "notebook_path"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			arg = v
			break
		}
	}
	s := name
	if arg != "" {
		clean := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return ' '
			}
			return r
		}, arg)
		s = name + "(" + strings.Join(strings.Fields(clean), " ") + ")"
	}
	if r := []rune(s); len(r) > askMax {
		s = string(r[:askMax-1]) + "…"
	}
	return s
}

// askFullMax caps AskFull. A longer rendering is not offered for a remote
// answer at all: the card says to open the terminal.
const askFullMax = 4 << 10

// askableArgs is the allowlist of tools whose primary argument IS the whole
// action being requested, mapped to that argument's JSON key. Only these
// tools may produce an AskFull: Edit/Write/NotebookEdit's real content is a
// diff or a body of text, invisible in a one-line rendering, so an operator
// answering Yes to the rendered line alone would not have seen what they are
// approving. Every other/unknown tool (and every unlisted key on an allowed
// tool) is refused the same way. (Controller ruling, 2026-09-29, amending
// spec §1.2 — askLine above is unaffected and keeps rendering every tool's
// command/file_path/notebook_path for the elided sidebar Ask line.)
var askableArgs = map[string]string{
	"Bash": "command",
	"Read": "file_path",
}

// askRender renders a tool call as one untruncated, printable line — the tool
// name plus its primary argument — but ONLY for a tool in askableArgs; hasArg
// is false for every other tool, including one that has a primary argument
// that just isn't safe to answer from alone (see askableArgs). Control
// characters (C0, DEL, C1) and Unicode format characters (bidi overrides,
// zero-width) become spaces, and whitespace runs collapse, exactly like
// askLine above: a heredoc or an ANSI-laden echo still renders as one clean
// line, and a bidi override cannot make the line read differently from what
// it says.
func askRender(name string, input json.RawMessage) (string, bool) {
	key, ok := askableArgs[name]
	if !ok {
		return "", false
	}
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	v, ok := in[key].(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, v)
	return name + "(" + strings.Join(strings.Fields(clean), " ") + ")", true
}

// trailingToolUses counts the tool_use blocks at the END of a record's
// content and reports whether the record held nothing else (all == true), so
// a caller can tell "N pending calls, and this record is entirely made of
// them" from "N pending calls, but there was also a text/thinking block after
// them" — the latter cannot happen for the tail record itself (a text block
// there means the run finished, see parsePeekTail), but does for a record
// further back that has both a text block and one or more trailing tool_use
// blocks preceding it.
func trailingToolUses(bs []contentBlock) (int, bool) {
	n := 0
	for j := len(bs) - 1; j >= 0 && bs[j].Type == "tool_use"; j-- {
		n++
	}
	return n, n > 0 && n == len(bs)
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

// needOf is the attention decision for one listed session, pure so every
// combination is table-testable. activity is what Info computed from PTY
// silence; blocked is Claude Code's own live registry saying the session sits
// on a prompt.
//
// blocked RAISES a live session to "waiting" and never lowers one. A permission
// prompt animates (~40 bytes every 0.6 s, measured on 2.1.284), so silence can
// never see it; a missing or drifted registry therefore costs only the ask case,
// which is exactly today's behaviour.
func needOf(activity string, claude bool, p Peek, havePeek, blocked bool) (act, need, ask string) {
	act = activity
	if !claude {
		return act, "", ""
	}
	if blocked && (act == "working" || act == "idle") {
		act = "waiting"
	}
	if act != "waiting" {
		return act, "", ""
	}
	switch {
	case blocked || (havePeek && p.Pending):
		need = "ask"
		if havePeek {
			ask = p.Ask
		}
	case havePeek:
		need = "turn"
	}
	return act, need, ask
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

// peekEntry is one cached parse, valid while the transcript's path AND mtime
// are unchanged — /clear moves a session to a new file.
type peekEntry struct {
	path  string
	mtime time.Time
	peek  Peek
}

// peekCached returns the cached parse when it still matches path and mtime.
func (m *Manager) peekCached(id, path string, mtime time.Time) (Peek, bool) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	e, ok := m.peekCache[id]
	if !ok || e.path != path || !e.mtime.Equal(mtime) {
		return Peek{}, false
	}
	return e.peek, true
}

func (m *Manager) peekStore(id, path string, mtime time.Time, p Peek) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	if m.peekCache == nil {
		m.peekCache = map[string]peekEntry{}
	}
	m.peekCache[id] = peekEntry{path: path, mtime: mtime, peek: p}
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
// The sidebar polls this for every session, so the hot path is a pidfile read,
// a registry read and one os.Stat: the tail is re-read only when the path or
// mtime moved.
func (m *Manager) PeekFor(id string) (Peek, bool) {
	p, ok, _ := m.peekAndBlocked(id)
	return p, ok
}

// peekAndBlocked is PeekFor plus whether Claude Code's live registry says the
// session is sitting on a prompt. blocked can be true with ok=false (registry
// readable, transcript not).
func (m *Manager) peekAndBlocked(id string) (Peek, bool, bool) {
	l := m.locate(id)
	blocked := l.reg != nil && l.reg.Status == regStatusWaiting
	if !l.claude || l.path == "" {
		return Peek{}, false, blocked
	}
	fi, err := os.Stat(l.path)
	if err != nil {
		return Peek{}, false, blocked
	}
	if p, ok := m.peekCached(id, l.path, fi.ModTime()); ok {
		return p, true, blocked
	}
	b, err := readUsageTail(l.path)
	if err != nil {
		return Peek{}, false, blocked
	}
	p, ok := parsePeekTail(b)
	if !ok {
		return Peek{}, false, blocked
	}
	m.peekStore(id, l.path, fi.ModTime(), p)
	return p, true, blocked
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
	p, havePeek, blocked := m.peekAndBlocked(id)
	if !havePeek {
		return ErrNoTranscript
	}
	// blocked joins pending: the registry can see a prompt before (or without)
	// the transcript's tool_use, and a line typed into a prompt answers it.
	if ok, why := canReply(info.Replyable, info.Activity, p.Pending || blocked); !ok {
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
