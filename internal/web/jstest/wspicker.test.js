"use strict";
// wspicker.js: the workspace folder picker factory (New session and the profile page's
// Workspace block). The repo has no jsdom, so it runs against a tiny fake DOM (as
// credentials-dom.test.js and profilepage.test.js do) with the real dirpicker.js helpers
// beside it. Each test builds its own context, so nothing is shared between them: which is
// also the property under test (two pickers keep separate state).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");

class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.attrs = {}; this.handlers = {};
    this.style = {}; this.hidden = false; this.className = ""; this.id = ""; this.title = ""; this.textContent = "";
    this.value = ""; this.selectionStart = 0; this.selectionEnd = 0;
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  removeAttribute(k) { delete this.attrs[k]; }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(type, fn) { (this.handlers[type] = this.handlers[type] || []).push(fn); }
  removeEventListener(type, fn) { const a = this.handlers[type] || []; const i = a.indexOf(fn); if (i >= 0) a.splice(i, 1); }
  append(...cs) { for (const c of cs) { c.parent = this; this.children.push(c); } }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.append(...cs); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  getBoundingClientRect() { return { left: 10, top: 100, bottom: 130, width: 300 }; }
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  select() { this.selectionStart = 0; this.selectionEnd = this.value.length; }
  scrollIntoView() {}
  focus() { if (this.env) { this.env.doc.activeElement = this; fire(this, "focus"); } }
}
// An event the way the page's handlers read it; preventDefault is recorded.
function fire(el, type, e) {
  const ev = Object.assign({ type, target: el, prevented: false, preventDefault() { this.prevented = true; } }, e || {});
  for (const f of (el.handlers[type] || []).slice()) f(ev);
  if (type === "click" && typeof el.onclick === "function") el.onclick(ev); // a row is wired with onclick
  return ev;
}
const count = (el) => Object.values(el.handlers).reduce((n, a) => n + a.length, 0);

// One page: a document, a window, a fetch that answers from `server`, a clock that only moves
// when the test says so, and the real helpers the factory calls.
function env(extra) {
  const server = { search: [], list: {}, searches: [], lists: [], check: {}, checks: [], roots: null, rootsReads: 0, writes: [] };
  const timers = [];
  const toasts = [];
  const doc = new El("document");
  doc.activeElement = null;
  doc.createElement = (t) => new El(t);
  const win = new El("window");
  win.innerHeight = 800;
  const box = Object.assign({
    document: doc, window: win, console, Promise, JSON, Math, Array, Object, Set, String, Number, AbortController,
    workspacesCache: [{ label: "culishop", path: "/home/me/culishop" }, { label: "notes", path: "/home/me/notes" }],
    showToast: (m, o) => { toasts.push([m, o && o.error === true]); },
    fetch: async (url, init) => {
      const u = new URL(url, "http://x");
      if (init && init.method && init.method !== "GET") server.writes.push(init.method + " " + u.pathname);
      if (u.pathname === "/api/workspace-roots") {
        server.rootsReads++;
        const body = server.roots;
        return { ok: !!body, status: body ? 200 : 500, json: async () => body || {} };
      }
      if (u.pathname === "/api/fs/search") {
        server.searches.push(u.searchParams.get("q"));
        const hits = server.search;
        return { ok: true, status: 200, json: async () => ({ results: hits, truncated: false }) };
      }
      if (u.pathname === "/api/workspace/check") {
        const asked = u.searchParams.get("path");
        server.checks.push(asked);
        const body = server.check[asked];
        return { ok: !!body, status: body ? 200 : 404, json: async () => body || {} };
      }
      server.lists.push(u.searchParams.get("path"));
      const r = server.list[u.searchParams.get("path")] || { status: 404 };
      return { ok: !r.status, status: r.status || 200, json: async () => r.body || {} };
    },
    setTimeout: (fn, ms) => { timers.push({ fn, ms, live: true }); return timers.length; },
    clearTimeout: (id) => { if (timers[id - 1]) timers[id - 1].live = false; },
  }, extra);
  const ctx = vm.createContext(box);
  for (const f of ["dirpicker.js", "wspicker.js"]) new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  const e = {
    box, doc, win, server, toasts,
    // The debounce timers that are still due (the 4 s fetch deadlines are not run).
    runDebounce: () => { for (const t of timers.splice(0)) if (t.live && t.ms < 1000) t.fn(); },
    pending: () => timers.filter((t) => t.live && t.ms < 1000).length,
    // One picker on its own input and list, wired the way New session wires it.
    make(opts) {
      const input = new El("input");
      const list = new El("div");
      list.hidden = true;
      input.env = e;
      const calls = { pick: [], note: [] };
      const picker = box.createWsPicker(Object.assign({
        input, list, onPick: (p) => calls.pick.push(p), onNote: (t, idle) => calls.note.push([t, idle]),
      }, opts));
      return { input, list, picker, calls };
    },
  };
  doc.env = e;
  return e;
}
const type = (p, text) => { p.input.value = text; fire(p.input, "input"); };
const rowsOf = (p) => p.list.children.filter((c) => c.attrs["aria-disabled"] === undefined).map((c) => c.children[0].textContent);
const flush = () => new Promise((r) => setImmediate(r));

test("two pickers keep separate state: typing in A does not change B's rows or value", async () => {
  const t = env();
  const a = t.make(), b = t.make();
  fire(a.input, "focus");
  fire(b.input, "focus");
  assert.deepStrictEqual(rowsOf(a), ["culishop", "notes"]);
  assert.deepStrictEqual(rowsOf(b), ["culishop", "notes"]);
  type(a, "/home/me/other");
  assert.strictEqual(a.picker.value(), "/home/me/other");
  assert.strictEqual(b.picker.value(), "", "B's value is its own");
  assert.deepStrictEqual(rowsOf(b), ["culishop", "notes"], "B's rows are not touched");
  assert.strictEqual(b.list.hidden, false, "and neither is its list");
  assert.deepStrictEqual(b.calls.pick, [], "B is told of nothing");
  // A fuzzy search in A: B's rows stay the workspaces.
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(a, "culi");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(a), ["culipulse"]);
  assert.deepStrictEqual(rowsOf(b), ["culishop", "notes"]);
  // Picking in B leaves A's value alone.
  fire(b.list.children[1], "click");
  assert.strictEqual(b.picker.value(), "/home/me/notes");
  assert.strictEqual(a.picker.value(), "/home/me/other");
});

test("value is the picked path, not the half-typed query", async () => {
  const t = env();
  const p = t.make();
  p.picker.set("/home/me/culishop");
  type(p, "cu");
  assert.strictEqual(p.input.value, "cu");
  assert.strictEqual(p.picker.value(), "/home/me/culishop", "a live search string is never the value");
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }, { rel: "culishop", path: "/home/me/culishop" }];
  type(p, "culi");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(t.server.searches, ["culi"]);
  assert.strictEqual(p.picker.value(), "/home/me/culishop", "still the old value while the rows are on screen");
  fire(p.list.children[0], "click");
  assert.strictEqual(p.picker.value(), "/home/me/work/culipulse");
  assert.strictEqual(p.input.value, "/home/me/work/culipulse", "the field shows the pick");
  assert.deepStrictEqual(p.calls.pick, ["/home/me/work/culipulse"]);
  assert.strictEqual(p.list.hidden, true);
  // Emptying the field empties the value.
  type(p, "");
  assert.strictEqual(p.picker.value(), "");
  assert.deepStrictEqual(p.calls.pick, ["/home/me/work/culipulse", ""]);
});

test("a typed absolute path is a valid value at once, with no pick and no Enter", () => {
  const t = env();
  const p = t.make();
  type(p, "/home/me/proj");
  assert.strictEqual(p.picker.value(), "/home/me/proj");
  assert.deepStrictEqual(p.calls.pick, ["/home/me/proj"]);
  assert.strictEqual(t.pending(), 0, "a path is not searched for");
  assert.strictEqual(p.list.hidden, true);
  // A relative or ~ query is a search, not a value.
  type(p, "~/proj");
  assert.strictEqual(p.picker.value(), "/home/me/proj");
  assert.strictEqual(t.pending(), 1);
});

