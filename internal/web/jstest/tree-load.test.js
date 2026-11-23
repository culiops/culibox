// The tree's root load (filetree.js ftLoadRoot) as it runs in the page, in a vm with a fake DOM: when git is asked, what is
// on screen while the folders the user had open are still listing, and what a newer load leaves of an older one. Listings
// answer when the test says so (`answer`), so every in-between state can be looked at.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 12; i++) await new Promise((r) => setImmediate(r)); };

// What the tree's code uses of a node beyond fakedom's.
if (!Object.getOwnPropertyDescriptor(Node.prototype, "childNodes")) {
  Object.defineProperty(Node.prototype, "childNodes", { get() { return this.children.slice(); } });
}
if (!Node.prototype.closest) {
  Node.prototype.closest = function closest(sel) {
    for (let x = this; x; x = x.parent) if (x instanceof Node && x.is(sel)) return x;
    return null;
  };
}

function tree(expanded) {
  const doc = { activeElement: null, listeners: {} };
  doc.createElement = (t) => {
    const n = new Node(t, doc);
    n.style = Object.assign(n.style || {}, { setProperty() {} });
    return n;
  };
  doc.getElementById = () => null;
  doc.addEventListener = () => {};
  const st = { lists: [], git: [], repaints: 0, log: [] };
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, WeakMap,
    document: doc,
    localStorage: { getItem: () => JSON.stringify(expanded || []), setItem: () => {} },
    setTimeout: (fn) => { fn(); return 1; },
    clearTimeout: () => {},
    pendingRegion: () => () => {},
    shortPath: (p) => p,
    api: (url) => new Promise((resolve, reject) => {
      const p = decodeURIComponent(url.replace(/^.*\?path=/, ""));
      st.lists.push({ path: p, resolve, reject, done: false });
      st.log.push("list " + p);
    }),
    gitStatusLoad: (root) => { st.git.push(root); st.log.push("git " + root); },
    gitStatusTreeSid: () => "s1",
    gitRepaintRows: () => { st.repaints++; st.log.push("repaint"); },
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("treestate.js"), "treestate.js");
  load(src("filetree.js"), "filetree.js");
  load("ftExpanded = new Set(" + JSON.stringify(expanded || []) + "); ftTreeEl = document.createElement('div'); ftTreeEl.setAttribute('role', 'tree'); ftRootSel = document.createElement('input');", "init");
  const p = {
    ctx, st,
    run: (code) => load(code, "test"),
    // answer: the oldest pending listing of `dir` gets these entries.
    answer: async (dir, entries) => {
      const l = st.lists.find((x) => x.path === dir && !x.done);
      assert.ok(l, "a pending listing of " + dir);
      l.done = true;
      l.resolve({ path: dir, entries });
      await settle();
    },
    pending: () => st.lists.filter((x) => !x.done).map((x) => x.path),
    // The paths of the rows in the live tree, in order.
    shown: () => load('ftTreeEl.querySelectorAll(".ft-node").map((r) => ftRowPaths.get(r))', "shown"),
  };
  return p;
}

const dir = (name) => ({ name, is_dir: true, size: 0 });
const file = (name) => ({ name, is_dir: false, size: 1 });

test("git is asked as soon as the root is known, before its listing answers", async () => {
  const p = tree();
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  assert.deepStrictEqual(p.st.git, ["/w"]);
  assert.deepStrictEqual(p.pending(), ["/w"], "the listing is still out");
  assert.strictEqual(p.st.log[0], "git /w", "asked first");
});

test("the top level is drawn while the remembered folders list, side by side; the marks are repainted on the swap", async () => {
  const p = tree(["/w/a", "/w/b"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("a"), dir("b"), file("f.txt")]);
  assert.deepStrictEqual(p.shown(), ["/w/a", "/w/b", "/w/f.txt"], "the top level is on screen at once");
  assert.deepStrictEqual(p.pending(), ["/w/a", "/w/b"], "both remembered folders list at the same time");
  assert.ok(p.st.repaints >= 1, "the rows drawn off the page get the marks of a status that landed meanwhile");
  await p.answer("/w/b", [file("y.go")]);
  await p.answer("/w/a", [file("x.go")]);
  assert.deepStrictEqual(p.shown(), ["/w/a", "/w/a/x.go", "/w/b", "/w/b/y.go", "/w/f.txt"]);
});

