"use strict";
// The Files dock's tabs as filesdrawer.js runs them, in a vm: the real filesdrawer.js, docktabs.js, dockcrumbs.js and
// drawerfiles.js, with a stand-in ViewerTab and a fake page. The pure rules (preview replacement, pinning, closing, what is
// stored) are docktabs.test.js's; this file proves the glue obeys them: which viewers exist, what asks, what is written and
// when. dock-strip.test.js draws the strip; dock-tabs-css.test.js pins the markup and wiring; the visual check proves it
// on screen.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const KEY = "agent-box:dockTabs"; // docktabs.js DOCK_TABS_KEY (docktabs.test.js pins it; a top-level const is not reachable from outside the vm)
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// A page: `st` is what a test reads (calls, timers, asks, the store), `run` evaluates code in the page's scope.
function page(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  const nodes = {};
  const node = (id) => (nodes[id] = nodes[id] || Object.assign(new Node("div", doc), { id }));
  for (const id of ["drawer", "drawer-vhost", "drawer-vempty", "dock-bar"]) node(id);
  doc.getElementById = (id) => nodes[id] || null;
  const st = { strip: [], crumbs: [], asks: [], toasts: [], timers: [], store: new Map(), revealed: [], follow: 0, made: [], answer: true, opened: [], full: false };
  const store = { getItem: (k) => (st.store.has(k) ? st.store.get(k) : null), setItem: (k, v) => { st.store.set(k, v); } };
  const handlers = {};
  class ViewerTab {
    constructor(p, opts) {
      this.opts = opts || {};
      this.path = p;
      this.box = this.opts.box || "";
      this.name = (this.box ? "box · " : "") + (p.split("/").pop() || p);
      this.el = new Node("div", doc);
      this.scroll = this.opts.scroll || 0;
      this.changes = this.opts.view === "changes";
      this.fileScroll = 0;
      this.editing = false;
      this.cm = null;
      this.auto = false;
      this.disposed = false;
      this.calls = [];
      this.toolbarApi = { paint: () => { this.calls.push("toolbar"); } };
      st.made.push(this);
    }
    dirty() { return this.editing && this.typed; }
    dispose() { this.disposed = true; }
    onHide() { this.calls.push("hide"); }
    onShow() { this.calls.push("show"); }
    onAttach() { this.calls.push("attach"); }
    focusReader() { this.calls.push("focus"); }
    revealLine(l, c) { this.calls.push(["line", l, c]); }
    showChanges() { this.calls.push("changes"); }
    toggleAuto() { this.auto = !this.auto; }
    changed() { this.calls.push("changed"); if (this.onUpdate) this.onUpdate(); }
    exitEdit() { this.editing = false; this.typed = false; this.cm = null; }
    // What the real enterEdit does: the editor comes up, then the host hears of it.
    edit(pin = true) {
      this.editing = true;
      this.typed = false;
      this.cm = { gen: 1, changeGeneration() { return this.gen; }, isClean(g) { return g === this.gen; }, refresh() {} };
      if (pin && this.opts.host && this.opts.host.onEditStart) this.opts.host.onEditStart(this);
    }
    type() { this.typed = true; this.cm.gen++; }
  }
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, Date,
    document: doc,
    window: { addEventListener: (ev, fn) => { handlers[ev] = fn; } },
    ViewerTab,
    TIER_BOX: "box",
    setTimeout: (fn, ms) => { st.timers.push({ fn, ms }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers[id - 1]) st.timers[id - 1].dead = true; },
    askConfirm: async (o) => { st.asks.push(o); return st.answer; },
    showToast: (m) => { st.toasts.push(m); },
    savedScroll: (t) => (t.changes ? t.fileScroll : t.scroll),
    normalizePath: (p) => p,
    drawerDesktop: () => true,
    drawerLiveSids: () => ["a", "b"],
    drawerSessionName: (id) => ({ a: "alpha", b: "beta" }[id] || ""),
    drawerStorage: () => store,
    lastSessions: [{ id: "a", name: "alpha" }, { id: "b", name: "beta" }],
    cellTerminal: () => null,
    focusedCell: {},
    dockForcesFull: () => st.full,
    syncFocusedTree: () => { st.follow++; },
    dockTreeFold: () => {},
    dockTreeReveal: () => { st.revealed.push("column"); },
    ftRoot: () => "/work/proj",
    ftReveal: async (p) => { st.revealed.push(p); return true; },
    relativeTo: (r, p) => p,
    gitStatusCodeFor: () => "",
    dockStripPaint: (items) => { st.strip.push(items); },
    dockCrumbsPaint: (crumbs, edited, on) => { st.crumbs.push({ crumbs, edited, on }); },
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  for (const f of ["drawerfiles.js", "docktabs.js", "dockcrumbs.js"]) load(src(f), f);
  load('let drawerState = { open: "files", sessionId: "a", pinned: false };', "state");
  load(src("filesdrawer.js"), "filesdrawer.js");
  const p = {
    ctx, st, doc, nodes, handlers,
    run: (code) => load(code, "test"),
    flush: () => { // run the timers that are due, once each
      const due = st.timers.filter((t) => !t.dead && !t.ran);
      for (const t of due) { t.ran = true; t.fn(); }
      return due.length;
    },
    show: (path, opts) => ctx.drawerShowFile(path, opts),
    state: (sid) => ctx.dockStateOf(sid === undefined ? "a" : sid),
    // Top-level let and const of the page's scripts are not properties of the vm's global: read and write them in it.
    get: (name) => load(name, "get"),
    set: (name, value) => { ctx.__v = value; load(name + " = __v", "set"); },
    live: () => [...load("dockViewers").values()],
    viewer: (path) => [...load("dockViewers").values()].find((v) => v.path === path) || null,
    drawer: (next) => { // a drawer state change, the way app.js does it
      const prev = load("drawerState");
      ctx.__v = next;
      load("drawerState = __v", "set");
      ctx.drawerFilesApply(prev, next);
    },
    live_: (...sids) => { ctx.drawerLiveSids = () => sids; },
    tabs: (sid) => ctx.dockStateOf(sid === undefined ? "a" : sid).tabs.map((t) => [t.path, t.preview]),
  };
  // openDrawer as app.js does it for the dock: the state changes, then drawerFilesApply runs.
  ctx.openDrawer = (kind, sid) => { st.opened.push([kind, sid]); p.drawer({ open: kind, sessionId: sid, pinned: false }); };
  p.restore = (sessions, raw) => { if (raw !== undefined) st.store.set(KEY, raw); ctx.dockRestore(sessions || [{ id: "a" }, { id: "b" }]); };
  return p;
}

