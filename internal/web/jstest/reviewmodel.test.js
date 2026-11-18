"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  REVIEWED_KEY, reviewFp, reviewMarkFor, reviewedState, reviewedCount, nextChange, pasteGate, dirtyNotedNames, pasteBarState, saveReviewed, loadReviewed,
  reviewRows, reviewNeighbour, reviewFirstOpen, reviewLineOf, reviewChangePos, notesPendingByPath, swipeClip, onionOpacity,
} = require("../static/reviewmodel.js");

function store(init) {
  const m = new Map(Object.entries(init || {}));
  return { m, getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); } };
}

const row = (o) => Object.assign({ path: "/r/a.go", name: "a.go", dir: "", code: "M", add: 3, del: 1, mine: true, folder: false, provisional: false }, o);
const markOf = (r, files, at) => reviewMarkFor(r, files || [], at === undefined ? 1000 : at);

test("reviewFp is code:add:del and stable for a row with no counts", () => {
  assert.strictEqual(reviewFp(row()), "M:3:1");
  assert.strictEqual(reviewFp(row({ code: "U", add: null, del: null })), "U::");
  assert.strictEqual(reviewFp(row({ code: "U", add: undefined, del: undefined })), "U::");
  assert.strictEqual(reviewFp(row({ add: 0, del: 0 })), "M:0:0");
});

test("reviewMarkFor takes the server baseline from Activity, not the browser clock", () => {
  const r = row();
  assert.deepStrictEqual(reviewMarkFor(r, [], 1234), { path: "/r/a.go", at: 1234, fp: "M:3:1", seen: 0 });
  assert.deepStrictEqual(reviewMarkFor(r, [{ path: "/r/a.go", ts: 777 }], 1234), { path: "/r/a.go", at: 1234, fp: "M:3:1", seen: 777 });
  // Other files, entries with no ts, null entries and a missing list give seen 0; a repeated path gives its newest ts.
  assert.strictEqual(reviewMarkFor(r, [{ path: "/r/b.go", ts: 9 }, { path: "/r/a.go" }, null], 1).seen, 0);
  assert.strictEqual(reviewMarkFor(r, undefined, 1).seen, 0);
  assert.strictEqual(reviewMarkFor(r, [{ path: "/r/a.go", ts: 5 }, { path: "/r/a.go", ts: 8 }, { path: "/r/a.go", ts: 6 }], 1).seen, 8);
});

test("reviewedState clears on a later agent edit or new counts", () => {
  const r = row();
  const files0 = [{ path: "/r/a.go", ts: 5000 }];
  const m = markOf(r, files0);
  assert.strictEqual(reviewedState(m, r, files0), true);
  assert.strictEqual(reviewedState(m, r, []), true);
  assert.strictEqual(reviewedState(undefined, r, files0), false);
  assert.strictEqual(reviewedState(null, r, files0), false);
  // New counts, or a new letter, since the mark.
  assert.strictEqual(reviewedState(m, row({ add: 4 }), files0), false);
  assert.strictEqual(reviewedState(m, row({ del: 2 }), files0), false);
  assert.strictEqual(reviewedState(m, row({ code: "A" }), files0), false);
  // An edit with ts > seen clears it, even with the same counts; the same ts or an older one does not. Activity ts is unix ms.
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 5001 }]), false);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 5000 }]), true);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go", ts: 4000 }]), true);
  // The path was not in Activity's list when marked (seen 0): its first entry clears the mark.
  const m0 = markOf(r, []);
  assert.strictEqual(m0.seen, 0);
  assert.strictEqual(reviewedState(m0, r, []), true);
  assert.strictEqual(reviewedState(m0, r, [{ path: "/r/a.go", ts: 1 }]), false);
  // The browser's clock does not matter: far ahead or far behind the server's ts, the mark answers on Activity ts alone.
  const server = 1700000000000;
  for (const now of [server + 3600000, server - 3600000, 0, Date.now()]) {
    const sm = markOf(r, [{ path: "/r/a.go", ts: server }], now);
    assert.strictEqual(reviewedState(sm, r, [{ path: "/r/a.go", ts: server }]), true, "marked file the agent just touched sticks, now=" + now);
    assert.strictEqual(reviewedState(sm, r, [{ path: "/r/a.go", ts: server + 1 }]), false, "re-edit clears, now=" + now);
  }
  // Another file's edit, an entry with no ts, and a missing or odd files list do not clear it.
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/b.go", ts: 9999999 }]), true);
  assert.strictEqual(reviewedState(m, r, [{ path: "/r/a.go" }, null]), true);
  assert.strictEqual(reviewedState(m, r, undefined), true);
  assert.strictEqual(reviewedState(m, r, null), true);
  // A mark for another path is not this row's; a mark with no server baseline is not trusted.
  assert.strictEqual(reviewedState({ path: "/r/z.go", at: 1000, fp: "M:3:1", seen: 0 }, r, []), false);
  assert.strictEqual(reviewedState({ path: "/r/a.go", at: 1000, fp: "M:3:1" }, r, []), false);
  // A row with no counts (untracked) keeps its mark until its letter or counts change.
  const u = row({ code: "U", add: null, del: null });
  assert.strictEqual(reviewedState(markOf(u), u, []), true);
  assert.strictEqual(reviewedState(markOf(u), row({ code: "U", add: 5, del: 0 }), []), false);
});

