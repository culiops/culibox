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

// AN_API_VALUE_TITLE: the "API value" tile's tooltip (#340): the amount is token usage priced at list rates, not
// what a subscription bills. Range-neutral, because the tile covers the chosen range; the sidebar says "today".
const AN_API_VALUE_TITLE = "Token usage priced at API list rates. A subscription does not bill this.";

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

// topSessionsByCost: /sessions or /session-costs rows → sessions aggregated across their day/model rows, most expensive first,
// limited to `limit`. A session also carries what its rows say of it: workspace, runtime, profile, branch and liveId ("" when
// they say nothing).
function topSessionsByCost(rows, limit) {
  const agg = new Map();
  for (const r of rows || []) {
    const k = (r.sessionId || "") + String.fromCharCode(31) + (r.project || "");
    let s = agg.get(k);
    if (!s) { s = { sid: (r.sessionId || "").slice(0, 8), day: "", project: r.project || "", workspace: "", runtime: "", profile: "", branch: "", liveId: "", models: new Set(), tokens: 0, cost: 0, prompts: 0 }; agg.set(k, s); }
    // From session_dim and the culibox index: the same for every row of one session, so the first one that has it.
    for (const f of ["workspace", "runtime", "profile", "branch", "liveId"]) if (!s[f] && r[f]) s[f] = r[f];
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
// anWsName: a workspace's name for a cell or a label: the last element of its folder path (the server's workspaceName), or the text
// folder slug as the Workspaces row names it (projShort) when it has no "/" (an unmatched row). The full path stays in the title.
function anWsName(ws) {
  const t = String(ws || "").replace(/\/+$/, "");
  const i = t.lastIndexOf("/");
  return i >= 0 && i < t.length - 1 ? t.slice(i + 1) : projShort(t);
}

// ---- the desktop page's KPI figures -------------------------------------------------------------------------------

// anCountText: a count in a KPI tile: thousands separators below 10,000 ("1,420"), fmtCompact from there.
function anCountText(n) {
  n = Number(n) || 0;
  return n < 10000 ? n.toLocaleString("en-US") : fmtCompact(n);
}

// anSessionKpis: the three tiles of the Sessions view (§3.4), from the loaded window's sessionCount, topSessions (cost-descending)
// and median. "Costliest session" carries the top session's workspace in its label and its COST in the value; with no session a
// dash and the plain label (and a workspace with no name leaves no dangling " · ").
function anSessionKpis(d) {
  const L = AN_COPY.sessions.kpis;
  const top = (d.topSessions || [])[0];
  const ws = top && anWsName(top.workspace || projShort(top.project));
  return [
    { value: anCountText(d.sessionCount), label: L[0] },
    { value: top ? usd(top.cost) : "—", label: ws ? L[1] + " · " + ws : L[1] },
    { value: d.median == null ? "—" : usd(d.median), label: L[2] },
  ];
}

// ---- what the Overview, Workspaces, Cost drivers, Sessions and Models views say: one preparation for both skins ----------------------
// The phone's modal (anOverview, anWorkspaces, anDrivers, anSessions, anModels) and the desktop page (the an...Page builders) draw
// different DOM from the same words, figures, colours and caps. Those live here and are called by both, so a change reaches both.

const AN_COPY = {
  // The head and toolbar of every view (§3.0). {from} and {to} are the window's dates.
  head: {
    title: "Analytics",
    line: "API value of your Claude Code usage at list rates, not what a subscription bills · {from} → {to} (UTC)",
    rescan: "Re-scan", close: "Esc ✕", where: "Where", whereAll: "All", compare: "Compare",
    views: ["Overview", "Workspaces", "Cost drivers", "Sessions", "Models"],
    ranges: ["7d", "30d", "90d", "All", "Custom…"],
  },
  overview: {
    chart: "Cost over time", chartHint: "Stacked by model · daily", byModel: "Cost by model",
    kpis: ["API value", "Sessions", "Median per session", "Cache hit"],
    compareSuffix: " vs the {N} days before",
    byWorkspace: "Cost by workspace", byWorkspaceHint: "top 8 · bar split = host / box",
    note: "Descriptive only: a difference between models reflects the tasks you gave each one, not model quality.",
  },
  workspaces: {
    title: "Workspaces",
    kpis: ["Workspaces in this window", "Host · Box, share of API value", "Costliest workspace"],
    columns: ["Workspace", "API value", "Sessions", "Host · Box", "Model mix", "Median / session", "Cache hit"],
    unmatched: "unmatched", unmatchedPath: "unmatched · folder no longer known",
    host: "host", box: "box · {profile}", clone: "clone · {dir}", worktrees: "worktrees ({N})",
    note: "A box clone counts toward the repo it was cloned from, and a Claude Code worktree toward its repo. Older usage whose folder can't be matched any more keeps its folder name and is marked unmatched.",
  },
  drivers: {
    title: "Cost drivers",
    kpis: ["Of API value ran in subagents", "Costliest plugin", "Costliest MCP server"],
    cards: {
      agent: { title: "Subagents", caption: "main agent vs subagents", rest: "main agent (no subagent)" },
      skill: { title: "Skills", caption: "", rest: "outside any skill" },
      plugin: { title: "Plugins", caption: "", rest: "outside any plugin" },
      mcp: { title: "MCP", caption: "server → its tools", rest: "outside any MCP tool" },
    },
    coverage: "Based on {N}% of this window's API value: sessions whose transcripts are still on disk.",
    note: "A turn counts toward the subagent, skill, plugin and MCP tool it ran under, so one turn can appear in several cards and the cards do not add up across. An MCP tool keeps the turns that read its result. Transcripts older than Claude Code's labels count as \"outside\".",
    empty: "No cost-driver data in this window: these sessions' transcripts are no longer on disk.",
  },
  sessions: {
    title: "Top sessions by cost", hint: "click a row to open the session",
    kpis: ["Sessions in this window", "Costliest session", "Median per session"],
    columns: ["Session", "Last day", "Workspace", "Where", "Branch", "Model", "Prompts", "API value"],
  },
  models: {
    title: "Per-model breakdown", hint: "Cost, volume & efficiency · window",
    columns: ["Model", "API value", "% of value", "Sessions", "Median / session", "Per prompt", "Cache hit", "Tokens"],
    sessionsTitle: "A session that used several models counts once in each.",
    note: "Descriptive only: a difference between models reflects the tasks you gave each one, not model quality. \"other (unpriced)\" are models with no published rate; their tokens are counted, their value is not.",
  },
};

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

// anSessionRow: a top session (topSessionsByCost) as text. `mixed` marks the several-model case, whose model is the word "mixed".
// The page's cells: the workspace (the folder's name when the session has none), `where` ("host", "box · prd", "box · unnamed",
// "unknown" with no runtime) with `box` for the box tag, the branch ("" when unknown) and `clickable`: true when culibox still
// lists a session whose Claude ids include this one (`liveId`), the only row that opens anything.
function anSessionRow(s) {
  return {
    sid: s.sid, day: s.day, project: projShort(s.project), model: shortModel(s.model), mixed: s.model === "mixed",
    tokens: fmtCompact(s.tokens), prompts: String(s.prompts), cost: usd(s.cost),
    workspace: anWsName(s.workspace || projShort(s.project)), workspacePath: s.workspace || "", where: anWhereText(s.runtime, s.profile), box: s.runtime === "box",
    branch: s.branch || "", liveId: s.liveId || "", clickable: !!s.liveId,
  };
}

// anWhereText: a session's Where tag as words: "host", "box · {profile}", "box · unnamed" for a box with no profile, "unknown" for
// a session with no runtime recorded (legacy usage).
function anWhereText(runtime, profile) {
  if (runtime === "host") return "host";
  if (runtime === "box") return "box · " + (profile || "unnamed");
  return "unknown";
}

// anTabKey: a view's key. The old `projects` and `tools` open Workspaces and Cost drivers, so a saved or phone-strip tab still
// opens; a key that is none of the five is Overview.
const AN_TAB_KEYS = ["overview", "workspaces", "drivers", "sessions", "models"];
function anTabKey(k) {
  if (k === "projects") return "workspaces";
  if (k === "tools") return "drivers";
  return AN_TAB_KEYS.includes(k) ? k : "overview";
}

// anWhereLabel: one Where option as the select prints it: its label, then its window's API value ("Box · prd · $2.1k"). The API's
// value is `costUsd`.
function anWhereLabel(o) {
  return o.label + " · " + usd(o.costUsd != null ? o.costUsd : o.cost);
}

// anWhereOptions: the select's entries [{key, text}] from the API's options. A chosen key the window no longer lists (a profile
// with no usage in a narrower range) stays as an entry of its own, at $0.00, so the select never says "All" over a filtered view.
function anWhereOptions(opts, current) {
  const out = (opts || []).map((o) => ({ key: o.key, text: anWhereLabel(o) }));
  const key = current || "";
  if (!out.length) out.push({ key: "", text: AN_COPY.head.whereAll });
  if (!out.some((o) => o.key === key)) {
    const name = key === "host" ? "Host" : key === "box" ? "Box · all profiles" : key === "unknown" ? "Unknown" : key === "box:" ? "Box · unnamed" : "Box · " + key.replace(/^box:/, "");
    out.push({ key, text: anWhereLabel({ label: name, costUsd: 0 }) });
  }
  return out;
}

// anDeltaParts: a tile's change line split where its figure ends: {lead: "▲ 12%", rest: " vs the 30 days before"} (rest "" when
// the line is only the figure). The lead takes the direction's tone, the rest stays muted.
function anDeltaParts(text) {
  const i = text.indexOf(" vs ");
  return i < 0 ? { lead: text, rest: "" } : { lead: text.slice(0, i), rest: text.slice(i) };
}

// anPerPrompt: a model's API value per prompt: dollars, three decimals under a cent ("$0.004"), "—" with no prompts.
function anPerPrompt(cost, prompts) {
  if (!(prompts > 0)) return "—";
  const v = cost / prompts;
  return v > 0 && v < 0.01 ? "$" + v.toFixed(3) : usd(v);
}

// anModelTableRow: a model's row of the Models table (§3.5) as text. `medians` is anModelMedians' map. "other (unpriced)" has no
// value to show: its value columns are dashes, its sessions and tokens are real.
function anModelTableRow(x, fold, medians) {
  const other = x.model === "other (unpriced)";
  const ch = cacheHit(x);
  const med = medians ? medians[x.model] : null;
  return {
    other,
    cost: other ? "—" : usd(x.cost),
    pct: other ? "—" : anPct(x.cost, fold.totalCost),
    sessions: String(x.sessions),
    median: other || med == null ? "—" : usd(med),
    perPrompt: other ? "—" : anPerPrompt(x.cost, x.prompts),
    cache: other || ch == null ? "—" : (ch * 100).toFixed(1) + "%",
    tokens: fmtCompact(x.tokens),
  };
}

// ---- Analytics v3: workspaces, cost drivers, medians and cache hit (both skins) ------------------------------------------
// Names (a workspace, a skill, an MCP server) come from the box and are kept as data here: nothing in this section builds HTML,
// the builders draw them with textContent.

// median: the middle of a list of numbers, the mean of the two middle ones for an even count; null for none. The list is not reordered.
function median(nums) {
  const a = (nums || []).filter((n) => typeof n === "number" && !isNaN(n)).sort((x, y) => x - y);
  if (!a.length) return null;
  const m = a.length >> 1;
  return a.length % 2 ? a[m] : (a[m - 1] + a[m]) / 2;
}

// cacheHit: cache_read / (input + cache_read + cache_creation) as a ratio. `r` carries {cacheRead, input, cacheCreation} or the
// API's {cacheRead, cacheDenom}; null when the denominator is 0 (nothing was read, so there is no rate to show).
function cacheHit(r) {
  if (!r) return null;
  const denom = r.cacheDenom != null ? r.cacheDenom : (r.input || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
  return denom > 0 ? (r.cacheRead || 0) / denom : null;
}

// kpiDelta: a tile's change vs the window before: {text, dir} with dir "up", "down" or "flat". `unit` "pct" is a percent change
// ("▲ 12%"; null with no baseline: prev null or 0); "pt" is the change of a ratio in percentage points ("▼ 0.4 pt"). null when
// either figure is missing.
function kpiDelta(cur, prev, unit) {
  if (cur == null || prev == null || isNaN(cur) || isNaN(prev)) return null;
  let n, word;
  if (unit === "pt") {
    n = Math.round((cur - prev) * 1000) / 10;
    word = Math.abs(n).toFixed(1) + " pt";
  } else {
    if (prev === 0) return null;
    n = Math.round((cur - prev) / prev * 100);
    word = Math.abs(n) + "%";
  }
  if (n === 0) return { text: word, dir: "flat" };
  return { text: (n > 0 ? "▲ " : "▼ ") + word, dir: n > 0 ? "up" : "down" };
}

// anOverviewKpis: the Overview's four tiles (§3.1) from the loaded window `d` and, with Compare on, `cmp` ({days, prev:
// {cost, sessions, median, cache}}). Each tile is {value, label, delta}: delta is kpiDelta's {text, dir} or null; the first
// tile's text says which window it is against.
function anOverviewKpis(d, cmp) {
  const cost = d.costByDay.totals.cost, p = cmp ? cmp.prev : null;
  const L = AN_COPY.overview.kpis;
  const first = p ? kpiDelta(cost, p.cost, "pct") : null;
  if (first) first.text += AN_COPY.overview.compareSuffix.replace("{N}", String(cmp.days));
  return [
    { value: usd(cost), label: L[0], delta: first },
    { value: anCountText(d.sessionCount), label: L[1], delta: p ? kpiDelta(d.sessionCount, p.sessions, "pct") : null },
    { value: d.median == null ? "—" : usd(d.median), label: L[2], delta: p ? kpiDelta(d.median, p.median, "pct") : null },
    { value: d.cache == null ? "—" : (d.cache * 100).toFixed(1) + "%", label: L[3], delta: p ? kpiDelta(d.cache, p.cache, "pt") : null },
  ];
}

// anWorkspaceRows: /workspaces → one row per workspace, as the table prints it: the name and its full path (an unmatched row's
// path says it is unmatched), API value, sessions, the host / box split in whole percents that add to 100 (0 / 0 with no cost),
// the model mix (dearest first, as shares of the row's cost), the median per session, the cache hit and the children.
function anWorkspaceRows(resp) {
  const fig = (w) => ({ cost: w.costUsd || 0, sessions: w.sessions || 0, median: median(w.sessionCosts), cacheHit: cacheHit(w) });
  return ((resp && resp.workspaces) || []).map((w) => {
    const host = w.hostCost || 0, box = w.boxCost || 0, split = host + box;
    const hostPct = split > 0 ? Math.round(host / split * 100) : 0;
    const models = Object.entries(w.byModel || {}).sort((a, b) => b[1] - a[1]);
    const modelTotal = models.reduce((t, e) => t + e[1], 0);
    return {
      name: w.name, workspace: w.workspace, path: w.unmatched ? AN_COPY.workspaces.unmatchedPath : w.workspace, unmatched: !!w.unmatched,
      ...fig(w),
      hostPct, boxPct: split > 0 ? 100 - hostPct : 0, hostCost: host, boxCost: box,
      mix: modelTotal > 0 ? models.map(([model, v]) => ({ model, share: v / modelTotal })) : [],
      children: (w.children || []).map((c) => ({ runtime: c.runtime, profile: c.profile, via: c.via, label: c.label, path: c.path, ...fig(c) })),
    };
  });
}

// How many workspaces the Overview's bars show (the copy says "top 8"), and how many the phone's Workspaces tab draws above its table.
const AN_TOP_WORKSPACES = 8;
const AN_PHONE_WORKSPACE_BARS = 14;

// anWorkspaceBars: the "Cost by workspace" bars from anWorkspaceRows' rows (any list with name, cost, hostCost and boxCost):
// the dearest `limit` first (all with no limit), each a name, its cost and two segments, host (--an-6) then box (--an-1), titled
// "host $31.00", and the longest bar (never under 1, so no width divides by zero). The page and the phone draw the same bars.
function anWorkspaceBars(rows, limit) {
  const top = [...(rows || [])].sort((x, y) => y.cost - x.cost).slice(0, limit == null ? undefined : limit);
  return {
    max: Math.max(1, ...top.map((w) => w.cost)),
    bars: top.map((w) => ({
      name: w.name, cost: w.cost,
      segs: [{ v: w.hostCost, c: "var(--an-6)", t: "host " + usd(w.hostCost) }, { v: w.boxCost, c: "var(--an-1)", t: "box " + usd(w.boxCost) }],
    })),
  };
}

// anDriverCards: /drivers → {empty, coverage, cards}. Four cards (agent, skill, plugin, mcp), each rows of {name, cost, pct, muted,
// sub} and `max`, the largest named row (0 when none, so no bar is drawn). Percentages are whole percents of CoveredTotal, the API
// value of the sessions that have driver data, so usage that exists only in rollups never counts as "main agent" or "outside".
// The muted last row is the rest: CoveredTotal minus the kind's rows, never below 0. A fold row from the API (`other`: the names
// past its cap, "other (N more)") is muted too and draws no bar, yet counts in the sum. An MCP server's tools follow it as `sub`
// rows. `coverage` is CoveredTotal / Total when under 1, else null. With no covered value there is nothing to divide by:
// {empty: true, coverage: null, cards: []}.
function anDriverCards(resp) {
  const total = (resp && resp.total) || 0, covered = (resp && resp.coveredTotal) || 0;
  if (!(covered > 0)) return { empty: true, coverage: null, cards: [] };
  const pct = (v) => Math.round(v / covered * 100);
  const row = (name, cost, muted, sub, other) => Object.assign({ name, cost, pct: pct(cost), muted, sub }, other ? { other: true } : {});
  const cards = ["agent", "skill", "plugin", "mcp"].map((kind) => {
    const C = AN_COPY.drivers.cards[kind];
    const rows = [];
    let sum = 0;
    if (kind === "mcp") {
      for (const s of resp.mcp || []) {
        sum += s.costUsd || 0;
        rows.push(row(s.server, s.costUsd || 0, !!s.other, false, s.other));
        for (const t of s.tools || []) rows.push(row(t.name, t.costUsd || 0, !!t.other, true, t.other));
      }
    } else {
      for (const x of resp[kind] || []) { sum += x.costUsd || 0; rows.push(row(x.name, x.costUsd || 0, !!x.other, false, x.other)); }
    }
    const max = rows.reduce((m, r) => (r.muted ? m : Math.max(m, r.cost)), 0);
    rows.push(row(C.rest, Math.max(0, covered - sum), true, false));
    return { kind, title: C.title, caption: C.caption, rows, max };
  });
  return { empty: false, coverage: total > 0 && covered / total < 1 ? covered / total : null, cards };
}

// anWorkspaceTiles: the three tiles of the Workspaces view (§3.2) from anWorkspaceRows' rows: how many, the host / box share of
// their API value in whole percents that add to 100, and the costliest, whose name goes in the label. A dash with no workspace or
// no cost; a nameless one leaves no dangling " · ".
function anWorkspaceTiles(rows) {
  const L = AN_COPY.workspaces.kpis;
  rows = rows || [];
  let host = 0, box = 0, top = null;
  for (const r of rows) {
    host += r.hostCost || 0; box += r.boxCost || 0;
    if (!top || r.cost > top.cost) top = r;
  }
  const hp = host + box > 0 ? Math.round(host / (host + box) * 100) : null;
  return [
    { value: anCountText(rows.length), label: L[0] },
    { value: hp == null ? "—" : hp + "% · " + (100 - hp) + "%", label: L[1] },
    { value: top ? usd(top.cost) : "—", label: top && top.name ? L[2] + " · " + top.name : L[2] },
  ];
}

// anWorkspaceRowModel: one workspace row as the table prints it: {cells, children, expandable}. `cells` are the strings of the row
// (a missing median or cache hit is a dash) beside the shares the mini bars draw; `children` are the rows beneath it, each with its
// label, the muted line under the label and the same figures. `expandable` is true when there is more than one child (one child
// says nothing the row does not). The muted line: a host child shows the host path, a worktrees child its path; a box child shows
// "clone · {path}" whenever its path is not the workspace's own. The child's `via` is never read: a box child folds the clone and
// in-place sessions of one profile, so its path alone says whether it is a clone.
function anWorkspaceRowModel(row) {
  const W = AN_COPY.workspaces;
  const figs = (x) => ({
    cost: usd(x.cost), sessions: anCountText(x.sessions),
    median: x.median == null ? "—" : usd(x.median),
    cache: x.cacheHit == null ? "—" : (x.cacheHit * 100).toFixed(1) + "%",
  });
  // The workspace's real path (an unmatched row's `path` is a display sentence, not a folder).
  const real = row.workspace != null ? row.workspace : row.path;
  const line = (c, kind) => {
    const p = c.path || "";
    if (kind === "box") return p && p !== real ? W.clone.replace("{dir}", p) : "";
    return p;
  };
  const children = (row.children || []).map((c) => {
    const kind = c.label === "host" ? "host" : /^box( |$)/.test(c.label || "") ? "box" : "other";
    return { label: c.label, kind, line: line(c, kind), ...figs(c) };
  });
  const split = row.hostPct + row.boxPct > 0 ? row.hostPct + "% · " + row.boxPct + "%" : "—";
  return {
    cells: { name: row.name, path: row.path, unmatched: !!row.unmatched, ...figs(row), split, hostPct: row.hostPct, boxPct: row.boxPct, mix: row.mix || [] },
    children,
    expandable: children.length > 1,
  };
}

// anDriverTiles: the three tiles of the Cost drivers view (§3.3) from the /drivers response: the share of the covered API value that
// ran in subagents, the costliest plugin and the costliest MCP server (a server's own row, not one of its tools), the name of the
// last two in the label. A kind with no rows, or no covered value at all, is a dash and carries no name.
function anDriverTiles(resp) {
  const L = AN_COPY.drivers.kpis;
  const r = anDriverCards(resp);
  const named = (kind) => {
    const card = r.cards.find((c) => c.kind === kind);
    return card ? card.rows.filter((x) => (!x.muted || x.other) && !x.sub) : [];
  };
  const top = (rows) => rows.reduce((t, x) => (x.other || (t && x.cost <= t.cost) ? t : x), null);
  const agents = named("agent");
  const sub = agents.reduce((s, x) => s + x.cost, 0);
  const plugin = top(named("plugin")), mcp = top(named("mcp"));
  return [
    { value: agents.length ? Math.min(100, Math.round(sub / resp.coveredTotal * 100)) + "%" : "—", label: L[0] },
    { value: plugin ? usd(plugin.cost) : "—", label: plugin ? L[1] + " · " + plugin.name : L[1] },
    { value: mcp ? usd(mcp.cost) : "—", label: mcp ? L[2] + " · " + mcp.name : L[2] },
  ];
}

// anCoverageText: the muted line under the Cost drivers tiles when only part of the window has driver data. `coverage` is
// anDriverCards' ratio (null at full coverage). The percent is floored so a coverage that rounds to 100 never reads "100%" while
// some is missing, and a sliver reads "<1%", never "0%". The tiny epsilon keeps 0.29 (28.999999999999996 as a float) at 29.
function anCoverageText(coverage) {
  if (!(coverage > 0 && coverage < 1)) return "";
  const n = Math.min(99, Math.floor(coverage * 100 + 1e-9)); // under 1 means some value is uncovered, so never "100"
  return AN_COPY.drivers.coverage.replace("{N}", n < 1 ? "<1" : String(n));
}

// anModelMedians: /session-costs modelCosts rows ({sessionId, project, model, costUsd}) → {model: median}, over each model's own
// per-session cost (a session's rows of one model add up first), not the whole session's.
function anModelMedians(modelCosts) {
  const per = new Map(); // model → Map(session → cost)
  for (const r of modelCosts || []) {
    let m = per.get(r.model);
    if (!m) { m = new Map(); per.set(r.model, m); }
    const k = (r.sessionId || "") + String.fromCharCode(31) + (r.project || "");
    m.set(k, (m.get(k) || 0) + (r.costUsd || 0));
  }
  const out = Object.create(null); // a model named "constructor" or "__proto__" is a plain key
  for (const [model, m] of per) out[model] = median([...m.values()]);
  return out;
}

// ---- the cost chart's scale, geometry and legend ------------------------------------------------------------------------

// anYTicks: the y labels of the daily cost chart, top to bottom: `frac` is the label's height as a share of the peak (`max`) and
// `text` the dollars there. The page labels the peak and its two thirds and never the baseline (`withZero` false, digest-design
// §B6): $421, $281, $140. The phone also labels the zero line, "$0.00" (`withZero` true). With no scale (`max` 0, NaN, negative)
// there is nothing to label: [] (`!(max > 0)` is also true for NaN). The text is `usd(max * (1 - i / 3))`, written exactly as the
// phone's chart always wrote it: 1 - 1/3 is not 2/3 to the last bit, and a different expression could round a boundary value to
// another cent.
function anYTicks(max, withZero) {
  if (!(max > 0)) return [];
  const ticks = [];
  for (let i = 0; i <= (withZero ? 3 : 2); i++) ticks.push({ frac: (3 - i) / 3, text: usd(max * (1 - i / 3)) });
  return ticks;
}

// The desktop page's chart, in px (digest-design §B6, §D1): a 150px plot with the date labels 6px under it, a 44px gutter to its
// left whose 38px label box right-aligns the y labels, 16px bars. Used only when anIsPage(); the phone's chart keeps its own.
const AN_PAGE_CHART = { plotH: 150, gutter: 44, labelBox: 38, bar: 16, xLabelGap: 6 };

// anXLabels: the date labels of the page's cost chart, as { slot, text } in slot order, one per `every` slots from the first. In date
// mode (a window of at most 120 days) the slots are the window's days, so a day with no cost is labelled like any other and each
// label says its own slot's date (MM-DD): the labels fall on a regular grid, as the frame's do. In index mode (a very wide window,
// where slots are only the days that have a bar) the bars are the slots, as the phone's chart labels them.
function anXLabels(lay, from, every) {
  const out = [];
  if (lay.dateMode) {
    const t0 = Date.parse(from + "T00:00:00Z");
    for (let s = 0; s < lay.slots; s += every) out.push({ slot: s, text: new Date(t0 + s * 86400000).toISOString().slice(5, 10) });
  } else {
    for (const b of lay.bars) if (b.slot % every === 0) out.push({ slot: b.slot, text: b.day.slice(5) });
  }
  return out;
}

// anLegendItems: a legend's entries, a name and a colour per model, coloured by its place in `colorModels` (the canonical list;
// `models` itself when none is given) so a legend of only some models wears the colours the bars have.
function anLegendItems(models, colorModels) {
  const key = colorModels || models;
  return models.map((m) => ({ name: shortModel(m), color: modelColor(key, m) }));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    fmtCompact, usd, AN_API_VALUE_TITLE, tokensPerPrompt, modelsByCost, foldCostByDay, normModels,
    topSessionsByCost, distinctSessions,
    prevWindow, pctDelta, modelColor, shortModel, projShort, anWsName,
    collapseZeroCost, pricedModelIds, daysBetween, clampRange, stackedLayout,
    anCountText, anSessionKpis,
    median, cacheHit, kpiDelta, anOverviewKpis, anWorkspaceRows, anDriverCards, anModelMedians, AN_COPY,
    anWorkspaceRowModel, anWorkspaceTiles, anDriverTiles, anCoverageText,
    anModelFold, anPct, anWorkspaceBars, anSessionRow,
    anWhereText, anTabKey, anWhereLabel, anWhereOptions, anDeltaParts, anPerPrompt, anModelTableRow,
    anYTicks, AN_PAGE_CHART, anLegendItems, anXLabels,
  };
}

// ---- rendering (browser only) -----------------------------------------------

// where: the Where select's key ("" is All: host, box, "box:{profile}"; the API ignores a key it does not know). whereOpts: the
// select's options for the window, [{key, label, costUsd}]. failed: the newest load failed (data is null until one succeeds).
const AN = { tab: "overview", from: null, to: null, range: "30d", compare: false, customOpen: false, data: null, where: "", whereOpts: [], failed: false };
const AN_LOAD_FAILED = "Could not load analytics.";

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
  AN.where = "";
  AN.tab = "overview";
  AN.to = anTodayUTC();
  AN.from = anRangeFrom("30d");
  syncTabButtons();
  anSyncPageControls();
  await anLoad();
}

function syncTabButtons() {
  for (const b of document.querySelectorAll("#an-tabs button")) {
    b.classList.toggle("act", anTabKey(b.dataset.antab) === AN.tab);
  }
}

// anSetTab: choose a view (any key anTabKey knows: the phone's strip still says projects and tools). The phone's tab strip (.act)
// and the page's view switch (aria-pressed) both follow AN.tab; the window's data is already loaded, so it only re-renders (no
// refetch).
function anSetTab(tab) {
  AN.tab = anTabKey(tab);
  syncTabButtons();
  anSyncPageControls();
  anRender();
}

// anBtnWord: set a button's word, keeping the glyph the page's Re-scan carries before it (#168); a button with no glyph is
// plain text, as the phone's is.
function anBtnWord(btn, word) {
  const ic = btn.querySelector(".ui-icon");
  if (!ic) { btn.textContent = word; return; }
  btn.replaceChildren(ic, document.createTextNode(word));
}

// anRescan: read the transcripts again, then reload the window. `btn` is the phone's #analytics-refresh or the page's
// #an-rescan: it disables itself and says "Scanning…" meanwhile. On the page it is the control in use, so focus comes
// back to it if the disabled button lost it (and only then: a focus the user moved on is left where they put it).
async function anRescan(btn) {
  const keep = anIsPage() && document.activeElement === btn;
  btn.disabled = true;
  anBtnWord(btn, "Scanning…");
  try { await api("/api/analytics/ingest", { method: "POST" }); await anLoad(); }
  catch (_) { /* leave the current view; the range line still shows */ }
  finally {
    btn.disabled = false;
    anBtnWord(btn, "Re-scan");
    const now = document.activeElement;
    if (keep && (!now || now === document.body)) btn.focus();
  }
}

// anCacheOfModels: the cache hit of normModels rows taken together (null when they read nothing).
function anCacheOfModels(rows) {
  const t = { cacheRead: 0, input: 0, cacheCreation: 0 };
  for (const r of rows || []) { t.cacheRead += r.cacheRead || 0; t.input += r.input || 0; t.cacheCreation += r.cacheCreation || 0; }
  return cacheHit(t);
}

// anSessionMedian: the median cost of a window's sessions, each taken whole (its day and model rows added up); null for none.
function anSessionMedian(rows) {
  return median(topSessionsByCost(rows, Infinity).map((x) => x.cost));
}

// anLoadSeq numbers the loads. Several can be in flight (a closed select fires change on each arrow key), and they may answer out of
// order: only the newest load may write AN.data or draw, so the page never shows the figures of a different Where, range or
// Compare than the controls say.
let anLoadSeq = 0;

async function anLoad() {
  const body = document.getElementById("analytics-body");
  if (!body) return;
  const seq = ++anLoadSeq;
  body.replaceChildren(anEmpty("Loading…"));
  const win = `from=${AN.from}&to=${AN.to}`;
  const q = `${win}&where=${encodeURIComponent(AN.where)}`; // one code path: an empty where is "All"
  let summary, models, workspaces, sessions, drivers, where;
  try {
    [summary, models, workspaces, sessions, drivers, where] = await Promise.all([
      api(`/api/analytics/summary?${q}`),
      api(`/api/analytics/models?${q}`),
      api(`/api/analytics/workspaces?${q}`),
      api(`/api/analytics/session-costs?${q}`),
      api(`/api/analytics/drivers?${q}`),
      api(`/api/analytics/where?${win}`),
    ]);
  } catch (_) {
    // The newest load failed: the window it was to replace is dropped with it, so no later draw (a tab switch) shows the figures
    // of a Where or range the controls no longer say; that draw shows the failure again.
    if (seq === anLoadSeq) { AN.data = null; AN.failed = true; body.replaceChildren(anEmpty(AN_LOAD_FAILED)); }
    return;
  }
  if (seq !== anLoadSeq) return; // a newer load has been asked for: this answer is for a filter that is no longer chosen
  AN.failed = false;
  const byDayModel = (summary && summary.byDayModel) || [];
  const sessRows = (sessions && sessions.sessions) || [];
  AN.whereOpts = (where && where.options) || [];
  AN.data = {
    from: summary ? summary.from : AN.from,
    to: summary ? summary.to : AN.to,
    costByDay: foldCostByDay(byDayModel),
    byModel: normModels((models && models.byModel) || []),
    workspaces: anWorkspaceRows(workspaces),
    drivers: anDriverCards(drivers),
    driverTiles: anDriverTiles(drivers),
    modelMedians: anModelMedians(sessions && sessions.modelCosts),
    sessions: sessRows,
    topSessions: topSessionsByCost(sessRows, 18),
    sessionCount: distinctSessions(sessRows),
    median: anSessionMedian(sessRows),
    cmp: null,
  };
  AN.data.cache = anCacheOfModels(AN.data.byModel);
  AN.data.models = AN.data.costByDay.models;
  AN.data.pricedModels = pricedModelIds(AN.data.byModel); // for legends: drop $0 models
  if (AN.compare) {
    const cmp = await anLoadCompare();
    if (seq !== anLoadSeq) return;
    AN.data.cmp = cmp;
  }
  anRender();
}

// anLoadCompare: the window before this one (equal length), only what the Overview's four tiles need: its API value, sessions,
// median per session and cache hit, returned as {days, prev} for anLoad to keep as AN.data.cmp (anOverviewKpis); null when the
// fetch fails.
async function anLoadCompare() {
  const pw = prevWindow(AN.from, AN.to);
  const q = `from=${pw.from}&to=${pw.to}&where=${encodeURIComponent(AN.where)}`;
  let ps, pm, psess;
  try {
    [ps, pm, psess] = await Promise.all([
      api(`/api/analytics/summary?${q}`),
      api(`/api/analytics/models?${q}`),
      api(`/api/analytics/session-costs?${q}`),
    ]);
  } catch (_) {
    return null; // comparison is best-effort; leave cmp null
  }
  const sessRows = (psess && psess.sessions) || [];
  return {
    days: daysBetween(AN.from, AN.to),
    prev: {
      cost: foldCostByDay((ps && ps.byDayModel) || []).totals.cost,
      sessions: distinctSessions(sessRows),
      median: anSessionMedian(sessRows),
      cache: anCacheOfModels(normModels((pm && pm.byModel) || [])),
    },
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
    body.appendChild(anEmpty(!d && AN.failed ? AN_LOAD_FAILED : "No usage recorded in this window. Try a wider range or Re-scan."));
    return;
  }
  // The five views are the phone's builders; on the page each hands over to its an...Page builder (anIsPage), both reading AN.data.
  const R = { overview: anOverview, workspaces: anWorkspaces, drivers: anDrivers, sessions: anSessions, models: anModels };
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

// anPickWhere: filter every view to one Where key and reload. The select is the control in use and is never rebuilt, so it keeps
// focus; `sel` is given focus again in case a load took it.
function anPickWhere(key, sel) {
  AN.where = key;
  anLoad();
  if (sel) sel.focus();
}

// anToggleCompare: compare this window with the equal-length one before it.
function anToggleCompare() {
  AN.compare = !AN.compare;
  anSyncPageControls();
  anLoad();
}

// control strip of the phone's modal: range presets + compare toggle, the Custom range's dates inline, then the Where select on a
// line of its own. (The desktop page has no strip: its toolbar is static markup, anSyncPageControls.)
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
  wrap.appendChild(anPhoneWhere());
  return wrap;
}

// anPhoneWhere: the phone's Where select, on a line of its own under the range chips (the page's is static markup in its toolbar).
// Its options and labels are the page's (anWhereOptions) and a change filters through the same action (anPickWhere). Text only.
function anPhoneWhere() {
  const line = el("div", "an-pwhere");
  const sel = el("select");
  sel.setAttribute("aria-label", AN_COPY.head.where);
  for (const o of anWhereOptions(AN.whereOpts, AN.where)) { const op = el("option", null, o.text); op.value = o.key; sel.appendChild(op); }
  sel.value = AN.where;
  sel.onchange = () => anPickWhere(sel.value, sel);
  line.append(el("span", "an-clabel", AN_COPY.head.where), sel);
  return line;
}

// ---- the desktop page's head and toolbar (index.html .an-phead, .an-ptools, .an-pcustom) -----------------------
// Static nodes: nothing here rebuilds them, so the control a click came from keeps focus.

// anSyncPageControls: bring them up to date with AN: aria-pressed on the view and range items, Custom… and Compare
// (Custom… mirrors the phone's `.on`: lit by an applied custom range, not by the row being open), the Custom row's hidden, and the
// description's window (the requested one, so a pressed range is named while it loads and if its load fails). The row's dates
// default to the window when the row opens. A no-op unless the dialog is the page.
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
    const line = AN_COPY.head.line;
    desc.textContent = AN.from && AN.to ? line.replace("{from}", AN.from).replace("{to}", AN.to) : line.slice(0, line.indexOf(" · {from}"));
  }
  anPaintWhere();
}

