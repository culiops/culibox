"use strict";
// Permission mode in the New session dialog (#342): renderOptions, getOptions, syncCritRmRow and syncSpawnExtras cut out of app.js
// (the page itself, not exported) and run on the fake DOM with the real spawnform.js and profileform.js beside them. Stood in:
// the profile, where the session runs (spawnWhere) and the few page globals the functions call.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const APP = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
function appFn(name) {
  const at = APP.indexOf("function " + name + "(");
  assert.ok(at >= 0, name + " is in app.js");
  return APP.slice(at, APP.indexOf("\n}\n", at) + 2);
}

const MODE = { key: "permission_mode", label: "Permission mode", type: "enum", choices: ["manual", "acceptEdits", "auto", "plan", "bypassPermissions"], value: "" };
const CRIT = { key: "skip_critical_rm_prompt", label: "Skip the critical-rm prompt", type: "bool", value: true, tier: "box" };
const LEGACY = { key: "yolo", label: "Bypass permissions", type: "bool", value: true };

// A page with the profile `profile`, running at `where`; the effort slot and #options as in the dialog.
function page(profile, where) {
  const doc = { createElement: (t) => new Node(t, doc) };
  const els = {};
  for (const id of ["spawn", "options", "effort-slot", "effort-row", "spawn-adv", "spawn-prompt-row", "spawn-replies-row", "spawn-replyable", "spawn-bypass-warn"]) els[id] = new Node("div", doc);
  els.spawn.append(els["effort-slot"], els.options);
  const box = {
    document: doc, console, repliesEnabled: true,
    $: (id) => els[id] || null,
    drawerDesktop: () => true,
    state: { profile, where },
    currentProfile: () => box.state.profile,
    spawnWhere: () => box.state.where,
    updateRuntimeWarning() {},
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["spawnform.js", "profileform.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  // spawnform.js and profileform.js declare with const/function; make the consts reachable from the cut-out functions.
  const fns = ["buildSeg", "isEffortOptionKey", "renderOptions", "getOptions", "optCtrlValue", "dialogOptions", "syncCritRmRow", "syncSpawnExtras"];
  new vm.Script(fns.map(appFn).join("\n"), { filename: "app.js" }).runInContext(ctx);
  els["spawn-replyable"].checked = true;
  const at = (sel) => els.spawn.querySelector(sel);
  return { box, els, ctx, at, run: (code) => new vm.Script(code).runInContext(ctx), doc };
}

const prof = (over) => Object.assign({ name: "claude", claude: true, accepts_prompt: true, options: [MODE] }, over);
const seg = (t) => t.at('[data-key="permission_mode"]');
const segPick = (t, v) => seg(t).querySelectorAll("button").find((b) => b.dataset.segvalue === v).click();
const rowText = (n) => n.parent.textContent;
const crit = (t) => t.at('[data-key="skip_critical_rm_prompt"]');

test("a legacy profile renders Permission mode on Bypass, with no checkbox for the legacy key, and sends the profile's default", () => {
  const t = page(prof({ legacy_bypass_key: "yolo", options: [LEGACY, MODE] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  assert.strictEqual(t.at('[data-key="yolo"]'), null, "the legacy bool has no control");
  assert.strictEqual(t.at("#opt-yolo"), null);
  assert.deepStrictEqual(seg(t).querySelectorAll("button").map((b) => b.textContent), ["Manual", "Accept edits", "Auto", "Plan", "Bypass"]);
  assert.strictEqual(seg(t).dataset.value, "bypassPermissions");
  const text = rowText(seg(t));
  assert.match(text, /Runs every action without asking\./);
  assert.match(text, /Set by Bypass permissions \(older form\)/);
  const o = t.run("getOptions()");
  assert.strictEqual(o.permission_mode, "", "an untouched legacy profile sends nothing: the legacy default applies");
  assert.ok(!("yolo" in o), "the legacy key is never sent");
  // The bypass decisions follow what the seg shows, not what is sent.
  assert.strictEqual(t.run("dialogOptions()").permission_mode, "bypassPermissions");
  
});

test("the replies warning shows for an untouched legacy profile and goes when another mode is picked", () => {
  const t = page(prof({ legacy_bypass_key: "yolo", options: [LEGACY, MODE] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  t.run("syncSpawnExtras(false)");
  assert.strictEqual(t.els["spawn-bypass-warn"].hidden, false);
  segPick(t, "plan");
  assert.strictEqual(t.els["spawn-bypass-warn"].hidden, true);
  assert.strictEqual(t.run("getOptions()").permission_mode, "plan");
  segPick(t, "bypassPermissions");
  assert.strictEqual(t.els["spawn-bypass-warn"].hidden, false);
  assert.strictEqual(t.run("getOptions()").permission_mode, "", "back on the legacy Bypass: nothing sent");
});

test("picking a mode redraws the line under it, and the legacy note goes with Bypass", () => {
  const t = page(prof({ legacy_bypass_key: "yolo", options: [LEGACY, MODE] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  segPick(t, "plan");
  assert.strictEqual(t.run("getOptions()").permission_mode, "plan");
  const text = rowText(seg(t));
  assert.match(text, /Reads and plans only\. Changes nothing until you approve the plan\./);
  assert.doesNotMatch(text, /older form/);
  assert.doesNotMatch(text, /Runs every action/);
});

test("a profile with a stored mode starts on it, and a stored Bypass is sent as Bypass", () => {
  const t = page(prof({ options: [Object.assign({}, MODE, { value: "bypassPermissions" })] }), { runtime: "box", clone: false });
  t.run("renderOptions()");
  assert.strictEqual(seg(t).dataset.value, "bypassPermissions");
  assert.strictEqual(t.run("getOptions()").permission_mode, "bypassPermissions");
  assert.doesNotMatch(rowText(seg(t)), /older form/);
  const u = page(prof({ options: [Object.assign({}, MODE, { value: "plan" })] }), { runtime: "box", clone: false });
  u.run("renderOptions()");
  assert.strictEqual(u.run("getOptions()").permission_mode, "plan");
});

test("a profile whose own args turn Bypass on says so under Permission mode", () => {
  const t = page(prof({ bypass_args: true, options: [Object.assign({}, MODE, { value: "plan" })] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  assert.match(rowText(seg(t)), /This profile's own arguments also turn Bypass on\./);
  const u = page(prof({ options: [Object.assign({}, MODE, { value: "plan" })] }), { runtime: "box", clone: true });
  u.run("renderOptions()");
  assert.doesNotMatch(rowText(seg(u)), /own arguments/);
});

test("a config.json permission_mode def with other choices shows them as written and has no line for them", () => {
  const def = Object.assign({}, MODE, { choices: ["default", "plan", "bypassPermissions"], value: "default" });
  const t = page(prof({ options: [def] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  assert.deepStrictEqual(seg(t).querySelectorAll("button").map((b) => b.textContent), ["default", "Plan", "Bypass"]);
  assert.strictEqual(t.run("getOptions()").permission_mode, "default");
  assert.strictEqual(seg(t).parent.querySelector('[data-line="mode"]').textContent, "");
});

test("the critical-rm row: label, detail, hidden on the host card, and never sent from it", () => {
  const t = page(prof({ options: [MODE, CRIT] }), { runtime: "host", clone: false });
  t.run("renderOptions()");
  assert.ok(crit(t), "the control is built");
  assert.strictEqual(crit(t).parent.hidden, true, "hidden on host");
  assert.ok(!("skip_critical_rm_prompt" in t.run("getOptions()")));
  // ticked, then the Host card picked: the hidden row is not sent
  t.box.state.where = { runtime: "box", clone: true };
  t.run("syncCritRmRow()");
  assert.strictEqual(crit(t).parent.hidden, false);
  crit(t).checked = true;
  assert.strictEqual(t.run("getOptions()").skip_critical_rm_prompt, true);
  t.box.state.where = { runtime: "host", clone: false };
  t.run("syncCritRmRow()");
  assert.strictEqual(crit(t).parent.hidden, true);
  assert.ok(!("skip_critical_rm_prompt" in t.run("getOptions()")));
  // no box layer at all: nothing is sent either
  t.box.state.where = {};
  assert.ok(!("skip_critical_rm_prompt" in t.run("getOptions()")));
  assert.strictEqual(crit(t).parent.querySelector("label").textContent, "Skip the critical-rm prompt");
  assert.match(crit(t).parent.textContent, /In Bypass, Claude Code still stops before an rm of the working folder, ~ or \/, waits 2 minutes, then refuses\. This turns that stop off\./);
});

test("the critical-rm row is dimmed outside Bypass and follows the card: plain on a clone, the warnbox in place", () => {
  const t = page(prof({ options: [Object.assign({}, MODE, { value: "plan" }), CRIT] }), { runtime: "box", clone: true });
  t.run("renderOptions()");
  const line = () => crit(t).parent.querySelector('[data-line="crit"]');
  assert.strictEqual(crit(t).disabled, true);
  assert.strictEqual(line().textContent, "Needs Permission mode: Bypass");
  assert.strictEqual(line().className, "opt-line");
  segPick(t, "bypassPermissions");
  assert.strictEqual(crit(t).disabled, false);
  assert.strictEqual(line().textContent, "The agent works on its own copy of the repo.");
  assert.strictEqual(line().className, "opt-line");
  t.box.state.where = { runtime: "box", clone: false };
  t.run("syncCritRmRow()");
  assert.strictEqual(line().className, "warnbox");
  assert.strictEqual(line().textContent, "Your own folder is mounted live. With this on, the agent can delete it, and everything in it, without asking.");
  segPick(t, "auto");
  assert.strictEqual(crit(t).disabled, true);
  assert.strictEqual(line().className, "opt-line", "dimmed: no warnbox");
});

test("the critical-rm row is enabled for a profile whose own args are Bypass, and for an untouched legacy profile", () => {
  const a = page(prof({ bypass_args: true, options: [Object.assign({}, MODE, { value: "plan" }), CRIT] }), { runtime: "box", clone: true });
  a.run("renderOptions()");
  assert.strictEqual(crit(a).disabled, false);
  const b = page(prof({ legacy_bypass_key: "yolo", options: [LEGACY, MODE, CRIT] }), { runtime: "box", clone: true });
  b.run("renderOptions()");
  assert.strictEqual(crit(b).disabled, false, "the seg reads Bypass");
});

test("another box-only option is hidden and not sent off a box, by its tier alone", () => {
  const other = { key: "exotic", label: "Exotic", type: "bool", value: true, tier: "box" };
  const t = page(prof({ options: [other] }), { runtime: "host", clone: false });
  t.run("renderOptions()");
  assert.strictEqual(t.at('[data-key="exotic"]').parent.hidden, true);
  assert.ok(!("exotic" in t.run("getOptions()")));
  t.box.state.where = { runtime: "box", clone: false };
  t.run("syncCritRmRow()");
  assert.strictEqual(t.at('[data-key="exotic"]').parent.hidden, false);
  assert.strictEqual(t.run("getOptions()").exotic, true);
});

test("app.css hides a hidden option row (the id rule sets display) and the lines are styled", () => {
  const css = fs.readFileSync(path.join(STATIC, "app.css"), "utf8");
  assert.match(css, /#options \.opt\[hidden\]\s*\{\s*display:\s*none/);
  assert.match(css, /#options \.opt-line/);
});
