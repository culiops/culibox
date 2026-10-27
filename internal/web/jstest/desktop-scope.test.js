// desktop.css is the ONE place desktop-only rules live, and every rule in it
// must sit inside a single (min-width: 701px) block. Mobile (<= 700px) is
// unchanged by construction only if nothing escapes that block, so this guard
// reads the file's structure rather than trusting reviewers to spot a stray
// rule at the top level.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");

test("desktop.css is non-empty", () => {
  assert.ok(stripComments(read("desktop.css")).trim().length > 0);
});

// scopeProblems: why `css` (comments already stripped) is not one desktop
// block, or [] when it is. The only other @media allowed is one NESTED
// (hover: none) rule: the row's ⋯ stays visible on a touch screen wider than
// 700px (#12).
function scopeProblems(css) {
  const out = [];
  if (!css.startsWith("@media (min-width: 701px) {")) out.push("must open with the desktop media query");
  let depth = 0;
  const zeroAt = [];
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth < 0) { out.push("unbalanced } at " + i); break; }
      if (depth === 0) zeroAt.push(i);
    }
  }
  if (depth !== 0) out.push("braces must balance");
  if (zeroAt.length !== 1 || zeroAt[0] !== css.length - 1) out.push("depth may return to 0 only at the last character");
  const medias = (css.match(/@media[^{]*/g) || []).map((m) => m.trim());
  if (medias.filter((m) => /min-width:\s*701px/.test(m)).length !== 1) out.push("one desktop @media");
  const others = medias.filter((m) => !/min-width:\s*701px/.test(m));
  if (others.length > 1 || others.some((m) => m !== "@media (hover: none)")) out.push("only one nested (hover: none) rule may be extra");
  return out;
}

test("desktop.css is one @media (min-width: 701px) block and nothing else", () => {
  assert.deepStrictEqual(scopeProblems(stripComments(read("desktop.css")).trim()), []);
});

test("the scope check rejects a top-level (hover: none) and any other extra @media", () => {
  const block = "@media (min-width: 701px) { .a { color: red; } }";
  assert.deepStrictEqual(scopeProblems(block), []);
  assert.ok(scopeProblems(block + " @media (hover: none) { .b { color: red; } }").length > 0, "top-level hover rule");
  assert.ok(scopeProblems("@media (hover: none) { .b { color: red; } } " + block).length > 0, "hover rule first");
  assert.ok(scopeProblems("@media (min-width: 701px) { @media (max-width: 900px) { .a { color: red; } } }").length > 0, "other nested @media");
  assert.deepStrictEqual(scopeProblems("@media (min-width: 701px) { @media (hover: none) { .a { color: red; } } }"), []);
});

test("index.html links desktop.css after app.css", () => {
  const html = read("index.html");
  const a = html.indexOf('href="/app.css"');
  const d = html.indexOf('href="/desktop.css"');
  assert.ok(a >= 0, "app.css link present");
  assert.ok(d > a, "desktop.css must come after app.css so it wins the cascade");
});

test("desktop :root defines the colour-role tokens", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(css, /:root\s*\{[^}]*--amber:\s*#f5b94a/);
  assert.match(css, /:root\s*\{[^}]*--red:\s*#f06a6a/);
  assert.match(css, /:root\s*\{[^}]*--ok:\s*#4ccf8a/);
});