// anWhereSig: what the Where select's options say now, so they are rebuilt only when it changes.
let anWhereSig = "";

// anPaintWhere: the page's Where select: its options (anWhereOptions, as text) and the chosen key. The select itself is static
// markup and is never replaced, so it keeps focus while a change reloads the window; only its options are swapped, and only when
// they differ.
function anPaintWhere() {
  const sel = document.getElementById("an-where");
  if (!sel) return;
  const opts = anWhereOptions(AN.whereOpts, AN.where);
  const sig = JSON.stringify(opts);
  if (sig !== anWhereSig) {
    anWhereSig = sig;
    sel.replaceChildren(...opts.map((o) => { const op = el("option", null, o.text); op.value = o.key; return op; }));
  }
  sel.value = AN.where;
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
  const where = dlg.querySelector("#an-where");
  if (where) where.onchange = () => anPickWhere(where.value, where);
  on("#an-papply", () => {
    const dates = dlg.querySelectorAll(".an-pcustom .an-date");
    if (dates.length < 2) return;
    const r = anApplyCustom(dates[0].value, dates[1].value);
    if (r) { dates[0].value = r.from; dates[1].value = r.to; }
  });
}

// ---- Overview ---------------------------------------------------------------
function anOverview(root) {
  if (anIsPage()) return anOverviewPage(root);
  const d = AN.data;
  // The page's four tiles (anOverviewKpis) in a 2×2 grid; each carries its change vs the window before while Compare is on.
  const kpis = el("div", "an-kpis");
  anOverviewKpis(d, AN.compare ? d.cmp : null).forEach((t, i) => kpis.appendChild(anKpi(i === 0 || i === 2 ? "cost" : "", t.value, t.label, t.delta)));
  root.appendChild(kpis);

  const sec = anSection(AN_COPY.overview.chart, AN_COPY.overview.chartHint);
  const box = el("div", "an-box");
  box.appendChild(anStackedTime(d.costByDay, d.from, d.to));
  anLegend(box, d.pricedModels, d.models);
  sec.appendChild(box);
  root.appendChild(sec);

  const g = el("div", "an-grid2");
  const b1 = el("div", "an-box"); b1.appendChild(el("h4", null, AN_COPY.overview.byModel));
  const fold = anModelFold(d.byModel);
  for (const x of fold.rows) anHbar(b1, shortModel(x.model), x.cost, fold.maxCost, anModelColor(d.models, x.model), usd);
  // Cost by workspace: the page's top 8, each bar split host / box, with the key under it.
  const b2 = el("div", "an-box"); b2.appendChild(el("h4", null, AN_COPY.overview.byWorkspace + " (top " + AN_TOP_WORKSPACES + ")"));
  anWorkspaceBarRows(b2, d.workspaces, AN_TOP_WORKSPACES);
  g.append(b1, b2);
  const sec2 = anSection("Where the money goes"); sec2.appendChild(g);
  root.appendChild(sec2);

  root.appendChild(anPhoneNote(AN_COPY.overview.note));
}

