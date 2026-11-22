"use strict";
// #340 S6: the desktop sidebar's chrome (sidechrome.js): the layout button beside New session and its menu,
// and the cost on the foot's links line. The pure helpers are required; the markup and the stylesheet are
// read as text, and app.js / tabs.js functions are cut out of their source by name.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const SC = require("../static/sidechrome.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const html = read("index.html");

test("xlay words for 1, 2 and 4", () => {
  assert.equal(SC.xlayLabel(1), "1 pane");
  assert.equal(SC.xlayLabel(2), "2 panes");
  assert.equal(SC.xlayLabel(4), "4 panes");
  assert.equal(SC.xlayLabel(3), "4 panes", "any other cap reads as 4, clampCap's default");
  assert.equal(SC.xlayTitle(2), "Layout: 2 panes (click to change)");
  assert.equal(SC.xlayTitle(1), "Layout: 1 pane (click to change)");
  assert.equal(SC.xlayAria(4), "Layout: 4 panes");
});

test("xlay shapes: a frame, then one divider per split", () => {
  assert.deepEqual([1, 2, 4].map((n) => SC.xlayShapes(n).length), [1, 2, 3]);
  assert.equal(SC.xlayShapes(1)[0][0], "rect");
  assert.equal(SC.xlayShapes(1)[0][1].rx, "2");
  assert.deepEqual(SC.xlayShapes(2)[1][1].d, "M12 3v18");
  assert.deepEqual(SC.xlayShapes(4).slice(1).map((s) => s[1].d), ["M3 12h18", "M12 3v18"]);
});

test("sideCostLabel: an approximate amount for today, in the Analytics page's format", () => {
  assert.equal(SC.sideCostLabel(454.2).text, "≈ $454 today");
  assert.equal(SC.sideCostLabel(99.5).text, "≈ $99.50 today");
  assert.equal(SC.sideCostLabel(12345).text, "≈ $12.3k today");
  assert.equal(SC.sideCostLabel(0).text, "≈ $0.00 today");
  assert.equal(SC.sideCostLabel(1).title, "Token usage today (UTC) priced at API list rates. A subscription does not bill this.");
  assert.equal(SC.SIDE_COST_TITLE, SC.sideCostLabel(1).title);
});

test("sidechrome.js loads after kitmenu.js and before tabs.js", () => {
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("kitmenu.js") > 0 && at("sidechrome.js") > 0 && at("tabs.js") > 0);
  assert.ok(at("kitmenu.js") < at("sidechrome.js"), "after kitmenu.js");
  assert.ok(at("sidechrome.js") < at("tabs.js"), "before tabs.js");
});

// ---- Task 2: the layout button and its menu, against fakedom.js -------------------------------------
// fakedom has no document, no createElementNS and no addEventListener: the test's own stand-in adds them.
// svgicon.js and kitmenu.js are the page's, put on global the way the browser shares one scope.
const { Node } = require("./fakedom.js");
const SVG = require("../static/svgicon.js");
const KM = require("../static/kitmenu.js");
Object.assign(global, { svgIcon: SVG.svgIcon, svgShape: SVG.svgShape, menuIconSvg: KM.menuIconSvg, kitMenuKeys: KM.kitMenuKeys });

function page() {
  const doc = { activeElement: null, listeners: [] };
  doc.createElement = (t) => new Node(t, doc);
  doc.createElementNS = (ns, t) => doc.createElement(t);
  doc.body = doc.createElement("body");
  doc.querySelector = (s) => doc.body.querySelector(s);
  doc.querySelectorAll = (s) => doc.body.querySelectorAll(s);
  doc.addEventListener = (type, fn) => doc.listeners.push([type, fn]);
  global.document = doc;
  const wrap = doc.createElement("span");
  wrap.className = "xlay-wrap";
  const btn = doc.createElement("button");
  btn.className = "btn btn-ghost btn-icon btn-sm xlay";
  btn.setAttribute("aria-expanded", "false");
  wrap.append(btn);
  const other = doc.createElement("button");
  doc.body.append(wrap, other);
  const picked = [];
  global.cap = 4;
  global.applyCap = (n) => { picked.push(n); global.cap = n; };
  return { doc, wrap, btn, other, picked, menu: () => wrap.querySelector(".xmenu") };
}

