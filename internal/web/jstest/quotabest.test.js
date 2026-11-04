"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { acctOptionLabel, acctBestLimits } = require("../static/accounts.js");

// acctBestLimits: whether the quota is per account or per model is not known, so
// each window shows the HIGHEST reading still inside its window. Within one
// window usage only rises: per account that is the freshest reading, per model it
// is the most constraining limit. It never understates.
const NOW = 1000;
const win = (pct, resets) => ({ used_percentage: pct, resets_at: resets });

test("acctBestLimits per-account case: a fresh higher top level wins over an older bucket", () => {
  const lim = {
    updated_at: 990, five_hour: win(40, 5000), seven_day: win(80, 9000),
    by_model: { opus: { updated_at: 900, five_hour: win(30, 5000), seven_day: win(70, 9000) } },
  };
  const b = acctBestLimits(lim, NOW);
  assert.equal(b.five_hour.used_percentage, 40);
  assert.equal(b.seven_day.used_percentage, 80);
  assert.equal(b.updated_at, 990); // both windows come from the top-level reading
});

test("acctBestLimits per-model case: an older bucket still in its window beats a fresh lower top level", () => {
  // opus session wrote last (top level); the haiku bucket reported 94% for the week.
  const lim = {
    updated_at: 990, five_hour: win(9, 5000), seven_day: win(76, 9000),
    by_model: {
      opus: { updated_at: 990, five_hour: win(9, 5000), seven_day: win(76, 9000) },
      haiku: { updated_at: 700, five_hour: win(0, 4000), seven_day: win(94, 8000) },
    },
  };
  const b = acctBestLimits(lim, NOW);
  assert.equal(b.five_hour.used_percentage, 9);
  assert.equal(b.seven_day.used_percentage, 94);
  assert.equal(b.seven_day.resets_at, 8000); // the winning reading's own reset time
  assert.equal(b.updated_at, 700);           // age is the oldest reading shown
});

test("acctBestLimits ignores a reading whose window has already reset", () => {
  const lim = {
    updated_at: 990, five_hour: win(9, 5000), seven_day: win(76, 9000),
    by_model: { haiku: { updated_at: 100, five_hour: win(99, 900), seven_day: win(94, NOW) } },
  };
  const b = acctBestLimits(lim, NOW);
  assert.equal(b.five_hour.used_percentage, 9);
  assert.equal(b.seven_day.used_percentage, 76);
  assert.equal(b.updated_at, 990);
});

test("acctBestLimits uses a live bucket when the top level has reset, and keeps a reset window when none is live", () => {
  const live = { updated_at: 990, five_hour: win(50, 500), by_model: { haiku: { updated_at: 800, five_hour: win(20, 5000) } } };
  assert.equal(acctBestLimits(live, NOW).five_hour.used_percentage, 20);
  // nothing live: the window is returned as is, so the meter renders it as reset
  const dead = { updated_at: 990, five_hour: win(50, 500), by_model: { haiku: { updated_at: 800, five_hour: win(20, 600) } } };
  const d = acctBestLimits(dead, NOW);
  assert.equal(d.five_hour.used_percentage, 50);
  assert.equal(d.five_hour.resets_at, 500);
});

test("acctBestLimits: a bucket without a window does not invent it; legacy and absent data pass through", () => {
  const lim = { updated_at: 990, five_hour: win(9, 5000), by_model: { opus: { updated_at: 990, five_hour: win(9, 5000) } } };
  assert.equal(acctBestLimits(lim, NOW).seven_day, undefined);
  const legacy = { updated_at: 5, five_hour: win(1, 9000) };
  assert.equal(acctBestLimits(legacy, NOW), legacy);
  assert.equal(acctBestLimits(null, NOW), null);
});

test("acctBestLimits tolerates malformed buckets", () => {
  const lim = { updated_at: 990, five_hour: win(9, 5000), by_model: { a: null, b: "x", c: { five_hour: { used_percentage: "9" } } } };
  assert.equal(acctBestLimits(lim, NOW).five_hour.used_percentage, 9);
});

test("acctOptionLabel (the spawn picker) shows the same reading as the header", () => {
  const lim = {
    updated_at: NOW - 10, five_hour: win(9, 5000), seven_day: win(76, 9000),
    by_model: { haiku: { updated_at: NOW - 60, five_hour: win(0, 4000), seven_day: win(94, 8000) } },
  };
  assert.equal(acctOptionLabel("work", lim, NOW), "work · 5h 9% · wk 94%");
});
