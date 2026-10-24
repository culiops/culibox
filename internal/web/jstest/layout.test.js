"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { reconcileLayout, serializeLayout, readLayout, writeLayout, tabKey, LAYOUT_KEY } = require("../static/layout.js");

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

test("serializeLayout keeps viewer-only cells, so pane indexes do not shift", () => {
  const viewer = { kind: "viewer", path: "/x", scroll: 0 };
  const a = { kind: "terminal", id: "a" };
  const b = { kind: "terminal", id: "b" };
  const c0 = { tabs: [a], activeTab: a };
  const c1 = { tabs: [viewer], activeTab: viewer };
  const c2 = { tabs: [viewer, b], activeTab: b };
  const out = serializeLayout({ cells: [c0, c1, c2], focused: c2, maximized: c2, cap: 4 });
  assert.deepStrictEqual(out.cells, [
    { tabs: [{ id: "a" }], active: 0 },
    { tabs: [{ path: "/x", scroll: 0 }], active: 0 },
    { tabs: [{ path: "/x", scroll: 0 }, { id: "b" }], active: 1 },
  ]);
  // No cell is dropped now, so the focused/maximized pane keeps index 2.
  assert.strictEqual(out.focused, 2);
  assert.strictEqual(out.maximized, 2);
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

test("serializeLayout returns null for an empty grid", () => {
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

// --- viewer tabs in the snapshot ---------------------------------------------
//
// A viewer is stored as its path plus how far down the file was read, so a
// reload comes back to the same place in the same file. Unlike a terminal it has
// nothing to reconcile against — a path is not in the session list — so a file
// deleted since the last visit is caught by the restored tab's own first read.

const view = (path, scroll) => ({ kind: "viewer", path, scroll });
const term = (id) => ({ kind: "terminal", id });

test("serializeLayout stores a viewer as its path and how far it was read", () => {
  const v = view("/home/u/a.md", 420);
  const t = term("a");
  const c0 = { tabs: [t, v], activeTab: v };
  const out = serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 });
  assert.deepStrictEqual(out.cells, [
    { tabs: [{ id: "a" }, { path: "/home/u/a.md", scroll: 420 }], active: 1 },
  ]);
});

test("a cell holding only viewers is kept, and so is a grid with no session", () => {
  const v = view("/x.md", 10);
  const c0 = { tabs: [v], activeTab: v };
  const out = serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 });
  assert.deepStrictEqual(out.cells, [{ tabs: [{ path: "/x.md", scroll: 10 }], active: 0 }]);
  assert.strictEqual(out.focused, 0);
});

test("serializeLayout returns null only when nothing at all is restorable", () => {
  const junk = { kind: "viewer", path: "" }; // a viewer with no path
  const c0 = { tabs: [junk], activeTab: junk };
  assert.strictEqual(serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 }), null);
});

test("a stored scroll offset is coerced to a sane pixel count", () => {
  const mk = (scroll) => {
    const v = view("/x.md", scroll);
    return serializeLayout({ cells: [{ tabs: [v], activeTab: v }], focused: null, maximized: null, cap: 4 })
      .cells[0].tabs[0].scroll;
  };
  assert.strictEqual(mk(undefined), 0);
  assert.strictEqual(mk(NaN), 0);
  assert.strictEqual(mk(-5), 0);
  assert.strictEqual(mk("nope"), 0);
  assert.strictEqual(mk(12.7), 13);
  assert.strictEqual(mk("300"), 300);
});

test("reconcile keeps a viewer without consulting the session list", () => {
  const plan = reconcileLayout(
    snap([{ tabs: [{ id: "gone" }, { path: "/x.md", scroll: 7 }], active: 0 }]),
    live("other"),
  );
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ path: "/x.md", scroll: 7 }], active: 0 }]);
});

test("reconcile drops a viewer entry with no usable path", () => {
  const plan = reconcileLayout(
    snap([{ tabs: [{ path: "" }, { path: 42 }, { id: "a" }], active: 0 }]),
    live("a"),
  );
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "a" }], active: 0 }]);
});

test("the same file open in two panes is kept once, like a session", () => {
  const plan = reconcileLayout(
    snap([{ tabs: [{ path: "/x.md", scroll: 3 }], active: 0 },
      { tabs: [{ path: "/x.md", scroll: 9 }, { id: "a" }], active: 0 }]),
    live("a"),
  );
  assert.deepStrictEqual(plan.cells, [
    { tabs: [{ path: "/x.md", scroll: 3 }], active: 0 },
    { tabs: [{ id: "a" }], active: 0 },
  ]);
});

test("the active tab follows identity across a mixed cell whose session died", () => {
  const plan = reconcileLayout(
    snap([{ tabs: [{ id: "gone" }, { path: "/a.md", scroll: 1 }, { path: "/b.md", scroll: 2 }], active: 2 }]),
    live("x"),
  );
  assert.strictEqual(plan.cells[0].active, 1); // /b.md, now at index 1
});

