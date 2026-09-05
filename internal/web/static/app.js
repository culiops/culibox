"use strict";

// --- fetch helpers -----------------------------------------------------------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();

// copyText copies to the clipboard and reports whether it actually worked via
// done(ok). The app is served over HTTP (not a secure context), so
// navigator.clipboard is usually unavailable: fall back to a hidden off-screen
// textarea + execCommand("copy"), which works over plain HTTP but can itself
// fail — its return value is what ok reports in that branch. `done` also
// restores terminal focus (the textarea steals it). window.__lastCopied is a
// test hook.
function copyText(text, done) {
  window.__lastCopied = text;
  const finish = (ok) => { if (done) done(ok); };
  if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(() => finish(true), () => finish(execCopy(text)));
    return;
  }
  finish(execCopy(text));
}
// execCopy returns whether execCommand("copy") actually succeeded: it can
// return false, or throw, with no other observable symptom.
function execCopy(text) {
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed"; ta.style.top = "0"; ta.style.left = "0"; ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.focus(); ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch (_) { /* best effort */ }
  document.body.removeChild(ta);
  return ok;
}

// PROBE_MS bounds every request on the reconnect path. A page whose fetches are
// wedged (e.g. behind an unanswered basic-auth challenge) leaves promises pending
// forever; without a deadline that silently killed the reconnect loop.
const PROBE_MS = 4000;

// timedFetch is fetch with a hard deadline. AbortSignal.timeout is not in every
// browser this may be opened from, so fall back to an AbortController + timer.
function timedFetch(path, ms) {
  if (AbortSignal.timeout) return fetch(path, { signal: AbortSignal.timeout(ms) });
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), ms);
  return fetch(path, { signal: ac.signal }).finally(() => clearTimeout(t));
}

// sessionStatus returns the server-side status of a session ("live" | "exited" |
// "detached"), null if it is no longer listed (gone), or "unknown" when the server
// could not be asked. "unknown" must NOT be confused with null: only a definite
// "this session is not listed" may end a terminal.
async function sessionStatus(id) {
  try {
    const res = await timedFetch("/api/sessions", PROBE_MS);
    if (!res.ok) return "unknown";
    const list = await res.json();
    const s = (list || []).find((x) => x.id === id);
    return s ? s.status : null;
  } catch (_) {
    return "unknown";
  }
}

// probeServer classifies why we cannot reach the socket: "ok" (server answered),
// "unauthorized" (the login/cookie is stale: a reload is the cure), "unreachable"
// (down, or this page's requests are wedged). A successful probe also re-mints the
// WS cookie server-side, so a stale-cookie tab heals itself here.
async function probeServer() {
  try {
    const res = await timedFetch("/api/meta", PROBE_MS);
    if (res.status === 401 || res.status === 403) return "unauthorized";
    return res.ok ? "ok" : "unreachable";
  } catch (_) {
    return "unreachable";
  }
}

// Grid + tabs live in tabs.js; terminals in terminal-tab.js; viewers in
// viewer-tab.js; the file tree in filetree.js. This file owns boot and the
// sidebar (spawn form, profile editor, session list).

// Sidebar toggle: fully hide the sidebar so the terminal fills the width. State
// persists across reloads (applied pre-paint by the inline <head> script).
// Named so the command palette can invoke the same path as the ☰ button rather
// than duplicating it (or synthesising a click on the DOM node).
function toggleSidebar() {
  const hidden = document.documentElement.classList.toggle("sidebar-hidden");
  try { localStorage.setItem("sidebarHidden", hidden ? "1" : "0"); } catch (_) {}
  renderGrid(); // reflow/refit terminals to the new width
  renderRail48(); // the rail has just appeared or gone
}
$("sidebar-toggle").onclick = toggleSidebar;
$("steer-toggle").onclick = toggleSteerRail;

// renderRail48 paints the icon rail. It rebuilds, so it is guarded by a
// signature: the session list is replaced every three seconds, and a rebuild
// under the pointer swallows a click — the same trap setFocusedCell's comment
// in tabs.js records, where a full renderBar() ate the first press of every
// pane button. Nothing here holds a value, so a signature is all it needs.
let rail48Key = "";
function renderRail48() {
  const rail = $("rail48");
  if (!rail) return;
  const items = railItems(lastSessions, { focusedId: selectedSessionId() });
  const key = items.map((i) => [i.kind, i.id || "", i.activity, i.on ? 1 : 0, i.alert ? 1 : 0].join(":")).join("|");
  if (key === rail48Key) return;
  rail48Key = key;
  const mk = (it) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "ri" + (it.on ? " on" : "") + (it.alert ? " alert" : "") + (it.kind === "new" ? " add" : "");
    b.setAttribute("aria-label", it.label);
    b.title = it.label;
    if (it.kind === "session") {
      const d = document.createElement("span");
      d.className = activityClass(it.activity).cls;
      b.appendChild(d);
      // it.name, not it.label: label carries "— waiting for you" for the
      // button's accessible name, and a freshly-opened tab is titled from this
      // argument permanently (nothing re-syncs a tab title from a later poll).
      b.onclick = () => openTerminal(it.id, it.name);
    } else {
      b.textContent = it.glyph;
      b.onclick = it.kind === "toggle" ? toggleSidebar : () => openSpawnDialog();
    }
    return b;
  };
  const mid = document.createElement("div");
  mid.className = "rin";
  mid.replaceChildren(...items.filter((i) => i.kind === "session").map(mk));
  rail.replaceChildren(mk(items[0]), mid, mk(items[items.length - 1]));
}

// Live count of open dialogs, for shortcutsBlocked (shell.js): the same source
// shouldRefocusTerminal already reads from.
const dialogsOpen = () => document.querySelectorAll("dialog[open]").length;

// Per-pane collapse. The state is two independent booleans and both collapsed
// at once is legal — you get two header bars and an empty rail.
//
// This does NOT call renderGrid(): collapsing a pane rearranges space inside
// the sidebar only, and the terminal grid's width is unchanged. Hiding the
// whole sidebar does change that width, which is why toggleSidebar() refits
// and this does not.
// readCollapsed is the STORAGE read: boot's seed value, and togglePane's
// "what was it before this click" base. Anything that instead needs "what is
// collapsed right now" — a repaint, a guard — must read collapsedFromDOM()
// (shell.js) instead: storage is a snapshot another tab can move past, and a
// throwing write leaves it stale even in one tab.
function readCollapsed() {
  let raw = null;
  try { raw = localStorage.getItem(PANES_KEY); } catch (_) {}
  return loadCollapsed(raw);
}

function applyCollapsed(state) {
  const root = document.documentElement;
  root.classList.toggle("sessions-collapsed", state.sessions);
  root.classList.toggle("files-collapsed", state.files);
  const cv5 = $("sessions-cv"), cvF = $("files-cv");
  if (cv5) {
    cv5.textContent = state.sessions ? "▸" : "▾";
    cv5.setAttribute("aria-expanded", state.sessions ? "false" : "true");
    cv5.title = state.sessions ? "Expand sessions" : "Collapse sessions";
  }
  if (cvF) {
    cvF.textContent = state.files ? "▸" : "▾";
    cvF.setAttribute("aria-expanded", state.files ? "false" : "true");
    cvF.title = state.files ? "Expand files" : "Collapse files";
  }
  $("sessions-phd") && $("sessions-phd").classList.toggle("closed", state.sessions);
  $("files-phd") && $("files-phd").classList.toggle("closed", state.files);
  renderCollapsedSummaries(state);
}

// The summaries are what make a collapsed pane still worth looking at, so they
// have to keep updating while collapsed — this is called from the same places
// that repaint the session list and the tree root, not only on toggle.
function renderCollapsedSummaries(state) {
  const st = state || collapsedFromDOM();
  const pip = $("sessions-pip"), pipText = $("sessions-pip-text");
  if (pip && pipText) {
    const s = collapsedSummary("sessions", { waiting: waitingCount() });
    pipText.textContent = s.text;
    pip.hidden = !(st.sessions && s.dot);
  }
  const path = $("files-path");
  if (path) {
    const f = collapsedSummary("files", { root: ftRoot() });
    path.textContent = f.text;
    path.hidden = !(st.files && f.text);
  }
}

function togglePane(kind) {
  const state = readCollapsed();
  state[kind] = !state[kind];
  try { localStorage.setItem(PANES_KEY, saveCollapsed(state)); } catch (_) {}
  applyCollapsed(state);
}

$("sessions-cv") && ($("sessions-cv").onclick = () => togglePane("sessions"));
$("files-cv") && ($("files-cv").onclick = () => togglePane("files"));
// Applying the collapsed state at boot happens later, in the boot sequence
// near initFileTree() — see the comment there. Assigning these two onclick
// handlers is safe here: it only creates closures, it does not call
// togglePane (and therefore does not touch lastSessions) until a real click.

// Copy/paste hint: platform-aware, dismissable (persisted). Lives outside the
// sidebar so it survives the sidebar-hide toggle.
(function initHint() {
  const hint = $("hint");
  if (!hint) return;
  if (localStorage.getItem("hintDismissed") === "1") { hint.classList.add("hidden"); return; }
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
  // The select-inside-a-TUI bypass key is platform-specific: Option on macOS,
  // Shift elsewhere (see macOptionClickForcesSelection in terminal-tab.js).
  // Non-mac uses Ctrl+Shift+K, not Ctrl+K: Ctrl+K is readline's kill-to-end-of-line
  // inside the shells and TUIs these panes host (see initPalette).
  // Both palette combos are named: another desktop app can hold the primary one
  // system-wide (Notion grabs Ctrl+Shift+K on Windows), and a shortcut that does
  // nothing with no alternative in sight reads as a broken app.
  $("hint-text").textContent = `${mac ? "⌘K" : "Ctrl+Shift+K"} or Ctrl+Shift+Space for commands · Select text to copy · ${mac ? "⌘V" : "Ctrl+V"} to paste · ${mac ? "⌥+drag" : "Shift+drag"} to select inside TUIs (e.g. claude)`;
  $("hint-x").onclick = () => {
    hint.classList.add("hidden");
    try { localStorage.setItem("hintDismissed", "1"); } catch (_) {}
  };
})();

