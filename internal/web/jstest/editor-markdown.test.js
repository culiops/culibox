"use strict";
// The markdown Preview of the dock's EditorTab (editormd.js, task 9): the real editortab.js, editorsync.js, editordiff.js, editormd.js
// and the pure modules in a vm on editorpage.js's fake page and fake Monaco. The render itself (mdsafe.js) is stubbed there: a fragment
// that holds the text it was asked for, so what these tests see is WHEN the preview draws and from WHICH buffer, and who holds the
// body. That the render loads nothing and runs nothing is md-render.test.js (the walk) and the task report (the real DOMPurify and
// marked on a hostile file, with the network log). What Monaco does with a detached editor is the browser check's.
const test = require("node:test");
const assert = require("node:assert");
const { page, keydown } = require("./editorpage.js");

const DOC = "# Title\nline 2\nline 3\nline 4\n";

// A page whose file `path` holds `text`, and a tab read and on screen (in Preview, for a markdown file). `serve`: the page's file system
// is one file the test can change under the tab (`disk`) and the dock is on screen, so the poll runs.
async function md(text, o) {
  const p = page(o && o.over);
  let disk = null;
  if (o && o.serve) { disk = p.serve(text); p.setShown(true); }
  else p.setRead({ ok: true, status: 200, data: { kind: "text", content: text === undefined ? DOC : text, version: "v1" } });
  const tab = await p.open((o && o.path) || "/p/README.md");
  await p.settle();
  return { p, tab, disk };
}
const pane = (tab) => tab.md.pane;
const shown = (tab) => pane(tab).children[0].children[0].textContent; // the text the pane's document was drawn from
const debounce = (p) => p.st.timers.filter((t) => t.ms === 300 && !t.dead);
const pressed = (tab) => ["preview", "code", "changes"].filter((k) => tab.seg[k].getAttribute("aria-pressed") === "true");

test("a markdown tab opens in Preview: the pane draws the buffer, the editor is not in the body, and the toggle reads Preview | Source | Changes", async () => {
  const { p, tab } = await md();
  assert.equal(tab.previewing, true);
  assert.equal(tab.bodyEl.children.length, 1);
  assert.equal(tab.bodyEl.children[0], pane(tab));
  assert.equal(pane(tab).className, "dk-md");
  assert.equal(pane(tab).children[0].className, "md-body");
  assert.equal(shown(tab), DOC);
  assert.equal(p.st.editors.length, 0, "the shared editor is not even made: nothing in Preview needs it");
  assert.equal(p.st.renders.length, 1);
  assert.equal(p.st.renders[0].text, DOC);
  assert.deepEqual([tab.seg.preview.textContent, tab.seg.code.textContent, tab.seg.changes.textContent], ["Preview", "Source", "Changes"]);
  assert.equal(tab.seg.preview.hidden, false);
  assert.deepEqual(pressed(tab), ["preview"]);
  assert.deepEqual(tab.seg.el.children, [tab.seg.preview, tab.seg.code, tab.seg.changes], "one control, Preview in front");
  assert.equal(pane(tab).tabIndex, 0, "the pane can hold the keyboard");
  assert.equal(pane(tab).getAttribute("aria-label"), "Preview of README.md");
});

test("the render gets the page's marked, DOMPurify and highlight.js, and reads a relative image from the file's own directory", async () => {
  const hl = { highlightElement() {} };
  const { p } = await md(DOC, { over: { hljs: hl } });
  const d = p.st.renders[0].deps;
  assert.equal(d.marked, p.ctx.marked);
  assert.equal(d.purify, p.ctx.DOMPurify);
  assert.equal(d.hljs, hl);
  assert.equal(d.hlLabelledOnly, true, "the dock highlights only a block whose fence names its language (no auto-detect)");
  assert.equal(d.hlMaxChars, 102400, "and none over 100 KB (MD_HL_MAX_CHARS)");
  assert.equal(d.rewrite("img/a b.png"), "/api/fs/read?path=" + encodeURIComponent("/p/img/a b.png"));
  // A file of a session's box is read through the box route.
  const b = page();
  b.setRead({ ok: true, status: 200, data: { kind: "text", content: DOC, version: "v1" } });
  const tab = await b.open("/work/shots/r.md", { box: "s9" });
  assert.equal(tab.previewing, true, "a box file has a Preview too (it is read-only, which Preview does not mind)");
  assert.equal(b.st.renders.at(-1).deps.rewrite("a.png"), "/box/s9/work/shots/a.png");
});

