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

// ---------------------------------------------------------------------------------------------------------------
// Task 2 (#174): every Settings page but Profiles has a page head (H1, one line, its one action, Esc ✕) built from
// a pure spec in settingshead.js; the add footers are gone and the save footers are the kit footer.
// ---------------------------------------------------------------------------------------------------------------

// A minimal fake DOM (the repo has no jsdom, as profilepage.test.js and credentials-dom.test.js say): just what
// settingshead.js and the two functions that feed it read and write.
class FText { constructor(t) { this.data = String(t); this.parent = null; this.nodeType = 3; } get textContent() { return this.data; } }
class FNode {
  constructor(tag) {
    this.tag = tag; this.nodeType = 1; this.children = []; this.parent = null; this.attrs = {}; this.own = "";
    this.onclick = null; this.type = ""; this.id = ""; this.hidden = false; this.className = ""; this.dataset = {};
    const self = this;
    this.classList = {
      contains: (c) => self.className.split(/\s+/).includes(c),
      add: (c) => { if (!self.classList.contains(c)) self.className = (self.className + " " + c).trim(); },
      remove: (c) => { self.className = self.className.split(/\s+/).filter((x) => x && x !== c).join(" "); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); if (k === "id") this.id = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  hasAttribute(k) { return k in this.attrs; }
  appendChild(c) {
    if (typeof c === "string") c = new FText(c);
    if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1);
    c.parent = this; this.children.push(c); return c;
  }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  after(n) { const i = this.parent.children.indexOf(this); n.parent = this.parent; this.parent.children.splice(i + 1, 0, n); }
  before(n) { const i = this.parent.children.indexOf(this); n.parent = this.parent; this.parent.children.splice(i, 0, n); }
  focus() { FNode.focused = this; }
  all(pred, out = []) { for (const c of this.children) { if (c.nodeType !== 1) continue; if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  // Only ".class" selectors: all the page scripts ask for here.
  querySelector(sel) { const cls = sel.replace(/^\./, ""); return this.all((n) => n.className.split(/\s+/).includes(cls))[0] || null; }
  click() { return this.onclick ? this.onclick({ target: this }) : undefined; }
}
const kids = (n) => n.children.filter((c) => c.nodeType === 1);
const withCls = (root, cls) => root.all((n) => n.className.split(/\s+/).includes(cls));
const classesOf = (n) => (n.className || "").split(/\s+/).filter(Boolean);

// The static paragraph of the Agent accounts panel, as index.html carries it (whitespace and all).
const ACCOUNTS_P = /<div class="dbd pad" id="accounts"[\s\S]*?<p class="hint">([\s\S]*?)<\/p>/.exec(HTML)[1];
const ACCOUNTS_LEAD = "Each agent account is a separate Claude login with its own history and quota.";
const ACCOUNTS_REST = "You choose the account when you start a session. CuliBox never switches accounts for you. On the host tier this keeps config apart but is not a security boundary; use a boxed profile when the accounts must not see each other.";

// A Settings dialog with the six panels (a .set-head slot first in each but Profiles), the About version line, the
// Agent accounts paragraph and the hidden #fs-reset, and settingshead.js loaded beside it in a vm.
function makeDom({ page = true, version = "CuliBox v0.24.2 · commit abc1234", attach } = {}) {
  const el = (tag, id, cls) => { const n = new FNode(tag); if (id) n.id = id; if (cls) n.className = cls; return n; };
  const root = el("body");
  const dlg = root.appendChild(el("dialog", "settings-modal", page ? "as-page" : ""));
  const panels = {};
  for (const id of ["manage", "creds", "settings", "tokens", "accounts", "about"]) {
    panels[id] = dlg.appendChild(el("div", id, "dbd"));
    if (id !== "manage") panels[id].appendChild(el("div", null, "set-head"));
  }
  const hint = panels.accounts.appendChild(el("p", null, "hint"));
  hint.textContent = ACCOUNTS_P;
  const ver = panels.about.appendChild(el("p", "about-version"));
  ver.textContent = version;
  const reset = dlg.appendChild(el("button", "fs-reset"));
  const calls = [];
  reset.onclick = () => calls.push("fs-reset");
  const doc = {
    getElementById: (id) => (root.id === id ? root : root.all((n) => n.id === id)[0] || null),
    createElement: (t) => new FNode(t),
    createTextNode: (t) => new FText(t),
  };
  const ctx = {
    document: doc, console,
    acctNew: () => calls.push("acctNew"),
    createToken: () => calls.push("createToken"),
    openBugReport: () => calls.push("openBugReport"),
    closeDialogFromUI: (id) => calls.push("close:" + id),
  };
  if (attach) ctx.credAddMenuAttach = attach;
  vm.createContext(ctx);
  new vm.Script(read("settingshead.js"), { filename: "settingshead.js" }).runInContext(ctx);
  // accounts.js is a whole page script; the one function the head calls is lifted out of it (as appFn does for app.js).
  const acct = read("accounts.js");
  const at = acct.indexOf("function acctDeskNote(");
  if (at >= 0) new vm.Script(acct.slice(at, acct.indexOf("\n}\n", at) + 2), { filename: "accounts.js#acctDeskNote" }).runInContext(ctx);
  return { ctx, dlg, panels, calls, hint, ver, reset, doc, slot: (tab) => panels[tab].children[0] };
}

const HEAD_TABS = ["creds", "tokens", "settings", "accounts", "about"];
const INFO = { version: "CuliBox v0.24.2 · commit abc1234", accountsLead: ACCOUNTS_LEAD };

test("settingsHeadSpec carries the approved copy", () => {
  // digest-design §A6-A8 (frames M2.3-M2.5) and the founder's answers for Agent accounts and About (2026-11-08).
  const { settingsHeadSpec } = require("../static/settingshead.js");
  assert.deepStrictEqual(settingsHeadSpec("creds", INFO), {
    title: "Credentials",
    desc: "Cloud accounts and tokens a boxed profile can use. Profiles pick from this list.",
    action: { id: "set-act-creds", label: "Add credential", menu: true },
  });
  assert.deepStrictEqual(settingsHeadSpec("tokens", INFO), {
    title: "API tokens",
    desc: "Let a client app or CLI connect with Authorization: Bearer … and no password. One per device.",
    mono: ["Authorization: Bearer …"],
    action: { id: "set-act-tokens", label: "New token" },
  });
  assert.deepStrictEqual(settingsHeadSpec("settings", INFO), {
    title: "Appearance",
    desc: "Text sizes and alerts for this browser. Sizes apply as you change them.",
    action: { id: "set-act-settings", label: "Reset to defaults" },
  });
  assert.deepStrictEqual(settingsHeadSpec("accounts", INFO), {
    title: "Agent accounts", desc: ACCOUNTS_LEAD, action: { id: "set-act-accounts", label: "Add account" },
  });
  assert.deepStrictEqual(settingsHeadSpec("about", INFO), {
    title: "About", desc: INFO.version, action: { id: "set-act-about", label: "Report a problem" },
  });
  // U+2026 and nothing else for the ellipsis the frame draws.
  assert.ok(settingsHeadSpec("tokens", INFO).desc.includes("Bearer … and"));
  // The two descriptions that come from the page read what they are given, and are empty without it.
  assert.strictEqual(settingsHeadSpec("about", {}).desc, "");
  assert.strictEqual(settingsHeadSpec("accounts", {}).desc, "");
  assert.strictEqual(settingsHeadSpec("about", { version: "CuliBox (build unknown)" }).desc, "CuliBox (build unknown)");
});

test("settingsHeadSpec is null for Profiles (its head is the profile page's) and for any unknown tab", () => {
  const { settingsHeadSpec } = require("../static/settingshead.js");
  assert.strictEqual(settingsHeadSpec("manage", INFO), null);
  assert.strictEqual(settingsHeadSpec("nope", INFO), null);
  assert.strictEqual(settingsHeadSpec(undefined, INFO), null);
  // Every spec action id is set-act-<tab>, the id the DOM button takes.
  for (const t of HEAD_TABS) assert.strictEqual(settingsHeadSpec(t, INFO).action.id, "set-act-" + t);
  // A fresh object each call: a caller that edits one cannot change the next.
  assert.notStrictEqual(settingsHeadSpec("tokens", INFO), settingsHeadSpec("tokens", INFO));
});

test("the first sentence of the accounts hint is the lead", () => {
  const { accountsLead } = require("../static/settingshead.js");
  const got = accountsLead(ACCOUNTS_P);
  assert.strictEqual(got.lead, ACCOUNTS_LEAD, "founder answer 2026-11-08: today's sentence 1");
  assert.strictEqual(got.rest, ACCOUNTS_REST, "sentences 2-4 stay as the desktop body note");
  // Nothing is lost or reworded: lead + rest is the paragraph with its line breaks collapsed.
  assert.strictEqual(got.lead + " " + got.rest, ACCOUNTS_P.replace(/\s+/g, " ").trim());
  assert.deepStrictEqual(accountsLead("One sentence only."), { lead: "One sentence only.", rest: "" });
  assert.deepStrictEqual(accountsLead("No full stop"), { lead: "No full stop", rest: "" });
  assert.deepStrictEqual(accountsLead("  Keep ~/.claude whole.  Then more.  "), { lead: "Keep ~/.claude whole.", rest: "Then more." }, "a dot inside a word is not the end");
  assert.deepStrictEqual(accountsLead(""), { lead: "", rest: "" });
  assert.deepStrictEqual(accountsLead(undefined), { lead: "", rest: "" });
});

test("each of the five pages starts with one empty .set-head slot", () => {
  for (const id of HEAD_TABS) {
    const m = new RegExp('<div class="dbd pad" id="' + id + '"[^>]*>\\s*<div class="set-head"></div>').exec(HTML);
    assert.ok(m, "#" + id + " has the slot as its first child, empty (the phone's DOM carries nothing the head would draw)");
    assert.strictEqual((HTML.match(/class="set-head"/g) || []).length, 5, "five slots, none on Profiles");
  }
  assert.doesNotMatch(HTML.slice(HTML.indexOf('id="manage"'), HTML.indexOf('id="pfv"')), /set-head/);
});

test("settingshead.js loads after the shell helpers it uses and before app.js", () => {
  const order = [...HTML.matchAll(/<script src="\/([a-z-]+\.js)"/g)].map((m) => m[1]);
  const at = (f) => order.indexOf(f);
  assert.strictEqual((order.filter((f) => f === "settingshead.js")).length, 1);
  assert.ok(at("settingshead.js") > at("shell.js"), "after shell.js (closeDialogFromUI)");
  assert.ok(at("settingshead.js") > at("bugreport.js") && at("settingshead.js") > at("tokens.js") && at("settingshead.js") > at("accounts.js"), "after the scripts whose actions it runs");
  assert.ok(at("settingshead.js") < at("app.js"), "before app.js");
  assert.ok(at("settingshead.js") < at("profilepage.js"), "before the profile page, which reuses settingsEscButton");
});

test("paintSettingsHead draws title, description, the one action, a divider and Esc ✕, only on the page", () => {
  const { ctx, slot } = makeDom();
  for (const tab of HEAD_TABS) {
    ctx.paintSettingsHead(tab);
    const head = slot(tab);
    const spec = require("../static/settingshead.js").settingsHeadSpec(tab, INFO);
    const [text, actions] = kids(head);
    assert.deepStrictEqual(classesOf(text), ["page-head-text"], tab);
    const [h1, desc] = kids(text);
    assert.strictEqual(h1.tag, "h1", tab);
    assert.deepStrictEqual(classesOf(h1), ["page-title"]);
    assert.strictEqual(h1.textContent, spec.title);
    assert.strictEqual(desc.tag, "p");
    assert.deepStrictEqual(classesOf(desc), ["page-desc"]);
    assert.strictEqual(desc.textContent, spec.desc, tab + ": the description reads exactly as the spec says");
    assert.deepStrictEqual(classesOf(actions), ["page-actions"]);
    const [act, sep, esc] = kids(actions);
    assert.strictEqual(act.id, spec.action.id);
    assert.strictEqual(act.tag, "button");
    assert.strictEqual(act.type, "button");
    assert.strictEqual(act.textContent.replace(/▾$/, ""), spec.action.label, tab + ": the label (a menu action adds its caret)");
    assert.deepStrictEqual(classesOf(sep), ["set-sep"], "the 1x16 divider sits between the action and Esc");
    assert.strictEqual(sep.getAttribute("aria-hidden"), "true");
    assert.ok(classesOf(esc).includes("set-esc"), "Esc ✕ is last");
    assert.strictEqual(kids(actions).length, 3);
  }
  // Painting again replaces the head, it does not stack a second one.
  ctx.paintSettingsHead("tokens");
  ctx.paintSettingsHead("tokens");
  assert.strictEqual(kids(slot("tokens")).length, 2, "one text block, one actions block");
  assert.strictEqual(withCls(slot("tokens"), "page-title").length, 1);
});

test("paintSettingsHead is a no-op unless the dialog is the page (a phone's modal draws nothing new)", () => {
  const { ctx, slot, panels } = makeDom({ page: false });
  for (const tab of HEAD_TABS) { ctx.paintSettingsHead(tab); assert.strictEqual(slot(tab).children.length, 0, tab + " untouched"); }
  assert.strictEqual(ctx.document.getElementById("acct-desk-note"), null, "no desktop note either");
  // Profiles has no slot and no spec: nothing to paint, nothing to throw.
  const on = makeDom();
  assert.doesNotThrow(() => on.ctx.paintSettingsHead("manage"));
  assert.doesNotThrow(() => on.ctx.paintSettingsHead("nope"));
  assert.strictEqual(on.panels.manage.children.length, 0);
  assert.ok(panels.about, "fixture sanity");
});

test("the API tokens description sets its machine text in its own mono span", () => {
  const { ctx, slot } = makeDom();
  ctx.paintSettingsHead("tokens");
  const desc = withCls(slot("tokens"), "page-desc")[0];
  assert.deepStrictEqual(desc.children.map((c) => [c.nodeType, c.textContent]), [
    [3, "Let a client app or CLI connect with "],
    [1, "Authorization: Bearer …"],
    [3, " and no password. One per device."],
  ]);
  const mono = desc.children[1];
  assert.deepStrictEqual(classesOf(mono), ["mono", "t-sm"], "the frame's `mono t-sm` span (12/18 mono)");
  // A description with no machine text is one text node.
  ctx.paintSettingsHead("settings");
  assert.deepStrictEqual(withCls(slot("settings"), "page-desc")[0].children.map((c) => c.nodeType), [3]);
});

test("no header action is primary", () => {
  const { ctx, slot } = makeDom();
  for (const tab of HEAD_TABS) {
    ctx.paintSettingsHead(tab);
    const act = withCls(slot(tab), "page-actions")[0].children[0];
    const cls = classesOf(act);
    assert.ok(cls.includes("btn"), tab + ": a kit .btn");
    for (const bad of ["btn-primary", "pri", "gh", "dgr", "btn-danger"]) assert.ok(!cls.includes(bad), tab + " is secondary, not " + bad);
  }
  // The head's Esc is ghost and small, never a fill.
  const esc = ctx.settingsEscButton();
  assert.ok(!classesOf(esc).includes("btn-primary") && !classesOf(esc).includes("pri"));
});

test("Esc ✕ is a button named Close (Esc), not data-close", () => {
  const { ctx, calls } = makeDom();
  const b = ctx.settingsEscButton();
  assert.strictEqual(b.tag, "button");
  assert.strictEqual(b.type, "button");
  assert.deepStrictEqual(classesOf(b), ["btn", "btn-ghost", "btn-sm", "set-esc"]);
  assert.strictEqual(b.getAttribute("aria-label"), "Close (Esc)");
  assert.strictEqual(b.getAttribute("title"), "Close (Esc)");
  // Mono "Esc" (the kit's .kbd-hint) then ✕ U+2715: frame A9.
  assert.deepStrictEqual(b.children.map((c) => [c.nodeType, c.tag || "", classesOf(c).join(" "), c.textContent]), [
    [1, "span", "kbd-hint", "Esc"],
    [3, "", "", "✕"],
  ]);
  // desktop.css hides [data-close] (Done is modal chrome), so the page's own close must not carry it.
  assert.strictEqual(b.hasAttribute("data-close"), false);
  assert.deepStrictEqual(b.dataset, {});
  // It closes the way the old ✕ did: through closeDialogFromUI, so the open page's leave hook can ask first.
  b.click();
  assert.deepStrictEqual(calls, ["close:settings-modal"]);
  // Two calls, two buttons (the profile page head builds its own).
  assert.notStrictEqual(ctx.settingsEscButton(), b);
});

test("each header action runs the handler its old footer button had", () => {
  const { ctx, slot, calls } = makeDom();
  const run = (tab) => { ctx.paintSettingsHead(tab); withCls(slot(tab), "page-actions")[0].children[0].click(); };
  run("accounts");  // + new account -> acctNew
  run("tokens");    // + new token -> createToken
  run("settings");  // Reset to defaults -> the #fs-reset handler (fontPrefs.reset(); initSettings())
  run("about");     // Report a problem -> openBugReport
  assert.deepStrictEqual(calls, ["acctNew", "createToken", "fs-reset", "openBugReport"]);
  // Reset with no #fs-reset on the page (it is static, so this is a guard) does not throw.
  const bare = makeDom();
  bare.dlg.children.splice(bare.dlg.children.indexOf(bare.reset), 1);
  bare.ctx.paintSettingsHead("settings");
  assert.doesNotThrow(() => withCls(bare.slot("settings"), "page-actions")[0].children[0].click());
});

test("Add credential ▾ is a menu button that hands itself to credAddMenuAttach when Task 5's menu exists", () => {
  // Without the menu code (it lands in a later task) the trigger still draws and nothing throws.
  const none = makeDom();
  assert.doesNotThrow(() => none.ctx.paintSettingsHead("creds"));
  const attached = [];
  const { ctx, slot } = makeDom({ attach: (btn) => attached.push(btn) });
  ctx.paintSettingsHead("creds");
  const btn = withCls(slot("creds"), "page-actions")[0].children[0];
  assert.strictEqual(btn.id, "set-act-creds");
  assert.strictEqual(btn.getAttribute("aria-haspopup"), "menu");
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
  assert.deepStrictEqual(kids(btn).map((c) => [c.tag, classesOf(c).join(" "), c.getAttribute("aria-hidden"), c.textContent]), [["span", "muted", "true", "▾"]], "the caret is muted and aria-hidden (§A6)");
  assert.strictEqual(btn.textContent, "Add credential▾", "the visible text; the name is 'Add credential' because the caret is hidden");
  assert.deepStrictEqual(attached, [btn], "attached once, with the button that is on the page");
  // Every other action is a plain button: no menu semantics, nothing handed to the menu code.
  for (const tab of ["tokens", "settings", "accounts", "about"]) {
    ctx.paintSettingsHead(tab);
    const b = withCls(slot(tab), "page-actions")[0].children[0];
    assert.strictEqual(b.getAttribute("aria-haspopup"), null, tab);
    assert.strictEqual(b.getAttribute("aria-expanded"), null, tab);
  }
  assert.strictEqual(attached.length, 1);
});

test("the Agent accounts description is today's first sentence and sentences 2-4 become a desktop note", () => {
  const { ctx, slot, panels, hint } = makeDom();
  ctx.paintSettingsHead("accounts");
  assert.strictEqual(withCls(slot("accounts"), "page-desc")[0].textContent, ACCOUNTS_LEAD);
  // The note is a sibling built beside the static paragraph (the phone keeps the paragraph as written, whole).
  const note = ctx.document.getElementById("acct-desk-note");
  assert.ok(note, "the note exists once the head is painted");
  assert.strictEqual(note.tag, "p");
  assert.ok(classesOf(note).includes("hint"), "it wears the page's hint look");
  assert.strictEqual(note.textContent, ACCOUNTS_REST);
  assert.strictEqual(note.parent, panels.accounts);
  assert.strictEqual(panels.accounts.children.indexOf(note), panels.accounts.children.indexOf(hint) + 1, "right after the paragraph");
  assert.strictEqual(hint.textContent, ACCOUNTS_P, "the static paragraph is never rewritten");
  // Repainting reuses the note rather than adding one, and the head's lead is still the same sentence (the note
  // is not mistaken for the paragraph).
  ctx.paintSettingsHead("accounts");
  assert.strictEqual(withCls(panels.accounts, "hint").length, 2);
  assert.strictEqual(withCls(slot("accounts"), "page-desc")[0].textContent, ACCOUNTS_LEAD);
  assert.strictEqual(note.textContent, ACCOUNTS_REST);
});

test("acctDeskNote (accounts.js) builds or refreshes the note after the static paragraph", () => {
  assert.ok(read("accounts.js").includes("function acctDeskNote("), "accounts.js owns the note");
  const { ctx, panels, hint } = makeDom();
  assert.strictEqual(ctx.document.getElementById("acct-desk-note"), null, "none until something paints it");
  ctx.acctDeskNote("");
  assert.strictEqual(ctx.document.getElementById("acct-desk-note"), null, "and no empty note is made");
  ctx.acctDeskNote("Three sentences. In a note. Here.");
  const note = ctx.document.getElementById("acct-desk-note");
  assert.strictEqual(note.textContent, "Three sentences. In a note. Here.");
  assert.strictEqual(panels.accounts.children.indexOf(note), panels.accounts.children.indexOf(hint) + 1);
  ctx.acctDeskNote("Changed.");
  assert.strictEqual(withCls(panels.accounts, "hint").length, 2, "one note, however often it is painted");
  assert.strictEqual(note.textContent, "Changed.");
  assert.strictEqual(note.hidden, false);
  ctx.acctDeskNote("");
  assert.strictEqual(note.hidden, true, "no text, no blank line");
});

test("About's description is the live version line, repainted once /api/meta has answered", async () => {
  // loadAboutInfo fills #about-version asynchronously; the head reads that text, so it repaints when it lands.
  const el = (id) => { const n = new FNode("p"); n.id = id; return n; };
  const about = el("about-version");
  about.textContent = "CuliBox";
  const els = { "about-version": about, "about-runtime": el("about-runtime") };
  const painted = [];
  const ctx = {
    document: { readyState: "complete", addEventListener() {}, getElementById: (id) => els[id] || null },
    api: async () => ({ version: "v0.24.2", commit: "abc1234", box_runtime: "" }),
    paintSettingsHead: (tab) => painted.push([tab, about.textContent]),
    console,
  };
  vm.createContext(ctx);
  new vm.Script(read("bugreport.js"), { filename: "bugreport.js" }).runInContext(ctx);
  await ctx.loadAboutInfo();
  assert.deepStrictEqual(painted, [["about", "CuliBox v0.24.2 · commit abc1234"]], "painted after the text is set, not before");
  ctx.api = async () => { throw new Error("offline"); };
  painted.length = 0;
  await ctx.loadAboutInfo();
  assert.deepStrictEqual(painted, [["about", "CuliBox (build unknown)"]], "the failure line is a description too");
  // And the head reads what that left in #about-version.
  const dom = makeDom({ version: "CuliBox v0.24.2 · commit abc1234" });
  dom.ctx.paintSettingsHead("about");
  assert.strictEqual(withCls(dom.slot("about"), "page-desc")[0].textContent, "CuliBox v0.24.2 · commit abc1234");
  dom.ver.textContent = "CuliBox (unknown build)";
  dom.ctx.paintSettingsHead("about");
  assert.strictEqual(withCls(dom.slot("about"), "page-desc")[0].textContent, "CuliBox (unknown build)");
});

test("selectSettingsTab paints the head, and openSettings paints it again once the page is open", () => {
  const shell = read("shell.js");
  const body = (name) => { const at = shell.indexOf("function " + name + "("); return shell.slice(at, shell.indexOf("\n}\n", at) + 2); };
  assert.match(body("selectSettingsTab"), /if \(typeof paintSettingsHead === "function"\) paintSettingsHead\(name\);/);
  // selectSettingsTab runs BEFORE openDialog on the first open, when the dialog is not yet .as-page and the paint is
  // a no-op, so openSettings paints once more after the page is open (as it does pfvShow).
  const open = body("openSettings");
  const dlg = open.indexOf('openDialog("settings-modal")');
  const again = open.indexOf('paintSettingsHead(tab || "manage")');
  assert.ok(dlg > 0 && again > dlg, "openSettings repaints the head after openDialog");
  assert.match(open, /if \(typeof paintSettingsHead === "function"\) paintSettingsHead\(tab \|\| "manage"\);/);
});

// --- CSS ----------------------------------------------------------------------------------------------------

const HEAD = P + " > .dbd > .set-head";

test("base CSS hides .set-head and #set-signout (and the accounts note): the phone draws none of them", () => {
  const base = parseRules(BASE_CSS);
  for (const sel of [".set-head", "#set-signout", "#acct-desk-note"]) {
    assert.ok(base.some((r) => parts(r.selector).includes(sel) && r.decls.get("display") === "none"), "app.css hides " + sel);
  }
  // One pinned rule (settings-page.test.js) carries all the desktop-only Settings nodes.
  const rule = base.find((r) => parts(r.selector).includes(".set-head"));
  assert.deepStrictEqual(parts(rule.selector), [".pg-group", ".pf-touch", ".pf-sec", ".pf-adv", "#pf-discard", "#about-runtime", "#set-signout", ".set-head", "#acct-desk-note"]);
});

test("the head is the kit .page-head inside the column: flex, top-aligned, gap 16, padding 24px 0 20px", () => {
  // §B1.4 / §C7 `.page-head` is padding 24px 32px 20px; the column's own 32px sides (Task 1) sit outside this
  // element, so only the 24 above and the 20 below are the head's.
  const h = decls(HEAD);
  assert.strictEqual(h.get("display"), "flex");
  assert.strictEqual(h.get("align-items"), "flex-start");
  assert.strictEqual(h.get("gap"), "var(--sp-4)");
  assert.strictEqual(h.get("padding"), "var(--sp-6) 0 var(--sp-5)");
  // Not in the frame: at a narrow window the actions wrap under the title rather than overflowing the panel.
  assert.strictEqual(h.get("flex-wrap"), "wrap");
  assert.strictEqual(decls(HEAD + " .page-head-text").get("flex"), "1 1 240px");
  const root = decls(":root");
  assert.deepStrictEqual(["--sp-4", "--sp-5", "--sp-6"].map((k) => root.get(k)), ["16px", "20px", "24px"]);
  // The kit does the rest: title 18/24 600, description 13/20 muted, actions gap 8 (§C7).
  const kit = decls(".page-title");
  assert.strictEqual(kit.get("font-size"), "var(--t-xl)");
  assert.strictEqual(kit.get("font-weight"), "var(--w-semibold)");
  assert.strictEqual(decls(".page-desc").get("color"), "var(--muted)");
  assert.strictEqual(decls(".page-actions").get("gap"), "var(--sp-2)");
  assert.strictEqual(decls(".page-head-text").get("min-width"), "0", "the title can shrink and truncate");
  // §B1.4: the title is truncated; a long description (the About version line) wraps rather than overflowing.
  const title = decls(HEAD + " .page-title");
  assert.strictEqual(title.get("overflow"), "hidden");
  assert.strictEqual(title.get("text-overflow"), "ellipsis");
  assert.strictEqual(title.get("white-space"), "nowrap");
  assert.strictEqual(decls(HEAD + " .page-desc").get("overflow-wrap"), "anywhere");
  // The divider: 1x16, --line, margin 0 4px (§B1.4 `.m2-vsep`).
  const sep = decls(HEAD + " .set-sep");
  assert.strictEqual(sep.get("width"), "1px");
  assert.strictEqual(sep.get("height"), "16px");
  assert.strictEqual(sep.get("background"), "var(--line)");
  assert.strictEqual(sep.get("margin"), "0 4px");
  assert.strictEqual(sep.get("flex"), "none");
  // The head scrolls with the body (Plan 4 call 9): nothing pins it.
  assert.strictEqual(h.get("position"), undefined);
  assert.strictEqual(h.get("max-width"), undefined, "the column rule already caps every child at 820px");
});

test("a page's legacy button rules never restyle the head's buttons", () => {
  // The panels' own rules (tokens, accounts, Appearance, About) paint every `.btn` under the panel id: a secondary
  // fill, 6px 12px padding, a muted ghost turned raise. The head sits inside each panel, so those rules stop at
  // its door (`> :not(.set-head)`), and Esc ✕ keeps the kit's 24px ghost look and the action the kit's 28px
  // secondary one.
  const straight = /#(?:tokens|accounts|settings|about) \.btn(?![\w.-])/;
  const all = RULES.flatMap((r) => parts(r.selector));
  assert.deepStrictEqual(all.filter((s) => straight.test(s)), [], "no `.btn` rule straight under a panel id");
  assert.ok(all.filter((s) => /:not\(\.set-head\) \.btn/.test(s)).length >= 4, "the four panels' rules still exist, one step in from the panel id");
});

test("Credentials' head buttons take their kit values back from the page's `.btn` rules", () => {
  // Those three rules are the flows' and the drawer's too (credentials.test.js keeps every #creds rule paired with
  // #cred-drawer, disabled-reason.test.js pins the hover), so they are left as they are: the head restates the
  // kit values one id heavier and later. Only what the rules changed: padding, border colour, and for Esc ✕ the
  // chip radius, 12px type, ghost fill and ghost hover (§C3 `.btn`, `.btn-sm`, `.btn-ghost`).
  const C = P + " :is(#creds, #cred-drawer) > .set-head";
  const legacy = decls(P + " :is(#creds, #cred-drawer) .btn");
  assert.strictEqual(legacy.get("padding"), "5px 11px", "the rules being undone are still there");
  const act = decls(C + " .btn:where(:not(.pri))");
  assert.strictEqual(act.get("padding"), "0 12px");
  assert.strictEqual(act.get("border-color"), "transparent");
  const sm = decls(C + " .btn.btn-sm");
  assert.strictEqual(sm.get("padding"), "0 8px");
  assert.strictEqual(sm.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(sm.get("font-size"), "var(--t-sm)");
  const ghost = decls(C + " .btn.btn-ghost:where(:not(.pri))");
  assert.strictEqual(ghost.get("background"), "transparent");
  assert.strictEqual(ghost.get("color"), "var(--muted)");
  assert.strictEqual(decls(C + " .btn.btn-ghost:where(:not(.pri)):hover").get("background"), "var(--hover)");
  assert.strictEqual(decls(C + " .btn.btn-ghost:where(:not(.pri)):hover").get("color"), "var(--fg)");
  // They win the cascade: one id more than `:is(#creds, #cred-drawer) .btn…` (an id and the head's own class) and later in the file.
  const at = (sel) => DESK_CSS.indexOf(sel + " {");
  assert.ok(at(C + " .btn.btn-ghost:where(:not(.pri))") > at(P + " :is(#creds, #cred-drawer) .btn.dgr:hover"), "after the page's own button rules");
  const kit = decls(".btn-sm");
  assert.strictEqual(kit.get("padding"), "0 8px");
  assert.strictEqual(kit.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(kit.get("font-size"), "var(--t-sm)");
  assert.strictEqual(decls(".btn-ghost").get("background"), "transparent");
  assert.strictEqual(decls(".btn-ghost:hover").get("background"), "var(--hover)");
});

test("the panel buttons are the kit's 28px .btn (min-height --h-ctl, padding 0 12px)", () => {
  const BODY = P + " > :is(#tokens, #accounts, #settings, #about) > :not(.set-head) .btn";
  const b = decls(BODY);
  assert.strictEqual(b.get("min-height"), undefined, "the kit's own min-height: var(--h-ctl) stands");
  assert.strictEqual(b.get("padding"), "0 12px");
  assert.strictEqual(decls(":root").get("--h-ctl"), "28px");
  assert.strictEqual(decls(".btn").get("min-height"), "var(--h-ctl)");
  // The secondary fill still skips a primary (Save token stays secondary on the page: accounts.js picks "btn").
  assert.strictEqual(decls(BODY + ":where(:not(.pri))").get("background"), "var(--raise)");
  assert.match(read("accounts.js"), /acctBtn\("Save token", acctPageMode\(\) \? "btn" : "btn pri"/);
});

test("the add footers are gone on the page; the save footers are the kit footer", () => {
  for (const id of ["foot-tokens", "foot-accounts", "foot-settings", "foot-creds", "foot-about"]) {
    assert.strictEqual(decls(P + " > #" + id).get("display"), "none", id);
  }
  // Their buttons live on: "+ new token" and "+ new account" are the head's New token / Add account, Reset to
  // defaults is the head's, Sign out… is the nav's. On a phone the footers are as they were.
  for (const id of ["tok-new", "acct-new", "fs-reset", "tok-logout"]) assert.match(HTML, new RegExp('id="' + id + '"'), id + " is still in the DOM (the phone's footer)");
  assert.ok(!BASE_CSS.includes("foot-tokens") && !BASE_CSS.includes("foot-accounts") && !BASE_CSS.includes("foot-settings"), "no base rule hides them: the phone shows them");
  // The two footers still drawn: padding 12px 20px, gap 8px, --line on top, 884px, 28px buttons.
  // §C7 `.dlg-foot`: padding var(--sp-3) var(--sp-5), gap var(--sp-2), border-top 1px --line; the page column is 884px.
  for (const id of ["foot-creds-add", "foot-creds-form"]) {
    const f = new Map([...decls(P + " > .dft"), ...decls(P + " > #" + id)]);
    assert.strictEqual(f.get("padding"), "12px 20px", id);
    assert.strictEqual(f.get("gap"), "8px", id);
    assert.strictEqual(f.get("border-top"), "1px solid var(--line)", id);
    assert.strictEqual(f.get("max-width"), "884px", id);
    assert.strictEqual(f.get("grid-column"), "2", id);
    assert.strictEqual(f.get("grid-row"), "3", id);
    assert.strictEqual(f.get("min-height"), "0", id + ": 12 + 28 + 12, not the old 56px floor");
    assert.strictEqual(f.get("flex-wrap"), "wrap", id + ": a narrow window wraps the row rather than clipping Save");
    assert.strictEqual(f.get("background"), "var(--bg)", id + ": the page's surface, not a panel strip 884px wide");
    assert.strictEqual(f.get("display"), undefined, id + ": [hidden] still hides it (awsShowFoot / credFormFoot toggle it)");
  }
  const btn = decls(P + " > .dft .btn");
  assert.strictEqual(btn.get("min-height"), "var(--h-ctl)");
  assert.strictEqual(btn.get("padding"), "0 12px");
  assert.strictEqual(decls(":root").get("--sp-3"), "12px");
  assert.strictEqual(decls(":root").get("--sp-5"), "20px");
  // Save all / Save stay the one primary: the footer's secondary fill skips them.
  assert.strictEqual(decls(P + " > .dft .btn:where(:not(.pri))").get("background"), "var(--raise)");
  assert.match(HTML, /<button id="aws-save" type="button" class="btn pri off" disabled>Save all<\/button>/);
  assert.match(HTML, /<button id="cred-form-save" type="button" class="btn pri">Save<\/button>/);
});

test("on the page the stale or repeated copy is hidden: the old hints, the version line and the report button", () => {
  // Desktop copy comes from JS; the static strings stay for the phone, so each is hidden by a page rule.
  const hidden = [
    [P + " > #tokens > .set-head + .hint", "the tokens hint (the head's description says it)"],
    [P + " > #settings > .fs-card > .hint", "the stale 'panes behind this dialog' hint (F35)"],
    [P + " > #accounts > .set-head + .hint", "the accounts paragraph (lead -> head, rest -> the note)"],
    [P + " > #about > #about-version", "the version line (it is the head's description)"],
    [P + " > #about > p:has(> #bug-open)", "the Report a problem button (it is the head's action)"],
  ];
  for (const [sel, why] of hidden) assert.strictEqual(decls(sel).get("display"), "none", why);
  // The static markup those selectors rest on.
  assert.match(HTML, /id="tokens"[^>]*>\s*<div class="set-head"><\/div>\s*<p class="hint">An access token/);
  assert.match(HTML, /<\/div>\s*<p class="hint">Sizes apply as you change them/);
  assert.match(HTML, /id="accounts"[^>]*>\s*<div class="set-head"><\/div>\s*<p class="hint">Each agent account/);
  assert.match(HTML, /<p><button type="button" class="btn" id="bug-open">Report a problem<\/button><\/p>/);
  // The note shows on the page (base-hidden); [hidden] still wins for an empty one.
  assert.strictEqual(decls(P + " > #accounts > #acct-desk-note").get("display"), "block");
  assert.strictEqual(decls(P + " > #accounts > #acct-desk-note[hidden]").get("display"), "none");
});

test("Agent accounts and About, which have no frame, put their body in one kit card with hairline rows", () => {
  // §C7 `.card` (--panel, --r-card) and `.card-row + .card-row` (1px --line between rows).
  const list = decls(P + " > #accounts > #acct-list");
  assert.strictEqual(list.get("background"), "var(--panel)");
  assert.strictEqual(list.get("border-radius"), "var(--r-card)");
  assert.strictEqual(list.get("overflow"), "hidden");
  const row = decls(P + " > #accounts > #acct-list > .acct-row");
  assert.strictEqual(row.get("padding"), "14px 16px");
  assert.strictEqual(row.get("border-bottom"), "0");
  assert.strictEqual(decls(P + " > #accounts > #acct-list > .acct-row + .acct-row").get("border-top"), "1px solid var(--line)");
  assert.strictEqual(decls(".card").get("background"), "var(--panel)");
  assert.strictEqual(decls(".card-row + .card-row").get("border-top"), "1px solid var(--line)");
  // About: Box runtime, Keyboard shortcuts, the report paragraph are the three rows (the version and the report
  // button moved into the head).
  const rows = [P + " > #about > #about-runtime", P + " > #about > p:has(> #kbd-open-about)", P + " > #about > .hint"];
  for (const sel of rows) {
    const r = decls(sel);
    assert.strictEqual(r.get("background"), "var(--panel)", sel);
    assert.strictEqual(r.get("padding"), "12px 16px", sel);
    assert.strictEqual(r.get("margin"), "0", sel);
  }
  assert.strictEqual(decls(rows[0]).get("border-radius"), "var(--r-card) var(--r-card) 0 0");
  assert.strictEqual(decls(rows[2]).get("border-radius"), "0 0 var(--r-card) var(--r-card)");
  assert.strictEqual(decls(rows[1]).get("border-top"), "1px solid var(--line)");
  assert.strictEqual(decls(rows[2]).get("border-top"), "1px solid var(--line)");
  assert.strictEqual(decls(rows[0]).get("display"), "block");
  assert.match(HTML, /<p id="about-runtime"><\/p>\s*<p><button type="button" class="btn" id="kbd-open-about">Keyboard shortcuts<\/button><\/p>\s*<p class="hint">Something not working\?/);
});

test("desktop.css says Esc ✕ closes the page now (the stale comment is reworded)", () => {
  const raw = read("desktop.css");
  assert.ok(!/Esc\s+and ✕ close the page/.test(raw.replace(/\s+/g, " ")), "the old wording is gone");
  assert.match(raw.replace(/\s+/g, " "), /the page head's Esc ✕ \(settingshead\.js\) and the Esc key close the page/);
});

test("the new classes all have a rule or a HOOKS entry", () => {
  // set-head / set-sep are styled above; set-esc is only a handle (its look is the kit's .btn.btn-ghost.btn-sm).
  const names = { "set-head": has(HEAD), "set-sep": has(HEAD + " .set-sep") };
  for (const [n, ok] of Object.entries(names)) assert.ok(ok, n + " has a desktop rule");
  assert.match(fs.readFileSync(path.join(__dirname, "classnames.test.js"), "utf8"), /"set-esc"/, "classnames.test.js HOOKS lists set-esc, with its reason");
});

// ---- Task 3: the Profiles overview (frame M2.1, digest-design §B1.3-4, §B2, §C3, §C7, §D) ----------------------------------------
// profilepage.test.js pins the markup (what the head holds, in which order, with which classes); these pin the values the kit
// classes and the page's own rules give it.
const PV = P + " #pfv";
// The narrow-window steps are `@container` rules that override these on purpose (desktop-chrome.test.js pins them): the values
// below are the ones above every step, so the rules inside a container block are left out.
function withoutContainers(css) {
  let out = "";
  let at = 0;
  for (let i = css.indexOf("@container"); i >= 0; i = css.indexOf("@container", at)) {
    out += css.slice(at, i);
    let depth = 0;
    let j = css.indexOf("{", i);
    for (; j < css.length; j++) {
      if (css[j] === "{") depth++;
      else if (css[j] === "}" && --depth === 0) break;
    }
    at = j + 1;
  }
  return out + css.slice(at);
}
const BASE_RULES = parseRules(withoutContainers(DESK_CSS));
const bdecls = (sel) => decls(sel, BASE_RULES);
const bhas = (sel) => BASE_RULES.some((r) => parts(r.selector).includes(sel));

test("the overview is a bounded column: 884px at most, left-aligned, 32px sides, and the head carries the 24px top", () => {
  // §B1.3 `.m2-col { max-width: 884px }` (no margin auto) and §B1.5 `.m2-body { padding: 0 32px 32px }`.
  const col = bdecls(PV + " .pfv-in");
  assert.strictEqual(col.get("max-width"), "884px");
  assert.strictEqual(col.get("padding"), "0 32px 32px");
  assert.strictEqual(col.get("margin"), undefined, "left-aligned, as the frame (54px free at the right of a 1440 window)");
  // The quiet line under the head moved with the head's own 20px padding (it was 26px of margin).
  assert.strictEqual(bdecls(PV + " .pfv-note").get("margin"), "-4px 0 24px");
});

test("the profile head is the page head the other five pages draw: flex, top-aligned, gap 16, padding 24px 0 20px, wrapping", () => {
  // §B1.4 / §C7; the same values as `.set-head` (Task 2), which sits in a panel and not under #pfv.
  const h = bdecls(PV + " .pfv-head");
  assert.strictEqual(h.get("display"), "flex");
  assert.strictEqual(h.get("align-items"), "flex-start");
  assert.strictEqual(h.get("gap"), "var(--sp-4)");
  assert.strictEqual(h.get("padding"), "var(--sp-6) 0 var(--sp-5)");
  assert.strictEqual(h.get("flex-wrap"), "wrap", "a narrow page puts the actions under the title instead of pushing Esc ✕ out of reach");
  assert.strictEqual(h.get("margin"), undefined, "the old 26px margin under the head is the padding now");
  assert.strictEqual(h.get("justify-content"), undefined);
  const main = bdecls(PV + " .pfv-head-main");
  assert.strictEqual(main.get("flex"), "1 1 240px");
  assert.strictEqual(main.get("min-width"), "0");
  // The title row: the name, then the chip, 8px apart (§B1.4 `.m2-titlerow`); a long name is cut short, not wrapped.
  const row = bdecls(PV + " .pfv-title");
  assert.strictEqual(row.get("display"), "flex");
  assert.strictEqual(row.get("align-items"), "center");
  assert.strictEqual(row.get("gap"), "8px");
  const name = bdecls(PV + " .pfv-title .page-title");
  assert.strictEqual(name.get("overflow"), "hidden");
  assert.strictEqual(name.get("text-overflow"), "ellipsis");
  assert.strictEqual(name.get("white-space"), "nowrap");
  assert.strictEqual(name.get("min-width"), "0");
  // The chip is the kit's .chip (§C5), not a page chip of its own.
  assert.ok(!bhas(PV + " .pfv-chip") && !bhas(PV + " .pfv-chip-host"), "no page chip: .chip .chip-box / .chip-host");
  assert.strictEqual(bdecls(".chip").get("height"), "18px");
  // The actions: gap 8 from the kit's .page-actions; they may wrap on their own line, and the divider is 1x16 with 4px either side.
  const acts = bdecls(PV + " .pfv-head .page-actions");
  assert.strictEqual(acts.get("flex-wrap"), "wrap");
  assert.strictEqual(acts.get("min-width"), "0");
  assert.strictEqual(bdecls(".page-actions").get("gap"), "var(--sp-2)");
  const sep = bdecls(PV + " .pfv-head .set-sep");
  assert.deepStrictEqual([sep.get("flex"), sep.get("width"), sep.get("height"), sep.get("margin"), sep.get("background")], ["none", "1px", "16px", "0 4px", "var(--line)"]);
  // Not the page's old header rules.
  assert.ok(!bhas(PV + " .pfv-acts"), "pfv-acts is the kit's .page-actions now");
});

test("the buttons are the kit's: .btn is 28px, the ⋯ is .btn-icon (28x28) and the page has no button rules of its own", () => {
  assert.strictEqual(bdecls(":root").get("--h-ctl"), "28px");
  assert.strictEqual(bdecls(".btn").get("min-height"), "var(--h-ctl)");
  assert.strictEqual(bdecls(".btn-icon").get("width"), "var(--h-ctl)");
  // Save, Cancel, Create profile and Switch to box are .btn too (Task 4): no .pfv-btn height, fill or hover to drift from the kit's.
  for (const sel of [" .pfv-btn", " .pfv-btn-primary", " .pfv-btn-secondary", " .pfv-btn-ghost", " .pfv-btn-icon"]) assert.ok(!bhas(PV + sel), sel.trim() + ": a second button system");
  assert.ok(!/\.pfv-btn(?!-host)/.test(DESK_CSS.slice(DESK_CSS.indexOf(PV + " .pfv-in"))), "no .pfv-btn rule anywhere in the page's block");
  // Pressed while its menu is open (kit .btn.is-pressed: a menu button is expanded, not pressed).
  const open = bdecls(PV + ' .pfv-more .btn-icon[aria-expanded="true"]');
  assert.strictEqual(open.get("background"), "var(--raise)");
  assert.strictEqual(open.get("box-shadow"), "inset 0 0 0 1px var(--faint)");
});

test("a block is the frame's row: label 132px, the value, then Edit; 24px between; padding 14px 16px 14px 20px", () => {
  // §B2 `.m2-blk`.
  const b = bdecls(PV + " .pfv-block");
  assert.strictEqual(b.get("display"), "grid");
  assert.strictEqual(b.get("grid-template-columns"), "132px minmax(0, 1fr) auto");
  assert.strictEqual(b.get("column-gap"), "24px");
  assert.strictEqual(b.get("align-items"), "start");
  assert.strictEqual(b.get("padding"), "14px 16px 14px 20px");
  assert.strictEqual(bdecls(PV + " .pfv-block + .pfv-block").get("border-top"), "1px solid var(--line)");
  // The label (§B2 `h3.t-md.w-600.m2-blabel`: 13/20, 600, a column with the count 2px under the name).
  const l = bdecls(PV + " .pfv-label");
  assert.deepStrictEqual([l.get("font-size"), l.get("font-weight"), l.get("line-height")], ["var(--t-md)", "600", "20px"]);
  assert.deepStrictEqual([l.get("display"), l.get("flex-direction"), l.get("gap")], ["flex", "column", "2px"]);
  // The value is --fg 500 and the helper --muted at --t-sm (§B2: no third text tier), with no measure of its own: it wraps with the column.
  const main = bdecls(PV + " .pfv-main");
  assert.strictEqual(main.get("font-weight"), "500");
  assert.strictEqual(main.get("color"), "var(--fg)");
  const sub = bdecls(PV + " .pfv-sub");
  assert.strictEqual(sub.get("font-size"), "var(--t-sm)");
  assert.strictEqual(sub.get("color"), "var(--muted)");
  assert.strictEqual(sub.get("max-width"), undefined, "the helper runs the width of the value column, as the frame's");
  // The section title, 15/22 600, 10px above the card.
  const sh = bdecls(PV + " .pfv-sheet-h");
  assert.deepStrictEqual([sh.get("font-size"), sh.get("line-height"), sh.get("font-weight"), sh.get("margin-bottom")], ["var(--t-lg)", "var(--lh-lg)", "600", "10px"]);
  // The card: --panel, --r-card (it does not clip: the "+ Add" picker hangs out of an edited block). Advanced is the seventh block of the one
  // card; a second card exists only around a block in edit (Task 4), and the cards are 12px apart (§B3: "Gap between the three cards = body gap").
  const card = bdecls(PV + " .pfv-sheet");
  assert.strictEqual(card.get("background"), "var(--panel)");
  assert.strictEqual(card.get("border-radius"), "var(--r-card)");
  assert.strictEqual(card.get("overflow"), undefined);
  assert.strictEqual(bdecls(PV + " .pfv-sheet + .pfv-sheet").get("margin-top"), "12px");
});

test("Edit sits beside the value, on its first line, and a row does not light up under the pointer", () => {
  // §B2 `.m2-edit { margin-top: -2px }`: the 24px button centred on the 20px first line.
  assert.strictEqual(bdecls(PV + " .pfv-act > .btn").get("margin-top"), "-2px");
  // §B2: "No hover tint on blocks (none defined)". No rule paints a block under the pointer, none steps Edit's hover up, and the first, last
  // and only block carry no corner rules (they existed to clip a tint; the card's own radius rounds a transparent row).
  const hovered = BASE_RULES.flatMap((r) => parts(r.selector)).filter((sel) => sel.startsWith(PV + " .pfv-block") && /:hover|:first-child|:last-child|:only-child/.test(sel));
  assert.deepStrictEqual(hovered, [], "no hover tint and no clip radii on .pfv-block, the block in edit's included (its own card rounds it)");
  assert.ok(!BASE_RULES.some((r) => parts(r.selector).some((sel) => /\.pfv-block:hover/.test(sel))));
  assert.strictEqual(bdecls(PV + " .pfv-block").get("background"), undefined, "a row is the card's --panel");
});

test("Advanced is a normal block: its count under the label (12/18 --muted 400), no fold, no chevron, no .fold of the page's", () => {
  const n = bdecls(PV + " .pfv-advst");
  assert.deepStrictEqual([n.get("font-size"), n.get("line-height"), n.get("font-weight"), n.get("color")], ["var(--t-sm)", "var(--lh-sm)", "400", "var(--muted)"]);
  assert.strictEqual(bdecls(PV + " .pfv-adv-sum").get("font-size"), "var(--t-sm)", "the helper line, as every block's");
  // §B2: "Disclosure chevron / expander: none on this page". Nothing of the old fold is left in the page's rules, base or narrow step.
  const all = DESK_CSS.slice(DESK_CSS.indexOf(PV + " .pfv-in"));
  assert.doesNotMatch(all, /\.pfv-fold|\.pfv-chev|\.pfv-open|\.pfv-adv-label/, "no fold, chevron, open state or Advanced-only label rule");
  // The kit's .fold stays in the kit (Credentials' "Stored values" row will use it); the profile page does not.
  assert.strictEqual(bdecls(".fold").get("gap"), "var(--sp-2)");
  assert.doesNotMatch(all, /\.fold\b/);
});

test("the six pages' heads share one set of rules: a rule that carries both selectors cannot drift", () => {
  // The review found them already apart (the profile head's actions wrapped, the other pages' did not; only one wrapped its description).
  const H = P + " > .dbd > .set-head";
  const pairs = [
    [H, PV + " .pfv-head"],
    [H + " .page-head-text", PV + " .pfv-head-main"],
    [H + " .page-actions", PV + " .pfv-head .page-actions"],
    [H + " .page-title", PV + " .pfv-title .page-title"],
    [H + " .page-desc", PV + " .pfv-head .page-desc"],
    [H + " .set-sep", PV + " .pfv-head .set-sep"],
  ];
  for (const [a, b] of pairs) {
    const rule = BASE_RULES.find((r) => parts(r.selector).includes(a));
    assert.ok(rule, a + " has a rule");
    assert.ok(parts(rule.selector).includes(b), "the same rule carries " + b);
    assert.deepStrictEqual(BASE_RULES.filter((r) => parts(r.selector).includes(b)), [rule], b + " has no rule of its own to drift");
  }
  // The values both heads get from them.
  for (const root of [H, PV + " .pfv-head"]) {
    assert.strictEqual(bdecls(root === H ? H + " .page-desc" : root + " .page-desc").get("overflow-wrap"), "anywhere", "a long description wraps wherever it breaks");
  }
  const wrap = bdecls(H + " .page-actions");
  assert.deepStrictEqual([wrap.get("flex"), wrap.get("flex-wrap"), wrap.get("min-width")], ["0 1 auto", "wrap", "0"]);
  // The profile's model id is a user-set string, drawn as one mono word in the description: the span takes the paragraph's wrap.
  assert.strictEqual(bdecls(PV + " .pfv-head .page-desc").get("overflow-wrap"), "anywhere");
  assert.strictEqual(bdecls(PV + " .pfv-head-main").get("min-width"), "0", "the column may shrink below its longest word");
  assert.strictEqual(bdecls(".mono").get("white-space"), undefined, ".mono does not forbid the wrap");
});

// ---- Task 4: a block in edit (frame M2.2, digest-design §B3, §C3, §C4, §C6, §C7, §D) ----------------------------------------------
// profilepage.test.js pins the markup (the card split, the foot's order and classes); these pin the values.
const px = (v) => parseFloat(String(v));

test("the block in edit is its own card: the kit's --panel (no --raise), no Edit column, 18px under the form, then the foot", () => {
  // §B3: `section.card.m2-card.m2-editing` is a .card like the others; `.m2-editing .m2-blk` has two columns and padding-bottom 18px.
  const ed = bdecls(PV + " .pfv-editing");
  assert.strictEqual(ed.get("background"), undefined, "the card is --panel, as the cards around it: not the old --raise block");
  assert.strictEqual(ed.get("grid-template-columns"), "132px minmax(0, 1fr)", "§D `.m2-editing .m2-blk`: the label, the form, no Edit column");
  assert.strictEqual(ed.get("padding-bottom"), "0", "the foot is the block's last row, flush with the card's bottom");
  assert.strictEqual(ed.get("border-radius"), "var(--r-card)", "the 1px ring of a nudged block follows the card's corners");
  assert.strictEqual(bdecls(PV + " .pfv-edit-body").get("padding-bottom"), "18px", "§D `.m2-editing .m2-blk { padding-bottom: 18px }`: the form's own 18px above the foot's rule");
  // The row clip radii existed for a raised block in a card of rows: gone with it.
  const clips = BASE_RULES.flatMap((r) => parts(r.selector)).filter((sel) => /\.pfv-editing:(first|last|only)-child/.test(sel));
  assert.deepStrictEqual(clips, [], "no first/last/only-child corner rule on the block in edit");
  assert.strictEqual(bdecls(PV + " .pfv-sheet + .pfv-sheet").get("margin-top"), "12px", "§B3: the cards are 12px apart");
});

test("the block foot is the kit's .dlg-foot row across the card: 12px 20px, 8px between, a --line rule on top, the status filling it", () => {
  // §C7 `.dlg-foot` and `.dlg-foot .hint`; §B3: the foot spans the card's full width (the block's padding is 14px 16px 14px 20px, so it bleeds out).
  const f = bdecls(PV + " .pfv-foot");
  assert.deepStrictEqual([f.get("display"), f.get("align-items"), f.get("gap")], ["flex", "center", "var(--sp-2)"]);
  assert.strictEqual(f.get("padding"), "var(--sp-3) var(--sp-5)");
  assert.strictEqual(f.get("border-top"), "1px solid var(--line)");
  assert.strictEqual(f.get("grid-column"), "1 / -1", "across both columns of the block's grid");
  assert.strictEqual(f.get("margin"), "0 -16px 0 -20px", "out to the card's edges: the block's right and left padding");
  assert.strictEqual(f.get("margin-top"), undefined, "the form's 18px is the form's, not a margin of the foot");
  assert.strictEqual(f.get("padding-top"), undefined);
  const st = bdecls(PV + " .pfv-status");
  assert.strictEqual(st.get("flex"), "1 1 auto", "§C7 `.hint { flex: 1 1 auto }`: Cancel and Save are pushed to the right");
  assert.strictEqual(st.get("font-size"), "var(--t-sm)");
  assert.strictEqual(st.get("line-height"), "var(--lh-sm)");
  assert.ok(!bhas(PV + " .pfv-fbtns"), "no wrapper of the buttons: they are the foot's own children");
  assert.ok(!bhas(PV + " .pfv-btn-ghost"), "Cancel is a secondary .btn: no page ghost");
  // "Save as host…": red on clear, never a fill, still weighing 500 like a secondary (the kit's primary would make it 600).
  const host = bdecls(PV + " .pfv-btn-host");
  assert.deepStrictEqual([host.get("background"), host.get("color"), host.get("font-weight")], ["none", "var(--red)", "var(--w-medium)"]);
  assert.strictEqual(host.get("border-color"), "rgba(240, 106, 106, .6)", "the ring is the button's own 1px border, as the kit's .btn-danger");
  assert.strictEqual(host.get("box-shadow"), undefined);
});

test("workspace mode choices sit side by side: .pfv-choice is a two-column grid of option cards (§D `.m2-two`, `.m2-opt`)", () => {
  const two = bdecls(PV + " .pfv-choice");
  assert.strictEqual(two.get("grid-template-columns"), "repeat(2, minmax(0, 1fr))", "§D `.m2-two`");
  assert.strictEqual(two.get("gap"), "8px");
  assert.strictEqual(two.get("max-width"), undefined, "the form's own 560px bounds it, not a 440px of its own");
  // Anything in the fieldset that is not a card (New profile's hint under its tier cards: "A private copy needs a box.", "On this machine a
  // profile cannot hold ...") takes the whole row, under the cards, not a cell of its own beside one. The legend is no grid item: the rule is harmless there.
  const row = bdecls(PV + " .pfv-choice > :not(.pfv-card)");
  assert.strictEqual(row.get("grid-column"), "1 / -1", "a hint spans both columns");
  const spanning = BASE_RULES.filter((r) => parts(r.selector).includes(PV + " .pfv-choice > :not(.pfv-card)"));
  assert.strictEqual(spanning.length, 1, "one rule says it");
  assert.strictEqual(bdecls(PV + " .pfv-field > legend.pfv-flabel").get("margin-bottom"), "6px", "§D `.m2-legend { margin-bottom: 6px }`: the choice's legend is a field's");
  assert.ok(!bhas(PV + " .pfv-choice > legend.pfv-flabel"), "no 8px legend of the choice's own");
  // The card is the kit's .opt-card (Plan 1: §D `.m2-opt`: 10px 12px, --r-ctl, a 1px --line ring, the chosen one filled --raise): the page rule adds layout only.
  const kit = bdecls(".opt-card");
  assert.deepStrictEqual([kit.get("padding"), kit.get("border-radius"), kit.get("box-shadow")], ["10px 12px", "var(--r-ctl)", "inset 0 0 0 1px var(--line)"]);
  assert.strictEqual(bdecls(".opt-card.is-on").get("background"), "var(--raise)");
  const card = bdecls(PV + " .pfv-card");
  for (const k of ["padding", "border", "border-radius", "background", "box-shadow"]) assert.strictEqual(card.get(k), undefined, k + " is the kit's");
  assert.strictEqual(card.get("grid-template-columns"), "14px minmax(0, 1fr)", "§C6 `.check input`: a 14px radio");
  assert.strictEqual(card.get("column-gap"), "8px", "§C6 `.check { gap: 8px }`");
  const radio = bdecls(PV + " .pfv-card > input");
  assert.deepStrictEqual([radio.get("width"), radio.get("height"), radio.get("margin")], ["14px", "14px", "3px 0 0"]);
  assert.ok(!bhas(PV + " .pfv-on"), "the chosen card is .is-on (the kit's), not an outline of the page's");
  const title = bdecls(PV + " .pfv-card-t");
  assert.deepStrictEqual([title.get("font-weight"), title.get("line-height")], ["500", "var(--lh-md)"]);
  const desc = bdecls(PV + " .pfv-card-d");
  assert.deepStrictEqual([desc.get("font-size"), desc.get("line-height"), desc.get("color")], ["var(--t-sm)", "var(--lh-sm)", "var(--muted)"]);
});

test("the form is §B3's: 560px at most, 16px between fields, 6px inside one, the label above its control", () => {
  assert.deepStrictEqual([bdecls(PV + " .pfv-fields").get("gap"), bdecls(PV + " .pfv-fields").get("max-width")], ["16px", "560px"]);
  const field = bdecls(PV + " .pfv-field");
  assert.strictEqual(field.get("gap"), "6px", "§C6 `.field { gap: 6px }`");
  assert.strictEqual(bdecls(PV + " .pfv-field > legend.pfv-flabel").get("margin-bottom"), "6px");
  assert.strictEqual(bdecls(PV + " .pfv-ctl").get("max-width"), "560px", "a control is as wide as the form, not 440px of it");
  const help = bdecls(PV + " .pfv-hint");
  assert.deepStrictEqual([help.get("font-size"), help.get("line-height"), help.get("color")], ["var(--t-sm)", "var(--lh-sm)", "var(--muted)"], "§C6 `.field-help`");
});

test("profile field labels are sentence-case field labels (§C6 `.field-label`): 12/18, 500, --fg, no transform; controls rest on --faint", () => {
  const kit = bdecls(".field-label");
  const l = bdecls(PV + " .pfv-flabel");
  for (const k of ["font-size", "line-height", "font-weight", "color"]) assert.strictEqual(l.get(k), k === "font-weight" ? "500" : kit.get(k).replace("var(--w-medium)", "500"), k + " is the kit label's");
  assert.deepStrictEqual([l.get("font-size"), l.get("line-height"), l.get("font-weight"), l.get("color")], ["var(--t-sm)", "var(--lh-sm)", "500", "var(--fg)"]);
  assert.strictEqual(l.get("text-transform"), undefined, "sentence case: nothing uppercases it");
  assert.strictEqual(l.get("letter-spacing"), undefined);
  // §C6 `.input`: a --faint rest border (G12), --muted on hover, the accent on focus; mono is 12px; a textarea is 72px at least.
  const c = bdecls(PV + " .pfv-ctl");
  assert.strictEqual(c.get("border"), "1px solid var(--faint)");
  assert.strictEqual(c.get("height"), "32px");
  assert.strictEqual(bdecls(PV + " .pfv-ctl:hover").get("border-color"), "var(--muted)");
  assert.deepStrictEqual([bdecls(PV + " .pfv-ctl:focus-visible").get("border-color"), bdecls(PV + " .pfv-ctl:focus-visible").get("box-shadow")], ["var(--accent)", "0 0 0 1px var(--accent)"]);
  assert.strictEqual(bdecls(PV + " .pfv-ctl-mono").get("font-size"), "var(--t-sm)", "§C6 `.input.mono`");
  const area = bdecls(PV + " .pfv-area");
  assert.deepStrictEqual([area.get("min-height"), area.get("padding"), area.get("line-height")], ["72px", "8px 10px", "var(--lh-md)"], "§B3: the textarea is 72px, not the dead 56px");
});

test("the effort control is the kit's .seg look at 28px: 2px in a --bg bar with a --line ring, 24px items, --t-sm 500", () => {
  const seg = bdecls(PV + " .pfv-seg");
  assert.deepStrictEqual([seg.get("display"), seg.get("gap"), seg.get("padding"), seg.get("border"), seg.get("border-radius")], ["inline-flex", "2px", "2px", "0", "var(--r-ctl)"]);
  assert.deepStrictEqual([seg.get("background"), seg.get("box-shadow")], ["var(--bg)", "inset 0 0 0 1px var(--line)"]);
  assert.strictEqual(seg.get("height"), undefined, "its height is its items' and its padding's");
  assert.strictEqual(seg.get("justify-self"), "start", "as wide as its choices: a grid item would stretch the bar across the whole form");
  const item = bdecls(PV + " .pfv-seg button");
  assert.strictEqual(item.get("height"), "var(--h-sm)");
  assert.deepStrictEqual([item.get("padding"), item.get("border"), item.get("border-radius")], ["0 10px", "0", "var(--r-chip)"]);
  assert.deepStrictEqual([item.get("font-size"), item.get("font-weight"), item.get("color"), item.get("background")], ["var(--t-sm)", "var(--w-medium)", "var(--muted)", "transparent"]);
  const root = bdecls(":root");
  assert.strictEqual(px(root.get("--h-sm")) + 2 * px(seg.get("padding")), px(root.get("--h-ctl")), "24px items + 2 x 2px = the 28px a .btn is");
  assert.deepStrictEqual([bdecls(PV + " .pfv-seg button:hover").get("color"), bdecls(PV + " .pfv-seg button:hover").get("background")], ["var(--fg)", "var(--hover)"]);
  assert.deepStrictEqual([bdecls(PV + " .pfv-seg button.on").get("color"), bdecls(PV + " .pfv-seg button.on").get("background")], ["var(--fg)", "var(--raise)"]);
  // A permission's Read/Write is the same control: no 26px variant of its own.
  assert.ok(!bhas(PV + " .pfv-perm .pfv-seg") && !bhas(PV + " .pfv-perm .pfv-seg button"));
});

test("New profile's and Duplicate's head is the kit page head: its title is the shared one-line ellipsis rule and it has no title / subline rules of its own (G3-D1)", () => {
  // The h1.page-title / p.page-desc it draws take the kit's 18/24 600 and 13/20 muted (margin 0) and the shared head rules; the old h2 at --t-lg and its
  // 12/18 subline with a 3px margin are gone, so Profiles -> New profile does not change the title's size.
  const sel = PV + " .pfv-title .page-title";
  const rule = BASE_RULES.find((r) => parts(r.selector).includes(sel));
  assert.ok(rule, "the one title rule every head takes");
  const d = rule.decls;
  assert.deepStrictEqual([d.get("min-width"), d.get("overflow"), d.get("text-overflow"), d.get("white-space")], ["0", "hidden", "ellipsis", "nowrap"]);
  assert.ok(parts(rule.selector).includes(P + " > .dbd > .set-head .page-title"), "shared with the five other heads: it cannot drift");
  assert.ok(!rule.decls.has("overflow-wrap"), "no overflow-wrap: anywhere on a one-line title");
  assert.ok(!bhas(PV + " .pfv-title-name") && !bhas(PV + " .pfv-hsub"), "no h2 / subline rule of its own");
  assert.ok(!BASE_RULES.some((r) => /pfv-title-name|pfv-hsub/.test(r.selector)), "and none inside a list either");
  const title = decls(".page-title");
  assert.deepStrictEqual([title.get("font-size"), title.get("line-height"), title.get("font-weight")], ["var(--t-xl)", "var(--lh-xl)", "var(--w-semibold)"], "the kit's 18/24 600 the head's h1 inherits");
  assert.deepStrictEqual([decls(".page-desc").get("font-size"), decls(".page-desc").get("line-height"), decls(".page-desc").get("color")], ["var(--t-md)", "var(--lh-md)", "var(--muted)"], "13/20 muted");
  const head = BASE_RULES.find((r) => parts(r.selector).includes(PV + " .pfv-head"));
  assert.ok(head && parts(head.selector).includes(P + " > .dbd > .set-head"), "the head's box is the shared .set-head / .pfv-head rule");
});

// ---- Credential forms (UI plan 4, Task 6; digest-design §C6 `.field-label` / `.input`, G19) ---------------------------
// The add flows (Add token, Add AWS accounts, Add GitHub, Set a value) draw label.flab + input.fld into #creds-body on the
// page and into #cred-drawer-body in the drawer over the profile page. credentials.test.js requires every desktop rule that
// names #creds to name its drawer twin too, so the page's rules are written for `:is(#creds, #cred-drawer)` and the drawer's
// own, heavier rules keep what the drawer's mockup drew (a 5px label gap, a 60px textarea).
const CRED = P + " :is(#creds, #cred-drawer)";
const DRW = P + " > #cred-drawer > #cred-drawer-body";

test("credential form labels are sentence case on the page and in the drawer", () => {
  // app.css draws `:is(#creds, #cred-drawer) .flab` as 10.5px UPPERCASE with .08em tracking (the old label system, F17); only
  // the drawer reset it, so the tab's own add flows still shouted. §C6: 12/18, 500, --fg, sentence case.
  const kit = decls(".field-label");
  for (const [who, sel] of [["the page", CRED + " .flab"], ["the drawer", DRW + " .flab"]]) {
    const l = decls(sel);
    assert.strictEqual(l.get("text-transform"), "none", who + ": nothing uppercases it");
    assert.strictEqual(l.get("letter-spacing"), "0", who + ": no tracking");
    assert.strictEqual(l.get("font-size"), "var(--t-sm)", who);
    assert.strictEqual(l.get("font-weight"), "500", who);
    assert.strictEqual(l.get("color"), "var(--fg)", who);
    assert.strictEqual(l.get("font-size"), kit.get("font-size"), who + ": the kit's .field-label size");
    assert.strictEqual(l.get("color"), kit.get("color"), who + ": the kit's .field-label colour");
    assert.strictEqual(l.get("line-height"), "var(--lh-sm)", who + ": §C6 `.field-label` is 12/18, set on the rule itself");
  }
  const page = decls(CRED + " .flab");
  assert.strictEqual(page.get("line-height"), "var(--lh-sm)", "§C6 `.field-label` is 12/18, not the 20px a label inherits");
  assert.strictEqual(page.get("margin-bottom"), "6px", "§C6 `.field { gap: 6px }`: the label sits 6px above its control");
  // The page rule is not a bare #creds one (credentials.test.js) and the base rule it overrides is still the uppercase one.
  assert.ok(!has(P + " #creds .flab"));
  assert.match(BASE_CSS, /:is\(#creds, #cred-drawer\) \.flab \{[^}]*text-transform: uppercase/);
  // CSS cannot make a label sentence case that the source wrote in capitals: no label literal is all-caps.
  const src = read("credentials.js");
  const lits = [...src.matchAll(/awsField\([^,]+,\s*"[^"]+",\s*"([^"]+)"/g), ...src.matchAll(/cred(?:Secret)?Field\([^,]+,\s*"([^"]+)"/g), ...src.matchAll(/(?:awsEl|credEl)\("(?:label|span)", "flab", "([^"]+)"/g)].map((m) => m[1]);
  assert.ok(lits.length >= 12, "found the form labels: " + lits.length);
  for (const t of lits) assert.ok(t !== t.toUpperCase(), "sentence case in the source: " + t);
});

test("credential fields rest on --faint like the kit's .input (§C6), on the page and in the drawer", () => {
  const kit = decls(".input");
  const f = decls(CRED + " .fld");
  assert.strictEqual(f.get("border"), "1px solid var(--faint)", "3.05:1 on the panel (G12), not the old --line");
  for (const k of ["border", "height", "padding", "border-radius", "font-size"]) assert.strictEqual(f.get(k), kit.get(k), k + " is the kit's .input value");
  assert.deepStrictEqual([f.get("height"), f.get("padding"), f.get("border-radius")], ["var(--h-lg)", "0 10px", "var(--r-ctl)"]);
  // No fill, colour or font of its own: `.fld.ro` keeps its panel fill and muted text (app.css), and a secret or key
  // textarea keeps its mono (desktop.css `textarea.fld, .fld.secret-area`), both lighter than this rule's id weight.
  for (const k of ["background", "color", "font", "font-family"]) assert.ok(!f.has(k), "the page's field rule leaves " + k + " to the rules that own it");
  // An id-weighted border out-ranks the global field focus, so the rule has a focus of its own (desktop-focus.test.js lists it).
  const focus = decls(CRED + " .fld:focus-visible");
  assert.deepStrictEqual([focus.get("outline"), focus.get("border-color"), focus.get("box-shadow")], ["none", "var(--accent)", "0 0 0 1px var(--accent)"]);
  // §C6 `textarea.input`: grows with its rows, 72px at least, 8px 10px, 20px lines.
  const ta = decls(CRED + " textarea.fld");
  assert.deepStrictEqual([ta.get("height"), ta.get("min-height"), ta.get("padding"), ta.get("line-height")], ["auto", "72px", "8px 10px", "var(--lh-md)"]);
  // The drawer's own field rule (heavier, so it wins there) rests on the same border; its geometry is the mockup's and stays.
  const d = decls(DRW + " .fld");
  assert.strictEqual(d.get("border"), "1px solid var(--faint)");
  assert.deepStrictEqual([d.get("height"), d.get("padding"), d.get("border-radius")], ["32px", "0 10px", "var(--r-ctl)"]);
  assert.strictEqual(decls(DRW + " .fld:focus-visible").get("border-color"), "var(--accent)");
  // No Credentials field rule rests on --line any more.
  const onLine = [];
  for (const r of RULES) {
    const b = (r.decls.get("border") || "") + " " + (r.decls.get("border-color") || "");
    if (!/var\(--line\)/.test(b)) continue;
    for (const s of parts(r.selector)) if (/#creds|#cred-drawer/.test(s) && /\.fld(?![-\w])/.test(s.split(/[\s>+~]+/).pop()) && !/:(hover|focus)/.test(s)) onLine.push(s);
  }
  assert.deepStrictEqual(onLine, []);
});

test("the tab breadcrumb has room: --t-sm, at least 12px under it, and nothing added to the drawer's header band", () => {
  // G19 "breadcrumb 'Credentials / Add token' cramped": the tab drew it at the base 12px with no space under it, straight
  // onto the form. It is the first child of #creds-body (the drawer's lives in #cred-drawer-head, whose padding is pinned).
  const c = decls(P + " :is(#creds-body, #cred-drawer-body) .crumb");
  assert.ok(px(c.get("margin-bottom")) >= 12, "margin-bottom " + c.get("margin-bottom"));
  assert.strictEqual(c.get("font-size"), "var(--t-sm)");
  assert.strictEqual(c.get("line-height"), "var(--lh-sm)");
  assert.ok(!has(CRED + " .crumb"), "not a rule for every .crumb: the drawer's header band has its own");
  assert.strictEqual(decls(P + " > #cred-drawer > #cred-drawer-head .crumb").get("margin-bottom"), undefined);
});

test("the drawer's foot is the kit footer: the page footer's 28px buttons, 8px apart, a --line rule on top, kept in line with the drawer's 22px gutters", () => {
  // The task is "the kit save footer": #foot-creds-add / #foot-creds-form (Task 2) are one, and the drawer's foot (#cred-drawer-foot,
  // built by awsBuildFoot / credFormBuildFoot into the same buttons) was left at 32px with no border. Its buttons are now named in
  // the page footer's own button rules, so the two cannot drift; the padding stays the drawer's 14px 22px (head, body and status
  // all have 22px gutters, so the page footer's 20px would misalign it; no frame draws the drawer).
  const FOOT = P + " > #cred-drawer > #cred-drawer-foot";
  const page = P + " > .dft .btn";
  const drawer = FOOT + " > .btn";
  const keys = ["min-height", "padding", "border", "border-radius", "font-family", "font-size"];
  for (const k of keys) assert.strictEqual(decls(drawer).get(k), decls(page).get(k), k + " is the page footer's button value");
  assert.deepStrictEqual(keys.map((k) => decls(drawer).get(k)), ["var(--h-ctl)", "0 12px", "1px solid transparent", "var(--r-ctl)", "var(--font-ui)", "var(--t-md)"]);
  assert.strictEqual(decls(":root").get("--h-ctl"), "28px");
  assert.strictEqual(decls(".btn").get("font-weight"), "var(--w-medium)", "the kit .btn is 500; nothing in the foot lowers it");
  assert.ok(!decls(drawer).has("height") && !decls(drawer).has("font-weight"), "no height or weight of the drawer's own");
  assert.ok(!decls(drawer + ".pri").has("font-weight") && !has(drawer + ".pri"), "the primary keeps the kit's 600 and fill");
  // The secondary fill, its hover and the primary come from the Credentials `.btn` rules (3 ids, so no foot rule has to restate them);
  // they are the page footer's own values: raise, fg, --line on hover (skipping aria-disabled), no weight of their own.
  const sec = CRED + " .btn:where(:not(.pri))";
  for (const k of ["background", "color"]) assert.strictEqual(decls(sec).get(k), decls(page + ":where(:not(.pri))").get(k), "the secondary " + k + " is the page footer's");
  assert.deepStrictEqual([decls(sec).get("background"), decls(sec).get("color")], ["var(--raise)", "var(--fg)"]);
  const hov = ":where(:not([aria-disabled=\"true\"])):hover";
  assert.strictEqual(decls(sec + hov).get("background"), decls(page + ":where(:not(.pri))" + hov).get("background"));
  assert.strictEqual(decls(sec + hov).get("background"), "var(--line)");
  assert.ok(!has(drawer + ":where(:not(.pri))") && !has(drawer + ":where(:not(.pri))" + hov), "no 4-id fill rule in the foot to outrank that hover");
  const f = decls(FOOT);
  assert.strictEqual(f.get("gap"), "8px");
  assert.strictEqual(f.get("border-top"), "1px solid var(--line)");
  assert.strictEqual(f.get("padding"), "14px 22px", "the drawer's gutters, not the page footer's 20px");
  assert.strictEqual(decls(P + " > #cred-drawer > #cred-drawer-head").get("padding"), "18px 22px 16px");
  assert.strictEqual(decls(P + " > #cred-drawer > #cred-drawer-status").get("padding"), "8px 22px");
  // Cancel stays the drawer's quiet button: no fill until hovered.
  assert.strictEqual(decls(drawer + ":nth-last-child(2):where(:not(.pri))").get("background"), "transparent");
  // Plan 3's reasons live in credentials.js and are not touched by a stylesheet.
  const src = read("credentials.js");
  for (const fn of ["setDisabledReason", "awsFootReasons", "awsFootNote"]) assert.ok(src.includes("function " + fn) || src.includes(fn + "("), fn + " is still there");
});

// ---- Task 8 (#174): Appearance, frame M2.5 (digest-design §A8, §B6, §C3, §C7, §D) and its Alerts row (§E3, the Settings half of F39/F47) ----
const CARD = P + " .fs-card";
const PRIM_CSS = stripComments(read("primitives.css")) + stripComments(read("tokens.css"));
const APPEARANCE = /<div class="dbd pad" id="settings"[\s\S]*?(?=<div class="dbd pad" id="tokens")/.exec(HTML)[0];

test("Appearance is one card of six rows: four sizes, Alerts, Shortcuts (the stale hint stays for the phone, hidden on the page)", () => {
  // §B6: one card, six rows. The label of the sixth is drawn by initSettings (desktop copy comes from JS), so five labels are static.
  assert.match(APPEARANCE, /<div class="set-head"><\/div>\s*<div class="fs-card">/, "the card follows the head slot and nothing else sits beside it");
  assert.deepStrictEqual([...APPEARANCE.matchAll(/<span class="lbl">([^<]*)<\/span>/g)].map((m) => m[1]), ["Terminal", "Code", "Markdown", "File tree", "Alerts"]);
  assert.strictEqual((APPEARANCE.match(/<div class="fsrow card-row">/g) || []).length, 3, "Terminal, Code, Markdown");
  assert.match(APPEARANCE, /<div class="fsrow card-row" id="fsrow-filetree">/);
  assert.match(APPEARANCE, /<div class="fsrow card-row" id="fsrow-alerts">/);
  assert.match(APPEARANCE, /<p class="card-row"><button type="button" class="btn" id="kbd-open-settings">Keyboard shortcuts<\/button><\/p>\s*<\/div>\s*<\/div>\s*$/, "Shortcuts is the card's last row, and the card closes the panel");
  // The text the phone shows is as it was: the stale hint (still the phone's), the sample lines and every stepper label.
  assert.match(APPEARANCE, /<p class="hint">Sizes apply as you change them — the panes behind this\s+dialog are already using them\. A stored size wins over the default\.<\/p>\s*<p class="card-row">/);
  for (const t of ["terminal", "code", "markdown", "filetree"]) {
    assert.match(APPEARANCE, new RegExp('<input class="val fs-val" data-fs="' + t + '" type="number" min="8" max="32" step="1" aria-label="[^"]+">'), t);
  }
  assert.match(APPEARANCE, /\$ claude --model opus-5\n● Running the suite…<\/div>/);
  // The kit's own `.card` is the session card on a phone (app.css: an amber 2px border), so the wrapper must not carry it.
  assert.ok(!/class="(?:[^"]*\s)?card(?:\s[^"]*)?"/.test(APPEARANCE), "no bare .card in the panel");
});

test("what the Appearance panel gained is styled only on the desktop page, so the phone's pixels cannot move", () => {
  // The rows may carry classes the phone has no rule for; app.css, primitives.css and tokens.css define none of them
  // (the phone also hides #fsrow-alerts and #notify-why outright, which index.html keeps).
  for (const c of ["fs-card", "card-row", "check", "check-box", "notify-block", "notify-how", "notify-steps"]) {
    assert.ok(!new RegExp("\\." + c + "(?![\\w-])").test(BASE_CSS + PRIM_CSS), c + " is not a phone class");
  }
  assert.ok(/#fsrow-alerts, #notify-why \{ display: none; \}/.test(BASE_CSS), "the phone still hides the Alerts row and its reason");
  assert.match(APPEARANCE, /<div class="fsrow card-row" id="fsrow-alerts"><span class="lbl">Alerts<\/span>\s*<label class="check notify-opt"><input type="checkbox" class="check-box" id="notify-toggle" aria-describedby="notify-why"> Desktop notifications<\/label>\s*<div class="hint" id="notify-why"><\/div>\s*<\/div>/);
  // The Shortcuts label is built by JS, base-hidden for the phone (its own rule: the pinned list of desktop-only nodes is untouched).
  const hide = parseRules(BASE_CSS).find((r) => parts(r.selector).includes("#fs-kbd-lbl"));
  assert.ok(hide && hide.decls.get("display") === "none", "app.css hides #fs-kbd-lbl");
  assert.strictEqual(parts(hide.selector).length, 1);
  assert.strictEqual(decls(CARD + " > p.card-row > #fs-kbd-lbl").get("display"), "block", "and the page shows it");
});

test("the card is the frame's: --panel, --r-card, 4px 20px; its rows are one grid, 120px 132px then the sample, 24px apart, 12px 0, hairlines between", () => {
  // §B6 `.card.m2-acard` / `.m2-arow`, §C7 `.card`, `.card-row + .card-row`. The wrapper restates the card's two tokens: it cannot take the class.
  const card = decls(CARD);
  assert.strictEqual(card.get("background"), "var(--panel)");
  assert.strictEqual(card.get("border-radius"), "var(--r-card)");
  assert.strictEqual(card.get("padding"), "4px 20px");
  assert.strictEqual(decls(".card").get("background"), "var(--panel)");
  assert.strictEqual(decls(".card").get("border-radius"), "var(--r-card)");
  const row = decls(CARD + " > :is(.fsrow, p.card-row)");
  assert.strictEqual(row.get("display"), "grid");
  assert.strictEqual(row.get("grid-template-columns"), "120px 132px minmax(0, 1fr)");
  assert.strictEqual(row.get("column-gap"), "24px");
  assert.strictEqual(row.get("row-gap"), "0", "app.css's `.fsrow { gap: 20px }` must not open 20px between the switch and its reason (Alerts has two grid rows)");
  assert.strictEqual(row.get("align-items"), "center");
  assert.strictEqual(row.get("padding"), "12px 0");
  assert.strictEqual(row.get("margin"), "0");
  assert.strictEqual(row.get("border-bottom"), "0", "the legacy bottom rule (app.css .fsrow) is gone: the divider is the next row's top");
  // The divider is the kit's. Behind the hidden stale hint the adjacent-sibling rule cannot reach Shortcuts, so that one row restates it.
  assert.strictEqual(decls(".card-row + .card-row").get("border-top"), "1px solid var(--line)");
  assert.strictEqual(decls(CARD + " > .hint + .card-row").get("border-top"), "1px solid var(--line)");
  // The label: 13/20 500 --fg, the frame's `.t-md.w-500`, in a 120px column of its own (the legacy 152px is not here).
  const lbl = decls(CARD + " .lbl");
  assert.strictEqual(lbl.get("width"), "auto");
  assert.strictEqual(lbl.get("font-size"), "var(--t-md)");
  assert.strictEqual(lbl.get("font-weight"), "var(--w-medium)");
  assert.strictEqual(lbl.get("color"), "var(--fg)");
  // Shortcuts: the button spans the two columns after the label and is not stretched.
  const btn = decls(CARD + " > p.card-row > .btn");
  assert.strictEqual(btn.get("grid-column"), "2 / 4");
  assert.strictEqual(btn.get("justify-self"), "start");
  // The old rules these replace are gone.
  assert.ok(!has(P + " .fsrow") && !has(P + " .fsrow .lbl") && !has(P + " .sample"), "no rule for the legacy .fsrow / .lbl / .sample left");
});

test("the steppers are the frame's: 28px square buttons, 4px, a 60 x 28 value box on --bg with a --faint rim", () => {
  // §B6: `.btn.btn-icon` = 28 x 28 (--h-ctl) and `.input.tnum.m2-stepv` = 60 x 28, 4px between: 28 + 4 + 60 + 4 + 28 = 124px in the 132px column.
  // MEASURED on frames/m2-6 (a pixel scan of the PNG): the "−" button is x 698-725, y 121-148 (28 x 28), the value box x 730-789 (60) y 121-148 (28).
  // The task brief's "24 px squares" (the kit's .btn-sm) is not what the approved frame draws, so the frame's 28 is pinned.
  assert.strictEqual(decls(":root").get("--h-ctl"), "28px");
  const S = CARD + " .stepper";
  const st = decls(S);
  assert.deepStrictEqual([st.get("display"), st.get("align-items"), st.get("gap"), st.get("border"), st.get("width")], ["flex", "center", "var(--sp-1)", "0", "auto"]);
  assert.strictEqual(decls(":root").get("--sp-1"), "4px");
  const b = decls(S + " button");
  assert.strictEqual(b.get("width"), "var(--h-ctl)");
  assert.strictEqual(b.get("height"), "var(--h-ctl)");
  assert.strictEqual(b.get("padding"), "0");
  assert.strictEqual(b.get("border-radius"), "var(--r-ctl)");
  assert.strictEqual(b.get("background"), "var(--raise)");
  assert.strictEqual(b.get("color"), "var(--fg)");
  assert.strictEqual(b.get("font-size"), "var(--t-md)");
  assert.strictEqual(b.get("font-weight"), "var(--w-medium)");
  assert.strictEqual(decls(S + " button:hover").get("background"), "var(--line)", "the kit's .btn:hover");
  const v = decls(S + " .val");
  assert.strictEqual(v.get("width"), "60px");
  assert.strictEqual(v.get("height"), "var(--h-ctl)");
  assert.strictEqual(v.get("padding"), "0 4px");
  assert.strictEqual(v.get("border"), "1px solid var(--faint)", "the kit's .input rests on --faint (G12)");
  assert.strictEqual(v.get("border-radius"), "var(--r-ctl)");
  assert.strictEqual(v.get("background"), "var(--bg)");
  assert.strictEqual(v.get("color"), "var(--fg)");
  assert.strictEqual(v.get("text-align"), "center");
  assert.strictEqual(v.get("font-variant-numeric"), "tabular-nums");
  assert.strictEqual(v.get("font-size"), "var(--t-md)");
  assert.strictEqual(decls(S + " .val:hover").get("border-color"), "var(--muted)");
  // Its border is id-weighted, so the global field focus cannot reach it: it has a field focus of its own (desktop-focus.test.js lists it).
  const f = decls(S + " .val:focus-visible");
  assert.deepStrictEqual([f.get("outline"), f.get("border-color"), f.get("box-shadow")], ["none", "var(--accent)", "0 0 0 1px var(--accent)"]);
  assert.ok(!decls(S + " .val").has("border-left") && !decls(S + " .val").has("border-right"), "one border all round, not the legacy left and right rules");
});

test("a sample is a --bg box in mono, 8px 12px, 6px corners, 1.45 lines, set in the size it previews", () => {
  // §B6 `.s-bg.mono.m2-sample`: no border, --bg, `white-space: pre`, the inline font-size is the setting (initSettings / paintFsSample set it).
  const s = decls(CARD + " .sample");
  assert.strictEqual(s.get("background"), "var(--bg)");
  assert.strictEqual(s.get("border"), "0");
  assert.strictEqual(s.get("border-radius"), "var(--r-ctl)");
  assert.strictEqual(s.get("padding"), "8px 12px");
  assert.strictEqual(s.get("font-family"), "var(--font-mono)");
  assert.strictEqual(s.get("line-height"), "1.45");
  assert.strictEqual(s.get("white-space"), "pre");
  assert.strictEqual(s.get("overflow"), "hidden");
  assert.strictEqual(s.get("min-width"), "0", "a long line is cut inside the grid column, it does not widen the card");
  assert.strictEqual(s.get("color"), "var(--fg)");
  assert.match(read("app.js"), /s\.style\.fontSize = fontPrefs\.get\(surface\) \+ "px"/, "the size stays the inline style paintFsSample sets");
});

test("the Alerts row is §E2's: a label above the first line, the switch and its reason in the two columns after it", () => {
  const A = CARD + " > #fsrow-alerts";
  assert.strictEqual(decls(A).get("align-items"), "start", "M8.1 .m8-alrow: a tall reason must not centre the label against itself");
  assert.strictEqual(decls(A + " > .lbl").get("padding-top"), "1px");
  assert.strictEqual(decls(A + " > :is(.notify-opt, #notify-why)").get("grid-column"), "2 / 4");
  // The switch is the kit's drawn checkbox on a .check label.
  assert.strictEqual(decls(".check").get("display"), "flex");
  assert.strictEqual(decls(".check-box").get("width"), "14px");
  // A one-line reason sits under the switch's text (14px box + 8px gap = 22px in), 2px below it: 12/18 --muted.
  const why = decls(A + " > #notify-why");
  assert.strictEqual(why.get("margin"), "2px 0 0");
  assert.strictEqual(why.get("padding-left"), "22px");
  assert.strictEqual(why.get("font-size"), "var(--t-sm)");
  assert.strictEqual(why.get("line-height"), "var(--lh-sm)");
  assert.strictEqual(why.get("color"), "var(--muted)");
  // A reason with steps is M8.1 `.m8-stack-4`: flush left under the switch, 12px between its parts.
  const blk = decls(A + " > #notify-why.notify-block");
  assert.deepStrictEqual([blk.get("display"), blk.get("flex-direction"), blk.get("gap"), blk.get("margin-top"), blk.get("padding-left")], ["flex", "column", "var(--sp-3)", "var(--sp-3)", "0"]);
  assert.strictEqual(blk.get("color"), "var(--fg)", "the blocked list's lead words and the heading are --fg; the muted parts carry .muted themselves (§E3 d)");
  assert.strictEqual(decls(A + " > #notify-why.notify-block > *").get("margin"), "0");
  assert.strictEqual(decls(":root").get("--sp-3"), "12px");
  // `.m8-stack` (heading, 8px, list) and `.m8-steps` (18px in, 4px between, no margin).
  const how = decls(A + " #notify-why .notify-how");
  assert.deepStrictEqual([how.get("display"), how.get("flex-direction"), how.get("gap")], ["flex", "column", "var(--sp-2)"]);
  const steps = decls(A + " #notify-why .notify-steps");
  assert.deepStrictEqual([steps.get("margin"), steps.get("padding-left"), steps.get("display"), steps.get("flex-direction"), steps.get("gap")], ["0", "18px", "flex", "column", "4px"]);
  // Check again is the kit's small button: the panel's `.btn` rule (0 12px, --t-md, --r-ctl) is answered one id heavier, and it has a hover.
  const sm = decls(P + " > #settings #notify-why .btn.btn-sm");
  assert.deepStrictEqual([sm.get("padding"), sm.get("border-radius"), sm.get("font-size")], ["0 8px", "var(--r-chip)", "var(--t-sm)"]);
  assert.strictEqual(decls(P + " > #settings .fs-card .btn:where(:not(.pri)):hover").get("background"), "var(--line)", "Keyboard shortcuts and Check again light up under the pointer");
  const panel = P + " > :is(#tokens, #accounts, #settings, #about) > :not(.set-head) .btn";
  const at = (sel) => DESK_CSS.indexOf(sel + " {");
  assert.ok(at(P + " > #settings #notify-why .btn.btn-sm") > at(panel), "written after the panel rule");
  // Every new class has its rule.
  for (const sel of [CARD, A + " > #notify-why.notify-block", A + " #notify-why .notify-how", A + " #notify-why .notify-steps"]) assert.ok(has(sel), sel);
});

test("the value box's field focus is listed with the page fields that need one of their own", () => {
  const src = fs.readFileSync(path.join(__dirname, "desktop-focus.test.js"), "utf8");
  assert.ok(src.includes('"#page-host > #settings-modal.as-page .fs-card .stepper .val:focus-visible"'), "PAGE_FIELDS names the stepper's value box");
});

// ---- the Alerts row's DOM: every state of the reason, painted from notifyRowCopy ----------------------------------------------
// The two app.js functions are lifted out of the page script (as profilepage.test.js does) and run on the fake DOM above, with the
// real attention.js beside them.
function lift(name) {
  const src = read("app.js");
  const at = src.indexOf("function " + name + "(");
  assert.ok(at >= 0, name + " is in app.js");
  return src.slice(at, src.indexOf("\n}\n", at) + 2);
}
const NOTIFY_ON = "When this tab is not focused: blocked on you or done. This browser only.";
const NOTIFY_ASK = " Your browser asks for permission when you turn it on.";
function makeNotifyDom({ secure = true, perm = "default", origin = "https://box.example", wanted = false, prompt = "granted" } = {}) {
  const el = (tag, id, cls) => { const n = new FNode(tag); if (id) n.id = id; if (cls) n.className = cls; return n; };
  const root = el("div");
  const row = root.appendChild(el("div", "fsrow-alerts", "fsrow card-row"));
  const cb = row.appendChild(el("input", "notify-toggle"));
  const why = row.appendChild(el("div", "notify-why", "hint"));
  const store = new Map(wanted ? [["agent-box:notify", "1"]] : []);
  const ctx = {
    document: { getElementById: (id) => root.all((n) => n.id === id)[0] || null, createElement: (t) => new FNode(t), createTextNode: (t) => new FText(t) },
    window: { isSecureContext: secure, location: { origin } },
    URL,  // a browser has it; a bare vm context does not
    localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) },
    console,
  };
  ctx.$ = (id) => ctx.document.getElementById(id);
  if (perm !== "unsupported") {
    ctx.Notification = { permission: perm, requestPermission: async () => { if (prompt !== "default") ctx.Notification.permission = prompt; return prompt; } };
  }
  vm.createContext(ctx);
  new vm.Script(read("attention.js"), { filename: "attention.js" }).runInContext(ctx);
  for (const fn of ["notifyPerm", "notifyMono", "paintNotifyWhy", "initNotifyToggle"]) new vm.Script(lift(fn), { filename: "app.js:" + fn }).runInContext(ctx);
  return { ctx, root, row, cb, why, store };
}
const monos = (n) => withCls(n, "mono").map((m) => m.textContent);

test("on an http page the row says it is not available there, names the page and gives the two ways out; the switch stays off and disabled", () => {
  const origin = "http://100.76.118.121:8899";
  const { ctx, cb, why } = makeNotifyDom({ secure: false, perm: "default", origin });
  ctx.initNotifyToggle();
  assert.strictEqual(cb.disabled, true);
  assert.strictEqual(cb.checked, false);
  assert.ok(classesOf(why).includes("notify-block"));
  const [para, how] = kids(why);
  assert.strictEqual(para.tag, "p");
  assert.strictEqual(para.textContent, "Not available at this address. Browsers allow notifications only on https:// or localhost pages, and this page is http://100.76.118.121:8899.");
  assert.deepStrictEqual(monos(para), ["https://", origin], "the scheme and the page's address are machine text");
  assert.deepStrictEqual(classesOf(para), ["t-sm", "muted"]);
  assert.strictEqual(how.tag, "div");
  assert.strictEqual(kids(how)[0].textContent, "How to enable");
  assert.deepStrictEqual(classesOf(kids(how)[0]), ["t-sm", "w-600"]);
  const items = withCls(how, "notify-steps")[0].children.filter((c) => c.nodeType === 1);
  assert.deepStrictEqual(items.map((li) => li.textContent), [
    "On the machine CuliBox runs on, open http://127.0.0.1:8899. Localhost counts as secure.",
    "From another device, put HTTPS in front of CuliBox yourself, for example tailscale serve or your own reverse proxy, and open that https:// address.",
  ]);
  assert.deepStrictEqual(items.map((li) => classesOf(li)), [["muted"], ["muted"]], "§E3 a: the whole list is muted, bullets included");
  assert.deepStrictEqual(monos(items[0]), ["http://127.0.0.1:8899"]);
  assert.deepStrictEqual(monos(items[1]), ["tailscale serve", "https://"]);
  assert.strictEqual(withCls(why, "btn").length, 0, "nothing to check again on a page that cannot ask");
  assert.strictEqual(kids(why).length, 2);
});

test("the page's origin never becomes markup: the row is built with text nodes, not innerHTML", () => {
  const origin = 'http://x"><img src=x onerror=alert(1)>:8899';
  const { ctx, why } = makeNotifyDom({ secure: false, origin });
  ctx.initNotifyToggle();
  assert.strictEqual(why.all((n) => n.tag === "img").length, 0);
  // new URL() refuses it: no origin is claimed, and nothing of it is printed.
  assert.ok(!why.textContent.includes("<img"));
  const hostile = "http://a.b:1";
  const dom = makeNotifyDom({ secure: false, origin: hostile });
  dom.ctx.initNotifyToggle();
  assert.ok(dom.why.textContent.includes(hostile));
  for (const fn of ["notifyMono", "paintNotifyWhy", "initNotifyToggle"]) assert.doesNotMatch(lift(fn), /innerHTML|insertAdjacentHTML|outerHTML/, fn);
});

test("blocked in the browser: the browser-by-browser steps and Check again; the switch is disabled", () => {
  const { ctx, cb, why } = makeNotifyDom({ perm: "denied", wanted: true });
  ctx.initNotifyToggle();
  assert.strictEqual(cb.disabled, true);
  assert.strictEqual(cb.checked, false, "off, though the user had opted in: nothing can be sent");
  assert.ok(classesOf(why).includes("notify-block"));
  const [para, list, again] = kids(why);
  assert.strictEqual(para.textContent, "This browser has blocked notifications for this address. Only the browser can undo that:");
  const items = kids(list);
  assert.deepStrictEqual(items.map((li) => kids(li)[0].textContent), ["Chrome, Edge:", "Firefox:", "Safari:"]);
  assert.deepStrictEqual(items.map((li) => classesOf(kids(li)[0])), [["w-500"], ["w-500"], ["w-500"]], "the lead word is --fg 500, the rest muted");
  assert.deepStrictEqual(items.map((li) => classesOf(kids(li)[1])), [["muted"], ["muted"], ["muted"]]);
  assert.strictEqual(items[0].textContent, "Chrome, Edge: click the icon left of the address, set Notifications to Allow, then reload.");
  assert.strictEqual(items[1].textContent, "Firefox: click the icon left of the address and clear the Notifications block, then reload.");
  assert.strictEqual(items[2].textContent, "Safari: Safari \u203a Settings \u203a Websites \u203a Notifications, set this address to Allow.");
  const btn = kids(again)[0];
  assert.strictEqual(btn.tag, "button");
  assert.strictEqual(btn.type, "button");
  assert.strictEqual(btn.textContent, "Check again");
  assert.deepStrictEqual(classesOf(btn), ["btn", "btn-sm"]);
  assert.strictEqual(classesOf(list).includes("muted"), false, "the list is not muted as a whole (§E3 d)");
  assert.deepStrictEqual(items.map((li) => classesOf(li)), [[], [], []], "so a bullet and a lead word are --fg");
});

test("Check again reads the permission again: unblocked, the switch is usable and holds the focus; still blocked, the new button holds it", () => {
  const { ctx, cb, why, store } = makeNotifyDom({ perm: "denied", wanted: true });
  ctx.initNotifyToggle();
  // Still blocked: the block is redrawn, and the keyboard stays on the button that took the press.
  FNode.focused = null;
  withCls(why, "btn")[0].click();
  assert.strictEqual(cb.disabled, true);
  assert.strictEqual(withCls(why, "btn").length, 1);
  assert.strictEqual(FNode.focused, withCls(why, "btn")[0], "focus is on the redrawn Check again, not lost to the page");
  // The user unblocked it in the browser: permission is granted, and the opt-in they had made comes back on.
  ctx.Notification.permission = "granted";
  withCls(why, "btn")[0].click();
  assert.strictEqual(cb.disabled, false);
  assert.strictEqual(cb.checked, true);
  assert.strictEqual(why.textContent, NOTIFY_ON);
  assert.strictEqual(withCls(why, "btn").length, 0);
  assert.strictEqual(FNode.focused, cb, "the press's button is gone: the switch it unlocked takes the focus");
  assert.strictEqual(store.get("agent-box:notify"), "1");
});

test("available, switched off, the browser still to ask: the helper says what it does and that the browser will ask", () => {
  const { ctx, cb, why } = makeNotifyDom({ perm: "default", wanted: false });
  ctx.initNotifyToggle();
  assert.deepStrictEqual([cb.disabled, cb.checked], [false, false]);
  assert.strictEqual(why.textContent, NOTIFY_ON + NOTIFY_ASK);
  assert.strictEqual(classesOf(why).includes("notify-block"), false);
  assert.strictEqual(kids(why).length, 0, "plain text under the switch");
});

test("available and on (granted and opted in): the same line without the ask, so it is there on reopen too", () => {
  const { ctx, cb, why } = makeNotifyDom({ perm: "granted", wanted: true });
  ctx.initNotifyToggle();
  assert.deepStrictEqual([cb.disabled, cb.checked], [false, true]);
  assert.strictEqual(why.textContent, NOTIFY_ON);
});

test("permission granted but the switch off: the helper does not promise a prompt that will not come", () => {
  const { ctx, cb, why } = makeNotifyDom({ perm: "granted", wanted: false });
  ctx.initNotifyToggle();
  assert.deepStrictEqual([cb.disabled, cb.checked], [false, false]);
  assert.strictEqual(why.textContent, NOTIFY_ON);
});

test("no Notification at all: the secure page says the browser lacks it, and an http page still says how to get HTTPS", () => {
  const a = makeNotifyDom({ perm: "unsupported", secure: true });
  a.ctx.initNotifyToggle();
  assert.strictEqual(a.cb.disabled, true);
  assert.strictEqual(a.why.textContent, "This browser does not support notifications.");
  assert.strictEqual(classesOf(a.why).includes("notify-block"), false);
  const b = makeNotifyDom({ perm: "unsupported", secure: false, origin: "http://box.lan:9000" });
  b.ctx.initNotifyToggle();
  assert.match(b.why.textContent, /^Not available at this address\./);
  assert.match(b.why.textContent, /open http:\/\/127\.0\.0\.1:9000\./);
});

test("turning it on: granted repaints the helper as on; a prompt left unanswered keeps the refusal line; a Block shows the blocked steps", async () => {
  // Granted.
  let d = makeNotifyDom({ perm: "default", prompt: "granted" });
  d.ctx.initNotifyToggle();
  d.cb.checked = true;
  await d.cb.onchange();
  assert.strictEqual(d.store.get("agent-box:notify"), "1");
  assert.deepStrictEqual([d.cb.checked, d.cb.disabled], [true, false]);
  assert.strictEqual(d.why.textContent, NOTIFY_ON, "no 'You will be notified when an agent needs you…' any more");
  // Dismissed: the prompt closed without an answer, the permission is still "default".
  d = makeNotifyDom({ perm: "default", prompt: "default" });
  d.ctx.initNotifyToggle();
  d.cb.checked = true;
  await d.cb.onchange();
  assert.strictEqual(d.store.has("agent-box:notify"), false);
  assert.deepStrictEqual([d.cb.checked, d.cb.disabled], [false, false], "off, and the user can try again");
  assert.strictEqual(d.why.textContent, "Permission was not granted, so notifications stay off.");
  assert.strictEqual(classesOf(d.why).includes("notify-block"), false);
  // The line is for this refusal: opening Settings again reads the permission afresh.
  d.ctx.initNotifyToggle();
  assert.strictEqual(d.why.textContent, NOTIFY_ON + NOTIFY_ASK);
  // Blocked from the prompt: the browser will not ask again, so the row turns into the blocked steps at once.
  d = makeNotifyDom({ perm: "default", prompt: "denied" });
  d.ctx.initNotifyToggle();
  d.cb.checked = true;
  await d.cb.onchange();
  assert.strictEqual(d.store.has("agent-box:notify"), false);
  assert.deepStrictEqual([d.cb.checked, d.cb.disabled], [false, true]);
  assert.match(d.why.textContent, /^This browser has blocked notifications for this address\./);
  assert.strictEqual(withCls(d.why, "btn").length, 1);
});

test("turning it off clears the opt-in and the helper stays (it is the row's text, not a message that goes away)", async () => {
  const d = makeNotifyDom({ perm: "granted", wanted: true });
  d.ctx.initNotifyToggle();
  d.cb.checked = false;
  await d.cb.onchange();
  assert.strictEqual(d.store.has("agent-box:notify"), false);
  assert.strictEqual(d.why.textContent, NOTIFY_ON);
  assert.strictEqual(d.cb.checked, false);
});

test("a prompt that throws is a refusal, not a crash", async () => {
  const d = makeNotifyDom({ perm: "default" });
  d.ctx.Notification.requestPermission = async () => { throw new Error("no"); };
  d.ctx.initNotifyToggle();
  d.cb.checked = true;
  await d.cb.onchange();
  assert.strictEqual(d.cb.checked, false);
  assert.strictEqual(d.why.textContent, "Permission was not granted, so notifications stay off.");
});

test("initSettings draws the Shortcuts label once; app.css hides it below the breakpoint", () => {
  const el = (tag, id, cls) => { const n = new FNode(tag); if (id) n.id = id; if (cls) n.className = cls; return n; };
  const root = el("div");
  const p = root.appendChild(el("p", null, "card-row"));
  const btn = p.appendChild(el("button", "kbd-open-settings", "btn"));
  const ctx = { document: { getElementById: (id) => root.all((n) => n.id === id)[0] || null, createElement: (t) => new FNode(t) } };
  ctx.$ = (id) => ctx.document.getElementById(id);
  vm.createContext(ctx);
  new vm.Script(lift("fsShortcutsLabel"), { filename: "app.js:fsShortcutsLabel" }).runInContext(ctx);
  ctx.fsShortcutsLabel();
  ctx.fsShortcutsLabel();
  const labels = withCls(p, "lbl");
  assert.strictEqual(labels.length, 1, "twice is still once");
  assert.strictEqual(labels[0].textContent, "Shortcuts");
  assert.strictEqual(labels[0].id, "fs-kbd-lbl");
  assert.strictEqual(p.children.indexOf(labels[0]), 0, "before the button, so the label reads first");
  assert.strictEqual(p.children[1], btn);
  assert.match(lift("initSettings"), /fsShortcutsLabel\(\)/, "initSettings calls it (it runs at boot, and the node exists by then)");
  const bare = { document: { getElementById: () => null } };
  bare.$ = (id) => bare.document.getElementById(id);
  vm.createContext(bare);
  new vm.Script(lift("fsShortcutsLabel"), { filename: "app.js:fsShortcutsLabel" }).runInContext(bare);
  assert.doesNotThrow(() => bare.fsShortcutsLabel(), "no button, no label, no error");
});
