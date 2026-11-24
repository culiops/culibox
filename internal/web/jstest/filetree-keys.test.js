"use strict";
// The file tree's keyboard path (UI plan 3, #173 task 3, audit F68) and its row menu as the kit menu on desktop.
// Before: the tree's rows were not focusable, the row's ⋯ was visibility:hidden until the pointer hovered
// (app.css), and the only key handler was Escape, so a keyboard user could not reach New folder, Rename,
// Delete or Download at all. Now the tree is a roving-tabindex tree (one row is the Tab stop, the arrows walk
// the rows, Right/Left open and close a folder), Shift+F10 or the ContextMenu key opens the focused row's menu,
// and the menu is the kit's `.menu-item` rows on desktop (the phone keeps its legacy buttons).
//
// The model is pure and tested as such (ftKeyAction, ftMenuGroups). The DOM half runs filetree.js whole in a
// vm against a small stand-in document (the repo has no jsdom), with the kit's builders beside it: focus obeys
// what a browser's does (a control or a tabindex element takes it, and a focus bubbles as focusin).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const FT = require("../static/filetree.js");

// ---- ftKeyAction: the pure key model ---------------------------------------------------------------------------------
// rows in visible order: a closed folder, an open folder with two children (a file and a closed folder), a file.
const R = (dir, open, level) => ({ dir, open, level });
const TREE = [
  R(true, false, 1),  // 0 src (closed)
  R(true, true, 1),   // 1 web (open)
  R(false, false, 2), // 2   web/a.js
  R(true, false, 2),  // 3   web/lib (closed)
  R(false, false, 1), // 4 README
];
const act = (at, key, shift) => FT.ftKeyAction(TREE, at, key, !!shift);

test("ftKeyAction: ArrowDown and ArrowUp move one row and clamp at the ends", () => {
  assert.deepStrictEqual(act(1, "ArrowDown"), { index: 2, action: "focus" });
  assert.deepStrictEqual(act(1, "ArrowUp"), { index: 0, action: "focus" });
  assert.deepStrictEqual(act(4, "ArrowDown"), { index: 4, action: "focus" }, "the last row stays, the key is still the tree's (no page scroll)");
  assert.deepStrictEqual(act(0, "ArrowUp"), { index: 0, action: "focus" });
});

test("ftKeyAction: Home and End jump to the first and the last row", () => {
  assert.deepStrictEqual(act(3, "Home"), { index: 0, action: "focus" });
  assert.deepStrictEqual(act(1, "End"), { index: 4, action: "focus" });
});

test("ftKeyAction: ArrowRight expands a closed folder, enters an open one, and does nothing on a file", () => {
  assert.deepStrictEqual(act(0, "ArrowRight"), { index: 0, action: "expand" });
  assert.deepStrictEqual(act(3, "ArrowRight"), { index: 3, action: "expand" });
  assert.deepStrictEqual(act(1, "ArrowRight"), { index: 2, action: "focus" }, "an open folder: its first child");
  assert.deepStrictEqual(act(2, "ArrowRight"), { index: 2, action: null });
  assert.deepStrictEqual(act(4, "ArrowRight"), { index: 4, action: null });
});

test("ftKeyAction: ArrowRight on an open folder with no children (or still loading) stays put", () => {
  const rows = [R(true, true, 1), R(false, false, 1)];
  assert.deepStrictEqual(FT.ftKeyAction(rows, 0, "ArrowRight", false), { index: 0, action: null }, "the next row is a sibling, not a child");
  assert.deepStrictEqual(FT.ftKeyAction([R(true, true, 1)], 0, "ArrowRight", false), { index: 0, action: null }, "nothing after it");
});

test("ftKeyAction: ArrowLeft collapses an open folder, else goes to the nearest previous row one level up", () => {
  assert.deepStrictEqual(act(1, "ArrowLeft"), { index: 1, action: "collapse" });
  assert.deepStrictEqual(act(2, "ArrowLeft"), { index: 1, action: "focus" }, "a child goes to its parent");
  assert.deepStrictEqual(act(3, "ArrowLeft"), { index: 1, action: "focus" }, "a closed folder goes to its parent, not its sibling");
  assert.deepStrictEqual(act(0, "ArrowLeft"), { index: 0, action: null }, "a top-level closed folder has no parent");
  assert.deepStrictEqual(act(4, "ArrowLeft"), { index: 4, action: null }, "a top-level file has none either");
  // The nearest, not the first: two parents of the same level above, the closer one wins.
  const deep = [R(true, true, 1), R(false, false, 2), R(true, true, 1), R(true, true, 2), R(false, false, 3)];
  assert.deepStrictEqual(FT.ftKeyAction(deep, 4, "ArrowLeft", false), { index: 3, action: "focus" });
  assert.deepStrictEqual(FT.ftKeyAction(deep, 3, "ArrowLeft", false), { index: 3, action: "collapse" });
  assert.deepStrictEqual(FT.ftKeyAction([R(false, false, 2)], 0, "ArrowLeft", false), { index: 0, action: null }, "a parent that is not in the list");
});

test("ftKeyAction: Enter opens the row", () => {
  for (let i = 0; i < TREE.length; i++) assert.deepStrictEqual(act(i, "Enter"), { index: i, action: "open" });
});

test("ftKeyAction: ContextMenu and Shift+F10 open the row's menu, a plain F10 does not", () => {
  assert.deepStrictEqual(act(2, "ContextMenu"), { index: 2, action: "menu" });
  assert.deepStrictEqual(act(2, "ContextMenu", true), { index: 2, action: "menu" });
  assert.deepStrictEqual(act(2, "F10", true), { index: 2, action: "menu" });
  assert.deepStrictEqual(act(2, "F10", false), { index: 2, action: null }, "F10 alone is the browser's menu bar");
});

test("ftKeyAction: any other key, and a focus that is on no row, is left alone", () => {
  for (const k of ["a", "Tab", "Escape", " ", "PageDown", "Delete", "F2"]) assert.deepStrictEqual(act(1, k), { index: 1, action: null }, k);
  assert.strictEqual(FT.ftKeyAction(TREE, -1, "ArrowDown", false).action, null);
  assert.strictEqual(FT.ftKeyAction(TREE, 9, "ArrowDown", false).action, null);
  assert.strictEqual(FT.ftKeyAction([], 0, "ArrowDown", false).action, null);
});

// ---- ftMenuGroups -----------------------------------------------------------------------------------------------------
const groups = (o) => FT.ftMenuGroups(FT.rowMenuIds(o));

test("ftMenuGroups: a folder is path actions | Download, New folder | Rename | Delete", () => {
  assert.deepStrictEqual(groups({ isDir: true, isImage: false, readOnly: false }),
    [["insert", "copy", "copyrel"], ["download", "newdir"], ["rename"], ["delete"]]);
});

test("ftMenuGroups: a file has Copy contents in the first group and no New folder", () => {
  assert.deepStrictEqual(groups({ isDir: false, isImage: false, readOnly: false }),
    [["insert", "copy", "copyrel", "copycontents"], ["download"], ["rename"], ["delete"]]);
  assert.deepStrictEqual(groups({ isDir: false, isImage: true, readOnly: false }),
    [["insert", "copy", "copyrel"], ["download"], ["rename"], ["delete"]], "an image has no text to copy");
});

test("ftMenuGroups: a read-only listing is the path actions and Download, nothing that writes", () => {
  assert.deepStrictEqual(groups({ isDir: false, isImage: false, readOnly: true }),
    [["insert", "copy", "copyrel", "copycontents"], ["download"]], "a read-only text file still copies its contents");
  assert.deepStrictEqual(groups({ isDir: true, isImage: false, readOnly: true }), [["insert", "copy", "copyrel"], ["download"]]);
  assert.deepStrictEqual(groups({ isDir: false, isImage: true, readOnly: true }), [["insert", "copy", "copyrel"], ["download"]]);
});

test("ftMenuGroups: every id of rowMenuIds lands in a group exactly once, in rowMenuIds' own order, and Delete is alone and last", () => {
  for (const isDir of [true, false]) for (const isImage of [true, false]) for (const readOnly of [true, false]) {
    const ids = FT.rowMenuIds({ isDir, isImage: isImage && !isDir, readOnly });
    const g = FT.ftMenuGroups(ids);
    assert.deepStrictEqual(g.flat(), ids.filter((id) => id !== null), "same ids, same order, no rule");
    for (const one of g) assert.ok(one.length > 0, "no empty group");
    if (!readOnly) assert.deepStrictEqual(g[g.length - 1], ["delete"]);
  }
  assert.deepStrictEqual(FT.ftMenuGroups([]), []);
});

