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

  const st = { session: new Map((over && over.__session) || []), reviewOpened: [], scrolled: [], reviewOn: false, reviewHas: new Set(), fetches: [], store: new Map(), shown: [], reveals: [], toasts: [], menu: null, closed: 0, owns: true, root: "/w", sid: "s1", loads: 0, newfile: [], mkdir: [] };
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
    sessionStorage: {
      getItem: (k) => (st.session.has(k) ? st.session.get(k) : null),
      setItem: (k, v) => { st.session.set(k, v); },
    },
    setTimeout: () => 1,
    clearTimeout: () => {},
    shortPath: (p) => p.replace(/^\/home\/u/, "~"),
    iconFor: (name, isDir) => (isDir ? "D" : "F"),
    ftRoot: () => st.root,
    ftReveal: async (p) => { st.reveals.push(p); return true; },
    ftLoadRoot: (o) => { st.loads++; st.lastLoad = o; },
    ftShowMenu: (anchor, items, left, label) => { st.menu = { anchor, items, label }; st.shows = (st.shows || 0) + 1; anchor.setAttribute("aria-expanded", "true"); },
    ftRootIsReadOnly: () => !!st.ro,
    closeRowMenu: () => { st.closed++; nodes["dock-more"].setAttribute("aria-expanded", "false"); },
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
    // The All changes tab (reviewall.js): the tab opens through reviewAllOpen; while it is on screen (st.reviewOn) a row's click is its scrollTo.
    reviewAllOpen: (sid, path) => { st.reviewOpened.push([sid, path]); return true; },
    reviewAllScrollTo: (path) => { if (!st.reviewOn || !st.reviewHas.has(path)) return false; st.scrolled.push(path); return true; },
  }, over));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load('let drawerState = { open: "files", sessionId: "s1", pinned: false }; let drawerChangedFiles = new Map(); let ftExpanded = new Set(["/w/a", "/w/a/b", "/other/x"]); let ftRootSel = null;', "state");
  for (const f of ["drawerfiles.js", "changesmodel.js", "gitlast.js", "gitstatus.js", "reviewmodel.js", "dockchanges.js"]) load(src(f), f);
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
// The server's base answer names the HEAD it is for (gitview BaseInfo.Head): h1, the head of okStatus.
const baseOk = (extra) => Object.assign({ state: "ok", branch: "feat/x", head: "h1", baseRef: "refs/heads/main", baseName: "main", mergeBase: "abc", ahead: false }, extra || {});
// Arrays and objects made inside the vm have its prototypes: compare them as plain data.
const plain = (x) => JSON.parse(JSON.stringify(x === undefined ? null : x));
const text = (n) => n.textContent;
const cls = (n) => n.className.split(/\s+/);

test("a status that lands asks for the base (the path alone) and the numstat, once each, and the rows show counts when they arrive", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "U" }]));
  assert.deepStrictEqual(p.calls("/api/git/base").map((f) => f.body), [{ path: "/w" }], "the base body names no ref");
  // Asked beside the status, before any base is known: the server picks the scope ("auto").
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "auto" }]);
  // Before the answers: the section is there with the status rows and no counts.
  assert.strictEqual(p.el("dock-changes").hidden, false);
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-nm"))), ["a.go", "b.go"]);
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
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "auto", fresh: true }]);
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([]));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "auto", fresh: true }, { path: "/w", scope: "head" }]);
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
  // The first ask could not know: its numstat and base went out beside it. Once a status said unavailable, nothing more.
  const n = p.calls("/api/git/numstat").length + p.calls("/api/git/base").length;
  assert.ok(n <= 2, n + " requests beside the first status");
  await p.status({ state: "unavailable", reason: "Git status needs the box runtime (docker)", rootRel: "", entries: [] });
  assert.strictEqual(p.calls("/api/git/numstat").length + p.calls("/api/git/base").length, n, "no request while status is unavailable");
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

