// The desktop markdown preview (P5, mockup 3, founder pick M1): Inter like the rest of the app, at the reader's size (--fs-md, 14px
// by default on desktop) and line-height 1.6, with prose blocks held to a 72-character measure while tables and code blocks keep
// the full width. The phone's .md-body (app.css) is unchanged.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const css = strip(fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8"));
const app = strip(fs.readFileSync(path.join(STATIC, "app.css"), "utf8"));

function rulesFor(src, sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(src))) {
    if (m[1].split(/,(?![^(]*\))/).map((x) => x.trim()).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  }
  return out.join(" ");
}

test("desktop .md-body is Inter with no box cap; the size and line-height stay the reader's", () => {
  const r = rulesFor(css, ".md-body");
  assert.match(r, /font-family: var\(--font-ui\);/);
  assert.match(r, /max-width: none;/);
  assert.ok(!/font-size|line-height/.test(r), "app.css's var(--fs-md) and 1.6 apply");
});

test("prose blocks are held to 72ch; tables and code blocks are not", () => {
  for (const sel of [".md-body p", ".md-body ul", ".md-body ol", ".md-body dl", ".md-body blockquote"]) {
    assert.match(rulesFor(css, sel), /max-width: 72ch;/, sel);
  }
  for (const sel of [".md-body table", ".md-body pre"]) assert.ok(!/72ch/.test(rulesFor(css, sel)), sel);
});

test("the phone's .md-body is as it was: the system sans stack, 900px wide at most", () => {
  const r = rulesFor(app, ".md-body");
  assert.match(r, /font-family: -apple-system, "Segoe UI", Roboto, sans-serif;/);
  assert.match(r, /max-width: 900px;/);
  assert.match(r, /font-size: var\(--fs-md\); line-height: 1\.6;/);
});
