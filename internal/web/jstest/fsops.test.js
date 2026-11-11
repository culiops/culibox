"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { deleteConfirmText, deleteSummary, normalizePath, shortPath, relativeTo } = require("../static/fsops.js");

// In the browser every static script shares one global scope and upload.js is
// loaded first, so humanBytes is simply there. Reproduce that here rather than
// testing the degraded raw-bytes fallback.
global.humanBytes = require("../static/upload.js").humanBytes;

test("a file names itself and says it cannot be undone", () => {
  const s = deleteConfirmText("notes.txt", false, {});
  assert.match(s, /notes\.txt/);
  assert.match(s, /cannot be undone/i);
  assert.match(s, /file/);
});

test("a directory reports how much is inside", () => {
  const s = deleteConfirmText("proj", true, { entries: 44, files: 42, bytes: 1363148, capped: "" });
  assert.match(s, /proj/);
  assert.match(s, /44 items/);
  assert.match(s, /42 files/);
  assert.match(s, /1\.3 MB/);
  assert.match(s, /folder/);
});

// The defect this replaced: a tree of directories and symlinks holds no regular
// files, so counting files alone described it exactly like an empty folder.
test("a tree with no regular files still says how many items it holds", () => {
  const s = deleteConfirmText("trap", true, { entries: 502, files: 0, bytes: 0, capped: "" });
  assert.match(s, /502 items/);
  assert.notStrictEqual(s, deleteConfirmText("trap", true, { entries: 0, files: 0, bytes: 0, capped: "" }));
});

test("one item and one file are singular", () => {
  const s = deleteConfirmText("proj", true, { entries: 1, files: 1, bytes: 12, capped: "" });
  assert.match(s, /1 item[^s]/);
  assert.match(s, /1 file[^s]/);
});

test("an entry-capped measurement hedges the count, not the bytes", () => {
  const s = deleteConfirmText("huge", true, { entries: 100000, files: 9, bytes: 1024, capped: "entries" });
  assert.match(s, /over 100000 items/i);
  assert.match(s, /9 files/);
});

// A byte cap says nothing about the entry count, which is exact — hedging the
// wrong half is how "over 3 files" ends up describing a 1 TB folder.
test("a byte-capped measurement hedges the size, not the count", () => {
  const s = deleteConfirmText("huge", true, { entries: 3, files: 3, bytes: 1 << 30, capped: "bytes" });
  assert.match(s, /3 items/);
  assert.match(s, /over 1\.0 GB/);
  assert.doesNotMatch(s, /over 3 items/);
});

test("a directory with nothing measured claims no contents", () => {
  const s = deleteConfirmText("empty", true, { entries: 0, files: 0, bytes: 0, capped: "" });
  assert.doesNotMatch(s, /items/);
  assert.doesNotMatch(s, /files/);
  assert.match(s, /empty/);
});

test("deleteSummary is empty exactly when there is nothing to report", () => {
  assert.strictEqual(deleteSummary({ entries: 0 }), "");
  assert.strictEqual(deleteSummary({}), "");
  assert.strictEqual(deleteSummary(null), "");
  assert.notStrictEqual(deleteSummary({ entries: 1 }), "");
});

test("normalizePath collapses the shapes the paste box can produce", () => {
  assert.strictEqual(normalizePath("/home/u/./a.txt"), "/home/u/a.txt");
  assert.strictEqual(normalizePath("//home//u/a.txt"), "/home/u/a.txt");
  assert.strictEqual(normalizePath("/home/u/sub/../a.txt"), "/home/u/a.txt");
  assert.strictEqual(normalizePath("/home/u/"), "/home/u");
  assert.strictEqual(normalizePath("/home/u/a.txt"), "/home/u/a.txt");
});

// shortPath is called from three separate scripts (the spawn toast, the command
// palette, the upload toasts). It lived in app.js, which no test can require, so
// when a refactor deleted it the suite stayed green and every caller threw
// ReferenceError at runtime. It lives in a required module now for that reason.
test("shortPath abbreviates home to ~", () => {
  assert.strictEqual(shortPath("/home/u/repo/a.txt", "/home/u"), "~/repo/a.txt");
  assert.strictEqual(shortPath("/home/u", "/home/u"), "~");
});

test("shortPath leaves a path outside home alone", () => {
  assert.strictEqual(shortPath("/srv/data", "/home/u"), "/srv/data");
});

// "/home/user2" starts with "/home/user" but is not inside it — a prefix test
// alone would render it "~2" and point at the wrong directory.
test("shortPath only matches whole path components", () => {
  assert.strictEqual(shortPath("/home/u2/a.txt", "/home/u"), "/home/u2/a.txt");
});

test("shortPath is a no-op when home is unknown", () => {
  assert.strictEqual(shortPath("/home/u/a.txt", ""), "/home/u/a.txt");
  assert.strictEqual(shortPath("/home/u/a.txt", undefined), "/home/u/a.txt");
});

test("a file never reports contents, even if a measurement is passed", () => {
  const s = deleteConfirmText("a.txt", false, { entries: 9, files: 9, bytes: 1024, capped: "" });
  assert.doesNotMatch(s, /9 files/);
  assert.doesNotMatch(s, /items/);
});

// relativeTo is the single definition of "relative path" in the app. The tree is
// rooted at whatever the root picker (#ft-root-input) last resolved to, while
// /api/fs/search returns paths relative to HOME, so without one shared rule the
// two surfaces would disagree.
test("relativeTo strips the root", () => {
  assert.strictEqual(relativeTo("/home/user/proj", "/home/user/proj/a/b.js"), "a/b.js");
});

test("relativeTo tolerates a trailing slash on the root", () => {
  assert.strictEqual(relativeTo("/home/user/proj/", "/home/user/proj/a/b.js"), "a/b.js");
});

test("relativeTo handles the filesystem root", () => {
  assert.strictEqual(relativeTo("/", "/etc/hosts"), "etc/hosts");
});

test("relativeTo calls the root itself dot", () => {
  assert.strictEqual(relativeTo("/home/user/proj", "/home/user/proj"), ".");
  assert.strictEqual(relativeTo("/home/user/proj/", "/home/user/proj"), ".");
});

// The trap shortPath already carries a comment about: a string prefix match puts
// /home/user/proj2 "inside" /home/user/proj. Compare whole components.
test("relativeTo does not treat a sibling with a shared prefix as inside", () => {
  assert.strictEqual(
    relativeTo("/home/user/proj", "/home/user/proj2/x", "/home/user"),
    "~/proj2/x");
});

test("relativeTo falls back to the absolute path when it is outside home too", () => {
  assert.strictEqual(relativeTo("/home/user/proj", "/etc/hosts", "/home/user"), "/etc/hosts");
});

test("relativeTo returns the path unchanged when there is no root", () => {
  assert.strictEqual(relativeTo("", "/etc/hosts", "/home/user"), "/etc/hosts");
});

test("boxFileURL addresses a session's box file route, encoding both parts", () => {
  const { boxFileURL } = require("../static/fsops.js");
  assert.strictEqual(
    boxFileURL("s 1", "/tmp/playwright-mcp/a b.png"),
    "/api/sessions/s%201/boxfile?path=%2Ftmp%2Fplaywright-mcp%2Fa%20b.png");
  assert.strictEqual(
    boxFileURL("s1", "/tmp/playwright-mcp/a.png", true),
    "/api/sessions/s1/boxfile?path=%2Ftmp%2Fplaywright-mcp%2Fa.png&download=1");
});
