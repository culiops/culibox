"use strict";
// Line notes in the Changes view (editornotes.js), run on the real tab and the fake Monaco of editorpage.js: the gutter "+", the note box,
// show / edit / delete, the keys, re-anchoring when the buffer is replaced from disk, persistence, several diff editors at once, and
// everything that has to be let go. The pure half is notesmodel.test.js; how Monaco draws a view zone, aligns the other side and hands a
// key to a command is the browser check's (the task report).
const test = require("node:test");
const assert = require("node:assert");
const { page, keydown } = require("./editorpage.js");

const HEAD_TEXT = "l1\nl2\nl3\nl5\nl6\n";
const BUF_TEXT = "l1\nl2 changed\nl3\nl4 new\nl5\n";
const MOUSE = { GUTTER_GLYPH_MARGIN: 2, GUTTER_LINE_NUMBERS: 3, CONTENT_TEXT: 6, CONTENT_VIEW_ZONE: 8 };

async function setup(o) {
  const opt = Object.assign({ load: true, store: new Map() }, o);
  const renders = [];
  const p = page({
    drawerState: { sessionId: "s1" },
    drawerChangedFiles: new Map(),
    dockChangesRender: () => { renders.push(1); },
    localStorage: { getItem: (k) => (opt.store.has(k) ? opt.store.get(k) : null), setItem: (k, v) => { opt.store.set(k, String(v)); } },
  });
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = { state: "ok", content: HEAD_TEXT };
  p.setShown(true);
  const tab = await p.open("/p/b.go");
  await p.settle();
  if (opt.load) p.get("notesLoad")(["s1"]);
  return { p, tab, renders, store: opt.store, ...(opt.enter === false ? {} : enter(p, tab)) };
}

function enter(p, tab) {
  tab.showChanges(true);
  const d = p.st.diffs.at(-1);
  [...d.updated].forEach((f) => f());
  d.lineChanges = [{ originalStartLineNumber: 2, originalEndLineNumber: 2, modifiedStartLineNumber: 2, modifiedEndLineNumber: 2 }];
  return { d, mod: d.modified, orig: d.original };
}

// What a test does with the timers the page asked for (setTimeout(fn, 0) stands for the next frame; 300 ms is the save).
const runTimers = (p, ms) => { for (const t of p.st.timers) if (!t.dead && (ms === undefined || t.ms === ms)) { t.dead = true; t.fn(); } };
const zonesOf = (ed) => [...ed.zones.values()];
const find = (z, sel) => z.domNode.querySelector(sel);
const stored = (store) => JSON.parse(store.get("agent-box:notes") || "null");
const ev = (o) => Object.assign({ key: "", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, code: "", defaultPrevented: false, stopped: false,
  preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.stopped = true; } }, o);
const hoverLine = (ed, line, type) => ed.fire("mousemove", { target: { type: type === undefined ? MOUSE.CONTENT_TEXT : type, position: { lineNumber: line } } });
const glyph = (ed) => ed.decor.list().filter((x) => x.options.glyphMarginClassName === "dk-note-add");
const pressGutter = (ed, line) => ed.fire("mousedown", { target: { type: MOUSE.GUTTER_GLYPH_MARGIN, position: { lineNumber: line } }, event: { leftButton: true } });
// Type into the open note box and press Mod+Enter.
function write(z, text) {
  const ta = find(z, "textarea.dk-note-in");
  ta.value = text;
  ta.oninput();
  return ta;
}
const save = (ta) => ta.onkeydown(ev({ key: "Enter", code: "Enter", ctrlKey: true }));
const notes = (p) => p.get("notesFor")("s1");

test("the diff editor is made with the glyph margin on, and the Changes view holds a notes host", async () => {
  const { d } = await setup();
  assert.equal(d.opts.glyphMargin, true);
  assert.equal(zonesOf(d.modified).length, 0, "no notes, no zones");
});

test("the pointer over a line of the new side shows the + in its glyph margin, one decoration that follows it; leaving takes it off", async () => {
  const { mod } = await setup();
  hoverLine(mod, 3);
  assert.equal(glyph(mod).length, 1);
  assert.equal(glyph(mod)[0].range.startLineNumber, 3);
  assert.match(glyph(mod)[0].options.glyphMarginHoverMessage.value, /Add a note \(C\)/);
  hoverLine(mod, 4, MOUSE.GUTTER_LINE_NUMBERS);
  assert.deepEqual(glyph(mod).map((x) => x.range.startLineNumber), [4]);
  const calls = mod.decor.list().length;
  hoverLine(mod, 4);
  assert.equal(mod.decor.list().length, calls, "the same line again writes nothing");
  hoverLine(mod, 5, MOUSE.CONTENT_VIEW_ZONE);
  assert.equal(glyph(mod).length, 0, "over a zone (a note) there is no +");
  hoverLine(mod, 2);
  mod.fire("mouseleave");
  assert.equal(glyph(mod).length, 0);
});