test("a file that is not markdown has no Preview: Code is Code, the editor has the body, and the toggle hides Preview", async () => {
  const { p, tab } = await md("package web\n", { path: "/p/a.go" });
  assert.equal(tab.previewing, false);
  assert.equal(tab.seg.preview.hidden, true);
  assert.equal(tab.seg.code.textContent, "Code");
  assert.deepEqual(pressed(tab), ["code"]);
  assert.equal(p.st.editors.length, 1);
  assert.equal(p.st.renders.length, 0);
  assert.equal(tab.md, null, "and keeps no state for it");
  const mk = await md(DOC, { path: "/p/NOTES.MARKDOWN" });
  assert.equal(mk.tab.previewing, true, ".markdown, in any case");
});

test("an image or a binary file has no Preview, whatever its name", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "binary", size: 9 } });
  const tab = await p.open("/p/doc.md");
  assert.ok(tab.inner, "read as a binary file");
  assert.equal(tab.previewing, false);
  assert.equal(tab.seg.preview.hidden, true);
  assert.equal(tab.md, null);
  assert.equal(p.st.renders.length, 0);
});

test("Preview draws the BUFFER, 300 ms after the last edit and not on each one, and the dirty dot, the status bar and Mod+S work in it", async () => {
  const { p, tab } = await md();
  assert.equal(debounce(p).length, 0, "no redraw is waiting");
  tab.model.type("# A\n");
  tab.model.type("# AB\n");
  assert.equal(p.st.renders.length, 1, "nothing is drawn on a keystroke");
  assert.equal(debounce(p).length, 1, "one redraw is waiting: the first was replaced");
  assert.equal(tab.dirty(), true, "the dot");
  assert.ok(p.st.host.includes("start") && p.st.host.includes("dirty"), "the host heard of the edit");
  debounce(p)[0].fn();
  assert.equal(p.st.renders.length, 2);
  assert.equal(p.st.renders[1].text, "# AB\n", "the text of the buffer, saved or not");
  assert.equal(shown(tab), "# AB\n");
  assert.equal(pane(tab), tab.bodyEl.children[0], "the same pane: it is the scroller, so the place read is kept");
  tab.model.type("# AB\n"); // no change in what is drawn: the text is the same as the one drawn
  debounce(p).at(-1).fn();
  assert.equal(p.st.renders.length, 2, "the same text is not drawn again");
  tab.el.listeners.keydown(keydown());
  await p.settle();
  assert.equal(p.st.writes.length, 1, "Mod+S saves from the tab, with the Preview up");
  assert.equal(p.st.writes[0].content, "# AB\n");
  assert.equal(tab.dirty(), false);
  assert.equal(tab.statusText, "Saved");
});

test("the status bar in Preview has no Ln/Col or indentation (mockup B4), and has them back in Source", async () => {
  const { p, tab } = await md();
  assert.equal(tab.sbParts.pos.hidden, true);
  assert.equal(tab.sbParts.indent.hidden, true);
  assert.equal(tab.sbParts.enc.hidden, false);
  assert.equal(tab.sbParts.eol.hidden, false);
  assert.equal(tab.sbParts.lang.hidden, false);
  tab.seg.code.click();
  assert.equal(tab.sbParts.pos.hidden, false);
  assert.equal(tab.sbParts.indent.hidden, false);
  assert.ok(p.st.editors.length === 1);
});

