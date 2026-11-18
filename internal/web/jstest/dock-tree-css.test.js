// The Files dock's explorer tree (Task 9a, spec 5.4, mockup B1) is a stylesheet, markup and wiring a node test cannot render.
// These are source pins for them (filetree-keys.test.js and filetree.test.js run the DOM half, the visual check proves the look):
// every restyle rule hangs from #drawer, so the phone's tree keeps app.css's look; the row, indent, guide, chevron, tint and
// selection values are the spec's; and the explorer column opened from the folded strip carries a visible fold control.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const css = stripComments(read("desktop.css"));
const app = stripComments(read("app.css"));

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

test("the tree restyle is the dock's: nothing in app.css reads --lvl, and the phone keeps its box-drawing branch and mono rows", () => {
  assert.ok(!/--lvl/.test(app), "app.css (the phone's tree) does not know the depth property");
  assert.match(rulesFor(app, "#filetree"), /font-family:\s*ui-monospace/);
  assert.match(rulesFor(app, ".ft-branch"), /white-space:\s*pre/);
  assert.strictEqual(rulesFor(app, ".ft-branch").includes("display"), false);
});

test("Inter rows, 22px tall at the default text size, the indent from --lvl", () => {
  const tree = rulesFor(css, "#drawer #filetree");
  assert.match(tree, /font-family:\s*var\(--font-ui\)/);
  const row = rulesFor(css, "#drawer .ft-node");
  assert.match(row, /min-height:\s*22px/, "the floor; the row's own height is its line (below), which grows with the setting");
  assert.ok(!/(^|[\s;])height:/.test(row), "no fixed height: a larger text size must be able to grow the row");
  assert.match(row, /padding:\s*0 8px 0 calc\(var\(--lvl, 0\) \* 14px \+ 8px\)/);
});

test("the dock tree keeps Settings' File tree text size: text and line are ratios of --fs-tree, exactly 13px in 22px rows at its default 12", () => {
  const tree = rulesFor(css, "#drawer #filetree");
  const size = /font-size:\s*calc\(var\(--fs-tree\) \* (\d+) \/ (\d+)\)/.exec(tree);
  const line = /line-height:\s*calc\(var\(--fs-tree\) \* (\d+) \/ (\d+)\)/.exec(tree);
  assert.ok(size, "font-size reads --fs-tree (the type test allows a calc with no px)");
  assert.ok(line, "line-height reads --fs-tree, so the row grows with the text");
  const DEFAULT = 12; // index.html's pre-paint script and fontprefs.js DEFAULTS.filetree on desktop
  assert.strictEqual(DEFAULT * size[1] / size[2], 13, "the spec's 13px text");
  assert.strictEqual(DEFAULT * line[1] / line[2], 22, "the spec's 22px rows");
  assert.ok(read("index.html").includes("filetree:['--fs-tree',12]"), "12 is the desktop default the ratios are built on");
  assert.match(read("fontprefs.js"), /DEFAULTS = \{[^}]*filetree: 12/);
  // The chevron grows with the text as well, and the guides do not depend on it: they are geometry (14px a level).
  assert.match(rulesFor(css, "#drawer .ft-node.dir::before"), /font-size:\s*calc\(var\(--fs-tree\) \* 15 \/ 12\)/);
  assert.match(rulesFor(css, "#drawer .ft-node::before"), /width:\s*14px/);
  // app.css still has its consumer (the phone's tree), so one setting drives both.
  assert.match(rulesFor(app, "#filetree"), /font-size:\s*var\(--fs-tree\)/);
});

test("indent guides: a 1px --line stripe every 14px, as wide as the indent, one per ancestor level", () => {
  const row = rulesFor(css, "#drawer .ft-node");
  assert.match(row, /background-image:\s*repeating-linear-gradient\(to right, var\(--line\) 0 1px, transparent 1px 14px\)/);
  assert.match(row, /background-size:\s*calc\(var\(--lvl, 0\) \* 14px\) 100%/);
  assert.match(row, /background-repeat:\s*no-repeat/);
  // The hover and selected washes set background-color only: app.css's `background:` shorthand would clear the image, and
  // the guides would vanish under the pointer.
  for (const sel of ["#drawer .ft-node:hover", "#drawer .ft-node.ft-sel"]) {
    const b = rulesFor(css, sel);
    assert.match(b, /background-color:/, sel);
    assert.ok(!/background:/.test(b), sel + " must not use the shorthand");
  }
});

