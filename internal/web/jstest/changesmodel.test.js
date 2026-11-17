// internal/web/jstest/changesmodel.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  CHANGES_MAX, changesRows, CHANGES_SCOPE_KEY, changesScopeParse, changesScopePut, changesScopeSave, changesScopeFor,
  changesBaseStale, changesTotals, changesView,
} = require("../static/changesmodel.js");

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

// --- Task 9b: the section's own rules (scope memory, base refetch, totals, the view) ---------------------------------------

const okBase = (extra) => Object.assign({ state: "ok", branch: "feat/x", baseRef: "refs/heads/main", baseName: "main", mergeBase: "abc", ahead: false }, extra || {});

test("the scope is remembered under the plan's key", () => {
  assert.equal(CHANGES_SCOPE_KEY, "agent-box:changesScope");
});

test("changesScopeFor: no base, no Branch; the default follows ahead; a remembered choice wins while there is a base", () => {
  const none = new Map();
  for (const base of [null, undefined, { state: "nobase", branch: "x", ahead: false }, { state: "notrepo" }, { state: "unavailable", reason: "r" }]) {
    assert.equal(changesScopeFor(none, "s", base), "head", "no usable base: Uncommitted");
    // A remembered Branch cannot hold without a base to compare with.
    assert.equal(changesScopeFor(new Map([["s", "branch"]]), "s", base), "head");
  }
  assert.equal(changesScopeFor(none, "s", okBase({ ahead: true })), "branch", "ahead: Branch");
  assert.equal(changesScopeFor(none, "s", okBase({ ahead: false })), "head", "not ahead: Uncommitted");
  assert.equal(changesScopeFor(new Map([["s", "head"]]), "s", okBase({ ahead: true })), "head", "the user's pick beats the default");
  assert.equal(changesScopeFor(new Map([["s", "branch"]]), "s", okBase({ ahead: false })), "branch");
  assert.equal(changesScopeFor(new Map([["other", "branch"]]), "s", okBase({ ahead: false })), "head", "per session");
  assert.equal(changesScopeFor(undefined, "s", okBase({ ahead: true })), "branch", "no stored map at all");
  assert.equal(changesScopeFor(new Map([["", "branch"]]), null, okBase()), "branch", "a pane of files has no session: the empty key");
  assert.equal(changesScopeFor(none, "s", okBase({ ahead: "yes" })), "head", "ahead is true only when it is true");
});

test("changesScopeParse keeps only head/branch under string keys, tolerates junk, and caps the count", () => {
  assert.deepStrictEqual([...changesScopeParse('{"a":"head","b":"branch","c":"main","d":3,"e":null}')], [["a", "head"], ["b", "branch"]]);
  for (const raw of [null, undefined, "", "{", "[]", "3", '"x"', "null", '["head"]']) assert.equal(changesScopeParse(raw).size, 0, String(raw));
  // A key that is a property of Object is just a session id here, never a prototype write.
  const m = changesScopeParse('{"__proto__":"branch","constructor":"head"}');
  assert.equal(m.get("__proto__"), "branch");
  assert.equal(m.get("constructor"), "head");
  assert.equal({}.polluted, undefined);
  const big = {};
  for (let i = 0; i < 150; i++) big["s" + i] = "head";
  const kept = changesScopeParse(JSON.stringify(big));
  assert.equal(kept.size, 100);
  assert.ok(kept.has("s149") && !kept.has("s49") && kept.has("s50"), "the newest hundred survive");
});

test("changesScopePut returns a new map with the session last, writes nothing to its input, and caps", () => {
  const a = Object.freeze(new Map([["x", "head"], ["y", "branch"]]));
  const b = changesScopePut(a, "x", "branch");
  assert.notStrictEqual(a, b);
  assert.deepStrictEqual([...b], [["y", "branch"], ["x", "branch"]], "the touched session moves to the end");
  assert.deepStrictEqual([...a], [["x", "head"], ["y", "branch"]]);
  assert.equal(changesScopePut(a, null, "head").get(""), "head", "null is the pane of files");
  assert.equal(changesScopePut(a, "x", "bogus"), a, "a value that is not a scope changes nothing");
  let m = new Map();
  for (let i = 0; i < 130; i++) m = changesScopePut(m, "s" + i, "head");
  assert.equal(m.size, 100);
  assert.ok(m.has("s129") && !m.has("s29") && m.has("s30"));
});