const files = (n) => Array.from({ length: n }, (_, i) => "/work/proj/f" + (i + 1) + ".go");
const names = (arr) => arr.map((v) => v.path);

// --- the preview rule -----------------------------------------------------------------------------------------------

test("three single clicks leave one italic preview tab and one viewer: each replaces the one before", () => {
  const p = page();
  const [a, b, c] = files(3);
  p.show(a); p.show(b); p.show(c);
  assert.deepEqual(p.tabs(), [[c, true]]);
  assert.deepEqual(names(p.live()), [c], "the replaced previews' viewers are disposed, not kept");
  assert.equal(p.st.made.filter((v) => v.disposed).length, 2);
  assert.equal(p.nodes["drawer-vhost"].children[0], p.viewer(c).el, "and the one on screen is the last");
});

test("a double-click pins: the next single click opens beside it, and both stay", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a);
  p.show(a, { preview: false }); // the second click of the double-click: the tab exists, so it is pinned where it stands
  assert.deepEqual(p.tabs(), [[a, false]]);
  p.show(b);
  assert.deepEqual(p.tabs(), [[a, false], [b, true]]);
  assert.deepEqual(names(p.live()).sort(), [a, b]);
});

test("an edit pins the tab (host.onEditStart), so the next single click opens another one and the edit stays", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a);
  const va = p.viewer(a);
  va.edit();
  va.type();
  assert.deepEqual(p.tabs(), [[a, false]], "pinned by the first edit, not by the first keystroke");
  p.show(b);
  assert.deepEqual(p.tabs(), [[a, false], [b, true]]);
  assert.equal(va.disposed, false);
  assert.equal(va.dirty(), true);
});

test("a dirty preview tab is never replaced silently, even if the host was never told of its edit", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a);
  const va = p.viewer(a);
  va.edit(false); // the edit began and nothing pinned the tab
  va.type();
  assert.deepEqual(p.tabs(), [[a, true]]);
  p.show(b);
  assert.deepEqual(p.tabs(), [[a, false], [b, true]], "pinned by the open that would have replaced it");
  assert.equal(va.disposed, false);
  assert.equal(va.dirty(), true);
  assert.equal(p.st.asks.length, 0, "opening a file never asks");
});

test("opening a file with a line or the diff tells a viewer that is already open, and builds a new one with them", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { line: 12, col: 5, alt: { base: "/x", rel: "a.go" } });
  const va = p.viewer(a);
  assert.equal(va.opts.line, 12);
  assert.equal(va.opts.col, 5);
  assert.deepEqual(va.opts.alt, { base: "/x", rel: "a.go" });
  assert.equal(va.opts.restored, false, "someone asked for it");
  p.show(a, { line: 30, view: "changes" });
  assert.deepEqual(va.calls.filter((c) => Array.isArray(c) || c === "changes"), [["line", 30, undefined], "changes"]);
  p.show(b, { view: "changes" });
  assert.equal(p.viewer(b).opts.view, "changes");
});

