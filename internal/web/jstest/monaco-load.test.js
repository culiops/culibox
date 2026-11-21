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
  const log = { required: [], removed: [], themes: 0, openers: [], gate: [] };
  let moduleRan = false; // an AMD module is evaluated once: only the first require() appends the stylesheet link
  const link = () => {
    const el = { tag: "link", rel: "", href: "", sheet: null, listeners: {}, addEventListener(t, fn) { this.listeners[t] = fn; if ((opt.css === "late-load" && t === "load") || (opt.css === "late-error" && t === "error")) setImmediate(fn); }, remove() { log.removed.push(this); head.children = head.children.filter((c) => c !== this); } };
    return el;
  };
  const doc = { head, createElement: (tag) => (tag === "link" ? link() : { tag, src: "" }) };
  const monaco = {
    editor: { defineTheme() { log.themes++; }, addKeybindingRule() {}, registerLinkOpener(o) { log.openers.push(o); return { dispose() {} }; } },
    KeyCode: { F1: 59 },
    languages: {},
  };
  if (opt.noOpenerApi) delete monaco.editor.registerLinkOpener;
  const win = { monaco };
  const ctx = vm.createContext({
    document: doc, window: win, console, Promise, Error,
    setTimeout: (fn, ms) => { const t = { fn, ms, dead: false }; timers.push(t); return timers.length; },
    clearTimeout: (id) => { if (timers[id - 1]) timers[id - 1].dead = true; },
    culiboxTheme: () => ({}),
    openAgentLink: opt.noGate ? undefined : (raw, c) => { log.gate.push([raw, c]); return Promise.resolve(true); },
    require: Object.assign((deps, ok, bad) => {
      log.required.push(deps[0]);
      if (opt.module === "error") { setImmediate(() => bad(new Error("module failed"))); return; }
      if (!moduleRan) {
        moduleRan = true;
        const l = link(); // editor.main.js appends its own stylesheet link
        l.rel = "stylesheet"; l.href = "/vendor/monaco/vs/editor/editor.main.css";
        if (opt.css === "applied") l.sheet = {};
        head.children.push(l);
      }
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

const links = (f) => f.head.children.filter((c) => c.tag === "link");
// The time is up on the one running timer (a timer that fired is not running any more).
const expire = (f) => { const t = live(f)[0]; t.dead = true; t.fn(); };
const settle = async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); };

test("a stylesheet whose load never fires does not hang the load: it fails after a timeout long enough for a slow first load", { timeout: 4000 }, async () => {
  const f = fake({ css: "hang" });
  const settled = f.loadMonaco().then(() => "loaded", (e) => "failed: " + e.message);
  await settle();
  assert.equal(live(f).length, 1, "a timer is running");
  // editor.main.css is about 390 KB and not compressed: 10 s made every first load below 39 KB/s fail, and each retry started over.
  assert.ok(live(f)[0].ms >= 60000 && live(f)[0].ms <= 120000, "patient but finite: " + live(f)[0].ms + " ms");
  expire(f); // the time is up
  assert.match(await settled, /failed: could not load .*editor\.main\.css \(no answer in 60 s\)/);
  assert.equal(f.log.themes, 0, "no theme on an editor that is not styled");
});

test("a stylesheet that timed out is kept, and the retry waits on that same link instead of restarting its download", { timeout: 4000 }, async () => {
  const f = fake({ css: "hang" });
  const first = f.loadMonaco().catch((e) => e.message);
  await settle();
  const link = links(f)[0];
  expire(f); // the first wait is over
  assert.match(await first, /no answer in 60 s/);
  assert.equal(f.log.removed.length, 0, "a link that is still loading is not thrown away");
  assert.deepEqual(links(f), [link], "and nothing else was added");

  // The retry (the next open) finds the same link and waits for it; its load event is the one that settles it.
  const retry = f.loadMonaco();
  await settle();
  assert.deepEqual(links(f), [link], "the retry made no second link: the download in progress is the one awaited");
  assert.equal(live(f).length, 1, "a fresh wait of its own");
  link.listeners.load(); // the download finishes
  assert.equal(await retry, f.win.monaco);
  assert.equal(f.log.themes, 1);
  assert.equal(f.log.removed.length, 0);
  assert.equal(live(f).length, 0, "no timer left behind");
});

test("a stylesheet that finished loading between the two attempts needs no second wait", { timeout: 4000 }, async () => {
  const f = fake({ css: "hang" });
  const first = f.loadMonaco().catch((e) => e.message);
  await settle();
  expire(f);
  await first;
  links(f)[0].sheet = {}; // it arrived after the timeout
  assert.equal(await f.loadMonaco(), f.win.monaco);
  assert.equal(live(f).length, 0, "no wait at all");
  assert.equal(f.log.removed.length, 0);
});

test("a stylesheet that times out twice is dead: the second timeout takes the link out, so the next retry makes a fresh one", { timeout: 4000 }, async () => {
  const f = fake({ css: "hang" });
  for (let attempt = 1; attempt <= 2; attempt++) {
    const p = f.loadMonaco().catch((e) => e.message);
    await settle();
    expire(f);
    assert.match(await p, /no answer in 60 s/);
    assert.equal(f.log.removed.length, attempt - 1, "attempt " + attempt);
  }
  assert.equal(links(f).length, 0, "no link left: an error that fired before anyone listened is not waited on for ever");
  const p = f.loadMonaco().catch((e) => e.message);
  await settle();
  assert.equal(links(f).length, 1, "the next open makes a fresh link");
  assert.equal(live(f).length, 1);
  expire(f);
  await p;
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
  const p = page({ loadMonaco: () => Promise.reject(new Error("could not load /vendor/monaco/vs/editor/editor.main.css (no answer in 60 s)")) });
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

// --- links (#348) ------------------------------------------------------------
// Ctrl/Cmd+click on a link in the editor (the dock editor and every diff editor) would open it in Monaco's own opener. The boot
// puts the link gate in front of that: the opener takes every link, so Monaco's never runs.
// Monaco hands open() a URI (or a string, which its opener service parses first); both read the same through String().
const uriOf = (s) => ({ toString: () => s });

test("boot registers a link opener that hands the link to the gate as an editor link and takes it", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  assert.equal(f.log.openers.length, 1);
  assert.strictEqual(f.log.openers[0].open(uriOf("https://example.com/a?b=1")), true);
  assert.deepEqual(f.log.gate, [["https://example.com/a?b=1", { source: "editor" }]]);
});

// Monaco's URI.toString() percent-encodes the query's = & + : / ; toString(true) is the readable form. This stub encodes the way
// Monaco's does, so a test that reads String(uri) sees what the gate would have been given before the fix.
const monacoUri = (readable) => {
  const i = readable.indexOf("?");
  const enc = i < 0 ? readable : readable.slice(0, i + 1) + readable.slice(i + 1).replace(/[=&+:/]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase());
  return { toString: (skipEncoding) => (skipEncoding ? readable : enc) };
};

test("the gate gets the readable link of a URI with a query, not Monaco's encoded form", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  f.log.openers[0].open(monacoUri("https://example.com/a?x=1&y=2&z=a+b"));
  f.log.openers[0].open(monacoUri("https://ex.com/login?next=https://evil.com/x"));
  assert.deepEqual(f.log.gate, [
    ["https://example.com/a?x=1&y=2&z=a+b", { source: "editor" }],
    ["https://ex.com/login?next=https://evil.com/x", { source: "editor" }],
  ], "an encoded query hides the redirect from the gate's redirect warning");
});

test("a space or a non-ASCII letter in a URI reaches the gate encoded once, as Monaco's own opener sends it", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  f.log.openers[0].open(monacoUri("https://example.com/a b/é"));
  assert.equal(f.log.gate[0][0], "https://example.com/a%20b/%C3%A9");
});

test("the opener takes every scheme, so Monaco's own never opens one: the gate says no to the ones it will not open", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  for (const u of ["file:///etc/passwd", "command:workbench.action.x", "javascript:alert(1)", "mailto:a@b.c"]) {
    assert.strictEqual(f.log.openers[0].open(uriOf(u)), true, u);
  }
  assert.equal(f.log.gate.length, 4);
});

test("the opener is registered once, however many opens share the load", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  await f.loadMonaco();
  assert.equal(f.log.openers.length, 1);
});

test("without the gate loaded the opener still takes the link and opens nothing", { timeout: 4000 }, async () => {
  const f = fake({ noGate: true });
  await f.loadMonaco();
  assert.strictEqual(f.log.openers[0].open(uriOf("https://example.com/")), true);
  assert.deepEqual(f.log.gate, []);
});

test("a Monaco without registerLinkOpener still boots", { timeout: 4000 }, async () => {
  const f = fake({ noOpenerApi: true });
  assert.equal(await f.loadMonaco(), f.win.monaco);
  assert.deepEqual(f.log.openers, []);
});
