// Files dock P3 (mockup 5): the explorer's two sections, Changes and the folder tree, each collapse with a chevron, and a
// divider between them sets the split. docksections.js keeps the state and paints it as data-chg-shut / data-tree-shut and
// --dk-chg-max on #drawer-tree; everything that changes on screen is these rules. Source pins, as in
// dock-changes-wire.test.js: the DOM glue is docksections-dom.test.js's, the look is the in-box visual check's.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const html = read("index.html");
const tree = html.slice(html.indexOf('id="drawer-tree"'), html.indexOf('id="drawer-strip"'));
const css = stripComments(read("desktop.css"));

// rulesFor: the declaration text of every rule whose selector list holds exactly `sel`, in source order.
function rulesFor(src, sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(src))) {
    if (m[1].split(/,(?![^(]*\))/).map((x) => x.trim()).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  }
  return out.join(" ");
}
const tag = (id) => { const m = tree.match(new RegExp('<[a-z]+ id="' + id + '"[^>]*>')); return m ? m[0] : ""; };

test("each section's header has a chevron: a real button with aria-expanded, naming what it shows and hides", () => {
  const chg = tag("dock-chg-chev");
  assert.match(chg, /^<button /);
  for (const a of ['type="button"', 'class="dk-chev"', 'aria-expanded="true"', 'aria-controls="dock-chg-list"', 'aria-label="Changes"', 'title="Collapse Changes"']) assert.ok(chg.includes(a), "Changes chevron: " + a);
  const head = tree.slice(tree.indexOf('class="dk-chg-head"'));
  assert.match(head, /^class="dk-chg-head">\s*<button id="dock-chg-chev"/, "the chevron is the header's first child");
  const tr = tag("dock-tree-chev");
  assert.match(tr, /^<button /);
  for (const a of ['type="button"', 'class="dk-chev"', 'aria-expanded="true"', 'aria-controls="files-pane"', 'aria-label="Folder tree"', 'title="Collapse the folder tree"']) assert.ok(tr.includes(a), "tree chevron: " + a);
  // The root name keeps opening the root picker: the chevron is a separate control before it, in one header row.
  assert.match(tree, /<div class="dk-roothead"><button id="dock-tree-chev"[^>]*><\/button><button id="dock-root" /);
});

test("the divider is a focusable horizontal separator between the sections, ahead of the root header", () => {
  const d = tag("dock-secsplit");
  assert.match(d, /^<div /);
  for (const a of ['class="dk-secsplit"', 'role="separator"', 'aria-orientation="horizontal"', 'aria-label="Resize Changes and the folder tree"', 'aria-controls="dock-changes"', 'tabindex="0"', 'title="Drag to resize, double-click to reset"']) assert.ok(d.includes(a), "divider: " + a);
  const at = (s) => tree.indexOf(s);
  assert.ok(at('id="dock-changes"') < at('id="dock-secsplit"'));
  assert.ok(at('id="dock-secsplit"') < at('class="dk-roothead"'));
  assert.ok(at('class="dk-roothead"') < at('class="dr-tnote"'));
  assert.match(tree, /<\/section>\s*(<!--[\s\S]*?-->\s*)?<div id="dock-secsplit"/, "right after the section");
});