// updateStatusLine keeps the bottom bar describing the current state. Everything
// it shows duplicates something already on screen — the env badge, the sessions
// count, the focused pane — which is why the bar stays dismissible: dismissing
// it loses no unique information.
function updateStatusLine() {
  const env = $("status-env");
  if (env && window.__envLabel) env.textContent = "[" + window.__envLabel.toLowerCase() + "]";
  const s = $("status-sessions");
  if (s) {
    const n = lastSessions.length;
    s.textContent = n === 1 ? "1 session" : n + " sessions";
  }
  const cwd = $("status-cwd");
  if (cwd) {
    const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
    cwd.textContent = t && t.cwd ? shortPath(t.cwd) : "";
  }
}

// renderRateLimits paints the account 5h/weekly usage into the header (once,
// globally). Each window renders independently: an absent one contributes no
// meter at all, and if neither is present the container ends up empty and
// occupies no space, so the header closes up rather than showing a gap.
//
// Built with createElement and textContent, never innerHTML — these values come
// from a file on disk written by an external hook.
function renderRateLimits(rl) {
  const host = $("limits");
  if (!host) return;
  host.replaceChildren();
  const now = Math.floor(Date.now() / 1000);
  const windows = [["5h", rl && rl.five_hour], ["wk", rl && rl.seven_day]];
  for (const [label, win] of windows) {
    const m = meterModel(label, win, now);
    if (!m) continue;
    const el = document.createElement("div");
    el.className = "mtr";
    if (m.hot) el.classList.add("hot");
    const name = document.createElement("span");
    name.textContent = m.label;
    const bar = document.createElement("span");
    bar.className = "bar";
    const fill = document.createElement("i");
    fill.style.width = m.width;
    bar.appendChild(fill);
    const pc = document.createElement("span");
    pc.className = "pc";
    pc.textContent = m.pct + "%";
    const rs = document.createElement("span");
    rs.className = "rs";
    rs.textContent = m.reset;
    el.append(name, bar, pc, rs);
    // The bar carries no text, so the meter needs an accessible name of its own.
    el.setAttribute("role", "img");
    el.setAttribute("aria-label", m.label + " usage " + m.pct + "%, " + m.reset);
    host.appendChild(el);
  }
}

// --- sidebar: session list ---------------------------------------------------
function emptyLi(text) {
  const li = document.createElement("li");
  li.className = "empty";
  li.textContent = text;
  return li;
}
// killing holds ids whose teardown is in flight, so the killing state is
// re-applied on every poll re-render (not just at click time).
const killing = new Set();

// openPeekId is the one expanded row. One at a time: the sidebar is 279px wide
// and several open bodies turn the session list into a scroll.
let openPeekId = null;
// lastPeek caches the most recently fetched peek ({ id, text, pending }). The
// sidebar re-renders the whole list on every poll and re-invokes togglePeek to
// keep an open row expanded (Step 4 below); without a cache that re-invocation
// would blank the body to "Loading…" every few seconds while the fetch is in
// flight, flashing in the reader's face. Cleared on collapse so opening a
// different session never shows a stale row's text.
let lastPeek = null;
// replyDraft holds the in-progress reply text ({ id, text }) across a poll
// re-render for the case where the reply <input> is NOT focused when a poll
// lands (e.g. right after the operator clicks the opt-in checkbox) — without
// it, that rebuild would silently drop whatever had been typed.
//
// It does NOT try to reconstruct focus or caret position. An earlier version
// did (tracking a "focused" flag and calling input.focus() on the new node),
// but removing a focused element from the DOM fires a native, synchronous
// blur on it — so by the time togglePeek's rebuild ran, the flag it read had
// already been set back to false by that blur, and the reconstruction never
// fired. Net effect measured live: the cursor silently dropped out of the
// reply box every ~3s while the operator was still typing, and never came
// back — worse than the bug this mechanism exists to prevent. Reconstructing
// focus across a real node swap is not reliable, so replyFocused (below)
// takes the other approach: stop the swap from happening at all while the
// input is focused, the same way `editing` (below, for inline rename)
// already stops the poll from destroying an in-progress rename.
let replyDraft = null;
// replyFocused mirrors `editing`'s guard shape (see refreshSessions): while
// true, the background poll must not rebuild the session list, because that
// would destroy the focused reply <input> out from under the operator. Set
// by the input's own focus/blur, so it cannot latch on past the operator
// actually leaving the field.
let replyFocused = false;
// pendingSends: session ids with a message request currently in flight, kept
// at module scope like openPeekId / lastPeek / replyDraft above — NOT as a
// local `sending` inside togglePeek's closure, because that closure does not
// reliably outlive one send. refresh() (below) writes `input.disabled =
// closed` on every call, and disabling a focused field blurs it; the blur
// handler clears replyFocused, which is the one thing holding refreshSessions
// back from rebuilding the row (see holdList). So the very next 3s poll can
// tear down this composer and build a fresh one — with its own local
// `sending` starting back at `false` — while the original request is still
// in flight. A flag that must survive a rebuild the poll can trigger has to
// live somewhere the rebuild does not reach.
let pendingSends = new Set();

// peekRowFor: the answer block belonging to a row, if it is open. A sibling,
// not a child — see the stylesheet for why the amber column needs it there.
function peekRowFor(li) {
  const n = li && li.nextElementSibling;
  return n && n.classList && n.classList.contains("peek") ? n : null;
}

// togglePeek expands or collapses the answer block under a session row. The
// fetch happens on expand only — the listing carries just the one-line
// summary. The block is a sibling `<li class="peek">`, not a set of children
// inside the row, so collapsing is a single removal.
async function togglePeek(li, s) {
  const open = peekRowFor(li);
  if (open) {
    open.remove();
    openPeekId = null;
    lastPeek = null;
    replyDraft = null; // collapsing must not let a later session inherit this draft
    replyFocused = false; // defensive: the input is gone, so nothing should stay guarded
    return;
  }
  openPeekId = s.id;
  const peek = document.createElement("li");
  // The block is rendered for every claude session, so it only wears the amber
  // when the row does. Same condition rowClasses uses for the row's own "needs
  // you" class: yellow means an agent is waiting on you and nothing else.
  peek.className = s.activity === "waiting" ? "peek needs" : "peek";
  li.after(peek);

  const body = document.createElement("div");
  body.className = "peektxt muted";
  if (lastPeek && lastPeek.id === s.id) {
    body.textContent = lastPeek.text;
    body.classList.toggle("muted", lastPeek.muted);
    li.dataset.peekPending = lastPeek.pending ? "1" : "";
  } else {
    body.textContent = "Loading…";
  }
  peek.appendChild(body);
  let status = 0;
  let payload = null;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/peek");
    status = res.status;
    if (status === 200) payload = await res.json();
  } catch (_) {
    status = 0;
  }
  if (!li.isConnected || openPeekId !== s.id || !peek.isConnected) return; // a poll re-rendered the row
  const st = peekState(!!s.claude, status, payload);
  body.textContent = st.text;
  body.classList.toggle("muted", st.kind !== "text");
  li.dataset.peekPending = payload && payload.pending ? "1" : "";
  lastPeek = { id: s.id, text: st.text, muted: st.kind !== "text", pending: !!(payload && payload.pending) };

  // Quick replies. A waiting agent is almost always on a numbered prompt, and
  // the whole point of answering from the rail is not having to open a pane to
  // type one character. Each button sends its own digit and nothing else.
  const qk = document.createElement("div");
  qk.className = "qk";
  for (const [digit, word] of [["1", "yes"], ["2", "always"], ["3", "no"]]) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn";
    b.textContent = digit + " " + word;
    b.title = "Send " + digit;
    b.onclick = () => sendReply(digit);
    qk.appendChild(b);
  }
  peek.appendChild(qk);

  // Opt-in toggle. Named in full, never a glyph: this grants a session the
  // ability to be driven from the sidebar without opening it. It has no
  // counterpart in the design — see the stylesheet comment above its rule —
  // and is kept because it is the only control that grants or revokes a
  // session's reply permission; without it the composer would be permanently
  // closed with no way to open it.
  const optin = document.createElement("label");
  optin.className = "peek-optin";
  const cb = document.createElement("input");
  cb.type = "checkbox";
  cb.checked = !!s.replyable;
  const optinText = document.createElement("span");
  optinText.textContent = "Allow replies from here";
  optin.append(cb, optinText);
  peek.appendChild(optin);

  const form = document.createElement("div");
  form.className = "rep";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "fld";
  input.placeholder = "Reply (one line)";
  const send = document.createElement("button");
  send.type = "button";
  send.className = "btn pri";
  send.textContent = "send";
  form.append(input, send);
  const why = document.createElement("span");
  why.className = "peek-why";
  peek.append(form, why);

  // Restore an in-progress draft's TEXT across the poll re-render that just
  // rebuilt this input from scratch. This only matters when the input is not
  // currently focused — while it is, replyFocused (set just below) stops the
  // rebuild from happening in the first place. See replyDraft's declaration
  // for why there is no matching focus/caret restore here.
  if (replyDraft && replyDraft.id === s.id) input.value = replyDraft.text;
  input.oninput = () => { replyDraft = { id: s.id, text: input.value }; };
  // See replyFocused's declaration: this is what keeps the background poll
  // from tearing this input out from under an in-progress keystroke.
  input.onfocus = () => { replyFocused = true; };
  input.onblur = () => { replyFocused = false; };

  // The in-flight latch is pendingSends (module-level, see its declaration) —
  // not a local `sending` here — because this composer's own closure can be
  // torn down and rebuilt by the 3s poll while a send is still outstanding.
  // refresh() feeds off it rather than setting send.disabled directly:
  // refresh() writes the disabled flags from the gate every time it runs, so
  // anything set behind its back is undone by the next call. It closes every
  // control, not only send — the three quick replies are what an operator
  // taps fast, and a double-tapped "2" answers two different questions on a
  // numbered prompt. The server does not dedupe.
  const refresh = () => {
    const st2 = sendState({
      replyable: cb.checked,
      activity: s.activity,
      pending: li.dataset.peekPending === "1",
    });
    const closed = !st2.enabled || pendingSends.has(s.id);
    // Three things move together, deliberately. The dimmed look says closed,
    // the caption says why, and the real disabled attribute makes it true —
    // a control that looks closed but still takes a keystroke is a control
    // whose caption lies, and a keyboard user is the one who finds out.
    send.disabled = closed;
    input.disabled = closed;
    send.classList.toggle("off", closed);
    input.classList.toggle("off", closed);
    for (const b of qk.querySelectorAll(".btn")) {
      b.disabled = closed;
      b.classList.toggle("off", closed);
    }
    why.textContent = st2.reason;
  };
  refresh();

  cb.onchange = async () => {
    const want = cb.checked;
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/replyable", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ replyable: want }),
      });
      if (!res.ok) throw new Error(String(res.status));
      s.replyable = want;
    } catch (_) {
      cb.checked = !want; // the server is the truth; put the box back
      showToast("Could not change the reply setting", { error: true });
    }
    refresh();
  };

  // sendReply is shared by the composer's Send button and the three quick
  // replies: one send path, one error mapping, one refresh().
  async function sendReply(text) {
    if (pendingSends.has(s.id)) return; // a second tap while the first is still in flight
    const err = replyTextError(text);
    if (err) {
      why.textContent = err === "empty" ? "Type something first."
        : err === "newline" ? "One line only — newlines are not sent."
        : "That text contains control characters and was not sent.";
      return;
    }
    pendingSends.add(s.id);
    refresh();
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/message", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: text }),
      });
      if (res.status === 204) {
        input.value = "";
        if (replyDraft && replyDraft.id === s.id) replyDraft = null; // sent — nothing left to restore
        showToast("Sent");
        return;
      }
      // Distinct statuses carry distinct meanings — say which one happened.
      // Routed through showToast, not just why.textContent: a poll can rebuild
      // this row (and detach `why` from the page) while the request is still
      // in flight, the same reason cb.onchange's failure path already toasts
      // instead of relying solely on an in-row element.
      const msg =
        res.status === 403 ? "Replies are not enabled for this session." :
        res.status === 409 ? "Session is not ready for a message right now." :
        res.status === 410 ? "That session is gone." :
        res.status === 400 ? "That text was rejected." :
        "Send failed (" + res.status + ").";
      why.textContent = msg;
      showToast(msg, { error: true });
    } catch (_) {
      const msg = "Send failed: no connection.";
      why.textContent = msg;
      showToast(msg, { error: true });
    } finally {
      // Runs whether the send succeeded, was rejected, or threw — including
      // when the session has since left the list — so the latch never
      // outlives the request that set it.
      pendingSends.delete(s.id);
      refresh();
    }
  }

  send.onclick = () => sendReply(input.value);
}