test("xlayPaint draws the glyph and the words for 1, 2 and 4", () => {
  const { btn } = page();
  SC.xlayPaint(btn, 2);
  assert.equal(btn.children.length, 1);
  const svg = btn.children[0];
  assert.equal(svg.getAttribute("viewBox"), "0 0 24 24");
  assert.equal(svg.getAttribute("stroke-width"), "2");
  assert.equal(svg.children.length, 2);
  assert.equal(btn.getAttribute("title"), "Layout: 2 panes (click to change)");
  assert.equal(btn.getAttribute("aria-label"), "Layout: 2 panes");
  SC.xlayPaint(btn, 1);
  assert.equal(btn.children.length, 1, "a repaint replaces the glyph");
  assert.equal(btn.children[0].children.length, 1);
  assert.equal(btn.getAttribute("title"), "Layout: 1 pane (click to change)");
  SC.xlayPaint(btn, 4);
  assert.equal(btn.children[0].children.length, 3);
  assert.equal(btn.getAttribute("aria-label"), "Layout: 4 panes");
});

test("xlayMenuBuild: three radio items, the current one ticked", () => {
  page();
  const got = [];
  const m = SC.xlayMenuBuild(2, (n) => got.push(n));
  assert.equal(m.getAttribute("role"), "menu");
  assert.equal(m.getAttribute("aria-label"), "Layout");
  assert.ok(m.classList.contains("menu") && m.classList.contains("xmenu"));
  assert.equal(m.children[0].className, "menu-label grp-label");
  assert.equal(m.children[0].textContent, "Layout");
  const items = m.querySelectorAll(".menu-item");
  assert.equal(items.length, 3);
  assert.deepEqual(items.map((b) => b.getAttribute("role")), ["menuitemradio", "menuitemradio", "menuitemradio"]);
  assert.deepEqual(items.map((b) => b.querySelector(".menu-text").textContent), ["1 pane", "2 panes", "4 panes"]);
  assert.deepEqual(items.map((b) => b.getAttribute("aria-checked")), ["false", "true", "false"]);
  assert.deepEqual(items.map((b) => b.classList.contains("on")), [false, true, false]);
  assert.deepEqual(items.map((b) => b.querySelector(".menu-kbd").children.length), [0, 1, 0]);
  assert.deepEqual(items.map((b) => b.querySelector(".menu-icon svg").children.length), [1, 2, 3], "each row draws its own layout");
  items[2].click();
  assert.deepEqual(got, [4]);
});

test("xlay menu: Esc closes it, refocuses the button and prevents the default", () => {
  const p = page();
  global.cap = 2;
  SC.initSideLayout();
  p.btn.click();
  assert.ok(p.menu(), "open");
  assert.equal(p.btn.getAttribute("aria-expanded"), "true");
  assert.equal(p.doc.activeElement, p.menu().querySelectorAll(".menu-item")[1], "focus starts on the ticked row");
  let prevented = 0;
  SC.xlayWrapKeydown({ key: "Escape", preventDefault: () => prevented++ });
  assert.equal(p.menu(), null);
  assert.equal(p.btn.getAttribute("aria-expanded"), "false");
  assert.equal(prevented, 1);
  assert.equal(p.doc.activeElement, p.btn);
  assert.deepEqual(p.picked, []);
});

test("xlay menu: the button toggles it and a click outside closes it", () => {
  const p = page();
  SC.initSideLayout();
  assert.equal(p.wrap.onkeydown, SC.xlayWrapKeydown);
  assert.deepEqual(p.doc.listeners, [["click", SC.xlayDocClick]]);
  p.btn.click();
  p.btn.click();
  assert.equal(p.menu(), null, "a second press closes it");
  assert.equal(p.btn.getAttribute("aria-expanded"), "false");
  p.btn.click();
  SC.xlayDocClick({ target: p.other });
  assert.equal(p.menu(), null, "a click elsewhere closes it");
  assert.deepEqual(p.picked, [], "without picking");
  p.btn.click();
  SC.xlayDocClick({ target: p.btn });
  assert.ok(p.menu(), "the click that opened it does not close it");
  // Picking closes the menu, gives focus back to the button, then sets the layout.
  p.menu().querySelectorAll(".menu-item")[0].click();
  assert.equal(p.menu(), null);
  assert.equal(p.doc.activeElement, p.btn);
  assert.deepEqual(p.picked, [1]);
  // A repaint from elsewhere (the palette, a restored layout) closes an open menu, so its tick is never stale.
  p.btn.click();
  SC.xlayPaint(p.btn, 2);
  assert.equal(p.menu(), null);
  assert.equal(p.btn.getAttribute("aria-expanded"), "false");
});

// fnSrc: one top-level function's source, cut out of a page script by name.
function fnSrc(src, name) {
  const m = new RegExp("function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("applyCap repaints every .xlay through querySelectorAll", () => {
  assert.match(fnSrc(read("tabs.js"), "applyCap"), /querySelectorAll\("\.xlay"\)\) xlayPaint\(b, n\)/);
});