test("ftLoading settles only once the remembered folders are drawn", async () => {
  const p = tree(["/w/a"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  let settled = false;
  p.run("ftLoading").then(() => { settled = true; });
  await settle();
  await p.answer("/w", [dir("a")]);
  assert.strictEqual(settled, false, "ftReveal must not look for rows that are still listing");
  await p.answer("/w/a", [file("x.go")]);
  await settle();
  assert.strictEqual(settled, true);
});

test("a newer load while the remembered folders list: the older root's folders never reach the new tree", async () => {
  const p = tree(["/w/a"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("a")]);
  p.run('setFtRoot("/v"); ftLoadRoot();');
  await settle();
  await p.answer("/v", [file("z.txt")]);
  assert.deepStrictEqual(p.shown(), ["/v/z.txt"]);
  await p.answer("/w/a", [file("x.go")]);
  assert.deepStrictEqual(p.shown(), ["/v/z.txt"], "the late listing of the old root adds nothing");
  assert.deepStrictEqual(p.st.git, ["/w", "/v"]);
});

test("the Tab stop goes back to the remembered row once its folder is drawn", async () => {
  const p = tree(["/w/a"]);
  p.run('ftRovePath = "/w/a/x.go"; setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("a"), file("f.txt")]);
  await p.answer("/w/a", [file("x.go")]);
  const stops = p.run(`ftTreeEl.querySelectorAll('.ft-node[tabindex="0"]').map((r) => ftRowPaths.get(r))`);
  assert.deepStrictEqual(stops, ["/w/a/x.go"]);
});

test("a remembered folder is drawn off the page and put under its row whole; one closed while it listed stays closed", async () => {
  const p = tree(["/w/a", "/w/b"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("a"), dir("b")]);
  // The user closes b while its listing is out.
  p.run('for (const r of ftTreeEl.querySelectorAll(".ft-node")) if (ftRowPaths.get(r) === "/w/b") r.onclick();');
  const boxes = () => p.run("ftTreeEl.children.length");
  const before = boxes();
  await p.answer("/w/a", [file("x.go"), file("y.go")]);
  assert.strictEqual(boxes(), before + 1, "a's rows arrive in one box, at once");
  assert.deepStrictEqual(p.shown(), ["/w/a", "/w/a/x.go", "/w/a/y.go", "/w/b"]);
  await p.answer("/w/b", [file("z.go")]);
  assert.deepStrictEqual(p.shown(), ["/w/a", "/w/a/x.go", "/w/a/y.go", "/w/b"], "b was closed: its late listing is not shown");
});

test("the marks are repainted once the remembered folders are attached: a status that landed while they listed reaches their rows", async () => {
  const p = tree(["/w/a"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("a")]);
  const r0 = p.st.repaints;
  await p.answer("/w/a", [file("x.go")]);
  assert.ok(p.st.repaints > r0, "repainted after a's rows went in");
});

// P5: the family goes on every file row however the tree was drawn: a root or a restored folder is listed before the dock opens
// on Files (found in the in-box check: those rows had none), and it is that tree the dock then shows.
test("file rows carry their family even when the tree is drawn with the dock closed; folders carry none", async () => {
  const p = tree(["/w/tf"]);
  p.run('setFtRoot("/w"); ftLoadRoot();');
  await settle();
  await p.answer("/w", [dir("tf"), file("Dockerfile"), file("notes.txt")]);
  await p.answer("/w/tf", [file("main.tf")]);
  const fams = p.run('ftTreeEl.querySelectorAll(".ft-node").map((r) => ftRowPaths.get(r) + "=" + (r.dataset.fam || ""))');
  assert.deepStrictEqual(fams, ["/w/tf=", "/w/tf/main.tf=tf", "/w/Dockerfile=build", "/w/notes.txt="]);
});