test("a row's click shows the file as a preview; a deleted file opens its diff; a folder is shown in the tree; Review opens the All changes tab at the first file", async () => {
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
  assert.deepStrictEqual(plain(p.st.shown), [], "Review no longer opens one file");
  assert.deepStrictEqual(plain(p.st.reviewOpened), [["s1", "/w/a.go"]], "the tab, at the first file (not the folder)");
  assert.strictEqual(p.el("dock-review").hidden, false);
  const onlyDirs = page();
  await onlyDirs.status(okStatus([{ p: "newdir/", x: "U" }]));
  assert.strictEqual(onlyDirs.el("dock-review").hidden, true, "nothing to review but a folder");
});

test("while the All changes tab is on screen a row's click scrolls to its section and opens no editor; a row with no section opens as usual", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "newdir/", x: "U" }]));
  const by = Object.fromEntries(p.rows().map((r) => [text(r.querySelector(".dk-nm")), r]));
  // The tab is not on screen: the click opens the file, as before.
  by["a.go"].click();
  assert.deepStrictEqual(plain(p.st.shown), [["/w/a.go", null]]);
  assert.deepStrictEqual(p.st.scrolled, []);
  p.st.shown.length = 0;
  // The tab is on screen, with sections for a.go and b.go only (the cap, say).
  p.st.reviewOn = true;
  p.st.reviewHas = new Set(["/w/a.go", "/w/b.go"]);
  by["a.go"].click();
  by["b.go"].click();
  assert.deepStrictEqual(p.st.scrolled, ["/w/a.go", "/w/b.go"]);
  assert.deepStrictEqual(plain(p.st.shown), [], "no editor tab was opened");
  p.st.reviewHas = new Set(["/w/b.go"]);
  by["a.go"].click();
  assert.deepStrictEqual(plain(p.st.shown), [["/w/a.go", null]], "a file with no section opens as usual");
  by["newdir"].click();
  assert.deepStrictEqual(p.st.reveals, ["/w/newdir"], "a folder is still the tree's");
  assert.deepStrictEqual(p.st.scrolled, ["/w/a.go", "/w/b.go"]);
});

// The review loop's marks on the section (P3 Task 3). The review store and the editor are other scripts, so each test stands in for the
// three things dockchanges.js asks of them: reviewMarksOf(sid) (a NEW object on every change), editorDiffAnyChanges() and notesFor(sid).
const reviewStubs = (p) => p.run(`globalThis.__marks = {}; globalThis.__rev = false; globalThis.__notes = null;
  globalThis.reviewMarksOf = () => globalThis.__marks;
  globalThis.editorDiffAnyChanges = () => globalThis.__rev;
  globalThis.notesFor = () => globalThis.__notes;`);
const markFor = (p, path, row) => plain(p.ctx.reviewMarkFor(Object.assign({ path, code: "M", add: null, del: null }, row), [], 1000));
const done = (p) => p.rows().filter((r) => cls(r.querySelector(".dk-ck")).includes("dk-ck-done")).map((r) => text(r.querySelector(".dk-nm")));

