// P3 Task 4: the notes UI's wiring in the Changes view. What the code does is run in editor-notes.test.js (a Changes view on the fake
// Monaco) and notesmodel.test.js (the pure half); these are the source pins for what no run reaches: the registry row, the calls the
// modules make into each other, the order of the page's scripts and when the stored notes are read.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const SC = require("../static/shortcuts.js");

test("editornotes.js re-anchors on a replace from disk and persists through saveNotes, and never writes note text as HTML", () => {
  const js = stripComments(read("editornotes.js"));
  assert.match(js, /\breanchor\(/);
  assert.match(js, /\bsaveNotes\(/);
  assert.match(js, /\bloadNotes\(/);
  assert.match(js, /\bonReplace\b/, "the host subscribes to editorSyncOnReplace");
  assert.match(js, /textContent/);
  assert.doesNotMatch(js, /innerHTML|insertAdjacentHTML|outerHTML/, "notes are shown in the control plane's origin");
});

test("the registry has Add note (c and Mod+Shift+A), scope changes, and the help screen lists it", () => {
  const e = SC.SHORTCUTS.find((s) => s.id === "editor.addNote");
  assert.ok(e, "the entry exists");
  assert.equal(e.scope, "changes", "the global dispatcher never sees it: Monaco gets it as a command (editorBind)");
  assert.equal(e.status, "bound");
  assert.deepStrictEqual(e.chords, [{ code: "KeyC" }, { mod: true, shift: true, code: "KeyA" }]);
  assert.equal(SC.shortcutHint("editor.addNote", false), "C");
  const rows = SC.helpRows(false).filter((r) => r.label === e.label);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].keys, "C, Ctrl+Shift+A");
});

test("the Changes view calls the notes in when it opens and when it is let go, and the base's refill checks the old-side notes", () => {
  const js = stripComments(read("editordiff.js"));
  assert.match(js, /editorNotesAttach\(tab\)/);
  assert.match(js, /editorNotesDetach\(tab\)/);
  assert.match(js, /editorNotesBaseMoved\(tab\)/);
  const d = js.indexOf("function editorDiffDrop(");
  assert.ok(d > 0 && js.indexOf("editorNotesDetach(tab)", d) > d, "detach is in editorDiffDrop, before the diff editor is disposed");
  assert.ok(js.indexOf("editorNotesDetach(tab)", d) < js.indexOf("d.diff.dispose()", d));
});

test("the diff editor has the glyph margin on, and the keys and the selection bar are wired", () => {
  assert.match(stripComments(read("editordiff.js")), /glyphMargin: true/);
  const tab = stripComments(read("editortab.js"));
  assert.match(tab, /scEntry\("editor\.addNote"\)/);
  assert.match(tab, /editorBindNoteKeys\(dockEditor\)/);
  assert.match(stripComments(read("editornotes.js")), /selBarAddAction\(\{[^}]*key: "note"/);
});

test("the script is on the page after editorreview.js and notesmodel.js, and the stored notes are read once the session list is", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("editornotes.js") > at("editorreview.js") && at("editorreview.js") > at("notesmodel.js"));
  const app = stripComments(read("app.js"));
  assert.match(app, /typeof notesLoad === "function"\) notesLoad\(/);
  const i = app.indexOf("reviewMarksLoad(sessions");
  assert.ok(i > 0 && app.indexOf("notesLoad(", i) > i && app.indexOf("notesLoad(", i) - i < 400, "next to reviewMarksLoad, after the first real list");
});

test("the notes follow the file for as long as the tab is open: tracked from the model's creation to dispose, and notesFor reads the decorations back", () => {
  const tab = stripComments(read("editortab.js"));
  assert.match(tab, /editorNotesTrack\(this\)/);
  assert.match(tab, /editorNotesUntrack\(this\)/);
  assert.ok(tab.indexOf("editorNotesUntrack(this)") < tab.indexOf("this.model.dispose()"), "before the model goes");
  const js = stripComments(read("editornotes.js"));
  assert.match(js, /function editorNotesTrack\([^)]*\)[\s\S]*editorSyncOnReplace\(tab,/, "one subscription per tab, not per Changes view");
  assert.match(js, /NeverGrowsWhenTypingAtEdges/);
  assert.match(js, /function notesFor\(sid\) \{\s*ntSyncAll\(\);/, "the lines are read back before notesFor answers");
  assert.match(js, /function ntFlush\(\) \{[\s\S]*ntSyncAll\(\)/, "and before a save");
});

test("a note's card is an overlay widget over an empty room, never inside the view zone (Monaco's .view-zones is aria-hidden)", () => {
  const js = stripComments(read("editornotes.js"));
  assert.match(js, /addOverlayWidget\(/);
  assert.match(js, /removeOverlayWidget\(/);
  assert.match(js, /onDomNodeTop/);
  assert.match(js, /role", "group"/);
  assert.doesNotMatch(js, /domNode: node/, "the zone's domNode is the empty room");
});
