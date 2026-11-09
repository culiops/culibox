"use strict";

// The Activity view's pure model (spec 5.8): a live, newest-first timeline of one
// session's tool calls, your messages and its credential and approval rows, with
// the summary tiles and the files changed. Everything the view draws is a field
// GET /api/sessions/{id}/activity really returned; a field it did not send is
// absent here, never a guess. DOM wiring lives in activitytab.js.
//
// Inputs, all optional so a half-loaded view still renders:
//   session  the list entry (Info): claude, need, ask, ask_full
//   resp     the activity reply, its `events` being every page held (newest first)
//   filter   one of ACTIVITY_FILTERS
//   nowMs    the clock, passed in so the model is a pure function
//   failed   the last read of the reply failed (activitytab.js's load state); only
//            read while no reply has arrived, to say so instead of "no events yet"
//
// Classic script: nothing here touches the DOM or the network, and every helper
// tolerates a field of the wrong type (the reply is the daemon's, but a box agent
// writes the transcript it is parsed from).

const ACTIVITY_FILTERS = ["all", "commands", "files", "cloud", "you"];

const ACT_FILE_TOOLS = new Set(["Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob"]);
const ACT_MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

const actStr = (v) => (typeof v === "string" ? v : "");
const actNum = (v) => (typeof v === "number" && Number.isFinite(v) ? v : null);
const actPad = (n) => String(n).padStart(2, "0");

// filterKind: which filter pill an event belongs to. A tool that is neither a
// command nor a file (WebFetch, an MCP tool, Task) is "other": only All shows it.
function filterKind(e) {
  if (!e || typeof e !== "object") return "other";
  switch (e.kind) {
    case "credential": case "approval": return "cloud";
    case "you": case "clear": return "you";
    case "tool":
      if (e.tool === "Bash") return "commands";
      return ACT_FILE_TOOLS.has(e.tool) ? "files" : "other";
    default: return "other";
  }
}

// actDur: a tool's run time. Sub-second times keep a decimal so 200ms reads 0.2s.
function actDur(ms) {
  if (ms < 100) return ms + "ms";
  if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
  const s = Math.round(ms / 1000);
  return Math.floor(s / 60) + "m " + (s % 60) + "s";
}

// actAge: how long something has been going, from whole seconds (duration.js).
function actAge(sec) {
  return (typeof fmtDuration === "function" ? fmtDuration : require("./duration.js").fmtDuration)(sec);
}

// actDelta: "+a −d". The minus is U+2212, as in the mockup.
function actDelta(add, del) {
  return "+" + (actNum(add) || 0) + " −" + (actNum(del) || 0);
}

// outcomeText: the right-aligned cell of a row. `ask` is not an API kind: it is
// the current pending ask the model prepends, whose ts is when it started waiting.
function outcomeText(e, nowMs) {
  if (!e || typeof e !== "object") return "";
  switch (e.kind) {
    case "ask": {
      const ts = actNum(e.ts);
      const now = actNum(nowMs);
      return ts && now !== null && now > ts ? "waiting " + actAge((now - ts) / 1000) : "waiting";
    }
    case "you": return "message";
    case "clear": return "";
    case "credential": case "approval": return actStr(e.state);
    case "tool": {
      const st = actStr(e.state);
      if (st === "error") return actNum(e.exit) !== null ? "error · exit " + e.exit : "error";
      if (st === "pending") return "running";
      if (actNum(e.add) !== null || actNum(e.del) !== null) return actDelta(e.add, e.del);
      const lines = actNum(e.lines);
      if (lines !== null) return lines + (lines === 1 ? " line" : " lines");
      const ms = actNum(e.ms);
      if (st === "ok") return ms ? "ok · " + actDur(ms) : "ok";
      return st;
    }
    default: return actStr(e.state);
  }
}

