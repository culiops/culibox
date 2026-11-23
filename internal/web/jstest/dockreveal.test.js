// dockreveal.js's pure half: the "Reveal open file" preference, which places an automatic reveal skips, and which tab's file
// the dock wants revealed. The glue is dockreveal-dom.test.js's.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
global.ancestorsBetween = require("../static/viewerpath.js").ancestorsBetween;
const { revealPrefOn, revealPrefSet, revealExcluded, revealWant, REVEAL_PREF_KEY } = require("../static/dockreveal.js");

function store(init) {
  const m = new Map(Object.entries(init || {}));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), m };
}

test("the preference is on by default, off only for \"0\", and survives a storage that throws", () => {
  assert.strictEqual(REVEAL_PREF_KEY, "agent-box:revealOpenFile");
  assert.strictEqual(revealPrefOn(store()), true);
  assert.strictEqual(revealPrefOn(store({ "agent-box:revealOpenFile": "0" })), false);
  assert.strictEqual(revealPrefOn(store({ "agent-box:revealOpenFile": "1" })), true);
  assert.strictEqual(revealPrefOn(null), true);
  assert.strictEqual(revealPrefOn({ getItem() { throw new Error("denied"); } }), true);
  const s = store();
  revealPrefSet(s, false);
  assert.strictEqual(s.m.get(REVEAL_PREF_KEY), "0");
  revealPrefSet(s, true);
  assert.strictEqual(s.m.get(REVEAL_PREF_KEY), "1");
  assert.doesNotThrow(() => revealPrefSet({ setItem() { throw new Error("quota"); } }, true));
});

test("node_modules and bower_components between the root and the file are skipped; the root itself is not judged", () => {
  assert.strictEqual(revealExcluded("/w", "/w/node_modules/x/a.js"), true);
  assert.strictEqual(revealExcluded("/w", "/w/web/bower_components/a.js"), true);
  assert.strictEqual(revealExcluded("/w", "/w/a.js"), false);
  assert.strictEqual(revealExcluded("/w", "/w/src/node_modules.txt"), false, "a file of that name is not a folder above it");
  assert.strictEqual(revealExcluded("/w/node_modules", "/w/node_modules/x/a.js"), false, "a root the user chose inside one is theirs");
  assert.strictEqual(revealExcluded("/w", "/v/node_modules/a.js"), false, "outside the root: not this rule's business");
  assert.strictEqual(revealExcluded("/w", "/w/my_node_modules/a.js"), false, "whole names only");
});

test("a gitignored folder between the root and the file is skipped too; the file itself and the root are not judged", () => {
  const ign = (d) => d === "/w/build" || d === "/w";
  assert.strictEqual(revealExcluded("/w", "/w/build/out/a.js", ign), true);
  assert.strictEqual(revealExcluded("/w", "/w/src/a.js", ign), false);
  assert.strictEqual(revealExcluded("/w", "/w/.env", () => true), false, "an ignored file in a shown folder is revealed");
  assert.strictEqual(revealExcluded("/w", "/w/src/a.js"), false, "no ignored list: as before");
});

test("the file the dock wants revealed: the active tab's host file, nothing for a box file, the All changes tab, a closed dock", () => {
  const tab = (t) => ({ open: true, tab: t });
  assert.strictEqual(revealWant(tab({ key: "k", path: "/w/a.go" })), "/w/a.go");
  assert.strictEqual(revealWant(tab({ key: "k", path: "/w/a.go", box: "s1" })), null);
  assert.strictEqual(revealWant(tab({ key: "r", review: true })), null);
  assert.strictEqual(revealWant(tab(null)), null);
  assert.strictEqual(revealWant(tab({ key: "k", path: "rel/a.go" })), null);
  assert.strictEqual(revealWant({ open: false, tab: { key: "k", path: "/w/a.go" } }), null);
});
