// Desktop chrome rules that live in desktop.css and have no JS to test: the
// focus ring only when 2+ panes show (D26) and the thin quiet scrollbars
// (D27). They are read as text, like desktop-scope.test.js, and each must sit
// inside the one @media (min-width: 701px) block so mobile stays unchanged.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const css = fs.readFileSync(path.join(__dirname, "..", "static", "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const open = css.indexOf("@media (min-width: 701px) {");
const block = css.slice(open); // the file is one block (desktop-scope.test.js)

// rulesFor: the declaration bodies of every rule whose selector list contains
// `sel` exactly as one of its comma-separated selectors.
function rulesFor(sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(block))) {
    const sels = m[1].split(",").map((s) => s.trim().replace(/\s+/g, " "));
    if (sels.includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  }
  return out;
}

test("desktop.css opens with the desktop media query", () => {
  assert.ok(open >= 0);
});

test("the focus ring is off for a single visible pane and a maximized pane (D26)", () => {
  const one = rulesFor("#grid.cols-1 .cell.focus::before");
  const max = rulesFor("#grid.maximized .cell.focus::before");
  assert.ok(one.some((b) => /content:\s*none/.test(b)), "cols-1 has no ring");
  assert.ok(max.some((b) => /content:\s*none/.test(b)), "maximized has no ring");
});

test("the focus ring itself is still drawn for the focused pane", () => {
  const ring = rulesFor(".cell.focus::before");
  assert.ok(ring.some((b) => /box-shadow:\s*inset 0 0 0 1px var\(--accent\)/.test(b)));
});

test("a 8px transparent webkit scrollbar, --line thumb, --faint on hover (D27)", () => {
  const bar = rulesFor("::-webkit-scrollbar");
  assert.ok(bar.some((b) => /width:\s*8px/.test(b) && /height:\s*8px/.test(b)), "8px both ways");
  assert.ok(rulesFor("::-webkit-scrollbar-track").some((b) => /background:\s*transparent/.test(b)), "no track");
  assert.ok(rulesFor("::-webkit-scrollbar-thumb").some((b) => /var\(--line\)/.test(b) && /border-radius/.test(b)), "rounded --line thumb");
  assert.ok(rulesFor("::-webkit-scrollbar-thumb:hover").some((b) => /var\(--faint\)/.test(b)), "--faint on hover");
  assert.ok(!/::-webkit-scrollbar-thumb[^{]*\{[^}]*var\(--muted\)/.test(block), "never --muted");
});

test("browsers without ::-webkit-scrollbar get scrollbar-width: thin (D27)", () => {
  assert.ok(/@supports not selector\(::-webkit-scrollbar\)\s*\{/.test(block));
  const fb = block.slice(block.indexOf("@supports not selector(::-webkit-scrollbar)"));
  assert.ok(/scrollbar-width:\s*thin/.test(fb));
  assert.ok(/scrollbar-color:\s*var\(--line\) transparent/.test(fb));
});

test("the xterm scrollbar (.scrollbar > .slider, not the native viewport bar) is 8px and styled to match (D27)", () => {
  const w = rulesFor(".cell .xterm .xterm-scrollable-element > .scrollbar.vertical > .slider");
  assert.ok(w.some((b) => /width:\s*8px !important/.test(b)), "inline width needs !important");
  assert.ok(rulesFor(".cell .xterm .xterm-scrollable-element > .scrollbar > .slider.active").some((b) => /var\(--faint\)/.test(b)), "--faint while dragging");
  assert.ok(rulesFor(".cell .xterm .xterm-scrollable-element > .scrollbar > .slider").some((b) => /var\(--line\)/.test(b)));
  assert.ok(rulesFor(".cell .xterm .xterm-scrollable-element > .scrollbar > .slider:hover").some((b) => /var\(--faint\)/.test(b)));
});
