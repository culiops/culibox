"use strict";
// Every way to insert a path at a prompt, run against the real decision on real Cell objects (insertpage.js): the tree's
// row menu and the palette (filetree.js insertPathAtPrompt), the viewers' Path menu and line-number insert
// (viewer-tab.js insertPath, with its real insertCandidate and viewerpath.js), and the paths an upload types
// (upload.js insertPathsIntoTerminal). The rule they share (#337): a terminal the user cannot see, or a session waiting
// on them, refuses with a toast that names why, types nothing and moves no focus. A source pin at the end says no insert
// can be added without asking insertDecision first.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page } = require("./insertpage.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

// The text of one top-level function, or one class method (two-space indent), cut from a source file.
function fnText(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
}
function methodText(file, name) {
  const text = src(file);
  const m = new RegExp("^  " + name + "\\(.*\\) \\{$", "m").exec(text);
  assert.ok(m, name + " is a method of " + file);
  return text.slice(m.index, text.indexOf("\n  }\n", m.index) + 4);
}

// A page with two panes (A, B) and the real insert code loaded into it: upload.js whole (shellQuote, insertTextAtPrompt,
// insertPathsIntoTerminal), the tree's insertPathAtPrompt, and the real insertCandidate and insertPath of ViewerTab on a
// stand-in viewer whose only own fields are the ones those two methods read.
function callersPage() {
  const p = page();
  p.names({ a: "alpha", b: "beta" });
  p.ctx.focusTerminal = () => { const t = p.ctx.focusedTerminal(); if (t) t.term.focus(); }; // shell.js's, minus the dialog guard
  p.ctx.dockHasViewer = (v) => v.inDock === true;
  p.run(src("upload.js"));
  p.run(src("viewerpath.js"));
  p.run(fnText("filetree.js", "insertPathAtPrompt"));
  p.run(fnText("filetree.js", "treeInsertTarget"));
  p.run("class ViewerMethods {\n" + methodText("viewer-tab.js", "insertCandidate") + "\n" + methodText("viewer-tab.js", "insertPath") + "\n}");
  p.run("let drawerState = { open: 'files', sessionId: null, pinned: false };");
  // What treeInsertTarget asks of the Files dock: the real drawerKeepsTree and the real docktabs.js rule (dockFollow) behind it.
  // Only "does the dock hold unsaved edits" is a stand-in, a flag the tests set.
  p.run(src("docktabs.js"));
  for (const [file, name] of [["filesdrawer.js", "drawerOwnsFileClicks"], ["filesdrawer.js", "drawerKeepsTree"], ["app.js", "drawerDesktop"],
    ["app.js", "drawerLiveSids"], ["app.js", "drawerSessionName"]]) p.run(fnText(file, name));
  p.run("let heldEdits = false; function drawerHasUnsaved() { return heldEdits; }");
  p.ta = p.term("a", "alpha");
  p.tb = p.term("b", "beta");
  p.ca = p.pane(p.ta);
  p.cb = p.pane(p.tb);
  p.focus(p.cb);
  return p;
}

// The ways a path is inserted: [name, a function of (page, target terminal) that makes the call the app makes, the text it
// types, the toast for a refusal given the decision's sentence, whether success takes the keyboard to the prompt].
// The viewer ones target a dock viewer, whose candidate is the dock's session's terminal (terminalTabFor).
const CALLERS = [
  ["the tree's row menu and the palette: insertPathAtPrompt", (p, t) => { p.ctx.__t = t; p.run("insertPathAtPrompt('/w/x.go', __t)"); }, "/w/x.go ", (why) => why, true],
  ["the viewer's Path menu insert", (p, t) => dockViewerInsert(p, t), "/w/x.go ", (why) => why, true],
  ["the viewer's line-number insert", (p, t) => dockViewerInsert(p, t, 12), "/w/x.go:12 ", (why) => why, true],
  ["an upload's inserted path", (p, t) => { p.ctx.__t = t; p.run("insertPathsIntoTerminal(__t, { uploaded: [{ path: '/w/x.go' }] })"); }, "/w/x.go ",
    (why) => "Uploaded, but the path was not inserted: " + why, false],
];

