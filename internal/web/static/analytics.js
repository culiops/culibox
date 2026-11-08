"use strict";

// analytics.js renders the token/cost Analytics view (fed by /api/analytics/*).
// The pure transforms at the top are unit-tested (node --test); the render code
// below builds DOM with textContent only and depends on the global api() from
// app.js. $-first: dollars are the primary number, tokens the detail. The panel
// is DESCRIPTIVE — "what did I spend, on what, over time" — never "which model is
// better" (token/prompt volume tracks the task, not model quality).

// ---- pure formatters --------------------------------------------------------

// fmtCompact: 0..999 as-is, then k / M / B. Mirrors the terminal usage readout.
function fmtCompact(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 1_000_000) return Math.round(n / 1000) + "k";
  if (n < 1_000_000_000) return (n / 1_000_000).toFixed(1) + "M";
  return (n / 1_000_000_000).toFixed(1) + "B";
}

// usd: compact dollars. <$100 keeps cents; larger rounds; ≥$1k uses "k".
function usd(n) {
  n = Number(n) || 0;
  if (n >= 1000) return "$" + (n / 1000).toFixed(1) + "k";
  if (n >= 100) return "$" + Math.round(n);
  return "$" + n.toFixed(2);
}

// tokensPerPrompt: total tokens / prompts, or 0 when there are none.
function tokensPerPrompt(totalTokens, prompts) {
  return prompts > 0 ? Math.round(totalTokens / prompts) : 0;
}

// ---- pure transforms (raw API rows → render-ready shapes) --------------------

// modelsByCost: ordered list of model ids, most expensive first, from any rows
// that carry {model, costUsd}.
function modelsByCost(rows) {
  const c = new Map();
  for (const r of rows || []) c.set(r.model, (c.get(r.model) || 0) + (r.costUsd || 0));
  return [...c.entries()].sort((a, b) => b[1] - a[1]).map((e) => e[0]);
}