// ---- the stand-in document -------------------------------------------------------------------------------------------
class Text { constructor(t) { this.text = String(t); this.parent = null; } get textContent() { return this.text; } }
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.handlers = {}; this.title = ""; this.onclick = null;
    this.style = { setProperty() {} }; this.offsetWidth = 200; this.offsetHeight = 220;
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  get classList() {
    const n = this;
    const set = () => new Set(n.className.split(/\s+/).filter(Boolean));
    return {
      add: (...c) => { const s = set(); c.forEach((x) => s.add(x)); n.className = [...s].join(" "); },
      remove: (...c) => { const s = set(); c.forEach((x) => s.delete(x)); n.className = [...s].join(" "); },
      contains: (c) => set().has(c),
    };
  }
  setAttribute(k, v) { this.attrs[k] = String(v); if (k === "class") this.className = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  appendChild(c) { if (typeof c === "string") c = new Text(c); if (c.parent) c.remove(); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; cs.forEach((c) => this.appendChild(c)); }
  after(n) { const i = this.parent.children.indexOf(this); if (n.parent) n.remove(); n.parent = this.parent; this.parent.children.splice(i + 1, 0, n); }
  remove() { if (this.parent) { this.parent.children.splice(this.parent.children.indexOf(this), 1); this.parent = null; } }
  addEventListener(ev, fn) { (this.handlers[ev] = this.handlers[ev] || []).push(fn); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  get isConnected() { for (let x = this; x; x = x.parent) if (x === Node.root) return true; return false; }
  getBoundingClientRect() { return { left: 800, right: 900, top: 100, bottom: 120 }; }
  // As a browser: a control or an element with a tabindex takes focus, and a focus bubbles as focusin.
  focus() {
    if (!(["input", "select", "textarea", "button"].includes(this.tag) || "tabindex" in this.attrs)) return;
    Node.focused = this;
    Node.log.push("focus:" + this.label());
    const ev = { target: this };
    for (let x = this; x; x = x.parent) for (const f of (x.handlers.focusin || [])) f(Object.assign({ currentTarget: x }, ev));
  }
  label() { return this.className.split(" ")[0] + (this.attrs["data-name"] ? "=" + this.attrs["data-name"] : "") + (this.own ? ":" + this.own : ""); }
  click() { if (this.onclick) return this.onclick({ target: this, stopPropagation() {}, preventDefault() {} }); }
  closest(sel) { for (let x = this; x; x = x.parent) if (x instanceof Node && x.matches(sel)) return x; return null; }
  // The selectors the tree and kitMenuKeys use: a comma list of `.class[attr="v"]...` (class optional) with an
  // optional trailing :not([attr="v"]). Anything else is a selector the real page may read differently: it fails loudly.
  matches(sel) {
    return sel.split(",").map((p) => p.trim()).some((part) => {
      const m = /^(\.[\w-]+)?((?:\[[\w-]+="[^"]*"\])*)(?::not\(\[([\w-]+)="([^"]*)"\]\))?$/.exec(part);
      assert.ok(m, "unexpected selector " + part);
      if (m[1] && !this.className.split(/\s+/).includes(m[1].slice(1))) return false;
      for (const a of m[2].matchAll(/\[([\w-]+)="([^"]*)"\]/g)) if (this.attrs[a[1]] !== a[2]) return false;
      return !(m[3] && this.attrs[m[3]] === m[4]);
    });
  }
  all(pred, out = []) { for (const c of this.children) { if (!(c instanceof Node)) continue; if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  querySelectorAll(sel) {
    // `:scope > a, :scope > b` (gitstatus.js's) is a list of the children that match any of them.
    const parts = sel.split(",").map((x) => x.trim());
    if (parts.every((x) => x.startsWith(":scope > "))) {
      const own = parts.map((x) => x.slice(":scope > ".length)).join(",");
      return this.children.filter((c) => c instanceof Node && c.matches(own));
    }
    return this.all((n) => n.matches(sel));
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
Node.root = null; Node.focused = null; Node.log = [];

const ENTRIES = {
  "/h/w": { entries: [{ name: "dir1", is_dir: true }, { name: "dir2", is_dir: true }, { name: "a.txt", is_dir: false }, { name: "b.png", is_dir: false }] },
  "/h/w/dir1": { entries: [{ name: "inner.txt", is_dir: false }, { name: "sub", is_dir: true }] },
  "/h/w/dir1/sub": { entries: [{ name: "deep.txt", is_dir: false }] },
  "/h/w/dir2": { entries: [] },
};

// world(width): the page as the tree sees it. The tree is wired the way initFileTree wires it (ftTreeSetup), so
// the keys and the focus travel through the same listeners.
function world(width, listings) {
  const calls = [];
  const zones = []; // every drop zone filetree.js wires (upload.js's makeDropZone is not loaded here)
  const root = new Node("body");
  Node.root = root; Node.focused = null; Node.log = [];
  const menu = new Node("div"); menu.attrs.id = "ft-menu";
  const tree = new Node("div"); tree.attrs.id = "filetree";
  root.append(tree, menu);
  const doc = {
    body: root,
    createElement: (t) => new Node(t), createElementNS: (_ns, t) => new Node(t), createTextNode: (t) => new Text(t),
    getElementById: (id) => (id === "ft-menu" ? menu : id === "filetree" ? tree : null),
    get activeElement() { return Node.focused; },
  };
  const lists = listings || ENTRIES;
  const icon = (k) => { const s = new Node("svg"); s.setAttribute("data-kind", k); return s; };
  const ctx = vm.createContext({
    document: doc, console, Promise, JSON, Math, Array, Object, Set, Map, String, Number, Date, WeakMap,
    window: { innerWidth: width, innerHeight: 900, HOME_DIR: "/h" },
    mobileActive: (w) => Number(w) <= 700,
    tabIcon: icon, dockIcon: icon,
    localStorage: { getItem: () => null, setItem() {} },
    pendingRegion: () => () => {},
    api: async (u) => { const p = decodeURIComponent(u.split("path=")[1]); if (!lists[p]) throw new Error("no " + p); return lists[p]; },
    openFile: (p) => calls.push("openFile:" + p),
    askPrompt: async (o) => { calls.push("askPrompt:" + o.title); return null; },
    askConfirm: async () => false, showToast: () => {},
    focusTerminal: () => calls.push("focusTerminal"),
    makeDropZone: (el, dest, opts) => zones.push({ el, dest, opts }),
  });
  const run = (src, f) => new vm.Script(src, { filename: f || "t.js" }).runInContext(ctx);
  for (const f of ["icons.js", "iconmap.js", "svgicon.js", "kitmenu.js", "copyfile.js", "fsops.js", "treestate.js", "filetree.js", "insertdecision.js"]) run(read(f), f);
  // What initFileTree does before it wires the tree: the root and the tree element.
  run("ftRootPath = '/h/w'; ftTreeEl = document.getElementById('filetree');");
  ctx.ftTreeSetup(tree);
  const key = (target, k, mods) => {
    // A keydown bubbles from the focused node: the tree's listener hears it from the row or the ⋯ inside it.
    const ev = Object.assign({ key: k, target, prevented: 0, preventDefault() { this.prevented++; } }, mods || {});
    for (let n = target; n; n = n.parent) for (const f of (n.handlers.keydown || [])) f(Object.assign(ev, { currentTarget: n }));
    return ev;
  };
  // The document-level keydown (Escape and the menu's arrows): initFileTree registers ftMenuKeydown on the document.
  const docKey = (k) => {
    const ev = { key: k, target: Node.focused, prevented: 0, preventDefault() { this.prevented++; } };
    ctx.ftMenuKeydown(ev);
    return ev;
  };
  // A contextmenu bubbles from its target to the tree's listener; the document-level one (ftMenuContextMenu,
  // registered by initFileTree) is called directly, as docKey does for the keys.
  const ctxMenu = (target, mods) => {
    const ev = Object.assign({ type: "contextmenu", target, prevented: 0, preventDefault() { this.prevented++; } }, mods || {});
    for (let n = target; n; n = n.parent) for (const f of (n.handlers.contextmenu || [])) f(Object.assign(ev, { currentTarget: n }));
    return ev;
  };
  const docCtxMenu = (target, mods) => {
    const ev = Object.assign({ type: "contextmenu", target, prevented: 0, preventDefault() { this.prevented++; } }, mods || {});
    ctx.ftMenuContextMenu(ev);
    return ev;
  };
  const render = async (into) => { const c = into || new Node("div"); if (!into) tree.append(c); await ctx.ftRenderDir("/h/w", c, ""); return c; };
  return { ctx, run, calls, root, tree, menu, zones, key, docKey, ctxMenu, docCtxMenu, render };
}
const hasCls = (cls) => (n) => n.className.split(/\s+/).includes(cls);
const byCls = (root, cls) => root.all(hasCls(cls));
const rowsOf = (c) => byCls(c, "ft-node");
const nameOf = (row) => byCls(row, "ft-name")[0].textContent;
const stops = (c) => rowsOf(c).filter((r) => r.attrs.tabindex === "0").map(nameOf);

// ---- the roving tabindex tree ---------------------------------------------------------------------------------------
test("ftRenderDir: exactly one row is the Tab stop (the first), every ⋯ is out of the Tab order", async () => {
  const w = world(1280);
  const c = await w.render();
  const rows = rowsOf(c);
  assert.deepStrictEqual(rows.map(nameOf), ["dir1", "dir2", "a.txt", "b.png"]);
  assert.deepStrictEqual(stops(c), ["dir1"]);
  assert.deepStrictEqual(rows.map((r) => r.attrs.tabindex), ["0", "-1", "-1", "-1"]);
  for (const r of rows) assert.strictEqual(byCls(r, "ft-more")[0].attrs.tabindex, "-1", nameOf(r) + "'s ⋯ is reached through the row's menu key");
});

test("ftRenderDir: rows are treeitems, folders carry aria-expanded, every row its level and name", async () => {
  const w = world(1280);
  const c = await w.render();
  const info = rowsOf(c).map((r) => [nameOf(r), r.attrs.role, r.attrs["aria-expanded"] === undefined ? "-" : r.attrs["aria-expanded"], r.attrs["aria-level"], r.attrs["aria-label"]]);
  assert.deepStrictEqual(info, [
    ["dir1", "treeitem", "false", "1", "dir1"], ["dir2", "treeitem", "false", "1", "dir2"],
    ["a.txt", "treeitem", "-", "1", "a.txt"], ["b.png", "treeitem", "-", "1", "b.png"],
  ]);
});

test("ftTreeSetup: the tree element is role=tree with a name", () => {
  const w = world(1280);
  assert.strictEqual(w.tree.attrs.role, "tree");
  assert.ok(w.tree.attrs["aria-label"], "a tree needs a name");
});

test("ftRenderDir: a remembered-open folder renders its children one level down, and the tree still has one Tab stop", async () => {
  const w = world(1280);
  w.run("addExpanded(ftExpanded, '/h/w/dir1'); addExpanded(ftExpanded, '/h/w/dir1/sub');");
  const c = await w.render();
  const rows = rowsOf(c);
  assert.deepStrictEqual(rows.map(nameOf), ["dir1", "inner.txt", "sub", "deep.txt", "dir2", "a.txt", "b.png"], "visible order, children after their folder");
  assert.deepStrictEqual(rows.map((r) => r.attrs["aria-level"]), ["1", "2", "2", "3", "1", "1", "1"]);
  assert.deepStrictEqual(rows.map((r) => r.attrs["aria-expanded"] || "-"), ["true", "-", "true", "-", "false", "-", "-"]);
  assert.deepStrictEqual(stops(c), ["dir1"], "one Tab stop in the whole tree, not one per folder");
});

test("ftRenderDir: a re-render keeps the Tab stop on the same path, and falls back to the first row when it is gone", async () => {
  const lists = { "/h/w": { entries: [{ name: "dir1", is_dir: true }, { name: "a.txt", is_dir: false }, { name: "b.png", is_dir: false }] }, "/h/w/dir1": { entries: [] } };
  const w = world(1280, lists);
  const c = await w.render();
  w.ctx.ftRoveTo(rowsOf(c)[1]); // a.txt takes the stop (a focus does this)
  assert.deepStrictEqual(stops(c), ["a.txt"], "the old stop was handed over, not kept");
  assert.deepStrictEqual(stops(await w.render()), ["a.txt"], "the same path in a fresh render");
  // The path is gone (renamed or deleted): the first row, never zero stops.
  lists["/h/w"] = { entries: [{ name: "dir1", is_dir: true }, { name: "z.txt", is_dir: false }] };
  assert.deepStrictEqual(stops(await w.render()), ["dir1"], "a.txt vanished: the first row is the stop");
});

test("ftRenderDir: a folder expanded later never adds a second Tab stop", async () => {
  const w = world(1280);
  const c = await w.render();
  const dir1 = rowsOf(c)[0];
  await w.run("ftRowOpeners").get(dir1).setOpen(true);
  assert.deepStrictEqual(rowsOf(c).map(nameOf), ["dir1", "inner.txt", "sub", "dir2", "a.txt", "b.png"]);
  assert.deepStrictEqual(stops(c), ["dir1"]);
  assert.strictEqual(dir1.attrs["aria-expanded"], "true");
  assert.deepStrictEqual(rowsOf(c).map((r) => r.attrs.tabindex), ["0", "-1", "-1", "-1", "-1", "-1"]);
});

test("a focus on a row hands it the Tab stop (the mouse does this too: a click focuses the row)", async () => {
  const w = world(1280);
  const c = await w.render();
  const rows = rowsOf(c);
  rows[3].focus();
  assert.deepStrictEqual(stops(c), ["b.png"]);
  byCls(rows[1], "ft-more")[0].focus(); // the ⋯ is inside dir2's row
  assert.deepStrictEqual(stops(c), ["dir2"], "a focus inside a row is on that row");
});

test("collapsing the folder that holds the Tab stop hands the stop to the first row (the tree stays reachable by Tab)", async () => {
  const w = world(1280);
  w.run("addExpanded(ftExpanded, '/h/w/dir1');");
  const c = await w.render();
  const rows = rowsOf(c);
  rows[1].focus(); // inner.txt
  assert.deepStrictEqual(stops(c), ["inner.txt"]);
  await w.run("ftRowOpeners").get(rows[0]).setOpen(false); // its folder closes: the focused row is gone
  assert.deepStrictEqual(rowsOf(c).map(nameOf), ["dir1", "dir2", "a.txt", "b.png"]);
  assert.deepStrictEqual(stops(c), ["dir1"]);
});

// Review M4: a drop onto an open folder empties its box and draws it again without the root render's bookkeeping, so a
// Tab stop that was inside the folder was lost with the old rows and Tab skipped the whole tree until a click.
const tick = () => new Promise((r) => setImmediate(r));
const dropOn = async (w, row) => {
  const z = w.zones.find((x) => x.el === row);
  assert.ok(z, "the folder row is a drop zone");
  z.opts.onDone();
  await tick();
};
const deepCopy = (x) => JSON.parse(JSON.stringify(x));

test("a drop onto an open folder re-draws it and the Tab stop that was inside it goes back to the same path (one stop, never none)", async () => {
  const w = world(1280);
  w.run("addExpanded(ftExpanded, '/h/w/dir1');");
  const c = await w.render();
  const names = rowsOf(c).map(nameOf);
  assert.deepStrictEqual(names, ["dir1", "inner.txt", "sub", "dir2", "a.txt", "b.png"]);
  rowsOf(c)[1].focus(); // a click on inner.txt: it is the stop
  assert.deepStrictEqual(stops(c), ["inner.txt"]);
  await dropOn(w, rowsOf(c)[0]);
  assert.deepStrictEqual(rowsOf(c).map(nameOf), names, "the folder was drawn again, same rows");
  assert.deepStrictEqual(stops(c), ["inner.txt"], "the same path is the stop in the fresh row");
  assert.deepStrictEqual(rowsOf(c).map((r) => r.attrs.tabindex), ["-1", "0", "-1", "-1", "-1", "-1"]);
});

test("a drop onto an open folder: when the stop's path is gone from the new listing, the first row takes it", async () => {
  const lists = deepCopy(ENTRIES);
  const w = world(1280, lists);
  w.run("addExpanded(ftExpanded, '/h/w/dir1');");
  const c = await w.render();
  rowsOf(c)[1].focus(); // inner.txt
  lists["/h/w/dir1"] = { entries: [{ name: "sub", is_dir: true }] }; // the upload's tool removed it meanwhile
  await dropOn(w, rowsOf(c)[0]);
  assert.deepStrictEqual(rowsOf(c).map(nameOf), ["dir1", "sub", "dir2", "a.txt", "b.png"]);
  assert.deepStrictEqual(stops(c), ["dir1"], "never zero stops");
});

test("a drop onto an open folder leaves a Tab stop outside it alone, and a drop on a closed folder does nothing to the tree", async () => {
  const w = world(1280);
  w.run("addExpanded(ftExpanded, '/h/w/dir1');");
  const c = await w.render();
  rowsOf(c)[4].focus(); // a.txt
  await dropOn(w, rowsOf(c)[0]);
  assert.deepStrictEqual(stops(c), ["a.txt"]);
  const before = rowsOf(c).length;
  await dropOn(w, rowsOf(c)[3]); // dir2, closed
  assert.strictEqual(rowsOf(c).length, before);
  assert.deepStrictEqual(stops(c), ["a.txt"]);
});

// ---- the keys, through the tree's own listener -----------------------------------------------------------------------
async function focused(w, name) {
  const c = await w.render();
  const row = rowsOf(c).find((r) => nameOf(r) === name);
  row.focus();
  return { c, row };
}

test("keys: ArrowDown and ArrowUp move focus (and the Tab stop) over the rows", async () => {
  const w = world(1280);
  const { c, row } = await focused(w, "dir1");
  const ev = w.key(row, "ArrowDown");
  assert.strictEqual(ev.prevented, 1, "the key is the tree's: no page scroll");
  assert.strictEqual(nameOf(Node.focused), "dir2");
  assert.deepStrictEqual(stops(c), ["dir2"]);
  w.key(Node.focused, "End");
  assert.strictEqual(nameOf(Node.focused), "b.png");
  w.key(Node.focused, "ArrowUp");
  assert.strictEqual(nameOf(Node.focused), "a.txt");
  w.key(Node.focused, "Home");
  assert.strictEqual(nameOf(Node.focused), "dir1");
});

test("keys: ArrowRight opens a folder, then enters it; ArrowLeft goes back to the parent, then closes it", async () => {
  const w = world(1280);
  const { c, row } = await focused(w, "dir1");
  w.key(row, "ArrowRight");
  await new Promise((r) => setImmediate(r)); // the listing arrives
  assert.strictEqual(row.attrs["aria-expanded"], "true");
  assert.deepStrictEqual(rowsOf(c).map(nameOf), ["dir1", "inner.txt", "sub", "dir2", "a.txt", "b.png"]);
  assert.strictEqual(Node.focused, row, "opening leaves focus on the folder");
  w.key(row, "ArrowRight");
  assert.strictEqual(nameOf(Node.focused), "inner.txt", "an open folder: Right enters it");
  w.key(Node.focused, "ArrowLeft");
  assert.strictEqual(Node.focused, row, "a child: Left goes to its parent");
  w.key(row, "ArrowLeft");
  await new Promise((r) => setImmediate(r));
  assert.strictEqual(row.attrs["aria-expanded"], "false");
  assert.deepStrictEqual(rowsOf(c).map(nameOf), ["dir1", "dir2", "a.txt", "b.png"]);
  assert.strictEqual(Node.focused, row, "closing leaves focus on the folder");
});

test("keys: Enter does what a click does (a file opens, a folder toggles)", async () => {
  const w = world(1280);
  const { c, row } = await focused(w, "a.txt");
  const ev = w.key(row, "Enter");
  assert.strictEqual(ev.prevented, 1);
  assert.deepStrictEqual(w.calls, ["openFile:/h/w/a.txt"]);
  rowsOf(c)[0].focus();
  w.key(rowsOf(c)[0], "Enter");
  await new Promise((r) => setImmediate(r));
  assert.strictEqual(rowsOf(c)[0].attrs["aria-expanded"], "true");
});

test("keys: a key typed in a row's ⋯, with a modifier, or one the tree does not own is left alone", async () => {
  const w = world(1280);
  const { c, row } = await focused(w, "dir1");
  const more = byCls(row, "ft-more")[0];
  more.focus();
  assert.strictEqual(w.key(more, "Enter").prevented, 0, "Enter on the ⋯ presses the ⋯, it must not also toggle the folder");
  assert.strictEqual(w.menu.className.includes("open"), false);
  row.focus();
  for (const [k, mods] of [["ArrowDown", { ctrlKey: true }], ["ArrowDown", { altKey: true }], ["ArrowDown", { metaKey: true }], ["a", {}], ["Tab", {}], ["F10", { shiftKey: false }]]) {
    assert.strictEqual(w.key(row, k, mods).prevented, 0, k + " " + JSON.stringify(mods));
  }
  assert.strictEqual(nameOf(Node.focused), "dir1");
  assert.deepStrictEqual(stops(c), ["dir1"]);
});

// ---- the row menu from the keyboard, on the kit menu (desktop) -------------------------------------------------------------
const items = (menu) => byCls(menu, "menu-item");
const text = (n) => byCls(n, "menu-text")[0].textContent;
// shape: the menu's children as one line each: the path header, the rows (danger marked), the separators.
const shape = (menu) => menu.children.map((n) => (n.className === "menu-sep" ? "---" : n.className.startsWith("menu-label") ? "label:" + n.textContent : (n.className.includes("is-danger") ? "!" : "") + text(n)));

test("Shift+F10 on a folder row opens its menu: the kit's rows, focus on the first, anchored to the row's ⋯", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  const ev = w.key(row, "F10", { shiftKey: true });
  assert.strictEqual(ev.prevented, 1, "the browser's own context menu is not also shown");
  assert.ok(w.menu.className.split(" ").includes("open"));
  assert.ok(w.menu.className.split(" ").includes("menu"), "the kit's .menu container");
  assert.strictEqual(w.menu.attrs.role, "menu");
  assert.strictEqual(w.menu.attrs["aria-label"], "Folder actions");
  assert.deepStrictEqual(shape(w.menu), [
    "label:~/w/dir1",
    "Insert path at prompt", "Copy path", "Copy relative path", "---",
    "Download .tar.gz", "New folder here…", "---",
    "Rename…", "---",
    "!Delete…",
  ]);
  assert.strictEqual(Node.focused, items(w.menu)[0], "focus lands on the first row");
});

test("the ContextMenu key opens the same menu; on a file it is the file's (Copy contents, no New folder)", async () => {
  const w = world(1280);
  const { row } = await focused(w, "a.txt");
  w.key(row, "ContextMenu");
  assert.strictEqual(w.menu.attrs["aria-label"], "File actions");
  assert.deepStrictEqual(shape(w.menu), [
    "label:~/w/a.txt",
    "Insert path at prompt", "Copy path", "Copy relative path", "Copy contents", "---",
    "Download", "---",
    "Rename…", "---",
    "!Delete…",
  ]);
});

test("the menu's rows are kit rows with a vendored glyph each and a red Delete…", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  const rows = items(w.menu);
  assert.deepStrictEqual(rows.map((b) => [b.tag, b.attrs.role, b.className]), rows.map((b, i) => ["button", "menuitem", i === rows.length - 1 ? "menu-item is-danger" : "menu-item"]));
  const icons = rows.map((b) => byCls(b, "menu-icon")[0].children[0].attrs["data-icon"]);
  assert.deepStrictEqual(icons, ["arrow-right-to-line", "clipboard-copy", "clipboard-copy", "download", "folder-plus", "pencil-line", "trash-2"]);
  assert.deepStrictEqual(w.menu.all((n) => n.tag === "hr" || n.className === "ft-menu-path"), [], "no legacy nodes on desktop");
});

test("a read-only listing's menu on desktop has no separator before a write action and no write action", async () => {
  const w = world(1280, { "/h/w": { readonly: true, entries: [{ name: "r.txt", is_dir: false }] } });
  const { row } = await focused(w, "r.txt");
  w.key(row, "ContextMenu");
  assert.deepStrictEqual(shape(w.menu), ["label:~/w/r.txt", "Insert path at prompt", "Copy path", "Copy relative path", "Copy contents", "---", "Download"]);
});

test("the path header wraps its text (it is where a keyboard user reads a name the tree cut), mono and muted", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  const head = w.menu.children[0];
  assert.strictEqual(head.className, "menu-label");
  const p = head.children[0];
  assert.strictEqual(p.className, "mono t-xs muted");
  assert.strictEqual(p.textContent, "~/w/dir1");
});

test("ArrowDown/ArrowUp walk the menu's rows over the separators; Home and End jump; Escape closes and gives focus back to the row", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  const rows = items(w.menu);
  assert.strictEqual(w.docKey("ArrowDown").prevented, 1);
  assert.strictEqual(Node.focused, rows[1]);
  w.docKey("End");
  assert.strictEqual(Node.focused, rows[rows.length - 1], "End is Delete…");
  w.docKey("ArrowDown");
  assert.strictEqual(Node.focused, rows[0], "Down wraps");
  w.docKey("ArrowUp");
  assert.strictEqual(Node.focused, rows[rows.length - 1], "Up wraps");
  const esc = w.docKey("Escape");
  assert.strictEqual(esc.prevented, 1, "marked used: the maximized drawer's Escape must not also fire");
  assert.strictEqual(w.menu.className.split(" ").includes("open"), false);
  assert.strictEqual(Node.focused, row, "focus is back on the row the menu hung from");
});

test("Escape with no menu open does nothing, and is not marked used", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  const esc = w.docKey("Escape");
  assert.strictEqual(esc.prevented, 0);
  assert.strictEqual(Node.focused, row);
});

test("choosing a row closes the menu, puts focus on the row, then runs the action (a dialog returns focus to the row)", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  Node.log.length = 0;
  const rename = items(w.menu).find((b) => text(b) === "Rename…");
  rename.click();
  assert.strictEqual(w.menu.className.split(" ").includes("open"), false, "closed first");
  assert.deepStrictEqual(Node.log, ["focus:ft-node"], "the only focus move is to the row, before the dialog opens");
  assert.strictEqual(Node.focused, row);
  assert.deepStrictEqual(w.calls, ["askPrompt:Rename"]);
});

test("a menu opened with the mouse (the ⋯) is the same menu, and Escape still ends on the row", async () => {
  const w = world(1280);
  const c = await w.render();
  const row = rowsOf(c)[1];
  byCls(row, "ft-more")[0].click();
  assert.ok(w.menu.className.split(" ").includes("open"));
  assert.strictEqual(Node.focused, items(w.menu)[0]);
  w.docKey("Escape");
  assert.strictEqual(Node.focused, row);
});

test("Escape after a refresh rebuilt the row: focus goes to the new row for the same path", async () => {
  const w = world(1280);
  const c = await w.render();
  const old = rowsOf(c)[2];
  old.focus();
  w.key(old, "ContextMenu");
  c.remove(); // ftLoadRoot swapped in a fresh tree under the open menu
  const fresh = await w.render();
  assert.strictEqual(old.isConnected, false);
  w.docKey("Escape");
  assert.strictEqual(Node.focused, rowsOf(fresh)[2], "a.txt, found again by its path");
  assert.strictEqual(nameOf(Node.focused), "a.txt");
});

test("the arrow keys of an open menu are not taken while focus is outside it", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  row.focus(); // the user tabbed back to the tree; the menu is still open
  assert.strictEqual(w.docKey("ArrowDown").prevented, 0);
});

