"use strict";
// The real editortab.js, editorsync.js and editordiff.js (and the real pure modules they call: the registry, monacokeys, editorref,
// fsedit, linediff, ...) in a vm, against a fake page (fakedom.js) and a fake Monaco, with fsRead and fsWrite stubbed. It proves the contracts
// the dock and the later tasks lean on: what a save sends and what it does to the buffer, what a 409 leaves alone, when the
// host is told of an edit, what dispose() lets go of, and (the second half) what happens when the file changes on disk under
// the buffer: the poll, the reload of a clean buffer, the conflict bar over a dirty one, and the races between them; and (the third
// half) Changes vs HEAD: the gutter drawn from a stubbed POST /api/git/show, the inline Changes view and what it lets go of. How
// Monaco itself behaves is the browser check's (the task report); the fake only has to be as exact as the calls the code makes:
// the alternative version id moves on every change, getValue takes (eol, preserveBOM), onDidChangeContent returns something
// with dispose(), a replace of the whole text (setValue) sends a cursor and a scroll back to the top, which is what the code
// has to undo, and it also clears the undo history, as Monaco's does (TextModel._setValueFromTextBuffer): the fake model keeps
// an undo stack of its own, so a test can press undo and see what comes back.
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

// A decoration store as a model or an editor keeps them: deltaDecorations(old ids, new) removes the old and returns an id per new.
function decorStore() {
  let n = 0;
  const live = new Map();
  return {
    live,
    delta(old, next) {
      for (const id of old) live.delete(id);
      return next.map((d) => { const id = "d" + (++n); live.set(id, d); return id; });
    },
    list: () => [...live.values()],
  };
}

// A model as the class uses it. `type(text)` is a user's edit: new text, a new alternative version id, a step on the undo
// stack, the listeners told. `undo()` is Ctrl+Z: the text and the alternative version id of the step before.
const eolOf = (t) => (/\r\n/.test(t) ? "\r\n" : "\n");
function fakeModel(text, lang, log, id) {
  return {
    value: text, alt: 1, ver: 1, disposed: false, listeners: [], getValueCalls: [], sets: [], undos: [], eol: eolOf(text),
    getAlternativeVersionId() { return this.alt; },
    getValue(eol, bom) { this.getValueCalls.push([eol, bom]); return this.value; },
    fire(e) { for (const l of [...this.listeners]) l(e || {}); },
    type(v) { this.undos.push({ value: this.value, alt: this.alt }); this.value = v; this.alt = ++this.ver; this.fire(); },
    undoTo(alt, v) { this.value = v; this.alt = alt; this.fire(); }, // an undo walks back to an earlier id
    undo() { const step = this.undos.pop(); if (!step) return false; this.value = step.value; this.alt = step.alt; this.fire(); return true; },
    // A new value: the undo history goes (Monaco's _commandManager.clear()), the alternative version id is a new one.
    setValue(v) { this.undos = []; this.value = v; this.alt = ++this.ver; this.eol = eolOf(v); this.sets.push(v); this.fire({ replaced: true }); },
    onDidChangeContent(fn) { this.listeners.push(fn); return { dispose: () => { this.listeners = this.listeners.filter((x) => x !== fn); } }; },
    onDidChangeOptions() { return { dispose() {} }; },
    getOptions() { return { insertSpaces: true, tabSize: 4 }; },
    getEOL() { return this.eol; },
    decor: decorStore(),
    deltaDecorations(old, next) { return this.decor.delta(old, next); },
    getLanguageId() { return lang; },
    getLineCount() { return this.value.split("\n").length; },
    getLineLength(n) { return (this.value.split("\n")[n - 1] || "").length; },
    dispose() { this.disposed = true; this.listeners = []; if (log) log.push("dispose#" + id); },
  };
}

function fakeEditor() {
  return {
    model: null, commands: [], options: [], top: 0, restored: [], decor: decorStore(), focused: 0,
    // A cursor and a scroll are lost to a replace of the text, as Monaco loses them: unless they are put back they are at the top.
    // The decorations this editor made go when its model does (Monaco's _postDetachModelCleanup).
    setModel(m) {
      if (m !== this.model) this.decor.live.clear();
      this.model = m;
      if (m) m.onDidChangeContent((e) => { if (e.replaced) this.top = 0; });
    },
    deltaDecorations(old, next) { return this.decor.delta(old, next); },
    getModel() { return this.model; },
    addCommand(keys, fn) { this.commands.push([keys, fn]); },
    onDidChangeCursorPosition() { return { dispose() {} }; },
    updateOptions(o) { this.options.push(o); },
    layout() {}, saveViewState() { return { saved: true, top: this.top }; }, restoreViewState(v) { this.restored.push(v); this.top = v.top; },
    getScrollTop() { return this.top; }, setScrollTop(v) { this.top = v; }, getPosition() { return { lineNumber: 1, column: 1 }; },
    setPosition() {}, revealLineInCenter() {}, focus() { this.focused++; },
  };
}

