"use strict";

// The two rails' pure layer, kept out of app.js so `node --test` can reach it.
//
// This half is the icon rail: the column the sidebar becomes when it is hidden.
// It is a list of items, not markup, because the only decisions worth testing
// are which items exist, in what order, and what each one is called. Every item
// carries a label: the design gives a name to the toggle alone, and a status
// square with no accessible name is a control a screen reader announces as
// "button" and nothing more.

function railItems(sessions, ctx) {
  const c = ctx || {};
  const out = [{ kind: "toggle", label: "Show sidebar", glyph: "☰", on: false, alert: false }];
  for (const s of sessions || []) {
    const waiting = s && s.activity === "waiting";
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
  // A clock a few seconds behind the daemon must read "just now", never
  // "-1m ago": the skew is ours, and the reader cannot act on it either way.
  let s = Math.floor(((nowMs || 0) - t) / 1000);
  if (s < 60) return "just now";
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d) return d + "d " + h + "h ago";
  if (h) return h + "h " + m + "m ago";
  return m + "m ago";
}

// railMeta: the header and the four-row block. cwdText is passed in already
// shortened, the way sessionMeta takes its own strings, so this file needs no
// opinion about what home is.
function railMeta(s, usage, cwdText, nowMs) {
  if (!s) return null;
  const u = usage || {};
  const rows = [];
  if (u.model) rows.push({ k: "model", v: String(u.model) });
  if (u.effort) rows.push({ k: "effort", v: String(u.effort) });
  if (cwdText) rows.push({ k: "cwd", v: String(cwdText) });
  const st = startedText(s.created, nowMs);
  if (st) rows.push({ k: "started", v: st });
  // Only the two tiers the daemon actually reports. An omitted runtime is not
  // "box" — a badge claiming containment that was never claimed by the server
  // is the one lie this panel must not tell.
  const tier = s.runtime === "host" ? { label: "host", cls: "bdg host" }
    : s.runtime === "box" ? { label: "box", cls: "bdg box" }
      : null;
  return { id: s.id, name: s.name || s.id, activity: s.activity || "", tier, rows };
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
    // The composer is gone, so nothing may re-gate it: both of the handles the
    // poll looks for go with it. An expando survives replaceChildren.
    delete msg.dataset.sid;
    msg._refresh = null;
    return;
  }
  // 1 and 2 — who this is, and how full its window is.
  renderSteerFacts(s);

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
function renderSteerFacts(s) {
  const head = document.getElementById("steer-head");
  const ctx = document.getElementById("steer-ctx");
  if (!head || !ctx) return;
  const tab = typeof terminalTabFor === "function" ? terminalTabFor(s.id) : null;
  const usage = tab && typeof tab.usage === "function" ? tab.usage() : null;
  const meta = railMeta(s, usage, s.cwd ? shortPath(s.cwd) : "", Date.now());
  const f = ctxFill(usage);
  const key = [s.id, meta.name, meta.activity, meta.tier ? meta.tier.label : "",
    meta.rows.map((r) => r.k + "=" + r.v).join(","), f.pct, f.used, f.max, s.claude ? 1 : 0].join("|");
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
    b.textContent = meta.tier.label;
    hrow.appendChild(b);
  }
  head.replaceChildren(hrow, ...meta.rows.map((r) => kvRow(r.k, r.v)));

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

function kvRow(k, v) {
  const row = document.createElement("div");
  row.className = "kv";
  const a = document.createElement("span");
  a.textContent = k;
  const b = document.createElement("span");
  b.className = "v";
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
    : "Result-payload bytes over the whole session — a proxy for context pressure, not billing.";
  kids.push(note);
  el.replaceChildren(...kids);
}

async function scanAttribution(id) {
  const s = (lastSessions || []).find((x) => x.id === id);
  if (!s) return;
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
  kids.push(form, note);
  el.replaceChildren(...kids);
  // The id the poll's repaint checks before it re-gates these controls.
  el.dataset.sid = s.id;

  // `cur` is the session as the latest poll describes it. Without it the gate
  // would read the object captured when this composer was built, and a session
  // that stopped working three seconds ago would keep a closed reply box.
  const refresh = (cur) => {
    const now = cur || s;
    // pending is false because the rail does not fetch /peek. POST /message
    // re-checks it server-side and answers 409, which the mapping below names:
    // the gate is at the server, and the note says what the server said.
    const st = sendState({ replyable: now.replyable, activity: now.activity, pending: false });
    const closed = !st.enabled || (typeof pendingSends !== "undefined" && pendingSends.has(s.id));
    input.disabled = closed;
    send.disabled = closed;
    input.classList.toggle("off", closed);
    send.classList.toggle("off", closed);
    input.placeholder = closed ? "reply opens when this session stops" : "reply…";
    note.textContent = closed ? (st.reason || "Sending…") : "";
    note.hidden = !closed;
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
      if (res.status === 204) { input.value = ""; showToast("Sent"); return; }
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
  module.exports = { railItems, railMeta, startedText, ctxFill };
}