// ---- contextmenu (review M5): the native menu must not stack on the kit's, and a keyboard or AT contextmenu opens it ----------
test("a contextmenu inside the open kit menu is prevented (Windows fires it on the Menu key's keyup, with focus already in the menu)", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  const first = items(w.menu)[0];
  assert.strictEqual(Node.focused, first);
  assert.strictEqual(w.docCtxMenu(first).prevented, 1, "on the row the key landed on");
  assert.strictEqual(w.docCtxMenu(w.menu.children[0]).prevented, 1, "and on the path header, anywhere inside the menu");
  assert.strictEqual(w.docCtxMenu(w.root).prevented, 0, "outside the menu the page's own menu is left alone");
  assert.strictEqual(w.docCtxMenu(row).prevented, 0, "a row of the tree is not the menu");
});

test("a contextmenu inside #ft-menu is left alone when it does not hold kit rows (the viewer's line menu, the phone's buttons)", async () => {
  const w = world(1280);
  const head = new Node("div"); head.className = "ft-menu-path";
  const gutter = new Node("span");
  w.root.append(gutter);
  w.ctx.ftShowMenu(gutter, [head], true);
  assert.strictEqual(w.docCtxMenu(head).prevented, 0, "the plain popover keeps what it had");
  const p = world(390);
  const c = await p.render();
  byCls(rowsOf(c)[2], "ft-more")[0].click();
  assert.ok(p.menu.className.split(" ").includes("open"));
  assert.strictEqual(p.docCtxMenu(p.menu.children[1]).prevented, 0, "the phone's menu is untouched");
});