test("the header reads 'reviewed n of m' while a tab is in Changes and 'N changed' otherwise; a row whose mark stands wears the tick", async () => {
  const p = page();
  reviewStubs(p);
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "newdir/", x: "U" }, { p: "n.txt", x: "U" }]));
  assert.strictEqual(text(p.el("dock-chg-count")), "4 changed", "no tab in Changes: the usual count");
  assert.deepStrictEqual(done(p), []);
  p.ctx.__rev = true; // a tab went to Changes: the dedupe sees `reviewing` move
  p.ctx.dockChangesRender();
  assert.strictEqual(text(p.el("dock-chg-count")), "reviewed 0 of 3", "the folder is not a file to review");
  // A mark is made: the store hands out a new object, and the section repaints from it.
  p.ctx.__marks = { "/w/a.go": markFor(p, "/w/a.go") };
  p.ctx.dockChangesRender();
  assert.strictEqual(text(p.el("dock-chg-count")), "reviewed 1 of 3");
  assert.deepStrictEqual(done(p), ["a.go"]);
  assert.match(p.rows()[0].getAttribute("aria-label"), /reviewed/, "the tick is in the row's words too");
  assert.strictEqual(cls(p.rows()[1].querySelector(".dk-ck")).includes("dk-ck-done"), false);
  // The agent edits a.go again (a new Activity list with a newer ts): the mark no longer stands, and the circle empties.
  p.run('drawerChangedFiles.set("s1", [{ path: "/w/a.go", ts: 50 }])');
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(done(p), []);
  assert.strictEqual(text(p.el("dock-chg-count")), "reviewed 0 of 3");
  // Git's count for the file moves (a fingerprint change): a mark made against the old one does not stand either.
  p.ctx.__marks = { "/w/b.go": markFor(p, "/w/b.go", { add: 9, del: 9 }) };
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(done(p), [], "b.go's mark was made for other numbers");
  p.ctx.__marks = { "/w/b.go": markFor(p, "/w/b.go") };
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(done(p), ["b.go"]);
  // Back to Code on every tab: the usual count again, the tick stays on the row.
  p.ctx.__rev = false;
  p.ctx.dockChangesRender();
  assert.strictEqual(text(p.el("dock-chg-count")), "4 changed");
  assert.deepStrictEqual(done(p), ["b.go"]);
});

test("a file's pending notes are a chip on its row, 'N notes', that goes when the notes do", async () => {
  const p = page();
  reviewStubs(p);
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  const chip = (i) => p.rows()[i].querySelector(".dk-nt");
  assert.strictEqual(chip(0), null, "no store, no notes, no chip");
  const note = (path, status) => ({ id: path + status, path, status });
  const a = p.rows()[0];
  p.ctx.__notes = [note("/w/a.go", "pending"), note("/w/a.go", "pending"), note("/w/b.go", "pending"), note("/w/b.go", "sent"), note("/other/x.go", "pending")];
  p.ctx.dockChangesRender();
  assert.strictEqual(text(chip(0)), "2 notes");
  assert.strictEqual(text(chip(1)), "1 note", "a sent note does not wait, and one is not 'notes'");
  assert.strictEqual(chip(0).dataset.n, "2", "the number alone, for a narrow list (desktop.css)");
  assert.strictEqual(chip(1).dataset.n, "1");
  assert.match(p.rows()[0].getAttribute("aria-label"), /2 notes/);
  assert.deepStrictEqual(Array.from(a.children, (c) => cls(c)[0]), ["dk-ck", "dk-ic", "dk-nm", "dk-dir", "dk-nt", "dk-st"], "the chip sits after the folder");
  // One is sent: the same list shape with a new content is a repaint (the dedupe reads the pending counts, not the list's identity).
  p.ctx.__notes = [note("/w/a.go", "pending"), note("/w/b.go", "sent")];
  p.ctx.dockChangesRender();
  assert.strictEqual(text(chip(0)), "1 note");
  assert.strictEqual(chip(1), null, "its chip is taken out");
  p.ctx.__notes = [];
  p.ctx.dockChangesRender();
  assert.strictEqual(chip(0), null);
  assert.strictEqual(p.rows()[0], a, "the row keeps its element throughout");
  // A store that throws is no notes, not a broken section.
  p.run("globalThis.notesFor = () => { throw new Error('boom'); }");
  p.ctx.__marks = {};
  p.ctx.dockChangesRender();
  assert.strictEqual(p.rows().length, 2);
});

