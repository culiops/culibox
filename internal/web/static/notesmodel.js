"use strict";

// Review notes for the Files dock (spec "Review loop (P3)"): the pure model under the notes UI and the paste bar. A note is a
// comment on one line of a file, kept on the page (never on the server) and pasted to the agent's prompt as one message.
//   {id, path, rel, side, line, quote, text, status, outdated, at}
//   path      the file's absolute path (what reanchor matches on); rel is the name the agent sees and the message prints;
//   side      "new": a line of the file as it is now, so the note moves with its text (reanchor); "old": a removed line, read from
//             the base text in Side by side. The base never changes under a note, so an old note is never moved: when HEAD or the
//             scope does move the base, reanchorOld only checks its quote against it (outdated or not);
//   line      1-based, in the file (new) or in the base text (old);
//   quote     the line's text when the note was made: it is both what the agent is shown and what reanchor looks for;
//   status    "pending" (not sent yet) or "sent" (kept so the user can check the fix against it);
//   outdated  the quote is no longer found near `line`: the note keeps its old line number and the message says so;
//   at        when it was made (ms).
// Every function returns a new list and leaves its input alone, so the UI can compare before and after.
//
// What goes to the terminal. composeMessage builds the text the paste bar types into the agent's PTY, and the note text, the quote
// and the file name all come from the page (the quote from a file the agent may have written), so none of it is trusted: every
// control character except "\n" and "\t" is removed (stripControls). That includes ESC, so a quote holding "\x1b[201~" cannot close
// the bracketed paste early and run the rest as typed input, and the C1 range (0x80-0x9f, where "\x9b" is a one-byte CSI). The
// message is capped at MSG_MAX characters; Enter is never part of it.
//
// Persistence. One JSON record in localStorage, {v:1, sessions:{<sid>:{notes:[...]}}}. loadNotes drops sessions that are gone (the
// caller names the live ones), a missing, corrupt or wrong-version record reads as no notes, and a blocked or full storage is
// ignored on save, as in viewerprefs.js: notes then live for the page only.
const NOTES_KEY = "agent-box:notes";
const MSG_MAX = 16000;
const REANCHOR_WINDOW = 50;

// Unique within the page: the time in ms plus a counter, so two notes made in the same ms differ.
let noteSeq = 0;
function noteNewId(now) { noteSeq += 1; return "n" + now.toString(36) + "-" + noteSeq.toString(36); }

function noteAdd(list, o, now) {
  const at = typeof now === "number" ? now : Date.now();
  const n = {
    id: noteNewId(at), path: o.path, rel: o.rel, side: o.side === "old" ? "old" : "new",
    line: o.line, quote: o.quote, text: o.text, status: "pending", outdated: false, at,
  };
  return list.concat([n]);
}

function noteEdit(list, id, text) {
  return list.map((n) => (n.id === id ? Object.assign({}, n, { text }) : n));
}

function noteDelete(list, id) {
  return list.filter((n) => n.id !== id);
}

// Where do the new-side notes of `path` sit in `newText` (the file as it is now)? The quote at its line: kept. Else the nearest
// line within `window` lines either side that equals the quote exactly (a tie goes to the earlier line): moved there. Else the note
// is outdated and keeps its line. A note that was outdated and finds its quote again is current again.
function reanchor(list, path, newText, window) {
  const win = typeof window === "number" ? window : REANCHOR_WINDOW;
  const lines = String(newText).split(/\r\n|\n|\r/);
  return list.map((n) => {
    if (n.path !== path || n.side !== "new") return n;
    if (lines[n.line - 1] === n.quote) return n.outdated ? Object.assign({}, n, { outdated: false }) : n;
    for (let d = 1; d <= win; d++) {
      // Earlier line first, so a tie is deterministic.
      for (const at of [n.line - d, n.line + d]) {
        if (at >= 1 && at <= lines.length && lines[at - 1] === n.quote) return Object.assign({}, n, { line: at, outdated: false });
      }
    }
    return n.outdated ? n : Object.assign({}, n, { outdated: true });
  });
}

// The base text moved (a commit, the other scope): the old-side notes of `path` are checked against it. An old note is about a line of
// the base text, which never moves under it, so it does not move with a new one: it keeps its line, and is outdated when the base's
// line there is no longer its quote (and current again when it is). A new-side note is none of this function's business.
function reanchorOld(list, path, baseText) {
  const lines = String(baseText).split(/\r\n|\n|\r/);
  return list.map((n) => {
    if (n.path !== path || n.side !== "old") return n;
    const stands = lines[n.line - 1] === n.quote;
    return stands === !n.outdated ? n : Object.assign({}, n, { outdated: !stands });
  });
}

