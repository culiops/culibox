// P3 Task 3b: the "All changes" dock tab. The behaviour is run in reviewall.test.js (the section lifecycle on a fake Monaco and a
// fake IntersectionObserver) and reviewmodel.test.js (the pure half); these are the source pins for what no run reaches: the calls the
// modules make into each other, the order of the page's scripts, and the numbers the brief fixed.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

test("reviewall.js: lazy sections (IntersectionObserver), collapsed unchanged regions, the cap of 100, the scope of the session", () => {
  const js = stripComments(read("reviewall.js"));
  assert.match(js, /hideUnchangedRegions:\s*\{\s*enabled:\s*true/);
  assert.match(js, /new IntersectionObserver\(/);
  assert.match(js, /readOnly:\s*true/);
  assert.match(js, /editorDiffOptions\(\)/); // renderSideBySide from diffMode, with useInlineViewWhenSpaceIsLimited: the single-file view's options
  assert.match(stripComments(read("editordiff.js")), /useInlineViewWhenSpaceIsLimited:\s*true/);
  assert.match(js, /getContentHeight\(\)/);
  assert.match(js, /\bdockScope\(/);
  assert.match(js, /Open files one by one from the Changes list/);
  assert.match(js, /\beditorNotesAttachHost\(/);
  assert.match(js, /\beditorNotesDetachHost\(/);
  assert.match(js, /\bimageDiffMount\(/);
  assert.match(js, /\breviewMark\(/);
  assert.match(js, /\bdrawerShowFile\(/);
  const model = stripComments(read("reviewmodel.js"));
  assert.match(model, /REVIEW_ALL_MAX\s*=\s*100\b/);
  assert.match(model, /REVIEW_ALL_HEIGHT\s*=\s*1200\b/);
});

test("the tab is a dock tab kind review keyed r:<sid>, and is not stored", () => {
  const tabs = stripComments(read("docktabs.js"));
  assert.match(tabs, /"r:"\s*\+\s*sid/);
  const drawer = stripComments(read("filesdrawer.js"));
  assert.match(drawer, /new ReviewAllTab\(/);
  assert.match(drawer, /dockOpenReview\(/);
});

test("the Changes list scrolls to the section while the tab is active; Review and View all open the tab", () => {
  const changes = stripComments(read("dockchanges.js"));
  assert.match(changes, /reviewAllScrollTo\(/);
  assert.match(changes, /reviewAllOpen\(/);
  const review = stripComments(read("editorreview.js"));
  assert.match(review, /reviewAllOpen\(/); // the View all button of the single-file header
  assert.match(review, /reviewAllSync\(/); // the rows, marks and notes the tab draws move with editorReviewRefresh
  const keys = stripComments(read("editortab.js"));
  assert.match(keys, /reviewAllStep\(/); // F7 / Shift+F7 reach the sections first
});

test("the page loads reviewall.js after imagediff.js, editorreview.js and editornotes.js", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("reviewall.js") > 0, "reviewall.js is loaded");
  for (const f of ["reviewmodel.js", "imagediff.js", "editorreview.js", "editornotes.js"]) assert.ok(at(f) > 0 && at(f) < at("reviewall.js"), f + " first");
});
