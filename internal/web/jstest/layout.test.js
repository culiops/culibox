"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { reconcileLayout, serializeLayout } = require("../static/layout.js");

const live = (...ids) => ids.map((id) => ({ id, name: id, status: "live", exited: false }));
const snap = (cells, extra) => Object.assign({ v: 1, cap: 4, focused: 0, maximized: null, cells }, extra || {});
const cell = (ids, active) => ({ tabs: ids.map((id) => ({ id })), active: active || 0 });

test("a snapshot whose sessions are all live is restored unchanged", () => {
  const plan = reconcileLayout(snap([cell(["a"]), cell(["b", "c"], 1)], { focused: 1 }), live("a", "b", "c"));
  assert.deepStrictEqual(plan, {
    cap: 4,
    focused: 1,
    maximized: null,
    cells: [{ tabs: [{ id: "a" }], active: 0 }, { tabs: [{ id: "b" }, { id: "c" }], active: 1 }],
  });
});

test("a tab whose session is gone is dropped", () => {
  const plan = reconcileLayout(snap([cell(["a", "gone"])]), live("a"));
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "a" }], active: 0 }]);
});

test("an exited session is dropped, a detached one is kept", () => {
  const sessions = [
    { id: "dead", status: "exited", exited: true },
    { id: "bg", status: "detached", exited: false },
  ];
  const plan = reconcileLayout(snap([cell(["dead", "bg"])]), sessions);
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "bg" }], active: 0 }]);
});

test("a cell that loses every tab is dropped and focused is remapped", () => {
  const plan = reconcileLayout(snap([cell(["gone"]), cell(["b"])], { focused: 1 }), live("b"));
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "b" }], active: 0 }]);
  assert.strictEqual(plan.focused, 0);
});

test("the active tab keeps its identity, not its index", () => {
  const plan = reconcileLayout(snap([cell(["gone", "b"], 1)]), live("b"));
  assert.strictEqual(plan.cells[0].active, 0);
});

test("an active tab that died falls back to the first survivor", () => {
  const plan = reconcileLayout(snap([cell(["a", "gone"], 1)]), live("a"));
  assert.strictEqual(plan.cells[0].active, 0);
});

test("a duplicate id is kept once, in its first position", () => {
  const plan = reconcileLayout(snap([cell(["a"]), cell(["a", "b"])]), live("a", "b"));
  assert.deepStrictEqual(plan.cells, [
    { tabs: [{ id: "a" }], active: 0 },
    { tabs: [{ id: "b" }], active: 0 },
  ]);
});

test("nothing survivable returns null", () => {
  assert.strictEqual(reconcileLayout(snap([cell(["gone"])]), live("a")), null);
  assert.strictEqual(reconcileLayout(snap([]), live("a")), null);
});

test("a snapshot that is missing, wrong-shaped or wrong-version returns null", () => {
  for (const bad of [null, undefined, 42, "not json", {}, { v: 2, cells: [cell(["a"])] }, { v: 1, cells: "x" }]) {
    assert.strictEqual(reconcileLayout(bad, live("a")), null);
  }
});

test("cap is clamped to 1..4 and defaults to 4", () => {
  const cells = [cell(["a"])];
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 9 }), live("a")).cap, 4);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 0 }), live("a")).cap, 4);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: "2" }), live("a")).cap, 2);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: undefined }), live("a")).cap, 4);
});

test("focused and maximized are clamped or cleared", () => {
  const cells = [cell(["a"]), cell(["b"])];
  assert.strictEqual(reconcileLayout(snap(cells, { focused: 7 }), live("a", "b")).focused, 0);
  assert.strictEqual(reconcileLayout(snap(cells, { maximized: 1 }), live("a", "b")).maximized, 1);
  // the maximized cell is the one that disappears
  assert.strictEqual(reconcileLayout(snap(cells, { maximized: 0 }), live("b")).maximized, null);
});

test("serializeLayout captures panes, active tab, focus and cap", () => {
  const t = (id) => ({ kind: "terminal", id });
  const c0 = { tabs: [t("a")], activeTab: null };
  c0.activeTab = c0.tabs[0];
  const c1 = { tabs: [t("b"), t("c")], activeTab: null };
  c1.activeTab = c1.tabs[1];
  assert.deepStrictEqual(
    serializeLayout({ cells: [c0, c1], focused: c1, maximized: null, cap: 2 }),
    { v: 1, cap: 2, focused: 1, maximized: null,
      cells: [{ tabs: [{ id: "a" }], active: 0 }, { tabs: [{ id: "b" }, { id: "c" }], active: 1 }] },
  );
});

test("serializeLayout skips viewer tabs and cells that hold only viewers", () => {
  const viewer = { kind: "viewer", path: "/x" };
  const term = { kind: "terminal", id: "a" };
  const c0 = { tabs: [viewer], activeTab: viewer };
  const c1 = { tabs: [viewer, term], activeTab: term };
  const out = serializeLayout({ cells: [c0, c1], focused: c1, maximized: null, cap: 4 });
  assert.deepStrictEqual(out.cells, [{ tabs: [{ id: "a" }], active: 0 }]);
  assert.strictEqual(out.focused, 0);
});

test("serializeLayout returns null when no terminal is open", () => {
  const viewer = { kind: "viewer", path: "/x" };
  const c0 = { tabs: [viewer], activeTab: viewer };
  assert.strictEqual(serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 }), null);
  assert.strictEqual(serializeLayout({ cells: [], focused: null, maximized: null, cap: 4 }), null);
});
