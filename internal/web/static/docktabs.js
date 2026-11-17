"use strict";

// The Files dock's tabs, as pure decisions: the per-session tab list and its
// preview rule, which session the dock shows (the follow rule), what is stored
// across a reload, and the one-time move of today's pane viewer tabs into the
// dock. No DOM, no storage: filesdrawer.js and tabs.js wire it. Classic script:
// browser globals, required under node.
//
// A session's state is {tabs: [{key, path, box, scroll, preview}], active}. The
// key is dockTabKey(path, box), the format layout.js's tabKey gives a viewer, so
// a file open in a pane and the same file in the dock compare the same way. `box`
// is the session id of a file that exists only in that session's box, else null.
// `preview` is the italic tab the next single click replaces; a double-click, an
// edit or a save pins it (dockPin).
//
// Every function returns new objects and leaves its arguments as they were: the
// dock keeps one state per session and compares before and after to decide what
// to repaint and what to save.

const DOCK_TABS_KEY = "agent-box:dockTabs";
const DOCK_MAX_TABS = 20; // per session, in what is stored; a thousand-file session must not fill localStorage
const DOCK_TABS_V = 1;

// dockTabKey identifies one file tab: `v:<path>` on the host, `b:<box>:<path>` in
// a session's box. The same path in two places is two files, so the box comes
// first and is part of the key. A box that is not a non-empty string is ignored,
// as in layout.js: a hand-edited entry degrades to a host file rather than
// failing. No usable path, no key (null).
//
// This is not named tabKey: layout.js already owns that global (it takes a stored
// tab object and also keys terminals), and classic scripts share one scope.
function dockTabKey(path, box) {
  if (typeof path !== "string" || !path) return null;
  if (typeof box === "string" && box) return "b:" + box + ":" + path;
  return "v:" + path;
}

// dockScroll keeps an offset to a finite, non-negative whole number of pixels: a
// stored entry is user-editable text and goes straight into scrollTop. The same
// rule as layout.js's clampScroll, kept here so this file loads and tests alone.
function dockScroll(v) {
  const n = Math.round(Number(v));
  return Number.isFinite(n) && n > 0 ? n : 0;
}

// dockMakeTab builds one tab in its one shape, or null when there is no key. The
// key is always derived, never taken from stored text, so a forged one cannot
// make two tabs collide or one tab answer to the wrong file.
function dockMakeTab(path, box, scroll, preview) {
  const key = dockTabKey(path, box);
  if (!key) return null;
  return { key, path, box: typeof box === "string" && box ? box : null, scroll: dockScroll(scroll), preview: preview === true };
}

// dockTabsOf: the tab array of a state, or an empty one for a state that is not.
function dockTabsOf(st) {
  return st && Array.isArray(st.tabs) ? st.tabs : [];
}

// dockActiveOf: the active key of a state, null when there is none.
function dockActiveOf(st) {
  return st && typeof st.active === "string" ? st.active : null;
}

// dockCopyTabs: the tabs, each one a new object.
function dockCopyTabs(tabs) {
  return tabs.map((t) => ({ ...t }));
}

// dockOpen opens a file in the dock and makes its tab the active one.
//   - A file that already has a tab is activated, and pinned when `preview` is
//     false: a double-click on a preview tab pins it where it stands. A preview
//     open never unpins.
//   - A new preview takes the place of the session's current preview tab: same
//     slot, so the tabs around it do not move.
//   - Any other new tab goes in right after the active one (at the end when
//     nothing is active), where the eye already is. A preview with no preview
//     tab to replace is such a tab.
// `preview` is true only when given as true: a caller that does not say opens a
// pinned tab, which cannot be lost by the next click.
function dockOpen(st, o) {
  const tabs = dockTabsOf(st);
  const active = dockActiveOf(st);
  const key = o ? dockTabKey(o.path, o.box) : null;
  if (!key) return { tabs: dockCopyTabs(tabs), active };
  const preview = o.preview === true;
  const at = tabs.findIndex((t) => t.key === key);
  if (at >= 0) {
    return { tabs: tabs.map((t, i) => (i === at && !preview ? { ...t, preview: false } : { ...t })), active: key };
  }
  const tab = dockMakeTab(o.path, o.box, 0, preview);
  const next = dockCopyTabs(tabs);
  const old = preview ? next.findIndex((t) => t.preview) : -1;
  if (old >= 0) {
    next[old] = tab;
    return { tabs: next, active: key };
  }
  const after = next.findIndex((t) => t.key === active);
  next.splice(after < 0 ? next.length : after + 1, 0, tab);
  return { tabs: next, active: key };
}