test("Review opens the first file whose mark does not stand, and the first file when every one stands", async () => {
  const p = page();
  reviewStubs(p);
  await p.status(okStatus([{ p: "newdir/", x: "U" }, { p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  const review = () => { p.st.reviewOpened.length = 0; p.el("dock-review").click(); return plain(p.st.reviewOpened); };
  assert.deepStrictEqual(review(), [["s1", "/w/a.go"]], "nothing reviewed: the first file");
  p.ctx.__marks = { "/w/a.go": markFor(p, "/w/a.go") };
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(review(), [["s1", "/w/b.go"]], "a.go is reviewed: the second row opens");
  // The agent edits a.go: its mark stops standing, so it is the first unreviewed again.
  p.run('drawerChangedFiles.set("s1", [{ path: "/w/a.go", ts: 50 }])');
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(review(), [["s1", "/w/a.go"]]);
  p.ctx.__marks = { "/w/a.go": markFor(p, "/w/a.go"), "/w/b.go": markFor(p, "/w/b.go") };
  p.run('drawerChangedFiles.set("s1", [])');
  p.ctx.dockChangesRender();
  assert.deepStrictEqual(review(), [["s1", "/w/a.go"]], "all reviewed: Review still takes the reader to the first");
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

// --- the list is updated in place (fix round 1) ------------------------------------------------------------------------

// deepFreeze: every node of the page below `n`, and the arrays and objects that hold its state. The page scripts are strict,
// so the first write to a frozen node (a property, an attribute, a child) throws: "no DOM writes" is then a test that
// does not throw. The vm's unhandled rejections are collected by the caller.
function deepFreeze(n, seen = new Set()) {
  if (!n || typeof n !== "object" || seen.has(n)) return;
  seen.add(n);
  if (Array.isArray(n.children)) n.children.forEach((c) => deepFreeze(c, seen));
  for (const k of ["children", "attrs", "style", "dataset"]) if (n[k]) Object.freeze(n[k]);
  Object.freeze(n);
}

test("a row keeps its element, and the keyboard focus on it, across status and numstat landings", async () => {
  const p = page();
  const ent = (n) => Array.from({ length: n }, (_, i) => ({ p: "f" + i + ".go", x: "M" }));
  const cnt = (n, k) => num(Array.from({ length: n }, (_, i) => ({ p: "f" + i + ".go", x: "M", a: i + k, d: 0 })));
  await p.status(okStatus(ent(3)));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", cnt(3, 1));
  const [r0, r1, r2] = p.rows();
  r1.focus();
  assert.strictEqual(p.doc.activeElement, r1);
  // Two landings, as live status gives: a status (a file more, a new object) and then its numstat (new counts).
  await p.status(okStatus(ent(4)));
  assert.deepStrictEqual(p.rows().slice(0, 3), [r0, r1, r2], "the same elements, the new row after them");
  assert.strictEqual(p.rows().length, 4);
  assert.strictEqual(p.doc.activeElement, r1, "focus is still on the row after the status");
  await p.answer("/api/git/numstat", cnt(4, 10));
  assert.deepStrictEqual(p.rows().slice(0, 3), [r0, r1, r2]);
  assert.strictEqual(p.doc.activeElement, r1, "and after the numstat");
  assert.strictEqual(text(r1.querySelector(".dk-ct")), "+11−0", "the counts of the same element changed in place");
  assert.strictEqual(text(r1.querySelector(".dk-nm")), "f1.go");
  // A click on the row reaches what the row shows now.
  r1.click();
  assert.strictEqual(p.st.shown[p.st.shown.length - 1][0], "/w/f1.go");
});

test("rows are matched by path: a row that goes loses its element, one that moves is the same element in a new place", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "c.go", x: "M" }]));
  const [a, b, c] = p.rows();
  // c becomes the session's: it moves to the top. b is gone.
  p.run('drawerChangedFiles.set("s1", [{ path: "/w/c.go", add: 1, del: 0 }])');
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "c.go", x: "M" }]));
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-nm"))), ["c.go", "a.go"]);
  assert.strictEqual(p.rows()[0], c, "the moved row is the same element");
  assert.strictEqual(p.rows()[1], a);
  assert.ok(cls(c).includes("dk-mine"), "its class changed in place");
  assert.ok(!p.rows().includes(b));
  // b comes back as a new element, in order.
  p.run('drawerChangedFiles.delete("s1")');
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }, { p: "c.go", x: "M" }]));
  assert.deepStrictEqual(Array.from(p.rows(), (r) => text(r.querySelector(".dk-nm"))), ["a.go", "b.go", "c.go"]);
  assert.strictEqual(p.rows()[0], a);
  assert.strictEqual(p.rows()[2], c);
  assert.ok(!cls(c).includes("dk-mine"));
});

