"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { fmtCompact, tokensPerPrompt, dayTotals, modelColor } = require("../static/analytics.js");

test("fmtCompact: units at the right thresholds", () => {
  assert.strictEqual(fmtCompact(0), "0");
  assert.strictEqual(fmtCompact(999), "999");
  assert.strictEqual(fmtCompact(1000), "1k");
  assert.strictEqual(fmtCompact(651000), "651k");
  assert.strictEqual(fmtCompact(1_500_000), "1.5M");
  assert.strictEqual(fmtCompact(undefined), "0");
});

test("tokensPerPrompt: divides, and never divides by zero", () => {
  assert.strictEqual(tokensPerPrompt(300, 4), 75);
  assert.strictEqual(tokensPerPrompt(300, 0), 0);
  assert.strictEqual(tokensPerPrompt(0, 0), 0);
});

test("dayTotals: folds per-(day,model) rows into stacks + window totals", () => {
  const rows = [
    { day: "2026-08-04", model: "m1", input: 100, output: 20, cacheRead: 0, cacheCreation: 0, prompts: 2 },
    { day: "2026-08-05", model: "m1", input: 50, output: 10, cacheRead: 0, cacheCreation: 0, prompts: 1 },
    { day: "2026-08-05", model: "m2", input: 200, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 3 },
  ];
  const agg = dayTotals(rows);
  assert.deepStrictEqual(agg.models, ["m1", "m2"]);
  assert.strictEqual(agg.days.length, 2);
  // Sorted ascending by day.
  assert.strictEqual(agg.days[0].day, "2026-08-04");
  assert.strictEqual(agg.days[0].total, 120);
  // 2026-08-05 stacks both models: 60 + 200 = 260, the window max.
  assert.strictEqual(agg.days[1].total, 260);
  assert.strictEqual(agg.days[1].segs.m1, 60);
  assert.strictEqual(agg.days[1].segs.m2, 200);
  assert.strictEqual(agg.maxTotal, 260);
  assert.strictEqual(agg.totals.tokens, 380);
  assert.strictEqual(agg.totals.prompts, 6);
});

test("dayTotals: empty input yields no days and zero totals", () => {
  const agg = dayTotals(null);
  assert.strictEqual(agg.days.length, 0);
  assert.strictEqual(agg.maxTotal, 0);
  assert.strictEqual(agg.totals.tokens, 0);
});

test("modelColor: stable per model index, wraps the palette", () => {
  const models = ["a", "b", "c"];
  assert.strictEqual(modelColor(models, "a"), modelColor(models, "a")); // stable
  assert.notStrictEqual(modelColor(models, "a"), modelColor(models, "b"));
  assert.strictEqual(typeof modelColor(models, "unknown"), "string"); // never throws
});