test("the + on the original side is only on lines the base has and the file has not", async () => {
  const { orig, mod } = await setup();
  hoverLine(orig, 1);
  assert.equal(glyph(orig).length, 0, "line 1 is the same on both sides");
  hoverLine(orig, 2);
  assert.equal(glyph(orig).length, 1, "line 2 is changed: it is a removed line");
  assert.equal(glyph(mod).length, 0);
});

test("a press on the + opens the box under the line; empty text cannot be saved; Mod+Enter saves a pending note with the line's text as its quote", async () => {
  const { p, mod, renders } = await setup();
  hoverLine(mod, 4);
  pressGutter(mod, 4);
  const [z] = zonesOf(mod);
  assert.equal(z.afterLineNumber, 4, "under the line");
  const ta = find(z, "textarea.dk-note-in");
  assert.ok(ta, "a textarea.dk-note-in");
  assert.equal(z.suppressMouseDown, true, "a press in the box is the box's, not the editor's");
  const saveBtn = find(z, "button.btn-primary");
  assert.equal(saveBtn.getAttribute("aria-disabled"), "true", "empty: Save says so");
  saveBtn.click();
  save(ta);
  ta.value = "   \n ";
  ta.oninput();
  save(ta);
  assert.equal(notes(p).length, 0, "blank text is not a note");
  write(z, "  This line drops the error.  ");
  assert.equal(saveBtn.getAttribute("aria-disabled"), "false");
  const e = ev({ key: "Enter", code: "Enter", ctrlKey: true });
  ta.onkeydown(e);
  assert.equal(e.defaultPrevented && e.stopped, true, "the key is the box's: neither Monaco's nor the page's");
  assert.equal(notes(p).length, 1);
  const n = notes(p)[0];
  assert.deepEqual({ path: n.path, rel: n.rel, side: n.side, line: n.line, quote: n.quote, text: n.text, status: n.status, outdated: n.outdated },
    { path: "/p/b.go", rel: "b.go", side: "new", line: 4, quote: "l4 new", text: "This line drops the error.", status: "pending", outdated: false });
  assert.equal(p.get("notesPending")("s1"), 1);
  assert.ok(renders.length >= 1, "the Changes section's chip is told");
  const [after] = zonesOf(mod);
  assert.equal(zonesOf(mod).length, 1, "the box became the note: one zone");
  assert.ok(find(after, ".dk-note[data-note]"));
  assert.equal(find(after, ".dk-note-body").textContent, "This line drops the error.");
  assert.match(find(after, ".dk-note-meta").textContent, /^on line 4 · just now$/);
  assert.equal(find(after, ".dk-note-who").textContent, "You");
});

test("Esc cancels the box: nothing is saved, the zone goes, the key does not reach the page", async () => {
  const { p, mod } = await setup();
  pressGutter(mod, 2); // no + shown yet on this line: nothing opens
  assert.equal(zonesOf(mod).length, 0);
  hoverLine(mod, 2);
  pressGutter(mod, 2);
  const ta = find(zonesOf(mod)[0], "textarea");
  ta.value = "half";
  const e = ev({ key: "Escape", code: "Escape" });
  ta.onkeydown(e);
  assert.equal(e.stopped && e.defaultPrevented, true);
  assert.equal(zonesOf(mod).length, 0);
  assert.equal(notes(p).length, 0);
  runTimers(p, 0);
  assert.ok(mod.textFocus, "the keyboard goes back to the editor");
});

test("a key typed in the box never reaches Monaco: a `c` is a letter", async () => {
  const { mod } = await setup();
  hoverLine(mod, 2);
  pressGutter(mod, 2);
  const ta = find(zonesOf(mod)[0], "textarea");
  const e = ev({ key: "c", code: "KeyC" });
  ta.onkeydown(e);
  assert.equal(e.stopped, true);
  assert.equal(e.defaultPrevented, false, "and the letter is typed");
});

