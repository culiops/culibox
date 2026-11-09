// The desktop primitives layer (UI plan 1, #171): the design kit's components under the kit's
// own class names, in ONE fenced section of desktop.css that Plans 2-6 port the pages onto. Read
// as text, like desktop-scope.test.js and desktop-type.test.js. It pins:
//   - every class the later plans use has a rule there, with no page prefix and no hex;
//   - one primary look (F11): the kit's filled accent, and no page rule restyles a primary;
//   - the collision guards, so a legacy element that already wears a kit name keeps its look;
//   - inputs rest on --faint and focus on the accent (G12); field labels are sentence case (F17).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const raw = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8");

const OPEN = "/* === kit primitives (2026-11, #171) === */";
const CLOSE = "/* === end kit primitives === */";

// The fence is found in the raw text, before any comment is stripped: the markers are comments.
function fence(src) {
  const a = src.indexOf(OPEN);
  const b = src.indexOf(CLOSE);
  if (a < 0 || b < a) return null;
  return { at: a, kit: stripComments(src.slice(a + OPEN.length, b)), rest: stripComments(src.slice(0, a) + src.slice(b + CLOSE.length)) };
}
const sections = () => {
  const f = fence(raw);
  assert.ok(f, "desktop.css has the fenced kit section");
  return f;
};

// parseRules: every style rule of `src` (comments stripped) as { selector, decls: [[prop, value]], at },
// empty ones included (the kit's `.chip-sso {}` is a rule: the neutral chip). At-rule containers
// (@media) are walked through; `at` is the chain of them around the rule. A ";" or a brace inside
// quotes or parentheses is part of the value.
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
    stack[stack.length - 1].decls.push([d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).replace(/\s+/g, " ").trim()]);
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
      if (done && !done.selector.startsWith("@")) rules.push(done);
    } else {
      buf += ch;
    }
  }
  return rules;
}

