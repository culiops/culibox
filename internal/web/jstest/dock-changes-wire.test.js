// Task 9b: the explorer's top row, the Changes section and New file are markup, a stylesheet and wiring a node test cannot
// render. These are source pins for them (dock-changes-dom.test.js runs the real dockchanges.js against a fake page, and
// changesmodel.test.js the pure half; the visual check proves the look): the Changes section sits in the explorer column
// ahead of the tree, reads its rows from changesRows and its counts from /api/git/numstat, every desktop rule is the
// desktop's, and New file goes through the name prompt and POST /api/fs/newfile.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
const html = read("index.html");
// The explorer column's own markup: from its opening tag to the folded strip that follows it.
const tree = html.slice(html.indexOf('id="drawer-tree"'), html.indexOf('id="drawer-strip"'));

test("dockchanges.js builds the section from changesRows and asks /api/git/numstat and /api/git/base", () => {
  const js = stripComments(read("dockchanges.js"));
  assert.match(js, /changesView\(/, "the section's model is the pure changesView");
  assert.match(read("changesmodel.js"), /changesRows\(/, "which is built on changesRows");
  assert.match(js, /\/api\/git\/numstat/);
  assert.match(js, /\/api\/git\/base/);
  // The client never names a ref: the base body is the path alone, the numstat body the path, a scope and fresh.
  assert.doesNotMatch(js, /baseRef|mergeBase/, "no ref is read or sent from here");
  assert.match(js, /\bdockScope\b/);
  assert.match(js, /\bgitBaseFor\b/);
  assert.match(js, /agent-box:changesScope|DKC_SCOPE_KEY|CHANGES_SCOPE_KEY/);
});

test("#dock-changes is in the explorer column, ahead of the root header and of the tree that is moved in after it", () => {
  assert.ok(tree.includes('id="dock-changes"'), "#dock-changes is inside #drawer-tree");
  const at = (s) => tree.indexOf(s);
  assert.ok(at('class="dr-goto"') >= 0 && at('class="dr-goto"') < at('id="dock-changes"'), "under the top row");
  assert.ok(at('id="dock-changes"') < at('id="dock-root"'), "ahead of the root header");
  assert.ok(at('id="dock-root"') < at('class="dr-tnote"'), "and of the note");
  // The tree is the existing #files-pane, moved in by appendChild: it lands after everything the column holds.
  assert.ok(html.indexOf('id="files-pane"') < html.indexOf('id="drawer-tree"'), "#files-pane starts in the sidebar");
  assert.match(stripComments(read("app.js")), /moveIntoDrawer\(\$\("files-pane"\), \$\("drawer-tree"\) \|\| body\)/);
  assert.match(stripComments(read("app.js")), /body\.appendChild\(node\)/);
});

test("the top row has Go to file with its chord, New file and a ⋯ menu; the section has its scope toggle, branch row and Review", () => {
  const top = tree.slice(0, tree.indexOf('id="dock-changes"'));
  assert.match(top, /id="drawer-goto-in"/);
  assert.match(top, /id="dock-newfile"[^>]*aria-label="New file"/);
  assert.match(top, /id="dock-more"[^>]*aria-haspopup="menu"/);
  const sec = tree.slice(tree.indexOf('id="dock-changes"'), tree.indexOf('id="dock-root"'));
  assert.match(sec, /class="seg dk-scope"/);
  assert.match(sec, /data-scope="head"[^>]*>Uncommitted</);
  assert.match(sec, /data-scope="branch"[^>]*>Branch</);
  assert.match(sec, /class="dk-branch"/);
  assert.match(sec, /id="dock-review"/);
});

test("the dock's explorer hides the old toolbar, and every new rule is inside the desktop block", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(css, /#drawer #files-phd\s*\{[^}]*display:\s*none/);
  for (const sel of [".dk-scope", ".dk-branch", ".dk-mine", ".dk-row", "#dock-changes", ".dk-prov"]) {
    assert.ok(css.includes(sel), sel + " is styled in desktop.css (the one desktop block)");
  }
  assert.doesNotMatch(stripComments(read("app.css")), /\.dk-(row|scope|branch|mine|prov)|#dock-changes/, "app.css (the phone) knows none of it");
});

test("dockchanges.js loads after filesdrawer.js and before app.js; the tree and the status call into it behind typeof guards", () => {
  const order = [...html.matchAll(/<script src="\/([^"]+)"/g)].map((m) => m[1]);
  assert.ok(order.indexOf("dockchanges.js") > order.indexOf("filesdrawer.js"), "after filesdrawer.js");
  assert.ok(order.indexOf("dockchanges.js") < order.indexOf("app.js"), "before app.js");
  assert.match(stripComments(read("gitstatus.js")), /typeof dockChangesOnStatus === "function"/);
  assert.match(stripComments(read("gitstatus.js")), /typeof dockChangesRender === "function"/);
});

test("New file: the name prompt, then POST /api/fs/newfile, then the tree refresh, then a pinned tab; 409 is the 'name already exists' toast", () => {
  assert.match(stripComments(read("fsops.js")), /function fsNewFile\(dir, name\)\s*\{\s*return fsPost\("\/api\/fs\/newfile", \{ dir, name \}\);/);
  const text = read("filetree.js");
  const start = text.indexOf("async function promptNewFile(");
  assert.ok(start >= 0, "promptNewFile is in filetree.js");
  const body = stripComments(text.slice(start, text.indexOf("\n}\n", start) + 2));
  const at = (s) => body.indexOf(s);
  assert.ok(at("askPrompt(") >= 0 && at("askPrompt(") < at("fsNewFile("), "the prompt comes first");
  assert.ok(at("fsNewFile(") < at("ftLoadRoot("), "then the create, then the refresh");
  assert.ok(at("ftLoadRoot(") < at("drawerShowFile("), "and the file opens after the tree is there");
  assert.match(body, /preview: false/);
  assert.match(body, /res\.status === 409/);
  assert.match(body, /name already exists/);
});

test("a removed count is the same red as the deleted and conflict letters (--danger), an added one the green of A and U (--ok)", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(css, /\.dk-del\s*\{\s*color:\s*var\(--danger\);\s*\}/);
  assert.match(css, /\.dk-st-c,\s*\.dk-st-d\s*\{\s*color:\s*var\(--danger\);\s*\}/);
  assert.match(css, /\.dk-add\s*\{\s*color:\s*var\(--ok\);\s*\}/);
});
