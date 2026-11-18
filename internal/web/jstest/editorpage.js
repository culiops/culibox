"use strict";
// editorpage.js: the page the editor tests run on. Not a test itself (the runner takes *.test.js only).
//
// The real editortab.js, editorsync.js, editordiff.js and editormd.js (and the pure modules they call) in a vm, against a fake page
// (fakedom.js) and a fake Monaco, with fsRead and fsWrite stubbed. editortab-contract.test.js drives it for the save, disk and
// Changes contracts; editor-follow.test.js and editor-selbar.test.js drive it for Follow agent and the selection bar. What the
// fake Monaco has to be exact about is what each of them says in its own header.
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

// The editor's events are the ones the dock and the selection bar subscribe to; `fire(name)` plays one (a test moves the selection with
// select(), gives or takes the keyboard with focus() and blur(), presses a key with key()). The selection, the keyboard, the content
// widgets and the lines revealed are kept for the tests to read, as the real editor's would be asked.
function fakeEditor() {
  const subs = {};
  const on = (name) => (fn) => {
    (subs[name] = subs[name] || []).push(fn);
    return { dispose: () => { subs[name] = (subs[name] || []).filter((x) => x !== fn); } };
  };
  return {
    model: null, commands: [], options: [], top: 0, restored: [], decor: decorStore(), focused: 0,
    textFocus: false, sel: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 }, widgets: [], layouts: 0, positions: [], centered: [],
    subs,
    fire(name, ev) { for (const fn of [...(subs[name] || [])]) fn(ev || {}); },
    // A cursor and a scroll are lost to a replace of the text, as Monaco loses them: unless they are put back they are at the top.
    // The decorations this editor made go when its model does (Monaco's _postDetachModelCleanup).
    setModel(m) {
      if (m !== this.model) { this.decor.live.clear(); this.sel = { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 }; }
      const changed = m !== this.model;
      this.model = m;
      if (m) m.onDidChangeContent((e) => { if (e.replaced) this.top = 0; });
      if (changed) this.fire("model");
    },
    deltaDecorations(old, next) { return this.decor.delta(old, next); },
    getModel() { return this.model; },
    addCommand(keys, fn) { this.commands.push([keys, fn]); },
    onDidChangeCursorPosition() { return { dispose() {} }; },
    onDidChangeCursorSelection: on("selection"), onDidFocusEditorText: on("focus"), onDidBlurEditorText: on("blur"),
    onDidChangeModel: on("model"), onKeyDown: on("key"),
    updateOptions(o) { this.options.push(o); },
    layout() {}, saveViewState() { return { saved: true, top: this.top }; }, restoreViewState(v) { this.restored.push(v); this.top = v.top; },
    getScrollTop() { return this.top; }, setScrollTop(v) { this.top = v; }, getPosition() { return this.pos || { lineNumber: 1, column: 1 }; },
    setPosition(p) { this.positions.push(p); this.pos = p; }, revealLineInCenter(l) { this.centered.push(l); },
    focus() { this.focused++; this.textFocus = true; this.fire("focus"); },
    blur() { this.textFocus = false; this.fire("blur"); },
    hasTextFocus() { return this.textFocus; },
    getSelection() { return this.sel; },
    select(sl, sc, el, ec) { this.sel = { startLineNumber: sl, startColumn: sc, endLineNumber: el, endColumn: ec }; this.fire("selection"); },
    key(keyCode) { this.fire("key", { keyCode }); },
    addContentWidget(w) { this.widgets.push(w); },
    removeContentWidget(w) { this.widgets = this.widgets.filter((x) => x !== w); },
    layoutContentWidget() { this.layouts++; },
  };
}

