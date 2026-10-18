"use strict";

// The two rails' pure layer, kept out of app.js so `node --test` can reach it.
//
// This half is the icon rail: the column the sidebar becomes when it is hidden.
// It is a list of items, not markup, because the only decisions worth testing
// are which items exist, in what order, and what each one is called. Every item
// carries a label: the design gives a name to the toggle alone, and a status
// square with no accessible name is a control a screen reader announces as
// "button" and nothing more.

function railNeedsYou(s) {
  return (typeof needsYou === "function" ? needsYou : require("./attention.js").needsYou)(s);
}

function railItems(sessions, ctx) {
  const c = ctx || {};
  const out = [{ kind: "toggle", label: "Show sidebar", glyph: "☰", on: false, alert: false }];
  for (const s of sessions || []) {
    // needsYou, not activity alone: an expired AWS sign-in wants the operator
    // while the agent still reads "working".
    const waiting = railNeedsYou(s);
    const name = (s && (s.name || s.id)) || "";
    out.push({
      kind: "session",
      id: s && s.id,
      // name is the session's own name, undecorated: it is what a newly-opened
      // tab gets titled, and a tab title is permanent until the operator renames
      // it by hand — nothing re-syncs it from a later poll. label carries the
      // "— waiting for you" suffix for the button's accessible name and tooltip
      // only; it must never reach a tab title.
      name: name,
      label: waiting ? name + " — waiting for you" : name,
      activity: (s && s.activity) || "",
      on: !!c.focusedId && !!s && s.id === c.focusedId,
      alert: !!waiting,
    });
  }
  out.push({ kind: "new", label: "New session", glyph: "+", on: false, alert: false });
  return out;
}

// The steer rail's own half: what one session's panel says about it.
//
// The hard rule here is that every row comes from a field the daemon really
// returns. There is no cost row because no session endpoint carries a cost;
// there is no history row because `activity` is one recomputed string with no
// transitions behind it; there is no live tool feed because nothing streams
// one. A rail that shows a plausible number the API never sent is worse than a
// rail with four rows, because the four rows can be trusted.

function startedText(created, nowMs) {
  if (!created) return "";
  const t = Date.parse(created);
  if (!Number.isFinite(t)) return "";
  // The clock face reads in the viewer's own local time, same as every other
  // timestamp in the app; the relative half below is what makes it actionable
  // without doing timezone math in your head.
  const dt = new Date(t);
  const clock = String(dt.getHours()).padStart(2, "0") + ":" + String(dt.getMinutes()).padStart(2, "0");
  // A clock a few seconds behind the daemon must read "just now", never
  // "-1m ago": the skew is ours, and the reader cannot act on it either way.
  let s = Math.floor(((nowMs || 0) - t) / 1000);
  if (s < 60) return clock + " · just now";
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d) return clock + " · " + d + "d " + h + "h ago";
  if (h) return clock + " · " + h + "h " + m + "m ago";
  return clock + " · " + m + "m ago";
}

// railMeta: the header and the four-row block. cwdText is passed in already
// shortened, the way sessionMeta takes its own strings, so this file needs no
// opinion about what home is — and `badge` is tierBadge()'s verdict, passed in
// for the same reason: the containment tier is a fail-loud SECURITY signal with
// exactly one owner (tier.js), and a second derivation here is how the sidebar
// and this panel would come to disagree about whether a session is contained.
// Passed as an argument rather than read as a global so this file stays pure
// and node --test can still require it.
function railMeta(s, usage, cwdText, nowMs, badge, extraRows) {
  if (!s) return null;
  const u = usage || {};
  const rows = [];
  if (u.model) rows.push({ k: "model", v: String(u.model) });
  if (u.effort) rows.push({ k: "effort", v: String(u.effort) });
  if (cwdText) rows.push({ k: "cwd", v: String(cwdText) });
  const st = startedText(s.created, nowMs);
  if (st) rows.push({ k: "started", v: st });
  // Rows the caller derived from state this file has no business reading (the
  // AWS binding and its broker counters). Null entries are skipped.
  for (const r of extraRows || []) if (r) rows.push(r);
  // tierBadge already decided the wording and the severity; this only dresses
  // it as the same flat .bdg.box / .bdg.host chip the sidebar draws for the
  // identical session — a danger tone takes the host tier's warn colour (per
  // the canvas — the host tier is NOT --danger pink), anything else takes the
  // box tier's ok colour. An unknown or absent runtime therefore reads as host
  // here exactly as it does in the sidebar, never as "no tier at all" — an
  // absent badge on the surface you steer from reads as "nothing to worry
  // about", which is the inversion this must not make.
  const tier = badge
    ? { label: badge.text, cls: "bdg " + (badge.tone === "danger" ? "host" : "box"), title: badge.title || "" }
    : null;
  return { id: s.id, name: s.name || s.id, activity: s.activity || "", tier, rows };
}

