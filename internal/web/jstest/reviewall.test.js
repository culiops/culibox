"use strict";
// The All changes tab (reviewall.js) as it runs on the page, in a vm: the real reviewall.js, reviewmodel.js and linediff.js against a fake DOM, a fake
// Monaco (diff editors that remember what was made and what was disposed), a fake IntersectionObserver the test drives, a fake fetch and spies for
// the notes host and the image mount. It proves what the browser check cannot see cheaply: which sections get an editor and a request and when,
// that an editor going takes its models, notes host and requests with it, that nothing survives a hidden tab, that a late answer is dropped, the
// cap, the row -> section scroll and F7 across sections. The look and the real Monaco are the visual check's.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const settle = async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); };

class El extends Node {
  constructor(tag, doc) { super(tag, doc); this.scrollTop = 0; this.clientHeight = 900; this.offsetTop = 0; this.offsetHeight = 0; this.listeners = {}; }
  addEventListener(type, fn) { this.listeners[type] = fn; }
  replaceChildren(...cs) { super.replaceChildren(...cs.map((c) => (typeof c === "string" ? new Text(c) : c))); }
  append(...cs) { super.append(...cs.map((c) => (typeof c === "string" ? new Text(c) : c))); }
  getBoundingClientRect() { return { top: this.offsetTop - this.scrollTop, left: 0, width: 600, height: this.offsetHeight }; }
}

function fakeEditor(st, id) {
  const subs = {};
  const on = (n) => (fn) => { (subs[n] = subs[n] || []).push(fn); return { dispose: () => { subs[n] = (subs[n] || []).filter((x) => x !== fn); } }; };
  return {
    id, subs, h: 300, pos: { lineNumber: 1, column: 1 }, focused: false, revealed: [], top: 0,
    getContentHeight() { return this.h; },
    onDidContentSizeChange: on("size"), onDidFocusEditorText: on("focus"),
    hasTextFocus() { return st.focus === this; },
    getPosition() { return this.pos; },
    setPosition(p) { this.pos = { lineNumber: p.lineNumber, column: p.column }; },
    revealLineInCenter(l) { this.revealed.push(l); },
    getTopForLineNumber(l) { return l * 19; },
    getScrollTop() { return this.top; },
    focus() { st.focus = this; this.focused = true; },
    fire(n) { for (const f of [...(subs[n] || [])]) f({}); },
  };
}

