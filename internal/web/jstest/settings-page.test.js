"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The Settings page on desktop (S4 Task 5): the grouped nav and the profile
// overview that replaced the profile form there. app.js, spawnpanel.js and the
// stylesheets need a DOM to run, so the wiring is pinned at the source level,
// the way settings-profiles-refetch.test.js does it. The old form (.plist, .ped,
// #foot-manage) is the phone's alone since Task 16.
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

test("the old profile form keeps the phone's fields and has no desktop-only node", () => {
  const ped = HTML.slice(HTML.indexOf('<div class="ped">'), HTML.indexOf('<div class="dbd pad" id="settings"'));
  assert.match(ped, /^<div class="ped">\s*<div class="lockbar" id="pf-lock" hidden>/);
  for (const id of ["pf-name-col", "pf-bin-col", "pf-base-col", "pf-cwd-col", "pf-args-row", "pf-options", "pf-replyable-row", "pf-env-block"]) {
    assert.match(ped, new RegExp('id="' + id + '"'), id);
  }
  // The Can touch box, the section heads and the Advanced fold were drawn on the desktop page only (base CSS hid them on a phone).
  assert.doesNotMatch(ped, /id="pf-touch"|class="pf-touch"|class="pf-sec"|id="pf-adv"|class="pf-adv"/);
});

test("the Profiles footer is the phone's (Delete, Duplicate, Save, Done); About has a runtime line", () => {
  const foot = /<div class="dft" id="foot-manage">[\s\S]*?<\/div>/.exec(HTML)[0];
  assert.match(foot, /id="pf-delete"[\s\S]*id="pf-duplicate"[\s\S]*id="pf-save"[\s\S]*data-close="settings-modal"/);
  assert.doesNotMatch(foot, /pf-discard/);
  assert.match(HTML, /<div class="dbd pad" id="about"[\s\S]*?<p id="about-runtime"><\/p>/);
});

