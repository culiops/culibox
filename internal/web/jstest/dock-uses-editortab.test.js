// Task 4 (P2): the desktop Files dock's tab body is the Monaco EditorTab, not the
// ViewerTab. Source pins for the wiring that no unit can reach (the dock is DOM and
// the editor is vendored), plus the registry row Alt+Z needs. The behaviour is
// proved in the browser (the task report).
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
// fn: the source of a top-level function, up to its closing brace at column 0.
function fn(file, name) {
  const text = read(file);
  const i = text.search(new RegExp("^(async )?function " + name + "\\(", "m"));
  assert.ok(i >= 0, name + " is in " + file);
  return text.slice(i, text.indexOf("\n}\n", i) + 2);
}

test("the dock mounts an EditorTab for a tab, and builds no ViewerTab of its own", () => {
  const body = stripComments(fn("filesdrawer.js", "dockMount"));
  assert.match(body, /new EditorTab\(tab\.path, \{/);
  assert.ok(!body.includes("new ViewerTab("), "an image or a binary file gets its ViewerTab inside the EditorTab");
  assert.match(body, /host: drawerToolbarHost/);
  assert.match(body, /v\.onUpdate = \(\) => drawerPaint\(true\)/);
});

test("the dock asks the viewer for its crumb note, so a dirty buffer says Unsaved", () => {
  assert.match(stripComments(fn("filesdrawer.js", "dockRender")), /crumbNote/);
});

test("editor.wrap is a registry row on Alt+Z in the editor scope", () => {
  const SC = require("../static/shortcuts.js");
  const row = SC.SHORTCUTS.find((s) => s.id === "editor.wrap");
  assert.ok(row, "editor.wrap is in the registry");
  assert.strictEqual(row.scope, "editor");
  assert.strictEqual(row.status, "bound");
  assert.deepStrictEqual(row.chords, [{ alt: true, code: "KeyZ" }]);
  assert.strictEqual(SC.shortcutHint("editor.wrap", false), "Alt+Z");
  assert.strictEqual(SC.shortcutHint("editor.wrap", true), "⌥Z");
});

test("the page loads the editor scripts before the dock, and Monaco itself only on demand", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  for (const f of ["editorstatus.js", "monacoload.js", "editortab.js"]) {
    assert.ok(at(f) > 0, f + " is a page script");
    assert.ok(at(f) < at("filesdrawer.js"), f + " loads before filesdrawer.js");
  }
  assert.ok(!/vendor\/monaco/.test(html), "index.html never names a Monaco file: monacoload.js injects them on the first open");
  assert.ok(at("monacokeys.js") < at("editortab.js") && at("monacotheme.js") < at("monacoload.js"), "the pure policy modules come first");
});
