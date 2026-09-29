"use strict";

// Pure helpers for the attention signal: what a needs-you card shows, the tab
// title/favicon, and which sessions just started needing you. DOM wiring lives
// in app.js; everything here is testable under node --test.

// needLabel: the human word for WHY a waiting session needs you. "" when it is
// not waiting, or waiting without a known need (the caller falls back to the
// activity's own title).
function needLabel(s) {
  if (!s || s.activity !== "waiting") return "";
  if (s.need === "ask") return "asks";
  if (s.need === "turn") return "your turn";
  return "";
}

// cardContent decides which blocks a desktop needs-you card renders.
//  - ask: the tool call it wants to run. NO reply field and NO digit buttons —
//    the server refuses any message to a pending/blocked session by design (a
//    permission dialog reads single keys), so the card sends you to the
//    terminal instead (Open, always rendered by the caller).
//  - turn on claude: the summary + a one-line reply when the session has
//    opted in, else an "Enable replies" control — never a dead field, and
//    never digits (a digit would become the agent's next prompt).
//  - non-claude (codex), OR claude with no `need` at all: Open only, no reply
//    path. The server only ever reports `need` for a waiting claude session
//    whose transcript it could read; empty `need` on a waiting claude session
//    means the transcript was NOT readable, and SendMessage refuses every
//    send to such a session (ErrNoTranscript) regardless of `replyable` — so
//    a reply field here would be dead on arrival. Same shape as codex on
//    purpose: neither has a real answer path from this card.
function cardContent(s) {
  const v = s || {};
  if (v.need === "ask") {
    return { text: v.ask || v.summary || "Waiting on a prompt in the terminal.", ask: true, reply: false, enableReplies: false };
  }
  if (!v.claude || !v.need) {
    return { text: v.summary || "Waiting for input.", ask: false, reply: false, enableReplies: false };
  }
  return { text: v.summary || "No summary", ask: false, reply: !!v.replyable, enableReplies: !v.replyable };
}

// attentionTitle: the tab title. The waiting count goes FIRST so it survives a
// tab strip that truncates from the right.
function attentionTitle(n, env) {
  return (n > 0 ? "(" + n + ") " : "") + (env ? "[" + env + "] " : "") + "Agent Box";
}

// faviconHref: the amber-dot icon while anything needs you.
function faviconHref(n) {
  return n > 0 ? "/favicon-needs.svg" : "/favicon.svg";
}

// needsTransitions: which sessions STARTED needing you since the last poll.
// prevIds === null means "first poll after load": it only seeds, so a reload
// never re-announces sessions that were already waiting.
function needsTransitions(prevIds, sessions) {
  const next = new Set();
  const fired = [];
  for (const s of Array.isArray(sessions) ? sessions : []) {
    if (!s || s.activity !== "waiting") continue;
    next.add(s.id);
    if (prevIds && !prevIds.has(s.id)) fired.push(s);
  }
  return { next, fired };
}

// notifyAvailability: can the toggle be offered at all on this page? The
// tailnet http:// origins are not secure contexts, and browsers only expose
// notifications to secure ones.
function notifyAvailability(secure, permission) {
  if (!secure) return { enabled: false, reason: "Requires HTTPS — open via agent-box.culiops.dev" };
  if (permission === "unsupported") return { enabled: false, reason: "This browser does not support notifications." };
  return { enabled: true, reason: "" };
}

// notifyChecked: the toggle reads on only when the operator opted in AND the
// browser still grants it (permission can be revoked behind our back).
function notifyChecked(wanted, permission) {
  return !!wanted && permission === "granted";
}

const NOTIFY_KEY = "agent-box:notify";
function notifyWanted() {
  try { return localStorage.getItem(NOTIFY_KEY) === "1"; } catch (_) { return false; }
}
function setNotifyWanted(v) {
  try { if (v) localStorage.setItem(NOTIFY_KEY, "1"); else localStorage.removeItem(NOTIFY_KEY); } catch (_) {}
}

// notifyBody: the OS notification body text. For an "ask" (a pending tool
// call), this is the tool NAME ONLY — "asks: Bash", never the full command —
// derived from the ask text up to its first "(". An OS keeps notification
// history outside this app's own access controls, and a shell command in
// that history (a path, a secret-looking argument, an rm target) is a token
// leak the operator did not choose to make. `turn` (and anything without a
// known need) keeps the one-line summary, unchanged.
function notifyBody(s) {
  const v = s || {};
  if (v.need === "ask") {
    const ask = v.ask || v.summary || "";
    if (!ask) return "Waiting for you";
    const i = ask.indexOf("(");
    return "asks: " + (i === -1 ? ask : ask.slice(0, i));
  }
  return v.summary || "Waiting for you";
}

// notifyPoll gates needsTransitions on `loaded`: true only once a real
// /api/sessions fetch has actually succeeded at least once. Before that,
// `lastSessions` in app.js is just the empty placeholder it starts as —
// running needsTransitions against it would seed prevIds to "nothing is
// waiting" from a list that was never real, so that when the real list lands
// every session already waiting would look like a fresh transition and fire
// a notification on reload. Not loaded: hand prevIds back untouched (still
// null on the very first call) so the real first successful poll still gets
// needsTransitions's own "first poll" seed-only behaviour.
function notifyPoll(loaded, prevIds, sessions) {
  if (!loaded) return { next: prevIds, fired: [] };
  return needsTransitions(prevIds, sessions);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { needLabel, cardContent, attentionTitle, faviconHref, needsTransitions,
    notifyAvailability, notifyChecked, notifyWanted, setNotifyWanted, notifyBody, notifyPoll };
}
