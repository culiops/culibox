"use strict";

const test = require("node:test");
const assert = require("node:assert");
const {
  fmtCompact, usd, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
  foldProjects, sessionCostBuckets, topSessionsByCost, distinctSessions,
  prevWindow, pctDelta, dowBuckets, modelColor, collapseZeroCost, pricedModelIds,
  daysBetween, clampRange, stackedLayout, anCountText, anSessionKpis, projShort,
  anToolGroups, anModelFold, anPct, anModelCells, anProjectBars, anProjectRow, anSessionRow,
  anYTicks, AN_PAGE_CHART, anLegendItems, anXLabels,
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
// The data the view reads, folded the way anLoad folds it (topSessions, byProject, sessionCount).
const sessionData = (rows) => ({
  sessionCount: distinctSessions(rows),
  topSessions: topSessionsByCost(rows, 18),
  byProject: foldProjects(rows, 14),
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

test("anToolGroups: a group per kind in the order met, ten rows at most, the kind's colour, the accent for an unknown kind", () => {
  const tools = [];
  for (let i = 1; i <= 12; i++) tools.push({ kind: "skill", name: "s" + i, records: i });
  tools.push({ kind: "mcp_server", name: "docs", records: 3 }, { kind: "plugin", name: "p", records: 0 }, { kind: "novel", name: "n", records: 2 }, { kind: "mcp_tool", name: "t", records: 1 });
  const groups = anToolGroups(tools);
  assert.deepEqual(groups.map((g) => g.kind), ["skill", "mcp_server", "plugin", "novel", "mcp_tool"]);
  assert.deepEqual(groups.map((g) => g.color), ["var(--an-2)", "var(--an-3)", "var(--an-4)", "var(--accent)", "var(--an-1)"]);
  assert.equal(groups[0].items.length, 10);
  assert.deepEqual(groups[0].items.map((t) => t.name), ["s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10"], "the first ten, in the order given");
  assert.deepEqual(groups.map((g) => g.max), [10, 3, 1, 2, 1], "the longest bar in the group's own rows; never under 1");
  assert.deepEqual(anToolGroups([]), []);
  assert.deepEqual(anToolGroups(undefined), []);
});

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
  const byProject = foldProjects([
    { project: "-home-dev-acme-api", model: "a", input: 10, prompts: 2, costUsd: 1 },
    { project: "-home-dev-acme-api", model: "b", input: 5, prompts: 1, costUsd: 3 },
    { project: "-home-dev-acme-web", model: "a", input: 1500, prompts: 7, costUsd: 2 },
  ], 14);
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
const toolRows = (kinds) => kinds.map((k, i) => ({ kind: k, name: "n" + i, records: 5 + i }));
function toolCards(page, kinds) {
  const { ctx, doc } = anPage(page);
  vm.runInContext("AN.data = { tools: " + JSON.stringify(toolRows(kinds)) + " }", ctx);
  const root = new Node("div", doc);
  ctx.anTools(root);
  return root.querySelectorAll(".an-card");
}

test("a Tools card head draws the glyph of its declared kind at 16, inside the heading before the kind word", () => {
  const want = { mcp_server: "server", plugin: "puzzle", mcp_tool: "plug", skill: "book-open" };
  const kinds = Object.keys(want);
  const cards = toolCards(true, kinds);
  assert.equal(cards.length, 4);
  cards.forEach((c, i) => {
    const head = c.querySelector(".card-head");
    const h3 = head.querySelector("h3");
    assert.equal(h3.textContent, kinds[i], "the words are the kind, unchanged");
    assert.equal(head.querySelectorAll("svg").length, 1, kinds[i]);
    assert.equal(h3.children[0].tag, "svg", "the glyph is the heading's first child");
    assert.equal(h3.children[0].getAttribute("data-icon"), want[kinds[i]], kinds[i]);
    assert.equal(h3.children[0].getAttribute("width"), "16");
    assert.equal(h3.children[0].getAttribute("aria-hidden"), "true");
    assert.equal(head.children.length, 1, "the head holds only the heading (no sibling glyph for the flex gap to push away)");
  });
});

test("the Tools glyph is 6px from its word: a margin on the h3's glyph, in the desktop page's rules", () => {
  const css = readStatic("desktop.css").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /#page-host > #analytics-modal\.as-page \.an-card \.card-head > h3 > \.ui-icon \{ margin-right: 6px; \}/);
});

test("anToolGroups: a hostile kind gets the default colour string, never a function", () => {
  for (const kind of ["constructor", "__proto__", "toString", "hasOwnProperty"]) {
    const [g] = anToolGroups([{ kind, name: "x", records: 3 }]);
    assert.equal(g.kind, kind);
    assert.equal(g.color, "var(--accent)", kind);
    assert.equal(g.items.length, 1);
  }
  assert.equal(anToolGroups([{ kind: "skill", name: "x", records: 1 }])[0].color, "var(--an-2)");
});

test("a Tools card head of an unknown kind keeps its words and draws no glyph", () => {
  const kinds = ["constructor", "__proto__", "toString", "weird"];
  const cards = toolCards(true, kinds);
  assert.equal(cards.length, 4, "a card per group");
  for (const c of cards) assert.equal(c.querySelector(".card-head").querySelector("svg"), null);
  assert.equal(cards[0].querySelector(".card-head h3").textContent, "constructor");
});

test("the Tools card rows stay plain", () => {
  const cards = toolCards(true, ["skill", "mcp_server"]);
  for (const c of cards) assert.equal(c.querySelector(".an-card-body").querySelector("svg"), null);
});

test("the phone's Tools boxes (not the page) draw no glyph", () => {
  const { ctx, doc } = anPage(false);
  vm.runInContext("AN.data = { tools: " + JSON.stringify(toolRows(["skill", "plugin", "mcp_tool", "mcp_server"])) + " }", ctx);
  const root = new Node("div", doc);
  ctx.anTools(root);
  assert.equal(root.querySelectorAll("svg").length, 0);
  assert.equal(root.querySelectorAll("h4").length, 4);
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