// dockClose removes a tab. Closing the active one activates its right neighbour,
// else its left one, else nothing. Closing another tab leaves the active one be.
function dockClose(st, key) {
  const tabs = dockTabsOf(st);
  const active = dockActiveOf(st);
  const at = tabs.findIndex((t) => t.key === key);
  if (at < 0) return { tabs: dockCopyTabs(tabs), active };
  const rest = dockCopyTabs(tabs.filter((_, i) => i !== at));
  if (active !== key) return { tabs: rest, active };
  // The right neighbour has slid into slot `at`; the left one is still at at-1.
  const next = rest[at] || rest[at - 1];
  return { tabs: rest, active: next ? next.key : null };
}

// dockPin keeps a preview tab: an edit, a save or a double-click. A tab that is
// not a preview, or not there, is left as it is.
function dockPin(st, key) {
  return { tabs: dockTabsOf(st).map((t) => (t.key === key ? { ...t, preview: false } : { ...t })), active: dockActiveOf(st) };
}

// dockActivate shows an existing tab. A key with no tab keeps the current one.
function dockActivate(st, key) {
  const tabs = dockTabsOf(st);
  return { tabs: dockCopyTabs(tabs), active: tabs.some((t) => t.key === key) ? key : dockActiveOf(st) };
}

// dockSetScroll records how far down a tab was read, to come back to the same place.
function dockSetScroll(st, key, n) {
  return { tabs: dockTabsOf(st).map((t) => (t.key === key ? { ...t, scroll: dockScroll(n) } : { ...t })), active: dockActiveOf(st) };
}

// dockLeaveNeedsAsk: does leaving this session's tabs (switching session, closing
// the dock, following focus away) put an unsaved buffer at risk? `isDirty` is the
// DOM side's answer for one tab key. Any tab counts, not only the shown one: an
// edit in a background tab survives a tab switch, so it must survive this too, or
// ask first.
function dockLeaveNeedsAsk(st, isDirty) {
  return typeof isDirty === "function" && dockTabsOf(st).some((t) => !!isDirty(t.key));
}

// dockLiveSet: liveSids as a Set. A Set or an array of session ids; anything else
// is no live session at all, which unpins rather than guesses.
function dockLiveSet(liveSids) {
  if (liveSids instanceof Set) return liveSids;
  return new Set(Array.isArray(liveSids) ? liveSids : []);
}

// dockFollow decides which session the dock shows when focus moves or a session
// ends. `cur` is what it shows now: {sid, pinned}.
//   - A pinned session that is still live keeps the dock, wherever focus goes.
//   - A pinned session that is gone is unpinned and the dock follows focus: a pin
//     on a dead session would hold the dock on something that cannot be reached.
//   - An unpinned dock follows focus.
//   - Focus on no session (a pane of files, no pane at all) changes nothing: the
//     dock keeps the sid it has rather than going blank on a click.
function dockFollow(cur, focusedSid, liveSids) {
  const sid = cur && cur.sid !== undefined ? cur.sid : null;
  const pinned = !!(cur && cur.pinned);
  if (pinned && dockLiveSet(liveSids).has(sid)) return { sid, pinned: true };
  return { sid: focusedSid == null ? sid : focusedSid, pinned: false };
}

// dockCleanState turns stored or foreign text into one valid session state, or
// null when no tab survives. Bad tabs are dropped, a tab's key is rebuilt from its
// path and box, a repeated file keeps its first tab, and at most DOCK_MAX_TABS are
// kept with the active one among them (when it has to be pulled in from past the
// cap it takes the last slot, the order of the rest undisturbed). An active key
// that names no surviving tab falls back to the first tab.
function dockCleanState(raw) {
  const rawTabs = raw && typeof raw === "object" && Array.isArray(raw.tabs) ? raw.tabs : [];
  const seen = new Set();
  let tabs = [];
  for (const r of rawTabs) {
    if (!r || typeof r !== "object") continue;
    const tab = dockMakeTab(r.path, r.box, r.scroll, r.preview);
    if (!tab || seen.has(tab.key)) continue;
    seen.add(tab.key);
    tabs.push(tab);
  }
  if (!tabs.length) return null;
  let active = raw && typeof raw.active === "string" && seen.has(raw.active) ? raw.active : tabs[0].key;
  if (tabs.length > DOCK_MAX_TABS) {
    const head = tabs.slice(0, DOCK_MAX_TABS);
    tabs = head.some((t) => t.key === active)
      ? head
      : head.slice(0, DOCK_MAX_TABS - 1).concat(tabs.filter((t) => t.key === active));
  }
  return { tabs, active };
}

