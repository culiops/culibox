package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/culiops/culibox/internal/mask"
)

var (
	ErrNoSession    = errors.New("no such session")
	ErrNotReplyable = errors.New("session has not opted in to messages")
	// ErrRepliesDisabled: the instance-wide kill-switch (config replies_enabled)
	// is off. Distinct from ErrNotReplyable, which is the per-session opt-in.
	ErrRepliesDisabled = errors.New("replies are disabled on this instance")
	ErrNotWaiting      = errors.New("session is not waiting for input")
	ErrPending         = errors.New("session is inside a tool call")
	ErrBadText         = errors.New("message text is empty, multi-line, or contains control characters")
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

	// askID and the askSrc* fields identify the exact pending-prompt READ
	// AskFull came from. Never serialised, never exposed outside this
	// package — Manager.Answer's per-session at-most-once memo (control.go)
	// is their only reader. askID is the pending tool_use's own id (real or,
	// rarely, the positional synthetic key toolUseKey falls back to); askSrc*
	// is the transcript read (path+mtime+size) that produced this Peek. Both
	// are set together, only when AskFull is non-empty — see peekAndBlocked
	// and parsePeekTailCut.
	askID       string
	askSrcPath  string
	askSrcMTime time.Time
	askSrcSize  int64
}

// summaryMax is the rune budget for the one-line sidebar summary. Measured at
// the real 279px sidebar width; longer text is elided rather than wrapped.
const summaryMax = 120

