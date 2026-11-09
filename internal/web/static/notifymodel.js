"use strict";

// The OS-notification rule table (#177): which notification a poll of /api/sessions produces,
// as plain data. No DOM, no Notification, no storage, no clock: app.js reads the permission,
// the focus and the prefs, hands them in through ctx, and constructs the notifications.
//
//   notifyStep(prev, sessions, ctx) -> { next, notes }
//     prev      null on the first poll after load (it only seeds), else the previous `next`
//     sessions  the /api/sessions list
//     ctx       { now: ms, focused: bool, prefs: {master, block, done, ended, message},
//                 endedByMe: Set|Array of session ids this client ended or restarted }
//     next      { block: Set<causeKey>, done: Set<sid>, live: Map<sid,{ended, crashed, name}>,
//                 away: Map<key, ms> }   (the state the next poll starts from)
//     notes     [{ title, body, tag, options, sid? }]; options carries the tag too, and
//               sid is the session to open on click (absent for a batch)
//
// State advances on every poll, even when nothing may be shown (tab focused, class switched
// off, master off), so turning a switch on never replays old events.
//
// Blocked causes are counted by cause, not by session: one per asking session, one per AWS
// sign-in source, one for the secret store. Bodies are fixed text: never a command, a path or
// the summary; only Done, with the "message" pref on, shows plainSummary text.

// attention.js is a classic script whose functions are globals in the browser; under node
// they are required. Resolved at call time so load order never matters (same pattern as
// deskshell.js's deskDep).
function nmDep(name) {
  const g = {
    attentionClass: typeof attentionClass === "function" ? attentionClass : null,
    connSSOSources: typeof connSSOSources === "function" ? connSSOSources : null,
    plainSummary: typeof plainSummary === "function" ? plainSummary : null,
    NEEDS_REARM_MS: typeof NEEDS_REARM_MS === "number" ? NEEDS_REARM_MS : null,
  };
  if (g[name] !== null && g[name] !== undefined) return g[name];
  return require("./attention.js")[name];
}

const NOTIFY_PREF_DEFAULTS = { master: true, block: true, done: true, ended: true, message: false, chime: false };
const NOTIFY_BATCH_MIN = 3;
const NOTIFY_TOOL_MAX = 40;

// notifyClassOf: "block" | "done" | "" (exited sessions are "").
function notifyClassOf(s) {
  return nmDep("attentionClass")(s);
}

// askTool: the tool name of an ask, never a command, path or arguments. The text before the first
// "(" (or the leading word when there is none), trimmed, must look like a tool name; else "a tool".
function askTool(ask) {
  const a = typeof ask === "string" ? ask : "";
  const cut = a.indexOf("(");
  const t = cut >= 0 ? a.slice(0, cut).trim() : (a.trim().split(/\s+/)[0] || "");
  return /^[A-Za-z][\w.-]*$/.test(t) ? t.slice(0, NOTIFY_TOOL_MAX) : "a tool";
}

function nmSSOSources(s) {
  const out = nmDep("connSSOSources")(s).slice();
  const legacy = s && s.aws && s.aws.sso_expired;
  if (legacy && !out.includes(legacy)) out.push(legacy);
  return out.sort();
}

// causeKeys: the notification keys a session contributes, which are also the tags: ab:<sid>:block,
// ab:sso:<source> (one per sign-in source), ab:store (one per instance), ab:<sid>:done.
function causeKeys(s) {
  const cls = notifyClassOf(s);
  if (cls === "done") return ["ab:" + s.id + ":done"];
  if (cls !== "block") return [];
  const keys = [];
  if (s.need === "ask") keys.push("ab:" + s.id + ":block");
  const src = nmSSOSources(s);
  for (const x of src) keys.push("ab:sso:" + x);
  if (s.need === "sso" && !src.length) keys.push("ab:sso:" + s.id);
  if (s.need === "store_locked") keys.push("ab:store");
  if (!keys.length) keys.push("ab:" + s.id + ":block");
  return keys;
}