test("changesScopeSave round-trips through changesScopeParse", () => {
  const m = changesScopePut(changesScopePut(new Map(), "a", "branch"), "b", "head");
  const text = changesScopeSave(m);
  assert.deepStrictEqual([...changesScopeParse(text)], [...m]);
  assert.deepStrictEqual(JSON.parse(text), { a: "branch", b: "head" });
});

test("changesBaseStale: ask again for a new root or HEAD, or for a failure once it has been a while; never for a settled answer", () => {
  const T = 1e12;
  const at = (reply, extra) => Object.assign({ root: "/r", head: "h1", reply, at: T }, extra || {});
  assert.equal(changesBaseStale(null, "/r", "h1", T), true, "nothing asked yet");
  for (const reply of [okBase(), { state: "nobase", branch: "x", ahead: false }, { state: "notrepo" }]) {
    assert.equal(changesBaseStale(at(reply), "/r", "h1", T + 3600e3), false, reply.state + " is settled until the root or HEAD moves");
    assert.equal(changesBaseStale(at(reply), "/r2", "h1", T), true, "another root");
    assert.equal(changesBaseStale(at(reply), "/r", "h2", T), true, "another HEAD");
    assert.equal(changesBaseStale(at(reply), "/r", "", T), true, "a repository that has just got no commits");
  }
  for (const reply of [null, { state: "unavailable", reason: "Git took too long" }]) {
    assert.equal(changesBaseStale(at(reply), "/r", "h1", T + 29999), false, "a failure is not asked again at once");
    assert.equal(changesBaseStale(at(reply), "/r", "h1", T + 30000), true, "but after 30 s it is");
  }
});

test("changesTotals sums the counts numstat knows, over the files under the root; unknown without an ok reply", () => {
  const ns = numstat([
    { p: "a.go", x: "M", a: 4, d: 2 },
    { p: "b/c.go", x: "A", a: 10, d: 0 },
    { p: "img.png", x: "M", a: 0, d: 0, bin: true },
    { p: "bad.go", x: "M", a: -1, d: 3 },
    { p: "a.go", x: "M", a: 100, d: 100 }, // a repeat of a path: the first one counts
    { p: "dir/", x: "M", a: 9, d: 9 },
    { p: "weird.go", x: "Z", a: 9, d: 9 },
  ]);
  assert.deepStrictEqual(changesTotals(ns, "/r"), { add: 14, del: 2 });
  assert.deepStrictEqual(changesTotals(numstat([]), "/r"), { add: 0, del: 0 }, "an empty diff is a known +0 −0");
  for (const bad of [null, undefined, { state: "unavailable", rootRel: ".", entries: [] }, { state: "toomany", rootRel: ".", entries: [] }, { state: "ok", rootRel: ".", entries: "x" }]) {
    assert.equal(changesTotals(bad, "/r"), null);
  }
  assert.equal(changesTotals(ns, ""), null);
  // Under a subfolder root only that folder's files count, as in the rows.
  const sub = numstat([{ p: "internal/a.go", x: "M", a: 2, d: 1 }, { p: "other/b.go", x: "M", a: 5, d: 5 }], { rootRel: "internal" });
  assert.deepStrictEqual(changesTotals(sub, "/home/u/r/internal"), { add: 2, del: 1 });
});

// view: the section's model for one root, with the records dockchanges.js keeps (status as {root, status}, numstat and base as {root, ..., reply}).
const view = (o) => changesView(Object.assign({ root: "/r", prefs: new Map(), sid: "s", files: [] }, o));
const stat = (st, root) => ({ root: root || "/r", status: st });
const numrec = (scope, reply, root) => ({ root: root || "/r", scope, reply });
const basrec = (reply, root) => ({ root: root || "/r", head: "h", reply, at: 1 });

