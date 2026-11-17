// Task 8b: on desktop every file opener lands in the Files dock, and the phone keeps its pane viewers. Source pins for the
// routing in tabs.js and the places that call it, then the real openFile, openBoxFile and restoreLayout run in a vm against
// stand-ins (dockOpenFile and what it does in the dock is dock-tabs-dom.test.js's; applyDrawer's `after` is
// dock-layout.test.js's); the visual check proves the result on screen.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
// fn: the source of a top-level function, up to its closing brace at column 0.
function fn(file, name) {
  const text = read(file);
  const i = text.search(new RegExp("^(async )?function " + name + "\\(", "m"));
  assert.ok(i >= 0, name + " is in " + file);
  return text.slice(i, text.indexOf("\n}\n", i) + 2);
}

test("openFile: the desktop branch shows the file in the dock; only the phone's body builds a pane ViewerTab", () => {
  const body = stripComments(fn("tabs.js", "openFile"));
  const dock = body.indexOf("dockOpenFile(");
  const pane = body.indexOf("new ViewerTab(");
  assert.ok(dock > 0, "the desktop branch calls dockOpenFile");
  assert.ok(pane > dock, "the pane viewer is built after it: only a call that did not return gets there");
  assert.match(body.slice(0, pane), /if \(opensInDock\(\)\) \{[^}]*dockOpenFile\(dockOpenSid\(\), path, opts\);\s*return;\s*\}/);
  // The loop that finds a pane viewer already showing the file is the phone's too.
  assert.ok(body.indexOf("for (const c of cells)") > dock, "no pane viewer is looked for on desktop");
  assert.match(fn("tabs.js", "opensInDock"), /!\(typeof mobileActive === "function" && mobileActive\(window\.innerWidth\)\)/);
});

test("openBoxFile: the desktop branch opens the file in the dock of the box's own session", () => {
  const body = stripComments(fn("tabs.js", "openBoxFile"));
  const dock = body.indexOf("dockOpenFile(");
  assert.ok(dock > 0 && dock < body.indexOf("new ViewerTab("), "before the pane viewer");
  assert.match(body.slice(0, body.indexOf("new ViewerTab(")), /if \(opensInDock\(\)\) \{[^}]*dockOpenFile\(sessionId, path, \{ \.\.\.opts, box: sessionId \}\);\s*return;\s*\}/);
});

