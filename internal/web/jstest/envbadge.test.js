"use strict";
// F79: the env badge colour is operator config, so nothing guarantees its text is
// readable on it. envTextColor picks black or white by WCAG contrast; parseRgb
// reads what getComputedStyle hands back. F80: the pane-cap toast has one wording.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { envTextColor, parseRgb } = require("../static/envbadge.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// WCAG 2.x relative luminance and contrast ratio, written out here so the test
// does not lean on the helper it is checking.
const lin = (v) => { const c = v / 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
const lum = ([r, g, b]) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const contrast = (a, b) => { const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05); };

test("envTextColor: a light colour gets black text, a dark one white, both >= 4.5:1", () => {
  const light = hex("#f5f5f5"), dark = hex("#1a1a1a");
  assert.strictEqual(envTextColor(light), "#000000");
  assert.strictEqual(envTextColor(dark), "#ffffff");
  assert.ok(contrast(light, hex(envTextColor(light))) >= 4.5);
  assert.ok(contrast(dark, hex(envTextColor(dark))) >= 4.5);
});

test("envTextColor: the stock environment colours stay readable", () => {
  for (const h of ["#ff0000", "#00ff00", "#0000ff", "#ffff00", "#ff8800", "#e11d48", "#2563eb", "#16a34a", "#facc15", "#000000", "#ffffff"]) {
    const rgb = hex(h);
    assert.ok(contrast(rgb, hex(envTextColor(rgb))) >= 4.5, h);
  }
});

test("envTextColor: mid-grey passes 4.5:1 whichever side it picks, and it picks the higher contrast", () => {
  const mid = hex("#777777");
  const pick = envTextColor(mid);
  assert.ok(contrast(mid, hex(pick)) >= 4.5);
  const other = pick === "#000000" ? "#ffffff" : "#000000";
  assert.ok(contrast(mid, hex(pick)) >= contrast(mid, hex(other)));
});

test("envTextColor: every grey level clears 4.5:1 with the colour it picks", () => {
  for (let v = 0; v <= 255; v++) {
    const rgb = [v, v, v];
    assert.ok(contrast(rgb, hex(envTextColor(rgb))) >= 4.5, "grey " + v);
  }
});

test("envTextColor: the answer is only ever black or white", () => {
  for (const rgb of [[0, 0, 0], [255, 255, 255], [200, 30, 90], [10, 200, 120]]) {
    assert.ok(["#000000", "#ffffff"].includes(envTextColor(rgb)));
  }
});

test("parseRgb: rgb() and rgba() as getComputedStyle returns them", () => {
  assert.deepStrictEqual(parseRgb("rgb(1, 2, 3)"), [1, 2, 3]);
  assert.deepStrictEqual(parseRgb("rgba(10, 20, 30, 0.5)"), [10, 20, 30]);
  assert.deepStrictEqual(parseRgb("rgb(255,255,255)"), [255, 255, 255]);
  assert.deepStrictEqual(parseRgb("  rgb(0, 0, 0)  "), [0, 0, 0]);
});

test("parseRgb: the space-separated form, with and without alpha", () => {
  assert.deepStrictEqual(parseRgb("rgb(1 2 3)"), [1, 2, 3]);
  assert.deepStrictEqual(parseRgb("rgb(1 2 3 / 0.5)"), [1, 2, 3]);
  assert.deepStrictEqual(parseRgb("rgb(1 2 3 / 50%)"), [1, 2, 3]);
});

test("parseRgb: anything else is null", () => {
  for (const junk of ["", "red", "#fff", "transparent", "hsl(0, 0%, 0%)", "rgb(1, 2)", "rgb(1, 2, x)", "rgb(300, 0, 0)", "rgb(1, 2, 3, 4, 5)", "rgb(-1, 0, 0)", "color(srgb 1 0 0)", null, undefined, 42, {}]) {
    assert.strictEqual(parseRgb(junk), null, String(junk));
  }
});

// tabs.js needs a DOM and cannot be require()d (tree-follows-focus.test.js), so
// F80 is pinned at the source: the sentence lives in one constant and every cap
// toast uses it.
test("F80: the pane-cap toast has one wording, defined once in tabs.js", () => {
  const tabs = read("tabs.js");
  assert.strictEqual((tabs.match(/const PANE_LIMIT_MSG = "Pane limit reached\.";/g) || []).length, 1);
  assert.strictEqual((tabs.match(/Pane limit reached/g) || []).length, 1, "no second literal of the sentence");
  // Three wordings used to be two call sites (one toast, one ternary of two). With one
  // sentence the ternary has nothing to choose between, so both sites are a plain call.
  assert.strictEqual((tabs.match(/showToast\(PANE_LIMIT_MSG\)/g) || []).length, 2, "both cap toasts use the constant");
  assert.ok(!/showToast\(docked \?/.test(tabs), "no ternary of identical sentences");
  assert.match(tabs, /module\.exports\s*=\s*\{[^}]*PANE_LIMIT_MSG/);
  for (const f of fs.readdirSync(STATIC).filter((n) => n.endsWith(".js") && n !== "tabs.js")) {
    assert.ok(!/Pane limit reached/.test(read(f)), f + " words the cap itself");
  }
});

test("F79: the desktop chip takes its text colour from the badge colour; the phone badge and strip are untouched", () => {
  const html = read("index.html");
  const iEnv = html.indexOf('src="/envbadge.js"'), iApp = html.indexOf('src="/app.js"');
  assert.ok(iEnv > 0 && iApp > iEnv, "envbadge.js loads before app.js");
  const app = read("app.js");
  const i = app.indexOf("const sideEnv = $(\"side-env\")");
  assert.ok(i > 0, "side-env block not found");
  const block = app.slice(i, app.indexOf("strip.hidden = false", i));
  assert.match(block, /CSS\.supports\("color", meta\.env_color\)/);
  assert.match(block, /sideEnv\.style\.backgroundColor = meta\.env_color/);
  assert.ok(!/style\.background\s*=/.test(block), "no background shorthand on the chip");
  assert.match(block, /parseRgb\(getComputedStyle\(sideEnv\)\.backgroundColor\)/);
  assert.match(block, /sideEnv\.style\.color = envTextColor\(/);
  // the phone: exactly as before
  assert.match(app, /badge\.style\.background = meta\.env_color; strip\.style\.background = meta\.env_color;/);
});
