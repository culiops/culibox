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

test("Inter 13px rows, 22px tall, the indent from --lvl", () => {
  assert.match(rulesFor(css, "#drawer #filetree"), /font-family:\s*var\(--font-ui\)/);
  assert.match(rulesFor(css, "#drawer #filetree"), /font-size:\s*var\(--t-md\)/, "13px, the kit step (the type test allows no raw px size)");
  const row = rulesFor(css, "#drawer .ft-node");
  assert.match(row, /height:\s*22px/);
  assert.match(row, /padding:\s*0 8px 0 calc\(var\(--lvl, 0\) \* 14px \+ 8px\)/);
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

test("changed names are tinted (M accent, U and A ok, a folder with changes below accent) and the folder dot is hidden", () => {
  const m = rulesFor(css, "#drawer .ft-node:has(> .ft-git-m) .ft-name");
  assert.match(m, /color:\s*color-mix\(in srgb, var\(--accent\) 50%, var\(--fg\)\)/);
  const u = rulesFor(css, "#drawer .ft-node:has(> .ft-git-u, > .ft-git-a) .ft-name");
  assert.match(u, /color:\s*color-mix\(in srgb, var\(--ok\) 50%, var\(--fg\)\)/);
  const dir = rulesFor(css, "#drawer .ft-node:has(> .ft-git-dot) .ft-name");
  assert.match(dir, /color:\s*color-mix\(in srgb, var\(--accent\) 50%, var\(--fg\)\)/);
  assert.ok(css.indexOf("#drawer .ft-node:has(> .ft-git-dot) .ft-name") > css.indexOf("#drawer .ft-node:has(> .ft-git-u, > .ft-git-a) .ft-name"),
    "the folder rule comes last, so a folder holding only new files is accent, not ok (equal specificity: order decides)");
  assert.match(rulesFor(css, "#drawer .ft-git-dot"), /display:\s*none/);
  assert.match(rulesFor(css, "#drawer .ft-node.dir"), /color:\s*var\(--fg\)/, "folders are not accent-coloured, or a tint would say nothing");
});

test("the selected row is the accent tint with a 1px ring, and keeps the session bar beside it", () => {
  const sel = rulesFor(css, "#drawer .ft-node.ft-sel");
  assert.match(sel, /background-color:\s*var\(--accent-tint\)/);
  assert.match(sel, /box-shadow:\s*inset 0 0 0 1px /);
  const both = rulesFor(css, "#drawer .ft-node.ft-sel:has(> .ft-chg)");
  assert.match(both, /box-shadow:\s*inset 2px 0 0 var\(--accent\), inset 0 0 0 1px /, "a selected row this session changed has both");
});

test("every tree rule of the dock hangs from #drawer (and the type icons keep the ones that ship)", () => {
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  const loose = [];
  while ((m = re.exec(css))) {
    for (const part of m[1].split(/,(?![^(]*\))/).map((x) => x.trim())) {
      if (/(^|\s)\.ft-(node|name|icon|branch|chg|git)\b/.test(part) && /--lvl|\.ft-chg|::before|:has\(/.test(m[0]) && !part.startsWith("#drawer")) loose.push(part);
    }
  }
  assert.deepStrictEqual(loose, []);
  assert.strictEqual(rulesFor(css, "#drawer .ft-icon"), "", "the icon is app.css's Nerd Font glyph, as shipped");
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
