"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtTokens, usageTab, usageTitle, fmtCountdown, rateLimitText } = require("../static/usage.js");

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

test("fmtCountdown", () => {
  const now = 1000000;
  assert.equal(fmtCountdown(now, now), "now");
  assert.equal(fmtCountdown(now - 5, now), "now");
  assert.equal(fmtCountdown(now + 13 * 60, now), "13m");
  assert.equal(fmtCountdown(now + 2 * 3600 + 50 * 60, now), "2h 50m");
  assert.equal(fmtCountdown(now + 5 * 86400 + 7 * 3600, now), "5d 7h");
});

test("rateLimitText composes label·pct·countdown; blank when absent", () => {
  const now = 1000000;
  assert.equal(rateLimitText("5h", { used_percentage: 80, resets_at: now + 2 * 3600 }, now), "5h 80% · 2h 0m");
  assert.equal(rateLimitText("week", null, now), "");
});
