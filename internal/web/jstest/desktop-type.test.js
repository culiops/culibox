// The desktop type rules (UI plan 1, #171, audit F06 and F07): every text size is one of
// the six kit steps (--t-xs ... --t-kpi), weights stop at 600, text is never --faint
// (3.05:1 on the panel, a border colour, not a text colour), and uppercase has one
// tracking. Read as text, like desktop-scope.test.js and desktop-tokens.test.js: a raw
// `font-size: 12.5px` added later would otherwise only show as one more size on one page,
// and nobody would notice until the scale had eleven steps again.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const css = stripComments(fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8"));

// parseRules: every block of `src` (comments already stripped) that holds declarations, as
// { selector, decls: [[property, value], ...] } in source order. At-rule containers
// (@media) hold only rules and are walked through; @font-face is kept, its prelude is the
// selector. A ";" or a brace inside a quote or a parenthesis (the data: URL backgrounds)
// is part of the value, so the splitter tracks both.
function parseRules(src) {
  const rules = [];
  const stack = [];
  let buf = "";
  let paren = 0;
  let quote = null;
  const flush = () => {
    const d = buf.trim();
    buf = "";
    if (!d || !stack.length) return;
    const i = d.indexOf(":");
    if (i < 0) return;
    stack[stack.length - 1].decls.push([
      d.slice(0, i).trim().toLowerCase(),
      d.slice(i + 1).replace(/!important/i, "").replace(/\s+/g, " ").trim(),
    ]);
  };
  for (const ch of src) {
    if (quote) { buf += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; buf += ch; continue; }
    if (ch === "(") paren++;
    else if (ch === ")") paren--;
    if (paren === 0 && ch === "{") {
      stack.push({ selector: buf.trim().replace(/\s+/g, " "), decls: [] });
      buf = "";
    } else if (paren === 0 && ch === ";") {
      flush();
    } else if (paren === 0 && ch === "}") {
      flush();
      const done = stack.pop();
      if (done.decls.length) rules.push(done);
    } else {
      buf += ch;
    }
  }
  return rules;
}

// offenders: "selector { property: value }" for every declaration `bad(property, value,
// selector)` flags, so a failure names the rule to fix.
function offenders(src, bad) {
  const out = [];
  for (const r of parseRules(src)) {
    for (const [p, v] of r.decls) if (bad(p, v, r.selector)) out.push(r.selector + " { " + p + ": " + v + " }");
  }
  return out;
}

// A px number where a font size goes. In the `font` shorthand the size is the token that
// is not behind a "/" (`12px/18px`: 12px is the size, 18px the line height, which this
// rule does not touch), so a px preceded by whitespace or the start is the size.
const rawFontSize = (p, v) =>
  (p === "font-size" && /\d(\.\d+)?px/.test(v)) || (p === "font" && /(^|\s)[\d.]+px/.test(v));

// A weight of 700 or more, as a keyword, a number, or the first token of `font`. The
// @font-face range `font-weight: 100 900` is a descriptor, not a weight: no single token
// of it is 700+ on its own, and it fails /^…$/.
const heavy = (p, v) =>
  (p === "font-weight" && /^(bold|bolder|[789]00)$/i.test(v)) ||
  (p === "font" && /(^|\s)(bold|bolder|[789]00)(\s|$)/i.test(v));

const faintText = (p, v) => p === "color" && /^var\(--(faint|foreground2)\)$/.test(v);

test("no raw px font size: text sizes are the six kit steps", () => {
  assert.deepStrictEqual(offenders(css, rawFontSize), []);
});

test("no weight 700: the kit stops at 600", () => {
  assert.deepStrictEqual(offenders(css, heavy), []);
});

test("text is never faint: --faint and --foreground2 are for borders, dots and rules", () => {
  assert.deepStrictEqual(offenders(css, faintText), []);
});

test("uppercase uses the one tracking", () => {
  const out = [];
  for (const r of parseRules(css)) {
    const get = (name) => (r.decls.find(([p]) => p === name) || [])[1];
    if (get("text-transform") !== "uppercase") continue;
    const ls = get("letter-spacing");
    if (ls !== undefined && ls !== "var(--track-caps)") out.push(r.selector + " { letter-spacing: " + ls + " }");
  }
  assert.deepStrictEqual(out, []);
});

test("the env chip takes the kit .chip-env look: weight 600, tracking .02em", () => {
  // It is not uppercase-transformed by CSS (the label arrives as DEV / PROD), so it is
  // outside the track-caps rule and carries the kit's own chip-env tracking.
  const env = parseRules(css).find((r) => r.selector === "#side-env");
  assert.ok(env, "#side-env rule found");
  const d = new Map(env.decls);
  assert.strictEqual(d.get("font-weight"), "600");
  assert.strictEqual(d.get("letter-spacing"), ".02em");
  assert.strictEqual(d.get("font-size"), "var(--t-xs)");
});

test("the dock tree's session mark is a 2px accent bar on the row, and the old dot is not drawn", () => {
  // Task 9a: the dot after the name (a 6px circle, which is why this pin once checked that it set no px font size) became the
  // spec's session bar, so a row this session changed carries `inset 2px 0 0` and the .ft-chg span only stays for filetree.js
  // to find and repaint.
  const rules = parseRules(css);
  const dot = rules.find((r) => r.selector === "#drawer .ft-chg");
  assert.ok(dot, "#drawer .ft-chg is styled");
  assert.strictEqual(new Map(dot.decls).get("display"), "none", "the dot is hidden");
  assert.ok(!rules.some((r) => r.selector === "#drawer .ft-chg::before"), "no dot is drawn any more");
  const bar = rules.find((r) => r.selector === "#drawer .ft-node:has(> .ft-chg)");
  assert.ok(bar, "the row that holds the mark is styled");
  assert.match(new Map(bar.decls).get("box-shadow"), /inset 2px 0 0 var\(--accent\)/);
});

test("the checks find what they should (a fixture of one offender each)", () => {
  const bad = `@media (min-width: 701px) {
    .a { font-size: 12.5px; }
    .b { font: 500 11px/16px var(--font-ui); }
    .c { font-weight: 700; }
    .d { font: 700 var(--t-sm)/18px var(--font-ui); }
    .e { color: var(--faint); }
    .f::placeholder { color: var(--foreground2); }
    .g { text-transform: uppercase; letter-spacing: .04em; }
  }`;
  assert.deepStrictEqual(offenders(bad, rawFontSize), [
    ".a { font-size: 12.5px }",
    ".b { font: 500 11px/16px var(--font-ui) }",
  ]);
  assert.deepStrictEqual(offenders(bad, heavy), [".c { font-weight: 700 }", ".d { font: 700 var(--t-sm)/18px var(--font-ui) }"]);
  assert.deepStrictEqual(offenders(bad, faintText), [".e { color: var(--faint) }", ".f::placeholder { color: var(--foreground2) }"]);
  const ok = `@media (min-width: 701px) {
    .a { font-size: var(--t-sm); border-color: var(--faint); }
    .b { font: 500 var(--t-sm)/18px var(--font-ui); background: url("data:image/svg+xml;utf8,<svg width='10px'/>"); }
    @font-face { font-family: "Inter"; font-weight: 100 900; }
    .c { font-weight: 600; color: var(--muted); }
  }`;
  for (const check of [rawFontSize, heavy, faintText]) assert.deepStrictEqual(offenders(ok, check), []);
});