test("Source gives the body to the editor at the place it was left; Preview lets go of its redraw and its subscription, and comes back at its own place", async () => {
  const { p, tab } = await md();
  assert.ok(tab.md.sub, "Preview follows the buffer");
  pane(tab).scrollTop = 120;
  tab.model.type("# edited\n");
  assert.equal(debounce(p).length, 1);
  const first = pane(tab);
  tab.seg.code.click();
  assert.equal(tab.previewing, false);
  assert.equal(tab.md.pane, null);
  assert.equal(tab.md.top, 120, "where Preview was read is kept");
  assert.equal(debounce(p).length, 0, "the pending redraw went with it");
  assert.equal(tab.md.sub, null, "so did the subscription to the buffer");
  assert.equal(p.st.editors.length, 1, "the editor is made now");
  assert.equal(tab.bodyEl.children[0], p.get("dockEditorEl"), "and has the body");
  assert.equal(p.st.editors[0].getModel(), tab.model);
  assert.deepEqual(pressed(tab), ["code"]);
  assert.equal(tab.md.view, "source");
  assert.equal(p.st.editors[0].focused, 1, "a press on Source gives the editor the keyboard");
  tab.model.type("# edited more\n");
  assert.equal(debounce(p).length, 0, "typing in Source costs the preview nothing");
  tab.seg.preview.click();
  assert.equal(tab.previewing, true);
  assert.notEqual(pane(tab), first, "a new pane");
  assert.equal(pane(tab).scrollTop, 120, "read where it was left");
  assert.equal(shown(tab), "# edited more\n", "from the buffer as it is now");
  assert.ok(tab.md.sub, "following it again");
  assert.deepEqual(pressed(tab), ["preview"]);
  assert.equal(p.get("dockEditorEl").parentNode === tab.bodyEl, false, "the editor is out of the body");
  assert.equal(p.doc.activeElement, pane(tab), "a press on Preview gives the pane the keyboard");
});

test("a press on the view already shown changes nothing", async () => {
  const { p, tab } = await md();
  const n = p.st.renders.length;
  const first = pane(tab);
  tab.seg.preview.click();
  assert.equal(pane(tab), first);
  assert.equal(p.st.renders.length, n);
  tab.seg.code.click();
  const ed = p.st.editors[0];
  const focused = ed.focused;
  ed.top = 300;
  tab.seg.code.click();
  assert.equal(tab.bodyEl.children[0], p.get("dockEditorEl"));
  assert.equal(ed.top, 300, "the editor was not claimed again (its saved view would put the scroll back)");
  assert.equal(ed.focused, focused + 1, "it only gets the keyboard");
});

test("the editor comes back in Source with the view it had: cursor and scroll are kept across Preview", async () => {
  const { p, tab } = await md();
  tab.seg.code.click();
  const ed = p.st.editors[0];
  ed.top = 240;
  tab.seg.preview.click();
  assert.equal(tab.viewState.top, 240, "stashed when Preview took the body");
  ed.top = 0; // a detached editor knows no place
  tab.seg.code.click();
  assert.equal(ed.top, 240, "put back");
});

test("Changes from Preview takes the body and drops the pane; leaving Changes returns to the view the tab had", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: DOC, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = { state: "ok", content: "# Old\n" };
  p.setShown(true);
  const tab = await p.open("/p/README.md");
  await p.settle();
  pane(tab).scrollTop = 77;
  tab.seg.changes.click();
  assert.equal(p.st.diffs.length, 1, "the diff editor");
  assert.ok(tab.diff);
  assert.equal(tab.md.pane, null, "Preview's pane is gone, not hidden under the diff");
  assert.equal(tab.md.sub, null, "and its subscription");
  assert.deepEqual(pressed(tab), ["changes"]);
  assert.equal(tab.md.top, 77);
  tab.seg.preview.click();
  assert.equal(tab.diff, null);
  assert.equal(p.st.diffs[0].disposed, true, "the diff editor is let go");
  assert.equal(tab.previewing, true);
  assert.equal(tab.bodyEl.children[0], pane(tab));
  assert.equal(pane(tab).scrollTop, 77);
  assert.deepEqual(pressed(tab), ["preview"]);
  tab.seg.changes.click();
  tab.seg.code.click();
  assert.equal(tab.diff, null);
  assert.equal(tab.previewing, false);
  assert.equal(tab.bodyEl.children[0], p.get("dockEditorEl"), "Source from Changes is the editor");
  assert.deepEqual(pressed(tab), ["code"]);
  tab.seg.changes.click();
  assert.ok(tab.diff, "Changes is up again");
  tab.showChanges(false);
  assert.equal(tab.previewing, false, "and leaving Changes by the API returns to Source: that is the view the tab had");
});

