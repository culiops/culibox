"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { culiboxTheme } = require("../static/monacotheme.js");

const rule = (t, token) => t.rules.find((r) => r.token === token);

test("culiboxTheme uses the tokens", () => {
  const t = culiboxTheme();
  assert.strictEqual(t.base, "vs-dark");
  assert.strictEqual(t.inherit, true);
  assert.deepStrictEqual(t.colors, {
    "editor.background": "#101317",
    "editor.foreground": "#e6e8eb",
    "editorLineNumber.foreground": "#4b525c",
    "editorLineNumber.activeForeground": "#e6e8eb",
    "editor.selectionBackground": "#283a54",
    "editorGutter.addedBackground": "#4ccf8a",
    "editorGutter.modifiedBackground": "#6ea8fe",
    "editorGutter.deletedBackground": "#f06a6a",
    "editorOverviewRuler.addedForeground": "#4ccf8a",
    "editorOverviewRuler.modifiedForeground": "#6ea8fe",
    "editorOverviewRuler.deletedForeground": "#f06a6a",
  });
});

test("culiboxTheme token rules are the GitHub Dark colours of the read view", () => {
  const t = culiboxTheme();
  // Monaco builds a rule's colour with Color.fromHex("#" + foreground), so a rule's hex has no "#".
  assert.strictEqual(rule(t, "keyword").foreground, "ff7b72");
  assert.strictEqual(rule(t, "string").foreground, "a5d6ff");
  assert.strictEqual(rule(t, "number").foreground, "79c0ff");
  assert.strictEqual(rule(t, "comment").foreground, "8b949e");
  assert.strictEqual(rule(t, "type").foreground, "d2a8ff");
  assert.strictEqual(rule(t, "function").foreground, "d2a8ff");
});

test("culiboxTheme overrides the base rules that are more specific than keyword, number and string", () => {
  const t = culiboxTheme();
  assert.strictEqual(rule(t, "keyword.flow").foreground, "ff7b72");
  assert.strictEqual(rule(t, "number.hex").foreground, "79c0ff");
  assert.strictEqual(rule(t, "string.sql").foreground, "a5d6ff");
});

test("culiboxTheme: every rule colour is six hex digits without a hash, every colour is #rrggbb", () => {
  const t = culiboxTheme();
  for (const r of t.rules) assert.match(r.foreground, /^[0-9a-f]{6}$/, r.token);
  for (const [k, v] of Object.entries(t.colors)) assert.match(v, /^#[0-9a-f]{6}$/, k);
});

test("culiboxTheme returns a fresh object so a caller cannot change the next one", () => {
  const a = culiboxTheme();
  a.colors["editor.background"] = "#000000";
  a.rules.length = 0;
  const b = culiboxTheme();
  assert.strictEqual(b.colors["editor.background"], "#101317");
  assert.ok(b.rules.length > 0);
});
