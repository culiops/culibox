// P3 Task 5, what the review bar DOES: the real reviewbar.js, reviewmodel.js (pasteBarState, pasteGate), notesmodel.js (composeMessage,
// markSent), editorselbar.js (the shared leave-Full-mode flow), insertdecision.js and the real tabs.js layout (insertpage.js), and
// app.js's real dockShowTerminal. Stood in: the notes store (editornotes.js's notesFor / notesUpdate / notesShowSent, which have their own
// tests), askDialog and copyText, drawerHasUnsaved, applyDrawer, and the xterm instance (focus, paste, modes) with its socket.
// The property under test is the one the product stands on: the block is typed at the agent's prompt as ONE xterm paste, on a click only,
// after the gate, the visibility decision and the composed (control-stripped) message, and never with Enter.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { page } = require("./insertpage.js");
const N = require("../static/notesmodel.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
function fnSource(file, name) {
  const text = src(file);
  const m = new RegExp("^(?:async )?function " + name + "\\(", "m").exec(text);
  assert.ok(m, name + " is a top-level function of " + file);
  return text.slice(m.index, text.indexOf("\n}\n", m.index) + 2);
}

const NOW = 1000;
function mkNotes(items) {
  let list = [];
  items.forEach((o, i) => { list = N.noteAdd(list, Object.assign({ path: "/work/a.go", rel: "a.go", side: "new", line: i + 1, quote: "q" + i, text: "t" + i }, o), NOW + i); });
  return list;
}

// Session a (alpha) in one pane with the dock open on it, as the real page has it; the real bar mounted on a stand-in EditorTab.
function rig(opts) {
  const o = Object.assign({ forced: false, full: false, activity: "idle", need: "", bracketed: true, open: true, dirty: false, notes: null, tabs: null, unsaved: false }, opts);
  const p = page();
  p.sessions({ id: "a", name: "alpha", activity: o.activity, need: o.need });
  const cell = p.pane(p.term("a", "alpha"));
  p.focus(cell);
  const term = p.ctx.terminalTabFor("a");
  const pasted = [];
  term.term.paste = (t) => { pasted.push(t); };
  term.term.modes = { bracketedPasteMode: o.bracketed };
  term.ws = { readyState: o.open ? 1 : 3 };
  for (const id of ["drawer-max", "drawer-max-label"]) { const n = new p.DNode("span"); n.id = id; p.els[id] = n; p.doc.body.appendChild(n); }
  p.ctx.$ = (id) => p.els[id] || null;
  p.run('let drawerState = { open: "files", sessionId: "a", pinned: false };');
  p.ctx.positionDrawer = () => {};
  p.ctx.dockRefit = () => {};
  p.run("setFocusedCell = function (c) { setFocusedCellRaw(c); };");
  p.ctx.drawerHasUnsaved = () => o.unsaved;
  const log = { applied: [], copied: [], dialogs: [], shown: [] };
  p.ctx.applyDrawer = (next, op) => {
    log.applied.push(next);
    p.run("drawerState = { open: null, sessionId: " + JSON.stringify(next.sessionId) + ", pinned: false }");
    p.drawer.hidden = true;
    p.run("paintDrawerMax(false, false)");
    if (op && typeof op.after === "function") op.after();
  };
  for (const [f, n] of [["app.js", "paintDrawerMax"], ["app.js", "setDrawerMax"], ["app.js", "dockShowTerminal"]]) p.run(fnSource(f, n));
  p.run(src("notesmodel.js"));
  p.run(src("reviewmodel.js"));
  p.run(src("editorselbar.js"));
  // The notes module's API on a plain store (editor-notes.test.js runs the real one).
  const st = { list: o.notes || mkNotes([{}, { line: 5 }]), showSent: false };
  p.ctx.notesFor = (sid) => (sid === "a" ? st.list : []);
  p.ctx.notesUpdate = (sid, fn) => { const next = fn(st.list); if (next === st.list) return false; st.list = next; p.ctx.reviewBarRender(sid); return true; };
  p.ctx.notesShowSent = (sid, on) => { st.showSent = !!on; p.ctx.reviewBarRender(sid); };
  p.ctx.notesShowSentOf = () => st.showSent;
  p.ctx.askDialog = (a) => { log.dialogs.push(a); return Promise.resolve({ outcome: "confirm", value: "" }); };
  p.ctx.copyText = (t, done) => { log.copied.push(t); if (done) done(true); };
  p.ctx.composeMessage = N.composeMessage;
  p.run(src("reviewbar.js"));
  // A stand-in EditorTab for file /work/a.go: a root element, a name, the host's session, and an unsaved-edit flag.
  const mkTab = (pth, dirty) => {
    const tab = { path: pth, name: pth.split("/").pop(), host: { sessionId: () => "a" }, model: {}, disposed: false, el: new p.DNode("div"), isDirty: !!dirty, dirty() { return this.isDirty; } };
    tab.el.appendChild(new p.DNode("div"));
    p.ctx.__tab = tab;
    p.run("reviewBarMount(__tab)");
    return tab;
  };
  const tabs = (o.tabs || [["/work/a.go", o.dirty]]).map(([pth, d]) => mkTab(pth, d));
  const bar = (tab) => p.run("rbBars").get(tab || tabs[0]);
  p.drawer.hidden = false;
  if (o.full) p.run("paintDrawerMax(true, " + (o.forced ? "true" : "false") + ")");
  const sent = () => p.st.sent.a || [];
  return {
    p, o, st, log, term, pasted, tabs, bar, sent, cell,
    paste: (tab) => bar(tab).paste.click(),
    render: () => p.ctx.reviewBarRender("a"),
    toasts: () => p.st.toasts,
    full: () => p.drawer.dataset.max === "1",
    texts: (tab) => { const b = bar(tab); return { count: b.count.textContent, sub: b.sub.textContent, paste: b.paste.textContent, title: b.paste.title, disabled: b.paste.getAttribute("aria-disabled"), hidden: b.el.hidden }; },
  };
}

test("the bar reads 'N notes · sent as one message' with Preview and 'Paste to <session>', and is first in the tab", () => {
  const r = rig();
  const t = r.texts();
  assert.equal(t.count, "2 notes");
  assert.equal(t.sub, " · sent as one message");
  assert.equal(t.paste, "Paste to alpha");
  assert.equal(t.disabled, "false");
  assert.equal(t.hidden, false);
  assert.equal(r.bar().preview.textContent, "Preview");
  assert.equal(r.tabs[0].el.children[0], r.bar().el, "the bar is the tab's first child");
  assert.equal(r.bar().el.getAttribute("role"), "region");
  assert.equal(rig({ notes: mkNotes([{}]) }).texts().count, "1 note");
});

test("Paste: ONE xterm paste of exactly composeMessage's text, never sendText or sendKey, no Enter; notes marked sent; toast; terminal takes the keyboard", () => {
  const r = rig();
  const want = N.composeMessage(r.st.list).text;
  r.paste();
  assert.deepEqual(r.pasted, [want], "one paste, the composed message");
  assert.deepEqual(r.sent(), [], "nothing went through sendText (the stand-in records it)");
  assert.doesNotMatch(r.pasted[0], /[\x00-\x08\x0b-\x1f\x7f-\x9f]/, "no control character but \\n and \\t");
  assert.doesNotMatch(r.pasted[0], /\r/, "no carriage return: the block is not submitted");
  assert.deepEqual(r.st.list.map((n) => n.status), ["sent", "sent"], "the notes that went are sent");
  assert.deepEqual(r.toasts(), [["Pasted at alpha's prompt — press Enter to send", false]]);
  assert.deepEqual(r.p.st.focused, ["a"], "the session's terminal has the keyboard, so the next key is the user's Enter");
  assert.equal(r.bar().paste.hidden, true, "no pending notes: Paste and Preview are gone (the slim bar keeps the Sent toggle)");
  // The paste cannot happen a second time: nothing is pending, so the message cannot be composed.
  r.p.run("reviewBarPaste('a')");
  assert.equal(r.pasted.length, 1);
  assert.deepEqual(r.toasts()[1], ["Nothing was pasted: no pending notes", true]);
});

test("what is pasted is the composed message: hostile note text, quote and file name arrive stripped (ESC, a fake paste end, C1, CR)", () => {
  const evil = "hi\x1b[201~rm -rf ~\x9b31m\rX\x00\x07end";
  const r = rig({ notes: mkNotes([{ text: evil, quote: "q\x1b[201~", rel: "a\x1b.go" }]) });
  r.paste();
  assert.equal(r.pasted.length, 1);
  const out = r.pasted[0];
  assert.ok(!out.includes("\x1b"), "no ESC: the paste cannot be ended early");
  assert.ok(!out.includes("\x1b[201~"));
  assert.ok(!/[\x80-\x9f]/.test(out), "no C1 byte (\\x9b is a one-byte CSI)");
  assert.ok(!/\r/.test(out));
  assert.ok(!/[\x00-\x08\x0b-\x1f\x7f]/.test(out));
  assert.ok(out.length <= 16000);
});

test("a finished turn ({waiting, need: turn}) is pasted into; working, asking, waiting with no need, exited are refused with the gate's sentence, and a press says it again", () => {
  const ok = rig({ activity: "waiting", need: "turn" });
  assert.equal(ok.texts().disabled, "false");
  ok.paste();
  assert.equal(ok.pasted.length, 1);
  for (const [activity, need, why] of [["working", "", "alpha is working; notes wait here"], ["waiting", "ask", "alpha is waiting on you; answer it first"],
    ["waiting", "sso", "alpha is waiting on you; answer it first"], ["waiting", "", "alpha is waiting on you; answer it first"],
    ["waiting", "store_locked", "alpha is waiting on you; answer it first"], ["done", "", "Not connected"], ["crashed", "", "Not connected"]]) {
    const r = rig({ activity, need });
    const t = r.texts();
    assert.equal(t.disabled, "true", activity + "/" + need);
    assert.equal(t.title, why, "the title says why");
    r.paste();
    assert.deepEqual(r.toasts(), [[why, true]], "and so does the toast");
    assert.deepEqual(r.pasted, []);
    assert.deepEqual(r.st.list.map((n) => n.status), ["pending", "pending"], "the notes wait");
  }
});

test("bracketed paste off, a closed socket and no terminal each refuse; nothing is pasted", () => {
  const off = rig({ bracketed: false });
  assert.equal(off.texts().title, "This program does not accept pasted blocks");
  off.paste();
  assert.deepEqual(off.pasted, []);
  assert.deepEqual(off.toasts(), [["This program does not accept pasted blocks", true]]);
  const closed = rig({ open: false });
  assert.equal(closed.texts().title, "Not connected");
  closed.paste();
  assert.deepEqual(closed.pasted, []);
  assert.deepEqual(closed.st.list.map((n) => n.status), ["pending", "pending"], "a paste that went nowhere does not mark the notes sent");
  // The terminal closed (no pane holds it): the click is refused at the gate.
  const gone = rig();
  gone.p.run("cells.length = 0");
  gone.paste();
  assert.deepEqual(gone.pasted, []);
});

test("R12: a file with pending notes that has unsaved edits blocks Paste ('Save <name> first: the agent reads the file on disk'); a clean one, or one with no notes, does not", () => {
  const dirty = rig({ dirty: true });
  assert.equal(dirty.texts().disabled, "true");
  assert.equal(dirty.texts().title, "Save a.go first: the agent reads the file on disk");
  dirty.paste();
  assert.deepEqual(dirty.toasts(), [["Save a.go first: the agent reads the file on disk", true]]);
  assert.deepEqual(dirty.pasted, []);
  // Another file's unsaved edit, with no notes on it, is not in the way.
  const other = rig({ tabs: [["/work/a.go", false], ["/work/b.go", true]] });
  assert.equal(other.texts().disabled, "false");
  other.paste();
  assert.equal(other.pasted.length, 1);
  // Two unsaved files with notes: both named, once each.
  const two = rig({ notes: mkNotes([{}, { path: "/work/b.go", rel: "b.go" }]), tabs: [["/work/a.go", true], ["/work/b.go", true]] });
  assert.equal(two.texts().title, "Save a.go, b.go first: the agent reads the file on disk");
  // The bar of the CLEAN tab says it too (the notes are the session's, across all its files).
  const mixed = rig({ notes: mkNotes([{ path: "/work/b.go", rel: "b.go" }]), tabs: [["/work/a.go", false], ["/work/b.go", true]] });
  assert.equal(mixed.texts(mixed.tabs[0]).title, "Save b.go first: the agent reads the file on disk");
});

test("the gate is read AT THE CLICK: a state that changed since the last repaint refuses, and a refused click moves nothing", () => {
  const r = rig();
  assert.equal(r.texts().disabled, "false", "the bar says ok");
  r.p.sessions({ id: "a", name: "alpha", activity: "working", need: "" }); // the poll has not repainted the bar yet
  r.paste();
  assert.deepEqual(r.pasted, []);
  assert.deepEqual(r.toasts(), [["alpha is working; notes wait here", true]]);
  assert.deepEqual(r.p.st.focused, [], "no focus moved");
  // A buffer that became dirty since the repaint.
  const d = rig();
  d.tabs[0].isDirty = true;
  d.paste();
  assert.deepEqual(d.pasted, []);
  assert.match(d.toasts()[0][0], /^Save a\.go first/);
  // Bracketed paste switched off by the program since the repaint.
  const b = rig();
  b.term.term.modes.bracketedPasteMode = false;
  b.paste();
  assert.deepEqual(b.pasted, []);
});

test("the terminal must be on screen: a pane showing Activity refuses with the visibility decision's sentence, types nothing, marks nothing", () => {
  const r = rig();
  r.cell.setView("activity");
  r.paste();
  assert.deepEqual(r.pasted, []);
  assert.equal(r.toasts().length, 1);
  assert.equal(r.toasts()[0][1], true);
  assert.match(r.toasts()[0][0], /Activity/);
  assert.deepEqual(r.st.list.map((n) => n.status), ["pending", "pending"]);
});

test("in a chosen Full mode the click LEAVES Full mode first (Show terminal), then asks the decision on the visible prompt, then pastes", () => {
  const r = rig({ full: true });
  assert.equal(r.full(), true);
  const asked = [];
  r.p.run("const __ask = insertRefused; insertRefused = function (t, l) { __log(document.getElementById('drawer').dataset.max === '1'); return __ask(t, l); };");
  r.p.ctx.__log = (full) => asked.push(full);
  r.paste();
  assert.equal(r.full(), false, "Full mode is over");
  assert.deepEqual(asked, [false], "the decision was asked once, with Full mode already off");
  assert.equal(r.pasted.length, 1);
  assert.deepEqual(r.toasts(), [["Pasted at alpha's prompt — press Enter to send", false]]);
  assert.deepEqual(r.sent(), []);
});

test("in a FORCED Full mode with an unsaved edit: 'Save first', the dock is not asked to close, nothing is pasted", () => {
  const r = rig({ full: true, forced: true, unsaved: true });
  r.paste();
  assert.deepEqual(r.toasts(), [["Save first: leaving Full mode would close the dock", true]]);
  assert.deepEqual(r.log.applied, []);
  assert.equal(r.full(), true);
  assert.deepEqual(r.pasted, []);
  assert.deepEqual(r.st.list.map((n) => n.status), ["pending", "pending"]);
});

test("in a FORCED Full mode without an edit the dock closes and the paste is made from applyDrawer's `after`", () => {
  const r = rig({ full: true, forced: true });
  r.paste();
  assert.deepEqual(r.log.applied.map((a) => a.open), [null]);
  assert.equal(r.pasted.length, 1);
  assert.deepEqual(r.st.list.map((n) => n.status), ["sent", "sent"]);
});

test("a message that cannot be composed (too long) pastes nothing and says so; the notes stay pending", () => {
  const big = "x".repeat(6000);
  const r = rig({ notes: mkNotes([{ text: big }, { text: big }, { text: big }]) });
  r.paste();
  assert.deepEqual(r.pasted, []);
  assert.deepEqual(r.toasts(), [["Nothing was pasted: too long; paste fewer notes", true]]);
  assert.deepEqual(r.st.list.map((n) => n.status), ["pending", "pending", "pending"]);
});

test("only the notes that were in the message are marked sent", () => {
  const r = rig();
  const ids = r.st.list.map((n) => n.id);
  // A note added between the compose and the mark cannot happen (one synchronous call), but a sent one stays out of the message.
  r.st.list = N.markSent(r.st.list, [ids[0]]);
  r.render();
  r.paste();
  assert.equal(r.pasted.length, 1);
  assert.ok(!/t0/.test(r.pasted[0]), "the sent note is not repeated");
  assert.ok(/t1/.test(r.pasted[0]));
  assert.deepEqual(r.st.list.map((n) => n.status), ["sent", "sent"]);
});

test("Preview opens the kit dialog with the exact composed text as textContent and a Copy button; it pastes nothing", () => {
  const r = rig({ notes: mkNotes([{ text: "<img src=x onerror=alert(1)> & more" }]) });
  r.bar().preview.click();
  assert.equal(r.log.dialogs.length, 1);
  const d = r.log.dialogs[0];
  const want = N.composeMessage(r.st.list).text;
  const pre = d.extra[0];
  assert.equal(pre.tag || pre.tagName, pre.tag ? "pre" : "PRE");
  assert.equal(pre.textContent, want, "the exact text, set as text");
  assert.equal(pre.children.length, 0, "no markup was made from the note's text");
  const copy = d.extra[1].children[0];
  assert.equal(copy.textContent, "Copy");
  copy.click();
  assert.deepEqual(r.log.copied, [want]);
  assert.deepEqual(r.pasted, []);
  assert.deepEqual(r.st.list.map((n) => n.status), ["pending"], "a preview marks nothing sent");
});

test("repaint: the session poll updates the button (working -> idle) and writes nothing to the DOM when nothing changed", () => {
  const r = rig({ activity: "working" });
  assert.equal(r.texts().disabled, "true");
  r.p.sessions({ id: "a", name: "alpha", activity: "idle", need: "" });
  r.p.run("reviewBarRender()");
  assert.equal(r.texts().disabled, "false");
  // Nothing changed: a marker written into the bar survives the repaint.
  r.bar().count.textContent = "MARK";
  r.p.run("reviewBarRender()");
  r.p.run("reviewBarRender('a')");
  assert.equal(r.bar().count.textContent, "MARK", "no DOM write when the state is the same");
  r.p.sessions({ id: "a", name: "alpha", activity: "working", need: "" });
  r.p.run("reviewBarRender()");
  assert.equal(r.texts().count, "2 notes", "a change repaints");
});

test("no notes: no bar; only sent notes: the slim bar with the Sent toggle (and no Paste); the toggle calls notesShowSent", () => {
  const none = rig({ notes: [] });
  assert.equal(none.texts().hidden, true);
  const all = mkNotes([{}, { line: 3 }]);
  const r = rig({ notes: N.markSent(all, all.map((n) => n.id)) });
  assert.equal(r.bar().el.hidden, false, "the slim bar stays so the toggle is reachable");
  assert.equal(r.texts().count, "2 notes sent");
  assert.equal(r.bar().paste.hidden, true);
  assert.equal(r.bar().preview.hidden, true);
  assert.equal(r.bar().sent.hidden, false);
  assert.equal(r.bar().sent.firstChild.textContent, "Sent (2)");
  assert.equal(r.bar().sent.getAttribute("aria-pressed"), "false");
  r.bar().sent.click();
  assert.equal(r.st.showSent, true);
  assert.equal(r.bar().sent.getAttribute("aria-pressed"), "true");
  r.bar().sent.click();
  assert.equal(r.st.showSent, false);
});

test("a tab's bar goes with the tab, and a tab of another session shows none of this session's notes", () => {
  const r = rig();
  const bar = r.bar();
  r.p.ctx.__tab = r.tabs[0];
  r.p.run("reviewBarUnmount(__tab)");
  assert.equal(bar.el.parent, null);
  assert.equal(r.p.run("rbBars").size, 0);
  const other = rig();
  other.tabs[0].host = { sessionId: () => "zzz" };
  other.render();
  other.p.run("reviewBarRender()");
  assert.equal(other.texts().hidden, true, "no notes for session zzz");
});
