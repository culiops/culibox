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
  assert.match(text, /if \(!\(await loadProfiles\(\)\)\) throw new Error\(/);
  assert.match(text, /Saved, but reloading it failed: /);
});
