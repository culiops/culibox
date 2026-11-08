"use strict";
// F55 + F69 (#172), the page wiring in app.js: the sidebar row learns that its
// terminal was taken over by another tab, and Mod+Shift+J / the palette go to the
// next session that needs you. app.js is a classic script that cannot be
// required, so functions are cut out of its source and run against stand-ins
// (the railmeters.test.js pattern); the pure decisions are tested in
// deskshell.test.js and activity.test.js.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const app = fs.readFileSync(path.join(STATIC, "app.js"), "utf8");
const termSrc = fs.readFileSync(path.join(STATIC, "terminal-tab.js"), "utf8");
const desk = require("../static/deskshell.js");

function cut(src, head) {
  const start = src.indexOf(head);
  assert.ok(start >= 0, head + " not found");
  return src.slice(start, src.indexOf("\n}\n", start) + 3);
}

// A class method, cut from its head to the closing brace at the class indent.
function method(src, head) {
  const start = src.indexOf(head);
  assert.ok(start >= 0, head + " not found");
  return src.slice(start, src.indexOf("\n  }\n", start) + 5);
}

// --- F55: the row knows a terminal was taken over -----------------------------
function rowLocalFor(tab) {
  const ctx = {
    terminalTabFor: () => tab, killing: new Set(), boxRuntime: "runc", globalThis: { repliesEnabled: false },
    pendingSends: new Set(), cardExpanded: new Set(), pendingAnswers: new Set(), ssoLocalSig: () => "",
    sessionElevated: () => false, isNestedShell: () => false, shellLabel: () => "", lastSessions: [], Date,
  };
  vm.runInNewContext(cut(app, "function rowLocal(s)") + "\nthis.out = rowLocal({ id: 'a' });", ctx);
  return ctx.out;
}

test("rowLocal: a terminal parked as 'taken over' is takenOver, and not heldHere", () => {
  const l = rowLocalFor({ status: () => "taken over" });
  assert.strictEqual(l.takenOver, true);
  assert.strictEqual(l.heldHere, false);
});

test("rowLocal: a connected terminal is heldHere, not takenOver; no tab is neither", () => {
  const held = rowLocalFor({ status: () => "connected" });
  assert.strictEqual(held.heldHere, true);
  assert.strictEqual(held.takenOver, false);
  for (const tab of [null, undefined, { status: () => "reconnecting…" }, { status: () => "ended" }]) {
    const l = rowLocalFor(tab);
    assert.strictEqual(l.takenOver, false);
    assert.strictEqual(l.heldHere, false);
  }
});

test("the compact row asks for the connection words with the page's takenOver fact, the phone row does not", () => {
  const body = cut(app, "function renderSession(");
  assert.match(body, /deskDot\(s, \{ takenOver \}\)/);
  assert.match(body, /paintCompactRow\(li, sname, grow, s, \{ takenOver \}\)/);
  assert.match(cut(app, "function paintCompactRow("), /rowSubline\(s, conn\)/);
  assert.match(body, /sessionDot\(s\)/, "the phone keeps sessionDot");
  assert.ok(body.indexOf("const takenOver") < body.indexOf("deskDot(s, { takenOver })"), "defined before its first use");
  assert.match(body, /const takenOver = compact && /, "a phone row never reads it");
  // A taken-over row's tooltip is the connection word, not the long 'someone is viewing' text.
  assert.match(body, /const viewed = elsewhere && !takenOver;/);
  assert.match(body, /adot\.title = viewed\n/);
});

test("terminal-tab: a status change into or out of 'taken over' repaints the sidebar now, on desktop only", () => {
  const body = method(termSrc, "  setStatus(t)");
  assert.match(body, /"taken over"/);
  assert.match(body, /refreshSessions/);
  assert.match(body, /mobileActive/);
});

