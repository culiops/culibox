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

// The error scrolls itself into view, so the fold must already be open when it
// does: opening it afterwards adds ~580px above the line and pushes it off screen.
// Likewise the per-block lines (#pf-copy-err, #pf-st-err, #pf-awsc-err) sit above
// the dialog's line, so each is un-hidden before the scroll, not after.
test("a failed Save opens the fold first and scrolls to the error last", () => {
  const e = body(APP, "pfSaveError");
  assert.ok(e.indexOf("setPfAdvOpen(") >= 0 && e.indexOf("setDialogError(") >= 0);
  assert.ok(e.indexOf("setPfAdvOpen(") < e.indexOf("setDialogError("), "fold open before the scrolling error");
  const save = body(APP, "saveProfile");
  const at = save.indexOf('pfSaveError("Save failed: " + e.message)');
  assert.ok(at > 0, "the server refusal goes through pfSaveError");
  for (const shown of ['$("pf-st-err").hidden = false', '$("pf-awsc-err").hidden = false', '$("pf-copy-err").hidden = false']) {
    const i = save.indexOf(shown);
    assert.ok(i > 0 && i < at, shown + " must run before the final pfSaveError scroll");
  }
});

// Fix round 1: #pf-touch is aria-live, so an unchanged line is not repainted.
test("Can touch repaints only when its text changes", () => {
  const r = body(APP, "renderPfTouch");
  assert.match(r, /const key = JSON\.stringify\(rows\)/);
  assert.match(r, /if \(key === pfTouchKey[^)]*\) return;/);
  assert.ok(r.indexOf("pfTouchKey = key") < r.indexOf("paintReachRows("), "the key is stored before the paint");
});

// #151: /api/bundle/imports answers 404 when bundling is off, on every load.
// /api/meta says so first (bundle_imports), and the profile form then neither
// requests the route nor shows an error: the row takes today's "absent" path.
test("refreshPfImports is skipped when meta.bundle_imports is false", () => {
  const b = body(APP, "refreshPfImports");
  const skip = b.search(/if \(metaLoaded && !bundleImports\) \{[^}]*pfImportsAbsent = true;[^}]*paintPfImports\(\);[^}]*return;/);
  assert.ok(skip >= 0, "an early return that marks the route absent and repaints");
  assert.ok(skip < b.indexOf('api("/api/bundle/imports"'), "the return comes before the request");
  assert.ok(b.indexOf("++pfImportsSeq") < skip, "a slower earlier answer cannot land after the skip");
});

// Both globals are set from /api/meta wherever boxRuntime is, after metaLoaded
// (spawn-refetch.test.js pins that adjacency), and each name is unique across
// the page scripts (they share one global scope).
test("the meta reads set bundleImports and boxUnavailableReason beside boxRuntime", () => {
  assert.match(APP, /let bundleImports = false;/);
  assert.match(APP, /let boxUnavailableReason = "";/);
  assert.strictEqual((APP.match(/bundleImports = !!\(meta && meta\.bundle_imports\);/g) || []).length, 2);
  assert.strictEqual((APP.match(/boxUnavailableReason = \(meta && meta\.box_unavailable_reason\) \|\| "";/g) || []).length, 2);
  for (const name of ["bundleImports", "boxUnavailableReason"]) {
    const decl = new RegExp("^(?:let|const|var|function)\\s+" + name + "\\b", "m");
    const owners = fs.readdirSync(STATIC).filter((f) => f.endsWith(".js") && decl.test(read(f)));
    assert.deepStrictEqual(owners, ["app.js"], name + " is declared once, in app.js");
  }
});

// The profile overview (Task 5): the read view replaces the form and the profile
// list on the desktop page, so its two nodes sit where the page needs them, and
// the base sheet hides them for a phone (the modal keeps today's form).
test("#pf-navlist follows the Profiles tab", () => {
  const tabs = /<div class="tabs" id="settings-tabs"[\s\S]*?<button type="button" class="t" data-tab="creds"/.exec(HTML)[0];
  assert.match(tabs, /data-tab="manage"[^>]*>Profiles<\/button>\s*<div id="pf-navlist"[^>]*><\/div>\s*<button type="button" class="t" data-tab="creds"/);
});

