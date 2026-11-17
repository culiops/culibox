"use strict";
// The Files dock's layout and pin logic in app.js and filesdrawer.js, run for real in a vm against a minimal fake page:
// the real functions are cut out of their files (as closepage-callers.test.js does), the real dockfit.js and docktabs.js
// are loaded, and only the DOM is a stand-in. dock-layout-css.test.js pins the markup and stylesheet; the visual check
// proves the result on screen.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  const end = text.indexOf("\n}\n", m.index);
  assert.ok(end > m.index, name + " ends at column 0");
  return text.slice(m.index, end + 2);
}

// A fake element: just what the dock code reads and writes.
function fake(id) {
  const props = {};
  const attrs = {};
  return {
    id, dataset: {}, hidden: false, disabled: false, textContent: "", className: "", title: "",
    style: { setProperty: (k, v) => { props[k] = v; }, removeProperty: (k) => { delete props[k]; } },
    props, attrs,
    setAttribute: (k, v) => { attrs[k] = String(v); },
    getAttribute: (k) => attrs[k],
  };
}

const IDS = ["drawer", "drawer-body", "drawer-max", "drawer-title", "drawer-sess", "drawer-sess-dot", "drawer-sess-name", "drawer-hint", "drawer-pin"];

// page: a context with the real dock code. `st` is what the test changes: panes (cell count), the focused pane's session,
// the session list, and the stored sizes.
function page(over) {
  const els = Object.fromEntries(IDS.map((id) => [id, fake(id)]));
  els.drawer.hidden = false;
  const st = { cells: 1, maximized: false, focusSid: "a", calls: [], roots: [] };
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Math, Number, JSON,
    $: (id) => els[id] || null,
    gridCols: () => Math.min(Math.max(st.cells, 1), 4),
    currentMaximized: () => (st.maximized ? {} : null),
    cellTerminal: () => (st.focusSid ? { id: st.focusSid } : null),
    focusedCell: {},
    lastSessions: [{ id: "a", name: "alpha" }, { id: "b", name: "beta" }],
    deskDot: (s) => ({ cls: "dot working", title: s.id }),
    drawerDesktop: () => true,
    toastPlace: () => {},
    drawerHoldOnFollow: () => false,
    drawerFilesApply: (prev, next) => st.calls.push(["apply", prev.sessionId, next.sessionId]),
    drawerFocusSame: () => st.calls.push(["same"]),
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("dockfit.js"), "dockfit.js");
  load(src("docktabs.js"), "docktabs.js");
  load('const TREE_DEFAULT = 0.4; let drawerSize = { width: null, tree: 0.4 };' +
    'let drawerState = { open: "files", sessionId: "a", pinned: false }; let dockTreeAsk = null;' +
    'let dockDrawn = { fold: "", avail: 0, dockW: 0, treeW: 0 }; let drawerOrigins = new Map();', "state");
  for (const [file, name] of [
    ["app.js", "dockCols"], ["app.js", "dockTreeShare"], ["app.js", "drawerLiveSids"], ["app.js", "paintDrawerMax"], ["app.js", "paintDrawerLayout"],
    ["app.js", "dockTreeReveal"], ["app.js", "dockTreeFold"], ["app.js", "drawerFollowFocus"], ["app.js", "toggleDrawerPin"],
    ["app.js", "paintDrawerHead"], ["app.js", "drawerSessionName"], ["app.js", "paintDrawerTabs"], ["app.js", "positionDrawer"],
    ["app.js", "dockMeasure"], ["app.js", "applyDrawer"], ["app.js", "drawerPinCheck"], ["filesdrawer.js", "drawerKeepsTree"],
    ["tabs.js", "syncFocusedTree"],
  ]) load(fnSource(file, name), file + ":" + name);
  // Stand-ins for what these call into and the tests do not look at. The real positionDrawer is replaced by a spy: the
  // tests that care about it load it again (see the phone test).
  load("const cells = []; function dockRefit() {} function moveIntoDrawer() {} function drawerRootTree() {}", "stubs");
  // The tree: treeFollowTarget names a root for the focused session's tab, syncTreeToRoot records where the tree was sent.
  ctx.focusedTab = () => (st.focusSid ? { id: st.focusSid } : null);
  ctx.treeFollowTarget = (t) => (t ? { root: "/work/" + t.id, key: "k:" + t.id } : null);
  ctx.syncTreeToRoot = (root, key) => { st.roots.push([root, key]); };
  ctx.drawerOwnsFileClicks = () => true;
  ctx.drawerHasUnsaved = () => false;
  ctx.positionDrawer = () => st.calls.push(["position"]);
  return { els, st, ctx, run: (code) => load(code, "test"), sessions: (list) => { ctx.lastSessions = list; } };
}

