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
    connSSOSources: typeof connSSOSources === "function" ? connSSOSources : null,
    sessionDot: typeof sessionDot === "function" ? sessionDot : null,
    tierBadge: typeof tierBadge === "function" ? tierBadge : null,
    isElevated: typeof isElevated === "function" ? isElevated : null,
    minutesLeft: typeof minutesLeft === "function" ? minutesLeft : null,
    awsBadgeParts: typeof awsBadgeParts === "function" ? awsBadgeParts : null,
    isNestedShell: typeof isNestedShell === "function" ? isNestedShell : null,
  };
  if (g[name]) return g[name];
  const mod = { sessionGroups: "./mobileshell.js", needsYou: "./attention.js", cardContent: "./attention.js",
    needLabel: "./attention.js", connSSOSources: "./attention.js", sessionDot: "./attention.js", tierBadge: "./tier.js",
    isElevated: "./sessionrow.js", minutesLeft: "./sessionrow.js", awsBadgeParts: "./sessionrow.js",
    isNestedShell: "./shellnest.js" }[name];
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

// jumpOrder: the list Alt+1..9 and the palette's first nine index into. On
// desktop that is the sidebar's visible order; mobile has no such list on
// screen and keeps the API order. Nested shells are left out of both: the
// numbers (and the rail dots) are for agents.
function jumpOrder(sessions, mobile) {
  const nested = deskDep("isNestedShell");
  const list = sessions || [];
  const agents = list.some((s) => nested(s, list)) ? list.filter((s) => !nested(s, list)) : list;
  return mobile ? agents : deskOrder(deskGroups(agents));
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
// (No idle duration: the session record carries none.) A sign-in line carries
// view: "access": a click opens the session's Access view, where sign-in lives.
function rowSubline(s) {
  if (deskDep("needsYou")(s)) {
    // A named connection's expired sign-in on an agent that is not itself
    // waiting has no needLabel and would read "Waiting for input.", which says
    // nothing about the sign-in.
    if (!deskDep("needLabel")(s) && s.activity !== "waiting" && deskDep("connSSOSources")(s).length) {
      return { text: "SSO expired \u00b7 Sign in", amber: true, view: "access" };
    }
    // A waiting session can have no named need (needLabel ""): no dangling ": ".
    const why = deskDep("needLabel")(s);
    const text = deskDep("cardContent")(s).text;
    const out = { text: why ? why + ": " + text : text, amber: true };
    if (s.need === "sso") out.view = "access";
    return out;
  }
  const v = s || {};
  return { text: v.summary || deskDep("sessionDot")(v).title || "", amber: false };
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

// fmtSessionCost: a session's cost as the row and header show it. Non-numbers
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
function headerSyncKey(name, session) {
  return JSON.stringify([name || "", session ? fmtSessionCost(session.cost_usd) : null]);
}

// paneHeaderModel: the pane header's one line, in fixed slot order: containment,
// cloud, model, ctx, cost. Below the narrow threshold only ONE chip survives (the
// write chip if there is one, else containment) and model/ctx/cost are dropped, so
// the name keeps the room. The name itself is never shortened here: CSS ellipsis
// does that, and a test pins that a long name passes through.
function paneHeaderModel({ tab, session, nowMs, boxRuntime, narrow }) {
  if (tab && tab.kind === "viewer") {
    return { dot: null, name: deskBaseName(tab.path || tab.name), chips: [], model: null, ctx: null, cost: null };
  }
  const s = session || null;
  const name = (s && (s.name || s.id)) || (tab && tab.name) || "";
  const dot = s ? deskDep("sessionDot")(s) : null;
  const chips = [];
  const cont = s ? deskContainmentChip(s, boxRuntime) : null;
  if (cont) chips.push(cont);
  // Only an open write window is red; a read credential is neutral, and
  // whether it is production stays in the title.
  const parts = s ? deskDep("awsBadgeParts")(s, nowMs) : null;
  if (sessionElevated(s, nowMs)) {
    chips.push(deskWriteChip(s, nowMs, parts ? parts.title : "AWS write window open"));
  } else if (parts) {
    chips.push({ text: parts.label, tone: "cloud", title: parts.title });
  }
  if (narrow) {
    // One chip at most: the write window if there is one, else containment. A
    // read credential is not worth the room.
    const w = chips.find((c) => c.tone === "write");
    return { dot, name, chips: w ? [w] : cont ? [cont] : [], model: null, ctx: null, cost: null };
  }
  const u = tab && typeof tab.usage === "function" ? tab.usage() : null;
  return {
    dot, name, chips,
    model: u && u.model ? String(u.model) : null,
    ctx: u && typeof u.pct === "number" ? u.pct + "%" : null,
    cost: s ? fmtSessionCost(s.cost_usd) : null,
  };
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
// needs you. cardContent's text is the command for an ask, else the need's own sentence.
function needStripModel(s) {
  if (!deskDep("needsYou")(s)) return null;
  const c = deskDep("cardContent")(s);
  // A waiting session can have no named need; label it as the row's dot does.
  return { label: deskDep("needLabel")(s) || deskDep("sessionDot")(s).title || "waiting", text: c.text };
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
    s.id, s.need || "", s.activity || "", s.ask || "", deskHash(s.ask_full), s.summary || "",
    !!s.replyable, !!s.claude, repliesEnabled === true,
    s.aws ? JSON.stringify(s.aws) : "", JSON.stringify(s.aws_connections || null),
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
// ever been set up (no sessions and only built-in profiles, which the server
// marks editable:false). "none": anything else with no pane open (the caller
// only asks when that is so), including sessions that exist while the profiles
// are still loading: a blank pane would be worse than "No session open".
// null while the session list is unknown (null), or while profiles have not
// loaded and there are no sessions, so the first-run screen never flashes at
// someone with custom profiles or live sessions. profiles: up
// to three recently spawned profile names that still exist.
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
    else if (ps.every((p) => p.builtin === true || !p.editable)) kind = "first";
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

// shortcutRows: what the "Keyboard shortcuts" dialog lists. Every row names a
// binding the code really installs (palette.js, app.js, terminal-tab.js,
// viewer-tab.js, spawnform.js); the last row is the box runtime. The runtime
// label mirrors tierRuntimeLabel (tier.js), which this file cannot reach in node.
function shortcutRows(isMac, boxRuntime) {
  const rt = boxRuntime === "sysbox-runc" ? "sysbox" : boxRuntime || "runc";
  const alt = isMac ? "⌥" : "Alt+";
  const mod = isMac ? "⌘" : "Ctrl+";
  return [
    { keys: isMac ? "⌘K" : "Ctrl+Shift+K", action: "Command palette" },
    { keys: "Ctrl+Shift+Space", action: "Command palette" },
    { keys: isMac ? "⌘Enter" : "Ctrl+Enter", action: "Palette: open in new pane / insert file path" },
    { keys: alt + "1…9", action: "Jump to session" },
    { keys: "Ctrl+Shift+`", action: "+ Shell below" },
    { keys: isMac ? "⌘B" : "Ctrl+Shift+B", action: "Toggle sidebar / rail" },
    { keys: isMac ? "⌥⌘S" : "Ctrl+Alt+S", action: "Toggle sidebar / rail" },
    { keys: isMac ? "⌥⌘F" : "Ctrl+Alt+F", action: "Files drawer" },
    { keys: (isMac ? "⌘" : "Ctrl") + " + / - / 0", action: "Zoom in / out / reset" },
    { keys: isMac ? "⌘C" : "Ctrl+Shift+C", action: "Terminal: copy selection" },
    { keys: isMac ? "⌘V" : "Ctrl+V", action: "Terminal: paste" },
    { keys: isMac ? "⌘S" : "Ctrl+S", action: "Editor: save" },
    { keys: isMac ? "⌘Enter" : "Ctrl+Enter", action: "New session: spawn" },
    { keys: "Runtime", action: rt },
  ];
}

// shortcutText: the dialog body, one line per row, keys padded to one column
// (the dialog sets it in monospace).
function shortcutText(rows) {
  const list = Array.isArray(rows) ? rows : [];
  const keyed = list.filter((r) => r.keys !== "Runtime");
  const w = Math.max(0, ...keyed.map((r) => r.keys.length));
  const body = keyed.map((r) => r.keys.padEnd(w) + "  " + r.action);
  const rt = list.find((r) => r.keys === "Runtime");
  return rt ? body.join("\n") + "\n\nRuntime: " + rt.action : body.join("\n");
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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deskGroups, deskOrder, jumpOrder, sessionElevated, elevatedCount, costTodayRange, rowChip, rowSubline,
    paneHeaderModel, fmtSessionCost, headerSyncKey, drawerFor, needStripModel, stripLabel, stripKey, stripHoldKey, emptyStateModel, pushRecentProfile, readRecent, writeRecent, deskStorage, shortcutRows, shortcutText, bootFlags, paneTitle };
}
