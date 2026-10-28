"use strict";
// The desktop page host (S4): Settings, Analytics and Audit re-parent their
// existing <dialog> into #page-host instead of opening modally. The DOM half
// needs a browser, so it is pinned at the source level the way
// settings-profiles-refetch.test.js does; the decision itself is pure.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// pageModeFor asks mobileshell's breakpoint, as the page does at runtime.
global.mobileActive = require("../static/mobileshell.js").mobileActive;
const { PAGE_IDS, pageModeFor } = require("../static/pagehost.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function body(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("PAGE_IDS is the three page-capable dialogs", () => {
  assert.deepEqual(PAGE_IDS, ["settings-modal", "analytics-modal", "audit-page"]);
});

test("pageModeFor: every page id is a page on desktop, a modal at 700px and below", () => {
  for (const id of PAGE_IDS) {
    assert.strictEqual(pageModeFor(id, 1280), true, id + " @1280");
    assert.strictEqual(pageModeFor(id, 701), true, id + " @701");
    assert.strictEqual(pageModeFor(id, 700), false, id + " @700");
    assert.strictEqual(pageModeFor(id, 390), false, id + " @390");
  }
});

test("pageModeFor: the other dialogs stay modal on desktop", () => {
  for (const id of ["spawn-modal", "bug-modal", "ask-modal", "palette", "sheet-modal", "", undefined]) {
    assert.strictEqual(pageModeFor(id, 1280), false, String(id));
  }
});

test("openDialog offers the dialog to the page host before showModal", () => {
  const b = body(read("shell.js"), "openDialog");
  const p = b.indexOf("openPage("), s = b.indexOf("showModal(");
  assert.ok(p >= 0, "openDialog must call openPage(");
  assert.ok(s > p, "openPage( must come before showModal(");
});

// Review Focus 1: anything that means "show me a session / the panes" closes
// the page first, so the user lands on what they asked for.
const pageCloseHooks = [
  ["tabs.js", "openTerminal"], ["tabs.js", "openPaneView"], ["tabs.js", "applyCap"], ["tabs.js", "toggleMax"],
  ["app.js", "spawnShellIn"], ["app.js", "spawnSession"], ["app.js", "toggleDrawer"], ["app.js", "openDrawer"],
  ["tabs.js", "dockShellBesideParent"],
];
for (const [file, name] of pageCloseHooks) {
  test(name + " (" + file + ") closes an open page", () => {
    assert.match(body(read(file), name), /closePage\(\)/);
  });
}

test("the palette's Activity and Access entries close an open page", () => {
  const src = read("palette.js");
  for (const k of ["activityView", "accessView"]) {
    assert.match(src, new RegExp(k + ":\\s*\\(\\)\\s*=>\\s*\\{\\s*closePage\\(\\);"), k);
  }
});

test("dialogsOpen counts modal dialogs only, never a page", () => {
  assert.match(read("app.js"), /const dialogsOpen = \(\) => document\.querySelectorAll\("dialog\[open\]:not\(\.as-page\)"\)\.length;/);
});

test("every dialog's close runs pageClosed before focusTerminal (the grid must be back to take focus)", () => {
  const b = body(read("shell.js"), "initDialogs");
  const pc = b.indexOf("pageClosed(d)"), ft = b.indexOf('addEventListener("close", focusTerminal)');
  assert.ok(pc >= 0 && ft > pc, "pageClosed(d) must be wired before focusTerminal");
});

test("a click on a page's empty area is not a backdrop click", () => {
  assert.match(body(read("shell.js"), "initDialogs"), /pressedBackdrop && e\.target === d && !d\.classList\.contains\("as-page"\)/);
});

test("crossing to mobile closes the page", () => {
  const src = read("app.js");
  const at = src.indexOf('window.matchMedia(mobileMediaQuery()).addEventListener("change"');
  assert.ok(at >= 0);
  assert.match(src.slice(at, at + 300), /closePage\(\)/);
});

test("the Audit link shows only when the server has an audit store", () => {
  assert.match(read("app.js"), /sideAudit\.hidden = !\(meta && meta\.audit\)/);
  assert.match(read("index.html"), /<button id="side-audit" type="button" hidden>Audit<\/button>\s*<button id="side-analytics"/);
});

test("#page-host sits after #drawer in #layout; pagehost.js loads after shell.js", () => {
  const html = read("index.html");
  const drawerEnd = html.indexOf("</aside>", html.indexOf('<aside id="drawer"'));
  const host = html.indexOf('<div id="page-host" hidden></div>');
  assert.ok(host > drawerEnd && host < html.indexOf('<aside id="steer-rail"'), "#page-host right after #drawer");
  assert.ok(html.indexOf('<script src="/pagehost.js">') > html.indexOf('<script src="/shell.js">'));
  assert.match(html, /<dialog id="audit-page" aria-label="Audit">/);
});

test("desktop.css hides the panes behind a page and makes the dialog fill the host", () => {
  const css = read("desktop.css");
  assert.match(css, /body\.page-open #grid, body\.page-open #drawer \{ display: none; \}/);
  assert.match(css, /#page-host \{ flex: 1; min-width: 0; display: flex; background: var\(--bg\); \}/);
  assert.match(css, /dialog\.as-page \{[^}]*position: static;[^}]*width: 100%; height: 100%;[^}]*border: 0;[^}]*display: flex; flex-direction: column;/);
  assert.match(css, /\.side-links \.on \{ color: var\(--fg\); font-weight: 600; \}/);
});

test("base CSS keeps #page-host off on mobile", () => {
  assert.match(read("app.css"), /#side-top, #side-foot, #drawer, #page-host \{ display: none; \}/);
});

test("with a page open, zoom and Ctrl+P stand down to the browser", () => {
  const guard = /if \(typeof currentPage === "function" && currentPage\(\)\) return;/;
  const app = read("app.js");
  const zoom = app.indexOf('if (!(e.ctrlKey || e.metaKey) || e.altKey) return;');
  assert.ok(zoom >= 0);
  assert.match(app.slice(zoom, zoom + 300), guard);
  const fd = read("filesdrawer.js");
  const p = fd.indexOf('if (e.code !== "KeyP"');
  assert.ok(p >= 0);
  assert.match(fd.slice(p, p + 300), guard);
});

test("classnames HOOKS lists the page-<id> body classes for every PAGE_ID", () => {
  const src = fs.readFileSync(path.join(__dirname, "classnames.test.js"), "utf8");
  for (const id of PAGE_IDS) assert.ok(src.includes('"page-' + id + '"'), "page-" + id);
});
