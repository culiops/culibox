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
  const write = b.search(/cdSendProfile\(path,\s*editing/); // the write, through the inplace_history question (copydialog.js)
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
  const write = b.search(/cdSendProfile\(path,\s*editing/); // the write, through the inplace_history question (copydialog.js)
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
  assert.match(b, /cdSendProfile\(path, editing \? "PUT" : "POST", withSchema\(editing \? withRev\(p, pfLoaded\) : p, pfSchema\.get\(\)\)\)/);
});

// #120: the schema is baked into profileform.js by the server (PROFILE_SCHEMA_BAKED), so the
// pin comes from the same response as the form's code. /api/meta, read later (and again after
// a retry), no longer pins: a page loaded while the daemon restarted into a new build could
// otherwise pin the new schema next to the old form.
test("the page pins the schema baked into profileform.js, and /api/meta no longer pins", () => {
  assert.match(APP, /const pfSchema = schemaPin\(\);\s*pfSchema\.pin\(globalThis\.PROFILE_SCHEMA_BAKED\);/);
  const boot = APP.indexOf("(async function initEnv(");
  assert.ok(boot >= 0, "initEnv not found");
  assert.ok(!/profile_schema/.test(APP.slice(boot, boot + 6000)), "initEnv must not read profile_schema");
  assert.strictEqual((APP.match(/pfSchema\.pin\(/g) || []).length, 1);
});

test("profileform.js itself has no baked value: the server appends it per response", () => {
  assert.ok(!/PROFILE_SCHEMA_BAKED\s*=/.test(fs.readFileSync(path.join(__dirname, "..", "static", "profileform.js"), "utf8")));
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
  // Anchored to the branch itself: the success path below also calls loadProfiles(.
  assert.match(b, /if \(e\.code === "profile_exists"\) await loadProfiles\(false\);/);
});

test("fillForm hides the stale banner: any load clears it", () => {
  assert.match(body("fillForm"), /\$\("pf-stale"\)\.hidden\s*=\s*true/);
});

test("showPfStale shows the message and scrolls to it", () => {
  const b = body("showPfStale");
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

// Measured on a phone viewport: Load latest was 24px tall while every other
// button in the editor is 44. The floor is the page's --tap, inside the
// max-width 700px block like the editor's other mobile buttons, so desktop is
// unchanged.
test("Load latest takes the mobile tap floor", () => {
  const css = fs.readFileSync(path.join(__dirname, "..", "static", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = /([^{}]*#pf-stale-reload[^{}]*)\{([^{}]*)\}/.exec(css);
  assert.ok(rule, "no rule names #pf-stale-reload");
  assert.match(rule[2], /min-height:\s*var\(--tap\)/);
  // The rule sits inside an open @media (max-width: 700px) block.
  const before = css.slice(0, rule.index);
  const media = before.lastIndexOf("@media");
  assert.ok(media >= 0 && /^@media \(max-width: 700px\)/.test(before.slice(media)), "the rule is not under @media (max-width: 700px)");
  const between = before.slice(media);
  const open = (between.match(/\{/g) || []).length - (between.match(/\}/g) || []).length;
  assert.ok(open >= 1, "the @media (max-width: 700px) block is already closed before the rule");
});

// #120: a delete carries the revision the editor opened the profile at.
test("deleteProfile sends the loaded revision", () => {
  const b = body("deleteProfile");
  assert.match(b, /api\(deletePath\(name, pfLoaded\), \{ method: "DELETE" \}\)/);
});

// #120: the save's own answer holds the new revision: a failed re-read keeps it, so the next
// Save is not refused for the user's own save.
test("saveProfile keeps the revision the save answered with when the re-read fails", () => {
  const b = body("saveProfile");
  const keep = b.indexOf("withSavedRev(");
  assert.ok(keep >= 0, "saveProfile must take the revision from the save's answer");
  assert.ok(keep < b.indexOf("the profile list could not be refreshed"), "before the re-read can fail");
  assert.ok(keep > b.search(/method:\s*editing/), "after the write");
});

// #120: Load latest says why it failed and leaves the banner up, instead of going on with a stale list.
test("reloadStaleProfile surfaces a failed refetch and keeps the banner", () => {
  const b = body("reloadStaleProfile");
  assert.ok(!/\$\("pf-stale"\)\.hidden\s*=\s*true/.test(b), "the banner stays until a load succeeds (fillForm hides it)");
  assert.match(b, /if \(!\(await loadProfiles\(false\)\)\) \{[^}]*Load latest failed/);
  assert.match(b, /if \(!\(await editProfile\(name\)\)\)/);
});

test("editProfile answers whether the profile was loaded", () => {
  const b = body("editProfile");
  assert.match(b, /let loaded = true;/);
  assert.match(b, /loaded = false;/);
  assert.match(b, /return loaded;/);
});
