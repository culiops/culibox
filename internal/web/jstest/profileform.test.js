"use strict";
// What the profile editor may do, and what a destructive confirm says. Kept out
// of the DOM so the rules can be read and tested without a browser: "built-in"
// is the server's own `editable` verdict, and the containment confirm's wording
// is the whole point of the control.
const test = require("node:test");
const assert = require("node:assert");
const { editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, cloneToSend, replyableToSend } =
  require("../static/profileform.js");

test("a new profile can be saved but not deleted or duplicated", () => {
  assert.deepStrictEqual(editorState(null),
    { locked: false, save: "on", delete: "off", duplicate: "off" });
});

test("a stored profile can do everything", () => {
  assert.deepStrictEqual(editorState({ name: "a", editable: true }),
    { locked: false, save: "on", delete: "on", duplicate: "on" });
});

test("a built-in is read-only, and duplicate is the way out", () => {
  assert.deepStrictEqual(editorState({ name: "claude", editable: false }),
    { locked: true, save: "off", delete: "off", duplicate: "on" });
});

test("saving into host promises the question", () => {
  assert.strictEqual(saveLabel("host"), "Save…");
  assert.strictEqual(saveLabel("box"), "Save");
  assert.strictEqual(saveLabel(""), "Save");
});

test("a duplicate gets a free name, never a collision", () => {
  assert.strictEqual(duplicateName("claude", ["claude"]), "claude copy");
  assert.strictEqual(duplicateName("claude", ["claude", "claude copy"]), "claude copy 2");
  assert.strictEqual(duplicateName("claude", ["claude", "claude copy", "claude copy 2"]), "claude copy 3");
  assert.strictEqual(duplicateName("claude", []), "claude copy");
});

test("box needs no confirm; host names what it gives away", () => {
  assert.strictEqual(containmentConfirm("box", "a"), null);
  assert.strictEqual(containmentConfirm("", "a"), null);
  const c = containmentConfirm("host", "review");
  assert.strictEqual(c.confirmLabel, "Save as host");
  assert.strictEqual(c.danger, true);
  assert.match(c.body, /review/);
  assert.match(c.body, /no container/i);
  // No type-to-confirm anywhere in this dialog: the confirm states the
  // consequence instead of asking for a password of a name.
  assert.strictEqual(c.mustMatch, undefined);
});

test("a segmented control's value round-trips through its container", () => {
  const el = { dataset: {}, querySelectorAll: () => [] };
  setSegValue(el, "host");
  assert.strictEqual(segValue(el), "host");
  assert.strictEqual(segValue(null), "");
  assert.strictEqual(segValue({ dataset: {} }), "");
});

test("the AWS block is off on a locked profile and on the host tier, never re-enabled by either", () => {
  assert.deepStrictEqual(awsFieldsState(false, ""), { disabled: false, hint: false });
  assert.deepStrictEqual(awsFieldsState(false, "host"), { disabled: true, hint: true });
  // a locked box profile is disabled, but the hint is about the tier, not the lock
  assert.deepStrictEqual(awsFieldsState(true, ""), { disabled: true, hint: false });
  assert.deepStrictEqual(awsFieldsState(true, "host"), { disabled: true, hint: true });
});

test("the editor lights the tier the profile really runs on", () => {
  assert.strictEqual(segFromRuntime("box", "box"), "box");
  assert.strictEqual(segFromRuntime("host", "host"), "host");
  assert.strictEqual(segFromRuntime("", "host"), "host");     // blank on a host-default instance (PROD)
  assert.strictEqual(segFromRuntime(undefined, "box"), "box");
  assert.strictEqual(segFromRuntime(undefined, undefined), "box"); // new profile
});

test("the editor always sends the tier it shows", () => {
  assert.strictEqual(runtimeToSend("box", "", true), "box");
  assert.strictEqual(runtimeToSend("host", "box", true), "host");
  assert.strictEqual(runtimeToSend("", "host", true), "host");
  assert.strictEqual(runtimeToSend("", "", true), "");
});

test("with no box layer, an unset tier lit as box stays unset", () => {
  // The seg lights box for a blank profile whose default resolves to box; sending
  // it would pin a profile that cannot spawn on an instance without containment.
  assert.strictEqual(runtimeToSend("box", "", false), "");
  assert.strictEqual(runtimeToSend("box", "box", false), "box"); // explicit stays explicit
  assert.strictEqual(runtimeToSend("host", "", false), "host");
  assert.strictEqual(runtimeToSend("box", "", true), "box");
});

// The retired per-profile box.image: the server refuses a save that carries it,
// so the editor must never send `box`. app.js needs a DOM and cannot be
// require()d, so pin it at the source level (see profile-save-refill.test.js).
test("readForm never sends a box override", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const app = fs.readFileSync(path.join(__dirname, "..", "static", "app.js"), "utf8");
  const m = /function readForm\s*\(/.exec(app);
  assert.ok(m, "readForm not found in app.js");
  const open = app.indexOf("{", app.indexOf(")", m.index));
  let depth = 0, end = -1;
  for (let i = open; i < app.length && end < 0; i++) {
    if (app[i] === "{") depth++;
    else if (app[i] === "}" && --depth === 0) end = i;
  }
  assert.ok(end > open, "unbalanced braces in readForm");
  const b = app.slice(open, end + 1);
  assert.doesNotMatch(b, /\bp\.box\b/);
  assert.doesNotMatch(b, /boxToSend/);
  assert.doesNotMatch(b, /pfLoadedBox/);
});

test("cloneToSend returns the stored clone fields and nothing else", () => {
  const loaded = { clone_of: "/r", clone_share: ["a"], other: 1 };
  const got = cloneToSend(loaded);
  assert.deepStrictEqual(got, { clone_of: "/r", clone_share: ["a"] });
  assert.notStrictEqual(got.clone_share, loaded.clone_share); // a copy, not the loaded array
  assert.deepStrictEqual(cloneToSend({ clone_of: "/r" }), { clone_of: "/r" }); // clone_share omitted when absent
  for (const v of [undefined, null, {}, { clone_of: "" }, { clone_of: 42 }, { clone_share: ["a"] }]) {
    assert.strictEqual(cloneToSend(v), undefined, JSON.stringify(v));
  }
});

test("a hidden replies row keeps what the profile stored", () => {
  assert.strictEqual(replyableToSend(false, false, true), true);
  assert.strictEqual(replyableToSend(false, false, undefined), false);
  assert.strictEqual(replyableToSend(true, false, true), false); // visible: the checkbox decides
  assert.strictEqual(replyableToSend(true, true, false), true);
});
