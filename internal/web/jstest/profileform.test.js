"use strict";
// What the profile editor may do, and what a destructive confirm says. Kept out
// of the DOM so the rules can be read and tested without a browser: "built-in"
// is the server's own `editable` verdict, and the containment confirm's wording
// is the whole point of the control.
const test = require("node:test");
const assert = require("node:assert");
const { editorState, saveLabel, duplicateName, containmentConfirm, segValue, setSegValue, awsFieldsState, segFromRuntime, runtimeToSend, boxToSend, cloneToSend } =
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

test("a stored runtime 'box' lights the box button; an AWS binding forces an explicit 'box'", () => {
  assert.strictEqual(segFromRuntime("box"), "");
  assert.strictEqual(segFromRuntime("host"), "host");
  assert.strictEqual(segFromRuntime(undefined), "");
  assert.strictEqual(runtimeToSend("", false, false), "");      // blank: the instance default decides
  assert.strictEqual(runtimeToSend("", true, false), "box");    // stored as box: stays box
  assert.strictEqual(runtimeToSend("", false, true), "box");    // AWS: the server refuses an empty runtime
  assert.strictEqual(runtimeToSend("host", false, true), "host"); // the server's refusal is the message
});

test("boxToSend returns the stored image override and nothing else", () => {
  assert.deepStrictEqual(boxToSend({ image: "culiops/agent-box:m3a-test" }), { image: "culiops/agent-box:m3a-test" });
  assert.deepStrictEqual(boxToSend({ image: "  img:1  " }), { image: "img:1" });
  for (const v of [undefined, null, {}, { image: "" }, { image: "   " }, { image: 42 }]) {
    assert.strictEqual(boxToSend(v), undefined, JSON.stringify(v));
  }
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
