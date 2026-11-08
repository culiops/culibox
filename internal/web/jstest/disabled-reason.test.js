"use strict";
// A disabled button says why (F03, frame M5.6, #173 Plan 3 Task 8), desktop only. The Add AWS accounts foot's
// "Check all" and "Save all" stay enabled-in-the-DOM on desktop but aria-disabled, so a hover shows the reason in
// the native title and Tab can reach them; a click or Enter/Space on one does nothing. The phone keeps the real
// `disabled` attribute and no title. The pure pieces (the reason text, the attribute writer) run under node; the
// foot itself runs against a minimal fake DOM (the repo has no jsdom, see credentials-dom.test.js), once as the
// profile page's drawer foot and once as the Settings page's static foot.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { awsFootNote, awsFootReasons, setDisabledReason, awsRowStateFor: rowState } = require("../static/credentials.js");

const STATIC = path.join(__dirname, "..", "static");
const CHECK_REASON = "Nothing to check yet: tick a profile above";
const SAVE_REASON = "Nothing ready to save yet";
const WAIT_ONE = "1 row is waiting for “I understand”.";
const CHECK_ROLE = "Check the new role before saving.";
const CHECK_PS = "Check that the permission set is read-only before saving.";

// --- the pure pieces ------------------------------------------------------------------------------

const ready = { assume: "ok", verdict: "read_only" };
const waiting = { assume: "ok", verdict: "writes", acked: false };
const unchecked = { assume: null };

test("awsFootReasons: no rows, nothing ticked yet", () => {
  assert.deepStrictEqual(awsFootReasons({ rows: [], saving: false }), { check: CHECK_REASON, save: SAVE_REASON });
});

test("awsFootReasons: rows but none ready, Check all is live and Save says nothing is ready", () => {
  assert.deepStrictEqual(awsFootReasons({ rows: [unchecked, { assume: "error" }], saving: false }), { check: "", save: SAVE_REASON });
});

test("awsFootReasons: a row waiting for 'I understand' beside a ready one gives Save today's waiting text", () => {
  assert.deepStrictEqual(awsFootReasons({ rows: [ready, waiting], saving: false }), { check: "", save: WAIT_ONE });
  assert.strictEqual(awsFootReasons({ rows: [ready, waiting, waiting], saving: false }).save, "2 rows are waiting for “I understand”.");
});

test("awsFootReasons: one row waiting for 'I understand' and none ready says so, as the note does", () => {
  const f = { rows: [waiting], saving: false };
  assert.strictEqual(awsFootReasons(f).save, WAIT_ONE);
  assert.strictEqual(awsFootReasons(f).save, awsFootNote(f, f.rows.map(rowState)), "the note's own text");
  assert.strictEqual(awsFootReasons({ rows: [waiting, unchecked], saving: false }).save, WAIT_ONE);
});

test("awsFootReasons: the add view with rows but none ready and none waiting keeps 'Nothing ready to save yet'", () => {
  const f = { rows: [unchecked, unchecked], saving: false };
  assert.strictEqual(awsFootNote(f, f.rows.map(rowState)), "0 of 2 ready to save.", "the note is the count, not a reason");
  assert.strictEqual(awsFootReasons(f).save, SAVE_REASON);
});

test("awsFootReasons: all ready and saving say nothing", () => {
  assert.deepStrictEqual(awsFootReasons({ rows: [ready, ready], saving: false }), { check: "", save: "" });
  assert.deepStrictEqual(awsFootReasons({ rows: [ready], saving: true }), { check: "", save: "" });
  assert.deepStrictEqual(awsFootReasons({ rows: [unchecked], saving: true }), { check: "", save: "" });
  assert.deepStrictEqual(awsFootReasons({ rows: [waiting], saving: true }), { check: "", save: "" }, "a save in flight is plain, whatever else is waiting");
});

// An edit is one connection: before its check the note says what to do, and the Save title says the same.
const unchangedEdit = { orig: { source_profile: "base", role_arn: "arn:r", mode: "" }, source_profile: "base", role_arn: "arn:r", mode: "" };

