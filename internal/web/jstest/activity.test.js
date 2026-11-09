"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { activityClass, connStateLabel } = require("../static/activity.js");

test("known states map to their class + human title", () => {
  assert.deepEqual(activityClass("working"), { cls: "dot working", title: "working" });
  assert.deepEqual(activityClass("waiting"), { cls: "dot waiting", title: "waiting for input" });
  assert.deepEqual(activityClass("crashed"), { cls: "dot crashed", title: "crashed" });
  assert.deepEqual(activityClass("done"), { cls: "dot done", title: "exited" });
  assert.deepEqual(activityClass("detached"), { cls: "dot detached", title: "reconnecting" });
  assert.deepEqual(activityClass("idle"), { cls: "dot idle", title: "idle" });
});

test("unknown/missing activity is muted with no crash", () => {
  for (const v of [undefined, null, "", "bogus"]) {
    assert.equal(activityClass(v).cls, "dot unknown", JSON.stringify(v));
  }
});

test("a tab waiting only on an AWS sign-in does not claim to wait for input", () => {
  assert.deepEqual(activityClass("waiting", "sso"), { cls: "dot waiting", title: "AWS sign-in needed" });
  assert.deepEqual(activityClass("waiting", ""), { cls: "dot waiting", title: "waiting for input" });
  assert.equal(activityClass("working", "sso").title, "working", "why only relabels the waiting dot");
});

// F55 (#172): one vocabulary for connection states. The phone's dot titles above
// are unchanged ("reconnecting" stays the phone's word for a detached session);
// the desktop row and dot read this map instead. Plan 7 reuses it on pane bars.
test("connStateLabel: detached, taken over and offline have their one wording, anything else is empty", () => {
  assert.strictEqual(connStateLabel("detached"), "detached \u00b7 reconnecting");
  assert.strictEqual(connStateLabel("taken-over"), "open in another tab");
  assert.strictEqual(connStateLabel("offline"), "offline");
  for (const v of ["", "working", "connected", "taken over", undefined, null, 7]) assert.strictEqual(connStateLabel(v), "", String(v));
});

test("connStateLabel does not touch the dot titles the phone draws", () => {
  assert.strictEqual(activityClass("detached").title, "reconnecting");
  assert.strictEqual(activityClass("working").title, "working");
  assert.strictEqual(activityClass("waiting").title, "waiting for input");
  assert.strictEqual(activityClass("done").title, "exited");
});

test("connStateLabel: ended and crashed are the row sub-line words the pane bars mirror (F55/F56)", () => {
  assert.strictEqual(connStateLabel("ended"), "ended");
  assert.strictEqual(connStateLabel("crashed"), "crashed");
});
