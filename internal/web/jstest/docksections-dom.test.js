// docksections.js against a fake explorer column (fakedom): the chevrons and the header collapse and open each section and
// remember it, focus never falls to the page when the part holding it hides, and the divider's keys, double-click and drag
// set the share. The CSS that turns this into a layout is dock-sections-css.test.js's; the look is the visual check's.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { EvNode } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const KEY = "agent-box:dockSections";

// TNode: the fake element plus what the glue uses that fakedom leaves out: closest() by tag or class, and style's custom
// property calls.
class TNode extends EvNode {
  constructor(tag, doc) {
    super(tag, doc);
    const props = {};
    this.style = { props, setProperty: (k, v) => { props[k] = String(v); }, removeProperty: (k) => { delete props[k]; } };
  }
  closest(sel) {
    for (let x = this; x; x = x.parent) if (sel[0] === "." ? x.classList.contains(sel.slice(1)) : x.tag === sel) return x;
    return null;
  }
}

function page(stored) {
  const doc = { activeElement: null, hidden: false, listeners: {} };
  doc.createElement = (t) => new TNode(t, doc);
  doc.addEventListener = (ev, fn) => { (doc.listeners[ev] = doc.listeners[ev] || []).push(fn); };
  const ids = {};
  const mk = (tag, id, cls, parent) => {
    const n = new TNode(tag, doc);
    if (id) { n.id = id; ids[id] = n; }
    if (cls) n.className = cls;
    if (parent) parent.appendChild(n);
    return n;
  };
  const tree = mk("div", "drawer-tree", "dr-tree");
  const sec = mk("section", "dock-changes", "", tree);
  const head = mk("div", "", "dk-chg-head", sec);
  const chgChev = mk("button", "dock-chg-chev", "dk-chev", head);
  const title = mk("span", "", "dk-chg-title", head);
  const review = mk("button", "dock-review", "link dk-review", head);
  const list = mk("div", "dock-chg-list", "dk-list", sec);
  const row = mk("button", "", "dk-row", list);
  const split = mk("div", "dock-secsplit", "dk-secsplit", tree);
  const roothead = mk("div", "", "dk-roothead", tree);
  const treeChev = mk("button", "dock-tree-chev", "dk-chev", roothead);
  const root = mk("button", "dock-root", "dk-root", roothead);
  const pane = mk("div", "files-pane", "", tree);
  const ftNode = mk("div", "", "ft-node", pane);
  for (const c of [chgChev, treeChev]) c.setAttribute("aria-expanded", "true");
  // Layout (px): the column is 800 tall from y=100; the section starts 40 below it and is 300 tall, its list 210 of that;
  // a Changes row and a tree row are 22, the root header 28. So the bounds are 156..652 px (min 90 + 3×22; max 800 − 40 −
  // (28 + 3×22 + 14)).
  tree.rect = { left: 0, top: 100, width: 300, height: 800 };
  sec.rect = { left: 0, top: 140, width: 300, height: 300 };
  list.rect = { left: 0, top: 230, width: 300, height: 210 };
  row.rect = { left: 0, top: 230, width: 300, height: 22 };
  roothead.rect = { left: 0, top: 440, width: 300, height: 28 };
  ftNode.rect = { left: 0, top: 474, width: 300, height: 22 };
  doc.getElementById = (id) => ids[id] || null;

  const store = new Map(stored === undefined ? [] : [[KEY, stored]]);
  const st = { sets: 0 };
  const ctx = vm.createContext({
    console, Math, Number, JSON, Object, Array, String,
    document: doc,
    localStorage: { getItem: (k) => (store.has(k) ? store.get(k) : null), setItem: (k, v) => { st.sets++; store.set(k, v); } },
  });
  new vm.Script(fs.readFileSync(path.join(STATIC, "docksections.js"), "utf8"), { filename: "docksections.js" }).runInContext(ctx);
  const saved = () => (store.has(KEY) ? JSON.parse(store.get(KEY)) : null);
  return { ctx, doc, st, saved, tree, sec, head, chgChev, title, review, list, row, split, roothead, treeChev, root, pane, ftNode };
}

const ev = (type, o) => Object.assign({ type, button: 0, pointerId: 1, bubbles: true }, o || {});