test("the box-drawing branch is hidden, and a folder gets a chevron that turns when it is open; a file gets the same width as a spacer", () => {
  assert.match(rulesFor(css, "#drawer .ft-branch"), /display:\s*none/);
  const spacer = rulesFor(css, "#drawer .ft-node::before");
  assert.match(spacer, /content:\s*""/);
  assert.match(spacer, /flex:\s*none/);
  assert.match(spacer, /width:\s*14px/, "one indent step, so a file's icon lines up with its folder siblings'");
  assert.match(rulesFor(css, "#drawer .ft-node.dir::before"), /content:\s*"\\203A"/, "the frame's › glyph");
  assert.match(rulesFor(css, '#drawer .ft-node.dir[aria-expanded="true"]::before'), /transform:\s*rotate\(90deg\)/);
});

test("changed names are tinted (M accent, U and A ok, conflicts red, a folder with changes below accent or red) and the folder dot is not drawn", () => {
  const m = rulesFor(css, "#drawer .ft-node:has(> .ft-git-m) .ft-name");
  assert.match(m, /color:\s*color-mix\(in srgb, var\(--accent\) 50%, var\(--fg\)\)/);
  const u = rulesFor(css, "#drawer .ft-node:has(> .ft-git-u, > .ft-git-a) .ft-name");
  assert.match(u, /color:\s*color-mix\(in srgb, var\(--ok\) 50%, var\(--fg\)\)/);
  const dir = rulesFor(css, "#drawer .ft-node:has(> .ft-git-dot) .ft-name");
  assert.match(dir, /color:\s*color-mix\(in srgb, var\(--accent\) 50%, var\(--fg\)\)/);
  assert.ok(css.indexOf("#drawer .ft-node:has(> .ft-git-dot) .ft-name") > css.indexOf("#drawer .ft-node:has(> .ft-git-u, > .ft-git-a) .ft-name"),
    "the folder rule comes last, so a folder holding only new files is accent, not ok (equal specificity: order decides)");
  // A conflict (file letter C) and a folder whose strongest change is a conflict or a deletion (dot code c or d) are red, as the
  // letters are; the folder one is stronger than the plain folder rule, so the order of the two does not matter.
  const red = /color:\s*color-mix\(in srgb, var\(--red\) 50%, var\(--fg\)\)/;
  assert.match(rulesFor(css, "#drawer .ft-node:has(> .ft-git-c) .ft-name"), red);
  assert.match(rulesFor(css, "#drawer .ft-node:has(> .ft-git-dot:is(.ft-git-c, .ft-git-d)) .ft-name"), red);
  assert.match(rulesFor(css, "#drawer .ft-node.dir"), /color:\s*var\(--fg\)/, "folders are not accent-coloured, or a tint would say nothing");
});

test("a row is the containing block of its hidden spans, so they can never widen the page's scrollable overflow", () => {
  // The visually-hidden dot and session span are position:absolute. With no positioned ancestor inside the tree their
  // containing block is #drawer (position:relative), which sits OUTSIDE the #filetree scroller: a span on a row below the
  // visible part of the tree then counts toward the document's scroll height, and wheeling past the end of the tree scrolls the
  // whole app away (measured: html.scrollHeight 720 -> 2136 with the last of 91 rows marked). Positioning the row keeps the
  // span inside the scroller's own overflow. It must stay a bare `position: relative`: no offsets, no z-index (no stacking context).
  const row = rulesFor(css, "#drawer .ft-node");
  assert.match(row, /(^|[\s;])position:\s*relative/);
  assert.ok(!/(^|[\s;])(top|left|right|bottom|z-index|transform):/.test(row), "no offset or stacking context on the row");
  for (const sel of ["#drawer .ft-git-dot", "#drawer .ft-chg"]) assert.match(rulesFor(css, sel), /position:\s*absolute/, sel + " is the absolute one this protects");
  // ... and nothing between the span and the scroller is positioned instead (the scroller itself is a plain overflow box).
  assert.ok(!/position:/.test(rulesFor(css, "#drawer #filetree")));
  assert.match(rulesFor(app, "#filetree"), /overflow:\s*auto/);
  assert.ok(!/position:/.test(rulesFor(app, "#filetree")));
});