test("a box file is its own tab, keyed by the session, and its viewer is built in that box", () => {
  const p = page();
  const f = "/work/proj/shot.png";
  p.show(f);
  p.show(f, { box: "s1", preview: false });
  assert.deepEqual(p.state().tabs.map((t) => t.key), ["v:" + f, "b:s1:" + f]);
  assert.equal(p.viewer(f), p.live()[0]);
  assert.equal(p.live()[1].box, "s1");
});

test("showing nothing: no path is no tab", () => {
  const p = page();
  assert.equal(p.show(""), false);
  assert.equal(p.show(undefined), false);
  assert.deepEqual(p.state().tabs, []);
});

// --- tabs of one session -------------------------------------------------------------------------------------------

test("switching between tabs keeps a dirty viewer alive: hidden and detached, then back with the edit", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  const va = p.viewer(a);
  va.edit();
  va.type();
  p.show(b, { preview: false });
  const vb = p.viewer(b);
  assert.ok(va.calls.includes("hide"), "onHide on the viewer that was left");
  assert.equal(va.disposed, false);
  assert.notEqual(va.el.parent, p.nodes["drawer-vhost"], "detached while hidden");
  assert.equal(p.nodes["drawer-vhost"].children[0], vb.el);
  p.ctx.dockActivateTab("v:" + a);
  assert.equal(p.nodes["drawer-vhost"].children[0], va.el, "the same viewer comes back");
  assert.equal(p.live().length, 2);
  assert.equal(va.dirty(), true, "and it is still dirty");
  assert.equal(va.calls.at(-1), "focus", "a click on a tab goes to reading that file");
  assert.equal(va.calls.filter((c) => c === "attach").length, 2, "onAttach each time it is put back (the scroll comes back)");
});

test("a tab with no viewer yet gets one when it is first shown, and it is a restored one", () => {
  const p = page();
  const [a, b] = files(2);
  p.restore(null, JSON.stringify({ v: 1, open: false, sessions: { a: { tabs: [{ path: a, scroll: 7 }, { path: b, scroll: 9 }], active: "v:" + a } } }));
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  assert.deepEqual(names(p.live()), [a], "one viewer, for the active tab: not one per stored tab");
  assert.equal(p.viewer(a).opts.scroll, 7);
  assert.equal(p.viewer(a).opts.restored, true, "nobody asked for it just now: a gone file closes it");
  p.ctx.dockActivateTab("v:" + b);
  assert.deepEqual(names(p.live()).sort(), [a, b]);
  assert.equal(p.viewer(b).opts.scroll, 9);
});

test("closing a clean tab closes it; the right neighbour (else the left) is shown", () => {
  const p = page();
  const [a, b, c] = files(3);
  for (const f of [a, b, c]) p.show(f, { preview: false });
  p.ctx.dockActivateTab("v:" + b);
  return p.ctx.dockCloseTab("v:" + b).then(() => {
    assert.deepEqual(p.tabs().map((t) => t[0]), [a, c]);
    assert.equal(p.state().active, "v:" + c);
    assert.equal(p.st.asks.length, 0);
    assert.deepEqual(names(p.live()).sort(), [a, c]);
    assert.equal(p.nodes["drawer-vhost"].children[0], p.viewer(c).el);
  });
});

test("closing a dirty tab asks: Cancel keeps the tab and the edit, Discard closes it", async () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  const va = p.viewer(a);
  va.edit();
  va.type();
  p.show(b, { preview: false });
  p.st.answer = false;
  await p.ctx.dockCloseTab("v:" + a);
  assert.equal(p.st.asks.length, 1);
  assert.match(p.st.asks[0].title, /a\.go|f1\.go/);
  assert.deepEqual(p.tabs().map((t) => t[0]), [a, b]);
  assert.equal(va.dirty(), true);
  p.st.answer = true;
  await p.ctx.dockCloseTab("v:" + a);
  assert.equal(p.st.asks.length, 2);
  assert.deepEqual(p.tabs().map((t) => t[0]), [b]);
  assert.equal(va.disposed, true);
});

test("closing the last tab empties the column", async () => {
  const p = page();
  p.show(files(1)[0]);
  await p.ctx.dockCloseTab(p.state().active);
  assert.deepEqual(p.state().tabs, []);
  assert.equal(p.nodes["drawer-vhost"].hidden, true);
  assert.equal(p.nodes["drawer-vempty"].hidden, false);
});

