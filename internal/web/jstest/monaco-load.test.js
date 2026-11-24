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
  const log = { required: [], removed: [], themes: 0, openers: [], gate: [], reads: [], fontLoads: [], remeasured: 0 };
  let moduleRan = false; // an AMD module is evaluated once: only the first require() appends the stylesheet link
  const link = () => {
    const el = { tag: "link", rel: "", href: "", sheet: null, listeners: {}, addEventListener(t, fn) { this.listeners[t] = fn; if ((opt.css === "late-load" && t === "load") || (opt.css === "late-error" && t === "error")) setImmediate(fn); }, remove() { log.removed.push(this); head.children = head.children.filter((c) => c !== this); } };
    return el;
  };
  // opt.base: the content of the page's asset-base meta (index.html as the server rewrote it); undefined = no such tag.
  // opt.fonts: "now" (document.fonts.load settles at once), "late" (it settles when f.fontLands() is called) or undefined (no
  // FontFaceSet at all). log.fontLoads holds what was asked for; log.remeasured counts monaco.editor.remeasureFonts().
  let fontLands = () => {};
  const fonts = opt.fonts === undefined ? undefined : { load(spec) { log.fontLoads.push(spec); return opt.fonts === "now" ? Promise.resolve([{}]) : new Promise((r) => { fontLands = () => r([{}]); }); } };
  const doc = { head, fonts, createElement: (tag) => (tag === "link" ? link() : { tag, src: "" }), querySelector: (sel) => (sel === 'meta[name="culibox-asset-base"]' && opt.base !== undefined ? { content: opt.base } : null) };
  const monaco = {
    editor: { defineTheme() { log.themes++; }, addKeybindingRule() {}, remeasureFonts() { log.remeasured++; }, registerLinkOpener(o) { log.openers.push(o); return { dispose() {} }; } },
    KeyCode: { F1: 59 },
    languages: {},
  };
  if (opt.noOpenerApi) delete monaco.editor.registerLinkOpener;
  // opt.models: the text of each open model; opt.Uri: Monaco's URI class. log.reads counts getValue() calls (a scan reads a model once).
  if (opt.Uri) monaco.Uri = opt.Uri;
  if (opt.models) monaco.editor.getModels = () => opt.models.map((t, i) => ({ getValueLength: () => t.length, getValue: () => { log.reads.push(i); return t; } }));
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
    }, { config(c) { log.config = c; } }),
  });
  if (opt.withLinks) new vm.Script(fs.readFileSync(path.join(__dirname, "..", "static", "links.js"), "utf8"), { filename: "links.js" }).runInContext(ctx);
  new vm.Script(SRC, { filename: "monacoload.js" }).runInContext(ctx);
  const loadMonaco = new vm.Script("loadMonaco", { filename: "get" }).runInContext(ctx);
  return { ctx, head, timers, log, loadMonaco, win, fontLands: () => fontLands() };
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

test("with no open file holding the text, a space or a non-ASCII letter reaches the gate percent-encoded (the fallback)", { timeout: 4000 }, async () => {
  const f = fake();
  await f.loadMonaco();
  f.log.openers[0].open(monacoUri("https://example.com/a b/é"));
  assert.equal(f.log.gate[0][0], "https://example.com/a%20b/%C3%A9");
});

// The real URI class of the vendored Monaco (an AMD chunk: define(name, deps, factory) fills `exports`), so the test sees what its
// parse() does to a link and what toString() gives back. Loaded once, in its own vm context.
const URI_CHUNK = (() => {
  const dir = path.join(__dirname, "..", "static", "vendor", "monaco", "vs");
  return path.join(dir, fs.readdirSync(dir).find((n) => /^editorWorkerHost-.*\.js$/.test(n)));
})();
const realUri = (() => {
  const ex = {};
  const g = { navigator: { userAgent: "node", platform: "linux", language: "en", languages: ["en"] }, console, performance, setTimeout, clearTimeout, queueMicrotask, TextDecoder, TextEncoder, URL };
  g.self = g; g.globalThis = g;
  const c = vm.createContext(g);
  c.define = (_n, _d, factory) => { factory(null, ex); };
  vm.runInContext(fs.readFileSync(URI_CHUNK, "utf8"), c, { filename: "editorWorkerHost.js" });
  return ex.URI;
})();