test("changesView: nothing to show without a root, without a status for this root, or for a folder that is not a repository", () => {
  const ok = status([{ p: "a.go", x: "M" }]);
  assert.deepStrictEqual(view({ root: "", status: stat(ok) }), { kind: "none", count: 0 });
  assert.deepStrictEqual(view({ status: null }), { kind: "none", count: 0 });
  assert.deepStrictEqual(view({ status: stat(ok, "/other") }), { kind: "none", count: 0 }, "the last reply was for another root");
  assert.deepStrictEqual(view({ status: stat({ state: "notrepo", entries: [] }) }), { kind: "none", count: 0 });
  assert.deepStrictEqual(view({ status: stat({ state: "mystery", entries: [] }) }), { kind: "none", count: 0 });
  assert.deepStrictEqual(changesView(), { kind: "none", count: 0 });
});

test("changesView: an unavailable status is one calm note with its reason as the hover text, never rows and never a wait", () => {
  const v = view({ status: stat({ state: "unavailable", reason: "Git status needs the box runtime (docker)", rootRel: "", entries: [] }) });
  assert.deepStrictEqual(v, { kind: "note", text: "Status unavailable", title: "Git status needs the box runtime (docker)", count: 0 });
  assert.equal(view({ status: stat({ state: "unavailable", rootRel: "", entries: [] }) }).title, "", "no reason, no title");
  assert.equal(view({ status: stat({ state: "unavailable", reason: 7, entries: [] }) }).title, "", "a reason that is not text is dropped");
  assert.deepStrictEqual(view({ status: stat({ state: "toomany", rootRel: ".", entries: [] }) }), { kind: "note", text: "Too many changes to list (>5000)", title: "", count: 0 });
});

test("changesView: Uncommitted lists status entries at once, counts follow when numstat lands, and the count is rows plus more", () => {
  const st = stat(status([{ p: "a.go", x: "M" }, { p: "n.txt", x: "U" }]));
  const before = view({ status: st });
  assert.equal(before.kind, "rows");
  assert.equal(before.scope, "head");
  assert.equal(before.showScope, false, "no base yet: no toggle");
  assert.equal(before.branch, null);
  assert.deepStrictEqual(before.rows.map((r) => [r.path, r.add, r.del]), [["/r/a.go", null, null], ["/r/n.txt", null, null]]);
  assert.equal(before.count, 2);
  const after = view({ status: st, numstat: numrec("head", numstat([{ p: "a.go", x: "M", a: 3, d: 1 }, { p: "n.txt", x: "U", a: 5, d: 0 }])) });
  assert.deepStrictEqual(after.rows.map((r) => [r.add, r.del]), [[3, 1], [5, 0]]);
  // A numstat record for the other scope or another root is not this section's.
  const other = view({ status: st, numstat: numrec("branch", numstat([{ p: "zz.go", x: "M", a: 1, d: 1 }])) });
  assert.deepStrictEqual(other.rows.map((r) => r.path), ["/r/a.go", "/r/n.txt"]);
  const stale = view({ status: st, numstat: numrec("head", numstat([{ p: "zz.go", x: "M", a: 1, d: 1 }]), "/other") });
  assert.deepStrictEqual(stale.rows.map((r) => r.path), ["/r/a.go", "/r/n.txt"]);
  const big = [];
  for (let i = 0; i < 250; i++) big.push({ p: "f" + String(i).padStart(3, "0") + ".go", x: "M" });
  const many = view({ status: stat(status(big)) });
  assert.equal(many.rows.length, 200);
  assert.equal(many.more, 50);
  assert.equal(many.count, 250);
});

