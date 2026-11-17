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

// settle: let the continuations of resolved requests run (setImmediate is not one of the mocked timers).
const settle = async () => { for (let i = 0; i < 8; i++) await new Promise((r) => setImmediate(r)); };

// pendingFetch: a fetch whose requests stay out until a test answers (`resolve`), fails (`fail`) or the code aborts them.
function pendingFetch() {
  const calls = [];
  global.fetch = (url, init) => new Promise((resolve, reject) => {
    const c = {
      url, init, done: false, sent: Date.now(), fresh: JSON.parse(init.body).fresh === true, aborted: false,
      resolve: (st) => { c.done = true; resolve({ ok: true, status: 200, json: async () => st }); },
      fail: () => { c.done = true; reject(new Error("offline")); },
    };
    if (init.signal) init.signal.addEventListener("abort", () => { if (!c.done) { c.done = true; c.aborted = true; reject(Object.assign(new Error("aborted"), { name: "AbortError" })); } });
    calls.push(c);
  });
  calls.out = () => calls.filter((c) => !c.done).length;
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

test("gitStatusLoad: replies land in the order they were sent: an older one that arrives first is applied, and the newer one replaces it", async () => {
  g.gitStatusReset();
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  const b = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([{ p: "old.go", x: "M" }]));
  await a;
  // R16: only a reply OLDER than the status already applied is dropped. This one is the first to land, so it is applied (it is
  // the freshest thing known), and it is not starved by the newer ask that is still out.
  assert.equal(g.gitStatusCodeFor("/r/old.go"), "M", "an older reply with nothing newer applied is shown");
  calls[1].resolve(ok([{ p: "new.go", x: "A" }]));
  await b;
  assert.equal(g.gitStatusCodeFor("/r/new.go"), "A");
  assert.equal(g.gitStatusCodeFor("/r/old.go"), "", "and the newer one replaces it");
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
  // Each request is answered before the next window: one that is still out would hold the next debounced ask (below).
  const land = async (i) => { calls[i].resolve(ok([])); await settle(); };
  try {
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[0].init.body).fresh, true);
    await land(0);
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[1].init.body).fresh, undefined);
    await land(1);
    // one fresh caller in the window makes the whole window fresh
    g.gitStatusRefreshSoon({ fresh: false });
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[2].init.body).fresh, true);
    await land(2);
    // and the flag does not leak into the next window
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(JSON.parse(calls[3].init.body).fresh, undefined);
    await land(3);
  } finally { delete global.ftRoot; }
});

// --- R16: statuses land in order, and never starve -----------------------------------------------------------------------------

test("gitStatusLoad: with a status slower than the gap between asks, every reply still lands while newer asks are out", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 1000 });
  const calls = deferredFetch();
  const heard = [];
  global.dockChangesOnStatus = (o) => heard.push(o);
  try {
    // An ask every 2 s (an edit-bearing poll and the debounce), each answer taking 2.5 s.
    const p = [g.gitStatusLoad("/r", "s1", { fresh: true })]; // sent at 1000
    t.mock.timers.tick(2000);
    p.push(g.gitStatusLoad("/r", "s1", { fresh: true })); // sent at 3000, while the first is still out
    t.mock.timers.tick(500); // 3500: the first answers
    calls[0].resolve(ok([{ p: "one.go", x: "M" }]));
    await p[0];
    assert.equal(g.gitStatusCodeFor("/r/one.go"), "M", "the first reply lands although a newer ask is out");
    assert.equal(g.gitStatusAt(), 1000);
    t.mock.timers.tick(1500); // 5000
    p.push(g.gitStatusLoad("/r", "s1", { fresh: true })); // sent at 5000
    t.mock.timers.tick(500); // 5500: the second answers, with the third out
    calls[1].resolve(ok([{ p: "one.go", x: "M" }, { p: "two.go", x: "M" }]));
    await p[1];
    assert.equal(g.gitStatusCodeFor("/r/two.go"), "M", "and the second");
    assert.equal(g.gitStatusAt(), 3000, "gitStatusAt follows the reply applied");
    t.mock.timers.tick(2000); // 7500
    calls[2].resolve(ok([{ p: "one.go", x: "M" }, { p: "two.go", x: "M" }, { p: "three.go", x: "A" }]));
    await p[2];
    assert.equal(g.gitStatusCodeFor("/r/three.go"), "A");
    assert.equal(g.gitStatusAt(), 5000);
    assert.deepEqual(heard, [{ fresh: true }, { fresh: true }, { fresh: true }], "the Changes section heard of each landing");
  } finally { delete global.dockChangesOnStatus; }
});

