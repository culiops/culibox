// The explorer's Changes section as dockchanges.js runs it, in a vm: the real dockchanges.js, changesmodel.js, gitstatus.js
// (its gitStatusLoad is what calls the section) and drawerfiles.js, against a fake page and a fake fetch. The model is
// changesmodel.test.js's and the markup and stylesheet are dock-changes-wire.test.js's; this file proves the glue obeys them:
// what is asked for and when (one numstat at a time, the base only when the root or HEAD moves, nothing while the dock is
// closed or the page hidden), what reaches the page and how (textContent), and what a click does. The visual check proves
// the look.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const KEY = "agent-box:changesScope";
const settle = async () => { for (let i = 0; i < 8; i++) await new Promise((r) => setImmediate(r)); };

function page(over) {
  const doc = { activeElement: null, hidden: false, listeners: {} };
  doc.createElement = (t) => new Node(t, doc);
  doc.addEventListener = (ev, fn) => { (doc.listeners[ev] = doc.listeners[ev] || []).push(fn); };
  const nodes = {};
  const node = (id, tag) => (nodes[id] = Object.assign(new Node(tag || "div", doc), { id }));
  for (const id of [
    "drawer", "drawer-tree", "dock-changes", "dock-chg-count", "dock-review", "dock-branch", "dock-chg-note", "dock-chg-list",
    "drawer-strip-count", "drawer-strip-changes", "dock-root", "dock-root-name", "dock-root-path", "dock-newfile", "dock-more",
    "drawer-goto-kbd", "ft-root-input", "ft-upload", "ft-menu",
  ]) node(id);
  nodes["dock-changes"].hidden = true;
  nodes["ft-root-input"].addEventListener = (ev, fn) => { (nodes["ft-root-input"].handlers = nodes["ft-root-input"].handlers || {})[ev] = fn; };
  nodes["drawer"].hidden = false;
  const scope = node("dock-scope");
  for (const s of ["head", "branch"]) {
    const b = new Node("button", doc);
    b.dataset.scope = s;
    b.setAttribute("aria-pressed", s === "head" ? "true" : "false");
    scope.append(b);
  }
  const legend = node("dock-chg-legend");
  legend.append(new Node("span", doc));
  doc.getElementById = (id) => nodes[id] || null;

  const st = { fetches: [], store: new Map(), shown: [], reveals: [], toasts: [], menu: null, closed: 0, owns: true, root: "/w", sid: "s1", loads: 0, newfile: [], mkdir: [] };
  const fetch = (url, init) => new Promise((resolve, reject) => {
    st.fetches.push({ url, body: JSON.parse(init.body), resolve, reject, signal: init.signal });
  });
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, AbortController,
    document: doc,
    fetch,
    localStorage: {
      getItem: (k) => (st.store.has(k) ? st.store.get(k) : null),
      setItem: (k, v) => { st.store.set(k, v); },
    },
    setTimeout: () => 1,
    clearTimeout: () => {},
    shortPath: (p) => p.replace(/^\/home\/u/, "~"),
    iconFor: (name, isDir) => (isDir ? "D" : "F"),
    ftRoot: () => st.root,
    ftReveal: async (p) => { st.reveals.push(p); return true; },
    ftLoadRoot: (o) => { st.loads++; st.lastLoad = o; },
    ftShowMenu: (anchor, items, left, label) => { st.menu = { anchor, items, label }; },
    ftRootIsReadOnly: () => !!st.ro,
    closeRowMenu: () => { st.closed++; },
    kitMenuButton: (label, icon, danger, run) => ({ label, icon, run, tag: "item", classList: {}, focus() {} }),
    kitMenuSep: () => ({ tag: "sep" }),
    promptMkdir: (d) => { st.mkdir.push(d); },
    promptNewFile: (d) => { st.newfile.push(d); },
    forgetSubtree: (set, p) => { for (const x of [...set]) if (x === p || x.startsWith(p + "/")) set.delete(x); },
    saveExpanded: (set) => { st.saved = [...set]; },
    showToast: (m) => { st.toasts.push(m); },
    shortcutHint: () => "Ctrl+P",
    IS_MAC: false,
    drawerOwnsFileClicks: () => st.owns,
    drawerSessionName: (id) => ({ s1: "fix-fs-cap" }[id] || ""),
    drawerShowFile: (p, o) => { st.shown.push([p, o]); return true; },
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load('let drawerState = { open: "files", sessionId: "s1", pinned: false }; let drawerChangedFiles = new Map(); let ftExpanded = new Set(["/w/a", "/w/a/b", "/other/x"]); let ftRootSel = null;', "state");
  for (const f of ["drawerfiles.js", "changesmodel.js", "gitstatus.js", "dockchanges.js"]) load(src(f), f);
  const p = {
    ctx, st, doc, nodes,
    el: (id) => nodes[id],
    run: (code) => load(code, "test"),
    // The status lands the way the tree's own load does it: gitStatusLoad asks, the fake answers, the section hears of it.
    status: async (data, o) => {
      const before = st.fetches.length;
      const done = ctx.gitStatusLoad(st.root, st.sid, o);
      st.fetches[before].resolve({ ok: true, json: async () => data });
      await done;
      await settle();
    },
    // The pending requests to `url`, oldest first.
    pending: (url) => st.fetches.filter((f) => f.url === url && !f.done),
    answer: async (url, data, ok) => {
      const f = st.fetches.find((x) => x.url === url && !x.done);
      assert.ok(f, "a pending " + url);
      f.done = true;
      f.resolve(ok === false ? { ok: false, status: 500, json: async () => ({}) } : { ok: true, json: async () => data });
      await settle();
      return f;
    },
    calls: (url) => st.fetches.filter((f) => f.url === url),
    rows: () => nodes["dock-chg-list"].children,
    prefs: () => st.store.get(KEY),
  };
  return p;
}