function page() {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new El(t, doc);
  const nodes = { "dock-acts": new El("div", doc), "drawer-max": new El("button", doc) };
  nodes["dock-acts"].append(nodes["drawer-max"]);
  doc.getElementById = (id) => nodes[id] || null;
  doc.addEventListener = () => {};
  const st = {
    fetches: [], io: [], diffs: [], models: [], attached: [], detached: [], mounts: [], toasts: [], marks: [], unmarks: [],
    shown: true, scope: "head", head: "h1", notes: [], view: { kind: "rows", rows: [] }, marksOf: {}, files: [], why: "", opened: [],
    kinds: {}, // path -> what /api/fs/read says for it: "text" (default), "binary", {status}
    texts: {}, // path -> working-tree text
    bases: {}, // path -> base text
    sid: "s1",
  };
  class FakeIO {
    constructor(cb, o) { this.cb = cb; this.opts = o; this.targets = new Set(); this.disconnected = false; st.io.push(this); }
    observe(el) { this.targets.add(el); }
    unobserve(el) { this.targets.delete(el); }
    disconnect() { this.targets.clear(); this.disconnected = true; }
  }
  const fetch = (url, init) => new Promise((resolve, reject) => {
    const f = { url, init: init || {}, body: init && init.body ? JSON.parse(init.body) : null, resolve, reject, signal: init && init.signal, aborted: false };
    if (init && init.signal) init.signal.addEventListener("abort", () => { f.aborted = true; reject(Object.assign(new Error("aborted"), { name: "AbortError" })); });
    st.fetches.push(f);
  });
  const monaco = {
    editor: {
      ScrollType: { Immediate: 1 },
      createModel(text, lang) {
        const m = { text, lang, disposed: false, getValue() { return this.text; }, getLineCount() { return this.text.split("\n").length; }, dispose() { this.disposed = true; } };
        st.models.push(m);
        return m;
      },
      createDiffEditor(host, opts) {
        const d = {
          host, opts, model: null, disposed: false, layouts: 0, mEd: fakeEditor(st, "m"), oEd: fakeEditor(st, "o"), updates: [],
          getModifiedEditor() { return this.mEd; }, getOriginalEditor() { return this.oEd; },
          setModel(m) { this.model = m; },
          layout() { this.layouts++; },
          updateOptions(o) { Object.assign(this.opts, o); },
          onDidUpdateDiff(fn) { this.updates.push(fn); return { dispose: () => { this.updates = this.updates.filter((x) => x !== fn); } }; },
          fireDiff() { for (const f of [...this.updates]) f(); },
          dispose() { this.disposed = true; },
        };
        st.diffs.push(d);
        return d;
      },
    },
  };
  const ctx = vm.createContext({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String, Error, AbortController, RegExp, Infinity, NaN,
    document: doc, IntersectionObserver: FakeIO, fetch,
    setTimeout: (fn, ms) => { st.timers = st.timers || []; st.timers.push({ fn, ms, live: true }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers && st.timers[id - 1]) st.timers[id - 1].live = false; },
    svgIcon: () => new El("svg", doc),
    showToast: (m) => { st.toasts.push(m); },
    IS_MAC: false,
    shortcutMatches: (id, e) => e.id === id,
    viewerPrefs: { getDiffMode: () => "side" },
    editorSyncShown: () => st.shown,
    dockChangesView: () => st.view,
    dockScope: () => st.scope,
    reviewMarksOf: () => st.marksOf,
    reviewFilesOf: () => st.files,
    reviewWhy: () => st.why,
    reviewMark: (sid, p, row) => { st.marks.push([sid, p, row.path]); return true; },
    reviewUnmark: (sid, p) => { st.unmarks.push([sid, p]); return true; },
    notesFor: () => st.notes,
    gitBaseFor: () => null,
    ftRoot: () => "/w",
    relativePath: (root, p) => (p.startsWith(root + "/") ? p.slice(root.length + 1) : null),
    iconFor: () => "F",
    shortPath: (p) => p,
    drawerShowFile: (p, o) => { st.opened.push([p, o]); return true; },
    editorDiffKey: (t) => ({ known: true, scope: st.scope, baseName: "", key: [t.path, st.scope, st.head].join("\n") }),
    editorDiffBase: (b) => (b && b.state === "ok" ? { head: b.missing ? "" : b.content, missing: !!b.missing, why: "", retry: false } : { head: null, missing: false, why: (b && b.reason) || "git is not available here", retry: true }),
    editorDiffHeadLabel: () => "HEAD ↔ working tree",
    editorDiffOptions: () => ({ renderSideBySide: true, useInlineViewWhenSpaceIsLimited: true }),
    editorDiffSeg: () => new El("div", doc),
    editorDiffSegItem: () => new El("button", doc),
    diffModeSet: () => {},
    loadMonaco: () => Promise.resolve(monaco),
    monacoLang: () => "plaintext",
    editorBindChangeKeys: () => { st.bound = (st.bound || 0) + 1; },
    editorBindNoteKeys: () => {},
    editorNotesAttachHost: (h) => { st.attached.push(h); return true; },
    editorNotesDetachHost: (k) => { st.detached.push(k); return true; },
    imageDiffIs: (p) => /\.png$/.test(p),
    imageDiffMount: (el, p, scope, o) => { const m = { el, p, scope, o, disposed: false, dispose() { this.disposed = true; } }; st.mounts.push(m); return m; },
  });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load('let dockMonaco = null; const MONACO_THEME = "culibox"; const DKC_CODE_TITLES = { M: "Modified", A: "Added", U: "Untracked", D: "Deleted", C: "Conflict" }; const DG_ICON_INLINE = [], DG_ICON_SIDE = [];', "state");
  for (const f of ["reviewmodel.js", "linediff.js", "reviewall.js"]) load(src(f), f);
  const p = { ctx, st, doc, nodes, monaco, run: (c) => load(c, "test") };
  // The answers a section's two requests get (the tests resolve them as they like).
  p.answer = async (pathOrFetch) => {
    const fs_ = st.fetches.filter((f) => !f.done && !f.aborted && (typeof pathOrFetch === "string" ? (f.body ? f.body.path === pathOrFetch : f.url.includes(encodeURIComponent(pathOrFetch))) : true));
    for (const f of fs_) {
      f.done = true;
      if (f.body) { // /api/git/show
        const b = st.bases[f.body.path];
        f.resolve({ ok: true, status: 200, json: async () => (b === undefined ? { state: "ok", missing: true } : { state: "ok", content: b }) });
      } else {
        const p_ = decodeURIComponent(f.url.split("path=")[1]);
        const k = st.kinds[p_] || "text";
        if (k === "binary") f.resolve({ ok: true, status: 200, json: async () => ({ kind: "binary" }) });
        else if (typeof k === "object") f.resolve({ ok: false, status: k.status, json: async () => ({ error: "x" }) });
        else f.resolve({ ok: true, status: 200, json: async () => ({ kind: "text", content: st.texts[p_] !== undefined ? st.texts[p_] : "" }) });
      }
    }
    await settle();
  };
  // The observers' own report: `near` and `far` are the paths within a screen and within two screens of the viewport.
  p.view = (tab, near, far) => {
    const [nearIO, farIO] = st.io.filter((i) => !i.disconnected).slice(-2);
    const entries = (io, paths) => [...io.targets].map((el) => ({ target: el, isIntersecting: paths.some((x) => tab.secs.get(x).el === el) }));
    farIO.cb(entries(farIO, far));
    nearIO.cb(entries(nearIO, near));
  };
  // The timers that dispose a let-go section's models (RA_GRACE_MS) run: the grace is over.
  p.grace = () => { for (const t of st.timers || []) if (t.live && t.ms === 2000) { t.live = false; t.fn(); } };
  p.tab = (rows) => {
    st.view = { kind: "rows", rows };
    ctx.__host = { sessionId: () => "s1" };
    const tab = load('new ReviewAllTab("s1", { host: __host })', "tab");
    p.t = tab;
    tab.onAttach();
    return tab;
  };
  return p;
}