test("gitStatusLoad: an older reply that arrives after a newer one was applied is dropped", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 100 });
  const calls = deferredFetch();
  const heard = [];
  global.dockChangesOnStatus = (o) => heard.push(o);
  try {
    const a = g.gitStatusLoad("/r", "s1", { fresh: true }); // sent at 100
    t.mock.timers.tick(50);
    const b = g.gitStatusLoad("/r", "s1", { fresh: true }); // sent at 150
    t.mock.timers.tick(50);
    const c = g.gitStatusLoad("/r", "s1", { fresh: true }); // sent at 200
    calls[1].resolve(ok([{ p: "b.go", x: "M" }]));
    await b;
    assert.equal(g.gitStatusCodeFor("/r/b.go"), "M");
    assert.equal(g.gitStatusAt(), 150);
    calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
    assert.equal(await a, null, "the older reply is dropped");
    assert.equal(g.gitStatusCodeFor("/r/a.go"), "", "its marks never show");
    assert.equal(g.gitStatusCodeFor("/r/b.go"), "M", "the status applied stays");
    assert.equal(g.gitStatusAt(), 150, "and so does its time");
    assert.equal(heard.length, 1, "nobody heard of the dropped one");
    calls[2].resolve(ok([{ p: "c.go", x: "A" }]));
    await c;
    assert.equal(g.gitStatusCodeFor("/r/c.go"), "A");
    assert.equal(g.gitStatusAt(), 200);
  } finally { delete global.dockChangesOnStatus; }
});

test("gitStatusLoad: a reply for another root than the newest ask's is dropped, and a reset drops what was out", async () => {
  g.gitStatusReset();
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  const b = g.gitStatusLoad("/other", "s1");
  calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
  assert.equal(await a, null);
  assert.equal(g.gitStatusCurrent("/r"), null, "the older ask was for the root left behind");
  assert.equal(g.gitStatusCodeFor("/r/a.go"), "");
  calls[1].resolve(ok([{ p: "z.go", x: "M" }]));
  await b;
  assert.equal(g.gitStatusCurrent("/other").status.entries[0].p, "z.go");
  // A reset forgets the status and every reply still out.
  g.gitStatusReset();
  calls = deferredFetch();
  const c = g.gitStatusLoad("/r", "s1");
  g.gitStatusReset();
  calls[0].resolve(ok([{ p: "c.go", x: "M" }]));
  assert.equal(await c, null);
  assert.equal(g.gitStatusCurrent("/r"), null);
});

// --- one status ask at a time from the debounced path ------------------------------------------------------------------------

const answerFor = (n) => ok(Array.from({ length: n }, (_, i) => ({ p: "f" + i + ".go", x: "M" })));

test("gitStatusRefreshSoon: with a status slower than the poll, no more than one status request is ever out", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  let most = 0;
  try {
    // An edit-bearing poll every 2 s asks for a fresh status (1 s debounce); git needs 2.5 s to answer.
    for (let ms = 0; ms < 24000; ms += 500) {
      if (ms % 2000 === 0) g.gitStatusRefreshSoon({ fresh: true });
      t.mock.timers.tick(500);
      for (const c of calls) if (!c.done && Date.now() - c.sent >= 2500) c.resolve(answerFor(1 + Math.floor(ms / 2000)));
      await settle();
      most = Math.max(most, calls.out());
    }
    assert.equal(most, 1, "never two out at once");
    // One round trip is 2.5 s and the next ask goes out at the landing: about one per round trip, not one per poll (12) or per
    // debounce window (12 of them); the requests are the number of landings plus the one still out.
    assert.ok(calls.length >= 8 && calls.length <= 10, "requests: " + calls.length);
    assert.ok(calls.every((c) => c.fresh), "and every one of them fresh");
    assert.equal(g.gitStatusCodeFor("/r/f0.go"), "M", "statuses land during the run (R16's rule is the safety net, not the plan)");
  } finally { delete global.ftRoot; }
});