test("the vendored URI class is the one the tests below stand on: parse decodes, toString encodes", () => {
  assert.equal(realUri.parse("https://ex.com/a%3Fb").toString(true), "https://ex.com/a%3Fb");
  assert.equal(realUri.parse("https://ex.com/a?x=1").toString(), "https://ex.com/a?x%3D1");
  assert.equal(encodeURI(realUri.parse("https://ex.com/a%3Fb").toString(true)), "https://ex.com/a%253Fb", "the lossy way: a %3F becomes %253F");
});

// encodeURI(uri.toString(true)) is lossy: URI.parse decodes every part, so a link that carries a %3F, %23 or an encoded nested query
// opens as a different link. Monaco's own click hands its opener the original text, but registerLinkOpener's wrapper parses it first, so
// the opener only gets the URI. The opener therefore finds the text again in the open models (a URI that parses back to the same URI) and
// hands the gate that, exactly as written.
const LOSSY = [
  "https://ex.com/a%3Fb",
  "https://ex.com/a%23b",
  "https://ex.com/login?continue=https%3A%2F%2Fapp.example.com%2Fdash%3Ftab%3D2%26x%3D1&hl=en",
  "https://evil.com%2F@good.com/x",
];

test("the gate gets the link exactly as written in the file, for a link a parse would change", { timeout: 4000 }, async () => {
  for (const link of LOSSY) {
    const f = fake({ Uri: realUri, models: ["// see " + link + " for more\nhttps://other.example/x\n"] });
    await f.loadMonaco();
    assert.strictEqual(f.log.openers[0].open(realUri.parse(link)), true);
    assert.deepEqual(f.log.gate, [[link, { source: "editor" }]], link);
  }
});

test("the written text is trimmed like the terminal's: a trailing full stop, and a ) the link did not open", { timeout: 4000 }, async () => {
  const link = "https://ex.com/a%3Fb";
  const f = fake({ Uri: realUri, models: ["(see " + link + "). Then " + link + ", or [x](" + link + ")"] });
  await f.loadMonaco();
  f.log.openers[0].open(realUri.parse(link));
  assert.equal(f.log.gate[0][0], link);
});

test("with links.js on the page the scan uses its URL regex and trimmer, and finds the same links", { timeout: 4000 }, async () => {
  for (const link of LOSSY) {
    const f = fake({ withLinks: true, Uri: realUri, models: ["see (" + link + "). and more"] });
    await f.loadMonaco();
    f.log.openers[0].open(realUri.parse(link));
    assert.equal(f.log.gate[0][0], link, link);
    assert.equal(vm.runInContext("typeof trimUrl", f.ctx), "function", "links.js really was loaded in this page");
  }
});

test("the first model that holds the link is read and the scan stops there", { timeout: 4000 }, async () => {
  const link = "https://ex.com/a%3Fb";
  const f = fake({ Uri: realUri, models: ["nothing here", "x " + link, "y " + link] });
  await f.loadMonaco();
  f.log.openers[0].open(realUri.parse(link));
  assert.deepEqual(f.log.reads, [0, 1], "the third model is never read");
  assert.equal(f.log.gate[0][0], link);
});

test("a model larger than 5 MB is skipped, not read", { timeout: 4000 }, async () => {
  const link = "https://ex.com/a%3Fb";
  const big = link + " " + "x".repeat(5 * 1024 * 1024);
  const f = fake({ Uri: realUri, models: [big, "ok " + link] });
  await f.loadMonaco();
  f.log.openers[0].open(realUri.parse(link));
  assert.deepEqual(f.log.reads, [1]);
  assert.equal(f.log.gate[0][0], link);
});

test("no model holds the link: the gate gets encodeURI(toString(true)), as before", { timeout: 4000 }, async () => {
  for (const models of [undefined, [], ["no links in here"], ["https://other.example/x"]]) {
    const f = fake({ Uri: realUri, models });
    await f.loadMonaco();
    f.log.openers[0].open(realUri.parse("https://ex.com/login?next=https://evil.com/x"));
    assert.equal(f.log.gate[0][0], "https://ex.com/login?next=https://evil.com/x");
  }
});

test("a scan that throws, or a Monaco with no getModels or Uri, falls back and still takes the link", { timeout: 4000 }, async () => {
  const link = "https://ex.com/a?x=1";
  const bad = fake({ Uri: { parse() { throw new Error("boom"); } }, models: ["see " + link] });
  await bad.loadMonaco();
  assert.strictEqual(bad.log.openers[0].open(realUri.parse(link)), true);
  assert.equal(bad.log.gate[0][0], link);
  const none = fake({ Uri: realUri });
  await none.loadMonaco();
  assert.strictEqual(none.log.openers[0].open(realUri.parse(link)), true);
  assert.equal(none.log.gate[0][0], link);
});

