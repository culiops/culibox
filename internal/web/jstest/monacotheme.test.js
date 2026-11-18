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
    "diffEditor.insertedLineBackground": "#4ccf8a13",
    "diffEditor.removedLineBackground": "#f06a6a18",
    "diffEditor.insertedTextBackground": "#4ccf8a40",
    "diffEditor.removedTextBackground": "#f06a6a40",
    "diffEditorGutter.insertedLineBackground": "#4ccf8a13",
    "diffEditorGutter.removedLineBackground": "#00000000",
    "diffEditorOverview.insertedForeground": "#4ccf8a",
    "diffEditorOverview.removedForeground": "#f06a6a",
  });
});

// `#rrggbbaa` laid over an opaque `[r, g, b]`, as the browser blends it.
const over = (base, hex) => {
  const [r, g, b, a] = [1, 3, 5, 7].map((i) => parseInt(hex.slice(i, i + 2), 16));
  return [r, g, b].map((v, i) => base[i] + ((v - base[i]) * a) / 255);
};
const near = (got, want, what) => got.forEach((v, i) => assert.ok(Math.abs(v - want[i]) <= 8, what + ": " + got.map(Math.round) + " is not within 8 of " + want));

test("culiboxTheme: the diff editors wear mockup B2's tinted lines and the gutter's green and red, not Monaco's olive and pure red", () => {
  const c = culiboxTheme().colors;
  // Monaco's defaults, which these replace: rgba(156,204,44,.2) olive and rgba(255,0,0,.2) red. Every name below must be set.
  for (const k of ["diffEditor.insertedLineBackground", "diffEditor.removedLineBackground", "diffEditor.insertedTextBackground", "diffEditor.removedTextBackground"]) {
    assert.ok(c[k], k + " is set");
  }
  // Translucent tints of the gutter's two colours, laid over the editor, come out within a few units of mockup B2's opaque ones.
  const bg = [0x10, 0x13, 0x17];
  const add = over(bg, c["diffEditor.insertedLineBackground"]);
  const del = over(bg, c["diffEditor.removedLineBackground"]);
  near(add, [0x10, 0x26, 0x1a], "mockup B2 .dl.add");
  near(del, [0x2a, 0x15, 0x17], "mockup B2 .dl.del");
  near(over(add, c["diffEditor.insertedTextBackground"]), [0x1f, 0x50, 0x36], "a changed word over an added line");
  near(over(del, c["diffEditor.removedTextBackground"]), [0x5c, 0x2a, 0x2c], "a changed word over a deleted line");
  assert.match(c["diffEditor.insertedLineBackground"], /^#4ccf8a/, "the gutter's green");
  assert.match(c["diffEditor.removedLineBackground"], /^#f06a6a/, "the gutter's red");
  assert.strictEqual(c["diffEditorGutter.insertedLineBackground"], c["diffEditor.insertedLineBackground"], "the gutter beside an added line wears the line's tint");
  assert.strictEqual(c["diffEditorOverview.insertedForeground"], c["editorGutter.addedBackground"], "the ruler agrees with the gutter");
  assert.strictEqual(c["diffEditorOverview.removedForeground"], c["editorGutter.deletedBackground"]);
});

test("culiboxTheme: the diff backgrounds are not opaque, so a selection on a changed line shows through them", () => {
  const c = culiboxTheme().colors;
  // Monaco: "The color must not be opaque so as not to hide underlying decorations".
  for (const k of ["diffEditor.insertedLineBackground", "diffEditor.removedLineBackground", "diffEditor.insertedTextBackground", "diffEditor.removedTextBackground", "diffEditorGutter.insertedLineBackground"]) {
    assert.match(c[k], /^#[0-9a-f]{6}[0-9a-f]{2}$/, k + " has an alpha");
    const a = parseInt(c[k].slice(7), 16);
    assert.ok(a > 0 && a < 0x80, k + " is a light tint (alpha " + a + "/255)");
  }
});

test("culiboxTheme: the gutter's deleted-line colour is transparent, or the filler beside an added block paints a red strip", () => {
  // Monaco puts a `gutter-delete` alignment zone, as tall as an inserted block, in the gutter column and paints it with this
  // colour (measured with #2a1517: a red strip beside every added block, in Changes and in an inline Compare).
  const c = culiboxTheme().colors;
  assert.strictEqual(parseInt(c["diffEditorGutter.removedLineBackground"].slice(7), 16), 0);
  assert.ok(c["diffEditor.removedLineBackground"], "the real deleted lines keep their colour");
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

test("culiboxTheme: every rule colour is six hex digits without a hash, every colour is #rrggbb (#rrggbbaa for the diff tints)", () => {
  const t = culiboxTheme();
  for (const r of t.rules) assert.match(r.foreground, /^[0-9a-f]{6}$/, r.token);
  for (const [k, v] of Object.entries(t.colors)) assert.match(v, /^diffEditor(Gutter)?\.[a-zA-Z]*Background$/.test(k) ? /^#[0-9a-f]{8}$/ : /^#[0-9a-f]{6}$/, k);
});

test("culiboxTheme returns a fresh object so a caller cannot change the next one", () => {
  const a = culiboxTheme();
  a.colors["editor.background"] = "#000000";
  a.rules.length = 0;
  const b = culiboxTheme();
  assert.strictEqual(b.colors["editor.background"], "#101317");
  assert.ok(b.rules.length > 0);
});
