"use strict";
// The real editortab.js (and the real pure modules it calls: the registry, monacokeys, editorref, fsedit, ...) in a vm,
// against a fake page (fakedom.js) and a fake Monaco, with fsRead and fsWrite stubbed. It proves the contracts the dock and
// the later tasks lean on: what a save sends and what it does to the buffer, what a 409 leaves alone, when the host is told
// of an edit, and what dispose() lets go of. How Monaco itself behaves is the browser check's (the task report); the fake
// only has to be as exact as the calls the class makes: the alternative version id moves on every change, getValue takes
// (eol, preserveBOM), onDidChangeContent returns something with dispose().
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node, Text } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// A real replaceChildren and append take strings (the note's sentence is one); the shared fake takes nodes only.
const nodes = (cs) => cs.map((c) => (typeof c === "string" ? new Text(c) : c));
class El extends Node {
  addEventListener(type, fn) { (this.listeners = this.listeners || {})[type] = fn; }
  replaceChildren(...cs) { super.replaceChildren(...nodes(cs)); }
  append(...cs) { super.append(...nodes(cs)); }
}

// A model as the class uses it. `type(text)` is a user's edit: new text, a new alternative version id, the listeners told.
function fakeModel(text, lang) {
  return {
    value: text, alt: 1, disposed: false, listeners: [], getValueCalls: [],
    getAlternativeVersionId() { return this.alt; },
    getValue(eol, bom) { this.getValueCalls.push([eol, bom]); return this.value; },
    fire() { for (const l of [...this.listeners]) l({}); },
    type(v) { this.value = v; this.alt++; this.fire(); },
    undoTo(alt, v) { this.value = v; this.alt = alt; this.fire(); }, // an undo walks back to an earlier id
    setValue(v) { this.value = v; this.alt++; this.fire(); },
    onDidChangeContent(fn) { this.listeners.push(fn); return { dispose: () => { this.listeners = this.listeners.filter((x) => x !== fn); } }; },
    onDidChangeOptions() { return { dispose() {} }; },
    getOptions() { return { insertSpaces: true, tabSize: 4 }; },
    getEOL() { return "\n"; },
    getLanguageId() { return lang; },
    getLineCount() { return this.value.split("\n").length; },
    getLineLength(n) { return (this.value.split("\n")[n - 1] || "").length; },
    dispose() { this.disposed = true; this.listeners = []; },
  };
}

function fakeEditor() {
  return {
    model: null, commands: [], options: [],
    setModel(m) { this.model = m; },
    getModel() { return this.model; },
    addCommand(keys, fn) { this.commands.push([keys, fn]); },
    onDidChangeCursorPosition() { return { dispose() {} }; },
    updateOptions(o) { this.options.push(o); },
    layout() {}, saveViewState() { return { saved: true }; }, restoreViewState() {},
    getScrollTop() { return 0; }, setScrollTop() {}, getPosition() { return { lineNumber: 1, column: 1 }; },
    setPosition() {}, revealLineInCenter() {}, focus() {},
  };
}