test("gitStatusRefreshSoon: asks that come while one is out collapse into ONE trailing ask, sent when it lands, fresh if any was", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(calls.length, 1);
    // Three windows pass while it is out, one of them with a fresh caller: nothing is sent.
    g.gitStatusRefreshSoon({ fresh: false }); t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon({ fresh: true }); t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon({ fresh: false }); t.mock.timers.tick(1000);
    await settle();
    assert.equal(calls.length, 1, "held");
    calls[0].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 2, "one trailing ask, at the landing, with no new debounce wait");
    assert.equal(calls[1].fresh, true, "fresh because one collapsed ask was");
    calls[1].resolve(answerFor(2));
    await settle();
    t.mock.timers.tick(10000);
    await settle();
    assert.equal(calls.length, 2, "and nothing more: it was sent once");
    // All collapsed asks plain: the trailing ask is plain.
    g.gitStatusRefreshSoon({ fresh: false }); t.mock.timers.tick(1000);
    assert.equal(calls.length, 3);
    g.gitStatusRefreshSoon({ fresh: false }); t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon({ fresh: false }); t.mock.timers.tick(1000);
    calls[2].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 4);
    assert.equal(calls[3].fresh, false, "no collapsed ask was fresh: the trailing one is not");
    assert.equal(calls[3].init.body.includes("fresh"), false);
  } finally { delete global.ftRoot; }
});

test("gitStatusRefreshSoon: a request that fails, or never answers, frees the slot and the held ask goes out", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000);
    assert.equal(calls.length, 1);
    calls[0].fail(); // the network
    await settle();
    assert.equal(calls.length, 2, "a failure releases it");
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000);
    assert.equal(calls.length, 2, "held behind the second");
    t.mock.timers.tick(14000); // the second has been out for 15 s: abandoned
    await settle();
    assert.equal(calls[1].aborted, true, "a request that never answers is aborted after GIT_FETCH_MS");
    assert.equal(calls.length, 3, "and the held ask is sent");
    assert.equal(calls.out(), 1);
  } finally { delete global.ftRoot; }
});

test("gitStatusRefreshSoon: the held ask is for the root and session of the moment it is sent, and a reset forgets it", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const calls = pendingFetch();
  let root = "/r";
  global.ftRoot = () => root;
  try {
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000); // held
    root = "/other"; // the tree moved while it was held
    calls[0].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 2);
    assert.equal(JSON.parse(calls[1].init.body).path, "/other");
    // A reset forgets a held ask (and what is out).
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000); // held behind the one out
    g.gitStatusReset();
    calls[1].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 2, "nothing is sent for a forgotten ask");
  } finally { delete global.ftRoot; }
});

test("an explicit load is not held (a tree paint, the refresh button go out at once), and a held ask waits for every request out", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000); // out: 1
    g.gitStatusRefreshSoon(); t.mock.timers.tick(1000); // held
    g.gitStatusLoad("/r", "s1", { fresh: true }); // the refresh button: goes out beside it (R16 orders the answers)
    assert.equal(calls.length, 2, "explicit asks are not held");
    calls[0].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 2, "still one out: the held ask waits for it too");
    calls[1].resolve(answerFor(2));
    await settle();
    assert.equal(calls.length, 3, "and goes when none is out");
  } finally { delete global.ftRoot; }
});

// --- A3: the dock watch's asks have a floor between them ------------------------------------------------------------------------

// watchRun: edits at `edits` (ms from the start), each one a watch-driven fresh ask as dockwatch.js makes it, for `until` ms in
// 100 ms steps. Every request that goes out is answered 200 ms later, so it is the floor and not the one-at-a-time rule that
// spaces them. -> {calls (each with .sent as ms from the start), t0}.
async function watchRun(t, edits, until) {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 100000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  const t0 = Date.now();
  for (let ms = 0; ms < until; ms += 100) {
    if (edits.includes(ms)) g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(100);
    for (const c of calls) if (!c.done && Date.now() - c.sent >= 200) c.resolve(answerFor(1));
    await settle();
  }
  for (const c of calls) c.sent -= t0;
  return { calls, t0 };
}

test("GIT_WATCH_MIN_MS is 3 s", () => {
  assert.equal(g.GIT_WATCH_MIN_MS, 3000);
});