test("a count that goes away takes its element out, and comes back into the same row", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 2, d: 1 }]));
  const row = p.rows()[0];
  assert.strictEqual(text(row.querySelector(".dk-ct")), "+2−1");
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 0, d: 0, bin: true }])); // now binary: no count
  assert.strictEqual(p.rows()[0], row);
  assert.strictEqual(row.querySelector(".dk-ct"), null, "no count, no span");
  assert.deepStrictEqual(Array.from(row.children, (c) => cls(c)[0]), ["dk-ck", "dk-ic", "dk-nm", "dk-dir", "dk-st"]);
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 4, d: 4 }]));
  assert.strictEqual(text(row.querySelector(".dk-ct")), "+4−4");
  assert.deepStrictEqual(Array.from(row.children, (c) => cls(c)[0]), ["dk-ck", "dk-ic", "dk-nm", "dk-dir", "dk-ct", "dk-st"], "the counts sit before the letter");
});

test("a status and a numstat that say what the screen already says write nothing to the page", async () => {
  const rejected = [];
  const onRej = (e) => rejected.push(e);
  process.on("unhandledRejection", onRej);
  try {
    const p = page();
    const ent = [{ p: "a.go", x: "M" }, { p: "n.txt", x: "U" }, { p: "dir/", x: "U" }];
    const ns = [{ p: "a.go", x: "M", a: 3, d: 1 }, { p: "n.txt", x: "U", a: 5, d: 0 }];
    p.run('drawerChangedFiles.set("s1", [{ path: "/w/a.go", add: 1, del: 1 }])');
    await p.status(okStatus(ent));
    await p.answer("/api/git/base", baseOk({ ahead: true }));
    await p.answer("/api/git/numstat", num(ns));
    await p.answer("/api/git/numstat", num(ns, {}), true); // the Branch list (the default, ahead)
    assert.strictEqual(p.rows().length, 3, "painted");
    for (const n of Object.values(p.nodes)) deepFreeze(n);
    // Each of these is a new object with the same content. Any write throws on the frozen page.
    await p.status(okStatus(ent));
    assert.strictEqual(p.pending("/api/git/numstat").length, 1, "the status asked for its numstat");
    await p.answer("/api/git/numstat", num(ns));
    await p.status(okStatus(ent));
    await p.answer("/api/git/numstat", num(ns));
    p.ctx.dockChangesRender();
    p.ctx.dockChangesRefresh();
    await settle();
    assert.deepStrictEqual(rejected.map(String), [], "nothing tried to write");
    // And the frozen page does throw on a write, so the test above could have failed.
    assert.throws(() => { p.rows()[0].className = "x"; }, TypeError);
  } finally { process.off("unhandledRejection", onRej); }
});

test("a change in one row writes only that row", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }, { p: "b.go", x: "M", a: 2, d: 2 }]));
  const [a, b] = p.rows();
  deepFreeze(a);
  // b's count changes; a is frozen, so touching it throws.
  await p.status(okStatus([{ p: "a.go", x: "M" }, { p: "b.go", x: "M" }]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 1 }, { p: "b.go", x: "M", a: 9, d: 2 }]));
  assert.strictEqual(text(b.querySelector(".dk-ct")), "+9−2");
  assert.strictEqual(text(a.querySelector(".dk-ct")), "+1−1");
});

// --- fix round 1: the base, the pick, the menu --------------------------------------------------------------------------

test("a branch switched to at the same commit asks for the base again, so the branch row names the new branch", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }], { branch: "main" }));
  await p.answer("/api/git/base", baseOk({ ahead: true, branch: "main" }));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 0 }]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 0 }]));
  assert.strictEqual(text(p.el("dock-branch").querySelector(".dk-br-name")), "main");
  await p.status(okStatus([{ p: "a.go", x: "M" }], { branch: "feat/x" })); // same head h1
  assert.strictEqual(p.calls("/api/git/base").length, 2, "git checkout -b leaves HEAD where it was");
  await p.answer("/api/git/base", baseOk({ ahead: true, branch: "feat/x" }));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 1, d: 0 }]));
  assert.strictEqual(text(p.el("dock-branch").querySelector(".dk-br-name")), "feat/x");
  await p.status(okStatus([{ p: "a.go", x: "M" }], { branch: "feat/x" }));
  assert.strictEqual(p.calls("/api/git/base").length, 2, "and not again for the same branch");
});

