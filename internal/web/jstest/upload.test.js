"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { shouldRefreshTree } = require("../static/upload.js");

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
