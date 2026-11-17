// internal/web/jstest/changesmodel.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { CHANGES_MAX, changesRows } = require("../static/changesmodel.js");

// freeze: a deep Object.freeze, so a function that writes to its input throws in strict mode.
function freeze(o) {
  if (o && typeof o === "object") {
    Object.values(o).forEach(freeze);
    Object.freeze(o);
  }
  return o;
}

const status = (entries, extra) => Object.assign({ state: "ok", top: "/real/r", rootRel: ".", entries }, extra || {});
const numstat = (entries, extra) => Object.assign({ state: "ok", rootRel: ".", entries }, extra || {});
const rows = (o) => changesRows(Object.assign({ scope: "head", root: "/r", files: [] }, o));
const paths = (r) => r.rows.map((x) => x.path);

test("the cap is the plan value", () => {
  assert.equal(CHANGES_MAX, 200);
});

test("changesRows branch scope uses numstat letters", () => {
  const r = rows({
    scope: "branch",
    // Status is against HEAD, so its M for a.go and its tracked-only entry say nothing about the branch.
    status: status([{ p: "a.go", x: "M" }, { p: "only-in-status.go", x: "M" }, { p: "new.txt", x: "U" }]),
    numstat: numstat([{ p: "a.go", x: "A", a: 10, d: 0 }, { p: "b/c.go", x: "M", a: 3, d: 1 }]),
  });
  assert.deepStrictEqual(r, {
    rows: [
      { path: "/r/a.go", name: "a.go", dir: "", code: "A", add: 10, del: 0, mine: false, folder: false },
      { path: "/r/b/c.go", name: "c.go", dir: "b", code: "M", add: 3, del: 1, mine: false, folder: false },
      // The untracked file numstat could not count is still listed, with no counts.
      { path: "/r/new.txt", name: "new.txt", dir: "", code: "U", add: null, del: null, mine: false, folder: false },
    ],
    more: 0,
  });
});

test("changesRows branch scope lists untracked files when numstat is unavailable", () => {
  const r = rows({
    scope: "branch",
    status: status([{ p: "a.go", x: "M" }, { p: "new.txt", x: "U" }]),
    numstat: { state: "unavailable", reason: "no base branch", rootRel: "", entries: [] },
  });
  assert.deepStrictEqual(paths(r), ["/r/new.txt"]);
});

test("changesRows head scope keeps status entries numstat lacks, with no counts", () => {
  const entries = [{ p: "a.go", x: "M" }, { p: "gone.go", x: "D" }, { p: "staged.go", x: "A" }, { p: "new.txt", x: "U" }];
  for (const ns of [null, undefined, { state: "unavailable", reason: "x", rootRel: "", entries: [] }]) {
    const r = rows({ status: status(entries), numstat: ns });
    assert.deepStrictEqual(r.rows.map((x) => [x.path, x.code, x.add, x.del]), [
      ["/r/a.go", "M", null, null],
      ["/r/gone.go", "D", null, null],
      ["/r/new.txt", "U", null, null],
      ["/r/staged.go", "A", null, null],
    ]);
  }
});

test("changesRows head scope takes counts and letters from numstat and one row per path", () => {
  const r = rows({
    status: status([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]),
    numstat: numstat([{ p: "a.go", x: "M", a: 4, d: 2 }, { p: "n.txt", x: "U", a: 7, d: 0 }]),
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.path, x.code, x.add, x.del]), [
    ["/r/a.go", "M", 4, 2],
    ["/r/b.go", "M", null, null], // numstat has not caught up with this one
    ["/r/n.txt", "U", 7, 0],
  ]);
});

test("changesRows head scope shows a conflict as C even where numstat says M", () => {
  // git diff reports an unmerged file as a modification; the tree badge says C, and so must this row.
  const r = rows({
    status: status([{ p: "a.go", x: "C" }]),
    numstat: numstat([{ p: "a.go", x: "M", a: 5, d: 5 }]),
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.code, x.add, x.del]), [["C", 5, 5]]);
});

test("changesRows mine first then by path", () => {
  const r = rows({
    status: status([]),
    numstat: numstat([
      { p: "a.go", x: "M", a: 1, d: 0 },
      { p: "b/x.go", x: "M", a: 1, d: 0 },
      { p: "m.go", x: "M", a: 1, d: 0 },
      { p: "z.go", x: "M", a: 1, d: 0 },
    ]),
    // Most recent edit first, and not alphabetical.
    files: [{ path: "/r/z.go", add: 1, del: 0, ts: 3 }, { path: "/elsewhere/q.go" }, { path: "/r/m.go", add: 2, del: 2, ts: 9 }],
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.path, x.mine]), [
    ["/r/z.go", true],
    ["/r/m.go", true],
    ["/r/a.go", false],
    ["/r/b/x.go", false],
  ]);
});