const dockProps = (p) => ({ dock: p.els.drawer.props["--dock-w"], tree: p.els["drawer-body"].props["--dr-tree"] });

// --- layout ---------------------------------------------------------------------------------------------------

test("a wide window: the dock gets dockLayout's width and the explorer its column, nothing is full or folded", () => {
  const p = page();
  p.run("paintDrawerLayout(1190)");
  assert.deepEqual(dockProps(p), { dock: "738px", tree: "258px" });
  assert.equal(p.els.drawer.dataset.max, undefined);
  assert.equal(p.els.drawer.dataset.forced, undefined);
  assert.equal(p.els.drawer.dataset.folded, undefined);
  assert.equal(p.els.drawer.props["--grid-w"], "1190px", "Full mode's width is the pane area's");
  assert.deepEqual(p.run("dockDrawn"), { fold: "open", avail: 1190, dockW: 738, treeW: 258 });
});

test("two panes at 1280 with the sidebar open: the explorer folds to its strip", () => {
  const p = page();
  p.st.cells = 2;
  p.run("paintDrawerLayout(1030)"); // the founder's case: 1280 minus the 250 sidebar
  assert.equal(p.els.drawer.dataset.folded, "1");
  assert.deepEqual(dockProps(p), { dock: "456px", tree: "36px" });
  assert.equal(p.els.drawer.dataset.max, undefined, "pushed, not Full mode");
});

test("a maximized pane is one column on screen, so two cells do not make the dock narrower", () => {
  const p = page();
  p.st.cells = 2;
  p.st.maximized = true;
  assert.equal(p.run("dockCols()"), 1);
  p.st.maximized = false;
  assert.equal(p.run("dockCols()"), 2);
  p.st.cells = 1;
  assert.equal(p.run("dockCols()"), 1);
});

test("1100 wide with 2 panes cannot keep the terminal floors: Full mode is forced, and ⛶ and Esc leave it alone", () => {
  const p = page();
  p.st.cells = 2;
  p.run("paintDrawerLayout(800)");
  assert.equal(p.els.drawer.dataset.max, "1");
  assert.equal(p.els.drawer.dataset.forced, "1");
  assert.equal(p.els["drawer-max"].attrs["aria-pressed"], "true");
  assert.equal(p.els.drawer.props["--grid-w"], "800px");
  assert.equal(p.els["drawer-body"].props["--dr-tree"], "280px", "Full mode divides the pane area: .35 of 800, dockFullLayout's explorer, not the dock's");
  assert.equal(p.els.drawer.props["--dock-w"], undefined, "a zero dock width is never written");
});

test("forced Full mode ends when there is room again, and takes a ⛶ pressed meanwhile with it", () => {
  const p = page();
  p.st.cells = 2;
  p.run("paintDrawerLayout(800)");
  p.run("paintDrawerLayout(1400)");
  assert.equal(p.els.drawer.dataset.max, undefined);
  assert.equal(p.els.drawer.dataset.forced, undefined);
  assert.equal(p.els["drawer-max"].attrs["aria-pressed"], "false");
  assert.equal(p.els.drawer.props["--dock-w"], "680px", "min(.62 of 1400, 1400 - 2 * 360)");
});

test("a ⛶ pressed by the user is not forced: it stays through a layout pass and is laid out over the pane area", () => {
  const p = page();
  p.run("paintDrawerMax(true, false)");
  p.run("paintDrawerLayout(1190)");
  assert.equal(p.els.drawer.dataset.max, "1");
  assert.equal(p.els.drawer.dataset.forced, undefined);
  assert.deepEqual(p.run("dockDrawn"), { fold: "open", avail: 1190, dockW: 1190, treeW: 417 });
});

