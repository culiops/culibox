"use strict";
// Insert at prompt from Full mode: the agent strip's (dockagent.js dockAgentInsert) and the selection bar's (editorselbar.js), both
// through editorInsertLeavingFull and app.js dockShowTerminal, founder 2026-11-18 (ruling R14 for the bar): it first LEAVES Full
// mode by the action of Show terminal, then asks the insert decision on the prompt that is on screen, and types `rangeRef + " "` (or,
// for the strip with nothing selected, the file's path) or, when the decision still refuses, toasts why and types nothing. Outside
// Full mode the bar's Insert asks at once and nothing is left.
// Run on insertpage.js's page: the REAL tabs.js layout (panes, tabs, maximize), the real insertdecision.js and insertvisible.js,
// the real editorref.js, editorselbar.js, viewerpath.js, upload.js and dockagent.js, and the real dockShowTerminal, paintDrawerMax
// and setDrawerMax cut out of app.js. The bar is the real widget on editorpage.js's fake Monaco editor. Stood in: applyDrawer (forced Full mode closes the dock through it; dock-layout.test.js runs the
// real one and pins that it applies the new state and then runs `after`), drawerHasUnsaved (filesdrawer.js: does the dock hold an edit; the
// test says), the dock's editor (a selection the test sets) and the
// layout calls that need a browser. "Moves no focus" is read as: nothing but what leaving Full mode does (Show terminal gives the
// session's terminal the keyboard); a refusal adds none and types nothing.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page } = require("./insertpage.js");
const { fakeEditor } = require("./editorpage.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
}

// A page with session a (alpha, idle) in one pane, the dock open on it in Full mode (forced or chosen) and a file in the dock.
function strip(opts) {
  const o = Object.assign({ forced: false, full: true, bar: false, sid: "a", selection: null, filePath: "/work/a.go", box: "", activity: "idle", need: "", dirty: false, file: true }, opts);
  const p = page();
  p.sessions({ id: "a", name: "alpha", activity: o.activity, need: o.need });
  const cell = p.pane(p.term("a", "alpha"));
  p.focus(cell);
  const term = p.ctx.terminalTabFor("a");
  for (const id of ["drawer-max", "drawer-max-label"]) { const n = new p.DNode("span"); n.id = id; p.els[id] = n; p.doc.body.appendChild(n); }
  p.ctx.$ = (id) => p.els[id] || null;
  for (const [f, n] of [["app.js", "paintDrawerMax"], ["app.js", "setDrawerMax"], ["app.js", "dockShowTerminal"]]) p.run(fnSource(f, n));
  p.run(src("viewerpath.js"));
  p.run(src("upload.js"));
  p.run(src("editorselbar.js"));
  p.run(src("dockagent.js"));
  const log = { applied: [], refit: 0 };
  p.run('let drawerState = { open: "files", sessionId: "a", pinned: false };');
  p.ctx.positionDrawer = () => {};
  p.ctx.dockRefit = () => { log.refit++; };
  p.run("setFocusedCell = function (c) { setFocusedCellRaw(c); };"); // tabs.js's also saves the layout (a timer)
  p.run("dockAgentSync = function () {};"); // the strip's poll is dockagent.test.js's
  // applyDrawer, as far as Show terminal depends on it: the state is applied, Full mode ends with the dock, then `after` runs.
  // `dirty`: the dock holds an unsaved edit. Leaving a forced Full mode closes the dock, which would ask whether to discard it: an Insert
  // never gets that far (ruling R17), so applyDrawer here never has to play the ask.
  p.ctx.drawerHasUnsaved = () => o.dirty;
  p.ctx.applyDrawer = (next, op) => {
    log.applied.push(next);
    p.run("drawerState = { open: null, sessionId: " + JSON.stringify(next.sessionId) + ", pinned: false }");
    p.drawer.hidden = true;
    p.run('paintDrawerMax(false, false)');
    // The dock is gone, and so is its editor: what the strip typed has to have been read before.
    p.run("dockEditorOwner = null; dockEditor = null;");
    p.ctx.dockAttached = () => null;
    if (op && typeof op.after === "function") op.after();
  };
  // The dock's editor and its active tab, with the selection the test gave it.
  p.ctx.__tab = o.file ? {
    path: o.filePath, box: o.box, model: { onDidChangeContent: () => ({ dispose() {} }) }, disposed: false, inner: null, diff: null, attached: true,
    host: { sessionId: () => o.sid }, focusReader() {},
  } : null;
  p.run("let dockEditor = null, dockEditorOwner = null;");
  let ed = null;
  if (o.file && o.bar) {
    // The real selection bar on the fake Monaco editor, which holds the tab's model and the keyboard.
    ed = fakeEditor();
    ed.setModel(p.ctx.__tab.model);
    p.ctx.__ed = ed;
    p.ctx.__monaco = { editor: { ContentWidgetPositionPreference: { ABOVE: 1, BELOW: 2 } }, KeyCode: { Escape: 9 } };
    p.run("dockEditor = __ed; dockEditorOwner = __tab; editorSelBarInstall(__ed, __monaco);");
    p.ctx.__tab.focusReader = () => ed.focus();
    ed.focus();
    if (o.selection) { const q = o.selection; ed.select(q.startLineNumber, q.startColumn, q.endLineNumber, q.endColumn); }
  } else if (o.file) {
    p.ctx.__sel = o.selection;
    p.run("dockEditor = { getSelection: () => __sel || { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 } }; dockEditorOwner = __tab;");
  }
  p.ctx.dockAttached = () => p.ctx.__tab;
  // Full mode, chosen or forced, as paintDrawerMax writes it (or the pushed dock, beside the panes).
  p.drawer.hidden = false;
  if (o.full) p.run("paintDrawerMax(true, " + (o.forced ? "true" : "false") + ")");
  const sent = () => p.st.sent.a || [];
  // The bar's Insert at prompt, the first of its buttons.
  const barInsert = () => { assert.equal(ed.widgets.length, 1, "the bar is up"); ed.widgets[0].getDomNode().children[0].click(); };
  return { p, o, log, term, sent, ed, barInsert, press: () => p.run("dockAgentInsert()"), toasts: () => p.st.toasts, full: () => p.drawer.dataset.max === "1", dockOpen: () => !p.drawer.hidden };
}

