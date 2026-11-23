// The tree's reveal (filetree.js ftReveal, P4) as it runs in the page, in a vm with a fake DOM: an automatic reveal opens the
// ancestors of the open file and selects its row without taking focus or saving what it opened; it waits for a folder that is
// still listing; a newer reveal or a newer root load supersedes it; outside the root an automatic reveal does nothing and an
// explicit one roots the tree at the file's repository and keeps that root while the focus key stays. Listings answer when the
// test says so (`answer`); /api/fs/where answers from `where`.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); };

if (!Object.getOwnPropertyDescriptor(Node.prototype, "childNodes")) {
  Object.defineProperty(Node.prototype, "childNodes", { get() { return this.children.slice(); } });
}
if (!Node.prototype.closest) {
  Node.prototype.closest = function closest(sel) {
    for (let x = this; x; x = x.parent) if (x instanceof Node && x.is(sel)) return x;
    return null;
  };
}

function tree(expanded, opts) {
  const o = opts || {};
  const doc = { activeElement: null, listeners: {} };
  doc.createElement = (t) => {
    const n = new Node(t, doc);
    n.style = Object.assign(n.style || {}, { setProperty() {} });
    n.scrollIntoView = (a) => { st.scrolls.push(a); };
    return n;
  };
  doc.getElementById = () => null;
  doc.addEventListener = () => {};
  const st = { lists: [], wheres: [], saves: [], opens: [], scrolls: [] };
  const where = o.where || {};
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, WeakMap, RegExp,
    document: doc,
    localStorage: { getItem: () => JSON.stringify({ v: 1, paths: expanded || [] }), setItem: (k, v) => { st.saves.push(JSON.parse(v).paths); } },
    setTimeout: (fn) => { fn(); return 1; },
    clearTimeout: () => {},
    pendingRegion: () => () => {},
    shortPath: (p) => p,
    dpClose: () => {},
    drawerOwnsFileClicks: () => o.docked !== false,
    dockSecOpen: (w) => { st.opens.push(w); },
    window: { WORKSPACE_ROOTS: ["/w", "/v", "/real"], HOME_DIR: "/w" },
    api: (url) => {
      if (url.indexOf("/api/fs/where?") === 0) {
        const p = decodeURIComponent(url.replace(/^.*\?path=/, ""));
        st.wheres.push(p);
        return where[p] ? Promise.resolve(where[p]) : Promise.reject(new Error("not found"));
      }
      return new Promise((resolve, reject) => {
        const p = decodeURIComponent(url.replace(/^.*\?path=/, ""));
        st.lists.push({ path: p, resolve, reject, done: false });
      });
    },
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("treestate.js"), "treestate.js");
  load(src("viewerpath.js"), "viewerpath.js");
  load(src("filetree.js"), "filetree.js");
  load("ftExpanded = loadExpanded(); ftTreeEl = document.createElement('div'); ftTreeEl.setAttribute('role', 'tree'); ftRootSel = document.createElement('input');", "init");
  const p = {
    ctx, st, doc,
    run: (code) => load(code, "test"),
    // answer: the oldest pending listing of `dir` gets these entries (and `resolved` as the path the server names).
    answer: async (dir, entries, resolved) => {
      const l = st.lists.find((x) => x.path === dir && !x.done);
      assert.ok(l, "a pending listing of " + dir + " (pending: " + p.pending().join(", ") + ")");
      l.done = true;
      l.resolve({ path: resolved || dir, entries });
      await settle();
    },
    fail: async (dir) => {
      const l = st.lists.find((x) => x.path === dir && !x.done);
      assert.ok(l, "a pending listing of " + dir);
      l.done = true;
      l.reject(new Error("forbidden"));
      await settle();
    },
    pending: () => st.lists.filter((x) => !x.done).map((x) => x.path),
    shown: () => load('ftTreeEl.querySelectorAll(".ft-node").map((r) => ftRowPaths.get(r))', "shown"),
    selected: () => load('ftTreeEl.querySelectorAll(".ft-sel").map((r) => ftRowPaths.get(r))', "sel"),
    stored: () => (st.saves.length ? st.saves[st.saves.length - 1] : expanded || []),
  };
  return p;
}