// One page per test: the editor, the owner and the loaded Monaco are the script's own top-level lets.
function page(over) {
  const doc = { activeElement: null };
  doc.createElement = (t) => new El(t, doc);
  doc.getElementById = () => null;
  const st = { writes: [], reads: [], toasts: [], models: [], editors: [], host: [], gitRefresh: 0, timers: [] };
  const monaco = {
    KeyCode: { KeyS: 49, KeyZ: 56 },
    KeyMod: { CtrlCmd: 2048, WinCtrl: 256, Shift: 1024, Alt: 512 },
    languages: { getLanguages: () => [{ id: "go", aliases: ["Go"] }, { id: "plaintext", aliases: ["Plain Text"] }] },
    editor: {
      EndOfLinePreference: { TextDefined: 0 }, ScrollType: { Immediate: 1 },
      createModel(text, lang) { const m = fakeModel(text, lang); st.models.push(m); return m; },
      create() { const e = fakeEditor(); st.editors.push(e); return e; },
    },
  };
  let read = { ok: true, status: 200, data: { kind: "text", content: "package web\n", version: "v1" } };
  let write = () => Promise.resolve({ ok: true, status: 200, data: { path: "/p/a.go", version: "v2" } });
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String,
    document: doc,
    setTimeout: (fn, ms) => { st.timers.push({ fn, ms, dead: false }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers[id - 1]) st.timers[id - 1].dead = true; },
    loadMonaco: () => Promise.resolve(monaco),
    MONACO_THEME: "culibox",
    fsRead: async (p) => { st.reads.push(p); return read; },
    fsWrite: (p, body) => { st.writes.push(body); return write(body); },
    boxFileURL: (sid, p) => "/box/" + sid + p,
    fsAlternates: async () => [],
    // The dock's repoint (filesdrawer.js dockRepointViewer) moves the tab to the new path in place.
    repointViewer: (tab, p2) => { tab.path = p2; tab.name = p2.split("/").pop(); return true; },
    // viewer-tab.js draws these for both viewers (alt-builders.test.js runs the real ones); here only what is asked of them.
    altNoteNodes: (wt) => ["note:" + wt],
    altPickNode: (list, alt, onPick) => {
      const box = new El("div", doc);
      box.className = "alts";
      for (const it of list) { const b = new El("button", doc); b.onclick = () => onPick(it); box.append(b); }
      return box;
    },
    closeTabEverywhere: () => {},
    normalizePath: (p) => p,
    clampScroll: (v) => { const n = Math.round(Number(v)); return Number.isFinite(n) && n > 0 ? n : 0; },
    svgIcon: () => new El("svg", doc),
    showToast: (m, o) => { st.toasts.push([m, o]); },
    gitStatusRefreshSoon: () => { st.gitRefresh++; },
    fontPrefs: { get: () => 12.5, STEP: 1, BOUNDS: { min: 8, max: 32 }, set() {} },
    viewerPrefs: { getWrap: () => false },
    toggleViewerWrap: () => {},
    ViewerTab: class {
      constructor(p, o) { this.path = p; this.opts = o; this.calls = []; this.el = new El("div", doc); this.scroll = 7; st.inner = this; }
      onShow() { this.calls.push("show"); } onAttach() { this.calls.push("attach"); } onHide() { this.calls.push("hide"); }
      dispose() { this.calls.push("dispose"); } focusReader() {} revealLine() {} wrapAnchor() { return () => {}; } applyWrap() {}
    },
  }, over));
  const load = (f) => new vm.Script(src(f), { filename: f }).runInContext(ctx);
  for (const f of ["shortcuts.js", "monacokeys.js", "monacolang.js", "editorref.js", "editorstatus.js", "fsedit.js", "copyfile.js", "viewerpath.js", "viewerlines.js", "editortab.js"]) load(f);
  const host = {
    onEditStart: () => st.host.push("start"),
    onDirtyChange: () => st.host.push("dirty"),
  };
  // A class or a let at a script's top level is not a property of the vm's global: read it by name, in it.
  const get = (name) => new vm.Script(name, { filename: "get" }).runInContext(ctx);
  const EditorTab = get("EditorTab");
  return {
    ctx, st, host, doc, monaco, get, EditorTab,
    setRead: (r) => { read = r; },
    setWrite: (fn) => { write = fn; },
    // A tab read, with Monaco loaded, and on screen (so it owns the editor).
    async open(p, o) {
      const tab = new EditorTab(p || "/p/a.go", Object.assign({ host }, o));
      for (let i = 0; i < 50 && !tab.model && !tab.inner && !tab.disposed; i++) await new Promise((r) => setImmediate(r));
      tab.onAttach();
      return tab;
    },
  };
}

const keydown = (over) => Object.assign({
  ctrlKey: true, metaKey: false, altKey: false, shiftKey: false, code: "KeyS", key: "s", defaultPrevented: false,
  preventDefault() { this.defaultPrevented = true; }, stopPropagation() {},
}, over);