// One page per test: the editor, the owner and the loaded Monaco are the script's own top-level lets.
function page(over) {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new El(t, doc);
  doc.getElementById = () => null;
  const st = {
    writes: [], reads: [], urls: [], toasts: [], models: [], editors: [], diffs: [], host: [], closed: [], log: [], gitRefresh: 0,
    timers: [], intervals: [],
    shown: false, page: null, session: "fix-fs-cap", // is the dock on screen; a page view over it; the session the dock shows
    // The page's git, off until a test turns it on (on: the tree's status is known and names HEAD `head`): the scope the Changes
    // section shows, the base it names, and what POST /api/git/show answers (an object, a function of the request body, or null
    // for a request that failed), delivered after `gate` if one is set.
    git: { on: false, head: "h1", scope: "head", base: null, answer: null, gate: null, shows: [] },
    metaGate: null, fullGate: null,                  // a promise a metadata / a full read waits on, after its answer is decided
  };
  const monaco = {
    KeyCode: { KeyS: 49, KeyZ: 56 },
    KeyMod: { CtrlCmd: 2048, WinCtrl: 256, Shift: 1024, Alt: 512 },
    languages: { getLanguages: () => [{ id: "go", aliases: ["Go"] }, { id: "plaintext", aliases: ["Plain Text"] }] },
    editor: {
      EndOfLinePreference: { TextDefined: 0 }, ScrollType: { Immediate: 1 },
      EndOfLineSequence: { LF: 0, CRLF: 1 },
      OverviewRulerLane: { Left: 1, Center: 2, Right: 4, Full: 7 },
      createModel(text, lang) { const m = fakeModel(text, lang, st.log, st.models.length + 1); st.models.push(m); return m; },
      create() { const e = fakeEditor(); st.editors.push(e); return e; },
      createDiffEditor(el, opts) {
        const modified = Object.assign(fakeEditor(), { revealed: [], cursorSubs: [], onDidChangeCursorPosition(fn) { this.cursorSubs.push(fn); return { dispose() {} }; }, revealLineInCenter(l) { this.revealed.push(l); } });
        const d = {
          el, opts, model: undefined, disposed: false, revealed: 0, updated: [], modified, laid: 0, options: [],
          getModifiedEditor() { return this.modified; },
          layout() { this.laid++; },
          updateOptions(o) { this.options.push(o); },
          setModel(m) { this.model = m; st.log.push(m ? "diff.setModel" : "diff.setModel(null)"); },
          onDidUpdateDiff(fn) { this.updated.push(fn); return { dispose: () => { this.updated = this.updated.filter((x) => x !== fn); } }; },
          revealFirstDiff() { this.revealed++; },
          dispose() { this.disposed = true; st.log.push("diff.dispose"); },
        };
        st.diffs.push(d);
        return d;
      },
    },
  };
  let read = { ok: true, status: 200, data: { kind: "text", content: "package web\n", version: "v1" } };
  let write = () => Promise.resolve({ ok: true, status: 200, data: { path: "/p/a.go", version: "v2" } });
  // serve() makes the page's file system one file that the test can change under the tab (the agent) or take away: reads
  // (whole, and the metadata read) and writes (the base_version guard, as fswrite.go has it) answer from it. The version of
  // a text is "h:" + the text. An answer is decided when the request is made and delivered after its gate, if the test set one.
  let disk = null;
  const hash = (t) => "h:" + t;
  const metaUrl = (u) => typeof u === "string" && u.endsWith("&meta=1");
  const diskRead = (url) => {
    if (disk.fail) return { ok: false, status: disk.fail, data: { error: "boom" } };
    if (!disk.exists) return { ok: false, status: 404, data: { error: "not found" } };
    const d = { kind: "text", size: disk.text.length, version: hash(disk.text), readonly: false };
    if (!metaUrl(url)) d.content = disk.text;
    return { ok: true, status: 200, data: d };
  };
  const diskWrite = (body) => {
    if (!disk.exists) return { ok: false, status: 404, data: { error: "not found" } };
    if (!body.overwrite && body.base_version !== hash(disk.text)) return { ok: false, status: 409, data: { error: "file changed on disk", current_version: hash(disk.text) } };
    disk.text = body.content;
    return { ok: true, status: 200, data: { path: body.path, version: hash(disk.text) } };
  };
  const ctx = vm.createContext(Object.assign({
    console, Array, Object, Set, Map, Math, Number, JSON, Date, Promise, String,
    document: doc,
    setTimeout: (fn, ms) => { st.timers.push({ fn, ms, dead: false }); return st.timers.length; },
    clearTimeout: (id) => { if (st.timers[id - 1]) st.timers[id - 1].dead = true; },
    setInterval: (fn, ms) => { st.intervals.push({ fn, ms, dead: false }); return st.intervals.length; },
    clearInterval: (id) => { if (st.intervals[id - 1]) st.intervals[id - 1].dead = true; },
    // What the poll asks the page: is the dock on screen (the dock's own predicate), and is a page view over it.
    drawerOwnsFileClicks: () => st.shown,
    currentPage: () => st.page,
    loadMonaco: () => Promise.resolve(monaco),
    MONACO_THEME: "culibox",
    fsRead: async (p, url) => {
      st.reads.push(p);
      st.urls.push(url);
      if (!disk) return read;
      const answer = diskRead(url);
      const gate = metaUrl(url) ? st.metaGate : st.fullGate;
      if (gate) await gate;
      return answer;
    },
    fsWrite: (p, body) => { st.writes.push(body); return write(body); },
    AbortController,
    ftRoot: () => "/p",
    dockScope: () => st.git.scope,
    gitBaseFor: () => st.git.base,
    gitStatusCurrent: () => (st.git.on ? { root: "/p", status: { state: "ok", head: st.git.head } } : null),
    fetch: async (url, init) => {
      const body = JSON.parse(init.body);
      st.git.shows.push({ url, init, body });
      const a = typeof st.git.answer === "function" ? st.git.answer(body) : st.git.answer;
      if (st.git.gate) await st.git.gate;
      return a === null ? { ok: false, status: 500, json: async () => null } : { ok: true, status: 200, json: async () => a };
    },
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
  for (const f of ["shortcuts.js", "monacokeys.js", "monacolang.js", "editorref.js", "editorstatus.js", "fsedit.js", "copyfile.js", "viewerpath.js", "viewerlines.js", "linediff.js", "editortab.js", "editorsync.js", "editordiff.js"]) load(f);
  const host = {
    onEditStart: () => st.host.push("start"),
    onDirtyChange: () => st.host.push("dirty"),
    sessionName: () => st.session,
    sessionId: () => "s1",
    closeTab: (t) => st.closed.push(t),
  };
  // A class or a let at a script's top level is not a property of the vm's global: read it by name, in it.
  const get = (name) => new vm.Script(name, { filename: "get" }).runInContext(ctx);
  const EditorTab = get("EditorTab");
  const settle = async () => { for (let i = 0; i < 30; i++) await new Promise((r) => setImmediate(r)); };
  return {
    ctx, st, host, doc, monaco, get, EditorTab, settle, hash, diskWrite,
    setRead: (r) => { read = r; },
    setWrite: (fn) => { write = fn; },
    // The file system is one file from now on: p.disk is the file (set .text to play the agent, .exists = false to delete it).
    serve(text) {
      disk = { text: text === undefined ? "package web\n" : text, exists: true };
      write = (body) => Promise.resolve(diskWrite(body));
      return disk;
    },
    // The dock comes on screen or goes off it (a page view over it, the page hidden), and the poll is told as the page tells it.
    setShown(on) { st.shown = on; get("editorSyncSync")(); },
    setPage(on) { st.page = on ? {} : null; get("editorSyncSync")(); },
    setHidden(on) { doc.hidden = on; get("editorSyncSync")(); },
    // One tick of the dock's one interval, and whatever it started, settled.
    async tick() {
      const live = st.intervals.filter((i) => !i.dead);
      assert.equal(live.length, 1, "one interval for the whole dock");
      live[0].fn();
      await settle();
    },
    live: () => st.intervals.filter((i) => !i.dead).length,
    metas: () => st.urls.filter((u) => typeof u === "string" && u.endsWith("&meta=1")),
    fulls: () => st.urls.filter((u) => !(typeof u === "string" && u.endsWith("&meta=1"))),
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

// bar: the pieces of the conflict bar the tests look at.
const bar = (tab) => {
  const [dot, msg, acts] = tab.barEl.children;
  return { dot, msg: msg.textContent, buttons: acts.children, labels: acts.children.map((b) => b.textContent) };
};
const MOD_S = () => keydown();

test("a 409 over a dirty buffer draws the amber bar, not a toast: the session's sentence and its three actions, as real buttons", async () => {
  const p = page();
  const tab = await p.open("/p/conf.go");
  assert.equal(tab.barEl.hidden, true, "no bar until something happens");
  tab.model.type("mine\n");
  p.setWrite(() => Promise.resolve({ ok: false, status: 409, data: { current_version: "v9" } }));
  await tab.save();
  assert.deepEqual(p.st.toasts, [], "a bar, not a toast");
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.barEl.className, "dk-conflict");
  assert.equal(tab.barEl.getAttribute("role"), "alert");
  const b = bar(tab);
  assert.equal(b.msg, "fix-fs-cap changed conf.go after you started editing. Your edits are not saved yet.");
  assert.deepEqual(b.labels, ["Compare", "Keep mine", "Load the agent's version"]);
  assert.ok(b.buttons.every((x) => x.tag === "button" && x.type === "button"), "real buttons, reachable by keyboard");
  assert.equal(tab.sync.bar.diskVersion, "v9", "Keep mine will take the server's version");
  assert.equal(tab.dirty(), true);
  assert.equal(tab.model.value, "mine\n");
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
  assert.deepEqual(a.viewState, { saved: true, top: 0 }, "the view was saved on the way out");
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

// --- the file changes on disk under the buffer (editorsync.js) -------------------------------------------------------------

test("the poll: one interval for the whole dock, every 2000 ms, asking the metadata of the tab on screen, and once when a tab is shown", async () => {
  const p = page();
  p.serve();
  p.setShown(true);
  assert.equal(p.live(), 0, "nothing is on screen yet: nothing is asked");
  const a = await p.open("/p/a.go");
  await p.settle();
  assert.equal(p.live(), 1);
  assert.equal(p.st.intervals[0].ms, 2000);
  assert.deepEqual(p.metas(), ["/api/fs/read?path=%2Fp%2Fa.go&meta=1"], "asked once as the tab was shown");
  await p.tick();
  assert.equal(p.metas().length, 2);
  a.onHide(); // the dock shows another tab: the one that was is hidden first
  assert.equal(p.live(), 0, "no tab on screen between the two calls: no interval");
  await p.open("/p/b.go");
  await p.settle();
  assert.equal(p.live(), 1, "still one interval, not one per tab");
  const n = p.metas().length;
  assert.equal(p.metas()[n - 1], "/api/fs/read?path=%2Fp%2Fb.go&meta=1", "the shown tab is the one asked");
  await p.tick();
  assert.deepEqual(p.metas().slice(n), ["/api/fs/read?path=%2Fp%2Fb.go&meta=1"], "and only that one");
  assert.equal(p.fulls().length, 2, "no text was read for either: only the opens'");
});

test("the poll: nothing is asked while the dock is off screen (closed, under a page view, page hidden), after a hide or a dispose, or for an image; coming back asks at once", async () => {
  const p = page();
  p.serve();
  const tab = await p.open("/p/a.go"); // the dock is not on screen
  await p.settle();
  assert.equal(p.live(), 0);
  assert.equal(p.metas().length, 0);
  let n = 0;
  const back = async (what) => {
    assert.equal(p.live(), 1, what + ": asked again");
    await p.settle();
    assert.equal(p.metas().length, ++n, what + ": and at once");
  };
  p.setShown(true);
  await back("the dock opened");
  p.setPage(true);
  assert.equal(p.live(), 0, "a page view covers the dock");
  p.setPage(false);
  await back("the page view closed");
  p.setHidden(true);
  assert.equal(p.live(), 0, "the page is hidden");
  p.setHidden(false);
  await back("the page is shown");
  p.setShown(false);
  assert.equal(p.live(), 0, "the dock closed");
  p.setShown(true);
  await back("the dock reopened");
  tab.onHide();
  assert.equal(p.live(), 0, "a tab that is hidden is not asked about");
  const img = await p.open("/p/pic.png");
  await p.settle();
  assert.equal(p.live(), 0, "an image has no version: it is never asked about");
  assert.equal(p.metas().length, n);
  img.dispose();
  const box = await p.open("/work/out.txt", { box: "s1" });
  await p.settle();
  assert.equal(p.live(), 1);
  assert.equal(p.metas().at(-1), "/box/s1/work/out.txt&meta=1", "a box file is asked about on its own route");
  box.dispose();
  assert.equal(p.live(), 0, "a tab that is disposed is not asked about");
});

test("a clean buffer is reloaded in place when the agent changes the file: the scroll kept, clean on the new version, no pin", async () => {
  const p = page();
  const disk = p.serve("one\ntwo\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  const ed = p.st.editors[0];
  ed.top = 120; // the user has scrolled
  p.st.host.length = 0;
  const heard = [];
  p.get("editorSyncOnReplace")(tab, (t) => heard.push(t));
  disk.text = "one\ntwo\nthree\n";
  await p.tick();
  assert.equal(tab.model.value, "one\ntwo\nthree\n");
  assert.deepEqual(tab.model.sets, ["one\ntwo\nthree\n"], "one new value, which also clears the undo history");
  assert.equal(ed.top, 120, "the scroll is where it was (the replace sent it to the top)");
  assert.deepEqual(ed.restored, [{ saved: true, top: 120 }], "the view was saved first and put back");
  assert.equal(tab.dirty(), false);
  assert.equal(tab.version, p.hash("one\ntwo\nthree\n"));
  assert.equal(tab.diskText, "one\ntwo\nthree\n");
  assert.deepEqual(p.st.host, [], "not the user's edit: nothing to pin, no dot");
  assert.deepEqual(heard, ["one\ntwo\nthree\n"], "whoever asked to hear of a replace (P3) is told, with the new text");
  assert.equal(tab.barEl.hidden, true);
  assert.equal(p.fulls().length, 2, "the open's read and the reload's");
  await p.tick();
  assert.equal(p.fulls().length, 2, "nothing changed since: the next poll reads no text");
  assert.equal(tab.model.sets.length, 1);
});

test("race: the user types between the metadata answer and the full read: it is decided again, so a bar and never a reload over the typing", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  let release;
  p.st.fullGate = new Promise((r) => { release = r; });
  disk.text = "agent\n";
  await p.tick(); // the metadata said "changed" and the buffer was clean: the full read is out
  assert.equal(p.fulls().length, 2, "the full read was asked");
  tab.model.type("one typed\n");
  release();
  await p.settle();
  assert.equal(tab.model.value, "one typed\n", "the typing is not replaced");
  assert.equal(tab.model.sets.length, 0);
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent\n"), "the bar is for what the full read found");
});

test("a dirty buffer whose file changed gets the bar and is left alone; the bar is not rebuilt by later polls, and takes a newer disk version", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open("/p/a.go");
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  assert.equal(bar(tab).msg, "fix-fs-cap changed a.go after you started editing. Your edits are not saved yet.");
  assert.equal(tab.model.value, "mine\n");
  assert.equal(tab.model.sets.length, 0, "nothing replaced");
  assert.equal(p.st.writes.length, 0, "nothing written");
  assert.equal(tab.version, p.hash("original\n"), "the buffer still began at the old version");
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent\n"));
  const first = bar(tab).buttons[0];
  disk.text = "agent twice\n";
  await p.tick();
  assert.equal(bar(tab).buttons[0], first, "the same buttons: a button the user has focus on is not replaced under them");
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent twice\n"), "the bar's target moved to the newest version");
  assert.equal(p.fulls().length, 1, "no text was read: a dirty buffer is never reloaded");
});

test("Review Focus 1: the agent rewrites a dirty file and Mod+S is pressed without reading the bar: a 409, the bar stays, nothing is overwritten until Keep mine", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false, "the poll put the bar up");
  assert.equal(p.st.writes.length, 0);
  // Mod+S, twice: each is one write guarded by the version the buffer began at, and the server refuses it
  for (let i = 1; i <= 2; i++) {
    tab.el.listeners.keydown(MOD_S());
    await p.settle();
    assert.equal(p.st.writes.length, i);
    assert.deepEqual(p.st.writes[i - 1], { path: "/p/a.go", content: "mine\n", base_version: p.hash("original\n"), overwrite: false });
    assert.equal(disk.text, "agent\n", "the agent's text is still what is on disk");
    assert.equal(tab.barEl.hidden, false, "the bar stays up");
    assert.equal(tab.dirty(), true);
    assert.equal(tab.model.value, "mine\n");
    assert.equal(tab.version, p.hash("original\n"), "a refused save does not move the base");
  }
  assert.deepEqual(p.st.toasts, []);
  // Keep mine: it saves nothing by itself, and the guard now holds the agent's version
  bar(tab).buttons[1].click();
  assert.equal(tab.barEl.hidden, true);
  assert.equal(p.st.writes.length, 2, "Keep mine writes nothing");
  assert.equal(disk.text, "agent\n");
  assert.equal(tab.version, p.hash("agent\n"));
  assert.equal(tab.dirty(), true);
  // The guard is still a guard: the agent writes again before the press, and that save is refused as well, with the bar back
  disk.text = "agent again\n";
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(p.st.writes[2].base_version, p.hash("agent\n"));
  assert.equal(disk.text, "agent again\n", "still not overwritten");
  assert.equal(tab.barEl.hidden, false, "the 409 brought the bar back");
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent again\n"));
  // Keep mine on that, and the press saves
  bar(tab).buttons[1].click();
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(p.st.writes[3].base_version, p.hash("agent again\n"));
  assert.equal(p.st.writes.every((w) => w.overwrite === false), true, "the server's overwrite flag is never used: base_version does the work");
  assert.equal(disk.text, "mine\n");
  assert.equal(tab.dirty(), false);
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.statusText, "Saved");
});

test("a save that gets a 409 before any poll noticed draws the bar too, and a later save that succeeds (the agent put the file back) takes it down", async () => {
  const p = page();
  const disk = p.serve("original\n");
  const tab = await p.open(); // the dock is not on screen: nothing polls
  tab.model.type("mine\n");
  disk.text = "agent\n";
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent\n"));
  assert.equal(disk.text, "agent\n");
  disk.text = "original\n"; // put back
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(disk.text, "mine\n");
  assert.equal(tab.barEl.hidden, true, "a saved buffer has nothing to ask");
});

test("Load the agent's version: the buffer becomes the disk's text in one undoable edit, clean, on the disk's version; the bar goes and the replace is announced", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  const ed = p.st.editors[0];
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  const heard = [];
  p.get("editorSyncOnReplace")(tab, (t) => heard.push(t));
  ed.top = 40;
  bar(tab).buttons[2].click();
  await p.settle();
  assert.equal(tab.model.value, "agent\n");
  assert.deepEqual(tab.model.sets, ["agent\n"], "one new value");
  assert.equal(ed.top, 40);
  assert.equal(tab.dirty(), false);
  assert.equal(tab.version, p.hash("agent\n"));
  assert.equal(tab.diskText, "agent\n");
  assert.equal(tab.barEl.hidden, true);
  assert.deepEqual(heard, ["agent\n"]);
  assert.equal(p.st.host.at(-1), "dirty", "the dot goes");
  assert.equal(p.st.host.filter((h) => h === "start").length, 1, "and loading pinned nothing (the one start is the typing)");
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(p.st.writes.length, 0, "clean: nothing to save");
  assert.equal(disk.text, "agent\n");
});

test("Load the agent's version that cannot read the file says why and keeps the bar and the buffer; a 404 is the deleted bar", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open("/p/a.go");
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  disk.fail = 500;
  bar(tab).buttons[2].click();
  await p.settle();
  assert.deepEqual(p.st.toasts, [["Could not read a.go: boom", { error: true }]]);
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.model.value, "mine\n");
  assert.equal(tab.dirty(), true);
  disk.fail = 0;
  disk.exists = false;
  bar(tab).buttons[2].click();
  await p.settle();
  assert.equal(bar(tab).msg, "This file was deleted on disk.");
});