function renderSession(s) {
  const li = document.createElement("li");
  // rowClasses decides the state; this function only paints it. Selected and
  // needs-you are separate axes and a row can carry both.
  li.className = rowClasses(s, { selectedId: selectedSessionId(), killing: killing.has(s.id) }).join(" ");
  li.dataset.sid = s.id;
  li.title = s.id; // tiebreaker when two labels match

  const act = activityClass(s.activity);
  const adot = document.createElement("span");
  adot.className = act.cls;
  // Someone else is holding this session from another browser. It rides ON the
  // activity dot as an outline rather than as a second marker: the row has one
  // status light, and a second square beside it read as a second state.
  const tab = terminalTabFor(s.id);
  const elsewhere = attachedElsewhere(s, !!tab && tab.status() === "connected");
  if (elsewhere) adot.classList.add("watching");
  const label = elsewhere
    ? (act.title ? act.title + "; someone is viewing this session elsewhere"
                 : "Someone is viewing this session elsewhere")
    : act.title;
  if (label) {
    adot.title = elsewhere
      ? "Someone is viewing this session in another browser or device. Opening it here disconnects them."
      : act.title;
    adot.setAttribute("role", "img");
    adot.setAttribute("aria-label", label);
  }
  li.appendChild(adot);

  const grow = document.createElement("div");
  grow.className = "grow";
  const sname = document.createElement("div");
  sname.className = "sname";
  // A real button, not a span with a click handler: this is the control that
  // opens the session, so it has to be reachable from the keyboard and carry
  // the app's one focus ring without any extra wiring.
  const nm = document.createElement("button");
  nm.type = "button";
  nm.className = "nm";
  nm.textContent = s.name || s.id;
  sname.appendChild(nm);
  grow.appendChild(sname);

  const smeta = document.createElement("div");
  smeta.className = "smeta";
  smeta.textContent = sessionMeta(s, s.cwd ? shortPath(s.cwd) : "", act.title);
  grow.appendChild(smeta);
  li.appendChild(grow);

  if (killing.has(s.id)) {
    const st = document.createElement("span");
    st.className = "bdg";
    st.textContent = "killing…";
    sname.appendChild(st);
    return li;
  }

  nm.onclick = () => openTerminal(s.id, s.name);
  nm.ondblclick = (e) => { e.stopPropagation(); startRename(li, nm, s); };

  // Containment tier. Shown for every live session, not only the risky one: a
  // badge that appears only when something is wrong trains you to ignore its
  // absence.
  const tier = tierBadge(s, boxRuntime);
  if (tier) {
    const tb = document.createElement("span");
    // .bdg.box (var(--ok)) / .bdg.host (var(--warn)) are primitives.css's own
    // flat chip colours — the danger tone reads as host, matching the canvas
    // (the uncontained tier is drawn in warn, not the --warn-fg "needs you"
    // colour, and not --danger).
    tb.className = "bdg tier " + (tier.tone === "danger" ? "host" : "box");
    tb.textContent = tier.text;
    tb.title = tier.title;
    sname.appendChild(tb);
  }

  const right = document.createElement("div");
  right.className = "sright";
  // Context percentage, when there is one to show. This reads the usage already
  // fetched for an open terminal tab; there is deliberately no per-row request,
  // because a fetch per session per poll is a lot of traffic for two characters.
  // No placeholder when it is absent — a dash would read as "zero".
  const u = tab && typeof tab.usage === "function" ? tab.usage() : null;
  if (u && typeof u.pct === "number") {
    const pct = document.createElement("span");
    pct.className = "pct";
    pct.textContent = u.pct + "%";
    pct.title = usageTitle(u);
    right.appendChild(pct);
  }
  const kb = document.createElement("button");
  kb.type = "button";
  kb.className = "kbtn";
  // ⋯ (U+22EF), not three periods and not the ellipsis character.
  kb.textContent = "⋯";
  // A glyph has no accessible name from its own text, so it gets one explicitly.
  kb.setAttribute("aria-label", "Session actions");
  kb.setAttribute("aria-haspopup", "menu");
  kb.title = "Session actions";
  // No stopPropagation: the directory picker and the file tree's row menu both
  // close on a bubble-phase document click, so stopping this one here left them
  // open behind the menu. Letting it through is safe — openSessionMenu has
  // already recorded this button as the menu's anchor by the time the document
  // listener runs, and that listener ignores a click on the anchor.
  kb.onclick = () => openSessionMenu(kb, s);
  right.appendChild(kb);
  li.appendChild(right);

  if (s.claude) {
    // Summary line: what this agent last said. Rendered for EVERY claude
    // session, not only ones with something to say — gating on the text made
    // the panel's empty states and the reply opt-in unreachable in exactly the
    // cases worth explaining. s.claude is the server's own verdict on the
    // binary, not the profile name, because the send path is scoped that way.
    const sum = document.createElement("span");
    sum.className = "summary";
    if (s.summary) { sum.textContent = s.summary; sum.title = "Show the full message"; }
    else { sum.textContent = "No summary"; sum.classList.add("placeholder"); sum.title = "Show why there is no summary"; }
    sum.setAttribute("role", "button");
    sum.setAttribute("tabindex", "0");
    const toggle = (e) => { e.stopPropagation(); togglePeek(li, s); };
    sum.onclick = toggle;
    sum.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") toggle(e); };
    grow.appendChild(sum);
  }
  return li;
}
// editing guards the inline rename: while true, the background poll must NOT
// re-render the list (that would destroy the input + focus) and a stray click
// must not open a pane.
let editing = false;

// startRename swaps the name span for an input; Enter/blur commit, Esc cancels.
// A single finish(commit) guarded by `finished` prevents Enter+blur double-commit
// and Esc+blur commit-on-cancel.
function startRename(li, span, s) {
  if (editing) return;
  // A detached row must not proceed: setting `editing` below freezes the
  // poll's list rebuild (see the comment on `editing`), and an input swapped
  // into a subtree nobody can see can never fire the blur/Enter/Escape that
  // would clear the flag again. Silent bail, not an error — the row can
  // legitimately be gone by the time a caller reaches this.
  if (!li.isConnected) return;
  editing = true;
  const cur = s.name || s.id;
  const input = document.createElement("input");
  input.type = "text";
  input.className = "ren-input";
  input.value = cur;
  // span's parent is .sname now, not li directly (the row grew a .grow/.sname
  // wrapper) — swap it out of its real parent, not the row.
  const parent = span.parentNode;
  let finished = false;
  const finish = async (commit) => {
    if (finished) return;
    finished = true;
    editing = false;
    const v = input.value.trim();
    if (commit && v && v !== cur) {
      try {
        await api("/api/sessions/" + encodeURIComponent(s.id), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: v }),
        });
        retitleTerminal(s.id, v);
      } catch (e) { showToast("Rename failed: " + e.message, { error: true }); }
    }
    refreshSessions();
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); finish(true); }
    else if (e.key === "Escape") { e.preventDefault(); finish(false); }
  };
  input.onblur = () => finish(true);
  parent.replaceChild(input, span);
  input.focus();
  input.select();
}

// lastSessions caches the most recent poll so the command palette can list
// sessions without issuing its own request on every keystroke.
let lastSessions = [];

// The collapsed Sessions bar's pip: how many of the cached sessions are
// waiting for input right now. Derived from lastSessions rather than a
// separate count so it can never drift from what the list itself would show.
function waitingCount() {
  return lastSessions.reduce((n, s) => n + (s.activity === "waiting" ? 1 : 0), 0);
}

// selectedSessionId: which row is "selected". Selected means the focused pane
// is showing THIS session's terminal — not "has a tab open somewhere", which is
// a different question and is answered by the tab strip.
//
// null is a real answer, not a missing one: when the focused pane shows a file
// viewer or holds no tabs at all, no agent is receiving your keystrokes, and
// lighting up whichever row was last selected would say otherwise.
function selectedSessionId() {
  const t = typeof focusedTerminal === "function" ? focusedTerminal() : null;
  return t ? t.id : null;
}