const dir = (name) => ({ name, is_dir: true, size: 0 });
const file = (name) => ({ name, is_dir: false, size: 1 });

async function rooted(p, root, entries) {
  p.run(`setFtRoot(${JSON.stringify(root)}); ftLoadRoot();`);
  await settle();
  await p.answer(root, entries);
}

test("an auto reveal opens the ancestors, selects the row, does not focus it and saves nothing", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a"), file("top.txt")]);
  const done = p.run('ftReveal("/w/a/b/f.go", { auto: true })');
  await settle();
  await p.answer("/w/a", [dir("b")]);
  await p.answer("/w/a/b", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.deepStrictEqual(p.selected(), ["/w/a/b/f.go"]);
  assert.strictEqual(p.doc.activeElement, null, "focus did not move");
  assert.deepStrictEqual(p.st.opens, [], "a collapsed tree is not opened by an automatic reveal");
  assert.deepStrictEqual(p.st.saves, [], "the folders a reveal opens are not remembered");
  assert.deepStrictEqual(p.stored(), []);
});

test("a manual reveal opens a collapsed tree and focuses the row, and saves nothing either", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  const done = p.run('ftReveal("/w/a/f.go")');
  await settle();
  await p.answer("/w/a", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.deepStrictEqual(p.st.opens, ["tree"]);
  assert.strictEqual(p.run("ftRowPaths.get(document.activeElement)"), "/w/a/f.go");
  assert.deepStrictEqual(p.st.saves, []);
});

test("a user click on a folder still remembers it, and re-opening refreshes it as the newest", async () => {
  const p = tree(["/w/b"]);
  await rooted(p, "/w", [dir("a"), dir("b")]);
  await p.answer("/w/b", []);
  p.run('for (const r of ftTreeEl.querySelectorAll(".ft-node")) if (ftRowPaths.get(r) === "/w/a") r.onclick();');
  await settle();
  assert.deepStrictEqual(p.st.saves[p.st.saves.length - 1], ["/w/b", "/w/a"]);
});

test("a reveal waits for a folder that is still listing", async () => {
  const p = tree(["/w/a"]);
  await rooted(p, "/w", [dir("a")]);
  assert.deepStrictEqual(p.pending(), ["/w/a"], "the remembered folder is listing");
  const done = p.run('ftReveal("/w/a/b/f.go", { auto: true })');
  await settle();
  await p.answer("/w/a", [dir("b")]);
  await p.answer("/w/a/b", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.deepStrictEqual(p.selected(), ["/w/a/b/f.go"]);
});

test("an open folder whose children are still listing is waited for, not given up on", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  // The user opens a; its listing is out when the reveal comes.
  p.run('for (const r of ftTreeEl.querySelectorAll(".ft-node")) if (ftRowPaths.get(r) === "/w/a") r.onclick();');
  await settle();
  const done = p.run('ftReveal("/w/a/f.go", { auto: true })');
  await settle();
  await p.answer("/w/a", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.deepStrictEqual(p.selected(), ["/w/a/f.go"]);
});

test("a newer reveal supersedes an older one", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a"), dir("b")]);
  const first = p.run('ftReveal("/w/a/x.go", { auto: true })');
  await settle();
  const second = p.run('ftReveal("/w/b/y.go", { auto: true })');
  await settle();
  await p.answer("/w/b", [file("y.go")]);
  await p.answer("/w/a", [file("x.go")]);
  assert.strictEqual(await first, false);
  assert.strictEqual(await second, true);
  assert.deepStrictEqual(p.selected(), ["/w/b/y.go"], "the older reveal never selects");
});

test("a root load during a reveal abandons it", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  const done = p.run('ftReveal("/w/a/f.go", { auto: true })');
  await settle();
  p.run('setFtRoot("/v"); ftLoadRoot();');
  await settle();
  await p.answer("/w/a", [file("f.go")]);
  await p.answer("/v", [file("z")]);
  assert.strictEqual(await done, false);
  assert.deepStrictEqual(p.selected(), []);
});

test("a failed ancestor listing: an auto reveal returns false", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  const done = p.run('ftReveal("/w/a/f.go", { auto: true })');
  await settle();
  await p.fail("/w/a");
  assert.strictEqual(await done, false);
  assert.deepStrictEqual(p.selected(), []);
});

