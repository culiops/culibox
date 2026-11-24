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
    needsYou: typeof needsYou === "function" ? needsYou : null,
    attentionClass: typeof attentionClass === "function" ? attentionClass : null,
    activityClass: typeof activityClass === "function" ? activityClass : null,
    cardContent: typeof cardContent === "function" ? cardContent : null,
    plainSummary: typeof plainSummary === "function" ? plainSummary : null,
    needLabel: typeof needLabel === "function" ? needLabel : null,
    connSSOSources: typeof connSSOSources === "function" ? connSSOSources : null,
    sessionDot: typeof sessionDot === "function" ? sessionDot : null,
    tierBadge: typeof tierBadge === "function" ? tierBadge : null,
    isElevated: typeof isElevated === "function" ? isElevated : null,
    minutesLeft: typeof minutesLeft === "function" ? minutesLeft : null,
    awsBadgeParts: typeof awsBadgeParts === "function" ? awsBadgeParts : null,
    isNestedShell: typeof isNestedShell === "function" ? isNestedShell : null,
    shellChildren: typeof shellChildren === "function" ? shellChildren : null,
    shortModel: typeof shortModel === "function" ? shortModel : null,
    connStateLabel: typeof connStateLabel === "function" ? connStateLabel : null,
    spawnIsShellProfile: typeof spawnIsShellProfile === "function" ? spawnIsShellProfile : null,
    claudeKindOf: typeof claudeKindOf === "function" ? claudeKindOf : null,
  };
  if (g[name]) return g[name];
  const mod = { needsYou: "./attention.js", attentionClass: "./attention.js", activityClass: "./activity.js", cardContent: "./attention.js", plainSummary: "./attention.js",
    needLabel: "./attention.js", connSSOSources: "./attention.js", sessionDot: "./attention.js", tierBadge: "./tier.js",
    isElevated: "./sessionrow.js", minutesLeft: "./sessionrow.js", awsBadgeParts: "./sessionrow.js",
    isNestedShell: "./shellnest.js", shellChildren: "./shellnest.js", shortModel: "./analytics.js", connStateLabel: "./activity.js",
    spawnIsShellProfile: "./spawnpreview.js", claudeKindOf: "./claudemark.js" }[name];
  return require(mod)[name];
}

const DESK_RECENT_MAX = 3;

// deskWsLabels: the group titles for a set of workspace roots, Map<root, label>.
// "" is "Other" (a session outside any git repo); any other root reads as its
// basename. Two or more distinct roots that share a basename each read
// "name · parent name", so /a/x/app and /b/y/app become "app · x" and
// "app · y". A root with no parent segment (/app) keeps its bare basename.
function deskWsLabels(roots) {
  const distinct = [...new Set((roots || []).filter((r) => typeof r === "string"))];
  const seen = new Map();
  for (const r of distinct) if (r) seen.set(deskBaseName(r), (seen.get(deskBaseName(r)) || 0) + 1);
  const out = new Map();
  for (const r of distinct) {
    if (!r) { out.set(r, "Other"); continue; }
    const base = deskBaseName(r);
    let label = base;
    if (seen.get(base) > 1) {
      const parent = deskBaseName(r.split("/").filter(Boolean).slice(0, -1).join("/"));
      if (parent) label = base + " · " + parent;
    }
    out.set(r, label);
  }
  return out;
}

// deskWsGroups: the sidebar's groups, one per workspace_root (spec 7). The key
// and path are the root exactly as the server sends it ("" is Other). A nested
// shell rides with its parent and its own root is ignored; an orphan shell is an
// ordinary row grouped by its own root. count, waiting and dots include those
// shells. Groups holding a row blocked on you (attentionClass "block", so an
// exited session's stale need does not count) come first; within each of those
// two tiers Other is last and the rest go by label, then path. Inside a group:
// blocked, then finished-turn, then working, then the rest, ties by name, then
// id, each row followed by its nested shells. A collapsed group keeps its
// sessions, its waiting count and its dots: only the sidebar hides the rows.
function deskWsGroups(sessions, collapsed) {
  const attn = deskDep("attentionClass");
  const list = sessions || [];
  const kids = deskDep("shellChildren")(list);
  const nested = deskDep("isNestedShell");
  const rank = (s) => { const a = attn(s); return a === "block" ? 0 : a === "done" ? 1 : s.activity === "working" ? 2 : 3; };
  const nameOf = (s) => String(s.name || s.id || "");
  const cmp = (a, b) => rank(a) - rank(b) || nameOf(a).localeCompare(nameOf(b)) || String(a.id).localeCompare(String(b.id));
  const byRoot = new Map();
  for (const s of list) {
    if (nested(s, list)) continue;
    const root = typeof s.workspace_root === "string" ? s.workspace_root : "";
    if (!byRoot.has(root)) byRoot.set(root, []);
    byRoot.get(root).push(s);
  }
  const labels = deskWsLabels([...byRoot.keys()]);
  const groups = [];
  for (const [root, tops] of byRoot) {
    const rows = [];
    for (const s of tops.sort(cmp)) rows.push(s, ...(kids.get(s.id) || []));
    groups.push({
      key: root, label: labels.get(root), path: root, count: rows.length,
      waiting: rows.filter((s) => attn(s) === "block").length,
      dots: rows.map((s) => deskDot(s).cls),
      collapsed: !!(collapsed && collapsed.has(root)),
      sessions: rows,
    });
  }
  const text = (a, b) => a.localeCompare(b, undefined, { sensitivity: "base" });
  return groups.sort((a, b) =>
    (b.waiting > 0) - (a.waiting > 0) || (a.key === "") - (b.key === "") || text(a.label, b.label) || text(a.path, b.path));
}

