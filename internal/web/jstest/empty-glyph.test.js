"use strict";
// The two full-surface empty states of the pane area (G4 90): "No session open" and the first-run welcome draw a 32 px
// muted hero glyph before the title, chosen by the declared kind (emptyState, iconmap.js), never by the words.
// app.js needs a browser and cannot be require()d, so emptyEl / emptyNoneEl / emptyFirstEl are cut out of its source
// and run against a tiny fake DOM, beside the real icons.js / iconmap.js / svgicon.js.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const APP = read("app.js");

function fnSource(src, name) {
  const m = new RegExp("(?:async )?function " + name + "\\s*\\(").exec(src);
  assert.ok(m, name + " not found");
  const open = src.indexOf("{", src.indexOf(")", m.index));
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return src.slice(m.index, i + 1);
  }
  throw new Error("unbalanced braces in " + name);
}

function page() {
  const doc = { createElement: (t) => new Node(t, doc), createElementNS: (ns, t) => new Node(t, doc) };
  const ctx = vm.createContext({
    document: doc, console, profiles: [], boxRuntime: true, boxUnavailableReason: "", boxDaemonReason: "", metaLoaded: true,
    drawerDesktop: () => true, openSpawnDialog() {}, openSettings() {}, $: () => new Node("div", doc), setDisabledReason() {},
    shortcutHint: () => "Ctrl+Shift+K", IS_MAC: false,
  });
  for (const f of ["icons.js", "iconmap.js", "svgicon.js"]) new vm.Script(read(f).replace(/^const /gm, "var "), { filename: f }).runInContext(ctx);
  vm.runInContext("function emptyHint() { return document.createElement('div'); }\n" + ["emptyEl", "emptyHero", "emptyNoneEl", "emptyFirstEl"].map((n) => fnSource(APP, n)).join("\n"), ctx);
  return ctx;
}

const glyphOf = (root) => root.children[0];

test("No session open: a 32 px app-window hero comes first, then the title, in the same words", () => {
  const root = page().emptyNoneEl({ kind: "none", profiles: [] });
  const g = glyphOf(root);
  assert.strictEqual(g.tag, "svg");
  assert.strictEqual(g.attrs["data-icon"], "app-window");
  assert.strictEqual(g.attrs.width, "32");
  assert.strictEqual(g.attrs.height, "32");
  assert.strictEqual(g.attrs["aria-hidden"], "true");
  assert.match(g.attrs.class, /\bempty-glyph\b/);
  assert.match(g.attrs.class, /\bempty-hero\b/);
  assert.strictEqual(root.children[1].tag, "h1");
  assert.strictEqual(root.children[1].textContent, "No session open");
  assert.strictEqual(root.children[2].textContent, "Pick a session on the left, or start an agent from a profile you used recently.");
});

test("first run: a 32 px box hero before the welcome title; the steps are untouched", () => {
  const root = page().emptyFirstEl();
  const g = glyphOf(root);
  assert.strictEqual(g.attrs["data-icon"], "box");
  assert.strictEqual(g.attrs.width, "32");
  assert.match(g.attrs.class, /\bempty-hero\b/);
  assert.strictEqual(root.children[1].textContent, "Welcome to CuliBox");
  assert.strictEqual(root.children[3].className, "empst-steps");
});

test("the hero is drawn from the declared kind, not from the message text", () => {
  const src = fnSource(APP, "emptyNoneEl") + fnSource(APP, "emptyFirstEl");
  assert.match(src, /emptyHero\("none"\)/);
  assert.match(src, /emptyHero\("first"\)/);
  assert.match(fnSource(APP, "emptyHero"), /emptyState\(kind\)/);
});