test("changesRows mine matches a path the way the tree does, and ignores junk entries", () => {
  const r = rows({
    status: status([]),
    numstat: numstat([{ p: "a.go", x: "M", a: 1, d: 0 }, { p: "b.go", x: "M", a: 1, d: 0 }]),
    files: [null, {}, { path: 5 }, { path: "relative/b.go" }, { path: "/r//./a.go" }],
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.path, x.mine]), [["/r/a.go", true], ["/r/b.go", false]]);
});

test("counts only from numstat", () => {
  const r = rows({
    status: status([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "bin.png", x: "M" }]),
    numstat: numstat([{ p: "b.go", x: "M", a: 2, d: 1 }, { p: "bin.png", x: "M", a: 0, d: 0, bin: true }]),
    // Activity counts edit strings, not the diff: never shown.
    files: [{ path: "/r/a.go", add: 99, del: 98 }, { path: "/r/b.go", add: 97, del: 96 }, { path: "/r/bin.png", add: 95, del: 94 }],
  });
  const by = Object.fromEntries(r.rows.map((x) => [x.name, [x.add, x.del]]));
  assert.deepStrictEqual(by, {
    "a.go": [null, null], // numstat lacks it: unknown, not Activity's 99
    "b.go": [2, 1],
    "bin.png": [null, null], // binary: unknown, never 0/0
  });
});

test("changesRows reads a count that is not a whole non-negative number as unknown, for the pair", () => {
  const r = rows({
    status: status([]),
    numstat: numstat([
      { p: "a.go", x: "M", a: "5", d: 1 },
      { p: "b.go", x: "M", a: -1, d: 1 },
      { p: "c.go", x: "M", a: 1.5, d: 1 },
      { p: "d.go", x: "M", a: 0, d: 0 },
    ]),
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.add, x.del]), [[null, null], [null, null], [null, null], [0, 0]]);
});

test("deleted and untracked folder rows", () => {
  const r = rows({
    status: status([{ p: "tmp/out/", x: "U" }, { p: "build/", x: "U" }, { p: "old.go", x: "D" }]),
    numstat: numstat([{ p: "old.go", x: "D", a: 0, d: 12 }]),
  });
  assert.deepStrictEqual(r.rows, [
    { path: "/r/build", name: "build", dir: "", code: "U", add: null, del: null, mine: false, folder: true },
    { path: "/r/old.go", name: "old.go", dir: "", code: "D", add: 0, del: 12, mine: false, folder: false },
    { path: "/r/tmp/out", name: "out", dir: "tmp", code: "U", add: null, del: null, mine: false, folder: true },
  ]);
});

test("non-ok status gives no rows", () => {
  const ns = numstat([{ p: "a.go", x: "M", a: 1, d: 0 }]);
  for (const s of [
    { state: "notrepo", rootRel: "", entries: [] },
    { state: "toomany", rootRel: ".", entries: [{ p: "a.go", x: "M" }] },
    { state: "unavailable", reason: "x", rootRel: "", entries: [] },
    null,
    undefined,
  ]) {
    for (const scope of ["head", "branch"]) {
      assert.deepStrictEqual(rows({ scope, status: s, numstat: ns }), { rows: [], more: 0 });
    }
  }
});

test("changesRows with no root or no arguments gives no rows", () => {
  assert.deepStrictEqual(rows({ status: status([{ p: "a.go", x: "M" }]), root: "" }), { rows: [], more: 0 });
  assert.deepStrictEqual(changesRows(), { rows: [], more: 0 });
  assert.deepStrictEqual(changesRows({}), { rows: [], more: 0 });
});

test("changesRows non-ok numstat contributes nothing", () => {
  for (const state of ["notrepo", "toomany", "unavailable"]) {
    const r = rows({
      scope: "branch",
      status: status([]),
      numstat: { state, rootRel: ".", entries: [{ p: "a.go", x: "M", a: 1, d: 1 }] },
    });
    assert.deepStrictEqual(r, { rows: [], more: 0 });
  }
});

test("changesRows caps at 200", () => {
  const entries = [];
  for (let i = 0; i < 5000; i++) entries.push({ p: "d/f" + String(i).padStart(4, "0") + ".go", x: "M", a: 1, d: 1 });
  const r = rows({ status: status([]), numstat: numstat(entries) });
  assert.equal(r.rows.length, 200);
  assert.equal(r.more, 4800);
  assert.equal(r.rows[0].path, "/r/d/f0000.go");
  assert.equal(r.rows[199].path, "/r/d/f0199.go");
});

