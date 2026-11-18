"use strict";
// The editor's selection bar and the one insert of a file reference (editorselbar.js): the real EditorTab, editorref.js (rangeRef,
// selectionLines), viewerpath.js (the box rule) and upload.js (shellQuote, insertTextAtPrompt) in a vm, against the fake page and
// the fake Monaco of editorpage.js. What the fake is exact about: the editor's events (selection, focus, blur, a key) fire as
// Monaco's do, a content widget is something added and removed, and a click on a line number leaves that line selected with its
// line break (Monaco's selectOnLineNumbers, which the browser check in the task report proves on the real editor). The insert
// decision (is the session waiting, is the terminal on screen) is insert-decision.test.js's, and insert-callers.test.js fails any
// insert that does not ask it first; here it is a stub that refuses or not, so what is proved is what the bar does with the answer:
// types the reference and nothing else, or toasts and types nothing and moves no focus.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page, El } = require("./editorpage.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const ESC = 9; // the fake Monaco's KeyCode.Escape

// A page with a session s1 (the dock's) and s2, each with a terminal that records what reaches its prompt and who takes the keyboard.
async function setup(over) {
  const typed = [], grabbed = [], asked = [], copied = [];
  const gate = { refuse: "" };
  const terms = {};
  for (const id of ["s1", "s2"]) terms[id] = { id, kind: "terminal", sendText: (t) => { typed.push([id, t]); return true; }, grabFocus: () => grabbed.push(id) };
  const p = page(Object.assign({
    terminalTabFor: (sid) => terms[sid] || null,
    // insertdecision.js insertRefused's contract: toast `lead + why` and answer true on a refusal, answer false when it may type.
    insertRefused: (term, lead) => {
      asked.push(term.id);
      if (!gate.refuse) return false;
      p.st.toasts.push([(lead || "") + gate.refuse, { error: true }]);
      return true;
    },
    copyPath: (text, refocus) => { copied.push(text); refocus(); },
  }, over));
  p.load("upload.js");
  p.ctx.menuIconSvg = (kind) => { const n = new El("svg", p.doc); n.className = "icon-" + kind; return n; };
  const tab = await p.open("/p/a.go");
  const ed = p.st.editors[0];
  return { p, tab, ed, typed, grabbed, asked, copied, gate, terms };
}

const widgetEl = (ed) => ed.widgets[0].getDomNode();
const buttons = (ed) => widgetEl(ed).children;
const labels = (ed) => buttons(ed).map((b) => b.textContent);

test("the bar shows only for a non-empty selection, and only while the editor has the keyboard", async () => {
  const { ed } = await setup();
  ed.focus();
  assert.equal(ed.widgets.length, 0, "a caret is not a selection");
  ed.select(3, 1, 5, 12);
  assert.equal(ed.widgets.length, 1);
  assert.equal(ed.widgets[0].getId(), "dk.selbar");
  assert.deepEqual(ed.widgets[0].getPosition().position, { lineNumber: 3, column: 1 }, "over the start of the selection");
  assert.deepEqual(ed.widgets[0].getPosition().preference, [1, 2], "above it, below when there is no room above");
  ed.select(5, 4, 5, 4);
  assert.equal(ed.widgets.length, 0, "the selection collapsed: the bar goes");
  ed.select(3, 1, 5, 12);
  assert.equal(ed.widgets.length, 1);
  ed.blur();
  assert.equal(ed.widgets.length, 0, "the keyboard went elsewhere (the terminal): the bar goes");
  ed.focus();
  assert.equal(ed.widgets.length, 1, "and comes back with it, for the selection that is still there");
});

test("the bar is for the tab on screen in Code: a hidden tab and the Changes view have none, and a read-only file has it", async () => {
  const { p, tab, ed } = await setup();
  ed.focus();
  ed.select(2, 1, 4, 3);
  assert.equal(ed.widgets.length, 1);
  tab.onHide();
  assert.equal(ed.widgets.length, 0, "a tab that is hidden has no bar");
  tab.onAttach();
  ed.focus();
  assert.equal(ed.widgets.length, 1, "shown again, with its selection");
  tab.diff = { diff: { getModifiedEditor: () => ed } }; // the Changes view is up: the bar is for Code
  tab.paint();
  assert.equal(ed.widgets.length, 0, "Changes is a review view with no bar in this phase");
  tab.diff = null;
  tab.paint();
  assert.equal(ed.widgets.length, 1);
  // A file the server marks read-only can still be pointed at.
  const ro = page();
  ro.serve("x\ny\nz\n");
  const t2 = await ro.open("/p/ro.txt", { box: "s1" });
  assert.equal(t2.mode, "read");
  const e2 = ro.st.editors[0];
  e2.focus();
  e2.select(1, 1, 3, 2);
  assert.equal(e2.widgets.length, 1, "a read-only file shows the bar too");
  void p;
});