test("a dragged share is used for the pass but never stored, and an unset explorer share is the dock's default", () => {
  const p = page();
  p.run("paintDrawerLayout(1190, { dockShare: 0.9 })");
  assert.equal(p.els.drawer.props["--dock-w"], "830px", "min(.9 of 1190, 1190 - 360)");
  assert.equal(p.run("drawerSize.width"), null);
  assert.equal(p.run("dockTreeShare()"), null, "TREE_DEFAULT means never dragged: the dock's own .35 applies");
  p.run("drawerSize = { width: null, tree: 0.5 }");
  assert.equal(p.run("dockTreeShare()"), 0.5);
});

// --- the folded explorer opened from the strip ------------------------------------------------------------------

test("the strip opens the explorer as a column that pushes the terminals, and a fold change spends the ask", () => {
  const p = page();
  p.st.cells = 2;
  p.run("paintDrawerLayout(1030)");
  assert.equal(p.els.drawer.dataset.folded, "1");
  p.run("positionDrawer = () => paintDrawerLayout(1030)");
  p.run("dockTreeReveal()");
  assert.equal(p.run("dockTreeAsk"), "folded");
  assert.equal(p.els.drawer.dataset.folded, undefined, "the column is open");
  assert.equal(p.els["drawer-body"].props["--dr-tree"], "200px");
  assert.equal(p.els.drawer.props["--dock-w"], "470px", "the terminals gave, down to their 280 floor each: 1030 - 470 = 560");
  // The window gets wider until the explorer unfolds by itself: the transient ask is spent.
  p.run("paintDrawerLayout(1500)");
  assert.equal(p.run("dockTreeAsk"), null);
  // And narrower again: it must not reopen.
  p.run("paintDrawerLayout(1030)");
  assert.equal(p.els.drawer.dataset.folded, "1");
});

test("dockTreeFold folds it again; with nothing opened, or no dock, it and dockTreeReveal change nothing", () => {
  const p = page();
  p.st.cells = 2;
  p.run("positionDrawer = () => paintDrawerLayout(1030)");
  p.run("paintDrawerLayout(1030)");
  p.run("dockTreeReveal()");
  assert.equal(p.els.drawer.dataset.folded, undefined);
  p.run("dockTreeFold()");
  assert.equal(p.run("dockTreeAsk"), null);
  assert.equal(p.els.drawer.dataset.folded, "1");
  p.run("dockTreeFold()"); // nothing to fold
  const open = page();
  open.run("paintDrawerLayout(1190)");
  open.run("dockTreeReveal()"); // not folded: nothing to open
  assert.equal(open.run("dockTreeAsk"), null);
  const shut = page();
  shut.els.drawer.hidden = true;
  shut.run("dockTreeReveal()");
  assert.equal(shut.run("dockTreeAsk"), null);
});

test("when the column cannot push (the editor beside it would be under 260) the ask lands in forced Full mode", () => {
  const p = page();
  p.st.cells = 2;
  p.run("positionDrawer = () => paintDrawerLayout(1020)");
  p.run("paintDrawerLayout(1020)");
  assert.equal(p.els.drawer.dataset.folded, "1");
  p.run("dockTreeReveal()");
  assert.equal(p.els.drawer.dataset.forced, "1");
  assert.equal(p.run("dockTreeAsk"), "folded", "kept: Full mode's explorer is the column too");
});

// --- the pin -----------------------------------------------------------------------------------------------------

test("focus moves: an unpinned dock follows, a pinned one on a live session stays", () => {
  const p = page();
  p.st.focusSid = "b";
  p.run("drawerFollowFocus()");
  assert.equal(p.run("drawerState.sessionId"), "b");
  assert.deepEqual(p.st.calls.find((c) => c[0] === "apply"), ["apply", "a", "b"]);

  const q = page();
  q.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  q.st.focusSid = "b";
  q.run("drawerFollowFocus()");
  assert.equal(q.run("drawerState.sessionId"), "a");
  assert.equal(q.run("drawerState.pinned"), true);
  assert.ok(!q.st.calls.some((c) => c[0] === "apply"), "nothing was re-applied");
});