test("a tab is read once, its model holds the text, and it starts clean and on screen", async () => {
  const p = page();
  const tab = await p.open();
  assert.equal(tab.mode, "edit");
  assert.equal(tab.model.value, "package web\n");
  assert.equal(tab.version, "v1");
  assert.equal(tab.dirty(), false);
  assert.equal(p.get("dockEditorOwner"), tab, "it owns the shared editor");
  assert.equal(p.st.editors.length, 1, "one editor for the dock");
  assert.equal(p.st.editors[0].getModel(), tab.model);
  assert.equal(p.st.models.length, 1);
  assert.deepEqual(p.st.host, [], "reading is not an edit");
  const second = await p.open("/p/b.go");
  assert.equal(p.st.editors.length, 1, "a second tab shares the editor");
  assert.equal(p.get("dockEditorOwner"), second);
  assert.equal(p.st.editors[0].getModel(), second.model, "and its model is the one shown");
  assert.equal(tab.model.disposed, false, "the first tab keeps its model while it is hidden");
});

test("save: one write with base_version = the read's version, the buffer text and the BOM kept; then clean and the new version kept", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("package web // edited\n");
  assert.equal(tab.dirty(), true);
  await tab.save();
  assert.equal(p.st.writes.length, 1, "exactly one write");
  assert.deepEqual(p.st.writes[0], { path: "/p/a.go", content: "package web // edited\n", base_version: "v1", overwrite: false });
  assert.deepEqual(tab.model.getValueCalls.at(-1), [0, true], "TextDefined line endings, preserveBOM");
  assert.equal(tab.dirty(), false, "the dot clears");
  assert.equal(tab.version, "v2", "the server's version is the next base_version");
  assert.equal(tab.diskText, "package web // edited\n");
  assert.equal(tab.statusText, "Saved");
  assert.equal(p.st.timers.length, 1);
  assert.equal(p.st.timers[0].ms, 2000, "Saved stays for 2000 ms");
  p.st.timers[0].fn();
  assert.equal(tab.statusText, "", "and then goes");
  assert.equal(p.st.gitRefresh, 1, "the git marks are asked again");
  assert.deepEqual(p.st.toasts, []);
  await tab.save();
  assert.equal(p.st.writes.length, 1, "a clean buffer is not written again");
  tab.model.type("again\n");
  await tab.save();
  assert.equal(p.st.writes[1].base_version, "v2", "the next save is guarded by the version the last one returned");
});

test("save: the editor's Mod+S command and a Mod+S inside the tab each save once, and a key Monaco already handled does not save twice", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("x\n");
  const ed = p.st.editors[0];
  const cmd = ed.commands.find(([keys]) => keys === (49 | 2048));
  assert.ok(cmd, "the editor has a command on Mod+S, from the registry's chord");
  cmd[1](); // the command does not return the save's promise: let it settle
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.writes.length, 1);
  tab.model.type("y\n");
  const e = keydown();
  tab.el.listeners.keydown(e);
  assert.equal(e.defaultPrevented, true, "the tab takes a Mod+S from anywhere inside it (the browser's Save page is not wanted)");
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.writes.length, 2);
  tab.model.type("z\n");
  tab.el.listeners.keydown(keydown({ defaultPrevented: true }));
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.writes.length, 2, "a press Monaco handled (and prevented) is not a second save");
});

test("a 409 writes nothing more, keeps the buffer dirty and the old version, and hands the new disk version to the conflict bar", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("mine\n");
  const alt = tab.savedAlt;
  const bars = [];
  tab.conflictBar = (v) => bars.push(v); // Task 6's seam
  p.setWrite(() => Promise.resolve({ ok: false, status: 409, data: { error: "file changed on disk", current_version: "v9" } }));
  await tab.save();
  assert.equal(p.st.writes.length, 1, "one attempt, no retry, no overwrite");
  assert.equal(p.st.writes[0].overwrite, false);
  assert.equal(tab.dirty(), true);
  assert.equal(tab.version, "v1", "the base version is not moved: Keep mine is the user's choice");
  assert.equal(tab.savedAlt, alt);
  assert.equal(tab.diskText, "package web\n");
  assert.equal(tab.model.value, "mine\n", "the buffer is untouched");
  assert.equal(tab.statusText, "");
  assert.deepEqual(bars, ["v9"]);
  assert.equal(tab.saving, false, "and the next press may try again");
  await tab.save();
  assert.equal(p.st.writes.length, 2);
  assert.equal(p.st.writes[1].base_version, "v1", "still guarded by the version it was read at");
});

