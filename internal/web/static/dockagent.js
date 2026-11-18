"use strict";

// The agent strip at the bottom of the Files dock's Full mode (spec 5.4, mockup B5): with the terminal covered, the strip
// keeps the session in view. It shows the session's dot, its name, its status words, the last thing the agent did, and
// "Show terminal" (app.js wires the button). DOM only; the markup is index.html's #dock-agent, and desktop.css shows it only
// under #drawer[data-max="1"].
//
// The last line is read from the session's Activity: GET /api/sessions/{id}/activity?limit=1, every DOCK_AGENT_MS, and only
// while it can be seen: Full mode is on, the dock shows Files for a session, and the page is visible. It stops on leaving
// Full mode, on the page being hidden and on the dock moving to another session (which starts again for the new one). One
// request at a time: a tick that finds the last one still out is skipped, and stopping aborts it. A reply that is not for
// the poll that asked is dropped, and a failed one leaves the line as it was (like the tree's dots: the answer is a hint).
// Text goes to the page with textContent: the line is the agent's own words.
//
// Insert at prompt (dockAgentInsert below) leaves Full mode first and then asks the insert decision; see there.

const DOCK_AGENT_MS = 4000;
const DOCK_AGENT_FETCH_MS = 8000; // a request that never answers must not hold the one poll slot for good
const DOCK_AGENT_LINE_MAX = 300;  // the server's detail is at most 300 runes; a stranger's reply is cut to the same

const DOCK_AGENT_KIND = { you: "You", clear: "Cleared", credential: "Credential", approval: "Approval" };

let dagSid = null;      // the session being polled, null when stopped
let dagTimer = null;
let dagFlight = null;   // the AbortController of the request that is out, null when none
let dagSeq = 0;         // bumped on every start and stop: a reply is for the poll that asked, or it is dropped
let dagLineSid = null;  // the session the line on screen belongs to

// dockAgentLine: the newest event as the strip says it. A tool call reads `Tool(detail)`, as the Activity view's row and the
// agent's own terminal do; any other kind (your message, a clear, a credential row) is its label, a colon and the detail.
// Whitespace runs are one space (a Bash command can span lines; the strip is one line). "" for no event, and for anything
// that is not an event, so the strip shows nothing rather than "undefined".
function dockAgentLine(events) {
  const e = Array.isArray(events) ? events[0] : null;
  if (!e || typeof e !== "object") return "";
  const detail = (typeof e.detail === "string" ? e.detail : "").replace(/\s+/g, " ").trim().slice(0, DOCK_AGENT_LINE_MAX);
  if (e.kind === "tool") {
    const tool = typeof e.tool === "string" && e.tool.trim() ? e.tool.trim() : "Tool";
    return detail ? tool + "(" + detail + ")" : tool;
  }
  const label = Object.prototype.hasOwnProperty.call(DOCK_AGENT_KIND, e.kind) ? DOCK_AGENT_KIND[e.kind] : "";
  if (!label) return detail;
  return detail ? label + ": " + detail : label;
}

function dagEl(id) { return document.getElementById(id); }

function dagSetText(id, text) {
  const el = dagEl(id);
  if (el && el.textContent !== text) el.textContent = text;
}

// dagSetLine puts the last activity on screen, and remembers whose it is.
function dagSetLine(sid, line) {
  dagLineSid = sid;
  dagSetText("dock-agent-last", line);
  const el = dagEl("dock-agent-last");
  if (el) el.title = line;
}

// dockAgentPaint brings the strip's static parts in step with the session list: the dot, the name and the status words,
// the same dot and words the sidebar row gives that session (deskDot). Cheap and idempotent; it runs on every session poll
// while the dock is open (drawerPaint), so a session that finishes its turn changes the strip within a poll.
function dockAgentPaint() {
  const sid = typeof drawerState !== "undefined" ? drawerState.sessionId : null;
  const rec = sid ? (typeof lastSessions !== "undefined" ? lastSessions : []).find((x) => x && x.id === sid) : null;
  const dot = typeof deskDot === "function" && rec ? deskDot(rec) : null;
  const dotEl = dagEl("dock-agent-dot");
  const cls = dot ? dot.cls : "dot";
  if (dotEl && dotEl.className !== cls) dotEl.className = cls;
  dagSetText("dock-agent-name", typeof drawerSessionName === "function" ? drawerSessionName(sid) : "");
  dagSetText("dock-agent-st", dot && dot.title ? dot.title : "");
  // Insert at prompt needs a file to point at.
  const ins = dagEl("dock-agent-ins");
  if (ins) {
    const tab = typeof dockAttached === "function" ? dockAttached() : null;
    const has = !!(tab && tab.path);
    if (ins.disabled === has) ins.disabled = !has;
    const title = has ? "Leave Full mode and insert the selected lines, or the file's path, at the prompt" : "Open a file in the dock first";
    if (ins.title !== title) ins.title = title;
  }
}

