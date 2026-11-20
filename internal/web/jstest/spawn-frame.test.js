"use strict";
// The New session dialog frame on the desktop (UI plan 3, #173; findings F15, F16, G19; frames M4.1 and M4.2):
// anchored 90px from the top (not centred), a fixed 640px body of two scrolling columns, a footer across the
// whole card, and one verb, "Start session". The CSS is read as text, like desktop-chrome.test.js; the label and
// the key hint are pure helpers in spawnform.js, and their wiring is pinned at source level (app.js has no DOM here).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { spawnSubmitLabel, spawnFootHint } = require("../static/spawnform.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const css = strip(read("desktop.css"));
const block = css.slice(css.indexOf("@media (min-width: 701px) {"));

// splitTop: a selector list's selectors (a comma inside parentheses or brackets does not split).
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
// rulesFor: the declaration bodies of every rule that has `sel` as one of its comma-separated selectors.
function rulesFor(sel) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(block))) if (splitTop(m[1]).includes(sel)) out.push(m[2].replace(/\s+/g, " ").trim());
  return out;
}
const rule = (sel) => rulesFor(sel).join(" ");

function body(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// --- one verb, and the hint as key chips (F16) -----------------------------------------------------

test("the submit says Start session on the desktop and keeps Spawn ⏎ on the phone", () => {
  assert.strictEqual(spawnSubmitLabel(true), "Start session");
  assert.strictEqual(spawnSubmitLabel(false), "Spawn ⏎");
});

test("the footer hint is the spawn.submit chord as keys, then 'to start' (Ctrl ↵ off the Mac, ⌘ ↵ on it)", () => {
  assert.deepStrictEqual(spawnFootHint(false), { keys: ["Ctrl", "↵"], text: "to start" });
  assert.deepStrictEqual(spawnFootHint(true), { keys: ["⌘", "↵"], text: "to start" });
});

test("the label and the hint are set from the helpers on open and after a failed spawn", () => {
  const app = read("app.js");
  const chrome = body(app, "paintSpawnChrome");
  assert.match(chrome, /spawnSubmitLabel\(/);
  assert.match(chrome, /spawnFootHint\(/);
  assert.match(chrome, /drawerDesktop\(\)/, "the layout decides, not the platform");
  // The phone keeps its text hint and its static markup (the sha gate): the desktop branch only.
  assert.match(chrome, /to spawn/, "phone hint unchanged");
  assert.match(body(app, "openSpawnDialog"), /paintSpawnChrome\(/, "set on open");
  assert.match(body(app, "initSpawnControl"), /paintSpawnChrome\(/, "set at boot");
  // The in-flight label is "Starting…"; the restore after the POST (failed, or closed) is the helper's, not a captured copy.
  const submit = app.slice(app.indexOf('$("spawn").addEventListener("submit"'));
  assert.match(submit, /textContent = "Starting…"/);
  assert.match(submit, /textContent = spawnSubmitLabel\(drawerDesktop\(\)\)/, "restore from the helper");
  assert.doesNotMatch(submit.slice(0, submit.indexOf("spawnBusy = false")), /goLabel/, "no captured label");
});

test("index.html keeps the phone's markup: the label stays Spawn ⏎ and the Name label stays one text node", () => {
  const html = read("index.html");
  assert.match(html, /<button type="submit" class="btn pri">Spawn ⏎<\/button>/);
  assert.match(html, /<label for="sess-name">Name \(optional\)<\/label>/);
  assert.match(body(read("app.js"), "paintSpawnChrome"), /spawn-name-row|sess-name/, "the desktop's muted (optional) is drawn at runtime");
});

// --- the card is anchored, not centred (F15) -------------------------------------------------------

test("#spawn-modal hangs min(90px, 10vh) from the top and is not centred vertically", () => {
  const r = rule("#spawn-modal");
  assert.match(r, /top:\s*min\(90px,\s*10vh\)/);
  assert.match(r, /bottom:\s*auto/);
  assert.match(r, /margin:\s*0 auto/, "centred across, not down");
  assert.doesNotMatch(r, /inset:\s*0/);
  assert.doesNotMatch(r, /margin:\s*auto(;|$)/);
  assert.match(r, /width:\s*820px/);
});

test("the body is a 240px column and a form, fixed at min(640px, ...) with a hairline above", () => {
  const r = rule("#spawn-modal .dlg-body");
  assert.match(r, /display:\s*grid/);
  assert.match(r, /grid-template-columns:\s*240px minmax\(0,\s*1fr\)/);
  assert.match(r, /border-top:\s*1px solid var\(--line\)/);
  assert.match(r, /overflow:\s*hidden/, "each column scrolls itself");
  assert.match(r, /flex:\s*none/, "the height below is the height (app.css gives .dlg-body flex: 1)");
  // 640px, and on a short window what is left under the top anchor, the head, the footer, the card's own
  // 2px of border and a 16px bottom gap (the card's max-height already leaves 16px each side).
  const h = /height:\s*(min\(640px,\s*calc\([^;]*\)\))/.exec(r);
  assert.ok(h, "height: min(640px, calc(...))");
  for (const part of ["100vh", "var(--ns-head)", "var(--ns-foot)", "min(90px, 10vh)", "var(--sp-4)"]) assert.ok(h[1].includes(part), part);
});

test("the plist and the form are the two scrolling columns; the divider is the plist's border (G19)", () => {
  const list = rule("#spawn-plist");
  assert.match(list, /border-right:\s*1px solid var\(--line\)/);
  assert.match(list, /overflow:\s*auto/);
  assert.match(list, /padding:\s*var\(--sp-3\) var\(--sp-2\)/);
  assert.match(list, /gap:\s*2px/);
  assert.doesNotMatch(list, /width:\s*236px/, "the 240px column is the grid's");
  assert.doesNotMatch(list, /contain:\s*size/, "the body's height is fixed, the list no longer has to be kept from sizing it");
  const form = rule("#spawn");
  assert.match(form, /overflow:\s*auto/);
  assert.match(form, /grid-template-columns:\s*minmax\(0,\s*1\.55fr\) minmax\(0,\s*1fr\)/);
  assert.match(form, /gap:\s*14px 16px/);
  assert.match(form, /padding:\s*14px var\(--sp-5\) var\(--sp-4\)/);
  assert.match(form, /min-height:\s*0/);
});

// --- the footer spans the card, outside the form's scroll box --------------------------------------

test("the footer is out of flow at the card's bottom edge, across both columns", () => {
  const f = rule("#spawn .spawn-actions");
  assert.match(f, /position:\s*absolute/);
  assert.match(f, /left:\s*0/);
  assert.match(f, /right:\s*0/);
  assert.match(f, /bottom:\s*0/);
  assert.doesNotMatch(f, /position:\s*sticky/);
  assert.match(f, /border-top:\s*1px solid var\(--line\)/);
  assert.match(f, /padding:\s*var\(--sp-3\) var\(--sp-5\)/);
  assert.match(f, /gap:\s*var\(--sp-2\)/);
  assert.match(f, /height:\s*var\(--ns-foot\)/);
  // Its containing block is the dialog (position: fixed): the scrolling form and the body must not be positioned,
  // or the footer would be clipped by them and scroll with the form.
  for (const sel of ["#spawn", "#spawn-modal .dlg-body", "#spawn-plist"]) assert.doesNotMatch(rule(sel), /position:/, sel);
  assert.match(read("app.css"), /#spawn-modal, #ask-modal, #analytics-modal \{\s*border: none; position: fixed;/);
  // The body leaves the footer's band free, so the form's last field is never under it.
  assert.match(rule("#spawn-modal .dlg-body"), /margin-bottom:\s*var\(--ns-foot\)/);
});

test("head 56px and footer 53px, the frame's: one custom property each, used for the height and the reserve", () => {
  const r = rule("#spawn-modal");
  assert.match(r, /--ns-head:\s*calc\(var\(--sp-4\) \+ var\(--h-ctl\) \+ var\(--sp-3\)\)/);
  assert.match(r, /--ns-foot:\s*calc\(var\(--sp-3\) \+ var\(--h-ctl\) \+ var\(--sp-3\) \+ 1px\)/);
  const t = rule("#spawn-modal .dlg-title");
  assert.match(t, /min-height:\s*var\(--ns-head\)/);
  assert.match(t, /letter-spacing:\s*0/, ".pane-head's .5px tracking is not the kit's .dlg-title");
  assert.doesNotMatch(t, /border-bottom:\s*1px/, "the divider is the body's border-top, not twice");
});

test("the close sits 16px in from the corner, level with the title text", () => {
  const head = rule("#spawn-modal .dlg-head");
  assert.match(head, /position:\s*absolute/);
  assert.match(head, /top:\s*var\(--sp-4\)/);
  assert.match(head, /right:\s*var\(--sp-4\)/);
  assert.doesNotMatch(head, /transform/);
});

test("the footer's buttons: Cancel is the secondary .btn, Start session the primary, 28px, 12px padding", () => {
  assert.match(rule("#spawn .spawn-actions .btn.gh"), /background:\s*var\(--raise\)/);
  assert.match(rule("#spawn .spawn-actions .btn.gh"), /color:\s*var\(--fg\)/);
  assert.match(rule("#spawn .spawn-actions .btn.gh:hover"), /background:\s*var\(--line\)/);
  const go = rule('#spawn .spawn-actions button[type="submit"]');
  assert.match(go, /padding:\s*0 var\(--sp-3\)/);
  assert.match(go, /min-height:\s*var\(--h-ctl\)/);
  const hint = rule("#spawn-foot-hint");
  assert.match(hint, /display:\s*flex/);
  assert.match(hint, /margin-right:\s*auto/);
  assert.match(hint, /gap:\s*var\(--sp-1\)/);
});

// --- the profile list: kit rows (M4.1) -------------------------------------------------------------

test("profile rows are the kit's two lines: name --t-md 500, sub --t-sm muted; selected keeps --raise and the accent bar", () => {
  const row = rule(".sp-item");
  assert.match(row, /padding:\s*6px 8px 6px 6px/, "6px + the 2px bar = the frame's 8px inset");
  assert.match(row, /gap:\s*1px/);
  assert.match(row, /border-left:\s*2px solid transparent/);
  assert.match(row, /border-radius:\s*var\(--r-chip\)/);
  const name = rule(".sp-name");
  assert.match(name, /font-size:\s*var\(--t-md\)/);
  assert.match(name, /font-weight:\s*var\(--w-medium\)/);
  assert.match(name, /line-height:\s*var\(--lh-md\)/);
  const sub = rule(".sp-touch");
  assert.match(sub, /font-size:\s*var\(--t-sm\)/);
  assert.match(sub, /line-height:\s*var\(--lh-sm\)/);
  assert.match(sub, /color:\s*var\(--muted\)/);
  const sel = rule(".sp-item.sel");
  assert.match(sel, /background:\s*var\(--raise\)/);
  assert.match(sel, /border-left-color:\s*var\(--accent\)/);
  assert.match(rule(".sp-item:hover"), /background:\s*var\(--hover\)/, "hover is quieter than selected");
  const head = rule(".sp-head");
  assert.match(head, /padding:\s*var\(--sp-1\) var\(--sp-2\) 6px/);
  assert.match(head, /font-weight:\s*var\(--w-semibold\)/);
});

// --- the form's fields: kit .field-label / .input / .field-help / .seg / .elev values (M4.1, M4.2) --

test("each labelled row is a kit .field: a column with a 6px gap, and a hidden one stays hidden", () => {
  const r = rule("#spawn :is(#spawn-ws-row, #spawn-name-row, #spawn-prompt-row, #runtime-row, #effort-row, #spawn-account-row)");
  assert.match(r, /display:\s*flex/);
  assert.match(r, /flex-direction:\s*column/);
  assert.match(r, /gap:\s*6px/);
  assert.match(rule("#spawn #spawn-prompt-row[hidden]"), /display:\s*none/);
  assert.match(rule("#spawn #spawn-account-row[hidden]"), /display:\s*none/);
});

test("the grid places Workspace and Name side by side, the first message across, Effort and Containment side by side", () => {
  assert.match(rule("#spawn > #spawn-ws-row"), /grid-column:\s*auto/, "app.css's .span2 would take the whole row");
  const all = block;
  assert.doesNotMatch(all, /#spawn > #spawn-ws-row,\s*#spawn > #spawn-name-row\s*\{[^}]*span 6/);
  assert.doesNotMatch(all, /#spawn > #effort-row\s*\{[^}]*span 8/);
  assert.doesNotMatch(all, /#spawn > #runtime-row\s*\{[^}]*span 4/);
});

test("labels are the kit .field-label: 12/500 --fg; (optional) muted 400", () => {
  const l = rule("#spawn :is(#spawn-ws-row, #spawn-name-row, #spawn-prompt-row, #runtime-row, #effort-row, #spawn-account-row) > label");
  assert.match(l, /font-size:\s*var\(--t-sm\)/);
  assert.match(l, /line-height:\s*var\(--lh-sm\)/);
  assert.match(l, /font-weight:\s*var\(--w-medium\)/);
  assert.match(l, /color:\s*var\(--fg\)/);
  assert.match(l, /margin:\s*0/);
  const opt = rule("#spawn #spawn-name-row > label .opt");
  assert.match(opt, /color:\s*var\(--muted\)/);
  assert.match(opt, /font-weight:\s*var\(--w-regular\)/);
});

test("fields are the kit .input: 32px, --faint border, mono for Workspace and Model, a 52px-minimum textarea", () => {
  for (const sel of ["#spawn select", "#spawn input", "#spawn textarea"]) {
    assert.match(rule(sel), /border-color:\s*var\(--faint\)/, sel);
    assert.match(rule(sel), /border-radius:\s*var\(--r-ctl\)/, sel);
  }
  for (const sel of ["#spawn input:not([type=checkbox]):not([type=radio])", "#spawn select"]) {
    const f = rule(sel);
    assert.match(f, /height:\s*var\(--h-lg\)/, sel);
    assert.match(f, /padding:\s*0 10px/, sel);
    assert.match(f, /font-size:\s*var\(--t-md\)/, sel);
  }
  const ws = rule("#spawn #spawn-ws-input");
  assert.match(ws, /font-family:\s*var\(--font-mono\)/);
  assert.match(ws, /font-size:\s*var\(--t-sm\)/);
  assert.match(rule("#spawn input::placeholder"), /color:\s*var\(--muted\)/);
  assert.match(rule("#spawn textarea::placeholder"), /color:\s*var\(--muted\)/);
  const model = rule("#effort-slot select");
  assert.match(model, /font-family:\s*var\(--font-mono\)/);
  assert.match(model, /font-size:\s*var\(--t-sm\)/);
  assert.match(model, /width:\s*100%/);
  assert.match(model, /padding-right:\s*28px/);
  assert.match(model, /border:\s*1px solid var\(--faint\)/);
  const ta = rule("#spawn textarea");
  assert.match(ta, /min-height:\s*52px/);
  assert.match(ta, /height:\s*72px/);
  assert.match(ta, /padding:\s*8px 10px/);
});

// The focus rule must out-weigh every field rule above: the ring is the accent border, and it is only seen if it wins.
test("the field rules never out-weigh the form's focus rule (specificity)", () => {
  const focus = "#spawn :is(select, textarea, input:not([type=checkbox]):not([type=radio])):focus-visible";
  assert.ok(rulesFor(focus).length > 0, "the focus rule is still there");
  // ids in a selector: the focus rule has one (#spawn), so a field rule that sets a border and has two would hide the ring.
  const ids = (sel) => (sel.match(/#[\w-]+/g) || []).length;
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  const bad = [];
  while ((m = re.exec(block))) {
    for (const sel of splitTop(m[1])) {
      if (!/^#spawn |^#effort-slot /.test(sel) || /:focus/.test(sel) || sel.includes("::")) continue;
      if (/(^|[ ;])(border|border-color|box-shadow|outline)\s*:/.test(" " + m[2]) && ids(sel) > 1 && /select|input|textarea/.test(sel)) bad.push(sel);
    }
  }
  assert.deepStrictEqual(bad, []);
});

test(".form-hint lines are the kit .field-help: --t-sm --muted, spaced by the field's gap", () => {
  const h = rule("#spawn .form-hint");
  assert.match(h, /margin:\s*0/);
  assert.match(h, /font-size:\s*var\(--t-sm\)/);
  assert.match(h, /line-height:\s*var\(--lh-sm\)/);
  assert.match(h, /color:\s*var\(--muted\)/);
  assert.match(rule("#spawn .form-hint + .form-hint"), /margin-top:\s*-4px/);
});

test("Effort and Containment are the kit .seg; the host item is red only when pressed", () => {
  const seg = rule("#spawn .seg");
  assert.match(seg, /display:\s*inline-flex/);
  assert.match(seg, /gap:\s*2px/);
  assert.match(seg, /padding:\s*2px/);
  assert.match(seg, /border:\s*0/);
  assert.match(seg, /background:\s*var\(--bg\)/);
  assert.match(seg, /box-shadow:\s*inset 0 0 0 1px var\(--line\)/);
  const item = rule("#spawn .seg button");
  assert.match(item, /height:\s*var\(--h-sm\)/);
  assert.match(item, /padding:\s*0 10px/);
  assert.match(item, /color:\s*var\(--muted\)/);
  assert.match(item, /font-size:\s*var\(--t-sm\)/);
  assert.match(item, /font-weight:\s*var\(--w-medium\)/);
  assert.match(item, /border-radius:\s*var\(--r-chip\)/);
  assert.match(rule("#spawn .seg button.on"), /background:\s*var\(--raise\)/);
  assert.match(rule("#spawn .seg button.on"), /color:\s*var\(--fg\)/);
  const host = rule("#spawn .seg button.dgr.on");
  assert.match(host, /background:\s*var\(--red-tint\)/);
  assert.match(host, /color:\s*var\(--red\)/);
  assert.match(host, /box-shadow:\s*inset 0 0 0 1px rgba\(240,\s*106,\s*106,\s*\.45\)/);
  assert.match(rule("#spawn .seg[hidden]"), /display:\s*none/);
  assert.match(rule("#spawn #runtime-row > .seg"), /align-self:\s*flex-start/, "Containment keeps its own width");
  // Model above Effort in one column, the seg at its own width.
  const slot = rule("#effort-slot");
  assert.match(slot, /flex-direction:\s*column/);
  assert.match(slot, /gap:\s*6px/);
  assert.match(slot, /align-items:\s*flex-start/, "the seg keeps its own width");
  assert.match(rule("#effort-slot .opt:not(.opt-seg)"), /align-self:\s*stretch/, "the Model select takes the column");
});

test("the host warning is the kit .elev with a bold red 'host' before today's sentence, on the desktop only", () => {
  const w = rule("#spawn #spawn-warn-row #runtime-warn");
  assert.match(w, /display:\s*flex/);
  assert.match(w, /gap:\s*10px/);
  assert.match(w, /padding:\s*8px 12px/);
  assert.match(w, /background:\s*var\(--red-tint\)/);
  assert.match(w, /border-radius:\s*var\(--r-ctl\)/);
  assert.match(w, /border-bottom:\s*1px solid rgba\(240,\s*106,\s*106,\s*\.45\)/);
  assert.match(w, /color:\s*var\(--fg\)/);
  assert.match(w, /font-size:\s*var\(--t-sm\)/);
  assert.match(rule("#spawn #spawn-warn-row #runtime-warn[hidden]"), /display:\s*none/);
  // Only while host is the chosen tier: the box tier's "cannot be boxed" shares the element.
  const label = rule("#spawn:has(#tierpick .tp-host.on) #spawn-warn-row #runtime-warn::before");
  assert.deepStrictEqual(rulesFor("#spawn #spawn-warn-row #runtime-warn::before"), [], "never unconditional");
  assert.match(label, /content:\s*"host"/);
  assert.match(label, /color:\s*var\(--red\)/);
  assert.match(label, /font-weight:\s*var\(--w-semibold\)/);
  // An empty row would still take a 14px grid gap.
  assert.match(rule("#spawn-warn-row:not(:has(> #runtime-warn:not([hidden])))"), /display:\s*none/);
  // The phone's markup has no 'host' word: the sentence is as it was.
  assert.match(read("index.html"), /<div class="warnbox" id="runtime-warn" hidden>Runs on your host with your user's full access: files, keys, network\.<\/div>/);
});

test("the tier cards: the seg hides on the desktop, the cards stack, a disabled card is dimmed", () => {
  assert.match(rule("#spawn #runtime-row > #runtime-seg"), /display:\s*none/);
  assert.match(rule("#spawn #runtime-row > .tierpick"), /flex-direction:\s*column/);
  assert.match(rule("#spawn #runtime-row .tp-lbl-ph"), /display:\s*none/);
  assert.match(rule("#spawn #runtime-row .tp-lbl"), /display:\s*inline/);
  assert.match(rule("#spawn .tierpick .tp.on"), /border-color:\s*var\(--accent\)/);
  assert.match(rule("#spawn .tierpick .tp.dis"), /cursor:\s*not-allowed/);
});

// --- gate fix wave (#173, DIFF G4 and G9) -----------------------------------------------------------

test("a profile name clips with an ellipsis, like the frame's .srow-name.ellipsis (G9)", () => {
  const name = rule(".sp-name");
  assert.match(name, /overflow:\s*hidden/);
  assert.match(name, /white-space:\s*nowrap/);
  assert.match(name, /text-overflow:\s*ellipsis/);
  assert.match(name, /max-width:\s*100%/, "a column flex item at flex-start is as wide as its text unless capped");
  assert.doesNotMatch(name, /overflow-wrap/, "no longer wraps");
  assert.match(rule(".sp-touch"), /overflow-wrap:\s*anywhere/, "the touches line still wraps");
});

// The frame's .m4-adv: the label then the option on ONE row, the option the kit's .check with the kit's .check-box,
// no rule above it. app.css's .spawn-adv (border-top, padding-top) and .spawn-adv-label are the phone's too, so this
// is all desktop.css.
test("Advanced is one row: the label, then the option, no rule above it (G4)", () => {
  const row = rule("#spawn > #spawn-adv");
  assert.match(row, /display:\s*flex/);
  assert.match(row, /align-items:\s*center/);
  assert.match(row, /gap:\s*var\(--sp-4\)/);
  assert.match(row, /border-top:\s*0/);
  assert.match(row, /padding-top:\s*0/);
  assert.match(row, /order:\s*9/, "it keeps its place in the form");
  const lab = rule("#spawn-adv .spawn-adv-label");
  assert.match(lab, /margin:\s*0/);
  assert.match(lab, /font-size:\s*var\(--t-xs\)/);
  assert.match(lab, /line-height:\s*var\(--lh-xs\)/);
  assert.match(lab, /font-weight:\s*var\(--w-semibold\)/);
  assert.match(lab, /letter-spacing:\s*var\(--track-caps\)/);
  assert.match(lab, /color:\s*var\(--muted\)/);
  const opt = rule("#spawn-adv .opt");
  assert.match(opt, /display:\s*flex/);
  assert.match(opt, /align-items:\s*center/);
  assert.match(opt, /gap:\s*var\(--sp-2\)/);
  assert.match(opt, /cursor:\s*default/);
  const text = rule("#spawn #spawn-adv .opt > label");
  assert.match(text, /flex:\s*none/, "the row is as wide as its words, the checkbox does not go to the right edge");
  assert.match(text, /font-size:\s*var\(--t-md\)/);
  assert.match(text, /line-height:\s*var\(--lh-md\)/);
  assert.match(rule("#spawn-adv .opt > input[type=\"checkbox\"]"), /order:\s*-1/, "the box comes first, as the frame draws it, with no markup change");
});

// The three selectors reach every checkbox in the form and out-weigh app.css's two id-weighted 16px rules
// (#options .opt input, #spawn label.spawn-check input). Same values as the kit's .check-box (desktop.css's kit section).
const SPAWN_BOXES = ["#spawn input[type=\"checkbox\"]", "#spawn-adv .opt input[type=\"checkbox\"]", "#spawn-replies-row label.spawn-check input[type=\"checkbox\"]"];
test("every checkbox in the New session form is the kit's check-box: 14px, --bg with a --muted inset ring, accent and a tick when checked (G4)", () => {
  for (const sel of SPAWN_BOXES) {
    const b = rule(sel);
    for (const re of [/appearance:\s*none/, /-webkit-appearance:\s*none/, /flex:\s*none/, /position:\s*relative/, /width:\s*14px/, /height:\s*14px/, /margin:\s*3px 0 0/,
      /border-radius:\s*var\(--r-chip\)/, /background:\s*var\(--bg\)/, /box-shadow:\s*inset 0 0 0 1px var\(--muted\)/, /cursor:\s*pointer/]) assert.match(b, re, sel + " " + re);
    const on = rule(sel + ":checked");
    assert.match(on, /background:\s*var\(--accent\)/, sel);
    assert.match(on, /box-shadow:\s*none/, sel);
    const tick = rule(sel + ":checked::after");
    assert.match(tick, /content:\s*""/, sel);
    assert.match(tick, /left:\s*4px/, sel);
    assert.match(tick, /top:\s*1px/, sel);
    assert.match(tick, /width:\s*4px/, sel);
    assert.match(tick, /height:\s*8px/, sel);
    assert.match(tick, /border:\s*solid var\(--on-fill\)/, sel);
    assert.match(tick, /border-width:\s*0 2px 2px 0/, sel);
    assert.match(tick, /transform:\s*rotate\(45deg\)/, sel);
  }
  // The keyboard ring is the global 2px one (primitives.css, desktop-focus.test.js): nothing here turns it off.
  const mine = block.match(/[^{}]*input\[type="checkbox"\][^{}]*\{[^{}]*\}/g).filter((r) => /#spawn(-adv|-replies-row| )/.test(r));
  assert.ok(mine.length > 0);
  for (const r of mine) assert.doesNotMatch(r, /outline/, r.trim());
  // The form's id-weight guard (the field focus rule) is about text fields, and these carry no border.
  const ring = SPAWN_BOXES.map((s) => rule(s)).join(" ");
  assert.doesNotMatch(ring, /(^|[ ;])border(-color)?:/);
});

test("the Advanced row's markup is untouched: the phone keeps its stacked group", () => {
  const html = read("index.html");
  assert.match(html, /<div class="span2 spawn-adv" id="spawn-adv">\s*<span class="spawn-adv-label">Advanced<\/span>\s*<div id="options"><\/div>\s*<\/div>/);
  const app = strip(read("app.css"));
  assert.match(app, /\.spawn-adv\s*\{\s*border-top:\s*1px solid var\(--border\);\s*padding-top:\s*10px;\s*\}/);
});

// --- nothing leaks to the phone --------------------------------------------------------------------

test("every rule above is inside the desktop media block; app.css's phone rules are unchanged", () => {
  assert.strictEqual(css.indexOf("@media (min-width: 701px) {"), css.search(/@media/), "one media block, first thing in the file");
  assert.ok(!/ns-foot|ns-head/.test(strip(read("app.css"))), "no desktop custom property in app.css");
});
