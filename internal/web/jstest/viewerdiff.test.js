"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { parseUnifiedDiff, diffSign, diffRowHtml, diffMaxLine } = require("../static/viewerdiff.js");

const TWO_HUNKS = [
  "diff --git a/a.go b/a.go",
  "index 111..222 100644",
  "--- a/a.go",
  "+++ b/a.go",
  "@@ -1,4 +1,5 @@ func main() {",
  " one",
  "-two",
  "+TWO",
  "+two-b",
  " three",
  " four",
  "@@ -20,3 +21,2 @@",
  " x",
  "-y",
  " z",
  "",
].join("\n");

const shape = (rows) => rows.map((r) => r.t + ":" + (r.n === null ? "-" : r.n));

test("two hunks: n follows the new file, del rows have no number", () => {
  const rows = parseUnifiedDiff(TWO_HUNKS);
  assert.deepEqual(shape(rows), [
    "meta:-", "meta:-", "meta:-", "meta:-",
    "hunk:-", "ctx:1", "del:-", "add:2", "add:3", "ctx:4", "ctx:5",
    "hunk:-", "ctx:21", "del:-", "ctx:22",
  ]);
});

test("row text drops the sign column; hunk and meta keep the whole line", () => {
  const rows = parseUnifiedDiff(TWO_HUNKS);
  assert.equal(rows[0].text, "diff --git a/a.go b/a.go");
  assert.equal(rows[4].text, "@@ -1,4 +1,5 @@ func main() {");
  assert.equal(rows[5].text, "one");
  assert.equal(rows[6].text, "two");
  assert.equal(rows[7].text, "TWO");
});

test("a text line that looks like a header inside a hunk stays a del/add row", () => {
  const d = "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n--- not a header\n+++ also not\n";
  assert.deepEqual(shape(parseUnifiedDiff(d)), ["meta:-", "meta:-", "hunk:-", "del:-", "add:1"]);
});

test("no-newline marker is meta and does not move the counters", () => {
  const d = "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n";
  assert.deepEqual(shape(parseUnifiedDiff(d)), [
    "meta:-", "meta:-", "hunk:-", "ctx:1", "del:-", "meta:-", "add:2", "meta:-",
  ]);
  assert.equal(parseUnifiedDiff(d)[5].text, "\\ No newline at end of file");
});

test("untracked file (/dev/null header) numbers from 1", () => {
  const d = [
    "diff --git a/new.txt b/new.txt", "new file mode 100644", "index 0000000..e69de29",
    "--- /dev/null", "+++ b/new.txt", "@@ -0,0 +1,3 @@", "+a", "+b", "+c", "",
  ].join("\n");
  const rows = parseUnifiedDiff(d);
  assert.deepEqual(rows.filter((r) => r.t === "add").map((r) => r.n), [1, 2, 3]);
  assert.deepEqual(rows.slice(0, 5).map((r) => r.t), ["meta", "meta", "meta", "meta", "meta"]);
});

test("hunk header without counts means one line each side", () => {
  const rows = parseUnifiedDiff("@@ -5 +7 @@\n-a\n+b\n");
  assert.deepEqual(shape(rows), ["hunk:-", "del:-", "add:7"]);
});

test("a blank line inside a hunk is an empty context row (some tools strip the space)", () => {
  const rows = parseUnifiedDiff("@@ -1,3 +1,3 @@\n a\n\n+b\n");
  assert.deepEqual(shape(rows), ["hunk:-", "ctx:1", "ctx:2", "add:3"]);
  assert.equal(rows[2].text, "");
});

test("lines after the hunk's counts are used up are meta (a second file's header)", () => {
  const d = "@@ -1 +1 @@\n-a\n+b\ndiff --git a/g b/g\n--- a/g\n+++ b/g\n@@ -1 +1 @@\n-c\n+d\n";
  assert.deepEqual(shape(parseUnifiedDiff(d)), [
    "hunk:-", "del:-", "add:1", "meta:-", "meta:-", "meta:-", "hunk:-", "del:-", "add:1",
  ]);
});