test("Enter on a typed path asks the server and takes the path it answers; a refusal says why and keeps the typed value", async () => {
  const t = env();
  const p = t.make();
  t.server.list["/home/me/proj"] = { body: { path: "/home/me/proj" } };
  type(p, "/home/me/proj");
  const ev = fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(ev.prevented, true, "Enter never submits the form around the field");
  await flush();
  assert.deepStrictEqual(t.server.lists, ["/home/me/proj"]);
  assert.strictEqual(p.picker.value(), "/home/me/proj");
  assert.strictEqual(p.input.value, "/home/me/proj");
  // 400 / 404: a toast in words. 403 (outside every root) is no toast: the check line speaks for it.
  type(p, "/home/me/file.txt");
  t.server.list["/home/me/file.txt"] = { status: 400 };
  fire(p.input, "keydown", { key: "Enter" });
  await flush();
  type(p, "/home/me/nope");
  fire(p.input, "keydown", { key: "Enter" });
  await flush();
  type(p, "/etc");
  t.server.list["/etc"] = { status: 403 };
  fire(p.input, "keydown", { key: "Enter" });
  await flush();
  assert.deepStrictEqual(t.toasts, [
    ["Not a directory: /home/me/file.txt", true],
    ["No such file or directory: /home/me/nope", true],
  ]);
  assert.strictEqual(p.picker.value(), "/etc", "the typed folder stays the value");
});

test("arrows highlight, Enter picks the highlighted row, and with a fuzzy query and none highlighted takes the first", async () => {
  const t = env();
  const p = t.make();
  fire(p.input, "focus");
  assert.strictEqual(p.picker.highlighted(), false);
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "ArrowDown" });
  assert.strictEqual(p.picker.highlighted(), true);
  assert.strictEqual(p.list.children[1].attrs["aria-selected"], "true");
  assert.strictEqual(p.input.attrs["aria-activedescendant"], p.list.children[1].id);
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/notes");
  // Blank field, nothing highlighted: Enter does nothing (it must not take row 0).
  type(p, "");
  fire(p.input, "focus");
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "");
  // A fuzzy query: Enter takes the first hit.
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(p, "culi");
  t.runDebounce();
  await flush();
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/work/culipulse");
});

test("Ctrl/Cmd+Enter is left to the form's hotkey", () => {
  const t = env();
  const p = t.make();
  type(p, "/home/me/proj");
  const ev = fire(p.input, "keydown", { key: "Enter", ctrlKey: true });
  assert.strictEqual(ev.prevented, false);
  assert.deepStrictEqual(t.server.lists, []);
});

test("Esc closes the list and keeps the value; it is consumed only when asked to, and only with a list open", () => {
  const t = env();
  const plain = t.make();
  plain.picker.set("/home/me/culishop");
  fire(plain.input, "focus");
  assert.strictEqual(plain.list.hidden, false);
  let ev = fire(plain.input, "keydown", { key: "Escape" });
  assert.strictEqual(plain.list.hidden, true);
  assert.strictEqual(plain.picker.value(), "/home/me/culishop");
  assert.strictEqual(ev.prevented, false, "New session's dialog closes on the same Esc, as it always did");
  // The page picker consumes the Esc that closed its list, so the page does not close with it.
  const page = t.make({ keepEsc: true });
  fire(page.input, "focus");
  ev = fire(page.input, "keydown", { key: "Escape" });
  assert.strictEqual(page.list.hidden, true);
  assert.strictEqual(ev.prevented, true);
  ev = fire(page.input, "keydown", { key: "Escape" });
  assert.strictEqual(ev.prevented, false, "with no list open the Esc goes on to the page");
});

test("a half-typed query left in the field never becomes the value, whatever closes the list", async () => {
  const t = env();
  const p = t.make();
  p.picker.set("/home/me/culishop");
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(p, "culi");
  t.runDebounce();
  await flush();
  fire(t.doc, "click", { target: new El("div") }); // an outside click
  assert.strictEqual(p.list.hidden, true);
  assert.strictEqual(p.picker.value(), "/home/me/culishop");
  assert.strictEqual(p.input.value, "culi");
});

test("set writes the value and the field, tells nobody, and a pick or a resolved path tells onPick", () => {
  const t = env();
  const p = t.make();
  p.picker.set("/home/me/a");
  assert.strictEqual(p.picker.value(), "/home/me/a");
  assert.strictEqual(p.input.value, "/home/me/a");
  assert.deepStrictEqual(p.calls.pick, []);
  p.picker.set("");
  assert.strictEqual(p.picker.value(), "");
  p.picker.set(undefined);
  assert.strictEqual(p.picker.value(), "");
});

test("a search that lands after the list was closed or the picker destroyed is dropped", async () => {
  const t = env();
  const p = t.make();
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(p, "culi");
  t.runDebounce();
  p.picker.close(); // before the answer
  await flush();
  assert.strictEqual(p.list.hidden, true);
  assert.deepStrictEqual(p.list.children, []);
  type(p, "culip");
  t.runDebounce();
  p.picker.destroy();
  await flush();
  assert.strictEqual(p.list.hidden, true);
  assert.deepStrictEqual(p.list.children, []);
});

test("search failures and an empty answer say why in the note, with idle false", async () => {
  const t = env();
  const p = t.make();
  t.server.search = [];
  type(p, "zzz");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(p.calls.note.pop(), ["projects and top-level folders — type a full path", false]);
  assert.strictEqual(p.list.hidden, false, "the note row is the list");
  t.box.fetch = async () => { throw new Error("offline"); };
  type(p, "zzzz");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(p.calls.note.pop(), ["folder search unavailable", false]);
});

test("opening a blank field says what it does (idle) and offers the project roots", () => {
  const t = env();
  const p = t.make();
  fire(p.input, "focus");
  assert.deepStrictEqual(p.calls.note.pop(), ["2 project roots · type to filter · ↵ to pick", true]);
  assert.strictEqual(p.input.attrs["aria-expanded"], "true");
  assert.strictEqual(p.list.style.left, "10px");
  assert.strictEqual(p.list.style.width, "300px");
  assert.strictEqual(p.list.style.top, "130px", "below the field, on the field's own rect");
  assert.strictEqual(p.picker.isOpen(), true);
  p.picker.close();
  assert.strictEqual(p.input.attrs["aria-expanded"], "false");
  assert.strictEqual(p.picker.isOpen(), false);
});

test("refresh redraws the project roots only while the list is open and nothing is being searched", () => {
  const t = env();
  const p = t.make();
  fire(p.input, "focus");
  t.box.workspacesCache = [{ label: "one", path: "/home/me/one" }];
  p.picker.refresh();
  assert.deepStrictEqual(rowsOf(p), ["one"]);
  assert.deepStrictEqual(p.calls.note.pop(), ["1 project root · type to filter · ↵ to pick", true]);
  type(p, "on");
  t.box.workspacesCache = [{ label: "two", path: "/home/me/two" }];
  p.picker.refresh();
  assert.deepStrictEqual(rowsOf(p), ["one"], "a search in flight is never clobbered");
  type(p, "");
  p.picker.close();
  p.picker.refresh();
  assert.strictEqual(p.list.hidden, true, "a closed list stays closed");
});

test("settle does what Enter does, and when that settles nothing it sends the user back to the field", () => {
  const t = env();
  const p = t.make();
  p.picker.set("/home/me/culishop");
  type(p, "culi"); // a query typed over the resolved directory
  t.doc.activeElement = null;
  p.picker.settle();
  assert.deepStrictEqual(p.calls.note.pop(), ["Pick a workspace first", false]);
  assert.strictEqual(t.doc.activeElement, p.input, "focus returns to the field, which reopens its matches");
  // A typed path is being resolved, so there is nothing to complain about.
  type(p, "/home/me/proj");
  p.calls.note.length = 0;
  p.picker.settle();
  assert.deepStrictEqual(p.calls.note, []);
});