const okStatus = (entries, extra) => Object.assign({ state: "ok", top: "/w", branch: "feat/x", head: "h1", rootRel: ".", entries }, extra || {});
const num = (entries, extra) => Object.assign({ state: "ok", rootRel: ".", entries }, extra || {});
const baseOk = (extra) => Object.assign({ state: "ok", branch: "feat/x", baseRef: "refs/heads/main", baseName: "main", mergeBase: "abc", ahead: false }, extra || {});
// Arrays and objects made inside the vm have its prototypes: compare them as plain data.
const plain = (x) => JSON.parse(JSON.stringify(x === undefined ? null : x));
const text = (n) => n.textContent;
const cls = (n) => n.className.split(/\s+/);

test("a status that lands asks for the base (the path alone) and the numstat, once each, and the rows show counts when they arrive", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "U" }]));
  assert.deepStrictEqual(p.calls("/api/git/base").map((f) => f.body), [{ path: "/w" }], "the base body names no ref");
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "head" }]);
  // Before the answers: the section is there with the status rows and no counts.
  assert.strictEqual(p.el("dock-changes").hidden, false);
  assert.deepStrictEqual(p.rows().map((r) => text(r.children[1])), ["a.go", "b.go"]);
  assert.strictEqual(p.rows()[0].querySelector(".dk-ct"), null, "no count is a guess");
  assert.strictEqual(text(p.el("dock-chg-count")), "2 changed");
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 3, d: 1 }, { p: "b.go", x: "U", a: 5, d: 0 }]));
  assert.strictEqual(text(p.rows()[0].querySelector(".dk-ct")), "+3−1");
  assert.strictEqual(text(p.rows()[1].querySelector(".dk-ct")), "+5−0");
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-st"))), ["M", "U"]);
  assert.strictEqual(p.calls("/api/git/numstat").length, 1, "an answer that leaves nothing stale asks for nothing more");
  assert.strictEqual(p.calls("/api/git/base").length, 1);
});

