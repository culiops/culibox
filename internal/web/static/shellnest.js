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
//
// A third rule: the parent must not be newer than the shell. A row with the
// parent's id but created after the shell is a later session that reused a
// freed id (ids restart from live sessions after a daemon restart), and must
// never adopt the shell or end it with itself. A missing `created` on either
// side does not block nesting.

function shellParentOf(s, sessions) {
  if (!s || !s.parent_id) return null;
  for (const p of sessions || []) {
    if (p && p.id === s.parent_id) {
      if (p.parent_id) return null;
      return Date.parse(p.created) > Date.parse(s.created) ? null : p;
    }
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

// shellList: "shell 1", "shell 1 and shell 2", "shell 1, shell 2 and shell 3".
function shellList(names) {
  if (names.length < 2) return names.join("");
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

// killDialogDeskOpts: the desktop End session dialog (frame M5.2): what ends, what
// is left alone, and the second way out named for what it keeps. shellNames are
// the live shells' labels ("shell 1"); cwd is the workspace as the page shows it
// ("~/…"), "" when the session has none, which drops the Files note rather than
// leaving a sentence about nothing. Both answers mean what killIdsFor says:
// confirm ends the parent and its shells, alt the parent only. One shell is the
// frame's "End both"; two or more keep today's "End all", since "both" would not
// be true. The phone keeps killDialogOpts.
function killDialogDeskOpts(name, id, shellNames, cwd) {
  const label = name || id;
  const names = shellNames || [];
  const n = names.length;
  const notes = [];
  if (cwd) notes.push(["Files in its workspace, ", { mono: cwd }, ", are not touched."]);
  const opts = {
    title: "End session " + label + "?",
    body: "The agent stops now and " + label + " leaves the sidebar. An ended session can't be restarted.",
    notes,
    confirmLabel: "End session",
    danger: true,
    cls: "end",
  };
  if (!(n > 0)) return opts;
  const only = "\u201cEnd " + label + " only\u201d";
  notes.push(["It has " + n + (n === 1 ? " shell" : " shells") + " open, " + shellList(names) + ". " + only +
    " keeps " + (n === 1 ? "that shell running as its own row." : "those shells running as their own rows.")]);
  opts.confirmLabel = n === 1 ? "End both" : "End all";
  opts.altLabel = "End " + label + " only";
  return opts;
}

// killIdsFor: which sessions to end for the user's answer, parent first.
function killIdsFor(outcome, parentId, shells) {
  if (outcome === "confirm") return [parentId, ...(shells || []).map((s) => s.id)];
  if (outcome === "alt") return [parentId];
  return [];
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { shellChildren, isNestedShell, shellLabel, liveShellsOf, killDialogOpts, killDialogDeskOpts, killIdsFor };
}
