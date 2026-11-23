// dockreveal.js's glue against a fake dock: when the dock's open file is revealed in the tree (after a short wait, once the
// tree can be seen and the user is not in it), what an automatic reveal never does (focus, open a collapsed tree, unfold the
// explorer), the "Reveal open file" toggle, re-selecting after a tree reload, and the "Outside this tree" state. ftReveal
// itself is filetree.js's (tree-reveal.test.js); here it is a recorder.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); };

function dock(o) {
  o = o || {};
  const st = { reveals: [], selects: [], renders: 0, wheres: [], secOpens: 0, unfolds: 0, timers: [], listeners: [] };
  const store = new Map(o.pref === undefined ? [] : [["agent-box:revealOpenFile", o.pref]]);
  const drawer = { dataset: {} };
  const col = { dataset: {} }; // #drawer-tree: docksections.js paints data-tree-shut on it
  const changes = { hidden: false }; // #dock-changes: with no Changes section a shut tree is still drawn open
  const treeEl = {
    hover: false,
    matches(sel) { return sel === ":hover" && this.hover; },
    contains(x) { return x === treeRow; },
    addEventListener(ev, fn) { st.listeners.push({ ev, fn }); },
    removeEventListener(ev, fn) { st.listeners = st.listeners.filter((l) => !(l.ev === ev && l.fn === fn)); },
  };
  const treeRow = { row: true };
  const tabs = { list: [], active: null };
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Promise, String, Error, RegExp,
    document: { activeElement: null, getElementById: (id) => ({ drawer, "drawer-tree": col, "dock-changes": changes })[id] || null },
    localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => store.set(k, String(v)) },
    setTimeout: (fn, ms) => { st.timers.push({ fn, ms }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers[id - 1]) st.timers[id - 1].fn = null; },
    drawerState: { open: "files", sessionId: "s1" },
    drawerOwnsFileClicks: () => true,
    dockStateOf: () => ({ tabs: tabs.list, active: tabs.active }),
    dockTabsOf: (s) => s.tabs,
    dockSecOpen: () => { st.secOpens++; },
    dockTreeReveal: () => { st.unfolds++; },
    dockRender: () => { st.renders++; },
    ftTreeEl: treeEl,
    ftRoot: () => st.root || "/w",
    ftGen: 1,
    ftReveal: (p, opts) => { st.reveals.push({ p, opts }); return Promise.resolve(true); },
    ftRowFor: (p) => (st.rows && st.rows.includes(p) ? { path: p } : null),
    ftSelect: (row) => { st.selects.push(row.path); },
    ftRevealTarget: (p, where) => {
      const root = st.root || "/w";
      if (ctx.ancestorsBetween(root, p) !== null) return p;
      return where && where.path && ctx.ancestorsBetween(root, where.path) !== null ? where.path : null;
    },
    insideAnyRoot: (d, roots) => roots.some((r) => d === r || d.indexOf(r + "/") === 0),
    ftCurrentRoots: () => ["/w", "/v"],
    api: (url) => {
      const p = decodeURIComponent(url.replace(/^.*\?path=/, ""));
      st.wheres.push(p);
      return (o.where && o.where[p]) ? Promise.resolve(o.where[p]) : Promise.reject(new Error("forbidden"));
    },
    kitMenuButton: (label, icon, danger, run) => {
      const attrs = {};
      return { label, icon, run, setAttribute: (k, v) => { attrs[k] = String(v); }, getAttribute: (k) => (k in attrs ? attrs[k] : null) };
    },
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("viewerpath.js"), "viewerpath.js");
  load(src("dockreveal.js"), "dockreveal.js");
  return {
    ctx, st, drawer, col, changes, treeEl, treeRow,
    run: (code) => load(code, "test"),
    open(p) { tabs.list = [{ key: "k:" + p, path: p }]; tabs.active = "k:" + p; },
    tab(t) { tabs.list = [t]; tabs.active = t.key; },
    // flush: run the timers that are due (the debounce), then let the promises settle.
    async flush() {
      for (let i = 0; i < 5; i++) {
        const due = st.timers.splice(0);
        for (const t of due) if (t.fn) t.fn();
        await settle();
        if (!st.timers.some((t) => t.fn)) break;
      }
    },
    fire(ev) { for (const l of st.listeners.filter((x) => x.ev === ev)) l.fn(); },
  };
}

test("a tab change reveals the file after the debounce, automatically, once", async () => {
  const d = dock();
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  assert.deepStrictEqual(d.st.reveals, [], "not at once: a burst of changes reveals once");
  assert.strictEqual(d.st.timers[0].ms, 150);
  await d.flush();
  assert.strictEqual(d.st.reveals.length, 1);
  assert.strictEqual(d.st.reveals[0].p, "/w/a/f.go");
  assert.strictEqual(d.st.reveals[0].opts.auto, true, "the automatic reveal: no focus, no collapsed tree opened");
  d.run("dockRevealSync(); dockRevealSync();");
  await d.flush();
  assert.strictEqual(d.st.reveals.length, 1, "the same file, root and tree: nothing more");
  assert.strictEqual(d.st.secOpens + d.st.unfolds, 0);
});

