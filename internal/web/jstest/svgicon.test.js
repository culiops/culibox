"use strict";
// svgIcon (svgicon.js) is the one place an inline icon's <svg> is built, and uiIcon (#168) the one builder
// every call site uses: a glyph of the vendored set (icons.js) at the 24 grid, stroke 2. svgicon.js and
// icons.js are classic scripts that cannot be required as modules of the page, so they are run whole against
// a stand-in document that records attributes.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");

const SVG_NS = "http://www.w3.org/2000/svg";
// A stand-in node that keeps its attributes as an ordered list, a repeated setAttribute replacing in place.
function node(tag, ns) {
  return {
    tag, ns: ns || "", attrs: [], children: [],
    setAttribute(k, v) {
      const hit = this.attrs.find((a) => a[0] === k);
      if (hit) hit[1] = String(v); else this.attrs.push([k, String(v)]);
    },
    removeAttribute(k) { this.attrs = this.attrs.filter((a) => a[0] !== k); },
    getAttribute(k) { const hit = this.attrs.find((a) => a[0] === k); return hit ? hit[1] : null; },
    append(...n) { this.children.push(...n); },
    replaceWith(n) { this.replacedBy = n; },
  };
}
const line = (n) => n.tag + " " + n.attrs.map(([k, v]) => k + "=" + v).join(" ");
const drawn = (n) => (n === null ? null : [line(n)].concat(n.children.map(line)));

function world() {
  const ctx = vm.createContext({ document: { createElementNS: (ns, t) => node(t, ns) } });
  const run = (src) => new vm.Script(src).runInContext(ctx);
  run(read("icons.js"));
  run(read("svgicon.js"));
  return ctx;
}

const BASE = "svg viewBox=0 0 14 14 aria-hidden=true fill=none stroke=currentColor stroke-width=1.2";
const ROUND = " stroke-linecap=round stroke-linejoin=round";

// ---- svgIcon itself ----------------------------------------------------------------------------------------------------
test("svgIcon: the preamble is the product's icon style, round by default, shapes in order with their attributes in order", () => {
  const w = world();
  const svg = w.svgIcon([["rect", { x: "1", y: "2" }], ["path", { d: "M0 0", fill: "currentColor" }]]);
  assert.deepStrictEqual(drawn(svg), [BASE + ROUND, "rect x=1 y=2", "path d=M0 0 fill=currentColor"]);
  assert.deepStrictEqual(drawn(w.svgIcon([])), [BASE + ROUND], "no shapes is a bare icon, not an error");
  assert.deepStrictEqual(drawn(w.svgIcon(undefined, undefined)), [BASE + ROUND]);
});

test("svgIcon: round:false is dockIcon's preamble, icon is the data-icon that comes last", () => {
  const w = world();
  assert.deepStrictEqual(drawn(w.svgIcon([], { round: false })), [BASE]);
  assert.deepStrictEqual(drawn(w.svgIcon([], { icon: "pie" })), [BASE + ROUND + " data-icon=pie"]);
  assert.deepStrictEqual(drawn(w.svgIcon([], { round: false, icon: "x" })), [BASE + " data-icon=x"]);
});

test("svgIcon: viewBox and strokeWidth are options; size is width and height, and only when asked for", () => {
  const w = world();
  const svg = w.svgIcon([], { viewBox: "0 0 16 16", strokeWidth: "1.5", size: 16 });
  assert.strictEqual(svg.getAttribute("viewBox"), "0 0 16 16");
  assert.strictEqual(svg.getAttribute("stroke-width"), "1.5");
  assert.strictEqual(svg.getAttribute("width"), "16");
  assert.strictEqual(svg.getAttribute("height"), "16");
  const plain = w.svgIcon([]);
  assert.strictEqual(plain.getAttribute("width"), null, "unsized: the stylesheet sizes it (14px), as every icon so far");
  assert.strictEqual(plain.getAttribute("height"), null);
});

test("svgIcon: the shape attributes are never shared between calls", () => {
  const w = world();
  const shapes = [["path", { d: "M0 0" }]];
  const a = w.svgIcon(shapes);
  const b = w.svgIcon(shapes);
  a.children[0].setAttribute("d", "changed");
  assert.strictEqual(b.children[0].getAttribute("d"), "M0 0");
});

