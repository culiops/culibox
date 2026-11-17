// internal/web/jstest/dockfit.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { DOCK_FLOORS, dockLayout } = require("../static/dockfit.js");

const F = DOCK_FLOORS;
const MIN_DOCK = F.editor + F.strip; // the smallest usable dock: editor floor + the folded explorer strip

test("DOCK_FLOORS are the spec values", () => {
  assert.deepEqual(F, { termComfy: 360, termFloor: 280, editor: 420, tree: 200, strip: 36, split: 5 });
});

test("one pane at 1190: the default shares fit and nothing folds", () => {
  assert.deepEqual(dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: null }), {
    dockW: 738, treeW: 258, editorW: 475, treeFolded: false, full: false,
  });
});

test("omitted shares are the same as null shares", () => {
  assert.deepEqual(dockLayout({ avail: 1190, cols: 1 }), dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: null }));
});

test("two panes at 1030: the explorer folds and the terminals drop to 280", () => {
  const r = dockLayout({ avail: 1030, cols: 2, dockShare: null, treeShare: null });
  assert.deepEqual(r, { dockW: 470, treeW: F.strip, editorW: 434, treeFolded: true, full: false });
  assert.equal((1030 - r.dockW) / 2, F.termFloor, "two terminals of 280: the grid keeps 560");
});

test("two panes at 850: the floors cannot be met, so full mode", () => {
  assert.equal(dockLayout({ avail: 850, cols: 2, dockShare: null, treeShare: null }).full, true);
});

test("a wide dock share is held back so the terminal keeps its comfort width", () => {
  const r = dockLayout({ avail: 1500, cols: 1, dockShare: 0.9, treeShare: null });
  assert.equal(r.dockW, 1140, "min(1350, 1500 - 360)");
  assert.equal(1500 - r.dockW, F.termComfy);
  assert.equal(r.full, false);
});

test("resize 1190 -> 850 -> 1190 (two panes) has no memory: the first answer comes back", () => {
  const at = (avail) => dockLayout({ avail, cols: 2, dockShare: null, treeShare: null });
  const first = at(1190);
  assert.deepEqual(first, { dockW: 470, treeW: F.strip, editorW: 434, treeFolded: true, full: false });
  assert.equal((1190 - first.dockW) / 2, F.termComfy, "terminals 360 each");
  assert.equal(at(850).full, true);
  assert.deepEqual(at(1190), first);
});

test("boundary, one pane: the terminal keeps 360 down to a 456 dock, then falls toward 280", () => {
  // 816 - 360 = 456: the dock is exactly editor floor + strip, the terminal exactly comfortable.
  assert.deepEqual(dockLayout({ avail: 816, cols: 1 }), { dockW: 456, treeW: F.strip, editorW: 420, treeFolded: true, full: false });
  // One px less and the comfortable dock is 455, below the editor floor, so the terminal gives in.
  const r = dockLayout({ avail: 815, cols: 1 });
  assert.equal(r.dockW, 505, "the wanted share (.62) still fits once the terminal may be 310");
  assert.equal(815 - r.dockW, 310);
  assert.equal(r.treeFolded, true);
  assert.equal(r.editorW, 505 - F.strip);
});

test("boundary, two panes: full exactly when the dock cannot reach 456 with 280 terminals", () => {
  // 1016 - 2*280 = 456
  assert.deepEqual(dockLayout({ avail: 1016, cols: 2 }), { dockW: 456, treeW: F.strip, editorW: 420, treeFolded: true, full: false });
  assert.equal(dockLayout({ avail: 1015, cols: 2 }).full, true);
});

test("a stored share that would make the dock too small is raised to editor floor + strip when space allows", () => {
  const r = dockLayout({ avail: 1190, cols: 1, dockShare: 0.1, treeShare: null });
  assert.deepEqual(r, { dockW: MIN_DOCK, treeW: F.strip, editorW: F.editor, treeFolded: true, full: false });
});

