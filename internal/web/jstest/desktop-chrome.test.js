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

test("the xterm bar stays visible at rest, but only when there is something to scroll (D27)", () => {
  // xterm hides an idle bar as "invisible fade"; a bar with nothing to scroll is
  // plain "invisible". Only the former may be forced visible.
  const sel = ".cell .xterm .xterm-scrollable-element > .scrollbar.invisible.fade";
  const r = rulesFor(sel);
  assert.ok(r.some((b) => /opacity:\s*1\b/.test(b)), "idle bar is opaque");
  assert.ok(r.some((b) => /transition:\s*none/.test(b)), "no fade-out transition");
  assert.ok(r.some((b) => /pointer-events:\s*auto/.test(b)), "still draggable");
  assert.ok(rulesFor(".cell .xterm .xterm-scrollable-element > .scrollbar.invisible").length === 0, "plain .invisible (nothing to scroll) is not overridden");
});

test("the New session dialog is a plain rounded card with a header and Esc, not the box frame", () => {
  assert.ok(rulesFor("#spawn-modal::before").some((b) => /display:\s*none/.test(b)), "box frame hidden");
  const card = rulesFor("#spawn-modal").join(" ");
  assert.ok(/border-radius:\s*var\(--r-dlg\)/.test(card), "rounded card");
  assert.ok(/background:\s*var\(--panel\)/.test(card) && /border:\s*1px solid var\(--line\)/.test(card));
  assert.ok(/width:\s*820px/.test(card) && /max-height:\s*calc\(100vh - 32px\)/.test(card), "size unchanged");
  const title = rulesFor("#spawn-modal .dlg-title").join(" ");
  assert.ok(/position:\s*static/.test(title) && /transform:\s*none/.test(title), "no legend in the border");
  assert.ok(/border-bottom:\s*1px solid var\(--line\)/.test(title), "header rule");
  assert.ok(rulesFor("#spawn-modal .dlg-head").some((b) => /position:\s*absolute/.test(b)), "close in the corner");
  assert.ok(rulesFor("#spawn-modal .dlg-x::before").some((b) => /content:\s*"Esc"/.test(b)), "Esc hint");
  assert.ok(rulesFor("#spawn-modal .dlg-x:hover").some((b) => /var\(--raise\)/.test(b)));
});

test("the profile list does not size the dialog: the form decides the height and the list scrolls", () => {
  const r = rulesFor("#spawn-plist").join(" ");
  assert.ok(/contain:\s*size/.test(r), "contain: size");
  assert.ok(/align-self:\s*stretch/.test(r), "stretch to the form's height");
  assert.ok(/overflow-y:\s*auto/.test(r), "still scrolls");
});

test("the Model select is a styled native select that never clips a long id", () => {
  const r = rulesFor("#effort-slot select").join(" ");
  assert.ok(/appearance:\s*none/.test(r), "own chevron");
  assert.ok(!/max-width:\s*190px/.test(r), "no 190px cap");
  assert.ok(/max-width:\s*none/.test(r) || !/max-width/.test(r));
  assert.ok(/padding-right:\s*28px/.test(r) && /url\(/.test(r) && /border-radius:\s*var\(--r-ctl\)/.test(r));
});

test("checkboxes in the New session form use the accent colour", () => {
  assert.ok(rulesFor('#spawn input[type="checkbox"]').some((b) => /accent-color:\s*var\(--accent\)/.test(b)));
});