// syncSelectedRow repaints just the selection, without rebuilding the list. The
// focus ring moves on mousedown; the list rebuilds every three seconds. Waiting
// for the poll would leave the highlight visibly behind the click.
function syncSelectedRow() {
  const id = selectedSessionId();
  const ul = $("sessions");
  if (!ul) return;
  for (const li of ul.children) {
    if (!li.classList || !li.classList.contains("srow")) continue;
    li.classList.toggle("sel", !!id && li.dataset.sid === id);
  }
  renderRail48(); // the focus ring moved, so .ri.on must move with it
}

// Returns the live session list, or null if the request failed: the boot-time
// restore must be able to tell "the server says there are none" (a valid answer:
// restore nothing) from "the server could not be asked" (retry, restore nothing
// yet). An empty array is the former.
async function refreshSessions() {
  const ul = $("sessions");
  // holdList: an in-progress inline rename or a focused peek reply is a live
  // <input> INSIDE this list, and rebuilding the list removes that node from
  // the document — which fires a native, synchronous blur and drops the
  // operator's cursor mid-keystroke.
  //
  // The guard used to sit at the top of this function and skip the whole poll.
  // That protected the input, but it also froze every other thing the poll
  // drives — activity dots, per-tab usage, the session count, the status line,
  // and lastSessions (which the palette reads) — with nothing on screen saying
  // the control plane had stopped. A rename commits within seconds; a reply box
  // can hold focus for as long as the operator thinks, or walks away. A frozen
  // green "working" dot on a crashed agent is a defect this codebase has fixed
  // once already, so it must not come back through the reply box.
  //
  // So: always fetch, always push activity/usage/count/status, and skip ONLY
  // the DOM rebuild. Every replaceChildren on `ul` below is guarded, and each
  // is checked SYNCHRONOUSLY at the moment of the rebuild — after the await,
  // never before it. Nothing but a rebuild can clear replyFocused (it is set
  // and cleared by the input's own focus/blur), so the flag cannot have gone
  // stale between the check and the call: the check and the call are adjacent
  // statements with no await between them.
  const holdList = () => editing || replyFocused;
  let sessions;
  try { sessions = await api("/api/sessions"); }
  catch (e) {
    if (!holdList()) ul.replaceChildren(emptyLi("Error: " + e.message));
    return null;
  }
  lastSessions = sessions || [];
  // Push each session's activity onto its open terminal tab (a different axis
  // from the tab's connection status). setActivity is a no-op when unchanged, so
  // this only re-renders a bar whose dot actually moved.
  const actById = new Map(lastSessions.map((s) => [s.id, s.activity]));
  // A tab whose session has left the list (killed/pruned) would otherwise keep
  // its last dot — a stale green "working" on a dead agent. Fall back to "done".
  eachTerminal((t) => { t.setActivity(actById.has(t.id) ? actById.get(t.id) : "done"); });
  // Repaint every open pane's answer-strip control state (disabled/.off/title)
  // against the session list this poll just refreshed. setActivity above is a
  // no-op when activity is unchanged, so a pane whose replyable flag was
  // revoked (or that entered a tool call) while its activity stayed "waiting"
  // would otherwise never repaint until activity next moves — the strip would
  // keep looking answerable after the real gate had already closed. This is
  // paint-only (see Cell.repaintReply): it never rebuilds the strip or touches
  // the field's value, so a caret mid-reply is untouched. It intentionally does
  // NOT re-fetch /peek's pending flag — post() always re-fetches that itself
  // immediately before it actually sends, so a stale pending here only ever
  // makes a control look more open than it is, never less closed than reality.
  cells.forEach((c) => c.repaintReply());
  // Same treatment for the steer rail's composer, and for the same reason: it
  // re-gates, it never rebuilds, so a half-typed reply keeps its caret.
  if (typeof repaintSteerRail === "function") repaintSteerRail();
  // Per-window usage: fetch only for open terminal tabs whose session is still
  // listed (alive), and only when visible. 204 (non-claude / no transcript) →
  // null → no readout. A gone session keeps its last usage rather than blanking.
  if (!document.hidden) {
    eachTerminal((t) => {
      if (!actById.has(t.id)) return;
      api("/api/sessions/" + encodeURIComponent(t.id) + "/usage")
        .then((u) => t.setUsage(u))
        .catch(() => {});
    });
  }
  const cnt = $("sessions-count");
  if (cnt) cnt.textContent = String(lastSessions.length);
  renderRail48(); // the list changed
  updateStatusLine();
  renderCollapsedSummaries();
  // A menu whose session has left the list would act on a dead id. Only that
  // case closes it: the list is replaced under the menu constantly, and the
  // menu is built to survive exactly that.
  const openSid = sessionMenuId();
  if (openSid && !lastSessions.some((s) => s.id === openSid)) closeSessionMenu(false);
  // Everything above has already been pushed. Only the list rebuild waits.
  if (holdList()) return lastSessions;
  if (lastSessions.length === 0) { ul.replaceChildren(emptyLi("No sessions yet")); return lastSessions; }
  const rows = lastSessions.map(renderSession);
  ul.replaceChildren(...rows);
  // The answer block is a sibling, so it can only be inserted once its row is
  // in the document. Re-opening after the attach also keeps the open panel from
  // collapsing under the reader every few seconds.
  const reopen = lastSessions.find((s) => s.id === openPeekId);
  if (reopen) {
    const li = rows[lastSessions.indexOf(reopen)];
    if (li && !peekRowFor(li)) togglePeek(li, reopen);
  }
  syncSelectedRow();
  return lastSessions;
}

// --- spawn / kill ------------------------------------------------------------
let profiles = [];
// profilesLoaded distinguishes "the server has no profiles" from "we could not
// ask". Without it, one transient failure at boot leaves the primary spawn
// button disabled under a caption that blames the config, and in dev, `air`
// restarts the daemon under an open tab routinely. Retries once, like initEnv.
let profilesLoaded = false;
async function loadProfiles(retry = true) {
  try {
    profiles = (await api("/api/profiles")) || [];
    profilesLoaded = true;
    $("agent").replaceChildren(...profiles.map((p) => {
      const o = document.createElement("option");
      o.value = p.name; o.textContent = p.name;
      return o;
    }));
    renderOptions();
    renderProfileList();
  } catch (_) {
    if (retry) setTimeout(() => loadProfiles(false), 2000);
  }
}
function currentProfile() {
  return profiles.find((p) => p.name === $("agent").value);
}
// renderOptions builds a toggle per option the selected profile exposes
// (checkbox for bool, select for enum), pre-set to the profile's default.
function renderOptions() {
  const p = currentProfile();
  const opts = (p && p.options) || [];
  $("options").replaceChildren(...opts.map((o) => {
    const row = document.createElement("div");
    row.className = "opt";
    const lbl = document.createElement("label");
    lbl.textContent = o.label || o.key;
    lbl.htmlFor = "opt-" + o.key;
    let ctrl;
    if (o.type === "bool") {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = o.value === true;
    } else {
      ctrl = document.createElement("select");
      for (const c of o.choices || []) {
        const co = document.createElement("option");
        co.value = c; co.textContent = c;
        if (c === o.value) co.selected = true;
        ctrl.appendChild(co);
      }
    }
    ctrl.id = "opt-" + o.key;
    ctrl.dataset.key = o.key;
    ctrl.dataset.type = o.type;
    row.append(lbl, ctrl);
    return row;
  }));
}
function getOptions() {
  const out = {};
  for (const ctrl of $("options").querySelectorAll("[data-key]")) {
    out[ctrl.dataset.key] = ctrl.dataset.type === "bool" ? ctrl.checked : ctrl.value;
  }
  return out;
}
// Switching profile re-derives everything that profile owns: its options AND the
// tier it runs under. Leaving the tier behind would show the previous profile's
// containment while naming this one.
$("agent").addEventListener("change", () => {
  renderOptions();
  syncRuntimeField();
  applyProfileCwd();
});

// applyProfileCwd overwrites the workspace picker with the selected profile's
// stored default_cwd. A profile without one leaves the picker untouched (there
// is nothing to overwrite with), preserving the keep-previous behavior.
function applyProfileCwd() {
  const p = currentProfile();
  const rt = ($("runtime-select") && $("runtime-select").value) || "";
  const sel = workspaceSelectionForCwd(workspacesCache, rt, p && p.default_cwd);
  if (!sel) return;
  const ws = $("workspace");
  ws.value = sel.value;
  $("cwd-custom").value = sel.custom;
  $("cwd-custom").style.display = ws.value === "__custom__" ? "block" : "none";
}

// renderPfWorkspaceOptions builds the profile-editor working-dir picker for the
// profile's tier: a "(no default)" entry, the tier-filtered workspace roots, then
// a Custom-path escape hatch. `cwd` is the value to preselect (a profile's stored
// default_cwd, or the current pick when re-rendering after a Runtime change).
function renderPfWorkspaceOptions(cwd) {
  const sel = $("pf-cwd");
  if (!sel) return;
  const tier = segValue($("pf-runtime")) === "host" ? "host" : "box";
  const none = document.createElement("option");
  none.value = ""; none.textContent = "(no default)";
  const opts = [none, ...workspaceOptions(workspacesCache, tier).map((o) => {
    const el = document.createElement("option");
    el.value = o.value; el.textContent = o.label; el.disabled = o.disabled;
    return el;
  })];
  const custom = document.createElement("option");
  custom.value = "__custom__"; custom.textContent = "Custom path…";
  opts.push(custom);
  sel.replaceChildren(...opts);

  // Place the value: workspaceSelectionForCwd returns null for empty -> "(no default)".
  const pick = workspaceSelectionForCwd(workspacesCache, tier, cwd);
  const customInput = $("pf-cwd-custom");
  if (!pick) {
    sel.value = "";
    customInput.value = "";
  } else {
    sel.value = pick.value;
    customInput.value = pick.custom;
  }
  customInput.hidden = sel.value !== "__custom__";
}

