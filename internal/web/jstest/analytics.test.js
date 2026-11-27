"use strict";

const test = require("node:test");
const assert = require("node:assert");
const {
  fmtCompact, usd, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
  topSessionsByCost, distinctSessions,
  prevWindow, pctDelta, modelColor, collapseZeroCost, pricedModelIds,
  daysBetween, clampRange, stackedLayout, anCountText, anSessionKpis, projShort,
  anModelFold, anPct, anModelCells, anProjectBars, anProjectRow, anSessionRow,
  anYTicks, AN_PAGE_CHART, anLegendItems, anXLabels,
  median, cacheHit, kpiDelta, anOverviewKpis, anWorkspaceRows, anDriverCards, anModelMedians,
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

test("collapseZeroCost: folds $0 models into one 'other (unpriced)' row, keeps prompts", () => {
  const rows = [
    { model: "claude-opus-5", cost: 9, tokens: 100, prompts: 4, sessions: 2 },
    { model: "", cost: 0, tokens: 0, prompts: 27, sessions: 1 },
    { model: "<synthetic>", cost: 0, tokens: 0, prompts: 41, sessions: 3 },
  ];
  const out = collapseZeroCost(rows);
  assert.equal(out.length, 2); // opus + one "other"
  assert.equal(out[0].model, "claude-opus-5");
  const other = out[1];
  assert.equal(other.model, "other (unpriced)");
  assert.equal(other.cost, 0);
  assert.equal(other.prompts, 68); // 27 + 41 — activity preserved, not hidden
  assert.equal(other.sessions, 4);
});

test("collapseZeroCost: no $0 models → unchanged, no 'other' row", () => {
  const rows = [{ model: "a", cost: 5, tokens: 1, prompts: 1, sessions: 1 }];
  const out = collapseZeroCost(rows);
  assert.equal(out.length, 1);
  assert.equal(out[0].model, "a");
});

test("pricedModelIds: only models with cost > 0, order preserved", () => {
  const rows = [
    { model: "a", cost: 5 }, { model: "", cost: 0 }, { model: "b", cost: 1 },
  ];
  assert.deepEqual(pricedModelIds(rows), ["a", "b"]);
});

test("daysBetween: inclusive day count", () => {
  assert.equal(daysBetween("2026-08-01", "2026-08-07"), 7);
  assert.equal(daysBetween("2026-08-06", "2026-08-06"), 1);
});

test("clampRange: orders from ≤ to, swapping if reversed", () => {
  assert.deepEqual(clampRange("2026-08-01", "2026-08-10"), { from: "2026-08-01", to: "2026-08-10" });
  assert.deepEqual(clampRange("2026-08-10", "2026-08-01"), { from: "2026-08-01", to: "2026-08-10" });
});

test("stackedLayout: date-mode positions bars by real date offset (gaps show)", () => {
  const days = [
    { day: "2026-08-01", cost: 5, segs: { a: 5 } },
    { day: "2026-08-05", cost: 3, segs: { a: 3 } },
  ];
  const lay = stackedLayout(days, "2026-08-01", "2026-08-07");
  assert.equal(lay.dateMode, true);
  assert.equal(lay.slots, 7);
  assert.equal(lay.bars[0].slot, 0); // Aug 1 → offset 0
  assert.equal(lay.bars[1].slot, 4); // Aug 5 → offset 4 (gap of 3 empty days shows)
  assert.equal(lay.bars[1].cost, 3);
});

test("stackedLayout: huge span falls back to index mode (no absurd compression)", () => {
  const days = [
    { day: "2000-06-01", cost: 1, segs: {} },
    { day: "2026-08-05", cost: 2, segs: {} },
  ];
  const lay = stackedLayout(days, "2000-01-01", "2026-08-07");
  assert.equal(lay.dateMode, false);
  assert.equal(lay.slots, 2);
  assert.equal(lay.bars[0].slot, 0);
  assert.equal(lay.bars[1].slot, 1);
});

test("modelColor returns the --an-N token, so the desktop palette can swap the hues", () => {
  assert.equal(modelColor(["a", "b"], "b"), "var(--an-2)");
  assert.equal(modelColor(["a", "b"], "a"), "var(--an-1)");
  const models = ["a", "b", "c", "d", "e", "f", "g", "h"];
  assert.equal(modelColor(models, "g"), "var(--an-7)");
  assert.equal(modelColor(models, "h"), "var(--an-1)"); // index 7 wraps
  assert.equal(modelColor(models, "zzz"), "var(--an-1)"); // unknown model: first slot, as before
});

test("analytics.js hard-codes no chart hex: every chart colour is a var()", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "analytics.js"), "utf8");
  assert.deepEqual(src.match(/#[0-9a-fA-F]{6}\b/g) || [], []);
  assert.doesNotMatch(src, /"var\(--(danger|ok)\)"/, "diverging fills use --an-up / --an-down");
});