const SEL = { startLineNumber: 3, startColumn: 1, endLineNumber: 5, endColumn: 12 };

test("in a chosen Full mode, Insert leaves Full mode first and then types the selection's path:a-b and a space at the prompt, no Enter", () => {
  const s = strip({ selection: SEL });
  assert.equal(s.full(), true, "the strip is up: Full mode");
  s.press();
  assert.equal(s.full(), false, "Full mode is over (the very action of Show terminal: setDrawerMax(false))");
  assert.equal(s.dockOpen(), true, "the pushed dock stays, beside the panes");
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "], "typed at the session's prompt");
  assert.ok(!/[\r\n\x00-\x1f]/.test(s.sent()[0]), "no Enter, no control byte");
  assert.deepEqual(s.toasts(), []);
  assert.ok(s.log.refit >= 1, "the layout was done again before the decision looked at the prompt");
  assert.deepEqual(s.p.st.focused, ["a", "a"], "Show terminal gave the terminal the keyboard, and the insert's success keeps it there");
});

test("a selection of one line is path:a, and with nothing selected the file's path is typed", () => {
  const one = strip({ selection: { startLineNumber: 8, startColumn: 1, endLineNumber: 9, endColumn: 1 } });
  one.press();
  assert.deepEqual(one.sent(), ["/work/a.go:8 "]);
  const none = strip({ selection: null });
  none.press();
  assert.deepEqual(none.sent(), ["/work/a.go "]);
  const spaced = strip({ selection: null, filePath: "/work/my file.go" });
  spaced.press();
  assert.deepEqual(spaced.sent(), ["'/work/my file.go' "], "quoted for a shell, like the tree's insert");
});

test("Insert asks the decision AFTER Full mode is over, so a prompt that was covered a moment ago is typed into", () => {
  const s = strip({ selection: SEL });
  const asked = [];
  s.p.run("const __ask = insertRefused; insertRefused = function (t, l) { __log(document.getElementById('drawer').dataset.max === '1'); return __ask(t, l); };");
  s.p.ctx.__log = (full) => asked.push(full);
  s.press();
  assert.deepEqual(asked, [false], "asked once, with Full mode already off");
  assert.equal(s.sent().length, 1);
  // The same press in Full mode refuses outright: that is the decision's own rule, and the reason the button leaves first.
  const direct = strip({ selection: SEL });
  const seen = direct.p.decide(direct.term);
  assert.equal(seen.ok, false);
  assert.match(seen.why, /covered by the dock in Full mode/);
});