test("a pinned session that has ended is unpinned and the dock follows focus", () => {
  const p = page();
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  p.st.focusSid = "b";
  p.sessions([{ id: "a", name: "alpha", exited: true }, { id: "b", name: "beta" }]);
  p.run("drawerFollowFocus()");
  assert.equal(p.run("drawerState.sessionId"), "b");
  assert.equal(p.run("drawerState.pinned"), false);
  // Gone from the list altogether is gone too.
  const q = page();
  q.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  q.st.focusSid = "b";
  q.sessions([{ id: "b", name: "beta" }]);
  q.run("drawerFollowFocus()");
  assert.deepEqual(q.run("({ s: drawerState.sessionId, p: drawerState.pinned })"), { s: "b", p: false });
});

test("a pinned session that ended while it is the focused one is unpinned in place", () => {
  const p = page();
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  p.sessions([{ id: "a", name: "alpha", exited: true }]);
  p.run("drawerFollowFocus()");
  assert.deepEqual(p.run("({ s: drawerState.sessionId, p: drawerState.pinned })"), { s: "a", p: false });
});

test("a focused pane with no session leaves the dock on the last session it showed", () => {
  const p = page();
  p.st.focusSid = null;
  p.run("drawerFollowFocus()");
  assert.equal(p.run("drawerState.sessionId"), "a");
});

test("the dirty-buffer gate still holds a follow: a refused hold leaves the dock where it was", () => {
  const p = page({ drawerHoldOnFollow: () => true });
  p.st.focusSid = "b";
  p.run("drawerFollowFocus()");
  assert.equal(p.run("drawerState.sessionId"), "a");
});

test("the pin toggles, shows in the header, and unpinning follows focus at once", () => {
  const p = page();
  p.st.cells = 2;
  p.st.focusSid = "b"; // focus is on another session while the dock shows a
  p.run("toggleDrawerPin()");
  assert.equal(p.run("drawerState.pinned"), true);
  assert.equal(p.els["drawer-pin"].attrs["aria-pressed"], "true");
  assert.equal(p.els["drawer-hint"].textContent, "pinned");
  assert.equal(p.run("drawerState.sessionId"), "a", "pinning moves nothing");
  p.run("toggleDrawerPin()");
  assert.equal(p.run("drawerState.pinned"), false);
  assert.equal(p.run("drawerState.sessionId"), "b", "unpinned: the dock follows the focused pane");
  assert.equal(p.els["drawer-hint"].textContent, "follows the focused pane");
});

// The tree is held while pinned (drawerKeepsTree), so when the pin ends it has to be sent to the focused session: the header and
// the viewer move through drawerFilesApply, which does not re-root it.
test("unpinning re-roots the tree at the focused session, which the pin was holding back", () => {
  const p = page();
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  p.st.focusSid = "b";
  p.run("syncFocusedTree()"); // focus moved to b while pinned to a: the tree stays
  assert.deepEqual(p.st.roots, [], "held by the pin");
  assert.equal(p.run("drawerState.sessionId"), "a");
  p.run("toggleDrawerPin()");
  assert.equal(p.run("drawerState.pinned"), false);
  assert.equal(p.run("drawerState.sessionId"), "b", "header and viewer follow");
  assert.deepEqual(p.st.roots, [["/work/b", "k:b"]], "and so does the tree");
});

test("a pinned session that ends: the poll re-roots the tree at the focused session; a live pin leaves everything alone", () => {
  const p = page();
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  p.st.focusSid = "b";
  p.run("drawerPinCheck()"); // a is live
  assert.deepEqual(p.st.roots, []);
  assert.equal(p.run("drawerState.pinned"), true);
  p.sessions([{ id: "a", name: "alpha", exited: true }, { id: "b", name: "beta" }]);
  p.run("drawerPinCheck()");
  assert.deepEqual(p.run("({ s: drawerState.sessionId, p: drawerState.pinned })"), { s: "b", p: false });
  assert.deepEqual(p.st.roots, [["/work/b", "k:b"]]);
  // Gone from the list altogether is gone too.
  const g = page();
  g.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  g.st.focusSid = "b";
  g.sessions([{ id: "b", name: "beta" }]);
  g.run("drawerPinCheck()");
  assert.deepEqual(g.st.roots, [["/work/b", "k:b"]]);
  // Nothing pinned: nothing to check, whatever the list says.
  const q = page();
  q.st.focusSid = "b";
  q.sessions([{ id: "a", exited: true }]);
  q.run("drawerPinCheck()");
  assert.deepEqual(q.st.roots, []);
});

