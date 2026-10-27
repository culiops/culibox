"use strict";

// The Files drawer's pure decisions (spec 5.4): which header actions are
// enabled, the per-session {path, scroll} memory, and when a change of drawer
// state leaves the file it shows. No DOM; filesdrawer.js wires them. Classic
// script: browser globals, required under node.

// drawerActions(state) -> which header buttons are enabled.
// state = {path|null, editing, box, dirty, image?}. With no file nothing is
// (the ✕ is not this function's business: it is always there). Editing swaps
// the whole row for Cancel · Save, dirty or not: Save of an unchanged buffer is
// harmless, and the row must not jump when the first key is typed. Edit needs a
// host file (a box file is read-only: the host cannot write into the box) that
// is text (an image has nothing to copy or edit).
function drawerActions(state) {
  const s = state || {};
  const has = !!s.path;
  const editing = has && !!s.editing;
  const read = has && !editing;
  return {
    copy: read && !s.image,
    insert: read,
    edit: read && !s.box && !s.image,
    openInPane: read,
    overflow: read,
    cancel: editing,
    save: editing,
  };
}

// Per-session memory, in memory only: a session id (null for a pane of files)
// -> the file the drawer last showed for it and how far down it was read
// (plus `box`, the session a box-only file is read through, when there is one).
// Returns a new Map; the one passed in is left as it was.
function rememberDrawerFile(map, sid, file) {
  const out = new Map(map || []);
  const path = file && file.path;
  if (!path) { out.delete(sid); return out; }
  const n = Number(file.scroll);
  const f = { path, scroll: Number.isFinite(n) && n > 0 ? Math.round(n) : 0 };
  // A box viewer's identity carries its session (viewer-tab.js); keep it.
  if (typeof file.box === "string" && file.box) f.box = file.box;
  out.set(sid, f);
  return out;
}

function drawerFileFor(map, sid) {
  if (!map || !map.has(sid)) return null;
  const f = map.get(sid);
  return f.box ? { path: f.path, scroll: f.scroll, box: f.box } : { path: f.path, scroll: f.scroll };
}

// drawerLeavesFile: does going from `prev` to `next` drawer state take the
// Files view away from the session it was showing? (Closing, switching to
// Access, or moving to another session.) That is when a dirty buffer must ask.
function drawerLeavesFile(prev, next) {
  if (!prev || prev.open !== "files") return false;
  if (!next || next.open !== "files") return true;
  return next.sessionId !== prev.sessionId;
}

// discardAsk: the askConfirm options for throwing away a dirty drawer buffer.
function discardAsk(name) {
  return {
    title: "Discard unsaved changes to " + name + "?",
    body: "Your edits have not been saved.",
    confirmLabel: "Discard",
    danger: true,
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { drawerActions, rememberDrawerFile, drawerFileFor, drawerLeavesFile, discardAsk };
}