// actTone: the colour role of an outcome (D23). Amber is the ask only; red is an
// error (a tool error, a failed credential) or a write window that opened or was
// extended; green is a success; tone "add" (a diff) is neutral text in the view.
// A refusal (a credential or an approval) is containment working: neutral, like
// everything else.
function actTone(e) {
  if (!e || typeof e !== "object") return "muted";
  const st = actStr(e.state);
  switch (e.kind) {
    case "ask": return "amber";
    case "credential": return st === "minted" ? "ok" : st === "failed" ? "err" : "muted";
    case "approval": return st === "granted" || st === "extended" ? "err" : "muted";
    case "tool":
      if (st === "error") return "err";
      if (st === "pending") return "muted";
      if (actNum(e.add) !== null || actNum(e.del) !== null) return "add";
      if (actNum(e.lines) !== null) return "muted";
      return st === "ok" ? "ok" : "muted";
    default: return "muted";
  }
}

// actTime: the clock for today, the date and clock for an earlier day.
function actTime(ts, nowMs) {
  const t = actNum(ts);
  if (t === null) return "";
  const d = new Date(t);
  const n = new Date(actNum(nowMs) === null ? Date.now() : nowMs);
  const sameDay = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
  const clock = actPad(d.getHours()) + ":" + actPad(d.getMinutes());
  return sameDay ? clock + ":" + actPad(d.getSeconds()) : d.getDate() + " " + ACT_MONTHS[d.getMonth()] + " " + clock;
}

// actRowKey: an event's identity across polls and repaints (which rows are
// expanded). The state is left out: a running call becomes ok or error in place.
function actRowKey(e) {
  const x = e && typeof e === "object" ? e : {};
  return [actNum(x.ts) === null ? "" : x.ts, actStr(x.kind), actStr(x.tool), actStr(x.detail)].join("\u0001");
}

function actKindLabel(e) {
  switch (e.kind) {
    case "tool": return actStr(e.tool) || "Tool";
    case "you": return "You";
    case "clear": return "Cleared";
    case "credential": return "Credential";
    case "approval": return "Approval";
    default: return "";
  }
}

function actRow(e, nowMs) {
  const detail = actStr(e.detail) || (e.kind === "clear" ? "conversation cleared" : "");
  return {
    key: actRowKey(e),
    ts: actNum(e.ts),
    time: actTime(e.ts, nowMs),
    kind: actKindLabel(e),
    detail,
    outcome: { text: outcomeText(e, nowMs), tone: actTone(e) },
    cls: e.kind === "you" || e.kind === "clear" ? "you" : "",
  };
}

// actRunning: the "Session age" tile: 35s / 41m / 1h 12m / 11d 13h. The server's
// running_s is whole seconds since the session was created, the same instant
// Access and the steer rail count from (startedText).
function actRunning(sec) {
  return actAge(actNum(sec) || 0);
}

// ACT_DASH: what a figure reads before the first reply. U+2014, never a fake 0.
const ACT_DASH = "—";

// actEmpty: the plain block a session with no transcript gets in place of tiles
// (M6.2). The reply of a non-Claude session only ever carries credential and
// approval rows, so "no credential events" is a real finding only once it is read.
//   name  s.name, else the id; the agent clause drops when the profile is unnamed
//   meta  Session age, then how many credential events the rows hold
function actEmpty(s, r, sum, events, failed) {
  const name = actStr(s.name) || actStr(s.id) || "This session";
  const agent = actStr(s.agent);
  const tail = " If this session uses a credential, the event still appears here.";
  const age = "Session age " + (r ? actRunning(sum.running_s) : ACT_DASH);
  let meta = age;
  if (r) {
    const n = events.filter((e) => e.kind === "credential").length;
    meta += " · " + (n === 0 ? "no credential events yet" : n === 1 ? "1 credential event" : n + " credential events");
  } else if (failed) {
    meta += " · could not read credential events";
  }
  return {
    title: "Activity is for Claude sessions",
    body: (agent ? name + " runs " + agent + ", so there is no transcript to show." : name + " has no transcript to show.") + tail,
    meta,
  };
}