test("Keep mine when the disk's version is unknown (a file past the read cap) says so and keeps the bar and the base", async () => {
  const p = page();
  const tab = await p.open();
  tab.model.type("mine\n");
  p.setWrite(() => Promise.resolve({ ok: false, status: 409, data: { error: "file changed on disk", current_version: "" } }));
  await tab.save();
  assert.equal(tab.barEl.hidden, false);
  bar(tab).buttons[1].click();
  assert.equal(p.st.toasts.length, 1);
  assert.match(p.st.toasts[0][0], /version is unknown/);
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.version, "v1");
});

test("a bar goes away by itself when the file is as the buffer began again; an undo back to clean makes the next poll reload", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  const clean = tab.model.getAlternativeVersionId();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  disk.text = "original\n"; // the agent put it back
  await p.tick();
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.model.value, "mine\n", "the buffer is still the user's");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  tab.model.undoTo(clean, "original\n"); // the user undoes their way back to what they read: clean
  assert.equal(tab.dirty(), false);
  await p.tick();
  assert.equal(tab.model.value, "agent\n", "nothing of the user's is at stake any more: it reloads");
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.version, p.hash("agent\n"));
});

test("Compare opens a read-only diff, disk on the left and the live buffer on the right, in place of the editor until dismissed; dismissing disposes the diff and the disk's model, never the buffer's", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open("/p/a.go");
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  bar(tab).buttons[0].click();
  await p.settle();
  const d = p.st.diffs[0];
  assert.equal(d.opts.readOnly, true);
  assert.equal(d.opts.originalEditable, false);
  assert.equal(d.opts.renderSideBySide, true);
  assert.equal(d.model.original.value, "agent\n", "the disk on the left");
  assert.equal(d.model.modified, tab.model, "the live buffer on the right, not a copy");
  assert.equal(d.revealed, 0);
  d.updated[0](); // Monaco has computed the diff
  assert.equal(d.revealed, 1, "it opens at the first difference");
  assert.equal(d.updated.length, 0, "once: a later refresh of the diff does not move it again");
  assert.equal(tab.cmpEl.hidden, false);
  assert.equal(tab.bodyEl.hidden, true, "in place of the editor");
  assert.equal(tab.barEl.hidden, false, "the bar is still up: Compare decides nothing");
  bar(tab).buttons[0].click();
  await p.settle();
  assert.equal(p.st.diffs.length, 1, "a second press opens no second view");
  tab.cmpEl.children[0].children[1].click(); // Back to editing
  assert.equal(d.disposed, true);
  assert.equal(d.model, null);
  assert.equal(p.st.models[1].disposed, true, "the disk's model");
  assert.equal(tab.model.disposed, false, "the buffer's stays");
  assert.equal(tab.cmpEl.hidden, true);
  assert.equal(tab.bodyEl.hidden, false);
  assert.equal(tab.barEl.hidden, false);
  assert.deepEqual(p.st.log, ["diff.setModel", "diff.setModel(null)", "diff.dispose", "dispose#2"]);
});