test("a tab pinned by a double-click that is not a preview changes nothing and writes nothing", () => {
  const p = page();
  p.restore();
  p.show(files(1)[0], { preview: false });
  p.flush();
  const before = p.st.store.get(KEY);
  p.st.store.delete(KEY);
  p.ctx.dockPinTab(p.state().active);
  assert.equal(p.flush(), 0, "no write is scheduled");
  assert.equal(p.st.store.has(KEY), false);
  assert.ok(before);
});

// --- the dirty gate -------------------------------------------------------------------------------------------------

function dirtyTwo() {
  const p = page();
  const [a, b, c] = files(3);
  p.show(a, { preview: false });
  p.show(b, { preview: false });
  p.show(c, { preview: false }); // c is shown; a is the background tab with an edit
  const va = p.viewer(a);
  va.edit();
  va.type();
  return { p, va, a, b, c };
}

test("any dirty tab of the shown session asks on a session switch, a close and a follow, not only the shown one", async () => {
  const { p } = dirtyTwo();
  assert.equal(p.ctx.drawerHasUnsaved(), true);
  const to = { open: "files", sessionId: "b", pinned: false };
  assert.equal(p.ctx.drawerWouldDiscard(p.get("drawerState"), to), true, "another session");
  assert.equal(p.ctx.drawerWouldDiscard(p.get("drawerState"), { open: null, sessionId: "a" }), true, "closing the dock");
  assert.equal(p.ctx.drawerWouldDiscard(p.get("drawerState"), { open: "files", sessionId: "a" }), false, "the same session: tabs switch freely");
  assert.equal(p.ctx.drawerHoldOnFollow(to), true, "a follow-focus move holds");
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(p.st.asks.length, 1, "and asks once");
  assert.equal(p.ctx.drawerHoldOnFollow(to), true, "a Cancel is kept quietly until something changes");
  assert.equal(p.st.asks.length, 1);
});

test("the ask names the file, or how many files when several hold edits", async () => {
  const { p, a, b } = dirtyTwo();
  p.ctx.drawerConfirmLeave();
  assert.match(p.st.asks[0].title, /f1\.go/);
  assert.equal(p.st.asks[0].confirmLabel, "Discard");
  await new Promise((r) => setImmediate(r));
  const vb = p.viewer(b);
  vb.edit();
  vb.type();
  await p.ctx.drawerConfirmLeave();
  assert.match(p.st.asks[1].title, /2 files/);
  assert.ok(a);
});

test("Discard on a follow lets the dock go on to the other session; Cancel keeps it until a buffer changes or another turns dirty", async () => {
  const { p, va, b } = dirtyTwo();
  const to = { open: "files", sessionId: "b", pinned: false };
  p.st.answer = false;
  p.ctx.drawerHoldOnFollow(to);
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.asks.length, 1);
  p.ctx.drawerHoldOnFollow(to);
  assert.equal(p.st.asks.length, 1, "kept");
  va.type(); // the buffer changed since
  p.ctx.drawerHoldOnFollow(to);
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.asks.length, 2, "asked again");
  p.st.answer = false;
  const vb = p.viewer(b);
  vb.edit();
  vb.type(); // a second tab turned dirty after the Cancel
  p.ctx.drawerHoldOnFollow(to);
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.asks.length, 3, "a tab that was not dirty at the Cancel is not covered by it");
  p.st.answer = true;
  va.type();
  p.ctx.drawerHoldOnFollow(to);
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.asks.length, 4);
  assert.equal(p.st.follow, 1, "Discard follows focus (syncFocusedTree)");
  assert.equal(va.editing, false);
  assert.equal(vb.editing, false);
});

test("the page's beforeunload guard sees a dirty background tab, with the dock closed or detached", () => {
  const { p } = dirtyTwo();
  p.drawer({ open: null, sessionId: "a", pinned: false });
  assert.equal(p.ctx.drawerHasUnsaved(), false, "a close that parks has asked first; nothing dirty is left");
  const q = dirtyTwo();
  q.p.ctx.drawerFilesDetach();
  assert.equal(q.p.ctx.drawerHasUnsaved(), true, "a kept buffer still holds the page");
});

// --- sessions ---------------------------------------------------------------------------------------------------------

test("switching session parks the shown session's viewers (scroll remembered) and shows the other's tabs", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b, { preview: false });
  p.viewer(a).scroll = 120;
  p.viewer(b).scroll = 30;
  p.drawer({ open: "files", sessionId: "b", pinned: false });
  assert.deepEqual(p.live(), [], "no viewer of session a stays");
  assert.deepEqual(p.state("a").tabs.map((t) => t.scroll), [120, 30], "scroll kept in the state");
  assert.equal(p.get("dockViewersSid"), null);
  p.show(files(3)[2], { preview: false });
  assert.deepEqual(p.tabs("b").map((t) => t[0]), [files(3)[2]]);
  assert.deepEqual(p.tabs("a").map((t) => t[0]), [a, b], "session a's tabs are not touched");
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  assert.deepEqual(names(p.live()), [b], "back on a: one viewer, the active tab's, at its scroll");
  assert.equal(p.viewer(b).opts.scroll, 30);
  assert.equal(p.viewer(b).opts.restored, true);
});