// ---- Task 3: the foot: the cost on the links line --------------------------------------------------
const foot = (() => {
  const s = html.slice(html.indexOf('<div id="side-foot">'));
  return s.slice(0, s.indexOf("</aside>"));
})();
// cssRule: the declarations of the rule whose selector is exactly `sel` (desktop.css).
function cssRule(sel) {
  const css = read("desktop.css");
  const at = css.indexOf("\n  " + sel + " {");
  assert.ok(at > 0, sel + " rule not found");
  return css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
}

test("the foot: elevated, store, then the cost and the links on one line", () => {
  assert.ok(!foot.includes("side-stats"), "no .side-stats row");
  const links = /<div class="xlinks">\s*<span id="side-cost" class="xcost" hidden><\/span>\s*<div class="side-links">/.exec(foot);
  assert.ok(links, "#side-cost sits in div.xlinks, before div.side-links");
  const at = ['id="limits-side"', 'id="side-elevated"', 'id="side-store"', 'class="xlinks"'].map((s) => foot.indexOf(s));
  assert.ok(at.every((i) => i > 0), "all four are in #side-foot");
  assert.deepEqual([...at].sort((a, b) => a - b), at, "meters, elevated, store, links");
  assert.match(foot, /<span id="side-elevated" hidden><\/span>/);
});

test("paintSideCost writes the label and the tooltip from sideCostLabel", () => {
  const body = fnSrc(read("app.js"), "paintSideCost");
  assert.match(body, /sideCostLabel\(/);
  assert.match(body, /el\.textContent = lab\.text;/);
  assert.match(body, /el\.title = lab\.title;/);
  assert.ok(!body.includes('"Spend today'), "no Spend wording is left");
});

test("the cost line wraps instead of truncating", () => {
  assert.match(cssRule("#side-foot .xlinks"), /flex-wrap: wrap;/);
  const cost = cssRule("#side-foot .xcost");
  assert.match(cost, /white-space: nowrap;/);
  assert.ok(!/overflow|text-overflow|max-width/.test(cost), "never cut: " + cost);
});

test("with the cost hidden, the links keep today's left edge", () => {
  assert.match(cssRule("#side-foot .xcost[hidden] + .side-links"), /margin-left: -8px;/);
  assert.match(cssRule("#side-foot .xcost[hidden]"), /display: none;/);
});

test("the elevated line keeps the foot's small muted type on its own row", () => {
  const el = cssRule("#side-elevated");
  assert.match(el, /font-size: var\(--t-sm\);/);
  assert.match(el, /color: var\(--muted\);/);
  assert.ok(!/margin-left: auto/.test(el), "left-aligned like #side-store, not pushed right");
});

// Measured in the browser (Task 5): #sidebar clips (overflow: hidden), and app.css gives every .menu a fixed 232px. A menu
// anchored to the 24px button's right edge ran 11px past the sidebar's left edge, and the tick (a menuIconSvg with no
// size, outside a .menu-icon slot) drew at 0x0.
test("the layout menu stays inside the sidebar, and its tick has a size", () => {
  assert.match(cssRule("#side-top .xnewrow"), /position: relative;/, "the menu is placed against the whole row");
  assert.ok(!/position/.test(cssRule("#side-top .xlay-wrap")), "not against the 24px button's wrap");
  const m = cssRule("#side-top .xlay-wrap > .menu.xmenu:has(> .menu-item)");
  assert.match(m, /right: 0;/);
  assert.match(m, /width: max-content;/, "app.css's 232px .menu width is reset");
  assert.match(m, /min-width: min\(180px, 100%\);/);
  assert.match(m, /max-width: 100%;/, "never wider than the row, at the 200px sidebar too");
  assert.match(cssRule("#side-top .xmenu .menu-kbd svg"), /width: 14px; height: 14px;/);
});

// Measured (Task 5): at the 200px sidebar New session gets 85px beside the three icon buttons; its label is 80px at 600
// 13px Inter, so the 12px side padding broke it onto two lines inside the 28px button.
test("New session keeps its label on one line at the 200px sidebar", () => {
  const b = cssRule("#side-top .xnewrow #side-new");
  assert.match(b, /white-space: nowrap;/);
  assert.match(b, /padding: 0 2px;/, "the label is centred, so the padding only sets how narrow it may get");
});

test("boot wires the layout button before the first applyCap", () => {
  const app = read("app.js");
  const init = app.indexOf("\ninitSideLayout();");
  assert.ok(init > 0, "initSideLayout() is called at boot");
  assert.ok(init < app.indexOf("\napplyCap(4);"), "before applyCap(4) paints it");
});

test("Analytics API value tooltip is range-neutral; the sidebar keeps today (UTC) (#340)", () => {
  const AN = require("../static/analytics.js");
  assert.equal(AN.AN_API_VALUE_TITLE, "Token usage priced at API list rates. A subscription does not bill this.");
  assert.equal(SC.SIDE_COST_TITLE, "Token usage today (UTC) priced at API list rates. A subscription does not bill this.");
});