test("a contextmenu on a focused tree row (the keyboard's, or an assistive technology's) opens its menu like Shift+F10, and is prevented", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  const ev = w.ctxMenu(row, { button: 0 });
  assert.strictEqual(ev.prevented, 1, "the browser's own menu is not also shown");
  assert.ok(w.menu.className.split(" ").includes("open"));
  assert.strictEqual(w.menu.attrs["aria-label"], "Folder actions");
  assert.strictEqual(Node.focused, items(w.menu)[0], "focus lands on the first row, as the key does");
  // The same menu the key opens, row for row.
  const viaKey = world(1280);
  const f2 = await focused(viaKey, "dir1");
  viaKey.key(f2.row, "F10", { shiftKey: true });
  assert.deepStrictEqual(shape(w.menu), shape(viaKey.menu));
  // A file's own menu, and a contextmenu with no button at all (an AT's synthetic event).
  const fw = world(1280);
  const file = await focused(fw, "a.txt");
  assert.strictEqual(fw.ctxMenu(file.row, {}).prevented, 1);
  assert.strictEqual(fw.menu.attrs["aria-label"], "File actions");
});

test("outside the dock a mouse right-click keeps the browser's menu: only the row itself, with a non-mouse contextmenu, opens ours (and not on the phone)", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  assert.strictEqual(w.ctxMenu(row, { button: 2 }).prevented, 0, "the right button is the pointer's");
  assert.strictEqual(w.menu.className.split(" ").includes("open"), false);
  const name = byCls(row, "ft-name")[0];
  assert.strictEqual(w.ctxMenu(name, { button: 0 }).prevented, 0, "a click on a part of the row is not the focused row's key");
  assert.strictEqual(w.menu.className.split(" ").includes("open"), false);
  const p = world(390);
  const f = await focused(p, "dir1");
  assert.strictEqual(p.ctxMenu(f.row, { button: 0 }).prevented, 0, "desktop behaviour only: the phone keeps what it had");
  assert.strictEqual(p.menu.className.split(" ").includes("open"), false);
});

