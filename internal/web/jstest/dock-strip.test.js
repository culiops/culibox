"use strict";
// dockstrip.js, the dock's tab strip and crumb row, drawn into a fake page (fakedom.js). The model behind it (what a tab
// is, what the label says) is docktabs.test.js's and dockcrumbs.test.js's; filesdrawer.js decides what to draw
// (dock-tabs-dom.test.js). This file proves what the elements show and what a click, a double-click, a middle-click and a key
// do. The look is the stylesheet's (dock-tabs-css.test.js pins it; the visual check shows it).
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");

function page(mac) {
  const resizes = []; // the callbacks of the ResizeObservers the script made, and what each watches
  class FakeResizeObserver {
    constructor(fn) { this.fn = fn; }
    observe(el) { resizes.push({ fn: this.fn, el }); }
  }
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  const nodes = {};
  for (const id of ["dock-bar", "dock-tabs", "dock-crumbs", "dock-trail", "dock-edited", "dock-outside"]) nodes[id] = Object.assign(new Node("div", doc), { id });
  // The hint's markup is static (index.html): its Reveal button is there from the start.
  nodes["dock-outside"].hidden = true;
  const go = new Node("button", doc);
  go.className = "link dk-outside-go";
  nodes["dock-outside"].appendChild(go);
  doc.getElementById = (id) => nodes[id] || null;
  // Focus is document.activeElement in the fake; a real focus() also fires the element's focus handler.
  const focus = Node.prototype.focus;
  Node.prototype.focus = function () { focus.call(this); if (this.onfocus) this.onfocus(); };
  const ctx = vm.createContext({ console, Array, Object, Set, Map, Math, document: doc, iconFor: (n) => "<" + n + ">", IS_MAC: !!mac, ResizeObserver: FakeResizeObserver });
  const load = (code, name) => new vm.Script(code, { filename: name }).runInContext(ctx);
  load(fs.readFileSync(path.join(STATIC, "docktabs.js"), "utf8"), "docktabs.js");
  load(fs.readFileSync(path.join(STATIC, "dockstrip.js"), "utf8"), "dockstrip.js");
  const calls = [];
  const on = {
    activate: (k) => calls.push(["activate", k]),
    pin: (k) => calls.push(["pin", k]),
    close: (k) => calls.push(["close", k]),
  };
  const item = (p, o) => Object.assign({ key: "v:" + p, path: p, box: null, name: p.split("/").pop(), title: p, letter: "", preview: false, dirty: false, active: false }, o);
  return {
    doc, nodes, calls, on, item, ctx, resizes, restore: () => { Node.prototype.focus = focus; },
    paint: (items) => ctx.dockStripPaint(items, on),
    tabs: () => nodes["dock-tabs"].children,
    tab: (i) => nodes["dock-tabs"].children[i],
    part: (el, cls) => el.querySelector("." + cls),
  };
}
const ev = (over) => Object.assign({ stopped: false, prevented: false, stopPropagation() { this.stopped = true; }, preventDefault() { this.prevented = true; } }, over);

test("a tab shows its name as text, with the preview, active and dirty marks and the git letter", () => {
  const p = page();
  try {
    p.paint([
      p.item("/p/a.go", { letter: "M", active: true }),
      p.item("/p/b.go", { preview: true }),
      p.item("/p/c.go", { dirty: true }),
    ]);
    const [a, b, c] = [p.tab(0), p.tab(1), p.tab(2)];
    assert.equal(p.tabs().length, 3);
    assert.equal(p.part(a, "dk-name").textContent, "a.go");
    assert.equal(a.className, "dk-tab dk-tab-on");
    assert.equal(a.getAttribute("aria-selected"), "true");
    assert.equal(a.getAttribute("role"), "tab");
    assert.equal(a.title, "/p/a.go");
    assert.equal(p.part(a, "dk-st").textContent, "M");
    assert.equal(p.part(a, "dk-st").className, "dk-st dk-st-m");
    assert.equal(p.part(a, "dk-st").title, "Modified");
    assert.equal(p.part(a, "dk-ic").textContent, "<a.go>");
    assert.equal(b.className, "dk-tab dk-prev", "the preview tab is the italic one");
    assert.equal(b.getAttribute("aria-selected"), "false");
    assert.equal(p.part(b, "dk-st").textContent, "");
    assert.equal(a.dataset.dirty, "");
    assert.equal(c.dataset.dirty, "1", "the dot and the close button are CSS's, off this");
    assert.ok(p.part(c, "dk-dirty"), "the dot is there");
    assert.equal(p.part(c, "dk-dirty").getAttribute("aria-label"), "Unsaved changes");
    assert.equal(p.part(a, "dk-x").getAttribute("aria-label"), "Close a.go");
    assert.equal(p.nodes["dock-bar"].hidden, false);
  } finally { p.restore(); }
});