function nmPlural(n, one, many) {
  return n + " " + (n === 1 ? one : many);
}

// notifyBodyFor: the fixed OS body for a cause.
//   cause { kind: "ask"|"wait"|"sso"|"store"|"done"|"end", sessions: [session], crashed? }
function notifyBodyFor(cause, prefs) {
  const ss = cause.sessions || [];
  const s = ss[0] || {};
  switch (cause.kind) {
    case "ask": return "Wants to run " + askTool(s.ask);
    case "sso": return ss.length > 1 ? "AWS sign-in expired · " + ss.length + " sessions" : "AWS sign-in expired";
    case "store": return "Secret store locked: " + nmPlural(ss.length, "session", "sessions") + " can't fetch credentials";
    case "done": {
      if (prefs && prefs.message) {
        const t = nmDep("plainSummary")(s.summary);
        if (t) return t;
      }
      return "Finished its turn";
    }
    case "end": return cause.crashed ? "Ended (crashed)" : "Ended";
    default: return "Waiting for you";
  }
}

function nmCauseKind(key, s) {
  if (key === "ab:store") return "store";
  if (key.indexOf("ab:sso:") === 0) return "sso";
  if (/:done$/.test(key)) return "done";
  return s.need === "ask" ? "ask" : "wait";
}

function nmIsEnded(s) {
  return !!(s.exited || s.status === "exited" || s.activity === "crashed");
}

function nmInSet(c, id) {
  if (!c) return false;
  return typeof c.has === "function" ? c.has(id) : Array.isArray(c) && c.includes(id);
}

function notifyStep(prev, sessions, ctx) {
  // A failed fetch hands over no list: nothing changed, so nothing ended (R8a).
  if (!Array.isArray(sessions)) return { next: prev, notes: [] };
  const c = ctx || {};
  const prefs = Object.assign({}, NOTIFY_PREF_DEFAULTS, c.prefs || {});
  const now = typeof c.now === "number" ? c.now : 0;
  const list = sessions;
  const rearm = nmDep("NEEDS_REARM_MS");

  // This poll's causes, grouped by key.
  const causes = new Map();
  for (const s of list) {
    for (const key of causeKeys(s)) {
      let cz = causes.get(key);
      if (!cz) {
        cz = { key, kind: nmCauseKind(key, s), sessions: [], pty: false };
        causes.set(key, cz);
      }
      cz.sessions.push(s);
      // Only a per-session cause on a PTY-silence session can blip; shared causes come from
      // server fields and the agent's own status has no blips.
      if ((cz.kind === "ask" || cz.kind === "wait" || cz.kind === "done") && s.activity_source === "pty") cz.pty = true;
    }
  }

  const next = { block: new Set(), done: new Set(), live: new Map(), away: new Map() };
  const fresh = []; // causes that start now and may be announced
  for (const cz of causes.values()) {
    const wasSet = cz.kind === "done" ? prev && prev.done.has(cz.sessions[0].id) : prev && prev.block.has(cz.key);
    (cz.kind === "done" ? next.done : next.block).add(cz.kind === "done" ? cz.sessions[0].id : cz.key);
    if (!prev || wasSet) continue;
    const left = prev.away.get(cz.key);
    if (cz.pty && left !== undefined && now - left < rearm) continue;
    fresh.push(cz);
  }
  // Keys that left this poll start (or keep) their re-arm clock.
  if (prev) {
    const gone = [];
    for (const id of prev.block) if (!causes.has(id)) gone.push(id);
    for (const id of prev.done) if (!causes.has("ab:" + id + ":done")) gone.push("ab:" + id + ":done");
    for (const k of gone) next.away.set(k, now);
    for (const [k, t] of prev.away) if (!causes.has(k) && !next.away.has(k)) next.away.set(k, t);
  }

  // Re-arm entries past the window can no longer suppress anything: drop them (R8b).
  for (const [k, t] of next.away) if (now - t >= rearm) next.away.delete(k);

  // Ended: live -> exited/crashed, or vanished, unless this client ended or restarted it.
  const ended = [];
  for (const s of list) {
    const e = nmIsEnded(s);
    next.live.set(s.id, { ended: e, crashed: s.activity === "crashed", name: s.name || s.id });
    const was = prev && prev.live.get(s.id);
    if (was && !was.ended && e && !nmInSet(c.endedByMe, s.id)) ended.push({ id: s.id, name: s.name || s.id, crashed: s.activity === "crashed" });
  }
  if (prev) {
    for (const [id, was] of prev.live) {
      if (!was.ended && !list.some((s) => s.id === id) && !nmInSet(c.endedByMe, id)) ended.push({ id, name: was.name, crashed: false });
    }
  }

  const notes = [];
  if (!prev || c.focused || !prefs.master) return { next, notes };

  if (prefs.block) {
    for (const cz of fresh) {
      if (cz.kind === "done") continue;
      const title = cz.kind === "store" || (cz.kind === "sso" && cz.sessions.length > 1) ? "CuliBox" : cz.sessions[0].name || cz.sessions[0].id;
      notes.push({
        title,
        body: notifyBodyFor(cz, prefs),
        tag: cz.key,
        sid: cz.sessions[0].id,
        options: { tag: cz.key, renotify: true, requireInteraction: true, silent: false },
      });
    }
  }
  if (prefs.done) {
    const dones = fresh.filter((cz) => cz.kind === "done");
    if (dones.length >= NOTIFY_BATCH_MIN) {
      notes.push({
        title: "CuliBox",
        body: dones.length + " sessions finished",
        tag: "ab:done-batch",
        options: { tag: "ab:done-batch", silent: true },
      });
    } else {
      for (const cz of dones) {
        const s = cz.sessions[0];
        notes.push({ title: s.name || s.id, body: notifyBodyFor(cz, prefs), tag: cz.key, sid: s.id, options: { tag: cz.key, silent: true } });
      }
    }
  }
  if (prefs.ended) {
    for (const e of ended) {
      const tag = "ab:" + e.id + ":end";
      notes.push({ title: e.name, body: notifyBodyFor({ kind: "end", crashed: e.crashed }, prefs), tag, sid: e.id, options: { tag, silent: false } });
    }
  }
  return { next, notes };
}

