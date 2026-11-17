"use strict";

// The rows of the explorer's Changes section, as one pure function: which files
// the workspace has changed, in what order, with what +a −d. No DOM, no globals
// from other scripts, so `node --test` reaches it; dockchanges.js paints the
// rows. Classic script: browser globals, required under node.
//
// A row's path is mapped from the reply's repo-relative `p` the way gitMarks
// (gitstatus.js) maps a status entry, through the reply's rootRel and not its
// symlink-resolved `top`, so a row's path is the one the tree's row for that
// file carries. The arithmetic is repeated here, not called, so this file loads
// on its own.

const CHANGES_MAX = 200; // rows shown; the rest are counted in `more`

// Letters a reply may carry. Numstat has no C (git diff calls an unmerged file
// a modification) and its U is the web layer's untracked file; status has all
// five. Anything else is dropped, so a stray letter never becomes a class name.
const CHANGES_NUM_CODES = { M: 1, A: 1, U: 1, D: 1 };
const CHANGES_STATUS_CODES = { M: 1, A: 1, U: 1, D: 1, C: 1 };

// changesSegs: the segments of an absolute path, normalized (no `.`, `..`,
// doubled or trailing slashes); null for anything else (not a string, relative,
// empty) and for a path that climbs above "/". The same rule as
// drawerfiles.js changedSegs, which decides which tree rows get a session bar,
// so a Changes row and its tree row agree on "mine".
function changesSegs(p) {
  if (typeof p !== "string" || p.charAt(0) !== "/") return null;
  const out = [];
  for (const part of p.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") { if (!out.length) return null; out.pop(); continue; }
    out.push(part);
  }
  return out;
}

// changesPlace puts one reply entry's `p` under the tree root: its absolute
// path, name, folder (dir, relative to the root, "" at the root) and whether it
// is a collapsed untracked folder (a trailing "/"). Null for an entry outside
// the shown tree, the root itself, a path with an empty segment, or a reply with
// no usable rootRel.
function changesPlace(p, rootRel, base) {
  if (typeof p !== "string" || typeof rootRel !== "string" || rootRel === "") return null;
  const folder = p.endsWith("/");
  let rel = folder ? p.slice(0, -1) : p;
  if (rootRel !== ".") {
    if (!rel.startsWith(rootRel + "/")) return null;
    rel = rel.slice(rootRel.length + 1);
  }
  if (rel === "") return null;
  const segs = rel.split("/");
  if (segs.some((s) => s === "")) return null;
  return { path: base + "/" + rel, name: segs[segs.length - 1], dir: segs.slice(0, -1).join("/"), folder };
}

// changesCount: a line count the reply gave, or null. Unknown is null and never
// 0: a count is shown only when git (or the web layer's own count) said it.
function changesCount(n) {
  return Number.isInteger(n) && n >= 0 ? n : null;
}

// changesRows: {rows, more} for the Changes section.
//   scope    "head" (uncommitted changes) or "branch" (HEAD against the merge
//            base, from /api/git/numstat's own scope); anything but "branch"
//            reads as "head".
//   status   the /api/git/status reply; only state "ok" yields rows.
//   numstat  the /api/git/numstat reply for the same scope, or null.
//   files    the Activity files of the session, most recently edited first.
//   root     the tree root, absolute.
//   statusAt when the status was asked for; Task 9c's provisional rows use it.
//
// A row is {path, name, dir, code, add, del, mine, folder}. Numstat gives the
// letter and the counts of each changed file, a deleted one included (the tree
// has no row for it). Status adds what numstat lacks: an untracked file or
// folder it could not count (no counts), and in "head" scope any other entry,
// so the list is not empty while numstat is slow or unavailable. In "branch"
// scope status's tracked entries are against HEAD, not the base, so only its
// untracked ones count. In "head" scope a conflict (C) wins over numstat's M,
// as the tree's badge says C.
//
// Counts come from numstat alone. Activity's add/del count edit strings, not the
// diff, so a file numstat does not list has null counts however much the agent
// typed. `mine` is true for a path in `files`. Mine come first in `files` order,
// then the rest by path (a plain code-unit compare: the same order whatever the
// browser's locale). Only CHANGES_MAX rows are returned, cut after the ordering
// so a row of this session is never the one dropped; `more` counts the rest.
function changesRows(opts) {
  const none = { rows: [], more: 0 };
  const { scope, status, numstat, files, root } = opts || {}; // statusAt: not read yet
  if (!status || status.state !== "ok" || typeof root !== "string" || root === "") return none;
  const head = scope !== "branch";
  const base = root.replace(/\/+$/, "");
  const byPath = new Map();
  const place = (at, code, add, del) => {
    byPath.set(at.path, { path: at.path, name: at.name, dir: at.dir, code, add, del, mine: false, folder: at.folder });
  };

  if (numstat && numstat.state === "ok" && Array.isArray(numstat.entries)) {
    for (const e of numstat.entries) {
      if (!e || !CHANGES_NUM_CODES[e.x]) continue;
      const at = changesPlace(e.p, numstat.rootRel, base);
      if (!at || at.folder || byPath.has(at.path)) continue; // numstat never lists a folder
      // A binary file has no lines to count, and a pair with one bad half is not
      // shown as a half-known "+? −3": unknown, not 0.
      const add = changesCount(e.a);
      const del = changesCount(e.d);
      const known = e.bin !== true && add !== null && del !== null;
      place(at, e.x, known ? add : null, known ? del : null);
    }
  }
  if (Array.isArray(status.entries)) {
    for (const e of status.entries) {
      if (!e || !CHANGES_STATUS_CODES[e.x]) continue;
      if (!head && e.x !== "U") continue;
      const at = changesPlace(e.p, status.rootRel, base);
      if (!at) continue;
      const have = byPath.get(at.path);
      if (have) {
        if (head && e.x === "C") have.code = "C";
        continue;
      }
      place(at, e.x, null, null);
    }
  }

  // Position in `files`, keyed by the normalized path; the first of a repeat wins.
  const order = new Map();
  if (Array.isArray(files)) {
    files.forEach((f, i) => {
      const segs = f ? changesSegs(f.path) : null;
      const key = segs ? "/" + segs.join("/") : null;
      if (key !== null && !order.has(key)) order.set(key, i);
    });
  }
  const ranked = [];
  for (const row of byPath.values()) {
    const segs = changesSegs(row.path);
    const at = segs ? order.get("/" + segs.join("/")) : undefined;
    if (at !== undefined) row.mine = true;
    ranked.push({ row, at: at === undefined ? -1 : at });
  }
  ranked.sort((a, b) => {
    if ((a.at >= 0) !== (b.at >= 0)) return a.at >= 0 ? -1 : 1;
    if (a.at >= 0) return a.at - b.at;
    return a.row.path < b.row.path ? -1 : a.row.path > b.row.path ? 1 : 0;
  });
  return { rows: ranked.slice(0, CHANGES_MAX).map((x) => x.row), more: Math.max(0, ranked.length - CHANGES_MAX) };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { CHANGES_MAX, changesRows };
}
