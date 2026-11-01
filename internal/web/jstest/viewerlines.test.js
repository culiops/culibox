"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { splitHighlightedLines, countSourceLines, clampLine } = require("../static/viewerlines.js");

test("split: plain", () => {
  assert.deepEqual(splitHighlightedLines("a\nb"), ["a", "b"]);
});

test("split: span crossing lines is closed and reopened", () => {
  assert.deepEqual(
    splitHighlightedLines('<span class="hljs-comment">/* a\nb */</span>x'),
    ['<span class="hljs-comment">/* a</span>', '<span class="hljs-comment">b */</span>x']);
});

test("split: nested spans", () => {
  assert.deepEqual(
    splitHighlightedLines('<span class="a"><span class="b">x\ny</span>z</span>'),
    ['<span class="a"><span class="b">x</span></span>', '<span class="a"><span class="b">y</span>z</span>']);
});

test("split: CRLF and trailing newline", () => {
  const out = splitHighlightedLines("a\r\nb\r\n");
  assert.deepEqual(out, ["a", "b"]);
  assert.ok(out.every((l) => !l.includes("\r")));
  assert.equal(countSourceLines("a\r\nb\r\n"), 2);
});

test("split: entities survive", () => {
  assert.deepEqual(splitHighlightedLines("&lt;a&gt;\n&amp;"), ["&lt;a&gt;", "&amp;"]);
});

test("split: blank lines are kept, only the final empty one is dropped", () => {
  assert.deepEqual(splitHighlightedLines("a\n\nb\n"), ["a", "", "b"]);
  assert.deepEqual(splitHighlightedLines("a\n\n"), ["a", ""]);
  assert.deepEqual(splitHighlightedLines("\n"), [""]);
  assert.deepEqual(splitHighlightedLines(""), []);
});

test("split: a newline inside a span right before the end adds no phantom line", () => {
  assert.deepEqual(splitHighlightedLines('<span class="s">x\n</span>'), ['<span class="s">x</span>']);
});

test("split: line count agrees with countSourceLines", () => {
  for (const t of ["", "a", "a\n", "a\nb", "a\nb\n", "\n", "\n\n", "a\r\n\r\nb"]) {
    assert.equal(splitHighlightedLines(t).length, countSourceLines(t), JSON.stringify(t));
  }
});

test("split: a 2M-char single line returns 1 line in < 200 ms", () => {
  const big = "x".repeat(2_000_000);
  const t0 = process.hrtime.bigint();
  const out = splitHighlightedLines(big);
  const ms = Number(process.hrtime.bigint() - t0) / 1e6;
  assert.equal(out.length, 1);
  assert.ok(ms < 200, "took " + ms + " ms");
});

test("countSourceLines", () => {
  assert.equal(countSourceLines(""), 0);
  assert.equal(countSourceLines("a"), 1);
  assert.equal(countSourceLines("a\nb\n"), 2);
  assert.equal(countSourceLines("a\n\n"), 2);
});

test("clampLine", () => {
  assert.deepEqual(clampLine(0, 10), { line: 1, clamped: true });
  assert.deepEqual(clampLine(11, 10), { line: 10, clamped: true });
  assert.deepEqual(clampLine(5, 10), { line: 5, clamped: false });
  assert.deepEqual(clampLine(10, 10), { line: 10, clamped: false });
  assert.deepEqual(clampLine(3, 0), { line: 1, clamped: true });
});
