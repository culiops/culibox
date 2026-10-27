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

test("desktop.css is one @media (min-width: 701px) block and nothing else", () => {
  const css = stripComments(read("desktop.css")).trim();
  assert.ok(css.startsWith("@media (min-width: 701px) {"), "must open with the desktop media query");
  let depth = 0;
  let zeroAt = [];
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      assert.ok(depth >= 0, "unbalanced } at " + i);
      if (depth === 0) zeroAt.push(i);
    }
  }
  assert.strictEqual(depth, 0, "braces must balance");
  assert.deepStrictEqual(zeroAt, [css.length - 1], "depth may return to 0 only at the last character");
  // The only other @media allowed is one nested (hover: none) rule: the row's
  // ⋯ stays visible on a touch screen wider than 700px (#12). Nested, so it is
  // still inside the desktop block (depth check above).
  const medias = css.match(/@media[^{]*/g) || [];
  assert.strictEqual(medias.filter((m) => /min-width:\s*701px/.test(m)).length, 1, "one desktop @media");
  assert.deepStrictEqual(medias.filter((m) => !/min-width:\s*701px/.test(m)).map((m) => m.trim()), ["@media (hover: none)"], "only the nested touch rule");
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
