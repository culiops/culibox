"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { lineDiff } = require("../static/linediff.js");

const L = (...lines) => lines.join("\n");
const diff = (a, b, max) => lineDiff(a, b, max);

test("lineDiff add mod del", () => {
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "b", "X", "c")),
    { changes: [{ kind: "add", from: 3, to: 3 }], capped: false });
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "B", "c")),
    { changes: [{ kind: "mod", from: 2, to: 2 }], capped: false });
  // a del anchors at the line after the removal, in the NEW text
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "c")),
    { changes: [{ kind: "del", from: 2, to: 2 }], capped: false });
  assert.deepStrictEqual(diff(L("a", "b"), L("b")),
    { changes: [{ kind: "del", from: 1, to: 1 }], capped: false });
});

test("lineDiff: a block of added lines is one range, in new-text line numbers", () => {
  assert.deepStrictEqual(diff(L("a", "d"), L("a", "b", "c", "d")).changes, [{ kind: "add", from: 2, to: 3 }]);
  assert.deepStrictEqual(diff(L("a"), L("x", "y", "a")).changes, [{ kind: "add", from: 1, to: 2 }]);
});

test("lineDiff: lines replaced by more or fewer lines are one mod over the new lines", () => {
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "x", "y", "z", "c")).changes, [{ kind: "mod", from: 2, to: 4 }]);
  // three old lines became one: the mod covers the new line, the extra removals are part of it
  assert.deepStrictEqual(diff(L("a", "b", "c", "d"), L("a", "X", "d")).changes, [{ kind: "mod", from: 2, to: 2 }]);
});

test("lineDiff: a removal at the end anchors one past the last line (the gutter clamps)", () => {
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "b")).changes, [{ kind: "del", from: 3, to: 3 }]);
  // with a trailing newline Monaco has an empty last line, and the anchor is that line
  assert.deepStrictEqual(diff("a\nb\nc\n", "a\nb\n").changes, [{ kind: "del", from: 3, to: 3 }]);
});

test("lineDiff: several hunks come back in order", () => {
  const old = L("1", "2", "3", "4", "5", "6", "7");
  const now = L("1", "TWO", "3", "5", "6", "7", "8", "9");
  assert.deepStrictEqual(diff(old, now).changes, [
    { kind: "mod", from: 2, to: 2 },
    { kind: "del", from: 4, to: 4 },
    { kind: "add", from: 7, to: 8 },
  ]);
});

test("lineDiff: no change is no hunks", () => {
  assert.deepStrictEqual(diff("", ""), { changes: [], capped: false });
  assert.deepStrictEqual(diff(L("a", "b"), L("a", "b")), { changes: [], capped: false });
  assert.deepStrictEqual(diff(null, undefined), { changes: [], capped: false });
});

test("lineDiff: a new file is all added and an emptied file is one removal", () => {
  assert.deepStrictEqual(diff("", L("a", "b", "c")).changes, [{ kind: "add", from: 1, to: 3 }]);
  assert.deepStrictEqual(diff(L("a", "b", "c"), "").changes, [{ kind: "del", from: 1, to: 1 }]);
});

test("lineDiff: line endings do not make a difference (Monaco keeps its own)", () => {
  assert.deepStrictEqual(diff("a\r\nb\r\nc", "a\nb\nc").changes, []);
  assert.deepStrictEqual(diff("a\r\nb\r\nc", "a\nB\nc").changes, [{ kind: "mod", from: 2, to: 2 }]);
});

test("lineDiff: the end-of-file newline is a line, like Monaco's empty last line", () => {
  assert.deepStrictEqual(diff("a\nb\n", "a\nb\nc\n").changes, [{ kind: "add", from: 3, to: 3 }]);
  assert.deepStrictEqual(diff("a", "a\n").changes, [{ kind: "add", from: 2, to: 2 }]);
});

test("lineDiff: repeated and blank lines are matched in order", () => {
  assert.deepStrictEqual(diff(L("x", "", "x", ""), L("x", "", "y", "x", "")).changes, [{ kind: "add", from: 3, to: 3 }]);
});

