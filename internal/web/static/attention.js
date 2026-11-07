"use strict";

// Pure helpers for the attention signal: what a needs-you card shows, the tab
// title/favicon, and which sessions just started needing you. DOM wiring lives
// in app.js; everything here is testable under node --test.

// needsYou: the one definition of "this session wants the operator". An expired
// AWS sign-in counts even while the agent is still running: its AWS calls fail
// until someone signs in, and nothing about the agent's activity says so. A
// locked secret store is the same: the session's store-backed credentials
// cannot be fetched until someone unlocks it.
function needsYou(s) {
  return !!s && (s.activity === "waiting" || s.need === "sso" || s.need === "store_locked" || connSSOSources(s).length > 0);
}

// connSSOSources: the distinct sign-in sources a session with named AWS
// connections waits on (sorted), [] for any other session. The list entry
// carries sso_expired per connection; any one of them wants the operator.
function connSSOSources(s) {
  const cs = s && Array.isArray(s.aws_connections) ? s.aws_connections : [];
  return [...new Set(cs.map((c) => c && c.sso_expired).filter(Boolean))].sort();
}

// tabNeedWhy: why a session's tab reads "waiting" when the agent itself is not
// waiting for input: "sso" for an expired AWS sign-in over any other activity,
// "" otherwise. The tab's dot and tooltip use it so a working agent is not
// described as waiting for input (the activity itself is never rewritten).
function tabNeedWhy(s) {
  if (!s || s.activity === "waiting") return "";
  if (s.need === "sso" || connSSOSources(s).length > 0) return "sso";
  return s.need === "store_locked" ? "store" : "";
}

// sessionDot: the activity dot (class + title) for a session on every surface
// that draws one. A session that needsYou for a non-activity reason (an expired
// AWS sign-in) reads as waiting, and says why, so the sheet header, the list row
// and the cards agree with the tab's dot.
function sessionDot(s) {
  const act = typeof activityClass === "function" ? activityClass : require("./activity.js").activityClass;
  const v = s || {};
  return act(needsYou(v) ? "waiting" : v.activity, tabNeedWhy(v));
}

// ssoExpiredText: the SSO card's sentence, shared by cardContent and the card
// model in elevation.js so the two cannot drift.
function ssoExpiredText(s) {
  const src = (s && s.aws && s.aws.sso_expired) || connSSOSources(s).join(", ");
  return "AWS sign-in expired" + (src ? " (" + src + ")" : "") + ". This session's AWS calls fail until you sign in.";
}

// storeLockedText: the store card's sentence. Plain: it names the effect, not
// the mechanism.
function storeLockedText() {
  return "This session's credentials from the store can't be fetched until you unlock it.";
}

// needLabel: the human word for WHY a waiting session needs you. "" when it is
// not waiting, or waiting without a known need (the caller falls back to the
// activity's own title).
function needLabel(s) {
  if (s && s.need === "sso") return "AWS sign-in";
  if (s && s.need === "store_locked") return "store locked";
  if (!s || s.activity !== "waiting") return "";
  if (s.need === "ask") return "asks";
  if (s.need === "turn") return "your turn";
  return "";
}

// plainSummary: the agent's last message without its markdown, for the desktop
// lines that show it on one line (the row sub-line, the ask strip, the palette).
// The server sends it raw, and a `**` drawn as text was the first thing read on
// the most-watched line of the product. Code keeps its text and loses only its
// backticks (nothing inside it is touched); a link or image keeps its label;
// `**`, a paired `__…__` and a line-start `#` go; whitespace collapses to one
// line. Display only, never sanitising: every caller writes it with textContent.
// Never for an ask, which is a tool call, not markdown (see cardContent).
function plainSummary(text) {
  const prose = (t) => t
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^[ \t]{0,3}#{1,6}[ \t]+/gm, "")
    .replace(/\*\*/g, "")
    .replace(/(^|\W)__(?=\S)([\s\S]*?\S)__(?!\w)/g, "$1$2")
    .replace(/`/g, "");
  // split with the two groups gives [prose, ticks, code, prose, ticks, code, …, prose].
  const parts = String(text == null ? "" : text).split(/(`+)([\s\S]*?)\1/);
  let out = "";
  for (let i = 0; i < parts.length; i += 3) out += prose(parts[i]) + (i + 2 < parts.length ? parts[i + 2] : "");
  return out.replace(/\s+/g, " ").trim();
}