test("closing the dock and opening it again keeps the tabs", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b);
  p.drawer({ open: null, sessionId: "a", pinned: false });
  assert.deepEqual(p.live(), []);
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  assert.deepEqual(p.tabs(), [[a, false], [b, true]]);
  assert.deepEqual(names(p.live()), [b]);
});

test("a pane of files has no session: its tabs live in memory under null and are never stored", () => {
  const p = page();
  p.restore();
  p.drawer({ open: "files", sessionId: null, pinned: false });
  p.show(files(1)[0], { preview: false });
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  p.show(files(2)[1], { preview: false });
  p.flush();
  const saved = JSON.parse(p.st.store.get(KEY));
  assert.deepEqual(Object.keys(saved.sessions), ["a"]);
  assert.equal(p.state(null).tabs.length, 1);
});

test("the mobile crossing parks clean viewers and keeps one with an edit, whole and detached; the next dock puts it back", () => {
  const { p, va, a, b } = dirtyTwo();
  p.ctx.drawerFilesDetach();
  assert.deepEqual(names(p.live()), [a], "the clean ones went, the dirty one stayed");
  assert.equal(va.disposed, false);
  assert.notEqual(va.el.parent, p.nodes["drawer-vhost"]);
  p.set("drawerState", { open: null, sessionId: null, pinned: false }); // restoreDrawerNodes resets it directly
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  assert.equal(p.viewer(a), va, "the very viewer");
  assert.equal(va.dirty(), true);
  assert.ok(b);
});

// --- what is stored -----------------------------------------------------------------------------------------------------

test("tabs are written 300 ms after the last change, with whether the dock is open, and as one write", () => {
  const p = page();
  p.restore();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b);
  const due = p.st.timers.filter((t) => !t.dead);
  assert.equal(due.length, 1, "one timer for both changes");
  assert.equal(due[0].ms, 300);
  assert.equal(p.st.store.has(KEY), false, "nothing yet");
  p.flush();
  const saved = JSON.parse(p.st.store.get(KEY));
  assert.equal(saved.v, 1);
  assert.equal(saved.open, true);
  assert.deepEqual(saved.sessions.a.tabs.map((t) => [t.path, t.preview]), [[a, false], [b, true]]);
  assert.equal(saved.sessions.a.active, "v:" + b);
});

test("nothing is written before the stored tabs were read: an early change must not replace them with nothing", () => {
  const p = page();
  const [a, b] = files(2);
  const stored = JSON.stringify({ v: 1, open: false, sessions: { b: { tabs: [{ path: b, scroll: 5 }], active: "v:" + b } } });
  p.st.store.set(KEY, stored);
  p.show(a, { preview: false }); // the dock was used before the session list arrived
  assert.equal(p.st.timers.filter((t) => !t.dead).length, 0, "no write is scheduled");
  p.handlers.pagehide();
  assert.equal(p.st.store.get(KEY), stored, "and a reload now writes nothing either");
  p.restore();
  assert.deepEqual(p.tabs("a").map((t) => t[0]), [a], "the page's own tabs win");
  assert.deepEqual(p.tabs("b").map((t) => t[0]), [b], "and the stored ones that were not touched come back");
  p.show(files(3)[2], { preview: false });
  p.flush();
  const saved = JSON.parse(p.st.store.get(KEY));
  assert.deepEqual(Object.keys(saved.sessions).sort(), ["a", "b"]);
});

test("restore reads only the sessions the list names; the rest are dropped, and the next write is without them", () => {
  const p = page();
  const [a, b] = files(2);
  p.restore([{ id: "a" }], JSON.stringify({
    v: 1, open: true,
    sessions: { a: { tabs: [{ path: a }], active: "v:" + a }, gone: { tabs: [{ path: b }], active: "v:" + b } },
  }));
  assert.deepEqual(p.tabs("a").map((t) => t[0]), [a]);
  assert.deepEqual(p.tabs("gone"), []);
  p.show(b, { preview: false });
  p.flush();
  assert.deepEqual(Object.keys(JSON.parse(p.st.store.get(KEY)).sessions), ["a"]);
});