test("a read-only tab carries the lock mark, a writable one does not", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a.go", { locked: true }), p.item("/p/b.go")]);
    assert.equal(p.tab(0).dataset.locked, "1", "the lock is CSS's, off this");
    assert.equal(p.tab(1).dataset.locked, "");
    const lock = p.part(p.tab(0), "dk-lock");
    assert.ok(lock, "the lock is there");
    assert.equal(lock.getAttribute("aria-label"), "Read-only");
    assert.equal(lock.title, "Read-only");
    p.paint([p.item("/p/a.go"), p.item("/p/b.go")]);
    assert.equal(p.tab(0).dataset.locked, "", "and a repaint takes it off");
  } finally { p.restore(); }
});

test("names, titles and paths are written as text: a hostile file name makes no element", () => {
  const p = page();
  try {
    const evil = '<img src=x onerror="alert(1)">.md';
    p.paint([p.item("/p/" + evil, { name: evil, title: "/p/" + evil })]);
    const t = p.tab(0);
    assert.equal(p.part(t, "dk-name").textContent, evil);
    assert.equal(p.part(t, "dk-name").children.length, 0, "no element under the name");
    assert.equal(t.querySelector("img"), null);
    assert.equal(t.title, "/p/" + evil);
    p.ctx.dockCrumbsPaint([{ name: evil, path: "/p/" + evil }], '<b>Edited by</b>', { reveal() {} });
    assert.equal(p.nodes["dock-trail"].children[0].children.length, 0);
    assert.equal(p.nodes["dock-edited"].textContent, "<b>Edited by</b>");
    assert.equal(p.nodes["dock-edited"].children.length, 0);
  } finally { p.restore(); }
});

test("no tab hides the strip", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a", { active: true })]);
    assert.equal(p.nodes["dock-bar"].hidden, false);
    p.paint([]);
    assert.equal(p.nodes["dock-bar"].hidden, true);
    assert.equal(p.tabs().length, 0);
  } finally { p.restore(); }
});

test("a repaint keeps the tab elements it has: a double-click needs the element its first click landed on", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a", { active: true }), p.item("/p/b", { preview: true })]);
    const [a, b] = [p.tab(0), p.tab(1)];
    p.paint([p.item("/p/a"), p.item("/p/b", { preview: false, active: true }), p.item("/p/c", { preview: true })]);
    assert.equal(p.tab(0), a);
    assert.equal(p.tab(1), b);
    assert.equal(p.tab(2).dataset.key, "v:/p/c");
    assert.equal(b.className, "dk-tab dk-tab-on", "updated in place");
    assert.equal(a.className, "dk-tab");
    p.paint([p.item("/p/c", { preview: true }), p.item("/p/a", { active: true })]);
    assert.equal(p.tabs().length, 2);
    assert.equal(p.tab(1), a, "a moved, it was not rebuilt");
    assert.equal(b.parent, null, "a closed tab's element is gone");
  } finally { p.restore(); }
});

test("a repaint that changes nothing touches no child list (focus on a tab survives a repaint)", () => {
  const p = page();
  try {
    const items = () => [p.item("/p/a", { active: true }), p.item("/p/b")];
    p.paint(items());
    let moved = 0;
    const host = p.nodes["dock-tabs"];
    const rc = host.replaceChildren.bind(host);
    host.replaceChildren = (...a) => { moved++; return rc(...a); };
    p.tab(1).focus();
    p.paint(items());
    p.paint(items().map((i) => Object.assign(i, { dirty: true })));
    assert.equal(moved, 0);
    assert.equal(p.doc.activeElement, p.tab(1));
  } finally { p.restore(); }
});

