// The desktop focus ring (UI plan 1, #171, audit F70): every control shows keyboard focus as the
// kit's 2px accent ring at a 2px offset, and every text field shows it as the kit's field focus
// (accent border + 1px accent shadow = 2px of accent) instead of the ring. Read as text, like
// desktop-scope.test.js and desktop-type.test.js: a `outline: 1px solid` copied from an old rule
// would otherwise only show as one thin ring on one page, and nobody would notice until the audit
// said "1px, no offset" again.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const rawDesktop = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8");
const desktop = stripComments(rawDesktop);
const app = stripComments(fs.readFileSync(path.join(STATIC, "app.css"), "utf8"));

// parseRules: every style rule of `src` (comments stripped) as { selector, decls: [[prop, value]], at }.
// At-rule containers (@media, @supports) are walked through; `at` is the chain of them around the rule.
// A ";" or a brace inside quotes or parentheses is part of the value (the data: URL backgrounds).
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
    stack[stack.length - 1].decls.push([d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).replace(/!important/i, "").replace(/\s+/g, " ").trim()]);
  };
  for (const ch of src) {
    if (quote) { buf += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; buf += ch; continue; }
    if (ch === "(") paren++;
    else if (ch === ")") paren--;
    if (paren === 0 && ch === "{") {
      const at = stack.filter((r) => r.selector.startsWith("@")).map((r) => r.selector).join(" ");
      stack.push({ selector: buf.trim().replace(/\s+/g, " "), decls: [], at });
      buf = "";
    } else if (paren === 0 && ch === ";") {
      flush();
    } else if (paren === 0 && ch === "}") {
      flush();
      const done = stack.pop();
      if (done && !done.selector.startsWith("@") && done.decls.length) rules.push(done);
    } else {
      buf += ch;
    }
  }
  return rules;
}

