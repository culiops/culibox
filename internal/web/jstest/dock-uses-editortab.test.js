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
  assert.ok(!body.includes("new ViewerTab("), "an image or a binary file gets its DockFileView inside the EditorTab");
  assert.match(body, /host: drawerToolbarHost/);
  assert.match(body, /v\.onUpdate = \(\) => drawerPaint\(true\)/);
});

// #344: every file the dock opens uses the dock's header. The phone's viewer (ViewerTab, with its own toolbar row) is not the dock's any more.
test("the dock never builds a ViewerTab: EditorTab mounts a DockFileView for a file that is not text, and the page loads its scripts before editortab.js", () => {
  const tab = stripComments(read("editortab.js"));
  assert.ok(!/new ViewerTab\(/.test(tab), "no ViewerTab in editortab.js");
  assert.match(tab, /new DockFileView\(\{/);
  assert.match(tab, /dockKindOf\(extOf\(this\.path\)\)/, "the kind is decided from the extension, before any text read");
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  for (const f of ["filekinds.js", "dockfileview.js"]) assert.ok(at(f) > 0 && at(f) < at("editortab.js"), f + " loads before editortab.js");
  assert.ok(at("filekinds.js") < at("dockfileview.js") && at("copyfile.js") < at("filekinds.js"), "the pure helpers first, after the extension lists");
  assert.ok(at("upload.js") < at("dockfileview.js"), "humanBytes is upload.js's");
  const view = stripComments(read("dockfileview.js"));
  assert.ok(!/innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment/.test(view), "a file's name is only ever text in the view");
  assert.ok(!/<(iframe|object|embed)\b|createElement\("(iframe|object|embed)"\)/.test(view), "a PDF is never embedded");
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

// Task 7 (P2): Changes vs HEAD and the gutter. The behaviour is run for real in editortab-contract.test.js; these pin the wiring that
// has no unit: the script's place in the page, the two places that tell the diff the status or the scope moved, and the host's session.
test("the page loads editordiff.js after linediff.js, editortab.js and editorsync.js and before the dock and app.js", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("editordiff.js") > at("editorsync.js"), "a DOM module the tab calls, after the class and the poll");
  assert.ok(at("editordiff.js") > at("linediff.js"), "it diffs with the pure module");
  assert.ok(at("editordiff.js") < at("filesdrawer.js") && at("editordiff.js") < at("app.js"));
});

test("a status that lands and a scope or base that moves each ask the editor's diff to look at its key (editorDiffSync)", () => {
  assert.match(stripComments(fn("gitstatus.js", "gitPaintTree")), /editorDiffSync\(\)/, "a new HEAD in the tree's status means a new base text");
  assert.match(stripComments(fn("dockchanges.js", "dockChangesRefresh")), /editorDiffSync\(\)/, "the scope toggle and the base landing both come through here");
});

test("the dock's toolbar host names the session whose scope the editor's diff follows", () => {
  const host = stripComments(read("filesdrawer.js").slice(read("filesdrawer.js").indexOf("const drawerToolbarHost = {")));
  assert.match(host, /sessionId: \(\) => drawerState\.sessionId \|\| null/);
});

test("the editor's diff asks the server for the file by path and scope only, never a revision, and never from a keystroke", () => {
  const src = stripComments(read("editordiff.js"));
  assert.match(src, /"\/api\/git\/show"/);
  assert.match(src, /editorDiffPost\(\{ path: tab\.path, scope: k\.scope \}\)/);
  assert.strictEqual((src.match(/editorDiffPost\(/g) || []).length, 2, "defined once and asked in one place");
  // The one place that listens to the buffer only schedules the debounced recompute.
  assert.match(src, /onDidChangeContent\(\(\) => editorDiffSoon\(tab\)\)/);
  assert.match(src, /const DG_DEBOUNCE_MS = 300;/);
});
