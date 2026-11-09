"use strict";
// Plan 7 Task 7 (#177): the system banner painter (sysbanner.js) over the real notifymodel.js, the stale-list
// class, the rail marker, and the app.js wiring pins (sys record, 401, version, visibility, R18).
// Fixtures use made-up names.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
const T = 9000000;
const nm = require("../static/notifymodel.js");

function page(sysOver, over) {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  const slot = doc.createElement("div"); slot.hidden = true;
  const list = doc.createElement("ul");
  const rail = doc.createElement("nav");
  const box = Object.assign({
    document: doc, window: { innerWidth: 1280, location: { href: "", reload() { box.calls.reload++; } } }, console, Date, Math,
    els: { "sys-banner-slot": slot, sessions: list, rail48: rail }, calls: { retry: 0, reload: 0, rail: 0, toggle: 0 }, phone: false,
    now: T,
    sys: Object.assign({ lastOkAt: T - 1000, failSince: null, signedOut: false, hiddenSince: null, paused: false,
      bootVersion: "v0.24.2", version: "v0.24.2", dismissedVersion: "" }, sysOver || {}),
    mobileActive: () => box.phone,
    versionLabel: (v) => v,
    refreshSessions: () => { box.calls.retry++; },
    renderRail48: () => { box.calls.rail++; },
    toggleSidebar: () => { box.calls.toggle++; },
  }, over || {});
  doc.getElementById = (id) => box.els[id] || null;
  box.document = doc;
  box.globalThis = box;
  const ctx = vm.createContext(box);
  // Date.now follows box.now so the clock is fake
  vm.runInContext("Date = { now: () => now, parse: globalThis.__parse };", Object.assign(ctx, { __parse: Date.parse }));
  for (const f of ["notifymodel.js", "sysbanner.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  return { box, doc, slot, list, rail };
}
const texts = (n) => n.textContent;

test("banner: nothing while polls are fine; the slot stays hidden and the list is not stale", () => {
  const { box, slot, list } = page();
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, true);
  assert.strictEqual(slot.children.length, 0);
  assert.strictEqual(list.classList.contains("sess-stale"), false);
});

test("banner: can't reach paints .banner with a hollow dot, title, muted body with the age, Retry now; rows go stale", () => {
  const { box, slot, list } = page({ lastOkAt: T - 14000, failSince: T - 11000 });
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, false);
  const b = slot.children[0];
  assert.strictEqual(b.className, "banner");
  assert.strictEqual(b.querySelector(".banner-title").textContent, "Can’t reach CuliBox");
  assert.strictEqual(b.querySelector(".dot").className, "dot dot-detached");
  assert.strictEqual(b.querySelector("p").className, "t-sm muted");
  assert.strictEqual(b.querySelector("p").textContent, "Retrying every 3 s. Last update 14 s ago; the list below is from then.");
  const btns = b.querySelectorAll(".btn");
  assert.deepStrictEqual(btns.map((x) => x.textContent), ["Retry now"]);
  assert.strictEqual(btns[0].className, "btn btn-sm");
  assert.strictEqual(list.classList.contains("sess-stale"), true);
  btns[0].click();
  assert.strictEqual(box.calls.retry, 1);
});

test("banner: the age moves in place (same element and key) as the clock runs", () => {
  const { box, slot } = page({ lastOkAt: T - 14000, failSince: T - 11000 });
  box.sysBannerSync();
  const el = slot.children[0];
  box.now = T + 6000;
  box.sysBannerSync();
  assert.strictEqual(slot.children[0], el, "not rebuilt");
  assert.match(el.querySelector("p").textContent, /Last update 20 s ago/);
});

test("banner: signed out has one primary Sign in again that goes to /login?reason=expired", () => {
  const { box, slot, list } = page({ signedOut: true });
  box.sysBannerSync();
  const b = slot.children[0];
  assert.strictEqual(b.querySelector(".banner-title").textContent, "Signed out");
  const btn = b.querySelector(".btn");
  assert.strictEqual(btn.className, "btn btn-sm btn-primary");
  assert.strictEqual(btn.textContent, "Sign in again");
  btn.click();
  assert.strictEqual(box.window.location.href, "/login?reason=expired");
  assert.strictEqual(list.classList.contains("sess-stale"), true);
});

test("banner: updated has no dot, Reload (only on click) and a close that hides it until a different version", () => {
  const { box, slot, list } = page({ version: "v0.25.0" });
  box.sysBannerSync();
  const b = slot.children[0];
  assert.strictEqual(b.querySelector(".banner-title").textContent, "CuliBox was updated to v0.25.0");
  assert.strictEqual(b.querySelector(".dot"), null);
  assert.strictEqual(list.classList.contains("sess-stale"), false, "the update does not stale the rows");
  assert.strictEqual(box.calls.reload, 0, "never automatic");
  const [reload, x] = b.querySelectorAll(".btn");
  assert.strictEqual(reload.textContent, "Reload");
  assert.strictEqual(x.className, "btn btn-ghost btn-icon btn-sm");
  assert.strictEqual(x.attrs["aria-label"], "Hide until the next update");
  assert.strictEqual(x.title, "Hide until the next update");
  reload.click();
  assert.strictEqual(box.calls.reload, 1);
  x.click();
  assert.strictEqual(slot.hidden, true);
  assert.strictEqual(box.sys.dismissedVersion, "v0.25.0");
  box.sys.version = "v0.25.1";
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, false);
});

