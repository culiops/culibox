// internal/web/jstest/shelldock.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const d = require("../static/shelldock.js");

test("DOCK_PLACEMENTS lists below and right", () => {
  assert.deepStrictEqual(d.DOCK_PLACEMENTS, ["below", "right"]);
});

test("newDock is an empty dock below the pane at 0.4", () => {
  assert.deepStrictEqual(d.newDock(), { placement: "below", ids: [], active: null, ratio: 0.4 });
  assert.notStrictEqual(d.newDock().ids, d.newDock().ids);
});

test("dockAdd appends an absent id and makes it active", () => {
  let k = d.dockAdd(d.newDock(), "a");
  assert.deepStrictEqual(k.ids, ["a"]);
  assert.strictEqual(k.active, "a");
  k = d.dockAdd(k, "b");
  assert.deepStrictEqual(k.ids, ["a", "b"]);
  assert.strictEqual(k.active, "b");
});

test("dockAdd of a present id only activates it", () => {
  let k = d.dockAdd(d.dockAdd(d.newDock(), "a"), "b");
  k = d.dockAdd(k, "a");
  assert.deepStrictEqual(k.ids, ["a", "b"]);
  assert.strictEqual(k.active, "a");
});

test("dockAdd does not mutate its input and accepts a missing dock", () => {
  const k0 = d.newDock();
  d.dockAdd(k0, "a");
  assert.deepStrictEqual(k0.ids, []);
  assert.deepStrictEqual(d.dockAdd(null, "a").ids, ["a"]);
});

test("dockRemove of the active id moves active to the left neighbour, else the right", () => {
  let k = { placement: "below", ids: ["a", "b", "c"], active: "b", ratio: 0.5 };
  assert.strictEqual(d.dockRemove(k, "b").active, "a");
  k = { placement: "below", ids: ["a", "b", "c"], active: "a", ratio: 0.5 };
  assert.strictEqual(d.dockRemove(k, "a").active, "b");
  assert.deepStrictEqual(d.dockRemove(k, "a").ids, ["b", "c"]);
});

test("dockRemove of an inactive id keeps the active id", () => {
  const k = { placement: "right", ids: ["a", "b", "c"], active: "c", ratio: 0.3 };
  const r = d.dockRemove(k, "a");
  assert.strictEqual(r.active, "c");
  assert.strictEqual(r.placement, "right");
  assert.strictEqual(r.ratio, 0.3);
});

test("dockRemove returns null when the dock empties; an absent id changes nothing", () => {
  const k = d.dockAdd(d.newDock(), "a");
  assert.strictEqual(d.dockRemove(k, "a"), null);
  assert.deepStrictEqual(d.dockRemove(k, "zzz"), k);
  assert.strictEqual(d.dockRemove(null, "a"), null);
});

test("dockSetPlacement accepts below/right and turns anything else into below", () => {
  const k = d.dockAdd(d.newDock(), "a");
  assert.strictEqual(d.dockSetPlacement(k, "right").placement, "right");
  assert.strictEqual(d.dockSetPlacement(k, "left").placement, "below");
  assert.strictEqual(d.dockSetPlacement(k, undefined).placement, "below");
  assert.strictEqual(k.placement, "below");
});

test("clampDockRatio clamps to [0.2, 0.8]; NaN or a non-number is 0.4", () => {
  assert.strictEqual(d.clampDockRatio(0.5), 0.5);
  assert.strictEqual(d.clampDockRatio(0.05), 0.2);
  assert.strictEqual(d.clampDockRatio(0.95), 0.8);
  assert.strictEqual(d.clampDockRatio(NaN), 0.4);
  assert.strictEqual(d.clampDockRatio("0.5"), 0.4);
  assert.strictEqual(d.clampDockRatio("x"), 0.4);
  assert.strictEqual(d.clampDockRatio(null), 0.4);
  assert.strictEqual(d.clampDockRatio(undefined), 0.4);
});

test("normalizeDock cleans a stored dock and refuses garbage", () => {
  assert.strictEqual(d.normalizeDock("nope"), null);
  assert.strictEqual(d.normalizeDock(null), null);
  assert.strictEqual(d.normalizeDock({ ids: [] }), null);
  assert.strictEqual(d.normalizeDock({ ids: "a" }), null);
  assert.deepStrictEqual(
    d.normalizeDock({ placement: "left", ids: ["a", 7, "", "a", "b"], active: "zzz", ratio: "x" }),
    { placement: "below", ids: ["a", "b"], active: "a", ratio: 0.4 });
  assert.deepStrictEqual(
    d.normalizeDock({ placement: "right", ids: ["a", "b"], active: "b", ratio: 9 }),
    { placement: "right", ids: ["a", "b"], active: "b", ratio: 0.8 });
});

const model = () => ({
  cells: [
    { tabs: [{ kind: "terminal", id: "p1" }], dock: { placement: "below", ids: ["s1"], active: "s1", ratio: 0.4 } },
    { tabs: [{ kind: "viewer", path: "/x" }, { kind: "terminal", id: "p2" }, { kind: "terminal", id: "s9" }] },
  ],
});

test("placeShell activates a shell already docked in some cell", () => {
  assert.deepStrictEqual(d.placeShell(model(), "s1", "p1"), { action: "activate", cellIndex: 0 });
});

