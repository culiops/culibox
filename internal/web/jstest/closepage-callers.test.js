"use strict";
// Every caller of closePage() (pagehost.js) acts on the answer. closePage asks the open page's leave hook, and the
// profile page's hook asks "Discard unsaved changes?": a caller that went on regardless would open the session, change
// the layout or attach the terminal behind a page the user chose to keep. The contract each caller keeps:
//   - nothing to ask (no page, a phone, nothing unsaved): closePage answers true and the caller goes on in the same
//     tick, with no added await or microtask, so mobile and the clean desktop path keep today's timing;
//   - Keep editing (false, or a question answered no): nothing happens;
//   - Discard: the page is shut, then the action runs (once).
// The callers are big functions of big scripts (tabs.js, app.js, palette.js), so each one's own source is cut out and
// run in a vm against stubs; the stub's first side effect is the "reached" signal.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// fnSource: the top-level function `name` of `file`, as source (sloppy mode, like the page).
function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  const end = text.indexOf("\n}\n", m.index);
  assert.ok(end > m.index, name + " ends at column 0");
  return text.slice(m.index, end + 2);
}

const tick = () => new Promise((r) => setTimeout(r, 0));

// world: a context with the real closePageHolds, a closePage whose answer the test sets (w.verdict: true, false, or a
// promise of either; a question is asked once, so the page is shut by the time the action runs again), and the
// functions under test cut out of their files.
function world(fns, over) {
  const w = { verdict: true, asked: 0 };
  const box = Object.assign({
    console, Promise, Array, Object, Set, WeakSet, JSON,
    window: { innerWidth: 1280 },
    closePage: () => {
      const v = w.verdict;
      if (v && typeof v.then === "function") { w.asked++; w.verdict = true; }
      return v;
    },
  }, over);
  const ctx = vm.createContext(box);
  new vm.Script(fnSource("pagehost.js", "closePageHolds")).runInContext(ctx);
  for (const [file, name] of fns) new vm.Script(fnSource(file, name), { filename: file + ":" + name }).runInContext(ctx);
  return { w, box, ctx, run: (code) => new vm.Script(code).runInContext(ctx) };
}

// scenarios: the three answers for one caller. call() invokes it, reached() counts its first real side effect.
async function scenarios(t, call, reached) {
  // Nothing to ask: the effect has happened by the time the caller returns.
  t.w.verdict = true;
  let before = reached();
  call();
  assert.strictEqual(reached(), before + 1, "clean: same tick");

  // Keep editing, answered at once.
  t.w.verdict = false;
  before = reached();
  call();
  await tick();
  assert.strictEqual(reached(), before, "stay, answered at once: nothing happens");

  // Keep editing, answered after a question.
  t.w.verdict = Promise.resolve(false);
  before = reached();
  call();
  assert.strictEqual(reached(), before, "while the question is open nothing has happened");
  await tick();
  assert.strictEqual(reached(), before, "Keep editing: nothing happens");

  // Discard: the page is shut (the next closePage says true), then the action runs, once.
  t.w.verdict = Promise.resolve(true);
  before = reached();
  call();
  assert.strictEqual(reached(), before, "while the question is open nothing has happened");
  await tick();
  assert.strictEqual(reached(), before + 1, "Discard: it runs once");
}

// --- tabs.js ---------------------------------------------------------------------------------------------------

test("tabs.js openTerminal acts on the answer of closePage", async () => {
  const built = [];
  const t = world([["tabs.js", "openTerminal"]], {
    placeShell: () => ({ action: "own" }), cells: [], cap: 4, focusedCell: null,
    ensureFirstCell() {}, setFocusedCell() {}, renderGrid() {}, refreshSessions() {}, showToast() {},
    Cell: class { constructor() { this.tabs = []; } addTab() {} },
    TerminalTab: class { constructor(id) { built.push(id); } },
  });
  await scenarios(t, () => t.box.openTerminal("s1", "one"), () => built.length);
});

test("tabs.js toggleMax acts on the answer of closePage", async () => {
  const laid = [];
  const t = world([["tabs.js", "toggleMax"]], { maximized: null, renderGrid: () => laid.push(1), scheduleSaveLayout() {} });
  const cell = {};
  await scenarios(t, () => { t.box.maximized = null; t.box.toggleMax(cell); }, () => laid.length);
  t.box.maximized = null;
  t.w.verdict = Promise.resolve(true);
  t.box.toggleMax(cell);
  await tick();
  assert.strictEqual(t.box.maximized, cell, "and it did what it does");
});