test("a click shows the tab, a double-click pins it, the cross and a middle-click close it", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a", { active: true }), p.item("/p/b", { preview: true })]);
    const b = p.tab(1);
    b.onclick(ev());
    assert.deepEqual(p.calls.pop(), ["activate", "v:/p/b"]);
    b.ondblclick(ev());
    assert.deepEqual(p.calls.pop(), ["pin", "v:/p/b"]);
    const x = p.part(b, "dk-x");
    const e = ev();
    x.onclick(e);
    assert.deepEqual(p.calls.pop(), ["close", "v:/p/b"]);
    assert.equal(e.stopped, true, "the cross does not also show the tab it closes");
    const mid = ev({ button: 1 });
    b.onauxclick(mid);
    assert.deepEqual(p.calls.pop(), ["close", "v:/p/b"]);
    assert.equal(mid.prevented, true);
    const down = ev({ button: 1 });
    b.onmousedown(down);
    assert.equal(down.prevented, true, "no autoscroll");
    const right = ev({ button: 2 });
    b.onauxclick(right);
    b.onmousedown(ev({ button: 0 }));
    assert.deepEqual(p.calls, [], "another button closes nothing");
  } finally { p.restore(); }
});

test("a tab painted for one key answers to its key after a repaint with other callbacks", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a")]);
    const got = [];
    p.ctx.dockStripPaint([p.item("/p/a")], { activate: (k) => got.push(k) });
    p.tab(0).onclick(ev());
    assert.deepEqual(got, ["v:/p/a"]);
    assert.deepEqual(p.calls, []);
  } finally { p.restore(); }
});

test("the keyboard: arrows, Home and End move focus along the tabs, Enter shows, Delete closes, a chord is left alone", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a", { active: true }), p.item("/p/b"), p.item("/p/c")]);
    const key = (k, o) => ev(Object.assign({ key: k }, o));
    const press = (el, k, o) => { const e = key(k, o); el.onkeydown(e); return e; };
    p.tab(0).focus();
    assert.equal(press(p.tab(0), "ArrowRight").prevented, true);
    assert.equal(p.doc.activeElement, p.tab(1));
    press(p.tab(1), "End");
    assert.equal(p.doc.activeElement, p.tab(2));
    press(p.tab(2), "ArrowRight");
    assert.equal(p.doc.activeElement, p.tab(2), "no wrap");
    press(p.tab(2), "Home");
    assert.equal(p.doc.activeElement, p.tab(0));
    press(p.tab(0), "ArrowLeft");
    assert.equal(p.doc.activeElement, p.tab(0));
    press(p.tab(1), "Enter");
    press(p.tab(1), " ");
    press(p.tab(2), "Delete");
    assert.deepEqual(p.calls, [["activate", "v:/p/b"], ["activate", "v:/p/b"], ["close", "v:/p/c"]]);
    p.calls.length = 0;
    for (const o of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { shiftKey: true }]) {
      assert.equal(press(p.tab(1), "Delete", o).prevented, false);
      assert.equal(press(p.tab(1), "ArrowRight", o).prevented, false);
    }
    assert.deepEqual(p.calls, [], "a chord belongs to the app (shortcuts.js)");
    assert.equal(press(p.tab(1), "a").prevented, false);
  } finally { p.restore(); }
});

test("on a Mac the Delete key is Backspace: a focused tab closes on either there, and only on Delete elsewhere", () => {
  const press = (p, i, k, o) => { const e = ev(Object.assign({ key: k }, o)); p.tab(i).onkeydown(e); return e; };
  const mac = page(true);
  const other = page(false);
  try {
    for (const p of [mac, other]) p.paint([p.item("/p/a", { active: true }), p.item("/p/b")]);
    assert.equal(press(mac, 1, "Backspace").prevented, true, "the page does not navigate back");
    assert.equal(press(mac, 0, "Delete").prevented, true);
    assert.deepEqual(mac.calls, [["close", "v:/p/b"], ["close", "v:/p/a"]]);
    assert.equal(press(other, 1, "Backspace").prevented, false, "elsewhere Backspace is left alone");
    assert.deepEqual(other.calls, []);
    press(other, 1, "Delete");
    assert.deepEqual(other.calls, [["close", "v:/p/b"]]);
    mac.calls.length = 0;
    assert.equal(press(mac, 1, "Backspace", { metaKey: true }).prevented, false, "a chord is still the app's");
    assert.deepEqual(mac.calls, []);
  } finally { mac.restore(); other.restore(); }
});