test("restore survives garbage, absent storage and a throwing store", () => {
  for (const raw of ["", "{", "null", "[]", '{"v":9}']) {
    const p = page();
    p.restore(null, raw);
    assert.deepEqual(p.state().tabs, []);
  }
  const p = page({ drawerStorage: () => ({ getItem() { throw new Error("denied"); }, setItem() { throw new Error("denied"); } }) });
  p.restore();
  p.show(files(1)[0], { preview: false });
  assert.doesNotThrow(() => p.flush());
  assert.equal(page({ drawerStorage: () => null }).ctx.dockStorage(), null);
});

test("a scroll alone writes nothing by itself, and the next save and pagehide both take it", () => {
  const p = page();
  p.restore();
  const a = files(1)[0];
  p.show(a, { preview: false });
  p.flush();
  p.viewer(a).scroll = 333;
  assert.equal(p.flush(), 0, "no timer for a scroll");
  p.handlers.pagehide();
  assert.equal(JSON.parse(p.st.store.get(KEY)).sessions.a.tabs[0].scroll, 333);
  p.viewer(a).scroll = 444;
  p.show(a, { preview: false, line: 3 }); // not a change of state
  p.st.store.delete(KEY);
  p.ctx.dockSaveNow();
  assert.equal(JSON.parse(p.st.store.get(KEY)).sessions.a.tabs[0].scroll, 444, "a save syncs the scroll first");
});

test("an idle page writes nothing on pagehide: it cannot overwrite what another page stored", () => {
  const p = page();
  p.restore();
  p.st.store.set(KEY, "from another page");
  p.handlers.pagehide();
  assert.equal(p.st.store.get(KEY), "from another page");
});

test("opening and closing the dock is a change too: the stored form says whether it is open", () => {
  const p = page();
  p.restore();
  p.show(files(1)[0], { preview: false });
  p.flush();
  p.drawer({ open: null, sessionId: "a", pinned: false });
  assert.equal(p.flush(), 1);
  assert.equal(JSON.parse(p.st.store.get(KEY)).open, false);
});

test("restore with the dock already open on a session shows the restored tabs (the list arrived late)", () => {
  const p = page();
  const a = files(1)[0];
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  p.restore(null, JSON.stringify({ v: 1, open: false, sessions: { a: { tabs: [{ path: a }], active: "v:" + a } } }));
  assert.deepEqual(names(p.live()), [a]);
});

// --- files that go ------------------------------------------------------------------------------------------------------

test("a file deleted in the app closes its tabs in every session, a folder takes the files under it, a box file stays", () => {
  const p = page();
  p.show("/work/proj/dir/x.go", { preview: false });
  p.show("/work/proj/dir/y.go", { preview: false });
  p.show("/work/proj/z.go", { preview: false });
  p.show("/work/proj/dir/x.go", { box: "s1", preview: false });
  p.drawer({ open: "files", sessionId: "b", pinned: false });
  p.show("/work/proj/dir/x.go", { preview: false });
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  p.ctx.dockForget("/work/proj/dir");
  assert.deepEqual(p.tabs("a").map((t) => t[0]), ["/work/proj/z.go", "/work/proj/dir/x.go"], "the host files under it go, the box file stays");
  assert.deepEqual(p.tabs("b"), [], "in another session too");
  assert.equal(p.state("a").tabs.length, 2);
  assert.equal(p.live().length, 1, "the dropped viewers went with their tabs (the survivors have none until shown)");
  p.ctx.dockForget("");
  p.ctx.dockForget("/work/proj/dirx"); // a sibling that only shares the prefix
  assert.equal(p.state("a").tabs.length, 2);
});

test("a viewer that closes itself (a restored file gone) closes its tab and the next one is shown; a stale viewer is a no-op", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b, { preview: false });
  const vb = p.viewer(b);
  p.ctx.dockCloseViewer(vb);
  assert.deepEqual(p.tabs().map((t) => t[0]), [a]);
  assert.equal(vb.disposed, true);
  assert.equal(p.ctx.dockHasViewer(vb), false);
  assert.doesNotThrow(() => p.ctx.dockCloseViewer(vb), "a disposed viewer's late 404 changes nothing");
  assert.equal(p.tabs().length, 1);
});

test("a viewer re-pointed at another checkout keeps its tab under the new path; one already open closes", () => {
  const p = page();
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b, { preview: false });
  const va = p.viewer(a);
  assert.equal(p.ctx.dockRepointViewer(va, "/other/a.go", { line: 4, col: 2 }), true);
  assert.deepEqual(p.tabs().map((t) => t[0]), ["/other/a.go", b]);
  assert.equal(p.state().tabs[0].key, "v:/other/a.go");
  assert.equal(va.path, "/other/a.go");
  assert.equal(va.id, "view:/other/a.go");
  assert.equal(va.name, "a.go");
  assert.equal(p.viewer("/other/a.go"), va, "the same viewer, found under its new key");
  p.ctx.dockActivateTab("v:/other/a.go");
  assert.equal(p.state().active, "v:/other/a.go");
  // Now the path of b is asked for by va: b's tab exists, so va's tab closes and b is shown at the line.
  assert.equal(p.ctx.dockRepointViewer(va, b, { line: 9, col: 1 }), false);
  assert.deepEqual(p.tabs().map((t) => t[0]), [b]);
  assert.equal(va.disposed, true);
  assert.deepEqual(p.viewer(b).calls.filter(Array.isArray), [["line", 9, 1]]);
  assert.equal(p.ctx.dockRepointViewer({ not: "a dock viewer" }, "/x", null), true);
});

