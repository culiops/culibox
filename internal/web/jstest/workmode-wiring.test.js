// Source checks for the work-mode cards in the profile editor: app.js and
// index.html are read as text (no DOM here), the way checkbox-theme.test.js
// reads the CSS. The pure rules are pinned in workmode.test.js; these pin that
// the editor actually goes through them.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const STATIC = path.join(__dirname, "..", "static");
const app = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const html = fs.readFileSync(path.join(STATIC, "index.html"), "utf8");
const profileform = fs.readFileSync(path.join(STATIC, "profileform.js"), "utf8");

// body: the braces-balanced body of `function name(...) {`.
function body(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\(").exec(src);
  assert.ok(m, "function " + name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(open, i + 1);
  }
  assert.fail("unbalanced braces in " + name);
}

test("the old clone carry-over is gone", () => {
  assert.doesNotMatch(app, /cloneToSend/);
  assert.doesNotMatch(app, /pfLoadedClone/);
  assert.doesNotMatch(profileform, /cloneToSend/);
});

test("readForm sends the work-mode fields through wmFieldsToSend, and only there", () => {
  const b = body(app, "readForm");
  assert.match(b, /wmFieldsToSend\(/);
  assert.match(b, /pfMode\(\)/);
  assert.doesNotMatch(b, /\bp\.default_cwd\b/, "default_cwd comes from wmFieldsToSend");
});

test("saveProfile refuses an incomplete copy before the PUT starts", () => {
  const b = body(app, "saveProfile");
  const at = b.indexOf("wmFormError(");
  assert.ok(at > 0, "saveProfile must call wmFormError");
  assert.ok(at < b.indexOf("withPending("), "wmFormError must run before withPending");
  assert.match(b, /wmErrText\(/);
  assert.match(b, /wmShareWarningsText\(/);
});

test("duplicateProfile gives a copy profile a fresh copy directory", () => {
  const b = body(app, "duplicateProfile");
  assert.match(b, /wmCopyDirSuggestion\(/);
  assert.ok(b.indexOf("wmCopyDirSuggestion(") < b.indexOf("fillForm("), "before fillForm");
});

test("fillForm shows the folder to copy in the workspace field", () => {
  const b = body(app, "fillForm");
  assert.match(b, /renderPfWorkspaceOptions\(\s*copy \? p\.clone_of/);
  assert.match(b, /pfSavedMode\s*=/);
  assert.match(b, /pfSavedCopyDir\s*=/);
});

test("syncPfMode is display only", () => {
  const b = body(app, "syncPfMode");
  assert.doesNotMatch(b, /\bfetch\(|\bapi\(|getBoundingClientRect|offsetHeight|offsetWidth|clientHeight|getComputedStyle|innerHTML/);
  assert.match(b, /wmCardState\(/);
  assert.match(b, /wmSwitchNote\(/);
  assert.match(b, /wmNeedsGitHubHint\(/);
});

test("the editor lock closes the work-mode radios too, and syncPfMode runs last", () => {
  assert.match(app, /\.ped input\[type=radio\]/);
  const b = body(app, "syncEditorActions");
  assert.ok(b.trimEnd().replace(/\}$/, "").trimEnd().endsWith("syncPfMode();"), "syncPfMode() is the last statement");
});

test("the meta reads set cloneUnavailable beside boxRuntime", () => {
  assert.match(app, /let cloneUnavailable = ""/);
  const n = (app.match(/cloneUnavailable = \(meta && meta\.clone_unavailable\) \|\| ""/g) || []).length;
  assert.strictEqual(n, 2);
});

test("index.html carries the two cards with their exact copy", () => {
  assert.ok(html.includes("<b>Work in this folder</b>"));
  assert.ok(html.includes("The agent edits your files directly. Your git history stays read-only: you review and commit yourself."));
  assert.ok(html.includes("<b>Work on a private copy</b>"));
  assert.ok(html.includes("The agent gets its own copy: it can commit, branch and push. Your folder is untouched until its work comes home."));
  assert.ok(html.includes("What's the difference?"));
  assert.ok(html.includes('id="pf-cwd-lab"'));
  for (const id of ["pf-mode-block", "pf-copy-block", "pf-copy-dir", "pf-copy-share", "pf-copy-gh", "pf-copy-warn", "pf-copy-err", "pf-mode-hint", "pf-mode-switch", "pf-mode-copy-why"]) {
    assert.ok(html.includes('id="' + id + '"'), id);
  }
});