// cardContent decides which blocks a desktop needs-you card renders.
//  - ask: the tool call it wants to run. No reply field and no digit buttons —
//    the server refuses any message to a pending/blocked session by design (a
//    permission dialog reads single keys), so the card sends you to the
//    terminal (Open, always rendered by the caller), and — for a replyable,
//    registry-blocked claude with the full command available — offers 1 Yes /
//    No (answerState). Not replyable: Enable replies.
//  - turn on claude: the summary + a one-line reply when the session has
//    opted in, else an "Enable replies" control — never a dead field, and
//    never digits (a digit would become the agent's next prompt).
//  - non-claude (a shell or other bin), OR claude with no `need` at all: Open only, no reply
//    path. The server only ever reports `need` for a waiting claude session
//    whose transcript it could read; empty `need` on a waiting claude session
//    means the transcript was NOT readable, and SendMessage refuses every
//    send to such a session (ErrNoTranscript) regardless of `replyable` — so
//    a reply field here would be dead on arrival. Same shape as a
//    non-claude session on purpose: neither has a real answer path from this card.
// opts.plain: the summary as plainSummary text, for the desktop's one-line
// surfaces. The ask is never touched: it is the command Yes would answer, and a
// stripped `__pycache__` would show one the agent is not going to run.
function cardContent(s, opts) {
  const v = s || {};
  const summary = opts && opts.plain ? plainSummary(v.summary) : v.summary;
  // Kill-switch: the text is the peek and stays; only the reply paths close.
  const on = globalThis.repliesEnabled === true;
  // An SSO need outranks turn/ask for WHICH card shows, but it must not take
  // the reply box away from a replyable session (spec 8.2).
  if (v.need === "sso") {
    return { text: ssoExpiredText(v), sso: true, ask: false, reply: on && !!v.replyable, enableReplies: false };
  }
  // A locked store: the card IS the unlock form (renderStoreBody). No reply path:
  // the session is not asking anything, and the fix is the passphrase.
  if (v.need === "store_locked") {
    return { title: "Store locked", text: storeLockedText(), unlock: true, ask: false, reply: false, enableReplies: false };
  }
  if (v.need === "ask") {
    return { text: v.ask || summary || "Waiting on a prompt in the terminal.", ask: true, reply: false, enableReplies: on && !!v.claude && !v.replyable };
  }
  if (!v.claude || !v.need) {
    return { text: summary || "Waiting for input.", ask: false, reply: false, enableReplies: false };
  }
  return { text: summary || "No summary", ask: false, reply: on && !!v.replyable, enableReplies: on && !v.replyable };
}

// maskHint: the one-line note under text the server's display mask touched.
// Empty unless the shown text really carries a "[masked:" token, so an
// unmasked ask or peek never claims to be masked. The mask is display-only
// (spec section 8): AskFull is never masked and never goes through this.
function maskHint(text) {
  return String(text == null ? "" : text).includes("[masked:")
    ? "Display mask: the agent and its transcript still hold the original."
    : "";
}

// attentionTitle: the tab title. The waiting count goes FIRST so it survives a
// tab strip that truncates from the right.
function attentionTitle(n, env) {
  return (n > 0 ? "(" + n + ") " : "") + (env ? "[" + env + "] " : "") + "CuliBox";
}

// faviconHref: the amber-dot icon while anything needs you.
function faviconHref(n) {
  return n > 0 ? "/favicon-needs.svg" : "/favicon.svg";
}

