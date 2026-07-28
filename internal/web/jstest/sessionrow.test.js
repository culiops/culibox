"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { attachedElsewhere } = require("../static/sessionrow.js");

const sess = (over) => Object.assign({ id: "claude-1", attached: false, exited: false }, over);

test("a session nobody is attached to is never elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess(), false), false);
  assert.strictEqual(attachedElsewhere(sess(), true), false);
});

test("attached and held by this page is not elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), true), false);
});

test("attached and not held by this page is elsewhere", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true }), false), true);
});

// An exited session's viewer is not something anyone can act on, and the row
// already says "exited".
test("an exited session is never elsewhere, even while attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: true, exited: true }), false), false);
});

// An older daemon behind a newer page sends no `attached` at all. The badge must
// never appear on a claim the server did not make.
test("a response with no attached field reads as not attached", () => {
  assert.strictEqual(attachedElsewhere({ id: "claude-1", exited: false }, false), false);
});

test("a missing session is not elsewhere rather than a crash", () => {
  assert.strictEqual(attachedElsewhere(null, false), false);
  assert.strictEqual(attachedElsewhere(undefined, false), false);
});

// Guards against a truthy-but-not-true value ever being treated as attached.
test("only a real boolean true counts as attached", () => {
  assert.strictEqual(attachedElsewhere(sess({ attached: "yes" }), false), false);
  assert.strictEqual(attachedElsewhere(sess({ attached: 1 }), false), false);
});
