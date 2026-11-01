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
  // Centred on the ~52px header (title: 16px padding + one 15px line), not pinned
  // by a fixed top that only matches one control height.
  const head = rulesFor("#spawn-modal .dlg-head").join(" ");
  assert.ok(/top:\s*26px/.test(head) && /transform:\s*translateY\(-50%\)/.test(head), "close centred on the header");
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

// The help dialog is ~33 lines and #ask-modal is overflow:hidden, so the info
// body must scroll on its own or the Close button falls off a short viewport.
// The bound is the dialog's OWN height cap (dialog { max-height: 88vh }), not
// the viewport: .dlg-body becomes a column that clips, #ask-body takes the
// leftover height and scrolls, and the actions row never shrinks. The same
// rules live in app.css (global), so desktop and phones get one guarantee.
test("the info ask body is bounded by the dialog's own cap, on every width (app.css)", () => {
  const app = fs.readFileSync(path.join(__dirname, "..", "static", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => {
    const m = new RegExp(sel.replace(/[.#]/g, "\\$&") + "\\s*\\{([^{}]*)\\}").exec(app);
    assert.ok(m, "app.css has " + sel);
    return m[1];
  };
  const wrap = rule("#ask-modal.info .dlg-body");
  assert.match(wrap, /display:\s*flex/);
  assert.match(wrap, /flex-direction:\s*column/);
  assert.match(wrap, /overflow:\s*hidden/);
  const body = rule("#ask-modal.info #ask-body");
  assert.match(body, /flex:\s*1 1 auto/);
  assert.match(body, /min-height:\s*0/);
  assert.match(body, /overflow:\s*auto/);
  assert.match(body, /margin-bottom:\s*0/, "flex margins do not collapse; the actions row's own margin spaces it");
  assert.match(body, /font:[^;]*var\(--font-mono/);
  assert.match(body, /white-space:\s*pre\s*;/);
  assert.ok(!/(^|[^-])height:\s*calc\(100/.test(body) && !/100dvh|100vh/.test(body), "no viewport-based bound");
  assert.match(rule("#ask-modal.info .ask-actions"), /flex:\s*none/);
});

test("the help dialog hides Cancel at every width (app.css, not only the desktop media query)", () => {
  const app = fs.readFileSync(path.join(__dirname, "..", "static", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /#ask-modal\.info #ask-cancel\s*\{([^{}]*)\}/.exec(app);
  assert.ok(m, "app.css has #ask-modal.info #ask-cancel");
  assert.match(m[1], /display:\s*none/);
});

test("desktop.css does not bound the info body by the viewport", () => {
  const r = rulesFor("#ask-modal.info #ask-body").join(" ");
  assert.ok(!/100dvh|100vh/.test(r), "viewport bound would ignore the dialog's 88vh cap");
});