test("when the decision still refuses (the session waits on you), Full mode is left, a toast says why, nothing is typed and no focus is added", () => {
  const s = strip({ selection: SEL, activity: "waiting", need: "ask" });
  s.press();
  assert.equal(s.full(), false, "the user lands on the terminal that is asking, which is the way to answer it");
  assert.deepEqual(s.sent(), [], "nothing typed");
  assert.deepEqual(s.toasts(), [["alpha is waiting on you; answer it first", true]]);
  assert.deepEqual(s.p.st.focused, ["a"], "only Show terminal's own focus; the refusal adds none");
});

test("a session that finished its turn (need: turn) takes the reference; a working one does too", () => {
  for (const [activity, need] of [["waiting", "turn"], ["working", ""]]) {
    const s = strip({ selection: SEL, activity, need });
    s.press();
    assert.deepEqual(s.sent(), ["/work/a.go:3-5 "], activity + "/" + need);
  }
});

test("in a FORCED Full mode (a narrow window) the dock is closed first, and the reference read before it closed is what is typed", () => {
  const s = strip({ forced: true, selection: SEL });
  assert.equal(s.p.drawer.dataset.forced, "1");
  s.press();
  assert.deepEqual(s.log.applied.map((a) => a.open), [null], "applyDrawer({open: null}): the dock closed, as Show terminal does");
  assert.equal(s.dockOpen(), false);
  assert.equal(s.full(), false);
  assert.equal(s.p.drawer.dataset.forced, undefined);
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "], "typed from applyDrawer's `after`, though the dock's editor and tab were gone by then");
  assert.deepEqual(s.toasts(), []);
});

test("forced Full mode with the session waiting: the dock closes, a toast says why, nothing is typed", () => {
  const s = strip({ forced: true, selection: SEL, activity: "waiting", need: "sso" });
  s.press();
  assert.equal(s.dockOpen(), false);
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.toasts(), [["alpha is waiting on you; answer it first", true]]);
  assert.deepEqual(s.p.st.focused, ["a"]);
});

test("forced Full mode with an unsaved edit: Insert refuses with 'Save first' instead of closing the dock into its discard ask", () => {
  const s = strip({ forced: true, selection: SEL, dirty: true });
  s.press();
  assert.deepEqual(s.toasts(), [["Save first: leaving Full mode would close the dock", true]]);
  assert.deepEqual(s.log.applied, [], "the dock was not asked to close: no discard question reaches the user");
  assert.equal(s.dockOpen(), true, "the dock is still up");
  assert.equal(s.full(), true, "still Full mode");
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.p.st.focused, [], "and no focus moved");
});

test("a chosen Full mode with an unsaved edit is not refused: leaving it keeps the dock and the edit, so the insert goes ahead", () => {
  const s = strip({ selection: SEL, dirty: true });
  s.press();
  assert.deepEqual(s.toasts(), []);
  assert.equal(s.full(), false);
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "]);
});

test("with no file in the dock there is nothing to insert: a toast, and Full mode is not left", () => {
  const s = strip({ file: false });
  s.press();
  assert.deepEqual(s.toasts(), [["Open a file in the dock first: that is what Insert at prompt types", true]]);
  assert.equal(s.full(), true);
  assert.deepEqual(s.sent(), []);
});

test("a session with no terminal on this page: Full mode is left and a toast says so", () => {
  const s = strip({ selection: SEL });
  s.p.run("terminalTabFor = function () { return null; };");
  s.press();
  assert.equal(s.full(), false);
  assert.deepEqual(s.sent(), []);
  assert.equal(s.toasts().length, 1);
  assert.equal(s.toasts()[0][0], "Open this file from a session's pane to insert at its prompt");
});

test("a file in another session's box is not typed at this session's prompt", () => {
  const s = strip({ selection: SEL, box: "other" });
  s.press();
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.toasts(), [["This file is inside another session's box", true]]);
});

test("a path with a control character is refused and nothing is typed", () => {
  const s = strip({ selection: null, filePath: "/work/a\nb.go" });
  s.press();
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.toasts(), [["That path contains control characters, so it was not inserted", true]]);
});

test("no file in the dock and no session are two different toasts", () => {
  const nofile = strip({ file: false });
  nofile.press();
  assert.deepEqual(nofile.toasts(), [["Open a file in the dock first: that is what Insert at prompt types", true]]);
  const nosess = strip({ sid: null, selection: SEL });
  nosess.press();
  assert.deepEqual(nosess.toasts(), [["This dock has no session, so there is no prompt to insert at", true]]);
  assert.equal(nosess.full(), true, "Full mode is not left for either");
  assert.deepEqual(nosess.sent(), []);
});

