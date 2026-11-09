"use strict";
// Plan 7 Task 5 (#177): visibleSessionIds (tabs.js), the attention chip painter (attnchip.js) and the Blocked
// chime shell (chime.js). The page scripts run in one vm context beside the real notifymodel.js, like the
// browser's shared scope; tabs.js cannot be required, so visibleSessionIds is cut out of it. Fixtures use made-up names.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function cutFunction(src, name) {
  const at = src.indexOf("function " + name + "(");
  assert.ok(at >= 0, name + " not found");
  let depth = 0;
  for (let i = src.indexOf("{", at); i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(at, i + 1);
  }
  throw new Error("unbalanced " + name);
}

// --- visibleSessionIds (E7) ---------------------------------------------------------------------------
function visIds(over) {
  const box = Object.assign({
    cells: [], maximized: null, page: "",
    cellTerminal: (c) => c.term || null,
    currentMaximized: () => box.maximized,
    currentPage: () => box.page,
  }, over || {});
  const ctx = vm.createContext(box);
  vm.runInContext(cutFunction(read("tabs.js"), "visibleSessionIds"), ctx);
  return () => [...box.visibleSessionIds()].sort();
}
const cell = (id) => ({ term: id ? { id } : null });

test("visibleSessionIds: the terminal of every shown pane", () => {
  assert.deepStrictEqual(visIds({ cells: [cell("a"), cell("b"), cell(null), cell("c")] })(), ["a", "b", "c"]);
});

test("visibleSessionIds: a maximized pane hides the others", () => {
  const max = cell("b");
  assert.deepStrictEqual(visIds({ cells: [cell("a"), max], maximized: max })(), ["b"]);
});

test("visibleSessionIds: an open page (Settings, Analytics, Audit) hides the grid, so nothing is visible", () => {
  assert.deepStrictEqual(visIds({ cells: [cell("a"), cell("b")], page: "settings-modal" })(), []);
});

test("visibleSessionIds: no panes, no ids", () => {
  assert.deepStrictEqual(visIds({ cells: [] })(), []);
});

// --- one page ---------------------------------------------------------------------------------------------
function page(over) {
  const listeners = [];
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new Node(t, doc);
  doc.createTextNode = (t) => new Text(t);
  doc.body = doc.createElement("body");
  doc.getElementById = (id) => box.els[id] || null;
  doc.addEventListener = (type, fn, cap) => listeners.push({ type, fn, cap });
  doc.removeEventListener = (type, fn) => { const i = listeners.findIndex((l) => l.type === type && l.fn === fn); if (i >= 0) listeners.splice(i, 1); };
  const grid = doc.createElement("main"); grid.getBoundingClientRect = () => ({ left: 290, top: 0, width: 800, height: 700 });
  const pageHost = doc.createElement("div"); pageHost.getBoundingClientRect = () => ({ left: 290, top: 0, width: 1000, height: 700 });
  const calls = { go: 0, played: [] };
  const box = Object.assign({
    document: doc, window: { innerWidth: 1280 }, console, Date, JSON, Map, Set, Math, Promise,
    els: { grid, "page-host": pageHost }, calls, phone: false, visible: new Set(), lastSessions: [], pageOpen: false,
    mobileActive: () => box.phone,
    jumpOrder: (list) => list,
    visibleSessionIds: () => box.visible,
    goNextNeedsYou: () => { calls.go++; return true; },
    shortcutHint: (id) => (id === "session.nextNeeds" ? "Ctrl+Shift+J" : ""),
    IS_MAC: false,
  }, over || {});
  box.globalThis = box;
  const ctx = vm.createContext(box);
  box.listeners = listeners;
  box.run = (src) => vm.runInContext(src, ctx);
  box.window.document = doc;
  for (const f of ["attention.js", "notifymodel.js", "attnchip.js", "chime.js"]) {
    new vm.Script(read(f), { filename: f }).runInContext(ctx);
  }
  return { box, doc, grid, pageHost, calls };
}
const ask = (id, over) => Object.assign({ id, name: "n-" + id, activity: "waiting", need: "ask", ask: "Bash(rm -rf x)", attention: "block" }, over || {});
const chipEl = (doc) => doc.body.children.flatMap((c) => c.querySelectorAll ? [c, ...c.querySelectorAll(".attn-chip")] : []).find((n) => n.className && n.className.split(/\s+/).includes("attn-chip"));
const host = (doc) => doc.body.children.find((c) => c.attrs.id === "attn-chip-host");

