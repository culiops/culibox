// internal/web/jstest/docktabs.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const D = require("../static/docktabs.js");
const { tabKey: layoutTabKey, reconcileLayout } = require("../static/layout.js");

const EMPTY = { tabs: [], active: null };

// freeze: a deep Object.freeze, so a function that writes to its input throws in strict mode.
function freeze(o) {
  if (o && typeof o === "object") {
    Object.values(o).forEach(freeze);
    Object.freeze(o);
  }
  return o;
}
const keys = (st) => st.tabs.map((t) => t.key);
const previews = (st) => st.tabs.filter((t) => t.preview).map((t) => t.path);
function open(st, path, preview, box) {
  return D.dockOpen(st, { path, box, preview });
}
// pinned builds a state of pinned tabs for the paths given, the first active.
function pinned(...paths) {
  let st = EMPTY;
  for (const p of paths) st = open(st, p, false);
  return D.dockActivate(st, D.dockTabKey(paths[0]));
}

test("the constants are the plan values", () => {
  assert.strictEqual(D.DOCK_TABS_KEY, "agent-box:dockTabs");
  assert.strictEqual(D.DOCK_MAX_TABS, 20);
});

test("tabKey distinguishes box and host for one path (dockTabKey)", () => {
  const p = "/home/u/proj/a b/é.go";
  assert.strictEqual(D.dockTabKey(p), "v:" + p);
  assert.strictEqual(D.dockTabKey(p, "s1"), "b:s1:" + p);
  assert.notStrictEqual(D.dockTabKey(p, "s1"), D.dockTabKey(p, "s2"));
  assert.notStrictEqual(D.dockTabKey(p, "s1"), D.dockTabKey(p));
  // A box that is not a non-empty string is no box, as in layout.js: the key degrades to a host file.
  assert.strictEqual(D.dockTabKey(p, ""), "v:" + p);
  assert.strictEqual(D.dockTabKey(p, null), "v:" + p);
  assert.strictEqual(D.dockTabKey(p, 42), "v:" + p);
  // No usable path, no key.
  assert.strictEqual(D.dockTabKey(""), null);
  assert.strictEqual(D.dockTabKey(null), null);
  assert.strictEqual(D.dockTabKey(42, "s1"), null);
});

test("dockTabKey is the same format as layout.js tabKey", () => {
  for (const [path, box] of [["/a", undefined], ["/a", "s1"], ["/x y/z", "s2"], ["/é", ""]]) {
    assert.strictEqual(D.dockTabKey(path, box), layoutTabKey({ path, box }));
  }
});

test("dockOpen into an empty dock makes the one tab active", () => {
  const st = open(EMPTY, "/a", true);
  assert.deepEqual(st, { tabs: [{ key: "v:/a", path: "/a", box: null, scroll: 0, preview: true }], active: "v:/a" });
  const b = open(EMPTY, "/a", false, "s1");
  assert.deepEqual(b.tabs[0], { key: "b:s1:/a", path: "/a", box: "s1", scroll: 0, preview: false });
  assert.deepEqual(open(null, "/a", false).tabs.map((t) => t.path), ["/a"]);
});

test("preview replaces preview, pinned stays", () => {
  let st = open(EMPTY, "/a", false);
  st = open(st, "/b", true); // [a, b*]
  st = open(st, "/z", false); // a new pinned tab goes after the active one: [a, b*, z]
  assert.deepEqual(st.tabs.map((t) => t.path), ["/a", "/b", "/z"]);
  st = D.dockActivate(st, "v:/a");
  const next = open(st, "/c", true);
  // c took b's slot, in place: same index, the pinned tabs around it did not move.
  assert.deepEqual(next.tabs.map((t) => t.path), ["/a", "/c", "/z"]);
  assert.deepEqual(previews(next), ["/c"]);
  assert.strictEqual(next.active, "v:/c");
  // Opening yet another preview keeps replacing the same slot.
  const again = open(next, "/d", true);
  assert.deepEqual(again.tabs.map((t) => t.path), ["/a", "/d", "/z"]);
});

