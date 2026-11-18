"use strict";
// "Insert path at prompt" (filetree.js insertPathAtPrompt), run for real in a vm, from its two callers.
//
// The TREE's row menu asks for the dock's target ({dock: true}): on the desktop the tree is the dock's, and the dock can be pinned
// to session A while B has the keyboard, so the path goes to the dock session's terminal (or, when the focused terminal is in that
// session's pane, to that one). The PALETTE (Ctrl/Cmd+Enter on a file row) is "at the focused prompt" and keeps focusedTerminal()
// whatever the dock shows: it is run here through the real palette key handler. The phone's tree is unchanged.
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

// A fake terminal tab: what the insert reads and writes. `log` is the order things happened in across the whole page.
function term(id, log, over) {
  const t = {
    id, kind: "terminal", sent: [], grabbed: 0,
    sendText(s) { this.sent.push(s); log.push("send:" + id); return true; },
    grabFocus() { this.grabbed++; log.push("grab:" + id); },
  };
  return Object.assign(t, over);
}

// A fake pane: its session tabs (cell.tabs: more than one once the pane cap is reached, they stack in one pane, one shown) and
// its docked shells (Cell.dockTabs / dockTabFor: companion shells, a different thing from a stacked session tab).
function cell(name, tabs, shells) {
  const docked = new Map((shells || []).map((s) => [s.id, s]));
  return { name, tabs, dockTabs: docked, dockTabFor: (id) => docked.get(id) || null };
}

// page: two panes, A (agent "a", with a shell "sh-a" docked in it) and B (agent "b"). B has the keyboard; the dock is pinned to A.
function page(o) {
  const log = [];
  const st = { toasts: [], refocus: 0, focusedCells: [] };
  const a = term("a", log);
  const b = term("b", log);
  const shA = term("sh-a", log);
  const cellA = cell("A", [a], [shA]);
  const cellB = cell("B", [b]);
  const c = term("c", log); // another session; not in any pane until a test stacks it (stack)
  const tabs = { a, b, "sh-a": shA };
  const cellOf = { a: cellA, b: cellB };
  st.focus = { term: b }; // what focusedTerminal() returns
  st.shown = { A: a, B: b }; // the tab each pane shows (cellTerminal)
  const doc = { handlers: {}, el: {} };
  const fakeEl = (id) => (doc.el[id] = { id, open: true, value: "", close() { this.open = false; }, showModal() { this.open = true; }, focus() {}, addEventListener(ev, fn) { (doc.handlers[id + ":" + ev] = doc.handlers[id + ":" + ev] || []).push(fn); }, querySelector: () => null });
  fakeEl("palette"); fakeEl("pal-input");
  const ctx = vm.createContext(Object.assign({
    console, RegExp, Object, Array, Map, Math, Number, JSON, String, Error, Set,
    navigator: { platform: "Linux x86_64", userAgent: "" },
    document: { getElementById: (id) => doc.el[id] || null },
    UNSAFE_TEXT: /[\u0000-\u001f\u007f]/,
    showToast: (m, opt) => st.toasts.push([m, opt && opt.error === true]),
    focusTerminal: () => { st.refocus++; log.push("focusTerminal"); },
    focusedTerminal: () => st.focus.term,
    terminalTabFor: (id) => tabs[id] || null,
    cells: [cellA, cellB],
    cellTerminal: (cl) => st.shown[cl.name] || null,
    setFocusedCell: (cl) => { st.focusedCells.push(cl.name); log.push("setFocusedCell:" + cl.name); st.focus.term = st.shown[cl.name]; },
    drawerOwnsFileClicks: () => true,
    drawerState: { open: "files", sessionId: "a", pinned: true }, // the dock is pinned to A
    palRefresh: () => {}, palSelect: () => {}, runItem: () => {},
  }, o));
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(src("shortcuts.js"), "shortcuts.js");
  load(fnSource("upload.js", "shellQuote"), "shellQuote");
  load(fnSource("upload.js", "insertTextAtPrompt"), "insertTextAtPrompt");
  for (const name of ["ftPaneOf", "dockInsertTarget", "dockInsertFocus", "insertPathAtPrompt"]) load(fnSource("filetree.js", name), name);
  load(src("palette.js"), "palette.js");
  load("initPalette()", "init");
  const keydown = doc.handlers["pal-input:keydown"][0];
  return {
    ctx, st, log, tabs, a, b, c, shA, cellA, cellB,
    // Session C stacked in A's pane after A (a full pane cap puts a second session in the same pane as a tab), C shown.
    stack: () => { cellA.tabs.push(c); tabs.c = c; st.shown.A = c; },
    run: (code) => load(code, "test"),
    // Ctrl+Enter in the palette on a file row.
    paletteInsert: (p) => {
      load("palItems = [{ kind: 'file', path: " + JSON.stringify(p) + " }]; palSel = 0;", "palItems");
      let prevented = false;
      keydown({ key: "Enter", ctrlKey: true, preventDefault() { prevented = true; } });
      return prevented;
    },
    treeInsert: (p) => load("insertPathAtPrompt(" + JSON.stringify(p) + ", { dock: true })", "tree"),
  };
}

