"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtTokens, usageTab, usageTitle, fmtCountdown, meterModel } = require("../static/usage.js");

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

test("meterModel maps a window to bar width, percentage and time remaining", () => {
  const now = 1_700_000_000;
  const m = meterModel("5h", { used_percentage: 42.5, resets_at: now + 2 * 3600 + 14 * 60 }, now);
  assert.equal(m.label, "5h");
  assert.equal(m.pct, 43);            // rounded for display
  assert.equal(m.width, "42.5%");     // the BAR keeps the precise value
  assert.equal(m.hot, false);
  assert.equal(m.reset, "2h 14m left");
});

// The producer emits fractional percentages (7.000000000000001 once broke an int
// field server-side). The percentage rounds; the bar does not, so a 42.5% bar is
// not silently redrawn at 43%.
test("meterModel rounds the label but not the bar", () => {
  const now = 1_700_000_000;
  const m = meterModel("wk", { used_percentage: 7.000000000000001, resets_at: now + 60 }, now);
  assert.equal(m.pct, 7);
  assert.equal(m.width, "7.000000000000001%");
});

// The threshold is "past 75%", so 75 exactly is NOT hot.
test("meterModel goes hot past 75 percent, not at it", () => {
  const now = 1_700_000_000;
  const at = (p) => meterModel("5h", { used_percentage: p, resets_at: now + 60 }, now).hot;
  assert.equal(at(74.9), false);
  assert.equal(at(75), false);
  assert.equal(at(75.1), true);
  assert.equal(at(78), true);
});

// Bad data must not paint a bar wider than its track or narrower than nothing.
test("meterModel clamps the bar width to 0..100", () => {
  const now = 1_700_000_000;
  assert.equal(meterModel("5h", { used_percentage: 140, resets_at: now }, now).width, "100%");
  assert.equal(meterModel("5h", { used_percentage: -3, resets_at: now }, now).width, "0%");
});

// An absent window renders no meter at all, never a 0% bar that would read as
// "plenty left" when the truth is "unknown" — applied per window, because
// five_hour and seven_day are independently nullable.
test("meterModel returns null for an absent or malformed window", () => {
  const now = 1_700_000_000;
  assert.equal(meterModel("5h", null, now), null);
  assert.equal(meterModel("5h", undefined, now), null);
  assert.equal(meterModel("5h", {}, now), null);
  assert.equal(meterModel("5h", { used_percentage: "80" }, now), null);
});