test("a preview with no preview tab to replace opens after the active tab", () => {
  let st = pinned("/a", "/b", "/c"); // a active
  st = open(st, "/p", true);
  assert.deepEqual(st.tabs.map((t) => t.path), ["/a", "/p", "/b", "/c"]);
  assert.deepEqual(previews(st), ["/p"]);
});

test("a new pinned tab is inserted after the active tab, and a preview stays", () => {
  let st = open(pinned("/a", "/b", "/c"), "/p", true); // [a, p*, b, c], p active
  st = D.dockActivate(st, "v:/b");
  st = open(st, "/d", false);
  assert.deepEqual(st.tabs.map((t) => t.path), ["/a", "/p", "/b", "/d", "/c"]);
  assert.deepEqual(previews(st), ["/p"]);
  assert.strictEqual(st.active, "v:/d");
});

test("open existing key activates and pins when preview false", () => {
  let st = open(pinned("/a"), "/b", true); // [a, b*], b active
  st = D.dockActivate(st, "v:/a");
  const viaPreview = open(st, "/b", true);
  assert.strictEqual(viaPreview.active, "v:/b");
  assert.deepEqual(previews(viaPreview), ["/b"], "a preview open of a preview leaves it a preview");
  const pinnedNow = open(st, "/b", false);
  assert.strictEqual(pinnedNow.active, "v:/b");
  assert.deepEqual(previews(pinnedNow), [], "a pinned open pins it");
  assert.deepEqual(pinnedNow.tabs.map((t) => t.path), ["/a", "/b"], "in place, no duplicate");
  // A preview open of an already pinned tab never unpins it.
  const stay = open(pinnedNow, "/a", true);
  assert.deepEqual(previews(stay), []);
  assert.strictEqual(stay.active, "v:/a");
});

test("the same path in a box and on the host is two tabs", () => {
  let st = open(EMPTY, "/a", false);
  st = open(st, "/a", false, "s1");
  assert.deepEqual(keys(st), ["v:/a", "b:s1:/a"]);
  // A preview of the box file does not replace the host file's pinned tab.
  st = open(st, "/a", true, "s2");
  assert.deepEqual(keys(st), ["v:/a", "b:s1:/a", "b:s2:/a"]);
  assert.strictEqual(st.active, "b:s2:/a");
});

test("dockOpen with no usable path changes nothing", () => {
  const st = pinned("/a", "/b");
  assert.deepEqual(D.dockOpen(st, { path: "", preview: false }), st);
  assert.deepEqual(D.dockOpen(st, { path: 7 }), st);
  assert.deepEqual(D.dockOpen(st, null), st);
});

test("close activates right then left", () => {
  const st = pinned("/a", "/b", "/c", "/d");
  const onB = D.dockActivate(st, "v:/b");
  // The right neighbour first.
  assert.strictEqual(D.dockClose(onB, "v:/b").active, "v:/c");
  assert.deepEqual(keys(D.dockClose(onB, "v:/b")), ["v:/a", "v:/c", "v:/d"]);
  // The last tab has no right neighbour: the left one.
  const onD = D.dockActivate(st, "v:/d");
  assert.strictEqual(D.dockClose(onD, "v:/d").active, "v:/c");
  // The first tab: the one that was to its right.
  assert.strictEqual(D.dockClose(st, "v:/a").active, "v:/b");
  // Closing a tab that is not active leaves the active one alone.
  assert.strictEqual(D.dockClose(onB, "v:/d").active, "v:/b");
  // The only tab: nothing is active.
  assert.deepEqual(D.dockClose(pinned("/a"), "v:/a"), EMPTY);
  // An unknown key changes nothing.
  assert.deepEqual(D.dockClose(onB, "v:/nope"), onB);
});

test("dockPin, dockActivate and dockSetScroll each return a new state", () => {
  const st = freeze(open(pinned("/a"), "/b", true));
  const pin = D.dockPin(st, "v:/b");
  assert.deepEqual(previews(pin), []);
  assert.deepEqual(previews(st), ["/b"], "the input is untouched");
  assert.deepEqual(D.dockPin(st, "v:/nope"), st);

  const act = D.dockActivate(st, "v:/a");
  assert.strictEqual(act.active, "v:/a");
  assert.strictEqual(st.active, "v:/b");
  assert.strictEqual(D.dockActivate(st, "v:/nope").active, "v:/b", "an unknown key keeps the active tab");

  const sc = D.dockSetScroll(st, "v:/a", 120.4);
  assert.strictEqual(sc.tabs[0].scroll, 120);
  assert.strictEqual(st.tabs[0].scroll, 0);
  assert.strictEqual(D.dockSetScroll(st, "v:/a", -5).tabs[0].scroll, 0);
  assert.strictEqual(D.dockSetScroll(st, "v:/a", NaN).tabs[0].scroll, 0);
  assert.deepEqual(D.dockSetScroll(st, "v:/nope", 9), st);
});