test("destroy takes every listener it put on the document and the window, and the picker stops reacting", () => {
  const t = env();
  const before = [count(t.doc), count(t.win)];
  const p = t.make();
  assert.ok(count(t.doc) > before[0] && count(t.win) > before[1], "the factory listens on the document and the window");
  assert.ok(count(p.input) > 0);
  fire(p.input, "focus");
  p.picker.destroy();
  assert.deepStrictEqual([count(t.doc), count(t.win)], before, "nothing is left behind on the page");
  assert.strictEqual(count(p.input), 0);
  assert.strictEqual(p.list.hidden, true);
  assert.strictEqual(t.pending(), 0);
  // Destroying twice is harmless.
  p.picker.destroy();
});

test("option ids are unique across pickers (aria-activedescendant points at one element)", () => {
  const t = env();
  const a = t.make(), b = t.make();
  fire(a.input, "focus");
  fire(b.input, "focus");
  const ids = [...a.list.children, ...b.list.children].map((c) => c.id);
  assert.strictEqual(new Set(ids).size, ids.length);
});

test("a click or focus on a filled field selects it whole, so typing replaces the path", () => {
  const t = env();
  const p = t.make();
  p.picker.set("/home/me/culishop");
  p.input.setSelectionRange(5, 5);
  fire(p.input, "click");
  assert.deepStrictEqual([p.input.selectionStart, p.input.selectionEnd], [0, 17]);
  // A drag selection is left alone.
  p.input.setSelectionRange(2, 6);
  fire(p.input, "click");
  assert.deepStrictEqual([p.input.selectionStart, p.input.selectionEnd], [2, 6]);
});

test("openOnFocus can keep the list closed on a focus the form made itself; a click still opens it", () => {
  const t = env();
  let ask = false;
  const p = t.make({ openOnFocus: () => ask });
  fire(p.input, "focus");
  assert.strictEqual(p.list.hidden, true, "asked, and the answer was no");
  assert.strictEqual(p.input.selectionEnd, p.input.value.length, "the field is still selected whole");
  fire(p.input, "click");
  assert.strictEqual(p.list.hidden, false, "a click is the user asking");
  p.picker.close();
  ask = true;
  fire(p.input, "focus");
  assert.strictEqual(p.list.hidden, false);
});

test("tabbing away closes the list; a pointer press that moves focus to a container does not", () => {
  const t = env();
  const p = t.make();
  fire(p.input, "focus");
  const container = new El("div");
  container.append(p.list);
  fire(p.input, "focusout", { relatedTarget: container });
  assert.strictEqual(p.list.hidden, false, "pointer press: the click handlers own dismissal");
  fire(p.input, "focusout", { relatedTarget: new El("button") });
  assert.strictEqual(p.list.hidden, true);
});

test("a resize while the list is open keeps it under the field; a scroll does only when the page asks (reanchor)", () => {
  const t = env();
  const plain = t.make();
  fire(plain.input, "focus");
  plain.list.style.top = "";
  fire(t.doc, "scroll", { target: new El("div") });
  assert.strictEqual(plain.list.style.top, "", "New session never listened for scrolls: the factory does not unless asked");
  fire(t.win, "resize");
  assert.strictEqual(plain.list.style.top, "130px");
  plain.picker.destroy();
  const page = t.make({ reanchor: true });
  fire(page.input, "focus");
  page.list.style.top = "";
  fire(t.doc, "scroll", { target: new El("div") });
  assert.strictEqual(page.list.style.top, "130px", "the profile page scrolls, and its list is position: fixed");
  page.list.style.top = "";
  fire(t.doc, "scroll", { target: page.list.children[0] });
  assert.strictEqual(page.list.style.top, "", "the list's own scroll moves nothing");
  const before = count(t.doc);
  page.picker.destroy();
  assert.ok(count(t.doc) < before, "the scroll listener goes with the picker");
});

test("a picker that is not asked to re-anchor puts no scroll listener on the document", () => {
  const t = env();
  const p = t.make();
  assert.strictEqual((t.doc.handlers.scroll || []).length, 0);
  p.picker.destroy();
  const q = t.make({ reanchor: true });
  assert.strictEqual(t.doc.handlers.scroll.length, 1);
  q.picker.destroy();
  assert.strictEqual(t.doc.handlers.scroll.length, 0);
});

test("every top-level name in wspicker.js is unique across the page scripts", () => {
  const names = (src) => new Set([...src.matchAll(/^(?:async\s+)?(?:function\s+|const\s+|let\s+|var\s+)([A-Za-z_$][\w$]*)/gm)].map((m) => m[1]));
  const mine = names(fs.readFileSync(path.join(STATIC, "wspicker.js"), "utf8"));
  assert.ok(mine.has("createWsPicker"));
  const clashes = [];
  for (const f of fs.readdirSync(STATIC).filter((x) => x.endsWith(".js") && x !== "wspicker.js")) {
    for (const n of names(fs.readFileSync(path.join(STATIC, f), "utf8"))) if (mine.has(n)) clashes.push(n + " (" + f + ")");
  }
  assert.deepStrictEqual(clashes, []);
});

// --- inline placement and "Last used" (UI plan 3, #173 Task 7; frame M4.3) -----------------------------------------------------

const POS = ["top", "left", "width", "bottom", "maxHeight"];
const cls = (el) => el.className.split(/\s+/);
// The row's trailing words: every span after the label that is not the parent-folder hint.
const lastOf = (row) => row.children.filter((c) => cls(c).includes("wsp-last")).map((c) => c.textContent);

test("inline: the list is placed by CSS under the field, so setOpen leaves no inline top/left/width/bottom/maxHeight", () => {
  const t = env();
  const p = t.make({ inline: () => true });
  // Leftovers from a fixed-position open (a window that was narrower a moment ago) are taken away.
  Object.assign(p.list.style, { top: "130px", left: "10px", width: "300px", bottom: "5px", maxHeight: "200px" });
  fire(p.input, "focus");
  assert.strictEqual(p.list.hidden, false);
  for (const k of POS) assert.ok(!p.list.style[k], k + " is not set inline: " + JSON.stringify(p.list.style[k]));
  assert.strictEqual(p.input.attrs["aria-expanded"], "true");
});

test("not inline (the phone, the profile page): the fixed placement from the input's rect is unchanged", () => {
  const t = env();
  for (const opts of [undefined, { inline: () => false }, { inline: undefined }]) {
    const p = t.make(opts);
    fire(p.input, "focus");
    assert.strictEqual(p.list.style.left, "10px");
    assert.strictEqual(p.list.style.width, "300px");
    assert.strictEqual(p.list.style.top, "130px");
    assert.strictEqual(p.list.style.maxHeight, "662px");
  }
});

test("inline rows are kit menu rows: a mono ellipsis label, the same roles and ids, no legacy ft-opt class", () => {
  const t = env();
  const p = t.make({ inline: () => true });
  fire(p.input, "focus");
  assert.strictEqual(p.list.children.length, 2);
  const row = p.list.children[0];
  assert.deepStrictEqual(cls(row), ["menu-item"]);
  assert.strictEqual(row.attrs.role, "option");
  assert.strictEqual(row.attrs["aria-selected"], "false");
  assert.strictEqual(row.title, "/home/me/culishop");
  const lab = row.children[0];
  assert.strictEqual(lab.textContent, "culishop");
  for (const c of ["menu-text", "mono", "t-sm", "ellipsis"]) assert.ok(cls(lab).includes(c), c);
  assert.strictEqual(rowsOf(p).join(), "culishop,notes");
  assert.strictEqual(row.children.length, 1, "a project root has no parent folder to show: an empty cell would add a 10px gap");
  // Not inline: the legacy row, as before.
  const q = t.make();
  fire(q.input, "focus");
  assert.deepStrictEqual(cls(q.list.children[0]), ["ft-opt"]);
});

test("inline keeps the keyboard: arrows move aria-selected and aria-activedescendant, Enter picks, Esc closes", () => {
  const t = env();
  const p = t.make({ inline: () => true });
  fire(p.input, "focus");
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "ArrowDown" });
  assert.strictEqual(p.list.children[1].attrs["aria-selected"], "true");
  assert.strictEqual(p.input.attrs["aria-activedescendant"], p.list.children[1].id);
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/notes");
  assert.strictEqual(p.list.hidden, true);
  fire(p.input, "focus");
  fire(p.input, "keydown", { key: "Escape" });
  assert.strictEqual(p.list.hidden, true);
});