function dockViewerInsert(p, term, line) {
  p.run("drawerState = { open: 'files', sessionId: " + JSON.stringify(term.id) + ", pinned: false };");
  p.ctx.__v = Object.assign(Object.create(p.run("ViewerMethods.prototype")), { path: "/w/x.go", box: "", inDock: true, focusReader() { p.st.focused.push("reader"); } });
  p.run("__v.insertPath(" + (line === undefined ? "" : line) + ")");
}

// The states: each makes `target` hidden or unfit and returns it, the way the user gets there.
const STATES = [
  ["Full mode", (p) => { p.full(true); return p.ta; }, /Full mode/],
  ["the pane's Activity view", (p) => { p.ca.setView("activity"); return p.ta; }, /Activity/],
  ["the pane's Access view", (p) => { p.ca.setView("access"); return p.ta; }, /Access/],
  ["another pane maximized", (p) => { p.maximize(p.cb); return p.ta; }, /maximized/],
  ["an inactive stacked tab", (p) => { p.names({ a: "alpha", b: "beta", c: "gamma" }); p.cb.addTab(p.term("c", "gamma")); return p.tb; }, /background tab/],
  ["behind a file viewer tab", (p) => { p.cb.addTab(p.viewer("/w/f.go")); return p.tb; }, /behind a file tab/],
  ["a hidden docked shell", (p) => { p.names({ a: "alpha", b: "beta", sh: "shell 1" }); const sh = p.ca.dockShell("sh", "shell 1"); p.ca.hideDock(); return sh; }, /hidden with its dock/],
  ["a page view open", (p) => { p.openPage("settings-modal"); return p.ta; }, /Settings is open/],
  ["the browser tab in the background", (p) => { p.doc.hidden = true; return p.ta; }, /background/],
  ["a pane collapsed to nothing", (p) => { p.ca.el.rect = { width: 0, height: 0 }; return p.ta; }, /not on screen/],
  ["a session waiting on a permission prompt", (p) => { p.names({ a: "alpha", b: "beta" }); p.ctx.lastSessions[0].activity = "waiting"; p.ctx.lastSessions[0].need = "ask"; return p.ta; }, /waiting on you/],
];

for (const [stateName, make, why] of STATES) {
  for (const [callerName, call, , expectToast] of CALLERS) {
    test("refused (" + stateName + "): " + callerName + " shows the reason, types nothing and moves no focus", () => {
      const p = callersPage();
      const target = make(p);
      const reason = p.decide(target);
      assert.strictEqual(reason.ok, false, "the decision refuses this state");
      assert.match(reason.why, why);
      call(p, target);
      assert.deepStrictEqual(p.st.sent, {}, "nothing reached any terminal, hidden or not");
      assert.deepStrictEqual(p.st.focused, [], "no focus moved");
      assert.deepStrictEqual(p.st.toasts, [[expectToast(reason.why), true]]);
    });
  }
}

for (const [callerName, call, typed, , takesFocus] of CALLERS) {
  test("allowed: " + callerName + " types the quoted path and a space at a visible prompt, no Enter, and focuses it", () => {
    const p = callersPage();
    p.sessions(p.sess("a", { name: "alpha", activity: "waiting", need: "turn" }), p.sess("b", { name: "beta", activity: "working" }));
    call(p, p.ta); // A is not the focused pane's terminal: the target is whatever the caller names
    assert.deepStrictEqual(p.st.sent, { a: [typed] });
    assert.ok(!/[\r\n]/.test(typed), "no Enter");
    assert.deepStrictEqual(p.st.toasts, []);
    // The menus and the palette hand the keyboard to the prompt they typed at; an upload finishing later never takes it.
    assert.deepStrictEqual(p.st.focused, takesFocus ? ["a"] : []);
  });
}

