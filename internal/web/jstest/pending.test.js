"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { PENDING_DELAY_MS, withPending, pendingRegion, pendingToast } = require("../static/pending.js");

function fakeEl(text) {
  const classes = new Set();
  const attrs = {};
  return {
    textContent: text,
    disabled: false,
    attrs,
    setAttribute(k, v) { attrs[k] = String(v); },
    removeAttribute(k) { delete attrs[k]; },
    getAttribute(k) { return k in attrs ? attrs[k] : null; },
    classList: {
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
    },
  };
}

function fakeContainer() {
  const c = { children: [] };
  c.prepend = (node) => { node.parent = c; c.children.unshift(node); };
  return c;
}

// The pending line is built with document.createElement in the browser.
function installDocument(toastMsg) {
  globalThis.document = {
    createElement: (tag) => {
      const n = { tag, className: "", textContent: "", parent: null };
      n.remove = () => {
        if (n.parent) n.parent.children = n.parent.children.filter((x) => x !== n);
        n.parent = null;
      };
      return n;
    },
    getElementById: (id) => (id === "toast-msg" ? toastMsg : null),
  };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test("fast fn never shows busy", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  const d = deferred();
  const p = withPending(el, "Saving…", () => d.promise);
  assert.strictEqual(el.disabled, true, "disabled while waiting");
  assert.strictEqual(el.getAttribute("aria-busy"), "true");
  t.mock.timers.tick(PENDING_DELAY_MS - 1);
  assert.strictEqual(el.classList.contains("busy"), false);
  assert.strictEqual(el.textContent, "Save");
  d.resolve("ok");
  assert.strictEqual(await p, "ok");
  t.mock.timers.tick(PENDING_DELAY_MS * 2);
  assert.strictEqual(el.classList.contains("busy"), false);
  assert.strictEqual(el.textContent, "Save");
  assert.strictEqual(el.disabled, false);
  assert.strictEqual(el.getAttribute("aria-busy"), null);
});

test("slow fn shows label after 300ms then restores", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  const d = deferred();
  const p = withPending(el, "Saving…", () => d.promise);
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.strictEqual(el.classList.contains("busy"), true);
  assert.strictEqual(el.textContent, "Saving…");
  d.resolve(42);
  assert.strictEqual(await p, 42);
  assert.strictEqual(el.textContent, "Save");
  assert.strictEqual(el.classList.contains("busy"), false);
  assert.strictEqual(el.getAttribute("aria-busy"), null);
  assert.strictEqual(el.disabled, false);
});

test("reject clears and rethrows", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  const d = deferred();
  const boom = new Error("boom");
  const p = withPending(el, "Saving…", () => d.promise);
  t.mock.timers.tick(PENDING_DELAY_MS);
  d.reject(boom);
  await assert.rejects(p, (e) => e === boom);
  assert.strictEqual(el.textContent, "Save");
  assert.strictEqual(el.classList.contains("busy"), false);
  assert.strictEqual(el.getAttribute("aria-busy"), null);
  assert.strictEqual(el.disabled, false);
});

test("a synchronous throw in fn rejects and leaves no stale re-entry guard", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  await assert.rejects(withPending(el, "Saving…", () => { throw new Error("sync"); }), /sync/);
  assert.strictEqual(el.disabled, false);
  let calls = 0;
  await withPending(el, "Saving…", async () => { calls++; });
  assert.strictEqual(calls, 1, "a settled call must not block the next one");
});

test("owner rewrite wins", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  const d = deferred();
  const p = withPending(el, "Saving…", () => d.promise);
  t.mock.timers.tick(PENDING_DELAY_MS);
  el.textContent = "Saved";
  el.disabled = true;
  d.resolve();
  await p;
  assert.strictEqual(el.textContent, "Saved", "helper must not overwrite the owner's text");
  assert.strictEqual(el.classList.contains("busy"), false);
  assert.strictEqual(el.getAttribute("aria-busy"), null);
});

