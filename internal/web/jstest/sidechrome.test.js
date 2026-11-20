"use strict";
// #340 S6: the desktop sidebar's chrome (sidechrome.js): the layout button beside New session and its menu,
// and the cost on the foot's links line. The pure helpers are required; the markup and the stylesheet are
// read as text, and app.js / tabs.js functions are cut out of their source by name.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const SC = require("../static/sidechrome.js");

const STATIC = path.join(__dirname, "..", "static");
const read = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");
const html = read("index.html");

test("xlay words for 1, 2 and 4", () => {
  assert.equal(SC.xlayLabel(1), "1 pane");
  assert.equal(SC.xlayLabel(2), "2 panes");
  assert.equal(SC.xlayLabel(4), "4 panes");
  assert.equal(SC.xlayLabel(3), "4 panes", "any other cap reads as 4, clampCap's default");
  assert.equal(SC.xlayTitle(2), "Layout: 2 panes (click to change)");
  assert.equal(SC.xlayTitle(1), "Layout: 1 pane (click to change)");
  assert.equal(SC.xlayAria(4), "Layout: 4 panes");
});

test("xlay shapes: a frame, then one divider per split", () => {
  assert.deepEqual([1, 2, 4].map((n) => SC.xlayShapes(n).length), [1, 2, 3]);
  assert.equal(SC.xlayShapes(1)[0][0], "rect");
  assert.equal(SC.xlayShapes(1)[0][1].rx, "2");
  assert.deepEqual(SC.xlayShapes(2)[1][1].d, "M12 3v18");
  assert.deepEqual(SC.xlayShapes(4).slice(1).map((s) => s[1].d), ["M3 12h18", "M12 3v18"]);
});

test("sideCostLabel: an approximate amount for today, in the Analytics page's format", () => {
  assert.equal(SC.sideCostLabel(454.2).text, "≈ $454 today");
  assert.equal(SC.sideCostLabel(99.5).text, "≈ $99.50 today");
  assert.equal(SC.sideCostLabel(12345).text, "≈ $12.3k today");
  assert.equal(SC.sideCostLabel(0).text, "≈ $0.00 today");
  assert.equal(SC.sideCostLabel(1).title, "Token usage today (UTC) priced at API list rates. A subscription does not bill this.");
  assert.equal(SC.SIDE_COST_TITLE, SC.sideCostLabel(1).title);
});

test("sidechrome.js loads after kitmenu.js and before tabs.js", () => {
  const at = (f) => html.indexOf('<script src="/' + f + '"></script>');
  assert.ok(at("kitmenu.js") > 0 && at("sidechrome.js") > 0 && at("tabs.js") > 0);
  assert.ok(at("kitmenu.js") < at("sidechrome.js"), "after kitmenu.js");
  assert.ok(at("sidechrome.js") < at("tabs.js"), "before tabs.js");
});