test("nothing is scanned at boot: only a click reads the models", { timeout: 4000 }, async () => {
  const f = fake({ Uri: realUri, models: ["https://ex.com/a%3Fb"] });
  await f.loadMonaco();
  assert.deepEqual(f.log.reads, []);
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

test("the loader, the editor module's base and the stylesheet come from the build's versioned base the page names", { timeout: 4000 }, async () => {
  const f = fake({ base: "/v/0123456789abcdef", css: "applied" });
  await f.loadMonaco();
  const script = f.head.children.find((c) => c.tag === "script");
  assert.equal(script.src, "/v/0123456789abcdef/vendor/monaco/vs/loader.js");
  assert.deepEqual(JSON.parse(JSON.stringify(f.log.config)), { paths: { vs: "/v/0123456789abcdef/vendor/monaco/vs" } });
});

test("a page with no asset base, or one that is not /v/<16 hex>, loads from the unversioned /vendor/monaco/vs", { timeout: 4000 }, async () => {
  for (const base of [undefined, "", "//evil.example", "https://evil.example/v/0123456789abcdef", "/v/../x", "/v/0123456789abcdeg", "/v/0123456789abcdef/", "javascript:alert(1)", "/v/0123456789ABCDEF"]) {
    const f = fake({ base, css: "applied" });
    await f.loadMonaco();
    assert.equal(f.head.children.find((c) => c.tag === "script").src, "/vendor/monaco/vs/loader.js", String(base));
    assert.equal(f.log.config.paths.vs, "/vendor/monaco/vs", String(base));
  }
});

test("a stylesheet the loader has to add itself comes from the versioned base too", { timeout: 4000 }, async () => {
  const f = fake({ base: "/v/0123456789abcdef", css: "late-load" });
  // A module that does not append its stylesheet: the loader appends one.
  f.ctx.require = Object.assign((deps, ok) => setImmediate(() => ok()), { config() {} });
  await f.loadMonaco();
  const l = f.head.children.find((c) => c.tag === "link");
  assert.equal(l.href, "/v/0123456789abcdef/vendor/monaco/vs/editor/editor.main.css");
});

// The dock's code font (P5): Monaco measures its font when an editor is made and never again on its own (no FontFaceSet listener in
// 0.57.0, read), so an editor made before JetBrains Mono arrives would lay the cursor out on the fallback's widths.
test("the load waits for the code font before any editor is made, and asks for JetBrains Mono", { timeout: 4000 }, async () => {
  const f = fake({ fonts: "now" });
  await f.loadMonaco();
  assert.deepEqual(f.log.fontLoads, ['400 13px "JetBrains Mono"']);
  assert.equal(f.log.remeasured, 0, "in time: nothing to remeasure");
  assert.equal(live(f).length, 0, "the font wait's timer is cleared");
});

test("a font slower than the wait does not hold the editor back; when it lands, the editors measure again, once", { timeout: 4000 }, async () => {
  const f = fake({ fonts: "late" });
  const p = f.loadMonaco();
  let settled = false;
  p.then(() => { settled = true; });
  for (let i = 0; i < 20 && !live(f).some((t) => t.ms === 1500); i++) await new Promise((r) => setImmediate(r));
  const wait = live(f).find((t) => t.ms === 1500);
  assert.ok(wait, "a 1500 ms wait for the font");
  assert.equal(settled, false, "the load waits for it");
  wait.dead = true; wait.fn();
  await p;
  assert.equal(f.log.remeasured, 0);
  f.fontLands();
  await new Promise((r) => setImmediate(r));
  assert.equal(f.log.remeasured, 1);
});

test("a page with no FontFaceSet loads as before", { timeout: 4000 }, async () => {
  const f = fake();
  assert.equal(await f.loadMonaco(), f.win.monaco);
  assert.equal(f.log.fontLoads.length, 0);
});

test("the font is asked for before the editor's script, so the two download side by side", { timeout: 4000 }, async () => {
  const f = fake({ fonts: "now" });
  let fontsAtRequire = -1;
  const req = f.ctx.require;
  f.ctx.require = Object.assign((...a) => { fontsAtRequire = f.log.fontLoads.length; return req(...a); }, { config: req.config });
  await f.loadMonaco();
  assert.equal(fontsAtRequire, 1);
});