test("chip: a blocked ask off-screen paints .attn-chip[role=status] with the name in bold, the Go button and the registry hint", () => {
  const { box, doc } = page();
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  const chip = chipEl(doc);
  assert.ok(chip, "chip painted");
  assert.strictEqual(host(doc).hidden, false);
  assert.strictEqual(chip.attrs.role, "status");
  assert.strictEqual(chip.attrs["aria-live"], "polite");
  assert.strictEqual(chip.textContent.replace(/Go.*$/, ""), "n-a wants to run Bash");
  const strong = chip.querySelector(".w-600");
  assert.strictEqual(strong.textContent, "n-a");
  const go = chip.querySelector(".btn");
  assert.strictEqual(go.className, "btn btn-sm");
  assert.strictEqual(go.querySelector(".kbd-hint").textContent, "Ctrl+Shift+J");
  const x = chip.querySelector(".btn-icon");
  assert.strictEqual(x.className, "btn btn-ghost btn-icon btn-sm");
  assert.strictEqual(x.attrs["aria-label"], "Hide until something else needs you");
  assert.strictEqual(x.title, "Hide until something else needs you");
  assert.strictEqual(chip.querySelector(".dot").className, "dot dot-needs");
});

test("chip: the hint is the registry's, never hard-coded", () => {
  const { box, doc } = page({ shortcutHint: () => "⇧⌘J" });
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  assert.strictEqual(chipEl(doc).querySelector(".kbd-hint").textContent, "⇧⌘J");
  assert.ok(!/Ctrl|Shift|\bJ\b/.test(read("attnchip.js").replace(/\/\/.*$/gm, "")), "no chord text in the painter");
});

test("chip: hidden when the session is on a visible pane, and returns when it is not", () => {
  const { box, doc } = page();
  box.lastSessions = [ask("a")];
  box.visible = new Set(["a"]);
  box.attnChipSync();
  assert.strictEqual(chipEl(doc), undefined, "never built while nothing needs it");
  box.visible = new Set();
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, false);
  box.visible = new Set(["a"]);
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, true);
});

test("chip: Done never shows a chip", () => {
  const { box, doc } = page();
  box.lastSessions = [{ id: "d", name: "n-d", activity: "waiting", need: "turn", attention: "done" }];
  box.attnChipSync();
  assert.strictEqual(chipEl(doc), undefined);
});

test("chip: repainted only when its model changes (text nodes untouched on an equal poll)", () => {
  const { box, doc } = page();
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  const before = chipEl(doc).querySelector(".w-600").children.slice();
  const textKid = chipEl(doc).children[1];
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  assert.strictEqual(chipEl(doc).children[1], textKid, "same element");
  box.lastSessions = [ask("a"), ask("b")];
  box.attnChipSync();
  assert.match(chipEl(doc).textContent, /n-a wants to run Bash · \+1/);
  assert.ok(before);
});

test("chip: Go runs goNextNeedsYou", () => {
  const { box, doc, calls } = page();
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  chipEl(doc).querySelector(".btn").onclick({ preventDefault() {} });
  assert.strictEqual(calls.go, 1);
});

test("chip: the X hides it until the set of blocked causes changes", () => {
  const { box, doc } = page();
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  chipEl(doc).querySelector(".btn-icon").onclick({});
  assert.strictEqual(host(doc).hidden, true);
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, true, "a poll with the same set keeps it hidden");
  box.lastSessions = [ask("a"), ask("b")];
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, false, "a new cause brings it back");
});

test("chip: centred over #grid at top 44, or over #page-host when a page is open", () => {
  const { box, doc, grid, pageHost } = page();
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  assert.deepStrictEqual([host(doc).style.left, host(doc).style.width, host(doc).style.top], ["290px", "800px", "44px"]);
  doc.body.classList.toggle("page-open", true);
  box.visible = new Set();
  box.attnChipSync();
  assert.deepStrictEqual([host(doc).style.left, host(doc).style.width, host(doc).style.top], ["290px", "1000px", "44px"]);
  assert.ok(grid && pageHost);
});

