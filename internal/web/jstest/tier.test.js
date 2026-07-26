"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { tierBadge, tierRuntimeLabel } = require("../static/tier.js");

test("a boxed session names the detected isolation runtime", () => {
  const b = tierBadge({ runtime: "box" }, "runc");
  assert.equal(b.text, "boxed · runc");
  assert.equal(b.tone, "neutral");
});

test("sysbox is shown by its short name", () => {
  assert.equal(tierBadge({ runtime: "box" }, "sysbox-runc").text, "boxed · sysbox");
  assert.equal(tierRuntimeLabel("sysbox-runc"), "sysbox");
});

test("a host session reads as trusted-host in the danger tone", () => {
  const b = tierBadge({ runtime: "host" }, "runc");
  assert.equal(b.text, "trusted-host");
  assert.equal(b.tone, "danger");
});

// The badge is a security signal. Anything it is not sure about must read as
// uncontained — claiming containment a session lacks is the dangerous direction
// to be wrong in.
test("an unknown or missing tier never claims containment", () => {
  for (const s of [{}, { runtime: "" }, { runtime: "vm" }, { runtime: undefined }]) {
    assert.equal(tierBadge(s, "runc").text, "trusted-host", JSON.stringify(s));
  }
});

test("no badge for an exited session or a missing session", () => {
  assert.equal(tierBadge({ runtime: "box", exited: true }, "runc"), null);
  assert.equal(tierBadge(null, "runc"), null);
});
