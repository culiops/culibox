"use strict";

// Shell dock: a pane can carry a strip of companion shells docked below or to
// the right of its parent terminal. A dock is {placement, ids, active, ratio} —
// ids are session ids in tab order, `active` is the one shown, `ratio` the share
// of the pane the dock takes. Pure decisions, no DOM and no I/O. Classic script:
// the names below are browser globals used by layout.js and tabs.js (so this
// loads before both); under node they are required.
//
// Every function returns a new dock and leaves its input alone, so a caller can
// compare before and after and a stored snapshot is never edited by accident.

const DOCK_PLACEMENTS = ["below", "right"];
const DOCK_RATIO_DEFAULT = 0.4;
const DOCK_RATIO_MIN = 0.2;
const DOCK_RATIO_MAX = 0.8;

function newDock() {
  return { placement: "below", ids: [], active: null, ratio: DOCK_RATIO_DEFAULT };
}

// clampDockRatio keeps a stored ratio inside what the splitter can draw. NaN or
// anything that is not a number (a hand-edited snapshot: "x", null) is the default.
function clampDockRatio(r) {
  if (typeof r !== "number" || Number.isNaN(r)) return DOCK_RATIO_DEFAULT;
  return Math.min(DOCK_RATIO_MAX, Math.max(DOCK_RATIO_MIN, r));
}

function dockPlacementOf(p) {
  return DOCK_PLACEMENTS.includes(p) ? p : "below";
}

function dockSetPlacement(dock, p) {
  return Object.assign({}, dock, { placement: dockPlacementOf(p) });
}

// dockAdd appends an id that is not there yet; either way it becomes the active one.
function dockAdd(dock, id) {
  const d = dock || newDock();
  const ids = d.ids.includes(id) ? d.ids.slice() : d.ids.concat([id]);
  return Object.assign({}, d, { ids, active: id });
}

// dockRemove returns null when the dock empties — the caller then drops the dock
// from its cell. Removing the active id hands "active" to the left neighbour, or
// the right one when it was first; removing any other id leaves it alone.
function dockRemove(dock, id) {
  if (!dock) return null;
  const at = dock.ids.indexOf(id);
  if (at < 0) return dock;
  const ids = dock.ids.filter((x) => x !== id);
  if (!ids.length) return null;
  const active = dock.active === id ? ids[Math.max(0, at - 1)] : dock.active;
  return Object.assign({}, dock, { ids, active });
}

// normalizeDock turns anything a snapshot may hold into a well-formed dock, or
// null when there is nothing to keep. It never throws: ids that are not
// non-empty strings and repeats are dropped, placement and ratio fall back to
// their defaults, and `active` falls back to the first id.
function normalizeDock(raw) {
  if (!raw || typeof raw !== "object" || !Array.isArray(raw.ids)) return null;
  const ids = [];
  for (const id of raw.ids) {
    if (typeof id === "string" && id && !ids.includes(id)) ids.push(id);
  }
  if (!ids.length) return null;
  return {
    placement: dockPlacementOf(raw.placement),
    ids,
    active: ids.includes(raw.active) ? raw.active : ids[0],
    ratio: clampDockRatio(raw.ratio),
  };
}

// placeShell decides where a shell session is shown, given the grid
// (model.cells, each with `tabs` and an optional `dock`). A terminal must be
// attached in exactly one place, so a shell already open — docked or as a tab —
// is only activated. Otherwise it docks into the pane that holds its parent; with
// no such pane the caller opens the parent first, or gives the shell its own
// pane (cellIndex -1: there is no cell to point at).
function placeShell(model, shellId, parentId) {
  const cells = (model && model.cells) || [];
  const holds = (c, id) => (c.tabs || []).some((t) => t && t.kind !== "viewer" && t.id === id);
  for (let i = 0; i < cells.length; i++) {
    const c = cells[i];
    if (!c) continue;
    if ((c.dock && Array.isArray(c.dock.ids) && c.dock.ids.includes(shellId)) || holds(c, shellId)) {
      return { action: "activate", cellIndex: i };
    }
  }
  for (let i = 0; i < cells.length; i++) {
    if (cells[i] && parentId && holds(cells[i], parentId)) return { action: "dock", cellIndex: i };
  }
  return { action: "own", cellIndex: -1 };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { DOCK_PLACEMENTS, newDock, dockAdd, dockRemove, dockSetPlacement, clampDockRatio, normalizeDock, placeShell };
}