// dockAgentInsert: the strip's Insert at prompt. It types the dock's active file as editorRefFor says (the selected lines as
// path:a-b, or the file's path when nothing is selected) at the session's prompt. The strip is there because Full mode covers the
// terminal, and a covered terminal is a state the insert decision refuses (insertdecision.js, #337); no exception is made for it
// (founder, 2026-11-18). So the button first leaves Full mode, as Show terminal does, and then asks the decision on the prompt
// that is on screen now: it types, or toasts why it will not (the session waits on the user) and types nothing. That is
// editorInsertLeavingFull (editorselbar.js), which the selection bar's Insert uses too. Never Enter.
function dockAgentInsert() {
  const tab = typeof dockAttached === "function" ? dockAttached() : null;
  const want = editorRefFor(tab);
  if (!want) { showToast("Open a file in the dock first: that is what Insert at prompt types", { error: true }); return; }
  if (!want.sid) { showToast("This dock has no session, so there is no prompt to insert at", { error: true }); return; }
  editorInsertLeavingFull(want);
}

// dockAgentWant: the session the strip should be polling right now, "" for none.
function dockAgentWant() {
  if (typeof document === "undefined" || document.hidden) return "";
  if (typeof currentPage === "function" && currentPage()) return ""; // a page view (Settings, Audit) hides the dock by CSS
  const dr = dagEl("drawer");
  if (!dr || dr.hidden || dr.dataset.max !== "1") return "";
  if (typeof drawerState === "undefined" || drawerState.open !== "files" || !drawerState.sessionId) return "";
  return drawerState.sessionId;
}

// dockAgentSync starts, keeps or stops the poll to match what is on screen, and repaints the static parts. Called from every
// place that changes what is on screen: Full mode on or off (paintDrawerMax), the dock moving or its list changing
// (drawerPaint), a page view opening or closing (pagehost.js) and the page being hidden or shown (below). A no-op when nothing changed.
function dockAgentSync() {
  const sid = dockAgentWant();
  if (!sid) { dockAgentStop(); return; }
  dockAgentPaint();
  dockAgentStart(sid);
}

// dockAgentStart polls `sid` now and every DOCK_AGENT_MS. Already polling it: nothing. Polling another: that one stops
// first and the old line goes at once, so one session's last action is never read under another's name.
function dockAgentStart(sid) {
  if (!sid) { dockAgentStop(); return; }
  if (dagSid === sid && dagTimer) return;
  dockAgentStop();
  dagSid = sid;
  if (dagLineSid !== sid) dagSetLine(sid, "");
  dagTimer = setInterval(dockAgentTick, DOCK_AGENT_MS);
  dockAgentTick();
}

// dockAgentStop ends the poll and drops a request that is out. The line stays as it is: it is only ever shown for the session
// it belongs to (dockAgentStart clears it for another).
function dockAgentStop() {
  if (dagTimer) { clearInterval(dagTimer); dagTimer = null; }
  dagSid = null;
  dagSeq++;
  if (dagFlight) { dagFlight.abort(); dagFlight = null; }
}

// dockAgentTick asks once, unless the last ask is still out.
async function dockAgentTick() {
  if (!dagSid || dagFlight) return;
  const sid = dagSid;
  const seq = dagSeq;
  const ac = new AbortController();
  dagFlight = ac;
  const timer = setTimeout(() => ac.abort(), DOCK_AGENT_FETCH_MS);
  let body;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/activity?limit=1", { signal: ac.signal });
    body = await accReadReply(res);
  } catch (_) { /* offline, timed out or aborted by a stop: the line stays */ } finally { clearTimeout(timer); }
  if (seq !== dagSeq) return; // stopped, or started again for another session, while this was out: nothing of it is for now
  dagFlight = null;
  if (body && Array.isArray(body.events)) dagSetLine(sid, dockAgentLine(body.events));
}

function dockAgentInit() {
  const kbd = dagEl("dock-agent-kbd");
  const btn = dagEl("dock-agent-term");
  if (typeof shortcutHint !== "function" || typeof IS_MAC === "undefined") return;
  const hint = shortcutHint("dock.showTerminal", IS_MAC);
  if (kbd) kbd.textContent = hint;
  if (btn) btn.title = hint ? "Show the terminal (" + hint + ")" : "Show the terminal";
}

if (typeof document !== "undefined" && document.addEventListener) {
  // A hidden page polls nothing; coming back starts it again at once (and only if Full mode is still on).
  document.addEventListener("visibilitychange", dockAgentSync);
  dockAgentInit();
}
