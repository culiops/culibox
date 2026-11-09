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

const NOTIFY_PREF_DEFAULTS = { master: true, block: true, done: true, ended: true, message: false };
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
//   icon  "normal" | "amber" | "neutral" | "offline", plus "-ring" (not offline) while a write window is open
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

function titleState(sessions, sys, env, now) {
  const base = (env ? "[" + env + "] " : "") + "CuliBox";
  if (nmOffline(sys, now)) return { title: "Offline · " + base, icon: "offline" };
  const blocked = new Set();
  let done = 0, ring = false;
  for (const s of Array.isArray(sessions) ? sessions : []) {
    if (!s || s.exited || s.status === "exited") continue;
    if (nmWindowOpen(s, now)) ring = true;
    for (const k of causeKeys(s)) {
      if (/:done$/.test(k)) done++;
      else blocked.add(k);
    }
  }
  let title = base, icon = "normal";
  if (blocked.size) { title = "(" + blocked.size + ") " + base; icon = "amber"; }
  else if (done) { title = "\u2713" + done + " " + base; icon = "neutral"; }
  return { title, icon: ring ? icon + "-ring" : icon };
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
    inner = favPath(FAV_OPEN, "#8b939e") + '<circle cx="11.5" cy="6" r="2" style="fill:none;stroke:#8b939e;stroke-width:1.4"/>';
  } else {
    // The frames draw no ring on the normal dot: centred on that dot (8,8 absolute), the one
    // place a ring can sit without leaving the badge corner empty.
    inner = favPath(FAV_DOOR, "#6ea8fe") + '<rect x="4" y="4" width="4" height="4" rx="2" style="fill:#4ccf8a"/>' + (ring ? favRing(6) : "");
  }
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16">' + FAV_BG +
    '<g transform="translate(2 2)">' + inner + "</g></svg>";
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { notifyClassOf, causeKeys, notifyStep, notifyBodyFor, askTool, NOTIFY_PREF_DEFAULTS, titleState, faviconSvg, SYS_OFFLINE_MS };
}