// awsStatsRows: the broker's counters for the focused session, from
// GET /api/sessions/{id}/aws. last_mint is the zero time before the first mint
// (Go's time.Time), which must read "never", not year 1. last_error is shown
// verbatim in a wrapping row of its own: it carries the actionable
// `aws sso login` hint, which an ellipsis would cut off.
function awsStatsRows(st, nowMs) {
  const x = st && st.stats;
  if (!x) return [];
  const n = Number(x.mints) || 0;
  const t = Date.parse(x.last_mint);
  const last = Number.isFinite(t) && t > Date.UTC(2000, 0, 1) ? startedText(x.last_mint, nowMs) : "never";
  const rows = [{ k: "minted", v: n + (n === 1 ? " mint" : " mints") + " · last mint " + last }];
  if (x.last_error) rows.push({ k: "error", v: String(x.last_error), wrap: true });
  return rows;
}

// egressRow: the egress proxy's line for the focused session, from the
// GET /api/sessions/{id}/aws response (AWS-bound) or /egress (unbound box); both
// carry the same {egress:{mode,denied}} object. The server omits `egress` when the session has no
// egress at all, so absent means no row, not "0 denied". denied is coerced so a
// missing or garbled count reads 0 rather than NaN. The count is the daemon's
// in-memory tally, so it says "since start": after a restart it restarts at 0
// while egress_audit keeps the history.
function egressRow(st) {
  const e = st && st.egress;
  if (!e) return null;
  const mode = e.mode === "open" ? "open" : "allowlist";
  return { k: "egress", v: mode + " \u00b7 " + (Number(e.denied) || 0) + " denied since start", wrap: true };
}

// ctxFill: the context meter. The server omits pct when the window maximum is
// unknown or already exceeded, so a present pct is its verdict and outranks a
// local division; with neither, the bar is not drawn at all rather than drawn
// empty, which would read as "0% used".
function ctxFill(u) {
  if (!u) return { pct: null, width: "0%", used: 0, max: 0 };
  const used = Number(u.contextTokens) || 0;
  const max = Number(u.contextMax) || 0;
  let pct = null;
  if (typeof u.pct === "number") pct = u.pct;
  else if (max > 0 && used > 0) pct = Math.round((used / max) * 100);
  if (pct !== null) pct = Math.max(0, Math.min(100, pct));
  return { pct, width: (pct === null ? 0 : pct) + "%", used, max };
}

// --- DOM ---------------------------------------------------------------------

const STEER_KEY = "agent-box:steerRail";

// The rail describes the session in the focused TERMINAL tab. A focused file
// viewer is not a session and deliberately leaves the rail on its empty state
// rather than showing whichever agent happens to be nearby.
function railSession() {
  const id = typeof selectedSessionId === "function" ? selectedSessionId() : null;
  if (!id) return null;
  return (lastSessions || []).find((s) => s.id === id) || null;
}

function steerOpen() {
  const el = document.getElementById("steer-rail");
  return !!el && !el.hidden;
}

function setSteerOpen(open) {
  const el = document.getElementById("steer-rail");
  if (!el) return;
  el.hidden = !open;
  const t = document.getElementById("steer-toggle");
  if (t) t.setAttribute("aria-pressed", open ? "true" : "false");
  try { localStorage.setItem(STEER_KEY, open ? "1" : "0"); } catch (_) {}
  // The docked rail takes width from the grid; the overlay does not. Refit
  // either way — measuring which case we are in costs more than one refit.
  if (typeof renderGrid === "function") renderGrid();
  if (open) renderSteerRail();
}

function toggleSteerRail() { setSteerOpen(!steerOpen()); }

// openSteerFor is what the two former entry points to the attribution dialog
// now call. It brings that session's tab to the front FIRST, because the rail
// always describes the focused session: opening the rail on one session while
// another is in front is how a reader attributes one agent's tools to another.
// openTerminal activates an already-open tab rather than making a second one —
// a session admits one subscriber, so two tabs would evict each other forever.
function openSteerFor(id) {
  const s = (lastSessions || []).find((x) => x.id === id);
  if (typeof openTerminal === "function") openTerminal(id, (s && (s.name || s.id)) || id);
  setSteerOpen(true);
  scanAttribution(id);
}

