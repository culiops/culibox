"use strict";
// index.html draws no icon shapes of its own (#168): each static chrome glyph is an empty
// <svg data-uicon="name"> that paintStaticIcons (svgicon.js) fills at boot. The one drawn exception is the
// .side-logo brand mark. Read as text: the page is not parsed, the markup is matched.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");

// [id, concept name, whether the button has words of its own (no aria-label needed): #drawer-max-label "Full",
// the blame author, "Show terminal"]
const CHROME = [
  ["side-palette", "search", false],
  ["side-new", "plus", true],
  ["side-collapse", "panel-left-close", false],
  ["side-audit", "scroll-text", true],
  ["side-analytics", "chart-no-axes-column", true],
  ["side-settings", "settings", true],
  ["drawer-x", "x", false],
  ["drawer-pin", "pin", false],
  ["drawer-max", "maximize", true],
  ["dock-blame", "git-commit-horizontal", true],
  ["dock-newfile", "file-plus", false],
  ["dock-more", "ellipsis", false],
  ["drawer-tree-fold", "panel-right-close", false],
  ["drawer-strip-tree", "folder-tree", false],
  ["drawer-strip-changes", "git-compare", false],
  ["drawer-strip-goto", "file-search", false],
  ["dock-agent-term", "square-terminal", true],
];

// The element's markup: its opening tag and everything to its closing tag (these are all <button>s).
function buttonOf(id) {
  const at = html.indexOf('id="' + id + '"');
  assert.ok(at >= 0, id + " is in index.html");
  const open = html.lastIndexOf("<", at);
  const close = html.indexOf("</button>", at);
  assert.ok(close > at, id + " closes");
  const tagEnd = html.indexOf(">", at);
  return { open: html.slice(open, tagEnd + 1), whole: html.slice(open, close + 9) };
}

test("index.html draws no icon shapes inline", () => {
  const rest = html.replace(/<svg class="side-logo"[\s\S]*?<\/svg>/, "");
  assert.notStrictEqual(rest, html, "the .side-logo mark is found and set aside");
  for (const tag of ["<path", "<circle", "<rect", "<polyline", "<line "]) {
    assert.ok(!rest.includes(tag), "no " + tag + " outside .side-logo");
  }
});

test("static chrome placeholders", () => {
  for (const [id, name] of CHROME) {
    const { whole } = buttonOf(id);
    const holes = [...whole.matchAll(/<svg\b[^>]*\bdata-uicon="([^"]+)"[^>]*><\/svg>/g)];
    assert.deepEqual(holes.map((m) => m[1]), [name], id);
  }
  assert.match(buttonOf("dock-blame").whole, /<svg class="dk-blame-i"[^>]*data-uicon="git-commit-horizontal"/, "blame keeps the class editorblame.js measures");
});

test("every icon-only chrome button keeps a name", () => {
  for (const [id, , hasText] of CHROME) {
    if (hasText) continue;
    const m = buttonOf(id).open.match(/\baria-label="([^"]+)"/);
    assert.ok(m && m[1].trim() !== "", id + " has an aria-label");
  }
});

test("a placeholder carries only what paintStaticIcons keeps", () => {
  for (const m of html.matchAll(/<svg\b[^>]*\bdata-uicon=[^>]*>/g)) {
    const attrs = [...m[0].matchAll(/\s([a-z-]+)=/g)].map((a) => a[1]).filter((a) => a !== "data-uicon");
    for (const a of attrs) assert.ok(["id", "class", "width", "height", "aria-hidden"].includes(a), m[0] + " has " + a);
  }
});

// #168 G2 11 / G3 2: the page Esc buttons (Analytics .page-esc, Audit #au-esc) draw the shared close glyph at chip size
// 12 after the "Esc" hint, not the ✕ text. They keep their label. settingshead.js builds Settings' own (settings-shell.test.js).
test("page Esc buttons", () => {
  const escs = [...html.matchAll(/<button\b[^>]*\bpage-esc\b[^>]*>[\s\S]*?<\/button>/g)].map((m) => m[0]);
  assert.equal(escs.length, 2, "Analytics and Audit");
  assert.ok(escs.some((b) => b.includes('id="au-esc"')));
  for (const b of escs) {
    assert.match(b, /aria-label="Close \(Esc\)"/);
    assert.match(b, /title="Close \(Esc\)"/);
    assert.match(b, /<span class="kbd-hint">Esc<\/span><svg data-uicon="x" width="12" height="12" aria-hidden="true"><\/svg><\/button>$/);
    assert.ok(!b.includes("✕"), "no ✕ text");
  }
});
