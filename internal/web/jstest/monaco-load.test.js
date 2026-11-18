"use strict";
// monacoload.js, the dock's lazy loader, in a vm on a fake page: loader.js, the editor module and its stylesheet are asked for in
// turn, and a failure of any of them ends the load (a rejected promise, forgotten, so the next open tries again) instead of hanging.
// What matters here is the stylesheet wait: it used to wait for a load or error event that may never come (the module injects the
// <link> itself and an error can fire before this file listens), and every open then said "loading…" until the page was reloaded.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const SRC = fs.readFileSync(path.join(__dirname, "..", "static", "monacoload.js"), "utf8");

// A page whose <head> takes the script and link elements the loader appends. `script` says what the browser does with loader.js:
// "load" or "error". `css` says what becomes of the stylesheet the editor module appends itself (as the real editor.main.js does):
// "applied" (it was loaded before anyone listened: its `sheet` is set), "late-load" or "late-error" (the event fires once a listener
// is on it), or "hang" (nothing ever fires: the event was lost before the loader listened, or the request is stuck).
function fake(o) {
  const opt = Object.assign({ script: "load", module: "load", css: "late-load" }, o);
  const head = { children: [], append(el) { this.children.push(el); el.parent = this; if (el.tag === "script") setImmediate(() => el.onload && opt.script === "load" && el.onload()); if (el.tag === "script" && opt.script === "error") setImmediate(() => el.onerror && el.onerror()); }, querySelector(sel) { return this.children.find((c) => c.tag === "link" && sel.includes("editor.main.css") && c.href.endsWith("/vs/editor/editor.main.css")) || null; } };
  const timers = [];
  const log = { required: [], removed: [], themes: 0 };
  const link = () => {
    const el = { tag: "link", rel: "", href: "", sheet: null, listeners: {}, addEventListener(t, fn) { this.listeners[t] = fn; if ((opt.css === "late-load" && t === "load") || (opt.css === "late-error" && t === "error")) setImmediate(fn); }, remove() { log.removed.push(this); head.children = head.children.filter((c) => c !== this); } };
    return el;
  };
  const doc = { head, createElement: (tag) => (tag === "link" ? link() : { tag, src: "" }) };
  const monaco = {
    editor: { defineTheme() { log.themes++; }, addKeybindingRule() {} },
    KeyCode: { F1: 59 },
    languages: {},
  };
  const win = { monaco };
  const ctx = vm.createContext({
    document: doc, window: win, console, Promise, Error,
    setTimeout: (fn, ms) => { const t = { fn, ms, dead: false }; timers.push(t); return timers.length; },
    clearTimeout: (id) => { if (timers[id - 1]) timers[id - 1].dead = true; },
    culiboxTheme: () => ({}),
    require: Object.assign((deps, ok, bad) => {
      log.required.push(deps[0]);
      if (opt.module === "error") { setImmediate(() => bad(new Error("module failed"))); return; }
      const l = link(); // editor.main.js appends its own stylesheet link
      l.rel = "stylesheet"; l.href = "/vendor/monaco/vs/editor/editor.main.css";
      if (opt.css === "applied") l.sheet = {};
      head.children.push(l);
      setImmediate(() => ok());
    }, { config() {} }),
  });
  new vm.Script(SRC, { filename: "monacoload.js" }).runInContext(ctx);
  const loadMonaco = new vm.Script("loadMonaco", { filename: "get" }).runInContext(ctx);
  return { ctx, head, timers, log, loadMonaco, win };
}
const live = (f) => f.timers.filter((t) => !t.dead);

test("a load whose stylesheet arrives settles with the editor, and sets the theme once", { timeout: 4000 }, async () => {
  const f = fake();
  const m = await f.loadMonaco();
  assert.equal(m, f.win.monaco);
  assert.equal(f.log.themes, 1);
  assert.equal(live(f).length, 0, "the stylesheet's timeout is cleared once it loaded");
  assert.equal(f.loadMonaco(), f.loadMonaco(), "single-flight: one load for every caller");
});

test("a stylesheet that is already applied needs no wait", { timeout: 4000 }, async () => {
  const f = fake({ css: "applied" });
  await f.loadMonaco();
  assert.equal(f.log.themes, 1);
});

test("a stylesheet whose load never fires does not hang the load: it fails after the timeout, and the dead link is dropped", { timeout: 4000 }, async () => {
  const f = fake({ css: "hang" });
  const p = f.loadMonaco();
  const settled = p.then(() => "loaded", (e) => "failed: " + e.message);
  for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  assert.equal(live(f).length, 1, "a timer is running");
  assert.ok(live(f)[0].ms >= 5000 && live(f)[0].ms <= 30000, "a patient but finite wait: " + live(f)[0].ms + " ms");
  live(f)[0].fn(); // the time is up
  assert.match(await settled, /failed: could not load .*editor\.main\.css/);
  assert.equal(f.log.removed.length, 1, "the link that will never load is taken out of the page, so a retry makes a fresh one");
  assert.equal(f.log.themes, 0, "no theme on an editor that is not styled");
});

test("a stylesheet that errors fails the load at once, and the next call tries again from the start", { timeout: 4000 }, async () => {
  const f = fake({ css: "late-error" });
  await assert.rejects(f.loadMonaco(), /could not load .*editor\.main\.css/);
  assert.equal(f.log.removed.length, 1);
  assert.equal(live(f).length, 0, "no timer left behind");
  // Another call is a new load, not the failed one: loader.js is asked for again.
  const before = f.head.children.filter((c) => c.tag === "script").length;
  const p = f.loadMonaco();
  await p.catch(() => {});
  assert.equal(f.head.children.filter((c) => c.tag === "script").length, before + 1, "a failure is not remembered");
});

test("loader.js or the editor module failing rejects the load too", { timeout: 4000 }, async () => {
  await assert.rejects(fake({ script: "error" }).loadMonaco(), /could not load .*loader\.js/);
  await assert.rejects(fake({ module: "error" }).loadMonaco(), /module failed/);
});

test("the editor tab says why when the editor cannot be loaded: a toast with the reason and a sentence in its body, never a 'loading…' that stays", { timeout: 4000 }, async () => {
  const { page } = require("./editorpage.js");
  const p = page({ loadMonaco: () => Promise.reject(new Error("could not load /vendor/monaco/vs/editor/editor.main.css (no answer in 10 s)")) });
  const tab = new p.EditorTab("/p/a.go", { host: p.host });
  for (let i = 0; i < 30; i++) await new Promise((r) => setImmediate(r));
  assert.equal(p.st.toasts.length, 1);
  assert.match(p.st.toasts[0][0], /^The editor could not be loaded: could not load .*editor\.main\.css/);
  assert.equal(p.st.toasts[0][1].error, true);
  const msg = tab.bodyEl.children[0];
  assert.match(msg.textContent, /could not be loaded/, "the body says so too, not 'loading…'");
  assert.notEqual(msg.textContent, "loading…");
  assert.equal(tab.model, null);
});
