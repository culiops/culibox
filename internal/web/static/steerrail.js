"use strict";

// The two rails' pure layer, kept out of app.js so `node --test` can reach it.
//
// This half is the icon rail: the column the sidebar becomes when it is hidden.
// It is a list of items, not markup, because the only decisions worth testing
// are which items exist, in what order, and what each one is called. Every item
// carries a label: the design gives a name to the toggle alone, and a status
// square with no accessible name is a control a screen reader announces as
// "button" and nothing more.

// railAttention: "block" (blocked on you), "done" (finished its turn) or "" — what
// the desktop's dots draw (attentionClass, attention.js). A finished turn is no
// longer an alert here: the rail's dot is neutral and the hover says it is your turn.
function railAttention(s) {
  return (typeof attentionClass === "function" ? attentionClass : require("./attention.js").attentionClass)(s);
}

// railDot: the session's dot, the one the sidebar row and the pane header draw.
function railDot(s) {
  return (typeof deskDot === "function" ? deskDot : require("./deskshell.js").deskDot)(s);
}

// railElevated: a write window is open on the session (deskshell.js owns the rule).
function railElevated(s, nowMs) {
  return (typeof sessionElevated === "function" ? sessionElevated : require("./deskshell.js").sessionElevated)(s, nowMs);
}

// railHint: a rail button's name with its chord from the registry (shortcuts.js).
function railHint(label, id, mac) {
  return (typeof hintTitle === "function" ? hintTitle : require("./shortcuts.js").hintTitle)(label, id, mac);
}

// The toggle is the frame's ghost "»" (the sidebar's "«" turned around), first in the
// item list; the DOM puts it at the top of the rail, level with the sidebar's collapse.
// ctx.mac picks the chord's spelling in its name.
function railItems(sessions, ctx) {
  const c = ctx || {};
  const nowMs = typeof c.nowMs === "number" ? c.nowMs : Date.now();
  const out = [{ kind: "toggle", label: railHint("Show sidebar", "sidebar.toggle", !!c.mac), glyph: "\u00bb", on: false, alert: false, elevated: false }];
  for (const s of sessions || []) {
    // attentionClass, not activity alone: an expired AWS sign-in blocks on the
    // operator while the agent still reads "working".
    const attention = railAttention(s);
    const waiting = attention === "block";
    const done = attention === "done";
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
      label: waiting ? name + " — waiting for you" : done ? name + " — your turn" : name,
      activity: (s && s.activity) || "",
      attention,
      dot: railDot(s),
      on: !!c.focusedId && !!s && s.id === c.focusedId,
      alert: !!waiting,
      // elevated: a write window is open (the rail draws a red ring). title is
      // the hover text: the bare name, so a dot alone says which session it is,
      // and for a finished turn "name · your turn" (the dot is neutral).
      elevated: railElevated(s, nowMs),
      title: done ? name + " \u00b7 your turn" : name,
    });
  }
  out.push({ kind: "new", label: "New session", glyph: "+", on: false, alert: false, elevated: false });
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
  const s = Math.floor(((nowMs || 0) - t) / 1000);
  if (s < 60) return clock + " · just now";
  const fmt = typeof fmtDuration === "function" ? fmtDuration : require("./duration.js").fmtDuration;
  return clock + " · " + fmt(s) + " ago";
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

// dropLabel: what one bundle drop is called on screen. A whole-kind drop has
// name "*" (the settings file as a whole, say), which reads as noise, so it is
// named by its kind. Any other name is a user config key and stays text.
function dropLabel(d) {
  return d.name === "*" ? d.kind : d.name;
}

// toastDropsSuffix: the spawn toast's tail for a boxed session whose config
// bundle left things out (Info.bundle_drops). A toast is a glance, so it names
// at most three and then an ellipsis; the rail has the full list. Absent or
// empty is "", so a clean spawn reads exactly as it did.
function toastDropsSuffix(drops) {
  if (!drops || !drops.length) return "";
  const names = drops.slice(0, 3).map(dropLabel);
  return " \u00b7 box left out " + drops.length + ": " + names.join(", ") + (drops.length > 3 ? ", \u2026" : "");
}

