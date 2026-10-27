"use strict";

// Pure decisions for the desktop shell (sidebar groups, row chip and sub-line,
// pane header, ask strip, empty state). No DOM, so `node --test` reaches all of
// it; the wiring lives in app.js and tabs.js.
//
// Everything it consumes is a top-level function of another classic script. In
// the browser those are globals; under node they are required. deskDep() resolves
// them at call time, so load order never matters and no top-level name here can
// collide with a sibling script's.

function deskDep(name) {
  const g = {
    sessionGroups: typeof sessionGroups === "function" ? sessionGroups : null,
    needsYou: typeof needsYou === "function" ? needsYou : null,
    cardContent: typeof cardContent === "function" ? cardContent : null,
    needLabel: typeof needLabel === "function" ? needLabel : null,
    sessionDot: typeof sessionDot === "function" ? sessionDot : null,
    tierBadge: typeof tierBadge === "function" ? tierBadge : null,
    isElevated: typeof isElevated === "function" ? isElevated : null,
    awsBadgeParts: typeof awsBadgeParts === "function" ? awsBadgeParts : null,
  };
  if (g[name]) return g[name];
  const mod = { sessionGroups: "./mobileshell.js", needsYou: "./attention.js", cardContent: "./attention.js",
    needLabel: "./attention.js", sessionDot: "./attention.js", tierBadge: "./tier.js",
    isElevated: "./sessionrow.js", awsBadgeParts: "./sessionrow.js" }[name];
  return require(mod)[name];
}

const DESK_RECENT_MAX = 3;

// deskGroups: the sidebar's groups. Same buckets as the mobile Sessions screen;
// a session that needsYou for a non-activity reason (expired AWS sign-in, locked
// store) is bucketed as waiting, like its dot.
function deskGroups(sessions) {
  const needs = deskDep("needsYou");
  return deskDep("sessionGroups")(sessions, (s) => (needs(s) ? "waiting" : s.activity));
}

// deskOrder: the groups flattened in the order the sidebar shows them. Alt+1..9
// and the palette's first nine rows both index into this, so what you see is
// what the number means.
function deskOrder(groups) {
  const out = [];
  for (const g of groups || []) out.push(...g.sessions);
  return out;
}

// sessionElevated: any AWS write window is open on the session, whether on the
// legacy single record or on any named connection.
function sessionElevated(s, nowMs) {
  if (!s) return false;
  const is = deskDep("isElevated");
  if (is(s.aws, nowMs)) return true;
  return Array.isArray(s.aws_connections) && s.aws_connections.some((c) => is(c, nowMs));
}

// elevatedCount: sessions with a write window open (a session is counted once,
// however many connections it has open).
function elevatedCount(sessions, nowMs) {
  return (sessions || []).filter((s) => sessionElevated(s, nowMs)).length;
}

// deskContainmentChip: the tier as a chip. tierBadge's "danger" tone is the host
// tier; null for an exited session, whose tier is not actionable.
function deskContainmentChip(s, boxRuntime) {
  const t = deskDep("tierBadge")(s, boxRuntime);
  if (!t) return null;
  return { text: t.text, tone: t.tone === "danger" ? "host" : "box", title: t.title };
}

// rowChip: the ONE chip on a sidebar row. An open write window outranks the
// tier because it is the fact a reader must not miss. null when there is
// nothing to show (an exited session that is not elevated).
function rowChip(s, nowMs, boxRuntime) {
  if (sessionElevated(s, nowMs)) return { text: "write", tone: "write" };
  const c = deskContainmentChip(s, boxRuntime);
  return c ? { text: c.text, tone: c.tone } : null;
}

// rowSubline: the row's second line. What the session needs, in amber, when it
// needs you; otherwise its summary, else the activity word the old row showed.
// (No idle duration: the session record carries none.)
function rowSubline(s) {
  if (deskDep("needsYou")(s)) {
    return { text: deskDep("needLabel")(s) + ": " + deskDep("cardContent")(s).text, amber: true };
  }
  const v = s || {};
  return { text: v.summary || deskDep("sessionDot")(v).title || "", amber: false };
}

function deskBaseName(p) {
  return String(p || "").split("/").filter(Boolean).pop() || String(p || "");
}

// paneHeaderModel: the pane header's one line, in fixed slot order: containment,
// cloud, model, ctx. Below the narrow threshold only ONE chip survives (the
// write chip if there is one, else containment) and model/ctx are dropped, so
// the name keeps the room. The name itself is never shortened here: CSS ellipsis
// does that, and a test pins that a long name passes through.
function paneHeaderModel({ tab, session, nowMs, boxRuntime, narrow }) {
  if (tab && tab.kind === "viewer") {
    return { dot: null, name: deskBaseName(tab.path || tab.name), chips: [], model: null, ctx: null };
  }
  const s = session || null;
  const name = (s && (s.name || s.id)) || (tab && tab.name) || "";
  const dot = s ? deskDep("sessionDot")(s) : null;
  const chips = [];
  const cont = s ? deskContainmentChip(s, boxRuntime) : null;
  if (cont) chips.push(cont);
  const parts = s ? deskDep("awsBadgeParts")(s, nowMs) : null;
  if (parts) {
    chips.push({ text: parts.label, tone: parts.write ? "write" : parts.prod ? "prod" : "cloud", title: parts.title });
  } else if (sessionElevated(s, nowMs)) {
    chips.push({ text: "write", tone: "write", title: "AWS write window open" });
  }
  if (narrow) {
    const w = chips.find((c) => c.tone === "write");
    return { dot, name, chips: w ? [w] : chips.slice(0, 1), model: null, ctx: null };
  }
  const u = tab && typeof tab.usage === "function" ? tab.usage() : null;
  return {
    dot, name, chips,
    model: u && u.model ? String(u.model) : null,
    ctx: u && typeof u.pct === "number" ? u.pct + "%" : null,
  };
}

// needStripModel: the ask strip under a pane header, null unless the session
// needs you. cardContent's text is the command for an ask, else the need's own sentence.
function needStripModel(s) {
  if (!deskDep("needsYou")(s)) return null;
  const c = deskDep("cardContent")(s);
  return { label: deskDep("needLabel")(s), text: c.text };
}

// emptyStateModel: which empty state the pane area shows. "first": nothing has
// ever been set up (no sessions and only built-in profiles, which the server
// marks editable:false). "none": sessions exist but no pane is open (the caller
// only asks when that is so). profiles: up to three recently spawned profile
// names that still exist.
function emptyStateModel({ sessions, profiles, recent }) {
  const ps = Array.isArray(profiles) ? profiles : [];
  const have = new Set(ps.map((p) => p.name));
  const names = (Array.isArray(recent) ? recent : []).filter((n) => have.has(n)).slice(0, DESK_RECENT_MAX);
  const hasSessions = Array.isArray(sessions) && sessions.length > 0;
  let kind = null;
  if (hasSessions) kind = "none";
  else if (ps.every((p) => p.builtin === true || !p.editable)) kind = "first";
  return { kind, profiles: names };
}

// pushRecentProfile: the recent-profiles list after spawning with `name`.
function pushRecentProfile(list, name) {
  const rest = (Array.isArray(list) ? list : []).filter((n) => n !== name);
  return (name ? [name, ...rest] : rest).slice(0, DESK_RECENT_MAX);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deskGroups, deskOrder, sessionElevated, elevatedCount, rowChip, rowSubline,
    paneHeaderModel, needStripModel, emptyStateModel, pushRecentProfile };
}
