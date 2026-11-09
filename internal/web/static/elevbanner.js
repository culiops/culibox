"use strict";

// The write-access banner (UI plan 6, digest M6.4): while a session has a write
// window open, one red band sits directly under its pane header, above the ask
// strip, on every view of that pane (Terminal, Files, Activity, Access, and the
// docked Shell, which hangs off the pane body below it). It names the connection,
// the clock face, the minutes left and the reason, and carries the pane's one
// "Drop now". Desktop only: it is never created while mobileActive(), and the
// crossing to a phone width takes it out in the same sync.
//
// The model is accessmodel.js's elevBanner; this file draws it. It is re-synced
// from Cell.syncNeeds / Cell.repaintReply (tabs.js), i.e. on the 3 s session
// poll, with no timer of its own. The element is built once per pane and then
// painted in place: its text nodes are touched only when something visible moved
// (the model's `key`), because it is role=status and a screen reader would
// otherwise say it again on every poll, and a rebuilt Drop now would lose focus.
// Every string goes in through textContent.
//
// Drop now is never `disabled`: a focused button that becomes disabled loses focus
// to <body> (the browser's focus fixup), and a keyboard user pressing Enter on it
// would be dropped out of the pane before the poll removes the banner. While a
// request is in flight, or a Grant is, it is aria-disabled and the click handler
// refuses, which the kit draws as the same closed look. `eb.pressed` records that
// THIS button started a Drop: only then does it read "Dropping…", and it stays
// inert after a success until the window changes or the banner leaves (the list
// catches up on the next poll), so a second press cannot send a second DELETE.
//
// accesstab.js's globals it leans on: accData (the cached /aws status, which holds
// the reason), accLoad (which returns a promise that settles once the read is stored),
// accDrop, accBusy, accViews.

// Per session id: the signature of its live write window the last time a status
// read was asked for it (absent = none). See elevWatch.
const ELEV_SEEN = new Map();

// elevWindowSig: the session's live write windows as one string, "" with none.
// Live means `until` is still ahead (isElevated), so a window the list still
// carries after it lapsed reads as none.
function elevWindowSig(s, nowMs) {
  const conns = s && Array.isArray(s.aws_connections) ? s.aws_connections : [];
  return [s && s.aws].concat(conns)
    .filter((c) => isElevated(c, nowMs))
    .map((c) => (c.name || "") + "@" + c.elevated_until)
    .join("|");
}

// elevWatch: ask for the Access status (accLoad force) once per change of the
// session's live write window: it opens, is extended, or closes (to none). The
// reason the banner shows and the "Write access ended" line the Access row keeps
// both come from that status, and nothing else re-reads it while another view is
// up. A session that never had a window costs nothing, and polls of an unchanged
// window cost nothing. accLoad drops a forced read while one is in flight, so a
// change that lands then is not recorded and is asked again on the next poll.
function elevWatch(s) {
  if (!s || !s.id) return;
  const sig = elevWindowSig(s, Date.now());
  if (sig === (ELEV_SEEN.get(s.id) || "")) return;
  const d = accData.get(s.id);
  if (d && d.inflight) return;
  if (sig) ELEV_SEEN.set(s.id, sig); else ELEV_SEEN.delete(s.id);
  // The read answers (accLoad returns its promise) once the status is stored: re-sync the panes of this session
  // then, not at the next poll. A read that was refused answers nothing, and the re-sync is then a no-op.
  Promise.resolve(accLoad(s.id, true)).then(() => elevResync(s.id), () => {});
}

// elevResync: re-sync every pane that shows a banner for session `sid` (Cell.syncElev finds the session the pane's
// terminal shows now, so a pane that moved on is synced to its own). The list says a window is over before the
// cached status does, and liveWindowConn falls back to that status; this is the moment the status has caught up.
// It asks for no read of its own: the window signature already matches. `cells` is tabs.js's list of panes.
function elevResync(sid) {
  if (typeof cells === "undefined" || !Array.isArray(cells)) return;
  for (const c of cells) if (c._elev && c._elev._eb.sid === sid && typeof c.syncElev === "function") c.syncElev();
}

// elevPrune: forget the sessions that left the list, as accPrune does, so ELEV_SEEN
// holds no entry for a session that closed with a window open. An empty list is
// the page before its first poll, not "everything is gone".
function elevPrune() {
  if (!lastSessions || !lastSessions.length) return;
  const live = new Set(lastSessions.map((x) => x.id));
  for (const id of [...ELEV_SEEN.keys()]) if (!live.has(id)) ELEV_SEEN.delete(id);
}

// ebFocusPane: where the keyboard goes when the banner is removed with focus on
// it (Drop now, the one control it holds): the pane, never <body>. A pane showing
// Activity or Access hands it to the view host, one on its terminal to the
// terminal's own keyboard path; anything else gets the pane element itself.
function ebFocusPane(cell) {
  const host = cell._viewHost;
  if (host && typeof host.focus === "function") { host.focus({ preventScroll: true }); return; }
  const t = cell.activeTab;
  if (t && t.kind === "terminal" && typeof t.onShow === "function") { t.onShow(); return; }
  if (cell.el && typeof cell.el.focus === "function") {
    cell.el.tabIndex = -1;
    cell.el.focus({ preventScroll: true });
  }
}