// dropsRow: the rail's "left out" row. Drops are grouped by kind + reason in
// first-seen order, so a dozen dropped settings keys that share one reason print
// that reason once. null when there is nothing to say, like egressRow.
function dropsRow(s) {
  const drops = s && s.bundle_drops;
  if (!drops || !drops.length) return null;
  const groups = new Map();
  for (const d of drops) {
    const key = d.kind + "\u0000" + d.reason;
    if (!groups.has(key)) groups.set(key, { reason: d.reason, labels: [] });
    groups.get(key).labels.push(dropLabel(d));
  }
  const v = [...groups.values()].map((g) => g.labels.join(", ") + " \u2014 " + g.reason).join("; ");
  return { k: "left out", v, wrap: true };
}

// lockedRow: the rail's "locked" row for a boxed session whose workspace files
// are locked on the Mac while it runs (Info.locked_files, macOS #96). null
// without any, like dropsRow. The names are workspace file names the server
// derived; they are only ever put in a string rendered with textContent.
function lockedRow(s) {
  const files = s && s.locked_files;
  if (!files || !files.length) return null;
  return { k: "locked", v: "Locked on your Mac while this session runs: " + files.join(", ") +
    " (macOS cannot otherwise stop the box changing them through another letter case)", wrap: true };
}

// cloneParts: what a clone-mode session's refresh state says, as plain strings.
// Every value in `c` is box-derived text (branch names, error text); it is
// sanitized server-side, but this layer treats it as untrusted anyway and only
// ever builds strings. The callers render them with textContent, never HTML.
// `warn` clauses want attention; `info` clauses are a status line. `unsent`,
// `skipped` and `refreshed` are the same clauses as in `info`, kept apart so the
// toast can pick the one that matters most.
function cloneClauses(c, profile) {
  const out = { warn: [], info: [], unsent: "", skipped: "", refreshed: "" };
  if (!c) return out;
  const b = c.base || "main";
  const br = c.branch;
  const stale = c.stale_branches || [];
  // The host record keeps an old tip for good, so `rewritten` alone stays true
  // after every branch is fixed. It is loud only while a branch still carries
  // the old history, or while there is no box report to check against.
  if (c.rewritten && (stale.length || !c.refreshed)) {
    out.warn.push("Host " + b + " was rewritten since this clone last saw it. " +
      (stale.length ? "Branches " + stale.join(", ") + " still carry the old history" : "Branches may carry the old history") +
      ": rebase them onto origin/" + b + " or recreate the clone before `clone fetch`.");
  } else if (c.rewritten) {
    out.info.push("host " + b + " was rewritten earlier; no branch here carries the old history");
  }
  if (c.error) {
    // An error beside a completed refresh (a host check that failed) does not
    // mean the box is on older code.
    out.warn.push(c.refreshed ? "Clone refreshed, but " + c.error + "." : "Clone not refreshed: " + c.error + "; the box is on older code.");
  }
  if (c.unverified) out.warn.push("This clone's history is unverified: created before refresh tracking. Recreate the clone to clear this.");
  if (c.head_in_host === false && !c.rewritten) {
    if (!br) out.unsent = "detached HEAD: commits not sent to host";
    else {
      const n = Number(c.ahead) || 0;
      out.unsent = "branch " + br + ": " + (n > 0 ? n + " commit" + (n === 1 ? "" : "s") : "commits") +
        " not sent to host (`culibox clone fetch " + (b === "main" ? "" : "-base " + b + " ") + profile + " " + br + "`)";
    }
    out.info.push(out.unsent);
  }
  if (c.refreshed) {
    if (c.ff_from) {
      // The count is box-derived: print it only when it is a positive number.
      const n = Number(c.ff_count) || 0;
      out.refreshed = b + " updated " + String(c.ff_from).slice(0, 7) + ".." + String(c.ff_to || "").slice(0, 7) + (n > 0 ? " (+" + n + ")" : "");
    }
    else if (c.ff_refused) out.refreshed = "local " + b + " has commits host " + b + " lacks; not updated";
    else out.refreshed = "up to date with host " + b;
    out.info.push(out.refreshed);
  }
  const skipText = {
    "in-use": "not refreshed: another session is using this clone",
    "other-instance": "not refreshed: a box from another CuliBox instance holds this clone",
    "created": "new clone of host " + b,
    "no-base": "not refreshed: host repo has neither main nor master",
  };
  if (c.skipped && skipText[c.skipped]) {
    out.skipped = skipText[c.skipped];
    out.info.push(out.skipped);
  }
  if (br && br !== b && c.behind > 0) out.info.push(br + " is " + c.behind + " behind " + b);
  return out;
}

