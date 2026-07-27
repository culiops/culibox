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

// --- spawn menu entries ----------------------------------------------------

const { spawnMenuEntries } = require("../static/tier.js");

test("without containment there is no trusted-host section at all", () => {
  const e = spawnMenuEntries([{ name: "bash" }, { name: "claude" }], "");
  assert.deepEqual(e.filter((x) => x.runtime === "host"), []);
  assert.equal(e.length, 2);
});

// The whole point of the escape hatch is running a SPECIFIC agent with host
// access (claude needing aws/ssh). One generic entry that silently reuses the
// default profile cannot express that.
test("every profile gets its own trusted-host entry", () => {
  const e = spawnMenuEntries([{ name: "bash" }, { name: "claude" }, { name: "zsh" }], "runc");
  const host = e.filter((x) => x.runtime === "host");
  assert.deepEqual(host.map((x) => x.agent), ["bash", "claude", "zsh"]);
  assert.ok(host.every((x) => x.tone === "danger"), "host entries must carry the danger tone");
});

test("boxed entries name their agent explicitly, never an implicit default", () => {
  const e = spawnMenuEntries([{ name: "bash" }, { name: "claude" }], "runc");
  const boxed = e.filter((x) => x.runtime === "box");
  assert.deepEqual(boxed.map((x) => x.agent), ["bash", "claude"]);
  assert.ok(boxed.every((x) => x.agent), "a menu entry must always carry its own agent");
});

test("the trusted-host group is introduced by a heading", () => {
  const e = spawnMenuEntries([{ name: "bash" }], "runc");
  const head = e.find((x) => x.kind === "heading");
  assert.ok(head, "expected a heading before the uncontained entries");
  assert.match(head.text, /trusted host/i);
});

test("no profiles yields no entries rather than a phantom host section", () => {
  assert.deepEqual(spawnMenuEntries([], "runc"), []);
});