// --- the selection bar's Insert in Full mode (ruling R14) ---------------------------------------------------------------------

test("the bar is drawn in Full mode, and its Insert leaves Full mode first, then types path:a-b and a space at the prompt, no Enter", () => {
  const s = strip({ bar: true, selection: SEL });
  assert.equal(s.full(), true);
  const asked = [];
  s.p.run("const __ask = insertRefused; insertRefused = function (t, l) { __log(document.getElementById('drawer').dataset.max === '1'); return __ask(t, l); };");
  s.p.ctx.__log = (full) => asked.push(full);
  s.barInsert();
  assert.equal(s.full(), false, "Full mode is over (Show terminal's action)");
  assert.equal(s.dockOpen(), true, "the pushed dock stays beside the panes");
  assert.deepEqual(asked, [false], "the decision is asked once, after Full mode is off");
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "]);
  assert.ok(!/[\r\n\x00-\x1f]/.test(s.sent()[0]), "no Enter, no control byte");
  assert.deepEqual(s.toasts(), []);
  assert.deepEqual(s.p.st.focused, ["a", "a"], "Show terminal gave the terminal the keyboard; the insert's success keeps it there");
});

test("the bar's Insert in Full mode with the session waiting: Full mode is left, a toast says why, nothing is typed, no focus is added", () => {
  const s = strip({ bar: true, selection: SEL, activity: "waiting", need: "ask" });
  s.barInsert();
  assert.equal(s.full(), false, "the user lands on the terminal that is asking");
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.toasts(), [["alpha is waiting on you; answer it first", true]]);
  assert.deepEqual(s.p.st.focused, ["a"], "only Show terminal's own focus");
});

test("the bar's Insert in a FORCED Full mode: the dock closes first and the reference read before it closed is typed from applyDrawer's `after`", () => {
  const s = strip({ bar: true, forced: true, selection: SEL });
  assert.equal(s.p.drawer.dataset.forced, "1");
  s.barInsert();
  assert.deepEqual(s.log.applied.map((a) => a.open), [null], "applyDrawer({open: null}), as Show terminal does");
  assert.equal(s.dockOpen(), false);
  assert.equal(s.full(), false);
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "], "though the editor and its tab were gone by then");
  assert.deepEqual(s.toasts(), []);
});

test("forced Full mode, session waiting: the bar's Insert closes the dock, toasts why and types nothing", () => {
  const waiting = strip({ bar: true, forced: true, selection: SEL, activity: "waiting", need: "sso" });
  waiting.barInsert();
  assert.equal(waiting.dockOpen(), false);
  assert.deepEqual(waiting.sent(), []);
  assert.deepEqual(waiting.toasts(), [["alpha is waiting on you; answer it first", true]]);
});

test("forced Full mode with an unsaved edit: the bar's Insert refuses with 'Save first' too, and neither closes the dock nor types", () => {
  const held = strip({ bar: true, forced: true, selection: SEL, dirty: true });
  held.barInsert();
  assert.deepEqual(held.toasts(), [["Save first: leaving Full mode would close the dock", true]]);
  assert.deepEqual(held.log.applied, [], "no close, so no discard ask");
  assert.equal(held.dockOpen(), true);
  assert.equal(held.full(), true);
  assert.deepEqual(held.sent(), []);
  assert.deepEqual(held.p.st.focused, []);
  // Saved (or discarded by the user's own doing), the same press goes through: the dock closes and the reference is typed.
  held.p.ctx.drawerHasUnsaved = () => false;
  held.barInsert();
  assert.deepEqual(held.sent(), ["/work/a.go:3-5 "]);
});

test("outside Full mode nothing is left: the bar's Insert asks the decision at once and types at the visible prompt", () => {
  const s = strip({ bar: true, full: false, selection: SEL });
  assert.equal(s.dockOpen(), true);
  assert.equal(s.full(), false);
  s.barInsert();
  assert.deepEqual(s.log.applied, [], "the dock was not touched");
  assert.equal(s.log.refit, 0, "and no layout was redone");
  assert.equal(s.dockOpen(), true);
  assert.deepEqual(s.sent(), ["/work/a.go:3-5 "]);
  assert.deepEqual(s.toasts(), []);
  // The decision still has its say there: a waiting session refuses, with nothing left either.
  const waiting = strip({ bar: true, full: false, selection: SEL, activity: "waiting", need: "ask" });
  waiting.barInsert();
  assert.deepEqual(waiting.sent(), []);
  assert.deepEqual(waiting.toasts(), [["alpha is waiting on you; answer it first", true]]);
  assert.deepEqual(waiting.log.applied, []);
});