test("the New session box paints its reach rows by textContent only", () => {
  assert.match(body(PANEL, "renderSpawnReach"), /paintReachRows\(/);
  const paint = body(PANEL, "paintReachRows");
  assert.match(paint, /sr-prod/);
  assert.doesNotMatch(paint, /inline/, "the Settings page's inline Can touch line went with the old form");
  assert.ok(!/innerHTML/.test(paint), "reach text goes in through textContent only");
});

test("About names the box runtime", () => {
  const a = body(BUG, "loadAboutInfo");
  assert.match(a, /about-runtime/);
  assert.match(a, /Box runtime: /);
  assert.match(a, /none \(host only\)/);
});

test("base CSS hides every desktop-only Settings node", () => {
  const css = stripComments(BASE_CSS);
  const m = /\.pg-group, \.pf-touch, \.pf-sec, \.pf-adv, #pf-discard, #about-runtime, #set-signout, \.set-head, #acct-desk-note \{ display: none; \}/.exec(css);
  assert.ok(m, "the base hide rule is missing");
});

test("the page rules live in desktop.css and target #settings-modal.as-page", () => {
  const css = stripComments(DESK_CSS);
  assert.match(css, /#page-host > #settings-modal\.as-page/);
  // Done is modal chrome: hidden on the page.
  assert.match(css, /#page-host > #settings-modal\.as-page \[data-close\]:not\(#settings-esc\)\s*\{[^}]*display: none/);
});

// A failed Save reports through the dialog's own line (a toast would paint beneath the modal backdrop), and the per-block
// lines (#pf-copy-err, #pf-st-err, #pf-awsc-err) sit above it, so each is un-hidden before the scroll, not after.
test("every failed Save reports through pfSaveError, per-block lines first", () => {
  const e = body(APP, "pfSaveError");
  assert.match(e, /setDialogError\("settings-modal", msg\)/);
  assert.doesNotMatch(e, /Adv/, "the Advanced fold was the desktop page's");
  const save = body(APP, "saveProfile");
  for (const err of ['"Name and Command are required."', "rowsErr", "ghErr", "target.error", '"Save failed: " + e.message']) {
    assert.ok(save.includes("pfSaveError(" + err + ")"), "saveProfile reports " + err + " through pfSaveError");
  }
  assert.ok(!/setDialogError\("settings-modal", "Name and Command|setDialogError\("settings-modal", rowsErr|setDialogError\("settings-modal", "Save failed/.test(save), "no failed-Save path bypasses pfSaveError");
  const at = save.indexOf('pfSaveError("Save failed: " + e.message)');
  assert.ok(at > 0, "the server refusal goes through pfSaveError");
  for (const shown of ['$("pf-st-err").hidden = false', '$("pf-awsc-err").hidden = false', '$("pf-copy-err").hidden = false']) {
    const i = save.indexOf(shown);
    assert.ok(i > 0 && i < at, shown + " must run before the final pfSaveError scroll");
  }
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
  assert.match(css, /#page-host > #settings-modal\.as-page > #settings-tabs > \.t\[data-tab="manage"\]\.on \+ #pf-navlist \{[^}]*display: flex/);
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
test("the drawer's skin: a fixed header band, a body that scrolls on a flex gap, a two-column store card, a neutral region bar, the kit's primary", () => {
  const css = stripComments(DESK_CSS);
  const D = "#page-host > #settings-modal.as-page > #cred-drawer";
  const rule = (sel) => { const at = css.indexOf("\n  " + sel + " {"); assert.ok(at >= 0, "rule: " + sel); return css.slice(at, css.indexOf("}", at)); };
  assert.match(rule(D + " > #cred-drawer-head"), /padding: 18px 22px 16px; border-bottom: 1px solid var\(--line\)/);
  assert.match(rule(D + " > #cred-drawer-head:empty"), /display: none/);
  assert.match(rule(D + " > #cred-drawer-head .crumb b"), /font-size: var\(--t-lg\); font-weight: 600; line-height: 22\.5px/);
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
  // One primary look (F11, #171): the page's block Save is the kit's .btn.btn-primary (fill, text, 600 and hover from the kit; Task 4 of
  // UI plan 4 took the page's own .pfv-btn-primary away), and the foot's Save is the kit's .btn.pri with no colour of its own.
  // "Save as host…" (.pfv-btn-host) stays red on a clear button.
  assert.doesNotMatch(css, /pfv-btn-primary/);
  assert.match(css, /#pfv \.pfv-btn-host \{ background: none; color: var\(--red\); font-weight: var\(--w-medium\); border-color: rgba\(240, 106, 106, \.6\); \}/);
  assert.ok(!css.includes("\n  " + D + " > #cred-drawer-foot > .btn.pri {"), "no foot rule restyles the primary");
  // Buttons and labels are --t-md (13px); the native boxes 14px.
  assert.match(rule(D + " > #cred-drawer-foot > .btn"), /font-size: var\(--t-md\)/);
  assert.match(rule(D + " > #cred-drawer-body .pf-check"), /font-size: var\(--t-md\)/);
  assert.match(css, /#cred-drawer-body input\[type="checkbox"\], [^{]*input\[type="radio"\] \{ width: 14px; height: 14px;/);
  // A refused Save is the foot's own line, --t-sm (12px).
  assert.match(rule(D + " > #cred-drawer-foot > .grow"), /font-size: var\(--t-sm\); line-height: 18px/);
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

// The nav is the M2 frame's on every Settings tab (UI plan 4, digest-design §B1.2; it replaced the Plan 1 profile-mockups
// look, which was sentence-case 12px labels on --bg): uppercase 11px .grp-label group labels, 30px rows, the --panel surface.
// settings-shell.test.js pins the rest of the frame (212px, padding, the nested list, Sign out…).
test("the Settings nav is the M2 nav: uppercase group labels, 30px rows, --panel", () => {
  const css = stripComments(DESK_CSS);
  const rule = (sel) => {
    const i = css.indexOf(sel + " {");
    assert.ok(i >= 0, sel + " rule missing");
    return css.slice(i, css.indexOf("}", i));
  };
  const nav = rule("#page-host > #settings-modal.as-page > #settings-tabs");
  assert.match(nav, /background: var\(--panel\)/);
  assert.match(nav, /gap: 1px/);
  const label = rule("#page-host > #settings-modal.as-page > #settings-tabs .pg-group");
  assert.match(label.replace(/\s+/g, " "), /font-size: var\(--t-xs\); line-height: var\(--lh-xs\); font-weight: 600; letter-spacing: var\(--track-caps\); text-transform: uppercase; color: var\(--muted\)/);
  assert.match(css, /#page-host > #settings-modal\.as-page > #settings-tabs \.t,\s*#page-host > #settings-modal\.as-page > #settings-tabs > #set-signout \{[^}]*height: 30px/);
  assert.match(rule("#page-host > #settings-modal.as-page > .dhd"), /display: none/);
});

// Task 16: the old profile form is the phone's alone. Nothing in desktop.css may style it any more: the only rules that name
// it are the two that keep it out of the page (an unstyled form would be drawn by the base sheet, which is the phone's).
test("no desktop.css rule targets .ped, .plist, .pitem, #foot-manage or any #pf-/.pf- node except the two that hide the old form", () => {
  const css = stripComments(DESK_CSS);
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim().replace(/\s+/g, " "), body: m[2].trim() }));
  // #pf-navlist and .pfv-* are the page's own; .pf-check is the Credentials flows' checkbox, which the drawer also draws.
  const OLD = /\.ped\b|\.plist\b|\.plist-foot\b|\.pitem\b|\.psub\b|#foot-manage\b|#pf-(?!navlist\b)|\.pf-(?!check\b)/;
  const hits = rules.filter((r) => OLD.test(r.sel));
  assert.deepStrictEqual(hits.map((r) => r.body), ["display: none;", "display: none;"], "only the two hide rules remain: " + hits.map((r) => r.sel).join(" | "));
  assert.match(hits[0].sel, /^#page-host > #settings-modal\.as-page #manage > \.plist, #page-host > #settings-modal\.as-page #manage > \.ped$/);
  assert.match(hits[1].sel, /^#page-host > #settings-modal\.as-page > #foot-manage$/);
});

test("the old form's desktop-only drivers are gone, and the wiring the phone and the desktop entry points use is not", () => {
  // Drivers of the nodes the page no longer draws: the preselect and its draft flag, the Can touch line, Discard, the Advanced fold.
  for (const name of ["preselectPageProfile", "pfUserDraft", "pfPreselecting", "pfMarkDraft", "renderPfTouch", "schedulePfTouch", "pfTouchKey", "discardProfileEdits", "setPfAdvOpen"]) {
    assert.ok(!new RegExp("\\b" + name + "\\b").test(APP + read("shell.js") + read("profileform.js")), name + " has no caller left");
  }
  // The phone: syncPfMode re-derives the work-mode display on every edit in the form (it used to hang off #pf-adv's parent).
  assert.match(APP, /for \(const ev of \["input", "change", "click"\]\) \$\("manage"\)\.querySelector\("\.ped"\)\.addEventListener\(ev, syncPfMode\)/);
  // The desktop entry into New profile that is not the page's own: the empty state's "Create box profile" presses the old button.
  assert.match(APP, /openSettings\("manage"\); \$\("pf-new"\)\.click\(\)/);
  assert.match(APP, /\$\("pf-new"\)\.addEventListener\("click", \(\) => \{[^}]*pfvNew\(\); return;/);
  // The page reads the credentials view (the reach words' elevate minutes) and is told when it changes.
  assert.match(body(APP, "refreshPfCreds"), /pfCredsView = got;\s*if \(typeof pfvMetaChanged === "function"\) pfvMetaChanged\(\);/);
});
