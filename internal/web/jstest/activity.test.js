"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { activityClass } = require("../static/activity.js");

test("known states map to their class + human title", () => {
  assert.deepEqual(activityClass("working"), { cls: "dot working", title: "working" });
  assert.deepEqual(activityClass("waiting"), { cls: "dot waiting", title: "waiting for input" });
  assert.deepEqual(activityClass("crashed"), { cls: "dot crashed", title: "crashed" });
  assert.deepEqual(activityClass("done"), { cls: "dot done", title: "exited" });
  assert.deepEqual(activityClass("detached"), { cls: "dot detached", title: "reconnecting" });
});

test("unknown/missing activity is muted with no crash", () => {
  for (const v of [undefined, null, "", "bogus"]) {
    assert.equal(activityClass(v).cls, "dot unknown", JSON.stringify(v));
  }
});