// anOverviewPage: M3.1 (§3.1; the mockup's frame 1): the four tiles (anOverviewKpis: API value, Sessions, Median per session, Cache
// hit; each carries its change vs the window before only while Compare is on); the cost chart's card, its head holding the title,
// its caption and the legend; then cost by model and cost by workspace as two cards; the footnote last, a plain line. Compare shows
// nothing else. The phone's anOverview above draws the same figures in its own components.
function anOverviewPage(root) {
  const d = AN.data;
  const tiles = anOverviewKpis(d, AN.compare ? d.cmp : null);
  tiles[0].title = AN_API_VALUE_TITLE;
  root.appendChild(anKpiRow(tiles));

  const sec = el("div", "an-psec");
  const card = el("div", "an-card");
  const head = el("div", "card-head");
  head.append(el("h2", "t-md w-600", AN_COPY.overview.chart), el("span", "t-sm muted", AN_COPY.overview.chartHint), el("span", "grow"), anPLegend(d.pricedModels, d.models));
  card.appendChild(head);
  const body = anCardBody(card);
  body.classList.add("an-chartbody");
  body.appendChild(anStackedTime(d.costByDay, d.from, d.to));
  sec.appendChild(card);
  root.appendChild(sec);

  const grid = el("div", "an-pgrid");
  const fold = anModelFold(d.byModel);
  const mc = anCard(AN_COPY.overview.byModel, null, "h2");
  const mrows = el("div", "an-hrows");
  for (const x of fold.rows) {
    // The "other (unpriced)" row is a label, not a model id: sans and muted, with an empty track ($0 draws no fill).
    const unpriced = x.model === "other (unpriced)";
    anHrow(mrows, shortModel(x.model), !unpriced, x.cost, fold.maxCost, [{ v: x.cost, c: anModelColor(d.models, x.model) }], false, anPct(x.cost, fold.totalCost));
  }
  anCardBody(mc).appendChild(mrows);

  // Cost by workspace: the top 8, each bar split into its host and box cost (the --an-6 and --an-1 slots), as a share of the window.
  const total = d.costByDay.totals.cost || 1;
  const wb = anWorkspaceBars(d.workspaces, AN_TOP_WORKSPACES);
  const wc = anCard(AN_COPY.overview.byWorkspace, AN_COPY.overview.byWorkspaceHint, "h2");
  const wrows = el("div", "an-hrows");
  for (const b of wb.bars) anHrow(wrows, b.name, true, b.cost, wb.max, b.segs, true, anPct(b.cost, total));
  const wbody = anCardBody(wc);
  wbody.appendChild(wrows);
  wbody.appendChild(anWhereKey());
  grid.append(mc, wc);
  root.appendChild(grid);
  root.appendChild(anFootnote(AN_COPY.overview.note));
}

