"use strict";
// The desktop toast stack on screen (F60 F61 F62 F64, #171): shell.js renders
// toaststack.js's model into #toasts above 700px, the phone keeps its one slot
// (#toast). toaststack.test.js covers the model's rules; this covers the page
// side: the pure entry view, the markup each entry gets, hover/focus pause, the
// one timer, Copy details, pending results, the static wiring (index.html,
// app.css, desktop.css) and the callers that now hand a human sentence and the
// raw text separately. The repo has no jsdom: a minimal fake DOM stands in.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const { createToastStack, toastFailText } = require("../static/toaststack.js");
const { mobileActive } = require("../static/mobileshell.js");

// --- fake DOM ----------------------------------------------------------------
class El {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.style = {}; this.listeners = {};
    this.title = ""; this.type = ""; this.onclick = null; this.focusVisible = false;
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { for (const c of this.children) c.parent = null; this.children = []; this.own = String(v); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
  fire(type, ev) { for (const fn of this.listeners[type] || []) fn(Object.assign({ target: this }, ev || {})); }
  appendChild(c) { return this.insertBefore(c, null); }
  insertBefore(c, ref) {
    if (c.parent) c.remove();
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i < 0) this.children.push(c); else this.children.splice(i, 0, c);
    c.parent = this;
    return c;
  }
  // Chrome blurs a focused node being removed BEFORE it is detached, firing focusout
  // synchronously; if a handler detaches it meanwhile, removeChild throws.
  remove() {
    const p = this.parent;
    if (!p) return;
    if (El.beforeRemove) El.beforeRemove(this);
    if (this.parent !== p) throw new Error("NotFoundError: the node to be removed is no longer a child of this node");
    p.children.splice(p.children.indexOf(this), 1);
    this.parent = null;
  }
  replaceWith(n) { const p = this.parent; if (!p) return; p.insertBefore(n, this); this.remove(); }
  get firstChild() { return this.children[0] || null; }
  get nextSibling() { if (!this.parent) return null; const s = this.parent.children; return s[s.indexOf(this) + 1] || null; }
  contains(n) { for (; n; n = n.parent) if (n === this) return true; return false; }
  matches(sel) { assert.strictEqual(sel, ":focus-visible", "the fake supports :focus-visible only"); return this.focusVisible; }
  find(cls) {
    for (const c of this.children) {
      if (!(c instanceof El)) continue;
      if (c.className.split(/\s+/).includes(cls)) return c;
      const deep = c.find(cls);
      if (deep) return deep;
    }
    return null;
  }
}

// The phone slot (index.html's #toast and its parts), as shell.js's phone path uses it.
function phoneSlot() {
  const classes = new Set();
  const toast = new El("div");
  toast.classList = {
    toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
    add: (c) => classes.add(c),
    remove: (c) => classes.delete(c),
    contains: (c) => classes.has(c),
  };
  return { toast, "toast-msg": new El("span"), "toast-undo": new El("button"), visible: () => classes.has("show") };
}

const GLOBALS = ["document", "window", "mobileActive", "createToastStack", "copyText"];

// A page at `width` with the real shell.js (fresh module state) and toaststack.js.
// `host: false` leaves #toasts out of the page. `blur: false` is Firefox and
// Safari: removing the focused node moves focus to the body and fires no
// blur/focusout at all (Bugzilla 559561); the default is Chrome's behaviour.
function page(t, { width = 1280, host = true, blur = true } = {}) {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1000 });
  const p = require.resolve("../static/shell.js");
  delete require.cache[p];
  const shell = require(p);
  const toasts = host ? new El("div") : null;
  const slot = phoneSlot();
  const docListeners = {};
  const doc = {
    activeElement: null,
    getElementById: (id) => (id === "toasts" ? toasts : slot[id] instanceof El ? slot[id] : null),
    createElement: (tag) => new El(tag),
    addEventListener: (type, fn) => { (docListeners[type] = docListeners[type] || []).push(fn); },
  };
  const copies = [];
  Object.assign(globalThis, {
    document: doc,
    window: { innerWidth: width, innerHeight: 900 },
    mobileActive,
    createToastStack,
    copyText: (text, done) => copies.push({ text, done }),
  });
  El.beforeRemove = (n) => {
    if (!toasts || !n.contains(doc.activeElement)) return;
    const was = doc.activeElement;
    doc.activeElement = null;
    if (blur) toasts.fire("focusout", { target: was, relatedTarget: null });
  };
  t.after(() => { for (const k of GLOBALS) delete globalThis[k]; El.beforeRemove = null; });
  const entries = () => toasts.children.map(view);
  const node = (i) => toasts.children[i];
  const docFire = (type, target) => { for (const fn of docListeners[type] || []) fn({ target }); };
  return { shell, toasts, slot, doc, copies, entries, node, docFire };
}