test("a tree share that leaves the editor under its floor folds the explorer", () => {
  const r = dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: 0.6 });
  assert.equal(r.dockW, 738);
  assert.equal(r.treeFolded, true);
  assert.equal(r.treeW, F.strip);
  assert.equal(r.editorW, 738 - F.strip);
});

test("a tree share under the explorer minimum folds it rather than showing a sliver", () => {
  const r = dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: 0.2 }); // 148 px tree
  assert.equal(r.treeFolded, true);
  assert.equal(r.treeW, F.strip);
});

test("a wide window keeps the explorer open and gives the editor the rest", () => {
  assert.deepEqual(dockLayout({ avail: 2000, cols: 1 }), { dockW: 1240, treeW: 434, editorW: 801, treeFolded: false, full: false });
});

test("full mode reports no dock, so a caller cannot size a panel from it by mistake", () => {
  assert.deepEqual(dockLayout({ avail: 700, cols: 2 }), { dockW: 0, treeW: 0, editorW: 0, treeFolded: false, full: true });
});

test("bad input never throws: no usable width is full mode, a bad cols is one column", () => {
  for (const avail of [NaN, undefined, null, -5, 0, Infinity, "1190"]) {
    assert.equal(dockLayout({ avail, cols: 1 }).full, true, String(avail));
  }
  assert.deepEqual(dockLayout({ avail: 1190, cols: 0 }), dockLayout({ avail: 1190, cols: 1 }));
  assert.deepEqual(dockLayout({ avail: 1190, cols: NaN }), dockLayout({ avail: 1190, cols: 1 }));
  assert.deepEqual(dockLayout({ avail: 1190, cols: 1, dockShare: NaN, treeShare: "x" }), dockLayout({ avail: 1190, cols: 1 }));
  assert.doesNotThrow(() => dockLayout());
  assert.equal(dockLayout().full, true);
});

test("dockLayout does not change its argument", () => {
  const args = Object.freeze({ avail: 1190, cols: 2, dockShare: 0.5, treeShare: 0.4 });
  assert.doesNotThrow(() => dockLayout(args));
});

test("invariants hold across widths, column counts and stored shares", () => {
  const shares = [null, 0.1, 0.3, 0.62, 0.9, 1.5];
  const trees = [null, 0.15, 0.35, 0.75];
  let seen = 0;
  for (let avail = 300; avail <= 3200; avail += 7) {
    for (const cols of [1, 2]) {
      for (const dockShare of shares) {
        for (const treeShare of trees) {
          const r = dockLayout({ avail, cols, dockShare, treeShare });
          const ctx = JSON.stringify({ avail, cols, dockShare, treeShare, r });
          // Full exactly when the smallest dock cannot be had with the terminals at their hard floor.
          assert.equal(r.full, avail - cols * F.termFloor < MIN_DOCK, ctx);
          if (r.full) continue;
          seen++;
          const term = (avail - r.dockW) / cols;
          assert.ok(term >= F.termFloor, "terminals keep their hard floor: " + ctx);
          assert.ok(r.editorW >= F.editor, "editor keeps its floor: " + ctx);
          assert.ok(r.dockW >= MIN_DOCK, ctx);
          assert.ok(Number.isInteger(r.dockW) && Number.isInteger(r.treeW) && Number.isInteger(r.editorW), ctx);
          assert.equal(r.dockW, r.treeW + (r.treeFolded ? 0 : F.split) + r.editorW, "the parts add up to the dock: " + ctx);
          if (r.treeFolded) assert.equal(r.treeW, F.strip, ctx);
          else assert.ok(r.treeW >= F.tree, "an open explorer keeps its minimum: " + ctx);
          // Terminals go below comfort only when the dock could not shrink to give them comfort.
          if (term < F.termComfy) assert.ok(avail - cols * F.termComfy < MIN_DOCK, "squeezed while the dock could still shrink: " + ctx);
        }
      }
    }
  }
  assert.ok(seen > 1000, "the sweep must reach the non-full cases");
});
