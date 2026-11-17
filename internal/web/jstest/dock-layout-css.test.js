// The Files dock is a column beside the pane grid that pushes the panes aside, not an overlay
// drawer. These are source pins for the parts a node test cannot render: the stylesheet and the
// markup that make it a column (the visual check proves it on screen). Full mode is the one
// place the dock still floats over the panes, and only under [data-max].
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");

// rulesFor: the declaration text of every rule whose selector is exactly `sel` (one selector, not a list),
// in source order. desktop.css rules do not nest except the one (hover: none) block, which has no #drawer.
function rulesFor(css, sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(css))) {
    if (m[1].trim() === sel) out.push(m[2]);
  }
  return out;
}

test("#drawer is a flex column of --dock-w, in the flow, and is absolute only under [data-max]", () => {
  const css = stripComments(read("desktop.css"));
  const plain = rulesFor(css, "#drawer");
  assert.ok(plain.length >= 1, "a plain #drawer rule");
  assert.ok(plain.some((r) => /flex:\s*0 0 var\(--dock-w/.test(r)), "flex: 0 0 var(--dock-w)");
  assert.ok(!plain.some((r) => /position:\s*absolute/.test(r)), "the plain rule does not take #drawer out of the flow");
  assert.ok(plain.some((r) => /position:\s*relative/.test(r)), "relative, so the width handle can hang off its left edge");
  // No other rule on the #drawer element itself (its [hidden], [data-kind] and the like) may be absolute either.
  const itself = /^#drawer(\[[^\]]*\]|:[\w-]+(\([^)]*\))?)*$/;
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(css))) {
    if (!/position:\s*absolute/.test(m[2])) continue;
    for (const part of m[1].split(",").map((x) => x.trim())) {
      if (itself.test(part)) assert.ok(/\[data-max/.test(part), "position:absolute on `" + part + "` must be under [data-max]");
    }
  }
  const max = rulesFor(css, '#drawer[data-max="1"]');
  assert.ok(max.some((r) => /position:\s*absolute/.test(r)), "Full mode is the overlay: absolute under data-max");
});

test("Full mode covers the pane area; the dock never sets a top (the header is the top of the column)", () => {
  const css = stripComments(read("desktop.css"));
  const max = rulesFor(css, '#drawer[data-max="1"]').join(" ");
  assert.match(max, /top:\s*0/);
  assert.match(max, /width:\s*var\(--grid-w/);
  assert.ok(!/style\.top/.test(read("app.js").slice(read("app.js").indexOf("function positionDrawer"), read("app.js").indexOf("function restoreDrawerNodes"))),
    "positionDrawer no longer hangs the drawer below the pane header");
});

test("the dock body is ordered viewer, splitter, explorer, then the folded strip", () => {
  const html = read("index.html");
  const dr = html.slice(html.indexOf('<aside id="drawer"'), html.indexOf("</aside>", html.indexOf('<aside id="drawer"')));
  const body = dr.slice(dr.indexOf('id="drawer-body"'));
  const at = (id) => body.indexOf('id="' + id + '"');
  for (const id of ["drawer-viewer", "drawer-split", "drawer-tree", "drawer-strip"]) assert.ok(at(id) > 0, id + " is inside #drawer-body");
  assert.ok(at("drawer-viewer") < at("drawer-split"), "viewer before the splitter");
  assert.ok(at("drawer-split") < at("drawer-tree"), "splitter before the tree");
  assert.ok(at("drawer-tree") < at("drawer-strip"), "tree before the strip");
});

test("the header has the pin and the session chip, and keeps max and close", () => {
  const html = read("index.html");
  const head = html.slice(html.indexOf('class="dr-head"'), html.indexOf('id="drawer-body"'));
  for (const id of ["drawer-title", "drawer-sess", "drawer-hint", "drawer-pin", "drawer-max", "drawer-x"]) {
    assert.ok(head.includes('id="' + id + '"'), "#" + id + " is in the dock header");
  }
  assert.match(head, /<button id="drawer-pin"[^>]*aria-pressed="false"/, "the pin is a toggle button");
  assert.ok(head.indexOf('id="drawer-sess"') < head.indexOf('id="drawer-pin"'), "chip before the pin, as in the mockup");
});

test("the strip holds Explorer, Changes with a count, and Go to file", () => {
  const html = read("index.html");
  const strip = html.slice(html.indexOf('id="drawer-strip"'), html.indexOf("</aside>", html.indexOf('id="drawer-strip"')));
  for (const id of ["drawer-strip-tree", "drawer-strip-changes", "drawer-strip-count", "drawer-strip-goto"]) {
    assert.ok(strip.includes('id="' + id + '"'), "#" + id);
  }
});

test("the empty state tells the user how to open a file", () => {
  assert.match(read("index.html"), /id="drawer-vempty"[^>]*>Open a file from the tree, or click a path in the terminal</);
});

test("every dock rule is inside the desktop block: the phone never sees a dock", () => {
  // desktop-scope.test.js proves the file is one block; this proves the dock rules are in the file, not in app.css.
  assert.ok(!/#drawer-(pin|strip|hint)|--dock-w/.test(read("app.css")), "app.css (shared with the phone) knows nothing of the dock");
  assert.match(read("desktop.css"), /\.dr-strip \{/);
});
