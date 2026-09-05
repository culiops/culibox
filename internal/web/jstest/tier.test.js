"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { tierBadge, tierRuntimeLabel, spawnRuntimeDefault } = require("../static/tier.js");

// --- which tier the New-session form opens on ------------------------------
// The form used to open on "box" unconditionally and always send that tier, so a
// profile saved as host — and even an instance whose default_runtime is host —
// was overridden on every spawn.

test("the form opens on the tier the selected profile declares", () => {
  assert.equal(spawnRuntimeDefault({ runtime: "host" }, "runc"), "host");
  assert.equal(spawnRuntimeDefault({ runtime: "box" }, "runc"), "box");
});

// Same fail-loud posture as the badge: anything unrecognised resolves to the
// contained tier, never to host.
test("an unknown or missing profile tier falls back to box", () => {
  for (const p of [null, undefined, {}, { runtime: "" }, { runtime: "vm" }]) {
    assert.equal(spawnRuntimeDefault(p, "runc"), "box", JSON.stringify(p));
  }
});

// With no box layer the tier row is hidden and no tier is sent, so the server
// keeps resolving it from the profile.
test("no tier is preselected when containment is unavailable", () => {
  assert.equal(spawnRuntimeDefault({ runtime: "host" }, ""), "");
  assert.equal(spawnRuntimeDefault({ runtime: "box" }, ""), "");
});

test("a boxed session reads as box, keeping the row narrow", () => {
  const b = tierBadge({ runtime: "box" }, "runc");
  assert.equal(b.text, "box");
  assert.equal(b.tone, "neutral");
});

// The runtime is instance-wide, so it belongs in the tooltip rather than eating
// sidebar width and wrapping the session name.
test("the isolation runtime is carried in the tooltip, not the label", () => {
  assert.match(tierBadge({ runtime: "box" }, "sysbox-runc").title, /sysbox/);
  assert.equal(tierBadge({ runtime: "box" }, "sysbox-runc").text, "box");
  assert.equal(tierRuntimeLabel("sysbox-runc"), "sysbox");
});

test("a host session reads as host in the danger tone", () => {
  const b = tierBadge({ runtime: "host" }, "runc");
  assert.equal(b.text, "host");
  assert.equal(b.tone, "danger");
});

// The badge is a security signal. Anything it is not sure about must read as
// uncontained — claiming containment a session lacks is the dangerous direction
// to be wrong in.
test("an unknown or missing tier never claims containment", () => {
  for (const s of [{}, { runtime: "" }, { runtime: "vm" }, { runtime: undefined }]) {
    assert.equal(tierBadge(s, "runc").text, "host", JSON.stringify(s));
  }
});

test("no badge for an exited session or a missing session", () => {
  assert.equal(tierBadge({ runtime: "box", exited: true }, "runc"), null);
  assert.equal(tierBadge(null, "runc"), null);
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

// --- workspace picker initialization via default_cwd -------------------------

const { workspaceSelectionForCwd } = require("../static/tier.js");

test("empty/blank cwd leaves the picker untouched", () => {
  assert.equal(workspaceSelectionForCwd(WS, "box", ""), null);
  assert.equal(workspaceSelectionForCwd(WS, "box", "   "), null);
  assert.equal(workspaceSelectionForCwd(WS, "box", undefined), null);
});

test("a path matching a usable option selects it, no custom", () => {
  assert.deepEqual(workspaceSelectionForCwd(WS, "box", "/home/u/proj-a"), {
    value: "/home/u/proj-a",
    custom: "",
  });
});

test("a path not offered on this tier falls to custom", () => {
  assert.deepEqual(workspaceSelectionForCwd(WS, "box", "/home/u/other"), {
    value: "__custom__",
    custom: "/home/u/other",
  });
  // home is present but disabled on the box tier → not a usable match → custom.
  assert.deepEqual(workspaceSelectionForCwd(WS, "box", "/home/u"), {
    value: "__custom__",
    custom: "/home/u",
  });
  // on the host tier home IS usable → selects it.
  assert.deepEqual(workspaceSelectionForCwd(WS, "host", "/home/u"), {
    value: "/home/u",
    custom: "",
  });
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
// deliberately and which the row badge still labels host.
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

// --- profile editor working-dir picker (pfCwdValue) -------------------------

const { pfCwdValue } = require("../static/tier.js");

test("pfCwdValue: (no default) option yields empty", () => {
  assert.equal(pfCwdValue("", "/ignored"), "");
});
test("pfCwdValue: custom option yields the trimmed typed path", () => {
  assert.equal(pfCwdValue("__custom__", "  /home/u/proj  "), "/home/u/proj");
  assert.equal(pfCwdValue("__custom__", ""), "");
});
test("pfCwdValue: a selected root yields that root", () => {
  assert.equal(pfCwdValue("/home/u/proj-a", "whatever"), "/home/u/proj-a");
});

// --- spawn dialog: segmented Profile vs. dropdown fallback (D5) -------------

const { profileFitsAsSeg } = require("../static/tier.js");

test("profileFitsAsSeg: a handful of profiles renders as a seg", () => {
  assert.equal(profileFitsAsSeg(1), true);
  assert.equal(profileFitsAsSeg(4), true);
});

test("profileFitsAsSeg: past the threshold falls back to the plain select", () => {
  assert.equal(profileFitsAsSeg(5), false);
  assert.equal(profileFitsAsSeg(12), false);
});

test("profileFitsAsSeg: zero profiles is also the select (nothing to segment)", () => {
  assert.equal(profileFitsAsSeg(0), false);
});