test("a base that fails once the page has a good one keeps the toggle and the Branch pick on screen", async () => {
  const p = page();
  p.st.store.set(KEY, JSON.stringify({ s1: "branch" }));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "branch" }], "the pick is asked beside the status");
  await p.answer("/api/git/base", baseOk({ ahead: false }));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "A", a: 7, d: 0 }])); // branch, the pick
  assert.strictEqual(p.el("dock-scope").hidden, false);
  assert.strictEqual(text(p.rows()[0].querySelector(".dk-ct")), "+7−0");
  // A commit moves HEAD: the base is asked for again and the request fails.
  await p.status(okStatus([{ p: "a.go", x: "M" }], { head: "h2" }));
  await p.answer("/api/git/base", null, false);
  assert.strictEqual(p.el("dock-scope").hidden, false, "the toggle does not vanish for a flake");
  assert.deepStrictEqual(Array.from(p.el("dock-scope").children, (b) => b.getAttribute("aria-pressed")), ["false", "true"], "Branch is still the scope");
  assert.strictEqual(p.ctx.dockScope("s1"), "branch");
  assert.strictEqual(p.ctx.gitBaseFor("/w").state, "ok", "the last good answer is the one held");
  assert.strictEqual(p.pending("/api/git/base").length, 0, "no retry loop");
  await p.status(okStatus([{ p: "a.go", x: "M" }], { head: "h2" }));
  assert.strictEqual(p.calls("/api/git/base").length, 2, "not asked again within the pause");
});

test("pressing either segment pins the choice, the one already on as well", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  await p.answer("/api/git/numstat", num([]));
  await p.answer("/api/git/numstat", num([]));
  assert.strictEqual(p.prefs(), undefined, "nothing pinned yet: Branch is the default");
  p.el("dock-scope").children[1].click(); // Branch, already on
  assert.deepStrictEqual(JSON.parse(p.prefs()), { s1: "branch" });
  // The default moves (HEAD is no longer ahead): the pick holds.
  await p.status(okStatus([{ p: "a.go", x: "M" }], { head: "h2" }));
  await p.answer("/api/git/base", baseOk({ ahead: false }));
  assert.strictEqual(p.ctx.dockScope("s1"), "branch");
  const q = page();
  await q.status(okStatus([{ p: "a.go", x: "M" }]));
  await q.answer("/api/git/base", baseOk({ ahead: false }));
  await q.answer("/api/git/numstat", num([]));
  q.el("dock-scope").children[0].click(); // Uncommitted, already on by default
  assert.deepStrictEqual(JSON.parse(q.prefs()), { s1: "head" });
});

test("a second press on the ⋯ closes its menu; a page without the menu element does nothing", async () => {
  const p = page();
  p.el("dock-more").onclick({ stopPropagation() {} });
  assert.strictEqual(p.st.shows, 1);
  assert.strictEqual(p.el("dock-more").getAttribute("aria-expanded"), "true");
  p.el("dock-more").onclick({ stopPropagation() {} });
  assert.strictEqual(p.st.shows, 1, "no second menu");
  assert.strictEqual(p.st.closed, 1, "it was closed (focus goes back to the ⋯)");
  assert.strictEqual(p.el("dock-more").getAttribute("aria-expanded"), "false");
  p.el("dock-more").onclick({ stopPropagation() {} });
  assert.strictEqual(p.st.shows, 2, "and a third press opens it again");
  const q = page();
  delete q.nodes["ft-menu"];
  assert.doesNotThrow(() => q.el("dock-more").onclick({ stopPropagation() {} }));
  assert.strictEqual(q.st.shows, undefined);
});

