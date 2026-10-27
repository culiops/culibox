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
  const idx = shellChildren(sessions).get(s.parent_id).indexOf(s);
  return "shell " + (idx + 1);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { shellChildren, isNestedShell, shellLabel };
}
