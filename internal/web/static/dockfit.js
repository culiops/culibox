"use strict";

// Files dock width policy: how wide the dock (explorer + editor) may be beside
// the terminals, what gives when the window is narrow, and when the dock must
// give up and open in Full mode. Pure arithmetic, no DOM. Classic script: the
// names below are browser globals used by app.js (so this loads before it);
// under node they are required.
//
// The order things give in, as the spec writes it:
//   1. the explorer folds to its icon strip,
//   2. the dock shrinks toward editor floor + strip, the terminals going from
//      their comfortable width down to their hard floor,
//   3. if even that does not fit, Full mode.
// dockLayout depends only on its arguments, so a window dragged narrow and
// wide again lands on the same layout it started from.

const DOCK_FLOORS = {
  termComfy: 360, // a terminal's comfortable width
  termFloor: 280, // its hard floor: Claude Code still works there, it wraps
  editor: 420, // the editor column's floor
  tree: 200, // an open explorer column's minimum
  strip: 36, // the folded explorer (an icon strip)
  split: 5, // the splitter between explorer and editor
};

const DOCK_SHARE_DEFAULT = 0.62; // the dock's share of the space the panes and the dock divide
const DOCK_TREE_DEFAULT = 0.35; // the explorer's share of the dock

// dockNum: a share that is a finite number, else its default (null, junk).
function dockNum(v, dflt) {
  return typeof v === "number" && Number.isFinite(v) ? v : dflt;
}

// dockLayout: whole-pixel widths for the dock and its columns.
//   avail      the #layout width minus the sidebar (or rail) width
//   cols       pane columns: 1, or 2 for cols-2|3|4 (junk counts as 1)
//   dockShare  stored share of avail for the dock, or null for the default
//   treeShare  stored share of the dock for the explorer, or null for the default
// -> {dockW, treeW, editorW, treeFolded, full}. When full is true nothing fits
// and the widths are 0: Full mode sizes the dock from the pane area, not from here.
function dockLayout(o) {
  const a = o || {};
  const F = DOCK_FLOORS;
  const avail = typeof a.avail === "number" && Number.isFinite(a.avail) ? a.avail : 0;
  const cols = typeof a.cols === "number" && a.cols >= 1 ? Math.floor(a.cols) : 1;
  const none = { dockW: 0, treeW: 0, editorW: 0, treeFolded: false, full: true };
  const smallest = F.editor + F.strip; // editor floor + the folded explorer

  // The wanted dock, raised to the smallest usable one: a stored share that
  // would leave the editor under its floor is not honoured when space allows.
  const want = Math.max(smallest, Math.round(avail * dockNum(a.dockShare, DOCK_SHARE_DEFAULT)));
  // The terminals keep their comfortable width while the dock can still be at
  // least `smallest`; only then do they go down toward their hard floor.
  let dockW = Math.min(want, avail - cols * F.termComfy);
  if (dockW < smallest) dockW = Math.min(want, avail - cols * F.termFloor);
  if (!(dockW >= smallest)) return none;

  const open = Math.round(dockW * dockNum(a.treeShare, DOCK_TREE_DEFAULT));
  const openEditor = dockW - open - F.split;
  if (open >= F.tree && openEditor >= F.editor) {
    return { dockW, treeW: open, editorW: openEditor, treeFolded: false, full: false };
  }
  return { dockW, treeW: F.strip, editorW: dockW - F.strip, treeFolded: true, full: false };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { DOCK_FLOORS, dockLayout };
}