function renderSteerRail() {
  // Every focus change lands here, so this is also where the header's quota
  // meters follow the focused session's agent account — before the early
  // return, because the rail being closed does not stop the header following.
  if (typeof syncRateLimitsAccount === "function") syncRateLimitsAccount();
  const rail = document.getElementById("steer-rail");
  if (!rail || rail.hidden) return;
  const s = railSession();
  steerFactsKey = ""; // an explicit rebuild is never suppressed by the poll's signature
  const head = document.getElementById("steer-head");
  const ctx = document.getElementById("steer-ctx");
  const attr = document.getElementById("steer-attr");
  const msg = document.getElementById("steer-msg");
  if (!s) {
    head.replaceChildren(railTitle("Session"));
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = "No session in focus. Open one to steer it.";
    head.appendChild(p);
    ctx.replaceChildren();
    attr.replaceChildren();
    msg.replaceChildren();
    resetElevSection();
    // The composer is gone, so nothing may re-gate it: both of the handles the
    // poll looks for go with it. An expando survives replaceChildren.
    delete msg.dataset.sid;
    msg._refresh = null;
    return;
  }
  // 1 and 2 — who this is, and how full its window is.
  if (s.aws) loadAwsRailStats(s.id);
  else if (s.egress) loadEgressRailStats(s.id);
  renderSteerFacts(s);
  // A half-open grant panel belongs to one session; focusing another drops it.
  if (elevPanelFor !== s.id) elevPanelFor = null;
  renderElevSection(s);

  // 3 — where the context went. On demand: this is a full transcript scan, so
  // a focus change clears the rows rather than carrying one agent's tools over
  // to the next.
  renderAttrSection(s, null);

  // 4 — what it last said, and the answer box.
  renderMsgSection(s);
}

// renderSteerFacts paints the two read-only sections. It is split out of
// renderSteerRail because the POLL has to be able to refresh them: a tab's
// usage is fetched one poll AFTER the tab itself exists, so a rail built at the
// moment a session is opened has no model, no effort and no context bar — and
// without this it would keep none until the next focus change happened to
// rebuild it. Neither section holds a control or a caret, so rebuilding them
// carries none of the hazard that keeps the composer and the tool rows out of
// the poll. A signature stops it churning every three seconds for nothing.
let steerFactsKey = "";

// The broker counters for AWS-bound sessions, fetched when a session takes the
// rail's focus (no new poll) and kept per session id. renderSteerFacts reads
// this cache on every repaint, so the rows are part of the signature and a
// rebuild triggered by anything else does not wipe them; a focus change
// refetches while the old value stays up, and one session's numbers can never
// be drawn for another because the key is the id.
const awsRailCache = new Map();
const awsRailInflight = new Set();
function loadAwsRailStats(id) {
  if (awsRailInflight.has(id)) return;
  awsRailInflight.add(id);
  fetch("/api/sessions/" + encodeURIComponent(id) + "/aws")
    .then((res) => (res.status === 200 ? res.json() : null), () => undefined)
    .then((st) => {
      // undefined = the request failed: keep what we had. null = 204/404: the
      // session has no binding (or the broker is off), so there is nothing to show.
      if (st === undefined) return;
      if (st === null) awsRailCache.delete(id); else awsRailCache.set(id, st);
      const cur = typeof railSession === "function" ? railSession() : null;
      if (cur && cur.id === id) { renderSteerFacts(cur); renderElevSection(cur); }
    })
    .finally(() => awsRailInflight.delete(id));
}

// The egress line for a box WITHOUT an AWS binding: /aws is 204 for it, so it
// reads /egress instead. Same shape as the AWS cache above and its own map, so
// the two can never be drawn for the wrong session.
const egressRailCache = new Map();
const egressRailInflight = new Set();
function loadEgressRailStats(id) {
  if (egressRailInflight.has(id)) return;
  egressRailInflight.add(id);
  fetch("/api/sessions/" + encodeURIComponent(id) + "/egress")
    .then((res) => (res.status === 200 ? res.json() : null), () => undefined)
    .then((st) => {
      if (st === undefined) return;
      if (st === null) egressRailCache.delete(id); else egressRailCache.set(id, st);
      const cur = typeof railSession === "function" ? railSession() : null;
      if (cur && cur.id === id) renderSteerFacts(cur);
    })
    .finally(() => egressRailInflight.delete(id));
}

