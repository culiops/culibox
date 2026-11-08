"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

// The desktop Settings frame (UI plan 4, #174, Task 1; frame M2, digest-design §B1): one 212px nav on every
// tab with Sign out… at its foot, no top bar, and a content column that starts 32px right of the nav and is
// at most 820px wide. Source-pinning, like settings-page.test.js: the stylesheets need a browser to draw, so
// each value is read off the rule that carries it.
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const HTML = read("index.html");
const BASE_CSS = stripComments(read("app.css"));
const DESK_CSS = stripComments(read("desktop.css"));
const TOKENS = read("tokens.js");

// parseRules: every style rule of `src` (comments stripped) as { selector, decls: Map }. A ";" or a brace inside
// quotes or parentheses is part of the value. At-rule containers (@media) are walked through.
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
    stack[stack.length - 1].decls.set(d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).replace(/\s+/g, " ").trim());
  };
  for (const ch of src) {
    if (quote) { buf += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; buf += ch; continue; }
    if (ch === "(") paren++;
    else if (ch === ")") paren--;
    if (paren === 0 && ch === "{") {
      stack.push({ selector: buf.trim().replace(/\s+/g, " "), decls: new Map() });
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
function parts(list) {
  const out = [];
  let depth = 0;
  let cur = "";
  for (const ch of list) {
    if (ch === "(" || ch === "[") depth++;
    else if (ch === ")" || ch === "]") depth--;
    if (ch === "," && depth === 0) { out.push(cur.trim()); cur = ""; } else cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

const RULES = parseRules(DESK_CSS);
// decls: the cascade of every desktop rule that lists exactly `sel` (later rules win), as one Map. Empty when none does.
function decls(sel, rules = RULES) {
  const out = new Map();
  for (const r of rules) if (parts(r.selector).includes(sel)) for (const [k, v] of r.decls) out.set(k, v);
  return out;
}
const has = (sel) => RULES.some((r) => parts(r.selector).includes(sel));

const P = "#page-host > #settings-modal.as-page";
const NAV = P + " > #settings-tabs";
const ROW = NAV + " .t";
const GROUP = NAV + " .pg-group";
const LIST = NAV + " > #pf-navlist";
const NI = P + " #pf-navlist .pfv-ni";

test("the Settings nav is 212px on every tab", () => {
  // digest-design §B1.2: `.m2-snav { width: 212px }`, the same on every M2 frame.
  const grid = decls(P);
  assert.strictEqual(grid.get("grid-template-columns"), "212px minmax(0, 1fr)");
  // Profiles used to widen a 170px nav through `.tall`; nothing but the page grid sets the width now.
  assert.deepStrictEqual(RULES.filter((r) => /\.tall\b/.test(r.selector)).map((r) => r.selector), [], "no .tall rule in desktop.css");
  const pageRule = new RegExp("^" + P.replace(/[>.]/g, "\\$&") + "(\\.[\\w-]+)*$");
  const widths = RULES.filter((r) => parts(r.selector).some((s) => pageRule.test(s)) && r.decls.has("grid-template-columns")).map((r) => r.selector);
  assert.deepStrictEqual(widths, [P], "one rule sets the Settings grid columns");
  assert.strictEqual(decls(NAV).get("width"), undefined, "the nav takes its width from the grid column");
  // The phone still uses .tall (the modal's taller box): selectSettingsTab keeps toggling it.
  assert.match(read("shell.js"), /classList\.toggle\("tall", name === "manage"\)/);
});

test("the nav is the M2 nav: panel surface, 16/8/12 padding, 1px gap, hairline on its right", () => {
  const nav = decls(NAV);
  assert.strictEqual(nav.get("display"), undefined, "a flex column from the base .tabs rule");
  assert.strictEqual(nav.get("flex-direction"), "column");
  assert.strictEqual(nav.get("padding"), "16px 8px 12px", "§B1.2 `.m2-snav` padding");
  assert.strictEqual(nav.get("gap"), "1px", "§B1.2 gap");
  assert.strictEqual(nav.get("background"), "var(--panel)", "§B1.2: the nav is --panel");
  assert.strictEqual(nav.get("border-right"), "1px solid var(--line)");
  assert.strictEqual(nav.get("overflow-y"), "auto", "a short window scrolls the nav, it never clips Sign out…");
});

test("the group labels are the kit .grp-label: 11/16, 600, uppercase, --track-caps, muted, 12px 10px 4px", () => {
  const g = decls(GROUP);
  assert.strictEqual(g.get("font-size"), "var(--t-xs)");
  assert.strictEqual(g.get("line-height"), "var(--lh-xs)");
  assert.strictEqual(g.get("font-weight"), "600");
  assert.strictEqual(g.get("text-transform"), "uppercase");
  assert.strictEqual(g.get("letter-spacing"), "var(--track-caps)");
  assert.strictEqual(g.get("color"), "var(--muted)");
  assert.strictEqual(g.get("padding"), "12px 10px 4px", "§B1.2 `.m2-navlbl`");
  assert.strictEqual(decls(GROUP + ":nth-of-type(1)").get("padding-top"), "4px", "the first label sits 4px down");
});

test("a nav row is the kit .nav-item: 30px, 0 10px, 4px radius, 13px; selected --raise 500; hover --hover", () => {
  const t = decls(ROW);
  assert.strictEqual(t.get("display"), "flex");
  assert.strictEqual(t.get("align-items"), "center");
  assert.strictEqual(t.get("gap"), "8px");
  assert.strictEqual(t.get("height"), "30px");
  assert.strictEqual(t.get("padding"), "0 10px");
  assert.strictEqual(t.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(t.get("font-size"), "var(--t-md)");
  assert.strictEqual(t.get("line-height"), "var(--lh-md)");
  assert.strictEqual(t.get("color"), "var(--fg)");
  assert.strictEqual(t.get("background"), "transparent");
  assert.strictEqual(t.get("box-shadow"), "none", "no accent bar: the base .tabs rule's inset top rule is the phone's");
  assert.strictEqual(decls(ROW + ":hover").get("background"), "var(--hover)");
  const on = decls(ROW + ".on");
  assert.strictEqual(on.get("background"), "var(--raise)");
  assert.strictEqual(on.get("font-weight"), "500");
  assert.strictEqual(on.get("color"), "var(--fg)");
  assert.strictEqual(on.get("box-shadow"), "none");
  // --r-chip is the 4px the frame draws; --hover and --raise are the two greys it names.
  const root = decls(":root");
  assert.strictEqual(root.get("--r-chip"), "4px");
  assert.strictEqual(root.get("--hover"), "#181b1f");
  assert.strictEqual(root.get("--raise"), "#1b1f24");
});

test("Profiles is selected like any other row while its profiles show (no bold-on-transparent override)", () => {
  // Plan 4 call 7 / frames §F3.3-4: the Profiles parent and the profile on screen are both --raise.
  assert.ok(!has(NAV + ' > .t[data-tab="manage"].on'), "the old transparent/600 rule is gone");
});

test("the nested profile list is the M2 list: a flex column, 1px gap, 12px in, no rule line", () => {
  const show = decls(NAV + ' > .t[data-tab="manage"].on + #pf-navlist');
  assert.strictEqual(show.get("display"), "flex");
  assert.strictEqual(show.get("flex-direction"), "column");
  const list = decls(LIST);
  assert.strictEqual(list.get("order"), "2", "right after Profiles");
  assert.strictEqual(list.get("gap"), "1px", "§B1.2 `.m2-plist`");
  assert.strictEqual(list.get("padding"), "0 0 4px 12px");
  assert.strictEqual(list.get("border-left"), undefined, "the frame draws no rule beside the list");
  assert.strictEqual(list.get("margin"), undefined);
});

test("a profile row: 30px, gap 6, name grows, a muted lock, a box chip that steps down to --panel when selected", () => {
  const row = decls(NI);
  assert.strictEqual(row.get("display"), "flex");
  assert.strictEqual(row.get("align-items"), "center");
  assert.strictEqual(row.get("gap"), "6px", "§B1.2 `.m2-pitem`");
  assert.strictEqual(row.get("height"), "30px");
  assert.strictEqual(row.get("padding"), "0 10px");
  assert.strictEqual(row.get("border-radius"), "var(--r-chip)");
  assert.match(row.get("font"), /^400 var\(--t-md\)\/var\(--lh-md\) var\(--font-ui\)$/);
  assert.strictEqual(decls(NI + ":hover").get("background"), "var(--hover)");
  const on = decls(NI + '[aria-current="true"]');
  assert.strictEqual(on.get("background"), "var(--raise)");
  assert.strictEqual(on.get("font-weight"), "500");
  const name = decls(NI + "-name");
  assert.strictEqual(name.get("flex"), "1 1 auto");
  assert.strictEqual(name.get("text-overflow"), "ellipsis");
  assert.strictEqual(name.get("min-width"), "0");
  // §C5 `.chip` + `.chip-box`: 18px, 0 6px, raise fill, muted mono 11px at 400.
  const chip = decls(NI + "-tier");
  assert.strictEqual(chip.get("height"), "18px");
  assert.strictEqual(chip.get("padding"), "0 6px");
  assert.strictEqual(chip.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(chip.get("background"), "var(--raise)");
  assert.strictEqual(chip.get("color"), "var(--muted)");
  assert.strictEqual(chip.get("font-family"), "var(--font-mono)");
  assert.strictEqual(chip.get("font-size"), "var(--t-xs)");
  assert.strictEqual(chip.get("font-weight"), "400");
  assert.strictEqual(decls(NI + '[aria-current="true"] .pfv-ni-tier').get("background"), "var(--panel)", "raise on raise is invisible");
  assert.strictEqual(decls(NI + "-host").get("box-shadow"), "inset 0 0 0 1px var(--faint)", "§C5 `.chip-host`");
  assert.strictEqual(decls(P + " #pf-navlist .pfv-lock").get("color"), "var(--muted)", "§B1.2: the lock is muted");
});

test('"+ New profile" is the kit .link-add: accent, 12/18, padding 6px 10px, no fill on hover', () => {
  const add = decls(NI + "-add");
  assert.strictEqual(add.get("color"), "var(--accent)");
  assert.strictEqual(add.get("font-size"), "var(--t-sm)");
  assert.strictEqual(add.get("line-height"), "var(--lh-sm)");
  assert.strictEqual(add.get("padding"), "6px 10px", "§B1.2 `.m2-addp`");
  assert.strictEqual(add.get("height"), "auto");
  const hover = decls(NI + "-add:hover");
  assert.strictEqual(hover.get("background"), "transparent");
  assert.strictEqual(hover.get("text-decoration"), "underline");
  assert.strictEqual(hover.get("text-underline-offset"), "2px", "§C2 `.link:hover`");
});

test("Sign out… is a neutral nav item at the nav foot, desktop only", () => {
  const tabs = HTML.slice(HTML.indexOf('id="settings-tabs"'), HTML.indexOf('<div class="dbd split" id="manage"'));
  const m = /<button\b([^>]*)\bid="set-signout"([^>]*)>([^<]*)<\/button>/.exec(tabs);
  assert.ok(m, "button#set-signout is a child of the nav");
  assert.strictEqual(m[3], "Sign out…", "U+2026, as the frame writes it (§A1)");
  const attrs = m[1] + m[2];
  assert.match(attrs, /type="button"/);
  assert.doesNotMatch(attrs, /data-tab|role=|aria-selected/, "it is not a tab: selectSettingsTab must not toggle it");
  assert.doesNotMatch(attrs, /class="[^"]*\bt\b/, "and does not wear the .t tab class");
  assert.ok(tabs.indexOf("set-signout") > tabs.indexOf('data-tab="about"'), "last in the nav, so focus order is visual order");
  // The phone never draws it: the base sheet hides it, desktop.css shows it only on the page.
  assert.ok(parseRules(BASE_CSS).some((r) => parts(r.selector).includes("#set-signout") && r.decls.get("display") === "none"), "app.css hides #set-signout");
  const so = decls(NAV + " > #set-signout");
  assert.strictEqual(so.get("display"), "flex");
  assert.strictEqual(so.get("margin-top"), "auto", "the foot: the nav is a flex column, the spacer is the free space");
  assert.ok(Number(so.get("order")) > 9, "after About, the last of the nine ordered rows");
  // Neutral: the row's own colour (--fg), not red; Sign out… only ends a login, it deletes nothing.
  const all = [...decls(NAV + " > #set-signout"), ...decls(NAV + " > #set-signout:hover"), ...decls(ROW)].map(([, v]) => v).join(" ");
  assert.doesNotMatch(all, /--red|--danger|#f06a6a/);
  // It is a row like the others: the same selector rule carries the row look.
  const rowRule = RULES.find((r) => parts(r.selector).includes(ROW) && r.decls.has("height"));
  assert.ok(rowRule && parts(rowRule.selector).includes(NAV + " > #set-signout"), "the .t row rule lists #set-signout too");
  assert.ok(parts(RULES.find((r) => parts(r.selector).includes(ROW + ":hover")).selector).includes(NAV + " > #set-signout:hover"), "and so does its hover");
});

test("tokens.js wires #set-signout to logout, which asks first", async () => {
  const els = {};
  const calls = { fetch: 0 };
  const asked = [];
  const ctx = {
    document: { readyState: "complete", addEventListener() {}, getElementById: (id) => (els[id] = els[id] || { id }) },
    window: { location: { href: "/" } },
    fetch: async () => { calls.fetch++; return {}; },
    askConfirm: async (o) => { asked.push(o); return false; },
    console,
  };
  vm.createContext(ctx);
  new vm.Script(TOKENS, { filename: "tokens.js" }).runInContext(ctx);
  assert.strictEqual(typeof els["set-signout"].onclick, "function", "the click handler is set");
  assert.strictEqual(els["set-signout"].onclick, ctx.logout, "it is logout itself: one sign-out path, not a copy");
  assert.strictEqual(els["tok-logout"].onclick, ctx.logout, "the phone's footer button keeps working");
  await els["set-signout"].onclick();
  assert.strictEqual(asked.length, 1, "the existing confirm is asked, no new one");
  assert.strictEqual(asked[0].title, "Sign out of this box");
  assert.strictEqual(calls.fetch, 0, "a cancelled confirm signs nobody out");
  assert.strictEqual(ctx.window.location.href, "/");
  ctx.askConfirm = async () => true;
  await els["set-signout"].onclick();
  assert.strictEqual(calls.fetch, 1, "confirmed: POST /logout");
  assert.strictEqual(ctx.window.location.href, "/login");
});

test("the nav's tab loop leaves #set-signout alone", () => {
  // initDialogs gives every `#settings-tabs button` a tab onclick; Sign out… is not a tab.
  assert.match(read("shell.js"), /querySelectorAll\("#settings-tabs button\[data-tab\]"\)/);
  assert.doesNotMatch(read("shell.js"), /querySelectorAll\("#settings-tabs button"\)/);
});

test("the top Settings bar is gone on desktop", () => {
  const dhd = decls(P + " > .dhd");
  assert.strictEqual(dhd.get("display"), "none");
  // Nothing else styles a bar nobody sees: the header's look and the Esc button's went with it.
  const styled = RULES.filter((r) => parts(r.selector).some((s) => s.startsWith(P) && (/\.dhd\b|#settings-esc/.test(s))) && !/\[data-close\]/.test(r.selector));
  assert.deepStrictEqual(styled.map((r) => r.selector), [P + " > .dhd"]);
  // The page grid's first row held the bar; with it gone every panel and footer still names its own row.
  assert.strictEqual(decls(NAV).get("grid-row"), "2 / 4");
  assert.strictEqual(decls(P + " > .dbd").get("grid-row"), "2");
  // The header is still in the DOM (the phone's modal draws it) and Esc still closes the page: pagehost.js, document level.
  assert.match(HTML, /<span class="grow">Settings<\/span>/);
  assert.match(read("pagehost.js"), /ev\.key !== "Escape"[\s\S]*closePage\("esc"\)/);
});

test("the content column starts 32px right of the nav and is at most 820px wide, left-aligned", () => {
  // §B1.1/§B1.5: `.m2-col` is 884px, the body pads 0 32px 32px: content x = nav + 32 .. + 820.
  const panel = decls(P + " > .dbd.pad");
  assert.strictEqual(panel.get("padding"), "0 32px 32px");
  assert.strictEqual(decls(P + " > .dbd").get("grid-column"), "2", "the panel is the column right of the nav");
  const kids = decls(P + " > .dbd.pad > *");
  assert.strictEqual(kids.get("max-width"), "820px");
  assert.ok(!/auto/.test(kids.get("margin") || ""), "not centred: the frame leaves the empty space on the right");
  assert.strictEqual(kids.get("margin-left"), undefined);
  assert.strictEqual(kids.get("margin-inline"), undefined);
  // The five panels this rule reaches are the .pad ones; Profiles (#manage) is the profile page's own column (Task 3).
  for (const id of ["settings", "tokens", "accounts", "about", "creds"]) assert.match(HTML, new RegExp('<div class="dbd pad" id="' + id + '"'), id);
  assert.match(HTML, /<div class="dbd split" id="manage"/);
});