test("a status that was read fresh asks for a fresh numstat; a plain one does not", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]), { fresh: true });
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "head", fresh: true }]);
  await p.answer("/api/git/numstat", num([]));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "head", fresh: true }, { path: "/w", scope: "head" }]);
});

test("at most one numstat is out at a time: statuses that land meanwhile are folded into one request afterwards", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.pending("/api/git/numstat").length, 1);
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "c.go", x: "M" }]));
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "c.go", x: "M" }, { p: "d.go", x: "M" }], { head: "h1" }));
  assert.strictEqual(p.calls("/api/git/numstat").length, 1, "the first is still out: no second request");
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }]));
  assert.strictEqual(p.calls("/api/git/numstat").length, 2, "one follow-up for the newest status, not one per status");
  assert.strictEqual(p.pending("/api/git/numstat").length, 1);
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }, { p: "c.go", x: "M", a: 2, d: 0 }, { p: "d.go", x: "M", a: 3, d: 0 }]));
  assert.strictEqual(p.calls("/api/git/numstat").length, 2);
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-ct"))), ["+1−1", "+2−0", "+3−0"]);
});

test("the base is asked for when the root or HEAD moves, and not again for the same", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([]));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/numstat", num([]));
  assert.strictEqual(p.calls("/api/git/base").length, 1, "same root, same HEAD");
  await p.status(okStatus([{ p: "a.go", x: "M" }], { head: "h2" }));
  assert.strictEqual(p.calls("/api/git/base").length, 2, "a commit moved HEAD");
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  await p.answer("/api/git/numstat", num([]));
  p.st.root = "/w2";
  await p.status(okStatus([{ p: "a.go", x: "M" }], { head: "h2" }));
  assert.strictEqual(p.calls("/api/git/base").length, 3, "another root");
  assert.deepStrictEqual(p.calls("/api/git/base")[2].body, { path: "/w2" });
});

test("a base that failed is asked for again only after a while, so the toggle comes back without a request per status", async () => {
  const p = page();
  const realNow = Date.now;
  let now = 1e12;
  p.ctx.Date = { now: () => now };
  try {
    await p.status(okStatus([{ p: "a.go", x: "M" }]));
    await p.answer("/api/git/base", null, false);
    await p.answer("/api/git/numstat", num([]));
    assert.strictEqual(p.el("dock-scope").hidden, true, "no base: no toggle");
    await p.status(okStatus([{ p: "a.go", x: "M" }]));
    await p.answer("/api/git/numstat", num([]));
    assert.strictEqual(p.calls("/api/git/base").length, 1, "not at once");
    now += 31000;
    await p.status(okStatus([{ p: "a.go", x: "M" }]));
    assert.strictEqual(p.calls("/api/git/base").length, 2, "after 30 s");
  } finally { p.ctx.Date = Object; Date.now = realNow; }
});

test("nothing is asked while the dock is closed, has no workspace to show or the page is hidden; showing it again asks", async () => {
  const p = page();
  p.st.owns = false; // the dock is not what shows the tree
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.calls("/api/git/numstat").length + p.calls("/api/git/base").length, 0, "closed");
  p.st.owns = true;
  p.el("drawer").hidden = true;
  p.ctx.dockChangesRefresh();
  assert.strictEqual(p.calls("/api/git/numstat").length, 0, "the dock element is hidden");
  p.el("drawer").hidden = false;
  p.el("drawer").dataset.noroot = "1";
  p.ctx.dockChangesRefresh();
  assert.strictEqual(p.calls("/api/git/numstat").length, 0, "a workspace the fs API refuses");
  p.el("drawer").dataset.noroot = "";
  p.doc.hidden = true;
  p.ctx.dockChangesRefresh();
  assert.strictEqual(p.calls("/api/git/numstat").length, 0, "the page is hidden");
  p.doc.hidden = false;
  for (const fn of p.doc.listeners.visibilitychange) fn();
  assert.strictEqual(p.calls("/api/git/numstat").length, 1, "back: it asks for what it missed");
  assert.strictEqual(p.calls("/api/git/base").length, 1);
});