// deskGroups: the desktop sidebar's groups, now by workspace (deskWsGroups). The
// name stays for its callers. The phone's list does not come through here: it
// keeps sessionGroups and needsYou (app.js refreshSessions).
function deskGroups(sessions, collapsed) {
  return deskWsGroups(sessions, collapsed);
}

// jumpOrder: the list Mod+1…9 and the palette's first nine index into. On
// desktop that is the sidebar's order; mobile has no such list on screen and
// keeps the API order. Nested shells are left out of both: the numbers (and the
// rail dots) are for agents. On desktop the groups are built from the FULL list,
// as the sidebar builds them, and the shells are dropped from the flattened
// order afterwards: a waiting shell lifts its group on screen, so it must lift
// it here too, or Mod+1 would open a row that is not first. Given `collapsed`, a
// collapsed group's rows are skipped too, because a number must point at a row
// the user can see. Without it the order holds every agent (the rail, the
// attention chip, the palette's list and nextNeedsYou read that one).
function jumpOrder(sessions, mobile, collapsed) {
  const nested = deskDep("isNestedShell");
  const list = sessions || [];
  const hasShell = list.some((s) => nested(s, list));
  if (mobile) return hasShell ? list.filter((s) => !nested(s, list)) : list;
  const order = deskOrder(deskGroups(list, collapsed), !!collapsed);
  return hasShell ? order.filter((s) => !nested(s, list)) : order;
}

// nextNeedsYou: Mod+Shift+J (F69). The session to go to: those blocked on you
// first, then those that finished their turn, each in the sidebar's order
// (jumpOrder, so nested shells are not targets), the first one after currentId
// and wrapping round. A currentId that is not one of them (a working session, a
// viewer, nothing) starts from the first. The current session itself when it is
// the only one; null when nothing needs you (the shortcut then does nothing).
// Returns the session record, as jumpTarget does.
function nextNeedsYou(sessions, currentId) {
  const attn = deskDep("attentionClass");
  const order = jumpOrder(sessions, false);
  const list = order.filter((s) => attn(s) === "block").concat(order.filter((s) => attn(s) === "done"));
  if (!list.length) return null;
  const at = list.findIndex((s) => s.id === currentId);
  return list[(at + 1) % list.length]; // at -1 (not a candidate) lands on the first
}

