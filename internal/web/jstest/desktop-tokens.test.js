// The desktop design tokens (UI plan 1, #171) live in the :root of desktop.css's
// one @media block, so mobile never sees them. The values are the design kit's
// (docs/superpowers/design/2026-11-ui-audit/canvas/audit-2026-11/ui-kit.css), whose
// header holds the measured contrast table. Read as text, like desktop-scope.test.js:
// a token that is missing, mistyped or declared twice would otherwise only show as a
// subtly wrong colour or size on one page.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const read = (f) => stripComments(fs.readFileSync(path.join(STATIC, f), "utf8"));

// rootDecls: the [name, value] pairs of the first `:root { ... }` in `css`, in source
// order. No value in the block holds a semicolon or a brace, so a split on ";" is exact.
function rootDecls(css) {
  const m = /:root\s*\{([^{}]*)\}/.exec(css);
  assert.ok(m, ":root block found");
  return m[1].split(";").map((d) => d.trim()).filter(Boolean).map((d) => {
    const i = d.indexOf(":");
    return [d.slice(0, i).trim(), d.slice(i + 1).trim().replace(/\s+/g, " ")];
  });
}

const decls = rootDecls(read("desktop.css"));
const tok = new Map(decls);

function expectTokens(expected) {
  for (const [name, value] of Object.entries(expected)) {
    assert.strictEqual(tok.get(name), value, name);
  }
}

test("raise and line keep the locked values (founder lock C1, spec section 4)", () => {
  expectTokens({ "--raise": "#1b1f24", "--line": "#23282f" });
});

test("the five approved tokens have the kit values", () => {
  expectTokens({
    "--hover": "#181b1f",
    "--amber-tint": "#26241f",
    "--red-tint": "#24181b",
    "--accent-tint": "#212b3b",
    "--term-sel": "#283a54",
  });
});

test("kit tokens exist", () => {
  expectTokens({
    "--on-fill": "#0d0f12",
    "--accent-hover": "#85b6ff",
    "--amber-hover": "#f8c76a",
    "--t-xs": "11px", "--lh-xs": "16px",
    "--t-sm": "12px", "--lh-sm": "18px",
    "--t-md": "13px", "--lh-md": "20px",
    "--t-lg": "15px", "--lh-lg": "22px",
    "--t-xl": "18px", "--lh-xl": "24px",
    "--t-kpi": "22px", "--lh-kpi": "28px",
    "--track-caps": ".06em",
    "--sp-1": "4px", "--sp-2": "8px", "--sp-3": "12px", "--sp-4": "16px",
    "--sp-5": "20px", "--sp-6": "24px", "--sp-8": "32px", "--sp-10": "40px",
    "--h-sm": "24px", "--h-ctl": "28px", "--h-lg": "32px",
    "--h-row": "32px", "--h-menu": "30px", "--h-ph": "36px",
    "--e-pop": "0 1px 2px rgba(0,0,0,.40), 0 8px 24px rgba(0,0,0,.50)",
    "--e-dlg": "0 2px 6px rgba(0,0,0,.40), 0 24px 64px rgba(0,0,0,.60)",
    "--focus-w": "2px", "--focus-off": "2px",
    "--term-sel": "#283a54",
  });
  const ansi = [
    "#23282f", "#ec6a6e", "#5ccb8f", "#e2b65e", "#6ea8fe", "#c391ea", "#55c3d8", "#c9ced4",
    "#7a828e", "#ff8f8f", "#82e0ab", "#f2cd7e", "#98c2ff", "#d8b2f6", "#86d9e8", "#f2f4f6",
  ];
  ansi.forEach((hex, i) => assert.strictEqual(tok.get("--ansi-" + i), hex, "--ansi-" + i));
});

test("the rest of the kit :root exists: scrim, weights, pane ring, terminal base, rail", () => {
  expectTokens({
    "--scrim": "rgba(5, 6, 8, .62)",
    "--w-regular": "400", "--w-medium": "500", "--w-semibold": "600",
    "--pane-ring": "inset 0 0 0 1px var(--accent)",
    "--term-bg": "var(--bg)", "--term-fg": "var(--fg)", "--term-cursor": "var(--accent)",
    "--rail-w": "52px",
  });
  assert.ok(![...tok.keys()].some((n) => /^--w-/.test(n) && tok.get(n) === "700"), "no 700 weight token");
});

test("no token is declared twice in the desktop :root", () => {
  // A second declaration silently wins, so a copy of a kit token that already exists
  // (--bg, --accent, --sidebar-w ...) would change nothing today and hide a drift later.
  const seen = new Set();
  const dupes = [];
  for (const [name] of decls) {
    if (seen.has(name)) dupes.push(name);
    seen.add(name);
  }
  assert.deepStrictEqual(dupes, []);
});

test("the global focus ring keeps tokens.css's 2px solid accent, so --focus-w agrees with it", () => {
  // desktop.css sets --focus-w and --focus-off only; --focus stays tokens.css's. The
  // outline width and --focus-w are two spellings of one number: if either moves, the
  // kit's focus ring (primitives built on --focus-w) and the global ring would disagree.
  const base = new Map(rootDecls(read("tokens.css")));
  assert.strictEqual(base.get("--focus"), "2px solid var(--accent)");
  assert.strictEqual(tok.has("--focus"), false, "desktop :root does not redefine --focus");
  assert.strictEqual(base.get("--focus").split(" ")[0], tok.get("--focus-w"));
});

// WCAG 2.x contrast ratio of two #rrggbb colours (relative luminance, sRGB transfer).
function luminance(hex) {
  const n = parseInt(hex.slice(1), 16);
  const lin = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) + 0.0722 * lin(n & 255);
}
function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

test("contrast: muted text on panel is at least 5.79, faint (non-text) on panel is 3.05", () => {
  const panel = tok.get("--panel");
  assert.match(panel, /^#[0-9a-f]{6}$/i);
  assert.ok(contrast(tok.get("--muted"), panel) >= 5.79, "muted on panel " + contrast(tok.get("--muted"), panel));
  assert.strictEqual(contrast(tok.get("--faint"), panel).toFixed(2), "3.05");
});