// What one rendered entry shows.
function view(n) {
  const dot = n.find("dot");
  const meter = n.find("meter");
  const act = n.find("toast-act");
  const x = n.find("toast-x");
  return {
    id: Number(n.dataset.id),
    cls: n.className,
    role: n.getAttribute("role"),
    text: n.find("toast-text").textContent,
    paused: n.find("toast-paused") ? n.find("toast-paused").textContent : null,
    act: act ? act.textContent : null,
    x: x ? { text: x.textContent, label: x.getAttribute("aria-label"), title: x.title, cls: x.className } : null,
    spin: !!n.find("spin"),
    dot: dot ? dot.className : null,
    meter: meter ? meter.children[0].style.width : null,
  };
}
const texts = (pg) => pg.entries().map((e) => e.text);

// --- toastView (pure) ----------------------------------------------------------
test("toastView: role is status for info, warn and pending, alert for error (F62)", () => {
  const { toastView } = require("../static/shell.js");
  const v = (kind, more) => toastView(Object.assign({ id: 1, text: "x", kind, undo: false, detail: "", progress: null }, more));
  assert.deepStrictEqual(v("info"), { role: "status", cls: ["toast"], dot: null, spin: false, action: null });
  assert.deepStrictEqual(v("warn"), { role: "status", cls: ["toast", "is-warn"], dot: "needs", spin: false, action: null });
  assert.deepStrictEqual(v("pending"), { role: "status", cls: ["toast"], dot: null, spin: true, action: null });
  assert.deepStrictEqual(v("error"), { role: "alert", cls: ["toast", "is-error"], dot: "crashed", spin: false, action: null });
});

test("toastView: Copy details only on an error that carries detail; Undo on an undo entry", () => {
  const { toastView } = require("../static/shell.js");
  const v = (kind, more) => toastView(Object.assign({ id: 1, text: "x", kind, undo: false, detail: "", progress: null }, more));
  assert.strictEqual(v("error", { detail: "docker stop failed: timeout (HTTP 500)" }).action, "copy");
  assert.strictEqual(v("error", { detail: "" }).action, null);
  assert.strictEqual(v("info", { detail: "raw" }).action, null, "only an error offers its details");
  assert.strictEqual(v("warn", { detail: "raw" }).action, null);
  assert.strictEqual(v("info", { undo: true }).action, "undo");
  assert.strictEqual(v("error", { undo: true, detail: "raw" }).action, "undo", "one action: Undo wins");
});

// --- desktop stack ---------------------------------------------------------------
test("three toasts in one tick all show, newest on top (F61); a fourth pushes the oldest plain one out", (t) => {
  const pg = page(t);
  pg.shell.showToast("a");
  pg.shell.showToast("b");
  pg.shell.showToast("c");
  assert.deepStrictEqual(texts(pg), ["c", "b", "a"]);
  pg.shell.showToast("d");
  assert.deepStrictEqual(texts(pg), ["d", "c", "b"]);
});

test("desktop showToast returns the stack id, leaves the phone slot alone and does not claim it", (t) => {
  const pg = page(t);
  const before = pg.shell.toastSeqNow();
  const id = pg.shell.showToast("Saved");
  assert.strictEqual(typeof id, "number");
  assert.strictEqual(pg.entries()[0].id, id);
  assert.strictEqual(pg.slot["toast-msg"].textContent, "", "the slot is not written");
  assert.strictEqual(pg.slot.visible(), false);
  assert.strictEqual(pg.shell.toastSeqNow(), before, "a pending toast on the phone path would not be blocked");
  assert.strictEqual(pg.shell.toastStacked(), true);
});

test("an error stays until its ×; an info toast hides at 6 s", (t) => {
  const pg = page(t);
  pg.shell.showToast("Could not end docs.", { error: true });
  pg.shell.showToast("Saved");
  t.mock.timers.tick(5999);
  assert.deepStrictEqual(texts(pg), ["Saved", "Could not end docs."]);
  t.mock.timers.tick(1);
  assert.deepStrictEqual(texts(pg), ["Could not end docs."]);
  t.mock.timers.tick(10 * 60 * 1000);
  assert.deepStrictEqual(texts(pg), ["Could not end docs."], "no timer removes an error");
  pg.node(0).find("toast-x").onclick();
  assert.deepStrictEqual(pg.entries(), []);
});

test("one timer, re-armed from the nearest deadline: a custom ms and the default both land on time", (t) => {
  const pg = page(t);
  pg.shell.showToast("long", { ms: 60000 });
  t.mock.timers.tick(1000);
  pg.shell.showToast("short");
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(texts(pg), ["long"]);
  t.mock.timers.tick(52999);
  assert.deepStrictEqual(texts(pg), ["long"]);
  t.mock.timers.tick(1);
  assert.deepStrictEqual(pg.entries(), []);
});

