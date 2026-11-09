"use strict";
// The CuliBox mark is drawn from one source, /favicon.svg (and its needs-you
// twin), but two places carry their own copy and have no JS to test: the login
// page inlines it, because /favicon.svg answers 401 before you log in, and the
// sidebar draws its glyph inline so the colours follow the ground (--accent and
// --ok are the desktop palette above 700px, Catppuccin below). A logo change
// that edits the favicon and forgets a copy is what these tests catch. Read as
// text, like desktop-chrome.test.js.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (name) => fs.readFileSync(path.join(STATIC, name), "utf8");
const favicon = read("favicon.svg").trim();
const faviconNeeds = read("favicon-needs.svg").trim();

// glyph: the drawing inside an SVG's <g>, without its colours: the box path's
// d and the dot's geometry. The ground tile and the translate are left out, so
// the 16px favicon and the 14px sidebar glyph compare equal.
function glyph(svg) {
  const g = svg.match(/<g\b[^>]*>([\s\S]*?)<\/g>/);
  assert.ok(g, "no <g> in: " + svg);
  const d = g[1].match(/<path\b[^>]*\sd="([^"]+)"/);
  const rect = g[1].match(/<rect\b([^>]*)>/);
  assert.ok(d && rect, "no path or rect in the <g> of: " + svg);
  const attrs = {};
  for (const k of ["x", "y", "width", "height", "rx"]) {
    const m = rect[1].match(new RegExp("\\s" + k + '="([^"]+)"'));
    attrs[k] = m && m[1];
  }
  return { d: d[1], rect: attrs };
}

// token: a colour from tokens.css's :root, the app's base palette.
function token(name) {
  const m = read("tokens.css").match(new RegExp("[\\s;{]" + name + ":\\s*(#[0-9a-fA-F]{3,8})\\b"));
  assert.ok(m, name + " not found in tokens.css");
  return m[1].toLowerCase();
}

test("the login page's inline icon is /favicon.svg", () => {
  const m = read("login.html").match(/<link rel="icon"[^>]*href='data:image\/svg\+xml,([^']*)'/);
  assert.ok(m, "login.html has no inline svg icon");
  assert.strictEqual(decodeURIComponent(m[1]), favicon);
});

test("the sidebar logo draws the favicon's glyph in the ground's colours", () => {
  const m = read("index.html").match(/<svg class="side-logo"[\s\S]*?<\/svg>/);
  assert.ok(m, "index.html has no .side-logo svg");
  const side = m[0];
  assert.deepStrictEqual(glyph(side), glyph(favicon));
  // The frame of logo/doorway/glyph-*.svg: the 12-unit glyph with a 1-unit
  // margin. Another viewBox or offset crops or shifts the mark.
  assert.match(side, /^<svg\b[^>]*\sviewBox="0 0 14 14"/);
  assert.match(side, /<g transform="translate\(1 1\)">/);
  assert.match(side, /<path\b[^>]*stroke="var\(--accent\)"/);
  assert.match(side, /<rect\b[^>]*fill="var\(--ok\)"/);
  assert.doesNotMatch(side, /#[0-9a-fA-F]{3,8}\b/, "a fixed colour would ignore the ground");
});

test("the needs-you icon is the same mark in its needs state", () => {
  const colours = (svg) => (svg.match(/#[0-9a-fA-F]{3,8}\b/g) || []).map((c) => c.toLowerCase());
  const ground = (svg) => svg.match(/^<svg\b[^>]*>\s*<rect\b[^>]*fill="([^"]+)"/)[1];
  const stroke = (svg) => svg.match(/<path\b[^>]*stroke="([^"]+)"/)[1];
  const dot = (svg) => svg.match(/<g\b[\s\S]*<rect\b[^>]*fill="([^"]+)"/)[1].toLowerCase();
  assert.strictEqual(ground(faviconNeeds), ground(favicon));
  assert.strictEqual(stroke(faviconNeeds), stroke(favicon));
  // The needs state opens the door and moves the dot, nothing else: the work
  // shape with a yellow dot would not read as a change at 16px.
  const work = glyph(favicon), needs = glyph(faviconNeeds);
  assert.notStrictEqual(needs.d, work.d, "the door must open wider");
  assert.notDeepStrictEqual(needs.rect, work.rect, "the dot must step out");
  const frame = (svg) => svg.replace(/\sd="[^"]+"/, "").replace(/(<g\b[\s\S]*<rect\b)[^>]*>/, "$1>").replace(/#[0-9a-fA-F]{3,8}\b/g, "");
  assert.strictEqual(frame(faviconNeeds), frame(favicon));
  assert.strictEqual(dot(favicon), token("--ok"));
  assert.strictEqual(dot(faviconNeeds), token("--warn-fg"));
  // Only the base palette: no colour (purple included) enters through the icon.
  const palette = ["--bg", "--accent", "--ok", "--warn-fg"].map(token);
  for (const c of [...colours(favicon), ...colours(faviconNeeds)]) {
    assert.ok(palette.includes(c), c + " is not one of " + palette.join(" "));
  }
});

// Plan 7 (#177, DD§F2): the runtime icons are data: URIs built in notifymodel.js, because a
// static /favicon-*.svg is behind auth and cannot load exactly when "Offline" shows. They
// must stay the same mark in the kit palette.
test("the runtime favicon states keep the mark's geometry and the kit palette", () => {
  const nm = require("../static/notifymodel.js");
  const dec = (k) => decodeURIComponent(nm.faviconSvg(k).slice("data:image/svg+xml,".length));
  const dOf = (svg) => svg.match(/<path\b[^>]*\sd="([^"]+)"/)[1];
  assert.strictEqual(dOf(dec("normal")), glyph(favicon).d, "normal is favicon.svg's door");
  for (const k of ["amber", "neutral", "offline", "amber-ring", "neutral-ring"]) {
    assert.strictEqual(dOf(dec(k)), glyph(faviconNeeds).d, k + " is favicon-needs.svg's open door");
  }
  const hex = (svg) => new Set((svg.match(/#[0-9a-f]{6}\b/g) || []));
  const kit = new Set(["#0d0f12", "#23282f", "#6ea8fe", "#4ccf8a", "#f5b94a", "#8b939e", "#f06a6a"]);
  for (const k of ["normal", "amber", "neutral", "offline", "normal-ring", "amber-ring", "neutral-ring"]) {
    for (const c of hex(dec(k))) assert.ok(kit.has(c), k + " uses " + c + ", not a kit colour");
  }
  assert.ok(!dec("amber").includes("#f06a6a") && dec("amber-ring").includes("#f06a6a"), "red only for the write ring");
  const index = read("index.html");
  assert.ok(index.includes('<script src="/notifymodel.js"></script>'), "index.html loads notifymodel.js");
  assert.ok(index.indexOf("/notifymodel.js") < index.indexOf("/app.js"), "before app.js");
});