// ---- Tab title and favicon (F46, DD§F, DC§2) ----
//
// titleState(sessions, sys, env, now) -> { title, icon }
//   sys   { lastOkAt, failSince, signedOut }: the client's view of the sessions poll (app.js `sys`)
//   env   the instance's env label ("PROD"), "" for none
//   icon  "normal" | "amber" | "neutral" | "offline", plus "-ring" while a write window is open
// The title carries the number and the check, the icon has no digit. Blocked causes are counted
// by cause (the store once, each sign-in source once), never by session; a finished turn gives
// "✓N" only when nothing is blocked. Offline drops the stale count: a 401 at once, a network
// failure once no poll has succeeded for SYS_OFFLINE_MS.
const SYS_OFFLINE_MS = 12000;

function nmOffline(sys, now) {
  if (!sys) return false;
  if (sys.signedOut) return true;
  if (sys.failSince == null) return false;
  const since = sys.lastOkAt != null ? sys.lastOkAt : sys.failSince;
  return now - since >= SYS_OFFLINE_MS;
}

function nmWindowOpen(s, now) {
  const open = (a) => !!a && !!a.elevated_until && Date.parse(a.elevated_until) > now;
  if (open(s.aws)) return true;
  return Array.isArray(s.aws_connections) && s.aws_connections.some(open);
}