const row = (name, extra) => Object.assign({ path: "/w/" + name, name, dir: "", code: "M", add: 2, del: 1, folder: false, provisional: false }, extra);
const names = (n) => Array.from({ length: n }, (_, i) => "f" + i + ".go");
const postsOf = (p) => p.st.fetches.filter((f) => f.url === "/api/git/show");
const readsOf = (p) => p.st.fetches.filter((f) => f.url.startsWith("/api/fs/read?path="));
const liveDiffs = (p) => p.st.diffs.filter((d) => !d.disposed);

test("a section that comes near the viewport asks for its two sides and gets a read-only diff editor with the unchanged regions collapsed", async () => {
  const p = page();
  const rows = names(12).map((n) => row(n));
  for (const r of rows) { p.st.bases[r.path] = "a\nb\nc\n"; p.st.texts[r.path] = "a\nB\nc\n"; }
  const tab = p.tab(rows);
  assert.strictEqual(tab.listEl.children.length, 12, "a section per row");
  assert.strictEqual(postsOf(p).length, 0, "nothing is asked before a section is near");
  p.view(tab, ["/w/f0.go", "/w/f1.go", "/w/f2.go"], ["/w/f0.go", "/w/f1.go", "/w/f2.go", "/w/f3.go", "/w/f4.go"]);
  assert.deepStrictEqual(postsOf(p).map((f) => f.body), [0, 1, 2].map((i) => ({ path: "/w/f" + i + ".go", scope: "head" })), "one /api/git/show per near section");
  assert.strictEqual(readsOf(p).length, 3, "and one read of the working tree");
  await p.answer();
  assert.strictEqual(p.st.diffs.length, 3);
  const o = p.st.diffs[0].opts;
  assert.deepStrictEqual(JSON.parse(JSON.stringify(o.hideUnchangedRegions)), { enabled: true });
  assert.strictEqual(o.readOnly, true);
  assert.strictEqual(o.originalEditable, false);
  assert.strictEqual(o.renderSideBySide, true, "the layout is the single-file view's preference");
  assert.strictEqual(o.useInlineViewWhenSpaceIsLimited, true);
  const d = p.st.diffs[0];
  assert.strictEqual(d.model.original.getValue(), "a\nb\nc\n");
  assert.strictEqual(d.model.modified.getValue(), "a\nB\nc\n");
  assert.strictEqual(p.st.attached.length, 3, "each section attaches its own notes host");
  assert.strictEqual(p.st.attached[0].diff, d);
  assert.strictEqual(p.st.attached[0].path(), "/w/f0.go");
  assert.strictEqual(p.st.attached[0].sid(), "s1");
});

test("a section's height is its content's, at most 1200, and follows the content", async () => {
  const p = page();
  const rows = [row("a.go"), row("b.go")];
  const tab = p.tab(rows);
  p.view(tab, ["/w/a.go", "/w/b.go"], ["/w/a.go", "/w/b.go"]);
  await p.answer();
  const [a, b] = p.st.diffs;
  assert.notStrictEqual(tab.secs.get("/w/a.go").body.style.height, "300px", "before Monaco's diff is computed the content is the whole file, not the folded view: the placeholder's height stands");
  p.st.diffs.forEach((d) => d.fireDiff());
  assert.strictEqual(tab.secs.get("/w/a.go").body.style.height, "300px");
  b.mEd.h = 5000;
  b.mEd.fire("size");
  assert.strictEqual(tab.secs.get("/w/b.go").body.style.height, "1200px", "capped, then it scrolls inside");
  b.mEd.h = 410.6;
  b.mEd.fire("size");
  assert.strictEqual(tab.secs.get("/w/b.go").body.style.height, "411px");
  assert.ok(b.layouts >= 1, "the diff editor is told");
});

