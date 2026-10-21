"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// A profile created through the API (or another tab) only showed in the
// Settings list after a full reload. Opening the Profiles tab now refetches the
// list; app.js and shell.js need a DOM, so pin the fix at the source level, the
// way spawn-refetch.test.js does.
const STATIC = path.join(__dirname, "..", "static");
const APP = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const SHELL = fs.readFileSync(path.join(STATIC, "shell.js"), "utf8");

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

test("showing the Profiles tab refetches the profile list", () => {
  assert.match(body(SHELL, "selectSettingsTab"), /name === "manage"[^\n]*loadProfiles\(false\)/);
});

test("loadProfiles repaints the list but never the editor form (unsaved edits survive)", () => {
  const text = body(APP, "loadProfiles");
  assert.match(text, /renderProfileList\(\)/);
  for (const forbidden of ["fillForm", "newProfile", "editProfile", "pf-"]) {
    assert.ok(!text.includes(forbidden), "loadProfiles must not touch the editor: found " + forbidden);
  }
});
