"use strict";

// The review loop's pure model for the Files dock (spec "Review loop (P3)"): which changed files the user has marked reviewed, how to
// step from change to change in a diff, and whether the paste bar may type into the agent's terminal. DOM-free so `node --test`
// reaches it; the diff header (F7, Mark reviewed) and the paste bar call it.
//
// Reviewed marks. A mark is {path, at, fp, seen}: the file's absolute path, when it was marked, the fingerprint of the Changes row at
// that moment, and the newest agent edit of the file the page knew of. A mark stands only while it is still true (reviewedState): the
// row's fingerprint is the same, and the agent has not edited the file since. Either one changing means the user has not seen what is
// there now. reviewMarkFor builds a mark, so every caller builds it the same way.
//   fp    "<code>:<add>:<del>", the row's git letter and numstat counts (reviewFp). A row with no counts (untracked, provisional) has
//         empty counts, "U::", so its fingerprint is stable and not "U:null:null" or "U:undefined:undefined" by accident of the source.
//   seen  the `ts` of the file's entry in Activity's files list when it was marked, 0 when the file was not in the list. Activity's
//         `ts` is unix MILLISECONDS (session.FileChange.TS) stamped by the box server from the agent's transcript, which may be on
//         another machine than the browser, so it is only ever compared with another Activity `ts`, never with the browser's clock:
//         an edit is new when its ts is greater than `seen`, whatever the two clocks say.
//   at    the browser's Date.now() when it was marked (ms). For display only; no decision reads it.
//
// Persistence. One JSON record in localStorage, {v:1, sessions:{<sid>:{marks:{<abs path>:<mark>}}}}, in the shape and with the
// safety of notesmodel.js: dead sessions and malformed marks are dropped on load (and from storage), a missing, corrupt or
// wrong-version record reads as no marks, and a blocked or full storage is ignored on save (marks then live for the page only).
const REVIEWED_KEY = "agent-box:reviewed";

// The fingerprint of a Changes row (changesRows): the one both the mark and the later comparison use.
function reviewFp(row) {
  const n = (v) => (v === null || v === undefined ? "" : String(v));
  return n(row && row.code) + ":" + n(row && row.add) + ":" + n(row && row.del);
}

// The newest `ts` Activity has for `path` in `files` (the session's Activity list, entries {path, ts, ...}), 0 when there is none. A
// missing list, a null entry or an entry with no ts says nothing.
function reviewSeen(files, path) {
  let seen = 0;
  if (Array.isArray(files)) {
    for (const f of files) {
      if (f && typeof f === "object" && f.path === path && Number.isFinite(f.ts) && f.ts > seen) seen = f.ts;
    }
  }
  return seen;
}

// The mark for `row` made now (`now`: the browser's Date.now(), ms, display only) against Activity's `files` as they stand.
function reviewMarkFor(row, files, now) {
  return { path: row.path, at: now, fp: reviewFp(row), seen: reviewSeen(files, row.path) };
}

// Is `mark` (for the row's file) still a review of what the row shows now? False when there is no mark (or one without a server
// baseline), it is for another path, the row's fingerprint moved, or Activity has an edit of the file newer than the mark's `seen`
// (a file that was not in the list when marked has seen 0, so its first entry clears the mark).
function reviewedState(mark, row, files) {
  if (!mark || !row || mark.path !== row.path || mark.fp !== reviewFp(row) || !Number.isFinite(mark.seen)) return false;
  return reviewSeen(files, row.path) <= mark.seen;
}

// {done, total} over the Changes rows: `marks` is an object keyed by abs path. A mark whose file has no row counts for nothing.
function reviewedCount(rows, marks, files) {
  const list = Array.isArray(rows) ? rows : [];
  const m = marks && typeof marks === "object" ? marks : {};
  let done = 0;
  for (const r of list) {
    if (r && Object.prototype.hasOwnProperty.call(m, r.path) && reviewedState(m[r.path], r, files)) done++;
  }
  return { done, total: list.length };
}

