"use strict";
const test = require("node:test");
const assert = require("node:assert");

// viewerprefs.js reads localStorage once per page load and then holds the value in
// memory, so each test loads a fresh copy of the module, as a page load would.
global.localStorage = { _v: null, getItem() { return this._v; }, setItem(_k, v) { this._v = v; } };

const MOD = require.resolve("../static/viewerprefs.js");
function fresh() { delete require.cache[MOD]; return require(MOD).viewerPrefs; }

test("wrap defaults to off", () => {
  global.localStorage._v = null;
  assert.strictEqual(fresh().getWrap(), false);
});

test("setWrap round-trips through getWrap", () => {
  global.localStorage._v = null;
  const viewerPrefs = fresh();
  viewerPrefs.setWrap(true);
  assert.strictEqual(viewerPrefs.getWrap(), true);
  viewerPrefs.setWrap(false);
  assert.strictEqual(viewerPrefs.getWrap(), false);
});

test("a corrupt stored value reads false", () => {
  for (const bad of ["{not json", "null", "42", "\"x\"", "[]", JSON.stringify({ wrap: "yes" }), JSON.stringify({ wrap: 1 })]) {
    global.localStorage._v = bad;
    assert.strictEqual(fresh().getWrap(), false, bad);
  }
});

test("only a stored boolean true reads true", () => {
  global.localStorage._v = JSON.stringify({ wrap: true });
  assert.strictEqual(fresh().getWrap(), true);
});

test("a throwing localStorage reads false and setWrap does not throw", () => {
  const saved = global.localStorage;
  global.localStorage = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
  };
  const p = fresh();
  assert.strictEqual(p.getWrap(), false);
  assert.doesNotThrow(() => p.setWrap(true));
  global.localStorage = saved;
});

test("with storage blocked, the value lives in memory: set(true) reads true, two toggles return to false", () => {
  const saved = global.localStorage;
  global.localStorage = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
  };
  const p = fresh();
  p.setWrap(true);
  assert.strictEqual(p.getWrap(), true);
  p.setWrap(!p.getWrap()); // toggle, as toggleViewerWrap does
  assert.strictEqual(p.getWrap(), false);
  p.setWrap(!p.getWrap());
  assert.strictEqual(p.getWrap(), true);
  p.setWrap(!p.getWrap());
  assert.strictEqual(p.getWrap(), false);
  global.localStorage = saved;
});

test("setWrap coerces to a boolean", () => {
  global.localStorage._v = null;
  const viewerPrefs = fresh();
  viewerPrefs.setWrap(1);
  assert.strictEqual(JSON.parse(global.localStorage._v).wrap, true);
  viewerPrefs.setWrap(0);
  assert.strictEqual(JSON.parse(global.localStorage._v).wrap, false);
});

test("the pre-paint script in index.html reads the same key", () => {
  const html = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/index.html"), "utf8");
  assert.ok(html.includes("'" + fresh().KEY + "'"), "index.html must read " + fresh().KEY);
  assert.ok(html.includes("viewer-wrap"));
});
