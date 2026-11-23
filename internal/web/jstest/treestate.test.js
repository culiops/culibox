"use strict";
const test = require("node:test");
const assert = require("node:assert");
const {
  loadExpanded, saveExpanded, addExpanded, removeExpanded, forgetSubtree,
  shouldAutoOpen, TREESTATE_KEY, TREESTATE_CAP,
} = require("../static/treestate.js");

// Same localStorage stand-in shape as layout.test.js: the two storage functions
// are the only impure ones, and their contract (corrupt value -> empty set,
// round-trip preserves order) is the whole persistence guarantee.
function fakeStorage() {
  const map = new Map();
  global.localStorage = {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: (k) => map.delete(k),
  };
  return map;
}

test("addExpanded appends and dedups; re-adding moves the path to the end (newest)", () => {
  const s = addExpanded(addExpanded(new Set(), "/a"), "/a/b");
  addExpanded(s, "/a"); // already present: moved to newest, no duplicate
  assert.deepStrictEqual([...s], ["/a/b", "/a"]);
});

test("addExpanded trims the oldest entry once over the cap", () => {
  const s = new Set();
  for (let i = 0; i < 505; i++) addExpanded(s, "/d/" + i);
  assert.strictEqual(s.size, 500);
  assert.ok(!s.has("/d/0"), "oldest dropped");
  assert.ok(!s.has("/d/4"), "five oldest dropped");
  assert.ok(s.has("/d/5"), "sixth oldest kept");
  assert.ok(s.has("/d/504"), "newest kept");
});

test("addExpanded is least-recently-used: re-adding moves a path to newest, so the cap evicts the least recently opened", () => {
  const s = new Set();
  for (let i = 0; i < TREESTATE_CAP; i++) addExpanded(s, "/d/" + i);
  addExpanded(s, "/d/0"); // opened again: now the newest
  assert.deepStrictEqual([...s].slice(-1), ["/d/0"]);
  addExpanded(s, "/d/new");
  assert.ok(s.has("/d/0"), "the folder opened again is kept");
  assert.ok(!s.has("/d/1"), "the least recently opened goes");
  assert.strictEqual(s.size, TREESTATE_CAP);
});

test("removeExpanded deletes only that path; descendants remain", () => {
  const s = new Set(["/a", "/a/b", "/a/b/c"]);
  removeExpanded(s, "/a");
  assert.deepStrictEqual([...s], ["/a/b", "/a/b/c"]);
});

test("forgetSubtree removes the path and everything strictly under it", () => {
  const s = new Set(["/a", "/a/b", "/a/b/c", "/a/bc", "/ab", "/x"]);
  forgetSubtree(s, "/a/b");
  // "/a/bc" is NOT under "/a/b" — the guard is the trailing slash, not a prefix.
  assert.deepStrictEqual([...s], ["/a", "/a/bc", "/ab", "/x"]);
});

test("shouldAutoOpen is exact membership", () => {
  const s = new Set(["/a/b"]);
  assert.strictEqual(shouldAutoOpen(s, "/a/b"), true);
  assert.strictEqual(shouldAutoOpen(s, "/a"), false);
  assert.strictEqual(shouldAutoOpen(s, "/a/b/c"), false);
});

test("save then load round-trips the set in order", () => {
  fakeStorage();
  const s = new Set(["/a", "/a/b", "/c"]);
  saveExpanded(s);
  assert.deepStrictEqual([...loadExpanded()], ["/a", "/a/b", "/c"]);
});

test("loadExpanded returns an empty set for absent, corrupt, or wrong-version data", () => {
  const map = fakeStorage();
  assert.strictEqual(loadExpanded().size, 0);           // absent
  map.set(TREESTATE_KEY, "{not json");
  assert.strictEqual(loadExpanded().size, 0);           // corrupt
  map.set(TREESTATE_KEY, JSON.stringify({ v: 99, paths: ["/a"] }));
  assert.strictEqual(loadExpanded().size, 0);           // wrong version
  map.set(TREESTATE_KEY, JSON.stringify({ v: 1, paths: ["/a", 7, null, "/b"] }));
  assert.deepStrictEqual([...loadExpanded()], ["/a", "/b"]); // non-strings dropped
});

test("loadExpanded enforces the cap against an oversized stored array", () => {
  const map = fakeStorage();
  const many = Array.from({ length: 505 }, (_, i) => "/d/" + i);
  map.set(TREESTATE_KEY, JSON.stringify({ v: 1, paths: many }));
  const s = loadExpanded();
  assert.strictEqual(s.size, 500);
  assert.ok(!s.has("/d/0"), "oldest hand-edited entry dropped");
  assert.ok(!s.has("/d/4"), "five oldest dropped");
  assert.ok(s.has("/d/5"), "sixth oldest kept");
  assert.ok(s.has("/d/504"), "newest kept");
});

test("saveExpanded and loadExpanded swallow a thrown storage backend", () => {
  global.localStorage = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
    removeItem() { throw new Error("blocked"); },
  };
  assert.doesNotThrow(() => saveExpanded(new Set(["/a"])));
  assert.strictEqual(loadExpanded().size, 0);
});