test("while Compare is open a newer disk version is read into it, so Keep mine never overwrites what the diff did not show; and a decision, or a close of the tab, takes the view down", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open("/p/a.go");
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  bar(tab).buttons[0].click();
  await p.settle();
  disk.text = "agent twice\n";
  await p.tick();
  assert.equal(p.st.models[1].value, "agent twice\n", "the disk's side is current");
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent twice\n"));
  bar(tab).buttons[1].click(); // Keep mine
  assert.equal(p.st.diffs[0].disposed, true, "a decision closes the view");
  assert.equal(tab.bodyEl.hidden, false);
  assert.equal(tab.version, p.hash("agent twice\n"));
  // Load closes it too
  tab.model.type("mine again\n");
  disk.text = "agent thrice\n";
  await p.tick();
  bar(tab).buttons[0].click();
  await p.settle();
  assert.equal(p.st.diffs.length, 2);
  bar(tab).buttons[2].click();
  await p.settle();
  assert.equal(p.st.diffs[1].disposed, true);
  assert.equal(tab.model.value, "agent thrice\n");
  // and a tab closed with a view open lets go of the editor and the model, the view's first
  tab.model.type("mine more\n");
  disk.text = "agent four\n";
  await p.tick();
  bar(tab).buttons[0].click();
  await p.settle();
  p.st.log.length = 0;
  const modelId = p.st.models.indexOf(tab.model) + 1;
  const diskId = p.st.models.length;
  tab.dispose();
  assert.deepEqual(p.st.log, ["diff.setModel(null)", "diff.dispose", "dispose#" + diskId, "dispose#" + modelId]);
  assert.equal(p.live(), 0);
});

test("a deleted file shows the deleted bar with Copy contents and Close; the buffer is kept; the bar goes when the file is back, and a changed file comes back as a reload", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  disk.exists = false;
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  assert.equal(bar(tab).msg, "This file was deleted on disk.");
  assert.deepEqual(bar(tab).labels, ["Copy contents", "Close"]);
  assert.equal(tab.model.value, "original\n", "the buffer is kept: it is all that is left of the file");
  let copied = 0;
  tab.copyContents = () => { copied++; };
  bar(tab).buttons[0].click();
  assert.equal(copied, 1);
  bar(tab).buttons[1].click();
  assert.deepEqual(p.st.closed, [tab], "Close is the dock's own close of the tab (which asks first when there is an edit)");
  disk.exists = true; // back, as it was
  await p.tick();
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.model.sets.length, 0, "the same text: nothing to reload");
  disk.exists = false;
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  disk.exists = true;
  disk.text = "recreated\n"; // back, and different
  await p.tick();
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.model.value, "recreated\n");
});

test("a dirty buffer on a deleted file gets the deleted bar too; Mod+S there (a 404) says it in the bar, not a toast, whether or not a poll saw it first", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.exists = false;
  await p.tick();
  assert.equal(bar(tab).msg, "This file was deleted on disk.");
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(bar(tab).msg, "This file was deleted on disk.");
  assert.deepEqual(p.st.toasts, []);
  assert.equal(tab.model.value, "mine\n");
  // no poll in between
  const q = page();
  const d2 = q.serve("original\n");
  const t2 = await q.open();
  t2.model.type("mine\n");
  d2.exists = false;
  t2.el.listeners.keydown(MOD_S());
  await q.settle();
  assert.equal(bar(t2).msg, "This file was deleted on disk.");
  assert.deepEqual(q.st.toasts, []);
});

test("race: no poll starts while a save is in flight", async () => {
  const p = page();
  p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  let release;
  p.setWrite((body) => new Promise((r) => { release = () => r(p.diskWrite(body)); }));
  const saving = tab.save();
  const n = p.metas().length;
  await p.tick();
  assert.equal(p.metas().length, n, "a tick during the save asks nothing");
  release();
  await saving;
  await p.tick();
  assert.equal(p.metas().length, n + 1, "and asks again once it is over");
});

test("race: a poll that began before our own save is dropped when it answers after it: our write is never taken for the agent's", async () => {
  for (const typedDuring of [false, true]) {
    const p = page();
    p.serve("original\n");
    p.setShown(true);
    const tab = await p.open();
    await p.settle();
    tab.model.type("mine\n");
    let releaseMeta;
    p.st.metaGate = new Promise((r) => { releaseMeta = r; });
    await p.tick(); // out, and answered "original" already (the version the buffer began at)
    p.st.metaGate = null;
    let releaseWrite;
    p.setWrite((body) => new Promise((r) => { releaseWrite = () => r(p.diskWrite(body)); }));
    const saving = tab.save();
    if (typedDuring) tab.model.type("mine more\n"); // dirty again when the save is over: a false bar, if the stale answer were believed
    releaseWrite();
    await saving;
    assert.equal(tab.version, p.hash("mine\n"));
    releaseMeta(); // the stale answer: "original", which is not the version the save just wrote
    await p.settle();
    const what = typedDuring ? "typed during the save" : "clean after the save";
    assert.equal(tab.barEl.hidden, true, what + ": no bar");
    assert.equal(tab.model.sets.length, 0, what + ": no reload of our own write");
    assert.equal(p.fulls().length, 1, what + ": no text was read");
    assert.equal(tab.dirty(), typedDuring, what);
  }
});

test("race: an answer that arrives after the tab was disposed, hidden, switched away from, re-pointed, or the dock went off screen is dropped", async () => {
  const cases = {
    "disposed": async (p, tab) => { tab.dispose(); },
    "hidden": async (p, tab) => { tab.onHide(); },
    "another tab shown": async (p, tab) => { tab.onHide(); await p.open("/p/b.go"); },
    "re-pointed": async (p, tab) => { tab.path = "/p/other.go"; },
    "dock closed": async (p) => { p.setShown(false); },
    "under a page view": async (p) => { p.setPage(true); },
  };
  for (const [name, change] of Object.entries(cases)) {
    const p = page();
    const disk = p.serve("one\n");
    p.setShown(true);
    const tab = await p.open("/p/a.go");
    await p.settle();
    let release;
    p.st.metaGate = new Promise((r) => { release = r; });
    disk.text = "agent\n";
    await p.tick(); // out, and answered "changed" already
    p.st.metaGate = null;
    await change(p, tab);
    const fulls = p.fulls().length;
    release();
    await p.settle();
    assert.equal(p.fulls().length, fulls, name + ": the late answer started no read");
    if (tab.model) {
      assert.equal(tab.model.value, "one\n", name + ": nothing replaced");
      assert.equal(tab.model.sets.length, 0, name);
    }
    assert.equal(tab.barEl.hidden, true, name + ": no bar");
  }
});

test("race: one request at a time: a tick that finds the last one still out asks nothing", async () => {
  const p = page();
  p.serve();
  p.setShown(true);
  await p.open();
  await p.settle();
  let release;
  p.st.metaGate = new Promise((r) => { release = r; });
  const n = p.metas().length;
  await p.tick();
  await p.tick();
  await p.tick();
  assert.equal(p.metas().length, n + 1);
  release();
  await p.settle();
  p.st.metaGate = null;
  await p.tick();
  assert.equal(p.metas().length, n + 2, "the answer landed: the next tick asks");
});

test("a poll that fails, or answers something that is not a version, changes nothing (and the next one still asks)", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  disk.fail = 500; // the server is unwell: the bar neither goes nor changes
  await p.tick();
  assert.equal(tab.barEl.hidden, false);
  assert.equal(tab.sync.bar.diskVersion, p.hash("agent\n"));
  disk.fail = 0;
  disk.text = "one\n"; // and when it answers again, it is heard
  await p.tick();
  assert.equal(tab.barEl.hidden, true);
});

test("editorSyncOnReplace: returns its unsubscribe, and one subscriber that throws does not stop the others", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  const on = p.get("editorSyncOnReplace");
  const heard = [];
  on(tab, () => { throw new Error("a subscriber's fault"); });
  const off = on(tab, (t) => heard.push("a:" + t));
  on(tab, (t) => heard.push("b:" + t));
  assert.equal(typeof on(tab, "not a function"), "function", "a no-op unsubscribe for a bad argument");
  disk.text = "two\n";
  await p.tick();
  assert.deepEqual(heard, ["a:two\n", "b:two\n"]);
  off();
  disk.text = "three\n";
  await p.tick();
  assert.deepEqual(heard, ["a:two\n", "b:two\n", "b:three\n"]);
});

test("a read-only tab (a file in a session's box) reloads when the file changes too, and never has a bar to show", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open("/work/out.txt", { box: "s1" });
  await p.settle();
  assert.equal(tab.mode, "read");
  disk.text = "two\n";
  await p.tick();
  assert.equal(tab.model.value, "two\n");
  assert.equal(tab.barEl.hidden, true);
  assert.equal(tab.dirty(), false);
});

