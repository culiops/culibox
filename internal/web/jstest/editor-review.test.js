"use strict";
// The review loop in the Changes view (editorreview.js, the diff modes in editordiff.js), run on the real tab and the fake Monaco of
// editorpage.js: the header row, "Change N of M", F7 and Shift+F7 inside a file and across files, Mark reviewed with its baseline and its
// disabled reasons, the layout control and Wrap diff. The pure half is reviewmodel.test.js; how Monaco draws any of it, and that a
// command added with addCommand outranks Monaco's own F7 in a diff editor, is the browser check's (the task report).
const test = require("node:test");
const assert = require("node:assert");
const { page, keydown } = require("./editorpage.js");

const HEAD_TEXT = "l1\nl2\nl3\nl5\nl6\n";
const BUF_TEXT = "l1\nl2 changed\nl3\nl4 new\nl5\n";
// lineDiff(HEAD_TEXT, BUF_TEXT): a modification at line 2, an addition at 4, a removal at the very end (line 6 of 6).
const row = (path, o) => Object.assign({ path, name: path.split("/").pop(), dir: "", code: "M", add: 3, del: 1, mine: true, folder: false, provisional: false }, o);
const ROWS = [row("/p/a.go"), row("/p/b.go"), row("/p/c.go")];

// A page whose file /p/b.go (the middle one of three changed files) holds BUF_TEXT, git has HEAD_TEXT for it, and the Changes section
// shows the three rows. `shown` records drawerShowFile, `store` is localStorage.
async function review(o) {
  const opt = Object.assign({ rows: ROWS, files: [{ path: "/p/b.go", ts: 100 }], path: "/p/b.go" }, o);
  const shown = [];
  const store = new Map();
  const tabs = new Map();
  const p = page(Object.assign({
    dockChangesRows: () => opt.rows,
    drawerState: { sessionId: "s1" },
    drawerChangedFiles: opt.files ? new Map([["s1", opt.files]]) : new Map(),
    dockViewers: tabs,
    dockTabKey: (path) => path,
    drawerShowFile: (path, os) => { shown.push([path, os]); return true; },
    localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => { store.set(k, String(v)); } },
  }, opt.over));
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: BUF_TEXT, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = { state: "ok", content: HEAD_TEXT };
  p.setShown(true);
  const tab = await p.open(opt.path);
  await p.settle();
  if (!opt.noMarks) p.get("reviewMarksLoad")(["s1"]);
  tab.showChanges(true);
  const d = p.st.diffs[0];
  [...d.updated].forEach((f) => f()); // Monaco computed the diff: the view opens at the first change
  return { p, tab, d, shown, store, tabs, at: () => d.modified.getPosition().lineNumber, head: tab.diff.head };
}

// The command added for a chord (keys as monacoKeyFor makes them), wherever it was added: the page's keybinding service has one.
const command = (p, keys) => {
  const all = [...p.st.editors, ...p.st.diffs.map((x) => x.modified)];
  const c = all.flatMap((e) => e.commands).filter(([k]) => k === keys).at(-1);
  assert.ok(c, "a command for " + keys);
  return c[1];
};
const F7 = 65, SHIFT = 1024, ALT = 512, KEYZ = 56;

test("the header row: the name, what is compared, git's +a −d for the file, Change N of M and the two buttons", async () => {
  const { tab, head } = await review();
  const el = tab.diff.wrap.children[0];
  assert.equal(el.className, "dk-dhead");
  assert.equal(el.children[0].textContent, "b.go");
  assert.equal(head.label.textContent, "HEAD ↔ working tree");
  assert.equal(head.ct.hidden, false);
  assert.equal(head.add.textContent, "+3");
  assert.equal(head.del.textContent, "−1");
  assert.equal(head.pos.textContent, "Change 1 of 3", "it opened on the first change, the cursor on it");
  assert.equal(head.next.getAttribute("aria-disabled"), "false");
  assert.equal(head.prev.getAttribute("aria-disabled"), "false", "there is a file before this one");
  assert.match(head.next.title, /Next change \(F7\)/);
  assert.match(head.prev.title, /Previous change \(Shift\+F7\)/);
});

test("a file with no Changes row has no counts, and no neighbour to go to", async () => {
  const { head, tab } = await review({ rows: [] });
  assert.equal(head.ct.hidden, true, "git gave no count: none is guessed");
  assert.equal(tab.changes.lineChanges.length, 3, "the changes are still numbered, from the file");
  assert.equal(head.mark.getAttribute("aria-disabled"), "true");
  assert.match(head.mark.title, /no changes in this scope/);
});