test("the tab functions never change their input", () => {
  const st = freeze(open(pinned("/a", "/b"), "/p", true));
  // Each of these would throw on a frozen input if it wrote to it.
  D.dockOpen(st, { path: "/c", preview: true });
  D.dockOpen(st, { path: "/c", preview: false });
  D.dockOpen(st, { path: "/a", preview: false });
  D.dockClose(st, st.active);
  D.dockClose(st, "v:/a");
  D.dockPin(st, "v:/p");
  D.dockActivate(st, "v:/a");
  D.dockSetScroll(st, "v:/a", 5);
  D.dockLeaveNeedsAsk(st, () => false);
  // And what comes back shares no tab object with it.
  const out = D.dockPin(st, "v:/p");
  out.tabs.forEach((t, i) => assert.notStrictEqual(t, st.tabs[i]));
});

test("dockLeaveNeedsAsk true when any tab dirty", () => {
  const st = pinned("/a", "/b", "/c");
  const dirty = new Set(["v:/b"]);
  assert.strictEqual(D.dockLeaveNeedsAsk(st, (k) => dirty.has(k)), true, "a dirty tab that is not the active one still asks");
  assert.strictEqual(D.dockLeaveNeedsAsk(st, () => false), false);
  assert.strictEqual(D.dockLeaveNeedsAsk(EMPTY, () => true), false, "no tabs, nothing to lose");
  assert.strictEqual(D.dockLeaveNeedsAsk(null, () => true), false);
  assert.strictEqual(D.dockLeaveNeedsAsk(st, null), false);
  // isDirty is asked with the tab's key.
  const seen = [];
  D.dockLeaveNeedsAsk(st, (k) => { seen.push(k); return false; });
  assert.deepEqual(seen, ["v:/a", "v:/b", "v:/c"]);
});

test("dockFollow: an unpinned dock follows focus", () => {
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: false }, "B", new Set(["A", "B"])), { sid: "B", pinned: false });
});

test("dockFollow: a pinned live session keeps the dock, however focus moves", () => {
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, "B", new Set(["A", "B"])), { sid: "A", pinned: true });
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, null, ["A"]), { sid: "A", pinned: true }, "liveSids may be an array");
});

test("dockFollow unpins a dead session", () => {
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, "B", new Set(["B"])), { sid: "B", pinned: false });
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, "B", []), { sid: "B", pinned: false });
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, "B", null), { sid: "B", pinned: false });
  // Nothing in focus either: the pin is released and the sid stays for the caller to show or close.
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: true }, null, new Set()), { sid: "A", pinned: false });
});

test("dockFollow keeps sid when focus has no session", () => {
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: false }, null, new Set(["A"])), { sid: "A", pinned: false });
  assert.deepEqual(D.dockFollow({ sid: "A", pinned: false }, undefined, new Set(["A"])), { sid: "A", pinned: false });
  assert.deepEqual(D.dockFollow({ sid: null, pinned: false }, null, new Set()), { sid: null, pinned: false });
});

test("dockFollow returns a new object and leaves its input alone", () => {
  const cur = freeze({ sid: "A", pinned: true });
  const out = D.dockFollow(cur, "B", new Set(["A"]));
  assert.notStrictEqual(out, cur);
  assert.deepEqual(cur, { sid: "A", pinned: true });
});