test("outside the root, an auto reveal does not re-root", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  const ok = await p.run('ftReveal("/v/x/f.go", { auto: true })');
  assert.strictEqual(ok, false);
  assert.strictEqual(p.run("ftRoot()"), "/w");
  assert.deepStrictEqual(p.pending(), []);
});

test("manual reveal outside the root: roots at where's top, and the same follow key keeps that root; another key re-roots", async () => {
  const p = tree(null, { where: { "/v/repo/src/f.go": { path: "/v/repo/src/f.go", top: "/v/repo" } } });
  p.run('syncTreeToRoot("/w", "sess:s1");');
  await settle();
  await p.answer("/w", [dir("a")]);
  const done = p.run('ftReveal("/v/repo/src/f.go")');
  await settle();
  await p.answer("/v/repo", [dir("src")]);
  await p.answer("/v/repo/src", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.strictEqual(p.run("ftRoot()"), "/v/repo", "rooted at the repository, not the file's folder");
  assert.deepStrictEqual(p.selected(), ["/v/repo/src/f.go"]);
  p.run('syncTreeToRoot("/w", "sess:s1");'); // the dock re-applies for the same session (a file opened)
  await settle();
  assert.strictEqual(p.run("ftRoot()"), "/v/repo", "no snap-back while the focus key is the same");
  p.run('syncTreeToRoot("/w", "sess:s2");');
  await settle();
  assert.strictEqual(p.run("ftRoot()"), "/w", "another focus key re-roots");
});

test("manual reveal outside the root with no repo: roots at the file's folder", async () => {
  const p = tree(null, { where: { "/v/notes/n.md": { path: "/v/notes/n.md" } } });
  await rooted(p, "/w", []);
  const done = p.run('ftReveal("/v/notes/n.md")');
  await settle();
  await p.answer("/v/notes", [file("n.md")]);
  assert.strictEqual(await done, true);
  assert.strictEqual(p.run("ftRoot()"), "/v/notes");
});

test("a symlink spelling under the resolved root reveals (ftRevealTarget)", async () => {
  const p = tree();
  p.run('setFtRoot("/w/lnk"); ftLoadRoot();');
  await settle();
  await p.answer("/w/lnk", [dir("a")], "/real/proj");
  assert.strictEqual(p.run("ftRootResolved()"), "/real/proj");
  assert.strictEqual(p.run('ftRevealTarget("/w/lnk/a/f.go", null)'), "/w/lnk/a/f.go", "textually under the root");
  assert.strictEqual(p.run('ftRevealTarget("/elsewhere/f.go", { path: "/real/proj/a/f.go" })'), "/w/lnk/a/f.go", "the resolved spelling maps onto the root's");
  assert.strictEqual(p.run('ftRevealTarget("/elsewhere/f.go", { path: "/real/projX/f.go" })'), null, "by whole components");
  assert.strictEqual(p.run('ftRevealTarget("/elsewhere/f.go", null)'), null);
  const done = p.run('ftReveal("/elsewhere/f.go", { auto: true, where: { path: "/real/proj/a/f.go" } })');
  await settle();
  await p.answer("/w/lnk/a", [file("f.go")]);
  assert.strictEqual(await done, true);
  assert.deepStrictEqual(p.selected(), ["/w/lnk/a/f.go"]);
});

test("the dock lists a link to a folder as a folder, with the folders, and marks every link; the phone lists them as before", async () => {
  const entries = [dir("b"), { name: "a-lnk", is_dir: false, size: 3, link: true, target_dir: true }, file("c.txt"), { name: "f-lnk", is_dir: false, size: 3, link: true }];
  const p = tree();
  await rooted(p, "/w", entries);
  assert.deepStrictEqual(p.shown(), ["/w/a-lnk", "/w/b", "/w/c.txt", "/w/f-lnk"]);
  assert.deepStrictEqual(p.run('ftTreeEl.querySelectorAll(".ft-node").map((r) => r.getAttribute("aria-expanded"))'), ["false", "false", null, null]);
  assert.deepStrictEqual(p.run('ftTreeEl.querySelectorAll(".ft-link").map((r) => ftRowPaths.get(r))'), ["/w/a-lnk", "/w/f-lnk"]);
  const m = tree(null, { docked: false });
  await rooted(m, "/w", entries);
  assert.deepStrictEqual(m.shown(), ["/w/b", "/w/a-lnk", "/w/c.txt", "/w/f-lnk"], "the phone keeps the server's order");
  assert.deepStrictEqual(m.run('ftTreeEl.querySelectorAll(".ft-node").map((r) => r.getAttribute("aria-expanded"))'), ["false", null, null, null]);
  assert.deepStrictEqual(m.run('ftTreeEl.querySelectorAll(".ft-link").length'), 0);
});

// Review round 1 (Important 1): every manual reveal that re-roots, opens a collapsed tree or unfolds the column also starts
// the dock's automatic reveal 150 ms later; it must never cancel the manual one still opening folders.
test("review 1: an automatic reveal never supersedes a manual one in progress", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a"), dir("c")]);
  const manual = p.run('ftReveal("/w/a/b/f.go")');
  await settle();
  await p.answer("/w/a", [dir("b")]);
  const auto = p.run('ftReveal("/w/c/x.go", { auto: true })');
  await settle();
  await p.answer("/w/a/b", [file("f.go")]);
  assert.strictEqual(await manual, true, "the manual reveal finishes (no false error toast)");
  assert.strictEqual(await auto, false);
  assert.deepStrictEqual(p.selected(), ["/w/a/b/f.go"]);
  assert.strictEqual(p.run("ftRowPaths.get(document.activeElement)"), "/w/a/b/f.go", "focus reaches the row");
});

