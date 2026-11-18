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

test("maxEscapeRestores: a press another handler already used (closed a menu or a page) is left alone", () => {
  assert.equal(d.maxEscapeRestores({ max: true, where: "body", owned: false, menuOpen: false, prevented: true }), false);
});

// escOwnedBy: does the element that has focus (the Escape's target) own the press? A field, an editor, a menu item. A tiny DOM with
// element.closest over the selectors the list uses (a tag, .class, [attr], [attr=value]) stands in for the page's.
function node(tag, o, parent) {
  const n = { tag, classes: (o && o.classes) || [], attrs: (o && o.attrs) || {}, parent: parent || null };
  n.matches = (sel) => {
    const m = /^([a-z]*)((?:\.[\w-]+)*)(?:\[([\w-]+)(?:=([\w-]+))?\])?$/.exec(sel);
    if (!m) throw new Error("test matcher does not know " + sel);
    if (m[1] && m[1] !== n.tag) return false;
    if (m[2] && !m[2].slice(1).split(".").every((c) => n.classes.includes(c))) return false;
    if (m[3] && !(m[3] in n.attrs && (m[4] === undefined || n.attrs[m[3]] === m[4]))) return false;
    return true;
  };
  n.closest = (list) => {
    const sels = list.split(",").map((x) => x.trim());
    for (let e = n; e; e = e.parent) if (sels.some((x) => e.matches(x))) return e;
    return null;
  };
  return n;
}

test("escOwnedBy: Monaco's input node (a div with EditContext on Chromium, a textarea elsewhere), CodeMirror, fields and menus own Escape; a button or the page do not", () => {
  const drawer = node("div");
  const monaco = node("div", { classes: ["monaco-editor", "focused"] }, drawer);
  assert.equal(d.escOwnedBy(node("div", { classes: ["native-edit-context"] }, monaco)), true, "Chrome/Edge: Monaco's EditContext is no textarea (Task 4 measured it)");
  assert.equal(d.escOwnedBy(node("textarea", { classes: ["inputarea"] }, monaco)), true, "Safari and Firefox");
  assert.equal(d.escOwnedBy(node("div", { classes: ["view-lines"] }, monaco)), true, "anything inside the editor");
  assert.equal(d.escOwnedBy(node("textarea", {}, node("div", { classes: ["CodeMirror"] }, drawer))), true, "CodeMirror, as before");
  assert.equal(d.escOwnedBy(node("input", {}, drawer)), true);
  assert.equal(d.escOwnedBy(node("div", { attrs: { contenteditable: "true" } }, drawer)), true);
  assert.equal(d.escOwnedBy(node("button", { attrs: { role: "menuitem" } }, node("div", { attrs: { role: "menu" } }, drawer))), true);
  assert.equal(d.escOwnedBy(node("button", {}, drawer)), false, "a button in the drawer lets the drawer's Escape through");
  assert.equal(d.escOwnedBy(node("div", { classes: ["dk-md"] }, drawer)), false, "the markdown Preview is not an editor");
  assert.equal(d.escOwnedBy(null), false);
  assert.equal(d.escOwnedBy({}), false, "a target with no closest (the document) owns nothing");
  assert.ok(/\.monaco-editor/.test(d.ESC_OWNERS), "the list names Monaco");
});

test("app.js asks escOwnedBy for the press's target, not its own list", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "app.js"), "utf8");
  assert.match(src, /owned: escOwnedBy\(t\)/);
  assert.ok(!/closest\("input, textarea, \[contenteditable\]/.test(src), "no second copy of the list in app.js");
});

// The sidebar width splitter (#split-v): a step in pixels, clamped to the sidebar's own bounds.
test("stepWidth: one step of 16px either way, clamped to min and max", () => {
  assert.equal(d.stepWidth(290, +1, 200, 560, 16), 306);
  assert.equal(d.stepWidth(290, -1, 200, 560, 16), 274);
  assert.equal(d.stepWidth(560, +1, 200, 560, 16), 560);
  assert.equal(d.stepWidth(552, +1, 200, 560, 16), 560);
  assert.equal(d.stepWidth(200, -1, 200, 560, 16), 200);
  assert.equal(d.stepWidth(210, -1, 200, 560, 16), 200);
});

test("stepWidth: a width that is not a number steps from the minimum", () => {
  assert.equal(d.stepWidth(NaN, +1, 200, 560, 16), 216);
  assert.equal(d.stepWidth(undefined, -1, 200, 560, 16), 200);
});

test("widthKeyDir: ArrowRight widens the sidebar, ArrowLeft narrows it, nothing else is ours", () => {
  assert.equal(d.widthKeyDir("ArrowRight"), 1);
  assert.equal(d.widthKeyDir("ArrowLeft"), -1);
  for (const k of ["ArrowUp", "ArrowDown", "Home", "End", "Enter", "Tab", " ", "a"]) assert.equal(d.widthKeyDir(k), 0, k);
});