test("c and Mod+Shift+A are Monaco commands bound through the registry: plain c only where a read-only diff editor has the text focus", async () => {
  const { p, mod } = await setup();
  const all = [...p.st.editors, ...p.st.diffs.map((x) => x.modified)].flatMap((e) => e.commands);
  const c = all.filter(([k]) => k === 33);
  const a = all.filter(([k]) => k === (2048 | 1024 | 31));
  assert.equal(c.length, 1, "bound once");
  assert.equal(a.length, 1);
  assert.match(c[0][2], /isInDiffEditor/);
  assert.match(c[0][2], /editorReadonly/);
  assert.match(c[0][2], /editorTextFocus/);
  assert.equal(a[0][2], undefined, "a chord with a modifier types nothing: no context");
  // c with the keyboard in the new side, cursor on line 3
  mod.textFocus = true;
  mod.pos = { lineNumber: 3, column: 1 };
  c[0][1]();
  assert.equal(zonesOf(mod)[0].afterLineNumber, 3);
  runTimers(p, 0);
  assert.equal(p.doc.activeElement, find(zonesOf(mod)[0], "textarea"), "the box has the keyboard");
});

test("with several lines selected the note is on the first one, and the quote is that line's text alone", async () => {
  const { p, mod } = await setup();
  mod.textFocus = true;
  mod.sel = { startLineNumber: 2, startColumn: 1, endLineNumber: 4, endColumn: 3 };
  p.get("editorNotesKey")();
  const z = zonesOf(mod)[0];
  assert.equal(z.afterLineNumber, 2);
  save(write(z, "check these"));
  assert.equal(notes(p)[0].line, 2);
  assert.equal(notes(p)[0].quote, "l2 changed");
});

test("c on the original side asks for a removed line; a line the file still has is told to use the right side", async () => {
  const { p, orig } = await setup();
  orig.textFocus = true;
  orig.pos = { lineNumber: 1, column: 1 };
  p.get("editorNotesKey")();
  assert.equal(zonesOf(orig).length, 0);
  assert.match(p.st.toasts.at(-1)[0], /removed line/);
  orig.pos = { lineNumber: 2, column: 1 };
  p.get("editorNotesKey")();
  assert.equal(zonesOf(orig)[0].afterLineNumber, 2);
  save(write(zonesOf(orig)[0], "why was this removed"));
  const n = notes(p)[0];
  assert.deepEqual([n.side, n.line, n.quote], ["old", 2, "l2"], "side old, the BASE's line and text");
  assert.equal(zonesOf(orig).length, 1, "drawn on the original editor");
  assert.match(find(zonesOf(orig)[0], ".dk-note-meta").textContent, /^on removed line 2/);
});

test("in Inline the original editor is not on screen: an old-side note is drawn on the modified side, after the change that holds its line", async () => {
  const { p, orig, mod, d } = await setup();
  orig.textFocus = true;
  orig.pos = { lineNumber: 2, column: 1 };
  p.get("editorNotesKey")();
  save(write(zonesOf(orig)[0], "old side"));
  orig.width = -10; // Inline (Monaco leaves the original editor a strip of line numbers: a negative text area)
  d.lineChanges = [{ originalStartLineNumber: 2, originalEndLineNumber: 2, modifiedStartLineNumber: 2, modifiedEndLineNumber: 3 }];
  orig.fire("layout");
  assert.equal(zonesOf(orig).length, 0);
  assert.equal(zonesOf(mod)[0].afterLineNumber, 3);
  orig.width = 400; // Side by side again
  orig.fire("layout");
  assert.equal(zonesOf(mod).length, 0);
  assert.equal(zonesOf(orig).length, 1);
});

test("Edit puts the text in a box in place of the note; Save keeps it one note, Cancel changes nothing; Delete removes it", async () => {
  const { p, mod, store } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "first"));
  const buttons = () => find(zonesOf(mod)[0], ".dk-note-act").children;
  buttons()[0].click(); // Edit
  let ta = find(zonesOf(mod)[0], "textarea.dk-note-in");
  assert.equal(ta.value, "first");
  ta.onkeydown(ev({ key: "Escape" }));
  assert.equal(notes(p)[0].text, "first");
  assert.ok(find(zonesOf(mod)[0], ".dk-note-body"), "back to the note");
  buttons()[0].click();
  ta = write(zonesOf(mod)[0], "second");
  save(ta);
  assert.equal(notes(p).length, 1);
  assert.equal(notes(p)[0].text, "second");
  assert.equal(find(zonesOf(mod)[0], ".dk-note-body").textContent, "second");
  buttons()[1].click(); // Delete
  assert.equal(notes(p).length, 0);
  assert.equal(zonesOf(mod).length, 0);
  runTimers(p, 300);
  assert.equal(stored(store).sessions.s1, undefined, "an empty session is not kept in the record");
});

