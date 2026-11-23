// docksections.js's pure half: the stored state of the explorer's two sections and the divider's arithmetic. The DOM glue
// is docksections-dom.test.js's.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  DOCK_SECTIONS_KEY, normalizeDockSections, loadDockSections, saveDockSections, dockSecBounds, dockSecShare, dockSecStep,
} = require("../static/docksections.js");

const DEF = { changes: true, tree: true, share: null };

test("the key is the dock's own, global (not per session)", () => {
  assert.strictEqual(DOCK_SECTIONS_KEY, "agent-box:dockSections");
});

test("normalizeDockSections: open by default, a share only when it is a sane fraction", () => {
  assert.deepStrictEqual(normalizeDockSections(null), DEF);
  assert.deepStrictEqual(normalizeDockSections([]), DEF);
  assert.deepStrictEqual(normalizeDockSections({ changes: false, tree: "no", share: 0.3 }), { changes: false, tree: true, share: 0.3 });
  for (const bad of ["x", 2, -1, NaN, Infinity, 0.01, 0.99, null, true]) {
    assert.strictEqual(normalizeDockSections({ share: bad }).share, null, String(bad));
  }
  assert.strictEqual(normalizeDockSections({ share: 0.33333 }).share, 0.333);
  assert.strictEqual(normalizeDockSections({ share: 0.05 }).share, 0.05);
  assert.strictEqual(normalizeDockSections({ share: 0.95 }).share, 0.95);
});

test("loadDockSections: broken, missing or throwing storage loads the defaults", () => {
  assert.deepStrictEqual(loadDockSections(null), DEF);
  assert.deepStrictEqual(loadDockSections({ getItem: () => null }), DEF);
  assert.deepStrictEqual(loadDockSections({ getItem: () => "{" }), DEF);
  assert.deepStrictEqual(loadDockSections({ getItem: () => { throw new Error("blocked"); } }), DEF);
  assert.deepStrictEqual(loadDockSections({ getItem: () => '"text"' }), DEF);
});

test("saveDockSections then loadDockSections round-trips; a throwing setItem is swallowed", () => {
  const m = new Map();
  const st = { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, v) };
  saveDockSections(st, { changes: false, tree: true, share: 0.42 });
  assert.deepStrictEqual(loadDockSections(st), { changes: false, tree: true, share: 0.42 });
  assert.doesNotThrow(() => saveDockSections({ setItem: () => { throw new Error("full"); } }, DEF));
  assert.doesNotThrow(() => saveDockSections(null, DEF));
});

const m = { col: 800, top: 40, fixed: 90, row: 22, treeMin: 120 };

test("dockSecBounds: Changes keeps its fixed parts and 3 rows, the tree its header and 3 rows", () => {
  assert.deepStrictEqual(dockSecBounds(m), { lo: 156, hi: 640 });
  // A column too short for both minimums: the section's own wins (the tree is the one that gives way, as today).
  assert.deepStrictEqual(dockSecBounds({ col: 200, top: 40, fixed: 90, row: 22, treeMin: 120 }), { lo: 156, hi: 156 });
});

test("dockSecShare: a height in px, held inside the bounds, as a share of the column", () => {
  assert.strictEqual(dockSecShare(100, m), 0.195);
  assert.strictEqual(dockSecShare(700, m), 0.8);
  assert.strictEqual(dockSecShare(300, m), 0.375);
  assert.strictEqual(dockSecShare(300, Object.assign({}, m, { col: 0 })), null);
  assert.strictEqual(dockSecShare(NaN, m), null);
});

test("dockSecStep: ArrowUp/ArrowDown move the divider 2% of the column, Home/End to the bounds, other keys nothing", () => {
  assert.strictEqual(dockSecStep(300, "ArrowDown", m), 0.395);
  assert.strictEqual(dockSecStep(300, "ArrowUp", m), 0.355);
  assert.strictEqual(dockSecStep(300, "Home", m), 0.195);
  assert.strictEqual(dockSecStep(300, "End", m), 0.8);
  assert.strictEqual(dockSecStep(160, "ArrowUp", m), 0.195, "held at the minimum");
  assert.strictEqual(dockSecStep(300, "a", m), null);
  assert.strictEqual(dockSecStep(300, "ArrowLeft", m), null);
});
