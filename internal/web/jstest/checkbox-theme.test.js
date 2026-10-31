// Native checkboxes and radios follow the page's dark theme on every page and
// at every width. accent-color alone only tints the CHECKED box; an unchecked
// box is drawn in the light scheme (a white square on the dark page) unless the
// control itself carries color-scheme: dark. One base rule in app.css, outside
// any media query, so no page has to remember it.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const css = fs.readFileSync(path.join(__dirname, "..", "static", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// topLevelRules: [selectorList, body] for rules not nested in an at-rule.
function topLevelRules(src) {
  const out = [];
  let depth = 0, start = 0, sel = "";
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    if (c === "{") {
      if (depth === 0) { sel = src.slice(start, i).trim(); start = i + 1; }
      depth++;
    } else if (c === "}") {
      depth--;
      if (depth === 0) {
        if (!sel.startsWith("@")) out.push([sel, src.slice(start, i)]);
        start = i + 1;
      }
    }
  }
  return out;
}

test("every checkbox and radio is drawn in the dark scheme with the accent", () => {
  const rule = topLevelRules(css).find(([sel]) => {
    const parts = sel.split(",").map((s) => s.trim());
    return parts.includes('input[type="checkbox"]') && parts.includes('input[type="radio"]');
  });
  assert.ok(rule, 'app.css needs a top-level rule for input[type="checkbox"], input[type="radio"]');
  assert.match(rule[1], /color-scheme:\s*dark/);
  assert.match(rule[1], /accent-color:\s*var\(--accent\)/);
});