test("the buttons are Insert at prompt and Copy <name>:a-b, as the mockup has them; the title says what is copied", async () => {
  const { ed } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:3-5"]);
  assert.equal(buttons(ed)[0].className, "btn btn-sm dk-selbar-b btn-primary", "Insert is the one primary");
  assert.match(buttons(ed)[1].className, /btn-ghost/);
  assert.equal(buttons(ed)[1].title, "Copy /p/a.go:3-5");
  assert.ok(buttons(ed).every((b) => b.tag === "button" && b.type === "button"), "real buttons");
  assert.equal(widgetEl(ed).getAttribute("role"), "toolbar");
  ed.select(7, 2, 7, 9);
  assert.equal(ed.widgets.length, 1, "one widget, moved");
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:7"], "one line is a:7, not a:7-7");
  assert.ok(ed.layouts >= 1, "the widget is laid out again for the new place");
});

test("Insert at prompt types rangeRef and one space at the dock's session's prompt: no Enter, no control byte, then the terminal takes the keyboard", async () => {
  const { ed, typed, grabbed, asked, gate } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  buttons(ed)[0].click();
  assert.deepEqual(typed, [["s1", "/p/a.go:3-5 "]], "path:a-b and a space");
  assert.deepEqual(asked, ["s1"], "the decision was asked, for the dock's session's terminal");
  assert.deepEqual(grabbed, ["s1"]);
  assert.ok(!/[\x00-\x1f\x7f]/.test(typed[0][1]), "nothing but printable text");
  ed.select(8, 1, 9, 1); // a click on line 8's number: the line and its break
  buttons(ed)[0].click();
  assert.deepEqual(typed[1], ["s1", "/p/a.go:8 "], "one line");
  assert.equal(gate.refuse, "");
});

test("the text is rangeRef of the selection as Monaco reports it, whatever way it was made", async () => {
  const { ed, typed } = await setup();
  ed.focus();
  const cases = [
    [[3, 1, 5, 12], "/p/a.go:3-5 "], // a drag
    [[3, 6, 6, 1], "/p/a.go:3-5 "], // down to the start of line 6: line 6 is not in it
    [[12, 1, 13, 1], "/p/a.go:12 "], // a line-number click
    [[40, 1, 40, 18], "/p/a.go:40 "], // the last line of a file with no final newline
    [[9, 3, 9, 4], "/p/a.go:9 "], // a few characters
  ];
  for (const [sel, want] of cases) {
    ed.select(...sel);
    buttons(ed)[0].click();
    assert.equal(typed.at(-1)[1], want, JSON.stringify(sel));
  }
});

test("a path that needs quoting is quoted whole for the shell, and one with a control character is refused with nothing typed", async () => {
  const { p, typed, grabbed } = await setup();
  const spaced = await p.open("/p/my file.go");
  const ed = p.st.editors[0];
  assert.equal(p.get("dockEditorOwner"), spaced);
  ed.focus();
  ed.select(3, 1, 5, 12);
  buttons(ed)[0].click();
  assert.deepEqual(typed, [["s1", "'/p/my file.go:3-5' "]], "one argument for the shell, as the tree's insert quotes a path");
  typed.length = 0;
  const nasty = await p.open("/p/a\nb.go");
  assert.equal(p.get("dockEditorOwner"), nasty);
  ed.focus();
  ed.select(1, 1, 2, 4);
  buttons(ed)[0].click();
  assert.deepEqual(typed, [], "a newline in a path would submit the line: never typed");
  assert.equal(p.st.toasts.at(-1)[0], "That path contains control characters, so it was not inserted");
  assert.deepEqual(grabbed.filter((x) => x), ["s1"], "and no keyboard move for it (the one earlier press only)");
});

test("a refusal types nothing, says why in a toast and moves no focus: the bar and the editor are as they were", async () => {
  const { p, ed, typed, grabbed, asked, gate } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  gate.refuse = "alpha is waiting on you; answer it first";
  const focusedBefore = ed.focused;
  buttons(ed)[0].click();
  assert.deepEqual(asked, ["s1"]);
  assert.deepEqual(typed, []);
  assert.deepEqual(grabbed, [], "no terminal took the keyboard");
  assert.equal(ed.focused, focusedBefore, "and the editor did not call focus either");
  assert.deepEqual(p.st.toasts, [["alpha is waiting on you; answer it first", { error: true }]]);
  assert.equal(ed.widgets.length, 1, "the bar stays: the selection is still there to insert once it can be");
  gate.refuse = "";
  buttons(ed)[0].click();
  assert.deepEqual(typed, [["s1", "/p/a.go:3-5 "]], "the same press goes through when the decision says so");
});