test("scrolling far away disposes the editors (models, notes host, requests) and scrolling back makes them again; between one screen and two they stay", async () => {
  const p = page();
  const rows = names(12).map((n) => row(n));
  for (const r of rows) { p.st.bases[r.path] = "x\n"; p.st.texts[r.path] = "y\n"; }
  const tab = p.tab(rows);
  const P = (...i) => i.map((n) => "/w/f" + n + ".go");
  p.view(tab, P(0, 1, 2), P(0, 1, 2, 3, 4));
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 3);
  // A little down: sections 1..3 near, 0..5 within two screens. 3 is made; 0 keeps its editor (it is between one and two screens away).
  p.view(tab, P(1, 2, 3), P(0, 1, 2, 3, 4, 5));
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 4);
  assert.strictEqual(postsOf(p).length, 4, "only the section that came near asked");
  // Far down: 9..11 near. 0..3 are more than two screens away.
  const modelsBefore = p.st.models.length;
  p.view(tab, P(9, 10, 11), P(7, 8, 9, 10, 11));
  assert.strictEqual(liveDiffs(p).length, 0, "the old editors are gone at once");
  assert.strictEqual(p.st.diffs.filter((d) => d.disposed).length, 4);
  assert.strictEqual(p.st.models.filter((m) => m.disposed).length, 0, "their models wait out the grace: a diff of them may be in flight in Monaco's worker");
  p.grace();
  assert.strictEqual(p.st.models.filter((m) => m.disposed).length, modelsBefore, "then go, every one");
  assert.strictEqual(p.st.detached.length, 4, "and their notes hosts");
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 3);
  // Back to the top: re-created, asked for again.
  p.view(tab, P(0, 1, 2), P(0, 1, 2, 3, 4));
  assert.strictEqual(liveDiffs(p).length, 0);
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 3);
  assert.strictEqual(postsOf(p).length, 4 + 3 + 3);
  // No growth per cycle: ten round trips leave the same count live and every disposed model disposed.
  for (let i = 0; i < 10; i++) {
    p.view(tab, P(9, 10, 11), P(7, 8, 9, 10, 11));
    await p.answer();
    p.view(tab, P(0, 1, 2), P(0, 1, 2, 3, 4));
    await p.answer();
  }
  p.grace();
  assert.strictEqual(liveDiffs(p).length, 3);
  assert.strictEqual(p.st.models.filter((m) => !m.disposed).length, 6, "two models per live editor, none leaked");
  assert.strictEqual(p.st.attached.length - p.st.detached.length, 3, "a notes host per live editor");
});

test("a request that is still out when its section goes is aborted, and its answer makes nothing", async () => {
  const p = page();
  const rows = names(6).map((n) => row(n));
  const tab = p.tab(rows);
  const P = (...i) => i.map((n) => "/w/f" + n + ".go");
  p.view(tab, P(0, 1), P(0, 1, 2));
  assert.strictEqual(postsOf(p).length, 2);
  p.view(tab, P(5), P(3, 4, 5)); // 0 and 1 are far away before their answers
  assert.ok(postsOf(p).slice(0, 2).every((f) => f.aborted), "aborted");
  assert.ok(readsOf(p).slice(0, 2).every((f) => f.aborted));
  await p.answer();
  assert.strictEqual(p.st.diffs.length, 1, "only the section that is near made an editor");
  assert.strictEqual(p.st.models.length, 2);
  // An answer that arrives after the abort (the fetch resolved before the signal was seen) is dropped by the sequence.
  const q = page();
  const t2 = q.tab([row("a.go")]);
  q.view(t2, ["/w/a.go"], ["/w/a.go"]);
  const sec = t2.secs.get("/w/a.go");
  const seq = sec.seq;
  q.st.fetches.forEach((f) => { f.done = true; f.resolve({ ok: true, status: 200, json: async () => ({ state: "ok", content: "x", kind: "text" }) }); });
  t2.drop(sec); // the section goes while the answers are on their way
  assert.ok(sec.seq > seq);
  await settle();
  assert.strictEqual(q.st.diffs.length, 0);
  assert.strictEqual(q.st.models.length, 0);
});

test("a hidden tab holds nothing: every editor goes, the observers go, nothing is asked, and an answer in flight is dropped", async () => {
  const p = page();
  const rows = names(4).map((n) => row(n));
  const tab = p.tab(rows);
  const P = (...i) => i.map((n) => "/w/f" + n + ".go");
  p.view(tab, P(0, 1), P(0, 1, 2));
  await p.answer("/w/f0.go");
  assert.strictEqual(liveDiffs(p).length, 1, "one editor, one request still out");
  tab.onHide(); // another dock tab is shown
  assert.strictEqual(liveDiffs(p).length, 0, "no section editor survives a hidden tab");
  assert.ok(p.st.io.every((i) => i.disconnected), "the observers are gone");
  p.grace();
  assert.strictEqual(p.st.models.filter((m) => !m.disposed).length, 0);
  assert.ok(postsOf(p).every((f) => f.done || f.aborted), "the request in flight was aborted");
  const before = p.st.fetches.length;
  await p.answer();
  tab.sync(); // a status lands while the tab is hidden
  assert.strictEqual(p.st.fetches.length, before, "nothing is asked for a hidden tab");
  assert.strictEqual(p.st.diffs.length, 1);
  // Back on screen: the observers watch again and the near sections ask again.
  tab.onAttach();
  p.view(tab, P(0), P(0, 1));
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 1);
  // The dock goes (closed, folded, a page over it, the page hidden) while the tab stays the shown one.
  p.st.shown = false;
  tab.sync();
  assert.strictEqual(liveDiffs(p).length, 0, "the dock is not on screen: nothing held");
  const asked = p.st.fetches.length;
  tab.sync();
  assert.strictEqual(p.st.fetches.length, asked);
  p.st.shown = true;
  tab.sync();
  p.view(tab, P(0), P(0, 1));
  assert.strictEqual(postsOf(p).length > 0, true);
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 1);
  // Closed: dispose lets go of everything and leaves no section in the document.
  tab.dispose();
  assert.strictEqual(liveDiffs(p).length, 0);
  p.grace();
  assert.strictEqual(p.st.models.filter((m) => !m.disposed).length, 0);
  assert.strictEqual(p.st.attached.length, p.st.detached.length, "every notes host was detached");
});

