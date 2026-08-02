"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtTokens, usageTab, usageTitle } = require("../static/usage.js");

test("fmtTokens", () => {
  assert.equal(fmtTokens(0), "0");
  assert.equal(fmtTokens(999), "999");
  assert.equal(fmtTokens(597348), "597k");
  assert.equal(fmtTokens(1_000_000), "1.0M");
});

test("usageTab shows raw tokens; pct only when present", () => {
  assert.equal(usageTab({ contextTokens: 597348 }), "597k");
  assert.equal(usageTab({ contextTokens: 600000, contextMax: 1000000, pct: 60 }), "600k · 60%");
  assert.equal(usageTab(null), "");
  assert.equal(usageTab({}), "");
});

test("usageTitle lists model·effort·exact", () => {
  assert.equal(
    usageTitle({ model: "claude-opus-4-8", effort: "high", contextTokens: 597348 }),
    "claude-opus-4-8 · high · 597,348 tokens"
  );
  assert.equal(usageTitle(null), "");
});