test("serializeDock has the stored shape", () => {
  const st = open(pinned("/a"), "/b", true);
  const map = new Map([["s1", st]]);
  const out = JSON.parse(D.serializeDock(map, true));
  assert.deepEqual(out, {
    v: 1,
    open: true,
    sessions: {
      s1: {
        tabs: [
          { key: "v:/a", path: "/a", box: null, scroll: 0, preview: false },
          { key: "v:/b", path: "/b", box: null, scroll: 0, preview: true },
        ],
        active: "v:/b",
      },
    },
  });
  assert.strictEqual(JSON.parse(D.serializeDock(map, false)).open, false);
  assert.strictEqual(JSON.parse(D.serializeDock(new Map(), true)).open, true);
  assert.deepEqual(JSON.parse(D.serializeDock(new Map(), false)).sessions, {});
});

test("serializeDock caps tabs keeps active", () => {
  const paths = Array.from({ length: 27 }, (_, i) => "/f" + i);
  const all = pinned(...paths);
  // The active tab is inside the first 20: the first 20 are kept, in order.
  const first = JSON.parse(D.serializeDock(new Map([["s", D.dockActivate(all, "v:/f3")]]), true)).sessions.s;
  assert.strictEqual(first.tabs.length, D.DOCK_MAX_TABS);
  assert.deepEqual(first.tabs.map((t) => t.path), paths.slice(0, 20));
  assert.strictEqual(first.active, "v:/f3");
  // The active tab is past the cap: it is kept, and the order of the rest is not disturbed.
  const late = JSON.parse(D.serializeDock(new Map([["s", D.dockActivate(all, "v:/f25")]]), true)).sessions.s;
  assert.strictEqual(late.tabs.length, D.DOCK_MAX_TABS);
  assert.deepEqual(late.tabs.map((t) => t.path), paths.slice(0, 19).concat(["/f25"]));
  assert.strictEqual(late.active, "v:/f25");
  // At the cap exactly nothing is dropped.
  const exact = JSON.parse(D.serializeDock(new Map([["s", pinned(...paths.slice(0, 20))]]), true)).sessions.s;
  assert.strictEqual(exact.tabs.length, 20);
});

test("serializeDock leaves out a session with no tabs and a bad sid, and does not change its input", () => {
  const st = freeze(pinned("/a"));
  const map = new Map([["s1", st], ["empty", EMPTY], ["", st], [null, st], ["bad", null]]);
  const out = JSON.parse(D.serializeDock(map, false));
  assert.deepEqual(Object.keys(out.sessions), ["s1"]);
  assert.strictEqual(map.size, 5);
});

test("serializeDock keeps a session whose id is __proto__", () => {
  const out = JSON.parse(D.serializeDock(new Map([["__proto__", pinned("/a")]]), true));
  assert.deepEqual(Object.keys(out.sessions), ["__proto__"]);
});

test("serializeDock and parseDock round trip", () => {
  let a = open(pinned("/a", "/b"), "/p", true);
  a = D.dockSetScroll(a, "v:/a", 310);
  const b = open(EMPTY, "/etc/hosts", false, "s2");
  const map = new Map([["s1", a], ["s2", b]]);
  const back = D.parseDock(D.serializeDock(map, true), new Set(["s1", "s2"]));
  assert.strictEqual(back.open, true);
  assert.deepEqual([...back.map], [...map]);
});

test("parseDock drops dead sessions and junk", () => {
  const good = { tabs: [{ key: "v:/a", path: "/a", box: null, scroll: 4, preview: false }], active: "v:/a" };
  const raw = JSON.stringify({ v: 1, open: true, sessions: { live: good, dead: good } });
  const out = D.parseDock(raw, new Set(["live"]));
  assert.strictEqual(out.open, true);
  assert.deepEqual([...out.map.keys()], ["live"]);
  assert.deepEqual(out.map.get("live"), good);
  assert.strictEqual(D.parseDock(raw, ["live"]).map.size, 1, "liveSids may be an array");
  assert.strictEqual(D.parseDock(raw, new Set()).map.size, 0);
  assert.strictEqual(D.parseDock(raw, null).map.size, 0);

  // Whole-snapshot junk gives the empty result, every time a fresh Map.
  for (const junk of [null, undefined, "", "not json", "[]", "7", "null", '"s"', '{"v":2,"open":true,"sessions":{}}',
    '{"open":true,"sessions":{}}', '{"v":1,"sessions":[]}', '{"v":1,"sessions":7}', {}, 5]) {
    const r = D.parseDock(junk, new Set(["live"]));
    assert.strictEqual(r.map.size, 0, String(junk));
    assert.strictEqual(r.open, false, String(junk));
  }
  assert.notStrictEqual(D.parseDock(null, []).map, D.parseDock(null, []).map);
  // open is true only when stored as true.
  assert.strictEqual(D.parseDock('{"v":1,"open":"yes","sessions":{}}', []).open, false);
  assert.strictEqual(D.parseDock('{"v":1,"open":true,"sessions":{}}', []).open, true);
});