test("the Explorer button on the strip puts focus inside the column: a tree row, else the Go to file field, else Close", () => {
  const strip = (hasRow, gotoShown) => {
    const focused = [];
    const mk = (id, shown) => ({ id, getClientRects: () => (shown ? [1] : []), focus() { focused.push(id); } });
    const row = hasRow ? mk("row", true) : null;
    const els = { "drawer-tree": { querySelector: () => row }, "drawer-goto-in": mk("goto", gotoShown), "drawer-x": mk("x", true) };
    const ctx = vm.createContext({ $: (id) => els[id], dockTreeReveal() {} });
    new vm.Script(fnSource("app.js", "dockStripOpen")).runInContext(ctx);
    ctx.dockStripOpen();
    return focused;
  };
  assert.deepEqual(strip(true, true), ["row"], "a loaded tree: its row");
  assert.deepEqual(strip(false, true), ["goto"], "the tree has not loaded: the field keeps keyboard users in the dock");
  assert.deepEqual(strip(false, false), ["x"], "a workspace the fs API refuses hides the field too: Close");
});

test("there is nothing to pin without a session or a dock", () => {
  const p = page();
  p.run('drawerState = { open: "files", sessionId: null, pinned: false }');
  p.run("toggleDrawerPin()");
  assert.equal(p.run("drawerState.pinned"), false);
  const q = page();
  q.run('drawerState = { open: null, sessionId: "a", pinned: false }');
  q.run("toggleDrawerPin()");
  assert.equal(q.run("drawerState.pinned"), false);
});

test("the header: one pane draws FILES alone; two or more add the chip, the hint and the pin", () => {
  const p = page();
  p.run("paintDrawerHead()");
  assert.equal(p.els["drawer-sess"].hidden, true);
  assert.equal(p.els["drawer-hint"].hidden, true);
  assert.equal(p.els["drawer-pin"].hidden, true);
  p.st.cells = 2;
  p.run("paintDrawerHead()");
  assert.equal(p.els["drawer-sess"].hidden, false);
  assert.equal(p.els["drawer-sess-name"].textContent, "alpha");
  assert.equal(p.els["drawer-sess-dot"].className, "dot working");
  assert.equal(p.els["drawer-hint"].hidden, false);
  assert.equal(p.els["drawer-pin"].hidden, false);
  assert.equal(p.els["drawer-pin"].disabled, false);
  p.run('drawerState = { open: "files", sessionId: null, pinned: false }');
  p.run("paintDrawerHead()");
  assert.equal(p.els["drawer-sess"].hidden, true, "no session, no chip");
  assert.equal(p.els["drawer-pin"].disabled, true);
});

test("a pin holds the tree too, so the explorer never names another session than the editor", () => {
  const live = (p) => p.run("drawerOwnsFileClicks = () => true; drawerHasUnsaved = () => false");
  const p = page();
  live(p);
  p.st.focusSid = "b";
  assert.equal(p.run("drawerKeepsTree()"), false, "unpinned and clean: the tree follows focus");
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  assert.equal(p.run("drawerKeepsTree()"), true, "pinned to a live session: the tree stays");
  p.sessions([{ id: "a", name: "alpha", exited: true }, { id: "b", name: "beta" }]);
  assert.equal(p.run("drawerKeepsTree()"), false, "the pin is on a dead session: the dock is about to follow, and so does the tree");
  p.sessions([{ id: "a" }, { id: "b" }]);
  p.st.focusSid = "a";
  assert.equal(p.run("drawerKeepsTree()"), false, "focus is on the dock's own session");
  p.run('drawerState = { open: "files", sessionId: "a", pinned: false }; drawerHasUnsaved = () => true');
  p.st.focusSid = "b";
  assert.equal(p.run("drawerKeepsTree()"), true, "a dirty buffer still holds it, as before");
});