function cloneParts(c, profile) {
  const o = cloneClauses(c, profile);
  return { warn: o.warn, info: o.info };
}

// cloneLevel: the severity of a session's clone state, null when it has none.
function cloneLevel(c) {
  if (!c) return null;
  return cloneClauses(c, "").warn.length ? "warn" : "info";
}

// cloneRow: the rail's "clone" row, one wrapping line. null without s.clone.
// `s.agent` is the profile name used in the `clone fetch` hint.
function cloneRow(s) {
  const c = s && s.clone;
  if (!c) return null;
  const p = cloneParts(c, s.agent);
  return {
    k: "clone",
    v: [...p.warn, ...p.info].join(" \u00b7 "),
    wrap: true,
    chip: p.warn.length ? "v warn" : undefined,
    title: "In the box, `git fetch origin` refreshes origin/" + (c.base || "main") + " at any time.",
  };
}

// toastCloneSuffix: the spawn toast's tail. One warning clause if there is one,
// else the single most useful short status (unsent work, then why a refresh was
// skipped, then the refresh result). "" without s.clone.
function toastCloneSuffix(s) {
  const c = s && s.clone;
  if (!c) return "";
  const o = cloneClauses(c, s.agent);
  if (o.warn.length) return " \u00b7 " + o.warn[0];
  const first = o.unsent || o.skipped || o.refreshed;
  return first ? " \u00b7 clone: " + first : "";
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

// ctxEmptyNote: the caption under the context meter when there is no bar to draw.
// A session just compacted whose boundary carried no size reports postCompact with
// zero tokens: that is "compacted", not "nothing reported yet".
function ctxEmptyNote(s, u) {
  if (!s.claude) return "Not a Claude session \u2014 no context to report.";
  if (u && u.postCompact && !(Number(u.contextTokens) > 0)) return "Compacted; size not reported.";
  // A count with no pct: the server found no usable window for this model, so no bar.
  if (u && Number(u.contextTokens) > 0) return "Window size unknown for this model.";
  return "No transcript usage reported yet.";
}

// ctxUsedText: the value of the "used" row; a post-compact count is the size the
// transcript recorded right after the compaction, not a reply's usage.
function ctxUsedText(f, u) {
  return withCommas(f.used) + (f.max ? " / " + withCommas(f.max) : "") + (u && u.postCompact ? " (after compact)" : "");
}

// --- DOM ---------------------------------------------------------------------

const STEER_KEY = "agent-box:steerRail";

// The rail describes the session in the focused TERMINAL tab. A focused file
// viewer is not a session and deliberately leaves the rail on its empty state
// rather than showing whichever agent happens to be nearby. A shell docked in
// the pane is not it either, even with the keyboard: the pane header names the
// pane's agent (cellTerminal), so the rail must describe, grant to and reply
// to that same session. (Desktop never shows the rail: Access is a pane view.)
function railSession() {
  const t = typeof focusedPaneTerminal === "function" ? focusedPaneTerminal() : null;
  const id = t ? t.id : null;
  if (!id) return null;
  return (lastSessions || []).find((s) => s.id === id) || null;
}

// steerOpen: the rail's open state, which on desktop is the focused pane's
// Access view (steerRailOpen, paneview.js): the rail itself stays hidden there,
// so reading it alone made toggleSteerRail open Access forever, never close it.
function steerOpen() {
  const el = document.getElementById("steer-rail");
  const shown = !!el && !el.hidden;
  if (typeof steerRailOpen !== "function" || typeof steerViaPaneView !== "function") return shown;
  const desktop = !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
  return steerRailOpen(desktop, focusedCell ? focusedCell.view : null, shown);
}

function setSteerOpen(open) {
  // Desktop: the rail no longer docks beside the grid; opening it opens the
  // focused pane's Access view (app.js steerViaPaneView). Mobile keeps the code below.
  if (typeof steerViaPaneView === "function" && steerViaPaneView(open)) return;
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
  if (s.aws || hasConns(s) || s.github || (s.tokens && s.tokens.length)) loadAwsRailStats(s.id);
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
  // /aws serves AWS-bound and GH-bound sessions alike (one broker, one audit
  // log), so a GH-only box takes its GitHub rows and egress line from the same
  // cache; the AWS rows and counters are the AWS binding's own.
  const brokerSt = awsRailCache.get(s.id);
  const deskWords = !(typeof mobileActive === "function" && mobileActive(window.innerWidth)); // "start" on the desktop, "spawn" on the phone
  const egressRows = s.aws || hasConns(s) || s.github || (s.tokens && s.tokens.length)
    ? [...awsRailRows(s, brokerSt, Date.now(), deskWords), ...(s.aws ? awsStatsRows(brokerSt, Date.now()) : []),
      ...awsConnRailRows(s, brokerSt, Date.now(), awsConnExpanded.has(s.id), deskWords),
      ...ghRailRows(brokerSt), ...stRailRows(brokerSt), egressRow(brokerSt)]
    : s.egress ? [egressRow(egressRailCache.get(s.id))] : [];
  // The bundle's drops and the clone's refresh state ride after the egress row, and on a boxed session with
  // no egress row at all; railMeta skips null rows.
  const extraRows = [...egressRows, dropsRow(s), lockedRow(s), cloneRow(s)];
  const meta = railMeta(s, usage, s.cwd ? shortPath(s.cwd) : "", Date.now(), badge, extraRows);
  const f = ctxFill(usage);
  const key = [s.id, meta.name, meta.activity,
    meta.tier ? meta.tier.label + ":" + meta.tier.cls + ":" + meta.tier.title : "",
    meta.rows.map((r) => r.k + "=" + r.v + (r.title ? "@" + r.title : "")).join(","), f.pct, f.used, f.max, s.claude ? 1 : 0, ctxEmptyNote(s, usage), usage && usage.postCompact ? 1 : 0,
    (s.aws && s.aws.elevated_until) || "", JSON.stringify(s.aws || null), JSON.stringify(s.github || null),
    JSON.stringify(s.aws_connections || null)].join("|");
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
  head.replaceChildren(hrow, ...meta.rows.map((r) => (r.kind ? connRowEl(s, r) : kvRow(r.k, r.v, r.wrap, r.chip, r.title))));

  // 2 — how full the window is. Drawn only when there is a number to draw.
  ctx.replaceChildren(railTitle("Context window"));
  if (f.max > 0 || f.used > 0) {
    ctx.appendChild(kvRow("used", ctxUsedText(f, usage)));
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
    cap.textContent = ctxEmptyNote(s, usage);
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

function hasConns(s) {
  return !!s && Array.isArray(s.aws_connections) && s.aws_connections.length > 0;
}

// Sessions whose connection list is shown in full instead of collapsed to six.
// Per session id, so opening one session's list never opens another's.
const awsConnExpanded = new Set();

// connRowEl: one row of awsConnRailRows. A block is three lines under one
// frame (name and chips; the muted identity; the base role's measured write
// access; the counters or last error); the
// collapse toggle is a plain button. Every string is server- or operator-
// supplied (a name, a label, a role, an error), so all of it goes in through
// textContent. The chips are the rail's own .bdg ones: no new colours.
function connRowEl(s, r) {
  if (r.kind === "more" || r.kind === "less") {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn connmore";
    b.textContent = r.kind === "more" ? "Show all " + r.n : "Show fewer";
    b.onclick = () => {
      if (r.kind === "more") awsConnExpanded.add(s.id); else awsConnExpanded.delete(s.id);
      renderSteerFacts(s);
    };
    return b;
  }
  const box = document.createElement("div");
  box.className = "connblk";
  const l1 = document.createElement("div");
  l1.className = "connl1";
  const nm = document.createElement("span");
  nm.className = "connnm";
  nm.textContent = r.name;
  nm.title = r.name;
  l1.appendChild(nm);
  for (const c of r.chips) {
    const chip = document.createElement("span");
    chip.className = c.chip;
    chip.textContent = c.text;
    l1.appendChild(chip);
  }
  box.appendChild(l1);
  const lines = [[r.detail, "", ""], [r.base, r.baseWarn ? " warn" : "", r.baseTitle], [r.stat, r.err ? " err" : "", ""]];
  for (const [text, extra, title] of lines) {
    if (!text) continue;
    const p = document.createElement("p");
    p.className = "tiny mut connln" + extra;
    p.textContent = text;
    if (title) p.title = title;
    box.appendChild(p);
  }
  return box;
}

function kvRow(k, v, wrap, chip, title) {
  const row = document.createElement("div");
  row.className = wrap ? "kv wrap" : "kv";
  const a = document.createElement("span");
  a.textContent = k;
  const b = document.createElement("span");
  // chip: the value is a status badge (the red production marker), not muted
  // text — it keeps its own colour and is never clipped.
  b.className = chip || "v";
  b.textContent = v;
  b.title = title || v;
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
    const st = sendState({ replyable: now.replyable, activity: now.activity, pending: now.need === "ask", need: now.need, reply_ready: now.reply_ready });
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
// The AWS connection picked in Write access, per session. Only meaningful for a
// session with named connections; grantPanel falls back to a valid one.
let elevSel = { id: "", name: "" };

function resetElevSection() {
  elevKey = "";
  elevPanelFor = null;
  elevBusy = null;
  elevErr = { id: "", text: "" };
  elevDraft = { id: "", minutes: 0, reason: "", label: "" };
  elevSel = { id: "", name: "" };
  const el = document.getElementById("steer-elev");
  if (el) { el.replaceChildren(); el.hidden = true; }
}

// elevRepaint forces a rebuild for session id, if it is still the one in focus.
function elevRepaint(id) {
  elevKey = "";
  const cur = typeof railSession === "function" ? railSession() : null;
  if (cur && cur.id === id) renderElevSection(cur);
}

// elevModel: the Write access section's inputs for session s, or null when
// there is nothing to elevate. `a` is the object elevActions reads: the inline
// binding, or the SELECTED connection. A window held by ANOTHER connection
// leaves Elevate offered but disabled (grantPanel's blockedText says why) and
// Drop live, since a drop names no connection.
function elevModel(s, st, now) {
  if (hasConns(s)) {
    const gp = grantPanel(null, st, s.aws_connections, elevSel.id === s.id ? elevSel.name : "", now);
    if (!gp) return null;
    const a = s.aws_connections.find((c) => c.name === gp.selected);
    const other = gp.elevatedConn && gp.elevatedConn !== gp.selected;
    return { gp, a, conns: true, line: connWindowLine(s.aws_connections, st, now),
      acts: other ? { elevate: true, extend: false, drop: true } : elevActions(a, now) };
  }
  const gp = s.aws ? grantPanel(s.aws, st) : null;
  return gp ? { gp, a: s.aws, conns: false, line: null, acts: elevActions(s.aws, now) } : null;
}

function renderElevSection(s) {
  const el = document.getElementById("steer-elev");
  if (!el) return;
  const st = awsRailCache.get(s.id) || null;
  const now = Date.now();
  const m = elevModel(s, st, now);
  if (!m) {
    // No write role on the binding (or no binding at all): no section.
    if (elevKey || !el.hidden) resetElevSection();
    return;
  }
  const { gp, a, acts } = m;
  const open = elevPanelFor === s.id && (acts.elevate || acts.extend) && !(m.conns && gp.elevatedConn && gp.elevatedConn !== gp.selected);
  const key = [s.id, a.elevated_until || "", acts.extend, acts.elevate,
    (st && st.elevation && st.elevation.last_valid) || "", gp.blocked,
    gp.hostSessions.length, gp.hostSessions.map((h) => h && h.name).join(","),
    open, elevBusy === s.id, elevErr.id === s.id ? elevErr.text : "",
    // The window line carries a minute countdown; the section is rebuilt when it
    // changes, but never under an open panel (that holds typed text).
    m.conns ? [gp.selected, gp.elevatedConn, gp.blockedText, gp.conns.map((c) => c.name).join(","),
      open || !m.line ? "" : m.line.name + ":" + m.line.text].join("~") : ""].join("|");
  if (key === elevKey) return;
  elevKey = key;
  el.hidden = false;
  el.replaceChildren(railTitle("Write access"),
    ...(m.conns ? [connPicker(s, gp)] : []),
    open ? buildElevPanel(s, gp, !acts.elevate) : buildElevButtons(s, gp, acts, m.line));
}

// connPicker: which connection Write access is about. Two or more write-capable
// connections get a segmented control; exactly one is shown as its name.
function connPicker(s, gp) {
  const wrap = document.createElement("div");
  wrap.className = "fcol";
  const lab = document.createElement("span");
  lab.className = "flab";
  lab.textContent = "Connection";
  wrap.appendChild(lab);
  if (gp.picker === "text") {
    const p = document.createElement("p");
    p.className = "tiny";
    p.textContent = gp.selected;
    wrap.appendChild(p);
    return wrap;
  }
  const pick = (name) => {
    if (name === gp.selected) return;
    // A prod label typed for one connection is not the next one's.
    elevSel = { id: s.id, name: name };
    elevErr = { id: "", text: "" };
    if (elevDraft.id === s.id) elevDraft.label = "";
    elevRepaint(s.id);
  };
  if (gp.picker === "select") {
    // Three or more would not fit the rail as buttons: a select never widens it.
    const sel = document.createElement("select");
    sel.className = "fld connsel";
    sel.setAttribute("aria-label", "Connection");
    for (const c of gp.conns) {
      const o = document.createElement("option");
      o.value = c.name;
      o.textContent = c.name;
      o.title = c.label || c.name;
      o.selected = c.name === gp.selected;
      sel.appendChild(o);
    }
    sel.onchange = () => pick(sel.value);
    wrap.appendChild(sel);
    return wrap;
  }
  const seg = document.createElement("div");
  seg.className = "seg connseg";
  seg.setAttribute("role", "group");
  seg.setAttribute("aria-label", "Connection");
  for (const c of gp.conns) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = c.name;
    b.title = c.label || c.name;
    const on = c.name === gp.selected;
    b.classList.toggle("on", on);
    b.setAttribute("aria-pressed", on ? "true" : "false");
    b.onclick = () => pick(c.name);
    seg.appendChild(b);
  }
  wrap.appendChild(seg);
  return wrap;
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

function buildElevButtons(s, gp, acts, line) {
  const wrap = document.createElement("div");
  const busy = elevBusy === s.id;
  const row = document.createElement("div");
  row.className = "elevbtns";
  // A refusal known in advance (another connection holds the window, or its
  // issued write credentials are still valid) closes the button; its text is
  // the server's own sentence, or "Drop <name> first".
  if (acts.elevate) row.appendChild(elevButton("Elevate", "btn", () => openElevPanel(s.id), busy || !!gp.blockedText));
  if (acts.extend) row.appendChild(elevButton("Extend", "btn", () => openElevPanel(s.id), busy));
  if (acts.drop) row.appendChild(elevButton("Drop", "btn", () => dropElevation(s), busy));
  wrap.appendChild(row);
  // The connection with a window (countdown, new-credentials warning) or with
  // write credentials still valid after a drop: the legacy scope row's own text.
  if (line) {
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = line.name + " \u00b7 " + line.text;
    wrap.appendChild(p);
  }
  if (acts.elevate) {
    const p = document.createElement("p");
    p.className = "tiny mut";
    p.textContent = gp.blockedText || ("Elevate gives this session " + gp.role + " for a limited time.");
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
  const title = (extend ? "Extend write access" : "Grant write access") + (hasConns(s) ? " \u00b7 " + gp.selected : "");
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
    llab.textContent = 'Type "' + gp.label + '" to confirm' + (hasConns(s) ? " (" + gp.selected + ")" : "");
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

  // blocked: the host-level refusal; blockedText: another connection's window
  // or leftover credentials. Both are the server's sentences, shown whole.
  for (const why of [gp.blocked, gp.blockedText]) {
    if (!why) continue;
    const warn = document.createElement("div");
    warn.className = "warnbox";
    warn.textContent = why;
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
    const ok = !gp.blocked && !gp.blockedText && elevBusy !== s.id && (!gp.needLabel || (typed && typed.value === gp.label));
    grant.disabled = !ok;
    grant.classList.toggle("off", !ok);
  }
  sync();
  return box;
}

// submitElevGrant: POST the grant (an extension is the same endpoint); the
// request itself is elevGrant's (elevation.js), shared with the Access view.
async function submitElevGrant(s, gp) {
  if (elevBusy) return;
  const id = s.id;
  elevBusy = id;
  elevErr = { id: "", text: "" };
  elevRepaint(id);
  let failure = null;
  try {
    await elevGrant(id, {
      name: hasConns(s) ? gp.selected : "", minutes: elevDraft.minutes, reason: elevDraft.reason,
      confirm_label: gp.needLabel ? elevDraft.label : undefined,
    });
  } catch (e) { failure = e; }
  elevBusy = null;
  if (!failure) {
    elevPanelFor = null;
    elevErr = { id: "", text: "" };
    elevDraft = { id: "", minutes: 0, reason: "", label: "" };
    loadAwsRailStats(id);
    if (typeof refreshSessions === "function") refreshSessions();
  } else {
    elevErr = { id: id, text: failure.message };
  }
  elevRepaint(id);
}

// dropElevation: lowering power needs no panel and no label (elevDrop's request).
async function dropElevation(s) {
  if (elevBusy) return;
  const id = s.id;
  elevBusy = id;
  elevRepaint(id);
  let view = null;
  let failure = null;
  try { view = await elevDrop(id); } catch (e) { failure = e; }
  elevBusy = null;
  if (!failure) {
    showToast(dropToast(view));
    loadAwsRailStats(id);
    if (typeof refreshSessions === "function") refreshSessions();
  } else {
    showToast(failure.message, { error: true });
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

// ghRowText: one broker audit row for a GH installation token, as a line of the
// steer rail. The payload carries no token (there is none to carry), only who it
// was minted for, how many repos, the outcome and when it lapses. A revoked row
// is just "revoked": its expiry is the original one and says nothing new. A
// refusal has no live expiry, so "until" appears only for a minted token.
function ghRowText(row) {
  const r = row || {};
  const parts = ["GitHub", String(r.subject || "")];
  if (r.outcome === "revoked") return parts.concat("revoked").join(" · ");
  const n = Number(r.repos) || 0;
  parts.push(n + (n === 1 ? " repo" : " repos"), String(r.outcome || "").replace(/_/g, " "));
  // Only a GitHub row's entry is a PAT; an AWS row's entry is the connection name.
  if (r.entry) parts.push((r.provider === "github" ? "PAT " : "") + String(r.entry));
  if (r.outcome === "minted") {
    const t = Date.parse(r.expires);
    if (Number.isFinite(t) && t > Date.UTC(2000, 0, 1)) {
      const d = new Date(t);
      parts.push("until " + String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0"));
    }
  }
  return parts.join(" · ");
}

// ghRailRows: the GH rows of GET /api/sessions/{id}/aws `recent` (the reply
// carries AWS and GH audit rows together, told apart by provider). Capped so a
// busy session cannot push the rail's other sections off screen.
const GH_RAIL_ROWS = 5;
function ghRailRows(st) {
  const recent = st && Array.isArray(st.recent) ? st.recent : [];
  return recent.filter((r) => r && r.provider === "github").slice(0, GH_RAIL_ROWS)
    .map((r) => ({ k: "github", v: ghRowText(r), wrap: true }));
}

// stRowText: one static-token audit row as a line of the steer rail:
// "Token · <entry> · <outcome> · HH:MM". Subject is the entry's name, never a
// value; the payload carries no value, variable name or path to print.
function stRowText(row) {
  const r = row || {};
  const parts = ["Token", String(r.subject || ""), String(r.outcome || "").replace(/_/g, " ")];
  const t = Date.parse(r.time);
  if (Number.isFinite(t) && t > Date.UTC(2000, 0, 1)) {
    const d = new Date(t);
    parts.push(String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0"));
  }
  return parts.join(" · ");
}

// stRailRows: the token rows of GET /api/sessions/{id}/aws: the recent static
// fetches (the reply keeps them apart from `recent`, so a chatty token cannot
// push the AWS and GitHub rows out), then one row naming the bound entries,
// and one flagging any production entry in the AWS prod chip's red. Capped so a
// busy session cannot push the rail's other sections off screen.
const ST_RAIL_ROWS = 5;
function stRailRows(st) {
  const recent = st && Array.isArray(st.static_recent) ? st.static_recent : [];
  const rows = recent.slice(0, ST_RAIL_ROWS).map((r) => ({ k: "token", v: stRowText(r), wrap: true }));
  const bound = st && Array.isArray(st.tokens) ? st.tokens : [];
  if (bound.length) {
    rows.push({ k: "tokens", v: bound.map((t) => t.name).join(", "), wrap: true });
    const prod = bound.filter((t) => t.prod);
    if (prod.length) rows.push({ k: "prod tokens", v: prod.map((t) => t.name).join(", "), chip: "bdg aws prod", wrap: true });
  }
  return rows;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { stRowText, stRailRows, railItems, railMeta, startedText, ctxFill, ctxEmptyNote, ctxUsedText, awsStatsRows, egressRow, dropLabel, toastDropsSuffix, dropsRow, lockedRow, cloneParts, cloneLevel, cloneRow, toastCloneSuffix, ghRowText, ghRailRows };
}
