"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { activityClass } = require("../static/activity.js");

test("known states map to their class + human title", () => {
  assert.deepEqual(activityClass("working"), { cls: "act-working", title: "working" });
  assert.deepEqual(activityClass("waiting"), { cls: "act-waiting", title: "waiting for input" });
  assert.deepEqual(activityClass("crashed"), { cls: "act-crashed", title: "crashed" });
  assert.deepEqual(activityClass("done"), { cls: "act-done", title: "exited" });
  assert.deepEqual(activityClass("detached"), { cls: "act-detached", title: "reconnecting" });
});

test("unknown/missing activity is muted with no crash", () => {
  for (const v of [undefined, null, "", "bogus"]) {
    assert.equal(activityClass(v).cls, "act-unknown", JSON.stringify(v));
  }
});