// anWhereKey: what the workspace bars' two colours are: the same swatch and mono label as the chart's legend.
function anWhereKey() {
  const key = el("div", "an-plegend an-wkey");
  for (const [name, color] of [["host", "var(--an-6)"], ["box", "var(--an-1)"]]) {
    const s = el("span"), sw = el("i", "an-sw");
    sw.style.background = color;
    s.append(sw, el("span", "mono t-xs fg", name));
    key.appendChild(s);
  }
  return key;
}

// ---- Models -----------------------------------------------------------------
function anModels(root) {
  if (anIsPage()) return anModelsPage(root);
  const d = AN.data;
  const sec = anSection(AN_COPY.models.title, AN_COPY.models.hint);
  const fold = anModelFold(d.byModel); // $0 (unpriced) models are folded into "other"
  const box = el("div", "an-box");
  const tbl = el("table", "an-table");
  tbl.appendChild(anColHead(AN_COPY.models.columns, 1, { 3: AN_COPY.models.sessionsTitle }));
  for (const x of fold.rows) {
    const c = anModelTableRow(x, fold, d.modelMedians);
    const tr = el("tr");
    const nm = el("td", c.other ? "dim" : "mono");
    const sw = el("span", "an-swatch"); sw.style.background = anModelColor(d.models, x.model);
    nm.append(sw, document.createTextNode(shortModel(x.model)));
    tr.append(nm, anTd(c.cost, "num"), anTd(c.pct, "dim num"), anTd(c.sessions, "dim num"), anTd(c.median, "dim num"),
      anTd(c.perPrompt, "dim num"), anTd(c.cache, "dim num"), anTd(c.tokens, "dim num"));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);
  root.appendChild(anPhoneNote(AN_COPY.models.note));
}

