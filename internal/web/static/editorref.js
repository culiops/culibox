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
// marked readonly, and a text file the server says is not UTF-8 (`utf8: false`): its
// bytes cannot travel through the buffer, so a save would change them. Only a kind of
// "text" is ever editable: a kind this build does not know is opened read-only rather
// than risk a save of something that is not text.
function editorMode(file) {
  const { kind, readonly, box, utf8 } = file || {};
  if (kind === "image") return "image";
  if (kind === "binary") return "binary";
  if (kind === "text" && !readonly && !box && utf8 !== false) return "edit";
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

// selectionLines turns an editor selection (Monaco's startLineNumber/startColumn/endLineNumber/endColumn, start before end) into the
// lines it covers, {a, b}, or null for an empty one. A selection that ends at the very start of a later line (a click on a line
// number, or a drag down to the next line's start) covers the lines before that one: "lines 3-5" is what the user pointed at, not
// "3-6". That is also what a line-number click gives, since Monaco selects the whole line, its line break included.
function selectionLines(sel) {
  if (!sel || typeof sel !== "object") return null;
  const { startLineNumber: sl, startColumn: sc, endLineNumber: el, endColumn: ec } = sel;
  if (![sl, sc, el, ec].every(Number.isInteger) || sl < 1 || el < sl) return null;
  if (sl === el && sc >= ec) return null;
  return { a: sl, b: el > sl && ec === 1 ? el - 1 : el };
}

// --- Follow agent ----------------------------------------------------------------------------------------------------------
// What the dock's watch (dockwatch.js) hands a subscriber is `files` ([{path, add, del, ts}], the edits that were applied) and
// `events` (the timeline events newer than the newest it had seen). Follow turns that into the one file to show, if any.

// FOLLOW_TOOLS: the tools whose finished call wrote a file. Anything else the agent does (a Read, a Bash command) is not a change.
const FOLLOW_TOOLS = ["Edit", "Write", "MultiEdit", "NotebookEdit"];

// followMarked: a path that is not one. Activity masks what looks like a credential ("[masked:github-token]") and cuts a row at 300
// characters with an ellipsis (internal/session/timeline.go tlDetail), and neither names a file that exists.
function followMarked(p) {
  return typeof p !== "string" || p === "" || p.includes("[masked:") || p.endsWith("\u2026");
}

// followFile is the entry of `files` an event's `detail` names, or null. The detail is the path the agent wrote, masked and cut,
// and `files[].path` is that same string for the edit it counts (timeline.go sets both from one value), so the entry, the path
// the dock already treats as a changed file, is what Follow opens: never the detail on its own. A relative detail names the one
// entry that ends with it ("src/a.go" in "/w/src/a.go"); more than one, or none, names nothing.
function followFile(files, detail) {
  if (followMarked(detail) || !Array.isArray(files)) return null;
  const list = files.filter((f) => f && !followMarked(f.path));
  const exact = list.find((f) => f.path === detail);
  if (exact) return exact;
  if (detail.charAt(0) === "/") return null;
  const tail = "/" + detail.replace(/^\.\//, "");
  const hits = list.filter((f) => f.path.endsWith(tail));
  return hits.length === 1 ? hits[0] : null;
}

// followPlan -> {path, seen}: the file to show for one answer of the watch ("" for none) and the newest edit time (`ts` of an
// entry of `files`, ms) that has now been looked at. `inRoot(path)` says whether a path is under the dock's root (an edit
// elsewhere is not the dock's business). `seen` is null until the first answer after Follow subscribed: that one only sets the mark.
//   1. The newest event that is a finished call of a FOLLOW_TOOLS tool (state "ok"; a call still pending or failed changed
//      nothing) whose file `files` has and that is under the root.
//   2. Else, once there is a mark: the newest entry of `files` whose `ts` passed it. This is the edit the watch first saw as a
//      pending call (an agent waiting on a permission prompt, or a poll in the middle of the call): its row keeps its timestamp
//      when the call finishes, so it is never a "new event" again, but `files` only gains the edit when it is applied.
function followPlan(files, events, inRoot, seen) {
  const list = Array.isArray(files) ? files.filter((f) => f && typeof f.path === "string") : [];
  const under = typeof inRoot === "function" ? inRoot : () => false;
  const top = list.reduce((n, f) => (Number.isFinite(f.ts) && f.ts > n ? f.ts : n), seen === null ? 0 : seen);
  for (const e of Array.isArray(events) ? events : []) {
    if (!e || e.kind !== "tool" || e.state !== "ok" || !FOLLOW_TOOLS.includes(e.tool)) continue;
    const f = followFile(list, e.detail);
    if (f && under(f.path)) return { path: f.path, seen: top };
  }
  if (seen !== null) {
    let best = null;
    for (const f of list) {
      if (!Number.isFinite(f.ts) || f.ts <= seen || followMarked(f.path) || !under(f.path)) continue;
      if (!best || f.ts > best.ts) best = f;
    }
    if (best) return { path: best.path, seen: top };
  }
  return { path: "", seen: top };
}

// followParse reads what is stored under agent-box:dockFollow, {sid: true}. The text can be edited by hand, so anything but a
// string key with the value true is dropped, and a non-object is nothing. The result has no prototype: a session id is a key.
function followParse(raw) {
  const out = Object.create(null);
  let o = null;
  try { o = typeof raw === "string" ? JSON.parse(raw) : null; } catch (_) { o = null; }
  if (!o || typeof o !== "object" || Array.isArray(o)) return out;
  for (const k of Object.keys(o)) if (o[k] === true && k !== "") out[k] = true;
  return out;
}

// followWith is `map` with `sid` turned on or off, as a new map. A session that no longer exists is dropped with it (`live` is
// the list of session ids that do; an empty or missing list says nothing, so nothing is dropped on a page that has no list yet).
function followWith(map, sid, on, live) {
  const out = Object.create(null);
  const keep = Array.isArray(live) && live.length ? new Set(live) : null;
  for (const k of Object.keys(map || {})) if (map[k] === true && (!keep || keep.has(k))) out[k] = true;
  if (typeof sid === "string" && sid !== "") { if (on) out[sid] = true; else delete out[sid]; }
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    rangeRef, insertGate, editorMode, conflictChoice, DELETED_TEXT, conflictText,
    selectionLines, FOLLOW_TOOLS, followFile, followPlan, followParse, followWith,
  };
}
