"use strict";

// analytics.js renders the token/prompt Analytics view (fed by /api/analytics/*).
// The pure transforms at the top are unit-tested (node --test); the render below
// builds DOM with textContent only and depends on the global api() from app.js.

// fmtCompact: 0..999 as-is, thousands as "Nk", millions as "N.NM". Mirrors the
// terminal usage readout so the two never disagree on how a number looks.
function fmtCompact(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 1_000_000) return Math.round(n / 1000) + "k";
  return (n / 1_000_000).toFixed(1) + "M";
}

// tokensPerPrompt: total tokens divided by prompts, or 0 when there are none (so
// a prompt-free day never shows Infinity/NaN).
function tokensPerPrompt(totalTokens, prompts) {
  return prompts > 0 ? Math.round(totalTokens / prompts) : 0;
}

// dayTotals folds per-(day,model) rows into per-day stacks plus window totals.
// Returned shapes are plain arrays/objects (no Maps) so they assert cleanly.
function dayTotals(rows) {
  const byDay = new Map();
  const models = [];
  const totals = { input: 0, output: 0, cacheRead: 0, cacheCreation: 0, prompts: 0, tokens: 0 };
  for (const r of rows || []) {
    const t = (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    totals.input += r.input || 0;
    totals.output += r.output || 0;
    totals.cacheRead += r.cacheRead || 0;
    totals.cacheCreation += r.cacheCreation || 0;
    totals.prompts += r.prompts || 0;
    totals.tokens += t;
    if (!models.includes(r.model)) models.push(r.model);
    let d = byDay.get(r.day);
    if (!d) { d = { day: r.day, total: 0, segs: {} }; byDay.set(r.day, d); }
    d.total += t;
    d.segs[r.model] = (d.segs[r.model] || 0) + t;
  }
  const days = [...byDay.values()].sort((a, b) => (a.day < b.day ? -1 : 1));
  const maxTotal = days.reduce((m, d) => Math.max(m, d.total), 0);
  return { days, models, maxTotal, totals };
}

// modelColor assigns a stable colour per model by its index in the model list.
const AN_COLORS = ["#6ea8fe", "#7ee787", "#f0883e", "#d2a8ff", "#ff7b72", "#79c0ff", "#e3b341"];
function modelColor(models, model) {
  const i = models.indexOf(model);
  return AN_COLORS[(i < 0 ? 0 : i) % AN_COLORS.length];
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { fmtCompact, tokensPerPrompt, dayTotals, modelColor };
}

// ---- rendering (browser only) -----------------------------------------------

// openAnalytics opens the dialog and (re)renders it from the API.
async function openAnalytics() {
  if (typeof openDialog === "function") openDialog("analytics-modal");
  await renderAnalytics();
}

async function renderAnalytics() {
  const body = document.getElementById("analytics-body");
  if (!body) return;
  body.replaceChildren(anEmpty("Loading…"));
  let summary = null, sessions = null, tools = null;
  try {
    [summary, tools, sessions] = await Promise.all([
      api("/api/analytics/summary"),
      api("/api/analytics/tools"),
      api("/api/analytics/sessions?limit=200"),
    ]);
  } catch (_) {
    body.replaceChildren(anEmpty("Could not load analytics."));
    return;
  }
  const range = document.getElementById("analytics-range");
  if (range && summary) range.textContent = `${summary.from} → ${summary.to} (UTC)`;

  const agg = dayTotals(summary && summary.byDayModel);
  body.replaceChildren();
  if (!agg.days.length) {
    body.appendChild(anEmpty("No usage recorded yet in this window. Try Re-scan."));
    return;
  }
  body.appendChild(anTotals(agg.totals));
  body.appendChild(anChart(agg));
  body.appendChild(anTools(tools && tools.tools));
  body.appendChild(anSessions(sessions && sessions.sessions));
}

function anEmpty(text) {
  const p = document.createElement("p");
  p.className = "an-empty";
  p.textContent = text;
  return p;
}

function anSection(title) {
  const s = document.createElement("div");
  s.className = "an-section";
  const h = document.createElement("h3");
  h.textContent = title;
  s.appendChild(h);
  return s;
}

function anTotals(t) {
  const sec = anSection("Totals (window)");
  const row = document.createElement("div");
  row.className = "an-totals";
  // output/prompt is the efficiency signal, not all-tokens/prompt: cache_read
  // (context re-read every turn) dwarfs everything, so total/prompt mostly tracks
  // context size, whereas output/prompt tracks what the model actually produced.
  const items = [
    ["input", t.input], ["output", t.output], ["cache read", t.cacheRead],
    ["cache write", t.cacheCreation], ["all tokens", t.tokens],
    ["prompts", t.prompts], ["output / prompt", tokensPerPrompt(t.output, t.prompts)],
  ];
  for (const [label, val] of items) {
    const cell = document.createElement("div");
    cell.className = "an-tot";
    const v = document.createElement("span"); v.className = "v"; v.textContent = fmtCompact(val);
    const l = document.createElement("span"); l.className = "l"; l.textContent = label;
    cell.append(v, l);
    row.appendChild(cell);
  }
  sec.appendChild(row);
  return sec;
}

function anChart(agg) {
  const sec = anSection("Tokens per day (stacked by model)");
  const bars = document.createElement("div");
  bars.className = "an-bars";
  for (const d of agg.days) {
    const bar = document.createElement("div");
    bar.className = "an-bar";
    bar.title = `${d.day}: ${fmtCompact(d.total)} tokens`;
    // Segments, tallest models first, sized as a share of the max day total.
    const segs = Object.entries(d.segs).sort((a, b) => b[1] - a[1]);
    for (const [model, tok] of segs) {
      const seg = document.createElement("div");
      seg.className = "seg";
      const pct = agg.maxTotal ? (tok / agg.maxTotal) * 100 : 0;
      seg.style.height = pct.toFixed(2) + "%";
      seg.style.background = modelColor(agg.models, model);
      bar.appendChild(seg);
    }
    const cap = document.createElement("div");
    cap.className = "cap";
    cap.textContent = d.day.slice(5); // MM-DD
    bar.appendChild(cap);
    bars.appendChild(bar);
  }
  sec.appendChild(bars);
  // Legend.
  const legend = document.createElement("div");
  legend.className = "an-legend";
  for (const m of agg.models) {
    const span = document.createElement("span");
    const i = document.createElement("i"); i.style.background = modelColor(agg.models, m);
    const t = document.createElement("span"); t.textContent = m || "(unknown)";
    span.append(i, t);
    legend.appendChild(span);
  }
  sec.appendChild(legend);
  return sec;
}

function anTools(rows) {
  const sec = anSection("Skills · plugins · MCP (by records)");
  if (!rows || !rows.length) {
    sec.appendChild(anEmpty("No skill/plugin/MCP attribution in this window (recorded only on recent transcripts)."));
    return sec;
  }
  const table = document.createElement("table");
  table.className = "an-table";
  table.appendChild(anHead(["name", "kind", "records"]));
  for (const r of rows) {
    const tr = document.createElement("tr");
    tr.append(anTd(r.name, "mono"), anTd(r.kind), anTd(fmtCompact(r.records)));
    table.appendChild(tr);
  }
  sec.appendChild(table);
  return sec;
}

function anSessions(rows) {
  const sec = anSection("Sessions (tokens · prompts · tokens/prompt)");
  if (!rows || !rows.length) {
    sec.appendChild(anEmpty("No sessions in this window."));
    return sec;
  }
  const table = document.createElement("table");
  table.className = "an-table";
  table.appendChild(anHead(["day", "project", "model", "tokens", "prompts", "out/prompt"]));
  for (const r of rows) {
    const tr = document.createElement("tr");
    const total = (r.input || 0) + (r.output || 0) + (r.cacheRead || 0) + (r.cacheCreation || 0);
    tr.append(
      anTd(r.day),
      anTd(projShort(r.project), "mono"),
      anTd(r.model || "(unknown)", "mono"),
      anTd(fmtCompact(total)),
      anTd(String(r.prompts || 0)),
      anTd(fmtCompact(tokensPerPrompt(r.output, r.prompts))),
    );
    table.appendChild(tr);
  }
  sec.appendChild(table);
  return sec;
}

// projShort trims the leading "-home-<user>-" a Claude Code slug carries, so the
// project column shows the recognisable tail (e.g. "culiops-agent-box").
function projShort(p) {
  if (!p) return "";
  return p.replace(/^-home-[^-]+-/, "").replace(/^-/, "");
}

function anHead(cols) {
  const tr = document.createElement("tr");
  for (const c of cols) {
    const th = document.createElement("th");
    th.textContent = c;
    tr.appendChild(th);
  }
  return tr;
}

function anTd(text, cls) {
  const td = document.createElement("td");
  if (cls) td.className = cls;
  td.textContent = text;
  return td;
}