function renderSteerFacts(s) {
  const head = document.getElementById("steer-head");
  const ctx = document.getElementById("steer-ctx");
  if (!head || !ctx) return;
  const tab = typeof terminalTabFor === "function" ? terminalTabFor(s.id) : null;
  const usage = tab && typeof tab.usage === "function" ? tab.usage() : null;
  const badge = tierBadge(s, boxRuntime);
  const awsRows = s.aws ? [...awsRailRows(s, awsRailCache.get(s.id), Date.now()), ...awsStatsRows(awsRailCache.get(s.id), Date.now()), egressRow(awsRailCache.get(s.id))]
    : s.egress ? [egressRow(egressRailCache.get(s.id))] : null;
  const meta = railMeta(s, usage, s.cwd ? shortPath(s.cwd) : "", Date.now(), badge, awsRows);
  const f = ctxFill(usage);
  const key = [s.id, meta.name, meta.activity,
    meta.tier ? meta.tier.label + ":" + meta.tier.cls + ":" + meta.tier.title : "",
    meta.rows.map((r) => r.k + "=" + r.v).join(","), f.pct, f.used, f.max, s.claude ? 1 : 0,
    (s.aws && s.aws.elevated_until) || "", JSON.stringify(s.aws || null)].join("|");
  if (key === steerFactsKey) return;
  steerFactsKey = key;

  // 1 — who this is.
  const hrow = document.createElement("div");
  hrow.className = "rttl";
  const act = activityClass(s.activity);
  const dot = document.createElement("span");
  dot.className = act.cls;
  // A coloured square with no accessible name is announced as nothing at all,
  // and this one carries whether the agent is working or waiting for you.
  if (act.title) { dot.title = act.title; dot.setAttribute("role", "img"); dot.setAttribute("aria-label", act.title); }
  const nm = document.createElement("span");
  nm.className = "grow";
  nm.style.textTransform = "none"; // .rttl is a small-caps label; a session name is not
  nm.textContent = meta.name;
  nm.title = meta.name;
  hrow.append(dot, nm);
  if (meta.tier) {
    const b = document.createElement("span");
    b.className = meta.tier.cls;
    b.style.textTransform = "none"; // .rttl is a small-caps label; "box"/"host" is not
    b.textContent = meta.tier.label;
    // The sentence that says what the tier actually means, the same one the
    // sidebar's badge carries.
    if (meta.tier.title) b.title = meta.tier.title;
    hrow.appendChild(b);
  }
  head.replaceChildren(hrow, ...meta.rows.map((r) => kvRow(r.k, r.v, r.wrap, r.chip)));

  // 2 — how full the window is. Drawn only when there is a number to draw.
  ctx.replaceChildren(railTitle("Context window"));
  if (f.max > 0 || f.used > 0) {
    ctx.appendChild(kvRow("used", withCommas(f.used) + (f.max ? " / " + withCommas(f.max) : "")));
  }
  if (f.pct !== null) {
    const bar = document.createElement("div");
    bar.className = "ctxbar";
    const i = document.createElement("i");
    i.style.width = f.width;
    bar.appendChild(i);
    bar.setAttribute("role", "img");
    bar.setAttribute("aria-label", "Context window " + f.pct + "% used");
    const cap = document.createElement("p");
    cap.className = "tiny mut";
    cap.textContent = f.pct + "% of the window";
    ctx.append(bar, cap);
  } else {
    const cap = document.createElement("p");
    cap.className = "tiny mut";
    cap.textContent = s.claude ? "No transcript usage reported yet." : "Not a Claude session — no context to report.";
    ctx.appendChild(cap);
  }
}

function railTitle(text) {
  const t = document.createElement("div");
  t.className = "rttl";
  const span = document.createElement("span");
  span.className = "grow";
  span.textContent = text;
  t.appendChild(span);
  return t;
}

function kvRow(k, v, wrap, chip) {
  const row = document.createElement("div");
  row.className = wrap ? "kv wrap" : "kv";
  const a = document.createElement("span");
  a.textContent = k;
  const b = document.createElement("span");
  // chip: the value is a status badge (the red production marker), not muted
  // text — it keeps its own colour and is never clipped.
  b.className = chip || "v";
  b.textContent = v;
  b.title = v;
  row.append(a, b);
  return row;
}

// The tool breakdown. Two things are true and both are stated where they are
// drawn: the number is result-payload BYTES shown as approximate tokens, which
// is a proxy for how much of the window a tool has eaten and not a bill; and it
// is a scan of the whole transcript, run when asked, not a live feed.
function renderAttrSection(s, attr) {
  const el = document.getElementById("steer-attr");
  if (!el) return;
  const title = railTitle("Context by tool");
  // Only a claude session has a transcript to scan. Offering the button on a
  // shell would put a control here whose only possible answer contradicts the
  // section directly above it, which already says there is no context to report.
  if (!s.claude) {
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = "Not a Claude session — no tool breakdown.";
    el.replaceChildren(title, p);
    return;
  }
  const scan = document.createElement("button");
  scan.type = "button";
  scan.className = "btn";
  scan.textContent = "scan";
  scan.onclick = () => scanAttribution(s.id);
  title.appendChild(scan);
  const kids = [title];
  if (attr === "loading") {
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = "Scanning the transcript…";
    kids.push(p);
  } else if (attr) {
    const rows = attributionRows(attr);
    if (!rows.length) {
      const p = document.createElement("p");
      p.className = "tiny mut";
      p.textContent = "No tool activity recorded for this session.";
      kids.push(p);
    } else {
      for (const r of rows) {
        const row = document.createElement("div");
        row.className = "attr";
        const kind = document.createElement("span");
        kind.className = "kind";
        kind.textContent = r.kind === "mcp" ? "mcp" : r.kind === "skill" ? "skill" : "tool";
        const name = document.createElement("span");
        name.className = "an";
        name.textContent = r.name;
        name.title = r.name;
        const calls = document.createElement("span");
        calls.className = "ac";
        calls.textContent = String(r.calls);
        const ctx = document.createElement("span");
        ctx.className = "ax";
        ctx.textContent = r.ctx;
        row.append(kind, name, calls, ctx);
        kids.push(row);
      }
      kids.push(kvRow("output tokens", withCommas(attr.outputTokens || 0)));
    }
  }
  const note = document.createElement("p");
  note.className = "tiny mut";
  note.textContent = attr && attr !== "loading"
    ? "Result-payload bytes — a proxy for context pressure, not billing. Re-scanned on demand."
    : "Result-payload bytes over the whole session — a proxy for context pressure, not billing. Re-scanned on demand.";
  kids.push(note);
  el.replaceChildren(...kids);
}