test("awsFootReasons in edit mode: before a check the Save title is the foot note (role)", () => {
  const f = { mode: "edit", rows: [{ assume: null }], saving: false };
  assert.strictEqual(awsFootNote(f, f.rows.map(rowState)), CHECK_ROLE);
  assert.deepStrictEqual(awsFootReasons(f), { check: "", save: CHECK_ROLE });
});

test("awsFootReasons in edit mode: an sso_direct connection reads the permission-set note", () => {
  const f = { mode: "edit", rows: [{ mode: "sso_direct", gate: null }], saving: false };
  assert.deepStrictEqual(awsFootReasons(f), { check: "", save: CHECK_PS });
  const g = { mode: "edit", rows: [{ mode: "sso_direct", gate: { state: "read_only" } }], saving: false };
  assert.strictEqual(awsFootReasons(g).save, "", "a read-only gate can save");
});

test("awsFootReasons in edit mode: a row waiting for 'I understand' reads the waiting text, a checked or unchanged one nothing", () => {
  assert.strictEqual(awsFootReasons({ mode: "edit", rows: [waiting], saving: false }).save, WAIT_ONE);
  assert.strictEqual(awsFootReasons({ mode: "edit", rows: [ready], saving: false }).save, "");
  assert.strictEqual(awsFootReasons({ mode: "edit", rows: [unchangedEdit], saving: false }).save, "", "an edit that leaves the role alone needs no check");
});

test("awsFootReasons in edit mode: saving says nothing", () => {
  assert.deepStrictEqual(awsFootReasons({ mode: "edit", rows: [{ assume: null }], saving: true }), { check: "", save: "" });
});

test("awsFootReasons takes the row states from a caller that has them, and works them out when it has none", () => {
  const f = { rows: [ready, waiting], saving: false };
  const states = f.rows.map(rowState);
  assert.deepStrictEqual(awsFootReasons(f, states), awsFootReasons(f));
});

// An element with the three things setDisabledReason may touch.
function el(disabled) {
  const attrs = {};
  return {
    disabled: !!disabled, attrs,
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return k in attrs ? attrs[k] : null; },
    removeAttribute(k) { delete attrs[k]; },
  };
}

test("setDisabledReason on desktop: aria-disabled and the title, never the disabled attribute", () => {
  const b = el();
  setDisabledReason(b, true, "Nothing ready to save yet", true);
  assert.strictEqual(b.getAttribute("aria-disabled"), "true");
  assert.strictEqual(b.getAttribute("title"), "Nothing ready to save yet");
  assert.strictEqual(b.disabled, false, "a disabled button gets no hover and no focus: this one stays reachable");
});

test("setDisabledReason on desktop: on again removes both, and an old phone disabled is lifted", () => {
  const b = el(true);
  setDisabledReason(b, true, "why", true);
  assert.strictEqual(b.disabled, false, "a button the phone layout disabled is made reachable on the next desktop paint");
  setDisabledReason(b, false, "why", true);
  assert.strictEqual(b.getAttribute("aria-disabled"), null);
  assert.strictEqual(b.getAttribute("title"), null);
  assert.strictEqual(b.disabled, false);
});

test("setDisabledReason on desktop: off without a reason is aria-disabled with no title (a save in flight)", () => {
  const b = el();
  setDisabledReason(b, true, "old reason", true);
  setDisabledReason(b, true, "", true);
  assert.strictEqual(b.getAttribute("aria-disabled"), "true");
  assert.strictEqual(b.getAttribute("title"), null, "a stale reason is not left behind");
});

test("setDisabledReason on the phone: the disabled attribute only, as before, no aria-disabled and no title", () => {
  const b = el();
  setDisabledReason(b, true, "Nothing ready to save yet", false);
  assert.strictEqual(b.disabled, true);
  assert.strictEqual(b.getAttribute("aria-disabled"), null);
  assert.strictEqual(b.getAttribute("title"), null);
  setDisabledReason(b, false, "x", false);
  assert.strictEqual(b.disabled, false);
});

