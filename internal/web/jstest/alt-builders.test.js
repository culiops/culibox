"use strict";
// The #53 "other checkouts hold this file" note and pick list are drawn in one place, viewer-tab.js (altNoteNodes,
// altPickNode), for both the phone's ViewerTab and the dock's EditorTab. The real functions run in a vm against the fake page
// (fakedom.js); the two classes are pinned to call them, so a second copy of the markup cannot come back.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");

const STATIC = path.join(__dirname, "..", "static");
const src = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const stripComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");

function page() {
  const doc = { activeElement: null };
  doc.createElement = (t) => new Node(t, doc);
  const ctx = vm.createContext({ console, Array, Object, Set, Map, Math, Number, JSON, Date, document: doc, window: { addEventListener() {} } });
  for (const f of ["viewerpath.js", "viewer-tab.js"]) new vm.Script(src(f), { filename: f }).runInContext(ctx);
  return ctx;
}

const NOW = 1_700_000_000;
const items = [
  { path: "/r/wt-a/x.go", worktree: "wt-a", mtime: NOW - 120 },
  { path: "/r/x.go", worktree: "", mtime: 0 },
];

test("altPickNode: the title, what was not found, and a button per copy with its name and age", () => {
  const ctx = page();
  const real = Date.now;
  Date.now = () => NOW * 1000;
  let box;
  const picked = [];
  try { box = ctx.altPickNode(items, { base: "/r", rel: "x.go" }, (it) => picked.push(it)); } finally { Date.now = real; }
  assert.equal(box.className, "viewer-msg viewer-alts");
  assert.equal(box.children[0].className, "viewer-alts-title");
  assert.equal(box.children[0].textContent, "This file exists in 2 places");
  assert.equal(box.children[1].textContent, "x.go was not found here. Pick the copy to open:");
  const btns = box.children.slice(2);
  assert.equal(btns.length, 2);
  assert.deepEqual(btns.map((b) => b.title), ["/r/wt-a/x.go", "/r/x.go"]);
  assert.deepEqual(btns.map((b) => b.querySelector(".viewer-alt-name").textContent), ["wt-a", "main checkout"]);
  assert.equal(btns[0].querySelector(".viewer-alt-ago").textContent, "2 min ago");
  assert.equal(btns[1].querySelector(".viewer-alt-ago"), null, "no mtime, no age");
  btns[1].click();
  btns[0].click();
  assert.deepEqual(picked, [items[1], items[0]], "a press hands back the item it was drawn from");
});

test("altPickNode: names and paths are text, never markup", () => {
  const ctx = page();
  const evil = '<img src=x onerror="alert(1)">';
  const box = ctx.altPickNode([{ path: "/r/" + evil, worktree: evil, mtime: 0 }], { base: "/r", rel: evil }, () => {});
  const all = [];
  const walk = (n) => { all.push(n); n.children.forEach(walk); };
  walk(box);
  assert.ok(all.every((n) => ["div", "button", "span"].includes(n.tag)), "only the elements the builder makes: " + all.map((n) => n.tag).join(","));
  assert.ok(box.textContent.includes(evil), "the hostile name is shown as text");
});

test("altNoteNodes: the sentence for the main checkout, or with the worktree's name in <code>", () => {
  const ctx = page();
  const main = ctx.altNoteNodes("");
  assert.deepEqual(main, ["Opened from the main checkout — not found in this worktree."]);
  const wt = ctx.altNoteNodes("wt-a");
  assert.equal(wt.length, 3);
  assert.equal(wt[0], "Opened from worktree ");
  assert.equal(wt[1].tag, "code");
  assert.equal(wt[1].textContent, "wt-a");
  assert.equal(wt[2], " — not found here.");
  const evil = ctx.altNoteNodes("<b>x</b>");
  assert.equal(evil[1].textContent, "<b>x</b>");
  assert.equal(evil[1].children.length, 0, "a name with markup in it makes no element");
});

test("ViewerTab and EditorTab both draw the note and the list through the shared builders, and nothing else spells that markup", () => {
  const vt = stripComments(src("viewer-tab.js"));
  const et = stripComments(src("editortab.js"));
  assert.match(vt, /this\.noteEl\.replaceChildren\(\.\.\.altNoteNodes\(worktree\)\)/);
  assert.match(vt, /altPickNode\(items, alt, \(it\) => this\.openAlternate\(it\.path, it\.worktree, line\)\)/);
  assert.match(et, /this\.altEl\.replaceChildren\(\.\.\.altNoteNodes\(worktree\)\)/);
  assert.match(et, /altPickNode\(items, alt, \(it\) => this\.openAlternate\(it\.path, it\.worktree, line\)\)/);
  const holders = fs.readdirSync(STATIC).filter((n) => n.endsWith(".js") && /viewer-alt-name/.test(stripComments(src(n))));
  assert.deepEqual(holders, ["viewer-tab.js"], "the pick list's markup is built in one file");
  assert.equal((stripComments(src("viewer-tab.js")).match(/viewer-alt-name/g) || []).length, 1, "and once in it");
});