test("entry markup: kit classes, roles, a dot for error and warn, × labelled Dismiss (F62)", (t) => {
  const pg = page(t);
  pg.shell.showToast("Saved");
  pg.shell.showToast("Clone needs you", { warn: true });
  pg.shell.showToast("End failed: boom", { error: true });
  const [err, warn, info] = pg.entries();
  assert.strictEqual(err.cls, "toast is-error");
  assert.strictEqual(err.role, "alert");
  assert.strictEqual(err.dot, "dot dot-crashed");
  assert.strictEqual(warn.cls, "toast is-warn");
  assert.strictEqual(warn.role, "status");
  assert.strictEqual(warn.dot, "dot dot-needs");
  assert.strictEqual(info.cls, "toast");
  assert.strictEqual(info.role, "status");
  assert.strictEqual(info.dot, null);
  for (const e of [err, warn, info]) {
    assert.deepStrictEqual(e.x, { text: "×", label: "Dismiss", title: "Dismiss", cls: "btn btn-ghost btn-icon btn-sm toast-x" });
    assert.strictEqual(e.spin, false);
    assert.strictEqual(e.meter, null);
  }
  assert.strictEqual(pg.node(0).getAttribute("data-id") || pg.node(0).dataset.id, String(err.id));
});

test("message text goes in as text, never markup", (t) => {
  const pg = page(t);
  pg.shell.showToast("<img src=x onerror=alert(1)>");
  assert.strictEqual(pg.entries()[0].text, "<img src=x onerror=alert(1)>");
  assert.strictEqual(pg.node(0).find("toast-text").children.length, 0);
});

test("human is the desktop sentence; msg is the fallback (F64)", (t) => {
  const pg = page(t);
  pg.shell.showToast("End failed: docker stop failed: timeout", { error: true, human: "Could not end docs.", detail: "docker stop failed: timeout (HTTP 500)" });
  pg.shell.showToast("Rename failed: x", { error: true });
  assert.deepStrictEqual(texts(pg), ["Rename failed: x", "Could not end docs."]);
});

test("Copy details copies the raw text and says Copied for 1.5 s; an error with no detail has no button", (t) => {
  const pg = page(t);
  pg.shell.showToast("End failed: docker stop failed: timeout", { error: true, human: "Could not end docs.", detail: "docker stop failed: timeout (HTTP 500)" });
  const btn = pg.node(0).find("toast-act");
  assert.strictEqual(btn.textContent, "Copy details");
  assert.strictEqual(btn.className, "btn btn-ghost btn-sm toast-act");
  assert.strictEqual(btn.type, "button");
  btn.onclick();
  assert.deepStrictEqual(pg.copies.map((c) => c.text), ["docker stop failed: timeout (HTTP 500)"]);
  pg.copies[0].done(true);
  assert.strictEqual(btn.textContent, "Copied");
  t.mock.timers.tick(1499);
  assert.strictEqual(btn.textContent, "Copied");
  t.mock.timers.tick(1);
  assert.strictEqual(btn.textContent, "Copy details");
  btn.onclick();
  pg.copies[1].done(false);
  assert.strictEqual(btn.textContent, "Copy failed", "a refused copy is not silent");
  t.mock.timers.tick(1500);
  assert.strictEqual(btn.textContent, "Copy details");
  assert.strictEqual(pg.entries().length, 1, "copying does not dismiss the error");

  pg.shell.showToast("Spawn failed: boom", { error: true });
  assert.strictEqual(pg.entries()[0].act, null);
});

test("Undo runs the callback and removes its entry; an Undo is pushed down, never replaced", (t) => {
  const pg = page(t);
  let undone = 0;
  pg.shell.showToast("Spawned docs", { undo: () => { undone++; } });
  pg.shell.showToast("a");
  pg.shell.showToast("b");
  pg.shell.showToast("c");
  assert.deepStrictEqual(texts(pg), ["c", "b", "Spawned docs"], "the plain ones went first");
  const undo = pg.node(2).find("toast-act");
  assert.strictEqual(undo.textContent, "Undo");
  assert.strictEqual(undo.dataset.act, "undo");
  undo.onclick();
  assert.strictEqual(undone, 1);
  assert.deepStrictEqual(texts(pg), ["c", "b"]);
});

