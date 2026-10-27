"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { shouldRefreshTree, insertTextAtPrompt } = require("../static/upload.js");

test("an upload into the directory the tree is showing refreshes it", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj", "/home/u/proj"), true);
});

test("a trailing slash on either side is the same directory", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj/", "/home/u/proj"), true);
  assert.strictEqual(shouldRefreshTree("/home/u/proj", "/home/u/proj/"), true);
});

test("an upload elsewhere leaves the tree alone", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/other", "/home/u/proj"), false);
});

// Deliberate: re-listing the root would not reveal a file nested one level down,
// and it would collapse every folder the user had expanded. Not worth it.
test("an upload into a subfolder of the root does not re-list the root", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj/sub", "/home/u/proj"), false);
});

test("an upload into the parent of the root does not re-list the root", () => {
  assert.strictEqual(shouldRefreshTree("/home/u", "/home/u/proj"), false);
});

test("a prefix that is not a path boundary is not a match", () => {
  assert.strictEqual(shouldRefreshTree("/home/u/proj2", "/home/u/proj"), false);
});

test("a missing destination or root never refreshes", () => {
  assert.strictEqual(shouldRefreshTree("", "/home/u/proj"), false);
  assert.strictEqual(shouldRefreshTree("/home/u/proj", ""), false);
  assert.strictEqual(shouldRefreshTree(null, null), false);
  assert.strictEqual(shouldRefreshTree(undefined, "/home/u/proj"), false);
});

test("the filesystem root is not mistaken for an empty path", () => {
  assert.strictEqual(shouldRefreshTree("/", "/"), true);
  assert.strictEqual(shouldRefreshTree("/", "/home"), false);
});

// A fake terminal: sendText's real contract is "returns false when it refuses".
const fakeTerm = (accept) => ({ sent: [], sendText(t) { if (!accept) return false; this.sent.push(t); return true; } });

test("insertTextAtPrompt sends the text and reports success", () => {
  const t = fakeTerm(true);
  assert.deepStrictEqual(insertTextAtPrompt(t, "/tmp/a.js "), { ok: true, reason: "" });
  assert.deepStrictEqual(t.sent, ["/tmp/a.js "]);
});

// The guarantee the whole feature rests on: nothing this app inserts may submit
// a line. A directory name may legally contain a newline on Linux.
test("insertTextAtPrompt refuses text carrying a newline", () => {
  const t = fakeTerm(true);
  assert.deepStrictEqual(insertTextAtPrompt(t, "/tmp/a\nrm -rf /"), { ok: false, reason: "unsafe" });
  assert.deepStrictEqual(t.sent, []);
});

test("insertTextAtPrompt refuses C1 control bytes", () => {
  const t = fakeTerm(true);
  assert.strictEqual(insertTextAtPrompt(t, "/tmp/ax").reason, "unsafe");
});

test("insertTextAtPrompt reports a missing terminal", () => {
  assert.deepStrictEqual(insertTextAtPrompt(null, "/tmp/a.js "), { ok: false, reason: "noterm" });
  assert.deepStrictEqual(insertTextAtPrompt({}, "/tmp/a.js "), { ok: false, reason: "noterm" });
});

test("insertTextAtPrompt reports a refused send", () => {
  assert.deepStrictEqual(insertTextAtPrompt(fakeTerm(false), "/tmp/a.js "), { ok: false, reason: "disconnected" });
});

const { dropHint } = require("../static/upload.js");

test("dropHint: mobile keeps today's exact strings", () => {
  assert.strictEqual(dropHint("nodest", false), "No destination folder yet. Drop onto a folder in Files instead");
  assert.strictEqual(dropHint("miss", false), "Drop onto a terminal pane, or onto a folder in Files");
});

test("dropHint: desktop names the Files drawer", () => {
  assert.strictEqual(dropHint("nodest", true), "No destination folder yet. Drop onto a folder in the Files drawer instead");
  assert.strictEqual(dropHint("miss", true), "Drop onto a terminal pane, or onto a folder in the Files drawer");
});
