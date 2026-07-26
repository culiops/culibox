"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { reconcileLayout, serializeLayout, readLayout, writeLayout, LAYOUT_KEY } = require("../static/layout.js");

// A minimal localStorage stand-in: readLayout/writeLayout are the only impure
// functions here, and their contract (corrupt value → null, null → key removed)
// is what keeps a bad entry from breaking boot and what deletes a stale layout.
function fakeStorage() {
  const map = new Map();
  global.localStorage = {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: (k) => map.delete(k),
  };
  return map;
}

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

test("cap is limited to the values the layout buttons produce", () => {
  const cells = [cell(["a"])];
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 9 }), live("a")).cap, 4);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 0 }), live("a")).cap, 4);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 3 }), live("a")).cap, 4); // no 3-window button exists
  assert.strictEqual(reconcileLayout(snap(cells, { cap: "2" }), live("a")).cap, 2);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: 1 }), live("a")).cap, 1);
  assert.strictEqual(reconcileLayout(snap(cells, { cap: undefined }), live("a")).cap, 4);
});

test("a malformed cell entry is skipped, not fatal", () => {
  const plan = reconcileLayout(snap([null, { tabs: "x" }, cell(["a"]), { nope: 1 }]), live("a"));
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "a" }], active: 0 }]);
  assert.strictEqual(plan.focused, 0);
});

test("an out-of-range active index falls back to the first tab", () => {
  const plan = reconcileLayout(snap([cell(["a", "b"], 7)]), live("a", "b"));
  assert.strictEqual(plan.cells[0].active, 0);
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
  const a = { kind: "terminal", id: "a" };
  const b = { kind: "terminal", id: "b" };
  const c0 = { tabs: [a], activeTab: a };
  const c1 = { tabs: [viewer], activeTab: viewer };   // dropped — no terminal in it
  const c2 = { tabs: [viewer, b], activeTab: b };
  const out = serializeLayout({ cells: [c0, c1, c2], focused: c2, maximized: c2, cap: 4 });
  assert.deepStrictEqual(out.cells, [
    { tabs: [{ id: "a" }], active: 0 },
    { tabs: [{ id: "b" }], active: 0 },
  ]);
  // The viewer-only cell is gone, so the focused/maximized cell moved 2 → 1.
  assert.strictEqual(out.focused, 1);
  assert.strictEqual(out.maximized, 1);
});

test("serializeLayout records a maximized pane, and reconcile brings it back", () => {
  const a = { kind: "terminal", id: "a" };
  const b = { kind: "terminal", id: "b" };
  const c0 = { tabs: [a], activeTab: a };
  const c1 = { tabs: [b], activeTab: b };
  const out = serializeLayout({ cells: [c0, c1], focused: c0, maximized: c1, cap: 2 });
  assert.strictEqual(out.maximized, 1);
  assert.strictEqual(reconcileLayout(out, live("a", "b")).maximized, 1);
});

test("a serialized layout round-trips through reconcile unchanged", () => {
  const a = { kind: "terminal", id: "a" };
  const b = { kind: "terminal", id: "b" };
  const c = { kind: "terminal", id: "c" };
  const c0 = { tabs: [a], activeTab: a };
  const c1 = { tabs: [b, c], activeTab: c };
  const out = serializeLayout({ cells: [c0, c1], focused: c1, maximized: null, cap: 2 });
  const plan = reconcileLayout(out, live("a", "b", "c"));
  assert.deepStrictEqual(plan, {
    cap: out.cap, focused: out.focused, maximized: out.maximized, cells: out.cells,
  });
});

test("serializeLayout returns null when no terminal is open", () => {
  const viewer = { kind: "viewer", path: "/x" };
  const c0 = { tabs: [viewer], activeTab: viewer };
  assert.strictEqual(serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 }), null);
  assert.strictEqual(serializeLayout({ cells: [], focused: null, maximized: null, cap: 4 }), null);
});

test("writeLayout stores a snapshot and readLayout parses it back", () => {
  const map = fakeStorage();
  const s = { v: 1, cap: 2, focused: 0, maximized: null, cells: [cell(["a"])] };
  writeLayout(s);
  assert.strictEqual(map.get(LAYOUT_KEY), JSON.stringify(s));
  assert.deepStrictEqual(readLayout(), s);
});

test("writeLayout(null) removes the key — an empty grid must not restore", () => {
  const map = fakeStorage();
  writeLayout({ v: 1, cap: 4, focused: 0, maximized: null, cells: [cell(["a"])] });
  writeLayout(null);
  assert.strictEqual(map.has(LAYOUT_KEY), false);
  assert.strictEqual(readLayout(), null);
});

test("a corrupt stored value reads as null instead of throwing", () => {
  const map = fakeStorage();
  map.set(LAYOUT_KEY, "{not json");
  assert.strictEqual(readLayout(), null);
  assert.strictEqual(reconcileLayout(readLayout(), live("a")), null);
});

test("an absent key reads as null", () => {
  fakeStorage();
  assert.strictEqual(readLayout(), null);
});