// ---- Workspaces -------------------------------------------------------------
// The page's three tiles, the bars (the page's top-of-Overview split, host / box) and the table: the page's columns less Model mix
// (a mini bar), a row a plain line (no breakdown to open).
function anWorkspaces(root) {
  if (anIsPage()) return anWorkspacesPage(root);
  const d = AN.data;
  root.appendChild(anPhoneKpis(anWorkspaceTiles(d.workspaces), [2]));
  const sec = anSection(AN_COPY.workspaces.title);
  const box = el("div", "an-box");
  anWorkspaceBarRows(box, d.workspaces, AN_PHONE_WORKSPACE_BARS);
  sec.appendChild(box);

  const box2 = el("div", "an-box"); const tbl = el("table", "an-table");
  const cols = AN_COPY.workspaces.columns.filter((_, i) => i !== 4);
  const head = anColHead(cols, cols.length);
  for (const i of [1, 2, 4, 5]) head.children[i].className = "num";
  tbl.appendChild(head);
  for (const row of d.workspaces) {
    const c = anWorkspaceRowModel(row).cells;
    const name = el("td");
    const nm = el("span", "mono", c.name);
    nm.setAttribute("title", c.name);
    name.appendChild(nm);
    if (c.unmatched) name.appendChild(el("span", "an-badge", AN_COPY.workspaces.unmatched));
    const tr = el("tr");
    tr.append(name, anTd(c.cost, "num"), anTd(c.sessions, "num"), anTd(c.split), anTd(c.median, "num"), anTd(c.cache, "num"));
    tbl.appendChild(tr);
  }
  box2.appendChild(anTableWrap(tbl)); sec.appendChild(box2);
  root.appendChild(sec);
  root.appendChild(anPhoneNote(AN_COPY.workspaces.note));
}