// ---- the docked tree (Task 9a): a click selects, a right-click opens the row's menu at the pointer ---------------------------
// The dock is showing the tree: filetree.js asks drawerOwnsFileClicks (filesdrawer.js) and shows a file through drawerShowFile.
// Each opened file is recorded, "(kept)" for the double-click's pinned tab.
function docked(w) {
  // dockOn: the dock can stop showing Files (closed, another kind, the viewport crossing to the phone) while the page lives.
  w.run("var dockOn = true; var dockShown = []; function drawerOwnsFileClicks() { return dockOn; } function drawerShowFile(p, o) { dockShown.push(p + (o && o.preview === false ? ' (kept)' : '')); }");
  return () => Array.from(w.run("dockShown"));
}
const selected = (c) => rowsOf(c).filter(hasCls("ft-sel")).map(nameOf);
const rowNamed = (c, name) => rowsOf(c).find((r) => nameOf(r) === name);
const menuAt = (w) => [w.menu.style.left, w.menu.style.top];

test("ftSelect: a click on a docked row (file or folder) selects it and only it", async () => {
  const w = world(1280);
  const shown = docked(w);
  const c = await w.render();
  assert.deepStrictEqual(selected(c), []);
  rowNamed(c, "a.txt").click();
  assert.deepStrictEqual(selected(c), ["a.txt"]);
  assert.deepStrictEqual(shown(), ["/h/w/a.txt"], "the click still shows the file in the dock");
  rowNamed(c, "dir1").click();
  assert.deepStrictEqual(selected(c), ["dir1"], "the selection moved: one row at a time");
  rowNamed(c, "b.png").click();
  assert.deepStrictEqual(selected(c), ["b.png"]);
});

