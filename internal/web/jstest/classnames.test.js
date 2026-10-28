// Every class the page uses must be a class some stylesheet defines.
//
// scripts/lint.sh models the browser for JS *globals*; no check in this repo can
// see a CSS selector. That gap is harmless while class names are stable and
// dangerous during a rename: a name left behind in a JS string literal renders
// unstyled, throws nothing, and fails no test.
//
// The two directions carry different severity on purpose:
//   used-but-undefined  -> FAILS. This is the rename's failure mode.
//   defined-but-unused  -> printed only. Compound state selectors and classes
//     applied through paths this extractor cannot follow would otherwise fail
//     constantly, and a guard that cries wolf gets deleted.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");

// Classes we define. Vendored stylesheets count: xterm, CodeMirror and
// highlight.js define names our markup legitimately uses.
const CSS_FILES = [
  "tokens.css", "primitives.css", "app.css", "desktop.css",
  "xterm/xterm.css", "vendor/highlight-theme.css", "vendor/codemirror.min.css",
  "vendor/webtui/box.css", "vendor/webtui/separator.css",
];

// Classes carried by an element purely so JS can find it again — never styled,
// so no stylesheet defines them and none should. Each entry names the query that
// justifies it. Keep this list SHORT: a growing list means the extractor needs
// fixing, not the list extending.
const HOOKS = new Set([
  "an-timewrap",  // analytics.js:400 querySelector(".an-timewrap")
  "fs-dec",  // app.js initSettings/bumpFs: classList.contains("fs-dec") picks the decrement button
  "fs-inc",  // app.js initSettings/bumpFs: classList.contains("fs-inc") picks the increment button
  "fs-val",  // app.js initSettings/bumpFs: querySelector('.fs-val[data-fs="…"]'); visual styling comes from .stepper .val
  // pagehost.js openPage: body.classList.add("page-" + id), the S4 contract's
  // per-page body hook for later page CSS; built by concatenation, so the
  // extractor above cannot see it.
  "page-settings-modal", "page-analytics-modal", "page-audit-page",
]);

function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

// Selector text is "everything before a '{', after the last '}' before it".
//
// The obvious `chunk.split("{")[0]` is WRONG: inside `@media (…) { .foo { … } }`
// it yields the media query and drops `.foo` entirely, so every class defined
// only inside a breakpoint reads as undefined. index.html's mobile block is 55
// lines of exactly that.
function scanCssInto(out, css) {
  for (const part of stripComments(css).split("{")) {
    const sel = part.slice(part.lastIndexOf("}") + 1);
    for (const m of sel.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) out.add(m[1]);
  }
}

// login.html links tokens.css and primitives.css but also keeps a small
// page-specific layout <style> block of its own — not a standalone file
// CSS_FILES can list. Classes defined there must still count as defined.
// Without this, .card and .sub read as undefined forever, which isn't the
// failure this guard exists to catch.
const HTML_FILES = ["index.html", "login.html"];

function definedClasses() {
  const out = new Set();
  for (const rel of CSS_FILES) {
    const p = path.join(STATIC, rel);
    if (!fs.existsSync(p)) continue;
    scanCssInto(out, fs.readFileSync(p, "utf8"));
  }
  for (const rel of HTML_FILES) {
    const p = path.join(STATIC, rel);
    if (!fs.existsSync(p)) continue;
    const html = fs.readFileSync(p, "utf8");
    for (const m of html.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) scanCssInto(out, m[1]);
  }
  return out;
}

function usedClasses() {
  const out = new Map(); // class -> first file that used it
  const files = ["index.html", "login.html"].concat(
    fs.readdirSync(STATIC).filter((f) => f.endsWith(".js"))
  );
  for (const f of files) {
    const src = fs.readFileSync(path.join(STATIC, f), "utf8");
    const add = (raw) => {
      for (const c of String(raw).split(/\s+/)) {
        // Skip template holes (`${cls}`) and concatenation prefixes
        // (`grid.className = "cols-" + n` yields the literal "cols-").
        if (!c || c.includes("$") || c.endsWith("-")) continue;
        if (!out.has(c)) out.set(c, f);
      }
    };
    // class="a b c". Only markup: no .js file in this app carries a class=
    // literal, and scanning JS for one would only find HTML in a comment.
    if (f.endsWith(".html")) {
      for (const m of src.matchAll(/class=["']([^"']*)["']/g)) add(m[1]);
    }
    // el.className = "a b" / += "a b"
    for (const m of src.matchAll(/\.className\s*\+?=\s*["']([^"']*)["']/g)) add(m[1]);
    // classList.add("a", "b") / .remove("a") — every argument is a class.
    for (const m of src.matchAll(/classList\.(?:add|remove)\(([^)]*)\)/g)) {
      for (const q of m[1].matchAll(/["']([^"']+)["']/g)) add(q[1]);
    }
    // classList.toggle("a", cond) / .contains("a") — only the FIRST argument is
    // a class. Taking every quoted string here reads the condition as one:
    // app.js:327 `toggle("muted", st.kind !== "text")` would report "text".
    for (const m of src.matchAll(/classList\.(?:toggle|contains)\(\s*["']([^"']+)["']/g)) {
      add(m[1]);
    }
    // { cls: "dot working" } — a class handed back as a plain object property,
    // consumed elsewhere as `act.cls` (activity.js:8-13, app.js:447, tabs.js:195)
    // or `d.cls` (analytics.js). `cls:` as a key appears in exactly these two
    // files under static/*.js, both genuine class-value maps.
    for (const m of src.matchAll(/\bcls\s*:\s*["']([^"']+)["']/g)) add(m[1]);
    // setAttribute("class", "a b") — a plain literal none of the shapes above
    // match. analytics.js:773 is the only call site.
    for (const m of src.matchAll(/setAttribute\(\s*["']class["']\s*,\s*["']([^"']*)["']/g)) {
      add(m[1]);
    }
  }
  return out;
}

test("every class used by the page is defined by a stylesheet", () => {
  const defined = definedClasses();
  const used = usedClasses();
  const missing = [];
  for (const [cls, file] of used) {
    if (!defined.has(cls) && !HOOKS.has(cls)) missing.push(`${cls}  (${file})`);
  }
  assert.deepEqual(
    missing, [],
    "these classes are used but no stylesheet defines them — each renders unstyled " +
    "with no error:\n  " + missing.join("\n  ")
  );
});

test("classes defined but never used are reported, not failed", () => {
  const defined = definedClasses();
  const used = new Set(usedClasses().keys());
  const dead = [...defined].filter((c) => !used.has(c)).sort();
  if (dead.length) console.log(`[classnames] ${dead.length} defined-but-unused: ${dead.join(" ")}`);
  assert.ok(true);
});