// --- no undo path to a silent overwrite (review round 1) ---------------------------------------------------------------------
// The invariant: `tab.version` is one value, so a buffer must never hold text that is not the text of the disk version it is
// guarded by unless the user chose that (Keep mine, and then a save). An undo across a replace from disk would break it: the
// old text comes back, dirty, with the agent's version as its base, and a save is accepted by the server with no 409 and no bar.

test("an undo cannot go back past a reload from disk: the history is gone, and what an undo does leaves the buffer clean and unwritten", async () => {
  const p = page();
  const disk = p.serve("one\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("typed\n");
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(disk.text, "typed\n");
  assert.equal(tab.model.undos.length, 1, "the user's typing is on the undo stack");
  disk.text = "agent\n"; // the agent writes: a clean buffer reloads, silently
  await p.tick();
  assert.equal(tab.model.value, "agent\n");
  assert.deepEqual(tab.model.undos, [], "and the typing before it can no longer be undone into the buffer");
  assert.equal(tab.model.undo(), false);
  assert.equal(tab.model.value, "agent\n");
  assert.equal(tab.dirty(), false);
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(p.st.writes.length, 1, "Mod+S writes nothing: only the user's own save is there");
  assert.equal(disk.text, "agent\n", "the agent's text is untouched");
  // the typing after the reload undoes as ever, back to the reloaded text, clean
  tab.model.type("agent plus\n");
  assert.equal(tab.dirty(), true);
  tab.model.undo();
  assert.equal(tab.model.value, "agent\n");
  assert.equal(tab.dirty(), false, "an undo back to the loaded text is clean: it is what is on disk");
});

test("an undo cannot go back past Load the agent's version either: the user's text is not coming back as a dirty buffer on the agent's version", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  bar(tab).buttons[2].click();
  await p.settle();
  assert.equal(tab.model.value, "agent\n");
  assert.equal(tab.model.undo(), false, "nothing to undo into");
  assert.equal(tab.model.value, "agent\n");
  assert.equal(tab.dirty(), false);
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(p.st.writes.length, 0);
  assert.equal(disk.text, "agent\n");
});

test("Keep mine keeps the buffer dirty for good: no undo makes it look clean, and Mod+S then saves, which is what Keep mine said", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  const clean = tab.model.getAlternativeVersionId();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  p.st.host.length = 0;
  bar(tab).buttons[1].click();
  assert.equal(tab.version, p.hash("agent\n"));
  assert.equal(tab.dirty(), true);
  tab.model.undo(); // back to the text the buffer began with: the id it began at
  assert.equal(tab.model.getAlternativeVersionId(), clean);
  assert.equal(tab.model.value, "original\n");
  assert.equal(tab.dirty(), true, "still dirty: the agent's file is under a buffer that must not read as clean");
  assert.equal(tab.crumbNote().length > 0, true, "and it says Unsaved");
  await p.tick();
  assert.equal(tab.dirty(), true, "a poll finds the disk where Keep mine put the base, and asks nothing");
  assert.equal(tab.barEl.hidden, true);
  tab.el.listeners.keydown(MOD_S()); // the user's choice
  await p.settle();
  assert.equal(p.st.writes.length, 1);
  assert.equal(p.st.writes[0].base_version, p.hash("agent\n"));
  assert.equal(p.st.writes[0].overwrite, false);
  assert.equal(disk.text, "original\n", "the buffer is what was saved");
  assert.equal(tab.dirty(), false, "a save is what ends it");
  tab.model.type("more\n");
  assert.equal(tab.dirty(), true);
  tab.model.undo();
  assert.equal(tab.dirty(), false, "and from then on an undo back to the saved text is clean again");
});

test("Keep mine on a buffer that an undo had made clean makes it dirty, and tells the host without pinning", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  tab.model.undo(); // clean, with the bar still up (no poll has run since)
  assert.equal(tab.dirty(), false);
  p.st.host.length = 0;
  bar(tab).buttons[1].click();
  assert.equal(tab.dirty(), true);
  assert.deepEqual(p.st.host, ["dirty"], "the dot comes; an edit that was not typed pins nothing");
});

test("discarding after Keep mine puts back the text and the version the buffer began at, so the next poll reloads the agent's file", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  bar(tab).buttons[1].click();
  tab.discardEdits();
  assert.equal(tab.model.value, "original\n");
  assert.equal(tab.version, p.hash("original\n"), "the version that goes with that text, not the disk's");
  assert.equal(tab.dirty(), false);
  await p.tick();
  assert.equal(tab.model.value, "agent\n", "clean on an older version: the poll reloads it");
  assert.equal(tab.version, p.hash("agent\n"));
});

test("race: Load, then Keep mine and a save inside the read: the late read is dropped and does not put the agent's text over what was saved", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  let release;
  p.st.fullGate = new Promise((r) => { release = r; });
  const heard = [];
  p.get("editorSyncOnReplace")(tab, (t) => heard.push(t));
  bar(tab).buttons[2].click(); // Load: a read, answered "agent" already, waits at the gate
  await p.settle();
  assert.equal(p.fulls().length, 2);
  p.st.fullGate = null;
  bar(tab).buttons[1].click(); // Keep mine
  tab.el.listeners.keydown(MOD_S()); // and a save
  await p.settle();
  assert.equal(disk.text, "mine\n");
  assert.equal(tab.dirty(), false);
  release();
  await p.settle();
  assert.equal(tab.model.value, "mine\n", "the late answer is not applied");
  assert.equal(tab.model.sets.length, 0);
  assert.equal(tab.version, p.hash("mine\n"));
  assert.equal(tab.dirty(), false);
  assert.deepEqual(heard, []);
  assert.equal(disk.text, "mine\n");
});

test("race: a Load or Compare whose answer lands after the bar was replaced, cleared or the tab re-pointed or disposed is dropped", async () => {
  const cases = {
    "the bar was cleared by the agent putting the file back": async (p, disk) => { disk.text = "original\n"; await p.tick(); },
    "the bar became the deleted bar": async (p, disk) => { disk.exists = false; await p.tick(); },
    "the tab was re-pointed": async (p, disk, tab) => { tab.path = "/p/other.go"; },
    "the tab was disposed": async (p, disk, tab) => { tab.dispose(); },
  };
  for (const button of [2, 0]) {
    for (const [name, change] of Object.entries(cases)) {
      const p = page();
      const disk = p.serve("original\n");
      p.setShown(true);
      const tab = await p.open("/p/a.go");
      await p.settle();
      tab.model.type("mine\n");
      disk.text = "agent\n";
      await p.tick();
      let release;
      p.st.fullGate = new Promise((r) => { release = r; });
      bar(tab).buttons[button].click();
      await p.settle();
      p.st.fullGate = null;
      await change(p, disk, tab);
      release();
      await p.settle();
      const what = (button === 2 ? "Load" : "Compare") + " / " + name;
      assert.equal(p.st.diffs.length, 0, what + ": no compare view");
      if (tab.model) assert.equal(tab.model.sets.length, 0, what + ": nothing replaced");
      if (tab.model) assert.equal(tab.model.value, "mine\n", what);
    }
  }
});

test("a save after Keep mine ends it: a later discard goes back to what that save wrote, not to the version before Keep mine", async () => {
  const p = page();
  const disk = p.serve("original\n");
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  tab.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  bar(tab).buttons[1].click();
  tab.el.listeners.keydown(MOD_S());
  await p.settle();
  assert.equal(disk.text, "mine\n");
  tab.model.type("mine more\n");
  tab.discardEdits();
  assert.equal(tab.model.value, "mine\n");
  assert.equal(tab.version, p.hash("mine\n"));
  assert.equal(tab.dirty(), false);
});

// ---------------------------------------------------------------------------------------------------------------------------
// Changes vs HEAD and the gutter (editordiff.js). The page's git is stubbed (page().st.git): the tree's status names HEAD "h1" and
// POST /api/git/show answers what the test sets. What Monaco draws from the decorations and how the inline diff looks is the
// browser check's (the task report); here are the calls the code makes, and when.

const HEAD_TEXT = "l1\nl2\nl3\nl5\nl6\n";
const BUF_TEXT = "l1\nl2 changed\nl3\nl4 new\nl5\n";
const MARKS = [["dk-g-mod", 2, 2], ["dk-g-add", 4, 4], ["dk-g-del", 6, 6]]; // lineDiff(HEAD_TEXT, BUF_TEXT): l2 changed, l4 added, l6 removed
const okBlob = (content) => ({ state: "ok", content });

// withGit: a page whose file holds `text`, the tree's git status known and the show answer set, and a tab read and on screen with its
// answer in. `o.opts` are the EditorTab's options.
async function withGit(answer, text, o) {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: text, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = answer;
  p.setShown(true);
  const tab = await p.open((o && o.path) || "/p/a.go", o && o.opts);
  await p.settle();
  return { p, tab };
}

// What the shared editor draws in the gutter: [class, first line, last line] per decoration.
const gutter = (p) => p.st.editors[0].decor.list().map((d) => [d.options.linesDecorationsClassName, d.range.startLineNumber, d.range.endLineNumber]);
const debounce = (p) => p.st.timers.filter((t) => t.ms === 300 && !t.dead);
const changesBtn = (tab) => tab.seg.changes;