// actFiles: the files changed, with their +a −d text.
function actFiles(files) {
  return (Array.isArray(files) ? files : [])
    .filter((f) => f && typeof f === "object" && typeof f.path === "string")
    .map((f) => ({ path: f.path, add: actNum(f.add) || 0, del: actNum(f.del) || 0, text: actDelta(f.add, f.del) }));
}

// activityModel: everything the view draws.
function activityModel(inp) {
  const i = inp || {};
  const s = i.session && typeof i.session === "object" ? i.session : null;
  const r = i.resp && typeof i.resp === "object" ? i.resp : null;
  const nowMs = actNum(i.nowMs) === null ? Date.now() : i.nowMs;
  const filter = ACTIVITY_FILTERS.includes(i.filter) ? i.filter : "all";
  // The list entry says what the session is; the reply only says what was found.
  const claude = !!s && s.claude === true;
  const sum = r && r.summary && typeof r.summary === "object" ? r.summary : {};
  const events = (r && Array.isArray(r.events) ? r.events : []).filter((e) => e && typeof e === "object");

  const asking = claude && s.need === "ask";
  // The pending tool call an ask stands for is the newest pending one: the agent
  // is blocked on its permission prompt. The ask row replaces it.
  const dupe = asking ? events.findIndex((e) => e.kind === "tool" && e.state === "pending") : -1;

  const rows = [];
  if (asking) {
    const since = actNum(r && r.waiting_since);
    const ask = { kind: "ask", ts: since && since > 0 ? since : 0 };
    rows.push({
      key: "ask",
      ts: ask.ts || null,
      time: ask.ts ? actTime(ask.ts, nowMs) : "",
      kind: "Asks to run",
      detail: actStr(s.ask_full) || actStr(s.ask) || "Waiting on a prompt in the terminal.",
      outcome: { text: outcomeText(ask, nowMs), tone: "amber" },
      cls: "ask",
    });
  }
  events.forEach((e, idx) => {
    if (idx === dupe) return;
    if (!claude && e.kind !== "credential" && e.kind !== "approval") return; // no transcript to describe
    const fk = filterKind(e);
    if (filter !== "all" && fk !== filter) return;
    rows.push(actRow(e, nowMs));
  });

  // The kit's five tiles, from the reply's summary and dashes until it arrives. A
  // session with no transcript has none: it gets the one plain block instead.
  const dash = !r;
  const tiles = claude
    ? [
      { n: dash ? ACT_DASH : actNum(sum.tool_calls) || 0, label: "Tool calls" },
      { n: dash ? ACT_DASH : actNum(sum.files_changed) || 0, label: "Files changed" },
      { n: dash ? ACT_DASH : actNum(sum.credentials) || 0, label: "Credentials" },
      { n: dash ? ACT_DASH : s.need ? 1 : 0, label: "Waiting for you" },
      { n: dash ? ACT_DASH : actRunning(sum.running_s), label: "Session age" },
    ]
    : [];

  return {
    tiles,
    rows,
    files: claude ? actFiles(r && r.files) : [],
    notClaude: !claude,
    empty: s && !claude ? actEmpty(s, r, sum, events, i.failed === true) : null,
    loaded: !!r,
    partial: !!(r && r.partial),
    more: !!(r && r.more),
    ask: asking,
  };
}

// actContext: the context bar's line. pct is the server's verdict when present
// (it omits it when the window is unknown), else used / max; null when neither.
function actContext(u) {
  if (!u || typeof u !== "object") return null;
  const used = actNum(u.contextTokens) || 0;
  const max = actNum(u.contextMax) || 0;
  let pct = actNum(u.pct);
  if (pct === null && max > 0 && used > 0) pct = Math.round((used / max) * 100);
  if (pct === null) return null;
  pct = Math.max(0, Math.min(100, pct));
  let of = "";
  if (max >= 1e6) of = " of " + String(Number((max / 1e6).toFixed(1))) + "M";
  else if (max >= 1000) of = " of " + Math.round(max / 1000) + "k";
  return { pct, width: pct + "%", text: "context " + pct + "%" + of };
}

