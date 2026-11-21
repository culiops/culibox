"use strict";

// Path actions for the file viewer (copy path:line, insert at the prompt, reveal
// in the tree). Pure: no DOM, no globals from other scripts, so `node --test`
// reaches it. The DOM half lives in viewer-tab.js, filetree.js and app.js.

// Same set as upload.js UNSAFE_TEXT (C0 + DEL + C1): a line break typed at a
// prompt would submit it. Duplicated rather than read from upload.js so this
// file stays loadable on its own; the VP_ prefix keeps the classic-script scope
// clear of upload.js's name.
const VP_UNSAFE = /[\x00-\x1f\x7f-\x9f]/;

// vpUnder: the part of `path` below `root`, or null when it is not inside it.
// A whole-component compare: "/h/pp" is not inside "/h/p".
function vpUnder(root, path) {
  if (!root || !path) return null;
  const r = root === "/" ? "/" : root.replace(/\/+$/, "");
  const prefix = r === "/" ? "/" : r + "/";
  if (path.indexOf(prefix) !== 0) return null;
  const rest = path.slice(prefix.length);
  return rest === "" ? null : rest;
}

// ancestorsBetween: the directories strictly between root and the file,
// outermost first; null when the path is not under root.
function ancestorsBetween(root, path) {
  const rest = vpUnder(root, path);
  if (rest === null) return null;
  const parts = rest.split("/");
  parts.pop(); // the file itself
  const base = root === "/" ? "" : root.replace(/\/+$/, "");
  const out = [];
  let cur = base;
  for (const p of parts) { cur += "/" + p; out.push(cur); }
  return out;
}

// pathLineText: `path` or `path:N`, refused when it holds a control character.
function pathLineText(path, line) {
  const text = line === undefined || line === null ? path : path + ":" + line;
  if (VP_UNSAFE.test(text)) return { ok: false, reason: "unsafe" };
  return { ok: true, text };
}

// relativePath: `path` relative to `root`, or null when it is outside.
function relativePath(root, path) { return vpUnder(root, path); }

// relativeAvail: the Copy relative path item's state. A box file's path means
// nothing on the host, and a file outside the tree's root has no honest
// relative form.
function relativeAvail(viewer, root) {
  if (viewer.box) return { ok: false, title: "This file is inside a session's box, so it has no path relative to the tree" };
  const rel = relativePath(root, viewer.path);
  if (rel === null) return { ok: false, title: "This file is outside the tree's root" };
  return { ok: true, text: rel };
}

// insertTargetFor: which terminal an "insert at prompt" goes to. A box file is
// only valid inside its own box, so a box viewer takes only the terminal whose
// id is viewer.box; a host path is valid in any session.
function insertTargetFor(viewer, candidate) {
  if (!candidate) return { ok: false, title: "Open this file from a session's pane to insert at its prompt" };
  if (viewer.box && candidate.id !== viewer.box) return { ok: false, title: "This file is inside another session's box" };
  return { ok: true, term: candidate };
}

// vpRoots: the roots revealable checks against: an array, or one string (an old caller's home), or
// (undefined) what the page holds: window.WORKSPACE_ROOTS plus window.HOME_DIR (filetree.js's ftCurrentRoots,
// kept here too because the editor page loads this file without filetree.js). Trailing slashes cut.
function vpRoots(roots) {
  let a = roots;
  if (a === undefined) {
    a = [];
    if (typeof window !== "undefined") {
      if (Array.isArray(window.WORKSPACE_ROOTS)) a = window.WORKSPACE_ROOTS.slice();
      if (window.HOME_DIR && !a.includes(window.HOME_DIR)) a.push(window.HOME_DIR);
    }
  }
  if (typeof a === "string") a = [a];
  if (!Array.isArray(a)) return [];
  return a.filter((r) => typeof r === "string" && r !== "").map((r) => (r.length > 1 ? r.replace(/\/+$/, "") || "/" : r));
}

// revealable: may the tree show this file? Not a box file (host listings would
// show an unrelated directory of the same name), and not outside every workspace
// root (home and the folders added to the list), where /api/fs/list answers 403.
// `roots` is an array or one string; omitted, the page's own. With no root known yet
// the check cannot run, so it allows (treeFollowTarget's convention).
function revealable(viewer, roots) {
  if (viewer.box) return { ok: false, title: "This file is inside a session's box, not on the host" };
  const list = vpRoots(roots);
  if (list.length && !list.some((r) => r === "/" || viewer.path.indexOf(r + "/") === 0)) {
    return { ok: false, title: "This file is outside your workspace roots, which the tree cannot list" };
  }
  return { ok: true };
}

// alternatesOutcome: what the viewer does with GET /api/fs/alternates after a 404
// (#53). `list` is the response's `candidates` (newest first, worktree "" = the
// main checkout). none: today's "not found". one: re-point the tab. pick: the
// in-pane list. Entries with no string path are dropped (agent-shaped data, never
// trusted), the order is re-established by mtime (stable: ties keep the server's
// order, a missing mtime sorts last) and the list is capped at the server's 64.
const VP_ALT_MAX = 64;
function alternatesOutcome(list) {
  const items = (Array.isArray(list) ? list : [])
    .filter((c) => c && typeof c.path === "string" && c.path)
    .map((c) => ({ path: c.path, worktree: typeof c.worktree === "string" ? c.worktree : "", mtime: Number.isFinite(c.mtime) ? c.mtime : 0 }));
  if (items.length === 0) return { kind: "none" };
  if (items.length === 1) return { kind: "one", path: items[0].path, worktree: items[0].worktree };
  const sorted = items
    .map((it, i) => ({ it, i }))
    .sort((a, b) => b.it.mtime - a.it.mtime || a.i - b.i)
    .map((x) => x.it);
  return { kind: "pick", items: sorted.slice(0, VP_ALT_MAX) };
}

// altNote: the one-line note over a file opened from another checkout. The name is
// split out so the DOM half can put it in <code>; both parts go in via textContent.
function altNote(worktree) {
  if (!worktree) return { pre: "Opened from the main checkout \u2014 not found in this worktree.", name: "", post: "" };
  return { pre: "Opened from worktree ", name: worktree, post: " \u2014 not found here." };
}

// agoText: a coarse "5 min ago" for a unix mtime (seconds), given the clock `now`
// (seconds). "" for an unknown mtime; a future one (clock skew) reads as just now.
function agoText(mtime, now) {
  if (!Number.isFinite(mtime) || mtime <= 0) return "";
  const s = now - mtime;
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + " min ago";
  if (s < 86400) return Math.floor(s / 3600) + " h ago";
  return Math.floor(s / 86400) + " d ago";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { ancestorsBetween, pathLineText, relativePath, relativeAvail, insertTargetFor, revealable, alternatesOutcome, altNote, agoText };
}