test("a tab opened in Changes (view: changes) shows Preview behind it, and its Preview comes back when Changes is left", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: DOC, version: "v1" } });
  p.st.git.on = true;
  p.st.git.answer = { state: "ok", content: "# Old\n" };
  p.setShown(true);
  const tab = await p.open("/p/README.md", { view: "changes" });
  await p.settle();
  assert.ok(tab.diff, "Changes is up");
  assert.equal(tab.md.pane, null);
  tab.showChanges(false);
  assert.equal(tab.previewing, true);
});

test("a tab that is hidden keeps the place it was read at and runs nothing; shown again it draws the buffer only if it changed", async () => {
  const { p, tab } = await md();
  pane(tab).scrollTop = 200;
  tab.model.type("# pending\n");
  assert.equal(debounce(p).length, 1);
  tab.onHide();
  assert.equal(tab.md.live, false);
  assert.equal(tab.md.top, 200);
  assert.equal(tab.scroll, 200, "the dock stores where Preview was read");
  assert.equal(debounce(p).length, 0, "the pending redraw is gone");
  assert.equal(tab.md.sub, null, "and the subscription");
  const n = p.st.renders.length;
  tab.model.type("# while hidden\n");
  assert.equal(debounce(p).length, 0, "a hidden tab schedules nothing");
  assert.equal(p.st.renders.length, n);
  pane(tab).scrollTop = 0; // what leaving the document does to a scroller
  tab.onAttach();
  assert.equal(p.st.renders.length, n + 1, "it changed meanwhile: drawn again");
  assert.equal(shown(tab), "# while hidden\n");
  assert.equal(pane(tab).scrollTop, 200, "at the place it was left");
  assert.ok(tab.md.sub, "following the buffer again");
  tab.onHide();
  tab.onAttach();
  assert.equal(p.st.renders.length, n + 1, "it did not change: not drawn again");
});

test("while Preview is up the tab's scroll is the pane's, and Source's is the editor's", async () => {
  const { p, tab } = await md();
  pane(tab).scrollTop = 150;
  assert.equal(tab.scroll, 150);
  assert.equal(tab.fileScroll, 150);
  tab.seg.code.click();
  p.st.editors[0].top = 40;
  assert.equal(tab.scroll, 40);
});

test("a file replaced under Preview is drawn again: the agent's text on a clean buffer, and Discard", async () => {
  const { p, tab, disk } = await md("# one\n", { serve: true });
  const n = p.st.renders.length;
  disk.text = "# agent\n";
  await p.tick();
  assert.equal(tab.model.value, "# agent\n", "the clean buffer reloaded in place");
  assert.equal(tab.dirty(), false);
  assert.equal(debounce(p).length, 1, "and the preview follows in 300 ms");
  debounce(p)[0].fn();
  assert.equal(p.st.renders.length, n + 1);
  assert.equal(shown(tab), "# agent\n");
  tab.model.type("# mine\n");
  debounce(p).at(-1).fn();
  assert.equal(shown(tab), "# mine\n");
  tab.discardEdits();
  assert.equal(tab.dirty(), false);
  debounce(p).at(-1).fn();
  assert.equal(shown(tab), "# agent\n", "Discard puts the saved text back, and the preview follows");
});

test("the conflict bar works under Preview: a dirty buffer whose file changed on disk gets the bar, and Preview keeps showing the buffer", async () => {
  const { p, tab, disk } = await md("# one\n", { serve: true });
  tab.model.type("# mine\n");
  debounce(p).at(-1).fn();
  assert.equal(tab.barEl.hidden, true);
  disk.text = "# agent\n";
  await p.tick();
  assert.equal(tab.barEl.hidden, false, "the amber bar is up over the preview");
  assert.equal(tab.previewing, true);
  assert.equal(shown(tab), "# mine\n", "the buffer is what is shown: nothing was replaced");
  assert.equal(tab.model.value, "# mine\n");
  tab.el.listeners.keydown(keydown());
  await p.settle();
  assert.equal(tab.barEl.hidden, false, "a Mod+S meanwhile gets the 409 again: the bar stays");
  assert.equal(tab.model.value, "# mine\n");
});