test("Insert goes to the dock's session, not to whichever terminal has the keyboard, and the box rule holds", async () => {
  const { p, ed, typed, asked } = await setup();
  p.host.sessionId = () => "s2";
  ed.focus();
  ed.select(3, 1, 5, 12);
  buttons(ed)[0].click();
  assert.deepEqual(typed, [["s2", "/p/a.go:3-5 "]]);
  assert.deepEqual(asked, ["s2"], "the decision is the target's own");
  // A file that exists only in session s2's box is not for s1's prompt.
  p.host.sessionId = () => "s1";
  const boxTab = await p.open("/work/b.go", { box: "s2" });
  assert.equal(p.get("dockEditorOwner"), boxTab);
  ed.focus();
  ed.select(1, 1, 2, 2);
  typed.length = 0;
  buttons(ed)[0].click();
  assert.deepEqual(typed, [], "refused before the decision is even asked");
  assert.equal(p.st.toasts.at(-1)[0], "This file is inside another session's box");
  p.host.sessionId = () => "s2";
  buttons(ed)[0].click();
  assert.deepEqual(typed, [["s2", "/work/b.go:1-2 "]], "its own session's prompt takes it");
});

test("no terminal for the dock's session on this page: a toast, nothing typed", async () => {
  const { p, ed, typed, asked } = await setup({ terminalTabFor: () => null });
  ed.focus();
  ed.select(3, 1, 5, 12);
  buttons(ed)[0].click();
  assert.deepEqual(typed, []);
  assert.deepEqual(asked, [], "there is no terminal to ask about");
  assert.equal(p.st.toasts.at(-1)[0], "Open this file from a session's pane to insert at its prompt");
});

test("a session that is not connected is said so, and nothing else happens", async () => {
  const { p, ed, terms, grabbed } = await setup();
  terms.s1.sendText = () => false;
  ed.focus();
  ed.select(3, 1, 5, 12);
  buttons(ed)[0].click();
  assert.equal(p.st.toasts.at(-1)[0], "That session is not connected, so the path was not inserted");
  assert.deepEqual(grabbed, []);
});

test("Copy copies the same reference, and the keyboard goes back to the editor", async () => {
  const { tab, ed, copied } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  const before = ed.focused;
  buttons(ed)[1].click();
  assert.deepEqual(copied, ["/p/a.go:3-5"]);
  assert.equal(ed.focused, before + 1, "copyPath's refocus is the tab's reader");
  assert.equal(ed.widgets.length, 1, "the selection is still there, so is the bar");
  ed.select(8, 1, 9, 1);
  buttons(ed)[1].click();
  assert.equal(copied.at(-1), "/p/a.go:8");
  void tab;
});

test("the bar of one tab is never drawn for the text of another: the editor holds the model first, the owner is set after", async () => {
  const { p, ed } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  assert.equal(ed.widgets.length, 1);
  const b = new p.EditorTab("/p/b.go", { host: p.host });
  await p.settle();
  ed.setModel(b.model); // editorClaim(b): the editor has b's model, dockEditorOwner is still a (and still attached)
  ed.select(3, 1, 5, 12); // a selection in b's text
  assert.equal(ed.widgets.length, 0, "not a bar for a.go over b.go's lines");
});

test("a press on the bar does not take the keyboard from the editor (it would hide the bar and drop the selection)", async () => {
  const { ed } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  let prevented = 0;
  widgetEl(ed).listeners.mousedown({ preventDefault: () => { prevented++; } });
  assert.equal(prevented, 1);
});