// parts: a selector list's selectors. Only a comma outside parentheses and brackets separates two,
// so ":is(#creds, #cred-drawer) .x" is one selector.
function parts(list) {
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

const decl = (rule, prop) => (rule.decls.find(([p]) => p === prop) || [])[1];
// What the desktop sees of a sheet: the rules outside a max-width media query (the <= 700px ones are mobile).
const desktopRules = (src) => parseRules(src).filter((r) => !/max-width/.test(r.at));
const D = desktopRules(desktop);
const A = desktopRules(app);
const hasPart = (rules, sel) => rules.filter((r) => parts(r.selector).includes(sel));

// ---- the rings: how wide, how far out -----------------------------------------------------------
// ringWidth: the px width of an `outline` shorthand ("1px solid var(--accent)"), or null for none /
// var(--focus) (the token is the 2px kit ring: desktop-tokens.test.js pins it).
const ringWidth = (v) => { const m = /^(\d+(?:\.\d+)?)px\b/.exec(v); return m ? parseFloat(m[1]) : null; };
// offsetPx: the number of an `outline-offset`, or null for a var() (--focus-off is 2px on desktop).
const offsetPx = (v) => { const m = /^(-?\d+(?:\.\d+)?)(?:px)?$/.exec(v); return m ? parseFloat(m[1]) : null; };

// Where a 2px/2px ring cannot be drawn, the ring stays 2px wide and moves inside the control (-2px).
// Every entry says why. Matched on one selector of a rule; a stale entry fails the test below.
const INSET = "an inset 2px ring: ";
const THIN_ALLOW = [
  [".cell-bar button:focus-visible", INSET + "the pane header scrolls (overflow-x: auto clips both axes) and its buttons sit at the bar's edge"],
  [".dock-tab:focus-visible", INSET + "the dock bar's tabs row is overflow: hidden"],
  [".dock-add:focus-visible", INSET + "the dock bar's tabs row is overflow: hidden"],
  [".dock-ic:focus-visible", INSET + "the dock bar's icon buttons sit at the bar's edge"],
  [".act-row:focus-visible", INSET + "a row spanning the activity list's full width, inside its scroll container"],
  [".vt-mi:focus-visible", INSET + "a menu row: the menu is a popover with 4px padding"],
  [".ft-node:focus-visible", INSET + "a tree row spans the full width of the #filetree scroll container, which clips an outside ring"],
  [".ft-node.ft-sel:focus-visible", INSET + "a tree row spans the full width of the #filetree scroll container, which clips an outside ring"],
  ["#page-host > #settings-modal.as-page #pf-navlist .pfv-ni:focus-visible", INSET + "a nav row of the settings page's left list"],
  ["#page-host > #settings-modal.as-page #pfv .pfv-seg button:focus-visible", INSET + "a segmented button: the group's border is the edge"],
  ["#page-host > #settings-modal.as-page #pfv .pfv-opt:focus-visible", INSET + "a menu row of the profile page's option list"],
  [".sp-item:focus-visible", INSET + "rows in a 2px-padded scroller (#sp-rows); an outset ring would be clipped"],
];
const allowed = new Set(THIN_ALLOW.map(([s]) => s));
// app.css's own inset ring, kept: it is already 2px wide (the global ring) at -2px.
const APP_INSET = [[".viewer-content:focus-visible", INSET + "the scroller is flush with its pane, whose overflow clips an outside ring (app.css says so)"]];

// thin: "selector { prop: value }" for every ring declaration narrower than 2px or closer than 2px.
function thin(rules) {
  const out = [];
  for (const r of rules) {
    for (const [p, v] of r.decls) {
      const bad = (p === "outline" && ringWidth(v) !== null && ringWidth(v) < 2) ||
        (p === "outline-offset" && offsetPx(v) !== null && offsetPx(v) < 2);
      if (bad) for (const s of parts(r.selector)) out.push(s + " { " + p + ": " + v + " }");
    }
  }
  return out;
}
const selOf = (line) => line.slice(0, line.indexOf(" { "));

test("no thin rings: 2px wide at a 2px offset, or an allowlisted inset 2px ring with a reason", () => {
  const bad = thin(D).filter((l) => !allowed.has(selOf(l)));
  assert.deepStrictEqual(bad, [], "ring rules that are not the kit's 2px/2px (delete the rule, or set outline: var(--focus); outline-offset: var(--focus-off))");
});

test("every allowlisted inset ring is a 2px ring at -2px and still names a rule", () => {
  const present = new Map();
  for (const r of D) for (const s of parts(r.selector)) present.set(s, r);
  for (const [sel, why] of THIN_ALLOW) {
    assert.ok(why.length > 20, sel + " carries a reason");
    const r = present.get(sel);
    assert.ok(r, "stale allowlist entry: " + sel);
    const w = decl(r, "outline");
    assert.ok(w === "var(--focus)" || ringWidth(w) === 2, sel + " keeps the ring 2px wide, got " + w);
    assert.strictEqual(decl(r, "outline-offset"), "-2px", sel + " is inset by exactly 2px");
  }
});

test("app.css's thin rings are overridden inside the desktop block", () => {
  // app.css is mobile's and does not change; its rules still apply on the desktop, so a thin ring there
  // needs a desktop.css rule for the same selector.
  // Only focus rules: .cell.focus is the pane's own outline (hidden on desktop by the pane ring, D26) and .drop-hot is the
  // drop target's dashed outline, neither is a control's keyboard focus.
  const mine = thin(A).filter((l) => /:focus/.test(selOf(l)));
  assert.ok(mine.length > 0, "the check finds app.css's .vt-btn ring");
  const covered = new Set(D.flatMap((r) => parts(r.selector)));
  const left = mine.filter((l) => !covered.has(selOf(l)) && !APP_INSET.some(([s]) => s === selOf(l)));
  assert.deepStrictEqual(left, [], "app.css thin rings with no desktop.css rule for the same selector");
  for (const [sel, why] of APP_INSET) assert.ok(why.length > 20 && mine.some((l) => selOf(l) === sel), "stale: " + sel);
});

// ---- outline: none needs a replacement ----------------------------------------------------------
const isNone = (rule) => rule.decls.some(([p, v]) => (p === "outline" && /^(none|0)$/.test(v)) || (p === "outline-style" && v === "none"));
const REPLACED_ELSEWHERE = [
  [".cell.focus:not(.drop-hot)", "the pane ring (.cell.focus::before, shown with 2+ panes, D26) is the replacement"],
  [".pane-view:focus", "a pane view is a tabindex=-1 container the app focuses; the pane ring (D26) marks the pane and its controls take the ring"],
  ["#page-host > #settings-modal.as-page > #cred-drawer:focus", "a dialog container focused by code (tabindex=-1) so Tab starts inside it; it is not a control and its controls take the ring"],
];

test("no outline:none without a replacement: box-shadow or border-color in the rule, or a reason", () => {
  const bad = [];
  for (const r of D) {
    if (!isNone(r)) continue;
    if (decl(r, "box-shadow") !== undefined || decl(r, "border-color") !== undefined) continue;
    for (const s of parts(r.selector)) if (!REPLACED_ELSEWHERE.some(([a]) => a === s)) bad.push(s);
  }
  assert.deepStrictEqual(bad, []);
  for (const [sel, why] of REPLACED_ELSEWHERE) {
    assert.ok(why.length > 20, sel + " carries a reason");
    assert.ok(D.some((r) => parts(r.selector).includes(sel) && isNone(r)), "stale: " + sel);
  }
});

test("the terminal and the editor are the exceptions: their focus is the pane ring (D26)", () => {
  // xterm's own sheet hides its textarea (opacity 0, off-screen) and `.xterm:focus { outline: none }`; CodeMirror 5's
  // textarea is 1px wide inside a 0px-high box with an inline outline: none. Neither is a field to the eye, so
  // field focus must not reach them (checked on the field rule below) and neither needs a rule of its own.
  const xterm = fs.readFileSync(path.join(STATIC, "xterm", "xterm.css"), "utf8");
  assert.match(xterm, /\.xterm \.xterm-helper-textarea\s*\{[^}]*opacity:\s*0/);
  assert.match(xterm, /\.xterm:focus\s*\{\s*outline:\s*none/);
});

// ---- app.css's outline:none opt-outs ------------------------------------------------------------
// A list item (not a field) takes the ring: the tree row Reveal lands on. Its desktop rule is :focus-visible
// (the ring is for the keyboard; app.css's :focus rule is what hid it).
const LIST_ITEM = { ".ft-node.ft-sel:focus": ".ft-node.ft-sel:focus-visible" };
// A field whose focus is carried by its wrapper: #pal-input has `outline: none` always, and the palette's
// :focus-within underline is the cue. Desktop makes that underline 2px.
const WRAPPED = { "#pal-input": "#pal-inputwrap:focus-within" };

test("app.css opt-outs are replaced on desktop: field focus on a field, the ring on a list item", () => {
  const optOuts = [];
  for (const r of A) if (isNone(r)) for (const s of parts(r.selector)) optOuts.push(s);
  const fields = optOuts.filter((s) => /:focus$/.test(s) && !LIST_ITEM[s]);
  assert.ok(fields.length >= 5, "the check finds the field opt-outs, got " + fields.join(", "));
  for (const s of fields) {
    const mine = hasPart(D, s);
    assert.ok(mine.some((r) => decl(r, "box-shadow") === "0 0 0 1px var(--accent)"), s + ": desktop.css sets box-shadow: 0 0 0 1px var(--accent)");
  }
  for (const s of optOuts.filter((s) => LIST_ITEM[s])) {
    const ring = hasPart(D, LIST_ITEM[s]);
    assert.ok(ring.some((r) => decl(r, "outline") === "var(--focus)" && decl(r, "outline-offset") === "-2px"), s + ": desktop.css gives the row an inset ring");
  }
  for (const s of optOuts.filter((s) => WRAPPED[s])) {
    const wrap = hasPart(D, WRAPPED[s]);
    assert.ok(wrap.some((r) => decl(r, "border-bottom-color") === "var(--accent)" && decl(r, "box-shadow") === "inset 0 -1px 0 var(--accent)"), s + ": the wrapper's underline is 2px of accent (border + 1px inset shadow)");
    assert.ok(hasPart(D, s + ":focus-visible").some((r) => decl(r, "box-shadow") === "none"), s + ": the field draws no box of its own, the wrapper does");
  }
  const unhandled = optOuts.filter((s) => /:focus$/.test(s) ? false : !WRAPPED[s]).filter((s) => /input/.test(s));
  assert.deepStrictEqual(unhandled, [], "an app.css input with outline: none and no replacement named in this test");
});

// ---- text fields: field focus, not the ring ------------------------------------------------------
const OPEN = "/* === kit primitives (2026-11, #171) === */";
const CLOSE = "/* === end kit primitives === */";
const kit = parseRules(stripComments(rawDesktop.slice(rawDesktop.indexOf(OPEN), rawDesktop.indexOf(CLOSE))));
const NOT_TEXT = ["checkbox", "radio", "range", "button", "submit", "reset", "color", "file"];
const fieldRule = (invalid) => kit.find((r) => parts(r.selector).some((p) => /:focus-visible/.test(p) && /textarea/.test(p) && /select/.test(p) && (invalid ? /aria-invalid/.test(p) : !/aria-invalid/.test(p))));

test("text fields get field focus on :focus-visible: outline none + accent border + 1px accent shadow", () => {
  const f = fieldRule(false);
  assert.ok(f, "the kit section has a :focus-visible rule for input, select and textarea");
  assert.strictEqual(decl(f, "outline"), "none");
  assert.strictEqual(decl(f, "border-color"), "var(--accent)");
  assert.strictEqual(decl(f, "box-shadow"), "0 0 0 1px var(--accent)");
  const sel = f.selector;
  assert.ok(!/#/.test(sel), "a primitive has no id in it");
  const notInput = /input:not\(([^()]*)\)/.exec(sel);
  assert.ok(notInput, "input:not(...) lists the types that keep the ring");
  for (const t of NOT_TEXT) assert.ok(notInput[1].split(/,\s*/).includes("[type=" + t + "]"), t + " inputs keep the ring: excluded from field focus");
  const notPane = /\):not\(([^()]*)\):focus-visible/.exec(sel);
  assert.ok(notPane, "a :not(...) of the pane ring's textareas");
  assert.ok(notPane[1].split(/,\s*/).includes(".xterm-helper-textarea"), "the terminal's textarea is the pane ring's");
  assert.ok(notPane[1].split(/,\s*/).includes(".CodeMirror textarea"), "the editor's textarea is the pane ring's");
});

test("an invalid field keeps its red border on focus and still shows 2px of accent", () => {
  const f = fieldRule(true);
  assert.ok(f, "the kit section has the invalid-and-focused rule");
  assert.strictEqual(decl(f, "border-color"), "var(--red)");
  assert.strictEqual(decl(f, "box-shadow"), "0 0 0 2px var(--accent)");
});

// An `input` selector that is a radio or a checkbox, not a text field: it keeps the ring, whatever its selector says.
const RING_INPUTS = [["#page-host > #settings-modal.as-page #pfv .pfv-card > input:focus-visible", "the radio inside a 'Where it runs' card (index.html: type=radio)"]];

test("no rule gives a text field the ring and field focus both, and no checkbox or radio loses its ring", () => {
  const both = [];
  const ringless = [];
  const field = /(^|[\s>])(input|select|textarea)\b|\.fld\b|\.input\b|-ctl\b|-search\b|-input\b|storepass|an-date|vf-input|ren-input/;
  for (const r of D) {
    const outline = decl(r, "outline");
    for (const s of parts(r.selector)) {
      const subject = s.split(/[\s>+~]+/).pop();
      if (/:focus(-visible)?$/.test(s) && field.test(subject) && !/\[type=(checkbox|radio)\]/.test(s) && !RING_INPUTS.some(([x]) => x === s) && outline !== undefined && outline !== "none") both.push(s);
      // Not counting a :not([type=checkbox], ...): that is the exclusion, the opposite of a rule that targets them.
      if (/\[type=(checkbox|radio|button|submit)\]/.test(s.replace(/:not\([^()]*\)/g, "")) && /:focus/.test(s) && isNone(r)) ringless.push(s);
    }
  }
  assert.deepStrictEqual(both, [], "a text field's rule that sets a ring");
  assert.deepStrictEqual(ringless, []);
});

// The field focus has to reach a field whatever its page rule does. The global rule is weighted above every
// page rule that has no id in it; the page rules that DO have an id (and so set a border colour the global
// rule cannot beat) get a focus rule of their own, listed here with the field they are for.
const PAGE_FIELDS = [
  ["#drawer-goto-in:focus", "the Files drawer's go-to-file field"],
  ["#sp-search:focus-visible", "the spawn dialog's profile search"],
  ["#spawn :is(select, textarea, input:not([type=checkbox]):not([type=radio])):focus-visible", "every field of the spawn dialog (#spawn input/select/textarea set an id-weighted border; #effort-slot's select is inside it)"],
  ["dialog:not(.as-page):not([box-]):not(.sheet-dialog) .fld:focus-visible", "the fields of every plain dialog (their .fld border is 0,4,1)"],
  ["#page-host > #analytics-modal.as-page .an-date:focus", "the analytics date pickers"],
  ["#ask-modal #ask-input:focus-visible", "the ask dialog's reply field"],
  ["#store-banner .storepass:focus-visible", "the store banner's password field"],
  ["body > main.card input[type=password]:focus-visible", "the login page's password field"],
  ["#page-host > #audit-page.as-page #au-session:focus-visible", "the audit page's session select"],
  ["#page-host > #settings-modal.as-page > .dft .fld:focus-visible", "the settings dialogs' footer fields"],
  ["#page-host > #settings-modal.as-page > #cred-drawer > #cred-drawer-body .fld:focus-visible", "the credential drawer's fields"],
  ["#page-host > #settings-modal.as-page #pfv .pfv-ctl:focus-visible", "the profile page's fields"],
];

test("fields whose page rule sets an id-weighted border get a field focus of their own (>= 2px of accent)", () => {
  for (const [sel, who] of PAGE_FIELDS) {
    const rules = hasPart(D, sel);
    assert.ok(rules.length > 0, who + ": no rule for " + sel);
    const ok = rules.some((r) => decl(r, "outline") === "none" && decl(r, "border-color") === "var(--accent)" && decl(r, "box-shadow") === "0 0 0 1px var(--accent)");
    assert.ok(ok, who + ": " + sel + " sets outline none, an accent border and a 1px accent shadow");
  }
  const bad = hasPart(D, "#page-host > #settings-modal.as-page #pfv .pfv-ctl.pfv-bad:focus-visible");
  assert.ok(bad.some((r) => decl(r, "border-color") === "var(--red)" && decl(r, "box-shadow") === "0 0 0 2px var(--accent)"), "a refused profile field keeps its red border under a 2px accent shadow");
  // The viewer's find field turns its border red when nothing matches (app.css, 0,3,0), and the user is typing in it: the red stays.
  const none = hasPart(D, ".vf-bar.vf-none .vf-input:focus-visible");
  assert.ok(none.some((r) => decl(r, "border-color") === "var(--danger)" && decl(r, "box-shadow") === "0 0 0 2px var(--accent)"), "the find field keeps its no-match red under 2px of accent");
  const f = fieldRule(false);
  const mine = parts(f.selector).map(specificity).sort((a, b) => heavier(b, a))[0];
  assert.ok(heavier(specificity(".vf-bar.vf-none .vf-input:focus-visible"), mine) > 0, "and out-ranks the kit's field focus rule");
});

// ---- the field rule out-ranks every page rule without an id ---------------------------------------
// specificity: [ids, classes + attributes + pseudo-classes, types] of one selector. :not / :is / :has take the
// weight of their heaviest argument, :where() none (CSS Selectors 4). Pseudo-elements count as types.
function specificity(sel) {
  const n = [0, 0, 0];
  let i = 0;
  while (i < sel.length) {
    const c = sel[i];
    if (c === "#") { n[0]++; i++; while (/[\w-]/.test(sel[i] || "")) i++; }
    else if (c === ".") { n[1]++; i++; while (/[\w-]/.test(sel[i] || "")) i++; }
    else if (c === "[") { n[1]++; i = sel.indexOf("]", i) + 1; }
    else if (c === ":" && sel[i + 1] === ":") { n[2]++; i += 2; while (/[\w-]/.test(sel[i] || "")) i++; }
    else if (c === ":") {
      let j = i + 1;
      while (/[\w-]/.test(sel[j] || "")) j++;
      const name = sel.slice(i + 1, j);
      if (sel[j] === "(") {
        let depth = 1, k = j + 1;
        while (depth && k < sel.length) { if (sel[k] === "(") depth++; else if (sel[k] === ")") depth--; k++; }
        const arg = sel.slice(j + 1, k - 1);
        if (/^(not|is|has|matches)$/.test(name)) {
          const heavy = parts(arg).map(specificity).sort((a, b) => b[0] - a[0] || b[1] - a[1] || b[2] - a[2])[0];
          for (let x = 0; x < 3; x++) n[x] += heavy[x];
        } else if (name !== "where") n[1]++;
        i = k;
      } else { n[1]++; i = j; }
    }
    else if (/[a-z*]/i.test(c)) { if (c !== "*") n[2]++; i++; while (/[\w-]/.test(sel[i] || "")) i++; }
    else i++;
  }
  return n;
}
const heavier = (a, b) => (a[0] - b[0]) || (a[1] - b[1]) || (a[2] - b[2]);

test("a field rule without an id that is as heavy as the field focus rule has a :focus-visible rule of its own", () => {
  const f = fieldRule(false);
  assert.ok(f);
  const mine = parts(f.selector).map(specificity).sort((a, b) => heavier(b, a))[0];
  assert.strictEqual(mine[0], 0, "no id in the primitive");
  const focusRules = new Set(D.flatMap((r) => (decl(r, "border-color") === "var(--accent)" ? parts(r.selector) : [])));
  const subject = /(^|[\s>])(input|select|textarea)\b|\.fld\b|\.input\b/;
  const beat = [];
  for (const r of [...A, ...D]) {
    if (r === f) continue;
    if (!r.decls.some(([p]) => p === "border" || p === "border-color")) continue;
    for (const s of parts(r.selector)) {
      if (/:hover|:disabled|:focus/.test(s) || !subject.test(s.split(/[\s>+~]+/).pop())) continue;
      const sp = specificity(s);
      if (sp[0] === 0 && heavier(sp, mine) >= 0 && !focusRules.has(s + ":focus-visible")) beat.push(s + " (" + sp.join(",") + ")");
    }
  }
  assert.deepStrictEqual(beat, [], "page rules at or above the field focus rule's weight (" + mine.join(",") + ") and no focus rule beside them");
  assert.ok(specificity("dialog:not(.as-page):not([box-]):not(.sheet-dialog) .fld").join() === "0,4,1", "the specificity helper agrees with the browser on a known selector");
});
