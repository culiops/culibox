"use strict";

// The rows of the explorer's Changes section, as one pure function: which files
// the workspace has changed, in what order, with what +a −d. No DOM, and the two
// functions it takes from other scripts are pure too, so `node --test` reaches
// it; dockchanges.js paints the rows. Classic script: browser globals, required
// under node.
//
// A row's path is mapped from the reply's repo-relative `p` by gitstatus.js's
// gitPlace, the rule the tree's marks use, and "mine" is decided by drawerfiles.js's
// changedSegs, the rule behind the tree's session bars. Both are reached at call
// time (a global in the page, a require under node), so a Changes row and its
// tree row cannot disagree about a path.

const CHANGES_MAX = 200; // rows shown; the rest are counted in `more`

// Letters a reply may carry. Numstat has no C (git diff calls an unmerged file
// a modification) and its U is the web layer's untracked file; status has all
// five. Anything else is dropped, so a stray letter never becomes a class name.
// Sets, not objects: "constructor" or "__proto__" must not pass for a letter.
const CHANGES_NUM_CODES = new Set(["M", "A", "U", "D"]);
const CHANGES_STATUS_CODES = new Set(["M", "A", "U", "D", "C"]);

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
  const placeOf = typeof gitPlace === "function" ? gitPlace : require("./gitstatus.js").gitPlace;
  const segsOf = typeof changedSegs === "function" ? changedSegs : require("./drawerfiles.js").changedSegs;
  const head = scope !== "branch";
  const base = root.replace(/\/+$/, "");
  const byPath = new Map();
  const add = (at, code, a, d) => {
    byPath.set(at.abs, {
      path: at.abs,
      name: at.segs[at.segs.length - 1],
      dir: at.segs.slice(0, -1).join("/"),
      code,
      add: a,
      del: d,
      mine: false,
      folder: at.isDir,
    });
  };

  if (numstat && numstat.state === "ok" && Array.isArray(numstat.entries)) {
    for (const e of numstat.entries) {
      if (!e || !CHANGES_NUM_CODES.has(e.x)) continue;
      const at = placeOf(e.p, numstat.rootRel, base);
      if (!at || at.isDir || byPath.has(at.abs)) continue; // numstat never lists a folder
      // A binary file has no lines to count, and a pair with one bad half is not
      // shown as a half-known "+? −3": unknown, not 0.
      const a = changesCount(e.a);
      const d = changesCount(e.d);
      const known = e.bin !== true && a !== null && d !== null;
      add(at, e.x, known ? a : null, known ? d : null);
    }
  }
  if (Array.isArray(status.entries)) {
    for (const e of status.entries) {
      if (!e || !CHANGES_STATUS_CODES.has(e.x)) continue;
      const at = placeOf(e.p, status.rootRel, base);
      if (!at) continue;
      // Every directory entry is an untracked folder, whatever its letter, as in gitMarks.
      const code = at.isDir ? "U" : e.x;
      if (!head && code !== "U") continue;
      const have = byPath.get(at.abs);
      if (have) {
        if (head && code === "C") have.code = "C";
        continue;
      }
      add(at, code, null, null);
    }
  }

  // Position in `files`, keyed by the normalized path; the first of a repeat wins.
  const keyOf = (p) => {
    const segs = segsOf(p);
    return segs ? "/" + segs.join("/") : null;
  };
  const order = new Map();
  if (Array.isArray(files)) {
    files.forEach((f, i) => {
      const key = f ? keyOf(f.path) : null;
      if (key !== null && !order.has(key)) order.set(key, i);
    });
  }
  const ranked = [];
  for (const row of byPath.values()) {
    const key = keyOf(row.path);
    const at = key === null ? undefined : order.get(key);
    if (at !== undefined) row.mine = true;
    ranked.push({ row, at: at === undefined ? -1 : at });
  }
  ranked.sort((x, y) => {
    if ((x.at >= 0) !== (y.at >= 0)) return x.at >= 0 ? -1 : 1;
    if (x.at >= 0) return x.at - y.at;
    return x.row.path < y.row.path ? -1 : x.row.path > y.row.path ? 1 : 0;
  });
  return { rows: ranked.slice(0, CHANGES_MAX).map((x) => x.row), more: Math.max(0, ranked.length - CHANGES_MAX) };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { CHANGES_MAX, changesRows };
}