// The oracle: the new lines no range covers are exactly the longest common subsequence of the two texts, and
// they read in the same order in the old text. Checked on random small texts over a tiny alphabet, where
// repeats make the choice hard.
function lcsLength(a, b) {
  const t = Array.from({ length: a.length + 1 }, () => new Array(b.length + 1).fill(0));
  for (let i = 1; i <= a.length; i++) for (let j = 1; j <= b.length; j++) {
    t[i][j] = a[i - 1] === b[j - 1] ? t[i - 1][j - 1] + 1 : Math.max(t[i - 1][j], t[i][j - 1]);
  }
  return t[a.length][b.length];
}
function isSubsequence(small, big) {
  let i = 0;
  for (const x of big) if (i < small.length && small[i] === x) i++;
  return i === small.length;
}
test("lineDiff: what it leaves unmarked is a longest common subsequence", () => {
  let seed = 12345;
  const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
  const text = () => Array.from({ length: rnd(14) }, () => "abc"[rnd(3)]);
  for (let i = 0; i < 400; i++) {
    const a = text();
    const b = text();
    const res = lineDiff(a.join("\n"), b.join("\n"));
    assert.strictEqual(res.capped, false);
    const al = a;
    const bl = b;
    const marked = new Set();
    let prevEnd = 0;
    for (const c of res.changes) {
      assert.ok(c.from >= 1 && c.to >= c.from, JSON.stringify(c));
      assert.ok(c.from > prevEnd, "ordered, with a kept line between hunks: " + JSON.stringify(res.changes));
      if (c.kind === "del") { assert.strictEqual(c.from, c.to); assert.ok(c.to <= bl.length + 1); prevEnd = c.from; continue; }
      assert.ok(c.to <= bl.length);
      for (let n = c.from; n <= c.to; n++) marked.add(n);
      prevEnd = c.to;
    }
    const kept = bl.filter((_, idx) => !marked.has(idx + 1));
    assert.strictEqual(kept.length, lcsLength(al, bl), JSON.stringify([a, b, res]));
    assert.ok(isSubsequence(kept, al), JSON.stringify([a, b, res]));
  }
});

test("lineDiff caps", () => {
  // above maxLines on either side: nothing computed
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "b", "c", "d"), 3), { changes: [], capped: true });
  assert.deepStrictEqual(diff(L("a", "b", "c", "d"), L("a", "b"), 3), { changes: [], capped: true });
  // at maxLines it still runs
  assert.deepStrictEqual(diff(L("a", "b", "c"), L("a", "B", "c"), 3).capped, false);
  // the default is 20000 lines
  const big = Array.from({ length: 20001 }, (_, i) => "line " + i).join("\n");
  assert.deepStrictEqual(diff(big, big + "x"), { changes: [], capped: true });
  const ok = Array.from({ length: 20000 }, (_, i) => "line " + i).join("\n");
  assert.strictEqual(diff(ok, ok + "").capped, false);
});

test("lineDiff caps a text over 2 MiB however few lines it has (one huge line)", () => {
  const limit = 2 << 20;
  const huge = "x".repeat(limit + 1);
  const t0 = Date.now();
  assert.deepStrictEqual(diff(huge, "x"), { changes: [], capped: true });
  assert.deepStrictEqual(diff("x", huge), { changes: [], capped: true });
  assert.ok(Date.now() - t0 < 2000, "capped without doing the work");
  // exactly at the limit still diffs, as one line
  const edge = "y".repeat(limit);
  assert.deepStrictEqual(diff(edge, edge + ""), { changes: [], capped: false });
  assert.deepStrictEqual(diff("z" + edge.slice(1), edge).changes, [{ kind: "mod", from: 1, to: 1 }]);
});

test("lineDiff gives up on two texts with nothing in common instead of freezing", () => {
  const a = Array.from({ length: 20000 }, (_, i) => "old " + i).join("\n");
  const b = Array.from({ length: 20000 }, (_, i) => "new " + i).join("\n");
  const t0 = Date.now();
  assert.deepStrictEqual(diff(a, b), { changes: [], capped: true });
  assert.ok(Date.now() - t0 < 3000, "bounded work");
});

test("lineDiff: a big file with a few edits is fast and exact", () => {
  const lines = Array.from({ length: 19000 }, (_, i) => "line " + i);
  const edited = lines.slice();
  edited[10] = "changed";
  edited.splice(5000, 3);
  edited.splice(12000, 0, "new a", "new b");
  const t0 = Date.now();
  const res = diff(lines.join("\n"), edited.join("\n"));
  assert.ok(Date.now() - t0 < 2000);
  assert.strictEqual(res.capped, false);
  assert.deepStrictEqual(res.changes, [
    { kind: "mod", from: 11, to: 11 },
    { kind: "del", from: 5001, to: 5001 },
    { kind: "add", from: 12001, to: 12002 }, // spliced in after the removal, so at index 12000 of the edited text
  ]);
});

test("lineDiff: a pure insertion or deletion of a whole file is never capped by the edit budget", () => {
  const big = Array.from({ length: 20000 }, (_, i) => "line " + i).join("\n");
  assert.deepStrictEqual(diff("", big).changes, [{ kind: "add", from: 1, to: 20000 }]);
  assert.deepStrictEqual(diff(big, "").changes, [{ kind: "del", from: 1, to: 1 }]);
  assert.deepStrictEqual(diff("head", "head\n" + big, 30000).changes, [{ kind: "add", from: 2, to: 20001 }]);
});