// peekRec is the subset of a transcript record we decode. content is kept raw
// because it is an array of blocks on assistant records but may be a bare
// string on user records — decoding it eagerly would reject the latter.
type peekRec struct {
	// Timestamp is the record's own RFC3339 write time (real transcripts:
	// "2026-09-15T12:51:20.057Z"), sibling to "message". Used only by the
	// tail record — see parsePeekTailCut's created parameter — to refuse a
	// pending tool_use written by a PRIOR run, before Restart's --resume
	// reused this same transcript file.
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role string `json:"role"`
		// ID is the API message id shared by every record one assistant
		// message was split across on write (parallel tool_use calls each
		// get their own record; a single streamed turn can split
		// thinking/text/tool_use the same way). It is what binds a "pending
		// run" together in parsePeekTail — see the comment there.
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	// ID is the tool_use block's own id (Claude Code writes "id":"toolu_…").
	// Used to dedupe a tool_use across the records of one pending run.
	ID   string `json:"id"`
	Text string `json:"text"`
	Name string `json:"name"` // tool_use
	// ToolUseID is the id of the tool_use a tool_result block resolves
	// ("tool_use_id" — a different field from ID above). Streaming writes a
	// tool_result USER record between two tool_use records of the SAME
	// message.id (measured 2026-09-29, fix round 2: 1,546 times in 400
	// transcripts) — see parsePeekTail's resolvedIDs.
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"` // tool_use
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
// and reports whether the final record is an unresolved tool_use. It is
// parsePeekTailCut with cut=false — the right default for a hand-built test
// tail, which represents "the whole relevant story" rather than a byte-sliced
// read of a real file. The one production caller that reads a real,
// possibly-truncated file (peekAndBlocked, via readUsageTail) calls
// parsePeekTailCut directly with the reader's own cut result.
func parsePeekTail(b []byte) (Peek, bool) {
	return parsePeekTailCut(b, false, time.Time{})
}

// parsePeekTailCut is parsePeekTail plus cut: whether the byte slice is
// KNOWN to be a truncated read of a larger file (readUsageTail sets this when
// size > usageTailBytes) rather than the whole file.
//
// created, when non-zero, is the session's own Session.Created instant. After
// Restart with --resume, the resumed process appends to the SAME transcript
// file — a pending tool_use record still sitting there from the OLD run (one
// this session never made) must not be offered. A tail record whose own
// timestamp is parsable and strictly before created has its AskFull refused;
// a record with no parsable timestamp, or a zero created (most callers,
// including every parsePeekTail caller below, which represent "the whole
// relevant story" rather than a real resumed transcript), keeps the ordinary
// behaviour.
//
// ok reports that at least one record parsed — i.e. the transcript is readable
// and non-empty. Text may still be empty ("no message yet"), which the UI shows
// differently from "transcript unreadable". A line that fails to unmarshal is
// skipped: the tail almost always begins mid-record.
func parsePeekTailCut(b []byte, cut bool, created time.Time) (Peek, bool) {
	lines := strings.Split(string(b), "\n")
	var p Peek
	var parsedAny, sawLast, foundText bool
	// Fix round 2 (2026-09-29): real transcripts (measured: 1,546 times in the
	// last 400) interleave a tool_result USER record BETWEEN two tool_use
	// records of the SAME message.id — Claude Code streams tool execution, so
	// an earlier call in a turn can resolve while a later call in that same
	// turn is still being written. A rule that ends the run at the first
	// non-assistant record (fix round 1's runActive) stops too early and can
	// offer AskFull for a call that ISN'T the one still unresolved. The rule
	// here instead:
	//
	//   - walks back through every record, collecting each user tool_result's
	//     tool_use_id into resolvedIDs (never ends the run — see below);
	//   - binds the run to the LAST assistant record's message.id (runID);
	//   - the run ends ONLY at an assistant record with a DIFFERENT
	//     non-empty message.id (a boundary) — a text/thinking-only record,
	//     and any user record (tool_result or not), do not end it;
	//   - pools EVERY tool_use block's id from every run-matching assistant
	//     record — not only trailing ones, because Claude Code can interleave
	//     a tool_use with a text block in one record — into runToolIDs;
	//   - once a boundary is found, keeps walking WITHOUT adding to
	//     runToolIDs, purely to check that runID does not reappear further
	//     back: a reappearance means message.id ordering is not the simple
	//     single-split-turn shape this rule assumes, so the count up to the
	//     boundary cannot be trusted (failed = true);
	//   - an assistant record inside the run (before any boundary) with an
	//     EMPTY message.id can't be told apart from a genuine continuation of
	//     the run, so it also fails closed (failed = true);
	//   - a line that fails to parse before a boundary is found means the
	//     256 KiB tail itself may have started mid-message — indistinguishable
	//     from a truncated record that could have held another call or the
	//     true boundary — so that fails closed too (a malformed line found
	//     AFTER a boundary is harmless: the run's own count is already
	//     sealed).
	//
	// Fix round 3 (2026-09-29), two more fail-open edges closed:
	//
	//   (a) The malformed-line check above only fires when the 256 KiB cut
	//       happens to land MID-line, producing unparseable JSON. When the
	//       cut instead lands exactly on a newline, no line fails to parse,
	//       yet a record just outside the window (e.g. an earlier unresolved
	//       call) is exactly as invisible. cut (from the reader) fixes this:
	//       when cut and no boundary was ever found inside the tail, AskFull
	//       stays empty regardless of what the visible ids alone would
	//       suggest — the window may simply be too small to have shown the
	//       whole run. A FULL file read (cut=false) with no boundary is the
	//       ordinary "this is the session's first message" case and must
	//       still offer.
	//   (b) AskFull used to assume the single unresolved id is always the
	//       tail's own last tool_use block — true in general (nothing can
	//       resolve the tail's own call, since nothing follows it in the
	//       file) EXCEPT when the SAME tool_use id is written more than once
	//       (a duplicate record) and a tool_result resolves that shared id
	//       while walking back BEFORE reaching an older, genuinely different,
	//       still-unresolved call: the count can then land on exactly one
	//       unresolved id that is NOT the tail's own. tailToolID captures the
	//       tail's own (possibly synthetic, see below) key at the point askFull
	//       is computed, and the final check requires the one unresolved id to
	//       equal it.
	var runID, tailToolID string
	var runOK, boundaryFound, failed, tailStale bool
	runToolIDs := map[string]bool{}
	resolvedIDs := map[string]bool{}
	var askFull string
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if ln == "" {
			continue
		}
		var rec peekRec
		if json.Unmarshal([]byte(ln), &rec) != nil {
			if runOK && !boundaryFound {
				failed = true
			}
			continue
		}
		parsedAny = true
		// Bookkeeping records (cost-state, last-prompt, ai-title, …) carry no
		// message. They are not "the last record" for pending purposes.
		if rec.Message.Role == "" {
			continue
		}
		bs := contentBlocks(rec.Message.Content)
		// toolUseKey resolves a block's dedup key: its own id, or — for a
		// block with no id (not a shape Claude Code writes, but not assumed)
		// — a synthetic key from its position (line i, block index j).
		// Position-based rather than a shared counter so the SAME block
		// queried from two different places below (the tail's own last
		// block, and the general per-record scan) always resolves to the
		// SAME key, without needing to share mutable state between them.
		toolUseKey := func(j int) string {
			if id := bs[j].ID; id != "" {
				return id
			}
			return fmt.Sprintf("\x00%d:%d", i, j)
		}
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
				tailToolID = toolUseKey(len(bs) - 1)
				// Restart --resume stale-record guard: only the TAIL record —
				// the pending tool_use itself — is checked. created is zero
				// for every parsePeekTail caller (no real Session behind it),
				// so this is a no-op there; a record with no parsable
				// timestamp is left unfiltered, matching pre-fix behaviour.
				if !created.IsZero() {
					if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil && ts.Before(created) {
						tailStale = true
					}
				}
				if rec.Message.ID != "" {
					runID = rec.Message.ID
					runOK = true
				}
			}
		}
		if runOK {
			if rec.Message.Role == "assistant" {
				switch {
				case boundaryFound:
					// Past the run's boundary: only watching for a
					// disqualifying reappearance now, never counting.
					if rec.Message.ID == runID {
						failed = true
					}
				case rec.Message.ID == "":
					failed = true
				case rec.Message.ID == runID:
					for j, blk := range bs {
						if blk.Type != "tool_use" {
							continue
						}
						runToolIDs[toolUseKey(j)] = true
					}
				default:
					boundaryFound = true
				}
			}
			for _, blk := range bs {
				if blk.Type == "tool_result" && blk.ToolUseID != "" {
					resolvedIDs[blk.ToolUseID] = true
				}
			}
		}
		if !foundText && rec.Message.Role == "assistant" {
			for j := len(bs) - 1; j >= 0; j-- {
				if bs[j].Type == "text" && strings.TrimSpace(bs[j].Text) != "" {
					p.Text = mask.Display(bs[j].Text)
					foundText = true
					break
				}
			}
		}
		// The text search's own early exit must not cut the run-checking
		// walk short: the run may still need to confirm there is no
		// boundary-crossing reappearance (see above), which can require
		// scanning past the point where text was found.
		if foundText && (!runOK || failed) {
			break
		}
	}
	if runOK && !failed && !tailStale && (!cut || boundaryFound) {
		unresolved := 0
		var onlyID string
		for id := range runToolIDs {
			if !resolvedIDs[id] {
				unresolved++
				onlyID = id
			}
		}
		if unresolved == 1 && onlyID == tailToolID {
			p.AskFull = askFull
			p.askID = onlyID
		}
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

// cleanArg makes a tool argument safe to render as one printable line —
// shared by askLine and askRender (fix round 1: this used to be duplicated
// verbatim in both). Control characters (C0, DEL, C1) and Unicode format
// characters become spaces, and whitespace runs collapse, so a heredoc or an
// ANSI-laden echo still renders as one clean line. unicode.IsControl covers
// C0 (< 0x20), DEL (0x7f) AND the C1 range (U+0080-U+009F, e.g. U+009B CSI,
// U+009D OSC) — strings.Fields alone would not collapse a C1 control, since
// unicode.IsSpace does not treat it as whitespace. unicode.Cf (format
// characters: bidi overrides/isolates, zero-width joiners/spaces, …) is
// mapped the same way — those are not IsControl either, but a bidi override
// or a zero-width character reaching a rendered tool-call line is the same
// class of problem: it changes what the line looks like without changing
// what it says.
func cleanArg(v string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, v)
	return strings.Join(strings.Fields(clean), " ")
}

