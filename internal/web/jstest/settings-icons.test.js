"use strict";
// #168 G2 1-10, 12, 13: the Settings nav tabs and Sign out, the Agent accounts head action and the account row's
// provider mark. Each glyph is keyed on a declared field (a tab's data-tab, the account's provider), never on the
// words beside it. The phone is frozen: its stylesheet hides every glyph this adds, desktop.css draws them on the page.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");
const { iconStubs } = require("./iconpage.js");
const { ICON_SETTINGS_TAB, ICON_AGENT_PROVIDER, ICON_ACTION, iconOf } = require("../static/iconmap.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const HTML = read("index.html");

test("ICON_SETTINGS_TAB and ICON_AGENT_PROVIDER are G2 1-9 and 13", () => {
  assert.deepStrictEqual({ ...ICON_SETTINGS_TAB }, {
    manage: "id-card", accounts: "circle-user-round", creds: "key-round", vmfolders: "hard-drive", wsroots: "folders",
    tokens: "monitor-smartphone", settings: "sliders-horizontal", linktrust: "link", about: "info",
  });
  assert.deepStrictEqual({ ...ICON_AGENT_PROVIDER }, { claude: "si:claude" });
  for (const m of [ICON_SETTINGS_TAB, ICON_AGENT_PROVIDER]) {
    assert.ok(Object.isFrozen(m));
    assert.equal(Object.getPrototypeOf(m), null);
    assert.equal(iconOf(m, "constructor"), "");
    assert.equal(iconOf(m, "__proto__"), "");
  }
});

// The markup of one button of the nav, by its opening-tag attribute.
function navButton(attr) {
  const at = HTML.indexOf(attr);
  assert.ok(at >= 0, attr + " is in index.html");
  const open = HTML.lastIndexOf("<button", at);
  return HTML.slice(open, HTML.indexOf("</button>", at) + 9);
}

test("settings nav glyphs by data-tab", () => {
  for (const [tab, glyph] of Object.entries(ICON_SETTINGS_TAB)) {
    const b = navButton('data-tab="' + tab + '"');
    assert.match(b, new RegExp('^<button[^>]*class="t[ "]'), tab + " is a nav tab");
    const holes = [...b.matchAll(/<svg\b[^>]*\bdata-uicon="([^"]+)"([^>]*)><\/svg>/g)];
    assert.deepEqual(holes.map((m) => m[1]), [glyph], tab);
    assert.match(holes[0][0], /width="15" height="15"/, tab + " at nav size 15");
    assert.ok(/<\/svg>[A-Za-z]/.test(b), tab + " keeps its words after the glyph");
  }
});

test("settings nav glyphs are keyed on data-tab, not on the words", () => {
  // The tab's text can be reworded without moving its glyph: no other attribute or word is read.
  const tabs = [...HTML.matchAll(/<button[^>]*\bdata-tab="([a-z]+)"/g)].map((m) => m[1]);
  assert.deepEqual([...tabs].sort(), Object.keys(ICON_SETTINGS_TAB).sort(), "every nav tab has a glyph, and only they do");
});

test("Sign out has log-out at 15", () => {
  const b = navButton('id="set-signout"');
  assert.match(b, /<svg\b[^>]*data-uicon="log-out"[^>]*width="15" height="15"[^>]*><\/svg>Sign out…/);
});

// Phone frozen: app.css hides the glyphs; desktop.css (inside the single min-width block) draws them on the page.
test("the phone draws no nav, Sign out or provider glyph; the desktop page draws them", () => {
  const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
  const base = strip(read("app.css"));
  const desk = strip(read("desktop.css"));
  assert.match(base, /#settings-tabs :is\(\.t, #set-signout\) > \.ui-icon[^{]*\{\s*display:\s*none/);
  assert.match(base, /\.acct-prov \.ui-icon[^{]*\{\s*display:\s*none/);
  assert.match(desk, /#settings-tabs :is\(\.t, #set-signout\) > \.ui-icon[^{]*\{\s*display:\s*inline-block/);
  assert.match(desk, /\.acct-prov \.ui-icon[^{]*\{[^}]*display:\s*inline-block[^}]*margin-right:\s*4px/);
});

function ctxWith(files, doc, extra) {
  const ctx = vm.createContext({
    console, Array, Object, Math, document: doc, ...iconStubs(() => new Node("svg", doc)), ...extra,
  });
  for (const f of files) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  return ctx;
}
function fakeDoc() {
  const doc = { readyState: "loading", addEventListener() {}, getElementById: () => null };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => { const n = new Node("#text", doc); n.own = String(t); return n; };
  return doc;
}

function metaOf(provider) {
  const doc = fakeDoc();
  const ctx = ctxWith(["accounts.js"], doc, { renderRateLimitsInto: () => 0 });
  const li = ctx.acctRow({ name: "work", provider, login_state: "logged_in", config_dir: "/h/.claude-work", default: true }, null, 0);
  return li.querySelector(".acct-meta");
}

test("account row shows the provider mark from a.provider", () => {
  const meta = metaOf("claude");
  const prov = meta.querySelector(".acct-prov");
  assert.ok(prov, "the provider word sits in its own span");
  assert.equal(prov.textContent, "claude");
  const svg = prov.children[0];
  assert.equal(svg.getAttribute("data-icon"), "si:claude");
  assert.equal(svg.getAttribute("width"), "12", "chip size, as the frame draws it");
  assert.equal(prov.children.length, 2, "mark, then the word");
  assert.equal(meta.textContent, "claude · " + vmText("logged_in") + " · /h/.claude-work", "the words are unchanged");
});

// What acctLoginText says for a login state: the row's words are read off it, not retyped.
function vmText(state) {
  return ctxWith(["accounts.js"], fakeDoc()).acctLoginText(state);
}

test("account row: an unknown provider draws no mark and the text is unchanged", () => {
  for (const p of ["other", "constructor", "__proto__"]) {
    const meta = metaOf(p);
    assert.equal(meta.querySelector("svg"), null, p + ": no svg");
    assert.equal(meta.querySelector(".acct-prov"), null, p + ": the word stays plain");
    assert.equal(meta.textContent, p + " · " + vmText("logged_in") + " · /h/.claude-work");
  }
});

test("account row: a missing or empty provider shows no word, no mark, no undefined and no leading separator", () => {
  const ctxRow = (a) => ctxWith(["accounts.js"], fakeDoc(), { renderRateLimitsInto: () => 0 }).acctRow(a, null, 0).querySelector(".acct-meta");
  const base = { name: "old", login_state: "logged_in", config_dir: "/h/.claude-old", default: true };
  for (const a of [base, { ...base, provider: "" }, { ...base, provider: null }]) {
    const meta = ctxRow(a);
    assert.equal(meta.querySelector("svg"), null);
    assert.equal(meta.querySelector(".acct-prov"), null);
    assert.equal(meta.textContent, vmText("logged_in") + " · /h/.claude-old");
    assert.ok(!/undefined|null/.test(meta.textContent) && !meta.textContent.startsWith(" "));
  }
});

test("Add account has the shared plus at 14, keeping its words", () => {
  const doc = fakeDoc();
  const ctx = ctxWith(["settingshead.js"], doc);
  const spec = ctx.settingsHeadSpec("accounts", {});
  const b = ctx.settingsHeadAction(spec.action);
  assert.equal(b.id, "set-act-accounts");
  assert.equal(b.children[0].getAttribute("data-icon"), ICON_ACTION.add);
  assert.equal(b.children[0].getAttribute("width"), "14");
  assert.equal(b.textContent, "Add account");
});
