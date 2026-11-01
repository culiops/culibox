"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { viewerToolbarModel } = require("../static/viewertoolbar.js");

const OK = { ok: true };
const base = (over) => Object.assign({
  path: "/h/u/a.go", image: false, box: false, readOnly: false, editing: false, dirty: false,
  markdown: false, auto: false, wrap: false, lines: true,
  insert: OK, reveal: OK, rel: OK, extras: [],
}, over);
const shownOf = (m) => Object.fromEntries(Object.entries(m).map(([k, e]) => [k, e.shown]));
const enabledOf = (m) => Object.fromEntries(Object.entries(m).map(([k, e]) => [k, e.enabled]));
const ALL = ["edit", "save", "cancel", "copy", "path", "find", "more", "pathAbs", "pathRel", "pathInsert", "reveal",
  "reload", "download", "follow", "wrap", "gotoLine", "smaller", "bigger"];

test("host text file, read mode: every control shown and enabled, with the founder's labels", () => {
  const m = viewerToolbarModel(base());
  assert.deepEqual(Object.keys(m), ALL);
  assert.deepEqual(shownOf(m), {
    edit: true, save: false, cancel: false, copy: true, path: true, find: true, more: true,
    pathAbs: true, pathRel: true, pathInsert: true, reveal: true,
    reload: true, download: true, follow: true, wrap: true, gotoLine: true, smaller: true, bigger: true,
  });
  assert.ok(Object.values(m).every((e) => e.enabled === true));
  assert.equal(m.edit.label, "Edit");
  assert.equal(m.copy.label, "Copy");
  assert.equal(m.path.label, "Path ▾");
  assert.equal(m.find.label, "Find");
  assert.equal(m.more.label, "⋯");
  assert.equal(m.save.label, "Save");
  assert.equal(m.cancel.label, "Cancel");
});

test("menu item labels", () => {
  const m = viewerToolbarModel(base());
  assert.deepEqual([m.pathAbs.label, m.pathRel.label, m.pathInsert.label, m.reveal.label],
    ["Copy absolute path", "Copy relative path", "Insert path at prompt", "Reveal in tree"]);
  assert.deepEqual([m.reload.label, m.download.label, m.follow.label, m.wrap.label, m.gotoLine.label, m.smaller.label, m.bigger.label],
    ["Reload", "Download", "Follow file", "Wrap lines", "Go to line…", "A−", "A+"]);
});

test("Follow file is a toggle whose title says how often it reads", () => {
  assert.match(viewerToolbarModel(base()).follow.title, /every 2 seconds/);
  assert.equal(viewerToolbarModel(base({ auto: true })).follow.pressed, true);
  assert.equal(viewerToolbarModel(base()).follow.pressed, false);
  assert.equal(viewerToolbarModel(base({ wrap: true })).wrap.pressed, true);
  assert.equal(viewerToolbarModel(base()).wrap.pressed, false);
});

test("editing: Save and Cancel replace Edit; Reload and Follow are off only while dirty", () => {
  const clean = viewerToolbarModel(base({ editing: true, dirty: false }));
  assert.equal(clean.edit.shown, false);
  assert.equal(clean.save.shown, true);
  assert.equal(clean.cancel.shown, true);
  assert.equal(clean.save.enabled, true);
  assert.equal(clean.cancel.enabled, true);
  assert.equal(clean.reload.enabled, true);
  assert.equal(clean.follow.enabled, true);
  const dirty = viewerToolbarModel(base({ editing: true, dirty: true }));
  assert.equal(dirty.reload.enabled, false);
  assert.equal(dirty.follow.enabled, false);
  assert.match(dirty.reload.title, /Save or cancel/);
  // Everything else is untouched by dirt.
  assert.equal(dirty.download.enabled, true);
  assert.equal(dirty.copy.enabled, true);
  assert.equal(dirty.find.enabled, true);
  assert.equal(dirty.gotoLine.enabled, true);
});

test("read mode never shows Save or Cancel", () => {
  const m = viewerToolbarModel(base());
  assert.equal(m.save.shown, false);
  assert.equal(m.cancel.shown, false);
});

test("box file: no Edit; relative path and Reveal are disabled with the reason as their title", () => {
  const rel = { ok: false, title: "This file is inside a session's box, so it has no path relative to the tree" };
  const reveal = { ok: false, title: "This file is inside a session's box, not on the host" };
  const m = viewerToolbarModel(base({ box: true, rel, reveal }));
  assert.equal(m.edit.shown, false);
  assert.equal(m.pathRel.enabled, false);
  assert.equal(m.pathRel.title, rel.title);
  assert.equal(m.reveal.enabled, false);
  assert.equal(m.reveal.title, reveal.title);
  assert.equal(m.pathAbs.enabled, true);
  assert.equal(m.copy.enabled, true);
});