test("applyDrawer carries the pin only while the same session stays open", () => {
  const apply = (p, next) => p.run("applyDrawer(" + JSON.stringify(next) + ")");
  const pinned = () => ({ open: "files", sessionId: "a", pinned: true });
  const p = page();
  p.run("drawerState = " + JSON.stringify(pinned()));
  apply(p, { open: "files", sessionId: "a" }); // a header press for the same session
  assert.equal(p.run("drawerState.pinned"), true);
  apply(p, { open: "files", sessionId: "b" }); // an explicit open of another session
  assert.deepEqual(p.run("({ s: drawerState.sessionId, p: drawerState.pinned })"), { s: "b", p: false });
  const q = page();
  q.run("drawerState = " + JSON.stringify(pinned()));
  apply(q, { open: null, sessionId: "a" }); // closed
  assert.equal(q.run("drawerState.pinned"), false);
  assert.equal(q.run("drawerState.open"), null);
  apply(q, { open: "files", sessionId: "a" }); // reopened: a fresh dock
  assert.equal(q.run("drawerState.pinned"), false);
});

test("closing the dock ends Full mode and the explorer opened from the strip, and hides the dock", () => {
  const p = page();
  p.run("paintDrawerMax(true, false); dockTreeAsk = 'folded'");
  p.run('applyDrawer({ open: null, sessionId: "a" })');
  assert.equal(p.els.drawer.hidden, true);
  assert.equal(p.els.drawer.dataset.max, undefined);
  assert.equal(p.run("dockTreeAsk"), null);
});

test("a pin ends when the panes are down to one: it is not drawn then, so it could not be undone", () => {
  const p = page();
  p.st.cells = 2;
  p.els.drawer.dataset.multi = "1";
  p.run('drawerState = { open: "files", sessionId: "a", pinned: true }');
  p.run("dockMeasure = () => ({ avail: 1190, left: 300 }); var paintDrawerSize = () => {}");
  new vm.Script(fnSource("app.js", "positionDrawer")).runInContext(p.ctx); // the real one, over page()'s spy
  p.st.cells = 1;
  p.run("positionDrawer()");
  assert.equal(p.run("drawerState.pinned"), false);
  assert.equal(p.run("drawerState.sessionId"), "a", "the session it shows is unchanged");
  assert.equal(p.els["drawer-pin"].hidden, true);
  assert.equal(p.els["drawer-pin"].attrs["aria-pressed"], "false");
});

test("the Go to file list stays on screen: the field is at the window's right edge now, so the list slides left", () => {
  const go = (rect, innerWidth) => {
    const input = { getBoundingClientRect: () => rect };
    const list = { style: {} };
    const ctx = vm.createContext({ Math, window: { innerWidth }, drawerEl: (id) => (id === "drawer-goto-in" ? input : list) });
    new vm.Script(fnSource("filesdrawer.js", "drawerGoPosition")).runInContext(ctx);
    ctx.drawerGoPosition();
    return { ...list.style };
  };
  assert.deepEqual(go({ left: 1244, bottom: 70, width: 188 }, 1440), { left: "1092px", top: "72px", width: "340px" });
  assert.deepEqual(go({ left: 100, bottom: 70, width: 200 }, 1440), { left: "100px", top: "72px", width: "340px" }, "room beside the field: unchanged");
  assert.deepEqual(go({ left: 100, bottom: 70, width: 500 }, 1440), { left: "100px", top: "72px", width: "500px" }, "a wide field keeps its width");
  assert.equal(go({ left: 5, bottom: 70, width: 188 }, 390).width, "340px", "never wider than the window less its margins");
});

// --- phone ------------------------------------------------------------------------------------------------------

test("positionDrawer measures nothing and paints nothing on a phone or with the dock closed", () => {
  const run = (hidden, desktop) => {
    const p = page({ drawerDesktop: () => desktop });
    p.els.drawer.hidden = hidden;
    p.run("var measured = 0; dockMeasure = () => { measured++; return null; };");
    new vm.Script(fnSource("app.js", "positionDrawer")).runInContext(p.ctx); // the real one, over page()'s spy
    p.run("positionDrawer()");
    return p.run("measured");
  };
  assert.equal(run(true, true), 0, "closed");
  assert.equal(run(false, false), 0, "phone");
  assert.equal(run(false, true), 1, "open on desktop: it measures");
});