async function scanAttribution(id) {
  const s = (lastSessions || []).find((x) => x.id === id);
  // Same gate as the button: a non-claude session has no transcript, so the
  // request would be spent to be told what the section already says.
  if (!s || !s.claude) return;
  renderAttrSection(s, "loading");
  let attr = null;
  try { attr = await api("/api/sessions/" + encodeURIComponent(id) + "/attribution"); } catch (_) { attr = null; }
  // A focus change during the scan means these rows belong to a session that is
  // no longer the one on screen. Drop them rather than attributing one agent's
  // tools to another.
  const now = railSession();
  if (!now || now.id !== id) return;
  renderAttrSection(now, attr || { tools: [], outputTokens: 0 });
}

// The composer is the rail's only control that acts on the agent, so it obeys
// the same gate the sidebar's does — sendState is peek.js's, unchanged — and
// the three things move together: the dimmed look, the caption that says why,
// and the real disabled attribute that makes it true.
//
// This is a SECOND instance of the sidebar's composer, not a shared one: that
// one's send path lives inside renderPeek's closure, and hoisting it out would
// refactor a surface this rail does not own. The fetch and the status mapping
// are repeated; everything reachable (sendState, replyTextError, pendingSends,
// showToast) is reused.
function renderMsgSection(s) {
  const el = document.getElementById("steer-msg");
  if (!el) return;
  const kids = [railTitle("Last message")];
  const txt = document.createElement("div");
  txt.className = "peektxt";
  txt.textContent = s.summary || "Nothing said yet.";
  if (!s.summary) txt.classList.add("mut");
  kids.push(txt);

  // Replies off (server kill-switch, /api/meta): the section is the title and the
  // text. No field, note or enable button is built. The sid and a no-op _refresh
  // are still set, because repaintSteerRail bails out without them and the poll
  // would stop updating the text and the facts above.
  if (globalThis.repliesEnabled !== true) {
    el.replaceChildren(...kids);
    el.dataset.sid = s.id;
    el._refresh = () => {};
    return;
  }

  const form = document.createElement("div");
  form.className = "rep";
  const input = document.createElement("input");
  input.className = "fld";
  input.type = "text";
  input.setAttribute("aria-label", "Reply to this session");
  const send = document.createElement("button");
  send.type = "button";
  send.className = "btn pri";
  send.textContent = "send";
  form.append(input, send);
  const note = document.createElement("p");
  note.className = "note";
  // "Enable replies for this session first." names a fix: offer it right here.
  // Built only for a claude session — belt-and-braces alongside
  // offerEnableReplies' own s.claude check (buildReplyStrip's early return is
  // the same guard for the pane strip): a non-claude session's reply box can
  // never be enabled on the server, so its composer gets no such button at all.
  const enable = s.claude ? buildEnableRepliesButton(s.id, () => {
    // Focus only once the re-gate (refreshSessions, run synchronously inside
    // buildEnableRepliesButton's onclick before this callback fires) has
    // actually opened the field — a session that stopped waiting or entered a
    // tool call in the meantime must not receive a focused-but-disabled input.
    if (!input.disabled) input.focus();
  }) : null;
  kids.push(form, note);
  if (enable) kids.push(enable);
  el.replaceChildren(...kids);
  // The id the poll's repaint checks before it re-gates these controls.
  el.dataset.sid = s.id;

  // Restore an in-progress reply across a rebuild. The poll never rebuilds this
  // composer, but a focus event does — and clicking the ALREADY-active tab of
  // the focused session still runs Cell.activate, so "type a reply, click the
  // usage chip, lose the reply" was a reachable sequence. replyDraft is the
  // sidebar's existing per-session draft, reused rather than duplicated: one
  // unsent reply per session, wherever it was typed. Like the sidebar's, this
  // restores the TEXT only and never tries to reconstruct focus or the caret —
  // see replyDraft's declaration for the measured reason.
  if (typeof replyDraft !== "undefined" && replyDraft && replyDraft.id === s.id) input.value = replyDraft.text;
  input.oninput = () => { replyDraft = { id: s.id, text: input.value }; };

  // `cur` is the session as the latest poll describes it. Without it the gate
  // would read the object captured when this composer was built, and a session
  // that stopped working three seconds ago would keep a closed reply box.
  const refresh = (cur) => {
    const now = cur || s;
    // The rail does not fetch /peek, so pending is otherwise unknowable here —
    // except for need === "ask": a claude session blocked on a permission
    // prompt redraws its own tool call, so it still LOOKS "waiting" with no
    // /peek, and the server refuses a reply to it (409). Treating that one
    // case as pending keeps this gate honest without a fetch; POST /message
    // re-checks server-side either way, and the note says what the server said.
    const st = sendState({ replyable: now.replyable, activity: now.activity, pending: now.need === "ask" });
    const closed = !st.enabled || (typeof pendingSends !== "undefined" && pendingSends.has(s.id));
    input.disabled = closed;
    send.disabled = closed;
    input.classList.toggle("off", closed);
    send.classList.toggle("off", closed);
    input.placeholder = closed ? "reply opens when this session stops" : "reply…";
    note.textContent = closed ? (st.reason || "Sending…") : "";
    note.hidden = !closed;
    if (enable) enable.hidden = !offerEnableReplies(now);
  };
  refresh();
  el._refresh = refresh;

  send.onclick = async () => {
    const err = replyTextError(input.value);
    if (err) {
      note.hidden = false;
      note.textContent = err === "empty" ? "Type something first."
        : err === "newline" ? "One line only — newlines are not sent."
          : "That text contains control characters and was not sent.";
      return;
    }
    if (pendingSends.has(s.id)) return;
    pendingSends.add(s.id);
    refresh();
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/message", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: input.value }),
      });
      if (res.status === 204) {
        input.value = "";
        if (replyDraft && replyDraft.id === s.id) replyDraft = null; // sent — nothing left to restore
        showToast("Sent");
        return;
      }
      // The same four statuses the sidebar's composer maps, with the same
      // meanings. Carried by the toast alone: the finally below re-runs the
      // gate, and the gate owns this note's text — anything written here would
      // be overwritten a moment later, or cleared outright on a session the
      // gate finds open.
      showToast(res.status === 403 ? "Replies are not enabled for this session." :
        res.status === 409 ? "Session is not ready for a message right now." :
          res.status === 410 ? "That session is gone." :
            res.status === 400 ? "That text was rejected." :
              "Send failed (" + res.status + ").", { error: true });
    } catch (_) {
      showToast("Send failed: no connection.", { error: true });
    } finally {
      pendingSends.delete(s.id);
      refresh();
    }
  };
}

