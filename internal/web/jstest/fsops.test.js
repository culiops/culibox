"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { deleteConfirmText } = require("../static/fsops.js");

// In the browser every static script shares one global scope and upload.js is
// loaded first, so humanBytes is simply there. Reproduce that here rather than
// testing the degraded raw-bytes fallback.
global.humanBytes = require("../static/upload.js").humanBytes;

test("a file names itself and says it cannot be undone", () => {
  const s = deleteConfirmText("notes.txt", false, 0, 0, false);
  assert.match(s, /notes\.txt/);
  assert.match(s, /cannot be undone/i);
  assert.match(s, /file/);
});

test("a directory reports how much is inside", () => {
  const s = deleteConfirmText("proj", true, 42, 1363148, false);
  assert.match(s, /proj/);
  assert.match(s, /42 files/);
  assert.match(s, /1\.3 MB/);
  assert.match(s, /folder/);
});

test("one file is singular", () => {
  assert.match(deleteConfirmText("proj", true, 1, 12, false), /1 file[^s]/);
});

test("a capped measurement never states a precise total", () => {
  const s = deleteConfirmText("huge", true, 100000, 1 << 30, true);
  assert.match(s, /over 100000 files/i);
  assert.doesNotMatch(s, /GB/);
});

test("an empty directory does not claim contents", () => {
  const s = deleteConfirmText("empty", true, 0, 0, false);
  assert.doesNotMatch(s, /files/);
  assert.match(s, /empty/);
});

test("a file never reports a size, even if one is passed", () => {
  const s = deleteConfirmText("a.txt", false, 9, 1024, false);
  assert.doesNotMatch(s, /9 files/);
});
