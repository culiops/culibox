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
