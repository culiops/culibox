"use strict";

// --- fetch helpers -----------------------------------------------------------
// api(path, opts) is fetch + the error shape below. opts.timeoutMs (session actions: deadline.js ACTION_*_MS) bounds the
// whole call, body included: past it the request is aborted and the call rejects with actionTimeoutError (`timedOut`, no
// status), which toastFailText words as the standard "Could not <what>." with the raw text behind Copy details.
function api(path, opts) {
  if (!opts || !opts.timeoutMs) return apiRun(path, opts);
  const ms = opts.timeoutMs;
  const rest = Object.assign({}, opts);
  delete rest.timeoutMs;
  return withDeadline(ms, (signal) => apiRun(path, Object.assign(rest, { signal })));
}
async function apiRun(path, opts) {
  const res = await fetch(path, opts);
  if (res.status === 401) sysNoteSignedOut(); // any call: the login is gone (Plan 7, R5)
  if (!res.ok) {
    let msg = res.statusText;
    let code = "";
    let body = null;
    try { body = await res.json(); msg = body.error || msg; code = body.code || ""; } catch (_) {}
    const err = new Error(msg);
    // The parsed body and Retry-After ride along for callers that need more
    // than the sentence (a 409 that names who uses an entry, a 429's wait).
    err.body = body;
    err.retryAfter = res.headers ? res.headers.get("Retry-After") || "" : "";
    // The status and machine code ride along so a caller can act on a specific
    // refusal (a locked store, 423) instead of parsing prose.
    err.status = res.status;
    err.code = code;
    throw err;
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
  // document.body is inert while a showModal() dialog is open, so focus() and
  // select() silently fail there and the copy with them. Host the textarea in
  // the topmost open dialog (the last one in document order) when there is one.
  const open = document.querySelectorAll("dialog[open]");
  const host = open.length ? open[open.length - 1] : document.body;
  host.appendChild(ta);
  ta.focus(); ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch (_) { /* best effort */ }
  host.removeChild(ta);
  return ok;
}

// PROBE_MS bounds every request on the reconnect path. A page whose fetches are
// wedged (e.g. behind an unanswered basic-auth challenge) leaves promises pending
// forever; without a deadline that silently killed the reconnect loop.
const PROBE_MS = 4000;

// timedFetch is fetch with a hard deadline (see deadlineSignal).
function timedFetch(path, ms) {
  return fetch(path, { signal: deadlineSignal(ms) });
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
  // No sidebar to toggle below the breakpoint (the bottom nav picks the
  // screen), and a value stored here would silently flip the next wide window.
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return;
  // Each state shows exactly one toggle (the sidebar's «, the rail's » without), so
  // the one just pressed disappears: hand focus to the other.
  const rail = $("rail48");
  const active = document.activeElement;
  const railToggle = rail && rail.querySelector(".ri.tog"); // by class: the » is not the only .ri
  const fromToggle = !!active && (active === $("sidebar-toggle") || active === $("side-collapse") || (!!rail && active === railToggle));
  const hidden = document.documentElement.classList.toggle("sidebar-hidden");
  try { localStorage.setItem("sidebarHidden", hidden ? "1" : "0"); } catch (_) {}
  // Shown again: the splitter's aria-valuenow reads the drawn width (a page loaded hidden never had one).
  if (!hidden && typeof syncSplitterAria === "function") syncSplitterAria();
  renderGrid(); // reflow/refit terminals to the new width
  renderRail48(); // the rail has just appeared or gone
  if (fromToggle) {
    const next = hidden ? rail && rail.querySelector(".ri.tog") : $("side-collapse");
    if (next) next.focus();
  }
}
$("sidebar-toggle").onclick = toggleSidebar;
$("side-collapse").onclick = toggleSidebar;
$("steer-toggle").onclick = toggleSteerRail;

// --- desktop Files dock (spec 5.3/5.4) -----------------------------------------
// One dock, a column right of the pane grid (desktop.css: the panes shrink for
// it, nothing is covered), showing the file tree (Files). Its width is
// positionDrawer's (dockfit.js). The tree is the EXISTING #files-pane node,
// moved in on first use (a DOM move, not a copy: every listener and the tree's
// state travel with it) and put back exactly where it was when the viewport
// crosses into mobile, whose Files screen expects #files-pane inside #sidebar.
// drawerFor (deskshell.js) owns the toggle decision. Activity and Access are
// not drawers: they are views in the pane body (tabs.js Cell.setView), and the
// steer rail stays hidden in #layout on desktop.
//
// Declared up here, before boot builds the first cells: tabs.js reads it while
// painting a header, and a `let` read before its line throws.
// pinned: the dock stays on sessionId while focus moves (dockFollow, docktabs.js).
let drawerState = { open: null, sessionId: null, pinned: false };
// The explorer opened from the folded strip (dockfit.js treeOpen): the fold it was
// asked at ("folded" or "full"), else null. It is spent when the fold changes, when
// the strip is pressed again, on Esc, and when a file is opened from the column.
let dockTreeAsk = null;
// What the last positionDrawer drew: the fold the strip is at without an ask (the
// key dockTreeAsk is compared with), the pane area's width and the dock's and
// explorer's, so a key press steps from what is on screen.
let dockDrawn = { fold: "", avail: 0, dockW: 0, treeW: 0 };
// The key of the empty state the grid shows now ("" when it shows panes), so a
// poll repaints it only when what it says changed. Declared up here for the
// same reason as drawerState: renderGrid reads it while boot builds the cells.
let emptyShownKey = "";
// The boot restore has run (or given up): until then the grid may be about to
// get its tabs back, so the empty state stays silent instead of flashing.
let bootRestoreDone = false;
// node -> { parent, next }: where a moved node came from. Filled on the first
// move only, emptied by restoreDrawerNodes.
const drawerOrigins = new Map();

function drawerDesktop() {
  return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
}

// toggleDrawer: a header tab's press. Opens `kind` for the session, closes it if
// that is what is already showing, or switches.
function toggleDrawer(kind, sessionId) {
  if (!drawerDesktop()) return;
  // A drawer hangs over the panes, which a page hides: close it first, or leave things as they are if it is kept.
  if (closePageHolds(() => toggleDrawer(kind, sessionId))) return;
  applyDrawer(drawerFor(drawerState, kind, sessionId == null ? null : sessionId));
}

// openDrawer: show `kind` for the session whatever is showing now (menu items,
// a press that also moved focus). opts.explicitRoot: an explicit
// "show me this session's files" (the menu's Open files here), which re-roots
// the tree even if it already follows the session; see drawerRootTree.
function openDrawer(kind, sessionId, opts) {
  if (!drawerDesktop()) return;
  if (closePageHolds(() => openDrawer(kind, sessionId, opts))) return;
  applyDrawer({ open: kind, sessionId: sessionId == null ? null : sessionId }, opts);
}

// revealInTree shows a viewer's file in the tree: the Files drawer on desktop
// (the viewer's own session: the drawer's for a drawer viewer, the pane's
// terminal for a pane viewer, none otherwise), the Files screen on mobile. Then
// ftReveal expands down to the row. false (with a toast) when it cannot.
async function revealInTree(viewer) {
  const can = revealable(viewer, window.HOME_DIR || "");
  if (!can.ok) { showToast(can.title, { error: true }); return false; }
  if (mobileActive(window.innerWidth)) {
    setMobileScreen("files");
  } else {
    let sid = null;
    if (drawerViewerTab() === viewer) sid = drawerState.sessionId;
    else {
      const cell = cells.find((c) => c.tabs.includes(viewer));
      const t = cell ? cellTerminal(cell) : null;
      sid = t ? t.id : null;
    }
    openDrawer("files", sid);
    dockTreeReveal(); // a folded explorer cannot show the row: open its column
  }
  const ok = await ftReveal(viewer.path);
  if (!ok) showToast("Could not show that file in the tree", { error: true });
  return ok;
}

// closeDrawerFor: close the drawer if it is showing `kind` (any, if undefined)
// for `sessionId` (any, if undefined).
function closeDrawerFor(sessionId, kind) {
  if (!drawerState.open) return;
  if (sessionId !== undefined && drawerState.sessionId !== sessionId) return;
  if (kind !== undefined && drawerState.open !== kind) return;
  applyDrawer({ open: null, sessionId: drawerState.sessionId });
}

// headerDrawerPress: a Files press in a pane header. A mouse press on a
// pane that was not focused has already moved focus there, and with it an open
// drawer (drawerFollowFocus), so toggling would close what the user just asked
// to see: open instead. A keyboard press moves no focus (e.detail is 0).
function headerDrawerPress(cell, kind, sessionId, e) {
  if (e && e.detail > 0 && !cell._pressFocused) { openDrawer(kind, sessionId); return; }
  // A keyboard press on an unfocused pane: focus it first, so the drawer hangs
  // under this pane's header. That focus change runs drawerFollowFocus, which
  // already moves an open drawer to this pane's session, so a toggle would
  // close it: open, the same rule as the mouse press above.
  if (focusedCell !== cell) { setFocusedCell(cell); openDrawer(kind, sessionId); return; }
  toggleDrawer(kind, sessionId);
}

function moveIntoDrawer(node, body) {
  if (!node || node.parentElement === body) return;
  if (!drawerOrigins.has(node)) drawerOrigins.set(node, { parent: node.parentElement, next: node.nextSibling });
  body.appendChild(node);
}

function applyDrawer(next, opts) {
  const dr = $("drawer");
  const body = $("drawer-body");
  if (!dr || !body) return;
  // A dirty buffer in the Files drawer's viewer asks before anything takes it
  // away (closing, another session); Cancel leaves the drawer as it is.
  // The ask is async, so this call ends here and a confirmed one comes back in.
  if (!(opts && opts.discardOk) && typeof drawerWouldDiscard === "function" && drawerWouldDiscard(drawerState, next)) {
    drawerConfirmLeave().then((ok) => {
      if (!ok) return;
      drawerDiscard();
      applyDrawer(next, Object.assign({}, opts, { discardOk: true }));
    });
    return;
  }
  const prev = drawerState;
  // The pin belongs to the dock showing one session: it carries over while that session stays open, and an explicit
  // press for another session or a close ends it. A caller that says (the pin's own toggle) is believed.
  const keepPin = !!(next.open && prev.open && prev.pinned && next.sessionId === prev.sessionId);
  next = { open: next.open, sessionId: next.sessionId, pinned: next.pinned === undefined ? keepPin : !!next.pinned && !!next.open };
  drawerState = next;
  // The explorer is the right column of the Files body; the viewer column beside it
  // is filesdrawer.js's.
  if (next.open === "files") moveIntoDrawer($("files-pane"), $("drawer-tree") || body);
  dr.dataset.kind = next.open || "";
  dr.hidden = !next.open;
  if (!next.open) {
    paintDrawerMax(false, false); // ⛶ lasts one opening: it hides the panes
    dockTreeAsk = null; // and so does the explorer opened from the strip
  }
  paintDrawerHead();
  positionDrawer();
  if (next.open === "files") drawerRootTree(next.sessionId, !!(opts && opts.explicitRoot));
  if (typeof drawerFilesApply === "function") drawerFilesApply(prev, next);
  for (const c of cells) paintDrawerTabs(c);
  dockRefit(); // the grid gained or lost the dock's width
}

// drawerRootTree roots the tree at the drawer's session. A header tab press
// uses its terminal's cwd under the same follow-key syncFocusedTree uses (so a
// tree already following it does not reload). `explicit` (the menu's Open
// files here) passes NO key, like the mobile path: an explicit request must
// re-root even when the key says the tree already follows this session (a
// manual root change since then leaves the key in place), and no key also
// clears the guard, so the tree resumes following focus at the next change.
// Without a usable terminal cwd: the session's recorded cwd; with no session,
// whatever the focused tab resolves to. The terminal's root and key come from
// treeFollowTarget, so an agent inside a git worktree roots the tree at that
// worktree, both here and in syncFocusedTree (#49).
function drawerRootTree(sessionId, explicit) {
  const t = sessionId ? terminalTabFor(sessionId) : null;
  const s = sessionId ? (lastSessions || []).find((x) => x.id === sessionId) : null;
  const live = t ? treeFollowTarget(t) : null; // null: not rootable (outside $HOME) or unresolved
  // A cwd /api/fs/list refuses (outside $HOME) is never a root: the tree would
  // be replaced by one "forbidden" line, and the mobile Files screen shares it.
  // Such a session leaves the tree where it is, the same as syncFocusedTree.
  if (explicit) {
    const cwd = (live && live.root) || (s && s.cwd) || "";
    if (cwd && rootableCwd(cwd)) { syncTreeToRoot(cwd).catch(() => {}); return; }
  }
  if (live) { syncTreeToRoot(live.root, live.key).catch(() => {}); return; }
  if (s && s.cwd && rootableCwd(s.cwd)) { syncTreeToRoot(s.cwd).catch(() => {}); return; }
  syncFocusedTree();
}

function drawerSessionName(id) {
  if (!id) return "";
  const s = (lastSessions || []).find((x) => x.id === id);
  if (s) return s.name || s.id;
  const t = terminalTabFor(id);
  return t ? t.title() : id;
}

// paintDrawerHead: FILES, then (with 2+ panes, where more than one session is on screen) the session the dock shows, what
// it does with focus, and the pin. With one pane there is nothing to follow or pin, so those three are not drawn.
function paintDrawerHead() {
  const title = $("drawer-title");
  const sess = $("drawer-sess");
  if (!title || !sess) return;
  title.textContent = "Files";
  const sid = drawerState.sessionId;
  const n = drawerSessionName(sid);
  const multi = gridCols() > 1;
  const rec = sid ? (lastSessions || []).find((x) => x.id === sid) : null;
  const dot = $("drawer-sess-dot");
  if (dot) dot.className = rec && typeof deskDot === "function" ? deskDot(rec).cls : "dot";
  const nameEl = $("drawer-sess-name");
  if (nameEl) nameEl.textContent = n;
  sess.title = n;
  sess.hidden = !multi || !n;
  const pinned = !!drawerState.pinned;
  const hint = $("drawer-hint");
  if (hint) {
    hint.textContent = pinned ? "pinned" : "follows the focused pane";
    hint.hidden = !multi;
  }
  const pin = $("drawer-pin");
  if (pin) {
    pin.hidden = !multi;
    pin.disabled = !sid;
    pin.setAttribute("aria-pressed", pinned ? "true" : "false");
    pin.title = pinned ? "Unpin: let the dock follow the focused pane" : "Keep the dock on " + (n || "this session");
    pin.setAttribute("aria-label", pinned ? "Unpin the dock" : "Pin the dock to " + (n || "this session"));
  }
}

// toggleDrawerPin: the header's pin. Pinned, the dock stays on its session while focus moves (dockFollow), and so does
// its tree. Unpinning lets both follow focus again at once, through the same dirty-buffer gate as any focus move:
// syncFocusedTree re-roots the tree (drawerFilesApply, which moves the header and the viewer, does not) and then runs
// drawerFollowFocus.
function toggleDrawerPin() {
  if (!drawerState.open || !drawerDesktop() || !drawerState.sessionId) return;
  drawerState = { open: drawerState.open, sessionId: drawerState.sessionId, pinned: !drawerState.pinned };
  paintDrawerHead();
  if (!drawerState.pinned) syncFocusedTree();
}

// paintDrawerTabs: the pressed state of a pane's Files control follows the drawer:
// the toggle button of a terminal pane, or the one Files tab of a pane of files (the
// only header controls with data-k; Activity/Access are views, Cell.setView). Only
// aria-pressed moves, which is also what draws it (desktop.css): the kit's one pressed
// look, never a second selected tab. Never a rebuild: see drawerFollowFocus.
function paintDrawerTabs(cell) {
  if (!cell || !cell.bar) return;
  const term = cellTerminal(cell);
  const sid = term ? term.id : null;
  // A pane of files has no session; its tab is the drawer's only while it is
  // the focused one.
  const mine = drawerState.sessionId === sid && (sid !== null || cell === focusedCell);
  for (const b of cell.bar.querySelectorAll("[data-k]")) {
    const on = mine && drawerState.open === b.dataset.k;
    b.setAttribute("aria-pressed", on ? "true" : "false");
  }
}

// drawerLiveSids: the sessions that can still be shown (the list, minus the ended ones), for dockFollow's pin check.
function drawerLiveSids() {
  return (lastSessions || []).filter((s) => !s.exited).map((s) => s.id);
}

// drawerPinCheck: the session list changed. A pinned session that has ended no longer holds the dock, so the dock and
// its tree follow focus again. syncFocusedTree, not drawerFollowFocus alone: the tree stayed on the pinned session
// (drawerKeepsTree) and only syncFocusedTree re-roots it; it ends in drawerFollowFocus. A pin on a live session, or no
// pin, is left alone (no work per poll).
function drawerPinCheck() {
  if (!drawerState.pinned || drawerLiveSids().includes(drawerState.sessionId)) return;
  syncFocusedTree();
}

// drawerFollowFocus: the dock's tree already follows the focused tab
// (syncFocusedTree), so its session and its header do too, unless the dock is
// pinned to a live session. dockFollow (docktabs.js) decides: a pin on a session
// that has ended is dropped here, and a focused pane with no session leaves the
// dock on the last session it showed.
function drawerFollowFocus() {
  if (!drawerState.open || !drawerDesktop()) return;
  const term = cellTerminal(focusedCell);
  const f = dockFollow({ sid: drawerState.sessionId, pinned: !!drawerState.pinned }, term ? term.id : null, drawerLiveSids());
  if (f.sid !== drawerState.sessionId) {
    const next = { open: drawerState.open, sessionId: f.sid, pinned: f.pinned };
    // A dirty buffer in the Files viewer asks first (once: a Cancel is kept
    // until focus comes back or the buffer changes). Cancel keeps the drawer,
    // and its tree (drawerKeepsTree), on its session; Discard follows focus.
    if (typeof drawerHoldOnFollow === "function" && drawerHoldOnFollow(next)) {
      positionDrawer();
      return;
    }
    const prev = drawerState;
    drawerState = next;
    paintDrawerHead();
    for (const c of cells) paintDrawerTabs(c);
    if (typeof drawerFilesApply === "function") drawerFilesApply(prev, next);
  } else {
    if (f.pinned !== !!drawerState.pinned) { // the pinned session ended while it was the focused one
      drawerState = { open: drawerState.open, sessionId: drawerState.sessionId, pinned: f.pinned };
      paintDrawerHead();
    }
    if (typeof drawerFocusSame === "function") drawerFocusSame();
  }
  positionDrawer();
}

// drawerTabClosed: a tab was closed. If it was the drawer's session's last tab,
// the drawer moves to whatever the focused pane shows.
function drawerTabClosed(id) {
  if (!drawerState.open || drawerState.sessionId !== id || !id || terminalTabFor(id)) return;
  drawerFollowFocus();
}

// dockCols: the pane columns dockLayout counts (dockfit.js): two for cols-2|3|4, one for a single pane. A maximized pane
// leaves one column on screen whatever the cell count is.
function dockCols() {
  return currentMaximized() || gridCols() < 2 ? 1 : 2;
}

// dockTreeShare: the explorer's stored share, or null for the dock's default. drawersize.js normalises "never dragged"
// to its own TREE_DEFAULT (.4, the old drawer's), which is not the dock's (.35): that value means "the default" here, and
// a double-click on the splitter (it stores TREE_DEFAULT) puts the default back.
function dockTreeShare() {
  return drawerSize.tree === TREE_DEFAULT ? null : drawerSize.tree;
}

// dockMeasure: the width the panes and the dock divide (the grid's left edge to the layout's right edge, whether the
// dock sits in the flow beside the panes or, in Full mode, over them) and where that starts. null while a page hides
// the grid: pageClosed measures again.
function dockMeasure() {
  const lay = $("layout");
  const grid = $("grid");
  if (!lay || !grid) return null;
  const lr = lay.getBoundingClientRect();
  const gr = grid.getBoundingClientRect();
  if (!(gr.width > 0)) return null;
  return { avail: Math.floor(lr.right - gr.left), left: gr.left };
}

// paintDrawerLayout: the dock's geometry for `avail`. dockLayout decides the widths: the dock, the explorer column or its
// folded strip, and whether the panes can keep their floors at all. When they cannot, the dock opens in Full mode over
// them (data-forced: the same overlay as ⛶, so no terminal is resized to nothing) and dockFullLayout divides the pane
// area. Forced Full mode lasts only while it is needed: the pushed dock comes back when there is room, and a ⛶ pressed
// before that does not survive it. `over` = {dockShare?, treeShare?} is a drag in progress, not yet stored.
function paintDrawerLayout(avail, over) {
  const dr = $("drawer");
  const body = $("drawer-body");
  if (!dr || !body) return;
  const o = over || {};
  const dockShare = o.dockShare !== undefined ? o.dockShare : drawerSize.width;
  const treeShare = o.treeShare !== undefined ? o.treeShare : dockTreeShare();
  const plan = (treeOpen) => dockLayout({ avail, cols: dockCols(), dockShare, treeShare, treeOpen });
  const base = plan(false);
  // What the strip is at, apart from any ask: a resize that changes it spends an explorer opened from the strip.
  const fold = base.full ? "full" : base.treeFolded ? "folded" : "open";
  if (dockTreeAsk !== null && dockTreeAsk !== fold) dockTreeAsk = null;
  const open = dockTreeAsk !== null;
  const lay = open ? plan(true) : base;
  if (lay.full) {
    if (dr.dataset.forced !== "1") paintDrawerMax(true, true);
  } else if (dr.dataset.forced === "1") {
    paintDrawerMax(false, false);
  }
  const full = dr.dataset.max === "1";
  const parts = full ? dockFullLayout({ width: avail, treeShare, treeOpen: open }) : lay;
  dr.style.setProperty("--grid-w", avail + "px");
  if (!lay.full) dr.style.setProperty("--dock-w", lay.dockW + "px");
  body.style.setProperty("--dr-tree", parts.treeW + "px");
  if (parts.treeFolded) dr.dataset.folded = "1";
  else delete dr.dataset.folded;
  dockDrawn = { fold, avail, dockW: full ? avail : lay.dockW, treeW: parts.treeW };
}

// positionDrawer: sizes the dock. It is a column beside the pane grid (desktop.css), so nothing is hung under a pane's
// header any more and nothing covers a strip or banner; the grid's px are what the widths come from.
function positionDrawer() {
  if (typeof toastPlace === "function") toastPlace(); // the toast stack clears the same strips (Plan 7 call 10)
  const dr = $("drawer");
  if (!dr || dr.hidden || !drawerDesktop()) return;
  const m = dockMeasure();
  if (!m) return;
  // The chip, hint and pin are for 2+ panes: a pane added or closed changes whether they are drawn. A pin that can no
  // longer be seen (the panes are down to one) cannot be undone, so it ends; the dock follows focus from the next focus move.
  const multi = gridCols() > 1 ? "1" : "";
  if (dr.dataset.multi !== multi) {
    dr.dataset.multi = multi;
    if (!multi && drawerState.pinned) drawerState = { open: drawerState.open, sessionId: drawerState.sessionId, pinned: false };
    paintDrawerHead();
  }
  paintDrawerLayout(m.avail);
  paintDrawerSize();
}

// dockTreeReveal: something needs the explorer on screen (its strip, Go to file, Reveal in tree). A folded explorer opens as
// its column, which pushes the terminals (dockfit.js treeOpen); one already open, or no dock, changes nothing.
function dockTreeReveal() {
  const dr = $("drawer");
  if (!dr || dr.hidden || dr.dataset.folded !== "1" || dockTreeAsk !== null) return;
  dockTreeAsk = dockDrawn.fold;
  positionDrawer();
  dockRefit();
}

// dockTreeFold: the explorer opened from the strip folds again (Esc, a file opened from the column). Nothing to do
// for one that was not opened that way.
function dockTreeFold() {
  if (dockTreeAsk === null) return;
  dockTreeAsk = null;
  positionDrawer();
  dockRefit();
}

// dockStripOpen: a press on the strip's Explorer or Changes. The strip is not drawn while the column is open, so focus
// goes in with it rather than to a button that has just gone: a tree row, or when the tree has not loaded one yet its
// Go to file field, or (a workspace the fs API refuses hides both) the dock's Close button.
function dockStripOpen() {
  dockTreeReveal();
  const to = [$("drawer-tree").querySelector('.ft-node[tabindex="0"]'), $("drawer-goto-in"), $("drawer-x")].find((n) => n && n.getClientRects().length > 0);
  if (to) to.focus();
}

// dockRefit: the grid's width changed (the dock opened, closed or was resized), so the visible terminals refit. Each
// one's own ResizeObserver does it too; this is what the sidebar splitter does on release, asked once on the next frame,
// once the new widths are laid out.
let dockRefitQueued = false;
function dockRefit() {
  if (dockRefitQueued || typeof fitAll !== "function") return;
  dockRefitQueued = true;
  requestAnimationFrame(() => {
    dockRefitQueued = false;
    fitAll();
  });
}

// restoreDrawerNodes puts every moved node back exactly where it came from and
// closes the drawer. Run on the crossing into mobile. The Files viewer is let
// go too (a dirty one is kept whole for the next desktop drawer: see
// drawerFilesDetach); no prompt can stop a resize. Every pane goes back to
// Terminal as well: mobile has no pane views.
function restoreDrawerNodes() {
  if (typeof drawerFilesDetach === "function") drawerFilesDetach();
  for (const [node, o] of drawerOrigins) {
    if (o.next && o.next.parentNode === o.parent) o.parent.insertBefore(node, o.next);
    else o.parent.appendChild(node);
  }
  drawerOrigins.clear();
  drawerState = { open: null, sessionId: null, pinned: false };
  dockTreeAsk = null;
  const dr = $("drawer");
  if (dr) { dr.hidden = true; dr.dataset.kind = ""; }
  paintDrawerMax(false, false);
  for (const c of cells) c.setView("terminal");
}

// steerViaPaneView is setSteerOpen's desktop route (steerrail.js): the rail
// never docks on desktop; opening it opens the focused pane's Access view
// (its agent, never a docked shell), closing it returns that pane to Terminal.
// false on mobile, where setSteerOpen keeps its own behaviour.
function steerViaPaneView(open) {
  if (!drawerDesktop()) return false;
  if (open) { openPaneView(null, "access"); return true; }
  if (focusedCell && focusedCell.view === "access") focusedCell.setView("terminal");
  return true;
}

// paintSteerToggle: the ◧ toggle's aria-pressed follows steerOpen(), so on
// desktop it is pressed while the focused pane shows Access, however the view
// got there (header tab, palette, sub-line) or left (Terminal, another pane
// focused). Called from Cell._syncView and setFocusedCell (tabs.js). On mobile
// steerOpen() is the rail's own state, the same value setSteerOpen writes.
function paintSteerToggle() {
  const t = $("steer-toggle");
  if (t) t.setAttribute("aria-pressed", steerOpen() ? "true" : "false");
}

$("drawer-x").onclick = () => closeDrawerFor();
$("drawer-pin").onclick = () => toggleDrawerPin();
$("drawer-strip-tree").onclick = () => dockStripOpen();
$("drawer-strip-changes").onclick = () => dockStripOpen();
$("drawer-strip-goto").onclick = () => drawerGoFocus();

// --- Files dock size (#34): drawersize.js stores, dockfit.js decides, this applies ------
// Two separators: the dock's left edge (its share of the pane area, the grid plus the
// dock) and the splitter between the editor and the explorer (the explorer's share of
// the dock). dockLayout turns the shares into px and holds the floors; a stored share
// that cannot be drawn is not an error, it is drawn as near as it can be.
function drawerStorage() {
  try { return window.localStorage; } catch (_) { return null; }
}
let drawerSize = loadDrawerSize(drawerStorage());

// drawnShare: the share a separator is drawn at, for a key press to step from (what is on screen, not what is stored,
// which dockLayout may have held back). null while the dock is not on screen.
function drawnShare(kind) {
  const d = dockDrawn;
  if (kind === "width") return d.avail > 0 && d.dockW > 0 ? d.dockW / d.avail : null;
  return d.dockW > 0 ? d.treeW / d.dockW : null;
}

function setSplitAria(el, r, kind) {
  if (!el || typeof r !== "number") return;
  const a = splitAria(r, kind);
  el.setAttribute("aria-valuenow", String(a.now));
  el.setAttribute("aria-valuemin", String(a.min));
  el.setAttribute("aria-valuemax", String(a.max));
}

// paintDrawerSize: the separators' aria values, from what positionDrawer drew.
function paintDrawerSize() {
  setSplitAria($("drawer-split"), drawnShare("tree"), "tree");
  setSplitAria($("drawer-edge"), drawnShare("width"), "width");
}

function setDrawerSize(next) {
  drawerSize = normalizeDrawerSize(next);
  saveDrawerSize(drawerStorage(), drawerSize);
  positionDrawer();
  dockRefit();
  if (typeof drawerGoPosition === "function") drawerGoPosition();
}

// dockTreeFromPointer: the explorer is the dock's right column, so its share is the distance from the pointer to the
// dock's right edge. (drawersize.js's treeRatioFromPointer measures from the left, for the old left-hand tree.)
function dockTreeFromPointer(body, x) {
  if (!body || !(body.width > 0)) return null;
  return clampShare((body.left + body.width - x) / body.width, "tree");
}

// wireDrawerSplit: drag, arrow keys and double-click (reset) for one
// separator. Like the dock splitter (tabs.js), a drag writes only the CSS
// variables per move, against a box measured once at the press; the share is
// saved once, on release. Each move still goes through dockLayout, so the
// floors hold while the pointer is beyond them.
function wireDrawerSplit(el, kind) {
  if (!el) return;
  const width = kind === "width";
  const paintLive = (avail, r) => {
    paintDrawerLayout(avail, width ? { dockShare: r } : { treeShare: r });
    paintDrawerSize();
  };
  el.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || el.classList.contains("dragging")) return; // one drag at a time
    const m = dockMeasure();
    if (!m) return;
    e.preventDefault();
    const pid = e.pointerId;
    el.setPointerCapture(pid);
    el.classList.add("dragging");
    // The dock's width is a share of the pane area, which the pointer is measured against from the area's right edge.
    const rect = width ? { left: m.left, width: m.avail } : $("drawer-body").getBoundingClientRect();
    let r = null;
    const move = (ev) => {
      if (ev.pointerId !== pid) return;
      const v = width ? drawerWidthFromPointer(rect, ev.clientX) : dockTreeFromPointer(rect, ev.clientX);
      if (v === null) return;
      r = v;
      paintLive(m.avail, r);
    };
    const up = (ev) => {
      if (ev.pointerId !== pid) return;
      try { el.releasePointerCapture(pid); } catch (_) {}
      el.classList.remove("dragging");
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      el.removeEventListener("lostpointercapture", up);
      if (r !== null) setDrawerSize(Object.assign({}, drawerSize, { [kind]: r }));
      else dockRefit();
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
    // A capture lost without pointerup (window blur, element hidden by ⛶)
    // still ends the drag, or "dragging" would stick and refuse every press.
    el.addEventListener("lostpointercapture", up);
  });
  el.addEventListener("dblclick", () => {
    setDrawerSize(Object.assign({}, drawerSize, width ? { width: null } : { tree: TREE_DEFAULT }));
  });
  el.addEventListener("keydown", (e) => {
    // The explorer is on the splitter's right, so its arrows are the mirror of drawersize.js's (written for a left-hand
    // tree): ArrowLeft grows it. The dock's edge already means what stepSplit says (ArrowLeft grows).
    const key = width ? e.key : e.key === "ArrowLeft" ? "ArrowRight" : e.key === "ArrowRight" ? "ArrowLeft" : e.key;
    // From what is drawn: a stored share the floors held back would otherwise take presses that move nothing.
    const v = stepSplit(null, key, kind, drawnShare(kind));
    if (v === null) return;
    e.preventDefault();
    setDrawerSize(Object.assign({}, drawerSize, { [kind]: v }));
  });
}

// paintDrawerMax: Full mode's attributes and its ⛶ button. `forced` is Full mode the layout needs (positionDrawer), which
// Esc and ⛶ must not undo.
function paintDrawerMax(on, forced) {
  const dr = $("drawer");
  const b = $("drawer-max");
  if (!dr) return;
  if (on) dr.dataset.max = "1";
  else delete dr.dataset.max;
  if (on && forced) dr.dataset.forced = "1";
  else delete dr.dataset.forced;
  if (b) {
    b.setAttribute("aria-pressed", on ? "true" : "false");
    b.textContent = on ? "❐" : "⛶";
    b.title = on ? "Restore the dock's width" : "Expand the dock over the panes";
    b.setAttribute("aria-label", on ? "Restore the dock" : "Expand the dock");
  }
  if (typeof drawerGoPosition === "function") drawerGoPosition();
}

// setDrawerMax: ⛶ and Esc. The dock changes geometry (over the panes, or beside them), so it is laid out again at once.
function setDrawerMax(on) {
  paintDrawerMax(on, false);
  positionDrawer();
  dockRefit();
}

wireDrawerSplit($("drawer-edge"), "width");
wireDrawerSplit($("drawer-split"), "tree");
$("drawer-max").onclick = () => { if ($("drawer").dataset.forced !== "1") setDrawerMax($("drawer").dataset.max !== "1"); };
// Escape restores a maximized drawer (maxEscapeRestores decides). On the
// document, not the drawer: a tree row click leaves focus on body. The viewer
// toolbar's menus (viewertoolbar.js, which loads earlier) take focus when they
// open, so an open one is also checked by state (vtMenuOpen), and they mark the
// press used with preventDefault when they close on it. The tree's row menu
// takes no focus, so its open state is checked too. Not a capture listener: its
// preventDefault would stop the page host closing a page.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  const dr = $("drawer");
  const t = e.target;
  const rm = $("ft-menu");
  const at = {
    where: t === document.body || t === document.documentElement ? "body" : dr && dr.contains(t) ? "drawer" : "other",
    owned: !!(t && t.closest && t.closest("input, textarea, [contenteditable], .CodeMirror, [role=menu]")),
    menuOpen: !!(vtMenuOpen() || (rm && rm.classList.contains("open"))),
    prevented: e.defaultPrevented,
  };
  // A forced Full mode has no pushed dock to go back to, so Esc leaves it be.
  if (maxEscapeRestores({ max: !!dr && dr.dataset.max === "1" && dr.dataset.forced !== "1", ...at })) {
    e.preventDefault();
    setDrawerMax(false);
    return;
  }
  // The explorer opened from the folded strip is as short-lived as ⛶, and Esc closes it in the same places and no others.
  if (dockTreeAsk !== null && maxEscapeRestores({ max: true, ...at })) {
    e.preventDefault();
    dockTreeFold();
  }
});
paintDrawerSize();
// Not in the observer's own callback: positionDrawer changes the grid's width (the dock is beside it), which this observer
// is also watching, and that raises "ResizeObserver loop completed with undelivered notifications" on the window. One run
// a frame at most, as credentials.js does for the same reason.
if (typeof ResizeObserver === "function") {
  let queued = false;
  new ResizeObserver(() => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => { queued = false; positionDrawer(); });
  }).observe($("grid"));
}

// renderRail48 paints the icon rail. It rebuilds, so it is guarded by a
// signature: the session list is replaced every three seconds, and a rebuild
// under the pointer swallows a click — the same trap setFocusedCell's comment
// in tabs.js records, where a full renderBar() ate the first press of every
// pane button. Nothing here holds a value, so a signature is all it needs.
let rail48Key = "";
// The brand mark above the rail's ☰ (spec 5.1): a copy of the sidebar top's
// logo, so the two cannot drift. Decorative; the page title names the app.
function railLogo() {
  const wrap = document.createElement("span");
  wrap.className = "rail-logo";
  wrap.setAttribute("aria-hidden", "true");
  const src = document.querySelector("#side-top .side-logo");
  if (src) wrap.appendChild(src.cloneNode(true));
  return wrap;
}
function renderRail48() {
  const rail = $("rail48");
  if (!rail) return;
  // Dots follow the sidebar's order (needs you first), as the Alt+N numbers do.
  const items = railItems(jumpOrder(lastSessions, mobileActive(window.innerWidth)), { focusedId: selectedSessionId(), mac: IS_MAC });
  // The one system-banner marker above the list (Plan 7 R19); null on a phone or with no banner.
  const marker = typeof sysRailMarkerModel === "function" ? sysRailMarkerModel() : null;
  const key = items.map((i) => [i.kind, i.id || "", i.activity, i.on ? 1 : 0, i.alert ? 1 : 0, i.elevated ? 1 : 0, i.title || "", i.dot ? i.dot.cls : ""].join(":")).join("|")
    + (marker ? "|m:" + marker.key + ":" + marker.title : "");
  if (key === rail48Key) return;
  rail48Key = key;
  const mk = (it) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "ri" + (it.on ? " on" : "") + (it.alert ? " alert" : "") + (it.elevated ? " is-elevated" : "") + (it.kind === "new" ? " add" : "") + (it.kind === "toggle" ? " tog btn btn-ghost btn-icon btn-sm" : "");
    b.setAttribute("aria-label", it.label);
    // A session's hover text is its bare name (the "waiting" suffix is in the
    // accessible name and the dot's colour); the toggle and add keep their label.
    b.title = it.kind === "session" ? it.title : it.label;
    if (it.kind === "session") {
      const d = document.createElement("span");
      // deskDot is the row's and the pane header's dot: an expired sign-in
      // blocks on you while the agent still works, and a finished turn is neutral.
      d.className = it.dot.cls;
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
  // Top to bottom (m1-2): the logo, the » that brings the sidebar back (level with the
  // sidebar's «), the primary +, the session dots, then the quota bars at the foot;
  // renderRateLimits fills the bars. Re-parked on every rebuild because
  // replaceChildren drops them.
  rail.replaceChildren(railLogo(), mk(items[0]), mk(items[items.length - 1]), ...(marker ? [sysRailMarkerEl(marker)] : []), mid, railMeters());
  paintRailMeters();
}

// paintSideElevated: the sidebar footer's "● N elevated" (digest M6.4), red while
// any session has an AWS write window open. With none open it is not there at all:
// "0 elevated" is a line that says nothing. A lapsed window clears on the next
// poll. The ● is decoration (aria-hidden), so the accessible text is "N elevated".
function paintSideElevated() {
  const el = $("side-elevated");
  if (!el) return;
  const n = elevatedCount(lastSessions, Date.now());
  el.hidden = n === 0;
  el.classList.toggle("hot", n > 0);
  if (n === 0) { el.replaceChildren(); return; }
  const dot = document.createElement("span");
  dot.setAttribute("aria-hidden", "true");
  dot.textContent = "\u25CF";
  el.replaceChildren(dot, document.createTextNode(" " + n + " elevated"));
}

// paintSideHints: the sidebar top's palette and collapse buttons name their chord, from the
// registry (so it is the Mac spelling on a Mac, and a rebound key cannot leave it stale). The
// same text is the tooltip and the accessible name, as the frames draw it. The rail's » takes
// its from railItems.
function paintSideHints() {
  const set = (id, label, shortcut) => {
    const el = $(id);
    if (!el) return;
    const t = hintTitle(label, shortcut, IS_MAC);
    el.title = t;
    el.setAttribute("aria-label", t);
  };
  set("side-palette", "Command palette", "palette.open");
  set("side-collapse", "Hide sidebar", "sidebar.toggle");
}

// paintSideCost: the footer's "$ today", the same sum the Analytics page shows
// for today (summary over costTodayRange, folded and formatted like it). Only
// asked when the analytics feature is on; a failed read leaves it hidden, with
// no toast: the footer is a glance, not a place for errors.
let sideCostOn = false;
async function paintSideCost() {
  const el = $("side-cost");
  if (!el) return;
  if (!sideCostOn) { el.hidden = true; return; }
  // The footer is desktop-only: on mobile nothing shows the sum, so no fetch.
  if (mobileActive(window.innerWidth)) return;
  try {
    const r = costTodayRange(Date.now());
    const summary = await api("/api/analytics/summary?from=" + r.from + "&to=" + r.to);
    el.textContent = usd(foldCostByDay((summary && summary.byDayModel) || []).totals.cost) + " today";
    el.title = "Spend today (UTC), as the Analytics page counts it";
    el.hidden = false;
  } catch (_) { el.hidden = true; }
}

// The rail's two mini quota bars (5h, week), from the same reading as the
// sidebar footer's meters: #limits-side holds the rendered .mtr rows, so this
// reads their fill rather than re-deriving the account and the window.
function railMeters() {
  const box = document.createElement("div");
  box.id = "rail-meters";
  box.className = "rail-meters";
  box.setAttribute("aria-hidden", "true");
  for (const k of ["5h", "wk"]) {
    const bar = document.createElement("span");
    bar.className = "rm";
    bar.dataset.win = k;
    bar.appendChild(document.createElement("i"));
    box.appendChild(bar);
  }
  return box;
}

function paintRailMeters() {
  const box = $("rail-meters"), src = $("limits-side");
  if (!box) return;
  const rows = src ? Array.from(src.querySelectorAll(".mtr")) : [];
  let any = false;
  for (const bar of box.children) {
    const row = rows.find((r) => r.firstElementChild && r.firstElementChild.textContent === bar.dataset.win);
    const fill = row && row.querySelector(".bar i");
    bar.hidden = !fill;
    if (fill) { bar.firstElementChild.style.width = fill.style.width; bar.classList.toggle("hot", row.classList.contains("hot")); bar.classList.toggle("unk", row.classList.contains("unk")); bar.classList.toggle("stale", row.classList.contains("stale")); any = true; }
  }
  box.hidden = !any;
}

// Live count of open MODAL dialogs, for shortcutContext (shell.js) and the
// page host's Esc. A desktop page (pagehost.js, `.as-page`) is not one: with
// Settings open as a page, a session jump (Mod+1..9) or a layout switch must still work, and
// they close the page on their way.
const dialogsOpen = () => document.querySelectorAll("dialog[open]:not(.as-page)").length;

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
  // The mobile nav's Sessions pip tracks the same waiting count as the
  // collapsed sidebar's pip, refreshed on the same call rather than a
  // separate poll. renderMobileNav is a function declaration further down
  // this file, hoisted like waitingCount() above; it no-ops safely before
  // #mobile-nav exists in the DOM or on desktop (where it stays hidden).
  renderMobileNav();
  // Same reasoning for the Terminal screen's chip strip and key bar: repaint
  // on the existing poll refresh rather than adding a second one.
  renderMobileChips();
  renderMobileKeybar();
}

function togglePane(kind) {
  // Desktop ignores the legacy pane collapse (desktop.css): the list always
  // shows and the tree lives in the drawer. A toggle there would only write a
  // key the next phone-width visit obeys.
  if (!mobileActive(window.innerWidth)) return;
  const state = readCollapsed();
  state[kind] = !state[kind];
  try { localStorage.setItem(PANES_KEY, saveCollapsed(state)); } catch (_) {}
  applyCollapsed(state);
}

$("sessions-cv") && ($("sessions-cv").onclick = () => togglePane("sessions"));
$("files-cv") && ($("files-cv").onclick = () => togglePane("files"));

// U3: a collapsed pane IS its header bar — the caret is visually the pane's
// only affordance, but requiring that specific 11px target to re-expand it
// is unnecessarily small. Any click on the bar that isn't on one of the bar's
// own controls (the caret itself, +new, the file actions, the root input…)
// re-expands the pane the same way the caret does. Expanded, nothing changes:
// this only ever fires while .closed is present.
//
// Desktop only. On mobile, #files-phd is promoted into the Files screen's own
// topbar and CSS forces it to always read as open regardless of whatever
// .closed the desktop toggle last recorded (app.css's
// html.mobile-screen-files #files-phd rules) — so a stray tap on that bar
// must not silently flip hidden desktop state. #sessions-phd never renders at
// all on mobile (display: none), so it needs no such guard, but the same
// early return keeps both call sites identical.
function phdRowClick(kind) {
  return (e) => {
    if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return;
    if (!e.currentTarget.classList.contains("closed")) return;
    if (e.target.closest("button, input, a, select, textarea")) return;
    togglePane(kind);
  };
}
$("sessions-phd") && $("sessions-phd").addEventListener("click", phdRowClick("sessions"));
$("files-phd") && $("files-phd").addEventListener("click", phdRowClick("files"));

// Applying the collapsed state at boot happens later, in the boot sequence
// near initFileTree() — see the comment there. Assigning these two onclick
// handlers is safe here: it only creates closures, it does not call
// togglePane (and therefore does not touch lastSessions) until a real click.

// --- mobile shell -----------------------------------------------------------
// At <=700px the app is one screen at a time (Sessions / Terminal / Files),
// switched by #mobile-nav. Desktop is untouched: the class does nothing above
// the 700px breakpoint because every rule keyed off it lives inside that query.
//
// Defining this block here (rather than calling initMobileShell() here) is
// safe for the same reason applyCollapsed's boot-sequence comment below gives
// for itself: renderMobileNav() reads waitingCount(), which reads lastSessions
// — a `let` not yet initialized this early in the script. Only the CALL to
// initMobileShell() has to wait; the function declarations can live anywhere.
let _mobileScreen = loadScreen((() => { try { return localStorage.getItem(MOBILESHELL_KEY); } catch (_) { return null; } })());

function currentMobileScreen() { return _mobileScreen; }

function setMobileScreen(screen) {
  _mobileScreen = loadScreen(screen);
  const root = document.documentElement;
  for (const s of MOBILE_SCREENS) root.classList.toggle("mobile-screen-" + s, s === _mobileScreen);
  try { localStorage.setItem(MOBILESHELL_KEY, saveScreen(_mobileScreen)); } catch (_) {}
  renderMobileNav();
}

// The pip reuses waitingCount() — the SAME count the collapsed Sessions bar's
// pip uses (see renderCollapsedSummaries) — so the two can never drift and
// there is no second poll.
function renderMobileNav() {
  const nav = document.getElementById("mobile-nav");
  if (!nav) return;
  const model = navModel(_mobileScreen, waitingCount());
  for (const item of model) {
    const btn = nav.querySelector('[data-screen="' + item.screen + '"]');
    if (!btn) continue;
    btn.classList.toggle("on", item.active);
    btn.setAttribute("aria-current", item.active ? "page" : "false");
    const pip = btn.querySelector(".mn-pip");
    if (pip) { pip.hidden = item.pip <= 0; pip.textContent = item.pip > 0 ? String(item.pip) : ""; }
  }
}

function initMobileShell() {
  const nav = document.getElementById("mobile-nav");
  if (nav) nav.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-screen]");
    if (btn) setMobileScreen(btn.dataset.screen);
  });
  // The mobile Sessions screen draws its own full-width "+ new session" (M1)
  // below the list; it opens the same dialog as the spawn trigger. It wraps
  // openSpawnDialog itself rather than forwarding a click to #spawn-go: that
  // button is hidden on mobile, so its pending state would be invisible and a
  // slow loadProfiles would look dead here. Plain-text button, so the label
  // swap keeps no markup.
  const sgm = document.getElementById("spawn-go-mobile");
  if (sgm) sgm.addEventListener("click", () => withPending(sgm, "Opening…", () => openSpawnDialog()));
  setMobileScreen(_mobileScreen); // applies class + paints nav on boot
  renderMobileChips();
  renderMobileKeybar();
  renderMobileSnips();
  initMobileKeyboard();
}

// --- mobile terminal: session chip strip -----------------------------------
// The chips are every open tab — terminal AND viewer (U1) — across all
// panes; the Sessions screen stays the full launcher. One chip is active (the
// focused pane's active tab); tapping another switches which single pane the
// Terminal screen shows. This reuses the pane machinery — no parallel
// session model.
function openTerminalTabs() {
  const out = [];
  // Pane tabs only, cell order then tab order. Not eachTerminal: that also walks
  // desktop docks, whose shells have no pane tab to focus (mobile undocks a
  // shell when it is opened, see openTerminal).
  if (typeof cells !== "undefined") for (const c of cells) for (const t of c.tabs) if (t.kind === "terminal") out.push(t);
  return out;
}

// openMobileTabs: every open tab — terminal AND viewer — across all cells,
// in cell order then tab order (the ordering Cell.tabs already holds, and
// the one the desktop cell-bar itself paints in). U1: the mobile chip strip
// is the single switcher for BOTH kinds, so unlike openTerminalTabs above
// this does not filter by kind.
function openMobileTabs() {
  const out = [];
  if (typeof cells !== "undefined") for (const c of cells) for (const t of c.tabs) out.push(t);
  return out;
}

// cellOfTab: tabs carry no back-pointer to their Cell, so this is the honest
// implementation — scan the live cells for the one holding this tab. `cells`
// is tabs.js's module-level array; classic scripts share one global scope, so
// it is reachable here like any other global.
function cellOfTab(tab) {
  if (typeof cells === "undefined") return null;
  for (const c of cells) if (c.tabs.includes(tab)) return c;
  return null;
}

// focusMobileTab: kind-agnostic — a terminal tab and a viewer tab are both
// just Cell tabs, and cellOfTab/activate don't care which. Renamed from
// focusTerminalTab (U1): the chip strip now switches to either kind, and a
// viewer chip's focus button must be able to call this too.
function focusMobileTab(tab) {
  if (!tab || typeof setFocusedCell !== "function") return;
  const cell = cellOfTab(tab);
  if (!cell) return;
  // Same order tabs.js's own openTerminal uses: focus the cell, then make this
  // tab its active one (Cell has no separate "select" — activate() is both).
  setFocusedCell(cell);
  cell.activate(tab);
  renderMobileChips();
}

// Fix round 2: renderMobileChips runs on every ~3s session poll
// (refreshSessions -> renderCollapsedSummaries -> renderMobileChips), not
// only on navigation. Scrolling the focused chip into view on every one of
// those renders fought a manual scroll of an overflowing strip — the strip
// snapped back within one poll cycle even while the operator was browsing
// it. This remembers the tab object last scrolled to; shouldScrollToFocused
// (chipmodel.js) gates the scroll on that actually changing.
let _lastScrolledChip = null;

// U1: chips list every open tab — terminal AND viewer — as the single mobile
// switcher (the desktop per-cell .cell-bar is hidden at <=700px, see
// app.css). The pure decision (label/title/on/wait/closable per tab) lives
// in chipmodel.js's chipModel(); this function only turns that model into
// DOM. Each chip is a wrapper holding a focus button (`.chip-main`) plus,
// on the focused chip only, a SEPARATE close button (`.chip-x`) — never a
// <button> nested inside a <button>.
function renderMobileChips() {
  const strip = document.getElementById("term-chips");
  if (!strip) return;
  const tabs = openMobileTabs();
  strip.hidden = tabs.length === 0;
  const focused = (typeof focusedTab === "function") ? focusedTab() : null;
  const model = chipModel(
    tabs, focused,
    (t) => (typeof t.activity === "function" ? t.activity() : null),
    (t) => (typeof t.status === "function" ? t.status() : null),
  );
  strip.textContent = "";
  tabs.forEach((t, i) => {
    const c = model[i];
    const chip = document.createElement("span");
    chip.className = "chip" + (c.on ? " on" : "") + (c.wait ? " wait" : "") + (c.status ? " trouble" : "");

    const main = document.createElement("button");
    main.type = "button";
    main.className = "chip-main";
    main.title = c.title;
    main.setAttribute("aria-label", (c.kind === "viewer" ? "File: " : "Session: ") + c.label);

    if (c.kind === "viewer") {
      // A small square marker, not a round activity dot — a viewer has no
      // activity of its own. A CSS shape rather than a symbol-font glyph:
      // see the Nerd Font "glyphs vanish on retype" gotcha in this repo's
      // memory — a plain drawn shape can't have that failure mode.
      const vic = document.createElement("span");
      vic.className = "vic";
      vic.setAttribute("aria-hidden", "true");
      main.appendChild(vic);
    } else {
      // Reuse activity.js's own class map (the same one the session row and the
      // tab-bar dot use) rather than re-deriving a state string.
      const a = (typeof activityClass === "function" && typeof t.activity === "function")
        ? activityClass(t.activity(), typeof t.needWhy === "function" ? t.needWhy() : "") : { cls: "dot unknown", title: "" };
      const dot = document.createElement("span");
      dot.className = a.cls;
      dot.setAttribute("aria-hidden", "true");
      main.appendChild(dot);
    }

    const nm = document.createElement("span");
    nm.className = "cnm";
    nm.textContent = c.label;
    main.appendChild(nm);

    // m4: a transient connection state (the same t.status() the desktop
    // tab-bar's .tab-st reads), on ANY terminal chip — not only the focused
    // one, since the point is to see trouble on a session you switched away
    // from. chipModel already suppresses the healthy "connected" state.
    if (c.kind === "terminal" && c.status) {
      const st = document.createElement("span");
      st.className = "chip-st";
      st.textContent = c.status;
      main.appendChild(st);
    }

    // Context %, same field/format the tab strip's own usage readout uses
    // (tabs.js renderBar / app.js renderSession) — terminal chips only, and
    // only the focused one, matching the model's `on`.
    if (c.kind === "terminal" && c.on && typeof t.usage === "function") {
      const u = t.usage();
      if (u && typeof u.pct === "number") {
        const pct = document.createElement("span");
        pct.className = "u";
        pct.textContent = u.pct + "%";
        main.appendChild(pct);
      }
    }

    main.addEventListener("click", () => focusMobileTab(t));
    chip.appendChild(main);

    // Only the focused chip carries a close control (c.closable === c.on).
    // It does exactly what the desktop cell-bar's per-tab × does today:
    // closes the VIEW, never ends the session (End stays in the ⋯ sheet).
    if (c.closable) {
      const x = document.createElement("button");
      x.type = "button";
      x.className = "chip-x";
      x.textContent = "×";
      x.title = "Close tab";
      x.setAttribute("aria-label", "Close tab");
      x.addEventListener("click", (e) => {
        e.stopPropagation();
        if (typeof closeTabEverywhere === "function") closeTabEverywhere(t);
        // m2/M1: repaint right here rather than waiting for the next
        // /api/sessions poll (closeTab's own refreshSessions call) — on a
        // slow link the closed tab, and its ×, would otherwise keep showing
        // for the fetch latency, or until the next successful poll at all.
        renderMobileChips();
      });
      chip.appendChild(x);
    }

    strip.appendChild(chip);
  });
  // m11: keep the focused chip in view when the strip overflows — a newly
  // opened tab (or one switched to via swipe/spawn) can land off-screen even
  // though the topbar already names it. `block: "nearest"` keeps this from
  // ever scrolling the page itself: #term-chips is the only scrolling
  // ancestor in play (vertically it already fits), so only its own
  // horizontal scroll position moves.
  //
  // Fix round 2: gated on shouldScrollToFocused, not run unconditionally.
  // This function also runs on every ~3s session poll (see _lastScrolledChip
  // above); without the gate, a poll landing while the operator had manually
  // scrolled the strip to look at a DIFFERENT chip would snap it straight
  // back to the focused one. Reset the tracker when the strip empties so a
  // later re-open scrolls again rather than staying silently stuck on a tab
  // object that no longer exists.
  if (tabs.length === 0) {
    _lastScrolledChip = null;
  } else if (shouldScrollToFocused(focused, _lastScrolledChip)) {
    const onChip = strip.querySelector(".chip.on");
    if (onChip && typeof onChip.scrollIntoView === "function") {
      onChip.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
    _lastScrolledChip = focused;
  }
  // The topbar mirrors this same focused-tab data (dot/name/context%), so
  // paint it right here rather than adding a second call at every one of
  // renderMobileChips' own call sites — the two can never drift apart.
  renderTermTopbar();
}

// --- mobile terminal: session topbar (M2) -----------------------------------
// Same data source as the focused chip above (the focused TAB's own
// activity()/name/usage(), not a session lookup) so the dot and badge can
// never disagree with the chip strip. ⋯ is the one control that needs a real
// session object (openActionsSheet reads s.cwd/s.claude/etc. for the tier
// badge and menu rows) — sessionForTab looks that up by the tab's id, which
// IS the session id (tabs.js:691/807 construct TerminalTab(id, name) from a
// session's own id).
function sessionForTab(t) {
  return t ? lastSessions.find((s) => s.id === t.id) || null : null;
}

// U1: the focused tab can now be a viewer, not only a terminal. A viewer
// shows its basename as the name, a neutral dot (the same "dot unknown"
// fallback already used below for "no tab focused" — a viewer genuinely has
// no activity of its own, so this is not a special case, just that same
// fallback), no ctx badge, and ⋯ (session actions) hidden+disabled since the
// sheet needs a real session. sessionForTab already returns null for a
// viewer's synthetic "view:<path>" id, so ⋯'s own onclick stays a safe no-op
// even if this guard were ever bypassed — belt and suspenders, not the only
// guard.
function renderTermTopbar() {
  const bar = document.getElementById("term-topbar");
  if (!bar) return;
  if (typeof mobileActive !== "function" || !mobileActive(window.innerWidth)) return;
  const dot = document.getElementById("tt-dot");
  const nm = document.getElementById("tt-name");
  const ctx = document.getElementById("tt-ctx");
  const more = document.getElementById("tt-more");
  const st = document.getElementById("tt-st");
  if (!dot || !nm || !ctx) return;
  const t = (typeof focusedTab === "function") ? focusedTab() : null;
  const isViewer = !!(t && t.kind === "viewer");
  if (more) { more.hidden = isViewer; more.disabled = isViewer; }
  if (isViewer) {
    dot.className = "dot unknown";
    dot.title = "";
    nm.textContent = t.name || "";
    ctx.textContent = "";
    ctx.hidden = true;
    // m4: a viewer has no connection state of its own — same reasoning as
    // the ctx badge above.
    if (st) { st.textContent = ""; st.hidden = true; }
    return;
  }
  const a = (t && typeof t.activity === "function")
    ? activityClass(t.activity(), typeof t.needWhy === "function" ? t.needWhy() : "") : { cls: "dot unknown", title: "" };
  dot.className = a.cls;
  dot.title = a.title;
  nm.textContent = t ? (t.name || t.id) : "";
  const u = (t && typeof t.usage === "function") ? t.usage() : null;
  if (u && typeof u.pct === "number") {
    ctx.textContent = u.pct + "%";
    ctx.hidden = false;
  } else {
    ctx.textContent = "";
    ctx.hidden = true;
  }
  // m4: mirror the desktop tab-bar's .tab-st (t.status()), same source and
  // the same "connected" is suppressed rule (tabs.js renderBar) — the healthy
  // steady state carries no information, every other value is a real signal.
  if (st) {
    const rawStatus = (t && typeof t.status === "function") ? t.status() : "";
    const text = rawStatus && rawStatus !== "connected" ? rawStatus : "";
    st.textContent = text;
    st.hidden = !text;
  }
}

const ttBack = document.getElementById("tt-back");
if (ttBack) ttBack.onclick = () => setMobileScreen("sessions");
const ttMore = document.getElementById("tt-more");
if (ttMore) ttMore.onclick = () => {
  const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  const s = sessionForTab(t);
  if (s) openActionsSheet(s);
};

// --- mobile terminal: snippet row ------------------------------------------
// ^C is a control byte sent immediately (panic stop, one tap); continue/yes/no//
// are TEXT inserted via sendText (never auto-submits — the user presses Enter).
// The split is the whole point: a control byte through sendText would be dropped,
// and text through sendKey could carry a submitting newline.
function renderMobileSnips() {
  const row = document.getElementById("term-snips");
  if (!row) return;
  row.textContent = "";
  for (const s of SNIPPETS) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "snip" + (s.danger ? " dgr" : "") + (s.ghost ? " gh" : "");
    b.textContent = s.label;
    b.setAttribute("aria-label", s.key === "sigint" ? "interrupt" : s.label);
    b.addEventListener("click", () => onSnip(s));
    row.appendChild(b);
  }
}

function onSnip(s) {
  const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  if (!t) return;
  if (s.kind === "control") { t.sendKey(s.bytes); t.term.focus(); return; }
  if (s.kind === "text") { t.sendText(s.text); t.term.focus(); return; }
  // s.kind === "more": overflow snippets are a later addition; no-op for now
  // (the button exists per M2, its menu content is not in this slice).
}

// --- mobile terminal: 7-key accessory bar ----------------------------------
// Fixed seven keys (esc tab ctrl ↑ ↓ ↵ ⋯), never reshaped between keyboard
// states. Control keys write raw bytes via sendKey; ctrl is a latch that arms
// the next typed char (handled in TerminalTab.onData). ⋯ reveals overflow keys.
const KEY_GLYPH = { esc: "esc", tab: "tab", ctrl: "ctrl", up: "↑", down: "↓", enter: "↵", more: "⋯",
                    left: "←", right: "→", pipe: "|", slash: "/", dash: "-", under: "_" };

function renderMobileKeybar() {
  const bar = document.getElementById("term-keybar");
  if (!bar) return;
  const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  const armed = !!(t && t.ctrlIsArmed && t.ctrlIsArmed());
  bar.textContent = "";
  for (const name of KEYBAR) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "key" + (name === "ctrl" && armed ? " armed" : "");
    b.dataset.key = name;
    b.textContent = KEY_GLYPH[name] || name;
    b.setAttribute("aria-label", name);
    b.addEventListener("click", () => onKeybarKey(name));
    bar.appendChild(b);
  }
}

function onKeybarKey(name) {
  const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  if (name === "more") { toggleKeybarMore(); return; }
  if (!t) return;
  if (name === "ctrl") { t.armCtrl(); renderMobileKeybar(); return; }
  const bytes = keyByte(name);
  if (bytes) t.sendKey(bytes);
  t.term.focus();  // keep the soft keyboard up after an accessory key
}

// The ⋯ popover: left/right arrows + a few symbols. Arrows send bytes; symbols
// insert TEXT (so a stray one never submits) via sendText. One pick closes it.
function toggleKeybarMore() {
  let pop = document.getElementById("keybar-more");
  if (!pop) {
    pop = document.createElement("div"); pop.id = "keybar-more";
    for (const name of KEYBAR_MORE) {
      const b = document.createElement("button"); b.type = "button"; b.className = "key";
      b.textContent = KEY_GLYPH[name] || name; b.setAttribute("aria-label", name);
      b.addEventListener("click", () => { onKeybarMoreKey(name); pop.classList.remove("open"); });
      pop.appendChild(b);
    }
    document.body.appendChild(pop);
  }
  const bar = document.getElementById("term-keybar");
  if (bar) { const r = bar.getBoundingClientRect(); pop.style.bottom = (window.innerHeight - r.top + 5) + "px"; }
  pop.classList.toggle("open");
}

function onKeybarMoreKey(name) {
  const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  if (!t) return;
  const bytes = keyByte(name);                       // left/right arrows
  if (bytes) { t.sendKey(bytes); t.term.focus(); return; }
  const text = SYMBOL_TEXT[name];                     // symbols are inserted text
  if (text) { t.sendText(text); t.term.focus(); }
}

// --- mobile terminal: soft-keyboard sizing ---------------------------------
// When the soft keyboard opens, visualViewport.height shrinks but 100vh does
// NOT — so a 100vh pane hides behind the keyboard. Size the mobile shell from
// visualViewport.height instead, hide the bottom nav + chip strip while it is
// up (M3), and refit xterm to the new height. Feature-detected: without
// visualViewport we leave the plain layout alone (no keyboard awareness).
function initMobileKeyboard() {
  const vv = window.visualViewport;
  if (!vv) return; // graceful fallback: no dynamic sizing on old browsers
  const apply = () => {
    const open = mobileActive(window.innerWidth) && keyboardOpen(window.innerHeight, vv.height, 120);
    document.documentElement.classList.toggle("kbd-open", open);
    document.documentElement.style.setProperty("--vvh", vv.height + "px");
    const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
    if (t && typeof t.fit === "function") requestAnimationFrame(() => t.fit());
  };
  vv.addEventListener("resize", apply);
  vv.addEventListener("scroll", apply);
  apply();
}

// onTerminalSwipe moves the focused pane one session left/right, cycling
// TERMINAL tabs only (openTerminalTabs, not the chip strip's openMobileTabs)
// — a swipe is bound to xterm, so a focused viewer has no gesture to drive
// this, and a viewer chip never counts as a stop along the way. "next" (drag
// left) goes forward, "prev" back. Clamped at the ends — a swipe past the
// last session does nothing rather than looping, so you always know where
// the edges are.
function onTerminalSwipe(dir) {
  const tabs = openTerminalTabs();
  if (tabs.length < 2) return;
  const cur = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
  let i = tabs.indexOf(cur);
  if (i < 0) i = 0;
  const j = dir === "next" ? Math.min(i + 1, tabs.length - 1) : Math.max(i - 1, 0);
  if (j !== i) focusMobileTab(tabs[j]);
}

// Shortcut hint: the right-hand half of the permanent #hint status bar
// (mockup D1 — no dismiss; a status bar you can't turn off, unlike the old
// tips bar it replaced). The runtime tag ("runc"/"sysbox") depends on
// boxRuntime, which is only known once /api/meta answers, so this must only
// ever be called from updateStatusLine() (runs post-boot, and again whenever
// sessions/meta refresh) — never at parse time: boxRuntime is a `let`
// declared far below in this file, and calling this before that declaration
// line has executed throws a TDZ ReferenceError that aborts the rest of
// app.js (blank footer AND no sidebar/sessions/terminals — this exact bug
// shipped once when an earlier IIFE called it at parse time; caught in
// review, since app.js isn't executed under node --test).
function hintShortcuts() {
  // Reuse tier.js's own runtime label (the same string the sidebar's tier
  // badge tooltip uses) rather than re-deriving "runc"/"sysbox" here.
  const runc = tierRuntimeLabel(boxRuntime);
  // Every chord named here is rendered from the registry (shortcuts.js), so the
  // bar cannot drift from the keys that work. The session jump is Mod+digit
  // (Cmd on macOS, Ctrl elsewhere), shown as the 1-9 range.
  return `${runc} · ${shortcutHint("palette.open", IS_MAC)} palette · ${shortcutHint("sidebar.toggle", IS_MAC)} sidebar`
    + ` · ${jumpHint(1, IS_MAC)}-9 session · ${shortcutHint("help.open", IS_MAC)} shortcuts`;
}

// shortcutsGridEl: the desktop's Keyboard shortcuts body (frame M5.3), from helpGrid (shortcuts.js): two columns
// of groups, a row per shortcut (its label, then its keys as the kit's .kbd chips, "or" between alternatives).
// Built with createElement and textContent, never innerHTML.
function shortcutsGridEl(grid) {
  const mk = (tag, cls, text) => {
    const n = document.createElement(tag);
    n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const cols = mk("div", "sc-cols");
  for (const col of grid) {
    const colEl = mk("div", "sc-col");
    for (const g of col) {
      const grp = mk("div", "sc-grp");
      grp.append(mk("div", "grp-label", g.group));
      for (const r of g.rows) {
        const row = mk("div", "sc-row");
        const chord = mk("span", "sc-chord");
        r.alts.forEach((alt, i) => {
          if (i) chord.append(mk("span", "sc-word", "or"));
          for (const t of alt) chord.append(t.kbd !== undefined ? mk("kbd", "kbd", t.kbd) : mk("span", "sc-word", t.text));
        });
        row.append(mk("span", "sc-label", r.label), chord);
        grp.append(row);
      }
      colEl.append(grp);
    }
    cols.append(colEl);
  }
  return cols;
}

// showShortcuts: the read-only "Keyboard shortcuts" dialog (palette command,
// desktop, Mod+/ and ?, Settings, About). The rows come from the registry (shortcuts.js).
// Desktop: two columns of key chips (shortcutsGridEl), no footer and no Close button (the X is the
// close), no Runtime line (Settings › About shows the box runtime) (F29, M5.3). Only .keys sizes the
// dialog, so the grid does not also ask for the wide card.
// Phone: the text goes in through textContent in askDialog, each row stacking (keys, then the
// description indented) because the aligned columns are wider than the dialog, then the Runtime
// line and Close. Decided when it opens (#59).
function showShortcuts() {
  if (!mobileActive(window.innerWidth)) {
    return askDialog({
      title: "Keyboard shortcuts",
      info: true,
      footless: true,
      cls: "keys",
      wide: false,
      extra: [shortcutsGridEl(helpGrid(IS_MAC))],
    });
  }
  // Next that needs you works where the sidebar groups are, so the phone's dialog does not list it.
  // The F6 region cycle has no regions to walk on a phone either.
  const desktopOnly = ["session.nextNeeds", "ui.regionNext", "ui.regionPrev"].map((id) => scEntry(id).label);
  const rows = helpRows(IS_MAC).filter((r) => !desktopOnly.includes(r.label));
  return askDialog({
    title: "Keyboard shortcuts",
    body: helpTextStacked(rows, tierRuntimeLabel(boxRuntime)),
    confirmLabel: "Close",
    info: true,
  });
}

// updateStatusLine keeps the bottom bar describing the current state. Everything
// it shows duplicates something already on screen — the env badge, the sessions
// count, the focused pane. The bar is now a permanent status bar with no dismiss.
function updateStatusLine() {
  const env = $("status-env");
  if (env && window.__envLabel) env.textContent = window.__envLabel.toUpperCase();
  const s = $("status-sessions");
  if (s) {
    const n = lastSessions.length;
    s.textContent = n === 1 ? "1 session" : n + " sessions";
  }
  const boxed = $("status-boxed");
  if (boxed) {
    // Same boxed-determination the sidebar tier badge uses — do not
    // re-derive it here.
    const m = lastSessions.filter((sess) => {
      const t = tierBadge(sess, boxRuntime);
      return t && t.text === TIER_BOX;
    }).length;
    boxed.textContent = m === 1 ? "1 boxed" : m + " boxed";
  }
  const cwd = $("status-cwd");
  if (cwd) {
    const t = (typeof focusedTerminal === "function") ? focusedTerminal() : null;
    cwd.textContent = t && t.cwd ? shortPath(t.cwd) : "";
  }
  const hintText = $("hint-text");
  if (hintText) hintText.textContent = hintShortcuts();
}

// renderRateLimitsInto paints the account 5h/weekly usage meters into a given
// host element (replacing its previous children) and returns how many meters
// it appended. Each window renders independently: an absent one contributes
// no meter at all, and if neither is present the host ends up empty (count
// 0) so a caller can hide its container rather than showing a 0% row.
//
// Built with createElement and textContent, never innerHTML — these values
// come from a file on disk written by an external hook.
//
// limitLine is the desktop sidebar's alone (#limits-side): a window at 100% or more
// swaps its percentage for "limit reached · resets 25m" instead of a number that
// reads like 78%. The header meters and the phone's band keep the percentage and the
// countdown; the line is no alert, so no amber and no notification ride on it.
function renderRateLimitsInto(host, rl, limitLine) {
  host.replaceChildren();
  const now = Math.floor(Date.now() / 1000);
  const windows = [["5h", rl && rl.five_hour], ["wk", rl && rl.seven_day]];
  let count = 0;
  for (const [label, win] of windows) {
    const m = meterModel(label, win, now);
    if (!m) continue;
    const el = document.createElement("div");
    el.className = "mtr";
    if (m.hot) el.classList.add("hot");
    if (m.unknown) el.classList.add("unk");
    // Staleness is per reading: the age belongs to this window's own number (a
    // window past its reset shows none, so has none).
    const note = m.unknown ? "" : acctAgeNote(acctWinAge(win, rl, now));
    if (note) el.classList.add("stale");
    const name = document.createElement("span");
    name.textContent = m.label;
    const bar = document.createElement("span");
    bar.className = "bar";
    const fill = document.createElement("i");
    fill.style.width = m.width;
    bar.appendChild(fill);
    const atLimit = !!limitLine && m.limit;
    const pc = document.createElement("span");
    pc.className = "pc";
    pc.textContent = m.unknown ? "–" : m.pct + "%";
    pc.hidden = atLimit;
    const rs = document.createElement("span");
    rs.className = atLimit ? "rs limit" : "rs";
    rs.textContent = atLimit ? m.limitText : m.reset;
    // A narrow sidebar cuts the line short: the full text is the title too.
    if (atLimit) rs.title = m.limitText;
    el.append(name, bar, pc, rs);
    if (note) {
      const ag = document.createElement("span");
      ag.className = "acct-age";
      ag.textContent = note;
      el.appendChild(ag);
    }
    // The bar carries no text, so the meter needs an accessible name of its own.
    el.setAttribute("role", "img");
    el.setAttribute("aria-label", (m.unknown ? m.label + " window reset, usage unknown" : m.label + " usage " + m.pct + "%, " + (atLimit ? m.limitText : m.reset)) + (note ? ", " + note : ""));
    host.appendChild(el);
    count++;
  }
  return count;
}

// renderRateLimits paints one agent account's 5h/weekly usage into the header
// and, when present, mirrors the same meters into the mobile Sessions-screen
// band (#limits-mobile, slice 5a). rl is /api/meta.rate_limits — a map keyed
// by account (acctLimitsMap also takes the pre-accounts flat shape). The
// account shown is the focused session's (headerAccount, accounts.js), else
// the default; a non-default one is labelled. A meter whose own reading is older
// than ACCT_STALE_SEC is dimmed and carries its age (renderRateLimitsInto), so
// it is never read as current. The mobile slot is
// hidden by count rather than by html-string truthiness (there is no html
// string — renderRateLimitsInto builds DOM nodes) so an absent reading
// vanishes the band cleanly instead of leaving an empty 0% row.
function renderRateLimits(rl) {
  const name = headerAccount();
  acctHeaderShown = name;
  const now = Math.floor(Date.now() / 1000);
  // One reading for every meter: the highest still-live one, so two sessions on
  // one token do not make the header flip between their numbers (acctBestLimits).
  const lims = acctBestLimits(acctLimitsMap(rl)[name] || null, now);
  const age = acctLimitsAge(lims && lims.updated_at, now);
  for (const id of ["limits", "limits-mobile", "limits-side"]) {
    const host = document.getElementById(id);
    if (!host) continue;
    const count = renderRateLimitsInto(host, lims, id === "limits-side");
    if (count && name !== ACCT_DEFAULT) {
      const lab = document.createElement("span");
      lab.className = "acct-lab";
      lab.textContent = name;
      host.prepend(lab);
    }
    host.title = count ? (name + " · updated " + age.text) : "";
    if (id === "limits-mobile" || id === "limits-side") host.hidden = count === 0;
  }
  paintRailMeters(); // the rail's mini bars mirror #limits-side
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
// replyFlash: the line a finished send leaves under the reply field, per session
// ({ text, until } in ms): "Sent" for REPLY_SENT_MS, or why the send failed for
// REPLY_ERROR_MS (long enough to read after looking away, short enough not to
// outlive the state it described; the next try clears it). Module level for the
// reason pendingSends is: the 3 s poll rebuilds the strip while a send just
// landed, and a line kept in the closure would go with the old field.
// buildReplyComposer's refresh() reads it, so a rebuilt field shows what is left.
const REPLY_SENT_MS = 2000;
const REPLY_ERROR_MS = 30000;
let replyFlash = new Map();
// cardExpanded: desktop needs-you cards currently showing their full message
// instead of cardContent's one-line text. Page-local, keyed by session id —
// unlike openPeekId this is NOT "one at a time": several waiting sessions can
// each have their card expanded together. Fed into rowLocal/rowSig (like
// `sending` above) so an expanded card survives a 3s poll that changes
// nothing else, and pruned in refreshSessions once a session stops waiting.
let cardExpanded = new Set();
// cardPeekCache: the last full-text fetch per expanded card ({text, muted}),
// so a poll-triggered rebuild of an already-expanded card does not flash back
// to "Loading…" for a fetch it already made. Cleared alongside cardExpanded.
let cardPeekCache = new Map();
// cardAskSeen: for an expanded card, the ask_full it showed when it was
// expanded — Yes/No unlock only for that exact command (answerState), and a
// different one arriving collapses the card (refreshSessions).
let cardAskSeen = new Map();
// pendingAnswers: sessions with an /answer in flight (the double-click latch;
// module-level for the same reason pendingSends is — a poll can rebuild the
// card mid-request). Fed into rowLocal/rowSig.
let pendingAnswers = new Set();

// syncMaskHint keeps the display-mask note as the sibling right after `anchor`
// (a text block already in the DOM): present iff the text shown there carries a
// mask token. textContent only; the hint is a fixed string, the text is data.
function syncMaskHint(anchor, text) {
  if (!anchor) return;
  const next = anchor.nextElementSibling;
  const have = next && next.classList.contains("maskhint") ? next : null;
  const msg = maskHint(text);
  if (!msg) { if (have) have.remove(); return; }
  if (have) { have.textContent = msg; return; }
  const p = document.createElement("p");
  p.className = "tiny mut maskhint";
  p.textContent = msg;
  anchor.after(p);
}

// peekRowFor: the answer block belonging to a row, if it is open. A sibling,
// not a child — see the stylesheet for why the amber column needs it there.
function peekRowFor(li) {
  const n = li && li.nextElementSibling;
  return n && n.classList && n.classList.contains("peek") ? n : null;
}

// sendReplyFor posts one reply for session `s` and reports the outcome the
// same way everywhere: a toast, and (via `onFail`, when given) the exact
// message text a caller wants to keep showing after the toast fades. It is
// the ONE place the 403/409/410/400 mapping lives — extracted out of
// togglePeek's composer so the mobile reply sheet (openReplySheet) can share
// it without a live `<li>` to close over. Returns true on success (204),
// false otherwise; never throws. opts.quiet: no "Sent" toast, for the desktop
// composer, which says it under its own field (F65); an error still toasts.
async function sendReplyFor(s, text, onFail, opts) {
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/message", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: text }),
    });
    if (res.status === 204) {
      if (!(opts && opts.quiet)) showToast("Sent");
      return true;
    }
    // Distinct statuses carry distinct meanings — say which one happened.
    // Routed through showToast, not just a caller-owned element: a poll can
    // rebuild the inline composer's row (detaching its `why` span) while the
    // request is still in flight, the same reason cb.onchange's failure path
    // already toasts instead of relying solely on an in-row element.
    const msg =
      res.status === 403 ? "Replies are not enabled for this session." :
      res.status === 409 ? "Session is not ready for a message right now." :
      res.status === 410 ? "That session is gone." :
      res.status === 400 ? "That text was rejected." :
      "Send failed (" + res.status + ").";
    if (onFail) onFail(msg);
    showToast(msg, { error: true });
    return false;
  } catch (_) {
    const msg = "Send failed: no connection.";
    if (onFail) onFail(msg);
    showToast(msg, { error: true });
    return false;
  }
}

// postReplyable grants or revokes a session's sidebar-reply opt-in. true on a
// 2xx; never throws. Shared by the peek checkbox and the needs-you card's
// "Enable replies".
async function postReplyable(id, want) {
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(id) + "/replyable", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ replyable: want }),
    });
    return res.ok;
  } catch (_) {
    return false;
  }
}

// buildEnableRepliesButton is the ONE "Enable replies" control: the needs-you
// card, the steer rail composer and the pane reply strip all use it. It grants
// the opt-in, re-polls (every gate re-reads `replyable` from the fresh list),
// then runs `after` — the card uses it to focus its new reply field.
function buildEnableRepliesButton(id, after) {
  const en = document.createElement("button");
  en.type = "button";
  en.className = "btn";
  en.textContent = "Enable replies";
  en.title = "Allow answering this session from CuliBox";
  en.onclick = async (e) => {
    e.stopPropagation();
    en.disabled = true;
    if (!(await postReplyable(id, true))) {
      en.disabled = false;
      showToast("Could not change the reply setting", { error: true });
      return;
    }
    await refreshSessions();
    en.disabled = false;
    if (after) after();
  };
  return en;
}

// sendAnswer answers a permission prompt with ONE key (chosen by the server:
// "1" or Esc), for exactly the command this card rendered — the body carries
// sha256Hex(ask_full), and the server refuses (409) a prompt that changed
// since. Whatever happens the card collapses: another prompt often follows a
// Yes at once, and it must be expanded and read before it can be answered.
async function sendAnswer(s, choice) {
  if (pendingAnswers.has(s.id) || !s.ask_full) return;
  pendingAnswers.add(s.id);
  let status = 0, msg = "";
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/answer", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ choice: choice, ask_sha256: sha256Hex(s.ask_full) }),
    });
    status = res.status;
    if (!res.ok) { try { msg = ((await res.json()) || {}).error || ""; } catch (_) {} }
  } catch (_) {
    status = 0;
  }
  cardExpanded.delete(s.id);
  cardPeekCache.delete(s.id);
  cardAskSeen.delete(s.id);
  pendingAnswers.delete(s.id);
  if (status === 204) showToast(choice === "yes" ? "Answered Yes" : "Answered No");
  else showToast(answerErrorText(status, msg), { error: true });
  await refreshSessions();
}

// postSessionAction: POST /api/sessions/{id}/{action} with no body. Resolves to
// the Response; never throws — a failed request resolves to {ok:false,status:0}.
// It gives up after `ms` (deadline.js; #257): the result is then also {timedOut:true,
// error}, which showActionTimeout words. The server still finishes what it was asked,
// so a late success is what the next poll shows.
async function postSessionAction(id, action, ms = ACTION_TIMEOUT_MS) {
  try {
    return await withDeadline(ms, (signal) => fetch("/api/sessions/" + encodeURIComponent(id) + "/" + action, { method: "POST", signal }));
  } catch (e) {
    const out = { ok: false, status: 0, json: async () => null };
    if (e && e.timedOut) { out.timedOut = true; out.error = e; }
    return out;
  }
}

// showActionTimeout: the error toast of a session action that got no answer in time,
// the standard "Could not <what>." with the raw text behind Copy details (toastFailText),
// the same as an End that fails any other way.
function showActionTimeout(what, res) {
  const f = toastFailText(what, res.error);
  showToast(f.text, { error: true, detail: f.detail });
}
// actionSubject: how a toast names the session an action was for.
function actionSubject(id) {
  const s = (lastSessions || []).find((x) => x.id === id);
  return (s && (s.name || s.id)) || id;
}

// bringHomeNow: "Bring work home now". The request only starts the return (202);
// its result lands on the session's return state and noteReturns toasts it.
async function bringHomeNow(s) {
  const res = await postSessionAction(s.id, "bring-home");
  if (res.timedOut) { showActionTimeout("bring work home from " + actionSubject(s.id), res); return; }
  if (!res.ok) {
    let msg = "";
    try { msg = ((await res.json()) || {}).error || ""; } catch (_) {}
    showToast(msg ? "Bring work home failed: " + msg : actionFailText("bringhome", res.status), { error: true });
    return;
  }
  // The request answers 202 at once, so a pending toast would never show.
  showToast("Bringing work home…");
}

// sessionWorkHome: the "work comes home" suffix for a clone session's line. The
// profile is looked up by the session's agent; with none found there is no
// suffix (workHomeSuffix of a bare clone_of would wrongly say auto-return is off).
function sessionWorkHome(s) {
  const p = profiles.find((x) => x.name === s.agent);
  return p ? workHomeSuffix(p, !!(s.clone && s.clone.push_repo)) : "";
}

// toastReturnSuffix: the spawn toast's tail for the return that ran before the
// spawn. "" when it has nothing to say.
function toastReturnSuffix(s) {
  const t = returnToastText(s.agent, s.clone && s.clone.return);
  return t ? " \u00b7 " + t : "";
}

// What the last sessions poll showed, for returnNews: seen is null until the
// first poll after boot, which seeds it silently (returns that finished before
// the page opened are not news).
let returnsSeen = null;
let returnsCloneIds = [];
// noteReturns: the sessions poll's return handling, a thin caller of returnNews.
function noteReturns(sessions) {
  const n = returnNews(returnsSeen, returnsCloneIds, sessions);
  returnsSeen = n.seen;
  returnsCloneIds = n.cloneIds;
  for (const t of n.toasts) showToast(t);
  // With no stored list there is nothing to add to: the next load seeds it whole.
  const stored = n.sigs.length ? notHomeSeenRead() : null;
  if (stored) notHomeSeenWrite(stored.concat(n.sigs));
  if (n.refetch) refreshProfilesQuiet();
}

// refreshProfilesQuiet: the background profile refresh (a return landed, a clone
// session ended). Unlike loadProfiles it repaints only what shows not-home state:
// loadProfiles rebuilds the profile segment and the option controls from profile
// defaults, which would reset a New-session dialog the user is editing.
let quietProfilesSeq = 0;
async function refreshProfilesQuiet() {
  const seq = ++quietProfilesSeq;
  let got;
  try { got = await api("/api/profiles", { signal: deadlineSignal(5000) }); } catch (_) { return; }
  if (seq !== quietProfilesSeq || !Array.isArray(got)) return;
  profiles = got;
  profilesLoaded = true;
  notHomeToasts();
  renderProfileList();
  renderSpawnPlist(); // the desktop dialog's profile list only: keeps its search box
  syncSpawnCopyLine();
  if (selectedProfile) renderPfNotHome(selectedProfile);
}

// The not-home signatures already announced, kept across reloads. Storage can be
// absent or full: every access is guarded, and "no list" seeds silently.
const NOT_HOME_SEEN_KEY = "ab.notHomeSeen";
function notHomeSeenRead() {
  try {
    const v = JSON.parse(localStorage.getItem(NOT_HOME_SEEN_KEY) || "null");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string") : null;
  } catch (_) { return null; }
}
function notHomeSeenWrite(list) {
  try { localStorage.setItem(NOT_HOME_SEEN_KEY, JSON.stringify(list)); } catch (_) {}
}
// notHomeToasts: after each profile load, one toast per profile whose last
// return left branches behind and was not announced yet.
function notHomeToasts() {
  const seen = notHomeSeenRead();
  const news = notHomeNews(seen || [], profiles);
  // First load with nothing stored (or no storage) only records what is there.
  if (seen !== null) for (const t of news.toasts) showToast(t);
  notHomeSeenWrite(news.seen);
}

// interruptSession: no confirmation (spec §2.4) — it only stops work.
async function interruptSession(id) {
  const res = await postSessionAction(id, "interrupt");
  if (res.timedOut) { showActionTimeout("interrupt " + actionSubject(id), res); return; }
  if (!res.ok) showToast(actionFailText("interrupt", res.status), { error: true });
}

// restartSession replaces an ended session with a new one from the same spec;
// its tab is swapped for the new session's.
//
// trigger: the control that was clicked, when one stays on screen (the pane's
// Restart button). It shows the wait itself ("Restarting…"); without one
// (palette, sidebar menu) a toast does, after the pending delay, and an error
// toast replaces it.
async function restartSession(s, trigger) {
  if (trigger) return withPending(trigger, "Restarting…", () => restartSessionRun(s));
  const done = pendingToast("Restarting " + (s.name || s.id) + "…");
  try {
    return await restartSessionRun(s);
  } finally {
    done();
  }
}

async function restartSessionRun(s) {
  endedMark(endedByMe, s.id, Date.now()); // the old session leaves on purpose
  let res;
  try { res = await postSessionAction(s.id, "restart", ACTION_SLOW_TIMEOUT_MS); }
  catch (e) { endedByMe.delete(s.id); throw e; }
  if (res.timedOut) {
    // No answer: the restart may still happen, and the next poll shows it. Nothing here claims it did not, so the
    // ended-by-me mark stays (it expires by itself): the old session's exit must not notify "Ended" when the server finishes.
    showActionTimeout("restart " + (s.name || s.id), res);
    await refreshSessions();
    return;
  }
  if (!res.ok) {
    endedByMe.delete(s.id);
    let msg = "", code = "";
    try { const b = (await res.json()) || {}; msg = b.error || ""; code = b.code || ""; } catch (_) {}
    // 423 = the store is locked: its message names the way out, and the banner
    // (refreshStore) is where the passphrase goes. 503 store_unavailable = the
    // Keychain failed: its message names the fix, and there is nothing to type.
    const named = (res.status === 400 || res.status === 423 || spawnRefusedUnavailable(res.status, code)) && msg;
    showToast(named ? "Restart failed: " + msg : actionFailText("restart", res.status), { error: true });
    if (res.status === 423) refreshStore();
    await refreshSessions();
    return;
  }
  restartedByMe.add(s.id); // the mark ends when a poll sees this id live again
  const ns = await res.json();
  const wasDocked = !!dockHolding(s.id) && !mobileActive(window.innerWidth); // a docked shell comes back docked
  closeTerminal(s.id);
  await refreshSessions();
  if (!(wasDocked && dockShellBesideParent(ns))) openTerminal(ns.id, ns.name);
  const note = restartNotice(ns);
  if (note) showToast(note);
}

// setRepliesFromMenu flips the reply opt-in from the menu or the palette.
async function setRepliesFromMenu(id, want) {
  if (!(await postReplyable(id, want))) {
    showToast("Could not change the reply setting", { error: true });
    return;
  }
  await refreshSessions();
}

// buildReplyComposer builds the one-line reply field + Send button + status
// caption shared by the sidebar peek panel (togglePeek) and the desktop
// needs-you card (renderNeedsBody) — one place for draft-restore, the
// replyFocused guard, the send gate and the in-flight latch, instead of two
// near-identical copies.
//
// `opts.replyable()` and `opts.pending()` are read at gate time, not once at
// build time, because the two callers know these differently: togglePeek's
// panel has a live checkbox and its own /peek fetch's `pending`; the card has
// neither and reads `s.replyable` / `s.need === "ask"` straight off the
// latest session record. `opts.onGate(closed)` lets a caller keep its own
// extra controls (togglePeek's quick-reply buttons) in step with the same
// `closed` value. `opts.ariaLabel` names the field for the card, which has no
// visible "Reply to <session>" context the way the peek panel's row does.
// `opts.afterSend()` runs once a send settles (success or failure), after the
// shared refresh(): togglePeek needs nothing extra, the card forces its own
// re-render because its controls are not otherwise repainted by the poll.
//
// Returns { form, why, refresh, send } — `send(text)` is exposed so a caller
// can reuse the exact same validate/latch/refresh path for other controls
// (togglePeek's three quick-reply digits) instead of re-deriving it.
function buildReplyComposer(s, opts) {
  const o = opts || {};
  const form = document.createElement("div");
  form.className = "rep";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "fld";
  input.placeholder = "Reply (one line)";
  if (o.ariaLabel) input.setAttribute("aria-label", o.ariaLabel);
  const send = document.createElement("button");
  send.type = "button";
  send.className = "btn pri";
  send.textContent = "send";
  form.append(input, send);
  const why = document.createElement("span");
  why.className = "peek-why";

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
  // refresh() writes the disabled flags from the gate every time it runs, so
  // anything set behind its back is undone by the next call.
  let flashTimer = null;
  const refresh = () => {
    // The poll's live copy, not the one this composer was built with: it can
    // outlive several polls (an opt-in, reply_ready turning true).
    const cur = lastSessions.find((x) => x.id === s.id) || s;
    const st = sendState({
      replyable: o.replyable ? o.replyable() : cur.replyable,
      activity: cur.activity,
      pending: o.pending ? o.pending() : false,
      need: cur.need,
      reply_ready: cur.reply_ready,
    });
    const closed = !st.enabled || pendingSends.has(s.id);
    // Three things move together, deliberately. The dimmed look says closed,
    // the caption says why, and the real disabled attribute makes it true —
    // a control that looks closed but still takes a keystroke is a control
    // whose caption lies, and a keyboard user is the one who finds out.
    send.disabled = closed;
    input.disabled = closed;
    send.classList.toggle("off", closed);
    input.classList.toggle("off", closed);
    // What the last send left (replyFlash) outranks the gate's reason until it
    // runs out: a refresh that overwrote it would hide "Sent" at once, and the
    // failure the operator most needs to read right after a refused send.
    const flash = replyFlash.get(s.id);
    const live = !!flash && flash.until > Date.now();
    if (flash && !live) replyFlash.delete(s.id);
    why.textContent = live ? flash.text : st.reason;
    clearTimeout(flashTimer);
    flashTimer = live ? setTimeout(refresh, flash.until - Date.now()) : null;
    if (o.onGate) o.onGate(closed);
    return st;
  };

  // sendText is shared by the composer's own Send button and, for togglePeek,
  // the three quick-reply digit buttons: one send path, one error mapping,
  // one refresh(). The POST + the 403/409/410/400 mapping live once in the
  // top-level sendReplyFor; this adds what is specific to a one-line
  // composer: the text-validation error shown before a request is even made,
  // the in-flight latch, and clearing the field/draft on success.
  async function sendText(text) {
    if (pendingSends.has(s.id)) return; // a second tap while the first is still in flight
    const err = replyTextError(text);
    if (err) {
      why.textContent = err === "empty" ? "Type something first."
        : err === "newline" ? "One line only — newlines are not sent."
        : "That text contains control characters and was not sent.";
      return;
    }
    replyFlash.delete(s.id);
    pendingSends.add(s.id);
    refresh();
    try {
      // The button says "Sending…" if the request takes a moment (F65); the
      // outcome is the line under the field (replyFlash), so no "Sent" toast.
      const ok = await withPending(send, "Sending…", () =>
        sendReplyFor(s, text, (msg) => { replyFlash.set(s.id, { text: msg, until: Date.now() + REPLY_ERROR_MS }); }, { quiet: true }));
      if (ok) {
        input.value = "";
        if (replyDraft && replyDraft.id === s.id) replyDraft = null; // sent — nothing left to restore
        replyFlash.set(s.id, { text: "Sent", until: Date.now() + REPLY_SENT_MS });
      }
    } finally {
      // Runs whether the send succeeded, was rejected, or threw — including
      // when the session has since left the list — so the latch never
      // outlives the request that set it.
      pendingSends.delete(s.id);
      refresh();
      // Disabling the field above (inside refresh(), while pendingSends still
      // held this id) already blurs it in a real browser, which clears this
      // through the field's own onblur — but that is indirect enough that it
      // is worth stating outright rather than relying on it: a send must
      // never leave the poll thinking a gone or re-rendered field is still
      // focused and holding the list rebuild back.
      replyFocused = false;
      if (o.afterSend) o.afterSend();
    }
  }

  send.onclick = () => sendText(input.value);
  if (o.submitOnEnter) {
    input.onkeydown = (e) => {
      // An IME (composing Vietnamese, CJK, …) commits its candidate with
      // Enter too — without this guard that keystroke both confirms the
      // candidate AND sends the still-mid-composition text. keyCode 229 is
      // the historical Safari/older-browser signal; isComposing is the
      // modern one, kept together since one browser is missing per source.
      if (e.isComposing || e.keyCode === 229) return;
      if (e.key === "Enter") { e.preventDefault(); sendText(input.value); }
    };
  }
  return { form, why, refresh, send: sendText };
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
  peek.className = needsYou(s) ? "peek needs" : "peek";
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
  syncMaskHint(body, body.textContent);
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
  syncMaskHint(body, st.text);
  li.dataset.peekPending = payload && payload.pending ? "1" : "";
  lastPeek = { id: s.id, text: st.text, muted: st.kind !== "text", pending: !!(payload && payload.pending) };

  // Replies off (server kill-switch, /api/meta): the panel is the text only. The
  // opt-in, the quick replies and the field are not built, so there is no hidden
  // focusable input and nothing for replyFocused to guard.
  if (globalThis.repliesEnabled !== true) return;

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
    b.onclick = () => composer.send(digit);
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

  // The inline composer itself (field + send + why) is shared with the
  // desktop needs-you card via buildReplyComposer — see its own comment.
  // `onGate` keeps the three quick-reply buttons above in the same
  // enabled/disabled state as the field: they are what an operator taps
  // fast, and a double-tapped "2" answers two different questions on a
  // numbered prompt (the server does not dedupe).
  const composer = buildReplyComposer(s, {
    replyable: () => cb.checked,
    pending: () => li.dataset.peekPending === "1" || s.need === "ask",
    onGate: (closed) => {
      for (const b of qk.querySelectorAll(".btn")) {
        b.disabled = closed;
        b.classList.toggle("off", closed);
      }
    },
  });
  peek.append(composer.form, composer.why);
  composer.refresh();

  cb.onchange = async () => {
    const want = cb.checked;
    if (await postReplyable(s.id, want)) {
      s.replyable = want;
      // The server's reply_ready moves with replyable: fetch the list now, so
      // composer.refresh() (which reads the live copy) sees it at once.
      await refreshSessions();
    } else {
      cb.checked = !want; // the server is the truth; put the box back
      showToast("Could not change the reply setting", { error: true });
    }
    composer.refresh();
  };
}

// toggleCardPeek expands or collapses a desktop needs-you card's text block in
// place — the card's counterpart to togglePeek's sidebar answer block, for
// exactly the sessions an operator is about to answer. Collapsing is
// immediate (local state only); expanding fetches the same `/peek` endpoint
// togglePeek uses and shows the full text via textContent, never innerHTML.
// The rebuild that shows "Loading…" happens through the normal rowSig path
// (cardExpanded is part of rowLocal), so this only needs to patch the fetched
// text into the DOM once it lands — the row itself is not torn down again in
// between (rowSig does not change a second time) so the node this looked up
// is still the right one to write into, guarded by cardExpanded staying set.
async function toggleCardPeek(s) {
  if (cardExpanded.has(s.id)) {
    cardExpanded.delete(s.id);
    cardPeekCache.delete(s.id);
    cardAskSeen.delete(s.id);
    cells.forEach((c) => c.syncNeeds()); // the desktop strip collapses at once
    await refreshSessions();
    return;
  }
  cardExpanded.add(s.id);
  cardAskSeen.set(s.id, s.ask_full || ""); // what the operator is about to read
  cells.forEach((c) => c.syncNeeds()); // the desktop strip, showing "Loading…"
  await refreshSessions(); // rebuilds the card now, showing "Loading…"
  let status = 0;
  let payload = null;
  try {
    const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/peek");
    status = res.status;
    if (status === 200) payload = await res.json();
  } catch (_) {
    status = 0;
  }
  if (!cardExpanded.has(s.id)) return; // collapsed again while the fetch was in flight
  const st = peekState(!!s.claude, status, payload);
  cardPeekCache.set(s.id, { text: st.text, muted: st.kind !== "text" });
  // The desktop ask strip draws the cached text; it repaints on the cache
  // arriving (its key reads cardPeekCache), not on the sidebar row.
  cells.forEach((c) => c.syncNeeds());
  const li = sessionRowFor(s.id);
  const txt = li && li.querySelector(".nbody .peektxt");
  if (!txt) return; // the row moved on (e.g. stopped waiting) before this landed
  txt.textContent = st.text;
  txt.classList.toggle("muted", st.kind !== "text");
  syncMaskHint(txt, st.text);
}

// usageOf(tab) → the usage object for an already-resolved terminal tab, or
// null. Shared by renderSession (paints the context-% badge) and rowLocal
// (feeds rowSig into reconcileRows) so the two never compute usage two
// different ways. Takes the tab, not a session id: both callers already have
// to resolve terminalTabFor(s.id) for their own other purpose (the elsewhere
// badge / heldHere), and a second sid-keyed lookup here would scan cells×tabs
// twice per session per poll for no reason.
function usageOf(tab) {
  return tab && typeof tab.usage === "function" ? tab.usage() : null;
}

function renderSession(s, opts) {
  // card: render this waiting session as the desktop needs-you card — the same
  // row, expanded in place with no click (renderNeedsBody). Row order is never
  // changed for it.
  const card = !!(opts && opts.card);
  // compact: the desktop sidebar's row (dot, name, one chip, ⋯, one sub-line).
  // Desktop never renders the needs-you card: the ask strip over the pane
  // carries it.
  const compact = !!(opts && opts.compact);
  const li = document.createElement("li");
  // rowClasses decides the state; this function only paints it. Selected and
  // needs-you are separate axes and a row can carry both.
  const cls = rowClasses(s, { selectedId: selectedSessionId(), killing: killing.has(s.id) });
  // The compact row says "needs you" with its amber sub-line, not the row tint.
  li.className = (compact ? cls.filter((c) => c !== "needs").concat("drow") : cls).join(" ") + (card ? " dcard" : "");
  li.dataset.sid = s.id;
  li.title = s.id; // tiebreaker when two labels match

  // The desktop row's dot is deskDot (a finished turn is neutral, F21-B); the
  // phone's rows (never compact) keep sessionDot. takenOver: another tab of this
  // browser took the session over, a fact only this page's terminal tab knows;
  // the compact row's dot and sub-line say so in the shared connection words (F55).
  const tab = terminalTabFor(s.id);
  const takenOver = compact && !!tab && tab.status() === "taken over";
  const act = compact ? deskDot(s, { takenOver }) : sessionDot(s);
  const adot = document.createElement("span");
  adot.className = act.cls;
  // Someone else is holding this session from another browser. It rides ON the
  // activity dot as an outline rather than as a second marker: the row has one
  // status light, and a second square beside it read as a second state.
  const elsewhere = attachedElsewhere(s, !!tab && tab.status() === "connected");
  if (elsewhere) adot.classList.add("watching");
  // An open write window rings the dot in red (the kit's .dot.is-elevated, digest M6.4). Where
  // "viewed elsewhere" also holds, desktop.css draws both rings. The phone's rows are never compact.
  if (compact && sessionElevated(s, Date.now())) adot.classList.add("is-elevated");
  // A taken-over row keeps the outline (another tab holds the session) but its
  // tooltip is the connection word alone: "open in another tab" already says who.
  const viewed = elsewhere && !takenOver;
  const label = viewed
    ? (act.title ? act.title + "; someone is viewing this session elsewhere"
                 : "Someone is viewing this session elsewhere")
    : act.title;
  if (label) {
    adot.title = viewed
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
  // A companion shell listed under its parent (desktop sidebar only) reads
  // "↳ shell N". Mobile never passes compact, so its rows are unchanged.
  const nested = compact && isNestedShell(s, lastSessions);
  nm.textContent = nested ? shellLabel(s, lastSessions) : (s.name || s.id);
  sname.appendChild(nm);
  grow.appendChild(sname);
  if (nested) {
    li.classList.add("nested");
    const arrow = document.createElement("span");
    arrow.className = "nest-arrow";
    arrow.setAttribute("aria-hidden", "true");
    arrow.textContent = "\u21B3"; // ↳
    li.insertBefore(arrow, adot);
  }

  if (!compact) {
    const smeta = document.createElement("div");
    smeta.className = "smeta";
    smeta.textContent = sessionMeta(s, s.clone_of ? wmCopyLine(s.clone_of, window.HOME_DIR) + sessionWorkHome(s) : s.cwd ? shortPath(s.cwd) : "", needLabel(s) || act.title);
    grow.appendChild(smeta);
  }
  li.appendChild(grow);

  if (killing.has(s.id)) {
    const st = document.createElement("span");
    st.className = "bdg";
    st.textContent = "ending…";
    sname.appendChild(st);
    return li;
  }

  // A nested shell is revealed beside its parent (docked), not opened as a pane.
  nm.onclick = () => (nested ? revealShell(s.id) : openTerminal(s.id, s.name));
  nm.ondblclick = (e) => { e.stopPropagation(); startRename(li, nm, s); };

  if (compact) {
    paintCompactRow(li, sname, grow, s, { takenOver });
    return li;
  }

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
  const ab = acctBadgeEl(s);
  if (ab) sname.appendChild(ab);
  const awsb = awsBadgeEl(s);
  if (awsb) sname.appendChild(awsb);

  const right = document.createElement("div");
  right.className = "sright";
  // Context percentage, when there is one to show. This reads the usage already
  // fetched for an open terminal tab; there is deliberately no per-row request,
  // because a fetch per session per poll is a lot of traffic for two characters.
  // No placeholder when it is absent — a dash would read as "zero".
  const u = usageOf(tab);
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
  kb.onclick = () => (mobileActive(window.innerWidth) ? openActionsSheet(s) : openSessionMenu(kb, s));
  right.appendChild(kb);
  li.appendChild(right);

  if (s.claude && !card) {
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
    // Mobile has no room to expand a row in place — the summary tap opens
    // the M4 reply sheet instead of the inline composer. Desktop is
    // unchanged: togglePeek still owns the sidebar's own expand/collapse.
    const toggle = (e) => {
      e.stopPropagation();
      if (mobileActive(window.innerWidth)) openReplySheet(s);
      else togglePeek(li, s);
    };
    sum.onclick = toggle;
    sum.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") toggle(e); };
    grow.appendChild(sum);
  }
  if (card) grow.appendChild(renderNeedsBody(s));
  return li;
}

// paintCompactRow adds the rest of a desktop sidebar row: ONE chip (rowChip),
// the ⋯ menu button and the sub-line. The chip carries the tier or the open
// write window; the account and AWS binding are in the row's tooltip instead of
// further chips, so a row never grows a second line of badges. conn is what only
// the page knows about the connection ({ takenOver }), for the sub-line's words.
function paintCompactRow(li, sname, grow, s, conn) {
  const nowMs = Date.now();
  const chip = rowChip(s, nowMs, boxRuntime);
  const facts = [s.id];
  const ab = acctBadge(s);
  if (ab) facts.push(ab.title);
  const aws = awsBadge(s, nowMs);
  if (aws) facts.push("AWS: " + aws.title);
  if (s.clone_of) facts.push(wmCopyLine(s.clone_of, window.HOME_DIR) + sessionWorkHome(s));
  li.title = facts.join("\n");
  // No per-session cost on the row (the M1 frames draw none; founder 2026-11-08): the pane
  // header's meta and Analytics show it.
  if (chip) {
    const c = document.createElement("span");
    c.className = "dchip " + chip.tone;
    c.textContent = chip.text;
    const tier = tierBadge(s, boxRuntime);
    c.title = chip.tone === "write" ? (aws ? aws.title : "AWS write window open") : tier ? tier.title : "";
    sname.appendChild(c);
  }
  const kb = document.createElement("button");
  kb.type = "button";
  // The kit's ghost icon button, 24px (the frames' .row-menu); .kbtn keeps the reveal on hover.
  kb.className = "kbtn btn btn-ghost btn-icon btn-sm";
  kb.textContent = "⋯"; // U+22EF, as on the mobile row
  kb.setAttribute("aria-label", "Session actions");
  kb.setAttribute("aria-haspopup", "menu");
  // The kit's disclosure state: expanded while this row's own menu is open (desktop.css shows it pressed).
  kb.setAttribute("aria-expanded", rowMenuOpenFor(s.id) ? "true" : "false");
  kb.title = "Session actions";
  // No stopPropagation: see the mobile row's note on the document-click closers.
  kb.onclick = () => openSessionMenu(kb, s);
  sname.appendChild(kb); // on the name line, after the chip
  // A nested shell has no sub-line: it has no summary and no ask to show.
  const sub = li.classList.contains("nested") ? { text: "" } : rowSubline(s, conn);
  if (sub.text) {
    const d = document.createElement("div");
    d.className = sub.amber ? "sub amber" : "sub";
    if (sub.mono) {
      // An ask: "Asks to run" and the tool call after it, the call in mono (machine text, frame m1-1).
      const m = document.createElement("span");
      m.className = "mono";
      m.textContent = sub.mono;
      d.append(sub.lead, m);
    } else d.textContent = sub.text;
    // The name button is the keyboard path; the sub-line is the same action for
    // a pointer (spec 7: it replaces the old summary-to-peek click). An SSO
    // sign-in line opens the session's Access view instead, where sign-in lives.
    d.onclick = () => { if (sub.view === "access" && openPaneView(s.id, "access")) return; openTerminal(s.id, s.name); };
    grow.appendChild(d);
  }
}

// renderDesktopEntry: what the desktop list renders for one item — a group
// header, or a compact row for a session.
function renderDesktopEntry(it) {
  if (it.header) {
    const h = document.createElement("li");
    // The kit's group label (uppercase, muted) carries the type; only Needs you
    // is amber. "Done · your turn" is as neutral as Running and Idle. .grp keeps
    // the sidebar's own spacing.
    h.className = "grp grp-label" + (it.key === "needs-you" ? " is-needs" : "");
    h.dataset.sid = it.id;
    const n = document.createElement("span");
    n.className = "n";
    n.textContent = String(it.count);
    h.append(it.label + " \u00b7 ", n);
    return h;
  }
  return renderSession(it.s, { compact: true });
}

// deskEntries: the desktop list as reconcile items, a header before each
// group's rows. Keys are "grp:<key>" for headers and the session id for rows.
function deskEntries(sessions) {
  const out = [];
  for (const g of deskGroups(sessions)) {
    out.push({ id: "grp:" + g.key, header: true, key: g.key, label: g.label, count: g.count });
    for (const s of g.sessions) out.push({ id: s.id, s });
  }
  return out;
}

// buildCardComposer: the one-line reply composer a desktop needs-you card
// carries — the ask/turn body and the SSO body build it the same way.
function buildCardComposer(s) {
  return buildReplyComposer(s, {
    replyable: () => s.replyable,
    pending: () => s.need === "ask",
    ariaLabel: "Reply to " + (s.name || s.id),
    submitOnEnter: true,
    afterSend: () => {
      // No forced rebuild here: pendingSends IS already part of rowSig (via
      // rowLocal's `sending`), so refreshSessions()'s own reconcile already
      // rebuilds this card whenever anything it renders actually changed —
      // typically `activity` itself, moving off "waiting" once the send
      // lands. A forced rebuild used to run unconditionally here and
      // clobbered this exact card's `why` text (just set by the send's failure
      // path, or left blank on success) with whatever a
      // freshly-built composer's gate reason says — hiding the one error
      // message an operator most needs to see right after a failed send.
      refreshSessions();
    },
  });
}

// stripOpenButton: the ask strip's "Open terminal": show me the agent, so no
// drawer stays over it, then focus (or open) the session's terminal. One builder
// for the three strip bodies (store, ask card, SSO).
function stripOpenButton(s) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn btn-sm";
  b.textContent = "Open terminal";
  b.onclick = () => { closeDrawerFor(s.id); openTerminal(s.id, s.name); };
  return b;
}

// renderNeedsBody builds the expanded part of a desktop needs-you card
// (cardContent decides which blocks). Sends go through the one gated path —
// sendState here, canReply on the server — and never offer digits (see
// cardContent). It shares its composer with togglePeek's inline reply panel
// via buildReplyComposer — see that function's own comment for what differs
// between the two callers.
//
// opts.strip: the body of the desktop ask strip over a pane (tabs.js
// buildNeedStrip). Same blocks, same gates and the same sendAnswer; only the
// wording of two buttons differs ("Yes", not "1 Yes", and "Open terminal"), and
// the open button also closes a drawer, as the pane header's Terminal tab does.
function renderNeedsBody(s, opts) {
  const inStrip = !!(opts && opts.strip);
  // The strip's one line is plain text (plainSummary); the expanded view still
  // shows the raw message. (The mobile needs-you card is renderNeedsCard.)
  const c = cardContent(s, { plain: inStrip });
  // An expired AWS sign-in outranks turn/ask for which card shows.
  if (c.sso) return renderSSOBody(s, { mobile: false, strip: inStrip });
  // A locked store: the card is the unlock form (store.js).
  if (c.unlock) {
    const sb = renderStoreBody(s, { mobile: false, strip: inStrip });
    if (inStrip) {
      // The store card has no Open of its own; the strip always offers one.
      const sacts = document.createElement("div");
      sacts.className = "nacts";
      sacts.appendChild(stripOpenButton(s));
      sb.appendChild(sacts);
    }
    return sb;
  }
  const body = document.createElement("div");
  body.className = "nbody";
  // The text block doubles as the expand/collapse control — the sidebar's old
  // togglePeek "Show the full message" peek, restored here for the card. A
  // real interactive element (role+tabindex+aria-expanded), not a click
  // handler on a plain div, so it is reachable and announced the same way the
  // row's own summary line (renderSession) already is.
  const expanded = cardExpanded.has(s.id);
  const txt = document.createElement("div");
  txt.className = c.ask ? "peektxt ask" : "peektxt";
  txt.setAttribute("role", "button");
  txt.setAttribute("tabindex", "0");
  txt.setAttribute("aria-expanded", expanded ? "true" : "false");
  const toggleLabel = expanded ? "Hide the full message" : "Show the full message";
  txt.title = toggleLabel;
  txt.setAttribute("aria-label", toggleLabel);
  // An ask in the strip keeps the command on line 1 whether or not it is
  // expanded (F51): expanding wraps it and adds below it, it never swaps the line
  // just read for the agent's message. Any other card shows the full message in
  // place of its one-line summary.
  const askStrip = inStrip && c.ask;
  const bash = askStrip ? bashCommand(s.ask) : null;
  const cached = expanded ? cardPeekCache.get(s.id) : undefined;
  if (expanded && !askStrip) {
    txt.textContent = cached ? cached.text : "Loading…";
    txt.classList.toggle("muted", !!(cached && cached.muted));
  } else if (bash !== null) {
    // "Asks to run" is the label's, so a Bash call is its command alone (frame m1-1); every other tool
    // keeps Tool(args). The expanded strip keeps this line too (F51), and the full command is below it.
    txt.textContent = bash;
  } else {
    txt.textContent = c.text;
  }
  const toggle = (e) => { e.stopPropagation(); toggleCardPeek(s); };
  txt.onclick = toggle;
  txt.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); toggle(e); } };
  body.appendChild(txt);
  syncMaskHint(txt, txt.textContent);
  // The message the agent said before the call, on its own line under the
  // command. A pending tool call often has none ("No message yet.", muted): then
  // there is no line, and nothing shows while it loads.
  if (askStrip && cached && !cached.muted && cached.text) {
    const msg = document.createElement("div");
    msg.className = "askmsg";
    msg.textContent = cached.text;
    body.appendChild(msg);
    syncMaskHint(msg, cached.text);
  }

  // The full command, once expanded — what Yes/No will answer. textContent
  // only; wrapped, monospace (spec §2.5).
  const ans = answerState(s, expanded, cardAskSeen.get(s.id));
  if (expanded && c.ask && s.ask_full) {
    const pre = document.createElement("pre");
    pre.className = "askfull";
    pre.textContent = s.ask_full;
    body.appendChild(pre);
  }

  let composer = null;
  if (c.reply) {
    composer = buildCardComposer(s);
    body.appendChild(composer.form);
    composer.refresh();
  }

  const acts = document.createElement("div");
  acts.className = "nacts";
  // The strip's action row (.nacts) is all the kit's 24px .btn-sm (Yes also the amber .btn-yes), like Open
  // terminal. The reply composer's send (buildReplyComposer) keeps its own height.
  const sm = inStrip ? " btn-sm" : "";
  if (c.enableReplies) {
    const en = buildEnableRepliesButton(s.id, () => {
      // replyable is in the signature, so the card re-rendered with its reply
      // field; land the operator in it rather than on <body>.
      // Desktop has no card: the field is in the ask strip over the pane.
      const li = sessionRowFor(s.id);
      const strip = mobileActive(window.innerWidth) ? null : Array.from(document.querySelectorAll(".needstrip")).find((n) => n.dataset.sid === s.id);
      const fld = (li && li.querySelector(".nbody .fld")) || (strip && strip.querySelector(".fld"));
      if (fld) fld.focus();
    });
    if (inStrip) en.classList.add("btn-sm");
    acts.appendChild(en);
  }
  if (c.reply) {
    // The card's counterpart to the sidebar peek panel's "Allow replies from
    // here" checkbox — restoring the one function that checkbox served (reply
    // permission must stay revocable from the sidebar) now that the card is
    // the surface an operator actually answers from.
    const dis = document.createElement("button");
    dis.type = "button";
    dis.className = "btn" + sm;
    dis.textContent = "Disallow replies";
    dis.title = "Revoke this session's sidebar-reply permission";
    dis.onclick = async () => {
      dis.disabled = true;
      if (!(await postReplyable(s.id, false))) {
        dis.disabled = false;
        showToast("Could not change the reply setting", { error: true });
        return;
      }
      await refreshSessions();
    };
    acts.appendChild(dis);
  }
  if (ans.show) {
    // Both buttons, built first and kept in `pair` — a click on either must
    // disable BOTH synchronously (before the await inside sendAnswer), or the
    // sibling stays clickable for the whole round trip and silently no-ops
    // through the pendingAnswers latch instead of visibly going inert.
    const pair = [["yes", inStrip ? "Yes" : "1 Yes"], ["no", "No"]].map(([choice, label]) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "btn" + sm + (inStrip && choice === "yes" ? " btn-yes" : "");
      b.textContent = label;
      b.disabled = !ans.enabled || pendingAnswers.has(s.id);
      b.classList.toggle("off", b.disabled);
      if (ans.title) b.title = ans.title;
      b.dataset.choice = choice;
      return b;
    });
    for (const b of pair) {
      b.onclick = (e) => {
        e.stopPropagation();
        for (const sib of pair) { sib.disabled = true; sib.classList.add("off"); }
        sendAnswer(s, b.dataset.choice);
      };
      acts.appendChild(b);
    }
  }
  if (inStrip) acts.appendChild(stripOpenButton(s));
  else {
    const open = document.createElement("button");
    open.type = "button";
    open.className = c.ask ? "btn pri" : "btn";
    open.textContent = c.ask ? "Open to answer" : "Open";
    open.onclick = () => openTerminal(s.id, s.name);
    acts.appendChild(open);
  }
  body.appendChild(acts);
  // The one line that says why an ask has no live Yes/No (answerState): replies
  // off (F50), not expanded yet (F51), or a prompt too long to show here.
  const askWhy = ans.note || ans.reason;
  if (askWhy) {
    const why = document.createElement("span");
    why.className = "peek-why";
    why.textContent = askWhy;
    body.appendChild(why);
  }
  if (composer) body.appendChild(composer.why);
  return body;
}

// renderNeedsCard builds the mobile "Needs you" card (M1) for one waiting
// session: message up front, three quick replies, and a Reply…/Open terminal
// pair — instead of the plain `.mrow`/renderSession row Running/Idle still use
// on mobile. Only the needs-you group gets this treatment (refreshSessions
// picks the builder per group below).
//
// The quick-reply buttons send through sendReplyFor(s, digit) — the same one
// gated send path the sheet's doSend uses. They are gated client-side too,
// via the SAME sendState peek.js function the reply sheet and the desktop
// composer read (binding constraint: card quick-replies reuse sendState). A
// card has no /peek fetch of its own, so it cannot know `pending`; we treat
// it conservatively as false and let the server's canReply be the real gate
// (it 409s, and sendReplyFor toasts, any send the client mis-permits) — the
// fail-closed contract ("the transcript decides, not the dot") holds.
function renderNeedsCard(s) {
  const li = document.createElement("li");
  li.className = "card";
  li.dataset.sid = s.id;
  li.title = s.id;

  const hdr = document.createElement("div");
  hdr.className = "hdr";
  const act = sessionDot(s);
  const dot = document.createElement("span");
  dot.className = act.cls;
  dot.setAttribute("role", "img");
  dot.setAttribute("aria-label", act.title);
  dot.title = act.title;
  hdr.appendChild(dot);
  const nm = document.createElement("span");
  nm.className = "grow";
  nm.textContent = s.name || s.id;
  hdr.appendChild(nm);
  // No duration: the session payload carries no waiting-since timestamp (only
  // a peek fetch reports pending, and that is not fetched for a card), so
  // "waiting Nm" would be invented. "waiting" alone stays honest — the same
  // call Task 4's sheet header already made for the identical reason.
  const wt = document.createElement("span");
  wt.className = "wt";
  wt.textContent = s.need === "sso" || s.need === "store_locked" ? needLabel(s) : "waiting";
  hdr.appendChild(wt);
  li.appendChild(hdr);

  // An expired AWS sign-in gets its own card body. It keeps the Reply… row for a
  // replyable session (spec 8.2), but not the 1/2/3 quick replies, which answer
  // a prompt that is not there.
  if (cardContent(s).sso) {
    li.appendChild(renderSSOBody(s, { mobile: true }));
    const swide = document.createElement("div");
    swide.className = "wide";
    if (globalThis.repliesEnabled === true && s.replyable) {
      const sreply = document.createElement("button");
      sreply.type = "button";
      sreply.className = "btn pri";
      sreply.textContent = "Reply…";
      sreply.onclick = () => openReplySheet(s);
      swide.appendChild(sreply);
    }
    const sterm = document.createElement("button");
    sterm.type = "button";
    sterm.className = "btn";
    sterm.textContent = "Open terminal";
    sterm.onclick = () => openTerminal(s.id, s.name);
    swide.appendChild(sterm);
    li.appendChild(swide);
    return li;
  }

  // A locked store: the unlock form, then Open terminal. No reply row: nothing
  // is being asked, and the fix is the passphrase.
  if (cardContent(s).unlock) {
    li.appendChild(renderStoreBody(s, { mobile: true }));
    const uwide = document.createElement("div");
    uwide.className = "wide";
    const uterm = document.createElement("button");
    uterm.type = "button";
    uterm.className = "btn";
    uterm.textContent = "Open terminal";
    uterm.onclick = () => openTerminal(s.id, s.name);
    uwide.appendChild(uterm);
    li.appendChild(uwide);
    return li;
  }

  const msg = document.createElement("div");
  msg.className = "msg";
  // Same source renderSession's summary line reads (s.summary, the server's
  // one-line digest of the last assistant message) — never a second fetch.
  msg.textContent = s.summary || "No summary";
  li.appendChild(msg);
  syncMaskHint(msg, msg.textContent);

  // Replies off (server kill-switch, /api/meta): the card is the message plus
  // Open terminal. No 1/2/3 row and no Reply… are built.
  const repliesOn = globalThis.repliesEnabled === true;
  const qk = document.createElement("div");
  qk.className = "qk";
  // Same gate as the reply sheet and the desktop composer: a digit always
  // passes replyTextError, so sendState is the only client-side check that
  // can close these. pending is otherwise unknowable from the session list
  // (no /peek here) — except need === "ask", which /api/sessions carries for
  // exactly this case (a claude session blocked on a permission prompt, which
  // the server refuses a reply to). The server's canReply stays the
  // authoritative gate either way.
  const cardSt = sendState({ replyable: s.replyable, pending: s.need === "ask", activity: s.activity, need: s.need, reply_ready: s.reply_ready });
  if (repliesOn) {
    for (const [digit, word] of [["1", "yes"], ["2", "always"], ["3", "no"]]) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "btn";
      b.textContent = digit + " " + word;
      b.title = "Send " + digit;
      b.disabled = !cardSt.enabled;
      b.onclick = () => sendReplyFor(s, digit);
      qk.appendChild(b);
    }
    li.appendChild(qk);
  }

  const wide = document.createElement("div");
  wide.className = "wide";
  const term = document.createElement("button");
  term.type = "button";
  term.className = "btn";
  term.textContent = "Open terminal";
  term.onclick = () => openTerminal(s.id, s.name);
  if (repliesOn) {
    const reply = document.createElement("button");
    reply.type = "button";
    reply.className = "btn pri";
    reply.textContent = "Reply…";
    reply.onclick = () => openReplySheet(s);
    wide.append(reply, term);
  } else {
    wide.append(term);
  }
  li.appendChild(wide);

  return li;
}
// --- AWS sign-in expired (M3b): the SSO needs-you card ------------------------
//
// A boxed session whose AWS sign-in lapsed shows this card instead of the
// ask/turn one (cardContent decides). "Sign in" starts a device-code flow on the
// host (the daemon runs `aws sso login`); the code and page URL come back to
// this browser session only. While a flow is open ONE 3 s poll per session reads
// GET .../aws until it ends; the poller is dropped the moment the session leaves
// the list or stops needing a sign-in (pruneSSO, from refreshSessions), so no
// timer outlives its card.
const ssoFlows = new Map();        // id -> login state {state, uri, uri_trusted, code, expires} | {fallback: command}
const ssoStatus = new Map();       // id -> last GET .../aws body (carries the host command)
const ssoStatusAsked = new Set();  // ids whose status was requested (one fetch per card lifetime)
const ssoStatusFailed = new Map(); // id -> failed status fetches, so a failure moves the card's rowSig and a later render retries
const ssoPollers = new Map();      // id -> interval handle, only while a flow is open
const ssoPolling = new Set();      // ids with a poll request in flight
const ssoStarting = new Set();     // ids whose sign-in POST is in flight
const SSO_POLL_MS = 3000;

function ssoPath(id, tail) { return "/api/sessions/" + encodeURIComponent(id) + "/aws" + (tail || ""); }
// ssoTarget: the connection the Sign in button will name for session id, so the
// command, the flow and the POST all talk about the same source ("" legacy).
function ssoTarget(id) {
  return ssoSignInName(lastSessions.find((x) => x.id === id));
}
function ssoCommand(id) {
  const st = ssoStatus.get(id);
  return ssoCommandOf(st && st.sso, ssoTarget(id));
}

// ssoLocalSig: what the SSO card reads beyond the session record, for rowSig, so
// a flow moving on (code arrived, signed in, failed) rebuilds the card and
// nothing else does.
function ssoLocalSig(id) {
  const f = ssoFlows.get(id) || {};
  return [f.state || "", f.code || "", f.uri || "", f.uri_trusted ? 1 : 0, f.expires || "", f.fallback || "",
    ssoStatus.has(id) ? 1 : 0, ssoStatusFailed.get(id) || 0, ssoCommand(id), ssoStarting.has(id) ? 1 : 0].join("~");
}

const SSO_DONE_TOAST = "Signed in. The agent's next AWS call works. Tell it to retry if it stopped.";

// ssoDone: id -> the session's activity at the last look, for each session whose
// AWS sign-in THIS tab finished (F63). The pane keeps SSO_DONE_TOAST as a line
// with a ✕ (tabs.js buildSSODoneLine) after the toast is gone, because the retry
// hint is the part an operator needs a minute later. The broker clears the need
// at sign-in, so the sign-in card is gone before the line matters: this is its own
// state, not the card's. pruneSSODone decides each poll whether it stays.
const ssoDone = new Map();

// ssoDoneHeard: the ids whose kept line has been drawn as a live region already. The strip
// is rebuilt whenever something else on it moves, and a rebuilt live region reads out again:
// the line is announced the first time it appears for a sign-in, and drawn silently after.
// It follows ssoDone: a line that goes away (or a new sign-in) is announced fresh next time.
const ssoDoneHeard = new Set();

// ssoDoneFirstShow: true the first time the line for `id` is drawn since its sign-in
// finished (buildSSODoneLine adds role="status" then and only then).
function ssoDoneFirstShow(id) {
  if (ssoDoneHeard.has(id)) return false;
  ssoDoneHeard.add(id);
  return true;
}

// markSSODone: this tab's sign-in for `id` just finished (next to the toast).
function markSSODone(id) {
  const s = lastSessions.find((x) => x.id === id);
  ssoDone.set(id, s ? s.activity : "");
  ssoDoneHeard.delete(id);
  // The strip is drawn from the poll; do not wait for the next one.
  if (!mobileActive(window.innerWidth)) cells.forEach((c) => c.repaintReply());
}

// ssoDoneShown: whether the pane draws the line for session `s`: kept, and not
// while the record still shows an expired sign-in (that strip is the form).
function ssoDoneShown(s) {
  return !!s && ssoDone.has(s.id) && s.need !== "sso" && connSSOSources(s).length === 0;
}

// pruneSSODone: one look per poll. A line stays while the session has not gone
// from not working to working (ssoDoneKeep); a session that left the list or
// ended drops it. Runs in refreshSessions right after pruneSSO.
function pruneSSODone() {
  const byId = new Map(lastSessions.map((s) => [s.id, s]));
  for (const [id, prev] of Array.from(ssoDone)) {
    const s = byId.get(id);
    if (s && ssoDoneKeep(prev, s.activity, !!s.exited)) ssoDone.set(id, s.activity);
    else { ssoDone.delete(id); ssoDoneHeard.delete(id); }
  }
}

// dismissSSODone: the ✕.
function dismissSSODone(id) {
  ssoDone.delete(id);
  ssoDoneHeard.delete(id);
  cells.forEach((c) => c.repaintReply());
}

// pruneReplyFlash: the line a send left under a reply field (replyFlash) goes with
// its session. Both lines time themselves out, but only when a field reads them,
// so a session that left the list must not keep one.
function pruneReplyFlash() {
  const ids = new Set(lastSessions.map((s) => s.id));
  for (const id of Array.from(replyFlash.keys())) if (!ids.has(id)) replyFlash.delete(id);
}

// ssoAdopt: take a GET .../aws body: cache it and follow the flow it carries.
function ssoAdopt(id, body) {
  ssoStatus.set(id, body || {});
  const login = ssoLoginOf(body && body.sso, ssoTarget(id));
  const cur = ssoFlows.get(id);
  if (login) {
    ssoFlows.set(id, login);
    if (login.state === "waiting") startSSOPoll(id);
  } else if (cur && !cur.fallback) {
    ssoFlows.delete(id); // the server no longer holds this flow
  }
}

// ensureSSOStatus: fetch the host command (and any flow already running, e.g.
// after a reload) when the card renders. A failed fetch (network, non-200, bad
// JSON) stores nothing and clears "asked", so the next render of the card asks
// again; ssoStatusFailed is in the card's rowSig, so that render comes with the
// next list poll: one retry per poll, never a tight loop.
function ensureSSOStatus(id) {
  if (ssoStatus.has(id) || ssoStatusAsked.has(id)) return;
  ssoStatusAsked.add(id);
  fetch(ssoPath(id)).then((res) => (res.status === 200 ? res.json() : null), () => null).catch(() => null).then((body) => {
    const s = lastSessions.find((x) => x.id === id);
    if (!s || s.need !== "sso") return; // the card went away while this was in flight
    if (!body || typeof body !== "object") {
      ssoStatusAsked.delete(id);
      ssoStatusFailed.set(id, (ssoStatusFailed.get(id) || 0) + 1);
      return; // the next poll's rowSig sees the counter move and re-renders
    }
    ssoStatusFailed.delete(id);
    ssoAdopt(id, body);
    refreshSessions();
  });
}

function startSSOPoll(id) {
  if (ssoPollers.has(id)) return;
  ssoPollers.set(id, setInterval(() => ssoPollTick(id, false), SSO_POLL_MS));
}
function stopSSOPoll(id) {
  const h = ssoPollers.get(id);
  if (h !== undefined) clearInterval(h);
  ssoPollers.delete(id);
}

// ssoPollTick: one read of the flow. `last` is pruneSSO's final look after the
// need cleared: a finished sign-in clears the need before the poller sees "ok",
// so this is the only place the success toast (and the pane's kept line,
// markSSODone) can still come from. It never writes the card's maps (the card is gone).
async function ssoPollTick(id, last) {
  if (ssoPolling.has(id)) return;
  ssoPolling.add(id);
  let status = 0;
  let body = null;
  try {
    const res = await fetch(ssoPath(id));
    status = res.status;
    if (status === 200) body = await res.json();
  } catch (_) { /* transient: keep polling */ }
  ssoPolling.delete(id);
  if (status === 404 || status === 410) { stopSSOPoll(id); return; }
  if (!body) return;
  const login = ssoLoginOf(body.sso, ssoTarget(id));
  const state = login ? login.state : "";
  if (last) {
    if (state === "ok") { showToast(SSO_DONE_TOAST); markSSODone(id); }
    return;
  }
  if (!ssoPollers.has(id)) return; // stopped while the request was in flight
  const before = ssoLocalSig(id);
  ssoAdopt(id, body);
  if (state !== "waiting") {
    stopSSOPoll(id);
    if (state === "ok") { showToast(SSO_DONE_TOAST); markSSODone(id); }
  }
  if (ssoLocalSig(id) !== before || state !== "waiting") refreshSessions();
}

// pruneSSO: drop SSO state (and any poller) for a session that left the list or
// no longer needs a sign-in.
function pruneSSO() {
  const byId = new Map(lastSessions.map((s) => [s.id, s]));
  const ids = new Set([...ssoFlows.keys(), ...ssoStatus.keys(), ...ssoStatusAsked, ...ssoStatusFailed.keys(), ...ssoPollers.keys()]);
  for (const id of ids) {
    const s = byId.get(id);
    if (s && s.need === "sso") continue;
    if (ssoPollers.has(id)) {
      stopSSOPoll(id);
      if (s) ssoPollTick(id, true);
    }
    ssoFlows.delete(id);
    ssoStatus.delete(id);
    ssoStatusAsked.delete(id);
    ssoStatusFailed.delete(id);
  }
}

// ssoSignIn: start the device-code flow. 502 means the daemon could not run the
// sign-in here; the body carries the command the human can run on the host.
async function ssoSignIn(s) {
  const id = s.id;
  if (ssoStarting.has(id)) return;
  ssoStarting.add(id);
  let status = 0;
  let body = null;
  try {
    // A session with named connections says which one to sign in (the server
    // resolves its recorded source profile); the legacy request has no body.
    const name = ssoSignInName(s);
    const res = await fetch(ssoPath(id, "/sso-login"), name
      ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: name }) }
      : { method: "POST" });
    status = res.status;
    try { body = await res.json(); } catch (_) { body = null; }
  } catch (_) { status = 0; }
  ssoStarting.delete(id);
  if (status === 200 && body && body.login) {
    ssoFlows.set(id, body.login);
    if (body.login.state === "waiting") startSSOPoll(id);
    else if (body.login.state === "ok") { showToast(SSO_DONE_TOAST); markSSODone(id); }
  } else if (status === 502) {
    ssoFlows.set(id, { fallback: (body && body.command) || ssoCommand(id) });
  } else {
    showToast(status === 0 ? "Sign-in failed: no connection."
      : status === 403 ? "Only a signed-in browser session can start an AWS sign-in."
        : status === 404 || status === 410 ? "That session is gone."
          : "Sign-in failed (" + status + ").", { error: true });
  }
  refreshSessions();
}

// ssoModel: ssoCard's model, fed from the page's own flow/status caches.
function ssoModel(s) {
  const flow = ssoFlows.get(s.id);
  const m = ssoCard(s, { sso: { command: ssoCommand(s.id), login: flow && !flow.fallback ? flow : null } });
  m.fallback = flow && flow.fallback ? flow.fallback : "";
  if (!m.command) m.command = m.fallback;
  return m;
}

function ssoNote(text) {
  const p = document.createElement("p");
  p.className = "ssonote";
  p.textContent = text;
  return p;
}

// renderSSOBody builds the SSO card's body for the desktop card and the mobile
// card (`mobile`: no composer or Open of its own, the mobile card has its own
// Reply…/Open row; `bare`: the Access view's card, which has neither either).
// Every server-provided string (the card text, the code, the URL, the command)
// goes in through textContent; the URL becomes a link only when ssoCard says it
// is trusted, and its href is set as a property.
function renderSSOBody(s, opts) {
  const mobile = !!(opts && opts.mobile);
  const inStrip = !!(opts && opts.strip); // the desktop ask strip (tabs.js buildNeedStrip)
  const bare = !!(opts && opts.bare); // the Access view's card (accesstab.js): no Open, no reply box
  ensureSSOStatus(s.id);
  const m = ssoModel(s);
  const body = document.createElement("div");
  body.className = mobile ? "ssocard mssocard" : "nbody ssocard";

  const txt = document.createElement("div");
  txt.className = "ssotxt";
  txt.textContent = m.text;
  body.appendChild(txt);

  const l = m.login;
  if (l && l.state === "waiting") {
    if (l.code) {
      const code = document.createElement("div");
      code.className = "ssocode";
      code.setAttribute("aria-label", "Sign-in code");
      code.textContent = l.code;
      body.appendChild(code);
    }
    const note = ssoNote("");
    if (l.link) {
      const a = document.createElement("a");
      a.className = "ssolink";
      a.textContent = "Open sign-in page";
      a.href = l.link;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      note.appendChild(a);
    } else if (l.plain) {
      note.appendChild(document.createTextNode("Sign-in page: " + l.plain));
    }
    if (l.expires) {
      const t = hhmm(l.expires);
      if (t) note.appendChild(document.createTextNode((note.childNodes.length ? " · " : "") + "expires " + t));
    }
    if (note.childNodes.length) body.appendChild(note);
  } else if (l && l.state === "failed") {
    body.appendChild(ssoNote("Sign-in did not finish. Try again, or run the host command."));
  } else if (m.fallback) {
    body.appendChild(ssoNote("Sign-in from here is not available right now; run this on the host:"));
  }

  const waiting = !!(l && l.state === "waiting");
  const acts = document.createElement("div");
  acts.className = "nacts";
  const signIn = document.createElement("button");
  signIn.type = "button";
  // In the strip's action row (.nacts) every button is the kit's 24px .btn-sm, Open terminal's size (renderNeedsBody
  // does the same). The reply composer's send and the locked-store form's Unlock keep their own height.
  const sm = inStrip ? " btn-sm" : "";
  signIn.className = "btn pri" + sm;
  signIn.textContent = waiting ? "Waiting for sign-in…" : l && l.state === "failed" ? "Try again" : "Sign in";
  signIn.disabled = waiting || ssoStarting.has(s.id);
  signIn.classList.toggle("off", signIn.disabled);
  signIn.onclick = () => ssoSignIn(s);
  acts.appendChild(signIn);

  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "btn" + sm;
  copy.textContent = "Copy host command";
  copy.disabled = !m.command;
  copy.classList.toggle("off", copy.disabled);
  copy.onclick = () => copyText(m.command, (ok) => {
    showToast(ok ? "Command copied" : "Could not copy. Select the command and copy it.", { error: !ok });
  });
  acts.appendChild(copy);

  if (!mobile && !bare) {
    if (inStrip) acts.appendChild(stripOpenButton(s));
    else {
      const open = document.createElement("button");
      open.type = "button";
      open.className = "btn";
      open.textContent = "Open";
      open.onclick = () => openTerminal(s.id, s.name);
      acts.appendChild(open);
    }
  }
  body.appendChild(acts);

  // Always shown as text as well: the clipboard can be unavailable here (plain
  // HTTP, an iframe), and the command is what the human types on the host.
  if (m.command) {
    const cmd = document.createElement("pre");
    cmd.className = "ssocmd";
    cmd.textContent = m.command;
    body.appendChild(cmd);
  }

  // P8: an SSO card does not take the reply box away from a replyable session.
  if (!mobile && !bare && m.reply) {
    const composer = buildCardComposer(s);
    body.appendChild(composer.form);
    composer.refresh();
    body.appendChild(composer.why);
  }
  return body;
}

// editing guards the inline rename: while true, the background poll must NOT
// re-render the list (that would destroy the input + focus) and a stray click
// must not open a pane.
let editing = false;

// renameActive: is an inline rename open? A terminal asks before it takes the
// keyboard (TerminalTab.grabFocus): a double-click on a name opens the pane on
// its first click, and the pane focuses itself later (onShow, or its socket
// opening), after the second click opened the input. Taking focus then would
// blur-commit the rename and send what the operator types to the session's PTY.
function renameActive() { return editing; }

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
    const save = commit && v && v !== cur;
    // Put the name back first, whatever happens next: the list's keyed reconcile
    // keeps a row whose signature has not changed, so refreshSessions() alone
    // never rebuilds it, and after Escape, an unchanged name or a failed save the
    // row kept the dead input and no name. Removing the focused input fires its
    // blur, which `finished` turns away. A save shows the new name at once and
    // takes it back if the save fails; a nested shell keeps its "shell N" label.
    const was = span.textContent;
    const nested = li.classList.contains("nested");
    if (save && !nested) span.textContent = v;
    if (input.parentNode) input.parentNode.replaceChild(span, input);
    if (save) {
      try {
        const info = await api("/api/sessions/" + encodeURIComponent(s.id), {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: v }),
        });
        // The server caps a name at 64 runes: show the one it stored. A cap that
        // lands on the old name leaves the row's signature unchanged, so no
        // refresh would correct the label.
        const stored = (info && info.name) || v;
        if (!nested) span.textContent = stored;
        retitleTerminal(s.id, stored);
      } catch (e) {
        span.textContent = was;
        const f = toastFailText("rename " + cur, e);
        showToast("Rename failed: " + e.message, { error: true, human: f.text, detail: f.detail });
      }
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
  return lastSessions.reduce((n, s) => n + (needsYou(s) ? 1 : 0), 0);
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
  // inpane: sessions with a tab open in ANY pane, not only the focused one.
  const open = new Set();
  eachTerminal((t) => open.add(t.id));
  for (const li of ul.children) {
    if (!li.classList || !li.classList.contains("srow")) continue;
    li.classList.toggle("sel", !!id && li.dataset.sid === id);
    li.classList.toggle("inpane", open.has(li.dataset.sid));
  }
  renderRail48(); // the focus ring moved, so .ri.on must move with it
}

// rowLocal: the page-local inputs a desktop row renders, for rowSig. Must list
// exactly what renderSession reads beyond the session record itself.
function rowLocal(s) {
  const tab = terminalTabFor(s.id);
  return {
    killing: killing.has(s.id),
    heldHere: !!tab && tab.status() === "connected",
    // Parked on "taken over": the row reads "open in another tab" (F55). Not
    // derivable from the session record, so it must move the signature.
    takenOver: !!tab && tab.status() === "taken over",
    // No context %: the compact row does not draw it (the pane header does), and
    // it moves every few minutes, which would rebuild the row for nothing.
    boxRuntime: boxRuntime,
    // The reply surfaces are built only when /api/meta says replies are on, and
    // that answer lands after the first render, so it must move the signature.
    replies: globalThis.repliesEnabled === true,
    sending: pendingSends.has(s.id),
    expanded: cardExpanded.has(s.id),
    answering: pendingAnswers.has(s.id),
    sso: ssoLocalSig(s.id),
    // The compact row's chip turns "write" when a window opens and back when it
    // lapses, which no session field says by itself.
    elev: sessionElevated(s, Date.now()),
    // A nested shell's "shell N" depends on its siblings, not on its own record.
    shellLabel: isNestedShell(s, lastSessions) ? shellLabel(s, lastSessions) : "",
  };
}

// sys: the client's view of the sessions poll and of the build this tab runs, read by the title and
// favicon (titleState) and by the sidebar banner (sysBannerModel, drawn by sysbanner.js).
//   lastOkAt  ms of the last OK /api/sessions poll (null before the first)
//   failSince ms of the first failure of the current run (null while polls succeed)
//   signedOut true once any api() call answered 401; the next OK session poll clears it
//   hiddenSince ms when the tab went hidden (null while visible)
//   paused    true when the tab became visible after SYS_PAUSE_MS hidden, until the next OK poll
//   bootVersion the first meta.version this tab saw; version the latest; dismissedVersion the one the
//             user closed the "updated" banner on (it comes back for a different version)
// A 401 is signed out at once; a network failure counts as offline only after SYS_OFFLINE_MS without an
// OK poll (nmOffline), so one blip never flickers the title or the banner.
const sys = { lastOkAt: null, failSince: null, signedOut: false, hiddenSince: null, paused: false,
  bootVersion: null, version: null, dismissedVersion: "" };
function sysPollOk() {
  const wasSignedOut = sys.signedOut;
  sys.lastOkAt = Date.now(); sys.failSince = null; sys.signedOut = false; sys.paused = false;
  // Panes parked by the signed-out state reconnect now (Plan 7 Task 8): the flag is the one thing that stopped them.
  if (wasSignedOut && typeof eachTerminal === "function") eachTerminal((t) => { if (t.resumeAfterSignIn) t.resumeAfterSignIn(); });
}
function sysPollFailed(e) {
  const now = Date.now();
  if (sys.failSince == null) sys.failSince = now;
  if (e && e.status === 401) sys.signedOut = true;
}
// A 401 on any call. api() can run before this file reaches `sys` (a const), so a not-yet-built record is a no-op.
function sysNoteSignedOut() {
  try {
    if (sys.signedOut) return;
    sys.signedOut = true;
    if (typeof sysBannerSync === "function") sysBannerSync();
    signalAttention();
  } catch (_) { /* before the record exists */ }
}
// sysSeeVersion: meta.version from initEnv and from the 30 s tick. The first one seen is the build this tab
// runs; a different one later is an update (the banner), never an automatic reload.
function sysSeeVersion(v) {
  if (!v || typeof v !== "string") return;
  if (sys.bootVersion == null) sys.bootVersion = v;
  sys.version = v;
  if (typeof sysBannerSync === "function") sysBannerSync();
}
// A tab hidden SYS_PAUSE_MS or longer shows "Paused while this tab was hidden" on return until a poll lands
// (the browser throttles background timers, so what the list shows is old). The poll is started at once.
document.addEventListener("visibilitychange", () => {
  const step = sysVisibilityStep(sys, document.hidden, Date.now()); // the 30 s rule is notifymodel.js's (R20)
  sys.hiddenSince = step.hiddenSince;
  sys.paused = step.paused;
  if (!step.poll) return;
  if (typeof sysBannerSync === "function") sysBannerSync();
  refreshSessions();
});

// signalAttention pushes the blocked/done state to the tab title and favicon: the part of the
// signal you see without looking at the page (titleState, notifymodel.js). Runs on every
// session poll, successful or not (a failed one moves sys), AND from initEnv's title refresh
// (which can land before the first poll resolves), so it touches title/favicon only.
// Notifications are gated on an actual session list and live in notifyTransitions, called
// separately by refreshSessions. The favicon href (a data: URI, because static icons sit
// behind auth) is swapped only when the icon state changes, so a steady state costs no reload.
let faviconIcon = null;
function signalAttention() {
  const st = titleState(lastSessions, sys, window.__envLabel || "", Date.now());
  if (document.title !== st.title) document.title = st.title;
  if (st.icon !== faviconIcon) {
    const link = $("favicon");
    if (link) link.href = faviconSvg(st.icon);
    faviconIcon = st.icon;
  }
  if (typeof sysBannerSync === "function") sysBannerSync();
}

// notifyTransitions turns one successful /api/sessions poll into OS notifications (#177): the pure
// rule table (notifyStep, notifymodel.js) says what to announce, this part only reads the world
// (prefs, focus, permission, the ids this client ended) and builds the notifications. State
// advances on every call, so a switch turned on later never replays old events. Quiet when the
// master switch is off, the tab is focused, or the browser has not granted permission.
// Accepted limit: Chrome throttles a tab hidden >5 min to ~1 timer/min, so this can lag up to
// ~60 s; no push channel is built for it.
//
// Called ONLY from refreshSessions, after a successful /api/sessions fetch, never from
// signalAttention/initEnv's title refresh and never on a fetch error path (a failed fetch must
// not announce "Ended"). `lastSessions` starts as `[]` before the first real fetch; the
// `sessionsLoaded` gate keeps that placeholder from seeding the state, and the first real poll
// only seeds (prev null).
let notifyPrev = null;
let sessionsLoaded = false;
// endedByMe: session ids this client ended or restarted (id -> ms), so its own End never
// announces "Ended". Kept 10 min per id (endedLive prunes).
const endedByMe = new Map();
// restartedByMe: ids whose restart succeeded; their endedByMe mark ends once a poll sees them live.
const restartedByMe = new Set();
function notifyOpen(note) {
  try { window.focus(); } catch (_) {}
  if (note.sid) {
    // The title of a cause note can be "CuliBox": the tab is named after the session.
    const sess = (lastSessions || []).find((s) => s.id === note.sid);
    openTerminal(note.sid, (sess && sess.name) || note.sid);
  } else if (document.documentElement.classList.contains("sidebar-hidden")) {
    toggleSidebar(); // a batch has no one session: show the list
  }
}
function notifyTransitions() {
  if (!sessionsLoaded || !Array.isArray(lastSessions)) return;
  const now = Date.now();
  const prefs = Object.assign(readPrefs(), { master: notifyWanted() });
  const st = notifyStep(notifyPrev, lastSessions, {
    now, focused: document.hasFocus(), prefs, endedByMe: endedLive(endedByMe, now),
  });
  notifyPrev = st.next;
  endedClearRestarted(endedByMe, restartedByMe, lastSessions);
  if (!st.notes.length) return;
  if (typeof Notification === "undefined" || Notification.permission !== "granted") return;
  deliverNotes(st.notes, Notification, notifyOpen);
}

// notifyPerm: what the browser says about notifications ("unsupported" when it has no Notification at all).
function notifyPerm() {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

// notifyMono appends `text` to `parent` as text nodes, with the parts named in `mono` (in the order they occur in
// the text) as machine text. Text only, never markup: the page's own origin is in some of these strings.
function notifyMono(parent, text, mono) {
  let rest = text;
  for (const m of mono || []) {
    const i = rest.indexOf(m);
    if (i < 0) continue;
    if (i > 0) parent.appendChild(document.createTextNode(rest.slice(0, i)));
    const span = document.createElement("span");
    span.className = "mono";
    span.textContent = m;
    parent.appendChild(span);
    rest = rest.slice(i + m.length);
  }
  if (rest) parent.appendChild(document.createTextNode(rest));
}

// paintNotifyWhy fills the Alerts row's reason (#notify-why) from notifyRowCopy (attention.js): one line under the
// switch, or, for a state with a way out, a paragraph, the steps (under a heading when the copy has one) and, when
// the browser can be asked again, Check again (`onAgain`). Frame M8.1.
function paintNotifyWhy(why, copy, onAgain, link) {
  why.textContent = "";
  if (!copy.para) {
    why.classList.remove("notify-block");
    why.textContent = copy.helper;
    return;
  }
  why.classList.add("notify-block");
  const el = (tag, cls) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    return n;
  };
  const para = el("p", "t-sm muted");
  notifyMono(para, copy.para, copy.paraMono);
  why.appendChild(para);
  const list = el("ul", "notify-steps t-sm");
  for (const step of copy.steps) {
    const li = el("li", step.lead ? "" : "muted");   // a list with no lead words is muted whole, bullets too (§E3 a)
    if (step.lead) {
      const lead = el("span", "w-500");
      lead.textContent = step.lead;
      li.appendChild(lead);
      li.appendChild(document.createTextNode(" "));
    }
    const body = el("span", "muted");
    // `link` (desktop only): the words of the step that name HTTPS are the remote-access guide's link; the step's text is unchanged.
    const at = link ? step.text.indexOf(link.phrase) : -1;
    if (at >= 0) {
      notifyMono(body, step.text.slice(0, at), []);
      const a = el("a", "link");
      a.textContent = link.phrase;
      a.href = link.href;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      body.appendChild(a);
      notifyMono(body, step.text.slice(at + link.phrase.length), step.mono);   // the machine text of this step follows the phrase
    } else {
      notifyMono(body, step.text, step.mono);
    }
    li.appendChild(body);
    list.appendChild(li);
  }
  if (copy.heading) {
    const how = el("div", "notify-how");
    const h = el("span", "t-sm w-600");
    h.textContent = copy.heading;
    how.appendChild(h);
    how.appendChild(list);
    why.appendChild(how);
  } else {
    why.appendChild(list);
  }
  if (copy.checkAgain) {
    const wrap = el("div");
    const b = el("button", "btn btn-sm");
    b.type = "button";
    b.textContent = "Check again";
    b.onclick = onAgain;
    wrap.appendChild(b);
    why.appendChild(wrap);
  }
}

// notifyPrefRow: one switch of the Alerts block (a kit .check with the title and its help under it) for the pref `name`,
// stored through writePref (notifymodel.js) as soon as it is clicked. Text nodes only.
function notifyPrefRow(name, prefs) {
  const def = NOTIFY_PREF_ROWS[name];
  const lab = document.createElement("label");
  lab.className = "check notify-pref";
  const box = document.createElement("input");
  box.type = "checkbox";
  box.className = "check-box";
  box.dataset.pref = name;
  box.checked = !!prefs[name];
  box.onchange = () => {
    writePref(name, box.checked);
    // The switch click is itself a user gesture: it may create or resume the audio context now, so the first blip is not lost.
    if (name === "chime" && typeof chimeGesture === "function") { if (box.checked) chimeGesture(); else chimeSync(false); }
  };
  const txt = document.createElement("span");
  txt.className = "opt-card-title";
  const t = document.createElement("span");
  t.className = "t-md";
  t.textContent = def.title;
  const h = document.createElement("span");
  h.className = "t-sm muted";
  h.textContent = def.help;
  txt.appendChild(t);
  txt.appendChild(h);
  lab.appendChild(box);
  lab.appendChild(txt);
  return lab;
}

// notifyAlertsSection builds, once, the desktop's Alerts section (frame M8.1, DD A2/B6): a title, the always-on note under it, and a card
// of rows on a label column. The master switch and its reason (#notify-toggle's label, #notify-why) move into the Notifications row
// (ids kept, so initNotifyToggle still finds them); what is left of the size card's Alerts row is hidden by desktop.css. Desktop only:
// the phone hides that row and never builds this.
function notifyAlertsSection(row, cb, why) {
  let sec = document.getElementById("alerts-section");
  if (sec) return sec;
  const el = (tag, cls, text, id) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (id) n.id = id;
    if (text) n.textContent = text;
    return n;
  };
  sec = el("section", "alerts-section", "", "alerts-section");
  const head = el("div", "alerts-head");
  head.appendChild(el("h2", "section-title", "Alerts"));
  head.appendChild(el("p", "notify-note t-sm muted", NOTIFY_ALWAYS_ON));
  sec.appendChild(head);
  const card = el("div", "card alerts-card");
  const rowN = el("div", "alerts-row", "", "alerts-row-n");
  rowN.appendChild(el("span", "alerts-lbl t-md w-500", "Notifications"));
  const col = el("div", "alerts-col", "", "alerts-col");
  const holder = cb.parentNode;
  col.appendChild(holder && holder.classList && holder.classList.contains("notify-opt") ? holder : cb);
  col.appendChild(why);
  col.appendChild(el("div", "notify-more", "", "notify-more"));
  rowN.appendChild(col);
  rowN.appendChild(el("div", "alerts-act", "", "alerts-act"));
  card.appendChild(rowN);
  card.appendChild(el("div", "alerts-rest", "", "notify-rows"));
  sec.appendChild(card);
  const fsCard = row.parentNode;
  if (fsCard && fsCard.parentNode) fsCard.after(sec);
  else if (fsCard) fsCard.appendChild(sec);
  return sec;
}

// paintNotifyMore draws the rest of the Alerts section (desktop only), repainted on every initNotifyToggle: with the master on, the
// Blocked / Done / Ended rows (the Firefox note under Blocked) and the Send test result in the Notifications row, Send test in its
// third column, then the Message text row; the chime row in every state ("Sound"; on an http page (a) it is the row "Meanwhile, in this
// tab", after the how-to-enable list, the only sound a background tab can make there). `state` is notifyAvailability's, `on` is the master.
// (The remote-access link is built with the how-to step itself, by paintNotifyWhy.)
function paintNotifyMore(row, state, on) {
  const cb = $("notify-toggle"), why = $("notify-why");
  const sec = notifyAlertsSection(row, cb, why);
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text) n.textContent = text;
    return n;
  };
  const more = document.getElementById("notify-more"), act = document.getElementById("alerts-act"), rest = document.getElementById("notify-rows");
  more.textContent = "";
  act.textContent = "";
  rest.textContent = "";
  const prefs = readPrefs();
  const line = (label, labelCls, ...children) => {
    const r = el("div", "alerts-row");
    r.appendChild(el("span", labelCls, label));
    const c = el("div", "alerts-col");
    for (const ch of children) c.appendChild(ch);
    r.appendChild(c);
    r.appendChild(el("div", "alerts-act"));
    rest.appendChild(r);
  };
  if (state === "ready" && on) {
    const group = el("div", "notify-classes");
    group.setAttribute("role", "group");
    group.setAttribute("aria-label", "Notify me about");
    group.appendChild(notifyPrefRow("block", prefs));
    group.appendChild(el("p", "notify-ff t-sm muted", NOTIFY_FIREFOX));
    group.appendChild(notifyPrefRow("done", prefs));
    group.appendChild(notifyPrefRow("ended", prefs));
    more.appendChild(group);
    const status = el("p", "notify-status t-sm muted");
    status.setAttribute("role", "status");
    more.appendChild(status);
    const btn = el("button", "btn btn-sm", "Send test");
    btn.type = "button";
    btn.onclick = () => {
      // The same builder as the real ones (notifymodel.js deliverNotes); a click on this one only closes it.
      const note = { title: "CuliBox", body: "Test notification", tag: "ab:test", options: { tag: "ab:test" } };
      status.textContent = deliverNotes([note], typeof Notification === "undefined" ? null : Notification, null) ? NOTIFY_TEST_SENT : NOTIFY_TEST_NOTHING;
    };
    act.appendChild(btn);
    line("Message text", "alerts-lbl t-md w-500", notifyPrefRow("message", prefs));
  }
  if (state === "insecure") line("Meanwhile, in this tab", "alerts-lbl t-sm w-600", notifyPrefRow("chime", prefs));
  else line("Sound", "alerts-lbl t-md w-500", notifyPrefRow("chime", prefs));
  return sec;
}

// initNotifyToggle wires Settings → Appearance → Desktop notifications: the switch, and the reason under it that
// says why it is off and how to turn it on (F39/F47; wording from frame M8.1, attention.js notifyRowCopy). It
// re-reads the browser's permission on every call: Settings opening, Check again, and each answer to the prompt.
// `refused`: the prompt just closed without a permission, so its line replaces the helper this once.
function initNotifyToggle(refused) {
  const cb = $("notify-toggle"), why = $("notify-why");
  if (!cb || !why) return;
  const perm = notifyPerm();
  const avail = notifyAvailability(window.isSecureContext, perm);
  cb.disabled = !avail.enabled;
  cb.checked = avail.enabled && notifyChecked(notifyWanted(), perm);
  const copy = notifyRowCopy(avail.state, cb.checked, window.location.origin, perm);
  if (refused && avail.state === "ready") copy.helper = "Permission was not granted, so notifications stay off.";
  paintNotifyWhy(why, copy, () => {
    initNotifyToggle();
    // The button that took the press was redrawn or is gone: keep the keyboard on its successor, or on the switch it unlocked.
    const next = why.querySelector(".btn") || (cb.disabled ? null : cb);
    if (next) next.focus();
  }, drawerDesktop() ? { phrase: REMOTE_ACCESS_PHRASE, href: REMOTE_ACCESS_DOC } : null);
  if (drawerDesktop()) paintNotifyMore($("fsrow-alerts"), avail.state, cb.checked);
  cb.onchange = async () => {
    if (!cb.checked) { setNotifyWanted(false); initNotifyToggle(); return; }
    let p = "denied";
    try { p = await Notification.requestPermission(); } catch (_) {}
    setNotifyWanted(p === "granted");
    initNotifyToggle(p !== "granted");
  };
}

// Returns the live session list, or null if the request failed: the boot-time
// restore must be able to tell "the server says there are none" (a valid answer:
// restore nothing) from "the server could not be asked" (retry, restore nothing
// yet). An empty array is the former.
//
// Plan 7 final review I2: the fetch has a deadline (SESSIONS_DEADLINE_MS, under the 12 s offline rule) so a
// half-open connection becomes a recorded failed poll instead of hanging forever, and only one is in flight.
// refreshSessions() keeps its contract (the promise resolves after a list fetched NOW): with none pending it
// starts one; with one pending (it may predate the caller's change) it returns ONE shared follow-up that runs
// when the pending one settles. The 3 s tick (sessionsTick) just skips while one is pending.
const SESSIONS_DEADLINE_MS = 10000;
let sessionsInflight = null;
let sessionsFollow = null;
function sessionsStart() {
  const p = refreshSessionsRun().finally(() => { if (sessionsInflight === p) sessionsInflight = null; });
  sessionsInflight = p;
  return p;
}
function refreshSessions() {
  if (!sessionsInflight) return sessionsStart();
  if (!sessionsFollow) {
    sessionsFollow = sessionsInflight.catch(() => {}).then(() => { sessionsFollow = null; return sessionsStart(); });
  }
  return sessionsFollow;
}
function sessionsTick() { if (!sessionsInflight) refreshSessions(); }
async function refreshSessionsRun() {
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
  //
  // A held list can briefly go on showing an ask card whose command already
  // changed underneath it (the DOM rebuild that would collapse it — see
  // staleExpanded above — is exactly what's being skipped); a stray Yes/No
  // click on it is still safe, because sendAnswer hashes the STALE ask_full it
  // has on hand and the server's own hash/memo gate (ask_sha256 mismatch ->
  // 409) refuses it rather than answering the wrong prompt.
  // A focused store passphrase field holds the rebuild too (store.js): a poll
  // must not tear down the form mid-keystroke.
  const holdList = () => editing || replyFocused || storeUnlockFocused();
  let sessions;
  try { sessions = await api("/api/sessions", { signal: deadlineSignal(SESSIONS_DEADLINE_MS) }); }
  catch (e) {
    // R27: on the desktop, once a list has loaded, a failed poll keeps the last-known rows (the banner says "the list
    // below is from then"; sess-stale marks them). Before the first list, and always on the phone, the "Error: ..." line.
    const keepRows = sessionsLoaded && !mobileActive(window.innerWidth);
    if (!keepRows && !holdList()) ul.replaceChildren(emptyLi("Error: " + e.message));
    sysPollFailed(e);
    signalAttention();
    return null;
  }
  // A null/odd body is not a list: keep the last real one and announce nothing (an empty list would end everything).
  // It is a failed poll for the offline escalation (R18), not a sign-out: a 200 with a frozen list must not read as live.
  if (!Array.isArray(sessions)) { sysPollFailed(null); signalAttention(); return null; }
  sysPollOk();
  lastSessions = sessions;
  sessionsLoaded = true;
  noteReturns(lastSessions);
  refreshEmptyState();
  // The Files drawer reads the list (box tier, workspace): at a boot restore
  // it opened before the list was here.
  if (drawerState.open === "files" && typeof drawerPaint === "function") drawerPaint();
  // The header's chip draws the session's dot and name from the same list.
  if (drawerState.open) paintDrawerHead();
  // A pinned session that has ended is unpinned, and the dock and its tree follow focus again (dockFollow).
  drawerPinCheck();
  // The focused session's account can only be known once the list is here.
  syncRateLimitsAccount();
  // A card's expanded state (and its cached full text) belongs to it needing
  // you, not to the session forever — drop both once the session is no longer
  // waiting, so reopening it later (a fresh "needs you") starts collapsed. An
  // ask whose full command changed also collapses: the new prompt has not been
  // read, so its Yes/No must not stay one click away.
  if (cardExpanded.size) {
    const byId = new Map(lastSessions.map((s) => [s.id, s]));
    for (const id of Array.from(cardExpanded)) {
      if (staleExpanded(byId.get(id), cardAskSeen.get(id))) {
        cardExpanded.delete(id); cardPeekCache.delete(id); cardAskSeen.delete(id);
      }
    }
  }
  pruneSSO();
  pruneSSODone();
  pruneReplyFlash();
  signalAttention();
  notifyTransitions();
  if (sessionsLoaded && Array.isArray(lastSessions)) chimeTransitions();
  attnChipSync();
  // Push each session's activity onto its open terminal tab (a different axis
  // from the tab's connection status). setActivity is a no-op when unchanged, so
  // this only re-renders a bar whose dot actually moved.
  // A session that needs you for a non-activity reason (an expired AWS sign-in)
  // reads "waiting" on its tab too, so the tab dot, the chip strip and the pane's
  // amber frame (chipmodel.js, tabs.js) follow needsYou without each knowing why.
  // The tab also carries tabNeedWhy so its dot says "AWS sign-in needed", not
  // "waiting for input", for an agent that is still working.
  const actById = new Map(lastSessions.map((s) => [s.id, needsYou(s) ? "waiting" : s.activity]));
  const whyById = new Map(lastSessions.map((s) => [s.id, tabNeedWhy(s)]));
  // A tab whose session has left the list (killed/pruned) would otherwise keep
  // its last dot — a stale green "working" on a dead agent. Fall back to "done".
  eachTerminal((t) => { t.setActivity(actById.has(t.id) ? actById.get(t.id) : "done", whyById.get(t.id) || ""); });
  // The tab strip keeps its AWS chip until told to rebuild: repaint it when a
  // grant, a drop or a lapsed window changes what it draws.
  const sessById = new Map(lastSessions.map((s) => [s.id, s]));
  eachTerminal((t) => { if (typeof t.setAwsSig === "function") t.setAwsSig(awsSig(sessById.get(t.id))); });
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
  // An open Activity / Access view repaints from the same list (Cell.updateView).
  cells.forEach((c) => c.updateView());
  // Same treatment for the steer rail's composer, and for the same reason: it
  // re-gates, it never rebuilds, so a half-typed reply keeps its caret.
  if (typeof repaintSteerRail === "function") repaintSteerRail();
  // And for an open mobile reply sheet: reply_ready turns true a few seconds
  // after a turn ends, and the sheet must not need reopening to notice.
  if (repaintReplySheet) repaintReplySheet();
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
  paintSideElevated();
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
  // Desktop reconciles by key (reconcile.js) so an unchanged row keeps its node
  // — and any focus inside it. Mobile keeps its grouped rebuild. holdList above
  // still guards both: a focused input must not even be re-rendered.
  let rowById;
  if (mobileActive(window.innerWidth)) {
    // (s) => renderSession(s), not a bare renderSession: map passes the index
    // as a second argument, and renderSession takes options there.
    const rows = lastSessions.map((s) => renderSession(s));
    rowById = new Map(lastSessions.map((s, i) => [s.id, rows[i]]));
    const nodes = [];
    for (const g of sessionGroups(lastSessions, (s) => (needsYou(s) ? "waiting" : s.activity))) {
      const h = document.createElement("li");
      h.className = g.key === "needs-you" ? "mhd needs" : "mhd";
      h.innerHTML = '<span>' + g.label + '</span><span class="count">' + g.count + '</span>';
      nodes.push(h);
      // needs-you (M1) gets the interactive card, built fresh from `s` — NOT
      // the renderSession row rowById holds for every other group.
      for (const s of g.sessions) nodes.push(g.key === "needs-you" ? renderNeedsCard(s) : rowById.get(s.id));
    }
    ul.replaceChildren(...nodes);
  } else {
    rowById = reconcileRows(ul, deskEntries(lastSessions), renderDesktopEntry, (it) => (it.header
      ? JSON.stringify(["grp", it.label, it.count])
      : rowSig(it.s, rowLocal(it.s))));
  }
  // The answer block is a sibling, so it can only be inserted once its row is
  // in the document. On mobile a needs-you session's row is built but never
  // appended (the card takes its place), so require isConnected, and clear the
  // id instead of leaving it stuck driving a /peek fetch every poll. On
  // desktop a waiting session is now rendered as the needs-you card, which is
  // already expanded in place — reopening togglePeek's own answer block under
  // it would be a second, redundant composer.
  const reopen = lastSessions.find((s) => s.id === openPeekId);
  if (reopen) {
    const li = rowById.get(reopen.id);
    // A desktop compact row has no peek: its sub-line opens the session.
    const isCard = !!(li && (li.classList.contains("dcard") || li.classList.contains("drow")));
    if (li && li.isConnected && !isCard && !peekRowFor(li)) togglePeek(li, reopen);
    else if (!li || !li.isConnected || isCard) openPeekId = null;
  }
  syncSelectedRow();
  return lastSessions;
}

// --- spawn / kill ------------------------------------------------------------
let profiles = [];
// pfSchema: the profile schema this page was loaded with (see schemaPin). The server appends it to
// profileform.js, so it comes from the same response as the form's code; /api/meta does not pin.
const pfSchema = schemaPin();
pfSchema.pin(globalThis.PROFILE_SCHEMA_BAKED); // a global the server defines (no value when profileform.js was not served by it)
// profilesLoaded distinguishes "the server has no profiles" from "we could not
// ask". Without it, one transient failure at boot leaves the primary spawn
// button disabled under a caption that blames the config, and in dev, `air`
// restarts the daemon under an open tab routinely. Retries once, like initEnv.
let profilesLoaded = false;
// Resolves true once profiles[] reflects the server, false when it could not be
// fetched (profiles[] is then left as it was). The spawn dialog and saveProfile
// both need to tell those apart.
async function loadProfiles(retry = true) {
  try {
    // A stalled response must not hang openSpawnDialog, which awaits this: a
    // timeout is just a failed refresh.
    profiles = (await api("/api/profiles", { signal: deadlineSignal(5000) })) || [];
    profilesLoaded = true;
    notHomeToasts();
    // replaceChildren resets a <select> to its first option, so a refetch while
    // the user has a profile picked would silently switch it.
    const prev = $("agent").value;
    $("agent").replaceChildren(...profiles.map((p) => {
      const o = document.createElement("option");
      o.value = p.name; o.textContent = p.name;
      return o;
    }));
    if (profiles.some((p) => p.name === prev)) $("agent").value = prev;
    renderProfileSeg();
    renderOptions();
    renderProfileList();
    // The editor's list follows the refetch (a return can land while it is open).
    if (selectedProfile) renderPfNotHome(selectedProfile);
    refreshEmptyState();
    return true;
  } catch (_) {
    if (retry) setTimeout(() => loadProfiles(false), 2000);
    return false;
  }
}
function currentProfile() {
  return profiles.find((p) => p.name === $("agent").value);
}
// renderProfileSeg keeps the segmented Profile control (D5) in sync with the
// canonical <select id="agent">: every other reader (currentProfile, the
// spawn request's own req.agent, syncRuntimeField/applyProfileCwd) still goes
// through #agent unchanged, so the seg only ever WRITES agent.value and
// dispatches 'change' — it never becomes a second source of truth. Above
// profileFitsAsSeg's threshold (tier.js) the seg would wrap or overflow the
// 580px dialog, so the fallback is the plain <select>, matching the profile
// editor's own <=4 seg threshold (renderPfOptions) — a product-owner decision,
// not a graceful-degradation afterthought.
function renderProfileSeg() {
  const seg = $("agent-seg"), sel = $("agent");
  if (!seg || !sel) return;
  seg.hidden = !profileFitsAsSeg(profiles.length);
  sel.hidden = !seg.hidden;
  if (!seg.hidden) {
    buildSeg(seg, profiles.map((p) => ({ value: p.name, label: p.name })), sel.value, (v) => {
      sel.value = v;
      sel.dispatchEvent(new Event("change"));
    });
  }
  renderSpawnPlist(); // the desktop dialog's profile list (spawnpanel.js); a no-op on mobile
}
// D5 routes the Effort enum into its own slot beside Containment; every other
// profile option (Model, Bypass permissions) stays in the Advanced group
// below. Matched by key, not position, so a profile that reorders its options
// still lands Effort in the right slot — "effortLevel" covers the same key
// bundle/settings.go's allowlist already reads under that spelling.
function isEffortOptionKey(key) {
  return key === "effort" || key === "effortLevel";
}
// renderOptions builds a toggle per option the selected profile exposes:
// checkbox for bool, a .seg for a <=5-choice enum (mirrors renderPfOptions,
// which stops at <=4 — the spawn dialog's own enums, notably Effort's five
// choices, are one wider on purpose; seeing the alternatives beats saving the
// space a select would), a plain select for anything wider. Pre-set to the
// profile's default. Effort's row goes into #effort-slot (D5's row beside
// Containment); every other option's row goes into #options (the Advanced
// group) — getOptions() below reads both by [data-key], not by container, so
// nothing is lost by living in a different slot.
function renderOptions() {
  const p = currentProfile();
  const opts = (p && p.options) || [];
  const effortRows = [], advRows = [];
  const deskModelSlot = drawerDesktop();
  for (const o of opts) {
    const row = document.createElement("div");
    row.className = "opt";
    const lbl = document.createElement("label");
    lbl.textContent = o.label || o.key;
    let ctrl;
    // Desktop: Model sits beside Effort ("Model · effort", spec 5.6) as a select,
    // which is as narrow as the model name rather than a seg of long names.
    const deskModel = deskModelSlot && o.key === "model" && o.type !== "bool";
    if (o.type === "bool") {
      ctrl = document.createElement("input");
      ctrl.type = "checkbox";
      ctrl.checked = o.value === true;
      lbl.htmlFor = "opt-" + o.key;
      ctrl.onchange = () => syncSpawnExtras(false); // the bypass warning reads it
    } else {
      const choices = o.choices || [];
      if (!deskModel && choices.length > 0 && choices.length <= 5) {
        ctrl = document.createElement("div");
        ctrl.className = "seg";
        buildSeg(ctrl, choices.map((c) => ({ value: c, label: c })), o.value || "");
        // A seg needs the row's full width, not half of it beside a label —
        // see #options .opt.opt-seg in app.css.
        row.className = "opt opt-seg";
      } else {
        ctrl = document.createElement("select");
        // A profile without a default for this enum (#35) starts on the agent's
        // own default: blank is sent, and the launcher adds no flag for it.
        if (!o.value) {
          const none = document.createElement("option");
          none.value = ""; none.textContent = "(agent default)";
          ctrl.appendChild(none);
        }
        for (const c of choices) {
          const co = document.createElement("option");
          co.value = c; co.textContent = c;
          if (c === o.value) co.selected = true;
          ctrl.appendChild(co);
        }
        lbl.htmlFor = "opt-" + o.key;
      }
    }
    ctrl.id = "opt-" + o.key;
    ctrl.dataset.key = o.key;
    ctrl.dataset.type = o.type;
    row.append(lbl, ctrl);
    if (deskModel) effortRows.unshift(row);
    else (isEffortOptionKey(o.key) ? effortRows : advRows).push(row);
  }
  const effortSlot = $("effort-slot"), effortRow = $("effort-row");
  if (effortSlot) effortSlot.replaceChildren(...effortRows);
  // A profile without an Effort option (e.g. bash) leaves an empty grid cell
  // beside Containment otherwise — hide the row rather than show a gap. Same
  // reasoning for the Advanced group when a profile has no other options.
  if (effortRow) effortRow.style.display = effortRows.length ? "" : "none";
  $("options").replaceChildren(...advRows);
  const advGroup = $("spawn-adv");
  if (advGroup) advGroup.style.display = advRows.length ? "" : "none";
}
// getOptions reads every option's control by [data-key] across the WHOLE
// form, not just #options — Effort now lives in #effort-slot instead, and a
// control this stops reading from is a silently-dropped spawn field.
function getOptions() {
  const out = {};
  for (const ctrl of $("spawn").querySelectorAll("[data-key]")) {
    if (ctrl.dataset.type === "bool") out[ctrl.dataset.key] = ctrl.checked;
    else if (ctrl.classList.contains("seg")) out[ctrl.dataset.key] = segValue(ctrl);
    else out[ctrl.dataset.key] = ctrl.value;
  }
  return out;
}
// Switching profile re-derives everything that profile owns: its options AND the
// tier it runs under. Leaving the tier behind would show the previous profile's
// containment while naming this one.
$("agent").addEventListener("change", () => {
  renderOptions();
  syncRuntimeField();
  syncSpawnAccount();
  applyProfileCwd();
  syncSpawnExtras(true);
  syncSpawnPreview();
});

// applyProfileCwd overwrites the workspace picker with the selected profile's
// stored default_cwd. Unlike the old <select>-backed version, there is no
// tier-aware matching to do here any more: the field is free text, so the
// profile's path is simply the value.
// On the desktop the line under the field then says "From the profile" (the source).
// A profile without one keeps what the USER put in the field (typed, picked, a
// Duplicate's). What the dialog or the previous profile filled in is that profile's
// leftover, so the choice made at open is made again for this profile (spawnCwdChoice:
// its own last directory, else the tier's default): a host profile otherwise kept a
// box-owned copy's folder and the server refused the spawn.
function applyProfileCwd() {
  const p = currentProfile();
  if (!p) return;
  if (!p.default_cwd) {
    const again = spawnCwdReselect({
      source: spawnWsSource, prefill: spawnWsPrefill, cwd: getCwd(), text: $("spawn-ws-input").value,
      profile: p, last: loadLastSpawn(), workspaces: workspacesCache, tier: segValue($("runtime-seg")), skipScratch: drawerDesktop(),
    });
    if (!again) {
      // The folder stays (the user's), and it is no more the previous source's: the help line must stop claiming it.
      if (spawnWsSource === "profile" || spawnWsSource === "last") {
        spawnWsSource = "";
        if (spawnWsPicker && drawerDesktop()) spawnWsPicker.showIdleNote();
      }
      return;
    }
    setSpawnCwd(again.cwd, again.source);
    onSpawnCwdChanged();
    if (spawnWsPicker && drawerDesktop()) spawnWsPicker.showIdleNote();
    return;
  }
  setSpawnCwd(p.default_cwd, "profile");
  onSpawnCwdChanged();
  if (spawnWsPicker && drawerDesktop()) spawnWsPicker.showIdleNote();
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
// choice count is runtime data, not something knowable at build time. An enum
// that fits four segments with its leading "not set" renders as a .seg —
// seeing the alternatives beats saving the space a select would — and
// anything wider (the built-in Model) stays a plain <select> so it
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
      if (choices.length > 0 && choices.length <= 3) {
        ctrl = document.createElement("div");
        ctrl.className = "seg";
        // A seg has no empty state: "not set" is how a picked value is cleared.
        buildSeg(ctrl, [{ value: "", label: "not set" }, ...choices.map((c) => ({ value: c, label: c }))], current[o.key] || "");
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
    { value: "box", label: "box" },
    { value: "host", label: "host", danger: true },
  ], value || "box", (v) => {
    $("pf-runtime-warn").hidden = v !== "host";
    renderPfWorkspaceOptions(pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value));
    syncPfAccountWarn();
    syncEditorActions();
  });
  $("pf-runtime-warn").hidden = (value || "") !== "host";
}
// renderPfAccount fills the profile's Agent account select. The default
// account is value "" (a profile with no account stored). It paints from the
// cache at once — fillForm is synchronous and syncEditorActions runs right
// after — and, when the cache is empty, repaints once the fetch lands,
// keeping whatever the select holds by then. acctChoices keeps the profile's
// own account selectable even if the list does not carry it, so Save can
// never silently drop it.
function renderPfAccount(value) {
  paintPfAccount(value || "");
  if (!accountsCache.length) fetchAccounts().then(() => paintPfAccount($("pf-account").value));
}
function paintPfAccount(value) {
  const sel = $("pf-account");
  sel.replaceChildren(...acctChoices(accountsCache, value).map((n) => {
    const o = document.createElement("option");
    o.value = n === ACCT_DEFAULT ? "" : n;
    o.textContent = n === ACCT_DEFAULT ? "default (~/.claude)" : n;
    return o;
  }));
  sel.value = value || "";
  syncPfAccountWarn();
  paintPfImports();
}
// syncPfAccountWarn: a non-default account on the host tier is config kept
// apart, not a security boundary — say so where the choice is made.
function syncPfAccountWarn() {
  $("pf-account-warn").hidden = !($("pf-account").value && segValue($("pf-runtime")) === "host");
}
// syncPfReplyable shows "Allow replies by default" only for a claude binary:
// replies are claude-only on the server, and a default nothing honours is a
// setting that lies.
function syncPfReplyable() {
  // Replies off (server kill-switch, /api/meta) or a non-claude bin: the row is
  // hidden, and the save line below (see replyableToSend) keeps the stored value
  // rather than dropping it.
  $("pf-replyable-row").hidden = globalThis.repliesEnabled !== true || !isClaudeBinName($("pf-bin").value);
}
// --- the GH block of the profile editor (N1) --------------------------------
// ghApps: the instance's GH App names from /api/meta (names only; the client id
// and key path never reach the browser). Loaded once at boot by initEnv.
let ghApps = [];
// stEntries: the instance's static_tokens as /api/meta describes them (names,
// env names, hosts; never a value or where it is read from). Loaded at boot.
let stEntries = [];
// awscEntries: the instance's aws_connections as /api/meta describes them (name,
// label, account, region, whether it can write; never the source profile).
let awscEntries = [];
// applyCredLists takes the credential lists out of a /api/meta answer and repaints
// the profile form's pickers. The ticks the operator has made in an open form are
// kept; only a tick on an entry that was defined a moment ago and is gone now is
// dropped (ticksAfterRefresh). A repaint is skipped when nothing changed, so a
// refresh never disturbs a form that needs none.
function applyCredLists(meta) {
  const apps = meta && Array.isArray(meta.github_apps) ? meta.github_apps : [];
  const sts = meta && Array.isArray(meta.static_tokens) ? meta.static_tokens : [];
  const awscs = meta && Array.isArray(meta.aws_connections) ? meta.aws_connections : [];
  const names = (l) => l.map((e) => (typeof e === "string" ? e : e.name));
  const oldSt = names(stEntries), oldAwsc = names(awscEntries);
  const changed = JSON.stringify([ghApps, stEntries, awscEntries]) !== JSON.stringify([apps, sts, awscs]);
  ghApps = apps;
  stEntries = sts;
  awscEntries = awscs;
  if (!changed && credListsPainted) return;
  credListsPainted = true;
  renderStPicker(ticksAfterRefresh(readStTicked(), oldSt, names(sts)));
  renderAwscPicker(ticksAfterRefresh(readAwscTicked(), oldAwsc, names(awscs)));
  renderGhOwners(readGhRows()); // a row on a removed App or token keeps it, shown, for the server to name
  syncEditorActions();
  // The profile page reads the same lists: a row it called missing may exist now.
  if (typeof pfvMetaChanged === "function") pfvMetaChanged();
}
let credListsPainted = false;
// refreshCredLists re-reads /api/meta and repaints the pickers: when the
// Profiles tab is shown and after a Credentials change. Newest answer wins; a
// failed fetch leaves the lists as they were. `view` is a GET /api/credentials
// answer the caller has just read (loadCredentials); without one the line's
// view is fetched.
let credListsSeq = 0;
async function refreshCredLists(view) {
  refreshPfCreds(view);
  const seq = ++credListsSeq;
  let meta;
  try { meta = await api("/api/meta"); } catch (_) { return; }
  if (seq !== credListsSeq) return;
  applyCredLists(meta);
}

// --- the profile form's CLAUDE.md imports row -------------------------------
// pfImportsSet: what GET /api/bundle/imports said when the form was last
// filled (the files a box would get, and what was left out), or null before the
// first answer and after a failure (pfImportsFailed tells the two apart).
// pfImportsAbsent: the route answered 404, which is how the server says "no box
// layer or the bundle is off". The row is then hidden, not shown as an error:
// there is nothing to give and nothing the user did wrong. The stored
// imports_off still rides along on Save (importsOffToSend).
let pfImportsSet = null;
let pfImportsFailed = false;
let pfImportsAbsent = false;
let pfImportsSeq = 0;
async function refreshPfImports() {
  const seq = ++pfImportsSeq;
  // Bundling is off: the route is not registered, so asking is a guaranteed 404
  // (#151). That is the "absent" answer already; take it without the request.
  // Before /api/meta has answered nothing is known, so the request still goes.
  if (metaLoaded && !bundleImports) {
    pfImportsSet = null;
    pfImportsFailed = false;
    pfImportsAbsent = true;
    paintPfImports();
    return;
  }
  let set = null;
  let failed = false;
  let absent = false;
  try {
    const v = await api("/api/bundle/imports", { signal: deadlineSignal(5000) });
    if (v && typeof v === "object" && Array.isArray(v.files)) set = v;
    else failed = true;
  } catch (e) {
    if (e && e.status === 404) absent = true;
    else failed = true;
  }
  if (seq !== pfImportsSeq) return;
  pfImportsSet = set;
  pfImportsFailed = failed;
  pfImportsAbsent = absent;
  paintPfImports();
}
// paintPfImports applies importsRowState to the row. The checkbox is the source
// of truth for the switch (fillForm sets it from the stored imports_off), so
// this never writes its checked state. Run from syncEditorActions (after its
// lock loop, which re-enables every checkbox) and on a tier, account or switch
// change, so the row and the profile it describes cannot disagree.
function paintPfImports() {
  const row = $("pf-imports-row");
  if (!row) return;
  const view = profiles.find((x) => x.name === selectedProfileName()) || null;
  const tier = (metaLoaded && !boxRuntime) || pfImportsAbsent || segValue($("pf-runtime")) === "host" ? "host" : "box";
  const st = importsRowState({
    tier,
    account: $("pf-account").value,
    accounts: accountsCache,
    set: pfImportsSet,
    failed: pfImportsFailed,
    off: !$("pf-imports").checked,
  });
  row.hidden = st.hidden;
  const cb = $("pf-imports");
  const locked = editorState(view).locked;
  cb.disabled = st.disabled || locked;
  cb.classList.toggle("off", cb.disabled);
  $("pf-imports-list").textContent = st.list;
  $("pf-imports-warn").textContent = st.warn;
  $("pf-imports-warn").hidden = !st.warn;
  $("pf-imports-refused").textContent = st.refused;
  $("pf-imports-refused").hidden = !st.refused;
}

// --- the desktop profile page's reach view (profilepage.js) -----------------
// pfCredsView: what GET /api/credentials said when the Profiles tab was last
// shown (the store lock and the elevate windows), or null: never fetched, or
// it failed, and the reach words then have no Store part and no window. Desktop
// only, like the New session box that reads the same view (spawnpanel.js).
let pfCredsView = null;
let pfCredsSeq = 0;
// `known` is a view the caller has just read: it is used as it is, with no second
// request, and still wins over a fetch that is in flight.
async function refreshPfCreds(known) {
  if (!drawerDesktop()) return;
  const seq = ++pfCredsSeq;
  let got = null;
  if (known && typeof known === "object") {
    got = known;
  } else {
    try {
      const v = await api("/api/credentials", { signal: deadlineSignal(5000) });
      if (v && typeof v === "object") got = v;
    } catch (_) { /* no creds answer: no Store part, no window */ }
  }
  if (seq !== pfCredsSeq) return;
  pfCredsView = got;
  if (typeof pfvMetaChanged === "function") pfvMetaChanged();
}

// The stored binding as loaded: the editor has no field for its extra egress
// hosts, so they are carried unchanged on save (see ghBindingFromForm).
let pfLoadedGh = null;
let ghRowSeq = 0;
function ghField(cls, labelText, ctrl) {
  const col = document.createElement("div");
  col.className = "fcol " + cls;
  const id = "pf-gh-f" + (++ghRowSeq);
  ctrl.id = id;
  const lab = document.createElement("label");
  lab.className = "flab";
  lab.htmlFor = id;
  lab.textContent = labelText;
  col.append(lab, ctrl);
  return col;
}
// renderGhOwners paints one row per owner: App or token picker, owner login, repos, and a
// remove button. Rows are rebuilt from plain {owner, app, token, repos} objects, so
// add and remove never lose what was typed in the other rows (readGhRows first).
function renderGhOwners(rows) {
  const host = $("pf-gh-owners");
  host.replaceChildren();
  rows.forEach((r, i) => {
    const row = document.createElement("div");
    row.className = "frow pf-gh-owner";
    const sel = document.createElement("select");
    sel.className = "fld pf-gh-app";
    // A stored App that is no longer defined stays selectable, so opening and
    // saving a profile never silently re-points it; the server then names it.
    // One picker for both kinds of owner: an App (values app:<name>) or a
    // personal access token entry (token:<entry>); see readGhRows.
    const apps = ghApps.includes(r.app) || !r.app ? ghApps : [...ghApps, r.app];
    const pats = ghUsableTokens(stEntries).map((e) => e.name);
    if (r.token && !pats.includes(r.token)) pats.push(r.token);
    if (!apps.length && !pats.length) {
      const o = document.createElement("option");
      o.value = ""; o.textContent = "no App or token defined"; o.disabled = true;
      sel.append(o);
    }
    for (const [label, kind, list] of [["Apps", "app", apps], ["Personal access tokens", "token", pats]]) {
      if (!list.length) continue;
      const grp = document.createElement("optgroup");
      grp.label = label;
      for (const n of list) {
        const o = document.createElement("option");
        o.value = kind + ":" + n; o.textContent = n;
        grp.append(o);
      }
      sel.append(grp);
    }
    sel.value = r.token ? "token:" + r.token : r.app ? "app:" + r.app : apps.length ? "app:" + apps[0] : pats.length ? "token:" + pats[0] : "";
    const note = document.createElement("p");
    note.className = "tiny pf-gh-pat-note";
    note.textContent = "A personal access token is not narrowed by CuliBox: the permissions above do not apply to it and it may reach more than these repositories. CuliBox only checks that each request names one of them. It is long-lived and is not revoked when the session ends.";
    note.hidden = !sel.value.startsWith("token:");
    sel.addEventListener("change", () => { note.hidden = !sel.value.startsWith("token:"); });
    const owner = document.createElement("input");
    owner.type = "text"; owner.className = "fld pf-gh-owner-name"; owner.placeholder = "culiops";
    owner.autocomplete = "off"; owner.spellcheck = false; owner.value = r.owner || "";
    const repos = document.createElement("textarea");
    repos.className = "fld pf-gh-repos"; repos.rows = 2; repos.spellcheck = false;
    repos.placeholder = "one per line or comma separated"; repos.value = r.repos || "";
    const rm = document.createElement("button");
    rm.type = "button"; rm.className = "btn pf-gh-rm"; rm.textContent = "×";
    rm.setAttribute("aria-label", "Remove owner " + (i + 1));
    rm.addEventListener("click", () => {
      const cur = readGhRows();
      cur.splice(i, 1);
      renderGhOwners(cur);
      syncEditorActions();
    });
    const rmCol = document.createElement("div");
    rmCol.className = "fcol rm";
    rmCol.append(rm);
    row.append(ghField("app", "App or token", sel), ghField("own", "Owner", owner), ghField("repos", "Repositories", repos), rmCol, note);
    host.append(row);
  });
  $("pf-gh-noapps").hidden = ghApps.length > 0 || ghUsableTokens(stEntries).length > 0;
}
function readGhRows() {
  return [...$("pf-gh-owners").querySelectorAll(".pf-gh-owner")].map((row) => {
    const pick = row.querySelector(".pf-gh-app").value;
    return {
      owner: row.querySelector(".pf-gh-owner-name").value,
      app: pick.startsWith("app:") ? pick.slice(4) : "",
      token: pick.startsWith("token:") ? pick.slice(6) : "",
      repos: row.querySelector(".pf-gh-repos").value,
    };
  });
}
// renderGhPerms draws one row per permission: a checkbox (ticked = read) and, for
// the keys the server lets be written, a read | write seg that shows only while
// the row is ticked. levels = ghPermLevels' shape.
function renderGhPerms(levels) {
  const host = $("pf-gh-perms");
  host.replaceChildren();
  for (const k of GH_PERMS) {
    const lev = levels[k] || "off";
    const row = document.createElement("div");
    row.className = "pf-gh-perm";
    const lab = document.createElement("label");
    lab.className = "pf-check";
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.dataset.perm = k; cb.checked = lev !== "off";
    lab.append(cb, document.createTextNode(k.replace(/_/g, " ")));
    row.append(lab);
    let seg = null;
    if (GH_WRITABLE.includes(k)) {
      seg = document.createElement("div");
      seg.className = "seg pf-gh-level";
      seg.dataset.permLevel = k;
      seg.setAttribute("role", "group");
      seg.setAttribute("aria-label", k.replace(/_/g, " ") + " level");
      buildSeg(seg, [{ value: "read", label: "read" }, { value: "write", label: "write" }], lev === "write" ? "write" : "read", () => syncGhWriteWarn());
      seg.hidden = !cb.checked;
      row.append(seg);
    }
    cb.addEventListener("change", () => {
      $("pf-gh-perm-err").hidden = true;
      if (seg) { seg.hidden = !cb.checked; if (!cb.checked) setSegValue(seg, "read"); }
      syncGhWriteWarn();
    });
    host.append(row);
  }
  syncGhWriteWarn();
}
function readGhPerms() {
  const out = {};
  for (const cb of $("pf-gh-perms").querySelectorAll("input[data-perm]")) {
    const seg = $("pf-gh-perms").querySelector('.pf-gh-level[data-perm-level="' + cb.dataset.perm + '"]');
    out[cb.dataset.perm] = !cb.checked ? "off" : seg && segValue(seg) === "write" ? "write" : "read";
  }
  return out;
}
// syncGhWriteWarn shows the one-line write warning under the permissions. The
// wording depends on the work mode (a copy can push, a folder profile cannot),
// so syncPfMode calls it too.
function syncGhWriteWarn() {
  const el = $("pf-gh-write-warn");
  if (!el) return;
  const text = ghWriteWarning(readGhPerms(), pfMode() !== "copy");
  el.textContent = text;
  el.hidden = !text;
}
$("pf-gh-add").addEventListener("click", () => {
  renderGhOwners([...readGhRows(), { owner: "", app: "", token: "", repos: "" }]);
  syncEditorActions();
  const last = $("pf-gh-owners").querySelector(".pf-gh-owner:last-child .pf-gh-owner-name");
  if (last) last.focus();
});

// --- the Tokens block of the profile editor (N3) ---------------------------
// renderStPicker draws one checkbox per entry, plus one for each ticked name the
// instance no longer defines (shown as "(not defined)"), so a save never drops
// a stored name silently: the server refuses it by name instead.
function renderStPicker(ticked) {
  const host = $("pf-st-list");
  host.replaceChildren();
  const defined = new Set(stEntries.map((e) => e.name));
  const groups = stGroups(stEntries);
  const missing = (ticked || []).filter((n) => !defined.has(n)).sort();
  if (missing.length) groups.push({ provider: "not defined", entries: missing.map((name) => ({ name, missing: true })) });
  for (const g of groups) {
    const head = document.createElement("span");
    head.className = "tiny mut pf-st-group";
    head.textContent = g.provider;
    host.append(head);
    for (const e of g.entries) {
      const lab = document.createElement("label");
      lab.className = "pf-check pf-st-entry";
      const cb = document.createElement("input");
      cb.type = "checkbox"; cb.dataset.token = e.name; cb.checked = (ticked || []).includes(e.name);
      cb.addEventListener("change", () => { $("pf-st-err").hidden = true; });
      const text = document.createElement("span");
      text.className = "pf-st-text";
      const nm = document.createElement("span");
      nm.textContent = e.missing ? e.name + " (not defined)" : e.label ? e.label + " · " + e.name : e.name;
      text.append(nm);
      if (e.prod) {
        // The AWS production chip, so a production token reads as one.
        const chip = document.createElement("span");
        chip.className = "bdg aws prod";
        chip.textContent = "production";
        text.append(" ", chip);
      }
      for (const line of e.missing ? [] : [e.description, stEntryLine(e)]) {
        if (!line) continue;
        const d = document.createElement("span");
        d.className = "tiny mut pf-st-line";
        d.textContent = line;
        text.append(d);
      }
      lab.append(cb, text);
      host.append(lab);
    }
  }
  $("pf-st-none").hidden = groups.length > 0;
}
function readStTicked() {
  return [...$("pf-st-list").querySelectorAll("input[data-token]")].filter((cb) => cb.checked).map((cb) => cb.dataset.token);
}

// --- the AWS connections block of the profile editor (#155) ---------------
// renderAwscPicker draws one checkbox per connection, plus one for each ticked
// name the instance no longer defines (shown as "(not defined)"), so a save
// never drops a stored name silently: the server refuses it by name instead.
function renderAwscPicker(ticked) {
  const host = $("pf-awsc-list");
  host.replaceChildren();
  const rows = awscPickerRows(awscEntries, ticked);
  for (const r of rows) {
    const lab = document.createElement("label");
    lab.className = "pf-check pf-st-entry";
    const cb = document.createElement("input");
    cb.type = "checkbox"; cb.dataset.awsc = r.name; cb.checked = (ticked || []).includes(r.name);
    cb.addEventListener("change", () => { $("pf-awsc-err").hidden = true; syncEditorActions(); });
    const text = document.createElement("span");
    text.className = "pf-st-text";
    const nm = document.createElement("span");
    nm.textContent = r.missing ? r.name + " (not defined)" : r.label ? r.label + " · " + r.name : r.name;
    text.append(nm);
    if (r.prod) {
      const chip = document.createElement("span");
      chip.className = "bdg aws prod";
      chip.textContent = "production";
      text.append(" ", chip);
    }
    if (r.write) {
      const chip = document.createElement("span");
      chip.className = "bdg aws write";
      chip.textContent = "write";
      text.append(" ", chip);
    }
    const entry = awscEntries.find((e) => e.name === r.name);
    for (const line of r.missing ? [] : [entry && entry.description, r.account && "account " + r.account + (entry && entry.region ? " · " + entry.region : "")]) {
      if (!line) continue;
      const d = document.createElement("span");
      d.className = "tiny mut pf-st-line";
      d.textContent = line;
      text.append(d);
    }
    lab.append(cb, text);
    host.append(lab);
  }
  $("pf-awsc-none").hidden = rows.length > 0;
}
function readAwscTicked() {
  return [...$("pf-awsc-list").querySelectorAll("input[data-awsc]")].filter((cb) => cb.checked).map((cb) => cb.dataset.awsc);
}

// --- The inline AWS binding of a legacy profile (N2) -----------------------
// Not editable: a summary, "Convert to a connection" (POST
// /api/profiles/{name}/convert-aws) and "Remove". The profile runs unchanged
// until one of the two is used. Every string below that came from the server
// goes in through textContent.
let pfAwsBusy = false;
// paintPfAws draws the block from pfInlineAws / pfAwsRemoved / pfAwsDone.
function paintPfAws() {
  const has = !!pfInlineAws;
  const removed = has && pfAwsRemoved;
  $("pf-aws-block").hidden = !has && !pfAwsDone;
  $("pf-aws-sum").hidden = !has || removed;
  $("pf-aws-removed").hidden = !removed;
  $("pf-aws-done").textContent = pfAwsDone;
  $("pf-aws-done").hidden = !pfAwsDone;
  $("pf-aws-sum-text").textContent = has ? inlineAWSSummary(pfInlineAws) : "";
  closePfAwsConfirm();
}
function closePfAwsConfirm() {
  $("pf-aws-confirm").hidden = true;
  $("pf-aws-ack").hidden = true;
  $("pf-aws-ack-cb").checked = false;
  $("pf-aws-convert-err").hidden = true;
  $("pf-aws-convert-err").textContent = "";
}
// syncPfAwsAck: with the write warning showing, Convert waits for "I understand".
function syncPfAwsAck() {
  if (!$("pf-aws-ack").hidden && !$("pf-aws-ack-cb").checked) {
    $("pf-aws-confirm-go").disabled = true;
    $("pf-aws-confirm-go").classList.add("off");
  }
}
function openPfAwsConfirm() {
  if (!pfInlineAws || !selectedProfileName()) return;
  closePfAwsConfirm();
  $("pf-aws-confirm-text").textContent = convertAWSConfirm(selectedProfileName()) + " Unsaved changes to this profile are discarded.";
  $("pf-aws-confirm").hidden = false;
  syncEditorActions();
  $("pf-aws-confirm-go").focus();
}
async function convertPfAws(ackWrite) {
  const name = selectedProfileName();
  if (!name || pfAwsBusy) return;
  const err = $("pf-aws-convert-err");
  err.hidden = true;
  pfAwsBusy = true;
  syncEditorActions();
  let res = null;
  try {
    res = await api("/api/profiles/" + encodeURIComponent(name) + "/convert-aws", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(ackWrite ? { ack_write: true } : {}),
    });
  } catch (e) {
    pfAwsBusy = false;
    if (e.status === 409 && e.code === "needs_ack") {
      // The role the profile uses can write to the account: D4 wording, then
      // "I understand", then the same request again with ack_write.
      const v = (e.body && e.body.verdict) || {};
      $("pf-aws-ack-text").textContent = CRED_D4 + " " + CRED_D4_REMEDY;
      $("pf-aws-ack-actions").textContent = Array.isArray(v.actions) && v.actions.length ? "Allowed here: " + v.actions.join(", ") : "";
      $("pf-aws-ack-cb").checked = false;
      $("pf-aws-ack").hidden = false;
    } else if (e.code === "profile_changed") {
      // The stored profile moved under the convert: show what it holds now, with
      // the sentence in the block's status line (the reload clears the error line).
      await reloadPfAfterConvert(name, convertAWSErrText(e));
      return;
    } else {
      err.textContent = convertAWSErrText(e) || awsErrText(e);
      err.hidden = false;
    }
    syncEditorActions();
    return;
  }
  pfAwsBusy = false;
  await reloadPfAfterConvert(name, "Converted to the connection " + String(res && res.connection) + ". The profile now uses it; the next session gets a new box.");
}
// reloadPfAfterConvert shows what the server now holds (the convert changed the
// stored profile), then the result line.
async function reloadPfAfterConvert(name, done) {
  try {
    await loadProfiles(false);
    // The convert created a connection: the pickers must list it before fillForm
    // ticks it, or it shows as "(not defined)" until the next refresh.
    await refreshCredLists();
    fillForm(await api("/api/profiles/" + encodeURIComponent(name)));
    pfAwsDone = done;
    paintPfAws();
    renderProfileList();
  } catch (e) { setDialogError("settings-modal", "Converted, but reloading the profile failed: " + e.message); }
  syncEditorActions();
}
$("pf-aws-convert").addEventListener("click", openPfAwsConfirm);
$("pf-aws-confirm-cancel").addEventListener("click", () => { closePfAwsConfirm(); syncEditorActions(); $("pf-aws-convert").focus(); });
$("pf-aws-confirm-go").addEventListener("click", () => convertPfAws(!$("pf-aws-ack").hidden && $("pf-aws-ack-cb").checked));
$("pf-aws-ack-cb").addEventListener("change", syncEditorActions);
$("pf-aws-remove").addEventListener("click", () => { pfAwsRemoved = true; paintPfAws(); syncEditorActions(); $("pf-aws-undo").focus(); });
$("pf-aws-undo").addEventListener("click", () => { pfAwsRemoved = false; paintPfAws(); syncEditorActions(); $("pf-aws-remove").focus(); });
// The Keyboard shortcuts buttons in Appearance and About open the help screen.
// (#bug-open is wired in bugreport.js; these two belong to the shortcuts feature.)
$("kbd-open-settings").onclick = () => showShortcuts();
$("kbd-open-about").onclick = () => showShortcuts();
// The "none defined" hints and the picker's pointer open the Credentials tab on
// the group they are about.
for (const a of document.querySelectorAll("#settings-modal a[data-creds-group]")) {
  a.addEventListener("click", (ev) => { ev.preventDefault(); openSettings("creds", a.dataset.credsGroup); });
}

// The allowlist's live rows, as the editor sees them: envRows(cfg) turns a
// saved profile into rows, the table renders and edits them in place, and
// rowsToConfig turns them back into the two fields the server stores.
let pfEnvRows = [];
// The stored profile's own runtime string ("" when it names none), kept for
// runtimeToSend's fallback.
let pfStoredRuntime = "";
// The stored profile as loaded (GET /api/profiles/{name}, the full config). The
// work-mode fields (clone_of, default_cwd, clone_share) are read back from their
// own inputs on save (wmFieldsToSend), so this only keeps the values a hidden
// row cannot speak for: replyable (replyableToSend) and imports_off
// (importsOffToSend) are written back as stored when their row is not usable.
let pfLoaded = null;
// The work mode and copy directory the loaded profile was saved with, for the
// "switching never deletes the copy" note (wmSwitchNote). Set by fillForm.
let pfSavedMode = "folder";
let pfSavedCopyDir = "";
// The stored profile's inline AWS binding exactly as loaded (null when it has
// none), and whether Remove was pressed since. readForm sends the binding back
// unchanged unless it was removed (awsInlineToSend).
let pfInlineAws = null;
let pfAwsRemoved = false;
// The line shown after a convert, until the form is next filled.
let pfAwsDone = "";
function fillForm(p) {
  // Any load is the editor showing what it was given: a stale-save banner about
  // the form it replaces no longer applies.
  $("pf-stale").hidden = true;
  $("pf-name").value = p.name || "";
  $("pf-bin").value = p.bin || "";
  $("pf-base").value = p.base || "inherit";
  pfStoredRuntime = p.runtime || "";
  pfLoaded = p;
  // A profile naming no tier runs on the instance default; /api/profiles has
  // already resolved it, so the seg lights the tier it really runs on.
  const resolved = (profiles.find((x) => x.name === p.name) || {}).runtime;
  renderPfRuntime(segFromRuntime(p.runtime, resolved));
  renderPfAccount(p.account || "");
  $("pf-args").value = (p.args || []).join("\n");
  pfEnvRows = envRows(p);
  renderEnvRows($("pf-env-rows"), pfEnvRows, () => setDialogError("settings-modal", rowError(pfEnvRows)));
  // A private copy's workspace field is the folder it copies (clone_of), which
  // need not be one of the workspace options: the custom-path branch shows it.
  const copy = wmModeOf(p) === "copy";
  pfSavedMode = copy ? "copy" : "folder";
  pfSavedCopyDir = copy ? p.default_cwd || "" : "";
  for (const r of document.querySelectorAll('input[name="pf-mode"]')) r.checked = r.value === pfSavedMode;
  renderPfWorkspaceOptions(copy ? p.clone_of : p.default_cwd || "");
  $("pf-copy-dir").value = pfSavedCopyDir;
  $("pf-copy-share").value = contextPathsToText(p.clone_share);
  $("pf-clone-autoreturn").checked = !p.clone_no_auto_return;
  renderPfNotHome(p.name);
  $("pf-copy-warn").hidden = true;
  $("pf-copy-err").hidden = true;
  renderPfOptions(p.options || {});
  $("pf-context").value = contextPathsToText(p.context_paths);
  $("pf-context-warn").hidden = true;
  $("pf-egress-allow").value = egressAllowToText(p.egress_allow);
  $("pf-egress-open").checked = !!p.egress_open;
  $("pf-egress-open-warn").hidden = !p.egress_open;
  $("pf-replyable").checked = !!p.replyable;
  // Checked = the box gets the files the CLAUDE.md imports; imports_off absent means on.
  $("pf-imports").checked = !p.imports_off;
  // The inline AWS binding is not editable here: it is carried as loaded and
  // shown as a summary (paintPfAws) until it is converted or removed.
  pfInlineAws = p.aws && typeof p.aws === "object" ? p.aws : null;
  pfAwsRemoved = false;
  pfAwsDone = "";
  paintPfAws();
  const gh = p.github || null;
  pfLoadedGh = gh;
  renderGhOwners(((gh && gh.owners) || []).map((o) => ({ owner: o.owner, app: o.app, token: o.token, repos: (o.repos || []).join("\n") })));
  renderGhPerms(ghPermLevels(gh));
  renderStPicker(Array.isArray(p.tokens) ? p.tokens : []);
  $("pf-st-err").hidden = true;
  renderAwscPicker(Array.isArray(p.aws_connections) ? p.aws_connections : []);
  $("pf-awsc-err").hidden = true;
  syncPfReplyable();
  syncEditorActions();
  refreshPfImports();
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
  // A read-only built-in profile can't Save, so Duplicate — the one action
  // that actually does something — takes the primary look instead; an
  // editable profile keeps Save as the primary and Duplicate plain.
  $("pf-save").classList.toggle("pri", !st.locked);
  $("pf-duplicate").classList.toggle("pri", st.locked);
  // Every interactive control the editor pane holds: a locked profile's fields
  // (.fld), its containment/option .seg buttons, its bare bool checkboxes (no
  // class of their own), and the allowlist's row buttons and "+ add" triggers
  // (.btn) — a read-only profile that still lets a row be added or removed is
  // the exact thing #pf-lock's caption is promising will not happen.
  for (const f of $("manage").querySelectorAll(".ped .fld, .ped .seg button, .ped .btn, .ped input[type=checkbox], .ped input[type=radio]")) {
    f.classList.toggle("off", st.locked);
    f.disabled = st.locked;
  }
  // After the loop above, which re-enables every unlocked field.
  const nm = $("pf-name");
  nm.classList.toggle("off", st.nameLocked);
  nm.disabled = st.nameLocked;
  nm.title = st.nameLocked ? "A saved profile's name is fixed. Duplicate it to use another name." : "";
  $("pf-save").textContent = saveLabel(segValue($("pf-runtime")));
  // Readable host paths only mean something inside a box, so the host tier hides
  // an empty row. A filled one stays visible with its "needs runtime: box" hint:
  // readForm still sends the text and the server refuses it, so the paths must
  // stay where the user can clear them (contextRowState).
  const ctxRow = contextRowState(segValue($("pf-runtime")), $("pf-context").value);
  $("pf-context-row").hidden = ctxRow.hidden;
  $("pf-context-hint").hidden = !ctxRow.hint;
  // After the lock loop above, which re-enabled the imports checkbox too.
  paintPfImports();
  // After the lock loop above, which re-enabled every .fld: the AWS blocks are
  // also closed by the host tier, and this is the only pass that knows both.
  const aws = awsFieldsState(st.locked, segValue($("pf-runtime")));
  $("pf-aws-hint").hidden = !aws.hint;
  // A profile binds AWS inline or by named connections, never both: an inline
  // binding closes the picker until it is converted or removed (awscExclusiveState).
  // The picker also closes on the lock and the host tier, like the Tokens block.
  const inline = awsInlineToSend(pfInlineAws, pfAwsRemoved);
  const excl = awscExclusiveState(readAwscTicked().length, inline ? inline.role_arn : "");
  for (const id of ["pf-aws-convert", "pf-aws-remove", "pf-aws-undo", "pf-aws-confirm-go", "pf-aws-confirm-cancel"]) {
    const b = $(id);
    b.classList.toggle("off", aws.disabled || pfAwsBusy);
    b.disabled = aws.disabled || pfAwsBusy;
  }
  // Both set (a stored profile the server would refuse): the connections win the
  // display, so converting is closed and untick/remove is the way out.
  if (excl.inlineDisabled) {
    $("pf-aws-convert").classList.add("off");
    $("pf-aws-convert").disabled = true;
  }
  $("pf-aws-sum-note").textContent = excl.inlineDisabled ? excl.note : "Read only. This profile still runs unchanged until you convert it.";
  for (const f of $("pf-awsc-block").querySelectorAll(".fld, input[type=checkbox]")) {
    f.classList.toggle("off", aws.disabled || excl.pickerDisabled);
    f.disabled = aws.disabled || excl.pickerDisabled;
  }
  // The whole row, label text included, not only its checkbox.
  for (const row of $("pf-awsc-list").querySelectorAll(".pf-st-entry")) {
    row.classList.toggle("off", aws.disabled || excl.pickerDisabled);
  }
  $("pf-awsc-hint").hidden = !aws.hint;
  $("pf-awsc-note").textContent = excl.pickerDisabled ? excl.note : "";
  $("pf-awsc-note").hidden = !excl.pickerDisabled || aws.disabled;
  syncPfAwsAck();
  // The GH block closes on exactly the same terms (lock, host tier).
  for (const f of $("pf-gh-block").querySelectorAll(".fld, .btn, input[type=checkbox], .seg button")) {
    f.classList.toggle("off", aws.disabled);
    f.disabled = aws.disabled;
  }
  $("pf-gh-hint").hidden = !aws.hint;
  // And so does the Tokens block.
  for (const f of $("pf-st-block").querySelectorAll(".fld, .btn, input[type=checkbox]")) {
    f.classList.toggle("off", aws.disabled);
    f.disabled = aws.disabled;
  }
  $("pf-st-hint").hidden = !aws.hint;
  // The Network block is a box-tier setting: hidden on host, locked like the rest.
  $("pf-net-block").hidden = segValue($("pf-runtime")) === "host";
  for (const f of $("pf-net-block").querySelectorAll(".fld, input[type=checkbox]")) {
    f.classList.toggle("off", aws.disabled);
    f.disabled = aws.disabled;
  }
  // Last: the lock loop above re-enabled every radio, and this is the pass that
  // knows the tier, the instance and the lock together.
  syncPfMode();
}
// pfMode: the work mode the editor shows. A disabled radio that is checked still
// matches :checked, so a copy profile on a host seg or a copy-less instance
// reads as "copy" and keeps its fields on save.
function pfMode() {
  const r = document.querySelector('input[name="pf-mode"]:checked');
  return r && r.value === "copy" ? "copy" : "folder";
}
// syncPfMode: display only, safe on every input event. It never touches the
// network and never reads layout, and it never un-checks a radio: a copy that
// cannot run here is still a copy, it only says why.
function syncPfMode() {
  const mode = pfMode();
  const view = profiles.find((p) => p.name === selectedProfileName()) || null;
  const st = wmCardState({ runtime: segValue($("pf-runtime")), mode, unavailable: cloneUnavailable, locked: editorState(view).locked });
  $("pf-cwd-lab").textContent = mode === "copy" ? "Folder to copy" : "Default workspace";
  syncGhWriteWarn();
  $("pf-copy-block").hidden = mode !== "copy";
  const folderRadio = document.querySelector('input[name="pf-mode"][value="folder"]');
  const copyRadio = document.querySelector('input[name="pf-mode"][value="copy"]');
  copyRadio.disabled = st.copyDisabled;
  copyRadio.classList.toggle("off", st.copyDisabled);
  $("pf-mode-copy-card").classList.toggle("off", st.copyDisabled);
  folderRadio.closest(".pf-mode-card").classList.toggle("off", folderRadio.disabled);
  $("pf-mode-copy-why").textContent = st.copyReason;
  // No box layer means every session is host, whatever the seg shows.
  const folderCopy = wmFolderCopy((metaLoaded && !boxRuntime) || segValue($("pf-runtime")) === "host");
  $("pf-mode-folder-body").textContent = folderCopy.body;
  $("pf-mode-diff-commit").textContent = folderCopy.commitCell;
  $("pf-mode-diff-home").textContent = folderCopy.homeCell;
  $("pf-mode-hint").textContent = st.hint;
  $("pf-mode-hint").hidden = !st.hint;
  const note = wmSwitchNote(pfSavedMode, mode, pfSavedCopyDir, window.HOME_DIR || "");
  $("pf-mode-switch").textContent = note;
  $("pf-mode-switch").hidden = !note;
  const ghBinding = ghBindingFromForm({ owners: readGhRows(), permissions: readGhPerms() });
  $("pf-copy-gh").hidden = st.copyDisabled || !wmNeedsGitHubHint(mode, ghBinding, readStTicked(), stEntries);
}
// renderPfNotHome: what the last return left behind for this profile, from the
// profile list's view (the editor's own GET has no such state). Per branch the
// reason, the fix and, when the server has one, the specific guard or git error.
// Every string is box-derived or server text: textContent only.
function renderPfNotHome(name) {
  const host = $("pf-nothome");
  const view = profiles.find((x) => x.name === name);
  const nh = view && view.not_home;
  const rows = [];
  for (const b of (nh && Array.isArray(nh.branches) ? nh.branches : [])) {
    const row = document.createElement("div");
    row.className = "pf-nh-row";
    const why = document.createElement("span");
    why.className = "pf-nh-why";
    why.textContent = notHomeReasonText(b);
    row.appendChild(why);
    if (b.detail) {
      const d = document.createElement("span");
      d.className = "pf-nh-detail";
      d.textContent = b.detail;
      row.appendChild(d);
    }
    const fix = document.createElement("span");
    fix.className = "pf-nh-fix mut";
    fix.textContent = notHomeFix(name, nh.base, b, !mobileActive(window.innerWidth));
    row.appendChild(fix);
    rows.push(row);
  }
  if (!rows.length && nh && nh.error) {
    const row = document.createElement("div");
    row.className = "pf-nh-row";
    row.textContent = notHomeLabel(nh) + ": " + nh.error;
    rows.push(row);
  }
  host.replaceChildren(...rows);
  host.hidden = rows.length === 0;
}
// pfFillCopyDir: a copy with no directory yet gets ~/box-clones/<folder name>,
// never a directory another profile already writes. Only fills an empty field.
function pfFillCopyDir() {
  if (pfMode() !== "copy" || $("pf-copy-dir").value.trim()) return;
  const me = selectedProfileName();
  const taken = profiles.filter((x) => x.name !== me).map((x) => x.default_cwd).filter(Boolean);
  const dir = wmCopyDirSuggestion(pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value), window.HOME_DIR || "", taken);
  if (dir) $("pf-copy-dir").value = dir;
}
// The editor lives in a closed <dialog> at boot, where focus() is a silent
// no-op; only grab focus when the dialog is actually on screen.
function newProfile() {
  selectedProfile = "";
  // bin starts as a real value, not just the placeholder: a blank bin hides the
  // claude-only options (Model, Effort) while the field still reads "claude" (#35).
  fillForm({ base: "inherit", bin: "claude" });
  setDialogError("settings-modal", "");
  renderProfileList();
  syncEditorActions();
  if ($("settings-modal").open) $("pf-name").focus();
}
function readForm() {
  const p = { name: $("pf-name").value.trim(), bin: $("pf-bin").value.trim(), base: $("pf-base").value };
  // The inline binding is not edited here: it goes back as loaded unless Remove was pressed.
  const aws = awsInlineToSend(pfInlineAws, pfAwsRemoved);
  if (aws) p.aws = aws;
  const gh = ghBindingFromForm({ owners: readGhRows(), permissions: readGhPerms(), egress_allow: pfLoadedGh && pfLoadedGh.egress_allow });
  if (gh) p.github = gh;
  const tokens = stTokensToSend(readStTicked());
  if (tokens) p.tokens = tokens;
  // Only when a connection is ticked: no key keeps a legacy profile's JSON as it was.
  const conns = awscToSend(readAwscTicked());
  if (conns) p.aws_connections = conns;
  const acct = $("pf-account").value;
  if (acct && acct !== ACCT_DEFAULT) p.account = acct;
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
  // default_cwd, and for a private copy clone_of and clone_share too, come from
  // the work-mode fields only: a copy saved without edits keeps all three.
  Object.assign(p, wmFieldsToSend(pfMode(), {
    folder: pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value),
    copyDir: $("pf-copy-dir").value,
    shareText: $("pf-copy-share").value,
    autoReturn: $("pf-clone-autoreturn").checked,
  }));
  const ctx = contextPathsFromText($("pf-context").value);
  if (ctx.length) p.context_paths = ctx;
  Object.assign(p, egressToSend(!$("pf-net-block").hidden, $("pf-egress-allow").value, $("pf-egress-open").checked));
  if (replyableToSend(!$("pf-replyable-row").hidden, $("pf-replyable").checked, pfLoaded && pfLoaded.replyable)) p.replyable = true;
  // A row the user cannot use (host tier, no box layer, an account without personal
  // config, a locked profile) keeps the stored value rather than dropping it.
  const importsUsable = !$("pf-imports-row").hidden && !$("pf-imports").disabled;
  if (importsOffToSend(importsUsable, $("pf-imports").checked, pfLoaded && pfLoaded.imports_off)) p.imports_off = true;
  // The tier is always sent as shown (see runtimeToSend): a blank would let the
  // instance default decide, and on a host-default instance that turns a profile
  // the editor lit as "box" into a host one. The server's own message answers an
  // AWS binding saved on the host tier. It is read last, from the whole profile:
  // with no box layer, whether a blank is kept depends on whether p holds a token,
  // egress, a readable folder or a private copy, and those keys are set above.
  const rt = runtimeToSend(segValue($("pf-runtime")), pfStoredRuntime, !metaLoaded || !!boxRuntime, p);
  if (rt) p.runtime = rt;
  return p;
}
// These three run while #settings-modal is open, so their errors go to the
// dialog's own inline line. A toast is fixed-position and paints beneath a modal
// backdrop, which would leave the failure invisible.
// pfSaveError: a Save that did not happen.
function pfSaveError(msg) {
  setDialogError("settings-modal", msg);
}
async function saveProfile() {
  const p = readForm();
  setDialogError("settings-modal", "");
  $("pf-stale").hidden = true;
  if (!p.name || !p.bin) { pfSaveError("Name and Command are required."); return; }
  const rowsErr = rowError(pfEnvRows);
  if (rowsErr) { pfSaveError(rowsErr); return; }
  // No permission ticked: shown on the GH block itself as well as in the dialog's line.
  const ghErr = ghFormError({ owners: readGhRows(), permissions: readGhPerms() });
  $("pf-gh-perm-err").textContent = ghErr;
  $("pf-gh-perm-err").hidden = !ghErr;
  if (ghErr) { pfSaveError(ghErr); return; }
  // A copy with no folder or no directory must never go out: the server would
  // read it as a plain profile. Shown on the block as well as in the dialog's line.
  const wmErr = wmFormError(pfMode(), { folder: pfCwdValue($("pf-cwd").value, $("pf-cwd-custom").value), copyDir: $("pf-copy-dir").value });
  $("pf-copy-err").textContent = wmErr;
  $("pf-copy-err").hidden = !wmErr;
  if (wmErr) { pfSaveError(wmErr); return; }
  // From the editor's state, not the typed name: a saved profile's name is
  // read-only, and a draft (New, Duplicate) always creates, so Save cannot
  // leave a copy behind.
  const target = saveTarget(selectedProfile, p.name, profiles.map((x) => x.name));
  if (target.error) { pfSaveError(target.error); return; }
  // Containment is the one field whose consequence is worth naming before it is
  // written. box needs no question; host gives up the whole point of the box.
  const ask = containmentConfirm(segValue($("pf-runtime")), p.name);
  if (ask && !(await askConfirm(ask))) return;
  // From here the save is the slow part (PUT, refetch, fill), so the button
  // shows "Saving…" if it runs long and a second click is ignored. Validation
  // and the confirm above stay outside: they answer at once or by the user.
  // withPending restores only what is still its own, and fillForm re-syncs the
  // button mid-save, so re-run the form's own sync afterwards: its owner
  // (syncEditorActions: saveLabel text, off/disabled) has the last word.
  try {
    await withPending($("pf-save"), "Saving…", async () => {
      const editing = target.method === "PUT";
      const path = editing ? "/api/profiles/" + encodeURIComponent(target.name) : "/api/profiles";
      $("pf-st-err").hidden = true;
      $("pf-awsc-err").hidden = true;
      $("pf-context-warn").hidden = true;
      $("pf-copy-warn").hidden = true;
      $("pf-copy-err").hidden = true;
      let saved = null;
      try {
        saved = await api(path, { method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(withSchema(editing ? withRev(p, pfLoaded) : p, pfSchema.get())) });
      } catch (e) {
        // A save from a form the server has moved past keeps what the user typed
        // and offers Load latest; no other refusal is cured by a reload, so the
        // rest fall through to the lines below.
        const stale = staleSave(e);
        if (stale) { showPfStale(stale); return; }
        // A create refused because the name is taken means the list on screen
        // was behind: refresh it, so the editor's own check knows the name too.
        if (e.code === "profile_exists") await loadProfiles(false);
        // The per-block lines first, the dialog's line (which scrolls) last: they sit
        // above it, so un-hiding one after the scroll would push it back off screen.
        // A refusal that names a token entry is also shown on the Tokens block.
        if (/^tokens?\b/.test(e.message) || (p.tokens || []).some((n) => String(e.message).includes(n))) {
          $("pf-st-err").textContent = String(e.message);
          $("pf-st-err").hidden = false;
        }
        // Likewise a refusal that names aws_connections (an undefined name, too
        // many, or both bindings at once) shows on the connections block.
        if (/aws_connections/.test(e.message)) {
          $("pf-awsc-err").textContent = String(e.message);
          $("pf-awsc-err").hidden = false;
        }
        // A refusal about the private copy's fields, worded with the labels on screen.
        const ce = wmErrText(pfMode(), e.message);
        if (ce) {
          $("pf-copy-err").textContent = ce;
          $("pf-copy-err").hidden = false;
        }
        pfSaveError("Save failed: " + e.message);
        return;
      }
      // Show what the server now holds, not what was typed: the editor and the
      // stored profile can only disagree on screen if this step is skipped. The
      // write succeeded, so a failure here must not say "Save failed".
      // The profile now exists, so the editor is no longer a draft even if the
      // reload below fails (a second Save must PUT, not POST again).
      selectedProfile = p.name;
      // The save answered with the revision of what it stored: hold it, so a re-read that
      // fails below does not leave the old one (the next Save would be refused for this save).
      pfLoaded = withSavedRev(editing ? (pfLoaded || p) : p, saved);
      try {
        // No background retry: one that succeeds later would leave the "Saved, but
        // reloading it failed" line on screen next to a good list.
        if (!(await loadProfiles(false))) throw new Error("the profile list could not be refreshed");
        fillForm(await api("/api/profiles/" + encodeURIComponent(p.name)));
      } catch (e) { setDialogError("settings-modal", "Saved, but reloading it failed: " + e.message); }
      renderProfileList();
      // After fillForm, which clears the warning: the save is done, and what the box
      // may read is the user's call, so the warnings stay on the field (a toast would
      // paint beneath the modal) for as long as the editor is showing this profile.
      const warn = contextWarningsText(saved);
      $("pf-context-warn").textContent = warn;
      $("pf-context-warn").hidden = !warn;
      const shareWarn = wmShareWarningsText(saved);
      $("pf-copy-warn").textContent = shareWarn;
      $("pf-copy-warn").hidden = !shareWarn;
      // Un-hidden with no scroll it can sit under the Save bar. "nearest" moves
      // nothing when it is already on screen.
      if (shareWarn) $("pf-copy-warn").scrollIntoView({ block: "nearest" });
    });
  } finally {
    syncEditorActions();
  }
}
// The banner for a save the server refused as stale.
function showPfStale(model) {
  $("pf-stale-msg").textContent = model.message;
  $("pf-stale").hidden = false;
  $("pf-stale").scrollIntoView({ block: "nearest" });
}
// Load latest: drop the edits and show what the server holds now. The list is
// refetched first, so a profile deleted meanwhile is noticed and the editor goes
// back to a blank draft instead of failing to load it. A load that fails says so
// and leaves the banner and the form as they were (fillForm, which a successful
// load runs, is what hides the banner), so the old revision stays and the next
// Save is refused as stale again, which is the safe side.
async function reloadStaleProfile() {
  const name = selectedProfileName();
  setDialogError("settings-modal", "");
  if (!(await loadProfiles(false))) {
    setDialogError("settings-modal", "Load latest failed: the profile list could not be refreshed. Your edits are still here.");
    return;
  }
  if (name && profiles.some((x) => x.name === name)) {
    if (!(await editProfile(name))) return; // editProfile said why
  } else newProfile();
}
// Duplicate is what makes a read-only built-in useful: a copy you own, seeded
// with what it does. It opens an UNSAVED draft, as New profile does, so Save
// creates exactly one profile under whatever name is typed. Writing the copy up
// front left it behind when the user renamed it and saved.
function duplicateProfile() {
  const name = selectedProfileName();
  if (!name) return;
  const draft = duplicateDraft(readForm(), name, profiles.map((x) => x.name));
  // Two profiles writing one clone directory would share a .git: a duplicate made
  // from copy mode never keeps the source's copy dir. Decided on the form's mode,
  // not on draft.clone_of: with the folder field cleared clone_of is omitted but
  // default_cwd (the source's clone dir) would still ride along.
  if (pfMode() === "copy") {
    const fresh = draft.clone_of ? wmCopyDirSuggestion(draft.clone_of, window.HOME_DIR || "", profiles.map((x) => x.default_cwd).filter(Boolean)) : "";
    if (fresh) draft.default_cwd = fresh;
    else delete draft.default_cwd;
  }
  selectedProfile = "";
  fillForm(draft);
  // fillForm took the saved mode and copy dir from an unsaved draft; nothing is
  // saved yet, so no "your private copy stays on disk" note may name a directory
  // that was never made.
  pfSavedMode = "folder";
  pfSavedCopyDir = "";
  syncPfMode();
  setDialogError("settings-modal", "");
  $("settings-modal").classList.add("editing");
  renderProfileList();
  syncEditorActions();
  $("pf-name").focus();
  $("pf-name").select();
}
// profilePageHolds: the desktop profile page holds unsaved block changes and is asking whether to leave them
// (profilepage.js pfvHold). true = the caller stops; `again` runs when the answer is Discard. Always false on a
// phone, where the page does not exist.
function profilePageHolds(again) {
  return typeof pfvHold === "function" && pfvHold(again);
}
async function editProfile(name) {
  setDialogError("settings-modal", "");
  selectedProfile = name;
  // Same drill-down as newProfile(); inert above 700px, where both columns
  // are already on screen.
  $("settings-modal").classList.add("editing");
  let loaded = true;
  try { fillForm(await api("/api/profiles/" + encodeURIComponent(name))); openSettings("manage"); }
  catch (e) { setDialogError("settings-modal", "Load failed: " + e.message); loaded = false; }
  renderProfileList();
  return loaded;
}
async function deleteProfile(name) {
  // The sibling destructive action (kill session) has always confirmed; this one
  // used to delete on the first click.
  const ok = await deleteProfileAsk(name);
  if (!ok) return;
  setDialogError("settings-modal", "");
  try { await api(deletePath(name, pfLoaded), { method: "DELETE" }); }
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
    // Where a read-only profile came from (built-in, config.json), by its `source`.
    const labels = profileListLabels(p);
    if (labels.badge) {
      const lk = document.createElement("span");
      lk.className = "lk";
      lk.textContent = labels.badge;
      b.appendChild(lk);
    }
    // Work that has not come home sits on the name's line, at every width.
    const nhLabel = notHomeLabel(p.not_home);
    if (nhLabel) {
      const nh = document.createElement("span");
      nh.className = "pnothome";
      nh.textContent = nhLabel;
      nh.title = notHomeTitle(p.not_home);
      b.appendChild(nh);
    }
    b.onclick = () => editProfile(p.name);
    li.appendChild(b);
    return li;
  }));
  // The desktop page lists the profiles in its nav instead (profilepage.js).
  if (typeof pfvRenderNav === "function") pfvRenderNav();
}
$("pf-save").addEventListener("click", saveProfile);
// newProfile() also runs once at boot (below) to seed the editor pane with a
// blank form before any profile is loaded — that call must NOT drill into the
// mobile editor view, so the class is added here, at the actual button press,
// rather than inside newProfile() itself.
$("pf-new").addEventListener("click", () => {
  if (profilePageHolds(() => $("pf-new").click())) return;
  // The desktop page has its own New profile page (pfv-new.js); the blank form below is the phone's.
  if (typeof pfvOnPage === "function" && pfvOnPage() && typeof pfvNew === "function") { pfvNew(); return; }
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
$("pf-stale-reload").addEventListener("click", reloadStaleProfile);
// The GitHub hint follows the GitHub and Tokens blocks, so every edit re-derives
// the work-mode display (syncPfMode is display only and cheap).
for (const ev of ["input", "change", "click"]) $("manage").querySelector(".ped").addEventListener(ev, syncPfMode);
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
// Only the hint follows typing: re-running the row rule here would hide the row
// under the cursor the moment its last path is deleted on the host tier.
$("pf-context").addEventListener("input", () => {
  $("pf-context-hint").hidden = !contextRowState(segValue($("pf-runtime")), $("pf-context").value).hint;
});
$("pf-egress-open").addEventListener("change", () => {
  $("pf-egress-open-warn").hidden = !$("pf-egress-open").checked;
});
$("pf-bin").addEventListener("input", () => {
  renderPfOptions(readForm().options);
  syncPfReplyable();
});
$("pf-cwd").addEventListener("change", () => {
  $("pf-cwd-custom").hidden = $("pf-cwd").value !== "__custom__";
  pfFillCopyDir();
  syncPfMode();
});
$("pf-cwd-custom").addEventListener("change", () => { pfFillCopyDir(); syncPfMode(); });
for (const r of document.querySelectorAll('input[name="pf-mode"]')) {
  r.addEventListener("change", () => {
    pfFillCopyDir();
    syncEditorActions();
  });
}
$("pf-copy-gh-link").addEventListener("click", (e) => {
  e.preventDefault();
  $("pf-gh-block").scrollIntoView({ block: "center" });
  $("pf-gh-add").focus();
});
$("pf-account").addEventListener("change", () => { syncPfAccountWarn(); paintPfImports(); });
$("pf-imports").addEventListener("change", paintPfImports);
// pf-runtime's own change is handled by renderPfRuntime's buildSeg onChange —
// a .seg is buttons, not a form control, so it has no native "change" event.
// workspacesCache backs the tier-aware re-render: switching Runtime must be able
// to re-evaluate which directories are selectable without another round trip.
let workspacesCache = [];

async function loadWorkspaces() {
  try {
    workspacesCache = (await api("/api/workspaces")) || [];
    // Refresh whatever depends on it: the picker's idle rows/caption (it skips
    // them while a fuzzy query is being searched, so a search in flight is never
    // clobbered by a background workspaces refresh) and the unboxable-workspace
    // warning, which looks workspacesCache up by path.
    if (spawnWsPicker) spawnWsPicker.refresh();
    updateRuntimeWarning();
  } catch (_) {}
}

// --- spawn workspace picker (D5) --------------------------------------------
// The New session dialog's Workspace field is one createWsPicker (wspicker.js),
// the same factory the profile page's Workspace block builds for itself. The
// reasoning for why the picker's state is its own and not dirpicker.js's
// module globals, and why value() is the resolved folder and never the raw
// search text, lives with the factory.
let spawnWsPicker = null;

// Where the field's prefilled value came from (spawnCwdChoice's source: "profile", "last", "default") and what that value
// was. On the desktop the help line under the field names the source for as long as the field still holds that value
// (frames M4.1 and M4.2); a pick or typing something else drops it. "" is no claim (a Duplicate's folder, a carried-over one).
let spawnWsSource = "", spawnWsPrefill = "";

// On the desktop (frame M4.3) the list sits inside the dialog under the field, "Last used" first; on the phone it is
// the fixed-position list it always was.
function initSpawnWsPicker() {
  const input = $("spawn-ws-input");
  const list = $("spawn-ws-list");
  if (!input || !list) return;
  spawnWsPicker = createWsPicker({
    input, list, onPick: onSpawnCwdChanged, onNote: setSpawnWsNote,
    inline: drawerDesktop,
    lastUsed: () => { const l = loadLastSpawn(); return l && typeof l.cwd === "string" ? l.cwd : ""; },
    idleText: spawnWsCaption,
    filter: (path) => { const f = spawnVMFilterFor(segValue($("runtime-seg")), spawnVMFilter); return f ? f(path) : true; },
  });
  input.addEventListener("input", hideSpawnNotShared);
}

// On a Mac the picker offers only folders the box VM can see (applied rw shares from /api/vm-folders). null = no filtering: not a
// Mac, the list could not be read, or the VM sees everything. The server still enforces either way.
let spawnVMFilter = null;
const spawnVMLatest = spawnLatest();
// syncSpawnVMFilter: the filter and its help line follow the selected runtime (spec I4): a host session needs no VM folder. An open
// list is redrawn, so a filter that arrives (or a runtime that changes) after the list opened shows at once.
function syncSpawnVMFilter() {
  const help = $("spawn-ws-vmhelp");
  const on = !!spawnVMFilterFor(segValue($("runtime-seg")), spawnVMFilter);
  if (help) {
    if (on) help.textContent = SPAWN_VM_SEES_HELP;
    help.hidden = !on;
  }
  if (spawnWsPicker) spawnWsPicker.refresh();
}
async function loadSpawnVMFilter() {
  const mine = spawnVMLatest.next();
  spawnVMFilter = null;
  syncSpawnVMFilter();
  if (!(globalThis.vmFoldersOn === true)) return;
  let view = null;
  try { view = await api("/api/vm-folders"); } catch (_) { return; }
  if (!spawnVMLatest.current(mine)) return; // a newer open of the dialog asked again: this answer is stale
  spawnVMFilter = vmShareFilter(view);
  syncSpawnVMFilter();
}

// The M9.9 refusal under the Workspace field: the line, and a "Share this folder…" button that closes the dialog and opens
// Settings > VM folders with the add card prefilled (M9.9b). Editing the field clears it.
function showSpawnNotShared(body, closeSpawn) {
  const box = $("spawn-notshared"), input = $("spawn-ws-input");
  if (!box) return;
  const b = body || {};
  const row = $("spawn-ws-row");
  if (row) row.classList.add("ns-on"); // the caption line gives way: input, error, button, help (M9.9)
  const line = document.createElement("div");
  line.className = "field-error spawn-ns-line";
  if ((b.pending === true || b.read_only === true) && b.error) line.textContent = spawnNotSharedLine(b);
  else {
    const code = document.createElement("code");
    code.textContent = b.path || "";
    line.append(code, SPAWN_NOT_SHARED_TAIL);
  }
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn btn-sm";
  btn.textContent = "Share this folder…";
  btn.onclick = () => {
    closeSpawn();
    openSettings("vmfolders");
    openVMFoldersAdd(b.path || "", spawnShareMode(b), "spawn");
  };
  box.replaceChildren(line, btn);
  box.hidden = false;
  if (input) { input.classList.add("is-invalid"); input.setAttribute("aria-invalid", "true"); }
}
function hideSpawnNotShared() {
  const box = $("spawn-notshared"), input = $("spawn-ws-input");
  if (box && !box.hidden) { box.hidden = true; box.replaceChildren(); }
  const row = $("spawn-ws-row");
  if (row) row.classList.remove("ns-on");
  if (input) { input.classList.remove("is-invalid"); input.removeAttribute("aria-invalid"); }
}

// spawnWsCaption: the steady-state line under the field: the plain "N project roots · type to filter · ↵ to pick", with
// where the value came from in front of it while the field holds that value, on the desktop only.
function spawnWsCaption(count) {
  const named = drawerDesktop() && spawnWsSource !== "" && spawnWsPicker && spawnWsPicker.value() === spawnWsPrefill;
  return spawnWsNoteFor(named ? spawnWsSource : "", count);
}

// setSpawnCwd writes the canonical cwd and the field's displayed value together
// — a pick, a resolved path, a profile's default_cwd, or the last-spawn
// fallback. It does not call onSpawnCwdChanged: its callers do. `source` is
// where a prefilled value came from (spawnCwdChoice); no source claims nothing.
function setSpawnCwd(path, source) {
  spawnWsSource = source || "";
  spawnWsPrefill = path || "";
  if (spawnWsPicker) spawnWsPicker.set(path);
}
// getCwd is the picker's value: the resolved directory, never the half-typed query.
function getCwd() { return spawnWsPicker ? spawnWsPicker.value() : ""; }
// Called on every cwd change (typing an absolute path, a pick, a resolve, a
// profile default, the last-spawn fallback) so the box/host copy and the
// unboxable-workspace warning never lag behind what will actually be spawned.
function onSpawnCwdChanged() {
  hideSpawnNotShared();
  updateTierExplain();
  updateRuntimeWarning();
}

// The picker's caption under the field (its own idle line, or what a search found).
function setSpawnWsNote(text) {
  const el = $("spawn-ws-note");
  if (el) el.textContent = text || "";
}
// updateTierExplain names the ACTUAL working dir in the box/host copy line
// (D5: "box — the agent sees only ~/culishop."), not a generic placeholder.
// Called everywhere the cwd can change: typing an absolute path, a pick, a
// resolve, a profile's default_cwd landing (applyProfileCwd), and a tier
// switch (both via onSpawnCwdChanged/updateRuntimeWarning).
function updateTierExplain() {
  const cwdEl = $("tier-explain-cwd");
  if (!cwdEl) return;
  const cwd = getCwd();
  cwdEl.textContent = cwd ? shortPath(cwd) : "the working dir";
}

// boxRuntime is the isolation runtime the daemon detected ("runc"/"sysbox-runc"),
// or "" when containment is unavailable. Empty means every session is host tier,
// and the tier chooser is hidden rather than offering a choice that does nothing.
let boxRuntime = "";
// boxUnavailableReason: why boxRuntime is "" when the containment layer failed
// to build (/api/meta box_unavailable_reason, a plain sentence such as "Docker
// is not installed"), "" otherwise. It only explains; the profile page reads it.
let boxUnavailableReason = "";
// boxDaemonReason: why a box layer that EXISTS cannot be used right now (/api/meta
// box_daemon_reason, a plain sentence such as "Docker is not running"), "" when it
// can. Unlike the reason above, boxRuntime stays set, so no default flips to the host
// tier; the pages only explain.
let boxDaemonReason = "";
// bundleImports: /api/bundle/imports exists on this instance (/api/meta
// bundle_imports). The route is only registered when the CLAUDE.md bundle is on,
// so with this false the page does not ask (it would answer 404 on every load).
let bundleImports = false;
// cloneUnavailable: why this instance cannot run private copies (/api/meta
// clone_unavailable), "" when it can. It only explains; a profile that is a
// copy still opens, saves and shows as one (wmCardState).
let cloneUnavailable = "";
// metaLoaded: /api/meta has answered at least once. boxRuntime is "" both for a
// box-less instance and before meta loads, so "no containment here" may only be
// concluded after a positive answer (see readForm); until then it fails closed.
let metaLoaded = false;

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
  // The new session's pane must be on screen, not behind a page. A page that asks about unsaved changes is answered
  // BEFORE anything starts: Keep editing spawns nothing (null; onCreated does not run, so the spawn dialog stays open
  // with its form) and the question is never asked a second time when openTerminal closes the page again. With nothing
  // to ask closePage answers true and the POST goes out in this same tick.
  const left = closePage();
  if (left !== true && (left === false || !(await left))) return null;
  const s = await api("/api/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  if (opts.onCreated) opts.onCreated(s);
  try {
    localStorage.setItem(LAST_SPAWN_KEY, JSON.stringify({
      agent: req.agent, cwd: req.cwd || "", options: req.options || {},
    }));
  } catch (_) {}
  {
    const st = deskStorage();
    writeRecent(st, pushRecentProfile(readRecent(st), req.agent));
  }
  await refreshSessions();
  openTerminal(s.id, s.name);
  if (opts.toast !== false) {
    // Report the directory the SERVER chose, not the one requested.
    const where = s.cwd || req.cwd;
    showToast((mobileActive(window.innerWidth) ? "Spawned" : "Started") + ' "' + (s.name || s.id) + '" in ' + (where ? shortPath(where) : "the default dir") + toastDropsSuffix(s.bundle_drops) + toastCloneSuffix(s) + toastReturnSuffix(s),
      { undo: () => killSession(s.id), warn: cloneLevel(s.clone) === "warn" });
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
  const row = $("runtime-row"), seg = $("runtime-seg");
  if (!row || !seg) return;
  const shown = boxRuntime ? "" : "none";
  row.style.display = shown;
  // The box/host explainer only makes sense alongside an actual choice —
  // with no box layer every session is host tier anyway (see the comment
  // above), so it is hidden together with the row that offers the choice.
  const explain = $("tier-explain");
  if (explain) explain.style.display = shown;
  const value = spawnRuntimeDefault(currentProfile(), boxRuntime);
  buildSeg(seg, runtimeSegChoices(), value, updateRuntimeWarning);
  updateRuntimeWarning(); // whether the picked cwd is boxable depends on the tier too
}

// syncSpawnAccount offers the Agent account select for a claude profile when
// there is more than the default account to choose from. It starts on the
// Duplicate's account when one is given, else the profile's own (ruling C2:
// the default is the explicit "default", so picking it overrides a pinned
// profile for this run). Painted from the cache at once; the fetch behind it
// repaints only when the set of accounts changed, keeping the user's pick.
// openSpawnDialog sets #agent programmatically (no change event), so it calls
// this itself, like syncRuntimeField.
function syncSpawnAccount(prefillAccount) {
  const before = acctChoices(accountsCache, "").join(",");
  paintSpawnAccount(prefillAccount);
  fetchAccounts().then(() => {
    if (acctChoices(accountsCache, "").join(",") === before) return;
    paintSpawnAccount($("spawn-account-row").hidden ? prefillAccount : $("spawn-account").value);
  });
}
function paintSpawnAccount(prefillAccount) {
  const row = $("spawn-account-row"), sel = $("spawn-account");
  const prof = currentProfile();
  const pick = acctSpawnPick(prefillAccount, prof && prof.account);
  const names = acctChoices(accountsCache, pick);
  if (!prof || !prof.claude || names.length < 2) {
    row.hidden = true;
    sel.replaceChildren();
    return;
  }
  const lims = acctLimitsMap(lastRateLimits);
  const now = Math.floor(Date.now() / 1000);
  sel.replaceChildren(...names.map((n) => {
    const o = document.createElement("option");
    o.value = n;
    o.textContent = acctOptionLabel(n, lims[n], now);
    return o;
  }));
  sel.value = pick;
  row.hidden = false;
  syncSpawnPreview(); // the reach box's Account row reads the picked account
}

// syncSpawnExtras shows the first-message field and the replies toggle only
// for a profile whose agent takes them, and keeps the bypass warning honest.
// resetReplies re-reads the toggle from the profile's default (dialog open,
// profile change); an option/checkbox change keeps what the operator set.
// openSpawnDialog sets #agent programmatically, which fires no change event,
// so it calls this itself (memory profile-default-cwd-built).
function syncSpawnExtras(resetReplies) {
  const p = currentProfile();
  $("spawn-prompt-row").hidden = !promptFieldVisible(p);
  $("spawn-replies-row").hidden = !repliesFieldVisible(p);
  if (resetReplies) $("spawn-replyable").checked = !!(p && p.replyable);
  $("spawn-bypass-warn").hidden = !bypassWarningShown(p, getOptions(), $("spawn-replyable").checked);
}

// applySpawnPrefill fills the dialog from a session (Duplicate): cwd, tier and
// this run's option values, over the profile defaults openSpawnDialog set.
function applySpawnPrefill(pf) {
  if (pf.cwd) setSpawnCwd(pf.cwd);
  const seg = $("runtime-seg");
  if (pf.runtime && boxRuntime && seg) setSegValue(seg, pf.runtime);
  for (const ctrl of $("spawn").querySelectorAll("[data-key]")) {
    const k = ctrl.dataset.key;
    if (!Object.prototype.hasOwnProperty.call(pf.options, k)) continue;
    const v = pf.options[k];
    if (ctrl.dataset.type === "bool") ctrl.checked = v === true;
    else if (ctrl.classList.contains("seg")) setSegValue(ctrl, String(v));
    else ctrl.value = String(v);
  }
  updateRuntimeWarning();
  syncSpawnExtras(false);
}

// spawnPromptDraft keeps a first message whose spawn failed, so reopening the
// dialog does not make the operator retype it.
let spawnPromptDraft = "";

// updateSpawnBoxOwned: under the Workspace field, before submit, when a host-tier session is pointed at a box's
// private copy (a folder a clone-mode profile's box writes in): the server refuses it (box.ErrBoxOwned) and that
// stays; this says why and which folder to use instead. Same field and tier as the refusal, on desktop and phone.
function updateSpawnBoxOwned() {
  const el = $("spawn-ws-owned");
  if (!el) return;
  const host = (metaLoaded && !boxRuntime) || segValue($("runtime-seg")) === "host";
  const hit = spawnBoxOwnedHit(getCwd(), host ? "host" : "box", profiles);
  el.textContent = hit ? spawnBoxOwnedLine(hit) : "";
  el.hidden = !hit;
}

// updateRuntimeWarning covers two DIFFERENT reasons #runtime-warn can be
// showing, and they are mutually exclusive:
//   host  — the generic "runs on your host" notice. Unrelated to which
//           directory is picked, since host is uncontained regardless.
//   box   — the picked workspace itself cannot be the box's one writable
//           mount (home, an ancestor of home, or "/"). The old <select>
//           simply disabled these entries so this state was unreachable; the
//           free-text field can no longer prevent typing/picking one, so this
//           is the reactive replacement — named after the fact, not blocked.
// Either way this is advisory only: the actual gate is the server's own
// refusal (box.ErrUnboxableWorkspace, surfaced by handleSessions in api.go),
// which still runs regardless of what this function decides to show.
function updateRuntimeWarning() {
  syncSpawnPreview(); // tier or workspace changed: the desktop reach box follows
  syncSpawnVMFilter(); // a box lists only shared folders; a host session lists them all
  updateSpawnBoxOwned();
  const seg = $("runtime-seg"), warn = $("runtime-warn");
  if (!seg || !warn) return;
  if (drawerDesktop() && metaLoaded && !boxRuntime) {
    // No box layer: the hidden seg holds nothing, so "cannot be boxed" below would blame a folder for a tier no session
    // is on. The dialog's own boxes-unavailable line (syncSpawnGate) says what is true (#142). The phone is as it was.
    warn.hidden = true;
    return;
  }
  if (segValue(seg) === "host") {
    warn.hidden = false;
    warn.textContent = "Runs on your host with your user's full access: files, keys, network.";
    return;
  }
  const cwd = getCwd();
  // A cwd matching one of the depth-1 entries /api/workspaces enumerates
  // already carries the server's own boxable/reason (see workspaceOptions) —
  // prefer that over the heuristic, which knows nothing about, say, a
  // symlinked child of home the server resolved differently.
  const known = cwd ? workspacesCache.find((w) => w.path === cwd) : null;
  const reason = known ? (!known.boxable && known.reason) : (cwd ? unboxableCwdReason(cwd, window.HOME_DIR || "") : null);
  if (reason) {
    const label = known ? known.label : shortPath(cwd);
    warn.textContent = "⚠ " + label + " cannot be boxed — " + reason + ". Pick a project root, or choose host deliberately.";
    warn.hidden = false;
  } else {
    warn.hidden = true;
  }
}

// openSpawnDialog preselects whatever the one-click button would have done, so
// "Advanced…" elaborates the visible target instead of presenting a fresh form.
// prefill (Duplicate, see spawnPrefill) is applied last, over those defaults —
// only when its profile still exists.
// It refetches profiles first: the form sends runtime/options/account/replyable
// from profiles[], and the request value overrides the server's profile, so a
// stale copy would silently revert an edit made from another tab. A failed
// refresh still opens (the old copy is usable) but says so.
// A second open while the first awaits the refetch would run the body twice, and
// the second pass would re-apply profile defaults over what was typed meanwhile.
let spawnOpening = false;
// refreshBoxMeta re-reads the box fields of /api/meta, as the dialog opens and before a daemon-down block refuses a Spawn. A box profile's
// Spawn is blocked from them (#142), so a page loaded while Docker was down must not keep blocking after the server has re-checked it
// (box_daemon_reason clears within seconds). Only the box fields, and only once the page's own first read has landed (initEnv sets the
// rest of the meta). A failed or slow read keeps what the page has.
async function refreshBoxMeta() {
  if (!metaLoaded) return;
  try {
    const meta = await api("/api/meta", { signal: deadlineSignal(5000) });
    if (!meta || typeof meta !== "object") return;
    boxRuntime = meta.box_runtime || "";
    boxUnavailableReason = meta.box_unavailable_reason || "";
    boxDaemonReason = meta.box_daemon_reason || "";
  } catch (_) { /* the values the page already holds */ }
}
async function openSpawnDialog(preselectAgent, prefill) {
  if (spawnOpening) return;
  spawnOpening = true;
  let fresh;
  try {
    // In parallel, so the phone's open is not slowed by a read it does not use.
    const metaRead = refreshBoxMeta();
    fresh = await loadProfiles(false);
    await metaRead;
  } finally {
    spawnOpening = false;
  }
  const last = loadLastSpawn();
  // An explicit profile (palette, Start, Duplicate) is honoured as asked; the
  // default pick is the last used, never the quick-shell profile.
  const want = preselectAgent && profiles.some((p) => p.name === preselectAgent)
    ? preselectAgent
    : pickSpawnProfile(profiles, last, window.shellProfile || "");
  if (want) {
    $("agent").value = want;
    renderOptions();
  }
  // The seg's own list never changes here (only which button is active
  // might), but rebuilding against the just-set #agent.value is simplest and
  // cheap — a handful of DOM nodes, rebuilt on every dialog open already.
  renderProfileSeg();
  // Tier first: it decides which workspaces are boxable, for the warning below.
  syncRuntimeField();
  hideSpawnNotShared();
  loadSpawnVMFilter(); // not awaited: the picker reads the filter when it draws
  // A Duplicate's account applies only where its prefill does (same profile).
  syncSpawnAccount(prefill && want === prefill.agent ? prefill.account : undefined);
  $("spawn-prompt").value = spawnPromptDraft;
  spawnPromptDraft = "";
  syncSpawnExtras(true);
  // Fresh open: don't carry a stale pick across dialog sessions. In priority
  // order (spawnCwdChoice): the profile's default_cwd, the last spawn's directory
  // only when that spawn was of THIS profile (another profile's leftover is never
  // prefilled) and, on the desktop only, not a scratch folder under /tmp (the phone
  // keeps its prefill as it was), then the first workspace usable on the tier. A
  // last cwd that is unusable on this tier is still restored; the warning below
  // names why, so choosing box vs. host stays the user's call, not a silent discard.
  // The source rides along: the help line under the field names it.
  const choice = spawnCwdChoice({ profile: currentProfile(), last, workspaces: workspacesCache, tier: segValue($("runtime-seg")), skipScratch: drawerDesktop() });
  setSpawnCwd(choice.cwd, choice.source);
  if (prefill) {
    if (want === prefill.agent) applySpawnPrefill(prefill);
    else showToast('Profile "' + prefill.agent + '" no longer exists — pick one', { error: true });
  }
  onSpawnCwdChanged();
  if (spawnWsPicker) spawnWsPicker.showIdleNote();
  hideSpawnUnlock();
  spawnPlistResetSearch(); // a fresh open starts with an empty profile filter
  spawnPanelOpen();
  paintSpawnChrome();
  openDialog("spawn-modal");
  setDialogError("spawn-modal", fresh ? "" : "Could not refresh profiles — showing the settings loaded earlier. Reopen to retry.");
  $("sess-name").focus();
}

// --- desktop empty states (spec 5.9) -----------------------------------------
// What the pane area says when no pane has a tab: "none" (sessions exist; the
// recent profiles to start from) or "first" (a fresh install: three steps).
// Everything profile- or path-derived goes in through textContent.
function emptyStateInput() {
  return {
    // Unknown sessions (null) keep the model silent: a built-in-only user with
    // live sessions must not see the first-run screen while the poll is out.
    sessions: sessionsLoaded && bootRestoreDone ? lastSessions : null,
    profiles: profilesLoaded ? profiles : null,
    recent: readRecent(deskStorage()),
  };
}

function emptyEl(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function emptyKbd(text) { return emptyEl("span", "empst-kbd", text); }

function emptyHint() {
  const hint = emptyEl("div", "empst-hint");
  // One key cap per glyph on macOS (⇧ ⌘ K), per "+"-separated name elsewhere.
  const chord = shortcutHint("palette.open", IS_MAC);
  const pal = emptyEl("span");
  for (const k of IS_MAC ? Array.from(chord) : chord.split("+")) pal.append(emptyKbd(k), " ");
  pal.append("command palette");
  hint.append(pal, emptyEl("span", "", "All profiles in New session"));
  return hint;
}

function emptyNoneEl(m) {
  const root = emptyEl("div", "placeholder empst");
  root.append(
    emptyEl("h1", "empst-title", "No session open"),
    emptyEl("p", "empst-sub", "Pick a session on the left, or start an agent from a profile you used recently."),
  );
  if (m.profiles.length) {
    const rows = emptyEl("div", "empst-rows");
    for (const name of m.profiles) {
      const p = profiles.find((x) => x.name === name);
      if (!p) continue;
      const row = emptyEl("div", "empst-row");
      const start = emptyEl("button", "btn", "Start session");
      start.type = "button";
      start.onclick = () => openSpawnDialog(name);
      // What the profile reaches, in the words of the New session list (one function draws both).
      row.append(emptyEl("b", "empst-name", name), start, emptyEl("small", "empst-touch", profileTouchLine(p, metaLoaded ? boxRuntime : undefined)));
      rows.append(row);
    }
    root.append(rows);
  }
  root.append(emptyHint());
  return root;
}

function emptyFirstEl() {
  const root = emptyEl("div", "placeholder empst empst-first");
  root.append(
    emptyEl("h1", "empst-title", "Welcome to CuliBox"),
    emptyEl("p", "empst-sub", "Run AI agents, keep them contained, and let them touch your cloud safely. Three steps:"),
  );
  const docker = !metaLoaded ? "" : (boxRuntime ? " (found)" : " (not found)");
  const step = (n, title, desc, label, cls, run) => {
    const el = emptyEl("div", "empst-step");
    const btn = emptyEl("button", cls, label);
    btn.type = "button";
    btn.onclick = run;
    el.append(emptyEl("span", "empst-n", String(n)), emptyEl("b", "empst-t", title), btn, emptyEl("small", "empst-d", desc));
    return el;
  };
  const steps = emptyEl("div", "empst-steps");
  // Desktop only (the phone never shows this node; the guard keeps it so). Metadata has answered and there is no
  // box layer: step 2 keeps its D18 copy and adds the server's plain reason (a text node), the install link and a
  // disabled button (DD§A11, DC§9). Nothing is claimed before /api/meta answers, and an empty reason draws no line.
  const noBox = drawerDesktop() && metaLoaded && !boxRuntime;
  const step2 = step(2, "Put it in a box", "A container that sees only its workspace. Needs Docker" + docker + ".", "Create box profile", "btn",
    // The same press as "+ new profile" in Settings: one profile form. A new
    // profile already opens on the box tier.
    () => { openSettings("manage"); $("pf-new").click(); });
  if (noBox) {
    const btn = step2.querySelector("button");
    btn.onclick = null;
    // aria-disabled + title, as every desktop control that cannot act yet (setDisabledReason): the reason stays hoverable.
    setDisabledReason(btn, true, boxUnavailableReason, true);
    const why = emptyEl("div", "empst-why");
    if (boxUnavailableReason) why.append(emptyEl("span", "empst-reason", boxUnavailableReason));
    const a = emptyEl("a", "link", "How to install Docker");
    a.href = "https://docs.docker.com/engine/install/";
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    why.append(a);
    step2.append(why);
  }
  if (drawerDesktop() && metaLoaded && boxRuntime && boxDaemonReason) {
    // The layer exists (Docker is installed) but its daemon cannot be used right now (#154): the
    // button stays enabled, since a profile can still be made, and the server's plain reason says why
    // a box cannot start. A text node, like the no-layer reason above; no install link.
    const why = emptyEl("div", "empst-why");
    why.append(emptyEl("span", "empst-reason", "Boxes can't start right now: " + boxDaemonReason + "."));
    step2.append(why);
  }
  steps.append(
    step(1, "Start an agent", "Claude Code on this machine, in a folder you choose. Works now.", "New session", "pbtn", () => openSpawnDialog()),
    step2,
    step(3, "Connect your cloud", "AWS SSO, or GitHub (an App or a token). Read-only by default; you choose where agents may write.", "Add credentials", "btn",
      () => openSettings("creds")),
  );
  root.append(steps, emptyEl("div", "empst-hint", "Settings \u2192 Profiles \u00b7 Credentials"));
  return root;
}

// desktopEmptyState: the node renderGrid shows in place of the panes, or null
// (mobile, a pane has a tab, or nothing is known yet: the pane's own "Click a
// session" note then stands in).
function desktopEmptyState() {
  emptyShownKey = "";
  if (!drawerDesktop() || cells.some((c) => c.tabs.length)) return null;
  const m = emptyStateModel(emptyStateInput());
  if (!m.kind) return null;
  emptyShownKey = emptyStateKey(m);
  return m.kind === "first" ? emptyFirstEl() : emptyNoneEl(m);
}

function emptyStateKey(m) {
  return JSON.stringify([m.kind, metaLoaded, !!boxRuntime, boxRuntime ? boxDaemonReason : boxUnavailableReason, m.profiles.map((n) => {
    const p = profiles.find((x) => x.name === n);
    return [n, p ? profileTouchLine(p, metaLoaded ? boxRuntime : undefined) : ""];
  })]);
}

// refreshEmptyState: the poll, a profile load or the meta answer changed what
// the empty state should say; repaint only when it did.
function refreshEmptyState() {
  if (!drawerDesktop() || cells.some((c) => c.tabs.length)) return;
  const m = emptyStateModel(emptyStateInput());
  const key = m.kind ? emptyStateKey(m) : "";
  if (key !== emptyShownKey) renderGrid();
}

// paintSpawnChrome: the parts of the New session dialog whose words depend on the layout. On the desktop (frame
// M4.1) the submit says "Start session", the footer hint is the chord as key chips and "to start", and the Name label's
// "(optional)" is muted; the phone keeps "Spawn ⏎", its text hint (app.css hides it) and its one text node. The
// desktop's label is built here, not in index.html, so the phone's markup stays what it was. Called when the dialog
// opens (the window may have changed size since boot) and at boot.
function paintSpawnChrome() {
  const desktop = drawerDesktop();
  const go = $("spawn").querySelector('button[type="submit"]');
  if (go && !go.disabled) go.textContent = spawnSubmitLabel(desktop);
  const foot = $("spawn-foot-hint");
  if (foot) {
    if (desktop) {
      const hint = spawnFootHint(IS_MAC);
      const keys = hint.keys.map((k) => { const c = document.createElement("kbd"); c.className = "kbd"; c.textContent = k; return c; });
      foot.replaceChildren(...keys, document.createTextNode(hint.text));
    } else {
      foot.textContent = shortcutHint("spawn.submit", IS_MAC) + " to spawn";
    }
  }
  const name = document.querySelector('#spawn-name-row > label[for="sess-name"]');
  if (name) {
    if (desktop) {
      const opt = document.createElement("span");
      opt.className = "opt";
      opt.textContent = "(optional)";
      name.replaceChildren("Name ", opt);
    } else {
      name.textContent = "Name (optional)";
    }
  }
}

function initSpawnControl() {
  // The .seg's own buildSeg onChange (wired in syncRuntimeField) already calls
  // updateRuntimeWarning on every click — a .seg is buttons, not a form
  // control, so there is no native "change" event to hook here.
  // openSpawnDialog awaits loadProfiles (up to 5s), so a slow open reads
  // "Opening…" on the button instead of looking dead. Both are persistent
  // buttons; the sidebar menu item has none, so it stays unwrapped.
  $("spawn-go").onclick = () => withPending($("spawn-go"), "Opening…", () => openSpawnDialog());
  // the sidebar top's button: the same flow as "+ new"
  $("side-new").onclick = () => withPending($("side-new"), "Opening…", () => openSpawnDialog());
  $("spawn-replyable").onchange = () => syncSpawnExtras(false);
  $("spawn-account").addEventListener("change", syncSpawnPreview);
  $("spawn-prompt-hint").textContent = shortcutHint("spawn.submit", IS_MAC) + " spawns";
  paintSpawnChrome();
  // From any field of the form (it bubbles up from the first message, the name,
  // the selects). A button keeps its own Enter. The submit handler spawns only
  // when the workspace field shows the directory in use.
  $("spawn").addEventListener("keydown", (e) => {
    if (!isSubmitHotkey(e, IS_MAC)) return;
    if (e.target && (e.target.tagName === "BUTTON" || (e.target.closest && e.target.closest("#spawn-unlock")))) return;
    e.preventDefault();
    $("spawn").requestSubmit();
  });
}

// spawnBusy: a spawn POST is in flight. The dialog now stays open until the
// server answers (a refused spawn must be able to show its unlock form in
// place), so a second submit would start a second session.
let spawnBusy = false;
// The Keychain refusal text the spawn dialog's error line is showing, if it is.
let spawnUnavailMsg = "";

// hideSpawnUnlock clears the inline unlock block. Called on every dialog open
// and on Retry, so a stale "store unlocked" never outlives its dialog.
function hideSpawnUnlock() {
  const box = $("spawn-unlock");
  if (!box) return;
  box.hidden = true;
  box.replaceChildren();
}

// showSpawnUnlock: a spawn was refused with 423 store_locked. The dialog stays
// open with the unlock form inline; after a 204 it offers Retry, which submits
// the same form again.
function showSpawnUnlock(message) {
  const box = $("spawn-unlock");
  if (!box) return;
  const txt = document.createElement("div");
  txt.className = "storetxt";
  txt.textContent = message;
  const slot = document.createElement("div");
  slot.className = "storeslot";
  renderUnlockForm(slot, () => {
    const done = document.createElement("div");
    done.className = "storeretry";
    const note = document.createElement("span");
    note.textContent = "Store unlocked.";
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "btn pri";
    retry.textContent = "Retry";
    retry.onclick = () => { hideSpawnUnlock(); $("spawn").requestSubmit(); };
    done.append(note, retry);
    slot.replaceChildren(done);
    retry.focus();
    spawnCredsRefetch(); // the reach box's Store row clears
  });
  box.replaceChildren(txt, slot);
  box.hidden = false;
  refreshStore(); // the banner behind the dialog shows the same lock
}

$("spawn").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  if (spawnBusy) return;
  // The workspace field may show text that is not the directory in use: settle
  // it first, never spawn in the previously resolved one (#20).
  if (spawnHotkeyAction({ text: $("spawn-ws-input").value, cwd: getCwd(), listOpen: spawnWsPicker.isOpen(), highlighted: spawnWsPicker.highlighted() }) === "pick") {
    spawnWsPicker.settle();
    return;
  }
  spawnWsPicker.close(); // the field shows the directory in use; an open list is just left over
  // A box profile with no boxes (or a box whose daemon is down) is not sent: the server would refuse it. The reason is already
  // on the dialog; the error line repeats it so a press does not look dead. Desktop only: on the phone the gate is never blocked.
  let gate = syncSpawnGate();
  if (gate.blocked && !gate.noLayer) {
    // The daemon was down when the dialog opened; the server re-checks it live, so Docker may be back since. Ask again before
    // refusing, or the user is stuck until the dialog is reopened. Held busy meanwhile so a second press cannot start a second read.
    spawnBusy = true;
    try { await refreshBoxMeta(); } finally { spawnBusy = false; }
    gate = syncSpawnGate();
  }
  if (gate.blocked) {
    spawnGateErr = gate.text;
    setDialogError("spawn-modal", gate.text);
    return;
  }
  spawnBusy = true;
  const goBtn = $("spawn").querySelector('button[type="submit"]');
  if (goBtn) { goBtn.disabled = true; goBtn.classList.add("busy"); goBtn.setAttribute("aria-busy", "true"); goBtn.textContent = "Starting…"; }
  const req = spawnPayload({
    agent: $("agent").value, name: $("sess-name").value, cwd: getCwd(), options: getOptions(),
    runtime: segValue($("runtime-seg")), prompt: $("spawn-prompt").value,
    replyable: $("spawn-replyable").checked, profile: currentProfile(),
    account: $("spawn-account-row").hidden ? "" : $("spawn-account").value,
  });
  const draft = $("spawn-prompt").value;
  // The dialog closes the moment the session exists, before the toast: a
  // fixed-position toast renders *beneath* a modal dialog's backdrop, so an Undo
  // offered while the dialog is still up is unreachable.
  const closeSpawn = () => {
    closeDialog("spawn-modal");
    $("sess-name").value = "";
    $("spawn-prompt").value = "";
  };
  hideSpawnUnlock();
  hideSpawnNotShared();
  // A Keychain refusal left on the line by the previous try is stale now; any
  // other note there (profiles could not be refreshed) is not ours to clear.
  if (spawnUnavailMsg && $("spawn-err").textContent === spawnUnavailMsg) setDialogError("spawn-modal", "");
  spawnUnavailMsg = "";
  // The POST returns only once the session exists (#36): say it is working.
  const prog = $("spawn-progress");
  const rt = req.runtime || (currentProfile() || {}).runtime || "";
  const t0 = Date.now();
  const paintProg = () => { prog.textContent = spawnProgressText((Date.now() - t0) / 1000, rt); };
  paintProg();
  prog.hidden = false;
  const progTimer = setInterval(paintProg, 1000);
  try {
    await spawnSession(req, { onCreated: closeSpawn });
  } catch (e) {
    if (spawnRefusedLocked(e.status, e.code)) {
      showSpawnUnlock(e.message); // dialog and form values stay as they were
    } else if (spawnRefusedNotShared(e.status, e.code)) {
      showSpawnNotShared(e.body || { error: e.message }, closeSpawn); // dialog and form values stay as they were
    } else if (spawnRefusedRestarting(e.status, e.code)) {
      // The VM is restarting: the sentence says so; follow the apply so the sidebar banner appears.
      setDialogError("spawn-modal", e.message);
      if (typeof startVMApplyPoll === "function") startVMApplyPoll();
    } else if (spawnRefusedUnavailable(e.status, e.code)) {
      // Keychain failure: nothing to type. The sentence names the fix; the dialog
      // and form values stay as they were.
      spawnUnavailMsg = e.message;
      setDialogError("spawn-modal", e.message);
    } else if (spawnRefusedBoxUnavailable(e.status, e.code) && drawerDesktop()) {
      // The containment refusal (no box layer, the daemon went away since the page loaded, egress control down, ...): the
      // dialog stays open with the form as it was (name, first message, workspace, options) and the line says why in plain
      // words; the server's sentence is shown as it is when the page has nothing plainer. The phone keeps its toast (#142).
      spawnUnavailMsg = spawnBoxRefusalLine({ message: e.message, metaLoaded, boxRuntime, boxUnavailableReason, boxDaemonReason });
      setDialogError("spawn-modal", spawnUnavailMsg);
      refreshBoxMeta().then(syncSpawnPreview); // the reason may have changed since the page loaded; the block follows it
    } else {
      closeSpawn();
      spawnPromptDraft = draft; // reopening New session restores it
      // The desktop sentence drops toastFailText's closing period for the note, and
      // only when there is a note.
      const kept = draft.trim() ? " — your first message is kept" : "";
      const f = toastFailText("start the session", e);
      showToast("Spawn failed: " + e.message + kept, { error: true, human: kept ? f.text.replace(/\.$/, "") + kept : f.text, detail: f.detail });
    }
  } finally {
    clearInterval(progTimer);
    prog.hidden = true;
    spawnBusy = false;
    if (goBtn) { goBtn.disabled = false; goBtn.classList.remove("busy"); goBtn.removeAttribute("aria-busy"); goBtn.textContent = spawnSubmitLabel(drawerDesktop()); }
    syncSpawnGate(); // the button is live again: a blocked profile reads blocked
  }
});
// The row's actions menu. One persistent node outside the list, rebuilt on
// every open — the list is replaced wholesale every few seconds, so a menu
// nested in a row would be torn out from under the pointer. Items close over
// the session id and name, never over a row element, so a rebuild underneath an
// open menu changes nothing.
let sessionMenuSid = null;
let sessionMenuAnchor = null;
// The session whose sidebar row's ⋯ opened the open menu (null when a pane header's ⋯, the tab
// switcher or nothing did): that ⋯ reads expanded, whichever node the row is by then.
let sessionMenuRowSid = null;

function sessionMenuId() { return sessionMenuSid; }

// rowMenuOpenFor: whether the menu open right now hangs from this session's sidebar row.
function rowMenuOpenFor(sid) {
  const menu = $("session-menu");
  return !!sid && sessionMenuRowSid === sid && !!menu && menu.classList.contains("open");
}

// syncRowMenuExpanded: the row ⋯ whose menu is open says aria-expanded="true" (desktop.css keeps it
// shown and draws it pressed, m5-1), every other row's says "false". Found by session id, not by the
// node the menu was opened from: the list rebuilds a row under an open menu (paintCompactRow sets
// the same state on the new node).
function syncRowMenuExpanded() {
  for (const kb of document.querySelectorAll("#sessions .drow .kbtn[aria-expanded]")) {
    const li = kb.closest(".drow");
    kb.setAttribute("aria-expanded", li && rowMenuOpenFor(li.dataset.sid) ? "true" : "false");
  }
}

function closeSessionMenu(restoreFocus) {
  const menu = $("session-menu");
  if (menu) menu.classList.remove("open");
  const a = sessionMenuAnchor;
  sessionMenuSid = null;
  sessionMenuAnchor = null;
  sessionMenuRowSid = null;
  syncRowMenuExpanded();
  // Focus goes back where it came from, but only when the trigger is still on
  // the page: a poll may have replaced the row while the menu was open.
  if (restoreFocus && a && a.isConnected) a.focus();
}

// runSessionAction is the one dispatch table behind BOTH the desktop
// #session-menu and the mobile actions sheet (openActionsSheet): `action` is
// one of menuItems(s)'s ids (rename/files/attribution/copycwd/newpane/
// interrupt/replies-on/replies-off/restart/duplicate/end) plus the
// mobile-only "open-terminal". Neither caller may duplicate a handler here —
// a second copy is exactly the drift this extraction exists to prevent.
function runSessionAction(action, s) {
  const name = s.name || s.id;
  const run = {
    // Resolved from the session id at call time, never from a captured row or
    // anchor: the list rebuilds under an open menu/sheet every few seconds
    // (neither is in holdList()), so by the time an item is chosen the
    // trigger that opened it may already be sitting in a detached subtree.
    // `sessionRowFor` looks up whatever row is live right now.
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
    // change. Unawaited from a click handler, hence the catch. Desktop (R5)
    // opens the Files drawer instead, and explicitRoot makes drawerRootTree do
    // the same keyless re-root.
    files: () => {
      if (!mobileActive(window.innerWidth)) { openDrawer("files", s.id, { explicitRoot: true }); return; }
      // The agent's live worktree when it entered one, as drawerRootTree does.
      const t = terminalTabFor(s.id);
      const live = t ? treeFollowTarget(t) : null;
      const root = (live && live.root) || s.cwd;
      if (root && rootableCwd(root)) syncTreeToRoot(root).catch(() => {});
      setMobileScreen("files");
    },
    // Desktop: the Activity view, whose right column carries Context by tool.
    attribution: () => { if (!mobileActive(window.innerWidth)) openPaneView(s.id, "activity"); else openSteerFor(s.id); },
    copycwd: () => copyPath(s.cwd),
    newpane: () => openTerminal(s.id, name, { newPane: true }),
    // Mobile-only: jump to (attaching if needed) and focus this session's
    // terminal, switching the phone off the sheet's originating screen.
    // openTerminal is the existing open-or-focus chokepoint (tabs.js) and
    // already flips the mobile screen to "terminal" itself when active, so
    // there is nothing left to duplicate here.
    "open-terminal": () => openTerminal(s.id, name),
    interrupt: () => interruptSession(s.id),
    "replies-on": () => setRepliesFromMenu(s.id, true),
    "replies-off": () => setRepliesFromMenu(s.id, false),
    restart: () => restartSession(s),
    bringhome: () => bringHomeNow(s),
    // Duplicate opens the form prefilled — never a spawn from one click (the
    // workspace IS the containment boundary; see "+ New session" above).
    duplicate: () => openSpawnDialog(s.agent, spawnPrefill(s)),
    end: () => confirmKill(s.id, name),
  };
  const fn = run[action];
  if (fn) fn();
}

// openActionsSheet is the mobile (M5) counterpart to openSessionMenu: same
// session, same runSessionAction dispatch, a bottom sheet instead of a
// floating menu. sheetActions/sheetMeta (sheet.js) are the single source for
// which rows appear and their order/danger flag — this only renders them.
function openActionsSheet(s) {
  openSheet(function (body) {
    const shd = document.createElement("div");
    shd.className = "shd";
    const act = sessionDot(s);
    const dot = document.createElement("span");
    dot.className = act.cls;
    dot.setAttribute("aria-hidden", "true");
    shd.appendChild(dot);
    const nm = document.createElement("span");
    nm.className = "grow";
    nm.textContent = s.name || s.id;
    shd.appendChild(nm);
    const tier = tierBadge(s, boxRuntime);
    if (tier) {
      const tb = document.createElement("span");
      tb.className = "bdg tier " + (tier.tone === "danger" ? "host" : "box");
      tb.textContent = tier.text;
      tb.title = tier.title;
      shd.appendChild(tb);
    }
    const ab = acctBadgeEl(s);
    if (ab) shd.appendChild(ab);
    const awsb = awsBadgeEl(s);
    if (awsb) shd.appendChild(awsb);

    // Meta line: model/effort/context% come from the terminal TAB bound to
    // this session (a session has none of its own — see usage.js/tab.usage()),
    // the same source renderSession and the 5b chip strip already read from.
    // cwd/started come from sheetMeta(s); only cwd is ever non-empty today
    // (Info carries no start time yet), but the join tolerates that.
    const tab = terminalTabFor(s.id);
    const u = tab && typeof tab.usage === "function" ? tab.usage() : null;
    const meta = sheetMeta(s);
    const parts = [
      u && u.model ? String(u.model) : "",
      u && u.effort ? "effort " + u.effort : "",
      u && typeof u.pct === "number" ? u.pct + "% of context" : "",
      meta.cwd ? shortPath(meta.cwd) : "",
      meta.started,
    ].filter(Boolean);
    const ml = document.createElement("div");
    ml.className = "sheet-meta";
    ml.textContent = parts.join(" · ");

    body.append(shd, ml);

    for (const a of sheetActions(s)) {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "arow" + (a.danger ? " dgr" : "");
      const gl = document.createElement("span");
      gl.className = "gl";
      gl.setAttribute("aria-hidden", "true");
      gl.textContent = a.glyph;
      const lbl = document.createElement("span");
      lbl.textContent = a.label;
      row.append(gl, lbl);
      row.onclick = () => { closeSheet(); runSessionAction(a.id, s); };
      body.appendChild(row);
    }
  }, { height: "min(556px, 88dvh)" });
}

// openReplySheet is the mobile (M4) counterpart to togglePeek's inline
// composer: the same last-message fetch, the same three quick replies, the
// same sendState/replyTextError gate, and the same sendReplyFor send path —
// a bottom sheet instead of an inline row under the session, because a phone
// has no room to expand a row in place without losing the list underneath it.
//
// Gating is single-sourced and fail-closed: `refresh()` below is the only
// place that computes send.disabled and the caption text, from the same
// sendState/replyTextError peek.js already ships to the desktop composer.
// Control and caption are written by the same call, so they cannot disagree.
//
// repaintReplySheet re-runs the open sheet's refresh() from the 3s poll, as
// repaintSteerRail does for the rail: paint-only, so a half-typed reply keeps
// its text and caret. null when no reply sheet is open; a stale one (the sheet
// closed or now shows something else) clears itself.
let repaintReplySheet = null;
function openReplySheet(s) {
  // pendingNow mirrors togglePeek's li.dataset.peekPending: whether the
  // session is inside a tool call right now. There is no session-list field
  // for it (only the /peek response carries it) — seed from the lastPeek
  // cache so the gate is never briefly wrong about a session already seen,
  // then refresh it for real once the fetch below lands.
  let pendingNow = !!(lastPeek && lastPeek.id === s.id && lastPeek.pending);

  openSheet(function (body) {
    // Session-identity header (M4): activity dot + session name + tier badge,
    // the same trio openActionsSheet's .shd builds — so a reply sheet opened
    // straight from the M1 card still says which session it is talking to.
    const shd = document.createElement("div");
    shd.className = "shd";
    const sact = sessionDot(s);
    const sdot = document.createElement("span");
    sdot.className = sact.cls;
    sdot.setAttribute("aria-hidden", "true");
    shd.appendChild(sdot);
    const snm = document.createElement("span");
    snm.className = "grow";
    snm.textContent = s.name || s.id;
    shd.appendChild(snm);
    const stier = tierBadge(s, boxRuntime);
    if (stier) {
      const stb = document.createElement("span");
      stb.className = "bdg tier " + (stier.tone === "danger" ? "host" : "box");
      stb.textContent = stier.text;
      stb.title = stier.title;
      shd.appendChild(stb);
    }
    const sab = acctBadgeEl(s);
    if (sab) shd.appendChild(sab);
    const sawsb = awsBadgeEl(s);
    if (sawsb) shd.appendChild(sawsb);

    const flab = document.createElement("div");
    flab.className = "flab";
    flab.textContent = "Last message";

    const box = document.createElement("div");
    box.className = "msgbox";
    box.textContent = lastPeek && lastPeek.id === s.id ? lastPeek.text : "Loading…";

    // Replies off (server kill-switch, /api/meta): the sheet is read-only — the
    // header, the full text and Open terminal. No opt-in, Send or quick replies
    // are built, so refresh() has nothing to paint and returns at once.
    const repliesOn = globalThis.repliesEnabled === true;
    const qkButtons = [];
    let ta = null, send = null, note = null, optin = null, qk = null;
    let optinCb = null, optinBusy = false; // the opt-in box, and whether its POST is in flight

    // Enable-replies toggle (I1 regression fix). Replyable defaults false and
    // the ONLY control that grants it is this checkbox — pre-5c it lived in
    // the inline peek composer the desktop still shows; on mobile the summary
    // tap now opens this sheet instead, so the toggle must move here too or a
    // default session's Send stays disabled forever with no way to enable it.
    // Same POST /api/sessions/<id>/replyable the desktop cb makes; on success
    // s.replyable flips and refresh() re-evaluates the gate (Send/qk enable
    // when the session is also waiting). The M4 mockup omitted this control,
    // but "UI fidelity never breaks function" — this restores a regressed
    // capability, kept compact and consistent with the sheet's style.
    if (repliesOn) {
      optin = document.createElement("label");
      optin.className = "sheet-optin";
      const cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = !!s.replyable;
      const optinText = document.createElement("span");
      optinText.textContent = "Enable replies for this session";
      optin.append(cb, optinText);
      optinCb = cb;
      cb.onchange = async () => {
        const want = cb.checked;
        let changed = false;
        optinBusy = true;
        try {
          const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/replyable", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ replyable: want }),
          });
          if (!res.ok) throw new Error(String(res.status));
          s.replyable = want;
          changed = true;
        } catch (_) {
          cb.checked = !want; // the server is the truth; put the box back
          showToast("Could not change the reply setting", { error: true });
        }
        // refresh() gates on the poll's copy (sheetGateInput): fetch it now so
        // the new replyable and the server's reply_ready land before it runs.
        try { if (changed) await refreshSessions(); } finally { optinBusy = false; }
        refresh();
      };

      // Quick replies: same three labels, same digit-only payload as the
      // inline composer's qk row (togglePeek, above) — sent through the same
      // doSend below, never a second copy of the send path.
      qk = document.createElement("div");
      qk.className = "qk";
      for (const [digit, word] of [["1", "yes"], ["2", "always"], ["3", "no"]]) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "btn";
        b.textContent = digit + " " + word;
        b.title = "Send " + digit;
        b.onclick = () => doSend(digit);
        qk.appendChild(b);
        qkButtons.push(b);
      }

      ta = document.createElement("textarea");
      ta.className = "ta";
      ta.rows = 3;
      ta.placeholder = "Type a reply…";
      // Restore an in-progress draft the same way the inline composer does —
      // replyDraft is shared module state, so a draft started here survives a
      // later desktop-width resize into the inline composer, and vice versa.
      if (replyDraft && replyDraft.id === s.id) ta.value = replyDraft.text;
      ta.addEventListener("input", () => {
        replyDraft = { id: s.id, text: ta.value };
        refresh();
      });
    }

    const wide = document.createElement("div");
    wide.className = "wide";
    const openT = document.createElement("button");
    openT.type = "button";
    openT.className = "btn";
    openT.textContent = "Open terminal";
    openT.onclick = () => { closeSheet(); openTerminal(s.id, s.name); };
    if (repliesOn) {
      send = document.createElement("button");
      send.type = "button";
      send.className = "btn pri";
      send.textContent = "Send ↵";
      send.onclick = () => doSend(ta.value);
      wide.append(openT, send);

      note = document.createElement("div");
      note.className = "sheet-note";

      body.append(shd, flab, box, optin, qk, ta, wide, note);
    } else {
      wide.append(openT);
      body.append(shd, flab, box, wide);
    }

    // refresh() is the ONE place send.disabled and the caption are decided —
    // see the function-level comment above. sendState/replyTextError are the
    // exact same peek.js functions the desktop composer's own refresh() uses.
    function refresh() {
      if (!repliesOn) return; // read-only sheet: no Send, quick replies or caption to paint
      const st = sendState(sheetGateInput(lastSessions, s, pendingNow, sessionsLoaded));
      const errCode = replyTextError(ta.value);
      const gateClosed = !st.enabled;
      // The quick replies are controls too — a session that is not
      // replyable/waiting/free must close them exactly like Send, not just
      // the textarea's own composer. A digit always passes replyTextError,
      // so without this they would stay tappable while the caption below
      // says otherwise.
      send.disabled = gateClosed || !!errCode;
      for (const b of qkButtons) b.disabled = gateClosed;
      // Same three sentences togglePeek's sendReply shows for these exact
      // codes (see its err-branch above) — reused, not reinvented, so the
      // wording an operator learns on desktop still applies here. Control
      // and caption must never disagree: whenever Send is disabled there is
      // always a reason on screen, gating reason or text-validation reason.
      note.textContent = gateClosed ? st.reason
        : errCode === "empty" ? "Type something first."
        : errCode === "newline" ? "One line only — newlines are not sent."
        : errCode === "control" ? "That text contains control characters and was not sent."
        : "";
      note.hidden = !note.textContent;
    }

    // doSend re-checks BOTH gates right before sending — sendState (a quick
    // reply's disabled attribute is the only thing stopping it otherwise;
    // this is the fail-closed backstop, not decorative) and replyTextError
    // (a quick reply's own digit always passes, but Send reads whatever is
    // in the textarea right now) — then reuses sendReplyFor for the actual
    // POST + error mapping, the one send path shared with the desktop inline
    // composer. pendingSends is the SAME module-level latch togglePeek uses,
    // so a send already in flight from the inline composer for this session
    // blocks a second one from the sheet too.
    function doSend(text) {
      if (pendingSends.has(s.id)) return; // a second tap while the first is still in flight
      const st = sendState(sheetGateInput(lastSessions, s, pendingNow, sessionsLoaded));
      if (!st.enabled || replyTextError(text)) { refresh(); return; } // the note already explains why
      pendingSends.add(s.id);
      send.disabled = true;
      for (const b of qkButtons) b.disabled = true;
      sendReplyFor(s, text).then((ok) => {
        pendingSends.delete(s.id);
        if (ok) {
          if (replyDraft && replyDraft.id === s.id) replyDraft = null; // sent — nothing left to restore
          closeSheet();
        } else {
          // Keep the sheet and the draft — sendReplyFor already toasted why.
          // Do NOT clear ta.value: a rejected send must not lose what the
          // operator typed.
          refresh();
        }
      });
    }

    refresh();
    repaintReplySheet = () => {
      const dlg = document.getElementById("sheet-modal");
      if (!dlg || !dlg.open || !box.isConnected) { repaintReplySheet = null; return; }
      if (optinCb) optinCb.checked = optinChecked(lastSessions, s, optinBusy, optinCb.checked);
      refresh();
    };

    // Live last message + pending flag, the same fetch togglePeek makes on
    // expand — the sheet can be opened straight from the M1 card without the
    // inline composer ever having run, so lastPeek may be stale or absent.
    (async () => {
      let status = 0;
      let payload = null;
      try {
        const res = await fetch("/api/sessions/" + encodeURIComponent(s.id) + "/peek");
        status = res.status;
        if (status === 200) payload = await res.json();
      } catch (_) {
        status = 0;
      }
      const dlg = document.getElementById("sheet-modal");
      // Two ways this fetch can land after it no longer matters: the sheet
      // was closed outright (dlg.open false), or it is still open but was
      // reopened for a different session in the meantime — openSheet's
      // body.replaceChildren() would have detached `box` from the page, the
      // same signal togglePeek reads via peek.isConnected.
      if (!dlg || !dlg.open || !box.isConnected) return;
      const st = peekState(!!s.claude, status, payload);
      box.textContent = st.text;
      syncMaskHint(box, st.text);
      pendingNow = !!(payload && payload.pending);
      lastPeek = { id: s.id, text: st.text, muted: st.kind !== "text", pending: pendingNow };
      refresh();
    })();
  }, { height: "min(648px, 92dvh)" });
}

// kitMenuNodes: the kit menu's children. `own` are the pane's own rows ({label, run}), a group
// of their own when there are any; `items` are menuItems' for the session. A .menu-sep goes
// between groups and nowhere else (menuGroups drops the empty ones).
function kitMenuNodes(own, items, s) {
  const groups = [];
  if (own.length) groups.push(own.map((it) => kitMenuButton(it.label, "", false, () => { closeSessionMenu(false); it.run(); })));
  for (const g of menuGroups(items)) {
    // Close first, then act: an action that opens a dialog would otherwise leave the menu
    // floating over it.
    groups.push(g.map((it) => kitMenuButton(it.label, menuIcon(it.id), it.danger, () => { closeSessionMenu(false); runSessionAction(it.id, s); })));
  }
  const nodes = [];
  groups.forEach((g, i) => {
    if (i > 0) nodes.push(kitMenuSep());
    nodes.push(...g);
  });
  return nodes;
}

// `extra` (optional) are pane-level rows ({label, run}) shown above the session's own, for a
// narrow pane header that has no room for their buttons; `s` may then be null (a pane of files
// has no session). Desktop draws the kit menu (F12); the phone keeps the flat .mi list.
function openSessionMenu(anchor, s, extra) {
  const menu = $("session-menu");
  if (!menu) return;
  const desktop = !mobileActive(window.innerWidth);
  // Desktop only: a shell listed under its parent gets the short shell menu.
  const nestedShell = !!s && desktop && isNestedShell(s, lastSessions);
  const items = s ? menuItems(s, { nested: nestedShell }) : [];
  let nodes;
  if (desktop) {
    nodes = kitMenuNodes(extra || [], items, s);
  } else {
    const own = (extra || []).map((it) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "mi";
      b.setAttribute("role", "menuitem");
      b.textContent = it.label;
      b.onclick = () => { closeSessionMenu(false); it.run(); };
      return b;
    });
    nodes = own.concat(items.map((it) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = it.danger ? "mi dgr" : "mi";
      b.setAttribute("role", "menuitem");
      b.textContent = it.label;
      // Close first, then act: an action that opens a dialog would otherwise
      // leave the menu floating over it.
      b.onclick = () => { closeSessionMenu(false); runSessionAction(it.id, s); };
      return b;
    }));
  }
  menu.replaceChildren(...nodes);
  sessionMenuSid = s ? s.id : null;
  placeSessionMenu(menu, anchor);
}

// placeSessionMenu opens #session-menu under `anchor` (above it if it would
// clip) and focuses its first item. Shared by the session menu and the pane
// header's tab switcher, which reuses the same element and its closing rules.
function placeSessionMenu(menu, anchor) {
  sessionMenuAnchor = anchor;
  // A sidebar row's ⋯ hangs the menu from the whole row (m5-1: below it, the menu's right edge on
  // the row's), not from the 24px button, so the menu never covers the row's own sub-line.
  const row = anchor.closest ? anchor.closest("#sessions .drow") : null;
  sessionMenuRowSid = row ? row.dataset.sid || null : null;

  menu.classList.add("open"); // must be visible before it can be measured
  syncRowMenuExpanded();
  // Rows sit anywhere down the rail, so a menu opened near the bottom flips
  // above its row rather than clipping. The height has to be measured AFTER the
  // items are in, because the item list varies per session.
  const pos = menuPlace((row || anchor).getBoundingClientRect(), menu.offsetWidth, menu.offsetHeight, window.innerWidth, window.innerHeight, row ? ROW_MENU_GAP : MENU_GAP);
  menu.style.left = pos.left + "px";
  menu.style.top = pos.top + "px";
  // Focus lands on the first item. The precedent popover in this app leaves
  // focus on the trigger, which puts its own items somewhere unpredictable in
  // the tab order — acceptable for a menu of file actions, not for one whose
  // last item ends a running agent.
  const first = menu.querySelector(".menu-item, .mi");
  if (first) first.focus();
}

// toggleSessionMenuAt: a ⋯ that opens the session menu, and closes it again
// when pressed while its own menu is open.
function toggleSessionMenuAt(anchor, s, extra) {
  const menu = $("session-menu");
  if (menu && menu.classList.contains("open") && sessionMenuAnchor === anchor) { closeSessionMenu(true); return; }
  openSessionMenu(anchor, s, extra);
}

// kitTabRow: one row of the desktop tab switcher, a .menu-row of the tab as a kit row (its glyph in the
// icon slot, its title, a check and aria-current on the shown one) and its own close. A viewer tab is a
// file, so it wears the pane header's folder glyph.
function kitTabRow(cell, t) {
  const row = document.createElement("div");
  row.className = "menu-row";
  const b = kitMenuButton(t.title(), "", false, () => { closeSessionMenu(false); setFocusedCell(cell); cell.activate(t); });
  b.children[0].append(tabIcon(t.kind === "terminal" ? "terminal" : "files"));
  b.title = t.kind === "viewer" ? t.path || t.title() : t.title();
  if (t === cell.activeTab) {
    b.setAttribute("aria-current", "true");
    const ck = document.createElement("span");
    ck.className = "menu-check";
    ck.setAttribute("aria-hidden", "true");
    ck.append(menuIconSvg("check"));
    b.append(ck);
  }
  const x = document.createElement("button");
  x.type = "button";
  x.className = "menu-x btn btn-ghost btn-icon btn-sm";
  x.textContent = "\u00D7";
  x.title = t.kind === "terminal" ? "Close tab (the session keeps running)" : "Close tab";
  x.setAttribute("aria-label", "Close " + t.title());
  x.onclick = () => { closeSessionMenu(false); cell.closeTab(t); };
  row.append(b, x);
  return row;
}

// openTabSwitcher: the pane header's `name ▾ · N` menu. One row per tab in the
// pane (the shown one marked), each with its own close. Same element and the
// same outside-click / Escape / arrow rules as the session menu. Desktop draws
// the kit menu; the header that opens it exists on desktop only, so the phone's
// flat .mi rows below are only a fallback for a window narrowed under it.
function openTabSwitcher(anchor, cell) {
  const menu = $("session-menu");
  if (!menu) return;
  if (menu.classList.contains("open") && sessionMenuAnchor === anchor) { closeSessionMenu(true); return; }
  const desktop = !mobileActive(window.innerWidth);
  const rows = desktop ? cell.tabs.map((t) => kitTabRow(cell, t)) : cell.tabs.map((t) => {
    const row = document.createElement("div");
    row.className = "mi-row";
    const b = document.createElement("button");
    b.type = "button";
    b.className = "mi" + (t === cell.activeTab ? " cur" : "");
    b.setAttribute("role", "menuitem");
    b.textContent = (t.kind === "terminal" ? "\u25B8 " : "") + t.title();
    b.title = t.kind === "viewer" ? t.path || t.title() : t.title();
    b.onclick = () => { closeSessionMenu(false); setFocusedCell(cell); cell.activate(t); };
    const x = document.createElement("button");
    x.type = "button";
    x.className = "mi-x";
    x.textContent = "\u00D7";
    x.title = t.kind === "terminal" ? "Close tab (the session keeps running)" : "Close tab";
    x.setAttribute("aria-label", "Close " + t.title());
    x.onclick = () => { closeSessionMenu(false); cell.closeTab(t); };
    row.append(b, x);
    return row;
  });
  menu.replaceChildren(...rows);
  sessionMenuSid = null; // not about one session: the list poll must not close it
  placeSessionMenu(menu, anchor);
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
  // Arrow navigation, which the app's other popover does not have. Same reason
  // as the focus move above: reaching a destructive item must not depend on
  // tabbing blindly through the document. Rows only: a kit menu's .menu-sep
  // separators are not items, so the keys step over them (kitmenu.js).
  kitMenuKeys(menu, e);
});

// confirmKill is the guarded entry point (the tab strip's kill button, the
// command palette, and the row menu's End session… item). killSession itself
// stays unguarded: Undo on a fresh spawn must not ask twice.
async function confirmKill(id, name) {
  const shells = liveShellsOf(id, lastSessions);
  // The desktop dialog names what ends and what is left (frame M5.2); the phone keeps its short question.
  // A nested companion shell keeps the short question on desktop too: its copy says "the agent stops",
  // and a shell has no agent. So does a top-level session whose profile runs a plain shell (bash, zsh, ...; the
  // profile is looked up by the session's agent, none found means the agent copy). shortPath reads the same
  // window.HOME_DIR every other path on the page does; a session with no cwd (or one the list no longer holds)
  // gives no Files note.
  const session = lastSessions.find((s) => s.id === id);
  const shellSession = !!session && spawnIsShellProfile(profiles.find((p) => p.name === session.agent));
  const outcome = await askChoice(mobileActive(window.innerWidth) || shellSession || isNestedShell(session, lastSessions)
    ? killDialogOpts(name, id, shells.length)
    : killDialogDeskOpts(name, id, shells.map((s) => shellLabel(s, lastSessions)), session && session.cwd ? shortPath(session.cwd) : ""));
  // "End session only" leaves the shells running as top-level rows (the parent
  // is gone, so nothing nests them any more).
  const ids = killIdsFor(outcome, id, shells);
  await killSessions(ids);
}

// killSessions: End both (R25). On the desktop, two or more ids share ONE pending toast, "Ending <name> and
// shell 1…", which becomes "Ended <name> and shell 1" naming only what ended; a failed id keeps today's error
// toast. Every step is killSession's: `batch` only swaps its own toast for a collector. Each id is marked
// ended-by-me up front, so the poll that lands while the parent's DELETE is still running cannot announce
// "Ended" for a shell this tab is also ending. The phone keeps one toast per session.
async function killSessions(ids) {
  if (ids.length < 2 || mobileActive(window.innerWidth)) { for (const sid of ids) await killSession(sid); return; }
  const labels = new Map(ids.map((sid) => {
    const s = (lastSessions || []).find((x) => x.id === sid);
    return [sid, (s ? shellLabel(s, lastSessions) : "") || sid];
  }));
  const now = Date.now();
  for (const sid of ids) if (!killing.has(sid)) endedMark(endedByMe, sid, now);
  const done = pendingToast("Ending " + shellEndList(ids.map((sid) => labels.get(sid))) + "…");
  const items = [];
  const batch = { collect: (sid) => (result) => items.push({ sid, result }) };
  for (const sid of ids) await killSession(sid, batch);
  const ok = items.filter((i) => !i.result.opts.error);
  const bad = items.filter((i) => i.result.opts.error);
  if (ok.length) {
    done({ msg: "Ended " + shellEndList(ok.map((i) => labels.get(i.sid))), opts: { desktopOnly: true } });
  } else if (bad.length) {
    done(bad.shift().result);
  } else {
    done(); // every id was already being ended: nothing to say
  }
  for (const i of bad) showToast(i.result.msg, i.result.opts);
}

async function killSession(id, batch) {
  if (killing.has(id)) return; // already tearing down: ignore repeat clicks
  killing.add(id);
  endedMark(endedByMe, id, Date.now()); // the poll that sees it gone must not announce "Ended"
  // A boxed session's docker stop can take seconds: a toast covers the wait
  // after the pending delay. It starts here, at the click, so the 300ms clock
  // is not spent on the re-render below. done(result) (in finally) turns it
  // into the outcome: on the desktop the same stack entry becomes "Ended <name>"
  // or the error (F64: a sentence, the raw text behind Copy details); the phone
  // gets "End failed: <raw>" only, since the row leaving says it ended.
  const sess = (lastSessions || []).find((s) => s.id === id);
  const name = (sess && (sess.name || sess.id)) || id;
  const done = batch ? batch.collect(id) : pendingToast("Ending " + name + "…");
  let failure = null;
  let result = { msg: "Ended " + name, opts: { desktopOnly: true } };
  try {
    await refreshSessions(); // re-render → row shows "ending…"
    await api("/api/sessions/" + encodeURIComponent(id), { method: "DELETE", timeoutMs: ACTION_TIMEOUT_MS });
  } catch (e) {
    failure = e;
    const f = toastFailText("end " + name, e);
    result = { msg: "End failed: " + e.message, opts: { error: true, human: f.text, detail: f.detail } };
  } finally {
    done(result);
  }
  if (failure) {
    killing.delete(id);
    // A refusal or a failure leaves it running: a later end is not ours. A request that got no answer in time (#257) may
    // still finish, and the poll that then sees the session gone must not announce "Ended" for what this tab ended: the
    // mark stays and expires by itself (ENDED_BY_ME_MS).
    if (!failure.timedOut) endedByMe.delete(id);
    await refreshSessions();
    return;
  }
  killing.delete(id);
  closeTerminal(id);
  await refreshSessions(); // session gone → row removed
}

// --- quick shell -------------------------------------------------------------
// "+ Shell" starts a companion shell of the cell's terminal neighbour through
// POST /api/sessions/{id}/shell, which takes the directory, tier and parent
// link from that session server-side (one path for desktop and mobile, so the
// parent link exists everywhere). placement: "below" | "right" docks it in the
// pane holding its parent, "own" gives it a pane; mobile opens it as a tab,
// exactly as before docks existed (shellSpawnRoute).
// Per-cell (not global) in-flight guard: a double click on one window is ignored,
// but +sh on another window still works instead of dying silently.
const shellSpawning = new WeakSet();
//
// trigger: the button that was clicked, when one stays on screen. It shows the
// wait itself ("Starting shell…") and its re-entry guard replaces the caller's
// own disabled/finally dance. Without one (palette, keyboard) a toast covers
// the shell request after the pending delay.
//
// label: the text the trigger shows while pending. Icon-sized buttons (the dock
// "+", the "+sh" chip) pass "…" so they do not grow into a sentence.
async function spawnShellIn(cell, placement = "below", trigger, label = "Starting shell…") {
  if (!window.shellProfile || !cell || shellSpawning.has(cell)) return;
  if (closePageHolds(() => spawnShellIn(cell, placement, trigger, label))) return;
  if (trigger) return withPending(trigger, label, () => spawnShellRun(cell, placement, false));
  return spawnShellRun(cell, placement, true);
}

async function spawnShellRun(cell, placement, toastWait) {
  shellSpawning.add(cell);
  try {
    // Only a gate and the form fallback's preselection now: the shell endpoint
    // picks the profile itself, server-side.
    // Re-read the profile name instead of trusting the boot-time cache: the server
    // withholds it once a stored profile takes over the seed's name, and profiles
    // can be created after this page loaded.
    const profile = await currentShellProfile();
    if (!profile) { showToast("No shell profile available, check the console log.", { error: true }); return; }
    const neighbour = await resolveCellNeighbour(cell);
    const plan = shellSpawnPlan(neighbour);
    if (plan.mode === "form") {
      // Nothing to sit beside: there is no context to inherit, so let the user
      // choose one rather than guessing a directory that may not be containable.
      openSpawnDialog(profile);
      return;
    }
    const done = toastWait ? pendingToast("Starting shell…") : () => {};
    let s;
    try {
      s = await api("/api/sessions/" + encodeURIComponent(neighbour.id) + "/shell", { method: "POST", timeoutMs: ACTION_SLOW_TIMEOUT_MS });
    } finally {
      done();
    }
    await refreshSessions(); // the strip's "shell N" label reads the new list
    // The cell can be destroyed while we await (applyCap collapses windows, a pane
    // can be removed). Attaching to a detached cell would leak a live xterm + socket
    // that is never rendered, so fall back to normal placement instead.
    const alive = cells.includes(cell);
    // No parent_id: the target had ended (or lost its parent), so the server
    // opened an unlinked shell in its directory. Nothing to dock beside.
    const route = shellSpawnRoute(placement, mobileActive(window.innerWidth), !!s.parent_id);
    if (route === "tab" || route === "own") {
      if (alive) setFocusedCell(cell);
      openTerminal(s.id, s.name, route === "own" ? { newPane: true } : undefined);
      return;
    }
    // The server answers with the real parent (a shell's own target is
    // flattened to its parent). No pane holds it (e.g. "+ Shell" in a shell's
    // own pane whose agent is closed): open the parent and dock beside it, as a
    // sidebar click does, so a dock's host is always the shell's parent.
    if (dockShellBesideParent(s, route)) return;
    revealShell(s.id);
    const host = dockHolding(s.id);
    if (host) host.setDockPlacement(route);
    else if (!terminalTabFor(s.id)) openTerminal(s.id, s.name); // not in the list yet: never leave it unshown
  } catch (e) {
    // 403 / 404 / 409 carry the server's sentence ("no shell profile").
    const f = toastFailText("start a shell", e);
    showToast("Shell failed: " + e.message, { error: true, human: f.text, detail: f.detail });
    if (spawnRefusedLocked(e.status, e.code)) refreshStore();
  } finally {
    shellSpawning.delete(cell);
  }
}

// revealShell(id) shows a companion shell from its sidebar row: activated
// where it already is (its dock, or a tab), else docked in the pane holding
// its parent, else the parent is opened first and the shell docked beside it.
// Mobile opens it like any session (openTerminal undocks a docked one).
// Every branch reuses an open tab: a second attach would supersede the first.
function revealShell(id) {
  const s = lastSessions.find((x) => x.id === id);
  if (!s) return;
  const parent = s.parent_id ? lastSessions.find((p) => p.id === s.parent_id) || null : null;
  const model = { cells: cells.map((c) => ({ tabs: c.tabs, dock: c.dock })) };
  const plan = revealPlan(model, s.id, parent, mobileActive(window.innerWidth));
  if (plan === "open-parent") openTerminal(parent.id, parent.name);
  if (plan === "open" || !dockShellBesideParent(s)) openTerminal(s.id, s.name);
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
    globalThis.repliesEnabled = !!(meta && meta.replies === true);
    window.shellProfile = (meta && meta.shell_profile) || "";
    boxRuntime = (meta && meta.box_runtime) || "";
    metaLoaded = true;
    cloneUnavailable = (meta && meta.clone_unavailable) || "";
    boxUnavailableReason = (meta && meta.box_unavailable_reason) || "";
    boxDaemonReason = (meta && meta.box_daemon_reason) || "";
    bundleImports = !!(meta && meta.bundle_imports);
    refreshEmptyState();
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
// fsShortcutsLabel draws "Shortcuts", the row label the Appearance frame (M2.5) puts beside the Keyboard shortcuts
// button. Desktop copy comes from here and not from index.html, which the phone also shows; app.css hides the node
// below the breakpoint. Once: initSettings runs at boot and again on Reset.
function fsShortcutsLabel() {
  if ($("fs-kbd-lbl")) return;
  const btn = $("kbd-open-settings");
  if (!btn) return;
  const lbl = document.createElement("span");
  lbl.id = "fs-kbd-lbl";
  lbl.className = "lbl";
  lbl.textContent = "Shortcuts";
  btn.before(lbl);
}
function initSettings() {
  fsShortcutsLabel();
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
// The open dock is sized from the window (dockLayout), so it is laid out BEFORE the terminals fit: registered first, it
// runs first. Without it fitAll measures at the old dock width and the grid's ResizeObserver refits a frame later at
// the new one, which sends the PTY two sizes for one drag step. The phone has no dock and does not run it.
window.addEventListener("resize", () => { if (drawerState.open && drawerDesktop()) positionDrawer(); });
window.addEventListener("resize", fitAll);
// The desktop's empty grid says "Start"; index.html keeps the phone's "Spawn" (founder call 1, Plan 7).
{
  const ph = document.querySelector("#grid > .placeholder");
  if (ph && !mobileActive(window.innerWidth)) ph.textContent = "Start or open a session. Panes appear here.";
}
// Analytics view (button revealed by initEnv only when the feature is enabled).
$("analytics-open").addEventListener("click", openAnalytics);
$("side-analytics").addEventListener("click", openAnalytics);
$("side-audit").addEventListener("click", () => openAudit());
$("side-palette").addEventListener("click", openPalette);
paintSideHints();
// "$ today" is a glance, so it refreshes on a slow clock; skipped while hidden.
setInterval(() => { if (!document.hidden) paintSideCost(); }, 5 * 60 * 1000);
{
  // Re-scan: the phone's button in the modal's head and the page's in its own (index.html #an-rescan).
  const anRefresh = $("analytics-refresh");
  if (anRefresh) anRefresh.addEventListener("click", () => anRescan(anRefresh));
  const anRescanBtn = $("an-rescan");
  if (anRescanBtn) anRescanBtn.addEventListener("click", () => anRescan(anRescanBtn));
  // Tab bar: switch the active analytics tab and re-render from cached data
  // (no refetch — the window data is already loaded).
  const anTabs = $("an-tabs");
  if (anTabs) anTabs.addEventListener("click", (e) => {
    const b = e.target.closest("button[data-antab]");
    if (!b) return;
    anSetTab(b.dataset.antab);
  });
  // The desktop page's view switch, range, Compare and Custom range row (analytics.js wires them).
  anWirePage();
}
// Layout buttons 1/2/4 → set the max-window cap (auto-split grows up to it).
// The header's group (mobile) and the sidebar top's (desktop) are the same control.
for (const grp of document.querySelectorAll(".layouts")) {
  grp.addEventListener("click", (e) => {
    if (e.target.dataset && e.target.dataset.cols) applyCap(Number(e.target.dataset.cols));
  });
}
applyCap(4); // default: auto-split up to 4 windows
initDialogs();
initSplitter();
initWidthSplitter();
// Pane collapse state reads live session state (waitingCount, via
// renderCollapsedSummaries) for the Sessions bar's pip, so it cannot run
// until lastSessions is initialized — this is why the call lives down here
// in the boot sequence rather than up near the sidebar-toggle wiring, where
// lastSessions's declaration has not been reached yet.
// The legacy keys are read through bootFlags (deskshell.js): on desktop the stored
// collapse is ignored (the pre-paint classes are cleared here, desktop.css
// neutralises them until then) and a stored Access rail is never opened.
const bootKeys = bootFlags(deskStorage(), mobileActive(window.innerWidth));
applyCollapsed(bootKeys.ignoreCollapsed ? { sessions: false, files: false } : readCollapsed());
// The steer rail's own persisted state. It lives down here for the same reason
// applyCollapsed does — opening the rail paints it, which reads lastSessions,
// whose declaration is not reached until this point in the boot sequence — and
// it is deliberately NOT a pre-paint <head> script: the rail is a right-hand
// panel, so arriving a frame late shifts nothing, and the four hand-mirrored
// parses already in <head> are enough to keep in step.
// bootFlags already absorbed a storage backend that throws, and opening the rail
// paints it, so a rendering failure here is NOT swallowed: it would present as
// "the rail just doesn't restore", with nothing in the console. Desktop never
// opens it (Access is a pane view there).
if (bootKeys.openAccess) setSteerOpen(true);
initSpawnControl();
initSpawnWsPicker();
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
    bootRestoreDone = true;
    refreshEmptyState();
    return;
  }
  // finally: a throwing restore must not keep the empty state silent for the
  // life of the page (it waits on bootRestoreDone).
  try { restoreLayout(sessions); }
  finally { bootRestoreDone = true; refreshEmptyState(); }
})();
initFileTree();
initUpload();
// initMobileShell() lives down here, not up near where it's defined, for the
// same reason applyCollapsed does above: renderMobileNav() reads
// waitingCount(), which reads lastSessions — safe to touch only once the DOM
// is up and the first session poll (restoreBoot, just above) has been wired.
initMobileShell();

// Env signal: show a colored DEV/PROD badge + top strip + tab title, so the
// active environment is unmistakable. Config-driven via /api/meta; empty label
// (no env configured) leaves the UI unchanged.
// One retry: this fetch also carries shell_profile, and a single transient failure
// would otherwise hide the +sh button for the whole page session.
(async function initEnv(retry = true) {
  try {
    const seq = ++credListsSeq; // shares the refresh's sequence, so a slower boot answer cannot overwrite a newer one
    const meta = await api("/api/meta");
    // Whether replies are on gates every reply surface; closed until this answers.
    globalThis.repliesEnabled = !!(meta && meta.replies === true);
    // Quick-shell profile first: it is independent of the env badge, so it must
    // be captured before the no-badge early return below.
    window.shellProfile = (meta && meta.shell_profile) || "";
    boxRuntime = (meta && meta.box_runtime) || "";
    metaLoaded = true;
    cloneUnavailable = (meta && meta.clone_unavailable) || "";
    boxUnavailableReason = (meta && meta.box_unavailable_reason) || "";
    boxDaemonReason = (meta && meta.box_daemon_reason) || "";
    bundleImports = !!(meta && meta.bundle_imports);
    refreshEmptyState();
    if (seq === credListsSeq) applyCredLists(meta); // the Tokens, connections and App pickers, now that the names are known
    const anBtn = $("analytics-open");
    if (anBtn) anBtn.hidden = !(meta && meta.analytics); // only when the feature is on
    const sideAn = $("side-analytics");
    if (sideAn) sideAn.hidden = !(meta && meta.analytics);
    const sideAudit = $("side-audit");
    if (sideAudit) sideAudit.hidden = !(meta && meta.audit); // only with an audit store
    // macOS only: Settings > VM folders (the nav button and the palette entry wait for the server to say the page exists).
    globalThis.vmFoldersOn = !!(meta && meta.vm_folders);
    if (typeof vmaFromMeta === "function") vmaFromMeta(meta && meta.vm_apply); // an apply in flight (or a failed one) shows at once
    const vmTab = document.querySelector('#settings-tabs [data-tab="vmfolders"]');
    if (vmTab) vmTab.hidden = !globalThis.vmFoldersOn;
    sideCostOn = !!(meta && meta.analytics);
    paintSideCost();
    lastRateLimits = (meta && meta.rate_limits) || null;
    renderRateLimits(lastRateLimits); // account 5h/weekly meters in the header
    refreshSessions(); // re-render rows now that the tier label is known
    if (globalThis.repliesEnabled && typeof renderSteerRail === "function") renderSteerRail(); // the rail was built before this answer
    if (window.shellProfile || globalThis.repliesEnabled) cells.forEach((c) => c.renderBar()); // reveal +sh, and the reply strip of a waiting pane
    sysSeeVersion(meta && meta.version);
    const ver = $("version-badge"), vl = versionLabel(meta && meta.version);
    if (ver && vl) { ver.textContent = vl; ver.title = "CuliBox " + meta.version; ver.hidden = false; }
    const sideVer = $("side-version");
    if (sideVer && vl) { sideVer.textContent = vl; sideVer.title = "CuliBox " + meta.version; sideVer.hidden = false; }
    if (!meta || !meta.env_label) return;
    const badge = $("env-badge"), strip = $("env-strip");
    badge.textContent = meta.env_label;
    window.__envLabel = meta.env_label; // the status line's lowercase copy reads this
    if (meta.env_color) { badge.style.background = meta.env_color; strip.style.background = meta.env_color; }
    badge.hidden = false;
    const sideEnv = $("side-env");
    if (sideEnv) {
      sideEnv.textContent = meta.env_label;
      // The chip's text colour follows its background (F79): operator config can be
      // any CSS colour, and the stylesheet's white text is unreadable on a pale one.
      // backgroundColor, not the shorthand, so no image or gradient can paint here.
      // The colour is applied only with a text colour picked for it: one the page
      // cannot read back as rgb (oklch(), lab() and color() come back unconverted)
      // is taken off again, so the chip keeps its stylesheet background and text.
      if (meta.env_color && CSS.supports("color", meta.env_color)) {
        sideEnv.style.backgroundColor = meta.env_color;
        const rgb = parseRgb(getComputedStyle(sideEnv).backgroundColor);
        if (rgb) sideEnv.style.color = envTextColor(rgb);
        else sideEnv.style.backgroundColor = "";
      }
      sideEnv.hidden = false;
    }
    strip.hidden = false;
    signalAttention(); // title = (N) [ENV] CuliBox, now that ENV is known
    updateStatusLine();
  } catch (_) {
    if (retry) setTimeout(() => initEnv(false), 2000); // else: no badge, no +sh
  }
})();

fontPrefs.applyCssVars();
initSettings();
initNotifyToggle();
initStore();
setInterval(sessionsTick, 3000);
// U8: crossing the mobile breakpoint (rotate, resize, devtools) changes which
// panes/screens are even in the DOM — mobileshell's own screen classes react
// immediately via CSS, but the sidebar's last-painted rows do not repaint
// themselves, so without this the operator sees last poll's shape for up to
// 3s. matchMedia's change event fires exactly on that edge; the query string
// is built from MOBILE_MAX_PX (mobileshell.js) so it cannot drift from the
// CSS breakpoint or from mobileActive()'s own edge. No new polling — this
// forces one extra refreshSessions() call only on the crossing itself.
// I1: also re-run renderGrid() on the same crossing — it is the one that
// reads currentMaximized() (tabs.js), so a live crossing (not just the next
// boot) re-lays the grid immediately: widening back past the breakpoint with
// a maximized cell restores it for desktop right away, instead of waiting
// for the next action that happens to call renderGrid().
if (typeof mobileMediaQuery === "function" && window.matchMedia) {
  window.matchMedia(mobileMediaQuery()).addEventListener("change", () => {
    // A phone has no pages: the dialog goes home closed, the panes come back. "force": the layout is leaving under
    // the page, so its leave hook (unsaved profile changes) is not asked: it ends the edit and keeps what was typed
    // in memory, for when that profile opens on the desktop page again.
    closePage("force");
    // Mobile's Files screen expects #files-pane back in #sidebar.
    // The pane-collapse html classes follow the layout: the stored state on
    // mobile (as at a fresh load), none on desktop, where app.css's
    // .files-collapsed rule would otherwise hide the tree inside the drawer.
    applyCollapsed(bootFlags(deskStorage(), mobileActive(window.innerWidth)).ignoreCollapsed ? { sessions: false, files: false } : readCollapsed());
    if (mobileActive(window.innerWidth)) restoreDrawerNodes();
    else paintSideCost(); // the footer was skipped while narrow
    paintSteerToggle(); // its meaning changed with the layout (steerRailOpen)
    refreshSessions();
    // F05: the terminals were built at the old width; a desktop's graphite and a phone's black are
    // set on the live xterm objects (pane tabs and docked shells), nothing is rebuilt. Before
    // renderGrid, which may build terminals of its own: this call has dropped the cache, so they read the tokens fresh.
    if (typeof applyTermThemeAll === "function") applyTermThemeAll();
    if (typeof renderGrid === "function") renderGrid();
  });
}
// Account limits change slowly and the file only updates when a claude statusLine
// renders — a 30s refresh keeps the % fresh and the countdown ticking without
// hammering /api/meta. Skipped while hidden.
setInterval(async () => {
  if (document.hidden) return;
  try {
    const m = await api("/api/meta");
    sysSeeVersion(m && m.version); // a different build than this tab booted with -> the "updated" banner
    globalThis.repliesEnabled = !!(m && m.replies === true);
    lastRateLimits = (m && m.rate_limits) || null;
    renderRateLimits(lastRateLimits);
    if (typeof vmaFromMeta === "function") vmaFromMeta(m && m.vm_apply); // the box VM restart: start the global poller if one runs
  } catch (_) {}
  // The store's lock state moves on the same slow cadence (TTL expiry, a lock
  // or unlock from the CLI), so it rides this tick rather than a timer of its own.
  refreshStore();
}, 30000);

// The one global keyboard dispatcher. shortcuts.js owns the table (which chord,
// which scope, which of terminal / editor / input / dialog / page may fire it);
// shortcutContext (shell.js) says where focus is now; the handlers below say
// what each binding does. Capture phase + stopPropagation (done by the
// dispatcher) because xterm listens on its textarea and forwards every
// combination it does not recognise into the running agent, so a bubbling
// handler would type the key into the process instead of acting on the app.
document.addEventListener("keydown", (e) => {
  dispatchShortcut(e, shortcutContext(document.activeElement, dialogsOpen(), !!currentPage(), mobileActive(window.innerWidth)), IS_MAC);
}, true);

// cellVisible: the cell's own element is attached and laid out. On a mobile
// screen other than the terminal, #grid is display:none but focusedCell still
// points at a cell nobody can see; acting on it would spawn a shell into, or
// change the font of, a hidden pane (spec "Hidden panes"). Returning false
// from a binding hands the key back to the browser.
function cellVisible(cell) {
  return !!(cell && cell.el && cell.el.isConnected && cell.el.getClientRects().length > 0);
}

// Desktop has no sessions pane to collapse; the combo is the sidebar <-> rail
// toggle (spec 7 keeps the binding), and mobile keeps the pane collapse.
bindShortcut("sidebar.toggle", (e, ctx) => {
  if (ctx.mobile) togglePane("sessions"); else toggleSidebar();
  return true;
});

// Desktop: the tree lives in the Files drawer, so the combo toggles that for
// the focused session; mobile keeps the pane collapse.
bindShortcut("files.toggle", (e, ctx) => {
  if (ctx.mobile) togglePane("files"); else toggleDrawer("files", (cellTerminal(focusedCell) || {}).id);
  return true;
});

// Jump to the Nth session. The number addresses the SIDEBAR list, which is the
// numbered thing on screen. The grid's panes are not a usable index: there can
// be fewer panes than sessions, and one pane can stack several.
//
// Ctrl/Cmd+digit, not Alt/Option+digit. A page CAN cancel Ctrl/Cmd+digit in
// Chrome and Firefox (only Ctrl/Cmd+T/N/W are out of reach), while Chrome on
// Linux binds Alt+1..9 to tab selection. Option+digit was dropped on macOS
// because it types [ ] { } | # on German and UK layouts. The sources are in the
// shortcuts.js header.
bindShortcut("session.jump", (e, ctx) => {
  const m = /^Digit([1-9])$/.exec(e.code); // .code, not .key: layout-independent under a modifier
  if (!m) return false;
  const s = jumpTarget(Number(m[1]), jumpOrder(lastSessions, ctx.mobile));
  // Out of range is a deliberate no-op, not a wrap-around: pressing 7 with four
  // sessions open must not land somewhere arbitrary. The handler still returns
  // true, so the digit is swallowed either way and never falls through to the agent.
  if (s) openTerminal(s.id, s.name);
  return true;
});

// Next that needs you (F69): Mod+Shift+J and the palette's command. Goes to the
// next session blocked on you, then those whose turn is done, in the sidebar's
// order (nextNeedsYou, deskshell.js), and wraps. "Goes to" is exactly what
// Mod+digit does: openTerminal focuses the session's pane or opens it. With
// nothing waiting it does nothing, but the key is still consumed (true), as an
// out-of-range digit is: it must not fall through to the browser's own binding.
function goNextNeedsYou() {
  const s = nextNeedsYou(lastSessions, selectedSessionId());
  if (s) openTerminal(s.id, s.name);
  return true;
}

// Desktop only: the phone has no sidebar groups, so the key is not ours there.
// A held key repeats: swallow it, or it would spin through every waiting session.
bindShortcut("session.nextNeeds", (e, ctx) => {
  if (ctx.mobile) return false;
  if (e.repeat) return true;
  return goNextNeedsYou();
});

// F6 / Shift+F6 (#177 call 12): the region cycle, sidebar (or the rail) -> the focused pane's header + strip -> its
// terminal -> the drawer. regioncycle.js owns the order and the step; this collects which regions are on screen,
// moves focus and paints the kit ring (the one :focus-visible ring, 2px --accent) on the region. It is bound in the
// capture-phase dispatcher above, so xterm never gets the key (it would send ESC[17~).
let regionRing = null; // the element wearing .region-focus now

function regionShown(el) {
  return !!(el && el.isConnected && !el.hidden && el.getClientRects().length > 0);
}

// regionEls: the element of each region that is on screen right now (absent = not shown).
function regionEls() {
  const out = {};
  const cell = focusedCell;
  const rail = $("rail48");
  if (regionShown($("sidebar"))) out.sidebar = $("sidebar");
  else if (regionShown(rail)) out.rail = rail;
  const page = typeof pageNow !== "undefined" && pageNow ? pageNow.dlg : null;
  if (page && regionShown(page)) out.page = page;
  else if (cellVisible(cell)) {
    if (regionShown(cell.bar)) out.header = cell.bar;
    if (cellTerminal(cell)) out.terminal = cell.body;
  }
  if (regionShown($("drawer"))) out.drawer = $("drawer");
  return out;
}

// regionHolding: the region that has focus, or null. The strip and the write banner belong to the header.
function regionHolding(els) {
  const ae = document.activeElement;
  if (!ae) return null;
  const cell = focusedCell;
  const header = [cell && cell.bar, cell && cell._elev, cell && cell._strip];
  for (const k of ["drawer", "page", "sidebar", "rail"]) if (els[k] && els[k].contains(ae)) return k;
  if (els.header && header.some((x) => x && x.contains(ae))) return "header";
  if (els.terminal && els.terminal.contains(ae)) return "terminal";
  return null;
}

const REGION_FOCUSABLE = 'button:not([disabled]), a[href], input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// regionFocus: put focus in region `name` and ring the part of it that took focus.
function regionFocus(name, els) {
  const cell = focusedCell;
  let target = null;
  let ring = els[name];
  if (name === "sidebar") {
    target = document.querySelector("#sessions .srow.sel .nm") || document.querySelector("#sessions .srow .nm") || $("side-new");
  } else if (name === "rail") {
    target = els.rail.querySelector(".ri.on") || els.rail.querySelector(".ri");
  } else if (name === "header") {
    // The ask strip is the part of the header with something to answer; otherwise the first tab or control.
    const strip = regionShown(cell._strip) ? cell._strip : null;
    target = (strip && strip.querySelector(REGION_FOCUSABLE)) || els.header.querySelector(REGION_FOCUSABLE);
    if (target && strip && strip.contains(target)) ring = strip;
  } else if (name === "terminal") {
    const tab = cell.view !== "terminal" ? null : focusedTerminal() || cellTerminal(cell);
    if (tab && tab.term) { tab.term.focus(); target = document.activeElement; }
    else if (cell._viewHost) { cell._viewHost.focus({ preventScroll: true }); target = document.activeElement; }
  } else if (name === "drawer") {
    // Folded, the explorer is its strip: its first button is the way in (the tree's rows are not on screen).
    const folded = $("drawer").dataset.folded === "1";
    target = (folded ? $("drawer-strip-tree") : $("drawer-tree").querySelector('.ft-node[tabindex="0"]')) || $("drawer-x");
  } else if (name === "page") {
    target = els.page.querySelector(REGION_FOCUSABLE);
    if (!target) { els.page.tabIndex = -1; target = els.page; }
  }
  if (!target) return false;
  if (target !== document.activeElement) target.focus();
  regionRingOn(ring);
  return true;
}

function regionRingOn(el) {
  if (regionRing) regionRing.classList.remove("region-focus");
  regionRing = el || null;
  if (regionRing) regionRing.classList.add("region-focus");
}

// The ring is the keyboard-focus cue for a region, so it goes when focus leaves that region.
document.addEventListener("focusin", (e) => {
  if (regionRing && !regionRing.contains(e.target)) regionRingOn(null);
}, true);

function cycleRegion(back) {
  const els = regionEls();
  const order = regionOrder({ sidebar: !!els.sidebar, rail: !!els.rail, header: !!els.header, terminal: !!els.terminal, drawer: !!els.drawer, page: !!els.page });
  const to = regionStep(order, regionHolding(els), back);
  if (!to) return false;
  return regionFocus(to, els);
}

// Desktop only: a phone has no regions. A held key repeats: swallow it, or it would spin round the page.
bindShortcut("ui.regionNext", (e, ctx) => {
  if (ctx.mobile) return false;
  if (e.repeat) return true;
  return cycleRegion(!!e.shiftKey);
});

// Ctrl+Shift+` -> + Shell below, in the focused window. With no visible focused
// cell it is not ours: false leaves the key alone. A held key over a visible
// cell is swallowed (true) so it is not typed into the terminal, but must not
// start a shell per repeat.
bindShortcut("shell.below", (e) => {
  if (!cellVisible(focusedCell)) return false; // before the repeat check: a hidden cell answers the same held or not
  if (e.repeat) return true;
  spawnShellIn(focusedCell, "below");
  return true;
});

// Live zoom the focused pane: Mod + '='/'+' bigger, '-' smaller, '0' reset (Mod
// is Cmd on mac, Ctrl elsewhere). A hidden pane is not zoomed: false hands the
// key back to the browser's own page zoom, as zoomFocused / resetZoomFocused do
// when the focused tab has nothing to zoom.
bindShortcut("zoom.in", () => cellVisible(focusedCell) && zoomFocused(+fontPrefs.STEP));
bindShortcut("zoom.out", () => cellVisible(focusedCell) && zoomFocused(-fontPrefs.STEP)); // key "-" only: Ctrl+_ is readline undo in terminals
bindShortcut("zoom.reset", () => cellVisible(focusedCell) && resetZoomFocused());

bindShortcut("help.open", () => {
  showShortcuts();
  return true;
});

// zoomFocused / resetZoomFocused are the named zoom path, shared by the keyboard
// bindings above and the command palette. Return false when the focused tab has
// nothing to zoom, so the caller can leave the key event alone.
function zoomableTab() {
  // A docked shell with the keyboard zooms itself, not the agent beside it.
  const sh = focusedCell && focusedCell.focusedShell ? focusedCell.focusedShell() : null;
  if (sh) return sh;
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
