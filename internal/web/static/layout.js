"use strict";

// Layout persistence: the grid (panes, their terminal tabs, the active tab per
// pane, focus, cap, maximized) is snapshotted to localStorage on every layout
// change and replayed on the next boot, so a refresh brings the console back
// as it was instead of an empty cell.
//
// Everything that decides *what* is stored or restored lives here as a pure
// function, so it is testable under `node --test`; tabs.js only wires it to the
// live model. Viewer tabs are deliberately absent: a stored path may no longer
// exist, and reopening a file costs one click in the tree.

const LAYOUT_KEY = "agent-console:layout";
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

// serializeLayout builds the snapshot from the live model. `model.cells` are the
// grid cells, `model.focused` / `model.maximized` are cell references (or null).
// Returns null when there is no terminal tab left to restore: the caller stores
// that as "no snapshot", so closing the last session then reloading gives the
// normal empty console instead of a stale grid.
function serializeLayout(model) {
  const cells = [];
  const newIndex = new Map(); // live cell -> index in the snapshot
  for (const c of model.cells || []) {
    const terms = (c.tabs || []).filter((t) => t && t.kind === "terminal" && t.id);
    if (!terms.length) continue;
    const active = Math.max(0, terms.indexOf(c.activeTab));
    newIndex.set(c, cells.length);
    cells.push({ tabs: terms.map((t) => ({ id: t.id })), active });
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
  const seen = new Set(); // one session cannot be open in two panes
  const cells = [];
  const kept = [];        // original index of each surviving cell
  saved.cells.forEach((c, i) => {
    if (!c || !Array.isArray(c.tabs)) return;
    const activeId = (c.tabs[c.active] || {}).id;
    const tabs = [];
    for (const t of c.tabs) {
      const id = t && typeof t.id === "string" ? t.id : null;
      if (!id || seen.has(id) || !live.has(id)) continue;
      seen.add(id);
      tabs.push({ id });
    }
    if (!tabs.length) return;
    // Follow the active tab by identity: after dropping dead tabs its old index
    // points at a different session. A dead active tab falls back to the first.
    const active = Math.max(0, tabs.findIndex((t) => t.id === activeId));
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
  module.exports = { serializeLayout, reconcileLayout, readLayout, writeLayout, clampCap, LAYOUT_KEY };
}