// foldCostByDay: /summary byDayModel rows → per-day cost stacks + window totals.
function foldCostByDay(rows) {
  const byDay = new Map();
  const totals = { cost: 0, tokens: 0, prompts: 0, input: 0, output: 0, cacheRead: 0, cacheCreation: 0 };
  for (const r of rows || []) {
    const tok = (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    totals.cost += r.costUsd || 0;
    totals.tokens += tok;
    totals.prompts += r.prompts || 0;
    totals.input += r.input || 0;
    totals.output += r.output || 0;
    totals.cacheRead += r.cacheRead || 0;
    totals.cacheCreation += r.cacheCreation || 0;
    let d = byDay.get(r.day);
    if (!d) { d = { day: r.day, cost: 0, segs: {} }; byDay.set(r.day, d); }
    d.cost += r.costUsd || 0;
    d.segs[r.model] = (d.segs[r.model] || 0) + (r.costUsd || 0);
  }
  const days = [...byDay.values()].sort((a, b) => (a.day < b.day ? -1 : 1));
  const maxCost = days.reduce((m, d) => Math.max(m, d.cost), 0);
  return { days, models: modelsByCost(rows), maxCost, totals };
}

// normModels: /models byModel rows → render rows with derived tokens + out/prompt,
// cost-descending.
function normModels(rows) {
  return (rows || [])
    .map((r) => {
      const tokens = (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
      return {
        model: r.model,
        input: r.input || 0, output: r.output || 0,
        cacheRead: r.cacheRead || 0, cacheCreation: r.cacheCreation || 0,
        tokens, prompts: r.prompts || 0, sessions: r.sessions || 0,
        cost: r.costUsd || 0,
        outPerPrompt: r.prompts ? Math.round((r.output || 0) / r.prompts) : 0,
      };
    })
    .sort((a, b) => b.cost - a.cost);
}

// collapseZeroCost: fold every $0-cost model (unknown/synthetic families — they
// price to exactly 0) into a single "other (unpriced)" row, so the model views
// aren't cluttered with rows that cost nothing. Activity is annotated, not hidden:
// the other row keeps the summed tokens/prompts/sessions. Input is normModels output.
function collapseZeroCost(rows) {
  const priced = [], zero = [];
  for (const r of rows || []) (r.cost > 0 ? priced : zero).push(r);
  if (!zero.length) return priced;
  const other = {
    model: "other (unpriced)", cost: 0,
    input: 0, output: 0, cacheRead: 0, cacheCreation: 0,
    tokens: 0, prompts: 0, sessions: 0, outPerPrompt: 0,
  };
  for (const z of zero) {
    other.input += z.input || 0; other.output += z.output || 0;
    other.cacheRead += z.cacheRead || 0; other.cacheCreation += z.cacheCreation || 0;
    other.tokens += z.tokens || 0; other.prompts += z.prompts || 0; other.sessions += z.sessions || 0;
  }
  return [...priced, other];
}

// pricedModelIds: model ids that actually cost money, order preserved — used for
// legends and colour assignment so unpriced models don't take a colour slot.
function pricedModelIds(rows) {
  return (rows || []).filter((r) => r.cost > 0).map((r) => r.model);
}

// foldProjects: /projects per-(project,model) rows → per-project totals + model
// mix, cost-descending, capped at topN (0 = all).
function foldProjects(rows, topN) {
  const byProj = new Map();
  for (const r of rows || []) {
    let p = byProj.get(r.project);
    if (!p) { p = { project: r.project, cost: 0, tokens: 0, prompts: 0, byModel: {} }; byProj.set(r.project, p); }
    const tok = (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    p.cost += r.costUsd || 0;
    p.tokens += tok;
    p.prompts += r.prompts || 0;
    p.byModel[r.model] = (p.byModel[r.model] || 0) + (r.costUsd || 0);
  }
  let list = [...byProj.values()].sort((a, b) => b.cost - a.cost);
  if (topN && list.length > topN) list = list.slice(0, topN);
  return list;
}

// sessionCostBuckets: /sessions rows → a fixed cost histogram (one row per
// distinct session, summed across its day/model rows).
function sessionCostBuckets(rows) {
  const perSession = new Map();
  for (const r of rows || []) {
    const k = (r.sessionId || "") + String.fromCharCode(31) + (r.project || "");
    perSession.set(k, (perSession.get(k) || 0) + (r.costUsd || 0));
  }
  const buckets = [
    { label: "<$1", lo: 0, hi: 1 },
    { label: "$1–5", lo: 1, hi: 5 },
    { label: "$5–15", lo: 5, hi: 15 },
    { label: "$15–40", lo: 15, hi: 40 },
    { label: "$40+", lo: 40, hi: Infinity },
  ];
  const out = buckets.map((b) => ({ label: b.label, count: 0 }));
  for (const cost of perSession.values()) {
    const i = buckets.findIndex((b) => cost >= b.lo && cost < b.hi);
    if (i >= 0) out[i].count++;
  }
  return out;
}

// topSessionsByCost: /sessions rows → sessions aggregated across their day/model
// rows, most expensive first, limited to `limit`.
function topSessionsByCost(rows, limit) {
  const agg = new Map();
  for (const r of rows || []) {
    const k = (r.sessionId || "") + String.fromCharCode(31) + (r.project || "");
    let s = agg.get(k);
    if (!s) { s = { sid: (r.sessionId || "").slice(0, 8), day: "", project: r.project || "", models: new Set(), tokens: 0, cost: 0, prompts: 0 }; agg.set(k, s); }
    s.day = s.day > r.day ? s.day : r.day;
    s.models.add(r.model);
    s.tokens += (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    s.cost += r.costUsd || 0;
    s.prompts += r.prompts || 0;
  }
  return [...agg.values()]
    .map((s) => ({ ...s, model: s.models.size === 1 ? [...s.models][0] : "mixed" }))
    .sort((a, b) => b.cost - a.cost)
    .slice(0, limit || 20);
}

// distinctSessions: count of distinct (sessionId, project) pairs in /sessions rows.
function distinctSessions(rows) {
  const s = new Set();
  for (const r of rows || []) s.add((r.sessionId || "") + String.fromCharCode(31) + (r.project || ""));
  return s.size;
}

// prevWindow: the equal-length window immediately before [from,to] (inclusive
// YYYY-MM-DD). Used for period-over-period comparison.
function prevWindow(from, to) {
  const DAY = 86400000;
  const f = Date.parse(from + "T00:00:00Z"), t = Date.parse(to + "T00:00:00Z");
  const len = Math.round((t - f) / DAY) + 1; // inclusive day count
  const prevTo = new Date(f - DAY), prevFrom = new Date(f - DAY * len);
  const iso = (d) => d.toISOString().slice(0, 10);
  return { from: iso(prevFrom), to: iso(prevTo) };
}

// daysBetween: inclusive count of days from `from` to `to` (YYYY-MM-DD, UTC).
function daysBetween(from, to) {
  const DAY = 86400000;
  return Math.round((Date.parse(to + "T00:00:00Z") - Date.parse(from + "T00:00:00Z")) / DAY) + 1;
}

// clampRange: return {from,to} ordered so from ≤ to (swap a reversed custom range).
function clampRange(a, b) {
  return a <= b ? { from: a, to: b } : { from: b, to: a };
}

// stackedLayout: position daily cost bars. In "date mode" (span ≤ 120 days) each
// bar sits at its true date offset within [from,to], so empty days show as gaps
// rather than being collapsed. For very wide spans (e.g. "All") it falls back to
// sequential index positioning, since date offsets would compress bars to nothing.
function stackedLayout(days, from, to) {
  const MAX_DATE_SPAN = 120;
  const span = from && to ? daysBetween(from, to) : 0;
  const dateMode = span > 0 && span <= MAX_DATE_SPAN;
  const bars = (days || []).map((d, i) => ({
    day: d.day, segs: d.segs, cost: d.cost,
    slot: dateMode ? daysBetween(from, d.day) - 1 : i,
  }));
  return { dateMode, slots: dateMode ? span : (days || []).length, bars };
}

// pctDelta: integer percent change cur vs prev; null when prev is 0 (no baseline).
function pctDelta(cur, prev) {
  if (!prev) return null;
  return Math.round((cur - prev) / prev * 100);
}

// dowBuckets: /summary byDayModel rows → cost per weekday, Mon..Sun (index 0..6).
function dowBuckets(rows) {
  const out = [0, 0, 0, 0, 0, 0, 0];
  for (const r of rows || []) {
    const d = new Date(r.day + "T00:00:00Z").getUTCDay(); // 0=Sun..6=Sat
    const idx = (d + 6) % 7; // → 0=Mon..6=Sun
    out[idx] += r.costUsd || 0;
  }
  return out;
}

// ---- model colour (categorical, stable per index) --------------------------
// The hues live in CSS as --an-1..7: app.css keeps the Catppuccin set on mobile,
// desktop.css swaps in a palette with no green, amber or red (those are status
// colours there). DOM bars take the var() through style.background, SVG marks
// through style.fill / style.stroke.
const AN_SLOTS = 7;
function modelColor(models, model) {
  const i = (models || []).indexOf(model);
  return "var(--an-" + (((i < 0 ? 0 : i) % AN_SLOTS) + 1) + ")";
}
function shortModel(m) { return (m || "(unknown)").replace(/^claude-/, "").replace(/-20\d{6}$/, ""); }
function projShort(p) {
  if (!p) return "";
  return p.replace(/^-home-[^-]+-/, "").replace(/^-/, "");
}

// ---- the desktop page's KPI figures -------------------------------------------------------------------------------

// anCountText: a count in a KPI tile: thousands separators below 10,000 ("1,420"), fmtCompact from there.
function anCountText(n) {
  n = Number(n) || 0;
  return n < 10000 ? n.toLocaleString("en-US") : fmtCompact(n);
}

// anSessionKpis: the three tiles of the Sessions view (digest-design §A3), from the loaded window's sessionCount, topSessions
// and byProject (both cost-descending). The two "Costliest" tiles carry a project in the label and a COST in the value: the top
// session's project and cost, the top project's name and cost. With none, a dash and the plain label (and a project with no
// name leaves no dangling " · ").
function anSessionKpis(d) {
  const named = (label, name) => (name ? label + " · " + name : label);
  const top = (d.topSessions || [])[0];
  const proj = (d.byProject || [])[0];
  return [
    { value: anCountText(d.sessionCount), label: "Sessions in this window" },
    { value: top ? usd(top.cost) : "—", label: named("Costliest session", top && projShort(top.project)) },
    { value: proj ? usd(proj.cost) : "—", label: named("Costliest project", proj && projShort(proj.project)) },
  ];
}

// ---- what the Models, Projects, Sessions and Tools views say: one preparation for both skins ----------------------
// The phone's modal (anModels, anProjects, anSessions, anTools) and the desktop page (the an...Page builders) draw different DOM
// from the same words, figures, colours and caps. Those live here and are called by both, so a change reaches both.

const AN_COPY = {
  models: {
    title: "Per-model breakdown", hint: "Cost, volume & efficiency · window",
    shareTitle: "Why $ ≠ token count", shareHint: "cache-read dominates tokens but is cheap; output is tiny but expensive — the two rankings disagree",
    tokens: "Share of tokens", cost: "Share of cost",
  },
  projects: { title: "Cost by project", hint: "Bar split shows the model mix inside each project · window", detail: "Detail" },
  sessions: { title: "Top sessions by cost" },
  tools: { title: "Skills · plugins · MCP", hint: "What the agents actually invoked · by records", none: "No skill/plugin/MCP attribution in this window (recorded only on recent transcripts)." },
};
// A table's columns and where its numbers start (anHead gives the th from there on .num; the page capitalises the labels).
const AN_TABLES = {
  models: [["model", "cost", "% cost", "tokens", "prompts", "out/prompt", "cache-read"], 1],
  projects: [["project", "cost", "tokens", "prompts"], 1],
  sessions: [["session", "day", "project", "model", "tokens", "prompts", "cost"], 4],
};
const AN_TOOL_COLORS = { skill: "var(--an-2)", plugin: "var(--an-4)", mcp_server: "var(--an-3)", mcp_tool: "var(--an-1)" };

// anToolGroups: tools rows ({kind, name, records}) as one group per kind, in the order the kinds are met: its colour (the accent
// for a kind not listed), its first ten rows and the longest bar among them (never under 1).
function anToolGroups(tools) {
  const kinds = {};
  for (const t of tools || []) (kinds[t.kind] = kinds[t.kind] || []).push(t);
  return Object.keys(kinds).map((kind) => {
    const items = kinds[kind].slice(0, 10);
    return { kind, color: AN_TOOL_COLORS[kind] || "var(--accent)", items, max: Math.max(1, ...items.map((t) => t.records)) };
  });
}

// anPct: v as a whole percent of total, "59%".
function anPct(v, total) { return Math.round(v / total * 100) + "%"; }

// anModelFold: the Models view's data: normModels rows with the $0 models folded into "other (unpriced)", the totals the shares
// divide by (never 0) and the longest token and cost bars (never under 1).
function anModelFold(byModel) {
  const rows = collapseZeroCost(byModel);
  return {
    rows,
    totalCost: rows.reduce((s, x) => s + x.cost, 0) || 1,
    totalTokens: rows.reduce((s, x) => s + x.tokens, 0) || 1,
    maxTokens: Math.max(1, ...rows.map((x) => x.tokens)),
    maxCost: Math.max(1, ...rows.map((x) => x.cost)),
  };
}

// anModelCells: a model row's figures after its name, as the table prints them (a fold's row).
function anModelCells(x, fold) {
  return {
    cost: usd(x.cost), pct: anPct(x.cost, fold.totalCost), tokens: fmtCompact(x.tokens), prompts: String(x.prompts),
    outPerPrompt: fmtCompact(x.outPerPrompt), cache: (x.tokens ? Math.round(x.cacheRead / x.tokens * 100) : 0) + "%",
  };
}

// anProjectBars: foldProjects rows as the stacked bars: each project's name, cost and segments (dearest model first, coloured by
// its place in `models`, titled "model $cost"), and the longest bar (never under 1).
function anProjectBars(byProject, models) {
  return {
    max: Math.max(1, ...byProject.map((x) => x.cost)),
    bars: byProject.map((p) => ({
      name: projShort(p.project), cost: p.cost,
      segs: Object.entries(p.byModel).sort((a, b) => b[1] - a[1]).map(([m, v]) => ({ v, c: modelColor(models, m), t: shortModel(m) + " " + usd(v) })),
    })),
  };
}

// anProjectRow: a project's detail row as text.
function anProjectRow(p) {
  return { name: projShort(p.project), cost: usd(p.cost), tokens: fmtCompact(p.tokens), prompts: String(p.prompts) };
}

// anSessionRow: a top session (topSessionsByCost) as text. `mixed` marks the several-model case, whose model is the word "mixed".
function anSessionRow(s) {
  return {
    sid: s.sid, day: s.day, project: projShort(s.project), model: shortModel(s.model), mixed: s.model === "mixed",
    tokens: fmtCompact(s.tokens), prompts: String(s.prompts), cost: usd(s.cost),
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    fmtCompact, usd, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
    foldProjects, sessionCostBuckets, topSessionsByCost, distinctSessions,
    prevWindow, pctDelta, dowBuckets, modelColor, shortModel, projShort,
    collapseZeroCost, pricedModelIds, daysBetween, clampRange, stackedLayout,
    anCountText, anSessionKpis,
    anToolGroups, anModelFold, anPct, anModelCells, anProjectBars, anProjectRow, anSessionRow,
  };
}

// ---- rendering (browser only) -----------------------------------------------

const AN = { tab: "overview", from: null, to: null, range: "30d", compare: false, customOpen: false, data: null };

// summaryRowsCache holds the current window's byDayModel rows so the compare pass
// can bucket the current period by day-of-week without re-fetching.
let summaryRowsCache = [];

const RANGES = { "7d": 6, "30d": 29, "90d": 89, all: null };

function anTodayUTC() { return new Date().toISOString().slice(0, 10); }
function anRangeFrom(key) {
  if (key === "all") return "2000-01-01";
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - RANGES[key]);
  return d.toISOString().slice(0, 10);
}

async function openAnalytics() {
  if (typeof openDialog === "function") openDialog("analytics-modal");
  AN.range = "30d";
  AN.compare = false;
  AN.customOpen = false;
  AN.tab = "overview";
  AN.to = anTodayUTC();
  AN.from = anRangeFrom("30d");
  syncTabButtons();
  anSyncPageControls();
  await anLoad();
}

function syncTabButtons() {
  for (const b of document.querySelectorAll("#an-tabs button")) {
    b.classList.toggle("act", b.dataset.antab === AN.tab);
  }
}

// anSetTab: choose a view. The phone's tab strip (.act) and the page's view switch (aria-pressed) both follow AN.tab; the
// window's data is already loaded, so it only re-renders (no refetch).
function anSetTab(tab) {
  AN.tab = tab;
  syncTabButtons();
  anSyncPageControls();
  anRender();
}

// anRescan: read the transcripts again, then reload the window. `btn` is the phone's #analytics-refresh or the page's
// #an-rescan: it disables itself and says "Scanning…" meanwhile. On the page it is the control in use, so focus comes
// back to it if the disabled button lost it (and only then: a focus the user moved on is left where they put it).
async function anRescan(btn) {
  const keep = anIsPage() && document.activeElement === btn;
  btn.disabled = true;
  btn.textContent = "Scanning…";
  try { await api("/api/analytics/ingest", { method: "POST" }); await anLoad(); }
  catch (_) { /* leave the current view; the range line still shows */ }
  finally {
    btn.disabled = false;
    btn.textContent = "Re-scan";
    const now = document.activeElement;
    if (keep && (!now || now === document.body)) btn.focus();
  }
}

async function anLoad() {
  const body = document.getElementById("analytics-body");
  if (!body) return;
  body.replaceChildren(anEmpty("Loading…"));
  const q = `from=${AN.from}&to=${AN.to}`;
  let summary, models, projects, sessions, tools;
  try {
    [summary, models, projects, sessions, tools] = await Promise.all([
      api(`/api/analytics/summary?${q}`),
      api(`/api/analytics/models?${q}`),
      api(`/api/analytics/projects?${q}`),
      api(`/api/analytics/session-costs?${q}`),
      api(`/api/analytics/tools?${q}`),
    ]);
  } catch (_) {
    body.replaceChildren(anEmpty("Could not load analytics."));
    return;
  }
  const byDayModel = (summary && summary.byDayModel) || [];
  summaryRowsCache = byDayModel; // current window rows, for the compare pass's day-of-week
  const sessRows = (sessions && sessions.sessions) || [];
  AN.data = {
    from: summary ? summary.from : AN.from,
    to: summary ? summary.to : AN.to,
    costByDay: foldCostByDay(byDayModel),
    byModel: normModels((models && models.byModel) || []),
    byProject: foldProjects((projects && projects.byProject) || [], 14),
    sessions: sessRows,
    sessionHist: sessionCostBuckets(sessRows),
    topSessions: topSessionsByCost(sessRows, 18),
    sessionCount: distinctSessions(sessRows),
    tools: (tools && tools.tools) || [],
    cmp: null,
  };
  AN.data.models = AN.data.costByDay.models;
  AN.data.pricedModels = pricedModelIds(AN.data.byModel); // for legends: drop $0 models
  if (AN.compare) await anLoadCompare();
  anRender();
}

async function anLoadCompare() {
  const pw = prevWindow(AN.from, AN.to);
  const q = `from=${pw.from}&to=${pw.to}`;
  let ps, pm, pp, psess;
  try {
    [ps, pm, pp, psess] = await Promise.all([
      api(`/api/analytics/summary?${q}`),
      api(`/api/analytics/models?${q}`),
      api(`/api/analytics/projects?${q}`),
      api(`/api/analytics/session-costs?${q}`),
    ]);
  } catch (_) {
    return; // comparison is best-effort; leave cmp null
  }
  const curTotals = AN.data.costByDay.totals;
  const prevFold = foldCostByDay((ps && ps.byDayModel) || []);
  const curModels = new Map(AN.data.byModel.map((m) => [m.model, m]));
  const prevModels = normModels((pm && pm.byModel) || []);
  const prevModelMap = new Map(prevModels.map((m) => [m.model, m]));
  const allModels = AN.data.models.slice();
  for (const m of prevModels) if (!allModels.includes(m.model)) allModels.push(m.model);
  const byModel = allModels.map((m) => {
    const c = curModels.get(m), p = prevModelMap.get(m);
    return {
      model: m,
      curCost: c ? c.cost : 0, prevCost: p ? p.cost : 0,
      curTok: c ? c.tokens : 0, prevTok: p ? p.tokens : 0,
    };
  });
  const curProj = new Map(AN.data.byProject.map((x) => [x.project, x.cost]));
  const prevProjList = foldProjects((pp && pp.byProject) || [], 0);
  const prevProj = new Map(prevProjList.map((x) => [x.project, x.cost]));
  const projKeys = new Set([...curProj.keys(), ...prevProj.keys()]);
  const byProject = [...projKeys]
    .map((k) => ({ project: k, curCost: curProj.get(k) || 0, prevCost: prevProj.get(k) || 0 }))
    .sort((a, b) => (b.curCost + b.prevCost) - (a.curCost + a.prevCost))
    .slice(0, 12);
  const curDow = dowBuckets(summaryRowsCache);
  const prevDow = dowBuckets((ps && ps.byDayModel) || []);
  const DOW = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  const dow = DOW.map((d, i) => ({ day: d, cur: curDow[i], prev: prevDow[i] }));
  AN.data.cmp = {
    curFrom: AN.from, curTo: AN.to, prevFrom: pw.from, prevTo: pw.to,
    models: allModels, // union of current+previous, so prev-only models get a distinct colour
    cur: { cost: curTotals.cost, tokens: curTotals.tokens, prompts: curTotals.prompts, sessions: AN.data.sessionCount },
    prev: {
      cost: prevFold.totals.cost, tokens: prevFold.totals.tokens, prompts: prevFold.totals.prompts,
      sessions: psess ? distinctSessions(psess.sessions || []) : 0,
    },
    byModel, byProject, dow,
  };
}

function anRender() {
  const body = document.getElementById("analytics-body");
  if (!body) return;
  const range = document.getElementById("analytics-range");
  if (range && AN.data) range.textContent = `${AN.data.from} → ${AN.data.to} (UTC)`;
  body.replaceChildren();
  // On the desktop page the view switch, range, Compare and the window line are the page's head and toolbar: static nodes
  // (index.html) that stay where they are and are brought up to date in place. The modal keeps its control strip at the
  // top of the body.
  const ctl = document.getElementById("an-ctl");
  if (ctl) ctl.replaceChildren();
  if (anIsPage()) anSyncPageControls();
  else body.appendChild(anControls());
  const d = AN.data;
  if (!d || !d.costByDay.days.length) {
    body.appendChild(anEmpty("No usage recorded in this window. Try a wider range or Re-scan."));
    return;
  }
  const R = { overview: anOverview, models: anModels, projects: anProjects, sessions: anSessions, tools: anTools };
  (R[AN.tab] || anOverview)(body);
  // Time-series charts read left→right oldest→newest; open scrolled to the most
  // recent day so a window with a leading empty gap doesn't render blank.
  const tw = body.querySelector(".an-timewrap");
  if (tw) tw.scrollLeft = tw.scrollWidth;
  anFitTimeChart();
}

// anFitTimeChart: on the desktop page the daily chart is redrawn at its card's
// width, so it fills a wide card and fits a narrow one (the modal keeps its
// fixed 720px floor and scrolls). Called after each render and on resize.
function anFitTimeChart() {
  const body = document.getElementById("analytics-body");
  const d = AN.data;
  if (!body || !d || !anIsPage()) return;
  const tw = body.querySelector(".an-timewrap");
  if (!tw) return;
  const w = Math.floor(tw.clientWidth);
  if (w <= 0 || String(w) === tw.dataset.fitw) return;
  const fresh = anStackedTime(d.costByDay, d.from, d.to, w);
  fresh.dataset.fitw = String(w);
  tw.replaceWith(fresh);
  fresh.scrollLeft = fresh.scrollWidth;
}
if (typeof document !== "undefined" && typeof ResizeObserver === "function") {
  const anBody = document.getElementById("analytics-body");
  if (anBody) new ResizeObserver(() => anFitTimeChart()).observe(anBody);
}

// anIsPage: the dialog is re-parented as a desktop page (pagehost.js).
function anIsPage() {
  const dlg = document.getElementById("analytics-modal");
  return !!(dlg && dlg.classList.contains("as-page"));
}

// the Custom range's date inputs + Apply (the phone's inline strip), as one list of nodes
function anCustomInputs() {
  const from = el("input", "an-date"); from.type = "date"; from.value = AN.from || "";
  const to = el("input", "an-date"); to.type = "date"; to.value = AN.to || "";
  const dash = el("span", "an-clabel", "→");
  const apply = el("button", "an-chip", "Apply");
  apply.type = "button";
  apply.onclick = () => { anApplyCustom(from.value, to.value); };
  return [from, dash, to, apply];
}

// The range controls' actions, shared by the phone's chips and the page's seg. Each sets AN, brings the page's controls
// up to date at once (a no-op on the phone) and reloads or re-renders as it always did.

// anPickRange: a preset window ending today. The page's Custom row closes with it.
function anPickRange(key) {
  AN.range = key; AN.customOpen = false; AN.from = anRangeFrom(key); AN.to = anTodayUTC();
  anSyncPageControls();
  anLoad();
}

// anToggleCustom: show or hide the Custom range's date inputs. Nothing is fetched; the page only shows its row (the body
// does not change), the phone re-renders its strip.
function anToggleCustom() {
  AN.customOpen = !AN.customOpen;
  if (anIsPage()) anSyncPageControls();
  else anRender();
}

// anApplyCustom: load [f, t] as the window, a reversed pair swapped (clampRange). Nothing happens while a date is empty.
// Returns the ordered range, or null.
function anApplyCustom(f, t) {
  if (!f || !t) return null;
  const r = clampRange(f, t);
  AN.range = "custom"; AN.from = r.from; AN.to = r.to;
  anSyncPageControls();
  anLoad();
  return r;
}

// anToggleCompare: compare this window with the equal-length one before it.
function anToggleCompare() {
  AN.compare = !AN.compare;
  anSyncPageControls();
  anLoad();
}

// control strip of the phone's modal: range presets + compare toggle, the Custom range's dates inline. (The desktop page
// has no strip: its toolbar is static markup, anSyncPageControls.)
function anControls() {
  const wrap = el("div", "an-controls");
  const lbl = el("span", "an-clabel", "Range");
  wrap.appendChild(lbl);
  for (const key of ["7d", "30d", "90d", "all"]) {
    const b = el("button", "an-chip" + (AN.range === key ? " on" : ""), key === "all" ? "All" : key);
    b.type = "button";
    b.onclick = () => anPickRange(key);
    wrap.appendChild(b);
  }
  // Custom range: a chip that reveals two date inputs + Apply.
  const custom = el("button", "an-chip" + (AN.range === "custom" ? " on" : ""), "Custom…");
  custom.type = "button";
  custom.onclick = anToggleCustom;
  wrap.appendChild(custom);
  if (AN.customOpen || AN.range === "custom") wrap.append(...anCustomInputs());
  const spacer = el("span"); spacer.style.width = "12px"; wrap.appendChild(spacer);
  const cmp = el("button", "an-chip" + (AN.compare ? " on" : ""), "⇄ Compare vs prev period");
  cmp.type = "button";
  cmp.title = "Compare this window to the equal-length window immediately before it";
  cmp.onclick = anToggleCompare;
  wrap.appendChild(cmp);
  return wrap;
}

// ---- the desktop page's head and toolbar (index.html .an-phead, .an-ptools, .an-pcustom) -----------------------
// Static nodes: nothing here rebuilds them, so the control a click came from keeps focus.

// anSyncPageControls: bring them up to date with AN: aria-pressed on the view and range items, Custom… and Compare
// (Custom… mirrors the phone's `.on`: lit by an applied custom range, not by the row being open), the Custom row's hidden,
// and the description's window (the loaded data's, else the requested one). The row's dates default to the window when
// the row opens. A no-op unless the dialog is the page.
function anSyncPageControls() {
  if (!anIsPage()) return;
  const dlg = document.getElementById("analytics-modal");
  const press = (b, on) => b.setAttribute("aria-pressed", on ? "true" : "false");
  for (const b of dlg.querySelectorAll("#an-pview [data-antab]")) press(b, b.dataset.antab === AN.tab);
  for (const b of dlg.querySelectorAll("#an-prange [data-range]")) press(b, AN.range === b.dataset.range);
  const cmp = dlg.querySelector("#an-compare");
  if (cmp) press(cmp, AN.compare);
  const row = dlg.querySelector(".an-pcustom");
  if (row) {
    const open = AN.customOpen || AN.range === "custom";
    if (open && row.hidden) {
      const dates = row.querySelectorAll(".an-date");
      if (dates[0]) dates[0].value = AN.from || "";
      if (dates[1]) dates[1].value = AN.to || "";
    }
    row.hidden = !open;
  }
  const desc = dlg.querySelector("#an-pdesc");
  if (desc) {
    const d = AN.data;
    const from = d ? d.from : AN.from, to = d ? d.to : AN.to;
    desc.textContent = "Spend read from Claude Code transcripts" + (from && to ? ` · ${from} → ${to} (UTC)` : "");
  }
}

// anWirePage: the page's clicks, wired once at boot (app.js). A group's one handler finds the item by its data attribute.
function anWirePage() {
  const dlg = document.getElementById("analytics-modal");
  if (!dlg) return;
  const on = (sel, fn) => { const n = dlg.querySelector(sel); if (n) n.onclick = fn; };
  on("#an-pview", (e) => { const b = e.target.closest("button[data-antab]"); if (b) anSetTab(b.dataset.antab); });
  on("#an-prange", (e) => {
    const b = e.target.closest("button[data-range]");
    if (!b) return;
    if (b.dataset.range === "custom") anToggleCustom();
    else anPickRange(b.dataset.range);
  });
  on("#an-compare", anToggleCompare);
  on("#an-papply", () => {
    const dates = dlg.querySelectorAll(".an-pcustom .an-date");
    if (dates.length < 2) return;
    const r = anApplyCustom(dates[0].value, dates[1].value);
    if (r) { dates[0].value = r.from; dates[1].value = r.to; }
  });
}

// ---- Overview ---------------------------------------------------------------
function anOverview(root) {
  const d = AN.data, t = d.costByDay.totals;
  if (anIsPage()) {
    // The page's tiles: the kit's KPI row, the frame's labels (M3.1, §A2) and thousands separators on the counts.
    root.appendChild(anKpiRow([
      { value: usd(t.cost), label: "Total cost" },
      { value: fmtCompact(t.tokens), label: "Tokens" },
      { value: anCountText(d.sessionCount), label: "Sessions" },
      { value: anCountText(t.prompts), label: "Prompts" },
      { value: usd(d.sessionCount ? t.cost / d.sessionCount : 0), label: "Avg per session" },
    ]));
  } else {
    const kpis = el("div", "an-kpis");
    const tiles = [
      ["cost", usd(t.cost), "total cost"],
      ["", fmtCompact(t.tokens), "tokens"],
      ["", String(d.sessionCount), "sessions"],
      ["", String(t.prompts), "prompts"],
      ["cost", usd(d.sessionCount ? t.cost / d.sessionCount : 0), "avg / session"],
    ];
    for (const [cls, v, l] of tiles) kpis.appendChild(anKpi(cls, v, l));
    root.appendChild(kpis);
  }

  const sec = anSection("Cost over time", "Stacked by model · daily");
  const box = el("div", "an-box");
  box.appendChild(anStackedTime(d.costByDay, d.from, d.to));
  anLegend(box, d.pricedModels, d.models);
  sec.appendChild(box);
  root.appendChild(sec);

  const g = el("div", "an-grid2");
  const b1 = el("div", "an-box"); b1.appendChild(el("h4", null, "Cost by model"));
  const modelsC = collapseZeroCost(d.byModel);
  const maxM = Math.max(1, ...modelsC.map((x) => x.cost));
  for (const x of modelsC) anHbar(b1, shortModel(x.model), x.cost, maxM, anModelColor(d.models, x.model), usd);
  const b2 = el("div", "an-box"); b2.appendChild(el("h4", null, "Cost by project (top)"));
  const maxP = Math.max(1, ...d.byProject.map((x) => x.cost));
  for (const p of d.byProject.slice(0, 10)) {
    const segs = Object.entries(p.byModel).sort((a, b) => b[1] - a[1]).map(([m, v]) => ({ v, c: modelColor(d.models, m), t: shortModel(m) + " " + usd(v) }));
    anHbarSegs(b2, projShort(p.project), p.cost, maxP, segs, usd);
  }
  g.append(b1, b2);
  const sec2 = anSection("Where the money goes"); sec2.appendChild(g);
  root.appendChild(sec2);

  if (d.cmp) root.appendChild(anCompareBlock());

  root.appendChild(anNote("Descriptive only. Cross-model differences reflect the <b>task</b> you gave each model, not model quality — this panel answers <i>“what did I spend”</i>, not <i>“which model is better”</i>."));
}

// ---- Models -----------------------------------------------------------------
function anModels(root) {
  if (anIsPage()) return anModelsPage(root);
  const d = AN.data;
  const sec = anSection(AN_COPY.models.title, AN_COPY.models.hint);
  const fold = anModelFold(d.byModel); // $0 (unpriced) models are folded into "other"
  const box = el("div", "an-box");
  const tbl = el("table", "an-table");
  tbl.appendChild(anHead(...AN_TABLES.models));
  for (const x of fold.rows) {
    const tr = el("tr");
    const nm = el("td", "mono");
    const sw = el("span", "an-swatch"); sw.style.background = anModelColor(d.models, x.model);
    nm.append(sw, document.createTextNode(shortModel(x.model)));
    const c = anModelCells(x, fold);
    tr.append(nm, anTd(c.cost, "num"), anTd(c.pct, "dim num"), anTd(c.tokens, "dim num"), anTd(c.prompts, "dim num"),
      anTd(c.outPerPrompt, "dim num"), anTd(c.cache, "dim num"));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);

  const g = el("div", "an-grid2");
  const bt = el("div", "an-box"); bt.appendChild(el("h4", null, AN_COPY.models.tokens));
  for (const x of fold.rows) anHbar(bt, shortModel(x.model), x.tokens, fold.maxTokens, anModelColor(d.models, x.model), (v) => anPct(v, fold.totalTokens));
  const bc = el("div", "an-box"); bc.appendChild(el("h4", null, AN_COPY.models.cost));
  for (const x of fold.rows) anHbar(bc, shortModel(x.model), x.cost, fold.maxCost, anModelColor(d.models, x.model), (v) => anPct(v, fold.totalCost));
  g.append(bt, bc);
  const s2 = anSection(AN_COPY.models.shareTitle, AN_COPY.models.shareHint);
  s2.appendChild(g); root.appendChild(s2);
}

// ---- Projects ---------------------------------------------------------------
function anProjects(root) {
  if (anIsPage()) return anProjectsPage(root);
  const d = AN.data;
  const sec = anSection(AN_COPY.projects.title, AN_COPY.projects.hint);
  const box = el("div", "an-box");
  const pb = anProjectBars(d.byProject, d.models);
  for (const b of pb.bars) anHbarSegs(box, b.name, b.cost, pb.max, b.segs, usd);
  anLegend(box, d.models);
  sec.appendChild(box); root.appendChild(sec);

  const s2 = anSection(AN_COPY.projects.detail);
  const box2 = el("div", "an-box"); const tbl = el("table", "an-table");
  tbl.appendChild(anHead(...AN_TABLES.projects));
  for (const p of d.byProject) {
    const r = anProjectRow(p);
    const tr = el("tr");
    tr.append(anTd(r.name, "mono"), anTd(r.cost, "num"), anTd(r.tokens, "dim num"), anTd(r.prompts, "dim num"));
    tbl.appendChild(tr);
  }
  box2.appendChild(anTableWrap(tbl)); s2.appendChild(box2); root.appendChild(s2);
}

// ---- Sessions ---------------------------------------------------------------
function anSessions(root) {
  if (anIsPage()) return anSessionsPage(root);
  const d = AN.data;
  const g = el("div", "an-grid2");
  const bh = el("div", "an-box"); bh.appendChild(el("h4", null, "Session cost distribution"));
  const maxH = Math.max(1, ...d.sessionHist.map((b) => b.count));
  for (const b of d.sessionHist) anHbar(bh, b.label, b.count, maxH, "var(--accent)", (v) => String(v));
  const bk = el("div", "an-box"); bk.appendChild(el("h4", null, "Fleet at a glance"));
  const heavy = d.topSessions[0];
  const info = el("div", "an-fleet");
  // Build with textContent (project names are filesystem-derived — never innerHTML).
  const fleetLine = (label, value) => {
    const row = el("div"); row.append(document.createTextNode(label + " "));
    const b = el("b", null, value); row.appendChild(b); return row;
  };
  info.appendChild(fleetLine("Sessions (window):", String(d.sessionCount)));
  if (heavy) {
    const row = fleetLine("Costliest session:", usd(heavy.cost));
    row.append(document.createTextNode(" (" + projShort(heavy.project) + ")"));
    info.appendChild(row);
  }
  if (d.byProject[0]) info.appendChild(fleetLine("Costliest project:", projShort(d.byProject[0].project)));
  bk.appendChild(info);
  g.append(bh, bk);
  const s1 = anSection("Session shape"); s1.appendChild(g); root.appendChild(s1);

  const sec = anSection(AN_COPY.sessions.title, "Spot the runaway session burning the budget. Counts include subagent / synthetic transcripts.");
  const box = el("div", "an-box"); const tbl = el("table", "an-table");
  tbl.appendChild(anHead(...AN_TABLES.sessions));
  for (const s of d.topSessions) {
    const r = anSessionRow(s);
    const tr = el("tr");
    tr.append(anTd(r.sid, "mono"), anTd(r.day, "dim"), anTd(r.project, "mono"),
      anTd(r.model, "dim"), anTd(r.tokens, "dim num"), anTd(r.prompts, "dim num"), anTd(r.cost, "num"));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);
}

// ---- Tools ------------------------------------------------------------------
function anTools(root) {
  if (anIsPage()) return anToolsPage(root);
  const d = AN.data;
  const sec = anSection(AN_COPY.tools.title, AN_COPY.tools.hint);
  if (!d.tools.length) {
    sec.appendChild(anEmpty(AN_COPY.tools.none));
    root.appendChild(sec); return;
  }
  const g = el("div", "an-grid2");
  for (const grp of anToolGroups(d.tools)) {
    const box = el("div", "an-box"); box.appendChild(el("h4", null, grp.kind));
    for (const t of grp.items) anHbar(box, t.name, t.records, grp.max, grp.color, fmtCompact);
    g.appendChild(box);
  }
  sec.appendChild(g); root.appendChild(sec);
}

// ---- the desktop page's views (Models, Projects, Sessions, Tools) ------------------------------------------------------
// The phone's functions above return into these under anIsPage(); everything the phone draws is those functions' own code.
// The words, figures, colours and caps are the same preparation the phone's builders call (AN_COPY, AN_TABLES, anToolGroups,
// anModelFold, anProjectBars, anSessionRow ...): only the DOM differs.
// The page's parts are the kit's: a KPI row, h2.section-title with its caption, cards (.an-card: --panel and --r-card, a name
// of its own because app.css's `.card` is the phone's amber session card) and the table look of §C9 on the same .an-table.
// Models, Projects and Tools have no frame: they carry today's content in those parts (plan call 13).

// anModelsPage: the per-model table in a card, then the two share cards under "Why $ ≠ token count".
function anModelsPage(root) {
  const d = AN.data;
  const fold = anModelFold(d.byModel);
  const sec = anPSection(AN_COPY.models.title, AN_COPY.models.hint);
  const tbl = el("table", "an-table");
  tbl.appendChild(anHead(...AN_TABLES.models));
  for (const x of fold.rows) {
    const tr = el("tr");
    // A model id is machine text (mono); the "other (unpriced)" row is a label, so sans and muted.
    const nm = el("td");
    const sw = el("span", "an-swatch"); sw.style.background = anModelColor(d.models, x.model);
    nm.append(sw, el("span", x.model === "other (unpriced)" ? "muted" : "mono t-sm", shortModel(x.model)));
    const c = anModelCells(x, fold);
    tr.append(nm, anTd(c.cost, "num w-500"), anTd(c.pct, "num"), anTd(c.tokens, "num"), anTd(c.prompts, "num"),
      anTd(c.outPerPrompt, "num"), anTd(c.cache, "num"));
    tbl.appendChild(tr);
  }
  const card = anCard();
  card.appendChild(anTableWrap(tbl));
  sec.appendChild(card);
  root.appendChild(sec);

  const bt = anCard(AN_COPY.models.tokens), bc = anCard(AN_COPY.models.cost);
  const bodyT = anCardBody(bt), bodyC = anCardBody(bc);
  for (const x of fold.rows) anHbar(bodyT, shortModel(x.model), x.tokens, fold.maxTokens, anModelColor(d.models, x.model), (v) => anPct(v, fold.totalTokens));
  for (const x of fold.rows) anHbar(bodyC, shortModel(x.model), x.cost, fold.maxCost, anModelColor(d.models, x.model), (v) => anPct(v, fold.totalCost));
  const g = el("div", "an-pgrid");
  g.append(bt, bc);
  const s2 = anPSection(AN_COPY.models.shareTitle, AN_COPY.models.shareHint);
  s2.appendChild(g);
  root.appendChild(s2);
}

// anProjectsPage: the stacked bars and their legend in a card, then the detail table in another.
function anProjectsPage(root) {
  const d = AN.data;
  const sec = anPSection(AN_COPY.projects.title, AN_COPY.projects.hint);
  const card = anCard();
  const body = anCardBody(card);
  const pb = anProjectBars(d.byProject, d.models);
  for (const b of pb.bars) anHbarSegs(body, b.name, b.cost, pb.max, b.segs, usd);
  anLegend(body, d.models);
  sec.appendChild(card);
  root.appendChild(sec);

  const s2 = anPSection(AN_COPY.projects.detail);
  const tbl = el("table", "an-table");
  tbl.appendChild(anHead(...AN_TABLES.projects));
  for (const p of d.byProject) {
    const r = anProjectRow(p);
    const tr = el("tr");
    tr.append(anIdTd(r.name, "an-wrap"), anTd(r.cost, "num w-500"), anTd(r.tokens, "num"), anTd(r.prompts, "num"));
    tbl.appendChild(tr);
  }
  const card2 = anCard();
  card2.appendChild(anTableWrap(tbl));
  s2.appendChild(card2);
  root.appendChild(s2);
}

// anSessionsPage: M3.2 (digest-design §B8). Three KPI tiles, then a split: the top-sessions table in a card, and "Session cost"
// (the cost bands) beside it. The split is one column below 1000px of body (desktop.css).
function anSessionsPage(root) {
  const d = AN.data;
  root.appendChild(anKpiRow(anSessionKpis(d)));
  const split = el("div", "an-split");

  const left = anPSection(AN_COPY.sessions.title, "Spot the runaway session. Counts include subagent and synthetic transcripts.");
  const tbl = el("table", "an-table an-stbl");
  tbl.appendChild(anHead(...AN_TABLES.sessions));
  for (const s of d.topSessions) {
    const r = anSessionRow(s);
    const tr = el("tr");
    // Session, Project and a single Model are ids (mono); "mixed" and the day are muted sans. Only Project wraps (§B8 .m3-wrap):
    // Session and Model are words that keep their width.
    tr.append(anIdTd(r.sid), anTd(r.day, "sub tnum"), anIdTd(r.project, "an-wrap"),
      r.mixed ? anTd(r.model, "sub") : anIdTd(r.model),
      anTd(r.tokens, "num"), anTd(r.prompts, "num"), anTd(r.cost, "num w-500"));
    tbl.appendChild(tr);
  }
  const card = anCard();
  card.appendChild(anTableWrap(tbl));
  left.appendChild(card);

  const right = anPSection("Session cost");
  const bands = anCard(null, "Sessions per cost band");
  const rows = el("div", "an-bands");
  const maxH = Math.max(1, ...d.sessionHist.map((b) => b.count));
  for (const b of d.sessionHist) {
    const row = el("div", "an-band");
    const meter = el("span", "meter");
    const fill = el("i"); fill.style.width = (b.count / maxH * 100) + "%"; // no colour: the meter's own accent
    meter.appendChild(fill);
    row.append(el("span", "t-sm tnum", b.label), meter, el("span", "t-sm tnum an-band-n", anCountText(b.count)));
    rows.appendChild(row);
  }
  anCardBody(bands).appendChild(rows);
  right.appendChild(bands);

  split.append(left, right);
  root.appendChild(split);
}

// anToolsPage: one card per kind, as today's boxes.
function anToolsPage(root) {
  const d = AN.data;
  const sec = anPSection(AN_COPY.tools.title, AN_COPY.tools.hint);
  if (!d.tools.length) {
    sec.appendChild(anEmpty(AN_COPY.tools.none));
    root.appendChild(sec); return;
  }
  const g = el("div", "an-pgrid");
  for (const grp of anToolGroups(d.tools)) {
    const card = anCard(grp.kind);
    const body = anCardBody(card);
    for (const t of grp.items) anHbar(body, t.name, t.records, grp.max, grp.color, fmtCompact);
    g.appendChild(card);
  }
  sec.appendChild(g);
  root.appendChild(sec);
}

// ---- comparison block -------------------------------------------------------
function anDelta(cur, prev, costLike) {
  const p = pctDelta(cur, prev);
  if (p === null) return { txt: cur ? "new" : "—", cls: costLike ? "an-up" : "an-up good" };
  const up = p >= 0;
  const cls = costLike ? (up ? "an-up" : "an-down") : (up ? "an-up good" : "an-down bad");
  return { txt: (up ? "▲" : "▼") + Math.abs(p) + "% vs prev", cls };
}

function anCompareBlock() {
  const C = AN.data.cmp;
  const sec = anSection("Change · this window vs previous", `${C.curFrom} → ${C.curTo}  vs  ${C.prevFrom} → ${C.prevTo}`);
  const kpis = el("div", "an-kpis");
  const tiles = [
    ["cost", usd(C.cur.cost), "cost", anDelta(C.cur.cost, C.prev.cost, true)],
    ["", fmtCompact(C.cur.tokens), "tokens", anDelta(C.cur.tokens, C.prev.tokens, true)],
    ["", String(C.cur.prompts), "prompts", anDelta(C.cur.prompts, C.prev.prompts, false)],
    ["", String(C.cur.sessions), "sessions", anDelta(C.cur.sessions, C.prev.sessions, false)],
  ];
  for (const [cls, v, l, d] of tiles) {
    const c = anKpi(cls, v, l);
    const dd = el("div", "an-kdelta " + d.cls, d.txt);
    c.appendChild(dd);
    kpis.appendChild(c);
  }
  sec.appendChild(kpis);

  const g = el("div", "an-grid2");
  g.append(
    anDiverging("Cost change by model", C.byModel.map((x) => ({ name: shortModel(x.model), cur: x.curCost, prev: x.prevCost }))),
    anDiverging("Cost change by project", C.byProject.map((x) => ({ name: projShort(x.project), cur: x.curCost, prev: x.prevCost }))),
  );
  sec.appendChild(g);

  const g2 = el("div", "an-grid2");
  g2.append(anMixShift(), anDowOverlay());
  sec.appendChild(g2);

  sec.appendChild(anNote("Read <b>mix-shift</b> and <b>day-of-week</b> with the deltas: a jump in absolute tokens can just mean you worked more — the <i>share</i> shift is what shows you changed which model you reach for. Short windows are noisy (one big session skews a week)."));
  return sec;
}

function anDiverging(title, items) {
  const box = el("div", "an-box"); box.appendChild(el("h4", null, title));
  const rows = items.map((it) => ({ ...it, d: it.cur - it.prev })).sort((a, b) => Math.abs(b.d) - Math.abs(a.d));
  const max = Math.max(0.01, ...rows.map((r) => Math.abs(r.d)));
  for (const r of rows) {
    if (Math.abs(r.d) < 0.01) continue;
    const row = el("div", "an-divrow");
    const nm = el("div", "nm", r.name); nm.title = r.name; row.appendChild(nm);
    const track = el("div", "an-divtrack"); track.appendChild(el("div", "mid"));
    const fill = el("div", "fill");
    fill.style.width = (Math.abs(r.d) / max * 50) + "%";
    fill.style.background = r.d >= 0 ? "var(--an-up)" : "var(--an-down)";
    if (r.d >= 0) fill.style.left = "50%"; else fill.style.right = "50%";
    track.appendChild(fill); row.appendChild(track);
    row.appendChild(el("div", "val " + (r.d >= 0 ? "an-up" : "an-down"), (r.d >= 0 ? "+" : "−") + usd(Math.abs(r.d))));
    box.appendChild(row);
  }
  return box;
}

function anMixShift() {
  const C = AN.data.cmp, box = el("div", "an-box");
  box.appendChild(el("h4", null, "Model mix shift (share of tokens)"));
  const rows = C.byModel;
  const curTot = rows.reduce((s, x) => s + x.curTok, 0) || 1;
  const prevTot = rows.reduce((s, x) => s + x.prevTok, 0) || 1;
  const cmodels = C.models || AN.data.models; // union of cur+prev for stable colours
  const stack = (label, total, key) => {
    const wrap = el("div", "an-mix");
    wrap.appendChild(el("div", "an-mixlabel", label));
    const bar = el("div", "an-mixbar");
    for (const x of rows) {
      const v = x[key]; if (!v) continue;
      const seg = el("div"); seg.style.width = (v / total * 100) + "%";
      seg.style.background = modelColor(cmodels, x.model);
      seg.title = shortModel(x.model) + " " + Math.round(v / total * 100) + "%";
      bar.appendChild(seg);
    }
    wrap.appendChild(bar); return wrap;
  };
  box.appendChild(stack("previous", prevTot, "prevTok"));
  box.appendChild(stack("current", curTot, "curTok"));
  anLegend(box, cmodels);
  return box;
}

function anDowOverlay() {
  const C = AN.data.cmp, d = C.dow, box = el("div", "an-box");
  box.appendChild(el("h4", null, "Cost by day-of-week (this vs prev)"));
  const W = 380, H = 150, pad = 26;
  const max = Math.max(1, ...d.map((x) => Math.max(x.cur, x.prev)));
  const svg = anSvg(W, H);
  for (let i = 0; i <= 2; i++) {
    const y = pad + (H - 2 * pad) * i / 2;
    svg.appendChild(anLine(pad, y, W - 6, y, "var(--rule)", 0.5));
    svg.appendChild(anText(2, y + 3, usd(max * (1 - i / 2)), "start"));
  }
  const xs = (i) => pad + i * ((W - 2 * pad) / 6);
  const ys = (v) => H - pad - (v / max) * (H - 2 * pad);
  const line = (key, color, dash) => {
    let dd = "";
    d.forEach((x, i) => { dd += (i ? "L" : "M") + xs(i).toFixed(1) + " " + ys(x[key]).toFixed(1) + " "; });
    const p = document.createElementNS(NS, "path");
    p.setAttribute("d", dd); p.setAttribute("fill", "none");
    p.style.stroke = color; p.setAttribute("stroke-width", "1.6");
    if (dash) p.setAttribute("stroke-dasharray", "3 3");
    svg.appendChild(p);
    d.forEach((x, i) => { const c = document.createElementNS(NS, "circle"); c.setAttribute("cx", xs(i)); c.setAttribute("cy", ys(x[key])); c.setAttribute("r", "2.2"); c.style.fill = color; svg.appendChild(c); });
  };
  line("prev", "var(--muted)", true);
  line("cur", "var(--accent)", false);
  d.forEach((x, i) => svg.appendChild(anText(xs(i), H - pad + 11, x.day, "middle")));
  box.appendChild(anWrapSvg(svg));
  const lg = el("div", "an-legend");
  const mk = (c, t, dash) => { const s = el("span"); const i = el("i"); i.style.background = c; if (dash) i.style.opacity = ".6"; s.append(i, el("span", null, t)); return s; };
  lg.append(mk("var(--accent)", "current", false), mk("var(--muted)", "previous", true));
  box.appendChild(lg);
  return box;
}

// ---- small builders ---------------------------------------------------------
function el(t, c, txt) { const e = document.createElement(t); if (c) e.className = c; if (txt != null) e.textContent = txt; return e; }
function anEmpty(text) { const p = el("p", "an-empty"); p.textContent = text; return p; }
function anSection(title, hint) {
  const s = el("div", "an-section");
  s.appendChild(el("h3", null, title));
  if (hint) s.appendChild(el("p", "an-hint", hint));
  return s;
}
function anKpi(cls, v, l) { const c = el("div", "an-kpi"); c.append(el("div", "v " + (cls || ""), v), el("div", "l", l)); return c; }
// The desktop page's parts (callers are the page's views, behind anIsPage()). anKpiRow, anCard and anPSection are general: the
// Overview builds on them too.
// anKpiRow: the kit's KPI row, the one Audit draws too: div.kpi-row > div.kpi > span.kpi-value + span.kpi-label. The label
// ellipsizes (desktop.css), so it carries its full text in title: the tile's own, else the label.
function anKpiRow(tiles) {
  const row = el("div", "kpi-row");
  for (const t of tiles) {
    const tile = el("div", "kpi");
    const label = el("span", "kpi-label", t.label);
    label.setAttribute("title", t.title || t.label);
    tile.append(el("span", "kpi-value", t.value), label);
    row.appendChild(tile);
  }
  return row;
}
// anPSection: a section of the page: the kit's h2.section-title and, on its baseline, a muted caption (M3.2's title row).
function anPSection(title, caption) {
  const sec = el("section", "an-psec");
  const head = el("div", "an-psec-head");
  head.appendChild(el("h2", "section-title", title));
  if (caption) head.appendChild(el("span", "t-sm muted", caption));
  sec.appendChild(head);
  return sec;
}
// anCard: a card (--panel, --r-card). A title and/or a caption give it the kit's .card-head, a hairline under it; with neither
// it is a bare panel, as a table's card is. Content goes in anCardBody (padded) or, for a table, straight in.
function anCard(title, caption) {
  const card = el("div", "an-card");
  if (title || caption) {
    const head = el("div", "card-head");
    if (title) head.appendChild(el("h3", "t-md w-600", title));
    if (caption) head.appendChild(el("span", "t-sm muted", caption));
    card.appendChild(head);
  }
  return card;
}
function anCardBody(card) { const b = el("div", "an-card-body"); card.appendChild(b); return b; }
function anNote(html) { const n = el("div", "an-cmpnote"); n.innerHTML = html; return n; }
// anHead: the header row. Columns from firstNum on hold numbers: their header gets
// "num" like their cells (anTd(text, "num")), so desktop.css can right-align both.
// The page writes the labels in sentence case (§A3: Session, Day, …); the phone's stay as given.
function anHead(cols, firstNum) {
  const page = anIsPage();
  const tr = el("tr");
  cols.forEach((c, i) => tr.appendChild(el("th", i >= firstNum ? "num" : null, page ? c.charAt(0).toUpperCase() + c.slice(1) : c)));
  return tr;
}
// anTableWrap keeps a wide table scrollable on its own on narrow viewports, so
// the dialog body never scrolls horizontally.
function anTableWrap(tbl) { const d = el("div", "an-tablewrap"); d.appendChild(tbl); return d; }
function anTd(text, cls) { const td = el("td", cls); td.textContent = text; return td; }
// anIdTd (the page): a cell holding an identifier: the cell is sans and the id sits in the kit's span.mono.t-sm (machine text only).
function anIdTd(text, cls) { const td = el("td", cls); td.appendChild(el("span", "mono t-sm", text)); return td; }
// anLegend shows `models`, but colours each by its index in `colorModels` (the
// canonical list) so legend swatches match chart/bar colours even when the
// legend displays only a subset (e.g. priced models).
function anLegend(parent, models, colorModels) {
  const key = colorModels || models;
  const lg = el("div", "an-legend");
  for (const m of models) { const s = el("span"); const i = el("i"); i.style.background = modelColor(key, m); s.append(i, el("span", null, shortModel(m))); lg.appendChild(s); }
  parent.appendChild(lg);
}
// anModelColor colours a model bar, with a neutral grey for the collapsed
// "other (unpriced)" row.
function anModelColor(models, model) {
  return model === "other (unpriced)" ? "var(--muted)" : modelColor(models, model);
}
function anHbar(parent, name, v, max, color, fmtVal) {
  const row = el("div", "an-hbar");
  const nm = el("div", "nm", name); nm.title = name; row.appendChild(nm);
  const track = el("div", "track"); const fill = el("div", "fill");
  fill.style.width = (v / max * 100) + "%"; fill.style.background = color; track.appendChild(fill);
  row.appendChild(track); row.appendChild(el("div", "val", fmtVal(v)));
  parent.appendChild(row);
}
function anHbarSegs(parent, name, total, max, segs, fmtVal) {
  const row = el("div", "an-hbar");
  const nm = el("div", "nm", name); nm.title = name; row.appendChild(nm);
  const track = el("div", "track");
  for (const s of segs) { const f = el("div", "fill"); f.style.width = (s.v / max * 100) + "%"; f.style.background = s.c; f.title = s.t || ""; track.appendChild(f); }
  row.appendChild(track); row.appendChild(el("div", "val", fmtVal(total)));
  parent.appendChild(row);
}

// SVG helpers
const NS = "http://www.w3.org/2000/svg";
function anSvg(w, h) { const s = document.createElementNS(NS, "svg"); s.setAttribute("viewBox", "0 0 " + w + " " + h); s.setAttribute("width", w); s.setAttribute("height", h); s.setAttribute("class", "an-axis"); return s; }
function anWrapSvg(s) { const d = el("div", "an-svgwrap"); d.appendChild(s); return d; }
function anRect(x, y, w, h, f) { const r = document.createElementNS(NS, "rect"); r.setAttribute("x", x); r.setAttribute("y", y); r.setAttribute("width", w); r.setAttribute("height", Math.max(h, 0)); r.style.fill = f; return r; }
function anLine(x1, y1, x2, y2, st, sw) { const l = document.createElementNS(NS, "line"); l.setAttribute("x1", x1); l.setAttribute("y1", y1); l.setAttribute("x2", x2); l.setAttribute("y2", y2); l.style.stroke = st; l.setAttribute("stroke-width", sw); return l; }
function anText(x, y, t, anchor) { const e = document.createElementNS(NS, "text"); e.setAttribute("x", x); e.setAttribute("y", y); e.setAttribute("text-anchor", anchor); e.textContent = t; return e; }
function anTitle(t) { const e = document.createElementNS(NS, "title"); e.textContent = t; return e; }

// stacked daily cost bars. Bars sit at their true date offset within [from,to]
// (empty days show as gaps) unless the span is very wide, where stackedLayout
// falls back to sequential positioning. `fitW` (the desktop page) draws it at
// that width, with a narrower floor per day; without it the width is fixed.
function anStackedTime(agg, from, to, fitW) {
  const lay = stackedLayout(agg.days, from, to);
  const slots = Math.max(1, lay.slots);
  const H = 170, pad = 24, W = fitW ? Math.max(slots * 8, fitW) : Math.max(slots * 22, 720);
  const max = Math.max(1, agg.maxCost);
  const slotW = (W - 2 * pad) / slots;
  const bw = Math.min(16, Math.max(2, slotW - 3));
  const svg = anSvg(W, H);
  for (let i = 0; i <= 3; i++) {
    const y = pad + (H - 2 * pad) * i / 3;
    svg.appendChild(anLine(pad, y, W - 4, y, "var(--rule)", 0.5));
    svg.appendChild(anText(2, y + 3, usd(max * (1 - i / 3)), "start"));
  }
  const labelEvery = Math.max(1, Math.round(slots / 12)); // ~12 date labels max
  for (const b of lay.bars) {
    const x = pad + b.slot * slotW;
    let yb = H - pad;
    const segs = Object.entries(b.segs).sort((a, c) => a[1] - c[1]);
    for (const [m, c] of segs) {
      const h = (c / max) * (H - 2 * pad); yb -= h;
      const r = anRect(x, yb, bw, h, modelColor(agg.models, m));
      r.appendChild(anTitle(b.day + " · " + shortModel(m) + " " + usd(c)));
      svg.appendChild(r);
    }
    if (b.slot % labelEvery === 0) svg.appendChild(anText(x + bw / 2, H - pad + 11, b.day.slice(5), "middle"));
  }
  const wrap = anWrapSvg(svg);
  wrap.classList.add("an-timewrap"); // marked so anRender scrolls it to the latest day
  return wrap;
}