test("the folder dot and the session span are visually hidden, not display:none, so their role and label stay in the accessibility tree", () => {
  for (const sel of ["#drawer .ft-git-dot", "#drawer .ft-chg"]) {
    const b = rulesFor(css, sel);
    assert.ok(!/display:\s*none/.test(b), sel + " keeps its accessibility node");
    assert.match(b, /position:\s*absolute/, sel);
    assert.match(b, /width:\s*1px/, sel);
    assert.match(b, /height:\s*1px/, sel);
    assert.match(b, /clip:\s*rect\(0 0 0 0\)/, sel);
    assert.match(b, /overflow:\s*hidden/, sel);
  }
});

test("the selected row is the accent tint with a 1px ring, and keeps the session bar beside it", () => {
  const sel = rulesFor(css, "#drawer .ft-node.ft-sel");
  assert.match(sel, /background-color:\s*var\(--accent-tint\)/);
  assert.match(sel, /box-shadow:\s*inset 0 0 0 1px /);
  const both = rulesFor(css, "#drawer .ft-node.ft-sel:has(> .ft-chg)");
  assert.match(both, /box-shadow:\s*inset 2px 0 0 var\(--accent\), inset 0 0 0 1px /, "a selected row this session changed has both");
});

test("the rules that read --lvl, draw a ::before or select with :has() on a tree row hang from #drawer", () => {
  // Only these: the focus ring and the ⋯'s hover rules are older desktop rules on the same rows and are not the restyle.
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  const loose = [];
  while ((m = re.exec(css))) {
    for (const part of m[1].split(/,(?![^(]*\))/).map((x) => x.trim())) {
      if (/(^|\s)\.ft-(node|name|icon|branch|chg|git)\b/.test(part) && /--lvl|\.ft-chg|::before|:has\(/.test(m[0]) && !part.startsWith("#drawer")) loose.push(part);
    }
  }
  assert.deepStrictEqual(loose, []);
});

test("the type icons are the ones that ship: the dock does not restyle .ft-icon", () => {
  assert.strictEqual(rulesFor(css, "#drawer .ft-icon"), "");
  assert.match(rulesFor(app, ".ft-icon"), /font-family:\s*"Symbols Nerd Font"/);
});

test("the explorer column has a fold control for the strip's transient open, shown only then", () => {
  const html = read("index.html");
  const goto = html.slice(html.indexOf('class="dr-goto"'), html.indexOf('class="dr-tnote"'));
  assert.match(goto, /<button id="drawer-tree-fold" type="button" class="btn btn-ghost btn-icon btn-sm"[^>]*aria-label="Fold the explorer"/);
  assert.match(rulesFor(css, "#drawer-tree-fold"), /display:\s*none/);
  assert.match(rulesFor(css, '#drawer[data-tree-open="1"] #drawer-tree-fold'), /display:\s*inline-flex/);
  const js = read("app.js");
  assert.match(js, /\$\("drawer-tree-fold"\)\.onclick = \(\) => dockTreeFoldPress\(\)/);
  const paint = js.slice(js.indexOf("function paintDrawerLayout"), js.indexOf("// dockForcesFull"));
  assert.match(paint, /dr\.dataset\.treeOpen = "1"/);
  assert.match(paint, /delete dr\.dataset\.treeOpen/);
});

test("every name the dock draws from the workspace or git is isolated for bidirectional text, as the branch name is", () => {
  // A file or folder name (or a branch) may hold Unicode bidi format characters; unisolated, one could reorder the counts, the
  // letter or the separators drawn next to it.
  for (const sel of [".dk-name", ".dk-crumb", ".dk-nm", ".dk-dir", "#drawer .ft-name", ".dk-root-name", ".dk-root-path", ".dk-br-name"]) {
    assert.match(rulesFor(css, sel), /unicode-bidi:\s*isolate/, sel + " is isolated");
  }
  assert.doesNotMatch(app, /unicode-bidi/, "the phone's tree (app.css) is left as it was: the rule is the desktop block's");
});
