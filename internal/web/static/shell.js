"use strict";

// UI shell primitives: transient toast, <dialog> plumbing, and the sidebar
// splitter. Deliberately free of app state and API calls — everything here is
// generic chrome that app.js and palette.js drive. Loaded before app.js, so it
// only DEFINES functions; boot wiring happens from app.js.

// --- toast -------------------------------------------------------------------
// One slot, one message. A second toast replaces the first rather than stacking:
// the messages here are transient confirmations, and a stack of them over a
// terminal is noise.
let toastTimer = null;

// showToast(msg, {undo, ms, error}) — `undo` is a callback; supplying it reveals
// the Undo button and clicking it dismisses the toast after running the callback.
function showToast(msg, opts) {
  opts = opts || {};
  const box = document.getElementById("toast");
  const undoBtn = document.getElementById("toast-undo");
  if (!box) return;
  document.getElementById("toast-msg").textContent = msg;
  box.classList.toggle("err", !!opts.error);
  if (opts.undo) {
    undoBtn.hidden = false;
    undoBtn.onclick = () => { hideToast(); opts.undo(); };
  } else {
    undoBtn.hidden = true;
    undoBtn.onclick = null;
  }
  box.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, opts.ms || 6000);
}

function hideToast() {
  clearTimeout(toastTimer);
  const box = document.getElementById("toast");
  if (box) box.classList.remove("show");
}

// --- dialogs -----------------------------------------------------------------
// focusTerminal returns the caret to the focused pane. Every dialog close path
// runs it: a native <dialog> restores focus to whatever opened it, which in this
// app means keystrokes stop reaching the agent — the one thing that must not
// happen after closing a modal.
function focusTerminal() {
  const tab = typeof focusedCell !== "undefined" && focusedCell ? focusedCell.activeTab : null;
  if (tab && tab.kind === "terminal" && tab.term) tab.term.focus();
}

function openDialog(id) {
  const d = document.getElementById(id);
  if (d && !d.open) d.showModal();
  return d;
}

function closeDialog(id) {
  const d = document.getElementById(id);
  if (d && d.open) d.close();
}

// openSettings shows the settings dialog with one tab selected ("manage" =
// profiles, "settings" = font sizes — the ids are the pre-existing panel ids,
// kept so every handler that already targets them keeps working).
function openSettings(tab) {
  selectSettingsTab(tab || "manage");
  openDialog("settings-modal");
}

function selectSettingsTab(name) {
  for (const b of document.querySelectorAll("#settings-tabs button")) {
    b.classList.toggle("act", b.dataset.tab === name);
  }
  for (const p of document.querySelectorAll("#settings-modal .tab-panel")) {
    p.hidden = p.id !== name;
  }
}

function initDialogs() {
  for (const btn of document.querySelectorAll("[data-close]")) {
    btn.onclick = () => closeDialog(btn.dataset.close);
  }
  for (const b of document.querySelectorAll("#settings-tabs button")) {
    b.onclick = () => selectSettingsTab(b.dataset.tab);
  }
  for (const d of document.querySelectorAll("dialog")) {
    // A click landing on the dialog element itself is a backdrop click: the
    // padding is 0 and the children cover the box, so nothing else can target it.
    d.addEventListener("click", (e) => { if (e.target === d) d.close(); });
    // Fires for Esc, ×, backdrop and programmatic close alike.
    d.addEventListener("close", focusTerminal);
  }
  const settingsOpen = document.getElementById("settings-open");
  if (settingsOpen) settingsOpen.onclick = () => openSettings("manage");
  const toastX = document.getElementById("toast-x");
  if (toastX) toastX.onclick = hideToast;
}

// --- sidebar splitter --------------------------------------------------------
// Drag height between the Sessions and Files panes. The ratio is a percentage on
// --sidebar-split, persisted and re-applied pre-paint by the inline <head>
// script so a reload does not flash the default split.
const SPLIT_KEY = "agent-console:sidebarSplit";
const SPLIT_MIN_PX = 56; // must match the smaller of the two min-heights in CSS

function setSidebarSplit(pct) {
  pct = Math.min(85, Math.max(15, pct));
  document.documentElement.style.setProperty("--sidebar-split", pct + "%");
  try { localStorage.setItem(SPLIT_KEY, String(pct)); } catch (_) {}
}

function initSplitter() {
  const handle = document.getElementById("split-h");
  const top = document.getElementById("sessions-pane");
  const bottom = document.getElementById("files-pane");
  if (!handle || !top || !bottom) return;

  handle.addEventListener("pointerdown", (e) => {
    // Pointer events (not mouse) so the drag also works under touch; the handle
    // sets touch-action:none so the browser does not scroll instead.
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");

    const move = (ev) => {
      const a = top.getBoundingClientRect();
      const b = bottom.getBoundingClientRect();
      const total = b.bottom - a.top;
      if (total <= 0) return;
      // Clamp in pixels first so neither pane can be dragged out of existence,
      // then convert — a percentage clamp alone would not respect the minimums.
      const y = Math.min(b.bottom - SPLIT_MIN_PX, Math.max(a.top + SPLIT_MIN_PX, ev.clientY));
      setSidebarSplit(((y - a.top) / total) * 100);
    };
    const up = (ev) => {
      handle.releasePointerCapture(ev.pointerId);
      handle.classList.remove("dragging");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", up);
      handle.removeEventListener("pointercancel", up);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up);
    handle.addEventListener("pointercancel", up);
  });

  // Double-click restores the default ratio — a cheap escape hatch from a drag
  // that went wrong on a small screen.
  handle.addEventListener("dblclick", () => setSidebarSplit(45));
}