test("Undo and × on the focused toast: the blur fired by its removal does not break the click", (t) => {
  const pg = page(t);
  let undone = 0;
  pg.shell.showToast("Saved");
  pg.shell.showToast("Spawned docs", { undo: () => { undone++; } });
  const undo = pg.node(0).find("toast-act");
  undo.focusVisible = true;
  pg.doc.activeElement = undo;
  pg.toasts.fire("focusin", { target: undo });
  assert.strictEqual(pg.entries()[1].paused, "paused");
  undo.onclick();
  assert.strictEqual(undone, 1, "the callback ran");
  assert.deepStrictEqual(pg.entries().map((e) => [e.text, e.paused]), [["Saved", null]], "focus left with the toast: resumed");
  const x = pg.node(0).find("toast-x");
  pg.doc.activeElement = x;
  pg.toasts.fire("focusin", { target: x });
  x.onclick();
  assert.deepStrictEqual(pg.entries(), []);
});

// Keyboard focus on the toast's own button, as Tab then Enter leaves it.
function keyFocus(pg, btn) {
  btn.focusVisible = true;
  pg.doc.activeElement = btn;
  pg.toasts.fire("focusin", { target: btn });
}

test("no blur on removal (Firefox, Safari): a keyboard Undo leaves the remaining toast timed", (t) => {
  const pg = page(t, { blur: false });
  let undone = 0;
  pg.shell.showToast("Saved");
  pg.shell.showToast("Spawned docs", { undo: () => { undone++; } });
  keyFocus(pg, pg.node(0).find("toast-act"));
  assert.strictEqual(pg.entries()[1].paused, "paused", "keyboard focus inside pauses");
  pg.node(0).find("toast-act").onclick();
  assert.strictEqual(undone, 1);
  assert.deepStrictEqual(pg.entries().map((e) => [e.text, e.paused]), [["Saved", null]], "focus went with the toast");
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(pg.entries(), [], "and its timer runs");
});

test("no blur on removal (Firefox, Safari): a keyboard × leaves the remaining toast timed", (t) => {
  const pg = page(t, { blur: false });
  pg.shell.showToast("Saved");
  pg.shell.showToast("Copied");
  keyFocus(pg, pg.node(0).find("toast-x"));
  assert.strictEqual(pg.entries()[1].paused, "paused");
  pg.node(0).find("toast-x").onclick();
  assert.deepStrictEqual(pg.entries().map((e) => [e.text, e.paused]), [["Saved", null]]);
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(pg.entries(), []);
});

test("no blur on removal: an error's keyboard × resumes the timed toasts under it", (t) => {
  const pg = page(t, { blur: false });
  pg.shell.showToast("Saved");
  pg.shell.showToast("End failed: x", { error: true });
  keyFocus(pg, pg.node(0).find("toast-x"));
  pg.node(0).find("toast-x").onclick();
  assert.deepStrictEqual(pg.entries().map((e) => [e.text, e.paused]), [["Saved", null]]);
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(pg.entries(), []);
});

test("hover pauses every timer and labels the timed entries paused; leaving resumes the rest", (t) => {
  const pg = page(t);
  pg.shell.showToast("End failed: x", { error: true });
  pg.shell.showToast("Saved");
  t.mock.timers.tick(4000);
  pg.toasts.fire("mouseenter");
  assert.deepStrictEqual(pg.entries().map((e) => e.paused), ["paused", null], "an error has no timer to pause");
  t.mock.timers.tick(60000);
  assert.deepStrictEqual(texts(pg), ["Saved", "End failed: x"], "nothing expires while paused");
  pg.toasts.fire("mouseleave");
  assert.deepStrictEqual(pg.entries().map((e) => e.paused), [null, null]);
  t.mock.timers.tick(1999);
  assert.deepStrictEqual(texts(pg), ["Saved", "End failed: x"], "2 s were left at the pause");
  t.mock.timers.tick(1);
  assert.deepStrictEqual(texts(pg), ["End failed: x"]);
});

test("removing the hovered toast fires no mouseleave: an empty list ends the pause, and so does the pointer landing outside", (t) => {
  const pg = page(t);
  pg.shell.showToast("End failed: x", { error: true });
  pg.toasts.fire("mouseenter");
  pg.node(0).find("toast-x").onclick();
  pg.shell.showToast("Saved");
  assert.strictEqual(pg.entries()[0].paused, null, "the new toast runs");
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(pg.entries(), []);

  pg.shell.showToast("a");
  pg.shell.showToast("b");
  pg.toasts.fire("mouseenter");
  pg.node(0).find("toast-x").onclick(); // b goes; the pointer is now over the terminal
  assert.strictEqual(pg.entries()[0].paused, "paused");
  pg.docFire("mouseover", new El("div"));
  assert.strictEqual(pg.entries()[0].paused, null, "the next pointer move outside the stack resumes");
  pg.docFire("mouseover", pg.node(0).find("toast-text"));
  pg.toasts.fire("mouseenter");
  assert.strictEqual(pg.entries()[0].paused, "paused", "a move inside does not");
});