// --- Write elevation (M3b) --------------------------------------------------
//
// #steer-elev holds the Elevate / Extend / Drop buttons and the inline grant
// panel. It is NOT repainted with the facts: the facts section repaints on every
// poll, and a panel with a half-typed reason or a typed production label would
// be wiped by it. This section rebuilds only when its key changes (what the
// buttons or the panel show), and whatever the operator has typed survives a
// rebuild through elevDraft. The countdown lives in the scope row, which is the
// part that does repaint. The rail is hidden on mobile, so this is desktop-only.
let elevKey = "";
// The session whose grant panel is open. Module-level so a key-changing repaint
// keeps the panel open; a focus change to another session drops it.
let elevPanelFor = null;
let elevBusy = null;                      // id with a grant/drop request in flight
let elevErr = { id: "", text: "" };       // the panel's inline error line
let elevDraft = { id: "", minutes: 0, reason: "", label: "" };

function resetElevSection() {
  elevKey = "";
  elevPanelFor = null;
  elevBusy = null;
  elevErr = { id: "", text: "" };
  elevDraft = { id: "", minutes: 0, reason: "", label: "" };
  const el = document.getElementById("steer-elev");
  if (el) { el.replaceChildren(); el.hidden = true; }
}

// elevRepaint forces a rebuild for session id, if it is still the one in focus.
function elevRepaint(id) {
  elevKey = "";
  const cur = typeof railSession === "function" ? railSession() : null;
  if (cur && cur.id === id) renderElevSection(cur);
}

function renderElevSection(s) {
  const el = document.getElementById("steer-elev");
  if (!el) return;
  const st = awsRailCache.get(s.id) || null;
  const gp = s.aws ? grantPanel(s.aws, st) : null;
  if (!gp) {
    // No write role on the binding (or no binding at all): no section.
    if (elevKey || !el.hidden) resetElevSection();
    return;
  }
  const now = Date.now();
  const acts = elevActions(s.aws, now);
  const open = elevPanelFor === s.id && (acts.elevate || acts.extend);
  const key = [s.id, s.aws.elevated_until || "", acts.extend, acts.elevate,
    (st && st.elevation && st.elevation.last_valid) || "", gp.blocked,
    gp.hostSessions.length, gp.hostSessions.map((h) => h && h.name).join(","),
    open, elevBusy === s.id, elevErr.id === s.id ? elevErr.text : ""].join("|");
  if (key === elevKey) return;
  elevKey = key;
  el.hidden = false;
  el.replaceChildren(railTitle("Write access"),
    open ? buildElevPanel(s, gp, !acts.elevate) : buildElevButtons(s, gp, acts));
}

