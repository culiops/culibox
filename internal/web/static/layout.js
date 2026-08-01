"use strict";

// Layout persistence: the grid (panes, their terminal tabs, the active tab per
// pane, focus, cap, maximized) is snapshotted to localStorage on every layout
// change and replayed on the next boot, so a refresh brings the console back
// as it was instead of an empty cell.
//
// Everything that decides *what* is stored or restored lives here as a pure
// function, so it is testable under `node --test`; tabs.js only wires it to the
// live model. Viewer tabs are stored too, with the offset they were read to, so
// a reload comes back to the same place in the same file. A path that no longer
// exists is not filtered here — it cannot be, this file does no I/O — the
// restored tab discovers it on its first read and closes itself.

const LAYOUT_KEY = "agent-box:layout";
const LAYOUT_V = 1;
const LAYOUT_MAX_CAP = 4;

// clampCap keeps a stored cap to a value the layout buttons can actually produce
// (1, 2 or 4). A cap of 3 would restore a window limit with no button lit, so
// anything else falls back to the boot default rather than to a phantom state.
const LAYOUT_CAPS = [1, 2, LAYOUT_MAX_CAP];
function clampCap(v) {
  const n = Math.round(Number(v));
  return LAYOUT_CAPS.includes(n) ? n : LAYOUT_MAX_CAP;
}

// A stored tab is either a terminal (its session id) or a viewer (its path plus
// how far down the file was read).
//
// tabKey identifies one across the snapshot: `t:<id>` or `v:<path>`. Paths
// compare as plain strings, which is safe only because ViewerTab normalizes its
// path in its constructor long before serializeLayout ever sees one — do NOT
// pull normalizePath in here, it would cost this file the purity that makes it
// testable under `node --test`.
function tabKey(t) {
  if (!t || typeof t !== "object") return null;
  if (typeof t.path === "string" && t.path) return "v:" + t.path;
  if (typeof t.id === "string" && t.id) return "t:" + t.id;
  return null;
}

// clampScroll keeps a stored offset to a finite, non-negative whole number of
// pixels: a snapshot is user-editable text, and NaN or -1 goes straight into
// scrollTop on the next boot. Also used by viewer-tab.js, which shares this
// scope — see the script order in index.html.
function clampScroll(v) {
  const n = Math.round(Number(v));
  return Number.isFinite(n) && n > 0 ? n : 0;
}

// storedTab → the entry written for a live tab, or null if it cannot be restored.
function storedTab(t) {
  if (!t) return null;
  if (t.kind === "viewer" && t.path) return { path: t.path, scroll: clampScroll(t.scroll) };
  if (t.kind === "terminal" && t.id) return { id: t.id };
  return null;
}

// serializeLayout builds the snapshot from the live model. `model.cells` are the
// grid cells, `model.focused` / `model.maximized` are cell references (or null).
// Returns null when nothing restorable is left — no session AND no open file:
// the caller stores that as "no snapshot", so an emptied console reloads empty
// instead of into a stale grid.
function serializeLayout(model) {
  const cells = [];
  const newIndex = new Map(); // live cell -> index in the snapshot
  for (const c of model.cells || []) {
    // Keep the live tabs, not their stored form: the active index is found by
    // identity against the objects the grid holds.
    const kept = (c.tabs || []).filter((t) => storedTab(t) !== null);
    if (!kept.length) continue;
    const active = Math.max(0, kept.indexOf(c.activeTab));
    newIndex.set(c, cells.length);
    cells.push({ tabs: kept.map(storedTab), active });
  }
  if (!cells.length) return null;
  return {
    v: LAYOUT_V,
    cap: clampCap(model.cap),
    focused: newIndex.has(model.focused) ? newIndex.get(model.focused) : 0,
    maximized: newIndex.has(model.maximized) ? newIndex.get(model.maximized) : null,
    cells,
  };
}

// runningIds → the ids a restore may reference: listed AND not exited. A session
// that has exited is still listed (with its final output) but is not brought back;
// "detached" is a running abduco session with nothing attached, which is exactly
// what a reload leaves behind, so it must count as running.
function runningIds(sessions) {
  const out = new Set();
  for (const s of sessions || []) {
    if (!s || typeof s.id !== "string") continue;
    if (s.exited === true || s.status === "exited") continue;
    out.add(s.id);
  }
  return out;
}

// reconcileLayout filters a stored snapshot against the live session list and
// returns a plan the grid can be rebuilt from, or null for "boot normally".
// Null covers every unusable input: absent, corrupt, wrong version, or nothing
// left after filtering, so a bad entry can never break boot.
function reconcileLayout(saved, sessions) {
  if (!saved || typeof saved !== "object" || saved.v !== LAYOUT_V) return null;
  if (!Array.isArray(saved.cells)) return null;
  const live = runningIds(sessions);
  const seen = new Set(); // one session — or one file — cannot be open in two panes
  const cells = [];
  const kept = [];        // original index of each surviving cell
  saved.cells.forEach((c, i) => {
    if (!c || !Array.isArray(c.tabs)) return;
    const activeKey = tabKey(c.tabs[c.active]);
    const tabs = [];
    for (const t of c.tabs) {
      const key = tabKey(t);
      if (!key || seen.has(key)) continue;
      // A terminal is restorable only while its session runs. A viewer has
      // nothing to check here — a path is not in the session list — so a file
      // deleted since the last visit is caught by the tab's own first read.
      if (key[0] === "t" && !live.has(t.id)) continue;
      seen.add(key);
      tabs.push(t.path ? { path: t.path, scroll: clampScroll(t.scroll) } : { id: t.id });
    }
    if (!tabs.length) return;
    // Follow the active tab by identity: after dropping dead tabs its old index
    // points at a different tab. A dead active tab falls back to the first.
    const active = Math.max(0, tabs.findIndex((x) => tabKey(x) === activeKey));
    kept.push(i);
    cells.push({ tabs, active });
  });
  if (!cells.length) return null;
  const focused = kept.indexOf(saved.focused);
  const maximized = kept.indexOf(saved.maximized);
  return {
    cap: clampCap(saved.cap),
    focused: focused < 0 ? 0 : focused,
    maximized: maximized < 0 ? null : maximized,
    cells,
  };
}

// readLayout / writeLayout are the only storage touchpoints. Both swallow their
// errors: a browser with storage disabled must lose the feature, not the app.
function readLayout() {
  try { return JSON.parse(localStorage.getItem(LAYOUT_KEY) || "null"); }
  catch (_) { return null; }
}
function writeLayout(snapshot) {
  try {
    if (!snapshot) localStorage.removeItem(LAYOUT_KEY);
    else localStorage.setItem(LAYOUT_KEY, JSON.stringify(snapshot));
  } catch (_) {}
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { serializeLayout, reconcileLayout, readLayout, writeLayout, clampCap, clampScroll, tabKey, LAYOUT_KEY };
}