test("an inline row shows the parent folder of a fuzzy hit as a trailing hint", async () => {
  const t = env();
  const p = t.make({ inline: () => true });
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(p, "culi");
  t.runDebounce();
  await flush();
  const row = p.list.children[0];
  assert.strictEqual(row.children[0].textContent, "culipulse");
  assert.strictEqual(row.children[1].textContent, "work");
  assert.ok(cls(row.children[1]).includes("wsp-hint"), "not .hint: app.css's global .hint adds a top margin and 11px type");
  // The frame's span is `mono t-xs muted` (M4.3): the class list says so too, not the --t-sm the name has.
  assert.deepStrictEqual(cls(row.children[1]), ["wsp-hint", "mono", "t-xs", "muted", "ellipsis"]);
  assert.ok(!cls(row.children[1]).includes("hint"));
  assert.deepStrictEqual(lastOf(row), []);
});

// Gate fix (#173, frame M4.3): the first row of a search is drawn highlighted and it is the one Enter takes, so the row
// Enter acts on is the row that looks chosen. The idle list (empty query) and the phone's and the profile page's lists
// start with nothing active, as before.
const searchHits = [{ rel: "work/a", path: "/home/me/work/a" }, { rel: "work/b", path: "/home/me/work/b" }, { rel: "work/c", path: "/home/me/work/c" }];
const selected = (p) => p.list.children.map((c, i) => (c.attrs["aria-selected"] === "true" ? i : -1)).filter((i) => i >= 0);

test("inline: after a search renders results, row 0 is active (aria-selected, aria-activedescendant) and Enter picks it", async () => {
  const t = env();
  const p = t.make({ inline: () => true });
  t.server.search = searchHits;
  type(p, "work");
  assert.deepStrictEqual(selected(p), [], "nothing before the results are in");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), ["a", "b", "c"]);
  assert.deepStrictEqual(selected(p), [0]);
  assert.strictEqual(p.input.attrs["aria-activedescendant"], p.list.children[0].id);
  assert.strictEqual(p.picker.highlighted(), true);
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/work/a");
  assert.deepStrictEqual(p.calls.pick, ["/home/me/work/a"]);
  assert.strictEqual(p.list.hidden, true);
});

test("inline: ArrowDown from the active first row goes to row 1, and ArrowUp from row 0 wraps to the last row, as it always did", async () => {
  const t = env();
  const p = t.make({ inline: () => true });
  t.server.search = searchHits;
  type(p, "work");
  t.runDebounce();
  await flush();
  fire(p.input, "keydown", { key: "ArrowDown" });
  assert.deepStrictEqual(selected(p), [1], "one row selected at a time");
  assert.strictEqual(p.input.attrs["aria-activedescendant"], p.list.children[1].id);
  fire(p.input, "keydown", { key: "ArrowUp" });
  assert.deepStrictEqual(selected(p), [0]);
  fire(p.input, "keydown", { key: "ArrowUp" });
  assert.deepStrictEqual(selected(p), [2], "the same wrap pickerNextIndex always did");
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/work/c");
});

test("inline: a new search starts on its own first row again, and a search that finds nothing activates nothing", async () => {
  const t = env();
  const p = t.make({ inline: () => true });
  t.server.search = searchHits;
  type(p, "work");
  t.runDebounce();
  await flush();
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "ArrowDown" });
  t.server.search = [{ rel: "x/y", path: "/home/me/x/y" }, { rel: "x/z", path: "/home/me/x/z" }];
  type(p, "x");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(selected(p), [0]);
  assert.strictEqual(p.input.attrs["aria-activedescendant"], p.list.children[0].id);
  t.server.search = [];
  type(p, "nothing-here");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), []);
  assert.deepStrictEqual(selected(p), [], "the note row is not an option to land on");
  assert.strictEqual(p.input.attrs["aria-activedescendant"], undefined);
  assert.strictEqual(p.picker.highlighted(), false);
});

test("inline: the pinned Last used row is the active one, so Enter takes it", async () => {
  const t = env();
  const p = t.make({ inline: () => true, lastUsed: () => "/home/me/work/b" });
  t.server.search = searchHits;
  type(p, "work");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), ["b", "a", "c"]);
  assert.deepStrictEqual(selected(p), [0]);
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/work/b");
});

test("the idle list starts with no active row, inline or not, and a non-inline search too (phone, profile page)", async () => {
  const t = env();
  const inline = t.make({ inline: () => true });
  fire(inline.input, "focus");
  assert.deepStrictEqual(selected(inline), [], "the idle list is unchanged");
  assert.strictEqual(inline.input.attrs["aria-activedescendant"], undefined);
  assert.strictEqual(inline.picker.highlighted(), false);
  const phone = t.make({ inline: () => false });
  t.server.search = searchHits;
  type(phone, "work");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(phone), ["a", "b", "c"]);
  assert.deepStrictEqual(selected(phone), [], "the phone's search starts with nothing highlighted");
  assert.strictEqual(phone.input.attrs["aria-activedescendant"], undefined);
  assert.strictEqual(phone.picker.highlighted(), false);
  const plain = t.make();
  type(plain, "work");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(selected(plain), []);
});

test("Last used: the row for the last spawn's folder comes first with a trailing 'Last used', and Enter/click pick it", () => {
  const t = env();
  const p = t.make({ inline: () => true, lastUsed: () => "/home/me/notes" });
  fire(p.input, "focus");
  assert.deepStrictEqual(rowsOf(p), ["notes", "culishop"]);
  assert.deepStrictEqual(lastOf(p.list.children[0]), ["Last used"]);
  assert.deepStrictEqual(lastOf(p.list.children[1]), []);
  assert.deepStrictEqual(p.list.children[0].children.map((c) => c.textContent), ["notes", "Last used"], "label, then the tag at the trailing edge");
  const tag = p.list.children[0].children.find((c) => cls(c).includes("wsp-last"));
  for (const c of ["t-sm", "muted"]) assert.ok(cls(tag).includes(c), c);
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/notes", "index 0 is the pinned row");
});

test("Last used: a hit of a fuzzy search is pinned the same way", async () => {
  const t = env();
  const p = t.make({ inline: () => true, lastUsed: () => "/home/me/work/b/" });
  t.server.search = [{ rel: "work/a", path: "/home/me/work/a" }, { rel: "work/b", path: "/home/me/work/b" }];
  type(p, "work");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), ["b", "a"]);
  assert.deepStrictEqual(lastOf(p.list.children[0]), ["Last used"]);
  fire(p.list.children[0], "click");
  assert.strictEqual(p.picker.value(), "/home/me/work/b");
});

test("Last used: a folder the list does not hold (deleted, under /tmp, not a project root) adds no row and no tag", async () => {
  const t = env();
  for (const gone of ["/home/me/deleted", "/tmp/claude-1001/x", ""]) {
    const p = t.make({ inline: () => true, lastUsed: () => gone });
    fire(p.input, "focus");
    assert.deepStrictEqual(rowsOf(p), ["culishop", "notes"], gone);
    assert.ok(p.list.children.every((r) => lastOf(r).length === 0), gone);
  }
  const p = t.make({ inline: () => true, lastUsed: () => "/home/me/deleted" });
  t.server.search = [{ rel: "work/a", path: "/home/me/work/a" }];
  type(p, "work");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), ["a"]);
});

test("Last used is New session's desktop list only: not inline, or no lastUsed option, leaves the rows as the server sent them", () => {
  const t = env();
  const phone = t.make({ inline: () => false, lastUsed: () => "/home/me/notes" });
  fire(phone.input, "focus");
  assert.deepStrictEqual(rowsOf(phone), ["culishop", "notes"], "the phone list is not reordered");
  assert.ok(phone.list.children.every((r) => lastOf(r).length === 0));
  const page = t.make({ inline: () => true });
  fire(page.input, "focus");
  assert.deepStrictEqual(rowsOf(page), ["culishop", "notes"], "a picker given no lastUsed pins nothing");
});