test("Uncommitted scope with a status of no entries asks for no numstat: nothing is left to count", async () => {
  const p = page();
  // The first status of a root cannot be known clean before it lands: its numstat goes out beside it.
  await p.status(okStatus([]));
  await p.answer("/api/git/base", baseOk({ ahead: false }));
  await p.answer("/api/git/numstat", num([]));
  assert.strictEqual(p.calls("/api/git/numstat").length, 1);
  assert.strictEqual(text(p.el("dock-chg-note")), "No changes");
  // Once a status said clean, every later empty status asks none, and the explicit Refresh path (a fresh one) neither.
  await p.status(okStatus([]));
  await p.status(okStatus([]), { fresh: true });
  assert.strictEqual(p.calls("/api/git/numstat").length, 1, "a clean tree's numstat would be empty by definition");
});

test("a status that turns empty after changes leaves no row behind, and the next one with entries asks again", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: false }));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 3, d: 1 }]));
  assert.strictEqual(p.rows().length, 1);
  // Another status with the change: its numstat goes out beside it and is still out when the tree goes clean.
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.strictEqual(p.calls("/api/git/numstat").length, 2);
  // Committed: the status is clean. The numstat of before must not keep a row (the numstat's own files make rows), not even
  // while one is out, nor when that older one lands late.
  await p.status(okStatus([], { head: "h2" }));
  assert.strictEqual(p.rows().length, 0, "no phantom row from the stale numstat");
  assert.strictEqual(p.el("dock-chg-note").hidden, false);
  assert.strictEqual(p.calls("/api/git/numstat").length, 2, "none was asked for the empty status");
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 3, d: 1 }]));
  assert.strictEqual(p.rows().length, 0, "a numstat answer older than the clean status cannot bring a row back");
  await p.status(okStatus([{ p: "b.go", x: "M" }], { head: "h2" }));
  assert.strictEqual(p.calls("/api/git/numstat").length, 3, "a status with entries asks");
});

test("Branch scope still asks with a clean working tree: the branch can differ from its base", async () => {
  const p = page();
  p.st.store.set(KEY, JSON.stringify({ s1: "branch" }));
  await p.status(okStatus([]));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  assert.strictEqual(p.ctx.dockScope("s1"), "branch");
  const ns = p.calls("/api/git/numstat");
  assert.strictEqual(ns.length, 1);
  assert.deepStrictEqual(ns[0].body, { path: "/w", scope: "branch" });
});

// --- P2: asked beside the status; the last list per root, drawn while git answers -------------------------------------------

// keptView: what gitlast.js keeps for /w, as its stored text.
const keptRows = [
  { path: "/w/a.go", name: "a.go", dir: "", code: "M", add: 3, del: 1, mine: false, folder: false, provisional: false },
  { path: "/w/b.go", name: "b.go", dir: "", code: "U", add: null, del: null, mine: false, folder: false, provisional: false },
];
const kept = (root, view, status) => [["agent-box:gitLast", JSON.stringify([[root, { status: status || null, view }]])]];
const keptView = { kind: "rows", scope: "branch", showScope: true, rows: keptRows, more: 0, count: 2, branch: { from: "feat/x", to: "main", add: 3, del: 1 } };

test("a status ask sends the numstat (scope auto) and the base at once, before the status lands", async () => {
  const p = page();
  const before = p.st.fetches.length;
  const done = p.ctx.gitStatusLoad("/w", "s1");
  await settle();
  assert.deepStrictEqual(p.st.fetches.slice(before).map((f) => [f.url, f.body]), [
    ["/api/git/status", { path: "/w" }],
    ["/api/git/base", { path: "/w" }],
    ["/api/git/numstat", { path: "/w", scope: "auto" }],
  ]);
  p.st.fetches[before].resolve({ ok: true, json: async () => okStatus([{ p: "a.go", x: "M" }]) });
  await done;
  await settle();
  assert.strictEqual(p.calls("/api/git/base").length, 1, "the landing does not ask the base again");
  assert.strictEqual(p.calls("/api/git/numstat").length, 1, "nor the numstat");
});

