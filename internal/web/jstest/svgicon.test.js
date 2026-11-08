"use strict";
// One SVG preamble (#173 task 1). tabs.js tabIcon and dockIcon and the kit menu's menuIconSvg each wrote
// the same eight-attribute <svg> by hand; svgIcon (svgicon.js) is the one place now. The PIN_* tables
// below are what the three builders drew BEFORE that refactor, written out by running the old code once
// (every attribute, in the order it was set, and every child shape): the refactor must not move a pixel.
// The builders are classic scripts that cannot be required, so svgicon.js and kitmenu.js are run whole
// and tabIcon / dockIcon are cut out of tabs.js by name, against a stand-in document that records attributes.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

const SVG_NS = "http://www.w3.org/2000/svg";
// A stand-in node that keeps its attributes as an ordered list, a repeated setAttribute replacing in place.
function node(tag, ns) {
  return {
    tag, ns: ns || "", attrs: [], children: [],
    setAttribute(k, v) {
      const hit = this.attrs.find((a) => a[0] === k);
      if (hit) hit[1] = String(v); else this.attrs.push([k, String(v)]);
    },
    getAttribute(k) { const hit = this.attrs.find((a) => a[0] === k); return hit ? hit[1] : null; },
    append(...n) { this.children.push(...n); },
  };
}
const line = (n) => n.tag + " " + n.attrs.map(([k, v]) => k + "=" + v).join(" ");
const drawn = (n) => (n === null ? null : [line(n)].concat(n.children.map(line)));

function world() {
  const ctx = vm.createContext({ document: { createElementNS: (ns, t) => node(t, ns) } });
  const run = (src) => new vm.Script(src).runInContext(ctx);
  run(read("svgicon.js"));
  run(read("kitmenu.js"));
  const tabs = read("tabs.js");
  run(fnSrc(tabs, "dockIcon") + "\n" + fnSrc(tabs, "tabIcon"));
  return ctx;
}

const BASE = "svg viewBox=0 0 14 14 aria-hidden=true fill=none stroke=currentColor stroke-width=1.2";
const ROUND = " stroke-linecap=round stroke-linejoin=round";

// ---- what the three builders drew before the refactor ------------------------------------------------------------------
const PIN_TAB = {
  "terminal": [
    BASE + ROUND,
    "rect x=1.5 y=2.5 width=11 height=9 rx=1.5",
    "path d=M4 5.8l2 1.7-2 1.7M7.6 9.3H10",
  ],
  "files": [
    BASE + ROUND,
    "path d=M1.5 3.5c0-.55.45-1 1-1h2.8l1.4 1.5h4.8c.55 0 1 .45 1 1v6c0 .55-.45 1-1 1h-9c-.55 0-1-.45-1-1z",
  ],
  "activity": [
    BASE + ROUND,
    "path d=M1 7.5h2.6l1.5-4.2 3 8.4 1.7-4.2H13",
  ],
  "access": [
    BASE + ROUND,
    "path d=M7 1.5l4.5 1.7v3.7c0 2.6-1.9 4.4-4.5 5.6-2.6-1.2-4.5-3-4.5-5.6V3.2z",
    "path d=M5 7l1.5 1.5L9.2 5.7",
  ],
};
const PIN_DOCK = {
  "below": [
    BASE,
    "rect x=1.5 y=1.5 width=11 height=11 rx=1.5",
    "rect x=1.5 y=8 width=11 height=4.5 fill=currentColor stroke=none",
  ],
  "right": [
    BASE,
    "rect x=1.5 y=1.5 width=11 height=11 rx=1.5",
    "rect x=7 y=1.5 width=5.5 height=11 fill=currentColor stroke=none",
  ],
  "own": [
    BASE,
    "rect x=1.5 y=4.5 width=8 height=8 rx=1.2",
    "path d=M4.5 4.5V2.7c0-.7.5-1.2 1.2-1.2h5.6c.7 0 1.2.5 1.2 1.2v5.6c0 .7-.5 1.2-1.2 1.2H9.5",
  ],
  "hide": [
    BASE,
    "path d=M3.5 3.5l7 7M10.5 3.5l-7 7",
  ],
};
const PIN_MENU = {
  "files": [
    BASE + ROUND + " data-icon=files",
    "path d=M1.5 3.5c0-.55.45-1 1-1h2.8l1.4 1.5h4.8c.55 0 1 .45 1 1v6c0 .55-.45 1-1 1h-9c-.55 0-1-.45-1-1z",
  ],
  "copy": [
    BASE + ROUND + " data-icon=copy",
    "rect x=1.5 y=4.5 width=8 height=8 rx=1.2",
    "path d=M4.5 4.5V2.7c0-.7.5-1.2 1.2-1.2h5.6c.7 0 1.2.5 1.2 1.2v5.6c0 .7-.5 1.2-1.2 1.2H9.5",
  ],
  "duplicate": [
    BASE + ROUND + " data-icon=duplicate",
    "rect x=1.5 y=4.5 width=8 height=8 rx=1.2",
    "path d=M4.5 4.5V2.7c0-.7.5-1.2 1.2-1.2h5.6c.7 0 1.2.5 1.2 1.2v5.6c0 .7-.5 1.2-1.2 1.2H9.5",
    "path d=M5.5 7v3M4 8.5h3",
  ],
  "end": [
    BASE + ROUND + " data-icon=end",
    "path d=M3.5 3.5l7 7M10.5 3.5l-7 7",
  ],
  "pencil": [
    BASE + ROUND + " data-icon=pencil",
    "path d=M2 12l.7-3L9.7 2a1.2 1.2 0 0 1 1.7 0l.6.6a1.2 1.2 0 0 1 0 1.7L5 11.3z",
    "path d=M8.4 3.3l2.3 2.3",
  ],
  "pie": [
    BASE + ROUND + " data-icon=pie",
    "circle cx=7 cy=7 r=5.5",
    "path d=M7 7V1.5A5.5 5.5 0 0 1 12.5 7z fill=currentColor stroke=none",
  ],
  "split": [
    BASE + ROUND + " data-icon=split",
    "rect x=1.5 y=2.5 width=11 height=9 rx=1.5",
    "path d=M7 2.5v9",
  ],
  "pause": [
    BASE + ROUND + " data-icon=pause",
    "rect x=3.5 y=2.5 width=2.5 height=9 rx=.8 fill=currentColor stroke=none",
    "rect x=8 y=2.5 width=2.5 height=9 rx=.8 fill=currentColor stroke=none",
  ],
  "tray": [
    BASE + ROUND + " data-icon=tray",
    "path d=M7 2v6.5M4.2 6l2.8 2.8L9.8 6",
    "path d=M2 9.5v2c0 .55.45 1 1 1h8c.55 0 1-.45 1-1v-2",
  ],
};