test("more than 100 rows: the first 100 sections and the note that says how to open the rest", () => {
  const p = page();
  const tab = p.tab(names(130).map((n) => row(n)));
  assert.strictEqual(tab.listEl.children.length, 100);
  assert.strictEqual(tab.moreEl.hidden, false);
  assert.match(tab.moreEl.textContent, /Open files one by one from the Changes list/);
  assert.match(tab.moreEl.textContent, /first 100 of 130/);
  assert.strictEqual(tab.scrollTo("/w/f100.go"), false, "a row past the cap has no section: the click opens the file as usual");
  assert.strictEqual(tab.scrollTo("/w/f99.go"), true);
  const small = page().tab([row("a.go")]);
  assert.strictEqual(small.moreEl.hidden, true);
});

test("the sections follow the Changes list: order, additions, removals, and a folder or a provisional row gets none", () => {
  const p = page();
  const tab = p.tab([row("a.go"), row("dir", { folder: true }), row("b.go", { provisional: true }), row("c.go")]);
  assert.deepStrictEqual([...tab.order], ["/w/a.go", "/w/c.go"]);
  const elA = tab.secs.get("/w/a.go").el;
  p.st.view = { kind: "rows", rows: [row("c.go"), row("a.go"), row("d.go")] };
  tab.sync();
  assert.deepStrictEqual([...tab.order], ["/w/c.go", "/w/a.go", "/w/d.go"], "the Changes list's order");
  assert.strictEqual(tab.secs.get("/w/a.go").el, elA, "a section keeps its element");
  assert.deepStrictEqual([...tab.listEl.children].map((e) => e.getAttribute("aria-label")), ["Changes in c.go", "Changes in a.go", "Changes in d.go"]);
  p.st.view = { kind: "note", text: "Git status needs the box runtime (docker)", title: "why" };
  tab.sync();
  assert.strictEqual(tab.listEl.children.length, 0);
  assert.strictEqual(tab.noteEl.hidden, false);
  assert.strictEqual(tab.noteEl.textContent, "Git status needs the box runtime (docker)", "the tab says why there is no list");
  p.st.view = { kind: "rows", rows: [] };
  tab.sync();
  assert.strictEqual(tab.noteEl.textContent, "No changes");
});

test("a section header: name, folder, letter, counts, the notes chip, the reviewed circle (a toggle) and Open", () => {
  const p = page();
  const r = row("a.go", { dir: "internal/web", code: "A", add: 7, del: 0 });
  p.st.notes = [{ path: "/w/a.go", status: "pending" }, { path: "/w/a.go", status: "pending" }, { path: "/w/a.go", status: "sent" }, { path: "/w/b.go", status: "pending" }];
  const tab = p.tab([r]);
  const sec = tab.secs.get("/w/a.go");
  const q = (cls) => sec.el.querySelector("." + cls);
  assert.strictEqual(q("dk-nm").textContent, "a.go");
  assert.strictEqual(q("dk-dir").textContent, "internal/web");
  assert.strictEqual(q("dk-st").textContent, "A");
  assert.strictEqual(q("dk-add").textContent, "+7");
  assert.strictEqual(q("dk-del").textContent, "−0");
  assert.strictEqual(q("dk-nt").hidden, false);
  assert.strictEqual(q("dk-nt").textContent, "2 notes", "pending notes of this file only");
  assert.strictEqual(q("dk-sec-ck").getAttribute("aria-pressed"), "false");
  q("dk-sec-ck").click();
  assert.deepStrictEqual(p.st.marks, [["s1", "/w/a.go", "/w/a.go"]]);
  // A mark that stands: the circle is done, and pressing it clears the mark.
  p.run('globalThis.__mark = reviewMarkFor({ path: "/w/a.go", code: "A", add: 7, del: 0 }, [], 1)');
  p.st.marksOf = { "/w/a.go": p.ctx.__mark };
  tab.sync();
  assert.strictEqual(q("dk-sec-ck").getAttribute("aria-pressed"), "true");
  assert.match(q("dk-sec-ck").className, /dk-ck-done/);
  q("dk-sec-ck").click();
  assert.deepStrictEqual(p.st.unmarks, [["s1", "/w/a.go"]]);
  // Not markable (no Activity list yet): says why, does nothing.
  p.st.why = "Waiting for the session's activity";
  p.st.marksOf = {};
  tab.sync();
  assert.strictEqual(q("dk-sec-ck").getAttribute("aria-disabled"), "true");
  assert.strictEqual(q("dk-sec-ck").title, "Waiting for the session's activity");
  p.st.marks.length = 0;
  q("dk-sec-ck").click();
  assert.deepStrictEqual(p.st.marks, []);
  q("dk-sec-open").click();
  assert.deepStrictEqual(JSON.parse(JSON.stringify(p.st.opened)), [["/w/a.go", { preview: false }]]);
  // No count where git gave none.
  p.st.view = { kind: "rows", rows: [row("a.go", { add: null, del: null })] };
  tab.sync();
  assert.strictEqual(q("dk-ct").hidden, true);
});