test("reviewedCount", () => {
  const a = row({ path: "/r/a.go" });
  const b = row({ path: "/r/b.go", add: 2, del: 0 });
  const c = row({ path: "/r/c.go", code: "U", add: null, del: null });
  const marks = { "/r/a.go": markOf(a), "/r/b.go": markOf(b), "/r/gone.go": { path: "/r/gone.go", at: 1, fp: "M:1:1", seen: 0 } };
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
  // Null or non-object entries are skipped; the index is still the position in the list.
  const holey = [null, { kind: "add", from: 4, to: 4 }, undefined, { kind: "mod", from: 9, to: 9 }];
  assert.deepStrictEqual(nextChange(holey, 1, 1), { index: 1, line: 4 });
  assert.deepStrictEqual(nextChange(holey, 4, 1), { index: 3, line: 9 });
  assert.deepStrictEqual(nextChange(holey, 9, -1), { index: 1, line: 4 });
  assert.strictEqual(nextChange([null], 1, 1), null);
  // A trailing del past the end carries lastLine + 1; nextChange reports it as is, the caller clamps.
  assert.deepStrictEqual(nextChange([{ kind: "del", from: 11, to: 11 }], 3, 1), { index: 0, line: 11 });
});

const term = (bp) => ({ term: { modes: { bracketedPasteMode: bp } } });

test("pasteGate idle ok", () => {
  assert.deepStrictEqual(pasteGate({ name: "api", activity: "idle" }, term(true)), { ok: true, label: "Paste to api", why: "" });
});

