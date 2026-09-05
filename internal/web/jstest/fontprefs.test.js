"use strict";
const test = require("node:test");
const assert = require("node:assert");

// fontprefs.js touches localStorage + document at module load; stub minimally.
global.localStorage = { _v: null, getItem() { return this._v; }, setItem(_k, v) { this._v = v; } };
global.document = { documentElement: { style: { setProperty() {} } } };

const { fontPrefs } = require("../static/fontprefs.js");

test("desktop defaults are unchanged", () => {
  const d = fontPrefs.load(false);
  assert.equal(d.terminal, 13);
  assert.equal(d.code, 12.5);
  assert.equal(d.markdown, 14);
  assert.equal(d.filetree, 12);
});

test("mobile defaults are larger than desktop (M9 measured values)", () => {
  const mo = fontPrefs.load(true);
  assert.equal(mo.terminal, 15);
  assert.equal(mo.code, 13);
  assert.equal(mo.markdown, 15);
  assert.equal(mo.filetree, 14);
});

test("a stored value wins over either default set", () => {
  global.localStorage._v = JSON.stringify({ terminal: 20 });
  assert.equal(fontPrefs.load(true).terminal, 20);
  assert.equal(fontPrefs.load(false).terminal, 20);
  global.localStorage._v = null;
});

test("reset() picks mobile or desktop defaults by current window width", () => {
  global.window = { innerWidth: 390 };
  fontPrefs.reset();
  assert.equal(fontPrefs.get("terminal"), 15);
  assert.equal(fontPrefs.get("code"), 13);
  assert.equal(fontPrefs.get("markdown"), 15);
  assert.equal(fontPrefs.get("filetree"), 14);

  global.window.innerWidth = 1440;
  fontPrefs.reset();
  assert.equal(fontPrefs.get("terminal"), 13);
  assert.equal(fontPrefs.get("code"), 12.5);
  assert.equal(fontPrefs.get("markdown"), 14);
  assert.equal(fontPrefs.get("filetree"), 12);

  delete global.window;
});
