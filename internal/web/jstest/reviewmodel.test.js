"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  REVIEWED_KEY, reviewFp, reviewedState, reviewedCount, nextChange, pasteGate, saveReviewed, loadReviewed,
} = require("../static/reviewmodel.js");

function store(init) {
  const m = new Map(Object.entries(init || {}));
  return { m, getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); } };
}

const row = (o) => Object.assign({ path: "/r/a.go", name: "a.go", dir: "", code: "M", add: 3, del: 1, mine: true, folder: false, provisional: false }, o);
const markOf = (r, at) => ({ path: r.path, at, fp: reviewFp(r) });

test("reviewFp is code:add:del and stable for a row with no counts", () => {
  assert.strictEqual(reviewFp(row()), "M:3:1");
  assert.strictEqual(reviewFp(row({ code: "U", add: null, del: null })), "U::");
  assert.strictEqual(reviewFp(row({ code: "U", add: undefined, del: undefined })), "U::");
  assert.strictEqual(reviewFp(row({ add: 0, del: 0 })), "M:0:0");
});

test("reviewedState clears on a later agent edit or new counts", () => {
  const r = row();
  const m = markOf(r, 1000);
  assert.strictEqual(reviewedState(m, r, []), true);
  assert.strictEqual(reviewedState(undefined, r, []), false);
  assert.strictEqual(reviewedState(null, r, []), false);
  // New counts, or a new letter, since the mark.
  assert.strictEqual(reviewedState(m, row({ add: 4 }), []), false);
  assert.strictEqual(reviewedState(m, row({ del: 2 }), []), false);
  assert.strictEqual(reviewedState(m, row({ code: "A" }), []), false);
  // Activity ts is unix ms, like mark.at (Date.now()): no conversion. 1001 is after 1000, 1000 is not, 1 (seconds-looking) is not.
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 1001 }]), false);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 1000 }]), true);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 1 }]), true);
  const real = Date.now();
  assert.strictEqual(reviewedState(markOf(r, real), r, [{ path: "/r/a.go", ts: real - 5000 }]), true);
  assert.strictEqual(reviewedState(markOf(r, real), r, [{ path: "/r/a.go", ts: real + 5000 }]), false);
  // Another file's edit, an entry with no ts, and a missing or odd files list do not clear it.
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/b.go", ts: 9999 }]), true);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go" }, null]), true);
  assert.strictEqual(reviewedState(m, r, undefined), true);
  assert.strictEqual(reviewedState(m, r, null), true);
  // A mark for another path is not this row's.
  assert.strictEqual(reviewedState({ path: "/r/z.go", at: 1000, fp: "M:3:1" }, r, []), false);
  // A row with no counts (untracked) keeps its mark until its letter or counts change.
  const u = row({ code: "U", add: null, del: null });
  assert.strictEqual(reviewedState(markOf(u, 1000), u, []), true);
  assert.strictEqual(reviewedState(markOf(u, 1000), row({ code: "U", add: 5, del: 0 }), []), false);
});

test("reviewedCount", () => {
  const a = row({ path: "/r/a.go" });
  const b = row({ path: "/r/b.go", add: 2, del: 0 });
  const c = row({ path: "/r/c.go", code: "U", add: null, del: null });
  const marks = { "/r/a.go": markOf(a, 1000), "/r/b.go": markOf(b, 1000), "/r/gone.go": { path: "/r/gone.go", at: 1, fp: "M:1:1" } };
  assert.deepStrictEqual(reviewedCount([a, b, c], marks, []), { done: 2, total: 3 });
  // b was edited again, so it is not done; a mark with no row counts for nothing.
  assert.deepStrictEqual(reviewedCount([a, b, c], marks, [{ path: "/r/b.go", ts: 2000 }]), { done: 1, total: 3 });
  assert.deepStrictEqual(reviewedCount([a, b, c], {}, []), { done: 0, total: 3 });
  assert.deepStrictEqual(reviewedCount([], marks, []), { done: 0, total: 0 });
  assert.deepStrictEqual(reviewedCount([a], undefined, undefined), { done: 0, total: 1 });
});