// deskOrder: the groups flattened in the order the sidebar shows them, minus the
// collapsed ones when skipCollapsed. Mod+1…9 and the palette's digit hints index
// into the skipping form, so what you see is what the number means.
function deskOrder(groups, skipCollapsed) {
  const out = [];
  for (const g of groups || []) if (!(skipCollapsed && g.collapsed)) out.push(...g.sessions);
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

// costTodayRange: the from/to of the sidebar footer's "$ today". UTC, because
// that is what the Analytics page's own "today" is (analytics.js anTodayUTC),
// and the footer must equal what that page shows.
function costTodayRange(nowMs) {
  const day = new Date(nowMs).toISOString().slice(0, 10);
  return { from: day, to: day };
}

// deskContainmentChip: the tier as a chip. tierBadge's "danger" tone is the host
// tier; null for an exited session, whose tier is not actionable.
function deskContainmentChip(s, boxRuntime) {
  const t = deskDep("tierBadge")(s, boxRuntime);
  if (!t) return null;
  return { text: t.text, tone: t.tone === "danger" ? "host" : "box", title: t.title };
}

const DESK_CLONE_TITLE = "Contained: runs in a box on a private copy of the repo.";

// rowChip: the ONE chip on a sidebar row. An open write window outranks the
// tier because it is the fact a reader must not miss. null when there is
// nothing to show (an exited session that is not elevated). A tier chip names
// its glyph (icon: box, clone or host); a boxed session that has a clone_of is
// a clone, and says what that means. The write chip has no glyph.
function rowChip(s, nowMs, boxRuntime) {
  if (sessionElevated(s, nowMs)) return { text: "write", tone: "write" };
  const c = deskContainmentChip(s, boxRuntime);
  if (!c) return null;
  if (c.tone === "host") return { text: c.text, tone: c.tone, icon: "host" };
  if (s && s.clone_of) return { text: "clone", tone: c.tone, icon: "clone", title: DESK_CLONE_TITLE };
  return { text: c.text, tone: c.tone, icon: "box" };
}

// deskRowKind: which glyph leads a sidebar row. A profile that runs a plain shell gets the terminal
// (a capability of the profile, looked up by the session's agent); a session whose view says it is a
// Claude one gets the mark. Identity for an icon, never a gate. null: no glyph.
function deskRowKind(s, profile) {
  if (deskDep("spawnIsShellProfile")(profile)) return { kind: "shell", title: deskBaseName(profile.bin) + " shell" };
  if (deskDep("claudeKindOf")(s)) return { kind: "claude", title: "Claude Code session" };
  return null;
}

// TAB_TIER_LABEL: a session's tier as the pane's tab switcher words it. The keys name the glyphs, the sidebar chip's and
// New session's (svgicon.js SVG_HOST_24, SVG_BOX_24, SVG_CLONE_24, which are tierpick.js's TP_ICONS).
const TAB_TIER_LABEL = { host: "Host", box: "Box", clone: "Box clone" };

// tabSwitchFacts: what a row of the pane's tab switcher shows for tab `t` (#365): its kind (claude, shell, file or
// generic) and, for a session, the tier it runs on. Both come from declared fields, never the title: a viewer tab is a
// file; a terminal tab's session (by id in `sessions`) is a Claude one or a shell by deskRowKind, the sidebar's rule;
// the tier is the session's runtime and clone_of, read as tierBadge and rowChip read them (box, a clone when it has a
// clone_of, else host). A session the list does not hold has no tier; an ended one keeps the tier it ran on, since
// its tab still shows its terminal.
function tabSwitchFacts(t, sessions, profiles) {
  if (t && t.kind === "viewer") return { kind: "file", tier: null };
  const s = t && t.kind === "terminal" ? (sessions || []).find((x) => x && x.id === t.id) : null;
  if (!s) return { kind: "generic", tier: null };
  const k = deskRowKind(s, (profiles || []).find((p) => p && p.name === s.agent));
  const key = s.runtime === "box" ? (s.clone_of ? "clone" : "box") : "host";
  return { kind: k ? k.kind : "generic", tier: { key, label: TAB_TIER_LABEL[key] } };
}

// rowSubline: the row's second line. What the session needs, in amber, when it
// is blocked on you; otherwise its summary, else the activity word the old row
// showed. A finished turn (F21-B) reads its summary like any session at rest, muted,
// or "your turn" when the agent left none: the group header already says it is
// done, so no "your turn:" prefix and no amber. (No idle duration: the session
// record carries none.) A sign-in line carries view: "access": a click opens the
// session's Access view, where sign-in lives. A session the daemon has lost, or
// whose terminal another tab took over (opts.takenOver, a fact only the page
// knows), reads that instead of its summary (F55): one connection vocabulary,
// connStateLabel. Blocked on you still wins: an ask is the more urgent line.
function rowSubline(s, opts) {
  const attn = deskDep("attentionClass")(s);
  const conn = attn === "block" ? "" : deskDep("connStateLabel")(deskConnState(s, opts));
  if (conn) return { text: conn, amber: false };
  if (attn === "done") return { text: deskDep("plainSummary")(s.summary) || "your turn", amber: false };
  if (attn === "block") {
    // A named connection's expired sign-in on an agent that is not itself
    // waiting has no needLabel and would read "Waiting for input.", which says
    // nothing about the sign-in.
    if (!deskDep("needLabel")(s) && s.activity !== "waiting" && deskDep("connSSOSources")(s).length) {
      return { text: "SSO expired \u00b7 Sign in", amber: true, view: "access" };
    }
    // A waiting session can have no named need (needLabel ""): no dangling ": ".
    const why = deskDep("needLabel")(s);
    const text = deskDep("cardContent")(s, { plain: true }).text;
    // An ask that is a tool call reads as the frames draw it: "Asks to run" and the call after it (the
    // command alone for Bash), the call in mono (machine text). needLabel keeps its "asks" word, which the
    // phone card shares. An ask with no tool call (s.ask empty: the text is the agent's own words or the
    // waiting line) keeps the plain "asks: <text>" below: it does not claim anything is about to run.
    if (why === "asks" && s.ask) {
      const lead = "Asks to run ";
      const mono = bashCommand(s.ask) || text;
      return { text: lead + mono, amber: true, lead, mono };
    }
    const out = { text: why ? why + ": " + text : text, amber: true };
    if (s.need === "sso") out.view = "access";
    return out;
  }
  const v = s || {};
  return { text: deskDep("plainSummary")(v.summary) || deskDot(v).title || "", amber: false };
}

// bashCommand: the command of a pending Bash call, from the ask text the server sends
// ("Bash(<command>)", cut at askMax with a trailing "…" and no closing paren when long). The
// ask strip and the row's sub-line show it after "Asks to run", where "Bash(" and ")" are
// noise. Only the outer parenthesis goes, so $(…) and subshells stay whole. null for any
// other tool (the caller keeps "Tool(args)") and for an empty command: "Bash()" says more
// than a bare "Asks to run".
function bashCommand(ask) {
  const t = String(ask == null ? "" : ask);
  if (!t.startsWith("Bash(")) return null;
  let cmd = t.slice(5);
  if (cmd.endsWith(")")) cmd = cmd.slice(0, -1);
  return cmd.trim() === "" ? null : cmd;
}

// deskConnState: the connection state a row reads, as a connStateLabel key.
// "taken-over" is the page's own fact (the terminal tab here is parked because
// another tab took the session; the session record cannot say so), so the
// caller passes it as opts.takenOver; "detached" is the record's activity.
function deskConnState(s, opts) {
  if (opts && opts.takenOver) return "taken-over";
  if (!s) return "";
  if (s.activity === "detached") return "detached";
  // M8.6c: the row says what the pane bar says. "crashed" only when the server says so (host-tier exit).
  if (s.activity === "crashed") return "crashed";
  return s.exited ? "ended" : "";
}

// deskDot: the activity dot (class + title) on every desktop surface that draws
// one for a session: the sidebar row, the icon rail, the pane header and the
// command palette's session rows. It is
// sessionDot, except that a finished turn is the neutral idle dot titled "your
// turn" (F21-B: amber means blocked on you and nothing else), and that an exited
// session, which never asks, is not painted amber by a need it left behind. A
// detached or taken-over session keeps its dot's colour but its title is the
// connection word (F55, as rowSubline); a blocked one keeps the blocked title.
// The phone's dots stay on sessionDot.
function deskDot(s, opts) {
  const v = s || {};
  const a = deskDep("attentionClass")(v);
  const act = deskDep("activityClass");
  let dot;
  if (a === "done") dot = { cls: act("idle").cls, title: "your turn" };
  else if (!a && deskDep("needsYou")(v)) dot = act(v.activity);
  else dot = deskDep("sessionDot")(v);
  const conn = a === "block" ? "" : deskDep("connStateLabel")(deskConnState(v, opts));
  return conn ? { cls: dot.cls, title: conn } : dot;
}

// deskWriteChip: the one red chip, "aws <conn> · WRITE · 23m": which connection
// has a write window open and how long it has left. When several are open it
// names the count and shows the soonest to lapse, the one that ends first being
// the one a reader must act on. Only called while sessionElevated.
function deskWriteChip(s, nowMs, title) {
  const is = deskDep("isElevated");
  const conns = Array.isArray(s.aws_connections) ? s.aws_connections : [];
  let live = conns.filter((c) => is(c, nowMs));
  if (!live.length && is(s.aws, nowMs)) live = [s.aws];
  const left = Math.min(...live.map((c) => deskDep("minutesLeft")(c.elevated_until, nowMs)));
  const who = live.length > 1 ? "\u00d7" + live.length : live[0].label || live[0].name || "";
  return { text: "aws " + (who ? who + " " : "") + "\u00b7 WRITE \u00b7 " + (left < 1 ? "<1m" : left + "m"), tone: "write", title };
}

function deskBaseName(p) {
  return String(p || "").split("/").filter(Boolean).pop() || String(p || "");
}

// fmtSessionCost: a session's cost as the pane header shows it. Non-numbers
// (the server omits cost_usd unless the whole conversation history is known)
// give null. Sub-cent but non-zero is "<$0.01", exactly 0 is "$0.00", under $100
// two decimals, from $100 whole dollars. The boundary is decided on the value
// ROUNDED TO CENTS, so 99.996 (which toFixed(2) would print "$100.00") is "$100".
function fmtSessionCost(n) {
  if (typeof n !== "number" || !isFinite(n)) return null;
  if (n <= 0) return "$0.00";
  if (n < 0.01) return "<$0.01";
  if (Math.round(n * 100) >= 10000) return "$" + Math.round(n);
  return "$" + n.toFixed(2);
}

// headerSyncKey: what syncHeaderName compares to decide the pane header must be
// rebuilt on a poll. The header is gated (a rebuild per poll eats a press), so
// every polled value it paints outside tab.onUpdate has to be in this key: the
// session's name and its formatted cost (cost_usd moves on the 2m ingest with no
// other signal, e.g. on an idle session). The cost is formatted here, not taken
// from paneHeaderModel, so a narrow pane (which hides it) does not look stale.
// The attention word is in it too: a turn that finishes turns the header's dot
// from amber to neutral, with no other signal in the key. So is the write chip's
// text ("aws culipulse-prod · WRITE · 23m"): its minutes move with the clock, not
// with any field of the session, so the key takes `nowMs` (the real clock when a
// caller leaves it out) and the header repaints each minute, never per poll.
function headerSyncKey(name, session, nowMs = Date.now()) {
  const write = session && sessionElevated(session, nowMs) ? deskWriteChip(session, nowMs, "").text : "";
  return JSON.stringify([name || "", session ? fmtSessionCost(session.cost_usd) : null, session ? deskDep("attentionClass")(session) : "", write]);
}

// modelLabel: a model id as the pane header shows it, `haiku-4.5` (F13). Analytics'
// shortModel drops "claude-" and a trailing -YYYYMMDD; a "-N-M" version tail then
// reads N.M. The major is ONE digit and the minor one or two: "gpt-4-0613" is a build tag,
// not version 4.613, and a dated id ("gpt-4o-2024-08-06") ends in -MM-DD, not a 08.06
// version. Nothing to show (null, "") is null, where shortModel would say
// "(unknown)". The raw id belongs in the title.
function modelLabel(id) {
  if (!id) return null;
  return deskDep("shortModel")(String(id)).replace(/-(\d)-(\d{1,2})$/, "-$1.$2");
}

// paneHeaderModel: the pane header's one line, in fixed slot order: containment,
// cloud, model, ctx, cost. Below the narrow threshold only ONE chip survives (the
// write chip if there is one, else containment) and model/ctx/cost are dropped, so
// the name keeps the room. The name itself is never shortened here: CSS ellipsis
// does that, and a test pins that a long name passes through. The meta reads
// `haiku-4.5 · ctx 35% · $3.12`: model is the short label (modelTitle the raw id),
// ctx says what the percentage is.
function paneHeaderModel({ tab, session, nowMs, boxRuntime, narrow }) {
  if (tab && tab.kind === "viewer") {
    return { dot: null, elevated: false, name: deskBaseName(tab.path || tab.name), chips: [], model: null, modelTitle: null, ctx: null, cost: null };
  }
  const s = session || null;
  const name = (s && (s.name || s.id)) || (tab && tab.name) || "";
  const dot = s ? deskDot(s) : null;
  // The dot's red ring (the kit's .dot.is-elevated, as the sidebar row's dot): tabs.js adds the class while a window is open.
  // The header's rebuild key (headerSyncKey) moves with the same predicate, through the write chip's text.
  const elevated = sessionElevated(s, nowMs);
  const chips = [];
  const cont = s ? deskContainmentChip(s, boxRuntime) : null;
  if (cont) chips.push(cont);
  // Only an open write window is red; a read credential is neutral, and
  // whether it is production stays in the title.
  const parts = s ? deskDep("awsBadgeParts")(s, nowMs) : null;
  if (elevated) {
    chips.push(deskWriteChip(s, nowMs, parts ? parts.title : "AWS write window open"));
  } else if (parts) {
    chips.push({ text: parts.label, tone: "cloud", title: parts.title });
  }
  if (narrow) {
    // One chip at most: the write window if there is one, else containment. A
    // read credential is not worth the room.
    const w = chips.find((c) => c.tone === "write");
    return { dot, elevated, name, chips: w ? [w] : cont ? [cont] : [], model: null, modelTitle: null, ctx: null, cost: null };
  }
  const u = tab && typeof tab.usage === "function" ? tab.usage() : null;
  return {
    dot, elevated, name, chips,
    model: u ? modelLabel(u.model) : null,
    modelTitle: u && u.model ? String(u.model) : null,
    ctx: u && typeof u.pct === "number" ? "ctx " + u.pct + "%" : null,
    cost: s ? fmtSessionCost(s.cost_usd) : null,
  };
}

// metaUnitsOff: which units of the pane header's meta the wrap pushed off its one line
// (`tops`: each unit's top, `box`: the meta's {top, bottom}). They are clipped by the
// meta's height, and tabs.js marks them .mi-off (visibility: hidden), which takes them out
// of the tab order and out of what a screen reader announces. Off = starting at or below
// the line's bottom, with half a pixel of tolerance. A header with no layout (a detached
// pane, display: none) has no line, so nothing is off there.
function metaUnitsOff(tops, box) {
  if (!box || !(box.bottom - box.top > 0)) return tops.map(() => false);
  return tops.map((t) => t >= box.bottom - 0.5);
}

// headerDot: the pane header's dot. The session record's dot (paneHeaderModel's
// `dot`, i.e. deskDot) wins whenever the pane has one: a finished turn is neutral
// and an exited session is not painted amber by a need it left behind, whatever
// activity the terminal tab last had pushed to it. Only a pane whose session is
// not in the list (or whose list has not loaded) falls back to the tab's own
// pushed activity; a viewer tab has none.
function headerDot(model, tab) {
  if (model && model.dot) return model.dot;
  if (!tab || tab.kind !== "terminal" || typeof tab.activity !== "function") return null;
  return deskDep("activityClass")(tab.activity(), typeof tab.needWhy === "function" ? tab.needWhy() : "");
}

// drawerFor: the pane drawer's next state when the header's Files tab is
// pressed for `sessionId` (Activity and Access are pane views: paneview.js). The same kind for the same session closes it; any
// other press opens the asked kind on the asked session, so one drawer is open
// at a time and switching kinds or panes never takes two clicks.
function drawerFor(state, kind, sessionId) {
  const cur = state || { open: null, sessionId: null };
  if (cur.open === kind && cur.sessionId === sessionId) return { open: null, sessionId };
  return { open: kind, sessionId };
}

// needStripModel: the ask strip under a pane header, null unless the session
// asks for you. cardContent's text is the command for an ask, else the need's own
// sentence. tone is "block" for a session blocked on you (amber) and "done" for
// a finished turn (F21-B: neutral; the strip stays because it carries the reply
// field). An exited session has no strip: it never asks.
function needStripModel(s) {
  const tone = deskDep("attentionClass")(s);
  if (!tone || !deskDep("needsYou")(s)) return null;
  const c = deskDep("cardContent")(s);
  // A waiting session can have no named need; label it as the row's dot does.
  return { label: deskDep("needLabel")(s) || deskDep("sessionDot")(s).title || "waiting", text: c.text, tone };
}

// stripLabel: the strip's visible label, in sentence case. An ask reads "Asks to
// run" (spec 5.3); the other needs keep needStripModel's own words with the
// first letter raised ("Your turn", "Store locked"; "AWS sign-in" is untouched).
// The mapping lives here, not in needLabel: the mobile card shares that text.
function stripLabel(label) {
  if (label === "asks") return "Asks to run";
  const t = label || "waiting";
  return t.charAt(0).toUpperCase() + t.slice(1);
}

// stripKey: the repaint key of a pane's ask strip. It moves with everything the
// strip paints from the session (ask text, need, the full command, replyability,
// the sentence an SSO or turn need shows) and with the replies kill-switch, and
// NOT with the pane's active tab kind: a viewer tab becoming active must leave an
// expanded strip, or a half-typed reply, alone. "" when there is nothing to show.
function stripKey(s, activeKind, repliesEnabled) {
  void activeKind; // part of the signature on purpose: it must NOT matter
  if (!s) return "";
  return JSON.stringify([
    s.id, s.need || "", s.activity || "", deskDep("attentionClass")(s), s.ask || "", deskHash(s.ask_full), s.summary || "",
    !!s.replyable, !!s.claude, repliesEnabled === true,
    s.aws ? JSON.stringify(s.aws) : "", JSON.stringify(s.aws_connections || null),
    // The composer's gate (sendState): it turns true a few seconds after a
    // turn ends. Not in stripHoldKey: a disabled field cannot hold focus, and a
    // flap to false mid-typing must not tear the field down (the server refuses).
    s.reply_ready === true,
  ]);
}

// stripHoldKey: what replaces a strip even while its reply field has focus: a
// different session, need or ask, and anything that opens or closes the reply
// gate (the replies kill-switch, the session's replyable/claude flags). Every
// other stripKey input (summary, AWS connections, SSO state) waits until focus
// leaves, so a half-typed reply survives; a closed gate must always show closed.
function stripHoldKey(s, repliesEnabled) {
  if (!s) return "";
  return JSON.stringify([s.id, s.need || "", s.ask || "", deskHash(s.ask_full), !!s.replyable, !!s.claude, repliesEnabled === true]);
}

// deskHash: a cheap 32-bit string hash (FNV-1a) for keys; not for security. The
// answer guard is the sha256 sendAnswer sends.
function deskHash(str) {
  const t = String(str == null ? "" : str);
  let h = 0x811c9dc5;
  for (let i = 0; i < t.length; i++) { h ^= t.charCodeAt(i); h = Math.imul(h, 0x01000193) >>> 0; }
  return t.length + ":" + h.toString(16);
}

// emptyStateModel: which empty state the pane area shows. "first": nothing has
// ever been set up (no sessions, and every profile is one CuliBox built in: the
// server marks those source:"builtin"). A profile from the user's config.json
// ("config") or saved in the UI ("runtime") is the user's own, so it is never
// first run, and neither is a profile with no source. "none": anything else
// with no pane open (the caller only asks when that is so), including sessions
// that exist while the profiles are still loading: a blank pane would be worse
// than "No session open". null while the session list is unknown (null), or
// while profiles have not loaded and there are no sessions, so the first-run
// screen never flashes at someone with custom profiles or live sessions.
// profiles: up to three recently spawned profile names that still exist.
function emptyStateModel({ sessions, profiles, recent }) {
  const ps = Array.isArray(profiles) ? profiles : [];
  const have = new Set(ps.map((p) => p.name));
  const names = (Array.isArray(recent) ? recent : []).filter((n) => have.has(n)).slice(0, DESK_RECENT_MAX);
  const hasSessions = Array.isArray(sessions) && sessions.length > 0;
  // sessions == null: the list has not arrived. Saying "first" or "none" now
  // could be wrong either way, so say nothing.
  if (sessions == null) return { kind: null, profiles: names };
  let kind = "none";
  if (!hasSessions) {
    if (!ps.length) kind = null;
    else if (ps.every((p) => p.source === "builtin")) kind = "first";
  }
  return { kind, profiles: names };
}

// pushRecentProfile: the recent-profiles list after spawning with `name`.
function pushRecentProfile(list, name) {
  const rest = (Array.isArray(list) ? list : []).filter((n) => n !== name);
  return (name ? [name, ...rest] : rest).slice(0, DESK_RECENT_MAX);
}

const DESK_RECENT_KEY = "agent-box:recentProfiles";

// readRecent / writeRecent: the recent-profiles list in localStorage. Storage
// can be absent, full or blocked, and the list is a convenience, so neither
// ever throws: a bad read is an empty list, a failed write is dropped.
// deskStorage: localStorage, or null where merely reading the property throws
// (blocked site data): the identifier must not be evaluated at a call site.
function deskStorage() {
  try { return globalThis.localStorage || null; } catch (_) { return null; }
}
function readRecent(storage) {
  try {
    const v = JSON.parse(storage.getItem(DESK_RECENT_KEY) || "[]");
    return Array.isArray(v) ? v.filter((n) => typeof n === "string" && n).slice(0, DESK_RECENT_MAX) : [];
  } catch (_) {
    return [];
  }
}
function writeRecent(storage, list) {
  try {
    storage.setItem(DESK_RECENT_KEY, JSON.stringify(Array.isArray(list) ? list.slice(0, DESK_RECENT_MAX) : []));
  } catch (_) { /* a convenience, not state */ }
}

// deskWsHeaderSig: the repaint key of a group header. The dots move it only
// while they are drawn (collapsed): a rebuild under the pointer eats a click
// (renderRail48's note), so an open group's header must not churn with a dot.
function deskWsHeaderSig(it) {
  return JSON.stringify(["wsh", it.key, it.label, it.path, it.count, it.waiting, !!it.collapsed, it.collapsed ? it.dots : []]);
}

const DESK_WS_COLLAPSED_KEY = "agent-box:wsCollapsed";
const DESK_WS_COLLAPSED_CAP = 500;

// wsCollapsedRead / wsCollapsedWrite: the collapsed groups' keys in localStorage,
// {"v":1,"keys":[...]}. Like the recent list it is a convenience: a wrong
// version, bad JSON or a blocked storage reads as nothing collapsed, a failed
// write is dropped. A read keeps the newest CAP keys, as treestate.js does.
function wsCollapsedRead(storage) {
  try {
    const v = JSON.parse(storage.getItem(DESK_WS_COLLAPSED_KEY));
    if (!v || v.v !== 1 || !Array.isArray(v.keys)) return new Set();
    return new Set(v.keys.filter((k) => typeof k === "string").slice(-DESK_WS_COLLAPSED_CAP));
  } catch (_) {
    return new Set();
  }
}
function wsCollapsedWrite(storage, set) {
  try {
    storage.setItem(DESK_WS_COLLAPSED_KEY, JSON.stringify({ v: 1, keys: [...set] }));
  } catch (_) { /* a convenience, not state */ }
}

// wsCollapsedToggle: flips `key` in the set (it is mutated) and says whether it
// is now collapsed. A new key evicts the oldest past the cap (insertion order,
// as treestate.js addExpanded). "" (Other) is a valid key.
function wsCollapsedToggle(set, key) {
  if (set.delete(key)) return false;
  set.add(key);
  if (set.size > DESK_WS_COLLAPSED_CAP) set.delete(set.values().next().value);
  return true;
}

// bootFlags: what boot reads from the legacy localStorage keys, decided once so
// the desktop rules are testable. The keys predate the desktop shell and are not
// renamed (spec 7): on desktop the rail is never shown (Access is a pane view;
// never opened from a stored "1"), the pane collapse hid panes that no longer exist as such,
// and the sessions/files split has no splitter. Mobile keeps every stored value.
// A blocked or absent storage reads as defaults; the two "ignore" flags depend on
// the layout alone.
function bootFlags(storage, isMobile) {
  const get = (k) => { try { return storage ? storage.getItem(k) : null; } catch (_) { return null; } };
  return {
    sidebarHidden: get("sidebarHidden") === "1",
    openAccess: !!isMobile && get("agent-box:steerRail") === "1",
    ignoreCollapsed: !isMobile,
    ignoreSplit: !isMobile,
  };
}

// paneTitle: the tooltip of the pane header's name. The cwd belongs to a terminal
// tab; a viewer tab shows a file, and the cell's session cwd would mislabel it.
function paneTitle(name, tab, session, tabCount) {
  const cwd = tab && tab.kind === "terminal" && session && session.cwd ? " \u00B7 " + session.cwd : "";
  return name + cwd + (tabCount > 1 ? " (" + tabCount + " tabs in this pane)" : "");
}

// paneTabs: the pane header's view tabs, in the order they show. `label` is the text
// of a wide header, `icon` the glyph tabIcon draws in its place when the pane is
// too narrow for the words (#13); the button keeps both, so CSS alone picks. A
// terminal pane has three views, Terminal, Activity and Access, and Files is NOT a
// tab of them: it opens a drawer over the pane, so it is a toggle button after the
// tabs (paneFilesToggle) and exactly one tab looks selected (G6). A pane of files has
// no session to describe: Files alone as its one tab, no Activity, no Access.
function paneTabs(hasTerm) {
  if (!hasTerm) return [paneFilesToggle()];
  return [
    { id: "terminal", label: "Terminal", icon: "terminal" },
    { id: "activity", label: "Activity", icon: "activity" },
    { id: "access", label: "Access", icon: "access" },
  ];
}

// paneFilesToggle: the Files control's one definition: the toggle after a terminal
// pane's tabs, and the one tab of a pane of files.
function paneFilesToggle() {
  return { id: "files", label: "Files", icon: "files" };
}

// paneTabsClass: the class of the tab group. A group of one tab (a pane of files) is
// marked `one`: the narrow-pane rule that hides the tabs (desktop.css) leaves it
// alone, since one 24px icon fits where four do not (#123).
function paneTabsClass(defs) {
  return defs && defs.length === 1 ? "ph-tabs one" : "ph-tabs";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deskWsLabels, deskWsGroups, deskWsHeaderSig, wsCollapsedRead, wsCollapsedWrite, wsCollapsedToggle, DESK_WS_COLLAPSED_KEY, DESK_WS_COLLAPSED_CAP, deskGroups, deskOrder, jumpOrder, nextNeedsYou, deskConnState, deskDot, headerDot, sessionElevated, elevatedCount, costTodayRange, rowChip, deskRowKind, tabSwitchFacts, DESK_CLONE_TITLE, rowSubline, bashCommand,
    paneHeaderModel, fmtSessionCost, headerSyncKey, drawerFor, needStripModel, stripLabel, stripKey, stripHoldKey, emptyStateModel, pushRecentProfile, readRecent, writeRecent, deskStorage, bootFlags, paneTitle, paneTabs, paneFilesToggle, paneTabsClass, modelLabel, metaUnitsOff };
}