test("the head says how many files, the counts, what is compared and reviewed n of m", () => {
  const p = page();
  const tab = p.tab([row("a.go", { add: 3, del: 1 }), row("b.go", { add: 4, del: 2 })]);
  assert.strictEqual(tab.countEl.textContent, "2 files");
  assert.strictEqual(tab.ctAdd.textContent, "+7");
  assert.strictEqual(tab.ctDel.textContent, "−3");
  assert.strictEqual(tab.cmpEl.textContent, "HEAD ↔ working tree");
  assert.strictEqual(tab.revEl.textContent, "reviewed 0 of 2");
});

test("a row's click: reviewAllScrollTo brings the section to the top, opens a collapsed one, and is false when the tab is not on screen", () => {
  const p = page();
  const tab = p.tab([row("a.go"), row("b.go"), row("c.go")]);
  tab.order.forEach((path_, i) => { tab.secs.get(path_).el.offsetTop = 1000 * i; tab.secs.get(path_).el.offsetHeight = 900; });
  assert.strictEqual(p.run('reviewAllScrollTo("/w/c.go")'), true);
  assert.strictEqual(tab.scrollEl.scrollTop, 2000);
  assert.strictEqual(p.run('reviewAllScrollTo("/w/nope.go")'), false);
  tab.collapse(tab.secs.get("/w/b.go"), true);
  assert.strictEqual(tab.secs.get("/w/b.go").body.hidden, true);
  p.run('reviewAllScrollTo("/w/b.go")');
  assert.strictEqual(tab.secs.get("/w/b.go").body.hidden, false, "the section the reader asked for is open");
  assert.strictEqual(tab.scrollEl.scrollTop, 1000);
  tab.onHide();
  assert.strictEqual(p.run('reviewAllScrollTo("/w/a.go")'), false, "the tab is not the one on screen: the click opens the file");
});

test("a collapsed section holds no editor and is not made again while it stays collapsed", async () => {
  const p = page();
  const tab = p.tab([row("a.go")]);
  const P = ["/w/a.go"];
  p.view(tab, P, P);
  await p.answer();
  assert.strictEqual(liveDiffs(p).length, 1);
  tab.collapse(tab.secs.get("/w/a.go"), true);
  assert.strictEqual(liveDiffs(p).length, 0, "collapsing lets go of the editor");
  const n = p.st.fetches.length;
  p.view(tab, P, P);
  tab.sync();
  assert.strictEqual(p.st.fetches.length, n);
  tab.collapse(tab.secs.get("/w/a.go"), false);
  assert.strictEqual(p.st.fetches.length, n + 2, "expanding asks again");
});

test("an image row gets the image comparer, mounted only when near and disposed when it goes; no Monaco, no text requests", async () => {
  const p = page();
  const tab = p.tab([row("logo.png", { code: "D" }), row("a.go")]);
  const P = ["/w/logo.png"];
  p.view(tab, P, P);
  assert.strictEqual(p.st.mounts.length, 1);
  const m = p.st.mounts[0];
  assert.strictEqual(m.p, "/w/logo.png");
  assert.strictEqual(m.scope, "head");
  assert.strictEqual(m.o.deleted, true, "a deleted image has only the old side");
  assert.strictEqual(p.st.fetches.length, 0, "the comparer asks for its own bytes");
  p.view(tab, [], []);
  assert.strictEqual(m.disposed, true, "disposed when the section leaves");
  tab.onHide();
  assert.strictEqual(p.st.mounts.filter((x) => !x.disposed).length, 0);
});