// parts: a selector list split on its top-level commas (not the ones inside :is() / :not()).
function parts(sel) {
  const out = [];
  let depth = 0;
  let cur = "";
  for (const ch of sel) {
    if (ch === "(") depth++;
    else if (ch === ")") depth--;
    if (ch === "," && depth === 0) { out.push(cur.trim()); cur = ""; } else cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

// compounds: one selector split on its combinators, outside parentheses and brackets.
function compounds(sel) {
  const out = [];
  let depth = 0;
  let cur = "";
  for (const ch of sel) {
    if (ch === "(" || ch === "[") depth++;
    else if (ch === ")" || ch === "]") depth--;
    if (depth === 0 && /[\s>+~]/.test(ch)) { if (cur) out.push(cur); cur = ""; } else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

const hasClass = (sel, name) => new RegExp("\\." + name.replace(/[-]/g, "\\-") + "(?![\\w-])").test(sel);
const decl = (rule, prop) => (rule.decls.find(([p]) => p === prop) || [])[1];
const ruleFor = (rules, partList) => rules.find((r) => JSON.stringify(parts(r.selector)) === JSON.stringify(partList));
const allParts = (rules) => rules.flatMap((r) => parts(r.selector));

// primaryOffenders: rules of `src` that restyle a legacy primary (.btn.pri, the kit's alias) with a
// colour of their own. A `.pri` inside :not() excludes the primary from a page rule, which is how a
// page keeps its secondary look without reaching the primary, so it does not count. A `.pri` on
// another component (the viewer toolbar's .vt-btn.pri) is not the kit's; primaryHoverGaps holds it.
const COLOUR_PROPS = new Set(["background", "background-color", "color", "border-color", "border", "filter"]);
function primaryOffenders(src) {
  const out = [];
  for (const r of parseRules(src)) {
    const hit = parts(r.selector).some((p) => compounds(p).some((c) => {
      const bare = c.replace(/:not\([^()]*\)/g, "");
      return hasClass(bare, "pri") && hasClass(bare, "btn");
    }));
    if (!hit) continue;
    for (const [p, v] of r.decls) if (COLOUR_PROPS.has(p)) out.push(r.selector + " { " + p + ": " + v + " }");
  }
  return out;
}

// unguardedBtn: page rules that paint a .btn (background or color) without excluding the primary.
// Such a rule reaches a .btn.pri as well and out-ranks the kit primary (0,2,0): the primary turns
// secondary, or, where only the fill is set, dark --on-fill text lands on a dark fill. A rule's .btn
// compound is safe when it carries :not(.pri) (pages use :where(:not(.pri)), which keeps the
// rule's weight) or a weight a primary never has (.gh, .dgr); a .pri compound is primaryOffenders'.
// What it can see: selectors that name .btn. A .btn styled only through its id (#au-more) or a type
// selector (`.x button`) is invisible to it.
const PAINT_PROPS = new Set(["background", "background-color", "color"]);
function unguardedBtn(src) {
  const out = [];
  for (const r of parseRules(src)) {
    const paint = r.decls.filter(([p]) => PAINT_PROPS.has(p));
    if (!paint.length) continue;
    for (const part of parts(r.selector)) {
      const last = compounds(part).pop();
      const bare = last.replace(/:not\([^()]*\)/g, "");
      if (!hasClass(last, "btn") || /:not\(\.pri\)/.test(last)) continue;
      if (hasClass(bare, "gh") || hasClass(bare, "dgr") || hasClass(bare, "pri")) continue;
      out.push(part + " { " + paint.map(([p, v]) => p + ": " + v).join("; ") + " }");
    }
  }
  return out;
}

// specificity of one selector, as [ids, classes, types]. :where() counts nothing; :not(), :is() and
// :has() count their most specific argument. Enough for the selectors in these sheets.
function specificity(sel) {
  const n = [0, 0, 0];
  const add = (v) => { n[0] += v[0]; n[1] += v[1]; n[2] += v[2]; };
  let s = sel.replace(/:where\((?:[^()]|\([^()]*\))*\)/g, "");
  s = s.replace(/:(?:not|is|has)\(((?:[^()]|\([^()]*\))*)\)/g, (m, args) => {
    add(parts(args.replace(/^\s*[>+~]/, "")).map(specificity).sort(cmpSpec).pop() || [0, 0, 0]);
    return "";
  });
  s = s.replace(/\[[^\]]*\]/g, () => { n[1]++; return ""; });
  s = s.replace(/::[\w-]+/g, () => { n[2]++; return ""; });
  s = s.replace(/#[\w-]+/g, () => { n[0]++; return ""; });
  s = s.replace(/\.[\w-]+/g, () => { n[1]++; return ""; });
  s = s.replace(/:[\w-]+(\([^()]*\))?/g, () => { n[1]++; return ""; });
  for (const t of s.split(/[\s>+~]+/)) if (/^[a-z][\w-]*$/i.test(t)) n[2]++;
  return n;
}
const cmpSpec = (x, y) => x[0] - y[0] || x[1] - y[1] || x[2] - y[2];

// A filled primary that is not the kit's .btn.pri: an accent fill with dark text. The scan cannot
// tell a button from a highlight, so every rule it finds is classified below, and a new one fails
// until it is: a primary must be 600 and hover to --accent-hover on desktop, like the kit's.
const filledAccent = (r) => r.decls.some(([p, v]) => p === "background" && v === "var(--accent)") &&
  r.decls.some(([p, v]) => p === "color" && /^var\(--(on-fill|bg)\)$/.test(v));
const FILLED_PRIMARIES = [
  "desktop.css .pbtn", "desktop.css .ri.add", "desktop.css #ask-modal:not(.info) #ask-ok",
  "desktop.css body > main.card button",
  "app.css #ask-ok", "app.css #spawn button[type=\"submit\"]", "app.css .vt-btn.pri",
];
const FILLED_NOT_CONTROLS = [
  "app.css .CodeMirror.cm-s-default span.cm-find-cur", "app.css .CodeMirror.cm-s-default span.cm-find-cur span", // the editor's current find match
];
const unwrap = (sel) => sel.replace(/:where\((?:[^()]|\([^()]*\))*\)/g, "").replace(/:not\([^()]*\)/g, "");
// primaryHoverGaps: the classified primaries of `sheets` ({ name: css }) that are not 600 or have no
// desktop rule (in `desk`) hovering them to --accent-hover at their weight or more; and any filled
// rule not classified yet.
function primaryHoverGaps(sheets, desk) {
  const out = [];
  const hovers = parseRules(desk).filter((r) => r.decls.some(([p, v]) => p === "background" && v === "var(--accent-hover)")).flatMap((r) => parts(r.selector));
  for (const [name, css] of Object.entries(sheets)) {
    for (const r of parseRules(css)) {
      if (/max-width:\s*700px/.test(r.at) || !filledAccent(r)) continue;
      const w = (r.decls.find(([p]) => p === "font-weight") || [])[1] || ((r.decls.find(([p]) => p === "font") || [])[1] || "").split(" ")[0];
      for (const part of parts(r.selector)) {
        const id = name + " " + part;
        if (FILLED_NOT_CONTROLS.includes(id)) continue;
        if (!FILLED_PRIMARIES.includes(id)) { out.push(id + ": unclassified accent fill with dark text"); continue; }
        if (!["600", "var(--w-semibold)"].includes(w)) out.push(id + ": weight " + (w || "unset"));
        const want = unwrap(part) + ":hover";
        const ok = hovers.some((h) => {
          const u = unwrap(h);
          return (u === want || u.endsWith(" " + want) || u.endsWith(">" + want)) && cmpSpec(specificity(h), specificity(part)) >= 0;
        });
        if (!ok) out.push(id + ": no desktop hover to --accent-hover");
      }
    }
  }
  return out;
}

test("the kit section is fenced once, right after the #169 disabled rules", () => {
  assert.strictEqual(raw.split(OPEN).length, 2, "one opening marker");
  assert.strictEqual(raw.split(CLOSE).length, 2, "one closing marker");
  const { at } = sections();
  const disabled = raw.indexOf("#ft-menu button:disabled, #ft-refresh[disabled] { color: var(--fg); }");
  const shell = raw.indexOf("header, #hint { display: none; }");
  assert.ok(disabled > 0 && disabled < at, "after the #169 disabled rules");
  assert.ok(shell > at, "before the first page rule, so every page rule further down still wins");
});

// The Produces list of the plan (Task 3): the names Task 7 and Plans 2-6 write in their markup.
const KIT_CLASSES = [
  "t-xs", "t-sm", "t-md", "t-lg", "t-xl", "t-kpi", "w-500", "w-600", "fg", "muted", "mono", "tnum", "ellipsis",
  "c-accent", "c-amber", "c-ok", "c-red", "link", "link-add", "grp-label", "is-needs",
  "field", "field-label", "field-help", "field-error", "input", "check", "check-box",
  "btn", "btn-primary", "btn-ghost", "btn-danger", "btn-yes", "btn-sm", "btn-lg", "btn-icon", "kbd-hint",
  "seg", "seg-item", "is-on", "is-danger",
  "chip", "chip-box", "chip-host", "chip-sso", "chip-env", "chip-prod", "chip-write", "chip-ok", "chip-needs", "chip-error", "chip-dot", "chip-fg",
  "dot", "dot-run", "dot-needs", "dot-idle", "dot-detached", "dot-crashed", "is-elevated", "kbd", "meter",
  "page-head", "page-head-text", "page-title", "page-desc", "page-actions", "section-title", "nav-item",
  "card", "card-head", "kpi-row", "kpi", "kpi-value", "is-bad", "kpi-label", "tbl", "num", "sub",
  "menu", "menu-item", "menu-icon", "menu-text", "menu-kbd", "menu-sep", "menu-label", "tip",
  "scrim", "dlg", "dlg-head", "dlg-title", "dlg-close", "dlg-body", "dlg-foot", "hint",
  "toast", "toast-text", "is-error", "is-warn",
  // Kit gaps two or more plans need (drawn page-local in the mockups).
  "toolbar", "input-sm", "card-outline", "card-row", "empty-state", "fold", "opt-card", "opt-card-title", "spin", "attn-chip", "banner", "banner-title", "banner-acts", "pane-bar", "pane-bar-msg",
];
const KIT_SELECTORS = [
  "select.input", "textarea.input", ".input.mono", ".input.input-sm", ".grp-label.is-needs",
  ".field-label .opt", ".btn .kbd-hint",
  ".seg > .seg-item.is-on", ".is-on.is-danger", ".dot.is-elevated", ".meter > i", ".kpi-value.is-bad",
  ".tbl .num", ".tbl td.sub", ".tbl tr.is-elevated", ".menu-item.is-danger",
  ".dlg > .dlg-head", ".dlg > .dlg-head > .dlg-title", ".dlg > .dlg-body", ".dlg > .dlg-foot", ".dlg > .dlg-foot .hint",
  ".toast.is-error", ".toast.is-warn",
];

test("every class the later plans use has a rule in the kit section", () => {
  const sel = allParts(parseRules(sections().kit));
  assert.deepStrictEqual(KIT_CLASSES.filter((c) => !sel.some((p) => hasClass(p, c))), [], "classes with no kit rule");
  assert.deepStrictEqual(KIT_SELECTORS.filter((s) => !sel.some((p) => p.includes(s))), [], "selectors with no kit rule");
});

test("no page prefix and no hex in the kit section", () => {
  const rules = parseRules(sections().kit);
  const prefixed = allParts(rules).filter((p) => /#|\.as-page(?![\w-])|\.m\d+-|\.dc-|\.ab(?![\w-])/.test(p));
  assert.deepStrictEqual(prefixed, [], "a primitive is never scoped to a page");
  const hex = [];
  const alpha = [];
  for (const r of rules) {
    for (const [p, v] of r.decls) {
      if (/#[0-9a-f]{3,8}\b/i.test(v)) hex.push(r.selector + " { " + p + ": " + v + " }");
      for (const m of v.match(/rgba?\([^)]*\)/g) || []) if (m.replace(/\s/g, "") !== "rgba(240,106,106,.45)") alpha.push(r.selector + " { " + p + ": " + v + " }");
    }
  }
  assert.deepStrictEqual(hex, [], "colours are tokens");
  assert.deepStrictEqual(alpha, [], "the one literal colour is the kit's red outline");
});

test("primary is one look: the kit's filled accent, aliased to the legacy .btn.pri", () => {
  const rules = parseRules(sections().kit);
  const pri = ruleFor(rules, [".btn-primary", ".btn.pri"]);
  assert.ok(pri, ".btn-primary, .btn.pri rule");
  assert.strictEqual(decl(pri, "background"), "var(--accent)");
  assert.strictEqual(decl(pri, "color"), "var(--on-fill)");
  assert.ok(["600", "var(--w-semibold)"].includes(decl(pri, "font-weight")), "weight 600");
  assert.strictEqual(decl(pri, "border-color"), "transparent", "the kit's border: primitives.css's accent one would ring the hover fill");
  const hover = ruleFor(rules, [".btn-primary:hover", ".btn.pri:hover"]);
  assert.ok(hover, ".btn-primary:hover, .btn.pri:hover rule");
  assert.strictEqual(decl(hover, "background"), "var(--accent-hover)");
});

test("no other primary look is left in desktop.css", () => {
  const all = stripComments(raw);
  assert.doesNotMatch(all, /#0b1220/i, "the hard-coded primary text colour");
  assert.doesNotMatch(all, /filter:\s*brightness/, "a brightened primary: the hover is --accent-hover");
  assert.deepStrictEqual(primaryOffenders(sections().rest), [], "page rules that restyle a primary");
  assert.deepStrictEqual(unguardedBtn(sections().rest), [], "page rules that paint every .btn, the primary too");
  const app = stripComments(fs.readFileSync(path.join(STATIC, "app.css"), "utf8"));
  assert.deepStrictEqual(primaryHoverGaps({ "desktop.css": sections().rest, "app.css": app }, sections().rest), [], "filled primaries outside the kit");
});

// weightOffenders: page rules that set a .btn's weight to anything but the primary's 600 without excluding the
// primary. Such a rule (0,4,0 and up) out-ranks the kit's .btn.pri weight, so the view's primary draws at
// the secondary's 500 (F11: one primary look). Excluded as unguardedBtn does: :not(.pri), .gh, .dgr, .pri.
function weightOffenders(src) {
  const out = [];
  for (const r of parseRules(src)) {
    const font = decl(r, "font");
    const m = font && /^(\d{3}|var\(--w-[a-z]+\))\s/.exec(font);
    const w = decl(r, "font-weight") || (m && m[1]);
    if (!w || w === "600" || w === "var(--w-semibold)") continue;
    for (const part of parts(r.selector)) {
      const last = compounds(part).pop();
      const bare = last.replace(/:not\([^()]*\)/g, "");
      if (!hasClass(last, "btn") || /:not\(\.pri\)/.test(last)) continue;
      if (hasClass(bare, "gh") || hasClass(bare, "dgr") || hasClass(bare, "pri")) continue;
      out.push(part + " { font-weight: " + w + " }");
    }
  }
  return out;
}

test("a primary keeps its 600 weight: no page rule weighs every .btn, the primary too", () => {
  assert.deepStrictEqual(weightOffenders(sections().rest), [], "page rules that draw the primary at a secondary's weight");
  const fx = `@media (min-width: 701px) {
    .a .btn { font-size: var(--t-md); font-weight: 500; }
    .b .btn:where(:not(.pri)) { font-weight: 500; }
    .c > .btn { font: 500 var(--t-md) var(--font-ui); }
    .d .btn.gh { font-weight: 400; }
    .e .btn { font-weight: 600; }
  }`;
  assert.deepStrictEqual(weightOffenders(fx), [".a .btn { font-weight: 500 }", ".c > .btn { font-weight: 500 }"]);
});

// "Save as host…" is the profile page's block Save (.btn .btn-primary, profilepage.js) with .pfv-btn-host added: red text on a clear
// button, never a fill, weighing 500 like a secondary. The page has no primary rule of its own any more (UI plan 4 Task 4: Save, Cancel and
// Create are the kit's .btn), so the kit's .btn-primary rules (no id: at most 0,3,0 with :hover and [aria-disabled]) are what the host rule
// must outrank, at rest and under the pointer; it carries three ids.
const HOST_REACH = new Set(["background", "background-color", "color", "font-weight", "font", "box-shadow", "border-color"]);
function hostOutranked(src, host) {
  const out = [];
  for (const r of parseRules(src)) {
    if (!r.decls.some(([p]) => HOST_REACH.has(p))) continue;
    for (const part of parts(r.selector)) {
      if (!hasClass(compounds(part).pop().replace(/:not\([^()]*\)/g, ""), "btn-primary")) continue;
      if (cmpSpec(specificity(part), specificity(host)) >= 0) out.push(part);
    }
  }
  return out;
}

test("Save as host… keeps its red, clear look: the host rule outranks every kit primary rule, and the page has no primary rule of its own", () => {
  const src = stripComments(raw);
  const rules = parseRules(src);
  const hostRule = rules.find((r) => parts(r.selector).some((p) => p.endsWith("#pfv .pfv-btn-host")));
  assert.ok(hostRule, "the host variant's rule");
  const host = parts(hostRule.selector).find((p) => p.endsWith("#pfv .pfv-btn-host"));
  assert.strictEqual(decl(hostRule, "background"), "none");
  assert.strictEqual(decl(hostRule, "color"), "var(--red)");
  assert.strictEqual(decl(hostRule, "font-weight"), "var(--w-medium)");
  assert.deepStrictEqual(hostOutranked(src, host), [], "no .btn-primary rule is as heavy as the host rule");
  assert.deepStrictEqual(rules.flatMap((r) => parts(r.selector)).filter((p) => /#pfv /.test(p) && /pfv-btn-primary/.test(p)), [], "the page restyles no primary");
  assert.deepStrictEqual(hostOutranked(".x .btn-primary:hover { background: red } #a #b #c .btn-primary:hover { background: red } .z .btn-primary .i { color: red }", host),
    ["#a #b #c .btn-primary:hover"], "fixture");
});

test("Close and Cancel are never primary: the Keyboard shortcuts dialog's Close is a secondary", () => {
  // askDialog({ info: true }) is the read-only dialog (showShortcuts); its OK button is "Close".
  const bad = parseRules(sections().rest).filter((r) =>
    parts(r.selector).some((p) => p.includes("#ask-ok") && !p.includes(":not(.info)")) &&
    /var\(--accent(-hover)?\)/.test(decl(r, "background") || ""));
  assert.deepStrictEqual(bad.map((r) => r.selector), [], "#ask-ok is filled only outside the info dialog");
  assert.match(fs.readFileSync(path.join(STATIC, "app.js"), "utf8"), /confirmLabel: "Close",\s*info: true/);
});

test("collision guards: kit containers that share a legacy name are matched by their kit child", () => {
  const rules = parseRules(sections().kit);
  // The kit menu's container is matched by its kit child: a .menu-item, or a .menu-row (an item with its own action, the tab switcher's).
  const guard = { seg: ":has(> .seg-item)", menu: ":has(> .menu-item", field: ":has(> .field-label)" };
  const loose = [];
  for (const p of allParts(rules)) {
    const last = compounds(p).pop();
    for (const [name, has] of Object.entries(guard)) if (hasClass(last, name) && !last.includes(has)) loose.push(p);
  }
  assert.deepStrictEqual(loose, [], "a legacy .seg, .menu popover or .field must not take a kit container rule");
  const seg = ruleFor(rules, [".seg:has(> .seg-item)"]);
  assert.ok(seg, ".seg:has(> .seg-item) rule");
  assert.strictEqual(decl(seg, "border"), "0", "primitives.css's .seg border does not reach a kit seg");
});

test("collision guards: .btn grows (min-height), it never clips at a fixed height", () => {
  const rules = parseRules(sections().kit);
  const btn = ruleFor(rules, [".btn"]);
  assert.ok(btn, ".btn rule");
  assert.strictEqual(decl(btn, "min-height"), "var(--h-ctl)");
  const fixed = rules.filter((r) => parts(r.selector).some((p) => hasClass(p, "btn")) && decl(r, "height") !== undefined);
  assert.deepStrictEqual(fixed.map((r) => r.selector), [], "no kit .btn rule sets height");
  assert.strictEqual(decl(ruleFor(rules, [".btn-sm"]), "min-height"), "var(--h-sm)");
  assert.strictEqual(decl(ruleFor(rules, [".btn-lg"]), "min-height"), "var(--h-lg)");
});

test("collision guards: the parts of a dialog and table sit under their kit root", () => {
  // Remove each allowed scoped form; whatever is left of a part name is unscoped.
  const scoped = [
    [/\.dlg > \.dlg-head > \.dlg-title(?![\w-])/g, /\.dlg-title(?![\w-])/],
    [/\.dlg > \.dlg-(head|body|foot)(?![\w-])/g, /\.dlg-(head|body|foot)(?![\w-])/],
    [/\.dlg > \.dlg-foot \.hint(?![\w-])/g, /\.hint(?![\w-])/],
    [/(:where\(\.dlg\)|\.dlg) \.dlg-close(?![\w-])/g, /\.dlg-close(?![\w-])/],
    [/\.tbl \.num(?![\w-])/g, /\.num(?![\w-])/],
    [/\.tbl td\.sub(?![\w-])/g, /\.sub(?![\w-])/],
  ];
  const loose = [];
  for (const p of allParts(parseRules(sections().kit))) {
    for (const [ok, bad] of scoped) if (bad.test(p.replace(/:has\([^()]*\)/g, "").replace(ok, ""))) loose.push(p);
  }
  assert.deepStrictEqual(loose, [], "the legacy dialog legend, .num, .hint and .sub keep their own look");
});

// .field-label, .field-help and .field-error are kit names no legacy markup or legacy rule wears, so nothing
// of a legacy container can collide with them: they are styled standalone. Under `.field >` only, the
// checkbox row's label (`.check > div > label.field-label`, M7.4 "Read only") drew 13px/400 and a bare
// `span.field-label` drew muted/400 (VERIFY-B 1.3 item 1). The .field CONTAINER is the other case: pfv
// builds its own .field, so only a .field with a kit child takes the kit's column (guard above).
test("collision guards: the field parts are standalone kit names, the .field container stays scoped", () => {
  const rules = parseRules(sections().kit);
  const own = (name) => rules.find((r) => parts(r.selector).includes(name));
  const label = own(".field-label");
  assert.ok(label, ".field-label has an unscoped rule");
  assert.strictEqual(decl(label, "font-size"), "var(--t-sm)");
  assert.strictEqual(decl(label, "line-height"), "var(--lh-sm)");
  assert.strictEqual(decl(label, "font-weight"), "var(--w-medium)");
  assert.strictEqual(decl(label, "color"), "var(--fg)");
  const help = own(".field-help");
  assert.ok(help, ".field-help has an unscoped rule");
  assert.strictEqual(decl(help, "font-size"), "var(--t-sm)");
  assert.strictEqual(decl(help, "line-height"), "var(--lh-sm)");
  assert.strictEqual(decl(help, "color"), "var(--muted)");
  const err = own(".field-error");
  assert.ok(err, ".field-error has an unscoped rule");
  assert.strictEqual(decl(err, "font-size"), "var(--t-sm)");
  assert.strictEqual(decl(err, "color"), "var(--red)");
  assert.ok(own(".field-label .opt"), "the optional marker inside a label");
  assert.deepStrictEqual(allParts(rules).filter((p) => /\.field\s*>\s*\.field-/.test(p)), [], "no part rule is scoped to a direct .field child");
  assert.ok(ruleFor(rules, [".field:has(> .field-label)"]), "the container is matched through its kit child");
  // Why unscoped is safe: no legacy stylesheet has a rule on these names.
  for (const f of ["app.css", "primitives.css", "tokens.css"]) {
    const css = stripComments(fs.readFileSync(path.join(STATIC, f), "utf8"));
    assert.doesNotMatch(css, /\.field-(label|help|error)(?![\w-])/, f + " has a rule on a kit field part");
  }
});

// A kit part that sits on a legacy rule's element must reset what that rule sets, or the part is not the
// frame's (VERIFY-B 1.3 items 2, 3 and 5). The legacy rules, by line in app.css: `.dlg-head` border-bottom,
// `.hint` margin-top and line-height 1.65, `.menu` position: fixed, z-index 40, display: none.
test("collision guards: a kit part resets the legacy rule it meets (dialog head, foot hint, menu popover)", () => {
  const rules = parseRules(sections().kit);
  const app = parseRules(stripComments(fs.readFileSync(path.join(STATIC, "app.css"), "utf8")));
  const legacy = (sel) => app.find((r) => !/max-width/.test(r.at) && JSON.stringify(parts(r.selector)) === JSON.stringify([sel]));
  assert.ok(decl(legacy(".dlg-head"), "border-bottom"), "the scan sees app.css's .dlg-head divider");
  assert.ok(decl(legacy(".hint"), "margin-top") && decl(legacy(".hint"), "line-height"), "the scan sees app.css's .hint margin and line-height");
  assert.strictEqual(decl(legacy(".menu"), "position"), "fixed", "the scan sees app.css's .menu popover");

  const head = ruleFor(rules, [".dlg > .dlg-head"]);
  assert.strictEqual(decl(head, "border"), "0", "the frame's dialog head has no divider");
  assert.strictEqual(decl(head, "padding"), "var(--sp-4) var(--sp-4) var(--sp-3) var(--sp-5)", "and keeps the kit's padding");
  const hint = ruleFor(rules, [".dlg > .dlg-foot .hint"]);
  assert.strictEqual(decl(hint, "margin"), "0", "the foot hint sits on the button baseline");
  assert.strictEqual(decl(hint, "line-height"), "var(--lh-sm)", "at the kit's line height, not .hint's 1.65");
  const menu = ruleFor(rules, [".menu:has(> .menu-item, > .menu-row)"]);
  assert.strictEqual(decl(menu, "position"), "static", "the primitive renders in flow, as drawn (a JS-placed popover adds its own placement)");
  assert.strictEqual(decl(menu, "z-index"), "auto");
  assert.strictEqual(decl(menu, "display"), "flex");
  // The legacy popover (children without .menu-item) is not reached: every kit rule that names .menu carries the guard.
  assert.deepStrictEqual(allParts(rules).filter((p) => hasClass(compounds(p).pop(), "menu") && !p.includes(":has(> .menu-item")), []);
});

// The row's ⋯ menu (F12, M5.1) is the kit menu placed by script. Its look is the kit's alone (a second
// copy of the surface would drift); the page rule only puts back what a script-placed popover needs,
// which the kit's in-flow `.menu:has(> .menu-item, > .menu-row)` resets (fixed, above the panes, closed until .open).
test("the session row menu is the kit menu: the kit's surface, rows of --h-menu, a page rule for placement only", () => {
  const kit = parseRules(sections().kit);
  const menu = ruleFor(kit, [".menu:has(> .menu-item, > .menu-row)"]);
  assert.strictEqual(decl(menu, "background"), "var(--panel)");
  assert.strictEqual(decl(menu, "border"), "1px solid var(--line)", "a hairline, not the legacy accent border");
  assert.strictEqual(decl(menu, "border-radius"), "var(--r-card)");
  assert.strictEqual(decl(menu, "box-shadow"), "var(--e-pop)");
  assert.strictEqual(decl(menu, "padding"), "4px");
  const item = ruleFor(kit, [".menu-item"]);
  assert.strictEqual(decl(item, "height"), "var(--h-menu)", "30px rows");
  assert.strictEqual(decl(item, "border"), "0", "no divider under each item");
  assert.strictEqual(decl(item, "font-size"), "var(--t-md)");
  assert.strictEqual(decl(ruleFor(kit, [".menu-sep"]), "margin"), "4px -4px", "a separator spans the padding");
  assert.strictEqual(decl(ruleFor(kit, [".menu-item.is-danger", ".menu-item.is-danger .menu-icon"]), "color"), "var(--red)");

  const page = parseRules(sections().rest);
  const placed = ruleFor(page, ["#session-menu:has(> .menu-item, > .menu-row)"]);
  assert.ok(placed, "a page rule places the kit menu");
  assert.strictEqual(decl(placed, "position"), "fixed", "script-placed (placeSessionMenu sets left and top)");
  assert.strictEqual(decl(placed, "z-index"), "40", "above the panes, as the legacy popover was");
  assert.strictEqual(decl(placed, "display"), "none", "closed until .open: the kit rule's display: flex would show it at once");
  assert.strictEqual(decl(ruleFor(page, ["#session-menu.open:has(> .menu-item, > .menu-row)"]), "display"), "flex");
  assert.deepStrictEqual(placed.decls.map(([p]) => p).filter((p) => /^(background|border|box-shadow|padding|color|font)/.test(p)), [],
    "the surface is the kit's: the placement rule paints nothing");
  // Only the kit-menu children are reached, and no rule on the legacy rows: the phone's .mi / .mi-row / .mi-x are
  // never drawn in this block (the tab switcher builds kit rows on desktop), so nothing styles them here.
  const inSession = page.filter((r) => parts(r.selector).some((p) => p.includes("#session-menu")));
  for (const r of inSession) for (const p of parts(r.selector)) {
    assert.ok(p.includes(":has(> .menu-item") || /^#session-menu (> \.menu-row|\.menu-item|\.menu-check)/.test(p), "unexpected #session-menu rule: " + p);
  }
  assert.deepStrictEqual(page.flatMap((r) => parts(r.selector)).filter((p) => /\.mi(-row|-x)?(?![\w-])/.test(p)), [],
    "no desktop rule on the legacy .mi rows (a .mi- class that is NOT a legacy row, such as .mi-off, is another component's)");
  // The row icons are inline SVG (not a font glyph) with no width or height of their own: 14px in the kit's 16px slot, on the kit's
  // .menu-icon so every kit menu has them (the session row's, the profile page's ⋯), drawn in the slot's colour; the tab switcher's
  // check, in its own slot, is the same size.
  for (const ico of [ruleFor(kit, [".menu-icon svg"]), ruleFor(page, ["#session-menu .menu-check svg"])]) {
    assert.ok(ico, "the icon sizing rule");
    assert.strictEqual(decl(ico, "width"), "14px");
    assert.strictEqual(decl(ico, "height"), "14px");
    assert.deepStrictEqual(ico.decls.map(([p]) => p).filter((p) => /^(color|stroke|fill)/.test(p)), [], "the icon takes the slot's colour (muted, red on End session…)");
  }
  assert.strictEqual(ruleFor(page, ["#session-menu .menu-item > .menu-icon svg"]), undefined, "no second copy on the one menu");
  // A long label never wraps out of its 30px row: it is clipped with an ellipsis.
  const text = ruleFor(page, ["#session-menu .menu-item > .menu-text"]);
  assert.strictEqual(decl(text, "white-space"), "nowrap");
  assert.strictEqual(decl(text, "text-overflow"), "ellipsis");
});

// The pane header's tab switcher (#173) is the kit menu too: a row is the tab (a kit .menu-item, taking the
// width) beside its close. The close is the kit's ghost icon button at the small size, so its 24px, --muted and
// --hover are the kit's, not a page rule's; the shown tab is bold and carries a check.
test("the tab switcher's rows: a flex row, the item takes the width, the close is a kit ghost icon button, the shown tab is semibold", () => {
  const page = parseRules(sections().rest);
  const kit = parseRules(sections().kit);
  const row = ruleFor(page, ["#session-menu > .menu-row"]);
  assert.strictEqual(decl(row, "display"), "flex");
  assert.strictEqual(decl(row, "align-items"), "center");
  const item = ruleFor(page, ["#session-menu > .menu-row > .menu-item"]);
  assert.strictEqual(decl(item, "flex"), "1 1 auto");
  assert.strictEqual(decl(item, "width"), "auto", "the kit's width: 100% would push the close out of the row");
  assert.strictEqual(decl(item, "min-width"), "0", "so a long title is clipped (.menu-text) rather than widening the row");
  assert.strictEqual(decl(ruleFor(page, ['#session-menu .menu-item[aria-current="true"]']), "font-weight"), "var(--w-semibold)");
  const check = ruleFor(page, ["#session-menu .menu-check"]);
  assert.strictEqual(decl(check, "width"), "16px", "the kit's icon slot");
  assert.strictEqual(decl(check, "color"), "var(--muted)");
  const x = ruleFor(page, ["#session-menu > .menu-row > .menu-x"]);
  assert.strictEqual(decl(x, "flex"), "none");
  assert.deepStrictEqual(x.decls.map(([p]) => p).filter((p) => /^(color|background|border|width|height|min-)/.test(p)), [],
    "size, colour and hover come from the kit's .btn-ghost.btn-icon.btn-sm");
  assert.match(fs.readFileSync(path.join(STATIC, "app.js"), "utf8"), /x\.className = "menu-x btn btn-ghost btn-icon btn-sm";/);
  assert.strictEqual(decl(ruleFor(kit, [".btn-icon.btn-sm"]), "width"), "var(--h-sm)", "24px");
  assert.strictEqual(decl(ruleFor(kit, [".btn-ghost", ".btn.gh"]), "color"), "var(--muted)");
  assert.strictEqual(decl(ruleFor(kit, [".btn-ghost:hover", ".btn.gh:hover"]), "background"), "var(--hover)");
});

test("collision guards: the legacy modifiers are aliases of the kit's", () => {
  const sel = parseRules(sections().kit).map((r) => parts(r.selector));
  const together = (a, b) => sel.some((ps) => ps.includes(a) && ps.includes(b));
  for (const [kit, legacy] of [[".btn-ghost", ".btn.gh"], [".btn-danger", ".btn.dgr"], [".dot-run", ".dot.working"], [".dot-needs", ".dot.waiting"], [".dot-crashed", ".dot.crashed"]]) {
    assert.ok(together(kit, legacy), kit + " and " + legacy + " share a rule");
  }
});

test("collision guards: no second disabled look, and a hidden kit element stays hidden", () => {
  const rules = parseRules(sections().kit);
  const dim = rules.filter((r) => /:disabled/.test(r.selector) && decl(r, "opacity") !== undefined);
  assert.deepStrictEqual(dim.map((r) => r.selector), [], "#169's rule is the one disabled look");
  // An author display beats the UA's [hidden] { display: none } (a hidden legacy .btn, #au-more,
  // would show). Every kit rule that sets a display has a [hidden] partner, except .dot: the legacy
  // dot was already inline-block, and its behaviour is not this layer's to change.
  const hidden = rules.filter((r) => parts(r.selector).every((p) => p.endsWith("[hidden]")) && decl(r, "display") === "none");
  const covered = (p) => hidden.some((h) => parts(h.selector).some((q) => {
    if (q === p + "[hidden]") return true;
    const m = q.match(/^:is\(([^()]*)\)\[hidden\]$/);
    return !!m && m[1].split(",").map((s) => s.trim()).includes(p);
  }));
  const shown = [];
  for (const r of rules) {
    const d = decl(r, "display");
    if (!d || d === "none" || d === "contents") continue;
    for (const p of parts(r.selector)) if (p !== ".dot" && !covered(p)) shown.push(p);
  }
  assert.deepStrictEqual(shown, [], "kit rules with a display and no [hidden] partner");
});

test("inputs rest on --faint and focus on the accent (G12)", () => {
  const rules = parseRules(sections().kit);
  const input = ruleFor(rules, [".input"]);
  assert.ok(input, ".input rule");
  assert.strictEqual(decl(input, "border"), "1px solid var(--faint)");
  const fld = ruleFor(rules, [".fld"]);
  assert.ok(fld, ".fld rule: the legacy field primitive");
  assert.strictEqual(decl(fld, "border-color"), "var(--faint)");
  const focus = rules.find((r) => parts(r.selector).includes(".input:focus") && parts(r.selector).includes(".fld:focus"));
  assert.ok(focus, ".input:focus, .fld:focus rule");
  assert.strictEqual(decl(focus, "border-color"), "var(--accent)");
  assert.strictEqual(decl(focus, "box-shadow"), "0 0 0 1px var(--accent)");
  assert.strictEqual(decl(focus, "outline"), "none");
});

test("field labels are sentence case; .grp-label is the only uppercase kit rule (F17)", () => {
  const rules = parseRules(sections().kit);
  const label = rules.find((r) => parts(r.selector).includes(".field-label"));
  assert.ok(label, ".field-label rule");
  assert.strictEqual(decl(label, "text-transform"), undefined);
  assert.strictEqual(decl(label, "font-size"), "var(--t-sm)");
  assert.strictEqual(decl(label, "font-weight"), "var(--w-medium)");
  assert.strictEqual(decl(label, "color"), "var(--fg)");
  const upper = rules.filter((r) => decl(r, "text-transform") === "uppercase").map((r) => r.selector);
  assert.deepStrictEqual(upper, [".grp-label"]);
});

// One disabled look on desktop (#171, "Added from the #169 box session", M7): opacity .42 and
// cursor: not-allowed, #169's rule. The bespoke looks (.ph-shell, .dock-add, .cell-sh, #ft-refresh,
// #analytics-refresh, the dialog .btn at .45/.5/.6 and cursor: default; .pfv-opt and #ft-menu muted at
// full opacity) are folded into it. A disabled rule in desktop.css keeps only what is not the look
// (text-decoration, a hover fill, filter). Where a heavier rule would give a disabled control a look of
// its own (a shared app.css rule, or a page rule's resting cursor: pointer), ONE desktop rule restates
// #169's values for exactly those selectors, at their own weight. A muted disabled colour is reset: the
// .42 already dims the control's own colours, and dimming muted text twice is a second look.
// A button that says why it cannot act (F03, #173) is aria-disabled, not :disabled: the same rule, the same weight (0,1,1).
const ONE_LOOK = ':is(button, input, select, textarea):is(:disabled, [aria-disabled="true"]):where(:not(.busy, [aria-busy="true"]))';
const NOT_BUSY = ':where(:not(.busy, [aria-busy="true"]))';
const RESTATED = [
  ".cell-sh:disabled", "#ft-refresh[disabled]", "#analytics-refresh:disabled", "#ask-ok:disabled", "#bug-modal .btn:disabled",
  "#page-host > #settings-modal.as-page #pfv .pfv-opt:disabled",
];
const MUTED_RESET = ["#ft-menu button:disabled", "#ft-refresh[disabled]"];
const bareSel = (sel) => sel.replace(/:where\((?:[^()]|\([^()]*\))*\)/g, "").replace(/:not\((?:[^()]|\([^()]*\))*\)/g, "");
// isDisabledPart: a selector that matches a closed control: :disabled, [disabled], [aria-disabled="true"] or the
// .off class, outside :not() and :where() (`:hover:not(:disabled)` is the open control).
const isDisabledPart = (p) => /:disabled|\[disabled\]|\[aria-disabled=["']?true|\.off(?![\w-])/.test(bareSel(p));
const isRestate = (r) => JSON.stringify(r.decls) === JSON.stringify([["opacity", ".42"], ["cursor", "not-allowed"]]) &&
  parts(r.selector).every((p) => p.endsWith(NOT_BUSY) && p !== ONE_LOOK);
// lookOffenders: disabled rules of `src` other than #169's and the restate rule that set opacity or cursor.
function lookOffenders(src) {
  const out = [];
  for (const r of parseRules(src)) {
    if (!parts(r.selector).some(isDisabledPart) || r.selector === ONE_LOOK || isRestate(r)) continue;
    for (const [p, v] of r.decls) if (p === "opacity" || p === "cursor") out.push(r.selector + " { " + p + ": " + v + " }");
  }
  return out;
}
// sharedLooks: rules of `app` (outside the phone block) on a disabled form control (:disabled or
// [disabled], not a busy one) that set an opacity or cursor other than the one look, or a muted colour,
// at more weight than #169's (0,1,1) where it is the look: the selectors desktop.css must answer.
function sharedLooks(app) {
  const look = [], muted = [];
  for (const r of parseRules(app)) {
    if (/max-width:\s*700px/.test(r.at)) continue;
    for (const part of parts(r.selector)) {
      if (!/:disabled|\[disabled\]/.test(bareSel(part)) || /\.busy(?![\w-])/.test(part)) continue;
      const own = r.decls.some(([p, v]) => (p === "opacity" && v !== ".42") || (p === "cursor" && v !== "not-allowed"));
      if (own && cmpSpec(specificity(part), [0, 1, 1]) > 0) look.push(part);
      if (r.decls.some(([p, v]) => p === "color" && /^var\(--(muted|faint)\)$/.test(v))) muted.push(part);
    }
  }
  return { look, muted };
}

test("one disabled look: no other disabled rule in desktop.css sets an opacity or a cursor", () => {
  const all = stripComments(raw);
  const rules = parseRules(all);
  assert.strictEqual(rules.filter((r) => r.selector === ONE_LOOK).length, 1, "#169's rule, once");
  assert.deepStrictEqual(lookOffenders(all), [], "a disabled look of its own");
  const restate = rules.filter(isRestate);
  assert.strictEqual(restate.length, 1, "one rule restates the look");
  const restated = parts(restate[0].selector).map((p) => p.slice(0, -NOT_BUSY.length));
  assert.deepStrictEqual(restated, RESTATED);
  assert.ok(restated.includes(".cell-sh:disabled"), ".cell-sh:disabled (app.css, shared with the phone) has its desktop override");
  assert.ok(!rules.some((r) => parts(r.selector).some(isDisabledPart) && r.decls.some(([p, v]) => p === "color" && /^var\(--(muted|faint)\)$/.test(v))), "no muted disabled colour in desktop.css");
  const reset = rules.find((r) => JSON.stringify(parts(r.selector)) === JSON.stringify(MUTED_RESET));
  assert.ok(reset, "the muted disabled colours of app.css are reset on desktop");
  assert.deepStrictEqual(reset.decls, [["color", "var(--fg)"]], "to the controls' resting colour");
});

test("one disabled look: every heavier shared app.css disabled look is answered on desktop", () => {
  const app = stripComments(fs.readFileSync(path.join(STATIC, "app.css"), "utf8"));
  const { look, muted } = sharedLooks(app);
  assert.deepStrictEqual(look.filter((p) => !RESTATED.includes(p)), [], "restate these in desktop.css");
  assert.deepStrictEqual(muted.filter((p) => !MUTED_RESET.includes(p)), [], "reset these colours in desktop.css");
  assert.ok(look.includes(".cell-sh:disabled") && look.includes("#ft-refresh[disabled]"), "the scan sees the shared looks");
});

test("one disabled look: the checks find what they should (fixtures)", () => {
  const desk = `@media (min-width: 701px) {
    ${ONE_LOOK} { opacity: .42; cursor: not-allowed; }
    .a:disabled${NOT_BUSY} { opacity: .42; cursor: not-allowed; }
    .x:disabled { opacity: .45; cursor: default; }
    .y .btn.off { opacity: .5; }
    .z:hover:not(:disabled) { cursor: pointer; }
    .w:disabled { text-decoration: none; }
    .q[aria-disabled="true"] { opacity: .5; }
    .r[aria-disabled="true"]:hover { background: var(--raise); }
  }`;
  assert.deepStrictEqual(lookOffenders(desk), [".x:disabled { opacity: .45 }", ".x:disabled { cursor: default }", ".y .btn.off { opacity: .5 }", '.q[aria-disabled="true"] { opacity: .5 }']);
  const app = `.p:disabled { opacity: .45; cursor: default; }
    #q[disabled] { opacity: .5; color: var(--muted); }
    #r button:disabled { cursor: not-allowed; color: var(--muted); }
    .busy:disabled { cursor: progress; }
    .s.off { opacity: .55; }
    @media (max-width: 700px) { #t:disabled { opacity: .5; } }`;
  assert.deepStrictEqual(sharedLooks(app), { look: [".p:disabled", "#q[disabled]"], muted: ["#q[disabled]", "#r button:disabled"] });
});

test("the checks find what they should (fixtures)", () => {
  const css = `@media (min-width: 701px) {
    .x .btn.pri { background: var(--accent); }
    .y .btn.pri:hover { filter: none; }
    .z .btn:where(:not(.pri)) { background: var(--raise); color: var(--fg); }
    .w .btn.pri { padding: 0 12px; }
    .v :is(.btn.pri, .pbtn) { color: var(--bg); }
    .u .vt-btn.pri:hover { background: var(--accent-hover); }
  }`;
  assert.deepStrictEqual(primaryOffenders(css), [
    ".x .btn.pri { background: var(--accent) }",
    ".y .btn.pri:hover { filter: none }",
    ".v :is(.btn.pri, .pbtn) { color: var(--bg) }",
  ]);
  const btnCss = `@media (min-width: 701px) {
    .a .btn { background: var(--raise); }
    .b .btn:hover, .b .btn.gh { color: var(--fg); }
    .c .btn:where(:not(.pri)) { background: var(--raise); }
    .d .btn:not(.dgr) { color: var(--muted); }
    .e .btn { padding: 0 12px; }
    .f .btn .kbd-hint { color: var(--muted); }
  }`;
  assert.deepStrictEqual(unguardedBtn(btnCss), [
    ".a .btn { background: var(--raise) }",
    ".b .btn:hover { color: var(--fg) }",
    ".d .btn:not(.dgr) { color: var(--muted) }",
  ]);
  assert.deepStrictEqual(specificity("#page-host > #settings-modal.as-page > .dft .btn:where(:not(.pri))"), [2, 3, 0]);
  assert.deepStrictEqual(specificity("#spawn button[type=\"submit\"]"), [1, 1, 1]);
  assert.deepStrictEqual(specificity(".seg:has(> .seg-item) > .seg-item:hover"), [0, 4, 0]);
  const gapApp = `.vt-btn.pri { background: var(--accent); color: var(--bg); font-weight: 600; }
    .hl { background: var(--accent); color: var(--bg); }
    @media (max-width: 700px) { .phone { background: var(--accent); color: var(--bg); } }`;
  assert.deepStrictEqual(primaryHoverGaps({ "app.css": gapApp }, ".vt-btn:hover { background: var(--accent-hover); }"), [
    "app.css .vt-btn.pri: no desktop hover to --accent-hover",
    "app.css .hl: unclassified accent fill with dark text",
  ], "a hover of lower weight does not count; the phone block is not desktop");
  assert.deepStrictEqual(primaryHoverGaps({ "app.css": gapApp.split("\n")[0] }, ".viewer .vt-btn.pri:hover:not(.vt-off) { background: var(--accent-hover); }"), []);
  assert.deepStrictEqual(compounds(".seg:has(> .seg-item) > .seg-item[aria-pressed=\"true\"]"), [".seg:has(> .seg-item)", ".seg-item[aria-pressed=\"true\"]"]);
  assert.deepStrictEqual(parts(".a, :is(.b, .c) .d"), [".a", ":is(.b, .c) .d"]);
  assert.ok(hasClass(".seg-item", "seg-item") && !hasClass(".seg-item", "seg"), "class match is whole-name");
  const f = fence("a { } " + OPEN + " .k { color: red; } /* c */ " + CLOSE + " .r { color: blue; }");
  assert.strictEqual(f.kit.trim(), ".k { color: red; }");
  assert.ok(f.rest.includes(".r") && !f.rest.includes(".k"));
});
