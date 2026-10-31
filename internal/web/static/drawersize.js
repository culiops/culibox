"use strict";

// Files drawer sizes (issue #34): the drawer's width as a share of the grid
// (dragged at its left edge) and the tree column's share of the drawer body
// (dragged at the tree/viewer splitter). Pure decisions plus guarded storage,
// no DOM. Classic script: the names below are browser globals used by
// app.js (so this loads before it); under node they are required.
//
// Both are ratios, not pixels, so a stored size still fits after the window
// or the grid changes width. The pixel floors (a usable tree and viewer) are
// CSS clamp()s in desktop.css, which also wins when the drawer is narrow.

const DRAWER_SIZE_KEY = "agent-box:drawerSize";
const DRAWER_W_MIN = 0.3;
const DRAWER_W_MAX = 0.92;
const TREE_DEFAULT = 0.4;
const TREE_MIN = 0.15;
const TREE_MAX = 0.75;
const SPLIT_STEP = 0.02;

function splitBounds(kind) {
  return kind === "width" ? [DRAWER_W_MIN, DRAWER_W_MAX] : [TREE_MIN, TREE_MAX];
}

function clampShare(r, kind) {
  const [lo, hi] = splitBounds(kind);
  return Math.min(hi, Math.max(lo, Math.round(r * 1000) / 1000));
}

function isShare(r) {
  return typeof r === "number" && Number.isFinite(r);
}

// normalizeDrawerSize: anything a stored value may hold → {width, tree}.
// width null means "never resized": the drawer keeps its CSS default.
function normalizeDrawerSize(raw) {
  const o = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
  return {
    width: isShare(o.width) ? clampShare(o.width, "width") : null,
    tree: isShare(o.tree) ? clampShare(o.tree, "tree") : TREE_DEFAULT,
  };
}

// Storage can be missing, full, or throw on access (private windows, blocked
// site data): either way the drawer just draws its defaults.
function loadDrawerSize(storage) {
  try {
    const s = storage && storage.getItem(DRAWER_SIZE_KEY);
    return normalizeDrawerSize(s ? JSON.parse(s) : null);
  } catch (_) {
    return normalizeDrawerSize(null);
  }
}

function saveDrawerSize(storage, size) {
  try {
    if (storage) storage.setItem(DRAWER_SIZE_KEY, JSON.stringify(normalizeDrawerSize(size)));
  } catch (_) { /* a size that is not kept is only a convenience lost */ }
}

// drawerWidthFromPointer: the drawer hangs off the grid's right edge, so its
// share is the distance from the pointer to that edge. null when the grid has
// no size (the caller then keeps what it had).
function drawerWidthFromPointer(grid, x) {
  if (!grid || !(grid.width > 0)) return null;
  return clampShare((grid.left + grid.width - x) / grid.width, "width");
}

// treeRatioFromPointer: the tree is the body's left column.
function treeRatioFromPointer(body, x) {
  if (!body || !(body.width > 0)) return null;
  return clampShare((x - body.left) / body.width, "tree");
}

// stepSplit: a splitter's keyboard move, or null for a key it does not take.
// The tree splitter grows the tree to the right; the drawer edge is on the
// drawer's left, so there ArrowLeft grows it. An unset width steps from
// `drawn`, the share it is drawn at (the minimum when that is unknown too).
function stepSplit(r, key, kind, drawn) {
  const [lo, hi] = splitBounds(kind);
  let cur = isShare(r) ? r : isShare(drawn) ? drawn : lo;
  if (key === "Home") return lo;
  if (key === "End") return hi;
  const grow = kind === "width" ? "ArrowLeft" : "ArrowRight";
  const shrink = kind === "width" ? "ArrowRight" : "ArrowLeft";
  if (key === grow) cur += SPLIT_STEP;
  else if (key === shrink) cur -= SPLIT_STEP;
  else return null;
  return clampShare(cur, kind);
}

// splitAria: the separator's aria-valuenow/min/max, in whole percents. now
// stays inside min..max: an undragged drawer (960px) on a grid wider than
// 3200px is drawn under the 30% floor, and the first key press snaps it there.
function splitAria(r, kind) {
  const [lo, hi] = splitBounds(kind);
  return { now: Math.round(Math.min(hi, Math.max(lo, r)) * 100), min: Math.round(lo * 100), max: Math.round(hi * 100) };
}

// maxEscapeRestores: does this Escape restore a maximized drawer? `where` is
// where focus is: "body" (nothing focused, e.g. after a tree row click, which
// re-renders the row), "drawer", or "other" (a terminal, the sidebar, a
// dialog: their Escape is theirs). `owned`: a field, the editor or a menu item
// has focus. An open menu takes the press to close itself, and only that;
// `prevented`: a handler that ran first already used it (a menu or page it
// closed has hidden itself by then, so the state check alone would miss it).
function maxEscapeRestores(s) {
  if (!s || !s.max || s.owned || s.menuOpen || s.prevented) return false;
  return s.where === "body" || s.where === "drawer";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    DRAWER_SIZE_KEY, DRAWER_W_MIN, DRAWER_W_MAX, TREE_DEFAULT, TREE_MIN, TREE_MAX,
    normalizeDrawerSize, loadDrawerSize, saveDrawerSize,
    drawerWidthFromPointer, treeRatioFromPointer, stepSplit, splitAria, maxEscapeRestores,
  };
}
