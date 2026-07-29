"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { iconFor } = require("../static/filetree.js");

// Codepoints are asserted by value, not by name. A glyph that silently changes
// is a tofu box in the tree, and only the number can catch that.
const FOLDER = "", FOLDER_OPEN = "", FILE = "";

test("a directory reports its open state", () => {
  assert.strictEqual(iconFor("internal", true, false), FOLDER);
  assert.strictEqual(iconFor("internal", true, true), FOLDER_OPEN);
});

test("known extensions map to their own glyph", () => {
  assert.strictEqual(iconFor("web.go", false), "");
  assert.strictEqual(iconFor("palette.js", false), "");
  assert.strictEqual(iconFor("README.md", false), "");
  assert.strictEqual(iconFor("config.json", false), "");
  assert.strictEqual(iconFor("index.html", false), "");
  assert.strictEqual(iconFor("xterm.css", false), "");
  assert.strictEqual(iconFor("logo.png", false), "");
  assert.strictEqual(iconFor("test.sh", false), "");
});

// The trap: .gitignore has no extension in the usual sense, and a naive
// "split on the last dot" treats "gitignore" as its extension.
test("exact names are matched before extensions", () => {
  assert.strictEqual(iconFor(".gitignore", false), "");
  assert.strictEqual(iconFor(".gitattributes", false), "");
});

test("matching ignores case", () => {
  assert.strictEqual(iconFor("README.MD", false), "");
  assert.strictEqual(iconFor("LOGO.PNG", false), "");
});

test("an unknown extension falls back to the plain file glyph", () => {
  assert.strictEqual(iconFor("notes.xyz", false), FILE);
  assert.strictEqual(iconFor("Makefile", false), FILE);
  assert.strictEqual(iconFor("", false), FILE);
});

test("a dotfile with no second dot is not read as an extension", () => {
  assert.strictEqual(iconFor(".env", false), FILE);
});

test("every glyph is exactly one character", () => {
  for (const n of ["web.go", ".gitignore", "x.unknown", "d"]) {
    assert.strictEqual(Array.from(iconFor(n, false)).length, 1, n);
    assert.strictEqual(Array.from(iconFor(n, true, false)).length, 1, n);
  }
});

// A prototype-chain lookup returns a function or an object, and the DOM would
// coerce either into a multi-character string that shifts the whole name column.
test("names that collide with Object.prototype fall back to the file glyph", () => {
  for (const n of ["constructor", "__proto__", "toString", "valueOf", "hasOwnProperty"]) {
    assert.strictEqual(iconFor(n, false), FILE, n);
    assert.strictEqual(Array.from(iconFor(n, false)).length, 1, n);
  }
});

test("extensions that collide with Object.prototype fall back too", () => {
  assert.strictEqual(iconFor("x.constructor", false), FILE);
  assert.strictEqual(iconFor("x.toString", false), FILE);
});
