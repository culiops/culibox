"use strict";

// The attention chip (UI plan 7, #177; DD§A6, §B3): a small amber status that floats over the top of the
// main area while a session is blocked on you and sits on no visible pane (a pane that shows it has its own
// strip; Done never gets a chip). It names the first such cause in the sidebar's order, adds " · +N" for the
// others, and has two controls: Go (opens the named session; the hint is the next-needs chord) and a close that hides the chip
// until the set of blocked causes changes. The model is notifymodel.js's chipModel; this file draws it.
//
// Desktop only: nothing is created while mobileActive(), and crossing to a phone width hides it. It is a
// floating status, not a toast: it never uses the toast slot. Its host is fixed over the main area, centred
// over #grid (or #page-host while a page hides the grid), at ATTN_CHIP_TOP below the area's top (the 36 px pane
// header plus 8: DD§B3 measured 324 x 32 at y 44), re-placed when either area changes size (a drawer, a
// resize) by a ResizeObserver, never by polling.
//
// Repaint only on a model change: the element is built once and its text nodes are touched only when the
// model's key moved, because it is role=status and a screen reader would say it again on every poll. Every
// string goes in as a text node. attnChipSync is cheap (one pass over the session list) and runs on each
// session poll and wherever the visible panes can change (renderGrid, Cell.activate, openPage, closePage).

const ATTN_CHIP_TOP = 44;

let attnDismissed = ""; // dismissKey the user closed the chip on
let attnHost = null;
let attnParts = null;
let attnPaintedKey = null;
let attnModel = null;
let attnObserved = false;

function attnChipBuild() {
  const host = document.createElement("div");
  host.setAttribute("id", "attn-chip-host");
  host.hidden = true;
  const chip = document.createElement("div");
  chip.className = "attn-chip";
  chip.setAttribute("role", "status");
  chip.setAttribute("aria-live", "polite");
  const dot = document.createElement("span");
  dot.className = "dot dot-needs";
  const text = document.createElement("span");
  const name = document.createElement("span");
  name.className = "w-600";
  const rest = document.createTextNode("");
  text.append(name, rest);
  const go = document.createElement("button");
  go.type = "button";
  go.className = "btn btn-sm";
  go.appendChild(document.createTextNode("Go"));
  const hint = document.createElement("span");
  hint.className = "kbd-hint";
  go.appendChild(hint);
  go.onclick = () => {
    // The session the chip names (the first off-screen one of a shared cause), not the next in the cycle.
    if (!attnModel) return;
    const sess = (lastSessions || []).find((x) => x.id === attnModel.sid);
    openTerminal(attnModel.sid, (sess && sess.name) || attnModel.sid);
  };
  const x = document.createElement("button");
  x.type = "button";
  x.className = "btn btn-ghost btn-icon btn-sm";
  x.setAttribute("aria-label", "Hide until something else needs you");
  x.title = "Hide until something else needs you";
  x.appendChild(document.createTextNode("✕"));
  x.onclick = () => {
    if (attnModel) attnDismissed = attnModel.dismissKey;
    attnChipSync();
  };
  chip.append(dot, text, go, x);
  host.appendChild(chip);
  document.body.appendChild(host);
  attnParts = { name, rest, hint };
  return host;
}

// attnChipPlace: the host spans the area the chip is centred over (it centres its child), at the fixed top.
function attnChipPlace() {
  if (!attnHost) return;
  const onPage = document.body.classList.contains("page-open");
  const area = document.getElementById(onPage ? "page-host" : "grid");
  if (!area || typeof area.getBoundingClientRect !== "function") return;
  const r = area.getBoundingClientRect();
  attnHost.style.left = r.left + "px";
  attnHost.style.width = r.width + "px";
  attnHost.style.top = r.top + ATTN_CHIP_TOP + "px";
}

function attnChipObserve() {
  if (attnObserved || typeof ResizeObserver !== "function") return;
  attnObserved = true;
  const ro = new ResizeObserver(() => attnChipPlace());
  for (const id of ["grid", "page-host"]) {
    const el = document.getElementById(id);
    if (el) ro.observe(el);
  }
}

function attnChipHide() {
  if (attnHost && !attnHost.hidden) attnHost.hidden = true;
}

// attnChipSync(place): `place` is true from the hooks that can move the area (grid repaint, pane switch, page
// open/close); the poll leaves it off, so a steady chip costs no layout read. It is also placed when it becomes
// shown or its key changes.
function attnChipSync(place) {
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) { attnChipHide(); return; }
  const order = jumpOrder(lastSessions, false);
  // A dismissal belongs to the exact set of causes it hid: whenever the set differs (a cause left, even while the
  // chip is not due), it is forgotten, so the same ask coming back shows the chip again.
  if (attnDismissed && attnDismissed !== chipDismissKey(order)) attnDismissed = "";
  const m = chipModel(order, visibleSessionIds(), attnDismissed);
  attnModel = m;
  if (!m) { attnChipHide(); return; }
  if (!attnHost) { attnHost = attnChipBuild(); attnChipObserve(); }
  const changed = m.key !== attnPaintedKey || attnHost.hidden;
  if (m.key !== attnPaintedKey) {
    attnParts.name.textContent = m.name;
    attnParts.rest.textContent = m.rest;
    attnParts.hint.textContent = shortcutHint("session.nextNeeds", IS_MAC);
    attnPaintedKey = m.key;
  }
  if (place || changed) attnChipPlace();
  if (attnHost.hidden) attnHost.hidden = false;
}
