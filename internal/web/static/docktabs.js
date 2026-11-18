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
// that names no surviving tab falls back to the first tab. Only the first preview
// tab stays a preview (a hand-edited or old entry could hold several).
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
  // At most one preview tab, the invariant dockOpen keeps: the first stays italic, any other is pinned.
  let seenPreview = false;
  tabs = tabs.map((t) => {
    if (!t.preview) return t;
    if (!seenPreview) { seenPreview = true; return t; }
    return { ...t, preview: false };
  });
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

// dockCellSid: the session a saved pane shows once the layout is restored, for dockFromLayout's cellSid: its active tab when
// that is a live terminal, else its first live terminal (reconcileLayout drops a dead tab and falls back to the first one
// left, and dockFromLayout resets a pane whose active tab was a viewer to its first). `liveSids` (an array or a Set, as for
// parseDock) must be the RUNNING sessions, the set reconcileLayout restores a terminal from (layout.js runningIds: listed and
// not exited), not the whole session list: a pane is restored with the terminals that run, so only those can be the session
// it shows, and an exited session is skipped exactly as reconcileLayout skips it. There is no fallback to an exited session.
// null when the pane has no running terminal, which is every pane of files and every pane whose sessions are gone or have
// exited: reconcileLayout does not restore such a pane, so its viewer tabs are dropped with it (spec: saved tabs whose
// session no longer exists are dropped), and dockFromLayout then has nowhere to put them.
function dockCellSid(cell, liveSids) {
  if (!cell || typeof cell !== "object" || !Array.isArray(cell.tabs)) return null;
  const live = dockLiveSet(liveSids);
  const alive = (t) => !!t && typeof t === "object" && !dockIsViewer(t) && typeof t.id === "string" && t.id !== "" && live.has(t.id);
  const act = cell.tabs[cell.active];
  if (alive(act)) return act.id;
  const first = cell.tabs.find(alive);
  return first ? first.id : null;
}

// dockMerge adds the tabs a layout migration produced (`add`) to the state the session already has (`st`): its own tabs
// first, then the migrated ones it does not have, so nothing is shown twice. It keeps showing what it showed; a session with
// no tabs takes the migrated state whole, active tab included. The result is cleaned like anything stored (the cap, one
// preview), because a migrated layout can be as long as the user's panes were.
function dockMerge(st, add) {
  const own = dockCopyTabs(dockTabsOf(st));
  const seen = new Set(own.map((t) => t.key));
  const tabs = own.slice();
  for (const t of dockTabsOf(add)) {
    if (seen.has(t.key)) continue;
    seen.add(t.key);
    tabs.push({ ...t });
  }
  const keep = dockActiveOf(st);
  const active = own.length && keep !== null && own.some((t) => t.key === keep) ? keep : dockActiveOf(add);
  return dockCleanState({ tabs, active }) || { tabs: [], active: null };
}

// --- what the strip and its glue ask of the model (Task 8a) ----------------------------------------------------

// dockTabLabel: what a tab is called and what its tooltip says: the file's name and
// its whole path, with the "box · " prefix ViewerTab puts on a file that exists only
// in a session's box. The name is the last segment taken verbatim (or the path itself
// when there is none to cut, as ViewerTab does); it goes to the page as text.
function dockTabLabel(path, box) {
  const p = typeof path === "string" ? path : "";
  const pre = typeof box === "string" && box ? "box · " : "";
  return { name: pre + (p.split("/").pop() || p), title: pre + p };
}