test("nextChange forward, backward, past the end", () => {
  // Starts are 3, 10, 20 (the del at 20 sits at one line).
  const lc = [{ kind: "add", from: 3, to: 5 }, { kind: "mod", from: 10, to: 12 }, { kind: "del", from: 20, to: 20 }];
  // Forward: the first change whose start is after the cursor line.
  assert.deepStrictEqual(nextChange(lc, 1, 1), { index: 0, line: 3 });
  assert.deepStrictEqual(nextChange(lc, 3, 1), { index: 1, line: 10 });
  assert.deepStrictEqual(nextChange(lc, 4, 1), { index: 1, line: 10 });
  assert.deepStrictEqual(nextChange(lc, 11, 1), { index: 2, line: 20 });
  assert.strictEqual(nextChange(lc, 20, 1), null);
  assert.strictEqual(nextChange(lc, 99, 1), null);
  // Backward: the last change whose start is strictly before the cursor line, so inside a change it goes to that change's start.
  assert.deepStrictEqual(nextChange(lc, 99, -1), { index: 2, line: 20 });
  assert.deepStrictEqual(nextChange(lc, 20, -1), { index: 1, line: 10 });
  assert.deepStrictEqual(nextChange(lc, 11, -1), { index: 1, line: 10 });
  assert.deepStrictEqual(nextChange(lc, 10, -1), { index: 0, line: 3 });
  assert.deepStrictEqual(nextChange(lc, 4, -1), { index: 0, line: 3 });
  assert.strictEqual(nextChange(lc, 3, -1), null);
  assert.strictEqual(nextChange(lc, 1, -1), null);
  // No changes, or odd input: null, never a throw.
  assert.strictEqual(nextChange([], 5, 1), null);
  assert.strictEqual(nextChange([], 5, -1), null);
  assert.strictEqual(nextChange(undefined, 5, 1), null);
  assert.strictEqual(nextChange(lc, NaN, 1), null);
  assert.strictEqual(nextChange(lc, 5, 0), null);
});

const term = (bp) => ({ term: { modes: { bracketedPasteMode: bp } } });

test("pasteGate idle ok", () => {
  assert.deepStrictEqual(pasteGate({ name: "api", activity: "idle" }, term(true)), { ok: true, label: "Paste to api", why: "" });
});

test("pasteGate working, waiting, need, no bracketed paste, no term", () => {
  const s = (o) => Object.assign({ name: "api", activity: "idle" }, o);
  assert.deepStrictEqual(pasteGate(s({ activity: "working" }), term(true)), { ok: false, label: "Paste to api", why: "api is working; notes wait here" });
  assert.deepStrictEqual(pasteGate(s({ activity: "waiting" }), term(true)), { ok: false, label: "Paste to api", why: "api is waiting on you; answer it first" });
  // Any need, even on an idle session, is the same refusal.
  for (const need of ["ask", "sso", "store_locked", "turn"]) {
    assert.deepStrictEqual(pasteGate(s({ need }), term(true)), { ok: false, label: "Paste to api", why: "api is waiting on you; answer it first" });
  }
  assert.deepStrictEqual(pasteGate(s(), term(false)), { ok: false, label: "Paste to api", why: "This program does not accept pasted blocks" });
  assert.strictEqual(pasteGate(s(), { term: { modes: {} } }).ok, false);
  assert.strictEqual(pasteGate(s(), { term: {} }).why, "This program does not accept pasted blocks");
  assert.strictEqual(pasteGate(s(), {}).why, "Not connected");
  assert.deepStrictEqual(pasteGate(s(), null), { ok: false, label: "Paste to api", why: "Not connected" });
  assert.strictEqual(pasteGate(s(), undefined).why, "Not connected");
  // Order when several apply: no term, working, waiting or need, bracketed paste off.
  assert.strictEqual(pasteGate(s({ activity: "working", need: "ask" }), null).why, "Not connected");
  assert.strictEqual(pasteGate(s({ activity: "working", need: "ask" }), term(false)).why, "api is working; notes wait here");
  assert.strictEqual(pasteGate(s({ activity: "waiting" }), term(false)).why, "api is waiting on you; answer it first");
  assert.strictEqual(pasteGate(s({ need: "ask" }), term(false)).why, "api is waiting on you; answer it first");
  // Only the string true passes; a truthy non-boolean does not.
  assert.strictEqual(pasteGate(s(), term(1)).ok, false);
  // A session that is over (done, crashed, exited) cannot take a paste either.
  for (const activity of ["done", "crashed", "exited", undefined]) {
    assert.strictEqual(pasteGate(s({ activity }), term(true)).ok, false);
    assert.strictEqual(pasteGate(s({ activity }), term(true)).why, "Not connected");
  }
  // A missing session.
  assert.deepStrictEqual(pasteGate(null, term(true)), { ok: false, label: "Paste to session", why: "Not connected" });
  assert.deepStrictEqual(pasteGate(undefined, null), { ok: false, label: "Paste to session", why: "Not connected" });
});