function runSetStatus(from, to, { mobile, withRefresh = true }) {
  const calls = [];
  const ctx = {
    mobileActive: () => !!mobile, window: { innerWidth: mobile ? 390 : 1280 },
    refreshSessions: withRefresh ? () => calls.push("refresh") : undefined,
  };
  vm.runInNewContext("class T { " + method(termSrc, "  setStatus(t)") + " }\nthis.T = T;", ctx);
  const t = new ctx.T();
  t.statusText = from;
  t.onUpdate = () => calls.push("update");
  t.setStatus(to);
  return { calls, status: t.statusText };
}

test("setStatus: entering or leaving 'taken over' refreshes the list once; other changes do not", () => {
  assert.deepStrictEqual(runSetStatus("connected", "taken over", { mobile: false }), { calls: ["update", "refresh"], status: "taken over" });
  assert.deepStrictEqual(runSetStatus("taken over", "connecting…", { mobile: false }).calls, ["update", "refresh"]);
  assert.deepStrictEqual(runSetStatus("taken over", "taken over", { mobile: false }).calls, ["update"], "no change, no fetch");
  assert.deepStrictEqual(runSetStatus("connected", "reconnecting…", { mobile: false }).calls, ["update"]);
  assert.deepStrictEqual(runSetStatus("connected", "taken over", { mobile: true }).calls, ["update"], "the phone's rows do not read it");
  assert.deepStrictEqual(runSetStatus("connected", "taken over", { mobile: false, withRefresh: false }).calls, ["update"], "no refreshSessions (a test page): no throw");
});

// --- F69: Next that needs you ---------------------------------------------------
function goFor(sessions, currentId) {
  const opened = [];
  const ctx = {
    lastSessions: sessions, selectedSessionId: () => currentId, nextNeedsYou: desk.nextNeedsYou,
    openTerminal: (id, name) => opened.push([id, name]),
  };
  vm.runInNewContext(cut(app, "function goNextNeedsYou(") + "\nthis.ret = goNextNeedsYou();", ctx);
  return { ret: ctx.ret, opened };
}

const ask = { id: "a", name: "ask-one", activity: "waiting", need: "ask", ask: "rm x", attention: "block" };
const turn = { id: "t", name: "turn-one", activity: "waiting", need: "turn", attention: "done" };
const work = { id: "w", name: "work-one", activity: "working" };

test("goNextNeedsYou: opens the next one through openTerminal (the Mod+digit focus path), ask then turn then ask", () => {
  assert.deepStrictEqual(goFor([work, turn, ask], "a").opened, [["t", "turn-one"]]);
  assert.deepStrictEqual(goFor([work, turn, ask], "t").opened, [["a", "ask-one"]]);
  assert.deepStrictEqual(goFor([work, turn, ask], "w").opened, [["a", "ask-one"]]);
});

test("goNextNeedsYou: with none it does nothing, and the key is still consumed", () => {
  const r = goFor([work], "w");
  assert.deepStrictEqual(r.opened, []);
  assert.strictEqual(r.ret, true);
});

test("session.nextNeeds is bound once: not ours on a phone, a held key does not spin through the list", () => {
  const i = app.indexOf('bindShortcut("session.nextNeeds"');
  assert.ok(i > 0, "bound");
  assert.strictEqual(app.indexOf('bindShortcut("session.nextNeeds"', i + 1), -1);
  const body = app.slice(i, app.indexOf("});", i));
  const mob = body.indexOf("ctx.mobile");
  const rep = body.indexOf("e.repeat");
  const go = body.indexOf("goNextNeedsYou()");
  assert.ok(mob > 0 && rep > mob && go > rep, "mobile check, then repeat check, then go");
  assert.match(body, /ctx\.mobile\) return false/);
  assert.match(body, /e\.repeat\) return true/);
});

test("the palette runs the same function the shortcut does", () => {
  const pal = fs.readFileSync(path.join(STATIC, "palette.js"), "utf8");
  assert.match(pal, /nextNeeds: goNextNeedsYou/);
});

test("showShortcuts leaves Next that needs you out on a phone, so the phone's dialog text is what it was", () => {
  const body = cut(app, "function showShortcuts(");
  assert.match(body, /session\.nextNeeds/);
  assert.match(body, /mobileActive\(window\.innerWidth\)/);
});