test("keyboard focus inside the stack pauses it; a mouse click's focus does not latch it; focus leaving resumes", (t) => {
  const pg = page(t);
  pg.shell.showToast("End failed: x", { error: true, detail: "raw" });
  pg.shell.showToast("Saved");
  const copy = pg.node(1).find("toast-act");
  copy.focusVisible = true;
  pg.doc.activeElement = copy;
  pg.toasts.fire("focusin", { target: copy });
  assert.strictEqual(pg.entries()[0].paused, "paused");
  const out = new El("textarea");
  pg.doc.activeElement = out;
  pg.toasts.fire("focusout", { target: copy, relatedTarget: out });
  assert.strictEqual(pg.entries()[0].paused, null);
  copy.focusVisible = false; // a click focuses the button without :focus-visible
  pg.doc.activeElement = copy;
  pg.toasts.fire("focusin", { target: copy });
  assert.strictEqual(pg.entries()[0].paused, null, "hover is what pauses for the pointer");
});

test("a pending entry has a spinner and no ×, and resolveToast turns it into its result in place", (t) => {
  const pg = page(t);
  pg.shell.showToast("Saved");
  const h = pg.shell.showToast("Ending docs…", { sticky: true });
  pg.shell.showToast("Copied");
  const pending = pg.entries()[1];
  assert.strictEqual(pending.text, "Ending docs…");
  assert.strictEqual(pending.spin, true);
  assert.strictEqual(pending.x, null, "M8.5 draws no × on a pending entry");
  assert.strictEqual(pending.role, "status");
  t.mock.timers.tick(60000);
  assert.deepStrictEqual(texts(pg), ["Ending docs…"], "the infos expired; pending has no timer");
  assert.strictEqual(pg.shell.resolveToast(h, "Ended docs", { desktopOnly: true }), h);
  assert.deepStrictEqual(pg.entries().map((e) => [e.id, e.text, e.spin, !!e.x]), [[h, "Ended docs", false, true]]);
  t.mock.timers.tick(6000);
  assert.deepStrictEqual(pg.entries(), [], "the result is timed like any info");
});

test("resolveToast keeps the position: the result sits where the pending entry was", (t) => {
  const pg = page(t);
  const h = pg.shell.showToast("Ending docs…", { sticky: true });
  pg.shell.showToast("Copied");
  pg.shell.resolveToast(h, "End failed: boom", { error: true, human: "Could not end docs.", detail: "boom (HTTP 500)" });
  const [top, second] = pg.entries();
  assert.strictEqual(top.text, "Copied");
  assert.deepStrictEqual([second.id, second.text, second.cls, second.act], [h, "Could not end docs.", "toast is-error", "Copy details"]);
});

test("an evicted pending entry's result is pushed as a new toast", (t) => {
  const pg = page(t);
  const h = pg.shell.showToast("Ending a…", { sticky: true });
  pg.shell.showToast("Ending b…", { sticky: true });
  pg.shell.showToast("Ending c…", { sticky: true });
  pg.shell.showToast("Ending d…", { sticky: true }); // all protected: the oldest pending goes
  assert.ok(!texts(pg).includes("Ending a…"));
  const id = pg.shell.resolveToast(h, "Ended a", {});
  assert.notStrictEqual(id, h);
  assert.strictEqual(pg.entries()[0].text, "Ended a");
});

test("replace updates a listed toast in place (upload progress), and the meter takes its own row", (t) => {
  const pg = page(t);
  const h = pg.shell.showToast("Uploading f.txt…", { progress: 0, ms: 60000 });
  pg.shell.showToast("Saved");
  const h2 = pg.shell.showToast("Uploading f.txt: 40%", { progress: 0.4, ms: 60000, replace: h });
  assert.strictEqual(h2, h);
  assert.deepStrictEqual(pg.entries().map((e) => [e.text, e.meter]), [["Saved", null], ["Uploading f.txt: 40%", "40%"]]);
  const meter = pg.node(1).find("meter");
  assert.strictEqual(pg.node(1).children[pg.node(1).children.length - 1], meter, "the meter is the entry's last child");
  pg.shell.showToast("Uploaded f.txt to ~/x", { replace: h });
  assert.deepStrictEqual(pg.entries().map((e) => [e.id, e.text, e.meter]), [[pg.entries()[0].id, "Saved", null], [h, "Uploaded f.txt to ~/x", null]]);
});

test("replace with a handle that is gone pushes a new toast", (t) => {
  const pg = page(t);
  const h = pg.shell.showToast("Uploading f.txt…", { progress: 0 });
  pg.node(0).find("toast-x").onclick();
  const h2 = pg.shell.showToast("Uploading f.txt: 50%", { progress: 0.5, replace: h });
  assert.notStrictEqual(h2, h);
  assert.deepStrictEqual(texts(pg), ["Uploading f.txt: 50%"]);
});