// ---- Sessions ---------------------------------------------------------------
// The page's three tiles, then its eight columns and cells. A row opens nothing here: the phone has no live-session action to share
// with it.
function anSessions(root) {
  if (anIsPage()) return anSessionsPage(root);
  const d = AN.data;
  root.appendChild(anPhoneKpis(anSessionKpis(d), [1, 2]));
  const sec = anSection(AN_COPY.sessions.title, "Spot the runaway session burning the budget. Counts include subagent / synthetic transcripts.");
  const box = el("div", "an-box"); const tbl = el("table", "an-table");
  tbl.appendChild(anColHead(AN_COPY.sessions.columns, 6));
  for (const s of d.topSessions) {
    const r = anSessionRow(s);
    const branch = anTd(r.branch || "—", r.branch ? "mono" : "dim");
    if (r.branch) branch.setAttribute("title", r.branch);
    const wsTd = anTd(r.workspace, "mono");
    wsTd.setAttribute("title", r.workspacePath || r.workspace);
    const tr = el("tr");
    tr.append(anTd(r.sid, "mono"), anTd(r.day, "dim"), wsTd, anTd(r.where), branch,
      anTd(r.model, "dim"), anTd(r.prompts, "num"), anTd(r.cost, "num"));
    tbl.appendChild(tr);
  }
  box.appendChild(anTableWrap(tbl)); sec.appendChild(box); root.appendChild(sec);
}

// ---- Cost drivers -----------------------------------------------------------
// The page's three tiles, then four sections in the bar style, each row name · bar · API value · % of the covered value; the muted
// last row has no bar. With nothing covered the sentence alone, no tiles (as the page).
function anDrivers(root) {
  if (anIsPage()) return anDriversPage(root);
  const dr = AN.data.drivers;
  if (dr.empty) {
    const sec = anSection(AN_COPY.drivers.title);
    sec.appendChild(anEmpty(AN_COPY.drivers.empty));
    root.appendChild(sec);
    return;
  }
  root.appendChild(anPhoneKpis(AN.data.driverTiles, [1, 2]));
  const cov = anCoverageText(dr.coverage);
  if (cov) root.appendChild(el("p", "an-hint", cov));
  for (const card of dr.cards) {
    const sec = anSection(card.title, card.caption || null);
    const box = el("div", "an-box");
    for (const row of card.rows) anDriverBar(box, row, card.max);
    sec.appendChild(box);
    root.appendChild(sec);
  }
  root.appendChild(anPhoneNote(AN_COPY.drivers.note));
}

// anDriverBar: a row of a Cost drivers section in the phone's bar style: the name, the track with one fill scaled to the card's
// largest named row, the API value and the % (a fourth column, .an-hpct). The muted last row, and any row of a card with no named
// row (max 0), has no fill: nothing is divided by 0. An MCP tool (sub) is indented by the stylesheet.
function anDriverBar(parent, row, max) {
  const r = el("div", "an-hbar an-hpct" + (row.muted ? " an-mut" : "") + (row.sub ? " an-sub" : ""));
  const nm = el("div", "nm", row.name); nm.title = row.name;
  const track = el("div", "track");
  if (!row.muted && max > 0 && row.cost > 0) {
    const f = el("div", "fill");
    f.style.width = (row.cost / max * 100) + "%";
    f.style.background = "var(--an-1)";
    track.appendChild(f);
  }
  r.append(nm, track, el("div", "val", usd(row.cost)), el("div", "val", row.pct + "%"));
  parent.appendChild(r);
}

// anWorkspaceBarRows: a box's host / box bars per workspace (anWorkspaceBars, the page's own figures) and the key to the two colours.
function anWorkspaceBarRows(box, rows, limit) {
  const wb = anWorkspaceBars(rows, limit);
  for (const b of wb.bars) anHbarSegs(box, b.name, b.cost, wb.max, b.segs, usd);
  anSwatchLegend(box, [{ name: "host", color: "var(--an-6)" }, { name: "box", color: "var(--an-1)" }]);
}

// anPhoneNote: the footnote under a phone view: one plain muted line (the phone's .an-hint).
function anPhoneNote(text) { return el("p", "an-hint", text); }

// anPhoneKpis: the tiles the page draws atop a view ({value, label} from anWorkspaceTiles, anDriverTiles, anSessionKpis), in the
// phone's own tile (anKpi) and 2×2 grid, as the phone's Overview draws its four. The tiles at the indexes in `costAt` are dollar
// figures and wear the Overview's cost value.
function anPhoneKpis(tiles, costAt) {
  const kpis = el("div", "an-kpis");
  tiles.forEach((t, i) => kpis.appendChild(anKpi(costAt.includes(i) ? "cost" : "", t.value, t.label, t.delta)));
  return kpis;
}

// ---- the desktop page's views (Models, Sessions, Workspaces, Cost drivers) ------------------------------------------------------
// The phone's functions above return into these under anIsPage(); everything the phone draws is those functions' own code.
// The words, figures, colours and caps are the same preparation the phone's builders call (AN_COPY, anModelFold, anModelTableRow,
// anWorkspaceRowModel, anSessionRow, anWorkspaceBars ...): only the DOM differs.
// The page's parts are the kit's: a KPI row, cards (.an-card: --panel and --r-card, a name of its own because app.css's `.card` is the
// phone's amber session card) and the table look of §C9 on the same .an-table.

// anModelsPage: §3.5 (the mockup's frame 5): the one table in a card, then the footnote. Its eight columns are AN_COPY.models.columns;
// the Sessions header says in a tooltip that a session which used several models counts once in each.
function anModelsPage(root) {
  const d = AN.data;
  const fold = anModelFold(d.byModel);
  const tbl = el("table", "an-table");
  tbl.appendChild(anColHead(AN_COPY.models.columns, 1, { 3: AN_COPY.models.sessionsTitle }));
  for (const x of fold.rows) {
    const tr = el("tr");
    // A model id is machine text (mono); the "other (unpriced)" row is a label, so sans and muted.
    const c = anModelTableRow(x, fold, d.modelMedians);
    const nm = el("td");
    const sw = el("span", "an-swatch"); sw.style.background = anModelColor(d.models, x.model);
    nm.append(sw, el("span", c.other ? "muted" : "mono t-sm", shortModel(x.model)));
    tr.append(nm, anTd(c.cost, "num w-500"), anTd(c.pct, "num"), anTd(c.sessions, "num"), anTd(c.median, "num"), anTd(c.perPrompt, "num"),
      anTd(c.cache, "num"), anTd(c.tokens, "num sub"));
    tbl.appendChild(tr);
  }
  const sec = el("section", "an-psec");
  const card = anCard();
  card.appendChild(anTableWrap(tbl));
  sec.append(card, anFootnote(AN_COPY.models.note));
  root.appendChild(sec);
}

// anSessionsPage: §3.4 (the mockup's frame 4). Three KPI tiles, then the top sessions in a card whose head says a row opens its session.
// A row culibox still holds (liveId) is a button-like row; any other is plain: no role, no focus stop, no hover or cursor.
function anSessionsPage(root) {
  const d = AN.data;
  root.appendChild(anKpiRow(anSessionKpis(d)));

  const tbl = el("table", "an-table an-stbl");
  tbl.appendChild(anColHead(AN_COPY.sessions.columns, 6));
  for (const s of d.topSessions) {
    const r = anSessionRow(s);
    const tr = el("tr");
    // Session, Workspace, Branch and a single Model are ids (mono); "mixed" and the day are muted sans. Only Workspace wraps (§B8
    // .m3-wrap); Branch ellipsizes and carries its full name in title.
    const where = el("td");
    where.appendChild(el("span", r.box ? "chip chip-box an-wbox" : r.where === "host" ? "chip chip-host" : "chip", r.where));
    const branch = el("td", r.branch ? "an-ell" : "an-ell sub");
    if (r.branch) { branch.appendChild(el("span", "mono t-sm", r.branch)); branch.setAttribute("title", r.branch); }
    else branch.textContent = "—";
    const wsTd = anIdTd(r.workspace, "an-wrap");
    if (r.workspacePath) wsTd.setAttribute("title", r.workspacePath);
    tr.append(anIdTd(r.sid), anTd(r.day, "sub tnum"), wsTd, where, branch,
      r.mixed ? anTd(r.model, "sub") : anIdTd(r.model), anTd(r.prompts, "num"), anTd(r.cost, "num w-500"));
    if (r.clickable) anLiveRow(tr, r.liveId);
    tbl.appendChild(tr);
  }
  const sec = el("section", "an-psec");
  const card = anCard(AN_COPY.sessions.title, AN_COPY.sessions.hint, "h2");
  card.appendChild(anTableWrap(tbl));
  sec.appendChild(card);
  root.appendChild(sec);
}

// anLiveRow: make a session's table row do what clicking that session's sidebar row does (anOpenLive). A button-like row: role,
// a focus stop, Enter and Space.
function anLiveRow(tr, liveId) {
  tr.classList.add("an-live");
  tr.setAttribute("role", "button");
  tr.setAttribute("tabindex", "0");
  tr.onclick = () => anOpenLive(liveId);
  tr.onkeydown = (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    anOpenLive(liveId);
  };
}

// anOpenLive: the sidebar row's own action (sessionRowAction), on the same terms as the row: the page is the desktop's, so the row is
// the compact one and a companion shell is nested (revealed, not opened), and a session being ended is not clickable. The live list
// is also what says the session is still there: a session it no longer holds opens nothing.
function anOpenLive(id) {
  if (typeof sessionRowAction !== "function" || typeof lastSessions === "undefined") return;
  const live = (lastSessions || []).find((x) => x && x.id === id);
  if (!live || killing.has(live.id)) return;
  sessionRowAction(live, isNestedShell(live, lastSessions));
}