test("setDisabledReason on the phone clears what a desktop paint left (a window narrowed across 700px)", () => {
  const b = el();
  setDisabledReason(b, true, "why", true);
  setDisabledReason(b, true, "why", false);
  assert.strictEqual(b.disabled, true);
  assert.strictEqual(b.getAttribute("aria-disabled"), null);
  assert.strictEqual(b.getAttribute("title"), null);
});

// --- the foot, in a fake DOM ----------------------------------------------------------------------

// "tag" or ".cls" or "tag.cls": every selector credentials.js asks this DOM for.
function matcher(sel) {
  const m = /^([a-z0-9]*)(?:\.([\w-]+))?$/i.exec(sel);
  assert.ok(m, "the fake DOM does not support the selector " + sel);
  return (n) => (!m[1] || n.tag === m[1]) && (!m[2] || n.className.split(/\s+/).includes(m[2]));
}
class Text { constructor(t) { this.text = t; this.parent = null; } get textContent() { return this.text; } }
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.disabled = false; this.colSpan = 1;
    const self = this;
    this.classList = {
      contains: (c) => self.className.split(/\s+/).includes(c),
      add: (c) => { if (!self.className.split(/\s+/).includes(c)) self.className = (self.className + " " + c).trim(); },
      remove: (c) => { self.className = self.className.split(/\s+/).filter((x) => x && x !== c).join(" "); },
      toggle: (c, on) => { const has = self.className.split(/\s+/).includes(c); if (on === undefined ? !has : on) self.classList.add(c); else if (has) self.classList.remove(c); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  get parentElement() { return this.parent; }
  get firstChild() { return this.children[0] || null; }
  get lastChild() { return this.children[this.children.length - 1] || null; }
  get firstElementChild() { return this.children.find((c) => c instanceof Node) || null; }
  get rows() { return this._rows !== undefined ? this._rows : this.children.filter((c) => c.tag === "tr"); }
  set rows(v) { this._rows = v; }
  get cells() { return this.children.filter((c) => c.tag === "td" || c.tag === "th"); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener() {}
  // A browser's click(): a disabled button gets none, an aria-disabled one does (that is the point of the test).
  click() { if (!this.disabled && this.onclick) return this.onclick({ type: "click" }); return undefined; }
  appendChild(c) { if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  remove() { if (this.parent) { this.parent.children.splice(this.parent.children.indexOf(this), 1); this.parent = null; } }
  closest(sel) { const hit = matcher(sel); for (let n = this; n; n = n.parent) if (n instanceof Node && hit(n)) return n; return null; }
  querySelectorAll(sel) {
    const hit = matcher(sel), out = [];
    const walk = (n) => { for (const c of n.children) { if (!(c instanceof Node)) continue; if (hit(c)) out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

// Enter or Space on a focused <button>, as a browser does it: the key handlers run first, then (unless one
// prevented the default) the press is a click, which a `disabled` button never gets.
function press(btn, key) {
  const ev = { key, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  if (btn.onkeydown) btn.onkeydown(ev);
  if (!ev.defaultPrevented && (key === "Enter" || key === " ")) btn.click();
}

const hostProfiles = [
  { name: "acme-dev", kind: "assume-role", role_arn: "arn:aws:iam::111111111111:role/agentbox-dev", source_profile: "base", region: "eu-west-1", selectable: true },
  { name: "acme-ops", kind: "assume-role", role_arn: "arn:aws:iam::222222222222:role/agentbox-ops", source_profile: "base", region: "eu-west-1", selectable: true },
];

// One page. `width` is window.innerWidth (390 = the phone); `statics` adds the Settings page's own foot buttons.
function page({ width = 1280, statics = false } = {}) {
  const calls = [];
  const byId = {};
  if (statics) {
    for (const id of ["foot-creds", "foot-creds-add"]) byId[id] = Object.assign(new Node("div"), { hidden: id !== "foot-creds" });
    for (const id of ["aws-check-all", "aws-cancel", "aws-save"]) byId[id] = new Node("button");
    byId["aws-foot-note"] = new Node("span");
    byId["creds-body"] = Object.assign(new Node("div"), { clientWidth: 800 });
    byId["settings-modal"] = Object.assign(new Node("dialog"), { className: "as-page" });
  }
  const box = {
    document: { createElement: (t) => new Node(t), createTextNode: (t) => new Text(t), getElementById: (id) => byId[id] || null },
    console, Promise, JSON, Date, setTimeout, clearTimeout, setInterval, clearInterval,
    window: { innerWidth: width },
    mobileActive: (w) => Number(w) <= 700,
    api: async (p, opt) => {
      const call = { path: p, method: (opt && opt.method) || "GET" };
      if (opt && opt.body) call.body = JSON.parse(opt.body);
      calls.push(call);
      return box.reply ? box.reply(p, opt) : {};
    },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["pending.js", "store.js", "credentials.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  box.loadCredentials = () => {};
  return { box, calls, byId };
}

const settle = () => new Promise((r) => setImmediate(r));
const btnIn = (root, label) => root.querySelectorAll("button").find((b) => b.textContent === label);
const checks = (p) => p.calls.filter((c) => c.path === "/api/credentials/aws/check").length;
const saves = (p) => p.calls.filter((c) => c.path === "/api/credentials/aws" && c.method !== "GET").length;

// The host profiles answer; a check answers per profile: acme-dev reads only, acme-ops would write (needs the
// "I understand" tick) or, with `opsFails`, does not pass. A save waits for `hold` when one is given.
function reply(over = {}) {
  return (p, opt) => {
    if (p === "/api/credentials/aws/host-profiles") return { profiles: hostProfiles };
    if (p === "/api/credentials/aws/check") {
      const b = JSON.parse(opt.body);
      if (b.role_arn.includes("agentbox-ops")) return over.opsFails ? { assume: "error" } : { assume: "ok", verdict: "writes", actions: ["s3:PutObject"] };
      return { assume: "ok", verdict: "read_only" };
    }
    if (p === "/api/credentials/aws") return over.hold ? over.hold() : {};
    return {};
  };
}

// The add view in a drawer's own foot (opts.foot), with nothing ticked.
async function openDrawer(p) {
  const opts = { head: new Node("div"), host: new Node("div"), foot: new Node("div"), onCreated: () => {}, onClose: () => {}, saveLabel: "Save all" };
  p.box.reply = reply();
  p.box.awsOpenAdd(opts);
  await settle();
  return opts;
}
async function tick(opts, ...names) {
  const ticks = opts.host.querySelectorAll("input").filter((i) => i.type === "checkbox");
  hostProfiles.forEach((hp, i) => { if (names.includes(hp.name)) { ticks[i].checked = true; ticks[i].onchange(); } });
}

test("desktop drawer foot, nothing ticked: both buttons are aria-disabled with their reason and still enabled in the DOM", async () => {
  const p = page();
  const opts = await openDrawer(p);
  const ca = btnIn(opts.foot, "Check all"), sv = btnIn(opts.foot, "Save all");
  assert.strictEqual(ca.getAttribute("aria-disabled"), "true");
  assert.strictEqual(ca.getAttribute("title"), CHECK_REASON);
  assert.strictEqual(ca.disabled, false);
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  assert.strictEqual(sv.getAttribute("title"), SAVE_REASON);
  assert.strictEqual(sv.disabled, false);
  assert.ok(sv.classList.contains("off"), "the .off class stays: the primary still reads as closed");
});

test("desktop drawer foot: a click, Enter and Space on an aria-disabled Check all / Save all run nothing", async () => {
  const p = page();
  const opts = await openDrawer(p);
  const ca = btnIn(opts.foot, "Check all"), sv = btnIn(opts.foot, "Save all");
  for (const b of [ca, sv]) {
    b.click();
    press(b, "Enter");
    press(b, " ");
  }
  await settle();
  assert.strictEqual(checks(p), 0);
  assert.strictEqual(saves(p), 0);
});

test("desktop drawer foot: Save all is held (a click saves nothing) while a row waits for 'I understand', and says so", async () => {
  const p = page();
  const opts = await openDrawer(p);
  await tick(opts, "acme-dev", "acme-ops");
  btnIn(opts.foot, "Check all").click();
  await settle();
  const sv = btnIn(opts.foot, "Save all"), ca = btnIn(opts.foot, "Check all");
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true", "acme-ops writes and has not been acknowledged");
  assert.strictEqual(sv.getAttribute("title"), WAIT_ONE, "the reason is the footer note's own waiting text");
  assert.strictEqual(ca.getAttribute("aria-disabled"), null, "Check all is live with rows ticked");
  assert.strictEqual(ca.getAttribute("title"), null);
  // acme-dev is ready, so an unguarded awsSaveAll would save it: the guard is what stops that.
  sv.click();
  press(sv, "Enter");
  press(sv, " ");
  await settle();
  assert.strictEqual(saves(p), 0, "nothing was saved");
});

test("desktop drawer foot: ready rows light Save all up, with no reason and no aria-disabled, and it saves", async () => {
  const p = page();
  const opts = await openDrawer(p);
  await tick(opts, "acme-dev");
  btnIn(opts.foot, "Check all").click();
  await settle();
  const sv = btnIn(opts.foot, "Save all");
  assert.strictEqual(sv.getAttribute("aria-disabled"), null);
  assert.strictEqual(sv.getAttribute("title"), null);
  assert.ok(!sv.classList.contains("off"));
  sv.click();
  await settle();
  assert.strictEqual(saves(p), 1);
});

test("desktop drawer foot: Check all is held while a save is in flight, with no title", async () => {
  const p = page();
  const opts = await openDrawer(p);
  let release;
  p.box.reply = reply({ opsFails: true, hold: () => new Promise((r) => { release = () => r({}); }) });
  await tick(opts, "acme-dev", "acme-ops");
  btnIn(opts.foot, "Check all").click();
  await settle();
  btnIn(opts.foot, "Save all").click();
  await settle();
  const ca = btnIn(opts.foot, "Check all"), sv = btnIn(opts.foot, "Save all");
  assert.strictEqual(ca.getAttribute("aria-disabled"), "true", "saving");
  assert.strictEqual(ca.getAttribute("title"), null, "there is no reason to give while it saves");
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  const before = checks(p);
  ca.click();
  press(ca, "Enter");
  await settle();
  assert.strictEqual(checks(p), before, "acme-ops did not pass, so an unguarded Check all would have checked it again");
  release();
  await settle();
});

test("phone drawer foot: Save all and Check all are disabled as before, with no title and no aria-disabled", async () => {
  const p = page({ width: 390 });
  const opts = await openDrawer(p);
  const ca = btnIn(opts.foot, "Check all"), sv = btnIn(opts.foot, "Save all");
  for (const b of [ca, sv]) {
    assert.strictEqual(b.disabled, true);
    assert.strictEqual(b.getAttribute("aria-disabled"), null);
    assert.strictEqual(b.getAttribute("title"), null);
  }
  ca.click();
  sv.click();
  await settle();
  assert.strictEqual(checks(p) + saves(p), 0);
});

test("the guard is on every aria-disabled button an AWS flow builds, not only the foot's (awsBtn)", () => {
  const p = page();
  let hit = 0;
  const b = p.box.awsBtn("Go", "", () => { hit++; return "ran"; });
  assert.strictEqual(b.click(), "ran", "a live button runs, and its result is passed on");
  b.setAttribute("aria-disabled", "true");
  b.click();
  press(b, "Enter");
  press(b, " ");
  assert.strictEqual(hit, 1);
  b.removeAttribute("aria-disabled");
  b.click();
  assert.strictEqual(hit, 2);
});

test("desktop Settings page foot (#aws-check-all, #aws-save): same reasons, and a click, Enter and Space run nothing", async () => {
  const p = page({ statics: true });
  p.box.reply = reply();
  p.box.awsOpenAdd();
  await settle();
  const ca = p.byId["aws-check-all"], sv = p.byId["aws-save"];
  assert.strictEqual(ca.getAttribute("aria-disabled"), "true");
  assert.strictEqual(ca.getAttribute("title"), CHECK_REASON);
  assert.strictEqual(ca.disabled, false);
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  assert.strictEqual(sv.getAttribute("title"), SAVE_REASON);
  assert.strictEqual(sv.disabled, false, "the markup's own disabled attribute is lifted on desktop");
  assert.ok(sv.classList.contains("off"));
  for (const b of [ca, sv]) {
    b.click();
    press(b, "Enter");
    press(b, " ");
  }
  await settle();
  assert.strictEqual(checks(p), 0);
  assert.strictEqual(saves(p), 0);
});

test("desktop Settings page foot: a ticked, checked row lights Save all up and clears the reason", async () => {
  const p = page({ statics: true });
  p.box.reply = reply();
  p.box.awsOpenAdd();
  await settle();
  const host = p.byId["creds-body"];
  const ticks = host.querySelectorAll("input").filter((i) => i.type === "checkbox");
  ticks[0].checked = true;
  ticks[0].onchange();
  p.byId["aws-check-all"].click();
  await settle();
  const sv = p.byId["aws-save"];
  assert.strictEqual(sv.getAttribute("aria-disabled"), null);
  assert.strictEqual(sv.getAttribute("title"), null);
  assert.strictEqual(p.byId["aws-check-all"].getAttribute("aria-disabled"), null);
  sv.click();
  await settle();
  assert.strictEqual(saves(p), 1);
});

test("desktop Settings page foot: Save all is held (a click, Enter and Space save nothing) while a row waits for 'I understand'", async () => {
  const p = page({ statics: true });
  p.box.reply = reply();
  p.box.awsOpenAdd();
  await settle();
  const ticks = p.byId["creds-body"].querySelectorAll("input").filter((i) => i.type === "checkbox");
  for (const t of ticks) { t.checked = true; t.onchange(); }
  p.byId["aws-check-all"].click();
  await settle();
  const sv = p.byId["aws-save"];
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  assert.strictEqual(sv.getAttribute("title"), WAIT_ONE);
  // acme-dev is ready, so an unguarded #aws-save handler would save it.
  sv.click();
  press(sv, "Enter");
  press(sv, " ");
  await settle();
  assert.strictEqual(saves(p), 0);
});

test("desktop Settings page foot, edit mode: Save all's title and the foot note are the same sentence before a check", async () => {
  const p = page({ statics: true });
  p.box.reply = reply();
  const role = "arn:aws:iam::111111111111:role/agentbox-dev";
  p.box.awsOpenEdit({ name: "acme-dev", source_profile: "base", role_arn: role, region: "eu-west-1" });
  await settle();
  const sv = p.byId["aws-save"], note = p.byId["aws-foot-note"];
  assert.strictEqual(sv.textContent, "Save");
  assert.strictEqual(note.textContent, "", "an edit that leaves the role alone needs no check: Save is live");
  assert.strictEqual(sv.getAttribute("aria-disabled"), null);
  // Changing the role makes the old check void.
  const rin = p.byId["creds-body"].querySelectorAll("input").find((i) => i.value === role);
  rin.value = role + "-2";
  rin.oninput();
  assert.strictEqual(note.textContent, CHECK_ROLE);
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  assert.strictEqual(sv.getAttribute("title"), note.textContent);
  p.byId["aws-save"].click();
  await settle();
  assert.strictEqual(saves(p), 0);
});

test("desktop drawer foot: with one row waiting and none ready the Save title is the foot note, not 'Nothing ready'", async () => {
  const p = page();
  const opts = await openDrawer(p);
  await tick(opts, "acme-ops");
  btnIn(opts.foot, "Check all").click();
  await settle();
  const sv = btnIn(opts.foot, "Save all");
  assert.strictEqual(sv.getAttribute("aria-disabled"), "true");
  assert.ok(opts.foot.textContent.includes(WAIT_ONE), "the foot note says the row is waiting");
  assert.strictEqual(sv.getAttribute("title"), WAIT_ONE);
});

test("desktop drawer foot: 0 of N ready keeps the frame copy for Save all beside the count", async () => {
  const p = page();
  const opts = await openDrawer(p);
  p.box.reply = reply({ opsFails: true });
  await tick(opts, "acme-ops");
  btnIn(opts.foot, "Check all").click();
  await settle();
  const sv = btnIn(opts.foot, "Save all");
  assert.ok(opts.foot.textContent.includes("0 of 1 ready to save."));
  assert.strictEqual(sv.getAttribute("title"), SAVE_REASON);
});

test("phone Settings page foot: disabled as before, no title", async () => {
  const p = page({ statics: true, width: 390 });
  p.box.reply = reply();
  p.box.awsOpenAdd();
  await settle();
  for (const id of ["aws-check-all", "aws-save"]) {
    const b = p.byId[id];
    assert.strictEqual(b.disabled, true, id);
    assert.strictEqual(b.getAttribute("aria-disabled"), null, id);
    assert.strictEqual(b.getAttribute("title"), null, id);
  }
});

// --- the CSS ----------------------------------------------------------------------------------------

const css = fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
// Every `selector { decls }` of the file with no nested block in it (the one @media wrapper is skipped).
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ selector: m[1].trim().replace(/\s+/g, " "), body: m[2].replace(/\s+/g, " ").trim() }));
const parts = (sel) => sel.split(",").map((s) => s.trim());

test("the #169 disabled rule also matches [aria-disabled=\"true\"], at the same weight and with the same look", () => {
  const rule = rules.find((r) => r.selector.startsWith(":is(button, input, select, textarea)") && /:disabled/.test(r.selector) && /opacity: \.42/.test(r.body));
  assert.ok(rule, "the rule is there");
  assert.strictEqual(rule.selector, ':is(button, input, select, textarea):is(:disabled, [aria-disabled="true"]):where(:not(.busy, [aria-busy="true"]))');
  assert.match(rule.body, /(^|; )opacity: \.42;/);
  assert.match(rule.body, /cursor: not-allowed/);
  assert.doesNotMatch(rule.body, /text-decoration/, "no strikethrough");
  assert.strictEqual(rules.filter((r) => /\[aria-disabled="true"\]/.test(r.selector) && /(^|; )opacity:/.test(r.body)).length, 1, "it is the one disabled look");
});

test("no desktop rule strikes an aria-disabled control through", () => {
  for (const r of rules) {
    if (!/\[aria-disabled/.test(r.selector)) continue;
    assert.doesNotMatch(r.body, /line-through/, r.selector);
  }
});

test("a kit or legacy hover does not light up an aria-disabled button (the :disabled hover restatements, for aria-disabled)", () => {
  const fill = (sel) => {
    const r = rules.find((x) => parts(x.selector).includes(sel));
    return r ? (/background: ([^;]+)/.exec(r.body) || [])[1] : undefined;
  };
  assert.strictEqual(fill('.btn[aria-disabled="true"]:hover'), "var(--raise)");
  assert.strictEqual(fill('.btn.pri[aria-disabled="true"]:hover'), "var(--accent)");
  assert.strictEqual(fill('.btn-primary[aria-disabled="true"]:hover'), "var(--accent)");
  assert.strictEqual(fill('.btn.gh[aria-disabled="true"]:hover'), "transparent");
  assert.strictEqual(fill('.btn.dgr[aria-disabled="true"]:hover'), "transparent");
  assert.strictEqual(fill('.btn-yes[aria-disabled="true"]:hover'), "var(--amber)");
});

test("the footers' own secondary hover (Settings page foot, Credentials page and drawer) skips an aria-disabled button", () => {
  const SKIP = ':where(:not([aria-disabled="true"]))';
  for (const sel of [
    "#page-host > #settings-modal.as-page > .dft .btn",
    "#page-host > #settings-modal.as-page :is(#creds, #cred-drawer) .btn",
  ]) {
    const want = sel + ":where(:not(.pri))" + SKIP + ":hover";
    const r = rules.find((x) => x.selector === want); // one selector, not a list: the :is(#creds, #cred-drawer) has a comma
    assert.ok(r, "a hover rule " + want);
    assert.match(r.body, /background: var\(--line\)/);
  }
  assert.ok(!rules.some((x) => x.selector === "#page-host > #settings-modal.as-page > .dft .btn:where(:not(.pri)):hover"), "the old hover that lit it up is gone");
  assert.ok(!rules.some((x) => x.selector === "#page-host > #settings-modal.as-page :is(#creds, #cred-drawer) .btn:where(:not(.pri)):hover"), "and so is the page's");
});