test("dismissToast removes an entry by handle; an unknown handle is a no-op", (t) => {
  const pg = page(t);
  const a = pg.shell.showToast("a", { error: true });
  pg.shell.showToast("b", { error: true });
  pg.shell.dismissToast(a);
  pg.shell.dismissToast(9999);
  assert.deepStrictEqual(texts(pg), ["b"]);
});

test("desktopOnly shows on the desktop", (t) => {
  const pg = page(t);
  pg.shell.showToast("Ended docs", { desktopOnly: true });
  assert.deepStrictEqual(texts(pg), ["Ended docs"]);
});

// --- phone slot --------------------------------------------------------------------
test("the phone #toast gets role status, or alert for an error (F62); the handle is the slot's sequence number", (t) => {
  const pg = page(t, { width: 390 });
  assert.strictEqual(pg.shell.toastStacked(), false);
  const h1 = pg.shell.showToast("Saved");
  assert.strictEqual(pg.slot.toast.getAttribute("role"), "status");
  assert.strictEqual(pg.slot["toast-msg"].textContent, "Saved");
  assert.strictEqual(h1, pg.shell.toastSeqNow());
  assert.strictEqual(pg.slot.toast.getAttribute("aria-live"), "polite");
  const h2 = pg.shell.showToast("End failed: boom", { error: true, human: "Could not end docs.", detail: "boom" });
  assert.strictEqual(pg.slot.toast.getAttribute("role"), "alert");
  // index.html's static aria-live="polite" would override the alert role's implicit assertive.
  assert.strictEqual(pg.slot.toast.getAttribute("aria-live"), "assertive", "an error interrupts, as role=alert does");
  assert.strictEqual(pg.slot["toast-msg"].textContent, "End failed: boom", "the phone shows msg, not human");
  assert.strictEqual(h2, h1 + 1);
  pg.shell.showToast("Clone needs you", { warn: true });
  assert.strictEqual(pg.slot.toast.getAttribute("role"), "status");
  assert.strictEqual(pg.slot.toast.getAttribute("aria-live"), "polite");
  assert.deepStrictEqual(pg.entries(), [], "nothing reaches the stack on the phone");
});

test("the phone shows nothing for desktopOnly and keeps its slot (no sequence bump)", (t) => {
  const pg = page(t, { width: 390 });
  pg.shell.showToast("Saved");
  const seq = pg.shell.toastSeqNow();
  assert.strictEqual(pg.shell.showToast("Ended docs", { desktopOnly: true }), null);
  assert.strictEqual(pg.slot["toast-msg"].textContent, "Saved");
  assert.strictEqual(pg.shell.toastSeqNow(), seq);
});

test("the phone ignores replace; dismissToast hides the slot only while it still shows that handle", (t) => {
  const pg = page(t, { width: 390 });
  const h = pg.shell.showToast("Uploading f.txt…", { progress: 0 });
  pg.shell.showToast("Uploading f.txt: 50%", { progress: 0.5, replace: h });
  assert.strictEqual(pg.slot["toast-msg"].textContent, "Uploading f.txt: 50%");
  pg.shell.dismissToast(h);
  assert.strictEqual(pg.slot.visible(), true, "a newer toast owns the slot");
  pg.shell.dismissToast(pg.shell.toastSeqNow());
  assert.strictEqual(pg.slot.visible(), false);
});

test("a page with no #toasts host keeps the slot at any width", (t) => {
  const pg = page(t, { width: 1280, host: false });
  assert.strictEqual(pg.shell.toastStacked(), false);
  pg.shell.showToast("Saved");
  assert.strictEqual(pg.slot["toast-msg"].textContent, "Saved");
});

// --- static wiring ------------------------------------------------------------------
test("index.html: #toasts is a labelled polite live region beside #toast; toaststack.js loads before shell.js", () => {
  const html = read("index.html");
  // aria-live on the host: a role=status entry inserted with its text already
  // set is not announced by many screen readers; an addition to a live region is.
  const host = '<div id="toasts" role="region" aria-label="Notifications" aria-live="polite"></div>';
  assert.ok(html.includes(host), "the stack host");
  const slotEnd = html.indexOf("</div>", html.indexOf('<div id="toast-bar"'));
  const between = html.slice(html.indexOf("</div>", slotEnd + 1) + 6, html.indexOf(host));
  assert.strictEqual(between.trim(), "", "right after #toast");
  // The phone slot is a live region from page load, before its first toast sets a role (review M5):
  // a region that only becomes live with its first content is often not announced.
  assert.ok(html.includes('<div id="toast" aria-live="polite">'), "#toast is polite-live in the markup");
  const stack = html.indexOf('<script src="/toaststack.js"></script>');
  assert.ok(stack > 0, "toaststack.js is loaded");
  assert.ok(stack < html.indexOf('<script src="/shell.js"></script>'), "before shell.js");
});