test("the insert item takes its disabled title from the target rule", () => {
  const insert = { ok: false, title: "This file is inside another session's box" };
  const m = viewerToolbarModel(base({ insert }));
  assert.equal(m.pathInsert.enabled, false);
  assert.equal(m.pathInsert.title, insert.title);
  assert.equal(viewerToolbarModel(base()).pathInsert.enabled, true);
});

test("a boolean rel / reveal / insert is accepted too", () => {
  const m = viewerToolbarModel(base({ rel: false, reveal: false, insert: false }));
  assert.equal(m.pathRel.enabled, false);
  assert.equal(m.reveal.enabled, false);
  assert.equal(m.pathInsert.enabled, false);
  assert.ok(m.pathRel.title && m.reveal.title && m.pathInsert.title);
});

test("read-only file: no Edit, everything else as for a host file", () => {
  const m = viewerToolbarModel(base({ readOnly: true }));
  assert.equal(m.edit.shown, false);
  assert.ok(Object.entries(m).filter(([k]) => k !== "edit" && k !== "save" && k !== "cancel").every(([, e]) => e.enabled));
});

test("image: nothing to copy, find, wrap, number or size; download stays", () => {
  const m = viewerToolbarModel(base({ path: "/h/u/s.png", image: true }));
  assert.equal(m.edit.shown, false);
  for (const k of ["copy", "find", "wrap", "gotoLine", "smaller", "bigger", "follow"]) {
    assert.equal(m[k].enabled, false, k + " is disabled for an image");
  }
  for (const k of ["download", "reload", "pathAbs", "path", "more"]) {
    assert.equal(m[k].enabled, true, k + " still works for an image");
  }
});

test("markdown: Go to line is disabled with the founder-approved reason", () => {
  const m = viewerToolbarModel(base({ path: "/h/u/r.md", markdown: true, lines: false }));
  assert.equal(m.gotoLine.enabled, false);
  assert.equal(m.gotoLine.title, "Markdown is shown rendered — no line numbers");
  assert.equal(m.find.enabled, true);
  assert.equal(m.edit.shown, true);
});

test("a text file with no numbered lines yet (loading, a message) cannot Go to line either", () => {
  const m = viewerToolbarModel(base({ lines: false }));
  assert.equal(m.gotoLine.enabled, false);
  assert.ok(m.gotoLine.title);
  // In the editor the lines exist whatever the read view shows.
  assert.equal(viewerToolbarModel(base({ lines: true, editing: true })).gotoLine.enabled, true);
});

test("extras sit on the primary row after Find and before the overflow button", () => {
  const extras = [{ key: "openInPane", label: "Open in pane", title: "Open this file in a pane, at the same place" }];
  const m = viewerToolbarModel(base({ extras }));
  const keys = Object.keys(m);
  assert.ok(keys.indexOf("find") < keys.indexOf("openInPane"));
  assert.ok(keys.indexOf("openInPane") < keys.indexOf("more"));
  assert.equal(m.openInPane.label, "Open in pane");
  assert.equal(m.openInPane.shown, true);
  assert.equal(m.openInPane.enabled, true);
  assert.equal(m.openInPane.title, extras[0].title);
  assert.equal(viewerToolbarModel(base()).openInPane, undefined);
  // While editing the file would leave its edit behind: off, and it says why.
  const ed = viewerToolbarModel(base({ extras, editing: true }));
  assert.equal(ed.openInPane.enabled, false);
  assert.match(ed.openInPane.title, /Save or cancel/);
});

test("key hints are appended to the titles that have a shortcut, never invented", () => {
  const keys = { find: "K-F", gotoLine: "K-G", save: "K-S", zoomIn: "K-+", zoomOut: "K--" };
  const m = viewerToolbarModel(base({ keys, editing: true }));
  assert.match(m.find.title, /\(K-F\)/);
  assert.match(m.gotoLine.title, /\(K-G\)/);
  assert.match(m.save.title, /\(K-S\)/);
  assert.match(m.bigger.title, /\(K-\+\)/);
  assert.match(m.smaller.title, /\(K--\)/);
  assert.doesNotMatch(viewerToolbarModel(base()).find.title, /\(/);
});

test("every entry has a non-empty title and label, in every state", () => {
  const states = [
    base(), base({ editing: true, dirty: true }), base({ box: true, rel: false, reveal: false }),
    base({ readOnly: true }), base({ image: true }), base({ markdown: true, lines: false }),
    base({ extras: [{ key: "x", label: "X", title: "do x" }], editing: true }), base({ insert: false }),
  ];
  for (const s of states) {
    for (const [k, e] of Object.entries(viewerToolbarModel(s))) {
      assert.ok(typeof e.title === "string" && e.title.length > 0, k + " has a title");
      assert.ok(typeof e.label === "string" && e.label.length > 0, k + " has a label");
      assert.equal(typeof e.shown, "boolean");
      assert.equal(typeof e.enabled, "boolean");
    }
  }
});

test("the model reads no globals and tolerates missing input", () => {
  assert.doesNotThrow(() => viewerToolbarModel());
  assert.doesNotThrow(() => viewerToolbarModel({}));
});