test("ftSelect: a double-click leaves its row selected (select is not a toggle) and still keeps the tab", async () => {
  const w = world(1280);
  const shown = docked(w);
  const c = await w.render();
  const row = rowNamed(c, "a.txt");
  row.click(); row.click(); row.ondblclick({ target: row });
  assert.deepStrictEqual(selected(c), ["a.txt"]);
  assert.deepStrictEqual(shown(), ["/h/w/a.txt", "/h/w/a.txt", "/h/w/a.txt (kept)"]);
});

test("ftSelect: with no dock (the phone) a click selects nothing and opens the file in a pane, as today", async () => {
  const w = world(390);
  const c = await w.render();
  rowNamed(c, "a.txt").click();
  rowNamed(c, "dir1").click();
  assert.deepStrictEqual(selected(c), []);
  assert.deepStrictEqual(w.calls, ["openFile:/h/w/a.txt"]);
});

test("a right-click on a docked row opens its menu where the pointer is, and selects the row it acts on", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  const row = rowNamed(c, "a.txt");
  const ev = w.ctxMenu(byCls(row, "ft-name")[0], { button: 2, clientX: 500, clientY: 300 });
  assert.strictEqual(ev.prevented, 1, "the browser's own menu is not also shown");
  assert.ok(w.menu.className.split(" ").includes("open"));
  assert.strictEqual(w.menu.attrs["aria-label"], "File actions");
  assert.deepStrictEqual(menuAt(w), ["500px", "300px"], "the menu's top-left corner is the pointer");
  assert.deepStrictEqual(selected(c), ["a.txt"]);
  assert.strictEqual(Node.focused, items(w.menu)[0], "focus lands on the first row, as the key does");
  assert.strictEqual(moreOf(row).attrs["aria-expanded"], "true", "the row's ⋯ still says its menu is open");
  // The same rows as the ⋯ and the key give: it is the one menu.
  const viaKey = world(1280);
  const f2 = await focused(viaKey, "a.txt");
  viaKey.key(f2.row, "F10", { shiftKey: true });
  assert.deepStrictEqual(shape(w.menu), shape(viaKey.menu));
});

test("a right-click on a folder row's icon opens that folder's menu; Escape hands focus back to the row", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  const row = rowNamed(c, "dir1");
  assert.strictEqual(w.ctxMenu(byCls(row, "ft-icon")[0], { button: 2, clientX: 40, clientY: 60 }).prevented, 1);
  assert.strictEqual(w.menu.attrs["aria-label"], "Folder actions");
  assert.strictEqual(shape(w.menu)[0], "label:~/w/dir1", "the path header names the row");
  w.docKey("Escape");
  assert.ok(!w.menu.className.split(" ").includes("open"));
  assert.strictEqual(Node.focused, row);
});

test("a right-click near the viewport's edges is clamped inside it (8px), by the menu's own size", async () => {
  const w = world(1280); // the window is 1280x900, the stand-in menu 200x220
  docked(w);
  const c = await w.render();
  const name = byCls(rowNamed(c, "b.png"), "ft-name")[0];
  w.ctxMenu(name, { button: 2, clientX: 1275, clientY: 895 });
  assert.deepStrictEqual(menuAt(w), ["1072px", "672px"], "right and bottom: the whole menu stays on screen");
  w.ctxMenu(name, { button: 2, clientX: 1, clientY: 2 });
  assert.deepStrictEqual(menuAt(w), ["8px", "8px"], "left and top");
  w.ctxMenu(name, { button: 2, clientX: 640, clientY: 450 });
  assert.deepStrictEqual(menuAt(w), ["640px", "450px"], "away from the edges: exactly the pointer");
});

test("a right-click off the rows (the empty tree) or on the phone keeps the browser's menu; a read-only listing's right-click menu has no Delete", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  assert.strictEqual(w.ctxMenu(w.tree, { button: 2, clientX: 5, clientY: 5 }).prevented, 0, "empty space keeps the browser's menu");
  assert.ok(!w.menu.className.split(" ").includes("open"));
  const p = world(390);
  docked(p);
  const pc = await p.render();
  assert.strictEqual(p.ctxMenu(byCls(rowNamed(pc, "a.txt"), "ft-name")[0], { button: 2, clientX: 5, clientY: 5 }).prevented, 0, "desktop only");
  assert.ok(!p.menu.className.split(" ").includes("open"));
  // A read-only listing's menu (no Rename, Delete) is the one the right-click opens, too.
  const ro = world(1280, { "/h/w": { entries: [{ name: "a.txt", is_dir: false }], readonly: true } });
  docked(ro);
  const rc = await ro.render();
  ro.ctxMenu(byCls(rowNamed(rc, "a.txt"), "ft-name")[0], { button: 2, clientX: 10, clientY: 10 });
  assert.ok(!shape(ro.menu).includes("Delete\u2026"), "a read-only listing offers no Delete");
});

test("a Ctrl+click on macOS (a contextmenu with button 0 and ctrlKey) on the name, the icon or the row opens the menu at the pointer", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  const row = rowNamed(c, "a.txt");
  const ev = w.ctxMenu(byCls(row, "ft-name")[0], { button: 0, ctrlKey: true, clientX: 600, clientY: 200 });
  assert.strictEqual(ev.prevented, 1, "the browser's own menu is not also shown");
  assert.deepStrictEqual(menuAt(w), ["600px", "200px"]);
  assert.strictEqual(w.menu.attrs["aria-label"], "File actions");
  assert.deepStrictEqual(selected(c), ["a.txt"]);
  w.docKey("Escape");
  const icon = byCls(rowNamed(c, "dir1"), "ft-icon")[0];
  assert.strictEqual(w.ctxMenu(icon, { button: 0, ctrlKey: true, clientX: 40, clientY: 70 }).prevented, 1);
  assert.deepStrictEqual(menuAt(w), ["40px", "70px"]);
  assert.strictEqual(w.menu.attrs["aria-label"], "Folder actions");
  w.docKey("Escape");
  assert.strictEqual(w.ctxMenu(rowNamed(c, "b.png"), { button: 0, ctrlKey: true, clientX: 9, clientY: 9 }).prevented, 1, "the row's own padding too");
  assert.deepStrictEqual(menuAt(w), ["9px", "9px"]);
  // Outside the dock, and on the phone, a Ctrl+click keeps the browser's menu.
  const n = world(1280);
  const nc = await n.render();
  assert.strictEqual(n.ctxMenu(byCls(rowNamed(nc, "a.txt"), "ft-name")[0], { button: 0, ctrlKey: true }).prevented, 0);
  const p = world(390);
  docked(p);
  const pc = await p.render();
  assert.strictEqual(p.ctxMenu(byCls(rowNamed(pc, "a.txt"), "ft-name")[0], { button: 0, ctrlKey: true }).prevented, 0);
  assert.ok(!p.menu.className.split(" ").includes("open"));
});

test("ftSelect also says aria-selected: true on the selected row, gone from the one it left", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  const sel = (r) => r.attrs["aria-selected"] === undefined ? "-" : r.attrs["aria-selected"];
  assert.deepStrictEqual(rowsOf(c).map(sel), ["-", "-", "-", "-"]);
  rowNamed(c, "a.txt").click();
  assert.deepStrictEqual(rowsOf(c).map((r) => nameOf(r) + ":" + sel(r)), ["dir1:-", "dir2:-", "a.txt:true", "b.png:-"]);
  rowNamed(c, "b.png").click();
  assert.deepStrictEqual(rowsOf(c).map((r) => nameOf(r) + ":" + sel(r)), ["dir1:-", "dir2:-", "a.txt:-", "b.png:true"]);
  w.ctxMenu(byCls(rowNamed(c, "dir2"), "ft-name")[0], { button: 2, clientX: 1, clientY: 1 });
  assert.deepStrictEqual(rowsOf(c).map((r) => nameOf(r) + ":" + sel(r)), ["dir1:-", "dir2:true", "a.txt:-", "b.png:-"], "a right-click moves it too");
  assert.deepStrictEqual(selected(c), ["dir2"], "the tint and the attribute agree");
});