test("a status that is unavailable is one calm note with the reason on hover, and nothing more is asked", async () => {
  const p = page();
  await p.status({ state: "unavailable", reason: "Git status needs the box runtime (docker)", rootRel: "", entries: [] });
  assert.strictEqual(p.el("dock-changes").hidden, false);
  const note = p.el("dock-chg-note");
  assert.strictEqual(note.hidden, false);
  assert.strictEqual(text(note), "Status unavailable");
  assert.strictEqual(note.title, "Git status needs the box runtime (docker)");
  assert.strictEqual(p.rows().length, 0);
  assert.strictEqual(p.el("dock-review").hidden, true);
  assert.strictEqual(p.el("dock-scope").hidden, true);
  assert.strictEqual(text(p.el("dock-chg-count")), "");
  assert.strictEqual(p.el("drawer-strip-count").hidden, true, "the strip says nothing either");
  assert.strictEqual(p.calls("/api/git/numstat").length + p.calls("/api/git/base").length, 0, "no request while status is unavailable");
});

test("a clean workspace says so, with no count in the header", async () => {
  const p = page();
  await p.status(okStatus([]));
  assert.strictEqual(p.el("dock-changes").hidden, false);
  assert.strictEqual(p.el("dock-chg-note").hidden, false);
  assert.strictEqual(text(p.el("dock-chg-note")), "No changes");
  assert.strictEqual(text(p.el("dock-chg-count")), "");
  assert.strictEqual(p.el("dock-review").hidden, true);
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.el("dock-chg-note").hidden, true, "gone as soon as there is a list");
});

test("a folder that is not a repository shows no section at all", async () => {
  const p = page();
  await p.status(okStatus([]));
  assert.strictEqual(p.el("dock-changes").hidden, false);
  await p.status({ state: "notrepo", rootRel: "", entries: [] });
  assert.strictEqual(p.el("dock-changes").hidden, true);
});

test("the section is gone while the status held is for another root, and back when the new root's lands", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.el("dock-changes").hidden, false);
  p.st.root = "/w2";
  p.ctx.dockChangesRender();
  assert.strictEqual(p.el("dock-changes").hidden, true, "the old root's rows are not shown under the new root");
  assert.strictEqual(text(p.el("dock-root-name")), "w2");
});

test("every path, name and branch reaches the page as text: markup in a name stays text, format characters are kept", async () => {
  const p = page();
  const evil = "<img src=x onerror=alert(1)>.go";
  const cf = "feat/\u202eevil\u200b"; // a right-to-left override and a zero-width space
  const baseName = "origin/\u2066main";
  await p.status(okStatus([{ p: "dir <b>/" + evil, x: "M" }]));
  // HEAD is ahead, so Branch is the default scope: its own numstat is asked for once the Uncommitted one is back.
  await p.answer("/api/git/base", baseOk({ ahead: true, branch: cf, baseName }));
  await p.answer("/api/git/numstat", num([{ p: "dir <b>/" + evil, x: "M", a: 1, d: 1 }]));
  await p.answer("/api/git/numstat", num([{ p: "dir <b>/" + evil, x: "M", a: 1, d: 1 }]));
  const row = p.rows()[0];
  assert.strictEqual(text(row.querySelector(".dk-nm")), evil);
  assert.strictEqual(text(row.querySelector(".dk-dir")), "dir <b>");
  assert.strictEqual(row.querySelector("img"), null, "nothing was parsed as markup");
  const names = Array.from(p.el("dock-branch").querySelectorAll(".dk-br-name"), text);
  assert.deepStrictEqual(names, [cf, baseName], "the branch and the base are verbatim text, each in its own span");
  assert.strictEqual(p.el("dock-branch").title, cf + " → " + baseName);
  assert.ok(row.title.includes(evil), "the hover text is the path");
});