// ebRemove: take the pane's banner out, and re-measure the Files drawer, which
// hangs below it (an insert or removal inside a pane does not fire the grid's
// ResizeObserver).
function ebRemove(cell) {
  const el = cell._elev;
  if (!el) return;
  // Focus is the banner's when it is inside it, or when this banner's Drop was pressed and focus has
  // since fallen to <body> (nothing else claimed it): either way it goes to the pane, not to <body>.
  const a = document.activeElement;
  const held = el.contains(a) || (el._eb.pressed && (!a || a === document.body));
  el.remove();
  cell._elev = null;
  if (held) ebFocusPane(cell);
  if (typeof positionDrawer === "function") positionDrawer();
}

// ebBuild: div.elev > strong, span, the spacer, the button (digest M6.4 verbatim).
// What it shows is ebPaint's. The button reads the pane's CURRENT session at press
// time (a pane can change which session it shows), and its repaint re-syncs this
// pane and the Access view mounted for that session, if any.
function ebBuild(cell) {
  const el = document.createElement("div");
  el.className = "elev";
  el.setAttribute("role", "status");
  const strong = document.createElement("strong");
  const span = document.createElement("span");
  const gap = document.createElement("span");
  gap.className = "ph-spacer";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn btn-danger btn-sm";
  el.append(strong, span, gap, btn);
  // pressed: this button started a Drop and its window has not changed since. settled: accDrop has
  // answered it (true), so the pressed state outlives the request. wsig/pressedSig: the live window
  // when painted / when pressed.
  const eb = { strong, span, btn, sid: "", sig: "", pressed: false, settled: false, wsig: "", pressedSig: "" };
  el._eb = eb;
  const resync = (force) => {
    paneElevSync(cell, (lastSessions || []).find((x) => x.id === eb.sid) || null);
    const view = accViews.get(eb.sid);
    if (view) view(force);
  };
  btn.onclick = () => {
    const sid = eb.sid;
    if (!sid || eb.pressed || accBusy.has(sid)) return;
    eb.pressed = true; // before accDrop: its first repaint is synchronous and reads it
    eb.settled = false;
    eb.pressedSig = eb.wsig;
    // Only an explicit true counts as dropped; a refusal, a failure or a throw all leave a button again.
    Promise.resolve(accDrop(sid, resync)).then((ok) => ok === true, () => false).then((dropped) => {
      if (dropped) { eb.settled = true; return; } // inert until the list says the window is over
      eb.pressed = false; // refused or failed (accDrop toasted it): a button again
      const a = document.activeElement;
      if (el.parentNode && (!a || a === document.body)) btn.focus({ preventScroll: true });
      resync(true);
    });
  };
  return el;
}

// ebPaint: put the model into the banner's nodes, writing only what differs.
function ebPaint(el, m, sid, busy, wsig) {
  const eb = el._eb;
  eb.sid = sid;
  eb.wsig = wsig;
  // A Drop that went through ends with its window; a different live window is a new question.
  if (eb.pressed && eb.settled && wsig !== eb.pressedSig) eb.pressed = eb.settled = false;
  const inert = busy || eb.pressed;
  const sig = m.key + "|" + (inert ? "1" : "0") + (eb.pressed ? "1" : "0");
  if (sig === eb.sig) return;
  eb.sig = sig;
  const name = "Write access to " + m.conn;
  if (eb.strong.textContent !== name) { eb.strong.textContent = name; eb.strong.title = name; }
  if (eb.span.textContent !== m.detail) { eb.span.textContent = m.detail; eb.span.title = m.detail; }
  const label = eb.pressed ? "Dropping…" : "Drop now";
  if (eb.btn.textContent !== label) eb.btn.textContent = label;
  // aria-disabled, never disabled (see the header): the click handler is the guard.
  if (inert) eb.btn.setAttribute("aria-disabled", "true"); else eb.btn.removeAttribute("aria-disabled");
  // A Drop at work is busy, not closed: the kit dims aria-disabled but not aria-busy.
  if (eb.pressed) eb.btn.setAttribute("aria-busy", "true"); else eb.btn.removeAttribute("aria-busy");
}

// paneElevSync: make the pane match its session `s` (the pane's agent, or null):
// create, update or remove cell._elev. Called once per poll per pane and at cell
// build. On a phone it only ever removes.
function paneElevSync(cell, s) {
  if (!cell || !cell.bar) return;
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) { ebRemove(cell); return; }
  elevPrune();
  if (!s) { ebRemove(cell); return; }
  elevWatch(s);
  const d = accData.get(s.id);
  const m = elevBanner(s, d ? d.aws : null, Date.now());
  if (!m) { ebRemove(cell); return; }
  let el = cell._elev;
  let placed = false;
  if (!el) { el = ebBuild(cell); cell._elev = el; }
  // Directly under the header, so ahead of the ask strip (Cell.syncStrip inserts
  // its strip after this element).
  if (!el.parentNode) { cell.bar.after(el); placed = true; }
  ebPaint(el, m, s.id, accBusy.has(s.id), elevWindowSig(s, Date.now()));
  if (placed && typeof positionDrawer === "function") positionDrawer();
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { paneElevSync, elevWatch, elevResync };
}
