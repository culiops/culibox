// The dock's code font (P5, mockup 4, founder pick "ship JetBrains Mono"): desktop.css declares the vendored font and the
// --font-code token, and the dock's code text uses it: the Monaco editor and every diff editor (editortab.js EDITOR_FONT, through
// editorOptions), the dock's read view, and markdown code. The terminal and --font-mono stay as they are (not asked; a separate
// founder call), and the phone (app.css) is not touched.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const css = strip(read("desktop.css"));
const app = strip(read("app.css"));

function rulesFor(src, sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(src))) {
    if (m[1].split(/,(?![^(]*\))/).map((x) => x.trim()).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  }
  return out.join(" ");
}

const STACK = '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

test("three @font-face rules name the vendored subsets, upright, weights 100-800, swap, with the package's unicode ranges", () => {
  const faces = [...css.matchAll(/@font-face\s*\{([^}]*)\}/g)].map((m) => m[1].replace(/\s+/g, " "));
  const jb = faces.filter((f) => /font-family: "JetBrains Mono"/.test(f));
  assert.equal(jb.length, 3);
  const manifest = fs.readFileSync(path.join(STATIC, "vendor", "jetbrains-mono", "MANIFEST.sha256"), "utf8");
  for (const sub of ["latin", "latin-ext", "vietnamese"]) {
    const file = "jetbrains-mono-" + sub + "-wght-normal.woff2";
    const f = jb.find((x) => x.includes('url("/vendor/jetbrains-mono/' + file + '")'));
    assert.ok(f, sub);
    assert.ok(manifest.includes("  " + file + "\n"), file + " is vendored");
    assert.match(f, /format\("woff2"\)/);
    assert.match(f, /font-weight: 100 800;/);
    assert.match(f, /font-style: normal;/);
    assert.match(f, /font-display: swap;/);
    assert.match(f, /unicode-range: U\+/);
  }
  assert.match(jb.find((x) => x.includes("vietnamese")), /U\+1EA0-1EF9/);
});

test("--font-code is JetBrains Mono over the old stack; --font-mono is unchanged", () => {
  assert.ok(css.includes("--font-code: " + STACK + ";"));
  assert.ok(css.includes("--font-mono: ui-monospace, \"SF Mono\", Menlo, Consolas, monospace;"));
});

test("the dock's read view and the desktop markdown code use it; the terminal does not", () => {
  assert.match(rulesFor(css, "#drawer .viewer-pre"), /font-family: var\(--font-code\)/);
  assert.match(rulesFor(css, ".md-body code"), /font-family: var\(--font-code\)/);
  assert.ok(!/\.xterm[^{]*\{[^}]*--font-code/.test(css), "the terminal keeps its font");
  assert.ok(!/JetBrains|--font-code/.test(app), "the phone's stylesheet is not touched");
});

test("the editor's font is the same stack, so Monaco and every diff editor draw what the read view draws", () => {
  const ed = read("editortab.js");
  const m = /const EDITOR_FONT = '([^']*)';/.exec(ed);
  assert.ok(m, "EDITOR_FONT is a single-quoted string (the family names hold double quotes)");
  assert.strictEqual(m[1], STACK);
  assert.match(read("monacoload.js"), /const MONACO_FONT = '400 13px "JetBrains Mono"';/);
});