test("F7 goes to the next change, Shift+F7 back; the header follows, and the cursor is put on the change", async () => {
  const { p, d, at, head } = await review();
  assert.equal(at(), 2);
  command(p, F7)();
  assert.equal(at(), 4);
  assert.equal(head.pos.textContent, "Change 2 of 3");
  assert.deepEqual(d.modified.revealed.at(-1), 4);
  command(p, F7 | SHIFT)();
  assert.equal(at(), 2);
  assert.equal(head.pos.textContent, "Change 1 of 3");
  assert.equal(d.modified.focused > 0, true, "the keyboard stays in the view");
});

test("a click that moves the cursor off the change takes N from the cursor; the last change, a removal at the end, is reached and then F7 leaves the file", async () => {
  const { p, tab, d, at, head, shown } = await review();
  d.modified.setPosition({ lineNumber: 3, column: 1 }); // the reader clicked line 3
  d.modified.fire("cursor");
  assert.equal(head.pos.textContent, "Change 1 of 3");
  command(p, F7)();
  assert.equal(at(), 4);
  command(p, F7)();
  assert.equal(at(), 6, "the removal at the end sits at line 6 of a 6-line buffer");
  assert.equal(head.pos.textContent, "Change 3 of 3");
  command(p, F7)(); // there is no later change in this file: the next file opens
  await p.settle();
  assert.deepEqual(shown, [["/p/c.go", { view: "changes" }]]);
  assert.equal(at(), 6, "the cursor was not moved off the last change");
  assert.equal(tab.disposed, false);
});

test("past the last change the next row opens in Changes and lands on its first change with the keyboard; before the first, the previous row on its last", async () => {
  const { p, d, shown, tabs } = await review();
  // The tabs the dock will have made for the next rows (drawerShowFile makes them): what the stub looks up.
  const next = { dg: null, wantFocus: false };
  tabs.set("/p/c.go", next);
  command(p, F7)(); command(p, F7)(); command(p, F7)();
  await p.settle();
  assert.deepEqual(shown.at(-1), ["/p/c.go", { view: "changes" }]);
  assert.equal(next.dg.land.focus, true, "the keyboard goes with the reader");
  assert.equal(next.dg.land.at, "first");
  // Back: on the first change (the cursor is there from the opening), Shift+F7 goes to the file before, on its last change.
  const prev = { dg: null, wantFocus: false };
  tabs.set("/p/a.go", prev);
  d.modified.setPosition({ lineNumber: 2, column: 1 });
  command(p, F7 | SHIFT)();
  await p.settle();
  assert.deepEqual(shown.at(-1), ["/p/a.go", { view: "changes" }]);
  assert.equal(prev.dg.land.at, "last");
});

test("a tab opened by F7 lands where it was told: the first change going on, the last going back, whatever scroll it had", async () => {
  const { p, tab, d, at } = await review();
  tab.onHide(); // the Changes view is let go; the tab comes back in Changes at the place it was left
  d.modified.top = 420;
  p.get("editorDiffOf")(tab).land = { at: "last", ts: Date.now() };
  tab.onAttach();
  const d2 = p.st.diffs[1];
  const placed = [];
  d2.modified.setScrollTop = (v) => placed.push(v);
  [...d2.updated].forEach((f) => f());
  assert.deepEqual(placed, [], "the old scroll is not restored over a landing");
  assert.deepEqual(d2.modified.positions.at(-1), { lineNumber: 6, column: 1 }, "on its LAST change (the trailing removal, clamped to line 6)");
  assert.equal(tab.dg.navIndex, 2);
  assert.equal(tab.diff.head.pos.textContent, "Change 3 of 3");
  assert.equal(d2.modified.focused, 0, "no keyboard asked for: none taken");
  // A landing nobody took long ago is forgotten.
  tab.onHide();
  p.get("editorDiffOf")(tab).land = { at: "last", ts: Date.now() - 60000 };
  tab.onAttach();
  assert.equal(p.get("editorReviewLanding")(tab), null);
});

test("with no row before or after, F7 says so in a toast and nothing opens", async () => {
  const { p, shown } = await review({ path: "/p/a.go" });
  command(p, F7 | SHIFT)();
  await p.settle();
  assert.deepEqual(shown, []);
  assert.deepEqual(p.st.toasts.at(-1)[0], "That was the first changed file");
  const q = await review({ path: "/p/c.go" });
  command(q.p, F7)(); command(q.p, F7)(); command(q.p, F7)();
  await q.p.settle();
  assert.deepEqual(q.p.st.toasts.at(-1)[0], "That was the last changed file");
  const r = await review({ rows: [], path: "/p/c.go" });
  command(r.p, F7 | SHIFT)();
  await r.p.settle();
  assert.deepEqual(r.p.st.toasts.at(-1)[0], "No other changed files", "an unchanged file has no neighbours");
});

