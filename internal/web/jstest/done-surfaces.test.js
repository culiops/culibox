"use strict";
// F21-B (#172): a finished turn is neutral on every desktop surface that drew it
// amber: the sidebar group header and row, the icon rail's dot, the pane header's
// dot and the pane strip. app.js and tabs.js are classic scripts that cannot be
// required, so the functions are cut out by name and run in vm against tiny DOM
// stand-ins; desktop.css is read as text.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// A DOM element reduced to what the cut-out functions touch.
function el(tag) {
  const e = {
    tag, className: "", children: [], attrs: {}, dataset: {}, title: "",
    classList: { add: (c) => { e.className = (e.className + " " + c).trim(); } },
    append(...kids) { for (const k of kids) e.children.push(k); },
    appendChild(k) { e.children.push(k); return k; },
    replaceChildren(...kids) { e.children = kids; },
    setAttribute(k, v) { e.attrs[k] = v; },
    get textContent() { return e.children.map((k) => (typeof k === "string" ? k : k.textContent)).join(""); },
    set textContent(v) { e.children = [String(v)]; },
  };
  return e;
}
const doc = { createElement: el };

const att = require("../static/attention.js");
const desk = require("../static/deskshell.js");
const rail = require("../static/steerrail.js");
const act = require("../static/activity.js");

test("renderDesktopEntry: Needs you is the one amber group header; Done · your turn is neutral", () => {
  const ctx = vm.createContext({ document: doc, String });
  new vm.Script(fnSrc(read("app.js"), "renderDesktopEntry") + "\nglobalThis.renderDesktopEntry = renderDesktopEntry;").runInContext(ctx);
  const head = (key, label, count) => ctx.renderDesktopEntry({ id: "grp:" + key, header: true, key, label, count });
  const needs = head("needs-you", "Needs you", 3);
  assert.strictEqual(needs.className, "grp grp-label is-needs");
  assert.strictEqual(needs.textContent, "Needs you · 3");
  assert.strictEqual(needs.dataset.sid, "grp:needs-you");
  assert.strictEqual(needs.children[1].className, "n", "the count is the kit's tabular .n");
  for (const [k, l] of [["your-turn", "Done · your turn"], ["running", "Running"], ["idle", "Idle"]]) {
    const h = head(k, l, 1);
    assert.strictEqual(h.className, "grp grp-label", k + " is not amber");
    assert.strictEqual(h.textContent, l + " · 1");
  }
});

test("buildNeedStrip: a finished turn's strip is .is-done, a blocked one is not", () => {
  const strip = (model) => {
    const ctx = vm.createContext({
      document: doc, cardContent: () => ({}), cardExpanded: new Set(), stripLabel: desk.stripLabel,
      renderNeedsBody: () => el("div"), ResizeObserver: undefined, ssoDoneShown: () => false,
    });
    new vm.Script(fnSrc(read("tabs.js"), "buildNeedStrip") + "\nglobalThis.buildNeedStrip = buildNeedStrip;").runInContext(ctx);
    return ctx.buildNeedStrip({ id: "s" }, model, null);
  };
  const turn = { id: "t", activity: "waiting", need: "turn", claude: true, summary: "done" };
  const done = strip(desk.needStripModel(turn));
  assert.strictEqual(done.className, "needstrip is-done");
  assert.strictEqual(done.children[0].textContent, "Your turn", "the label stays");
  const ask = strip(desk.needStripModel({ id: "a", activity: "waiting", need: "ask", ask: "ls" }));
  assert.strictEqual(ask.className, "needstrip");
});

// railCtx runs renderRail48 over a session list and hands back each session's dot button.
function renderRail(sessions) {
  const railEl = el("nav");
  const ctx = vm.createContext({
    document: doc, window: { innerWidth: 1440 }, $: () => railEl, railItems: rail.railItems, jumpOrder: desk.jumpOrder, lastSessions: sessions,
    mobileActive: () => false, selectedSessionId: () => null, railLogo: () => el("span"), railMeters: () => el("div"),
    paintRailMeters: () => {}, activityClass: act.activityClass, deskDot: desk.deskDot, rail48Key: "",
    needsYou: att.needsYou, attentionClass: att.attentionClass, sessionDot: att.sessionDot,
    openTerminal: () => {}, toggleSidebar: () => {}, openSpawnDialog: () => {}, IS_MAC: false,
  });
  new vm.Script(fnSrc(read("app.js"), "renderRail48") + "\nglobalThis.renderRail48 = renderRail48;").runInContext(ctx);
  ctx.renderRail48();
  const mid = railEl.children[3]; // logo, », +, the dots, meters
  return mid.children.map((b) => ({ btn: b, dot: b.children[0] }));
}

test("renderRail48: a finished turn's dot is neutral and its hover says your turn; a block stays amber", () => {
  const rows = renderRail([
    { id: "a", name: "asks", activity: "waiting", need: "ask", attention: "block" },
    { id: "t", name: "turn", activity: "waiting", need: "turn", attention: "done" },
    { id: "s", name: "sso", activity: "working", need: "sso", attention: "block" },
    { id: "w", name: "work", activity: "working" },
  ]);
  const by = Object.fromEntries(rows.map((r) => [r.btn.title.split(" ")[0], r]));
  assert.strictEqual(by.asks.dot.className, "dot waiting");
  assert.strictEqual(by.asks.btn.className.includes("alert"), true);
  assert.strictEqual(by.turn.dot.className, act.activityClass("idle").cls);
  assert.strictEqual(by.turn.btn.title, "turn · your turn");
  assert.strictEqual(by.turn.btn.className.includes("alert"), false, "no alert on a finished turn");
  assert.strictEqual(by.sso.dot.className, "dot waiting", "an expired sign-in still blocks while the agent works");
  assert.strictEqual(by.work.dot.className, "dot working");
});

test("renderSession: the desktop (compact) row draws deskDot, the phone's row keeps sessionDot", () => {
  const src = fnSrc(read("app.js"), "renderSession");
  // The page's takenOver fact rides along (F55); the phone row never reads it.
  assert.match(src, /const act = compact \? deskDot\(s, \{ takenOver \}\) : sessionDot\(s\);/);
});

test("desktop.css: the group header's colour is the kit's, and the done strip is neutral", () => {
  const css = read("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = (sel) => { const m = new RegExp("(?:^|[}\\s])" + sel.replace(/[.[\]]/g, "\\$&") + "\\s*\\{([^}]*)\\}").exec(css); return m ? m[1] : null; };
  assert.strictEqual(rule(".grp.hot"), null, "the bespoke amber header rule is gone");
  const grp = rule(".grp");
  assert.ok(grp, ".grp keeps the sidebar spacing");
  assert.ok(!/color|text-transform|font-size|font-weight|letter-spacing/.test(grp), ".grp must not restyle the kit's .grp-label type");
  const done = rule(".needstrip.is-done");
  assert.match(done, /background:\s*var\(--panel\)/);
  assert.ok(!/amber/.test(done), "no amber in the done strip");
  const lab = rule(".needstrip.is-done .ns-label");
  assert.match(lab, /color:\s*var\(--muted\)/);
});

test("tabs.js renderHeader paints the dot through headerDot, never the tab's activity over a session record", () => {
  const src = read("tabs.js");
  const start = src.indexOf("  renderHeader() {");
  assert.ok(start >= 0, "renderHeader not found");
  const body = src.slice(start, src.indexOf("\n  }\n", start));
  assert.match(body, /const a = headerDot\(m, t\);/);
  assert.ok(!/activityClass\(/.test(body), "renderHeader must not call activityClass itself");
});
