"use strict";

// Companion-shell nesting: a shell started from a session carries that
// session's id as parent_id and is listed under it, not as a row of its own.
// Pure decisions, no DOM. Classic script: the three names below are browser
// globals used by mobileshell.js and deskshell.js (so this loads before both);
// under node they are required.
//
// A shell is nested only when its parent is in the list AND the parent is not
// itself a shell. The second rule keeps a hand-made parent chain or cycle from
// hiding sessions: only top-level rows are ever walked for children, so a
// nested row whose parent is also nested would never be listed at all.

function shellParentOf(s, sessions) {
  if (!s || !s.parent_id) return null;
  for (const p of sessions || []) {
    if (p && p.id === s.parent_id) return p.parent_id ? null : p;
  }
  return null;
}

// shellChildren: Map<parentId, shells[]>, each list oldest first (ties and
// missing `created` keep list order). Only shells whose parent is present count.
function shellChildren(sessions) {
  const out = new Map();
  const list = sessions || [];
  const when = (s) => { const t = Date.parse(s.created); return Number.isNaN(t) ? Infinity : t; };
  list.forEach((s, i) => {
    if (!shellParentOf(s, list)) return;
    if (!out.has(s.parent_id)) out.set(s.parent_id, []);
    out.get(s.parent_id).push({ s, i, t: when(s) });
  });
  for (const [k, v] of out) {
    v.sort((a, b) => (a.t === b.t ? a.i - b.i : a.t < b.t ? -1 : 1));
    out.set(k, v.map((x) => x.s));
  }
  return out;
}

// isNestedShell: listed under a parent that is present in `sessions`.
function isNestedShell(s, sessions) {
  return shellParentOf(s, sessions) !== null;
}

// shellLabel: "shell N", N being the 1-based place among its parent's shells by
// creation time; the session's own name when it is not nested.
function shellLabel(s, sessions) {
  if (!isNestedShell(s, sessions)) return s ? s.name : "";
  // By id, not identity: a caller may hold a copy of the row's session.
  const idx = shellChildren(sessions).get(s.parent_id).findIndex((c) => c.id === s.id);
  return "shell " + (idx + 1);
}

// liveShellsOf: the parent's nested shells that have not exited, oldest first.
// "detached" still counts: the process is alive, only no client is attached.
function liveShellsOf(parentId, sessions) {
  return (shellChildren(sessions).get(parentId) || []).filter((s) => s.status !== "exited");
}

// killDialogOpts: the question confirmKill asks. With no live shells it is the
// plain End dialog, word for word what shipped before nesting; with n > 0 it
// adds the second way out ("End session only" leaves the shells as top-level
// rows, the parent being gone).
function killDialogOpts(name, id, n) {
  const label = name || id;
  if (!(n > 0)) {
    return {
      title: "End session",
      body: 'End session "' + label + '"? This cannot be undone.',
      confirmLabel: "End",
      danger: true,
    };
  }
  return {
    title: "End session",
    body: 'End "' + label + '" and its ' + n + (n === 1 ? " shell" : " shells") + "? This cannot be undone.",
    confirmLabel: "End all",
    altLabel: "End session only",
    danger: true,
  };
}

// killIdsFor: which sessions to end for the user's answer, parent first.
function killIdsFor(outcome, parentId, shells) {
  if (outcome === "confirm") return [parentId, ...(shells || []).map((s) => s.id)];
  if (outcome === "alt") return [parentId];
  return [];
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { shellChildren, isNestedShell, shellLabel, liveShellsOf, killDialogOpts, killIdsFor };
}