test("tabIcon draws every kind exactly as before, round caps and joins set up front", () => {
  const w = world();
  for (const k of Object.keys(PIN_TAB)) assert.deepStrictEqual(drawn(w.tabIcon(k)), PIN_TAB[k], "tabIcon(" + k + ")");
});

test("dockIcon draws every kind exactly as before, with no linecap or linejoin", () => {
  const w = world();
  for (const k of Object.keys(PIN_DOCK)) assert.deepStrictEqual(drawn(w.dockIcon(k)), PIN_DOCK[k], "dockIcon(" + k + ")");
});

test("menuIconSvg draws every kind it drew before exactly as before: round caps and joins, data-icon last", () => {
  const w = world();
  for (const k of Object.keys(PIN_MENU)) assert.deepStrictEqual(drawn(w.menuIconSvg(k)), PIN_MENU[k], "menuIconSvg(" + k + ")");
  assert.strictEqual(w.menuIconSvg(""), null, "no kind, no icon: the slot stays an empty box");
  assert.strictEqual(w.menuIconSvg(undefined), null);
});

test("menuIconSvg's added kinds (delete, check, download, insert, folder-plus) carry the same preamble, data-icon last", () => {
  const w = world();
  for (const k of ["delete", "check", "download", "insert", "folder-plus"]) {
    const d = drawn(w.menuIconSvg(k));
    assert.strictEqual(d[0], BASE + ROUND + " data-icon=" + k, k);
    assert.ok(d.length >= 2, k + " draws a shape");
  }
  // delete is the End session cross, folder-plus the pane header's folder and one more path.
  assert.deepStrictEqual(drawn(w.menuIconSvg("delete")).slice(1), PIN_MENU.end.slice(1));
  assert.deepStrictEqual(drawn(w.menuIconSvg("folder-plus")).slice(1, 2), PIN_MENU.files.slice(1));
  assert.strictEqual(drawn(w.menuIconSvg("folder-plus")).length, 3);
});

test("every built shape and the svg itself are in the SVG namespace", () => {
  const w = world();
  for (const svg of [w.tabIcon("access"), w.dockIcon("below"), w.menuIconSvg("duplicate"), w.menuIconSvg("pie")]) {
    assert.strictEqual(svg.ns, SVG_NS);
    for (const c of svg.children) assert.strictEqual(c.ns, SVG_NS);
  }
});

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