test("until the conflict bar exists a 409 says so in a toast and writes nothing", async () => {
  const p = page();
  const tab = await p.open("/p/conf.go");
  tab.model.type("mine\n");
  p.setWrite(() => Promise.resolve({ ok: false, status: 409, data: { current_version: "v9" } }));
  await tab.save();
  assert.equal(p.st.toasts.length, 1);
  assert.match(p.st.toasts[0][0], /^Not saved: conf\.go changed on disk/);
  assert.equal(p.st.toasts[0][1].error, true);
  assert.equal(tab.dirty(), true);
});

test("any other failed write is an error toast and leaves the buffer dirty on the old version", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("mine\n");
  p.setWrite(() => Promise.resolve({ ok: false, status: 500, data: { error: "cannot save" } }));
  await tab.save();
  assert.deepEqual(p.st.toasts, [["cannot save", { error: true }]]);
  assert.equal(tab.dirty(), true);
  assert.equal(tab.version, "v1");
  p.setWrite(() => Promise.resolve({ ok: false, status: 0, data: null }));
  await tab.save();
  assert.equal(p.st.toasts.at(-1)[0], "could not reach the server");
});

test("an edit made while a save is in flight stays dirty after it resolves, and a second press does not write twice", async () => {
  const p = page();
  const tab = await p.open();
  let release;
  p.setWrite(() => new Promise((r) => { release = r; }));
  tab.model.type("first\n");
  const savedAlt = tab.model.getAlternativeVersionId();
  const done = tab.save();
  assert.equal(p.st.writes.length, 1);
  assert.equal(tab.saving, true);
  tab.model.type("first then more\n");
  await tab.save();
  assert.equal(p.st.writes.length, 1, "a press while one is in flight is not a second write");
  release({ ok: true, status: 200, data: { path: "/p/a.go", version: "v2" } });
  await done;
  assert.equal(tab.saving, false);
  assert.equal(tab.version, "v2");
  assert.equal(tab.diskText, "first\n", "what is on disk is what was sent");
  assert.equal(tab.savedAlt, savedAlt, "the saved mark is the buffer as it was sent");
  assert.equal(tab.dirty(), true, "the later edit is not lost to the dot");
  p.setWrite(() => Promise.resolve({ ok: true, status: 200, data: { version: "v3" } }));
  await tab.save();
  assert.deepEqual([p.st.writes[1].content, p.st.writes[1].base_version], ["first then more\n", "v2"]);
  assert.equal(tab.dirty(), false);
});

test("an undo back to the saved text is clean again, and tells the host without pinning", async () => {
  const p = page();
  const tab = await p.open();
  const clean = tab.model.getAlternativeVersionId();
  tab.model.type("x\n");
  assert.equal(tab.dirty(), true);
  tab.model.undoTo(clean, "package web\n");
  assert.equal(tab.dirty(), false);
  assert.deepEqual(p.st.host, ["start", "dirty", "dirty"]);
});

test("read mode never writes: save says why, and typing is not a dirty buffer", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: "scratch\n", version: "v1", readonly: true } });
  const tab = await p.open("/tmp/claude-1/note.txt");
  assert.equal(tab.mode, "read");
  assert.equal(tab.readOnly, true);
  tab.model.type("x\n");
  assert.equal(tab.dirty(), false);
  await tab.save();
  tab.el.listeners.keydown(keydown());
  await new Promise((r) => setImmediate(r));
  assert.equal(p.st.writes.length, 0);
  assert.equal(p.st.toasts.length, 2);
  assert.match(p.st.toasts[0][0], /read-only/);
  assert.deepEqual(p.st.host, [], "an edit that cannot be saved does not pin the tab or draw a dot");
  assert.deepEqual(p.st.editors[0].options.at(-1).readOnly, true, "Monaco is read-only over it");
  // A file in a box is read-only before anything is read from it.
  const box = new p.EditorTab("/work/out.txt", { box: "s1", host: p.host });
  assert.equal(box.readOnly, true);
});