test("the gutter: the base text from POST /api/git/show against the buffer marks added, modified and removed lines, with the theme's overview ruler colours", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  assert.equal(p.st.git.shows.length, 1, "one request");
  assert.equal(p.st.git.shows[0].url, "/api/git/show");
  assert.deepEqual(p.st.git.shows[0].body, { path: "/p/a.go", scope: "head" }, "the path and the scope, never a revision");
  assert.deepEqual(gutter(p), MARKS);
  const decs = p.st.editors[0].decor.list();
  assert.deepEqual(decs.map((d) => d.options.overviewRuler), [
    { color: { id: "editorOverviewRuler.modifiedForeground" }, position: 1 },
    { color: { id: "editorOverviewRuler.addedForeground" }, position: 1 },
    { color: { id: "editorOverviewRuler.deletedForeground" }, position: 1 },
  ]);
  assert.ok(decs.every((d) => d.options.isWholeLine));
  assert.deepEqual(tab.changes.lineChanges, [{ kind: "mod", from: 2, to: 2 }, { kind: "add", from: 4, to: 4 }, { kind: "del", from: 6, to: 6 }]);
  assert.equal(tab.changes.head, HEAD_TEXT);
  assert.equal(tab.changes.scope, "head");
  assert.equal(tab.changes.capped, false);
});

test("the gutter: a removal at the very end sits one past the last line and is drawn on the last", async () => {
  const { p } = await withGit(okBlob("a\nb\nc"), "a\nb");
  assert.deepEqual(gutter(p), [["dk-g-del", 2, 2]], "lineDiff says line 3 of a 2-line buffer; the marker goes on line 2");
});

test("the gutter: typing asks git for nothing; the marks follow 300 ms after the last edit, as one recompute", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  tab.model.type(BUF_TEXT + "l7\n");
  tab.model.type(BUF_TEXT + "l7\nl8\n");
  tab.model.type("l1\nl2 changed\nl3\nl4 new\nl5\nl7\nl8\n");
  assert.equal(p.st.git.shows.length, 1, "no show request per keystroke");
  assert.equal(debounce(p).length, 1, "one pending recompute: each edit pushed the last one back");
  assert.equal(debounce(p)[0].ms, 300);
  assert.deepEqual(gutter(p), MARKS, "the marks wait for the pause");
  debounce(p)[0].fn();
  assert.deepEqual(gutter(p), [["dk-g-mod", 2, 2], ["dk-g-add", 4, 4], ["dk-g-mod", 6, 7]]);
  assert.equal(p.st.git.shows.length, 1);
  tab.model.type(HEAD_TEXT); // back to the base: no marks at all
  debounce(p)[0].fn();
  assert.deepEqual(gutter(p), []);
  assert.deepEqual(tab.changes.lineChanges, []);
});

test("the gutter: marks that did not change are not written again", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  const ed = p.st.editors[0];
  let calls = 0;
  const delta = ed.deltaDecorations.bind(ed);
  ed.deltaDecorations = (a, b) => { calls++; return delta(a, b); };
  tab.model.type(BUF_TEXT.replace("l5", "l5")); // an edit that moves no line's status (same text)
  tab.model.type("l1\nl2 changed again\nl3\nl4 new\nl5\n"); // still the same three marks
  debounce(p)[0].fn();
  assert.equal(calls, 0, "same marks, no deltaDecorations");
});

test("the gutter: a diff lineDiff declines (too different) draws nothing and costs nothing per keystroke; Changes is still offered", async () => {
  const lines = (c) => Array.from({ length: 3000 }, (_, i) => c + i).join("\n") + "\n";
  const { p, tab } = await withGit(okBlob(lines("a")), lines("b"));
  assert.deepEqual(gutter(p), []);
  assert.equal(tab.changes.capped, true);
  assert.deepEqual(tab.changes.lineChanges, []);
  assert.equal(changesBtn(tab).getAttribute("aria-disabled"), "false", "capped is about the marks, not about whether Monaco can diff");
  assert.equal(p.st.git.shows.length, 1);
});

test("the gutter: a file the base does not have is all added", async () => {
  const { p, tab } = await withGit({ state: "ok", missing: true, content: "" }, "x\ny\n");
  assert.deepEqual(gutter(p), [["dk-g-add", 1, 3]], "x, y and the empty line after the final newline");
  assert.equal(tab.changes.head, "");
  assert.equal(tab.changes.missing, true);
  assert.equal(changesBtn(tab).getAttribute("aria-disabled"), "false");
});

test("unavailable, notrepo, truncated and a failed request: no marks, the toggle is disabled with the reason, and Changes does not open", async () => {
  const cases = [
    [{ state: "unavailable", reason: "Git status needs the box runtime (docker)" }, "Git status needs the box runtime (docker)"],
    [{ state: "unavailable" }, "git is not available here"],
    [{ state: "notrepo" }, "this file is not in a git repository"],
    [{ state: "ok", content: "partial", truncated: true }, "the version in git is over 2 MiB"],
    [null, "the server did not answer"],
  ];
  for (const [answer, why] of cases) {
    const { p, tab } = await withGit(answer, BUF_TEXT);
    const b = changesBtn(tab);
    assert.equal(b.getAttribute("aria-disabled"), "true", why);
    assert.equal(b.title, "Changes unavailable: " + why);
    assert.equal(tab.seg.code.getAttribute("aria-pressed"), "true");
    assert.deepEqual(gutter(p), [], why);
    assert.equal(tab.changes.head, null);
    b.click(); // a disabled button does nothing
    assert.equal(p.st.diffs.length, 0, why + ": no diff editor");
    assert.equal(tab.showChanges(true), false);
    assert.deepEqual(p.st.toasts.at(-1), ["Changes unavailable: " + why, undefined], "a call from outside (a badge's click) says why");
    assert.equal(p.st.diffs.length, 0);
    tab.model.type("more\n");
    assert.equal(debounce(p).length, 0, "typing costs no recompute when there is nothing to compare with");
    assert.deepEqual(tab.bodyEl.children, [p.get("dockEditorEl")], "the file stays in Code");
  }
});

test("a box tab asks git nothing: Changes is disabled with the reason", async () => {
  const p = page();
  p.st.git.on = true;
  p.st.git.answer = okBlob(HEAD_TEXT);
  p.setShown(true);
  const tab = await p.open("/p/shot.txt", { box: "s1" });
  await p.settle();
  assert.equal(p.st.git.shows.length, 0);
  assert.equal(changesBtn(tab).getAttribute("aria-disabled"), "true");
  assert.equal(changesBtn(tab).title, "Changes unavailable: a file in a session's box has no git history");
  assert.equal(tab.showChanges(true), false);
  assert.equal(p.st.git.shows.length, 0);
  assert.equal(p.st.diffs.length, 0);
});

test("git's status not known yet: nothing is asked until it is; a status that lands asks once", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.answer = okBlob(HEAD_TEXT);
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  assert.equal(p.st.git.shows.length, 0, "no status, no HEAD to key the answer by");
  p.st.git.on = true;
  p.get("editorDiffSync")(); // gitPaintTree
  await p.settle();
  assert.equal(p.st.git.shows.length, 1);
  assert.deepEqual(gutter(p), MARKS);
  p.get("editorDiffSync")();
  assert.equal(p.st.git.shows.length, 1, "the same key asks nothing again");
  assert.equal(tab.changes.key, "/p/a.go\nhead\nh1\n");
});

test("a dock that is not on screen asks nothing; it asks when it comes back", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = okBlob(HEAD_TEXT);
  const tab = await p.open(); // the dock is not shown
  await p.settle();
  assert.equal(p.st.git.shows.length, 0);
  p.setShown(true);
  p.get("editorDiffSync")(); // drawerPaint, through dockChangesRefresh
  await p.settle();
  assert.equal(p.st.git.shows.length, 1);
  assert.deepEqual(gutter(p), MARKS);
  assert.ok(tab.changes);
});

test("a new HEAD in the status asks again; the same one, an edit of the file or a save asks nothing", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  p.get("editorDiffSync")();
  tab.model.type("edited\n");
  await tab.save();
  p.get("editorDiffSync")();
  await p.settle();
  assert.equal(p.st.git.shows.length, 1, "a status of the same HEAD: the base text has not moved");
  p.st.git.head = "h2";
  p.st.git.answer = okBlob("edited\n");
  p.get("editorDiffSync")();
  await p.settle();
  assert.equal(p.st.git.shows.length, 2, "the commit moved the base text");
  assert.equal(tab.changes.key, "/p/a.go\nhead\nh2\n");
  assert.deepEqual(gutter(p), [], "the buffer is what HEAD has now");
});

test("a base text that could not be read is asked for again only after a while, not on every status", async () => {
  const { p, tab } = await withGit(null, BUF_TEXT);
  assert.equal(p.st.git.shows.length, 1);
  p.get("editorDiffSync")();
  p.get("editorDiffSync")();
  await p.settle();
  assert.equal(p.st.git.shows.length, 1, "the failure stands for a while");
  const fresh = p.get("editorDiffFresh");
  const k = { key: tab.changes.key };
  assert.equal(fresh(tab.changes, k, tab.changes.at + 29999), true);
  assert.equal(fresh(tab.changes, k, tab.changes.at + 30000), false, "then it is asked again");
  assert.equal(fresh({ key: k.key, head: "", at: 0 }, k, 1e12), true, "a text that was read stands until the key moves");
  assert.equal(fresh(tab.changes, { key: "other" }, tab.changes.at), false);
});