// askLine renders a pending tool call as one printable line: the tool name,
// plus the command (shell tools) or the path (file tools). Everything else is
// the name alone — its input is not something to read at a glance.
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
		s = name + "(" + cleanArg(mask.Display(arg)) + ")"
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

// askFullBlankRuns matches 4+ consecutive newlines (3+ blank lines) — the one
// whitespace-shaping askFullClean performs; see its doc comment.
var askFullBlankRuns = regexp.MustCompile(`\n{4,}`)

// askFullClean is askRender's OWN cleaner — deliberately NOT cleanArg, the
// one askLine (and the one-line sidebar/notification text) shares. cleanArg
// maps every control character, INCLUDING \n, to a space and then collapses
// whitespace runs — which is exactly the defect this closes (controller
// ruling, final fix wave, 2026-09-29, I1): a comment hiding a second command
// on its own line, "# harmless note\nrm -rf ~/x", rendered and HASHED
// identically to the comment-only "# harmless note rm -rf ~/x", so the card
// showed the harmless comment while Yes ran the hidden rm.
//
// \n and \t are kept — a heredoc or an indented script argument keeps its
// real, reviewable line structure. Every OTHER C0/DEL/C1 control character
// and every Unicode Cf format character (bidi overrides, zero-width
// joiners/spaces, …) becomes the VISIBLE U+FFFD replacement character —
// visible, unlike cleanArg's space, so a hidden character cannot pass as
// nothing. Whitespace runs are otherwise left exactly as written: this
// function must not lose or reshape anything the operator is approving. The
// one exception is a run of 3+ blank lines, collapsed to 2, so a heredoc with
// deliberately huge vertical whitespace does not make the card unreadable —
// cosmetic only, and never changes what the command DOES.
func askFullClean(v string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, v)
	return askFullBlankRuns.ReplaceAllString(clean, "\n\n\n")
}

// askRender renders a tool call as one untruncated, printable line — the tool
// name plus its primary argument — but ONLY for a tool in askableArgs; hasArg
// is false for every other tool, including one that has a primary argument
// that just isn't safe to answer from alone (see askableArgs), and for an
// allowed tool whose call used a different key (e.g. Read with "command"
// instead of "file_path" — not a shape Claude Code produces, but not trusted
// either).
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
	return name + "(" + askFullClean(v) + ")", true
}