test("a refusal leaves the other session's prompt alone as well: nothing is typed anywhere while B is hidden by Full mode", () => {
  const p = callersPage();
  p.full(true);
  for (const [, call] of CALLERS) { call(p, p.ta); call(p, p.tb); }
  assert.deepStrictEqual(p.st.sent, {});
  assert.deepStrictEqual(p.st.focused, []);
  assert.strictEqual(p.st.toasts.length, CALLERS.length * 2);
});

test("the unsafe-path and not-connected refusals are kept, after the decision", () => {
  const p = callersPage();
  p.ctx.__t = p.ta;
  p.run("insertPathAtPrompt('/w/x\\ny.go', __t)");
  assert.match(p.st.toasts[0][0], /control characters/);
  p.ta.sendText = () => false;
  p.run("insertPathAtPrompt('/w/x.go', __t)");
  assert.match(p.st.toasts[1][0], /not connected/);
  assert.deepStrictEqual(p.st.sent, {});
  p.run("insertPathAtPrompt('/w/x.go', null)");
  assert.match(p.st.toasts[2][0], /Focus a terminal first/);
});

test("the phone's tree inserts at the focused terminal as before: its screens are the user's to move between", () => {
  const p = callersPage();
  p.width(390);
  p.run("insertPathAtPrompt('/w/x.go', focusedTerminal())");
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
  assert.deepStrictEqual(p.st.toasts, []);
  p.sessions(p.sess("a", { name: "alpha" }), p.sess("b", { name: "beta", activity: "waiting", need: "ask" }));
  p.run("insertPathAtPrompt('/w/y.go', focusedTerminal())");
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] }, "the gate still applies on the phone");
  assert.deepStrictEqual(p.st.toasts, [["beta is waiting on you; answer it first", true]]);
});

// --- the tree and the Files dock (#337, step 4) ------------------------------------------------------------------------------
// The tree belongs to the dock's session while the dock holds it against the focused pane (a pin on a live session, or a
// buffer with unsaved edits). Its insert then targets that session's terminal if it is on screen, and otherwise refuses:
// it never falls back to the focused terminal. The palette keeps the focused terminal whatever the dock shows.

// The row menu's handler: pick the target, insert. (A source pin below says filetree.js's menu does exactly this.)
const treeInsert = (p, file) => p.run("{ const to = treeInsertTarget(); insertPathAtPrompt(" + JSON.stringify(file || "/w/x.go") + ", to.term, to.absent); }");
// The palette's: the focused terminal.
const paletteInsert = (p, file) => p.run("insertPathAtPrompt(" + JSON.stringify(file || "/w/x.go") + ", focusedTerminal())");
// The dock open on session A and pinned to it, while pane B (the one with the keyboard) shows another session.
function pinnedToA() {
  const p = callersPage();
  p.run("drawerState = { open: 'files', sessionId: 'a', pinned: true };");
  return p;
}

test("a tree pinned to A while B has the keyboard inserts at A's terminal, which is on screen", () => {
  const p = pinnedToA();
  assert.strictEqual(p.ctx.focusedTerminal(), p.tb, "B has the keyboard");
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { a: ["/w/x.go "] }, "A got it, B did not");
  assert.deepStrictEqual(p.st.focused, ["a"], "and the keyboard goes where the text went");
  assert.deepStrictEqual(p.st.toasts, []);
});

test("the palette keeps the focused terminal while the dock is pinned to A: B gets the path", () => {
  const p = pinnedToA();
  paletteInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
  assert.deepStrictEqual(p.st.focused, ["b"]);
});

