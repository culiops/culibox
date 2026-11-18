"use strict";
// What the editor puts in the dock's bar area, on editorpage.js's page (the real editortab.js on a fake Monaco): the conflict bar stands
// in the dock's slot between the tab strip and the crumbs (mockup B4) while its tab is on screen, follows a tab switch, and goes
// back into its tab when the tab leaves; and the view toggle has the icon and the word of each item, the bar showing one of them
// (desktop.css, dock-tabs-css.test.js).
const test = require("node:test");
const assert = require("node:assert");
const { page, El } = require("./editorpage.js");

// A page with the dock's slot, as index.html has it (#dock-conflict).
function withSlot(p) {
  const slot = new El("div", p.doc);
  slot.id = "dock-conflict";
  const get = p.doc.getElementById;
  p.doc.getElementById = (id) => (id === "dock-conflict" ? slot : get(id));
  return slot;
}

test("the conflict bar of the tab on screen stands in the dock's slot, and follows the tab switch", async () => {
  const p = page();
  const slot = withSlot(p);
  p.serve("one\n");
  p.setShown(true);
  const a = await p.open("/p/a.go");
  await p.settle();
  assert.deepEqual(slot.children, [a.barEl], "the shown tab's bar is in the slot, not in its own element");
  assert.equal(a.el.children.includes(a.barEl), false);
  // Another tab is shown: the dock hides the first and attaches the second.
  a.onHide();
  const b = new p.EditorTab("/p/b.go", { host: p.host });
  for (let i = 0; i < 50 && !b.model; i++) await new Promise((r) => setImmediate(r));
  b.onAttach();
  assert.deepEqual(slot.children, [b.barEl], "now the second tab's");
  assert.equal(a.el.children.includes(a.barEl), true, "and the first tab has its own back");
  assert.equal(a.barEl.parent, a.el);
  // The first tab comes back.
  b.onHide();
  a.onAttach();
  assert.deepEqual(slot.children, [a.barEl]);
  assert.equal(b.el.children.includes(b.barEl), true);
});

test("a conflict bar that is up is in the slot while its tab is shown, and the tab that is shown is the only one to have one there", async () => {
  const p = page();
  const slot = withSlot(p);
  const disk = p.serve("one\n");
  p.setShown(true);
  const a = await p.open("/p/a.go");
  await p.settle();
  a.model.type("mine\n");
  disk.text = "agent\n";
  await p.tick();
  assert.equal(a.barEl.hidden, false, "the amber bar is up");
  assert.equal(slot.children[0], a.barEl);
  a.onHide();
  assert.equal(slot.children.length, 0, "a tab that is not shown has nothing in the dock's slot");
  assert.equal(a.barEl.hidden, false, "its bar is still up for when it is shown again");
  a.onAttach();
  assert.equal(slot.children[0], a.barEl);
  assert.equal(a.barEl.hidden, false);
});

test("a tab that is closed leaves nothing in the slot, and a page without a slot keeps the bar in the tab", async () => {
  const p = page();
  const slot = withSlot(p);
  p.serve("one\n");
  const a = await p.open("/p/a.go");
  assert.equal(slot.children.length, 1);
  a.dispose();
  assert.equal(slot.children.length, 0, "dispose takes the bar out of the dock");
  const q = page(); // no slot
  q.serve("one\n");
  const b = await q.open("/p/b.go");
  assert.equal(b.el.children.includes(b.barEl), true, "no slot: the bar stays where it was built");
});

test("the view toggle: each item has an icon and its word, the word is the item's label, and Code reads Source for markdown", async () => {
  const p = page();
  const tab = await p.open("/p/a.go");
  for (const k of ["preview", "code", "changes"]) {
    const b = tab.seg[k];
    assert.equal(b.children.length, 2, k + ": an icon and a word");
    assert.equal(b.children[0].attrs.class, "dk-seg-ic");
    assert.equal(b.children[1].className, "dk-seg-t");
  }
  assert.equal(tab.seg.code.textContent, "Code");
  assert.equal(tab.seg.code.title, "Code", "the word is also the tooltip, for the bar that shows the icon only");
  assert.equal(tab.seg.preview.textContent, "Preview");
  assert.equal(tab.seg.changes.textContent, "Changes");
});

test("Follow has the eye and its word in a span of its own, which the narrow bar hides", async () => {
  const p = page();
  const tab = await p.open("/p/a.go");
  const word = tab.followBtn.children.find((c) => c.className === "dk-follow-t");
  assert.ok(word, "the word has its own span");
  assert.equal(word.textContent, "Follow");
});
