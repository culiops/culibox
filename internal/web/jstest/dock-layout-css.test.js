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

test("pressing Full does not resize the terminals: the grid keeps the dock's width reserved while Full is on and not forced", () => {
  // Full takes the dock out of the flow, so the grid used to widen under it (every terminal resized to the wider size) and shrink
  // back on Exit full: two PTY resizes per toggle. The margin holds the dock's pushed width, which --dock-w still carries.
  const css = stripComments(read("desktop.css"));
  const sel = '#layout:has(> #drawer[data-max="1"]:not([data-forced="1"]):not([hidden])) > #grid';
  assert.match(rulesFor(css, sel).join(" "), /^\s*margin-right:\s*var\(--dock-w\);\s*$/);
  // Not while forced (the pushed dock does not fit, so none is reserved), and not once the dock is hidden.
  assert.ok(sel.includes(':not([data-forced="1"])') && sel.includes(":not([hidden])"));
  // --dock-w is the pushed width in non-forced Full mode too: paintDrawerLayout writes it whenever the layout is not full.
  const app = stripComments(read("app.js"));
  const fn = app.slice(app.indexOf("function paintDrawerLayout("), app.indexOf("function dockForcesFull("));
  assert.match(fn, /if \(!lay\.full\) dr\.style\.setProperty\("--dock-w", lay\.dockW \+ "px"\)/);
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

test("the header has the pin and the session chip and keeps close; the Full toggle moved into the tab bar", () => {
  const html = read("index.html");
  const head = html.slice(html.indexOf('class="dr-head"'), html.indexOf('id="drawer-body"'));
  for (const id of ["drawer-title", "drawer-sess", "drawer-hint", "drawer-pin", "drawer-x"]) {
    assert.ok(head.includes('id="' + id + '"'), "#" + id + " is in the dock header");
  }
  assert.ok(!head.includes('id="drawer-max"'), "the toggle is not in the header any more (spec: Toggled from the editor bar)");
  const acts = html.slice(html.indexOf('id="dock-acts"'), html.indexOf('id="dock-crumbs"'));
  assert.match(acts, /<button id="drawer-max" type="button" class="btn btn-sm"[^>]*>[\s\S]*<span id="drawer-max-label">Full<\/span><\/button>/, "Full / Exit full / Show terminal, in the bar's actions area");
  assert.ok(!/aria-pressed|aria-label/.test(acts.slice(acts.indexOf('id="drawer-max"'), acts.indexOf("</button>"))), "its label is its name, so it carries no pressed state or second name");
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

test("the window's resize lays the open dock out before the terminals fit, and the phone does not run it", () => {
  const app = read("app.js");
  const dock = app.indexOf('window.addEventListener("resize", () => { if (drawerState.open && drawerDesktop()) positionDrawer(); });');
  const fit = app.indexOf('window.addEventListener("resize", fitAll);');
  assert.ok(dock > 0, "a resize listener that positions the dock, only for an open desktop dock");
  assert.ok(fit > dock, "registered before fitAll, so it runs first and fitAll measures the new dock width");
});

test("the agent strip is the last child of #drawer, after the body: dot, name, status, last line, Show terminal with its chord", () => {
  const html = read("index.html");
  const dr = html.slice(html.indexOf('<aside id="drawer"'), html.indexOf("</aside>", html.indexOf('<aside id="drawer"')));
  const strip = dr.slice(dr.indexOf('id="dock-agent"'));
  assert.ok(dr.indexOf('id="dock-agent"') > dr.indexOf('id="drawer-strip"'), "after the dock body, at the bottom of the column");
  const at = (id) => strip.indexOf('id="' + id + '"');
  const order = ["dock-agent-dot", "dock-agent-name", "dock-agent-st", "dock-agent-last", "dock-agent-term", "dock-agent-kbd"];
  order.forEach((id, i) => { assert.ok(at(id) > 0, "#" + id); if (i) assert.ok(at(id) > at(order[i - 1]), id + " after " + order[i - 1]); });
  assert.match(strip, /<button id="dock-agent-term" type="button" class="btn btn-sm">/);
  assert.ok(!/Insert at prompt|insert/i.test(strip), "no Insert at prompt: P2 builds the gate that says when typing into a terminal is safe");
  assert.ok(!/aria-live/.test(strip), "a 4 s poll must not be announced");
});

test("the strip is shown only in Full mode, is one 36px line whose last-activity part is the one that gives way", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(rulesFor(css, ".dk-agent").join(" "), /display:\s*none/);
  assert.match(rulesFor(css, ".dk-agent").join(" "), /height:\s*36px/);
  assert.match(rulesFor(css, ".dk-agent").join(" "), /flex:\s*none/);
  assert.match(rulesFor(css, ".dk-agent").join(" "), /white-space:\s*nowrap/);
  assert.match(rulesFor(css, '#drawer[data-max="1"] .dk-agent').join(" "), /display:\s*flex/);
  const last = rulesFor(css, ".dk-ag-last").join(" ");
  assert.match(last, /flex:\s*1 1 auto/);
  assert.match(last, /min-width:\s*0/);
  assert.match(last, /text-overflow:\s*ellipsis/);
  assert.match(last, /overflow:\s*hidden/);
  assert.match(rulesFor(css, ".dk-ag-st").join(" "), /flex:\s*none/);
});

test("forced Full mode has one Show terminal, the agent strip's: the tab row's toggle is hidden while forced", () => {
  const css = stripComments(read("desktop.css"));
  // Both buttons did the same thing (close the dock to show the terminal). The strip is shown whenever the dock is in Full mode,
  // forced or not, so hiding the tab row's costs nothing; its actions area goes too, or its padding would be an empty gap.
  assert.match(rulesFor(css, '#drawer[data-forced="1"] #dock-acts').join(" "), /display:\s*none/);
  assert.match(rulesFor(css, '#drawer[data-max="1"] .dk-agent').join(" "), /display:\s*flex/, "the strip that stays");
  assert.ok(!/#drawer-max\[aria-pressed/.test(css));
  assert.ok(!/#drawer\[data-forced="1"\] \.dk-agent/.test(css), "and nothing hides the strip's button");
});

test("Exit full wears the accent border (mockup B5): one token rule, on the toggle while Full mode is on", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(rulesFor(css, '#drawer[data-max="1"] #drawer-max').join(" "), /^\s*border-color:\s*var\(--accent\);\s*$/);
});