// Review round 1 (Minor): an automatic reveal for a tab the user has already left stops before it selects or scrolls.
test("review 1: an automatic reveal stops when opts.still() says the file is no longer the open one", async () => {
  const p = tree();
  await rooted(p, "/w", [dir("a")]);
  p.run("globalThis.wantA = true;");
  const done = p.run('ftReveal("/w/a/x.go", { auto: true, still: () => globalThis.wantA })');
  await settle();
  p.run("globalThis.wantA = false;");
  await p.answer("/w/a", [file("x.go")]);
  assert.strictEqual(await done, false);
  assert.deepStrictEqual(p.selected(), []);
  assert.deepStrictEqual(p.st.scrolls, []);
});

// Review round 1 (Important 2): the phone's Reveal in tree is what it was on main: a file outside the root roots the tree at
// the file's folder with no follow key (focus is followed again at the next change), and the folders it opens are kept.
test("review 1: the phone's Reveal is main's: folder root, no sticky key, no /api/fs/where, opened folders saved", async () => {
  const p = tree(null, { docked: false, where: { "/v/repo/sub/f.go": { path: "/v/repo/sub/f.go", top: "/v/repo" } } });
  p.run('syncTreeToRoot("/w", "sess:1");');
  await settle();
  await p.answer("/w", [dir("a")]);
  const out = p.run('ftReveal("/v/repo/sub/f.go")');
  await settle();
  await p.answer("/v/repo/sub", [file("f.go")]);
  assert.strictEqual(await out, true);
  assert.strictEqual(p.run("ftRoot()"), "/v/repo/sub");
  assert.strictEqual(p.run("lastSyncedKey"), null);
  assert.deepStrictEqual(p.st.wheres, []);
  const q = tree(null, { docked: false });
  await rooted(q, "/w", [dir("a")]);
  const r = q.run('ftReveal("/w/a/f.go")');
  await settle();
  await q.answer("/w/a", [file("f.go")]);
  assert.strictEqual(await r, true);
  assert.deepStrictEqual(q.st.saves[q.st.saves.length - 1], ["/w/a"], "the phone keeps what Reveal opened, as on main");
  assert.deepStrictEqual(q.st.opens, [], "and never touches the desktop's sections");
});

// Review round 1 (Minor): a keyless syncTreeToRoot (an explicit Open files here) forgets the key seen before it, so a sticky
// re-root after it does not store a stale key.
test("review 1: the follow key seen is cleared by a keyless re-root", async () => {
  const p = tree();
  p.run('syncTreeToRoot("/w", "sess:old");');
  await settle();
  await p.answer("/w", []);
  p.run('syncTreeToRoot("/v");');
  await settle();
  await p.answer("/v", []);
  assert.strictEqual(p.run("ftFollowKeySeen"), null);
});