// ACT_TOOL_ROWS: how many tools the Context by tool list shows.
const ACT_TOOL_ROWS = 8;

// actToolRows: GET .../attribution as bar rows, biggest first (the server sorts).
// pct is a tool's share of the result bytes summed over every tool, so the bars
// compare tools with each other; the number a bar stands for is bytes, a proxy
// for context pressure (attribution.js), and the title says so.
function actToolRows(attr) {
  const tools = attr && Array.isArray(attr.tools) ? attr.tools.filter((t) => t && typeof t === "object") : [];
  const bytes = (t) => Math.max(0, actNum(t.ctxBytes) || 0);
  const total = tools.reduce((n, t) => n + bytes(t), 0);
  const fmt = typeof fmtCtx === "function" ? fmtCtx : require("./attribution.js").fmtCtx;
  const rows = tools.slice(0, ACT_TOOL_ROWS).map((t) => ({
    name: actStr(t.name) || "?",
    kind: t.kind === "mcp" || t.kind === "skill" ? t.kind : "tool",
    calls: actNum(t.calls) || 0,
    ctx: fmt(bytes(t)),
    pct: total > 0 ? Math.round((bytes(t) / total) * 100) : 0,
  }));
  return { rows, more: Math.max(0, tools.length - rows.length) };
}

// --- paging -----------------------------------------------------------------------

// actOldest: the ts a "Load older" asks below: the oldest event held, whichever
// list it is in. 0 when nothing is held.
function actOldest(live, older) {
  const last = (a) => (Array.isArray(a) && a.length ? actNum(a[a.length - 1].ts) : null);
  const ts = [last(live), last(older)].filter((t) => t !== null);
  return ts.length ? Math.min(...ts) : 0;
}

// actSlide: the live page is the newest page, refetched every poll, so new events
// push the oldest ones off it. Once the operator has loaded older pages those
// events must not fall into a gap between the two: they join the older list. Strictly
// older than the new page's oldest ts only (the server completes that ms group, so
// nothing is held twice). Before any older page was loaded nothing accumulates, so
// an idle view stays one page.
function actSlide(prev, next, older, pinned) {
  const o = Array.isArray(older) ? older : [];
  if (!pinned || !Array.isArray(prev) || !prev.length || !Array.isArray(next) || !next.length) return o;
  const floor = actNum(next[next.length - 1].ts);
  if (floor === null) return o;
  return prev.filter((e) => e && actNum(e.ts) !== null && e.ts < floor).concat(o);
}

// --- the load path is total ---------------------------------------------------------

// actReadReply: one activity reply as data. A 200 whose body is an object with an
// events list is that object; anything else (403 for a token, 404, 5xx, a 204, a
// body that is not JSON) is undefined, "could not read". Never throws.
async function actReadReply(res) {
  try {
    if (!res || res.status !== 200) return undefined;
    const j = await res.json();
    return j && typeof j === "object" && Array.isArray(j.events) ? j : undefined;
  } catch (_) { return undefined; }
}

// actLoadDone: the load state after one read finished. `reply` is a parsed reply
// or undefined; a failed read keeps what was there, and the read is over either
// way (inflight false), so a bad reply can never block the next one.
function actLoadDone(prev, reply, nowMs) {
  const p = prev || {};
  const ok = reply !== undefined && reply !== null;
  return {
    resp: ok ? reply : p.resp === undefined ? null : p.resp,
    at: nowMs, inflight: false,
    loaded: ok || !!p.loaded,
    failed: !ok,
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    activityModel, ACTIVITY_FILTERS, filterKind, outcomeText, actTime, actReadReply, actLoadDone,
    actSlide, actOldest, actContext, actDelta, actRowKey, actToolRows, actAge,
  };
}