test("a deleted file's tab: the whole file is one removal, and Change 1 of 1 is shown at line 1", async () => {
  const p = page({
    dockChangesRows: () => [row("/p/gone.go", { code: "D" })], drawerState: { sessionId: "s1" }, drawerChangedFiles: new Map([["s1", []]]),
    localStorage: { getItem: () => null, setItem() {} },
  });
  p.setRead({ ok: false, status: 404, data: { error: "not found" } });
  p.st.git.on = true;
  p.st.git.answer = { state: "ok", content: HEAD_TEXT };
  p.setShown(true);
  const tab = await p.open("/p/gone.go", { view: "changes" });
  await p.settle();
  assert.ok(tab.diff, "it opened in Changes");
  assert.deepEqual(tab.changes.lineChanges, [{ kind: "del", from: 1, to: 1 }]);
  const d = p.st.diffs[0];
  [...d.updated].forEach((f) => f());
  assert.equal(tab.diff.head.pos.textContent, "Change 1 of 1");
  assert.deepEqual(d.modified.positions.at(-1), { lineNumber: 1, column: 1 });
});

test("F7, Shift+F7 and Alt+Z pressed on the header's own buttons (outside Monaco) do the same", async () => {
  const { tab, at, head, p } = await review();
  const key = (code, over) => { const e = keydown(Object.assign({ code, key: code, ctrlKey: false }, over)); tab.el.listeners.keydown(e); return e; };
  assert.equal(key("F7").defaultPrevented, true);
  assert.equal(at(), 4);
  assert.equal(key("F7", { shiftKey: true }).defaultPrevented, true);
  assert.equal(at(), 2);
  assert.equal(head.pos.textContent, "Change 1 of 3");
  assert.equal(p.ctx.viewerPrefs.getDiffWrap(), false);
  assert.equal(key("KeyZ", { altKey: true }).defaultPrevented, true);
  assert.equal(p.ctx.viewerPrefs.getDiffWrap(), true);
  // A key Monaco already handled is not handled twice.
  assert.equal(key("F7", { defaultPrevented: true }).defaultPrevented, true);
  assert.equal(at(), 2);
});