// The dock draws the session's edit and a folder's changes as a bar and a tint, so the words live in the DOM instead: the
// .ft-chg span carries a label (role img, aria-label, title) and the hover text of the name and the row lists the marks.
test("the session mark carries its words: role img, aria-label, title on the span, and the same on the name's and the row's hover text", async () => {
  const w = world(1280);
  docked(w);
  const c = await w.render();
  const row = rowNamed(c, "a.txt");
  const name = byCls(row, "ft-name")[0];
  assert.strictEqual(name.title, "~/w/a.txt", "unmarked: the path, as always");
  assert.strictEqual(row.title, "");
  w.run("ftPaintChanged(new Set(['/h/w/a.txt']))");
  const span = byCls(row, "ft-chg")[0];
  assert.ok(span, "the span is still built (the bar is drawn from it)");
  assert.strictEqual(span.attrs.role, "img");
  assert.strictEqual(span.attrs["aria-label"], "Edited by this session's agent");
  assert.strictEqual(span.title, "Edited by this session's agent");
  assert.strictEqual(name.title, "~/w/a.txt\nEdited by this session's agent", "hovering the name reads the path, then the mark");
  assert.strictEqual(row.title, "Edited by this session's agent", "hovering the rest of the row reads the mark");
  assert.strictEqual(rowNamed(c, "b.png").title, "", "another row is untouched");
  w.run("ftPaintChanged(new Set())");
  assert.strictEqual(byCls(row, "ft-chg").length, 0);
  assert.strictEqual(name.title, "~/w/a.txt", "cleared: the path alone again");
  assert.strictEqual(row.title, "");
  // The phone keeps the titles it has (no dock: the dot is drawn there and carries its own).
  const p = world(390);
  const pc = await p.render();
  const prow = rowNamed(pc, "a.txt");
  p.run("ftPaintChanged(new Set(['/h/w/a.txt']))");
  assert.strictEqual(byCls(prow, "ft-name")[0].title, "~/w/a.txt");
  assert.strictEqual(prow.title, "");
});

// The dock leaving Files (closed, another kind, or the viewport crossing to the phone) clears the marks while the tree is no
// longer docked: the hover texts that listed them must go with them, or a row keeps "Edited by this session's agent" with
// nothing behind it.
test("when the dock leaves Files the marks are cleared and so are their words: the name's title is the path again and the row has none", async () => {
  const w = world(1280);
  docked(w);
  w.run(read("gitstatus.js"), "gitstatus.js");
  const c = await w.render();
  const file = rowNamed(c, "a.txt");
  const dir = rowNamed(c, "dir1");
  const fname = byCls(file, "ft-name")[0];
  const dname = byCls(dir, "ft-name")[0];
  w.run("ftPaintChanged(new Set(['/h/w/a.txt', '/h/w/dir1']))");
  w.run("gitState = { root: '/h/w', sid: null, status: { state: 'ok' }, marks: new Map([['/h/w/dir1', { code: 'M', dir: true, title: '2 changes inside' }]]) }; gitMarkRow(ftRowFor('/h/w/dir1'), '/h/w/dir1');");
  assert.strictEqual(fname.title, "~/w/a.txt\nEdited by this session's agent");
  assert.strictEqual(dname.title, "~/w/dir1\n2 changes inside\nEdited by this session's agent");
  // filesdrawer.js drawerPaintChanged: the drawer is no longer Files, so the changed set is null; the same goes for a git
  // repaint that finds no marks after the tree stopped being the dock's.
  w.run("dockOn = false");
  w.run("ftPaintChanged(null)");
  assert.strictEqual(byCls(file, "ft-chg").length, 0);
  assert.strictEqual(fname.title, "~/w/a.txt", "the path alone: no mark is left to explain");
  assert.strictEqual(file.title, "");
  assert.strictEqual(dname.title, "~/w/dir1", "not docked: the path alone (a visible dot, the phone's, carries its own title)");
  assert.strictEqual(dir.title, "");
  w.run("gitState = { root: '/h/w', sid: null, status: { state: 'ok' }, marks: new Map() }; gitMarkRow(ftRowFor('/h/w/dir1'), '/h/w/dir1');");
  assert.strictEqual(byCls(dir, "ft-git-dot").length, 0);
  assert.strictEqual(dname.title, "~/w/dir1");
  assert.strictEqual(dir.title, "");
});

test("a folder's git dot keeps its words in the name's hover text, with the session's after them (gitstatus.js on the same tree)", async () => {
  const w = world(1280);
  docked(w);
  w.run(read("gitstatus.js"), "gitstatus.js");
  const c = await w.render();
  const row = rowNamed(c, "dir1");
  const name = byCls(row, "ft-name")[0];
  w.run("gitState = { root: '/h/w', sid: null, status: { state: 'ok' }, marks: new Map([['/h/w/dir1', { code: 'M', dir: true, title: '2 changes inside' }]]) }; gitMarkRow(ftRowFor('/h/w/dir1'), '/h/w/dir1');");
  const dot = byCls(row, "ft-git-dot")[0];
  assert.ok(dot, "the dot is built, for assistive technology and for the tint");
  assert.strictEqual(dot.attrs.role, "img");
  assert.strictEqual(dot.attrs["aria-label"], "2 changes inside");
  assert.strictEqual(name.title, "~/w/dir1\n2 changes inside");
  assert.strictEqual(row.title, "2 changes inside");
  w.run("ftPaintChanged(new Set(['/h/w/dir1']))");
  assert.strictEqual(name.title, "~/w/dir1\n2 changes inside\nEdited by this session's agent");
  w.run("gitState = { root: '/h/w', sid: null, status: { state: 'ok' }, marks: new Map() }; gitMarkRow(ftRowFor('/h/w/dir1'), '/h/w/dir1');");
  assert.strictEqual(byCls(row, "ft-git-dot").length, 0, "no mark any more");
  assert.strictEqual(name.title, "~/w/dir1\nEdited by this session's agent", "the git line is gone, the session's stays");
});

// A folder's git dot is not the session's mark, so a repaint of the session marks that finds nothing to add or remove on its
// row used to leave the row's hover text as it was. The title can have been reset to the path alone while the tree was not
// the dock's (a git repaint then), with the dot still on the row, so re-docking must give the note back on every row.
test("ftPaintChanged rebuilds every row's hover text, so a folder's git note comes back after the dock is shown again", async () => {
  const w = world(1280);
  docked(w);
  w.run(read("gitstatus.js"), "gitstatus.js");
  const c = await w.render();
  const row = rowNamed(c, "dir1");
  const name = byCls(row, "ft-name")[0];
  w.run("gitState = { root: '/h/w', sid: null, status: { state: 'ok' }, marks: new Map([['/h/w/dir1', { code: 'M', dir: true, title: '2 changes inside' }]]) }; gitMarkRow(ftRowFor('/h/w/dir1'), '/h/w/dir1');");
  assert.strictEqual(name.title, "~/w/dir1\n2 changes inside");
  w.run("dockOn = false; ftNameTitle(ftRowFor('/h/w/dir1'));"); // a git repaint while the tree is not the dock's
  assert.strictEqual(name.title, "~/w/dir1", "not docked: the path alone, the dot stays on the row");
  assert.strictEqual(byCls(row, "ft-git-dot").length, 1);
  w.run("dockOn = true; ftPaintChanged(new Set(['/h/w/a.txt']));"); // the dock shows Files again; the folder is not in the set
  assert.strictEqual(name.title, "~/w/dir1\n2 changes inside", "the folder's note is back in the name's hover text");
  assert.strictEqual(row.title, "2 changes inside");
});

test("initFileTree registers the contextmenu guard on the document, beside the click closer and the key handler", () => {
  const src = read("filetree.js");
  const init = src.slice(src.indexOf("async function initFileTree"));
  assert.match(init, /document\.addEventListener\("contextmenu", ftMenuContextMenu\)/);
  assert.match(src, /el\.addEventListener\("contextmenu", ftTreeContextMenu\)/);
});

// ---- the row ⋯ is a menu button (review M9) --------------------------------------------------------------------------------
const moreOf = (row) => byCls(row, "ft-more")[0];
for (const [label, width] of [["desktop", 1280], ["phone", 390]]) {
  test("every row's ⋯ is a menu button that is closed at rest (" + label + "): aria-haspopup=menu, aria-expanded=false", async () => {
    const w = world(width);
    w.run("addExpanded(ftExpanded, '/h/w/dir1');");
    const c = await w.render();
    assert.ok(rowsOf(c).length > 4);
    for (const r of rowsOf(c)) {
      assert.strictEqual(moreOf(r).attrs["aria-haspopup"], "menu", nameOf(r));
      assert.strictEqual(moreOf(r).attrs["aria-expanded"], "false", nameOf(r));
    }
  });
}