test("the first typed edit tells the host to pin the tab BEFORE it draws the dot; later edits and a save tell it no more than the flips", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("a\n");
  assert.deepEqual(p.st.host, ["start", "dirty"]);
  tab.model.type("ab\n");
  tab.model.type("abc\n");
  assert.deepEqual(p.st.host, ["start", "dirty"], "already dirty: nothing new to say");
  await tab.save();
  assert.deepEqual(p.st.host, ["start", "dirty", "dirty"], "the save flips it clean: a dot update, never a pin");
  tab.model.type("d\n");
  assert.deepEqual(p.st.host, ["start", "dirty", "dirty", "start", "dirty"], "the next edit pins again (a no-op for a pinned tab) and flips dirty");
});

test("discardEdits and applyText put text in as the clean state: not an edit, so nothing is pinned", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("typed\n");
  p.st.host.length = 0;
  tab.discardEdits();
  assert.equal(tab.model.value, "package web\n", "back to what is on disk");
  assert.equal(tab.dirty(), false);
  assert.deepEqual(p.st.host, ["dirty"], "the dot goes; no onEditStart");
  p.st.host.length = 0;
  tab.applyText("from the agent\n");
  assert.equal(tab.dirty(), false);
  assert.equal(tab.diskText, "from the agent\n");
  assert.equal(tab.savedAlt, tab.model.getAlternativeVersionId());
  assert.deepEqual(p.st.host, [], "a clean tab stays clean: nothing to tell");
  tab.discardEdits();
  assert.equal(tab.model.value, "from the agent\n", "a discard of nothing changes nothing");
});

test("generation moves with every edit, and is back after an undo: the dock's Cancel stands only while it holds", async () => {
  const p = page();
  const tab = await p.open();
  const g0 = tab.generation();
  tab.model.type("x\n");
  const g1 = tab.generation();
  assert.notEqual(g1, g0);
  tab.model.type("xy\n");
  assert.notEqual(tab.generation(), g1);
  tab.model.undoTo(g1, "x\n");
  assert.equal(tab.generation(), g1);
});

test("dispose() disposes the model, takes it out of the shared editor and stops hearing edits", async () => {
  const p = page();
  const tab = await p.open();
  const model = tab.model;
  const ed = p.st.editors[0];
  tab.dispose();
  assert.equal(model.disposed, true);
  assert.equal(tab.model, null);
  assert.equal(ed.getModel(), null, "the shared editor shows nothing rather than a dead model");
  assert.equal(p.get("dockEditorOwner"), null);
  assert.equal(tab.dirty(), false);
  model.type("late\n");
  assert.deepEqual(p.st.host, [], "a disposed tab tells the host nothing");
  await tab.save();
  assert.equal(p.st.writes.length, 0);
  tab.dispose(); // twice is fine
});

test("disposing a hidden tab leaves the shown tab's model in the editor", async () => {
  const p = page();
  const a = await p.open("/p/a.go");
  const b = await p.open("/p/b.go");
  assert.equal(p.get("dockEditorOwner"), b);
  a.dispose();
  assert.equal(a.model, null);
  assert.equal(p.st.editors[0].getModel(), b.model);
  assert.equal(p.get("dockEditorOwner"), b);
  assert.equal(b.model.disposed, false);
});

test("a dirty tab kept detached keeps its model and its edit: hiding stashes the view and disposes nothing", async () => {
  const p = page();
  const a = await p.open("/p/a.go");
  a.model.type("kept\n");
  a.onHide();
  assert.equal(a.model.disposed, false);
  assert.equal(a.dirty(), true);
  assert.deepEqual(a.viewState, { saved: true }, "the view was saved on the way out");
  a.onAttach();
  assert.equal(p.st.editors[0].getModel(), a.model);
});

test("an image mounts a ViewerTab without a read; the tab forwards the lifecycle and has no model, bar or dirt", async () => {
  const p = page();
  const tab = await p.open("/p/pic.png", { scroll: 40 });
  assert.equal(p.st.reads.length, 0, "an image is known by its extension: no read");
  assert.equal(tab.mode, "image");
  assert.equal(tab.inner, p.st.inner);
  assert.equal(tab.model, null);
  assert.equal(tab.dirty(), false);
  assert.equal(tab.scroll, 7, "the dock's stored scroll is the viewer's");
  assert.deepEqual(tab.inner.calls, ["attach"], "attached when the dock attached the tab (onShow is the dock's own call)");
  tab.onHide();
  tab.dispose();
  assert.deepEqual(tab.inner.calls, ["attach", "hide", "dispose"]);
  assert.equal(p.st.editors.length, 0, "no editor was made for it");
  assert.equal(p.st.models.length, 0);
});