test("tabs.js applyCap acts on the answer of closePage", async () => {
  const laid = [];
  const t = world([["tabs.js", "applyCap"]], {
    cap: 1, cells: [], focusedCell: null, maximized: null,
    document: { querySelectorAll: () => [] },
    setFocusedCellRaw() {}, ensureFirstCell() {}, renderGrid: () => laid.push(1), refreshSessions() {}, scheduleSaveLayout() {}, syncFocusedTree() {}, renderSteerRail() {},
  });
  await scenarios(t, () => t.box.applyCap(2), () => laid.length);
  assert.strictEqual(t.box.cap, 2);
});

test("tabs.js openPaneView acts on the answer of closePage; a held view answers true so no caller falls back to opening the terminal", async () => {
  const views = [];
  const cell = { tabs: [{ kind: "terminal", id: "s1" }], dock: null, activate() {}, setView: (k) => views.push(k) };
  const t = world([["tabs.js", "openPaneView"]], {
    mobileActive: () => false, cells: [cell], focusedCell: cell, lastSessions: [],
    paneViewTarget: () => ({ cellIndex: 0, sid: "s1" }), cellTerminal: (c) => c.tabs[0], setFocusedCell() {}, openTerminal() { throw new Error("must not open"); },
  });
  await scenarios(t, () => t.box.openPaneView("s1", "access"), () => views.length);
  t.w.verdict = false;
  assert.strictEqual(t.box.openPaneView("s1", "access"), true, "held: handled, the caller's own fallback must not run");
  t.w.verdict = true;
  assert.strictEqual(t.box.openPaneView("s1", "access"), true);
  // A phone has no views, and never reaches the page.
  const p = world([["tabs.js", "openPaneView"]], { mobileActive: () => true, closePage: () => { throw new Error("not asked"); } });
  assert.strictEqual(p.box.openPaneView("s1", "access"), false);
});

test("tabs.js dockShellBesideParent acts on the answer of closePage; held answers true, and Discard falls back to opening the shell", async () => {
  const docked = [];
  const opened = [];
  const cell = { dockShell: (id) => docked.push(id), setDockPlacement() {} };
  const plan = { action: "dock" };
  const t = world([["tabs.js", "dockShellBesideParent"]], {
    placeShell: () => plan, cells: [cell], setFocusedCell() {}, openTerminal: (id) => opened.push(id),
  });
  plan.cellIndex = 0;
  await scenarios(t, () => t.box.dockShellBesideParent({ id: "sh1", name: "sh", parent_id: "p1" }), () => docked.length);
  t.w.verdict = false;
  assert.strictEqual(t.box.dockShellBesideParent({ id: "sh1", name: "sh", parent_id: "p1" }), true, "held: not 'no pane holds the parent'");
  // No pane holds the parent: clean, it says so (false) and the caller decides; after a Discard it opens the shell itself.
  plan.action = "own";
  t.w.verdict = true;
  assert.strictEqual(t.box.dockShellBesideParent({ id: "sh2", name: "sh", parent_id: "p1" }), false);
  assert.deepStrictEqual(opened, []);
  t.w.verdict = Promise.resolve(true);
  assert.strictEqual(t.box.dockShellBesideParent({ id: "sh2", name: "sh", parent_id: "p1" }), true);
  await tick();
  assert.deepStrictEqual(opened, ["sh2"]);
});

// --- app.js ----------------------------------------------------------------------------------------------------

test("app.js toggleDrawer and openDrawer act on the answer of closePage", async () => {
  const applied = [];
  const base = { mobileActive: () => false, applyDrawer: (d) => applied.push(d), drawerFor: () => ({}), drawerState: {} };
  const t = world([["app.js", "drawerDesktop"], ["app.js", "toggleDrawer"]], base);
  await scenarios(t, () => t.box.toggleDrawer("files", "s1"), () => applied.length);
  const o = world([["app.js", "drawerDesktop"], ["app.js", "openDrawer"]], base);
  await scenarios(o, () => o.box.openDrawer("files", "s1"), () => applied.length);
  // A phone has no drawer and never reaches the page.
  const p = world([["app.js", "drawerDesktop"], ["app.js", "toggleDrawer"]], { mobileActive: () => true, closePage: () => { throw new Error("not asked"); } });
  assert.strictEqual(p.box.toggleDrawer("files", "s1"), undefined);
});

