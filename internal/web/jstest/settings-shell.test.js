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
    [P + " > #settings > .hint:not(#notify-why)", "the stale 'panes behind this dialog' hint (F35)"],
    [P + " > #accounts > .set-head + .hint", "the accounts paragraph (lead -> head, rest -> the note)"],
    [P + " > #about > #about-version", "the version line (it is the head's description)"],
    [P + " > #about > p:has(> #bug-open)", "the Report a problem button (it is the head's action)"],
  ];
  for (const [sel, why] of hidden) assert.strictEqual(decls(sel).get("display"), "none", why);
  // The static markup those selectors rest on.
  assert.match(HTML, /id="tokens"[^>]*>\s*<div class="set-head"><\/div>\s*<p class="hint">An access token/);
  assert.match(HTML, /<p class="hint" id="notify-why"><\/p>\s*<p class="hint">Sizes apply as you change them/);
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