test("lastUsed is asked each time the list is drawn, so a spawn between two opens is seen", () => {
  const t = env();
  let last = "";
  const p = t.make({ inline: () => true, lastUsed: () => last });
  fire(p.input, "focus");
  assert.deepStrictEqual(rowsOf(p), ["culishop", "notes"]);
  p.picker.close();
  last = "/home/me/notes";
  fire(p.input, "focus");
  assert.deepStrictEqual(rowsOf(p), ["notes", "culishop"]);
});

test("idleText replaces the steady-state caption wherever the picker says it; the profile page keeps the default", () => {
  const t = env();
  const p = t.make({ idleText: (n) => "Last used · " + n + " roots" });
  fire(p.input, "focus");
  assert.deepStrictEqual(p.calls.note.pop(), ["Last used · 2 roots", true]);
  p.picker.showIdleNote();
  assert.deepStrictEqual(p.calls.note.pop(), ["Last used · 2 roots", true]);
  fire(p.list.children[0], "click");
  assert.deepStrictEqual(p.calls.note.pop(), ["Last used · 2 roots", true], "after a pick too");
  const q = t.make();
  q.picker.showIdleNote();
  assert.deepStrictEqual(q.calls.note.pop(), ["2 project roots · type to filter · ↵ to pick", true]);
});

test("a window resize across the phone breakpoint closes an open list instead of leaving legacy rows in a kit menu", () => {
  const t = env();
  let desktop = true;
  const p = t.make({ inline: () => desktop });
  fire(p.input, "focus");
  fire(t.win, "resize");
  assert.strictEqual(p.list.hidden, false, "a resize inside the same layout keeps the list");
  desktop = false;
  fire(t.win, "resize");
  assert.strictEqual(p.list.hidden, true);
  fire(p.input, "focus");
  assert.deepStrictEqual(cls(p.list.children[0]), ["ft-opt"], "reopened, it is drawn for the layout it is in");
});

test("filter(path) drops rows from the idle list and from a search, and drops nothing when absent", async () => {
  const t = env();
  const only = (p) => p.startsWith("/home/me/culishop");
  const a = t.make({ filter: only });
  fire(a.input, "focus");
  assert.deepStrictEqual(rowsOf(a), ["culishop"], "idle list keeps only visible folders");
  t.server.search = [{ rel: "me/culishop/api", path: "/home/me/culishop/api" }, { rel: "me/notes/x", path: "/home/me/notes/x" }];
  type(a, "ap");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(a), ["api"], "search hits are filtered too");
  const b = t.make();
  fire(b.input, "focus");
  assert.deepStrictEqual(rowsOf(b), ["culishop", "notes"], "no filter, no change");
});

test("openOnClick false keeps a click from opening the list (an on-demand picker); open() still does", () => {
  const t = env();
  const p = t.make({ openOnFocus: () => false, openOnClick: () => false });
  fire(p.input, "focus");
  fire(p.input, "click");
  assert.strictEqual(p.list.hidden, true);
  p.picker.open();
  assert.strictEqual(p.list.hidden, false);
});

test("openOnType false: typing never opens the list, and closes one Browse… opened", () => {
  const t = env();
  const p = t.make({ openOnFocus: () => false, openOnClick: () => false, openOnType: () => false });
  p.input.value = "proj";
  fire(p.input, "input");
  assert.strictEqual(p.list.hidden, true, "typing a fuzzy query opens nothing");
  p.input.value = "";
  fire(p.input, "input");
  assert.strictEqual(p.list.hidden, true, "emptying the field opens nothing");
  p.picker.open();
  assert.strictEqual(p.list.hidden, false, "Browse… opens it");
  p.input.value = "a";
  fire(p.input, "input");
  assert.strictEqual(p.list.hidden, true, "typing closes it again");
});

// --- the check line (#343): what Start in the typed folder does to the workspace roots ---

// A /api/workspace/check answer, field by field as the server sends it.
const chk = (over) => Object.assign({
  path: "/srv/x", exists: true, is_dir: true, root: "", would_add: true, add_root_path: "/srv/x", refused: "",
  box: { ok: true, reason: "", code: "" }, vm_shared: true,
}, over);

test("wsCheckLine: a missing or non-directory path is an error in today's words", () => {
  const t = env();
  const line = (c, tier) => JSON.parse(JSON.stringify(t.box.wsCheckLine(c, tier)));
  assert.deepStrictEqual(line(chk({ path: "/srv/nope", exists: false, is_dir: false, would_add: false }), "host"),
    { kind: "error", text: "No such file or directory: /srv/nope", share: false });
  assert.deepStrictEqual(line(chk({ path: "/srv/f.txt", is_dir: false, would_add: false }), "box"),
    { kind: "error", text: "Not a directory: /srv/f.txt", share: false });
});

test("wsCheckLine: a refused folder is an amber line that says a host session can still run there", () => {
  const t = env();
  const l = t.box.wsCheckLine(chk({ path: "/etc/nginx", add_root_path: "/etc", would_add: false, refused: "system folder" }), "host");
  assert.strictEqual(l.kind, "warn");
  assert.strictEqual(l.text, "CuliBox won't open /etc in Files (system folder). A host session can still run here; a box can't.");
  assert.strictEqual(l.share, false);
});

test("wsCheckLine: would_add is the neutral line, with the repo note when the root is the repo top", () => {
  const t = env();
  let l = t.box.wsCheckLine(chk(), "host");
  assert.deepStrictEqual(JSON.parse(JSON.stringify(l)), { kind: "info", text: "Starting here also lets CuliBox's Files, git and search open /srv/x.", share: false });
  l = t.box.wsCheckLine(chk({ path: "/srv/x/api", add_root_path: "/srv/x" }), "box");
  assert.strictEqual(l.kind, "info");
  assert.strictEqual(l.text, "Starting here also lets CuliBox's Files, git and search open /srv/x (the repo this folder is in).");
});

test("wsCheckLine: a folder the VM cannot see adds the share sentence and asks for the button; box tier makes it a warning", () => {
  const t = env();
  const unseen = chk({ vm_shared: false, box: { ok: false, reason: "not shared", code: "not_shared" } });
  const share = " The box VM can't see /srv/x, so git status and boxes won't work there.";
  let l = t.box.wsCheckLine(unseen, "host");
  assert.deepStrictEqual(JSON.parse(JSON.stringify(l)), { kind: "info", text: "Starting here also lets CuliBox's Files, git and search open /srv/x." + share, share: true });
  l = t.box.wsCheckLine(unseen, "box");
  assert.strictEqual(l.kind, "warn");
  assert.strictEqual(l.text, "Starting here also lets CuliBox's Files, git and search open /srv/x." + share);
  assert.strictEqual(l.share, true);
});

test("wsCheckLine: a box that would refuse a visible folder says why; a covered folder says nothing otherwise", () => {
  const t = env();
  const covered = chk({ root: "/srv", would_add: false });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(t.box.wsCheckLine(covered, "host"))), { kind: "none", text: "", share: false });
  assert.strictEqual(t.box.wsCheckLine(covered, "box").kind, "none");
  const bad = chk({ root: "/srv", would_add: false, box: { ok: false, reason: "it is your home folder", code: "unboxable" } });
  assert.strictEqual(t.box.wsCheckLine(bad, "host").kind, "none", "the host tier has no box to refuse");
  const l = t.box.wsCheckLine(bad, "box");
  assert.deepStrictEqual(JSON.parse(JSON.stringify(l)), { kind: "warn", text: "it is your home folder.", share: false });
  // A covered folder the VM cannot see: only the box tier cares.
  const mac = chk({ root: "/Users/me", would_add: false, vm_shared: false, box: { ok: false, reason: "not shared", code: "not_shared" } });
  assert.strictEqual(t.box.wsCheckLine(mac, "host").kind, "none");
  const m = t.box.wsCheckLine(mac, "box");
  assert.strictEqual(m.kind, "warn");
  assert.strictEqual(m.text, "The box VM can't see /srv/x, so git status and boxes won't work there.");
  assert.strictEqual(m.share, true);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(t.box.wsCheckLine(null, "box"))), { kind: "none", text: "", share: false });
});