// depth-0 rules of a stylesheet (comments stripped), as [selector, body].
function topRules(css) {
  const out = [];
  let depth = 0, start = 0, sel = "";
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") { if (depth === 0) { sel = css.slice(start, i).trim(); start = i + 1; } depth++; }
    else if (css[i] === "}") { depth--; if (depth === 0) { out.push([sel, css.slice(start, i)]); start = i + 1; } }
  }
  return out;
}

test("app.css hides #toasts at the top level (every width) and stills the toast spinner for reduced motion", () => {
  const css = stripComments(read("app.css"));
  const rules = topRules(css);
  const hide = rules.filter(([s]) => s === "#toasts");
  assert.strictEqual(hide.length, 1, "one top-level #toasts rule");
  assert.strictEqual(hide[0][1].trim().replace(/;$/, ""), "display: none");
  const rm = rules.find(([s]) => s === "@media (prefers-reduced-motion: reduce)" && /\.pending-line::before/.test(rules.find(([q]) => q === s)[1]));
  assert.ok(rm, "the shared reduced-motion rule");
  assert.match(rm[1], /\.toast \.spin/);
  assert.match(rm[1], /animation:\s*none/);
});

test("desktop.css shows #toasts top right inside the desktop block; only the toasts take the pointer", () => {
  const css = stripComments(read("desktop.css"));
  assert.ok(css.trim().startsWith("@media (min-width: 701px) {"));
  const m = /(^|[\s}])#toasts\s*\{([^}]*)\}/.exec(css);
  assert.ok(m, "#toasts rule");
  const decl = (p) => { const r = new RegExp("(^|;)\\s*" + p + ":\\s*([^;]+)").exec(m[2]); return r ? r[2].trim() : undefined; };
  assert.strictEqual(decl("position"), "fixed");
  assert.strictEqual(decl("top"), "48px");
  assert.strictEqual(decl("right"), "var(--sp-4)");
  assert.strictEqual(decl("display"), "flex");
  assert.strictEqual(decl("flex-direction"), "column");
  assert.strictEqual(decl("align-items"), "flex-end");
  assert.strictEqual(decl("gap"), "var(--sp-2)");
  assert.strictEqual(decl("pointer-events"), "none");
  assert.ok(Number(decl("z-index")) >= 60, "at least the old #toast's z-index");
  assert.match(css, /#toasts > \.toast\s*\{[^}]*pointer-events:\s*auto/);
});

