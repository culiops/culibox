"use strict";

// Files dock width policy: how wide the dock (explorer + editor) may be beside
// the terminals, what gives when the window is narrow, and when the dock must
// give up and open in Full mode. Pure arithmetic, no DOM. Classic script: the
// names below are browser globals used by app.js (so this loads before it);
// under node they are required.
//
// The order things give in, as the spec writes it:
//   1. the explorer folds to its icon strip,
//   2. the editor shrinks to its floor (the dock to editor floor + strip),
//   3. only then the terminals go from their comfortable width down to their
//      hard floor,
//   4. if even that does not fit, Full mode.
// The dock is therefore as wide as the shares ask for, held back so the
// terminals keep their comfort, and never below editor floor + strip: narrowing
// the window never makes it grow. dockLayout depends only on its arguments, so
// a window dragged narrow and wide again lands on the layout it started from.
//
// treeOpen is the one transient input: the user clicked the folded explorer
// strip to open the column. A folded layout then asks for room for the column
// (tree minimum + splitter + editor floor) and takes it from the terminals
// first, down to their hard floor, because the spec says it pushes and never
// covers. It changes nothing when the explorer is not folded.

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
const DOCK_EDITOR_OPENED = 260; // the editor's least width beside an explorer opened from the strip

// dockNum: a share that is a finite number, else its default (null, junk).
function dockNum(v, dflt) {
  return typeof v === "number" && Number.isFinite(v) ? v : dflt;
}

// dockLayout: whole-pixel widths for the dock and its columns.
//   avail      the #layout width minus the sidebar (or rail) width; a fraction is floored
//   cols       pane columns: 1, or 2 for cols-2|3|4 (junk counts as 1)
//   dockShare  stored share of avail for the dock, or null for the default
//   treeShare  stored share of the dock for the explorer, or null for the default
//   treeOpen   true when the user opened the explorer from its strip (see the header)
// -> {dockW, treeW, editorW, treeFolded, full}. When full is true nothing fits
// and the widths are 0: Full mode sizes the dock from the pane area, not from here.
function dockLayout(o) {
  const a = o || {};
  const F = DOCK_FLOORS;
  const avail = typeof a.avail === "number" && Number.isFinite(a.avail) ? Math.floor(a.avail) : 0;
  const cols = typeof a.cols === "number" && a.cols >= 1 ? Math.floor(a.cols) : 1;
  const none = { dockW: 0, treeW: 0, editorW: 0, treeFolded: false, full: true };
  const smallest = F.editor + F.strip; // editor floor + the folded explorer
  const unfoldAt = F.tree + F.split + F.editor; // the dock that holds an open explorer and the editor floor

  // The wanted dock, raised to the smallest usable one: a stored share that
  // would leave the editor under its floor is not honoured when space allows.
  const want = Math.max(smallest, Math.round(avail * dockNum(a.dockShare, DOCK_SHARE_DEFAULT)));
  // Held back so the terminals keep their comfortable width, but never under
  // `smallest`: the terminals give in only after the dock has stopped shrinking.
  const dockW = Math.max(smallest, Math.min(want, avail - cols * F.termComfy));
  if (avail - dockW < cols * F.termFloor) return none;

  if (dockW >= unfoldAt) {
    // Room for an open explorer: it keeps its share, but between its minimum
    // and what leaves the editor its floor, so a stored share never folds it.
    const share = Math.round(dockW * dockNum(a.treeShare, DOCK_TREE_DEFAULT));
    const treeW = Math.min(Math.max(share, F.tree), dockW - F.split - F.editor);
    return { dockW, treeW, editorW: dockW - treeW - F.split, treeFolded: false, full: false };
  }
  if (a.treeOpen === true) {
    // The column pushes: it takes from the terminals, down to their floor.
    const wide = Math.min(avail - cols * F.termFloor, Math.max(dockW, unfoldAt));
    const editorW = wide - F.tree - F.split;
    if (editorW < DOCK_EDITOR_OPENED) return none;
    return { dockW: wide, treeW: F.tree, editorW, treeFolded: false, full: false };
  }
  return { dockW, treeW: F.strip, editorW: dockW - F.strip, treeFolded: true, full: false };
}

// dockFullLayout: the same split for Full mode, where the dock is the whole pane area and shares it with
// nothing, so there is no dock width to decide, only how that width divides.
//   width      the pane area's width; a fraction is floored
//   treeShare  stored share of it for the explorer, or null for the default
//   treeOpen   true when the user opened the explorer from its strip
// -> {treeW, editorW, treeFolded}. The explorer keeps its share between its minimum and what leaves the editor its
// floor. A width that holds neither folds it to the strip; treeOpen then opens it at its minimum however short the
// editor is left, because in Full mode there is nothing left to give.
function dockFullLayout(o) {
  const a = o || {};
  const F = DOCK_FLOORS;
  const width = typeof a.width === "number" && Number.isFinite(a.width) ? Math.max(0, Math.floor(a.width)) : 0;
  if (width >= F.tree + F.split + F.editor) {
    const share = Math.round(width * dockNum(a.treeShare, DOCK_TREE_DEFAULT));
    const treeW = Math.min(Math.max(share, F.tree), width - F.split - F.editor);
    return { treeW, editorW: width - treeW - F.split, treeFolded: false };
  }
  if (a.treeOpen === true) return { treeW: F.tree, editorW: Math.max(0, width - F.tree - F.split), treeFolded: false };
  return { treeW: F.strip, editorW: Math.max(0, width - F.strip), treeFolded: true };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { DOCK_FLOORS, dockLayout, dockFullLayout };
}