// --- the tree's row menu -------------------------------------------------------------------------------------------

test("the dock's tree types the path at the DOCK session's terminal, not the focused one", () => {
  const p = page();
  p.treeInsert("/w/a b.go");
  assert.deepEqual(p.a.sent, ["'/w/a b.go' "], "A is the session whose file it is");
  assert.deepEqual(p.b.sent, [], "B, which has the keyboard, got nothing");
  assert.deepEqual(p.st.toasts, []);
});

test("the dock's session has no terminal: the TREE refuses with a toast and types nothing anywhere", () => {
  const p = page({ drawerState: { open: "files", sessionId: "gone", pinned: true } });
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.a.sent, []);
  assert.deepEqual(p.b.sent, [], "never falls back to the focused terminal");
  assert.equal(p.st.toasts.length, 1);
  assert.equal(p.st.toasts[0][1], true, "an error toast");
  assert.match(p.st.toasts[0][0], /no open terminal/);
});

test("the focused terminal is in the dock session's pane (the agent, or a shell docked there): the tree types there", () => {
  // A shell docked below A's agent has the keyboard; the dock shows A's files (pinned, or following A's pane).
  const p = page();
  p.st.focus.term = p.shA;
  p.cellA.focused = true;
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.shA.sent, ["/w/x.go "], "the shell the user is typing in, not the pane's agent");
  assert.deepEqual(p.a.sent, []);
  assert.deepEqual(p.b.sent, []);
  assert.deepEqual(p.st.focusedCells, [], "focus was already there: no pane focus change");
  assert.equal(p.st.refocus, 1, "the caret goes back to the focused terminal, as before");
  // The agent itself focused in that pane: also itself.
  const q = page();
  q.st.focus.term = q.a;
  q.treeInsert("/w/y.go");
  assert.deepEqual(q.a.sent, ["/w/y.go "]);
  assert.deepEqual(q.st.focusedCells, []);
  // A shell docked in ANOTHER pane than the dock session's is not that pane's: the dock session's terminal wins.
  const r = page();
  const shB = term("sh-b", r.log);
  r.cellB.dockTabs.set("sh-b", shB);
  r.st.focus.term = shB;
  r.treeInsert("/w/z.go");
  assert.deepEqual(r.a.sent, ["/w/z.go "]);
  assert.deepEqual(shB.sent, []);
});

test("another session stacked in the dock session's pane is NOT the dock session: its prompt never gets A's path", () => {
  // The pane cap stacks sessions as tabs of one pane. Dock pinned to A (the pin survives this), pane 1 holds A and C, C shown and
  // focused. A's file must not be typed at C's prompt: that is the defect B6 closed.
  const p = page();
  p.stack();
  p.st.focus.term = p.c;
  p.treeInsert("/w/a-file.go");
  assert.deepEqual(p.a.sent, ["/w/a-file.go "], "A's terminal, where the file's session is");
  assert.deepEqual(p.c.sent, [], "never C's");
  assert.deepEqual(p.b.sent, []);
  assert.deepEqual(p.st.toasts, []);
  assert.ok(!p.log.includes("send:c") && !p.log.some((l) => l === "grab:a" || l === "grab:c"), "no hidden tab is given the keyboard");
  assert.deepEqual(p.log, ["send:a", "setFocusedCell:A", "focusTerminal"], "A's tab is out of sight: the keyboard stays with the terminal on screen");
  // Unpinned and following: the dock is on C's session then, and C is the focused one: its own terminal.
  const q = page({ drawerState: { open: "files", sessionId: "c", pinned: false } });
  q.stack();
  q.st.focus.term = q.c;
  q.treeInsert("/w/c-file.go");
  assert.deepEqual(q.c.sent, ["/w/c-file.go "]);
  assert.deepEqual(q.a.sent, []);
  // And with A shown and focused in the stacked pane, A is both the focused terminal and the dock's: itself.
  const r = page();
  r.stack();
  r.st.shown.A = r.a;
  r.st.focus.term = r.a;
  r.treeInsert("/w/y.go");
  assert.deepEqual(r.a.sent, ["/w/y.go "]);
  assert.deepEqual(r.c.sent, []);
});