test("Branch: the toggle shows with an ok base, defaults to Branch when ahead, and the branch row reads branch → base · +a −d", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }]));
  assert.strictEqual(p.el("dock-scope").hidden, false);
  const [head, branch] = p.el("dock-scope").children;
  assert.deepStrictEqual([head.getAttribute("aria-pressed"), branch.getAttribute("aria-pressed")], ["false", "true"], "ahead: Branch is on");
  assert.strictEqual(p.calls("/api/git/numstat")[1].body.scope, "branch");
  assert.strictEqual(text(p.el("dock-chg-note")), "Reading branch changes…", "the list waits for its own numstat; no spinner");
  assert.strictEqual(p.el("dock-scope").hidden, false, "and the toggle is still there");
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "A", a: 10, d: 0 }, { p: "b.go", x: "M", a: 3, d: 1 }]));
  const row = p.el("dock-branch");
  assert.strictEqual(row.hidden, false);
  assert.strictEqual(text(row), "feat/x→main·+13−1");
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-st"))), ["A", "M"]);
  assert.strictEqual(text(p.el("dock-chg-count")), "2 changed");
  assert.strictEqual(p.el("drawer-strip-count").hidden, false);
  assert.strictEqual(text(p.el("drawer-strip-count")), "2", "the strip's count follows the scope too");
});

test("a detached HEAD is named so, and the toggle is hidden without a base", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }], { branch: "" }));
  await p.answer("/api/git/base", { state: "nobase", reason: "no base branch found", branch: "", ahead: false });
  await p.answer("/api/git/numstat", num([]));
  assert.strictEqual(p.el("dock-scope").hidden, true);
  assert.strictEqual(p.el("dock-branch").hidden, true);
  await p.status(okStatus([{ p: "a.go", x: "M" }], { branch: "", head: "h2" }));
  await p.answer("/api/git/base", baseOk({ ahead: true, branch: "" }));
  await p.answer("/api/git/numstat", num([]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 0 }]));
  assert.strictEqual(text(p.el("dock-branch").querySelector(".dk-br-name")), "(detached)");
});

test("pressing a scope remembers it for the session and asks for that scope's list; the pick wins over the default", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  await p.answer("/api/git/numstat", num([]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }])); // Branch, by default
  p.el("dock-scope").children[0].click(); // Uncommitted
  assert.deepStrictEqual(JSON.parse(p.prefs()), { s1: "head" });
  assert.strictEqual(p.ctx.dockScope("s1"), "head");
  assert.strictEqual(p.calls("/api/git/numstat")[2].body.scope, "head");
  assert.deepStrictEqual(p.el("dock-scope").children.map((b) => b.getAttribute("aria-pressed")), ["true", "false"]);
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 2, d: 2 }]));
  // A newer status after the pick stays on Uncommitted although HEAD is ahead.
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.calls("/api/git/numstat")[3].body.scope, "head");
  assert.strictEqual(p.ctx.dockScope("other"), "branch", "another session still has the default");
  assert.strictEqual(p.ctx.gitBaseFor("/w").state, "ok");
  assert.strictEqual(p.ctx.gitBaseFor("/elsewhere"), null);
});

test("a remembered scope from an earlier visit is read from storage; one that is not a scope is ignored", async () => {
  const p = page();
  p.st.store.set(KEY, JSON.stringify({ s1: "branch" }));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: false }));
  assert.strictEqual(p.ctx.dockScope("s1"), "branch");
  const q = page();
  q.st.store.set(KEY, "{not json");
  await q.status(okStatus([{ p: "a.go", x: "M" }]));
  await q.answer("/api/git/base", baseOk({ ahead: false }));
  assert.strictEqual(q.ctx.dockScope("s1"), "head");
});