// NEEDS_REARM_MS: how long a session must stop needing you before needing you
// again is a NEW waiting period (and a new notification). A false "working"
// from a repaint read as work is visible for about 7-13 s (the server's 10 s
// silence threshold plus the 3 s poll, #77), so 15 s rides it out. It applies
// only to a session whose activity_source is "pty" (the silence fallback); the
// agent's own status has no blips, so its quick second prompt always fires.
// The cost on the fallback: a real turn shorter than this, answered from
// another tab or device, re-enters the same period and does not notify again.
const NEEDS_REARM_MS = 15000;

// needsTransitions: which sessions STARTED needing you since the last poll.
// prev === null means "first poll after load": it only seeds, so a reload
// never re-announces sessions that were already waiting. prev/next are
// { ids, away }: ids needed you on that poll; away maps an id that stopped
// needing you to when it was first seen not needing you. One waiting period
// fires once: a pty-sourced session that needs you again before NEEDS_REARM_MS
// has passed since it left is the same period (an output blip), not a new
// one. now is the poll's time in ms.
function needsTransitions(prev, sessions, now) {
  const next = { ids: new Set(), away: new Map() };
  const fired = [];
  for (const s of Array.isArray(sessions) ? sessions : []) {
    const was = !!prev && prev.ids.has(s.id);
    const left = prev ? prev.away.get(s.id) : undefined;
    // Only the PTY-silence fallback blips. The agent's own status flips
    // exactly at turn boundaries, so a quick second prompt there is real.
    const sameWait = s.activity_source === "pty" && left !== undefined && now - left < NEEDS_REARM_MS;
    if (!needsYou(s)) {
      if (was) next.away.set(s.id, now);
      else if (sameWait) next.away.set(s.id, left);
      continue;
    }
    next.ids.add(s.id);
    if (prev && !was && !sameWait) fired.push(s);
  }
  return { next, fired };
}

// notifyAvailability: can the toggle be offered at all on this page? The
// tailnet http:// origins are not secure contexts, and browsers only expose
// notifications to secure ones.
function notifyAvailability(secure, permission) {
  if (!secure) return { enabled: false, reason: "Requires HTTPS — open CuliBox over an https:// URL" };
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
  // No profile, no command: OS notification history is outside our access control.
  if (v.need === "sso") return "AWS sign-in expired";
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
function notifyPoll(loaded, prevIds, sessions, now) {
  if (!loaded) return { next: prevIds, fired: [] };
  return needsTransitions(prevIds, sessions, now);
}

// utf8Bytes: a string's UTF-8 bytes, by hand. A lone surrogate becomes
// U+FFFD, as it does in Go's json decoding — the server hashes the same bytes.
function utf8Bytes(str) {
  const out = [];
  for (let i = 0; i < str.length; i++) {
    let c = str.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff && i + 1 < str.length) {
      const d = str.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) { c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00); i++; } else c = 0xfffd;
    } else if (c >= 0xd800 && c <= 0xdfff) {
      c = 0xfffd;
    }
    if (c < 0x80) out.push(c);
    else if (c < 0x800) out.push(0xc0 | (c >> 6), 0x80 | (c & 63));
    else if (c < 0x10000) out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
    else out.push(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
  }
  return out;
}

const SHA256_K = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

