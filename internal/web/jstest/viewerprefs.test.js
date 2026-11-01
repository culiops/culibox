"use strict";
const test = require("node:test");
const assert = require("node:assert");

// viewerprefs.js reads localStorage lazily; stub it per test.
global.localStorage = { _v: null, getItem() { return this._v; }, setItem(_k, v) { this._v = v; } };

const { viewerPrefs } = require("../static/viewerprefs.js");

test("wrap defaults to off", () => {
  global.localStorage._v = null;
  assert.strictEqual(viewerPrefs.getWrap(), false);
});

test("setWrap round-trips through getWrap", () => {
  global.localStorage._v = null;
  viewerPrefs.setWrap(true);
  assert.strictEqual(viewerPrefs.getWrap(), true);
  viewerPrefs.setWrap(false);
  assert.strictEqual(viewerPrefs.getWrap(), false);
});

test("a corrupt stored value reads false", () => {
  for (const bad of ["{not json", "null", "42", "\"x\"", "[]", JSON.stringify({ wrap: "yes" }), JSON.stringify({ wrap: 1 })]) {
    global.localStorage._v = bad;
    assert.strictEqual(viewerPrefs.getWrap(), false, bad);
  }
});

test("only a stored boolean true reads true", () => {
  global.localStorage._v = JSON.stringify({ wrap: true });
  assert.strictEqual(viewerPrefs.getWrap(), true);
});

test("a throwing localStorage reads false and setWrap does not throw", () => {
  const saved = global.localStorage;
  global.localStorage = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
  };
  assert.strictEqual(viewerPrefs.getWrap(), false);
  assert.doesNotThrow(() => viewerPrefs.setWrap(true));
  global.localStorage = saved;
});

test("setWrap coerces to a boolean", () => {
  global.localStorage._v = null;
  viewerPrefs.setWrap(1);
  assert.strictEqual(JSON.parse(global.localStorage._v).wrap, true);
  viewerPrefs.setWrap(0);
  assert.strictEqual(JSON.parse(global.localStorage._v).wrap, false);
});

test("the pre-paint script in index.html reads the same key", () => {
  const html = require("node:fs").readFileSync(require("node:path").join(__dirname, "../static/index.html"), "utf8");
  assert.ok(html.includes("'" + viewerPrefs.KEY + "'"), "index.html must read " + viewerPrefs.KEY);
  assert.ok(html.includes("viewer-wrap"));
});
