"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The spawn form sends runtime/options/account/replyable from the in-memory
// profiles[], which used to be loaded only at boot and after in-tab saves, so
// a stale tab silently reverted an edit made elsewhere. app.js needs a DOM and
// cannot be require()d, so pin the fix at the source level, the way
// profile-save-refill.test.js does.
const STATIC = path.join(__dirname, "..", "static");
const APP = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const HTML = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");

function body(name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(APP);
  assert.ok(m, name + " not found in app.js");
  const open = APP.indexOf("{", APP.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < APP.length; i++) {
    if (APP[i] === "{") depth++;
    else if (APP[i] === "}" && --depth === 0) return { text: APP.slice(open, i + 1), async: !!m[1] };
  }
  throw new Error("unbalanced braces in " + name);
}

test("openSpawnDialog is async and refetches profiles before opening", () => {
  const { text, async: isAsync } = body("openSpawnDialog");
  assert.ok(isAsync, "openSpawnDialog must be async");
  const load = text.indexOf("await loadProfiles(");
  const read = text.indexOf("loadLastSpawn(");
  const open = text.indexOf('openDialog("spawn-modal")');
  assert.ok(load >= 0, "openSpawnDialog must await loadProfiles(");
  assert.ok(open > load, "the refetch must precede openDialog");
  assert.ok(read > load, "the refetch must precede anything reading profiles[]");
  assert.match(text, /await loadProfiles\(false\)/);
});

test("openSpawnDialog says so when the refresh failed, and clears it otherwise", () => {
  const { text } = body("openSpawnDialog");
  const open = text.indexOf('openDialog("spawn-modal")');
  const err = text.search(/setDialogError\("spawn-modal",\s*fresh\s*\?\s*""\s*:\s*"Could not refresh profiles/);
  assert.ok(err > open, 'setDialogError("spawn-modal", …) must follow openDialog');
  assert.match(text, /showing the settings loaded earlier\. Reopen to retry\./);
});

test("the spawn dialog has an inline error line for setDialogError to find", () => {
  const dlg = HTML.slice(HTML.indexOf('<dialog id="spawn-modal"'), HTML.indexOf("</dialog>", HTML.indexOf('<dialog id="spawn-modal"')));
  assert.match(dlg, /class="[^"]*\bdlg-err\b[^"]*"/);
});

test("loadProfiles reports success/failure and keeps the picked profile", () => {
  const { text } = body("loadProfiles");
  assert.match(text, /return true/);
  assert.match(text, /return false/);
  // replaceChildren resets a <select> to its first option.
  const keep = text.search(/const (\w+) = \$\("agent"\)\.value/);
  const rebuild = text.indexOf(".replaceChildren(");
  assert.ok(keep >= 0 && keep < rebuild, "capture #agent value before the rebuild");
  assert.match(text, /profiles\.some\(\(p\) => p\.name === prev\)/);
});

test("saveProfile surfaces a failed profile reload", () => {
  const { text } = body("saveProfile");
  assert.match(text, /if \(!\(await loadProfiles\(false\)\)\) throw new Error\(/);
  assert.match(text, /Saved, but reloading it failed: /);
});

test("loadProfiles times out instead of hanging the spawn dialog", () => {
  const { text } = body("loadProfiles");
  assert.match(text, /api\("\/api\/profiles",\s*\{\s*signal: deadlineSignal\(5000\)\s*\}\)/);
});

test("openSpawnDialog ignores a second open while the first awaits the refetch", () => {
  const { text } = body("openSpawnDialog");
  const guard = text.search(/if \(spawnOpening\) return;/);
  const set = text.search(/spawnOpening = true;/);
  const load = text.indexOf("await loadProfiles(");
  const fin = text.search(/finally\s*\{\s*spawnOpening = false;/);
  assert.ok(guard >= 0 && guard < set && set < load, "guard, then set, then the await");
  assert.ok(fin > load, "the guard is released in a finally after the await");
  assert.match(APP, /let spawnOpening = false;/);
});

test("saveProfile's reload does not schedule a background retry", () => {
  const { text } = body("saveProfile");
  assert.match(text, /await loadProfiles\(false\)/);
  assert.doesNotMatch(text, /loadProfiles\(\)/);
});

test("the editor passes whether a box layer exists to runtimeToSend", () => {
  const { text } = body("readForm");
  assert.match(text, /runtimeToSend\(segValue\(\$\("pf-runtime"\)\), pfStoredRuntime, !metaLoaded \|\| !!boxRuntime\)/);
});

test("no caller uses AbortSignal.timeout directly; both go through deadlineSignal", () => {
  assert.doesNotMatch(APP, /AbortSignal\.timeout/);
  assert.match(body("timedFetch").text, /deadlineSignal\(ms\)/);
});

test("metaLoaded is set wherever boxRuntime is assigned from /api/meta", () => {
  const assigns = APP.match(/boxRuntime = \(meta && meta\.box_runtime\) \|\| "";\s*\n\s*metaLoaded = true;/g) || [];
  const all = APP.match(/boxRuntime = \(meta && meta\.box_runtime\) \|\| "";/g) || [];
  assert.ok(all.length >= 2);
  assert.strictEqual(assigns.length, all.length, "every meta assignment of boxRuntime must set metaLoaded");
});

// The persistent New-session buttons show "Opening…" while openSpawnDialog
// waits on loadProfiles. The sidebar menu item has no persistent button, so it
// is exempt; spawnOpening stays for the callers that have no button to wrap.
test("New session triggers show Opening… while profiles load", () => {
  for (const id of ["spawn-go", "side-new"]) {
    const m = new RegExp('\\$\\("' + id + '"\\)\\.onclick\\s*=\\s*([^\\n]*)').exec(APP);
    assert.ok(m, id + " handler not found");
    assert.match(m[1], new RegExp('withPending\\(\\s*\\$\\("' + id + '"\\),\\s*"Opening…",[^;]*openSpawnDialog\\('), id + " must wrap openSpawnDialog in withPending");
  }
  assert.match(APP, /let spawnOpening = false;/);
});

// #spawn-go is hidden on mobile, so forwarding the tap to it would show nothing.
// The mobile CTA wraps openSpawnDialog itself, and is a plain-text button so the
// "Opening…" label swap flattens no markup.
test("the mobile New session button shows Opening… itself", () => {
  const m = /const sgm = document\.getElementById\("spawn-go-mobile"\);\s*\n\s*if \(sgm\)([^\n]*)/.exec(APP);
  assert.ok(m, "spawn-go-mobile handler not found");
  assert.match(m[1], /withPending\(\s*sgm,\s*"Opening…",[^;]*openSpawnDialog\(/);
  assert.doesNotMatch(m[1], /spawn-go"\)|\.click\(\)/, "must not forward to the hidden #spawn-go");
  assert.match(HTML, /<button id="spawn-go-mobile"[^>]*>[^<]*<\/button>/, "#spawn-go-mobile must stay a plain-text button");
});