// blockedCauses: the distinct blocked causes in a session list, in the order the list is given (the
// sidebar's order, for the chip): one per asking or waiting session, one per AWS sign-in source, one for the
// secret store. Done and exited sessions contribute none. Shared by the tab title (its count) and the
// attention chip, so the two agree, with two exceptions: a generic "wait" cause counts in the title but gets no
// chip (R13: it has no designed copy), and nested shells are not in the chip's sidebar-ordered list. Each cause: { key, kind, sessions, pty }.
function blockedCauses(sessions) {
  const out = [];
  const by = new Map();
  for (const s of Array.isArray(sessions) ? sessions : []) {
    if (!s || s.exited || s.status === "exited") continue;
    for (const key of causeKeys(s)) {
      if (/:done$/.test(key)) continue;
      let c = by.get(key);
      if (!c) {
        c = { key, kind: nmCauseKind(key, s), sessions: [], pty: false };
        by.set(key, c);
        out.push(c);
      }
      c.sessions.push(s);
      if ((c.kind === "ask" || c.kind === "wait") && s.activity_source === "pty") c.pty = true;
    }
  }
  return out;
}

function titleState(sessions, sys, env, now) {
  const base = (env ? "[" + env + "] " : "") + "CuliBox";
  if (nmOffline(sys, now)) {
    // The last-known list (the last OK poll): a write window still open on it keeps its ring
    // around the hollow dot; once its deadline passes the ring goes (R9).
    const open = (Array.isArray(sessions) ? sessions : []).some((s) => s && !s.exited && s.status !== "exited" && nmWindowOpen(s, now));
    return { title: "Offline \u00b7 " + base, icon: open ? "offline-ring" : "offline" };
  }
  const blocked = blockedCauses(sessions);
  let done = 0, ring = false;
  for (const s of Array.isArray(sessions) ? sessions : []) {
    if (!s || s.exited || s.status === "exited") continue;
    if (nmWindowOpen(s, now)) ring = true;
    for (const k of causeKeys(s)) if (/:done$/.test(k)) done++;
  }
  let title = base, icon = "normal";
  if (blocked.length) { title = "(" + blocked.length + ") " + base; icon = "amber"; }
  else if (done) { title = "\u2713" + done + " " + base; icon = "neutral"; }
  return { title, icon: ring ? icon + "-ring" : icon };
}

// ---- The system banner and the rail marker (DD§A8 M8.6/6b/6c, §B1; R19) ----
//
// sysBannerModel(sys, now, fmt) -> null, or the ONE system banner the sidebar shows:
//   { kind, key, title, body, dot, stale, dismiss, actions: [{id, label, primary}] }
// sys is app.js's `sys` record (lastOkAt, failSince, signedOut, paused, bootVersion, version,
// dismissedVersion); fmt turns a raw version into its short label (versionLabel). Priority: signed out >
// unreachable > paused > updated. Unreachable follows nmOffline (12 s without an OK poll), signed out is
// immediate, paused is set by the page after a hidden period and cleared by the next OK poll. Ages come from
// the client's lastOkAt only, never a server time. `stale` says the sidebar rows are the last-known list
// (hollow dots, muted names); an update or a pause does not stale them. `key` moves with the kind and the
// version, not with the ticking age, so the painter can leave the DOM alone between polls.
function nmAge(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return s + " s";
  if (s < 3600) return Math.floor(s / 60) + " min";
  return Math.floor(s / 3600) + " h";
}

function sysBannerModel(sys, now, fmt) {
  if (!sys) return null;
  const f = typeof fmt === "function" ? fmt : (v) => v;
  if (sys.signedOut) {
    return { kind: "signedout", key: "sys:signedout", title: "Signed out",
      body: "Your login expired. Sessions keep running on the server.", dot: true, stale: true, dismiss: false,
      actions: [{ id: "signin", label: "Sign in again", primary: true }] };
  }
  if (nmOffline(sys, now)) {
    const age = sys.lastOkAt != null ? " Last update " + nmAge(now - sys.lastOkAt) + " ago; the list below is from then." : "";
    return { kind: "unreachable", key: "sys:unreachable", title: "Can\u2019t reach CuliBox",
      body: "Retrying every 3 s." + age, dot: true, stale: true, dismiss: false,
      actions: [{ id: "retry", label: "Retry now", primary: false }] };
  }
  if (sys.paused) {
    return { kind: "paused", key: "sys:paused", title: "Paused while this tab was hidden \u00b7 updating\u2026",
      body: "", dot: true, stale: false, dismiss: false, actions: [] };
  }
  if (sys.bootVersion && sys.version && sys.version !== sys.bootVersion && sys.version !== sys.dismissedVersion) {
    return { kind: "updated", key: "sys:updated:" + sys.version, title: "CuliBox was updated to " + f(sys.version),
      body: "This tab still runs " + f(sys.bootVersion) + ". Reload when you are not typing.", dot: false, stale: false,
      dismiss: true, actions: [{ id: "reload", label: "Reload", primary: false }] };
  }
  return null;
}

