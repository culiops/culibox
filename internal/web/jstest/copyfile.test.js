"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { copyContentPlan, copyResultMessage, extOf, IMAGE_EXTS } = require("../static/copyfile.js");

const okRes = (data) => ({ ok: true, status: 200, data });
const errRes = (status, error) => ({ ok: false, status, data: { error } });

test("a text file is copied verbatim", () => {
  const p = copyContentPlan(okRes({ kind: "text", content: "a\nb\n" }));
  assert.equal(p.action, "copy");
  assert.equal(p.text, "a\nb\n");
});

// "a\nb" and "a\nb\n" are the same two lines. split("\n").length says 3 for the
// second, and the toast would over-report every file that ends properly.
test("a trailing newline does not invent a line", () => {
  assert.equal(copyContentPlan(okRes({ kind: "text", content: "a\nb\n" })).lines, 2);
  assert.equal(copyContentPlan(okRes({ kind: "text", content: "a\nb" })).lines, 2);
  assert.equal(copyContentPlan(okRes({ kind: "text", content: "solo" })).lines, 1);
});

// fsRead.Content is json:"content,omitempty", so a 0-byte file arrives with NO
// content key at all — not "".
test("an empty file is refused, whether content is absent or empty", () => {
  for (const data of [{ kind: "text", size: 0 }, { kind: "text", content: "" }]) {
    assert.deepEqual(copyContentPlan(okRes(data)), { action: "refuse", reason: "empty" });
  }
});

test("a binary file is refused", () => {
  assert.deepEqual(copyContentPlan(okRes({ kind: "binary", size: 99 })), { action: "refuse", reason: "binary" });
});

test("413 is refused as too large, not as a generic error", () => {
  assert.deepEqual(copyContentPlan(errRes(413, "file too large to preview")),
    { action: "refuse", reason: "toolarge" });
});

// A symlink to a directory reaches the tree as a file row, so this is reachable.
test("400 is-a-directory is refused as a directory", () => {
  assert.deepEqual(copyContentPlan(errRes(400, "is a directory")), { action: "refuse", reason: "isdir" });
});

test("any other failure is unreadable and keeps the server's sentence", () => {
  const p = copyContentPlan(errRes(404, "not found"));
  assert.equal(p.action, "refuse");
  assert.equal(p.reason, "unreadable");
  assert.equal(p.detail, "not found");
});

test("a viewer snapshot that has not loaded yet is refused as loading", () => {
  assert.deepEqual(copyContentPlan({ ok: false, status: 0, data: null }), { action: "refuse", reason: "loading" });
});

test("a successful copy reports the file and its line count", () => {
  const m = copyResultMessage({ action: "copy", text: "a\nb", lines: 2 }, "notes.md", true);
  assert.equal(m.error, false);
  assert.match(m.text, /notes\.md/);
  assert.match(m.text, /2 lines/);
});

test("one line is not reported as 1 lines", () => {
  assert.match(copyResultMessage({ action: "copy", text: "a", lines: 1 }, "a.txt", true).text, /1 line\b/);
});

test("a failed clipboard write is an error toast that suggests retrying", () => {
  const m = copyResultMessage({ action: "copy", text: "x", lines: 1 }, "notes.md", false);
  assert.equal(m.error, true);
  assert.match(m.text, /try again/i);
});

test("every refusal is an error toast naming the file", () => {
  for (const reason of ["empty", "binary", "toolarge", "isdir", "loading", "unreadable"]) {
    const m = copyResultMessage({ action: "refuse", reason, detail: "boom" }, "notes.md", false);
    assert.equal(m.error, true, reason);
    assert.match(m.text, /notes\.md/, reason);
  }
});

test("the binary and too-large refusals point at Download, which does handle them", () => {
  assert.match(copyResultMessage({ action: "refuse", reason: "binary" }, "a.bin", false).text, /Download/);
  assert.match(copyResultMessage({ action: "refuse", reason: "toolarge" }, "a.log", false).text, /Download/);
});

// Go lowercases the extension before deciding an image is an image, so the
// client must too or it offers Copy on photo.PNG and then fails in JSON.parse.
test("extOf lowercases, so an uppercase image extension is still an image", () => {
  assert.equal(extOf("/tmp/photo.PNG"), "png");
  assert.ok(IMAGE_EXTS.has(extOf("/tmp/photo.PNG")));
  assert.equal(extOf("/tmp/README"), "");
  assert.equal(extOf("/tmp/a.b/c"), "");
});