test("placeShell activates a shell that already is a tab, wherever its parent is", () => {
  assert.deepStrictEqual(d.placeShell(model(), "s9", "p1"), { action: "activate", cellIndex: 1 });
});

test("placeShell docks into the cell whose tabs hold the parent", () => {
  assert.deepStrictEqual(d.placeShell(model(), "s2", "p2"), { action: "dock", cellIndex: 1 });
  assert.deepStrictEqual(d.placeShell(model(), "s2", "p1"), { action: "dock", cellIndex: 0 });
});

test("placeShell says own when no cell holds the parent", () => {
  assert.deepStrictEqual(d.placeShell(model(), "s2", "gone"), { action: "own", cellIndex: -1 });
  assert.deepStrictEqual(d.placeShell(model(), "s2", null), { action: "own", cellIndex: -1 });
  assert.deepStrictEqual(d.placeShell({ cells: [] }, "s2", "p1"), { action: "own", cellIndex: -1 });
  assert.deepStrictEqual(d.placeShell({}, "s2", "p1"), { action: "own", cellIndex: -1 });
});

test("placeShell never matches a viewer tab as a parent", () => {
  const m = { cells: [{ tabs: [{ kind: "viewer", path: "/x", id: "p1" }] }] };
  assert.deepStrictEqual(d.placeShell(m, "s2", "p1"), { action: "own", cellIndex: -1 });
});

// ---- + Shell routing and the sidebar reveal ----
test("shellSpawnRoute: below/right dock, own gets a pane, anything else docks below", () => {
  assert.strictEqual(d.shellSpawnRoute("below", false), "below");
  assert.strictEqual(d.shellSpawnRoute("right", false), "right");
  assert.strictEqual(d.shellSpawnRoute("own", false), "own");
  assert.strictEqual(d.shellSpawnRoute(undefined, false), "below");
  assert.strictEqual(d.shellSpawnRoute("left", false), "below");
});

test("shellSpawnRoute: mobile always opens the shell as a tab, whatever was asked", () => {
  for (const p of ["below", "right", "own", undefined]) assert.strictEqual(d.shellSpawnRoute(p, true), "tab");
});

test("shellSpawnRoute: an unlinked shell (nothing alive to belong to) is a plain tab, never docked", () => {
  assert.strictEqual(d.shellSpawnRoute("below", false, false), "tab");
  assert.strictEqual(d.shellSpawnRoute("right", false, false), "tab");
  assert.strictEqual(d.shellSpawnRoute(undefined, false, false), "tab");
  assert.strictEqual(d.shellSpawnRoute("own", false, false), "own", "an explicit own pane is still honoured");
  assert.strictEqual(d.shellSpawnRoute("below", true, false), "tab");
  assert.strictEqual(d.shellSpawnRoute("below", false, true), "below");
});

test("revealPlan: a shell already open anywhere is only opened (activated) where it is", () => {
  assert.strictEqual(d.revealPlan(model(), "s1", { id: "p1" }, false), "open");
  assert.strictEqual(d.revealPlan(model(), "s9", { id: "p1" }, false), "open");
});

test("revealPlan: a closed shell docks into the pane holding its parent", () => {
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "p2" }, false), "dock");
  // An ended parent that is still on screen still takes the dock.
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "p1", exited: true }, false), "dock");
});

test("revealPlan: parent not on screen -> open the parent first, unless it has ended", () => {
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "gone" }, false), "open-parent");
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "gone", exited: true }, false), "open");
});

test("revealPlan: no parent, or mobile, opens the shell like any session", () => {
  assert.strictEqual(d.revealPlan(model(), "s2", null, false), "open");
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "p2" }, true), "open");
  assert.strictEqual(d.revealPlan(model(), "s2", { id: "gone" }, true), "open");
});

// ---- splitter ----
const rect = { left: 100, top: 50, width: 800, height: 400 }; // right 900, bottom 450

test("dockRatioFromPointer below: the share of the pane under the pointer", () => {
  assert.strictEqual(d.dockRatioFromPointer("below", rect, 0, 290), 0.4);  // 160 of 400
  assert.strictEqual(d.dockRatioFromPointer("below", rect, 0, 250), 0.5);
});

test("dockRatioFromPointer right: the share of the pane right of the pointer", () => {
  assert.strictEqual(d.dockRatioFromPointer("right", rect, 500, 0), 0.5);
  assert.strictEqual(d.dockRatioFromPointer("right", rect, 580, 0), 0.4);  // 320 of 800
});

test("dockRatioFromPointer clamps a drag past either edge", () => {
  assert.strictEqual(d.dockRatioFromPointer("below", rect, 0, 0), 0.8);
  assert.strictEqual(d.dockRatioFromPointer("below", rect, 0, 9999), 0.2);
  assert.strictEqual(d.dockRatioFromPointer("right", rect, 0, 0), 0.8);
  assert.strictEqual(d.dockRatioFromPointer("right", rect, 9999, 0), 0.2);
});

test("dockRatioFromPointer of a pane with no size is the default, never NaN", () => {
  assert.strictEqual(d.dockRatioFromPointer("below", { left: 0, top: 0, width: 0, height: 0 }, 5, 5), 0.4);
  assert.strictEqual(d.dockRatioFromPointer("right", null, 5, 5), 0.4);
});

test("dockRatioFromPointer treats an unknown placement as below", () => {
  assert.strictEqual(d.dockRatioFromPointer("left", rect, 0, 250), 0.5);
});