// Every way A's terminal can be off screen or unfit while the tree is pinned to it: the tree refuses, and B, which has the
// keyboard and is on screen, is NOT typed into instead.
const PINNED_REFUSALS = [
  ["Full mode", (p) => { p.full(true); }, /Full mode/],
  ["A's pane showing Activity", (p) => { p.ca.setView("activity"); }, /Activity/],
  ["A's pane hidden by B being maximized", (p) => { p.maximize(p.cb); }, /maximized/],
  ["A an inactive stacked tab", (p) => { p.names({ a: "alpha", b: "beta", c: "gamma" }); p.ca.addTab(p.term("c", "gamma")); }, /background tab/],
  ["A behind a file viewer tab", (p) => { p.ca.addTab(p.viewer("/w/f.go")); }, /behind a file tab/],
  ["a page view open", (p) => { p.openPage("audit-page"); }, /Audit is open/],
  ["A waiting on a permission prompt", (p) => { p.ctx.lastSessions[0].activity = "waiting"; p.ctx.lastSessions[0].need = "ask"; }, /alpha is waiting on you/],
];
for (const [what, make, why] of PINNED_REFUSALS) {
  test("a tree pinned to A refuses when " + what + ", and does not fall back to the focused terminal", () => {
    const p = pinnedToA();
    make(p);
    treeInsert(p);
    assert.strictEqual(p.st.toasts.length, 1);
    assert.match(p.st.toasts[0][0], why);
    assert.strictEqual(p.st.toasts[0][1], true);
    assert.deepStrictEqual(p.st.sent, {}, "nothing typed in A, and nothing in B");
    assert.deepStrictEqual(p.st.focused, [], "no focus moved");
  });
}

test("a shell docked in A's pane and focused still gets the path while the tree is pinned to A", () => {
  const p = pinnedToA();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  const sh = p.ca.dockShell("sh", "shell 1"); // focuses the shell in A's pane
  p.focus(p.ca);
  assert.strictEqual(p.ctx.focusedTerminal(), sh, "the keyboard is in A's dock");
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { sh: ["/w/x.go "] }, "the shell, not A's agent");
  assert.deepStrictEqual(p.st.toasts, []);
  // The dock hidden: the keyboard is back on A's agent, which is what the tree is pinned to.
  p.st.sent = {};
  p.ca.hideDock();
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { a: ["/w/x.go "] });
});

test("a docked shell of A's pane is judged like any terminal: hidden by Full mode, the pinned tree refuses it too", () => {
  const p = pinnedToA();
  p.names({ a: "alpha", b: "beta", sh: "shell 1" });
  p.ca.dockShell("sh", "shell 1");
  p.focus(p.ca);
  p.full(true);
  treeInsert(p);
  assert.match(p.st.toasts[0][0], /Full mode/);
  assert.deepStrictEqual(p.st.sent, {});
});

test("a tree that is not pinned follows focus: it inserts at the focused terminal", () => {
  const p = callersPage();
  p.run("drawerState = { open: 'files', sessionId: 'a', pinned: false };"); // the dock names A for a moment, unpinned
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
  p.st.sent = {};
  p.run("drawerState = { open: 'files', sessionId: 'b', pinned: true };"); // pinned to the session that has the keyboard
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
});

test("a tree whose dock holds A for unsaved edits (not a pin) inserts at A too: header, viewer and tree name one session", () => {
  const p = callersPage();
  p.run("drawerState = { open: 'files', sessionId: 'a', pinned: false }; heldEdits = true;");
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { a: ["/w/x.go "] });
});

test("no dock open, or a dock for a pane of files with no session: the focused terminal, as before", () => {
  const p = callersPage();
  p.run("drawerState = { open: null, sessionId: null, pinned: false };");
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
  p.st.sent = {};
  p.run("drawerState = { open: 'files', sessionId: null, pinned: false }; heldEdits = true;");
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
});

test("a tree pinned to a session with no terminal on this page refuses by name instead of asking for a focus", () => {
  const p = callersPage();
  p.names({ a: "alpha", b: "beta", z: "zulu" });
  p.run("drawerState = { open: 'files', sessionId: 'z', pinned: true };");
  treeInsert(p);
  assert.deepStrictEqual(p.st.toasts, [["zulu is not open in a pane; open it first", true]]);
  assert.deepStrictEqual(p.st.sent, {});
});

test("a tree pinned to a session that has ended no longer holds the dock: it inserts at the focused terminal", () => {
  const p = pinnedToA();
  p.names({ b: "beta" }); // A is gone from the live list: dockFollow drops the pin
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
});