// Step from the cursor to the next (dir 1) or previous (dir -1) change of a file. `lineChanges` is lineDiff's list, [{kind, from, to}]
// in order with 1-based lines in the new text (a "del" sits at one line, from === to). Each entry is one change that starts at `from`;
// `index` is its 0-based position. Forward is the first change that starts after the cursor line; backward is the last change that
// starts strictly before the cursor line (as VS Code does), so from inside a change it goes to that change's start, and from its
// start to the one before. null past the last (forward) or before the first (backward): the caller moves on to the neighbouring file.
// A "del" at the very end of the text carries line = lastLine + 1 (see lineDiff), so the caller clamps `line` to the buffer's last line
// before it reveals it. Null or non-object entries in the list are skipped.
function nextChange(lineChanges, cursorLine, dir) {
  if (!Array.isArray(lineChanges) || !Number.isFinite(cursorLine) || (dir !== 1 && dir !== -1)) return null;
  if (dir === 1) {
    for (let i = 0; i < lineChanges.length; i++) {
      if (lineChanges[i] && lineChanges[i].from > cursorLine) return { index: i, line: lineChanges[i].from };
    }
    return null;
  }
  for (let i = lineChanges.length - 1; i >= 0; i--) {
    if (lineChanges[i] && lineChanges[i].from < cursorLine) return { index: i, line: lineChanges[i].from };
  }
  return null;
}

// --- the diff header and the Changes list ----------------------------------------------------------------------

// The Changes rows a review walks and counts: the files git has answered for. A folder (an untracked directory is a place in the
// tree, not a file) and a provisional row (the agent's edit git has not been asked about: no diff to read yet) are not in it.
function reviewRows(rows) {
  return Array.isArray(rows) ? rows.filter((r) => r && typeof r === "object" && !r.folder && !r.provisional) : [];
}

// The row F7 moves on to from the file at `fromPath`: the next (dir 1) or the previous (dir -1) of `rows` in the order the Changes
// section shows them, null past either end. A file that has no row (the open file is unchanged) has no neighbour.
function reviewNeighbour(rows, fromPath, dir) {
  const list = reviewRows(rows);
  const at = list.findIndex((r) => r.path === fromPath);
  if (at < 0 || (dir !== 1 && dir !== -1)) return null;
  return list[at + dir] || null;
}

// The row Review opens: the first one whose mark does not stand (reviewedState), else the first row (every file reviewed: Review
// still takes the reader somewhere). null when there is no row.
function reviewFirstOpen(rows, marks, files) {
  const list = reviewRows(rows);
  const m = marks && typeof marks === "object" ? marks : {};
  return list.find((r) => !(Object.prototype.hasOwnProperty.call(m, r.path) && reviewedState(m[r.path], r, files))) || list[0] || null;
}

// The line a change is shown at: its start, clamped into the buffer ("del" at the very end of the text carries lastLine + 1, and a
// removal in an emptied file sits at line 1).
function reviewLineOf(change, lineCount) {
  const last = Number.isFinite(lineCount) && lineCount >= 1 ? Math.floor(lineCount) : 1;
  return Math.max(1, Math.min(change.from, last));
}

// "Change N of M": {n, m, text}. M is the number of changes. N is the one the reader is at: the one F7 last landed on (`navIndex`)
// while the cursor is still on its line (a click elsewhere is no longer "at" it), else the number of changes that start at or
// before the cursor line (0 before the first). The text says "No changes", "M changes" when the cursor is before the first, or
// "Change N of M".
function reviewChangePos(lineChanges, cursorLine, navIndex, lineCount) {
  const list = Array.isArray(lineChanges) ? lineChanges.filter((c) => c && typeof c === "object") : [];
  const m = list.length;
  let n = 0;
  if (Number.isInteger(navIndex) && navIndex >= 0 && navIndex < m && reviewLineOf(list[navIndex], lineCount) === cursorLine) {
    n = navIndex + 1;
  } else {
    for (const c of list) if (reviewLineOf(c, lineCount) <= cursorLine) n++;
  }
  const text = m === 0 ? "No changes" : n === 0 ? m + (m === 1 ? " change" : " changes") : "Change " + n + " of " + m;
  return { n, m, text };
}

// Pending notes per file: {<abs path>: count} from a notes list (notesmodel.js entries); sent notes are not waiting, so they are not
// counted. Anything that is not a list, or an entry that is not a note, counts for nothing.
function notesPendingByPath(list) {
  const out = {};
  if (!Array.isArray(list)) return out;
  for (const n of list) {
    if (n && typeof n === "object" && n.status === "pending" && typeof n.path === "string") out[n.path] = (out[n.path] || 0) + 1;
  }
  return out;
}

