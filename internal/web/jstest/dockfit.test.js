// internal/web/jstest/dockfit.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { DOCK_FLOORS, dockLayout, dockFullLayout } = require("../static/dockfit.js");

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

test("two panes at 1030: the editor floor comes before the terminals, so the dock is 456 and the terminals 287", () => {
  const r = dockLayout({ avail: 1030, cols: 2, dockShare: null, treeShare: null });
  assert.deepEqual(r, { dockW: 456, treeW: F.strip, editorW: 420, treeFolded: true, full: false });
  assert.equal((1030 - r.dockW) / 2, 287, "two terminals of 287, above the 280 floor");
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

test("boundary, one pane: the dock stops shrinking at 456 and the terminal gives in only after that", () => {
  // 816 - 360 = 456: the dock is exactly editor floor + strip, the terminal exactly comfortable.
  assert.deepEqual(dockLayout({ avail: 816, cols: 1 }), { dockW: 456, treeW: F.strip, editorW: 420, treeFolded: true, full: false });
  // One px less: the dock holds at 456 and the terminal takes the px (359), instead of the dock growing back.
  const r = dockLayout({ avail: 815, cols: 1 });
  assert.deepEqual(r, { dockW: 456, treeW: F.strip, editorW: 420, treeFolded: true, full: false });
  assert.equal(815 - r.dockW, 359);
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

test("a tree share too big for the editor floor is clamped, and the explorer stays open", () => {
  const r = dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: 0.6 });
  assert.deepEqual(r, { dockW: 738, treeW: 313, editorW: F.editor, treeFolded: false, full: false });
});

test("a tree share under the explorer minimum is raised to it on a wide screen, not folded", () => {
  const r = dockLayout({ avail: 1190, cols: 1, dockShare: null, treeShare: 0.2 }); // 148 px wanted
  assert.deepEqual(r, { dockW: 738, treeW: F.tree, editorW: 533, treeFolded: false, full: false });
});

test("the explorer folds only when the dock is under 625 (tree min + split + editor floor)", () => {
  const unfoldAt = F.tree + F.split + F.editor;
  assert.equal(unfoldAt, 625);
  // cols 1: dockShare picks the dock width directly (the terminal stays comfortable at avail 1500).
  const at = (dockW) => dockLayout({ avail: 1500, cols: 1, dockShare: dockW / 1500, treeShare: 0.05 });
  assert.deepEqual(at(624), { dockW: 624, treeW: F.strip, editorW: 588, treeFolded: true, full: false });
  assert.deepEqual(at(625), { dockW: 625, treeW: F.tree, editorW: F.editor, treeFolded: false, full: false });
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

test("treeOpen: the explorer column opened from the strip pushes the terminals down to their floor first", () => {
  // 1190/2 would fold at a 470 dock; opening it asks for 625, which leaves the terminals about 282 each.
  assert.deepEqual(dockLayout({ avail: 1190, cols: 2, treeOpen: true }), {
    dockW: 625, treeW: F.tree, editorW: 420, treeFolded: false, full: false,
  });
  // 1030/2: the terminals are already near their floor, so the column takes what is left of them.
  assert.deepEqual(dockLayout({ avail: 1030, cols: 2, treeOpen: true }), {
    dockW: 470, treeW: F.tree, editorW: 265, treeFolded: false, full: false,
  });
  // One pane, narrow: 816 - 280 = 536.
  assert.deepEqual(dockLayout({ avail: 816, cols: 1, treeOpen: true }), {
    dockW: 536, treeW: F.tree, editorW: 331, treeFolded: false, full: false,
  });
});

test("treeOpen: an editor under 260 beside the open column is full mode", () => {
  // 1016/2 has a 456 dock and 560 for the terminals: the column would leave the editor 251.
  assert.equal(dockLayout({ avail: 1016, cols: 2 }).full, false);
  assert.equal(dockLayout({ avail: 1016, cols: 2, treeOpen: true }).full, true);
  assert.equal(dockLayout({ avail: 1024, cols: 2, treeOpen: true }).full, true, "editor 259");
  assert.deepEqual(dockLayout({ avail: 1025, cols: 2, treeOpen: true }), {
    dockW: 465, treeW: F.tree, editorW: 260, treeFolded: false, full: false,
  });
});

test("treeOpen changes nothing when the explorer is not folded, or when nothing fits", () => {
  for (const args of [{ avail: 1190, cols: 1 }, { avail: 2000, cols: 1 }, { avail: 1500, cols: 1, dockShare: 0.9 }, { avail: 1400, cols: 2 }]) {
    const plain = dockLayout(args);
    assert.equal(plain.treeFolded, false, JSON.stringify(args));
    assert.deepEqual(dockLayout({ ...args, treeOpen: true }), plain, JSON.stringify(args));
  }
  assert.deepEqual(dockLayout({ avail: 850, cols: 2, treeOpen: true }), dockLayout({ avail: 850, cols: 2 }));
});

test("treeOpen is on only for the boolean true", () => {
  for (const treeOpen of [undefined, null, false, 0, 1, "true", {}]) {
    assert.deepEqual(dockLayout({ avail: 1190, cols: 2, treeOpen }), dockLayout({ avail: 1190, cols: 2 }), String(treeOpen));
  }
});

test("a fractional width is floored first, so every size stays whole pixels", () => {
  assert.deepEqual(dockLayout({ avail: 1190.5, cols: 2 }), dockLayout({ avail: 1190, cols: 2 }));
  assert.deepEqual(dockLayout({ avail: 1190.9, cols: 1 }), dockLayout({ avail: 1190, cols: 1 }));
  const r = dockLayout({ avail: 1190.5, cols: 2 });
  assert.ok(Number.isInteger(r.dockW) && Number.isInteger(r.treeW) && Number.isInteger(r.editorW));
  assert.equal(dockLayout({ avail: 1015.9, cols: 2 }).full, true, "1015.9 is 1015, which is full");
});

test("narrowing the window never grows the dock, and the dock moves by at most 1px per px", () => {
  for (const cols of [1, 2]) {
    for (const dockShare of [null, 0.1, 0.3, 0.5, 0.62, 0.8, 0.9, 1]) {
      let prev = null; // dockW at avail + 1
      for (let avail = 3200; avail >= 300; avail--) {
        const r = dockLayout({ avail, cols, dockShare, treeShare: null });
        const ctx = JSON.stringify({ avail, cols, dockShare, r });
        if (r.full) { prev = null; continue; }
        if (prev !== null) {
          assert.ok(r.dockW <= prev, "dock grew while the window narrowed: " + ctx);
          assert.ok(prev - r.dockW <= 1, "dock jumped: " + ctx);
        }
        prev = r.dockW;
      }
    }
  }
});

test("full mode is one threshold, and stays full below it", () => {
  for (const cols of [1, 2]) {
    const lowest = cols * F.termFloor + MIN_DOCK;
    for (let avail = 0; avail < 3200; avail++) {
      assert.equal(dockLayout({ avail, cols }).full, avail < lowest, JSON.stringify({ avail, cols }));
    }
  }
});

test("invariants hold across widths, column counts and stored shares", () => {
  const shares = [null, 0.1, 0.3, 0.62, 0.9, 1.5];
  const trees = [null, 0.05, 0.15, 0.35, 0.75, 1.2];
  const unfoldAt = F.tree + F.split + F.editor;
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
          // The explorer folds exactly when the dock is too narrow for tree min + split + editor floor.
          assert.equal(r.treeFolded, r.dockW < unfoldAt, ctx);
          if (r.treeFolded) assert.equal(r.treeW, F.strip, ctx);
          else assert.ok(r.treeW >= F.tree, "an open explorer keeps its minimum: " + ctx);
          // The terminals go below comfort only once the dock is at its smallest.
          if (term < F.termComfy) assert.equal(r.dockW, MIN_DOCK, "squeezed while the dock could still shrink: " + ctx);
        }
      }
    }
  }
  assert.ok(seen > 1000, "the sweep must reach the non-full cases");
});

