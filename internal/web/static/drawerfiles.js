"use strict";

// The Files drawer's pure decisions (spec 5.4): the per-session {path, scroll}
// memory, and when a change of drawer state leaves the file it shows. No DOM;
// filesdrawer.js wires them. Classic script: browser globals, required under node.
// (The viewer's buttons are the shared toolbar's: viewertoolbar.js.)

// Per-session memory, in memory only: a session id (null for a pane of files)
// -> the file the drawer last showed for it and how far down it was read.
// Returns a new Map; the one passed in is left as it was.
function rememberDrawerFile(map, sid, file) {
  const out = new Map(map || []);
  const path = file && file.path;
  if (!path) { out.delete(sid); return out; }
  const n = Number(file.scroll);
  out.set(sid, { path, scroll: Number.isFinite(n) && n > 0 ? Math.round(n) : 0 });
  return out;
}

function drawerFileFor(map, sid) {
  if (!map || !map.has(sid)) return null;
  const f = map.get(sid);
  return { path: f.path, scroll: f.scroll };
}

// drawerTreeBlocked: should the tree column say "can't be browsed" instead of
// showing a tree? Yes when the drawer names a session whose workspace is known
// and is one /api/fs/list refuses (rootable = filetree.js's rootableCwd, the
// S1 rule): the tree would otherwise still show some other place, under a
// header naming this session. A cwd not known yet holds; no session, no block.
function drawerTreeBlocked(sid, cwd, rootable) {
  return !!sid && !!cwd && !rootable;
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

// goToFileURL: the Files drawer's "Go to file…" query. The existing bounded,
// indexed filename search (no new endpoint, and not a search inside files);
// prefer=<tree root> lists that subtree's matches first.
function goToFileURL(q, root) {
  let u = "/api/fs/search?limit=40&q=" + encodeURIComponent(q);
  if (root) u += "&prefer=" + encodeURIComponent(root);
  return u;
}

// hitsUnderRoot: the hits whose path lies inside `root`. The search also
// returns matches elsewhere under $HOME (the preferred subtree only comes
// first), but the drawer's tree shows one root, so a jump must stay in it.
// Compared on a path boundary: root /home/u/a does not own /home/u/ab/x. No
// root, no hits.
function hitsUnderRoot(hits, root) {
  if (!Array.isArray(hits) || !root) return [];
  const prefix = String(root).replace(/\/+$/, "") + "/";
  return hits.filter((h) => !!h && typeof h.path === "string" && h.path.startsWith(prefix) && h.path.length > prefix.length);
}

// changedSet: the paths to mark with the agent-changed dot in the tree under
// `root`. `files` is the activity reply's [{path, add, del}] (absolute paths as
// the agent wrote them). The set holds each changed file and every folder
// between it and the root, so a collapsed folder shows the dot too; the root
// itself is never in it, and a path outside the root (or relative, or equal to
// the root) is ignored. Paths are normalized (no `.`, `..`, doubled or
// trailing slashes) and compared by segment, so /w2/x does not belong to /w.
function changedSet(files, root) {
  const out = new Set();
  const base = changedSegs(root);
  if (!base || !Array.isArray(files)) return out;
  for (const f of files) {
    const segs = f && typeof f.path === "string" ? changedSegs(f.path) : null;
    if (!segs || segs.length <= base.length) continue;
    if (!base.every((seg, i) => segs[i] === seg)) continue;
    for (let n = base.length + 1; n <= segs.length; n++) out.add("/" + segs.slice(0, n).join("/"));
  }
  return out;
}

// changedSegs: the segments of an absolute path, normalized; null for anything
// else (not a string, relative, empty) and for a path that climbs above "/".
function changedSegs(p) {
  if (typeof p !== "string" || p.charAt(0) !== "/") return null;
  const out = [];
  for (const part of p.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") { if (!out.length) return null; out.pop(); continue; }
    out.push(part);
  }
  return out;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { rememberDrawerFile, drawerFileFor, drawerLeavesFile, discardAsk, drawerTreeBlocked, goToFileURL, hitsUnderRoot, changedSet };
}