test("a binary file says so, a file that cannot be read says why, and neither makes an editor", async () => {
  const p = page();
  const tab = p.tab([row("bin.dat"), row("huge.txt"), row("odd.txt")]);
  p.st.kinds["/w/bin.dat"] = "binary";
  p.st.kinds["/w/huge.txt"] = { status: 413 };
  p.st.kinds["/w/odd.txt"] = { status: 500 };
  const all = tab.order;
  p.view(tab, all, all);
  await p.answer();
  assert.strictEqual(p.st.diffs.length, 0);
  const text = (n) => tab.secs.get("/w/" + n).bodyIn.textContent;
  assert.strictEqual(text("bin.dat"), "binary file changed");
  assert.match(text("huge.txt"), /too large/);
  assert.match(text("odd.txt"), /could not read the file/);
  // A section that shows a message holds it: scrolling does not ask again until it leaves and comes back.
  const n = p.st.fetches.length;
  p.view(tab, all, all);
  assert.strictEqual(p.st.fetches.length, n);
});

test("a deleted file shows its base against an empty new side and does not read the file; an added one has an empty base", async () => {
  const p = page();
  const tab = p.tab([row("gone.go", { code: "D", add: 0, del: 3 }), row("new.go", { code: "U", add: 2, del: 0 })]);
  p.st.bases["/w/gone.go"] = "one\ntwo\nthree\n";
  p.st.texts["/w/new.go"] = "x\ny\n";
  p.view(tab, tab.order, tab.order);
  assert.deepStrictEqual(readsOf(p).map((f) => f.url), ["/api/fs/read?path=" + encodeURIComponent("/w/new.go")], "no read for the deleted file");
  await p.answer();
  const [gone, added] = p.st.diffs;
  assert.strictEqual(gone.model.original.getValue(), "one\ntwo\nthree\n");
  assert.strictEqual(gone.model.modified.getValue(), "");
  assert.strictEqual(added.model.original.getValue(), "", "the base has no such file");
  assert.strictEqual(added.model.modified.getValue(), "x\ny\n");
});

test("when git cannot give the base the section says why and makes no editor", async () => {
  const p = page();
  const tab = p.tab([row("a.go")]);
  p.view(tab, ["/w/a.go"], ["/w/a.go"]);
  p.st.fetches.forEach((f) => {
    f.done = true;
    if (f.body) f.resolve({ ok: true, status: 200, json: async () => ({ state: "unavailable", reason: "git needs the box runtime" }) });
    else f.resolve({ ok: true, status: 200, json: async () => ({ kind: "text", content: "x" }) });
  });
  await settle();
  assert.strictEqual(p.st.diffs.length, 0);
  assert.match(tab.secs.get("/w/a.go").bodyIn.textContent, /Changes unavailable: git needs the box runtime/);
});

test("a section is asked for again when its row or the scope moves, not otherwise", async () => {
  const p = page();
  const r = row("a.go");
  const tab = p.tab([r]);
  const P = ["/w/a.go"];
  p.view(tab, P, P);
  await p.answer();
  assert.strictEqual(postsOf(p).length, 1);
  tab.sync(); // a status that says what the last one said
  assert.strictEqual(postsOf(p).length, 1);
  p.st.view = { kind: "rows", rows: [row("a.go", { add: 9 })] }; // the agent edited it
  tab.sync();
  assert.strictEqual(liveDiffs(p).length, 0, "the old version's editor went");
  assert.strictEqual(postsOf(p).length, 2, "and the new one asks, once");
  await p.answer();
  p.st.scope = "branch";
  tab.sync();
  assert.strictEqual(postsOf(p).length, 3);
  assert.strictEqual(postsOf(p)[2].body.scope, "branch", "the scope of the session's Changes list");
  assert.strictEqual(postsOf(p).length, 3);
});

