"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The profile editor bug on a host-default instance: the box button carried
// value "" and Save sent no runtime, so the stored profile resolved to host
// while the editor still lit "box". app.js needs a DOM and cannot be
// require()d, so pin the two halves of the fix at the source level, the way
// tree-follows-focus.test.js does.
const APP = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");

// The text of `function name(...) { ... }` by brace matching from its opening
// line. Braces inside strings/regexes would confuse it; these two functions
// carry none that are unbalanced.
function body(name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(APP);
  assert.ok(m, name + " not found in app.js");
  const open = APP.indexOf("{", APP.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < APP.length; i++) {
    if (APP[i] === "{") depth++;
    else if (APP[i] === "}" && --depth === 0) return APP.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("renderPfRuntime's buttons both carry a real tier value", () => {
  const b = body("renderPfRuntime");
  assert.match(b, /value:\s*"box"/);
  assert.match(b, /value:\s*"host"/);
  assert.doesNotMatch(b, /value:\s*""/);
});

test("saveProfile re-fills the editor from the server after the write", () => {
  const b = body("saveProfile");
  const write = b.search(/method:\s*editing/);
  const refill = b.search(/fillForm\(/);
  assert.ok(write >= 0, "saveProfile no longer writes");
  assert.ok(refill > write, "saveProfile must fillForm( after the PUT/POST");
  assert.match(b, /Saved, but reloading it failed/);
});

// Duplicate must open an unsaved draft, as New does: a POST of "<name> copy" up
// front is what left a stray copy behind once the user renamed it and saved.
test("duplicateProfile opens a draft and writes nothing", () => {
  const b = body("duplicateProfile");
  assert.doesNotMatch(b, /api\(/);
  assert.doesNotMatch(b, /POST|PUT/);
  assert.match(b, /selectedProfile\s*=\s*""/);
  assert.match(b, /duplicateDraft\(/);
  assert.match(b, /fillForm\(/);
});

test("saveProfile picks create vs update from the draft state, not the typed name", () => {
  const b = body("saveProfile");
  assert.match(b, /saveTarget\(/);
  assert.doesNotMatch(b, /profiles\.some\(/);
});

// A slow save must show "Saving…" and ignore a second click. The wrap starts
// after the containment confirm (validation and the question stay outside) and
// before the write; afterwards the form's own sync runs again so the owner of
// #pf-save's text/disabled has the last word over the helper's restore.
test("saveProfile wraps the save chain in withPending after the confirm", () => {
  const b = body("saveProfile");
  const confirm = b.indexOf("await askConfirm(");
  const wrap = b.indexOf("withPending(");
  const write = b.search(/method:\s*editing/);
  assert.ok(confirm >= 0, "saveProfile no longer confirms");
  assert.ok(wrap > confirm, "withPending( must come after the askConfirm");
  assert.ok(write > wrap, "withPending( must come before the PUT/POST");
  assert.match(b, /withPending\(\s*\$\("pf-save"\),\s*"Saving…"/);
  // Validation early returns stay outside the wrap.
  assert.ok(b.indexOf("Name and Command are required.") < wrap);
});

test("saveProfile re-runs syncEditorActions after withPending settles", () => {
  const b = body("saveProfile");
  const wrap = b.indexOf("withPending(");
  const after = b.indexOf("syncEditorActions()", wrap);
  assert.ok(after > wrap, "syncEditorActions() must follow withPending(");
  assert.match(b.slice(wrap), /finally\s*\{\s*syncEditorActions\(\);\s*\}/);
});

// #109: the editor sends the revision it opened, so a save made from a stale
// form is refused instead of silently reverting a newer setting. A draft has no
// revision and POSTs as before; a stale refusal keeps the user's edits.
const HTML = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");

test("saveProfile sends the opened revision on PUT and nothing extra on POST", () => {
  const b = body("saveProfile");
  // Both carry the schema the page pinned at load; only the PUT carries a rev.
  assert.match(b, /JSON\.stringify\(withSchema\(editing \? withRev\(p, pfLoaded\) : p, pfSchema\.get\(\)\)\)/);
});

// #109: the schema comes from /api/meta, which the page reads at boot (initEnv);
// the pin keeps the first value, so a later read after a deploy cannot give an
// old page the new schema.
test("the page pins profile_schema from the first /api/meta answer", () => {
  assert.match(APP, /const pfSchema = schemaPin\(\);/);
  const boot = APP.indexOf("(async function initEnv(");
  assert.ok(boot >= 0, "initEnv not found");
  const pin = APP.indexOf("pfSchema.pin(meta && meta.profile_schema)", boot);
  assert.ok(pin > boot, "initEnv must pin meta.profile_schema");
  assert.ok(pin < APP.indexOf("metaLoaded = true", boot), "the pin comes with the first meta answer");
  // Only the boot read pins: no other /api/meta reader may move it.
  assert.strictEqual((APP.match(/pfSchema\.pin\(/g) || []).length, 1);
});

test("saveProfile shows a stale save on the banner before the per-block lines", () => {
  const b = body("saveProfile");
  const stale = b.indexOf("staleSave(");
  assert.ok(stale >= 0, "saveProfile must call staleSave(");
  assert.match(b, /showPfStale\(/);
  assert.ok(b.indexOf("showPfStale(") > stale);
  assert.ok(stale < b.indexOf('$("pf-st-err").textContent'), "staleSave( must run before the per-block lines");
  assert.ok(stale < b.indexOf("Save failed:"), "staleSave( must run before the generic failure line");
  // Cleared at the start, so a banner from the last attempt never outlives a retry.
  assert.match(b.slice(0, b.indexOf("withPending(")), /\$\("pf-stale"\)\.hidden\s*=\s*true/);
});

test("saveProfile refreshes the list when the name turned out to be taken", () => {
  const b = body("saveProfile");
  assert.match(b, /profile_exists[\s\S]*loadProfiles\(/);
});

test("fillForm hides the stale banner: any load clears it", () => {
  assert.match(body("fillForm"), /\$\("pf-stale"\)\.hidden\s*=\s*true/);
});

test("showPfStale opens Advanced first, then shows the message and scrolls to it", () => {
  const b = body("showPfStale");
  const open = b.indexOf("setPfAdvOpen(true)");
  const text = b.indexOf("pf-stale-msg");
  assert.ok(open >= 0 && text > open, "setPfAdvOpen(true) must come before the message is set");
  assert.match(b, /\$\("pf-stale-msg"\)\.textContent\s*=\s*model\.message/);
  assert.match(b, /\$\("pf-stale"\)\.hidden\s*=\s*false/);
  assert.match(b, /scrollIntoView\(\{\s*block:\s*"nearest"\s*\}\)/);
});

test("reloadStaleProfile refetches the list before it reopens the profile", () => {
  const b = body("reloadStaleProfile");
  const list = b.indexOf("loadProfiles(");
  const edit = b.indexOf("editProfile(");
  assert.ok(list >= 0 && edit > list, "loadProfiles( must come before editProfile(");
  assert.match(b, /newProfile\(\)/);
  assert.match(APP, /\$\("pf-stale-reload"\)\.addEventListener\("click",\s*reloadStaleProfile\)/);
});

test("the stale banner is in the editor markup, above the dialog's error line", () => {
  for (const id of ["pf-stale", "pf-stale-msg", "pf-stale-reload"]) {
    assert.match(HTML, new RegExp('id="' + id + '"'), id + " missing from index.html");
  }
  assert.ok(HTML.indexOf('id="pf-stale"') < HTML.indexOf('id="settings-err"'));
  assert.match(HTML, /<div class="warnbox" id="pf-stale" role="alert" hidden>/);
});
