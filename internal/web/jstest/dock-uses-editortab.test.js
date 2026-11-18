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

// Task 6 (P2): disk changes under the editor. The behaviour is run for real in editortab-contract.test.js; these pin the wiring that
// has no unit: the script's place in the page, and the three places that keep the one poll in step with what is on screen.
test("the page loads editorsync.js after editortab.js and before the dock and app.js: a DOM module the dock's paint and the page host call", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("editorsync.js") > at("editortab.js"), "after the class whose lifecycle it follows");
  assert.ok(at("editorsync.js") < at("filesdrawer.js") && at("editorsync.js") < at("app.js"));
  assert.match(stripComments(fn("filesdrawer.js", "drawerPaint")), /editorSyncSync\(\)/, "every dock paint keeps the poll in step");
  const host = stripComments(read("pagehost.js"));
  assert.strictEqual((host.match(/editorSyncSync\(\)/g) || []).length, 2, "a page view opening and closing");
  assert.match(read("editorsync.js"), /addEventListener\("visibilitychange"/, "and the page being hidden or shown");
});

test("the dock's toolbar host gives the bar the session's name and a close that asks first when the buffer holds an edit", () => {
  const host = stripComments(read("filesdrawer.js").slice(read("filesdrawer.js").indexOf("const drawerToolbarHost = {")));
  assert.match(host, /sessionName: \(\) => .*drawerSessionName\(drawerState\.sessionId\)/);
  assert.match(host, /closeTab: \(v\) => \{[^}]*dockCloseTab\(key\)/, "dockCloseTab asks Discard / Cancel for a dirty tab; the deleted bar's Close is that close");
});
