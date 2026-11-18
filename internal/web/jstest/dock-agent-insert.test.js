"use strict";
// The Full-mode agent strip's Insert at prompt (dockagent.js dockAgentInsert, app.js dockShowTerminal), founder 2026-11-18: it first
// LEAVES Full mode by the action of Show terminal, then asks the insert decision on the prompt that is on screen, and types
// `rangeRef + " "` (or the file's path when nothing is selected) or, when the decision still refuses, toasts why and types nothing.
// Run on insertpage.js's page: the REAL tabs.js layout (panes, tabs, maximize), the real insertdecision.js and insertvisible.js,
// the real editorref.js, editorselbar.js, viewerpath.js, upload.js and dockagent.js, and the real dockShowTerminal, paintDrawerMax
// and setDrawerMax cut out of app.js. Stood in: applyDrawer (forced Full mode closes the dock through it; dock-layout.test.js runs the
// real one and pins that it applies the new state and then runs `after`), the dock's editor (a selection the test sets) and the
// layout calls that need a browser. "Moves no focus" is read as: nothing but what leaving Full mode does (Show terminal gives the
// session's terminal the keyboard); a refusal adds none and types nothing.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page } = require("./insertpage.js");

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
  const o = Object.assign({ forced: false, selection: null, filePath: "/work/a.go", box: "", activity: "idle", need: "", holdClose: false, file: true }, opts);
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
  // holdClose: a dirty tab's discard ask the user answers Cancel to, so nothing is applied and `after` never runs.
  p.ctx.applyDrawer = (next, op) => {
    log.applied.push(next);
    if (o.holdClose) return;
    p.run("drawerState = { open: null, sessionId: " + JSON.stringify(next.sessionId) + ", pinned: false }");
    p.drawer.hidden = true;
    p.run('paintDrawerMax(false, false)');
    // The dock is gone, and so is its editor: what the strip typed has to have been read before.
    p.run("dockEditorOwner = null; dockEditor = null;");
    p.ctx.dockAttached = () => null;
    if (op && typeof op.after === "function") op.after();
  };
  // The dock's editor and its active tab, with the selection the test gave it.
  p.ctx.__tab = o.file ? { path: o.filePath, box: o.box, model: {}, disposed: false, inner: null, diff: null, host: { sessionId: () => "a" } } : null;
  p.run("let dockEditor = null, dockEditorOwner = null;");
  if (o.file) {
    p.ctx.__sel = o.selection;
    p.run("dockEditor = { getSelection: () => __sel || { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 } }; dockEditorOwner = __tab;");
  }
  p.ctx.dockAttached = () => p.ctx.__tab;
  // Full mode, chosen or forced, as paintDrawerMax writes it.
  p.drawer.hidden = false;
  p.run("paintDrawerMax(true, " + (o.forced ? "true" : "false") + ")");
  const sent = () => p.st.sent.a || [];
  return { p, o, log, term, sent, press: () => p.run("dockAgentInsert()"), toasts: () => p.st.toasts, full: () => p.drawer.dataset.max === "1", dockOpen: () => !p.drawer.hidden };
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

test("forced Full mode with an edit that holds the close behind its discard ask: until Discard nothing happens, and on Cancel nothing ever does", () => {
  const s = strip({ forced: true, selection: SEL, holdClose: true });
  s.press();
  assert.equal(s.dockOpen(), true, "the dock is still up");
  assert.equal(s.full(), true, "still Full mode");
  assert.deepEqual(s.sent(), []);
  assert.deepEqual(s.toasts(), [], "no toast either: the user has not been refused anything");
  assert.deepEqual(s.p.st.focused, [], "and no focus moved");
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

test("the strip's Insert reuses Show terminal and the one insert; it has no way of its own to leave Full mode or to the prompt", () => {
  const code = (t) => t.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  const body = code(fnSource("dockagent.js", "dockAgentInsert"));
  assert.match(body, /dockShowTerminal\(\(\) => \{ editorInsertRef\(want\); \}\);/);
  assert.doesNotMatch(body, /setDrawerMax|applyDrawer|paintDrawerMax|insertTextAtPrompt|sendText|sendKey|\\n|\\r/);
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