// May the paste bar type into this session's terminal right now? `term` is the session's TerminalTab (its `.term` is the xterm
// instance). {ok, label, why}: the label is always "Paste to <name>" and `why` says why not ("" when ok). One refusal is reported
// when several apply, in this order: no terminal, working, waiting on anything but a finished turn, bracketed paste off. Ruling R13:
// need "turn" passes, as in insertGate (editorref.js): a Claude session that finished its turn is {activity: "waiting", need: "turn"}
// (internal/session/peek.go), and that open prompt is where the review message belongs; need "ask", "sso", "store_locked", any other
// need, and a waiting session with no need are refused. A session that is neither idle, working nor waiting (done, crashed, exited)
// has no terminal to paste into and reads "Not connected". Only a strict `true` from xterm passes, so a program that never asked
// for bracketed paste gets no block typed at it. This is the agent-state half; the visibility half (insertDecision) is the bar's.
function pasteGate(session, term) {
  if (!session) return { ok: false, label: "Paste to session", why: "Not connected" };
  const name = session.name;
  const no = (why) => ({ ok: false, label: "Paste to " + name, why });
  if (!term || !term.term) return no("Not connected");
  if (session.activity === "working") return no(name + " is working; notes wait here");
  const turn = session.need === "turn";
  if (turn && session.activity !== "waiting" && session.activity !== "idle") return no("Not connected"); // a finished turn of a session that is over
  if (!turn && (session.activity === "waiting" || session.need)) return no(name + " is waiting on you; answer it first");
  if (!turn && session.activity !== "idle") return no("Not connected");
  if (!term.term.modes || term.term.modes.bracketedPasteMode !== true) return no("This program does not accept pasted blocks");
  return { ok: true, label: "Paste to " + name, why: "" };
}

// The names of the open files that have pending notes and an unsaved edit (ruling R12). A note's line number is the open buffer's, but
// the agent reads the file on disk, so a note on a file with unsaved edits would point at the wrong line there. `tabs` is
// [{path, name, dirty}], `list` the session's notes. Each name once, in the tabs' order.
function dirtyNotedNames(tabs, list) {
  const pend = notesPendingByPath(list);
  const out = [];
  for (const t of Array.isArray(tabs) ? tabs : []) {
    if (!t || !t.dirty || !Object.prototype.hasOwnProperty.call(pend, t.path) || pend[t.path] <= 0) continue;
    if (!out.includes(t.name)) out.push(t.name);
  }
  return out;
}

// What the review bar shows and whether Paste may act, from what the page knows now: {session (the sessions row or null), term (the
// session's TerminalTab or null), connected (its socket is open: a paste into a closed one goes nowhere, and must not mark the notes
// sent), list (the session's notes), tabs (see dirtyNotedNames)}. -> {shape, pending, sent, label, ok, why}.
//   shape  "full" while any note is pending (the bar with Preview and Paste), "sent" when only sent ones are left (the slim bar that
//          keeps the Sent toggle reachable), "none".
//   ok     pasteGate says ok, the socket is open, and no file with pending notes has unsaved edits; `why` is the one sentence for
//          the button's title and the toast. The gate's own refusal wins over the unsaved-file one.
// It decides only the agent-state half and the notes half; whether the terminal is on screen is insertDecision's, asked at the click.
function pasteBarState(o) {
  const list = Array.isArray(o && o.list) ? o.list : [];
  let pending = 0;
  let sent = 0;
  for (const n of list) {
    if (n && n.status === "pending") pending += 1;
    else if (n && n.status === "sent") sent += 1;
  }
  const shape = pending > 0 ? "full" : sent > 0 ? "sent" : "none";
  const gate = pasteGate(o && o.session, o && o.term);
  const out = { shape, pending, sent, label: gate.label, ok: gate.ok, why: gate.why };
  if (out.ok && o.connected !== true) { out.ok = false; out.why = "Not connected"; }
  if (out.ok) {
    const names = dirtyNotedNames(o.tabs, list);
    if (names.length) { out.ok = false; out.why = "Save " + names.join(", ") + " first: the agent reads the file on disk"; }
  }
  return out;
}

