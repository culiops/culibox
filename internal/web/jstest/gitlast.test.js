// internal/web/jstest/gitlast.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  GITLAST_KEY, GITLAST_ROOTS, GITLAST_ENTRIES, GITLAST_BYTES, gitLastParse, gitLastPut, gitLastSave,
} = require("../static/gitlast.js");

const status = (n, extra) => Object.assign({
  state: "ok", top: "/w", branch: "feat/x", head: "a".repeat(40), rootRel: ".",
  entries: Array.from({ length: n }, (_, i) => ({ p: "f" + i + ".go", x: "M" })),
}, extra || {});
const row = (o) => Object.assign({ path: "/w/a.go", name: "a.go", dir: "", code: "M", add: 3, del: 1, mine: true, folder: false, provisional: false }, o || {});
const view = (rows, extra) => Object.assign({ kind: "rows", scope: "branch", showScope: true, rows, more: 0, count: rows.length, branch: { from: "feat/x", to: "main", add: 3, del: 1 } }, extra || {});
const roundTrip = (m) => gitLastParse(gitLastSave(m));

test("the caps are the plan's values", () => {
  assert.equal(GITLAST_KEY, "agent-box:gitLast");
  assert.equal(GITLAST_ROOTS, 8);
  assert.equal(GITLAST_ENTRIES, 500);
  assert.equal(GITLAST_BYTES, 200 * 1024);
});

test("a status and a view round-trip exactly, and put returns a new map", () => {
  const m0 = new Map();
  const m1 = gitLastPut(m0, "/w", "status", status(2));
  const m2 = gitLastPut(m1, "/w", "view", view([row(), row({ path: "/w/b.go", name: "b.go", code: "U", add: null, del: null, mine: false })]));
  assert.notStrictEqual(m1, m0);
  assert.equal(m0.size, 0, "put writes nothing to its input");
  const back = roundTrip(m2).get("/w");
  assert.deepStrictEqual(back.status.entries, status(2).entries);
  assert.equal(back.status.head, "a".repeat(40));
  assert.equal(back.status.branch, "feat/x");
  assert.deepStrictEqual(back.view, view([row(), row({ path: "/w/b.go", name: "b.go", code: "U", add: null, del: null, mine: false })]));
});

test("a status over 500 entries is kept as no status (no marks), and a view keeps at most 200 rows", () => {
  const m = gitLastPut(new Map(), "/w", "status", status(GITLAST_ENTRIES + 1));
  assert.strictEqual(m.get("/w").status, null);
  assert.equal(gitLastPut(new Map(), "/w", "status", status(GITLAST_ENTRIES)).get("/w").status.entries.length, 500);
  const many = Array.from({ length: 201 }, (_, i) => row({ path: "/w/f" + i, name: "f" + i }));
  assert.strictEqual(gitLastPut(new Map(), "/w", "view", view(many)).get("/w").view, null);
});

test("only an ok or toomany status and a rows view are kept", () => {
  for (const bad of [null, 1, "x", [], { state: "unavailable", entries: [] }, { state: "notrepo", entries: [] }, { state: "ok" }, { state: "ok", rootRel: 1, entries: [] }]) {
    assert.strictEqual(gitLastPut(new Map(), "/w", "status", bad).get("/w").status, null, JSON.stringify(bad));
  }
  assert.deepStrictEqual(gitLastPut(new Map(), "/w", "status", { state: "toomany", rootRel: ".", entries: [] }).get("/w").status.entries, []);
  for (const bad of [null, { kind: "note", text: "x" }, view([row()], { scope: "x" }), view([row()], { more: -1 }), view([row()], { count: 1.5 }), view([row()], { branch: { from: 1, to: "m" } })]) {
    assert.strictEqual(gitLastPut(new Map(), "/w", "view", bad).get("/w").view, null, JSON.stringify(bad));
  }
});

test("hostile storage: a letter that is not a code drops the row or entry, wrong types drop the record, odd roots are dropped", () => {
  const raw = JSON.stringify([
    ["/w", {
      status: { state: "ok", rootRel: ".", entries: [{ p: "a.go", x: "M" }, { p: "b.go", x: "x onload" }, { p: 5, x: "M" }, { p: "c.go", x: "__proto__" }] },
      view: view([row(), row({ path: "/w/e", name: "e", code: "x onload" }), row({ path: "/w/f", name: "f", add: -1 }), row({ path: "/w/g", name: "g", mine: "yes" }), row({ path: "/w/h", name: "h", code: "" , provisional: true, add: null, del: null })]),
    }],
    ["__proto__", { status: status(1), view: null }],
    ["rel/x", { status: status(1), view: null }],
    ["/v", "not an object"],
    ["/u\u0000x", { status: status(1), view: null }],
  ]);
  const m = gitLastParse(raw);
  assert.deepStrictEqual([...m.keys()], ["/w"]);
  assert.deepStrictEqual(m.get("/w").status.entries, [{ p: "a.go", x: "M" }]);
  assert.deepStrictEqual(m.get("/w").view.rows.map((r) => r.name), ["a.go", "h"]);
  assert.strictEqual(Object.getPrototypeOf(m.get("/w")), Object.prototype);
  for (const junk of [null, "", "{", "[1,2]", '{"/w":{}}', "null", "true", JSON.stringify([["/w"]])]) {
    assert.equal(gitLastParse(junk).size, 0, String(junk));
  }
});

test("a huge stored text is refused fast, not parsed", () => {
  const big = JSON.stringify([["/w", { status: status(1), view: null }]]) + " ".repeat(5 * 1024 * 1024);
  const t0 = Date.now();
  assert.equal(gitLastParse(big).size, 0);
  assert.ok(Date.now() - t0 < 200, "took " + (Date.now() - t0) + " ms");
});

test("at most 8 roots, the most recently put last; save drops the oldest roots until it fits 200 KB", () => {
  let m = new Map();
  for (let i = 0; i < 9; i++) m = gitLastPut(m, "/r" + i, "status", status(1));
  assert.deepStrictEqual([...m.keys()], ["/r1", "/r2", "/r3", "/r4", "/r5", "/r6", "/r7", "/r8"]);
  m = gitLastPut(m, "/r1", "view", view([row()]));
  assert.equal([...m.keys()].pop(), "/r1", "a put moves its root last");
  // Each root ~60 KB: only the newest three fit.
  let big = new Map();
  for (let i = 0; i < 5; i++) big = gitLastPut(big, "/b" + i, "status", status(500, { rootRel: "x".repeat(10) }));
  for (const [k, e] of big) e.status.entries.forEach((x) => { x.p = x.p + "-".repeat(100); });
  const text = gitLastSave(big);
  assert.ok(text.length <= GITLAST_BYTES, "saved " + text.length);
  const kept = [...gitLastParse(text).keys()];
  assert.ok(kept.length >= 1 && kept.length < 5);
  assert.equal(kept[kept.length - 1], "/b4", "the newest root is kept");
});