// dockTabItems: the strip's view model, one item per tab. `hooks.dirty(key)` says
// whether the tab's buffer has unsaved edits (any truthy answer is a dirty tab) and
// `hooks.locked(key)` whether it is read-only (a lock in the tab) and
// `hooks.code(path)` is its git code. Only M, A, U and C are letters (the tree's own
// marks, gitstatus.js), and a box file has none: git has nothing to say about a path
// inside a box.
function dockTabItems(st, hooks) {
  const h = hooks || {};
  const active = dockActiveOf(st);
  return dockTabsOf(st).map((t) => {
    const label = dockTabLabel(t.path, t.box);
    const code = !t.box && typeof h.code === "function" ? h.code(t.path) : "";
    return {
      key: t.key, path: t.path, box: t.box, name: label.name, title: label.title,
      letter: typeof code === "string" && code.length === 1 && "MAUC".includes(code) ? code : "",
      preview: !!t.preview,
      dirty: typeof h.dirty === "function" && !!h.dirty(t.key),
      locked: typeof h.locked === "function" && !!h.locked(t.key),
      active: t.key === active,
    };
  });
}

// dockRemoved: the keys `before` has that `after` has not, in `before`'s order: the
// tabs a change took out, whose viewers must go with them.
function dockRemoved(before, after) {
  const kept = new Set(dockTabsOf(after).map((t) => t.key));
  return dockTabsOf(before).filter((t) => !kept.has(t.key)).map((t) => t.key);
}

// dockSyncScrolls reads each tab's offset from its viewer: `scrollOf(key)` is the
// number to record, or undefined for a tab with no viewer (it keeps the offset it
// has). Returns {st, changed}; when nothing moved, `st` is the state passed in.
function dockSyncScrolls(st, scrollOf) {
  if (typeof scrollOf !== "function") return { st, changed: false };
  let changed = false;
  const tabs = dockTabsOf(st).map((t) => {
    const v = scrollOf(t.key);
    if (v === undefined) return t;
    const n = dockScroll(v);
    if (n === t.scroll) return t;
    changed = true;
    return { ...t, scroll: n };
  });
  return changed ? { st: { tabs, active: dockActiveOf(st) }, changed } : { st, changed };
}

// dockStepKey: the tab an arrow key goes to from `key`: one left or right (`step`
// -1 or 1), "home" or "end". It stops at the ends rather than wrapping. A key that
// names no tab starts from the first one; no tabs, no key.
function dockStepKey(st, key, step) {
  const tabs = dockTabsOf(st);
  if (!tabs.length) return null;
  const at = tabs.findIndex((t) => t.key === key);
  let to = at < 0 ? 0 : at;
  if (step === "home") to = 0;
  else if (step === "end") to = tabs.length - 1;
  else if (at >= 0 && (step === 1 || step === -1)) to = Math.min(tabs.length - 1, Math.max(0, at + step));
  return tabs[to].key;
}

// dockRepointTab: a tab's file turned out to live at another path (#53: a relative
// terminal link found in another checkout). The tab keeps its slot, scroll and
// preview, and its active mark moves with it; a box tab stays in its box. When the
// new path already has a tab, or there is no usable path, the state is returned
// as it was and the caller decides (it closes this tab and shows the other).
function dockRepointTab(st, oldKey, path) {
  const tabs = dockTabsOf(st);
  const active = dockActiveOf(st);
  const at = tabs.findIndex((t) => t.key === oldKey);
  const same = { tabs: dockCopyTabs(tabs), active };
  if (at < 0) return same;
  const old = tabs[at];
  const tab = dockMakeTab(path, old.box, old.scroll, old.preview);
  if (!tab || tabs.some((t, i) => i !== at && t.key === tab.key)) return same;
  const next = dockCopyTabs(tabs);
  next[at] = tab;
  return { tabs: next, active: active === oldKey ? tab.key : active };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    DOCK_TABS_KEY, DOCK_MAX_TABS, dockTabKey, dockOpen, dockClose, dockPin, dockActivate, dockSetScroll,
    dockLeaveNeedsAsk, dockFollow, serializeDock, parseDock, dockFromLayout, dockCellSid, dockMerge,
    dockTabLabel, dockTabItems, dockRemoved, dockSyncScrolls, dockStepKey, dockRepointTab,
  };
}