// sha256Hex: lowercase hex SHA-256 of a string's UTF-8 bytes. Pure JS on
// purpose: crypto.subtle exists only in a secure context, and the tailnet
// http:// origin is not one — one tested path beats a fallback nobody runs.
function sha256Hex(str) {
  const bytes = utf8Bytes(String(str == null ? "" : str));
  const bitLen = bytes.length * 8;
  bytes.push(0x80);
  while (bytes.length % 64 !== 56) bytes.push(0);
  const hi = Math.floor(bitLen / 0x100000000), lo = bitLen >>> 0;
  bytes.push((hi >>> 24) & 255, (hi >>> 16) & 255, (hi >>> 8) & 255, hi & 255,
    (lo >>> 24) & 255, (lo >>> 16) & 255, (lo >>> 8) & 255, lo & 255);
  const h = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
  const w = new Array(64);
  const rotr = (x, n) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < bytes.length; off += 64) {
    for (let i = 0; i < 16; i++) {
      const j = off + i * 4;
      w[i] = ((bytes[j] << 24) | (bytes[j + 1] << 16) | (bytes[j + 2] << 8) | bytes[j + 3]) >>> 0;
    }
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, hh] = h;
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + SHA256_K[i] + w[i]) >>> 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) >>> 0;
      hh = g; g = f; f = e; e = (d + t1) >>> 0; d = c; c = b; b = a; a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0] + a) >>> 0; h[1] = (h[1] + b) >>> 0; h[2] = (h[2] + c) >>> 0; h[3] = (h[3] + d) >>> 0;
    h[4] = (h[4] + e) >>> 0; h[5] = (h[5] + f) >>> 0; h[6] = (h[6] + g) >>> 0; h[7] = (h[7] + hh) >>> 0;
  }
  return h.map((x) => x.toString(16).padStart(8, "0")).join("");
}

// answerState: whether an ask card offers 1 Yes / No, and whether they are
// live. Offered only when the server sent ask_full — it does so only for a
// replyable, registry-blocked claude with exactly one pending call. Live only
// while the card is expanded AND the command on screen is the one that was
// expanded (seenAskFull): a prompt that changed underneath must be read again.
function answerState(s, expanded, seenAskFull) {
  const off = { show: false, enabled: false, title: "", note: "" };
  const v = s || {};
  // Kill-switch: no Yes/No and no "answer in the terminal" note either.
  if (globalThis.repliesEnabled !== true) return off;
  if (v.need !== "ask" || !v.replyable) return off;
  if (!v.ask_full) return { show: false, enabled: false, title: "", note: "Answer this one in the terminal — the full prompt can't be shown here." };
  if (!expanded) return { show: true, enabled: false, title: "Expand to see the full command first", note: "" };
  if (seenAskFull !== v.ask_full) return { show: true, enabled: false, title: "The prompt changed — collapse and expand to review it", note: "" };
  return { show: true, enabled: true, title: "", note: "" };
}

// answerErrorText: the toast for a refused answer. The two 409s differ: the
// server names a changed prompt ("the prompt changed") vs. one that is gone.
function answerErrorText(status, errMsg) {
  if (status === 409) return /changed/.test(errMsg || "") ? "The prompt changed — review it again" : "The session is no longer waiting on a prompt.";
  if (status === 429) return "Too soon — try again in a second.";
  if (status === 403) return "Replies are not enabled for this session.";
  if (status === 404 || status === 410) return "That session is gone.";
  if (status === 0) return "Answer failed: no connection.";
  return "Answer failed (" + status + ").";
}

// staleExpanded: whether an expanded needs-you card's state (its cardExpanded
// membership and cached peek/ask_full text) must be dropped on this poll —
// pulled out of refreshSessions's pruning loop so it is directly testable. `s`
// is the session's CURRENT record, or undefined/null if it vanished from the
// list entirely; `seenAskFull` is whatever cardAskSeen recorded for this id
// when the card was expanded (undefined if nothing was ever recorded for it).
// A session that vanished or stopped waiting is always stale. Otherwise, a
// recorded seenAskFull that no longer matches the session's current ask_full
// means the prompt underneath changed since the operator expanded the card —
// including a turn card that just became an ask (seenAskFull was "" then) —
// and it must collapse so the new prompt gets read again before Yes/No unlock.
function staleExpanded(s, seenAskFull) {
  if (!needsYou(s)) return true;
  if (seenAskFull === undefined) return false;
  return (s.ask_full || "") !== seenAskFull;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { needsYou, connSSOSources, storeLockedText, sessionDot, tabNeedWhy, ssoExpiredText, needLabel, plainSummary, cardContent, attentionTitle, faviconHref, needsTransitions, NEEDS_REARM_MS,
    notifyAvailability, notifyChecked, notifyWanted, setNotifyWanted, notifyBody, notifyPoll,
    sha256Hex, answerState, answerErrorText, staleExpanded, maskHint };
}