test("a numstat that fails or never lands does not stall the section: it keeps the status rows, and a later status asks again", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", null, false); // an error status
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-nm"))), ["a.go"]);
  assert.strictEqual(p.pending("/api/git/numstat").length, 0, "no retry loop");
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.pending("/api/git/numstat").length, 1, "the next status asks again");
  // The 20 s timer aborts the request, which rejects the fetch: the same as a failure, and the slot is free again.
  const f = p.pending("/api/git/numstat")[0];
  f.done = true;
  f.reject(new Error("aborted"));
  await settle();
  assert.strictEqual(p.pending("/api/git/numstat").length, 0);
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-nm"))), ["a.go"]);
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  assert.strictEqual(p.pending("/api/git/numstat").length, 1, "and the one after that goes out");
});

test("a row of this session wears the bar and says so in words; the legend names the session", async () => {
  const p = page();
  p.run('drawerChangedFiles.set("s1", [{ path: "/w/b.go", add: 1, del: 1 }])');
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  p.ctx.dockChangesRender();
  const [first, second] = p.rows();
  assert.ok(cls(first).includes("dk-mine") && text(first.querySelector(".dk-nm")) === "b.go", "mine first");
  assert.ok(!cls(second).includes("dk-mine"));
  assert.ok(first.getAttribute("aria-label").includes("changed by fix-fs-cap"));
  assert.ok(!second.getAttribute("aria-label").includes("changed by"));
  const legend = p.el("dock-chg-legend");
  assert.strictEqual(legend.hidden, false);
  assert.strictEqual(text(legend.children[0]), "changed by fix-fs-cap");
  p.run('drawerChangedFiles.delete("s1")');
  p.ctx.dockChangesRender();
  assert.strictEqual(p.el("dock-chg-legend").hidden, true, "no row of the session, no legend");
});

test("the list is capped with an 'and N more' row, and the count is the whole number", async () => {
  const p = page();
  const entries = [];
  for (let i = 0; i < 230; i++) entries.push({ p: "f" + String(i).padStart(3, "0") + ".go", x: "M" });
  await p.status(okStatus(entries));
  assert.strictEqual(p.rows().length, 201);
  assert.strictEqual(text(p.rows()[200]), "and 30 more");
  assert.strictEqual(text(p.el("dock-chg-count")), "230 changed");
  assert.strictEqual(text(p.el("drawer-strip-count")), "99+");
});

test("a row's click shows the file as a preview; a deleted file opens its diff; a folder is shown in the tree; Review pins the first file's Changes", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "gone.go", x: "D" }, { p: "newdir/", x: "U" }]));
  const by = Object.fromEntries(p.rows().map((r) => [text(r.querySelector(".dk-nm")), r]));
  by["a.go"].click();
  by["gone.go"].click();
  by["newdir"].click();
  assert.deepStrictEqual(plain(p.st.shown), [["/w/a.go", null], ["/w/gone.go", { view: "changes" }]]);
  assert.deepStrictEqual(p.st.reveals, ["/w/newdir"]);
  p.st.shown.length = 0;
  p.el("dock-review").click();
  assert.deepStrictEqual(plain(p.st.shown), [["/w/a.go", { view: "changes", preview: false }]], "the first file, not the folder");
  assert.strictEqual(p.el("dock-review").hidden, false);
  const onlyDirs = page();
  await onlyDirs.status(okStatus([{ p: "newdir/", x: "U" }]));
  assert.strictEqual(onlyDirs.el("dock-review").hidden, true, "nothing to review but a folder");
});