test("the ⋯ says expanded while its menu is open, and collapsed again on Escape, a chosen row, a click elsewhere or a newer menu (desktop)", async () => {
  const w = world(1280);
  const c = await w.render();
  const [r0, r1] = rowsOf(c);
  moreOf(r0).click();
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "true");
  assert.strictEqual(moreOf(r1).attrs["aria-expanded"], "false", "only the one that opened it");
  w.docKey("Escape");
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "false", "Escape");
  moreOf(r0).click();
  items(w.menu).find((b) => text(b) === "Rename…").click();
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "false", "a chosen row");
  moreOf(r0).click();
  w.ctx.closeRowMenu(); // initFileTree's document click closer, for a click elsewhere
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "false", "a click elsewhere");
  moreOf(r0).click();
  moreOf(r1).click(); // the ⋯ stops its click: the document closer never runs, the newer menu just replaces the first
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "false", "replaced by a newer menu");
  assert.strictEqual(moreOf(r1).attrs["aria-expanded"], "true");
  w.docKey("Escape");
  assert.deepStrictEqual(rowsOf(c).map((r) => moreOf(r).attrs["aria-expanded"]), ["false", "false", "false", "false"]);
});

test("the ⋯ expanded state follows the menu the same way on the phone, and the Shift+F10 route sets it too", async () => {
  const p = world(390);
  const c = await p.render();
  const row = rowsOf(c)[2];
  moreOf(row).click();
  assert.strictEqual(moreOf(row).attrs["aria-expanded"], "true");
  p.ctx.closeRowMenu();
  assert.strictEqual(moreOf(row).attrs["aria-expanded"], "false");
  const w = world(1280);
  const { row: r } = await focused(w, "dir2");
  w.key(r, "F10", { shiftKey: true });
  assert.strictEqual(moreOf(r).attrs["aria-expanded"], "true", "the key presses the ⋯, so it is the opener");
  w.docKey("Escape");
  assert.strictEqual(moreOf(r).attrs["aria-expanded"], "false");
});

test("the viewer's line menu (anchored to a gutter, not a ⋯) closes the ⋯ that was open and marks no one expanded", async () => {
  const w = world(1280);
  const c = await w.render();
  const r0 = rowsOf(c)[0];
  moreOf(r0).click();
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "true");
  const head = new Node("div"); head.className = "ft-menu-path";
  const gutter = new Node("span");
  w.root.append(gutter);
  w.ctx.ftShowMenu(gutter, [head], true);
  assert.strictEqual(moreOf(r0).attrs["aria-expanded"], "false");
  assert.ok(!("aria-expanded" in gutter.attrs), "the gutter is not a menu button");
  w.ctx.closeRowMenu();
  assert.deepStrictEqual(rowsOf(c).map((r) => moreOf(r).attrs["aria-expanded"]), ["false", "false", "false", "false"]);
});

// ---- the phone keeps today's menu ---------------------------------------------------------------------------------------
test("phone: the row menu is today's path header, plain buttons and rules (no Insert path at prompt: every terminal is hidden on the Files screen), with no kit class, role or focus move", async () => {
  const w = world(390);
  const c = await w.render();
  const row = rowsOf(c)[2]; // a.txt
  byCls(row, "ft-more")[0].click();
  assert.ok(w.menu.className.split(" ").includes("open"));
  assert.strictEqual(w.menu.className.split(" ").includes("menu"), false);
  assert.strictEqual("role" in w.menu.attrs, false);
  assert.strictEqual("aria-label" in w.menu.attrs, false);
  assert.deepStrictEqual(w.menu.children.map((n) => (n.tag === "hr" ? "---" : n.className === "ft-menu-path" ? "path:" + n.textContent : n.tag + ":" + n.textContent)), [
    "path:~/w/a.txt",
    "button:Copy path", "button:Copy relative path", "button:Copy contents", "---",
    "button:Download", "---", "button:Rename…", "button:Delete…",
  ]);
  assert.strictEqual(w.menu.all((n) => n.className.includes("menu-item") || n.className.includes("menu-sep")).length, 0);
  assert.strictEqual(Node.focused, null, "the phone's menu takes no focus, as before");
});

test("the viewer's line menu (ftShowMenu with plain nodes) clears the kit's class and role off the shared #ft-menu", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  assert.ok(w.menu.className.split(" ").includes("menu"));
  const head = new Node("div"); head.className = "ft-menu-path";
  const gutter = new Node("span");
  w.root.append(gutter);
  w.ctx.ftShowMenu(gutter, [head], true);
  assert.strictEqual(w.menu.className.split(" ").includes("menu"), false);
  assert.strictEqual("role" in w.menu.attrs, false);
  assert.strictEqual("aria-label" in w.menu.attrs, false);
  assert.ok(w.menu.className.split(" ").includes("open"));
  // Its Escape does not drag focus onto a tree row: it did not hang from one.
  Node.focused = gutter;
  w.docKey("Escape");
  assert.strictEqual(Node.focused, gutter);
});

// ---- the stylesheet half ---------------------------------------------------------------------------------------------------
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const norm = (s) => stripComments(s).replace(/\s+/g, " ");
const desktop = norm(read("desktop.css"));
const app = norm(read("app.css"));
// The declarations of the rule whose selector list contains `sel`, as one string ("" if none).
function ruleBody(css, sel) {
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  const out = [];
  while ((m = re.exec(css))) if (m[1].split(",").map((s) => s.trim()).includes(sel)) out.push(m[2].trim());
  return out.join(" ");
}

test("desktop.css shows a row's ⋯ while the row has focus (F68); the phone's own rules are untouched", () => {
  assert.match(ruleBody(desktop, ".ft-node:focus-within .ft-more"), /visibility:\s*visible/);
  assert.match(ruleBody(app, ".ft-node:hover .ft-more"), /visibility:\s*visible/, "app.css still shows it on hover");
  assert.strictEqual(ruleBody(app, ".ft-node:focus-within .ft-more"), "", "the focus rule is a desktop rule, app.css has none");
});

test("desktop.css puts the focused row's 2px ring inside it, on every row, selected or not", () => {
  for (const sel of [".ft-node:focus-visible", ".ft-node.ft-sel:focus-visible"]) {
    const b = ruleBody(desktop, sel);
    assert.match(b, /outline:\s*var\(--focus\)/, sel);
    assert.match(b, /outline-offset:\s*-2px/, sel + " is inset: #filetree is a scroll container");
  }
});

test("desktop.css places #ft-menu as a kit menu: its own fixed box and the kit's values over app.css's id rules", () => {
  const box = ruleBody(desktop, "#ft-menu:has(> .menu-item)");
  assert.match(box, /border:\s*1px solid var\(--line\)/, "the kit's hairline, not app.css's var(--border)");
  assert.match(box, /padding:\s*4px/);
  assert.match(box, /box-shadow:\s*var\(--e-pop\)/);
  assert.match(box, /width:\s*max-content/);
  assert.match(ruleBody(desktop, "#ft-menu.open:has(> .menu-item)"), /display:\s*flex/);
  // app.css's `#ft-menu button` (1,0,1) out-ranks the kit's .menu-item (0,1,0): the kit's row values are restated.
  const row = ruleBody(desktop, "#ft-menu .menu-item");
  assert.match(row, /display:\s*flex/);
  assert.match(row, /padding:\s*0 10px/);
  assert.match(row, /font-size:\s*var\(--t-md\)/);
  assert.match(ruleBody(desktop, "#ft-menu .menu-item:hover"), /background:\s*var\(--raise\)/);
  assert.match(ruleBody(desktop, "#ft-menu .menu-item.is-danger"), /color:\s*var\(--red\)/);
  assert.match(ruleBody(desktop, "#ft-menu .menu-item.is-danger:hover"), /background:\s*var\(--red-tint\)/);
});

test("the legacy #ft-menu look stays in app.css for the phone", () => {
  assert.match(ruleBody(app, "#ft-menu button"), /padding:\s*6px 9px/);
  assert.match(ruleBody(app, "#ft-menu .ft-menu-path"), /word-break:\s*break-all/);
});