test("a viewer scroll offset survives a serialize/reconcile round trip", () => {
  const v = view("/deep.md", 1234);
  const t = term("a");
  const c0 = { tabs: [t, v], activeTab: v };
  const out = serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 2 });
  const plan = reconcileLayout(out, live("a"));
  assert.deepStrictEqual(plan.cells, out.cells);
  assert.strictEqual(plan.cells[0].tabs[1].scroll, 1234);
});

test("an entry carrying BOTH keys leaves in the shape it was classified as", () => {
  // tabKey reads `path` first, so {id, path} is a viewer and must not be
  // liveness-checked as a terminal. The inverse — a non-string path — is a
  // terminal, and must NOT come back out as {path: 42}: applyPlan would hand
  // that to new ViewerTab(42) and the restore would throw half-built.
  const plan = reconcileLayout(
    snap([{ tabs: [{ id: "gone", path: 42 }, { id: "dead", path: "/v.md", scroll: 5 }], active: 0 }]),
    live("a"),
  );
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ path: "/v.md", scroll: 5 }], active: 0 }]);
});

test("a hand-edited scroll offset is coerced on the way OUT of reconcile too", () => {
  // The reconcile side is the one that reads user-editable localStorage, so it
  // is the side that actually has to defend scrollTop.
  const mk = (scroll) => reconcileLayout(
    snap([{ tabs: [{ path: "/x.md", scroll }], active: 0 }]), live("a"),
  ).cells[0].tabs[0].scroll;
  assert.strictEqual(mk(-1), 0);
  assert.strictEqual(mk("1e999"), 0);   // Infinity is not a pixel count
  assert.strictEqual(mk(null), 0);
  assert.strictEqual(mk({}), 0);
  assert.strictEqual(mk(19.4), 19);
});

test("a v1 snapshot written before viewers existed still restores", () => {
  const plan = reconcileLayout(snap([cell(["a", "b"], 1)]), live("a", "b"));
  assert.deepStrictEqual(plan.cells, [{ tabs: [{ id: "a" }, { id: "b" }], active: 1 }]);
});

// A box viewer is the same file path as a host viewer but a different file: the
// path only exists inside that session's box. Its key carries the session so two
// boxes' identically-named screenshots, and a host file at the same path, never
// collapse into one tab.
test("a box viewer keys on its session and never collides with a host viewer", () => {
  const p = "/tmp/playwright-mcp/a.png";
  assert.strictEqual(tabKey({ box: "s1", path: p }), "b:s1:" + p);
  assert.strictEqual(tabKey({ path: p }), "v:" + p);
  assert.strictEqual(tabKey({ box: "", path: p }), "v:" + p);
  assert.notStrictEqual(tabKey({ box: "s1", path: p }), tabKey({ box: "s2", path: p }));
});

test("a corrupt box field is ignored, a corrupt path with a box is dropped", () => {
  assert.strictEqual(tabKey({ box: 42, path: "/x" }), "v:/x");
  assert.strictEqual(tabKey({ box: "s1", path: 42 }), null);
  assert.strictEqual(tabKey({ box: "s1", path: "" }), null);
});

test("a box viewer and a host viewer of the same path both survive a round trip", () => {
  const p = "/tmp/playwright-mcp/a.png";
  const a = { kind: "terminal", id: "a" };
  const bv = { kind: "viewer", path: p, box: "s1", scroll: 7 };
  const hv = { kind: "viewer", path: p, box: "", scroll: 3 };
  const c0 = { tabs: [a, bv, hv], activeTab: bv };
  const out = serializeLayout({ cells: [c0], focused: c0, maximized: null, cap: 4 });
  const want = [{ id: "a" }, { path: p, box: "s1", scroll: 7 }, { path: p, scroll: 3 }];
  assert.deepStrictEqual(out.cells[0].tabs, want);
  const plan = reconcileLayout(out, live("a"));
  assert.deepStrictEqual(plan.cells[0].tabs, want);
  assert.strictEqual(plan.cells[0].active, 1);
});

test("reconcile drops a duplicate box viewer and ignores a box field that is not a string", () => {
  const p = "/tmp/playwright-mcp/a.png";
  const plan = reconcileLayout(snap([{ tabs: [
    { path: p, box: "s1", scroll: 1 },
    { path: p, box: "s1", scroll: 2 },
    { path: "/x", box: 42, scroll: 0 },
  ], active: 0 }]), live("a"));
  assert.deepStrictEqual(plan.cells[0].tabs, [
    { path: p, box: "s1", scroll: 1 },
    { path: "/x", scroll: 0 },
  ]);
});
