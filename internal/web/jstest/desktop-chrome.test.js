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

// splitTop: a selector list's selectors. Only a comma outside parentheses and brackets
// separates two, so ":is(#creds, #cred-drawer) .x" is one selector.
function splitTop(list) {
  const out = [];
  let depth = 0, from = 0;
  for (let i = 0; i < list.length; i++) {
    const c = list[i];
    if (c === "(" || c === "[") depth++;
    else if (c === ")" || c === "]") depth--;
    else if (c === "," && depth === 0) { out.push(list.slice(from, i)); from = i + 1; }
  }
  out.push(list.slice(from));
  return out.map((s) => s.trim().replace(/\s+/g, " "));
}

// rulesFor: the declaration bodies of every rule whose selector list contains
// `sel` exactly as one of its comma-separated selectors.
function rulesFor(sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(block))) {
    if (splitTop(m[1]).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  }
  return out;
}

// The credentials rules are written for the tab and for the drawer the add flows also run in.
const CREDS = "#page-host > #settings-modal.as-page :is(#creds, #cred-drawer)";

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

test("the New session dialog is a plain rounded card with a header and a close, not the box frame", () => {
  assert.ok(rulesFor("#spawn-modal::before").some((b) => /display:\s*none/.test(b)), "box frame hidden");
  const card = rulesFor("#spawn-modal").join(" ");
  assert.ok(/border-radius:\s*var\(--r-dlg\)/.test(card), "rounded card");
  assert.ok(/background:\s*var\(--panel\)/.test(card) && /border:\s*1px solid var\(--line\)/.test(card));
  assert.ok(/width:\s*820px/.test(card) && /max-height:\s*calc\(100vh - 32px\)/.test(card), "size unchanged");
  const title = rulesFor("#spawn-modal .dlg-title").join(" ");
  assert.ok(/position:\s*static/.test(title) && /transform:\s*none/.test(title), "no legend in the border");
  // The header's rule is the body's border-top (frame M4.1, #173), so the title carries none of its own.
  assert.ok(!/border-bottom/.test(title), "the title's rule moved to the body");
  assert.ok(/border-top:\s*1px solid var\(--line\)/.test(rulesFor("#spawn-modal .dlg-body").join(" ")), "header rule");
  assert.ok(rulesFor("#spawn-modal .dlg-head").some((b) => /position:\s*absolute/.test(b)), "close in the corner");
  // The close is the dialogs' one ghost X (F28, #173): a 28px square with no "Esc" word beside it.
  assert.deepStrictEqual(rulesFor("#spawn-modal .dlg-x::before"), [], "no Esc word");
  const x = rulesFor("#spawn-modal .dlg-x").join(" ");
  assert.ok(/width:\s*var\(--h-ctl\)/.test(x) && /color:\s*var\(--muted\)/.test(x), "ghost icon square");
  // Level with the title text: the head is 16px above and 12px below the 28px close (frame M4.1, #173), so the
  // close sits 16px down and 16px in, and the title is centred on the same 28px.
  const head = rulesFor("#spawn-modal .dlg-head").join(" ");
  assert.ok(/top:\s*var\(--sp-4\)/.test(head) && /right:\s*var\(--sp-4\)/.test(head) && !/transform/.test(head), "close level with the title");
  assert.ok(rulesFor("#spawn-modal .dlg-x:hover").some((b) => /background:\s*var\(--hover\)/.test(b) && /color:\s*var\(--fg\)/.test(b)));
});

// F28 (#173): the three plain dialogs (ask, New session, Report a problem) close with the same X: the kit's ghost
// icon button, a --h-ctl square, muted, a --hover fill on hover, the × glyph on the page's type scale.
test("the ask, New session and Report a problem closes are one ghost icon button", () => {
  const SELS = ["#ask-modal .dlg-x", "#spawn-modal .dlg-x", '#bug-modal .dhd .btn.gh[data-close="bug-modal"]'];
  const look = SELS.map((sel) => rulesFor(sel).join(" "));
  const want = [/width:\s*var\(--h-ctl\)/, /min-height:\s*var\(--h-ctl\)/, /padding:\s*0;/, /background:\s*transparent/, /color:\s*var\(--muted\)/,
    /border-radius:\s*var\(--r-ctl\)/, /font-size:\s*var\(--t-lg\)/, /justify-content:\s*center/];
  for (let i = 0; i < SELS.length; i++) for (const re of want) assert.match(look[i], re, SELS[i] + " " + re);
  for (const sel of SELS) {
    const hover = rulesFor(sel + ":hover").join(" ");
    assert.match(hover, /background:\s*var\(--hover\)/, sel);
    assert.match(hover, /color:\s*var\(--fg\)/, sel);
  }
  // The bug dialog's id has to weigh over the box-less dialogs' `.dhd .btn.gh` padding, size and --raise hover.
  assert.ok(rulesFor("dialog:not(.as-page):not([box-]):not(.sheet-dialog) .dhd .btn.gh").length > 0, "the rule it must out-weigh is still there");
  // No "Esc" word on any of the three, and the phone's own close (#spawn-topbar .spawn-x) is not one of them.
  for (const sel of ["#ask-modal .dlg-x::before", "#spawn-modal .dlg-x::before", '#bug-modal .dhd .btn.gh[data-close="bug-modal"]::before']) assert.deepStrictEqual(rulesFor(sel), [], sel);
  assert.deepStrictEqual(rulesFor("#spawn-topbar .spawn-x"), [], "desktop.css leaves the phone sheet's close alone");
});