test("a burst of tab changes reveals only the last one", async () => {
  const d = dock();
  d.open("/w/a.go");
  d.run("dockRevealSync()");
  d.open("/w/b.go");
  d.run("dockRevealSync()");
  d.open("/w/c.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/c.go"]);
});

test("the pointer over the tree holds the reveal until it leaves", async () => {
  const d = dock();
  d.treeEl.hover = true;
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, [], "no jump under the pointer");
  d.treeEl.hover = false;
  d.fire("pointerleave");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go"]);
  assert.strictEqual(d.st.listeners.length, 0, "the wait cleans up after itself");
});

test("focus in the tree holds the reveal until it leaves", async () => {
  const d = dock();
  d.ctx.document.activeElement = d.treeRow;
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  d.ctx.document.activeElement = null;
  d.fire("focusout");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go"]);
});

test("a collapsed tree stays collapsed, and the file is revealed when the tree is opened", async () => {
  const d = dock();
  d.col.dataset.treeShut = "1";
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  assert.strictEqual(d.st.secOpens, 0, "never opened by an automatic reveal");
  delete d.col.dataset.treeShut;
  d.run("dockRevealSync()"); // what dockSecPaint calls when the user opens it
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go"]);
});

test("a tree stored shut but drawn open (no Changes section) is revealed into", async () => {
  const d = dock();
  d.col.dataset.treeShut = "1";
  d.changes.hidden = true;
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go"]);
});

test("a folded explorer is not unfolded, and the file is revealed when the strip opens the column", async () => {
  const d = dock();
  d.drawer.dataset.folded = "1";
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  assert.strictEqual(d.st.unfolds, 0);
  d.drawer.dataset.treeOpen = "1";
  d.run("dockRevealSync()"); // paintDrawerLayout's call
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go"]);
});

test("the strip opening the folded explorer reveals at once, though it puts focus in the tree (dockRevealNow)", async () => {
  const d = dock();
  d.drawer.dataset.folded = "1";
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  d.drawer.dataset.treeOpen = "1";
  d.ctx.document.activeElement = d.treeRow; // dockStripOpen focuses the tree's Tab stop
  const ok = await d.run("dockRevealNow()");
  assert.strictEqual(ok, true);
  assert.deepStrictEqual(d.st.reveals.map((r) => [r.p, r.opts.auto]), [["/w/a/f.go", true]]);
  d.run("dockRevealSync()");
  await d.flush();
  assert.strictEqual(d.st.reveals.length, 1, "dealt with: the sync after it adds nothing");
  const off = dock({ pref: "0" });
  off.open("/w/a/f.go");
  assert.strictEqual(await off.run("dockRevealNow()"), false, "off: the strip only opens the column");
  assert.deepStrictEqual(off.st.reveals, []);
});

test("dockStripOpen focuses the tree at once and hands focus to the revealed row when the reveal lands (source pin)", () => {
  const s = src("app.js");
  const at = s.indexOf("function dockStripOpen(");
  const body = s.slice(at, s.indexOf("\n}\n", at));
  assert.match(body, /dockRevealNow\(\)/);
  assert.match(body, /document\.activeElement === to/, "only when the user has not moved focus meanwhile");
});

test("Reveal open file off: no reveal, but a row already on screen is selected; the ⋯ row toggles it and reveals at once", async () => {
  const d = dock({ pref: "0" });
  d.st.rows = ["/w/f.go"];
  d.open("/w/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  assert.deepStrictEqual(d.st.selects, ["/w/f.go"]);
  let closed = 0;
  const row = d.run("dockRevealMenuRow")(() => { closed++; });
  assert.strictEqual(row.label, "Reveal open file");
  assert.strictEqual(row.getAttribute("role"), "menuitemcheckbox");
  assert.strictEqual(row.getAttribute("aria-checked"), "false");
  row.run();
  assert.strictEqual(closed, 1);
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/f.go"]);
  const on = d.run("dockRevealMenuRow")(() => {});
  assert.strictEqual(on.getAttribute("aria-checked"), "true");
  assert.strictEqual(on.icon, "check");
  on.run();
  assert.strictEqual(d.ctx.localStorage.getItem("agent-box:revealOpenFile"), "0");
});

test("after a tree reload the open file is revealed again", async () => {
  const d = dock();
  d.open("/w/a/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  d.run("ftGen = 2; dockRevealAfterLoad();");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals.map((r) => r.p), ["/w/a/f.go", "/w/a/f.go"]);
});

test("node_modules is skipped automatically (no hint either)", async () => {
  const d = dock();
  d.open("/w/node_modules/x/a.js");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  assert.strictEqual(d.run('dockRevealOutside("/w/node_modules/x/a.js")'), false);
});

test("a box file and the All changes tab reveal nothing and ask nothing", async () => {
  const d = dock();
  d.tab({ key: "b", path: "/w/a.go", box: "s1" });
  d.run("dockRevealSync()");
  await d.flush();
  d.tab({ key: "r", review: true });
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual([d.st.reveals, d.st.wheres], [[], []]);
});

test("outside the root: the server is asked where the file is; a resolved spelling under the root is revealed", async () => {
  const d = dock({ where: { "/v/lnk/f.go": { path: "/w/a/f.go" } } });
  d.open("/v/lnk/f.go");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.wheres, ["/v/lnk/f.go"]);
  assert.strictEqual(d.st.reveals.length, 1);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(d.st.reveals[0])), { p: "/v/lnk/f.go", opts: { auto: true, where: { path: "/w/a/f.go" } } });
  assert.strictEqual(d.run('dockRevealOutside("/v/lnk/f.go")'), false);
});

