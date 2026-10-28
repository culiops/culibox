"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

// The desktop Settings page preselects a profile after two fetches. A user who
// starts typing into the blank form meanwhile must keep it: typing marks the
// form a draft (pfUserDraft), which the preselect checks before and after each
// await. app.js needs a DOM, so pin it at the source level (as
// settings-profiles-refetch.test.js does).
const APP = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");

function body(name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(APP);
  assert.ok(m, name + " not found");
  const open = APP.indexOf("{", APP.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < APP.length; i++) {
    if (APP[i] === "{") depth++;
    else if (APP[i] === "}" && --depth === 0) return APP.slice(open, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

test("typing into the profile form with no profile selected marks it a draft", () => {
  const fn = body("pfMarkDraft");
  assert.match(fn, /if \(selectedProfile\) return/);
  assert.match(fn, /pfUserDraft = true/);
  assert.match(fn, /\.seg button/); // a click counts only on a segmented control
  // Wired once on the editor pane, for typing and for selects and checkboxes.
  assert.match(APP, /for \(const ev of \["input", "change", "click"\]\) \$\("pf-adv"\)\.closest\("\.ped"\)\.addEventListener\(ev, pfMarkDraft\)/);
});

test("the draft flag is set by real edits only, never by fillForm", () => {
  assert.ok(!body("fillForm").includes("pfMarkDraft"));
  assert.ok(!body("fillForm").includes("pfUserDraft"));
});

test("preselect re-checks the draft flag after every await", () => {
  const text = body("preselectPageProfile");
  const afterAwaits = text.split(/await /).slice(1);
  assert.equal(afterAwaits.length, 2, "two awaits expected");
  for (const chunk of afterAwaits) {
    assert.match(chunk, /pfUserDraft/, "an await is not followed by a pfUserDraft check");
  }
  // The check precedes fillForm, the write that would clobber the draft.
  assert.ok(text.lastIndexOf("pfUserDraft") < text.indexOf("fillForm(full)"));
});