test("the phone's tree has no dock: it inserts at the focused terminal", () => {
  const p = pinnedToA();
  p.width(390);
  treeInsert(p);
  assert.deepStrictEqual(p.st.sent, { b: ["/w/x.go "] });
});

// --- source pins: no insert can skip the decision ------------------------------------------------------------------------

// The function or class method that contains `idx` of `text`: from the nearest line that opens one at column 0 or two
// spaces, to its closing brace.
function enclosing(text, idx) {
  const head = /^(?:async )?function \w+\(.*|^  (?:async |static )?\w+\(.*\) \{$/gm;
  let start = -1;
  for (const m of text.matchAll(head)) { if (m.index <= idx) start = m.index; else break; }
  assert.ok(start >= 0, "a function encloses the call");
  const open = text.indexOf("{", start + (text.slice(start).search(/\)\s*\{/)));
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    if (text[i] === "{") depth++;
    else if (text[i] === "}" && --depth === 0) return text.slice(start, i + 1);
  }
  throw new Error("unbalanced braces");
}
const code = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "").replace(/\s\/\/ .*$/gm, "");

test("every call of insertTextAtPrompt sits in a function that asks insertDecision first", () => {
  const seen = [];
  for (const f of fs.readdirSync(STATIC).filter((n) => /\.js$/.test(n) && !/^monaco|vendor/.test(n))) {
    const text = src(f);
    for (const m of text.matchAll(/insertTextAtPrompt\(/g)) {
      const line = text.slice(text.lastIndexOf("\n", m.index) + 1, text.indexOf("\n", m.index));
      if (/^\s*(\/\/|\*)/.test(line) || /^function insertTextAtPrompt\(/.test(line)) continue; // a comment, or the definition itself
      const body = code(enclosing(text, m.index));
      seen.push(f + ": " + (/^\s*(?:async )?function (\w+)|^\s*(?:async |static )?(\w+)\(/.exec(body) || []).slice(1).find(Boolean));
      assert.match(body, /insertDecision\(/, f + " calls insertTextAtPrompt without insertDecision in the same function");
      assert.ok(body.indexOf("insertDecision(") < body.indexOf("insertTextAtPrompt("), f + " asks after typing");
    }
  }
  assert.deepStrictEqual(seen.sort(), ["filetree.js: insertPathAtPrompt", "upload.js: insertPathsIntoTerminal", "viewer-tab.js: insertPath"]);
});

test("no PTY write path was added: sendText is called only by insertTextAtPrompt and the phone's snippet and key rows", () => {
  const callers = [];
  for (const f of fs.readdirSync(STATIC).filter((n) => /\.js$/.test(n) && !/^monaco/.test(n))) {
    const n = (code(src(f)).match(/\.sendText\(/g) || []).length;
    if (n) callers.push(f + ":" + n);
  }
  // app.js: the phone's snippet row (continue, yes, no, /) and its key bar's symbols, typed on the Terminal screen at the
  // terminal in front, and meant to answer a session that is asking; upload.js: insertTextAtPrompt's one call.
  assert.deepStrictEqual(callers.sort(), ["app.js:2", "upload.js:1"]);
});

test("the row menu, the palette, the Path menu and the line-number menu all reach the decision through these callers", () => {
  const menu = code(src("filetree.js"));
  assert.match(menu, /insert: \(\) => \{ const to = treeInsertTarget\(\); insertPathAtPrompt\(path, to\.term, to\.absent\); \},/, "the tree's row menu inserts at the target the tree picks");
  assert.match(code(src("palette.js")), /insertPathAtPrompt\(p, typeof focusedTerminal === "function" \? focusedTerminal\(\) : null\)/, "the palette passes the focused terminal");
  assert.match(code(src("viewertoolbar.js")), /act\.pathInsert = \(\) => viewer\.insertPath\(\);/, "the Path menu inserts through the viewer");
  const lineMenu = code(methodText("viewer-tab.js", "openLineMenu"));
  assert.match(lineMenu, /ins\.onclick = \(\) => \{[^}]*this\.insertPath\(line\);/, "the line-number menu inserts through the viewer");
});