test("parseDock drops bad tabs and repairs the rest", () => {
  const st = (tabs, active) => ({ tabs, active });
  const tab = (o) => Object.assign({ key: "x", path: "/a", box: null, scroll: 0, preview: false }, o);
  const raw = JSON.stringify({
    v: 1,
    open: false,
    sessions: {
      mixed: st([
        null, 7, "x", [],
        tab({ path: "" }), tab({ path: 42 }), tab({ path: undefined }),
        tab({ path: "/ok", key: "forged-key", scroll: -9, preview: "yes" }),
        tab({ path: "/ok", key: "v:/ok" }), // the same file again: the first one wins
        tab({ path: "/boxed", box: "s1", scroll: 7.6 }),
        tab({ path: "/num-box", box: 5 }),
      ], "b:s1:/boxed"),
      nothing: st([null, tab({ path: "" })], null),
      notabs: st([], null),
      notarray: { tabs: "v:/a", active: "v:/a" },
      notobject: 5,
      lostactive: st([tab({ path: "/a" }), tab({ path: "/b" })], "v:/gone"),
    },
  });
  const out = D.parseDock(raw, new Set(["mixed", "nothing", "notabs", "notarray", "notobject", "lostactive"])).map;
  assert.deepEqual([...out.keys()].sort(), ["lostactive", "mixed"], "a session left with no tab is dropped");
  assert.deepEqual(out.get("mixed"), {
    tabs: [
      { key: "v:/ok", path: "/ok", box: null, scroll: 0, preview: false },
      { key: "b:s1:/boxed", path: "/boxed", box: "s1", scroll: 8, preview: false },
      { key: "v:/num-box", path: "/num-box", box: null, scroll: 0, preview: false },
    ],
    active: "b:s1:/boxed",
  });
  // A stored active tab that is not in the list falls back to the first.
  assert.strictEqual(out.get("lostactive").active, "v:/a");
});

test("parseDock caps a hand-edited session at DOCK_MAX_TABS and keeps its active tab", () => {
  const tabs = Array.from({ length: 30 }, (_, i) => ({ key: "v:/f" + i, path: "/f" + i, box: null, scroll: 0, preview: false }));
  const raw = JSON.stringify({ v: 1, open: true, sessions: { s: { tabs, active: "v:/f28" } } });
  const st = D.parseDock(raw, ["s"]).map.get("s");
  assert.strictEqual(st.tabs.length, D.DOCK_MAX_TABS);
  assert.strictEqual(st.active, "v:/f28");
  assert.ok(st.tabs.some((t) => t.key === "v:/f28"));
});

test("parseDock does not turn a __proto__ session id into the Map's prototype", () => {
  const raw = '{"v":1,"open":true,"sessions":{"__proto__":{"tabs":[{"path":"/a"}],"active":null}}}';
  const out = D.parseDock(raw, ["__proto__"]);
  assert.deepEqual([...out.map.keys()], ["__proto__"]);
  assert.strictEqual(out.map.get("__proto__").tabs[0].path, "/a");
});

const cell = (...tabs) => ({ tabs, active: 0 });

