// internal/web/jstest/drawersize.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const d = require("../static/drawersize.js");

function memStore(init) {
  const m = new Map(Object.entries(init || {}));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), m };
}
const THROWS = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("denied"); } };

test("normalizeDrawerSize: nothing stored is the CSS default width and a 40% tree", () => {
  for (const raw of [null, undefined, "x", 3, [], {}]) {
    assert.deepEqual(d.normalizeDrawerSize(raw), { width: null, tree: 0.4 });
  }
});

test("normalizeDrawerSize: values are clamped, junk falls back per field", () => {
  assert.deepEqual(d.normalizeDrawerSize({ width: 0.5, tree: 0.3 }), { width: 0.5, tree: 0.3 });
  assert.deepEqual(d.normalizeDrawerSize({ width: 0.01, tree: 0.01 }), { width: d.DRAWER_W_MIN, tree: d.TREE_MIN });
  assert.deepEqual(d.normalizeDrawerSize({ width: 5, tree: 5 }), { width: d.DRAWER_W_MAX, tree: d.TREE_MAX });
  assert.deepEqual(d.normalizeDrawerSize({ width: "0.5", tree: NaN }), { width: null, tree: 0.4 });
  assert.deepEqual(d.normalizeDrawerSize({ width: null, tree: 0.6 }), { width: null, tree: 0.6 });
});

test("loadDrawerSize / saveDrawerSize round-trip through storage", () => {
  const s = memStore();
  d.saveDrawerSize(s, { width: 0.7, tree: 0.35 });
  assert.deepEqual(JSON.parse(s.m.get(d.DRAWER_SIZE_KEY)), { width: 0.7, tree: 0.35 });
  assert.deepEqual(d.loadDrawerSize(s), { width: 0.7, tree: 0.35 });
});

test("loadDrawerSize: unparsable or throwing storage is the default, never an error", () => {
  assert.deepEqual(d.loadDrawerSize(memStore({ [d.DRAWER_SIZE_KEY]: "{not json" })), { width: null, tree: 0.4 });
  assert.deepEqual(d.loadDrawerSize(THROWS), { width: null, tree: 0.4 });
  assert.deepEqual(d.loadDrawerSize(null), { width: null, tree: 0.4 });
  assert.doesNotThrow(() => d.saveDrawerSize(THROWS, { width: 0.5, tree: 0.5 }));
  assert.doesNotThrow(() => d.saveDrawerSize(null, { width: 0.5, tree: 0.5 }));
});

test("saveDrawerSize stores the normalised value, not what it was handed", () => {
  const s = memStore();
  d.saveDrawerSize(s, { width: 9, tree: "x" });
  assert.deepEqual(JSON.parse(s.m.get(d.DRAWER_SIZE_KEY)), { width: d.DRAWER_W_MAX, tree: 0.4 });
});

test("drawerWidthFromPointer: share of the grid from the pointer to its right edge", () => {
  const grid = { left: 100, width: 1000 };
  assert.equal(d.drawerWidthFromPointer(grid, 500), 0.6);
  assert.equal(d.drawerWidthFromPointer(grid, 1099), d.DRAWER_W_MIN); // tiny → min
  assert.equal(d.drawerWidthFromPointer(grid, 0), d.DRAWER_W_MAX);    // past the left edge → max
  assert.equal(d.drawerWidthFromPointer(grid, 433.3333), 0.667);      // rounded to 1/1000
  assert.equal(d.drawerWidthFromPointer({ left: 0, width: 0 }, 10), null);
  assert.equal(d.drawerWidthFromPointer(null, 10), null);
});

test("treeRatioFromPointer: share of the drawer body left of the pointer", () => {
  const body = { left: 200, width: 800 };
  assert.equal(d.treeRatioFromPointer(body, 520), 0.4);
  assert.equal(d.treeRatioFromPointer(body, 200), d.TREE_MIN);
  assert.equal(d.treeRatioFromPointer(body, 2000), d.TREE_MAX);
  assert.equal(d.treeRatioFromPointer({ left: 0, width: 0 }, 10), null);
});

test("stepSplit: arrows move 2%, Home/End jump to the ends, other keys are not ours", () => {
  // The tree splitter: ArrowRight widens the tree.
  assert.equal(d.stepSplit(0.4, "ArrowRight", "tree"), 0.42);
  assert.equal(d.stepSplit(0.4, "ArrowLeft", "tree"), 0.38);
  assert.equal(d.stepSplit(0.4, "Home", "tree"), d.TREE_MIN);
  assert.equal(d.stepSplit(0.4, "End", "tree"), d.TREE_MAX);
  assert.equal(d.stepSplit(d.TREE_MAX, "ArrowRight", "tree"), d.TREE_MAX);
  assert.equal(d.stepSplit(0.4, "Enter", "tree"), null);
  // The drawer edge sits on the drawer's LEFT: ArrowLeft widens the drawer.
  assert.equal(d.stepSplit(0.6, "ArrowLeft", "width"), 0.62);
  assert.equal(d.stepSplit(0.6, "ArrowRight", "width"), 0.58);
  assert.equal(d.stepSplit(0.6, "Home", "width"), d.DRAWER_W_MIN);
  assert.equal(d.stepSplit(0.6, "End", "width"), d.DRAWER_W_MAX);
});

test("stepSplit: an unset drawer width steps from the share it is drawn at", () => {
  // width null = the CSS default; the caller passes the drawn share instead.
  assert.equal(d.stepSplit(null, "ArrowLeft", "width", 0.5), 0.52);
  assert.equal(d.stepSplit(null, "ArrowLeft", "width"), d.DRAWER_W_MIN + 0.02);
});

test("splitAria: valuenow/min/max are whole percents", () => {
  assert.deepEqual(d.splitAria(0.4, "tree"), { now: 40, min: Math.round(d.TREE_MIN * 100), max: Math.round(d.TREE_MAX * 100) });
  assert.deepEqual(d.splitAria(0.615, "width"), { now: 62, min: Math.round(d.DRAWER_W_MIN * 100), max: Math.round(d.DRAWER_W_MAX * 100) });
});

test("splitAria: a drawn share outside the bounds reports inside them", () => {
  // An undragged 960px drawer on a >3200px grid is drawn under the 30% floor.
  assert.equal(d.splitAria(0.27, "width").now, Math.round(d.DRAWER_W_MIN * 100));
  assert.equal(d.splitAria(0.99, "width").now, Math.round(d.DRAWER_W_MAX * 100));
});

test("maxEscapeRestores: only a maximized drawer, focus on the page or in the drawer, nothing else owning Escape", () => {
  const base = { max: true, where: "body", owned: false, menuOpen: false };
  assert.equal(d.maxEscapeRestores(base), true);                        // a tree row click leaves focus on body
  assert.equal(d.maxEscapeRestores({ ...base, where: "drawer" }), true);
  assert.equal(d.maxEscapeRestores({ ...base, max: false }), false);
  assert.equal(d.maxEscapeRestores({ ...base, where: "other" }), false); // a terminal, the sidebar, a dialog
  assert.equal(d.maxEscapeRestores({ ...base, owned: true }), false);    // a field, the editor, a menu item
  assert.equal(d.maxEscapeRestores({ ...base, menuOpen: true }), false); // its Escape closes the menu only
  assert.equal(d.maxEscapeRestores(null), false);
});
