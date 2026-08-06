"use strict";

const test = require("node:test");
const assert = require("node:assert");
const {
  fmtCompact, usd, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
  foldProjects, sessionCostBuckets, topSessionsByCost, distinctSessions,
  prevWindow, pctDelta, dowBuckets, modelColor,
} = require("../static/analytics.js");

test("fmtCompact: units at the right thresholds", () => {
  assert.equal(fmtCompact(0), "0");
  assert.equal(fmtCompact(999), "999");
  assert.equal(fmtCompact(1500), "2k");
  assert.equal(fmtCompact(2_400_000), "2.4M");
  assert.equal(fmtCompact(24_200_000_000), "24.2B");
});

test("usd: cents under $100, rounded to $1k above", () => {
  assert.equal(usd(0), "$0.00");
  assert.equal(usd(12.5), "$12.50");
  assert.equal(usd(250), "$250");
  assert.equal(usd(9583.37), "$9.6k");
});

test("tokensPerPrompt: divides, and never divides by zero", () => {
  assert.equal(tokensPerPrompt(300, 4), 75);
  assert.equal(tokensPerPrompt(300, 0), 0);
});

test("modelsByCost: orders model ids by total cost, most first", () => {
  const rows = [
    { model: "a", costUsd: 1 },
    { model: "b", costUsd: 5 },
    { model: "a", costUsd: 2 },
  ];
  assert.deepEqual(modelsByCost(rows), ["b", "a"]);
});

test("foldCostByDay: folds byDayModel into per-day cost stacks + totals", () => {
  const rows = [
    { day: "2026-08-05", model: "a", input: 100, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 2 },
    { day: "2026-08-05", model: "b", input: 0, output: 50, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 3 },
    { day: "2026-08-06", model: "a", input: 0, output: 0, cacheRead: 200, cacheCreation: 0, prompts: 2, costUsd: 1 },
  ];
  const agg = foldCostByDay(rows);
  assert.equal(agg.days.length, 2);
  assert.equal(agg.days[0].day, "2026-08-05");
  assert.ok(Math.abs(agg.days[0].cost - 5) < 1e-9);
  assert.equal(agg.days[0].segs.a, 2);
  assert.ok(Math.abs(agg.maxCost - 5) < 1e-9);
  assert.ok(Math.abs(agg.totals.cost - 6) < 1e-9);
  assert.equal(agg.totals.tokens, 350);
  assert.equal(agg.totals.prompts, 4);
});

test("foldCostByDay: empty input yields no days and zero totals", () => {
  const agg = foldCostByDay([]);
  assert.equal(agg.days.length, 0);
  assert.equal(agg.totals.cost, 0);
  assert.equal(agg.maxCost, 0);
});

test("normModels: derives tokens + out/prompt, cost-descending", () => {
  const out = normModels([
    { model: "cheap", input: 10, output: 10, cacheRead: 0, cacheCreation: 0, prompts: 2, sessions: 1, costUsd: 1 },
    { model: "dear", input: 100, output: 40, cacheRead: 0, cacheCreation: 0, prompts: 4, sessions: 3, costUsd: 9 },
  ]);
  assert.equal(out[0].model, "dear"); // sorted by cost desc
  assert.equal(out[0].tokens, 140);
  assert.equal(out[0].outPerPrompt, 10);
  assert.equal(out[1].outPerPrompt, 5);
});

test("foldProjects: per-project totals + model mix, cost-descending, top-N", () => {
  const rows = [
    { project: "alpha", model: "a", input: 0, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 5 },
    { project: "alpha", model: "b", input: 0, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 3 },
    { project: "beta", model: "a", input: 0, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 4 },
  ];
  const out = foldProjects(rows, 0);
  assert.equal(out.length, 2);
  assert.equal(out[0].project, "alpha");
  assert.ok(Math.abs(out[0].cost - 8) < 1e-9);
  assert.deepEqual(Object.keys(out[0].byModel).sort(), ["a", "b"]);
  // top-N cap
  assert.equal(foldProjects(rows, 1).length, 1);
});

test("sessionCostBuckets: buckets distinct sessions by cost", () => {
  const rows = [
    { sessionId: "S1", project: "p", costUsd: 0.5 }, // <$1
    { sessionId: "S1", project: "p", costUsd: 0.2 }, // same session → $0.7 total, still <$1
    { sessionId: "S2", project: "p", costUsd: 7 },   // $5–15
    { sessionId: "S3", project: "p", costUsd: 50 },  // $40+
  ];
  const b = sessionCostBuckets(rows);
  const by = Object.fromEntries(b.map((x) => [x.label, x.count]));
  assert.equal(by["<$1"], 1);
  assert.equal(by["$5–15"], 1);
  assert.equal(by["$40+"], 1);
});

test("topSessionsByCost + distinctSessions: aggregate across day/model rows", () => {
  const rows = [
    { sessionId: "S1", project: "p", day: "2026-08-05", model: "a", input: 100, costUsd: 2, prompts: 1 },
    { sessionId: "S1", project: "p", day: "2026-08-06", model: "b", input: 0, output: 50, costUsd: 3, prompts: 2 },
    { sessionId: "S2", project: "p", day: "2026-08-05", model: "a", input: 10, costUsd: 1, prompts: 1 },
  ];
  assert.equal(distinctSessions(rows), 2);
  const top = topSessionsByCost(rows, 10);
  assert.equal(top[0].sid, "S1");
  assert.ok(Math.abs(top[0].cost - 5) < 1e-9); // 2 + 3
  assert.equal(top[0].model, "mixed"); // two models
  assert.equal(top[0].day, "2026-08-06"); // latest day
});

test("prevWindow: equal-length window immediately before", () => {
  // 14-day window → previous 14 days.
  assert.deepEqual(prevWindow("2026-07-24", "2026-08-06"), { from: "2026-07-10", to: "2026-07-23" });
  // single day → the day before.
  assert.deepEqual(prevWindow("2026-08-06", "2026-08-06"), { from: "2026-08-05", to: "2026-08-05" });
});

test("pctDelta: percent change, null when no baseline", () => {
  assert.equal(pctDelta(120, 100), 20);
  assert.equal(pctDelta(80, 100), -20);
  assert.equal(pctDelta(5, 0), null);
});

test("dowBuckets: sums cost per weekday Mon..Sun", () => {
  // 2026-08-03 is a Monday, 2026-08-09 a Sunday.
  const rows = [
    { day: "2026-08-03", costUsd: 2 }, // Mon → idx 0
    { day: "2026-08-03", costUsd: 1 }, // Mon → idx 0
    { day: "2026-08-09", costUsd: 4 }, // Sun → idx 6
  ];
  const d = dowBuckets(rows);
  assert.equal(d[0], 3);
  assert.equal(d[6], 4);
  assert.equal(d[3], 0);
});

test("modelColor: stable per model index, wraps the palette", () => {
  const models = ["a", "b", "c", "d", "e", "f", "g", "h"];
  assert.equal(modelColor(models, "a"), modelColor(models, "a")); // stable
  assert.notEqual(modelColor(models, "a"), modelColor(models, "b"));
  assert.equal(modelColor(models, "a"), modelColor(models, "h")); // 8th wraps to 1st (7 colors)
});
