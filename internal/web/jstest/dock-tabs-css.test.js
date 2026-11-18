// The Files dock's tab strip is markup, a stylesheet and wiring between files that a node test cannot render. These are
// source pins for those (dock-tabs-dom.test.js runs the logic, dock-strip.test.js the elements; the visual check proves the
// result on screen): the strip and the crumbs are in the viewer column in the right order, the rules that keep the actions
// from being pushed out and mark the preview and the dirty tab are in the desktop block, a single click and a double-click on
// a tree row are different opens, and the stored tabs are read after the session list and not before.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
function rulesFor(css, sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(css))) if (m[1].trim() === sel) out.push(m[2]);
  return out.join(" ");
}
// fn: the source of a top-level function, up to its closing brace at column 0.
function fn(file, name) {
  const text = read(file);
  const i = text.search(new RegExp("^(async )?function " + name + "\\(", "m"));
  assert.ok(i >= 0, name + " is in " + file);
  return text.slice(i, text.indexOf("\n}\n", i) + 2);
}

test("the viewer column is the strip, then the crumbs, then the viewer, then the empty state", () => {
  const html = read("index.html");
  const col = html.slice(html.indexOf('id="drawer-viewer"'), html.indexOf('id="drawer-split"'));
  const at = (id) => col.indexOf('id="' + id + '"');
  for (const id of ["dock-bar", "dock-tabs", "dock-acts", "dock-crumbs", "dock-trail", "dock-edited", "drawer-vhost", "drawer-vempty"]) assert.ok(at(id) > 0, "#" + id + " is in the viewer column");
  assert.ok(at("dock-bar") < at("dock-crumbs") && at("dock-crumbs") < at("drawer-vhost") && at("drawer-vhost") < at("drawer-vempty"));
  assert.ok(at("dock-tabs") < at("dock-acts") && at("dock-acts") < at("dock-crumbs"), "the actions area is beside the tabs, in the bar");
  assert.ok(at("dock-bar") < at("dock-conflict") && at("dock-conflict") < at("dock-crumbs"), "the conflict bar's slot is between the strip and the crumbs (mockup B4)");
  assert.match(col, /id="dock-tabs"[^>]*role="tablist"/);
  assert.match(col, /<div id="dock-acts" class="dk-acts"><button id="drawer-max"[^>]*>[\s\S]*<\/button><\/div>/, "the Full toggle is in it (Task 8b), the bar's only action");
  assert.match(col, /id="dock-bar"[^>]*hidden/, "no tab, no strip");
  assert.match(col, /id="dock-crumbs"[^>]*hidden/);
});

test("dockstrip.js loads after the pure modules it calls and before filesdrawer.js, which draws through it", () => {
  const html = read("index.html");
  const at = (f) => html.indexOf('src="/' + f + '"');
  assert.ok(at("docktabs.js") > 0 && at("dockcrumbs.js") > 0);
  assert.ok(at("dockstrip.js") > at("docktabs.js"));
  assert.ok(at("dockstrip.js") < at("filesdrawer.js"));
  assert.ok(at("filesdrawer.js") < at("app.js"));
  assert.match(read("dockstrip.js"), /^"use strict";/);
});

test("the tabs scroll sideways and shrink away; the actions area never shrinks, so it is never pushed out", () => {
  const css = stripComments(read("desktop.css"));
  const tabs = rulesFor(css, ".dk-tabs");
  assert.match(tabs, /overflow-x:\s*auto/);
  assert.match(tabs, /min-width:\s*140px/, "the strip has a floor of its own (a tab name is about 130 px): the bar's actions compress first (below)");
  assert.match(tabs, /flex:\s*1 1 auto/);
  assert.match(tabs, /position:\s*relative/, "offsetLeft is read from the strip (dockstrip.js dkReveal)");
  assert.match(rulesFor(css, ".dk-acts"), /flex:\s*none/);
  assert.match(rulesFor(css, ".dk-acts:empty"), /display:\s*none/);
  assert.match(rulesFor(css, ".dk-tab"), /flex:\s*0 0 auto/, "a tab keeps its width and the strip scrolls, rather than squeezing names to nothing");
  assert.match(rulesFor(css, ".dk-edited"), /flex:\s*none/, "the Edited-by label is not cut before the path is");
  assert.match(rulesFor(css, ".dk-trail"), /min-width:\s*0/);
  assert.match(rulesFor(css, ".dk-crumb"), /flex:\s*0 100 auto/, "the folders give way first");
  assert.match(rulesFor(css, ".dk-crumb:last-child"), /flex:\s*0 0 auto;\s*max-width:\s*100%/, "and the file does not shrink at all, unless it alone is wider than the row");
});

