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

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    fmtCompact, usd, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
    foldProjects, sessionCostBuckets, topSessionsByCost, distinctSessions,
    prevWindow, pctDelta, dowBuckets, modelColor, shortModel, projShort,
    collapseZeroCost, pricedModelIds, daysBetween, clampRange, stackedLayout,
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
  await anLoad();
}

function syncTabButtons() {
  for (const b of document.querySelectorAll("#an-tabs button")) {
    b.classList.toggle("act", b.dataset.antab === AN.tab);
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
  // On the desktop page the range chips + Compare live in the page header
  // (#an-ctl); the modal keeps them at the top of the body.
  const page = anIsPage();
  const ctl = document.getElementById("an-ctl");
  if (ctl) ctl.replaceChildren();
  if (page && ctl) {
    ctl.appendChild(anControls(true));
    // Custom's two date inputs would not fit the header: a second row, first in the body.
    if (AN.customOpen || AN.range === "custom") body.appendChild(anCustomRow());
  } else {
    body.appendChild(anControls(false));
  }
  const d = AN.data;
  if (!d || !d.costByDay.days.length) {
    body.appendChild(anEmpty("No usage recorded in this window. Try a wider range or Re-scan."));
    return;
  }
  const R = { overview: anOverview, models: anModels, projects: anProjects, sessions: anSessions, tools: anTools };
  (R[AN.tab] || anOverview)(body);
  // The mockup shows the window in the first chart's caption; the header's own
  // #analytics-range caption is hidden on the page.
  if (page) {
    const win = `${d.from} → ${d.to} (UTC)`;
    const hint = body.querySelector(".an-section .an-hint");
    if (hint) hint.textContent += " · " + win;
    else body.insertBefore(el("p", "an-hint", win), body.querySelector(".an-section"));
  }
  // Time-series charts read left→right oldest→newest; open scrolled to the most
  // recent day so a window with a leading empty gap doesn't render blank.
  const tw = body.querySelector(".an-timewrap");
  if (tw) tw.scrollLeft = tw.scrollWidth;
}

// anIsPage: the dialog is re-parented as a desktop page (pagehost.js).
function anIsPage() {
  const dlg = document.getElementById("analytics-modal");
  return !!(dlg && dlg.classList.contains("as-page"));
}

// the Custom range's date inputs + Apply, as one list of nodes
function anCustomInputs() {
  const from = el("input", "an-date"); from.type = "date"; from.value = AN.from || "";
  const to = el("input", "an-date"); to.type = "date"; to.value = AN.to || "";
  const dash = el("span", "an-clabel", "→");
  const apply = el("button", "an-chip", "Apply");
  apply.type = "button";
  apply.onclick = () => {
    if (!from.value || !to.value) return;
    const r = clampRange(from.value, to.value);
    AN.range = "custom"; AN.from = r.from; AN.to = r.to; anLoad();
  };
  return [from, dash, to, apply];
}
function anCustomRow() {
  const row = el("div", "an-customrow");
  row.append(el("span", "an-clabel", "Custom range"), ...anCustomInputs());
  return row;
}

// control strip: range presets + compare toggle. `page` = the desktop header
// slot: no inline date inputs (anCustomRow), and the shorter Compare label.
function anControls(page) {
  const wrap = el("div", "an-controls");
  const lbl = el("span", "an-clabel", "Range");
  wrap.appendChild(lbl);
  for (const key of ["7d", "30d", "90d", "all"]) {
    const b = el("button", "an-chip" + (AN.range === key ? " on" : ""), key === "all" ? "All" : key);
    b.type = "button";
    b.onclick = () => { AN.range = key; AN.customOpen = false; AN.from = anRangeFrom(key); AN.to = anTodayUTC(); anLoad(); };
    wrap.appendChild(b);
  }
  // Custom range: a chip that reveals two date inputs + Apply.
  const custom = el("button", "an-chip" + (AN.range === "custom" ? " on" : ""), "Custom…");
  custom.type = "button";
  custom.onclick = () => { AN.customOpen = !AN.customOpen; anRender(); };
  wrap.appendChild(custom);
  if (!page && (AN.customOpen || AN.range === "custom")) wrap.append(...anCustomInputs());
  const spacer = el("span"); spacer.style.width = "12px"; wrap.appendChild(spacer);
  const cmp = el("button", "an-chip" + (AN.compare ? " on" : ""), page ? "⇄ Compare" : "⇄ Compare vs prev period");
  cmp.type = "button";
  cmp.title = "Compare this window to the equal-length window immediately before it";
  cmp.onclick = () => { AN.compare = !AN.compare; anLoad(); };
  wrap.appendChild(cmp);
  return wrap;
}

// ---- Overview ---------------------------------------------------------------
function anOverview(root) {
  const d = AN.data, t = d.costByDay.totals;
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
  const d = AN.data;
  const sec = anSection("Per-model breakdown", "Cost, volume & efficiency · window");
  const mc = collapseZeroCost(d.byModel); // fold $0 (unpriced) models into "other"
  const box = el("div", "an-box");
  const tbl = el("table", "an-table");
  tbl.appendChild(anHead(["model", "cost", "% cost", "tokens", "prompts", "out/prompt", "cache-read"]));
  const totalCost = mc.reduce((s, x) => s + x.cost, 0) || 1;
  for (const x of mc) {
    const tr = el("tr");
    const nm = el("td", "mono");
    const sw = el("span", "an-swatch"); sw.style.background = anModelColor(d.models, x.model);
    nm.append(sw, document.createTextNode(shortModel(x.model)));
    const crShare = x.tokens ? Math.round(x.cacheRead / x.tokens * 100) : 0;
    tr.append(nm, anTd(usd(x.cost)), anTd(Math.round(x.cost / totalCost * 100) + "%", "dim"),
      anTd(fmtCompact(x.tokens), "dim"), anTd(String(x.prompts), "dim"),
      anTd(fmtCompact(x.outPerPrompt), "dim"), anTd(crShare + "%", "dim"));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);

  const g = el("div", "an-grid2");
  const totTok = mc.reduce((s, x) => s + x.tokens, 0) || 1;
  const bt = el("div", "an-box"); bt.appendChild(el("h4", null, "Share of tokens"));
  const maxTok = Math.max(1, ...mc.map((x) => x.tokens));
  for (const x of mc) anHbar(bt, shortModel(x.model), x.tokens, maxTok, anModelColor(d.models, x.model), (v) => Math.round(v / totTok * 100) + "%");
  const bc = el("div", "an-box"); bc.appendChild(el("h4", null, "Share of cost"));
  const maxC = Math.max(1, ...mc.map((x) => x.cost));
  for (const x of mc) anHbar(bc, shortModel(x.model), x.cost, maxC, anModelColor(d.models, x.model), (v) => Math.round(v / totalCost * 100) + "%");
  g.append(bt, bc);
  const s2 = anSection("Why $ ≠ token count", "cache-read dominates tokens but is cheap; output is tiny but expensive — the two rankings disagree");
  s2.appendChild(g); root.appendChild(s2);
}

// ---- Projects ---------------------------------------------------------------
function anProjects(root) {
  const d = AN.data;
  const sec = anSection("Cost by project", "Bar split shows the model mix inside each project · window");
  const box = el("div", "an-box");
  const maxP = Math.max(1, ...d.byProject.map((x) => x.cost));
  for (const p of d.byProject) {
    const segs = Object.entries(p.byModel).sort((a, b) => b[1] - a[1]).map(([m, v]) => ({ v, c: modelColor(d.models, m), t: shortModel(m) + " " + usd(v) }));
    anHbarSegs(box, projShort(p.project), p.cost, maxP, segs, usd);
  }
  anLegend(box, d.models);
  sec.appendChild(box); root.appendChild(sec);

  const s2 = anSection("Detail");
  const box2 = el("div", "an-box"); const tbl = el("table", "an-table");
  tbl.appendChild(anHead(["project", "cost", "tokens", "prompts"]));
  for (const p of d.byProject) {
    const tr = el("tr");
    tr.append(anTd(projShort(p.project), "mono"), anTd(usd(p.cost)), anTd(fmtCompact(p.tokens), "dim"), anTd(String(p.prompts), "dim"));
    tbl.appendChild(tr);
  }
  box2.appendChild(anTableWrap(tbl)); s2.appendChild(box2); root.appendChild(s2);
}

// ---- Sessions ---------------------------------------------------------------
function anSessions(root) {
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

  const sec = anSection("Top sessions by cost", "Spot the runaway session burning the budget. Counts include subagent / synthetic transcripts.");
  const box = el("div", "an-box"); const tbl = el("table", "an-table");
  tbl.appendChild(anHead(["session", "day", "project", "model", "tokens", "prompts", "cost"]));
  for (const s of d.topSessions) {
    const tr = el("tr");
    tr.append(anTd(s.sid, "mono"), anTd(s.day, "dim"), anTd(projShort(s.project), "mono"),
      anTd(shortModel(s.model), "dim"), anTd(fmtCompact(s.tokens), "dim"), anTd(String(s.prompts), "dim"), anTd(usd(s.cost)));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);
}

// ---- Tools ------------------------------------------------------------------
function anTools(root) {
  const d = AN.data;
  const sec = anSection("Skills · plugins · MCP", "What the agents actually invoked · by records");
  if (!d.tools.length) {
    sec.appendChild(anEmpty("No skill/plugin/MCP attribution in this window (recorded only on recent transcripts)."));
    root.appendChild(sec); return;
  }
  const kinds = {};
  for (const t of d.tools) (kinds[t.kind] = kinds[t.kind] || []).push(t);
  const kc = { skill: "var(--an-2)", plugin: "var(--an-4)", mcp_server: "var(--an-3)", mcp_tool: "var(--an-1)" };
  const g = el("div", "an-grid2");
  for (const kind of Object.keys(kinds)) {
    const box = el("div", "an-box"); box.appendChild(el("h4", null, kind));
    const items = kinds[kind].slice(0, 10);
    const max = Math.max(1, ...items.map((t) => t.records));
    for (const t of items) anHbar(box, t.name, t.records, max, kc[kind] || "var(--accent)", fmtCompact);
    g.appendChild(box);
  }
  sec.appendChild(g); root.appendChild(sec);
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
function anNote(html) { const n = el("div", "an-cmpnote"); n.innerHTML = html; return n; }
function anHead(cols) { const tr = el("tr"); for (const c of cols) tr.appendChild(el("th", null, c)); return tr; }
// anTableWrap keeps a wide table scrollable on its own on narrow viewports, so
// the dialog body never scrolls horizontally.
function anTableWrap(tbl) { const d = el("div", "an-tablewrap"); d.appendChild(tbl); return d; }
function anTd(text, cls) { const td = el("td", cls); td.textContent = text; return td; }
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
// falls back to sequential positioning.
function anStackedTime(agg, from, to) {
  const lay = stackedLayout(agg.days, from, to);
  const slots = Math.max(1, lay.slots);
  const H = 170, pad = 24, W = Math.max(slots * 22, 720);
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