// anOpenWs: the workspaces whose breakdown is open. It outlives a redraw (a Where or range change), so a row the user opened stays open.
const anOpenWs = new Set();

// anMini: a 70px mini bar of coloured parts ({pct, color, title}); a part with no share draws nothing, so no share is an empty track.
function anMini(parts) {
  const bar = el("span", "an-mini");
  for (const p of parts) {
    if (!(p.pct > 0)) continue;
    const f = el("i");
    f.style.width = p.pct + "%";
    f.style.background = p.color;
    if (p.title) f.setAttribute("title", p.title);
    bar.appendChild(f);
  }
  return bar;
}

// anWsChildRow: a breakdown row (host, box · profile, worktrees (N)): the label as a chip, the muted line after it, then its API value,
// sessions, median and cache hit. Host · Box and Model mix are empty: a child is one side of the split.
function anWsChildRow(c) {
  const tr = el("tr", "an-child");
  const td = el("td", "an-wcell");
  td.appendChild(el("span", c.kind === "box" ? "chip chip-box an-wbox" : c.kind === "host" ? "chip chip-host" : "chip", c.label));
  if (c.line) {
    const line = el("span", "an-wpath mono t-xs muted", c.line);
    line.setAttribute("title", c.line);
    td.appendChild(line);
  }
  tr.append(td, anTd(c.cost, "num"), anTd(c.sessions, "num"), el("td"), el("td"), anTd(c.median, "num"), anTd(c.cache, "num"));
  return tr;
}

// anWsRow: a workspace's row in `tbl` and, while it is open, its child rows right after it. The open state is the button's
// aria-expanded; pressing it only changes the button's glyph and adds or removes the child rows, so focus is never lost.
function anWsRow(tbl, models, row) {
  const m = anWorkspaceRowModel(row), c = m.cells;
  const key = (row.unmatched ? "u" : "w") + "\n" + row.name + "\n" + row.path;
  const tr = el("tr");
  const kids = [];
  const name = el("td", "an-wcell");
  let btn = null;
  if (m.expandable) {
    btn = el("button", "an-exp", "▸");
    btn.setAttribute("type", "button");
    btn.setAttribute("aria-expanded", "false");
    btn.setAttribute("aria-label", c.name);
    name.appendChild(btn);
  } else {
    const sp = el("span", "an-exp");
    sp.setAttribute("aria-hidden", "true");
    name.appendChild(sp);
  }
  const text = el("div", "an-wtext");
  const top = el("div", "an-wtop");
  const nm = el("span", "an-wname mono t-sm", c.name);
  nm.setAttribute("title", c.name);
  top.appendChild(nm);
  if (c.unmatched) top.appendChild(el("span", "chip an-wtag", AN_COPY.workspaces.unmatched));
  const path = el("div", "an-wpath mono t-xs muted", c.path);
  path.setAttribute("title", c.path);
  text.append(top, path);
  name.appendChild(text);

  const where = el("td", "tnum");
  where.append(anMini([{ pct: c.hostPct, color: "var(--an-6)", title: "host " + c.hostPct + "%" }, { pct: c.boxPct, color: "var(--an-1)", title: "box " + c.boxPct + "%" }]),
    document.createTextNode(c.split));
  const mix = el("td");
  mix.appendChild(anMini(c.mix.map((x) => ({ pct: x.share * 100, color: anModelColor(models, x.model), title: shortModel(x.model) + " " + Math.round(x.share * 100) + "%" }))));
  tr.append(name, anTd(c.cost, "num w-500"), anTd(c.sessions, "num"), where, mix, anTd(c.median, "num"), anTd(c.cache, "num"));
  tbl.appendChild(tr);

  const set = (on) => {
    btn.setAttribute("aria-expanded", on ? "true" : "false");
    btn.textContent = on ? "▾" : "▸";
    tr.classList.toggle("an-open", on);
    for (const k of kids) k.remove();
    kids.length = 0;
    let prev = tr;
    if (on) for (const ch of m.children) { const k = anWsChildRow(ch); kids.push(k); prev.after(k); prev = k; }
    if (on) anOpenWs.add(key); else anOpenWs.delete(key);
  };
  if (btn) {
    btn.onclick = () => set(btn.getAttribute("aria-expanded") !== "true");
    if (anOpenWs.has(key)) set(true);
  }
}

// anWorkspacesPage: §3.2 (the mockup's frame 2): three tiles, then one kit table in a card, a row per repo with the breakdown a button
// opens beneath it, then the footnote. Host · Box and Model mix are 70px mini bars (host --an-6, box --an-1, models by their --an
// slot) beside the figures; the numbers are right-aligned under their headers.
function anWorkspacesPage(root) {
  const d = AN.data;
  root.appendChild(anKpiRow(anWorkspaceTiles(d.workspaces)));
  const tbl = el("table", "an-table an-wtbl");
  const head = anColHead(AN_COPY.workspaces.columns, AN_COPY.workspaces.columns.length);
  for (const i of [1, 2, 5, 6]) head.children[i].className = "num";
  tbl.appendChild(head);
  for (const row of d.workspaces) anWsRow(tbl, d.models, row);
  const sec = el("section", "an-psec");
  const card = anCard();
  card.appendChild(anTableWrap(tbl));
  sec.append(card, anFootnote(AN_COPY.workspaces.note));
  root.appendChild(sec);
}

// anDriverRow: a row of a Cost drivers card: the name, the track (the kit's .meter) with one fill scaled to the card's largest named
// row, the API value, and the row's % of the covered value. The muted last row (outside / main agent) has no fill, and a card with
// no named row (max 0) draws no fill at all: nothing is divided by it. An MCP tool row (sub) is indented by the stylesheet.
function anDriverRow(parent, row, max) {
  const r = el("div", "an-hrow an-drow an-pct" + (row.muted ? " an-mut" : "") + (row.sub ? " an-sub" : ""));
  const nm = el("span", "mono t-sm ellipsis" + (row.muted ? " muted" : ""), row.name);
  nm.setAttribute("title", row.name);
  const meter = el("span", "meter");
  if (!row.muted && max > 0 && row.cost > 0) {
    const f = el("i");
    f.style.width = (row.cost / max * 100) + "%";
    f.style.background = "var(--an-1)";
    meter.appendChild(f);
  }
  r.append(nm, meter, el("span", "t-sm tnum an-hv" + (row.muted ? " muted" : ""), usd(row.cost)), el("span", "t-sm tnum muted an-hp", row.pct + "%"));
  parent.appendChild(r);
}