test("desktop.css: .toast-paused and .toast-act live in the kit section; the pending spinner pulses", () => {
  const raw = read("desktop.css");
  const kit = stripComments(raw.slice(raw.indexOf("/* === kit primitives (2026-11, #171) === */"), raw.indexOf("/* === end kit primitives === */")));
  assert.match(kit, /\.toast-paused\s*[{,]/);
  assert.match(kit, /\.toast-act[\s{,[]/);
  assert.match(kit, /\.toast-act\[data-act="undo"\][^{]*\{[^}]*color:\s*var\(--accent\)/, "Undo is the accent action (M7.5)");
  // Below app.css's reduced-motion `.toast .spin` (0,2,0), which loads first.
  assert.match(kit, /:where\(\.toast\) \.spin\s*\{[^}]*animation:\s*spawn-pulse/);
});

// --- callers -------------------------------------------------------------------------
function fnSrc(src, name) {
  const m = new RegExp("(async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

// killSession cut out of app.js and run with stand-ins: what it hands done().
async function runKill({ fail } = {}) {
  const results = [], toasts = [], closed = [];
  const ctx = vm.createContext({
    lastSessions: [{ id: "s1", name: "docs" }],
    killing: new Set(),
    refreshSessions: async () => {},
    api: async () => { if (fail) throw fail; },
    pendingToast: (text) => { results.push(["pending", text]); return (r) => results.push(["done", r]); },
    toastFailText,
    showToast: (msg, opts) => toasts.push([msg, opts]),
    closeTerminal: (id) => closed.push(id),
    encodeURIComponent,
  });
  new vm.Script(fnSrc(read("app.js"), "killSession") + "\nglobalThis.killSession = killSession;").runInContext(ctx);
  await ctx.killSession("s1");
  return { results: JSON.parse(JSON.stringify(results)), toasts, closed };
}

test("End: success hands done a desktop-only \"Ended <name>\"", async () => {
  const r = await runKill();
  assert.deepStrictEqual(r.results, [["pending", "Ending docs…"], ["done", { msg: "Ended docs", opts: { desktopOnly: true } }]]);
  assert.deepStrictEqual(r.toasts, []);
  assert.deepStrictEqual(r.closed, ["s1"]);
});

test("End: failure hands done the phone's \"End failed: …\" plus the human sentence and the raw detail (F64)", async () => {
  const err = Object.assign(new Error("docker stop failed: timeout"), { status: 500 });
  const r = await runKill({ fail: err });
  assert.deepStrictEqual(r.results[1], ["done", {
    msg: "End failed: docker stop failed: timeout",
    opts: { error: true, human: "Could not end docs.", detail: "docker stop failed: timeout (HTTP 500)" },
  }]);
  assert.deepStrictEqual(r.toasts, [], "no second error toast: the pending entry becomes it");
  assert.deepStrictEqual(r.closed, []);
});

test("palette: a failed command names itself in the sentence and keeps the raw text for Copy details", async () => {
  const toasts = [];
  const ctx = vm.createContext({ closePalette: () => {}, showToast: (m, o) => toasts.push([m, o]), toastFailText, Promise });
  new vm.Script(fnSrc(read("palette.js"), "runItem") + "\nglobalThis.runItem = runItem;").runInContext(ctx);
  await ctx.runItem({ label: "End session · docs", run: () => { throw Object.assign(new Error("500 internal error"), { status: 500 }); } });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(toasts)), [["Failed: 500 internal error",
    { error: true, human: 'Could not run "End session · docs".', detail: "500 internal error (HTTP 500)" }]]);
});

test("upload: one handle per upload; progress and the result replace it in place", () => {
  const { uploadFiles } = require("../static/upload.js");
  const calls = [];
  let next = 7, xhr = null;
  globalThis.showToast = (msg, o) => { calls.push([msg, o]); return o && o.replace ? o.replace : next++; };
  globalThis.toastFailText = toastFailText;
  globalThis.shortPath = (p) => p;
  globalThis.XMLHttpRequest = class { constructor() { this.upload = {}; xhr = this; } open() {} send() {} };
  try {
    uploadFiles([new File(["x"], "f.txt")], "/d");
    xhr.upload.onprogress({ lengthComputable: true, loaded: 1, total: 2 });
    xhr.status = 200;
    xhr.responseText = JSON.stringify({ uploaded: [{ name: "f.txt" }] });
    xhr.onload();
    assert.deepStrictEqual(calls.map(([m, o]) => [m, o.replace]), [
      ["Uploading f.txt…", undefined],
      ["Uploading f.txt: 50%", 7],
      ["Uploaded f.txt to /d", 7],
    ]);

    calls.length = 0;
    uploadFiles([new File(["x"], "a"), new File(["y"], "b")], "/d");
    xhr.status = 500;
    xhr.responseText = JSON.stringify({ error: "disk full" });
    xhr.onload();
    assert.deepStrictEqual(calls[1], ["Upload failed: disk full", { error: true, human: "Could not upload 2 files.", detail: "disk full (HTTP 500)", replace: 8 }]);

    calls.length = 0;
    uploadFiles([new File(["x"], "f.txt")], "/d");
    xhr.onerror();
    assert.deepStrictEqual(calls[1], ["Upload failed: connection lost", { error: true, human: "Could not upload f.txt.", detail: "connection lost", replace: 9 }]);
  } finally {
    for (const k of ["showToast", "toastFailText", "shortPath", "XMLHttpRequest"]) delete globalThis[k];
  }
});

test("rename, spawn and + Shell keep msg for the phone and add the human sentence and detail", () => {
  const app = read("app.js");
  assert.match(app, /const f = toastFailText\("rename " \+ cur, e\);\s*showToast\("Rename failed: " \+ e\.message, \{ error: true, human: f\.text, detail: f\.detail \}\);/);
  assert.match(app, /toastFailText\("start the session", e\)/);
  assert.match(app, /const kept = draft\.trim\(\) \? " — your first message is kept" : "";/);
  // The spawn sentence, run as written: the closing period gives way to the note only when there
  // is a note; without one the sentence keeps its full stop like every other (review M1).
  const m = /showToast\("Spawn failed: " \+ e\.message \+ kept, \{ error: true, human: ([^\n]*?), detail: f\.detail \}\);/.exec(app);
  assert.ok(m, "the spawn failure toast");
  const human = new Function("f", "kept", "return " + m[1] + ";");
  const failed = toastFailText("start the session", { message: "boom", status: 500 });
  assert.strictEqual(human(failed, ""), "Could not start the session.");
  assert.strictEqual(human(failed, " — your first message is kept"), "Could not start the session — your first message is kept");
  const named = toastFailText("start the session", { message: "Profile not found.", status: 404 });
  assert.strictEqual(human(named, ""), "Could not start the session: Profile not found.", "a server sentence keeps its own period");
  assert.match(app, /const f = toastFailText\("start a shell", e\);\s*showToast\("Shell failed: " \+ e\.message, \{ error: true, human: f\.text, detail: f\.detail \}\);/);
});