test("the section's height: as tall as its rows up to the dragged share, else today's 45%", () => {
  assert.match(rulesFor(css, "#dock-changes"), /max-height:\s*min\(var\(--dk-chg-max,\s*45%\),/);
});

test("collapsed Changes is its header row only: the count and Review stay", () => {
  assert.match(rulesFor(css, '#drawer-tree[data-chg-shut="1"] #dock-changes > :not(.dk-chg-head)'), /display:\s*none/);
  assert.match(rulesFor(css, ".dk-chg-head"), /cursor:\s*pointer/, "the header's free space toggles too");
});

test("a collapsed tree hides its rows and docks its header at the bottom; Changes then takes the column (mockup ③)", () => {
  const shut = '#drawer-tree[data-tree-shut="1"]:has(> #dock-changes:not([hidden]))';
  assert.match(rulesFor(css, shut + " #files-pane"), /display:\s*none/);
  const head = rulesFor(css, shut + " .dk-roothead");
  assert.match(head, /margin-top:\s*auto/);
  assert.match(head, /border-top:\s*1px solid var\(--line\)/);
  const sec = rulesFor(css, '#drawer-tree[data-tree-shut="1"]:not([data-chg-shut="1"]) #dock-changes');
  assert.match(sec, /max-height:\s*none/);
  assert.match(sec, /flex:\s*0 1 auto/);
  assert.match(sec, /border-bottom:\s*0/);
});

test("with no Changes section there is nothing to give the room to: the tree stays drawn and its chevron is not shown", () => {
  assert.match(rulesFor(css, "#drawer-tree:has(> #dock-changes[hidden]) #dock-tree-chev"), /visibility:\s*hidden/);
  assert.match(rulesFor(css, '#drawer[data-noroot="1"] .dk-roothead'), /display:\s*none/);
});

test("the divider is drawn only while both sections are open and shown", () => {
  for (const sel of ['#drawer-tree[data-chg-shut="1"] .dk-secsplit', '#drawer-tree[data-tree-shut="1"] .dk-secsplit', "#dock-changes[hidden] + .dk-secsplit", '#drawer[data-noroot="1"] .dk-secsplit']) {
    assert.match(rulesFor(css, sel), /display:\s*none/, sel);
  }
  const d = rulesFor(css, ".dk-secsplit");
  assert.match(d, /cursor:\s*row-resize/);
  assert.match(d, /touch-action:\s*none/);
});

test("founder Q1: the grips' pill shows on hover, keyboard focus and drag only, the divider's and the column splitter's alike", () => {
  for (const s of [".dk-secsplit", ".dr-split"]) {
    assert.match(rulesFor(css, s + "::after"), /opacity:\s*0/, s + " pill hidden at rest");
    for (const st of [":hover", ":focus-visible", ".dragging"]) assert.match(rulesFor(css, s + st + "::after"), /opacity:\s*1/, s + st);
    for (const st of [":hover", ":focus-visible", ".dragging"]) assert.match(rulesFor(css, s + st), /background:\s*color-mix\(in srgb, var\(--accent\) 40%, transparent\)/, s + st + " band");
  }
});

test("the chevron is the tree's own glyph, turned when open, with the kit's focus ring", () => {
  assert.match(rulesFor(css, ".dk-chev::before"), /content:\s*"\\203A"/);
  assert.match(rulesFor(css, '.dk-chev[aria-expanded="true"]::before'), /transform:\s*rotate\(90deg\)/);
  assert.match(rulesFor(css, ".dk-chev:focus-visible"), /outline:\s*var\(--focus\)/);
});

test("the phone's stylesheet knows none of it", () => {
  assert.doesNotMatch(stripComments(read("app.css")), /dk-chev|dk-secsplit|dk-roothead|data-chg-shut|data-tree-shut/);
});

test("an explicit ask opens a collapsed section: Reveal in tree (not the automatic reveal), and the folded strip's Changes button", () => {
  const ft = read("filetree.js");
  const body = stripComments(ft.slice(ft.indexOf("async function ftReveal("), ft.indexOf("\n}\n", ft.indexOf("async function ftReveal("))));
  assert.match(body, /^async function ftReveal\(path, opts\) \{\s*const o = opts \|\| \{\};\s*if \(!o\.auto && typeof dockSecOpen === "function" && ftDocked\(\)\) dockSecOpen\("tree"\);/, "first thing in ftReveal, only for an explicit ask (P4: never the automatic reveal) and only for the dock's tree (the phone's Reveal leaves the desktop choice alone)");
  assert.match(stripComments(read("app.js")), /\$\("drawer-strip-changes"\)\.onclick = \(\) => \{\s*if \(typeof dockSecOpen === "function"\) dockSecOpen\("changes"\);\s*dockStripOpen\(\);\s*\};/);
});

test("founder Q3: the Changes header keeps one row at the default 1280 width (203px column, 3.2px to spare on main)", () => {
  // Main: 10 padding + CHANGES 59.3 + 6 + "42 changed" 67.9 + 6 + Review 40.6 + 10 = 199.8 of 203. The chevron may not cost more.
  const head = rulesFor(css, ".dk-chg-head");
  assert.match(head, /column-gap:\s*4px/);
  assert.match(head, /padding:\s*0 10px 0 0/);
  const chev = rulesFor(css, ".dk-chev");
  assert.match(chev, /width:\s*14px/);
  assert.match(chev, /margin-right:\s*-4px/);
  assert.match(rulesFor(css, ".dk-chev::after"), /inset:\s*-2px -5px/, "a 24px press area around the 14x20 box");
});

test("review 1: a breadcrumb press opens a collapsed tree, the root crumb too (it never reaches ftReveal)", () => {
  const fd = read("filesdrawer.js");
  const start = fd.indexOf("function dockCrumbsReveal(");
  const body = stripComments(fd.slice(start, fd.indexOf("\n}\n", start)));
  const open = body.indexOf('dockSecOpen("tree")');
  assert.ok(open > 0, "dockCrumbsReveal opens the tree");
  assert.ok(open < body.indexOf('if (where === "root")'), "before the root crumb's early return");
});

test("review 1: a share stored on a taller window never takes the tree's header and 3 rows on a shorter one", () => {
  // 31 the top row, 26 the root header, 14 the tree body's padding (measured in Chromium), 3 rows of the tree's size.
  assert.match(rulesFor(css, "#dock-changes"), /max-height:\s*min\(var\(--dk-chg-max,\s*45%\),\s*calc\(100% - 71px - 3 \* max\(22px, var\(--fs-tree\) \* 22 \/ 12\)\)\)/, "a tree row is never under 22px (#drawer .ft-node min-height)");
});