test("watch-driven asks: an edit every 1.5 s for 10 s sends at most 4 status requests, 3 s apart, and the last edit still gets its ask", async (t) => {
  try {
    const edits = [0, 1500, 3000, 4500, 6000, 7500, 9000];
    const { calls } = await watchRun(t, edits, 16000);
    const sent = calls.map((c) => c.sent);
    assert.ok(sent.filter((ms) => ms < 10000).length <= 4, "within the 10 s of the burst: " + sent);
    for (let i = 1; i < sent.length; i++) assert.ok(sent[i] - sent[i - 1] >= 3000, "3 s floor between sends: " + sent);
    assert.ok(calls.every((c) => c.fresh), "every one fresh");
    assert.ok(sent.at(-1) > 9000, "the trailing ask: a status is asked after the last edit, so its letter lands: " + sent);
    assert.equal(calls.out(), 0);
    assert.equal(g.gitStatusCodeFor("/r/f0.go"), "M", "and it landed");
  } finally { delete global.ftRoot; }
});

test("watch-driven asks: without the floor an edit every 1.5 s was a status every 1.5 s; the floor halves that, never starves the last edit", async (t) => {
  try {
    const edits = [];
    for (let ms = 0; ms < 30000; ms += 1500) edits.push(ms);
    const { calls } = await watchRun(t, edits, 40000);
    const sent = calls.map((c) => c.sent);
    // 20 edits over 30 s: the debounce alone (1 s) would have sent one per edit; the floor allows one per 3 s, and one trailing.
    assert.ok(calls.length <= 12, "requests: " + sent);
    assert.ok(calls.length >= 8, "but it keeps up: " + sent);
    for (let i = 1; i < sent.length; i++) assert.ok(sent[i] - sent[i - 1] >= 3000, sent.join());
    assert.ok(sent.at(-1) > 28500, "the last edit's ask is sent: " + sent);
  } finally { delete global.ftRoot; }
});

test("watch-driven asks: a single edit is sent after the debounce alone, not held for the floor", async (t) => {
  try {
    const { calls } = await watchRun(t, [0], 3000);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].sent, 1000, "the 1 s debounce, nothing more");
  } finally { delete global.ftRoot; }
});

test("watch-driven asks: edits inside the floor collapse into ONE trailing ask when the floor ends", async (t) => {
  try {
    // 0: sent at 1000. Then three edits at 1500, 2000 and 2600: their debounce ends at 3600, the floor at 4000: one ask at 4000.
    const { calls } = await watchRun(t, [0, 1500, 2000, 2600], 8000);
    assert.deepEqual(calls.map((c) => c.sent), [1000, 4000]);
  } finally { delete global.ftRoot; }
});

test("explicit asks are never delayed by the watch floor, and a fresh one answers the watch ask that was waiting", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 100000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  const land = async () => { for (const c of calls) if (!c.done) c.resolve(answerFor(1)); await settle(); };
  try {
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    await land();
    assert.equal(calls.length, 1);
    // A watch ask inside the floor waits (its window ends 1 s after, the floor 2 s later).
    t.mock.timers.tick(200);
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    assert.equal(calls.length, 1, "waiting for the floor");
    // An explicit ask in the same stretch (a save, a file op, the end of a turn) goes out after its own debounce only.
    g.gitStatusRefreshSoon();
    t.mock.timers.tick(1000);
    assert.equal(calls.length, 2, "not delayed by the floor");
    await land();
    // That fresh status was read after the edit, so the watch ask that waited has nothing left to ask.
    t.mock.timers.tick(10000);
    await settle();
    assert.equal(calls.length, 2, "no second status for the same edit");
    // A plain (non-fresh) explicit ask does not answer it.
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    await land();
    assert.equal(calls.length, 3, "the first of a new stretch is sent");
    t.mock.timers.tick(200);
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    g.gitStatusRefreshSoon({ fresh: false });
    t.mock.timers.tick(1000);
    assert.equal(calls.length, 4, "the plain ask goes out");
    await land();
    t.mock.timers.tick(5000);
    await settle();
    assert.equal(calls.length, 5, "and the watch ask still goes when its floor ends");
    assert.equal(calls[4].fresh, true);
    // An explicit load (a tree paint, the refresh button) is never held or delayed.
    await land();
    g.gitStatusLoad("/r", "s1", { fresh: true });
    assert.equal(calls.length, 6);
  } finally { delete global.ftRoot; }
});