test("the strip's Insert reuses Show terminal and the one insert; it has no way of its own to leave Full mode or to the prompt", () => {
  const code = (t) => t.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const body = code(fnSource("dockagent.js", "dockAgentInsert"));
  assert.match(body, /editorInsertLeavingFull\(want\);/);
  assert.doesNotMatch(body, /setDrawerMax|applyDrawer|paintDrawerMax|insertTextAtPrompt|sendText|sendKey|\\n|\\r/);
  const leaving = code(fnSource("editorselbar.js", "editorInsertLeavingFull"));
  assert.match(leaving, /editorRunLeavingFull\(\(\) => \{ editorInsertRef\(want\); \}\);/, "the shared flow, then the one insert; nothing of its own");
  assert.doesNotMatch(leaving, /setDrawerMax|applyDrawer|paintDrawerMax|insertTextAtPrompt|sendText|sendKey/);
  // The shared flow (also the review bar's Paste): the forced-Full refusal, then Show terminal's own function, `run` from its `then`.
  const flow = code(fnSource("editorselbar.js", "editorRunLeavingFull"));
  assert.match(flow, /if \(editorLeavingWouldDiscard\(\)\) \{ showToast\(EDITOR_SAVE_FIRST, \{ error: true \}\); return; \}\n\s*if \(!dockShowTerminal\(run\)\) run\(\);/);
  assert.doesNotMatch(flow, /setDrawerMax|applyDrawer|paintDrawerMax|insertTextAtPrompt|sendText|sendKey/);
  assert.match(code(fnSource("editorselbar.js", "editorSelBarBuild")), /editorInsertLeavingFull\(editorRefFor\(esbTab, esbLines\)\)/, "the bar's Insert goes through it too");
  assert.match(code(src("app.js")), /\$\("dock-agent-ins"\)\.onclick = \(\) => dockAgentInsert\(\);/);
  const html = src("index.html");
  const at = html.indexOf('id="dock-agent-ins"');
  assert.ok(at > html.indexOf('id="dock-agent"') && at < html.indexOf('id="dock-agent-term"'), "in the strip, before Show terminal");
  assert.match(html.slice(html.lastIndexOf("<button", at), html.indexOf("</button>", at)), /type="button"[^>]*>.*Insert at prompt/);
  // Show terminal's own function is the one the Ctrl+` chord and the strip's button use, and it still hands the key back outside Full mode.
  assert.match(fnSource("app.js", "dockShowTerminal"), /if \(!dr \|\| dr\.hidden \|\| dr\.dataset\.max !== "1"\) return false;/);
});

test("the strip paints Insert disabled while the dock has no file, with the reason as its title", () => {
  const els = {};
  const mk = (id) => (els[id] = { id, disabled: false, title: "", textContent: "", className: "", dataset: {} });
  for (const id of ["dock-agent-ins", "dock-agent-dot", "dock-agent-name", "dock-agent-st", "dock-agent-last"]) mk(id);
  const vm = require("node:vm");
  let tab = null;
  const ctx = vm.createContext({
    console, Array, Object, document: { getElementById: (id) => els[id] || null, addEventListener() {}, hidden: false },
    drawerState: { sessionId: "a" }, lastSessions: [{ id: "a", name: "alpha" }], deskDot: () => ({ cls: "dot", title: "" }), drawerSessionName: () => "alpha",
    dockAttached: () => tab,
  });
  new vm.Script(src("dockagent.js")).runInContext(ctx);
  new vm.Script("dockAgentPaint()").runInContext(ctx);
  assert.equal(els["dock-agent-ins"].disabled, true);
  assert.equal(els["dock-agent-ins"].title, "Open a file in the dock first");
  tab = { path: "/w/a.go" };
  new vm.Script("dockAgentPaint()").runInContext(ctx);
  assert.equal(els["dock-agent-ins"].disabled, false);
  assert.match(els["dock-agent-ins"].title, /^Leave Full mode and insert/);
});
