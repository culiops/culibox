"use strict";

// The editor's small policy decisions that are not about Monaco: what reference to
// type into a prompt, whether a session may be typed into, how a file is opened,
// and what a changed or failed file means for the buffer. Pure and DOM-free so
// `node --test` reaches all of it; editortab.js and the insert paths only act on
// the answers.

// rangeRef is the text "Insert at prompt" types for a selection: "rel:a" for one
// line, "rel:a-b" for several. A selection made upwards arrives as (high, low); it
// is written low-high, because "f.go:12-7" does not say which end is which.
function rangeRef(rel, a, b) {
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  return lo === hi ? rel + ":" + lo : rel + ":" + lo + "-" + hi;
}

// insertGate decides whether text may be typed at a session's prompt. The editor
// never presses Enter, but typing into a prompt that is waiting on a permission
// choice or a sign-in would put the text where an answer is expected, so a session
// that waits on the user for anything but a finished turn is refused.
//   ok       idle or working, with no need; or need "turn".
//   refused  need "ask", "sso", "store_locked" or any other non-empty need; activity
//            "waiting" with no need; an exited session; no session.
// need "turn" passes on purpose (ruling R1): a Claude session that finished its turn
// is {activity: "waiting", need: "turn"} (internal/session/peek.go), and that is the
// open prompt the user wants a reference typed into.
function insertGate(session) {
  if (!session) return { ok: false, why: "There is no session to insert into" };
  const name = session.name || session.id || "the session";
  // "done" is the activity of a session that exited, and "crashed" of one that died.
  if (session.exited || session.activity === "done" || session.activity === "crashed") return { ok: false, why: name + " has exited" };
  if (session.need === "turn") return { ok: true, why: "" };
  if (session.need || session.activity === "waiting") return { ok: false, why: name + " is waiting on you; answer it first" };
  if (session.activity === "idle" || session.activity === "working") return { ok: true, why: "" };
  return { ok: false, why: name + " is not ready for input" }; // detached, or an activity this build does not know
}

// editorMode is how the dock opens a file: "edit" (Monaco, writable), "read"
// (Monaco, read-only), "image" or "binary" (the viewer's own bodies). A file in a
// session's box is read-only (viewer-tab.js enterEdit), as is a file the server
// marked readonly. Only a kind of "text" is ever editable: a kind this build does not
// know is opened read-only rather than risk a save of something that is not text.
function editorMode(file) {
  const { kind, readonly, box } = file || {};
  if (kind === "image") return "image";
  if (kind === "binary") return "binary";
  if (kind === "text" && !readonly && !box) return "edit";
  return "read";
}

// conflictChoice says what the editor must do about its file. saveStatus is the
// HTTP status of the last save (or re-read) of the file: 409 means the file changed
// under the buffer, 404 that it is gone.
//   deleted  the file is gone, dirty or not.
//   bar      the amber conflict bar: a 409, or a dirty buffer whose disk version is
//            no longer the one it started from. Never replaced without a choice.
//   reload   a clean buffer whose disk version changed: swapped in place.
//   none     nothing to do. A version not known yet is not a change.
function conflictChoice(state) {
  const { dirty, diskVersion, baseVersion, saveStatus } = state || {};
  if (saveStatus === 404) return "deleted";
  if (saveStatus === 409) return "bar";
  if (diskVersion && baseVersion && diskVersion !== baseVersion) return dirty ? "bar" : "reload";
  return "none";
}

// DELETED_TEXT is the conflict bar's sentence for a file that is gone from disk.
const DELETED_TEXT = "This file was deleted on disk.";

// conflictText is the conflict bar's sentence as [lead, rest]: the lead ("<session> changed <name>") is drawn bold. A dock
// with no session name says the file changed on disk, rather than "undefined changed ...".
function conflictText(session, name) {
  const who = typeof session === "string" && session.trim() !== "" ? session.trim() : "";
  return [who ? who + " changed " + name : name + " changed on disk", " after you started editing. Your edits are not saved yet."];
}

// textEOL is the line ending a Monaco model takes from `text` (its own rule: CRLF when more than half of the line breaks
// are, else LF), or null when `text` has no line break and the model keeps the one it has. Lone CRs are not counted.
function textEOL(text) {
  const lf = (text.match(/\n/g) || []).length;
  if (lf === 0) return null;
  const crlf = (text.match(/\r\n/g) || []).length;
  return crlf * 2 > lf ? "\r\n" : "\n";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { rangeRef, insertGate, editorMode, conflictChoice, DELETED_TEXT, conflictText, textEOL };
}
