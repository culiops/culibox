"use strict";
// paintRailMeters (app.js) mirrors the #limits-side meters into the rail's mini
// bars. app.js is a classic page script that cannot be required, so the function
// is cut out of its source and run against tiny stand-ins for the elements.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const src = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
const start = src.indexOf("function paintRailMeters()");
assert.ok(start >= 0, "paintRailMeters not found in app.js");
const end = src.indexOf("\n}\n", start) + 3;

function classes(initial) {
  const set = new Set(initial);
  return {
    contains: (c) => set.has(c),
    toggle: (c, on) => { if (on) set.add(c); else set.delete(c); },
  };
}

// One source meter row (what renderRateLimitsInto builds) and one rail bar.
function paint(rowClasses) {
  const row = {
    firstElementChild: { textContent: "5h" },
    classList: classes(rowClasses),
    querySelector: () => ({ style: { width: rowClasses.includes("unk") ? "0%" : "42%" } }),
  };
  const bar = { dataset: { win: "5h" }, hidden: true, classList: classes([]), firstElementChild: { style: {} } };
  const box = { children: [bar], hidden: true };
  const side = { querySelectorAll: () => [row] };
  const ctx = { $: (id) => (id === "rail-meters" ? box : side), Array };
  vm.runInNewContext(src.slice(start, end) + "\npaintRailMeters();", ctx);
  return bar;
}

test("the rail bar of a live window is not marked unknown", () => {
  const bar = paint(["mtr"]);
  assert.equal(bar.classList.contains("unk"), false);
  assert.equal(bar.firstElementChild.style.width, "42%");
});

test("the rail bar copies a stale meter's dimming", () => {
  assert.equal(paint(["mtr", "stale"]).classList.contains("stale"), true);
  assert.equal(paint(["mtr"]).classList.contains("stale"), false);
});

test("the rail bar of a window past its reset is marked unknown, not drawn as a plain empty bar", () => {
  const bar = paint(["mtr", "unk"]);
  assert.equal(bar.classList.contains("unk"), true);
  assert.equal(bar.hidden, false);
});
