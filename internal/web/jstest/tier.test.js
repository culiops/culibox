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

// --- workspace options -----------------------------------------------------

const { workspaceOptions } = require("../static/tier.js");

const WS = [
  { label: "~ (home)", path: "/home/u", boxable: false, reason: "would mount your whole home" },
  { label: "proj-a", path: "/home/u/proj-a", boxable: true },
  { label: "proj-b", path: "/home/u/proj-b", boxable: true },
];

// Choosing a workspace IS choosing the containment boundary, so an option that
// cannot be contained must be visibly unusable — not silently offered and then
// refused by the server.
test("boxed tier disables the non-boxable entries and says why", () => {
  const opts = workspaceOptions(WS, "box");
  const home = opts.find((o) => o.value === "/home/u");
  assert.equal(home.disabled, true);
  assert.match(home.label, /whole home/);
  assert.equal(opts.find((o) => o.value === "/home/u/proj-a").disabled, false);
});

// On the host tier nothing is contained, so home is a perfectly ordinary choice.
test("host tier leaves every entry selectable", () => {
  const opts = workspaceOptions(WS, "host");
  assert.ok(opts.every((o) => !o.disabled));
  assert.equal(opts.find((o) => o.value === "/home/u").label, "~ (home)");
});

// The default must never land on an option that cannot be used.
test("the default selection is the first usable entry", () => {
  assert.equal(defaultWorkspace(WS, "box"), "/home/u/proj-a");
  assert.equal(defaultWorkspace(WS, "host"), "/home/u");
});

const { defaultWorkspace } = require("../static/tier.js");

test("with nothing boxable the default is empty rather than a doomed pick", () => {
  assert.equal(defaultWorkspace([WS[0]], "box"), "");
});

// --- quick shell (+sh) -----------------------------------------------------

const { shellSpawnPlan } = require("../static/tier.js");

// "+sh" means "a shell right next to this". Its value is the inherited context,
// so it must inherit the WHOLE context — inheriting the directory but not the
// tier is the incoherent middle that produced a refused spawn.
test("a shell beside a boxed session joins that session's tier and directory", () => {
  const p = shellSpawnPlan({ cwd: "/home/u/proj", runtime: "box" });
  assert.deepEqual(p, { mode: "spawn", cwd: "/home/u/proj", runtime: "box" });
});

// Beside an uncontained session in home, a boxed shell would be refused — and
// the honest answer is to match the neighbour, whose tier the user already chose
// deliberately and which the row badge still labels in red.
test("a shell beside a trusted-host session is also trusted-host", () => {
  const p = shellSpawnPlan({ cwd: "/home/u", runtime: "host" });
  assert.deepEqual(p, { mode: "spawn", cwd: "/home/u", runtime: "host" });
});

// With nothing to sit beside, there is no context to inherit, so the quick path
// has no meaning and the deliberate one takes over.
test("with no neighbour the button falls back to the form", () => {
  assert.deepEqual(shellSpawnPlan(null), { mode: "form" });
  assert.deepEqual(shellSpawnPlan({ cwd: "" }), { mode: "form" });
});
