// Source test: every global shortcut runs through the ONE capture-phase
// dispatcher in app.js, driven by the shortcuts.js registry. The old per-feature
// document keydown listeners each re-derived "where is focus" and drifted apart;
// this pins that none of them comes back.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8")
  .split("\n")
  .map((l) => l.replace(/^\s*\/\/.*$/, "").replace(/\s\/\/ .*$/, ""))
  .join("\n");

const app = src("app.js");
const palette = src("palette.js");
const drawer = src("filesdrawer.js");
const shell = src("shell.js");
const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");

test("app.js has exactly one keydown listener that dispatches, and it is capture-phase", () => {
  const re = /document\.addEventListener\("keydown",[\s\S]*?\}, true\);/g;
  const hits = (app.match(re) || []).filter((b) => b.includes("dispatchShortcut"));
  assert.strictEqual(hits.length, 1);
  assert.strictEqual((app.match(/dispatchShortcut\(/g) || []).length, 1);
  assert.match(hits[0], /shortcutContext\(/);
  assert.match(hits[0], /IS_MAC/);
});

test("the old per-feature key plumbing is gone", () => {
  for (const name of ["JUMP_MOD", "SIDEBAR_SHORTCUT_MAC", "shortcutsBlocked"]) {
    assert.ok(!app.includes(name), "app.js still has " + name);
    assert.ok(!shell.includes(name), "shell.js still has " + name);
  }
  assert.ok(!palette.includes("isPaletteHotkey"));
});

test("palette.js and filesdrawer.js add no document keydown listener for shortcuts", () => {
  assert.ok(!palette.includes('document.addEventListener("keydown"'));
  assert.ok(!drawer.includes('e.code !== "KeyP"'));
});

test("handlers are bound by registry id", () => {
  for (const id of ["palette.open", "palette.open.alt"]) assert.ok(palette.includes(`bindShortcut("${id}"`), id);
  assert.ok(drawer.includes('bindShortcut("files.goto"'));
  for (const id of ["sidebar.toggle", "files.toggle", "session.jump", "shell.below", "zoom.in", "zoom.out", "zoom.reset", "help.open"]) {
    assert.ok(app.includes(`bindShortcut("${id}"`), id);
  }
});

test("shell.below stands down when the focused cell is not visible", () => {
  const i = app.indexOf('bindShortcut("shell.below"');
  assert.ok(i > 0);
  const body = app.slice(i, app.indexOf("});", i));
  assert.match(body, /cellVisible\(focusedCell\)/);
  assert.match(body, /e\.repeat/);
});

test("session.jump consumes an out-of-range digit (R5)", () => {
  const i = app.indexOf('bindShortcut("session.jump"');
  assert.ok(i > 0);
  const body = app.slice(i, app.indexOf("});", i));
  assert.match(body, /if \(s\) openTerminal\(/);
  assert.match(body, /return true;/);
});

test("index.html loads /shortcuts.js before /spawnform.js", () => {
  const a = html.indexOf('<script src="/shortcuts.js"></script>');
  const b = html.indexOf('<script src="/spawnform.js"></script>');
  assert.ok(a > 0, "shortcuts.js script tag missing");
  assert.ok(b > a, "shortcuts.js must load before spawnform.js");
});
