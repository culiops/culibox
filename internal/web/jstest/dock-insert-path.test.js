"use strict";
// "Insert path at prompt" from the tree's row menu (filetree.js insertPathAtPrompt), run for real in a vm. On the desktop the
// tree is the dock's, and the dock can be pinned to session A while session B has the keyboard: the path must go to A's
// terminal (the session whose files the row names), never to whichever terminal is focused. The phone's tree is unchanged.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  const end = text.indexOf("\n}\n", m.index);
  return text.slice(m.index, end + 2);
}

// A fake terminal tab: what the insert reads and writes.
function term(id, over) {
  const t = { id, sent: [], focused: 0, sendText(s) { this.sent.push(s); return true; }, grabFocus() { this.focused++; } };
  return Object.assign(t, over);
}

function page(o) {
  const st = { toasts: [], refocus: 0 };
  const tabs = { a: term("a"), b: term("b") };
  const ctx = vm.createContext(Object.assign({
    console, RegExp, Object, Array,
    UNSAFE_TEXT: /[\u0000-\u001f\u007f]/,
    showToast: (m, opt) => st.toasts.push([m, opt && opt.error === true]),
    focusTerminal: () => { st.refocus++; },
    focusedTerminal: () => tabs.b, // B has the keyboard
    terminalTabFor: (id) => tabs[id] || null,
    drawerOwnsFileClicks: () => true,
    drawerState: { open: "files", sessionId: "a", pinned: true }, // the dock is pinned to A
  }, o));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(fnSource("upload.js", "shellQuote"), "shellQuote");
  load(fnSource("upload.js", "insertTextAtPrompt"), "insertTextAtPrompt");
  load(fnSource("filetree.js", "insertPathAtPrompt"), "insertPathAtPrompt");
  return { ctx, st, tabs, run: (code) => load(code, "test") };
}

test("the dock's tree types the path at the DOCK session's terminal, not the focused one", () => {
  const p = page();
  p.run('insertPathAtPrompt("/w/a b.go")');
  assert.deepEqual(p.tabs.a.sent, ["'/w/a b.go' "], "A is the session whose file it is");
  assert.deepEqual(p.tabs.b.sent, [], "B, which has the keyboard, got nothing");
  assert.deepEqual(p.st.toasts, []);
  assert.equal(p.tabs.a.focused, 1, "the caret goes where the path went");
});

test("the dock's session has no terminal: refused with a toast, and nothing is typed anywhere", () => {
  const p = page({ drawerState: { open: "files", sessionId: "gone", pinned: true } });
  p.run('insertPathAtPrompt("/w/x.go")');
  assert.deepEqual(p.tabs.a.sent, []);
  assert.deepEqual(p.tabs.b.sent, [], "never falls back to the focused terminal");
  assert.equal(p.st.toasts.length, 1);
  assert.equal(p.st.toasts[0][1], true, "an error toast");
  assert.match(p.st.toasts[0][0], /no open terminal|not inserted/);
});

test("the dock's session not connected, or a path with control characters, is the same refusal as before", () => {
  const p = page();
  p.tabs.a.sendText = () => false;
  p.run('insertPathAtPrompt("/w/x.go")');
  assert.match(p.st.toasts.at(-1)[0], /not connected/);
  const q = page();
  q.run('insertPathAtPrompt("/w/x\\ny.go")');
  assert.match(q.st.toasts.at(-1)[0], /control characters/);
  assert.deepEqual(q.tabs.a.sent, []);
});

test("without the dock (the phone's Files screen) the path goes to the focused terminal, as today", () => {
  const p = page({ drawerOwnsFileClicks: () => false });
  p.run('insertPathAtPrompt("/w/x.go")');
  assert.deepEqual(p.tabs.b.sent, ["/w/x.go "]);
  assert.deepEqual(p.tabs.a.sent, []);
  assert.equal(p.st.refocus, 1, "focusTerminal, as before");
  const none = page({ drawerOwnsFileClicks: () => false, focusedTerminal: () => null });
  none.run('insertPathAtPrompt("/w/x.go")');
  assert.match(none.st.toasts[0][0], /Focus a terminal first/);
});

test("the dock open but not pinned follows the focused pane, so its session is the focused one's", () => {
  const p = page({ drawerState: { open: "files", sessionId: "b", pinned: false } });
  p.run('insertPathAtPrompt("/w/x.go")');
  assert.deepEqual(p.tabs.b.sent, ["/w/x.go "]);
  assert.deepEqual(p.tabs.a.sent, []);
});