test("a show answer for a scope the tab has left, or for a tab that was disposed, is dropped", async () => {
  // The scope is switched while the request is out: that answer is about the old scope, and the new one is asked for.
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = (b) => okBlob(b.scope === "branch" ? "l1\nl2 changed\nl3\nl4 new\n" : HEAD_TEXT);
  let release;
  p.st.git.gate = new Promise((r) => { release = r; });
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  assert.equal(p.st.git.shows.length, 1);
  p.st.git.scope = "branch";
  p.st.git.base = { mergeBase: "mb1", baseName: "main" };
  release();
  await p.settle();
  assert.deepEqual(p.st.git.shows.map((s) => s.body.scope), ["head", "branch"], "the old answer is not kept; the new scope is asked for");
  assert.equal(tab.changes.scope, "branch");
  assert.equal(tab.changes.key, "/p/a.go\nbranch\nh1\nmb1");
  assert.deepEqual(gutter(p), [["dk-g-add", 5, 5]], "marks of the branch's base text (it lacks l5), not HEAD's");

  // Disposed while the request is out.
  const q = page();
  q.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  q.st.git.on = true;
  q.st.git.answer = okBlob(HEAD_TEXT);
  let release2;
  q.st.git.gate = new Promise((r) => { release2 = r; });
  q.setShown(true);
  const t2 = await q.open();
  await q.settle();
  assert.equal(q.st.git.shows.length, 1);
  t2.showChanges(true); // the user had asked for Changes too
  t2.dispose();
  release2();
  await q.settle();
  assert.equal(t2.changes, null, "nothing kept");
  assert.equal(q.st.diffs.length, 0, "no diff editor for a closed tab");
  assert.equal(q.st.toasts.length, 0);
});

test("Changes: the toggle swaps the body for a read-only inline diff of the base text against the live model, headed 'vs HEAD'", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  tab.model.type(BUF_TEXT + "unsaved\n"); // an edit not saved: Changes shows it
  assert.equal(tab.dirty(), true);
  changesBtn(tab).click();
  assert.equal(p.st.diffs.length, 1);
  const d = p.st.diffs[0];
  assert.equal(d.opts.renderSideBySide, false, "inline");
  assert.equal(d.opts.readOnly, true, "a review view: typing in it does nothing");
  assert.match(d.opts.readOnlyMessage.value, /Switch to Code to edit/);
  assert.equal(d.opts.originalEditable, false);
  assert.equal(d.opts.ignoreTrimWhitespace, false, "a whitespace-only change is shown, as git diff and the gutter do");
  assert.equal(d.opts.theme, "culibox");
  assert.equal(d.model.original.value, HEAD_TEXT, "the removed side is the base text");
  assert.equal(d.model.original.getLanguageId(), "go");
  assert.equal(d.model.modified, tab.model, "the new side is the tab's own model: the live buffer, not a copy");
  assert.equal(d.model.modified.value, BUF_TEXT + "unsaved\n");
  assert.notEqual(d.model.original, tab.model);
  assert.equal(tab.diff.diff, d);
  assert.deepEqual(tab.bodyEl.children.map((c) => c.className), ["dk-ed-cmp"], "the body is the view, not the editor");
  const [head, body] = tab.bodyEl.children[0].children;
  assert.equal(head.textContent, "vs HEAD");
  assert.equal(d.el, body);
  assert.equal(tab.seg.code.getAttribute("aria-pressed"), "false");
  assert.equal(changesBtn(tab).getAttribute("aria-pressed"), "true");
  assert.equal(p.st.git.shows.length, 1, "the toggle reuses the text the gutter asked for");
  assert.ok(d.laid > 0);
  [...d.updated].forEach((f) => f()); // Monaco computed the diff
  assert.equal(d.revealed, 1, "it opens at the first change");
  assert.equal(d.updated.length, 0);
  // The tab's own state does not change under it.
  assert.equal(tab.dirty(), true);
  assert.equal(tab.model.value, BUF_TEXT + "unsaved\n");
});

test("Changes: the status bar reads the cursor of the diff's new side", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  changesBtn(tab).click();
  const m = p.st.diffs[0].modified;
  m.getPosition = () => ({ lineNumber: 4, column: 7 });
  m.cursorSubs.forEach((f) => f());
  assert.match(tab.sbParts.pos.textContent, /4/);
  assert.match(tab.sbParts.pos.textContent, /7/);
});

test("Changes: leaving it disposes the diff editor and the model of the base text, and the editor is back at the place it was left", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  const ed = p.st.editors[0];
  ed.top = 240;
  changesBtn(tab).click();
  const d = p.st.diffs[0];
  const original = d.model.original;
  p.st.log.length = 0;
  tab.seg.code.click();
  assert.equal(d.disposed, true);
  assert.equal(original.disposed, true, "the base text's model is let go");
  assert.equal(tab.model.disposed, false, "the tab's own model is not the view's");
  assert.deepEqual(p.st.log.slice(0, 2), ["diff.setModel(null)", "diff.dispose"]);
  assert.ok(p.st.log.some((l) => l.startsWith("dispose#")), "and the base model is disposed after the editor that held it");
  assert.equal(tab.diff, null);
  assert.deepEqual(tab.bodyEl.children, [p.get("dockEditorEl")], "the shared editor is the body again");
  assert.deepEqual(ed.restored.at(-1), { saved: true, top: 240 }, "at the place it was left");
  assert.deepEqual(gutter(p), MARKS, "and its gutter is drawn");
  assert.equal(tab.seg.code.getAttribute("aria-pressed"), "true");
  assert.equal(ed.focused, 1, "Code takes the keyboard back");
  assert.equal(tab.showChanges(false), false);
  // Another round makes a fresh view, and nothing is left behind.
  changesBtn(tab).click();
  assert.equal(p.st.diffs.length, 2);
  assert.equal(p.st.diffs[1].disposed, false);
});

test("Changes: a tab closed in Changes disposes the diff editor and the base text's model, the timer and the subscription", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  changesBtn(tab).click();
  const d = p.st.diffs[0];
  const original = d.model.original;
  tab.model.type("again\n");
  assert.equal(debounce(p).length, 1);
  const model = tab.model;
  tab.dispose();
  assert.equal(d.disposed, true);
  assert.equal(original.disposed, true);
  assert.equal(model.disposed, true);
  const at = (l) => p.st.log.indexOf(l);
  assert.ok(at("diff.dispose") >= 0 && at("diff.dispose") < at("dispose#" + (p.st.models.indexOf(original) + 1)), "the editor that holds the models goes first");
  assert.ok(at("diff.dispose") < at("dispose#" + (p.st.models.indexOf(model) + 1)));
  assert.equal(debounce(p).length, 0, "the pending recompute is cancelled");
  assert.equal(tab.changes, null);
  assert.equal(tab.diff, null);
  assert.equal(model.listeners.length, 0, "no subscription of the gutter's is left on the model");
});

test("Changes opens when the base text lands if the user asked before it was in", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.answer = okBlob(HEAD_TEXT); // status not known: nothing is asked by itself
  p.setShown(true);
  const tab = await p.open();
  await p.settle();
  assert.equal(p.st.git.shows.length, 0);
  assert.equal(changesBtn(tab).getAttribute("aria-disabled"), "false", "nothing says Changes cannot be had");
  changesBtn(tab).click(); // asks, whatever the status says
  await p.settle();
  assert.equal(p.st.git.shows.length, 1);
  assert.equal(p.st.diffs.length, 1);
  assert.ok(tab.diff);
});

test("view: changes at open: the file opens in Changes once the model and the base text are in, in either order of attach and read", async () => {
  // The model first, then the dock attaches the tab (what open() does).
  const a = await withGit(okBlob(HEAD_TEXT), BUF_TEXT, { opts: { view: "changes" } });
  assert.equal(a.p.st.diffs.length, 1);
  assert.ok(a.tab.diff);
  assert.deepEqual(a.tab.bodyEl.children.map((c) => c.className), ["dk-ed-cmp"]);
  assert.equal(a.p.st.editors.length, 0, "no Code editor was made for a file that opened in Changes");
  a.tab.seg.code.click();
  assert.equal(a.p.st.editors.length, 1, "Code makes it when asked for");
  assert.deepEqual(a.tab.bodyEl.children, [a.p.get("dockEditorEl")]);

  // Attached while the file is still being read (the dock's order).
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = okBlob(HEAD_TEXT);
  p.setShown(true);
  const tab = new p.EditorTab("/p/a.go", { host: p.host, view: "changes" });
  tab.onAttach();
  for (let i = 0; i < 50 && !tab.diff; i++) await new Promise((r) => setImmediate(r));
  assert.ok(tab.diff, "opened in Changes");
  assert.equal(p.st.git.shows.length, 1, "one request: the gutter's, which Changes reuses");
  assert.equal(p.st.diffs.length, 1);

  // Git cannot give the base text: the file stays in Code, and the toast says why.
  const u = await withGit({ state: "unavailable", reason: "Git status needs the box runtime (docker)" }, BUF_TEXT, { opts: { view: "changes" } });
  assert.equal(u.p.st.diffs.length, 0);
  assert.equal(u.tab.diff, null);
  assert.deepEqual(u.p.st.toasts.at(-1), ["Changes unavailable: Git status needs the box runtime (docker)", undefined]);
  assert.deepEqual(u.tab.bodyEl.children, [u.p.get("dockEditorEl")]);
});

