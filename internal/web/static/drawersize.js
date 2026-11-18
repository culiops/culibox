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

// stepWidth: the sidebar width splitter's keyboard move, in pixels. dir is +1
// (wider) or -1 (narrower); the result stays inside min..max, and a width that is
// not a number steps from min.
function stepWidth(cur, dir, min, max, step) {
  const from = Number.isFinite(cur) ? cur : min;
  return Math.min(max, Math.max(min, from + dir * step));
}

// widthKeyDir: the direction a key moves the sidebar's right edge (ArrowRight
// widens it), 0 for a key the splitter does not take.
function widthKeyDir(key) {
  return key === "ArrowRight" ? 1 : key === "ArrowLeft" ? -1 : 0;
}

// splitAria: the separator's aria-valuenow/min/max, in whole percents. now
// stays inside min..max: an undragged drawer (960px) on a grid wider than
// 3200px is drawn under the 30% floor, and the first key press snaps it there.
function splitAria(r, kind) {
  const [lo, hi] = splitBounds(kind);
  return { now: Math.round(Math.min(hi, Math.max(lo, r)) * 100), min: Math.round(lo * 100), max: Math.round(hi * 100) };
}

// ESC_OWNERS: the elements that own an Escape pressed with focus in or on them, as a closest() list: a field, an editable element, an
// editor, a menu. Monaco is listed as a whole because what has focus inside it is not always a textarea: with the EditContext API
// (Chrome and Edge, Monaco's default there) it is a div (.native-edit-context), and an Escape Monaco has no use for (no selection to
// collapse, no widget to close) bubbles to the page, where it would leave Full mode from inside the editor.
const ESC_OWNERS = "input, textarea, [contenteditable], .CodeMirror, .monaco-editor, [role=menu]";

// escOwnedBy: does the element that has focus (an Escape's target) own the press (ESC_OWNERS)? Anything that is not an element
// (the document) owns nothing.
function escOwnedBy(t) {
  return !!(t && typeof t.closest === "function" && t.closest(ESC_OWNERS));
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
    drawerWidthFromPointer, treeRatioFromPointer, stepSplit, stepWidth, widthKeyDir, splitAria, maxEscapeRestores, ESC_OWNERS, escOwnedBy,
  };
}