test("changesRows caps after ordering, so a row of this session is never cut", () => {
  const entries = [];
  for (let i = 0; i < 300; i++) entries.push({ p: "f" + String(i).padStart(3, "0") + ".go", x: "M", a: 1, d: 1 });
  const r = rows({ status: status([]), numstat: numstat(entries), files: [{ path: "/r/f299.go" }] });
  assert.equal(r.rows.length, 200);
  assert.equal(r.more, 100);
  assert.equal(r.rows[0].path, "/r/f299.go");
  assert.equal(r.rows[0].mine, true);
});

test("changesRows exactly at the cap has no more", () => {
  const entries = [];
  for (let i = 0; i < 200; i++) entries.push({ p: "f" + i + ".go", x: "M", a: 1, d: 1 });
  const r = rows({ status: status([]), numstat: numstat(entries) });
  assert.equal(r.rows.length, 200);
  assert.equal(r.more, 0);
});

test("changesRows maps a subfolder root through rootRel, as the tree marks do", () => {
  const r = changesRows({
    scope: "head",
    root: "/home/u/r/internal/",
    files: [],
    status: status([{ p: "internal/a/b.go", x: "M" }, { p: "README.md", x: "M" }, { p: "internalx/z.go", x: "M" }, { p: "internal/", x: "U" }], { rootRel: "internal" }),
    numstat: numstat([{ p: "internal/a/b.go", x: "M", a: 2, d: 0 }, { p: "other/c.go", x: "M", a: 1, d: 1 }], { rootRel: "internal" }),
  });
  assert.deepStrictEqual(r.rows.map((x) => [x.path, x.dir, x.name, x.add]), [["/home/u/r/internal/a/b.go", "a", "b.go", 2]]);
});

test("changesRows drops a letter that is a property of Object, not a code", () => {
  const bad = ["__proto__", "constructor", "toString", "hasOwnProperty", "valueOf", "", "m"];
  for (const scope of ["head", "branch"]) {
    const r = rows({
      scope,
      status: status(bad.map((x, i) => ({ p: "s" + i + ".go", x })).concat([{ p: "dir" + "/", x: "constructor" }])),
      numstat: numstat(bad.map((x, i) => ({ p: "n" + i + ".go", x, a: 1, d: 1 }))),
    });
    assert.deepStrictEqual(r, { rows: [], more: 0 });
  }
});

test("changesRows reads every directory entry as an untracked folder, as gitMarks does", () => {
  const entries = [{ p: "a/", x: "M" }, { p: "b/", x: "D" }, { p: "c/", x: "U" }];
  for (const scope of ["head", "branch"]) {
    const r = rows({ scope, status: status(entries), numstat: numstat([]) });
    assert.deepStrictEqual(r.rows.map((x) => [x.path, x.code, x.folder, x.add, x.del]), [
      ["/r/a", "U", true, null, null],
      ["/r/b", "U", true, null, null],
      ["/r/c", "U", true, null, null],
    ]);
  }
});

test("changesRows drops entries that cannot be placed under the root", () => {
  const r = rows({
    status: status([{ p: "a//b.go", x: "M" }, { p: "ok.go", x: "M" }, { p: "weird.go", x: "Z" }, { p: "", x: "M" }, { p: 7, x: "M" }, null]),
    numstat: numstat([{ p: "/abs.go", x: "M", a: 1, d: 0 }, { p: "dir/", x: "M", a: 1, d: 0 }, { p: "q.go", x: "?", a: 1, d: 0 }]),
  });
  assert.deepStrictEqual(paths(r), ["/r/ok.go"]);
});

test("changesRows drops everything when a reply has no usable rootRel", () => {
  for (const rootRel of ["", undefined, 3]) {
    const r = rows({ status: status([{ p: "a.go", x: "M" }], { rootRel }), numstat: numstat([{ p: "b.go", x: "M", a: 1, d: 0 }], { rootRel }) });
    assert.deepStrictEqual(r, { rows: [], more: 0 });
  }
});

test("changesRows leaves its arguments as they were and accepts statusAt", () => {
  const args = freeze({
    scope: "head",
    root: "/r",
    statusAt: 1700000000000,
    files: [{ path: "/r/a.go", add: 1, del: 1, ts: 5 }],
    status: status([{ p: "a.go", x: "M" }, { p: "n/", x: "U" }]),
    numstat: numstat([{ p: "a.go", x: "M", a: 1, d: 1 }]),
  });
  const r = changesRows(args);
  assert.deepStrictEqual(r.rows.map((x) => x.path), ["/r/a.go", "/r/n"]);
});