test("chip: never created on a phone, and taken away when the viewport crosses to one", () => {
  const { box, doc } = page({ phone: true });
  box.lastSessions = [ask("a")];
  box.attnChipSync();
  assert.strictEqual(host(doc), undefined, "no element on a phone");
  box.phone = false;
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, false);
  box.phone = true;
  box.attnChipSync();
  assert.strictEqual(host(doc).hidden, true);
});

test("chip: no innerHTML anywhere in the painter or the chime", () => {
  for (const f of ["attnchip.js", "chime.js"]) assert.ok(!/innerHTML/.test(read(f)), f);
});

// --- chime shell --------------------------------------------------------------------------------------------
function fakeAudio(startState) {
  const log = { created: 0, resumed: 0, osc: [] };
  class Ctx {
    constructor() { log.created++; this.state = startState; this.currentTime = 0; this.destination = {}; }
    resume() { log.resumed++; this.state = "running"; return Promise.resolve(); }
    createGain() { return { gain: { setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect() {} }; }
    createOscillator() {
      const o = { type: "", frequency: { setValueAtTime() {}, value: 0 }, connect() {}, start(t) { o.t0 = t; }, stop(t) { o.t1 = t; log.osc.push(o); } };
      return o;
    }
  }
  return { Ctx, log };
}

test("chime: pref off creates no context and plays nothing", () => {
  const { Ctx, log } = fakeAudio("running");
  const { box } = page();
  box.window.AudioContext = Ctx;
  box.chimeSync(false);
  assert.strictEqual(log.created, 0);
  assert.strictEqual(box.chimePlay(), false);
  assert.strictEqual(box.listeners.length, 0, "no gesture listener while off");
});

test("chime: with the pref on, a context exists only after the first user gesture; before it a play is a silent no-op", () => {
  const { Ctx, log } = fakeAudio("suspended");
  const { box } = page();
  box.window.AudioContext = Ctx;
  box.chimeSync(true);
  assert.strictEqual(log.created, 0, "not before a gesture");
  assert.deepStrictEqual(box.listeners.map((l) => l.type).sort(), ["keydown", "pointerdown"]);
  assert.strictEqual(box.chimePlay(), false);
  box.listeners.find((l) => l.type === "pointerdown").fn({});
  assert.strictEqual(log.created, 1);
  assert.strictEqual(log.resumed, 1);
  assert.strictEqual(box.listeners.length, 0, "the one-time listeners leave once the context runs");
});

test("chime: a running context plays a two-tone blip of about 180 ms", () => {
  const { Ctx, log } = fakeAudio("running");
  const { box } = page();
  box.window.AudioContext = Ctx;
  box.chimeSync(true);
  box.listeners.find((l) => l.type === "keydown").fn({});
  assert.strictEqual(box.chimePlay(), true);
  assert.strictEqual(log.osc.length, 2);
  const span = Math.max(...log.osc.map((o) => o.t1)) - Math.min(...log.osc.map((o) => o.t0));
  assert.ok(span > 0.15 && span < 0.21, "span " + span);
});

test("chime: webkitAudioContext is used when AudioContext is absent; no constructor is a silent no-op", () => {
  const { Ctx, log } = fakeAudio("running");
  const a = page();
  a.box.window.webkitAudioContext = Ctx;
  a.box.chimeSync(true);
  a.box.listeners[0].fn({});
  assert.strictEqual(log.created, 1);
  const b = page();
  b.box.chimeSync(true);
  b.box.listeners.forEach((l) => l.fn({}));
  assert.strictEqual(b.box.chimePlay(), false);
});

test("chime: chimeTransitions plays once per new blocked cause and not when the pref is off", () => {
  const { Ctx, log } = fakeAudio("running");
  const { box } = page();
  box.window.AudioContext = Ctx;
  box.prefsOn = true;
  box.readPrefs = () => ({ chime: box.prefsOn });
  box.run("readPrefs = () => ({ chime: prefsOn });");
  box.lastSessions = [];
  box.chimeTransitions(); // seed
  box.listeners.forEach((l) => l.fn({}));
  box.lastSessions = [ask("a")];
  box.chimeTransitions();
  assert.strictEqual(log.osc.length, 2, "one blip = two tones");
  box.chimeTransitions();
  assert.strictEqual(log.osc.length, 2, "same cause, no second blip");
  box.prefsOn = false;
  box.lastSessions = [ask("a"), ask("b")];
  box.chimeTransitions();
  assert.strictEqual(log.osc.length, 2, "pref off: nothing plays");
});