// anDriversPage: §3.3 (the mockup's frame 3): three tiles, one muted coverage line when driver data covers only part of the window,
// four cards in a 2×2 grid (one column under 1000px of body, as Plan 5's split), then the footnote. With nothing covered there is
// nothing to divide by: the sentence alone, no tiles.
function anDriversPage(root) {
  const d = AN.data, dr = d.drivers;
  if (dr.empty) { root.appendChild(anEmpty(AN_COPY.drivers.empty)); return; }
  root.appendChild(anKpiRow(d.driverTiles));
  const cov = anCoverageText(dr.coverage);
  if (cov) root.appendChild(el("p", "an-cov t-sm muted", cov));
  const grid = el("div", "an-dgrid");
  for (const card of dr.cards) {
    const c = anCard(card.title, card.caption || null, "h2");
    const rows = el("div", "an-hrows");
    for (const row of card.rows) anDriverRow(rows, row, card.max);
    anCardBody(c).appendChild(rows);
    grid.appendChild(c);
  }
  root.appendChild(grid);
  root.appendChild(anFootnote(AN_COPY.drivers.note));
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
// anKpi: the phone's tile. `delta` (kpiDelta's {text, dir}, with Compare on) adds the change line under the label, as the page's tile has.
function anKpi(cls, v, l, delta) {
  const c = el("div", "an-kpi");
  c.append(el("div", "v " + (cls || ""), v), el("div", "l", l));
  if (delta) c.appendChild(anDeltaLine(delta));
  return c;
}
// The desktop page's parts (callers are the page's views, behind anIsPage()). anKpiRow and anCard are general: every view builds on them.
// anKpiRow: the kit's KPI row, the one Audit draws too: div.kpi-row > div.kpi > span.kpi-value + span.kpi-label. The label
// ellipsizes (desktop.css), so it carries its full text in title: the tile's own, else the label.
function anKpiRow(tiles) {
  const row = el("div", "kpi-row");
  for (const t of tiles) {
    const tile = el("div", "kpi");
    const label = el("span", "kpi-label", t.label);
    label.setAttribute("title", t.title || t.label);
    tile.append(el("span", "kpi-value", t.value), label);
    // A tile may carry a change line ({text, dir} from kpiDelta), under the label.
    if (t.delta) tile.appendChild(anDeltaLine(t.delta));
    row.appendChild(tile);
  }
  return row;
}
// anDeltaLine: a tile's change line: the figure ("▲ 12%") in the tone of its direction (an-dup, an-ddown, an-dflat: palette hues and
// muted, never red or green: the arrow carries the direction), then the window it is against as plain muted text.
function anDeltaLine(delta) {
  const parts = anDeltaParts(delta.text);
  const line = el("div", "an-kdelta");
  line.appendChild(el("span", "an-dlead an-d" + (delta.dir === "up" || delta.dir === "down" ? delta.dir : "flat"), parts.lead));
  if (parts.rest) line.appendChild(document.createTextNode(parts.rest));
  return line;
}
// anCard: a card (--panel, --r-card). A title and/or a caption give it the kit's .card-head, a hairline under it; with neither
// it is a bare panel, as a table's card is. The title is an h3 unless `level` says otherwise (the views' cards are the frame's
// h2). Content goes in anCardBody (padded) or, for a table, straight in.
function anCard(title, caption, level) {
  const card = el("div", "an-card");
  if (title || caption) {
    const head = el("div", "card-head");
    if (title) head.appendChild(el(level || "h3", "t-md w-600", title));
    if (caption) head.appendChild(el("span", "t-sm muted", caption));
    card.appendChild(head);
  }
  return card;
}
function anCardBody(card) { const b = el("div", "an-card-body"); card.appendChild(b); return b; }
// anFootnote (the page): the note under a card as one plain muted line (§A2: no box, no border, no italics). Plain text only.
function anFootnote(text) { return el("p", "an-foot t-sm muted", text); }
// anColHead: the header row of a table whose labels AN_COPY writes in full (both skins; the phone's CSS sets them in capitals).
// Columns from firstNum on hold numbers: their header gets "num" like their cells (anTd(text, "num")), so both stylesheets can
// right-align both. `titles` maps a column's index to its tooltip.
function anColHead(cols, firstNum, titles) {
  const tr = el("tr");
  cols.forEach((c, i) => {
    const th = el("th", i >= firstNum ? "num" : null, c);
    if (titles && titles[i]) th.setAttribute("title", titles[i]);
    tr.appendChild(th);
  });
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
function anLegend(parent, models, colorModels) { anSwatchLegend(parent, anLegendItems(models, colorModels)); }
// anSwatchLegend: the phone's key, a swatch and a name per item ({name, color}).
function anSwatchLegend(parent, items) {
  const lg = el("div", "an-legend");
  for (const it of items) { const s = el("span"); const i = el("i"); i.style.background = it.color; s.append(i, el("span", null, it.name)); lg.appendChild(s); }
  parent.appendChild(lg);
}
// anPLegend (the page): the cost chart's legend for a card head (§A2, §D1): an 8px square swatch and the model id in mono, per
// model. The same entries as anLegend's.
function anPLegend(models, colorModels) {
  const lg = el("div", "an-plegend");
  for (const it of anLegendItems(models, colorModels)) {
    const s = el("span"), sw = el("i", "an-sw");
    sw.style.background = it.color;
    s.append(sw, el("span", "mono t-xs fg", it.name));
    lg.appendChild(s);
  }
  return lg;
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
// anHrow (the page): a row of the Overview's cost cards (§B7): the label, the track (the kit's .meter) holding one fill per segment,
// and the total as dollars. A segment is {v, c, t?}: its cost, its colour, its tooltip; a fill's width and offset are shares of
// `max`, so the second segment starts where the first ends, and a segment with no cost draws no fill. `mono` is a machine id (a
// model or a project); the other label is words, so sans and muted along with its value. `wide` is the project card's 184px label.
// `pct` (text) adds the row's share of the window as a fourth, muted column.
function anHrow(parent, name, mono, total, max, segs, wide, pct) {
  const row = el("div", (wide ? "an-hrow an-wide" : "an-hrow") + (pct ? " an-pct" : ""));
  const nm = el("span", (mono ? "mono t-sm" : "t-sm muted") + " ellipsis", name);
  nm.title = name;
  const meter = el("span", "meter");
  let from = 0;
  for (const s of segs) {
    if (!(s.v > 0)) continue;
    const f = el("i");
    f.style.left = (from / max * 100) + "%"; f.style.width = (s.v / max * 100) + "%"; f.style.background = s.c;
    if (s.t) f.title = s.t;
    meter.appendChild(f);
    from += s.v;
  }
  row.append(nm, meter, el("span", "t-sm tnum an-hv" + (mono ? "" : " muted"), usd(total)));
  if (pct) row.appendChild(el("span", "t-sm tnum muted an-hp", pct));
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
// The page's chart has its own geometry (anPageChart); the phone's is below.
function anStackedTime(agg, from, to, fitW) {
  const lay = stackedLayout(agg.days, from, to);
  const slots = Math.max(1, lay.slots);
  if (anIsPage()) return anPageChart(agg, lay, slots, from, to, fitW);
  const H = 170, pad = 24, W = fitW ? Math.max(slots * 8, fitW) : Math.max(slots * 22, 720);
  const max = Math.max(1, agg.maxCost);
  const slotW = (W - 2 * pad) / slots;
  const bw = Math.min(16, Math.max(2, slotW - 3));
  const svg = anSvg(W, H);
  const ticks = anYTicks(max, true);
  for (let i = 0; i <= 3; i++) {
    const y = pad + (H - 2 * pad) * i / 3;
    svg.appendChild(anLine(pad, y, W - 4, y, "var(--rule)", 0.5));
    svg.appendChild(anText(2, y + 3, ticks[i].text, "start"));
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

// anChartLabel: the page chart's accessible name (§A2): the window and, when any day cost anything, the peak and its day.
function anChartLabel(agg, from, to) {
  let label = "Daily cost stacked by model, " + from + " to " + to;
  const peak = agg.maxCost > 0 ? agg.days.find((d) => d.cost === agg.maxCost) : null;
  if (peak) label += ", peak " + usd(agg.maxCost) + " on " + peak.day.slice(5);
  return label;
}

// anPageChart: the cost chart of the desktop page (§B6, AN_PAGE_CHART). The plot is `plotH` high and starts `gutter` from the left;
// the y labels, the peak and its thirds, right-align at the gutter's `labelBox` edge, centred on their gridline, with no label on the
// baseline; a bar is `bar` wide (narrower in a crowded window) centred in its slot, its segments stacked cost-ascending with 1px
// between them, 1px at least (a day's cost is never drawn as nothing, and a segment with no cost is not drawn); the date labels sit
// `xLabelGap` under the plot, on every round(slots/12)-th slot of the window (a day with no cost too), never so close that two touch
// and never past the svg's edge. The scale is the raw peak: a window with no cost has no y labels, not a "$0.00" (the phone floors it
// at 1). The svg overflows visibly and keeps TOP px above the plot for the top label, which is centred on the plot's first line.
function anPageChart(agg, lay, slots, from, to, fitW) {
  const C = AN_PAGE_CHART;
  const TOP = 8, GAP = 1, LABEL_H = 16, DATE_W = 44, LABEL_W = 34; // LABEL_W: an upper estimate of an 11px "MM-DD" (about 30px)
  const W = fitW ? Math.max(slots * 8 + C.gutter, fitW) : Math.max(slots * 22, 720);
  const H = TOP + C.plotH + C.xLabelGap + LABEL_H;
  const slotW = (W - C.gutter) / slots;
  const bw = Math.min(C.bar, Math.max(2, slotW - 3));
  const max = agg.maxCost;
  const bottom = TOP + C.plotH;
  const svg = anSvg(W, H);
  svg.setAttribute("overflow", "visible");
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", anChartLabel(agg, from, to));
  // A 1px line fills the pixel row from y, so it is drawn through y + 0.5; a label's baseline is 4px under its centre (an 11px digit
  // is 8px tall).
  const grid = (y) => svg.appendChild(anLine(C.gutter, y + 0.5, W, y + 0.5, "var(--line)", 1));
  for (const t of anYTicks(max, false)) {
    const y = TOP + Math.round(C.plotH * (1 - t.frac));
    grid(y);
    svg.appendChild(anText(C.labelBox, y + 4, t.text, "end"));
  }
  grid(bottom);
  const labelEvery = Math.max(1, Math.round(slots / 12), Math.ceil(DATE_W / slotW));
  for (const b of lay.bars) {
    const x = C.gutter + b.slot * slotW + (slotW - bw) / 2;
    let yb = bottom;
    const segs = Object.entries(b.segs).filter(([, c]) => c > 0).sort((a, c) => a[1] - c[1]);
    segs.forEach(([m, c], k) => {
      const h = Math.max(1, Math.round(c / max * C.plotH));
      yb -= (k ? GAP : 0) + h;
      const r = anRect(x, yb, bw, h, modelColor(agg.models, m));
      r.appendChild(anTitle(b.day + " · " + shortModel(m) + " " + usd(c)));
      svg.appendChild(r);
    });
  }
  // The date labels sit on the window's slots (anXLabels), not only the ones with a bar. A label centred on the last slot would
  // pass the svg's right edge and be clipped, so it ends at the edge instead; one that would then touch its neighbour is left out.
  let prevRight = -Infinity;
  for (const l of anXLabels(lay, from, labelEvery)) {
    const cx = C.gutter + (l.slot + 0.5) * slotW;
    const atEdge = cx + LABEL_W / 2 > W;
    const left = atEdge ? W - LABEL_W : cx - LABEL_W / 2;
    if (left < prevRight + 2) continue;
    prevRight = left + LABEL_W;
    svg.appendChild(anText(atEdge ? W : cx, bottom + C.xLabelGap + 12, l.text, atEdge ? "end" : "middle"));
  }
  const wrap = anWrapSvg(svg);
  wrap.classList.add("an-timewrap"); // marked so anRender scrolls it to the latest day
  return wrap;
}
