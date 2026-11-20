"use strict";
// #142, the New session half: with no Docker (or no colima) the dialog says so, blocks a box profile's Spawn with
// the reason and keeps the server's containment refusal on its own error line. The decisions are pure
// (spawnBoxGate, spawnReachNeedsBox, spawnBoxRefusalLine, profileTouchLine in spawnpreview.js; the status/code test
// in spawnform.js); syncSpawnGate (spawnpanel.js) and the submit handler (app.js) are cut out of their source and run
// over the shared fake DOM / checked at the source level, since app.js needs a browser.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");
const { profileTouchLine, spawnBoxGate, spawnReachNeedsBox, spawnBoxRefusalLine } = require("../static/spawnpreview.js");
const { spawnRefusedBoxUnavailable } = require("../static/spawnform.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

const boxProfile = { name: "aws-ops", runtime: "box", runtime_asked: "box", bin: "claude", aws_connections: ["staging"] };
const copyProfile = { name: "gitops", runtime: "box", clone_of: "/home/u/infra" };
const ctxProfile = { name: "reader", runtime: "box", context_paths: ["/home/u/docs"] };
const plainProfile = { name: "claude", runtime: "box", bin: "claude" }; // asks for nothing: resolves to box
const hostProfile = { name: "bash", runtime: "host", runtime_asked: "host", bin: "/bin/bash" };
const NONE = { metaLoaded: true, boxRuntime: "", boxUnavailableReason: "Docker is not installed", boxDaemonReason: "" };

test("touch line: a profile that needs a box does not read 'host · not contained' when there is no box layer", () => {
  for (const p of [boxProfile, copyProfile, ctxProfile]) assert.strictEqual(profileTouchLine(p, ""), "needs a box · unavailable", p.name);
  // A profile that asks for nothing really does run on this machine, uncontained; a host one says what it is.
  assert.strictEqual(profileTouchLine(plainProfile, ""), "host · not contained");
  assert.strictEqual(profileTouchLine(hostProfile, ""), "host · bash");
  // With a layer, or before /api/meta answered, nothing changes.
  assert.strictEqual(profileTouchLine(boxProfile, "runc"), "box · aws staging");
  assert.strictEqual(profileTouchLine(boxProfile, undefined), "box · aws staging");
});

test("gate, no box layer: a box profile is blocked with the reason, in the words of the profile page", () => {
  const g = spawnBoxGate({ ...NONE, profile: boxProfile, tier: "" });
  assert.strictEqual(g.show, true);
  assert.strictEqual(g.blocked, true);
  assert.strictEqual(g.tone, "error");
  assert.strictEqual(g.line, "Boxes are unavailable on this machine: Docker is not installed.");
  assert.strictEqual(g.more, "Sessions with this profile can't start until Docker is installed.");
  assert.strictEqual(g.text, g.line + " " + g.more);
  for (const p of [copyProfile, ctxProfile]) assert.strictEqual(spawnBoxGate({ ...NONE, profile: p, tier: "" }).blocked, true, p.name);
  const colima = spawnBoxGate({ ...NONE, boxUnavailableReason: "colima is not installed", profile: boxProfile, tier: "" });
  assert.strictEqual(colima.more, "Sessions with this profile can't start until colima is installed.");
  const doctor = spawnBoxGate({ ...NONE, boxUnavailableReason: "run `culibox doctor` for details", profile: boxProfile, tier: "" });
  assert.strictEqual(doctor.line, "Boxes are unavailable on this machine: run `culibox doctor` for details.");
  assert.strictEqual(doctor.more, "Sessions with this profile can't start until boxes are available.");
  const bare = spawnBoxGate({ ...NONE, boxUnavailableReason: "", profile: boxProfile, tier: "" });
  assert.strictEqual(bare.line, "Boxes are unavailable on this machine.");
});

test("gate, no box layer: a profile that does not need one is not blocked and the line is quiet", () => {
  const host = spawnBoxGate({ ...NONE, profile: hostProfile, tier: "" });
  assert.deepStrictEqual([host.show, host.blocked, host.tone, host.more], [true, false, "quiet", ""]);
  assert.strictEqual(host.line, "Boxes are unavailable on this machine: Docker is not installed.");
  assert.strictEqual(host.text, "");
  const plain = spawnBoxGate({ ...NONE, profile: plainProfile, tier: "" });
  assert.deepStrictEqual([plain.show, plain.blocked, plain.tone], [true, false, "quiet"]);
  assert.strictEqual(plain.more, "Until it is, sessions start on this machine, uncontained.");
  assert.strictEqual(spawnBoxGate({ ...NONE, boxUnavailableReason: "colima is not installed", profile: plainProfile, tier: "" }).more, "Until it is, sessions start on this machine, uncontained.");
});

test("gate: says nothing before /api/meta answers, with no profile, or with boxes working", () => {
  const none = { show: false, tone: "", line: "", more: "", blocked: false, noLayer: false, text: "" };
  assert.deepStrictEqual(spawnBoxGate({ ...NONE, metaLoaded: false, profile: boxProfile, tier: "" }), none);
  assert.deepStrictEqual(spawnBoxGate({ ...NONE, profile: null, tier: "" }), none);
  assert.deepStrictEqual(spawnBoxGate({ metaLoaded: true, boxRuntime: "runc", boxUnavailableReason: "", boxDaemonReason: "", profile: boxProfile, tier: "box" }), none);
  assert.deepStrictEqual(spawnBoxGate(), none);
});

test("gate, layer present but its daemon unusable (#154): the boxed tier is blocked for the two named reasons only", () => {
  const live = { metaLoaded: true, boxRuntime: "runc", boxUnavailableReason: "", profile: boxProfile };
  for (const r of ["Docker is not running", "no permission to use Docker"]) {
    const g = spawnBoxGate({ ...live, boxDaemonReason: r, tier: "box" });
    assert.deepStrictEqual([g.show, g.blocked, g.tone, g.noLayer], [true, true, "error", false], r);
    assert.strictEqual(g.line, "Boxes can't start right now: " + r + ".");
    assert.strictEqual(g.more, "Sessions with this profile can't start until that is fixed.");
  }
  // The doctor pointer is not a refusal: the spawn's own docker calls may still work. It warns and lets it through.
  const maybe = spawnBoxGate({ ...live, boxDaemonReason: "run `culibox doctor` for details", tier: "box" });
  assert.deepStrictEqual([maybe.show, maybe.blocked, maybe.tone], [true, false, "error"]);
  assert.strictEqual(maybe.more, "Sessions with this profile may fail to start.");
  // The user picked host for this run: nothing is wrong with it. No tier shown yet counts as the boxed default.
  assert.strictEqual(spawnBoxGate({ ...live, boxDaemonReason: "Docker is not running", tier: "host" }).show, false);
  assert.strictEqual(spawnBoxGate({ ...live, boxDaemonReason: "Docker is not running", tier: "" }).blocked, true);
  // No daemon reason: no line.
  assert.strictEqual(spawnBoxGate({ ...live, boxDaemonReason: "", tier: "box" }).show, false);
});

test("reach box for a box profile with no boxes: says it needs one and why, never 'whole user account' or 'not contained'", () => {
  const rows = spawnReachNeedsBox("Docker is not installed");
  assert.deepStrictEqual(rows.map((r) => r.k), ["Needs", "Reach"]);
  assert.strictEqual(rows[0].v, "A box (unavailable: Docker is not installed)");
  assert.strictEqual(rows[0].tone, "warn");
  assert.strictEqual(rows[1].v, "Nothing: it can't start without a box");
  assert.strictEqual(spawnReachNeedsBox("")[0].v, "A box (unavailable)");
  assert.ok(!/whole user|not contained/i.test(JSON.stringify(rows)));
});

test("refusal line (desktop dialog): plain words from what the page knows, the server's sentence otherwise", () => {
  const msg = 'containment unavailable: this session asks for runtime box, and this instance has no box layer (see the boot log); set the profile\'s tier to host (or send runtime "host") or fix containment';
  const noLayer = spawnBoxRefusalLine({ ...NONE, message: msg });
  assert.strictEqual(noLayer, "Boxes are unavailable on this machine: Docker is not installed. Sessions with this profile can't start until Docker is installed.");
  assert.ok(!/runtime|tier|boot log/.test(noLayer), "no API words");
  assert.strictEqual(spawnBoxRefusalLine({ ...NONE, boxUnavailableReason: "", message: msg }), "Boxes are unavailable on this machine. Sessions with this profile can't start until boxes are available.");
  // A layer whose daemon went away since the page loaded: the page knows the reason.
  assert.strictEqual(spawnBoxRefusalLine({ metaLoaded: true, boxRuntime: "runc", boxDaemonReason: "Docker is not running", message: msg }), "Boxes can't start right now: Docker is not running.");
  // Any other containment refusal (egress down, clone failed, SSO check) is already a plain, actionable sentence.
  const other = "containment unavailable: egress control is not running, so no box can start";
  assert.strictEqual(spawnBoxRefusalLine({ metaLoaded: true, boxRuntime: "runc", boxDaemonReason: "", message: other }), other);
  assert.strictEqual(spawnBoxRefusalLine({ metaLoaded: false, boxRuntime: "", boxUnavailableReason: "", message: other }), other, "unknown before /api/meta answers: the server's words");
  // The doctor pointer is vaguer than what the server said: the server's sentence stays.
  assert.strictEqual(spawnBoxRefusalLine({ metaLoaded: true, boxRuntime: "runc", boxDaemonReason: "run `culibox doctor` for details", message: other }), other);
  assert.strictEqual(spawnBoxRefusalLine({ metaLoaded: true, boxRuntime: "runc", boxDaemonReason: "no permission to use Docker", message: other }), "Boxes can't start right now: no permission to use Docker.");
});

test("spawnRefusedBoxUnavailable: only a 400 carrying the box_unavailable code", () => {
  assert.strictEqual(spawnRefusedBoxUnavailable(400, "box_unavailable"), true);
  assert.strictEqual(spawnRefusedBoxUnavailable(400, ""), false);
  assert.strictEqual(spawnRefusedBoxUnavailable(503, "box_unavailable"), false);
  assert.strictEqual(spawnRefusedBoxUnavailable(400, "not_shared"), false);
  assert.strictEqual(spawnRefusedBoxUnavailable(503, "vm_restarting"), false);
  assert.strictEqual(spawnRefusedBoxUnavailable(undefined, undefined), false);
});

// ---- syncSpawnGate over a fake DOM -----------------------------------------------------------------------------

function fnSource(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

function dialog(over) {
  const doc = {};
  const mk = (t) => new Node(t, doc);
  const nobox = mk("div"); nobox.hidden = true;
  const go = mk("button");
  const err = mk("div");
  const els = { "spawn-nobox": nobox, "spawn-err": err, "spawn-go": go };
  const ctx = vm.createContext({
    document: { createElement: mk }, console,
    metaLoaded: true, boxRuntime: "", boxUnavailableReason: "Docker is not installed", boxDaemonReason: "", spawnBusy: false, spawnGateErr: "",
    desktop: true, profile: boxProfile, tier: "",
    drawerDesktop: () => ctx.desktop,
    currentProfile: () => ctx.profile,
    segValue: () => ctx.tier,
    spawnWhere: () => ({ runtime: ctx.tier }),
    $: (id) => els[id] || (id === "spawn" ? { querySelector: () => go } : null),
    setDialogError: (_id, msg) => { err.textContent = msg || ""; },
    ...over,
  });
  vm.runInContext(read("credentials.js").match(/function setDisabledReason[\s\S]*?\n}\n/)[0], ctx);
  vm.runInContext(["spawnNeedsBox", "spawnBoxGate", "spawnReachNeedsBox", "spawnBoxRefusalLine"].map((n) => fnSource(read("spawnpreview.js"), n)).join("\n"), ctx);
  ctx.spawnDep = (name, file) => require(path.join(STATIC, file))[name]; // the browser reads the page global
  vm.runInContext(["spawnPanelOn", "spawnPanelEl", "spawnGateInput", "syncSpawnGate"].map((n) => fnSource(read("spawnpanel.js"), n)).join("\n"), ctx);
  return { ctx, nobox, go, err };
}
const all = (n, out = []) => { out.push(n); for (const c of n.children) all(c, out); return out; };

test("syncSpawnGate: no boxes + a box profile draws the red block and marks Spawn blocked with the reason", () => {
  const { ctx, nobox, go } = dialog();
  const g = ctx.syncSpawnGate();
  assert.strictEqual(g.blocked, true);
  assert.strictEqual(nobox.hidden, false);
  assert.strictEqual(nobox.textContent, "Boxes are unavailable on this machine: Docker is not installed.Sessions with this profile can't start until Docker is installed.");
  assert.ok(nobox.className.split(/\s+/).includes("err"));
  const kids = nobox.children.map((c) => c.className);
  assert.deepStrictEqual(kids, ["nb-line", "nb-more"]);
  assert.strictEqual(go.attrs["aria-disabled"], "true");
  assert.strictEqual(go.attrs.title, g.text);
  assert.strictEqual(go.disabled, false, "aria-disabled keeps the reason hoverable");
});

test("syncSpawnGate: a host profile gets the quiet line and a live Spawn; boxes working clears everything", () => {
  const { ctx, nobox, go } = dialog({ profile: hostProfile });
  const g = ctx.syncSpawnGate();
  assert.strictEqual(g.blocked, false);
  assert.strictEqual(nobox.hidden, false);
  assert.ok(!nobox.className.split(/\s+/).includes("err"));
  assert.strictEqual(go.attrs["aria-disabled"], undefined);
  assert.strictEqual(go.attrs.title, undefined);
  ctx.boxRuntime = "runc"; ctx.boxUnavailableReason = ""; ctx.profile = boxProfile; ctx.tier = "box";
  ctx.syncSpawnGate();
  assert.strictEqual(nobox.hidden, true);
  assert.strictEqual(nobox.textContent, "");
  assert.strictEqual(go.attrs["aria-disabled"], undefined);
});

test("syncSpawnGate: the phone is untouched (hidden block, live Spawn), and an in-flight Spawn is not re-enabled", () => {
  const { ctx, nobox, go } = dialog({ desktop: false });
  assert.strictEqual(ctx.syncSpawnGate().blocked, false);
  assert.strictEqual(nobox.hidden, true);
  assert.strictEqual(go.attrs["aria-disabled"], undefined);
  const busy = dialog({ spawnBusy: true });
  busy.go.disabled = true;
  busy.ctx.syncSpawnGate();
  assert.strictEqual(busy.go.disabled, true, "setDisabledReason would have cleared the in-flight disable");
});

test("syncSpawnGate: the blocked-submit message leaves the error line when the profile no longer needs one", () => {
  const { ctx, err } = dialog();
  const g = ctx.syncSpawnGate();
  ctx.spawnGateErr = g.text; err.textContent = g.text; // what a blocked submit writes (app.js)
  ctx.syncSpawnGate();
  assert.strictEqual(err.textContent, g.text, "still blocked: the line stays");
  ctx.profile = hostProfile;
  ctx.syncSpawnGate();
  assert.strictEqual(err.textContent, "", "switched to a host profile: the stale refusal is gone");
  err.textContent = "Could not refresh profiles: x"; // another note is not ours to clear
  ctx.syncSpawnGate();
  assert.strictEqual(err.textContent, "Could not refresh profiles: x");
});

// ---- the submit handler and the other call sites (app.js needs a browser: source-level) ----------------------

const APP = read("app.js");
const HTML = read("index.html");
const submit = APP.slice(APP.indexOf('$("spawn").addEventListener("submit"'), APP.indexOf("// The row's actions menu."));

test("submit: a blocked Spawn never posts and says why; the containment refusal stays in the dialog on the desktop only", () => {
  assert.ok(submit.length > 500, "submit handler found");
  const gate = submit.indexOf("syncSpawnGate()");
  const busy = submit.indexOf("spawnBusy = true", submit.indexOf("return;", submit.indexOf("setDialogError(\"spawn-modal\", gate.text)"))); // the one that starts the POST
  assert.ok(gate > 0 && gate < busy, "the gate is read before anything is sent");
  assert.match(submit.slice(gate, busy), /if \(gate\.blocked\)[\s\S]*setDialogError\("spawn-modal", gate\.text\)[\s\S]*return;/);
  const branch = submit.indexOf("spawnRefusedBoxUnavailable(e.status, e.code)");
  const toast = submit.indexOf('showToast("Spawn failed: "');
  assert.ok(branch > 0 && branch < toast, "the dialog branch comes before the toast fallback");
  assert.match(submit.slice(branch, branch + 200), /drawerDesktop\(\)/, "the phone keeps its toast (#142 criterion 9)");
  const arm = submit.slice(branch, submit.indexOf("} else {", branch));
  assert.match(arm, /spawnUnavailMsg = spawnBoxRefusalLine\(/);
  assert.match(arm, /setDialogError\("spawn-modal", spawnUnavailMsg\)/, "the stale line is cleared on the next try like the Keychain one");
  assert.ok(!arm.includes("closeSpawn()") && !arm.includes("showToast"), "the dialog and its form (name, first message, workspace) stay as they were");
  // A daemon-down block is not final: the meta is re-read first, and only a block that survives it refuses (a stale
  // "Docker is not running" must not trap the user once Docker is back). No layer is not re-read: boot decided that.
  const first = submit.indexOf("let gate = syncSpawnGate();");
  assert.ok(first > 0 && first < busy);
  const retry = submit.slice(first, submit.indexOf("if (gate.blocked) {", first + 40));
  assert.match(retry, /if \(gate\.blocked && !gate\.noLayer\) \{[\s\S]*spawnBusy = true;[\s\S]*await refreshBoxMeta\(\)[\s\S]*spawnBusy = false;[\s\S]*gate = syncSpawnGate\(\);/);
  // Re-synced once the button is restored, so a blocked profile is not left looking live.
  assert.match(submit.slice(submit.indexOf("} finally {")), /syncSpawnGate\(\)/);
});

test("the dialog has the block, above the fields, and the no-layer red 'cannot be boxed' warning is not drawn on the desktop", () => {
  assert.match(HTML, /<div class="span2" id="spawn-nobox" role="status" hidden><\/div>/);
  assert.ok(HTML.indexOf('id="spawn-nobox"') < HTML.indexOf('id="spawn-ws-row"'));
  const warn = fnSource(APP, "updateRuntimeWarning");
  assert.match(warn, /drawerDesktop\(\) && metaLoaded && !boxRuntime[\s\S]*warn\.hidden = true;[\s\S]*return;/);
  assert.ok(warn.indexOf("drawerDesktop() && metaLoaded && !boxRuntime") < warn.indexOf("cannot be boxed"));
  const panel = fnSource(read("spawnpanel.js"), "renderSpawnReach");
  assert.match(panel, /spawnReachNeedsBox\(/);
  assert.match(fnSource(read("spawnpanel.js"), "syncSpawnPreview"), /syncSpawnGate\(\)/);
});

test("opening the dialog re-reads the box fields of /api/meta, in parallel with the profiles, so a page loaded while Docker was down does not keep blocking", () => {
  const open = fnSource(APP, "openSpawnDialog");
  const start = open.indexOf("refreshBoxMeta()"), profiles = open.indexOf("await loadProfiles(false)"), join = open.indexOf("await metaRead");
  assert.ok(start > 0 && start < profiles && profiles < join && join < open.indexOf("syncRuntimeField()"), "started first, joined before the tier and the gate are derived");
  const fn = fnSource(APP, "refreshBoxMeta");
  assert.match(fn, /deadlineSignal\(5000\)/, "a stalled answer must not hang the open");
  for (const k of ["box_runtime", "box_unavailable_reason", "box_daemon_reason"]) assert.ok(fn.includes("meta." + k), k);
  assert.match(fn, /catch \(_\) \{/, "a failed read keeps what the page has");
  assert.match(fn, /if \(!metaLoaded\) return;/, "before the page's own first read there is nothing to refresh");
  assert.ok(!/metaLoaded\s*=/.test(fn), "it never marks the meta loaded, which would claim fields it did not read");
});

test("Docker back since the dialog opened: the same profile is no longer blocked once the box fields are re-read", () => {
  const { ctx, go } = dialog({ boxRuntime: "runc", boxUnavailableReason: "", boxDaemonReason: "Docker is not running", tier: "box" });
  const g = ctx.syncSpawnGate();
  assert.deepStrictEqual([g.blocked, g.noLayer], [true, false], "blocked from the stale read, and it is the daemon case the submit re-reads");
  ctx.boxDaemonReason = ""; // what refreshBoxMeta stores once /api/meta says it is healthy
  assert.strictEqual(ctx.syncSpawnGate().blocked, false, "so the request is sent");
  assert.strictEqual(go.attrs["aria-disabled"], undefined);
});