test("a line a link or Follow asked for is kept while Preview is up, and shown in Source", async () => {
  const { p, tab } = await md();
  tab.revealLine(3);
  assert.deepEqual(tab.pendingLine, { line: 3, col: undefined });
  assert.equal(tab.previewing, true, "Preview is not left for it: Follow reveals on every write of the agent");
  assert.equal(p.st.editors.length, 0);
  tab.seg.code.click();
  assert.equal(tab.pendingLine, null);
  assert.deepEqual(p.st.editors[0].centered, [3]);
  assert.equal(tab.previewing, false);
});

test("the editor's selection is not the tab's while Preview is up: the bar has none and the insert names the file", async () => {
  const { p, tab } = await md();
  tab.seg.code.click();
  p.st.editors[0].select(1, 1, 2, 3);
  const sel = p.get("editorTabSelection");
  assert.deepEqual(sel(tab), { a: 1, b: 2 });
  tab.seg.preview.click();
  assert.equal(sel(tab), null, "the selection is the detached editor's");
  const ref = p.get("editorRefFor")(tab);
  assert.equal(ref.ref, "/p/README.md");
  assert.equal(ref.ranged, false);
});

test("a tab asked for the keyboard while it was being read gives it to the pane when Preview is shown", async () => {
  const p = page();
  p.setRead({ ok: true, status: 200, data: { kind: "text", content: DOC, version: "v1" } });
  const tab = new p.EditorTab("/p/README.md", { host: p.host });
  tab.focusReader();
  assert.equal(tab.wantFocus, true);
  await p.settle();
  tab.onAttach();
  assert.equal(tab.wantFocus, false);
  assert.equal(p.doc.activeElement, pane(tab));
});

test("the ⋯ menu's font size steps the markdown size while Preview is up and the code size in Source", async () => {
  const sets = [];
  const sizes = { code: 12.5, markdown: 14 };
  const fontPrefs = { get: (s) => sizes[s], STEP: 1, BOUNDS: { min: 8, max: 32 }, set: (s, v) => { sets.push([s, v]); sizes[s] = v; } };
  const { tab } = await md(DOC, { over: { fontPrefs } });
  const bigger = (row) => row.children.find((c) => c.getAttribute && c.getAttribute("aria-label") === "Bigger text");
  const row = tab.fontRow();
  assert.equal(row.children[2].textContent, "14", "the size shown is the markdown one");
  bigger(row).click();
  assert.deepEqual(sets, [["markdown", 15]]);
  tab.seg.code.click();
  const row2 = tab.fontRow();
  assert.equal(row2.children[2].textContent, "12.5");
  bigger(row2).click();
  assert.deepEqual(sets, [["markdown", 15], ["code", 13.5]]);
});

test("a render that fails or has no libraries to run on leaves a sentence in the pane, not a blank one", async () => {
  const { tab } = await md(DOC, { over: { mdRenderFragment: () => { throw new Error("no"); } } });
  assert.equal(pane(tab).children[0].className, "viewer-msg");
  assert.match(pane(tab).children[0].textContent, /could not be drawn/);
  const none = await md(DOC, { over: { DOMPurify: undefined } });
  assert.match(pane(none.tab).children[0].textContent, /needs marked and DOMPurify/);
});

test("closing a tab in Preview lets go of its pane, its redraw and its subscription", async () => {
  const { p, tab } = await md();
  tab.model.type("# x\n");
  assert.equal(debounce(p).length, 1);
  const model = tab.model;
  tab.dispose();
  assert.equal(debounce(p).length, 0);
  assert.equal(tab.md.pane, null);
  assert.equal(model.listeners.length, 0, "no one is left listening to the model (it is disposed)");
  assert.equal(model.disposed, true);
});

// The look of a table in the document is the stylesheet's (a node test cannot lay it out; the shots in the fix wave's report do): a
// table wider than the column scrolls inside itself, in the dock's Preview and on the phone's viewer, which share `.md-body`.
test("a table wider than the column scrolls inside itself instead of widening the page", () => {
  const css = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = /\.md-body table\s*\{([^}]*)\}/.exec(css);
  assert.ok(rule, "a rule for the document's tables");
  assert.match(rule[1], /display:\s*block/, "the table is its own box, which can scroll");
  assert.match(rule[1], /max-width:\s*100%/, "no wider than the column");
  assert.match(rule[1], /overflow-x:\s*auto/, "and scrolls sideways when its cells need more");
  assert.match(rule[1], /width:\s*max-content/, "a narrow table is as wide as its cells, not the column");
});