test("Changes: a call with no argument (a badge's click on a tab that is open) turns it on, and one with false goes back", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  assert.equal(tab.showChanges(), true);
  assert.equal(p.st.diffs.length, 1);
  assert.equal(tab.showChanges(), true, "already on");
  assert.equal(p.st.diffs.length, 1, "not made twice");
  assert.equal(tab.showChanges(false), false);
  assert.equal(tab.diff, null);
});

test("Changes: the label follows the scope; a switch of scope or a new HEAD while it is open puts the new base text in place", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  p.st.git.answer = (b) => okBlob(b.scope === "branch" ? "l1\nl2\n" : HEAD_TEXT);
  changesBtn(tab).click();
  const d = p.st.diffs[0];
  const label = () => tab.bodyEl.children[0].children[0].children[0].textContent;
  assert.equal(label(), "vs HEAD");
  p.st.git.scope = "branch";
  p.st.git.base = { mergeBase: "mb1", baseName: "main" };
  p.get("editorDiffSync")(); // dockChangesRefresh: the scope was picked
  await p.settle();
  assert.equal(p.st.diffs.length, 1, "the same view, not a new one");
  assert.equal(d.model.original.value, "l1\nl2\n", "the removed side is the merge base's text");
  assert.deepEqual(d.model.original.sets, ["l1\nl2\n"]);
  assert.equal(label(), "vs main (merge base)");
  assert.equal(changesBtn(tab).title, "Changes vs main (merge base)");
  p.st.git.scope = "head";
  p.get("editorDiffSync")();
  await p.settle();
  assert.equal(d.model.original.value, HEAD_TEXT);
  assert.equal(label(), "vs HEAD");
  // Git can no longer give it: the view goes, and says why.
  p.st.git.head = "h9";
  p.st.git.answer = { state: "unavailable", reason: "Git took too long" };
  p.get("editorDiffSync")();
  await p.settle();
  assert.equal(tab.diff, null);
  assert.equal(d.disposed, true);
  assert.deepEqual(p.st.toasts.at(-1), ["Changes unavailable: Git took too long", undefined]);
  assert.equal(changesBtn(tab).getAttribute("aria-disabled"), "true");
  assert.deepEqual(tab.bodyEl.children, [p.get("dockEditorEl")], "back in Code");
});

test("a tab left in Changes comes back in Changes: no new diff editor, and the gutter and the timer sleep while it is hidden", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  changesBtn(tab).click();
  const d = p.st.diffs[0];
  tab.model.type("more\n");
  assert.equal(debounce(p).length, 1);
  tab.onHide();
  assert.equal(debounce(p).length, 0, "the recompute pending for a hidden tab is cancelled");
  tab.model.type("hidden\n");
  assert.equal(debounce(p).length, 0, "and a hidden tab's edits schedule none");
  assert.equal(d.disposed, false, "the view is kept");
  const laid = d.laid;
  tab.onAttach();
  assert.equal(p.st.diffs.length, 1);
  assert.equal(tab.diff.diff, d);
  assert.deepEqual(tab.bodyEl.children.map((c) => c.className), ["dk-ed-cmp"]);
  assert.ok(d.laid > laid, "laid out again");
  assert.equal(d.options.length, 1, "with the preferences of now");
});

test("the gutter is drawn again when a tab that another tab's claim of the editor stripped is shown again", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  assert.deepEqual(gutter(p), MARKS);
  tab.onHide();
  p.st.git.answer = okBlob(BUF_TEXT);
  const second = await p.open("/p/b.go"); // its text is its base: no marks
  await p.settle();
  assert.deepEqual(gutter(p), [], "the first tab's marks went with its model");
  second.onHide();
  tab.onAttach(); // not edited meanwhile: the same marks as before, which must still be drawn
  assert.deepEqual(gutter(p), MARKS);
  assert.equal(p.st.git.shows.length, 2, "one request for each file: the first tab's text is kept");
  tab.onHide();
  tab.model.type("one more\n"); // while hidden: the gutter does not see it
  second.onAttach();
  tab.onAttach();
  assert.deepEqual(gutter(p), [["dk-g-mod", 1, 1]], "drawn again for the buffer as it is now");
});

test("a path:line link bands the line with dk-target in Code and Changes, and the first change of the text takes the band away", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), "l1\nl2\nl3\nl4\nl5\n", { opts: { line: 3 } });
  const band = () => tab.model.decor.list().map((d) => [d.options.className, d.options.marginClassName, d.range.startLineNumber, d.range.endLineNumber, d.options.isWholeLine]);
  assert.deepEqual(band(), [["dk-target", "dk-target-m", 3, 3, true]]);
  changesBtn(tab).click();
  const d = p.st.diffs[0];
  assert.deepEqual(band(), [["dk-target", "dk-target-m", 3, 3, true]], "a decoration of the model: the new side of the diff has it too");
  [...d.updated].forEach((f) => f());
  assert.deepEqual(d.modified.revealed, [3], "Changes opens at the line the link asked for");
  assert.equal(d.revealed, 0, "not at the first change");
  tab.revealLine(5); // another link, to the open tab
  assert.deepEqual(d.modified.revealed, [3, 5]);
  assert.deepEqual(band(), [["dk-target", "dk-target-m", 5, 5, true]], "one band: the new line's");
  tab.model.type("changed\n");
  assert.deepEqual(band(), [], "an edit takes it away");
});

test("the toggle: Code and Changes are a pressed pair; a press on Code in Code does nothing but give the keyboard", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  const { el, code, changes } = tab.seg;
  assert.equal(el.className, "seg dk-seg");
  assert.equal(el.getAttribute("role"), "group");
  assert.deepEqual([code.textContent, changes.textContent], ["Code", "Changes"]);
  assert.equal(code.getAttribute("aria-pressed"), "true");
  assert.equal(changes.getAttribute("aria-pressed"), "false");
  assert.equal(changes.title, "Changes vs HEAD");
  assert.ok(tab.actsEl.children.indexOf(el) < tab.actsEl.children.indexOf(tab.moreBtn), "before the ⋯");
  code.click();
  assert.equal(p.st.diffs.length, 0);
  changes.click();
  changes.click();
  assert.equal(p.st.diffs.length, 1, "a second press on Changes does not make a second view");
  assert.equal(tab.seg.changes.getAttribute("aria-pressed"), "true");
});

test("the stored scroll of a tab is where the file is read, in Code and in Changes (layout.js savedScroll takes fileScroll once `changes` is set)", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  const ed = p.st.editors[0];
  ed.top = 360;
  const saved = (t) => (t.changes ? t.fileScroll : t.scroll); // layout.js savedScroll
  assert.ok(tab.changes, "changes is set in Code too: the gutter's data");
  assert.equal(tab.scroll, 360);
  assert.equal(saved(tab), 360, "not the 0 of a viewer that never scrolled");
  changesBtn(tab).click();
  ed.top = 0; // whatever the hidden editor does
  assert.equal(tab.scroll, 360, "in Changes the file's place is the one it was left at");
  assert.equal(saved(tab), 360);
});

test("Changes: Mod+S in the tab still saves the buffer (a save is not typing); the view follows what was saved", async () => {
  const { p, tab } = await withGit(okBlob(HEAD_TEXT), BUF_TEXT);
  tab.model.type("saved text\n");
  changesBtn(tab).click();
  tab.el.listeners.keydown(keydown());
  await p.settle();
  assert.equal(p.st.writes.length, 1);
  assert.equal(p.st.writes[0].content, "saved text\n");
  assert.equal(tab.dirty(), false);
  assert.ok(tab.diff, "still in Changes");
});

test("editordiff's pure pieces: the label, the answer's reading, the decorations", async () => {
  const p = page();
  const label = p.get("editorDiffLabel");
  assert.equal(label("head", ""), "vs HEAD");
  assert.equal(label("branch", "origin/main"), "vs origin/main (merge base)");
  assert.equal(label("branch", ""), "vs the merge base");
  const base = p.get("editorDiffBase");
  assert.deepEqual(base({ state: "ok", content: "x" }), { head: "x", missing: false, why: "" });
  assert.deepEqual(base({ state: "ok", missing: true, content: "" }), { head: "", missing: true, why: "" });
  assert.equal(base({ state: "ok", content: "partial", truncated: true }).head, null, "a text cut at the cap is not a base to diff against");
  assert.equal(base({ state: "ok" }).head, "", "an ok answer with no content is an empty file");
  assert.equal(base(undefined).head, null);
  assert.equal(base({ state: "weird" }).head, null);
  const decs = p.get("editorDiffDecorations")([{ kind: "add", from: 1, to: 2 }, { kind: "del", from: 4, to: 4 }, { kind: "mod", from: 3, to: 9 }], 3, 1);
  assert.deepEqual(decs.map((d) => [d.options.linesDecorationsClassName, d.range.startLineNumber, d.range.endLineNumber]), [["dk-g-add", 1, 2], ["dk-g-del", 3, 3], ["dk-g-mod", 3, 3]], "clamped to the buffer");
  assert.deepEqual(decs.map((d) => d.options.overviewRuler.position), [1, 1, 1]);
  assert.ok(decs.every((d) => d.range.startColumn === 1 && d.range.endColumn > 1000));
});