test("wsCheckBlock: Start waits only on the box tier with a folder the box would refuse", () => {
  const t = env();
  const unseen = chk({ vm_shared: false, box: { ok: false, reason: "not shared", code: "not_shared" } });
  assert.match(t.box.wsCheckBlock(unseen, "box"), /^Starting here .*The box VM can't see \/srv\/x/);
  assert.strictEqual(t.box.wsCheckBlock(unseen, "host"), "", "switching to host clears it");
  assert.strictEqual(t.box.wsCheckBlock(chk(), "box"), "");
  assert.strictEqual(t.box.wsCheckBlock(chk({ exists: false, is_dir: false, box: { ok: false, reason: "x", code: "" } }), "box"), "", "a missing folder is the error line's, not a block");
  assert.strictEqual(t.box.wsCheckBlock(null, "box"), "");
});

test("a typed outside path is checked after 250 ms and renders the info line, with no toast", async () => {
  const t = env();
  const lines = [];
  const p = t.make({ onCheck: (c) => lines.push(c === null ? null : t.box.wsCheckLine(c, "host").text), checkOn: () => true });
  t.server.check["/srv/x"] = chk();
  lines.length = 0;
  type(p, "/srv/x");
  assert.deepStrictEqual(t.server.checks, [], "not on the keystroke");
  assert.strictEqual(t.pending(), 1);
  assert.deepStrictEqual(lines, [null], "the old line goes at once");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(t.server.checks, ["/srv/x"]);
  assert.deepStrictEqual(lines, [null, "Starting here also lets CuliBox's Files, git and search open /srv/x."]);
  assert.deepStrictEqual(t.toasts, []);
  assert.strictEqual(p.picker.value(), "/srv/x");
});

test("Enter on a path outside every root shows the line, not a toast, and keeps the value", async () => {
  const t = env();
  const got = [];
  const p = t.make({ onCheck: (c) => got.push(c) });
  t.server.list["/srv/x"] = { status: 403 };
  t.server.check["/srv/x"] = chk();
  type(p, "/srv/x");
  got.length = 0;
  t.runDebounce(); // the typing's own check
  await flush();
  got.length = 0;
  t.server.checks.length = 0;
  fire(p.input, "keydown", { key: "Enter" });
  await flush();
  assert.deepStrictEqual(t.toasts, []);
  assert.deepStrictEqual(t.server.checks, ["/srv/x"], "asked at once on Enter");
  assert.strictEqual(got[got.length - 1].path, "/srv/x");
  assert.strictEqual(p.picker.value(), "/srv/x");
});

test("only the newest typed path's answer is shown; a pick or an emptied field is checked or cleared at once", async () => {
  const t = env();
  const got = [];
  const p = t.make({ onCheck: (c) => got.push(c && c.path) });
  t.server.check["/srv/a"] = chk({ path: "/srv/a" });
  t.server.check["/srv/b"] = chk({ path: "/srv/b" });
  t.server.check["/home/me/notes"] = chk({ path: "/home/me/notes", root: "/home/me", would_add: false });
  type(p, "/srv/a");
  t.runDebounce();
  type(p, "/srv/b"); // /srv/a's answer is in flight and now stale
  await flush();
  assert.ok(!got.includes("/srv/a"), "a stale answer is dropped: " + JSON.stringify(got));
  t.runDebounce();
  await flush();
  assert.strictEqual(got[got.length - 1], "/srv/b");
  // Empty: no ask, the line goes.
  got.length = 0;
  t.server.checks.length = 0;
  type(p, "");
  assert.deepStrictEqual(got, [null]);
  assert.deepStrictEqual(t.server.checks, []);
  // A pick asks at once.
  fire(p.input, "focus");
  fire(p.list.children[1], "click");
  await flush();
  assert.deepStrictEqual(t.server.checks, ["/home/me/notes"]);
  assert.strictEqual(got[got.length - 1], "/home/me/notes");
});

test("without onCheck, or when checkOn says no, the picker never asks", async () => {
  const t = env();
  const plain = t.make();
  type(plain, "/srv/x");
  const off = t.make({ onCheck: () => { throw new Error("not asked"); }, checkOn: () => false });
  type(off, "/srv/y");
  off.picker.set("/srv/z");
  assert.strictEqual(t.pending(), 0);
  await flush();
  assert.deepStrictEqual(t.server.checks, []);
});

test("a relative or unanswered check leaves no line; destroy stops a pending ask", async () => {
  const t = env();
  const got = [];
  const p = t.make({ onCheck: (c) => got.push(c) });
  type(p, "~/proj");
  assert.deepStrictEqual(got, [], "a fuzzy query leaves the value's line alone");
  type(p, "/srv/none"); // no answer scripted: the mock answers 404
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(got, [null], "no answer, no line");
  type(p, "/srv/later");
  p.picker.destroy();
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(t.server.checks, ["/srv/none"]);
});

// --- "Other places" (spec 6.8) ------------------------------------------------------------------------------------------
// The group is the idle list's tail: project folders the server found outside home. It is drawn when the read arrives.
const others = (...paths) => ({ roots: [], corrupt: false, elsewhere: paths.map((p) => ({ path: p, label: p.slice(p.lastIndexOf("/") + 1) })) });
const optionRows = (p) => p.list.children.filter((c) => c.attrs.role === "option" && c.attrs["aria-disabled"] === undefined);
const headings = (p) => p.list.children.filter((c) => c.className.includes("wsp-head")).map((c) => c.textContent);

test("Other places: rows arrive under a heading below the project roots, the caption counts roots only", async () => {
  const t = env();
  t.server.roots = others("/srv/api", "/opt/web");
  const p = t.make({ otherPlaces: () => true, inline: () => true });
  fire(p.input, "focus");
  assert.deepStrictEqual(rowsOf(p), ["culishop", "notes"], "the roots are drawn at once, before the read answers");
  assert.deepStrictEqual(headings(p), []);
  await flush();
  assert.deepStrictEqual(optionRows(p).map((c) => c.children[0].textContent), ["culishop", "notes", "api", "web"]);
  assert.deepStrictEqual(headings(p), ["Other places"]);
  const kids = p.list.children;
  assert.strictEqual(kids[2].textContent, "Other places", "the heading sits between the roots and the other places");
  assert.strictEqual(kids[3].children[0].textContent, "api");
  assert.strictEqual(kids[3].children[1].textContent, "/srv", "the parent folder is the hint");
  assert.strictEqual(kids[3].title, "/srv/api");
  assert.strictEqual(p.calls.note[p.calls.note.length - 1][0], "2 project roots · type to filter · ↵ to pick");
  assert.strictEqual(p.list.hidden, false);
});

test("Other places: picking one sets the folder and asks the check; nothing is added", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  t.server.check["/srv/api"] = { path: "/srv/api", exists: true, is_dir: true, root: "", would_add: true, add_root_path: "/srv/api", refused: "", box: { ok: true }, vm_shared: true };
  const checks = [];
  const p = t.make({ otherPlaces: () => true, onCheck: (c) => checks.push(c) });
  fire(p.input, "focus");
  await flush();
  const row = optionRows(p)[2];
  fire(row, "click");
  await flush();
  assert.strictEqual(p.picker.value(), "/srv/api");
  assert.strictEqual(p.input.value, "/srv/api");
  assert.deepStrictEqual(p.calls.pick, ["/srv/api"]);
  assert.deepStrictEqual(t.server.checks, ["/srv/api"]);
  assert.strictEqual(checks[checks.length - 1].add_root_path, "/srv/api");
  assert.strictEqual(p.list.hidden, true);
  assert.deepStrictEqual(t.server.writes, [], "picking is no add: only Start adds a root");
});

test("Other places: the arrows skip the heading and Enter picks the highlighted other place", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  const p = t.make({ otherPlaces: () => true });
  fire(p.input, "focus");
  await flush();
  for (let i = 0; i < 3; i++) fire(p.input, "keydown", { key: "ArrowDown" });
  const rows = optionRows(p);
  assert.deepStrictEqual(rows.map((c) => c.attrs["aria-selected"]), ["false", "false", "true"]);
  assert.strictEqual(p.input.attrs["aria-activedescendant"], rows[2].id);
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/srv/api");
});