test("outside the root and not under it either: no reveal, the hint is on (and repainted), asked once per path", async () => {
  const d = dock({ where: { "/v/notes/n.md": { path: "/v/notes/n.md" } } });
  d.open("/v/notes/n.md");
  d.run("dockRevealSync()");
  await d.flush();
  assert.deepStrictEqual(d.st.reveals, []);
  assert.strictEqual(d.run('dockRevealOutside("/v/notes/n.md")'), true);
  assert.ok(d.st.renders >= 1, "the crumbs repaint with the hint");
  d.run("ftGen = 2; dockRevealAfterLoad();");
  await d.flush();
  assert.deepStrictEqual(d.st.wheres, ["/v/notes/n.md"], "the answer is kept for the page");
  // The tree is re-rooted where the file is: the hint goes.
  d.st.root = "/v/notes";
  d.run("ftGen = 3; dockRevealAfterLoad();");
  await d.flush();
  assert.strictEqual(d.run('dockRevealOutside("/v/notes/n.md")'), false);
});

test("the hint is off for a path the tree can never show (outside every workspace root) and when the server refuses", async () => {
  const d = dock({ where: { "/etc/x.conf": { path: "/etc/x.conf" } } });
  d.open("/etc/x.conf");
  d.run("dockRevealSync()");
  await d.flush();
  assert.strictEqual(d.run('dockRevealOutside("/etc/x.conf")'), false);
  d.open("/v/gone.md"); // the where call rejects
  d.run("dockRevealSync()");
  await d.flush();
  assert.strictEqual(d.run('dockRevealOutside("/v/gone.md")'), false);
});

test("the hint's Reveal is an explicit reveal of the open file", async () => {
  const d = dock({ where: { "/v/notes/n.md": { path: "/v/notes/n.md" } } });
  d.open("/v/notes/n.md");
  d.run("dockRevealSync()");
  await d.flush();
  d.run("dockRevealOutsideGo()");
  await settle();
  assert.strictEqual(d.st.reveals.length, 1);
  assert.strictEqual(d.st.reveals[0].p, "/v/notes/n.md");
  assert.ok(!d.st.reveals[0].opts.auto, "explicit: it may re-root and open the tree");
  assert.strictEqual(d.st.unfolds, 1, "a folded explorer opens for it");
});

test("drawerPaint, paintDrawerLayout, dockSecPaint and ftLoadRoot hand the reveal its chances (source pins)", () => {
  const body = (file, fn) => {
    const s = src(file);
    const at = s.indexOf("function " + fn + "(");
    assert.ok(at >= 0, fn + " in " + file);
    return s.slice(at, s.indexOf("\n}\n", at));
  };
  assert.match(body("filesdrawer.js", "drawerPaint"), /if \(typeof dockRevealSync === "function"\) dockRevealSync\(\);/);
  assert.match(body("app.js", "paintDrawerLayout"), /if \(typeof dockRevealSync === "function"\) dockRevealSync\(\);/);
  assert.match(body("docksections.js", "dockSecPaint"), /if \(typeof dockRevealSync === "function"\) dockRevealSync\(\);/);
  assert.match(body("filetree.js", "ftLoadRoot"), /if \(gen === ftGen && typeof dockRevealAfterLoad === "function"\) dockRevealAfterLoad\(\);/);
  // Every way the active tab changes ends in dockShow, which ends in drawerPaint: an opener, a tab switch, a close, a restore.
  assert.match(body("filesdrawer.js", "dockShow"), /drawerPaint\(\);\s*$/);
  for (const fn of ["drawerShowFile", "dockActivateTab", "dockCloseTab", "dockRestore", "drawerFilesApply"]) {
    assert.match(body("filesdrawer.js", fn), /dockShow\(/, fn + " shows the active tab");
  }
  const idx = src("index.html");
  assert.ok(idx.indexOf('src="/dockreveal.js"') > idx.indexOf('src="/docksections.js"'), "dockreveal.js loads after docksections.js");
});