// --- what the strip and the crumbs are told ----------------------------------------------------------------------------------

test("dockRender: the strip gets the tabs with the dot only for a viewer of the shown session, and the crumbs get the active file", () => {
  const p = page({ gitStatusCodeFor: (f) => (f.endsWith("f1.go") ? "M" : "") });
  const [a, b] = files(2);
  p.show(a, { preview: false });
  p.show(b);
  p.viewer(a).edit();
  p.viewer(a).type();
  p.ctx.dockRender();
  const items = p.st.strip.at(-1);
  assert.deepEqual(items.map((i) => [i.name, i.letter, i.dirty, i.preview, i.active]), [["f1.go", "M", true, false, false], ["f2.go", "", false, true, true]]);
  const c = p.st.crumbs.at(-1);
  assert.deepEqual(c.crumbs.map((x) => x.name), ["proj", "f2.go"], "from the tree's root");
  assert.equal(typeof c.on.reveal, "function");
  assert.equal(c.edited, "");
});

test("dockRender: the Edited-by label comes from this session's Activity files; not named, nothing", () => {
  const p = page();
  const [a, b] = files(2);
  p.get("drawerChangedFiles").set("a", [{ path: a, add: 1, del: 0, ts: Date.now() - 125000 }]);
  p.show(a, { preview: false });
  assert.equal(p.st.crumbs.at(-1).edited, "Edited by alpha · 2 min ago");
  p.show(b, { preview: false });
  assert.equal(p.st.crumbs.at(-1).edited, "");
  p.drawer({ open: "files", sessionId: "b", pinned: false });
  p.show(a, { preview: false });
  assert.equal(p.st.crumbs.at(-1).edited, "", "another session's Activity does not name this file");
});

test("dockRender: a box file's crumbs run from / and are text; with no tab, or the dock closed, nothing is drawn", () => {
  const p = page();
  p.show("/work/out/shot.png", { box: "s1", preview: false });
  const c = p.st.crumbs.at(-1);
  assert.deepEqual(c.crumbs.map((x) => x.name), ["/", "work", "out", "shot.png"]);
  assert.equal(c.on, null);
  p.drawer({ open: null, sessionId: "a", pinned: false });
  assert.deepEqual(p.st.strip.at(-1), []);
  assert.deepEqual(p.st.crumbs.at(-1).crumbs, []);
});

test("a crumb click reveals a folder under the root, opens the column for the root, and never throws for one outside", async () => {
  const p = page();
  p.ctx.dockCrumbsReveal("/work/proj/src");
  assert.deepEqual(p.st.revealed, ["column", "/work/proj/src"]);
  p.st.revealed.length = 0;
  p.ctx.dockCrumbsReveal("/work/proj");
  assert.deepEqual(p.st.revealed, ["column"], "the root: the column, no reveal");
  p.st.revealed.length = 0;
  for (const outside of ["/", "/work", "/work/proj2", "/etc"]) {
    assert.doesNotThrow(() => p.ctx.dockCrumbsReveal(outside));
  }
  assert.deepEqual(p.st.revealed, [], "nothing is revealed, the column is not opened, the tree is not re-rooted");
  assert.equal(p.st.toasts.length, 4);
  const q = page({ ftReveal: async () => false });
  q.ctx.dockCrumbsReveal("/work/proj/gone");
  await new Promise((r) => setImmediate(r));
  assert.match(q.st.toasts[0], /Could not show/);
});

test("the viewer's host hears the first edit (pin) and the dirty change (repaint); a pane viewer's has neither", () => {
  const p = page();
  p.show(files(1)[0]);
  const host = p.get("drawerToolbarHost");
  assert.equal(typeof host.onEditStart, "function");
  assert.equal(typeof host.onDirtyChange, "function");
  assert.equal("extras" in host, false, "no Open in pane");
  const n = p.st.strip.length;
  host.onDirtyChange();
  assert.equal(p.st.strip.length, n + 1);
  assert.equal(p.viewer(files(1)[0]).opts.host, host);
});

