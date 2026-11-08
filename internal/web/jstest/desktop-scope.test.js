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

test("login.html links desktop.css after primitives.css (the login page gets the desktop tokens above 700px)", () => {
  const html = read("login.html");
  const p = html.indexOf('href="/primitives.css"');
  const d = html.indexOf('href="/desktop.css"');
  assert.ok(p >= 0, "primitives.css link present");
  assert.ok(d > p, "desktop.css must come after primitives.css so it wins the cascade");
});

test("desktop :root defines the colour-role tokens", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(css, /:root\s*\{[^}]*--amber:\s*#f5b94a/);
  assert.match(css, /:root\s*\{[^}]*--red:\s*#f06a6a/);
  assert.match(css, /:root\s*\{[^}]*--ok:\s*#4ccf8a/);
});

// ---- Analytics palette (S4 Task 8) ------------------------------------------
// Charts use a categorical palette with no status colour. The rule: every
// --an-N is at least 25 degrees of HSL hue from --ok / --amber / --red, or
// near-grey (saturation < 15%).
const hslOf = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const mx = Math.max(r, g, b), mn = Math.min(r, g, b), d = mx - mn, l = (mx + mn) / 2;
  if (!d) return { h: 0, s: 0, l };
  const s = d / (1 - Math.abs(2 * l - 1));
  const h = mx === r ? ((g - b) / d) % 6 : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return { h: (h * 60 + 360) % 360, s, l };
};
const hueGap = (a, b) => { const d = Math.abs(a - b) % 360; return Math.min(d, 360 - d); };
const tokenOf = (css, name) => { const m = css.match(new RegExp("--" + name + ":\\s*(#[0-9a-fA-F]{6})")); return m && m[1]; };

test("desktop --an-1..7 keep 25 degrees of hue from --ok, --amber and --red (or are grey)", () => {
  const css = stripComments(read("desktop.css"));
  const status = ["ok", "amber", "red"].map((n) => { const v = tokenOf(css, n); assert.ok(v, "--" + n); return hslOf(v).h; });
  for (let i = 1; i <= 7; i++) {
    const v = tokenOf(css, "an-" + i);
    assert.ok(v, "desktop.css defines --an-" + i);
    const c = hslOf(v);
    if (c.s < 0.15) continue;
    for (const sh of status) assert.ok(hueGap(c.h, sh) >= 25, `--an-${i} ${v} (hue ${c.h.toFixed(0)}) is within 25 degrees of a status hue ${sh.toFixed(0)}`);
  }
});

test("the hue rule rejects the mobile Catppuccin peach and rose (they sit on amber and red)", () => {
  assert.ok(hueGap(hslOf("#fab387").h, hslOf("#f5b94a").h) < 25, "mobile --an-3 (peach) clashes with amber");
  assert.ok(hueGap(hslOf("#f38ba8").h, hslOf("#f06a6a").h) < 25, "mobile --an-5 (rose) clashes with red");
});

test("desktop --an-up / --an-down keep 25 degrees of hue from --ok, --amber and --red (or are grey), like the series", () => {
  const css = stripComments(read("desktop.css"));
  const status = ["ok", "amber", "red"].map((n) => hslOf(tokenOf(css, n)).h);
  for (const n of ["an-up", "an-down"]) {
    const v = tokenOf(css, n);
    assert.ok(v, "desktop.css defines --" + n);
    const c = hslOf(v);
    if (c.s < 0.15) continue;
    for (const sh of status) assert.ok(hueGap(c.h, sh) >= 25, `--${n} ${v} (hue ${c.h.toFixed(0)}) is within 25 degrees of a status hue ${sh.toFixed(0)}`);
  }
});

// ---- the separating palette (UI plan 5 Task 6, call 8; digest-design §E) -------------------------------------
// WCAG 2.x relative luminance and contrast ratio, lighter colour on top.
const lumOf = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const ratioOf = (a, b) => { const [hi, lo] = [lumOf(a), lumOf(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05); };
const seriesOf = (css) => [1, 2, 3, 4, 5, 6, 7].map((i) => { const v = tokenOf(css, "an-" + i); assert.ok(v, "desktop.css defines --an-" + i); return v; });
const isMagenta = (hex) => { const c = hslOf(hex); const deg = c.h; return c.s >= 0.15 && deg >= 290 && deg <= 345; };

test("desktop --an-1..7 and --an-up / --an-down are the separating palette (call 8)", () => {
  const css = stripComments(read("desktop.css"));
  assert.deepStrictEqual(seriesOf(css), ["#6ea8fe", "#86d9e8", "#7a828e", "#5d6570", "#c391ea", "#c9ced4", "#7b853d"], "slots 1-5 are the frame's colours, 6 and 7 are the added light grey and olive");
  assert.strictEqual(tokenOf(css, "an-up"), "#6ea8fe", "a cost that rose is the salient colour");
  assert.strictEqual(tokenOf(css, "an-down"), "#7a828e");
  assert.doesNotMatch(css, /--an-8:/, "exactly seven slots (AN_SLOTS)");
});

test("series that can touch in a stack (cyclic slot distance 1 and 2) are at least 1.5:1 apart", () => {
  const css = stripComments(read("desktop.css"));
  const P = seriesOf(css);
  // A model missing on a day puts slots two apart side by side; the wrap (the 8th model is slot 1) puts 7 and 1 side by side.
  for (let i = 0; i < 7; i++) for (const d of [1, 2]) {
    const j = (i + d) % 7;
    assert.ok(ratioOf(P[i], P[j]) >= 1.5, `--an-${i + 1} ${P[i]} vs --an-${j + 1} ${P[j]} (distance ${d}) is ${ratioOf(P[i], P[j]).toFixed(3)}:1`);
  }
  assert.ok(ratioOf(tokenOf(css, "an-up"), tokenOf(css, "an-down")) >= 1.5, "up vs down");
  // The check can fail: today's palette, which the issue measured at 1.07 for slots 1 and 2.
  const old = ["#3d86ea", "#d462b1", "#3a6fd0", "#af6af2", "#07a2c6", "#994c96", "#7b84e8"];
  assert.ok(ratioOf(old[0], old[1]) < 1.5 && ratioOf(old[0], old[6]) < 1.5, "the old pairs the test is written against");
});

test("every series colour is at least 3:1 on the card (--panel)", () => {
  const css = stripComments(read("desktop.css"));
  const panel = tokenOf(css, "panel");
  assert.strictEqual(panel, "#14171b");
  for (const [i, v] of [...seriesOf(css), tokenOf(css, "an-up"), tokenOf(css, "an-down")].entries()) {
    assert.ok(ratioOf(v, panel) >= 3, `${i < 7 ? "--an-" + (i + 1) : i === 7 ? "--an-up" : "--an-down"} ${v} is ${ratioOf(v, panel).toFixed(2)}:1 on the panel`);
  }
});

test("no series or compare colour is magenta (saturated, hue 290-345)", () => {
  const css = stripComments(read("desktop.css"));
  for (const [n, v] of [...seriesOf(css).map((v, i) => ["--an-" + (i + 1), v]), ["--an-up", tokenOf(css, "an-up")], ["--an-down", tokenOf(css, "an-down")]]) {
    assert.ok(!isMagenta(v), `${n} ${v} is magenta (hue ${hslOf(v).h.toFixed(0)}, saturation ${(hslOf(v).s * 100).toFixed(0)}%)`);
  }
  // The check can fail: the old --an-2 / --an-up (hue 318, 62%) and --an-6 (hue 302, 34%) are magenta; a grey of the same hue is not.
  assert.ok(isMagenta("#d462b1") && isMagenta("#994c96"));
  assert.ok(!isMagenta("#7a828e") && !isMagenta("#c391ea"), "greys and the pastel violet (hue 274) pass");
});

test("app.css base --an-* are today's hex, and up/down are today's danger/ok (mobile unchanged)", () => {
  const css = stripComments(read("app.css"));
  const today = ["#89b4fa", "#a6e3a1", "#fab387", "#cba6f7", "#f38ba8", "#94e2d5", "#f9e2af"];
  today.forEach((hex, i) => assert.equal(tokenOf(css, "an-" + (i + 1)), hex, "--an-" + (i + 1)));
  assert.match(css, /--an-up:\s*var\(--danger\)/);
  assert.match(css, /--an-down:\s*var\(--ok\)/);
});

test("app.css hides #an-ctl at base (an empty flex item would shift mobile's header by one gap); the page hides the phone's head and title", () => {
  const app = stripComments(read("app.css"));
  const desk = stripComments(read("desktop.css"));
  assert.match(app, /#an-ctl\s*\{\s*display:\s*none;?\s*\}/);
  // The page's own head and toolbar (UI plan 5 Task 4) replace the phone's .dlg-head: it, and the legend title above it, are
  // not drawn on the page, so #an-ctl is never shown there and the page has no rule for it.
  assert.match(desk, /#page-host > #analytics-modal\.as-page > \.dlg-title,\s*#page-host > #analytics-modal\.as-page > \.dlg-head\s*\{\s*display:\s*none/);
  assert.doesNotMatch(desk, /#an-ctl/, "nothing on desktop draws into or shows #an-ctl any more");
});

test("desktop KPI deltas are neutral, not the status red/green", () => {
  const css = stripComments(read("desktop.css"));
  assert.match(css, /#page-host > #analytics-modal\.as-page \.an-kdelta\s*\{\s*color:\s*var\(--muted\)/);
});