test("a shell docked in the dock session's pane still gets the path while another session is stacked there", () => {
  const p = page();
  p.stack();
  p.st.focus.term = p.shA; // the shell docked below the pane (Cell.dockTabs), pressed into
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.shA.sent, ["/w/x.go "]);
  assert.deepEqual(p.a.sent, []);
  assert.deepEqual(p.c.sent, []);
  assert.ok(!p.log.includes("setFocusedCell:A"), "it was already the focused terminal");
});

test("typing at another terminal than the focused one moves pane focus there first, as Show terminal does, then gives it the keyboard", () => {
  const p = page();
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.st.focusedCells, ["A"]);
  assert.deepEqual(p.log, ["send:a", "setFocusedCell:A", "grab:a"], "inserted, the pane focused, then the caret");
  assert.equal(p.st.focus.term, p.a, "focusedTerminal() and the keys agree now");
  assert.equal(p.st.refocus, 0, "focusTerminal() would have returned the caret to B");
  // A failed insert (not connected) changes no focus.
  const q = page();
  q.a.sendText = () => false;
  q.treeInsert("/w/x.go");
  assert.deepEqual(q.st.focusedCells, []);
  assert.match(q.st.toasts.at(-1)[0], /not connected/);
});

test("the tree's refusals are as before: control characters in the path, a session that is not connected", () => {
  const q = page();
  q.treeInsert("/w/x\ny.go");
  assert.match(q.st.toasts.at(-1)[0], /control characters/);
  assert.deepEqual(q.a.sent, []);
  assert.deepEqual(q.b.sent, []);
});

test("the phone's Files screen is unchanged: the tree asks for the dock but there is none, so the focused terminal", () => {
  const p = page({ drawerOwnsFileClicks: () => false });
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.b.sent, ["/w/x.go "]);
  assert.deepEqual(p.a.sent, []);
  assert.equal(p.st.refocus, 1, "focusTerminal, as before");
  assert.deepEqual(p.st.focusedCells, []);
  const none = page({ drawerOwnsFileClicks: () => false, focusedTerminal: () => null });
  none.treeInsert("/w/x.go");
  assert.match(none.st.toasts[0][0], /Focus a terminal first/);
});

test("the dock is open but unpinned: the tree's target is the focused pane's session", () => {
  const p = page({ drawerState: { open: "files", sessionId: "b", pinned: false } });
  p.treeInsert("/w/x.go");
  assert.deepEqual(p.b.sent, ["/w/x.go "]);
  assert.deepEqual(p.a.sent, []);
});

// --- the palette: "at the focused prompt", whatever the dock shows -----------------------------------------------------

test("the PALETTE types at the focused terminal while the dock is pinned to another session, and the caret stays there", () => {
  const p = page(); // dock pinned to A, focus in B
  assert.equal(p.paletteInsert("/w/a b.go"), true, "the key was taken");
  assert.deepEqual(p.b.sent, ["'/w/a b.go' "], "B has the keyboard: it gets the path");
  assert.deepEqual(p.a.sent, [], "A, the dock's session, gets nothing");
  assert.deepEqual(p.st.focusedCells, [], "no pane focus change");
  assert.equal(p.st.refocus, 1, "focusTerminal(): the caret returns to the focused pane");
  assert.equal(p.a.grabbed + p.b.grabbed, 0);
  assert.deepEqual(p.st.toasts, []);
});

test("the PALETTE works when the dock's session has no terminal at all", () => {
  const p = page({ drawerState: { open: "files", sessionId: "gone", pinned: true } });
  p.paletteInsert("/w/x.go");
  assert.deepEqual(p.b.sent, ["/w/x.go "], "a terminal is focused: that is where it goes");
  assert.deepEqual(p.st.toasts, [], "not refused");
});

test("the PALETTE with no terminal focused still says to focus one, dock or no dock", () => {
  const p = page({ focusedTerminal: () => null });
  p.paletteInsert("/w/x.go");
  assert.match(p.st.toasts[0][0], /Focus a terminal first/);
  assert.deepEqual(p.a.sent, []);
});

test("the PALETTE is not the dock's: only the tree's row menu asks for it", () => {
  // The tree passes {dock: true}; every other caller (the palette is the only one today) passes nothing and so keeps the
  // focused terminal. Pinned here so a new caller has to choose.
  const tree = src("filetree.js").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  assert.match(tree, /insert: \(\) => insertPathAtPrompt\(path, \{ dock: true \}\)/);
  const pal = src("palette.js").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  assert.match(pal, /insertPathAtPrompt\(p\);/);
  const callers = [...src("filetree.js").matchAll(/insertPathAtPrompt\(/g)].length + [...src("palette.js").matchAll(/insertPathAtPrompt\(/g)].length;
  assert.ok(callers >= 3, "the definition, the tree and the palette");
});