test("banner: paused shows the one line and no buttons; the next OK poll clears it", () => {
  const { box, slot } = page({ paused: true });
  box.sysBannerSync();
  const b = slot.children[0];
  assert.strictEqual(b.querySelector(".banner-title").textContent, "Paused while this tab was hidden · updating…");
  assert.strictEqual(b.querySelectorAll(".btn").length, 0);
  box.sys.paused = false;
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, true);
});

test("banner: a phone width builds nothing and clears the stale class", () => {
  const { box, slot, list } = page({ signedOut: true }, { phone: true });
  box.sysBannerSync();
  assert.strictEqual(slot.children.length, 0);
  assert.strictEqual(list.classList.contains("sess-stale"), false);
});

test("rail marker: one 32px button, named by the banner title, neutral hollow dot, opens the sidebar", () => {
  const { box } = page({ signedOut: true });
  const mk = box.sysRailMarkerModel();
  assert.strictEqual(mk.title, "Signed out");
  const el = box.sysRailMarkerEl(mk);
  assert.strictEqual(el.className, "ri ri-banner");
  assert.strictEqual(el.attrs["aria-label"], "Signed out");
  assert.strictEqual(el.title, "Signed out");
  assert.strictEqual(el.querySelector(".dot").className, "dot dot-detached");
  el.click();
  assert.strictEqual(box.calls.toggle, 1);
  assert.strictEqual(page().box.sysRailMarkerModel(), null);
});

test("rail: a change of banner asks the rail to repaint once, not on every poll", () => {
  const { box } = page({ signedOut: true });
  box.sysBannerSync(); box.sysBannerSync(); box.sysBannerSync();
  assert.strictEqual(box.calls.rail, 1);
  box.sys.signedOut = false;
  box.sysBannerSync();
  assert.strictEqual(box.calls.rail, 2);
});

// --- app.js wiring pins ----------------------------------------------------------------------------------
const app = read("app.js");
test("app.js: any api() 401 marks signed out; a poll that lands clears signed out and paused", () => {
  assert.match(app, /const res = await fetch\(path, opts\);\n\s+if \(res\.status === 401\) sysNoteSignedOut\(\);/);
  assert.match(app, /function sysPollOk\(\) \{[^}]*sys\.signedOut = false; sys\.paused = false;/);
});
test("app.js: the version is first seen in initEnv and compared on the 30 s tick", () => {
  assert.match(app, /sysSeeVersion\(meta && meta\.version\);/);
  assert.match(app, /const m = await api\("\/api\/meta"\);\n\s+sysSeeVersion\(m && m\.version\);/);
});
test("app.js: a tab hidden 30 s or more pauses on return and polls at once; the signal repaints the banner", () => {
  assert.match(app, /document\.addEventListener\("visibilitychange"/);
  assert.match(app, /const step = sysVisibilityStep\(sys, document\.hidden, Date\.now\(\)\);[\s\S]*?if \(!step\.poll\) return;[\s\S]*?refreshSessions\(\);/);
  assert.match(app, /faviconIcon = st\.icon;\n\s+\}\n\s+if \(typeof sysBannerSync === "function"\) sysBannerSync\(\);/);
});

test("rail: #rail48 wears sess-stale with the list and loses it when the poll lands", () => {
  const { box, rail, list } = page({ lastOkAt: T - 14000, failSince: T - 11000 });
  box.sysBannerSync();
  assert.strictEqual(rail.classList.contains("sess-stale"), true);
  assert.strictEqual(list.classList.contains("sess-stale"), true);
  box.sys.failSince = null; box.sys.lastOkAt = T;
  box.sysBannerSync();
  assert.strictEqual(rail.classList.contains("sess-stale"), false);
  const u = page({ version: "v9" });
  u.box.sysBannerSync();
  assert.strictEqual(u.rail.classList.contains("sess-stale"), false, "an update does not stale the rail");
  const ph = page({ signedOut: true }, { phone: true });
  ph.box.sysBannerSync();
  assert.strictEqual(ph.rail.classList.contains("sess-stale"), false);
});

test("sysVisibilityStep: hidden records when; 29.9 s away does not pause, 30 s does and asks for a poll", () => {
  const s0 = { hiddenSince: null, paused: false };
  const h = nm.sysVisibilityStep(s0, true, 1000);
  assert.deepStrictEqual(h, { hiddenSince: 1000, paused: false, poll: false });
  assert.strictEqual(nm.sysVisibilityStep({ hiddenSince: 1000, paused: false }, true, 5000).hiddenSince, 1000, "a repeat keeps the first time");
  assert.deepStrictEqual(nm.sysVisibilityStep({ hiddenSince: 1000, paused: false }, false, 1000 + 29900), { hiddenSince: null, paused: false, poll: false });
  assert.deepStrictEqual(nm.sysVisibilityStep({ hiddenSince: 1000, paused: false }, false, 1000 + 30000), { hiddenSince: null, paused: true, poll: true });
  assert.deepStrictEqual(nm.sysVisibilityStep({ hiddenSince: null, paused: false }, false, 5), { hiddenSince: null, paused: false, poll: false });
});

test("paused clears on the next OK poll (sysPollOk) and the banner follows", () => {
  assert.match(app, /function sysPollOk\(\) \{[^}]*sys\.paused = false;/);
  const { box, slot } = page({ paused: true });
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, false);
  box.sys.paused = false; // what sysPollOk does
  box.sysBannerSync();
  assert.strictEqual(slot.hidden, true);
});