test("Mark reviewed: disabled with its reason until the session list and the session's Activity are known, then a toggle", async () => {
  const a = await review({ noMarks: true });
  assert.equal(a.head.mark.getAttribute("aria-disabled"), "true");
  assert.match(a.head.mark.title, /Waiting for the session list/);
  a.head.mark.onclick();
  assert.equal(a.store.size, 0, "nothing was marked");
  const b = await review({ files: null });
  assert.equal(b.head.mark.getAttribute("aria-disabled"), "true");
  assert.match(b.head.mark.title, /Waiting for the session's activity/);
  b.head.mark.onclick();
  assert.equal(b.store.size, 0, "a mark with no baseline would clear on the first older edit: none is made");
  const c = await review();
  assert.equal(c.head.mark.getAttribute("aria-disabled"), "false");
  assert.equal(c.head.mark.textContent, "Mark reviewed");
  assert.equal(c.head.mark.getAttribute("aria-pressed"), "false");
  c.head.mark.onclick();
  const rec = JSON.parse(c.store.get("agent-box:reviewed"));
  assert.deepEqual(rec.sessions.s1.marks["/p/b.go"], { path: "/p/b.go", at: rec.sessions.s1.marks["/p/b.go"].at, fp: "M:3:1", seen: 100 }, "the baseline is Activity's own ts");
  assert.equal(c.head.mark.textContent, "Reviewed");
  assert.equal(c.head.mark.getAttribute("aria-pressed"), "true");
  c.head.mark.onclick(); // pressed again: the mark goes
  assert.equal(c.head.mark.textContent, "Mark reviewed");
  assert.deepEqual(JSON.parse(c.store.get("agent-box:reviewed")).sessions, {});
});

test("a mark stops standing when the agent edits the file again, and when git's count for it moves", async () => {
  const { p, tab, head } = await review();
  head.mark.onclick();
  assert.equal(head.mark.getAttribute("aria-pressed"), "true");
  // The agent edits it: Activity's list has a newer ts. The page repaints the header (dockChangesRender -> editorReviewRefresh).
  p.ctx.drawerChangedFiles.set("s1", [{ path: "/p/b.go", ts: 101 }]);
  p.get("editorReviewRefresh")();
  assert.equal(head.mark.getAttribute("aria-pressed"), "false");
  assert.equal(head.mark.textContent, "Mark reviewed");
  head.mark.onclick(); // reviewed again, against the newer baseline
  assert.equal(head.mark.getAttribute("aria-pressed"), "true");
  const marks = p.get("reviewMarksOf")("s1");
  assert.equal(marks["/p/b.go"].seen, 101);
  // git's numbers moved: the row's fingerprint is another.
  p.ctx.dockChangesRows = () => [row("/p/b.go", { add: 9 })];
  p.get("editorReviewRefresh")();
  assert.equal(head.mark.getAttribute("aria-pressed"), "false");
  assert.equal(tab.disposed, false);
});

test("the marks are read once, from a real list: an empty one reads nothing, a second one is ignored", async () => {
  const p = page({ localStorage: { getItem: (k) => (k === "agent-box:reviewed" ? JSON.stringify({ v: 1, sessions: { s1: { marks: { "/p/b.go": { path: "/p/b.go", at: 1, fp: "M:3:1", seen: 5 } } }, dead: { marks: { "/p/x": { path: "/p/x", at: 1, fp: "M::", seen: 0 } } } } }) : null), setItem() {} } });
  const load = p.get("reviewMarksLoad");
  const of = p.get("reviewMarksOf");
  load([]);
  load(undefined);
  assert.deepEqual(Object.keys(of("s1")), [], "an empty list says nothing about which sessions are gone: nothing is read");
  load(["s1"]);
  assert.deepEqual(Object.keys(of("s1")), ["/p/b.go"]);
  assert.deepEqual(Object.keys(of("dead")), []);
  load(["other"]);
  assert.deepEqual(Object.keys(of("s1")), ["/p/b.go"], "read once");
});

test("the layout control shows in Changes only; Side by side is the default and the choice goes to the view and the prefs", async () => {
  const { p, tab, d } = await review();
  const m = tab.modeSeg;
  assert.equal(m.el.hidden, false);
  assert.equal(m.side.getAttribute("aria-pressed"), "true");
  assert.equal(m.inline.getAttribute("aria-pressed"), "false");
  assert.equal(d.opts.renderSideBySide, true);
  assert.equal(d.opts.useInlineViewWhenSpaceIsLimited, true);
  m.inline.onclick();
  assert.equal(p.ctx.viewerPrefs.getDiffMode(), "inline");
  assert.equal(d.options.at(-1).renderSideBySide, false);
  assert.equal(d.options.at(-1).useInlineViewWhenSpaceIsLimited, true, "the fallback stays on in either mode");
  assert.equal(m.inline.getAttribute("aria-pressed"), "true");
  assert.equal(m.side.getAttribute("aria-pressed"), "false");
  p.get("diffModeSet")("side");
  assert.equal(d.options.at(-1).renderSideBySide, true);
  // Leaving Changes hides it.
  tab.showChanges(false);
  assert.equal(m.el.hidden, true);
});

test("Wrap diff and Line numbers act on the diff only: Alt+Z in the view toggles Wrap diff, in Code the editor's wrap", async () => {
  const wraps = [];
  const { p, tab, d } = await review({ over: { toggleViewerWrap: () => wraps.push("code") } });
  assert.equal(d.opts.diffWordWrap, "off");
  assert.equal("wordWrap" in d.opts, false, "the editor's wrap is not the diff's");
  d.modified.focus(); // the keyboard is in the Changes view
  command(p, ALT | KEYZ)();
  assert.equal(p.ctx.viewerPrefs.getDiffWrap(), true);
  assert.equal(d.options.at(-1).diffWordWrap, "on");
  assert.equal("wordWrap" in d.options.at(-1), false);
  assert.deepEqual(wraps, [], "the editor's wrap was not toggled");
  p.get("diffLineNumbersToggle")();
  assert.equal(d.options.at(-1).lineNumbers, "off");
  // The editor's own font or wrap change reaches the diff without its wrap.
  p.get("editorApplyPrefs")();
  assert.equal("wordWrap" in d.options.at(-1), false);
  assert.equal(d.options.at(-1).diffWordWrap, "on");
  // In Code (no Changes view, or the keyboard elsewhere): the editor's wrap.
  d.modified.blur();
  command(p, ALT | KEYZ)();
  assert.deepEqual(wraps, ["code"]);
  tab.showChanges(false);
  command(p, ALT | KEYZ)();
  assert.deepEqual(wraps, ["code", "code"]);
});

test("the ⋯ menu in Changes has Wrap diff and Line numbers, in Code Wrap lines", async () => {
  const { El } = require("./editorpage.js");
  const items = [];
  const { p, tab } = await review({
    over: {
      ftShowMenu: (a, nodes) => items.push(nodes.map((n) => n.label)),
      kitMenuButton: (label, icon, danger, run) => { const n = new El("button", null); n.label = label; n.run = run; return n; },
      kitMenuSep: () => ({ label: "-" }),
      closeRowMenu: () => {}, window: { HOME_DIR: "/h" },
      relativeAvail: () => ({ ok: true }),
      revealable: () => ({ ok: true }),
    },
  });
  const menu = new El("div", p.doc);
  menu.querySelector = () => null;
  p.doc.getElementById = (id) => (id === "ft-menu" ? menu : null);
  tab.fontRow = () => ({ label: "Font size" });
  tab.openMenu();
  assert.ok(items[0].includes("Wrap diff") && items[0].includes("Line numbers") && !items[0].includes("Wrap lines"), items[0].join("|"));
  tab.showChanges(false);
  tab.openMenu();
  assert.ok(items[1].includes("Wrap lines") && !items[1].includes("Wrap diff") && !items[1].includes("Line numbers"), items[1].join("|"));
});

test("while a tab is in Changes the Changes section is told (editorDiffAnyChanges), and when it leaves", async () => {
  const { p, tab, tabs } = await review();
  tabs.set("/p/b.go", tab);
  p.ctx.dockViewerList = () => [...tabs.values()];
  assert.equal(p.get("editorDiffAnyChanges")(), true);
  tab.onHide();
  assert.equal(p.get("editorDiffAnyChanges")(), true, "a tab left in Changes comes back in it");
  tab.showChanges(false);
  assert.equal(p.get("editorDiffAnyChanges")(), false);
});

test("a Changes view opened by F7 takes the keyboard, one opened any other way does not", async () => {
  const { p, tab } = await review();
  tab.onHide();
  p.get("editorDiffOf")(tab).land = { at: "first", ts: Date.now(), focus: true };
  tab.onAttach();
  assert.equal(p.st.diffs[1].modified.focused > 0, true);
  tab.onHide();
  p.get("editorDiffOf")(tab).land = { at: "first", ts: Date.now() - 60000, focus: true };
  tab.onAttach();
  assert.equal(p.st.diffs[2].modified.focused, 0, "an old request is not a request");
});

test("a tab the dock already has is told where to land before it is opened (a hidden tab opens its Changes view inside that call)", async () => {
  const { p, tabs, d } = await review();
  const existing = { dg: null };
  tabs.set("/p/c.go", existing);
  const seen = [];
  p.ctx.drawerShowFile = (path) => { seen.push(existing.dg && existing.dg.land); return true; };
  d.modified.setPosition({ lineNumber: 6, column: 1 });
  command(p, F7)();
  await p.settle();
  assert.equal(seen.length, 1);
  assert.deepEqual({ at: seen[0].at, focus: seen[0].focus }, { at: "first", focus: true }, "known to the tab when drawerShowFile runs");
});

test("View all in the header opens the All changes tab at this file's section; F7 goes to the tab first when it is on screen", async () => {
  const opened = [];
  const steps = [];
  let on = false;
  const { p, tab, d } = await review({
    over: {
      reviewAllOpen: (sid, path) => { opened.push([sid, path]); return true; },
      reviewAllStep: (dir) => { steps.push(dir); return on; },
    },
  });
  const all = tab.diff.head.el.querySelector(".dk-dh-all");
  assert.ok(all, "a View all button in the header");
  assert.equal(all.textContent, "View all");
  all.click();
  assert.deepEqual(opened, [["s1", "/p/b.go"]]);
  // The tab is not on screen: the call is offered the key, answers no, and the file's own view steps.
  const before = d.modified.getPosition().lineNumber;
  command(p, F7)();
  assert.deepEqual(steps, [1]);
  assert.notEqual(d.modified.getPosition().lineNumber, before, "the single-file view moved");
  // The tab is on screen: it took the key, so this view did not move.
  on = true;
  const at = d.modified.getPosition().lineNumber;
  command(p, F7)();
  assert.deepEqual(steps, [1, 1]);
  assert.equal(d.modified.getPosition().lineNumber, at);
});