// The body is a fixed height now (frame M4.1, #173): neither the list nor the form sizes the dialog, each scrolls inside
// its column. (It used to be the other way round: the form decided and the list was kept from stretching it.)
test("the profile list does not size the dialog: the body's height is fixed and the list scrolls", () => {
  const r = rulesFor("#spawn-plist").join(" ");
  assert.ok(/min-height:\s*0/.test(r), "may be shorter than its rows");
  assert.ok(/overflow:\s*auto/.test(r), "still scrolls");
  assert.ok(/height:\s*min\(640px,/.test(rulesFor("#spawn-modal .dlg-body").join(" ")), "the body, not the list or the form, sets the height");
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

// The pane header's container queries (#13, re-measured for #172 Task 3, again after the kit tab/+ Shell
// look and the always-reserved ⋯ ⛶ ×), on the bar's content box (cell width minus 20): below ICONS_MAX
// every tab and Files is an icon, below COLLAPSE_MAX the tabs and Files are gone, below COMPACT_MAX the
// text tabs tighten. The floors are measured widths where the content stops fitting (pane width = content
// box + 20; the bar keeps a 140px name column and the 10px column gap); a threshold under its floor clips a
// control, one far above it hides room that was free. HOW they were measured: Playwright on the review
// instance, a 2-column grid sized so one pane is exactly W px, W = 300..800 in 1px steps, in five states: a
// session pane (a short and a 35-character name), a pane of files, a maximized pane, and a pane under 640px
// with a long write chip. Per width: any control past the side column's edge, any scrollWidth over
// clientWidth, any meta unit half shown or shown without being on the line (or hidden while on it), the
// name's width. Result, all states: no problem at any width. Before (Plan 1's header, same sweep): 575px
// panes clipped by the hovered wide look, a cost cut at 578-635 hovered; after, with NARROW_PANE_PX at 640
// and only ⋯ in the flow below it, the wide look needs a plain 521px pane and a maximized 547px pane.
function containerRules() {
  const out = [];
  const re = /@container\s*\(max-width:\s*(\d+)px\)\s*\{/g;
  let m;
  while ((m = re.exec(block))) {
    let depth = 1, i = re.lastIndex;
    while (depth && i < block.length) { const ch = block[i++]; if (ch === "{") depth++; else if (ch === "}") depth--; }
    out.push({ px: +m[1], body: block.slice(re.lastIndex, i - 1).replace(/\s+/g, " ") });
  }
  return out;
}
const headerRule = (has) => containerRules().find((r) => has.test(r.body));

// NARROW_PANE_PX (tabs.js) is the pane width below which the header is dot, name and ONE chip and only
// ⋯ stays in the flow (D9; m1-1 draws the 575px panes of two columns at 1440px that way). The meta needs
// the room above it: from 640 up the hovered header's side column is 407px, a 577px pane.
test("the narrow verdict is 640px, as the frames draw it", () => {
  const tabs = fs.readFileSync(path.join(__dirname, "..", "static", "tabs.js"), "utf8");
  assert.match(tabs, /const NARROW_PANE_PX = 640;/);
});

test("the pane header's container thresholds are ordered and above the widths where each look fits", () => {
  const compact = headerRule(/\.ph-sep\s*\{/);
  const icons = headerRule(/^\s*\.ph-lbl\s*\{/);
  const collapse = headerRule(/^\s*\.ph-tabs:not\(\.one\), \.ph-files\s*\{/);
  const floor = headerRule(/^\s*\.ph-shell\s*\{/);
  assert.ok(compact && icons && collapse && floor, "the compact, icon, collapse and + Shell floor rules each exist");
  assert.ok(floor.px < collapse.px && collapse.px < icons.px && icons.px < compact.px, "floor < collapse < icons < compact");
  // Measured (see above): three icon tabs + the Files icon + ⋯ + the "+" need a 331px pane (311 content);
  // the three compact text tabs + Files need 439px (419 content); a plain pane's wide look needs 521px,
  // a maximized one (⛶ pinned beside ⋯) 547px (527 content), and one rule serves both.
  assert.ok(collapse.px >= 311, "the icons need 311 px of content box: never shown in less");
  assert.ok(icons.px >= 419, "the compact text tabs need 419 px of content box: never shown in less");
  assert.ok(compact.px >= 527, "a maximized pane's wide look clips Terminal up to a 546 px pane (527 content)");
  // Not far above the floor either: more than ~8px of slack hides room that was free.
  assert.ok(collapse.px <= 311 + 8 && icons.px <= 419 + 8 && compact.px <= 527 + 8, "thresholds sit within 8px of the measured floors");
});

// A maximized pane keeps ⛶ pinned in the flow beside ⋯ (26 px), so its compact text tabs need 26 px more
// than a plain pane's: measured, they need a 465px pane (445 content) where a plain pane's need 439. Below
// its own floor a maximized pane draws the icons; a plain pane keeps its words down to the icon rule.
test("a maximized pane switches to icons at its own, wider floor", () => {
  const icons = headerRule(/^\s*\.ph-lbl\s*\{/);
  const compact = headerRule(/\.ph-sep\s*\{/);
  const maxed = headerRule(/\.cell-bar:has\(\.ph-max\.pin\) \.ph-lbl\s*\{/);
  assert.ok(maxed, "a container rule for the maximized pane's labels");
  assert.ok(maxed.px >= 445, "a maximized pane's text tabs need 445 px of content box");
  assert.ok(icons.px < maxed.px && maxed.px < compact.px, "icons < maximized icons < compact");
  assert.match(maxed.body, /\.cell-bar:has\(\.ph-max\.pin\) \.ph-lbl \{ display: none; \}/);
  assert.match(maxed.body, /\.cell-bar:has\(\.ph-max\.pin\) \.ph-ico \{ display: block; \}/);
  assert.ok(!/\.ph-tab/.test(maxed.body), "the tabs' 5px compact padding already makes the icon a 24px square");
  assert.match(maxed.body, /\.cell-bar:has\(\.ph-max\.pin\) \.ph-files \{ padding: 0 4px; \}/, "Files is a 24px icon square too");
  assert.ok(!/ph-shell/.test(maxed.body), "+ Shell stays: the icons leave it room down to the icon rule");
});

// The founder chose "dot, name, chip, + and ⋯" for a 250-338 px pane: only the
// tabs and the Files toggle hide there (Files is no longer in the tab group, so the collapse rule
// names it). + Shell (24.7 px) beside the 140 px name column and ⋯
// fits from a 227 px pane (207 content), measured; below it, both go.
test("in a narrow pane only the tabs hide; + Shell stays down to the width where it fits", () => {
  const collapse = headerRule(/^\s*\.ph-tabs:not\(\.one\), \.ph-files\s*\{/);
  const floor = headerRule(/^\s*\.ph-shell\s*\{/);
  assert.ok(/^\s*\.ph-tabs:not\(\.one\), \.ph-files\s*\{\s*display:\s*none;?\s*\}\s*$/.test(collapse.body), "the collapse rule hides the tabs of a group of several and the Files toggle, and nothing else");
  assert.ok(/^\s*\.ph-shell\s*\{\s*display:\s*none;?\s*\}\s*$/.test(floor.body), "the floor rule hides + Shell");
  assert.ok(floor.px >= 207, "+ Shell needs 207 px of content box: never shown in less");
  assert.ok(floor.px < 230, "a 250 px pane (230 content) still shows + Shell");
});

// The profile page in a narrow window (Task 11 slice gate, gap 1). The shell takes 502 px (sidebar 290, Settings nav 212), so #pfv is 398 px in a
// 900 px window and 199 px in a 701 px one. The steps are container rules on #pfv, each inside the desktop block (so mobile cannot see them):
// 863 puts the scope under the name, 624 puts a block's label above its body (the Edit link stays on the label's line), 440 puts Remove or Undo
// on its own line under a row's content, 390 stacks every GitHub permission with its control under it (together, never one row alone) and wraps an
// option's segmented control (Agent's Effort: 284 px of choices, which held its grid track open past the block in a 701 to 800 px window), 300 puts
// the kind word above the content and lets the chip after "In a box" wrap (the cards of Runs in and of New profile), 240 trims the margins so Discard
// and Save fit at 701.
test("the profile page stacks its blocks in narrow windows: container steps on #pfv, widest first, each inside the desktop block", () => {
  const steps = containerRules().filter((r) => /#pfv /.test(r.body));
  assert.deepStrictEqual(steps.map((r) => r.px), [863, 624, 440, 390, 300, 240]);
  const at = (px) => steps.find((r) => r.px === px).body;
  // The label on its own line above the body; Edit on the label's line, right-aligned; the body the full width, Advanced's too.
  assert.match(at(624), /\.pfv-block \{ grid-template-columns: minmax\(0, 1fr\) auto;/);
  assert.match(at(624), /\.pfv-act \{ grid-column: 2; grid-row: 1;/);
  assert.match(at(624), /\.pfv-body, [^{]*\.pfv-adv-body \{ grid-column: 1 \/ -1; grid-row: 2;/);
  assert.match(at(624), /\.pfv-rows \{ grid-template-columns: minmax\(0, 1fr\);/, "a row's track may be narrower than its longest word");
  // Remove never sits beside an input: it has its own line under the content, at the right.
  assert.match(at(440), /\.pfv-row \{ grid-template-columns: 52px minmax\(0, 1fr\);/);
  assert.match(at(440), /\.pfv-row > \.pfv-rmbtn \{ grid-column: 2; grid-row: auto; justify-self: end;/);
  assert.match(at(300), /\.pfv-row \{ display: flex; flex-direction: column;/);
  assert.match(at(240), /\.pfv-in \{ padding: 20px 12px 120px;/);
  // A permission and its control share a line while they fit (and wrap on their own if one ever does not); at 390 all of them stack.
  assert.ok(rulesFor("#page-host > #settings-modal.as-page #pfv .pfv-perm").some((b) => /display:\s*flex/.test(b) && /flex-wrap:\s*wrap/.test(b)));
  assert.match(at(390), /\.pfv-perm \{ flex-direction: column; flex-wrap: nowrap; align-items: flex-start;/);
  // An option's segmented control (the fieldset's own, not a permission's 26px one) wraps its choices once they no longer fit in a row, every line
  // 26px as the one line is; a long stored value wraps its text instead of widening the control. Without it the control's min-content (284 px for
  // Effort) held its grid track open and #pfv scrolled sideways at 701 to 800 px (gap N4).
  assert.match(at(390), /\.pfv-field > \.pfv-seg \{ flex-wrap: wrap; align-content: flex-start; height: auto; \}/);
  assert.match(at(390), /\.pfv-field > \.pfv-seg button \{ min-width: 0; max-width: 100%; min-height: 26px; padding: 4px 12px; white-space: normal; overflow-wrap: anywhere; \}/);
  assert.ok(!/\.pfv-perm \.pfv-seg/.test(at(390)), "a permission's Read/Write control keeps its one 26px line");
  assert.ok(at(390).length < 1100, "the 390 step is the permissions' and the option controls' alone");
  // The chip after "In a box" wraps under the title on every tier card, so Runs in's (a 4px overflow at 701) leaves no card either.
  assert.match(at(300), /\.pfv-card \.pfv-card-t \{ flex-wrap: wrap; gap: 2px 8px; \}/);
});

test("below the icon threshold every tab shows its icon and hides its label", () => {
  const icons = headerRule(/^\s*\.ph-lbl\s*\{/);
  assert.ok(/\.ph-lbl\s*\{[^}]*display:\s*none/.test(icons.body), "label hidden");
  assert.ok(/\.ph-ico\s*\{[^}]*display:\s*block/.test(icons.body), "icon shown");
  // 14px icon + 5px each side = 24px, the kit tab's 24px height: a square like the other header icons. The compact
  // rule above sets that padding (the icon rule needs no tab padding of its own); Files has a 1px border, so 4px.
  assert.ok(/\.ph-tab \{ padding: 0 5px; \}/.test(headerRule(/\.ph-sep\s*\{/).body), "compact and icon tabs: 0 5px under the 24px height");
  assert.ok(/\.ph-files \{ padding: 0 4px; \}/.test(icons.body), "Files: 4px + its 1px border each side");
  assert.ok(/\.cell-bar:has\(\.ph-max\.pin\) \.ph-shell\s*\{[^}]*display:\s*none/.test(icons.body), "a maximized pane's pinned button gives + Shell's room");
});

test("the icon is hidden and sized by default, so a wide header is unchanged", () => {
  assert.ok(rulesFor(".ph-ico").some((b) => /display:\s*none/.test(b)), "hidden by default");
  assert.ok(rulesFor(".ph-ico svg").some((b) => /display:\s*block/.test(b) && /width:\s*14px/.test(b) && /height:\s*14px/.test(b)));
  assert.ok(rulesFor(".ph-tab").some((b) => /display:\s*inline-flex/.test(b) && /align-items:\s*center/.test(b)));
});

// #27: the AWS table keeps the automatic column layout, so a name that cannot wrap holds
// its column open past the section's box (overflow: hidden): a 30-character name cut
// Delete off at 1240 px, a 60-character one scrolled the whole page. The name wraps like
// the GitHub and token tables' (hyphens first, mid-word only past the column).
// The token and GitHub tables (table-layout: fixed, shares of the width). Measured on the seeded page (Task 10a, fix
// wave): the tables are 922 px wide at a 1440 window and 762 px at 1280, a cell's padding is 32 px. The Env column's
// 12px host (F06) needs 203 px for artifactory.acme.example.com (25% gave it 199 at 1440: "...example.co" / "m"); the
// Status column keeps its longest word ("environment", 77 px) whole at 1280.
test("the token tables give the 12px host one line at 1440 and keep Status's words whole at 1280", () => {
  const share = (n) => {
    const r = rulesFor(CREDS + ' .ctab-sec:not([data-group="aws"]) .ctab th:nth-child(' + n + ")").join(" ");
    const m = /width:\s*([\d.]+)%/.exec(r);
    assert.ok(m, "column " + n + " has a share");
    return +m[1];
  };
  const [token, env, status, actions] = [share(1), share(2), share(3), share(5)];
  assert.ok(env / 100 * 922 - 32 >= 203, "Env holds artifactory.acme.example.com at 1440: " + env + "%");
  assert.ok(status / 100 * 762 - 32 >= 77, "Status holds \"environment\" at 1280: " + status + "%");
  assert.strictEqual(token, 20, "Token keeps its share: less wraps the token names (measured: +54 px of rows at 1440)");
  assert.strictEqual(actions, 16, "Actions keeps its share: Edit and Delete");
  assert.ok(100 - token - env - status - actions >= 23, "Used by keeps its share within half a point");
});

test("the AWS connection name is allowed to wrap (a nowrap name overflowed the table's box)", () => {
  assert.ok(!/\[data-group="aws"\][^{}]*\.c-nm[^{}]*\{[^}]*white-space:\s*nowrap/.test(block), "no nowrap on the AWS name");
  assert.ok(rulesFor(CREDS + " .ctab .c-nm").some((b) => /display:\s*inline-block/.test(b) && /max-width:\s*100%/.test(b)), "the name still sizes to its box");
  assert.ok(rulesFor(CREDS + " .ctab .c-main").some((b) => /overflow-wrap:\s*break-word/.test(b)), "and breaks between words first");
});

// A name with no hyphen (the name rule allows one: ^[a-z0-9][a-z0-9-]{0,62}$) has no break
// point, and break-word does not lower a column's min-content width, so on the AWS
// table's automatic layout it held the column open like nowrap did (a 30-character one
// overflowed 92px at 1225px, a 60-character one 316px). Only `anywhere` counts in the
// column sizing, so the AWS name gets it. But credCellNode wraps EVERY plain cell in .c-nm
// (the account id, Used by, ...), and those must keep break-word: an account id or a
// profile name splitting mid-word ("1234567890/12") is what the rule above them forbids.
// So it is the first cell's .c-nm only.
test("only the AWS connection name (first cell) may break anywhere, not the account id or Used by", () => {
  const sel = CREDS + ' .ctab-sec[data-group="aws"] .ctab tr > td:first-child .c-nm';
  assert.ok(rulesFor(sel).some((b) => /overflow-wrap:\s*anywhere/.test(b)), "overflow-wrap: anywhere on the AWS name cell's name");
  assert.ok(!rulesFor(CREDS + ' .ctab-sec[data-group="aws"] .ctab .c-nm').length, "no rule reaches every .c-nm of the AWS table");
});

// #123: .btn is nowrap (primitives.css), so in the store strip a 701px window (the strip's
// body is 178px wide there) pushed "Create the encrypted store" (186px) 25px past its
// warning box, and again inside the passphrase form it opens. Wrapping the label is what
// every other button does when it has to.
test("the credentials store strip's buttons wrap instead of spilling out of the strip", () => {
  const rules = rulesFor(CREDS + " .cstrip .btn");
  assert.ok(rules.some((b) => /white-space:\s*normal/.test(b) && /max-width:\s*100%/.test(b)), "wrap, inside the strip's width");
});

// #123: the 310px rule is sized for the three icon tabs and the Files icon beside the name's room. A pane of files
// has one tab (24px), which fits far below it: the group is marked `one` (paneTabsClass)
// and the rule leaves it alone, or the Files tab was gone from every 271-338px pane.
test("the narrow-pane collapse spares a one-tab group (a pane of files keeps its Files tab)", () => {
  assert.ok(headerRule(/^\s*\.ph-tabs:not\(\.one\), \.ph-files\s*\{/), "the collapse rule is scoped to groups that are not `one`");
  assert.ok(!headerRule(/^\s*\.ph-tabs\s*\{\s*display:\s*none/), "no rule hides every .ph-tabs");
});

// Measured (kit + Shell, 1px steps, #172): with + Shell and the 140px name column, one icon tab needs a 253px
// pane (content box 233). Below that it hides too (4px of slack), or the lone Files tab is cut in half.
test("a one-tab group hides only below the width where its single icon stops fitting", () => {
  const floor = headerRule(/^\s*\.ph-tabs\.one\s*\{\s*display:\s*none;?\s*\}\s*$/);
  const collapse = headerRule(/^\s*\.ph-tabs:not\(\.one\), \.ph-files\s*\{/);
  assert.ok(floor, "a rule hides the one-tab group in a very narrow pane");
  assert.ok(floor.px >= 233, "the icon is clipped up to a 252px pane (232 content): never shown in less");
  assert.ok(floor.px < collapse.px, "and it lasts far below the width the icons need");
});

// #172 Task 3 (G6, F13, F14): one selected look for a tab, Files a pressed ghost button, the meta in the kit's
// .ph-meta look, wrapped so a unit leaves whole.
test("a tab has ONE selected look: --raise fill, --fg, no underline, no .open class (G6)", () => {
  const on = rulesFor(".ph-tab.on").join(" ");
  assert.match(on, /color:\s*var\(--fg\)/);
  assert.match(on, /background:\s*var\(--raise\)/);
  assert.deepStrictEqual(rulesFor(".ph-tab.open"), [], "the accent underline of the Files tab is gone");
  assert.ok(!/inset 0 -2px 0 var\(--accent\)/.test(block), "no underline anywhere in the header");
  // A pane of files has Files as its one tab: pressed (the drawer is open) takes the same look.
  const pressed = rulesFor('.ph-tab[aria-pressed="true"]').join(" ");
  assert.match(pressed, /color:\s*var\(--fg\)/);
  assert.match(pressed, /background:\s*var\(--raise\)/);
});

test("the Files toggle is the kit's ghost button, not a tab: no bespoke look, only its place in the row", () => {
  assert.ok(rulesFor(".ph-files").every((b) => !/background|box-shadow|color/.test(b)), "its pressed look is the kit's .btn[aria-pressed]");
  assert.ok(rulesFor('.btn[aria-pressed="true"]').some((b) => /background:\s*var\(--raise\)/.test(b) && /inset 0 0 0 1px var\(--faint\)/.test(b)));
  assert.strictEqual(headerRule(/^\s*\.ph-lbl\s*\{/).body.match(/\.ph-files \{ padding: 0 4px; \}/).length, 1, "icon mode: a 24px square");
});

test("the meta is the kit's .ph-meta look (mono, --t-xs, --muted) and gives way in whole units, cost first", () => {
  const m = rulesFor(".ph-meta").join(" ");
  assert.match(m, /font:\s*var\(--t-xs\)\/var\(--lh-xs\) var\(--font-mono\)/);
  assert.match(m, /color:\s*var\(--muted\)/);
  // The wrap places units in DOM order and drops the last first, so the DOM order (model, ctx, cost) IS the give-way
  // order, cost first (F14, #172 acceptance: a 700px pane keeps haiku-4.5 and ctx 35%). Nothing reverses or reorders it.
  assert.match(m, /flex-flow:\s*row wrap/, "plain row wrap: the last unit wraps away first");
  assert.ok(!/row-reverse|wrap-reverse|direction/.test(m), "no reversed flow");
  assert.deepStrictEqual([".mi-cost", ".mi-ctx", ".mi-model"].flatMap((c) => rulesFor(c)).filter((b) => /order:/.test(b)), [], "no unit is re-ordered: the DOM order is the priority");
  assert.match(m, /height:\s*var\(--lh-xs\)/, "the second row is clipped");
  assert.match(m, /overflow:\s*hidden/);
  assert.match(rulesFor(".ph-meta::before").join(" "), /height:\s*var\(--lh-xs\)/, "the zero-width first item is a full line tall");
  // The meta shrinks far faster than the name, so the name never loses a sub-pixel to Chrome's ellipsis.
  const shrink = (sel) => +/flex:\s*0\s+(\d+)\s+auto/.exec(rulesFor(sel).join(" "))[1];
  assert.ok(shrink(".ph-meta") > shrink(".ph-chip.write") && shrink(".ph-chip.write") >= 10000 && shrink(".ph-chip.cloud") >= 10000);
});

// The frames' pane header (m1-1/m1-2, ui-kit.css .ptab, .btn-ghost .btn-sm): view tabs are the kit's
// tab type (--t-sm, weight 500, --muted, 24px, --r-chip), hover --hover, selected --raise + --fg.
test("a view tab is the kit's .ptab: 24px, --t-sm medium, --muted, hover --hover, selected --raise (G6, F18)", () => {
  const tab = rulesFor(".ph-tab").join(" ");
  assert.match(tab, /height:\s*var\(--h-sm\)/);
  assert.match(tab, /font:\s*(var\(--w-medium\)|500) var\(--t-sm\) var\(--font-ui\)/);
  assert.match(tab, /color:\s*var\(--muted\)/);
  assert.match(tab, /border-radius:\s*var\(--r-chip\)/);
  assert.match(tab, /padding:\s*0 10px/);
  assert.ok(!/var\(--t-md\)|padding:\s*4px/.test(tab), "not Plan 1's 13px / 4px 9px tab");
  const hover = rulesFor(".ph-tab:hover").join(" ");
  assert.match(hover, /color:\s*var\(--fg\)/);
  assert.match(hover, /background:\s*var\(--hover\)/);
});

test("+ Shell is the kit's ghost button: the header keeps no look of its own for it", () => {
  const look = (b) => /background|color|font:|border|box-shadow/.test(b.replace(/font-size:\s*0/, ""));
  assert.ok(rulesFor(".ph-shell").every((b) => !look(b)), "only the compact + label's size and padding, never a fill, colour or border");
  assert.deepStrictEqual(rulesFor(".ph-shell:hover"), [], "hover is .btn-ghost:hover");
  assert.ok(rulesFor(".ph-shell[hidden]").some((b) => /display:\s*none/.test(b)), "a header with no shell profile hides it");
  // The compact "+": the kit button's 6px gap would sit between the label's empty text and the glyph (+6px), so it is 0.
  assert.match(headerRule(/\.ph-sep\s*\{/).body, /\.ph-shell \{ font-size: 0; padding: 0 8px; gap: 0; \}/);
});

// F18 / ruling 2: hovering the header never moves it. ⋯ ⛶ × keep their 24px at all times and only fade in
// (opacity), so the meta's room is the same with the pointer in or out; pointer-events stay off while they
// are invisible so a click cannot land on an icon nobody sees. Keyboard focus reveals them too.
test("the header's ⋯ ⛶ × always hold their 24px: hover and focus change opacity, never width", () => {
  const base = rulesFor(".ph-ic").join(" ");
  assert.match(base, /width:\s*24px/);
  assert.ok(!/width:\s*0/.test(base), "no collapsed width to grow from");
  assert.match(base, /opacity:\s*0/);
  assert.match(base, /pointer-events:\s*none/);
  const shown = rulesFor(".cell-bar:hover .ph-ic").join(" ");
  assert.match(shown, /opacity:\s*1/);
  assert.match(shown, /pointer-events:\s*auto/);
  assert.ok(!/width/.test(shown), "the revealed state changes no size");
  assert.deepStrictEqual(rulesFor(".cell-bar:focus-within .ph-ic"), rulesFor(".cell-bar:hover .ph-ic"), "keyboard focus reveals them like the pointer");
  assert.ok(rulesFor(".ph-ics.nar .ph-more").some((b) => /opacity:\s*1/.test(b) && /pointer-events:\s*auto/.test(b)), "a narrow header's ⋯ is always usable");
});

test("a meta unit that wrapped off the line is visibility: hidden: out of the tab order and the accessibility tree", () => {
  const off = rulesFor(".mi-off").join(" ");
  assert.match(off, /visibility:\s*hidden/);
  assert.ok(!/display:\s*none/.test(off), "no layout change: the fit is measured on the same boxes");
});

// ---- task 9b: the session workspace's chrome takes the frames' metrics (ui-kit.css .ph .ask .srow .rail; m1-1, m5-1) ----------
// Each value is the kit's own, as a token; a raw px is only where the kit writes one (the 2px bar, the 6/8/12/16 paddings).
const decl = (sel) => rulesFor(sel).join(" ");

test("the pane header is the kit's .ph: --panel, --h-ph high, 12px left and 6px right", () => {
  const bar = rulesFor(".cell-bar").find((b) => /container-type/.test(b));
  assert.ok(bar, ".cell-bar rule");
  assert.match(bar, /height:\s*var\(--h-ph\)/);
  assert.match(bar, /padding:\s*0 6px 0 12px/);
  assert.match(bar, /background:\s*var\(--panel\)/);
  assert.match(bar, /border-bottom:\s*1px solid var\(--line\)/);
});

test("the ask strip is the kit's .ask: 8px 12px on --amber-tint, Open terminal a real .btn-sm, Yes text --on-fill", () => {
  const strip = decl(".needstrip");
  assert.match(strip, /padding:\s*8px 12px/);
  assert.match(strip, /background:\s*var\(--amber-tint\)/);
  assert.ok(!/color-mix/.test(strip), "no mixed tint");
  assert.match(decl(".needstrip.is-done"), /background:\s*var\(--panel\)/, "the neutral strip is unchanged");
  // The padding and radius override skips .btn-sm, so Open terminal is the kit's 24px chip-radius button.
  assert.ok(rulesFor(".needstrip .nacts .btn:where(:not(.btn-sm))").some((b) => /padding:\s*3px 10px/.test(b) && /border-radius:\s*var\(--r-ctl\)/.test(b)));
  assert.deepStrictEqual(rulesFor(".needstrip .nacts .btn"), [], "no override reaches a .btn-sm");
  assert.match(decl(".needstrip .nacts"), /align-items:\s*center/, "the buttons centre on the line");
  // Yes is the kit's amber .btn-yes (amber fill, --on-fill text): the strip's secondary fill, border and hover skip it.
  assert.match(decl(".btn-yes"), /background:\s*var\(--amber\)[^}]*color:\s*var\(--on-fill\)/);
  const skip = ".needstrip .nacts .btn:where(:not(.pri):not(.btn-yes))";
  assert.ok(rulesFor(skip).some((b) => /background:\s*var\(--raise\)/.test(b)), "the secondary fill skips Yes");
  assert.ok(rulesFor(skip + ":hover").length === 1 && rulesFor(".needstrip .nacts .btn.off:where(:not(.pri):not(.btn-yes))").length === 1);
  assert.deepStrictEqual(rulesFor('.needstrip .nacts .btn[data-choice="yes"]:where(:not(.pri))'), [], "no bespoke Yes rule left");
  assert.match(decl(".btn-yes:disabled:hover"), /background:\s*var\(--amber\)/, "a disabled Yes keeps its fill on hover");
  assert.ok(!/#1a1304/i.test(block), "no raw dark text left in the strip");
});

test("sidebar group labels and rows follow the frame's rhythm", () => {
  assert.match(decl(".grp"), /padding:\s*12px 8px 4px/);
  assert.deepStrictEqual(rulesFor(".grp:first-child"), [], "the first label has the same 12px above it");
  const row = decl("#sessions .drow");
  // Hover is half a step (--hover), always quieter than the selected row's --raise: a hovered row never reads as a second selected one.
  assert.match(decl("#sessions .drow:hover"), /background:\s*var\(--hover\)/);
  assert.match(decl("#sessions .drow.sel"), /background:\s*var\(--raise\)/);
  assert.ok(block.lastIndexOf("#sessions .drow.sel {") > block.lastIndexOf("#sessions .drow:hover {"), "selected + hovered stays --raise (declared later, same weight)");
  assert.match(row, /gap:\s*10px/);
  assert.match(row, /padding:\s*6px 8px/);
  assert.match(decl("#sessions .drow .sname"), /line-height:\s*var\(--lh-md\)/);
  assert.match(decl("#sessions .drow:not(.nested) .nm"), /font-weight:\s*var\(--w-medium\)/);
  const sub = decl("#sessions .drow .sub");
  assert.match(sub, /line-height:\s*var\(--lh-sm\)/);
  assert.ok(!/margin-top/.test(sub), "the sub-line sits right under the name");
  assert.match(decl(".dchip"), /font:\s*500 var\(--t-xs\)\/18px/, "18px high chip");
  assert.match(decl(".dchip.box"), /font-family:\s*var\(--font-mono\)[^}]*font-weight:\s*var\(--w-regular\)/, "the tier is mono 400 like .chip-box");
  assert.match(decl("#sessions .drow.nested"), /padding:\s*3px 8px 4px 26px/);
});

test("the in-pane bar sits on the sidebar's edge on every in-pane row, faint, accent on the selected one", () => {
  const bar = decl("#sessions .drow.inpane::before");
  assert.match(bar, /left:\s*-8px/);
  assert.match(bar, /top:\s*6px/);
  assert.match(bar, /bottom:\s*6px/);
  assert.match(bar, /width:\s*2px/);
  assert.match(bar, /border-radius:\s*1px/);
  assert.match(bar, /background:\s*var\(--faint\)/);
  assert.match(decl("#sessions .drow.sel.inpane::before"), /background:\s*var\(--accent\)/, "C1: the selected row keeps the accent bar");
  assert.deepStrictEqual(rulesFor("#sessions .drow.inpane"), [], "no inset shadow on the row any more");
  assert.ok(!rulesFor("#sessions .drow.sel").some((b) => /box-shadow/.test(b)));
});

test("the row ⋯ shows on hover, on the selected row and while its own menu is open, pressed in the last case", () => {
  const shown = rulesFor('#sessions .drow .kbtn[aria-expanded="true"]');
  assert.ok(shown.some((b) => /background:\s*var\(--raise\)/.test(b) && /box-shadow:\s*inset 0 0 0 1px var\(--faint\)/.test(b)), "the kit's pressed look");
  const reveal = rulesFor("#sessions .drow:hover .kbtn").join(" ");
  assert.match(reveal, /opacity:\s*1/);
  assert.match(reveal, /width:\s*var\(--h-sm\)/);
  assert.ok(rulesFor("#sessions .drow.sel .kbtn").some((b) => /opacity:\s*1/.test(b)), "the selected row's ⋯ stays");
  assert.ok(rulesFor('#sessions .drow .kbtn[aria-expanded="true"]').some((b) => /opacity:\s*1/.test(b) && /width:\s*var\(--h-sm\)/.test(b)), "an open menu's ⋯ is neither collapsed nor transparent");
  // The pressed look is declared after the hover fill, so hovering the open menu's ⋯ keeps it pressed (the kit's order).
  assert.ok(block.lastIndexOf('#sessions .drow .kbtn[aria-expanded="true"] {') > block.indexOf("#sessions .drow .kbtn:hover {"));
});

test("the sidebar's top and footer are the frame's: 12px 16px, a 28px New session, 12px links", () => {
  assert.match(decl("#side-top"), /padding:\s*12px 16px/);
  assert.match(decl("#side-top"), /gap:\s*var\(--sp-3\)/, "12px between the top rows, as .m1-side-top");
  assert.match(decl("#side-new"), /height:\s*var\(--h-ctl\)/);
  assert.match(decl("#side-foot"), /padding:\s*12px 16px/);
  assert.match(decl(".side-links button"), /font:\s*var\(--t-sm\) var\(--font-ui\)/);
  assert.match(decl(".side-links"), /margin:\s*0 -8px/, "the first link's word starts on the 16px edge");
});

test("the rail: 12px above, 6px apart, dots 32x32, + the kit's 24px icon button (accent, p2)", () => {
  const rail = decl(".sidebar-hidden .rail48");
  assert.match(rail, /padding:\s*12px 0 10px/);
  assert.match(rail, /gap:\s*6px/);
  assert.match(decl(".ri"), /width:\s*32px[^}]*height:\s*32px|height:\s*32px[^}]*width:\s*32px/);
  assert.match(decl(".ri"), /border-radius:\s*var\(--r-ctl\)/);
  const add = decl(".ri.add");
  assert.match(add, /width:\s*var\(--h-sm\)/);
  assert.match(add, /height:\s*var\(--h-sm\)/);
  assert.match(add, /background:\s*var\(--accent\)/, "the decided accent stays");
  assert.match(decl(".ri.on"), /background:\s*var\(--raise\)[^}]*box-shadow:\s*inset 2px 0 0 var\(--accent\)/, "C1: selected = raise + the accent inset");
});
