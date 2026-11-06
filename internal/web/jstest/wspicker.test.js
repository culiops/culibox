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
  const server = { search: [], list: {}, searches: [], lists: [] };
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
      if (u.pathname === "/api/fs/search") {
        server.searches.push(u.searchParams.get("q"));
        const hits = server.search;
        return { ok: true, status: 200, json: async () => ({ results: hits, truncated: false }) };
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
  // 400 / 404 / 403: a toast in words.
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
    ["That path is outside your home directory", true],
  ]);
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
