"use strict";

// The review loop's pure model for the Files dock (spec "Review loop (P3)"): which changed files the user has marked reviewed, how to
// step from change to change in a diff, and whether the paste bar may type into the agent's terminal. DOM-free so `node --test`
// reaches it; the diff header (F7, Mark reviewed) and the paste bar call it.
//
// Reviewed marks. A mark is {path, at, fp}: the file's absolute path, when it was marked (ms, Date.now()) and the fingerprint of
// the Changes row at that moment. A mark stands only while it is still true (reviewedState): the row's fingerprint is the same, and the
// agent has not edited the file since. Either one changing means the user has not seen what is there now.
//   fp   "<code>:<add>:<del>", the row's git letter and numstat counts (reviewFp). A row with no counts (untracked, provisional) has
//        empty counts, "U::", so its fingerprint is stable and not "U:null:null" or "U:undefined:undefined" by accident of the source.
//   ts   Activity's file `ts` is unix MILLISECONDS (session.FileChange.TS), the same unit as mark.at, so the two are compared as they
//        are. (Not seconds: nothing is converted, and a test pins it.)
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

// Is `mark` (for the row's file) still a review of what the row shows now? False when there is no mark, it is for another path, the
// row's fingerprint moved, or Activity says the agent edited the file after the mark (`files`: the session's Activity list, entries
// {path, ts, ...}; a missing list or an entry with no ts says nothing).
function reviewedState(mark, row, files) {
  if (!mark || !row || mark.path !== row.path || mark.fp !== reviewFp(row)) return false;
  if (Array.isArray(files)) {
    for (const f of files) {
      if (f && f.path === row.path && Number.isFinite(f.ts) && f.ts > mark.at) return false;
    }
  }
  return true;
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
function nextChange(lineChanges, cursorLine, dir) {
  if (!Array.isArray(lineChanges) || !Number.isFinite(cursorLine) || (dir !== 1 && dir !== -1)) return null;
  if (dir === 1) {
    for (let i = 0; i < lineChanges.length; i++) {
      if (lineChanges[i].from > cursorLine) return { index: i, line: lineChanges[i].from };
    }
    return null;
  }
  for (let i = lineChanges.length - 1; i >= 0; i--) {
    if (lineChanges[i].from < cursorLine) return { index: i, line: lineChanges[i].from };
  }
  return null;
}

// May the paste bar type into this session's terminal right now? `term` is the session's TerminalTab (its `.term` is the xterm
// instance). {ok, label, why}: the label is always "Paste to <name>" and `why` says why not ("" when ok). One refusal is reported
// when several apply, in this order: no terminal, working, waiting or any need, bracketed paste off. A session that is neither idle,
// working nor waiting (done, crashed, exited) has no terminal to paste into and reads "Not connected". Only a strict `true` from
// xterm passes, so a program that never asked for bracketed paste gets no block typed at it.
function pasteGate(session, term) {
  if (!session) return { ok: false, label: "Paste to session", why: "Not connected" };
  const name = session.name;
  const no = (why) => ({ ok: false, label: "Paste to " + name, why });
  if (!term || !term.term) return no("Not connected");
  if (session.activity === "working") return no(name + " is working; notes wait here");
  if (session.activity === "waiting" || session.need) return no(name + " is waiting on you; answer it first");
  if (session.activity !== "idle") return no("Not connected");
  if (!term.term.modes || term.term.modes.bracketedPasteMode !== true) return no("This program does not accept pasted blocks");
  return { ok: true, label: "Paste to " + name, why: "" };
}

function markValid(path, m) {
  return !!m && typeof m === "object" && m.path === path && typeof m.fp === "string" && Number.isFinite(m.at);
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    REVIEWED_KEY, reviewFp, reviewedState, reviewedCount, nextChange, pasteGate, saveReviewed, loadReviewed,
  };
}