// --- profile editor (structured create / edit / delete of runtime profiles) --
let optionDefs = [];
async function loadOptionDefs() {
  try { optionDefs = (await api("/api/options")) || []; } catch (_) {}
}
// optApplies mirrors config.OptionDef.AppliesTo: no bins → all agents; else match
// the bin or its basename. Keeps the editor's option list scoped to the agent.
function optApplies(o, bin) {
  if (!o.bins || o.bins.length === 0) return true;
  const base = (bin || "").split("/").pop();
  return o.bins.includes(bin) || o.bins.includes(base);
}
// buildSeg renders a segmented control into a container that already carries
// the .seg class, and reports the new value through onChange. The value lives
// on the container (see profileform.js) because buttons have no value of
// their own, and every reader goes through segValue rather than reaching for
// a button's class.
function buildSeg(el, choices, value, onChange) {
  el.replaceChildren(...choices.map((c) => {
    const b = document.createElement("button");
    b.type = "button";
    b.dataset.segvalue = c.value;
    b.className = c.danger ? "dgr" : "";
    b.textContent = c.label;
    b.onclick = () => { setSegValue(el, c.value); if (onChange) onChange(c.value); };
    return b;
  }));
  setSegValue(el, value);
  return el;
}
// Option definitions come from the daemon's config (GET /api/options), so the
// choice count is runtime data, not something knowable at build time. A <=4
// choice enum renders as a .seg — seeing the alternatives beats saving the
// space a select would — and anything wider stays a plain <select> so it
// cannot overflow a narrow column. A bool option is always a checkbox.
function renderPfOptions(current) {
  current = current || {};
  const bin = $("pf-bin").value.trim();
  $("pf-options").replaceChildren(...optionDefs.filter((o) => optApplies(o, bin)).map((o) => {
    const row = document.createElement("div");
    row.className = "frow";
    const col = document.createElement("div");
    col.className = "fcol";
    const lbl = document.createElement("label");
    let ctrl;
    if (o.type === "bool") {
      // A checkbox is its own label; it does not get a .flab above it.
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = current[o.key] === true;
      lbl.textContent = o.label || o.key;
    } else {
      lbl.className = "flab";
      lbl.textContent = o.label || o.key;
      const choices = o.choices || [];
      if (choices.length > 0 && choices.length <= 4) {
        ctrl = document.createElement("div");
        ctrl.className = "seg";
        buildSeg(ctrl, choices.map((c) => ({ value: c, label: c })), current[o.key] || "");
      } else {
        ctrl = document.createElement("select");
        ctrl.className = "fld";
        const none = document.createElement("option");
        none.value = ""; none.textContent = "(not set)";
        ctrl.appendChild(none);
        for (const c of choices) {
          const co = document.createElement("option");
          co.value = c; co.textContent = c;
          if (current[o.key] === c) co.selected = true;
          ctrl.appendChild(co);
        }
      }
    }
    ctrl.dataset.optkey = o.key;
    ctrl.dataset.opttype = o.type;
    col.append(lbl, ctrl);
    row.appendChild(col);
    return row;
  }));
}
// renderPfRuntime builds the containment .seg. Which directories are
// selectable depends on the tier, and Save's caption depends on it too (see
// profileform.js's saveLabel), so both are re-derived the moment it changes —
// leaving either behind is how a workspace that voids the box, or a stale
// Save caption, gets left on screen.
function renderPfRuntime(value) {
  const el = $("pf-runtime");
  buildSeg(el, [
    { value: "", label: "box" },
    { value: "host", label: "host", danger: true },
  ], value || "", (v) => {
    $("pf-runtime-warn").hidden = v !== "host";
    renderPfWorkspaceOptions(pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value));
    syncEditorActions();
  });
  $("pf-runtime-warn").hidden = (value || "") !== "host";
}
// The allowlist's live rows, as the editor sees them: envRows(cfg) turns a
// saved profile into rows, the table renders and edits them in place, and
// rowsToConfig turns them back into the two fields the server stores.
let pfEnvRows = [];
function fillForm(p) {
  $("pf-name").value = p.name || "";
  $("pf-bin").value = p.bin || "";
  $("pf-base").value = p.base || "inherit";
  renderPfRuntime(p.runtime || "");
  $("pf-args").value = (p.args || []).join("\n");
  pfEnvRows = envRows(p);
  renderEnvRows($("pf-env-rows"), pfEnvRows, () => setDialogError("settings-modal", rowError(pfEnvRows)));
  renderPfWorkspaceOptions(p.default_cwd || "");
  renderPfOptions(p.options || {});
  syncEditorActions();
}
// syncEditorActions is the single place that decides what the editor may do,
// so a dimmed button and the reason it is dimmed can never disagree. .off is
// only the look; the real disabled attribute is what actually closes a
// control, and without it a dimmed button still takes a click and a
// keystroke.
function syncEditorActions() {
  const view = profiles.find((p) => p.name === selectedProfileName()) || null;
  const st = editorState(view);
  $("pf-lock").hidden = !st.locked;
  for (const [id, state] of [["pf-save", st.save], ["pf-delete", st.delete], ["pf-duplicate", st.duplicate]]) {
    const b = $(id);
    b.classList.toggle("off", state === "off");
    b.disabled = state === "off";
  }
  // Every interactive control the editor pane holds: a locked profile's fields
  // (.fld), its containment/option .seg buttons, its bare bool checkboxes (no
  // class of their own), and the allowlist's row buttons and "+ add" triggers
  // (.btn) — a read-only profile that still lets a row be added or removed is
  // the exact thing #pf-lock's caption is promising will not happen.
  for (const f of $("manage").querySelectorAll(".ped .fld, .ped .seg button, .ped .btn, .ped input[type=checkbox]")) {
    f.classList.toggle("off", st.locked);
    f.disabled = st.locked;
  }
  $("pf-save").textContent = saveLabel(segValue($("pf-runtime")));
}
// The editor lives in a closed <dialog> at boot, where focus() is a silent
// no-op; only grab focus when the dialog is actually on screen.
function newProfile() {
  selectedProfile = "";
  fillForm({ base: "inherit" });
  setDialogError("settings-modal", "");
  renderProfileList();
  syncEditorActions();
  if ($("settings-modal").open) $("pf-name").focus();
}
function readForm() {
  const p = { name: $("pf-name").value.trim(), bin: $("pf-bin").value.trim(), base: $("pf-base").value };
  // Blank means "use the default tier", which the server resolves to box. Sending
  // it explicitly would hard-code today's default into every stored profile.
  const rt = segValue($("pf-runtime"));
  if (rt) p.runtime = rt;
  const args = $("pf-args").value.split("\n").map((s) => s.trim()).filter(Boolean);
  if (args.length) p.args = args;
  const { env, secrets } = rowsToConfig(pfEnvRows);
  if (Object.keys(env).length) p.env = env;
  if (secrets.length) p.secrets = secrets;
  const opts = {};
  for (const ctrl of $("pf-options").querySelectorAll("[data-optkey]")) {
    if (ctrl.dataset.opttype === "bool") opts[ctrl.dataset.optkey] = ctrl.checked;
    // A <=4-choice enum renders as a .seg (see renderPfOptions): its value
    // lives in data-value on the container, not in a form control's .value.
    else if (ctrl.classList.contains("seg")) {
      const v = segValue(ctrl);
      if (v) opts[ctrl.dataset.optkey] = v;
    } else if (ctrl.value) opts[ctrl.dataset.optkey] = ctrl.value;
  }
  if (Object.keys(opts).length) p.options = opts;
  const cwd = pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value);
  if (cwd) p.default_cwd = cwd;
  return p;
}
// These three run while #settings-modal is open, so their errors go to the
// dialog's own inline line. A toast is fixed-position and paints beneath a modal
// backdrop, which would leave the failure invisible.
async function saveProfile() {
  const p = readForm();
  setDialogError("settings-modal", "");
  if (!p.name || !p.bin) { setDialogError("settings-modal", "Name and Command are required."); return; }
  const rowsErr = rowError(pfEnvRows);
  if (rowsErr) { setDialogError("settings-modal", rowsErr); return; }
  // Containment is the one field whose consequence is worth naming before it is
  // written. box needs no question; host gives up the whole point of the box.
  const ask = containmentConfirm(segValue($("pf-runtime")), p.name);
  if (ask && !(await askConfirm(ask))) return;
  const editing = profiles.some((x) => x.name === p.name && x.editable);
  const path = editing ? "/api/profiles/" + encodeURIComponent(p.name) : "/api/profiles";
  try {
    await api(path, { method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) });
  } catch (e) { setDialogError("settings-modal", "Save failed: " + e.message); return; }
  await loadProfiles();
}
// Duplicate is what makes a read-only built-in useful: a copy you own, seeded
// with what it does. It reads the editor exactly as Save would, only the name
// changes and the destination is always a new profile (POST, never PUT).
async function duplicateProfile() {
  const name = selectedProfileName();
  if (!name) return;
  setDialogError("settings-modal", "");
  const p = readForm();
  p.name = duplicateName(name, profiles.map((x) => x.name));
  try {
    await api("/api/profiles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(p) });
  } catch (e) { setDialogError("settings-modal", "Duplicate failed: " + e.message); return; }
  await loadProfiles();
  await editProfile(p.name);
}
async function editProfile(name) {
  setDialogError("settings-modal", "");
  selectedProfile = name;
  // Same drill-down as newProfile(); inert above 700px, where both columns
  // are already on screen.
  $("settings-modal").classList.add("editing");
  try { fillForm(await api("/api/profiles/" + encodeURIComponent(name))); openSettings("manage"); }
  catch (e) { setDialogError("settings-modal", "Load failed: " + e.message); }
  renderProfileList();
}
async function deleteProfile(name) {
  // The sibling destructive action (kill session) has always confirmed; this one
  // used to delete on the first click.
  const ok = await askConfirm({
    title: "Delete profile",
    body: 'Delete the profile "' + name + '"? This cannot be undone.',
    confirmLabel: "Delete",
    danger: true,
  });
  if (!ok) return;
  setDialogError("settings-modal", "");
  try { await api("/api/profiles/" + encodeURIComponent(name), { method: "DELETE" }); }
  catch (e) { setDialogError("settings-modal", "Delete failed: " + e.message); return; }
  await loadProfiles();
}
// The list shows every profile, built-ins included: a built-in is a thing you
// can read and duplicate, not a thing that should be invisible. `editable` is
// the server's own verdict (a profile is read-only precisely because it exists
// only as a code-level seed and has never been written to the store), so the
// marker is not inferred from the name.
let selectedProfile = "";
function selectedProfileName() { return selectedProfile; }
function renderProfileList() {
  $("pf-list").replaceChildren(...profiles.map((p) => {
    const li = document.createElement("li");
    const b = document.createElement("button");
    b.type = "button";
    b.className = "pitem" + (p.name === selectedProfile ? " on" : "");
    const nm = document.createElement("span");
    nm.textContent = p.name;
    b.appendChild(nm);
    if (!p.editable) {
      const lk = document.createElement("span");
      lk.className = "lk";
      lk.textContent = "built-in";
      b.appendChild(lk);
    }
    b.onclick = () => editProfile(p.name);
    li.appendChild(b);
    return li;
  }));
}
$("pf-save").addEventListener("click", saveProfile);
// newProfile() also runs once at boot (below) to seed the editor pane with a
// blank form before any profile is loaded — that call must NOT drill into the
// mobile editor view, so the class is added here, at the actual button press,
// rather than inside newProfile() itself.
$("pf-new").addEventListener("click", () => {
  newProfile();
  $("settings-modal").classList.add("editing");
});
// The drill-down's only way back to the list; inert above 700px, where the
// button itself stays hidden.
$("pf-back").addEventListener("click", () => {
  $("settings-modal").classList.remove("editing");
  renderProfileList();
});
$("pf-delete").addEventListener("click", () => deleteProfile(selectedProfileName()));
$("pf-duplicate").addEventListener("click", duplicateProfile);
// The two "+ add" buttons append a blank row of the matching kind and hand
// focus to its name field, so typing the variable name is the very next thing
// that happens.
function addEnvRow(kind) {
  pfEnvRows.push({ key: "", value: "", kind });
  renderEnvRows($("pf-env-rows"), pfEnvRows, () => setDialogError("settings-modal", rowError(pfEnvRows)));
  const rows = $("pf-env-rows").querySelectorAll(".kvrow");
  const last = rows[rows.length - 1];
  if (last) last.querySelector(".k").focus();
}
$("pf-env-add").addEventListener("click", () => addEnvRow("literal"));
$("pf-secret-add").addEventListener("click", () => addEnvRow("secret"));
// Re-scope the option list when the agent binary changes.
$("pf-bin").addEventListener("input", () => renderPfOptions(readForm().options));
$("pf-cwd").addEventListener("change", () => {
  $("pf-cwd-custom").hidden = $("pf-cwd").value !== "__custom__";
});
// pf-runtime's own change is handled by renderPfRuntime's buildSeg onChange —
// a .seg is buttons, not a form control, so it has no native "change" event.
// workspacesCache backs the tier-aware re-render: switching Runtime must be able
// to re-evaluate which directories are selectable without another round trip.
let workspacesCache = [];

async function loadWorkspaces() {
  try {
    workspacesCache = (await api("/api/workspaces")) || [];
    renderWorkspaceOptions();
  } catch (_) {}
}

// renderWorkspaceOptions rebuilds the picker for the tier currently selected.
// On the boxed tier a directory that cannot be contained is disabled and shows
// why, because choosing a workspace IS choosing the containment boundary.
function renderWorkspaceOptions() {
  const sel = $("workspace");
  if (!sel) return;
  const rt = ($("runtime-select") && $("runtime-select").value) || "";
  const prev = sel.value;
  const opts = workspaceOptions(workspacesCache, rt).map((o) => {
    const el = document.createElement("option");
    el.value = o.value; el.textContent = o.label; el.disabled = o.disabled;
    return el;
  });
  const custom = document.createElement("option");
  custom.value = "__custom__"; custom.textContent = "Custom path…";
  opts.push(custom);
  sel.replaceChildren(...opts);
  // Keep the previous choice only if it is still usable on this tier; otherwise
  // fall to the first usable one so the form never opens on a doomed default.
  const stillOK = opts.some((o) => o.value === prev && !o.disabled);
  sel.value = stillOK ? prev : (defaultWorkspace(workspacesCache, rt) || "__custom__");
  $("cwd-custom").style.display = sel.value === "__custom__" ? "block" : "none";
}
$("workspace").addEventListener("change", () => {
  $("cwd-custom").style.display = $("workspace").value === "__custom__" ? "block" : "none";
});
function getCwd() {
  const w = $("workspace").value;
  return w === "__custom__" ? $("cwd-custom").value.trim() : (w || "");
}

// boxRuntime is the isolation runtime the daemon detected ("runc"/"sysbox-runc"),
// or "" when containment is unavailable. Empty means every session is host tier,
// and the tier chooser is hidden rather than offering a choice that does nothing.
let boxRuntime = "";

// The last spawn is remembered only to PRE-FILL the form, never to act on its
// own: the form is always shown, so a stale value can be seen and changed
// before anything starts.
const LAST_SPAWN_KEY = "agent-box:lastSpawn";
function loadLastSpawn() {
  try {
    const v = JSON.parse(localStorage.getItem(LAST_SPAWN_KEY) || "null");
    return v && typeof v.agent === "string" ? v : null;
  } catch (_) {
    return null;
  }
}

// --- the "+ New session" button ----------------------------------------------
// Spawning always goes through the form. Under containment the workspace is the
// box's only writable mount, so choosing it IS choosing what the agent can
// reach: a one-click spawn that picked a directory for you was choosing your
// security boundary silently, and its default (home) could not be contained at
// all. One deliberate choice per session is the honest cost of the model.

// spawnSession is the single POST path for every agent spawn, so "last used"
// and the undo toast are recorded in exactly one place. spawnShellIn
// deliberately does NOT route through here: a quick shell must never become
// what "+ New session" does.
async function spawnSession(req, opts) {
  opts = opts || {};
  const s = await api("/api/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  try {
    localStorage.setItem(LAST_SPAWN_KEY, JSON.stringify({
      agent: req.agent, cwd: req.cwd || "", options: req.options || {},
    }));
  } catch (_) {}
  await refreshSessions();
  openTerminal(s.id, s.name);
  if (opts.toast !== false) {
    // Report the directory the SERVER chose, not the one requested.
    const where = s.cwd || req.cwd;
    showToast('Spawned "' + (s.name || s.id) + '" in ' + (where ? shortPath(where) : "the default dir"),
      { undo: () => killSession(s.id) });
  }
  return s;
}

// syncRuntimeField shows the tier chooser only when containment is actually
// available. With no box layer every session is host tier anyway, and offering
// a choice that changes nothing would be a lie.
//
// The chooser opens on the tier the SELECTED PROFILE runs under (server-resolved
// in ProfileView.runtime), not on a fixed "box". It used to hard-code box on
// every open and the submit always sent that value, so a profile saved as host —
// and even an instance whose default_runtime is host — was overridden on every
// spawn and the setting could never take effect. It is still not sticky: the
// value comes from the profile each time, never from the last spawn, and a
// profile that declares nothing still resolves to contained.
function syncRuntimeField() {
  const row = $("runtime-row"), sel = $("runtime-select");
  if (!row || !sel) return;
  row.style.display = boxRuntime ? "" : "none";
  sel.value = spawnRuntimeDefault(currentProfile(), boxRuntime);
  updateRuntimeWarning(); // also re-renders the workspace list for this tier
}

function updateRuntimeWarning() {
  const sel = $("runtime-select"), warn = $("runtime-warn");
  if (!sel || !warn) return;
  warn.hidden = sel.value !== "host";
  renderWorkspaceOptions(); // selectable directories depend on the tier
}

// openSpawnDialog preselects whatever the one-click button would have done, so
// "Advanced…" elaborates the visible target instead of presenting a fresh form.
function openSpawnDialog(preselectAgent) {
  const last = loadLastSpawn();
  const want = preselectAgent && profiles.some((p) => p.name === preselectAgent)
    ? preselectAgent
    : (last && last.agent && profiles.some((p) => p.name === last.agent) ? last.agent : "");
  if (want) {
    $("agent").value = want;
    renderOptions();
  }
  // Tier first: it decides which workspaces are selectable at all.
  syncRuntimeField();
  // Profile default_cwd wins over the last spawn's cwd — applyProfileCwd()
  // pre-fills from the (possibly programmatically) preselected profile, and
  // last.cwd only kicks in as a fallback when that profile has none set
  // (applyProfileCwd() is then a no-op, leaving the picker untouched).
  applyProfileCwd();
  const ws = $("workspace");
  const profile = currentProfile();
  if (last && last.cwd && !(profile && profile.default_cwd)) {
    const usable = Array.prototype.some.call(ws.options, (o) => o.value === last.cwd && !o.disabled);
    if (usable) ws.value = last.cwd;
  }
  $("cwd-custom").style.display = ws.value === "__custom__" ? "block" : "none";
  openDialog("spawn-modal");
  $("sess-name").focus();
}

function initSpawnControl() {
  const rt = $("runtime-select");
  if (rt) rt.onchange = updateRuntimeWarning;
  $("spawn-go").onclick = () => openSpawnDialog();
}

$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const req = { agent: $("agent").value, name: $("sess-name").value.trim(), cwd: getCwd(), options: getOptions() };
  const rt = $("runtime-select");
  if (rt && rt.value) req.runtime = rt.value;
  // Close first: a fixed-position toast renders *beneath* a modal dialog's
  // backdrop, so an Undo offered while the dialog is still up is unreachable.
  closeDialog("spawn-modal");
  $("sess-name").value = "";
  try {
    await spawnSession(req);
  } catch (e) { showToast("Spawn failed: " + e.message, { error: true }); }
});
// The row's actions menu. One persistent node outside the list, rebuilt on
// every open — the list is replaced wholesale every few seconds, so a menu
// nested in a row would be torn out from under the pointer. Items close over
// the session id and name, never over a row element, so a rebuild underneath an
// open menu changes nothing.
let sessionMenuSid = null;
let sessionMenuAnchor = null;