// CR LF and a lone CR become LF; then every other control character is removed, except TAB (0x09) and LF (0x0a).
function stripControls(s) {
  return String(s).replace(/\r\n?/g, "\n").replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f]/g, "");
}

// The message for the pending notes, by file name then line. {ok:true, text} or {ok:false, why}; why is for the paste bar.
function composeMessage(notes) {
  const pend = notes.filter((n) => n.status === "pending");
  if (pend.length === 0) return { ok: false, why: "no pending notes" };
  // Code-unit order, not localeCompare: the same notes give the same message on every machine and locale.
  pend.sort((a, b) => (a.rel < b.rel ? -1 : a.rel > b.rel ? 1 : 0) || a.line - b.line || a.at - b.at);
  let text = "Review notes on your changes:\n";
  for (const n of pend) {
    // One note is one "- " line, one "  > " line and indented continuation lines, so a reader (the agent) can always tell where a note
    // ends. A newline is the only way to fake a structural line, and it survives stripControls, so it is dealt with here, after the
    // CR normalisation (a lone CR is already a LF by then): rel and quote are single-line by definition, so a newline in them (a file
    // name the agent made, a pasted quote) becomes a space; the note's text may have several lines, and every continuation line is
    // indented by 4 spaces, so none can start with "- " at column 0 or "  > " at column 2.
    const rel = stripControls(n.rel).replace(/\n/g, " ");
    const quote = stripControls(n.quote).replace(/\n/g, " ");
    const body = stripControls(n.text).replace(/\n/g, "\n    ");
    const where = n.side === "old" ? rel + " (removed line " + n.line + ")" : rel + ":" + n.line;
    text += "- " + where + (n.outdated ? " (line moved or removed since)" : "") + " — " + body + "\n  > " + quote + "\n";
  }
  text = stripControls(text);
  if (text.length > MSG_MAX) return { ok: false, why: "too long; paste fewer notes" };
  return { ok: true, text };
}

function markSent(list, ids) {
  const set = new Set(ids);
  return list.map((n) => (set.has(n.id) ? Object.assign({}, n, { status: "sent" }) : n));
}

// A stored note must have the shape the rest of the page reads; anything else is dropped rather than trusted.
function noteValid(n) {
  return !!n && typeof n === "object" && typeof n.id === "string" && typeof n.path === "string" && typeof n.rel === "string" &&
    (n.side === "new" || n.side === "old") && Number.isFinite(n.line) && typeof n.quote === "string" && typeof n.text === "string" &&
    (n.status === "pending" || n.status === "sent") && typeof n.outdated === "boolean" && Number.isFinite(n.at);
}

// The stored record, or an empty one when it is missing, unreadable, corrupt or of another version.
function notesRead(storage) {
  try {
    const o = JSON.parse(storage.getItem(NOTES_KEY) || "null");
    if (o && typeof o === "object" && o.v === 1 && o.sessions && typeof o.sessions === "object" && !Array.isArray(o.sessions)) return o;
  } catch (_) { /* fall through */ }
  return { v: 1, sessions: {} };
}

// Replaces session `sid`'s notes in the record; an empty list removes the entry so the record does not grow.
function saveNotes(storage, sid, list) {
  try {
    const rec = notesRead(storage);
    if (list.length) rec.sessions[sid] = { notes: list };
    else delete rec.sessions[sid];
    storage.setItem(NOTES_KEY, JSON.stringify(rec));
  } catch (_) { /* blocked or full: the notes live for this page only */ }
}

// Map of sid -> notes for the sessions in `liveSids` (an array or a Set). Never throws. What it drops (a dead session, a malformed
// note) is also removed from the stored record, so a session that is gone does not sit in localStorage until the quota is full and
// saveNotes can no longer write.
function loadNotes(storage, liveSids) {
  const out = new Map();
  try {
    const live = new Set(liveSids);
    const rec = notesRead(storage);
    let dropped = false;
    for (const sid of Object.keys(rec.sessions)) {
      const s = rec.sessions[sid];
      const notes = live.has(sid) && s && Array.isArray(s.notes) ? s.notes.filter(noteValid) : [];
      if (!live.has(sid) || !s || !Array.isArray(s.notes) || notes.length !== s.notes.length) dropped = true;
      if (notes.length) { out.set(sid, notes); rec.sessions[sid] = { notes }; } else delete rec.sessions[sid];
    }
    if (dropped) {
      try { storage.setItem(NOTES_KEY, JSON.stringify(rec)); } catch (_) { /* blocked or full: the next load tries again */ }
    }
  } catch (_) { /* no notes */ }
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    NOTES_KEY, MSG_MAX, noteAdd, noteEdit, noteDelete, reanchor, reanchorOld, stripControls,
    composeMessage, markSent, saveNotes, loadNotes,
  };
}