test("watch-driven asks: one held behind a slow request goes out at the landing when the floor has passed, and still one status out at a time", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 100000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  let most = 0;
  try {
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    assert.equal(calls.length, 1);
    // git takes 5 s: the asks that come meanwhile are held, not sent beside it.
    g.gitStatusRefreshSoon({ fresh: true, watch: true }); t.mock.timers.tick(1500);
    g.gitStatusRefreshSoon({ fresh: true, watch: true }); t.mock.timers.tick(1500);
    g.gitStatusRefreshSoon({ fresh: true, watch: true }); t.mock.timers.tick(1000);
    most = Math.max(most, calls.out());
    assert.equal(calls.length, 1);
    t.mock.timers.tick(1000); // 5 s since the send
    calls[0].resolve(answerFor(1));
    await settle();
    assert.equal(calls.length, 2, "the trailing ask goes at the landing: the round trip already spaced it by more than the floor");
    assert.equal(calls[1].fresh, true);
    most = Math.max(most, calls.out());
    assert.equal(most, 1);
    // A slow reply that lands INSIDE the floor: the held ask waits out the rest of it.
    calls[1].resolve(answerFor(1));
    await settle();
    t.mock.timers.tick(3000); // the floor since the last send is over
    g.gitStatusRefreshSoon({ fresh: true, watch: true }); t.mock.timers.tick(1000);
    assert.equal(calls.length, 3);
    g.gitStatusRefreshSoon({ fresh: true, watch: true }); t.mock.timers.tick(1000); // held behind it
    calls[2].resolve(answerFor(1)); // lands 1 s after it was sent
    await settle();
    assert.equal(calls.length, 3, "inside the floor: not sent at the landing");
    t.mock.timers.tick(2000);
    await settle();
    assert.equal(calls.length, 4, "sent when the floor ends");
  } finally { delete global.ftRoot; }
});

test("a reset forgets the ask that was waiting for the floor", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 100000 });
  const calls = pendingFetch();
  global.ftRoot = () => "/r";
  try {
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000);
    calls[0].resolve(answerFor(1));
    await settle();
    t.mock.timers.tick(200);
    g.gitStatusRefreshSoon({ fresh: true, watch: true });
    t.mock.timers.tick(1000); // waiting for the floor
    g.gitStatusReset();
    t.mock.timers.tick(10000);
    await settle();
    assert.equal(calls.length, 1, "nothing is sent for a forgotten ask");
  } finally { delete global.ftRoot; }
});

// A test that leaves a request out must not keep the process alive for the request's abort timer.
test.after(() => g.gitStatusReset());

// --- the Changes section's reads (dockchanges.js) -----------------------------------------------------------------

test("gitStatusCurrent: the last status that landed and its root, nothing for another root or before any", async () => {
  g.gitStatusReset();
  assert.equal(g.gitStatusCurrent("/r"), null, "nothing yet");
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
  await a;
  const cur = g.gitStatusCurrent("/r");
  assert.equal(cur.root, "/r");
  assert.equal(cur.status.entries[0].p, "a.go");
  assert.equal(g.gitStatusCurrent("/other"), null, "the held status is for /r only");
  assert.equal(g.gitStatusCurrent(""), null);
});

test("gitStatusLoad tells the Changes section when a status lands, fresh when it was asked for fresh, and not for one that failed", async () => {
  g.gitStatusReset();
  const heard = [];
  global.dockChangesOnStatus = (o) => heard.push(o);
  try {
    const calls = deferredFetch();
    const a = g.gitStatusLoad("/r", "s1", { fresh: true });
    calls[0].resolve(ok([]));
    await a;
    const b = g.gitStatusLoad("/r", "s1");
    calls[1].resolve(ok([]));
    await b;
    assert.deepEqual(heard, [{ fresh: true }, { fresh: false }]);
    global.fetch = () => Promise.reject(new Error("offline"));
    await g.gitStatusLoad("/r", "s1");
    assert.equal(heard.length, 2, "a failed request keeps what was shown and says nothing");
  } finally { delete global.dockChangesOnStatus; }
});

// --- Task 9c: when the status held was asked for (gitStatusAt) ------------------------------------------------------------

test("gitStatusAt: 0 before any status, then the time the request that landed was SENT, not when it landed", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 1000 });
  assert.equal(g.gitStatusAt(), 0, "nothing yet");
  const calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1", { fresh: true });
  t.mock.timers.tick(700); // the container takes a while to answer
  calls[0].resolve(ok([]));
  await a;
  assert.equal(g.gitStatusAt(), 1000, "the ask, not the answer");
  g.gitStatusReset();
  assert.equal(g.gitStatusAt(), 0, "a reset forgets it with the status");
});

