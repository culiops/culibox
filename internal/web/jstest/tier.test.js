"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { tierBadge, tierRuntimeLabel } = require("../static/tier.js");

test("a boxed session reads as boxed, keeping the row narrow", () => {
  const b = tierBadge({ runtime: "box" }, "runc");
  assert.equal(b.text, "boxed");
  assert.equal(b.tone, "neutral");
});

// The runtime is instance-wide, so it belongs in the tooltip rather than eating
// sidebar width and wrapping the session name.
test("the isolation runtime is carried in the tooltip, not the label", () => {
  assert.match(tierBadge({ runtime: "box" }, "sysbox-runc").title, /sysbox/);
  assert.equal(tierBadge({ runtime: "box" }, "sysbox-runc").text, "boxed");
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