test("pasteGate working, waiting, need, no bracketed paste, no term", () => {
  const s = (o) => Object.assign({ name: "api", activity: "idle" }, o);
  assert.deepStrictEqual(pasteGate(s({ activity: "working" }), term(true)), { ok: false, label: "Paste to api", why: "api is working; notes wait here" });
  assert.deepStrictEqual(pasteGate(s({ activity: "waiting" }), term(true)), { ok: false, label: "Paste to api", why: "api is waiting on you; answer it first" });
  // Any need but a finished turn, even on an idle session, is the same refusal.
  for (const need of ["ask", "sso", "store_locked", "other"]) {
    assert.deepStrictEqual(pasteGate(s({ need, activity: "waiting" }), term(true)), { ok: false, label: "Paste to api", why: "api is waiting on you; answer it first" });
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
  // Only boolean true passes; a truthy non-boolean does not.
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

test("pasteGate lets a finished turn through (R13): waiting with need turn, and idle with need turn", () => {
  const s = (o) => Object.assign({ name: "api", activity: "waiting", need: "turn" }, o);
  assert.deepStrictEqual(pasteGate(s(), term(true)), { ok: true, label: "Paste to api", why: "" });
  assert.strictEqual(pasteGate(s({ activity: "idle" }), term(true)).ok, true);
  // The other refusals still apply to it.
  assert.strictEqual(pasteGate(s({ activity: "working" }), term(true)).why, "api is working; notes wait here");
  assert.strictEqual(pasteGate(s(), term(false)).why, "This program does not accept pasted blocks");
  assert.strictEqual(pasteGate(s(), null).why, "Not connected");
  for (const activity of ["done", "crashed", "exited", undefined]) assert.strictEqual(pasteGate(s({ activity }), term(true)).why, "Not connected");
  // Waiting with no need, and any other need, stay refused.
  assert.strictEqual(pasteGate(s({ need: "" }), term(true)).why, "api is waiting on you; answer it first");
  assert.strictEqual(pasteGate(s({ need: undefined }), term(true)).why, "api is waiting on you; answer it first");
  assert.strictEqual(pasteGate(s({ need: "ask" }), term(true)).why, "api is waiting on you; answer it first");
});

const pn = (path, status) => ({ id: path + status, path, status });

test("dirtyNotedNames: only dirty tabs whose file has PENDING notes, each name once", () => {
  const list = [pn("/a.go", "pending"), pn("/a.go", "pending"), pn("/b.go", "sent"), pn("/c.go", "pending")];
  const tabs = [{ path: "/a.go", name: "a.go", dirty: true }, { path: "/b.go", name: "b.go", dirty: true }, { path: "/c.go", name: "c.go", dirty: false },
    { path: "/d.go", name: "d.go", dirty: true }, { path: "/a.go", name: "a.go", dirty: true }];
  assert.deepStrictEqual(dirtyNotedNames(tabs, list), ["a.go"]);
  assert.deepStrictEqual(dirtyNotedNames([], list), []);
  assert.deepStrictEqual(dirtyNotedNames(null, null), []);
});

test("pasteBarState: shape, counts, the gate's sentence, the socket, then the unsaved-file rule (R12)", () => {
  const sess = { name: "api", activity: "idle" };
  const list = [pn("/a.go", "pending"), pn("/a.go", "sent")];
  const base = { session: sess, term: term(true), connected: true, list, tabs: [] };
  assert.deepStrictEqual(pasteBarState(base), { shape: "full", pending: 1, sent: 1, label: "Paste to api", ok: true, why: "" });
  assert.strictEqual(pasteBarState(Object.assign({}, base, { list: [pn("/a.go", "sent")] })).shape, "sent");
  assert.strictEqual(pasteBarState(Object.assign({}, base, { list: [] })).shape, "none");
  assert.strictEqual(pasteBarState(Object.assign({}, base, { session: { name: "api", activity: "working" } })).why, "api is working; notes wait here");
  assert.strictEqual(pasteBarState(Object.assign({}, base, { connected: false })).why, "Not connected");
  assert.strictEqual(pasteBarState(Object.assign({}, base, { connected: undefined })).ok, false, "only a strict true connects");
  const dirty = Object.assign({}, base, { tabs: [{ path: "/a.go", name: "a.go", dirty: true }] });
  assert.deepStrictEqual(pasteBarState(dirty), { shape: "full", pending: 1, sent: 1, label: "Paste to api", ok: false, why: "Save a.go first: the agent reads the file on disk" });
  // The gate's own refusal wins over the unsaved one.
  assert.strictEqual(pasteBarState(Object.assign({}, dirty, { session: { name: "api", activity: "working" } })).why, "api is working; notes wait here");
  assert.strictEqual(pasteBarState({}).ok, false);
});

test("saveReviewed and loadReviewed round-trip, drop dead sessions, ignore a bad record", () => {
  assert.strictEqual(REVIEWED_KEY, "agent-box:reviewed");
  const m1 = { "/r/a.go": { path: "/r/a.go", at: 5, fp: "M:1:1", seen: 4 } };
  const st = store();
  saveReviewed(st, "s1", m1);
  saveReviewed(st, "s2", { "/r/b.go": { path: "/r/b.go", at: 6, fp: "A:2:0", seen: 0 } });
  assert.deepStrictEqual(JSON.parse(st.m.get(REVIEWED_KEY)).v, 1);
  assert.deepStrictEqual([...loadReviewed(st, ["s1", "s2"])], [["s1", m1], ["s2", { "/r/b.go": { path: "/r/b.go", at: 6, fp: "A:2:0", seen: 0 } }]]);
  // s2 is gone: not returned, and removed from the stored record.
  assert.deepStrictEqual([...loadReviewed(st, new Set(["s1"])).keys()], ["s1"]);
  assert.deepStrictEqual(Object.keys(JSON.parse(st.m.get(REVIEWED_KEY)).sessions), ["s1"]);
  // An empty marks object removes the entry.
  saveReviewed(st, "s1", {});
  assert.deepStrictEqual(JSON.parse(st.m.get(REVIEWED_KEY)).sessions, {});
  // A malformed mark is dropped, the good one kept.
  const bad = store({ [REVIEWED_KEY]: JSON.stringify({ v: 1, sessions: { s1: { marks: {
    "/r/a.go": { path: "/r/a.go", at: 5, fp: "M:1:1", seen: 4 }, "/r/b.go": { path: "/r/b.go", at: "x", fp: "M", seen: 0 }, "/r/c.go": 7,
    "/r/d.go": { path: "/r/other.go", at: 5, fp: "M:1:1", seen: 4 },
    "/r/e.go": { path: "/r/e.go", at: 5, fp: "M:1:1" }, "/r/f.go": { path: "/r/f.go", at: 5, fp: "M:1:1", seen: -1 } } } } }) });
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

const rows3 = [row({ path: "/r/a.go" }), row({ path: "/r/dir", folder: true }), row({ path: "/r/p.go", provisional: true, code: "" }), row({ path: "/r/b.go" }), row({ path: "/r/c.go", code: "D" })];

test("reviewRows keeps the files git answered for, in the section's order", () => {
  assert.deepStrictEqual(reviewRows(rows3).map((r) => r.path), ["/r/a.go", "/r/b.go", "/r/c.go"]);
  assert.deepStrictEqual(reviewRows(null), []);
  assert.deepStrictEqual(reviewRows([null, 3, row()]).length, 1);
});

test("reviewNeighbour steps over folders and provisional rows and stops at the ends", () => {
  assert.strictEqual(reviewNeighbour(rows3, "/r/a.go", 1).path, "/r/b.go");
  assert.strictEqual(reviewNeighbour(rows3, "/r/b.go", 1).path, "/r/c.go");
  assert.strictEqual(reviewNeighbour(rows3, "/r/c.go", 1), null);
  assert.strictEqual(reviewNeighbour(rows3, "/r/c.go", -1).path, "/r/b.go");
  assert.strictEqual(reviewNeighbour(rows3, "/r/a.go", -1), null);
  assert.strictEqual(reviewNeighbour(rows3, "/r/none.go", 1), null, "an unchanged file has no neighbour");
  assert.strictEqual(reviewNeighbour(rows3, "/r/a.go", 0), null);
  assert.strictEqual(reviewNeighbour(undefined, "/r/a.go", 1), null);
});

test("reviewFirstOpen is the first row whose mark does not stand, else the first row", () => {
  const files = [{ path: "/r/a.go", ts: 10 }, { path: "/r/b.go", ts: 10 }];
  const marks = { "/r/a.go": markOf(rows3[0], files) };
  assert.strictEqual(reviewFirstOpen(rows3, marks, files).path, "/r/b.go");
  // The agent edited a.go since: its mark no longer stands, so it is the first again.
  assert.strictEqual(reviewFirstOpen(rows3, marks, [{ path: "/r/a.go", ts: 11 }]).path, "/r/a.go");
  const all = { "/r/a.go": markOf(rows3[0], files), "/r/b.go": markOf(rows3[3], files), "/r/c.go": markOf(rows3[4], files) };
  assert.strictEqual(reviewFirstOpen(rows3, all, files).path, "/r/a.go", "all reviewed: Review still opens the first");
  assert.strictEqual(reviewFirstOpen(rows3, null, null).path, "/r/a.go");
  assert.strictEqual(reviewFirstOpen([row({ folder: true })], {}, []), null);
});

test("reviewLineOf clamps a change into the buffer: a removal at the end carries lastLine + 1, an emptied file has one line", () => {
  assert.strictEqual(reviewLineOf({ kind: "mod", from: 5, to: 6 }, 10), 5);
  assert.strictEqual(reviewLineOf({ kind: "del", from: 11, to: 11 }, 10), 10);
  assert.strictEqual(reviewLineOf({ kind: "del", from: 1, to: 1 }, 1), 1);
  assert.strictEqual(reviewLineOf({ kind: "del", from: 0, to: 0 }, 5), 1);
  assert.strictEqual(reviewLineOf({ kind: "del", from: 3, to: 3 }, undefined), 1);
});

test("reviewChangePos: N of M from the last F7 landing while the cursor is on it, else from the cursor", () => {
  const lc = [{ kind: "mod", from: 4, to: 5 }, { kind: "add", from: 9, to: 9 }, { kind: "del", from: 21, to: 21 }];
  assert.deepStrictEqual(reviewChangePos(lc, 4, 0, 20), { n: 1, m: 3, text: "Change 1 of 3" });
  assert.strictEqual(reviewChangePos(lc, 9, 1, 20).text, "Change 2 of 3");
  assert.strictEqual(reviewChangePos(lc, 20, 2, 20).text, "Change 3 of 3", "the trailing removal is shown at the last line");
  // The cursor moved off the landing: counted from the cursor (changes that start at or before it).
  assert.strictEqual(reviewChangePos(lc, 7, 1, 20).text, "Change 1 of 3");
  assert.strictEqual(reviewChangePos(lc, 12, null, 20).text, "Change 2 of 3");
  assert.strictEqual(reviewChangePos(lc, 1, null, 20).text, "3 changes", "before the first change");
  assert.strictEqual(reviewChangePos([lc[0]], 1, null, 20).text, "1 change");
  assert.deepStrictEqual(reviewChangePos([], 1, null, 20), { n: 0, m: 0, text: "No changes" });
  assert.strictEqual(reviewChangePos(undefined, 1, 0, 20).text, "No changes");
  assert.strictEqual(reviewChangePos(lc, 4, 7, 20).text, "Change 1 of 3", "a stale index is ignored");
  // A deleted file: one removal in an empty buffer.
  assert.strictEqual(reviewChangePos([{ kind: "del", from: 1, to: 1 }], 1, 0, 1).text, "Change 1 of 1");
});

test("notesPendingByPath counts pending notes per file and nothing else", () => {
  const n = (path, status) => ({ id: "x", path, status });
  assert.deepStrictEqual(notesPendingByPath([n("/r/a.go", "pending"), n("/r/a.go", "pending"), n("/r/b.go", "pending"), n("/r/a.go", "sent"), null, 4, { status: "pending" }]), { "/r/a.go": 2, "/r/b.go": 1 });
  assert.deepStrictEqual(notesPendingByPath(undefined), {});
  assert.deepStrictEqual(notesPendingByPath({ a: 1 }), {});
});

test("swipeClip is the divider's place as a percent of the width, clamped to 0..100", () => {
  assert.strictEqual(swipeClip(0, 200), 0);
  assert.strictEqual(swipeClip(50, 200), 25);
  assert.strictEqual(swipeClip(200, 200), 100);
  assert.strictEqual(swipeClip(-30, 200), 0, "left of the image");
  assert.strictEqual(swipeClip(260, 200), 100, "right of the image");
  assert.strictEqual(swipeClip(-Infinity, 200), 0);
  assert.strictEqual(swipeClip(Infinity, 200), 100);
});

test("swipeClip with no usable width or position is the middle, never NaN", () => {
  assert.strictEqual(swipeClip(10, 0), 50, "zero width");
  assert.strictEqual(swipeClip(10, -5), 50, "negative width");
  assert.strictEqual(swipeClip(10, NaN), 50);
  assert.strictEqual(swipeClip(10, Infinity), 50);
  assert.strictEqual(swipeClip(NaN, 200), 50);
  assert.strictEqual(swipeClip(undefined, 200), 50);
  assert.strictEqual(swipeClip(null, 200), 50);
  assert.strictEqual(swipeClip("40", 200), 20, "a numeric string counts");
  assert.strictEqual(swipeClip("x", 200), 50);
});

test("onionOpacity maps the slider's 0..100 to 0..1, clamped", () => {
  assert.strictEqual(onionOpacity(0), 0);
  assert.strictEqual(onionOpacity(50), 0.5);
  assert.strictEqual(onionOpacity(100), 1);
  assert.strictEqual(onionOpacity(-10), 0);
  assert.strictEqual(onionOpacity(250), 1);
  assert.strictEqual(onionOpacity(-Infinity), 0);
  assert.strictEqual(onionOpacity(Infinity), 1);
  assert.strictEqual(onionOpacity("25"), 0.25, "an input's value is a string");
});

test("onionOpacity of nothing usable is the half-way blend, never NaN", () => {
  for (const v of [NaN, undefined, null, "", "  ", "abc", {}, []]) assert.strictEqual(onionOpacity(v), 0.5, String(v));
});

// --- the All changes tab (reviewall.js) ---
const RA = require("../static/reviewmodel.js");

const rowOf = (p, extra) => Object.assign({ path: p, name: p.split("/").pop(), dir: "", code: "M", add: 1, del: 0, folder: false, provisional: false }, extra);

test("reviewAllRows: files only, in order, the first 100, and how many were left out", () => {
  const rows = [rowOf("/w/a"), rowOf("/w/dir", { folder: true }), rowOf("/w/b", { provisional: true }), rowOf("/w/c")];
  const r = RA.reviewAllRows(rows);
  assert.deepStrictEqual(r.rows.map((x) => x.path), ["/w/a", "/w/c"], "a folder and a provisional row have no diff to read");
  assert.strictEqual(r.more, 0);
  const many = Array.from({ length: 130 }, (_, i) => rowOf("/w/f" + i));
  const c = RA.reviewAllRows(many);
  assert.strictEqual(c.rows.length, 100);
  assert.strictEqual(c.rows[0].path, "/w/f0");
  assert.strictEqual(c.rows[99].path, "/w/f99");
  assert.strictEqual(c.more, 30);
  assert.strictEqual(RA.reviewAllRows(Array.from({ length: 100 }, (_, i) => rowOf("/w/f" + i))).more, 0, "exactly 100 is all of them");
  assert.deepStrictEqual(RA.reviewAllRows(null), { rows: [], more: 0 });
});

test("reviewAllIndex maps a path to its section, -1 for a folder, a path past the cap or nothing", () => {
  const shown = RA.reviewAllRows(Array.from({ length: 105 }, (_, i) => rowOf("/w/f" + i))).rows;
  assert.strictEqual(RA.reviewAllIndex(shown, "/w/f0"), 0);
  assert.strictEqual(RA.reviewAllIndex(shown, "/w/f99"), 99);
  assert.strictEqual(RA.reviewAllIndex(shown, "/w/f100"), -1, "past the cap");
  assert.strictEqual(RA.reviewAllIndex(shown, "/w/nope"), -1);
  assert.strictEqual(RA.reviewAllIndex(null, "/w/f0"), -1);
});

test("reviewAllHeight: content height, at most 1200, never negative or NaN", () => {
  assert.strictEqual(RA.reviewAllHeight(340.4), 340);
  assert.strictEqual(RA.reviewAllHeight(1200), 1200);
  assert.strictEqual(RA.reviewAllHeight(5000), 1200);
  assert.strictEqual(RA.reviewAllHeight(-3), 0);
  for (const v of [NaN, undefined, "x", Infinity]) assert.strictEqual(RA.reviewAllHeight(v), 0, String(v));
});

test("reviewAllPlan: a near section without an editor gets one; an editor past two screens goes; the rest is left alone", () => {
  const order = ["a", "b", "c", "d", "e", "f"];
  // Scrolled to c..d: near = c,d ; far (two screens) = b,c,d,e. Live from before: a, b, c.
  const p = RA.reviewAllPlan(order, new Set(["a", "b", "c"]), new Set(["c", "d"]), new Set(["b", "c", "d", "e"]));
  assert.deepStrictEqual(p.create, ["d"]);
  assert.deepStrictEqual(p.dispose, ["a"], "b is between one and two screens: it keeps its editor");
  // Nothing moved: nothing to do (no per-scroll work).
  assert.deepStrictEqual(RA.reviewAllPlan(order, ["c", "d"], ["c", "d"], ["b", "c", "d", "e"]), { create: [], dispose: [] });
  // Far away and back: everything is made again.
  const away = RA.reviewAllPlan(order, new Set(["a", "b"]), new Set(["e", "f"]), new Set(["d", "e", "f"]));
  assert.deepStrictEqual(away, { create: ["e", "f"], dispose: ["a", "b"] });
  const back = RA.reviewAllPlan(order, new Set(["e", "f"]), new Set(["a", "b"]), new Set(["a", "b", "c"]));
  assert.deepStrictEqual(back, { create: ["a", "b"], dispose: ["e", "f"] });
  // A path that is no longer a section is not made or kept by the plan (the caller disposes what it no longer lists).
  assert.deepStrictEqual(RA.reviewAllPlan(["a"], new Set(["z"]), new Set(["z"]), new Set(["z"])), { create: [], dispose: [] });
  // Nothing visible (a hidden tab): no section is near, and the ones past far go.
  assert.deepStrictEqual(RA.reviewAllPlan(order, new Set(["a"]), new Set(), new Set()), { create: [], dispose: ["a"] });
});

test("reviewAllStepPlan: F7 walks the changes of a section, then the next section's, skipping unchanged ones", () => {
  const text = (...froms) => ({ kind: "text", lineCount: 50, changes: froms.map((f) => ({ kind: "mod", from: f, to: f })) });
  const secs = [text(5, 20), text(), text(7), text(9, 30)];
  // inside the first section
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 0, line: 5 }, 1), { idx: 0, at: "change", index: 1, line: 20 });
  // past its last: the next section that has a change (section 1 has none)
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 0, line: 20 }, 1), { idx: 2, at: "change", index: 0, line: 7 });
  // backwards from the start of section 2: the previous section's LAST change
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 2, line: 7 }, -1), { idx: 0, at: "change", index: 1, line: 20 });
  // forward into a section from "before its first line"
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 3, line: 0 }, 1), { idx: 3, at: "change", index: 0, line: 9 });
  // the ends
  assert.strictEqual(RA.reviewAllStepPlan(secs, { idx: 3, line: 30 }, 1), null);
  assert.strictEqual(RA.reviewAllStepPlan(secs, { idx: 0, line: 5 }, -1), null);
  // nothing selected yet (idx -1): forward is the first change
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: -1, line: 0 }, 1), { idx: 0, at: "change", index: 0, line: 5 });
  assert.strictEqual(RA.reviewAllStepPlan(secs, { idx: 0, line: 5 }, 0), null, "a step is 1 or -1");
  assert.strictEqual(RA.reviewAllStepPlan(null, { idx: 0, line: 5 }, 1), null);
});

test("reviewAllStepPlan: an image or a section with no diff yet is a stop to go to; a trailing removal is clamped into the buffer", () => {
  const secs = [
    { kind: "text", lineCount: 10, changes: [{ kind: "del", from: 11, to: 11 }] }, // a removal at the very end carries lastLine + 1
    { kind: "stop" },
    { kind: "unknown" },
    { kind: "text", lineCount: 4, changes: [{ kind: "add", from: 2, to: 3 }] },
  ];
  // The cursor sits on the clamped line (10): the removal is that change, not a later one, so F7 leaves the section.
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 0, line: 10 }, 1), { idx: 1, at: "section", land: "first" });
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 1, line: 0 }, 1), { idx: 2, at: "section", land: "first" });
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 2, line: 0 }, 1), { idx: 3, at: "change", index: 0, line: 2 });
  // backwards over the stops, landing on the LAST change of a section that has no diff yet
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 3, line: 2 }, -1), { idx: 2, at: "section", land: "last" });
  assert.deepStrictEqual(RA.reviewAllStepPlan(secs, { idx: 1, line: 0 }, -1), { idx: 0, at: "change", index: 0, line: 10 }, "the trailing removal is shown on line 10");
});