test("owner re-enabling mid-flight is not clobbered", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  el.disabled = false;
  const d = deferred();
  const p = withPending(el, "Saving…", () => d.promise);
  el.disabled = false; // owner changed it
  d.resolve();
  await p;
  assert.strictEqual(el.disabled, false);
});

test("an already-disabled element is restored to disabled", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  el.disabled = true;
  await withPending(el, "Saving…", async () => {});
  assert.strictEqual(el.disabled, true);
});

test("re-entry returns same promise", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const el = fakeEl("Save");
  const d = deferred();
  let calls = 0;
  const p1 = withPending(el, "Saving…", () => { calls++; return d.promise; });
  const p2 = withPending(el, "Saving…", () => { calls++; return d.promise; });
  assert.strictEqual(p1, p2);
  assert.strictEqual(calls, 1);
  d.resolve();
  await p1;
  // once settled, a new call runs fn again
  await withPending(el, "Saving…", async () => { calls++; });
  assert.strictEqual(calls, 2);
});

test("pendingRegion: fast shows no line, slow prepends one, done removes it, twice is safe", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  installDocument(null);
  const box = fakeContainer();
  const existing = { name: "row" };
  box.children.push(existing);

  const fast = pendingRegion(box, "Loading…");
  t.mock.timers.tick(PENDING_DELAY_MS - 1);
  fast();
  t.mock.timers.tick(PENDING_DELAY_MS * 2);
  assert.strictEqual(box.children.length, 1, "no line when done before the delay");

  const slow = pendingRegion(box, "Loading…");
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.strictEqual(box.children.length, 2);
  assert.strictEqual(box.children[0].className, "pending-line");
  assert.strictEqual(box.children[0].textContent, "Loading…");
  assert.strictEqual(box.children[1], existing, "existing children stay");
  slow();
  assert.deepStrictEqual(box.children, [existing]);
  slow();
  assert.deepStrictEqual(box.children, [existing]);
});

test("pendingToast: shows sticky after the delay; done hides only its own toast", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const msg = { textContent: "" };
  installDocument(msg);
  const shown = [];
  let hides = 0;
  globalThis.showToast = (text, opts) => { shown.push([text, opts]); msg.textContent = text; };
  globalThis.hideToast = () => { hides++; };
  t.after(() => { delete globalThis.showToast; delete globalThis.hideToast; delete globalThis.document; });

  // fast: never shown, done does not hide anything
  const fast = pendingToast("Working…");
  t.mock.timers.tick(PENDING_DELAY_MS - 1);
  fast();
  t.mock.timers.tick(PENDING_DELAY_MS * 2);
  assert.strictEqual(shown.length, 0);
  assert.strictEqual(hides, 0);

  // slow: shown sticky, done hides it
  const slow = pendingToast("Working…");
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.deepStrictEqual(shown, [["Working…", { sticky: true }]]);
  slow();
  assert.strictEqual(hides, 1);

  // a later error toast is left alone
  const again = pendingToast("Working…");
  t.mock.timers.tick(PENDING_DELAY_MS);
  msg.textContent = "Failed: boom";
  again();
  assert.strictEqual(hides, 1, "must not hide a toast that is not ours");
});

// The next two tests run the REAL showToast/hideToast from shell.js against a
// tiny fake DOM, so the toast-slot counter pendingToast relies on is the one the
// page actually has, not a stub that could drift from it.
function installRealToast(t) {
  // Fresh module state per test: toastTimer/toastSeq are module-level, and a
  // timer handle from a previous test's mock clock must not leak into this one.
  const path = require.resolve("../static/shell.js");
  delete require.cache[path];
  const shell = require(path);
  const classes = new Set();
  const els = {
    // `style`: showToast sets the inline bottom offset on the phone layout (#111).
    toast: { style: {}, classList: {
      toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)),
      add: (c) => classes.add(c),
      remove: (c) => classes.delete(c),
      contains: (c) => classes.has(c),
    } },
    "toast-undo": { hidden: true, onclick: null },
    "toast-msg": { textContent: "" },
    "toast-bar": null,
  };
  globalThis.document = { getElementById: (id) => els[id] || null };
  globalThis.showToast = shell.showToast;
  globalThis.hideToast = shell.hideToast;
  globalThis.toastSeqNow = shell.toastSeqNow;
  t.after(() => {
    delete globalThis.showToast; delete globalThis.hideToast;
    delete globalThis.toastSeqNow; delete globalThis.document;
  });
  return { msg: els["toast-msg"], visible: () => classes.has("show") };
}

