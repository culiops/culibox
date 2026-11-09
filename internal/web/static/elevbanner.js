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
// accesstab.js's globals it leans on: accData (the cached /aws status, which holds
// the reason), accLoad, accDrop, accBusy, accViews.

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
  accLoad(s.id, true);
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
  const held = el.contains(document.activeElement);
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
  const eb = { strong, span, btn, sid: "", sig: "" };
  el._eb = eb;
  btn.onclick = () => {
    const sid = eb.sid;
    if (!sid || accBusy.has(sid)) return;
    accDrop(sid, (force) => {
      paneElevSync(cell, (lastSessions || []).find((x) => x.id === sid) || null);
      const view = accViews.get(sid);
      if (view) view(force);
    });
  };
  return el;
}

// ebPaint: put the model into the banner's nodes, writing only what differs.
function ebPaint(el, m, sid, busy) {
  const eb = el._eb;
  eb.sid = sid;
  const sig = m.key + "|" + (busy ? "1" : "0");
  if (sig === eb.sig) return;
  eb.sig = sig;
  const name = "Write access to " + m.conn;
  if (eb.strong.textContent !== name) { eb.strong.textContent = name; eb.strong.title = name; }
  if (eb.span.textContent !== m.detail) { eb.span.textContent = m.detail; eb.span.title = m.detail; }
  const label = busy ? "Dropping…" : "Drop now";
  if (eb.btn.textContent !== label) eb.btn.textContent = label;
  eb.btn.disabled = busy;
  // A control at work is busy, not closed: the kit does not dim aria-busy.
  if (busy) eb.btn.setAttribute("aria-busy", "true"); else eb.btn.removeAttribute("aria-busy");
}

// paneElevSync: make the pane match its session `s` (the pane's agent, or null):
// create, update or remove cell._elev. Called once per poll per pane and at cell
// build. On a phone it only ever removes.
function paneElevSync(cell, s) {
  if (!cell || !cell.bar) return;
  if (!s || (typeof mobileActive === "function" && mobileActive(window.innerWidth))) { ebRemove(cell); return; }
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
  ebPaint(el, m, s.id, accBusy.has(s.id));
  if (placed && typeof positionDrawer === "function") positionDrawer();
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { paneElevSync, elevWatch };
}
