"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { copyContentPlan, copyResultMessage, extOf, IMAGE_EXTS, readOnlyFrom } = require("../static/copyfile.js");

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

// /api/fs/read has THREE different 400s: "is a directory" (fsapi.go:152),
// "bad path" (:77, what the paste-path box produces for a relative path) and
// "not a directory" (:81). Only the first is a directory.
test("a 400 that is not is-a-directory stays unreadable", () => {
  for (const msg of ["bad path", "not a directory"]) {
    const p = copyContentPlan(errRes(400, msg));
    assert.equal(p.reason, "unreadable", msg);
    assert.equal(p.detail, msg);
  }
});

// A 200 whose body would not parse arrives as data:null. A genuinely empty text
// file NEVER looks like that — it is always the object {kind,lang,size:0} — so
// calling this "empty" would be a confident lie about a file we never read.
test("a 200 with an unparseable body is unreadable, not empty", () => {
  const p = copyContentPlan({ ok: true, status: 200, data: null });
  assert.equal(p.action, "refuse");
  assert.equal(p.reason, "unreadable");
});

// status 0 means fetch threw: the daemon restarted, the tunnel dropped. Saying
// "still loading" describes a state that does not exist, and in the viewer it
// contradicts the pane, which already says the server could not be reached.
test("an unreachable server is offline, not loading", () => {
  assert.deepEqual(copyContentPlan({ ok: false, status: 0, data: null }),
    { action: "refuse", reason: "offline" });
  const m = copyResultMessage({ action: "refuse", reason: "offline" }, "a.txt", false);
  assert.equal(m.error, true);
  assert.match(m.text, /reach the server/i);
});

// The viewer's own pre-first-load state stays distinct from a dead socket.
test("loading is still its own reason for a viewer that has not loaded yet", () => {
  const m = copyResultMessage({ action: "refuse", reason: "loading" }, "a.txt", false);
  assert.match(m.text, /still loading/i);
});

test("an image has no text and says so rather than blaming the clipboard", () => {
  const m = copyResultMessage({ action: "refuse", reason: "image" }, "photo.png", false);
  assert.equal(m.error, true);
  assert.match(m.text, /image/i);
});

test("any other failure is unreadable and keeps the server's sentence", () => {
  const p = copyContentPlan(errRes(404, "not found"));
  assert.equal(p.action, "refuse");
  assert.equal(p.reason, "unreadable");
  assert.equal(p.detail, "not found");
});

// "loading" is a state the viewer holds about itself (copyRefusal before its
// first load resolves), never something a response can say — a request that
// came back at all has a real answer. See the offline test above for status 0.

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

// #50: the server marks scratch-folder files readonly; the viewer must then
// offer no Edit, because a save there is always refused.
test("readOnlyFrom: only a successful read flagged readonly is read-only", () => {
  assert.equal(readOnlyFrom(okRes({ kind: "text", content: "x", readonly: true })), true);
  assert.equal(readOnlyFrom(okRes({ kind: "binary", size: 3, readonly: true })), true);
  assert.equal(readOnlyFrom(okRes({ kind: "text", content: "x" })), false);
  // A text file that is not UTF-8 would be saved back with its bytes changed: no Edit for it either.
  assert.equal(readOnlyFrom(okRes({ kind: "text", content: "x", utf8: false })), true);
  assert.equal(readOnlyFrom(okRes({ kind: "text", content: "x", utf8: true })), false);
  assert.equal(readOnlyFrom(errRes(403, "the Claude scratch folder is read-only")), false);
  assert.equal(readOnlyFrom(okRes(null)), false);
  assert.equal(readOnlyFrom(undefined), false);
});

test("the viewer hides Edit and refuses enterEdit for a read-only file", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/viewer-tab.js"), "utf8");
  assert.match(src, /this\.readOnly = ro/, "load() records readOnly from the read");
  assert.match(src, /readOnlyFrom\(res\)/, "...taken from the read's own answer");
  // The toolbar hides Edit through the model (viewertoolbar.test.js asserts it);
  // here only that a change of readOnly repaints it.
  assert.match(src, /this\.readOnly = ro; this\.changed\(\)/, "the toolbar repaints when readOnly changes");
  assert.match(src, /enterEdit\(\) \{\n\s+if \(this\.editing \|\| this\.box \|\| this\.readOnly\) return;/, "enterEdit guards readOnly");
});