function markValid(path, m) {
  return !!m && typeof m === "object" && m.path === path && typeof m.fp === "string" && Number.isFinite(m.at) && Number.isFinite(m.seen) && m.seen >= 0;
}

// The stored record, or an empty one when it is missing, unreadable, corrupt or of another version.
function reviewedRead(storage) {
  try {
    const o = JSON.parse(storage.getItem(REVIEWED_KEY) || "null");
    if (o && typeof o === "object" && o.v === 1 && o.sessions && typeof o.sessions === "object" && !Array.isArray(o.sessions)) return o;
  } catch (_) { /* fall through */ }
  return { v: 1, sessions: {} };
}

// Replaces session `sid`'s marks in the record; an empty object removes the entry so the record does not grow.
function saveReviewed(storage, sid, marks) {
  try {
    const rec = reviewedRead(storage);
    if (marks && Object.keys(marks).length) rec.sessions[sid] = { marks };
    else delete rec.sessions[sid];
    storage.setItem(REVIEWED_KEY, JSON.stringify(rec));
  } catch (_) { /* blocked or full: the marks live for this page only */ }
}

// Map of sid -> marks (an object keyed by abs path) for the sessions in `liveSids` (an array or a Set). Never throws. What it drops (a
// dead session, a malformed mark) is also removed from the stored record, as loadNotes does.
function loadReviewed(storage, liveSids) {
  const out = new Map();
  try {
    const live = new Set(liveSids);
    const rec = reviewedRead(storage);
    let dropped = false;
    for (const sid of Object.keys(rec.sessions)) {
      const s = rec.sessions[sid];
      const raw = live.has(sid) && s && s.marks && typeof s.marks === "object" && !Array.isArray(s.marks) ? s.marks : null;
      const marks = {};
      let n = 0;
      if (raw) for (const p of Object.keys(raw)) if (markValid(p, raw[p])) { marks[p] = raw[p]; n++; }
      if (!raw || n !== Object.keys(raw).length) dropped = true;
      if (n) { out.set(sid, marks); rec.sessions[sid] = { marks }; } else delete rec.sessions[sid];
    }
    if (dropped) {
      try { storage.setItem(REVIEWED_KEY, JSON.stringify(rec)); } catch (_) { /* blocked or full: the next load tries again */ }
    }
  } catch (_) { /* no marks */ }
  return out;
}

// --- the All changes tab (reviewall.js) -------------------------------------------------------------------------------------

const REVIEW_ALL_MAX = 100;     // sections the tab makes; past it the Changes list is how files are opened one by one
const REVIEW_ALL_HEIGHT = 1200; // px: a section's diff editor is as tall as its content up to here, then scrolls inside

// The sections of the tab for the Changes rows: the files (reviewRows) in the section's own order, the first REVIEW_ALL_MAX of them,
// and how many were left out. {rows, more}.
function reviewAllRows(rows) {
  const files = reviewRows(rows);
  return { rows: files.slice(0, REVIEW_ALL_MAX), more: Math.max(0, files.length - REVIEW_ALL_MAX) };
}

// Where the section of `path` is among `shown` (reviewAllRows' rows), -1 when it has none (a folder, a path past the cap).
function reviewAllIndex(shown, path) {
  return Array.isArray(shown) ? shown.findIndex((r) => r && r.path === path) : -1;
}

// A section's editor height: its content's, at least 0 and at most REVIEW_ALL_HEIGHT; a height that is no number is 0.
function reviewAllHeight(h) {
  const n = Number(h);
  return Number.isFinite(n) ? Math.min(REVIEW_ALL_HEIGHT, Math.max(0, Math.round(n))) : 0;
}

// Which sections get a diff editor and which lose theirs. `order` is the sections' paths in order; `live` the paths that have an editor
// now; `near` those within a screen of the viewport (they need one); `far` those within two screens (the ones that may keep theirs).
// {create, dispose} in `order`: a near section without an editor gets one, an editor whose section is out of `far` goes. A section that is
// near but not in `far` cannot happen for one observer pair; it is kept rather than made and dropped in one step.
function reviewAllPlan(order, live, near, far) {
  const L = live instanceof Set ? live : new Set(live || []);
  const N = near instanceof Set ? near : new Set(near || []);
  const F = far instanceof Set ? far : new Set(far || []);
  const list = Array.isArray(order) ? order : [];
  return {
    create: list.filter((p) => N.has(p) && !L.has(p)),
    dispose: list.filter((p) => L.has(p) && !N.has(p) && !F.has(p)),
  };
}