test("the strip has one Tab stop: the active tab, or the tab the user moved to", () => {
  const p = page();
  try {
    p.paint([p.item("/p/a"), p.item("/p/b", { active: true }), p.item("/p/c")]);
    assert.deepEqual([...p.tabs()].map((t) => t.tabIndex), [-1, 0, -1]);
    p.tab(2).focus();
    assert.deepEqual([...p.tabs()].map((t) => t.tabIndex), [-1, -1, 0], "roving: focus moved the stop");
    p.paint([p.item("/p/a"), p.item("/p/b", { active: true }), p.item("/p/c")]);
    assert.deepEqual([...p.tabs()].map((t) => t.tabIndex), [-1, -1, 0], "a repaint does not take it back");
    p.paint([p.item("/p/a", { active: true }), p.item("/p/b")]);
    assert.deepEqual([...p.tabs()].map((t) => t.tabIndex), [0, -1], "a stop on a tab that is gone falls back to the active one");
    assert.equal(p.part(p.tab(0), "dk-x").tabIndex, -1, "the cross is not a Tab stop: Delete closes");
  } finally { p.restore(); }
});

// --- the crumb row ---------------------------------------------------------------------------------------------------

test("crumbs are buttons that reveal their folder; the last is the file; the label sits on the right", () => {
  const p = page();
  try {
    const got = [];
    p.ctx.dockCrumbsPaint(
      [{ name: "proj", path: "/h/proj" }, { name: "src", path: "/h/proj/src" }, { name: "a.go", path: "/h/proj/src/a.go" }],
      "Edited by alpha · 2 min ago", { reveal: (x) => got.push(x) });
    const trail = p.nodes["dock-trail"];
    assert.deepEqual(trail.children.map((c) => c.tag + ":" + c.className), ["button:dk-crumb", "span:dk-sep", "button:dk-crumb", "span:dk-sep", "button:dk-crumb"]);
    assert.deepEqual(trail.children.filter((c) => c.tag === "button").map((c) => c.textContent), ["proj", "src", "a.go"]);
    assert.equal(trail.children[2].title, "/h/proj/src");
    assert.equal(trail.children[1].getAttribute("aria-hidden"), "true");
    assert.equal(trail.children[4].getAttribute("aria-current"), "page");
    assert.equal(trail.children[0].getAttribute("aria-current"), null);
    trail.children[2].onclick();
    trail.children[4].onclick();
    assert.deepEqual(got, ["/h/proj/src", "/h/proj/src/a.go"]);
    assert.equal(p.nodes["dock-crumbs"].hidden, false);
    assert.equal(p.nodes["dock-edited"].textContent, "Edited by alpha · 2 min ago");
    assert.equal(p.nodes["dock-edited"].hidden, false);
  } finally { p.restore(); }
});

test("a file outside the tree's root shows the hint beside the trail; its Reveal calls on.revealOutside; \"Edited by\" is untouched", () => {
  const p = page();
  try {
    let asked = 0;
    const crumbs = [{ name: "/", path: "/" }, { name: "v", path: "/v" }, { name: "n.md", path: "/v/n.md" }];
    p.ctx.dockCrumbsPaint(crumbs, "Edited by alpha", { reveal() {}, revealOutside: () => { asked++; } }, true);
    const out = p.nodes["dock-outside"];
    assert.equal(out.hidden, false);
    out.querySelector(".dk-outside-go").onclick();
    assert.equal(asked, 1);
    assert.equal(p.nodes["dock-edited"].textContent, "Edited by alpha");
    assert.equal(p.nodes["dock-edited"].hidden, false, "the hint never displaces Edited by");
    p.ctx.dockCrumbsPaint(crumbs, "", { reveal() {} }, false);
    assert.equal(out.hidden, true);
    p.ctx.dockCrumbsPaint(crumbs, "", null);
    assert.equal(out.hidden, true, "no fourth argument: no hint");
  } finally { p.restore(); }
});

