// The F6 / Shift+F6 region cycle (#177, plan call 12; frame M8.8): which regions are on screen, in which order,
// and which one a step lands on. Pure: the DOM binding in app.js only collects the booleans and moves focus.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const R = require("../static/regioncycle.js");

const ALL = { sidebar: true, rail: false, header: true, terminal: true, drawer: true, page: false };
const v = (o) => Object.assign({}, ALL, o);

test("order: sidebar, pane header + strip, terminal, drawer", () => {
  assert.deepStrictEqual(R.regionOrder(ALL), ["sidebar", "header", "terminal", "drawer"]);
});

test("order: hidden regions are skipped", () => {
  assert.deepStrictEqual(R.regionOrder(v({ drawer: false })), ["sidebar", "header", "terminal"]);
  assert.deepStrictEqual(R.regionOrder(v({ sidebar: false, drawer: false })), ["header", "terminal"]);
  assert.deepStrictEqual(R.regionOrder(v({ header: false })), ["sidebar", "terminal", "drawer"]);
  assert.deepStrictEqual(R.regionOrder(v({ header: false, terminal: false, drawer: false })), ["sidebar"]);
  assert.deepStrictEqual(R.regionOrder({}), []);
});

test("order: the collapsed rail stands in for the sidebar", () => {
  assert.deepStrictEqual(R.regionOrder(v({ sidebar: false, rail: true })), ["rail", "header", "terminal", "drawer"]);
  // The sidebar wins if both are somehow reported.
  assert.deepStrictEqual(R.regionOrder(v({ rail: true }))[0], "sidebar");
});

test("order: an open page replaces the pane header and the terminal", () => {
  assert.deepStrictEqual(R.regionOrder(v({ page: true })), ["sidebar", "page", "drawer"]);
  assert.deepStrictEqual(R.regionOrder(v({ page: true, sidebar: false, rail: true, drawer: false })), ["rail", "page"]);
});

test("step forward wraps", () => {
  const o = R.regionOrder(ALL);
  assert.strictEqual(R.regionStep(o, "sidebar", false), "header");
  assert.strictEqual(R.regionStep(o, "header", false), "terminal");
  assert.strictEqual(R.regionStep(o, "terminal", false), "drawer");
  assert.strictEqual(R.regionStep(o, "drawer", false), "sidebar");
});

test("step back (Shift+F6) wraps the other way", () => {
  const o = R.regionOrder(ALL);
  assert.strictEqual(R.regionStep(o, "drawer", true), "terminal");
  assert.strictEqual(R.regionStep(o, "terminal", true), "header");
  assert.strictEqual(R.regionStep(o, "header", true), "sidebar");
  assert.strictEqual(R.regionStep(o, "sidebar", true), "drawer");
});

test("step from nowhere: forward lands on the first region, back on the last", () => {
  const o = R.regionOrder(ALL);
  assert.strictEqual(R.regionStep(o, null, false), "sidebar");
  assert.strictEqual(R.regionStep(o, null, true), "drawer");
  // A region that is no longer on screen counts as nowhere (the drawer closed while it held focus).
  assert.strictEqual(R.regionStep(R.regionOrder(v({ drawer: false })), "drawer", false), "sidebar");
  assert.strictEqual(R.regionStep(R.regionOrder(v({ drawer: false })), "drawer", true), "terminal");
});

test("step: one region stays put, none gives null", () => {
  assert.strictEqual(R.regionStep(["terminal"], "terminal", false), "terminal");
  assert.strictEqual(R.regionStep(["terminal"], "terminal", true), "terminal");
  assert.strictEqual(R.regionStep([], null, false), null);
});

test("step skips with a page open", () => {
  const o = R.regionOrder(v({ page: true }));
  assert.strictEqual(R.regionStep(o, "sidebar", false), "page");
  assert.strictEqual(R.regionStep(o, "page", false), "drawer");
  assert.strictEqual(R.regionStep(o, "page", true), "sidebar");
});