test("nothing stored: both sections open, no share, chevrons say so", () => {
  const p = page();
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), null);
  assert.strictEqual(p.tree.getAttribute("data-tree-shut"), null);
  assert.strictEqual(p.chgChev.getAttribute("aria-expanded"), "true");
  assert.strictEqual(p.treeChev.getAttribute("aria-expanded"), "true");
  assert.strictEqual(p.chgChev.title, "Collapse Changes");
  assert.strictEqual(p.treeChev.title, "Collapse the folder tree");
  assert.ok(!("--dk-chg-max" in p.tree.style.props));
});

test("a stored state is painted on load: Changes shut, a 30% share", () => {
  const p = page(JSON.stringify({ changes: false, tree: true, share: 0.3 }));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), "1");
  assert.strictEqual(p.chgChev.getAttribute("aria-expanded"), "false");
  assert.strictEqual(p.chgChev.title, "Expand Changes");
  assert.strictEqual(p.tree.style.props["--dk-chg-max"], "30%");
});

test("a broken stored value loads as the defaults", () => {
  const p = page("{not json");
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), null);
  assert.strictEqual(p.tree.getAttribute("data-tree-shut"), null);
});

test("the chevrons toggle their own section and remember it", () => {
  const p = page();
  p.chgChev.dispatchEvent(ev("click"));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), "1");
  assert.deepStrictEqual(p.saved(), { changes: false, tree: true, share: null });
  p.treeChev.dispatchEvent(ev("click"));
  assert.strictEqual(p.tree.getAttribute("data-tree-shut"), "1");
  assert.strictEqual(p.treeChev.getAttribute("aria-expanded"), "false");
  assert.strictEqual(p.treeChev.title, "Expand the folder tree");
  assert.deepStrictEqual(p.saved(), { changes: false, tree: false, share: null });
  p.chgChev.dispatchEvent(ev("click"));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), null);
  assert.strictEqual(p.chgChev.getAttribute("aria-expanded"), "true");
});

test("the header's free space toggles Changes; Review does not", () => {
  const p = page();
  p.title.dispatchEvent(ev("click"));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), "1");
  p.head.dispatchEvent(ev("click", { bubbles: false }));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), null);
  p.review.dispatchEvent(ev("click"));
  assert.strictEqual(p.tree.getAttribute("data-chg-shut"), null, "Review is Review");
});

test("focus in a part that hides goes to that section's chevron; focus elsewhere stays", () => {
  const p = page();
  p.row.focus();
  p.ctx.dockSecSet("changes", false);
  assert.strictEqual(p.doc.activeElement, p.chgChev);
  p.ftNode.focus();
  p.ctx.dockSecSet("tree", false);
  assert.strictEqual(p.doc.activeElement, p.treeChev);
  p.ctx.dockSecSet("changes", true);
  p.review.focus();
  p.ctx.dockSecSet("changes", false);
  assert.strictEqual(p.doc.activeElement, p.review, "the header stays on screen, so Review keeps focus");
});

test("dockSecOpen opens a shut section and writes nothing for an open one", () => {
  const p = page(JSON.stringify({ changes: true, tree: false, share: null }));
  p.ctx.dockSecOpen("changes");
  assert.strictEqual(p.st.sets, 0);
  p.ctx.dockSecOpen("tree");
  assert.strictEqual(p.tree.getAttribute("data-tree-shut"), null);
  assert.strictEqual(p.st.sets, 1);
});

test("divider keys: ArrowDown/ArrowUp step 2% of the column from what is drawn, Home/End go to the bounds", () => {
  const p = page();
  p.split.dispatchEvent(ev("keydown", { key: "ArrowDown" }));
  // drawn 300 px of 800 → 316 px → 0.395
  assert.strictEqual(p.saved().share, 0.395);
  assert.strictEqual(p.tree.style.props["--dk-chg-max"], "39.5%");
  p.split.dispatchEvent(ev("keydown", { key: "Home" }));
  assert.strictEqual(p.saved().share, 0.195);
  p.split.dispatchEvent(ev("keydown", { key: "End" }));
  assert.strictEqual(p.saved().share, 0.815);
  const before = p.st.sets;
  const e = ev("keydown", { key: "Tab" });
  p.split.dispatchEvent(e);
  assert.strictEqual(p.st.sets, before, "other keys are not taken");
  assert.ok(!e.defaultPrevented);
});