test("Other places: the highlighted row stays highlighted when the read arrives", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  const p = t.make({ otherPlaces: () => true });
  fire(p.input, "focus");
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "ArrowDown" }); // notes
  await flush();
  const rows = optionRows(p);
  assert.deepStrictEqual(rows.map((c) => c.attrs["aria-selected"]), ["false", "true", "false"]);
});

test("Other places: not asked, and not drawn, unless otherPlaces says yes", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  const plain = t.make();
  fire(plain.input, "focus");
  const off = t.make({ otherPlaces: () => false });
  fire(off.input, "focus");
  await flush();
  assert.strictEqual(t.server.rootsReads, 0);
  assert.deepStrictEqual(rowsOf(plain), ["culishop", "notes"]);
  assert.deepStrictEqual(rowsOf(off), ["culishop", "notes"]);
});

test("Other places: a failed read, a damaged answer or an empty list adds no group", async () => {
  for (const roots of [null, { elsewhere: "no" }, { elsewhere: [] }, { elsewhere: [{ path: "rel/x", label: "x" }, { path: "/ok", label: "" }, null] }]) {
    const t = env();
    t.server.roots = roots;
    const p = t.make({ otherPlaces: () => true, inline: () => true });
    fire(p.input, "focus");
    await flush();
    assert.deepStrictEqual(headings(p), [], JSON.stringify(roots));
    assert.deepStrictEqual(rowsOf(p), ["culishop", "notes"]);
  }
});

test("Other places: a read that lands after the list closed does not reopen it", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  const p = t.make({ otherPlaces: () => true });
  fire(p.input, "focus");
  fire(p.input, "keydown", { key: "Escape" });
  await flush();
  assert.strictEqual(p.list.hidden, true);
  fire(p.input, "focus"); // the next open already has them
  assert.deepStrictEqual(optionRows(p).map((c) => c.children[0].textContent), ["culishop", "notes", "api"]);
});

test("Other places: the project-root filter applies to them too, and a root is never listed twice", async () => {
  const t = env();
  t.server.roots = others("/srv/api", "/srv/vmless", "/home/me/notes");
  const p = t.make({ otherPlaces: () => true, filter: (path) => path !== "/srv/vmless" });
  fire(p.input, "focus");
  await flush();
  assert.deepStrictEqual(optionRows(p).map((c) => c.children[0].textContent), ["culishop", "notes", "api"]);
});

test("Other places: a search shows search hits only, and the destroyed picker ignores a late read", async () => {
  const t = env();
  t.server.roots = others("/srv/api");
  const p = t.make({ otherPlaces: () => true });
  fire(p.input, "focus");
  await flush();
  t.server.search = [{ rel: "work/culipulse", path: "/home/me/work/culipulse" }];
  type(p, "culi");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(headings(p), []);
  assert.deepStrictEqual(rowsOf(p), ["culipulse"]);
  const q = t.make({ otherPlaces: () => true });
  fire(q.input, "focus");
  q.picker.destroy();
  await flush();
  assert.strictEqual(q.list.hidden, true);
});

// --- slice 4 (#343): the flat picker: full paths, added roots, recents, the footer, name matches -----------------------------

const wsp = () => env().box;
const twoOf = (row) => row.children.find((c) => cls(c).includes("wsp-two"));
const nameOf = (row) => { const t = twoOf(row); return t ? t.children[0].textContent : row.children[0].textContent; };
const pathOf = (row) => { const t = twoOf(row); return t ? t.children[1].textContent : null; };
const tagOf = (row) => { const t = row.children.find((c) => cls(c).includes("wsp-last")); return t ? t.textContent : ""; };
const names = (p) => optionRows(p).map(nameOf);
const footOf = (p) => { const n = p.list.children.filter((c) => c.attrs["aria-disabled"] === "true"); return n.length ? n[n.length - 1].textContent : ""; };
// A page with home /home/me, its two folders, and one added root.
function flat(extra) {
  const t = env();
  t.win.HOME_DIR = "/home/me";
  t.win.WORKSPACE_ROOTS = ["/home/me", "/srv/code/proj"];
  t.box.workspacesCache = [{ label: "~ (home)", path: "/home/me" }, { label: "culishop", path: "/home/me/culishop" }, { label: "notes", path: "/home/me/notes" }];
  return Object.assign(t, extra || {});
}

test("wsShownPath: ~/rest under home, home itself and anything else absolute", () => {
  const { wsShownPath } = wsp();
  assert.strictEqual(wsShownPath("/home/me/a/b", "/home/me"), "~/a/b");
  assert.strictEqual(wsShownPath("/home/me/a", "/home/me/"), "~/a", "a trailing slash on home is ignored");
  assert.strictEqual(wsShownPath("/home/me", "/home/me"), "/home/me");
  assert.strictEqual(wsShownPath("/home/meX/a", "/home/me"), "/home/meX/a", "a sibling with the same prefix is not under home");
  assert.strictEqual(wsShownPath("/srv/x", "/home/me"), "/srv/x");
  assert.strictEqual(wsShownPath("/srv/x", ""), "/srv/x");
  assert.strictEqual(wsShownPath("/srv/x", "/"), "/srv/x", "a root home abbreviates nothing");
});

test("wsRecentsRead: the stored list, deduped and cut to 5; empty or damaged falls back to the last spawn's folder", () => {
  const { wsRecentsRead } = wsp();
  const raw = JSON.stringify({ v: 1, paths: ["/a", "/b/", "/b", "rel", 7, "/c", "/d", "/e", "/f"] });
  assert.deepStrictEqual(Array.from(wsRecentsRead(raw, "/z")), ["/a", "/b/", "/c", "/d", "/e"]);
  assert.deepStrictEqual(Array.from(wsRecentsRead(null, "/z")), ["/z"]);
  assert.deepStrictEqual(Array.from(wsRecentsRead("{oops", "/z")), ["/z"]);
  assert.deepStrictEqual(Array.from(wsRecentsRead(JSON.stringify({ v: 1, paths: [] }), "/z")), ["/z"]);
  assert.deepStrictEqual(Array.from(wsRecentsRead(JSON.stringify({ v: 2, paths: ["/a"] }), "")), [], "an unknown version is not read");
  assert.deepStrictEqual(Array.from(wsRecentsRead(null, "relative")), []);
  assert.deepStrictEqual(Array.from(wsRecentsRead(null, undefined)), []);
});

test("wsRecentsPush: the folder goes first, once, and the list keeps 5", () => {
  const { wsRecentsPush } = wsp();
  assert.deepStrictEqual(Array.from(wsRecentsPush(["/a", "/b", "/c"], "/b/")), ["/b/", "/a", "/c"]);
  assert.deepStrictEqual(Array.from(wsRecentsPush(["/a", "/b", "/c", "/d", "/e"], "/f")), ["/f", "/a", "/b", "/c", "/d"]);
  assert.deepStrictEqual(Array.from(wsRecentsPush(["/a"], "")), ["/a"]);
  assert.deepStrictEqual(Array.from(wsRecentsPush(["/a"], "rel")), ["/a"]);
  assert.deepStrictEqual(Array.from(wsRecentsPush(null, "/a")), ["/a"]);
});

test("wsNameMatches: names that contain the query, case-insensitive, the ones that start with it first", () => {
  const { wsNameMatches } = wsp();
  const rows = [{ label: "billing-api" }, { label: "notes" }, { label: "API-server" }, { label: "rapid" }];
  assert.deepStrictEqual(Array.from(wsNameMatches(rows, "api"), (r) => r.label), ["API-server", "billing-api", "rapid"]);
  assert.deepStrictEqual(Array.from(wsNameMatches(rows, "  "), (r) => r.label), []);
  assert.deepStrictEqual(Array.from(wsNameMatches(rows, "zzz"), (r) => r.label), []);
});