test("a narrow dock's bar shows the toggle, Follow and Full as icons: the bar is a size container and the words are clipped, not removed", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(rulesFor(css, ".dk-bar"), /container:\s*dkbar \/ inline-size/, "the bar is what the query measures (the dock's own width is the viewport's minus the sidebar and the panes)");
  assert.match(rulesFor(css, ".dk-seg-ic"), /display:\s*none/, "wide: the words");
  const q = /@container dkbar \(max-width:\s*(\d+)px\)\s*\{([\s\S]*?\n  \})/.exec(css);
  assert.ok(q, "one container query for the bar");
  assert.ok(Number(q[1]) >= 600 && Number(q[1]) <= 700, "below about 640 px of bar (a markdown file's toggle, Follow and Full with words are 380 px, and a tab is 130)");
  assert.match(q[2], /\.dk-seg-ic\s*\{\s*display:\s*block/, "narrow: the icons");
  assert.match(q[2], /\.dk-seg-t, \.dk-follow-t, #drawer-max-label\s*\{[^}]*clip:\s*rect\(0 0 0 0\)/, "and the words clipped away, still read by a screen reader");
  assert.ok(!/\.dk-seg-t[^{]*\{[^}]*display:\s*none/.test(q[2]), "never display: none, which would take the name away");
  const full = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
  assert.match(full, /<span id="drawer-max-label">Full<\/span>/, "Full's word is in a span of its own for the query to clip");
});

test("the preview tab is italic, the active one is the viewer's own colour, and a dirty tab swaps its cross for a dot", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(rulesFor(css, ".dk-prev .dk-name"), /font-style:\s*italic/);
  assert.match(rulesFor(css, ".dk-tab-on, .dk-tab-on:hover"), /background:\s*var\(--bg\)/);
  assert.match(rulesFor(css, ".dk-dirty"), /display:\s*none/);
  assert.match(rulesFor(css, '.dk-tab[data-dirty="1"] .dk-dirty'), /display:\s*block/);
  assert.match(rulesFor(css, '.dk-tab[data-dirty="1"] .dk-x'), /display:\s*none/);
  assert.match(rulesFor(css, '.dk-tab[data-dirty="1"]:hover .dk-x, .dk-tab[data-dirty="1"] .dk-x:focus-visible'), /display:\s*inline-flex/, "a dirty tab can still be closed by hand (it asks)");
  assert.match(rulesFor(css, ".dk-tab:hover .dk-x, .dk-tab-on .dk-x, .dk-x:focus-visible"), /visibility:\s*visible/);
});

test("every strip rule is in the desktop stylesheet and none in app.css, which the phone shares", () => {
  assert.ok(!/\.dk-|#dock-(bar|tabs|crumbs|trail|acts|edited)/.test(stripComments(read("app.css"))), "the phone knows nothing of the strip");
  assert.match(stripComments(read("desktop.css")), /\.dk-tab \{/);
});

test("a single click on a file row opens a preview tab and a double-click keeps it; the phone's click is openFile and its double-click nothing", () => {
  const tree = read("filetree.js");
  // The dock test is one function (Task 9a moved it out of the row loop, where the click's selection and the right-click share it).
  assert.match(fn("filetree.js", "ftDocked"), /typeof drawerOwnsFileClicks === "function" && drawerOwnsFileClicks\(\)/, "desktop and the Files dock only");
  const i = tree.indexOf("row.onclick = () => {\n        if (ftDocked())");
  assert.ok(i > 0, "the file row's click asks ftDocked");
  const block = tree.slice(i, tree.indexOf("}\n  }\n  if (rootCall)", i));
  assert.match(block, /row\.onclick = \(\) => \{\s*if \(ftDocked\(\)\) \{ ftSelect\(row\); drawerShowFile\(full\); \}\s*else openFile\(full\);\s*\};/);
  assert.match(block, /row\.ondblclick = \(\) => \{ if \(ftDocked\(\)\) drawerShowFile\(full, \{ preview: false \}\); \};/);
});

test("the dock's toolbar host has no Open in pane and tells the dock of an edit and of a dirty change", () => {
  const dr = read("filesdrawer.js");
  const host = dr.slice(dr.indexOf("const drawerToolbarHost = {"), dr.indexOf("// drawerPaint brings"));
  assert.ok(!/openInPane|extras|drawerOpenInPane/.test(host));
  assert.ok(!/openInPane|drawerOpenInPane/.test(stripComments(dr).replace(/\/\/.*$/gm, "")), "the function is gone with the pane viewers");
  assert.match(host, /onEditStart:\s*\(v\) => dockPinViewer\(v\)/);
  assert.match(host, /onDirtyChange:/);
});

test("the viewer tells its host when an edit starts, once the editor is up, and when the buffer turns dirty", () => {
  const vt = read("viewer-tab.js");
  const edit = vt.slice(vt.indexOf("  enterEdit() {"), vt.indexOf("  renderEditBar() {"));
  assert.match(edit, /this\.cm = CodeMirror\(/);
  assert.ok(edit.indexOf("this.cm = CodeMirror(") < edit.indexOf("this.host.onEditStart(this)"), "after the editor exists, so dirty() answers");
  assert.ok(edit.indexOf('this.cm.on("change", () => this.noteDirty())') > 0);
  assert.match(vt, /this\.host = \(opts && opts\.host\) \|\| null;/);
  assert.match(vt, /buildViewerToolbar\(this, this\.host\)/);
  // A pane viewer has no host: nothing is scheduled per keystroke for it.
  assert.match(vt, /if \(this\.dirtyTimer \|\| !this\.host \|\| !this\.host\.onDirtyChange\) return;/);
});

test("the stored tabs are read once the session list is known, never on the path that gave up on it", () => {
  const app = read("app.js");
  const boot = app.slice(app.indexOf("(async function restoreBoot("), app.indexOf("initFileTree();"));
  const giveUp = boot.indexOf("endRestore(false)");
  const restore = boot.indexOf("dockRestore(sessions);");
  const layout = boot.indexOf("restoreLayout(sessions);");
  const reopen = boot.indexOf("dockReopen();");
  const tryAt = boot.indexOf("try {");
  const finallyAt = boot.indexOf("finally {");
  assert.ok(giveUp > 0 && restore > giveUp, "after the give-up return: a page with no list never reads, so never writes");
  assert.ok(restore < layout, "before the layout restore, so a migration (Task 8b) merges into what is stored");
  assert.ok(tryAt > giveUp && tryAt < restore && layout < finallyAt && reopen < finallyAt, "inside the try whose finally ends the boot restore, so a throw cannot stall it");
  assert.ok(reopen > layout, "the dock reopens after the layout: the focused pane is not known before");
  // The write is gated on that read.
  const save = fn("filesdrawer.js", "dockSaveNow");
  assert.match(save, /if \(!dockLoaded\) return;/);
  assert.match(fn("filesdrawer.js", "dockSaveSoon"), /if \(!dockLoaded \|\| dockSaveTimer\) return;/);
});

test("the strip and the crumbs are repainted from git status, the Activity answer and the viewers", () => {
  assert.match(fn("gitstatus.js", "gitPaintTree"), /dockRender\(\)/);
  // The Activity answer (the tree's own load, and the dock watch's poll while the agent works) is written by drawerSetChanged.
  assert.match(fn("filesdrawer.js", "drawerSetChanged"), /dockRender\(\)/);
  assert.match(fn("filesdrawer.js", "drawerLoadChanged"), /drawerSetChanged\(sid, files\)/);
  assert.match(fn("filesdrawer.js", "drawerPaint"), /dockRender\(\)/);
});

test("a dock viewer is closed, forgotten and re-pointed through the dock, not as a pane tab", () => {
  const tabs = read("tabs.js");
  assert.match(fn("tabs.js", "closeTabEverywhere"), /dockHasViewer\(tab\)\) \{ dockCloseViewer\(tab\); return; \}/);
  assert.match(fn("tabs.js", "closeViewersFor"), /dockForget\(path\)/);
  assert.match(fn("tabs.js", "repointViewer"), /dockHasViewer\(tab\)\) return dockRepointViewer\(tab, path, line\);/);
  assert.ok(!/drawerViewerTab|drawerClearViewer/.test(tabs + read("app.js") + read("viewer-tab.js") + read("filesdrawer.js")), "the one-viewer accessors are gone");
});