function elevButton(text, cls, onclick, disabled) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = cls;
  b.textContent = text;
  b.disabled = !!disabled;
  b.classList.toggle("off", !!disabled);
  b.onclick = onclick;
  return b;
}

function buildElevButtons(s, gp, acts) {
  const wrap = document.createElement("div");
  const busy = elevBusy === s.id;
  const row = document.createElement("div");
  row.className = "elevbtns";
  if (acts.elevate) row.appendChild(elevButton("Elevate", "btn", () => openElevPanel(s.id), busy));
  if (acts.extend) row.appendChild(elevButton("Extend", "btn", () => openElevPanel(s.id), busy));
  if (acts.drop) row.appendChild(elevButton("Drop", "btn", () => dropElevation(s), busy));
  wrap.appendChild(row);
  if (acts.elevate) {
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = "Read-only now. Elevate gives this session " + gp.role + " for a limited time.";
    wrap.appendChild(p);
  }
  return wrap;
}

function openElevPanel(id) {
  elevPanelFor = id;
  elevErr = { id: "", text: "" };
  elevDraft = { id: id, minutes: 0, reason: "", label: "" };
  // The host-session list and any blocker are read when the panel opens, not
  // trusted from whenever the rail was last focused.
  loadAwsRailStats(id);
  elevRepaint(id);
}

function closeElevPanel(id) {
  elevPanelFor = null;
  elevErr = { id: "", text: "" };
  elevDraft = { id: "", minutes: 0, reason: "", label: "" };
  elevRepaint(id);
}

// buildElevPanel: every server- or operator-supplied string (the production
// label, the role, host-session names, the blocker, the error) goes in through
// textContent.
function buildElevPanel(s, gp, extend) {
  if (elevDraft.id !== s.id) elevDraft = { id: s.id, minutes: 0, reason: "", label: "" };
  const box = document.createElement("div");
  box.className = "elevpanel";
  box.setAttribute("role", "group");
  const title = extend ? "Extend write access" : "Grant write access";
  box.setAttribute("aria-label", title);

  const ttl = document.createElement("p");
  ttl.className = "elevtitle";
  ttl.textContent = title;
  box.appendChild(ttl);

  // The window: one button per offered length, the longest preselected.
  let minutes = gp.windows.includes(elevDraft.minutes) ? elevDraft.minutes : gp.windows[gp.windows.length - 1];
  elevDraft.minutes = minutes;
  const wcol = document.createElement("div");
  wcol.className = "fcol";
  const wlab = document.createElement("span");
  wlab.className = "flab";
  wlab.textContent = "Window";
  const seg = document.createElement("div");
  seg.className = "seg";
  seg.setAttribute("role", "group");
  seg.setAttribute("aria-label", "Window");
  const segBtns = gp.windows.map((w) => {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = w + " min";
    b.onclick = () => {
      minutes = w;
      elevDraft.minutes = w;
      sync();
    };
    seg.appendChild(b);
    return [w, b];
  });
  wcol.append(wlab, seg);
  box.appendChild(wcol);

  const rcol = document.createElement("div");
  rcol.className = "fcol";
  const rlab = document.createElement("label");
  rlab.className = "flab";
  rlab.htmlFor = "elev-reason";
  rlab.textContent = "Reason (optional)";
  const reason = document.createElement("input");
  reason.id = "elev-reason";
  reason.className = "fld";
  reason.type = "text";
  reason.maxLength = 200;
  reason.autocomplete = "off";
  reason.value = elevDraft.reason;
  reason.oninput = () => { elevDraft.reason = reason.value; };
  rcol.append(rlab, reason);
  box.appendChild(rcol);

  let typed = null;
  if (gp.needLabel) {
    const lcol = document.createElement("div");
    lcol.className = "fcol";
    const llab = document.createElement("label");
    llab.className = "flab";
    llab.htmlFor = "elev-label";
    llab.textContent = 'Type "' + gp.label + '" to confirm';
    typed = document.createElement("input");
    typed.id = "elev-label";
    typed.className = "fld";
    typed.type = "text";
    typed.autocomplete = "off";
    typed.spellcheck = false;
    typed.value = elevDraft.label;
    typed.oninput = () => { elevDraft.label = typed.value; sync(); };
    lcol.append(llab, typed);
    box.appendChild(lcol);
  }

  if (gp.hostSessions.length) {
    const warn = document.createElement("div");
    warn.className = "warnbox";
    const lead = document.createElement("p");
    lead.textContent = "These sessions run on the host and can bypass elevation controls:";
    const ul = document.createElement("ul");
    for (const h of gp.hostSessions) {
      const li = document.createElement("li");
      li.textContent = (h && (h.name || h.id)) || "";
      ul.appendChild(li);
    }
    warn.append(lead, ul);
    box.appendChild(warn);
  }

  const role = document.createElement("p");
  role.className = "tiny mut";
  role.textContent = "Role: " + gp.role;
  box.appendChild(role);

  if (gp.blocked) {
    const warn = document.createElement("div");
    warn.className = "warnbox";
    warn.textContent = gp.blocked;
    box.appendChild(warn);
  }

  const err = document.createElement("p");
  err.className = "tiny elev-err";
  err.setAttribute("role", "alert");
  err.textContent = elevErr.id === s.id ? elevErr.text : "";
  err.hidden = !err.textContent;
  box.appendChild(err);

  const row = document.createElement("div");
  row.className = "elevbtns";
  const grant = elevButton("Grant write", "btn pri", () => submitElevGrant(s, gp));
  const cancel = elevButton("Cancel", "btn", () => closeElevPanel(s.id), elevBusy === s.id);
  row.append(grant, cancel);
  box.appendChild(row);

  // One place decides what is pressed and whether Grant is live, so the look
  // and the real disabled attribute cannot disagree. A blocker or an unmatched
  // production label closes it; the server re-checks all of it.
  function sync() {
    for (const [w, b] of segBtns) {
      b.classList.toggle("on", w === minutes);
      b.setAttribute("aria-pressed", w === minutes ? "true" : "false");
    }
    const ok = !gp.blocked && elevBusy !== s.id && (!gp.needLabel || (typed && typed.value === gp.label));
    grant.disabled = !ok;
    grant.classList.toggle("off", !ok);
  }
  sync();
  return box;
}