test("saveReviewed and loadReviewed round-trip, drop dead sessions, ignore a bad record", () => {
  assert.strictEqual(REVIEWED_KEY, "agent-box:reviewed");
  const m1 = { "/r/a.go": { path: "/r/a.go", at: 5, fp: "M:1:1" } };
  const st = store();
  saveReviewed(st, "s1", m1);
  saveReviewed(st, "s2", { "/r/b.go": { path: "/r/b.go", at: 6, fp: "A:2:0" } });
  assert.deepStrictEqual(JSON.parse(st.m.get(REVIEWED_KEY)).v, 1);
  assert.deepStrictEqual([...loadReviewed(st, ["s1", "s2"])], [["s1", m1], ["s2", { "/r/b.go": { path: "/r/b.go", at: 6, fp: "A:2:0" } }]]);
  // s2 is gone: not returned, and removed from the stored record.
  assert.deepStrictEqual([...loadReviewed(st, new Set(["s1"])).keys()], ["s1"]);
  assert.deepStrictEqual(Object.keys(JSON.parse(st.m.get(REVIEWED_KEY)).sessions), ["s1"]);
  // An empty marks object removes the entry.
  saveReviewed(st, "s1", {});
  assert.deepStrictEqual(JSON.parse(st.m.get(REVIEWED_KEY)).sessions, {});
  // A malformed mark is dropped, the good one kept.
  const bad = store({ [REVIEWED_KEY]: JSON.stringify({ v: 1, sessions: { s1: { marks: {
    "/r/a.go": { path: "/r/a.go", at: 5, fp: "M:1:1" }, "/r/b.go": { path: "/r/b.go", at: "x", fp: "M" }, "/r/c.go": 7,
    "/r/d.go": { path: "/r/other.go", at: 5, fp: "M:1:1" } } } } }) });
  assert.strictEqual(loadReviewed(bad, ["s1"]).get("s1")["/r/a.go"].fp, "M:1:1");
  assert.deepStrictEqual(Object.keys(loadReviewed(bad, ["s1"]).get("s1")), ["/r/a.go"]);
  // Missing, corrupt, wrong-version records read as nothing; a throwing storage never throws.
  assert.strictEqual(loadReviewed(store(), ["s1"]).size, 0);
  assert.strictEqual(loadReviewed(store({ [REVIEWED_KEY]: "{nope" }), ["s1"]).size, 0);
  assert.strictEqual(loadReviewed(store({ [REVIEWED_KEY]: JSON.stringify({ v: 2, sessions: { s1: { marks: m1 } } }) }), ["s1"]).size, 0);
  const boom = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("full"); } };
  saveReviewed(boom, "s1", m1);
  assert.strictEqual(loadReviewed(boom, ["s1"]).size, 0);
});