test("Esc hides the bar until the selection changes; a new selection, or the same one made again after it was dropped, shows it", async () => {
  const { ed } = await setup();
  ed.focus();
  ed.select(3, 1, 5, 12);
  ed.key(ESC);
  assert.equal(ed.widgets.length, 0, "hidden on Esc");
  ed.select(3, 1, 5, 12);
  assert.equal(ed.widgets.length, 0, "the same selection (an event with nothing changed) stays hidden");
  ed.blur();
  ed.focus();
  assert.equal(ed.widgets.length, 0, "and so does coming back to the editor with it");
  ed.select(3, 1, 6, 4);
  assert.equal(ed.widgets.length, 1, "another selection is another bar");
  ed.key(ESC);
  ed.select(6, 2, 6, 2); // dropped
  ed.select(3, 1, 6, 4); // made again
  assert.equal(ed.widgets.length, 1, "after the selection was gone, the same range shows it again");
  ed.key(65); // any other key leaves it
  assert.equal(ed.widgets.length, 1);
  const idle = await setup();
  idle.ed.focus();
  idle.ed.key(ESC);
  assert.equal(idle.ed.widgets.length, 0, "Esc with no bar is nothing");
});

test("a line-number click selects the line: the editor is made with selectOnLineNumbers, and the selection it leaves is a bar for that line", async () => {
  const { ed, typed, copied } = await setup();
  assert.equal(ed.createOpts.selectOnLineNumbers, true, "Monaco's own click-the-number-to-select, stated");
  ed.focus();
  ed.select(21, 1, 22, 1); // what Monaco leaves after a click on 21's number
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:21"]);
  buttons(ed)[0].click();
  buttons(ed)[1].click();
  assert.deepEqual(typed, [["s1", "/p/a.go:21 "]]);
  assert.deepEqual(copied, ["/p/a.go:21"]);
  ed.select(21, 1, 24, 1); // shift-click down to 23's number
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:21-23"]);
});

test("selBarAddAction (P3's seam) appends a button that runs with the tab and the lines; the same key replaces it; bad input is refused", async () => {
  const { p, tab, ed } = await setup();
  const get = (n) => p.get(n);
  const ran = [];
  ed.focus();
  ed.select(3, 1, 5, 12);
  assert.equal(get("selBarAddAction")({ key: "note", label: "Add note", icon: "pencil", run: (t, r) => ran.push([t, r]) }), true);
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:3-5", "Add note"], "drawn at once while the bar is up, after the built-in two");
  buttons(ed)[2].click();
  assert.equal(ran.length, 1);
  assert.equal(ran[0][0], tab);
  assert.deepEqual(JSON.parse(JSON.stringify(ran[0][1])), { a: 3, b: 5 });
  assert.equal(get("selBarAddAction")({ key: "note", label: "Note", run: () => ran.push("second") }), true);
  assert.deepEqual(labels(ed), ["Insert at prompt", "Copy a.go:3-5", "Note"], "the same key is one button");
  for (const bad of [null, {}, { key: "", run() {} }, { key: "x" }, { key: "x", run: 3 }]) assert.equal(get("selBarAddAction")(bad), false, JSON.stringify(bad));
  assert.equal(buttons(ed).length, 3);
});

test("the dock's other callers get the same data: editorRefFor is the selection's reference, or the path when nothing is selected, and it is plain data", async () => {
  const { p, tab, ed } = await setup();
  const refFor = p.get("editorRefFor");
  assert.deepEqual(JSON.parse(JSON.stringify(refFor(tab))), { ref: "/p/a.go", box: "", sid: "s1", ranged: false }, "nothing selected: the path");
  ed.focus();
  ed.select(3, 1, 5, 12);
  assert.deepEqual(JSON.parse(JSON.stringify(refFor(tab))), { ref: "/p/a.go:3-5", box: "", sid: "s1", ranged: true });
  ed.blur(); // the strip is pressed: the editor loses the keyboard, the selection stays
  assert.equal(refFor(tab).ref, "/p/a.go:3-5");
  assert.equal(refFor(null), null);
  assert.equal(refFor({ path: "" }), null);
  const other = await p.open("/p/b.go");
  assert.equal(refFor(tab).ref, "/p/a.go", "a tab that has not the editor has no live selection");
  assert.equal(refFor(other).ref, "/p/b.go");
  const boxed = await p.open("/work/c.go", { box: "s2" });
  assert.deepEqual(JSON.parse(JSON.stringify(refFor(boxed))), { ref: "/work/c.go", box: "s2", sid: "s1", ranged: false });
});

test("the module types only through insertTextAtPrompt and asks insertRefused first (the repo-wide pin lists it)", () => {
  const code = src("editorselbar.js").replace(/^\s*\/\/.*$/gm, "");
  assert.match(code, /if \(insertRefused\(target\.term\)\) return false;\n\s*const res = insertTextAtPrompt\(target\.term, shellQuote\(want\.ref\) \+ " "\);/);
  assert.doesNotMatch(code, /sendText|sendKey|\\r|\\n|\\x/, "no other way to the PTY, no Enter, no control byte");
});
