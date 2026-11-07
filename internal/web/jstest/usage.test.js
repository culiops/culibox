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

test("usageTitle says so when the count is the size right after a compaction", () => {
  assert.equal(
    usageTitle({ model: "claude-opus-4-8", contextTokens: 25340, postCompact: true }),
    "claude-opus-4-8 · 25,340 tokens (after compact)"
  );
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
  assert.equal(meterModel("5h", { used_percentage: 140, resets_at: now + 60 }, now).width, "100%");
  assert.equal(meterModel("5h", { used_percentage: -3, resets_at: now + 60 }, now).width, "0%");
  // The LABEL is deliberately not clamped: an out-of-range percentage means the
  // producer is wrong, and showing 100% would hide that. Only the bar is clamped,
  // because a bar cannot be drawn wider than its track.
  assert.equal(meterModel("5h", { used_percentage: 140, resets_at: now + 60 }, now).pct, 140);
  assert.equal(meterModel("5h", { used_percentage: -3, resets_at: now + 60 }, now).pct, -3);
});

// Once resets_at has passed, the window has rolled over and the stored percentage
// describes the OLD window. We do not know the new usage, so the meter says
// "reset" with no number and no fill rather than a stale 88% (or a false 0%).
test("meterModel shows a window whose reset time has passed as reset, never the old percentage", () => {
  const now = 1_700_000_000;
  for (const at of [now - 3600, now - 1, now]) {
    const m = meterModel("5h", { used_percentage: 88, resets_at: at }, now);
    assert.equal(m.unknown, true, "resets_at " + (at - now));
    assert.equal(m.pct, null);
    assert.equal(m.width, "0%");
    assert.equal(m.hot, false);
    assert.equal(m.reset, "reset");
  }
  const live = meterModel("5h", { used_percentage: 88, resets_at: now + 1 }, now);
  assert.equal(live.unknown, false);
  assert.equal(live.pct, 88);
});

// resets_at has no omitempty server-side, so a hook that omits it sends 0: that
// is "reset time unknown", which does not prove the window rolled over.
test("meterModel does not call a window with no reset time reset", () => {
  const now = 1_700_000_000;
  const m = meterModel("5h", { used_percentage: 40, resets_at: 0 }, now);
  assert.equal(m.unknown, false);
  assert.equal(m.pct, 40);
  assert.equal(m.reset, "resets now");
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

// F32: at 100% the sidebar says the limit is hit instead of a number that reads like 78%.
// "pct" is the rounded label, so anything the label would draw as 100% or more is the limit.
test("meterModel marks a window at 100 percent or more as the limit, with the reset in the words", () => {
  const now = 1_700_000_000;
  for (const p of [100, 120]) {
    const m = meterModel("5h", { used_percentage: p, resets_at: now + 25 * 60 }, now);
    assert.equal(m.limit, true, "pct " + p);
    assert.equal(m.limitText, "limit reached · resets 25m", "pct " + p);
  }
  const long = meterModel("wk", { used_percentage: 100, resets_at: now + 2 * 3600 + 50 * 60 }, now);
  assert.equal(long.limitText, "limit reached · resets 2h 50m");
});

test("meterModel: a limit with no known reset time says only that the limit is reached", () => {
  const now = 1_700_000_000;
  const m = meterModel("5h", { used_percentage: 100, resets_at: 0 }, now);
  assert.equal(m.limit, true);
  assert.equal(m.limitText, "limit reached");
});

test("meterModel: 99 is not the limit, 99.5 draws as 100% and is", () => {
  const now = 1_700_000_000;
  const at = (p) => meterModel("5h", { used_percentage: p, resets_at: now + 60 }, now);
  assert.equal(at(99).limit, false);
  assert.equal(at(99).limitText, "");
  assert.equal(at(99.4).limit, false);
  assert.equal(at(99.5).pct, 100);
  assert.equal(at(99.5).limit, true);
});

test("meterModel: a window past its reset is not a limit, and the existing fields are untouched at the limit", () => {
  const now = 1_700_000_000;
  const rolled = meterModel("5h", { used_percentage: 100, resets_at: now - 5 }, now);
  assert.equal(rolled.limit, false);
  assert.equal(rolled.limitText, "");
  assert.equal(rolled.unknown, true);
  const m = meterModel("5h", { used_percentage: 100, resets_at: now + 25 * 60 }, now);
  assert.equal(m.pct, 100);
  assert.equal(m.width, "100%");
  assert.equal(m.hot, true);
  assert.equal(m.reset, "25m left"); // the header and the phone band keep this wording
});