function sessionMenuId() { return sessionMenuSid; }

function closeSessionMenu(restoreFocus) {
  const menu = $("session-menu");
  if (menu) menu.classList.remove("open");
  const a = sessionMenuAnchor;
  sessionMenuSid = null;
  sessionMenuAnchor = null;
  // Focus goes back where it came from, but only when the trigger is still on
  // the page: a poll may have replaced the row while the menu was open.
  if (restoreFocus && a && a.isConnected) a.focus();
}

function openSessionMenu(anchor, s) {
  const menu = $("session-menu");
  if (!menu) return;
  const name = s.name || s.id;
  const run = {
    // Resolved from the session id at click time, never from `anchor`: the
    // list rebuilds under the menu every few seconds (the menu is
    // deliberately not in holdList()), so by the time an item is chosen the
    // trigger that opened the menu may already be sitting in a detached
    // subtree. `sessionRowFor` looks up whatever row is live right now.
    rename: () => { const li = sessionRowFor(s.id); const nm = li && li.querySelector(".nm"); if (li && nm) startRename(li, nm, s); },
    // Through syncTreeToRoot, not a setFtRoot + ftLoadRoot pair of its own: that
    // pair is missing the dpClose() the real re-root does, and that close is
    // load-bearing. setFtRoot writes the root input's value programmatically and
    // fires no "input" event, so a fuzzy search debounced a moment earlier lands
    // AFTER the re-root and renders its hits underneath a path value.
    //
    // No follow-key: this is an explicit request and must re-root even when the
    // tree is already following this session, and passing none also clears the
    // guard, so the tree resumes following the focused tab at the next focus
    // change. Unawaited from a click handler, hence the catch.
    files: () => { syncTreeToRoot(s.cwd).catch(() => {}); },
    attribution: () => openSteerFor(s.id),
    copycwd: () => copyPath(s.cwd),
    newpane: () => openTerminal(s.id, name, { newPane: true }),
    end: () => confirmKill(s.id, name),
  };
  const nodes = menuItems(s).map((it) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = it.danger ? "mi dgr" : "mi";
    b.setAttribute("role", "menuitem");
    b.textContent = it.label;
    // Close first, then act: an action that opens a dialog would otherwise
    // leave the menu floating over it.
    b.onclick = () => { closeSessionMenu(false); const fn = run[it.id]; if (fn) fn(); };
    return b;
  });
  menu.replaceChildren(...nodes);
  sessionMenuSid = s.id;
  sessionMenuAnchor = anchor;

  menu.classList.add("open"); // must be visible before it can be measured
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  menu.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) + "px";
  // Rows sit anywhere down the rail, so a menu opened near the bottom flips
  // above its row rather than clipping. The height has to be measured AFTER the
  // items are in, because the item list varies per session.
  const h = menu.offsetHeight;
  const below = r.bottom + 4;
  menu.style.top = (below + h <= window.innerHeight - 8 ? below : Math.max(8, r.top - h - 4)) + "px";
  // Focus lands on the first item. The precedent popover in this app leaves
  // focus on the trigger, which puts its own items somewhere unpredictable in
  // the tab order — acceptable for a menu of file actions, not for one whose
  // last item ends a running agent.
  const first = menu.querySelector(".mi");
  if (first) first.focus();
}