// trailingToolUses counts the tool_use blocks at the END of a record's
// content, and reports whether the record held nothing else (all == true).
//
// Fix round 2 (2026-09-29): parsePeekTail's pending-run count no longer calls
// this — Claude Code can interleave a tool_use with a text block in one
// record (measured; also the `[tool_use B, text, tool_use C]` shape), so
// "trailing only" would miss B and undercount the run. parsePeekTail now
// scans every block in a run-matching record itself. This function is kept
// for the exact-name contract (constraints.md) and for a caller that
// genuinely wants "does this record end on nothing but tool_use" rather than
// "every tool_use anywhere in it".
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

// peekEntry is one cached parse, valid while the transcript's path, mtime AND
// size are unchanged — /clear moves a session to a new file. size guards
// against a coarse mtime (measured on this kernel): an edit landing in the
// same tick as the cached read leaves mtime unchanged while the file's
// content, and therefore its size, moved — without size in the key, that
// edit would never be seen again.
type peekEntry struct {
	path  string
	mtime time.Time
	size  int64
	peek  Peek
}

// peekCached returns the cached parse when it still matches path, mtime and size.
func (m *Manager) peekCached(id, path string, mtime time.Time, size int64) (Peek, bool) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	e, ok := m.peekCache[id]
	if !ok || e.path != path || !e.mtime.Equal(mtime) || e.size != size {
		return Peek{}, false
	}
	return e.peek, true
}

func (m *Manager) peekStore(id, path string, mtime time.Time, size int64, p Peek) {
	m.peekMu.Lock()
	defer m.peekMu.Unlock()
	if m.peekCache == nil {
		m.peekCache = map[string]peekEntry{}
	}
	m.peekCache[id] = peekEntry{path: path, mtime: mtime, size: size, peek: p}
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
	p, ok, reg := m.peekAndReg(id)
	return p, ok, reg == regStatusWaiting
}

// peekAndReg is PeekFor plus the live registry entry's status ("" when no
// entry validated). List feeds the status to regActivity.
func (m *Manager) peekAndReg(id string) (Peek, bool, string) {
	l, bound := m.boundTranscript(id)
	var reg string
	if l.reg != nil {
		reg = l.reg.Status
	}
	if !bound {
		return Peek{}, false, reg
	}
	var f *os.File
	var fi, tailFi os.FileInfo // fi keys the cache; tailFi sizes the tail read
	if l.boxDir == "" {
		// Host tier, unchanged: one stat, and the file is opened only on a miss.
		var err error
		if fi, err = os.Stat(l.path); err != nil {
			return Peek{}, false, reg
		}
		if p, ok := m.peekCached(id, l.path, fi.ModTime(), fi.Size()); ok {
			return p, true, reg
		}
		var ok bool
		if f, tailFi, ok = l.openTranscript(); !ok {
			return Peek{}, false, reg
		}
		defer f.Close()
	} else {
		// Boxed: open through boxread first and fstat the fd for the cache key,
		// so no stat of a box-chosen path ever runs outside the guard.
		var ok bool
		if f, fi, ok = l.openTranscript(); !ok {
			return Peek{}, false, reg
		}
		defer f.Close()
		if p, ok := m.peekCached(id, l.path, fi.ModTime(), fi.Size()); ok {
			return p, true, reg
		}
		tailFi = fi
	}
	b, cut, err := readUsageTail(f, tailFi)
	if err != nil {
		return Peek{}, false, reg
	}
	p, ok := parsePeekTailCut(b, cut, l.created)
	if !ok {
		return Peek{}, false, reg
	}
	if p.AskFull != "" {
		p.askSrcPath, p.askSrcMTime, p.askSrcSize = l.path, fi.ModTime(), fi.Size()
	}
	m.peekStore(id, l.path, fi.ModTime(), fi.Size(), p)
	return p, true, reg
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
	repliesOn := m.repliesOn
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	// The kill-switch comes first and refuses outright, so a session that
	// somehow still holds replyable=true (a stale state) cannot type into the PTY.
	if !repliesOn {
		return ErrRepliesDisabled
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
	p, havePeek, reg := m.peekAndReg(id)
	if !havePeek {
		return ErrNoTranscript
	}
	// The registry may only tighten the PTY verdict here (replyActivity): a
	// busy turn is never typed into, and a forged idle never loosens it.
	_, silent := s.quietActivity()
	act := replyActivity(info.Activity, reg, silent)
	// blocked joins pending: the registry can see a prompt before (or without)
	// the transcript's tool_use, and a line typed into a prompt answers it.
	if ok, why := canReply(info.Replyable, act, p.Pending || reg == regStatusWaiting); !ok {
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