test("#pfv is inside #manage", () => {
  const start = HTML.indexOf('<div class="dbd split" id="manage"');
  const end = HTML.indexOf('<div class="dbd pad" id="settings"');
  assert.ok(start > 0 && end > start);
  assert.match(HTML.slice(start, end), /<div id="pfv">\s*<div class="pfv-in" id="pfv-in"><\/div>\s*<\/div>/);
});

test("base app.css hides #pfv and #pf-navlist", () => {
  assert.match(stripComments(BASE_CSS), /#pfv, #pf-navlist \{ display: none; \}/);
});

test("desktop.css shows them on the page and takes the old list, form and footer away", () => {
  const css = stripComments(DESK_CSS);
  assert.match(css, /#page-host > #settings-modal\.as-page #pfv \{[^}]*display: block/);
  assert.match(css, /#page-host > #settings-modal\.as-page > #settings-tabs > \.t\[data-tab="manage"\]\.on \+ #pf-navlist \{[^}]*display: block/);
  assert.match(css, /#page-host > #settings-modal\.as-page #manage > \.plist, #page-host > #settings-modal\.as-page #manage > \.ped \{ display: none; \}/);
  assert.match(css, /#page-host > #settings-modal\.as-page > #foot-manage \{ display: none; \}/);
});

// The Credentials drawer over the profile page (Task 15): desktop markup that a phone never draws.
test("base app.css hides the Credentials drawer and its scrim; desktop.css shows them only once the page has opened them", () => {
  assert.match(stripComments(BASE_CSS), /#cred-scrim, #cred-drawer \{ display: none; \}/);
  const css = stripComments(DESK_CSS);
  // Fixed, over the whole page, above its own pickers (z 30) and below nothing of the page's.
  assert.match(css, /#page-host > #settings-modal\.as-page > #cred-scrim:not\(\[hidden\]\) \{[^}]*position: fixed; inset: 0; z-index: 50;/);
  const drawer = /#page-host > #settings-modal\.as-page > #cred-drawer:not\(\[hidden\]\) \{([^}]*)\}/.exec(css);
  assert.ok(drawer, "the drawer rule");
  assert.match(drawer[1], /position: fixed; top: 0; right: 0; bottom: 0; z-index: 51;/);
  assert.match(drawer[1], /width: 540px/);
  assert.match(drawer[1], /background: var\(--panel\)/);
  assert.match(drawer[1], /border-radius: var\(--r-dlg\) 0 0 var\(--r-dlg\)/, "10px on the free edge");
  // Every rule for the drawer's own parts is under the page (desktop-scope.test.js checks the one desktop block for the whole file);
  // the `:is(#creds, #cred-drawer)` twins of the Credentials rules are the flows' and carry their own scope.
  const own = [...css.matchAll(/(^|\n)\s*([^{}\n]*#cred-(?:drawer|scrim)[^{}\n]*)\{/g)].map((m) => m[2].trim()).filter((sel) => !sel.includes(":is(#creds, #cred-drawer)") && !sel.includes("#cred-drawer-body"));
  assert.ok(own.length >= 6, "the drawer's own rules");
  for (const sel of own) assert.match(sel, /^#page-host > #settings-modal\.as-page > #cred-/, "scoped: " + sel);
});

// Task 15 gate fix (R67/R68): the drawer's skin is the mockup's, scoped to #cred-drawer (the Credentials tab keeps its own rules).
test("the drawer's skin: a fixed header band, a body that scrolls on a flex gap, a two-column store card, a neutral region bar, the page's own primary text", () => {
  const css = stripComments(DESK_CSS);
  const D = "#page-host > #settings-modal.as-page > #cred-drawer";
  const rule = (sel) => { const at = css.indexOf("\n  " + sel + " {"); assert.ok(at >= 0, "rule: " + sel); return css.slice(at, css.indexOf("}", at)); };
  assert.match(rule(D + " > #cred-drawer-head"), /padding: 18px 22px 16px; border-bottom: 1px solid var\(--line\)/);
  assert.match(rule(D + " > #cred-drawer-head:empty"), /display: none/);
  assert.match(rule(D + " > #cred-drawer-head .crumb b"), /font-size: 15px; font-weight: 600; line-height: 22\.5px/);
  const body = rule(D + " > #cred-drawer-body");
  assert.match(body, /flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 22px; padding: 20px 22px/);
  // The body's last section used to keep a 22px margin on top of the body's padding (a :last-child rule that lost to the one above it).
  assert.doesNotMatch(css, /#cred-drawer-body > :last-child/);
  assert.match(rule(D + " > #cred-drawer-body .storecreate"), /grid-template-columns: repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(rule(D + " > #cred-drawer-body .storecreate .lead"), /font-weight: 500/);
  // The Service region bar: full width, neutral; accent stays with the primary button, the focus ring and links.
  const seg = rule(D + " > #cred-drawer-body .seg") + rule(D + " > #cred-drawer-body .seg button") + rule(D + " > #cred-drawer-body .seg button.on");
  assert.doesNotMatch(seg, /--accent/);
  assert.match(rule(D + " > #cred-drawer-body .seg button.on"), /background: var\(--raise\); font-weight: 500; color: var\(--fg\)/);
  assert.match(rule(D + " > #cred-drawer-body .seg"), /display: flex;/);
  // The primary button reads #0b1220 as the page's own block Save does.
  assert.match(css, /#pfv \.pfv-btn-primary \{ background: var\(--accent\); color: #0b1220; \}/);
  assert.match(rule(D + " > #cred-drawer-foot > .btn.pri"), /color: #0b1220/);
  // Buttons and labels are 13px; the native boxes 14px.
  assert.match(rule(D + " > #cred-drawer-foot > .btn"), /font-size: 13px/);
  assert.match(rule(D + " > #cred-drawer-body .pf-check"), /font-size: 13px/);
  assert.match(css, /#cred-drawer-body input\[type="checkbox"\], [^{]*input\[type="radio"\] \{ width: 14px; height: 14px;/);
  // A refused Save is the foot's own line, 12px.
  assert.match(rule(D + " > #cred-drawer-foot > .grow"), /font-size: 12px; line-height: 18px/);
});

test("the page script is loaded after the read model and before app.js", () => {
  const order = [...HTML.matchAll(/<script src="\/([a-z-]+\.js)"/g)].map((m) => m[1]);
  assert.ok(order.indexOf("profilepage.js") > order.indexOf("profileview.js"), "after profileview.js");
  assert.ok(order.indexOf("profilepage.js") < order.indexOf("app.js"), "before app.js");
});

test("Settings shows the overview when Profiles is selected on the page", () => {
  const SHELL = read("shell.js");
  assert.match(body(SHELL, "selectSettingsTab"), /name === "manage" && typeof pfvShow === "function"\) pfvShow\(\)/);
  assert.match(body(SHELL, "openSettings"), /typeof pfvShow === "function"\) pfvShow\(\)/);
  assert.match(body(APP, "renderProfileList"), /typeof pfvRenderNav === "function"\) pfvRenderNav\(\)/);
});

// The nav is the approved mockup's on every Settings tab (profile-mockups/option-b/index.html .nav):
// sentence-case 12px muted group labels, 30px rows, the page's own background.
test("the Settings nav is the mockup's: sentence-case muted 12px labels, 30px rows, --bg", () => {
  const css = stripComments(DESK_CSS);
  const rule = (sel) => {
    const i = css.indexOf(sel + " {");
    assert.ok(i >= 0, sel + " rule missing");
    return css.slice(i, css.indexOf("}", i));
  };
  const nav = rule("#page-host > #settings-modal.as-page > #settings-tabs");
  assert.match(nav, /background: var\(--bg\)/);
  assert.match(nav, /gap: 0/);
  const label = rule("#page-host > #settings-modal.as-page > #settings-tabs .pg-group");
  assert.match(label, /font-size: 12px; font-weight: 600; color: var\(--muted\)/);
  assert.ok(!/text-transform|letter-spacing/.test(label), "no uppercase, no tracking");
  assert.match(rule("#page-host > #settings-modal.as-page > #settings-tabs .t"), /height: 30px/);
  assert.match(rule("#page-host > #settings-modal.as-page > .dhd"), /font-size: 15px/);
});
