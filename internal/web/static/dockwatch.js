"use strict";

// Live change tracking for the Files dock (spec 5.4, Task 9c). Git is asked about the workspace on a tree paint, a save, a
// file operation, ↻ and the end of the agent's turn, so during a long turn the tree and the Changes section would be minutes
// stale. While the dock shows a session's files and that session is working, this reads the session's Activity every
// DOCK_WATCH_MS, marks the files the agent changed as soon as Activity knows them, and asks git for a fresh status and numstat
// a moment later (the Changes section shows the file as a provisional "…" row until git answers, changesmodel.js).
//
// It polls only while all of these hold: the dock shows Files for a session on the desktop, the page is visible, no page view
// (Settings, Audit) is over the dock, and the session's `activity` in the session list is "working". It stops the moment any
// of them ends (the end-of-turn refresh of gitstatus.js still runs) and starts again when they hold again. One request at a
// time: a tick that finds the last one still out is skipped, a request that never answers is abandoned after
// DOCK_WATCH_FETCH_MS, and stopping aborts what is out, so a reply is for the poll that asked or it is dropped. A failed or
// odd reply changes nothing (the answer is a hint, like the tree's dots). Nothing here knows which agent the session runs: a
// session with no transcript answers `files: []`, so nothing lights up, and it is polled only while it reads "working".
//
// What a poll does with `files` (Activity's, ordered by the edit applied last, not by ts):
//   - the page's list for the session (drawerChangedFiles) is set at once when it differs, and every reader repainted: the
//     tree's session bars, the Changes section and the crumbs (drawerSetChanged);
//   - an entry that is new or changed against what this watch last saw, and sits under the tree root, asks for a fresh git
//     status (gitStatusRefreshSoon({fresh: true})). Its 1 s debounce and the server's single flight bound the container
//     runs, and the numstat that follows the status is fresh too (dockchanges.js): no second timer-driven git poll;
//   - subscribers hear (files, newEvents) when the files changed or there are new events.
//
// dockWatchSubscribe(sid, fn) -> unsubscribe is for P2's Follow agent, so it need not poll Activity on its own. `newEvents` are
// the Activity timeline events newer than the newest this watch has seen for the session, newest first as the endpoint gives
// them, at most DOCK_WATCH_EVENTS of them (older ones the poll missed are not recovered). The first answer for a session on
// this page is the baseline: its events were there before the watch and are not new, and the watch keeps them across a stop
// and a start (a later turn gets what came since).

const DOCK_WATCH_MS = 2000;
const DOCK_WATCH_FETCH_MS = 8000; // a request that never answers must not hold the one poll slot for good
const DOCK_WATCH_EVENTS = 20;

let dwSid = null;     // the session being polled, null when stopped
let dwTimer = null;
let dwFlight = null;  // {ac, timer} of the request that is out (its AbortController and its timeout), null when none
let dwSeq = 0;        // bumped on every start and stop: a reply is for the poll that asked, or it is dropped
// What the watch has acted on, for one session: {sid, sig: Map(path -> "ts|add|del"), ev}. `ev` is the newest event ts seen, -1
// before the first answer (that one is the baseline). Kept across a stop; another session replaces it.
let dwKnown = { sid: null, sig: new Map(), ev: -1 };
const dwSubs = new Map(); // session id -> Set of {fn}

// dockWatchWant: the session the watch should be polling right now, "" for none.
function dockWatchWant() {
  if (typeof document === "undefined" || document.hidden) return "";
  if (typeof currentPage === "function" && currentPage()) return ""; // the page hides the dock
  if (typeof drawerOwnsFileClicks !== "function" || !drawerOwnsFileClicks()) return "";
  const sid = typeof drawerState !== "undefined" && drawerState ? drawerState.sessionId : null;
  if (!sid) return "";
  const rec = typeof lastSessions !== "undefined" && Array.isArray(lastSessions) ? lastSessions.find((x) => x && x.id === sid) : null;
  return rec && rec.activity === "working" ? sid : "";
}

// dockWatchSync starts, keeps or stops the poll to match what is on screen. Called from everywhere that changes it: every
// session poll while the dock is open and a close (drawerPaint), a page view opening or closing (pagehost.js) and the
// page being hidden or shown (below). A no-op when nothing changed.
function dockWatchSync() {
  const sid = dockWatchWant();
  if (!sid) { dockWatchStop(); return; }
  dockWatchStart(sid);
}

// dockWatchStart polls `sid` now and every DOCK_WATCH_MS. Already polling it: nothing. Polling another: that one stops first.
function dockWatchStart(sid) {
  if (dwSid === sid && dwTimer) return;
  dockWatchStop();
  dwSid = sid;
  if (dwKnown.sid !== sid) {
    const held = typeof drawerChangedFiles !== "undefined" ? drawerChangedFiles.get(sid) : null;
    dwKnown = { sid, sig: dwSigs(held), ev: -1 };
  }
  dwTimer = setInterval(dockWatchTick, DOCK_WATCH_MS);
  dockWatchTick();
}