// submitElevGrant: POST the grant (an extension is the same endpoint). The
// browser adds Origin itself; the server accepts only a signed-in browser.
async function submitElevGrant(s, gp) {
  if (elevBusy) return;
  const id = s.id;
  elevBusy = id;
  elevErr = { id: "", text: "" };
  elevRepaint(id);
  let status = 0;
  let body = null;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(id) + "/aws/elevate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(grantBody(elevDraft.minutes, elevDraft.reason, elevDraft.label, gp.needLabel)),
    });
    status = res.status;
    try { body = await res.json(); } catch (_) { body = null; }
  } catch (_) { status = 0; }
  elevBusy = null;
  if (status === 200) {
    elevPanelFor = null;
    elevErr = { id: "", text: "" };
    elevDraft = { id: "", minutes: 0, reason: "", label: "" };
    loadAwsRailStats(id);
    if (typeof refreshSessions === "function") refreshSessions();
  } else {
    elevErr = { id: id, text: grantErrorText(status, body && body.error) };
  }
  elevRepaint(id);
}

// dropElevation: lowering power needs no panel and no label.
async function dropElevation(s) {
  if (elevBusy) return;
  const id = s.id;
  elevBusy = id;
  elevRepaint(id);
  let status = 0;
  let body = null;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(id) + "/aws/elevate", { method: "DELETE" });
    status = res.status;
    try { body = await res.json(); } catch (_) { body = null; }
  } catch (_) { status = 0; }
  elevBusy = null;
  if (status === 200) {
    showToast(dropToast(body && body.elevation));
    loadAwsRailStats(id);
    if (typeof refreshSessions === "function") refreshSessions();
  } else {
    showToast(status === 0 ? "Drop failed: no connection."
      : status === 404 || status === 410 ? "That session is gone."
        : "Drop failed (" + status + ").", { error: true });
  }
  elevRepaint(id);
}

// repaintSteerRail is the poll's entry point. It refreshes everything that can
// be refreshed WITHOUT rebuilding the composer: the facts, the last message's
// text, and the composer's own gate. The composer itself is never rebuilt from
// the 3-second poll — a rebuild every three seconds takes the caret out of a
// half-typed reply, which is the exact failure Cell.repaintReply exists to
// avoid in the panes — and the tool rows are never re-scanned, because that is
// a whole-transcript read and stays on demand.
function repaintSteerRail() {
  const el = document.getElementById("steer-msg");
  const rail = document.getElementById("steer-rail");
  if (!rail || rail.hidden || !el || typeof el._refresh !== "function") return;
  const s = railSession();
  // Only when the poll still describes the session this composer was built for.
  // A focus change rebuilds the whole rail through renderSteerRail, so there is
  // nothing to do here in that case — and gating one session's composer with
  // another session's state is the mix-up worth refusing.
  if (!s || s.id !== el.dataset.sid) return;
  renderSteerFacts(s);
  renderElevSection(s);
  // The last message in place, not through a rebuild: the composer below it
  // holds the caret.
  const txt = el.querySelector(".peektxt");
  if (txt) {
    txt.textContent = s.summary || "Nothing said yet.";
    txt.classList.toggle("mut", !s.summary);
  }
  el._refresh(s);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { railItems, railMeta, startedText, ctxFill, awsStatsRows, egressRow };
}