test("a read that fails paints the server's sentence, makes no model and no bar", async () => {
  const p = page();
  p.setRead({ ok: false, status: 413, data: { error: "file too large to preview" } });
  const tab = await p.open("/p/big.log");
  assert.equal(tab.model, null);
  assert.equal(tab.bodyEl.children[0].textContent, "file too large to preview");
  assert.equal(tab.dirty(), false);
  assert.equal(p.st.models.length, 0);
});

test("a restored tab whose file is gone closes itself on the first read (404, and only 404)", async () => {
  const closed = [];
  const p = page({ closeTabEverywhere: (t) => closed.push(t) });
  p.setRead({ ok: false, status: 404, data: { error: "not found" } });
  const gone = await p.open("/p/gone.go", { restored: true });
  assert.deepEqual(closed, [gone]);
  const asked = await p.open("/p/asked.go");
  assert.equal(closed.length, 1, "a tab somebody asked for keeps its error");
  assert.equal(asked.bodyEl.children[0].textContent, "not found");
  p.setRead({ ok: false, status: 0, data: null });
  await p.open("/p/offline.go", { restored: true });
  assert.equal(closed.length, 1, "status 0 says nothing about the file");
});

// #53: a relative terminal link that is not in this checkout may be in another one.
test("a 404 with a relative link looks in the other checkouts: one copy re-points the tab and reads it, with a note, and the link's line still waiting", async () => {
  const asked = [];
  const p = page({ fsAlternates: async (base, rel) => { asked.push([base, rel]); return [{ path: "/r/wt/x.go", worktree: "wt", mtime: 1 }]; } });
  p.setRead({ ok: false, status: 404, data: { error: "not found" } });
  const tab = new p.EditorTab("/r/x.go", { host: p.host, alt: { base: "/r", rel: "x.go" }, line: 3, col: 2 });
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  assert.deepEqual(asked, [["/r", "x.go"]]);
  assert.equal(tab.path, "/r/wt/x.go", "re-pointed at the copy");
  assert.deepEqual(p.st.reads, ["/r/x.go", "/r/wt/x.go"], "and read again from there");
  assert.equal(tab.altEl.hidden, false);
  assert.equal(tab.altEl.textContent, "note:wt", "the note says where it came from");
  assert.deepEqual(tab.pendingLine, { line: 3, col: 2 }, "the line the link asked for waits for the editor");
  assert.equal(tab.alt, null, "the lookup is for the first read only");
});

test("a 404 with a relative link and several other copies lists them; a press opens that one", async () => {
  const p = page({ fsAlternates: async () => [{ path: "/r/a/x.go", worktree: "a", mtime: 2 }, { path: "/r/b/x.go", worktree: "b", mtime: 1 }] });
  p.setRead({ ok: false, status: 404, data: { error: "not found" } });
  const tab = new p.EditorTab("/r/x.go", { host: p.host, alt: { base: "/r", rel: "x.go" } });
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  const list = tab.bodyEl.children[0];
  assert.equal(list.className, "alts");
  assert.equal(list.children.length, 2);
  assert.equal(tab.path, "/r/x.go", "nothing opened until one is picked");
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: "b\n", version: "vb" } });
  list.children[1].click();
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  assert.equal(tab.path, "/r/b/x.go");
  assert.equal(tab.model.value, "b\n");
  assert.equal(tab.version, "vb");
});

test("a 404 with a relative link and no other copy is the ordinary not found", async () => {
  const p = page({ fsAlternates: async () => [] });
  p.setRead({ ok: false, status: 404, data: { error: "not found" } });
  const tab = new p.EditorTab("/r/x.go", { host: p.host, alt: { base: "/r", rel: "x.go" } });
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  assert.equal(tab.bodyEl.children[0].textContent, "not found");
  assert.equal(tab.model, null);
});