// railMarkerModel: the collapsed rail has room for ONE marker (R19), for the first banner in the sidebar's
// display order: the system banner if there is one, else the store's. Its title is the banner's title (the
// button's accessible name and tooltip). Neutral for a system banner, amber for the store (a blocked cause).
// storeBanner is Task 9's input: anything with a `title`, or null.
function railMarkerModel(sysBanner, storeBanner) {
  if (sysBanner) return { title: sysBanner.title, tone: "neutral", key: sysBanner.key };
  if (storeBanner && storeBanner.title) return { title: storeBanner.title, tone: "amber", key: "store" };
  return null;
}

// ---- The attention chip and the chime (DD§A6, §B3; DC§5) ----
// attnChipModel(ordered, visibleIds, dismissedKey) -> null, or what the chip says. `ordered` is the session
// list in the sidebar's order (the caller's, not re-sorted here); `visibleIds` the session ids on a shown
// pane (Set or array; empty while a page hides the grid). A cause is off-screen when none of its sessions
// is visible. The chip names the first off-screen cause and counts the others as "+N". Done never shows.
// dismissKey is the sorted key set of ALL blocked causes: the chip stays hidden for the key the user
// closed it on and returns when the set changes.
function nmChipText(c) {
  const s = c.sessions[0];
  const name = s.name || s.id;
  switch (c.kind) {
    case "ask": return { name, rest: " wants to run " + askTool(s.ask) };
    case "sso":
      return c.sessions.length > 1
        ? { name: "", rest: "AWS sign-in expired \u00b7 " + c.sessions.length + " sessions" }
        : { name, rest: " needs AWS sign-in" };
    case "store": return { name: "", rest: "Secret store locked \u00b7 " + nmPlural(c.sessions.length, "session", "sessions") };
    default: return null; // no designed copy (a generic wait, R13): no chip
  }
}

function nmOffScreen(c, visibleIds) {
  return !c.sessions.some((s) => nmInSet(visibleIds, s.id));
}

// Only a cause with chip copy is eligible (ask, SSO, store); a generic wait (R13) is neither named, counted in +N,
// nor part of the dismiss key, so starting one never brings back a closed chip.
function nmChipCauses(ordered) {
  return blockedCauses(ordered).filter((c) => nmChipText(c) !== null);
}

// chipDismissKey: the sorted keys of the chip-eligible blocked causes. The caller clears a stored dismissal
// whenever this differs from it, on every sync, even when no chip is due (all causes on screen, or none).
function chipDismissKey(ordered) {
  return nmChipCauses(ordered).map((c) => c.key).sort().join("|");
}

function attnChipModel(ordered, visibleIds, dismissedKey) {
  const causes = nmChipCauses(ordered);
  const off = causes.filter((c) => nmOffScreen(c, visibleIds));
  if (!off.length) return null;
  const dismissKey = chipDismissKey(ordered);
  if (dismissedKey && dismissedKey === dismissKey) return null;
  const t = nmChipText(off[0]);
  const more = off.length - 1;
  const rest = t.rest + (more ? " \u00b7 +" + more : "");
  return { name: t.name, rest, text: t.name + rest, more, key: t.name + "\u0001" + rest, dismissKey, sid: off[0].sessions.find((x) => !nmInSet(visibleIds, x.id)).id };
}