test("crumbs without a reveal are text, not buttons (a file inside a box is in no tree)", () => {
  const p = page();
  try {
    p.ctx.dockCrumbsPaint([{ name: "/", path: "/" }, { name: "ws", path: "/ws" }], "", null);
    assert.deepEqual(p.nodes["dock-trail"].children.map((c) => c.tag), ["span", "span", "span"]);
    assert.deepEqual(p.nodes["dock-trail"].children.filter((c) => c.className === "dk-crumb").length, 2);
    assert.equal(p.nodes["dock-edited"].hidden, true, "nothing to say, nothing drawn");
  } finally { p.restore(); }
});

test("the trail is rebuilt only when the path changed, so a focused crumb keeps its focus", () => {
  const p = page();
  try {
    const c1 = [{ name: "proj", path: "/h/proj" }, { name: "a.go", path: "/h/proj/a.go" }];
    const on = { reveal() {} };
    p.ctx.dockCrumbsPaint(c1, "x", on);
    const first = p.nodes["dock-trail"].children[0];
    p.ctx.dockCrumbsPaint(c1.map((c) => ({ ...c })), "y", on);
    assert.equal(p.nodes["dock-trail"].children[0], first);
    assert.equal(p.nodes["dock-edited"].textContent, "y", "the label still follows");
    p.ctx.dockCrumbsPaint([c1[0], { name: "b.go", path: "/h/proj/b.go" }], "y", on);
    assert.notEqual(p.nodes["dock-trail"].children[0], first);
    p.ctx.dockCrumbsPaint([], "", null);
    assert.equal(p.nodes["dock-crumbs"].hidden, true);
  } finally { p.restore(); }
});

test("the strip scrolls to a tab that became active, nudges one left half-cut by a width change, and leaves one scrolled away", () => {
  const p = page();
  try {
    const items = (active) => ["a", "b", "c", "d"].map((n) => p.item("/p/" + n, { active: "v:/p/" + n === active }));
    p.paint(items("v:/p/a"));
    const host = p.nodes["dock-tabs"];
    // Four tabs of 100px in a 250px strip: the fake page has no layout, so the geometry is set by hand.
    [...p.tabs()].forEach((t, i) => { t.offsetLeft = i * 100; t.offsetWidth = 100; });
    host.clientWidth = 250;
    host.scrollLeft = 0;
    p.paint(items("v:/p/d"));
    assert.equal(host.scrollLeft, 150, "d (300..400) is brought into view at the right edge");
    host.scrollLeft = 0;
    p.paint(items("v:/p/d"));
    assert.equal(host.scrollLeft, 0, "scrolled wholly away by the user: a repaint leaves it be");
    host.scrollLeft = 60;
    p.paint(items("v:/p/d"));
    assert.equal(host.scrollLeft, 150, "60..310 shows d (300..400) only partly: nudged in");
    host.scrollLeft = 60;
    p.paint(items("v:/p/a"));
    assert.equal(host.scrollLeft, 0, "a (0..100) was half off the left edge");
  } finally { p.restore(); }
});

test("the strip is watched for its size, and a resize brings the active tab back into view (the toggle and Follow arrive after the first paint)", () => {
  const p = page();
  try {
    assert.equal(p.resizes.length, 1, "one observer, on the tab strip");
    assert.equal(p.resizes[0].el, p.nodes["dock-tabs"]);
    const items = (active) => ["a", "b", "c"].map((n) => p.item("/p/" + n, { active: "v:/p/" + n === active }));
    p.paint(items("v:/p/c"));
    const host = p.nodes["dock-tabs"];
    [...p.tabs()].forEach((t, i) => { t.offsetLeft = i * 100; t.offsetWidth = 100; });
    host.clientWidth = 300; // all three fit: c (200..300) is in view
    host.scrollLeft = 0;
    p.resizes[0].fn();
    assert.equal(host.scrollLeft, 0, "in view: nothing moves");
    host.clientWidth = 150; // the bar's actions grew: c is off the edge
    p.resizes[0].fn();
    assert.equal(host.scrollLeft, 150, "c (200..300) is brought in at the right edge");
    host.clientWidth = 0; // the bar is hidden (no tab, or the dock is closed): no size to scroll by
    host.scrollLeft = 7;
    p.resizes[0].fn();
    assert.equal(host.scrollLeft, 7, "a strip with no width is left alone");
    p.paint([]);
    host.clientWidth = 150;
    p.resizes[0].fn();
    assert.equal(host.scrollLeft, 7, "no active tab: nothing to reveal");
  } finally { p.restore(); }
});