// sessionRowFor(id): the live row for a session id, or null if none is on
// screen right now (killed, exited, or a poll rebuilt without it). Looked up
// by data-sid rather than captured as a DOM reference, on the same grounds
// syncSelectedRow already does its own scan this way: the list is replaced
// wholesale every few seconds, independently of anything holding onto a node
// from an earlier render.
function sessionRowFor(id) {
  const ul = $("sessions");
  if (!ul) return null;
  for (const li of ul.children) {
    if (li.classList && li.classList.contains("srow") && li.dataset.sid === id) return li;
  }
  return null;
}

document.addEventListener("click", (e) => {
  const menu = $("session-menu");
  if (!menu || !menu.classList.contains("open")) return;
  if (!menu.contains(e.target) && e.target !== sessionMenuAnchor) closeSessionMenu(false);
});
document.addEventListener("keydown", (e) => {
  const menu = $("session-menu");
  if (!menu || !menu.classList.contains("open")) return;
  if (e.key === "Escape") { e.preventDefault(); closeSessionMenu(true); return; }
  if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
  // Arrow navigation, which the app's other popover does not have. Same reason
  // as the focus move above: reaching a destructive item must not depend on
  // tabbing blindly through the document.
  const items = Array.from(menu.querySelectorAll(".mi"));
  if (items.length === 0) return;
  e.preventDefault();
  const at = items.indexOf(document.activeElement);
  const next = e.key === "ArrowDown"
    ? (at < 0 ? 0 : (at + 1) % items.length)
    : (at < 0 ? items.length - 1 : (at - 1 + items.length) % items.length);
  items[next].focus();
});

// confirmKill is the guarded entry point (the tab strip's kill button, the
// command palette, and the row menu's End session… item). killSession itself
// stays unguarded: Undo on a fresh spawn must not ask twice.
async function confirmKill(id, name) {
  const ok = await askConfirm({
    title: "Kill session",
    body: 'Kill session "' + (name || id) + '"? This cannot be undone.',
    confirmLabel: "Kill",
    danger: true,
  });
  if (ok) killSession(id);
}

async function killSession(id) {
  if (killing.has(id)) return; // already tearing down: ignore repeat clicks
  killing.add(id);
  await refreshSessions(); // re-render → row shows "killing…"
  try {
    await api("/api/sessions/" + encodeURIComponent(id), { method: "DELETE" });
  } catch (e) {
    killing.delete(id);
    await refreshSessions();
    showToast("Kill failed: " + e.message, { error: true });
    return;
  }
  killing.delete(id);
  closeTerminal(id);
  await refreshSessions(); // session gone → row removed
}

// --- quick shell -------------------------------------------------------------
// Spawn the detected shell profile in a cell's working dir, then hand off to the
// normal openTerminal path so auto-split/cap placement stays in one place.
// Per-cell (not global) in-flight guard: a double click on one window is ignored,
// but +sh on another window still works instead of dying silently.
const shellSpawning = new WeakSet();
async function spawnShellIn(cell) {
  if (!window.shellProfile || !cell || shellSpawning.has(cell)) return;
  shellSpawning.add(cell);
  try {
    // Re-read the profile name instead of trusting the boot-time cache: the server
    // withholds it once a stored profile takes over the seed's name, and profiles
    // can be created after this page loaded.
    const profile = await currentShellProfile();
    if (!profile) { showToast("No shell profile available, check the console log.", { error: true }); return; }
    const plan = shellSpawnPlan(await resolveCellNeighbour(cell));
    if (plan.mode === "form") {
      // Nothing to sit beside: there is no context to inherit, so let the user
      // choose one rather than guessing a directory that may not be containable.
      openSpawnDialog(profile);
      return;
    }
    const cwd = plan.cwd;
    // Always prefix the label: with no cwd the server would name the session after
    // default_cwd's basename, which reads exactly like an agent session.
    const base = cwd.replace(/\/$/, "").split("/").pop();
    const req = { agent: profile, cwd, name: "sh:" + (base || profile) };
    if (plan.runtime) req.runtime = plan.runtime; // match the neighbour's tier
    const s = await api("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
    });
    await refreshSessions();
    // The cell can be destroyed while we await (applyCap collapses windows, a pane
    // can be removed). Attaching to a detached cell would leak a live xterm + socket
    // that is never rendered, so fall back to normal placement instead.
    if (cells.includes(cell)) setFocusedCell(cell);
    openTerminal(s.id, s.name);
  } catch (e) {
    showToast("Shell failed: " + e.message, { error: true });
  } finally {
    shellSpawning.delete(cell);
  }
}

// resolveCellNeighbour falls back to the server when a terminal tab exists but hasn't
// resolved its cwd yet (TerminalTab fetches it asynchronously). Without this, a
// +sh right after opening a session silently lands in default_cwd instead of the
// session's directory: the failure the feature exists to prevent.
// resolveCellNeighbour returns the session a quick shell should sit beside:
// the whole record, because "+sh" inherits the tier as well as the directory.
async function resolveCellNeighbour(cell) {
  // Mirror cellCwd's precedence exactly: active tab first, then newest backwards.
  // Picking a different terminal here would open the shell in another session's
  // directory, which is the bug this function exists to prevent.
  const act = cell.activeTab;
  let term = act && act.kind === "terminal" && act.id ? act : null;
  for (let i = cell.tabs.length - 1; !term && i >= 0; i--) {
    const t = cell.tabs[i];
    if (t.kind === "terminal" && t.id) term = t;
  }
  if (!term) return null;
  try {
    const list = await api("/api/sessions");
    return (list || []).find((x) => x.id === term.id) || null;
  } catch (_) {
    return null;
  }
}

// currentShellProfile re-fetches the server's verdict, falling back to the cached
// value only if the request fails (so a network blip doesn't disable the button).
async function currentShellProfile() {
  try {
    const meta = await api("/api/meta");
    window.shellProfile = (meta && meta.shell_profile) || "";
    boxRuntime = (meta && meta.box_runtime) || "";
    cells.forEach((c) => c.renderBar()); // reflect a profile that appeared/disappeared
  } catch (_) { /* keep the cached value */ }
  return window.shellProfile;
}

// --- settings: font sizes ----------------------------------------------------
const FS_SURFACES = ["terminal", "code", "markdown", "filetree"];
// The sample carries its own size rather than reading a CSS variable, because
// one of the four surfaces has no variable to read: the terminal size is
// pushed straight into xterm, not onto the document.
function paintFsSample(surface) {
  const s = document.querySelector('[data-fs-sample="' + surface + '"]');
  if (s) s.style.fontSize = fontPrefs.get(surface) + "px";
}
function bumpFs(surface, delta) {
  fontPrefs.set(surface, fontPrefs.get(surface) + delta);
  const val = document.querySelector('.fs-val[data-fs="' + surface + '"]');
  if (val) val.value = fontPrefs.get(surface);
  paintFsSample(surface);
}
function initSettings() {
  for (const s of FS_SURFACES) {
    const val = document.querySelector('.fs-val[data-fs="' + s + '"]');
    if (val) val.value = fontPrefs.get(s);
    paintFsSample(s);
  }
  document.querySelectorAll("#settings [data-fs]").forEach((el) => {
    const s = el.dataset.fs;
    if (el.classList.contains("fs-inc")) el.onclick = () => bumpFs(s, +fontPrefs.STEP);
    else if (el.classList.contains("fs-dec")) el.onclick = () => bumpFs(s, -fontPrefs.STEP);
    else if (el.classList.contains("fs-val")) el.onchange = () => { if (el.value.trim() !== "") fontPrefs.set(s, el.value); el.value = fontPrefs.get(s); paintFsSample(s); };
  });
  const reset = $("fs-reset");
  if (reset) reset.onclick = () => { fontPrefs.reset(); initSettings(); };
}