// F7 / Shift+F7 across the sections. `secs` is one entry per section in order: {kind: "text", changes (lineDiff's list), lineCount} for
// a section whose diff is computed, {kind: "stop"} for one with nothing to step through (an image, a binary file, one that could not be
// read: the reader stops at it once), {kind: "unknown"} for one that has no diff yet (it is not made, or not read). `from` is {idx, line}:
// the section the reader is in and the line of the cursor in it (0: before the first line). The answer:
//   {idx, at: "change", index, line}  a change of a computed section (the next one in the section, else the first/last of the next one
//                                     that has any); `line` is clamped into the buffer as the single-file view does
//   {idx, at: "section", land}        a section to go to and land in when its diff is there: "first" going forward, "last" going back
//   null                              past the last (forward) or before the first (backward)
function reviewAllStepPlan(secs, from, dir) {
  if (!Array.isArray(secs) || (dir !== 1 && dir !== -1) || !from || !Number.isInteger(from.idx)) return null;
  const shown = (s) => (Array.isArray(s.changes) ? s.changes : []).filter((c) => c && typeof c === "object").map((c) => Object.assign({}, c, { from: reviewLineOf(c, s.lineCount) }));
  const line = Number.isFinite(from.line) ? from.line : 0;
  const cur = secs[from.idx];
  if (cur && cur.kind === "text") {
    const hit = nextChange(shown(cur), line, dir);
    if (hit) return { idx: from.idx, at: "change", index: hit.index, line: hit.line };
  }
  for (let i = from.idx + dir; i >= 0 && i < secs.length; i += dir) {
    const s = secs[i];
    if (!s || typeof s !== "object") continue;
    if (s.kind === "unknown" || s.kind === "stop") return { idx: i, at: "section", land: dir === 1 ? "first" : "last" };
    if (s.kind !== "text") continue;
    const list = shown(s);
    if (!list.length) continue;
    const index = dir === 1 ? 0 : list.length - 1;
    return { idx: i, at: "change", index, line: list[index].from };
  }
  return null;
}

// --- image diff (imagediff.js) ---------------------------------------------------------------------------------------------

// imgNum: v as a finite-or-infinite number, or NaN for anything that is not one (null, "", "  ", an object: Number() would make
// 0 of the first three, and the divider or the blend must not jump to an end for want of a value). A numeric string is a number:
// an <input type=range> hands its value over as one.
function imgNum(v) {
  if (typeof v === "number") return v;
  if (typeof v === "string" && v.trim() !== "") return Number(v);
  return NaN;
}

// swipeClip: where the Swipe divider is, as a percent (0..100) of the image's width, from `x` px in a box `width` px wide. The
// new image is clipped to the part right of it. Clamped, so a drag past either edge sits on the edge; no usable width (not laid
// out yet: zero, negative, not a number) or position gives the middle (50), never NaN.
function swipeClip(x, width) {
  const w = imgNum(width);
  const px = imgNum(x);
  if (!(w > 0) || w === Infinity || Number.isNaN(px)) return 50;
  return Math.min(100, Math.max(0, (px / w) * 100));
}

// onionOpacity: the opacity (0..1) of the new image over the old one for the Onion skin slider's value (0..100). Clamped; a value
// that is no number is the half-way blend (0.5), the slider's own start.
function onionOpacity(v) {
  const n = imgNum(v);
  if (Number.isNaN(n)) return 0.5;
  return Math.min(1, Math.max(0, n / 100));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    REVIEWED_KEY, reviewFp, reviewMarkFor, reviewedState, reviewedCount, nextChange, pasteGate, dirtyNotedNames, pasteBarState, saveReviewed, loadReviewed,
    reviewRows, reviewNeighbour, reviewFirstOpen, reviewLineOf, reviewChangePos, notesPendingByPath, swipeClip, onionOpacity,
    REVIEW_ALL_MAX, REVIEW_ALL_HEIGHT, reviewAllRows, reviewAllIndex, reviewAllHeight, reviewAllPlan, reviewAllStepPlan,
  };
}