test("changesView: the toggle shows only with an ok base; the default is Branch when ahead; a remembered pick wins", () => {
  const st = stat(status([{ p: "a.go", x: "M" }]));
  for (const reply of [{ state: "nobase", branch: "x", ahead: false }, { state: "notrepo" }, { state: "unavailable", reason: "r" }, null]) {
    const v = view({ status: st, base: basrec(reply) });
    assert.equal(v.showScope, false, String(reply && reply.state));
    assert.equal(v.scope, "head");
  }
  assert.equal(view({ status: st, base: basrec(okBase({ ahead: false })) }).showScope, true);
  assert.equal(view({ status: st, base: basrec(okBase({ ahead: false })) }).scope, "head");
  // Ahead, nothing remembered: Branch, whose rows wait for their own numstat.
  const wait = view({ status: st, base: basrec(okBase({ ahead: true })) });
  assert.deepStrictEqual(wait, { kind: "note", text: "Reading branch changes…", title: "", count: 0, scope: "branch", showScope: true },
    "Branch, whose rows wait for their own numstat; the toggle stays so the way back is on screen");
  const pick = view({ status: st, base: basrec(okBase({ ahead: true })), prefs: new Map([["s", "head"]]) });
  assert.equal(pick.scope, "head");
  assert.equal(pick.kind, "rows");
  // The base record is for one root: another root's base is no base.
  assert.equal(view({ status: st, base: basrec(okBase({ ahead: true }), "/other") }).showScope, false);
});

test("changesView: Branch lists numstat's files against the merge base, with the branch row and its totals", () => {
  const st = stat(status([{ p: "a.go", x: "M" }, { p: "new.txt", x: "U" }]));
  const base = basrec(okBase({ ahead: true, branch: "feat/x", baseName: "origin/main" }));
  const ns = numrec("branch", numstat([{ p: "a.go", x: "A", a: 10, d: 0 }, { p: "b.go", x: "M", a: 3, d: 1 }]));
  const v = view({ status: st, base, numstat: ns });
  assert.equal(v.kind, "rows");
  assert.equal(v.scope, "branch");
  assert.equal(v.showScope, true);
  assert.deepStrictEqual(v.rows.map((r) => [r.path, r.code, r.add, r.del]), [["/r/a.go", "A", 10, 0], ["/r/b.go", "M", 3, 1], ["/r/new.txt", "U", null, null]]);
  assert.deepStrictEqual(v.branch, { from: "feat/x", to: "origin/main", add: 13, del: 1 });
  assert.equal(v.count, 3);
});

test("changesView: a detached HEAD is named so, and a branch row has no counts when it has no numstat total", () => {
  const st = stat(status([]));
  const base = basrec(okBase({ ahead: true, branch: "" }));
  const v = view({ status: st, base, numstat: numrec("branch", numstat([])) });
  assert.deepStrictEqual(v.branch, { from: "(detached)", to: "main", add: 0, del: 0 });
  assert.deepStrictEqual(view({ status: st, base: basrec(okBase({ ahead: true, branch: 5, baseName: undefined })), numstat: numrec("branch", numstat([])) }).branch, { from: "(detached)", to: "", add: 0, del: 0 });
});

test("changesView: Branch with a numstat that failed is a calm note; too many is said as such", () => {
  const st = stat(status([{ p: "a.go", x: "M" }]));
  const base = basrec(okBase({ ahead: true }));
  const failed = view({ status: st, base, numstat: numrec("branch", null) });
  assert.deepStrictEqual(failed, { kind: "note", text: "Branch changes unavailable", title: "", count: 0, scope: "branch", showScope: true });
  const un = view({ status: st, base, numstat: numrec("branch", { state: "unavailable", reason: "no base branch", rootRel: "", entries: [] }) });
  assert.deepStrictEqual({ text: un.text, title: un.title }, { text: "Branch changes unavailable", title: "no base branch" });
  const many = view({ status: st, base, numstat: numrec("branch", { state: "toomany", rootRel: ".", entries: [] }) });
  assert.deepStrictEqual([many.text, many.scope, many.showScope], ["Too many changes to list (>5000)", "branch", true]);
});

test("changesView: mine rows come first and the view leaves its inputs alone", () => {
  const args = freeze({
    root: "/r",
    sid: "s",
    prefs: new Map(),
    files: [{ path: "/r/z.go", add: 1, del: 0 }],
    status: stat(status([{ p: "a.go", x: "M" }, { p: "z.go", x: "M" }])),
    base: basrec(okBase()),
    numstat: numrec("head", numstat([{ p: "a.go", x: "M", a: 1, d: 1 }, { p: "z.go", x: "M", a: 2, d: 2 }])),
  });
  const v = changesView(args);
  assert.deepStrictEqual(v.rows.map((r) => [r.name, r.mine]), [["z.go", true], ["a.go", false]]);
});