// --- Full mode: the dock is the whole pane area ---------------------------------------------------------------

test("full mode at 1100 wide: the explorer takes its default share and the editor the rest", () => {
  assert.deepEqual(dockFullLayout({ width: 1100 }), { treeW: 385, editorW: 710, treeFolded: false });
});

test("full mode: omitted, null and junk shares are the default share", () => {
  const dflt = dockFullLayout({ width: 1100, treeShare: null });
  assert.deepEqual(dockFullLayout({ width: 1100 }), dflt);
  assert.deepEqual(dockFullLayout({ width: 1100, treeShare: "wide" }), dflt);
  assert.deepEqual(dockFullLayout({ width: 1100, treeShare: NaN }), dflt);
});

test("full mode: a stored share is held between the explorer minimum and what leaves the editor its floor", () => {
  assert.equal(dockFullLayout({ width: 1100, treeShare: 0.05 }).treeW, F.tree);
  const r = dockFullLayout({ width: 1100, treeShare: 0.75 });
  assert.equal(r.treeW, 1100 - F.split - F.editor);
  assert.equal(r.editorW, F.editor);
});

test("full mode folds the explorer exactly when tree + splitter + editor floor do not fit", () => {
  const at = F.tree + F.split + F.editor;
  assert.equal(dockFullLayout({ width: at }).treeFolded, false);
  assert.equal(dockFullLayout({ width: at - 1 }).treeFolded, true);
  assert.deepEqual(dockFullLayout({ width: at - 1 }), { treeW: F.strip, editorW: at - 1 - F.strip, treeFolded: true });
});

test("full mode: a click on the strip opens the explorer at its minimum, however short the editor is left", () => {
  assert.deepEqual(dockFullLayout({ width: 500, treeOpen: true }), { treeW: F.tree, editorW: 500 - F.tree - F.split, treeFolded: false });
  assert.equal(dockFullLayout({ width: 100, treeOpen: true }).editorW, 0, "never negative");
  assert.equal(dockFullLayout({ width: 1100, treeOpen: true }).treeW, 385, "it changes nothing when the explorer is not folded");
});

test("full mode: the parts add up to the width, in whole pixels", () => {
  for (let width = 380; width <= 3200; width += 11) {
    for (const treeShare of [null, 0.15, 0.5, 0.75]) {
      for (const treeOpen of [false, true]) {
        const r = dockFullLayout({ width, treeShare, treeOpen });
        const ctx = JSON.stringify({ width, treeShare, treeOpen, r });
        assert.ok(Number.isInteger(r.treeW) && Number.isInteger(r.editorW), ctx);
        assert.equal(r.treeW + (r.treeFolded ? 0 : F.split) + r.editorW, width, ctx);
      }
    }
  }
});

test("full mode with no width or a junk one is a folded, empty dock", () => {
  for (const width of [undefined, null, NaN, -5, "800"]) {
    const r = dockFullLayout({ width });
    assert.equal(r.treeFolded, true, String(width));
    assert.equal(r.editorW, 0, String(width));
  }
  assert.equal(dockFullLayout().treeFolded, true);
});