test("F7 / Shift+F7 across sections: the changes of a section, then the next section's, a stop at one with no diff, and the ends", async () => {
  const p = page();
  const rows = ["a.go", "b.go", "c.go"].map((n) => row(n));
  const base = Array.from({ length: 30 }, (_, i) => "l" + i);
  const edit = (...at) => base.map((l, i) => (at.includes(i) ? l + "!" : l)).join("\n");
  p.st.bases["/w/a.go"] = base.join("\n"); p.st.texts["/w/a.go"] = edit(4, 20); // changes on lines 5 and 21
  p.st.bases["/w/b.go"] = base.join("\n"); p.st.texts["/w/b.go"] = edit(9);     // line 10
  p.st.bases["/w/c.go"] = base.join("\n"); p.st.texts["/w/c.go"] = edit(2);     // line 3
  const tab = p.tab(rows);
  const all = tab.order;
  tab.order.forEach((path_, i) => { tab.secs.get(path_).el.offsetTop = 1000 * i; tab.secs.get(path_).el.offsetHeight = 900; });
  p.view(tab, all, all);
  await p.answer();
  p.st.diffs.forEach((d) => d.fireDiff()); // Monaco's own diff is computed: the sections are settled
  const [da, db, dc] = p.st.diffs;
  assert.strictEqual(p.run("reviewAllStep(1)"), true);
  assert.strictEqual(da.mEd.pos.lineNumber, 5, "from the top: the first change of the first section");
  assert.strictEqual(da.mEd.focused, true, "the keyboard goes with it");
  p.run("reviewAllStep(1)");
  assert.strictEqual(da.mEd.pos.lineNumber, 21);
  p.run("reviewAllStep(1)");
  assert.strictEqual(db.mEd.pos.lineNumber, 10, "past the last change of a.go: b.go's first");
  p.run("reviewAllStep(1)");
  assert.strictEqual(dc.mEd.pos.lineNumber, 3);
  assert.deepStrictEqual(p.st.toasts, []);
  p.run("reviewAllStep(1)");
  assert.deepStrictEqual(p.st.toasts, ["That was the last change"]);
  p.run("reviewAllStep(-1)");
  assert.strictEqual(db.mEd.pos.lineNumber, 10, "back: the previous section's LAST change");
  p.run("reviewAllStep(-1)");
  assert.strictEqual(da.mEd.pos.lineNumber, 21);
  // The key is reviewAll's only while the tab is on screen.
  tab.onHide();
  assert.strictEqual(p.run("reviewAllStep(1)"), false, "a hidden tab leaves F7 to the single-file view");
});

test("F7 into a section that has no editor yet scrolls to it and lands on its first change when the diff is there; a stop (image) is just a stop", async () => {
  const p = page();
  const rows = [row("a.go"), row("logo.png"), row("c.go")];
  const base = Array.from({ length: 30 }, (_, i) => "l" + i).join("\n");
  p.st.bases["/w/a.go"] = base; p.st.texts["/w/a.go"] = base.replace("l4", "L4");
  p.st.bases["/w/c.go"] = base; p.st.texts["/w/c.go"] = base.replace("l7", "L7");
  const tab = p.tab(rows);
  tab.order.forEach((path_, i) => { tab.secs.get(path_).el.offsetTop = 1000 * i; tab.secs.get(path_).el.offsetHeight = 900; });
  p.view(tab, ["/w/a.go"], ["/w/a.go", "/w/logo.png"]);
  await p.answer();
  p.st.diffs[0].fireDiff();
  p.run("reviewAllStep(1)"); // a.go's one change, line 5
  assert.strictEqual(p.st.diffs[0].mEd.pos.lineNumber, 5);
  p.run("reviewAllStep(1)"); // the image section has no diff to step through: it is a stop
  assert.strictEqual(tab.scrollEl.scrollTop, 1000, "scrolled to the image section");
  p.view(tab, ["/w/logo.png", "/w/c.go"], ["/w/a.go", "/w/logo.png", "/w/c.go"]);
  await p.answer();
  p.run("reviewAllStep(1)"); // c.go has no editor yet (its diff is not known): scrolled to, and landed on when it settles
  assert.strictEqual(tab.scrollEl.scrollTop, 2000);
  const dc = p.st.diffs[p.st.diffs.length - 1];
  assert.strictEqual(dc.model.modified.getValue().includes("L7"), true);
  dc.fireDiff();
  assert.strictEqual(dc.mEd.pos.lineNumber, 8, "the first change of the section that was made on the way");
  assert.strictEqual(dc.mEd.focused, true);
});

test("the layout, wrap and font follow into every section editor", async () => {
  const p = page();
  const tab = p.tab([row("a.go"), row("b.go")]);
  p.view(tab, tab.order, tab.order);
  await p.answer();
  p.ctx.editorDiffOptions = () => ({ renderSideBySide: false, diffWordWrap: "on" });
  p.run("reviewAllApplyPrefs()");
  assert.ok(p.st.diffs.every((d) => d.opts.renderSideBySide === false && d.opts.diffWordWrap === "on"));
});

test("the tab answers what the dock asks of a viewer", () => {
  const p = page();
  const tab = p.tab([row("a.go")]);
  assert.strictEqual(tab.dirty(), false);
  assert.strictEqual(tab.generation(), null);
  assert.strictEqual(tab.path, "");
  assert.strictEqual(tab.name, "All changes");
  assert.strictEqual(tab.scroll, 0);
  assert.strictEqual(typeof tab.toolbarApi.paint, "function");
  assert.strictEqual(tab.showChanges(), false);
  tab.toolbarApi.paint();
  tab.applyWrap();
  tab.wrapAnchor()();
  // The layout control is in the dock's bar while the tab is on screen, and out of it when it is not.
  const acts = p.nodes["dock-acts"];
  assert.ok(acts.children.includes(tab.actsEl));
  tab.onHide();
  assert.ok(!acts.children.includes(tab.actsEl));
  tab.onAttach();
  tab.dispose();
  assert.ok(!acts.children.includes(tab.actsEl));
  tab.dispose(); // twice is fine
});