test("paths: idle rows are two lines, the name and its path, and the added roots follow the folders, tagged", () => {
  const t = flat();
  const p = t.make({ paths: () => true, inline: () => true });
  fire(p.input, "focus");
  assert.deepStrictEqual(names(p), ["~ (home)", "culishop", "notes", "proj"]);
  assert.deepStrictEqual(optionRows(p).map(pathOf), ["/home/me", "~/culishop", "~/notes", "/srv/code/proj"]);
  assert.deepStrictEqual(optionRows(p).map(tagOf), ["", "", "", "Workspace root"]);
  const two = twoOf(optionRows(p)[1]);
  assert.ok(cls(two.children[1]).includes("muted"), "the path is dimmed");
  assert.strictEqual(optionRows(p)[3].title, "/srv/code/proj");
  fire(optionRows(p)[3], "click");
  assert.strictEqual(p.picker.value(), "/srv/code/proj", "an added root is picked like any row");
});

test("paths: a root that is home, already a row, or filtered out adds no row; without paths there is no root row (the phone)", () => {
  const t = flat();
  t.win.WORKSPACE_ROOTS = ["/home/me", "/home/me/notes", "/srv/a", "/mnt/b", "/srv/a"];
  const p = t.make({ paths: () => true, inline: () => true, filter: (x) => x !== "/mnt/b" });
  fire(p.input, "focus");
  assert.deepStrictEqual(names(p), ["~ (home)", "culishop", "notes", "a"]);
  const phone = t.make({ inline: () => false });
  fire(phone.input, "focus");
  assert.deepStrictEqual(rowsOf(phone), ["~ (home)", "culishop", "notes"]);
  assert.strictEqual(twoOf(phone.list.children[0]), undefined, "the phone keeps its one-line rows");
});

test("paths, not inline (the profile page): legacy rows get the two lines too, and no footer", () => {
  const t = flat();
  const p = t.make({ paths: () => true });
  fire(p.input, "focus");
  const r = optionRows(p);
  assert.ok(cls(r[0]).includes("ft-opt"));
  assert.deepStrictEqual(r.map(pathOf), ["/home/me", "~/culishop", "~/notes", "/srv/code/proj"]);
  assert.strictEqual(footOf(p), "");
});

test("paths + inline: the idle list ends with the footer, which the arrows skip and Enter cannot take", () => {
  const t = flat();
  const p = t.make({ paths: () => true, inline: () => true });
  fire(p.input, "focus");
  assert.strictEqual(footOf(p), "Type to search · or a full path (/…)");
  const foot = p.list.children[p.list.children.length - 1];
  assert.ok(cls(foot).includes("wsp-foot"));
  for (let i = 0; i < 5; i++) fire(p.input, "keydown", { key: "ArrowDown" });
  assert.strictEqual(p.input.attrs["aria-activedescendant"], optionRows(p)[0].id, "five downs wrap over the four rows, never the footer");
});

test("recents: a Recent group comes first, Last used on the last spawn's folder, then a Folders heading", () => {
  const t = flat();
  const p = t.make({
    paths: () => true, inline: () => true, lastUsed: () => "/home/me/culishop/api",
    recents: () => ["/home/me/culishop/api", "/srv/code/proj", "/home/me/notes"],
  });
  fire(p.input, "focus");
  assert.deepStrictEqual(headings(p), ["Recent", "Folders"]);
  assert.deepStrictEqual(names(p), ["api", "proj", "notes", "~ (home)", "culishop", "notes", "proj"]);
  assert.deepStrictEqual(optionRows(p).slice(0, 3).map(pathOf), ["~/culishop/api", "/srv/code/proj", "~/notes"]);
  assert.deepStrictEqual(optionRows(p).map(tagOf), ["Last used", "", "", "", "", "", "Workspace root"]);
  assert.strictEqual(p.list.children[0].textContent, "Recent", "the heading is the first thing in the list");
  fire(p.input, "keydown", { key: "ArrowDown" });
  fire(p.input, "keydown", { key: "Enter" });
  assert.strictEqual(p.picker.value(), "/home/me/culishop/api");
});

test("recents: Last used only when the first recent is the last spawn's; the filter applies; none means no headings", () => {
  const t = flat();
  const p = t.make({ paths: () => true, inline: () => true, lastUsed: () => "/elsewhere", recents: () => ["/home/me/notes", "/mnt/hidden"], filter: (x) => x !== "/mnt/hidden" });
  fire(p.input, "focus");
  assert.deepStrictEqual(names(p).slice(0, 2), ["notes", "~ (home)"]);
  assert.strictEqual(tagOf(optionRows(p)[0]), "");
  const q = t.make({ paths: () => true, inline: () => true, recents: () => [] });
  fire(q.input, "focus");
  assert.deepStrictEqual(headings(q), []);
  assert.deepStrictEqual(names(q), ["~ (home)", "culishop", "notes", "proj"]);
});

test("recents: the root heading and Other places still follow; the folders are not reordered by Last used", async () => {
  const t = flat();
  t.server.roots = others("/opt/web");
  const p = t.make({ paths: () => true, inline: () => true, otherPlaces: () => true, lastUsed: () => "/home/me/notes", recents: () => ["/home/me/notes"] });
  fire(p.input, "focus");
  await flush();
  assert.deepStrictEqual(headings(p), ["Recent", "Folders", "Other places"]);
  assert.deepStrictEqual(names(p), ["notes", "~ (home)", "culishop", "notes", "proj", "web"]);
  assert.strictEqual(pathOf(optionRows(p)[5]), "/opt/web", "an other place shows its full path");
  assert.strictEqual(p.calls.note[p.calls.note.length - 1][0], "4 project roots · type to filter · ↵ to pick", "the caption still counts the folders only");
});

test("search with paths: roots and other places whose name matches come first, tagged; hits show full paths; one row per path", async () => {
  const t = flat();
  t.win.WORKSPACE_ROOTS = ["/home/me", "/srv/code/proj", "/srv/api-gw"];
  t.server.roots = others("/opt/api-server", "/opt/web");
  const p = t.make({ paths: () => true, inline: () => true, otherPlaces: () => true });
  fire(p.input, "focus");
  await flush();
  t.server.search = [
    { rel: "billing-api", path: "/home/me/billing-api" },
    { rel: "svc-api", path: "/srv/code/proj/svc-api", root: "/srv/code/proj" },
    { rel: "api-gw", path: "/srv/api-gw", root: "/srv/api-gw" },
  ];
  type(p, "api");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(names(p), ["api-gw", "api-server", "billing-api", "svc-api"]);
  assert.deepStrictEqual(optionRows(p).map(pathOf), ["/srv/api-gw", "/opt/api-server", "~/billing-api", "/srv/code/proj/svc-api"]);
  assert.deepStrictEqual(optionRows(p).map(tagOf), ["Workspace root", "Other place", "", ""]);
  assert.strictEqual(footOf(p), "4 matches · ↵ to pick");
  assert.strictEqual(p.input.attrs["aria-activedescendant"], optionRows(p)[0].id, "the first row is the one Enter takes");
});

test("search with paths: one match says so; nothing found, a truncated index and a failure keep today's notes", async () => {
  const t = flat();
  const p = t.make({ paths: () => true, inline: () => true });
  t.server.search = [{ rel: "notes", path: "/home/me/notes" }];
  type(p, "note");
  t.runDebounce();
  await flush();
  assert.strictEqual(footOf(p), "1 match · ↵ to pick");
  t.server.search = [];
  type(p, "zzz");
  t.runDebounce();
  await flush();
  assert.strictEqual(footOf(p), "projects and top-level folders — type a full path");
  type(p, "proj");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(names(p), ["proj"], "the added root is found by its own name although the server has no hit for it");
});

test("search without paths: no root or other-place rows join, and the note is today's", async () => {
  const t = flat();
  const p = t.make({ inline: () => true });
  t.server.search = [{ rel: "work/notes", path: "/home/me/work/notes" }];
  type(p, "proj");
  t.runDebounce();
  await flush();
  assert.deepStrictEqual(rowsOf(p), ["notes"]);
  assert.strictEqual(p.list.children.filter((c) => c.attrs["aria-disabled"] === "true").length, 0);
});