// serializeDock is what is written to DOCK_TABS_KEY: whether the dock is open and
// each session's tabs. A session with no tab has nothing to restore and is left
// out. Built with Object.fromEntries so a session id of "__proto__" is a key like
// any other and not a write to the object's prototype.
function serializeDock(map, open) {
  const entries = [];
  for (const [sid, st] of map instanceof Map ? map : []) {
    if (typeof sid !== "string" || !sid) continue;
    const clean = dockCleanState(st);
    if (clean) entries.push([sid, clean]);
  }
  return JSON.stringify({ v: DOCK_TABS_V, open: open === true, sessions: Object.fromEntries(entries) });
}

// parseDock reads what serializeDock wrote, defending against everything else:
// absent, corrupt, another version, tabs that are not tabs. Sessions that are not
// in liveSids are dropped (their tabs have nowhere to show), as is a session left
// with no valid tab. Returns {open, map} with a Map of sid -> state, never null.
function parseDock(raw, liveSids) {
  const out = { open: false, map: new Map() };
  let v = null;
  try { v = typeof raw === "string" ? JSON.parse(raw) : null; } catch (_) { return out; }
  if (!v || typeof v !== "object" || v.v !== DOCK_TABS_V) return out;
  out.open = v.open === true;
  const ss = v.sessions;
  if (!ss || typeof ss !== "object" || Array.isArray(ss)) return out;
  const live = dockLiveSet(liveSids);
  for (const sid of Object.keys(ss)) {
    if (!live.has(sid)) continue;
    const st = dockCleanState(ss[sid]);
    if (st) out.map.set(sid, st);
  }
  return out;
}

// dockIsViewer: is this stored pane tab a file? The same test as layout.js's
// tabKey, which tries `path` first: a non-empty string path is a viewer even if
// the entry also has an id, and {id, path: 42} is the terminal.
function dockIsViewer(t) {
  return !!t && typeof t === "object" && typeof t.path === "string" && t.path !== "";
}

// dockFromLayout is the one-time migration of today's saved layouts: files used to
// open as tabs in a pane, and now live in the dock of the session that pane shows.
// For each cell, its viewer tabs ({path, scroll} or {path, box, scroll}) become
// pinned tabs of cellSid(i) (the session id of cell i, or null), and are removed
// from the cell. Returns {map, layout}: the new states by session, and a copy of
// the layout without the viewers, ready for reconcileLayout. The caller merges
// `map` into the dock's own.
//
// A cell with no session (a pane of files) has nowhere to put its viewers, so
// they are dropped. A cell left with no tab at all is dropped too, and `focused`
// and `maximized` follow their cells to the new indexes the way reconcileLayout
// does when it drops one (focus falls to 0, maximized to null). Cells that held no
// viewer, and entries that are not cells at all, pass through as they are, for
// reconcileLayout to judge. The dock opens on the tab the pane had active when it
// was a viewer, else on the first one moved.
function dockFromLayout(layout, cellSid) {
  const map = new Map();
  if (!layout || typeof layout !== "object" || !Array.isArray(layout.cells)) return { map, layout };
  const cells = [];
  const newIndex = new Map(); // index in the old layout -> index in the new one
  let dropped = false;
  layout.cells.forEach((c, i) => {
    if (!c || !Array.isArray(c.tabs) || !c.tabs.some(dockIsViewer)) {
      newIndex.set(i, cells.length);
      cells.push(c);
      return;
    }
    const sid = typeof cellSid === "function" ? cellSid(i) : null;
    const activeTab = c.tabs[c.active];
    if (typeof sid === "string" && sid) {
      const st = map.get(sid) || { tabs: [], active: null };
      for (const t of c.tabs) {
        const tab = dockIsViewer(t) ? dockMakeTab(t.path, t.box, t.scroll, false) : null;
        if (!tab || st.tabs.some((x) => x.key === tab.key)) continue;
        st.tabs.push(tab);
        if (t === activeTab && !st.active) st.active = tab.key;
      }
      if (!st.active && st.tabs.length) st.active = st.tabs[0].key;
      map.set(sid, st);
    }
    const rest = c.tabs.filter((t) => !dockIsViewer(t));
    if (!rest.length) { dropped = true; return; }
    newIndex.set(i, cells.length);
    cells.push({ ...c, tabs: rest.map((t) => (t && typeof t === "object" ? { ...t } : t)), active: Math.max(0, rest.indexOf(activeTab)) });
  });
  const out = { ...layout, cells };
  if (dropped) {
    out.focused = newIndex.has(layout.focused) ? newIndex.get(layout.focused) : 0;
    out.maximized = newIndex.has(layout.maximized) ? newIndex.get(layout.maximized) : null;
  }
  return { map, layout: out };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    DOCK_TABS_KEY, DOCK_MAX_TABS, dockTabKey, dockOpen, dockClose, dockPin, dockActivate, dockSetScroll,
    dockLeaveNeedsAsk, dockFollow, serializeDock, parseDock, dockFromLayout,
  };
}