// chimeStep(prev, causes, visibleIds, ctx) -> { next, play }: the pure decision behind the optional blip
// for Blocked. `causes` is blockedCauses(list); ctx = { now, focused, on }. A cause that is new since the
// last call is chime-worthy when its session is on no visible pane OR the tab is unfocused. With `on`
// false nothing plays, but the state still advances, so switching the chime on later never replays old
// causes. The first call (prev null) only seeds. A PTY-silence cause that comes back within the re-arm
// window is a blip, not a new cause (the same rule as notifyStep).
function chimeStep(prev, causes, visibleIds, ctx) {
  const c = ctx || {};
  const now = typeof c.now === "number" ? c.now : 0;
  const rearm = nmDep("NEEDS_REARM_MS");
  const list = Array.isArray(causes) ? causes : [];
  const keys = new Set(list.map((x) => x.key));
  const away = new Map();
  let play = false;
  if (prev) {
    for (const k of prev.keys) if (!keys.has(k)) away.set(k, now);
    for (const [k, t] of prev.away) if (!keys.has(k) && !away.has(k) && now - t < rearm) away.set(k, t);
    for (const cz of list) {
      if (prev.keys.has(cz.key)) continue;
      const left = prev.away.get(cz.key);
      if (cz.pty && left !== undefined && now - left < rearm) continue;
      if (c.on && (!c.focused || nmOffScreen(cz, visibleIds))) play = true;
    }
  }
  return { next: { keys, away }, play };
}

