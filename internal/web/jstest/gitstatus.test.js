// internal/web/jstest/gitstatus.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const g = require("../static/gitstatus.js");

const ok = (entries, extra) => Object.assign({ state: "ok", reason: "", top: "/r", rootRel: ".", noHead: false, entries }, extra || {});

test("gitPlace: a path under the top, its segments, and a trailing slash for a folder", () => {
  assert.deepEqual(g.gitPlace("a/b/c.go", ".", "/r"), { abs: "/r/a/b/c.go", segs: ["a", "b", "c.go"], isDir: false });
  assert.deepEqual(g.gitPlace("tmp/out/", ".", "/r"), { abs: "/r/tmp/out", segs: ["tmp", "out"], isDir: true });
  assert.deepEqual(g.gitPlace("x.go", ".", ""), { abs: "/x.go", segs: ["x.go"], isDir: false }); // a root of "/" has an empty base
});

test("gitPlace: a subfolder root maps through rootRel and drops what is outside it or is the root", () => {
  assert.deepEqual(g.gitPlace("internal/a/b.go", "internal", "/home/u/r/internal"), { abs: "/home/u/r/internal/a/b.go", segs: ["a", "b.go"], isDir: false });
  assert.deepEqual(g.gitPlace("internal/new/", "internal", "/h/internal"), { abs: "/h/internal/new", segs: ["new"], isDir: true });
  for (const p of ["README.md", "internalx/z.go", "internal", "internal/", "other/internal/a.go"]) {
    assert.equal(g.gitPlace(p, "internal", "/h/internal"), null, p);
  }
});

test("gitPlace: junk gives null (empty or doubled segments, a path that is not text, no usable rootRel)", () => {
  for (const p of ["", "/", "a//b.go", "/abs.go", "a/b//", 7, null, undefined]) assert.equal(g.gitPlace(p, ".", "/r"), null, String(p));
  for (const rr of ["", undefined, null, 3]) assert.equal(g.gitPlace("a.go", rr, "/r"), null, String(rr));
});

test("gitMarks: a modified file is marked, its folders get a dot, the root does not", () => {
  const m = g.gitMarks(ok([{ p: "a/b/c.go", x: "M" }]), "/r");
  assert.deepEqual([...m.keys()].sort(), ["/r/a", "/r/a/b", "/r/a/b/c.go"]);
  assert.equal(m.get("/r/a/b/c.go").code, "M");
  assert.ok(!m.get("/r/a/b/c.go").dir);
  assert.equal(m.get("/r/a").dir, true);
  assert.equal(m.get("/r/a").code, "M");
  assert.equal(m.get("/r/a").title, "1 change inside");
  assert.ok(!m.has("/r"));
});

test("gitMarks: subfolder root maps through rootRel, not top; entries outside are ignored", () => {
  // top is the symlink-resolved checkout and may not match the row paths (R10).
  const st = ok([{ p: "internal/a/b.go", x: "M" }, { p: "README.md", x: "M" }, { p: "internalx/z.go", x: "M" }],
    { top: "/real/r", rootRel: "internal" });
  const m = g.gitMarks(st, "/home/u/r/internal");
  assert.deepEqual([...m.keys()].sort(), ["/home/u/r/internal/a", "/home/u/r/internal/a/b.go"]);
});

test("gitMarks: a trailing slash on root is tolerated", () => {
  const m = g.gitMarks(ok([{ p: "a/b.go", x: "A" }]), "/r/");
  assert.equal(m.get("/r/a/b.go").code, "A");
});

test("gitMarks: a deleted file gets no row, only the folder dot with a deleted count", () => {
  const m = g.gitMarks(ok([{ p: "src/gone.go", x: "D" }]), "/r");
  assert.ok(!m.has("/r/src/gone.go"));
  assert.equal(m.get("/r/src").dir, true);
  assert.equal(m.get("/r/src").code, "D");
  assert.equal(m.get("/r/src").title, "1 change inside (1 deleted)");
});

test("gitMarks: a deleted file directly in the root marks nothing visible", () => {
  assert.equal(g.gitMarks(ok([{ p: "gone.go", x: "D" }]), "/r").size, 0);
});

test("gitMarks: an untracked directory marks its own row with U", () => {
  const m = g.gitMarks(ok([{ p: "vendor/", x: "U" }, { p: "a/cache/", x: "U" }]), "/r");
  assert.equal(m.get("/r/vendor").code, "U");
  assert.equal(m.get("/r/vendor").untracked, true);
  assert.equal(m.get("/r/a/cache").code, "U");
  assert.equal(m.get("/r/a").dir, true);
  assert.equal(m.get("/r/a").code, "U");
  assert.ok(!m.get("/r/a").untracked);
});

test("gitMarks: a folder takes the strongest descendant (C = D > M > A = U) and counts them all", () => {
  const m = g.gitMarks(ok([
    { p: "d/a.go", x: "A" }, { p: "d/u.go", x: "U" }, { p: "d/m.go", x: "M" },
    { p: "e/a.go", x: "A" }, { p: "e/m.go", x: "M" },
    { p: "f/m.go", x: "M" }, { p: "f/x.go", x: "D" }, { p: "f/y.go", x: "D" },
    { p: "h/m.go", x: "M" }, { p: "h/c.go", x: "C" },
  ]), "/r");
  assert.equal(m.get("/r/d").code, "M");
  assert.equal(m.get("/r/d").title, "3 changes inside");
  assert.equal(m.get("/r/e").code, "M");
  assert.equal(m.get("/r/f").code, "D");
  assert.equal(m.get("/r/f").title, "3 changes inside (2 deleted)");
  assert.equal(m.get("/r/h").code, "C");
});