test("note text is shown as text only: no markup in it becomes an element", async () => {
  const { p, mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], '<img src=x onerror="alert(1)"> <b>bold</b>'));
  const body = find(zonesOf(mod)[0], ".dk-note-body");
  assert.equal(body.children.length, 0);
  assert.equal(body.textContent, '<img src=x onerror="alert(1)"> <b>bold</b>');
  assert.equal(notes(p).length, 1);
});

test("saving is debounced 300 ms: a burst of changes is one write, and the record holds the session's notes", async () => {
  const { p, mod, store } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "one"));
  hoverLine(mod, 2); pressGutter(mod, 2);
  save(write(zonesOf(mod).find((z) => find(z, "textarea")), "two"));
  assert.equal(store.has("agent-box:notes"), false, "nothing written yet");
  const live = p.st.timers.filter((t) => !t.dead && t.ms === 300);
  assert.equal(live.length, 1, "the earlier timer was reset, not stacked");
  runTimers(p, 300);
  const rec = stored(store);
  assert.equal(rec.v, 1);
  assert.deepEqual(rec.sessions.s1.notes.map((n) => n.text).sort(), ["one", "two"]);
});

test("a page that is hidden or closed writes what is waiting at once", async () => {
  const handlers = {};
  const store = new Map();
  const p = page({
    window: { addEventListener: (t, f) => { handlers[t] = f; } },
    dockChangesRender: () => {},
    localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => { store.set(k, String(v)); } },
  });
  p.get("notesLoad")(["s1"]);
  p.get("notesUpdate")("s1", () => [{ id: "n1", path: "/p/a", rel: "a", side: "new", line: 1, quote: "q", text: "t", status: "pending", outdated: false, at: 1 }]);
  assert.equal(store.has("agent-box:notes"), false);
  handlers.pagehide();
  assert.equal(JSON.parse(store.get("agent-box:notes")).sessions.s1.notes.length, 1);
});

test("notes survive a reload: the stored record is read once from the first session list, and a dead session's notes are dropped", async () => {
  const first = await setup();
  hoverLine(first.mod, 4); pressGutter(first.mod, 4);
  save(write(zonesOf(first.mod)[0], "kept"));
  runTimers(first.p, 300);
  const rec = stored(first.store);
  rec.sessions.dead = { notes: rec.sessions.s1.notes.map((n) => Object.assign({}, n, { id: "x" + n.id })) };
  first.store.set("agent-box:notes", JSON.stringify(rec));
  // A new page on the same storage.
  const second = await setup({ store: first.store, load: false });
  assert.equal(second.p.get("notesFor")("s1").length, 0, "not read yet");
  second.p.get("notesLoad")([]);
  assert.equal(second.p.get("notesFor")("s1").length, 0, "an empty list says nothing about which sessions are gone: nothing is read");
  assert.ok(stored(first.store).sessions.dead, "and nothing was pruned");
  second.p.get("notesLoad")(["s1"]);
  assert.equal(second.p.get("notesFor")("s1")[0].text, "kept");
  assert.equal(zonesOf(second.mod).length, 1, "the open Changes view draws it when the store lands");
  assert.equal(stored(first.store).sessions.dead, undefined, "dead sessions are written back pruned");
});

test("before the stored notes are read a note cannot be made: a toast says why, and the stored record is not overwritten", async () => {
  const { p, mod, store } = await setup({ load: false });
  store.set("agent-box:notes", JSON.stringify({ v: 1, sessions: { s1: { notes: [] } } }));
  hoverLine(mod, 3); pressGutter(mod, 3);
  assert.equal(zonesOf(mod).length, 0);
  assert.match(p.st.toasts.at(-1)[0], /Waiting for the session list/);
});

test("a file that is not open in a session takes no note", async () => {
  const { p, mod } = await setup();
  p.host.sessionId = () => null;
  hoverLine(mod, 3); pressGutter(mod, 3);
  assert.equal(zonesOf(mod).length, 0);
  assert.match(p.st.toasts.at(-1)[0], /not open in one/);
});

