"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The Settings page on desktop (S4 Task 5): grouped nav, the regrouped profile
// form, Discard, the "Can touch" summary. app.js, spawnpanel.js and the
// stylesheets need a DOM to run, so the wiring is pinned at the source level,
// the way settings-profiles-refetch.test.js does it.
const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const HTML = read("index.html");
const APP = read("app.js");
const PANEL = read("spawnpanel.js");
const BUG = read("bugreport.js");
const BASE_CSS = read("app.css");
const DESK_CSS = read("desktop.css");

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

const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");

test("the tokens tab is named API tokens (the palette already says so)", () => {
  const m = /<button[^>]*data-tab="tokens"[^>]*>([^<]*)<\/button>/.exec(HTML);
  assert.ok(m, "tokens tab not found");
  assert.strictEqual(m[1].trim(), "API tokens");
  assert.ok(!/>Access tokens</.test(HTML), "no tab still says Access tokens");
});

test("the nav carries the three group labels, as presentation-only spans", () => {
  const tabs = /<div class="tabs" id="settings-tabs"[\s\S]*?<\/div>/.exec(HTML)[0];
  for (const label of ["Agents", "Access", "App"]) {
    assert.match(tabs, new RegExp('<span class="pg-group" role="presentation">' + label + "</span>"));
  }
});

test("the profile form gains the Can touch box first, three section heads and the Advanced fold", () => {
  const ped = HTML.slice(HTML.indexOf('<div class="ped">'), HTML.indexOf('<div class="dbd pad" id="settings"'));
  assert.match(ped, /^<div class="ped">\s*<div id="pf-touch" class="pf-touch" aria-live="polite"><\/div>/);
  for (const sec of ["agent", "where", "touch"]) assert.match(ped, new RegExp('<h3 class="pf-sec" data-sec="' + sec + '">'));
  assert.match(ped, /<button id="pf-adv" class="pf-adv" type="button" aria-expanded="false" aria-controls="pf-base-col pf-options pf-replyable-row pf-args-row pf-env-block">Advanced · base environment, options, args, environment variables, replies<\/button>/);
  // Every id aria-controls names exists.
  for (const id of ["pf-base-col", "pf-options", "pf-replyable-row", "pf-args-row", "pf-env-block"]) assert.match(ped, new RegExp('id="' + id + '"'), id);
  for (const id of ["pf-name-col", "pf-bin-col", "pf-base-col", "pf-cwd-col", "pf-args-row"]) {
    assert.match(ped, new RegExp('id="' + id + '"'), id);
  }
});

test("Discard sits in the Profiles footer before Save; About has a runtime line", () => {
  const foot = /<div class="dft" id="foot-manage">[\s\S]*?<\/div>/.exec(HTML)[0];
  assert.match(foot, /<button id="pf-discard" class="btn gh" type="button">Discard<\/button>\s*<button id="pf-save"/);
  assert.match(HTML, /<div class="dbd pad" id="about"[\s\S]*?<p id="about-runtime"><\/p>/);
});

test("the New session box and Can touch share one row painter", () => {
  assert.match(body(PANEL, "renderSpawnReach"), /paintReachRows\(/);
  const paint = body(PANEL, "paintReachRows");
  assert.match(paint, /inline/);
  assert.match(paint, /sr-prod/);
  assert.ok(!/innerHTML/.test(paint), "reach text goes in through textContent only");
});

test("renderPfOptions tags each option row with its key", () => {
  assert.match(body(APP, "renderPfOptions"), /row\.dataset\.opt = o\.key/);
});

test("Discard reloads the profile, or starts a new one when it is unsaved", () => {
  const d = body(APP, "discardProfileEdits");
  assert.match(d, /editProfile\(/);
  assert.match(d, /newProfile\(/);
  assert.match(APP, /\$\("pf-discard"\)\.addEventListener\("click", discardProfileEdits\)/);
});

test("Advanced toggles .adv-open on .ped and aria-expanded", () => {
  const i = APP.indexOf('$("pf-adv")');
  assert.ok(i > 0, "#pf-adv is wired");
  const near = APP.slice(i, i + 600);
  assert.match(near, /adv-open/);
  assert.match(near, /aria-expanded/);
});

test("Can touch is built by reachRows and repainted from the form", () => {
  const r = body(APP, "renderPfTouch");
  assert.match(r, /reachRows\(/);
  assert.match(r, /readForm\(\)/);
  assert.match(r, /pfCredsView/);
  assert.match(r, /paintReachRows\(/);
  assert.match(body(APP, "fillForm"), /renderPfTouch\(\)/);
});

test("About names the box runtime", () => {
  const a = body(BUG, "loadAboutInfo");
  assert.match(a, /about-runtime/);
  assert.match(a, /Box runtime: /);
  assert.match(a, /none \(host only\)/);
});

test("base CSS hides every desktop-only Settings node", () => {
  const css = stripComments(BASE_CSS);
  const m = /\.pg-group, \.pf-touch, \.pf-sec, \.pf-adv, #pf-discard, #about-runtime \{ display: none; \}/.exec(css);
  assert.ok(m, "the base hide rule is missing");
});

test("the page rules live in desktop.css and target #settings-modal.as-page", () => {
  const css = stripComments(DESK_CSS);
  assert.match(css, /#page-host > #settings-modal\.as-page/);
  // Done is modal chrome: hidden on the page.
  assert.match(css, /#page-host > #settings-modal\.as-page \[data-close\]:not\(#settings-esc\)\s*\{[^}]*display: none/);
});

// Fix round 1: a failed Save can be about a field folded under Advanced (the
// allowlist's rows, or a server refusal naming args or an option), so every
// failed Save opens the fold; a reload failure after a good Save does not.
test("a failed Save opens the Advanced fold", () => {
  const e = body(APP, "pfSaveError");
  assert.match(e, /setDialogError\("settings-modal", msg\)/);
  assert.match(e, /setPfAdvOpen\(true\)/);
  const o = body(APP, "setPfAdvOpen");
  assert.match(o, /classList\.toggle\("adv-open", open\)/);
  assert.match(o, /aria-expanded/);
  const save = body(APP, "saveProfile");
  for (const err of ['"Name and Command are required."', "rowsErr", "ghErr", "target.error", '"Save failed: " + e.message']) {
    assert.ok(save.includes("pfSaveError(" + err + ")"), "saveProfile reports " + err + " through pfSaveError");
  }
  assert.ok(!/setDialogError\("settings-modal", "Name and Command|setDialogError\("settings-modal", rowsErr|setDialogError\("settings-modal", "Save failed/.test(save), "no failed-Save path bypasses the fold");
});

// Fix round 1: #pf-touch is aria-live, so an unchanged line is not repainted.
test("Can touch repaints only when its text changes", () => {
  const r = body(APP, "renderPfTouch");
  assert.match(r, /const key = JSON\.stringify\(rows\)/);
  assert.match(r, /if \(key === pfTouchKey[^)]*\) return;/);
  assert.ok(r.indexOf("pfTouchKey = key") < r.indexOf("paintReachRows("), "the key is stored before the paint");
});