test("app.js spawnShellIn acts on the answer of closePage, before anything is started", async () => {
  const ran = [];
  const t = world([["app.js", "spawnShellIn"]], {
    window: { shellProfile: "shell", innerWidth: 1280 }, shellSpawning: new WeakSet(), withPending: (_el, _l, fn) => fn(), spawnShellRun: (...a) => { ran.push(a); },
  });
  const cell = {};
  await scenarios(t, () => t.box.spawnShellIn(cell, "below"), () => ran.length);
});

test("app.js spawnSession asks BEFORE the POST: Keep editing starts nothing and says so, Discard starts it once, clean starts it in the same tick", async () => {
  const posts = [];
  const made = [];
  const t = world([["app.js", "spawnSession"]], {
    api: (url) => { posts.push(url); return Promise.resolve({ id: "s9", name: "n" }); },
    LAST_SPAWN_KEY: "k", localStorage: { setItem() {} }, deskStorage: () => ({}), writeRecent() {}, readRecent: () => [], pushRecentProfile: () => [],
    refreshSessions: () => Promise.resolve(), openTerminal: (id) => made.push(id),
  });
  const req = { agent: "claude" };
  const created = [];
  const call = () => t.box.spawnSession(req, { toast: false, onCreated: (s) => created.push(s.id) });
  // Clean: the POST is out by the time spawnSession returns its promise (no await in front of it).
  t.w.verdict = true;
  const p = call();
  assert.deepStrictEqual(posts, ["/api/sessions"], "clean: the POST goes out synchronously");
  assert.strictEqual((await p).id, "s9");
  assert.deepStrictEqual(created, ["s9"]);

  // Keep editing, at once or after the question: no POST, no onCreated (the spawn dialog stays open with its form), null.
  for (const v of [false, Promise.resolve(false)]) {
    posts.length = 0; created.length = 0; made.length = 0;
    t.w.verdict = v;
    assert.strictEqual(await call(), null);
    assert.deepStrictEqual([posts, created, made], [[], [], []]);
  }

  // Discard: asked once, then the POST, then the session opens; the question is never asked a second time.
  posts.length = 0; created.length = 0; made.length = 0; t.w.asked = 0;
  t.w.verdict = Promise.resolve(true);
  const s = await call();
  assert.strictEqual(s.id, "s9");
  assert.deepStrictEqual([posts, created, made, t.w.asked], [["/api/sessions"], ["s9"], ["s9"], 1]);
});

test("accounts.js acctLogin does not claim a login session started when the spawn was held", async () => {
  const log = [];
  const t = world([["accounts.js", "acctLogin"]], {
    setAcctErr: (m) => { if (m) log.push("err:" + m); }, setAcctNote() {}, acctLoginProfile: () => ({ name: "claude" }), profiles: [], spawnPayload: (x) => x,
    spawnSession: () => Promise.resolve(null), closeDialog: (id) => log.push("close:" + id), showToast: (m) => log.push("toast:" + m),
  });
  await t.box.acctLogin("work");
  assert.deepStrictEqual(log, [], "no 'Run /login' toast and no Settings close for a session that never started");
  t.box.spawnSession = () => Promise.resolve({ id: "s1" });
  await t.box.acctLogin("work");
  assert.deepStrictEqual(log, ["close:settings-modal", "toast:Run /login in the new session"]);
});

// --- palette.js ------------------------------------------------------------------------------------------------

test("palette.js view commands open the view only after closePage says the page is shut", async () => {
  const toggled = [];
  const t = world([["palette.js", "paletteOpenView"]], { focusedCell: { toggleView: (k) => toggled.push(k) } });
  await scenarios(t, () => t.box.paletteOpenView("activity"), () => toggled.length);
  assert.ok(toggled.every((k) => k === "activity"));
  // And both palette entries go through it.
  const pal = src("palette.js");
  assert.match(pal, /activityView: \(\) => paletteOpenView\("activity"\)/);
  assert.match(pal, /accessView: \(\) => paletteOpenView\("access"\)/);
});

// --- every caller is covered -------------------------------------------------------------------------------------

test("no bare closePage() statement is left in a caller that acts afterwards", () => {
  // The only places closePage() may be called without acting on the answer: pagehost.js itself and its documentation.
  // Every other file calls closePageHolds (or reads the answer, as spawnSession and openPage do).
  const bare = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "pagehost.js")) {
    src(f).split("\n").forEach((line, i) => {
      if (/^\s*closePage\(\);/.test(line)) bare.push(f + ":" + (i + 1) + ": " + line.trim());
    });
  }
  assert.deepStrictEqual(bare, []);
});
