// P3 Task 3: the review loop's wiring in the Files dock (diff modes, F7 across files, Mark reviewed, the review markers in the
// Changes list). The behaviour is run for real in editor-review.test.js (the Changes view on the fake Monaco) and
// reviewmodel.test.js (the pure half); these are the source pins for what no run reaches: the registry's chords, the calls the
// modules make into each other, the order of the page's scripts and when the stored marks are read.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const SC = require("../static/shortcuts.js");

test("the registry has Next change (F7) and Previous change (Shift+F7), scope editor", () => {
  const next = SC.SHORTCUTS.find((s) => s.id === "editor.nextChange");
  const prev = SC.SHORTCUTS.find((s) => s.id === "editor.prevChange");
  assert.ok(next && prev, "both entries exist");
  for (const s of [next, prev]) {
    assert.equal(s.scope, "editor", "the global dispatcher never sees them: Monaco gets them as commands (editorBind)");
    assert.equal(s.status, "bound");
    assert.equal(s.chords.length, 1);
  }
  assert.deepStrictEqual(next.chords[0], { code: "F7" });
  assert.deepStrictEqual(prev.chords[0], { shift: true, code: "F7" });
  assert.equal(SC.shortcutHint("editor.nextChange", false), "F7");
  assert.equal(SC.shortcutHint("editor.prevChange", false), "Shift+F7");
});

test("dockchanges.js reads reviewedCount and reviewedState (what they do is run in dock-changes-dom.test.js)", () => {
  const js = stripComments(read("dockchanges.js"));
  assert.match(js, /\breviewedCount\(/);
  assert.match(js, /\breviewedState\(/);
  assert.match(js, /dk-ck/);
  assert.match(js, /dk-nt/);
});

test("editordiff.js asks Monaco for side by side by default and for its own inline fallback, and keeps the prefs in viewerprefs.js", () => {
  const js = stripComments(read("editordiff.js"));
  assert.match(js, /useInlineViewWhenSpaceIsLimited: true/);
  assert.match(js, /renderSideBySide:/);
  assert.match(js, /diffWordWrap/);
  assert.match(js, /viewerPrefs\.getDiffMode\(\)/);
  assert.match(js, /viewerPrefs\.getDiffWrap\(\)/);
  assert.match(js, /function diffModeSet\(/);
  const prefs = stripComments(read("viewerprefs.js"));
  assert.match(prefs, /getDiffMode/);
  assert.match(prefs, /setDiffWrap/);
});

test("F7 and Shift+F7 are bound through the registry, onto Monaco, and Alt+Z follows the focus", () => {
  const tab = stripComments(read("editortab.js"));
  assert.match(tab, /editorBind\(ed, "editor\.nextChange"/);
  assert.match(tab, /editorBind\(ed, "editor\.prevChange"/);
  assert.doesNotMatch(tab, /editorBind\(dockEditor, "editor\.wrap", \(\) => toggleViewerWrap\(\)\)/, "Alt+Z is Wrap diff while a Changes view has the focus");
});

test("the script is on the page after editordiff.js and reviewmodel.js, and the stored marks are read once the session list is", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("editorreview.js") > at("reviewmodel.js") && at("reviewmodel.js") > 0);
  assert.ok(at("editorreview.js") < at("filesdrawer.js"));
  const app = stripComments(read("app.js"));
  const i = app.indexOf("lastSessions = sessions;");
  assert.ok(i > 0);
  assert.match(app.slice(i, i + 600), /reviewMarksLoad\(/, "after the first successful list");
  const js = stripComments(read("editorreview.js"));
  assert.match(js, /loadReviewed\(/);
  assert.match(js, /saveReviewed\(/);
  assert.match(js, /reviewMarkFor\(/, "a mark is always built by the model's builder");
});