test("dockFromLayout moves viewer tabs to the cell session", () => {
  const layout = freeze({
    v: 1,
    cap: 2,
    focused: 1,
    maximized: null,
    cells: [
      { tabs: [{ id: "s1" }, { path: "/a.go", scroll: 120 }, { path: "/etc/b", box: "s1", scroll: 5.2 }], active: 1 },
      { tabs: [{ path: "/files-only", scroll: 3 }], active: 0 }, // a pane of files: no session
      { tabs: [{ id: "s2" }, { path: "/c", scroll: 0 }], active: 0, dock: { ids: ["x"], active: "x" } },
    ],
  });
  const sids = ["s1", null, "s2"];
  const seenCells = [];
  const out = D.dockFromLayout(layout, (i) => { seenCells.push(i); return sids[i]; });
  assert.deepEqual([...out.map.keys()].sort(), ["s1", "s2"]);
  assert.deepEqual(out.map.get("s1"), {
    tabs: [
      { key: "v:/a.go", path: "/a.go", box: null, scroll: 120, preview: false },
      { key: "b:s1:/etc/b", path: "/etc/b", box: "s1", scroll: 5, preview: false },
    ],
    active: "v:/a.go", // the pane had it active
  });
  assert.deepEqual(out.map.get("s2"), { tabs: [{ key: "v:/c", path: "/c", box: null, scroll: 0, preview: false }], active: "v:/c" });
  // The viewers are gone from the layout; the cell with nothing left (no session to take its file) is gone too.
  assert.deepEqual(out.layout.cells, [
    { tabs: [{ id: "s1" }], active: 0 },
    { tabs: [{ id: "s2" }], active: 0, dock: { ids: ["x"], active: "x" } },
  ]);
  // Everything else on the layout is kept, and the focused cell follows its cell to its new index.
  assert.strictEqual(out.layout.v, 1);
  assert.strictEqual(out.layout.cap, 2);
  assert.strictEqual(out.layout.focused, 0, "cell 1 was removed: focus falls to the first cell, as reconcileLayout does");
  assert.strictEqual(out.layout.maximized, null);
  assert.deepEqual(seenCells, [0, 1, 2], "cellSid is asked per cell index of the layout it was given");
});

test("dockFromLayout remaps focused and maximized when a cell goes", () => {
  const mk = (focused, maximized) => ({
    focused, maximized,
    cells: [cell({ id: "s1" }), cell({ path: "/f" }), cell({ id: "s3" }), cell({ id: "s4" })],
  });
  const sidOf = (i) => (i === 1 ? null : "s" + (i + 1));
  const a = D.dockFromLayout(mk(2, 3), sidOf).layout;
  assert.strictEqual(a.focused, 1);
  assert.strictEqual(a.maximized, 2);
  const b = D.dockFromLayout(mk(0, 1), sidOf).layout;
  assert.strictEqual(b.focused, 0);
  assert.strictEqual(b.maximized, null, "the maximized cell was the one removed");
  // Nothing removed (cell 1 keeps a terminal of its own this time): the indexes are not touched.
  const keep = { focused: 3, maximized: 2, cells: [cell({ id: "s1" }), cell({ id: "s2" }, { path: "/f" }), cell({ id: "s3" }), cell({ id: "s4" })] };
  const c = D.dockFromLayout(keep, () => "s").layout;
  assert.strictEqual(c.focused, 3);
  assert.strictEqual(c.maximized, 2);
});

test("dockFromLayout follows the active tab of a cell by identity", () => {
  const layout = { cells: [{ tabs: [{ path: "/a" }, { id: "s1" }, { path: "/b" }, { id: "s2" }], active: 3 }] };
  const out = D.dockFromLayout(layout, () => "s1");
  assert.deepEqual(out.layout.cells[0], { tabs: [{ id: "s1" }, { id: "s2" }], active: 1 });
  // The active tab was a viewer: the cell falls back to its first remaining tab, and the dock tab is active.
  const viewerActive = D.dockFromLayout({ cells: [{ tabs: [{ id: "s1" }, { path: "/a" }, { path: "/b" }], active: 2 }] }, () => "s1");
  assert.strictEqual(viewerActive.layout.cells[0].active, 0);
  assert.strictEqual(viewerActive.map.get("s1").active, "v:/b");
  // The active tab was a terminal: the dock opens on its first moved tab.
  const termActive = D.dockFromLayout({ cells: [{ tabs: [{ id: "s1" }, { path: "/a" }, { path: "/b" }], active: 0 }] }, () => "s1");
  assert.strictEqual(termActive.map.get("s1").active, "v:/a");
});