test("modelColor: stable per model index, wraps the palette", () => {
  const models = ["a", "b", "c", "d", "e", "f", "g", "h"];
  assert.equal(modelColor(models, "a"), modelColor(models, "a")); // stable
  assert.notEqual(modelColor(models, "a"), modelColor(models, "b"));
  assert.equal(modelColor(models, "a"), modelColor(models, "h")); // 8th wraps to 1st (7 colors)
});

// ---- the page's KPI counts and the Sessions view's three tiles (UI plan 5, #175; digest-design §A2, §A3) ----

test("anCountText: thousands separators under 10,000, fmtCompact from there", () => {
  assert.equal(anCountText(0), "0");
  assert.equal(anCountText(1420), "1,420");
  assert.equal(anCountText(3994), "3,994");
  assert.equal(anCountText(9999), "9,999");
  assert.equal(anCountText(10000), fmtCompact(10000));
  assert.equal(anCountText(10400000000), fmtCompact(10400000000));
  assert.equal(anCountText(10400000000), "10.4B");
});

// A made-up window: two projects, three sessions. The costliest SESSION is in acme-api (one session, $120.50) and the
// costliest PROJECT is acme-web-frontend (two sessions, $160.14): the two tiles name different projects.
const SESS_ROWS = [
  { sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", day: "2026-10-30", model: "claude-opus-5-5", input: 1000, output: 200, cacheRead: 5000, cacheCreation: 0, prompts: 10, costUsd: 120 },
  { sessionId: "3d6b98e8aaaa", project: "-home-dev-acme-api", day: "2026-10-31", model: "claude-sonnet-5-5", input: 10, output: 5, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 0.5 },
  { sessionId: "2cf1c405bbbb", project: "-home-dev-acme-web-frontend", day: "2026-10-30", model: "claude-opus-5-5", input: 100, output: 20, cacheRead: 500, cacheCreation: 0, prompts: 4, costUsd: 90 },
  { sessionId: "9d3dc2afcccc", project: "-home-dev-acme-web-frontend", day: "2026-10-26", model: "claude-opus-5-5", input: 1, output: 1, cacheRead: 0, cacheCreation: 0, prompts: 1, costUsd: 70.14 },
];
// byProject rows (project, cost, tokens, prompts, byModel) folded from session rows: the shape anProjectBars and anSessionKpis read.
function projectsOf(rows) {
  const m = new Map();
  for (const r of rows) {
    const p = m.get(r.project) || { project: r.project, cost: 0, tokens: 0, prompts: 0, byModel: {} };
    p.cost += r.costUsd || 0;
    p.tokens += (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    p.prompts += r.prompts || 0;
    p.byModel[r.model] = (p.byModel[r.model] || 0) + (r.costUsd || 0);
    m.set(r.project, p);
  }
  return [...m.values()].sort((a, b) => b.cost - a.cost);
}
// The data the view reads (topSessions, byProject, sessionCount).
const sessionData = (rows) => ({
  sessionCount: distinctSessions(rows),
  topSessions: topSessionsByCost(rows, 18),
  byProject: projectsOf(rows),
});

test("anSessionKpis: sessions in the window, the costliest session by its project, the costliest project by its cost", () => {
  const d = sessionData(SESS_ROWS);
  assert.equal(d.sessionCount, 3);
  assert.deepEqual(anSessionKpis(d).map((t) => ({ value: t.value, label: t.label })), [
    { value: "3", label: "Sessions in this window" },
    { value: "$121", label: "Costliest session · acme-api" },
    { value: "$160", label: "Costliest project · acme-web-frontend" },
  ]);
  // The values are today's formatters on today's data: usd of the top session's cost, usd of the top project's COST.
  assert.equal(usd(d.topSessions[0].cost), "$121");
  assert.equal(usd(d.byProject[0].cost), "$160");
  // A window of 1,420 sessions reads 1,420, not 1k.
  assert.equal(anSessionKpis({ ...d, sessionCount: 1420 })[0].value, "1,420");
  assert.equal(anSessionKpis({ ...d, sessionCount: 12500 })[0].value, fmtCompact(12500));
});

test("anSessionKpis: no sessions or projects gives a dash and the plain label", () => {
  const tiles = anSessionKpis({ sessionCount: 0, topSessions: [], byProject: [] });
  assert.deepEqual(tiles.map((t) => ({ value: t.value, label: t.label })), [
    { value: "0", label: "Sessions in this window" },
    { value: "—", label: "Costliest session" },
    { value: "—", label: "Costliest project" },
  ]);
  // A session or project with no name (a row with an empty project) does not leave a dangling " · " in the label.
  const nameless = anSessionKpis({ sessionCount: 1, topSessions: [{ project: "", cost: 3 }], byProject: [{ project: "", cost: 3 }] });
  assert.deepEqual(nameless.map((t) => t.label), ["Sessions in this window", "Costliest session", "Costliest project"]);
  assert.deepEqual(nameless.map((t) => t.value), ["1", "$3.00", "$3.00"]);
});

// ---- the data both builders share (the phone's modal and the desktop page): one preparation, two sets of DOM ----

test("anModelFold, anModelCells and anPct: the Models figures", () => {
  const rows = normModels([
    { model: "claude-opus-5-5", input: 4100, output: 200, cacheRead: 6100, cacheCreation: 0, prompts: 4, sessions: 4, costUsd: 4300 },
    { model: "<synthetic>", prompts: 41, sessions: 3 },
    { model: "", prompts: 27, sessions: 1 },
  ]);
  const f = anModelFold(rows);
  assert.deepEqual(f.rows.map((r) => r.model), ["claude-opus-5-5", "other (unpriced)"], "zero-cost models fold into one row");
  assert.equal(f.totalCost, 4300);
  assert.equal(f.totalTokens, 10400);
  assert.equal(f.maxTokens, 10400);
  assert.equal(f.maxCost, 4300);
  assert.deepEqual({ ...anModelCells(f.rows[0], f) }, { cost: "$4.3k", pct: "100%", tokens: "10k", prompts: "4", outPerPrompt: "50", cache: "59%" });
  assert.deepEqual({ ...anModelCells(f.rows[1], f) }, { cost: "$0.00", pct: "0%", tokens: "0", prompts: "68", outPerPrompt: "0", cache: "0%" }, "no tokens: a 0% cache share, not NaN");
  assert.equal(anPct(1, 3), "33%");
  assert.equal(anPct(2, 3), "67%");
  // Nothing priced and nothing counted: totals and maxima stay at 1, so no bar or share divides by zero.
  const none = anModelFold([]);
  assert.deepEqual([none.rows.length, none.totalCost, none.totalTokens, none.maxTokens, none.maxCost], [0, 1, 1, 1, 1]);
});

test("anProjectBars and anProjectRow: the stacked bars' segments (dearest model first) and the detail row", () => {
  const byProject = projectsOf([
    { project: "-home-dev-acme-api", model: "a", input: 10, prompts: 2, costUsd: 1 },
    { project: "-home-dev-acme-api", model: "b", input: 5, prompts: 1, costUsd: 3 },
    { project: "-home-dev-acme-web", model: "a", input: 1500, prompts: 7, costUsd: 2 },
  ]);
  const bars = anProjectBars(byProject, ["a", "b"]);
  assert.equal(bars.max, 4);
  assert.deepEqual(bars.bars.map((b) => [b.name, b.cost]), [["acme-api", 4], ["acme-web", 2]]);
  assert.deepEqual(bars.bars[0].segs.map((g) => ({ ...g })), [
    { v: 3, c: "var(--an-2)", t: "b $3.00" },
    { v: 1, c: "var(--an-1)", t: "a $1.00" },
  ]);
  assert.deepEqual({ ...anProjectRow(byProject[1]) }, { name: "acme-web", cost: "$2.00", tokens: "2k", prompts: "7" });
  assert.deepEqual(anProjectBars([], []).bars, []);
  assert.equal(anProjectBars([], []).max, 1);
});

test("anSessionRow: one session's cells as text; mixed is flagged, a single model is short", () => {
  const top = topSessionsByCost(SESS_ROWS, 18);
  assert.deepEqual({ ...anSessionRow(top[0]) }, { sid: "3d6b98e8", day: "2026-10-31", project: "acme-api", model: "mixed", mixed: true, tokens: "6k", prompts: "11", cost: "$121" });
  const single = anSessionRow(top.find((s) => s.sid === "2cf1c405"));
  assert.equal(single.model, "opus-5-5");
  assert.equal(single.mixed, false);
});

// ---- the cost chart's y labels and the legend (UI plan 5 Task 6; digest-design §A2, §B6) ----

test("anYTicks: the page's three labels at the peak and its thirds, no zero label", () => {
  const t = anYTicks(421, false);
  assert.deepStrictEqual(t.map((x) => x.text), ["$421", "$281", "$140"], "§A2: peak, 2/3 and 1/3 of it, formatted by usd()");
  assert.deepStrictEqual(t.map((x) => x.frac), [1, 2 / 3, 1 / 3]);
  assert.ok(!t.some((x) => x.text === "$0.00" || x.frac === 0), "the baseline is not labelled");
  assert.deepStrictEqual(anYTicks(12.5, false).map((x) => x.text), ["$12.50", "$8.33", "$4.17"]);
});

test("anYTicks: no scale (0, NaN, negative, missing) means no labels, never $0.00 or NaN", () => {
  for (const bad of [0, NaN, -1, undefined, null, "x"]) {
    assert.deepStrictEqual(anYTicks(bad, false), [], String(bad));
    assert.deepStrictEqual(anYTicks(bad, true), [], String(bad) + " (with the zero)");
  }
});

test("anYTicks(max, true): the phone's four labels, text for text what 'usd(max * (1 - i / 3))' printed", () => {
  const t = anYTicks(421, true);
  assert.deepStrictEqual(t.map((x) => x.text), ["$421", "$281", "$140", "$0.00"]);
  assert.deepStrictEqual(t.map((x) => x.frac), [1, 2 / 3, 1 / 3, 0]);
  // The phone drew usd(max * (1 - i / 3)) for i = 0..3 (anStackedTime on main, c47442b). 1 - 1/3 is not 2/3 to the last bit, so the
  // thirds must not be computed any other way: the same expression, for any max, prints the same text.
  // 1725 and 2175 are values where 'max * 2 / 3' or 'max * (2 / 3)' prints $1.1k / $1.4k and the phone printed $1.2k / $1.5k.
  assert.deepStrictEqual(anYTicks(1725, true).map((x) => x.text), ["$1.7k", "$1.2k", "$575", "$0.00"]);
  assert.deepStrictEqual(anYTicks(2175, false).map((x) => x.text), ["$2.2k", "$1.5k", "$725"]);
  for (const max of [1, 1.5, 12.5, 33.33, 99.99, 100, 150, 421, 999.99, 1000, 1234.5, 1725, 2175, 9583.37, 123456.789, 0.7, 2.25, 299.97, 300.03]) {
    assert.deepStrictEqual(anYTicks(max, true).map((x) => x.text), [0, 1, 2, 3].map((i) => usd(max * (1 - i / 3))), String(max));
    assert.deepStrictEqual(anYTicks(max, false).map((x) => x.text), [0, 1, 2].map((i) => usd(max * (1 - i / 3))), String(max) + " (page)");
  }
});

test("AN_PAGE_CHART is the frame's chart: 150px plot, 44px gutter with a 38px label box, 16px bars, labels 6px below", () => {
  assert.deepStrictEqual({ ...AN_PAGE_CHART }, { plotH: 150, gutter: 44, labelBox: 38, bar: 16, xLabelGap: 6 });
});

test("anLegendItems: a name and a colour per model, coloured by its place in the canonical list", () => {
  const all = ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"];
  assert.deepStrictEqual(anLegendItems(all), [
    { name: "opus-5-5", color: "var(--an-1)" }, { name: "sonnet-5-5", color: "var(--an-2)" }, { name: "haiku-5-5", color: "var(--an-3)" },
  ]);
  // A priced-only legend keeps the colours the bars have.
  assert.deepStrictEqual(anLegendItems(["claude-haiku-5-5"], all), [{ name: "haiku-5-5", color: "var(--an-3)" }]);
  assert.deepStrictEqual(anLegendItems([]), []);
});

test("anXLabels: in date mode every every-th slot of the window is labelled with its own date, a day with no cost included", () => {
  // 2026-10-11 .. 2026-11-09 is 30 slots; only six days cost anything (the gate's sparse 30d window), so the bars alone would label 6.
  const days = [0, 6, 12, 21, 24, 27].map((slot) => ({ day: new Date(Date.UTC(2026, 9, 11 + slot)).toISOString().slice(0, 10), segs: {}, cost: 1 }));
  const lay = stackedLayout(days, "2026-10-11", "2026-11-09");
  assert.deepStrictEqual(lay.bars.map((b) => b.slot), [0, 6, 12, 21, 24, 27]);
  const labels = anXLabels(lay, "2026-10-11", 3);
  assert.deepStrictEqual(labels.map((l) => l.slot), [0, 3, 6, 9, 12, 15, 18, 21, 24, 27], "every 3rd slot from the first (round(30 / 12) = 3, the frame's pitch)");
  assert.deepStrictEqual(labels.map((l) => l.text), ["10-11", "10-14", "10-17", "10-20", "10-23", "10-26", "10-29", "11-01", "11-04", "11-07"], "each slot's own date, across a month end");
  // A one-day window: its one slot. A pitch of 1 labels every slot.
  assert.deepStrictEqual(anXLabels(stackedLayout([], "2026-10-09", "2026-10-09"), "2026-10-09", 1), [{ slot: 0, text: "10-09" }]);
  assert.strictEqual(anXLabels(stackedLayout([], "2026-10-09", "2026-10-15"), "2026-10-09", 1).length, 7);
  // An empty window still has its slots (the chart draws the baseline and the dates under it).
  assert.deepStrictEqual(anXLabels(stackedLayout([], "2026-10-09", "2026-10-15"), "2026-10-09", 3).map((l) => l.text), ["10-09", "10-12", "10-15"]);
});

test("anXLabels: a very wide window is index mode, where the slots are the bars, as the phone's chart labels them", () => {
  const days = [];
  for (let i = 0; i < 40; i++) days.push({ day: new Date(Date.UTC(2026, 0, 1 + i * 3)).toISOString().slice(0, 10), segs: {}, cost: 1 });
  const lay = stackedLayout(days, "2000-01-01", "2026-08-07");
  assert.strictEqual(lay.dateMode, false);
  const labels = anXLabels(lay, "2000-01-01", 10);
  assert.deepStrictEqual(labels.map((l) => l.slot), [0, 10, 20, 30]);
  assert.deepStrictEqual(labels.map((l) => l.text), [days[0], days[10], days[20], days[30]].map((d) => d.day.slice(5)));
  assert.deepStrictEqual(anXLabels(stackedLayout([], "2000-01-01", "2026-08-07"), "2000-01-01", 10), []);
});

// ---- #168 G3 1, 3-6: Re-scan and the Tools card heads carry their glyphs ----------------------------------------------
// Each head glyph is keyed on the group's declared `kind` (Go analytics.AttrKey.Kind), never on the words; the rest of the
// page (card titles, rows, view switch, KPIs) stays plain (legend left-outs a-k).
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { Node } = require("./fakedom.js");
const { ICON_SRC } = require("./iconpage.js");
const { ICON_TOOL_KIND, iconOf } = require("../static/iconmap.js");

const STATIC = path.join(__dirname, "..", "static");
const readStatic = (f) => fs.readFileSync(path.join(STATIC, f), "utf8");

test("ICON_TOOL_KIND is G3 3-6, frozen and prototype-free", () => {
  assert.deepStrictEqual({ ...ICON_TOOL_KIND }, { mcp_server: "server", plugin: "puzzle", mcp_tool: "plug", skill: "book-open" });
  assert.ok(Object.isFrozen(ICON_TOOL_KIND));
  assert.equal(Object.getPrototypeOf(ICON_TOOL_KIND), null);
  for (const k of ["constructor", "__proto__", "toString", "", "other"]) assert.equal(iconOf(ICON_TOOL_KIND, k), "", k);
});

// analytics.js and the icon scripts in one vm context, on a fake document; `page` says whether the dialog is the desktop page.
function anPage(page) {
  const doc = { readyState: "loading", addEventListener() {} };
  doc.createElement = (t) => new Node(t, doc);
  doc.createElementNS = (_ns, t) => new Node(t, doc);
  doc.createTextNode = (t) => { const n = new Node("#text", doc); n.own = String(t); return n; };
  const dlg = new Node("dialog", doc);
  if (page) dlg.className = "as-page";
  doc.getElementById = (id) => (id === "analytics-modal" ? dlg : null);
  const ctx = vm.createContext({ console, Array, Object, Math, Date, document: doc });
  new vm.Script(ICON_SRC + "\n" + readStatic("analytics.js"), { filename: "analytics.js" }).runInContext(ctx);
  return { ctx, doc };
}
test("the Tools glyph is 6px from its word: a margin on the h3's glyph, in the desktop page's rules", () => {
  const css = readStatic("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#page-host > #analytics-modal\.as-page \.an-card \.card-head > h3 > \.ui-icon \{ margin-right: 6px; \}/);
});

test("card titles stay plain: a titled card (the Overview's h2s) has no glyph", () => {
  const { ctx } = anPage(true);
  for (const t of ["Cost over time", "Cost by model", "Cost by project"]) {
    const card = ctx.anCard(t, "", "h2");
    assert.equal(card.querySelectorAll("svg").length, 0, t);
  }
  assert.equal(ctx.anCard("something", "cap").querySelectorAll("svg").length, 0);
});

test("Re-scan: #an-rescan holds the refresh-cw placeholder at 14 before its word", () => {
  const html = readStatic("index.html");
  const btn = /<button[^>]*id="an-rescan"[^>]*>[\s\S]*?<\/button>/.exec(html);
  assert.ok(btn, "#an-rescan is in index.html");
  assert.match(btn[0], /^<button[^>]*>\s*<svg data-uicon="refresh-cw" width="14" height="14" aria-hidden="true"><\/svg>Re-scan<\/button>$/);
  // The phone's own Re-scan (#analytics-refresh) is unchanged: words only.
  const ph = /<button[^>]*id="analytics-refresh"[^>]*>[\s\S]*?<\/button>/.exec(html);
  assert.equal(ph[0].includes("<svg"), false);
});

test("anRescan keeps the glyph while it says Scanning… and after, and still works on a words-only button", async () => {
  const { ctx, doc } = anPage(true);
  const mk = (withIcon) => {
    const b = new Node("button", doc);
    if (withIcon) { const s = new Node("svg", doc); s.className = "ui-icon"; b.appendChild(s); }
    b.own = withIcon ? "" : "Re-scan";
    if (withIcon) b.appendChild(doc.createTextNode("Re-scan"));
    return b;
  };
  const seen = [];
  ctx.api = async () => { seen.push(b.textContent + "|" + b.querySelectorAll("svg").length); };
  ctx.anLoad = async () => {};
  const b = mk(true);
  await ctx.anRescan(b);
  assert.deepEqual(seen, ["Scanning…|1"], "glyph kept, word replaced, while it runs");
  assert.equal(b.textContent, "Re-scan");
  assert.equal(b.querySelectorAll("svg").length, 1);
  assert.equal(b.disabled, false);
  const p = mk(false);
  seen.length = 0;
  ctx.api = async () => { seen.push(p.textContent); };
  await ctx.anRescan(p);
  assert.deepEqual(seen, ["Scanning…"]);
  assert.equal(p.textContent, "Re-scan");
});

test("anRescan: a rejected request leaves the glyph and the word in place and the button enabled", async () => {
  const { ctx, doc } = anPage(true);
  const b = new Node("button", doc);
  const s = new Node("svg", doc); s.className = "ui-icon";
  b.append(s, doc.createTextNode("Re-scan"));
  ctx.api = async () => { throw new Error("boom"); };
  ctx.anLoad = async () => {};
  await ctx.anRescan(b);
  assert.equal(b.textContent, "Re-scan");
  assert.equal(b.querySelectorAll("svg").length, 1);
  assert.equal(b.disabled, false);
});

// ---- Analytics v3: the shared preparation of workspaces, cost drivers, medians and cache hit ----

test("median: null for none, the middle value, the mean of the two middle values for an even count; the input is not reordered", () => {
  assert.equal(median([]), null);
  assert.equal(median(undefined), null);
  assert.equal(median([3]), 3);
  assert.equal(median([1, 9, 2, 4]), 3);
  assert.equal(median([10, 2, 7]), 7);
  const xs = [9, 1, 5];
  median(xs);
  assert.deepEqual(xs, [9, 1, 5]);
});

test("cacheHit: a ratio from the three token counts or from cacheRead and cacheDenom; null when the denominator is 0", () => {
  assert.equal(cacheHit({ cacheRead: 0, cacheDenom: 0 }), null);
  assert.equal(cacheHit({ cacheRead: 0, input: 0, cacheCreation: 0 }), null);
  assert.equal(cacheHit({ cacheRead: 96, cacheDenom: 100 }), 0.96);
  assert.equal(cacheHit({ cacheRead: 60, input: 20, cacheCreation: 20 }), 0.6);
  assert.equal(cacheHit({}), null);
  assert.equal(cacheHit(null), null);
});

test("kpiDelta: percent for counts and dollars, points for the cache ratio, null with no baseline", () => {
  assert.equal(kpiDelta(1.12, 1, "pct").text, "▲ 12%");
  assert.equal(kpiDelta(1.12, 1, "pct").dir, "up");
  assert.equal(kpiDelta(0.5, 1, "pct").text, "▼ 50%");
  assert.equal(kpiDelta(0.5, 1, "pct").dir, "down");
  assert.equal(kpiDelta(0.957, 0.961, "pt").text, "▼ 0.4 pt");
  assert.equal(kpiDelta(0.961, 0.957, "pt").text, "▲ 0.4 pt");
  assert.equal(kpiDelta(5, 0, "pct"), null);
  assert.equal(kpiDelta(5, null, "pct"), null);
  assert.equal(kpiDelta(null, 5, "pct"), null);
  assert.equal(kpiDelta(0.9, null, "pt"), null);
  assert.equal(kpiDelta(null, 0.9, "pt"), null);
  assert.equal(kpiDelta(1, 1, "pct").dir, "flat");
});

test("anOverviewKpis: the four tiles of §3.1, labels exactly, no delta without Compare", () => {
  const d = { costByDay: { totals: { cost: 112 } }, sessionCount: 50, median: 1.5, cache: 0.957 };
  const tiles = anOverviewKpis(d, null);
  assert.deepEqual(tiles.map((t) => t.label), ["API value", "Sessions", "Median per session", "Cache hit"]);
  assert.deepEqual(tiles.map((t) => t.value), ["$112", "50", "$1.50", "95.7%"]);
  for (const t of tiles) assert.equal(t.delta, null);
});

test("anOverviewKpis: with Compare each tile has its change; only the first says 'vs the N days before'; cache hit is in points", () => {
  const d = { costByDay: { totals: { cost: 112 } }, sessionCount: 60, median: 2, cache: 0.957 };
  const cmp = { days: 30, prev: { cost: 100, sessions: 50, median: 2.5, cache: 0.961 } };
  const t = anOverviewKpis(d, cmp);
  assert.equal(t[0].delta.text, "▲ 12% vs the 30 days before");
  assert.equal(t[0].delta.dir, "up");
  assert.equal(t[1].delta.text, "▲ 20%");
  assert.equal(t[2].delta.text, "▼ 20%");
  assert.equal(t[3].delta.text, "▼ 0.4 pt");
});

test("anOverviewKpis: no baseline for a tile leaves that tile without a delta, and an empty window shows dashes", () => {
  const d = { costByDay: { totals: { cost: 0 } }, sessionCount: 0, median: null, cache: null };
  const t = anOverviewKpis(d, { days: 7, prev: { cost: 0, sessions: 0, median: null, cache: null } });
  assert.deepEqual(t.map((x) => x.value), ["$0.00", "0", "—", "—"]);
  for (const x of t) assert.equal(x.delta, null);
});

test("anDriverCards: percentages are over the covered total; the muted last row takes the rest; coverage is covered/total", () => {
  const r = anDriverCards({ total: 100, coveredTotal: 10, agent: [{ name: "general-purpose", costUsd: 7 }], skill: [], plugin: [], mcp: [] });
  assert.equal(r.empty, false);
  assert.equal(r.coverage, 0.1);
  assert.deepEqual(r.cards.map((c) => c.kind), ["agent", "skill", "plugin", "mcp"]);
  const agent = r.cards[0];
  assert.deepEqual(agent.rows.map((x) => [x.name, x.cost, x.pct, x.muted, x.sub]), [
    ["general-purpose", 7, 70, false, false],
    ["main agent (no subagent)", 3, 30, true, false],
  ]);
  assert.equal(agent.max, 7);
  assert.equal(agent.title, "Subagents");
  assert.equal(agent.caption, "main agent vs subagents");
  assert.deepEqual(r.cards[1].rows.map((x) => [x.name, x.cost, x.pct, x.muted]), [["outside any skill", 10, 100, true]]);
  assert.deepEqual(r.cards[2].rows.map((x) => x.name), ["outside any plugin"]);
  assert.deepEqual(r.cards[3].rows.map((x) => x.name), ["outside any MCP tool"]);
  assert.equal(r.cards[1].max, 0, "no named row, no bar");
});

test("anDriverCards: full coverage has no coverage line; an overshoot clamps the muted row at zero", () => {
  assert.equal(anDriverCards({ total: 10, coveredTotal: 10, agent: [], skill: [], plugin: [], mcp: [] }).coverage, null);
  const r = anDriverCards({ total: 10, coveredTotal: 10, agent: [{ name: "a", costUsd: 6 }, { name: "b", costUsd: 6 }], skill: [], plugin: [], mcp: [] });
  const last = r.cards[0].rows[r.cards[0].rows.length - 1];
  assert.equal(last.muted, true);
  assert.equal(last.cost, 0);
  assert.equal(last.pct, 0);
});

test("anDriverCards: MCP servers are followed by their tools (indented rows); the muted row is the rest of the covered total", () => {
  const r = anDriverCards({
    total: 20, coveredTotal: 20, agent: [], skill: [], plugin: [],
    mcp: [{ server: "docs", costUsd: 8, tools: [{ name: "search", costUsd: 5 }, { name: "fetch", costUsd: 2 }] }, { server: "db", costUsd: 4, tools: [] }],
  });
  const mcp = r.cards[3];
  assert.equal(mcp.caption, "server → its tools");
  assert.deepEqual(mcp.rows.map((x) => [x.name, x.cost, x.sub, x.muted]), [
    ["docs", 8, false, false], ["search", 5, true, false], ["fetch", 2, true, false], ["db", 4, false, false],
    ["outside any MCP tool", 8, false, true],
  ]);
  assert.equal(mcp.max, 8);
  assert.equal(mcp.rows[4].pct, 40);
});

test("anDriverCards: no covered value is the empty state, never NaN or 0% tiles or a divide by zero", () => {
  const dead = [
    { total: 100, coveredTotal: 0, agent: [], skill: [], plugin: [], mcp: [] },
    { total: 0, coveredTotal: 0, agent: [], skill: [], plugin: [], mcp: [] },
    { total: 5, coveredTotal: 0, agent: [{ name: "x", costUsd: 0 }], skill: [], plugin: [], mcp: [] },
    {}, null, undefined,
  ];
  for (const resp of dead) {
    const r = anDriverCards(resp);
    assert.equal(r.empty, true);
    assert.equal(r.coverage, null);
    assert.deepEqual(r.cards, []);
    assert.ok(!JSON.stringify(r).includes("NaN"));
  }
});

test("anDriverCards: a name is kept as data, unescaped, newline and markup included", () => {
  const r = anDriverCards({ total: 10, coveredTotal: 10, agent: [{ name: "a\nb", costUsd: 4 }], skill: [{ name: "<b>x</b>", costUsd: 1 }], plugin: [], mcp: [{ server: "s\x1b[31m", costUsd: 1, tools: [{ name: "t\"'", costUsd: 1 }] }] });
  assert.equal(r.cards[0].rows[0].name, "a\nb");
  assert.equal(r.cards[1].rows[0].name, "<b>x</b>");
  assert.equal(r.cards[3].rows[0].name, "s\x1b[31m");
  assert.equal(r.cards[3].rows[1].name, "t\"'");
});

const WS_RESP = {
  from: "2026-11-01", to: "2026-11-27",
  workspaces: [
    {
      workspace: "/home/u2/acme", name: "acme", unmatched: false, costUsd: 100, sessions: 4, hostCost: 31, boxCost: 69,
      byModel: { opus: 75, haiku: 25 }, cacheRead: 90, cacheDenom: 100, sessionCosts: [10, 20, 30, 40],
      children: [
        { runtime: "host", profile: "", via: "", label: "host", path: "/home/u2/acme", costUsd: 31, sessions: 1, cacheRead: 9, cacheDenom: 10, sessionCosts: [31] },
        { runtime: "box", profile: "work", via: "clone", label: "box · work", path: "/home/agent/acme", costUsd: 69, sessions: 3, cacheRead: 0, cacheDenom: 0, sessionCosts: [10, 19, 40] },
      ],
    },
    { workspace: "-home-x-old", name: "old", unmatched: true, costUsd: 0, sessions: 1, hostCost: 0, boxCost: 0, byModel: {}, cacheRead: 0, cacheDenom: 0, sessionCosts: [], children: [] },
  ],
};

test("anWorkspaceRows: host and box shares add to 100, the model mix is dearest first, medians and cache hit come from the row's own figures", () => {
  const [a, old] = anWorkspaceRows(WS_RESP);
  assert.equal(a.name, "acme");
  assert.equal(a.path, "/home/u2/acme");
  assert.equal(a.unmatched, false);
  assert.equal(a.cost, 100);
  assert.equal(a.sessions, 4);
  assert.equal(a.hostPct + a.boxPct, 100);
  assert.deepEqual([a.hostPct, a.boxPct], [31, 69]);
  assert.deepEqual(a.mix.map((m) => [m.model, m.share]), [["opus", 0.75], ["haiku", 0.25]]);
  assert.equal(a.median, 25);
  assert.equal(a.cacheHit, 0.9);
  assert.equal(a.children.length, 2);
  assert.deepEqual(
    a.children.map((c) => [c.runtime, c.profile, c.via, c.label, c.path, c.cost, c.sessions, c.median, c.cacheHit]),
    [["host", "", "", "host", "/home/u2/acme", 31, 1, 31, 0.9], ["box", "work", "clone", "box · work", "/home/agent/acme", 69, 3, 19, null]],
  );
  // an unmatched row passes through, says why it has no path, and has no shares or medians to invent
  assert.equal(old.unmatched, true);
  assert.equal(old.name, "old");
  assert.equal(old.path, "unmatched · folder no longer known");
  assert.deepEqual([old.hostPct, old.boxPct, old.median, old.cacheHit], [0, 0, null, null]);
  assert.deepEqual(old.mix, []);
});

test("anWorkspaceRows: odd splits still add to 100; no response is no rows", () => {
  const r = anWorkspaceRows({ workspaces: [{ workspace: "w", name: "w", unmatched: false, costUsd: 3, sessions: 1, hostCost: 1, boxCost: 2, byModel: { m: 3 }, cacheRead: 0, cacheDenom: 0, sessionCosts: [3], children: [] }] })[0];
  assert.equal(r.hostPct + r.boxPct, 100);
  assert.deepEqual(anWorkspaceRows(null), []);
  assert.deepEqual(anWorkspaceRows({}), []);
  assert.deepEqual(anWorkspaceRows({ workspaces: [] }), []);
});

test("anModelMedians: each model's median over its own per-session cost", () => {
  const rows = [
    { sessionId: "s1", project: "p", model: "opus", costUsd: 5 },
    { sessionId: "s2", project: "p", model: "opus", costUsd: 3 },
    { sessionId: "s1", project: "p", model: "haiku", costUsd: 1 },
  ];
  assert.deepEqual({ ...anModelMedians(rows) }, { opus: 4, haiku: 1 });
  assert.deepEqual({ ...anModelMedians([]) }, {});
  assert.deepEqual({ ...anModelMedians(undefined) }, {});
});

test("anModelMedians: rows of one session and model are one session's cost; a model named like a prototype key is data", () => {
  const rows = [
    { sessionId: "s1", project: "p", model: "opus", costUsd: 2 },
    { sessionId: "s1", project: "p", model: "opus", costUsd: 3 },
    { sessionId: "s2", project: "p", model: "opus", costUsd: 1 },
    { sessionId: "s1", project: "p", model: "constructor", costUsd: 4 },
  ];
  const m = anModelMedians(rows);
  assert.equal(m.opus, 3);
  assert.equal(m.constructor, 4);
});
