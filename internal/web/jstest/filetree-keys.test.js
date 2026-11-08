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
    const scoped = /^:scope > (.+)$/.exec(sel);
    if (scoped) return this.children.filter((c) => c instanceof Node && c.matches(scoped[1]));
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
  });
  const run = (src, f) => new vm.Script(src, { filename: f || "t.js" }).runInContext(ctx);
  for (const f of ["svgicon.js", "kitmenu.js", "copyfile.js", "fsops.js", "treestate.js", "filetree.js"]) run(read(f), f);
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
  const render = async (into) => { const c = into || new Node("div"); if (!into) tree.append(c); await ctx.ftRenderDir("/h/w", c, ""); return c; };
  return { ctx, run, calls, root, tree, menu, key, docKey, render };
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

test("the menu's rows are kit rows with an inline-SVG icon each and a red Delete…", async () => {
  const w = world(1280);
  const { row } = await focused(w, "dir1");
  w.key(row, "ContextMenu");
  const rows = items(w.menu);
  assert.deepStrictEqual(rows.map((b) => [b.tag, b.attrs.role, b.className]), rows.map((b, i) => ["button", "menuitem", i === rows.length - 1 ? "menu-item is-danger" : "menu-item"]));
  const icons = rows.map((b) => byCls(b, "menu-icon")[0].children[0].attrs["data-icon"]);
  assert.deepStrictEqual(icons, ["insert", "copy", "copy", "download", "folder-plus", "pencil", "delete"]);
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

// ---- the phone keeps today's menu ---------------------------------------------------------------------------------------
test("phone: the row menu is today's path header, plain buttons and rules, with no kit class, role or focus move", async () => {
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
    "button:Insert path at prompt", "button:Copy path", "button:Copy relative path", "button:Copy contents", "---",
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