test("the root header is the root's name and its path, and a click shows the root field until the root changes or Escape", async () => {
  const p = page();
  p.st.root = "/home/u/src/culibox";
  p.ctx.dockChangesRender();
  assert.strictEqual(text(p.el("dock-root-name")), "culibox");
  assert.strictEqual(text(p.el("dock-root-path")), "~/src/culibox");
  const focused = [];
  p.run("ftRootSel = { focus() { __f.push('focus'); }, select() { __f.push('select'); } }".replace("__f", "globalThis.__f"));
  p.ctx.__f = focused;
  p.el("dock-root").click();
  assert.strictEqual(p.el("drawer-tree").dataset.rootedit, "1");
  assert.strictEqual(p.el("dock-root").getAttribute("aria-expanded"), "true");
  assert.deepStrictEqual(focused, ["focus", "select"], "the field takes the caret");
  p.el("dock-root").click();
  assert.strictEqual(p.el("drawer-tree").dataset.rootedit, undefined, "a second click puts it away");
  p.el("dock-root").click();
  p.st.root = "/home/u/src/other"; // a pick re-roots the tree
  p.ctx.dockChangesRender();
  assert.strictEqual(p.el("drawer-tree").dataset.rootedit, undefined, "a new root ends the edit");
  assert.strictEqual(text(p.el("dock-root-name")), "other");
  const root = page();
  root.st.root = "/";
  root.ctx.dockChangesRender();
  assert.strictEqual(text(root.el("dock-root-name")), "/");
});

test("a box session's root cannot be edited", async () => {
  const p = page();
  p.el("drawer").dataset.box = "1";
  p.ctx.dockChangesRender();
  assert.strictEqual(p.el("dock-root").getAttribute("aria-disabled"), "true");
  assert.strictEqual(p.el("dock-root").getAttribute("aria-expanded"), null);
  p.el("dock-root").click();
  assert.strictEqual(p.el("drawer-tree").dataset.rootedit, undefined);
});

test("the strip's Changes button carries the count in its name", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "c.go", x: "M" }]));
  assert.strictEqual(text(p.el("drawer-strip-count")), "3");
  assert.strictEqual(p.el("drawer-strip-changes").getAttribute("aria-label"), "Open the explorer at Changes, 3 changed");
  await p.status(okStatus([]));
  assert.strictEqual(p.el("drawer-strip-count").hidden, true);
  assert.strictEqual(p.el("drawer-strip-changes").getAttribute("aria-label"), "Open the explorer at Changes");
});

test("the top row: New file goes to the root; the ⋯ menu lists New folder, Upload, Refresh and Collapse all", async () => {
  const p = page();
  p.el("dock-newfile").click();
  assert.deepStrictEqual(p.st.newfile, ["/w"]);
  assert.strictEqual(text(p.el("drawer-goto-kbd")), "Ctrl+P", "the chord is the registry's");
  let stopped = 0;
  p.el("dock-more").onclick({ stopPropagation() { stopped++; } });
  assert.strictEqual(stopped, 1, "the document's menu closer must not see this press");
  assert.strictEqual(p.st.menu.anchor, p.el("dock-more"));
  assert.strictEqual(p.st.menu.label, "Explorer actions");
  assert.deepStrictEqual(Array.from(p.st.menu.items, (i) => i.label || i.tag), ["New folder…", "Upload…", "sep", "Refresh", "Collapse all"]);
  const item = (l) => p.st.menu.items.find((i) => i.label === l);
  item("New folder…").run();
  assert.deepStrictEqual(p.st.mkdir, ["/w"]);
  assert.strictEqual(p.st.closed, 1, "closes first, then acts");
  let uploaded = 0;
  p.el("ft-upload").onclick = () => { uploaded++; };
  item("Upload…").run();
  assert.strictEqual(uploaded, 1, "the existing upload button's wiring");
  item("Refresh").run();
  assert.deepStrictEqual(plain([p.st.loads, p.st.lastLoad]), [1, { fresh: true }]);
});

test("Collapse all forgets every folder opened under the root, keeps others, and reloads", async () => {
  const p = page();
  p.el("dock-more").onclick({ stopPropagation() {} });
  p.st.menu.items.find((i) => i.label === "Collapse all").run();
  assert.deepStrictEqual(p.st.saved, ["/other/x"]);
  assert.strictEqual(p.st.loads, 1);
});

test("a read-only folder's ⋯ menu offers only what reads", async () => {
  const p = page();
  p.st.ro = true;
  p.el("dock-more").onclick({ stopPropagation() {} });
  assert.deepStrictEqual(Array.from(p.st.menu.items, (i) => i.label), ["Refresh", "Collapse all"]);
});