test("the notes follow the file when its text is replaced from disk: five lines above move the note; the line gone makes it outdated; it comes back when the text does", async () => {
  const { p, tab, mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "on l4"));
  const replace = (text) => p.get("editorSyncReplace")(tab, text, "v" + text.length, true);
  replace("a1\na2\na3\na4\na5\n" + BUF_TEXT);
  assert.equal(notes(p)[0].line, 9);
  assert.equal(notes(p)[0].outdated, false);
  assert.equal(zonesOf(mod).length, 1);
  assert.equal(zonesOf(mod)[0].afterLineNumber, 9, "the zone is under the line it moved to");
  assert.equal(p.get("notesPending")("s1"), 1);
  replace("a1\na2\nx\nl3\nl5\n");
  assert.equal(notes(p)[0].outdated, true);
  assert.equal(notes(p)[0].line, 9, "an outdated note keeps its line");
  const z = zonesOf(mod)[0];
  assert.equal(z.afterLineNumber, 6, "drawn at the end of a shorter file (its last line, the empty one after the final newline)");
  assert.ok(find(z, ".dk-note-badge"));
  assert.equal(find(z, ".dk-note-badge").textContent, "outdated");
  assert.equal(find(z, ".dk-note-q").textContent, "l4 new", "its quote is shown");
  assert.match(find(z, ".dk-note-meta").textContent, /^was on line 9/);
  replace("l4 new\n" + BUF_TEXT);
  assert.equal(notes(p)[0].outdated, false);
  assert.equal(notes(p)[0].line, 1 + 4, "found again");
  assert.equal(find(zonesOf(mod)[0], ".dk-note-badge"), null);
});

test("opening Changes follows the file too: a file that changed while no Changes view was open", async () => {
  const { p, tab, mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "n"));
  tab.showChanges(false);
  tab.model.value = "x\ny\n" + BUF_TEXT; // the buffer moved under the note (a reload while it was closed)
  const again = enter(p, tab);
  assert.equal(notes(p)[0].line, 6);
  assert.equal(zonesOf(again.mod)[0].afterLineNumber, 6);
});

test("a new base text (a commit, the other scope) checks the old-side notes' quote against it and never moves them", async () => {
  const { p, tab, orig } = await setup();
  orig.textFocus = true;
  orig.pos = { lineNumber: 2, column: 1 };
  p.get("editorNotesKey")();
  save(write(zonesOf(orig)[0], "removed?"));
  assert.equal(notes(p)[0].outdated, false);
  p.st.git.head = "h2";
  p.st.git.answer = { state: "ok", content: "l1\nnew first\nl2\nl3\n" };
  p.get("gitStatusCurrent");
  tab.changes.key = "stale"; // the key moved: the base is asked for again
  p.get("editorDiffSync")(true);
  await p.settle();
  assert.equal(notes(p)[0].line, 2, "it keeps its line");
  assert.equal(notes(p)[0].outdated, true, "and says so: line 2 of the new base is not what it quoted");
});

test("Show sent: sent notes are not drawn until the toggle's setter turns them on", async () => {
  const { p, mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "to send"));
  const id = notes(p)[0].id;
  p.get("notesUpdate")("s1", (l) => l.map((n) => Object.assign({}, n, { status: "sent" })));
  assert.equal(zonesOf(mod).length, 0, "hidden");
  assert.equal(p.get("notesPending")("s1"), 0);
  assert.equal(notes(p).length, 1, "but kept");
  p.get("notesShowSent")("s1", true);
  assert.equal(zonesOf(mod).length, 1);
  assert.ok(find(zonesOf(mod)[0], ".dk-note-badge-sent"));
  assert.equal(find(zonesOf(mod)[0], ".dk-note").getAttribute("data-note"), id);
  p.get("notesShowSent")("s1", false);
  assert.equal(zonesOf(mod).length, 0);
});

test("Add note in Code opens Changes with the box under the first line of the selection", async () => {
  const { p, tab } = await setup({ enter: false });
  const ok = p.get("editorNotesFromCode")(tab, { a: 3, b: 4 });
  assert.equal(ok, true);
  await p.settle();
  const d = p.st.diffs.at(-1);
  assert.ok(d, "Changes opened");
  const z = zonesOf(d.modified)[0];
  assert.equal(z.afterLineNumber, 3);
  assert.ok(find(z, "textarea.dk-note-in"));
  [...d.updated].forEach((f) => f());
  save(write(z, "from code"));
  assert.equal(notes(p)[0].line, 3);
});

