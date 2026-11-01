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

// revealable: may the tree show this file? Not a box file (host listings would
// show an unrelated directory of the same name), and not outside $HOME, where
// /api/fs/list answers 403. With no home known yet the check cannot run, so it
// allows (treeFollowTarget's convention).
function revealable(viewer, home) {
  if (viewer.box) return { ok: false, title: "This file is inside a session's box, not on the host" };
  if (home && viewer.path.indexOf(home.replace(/\/+$/, "") + "/") !== 0) {
    return { ok: false, title: "This file is outside your home folder, which the tree cannot list" };
  }
  return { ok: true };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { ancestorsBetween, pathLineText, relativePath, relativeAvail, insertTargetFor, revealable };
}