test("gitStatusAt follows the status shown: an ask still out, a superseded one and a failed one do not move it", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 5000 });
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1", { fresh: true });
  calls[0].resolve(ok([]));
  await a;
  assert.equal(g.gitStatusAt(), 5000);
  t.mock.timers.tick(3000);
  // A newer ask is out, and the one on screen is still the older: an edit between them is not git's to have seen.
  calls = deferredFetch();
  const b = g.gitStatusLoad("/r", "s1", { fresh: true });
  assert.equal(g.gitStatusAt(), 5000, "the answer on screen is for the first ask");
  calls[0].resolve(ok([]));
  await b;
  assert.equal(g.gitStatusAt(), 8000);
  // A failed request keeps the status, and so its time.
  t.mock.timers.tick(1000);
  global.fetch = () => Promise.reject(new Error("offline"));
  await g.gitStatusLoad("/r", "s1", { fresh: true });
  assert.equal(g.gitStatusAt(), 8000);
});

test("gitStatusAt: a plain reload may be the server's reused answer, so it proves nothing newer than the fresh one before it did", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 10000 });
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1", { fresh: true });
  calls[0].resolve(ok([]));
  await a;
  t.mock.timers.tick(1500);
  calls = deferredFetch();
  const b = g.gitStatusLoad("/r", "s1"); // a tree paint: the server may answer from what it read 2 s ago
  calls[0].resolve(ok([{ p: "a.go", x: "M" }]));
  await b;
  assert.equal(g.gitStatusCodeFor("/r/a.go"), "M", "its marks are shown all the same");
  assert.equal(g.gitStatusAt(), 10000, "but an edit after the fresh ask still has no answer from git");
  t.mock.timers.tick(1000);
  calls = deferredFetch();
  const c = g.gitStatusLoad("/r", "s1", { fresh: true });
  calls[0].resolve(ok([]));
  await c;
  assert.equal(g.gitStatusAt(), 12500);
});

test("gitStatusAt: a plain ask is trusted only back to the server's reuse window before it was sent; a fresh one to its send time", async (t) => {
  assert.equal(g.GIT_REUSE_MS, 2000, "internal/gitview/runner.go defaultReuse: the server answers a plain ask from a run up to 2 s old");
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 20000 });
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1"); // plain, the first for this root
  t.mock.timers.tick(10);
  calls[0].resolve(ok([]));
  await a;
  assert.equal(g.gitStatusAt(), 18000, "the first plain load: its send time minus the reuse window (an edit 1 s before it may not be in the answer)");
  // A fresh ask later is as new as its send time.
  t.mock.timers.tick(5000); // 25010
  calls = deferredFetch();
  const f = g.gitStatusLoad("/r", "s1", { fresh: true });
  calls[0].resolve(ok([]));
  await f;
  assert.equal(g.gitStatusAt(), 25010);
  // A plain ask much later is not trusted back to that fresh one: only to its own send time minus the window.
  t.mock.timers.tick(30000); // 55010
  calls = deferredFetch();
  const b = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([]));
  await b;
  assert.equal(g.gitStatusAt(), 53010);
  // Another root starts over: the old root's time is nobody's for it.
  t.mock.timers.tick(1000); // 56010
  calls = deferredFetch();
  const c = g.gitStatusLoad("/other", "s1");
  calls[0].resolve(ok([]));
  await c;
  assert.equal(g.gitStatusAt(), 54010);
});

test("gitStatusAt never goes backwards for a root: a plain ask just after a fresh one keeps the fresh time", async (t) => {
  g.gitStatusReset();
  t.mock.timers.enable({ apis: ["Date"], now: 100000 });
  let calls = deferredFetch();
  const a = g.gitStatusLoad("/r", "s1", { fresh: true }); // sent at 100000
  calls[0].resolve(ok([]));
  await a;
  t.mock.timers.tick(500); // 100500: a plain ask now would be trusted only to 98500, but the fresh run read after 100000
  calls = deferredFetch();
  const b = g.gitStatusLoad("/r", "s1");
  calls[0].resolve(ok([]));
  await b;
  assert.equal(g.gitStatusAt(), 100000);
});