test("drawerShowFile folds the explorer column the strip opened, as before", () => {
  let folded = 0;
  const p = page({ dockTreeFold: () => { folded++; } });
  p.show(files(1)[0]);
  assert.equal(folded, 1);
});

// --- the dock reopens after a reload when it was open -------------------------------------------------------------------

const STORED = (open) => JSON.stringify({ v: 1, open, sessions: { a: { tabs: [{ path: "/work/proj/f1.go" }], active: "v:/work/proj/f1.go" } } });
// boot: what restoreBoot does, in its order: dockRestore, (the layout), dockReopen. The page starts with the dock closed.
function boot(over, open) {
  const p = page(Object.assign({ cellTerminal: () => ({ id: "a" }) }, over));
  p.set("drawerState", { open: null, sessionId: null, pinned: false });
  p.restore(null, STORED(open));
  p.ctx.dockReopen();
  return p;
}
const storedOpen = (p) => JSON.parse(p.st.store.get(KEY)).open;

test("a dock that was open comes back open on the focused pane's session, through the normal open path, with its tabs", () => {
  const p = boot({}, true);
  assert.deepEqual(p.st.opened, [["files", "a"]]);
  assert.deepEqual(p.get("drawerState"), { open: "files", sessionId: "a", pinned: false });
  assert.deepEqual(names(p.live()), ["/work/proj/f1.go"], "the restored tabs are on screen");
  p.flush();
  assert.equal(storedOpen(p), true, "the write says it is open");
  p.ctx.dockReopen();
  assert.equal(p.st.opened.length, 1, "once: the flag is spent by the open");
});

test("a dock that was closed stays closed", () => {
  const p = boot({}, false);
  assert.deepEqual(p.st.opened, []);
  assert.equal(p.get("drawerState").open, null);
  const none = page({ cellTerminal: () => ({ id: "a" }) });
  none.ctx.dockRestore([{ id: "a" }]); // nothing stored at all
  none.ctx.dockReopen();
  assert.deepEqual(none.st.opened, []);
});

test("a dock that would land in forced Full mode is not reopened on boot, and the stored flag stays true", () => {
  const p = boot({}, true);
  const q = page({ cellTerminal: () => ({ id: "a" }) });
  q.st.full = true;
  q.set("drawerState", { open: null, sessionId: null, pinned: false });
  q.restore(null, STORED(true));
  q.ctx.dockReopen();
  assert.deepEqual(q.st.opened, [], "the terminals do not come back covered");
  assert.equal(q.get("drawerState").open, null);
  q.ctx.dockSaveNow();
  assert.equal(storedOpen(q), true, "a write while it is held back keeps the flag");
  assert.ok(p);
  // The user opens it and closes it: that is a decision, and the flag follows it.
  q.drawer({ open: "files", sessionId: "a", pinned: false });
  q.drawer({ open: null, sessionId: "a", pinned: false });
  q.ctx.dockSaveNow();
  assert.equal(storedOpen(q), false);
});

test("the phone does not reopen anything, and a write from it does not store \"closed\" over the desktop's flag", () => {
  const p = boot({ drawerDesktop: () => false }, true);
  assert.deepEqual(p.st.opened, []);
  assert.equal(p.get("drawerState").open, null);
  p.ctx.dockSaveNow();
  assert.equal(storedOpen(p), true);
});

test("no focused session (an empty grid, a pane of files) is nothing to reopen the dock on", () => {
  for (const cellTerminal of [() => null, () => ({ id: "" }), () => ({})]) {
    const p = boot({ cellTerminal }, true);
    assert.deepEqual(p.st.opened, []);
    p.ctx.dockSaveNow();
    assert.equal(storedOpen(p), true, "and the flag is kept");
  }
});

test("a dock the page opened itself before the list arrived is not opened a second time", () => {
  const p = page({ cellTerminal: () => ({ id: "a" }) });
  p.drawer({ open: "files", sessionId: "a", pinned: false });
  p.restore(null, STORED(true));
  p.ctx.dockReopen();
  assert.deepEqual(p.st.opened, []);
});

test("closing a null-session pane's files and opening another session parks the viewer: no viewers is not 'the null session'", () => {
  const p = page();
  p.drawer({ open: "files", sessionId: null, pinned: false });
  p.show(files(1)[0], { preview: false });
  assert.equal(p.live().length, 1);
  assert.equal(p.get("dockViewersSid"), null, "the null session has viewers");
  p.set("drawerState", { open: "files", sessionId: "a", pinned: false }); // moved without drawerFilesApply's own park
  p.ctx.dockShow();
  assert.deepEqual(p.live(), [], "the pane of files' viewer did not stay on screen under session a");
  assert.equal(p.state(null).tabs.length, 1, "its tab is kept for when that pane's Files is open again");
});