test("divider aria: whole percents of the column, from what is drawn and the bounds", () => {
  const p = page();
  p.split.dispatchEvent(ev("focus"));
  assert.strictEqual(p.split.getAttribute("aria-valuenow"), "38");
  assert.strictEqual(p.split.getAttribute("aria-valuemin"), "20");
  assert.strictEqual(p.split.getAttribute("aria-valuemax"), "82");
});

test("double-click resets to today's rule: no share, no variable", () => {
  const p = page(JSON.stringify({ changes: true, tree: true, share: 0.3 }));
  p.split.dispatchEvent(ev("dblclick"));
  assert.strictEqual(p.saved().share, null);
  assert.ok(!("--dk-chg-max" in p.tree.style.props));
});

test("a drag draws live and saves once, on release; a press that is not the main button does nothing", () => {
  const p = page();
  p.split.dispatchEvent(ev("pointerdown", { button: 2, clientY: 440 }));
  assert.ok(!p.split.classList.contains("dragging"));
  p.split.dispatchEvent(ev("pointerdown", { clientY: 440 }));
  assert.ok(p.split.classList.contains("dragging"));
  p.split.dispatchEvent(ev("pointermove", { clientY: 380 })); // 240 px below the section's top → 0.3
  assert.strictEqual(p.tree.style.props["--dk-chg-max"], "30%");
  p.split.dispatchEvent(ev("pointermove", { clientY: 150 })); // held at the minimum, 156 px
  assert.strictEqual(p.tree.style.props["--dk-chg-max"], "19.5%");
  assert.strictEqual(p.st.sets, 0, "nothing saved mid-drag");
  p.split.dispatchEvent(ev("pointerup"));
  assert.ok(!p.split.classList.contains("dragging"));
  assert.strictEqual(p.st.sets, 1);
  assert.strictEqual(p.saved().share, 0.195);
  p.split.dispatchEvent(ev("pointermove", { clientY: 600 }));
  assert.strictEqual(p.tree.style.props["--dk-chg-max"], "19.5%", "moves after the release do nothing");
});

test("a lost capture ends the drag (window blur), keeping what was drawn", () => {
  const p = page();
  p.split.dispatchEvent(ev("pointerdown", { clientY: 440 }));
  p.split.dispatchEvent(ev("pointermove", { clientY: 380 }));
  p.split.dispatchEvent(ev("lostpointercapture"));
  assert.ok(!p.split.classList.contains("dragging"));
  assert.strictEqual(p.saved().share, 0.3);
});

test("review 1: collapsing the tree puts the root field away, so the next root press opens it again", () => {
  const p = page();
  p.tree.dataset.rootedit = "1";
  const calls = [];
  p.ctx.dkcRootEdit = (on) => { calls.push(on); if (!on) delete p.tree.dataset.rootedit; };
  p.ctx.dockSecSet("tree", false);
  assert.deepStrictEqual(calls, [false]);
  assert.strictEqual(p.tree.dataset.rootedit, undefined);
  p.ctx.dockSecSet("tree", true);
  p.ctx.dockSecSet("changes", false);
  assert.deepStrictEqual(calls, [false], "only the tree's collapse, and only with the field out");
});

test("review 1: the separator has its aria values from the first paint, not only after a focus", () => {
  const p = page();
  assert.strictEqual(p.split.getAttribute("aria-valuenow"), "38");
});

test("review 1: a drag move writes without measuring the column again (one layout read at the press)", () => {
  const p = page();
  p.split.dispatchEvent(ev("pointerdown", { clientY: 440 }));
  let reads = 0;
  for (const n of [p.tree, p.sec, p.list, p.row, p.roothead, p.ftNode]) {
    const r = n.rect;
    n.getBoundingClientRect = () => { reads++; return r; };
  }
  p.split.dispatchEvent(ev("pointermove", { clientY: 380 }));
  p.split.dispatchEvent(ev("pointermove", { clientY: 400 }));
  assert.strictEqual(reads, 0);
  assert.strictEqual(p.split.getAttribute("aria-valuenow"), "33", "aria from the share and the press-time bounds");
  p.split.dispatchEvent(ev("pointerup"));
});
