"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { vfFold, vfFindAll, vfLocate, vfCountLabel } = require("../static/viewerfind.js");

test("matcher: case-insensitive, non-overlapping, ordered", () => {
  assert.deepEqual(vfFindAll("aXa xa", "xa", 10), { hits: [[1, 3], [4, 6]], capped: false });
  assert.deepEqual(vfFindAll("aaaa", "aa", 10).hits, [[0, 2], [2, 4]]);
});

test("matcher keeps offsets when lowercase changes length", () => {
  assert.equal(vfFold("İ").length, 1);
  assert.deepEqual(vfFindAll("İi", "i", 10).hits, [[1, 2]]);
  assert.equal(vfFold("AbC").length, 3);
});

test("matcher: empty query finds nothing", () => {
  assert.deepEqual(vfFindAll("abc", "", 10), { hits: [], capped: false });
});

test("matcher stops at cap", () => {
  const t0 = Date.now();
  const r = vfFindAll("a".repeat(100000), "a", 10000);
  assert.equal(r.hits.length, 10000);
  assert.equal(r.capped, true);
  assert.ok(Date.now() - t0 < 200);
});

test("matcher: exactly cap hits is not capped", () => {
  const r = vfFindAll("aaa", "a", 3);
  assert.equal(r.hits.length, 3);
  assert.equal(r.capped, false);
});

test("regex metacharacters are literal", () => {
  assert.deepEqual(vfFindAll("a.b axb", ".", 10).hits, [[1, 2]]);
  assert.deepEqual(vfFindAll("a(b", "(", 10).hits, [[1, 2]]);
});

test("locate: offset in a later segment", () => {
  assert.deepEqual(vfLocate(["ab", "cde"], 3), { seg: 1, off: 1 });
  assert.deepEqual(vfLocate(["ab", "cde"], 0), { seg: 0, off: 0 });
});

test("locate: a boundary offset belongs to the next segment as a start, the previous as an end", () => {
  assert.deepEqual(vfLocate(["ab", "cde"], 2), { seg: 1, off: 0 });
  assert.deepEqual(vfLocate(["ab", "cde"], 2, true), { seg: 0, off: 2 });
  assert.deepEqual(vfLocate(["ab", "cde"], 5, true), { seg: 1, off: 3 });
});

test("locate: skips empty segments", () => {
  assert.deepEqual(vfLocate(["", "ab", "", "c"], 2), { seg: 3, off: 0 });
});

test("count label", () => {
  assert.equal(vfCountLabel(0, 0, false), "No results");
  assert.equal(vfCountLabel(3, 17, false), "3 of 17");
  assert.equal(vfCountLabel(3, 10000, true), "3 of 10000+");
});
