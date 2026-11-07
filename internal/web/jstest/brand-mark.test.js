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
  assert.strictEqual(dot(favicon), token("--ok"));
  assert.strictEqual(dot(faviconNeeds), token("--warn-fg"));
  // Only the base palette: no colour (purple included) enters through the icon.
  const palette = ["--bg", "--accent", "--ok", "--warn-fg"].map(token);
  for (const c of [...colours(favicon), ...colours(faviconNeeds)]) {
    assert.ok(palette.includes(c), c + " is not one of " + palette.join(" "));
  }
});