// ---- the preamble is written once --------------------------------------------------------------------------------------
test("the <svg> preamble lives in svgicon.js only: tabs.js, kitmenu.js and app.js do not set it up themselves", () => {
  for (const f of ["tabs.js", "kitmenu.js", "app.js"]) {
    const src = read(f);
    assert.ok(!/setAttribute\("viewBox"/.test(src), f + " sets a viewBox by hand");
    assert.ok(!/setAttribute\("stroke-width"/.test(src), f + " sets a stroke width by hand");
    assert.ok(!/createElementNS\(NS, "svg"\)/.test(src), f + " creates an <svg> by hand");
  }
  assert.ok(!/function menuIconSvg|function kitMenuButton/.test(read("app.js")), "the kit menu builders moved out of app.js");
  assert.ok(!/function menuNavIndex/.test(read("sessionrow.js")), "menuNavIndex moved out of sessionrow.js");
});

test("index.html loads svgicon.js and kitmenu.js before tabs.js (lint.sh bundles in this order)", () => {
  const order = [...read("index.html").matchAll(/<script src="\/([a-z-]+\.js)"/g)].map((m) => m[1]);
  assert.ok(order.indexOf("svgicon.js") >= 0 && order.indexOf("kitmenu.js") >= 0, "both scripts are in index.html");
  assert.ok(order.indexOf("svgicon.js") < order.indexOf("kitmenu.js"), "svgIcon first");
  assert.ok(order.indexOf("kitmenu.js") < order.indexOf("tabs.js"), "the menu builders before tabs.js");
});

// ---- uiIcon: the one builder -------------------------------------------------------------------------------------------
const attrsOf = (n) => Object.fromEntries(n.attrs);
const shapesOf = (n) => n.children.map((c) => [c.tag, Object.fromEntries(c.attrs)]);

test("uiIcon draws a Lucide glyph at 24-grid stroke 2", () => {
  const w = world();
  const svg = w.uiIcon("x", { size: 12 });
  assert.strictEqual(svg.tag, "svg");
  assert.strictEqual(svg.ns, SVG_NS);
  assert.deepStrictEqual(attrsOf(svg), {
    viewBox: "0 0 24 24", "aria-hidden": "true", fill: "none", stroke: "currentColor", "stroke-width": "2",
    "stroke-linecap": "round", "stroke-linejoin": "round", "data-icon": "x", width: "12", height: "12",
    focusable: "false", class: "ui-icon",
  });
  assert.deepStrictEqual(shapesOf(svg), JSON.parse(vm.runInContext("JSON.stringify(ICON_SET.x[1])", w)));
  for (const c of svg.children) assert.strictEqual(c.ns, SVG_NS);
});

test("uiIcon: the default size is 14", () => {
  assert.strictEqual(world().uiIcon("x").getAttribute("width"), "14");
  assert.strictEqual(world().uiIcon("x").getAttribute("height"), "14");
});

test("uiIcon fills a Simple Icons mark", () => {
  const svg = world().uiIcon("si:claude");
  assert.strictEqual(svg.getAttribute("fill"), "currentColor");
  assert.strictEqual(svg.getAttribute("stroke"), "none");
  assert.strictEqual(svg.getAttribute("width"), "14");
  assert.strictEqual(svg.getAttribute("data-icon"), "si:claude");
  assert.ok(svg.children.length >= 1);
});

test("uiIcon keeps a child's own fill: key-round's dot stays filled in a stroked glyph", () => {
  const w = world();
  const svg = w.uiIcon("key-round");
  assert.strictEqual(svg.getAttribute("fill"), "none");
  assert.ok(svg.children.some((c) => c.getAttribute("fill") === "currentColor"), "the dot keeps fill=currentColor");
});

test("uiIcon with a label is an image, not hidden", () => {
  const svg = world().uiIcon("lock", { label: "Always shared, set by CuliBox" });
  assert.strictEqual(svg.getAttribute("role"), "img");
  assert.strictEqual(svg.getAttribute("aria-label"), "Always shared, set by CuliBox");
  assert.strictEqual(svg.getAttribute("aria-hidden"), null);
  assert.strictEqual(world().uiIcon("lock").getAttribute("role"), null, "no label, no role");
});

test("uiIcon unknown name draws an empty glyph", () => {
  const w = world();
  for (const name of ["nope", "constructor", "__proto__", "toString", ""]) {
    let svg;
    assert.doesNotThrow(() => { svg = w.uiIcon(name); }, JSON.stringify(name));
    assert.strictEqual(svg.tag, "svg");
    assert.strictEqual(svg.children.length, 0, JSON.stringify(name) + " draws nothing");
    assert.strictEqual(svg.getAttribute("data-icon"), name);
  }
});

test("uiIcon cls appends", () => {
  const w = world();
  assert.strictEqual(w.uiIcon("x", { cls: "tchip-i" }).getAttribute("class"), "ui-icon tchip-i");
  assert.strictEqual(w.uiIcon("x", { cls: "" }).getAttribute("class"), "ui-icon");
});

test("uiIcon never builds an icon from markup", () => {
  assert.ok(!/innerHTML|insertAdjacentHTML/.test(read("svgicon.js").replace(/\/\/.*$/gm, "")));
});

test("paintStaticIcons replaces placeholders keeping id and class", () => {
  const w = world();
  const ph = node("svg", SVG_NS);
  ph.setAttribute("id", "drawer-pin");
  ph.setAttribute("class", "a");
  ph.setAttribute("width", "14");
  ph.dataset = { uicon: "pin" };
  const bare = node("svg", SVG_NS);
  bare.dataset = { uicon: "x" };
  const root = { querySelectorAll: (sel) => { root.sel = sel; return [ph, bare]; } };
  assert.strictEqual(w.paintStaticIcons(root), 2);
  assert.strictEqual(root.sel, "svg[data-uicon]");
  const n = ph.replacedBy;
  assert.strictEqual(n.getAttribute("id"), "drawer-pin");
  assert.strictEqual(n.getAttribute("class"), "ui-icon a");
  assert.strictEqual(n.getAttribute("data-icon"), "pin");
  assert.strictEqual(n.getAttribute("width"), "14");
  assert.strictEqual(bare.replacedBy.getAttribute("width"), "14", "no width attribute: the default size");
  assert.strictEqual(bare.replacedBy.getAttribute("id"), null);
  assert.strictEqual(w.paintStaticIcons({ querySelectorAll: () => [] }), 0);
});

test("the shared stylesheet aligns every .ui-icon, phone included", () => {
  // Shared builders (renderSession, tierpick) also draw on the phone, so the rule is in app.css, outside any media block.
  const css = read("app.css");
  const m = /^ {4}\.ui-icon \{([^}]*)\}/m.exec(css);
  assert.ok(m, "a top-level .ui-icon rule in app.css");
  for (const d of ["flex: none", "display: inline-block", "vertical-align: -2px"]) assert.ok(m[1].includes(d), d);
});

test("only svgicon.js builds an <svg>", () => {
  // analytics.js draws chart primitives (anSvg), notifymodel.js a favicon string.
  const skip = new Set(["svgicon.js", "analytics.js", "notifymodel.js"]);
  const dir = path.join(__dirname, "..", "static");
  const bad = [];
  for (const f of fs.readdirSync(dir).filter((f) => f.endsWith(".js") && !skip.has(f))) {
    const src = read(f);
    if (/createElementNS\([^)]*["']svg["']/.test(src)) bad.push(f + ": createElementNS svg");
    if (/["'`]<svg/.test(src)) bad.push(f + ": <svg string");
  }
  assert.deepStrictEqual(bad, []);
});

// An icon is decoration (aria-hidden, beside its text): a click on it belongs to the button around it, so handlers that
// read e.target see the button and not the <svg> or its shape (#168). The one rule is in app.css, which every page loads.
test(".ui-icon takes no pointer events, so the button stays the event target", () => {
  const css = read("app.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /(?:^|[}\s])\.ui-icon\s*\{([^}]*)\}/.exec(css);
  assert.ok(m, "the shared .ui-icon base rule exists");
  assert.match(m[1], /pointer-events:\s*none/);
});