test("a remembered Branch pick asks Branch beside the status; Uncommitted after a clean status waits for the landing", async () => {
  const p = page();
  p.st.store.set(KEY, JSON.stringify({ s1: "branch" }));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.deepStrictEqual(p.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "branch" }]);
  const q = page();
  q.st.store.set(KEY, JSON.stringify({ s1: "head" }));
  await q.status(okStatus([])); // the first status of the root: not known clean before it lands, so one is asked beside it
  await q.answer("/api/git/base", baseOk());
  await q.answer("/api/git/numstat", num([]));
  await q.status(okStatus([]));
  assert.strictEqual(q.calls("/api/git/numstat").length, 1, "Uncommitted after a clean status: nothing beside the next one");
  await q.status(okStatus([{ p: "a.go", x: "M" }]));
  assert.deepStrictEqual(q.calls("/api/git/numstat").map((f) => f.body), [{ path: "/w", scope: "head" }, { path: "/w", scope: "head" }], "asked when a status with entries landed");
});

test("an auto answer that names Branch is the Branch list once the base lands, with no second numstat", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 2, d: 0 }, { p: "c.go", x: "A", a: 9, d: 0 }], { scope: "branch" }));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  assert.deepStrictEqual(Array.from(p.el("dock-scope").children, (b) => b.getAttribute("aria-pressed")), ["false", "true"]);
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-nm"))), ["a.go", "c.go"]);
  assert.strictEqual(text(p.rows()[1].querySelector(".dk-ct")), "+9−0");
  assert.strictEqual(p.calls("/api/git/numstat").length, 1);
});

test("a kept list is drawn Updating… before git answers, and nothing reads it: no Review, no rows for the review loop", async () => {
  const p = page({ __session: kept("/w", keptView) });
  p.ctx.dockChangesRender();
  assert.strictEqual(p.el("dock-changes").hidden, false);
  assert.deepStrictEqual(p.rows().map((r) => text(r.querySelector(".dk-nm"))), ["a.go", "b.go"]);
  assert.strictEqual(text(p.el("dock-chg-count")), "Updating…");
  assert.strictEqual(p.el("dock-review").hidden, true, "Review waits for git");
  assert.strictEqual(p.ctx.dockChangesRows().length, 0, "F7 and Mark reviewed see no rows");
  assert.strictEqual(p.ctx.dockChangesView(), null, "All changes sees no view");
  p.rows()[0].onclick();
  assert.deepStrictEqual(plain(p.st.shown), [["/w/a.go", null]], "a row still opens its file (a read of the file as it is now)");
});

test("the live list replaces the kept one; Branch waiting for its list keeps the kept one; a folder that is not a repository hides it", async () => {
  const p = page({ __session: kept("/w", keptView) });
  p.st.store.set(KEY, JSON.stringify({ s1: "branch" }));
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk({ ahead: true }));
  assert.strictEqual(text(p.el("dock-chg-count")), "Updating…", "Branch is still reading its list: the kept one stays");
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 5, d: 0 }]));
  assert.strictEqual(text(p.el("dock-chg-count")), "1 changed");
  assert.strictEqual(p.ctx.dockChangesRows().length, 1);
  assert.strictEqual(p.el("dock-review").hidden, false);
  const q = page({ __session: kept("/w", keptView) });
  await q.status({ state: "notrepo", entries: [] });
  assert.strictEqual(q.el("dock-changes").hidden, true);
});

test("a live list is kept for its root; a note or a kept list is not written back", async () => {
  const p = page();
  await p.status(okStatus([{ p: "a.go", x: "M" }]));
  await p.answer("/api/git/base", baseOk());
  await p.answer("/api/git/numstat", num([{ p: "a.go", x: "M", a: 3, d: 1 }]));
  const saved = JSON.parse(p.st.session.get("agent-box:gitLast"));
  assert.strictEqual(saved[0][0], "/w");
  assert.deepStrictEqual(saved[0][1].view.rows.map((r) => [r.name, r.code, r.add, r.del]), [["a.go", "M", 3, 1]]);
  assert.strictEqual(saved[0][1].status.entries.length, 1, "and the status the tree's marks come from");
});