// faviconSvg(icon) -> a data:image/svg+xml URI. Static favicons sit behind auth (E6), so each
// state is built here in kit hex (DD§F2: a standalone SVG cannot read the page's variables).
const FAV_BG = '<rect width="16" height="16" rx="3.5" style="fill:#0d0f12;stroke:#23282f"/>';
const FAV_DOOR = "M11 5V3a2 2 0 0 0-2-2H3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V7";
const FAV_OPEN = "M11 3.5V3a2 2 0 0 0-2-2H3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2V8.5";
function favPath(d, stroke) {
  return '<path d="' + d + '" fill="none" style="stroke:' + stroke + '" stroke-width="2"/>';
}
function favDot(fill) {
  return '<rect x="9" y="3.5" width="5" height="5" rx="2.5" style="fill:' + fill + '"/>';
}
function favRing(cx) {
  return '<circle cx="' + cx + '" cy="6" r="3.9" style="fill:none;stroke:#f06a6a;stroke-width:1.3"/>';
}
function faviconSvg(icon) {
  const key = String(icon || "");
  const ring = /-ring$/.test(key);
  const kind = key.replace(/-ring$/, "");
  let inner;
  if (kind === "amber") inner = favPath(FAV_OPEN, "#6ea8fe") + favDot("#f5b94a") + (ring ? favRing(11.5) : "");
  else if (kind === "neutral") inner = favPath(FAV_OPEN, "#6ea8fe") + favDot("#8b939e") + (ring ? favRing(11.5) : "");
  else if (kind === "offline") {
    inner = favPath(FAV_OPEN, "#8b939e") + '<circle cx="11.5" cy="6" r="2" style="fill:none;stroke:#8b939e;stroke-width:1.4"/>' + (ring ? favRing(11.5) : "");
  } else {
    // The frames draw no ring on the normal dot: centred on that dot (8,8 absolute), the one
    // place a ring can sit without leaving the badge corner empty.
    inner = favPath(FAV_DOOR, "#6ea8fe") + '<rect x="4" y="4" width="4" height="4" rx="2" style="fill:#4ccf8a"/>' + (ring ? favRing(6) : "");
  }
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">' + FAV_BG +
    '<g transform="translate(2 2)">' + inner + "</g></svg>";
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

// ---- Prefs (localStorage; Task 4 draws the UI, Task 5 the chime) ----
// The master switch is the older attention.js NOTIFY_KEY mechanism (notifyWanted); the rest are
// one key each. Every access is guarded: with storage unavailable or a key missing the default
// stands (block, done, ended on; message and chime off).
const NOTIFY_PREFS = {
  block: "agent-box:notify:block",
  done: "agent-box:notify:done",
  ended: "agent-box:notify:ended",
  message: "agent-box:notify:message",
  chime: "agent-box:notify:chime",
};
function nmStore() {
  try { return typeof localStorage !== "undefined" ? localStorage : null; } catch (_) { return null; }
}
// readPrefs() -> {block, done, ended, message, chime}; "1" is on, "0" is off, anything else is the default.
function readPrefs() {
  const out = {};
  const st = nmStore();
  for (const name of Object.keys(NOTIFY_PREFS)) {
    out[name] = NOTIFY_PREF_DEFAULTS[name];
    try {
      const v = st ? st.getItem(NOTIFY_PREFS[name]) : null;
      if (v === "1") out[name] = true;
      else if (v === "0") out[name] = false;
    } catch (_) { /* default stands */ }
  }
  return out;
}
// writePref(name, value) -> true when stored. An unknown name is refused.
function writePref(name, value) {
  if (!Object.prototype.hasOwnProperty.call(NOTIFY_PREFS, name)) return false;
  const st = nmStore();
  try {
    if (!st) return false;
    st.setItem(NOTIFY_PREFS[name], value ? "1" : "0");
    return true;
  } catch (_) { return false; }
}

// ---- endedByMe: ids this client ended or restarted, kept 10 min each ----
const ENDED_BY_ME_MS = 10 * 60 * 1000;
// endedMark(map, id, now): record that this client ended `id` (a Map of id -> ms).
function endedMark(map, id, now) {
  if (map && id) map.set(id, now);
}
// endedLive(map, now) -> ids still inside the window; prunes the older ones from the map.
function endedLive(map, now) {
  const out = [];
  if (!map) return out;
  for (const [id, t] of Array.from(map)) {
    if (now - t >= ENDED_BY_ME_MS) map.delete(id);
    else out.push(id);
  }
  return out;
}

// endedClearRestarted(map, restarted, sessions): a restart mark outlives the restart only until a
// successful poll sees that id live again; then the mark goes, so a later crash still notifies.
// `restarted` is the Set of ids whose restart request succeeded. Kill marks are never touched.
function endedClearRestarted(map, restarted, sessions) {
  if (!restarted || !Array.isArray(sessions)) return;
  for (const id of Array.from(restarted)) {
    const s = sessions.find((x) => x.id === id);
    if (s && !nmIsEnded(s)) { restarted.delete(id); if (map) map.delete(id); }
  }
}

// ---- Delivery: notes -> OS notifications ----
// deliverNotes(notes, NotificationCtor, onOpen) builds one notification per note. A note's click
// calls onOpen(note) (the page focuses the window and opens note.sid, or shows the sidebar for a
// batch) and closes it. A browser that refuses at construction time stays silent. Returns the
// number built.
function deliverNotes(notes, NotificationCtor, onOpen) {
  let n = 0;
  if (typeof NotificationCtor !== "function") return n;
  for (const note of notes || []) {
    try {
      const o = new NotificationCtor(note.title, Object.assign({ body: note.body }, note.options));
      o.onclick = () => {
        try { if (onOpen) onOpen(note); } finally { try { o.close(); } catch (_) {} }
      };
      n++;
    } catch (_) { /* stay silent */ }
  }
  return n;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { notifyClassOf, causeKeys, notifyStep, notifyBodyFor, askTool, NOTIFY_PREF_DEFAULTS, titleState, blockedCauses, attnChipModel, chipDismissKey, nmChipText, chimeStep, faviconSvg, SYS_OFFLINE_MS, sysBannerModel, railMarkerModel,
    NOTIFY_PREFS, readPrefs, writePref, ENDED_BY_ME_MS, endedMark, endedLive, endedClearRestarted, deliverNotes };
}