// One page per test: the editor, the owner and the loaded Monaco are the script's own top-level lets.
function page(over) {
  const doc = { activeElement: null, hidden: false };
  doc.createElement = (t) => new El(t, doc);
  doc.getElementById = () => null;
  const st = {
    writes: [], reads: [], urls: [], toasts: [], models: [], editors: [], diffs: [], host: [], closed: [], log: [], gitRefresh: 0,
    timers: [], intervals: [], renders: [],
    shown: false, page: null, session: "fix-fs-cap", // is the dock on screen; a page view over it; the session the dock shows
    // The page's git, off until a test turns it on (on: the tree's status is known and names HEAD `head`): the scope the Changes
    // section shows, the base it names, and what POST /api/git/show answers (an object, a function of the request body, or null
    // for a request that failed), delivered after `gate` if one is set.
    git: { on: false, head: "h1", scope: "head", base: null, answer: null, gate: null, shows: [] },
    metaGate: null, fullGate: null,                  // a promise a metadata / a full read waits on, after its answer is decided
  };
  const monaco = {
    KeyCode: { KeyS: 49, KeyZ: 56, F7: 65, Escape: 9 },
    KeyMod: { CtrlCmd: 2048, WinCtrl: 256, Shift: 1024, Alt: 512 },
    languages: { getLanguages: () => [{ id: "go", aliases: ["Go"] }, { id: "plaintext", aliases: ["Plain Text"] }] },
    editor: {
      EndOfLinePreference: { TextDefined: 0 }, ScrollType: { Immediate: 1 }, ContentWidgetPositionPreference: { EXACT: 0, ABOVE: 1, BELOW: 2 },
      EndOfLineSequence: { LF: 0, CRLF: 1 },
      OverviewRulerLane: { Left: 1, Center: 2, Right: 4, Full: 7 },
      createModel(text, lang) { const m = fakeModel(text, lang, st.log, st.models.length + 1); st.models.push(m); return m; },
      create(el, opts) { const e = fakeEditor(); e.createOpts = opts; st.editors.push(e); return e; },
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
    const d = { kind: "text", size: disk.text.length, version: hash(disk.text), readonly: false, utf8: disk.utf8 !== false };
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
    // The markdown Preview's render (mdsafe.js, tested on its own in md-render.test.js): here a fragment holding the text it was
    // asked for, so what a test sees is when the preview draws and from which buffer.
    MD_EXTS: new Set(["md", "markdown"]),
    marked: { parse: () => "" },
    DOMPurify: {},
    mdRenderFragment: (text, deps) => {
      st.renders.push({ text, deps });
      const f = new El("div", doc);
      f.className = "rendered";
      f.textContent = text;
      return f;
    },
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
    // The diff's prefs live here for the test: the mode ("side" the default) and the diff's wrap.
    viewerPrefs: (() => {
      const v = { diffMode: "side", diffWrap: false };
      return { getWrap: () => false, getDiffMode: () => v.diffMode, setDiffMode: (m) => { v.diffMode = m; }, getDiffWrap: () => v.diffWrap, setDiffWrap: (b) => { v.diffWrap = b; } };
    })(),
    toggleViewerWrap: () => {},
    ViewerTab: class {
      constructor(p, o) { this.path = p; this.opts = o; this.calls = []; this.el = new El("div", doc); this.scroll = 7; st.inner = this; }
      onShow() { this.calls.push("show"); } onAttach() { this.calls.push("attach"); } onHide() { this.calls.push("hide"); }
      dispose() { this.calls.push("dispose"); } focusReader() {} revealLine() {} wrapAnchor() { return () => {}; } applyWrap() {}
    },
  }, over));
  const load = (f) => new vm.Script(src(f), { filename: f }).runInContext(ctx);
  for (const f of ["shortcuts.js", "monacokeys.js", "monacolang.js", "editorref.js", "editorstatus.js", "fsedit.js", "copyfile.js", "viewerpath.js", "viewerlines.js", "linediff.js", "editortab.js", "reviewmodel.js", "editorsync.js", "editordiff.js", "imagediff.js", "editorreview.js", "editormd.js", "editorselbar.js", "editorfollow.js"]) load(f);
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
    ctx, st, host, doc, monaco, get, load, EditorTab, settle, hash, diskWrite,
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

module.exports = { page, keydown, fakeEditor, fakeModel, decorStore, El };
