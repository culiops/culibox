"use strict";
// Source-level pins for the New session profile search (spec 5.5, builder-check
// rulings 2-4). The DOM half needs a browser, so these pin the wiring the way
// pending-sites.test.js does; profileMatches itself is tested in spawnform.test.js.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

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

const panel = read("spawnpanel.js");

test("renderSpawnPlist keeps the search input: full build only without it, else only #sp-rows", () => {
  const b = body(panel, "renderSpawnPlist");
  assert.match(b, /PLIST_SEARCH_MIN/);
  assert.match(b, /sp-rows/);
  assert.match(b, /sp-search/);
  // one host.replaceChildren( (first / threshold-crossing build), behind a guard
  assert.strictEqual((b.match(/host\.replaceChildren\(/g) || []).length, 1);
  assert.match(b.slice(0, b.indexOf("host.replaceChildren(")), /if \(/);
  assert.match(b, /spawnPlistFilter\(/);
});

test("the search input is built outside the rows, with the placeholder", () => {
  const b = body(panel, "spawnPlistSearchInput");
  assert.match(b, /Filter profiles/);
  assert.match(b, /sp-search/);
  assert.match(b, /spawnPlistSearchKey/);
});

test("search keydown: submit hotkey, Escape-clears, ArrowDown, Enter", () => {
  const b = body(panel, "spawnPlistSearchKey");
  assert.match(b, /isSubmitHotkey\(/);
  assert.match(b, /\$\("spawn"\)\.requestSubmit\(\)/);
  assert.match(b, /ArrowDown/);
  assert.match(b, /\.focus\(\)/);
  assert.match(b, /sp-item:not\(\[hidden\]\)/);
  assert.match(b, /Enter/);
  assert.match(b, /spawnPlistPick\(/);
  // Escape with text clears and keeps the dialog open
  const esc = b.slice(b.indexOf('"Escape"'));
  assert.match(esc, /stopPropagation\(\)/);
  assert.match(esc, /preventDefault\(\)/);
  assert.match(esc, /value\s*=\s*""/);
});

test("Enter in the search selects the first visible match and keeps focus in the input", () => {
  const b = body(panel, "spawnPlistSearchKey");
  const enter = b.slice(b.indexOf('e.key === "Enter"'));
  assert.match(enter, /sp-item:not\(\[hidden\]\)/);
  assert.match(enter, /dataset\.name/);
  assert.doesNotMatch(enter, /\.close\(|closeDialog\(|requestSubmit\(|\.focus\(/);
});

test("spawnPlistMove skips hidden rows and ArrowUp from the first visible row returns to the search", () => {
  const b = body(panel, "spawnPlistMove");
  assert.match(b, /\[hidden\]/);
  assert.match(b, /sp-search/);
  assert.match(b, /\.focus\(\)/);
});

test("the filter hides with the hidden attribute (never removes rows) and names the empty case", () => {
  const b = body(panel, "spawnPlistFilter");
  assert.match(b, /profileMatches\(/);
  assert.match(b, /\.hidden\s*=/);
  assert.match(b, /sp-empty/);
  assert.match(b, /No profile matches "/);
});

test("the query resets on every open, before the list is built", () => {
  const reset = body(panel, "spawnPlistResetSearch");
  assert.match(reset, /value\s*=\s*""/);
  const open = body(read("app.js"), "openSpawnDialog");
  const r = open.indexOf("spawnPlistResetSearch(");
  assert.ok(r > 0, "openSpawnDialog resets the query");
  assert.ok(r < open.indexOf("spawnPanelOpen("), "reset before spawnPanelOpen");
});

test("the search is never auto-focused on open", () => {
  const open = body(read("app.js"), "openSpawnDialog");
  assert.doesNotMatch(open, /sp-search/);
  assert.doesNotMatch(body(panel, "renderSpawnPlist"), /\.focus\(\)/);
});

test("desktop.css: hidden rows really hide, the rows scroll under a fixed input, empty line is muted", () => {
  const css = read("desktop.css");
  assert.match(css, /\.sp-item\[hidden\]\s*\{\s*display:\s*none/);
  assert.match(css, /#sp-rows\s*\{[^}]*overflow-y:\s*auto/);
  assert.match(css, /#sp-search\s*\{[^}]*var\(--line\)/);
  assert.match(css, /#sp-search\s*\{[^}]*var\(--r-ctl\)/);
  assert.match(css, /\.sp-empty\s*\{[^}]*var\(--muted\)/);
});