// dockWatchStop ends the poll and drops a request that is out. No timer is left, this very moment: the interval and the
// request's timeout are cleared here, not when the aborted request gets round to failing.
function dockWatchStop() {
  if (dwTimer) { clearInterval(dwTimer); dwTimer = null; }
  dwSid = null;
  dwSeq++;
  if (dwFlight) { clearTimeout(dwFlight.timer); dwFlight.ac.abort(); dwFlight = null; }
}

// dockWatchTick asks once, unless the last ask is still out. It re-checks the conditions first: whatever changed them and
// did not say so (a page view opened by a route that does not call dockWatchSync) ends the poll here, before a request.
async function dockWatchTick() {
  if (!dwSid) return;
  if (dockWatchWant() !== dwSid) { dockWatchSync(); return; }
  if (dwFlight) return;
  const sid = dwSid;
  const seq = dwSeq;
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), DOCK_WATCH_FETCH_MS);
  dwFlight = { ac, timer };
  let data;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/activity?limit=" + DOCK_WATCH_EVENTS, { signal: ac.signal });
    data = await accReadReply(res);
  } catch (_) { /* offline, timed out or aborted by a stop: nothing changes */ } finally { clearTimeout(timer); }
  if (seq !== dwSeq) return; // stopped, or started again for another session, while this was out: nothing of it is for now
  dwFlight = null;
  if (data && typeof data === "object" && Array.isArray(data.files)) dwApply(sid, data);
}

// dwSigs: path -> "ts|add|del" for a files list, the first of a repeated path winning. A list that is not an array is empty.
function dwSigs(files) {
  const m = new Map();
  if (!Array.isArray(files)) return m;
  for (const f of files) {
    if (!f || typeof f.path !== "string" || m.has(f.path)) continue;
    m.set(f.path, [f.ts, f.add, f.del].join("|"));
  }
  return m;
}

// dwSame: the same entries in the same order (the order is the bars' and the Changes list's).
function dwSame(a, b) {
  if (a.size !== b.size) return false;
  const bi = b.entries();
  for (const [k, v] of a) {
    const n = bi.next().value;
    if (n[0] !== k || n[1] !== v) return false;
  }
  return true;
}

// dwUnderRoot: the path is a file under the tree root, so a status of the tree could say something about it.
function dwUnderRoot(p) {
  const root = typeof ftRoot === "function" ? ftRoot() : "";
  const base = root ? changedSegs(root) : null;
  const segs = changedSegs(p);
  return !!base && !!segs && segs.length > base.length && base.every((s, i) => segs[i] === s);
}

// dwApply takes one good answer for `sid`.
function dwApply(sid, data) {
  const files = data.files;
  const next = dwSigs(files);
  const had = dwKnown.sig;
  const news = [];
  for (const [p, sig] of next) if (had.get(p) !== sig) news.push(p);
  const moved = !dwSame(had, next);
  dwKnown.sig = next;

  // The events: newer than the newest seen. The first answer for the session is the baseline, so its events are not new.
  const events = Array.isArray(data.events) ? data.events.filter((e) => e && typeof e === "object" && Number.isFinite(e.ts)) : [];
  const seen = dwKnown.ev;
  const fresh = seen < 0 ? [] : events.filter((e) => e.ts > seen);
  dwKnown.ev = events.reduce((n, e) => Math.max(n, e.ts), Math.max(seen, 0));

  // The page's list: compared with what the page holds, not with what this watch saw, so an older answer of the tree's own
  // load that landed after this one's cannot leave the bars behind.
  const held = typeof drawerChangedFiles !== "undefined" ? drawerChangedFiles.get(sid) : null;
  if (!dwSame(dwSigs(held), next)) drawerSetChanged(sid, files);
  // Git is asked when the agent changed something the shown tree can say something about.
  if (news.some(dwUnderRoot) && typeof gitStatusRefreshSoon === "function") gitStatusRefreshSoon({ fresh: true });

  if (moved || fresh.length) dwNotify(sid, files, fresh);
}

function dwNotify(sid, files, events) {
  const subs = dwSubs.get(sid);
  if (!subs) return;
  for (const s of [...subs]) {
    try { s.fn(files, events); } catch (_) { /* one subscriber's fault is neither the watch's nor the others' */ }
  }
}

// dockWatchSubscribe: fn(files, newEvents) after each answer for `sid` that brought news (see the top). It does not start a
// poll: the watch runs on its own conditions. Returns the unsubscribe function; a fn that is not a function gets a no-op one.
function dockWatchSubscribe(sid, fn) {
  if (typeof fn !== "function") return () => {};
  const sub = { fn };
  let set = dwSubs.get(sid);
  if (!set) { set = new Set(); dwSubs.set(sid, set); }
  set.add(sub);
  return () => {
    const cur = dwSubs.get(sid);
    if (!cur) return;
    cur.delete(sub);
    if (!cur.size) dwSubs.delete(sid);
  };
}

// A hidden page polls nothing; coming back starts it again at once (if the other conditions still hold).
if (typeof document !== "undefined" && document.addEventListener) document.addEventListener("visibilitychange", dockWatchSync);