test("dockOpenFile shows the file through drawerShowFile once the dock is open for the session", () => {
  const body = stripComments(fn("filesdrawer.js", "dockOpenFile"));
  assert.match(body, /openDrawer\("files", sid, \{\s*after: /);
  assert.match(body, /drawerShowFile\(path, opts\)/);
});

test("applyDrawer runs the caller's `after` once the drawer is applied, also when a discard ask or a page held it first", () => {
  const body = stripComments(fn("app.js", "applyDrawer"));
  assert.match(body, /if \(opts && typeof opts\.after === "function"\) opts\.after\(\);/);
  // The held paths come back through applyDrawer with the same opts, so `after` survives them.
  assert.match(body, /applyDrawer\(next, Object\.assign\(\{\}, opts, \{ discardOk: true \}\)\)/);
  assert.match(fn("app.js", "openDrawer"), /closePageHolds\(\(\) => openDrawer\(kind, sessionId, opts\)\)/);
});

test("restoreLayout moves pane viewer tabs to the dock before reconciling the layout, on desktop only", () => {
  const body = stripComments(fn("tabs.js", "restoreLayout"));
  const mig = body.indexOf("dockFromLayout(");
  const rec = body.indexOf("reconcileLayout(");
  assert.ok(mig > 0 && rec > mig, "migrated first, then reconciled: reconcileLayout sees a layout without viewers");
  assert.match(body, /opensInDock\(\)/);
  assert.match(body, /dockCellSid\(/);
  assert.match(body, /dockAdopt\(/);
  assert.ok(body.indexOf("dockAdopt(") < rec, "the tabs are in the dock's own map before the layout is built");
  assert.match(body, /reconcileLayout\(saved, sessions\)/, "reconciles what the migration returned, not bootLayout");
});

test("the dock's own routes are untouched: the tree, the badge, the terminal link and the palette reach it through the same openers", () => {
  // terminal-tab.js keeps its call shape (terminal-tab.test.js pins it): the dock is chosen inside openFile.
  const term = read("terminal-tab.js");
  assert.match(term, /openFile\(c\.path, alt \? \{ \.\.\.opts, alt \} : opts\);/);
  assert.match(term, /window\.openBoxFile\(this\.id, c\.path, opts\)/);
  // The palette's file rows open through openFile; its "open in new pane" is a SESSION row (openTerminal), not a file opener.
  const pal = read("palette.js");
  assert.match(pal, /run: \(\) => openFile\(r\.path\),/);
  assert.match(pal, /runNewPane: \(\) => openTerminal\(s\.id, name, \{ newPane: true \}\)/);
  assert.ok(!/runNewPane[^\n]*openFile/.test(pal));
  // A badge opens Changes in the dock while it owns the tree, and through openFile (also the dock) otherwise.
  assert.match(fn("gitstatus.js", "gitOpenChanges"), /drawerShowFile\(path, opts\);\s*else if \(typeof openFile === "function"\) openFile\(path, opts\);/);
});

test("Open in pane is gone: no openInPane or drawerOpenInPane anywhere in the page's scripts", () => {
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js"))) {
    assert.ok(!/openInPane|drawerOpenInPane/.test(stripComments(read(f))), f + " has no Open in pane");
  }
  assert.ok(!/openInPane/.test(read("filesdrawer.js")), "not even in a comment of filesdrawer.js");
});

// --- the real functions, in a vm ---------------------------------------------------------------------------------------

// A page for tabs.js's openers: `st` records what they did. `width` is the window's (the phone is 700 or less, as in mobileshell.js).
function opener(width, over) {
  const st = { dock: [], pane: [], closed: 0, screens: [], chips: 0 };
  const cell = { addTab: (t) => st.pane.push(["addTab", t.path, t.opts]), tabs: [], activate() {} };
  class ViewerTab {
    constructor(p, o) { this.path = p; this.opts = o; }
    focusReader() { st.pane.push(["focus", this.path]); }
  }
  const ctx = vm.createContext(Object.assign({
    console, Object, Array,
    window: { innerWidth: width },
    mobileActive: (w) => w <= 700,
    normalizePath: (p) => p.replace(/\/\.\//g, "/"),
    cells: [],
    ViewerTab,
    focusedCellOrCreate: () => cell,
    renderGrid: () => st.pane.push(["renderGrid"]),
    closeDrawerFor: () => { st.closed++; },
    setMobileScreen: (n) => st.screens.push(n),
    renderMobileChips: () => { st.chips++; },
    dockOpenFile: (sid, p, o) => { st.dock.push([sid, p, o]); return true; },
    dockOpenSid: () => "SID",
  }, over));
  for (const name of ["opensInDock", "openFile", "openBoxFile"]) new vm.Script(fn("tabs.js", name)).runInContext(ctx);
  return { ctx, st };
}

test("openFile on desktop: the file is shown in the dock of the focused session, normalised, and no pane viewer is built", () => {
  const { ctx, st } = opener(1440);
  ctx.openFile("/home/u/./a.md", { line: 12, col: 5, alt: { base: "/r", rel: "a.md" } });
  assert.deepEqual(st.dock, [["SID", "/home/u/a.md", { line: 12, col: 5, alt: { base: "/r", rel: "a.md" } }]]);
  assert.deepEqual(st.pane, [], "nothing built in a pane, nothing rendered");
  assert.equal(st.closed, 0, "no drawer closed to make room for a pane viewer");
  assert.deepEqual(st.screens, []);
  ctx.openFile("/x.go");
  assert.deepEqual(st.dock[1], ["SID", "/x.go", undefined], "the opts are passed as they came (the dock's own defaults apply)");
});

test("openFile on the phone is what it was: a pane viewer in the focused cell, the drawer closed, the Terminal screen, the chip", () => {
  const { ctx, st } = opener(390);
  ctx.openFile("/home/u/a.md", { line: 3, col: 1, alt: { base: "/r", rel: "a.md" }, view: "changes" });
  assert.deepEqual(st.dock, [], "the phone has no dock");
  assert.deepEqual(st.pane, [
    ["addTab", "/home/u/a.md", { line: 3, col: 1, alt: { base: "/r", rel: "a.md" }, view: "changes" }],
    ["renderGrid"],
    ["focus", "/home/u/a.md"],
  ]);
  assert.equal(st.closed, 1);
  assert.deepEqual(st.screens, ["terminal"]);
  assert.equal(st.chips, 1);
});

test("at the phone breakpoint (700) the phone's path is taken; one pixel wider is desktop", () => {
  assert.equal(opener(700).ctx.opensInDock(), false);
  assert.equal(opener(701).ctx.opensInDock(), true);
});

test("when filesdrawer.js has not loaded there is no dock to open in, so the pane is the fallback and nothing throws", () => {
  const { ctx, st } = opener(1440, { dockOpenFile: undefined });
  ctx.openFile("/a.md");
  assert.deepEqual(st.dock, []);
  assert.equal(st.pane[0][0], "addTab");
});

test("openBoxFile on desktop: a box tab of the session that printed the path, the line kept; the phone keeps the pane viewer", () => {
  const { ctx, st } = opener(1440);
  ctx.openBoxFile("s7", "/tmp/playwright-mcp/shot.png", { line: 4, col: 2 });
  assert.deepEqual(st.dock, [["s7", "/tmp/playwright-mcp/shot.png", { line: 4, col: 2, box: "s7" }]]);
  assert.deepEqual(st.pane, []);
  ctx.openBoxFile("s7", "/tmp/playwright-mcp/shot.png"); // no opts at all (a spread of undefined is nothing)
  assert.deepEqual(st.dock[1], ["s7", "/tmp/playwright-mcp/shot.png", { box: "s7" }]);
  const phone = opener(390);
  phone.ctx.openBoxFile("s7", "/tmp/playwright-mcp/shot.png", { line: 4, col: 2 });
  assert.deepEqual(phone.st.dock, []);
  assert.deepEqual(phone.st.pane[0], ["addTab", "/tmp/playwright-mcp/shot.png", { box: "s7", line: 4, col: 2 }]);
});

// restoreLayout against the real layout.js and docktabs.js: `plan` is what applyPlan was handed, `adopted` what the dock was given.
function restorer(width, layout) {
  const st = { plans: [], adopted: [], ended: 0 };
  const ctx = vm.createContext({
    console, Object, Array, Set, Map, Math, Number, JSON,
    window: { innerWidth: width },
    mobileActive: (w) => w <= 700,
    dockAdopt: (m) => st.adopted.push(m),
    applyPlan: (plan) => st.plans.push(plan),
    endRestore: () => { st.ended++; },
    bootLayout: layout,
  });
  for (const f of ["shelldock.js", "layout.js", "docktabs.js"]) new vm.Script(read(f), { filename: f }).runInContext(ctx);
  for (const name of ["opensInDock", "restoreLayout"]) new vm.Script(fn("tabs.js", name)).runInContext(ctx);
  ctx.dockOpenFile = () => true; // opensInDock asks for it
  return { ctx, st };
}
const SAVED = () => ({
  v: 1, cap: 4, focused: 1, maximized: null,
  cells: [
    { tabs: [{ id: "s1" }, { path: "/a.go", scroll: 120 }, { path: "/etc/b", box: "s1", scroll: 5 }], active: 1 },
    { tabs: [{ id: "gone" }, { path: "/dead.go" }], active: 1 },
    { tabs: [{ id: "s2" }], active: 0 },
  ],
});
const LIVE = [{ id: "s1" }, { id: "s2" }];
// The dock is handed a map (an empty one when nothing moved: dockAdopt ignores it); "nothing adopted" is no session in any.
const adoptedSessions = (st) => st.adopted.flatMap((m) => [...m.keys()]);

test("restoreLayout on desktop: pane viewer tabs go to the dock of the session each pane shows, and the layout is rebuilt without them", () => {
  const { ctx, st } = restorer(1440, SAVED());
  ctx.restoreLayout(LIVE);
  assert.equal(st.adopted.length, 1);
  assert.deepEqual([...st.adopted[0].keys()], ["s1"], "the pane of the gone session has no dock, so its file is dropped with it");
  const s1 = st.adopted[0].get("s1");
  assert.deepEqual(s1.tabs.map((t) => [t.key, t.scroll, t.preview]), [["v:/a.go", 120, false], ["b:s1:/etc/b", 5, false]], "pinned, scroll kept, the box file keeps its box");
  assert.equal(s1.active, "v:/a.go", "the pane had the file active");
  assert.equal(st.plans.length, 1);
  assert.deepEqual(st.plans[0].cells.map((c) => c.tabs), [[{ id: "s1" }], [{ id: "s2" }]], "no viewer in any pane; the dead session's pane is gone");
  assert.equal(st.plans[0].focused, 0, "focus follows its cell: the pane it was on was dropped, so it falls to the first");
  assert.equal(st.ended, 1);
});

test("restoreLayout on the phone is what it was: the viewers stay in the panes, the dock is told nothing", () => {
  const { ctx, st } = restorer(390, SAVED());
  ctx.restoreLayout(LIVE);
  assert.deepEqual(st.adopted, []);
  assert.deepEqual(st.plans[0].cells[0].tabs, [{ id: "s1" }, { path: "/a.go", scroll: 120 }, { path: "/etc/b", box: "s1", scroll: 5 }]);
  assert.equal(st.ended, 1);
});

test("restoreLayout leaves a layout reconcileLayout would refuse alone, and survives no layout at all", () => {
  const bad = SAVED();
  bad.v = 2;
  const a = restorer(1440, bad);
  a.ctx.restoreLayout(LIVE);
  assert.deepEqual(adoptedSessions(a.st), [], "the files of a layout nothing restores are not moved");
  assert.deepEqual(a.st.plans, []);
  assert.equal(a.st.ended, 1);
  for (const none of [null, undefined, "x", { v: 1 }, { v: 1, cells: "no" }]) {
    const b = restorer(1440, none);
    b.ctx.restoreLayout(LIVE);
    assert.deepEqual(adoptedSessions(b.st), []);
    assert.deepEqual(b.st.plans, []);
    assert.equal(b.st.ended, 1);
  }
});

test("restoreLayout: a layout that held only pane viewers (no terminal) restores nothing and the dock gets nothing", () => {
  const { ctx, st } = restorer(1440, { v: 1, cap: 4, focused: 0, maximized: null, cells: [{ tabs: [{ path: "/a.go" }], active: 0 }] });
  ctx.restoreLayout(LIVE);
  assert.deepEqual(adoptedSessions(st), [], "a pane of files has no session to give its file to");
  assert.deepEqual(st.plans, []);
});

test("restoreLayout still ends the restore when the migration throws", () => {
  const { ctx, st } = restorer(1440, SAVED());
  ctx.dockAdopt = () => { throw new Error("boom"); };
  assert.throws(() => ctx.restoreLayout(LIVE), /boom/);
  assert.equal(st.ended, 1, "the finally re-arms layout saving");
});