test("dockFromLayout merges cells of one session and drops a repeated file", () => {
  const layout = {
    cells: [
      { tabs: [{ id: "s1" }, { path: "/a", scroll: 1 }, { path: "/b" }], active: 0 },
      { tabs: [{ id: "s2" }, { path: "/a", scroll: 9 }, { path: "/c" }], active: 0 },
    ],
  };
  const out = D.dockFromLayout(layout, () => "s1");
  assert.deepEqual(out.map.get("s1").tabs.map((t) => t.key), ["v:/a", "v:/b", "v:/c"]);
  assert.strictEqual(out.map.get("s1").tabs[0].scroll, 1, "the first copy wins");
  assert.strictEqual(out.map.get("s1").active, "v:/a");
});

test("dockFromLayout leaves a layout with no viewer tabs as it is", () => {
  const layout = freeze({ v: 1, cap: 4, focused: 1, maximized: 0, cells: [cell({ id: "s1" }), cell({ id: "s2" }, { id: "s3" })] });
  const out = D.dockFromLayout(layout, () => "s1");
  assert.strictEqual(out.map.size, 0);
  assert.deepEqual(out.layout, layout);
});

test("dockFromLayout keeps a tab it cannot call a viewer, and a cell it did not empty", () => {
  // The viewer test is layout.js's: a non-empty string path. {id, path: 42} is a terminal there.
  const odd = { id: "s1", path: 42 };
  const junkCell = { tabs: "nope" };
  const layout = { cells: [{ tabs: [odd, { path: "" }, null, { path: "/a" }], active: 0 }, junkCell, null] };
  const out = D.dockFromLayout(layout, () => "s1");
  assert.deepEqual(out.layout.cells, [{ tabs: [odd, { path: "" }, null], active: 0 }, junkCell, null]);
  assert.deepEqual(out.map.get("s1").tabs.map((t) => t.path), ["/a"]);
});

test("dockFromLayout drops the viewers of a cell without a session", () => {
  const layout = { cells: [{ tabs: [{ id: "s1" }, { path: "/lost" }], active: 1 }] };
  for (const cellSid of [() => null, () => undefined, () => "", null]) {
    const out = D.dockFromLayout(layout, cellSid);
    assert.strictEqual(out.map.size, 0);
    assert.deepEqual(out.layout.cells, [{ tabs: [{ id: "s1" }], active: 0 }]);
  }
});

test("dockFromLayout takes no layout at all", () => {
  for (const bad of [null, undefined, 7, {}, { cells: "x" }]) {
    const out = D.dockFromLayout(bad, () => "s1");
    assert.strictEqual(out.map.size, 0);
    assert.strictEqual(out.layout, bad);
  }
});

test("dockFromLayout never changes its input, and shares no tab with it", () => {
  const layout = freeze({ cap: 2, focused: 0, cells: [{ tabs: [{ id: "s1" }, { path: "/a", scroll: 4 }], active: 1 }] });
  const out = D.dockFromLayout(layout, () => "s1");
  assert.notStrictEqual(out.layout, layout);
  assert.notStrictEqual(out.layout.cells[0], layout.cells[0]);
  assert.notStrictEqual(out.layout.cells[0].tabs[0], layout.cells[0].tabs[0]);
  assert.deepEqual(layout.cells[0].tabs, [{ id: "s1" }, { path: "/a", scroll: 4 }]);
});

test("a migrated layout still reconciles: the plan has the terminals and no viewers", () => {
  const saved = {
    v: 1, cap: 2, focused: 2, maximized: null,
    cells: [
      { tabs: [{ path: "/files-only" }], active: 0 },
      { tabs: [{ id: "s1" }, { path: "/a", scroll: 40 }], active: 1 },
      { tabs: [{ id: "s2" }], active: 0 },
    ],
  };
  const moved = D.dockFromLayout(saved, (i) => [null, "s1", "s2"][i]);
  const plan = reconcileLayout(moved.layout, [{ id: "s1" }, { id: "s2" }]);
  assert.deepEqual(plan.cells, [{ tabs: [{ id: "s1" }], active: 0 }, { tabs: [{ id: "s2" }], active: 0 }]);
  assert.strictEqual(plan.focused, 1, "the focused cell is still the one holding s2");
  assert.strictEqual(moved.map.get("s1").tabs[0].scroll, 40);
});