test("pendingToast: a toast shown before the delay owns the slot, so the pending one never paints", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const ui = installRealToast(t);
  const done = pendingToast("Restarting X…");
  t.mock.timers.tick(PENDING_DELAY_MS - 1);
  showToast("Restart failed: boom", { error: true }); // the action failed fast
  t.mock.timers.tick(PENDING_DELAY_MS * 2);           // ...but cleanup is still running
  assert.strictEqual(ui.msg.textContent, "Restart failed: boom", "error not overwritten");
  assert.strictEqual(ui.visible(), true);
  done();
  assert.strictEqual(ui.msg.textContent, "Restart failed: boom", "done leaves it alone");
  assert.strictEqual(ui.visible(), true);
});

test("pendingToast: the toast is sticky, so it survives past the normal auto-hide", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const ui = installRealToast(t);
  const done = pendingToast("Working…");
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.strictEqual(ui.msg.textContent, "Working…");
  t.mock.timers.tick(60000);
  assert.strictEqual(ui.visible(), true, "no auto-hide timer was armed");
  done();
  assert.strictEqual(ui.visible(), false, "its owner hides it");
  // and an ordinary toast still auto-hides
  showToast("Saved");
  assert.strictEqual(ui.visible(), true);
  t.mock.timers.tick(6000);
  assert.strictEqual(ui.visible(), false);
});

// A click can re-render the control's container, so by the time the delay
// fires the clicked node is detached and relabelling it is invisible. The
// feedback then goes to the toast slot instead.
test("withPending: a detached element shows its label as a sticky toast, not on the node", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const ui = installRealToast(t);
  const el = fakeEl("+ Shell");
  el.isConnected = true;
  const d = deferred();
  const p = withPending(el, "Starting shell…", () => d.promise);
  el.isConnected = false; // the pane header re-rendered
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.strictEqual(ui.msg.textContent, "Starting shell…");
  assert.strictEqual(ui.visible(), true);
  assert.strictEqual(el.textContent, "+ Shell", "the detached node is left alone");
  assert.strictEqual(el.classList.contains("busy"), false);
  t.mock.timers.tick(60000);
  assert.strictEqual(ui.visible(), true, "sticky");
  d.resolve();
  await p;
  assert.strictEqual(ui.visible(), false, "finally hides its own toast");
});

test("withPending: detached + another toast before the delay -> no pending toast", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const ui = installRealToast(t);
  const el = fakeEl("+ Shell");
  el.isConnected = false;
  const d = deferred();
  const p = withPending(el, "Starting shell…", () => d.promise);
  t.mock.timers.tick(PENDING_DELAY_MS - 1);
  showToast("Could not start shell", { error: true });
  t.mock.timers.tick(PENDING_DELAY_MS * 2);
  assert.strictEqual(ui.msg.textContent, "Could not start shell");
  d.resolve();
  await p;
  assert.strictEqual(ui.msg.textContent, "Could not start shell", "not ours, left alone");
  assert.strictEqual(ui.visible(), true);
});

test("withPending: a connected element is relabelled in place, no toast", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const ui = installRealToast(t);
  const el = fakeEl("+ Shell");
  el.isConnected = true;
  const d = deferred();
  const p = withPending(el, "Starting shell…", () => d.promise);
  t.mock.timers.tick(PENDING_DELAY_MS);
  assert.strictEqual(el.textContent, "Starting shell…");
  assert.strictEqual(ui.visible(), false);
  d.resolve();
  await p;
  assert.strictEqual(el.textContent, "+ Shell");
  assert.strictEqual(ui.visible(), false);
});