// --- boot --------------------------------------------------------------------
window.addEventListener("resize", fitAll);
// Analytics view (button revealed by initEnv only when the feature is enabled).
$("analytics-open").addEventListener("click", openAnalytics);
{
  const anRefresh = $("analytics-refresh");
  if (anRefresh) anRefresh.addEventListener("click", async () => {
    anRefresh.disabled = true;
    anRefresh.textContent = "Scanning…";
    try { await api("/api/analytics/ingest", { method: "POST" }); await anLoad(); }
    catch (_) { /* leave the current view; the range line still shows */ }
    finally { anRefresh.disabled = false; anRefresh.textContent = "Re-scan"; }
  });
  // Tab bar: switch the active analytics tab and re-render from cached data
  // (no refetch — the window data is already loaded).
  const anTabs = $("an-tabs");
  if (anTabs) anTabs.addEventListener("click", (e) => {
    const b = e.target.closest("button[data-antab]");
    if (!b) return;
    AN.tab = b.dataset.antab;
    syncTabButtons();
    anRender();
  });
}
// Layout buttons 1/2/4 → set the max-window cap (auto-split grows up to it).
$("layouts").addEventListener("click", (e) => {
  if (e.target.dataset && e.target.dataset.cols) applyCap(Number(e.target.dataset.cols));
});
applyCap(4); // default: auto-split up to 4 windows
initDialogs();
initSplitter();
initWidthSplitter();
// Pane collapse state reads live session state (waitingCount, via
// renderCollapsedSummaries) for the Sessions bar's pip, so it cannot run
// until lastSessions is initialized — this is why the call lives down here
// in the boot sequence rather than up near the sidebar-toggle wiring, where
// lastSessions's declaration has not been reached yet.
applyCollapsed(readCollapsed());
// The steer rail's own persisted state. It lives down here for the same reason
// applyCollapsed does — opening the rail paints it, which reads lastSessions,
// whose declaration is not reached until this point in the boot sequence — and
// it is deliberately NOT a pre-paint <head> script: the rail is a right-hand
// panel, so arriving a frame late shifts nothing, and the four hand-mirrored
// parses already in <head> are enough to keep in step.
let steerRestore = false;
try { steerRestore = localStorage.getItem(STEER_KEY) === "1"; } catch (_) {}
// Outside the catch on purpose: the try is there for a storage backend that
// throws, and opening the rail paints it. A rendering failure swallowed here
// would present as "the rail just doesn't restore", with nothing in the console.
if (steerRestore) setSteerOpen(true);
initSpawnControl();
initPalette();
loadProfiles();
loadWorkspaces();
loadOptionDefs().then(newProfile);
// Restore the grid from the last visit, once the live session list is known.
// A failed list is retried once (the loadProfiles/initEnv pattern): restoring
// against an unknown server would open tabs for sessions that may be gone, so
// on a second failure we give up on restoring and just re-arm saving.
(async function restoreBoot(retry = true) {
  const sessions = await refreshSessions();
  if (sessions === null) {
    if (retry) { setTimeout(() => restoreBoot(false), 2000); return; }
    endRestore(false); // never persist the empty boot grid over a real snapshot
    return;
  }
  restoreLayout(sessions);
})();
initFileTree();
initUpload();

// Env signal: show a colored DEV/PROD badge + top strip + tab title, so the
// active environment is unmistakable. Config-driven via /api/meta; empty label
// (no env configured) leaves the UI unchanged.
// One retry: this fetch also carries shell_profile, and a single transient failure
// would otherwise hide the +sh button for the whole page session.
(async function initEnv(retry = true) {
  try {
    const meta = await api("/api/meta");
    // Quick-shell profile first: it is independent of the env badge, so it must
    // be captured before the no-badge early return below.
    window.shellProfile = (meta && meta.shell_profile) || "";
    boxRuntime = (meta && meta.box_runtime) || "";
    const anBtn = $("analytics-open");
    if (anBtn) anBtn.hidden = !(meta && meta.analytics); // only when the feature is on
    renderRateLimits(meta && meta.rate_limits); // account 5h/weekly meters in the header
    refreshSessions(); // re-render rows now that the tier label is known
    if (window.shellProfile) cells.forEach((c) => c.renderBar()); // reveal +sh
    if (!meta || !meta.env_label) return;
    const badge = $("env-badge"), strip = $("env-strip");
    badge.textContent = meta.env_label;
    window.__envLabel = meta.env_label; // the status line's lowercase copy reads this
    if (meta.env_color) { badge.style.background = meta.env_color; strip.style.background = meta.env_color; }
    badge.hidden = false;
    strip.hidden = false;
    document.title = "[" + meta.env_label + "] Agent Box";
    updateStatusLine();
  } catch (_) {
    if (retry) setTimeout(() => initEnv(false), 2000); // else: no badge, no +sh
  }
})();

fontPrefs.applyCssVars();
initSettings();
setInterval(refreshSessions, 3000);
// Account limits change slowly and the file only updates when a claude statusLine
// renders — a 30s refresh keeps the % fresh and the countdown ticking without
// hammering /api/meta. Skipped while hidden.
setInterval(async () => {
  if (document.hidden) return;
  try { const m = await api("/api/meta"); renderRateLimits(m && m.rate_limits); } catch (_) {}
}, 30000);

// Ctrl+Shift+` → new shell in the focused window. Capture phase + stopPropagation
// for the same reason as the zoom handler below: xterm listens on its textarea and
// would otherwise swallow the combo while a terminal has focus.
document.addEventListener("keydown", (e) => {
  if (!e.ctrlKey || !e.shiftKey || e.altKey || e.metaKey) return;
  if (e.code !== "Backquote") return; // .code is layout/shift independent
  if (shortcutsBlocked(document.activeElement, dialogsOpen())) return;
  e.preventDefault();
  e.stopPropagation();
  spawnShellIn(focusedCell);
}, true);

// Live zoom the focused pane: Ctrl/Cmd + '='/'+' bigger, '-'/'_' smaller, '0' reset.
// Capture phase + stopPropagation so xterm (which listens on its textarea) never
// sees the combo; preventDefault stops the browser's own page zoom.
document.addEventListener("keydown", (e) => {
  if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
  if (shortcutsBlocked(document.activeElement, dialogsOpen())) return;
  let delta = null, doReset = false;
  if (e.key === "=" || e.key === "+") delta = +fontPrefs.STEP;
  else if (e.key === "-") delta = -fontPrefs.STEP; // not "_": Ctrl+_ is readline undo in terminals
  else if (e.key === "0") doReset = true;
  else return;
  if (!(doReset ? resetZoomFocused() : zoomFocused(delta))) return;
  e.preventDefault();
  e.stopPropagation();
}, true);

// Sidebar shortcuts. Capture phase with stopPropagation, not because the
// palette needs it but because the terminal does: xterm forwards every
// combination it does not recognise into the running process, so a handler
// that lets these bubble types them into the agent instead of toggling a pane.
const SIDEBAR_SHORTCUT_MAC =
  /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");
document.addEventListener("keydown", (e) => {
  if (shortcutsBlocked(document.activeElement, dialogsOpen())) return;
  const mod = e.metaKey || e.ctrlKey;
  if (!mod) return;
  const code = e.code;
  let hit = null;
  if (e.altKey && code === "KeyS") hit = () => togglePane("sessions");
  else if (e.altKey && code === "KeyF") hit = () => togglePane("files");
  // Toggle sidebar: ⌘B is free on macOS, but the unshifted Ctrl+B belongs to
  // the shells and TUIs these panes host — backward-char in readline, page-up
  // in vim, tmux's default prefix — so on every other platform the binding
  // carries Shift to stay off it, the same reasoning that already keeps
  // Ctrl+K clear of the command palette.
  else if (!e.altKey && code === "KeyB" &&
      (SIDEBAR_SHORTCUT_MAC ? (e.metaKey && !e.ctrlKey && !e.shiftKey)
                             : (e.ctrlKey && !e.metaKey && e.shiftKey))) hit = toggleSidebar;
  if (!hit) return;
  e.preventDefault();
  e.stopPropagation();
  hit();
}, true);

// Jump to the Nth session. Capture phase with stopPropagation, like every other
// global shortcut here, because xterm forwards any combination it does not
// recognise straight into the running agent: without it, this types a digit into
// whatever is in the focused pane.
//
// The number addresses the SIDEBAR list, which is the numbered thing on screen.
// The grid's panes are not a usable index — there can be fewer panes than
// sessions, and one pane can stack several.
//
// Alt+digit, not Ctrl/Cmd+digit: Ctrl+1…8 switches browser tabs in Chrome and
// Firefox on Linux/Windows, and Cmd+1…9 does the same in Chrome, Safari and
// Firefox on macOS — a page cannot take that back. Alt+digit is free in
// Chrome/Chromium on Linux/Windows and in Safari, and Option+digit is not a
// browser accelerator on macOS; its one known conflict is Firefox on
// Linux/Windows, where Alt+digit also switches tabs.
const JUMP_MOD = (e) => e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey;
document.addEventListener("keydown", (e) => {
  if (shortcutsBlocked(document.activeElement, dialogsOpen())) return;
  if (!JUMP_MOD(e)) return;
  const m = /^Digit([1-9])$/.exec(e.code); // .code, not .key: layout-independent under a modifier
  if (!m) return;
  const s = jumpTarget(Number(m[1]), lastSessions);
  e.preventDefault();
  e.stopPropagation();
  // Out of range is a deliberate no-op, not a wrap-around: pressing 7 with four
  // sessions open must not land somewhere arbitrary. preventDefault/stopPropagation
  // above still run first, so the digit is swallowed either way, not left to fall
  // through to the agent.
  if (s) openTerminal(s.id, s.name);
}, true);

// zoomFocused / resetZoomFocused are the named zoom path, shared by the keyboard
// handler above and the command palette. Return false when the focused tab has
// nothing to zoom, so the caller can leave the key event alone.
function zoomableTab() {
  const tab = focusedCell && focusedCell.activeTab;
  return tab && (tab.kind === "terminal" || (tab.kind === "viewer" && tab.zoomVar)) ? tab : null;
}
function zoomFocused(delta) {
  const tab = zoomableTab();
  if (!tab) return false;
  tab.zoom?.(delta);
  return true;
}
function resetZoomFocused() {
  const tab = zoomableTab();
  if (!tab) return false;
  tab.resetZoom?.();
  return true;
}