test("a truncated diff (cut mid-hunk) still parses what is there", () => {
  assert.deepEqual(shape(parseUnifiedDiff("@@ -1,9 +1,9 @@\n a\n-b\n+c")), ["hunk:-", "ctx:1", "del:-", "add:2"]);
});

test("empty and falsy input give no rows; a lone trailing newline is not a row", () => {
  assert.deepEqual(parseUnifiedDiff(""), []);
  assert.deepEqual(parseUnifiedDiff(null), []);
  assert.deepEqual(parseUnifiedDiff(undefined), []);
  assert.deepEqual(parseUnifiedDiff("\n"), [{ t: "meta", text: "", n: null }]);
});

test("diffSign: + and the true minus, nothing for the rest", () => {
  assert.equal(diffSign("add"), "+");
  assert.equal(diffSign("del"), "−");
  for (const t of ["ctx", "hunk", "meta"]) assert.equal(diffSign(t), "");
});

test("diffMaxLine is the last new-file line shown", () => {
  assert.equal(diffMaxLine(parseUnifiedDiff(TWO_HUNKS)), 22);
  assert.equal(diffMaxLine([]), 0);
});

// diffRowHtml(rows, hl, esc): one html string per row. hl(text, lang) -> html for a
// whole stream of lines (or throws / returns null when it cannot).
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const fakeHl = (text) => text.split("\n").map((l) => "[" + esc(l) + "]").join("\n");

test("diffRowHtml: code rows go through the highlighter, hunk/meta rows are escaped plain", () => {
  const rows = parseUnifiedDiff("--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@ <T>\n a<b\n-old\n+new\n");
  const html = diffRowHtml(rows, fakeHl, esc);
  assert.equal(html.length, rows.length);
  assert.equal(html[0], "--- a/f");
  assert.equal(html[2], "@@ -1,2 +1,2 @@ &lt;T&gt;");
  assert.equal(html[3], "[a&lt;b]");
  assert.equal(html[4], "[old]");
  assert.equal(html[5], "[new]");
});

test("diffRowHtml: old and new sides are separate streams, so a multi-line token does not leak across", () => {
  const seen = [];
  const hl = (text) => { seen.push(text); return text.split("\n").map(esc).join("\n"); };
  const rows = parseUnifiedDiff("@@ -1,3 +1,3 @@\n ctx\n-del1\n+add1\n ctx2\n");
  diffRowHtml(rows, hl, esc);
  assert.deepEqual(seen, ["ctx\nadd1\nctx2", "ctx\ndel1\nctx2"]);
});

test("diffRowHtml: a highlighter that throws or loses a line falls back to plain escaped text", () => {
  const rows = parseUnifiedDiff("@@ -1,2 +1,2 @@\n a<\n+b\n");
  assert.deepEqual(diffRowHtml(rows, () => { throw new Error("x"); }, esc).slice(1), ["a&lt;", "b"]);
  assert.deepEqual(diffRowHtml(rows, () => "only one line", esc).slice(1), ["a&lt;", "b"]);
  assert.deepEqual(diffRowHtml(rows, null, esc).slice(1), ["a&lt;", "b"]);
});

test("diffRowHtml: empty rows survive (a blank source line is not dropped)", () => {
  const rows = parseUnifiedDiff("@@ -1,3 +1,3 @@\n a\n+\n+\n");
  const html = diffRowHtml(rows, fakeHl, esc);
  assert.deepEqual(html.slice(1), ["[a]", "[]", "[]"]);
});

test("diffRowHtml: a span that crosses lines is closed and reopened per row", () => {
  const hl = () => '<span class="c">a\nb</span>';
  const rows = parseUnifiedDiff("@@ -0,0 +1,2 @@\n+a\n+b\n");
  const html = diffRowHtml(rows, hl, esc);
  assert.equal(html[1], '<span class="c">a</span>');
  assert.equal(html[2], '<span class="c">b</span>');
});