test("gitMarks: nested folders each count the changes below them", () => {
  const m = g.gitMarks(ok([{ p: "a/b/x.go", x: "M" }, { p: "a/y.go", x: "A" }]), "/r");
  assert.equal(m.get("/r/a").title, "2 changes inside");
  assert.equal(m.get("/r/a/b").title, "1 change inside");
});

test("gitMarks: anything but state ok is an empty map", () => {
  const e = [{ p: "a.go", x: "M" }];
  for (const state of ["unavailable", "toomany", "notrepo", ""]) {
    assert.equal(g.gitMarks(ok(e, { state }), "/r").size, 0, state);
  }
  assert.equal(g.gitMarks(null, "/r").size, 0);
  assert.equal(g.gitMarks(ok(e, { rootRel: "" }), "/r").size, 0);
  assert.equal(g.gitMarks({ state: "ok" }, "/r").size, 0);
});

test("gitMarks: unknown codes and malformed entries are skipped", () => {
  const m = g.gitMarks(ok([{ p: "a.go", x: "Z" }, { p: "", x: "M" }, null, { x: "M" }, { p: "ok.go", x: "M" }]), "/r");
  assert.deepEqual([...m.keys()], ["/r/ok.go"]);
});

test("gitMarks: an entry that is the root itself is ignored", () => {
  const st = ok([{ p: "internal/", x: "U" }], { rootRel: "internal" });
  assert.equal(g.gitMarks(st, "/h/r/internal").size, 0);
});

// --- seq guard ---------------------------------------------------------------

function deferredFetch() {
  const calls = [];
  global.fetch = (url, init) => new Promise((resolve) => {
    calls.push({ url, init, resolve: (status) => resolve({ ok: true, status: 200, json: async () => status }) });
  });
  return calls;
}

test("gitStatusLoad: a slow older reply does not overwrite a newer one", async () => {
  g.gitStatusReset();
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  const b = g.gitStatusLoad("/r", "s1");
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, "/api/git/status");
  assert.equal(JSON.parse(calls[0].init.body).path, "/r");
  assert.equal(calls[0].init.method, "POST");
  calls[1].resolve(ok([{ p: "new.go", x: "M" }]));
  await b;
  calls[0].resolve(ok([{ p: "old.go", x: "M" }]));
  await a;
  assert.equal(g.gitStatusCodeFor("/r/new.go"), "M");
  assert.equal(g.gitStatusCodeFor("/r/old.go"), "");
});

test("gitStatusLoad: the newest reply wins even when it arrives first and the old one is last", async () => {
  g.gitStatusReset();
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  const b = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([{ p: "old.go", x: "M" }]));
  await a;
  assert.equal(g.gitStatusCodeFor("/r/old.go"), "", "a superseded reply is dropped");
  calls[1].resolve(ok([{ p: "new.go", x: "A" }]));
  await b;
  assert.equal(g.gitStatusCodeFor("/r/new.go"), "A");
});

test("gitStatusLoad: a network failure keeps what was shown", async () => {
  g.gitStatusReset();
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
  await a;
  global.fetch = () => Promise.reject(new Error("offline"));
  await g.gitStatusLoad("/r", "s1");
  assert.equal(g.gitStatusCodeFor("/r/a.go"), "M");
});

test("gitStatusLoad: a different root drops the old marks at once", async () => {
  g.gitStatusReset();
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
  await a;
  assert.equal(g.gitStatusCodeFor("/r/a.go"), "M");
  g.gitStatusLoad("/other", "s1");
  assert.equal(g.gitStatusCodeFor("/r/a.go"), "");
});

test("gitStatusReason: unavailable carries the reason, toomany a fixed note, ok nothing", async () => {
  g.gitStatusReset();
  let calls = deferredFetch();
  let p = g.gitStatusLoad("/r", "s1");
  calls[0].resolve({ state: "unavailable", reason: "docker is not running", entries: [] });
  await p;
  assert.equal(g.gitStatusReason(), "docker is not running");
  assert.equal(g.gitStatusNote(), "");
  calls = deferredFetch();
  p = g.gitStatusLoad("/r", "s1");
  calls[0].resolve({ state: "toomany", reason: "", entries: [] });
  await p;
  assert.equal(g.gitStatusReason(), "");
  assert.equal(g.gitStatusNote(), "Too many changes to mark (>5000)");
  calls = deferredFetch();
  p = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([]));
  await p;
  assert.equal(g.gitStatusReason(), "");
  assert.equal(g.gitStatusNote(), "");
});

test("gitStatusRefreshSoon: debounces to one request after 1 s", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const calls = deferredFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon();
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(900);
    g.gitStatusRefreshSoon(); // restarts the window
    t.mock.timers.tick(900);
    assert.equal(calls.length, 0);
    t.mock.timers.tick(100);
    assert.equal(calls.length, 1);
  } finally { delete global.ftRoot; }
});

test("gitStatusLoad: fresh is sent only when asked for", async () => {
  g.gitStatusReset();
  const calls = deferredFetch();
  g.gitStatusLoad("/r", "s1");
  g.gitStatusLoad("/r", "s1", { fresh: true });
  assert.deepEqual(JSON.parse(calls[0].init.body), { path: "/r" });
  assert.deepEqual(JSON.parse(calls[1].init.body), { path: "/r", fresh: true });
});

test("gitStatusRefreshSoon: the debounced request is fresh, unless every caller opted out", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const calls = deferredFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[0].init.body).fresh, true);
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[1].init.body).fresh, undefined);
    // one fresh caller in the window makes the whole window fresh
    g.gitStatusRefreshSoon({ fresh: false });
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[2].init.body).fresh, true);
    // and the flag does not leak into the next window
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[3].init.body).fresh, undefined);
  } finally { delete global.ftRoot; }
});