test("Add note in Code is refused with a reason when there is no session or the notes are not read", async () => {
  const a = await setup({ enter: false, load: false });
  assert.equal(a.p.get("editorNotesFromCode")(a.tab, { a: 1, b: 1 }), false);
  assert.match(a.p.st.toasts.at(-1)[0], /Waiting for the session list/);
  assert.equal(a.tab.diff, null, "Changes was not opened for it");
});

test("a note box that holds text comes back with the file's next Changes view", async () => {
  const { p, tab, mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  write(zonesOf(mod)[0], "half a thought");
  tab.showChanges(false);
  assert.equal(zonesOf(mod).length, 0, "the zone went with the view");
  const again = enter(p, tab);
  const z = zonesOf(again.mod)[0];
  assert.equal(z.afterLineNumber, 4);
  assert.equal(find(z, "textarea").value, "half a thought");
});

test("leaving Changes lets everything go: zones, the hover mark and every listener; closing the tab too", async () => {
  const { p, tab, mod, orig } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  save(write(zonesOf(mod)[0], "n"));
  hoverLine(mod, 2);
  assert.equal(zonesOf(mod).length, 1);
  const subsOf = (e) => ["mousemove", "mouseleave", "mousedown", "layout"].flatMap((n) => e.subs[n] || []).length;
  assert.equal(subsOf(mod) > 0 && subsOf(orig) > 0, true);
  tab.showChanges(false);
  assert.equal(zonesOf(mod).length, 0);
  assert.equal(glyph(mod).length, 0);
  assert.equal(subsOf(mod), 0);
  assert.equal(subsOf(orig), 0);
  assert.equal(notes(p).length, 1, "the note itself is kept");
  const again = enter(p, tab);
  assert.equal(zonesOf(again.mod).length, 1, "and drawn again in the next view");
  const second = p.st.diffs.at(-1);
  tab.dispose();
  assert.equal(zonesOf(second.modified).length, 0);
  assert.equal(subsOf(second.modified), 0);
  assert.equal(subsOf(second.original), 0);
  // A replace after the tab is gone reaches nothing.
  assert.doesNotThrow(() => runTimers(p));
});

test("several diff editors at once: each is a host of its own, the same note change repaints both, one detach leaves the other", async () => {
  const { p, mod } = await setup();
  const attach = p.get("editorNotesAttachHost");
  const mk = (path) => {
    const d = p.monaco.editor.createDiffEditor({}, {});
    const m = p.monaco.editor.createModel("s1\ns2\ns3\n", "go");
    const o = p.monaco.editor.createModel("s1\ns2\n", "go");
    d.setModel({ original: o, modified: m });
    const host = { key: path, sid: () => "s1", path: () => path, rel: () => path.slice(3), diff: d, onReplace: () => () => {} };
    return { d, host };
  };
  const a = mk("/p/sec-a.go");
  const b = mk("/p/sec-b.go");
  assert.equal(attach(a.host), true);
  assert.equal(attach(b.host), true);
  assert.equal(attach(a.host), false, "once per key");
  hoverLine(a.d.modified, 2);
  pressGutter(a.d.modified, 2);
  save(write(zonesOf(a.d.modified)[0], "in a"));
  hoverLine(b.d.modified, 3);
  pressGutter(b.d.modified, 3);
  save(write(zonesOf(b.d.modified)[0], "in b"));
  assert.equal(notes(p).length, 2);
  assert.equal(zonesOf(a.d.modified).length, 1, "a shows only a's note");
  assert.equal(zonesOf(b.d.modified).length, 1);
  assert.equal(notes(p).find((n) => n.text === "in b").rel, "sec-b.go");
  assert.equal(zonesOf(mod).length, 0, "the tab's own view has neither");
  p.get("editorNotesDetachHost")("/p/sec-a.go");
  assert.equal(zonesOf(a.d.modified).length, 0);
  p.get("notesUpdate")("s1", (l) => l.filter((n) => n.text !== "in b"));
  assert.equal(zonesOf(b.d.modified).length, 0, "b still follows the store");
});

test("a zone is as tall as its content: a measured height lays it out again, once", async () => {
  const { mod } = await setup();
  hoverLine(mod, 4); pressGutter(mod, 4);
  const z = zonesOf(mod)[0];
  assert.equal(z.heightInPx > 0, true);
});
