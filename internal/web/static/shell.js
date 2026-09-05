"use strict";

// UI shell primitives: transient toast, <dialog> plumbing, and the sidebar
// splitter. Deliberately free of app state and API calls: everything here is
// generic chrome that app.js and palette.js drive. Loaded before app.js, so it
// only DEFINES functions; boot wiring happens from app.js.

// --- toast -------------------------------------------------------------------
// One slot, one message. A second toast replaces the first rather than stacking:
// the messages here are transient confirmations, and a stack of them over a
// terminal is noise.
let toastTimer = null;

// showToast(msg, {undo, ms, error}): `undo` is a callback; supplying it reveals
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
  // A numeric `progress` (0..1) reveals the rail; anything else hides it, so an
  // ordinary toast never inherits a stale bar from a finished transfer.
  const bar = document.getElementById("toast-bar");
  if (bar) {
    if (typeof opts.progress === "number") {
      bar.hidden = false;
      bar.firstElementChild.style.width = Math.max(0, Math.min(1, opts.progress)) * 100 + "%";
    } else {
      bar.hidden = true;
    }
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
// app means keystrokes stop reaching the agent: the one thing that must not
// happen after closing a modal.
function focusTerminal() {
  // A nested dialog (a confirm raised from Settings) closes while its parent is
  // still on screen. Handing focus to the pane underneath there would silently
  // stop keystrokes reaching the agent, with nothing visible to explain it.
  if (!shouldRefocusTerminal(document.querySelectorAll("dialog[open]").length)) return;
  const tab = typeof focusedTerminal === "function" ? focusedTerminal() : null;
  if (tab && tab.term) tab.term.focus();
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
// profiles, "settings" = font sizes: the ids are the pre-existing panel ids,
// kept so every handler that already targets them keeps working).
function openSettings(tab) {
  selectSettingsTab(tab || "manage");
  openDialog("settings-modal");
}

function selectSettingsTab(name) {
  for (const b of document.querySelectorAll("#settings-tabs .t")) {
    const on = b.dataset.tab === name;
    b.classList.toggle("on", on);
    b.setAttribute("aria-selected", on ? "true" : "false");
  }
  for (const p of document.querySelectorAll("#settings-modal .dbd")) {
    p.hidden = p.id !== name;
  }
  // Each tab owns its own actions, so each has its own footer rather than one
  // footer rebuilt three ways — the buttons keep their ids and their listeners.
  for (const f of document.querySelectorAll("#settings-modal .dft")) {
    f.hidden = f.id !== "foot-" + name;
  }
  // The profile editor is the only tab with two columns to fill; the other two
  // are a single column and a taller box would just be empty at the bottom.
  document.getElementById("settings-modal").classList.toggle("tall", name === "manage");
  // Deliberately left alone: on a narrow screen the profile list and its editor
  // are two views gated by an "editing" class, not two columns, and switching
  // to another tab and back must not silently reset that view. Losing an
  // in-progress edit because someone glanced at Appearance is the worse
  // surprise, and the way back to the list is one tap away either way.
  // The inline error belongs to the action that raised it, not to the dialog.
  setDialogError("settings-modal", "");
  // The Access-tokens tab loads its list lazily whenever it is shown.
  if (name === "tokens" && typeof loadTokens === "function") loadTokens();
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
    //
    // The mousedown must have landed there too. A `click` fires on the nearest
    // common ancestor of press and release, so drag-selecting text inside a
    // textarea and releasing past the dialog edge would otherwise synthesise a
    // backdrop click and dismiss the profile editor mid-edit.
    let pressedBackdrop = false;
    d.addEventListener("mousedown", (e) => { pressedBackdrop = e.target === d; });
    d.addEventListener("click", (e) => { if (pressedBackdrop && e.target === d) d.close(); });
    // Fires for Esc, ×, backdrop and programmatic close alike.
    d.addEventListener("close", focusTerminal);
  }
  const settingsOpen = document.getElementById("settings-open");
  if (settingsOpen) settingsOpen.onclick = () => openSettings("manage");
  const toastX = document.getElementById("toast-x");
  if (toastX) toastX.onclick = hideToast;
}

// collapsedFromDOM reads the pane-collapse state off the root element's own
// classes, which is what the CSS is actually keyed on and what is actually
// rendered right now. localStorage (panes.js's readCollapsed) is only the
// restore seed for boot and the value togglePane writes back — it is a
// snapshot, and snapshots go stale: a second tab can collapse a pane and
// write its own storage entry while this tab's DOM (and this tab's next
// localStorage read, if it raced the write or the write itself threw) still
// disagrees. Anything that needs to know "is a pane collapsed right now" —
// the splitter guards below and app.js's renderCollapsedSummaries — should
// read the DOM, not storage.
function collapsedFromDOM() {
  const root = document.documentElement;
  return { sessions: root.classList.contains("sessions-collapsed"),
           files: root.classList.contains("files-collapsed") };
}

// --- sidebar splitter --------------------------------------------------------
// Drag height between the Sessions and Files panes. The ratio is a percentage on
// --sidebar-split, persisted and re-applied pre-paint by the inline <head>
// script so a reload does not flash the default split.
const SPLIT_KEY = "agent-box:sidebarSplit";

// setSidebarSplit applies the ratio; persist() writes it. They are separate
// because a drag calls this on every pointermove, and a synchronous
// localStorage write per move is dozens of writes per drag.
function setSidebarSplit(pct) {
  pct = Math.min(85, Math.max(15, pct));
  document.documentElement.style.setProperty("--sidebar-split", pct + "%");
  return pct;
}

function persistSidebarSplit(pct) {
  try { localStorage.setItem(SPLIT_KEY, String(pct)); } catch (_) {}
}

// --- sidebar width -----------------------------------------------------------
// The sidebar was a hardcoded 280px, which at depth 3 left a file name ~230px
// after indentation: truncation was structural, not incidental. Dragging is the
// root-cause fix; the row tooltip covers whatever is still too long.
const WIDTH_KEY = "agent-box:sidebarWidth";
const SIDEBAR_MIN = 200, SIDEBAR_MAX = 560; // also inlined in index.html's pre-paint script

function setSidebarWidth(px) {
  px = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, px));
  document.documentElement.style.setProperty("--sidebar-w", px + "px");
  return px;
}

function persistSidebarWidth(px) {
  try { localStorage.setItem(WIDTH_KEY, String(px)); } catch (_) {}
}

// minHeightOf reads the pane's CSS minimum rather than hardcoding it: the two
// viewports use different values, and a constant here drifts from the stylesheet
// silently (the JS would allow a drag that CSS then refuses to render, and the
// unhonourable ratio would still get persisted).
function minHeightOf(el) {
  const v = parseFloat(getComputedStyle(el).minHeight);
  return isFinite(v) && v > 0 ? v : 56;
}

function initSplitter() {
  const sidebar = document.getElementById("sidebar");
  const handle = document.getElementById("split-h");
  const top = document.getElementById("sessions-pane");
  const bottom = document.getElementById("files-pane");
  if (!sidebar || !handle || !top || !bottom) return;

  handle.addEventListener("pointerdown", (e) => {
    // A collapsed pane is a fixed bar; there is nothing to size against, and
    // the clamp below would invert. The CSS hides the handle too — this guard
    // covers the case where a drag is already in flight when a pane collapses.
    if (typeof splitterLive === "function" &&
        !splitterLive(collapsedFromDOM())) return;
    // Pointer events (not mouse) so the drag also works under touch; the handle
    // sets touch-action:none so the browser does not scroll instead.
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");
    let pct = null;

    const move = (ev) => {
      const a = top.getBoundingClientRect();
      const b = bottom.getBoundingClientRect();
      // The percentage is a flex-basis, so it resolves against the FLEX
      // CONTAINER's content height (all of #sidebar, spawn bar and handle
      // included), not against the two panes. Dividing by the panes alone made
      // the handle overshoot the pointer by ~12% on desktop and ~31% on mobile.
      const space = sidebar.clientHeight;
      if (space <= 0) return;
      // Clamp in pixels first so neither pane can be dragged out of existence;
      // a percentage clamp alone would not respect the minimums.
      const y = Math.min(b.bottom - minHeightOf(bottom),
        Math.max(a.top + minHeightOf(top), ev.clientY));
      pct = setSidebarSplit(((y - a.top) / space) * 100);
    };
    const up = (ev) => {
      handle.releasePointerCapture(ev.pointerId);
      handle.classList.remove("dragging");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", up);
      handle.removeEventListener("pointercancel", up);
      if (pct !== null) persistSidebarSplit(pct);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up);
    handle.addEventListener("pointercancel", up);
  });

  // Double-click restores the default ratio: a cheap escape hatch from a drag
  // that went wrong on a small screen.
  handle.addEventListener("dblclick", () => {
    // Same guard as pointerdown above: a double-click on a hidden handle must
    // not reset the split either.
    if (typeof splitterLive === "function" &&
        !splitterLive(collapsedFromDOM())) return;
    persistSidebarSplit(setSidebarSplit(45));
  });
}

function initWidthSplitter() {
  const sidebar = document.getElementById("sidebar");
  const handle = document.getElementById("split-v");
  if (!sidebar || !handle) return;

  handle.addEventListener("pointerdown", (e) => {
    // The handle is display:none under 700px, so a drag cannot start there and
    // no viewport check is needed here.
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    handle.classList.add("dragging");
    let px = null;

    const move = (ev) => {
      // The sidebar starts at the viewport's left edge, so its width IS the
      // pointer's x. No flex-basis arithmetic like the horizontal splitter's:
      // this is a plain width on a flex-shrink:0 item.
      px = setSidebarWidth(ev.clientX - sidebar.getBoundingClientRect().left);
    };
    const up = (ev) => {
      handle.releasePointerCapture(ev.pointerId);
      handle.classList.remove("dragging");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", up);
      handle.removeEventListener("pointercancel", up);
      // Persist once per drag, not once per pointermove: a synchronous
      // localStorage write per move is dozens of writes per drag.
      if (px !== null) persistSidebarWidth(px);
      // Unlike the horizontal splitter, this one changes the GRID's width, so
      // every open terminal has to recompute its column count. On pointerup
      // only: fitAll on every pointermove re-measures each xterm per pixel.
      fitAll();
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up);
    handle.addEventListener("pointercancel", up);
  });

  // Double-click restores the default, mirroring the horizontal handle's escape
  // hatch from a drag that went wrong. 322 mirrors tokens.css's --sidebar-w
  // default; the two must move together.
  handle.addEventListener("dblclick", () => { persistSidebarWidth(setSidebarWidth(322)); fitAll(); });
}

// --- ask dialog --------------------------------------------------------------
// One shared <dialog> answers every question the app used to hand to the
// browser's alert/confirm/prompt. The decision logic is split out from the DOM
// wiring because that is the part with teeth: a promise that never settles
// hangs the caller forever, and a confirm button that unlocks on the wrong
// input defeats the typed-name gate on protected folders.

// A <dialog> reports returnValue "" for Esc, a backdrop click, and a bare
// close(). Treat anything that is not the confirm button as a cancel, so an
// unrecognised close path fails safe.
function askOutcome(returnValue) {
  return returnValue === "confirm" ? "confirm" : "cancel";
}

// matchesRequired gates the typed-name confirmations. An empty or missing
// requirement matches nothing: it must never become a skeleton key.
function matchesRequired(typed, required) {
  if (typeof typed !== "string" || typeof required !== "string") return false;
  if (required === "") return false;
  return typed.trim() === required;
}

// null is "cancelled"; "" is "submitted, but blank". The callers rely on the
// difference, so they are not collapsed.
function promptValue(returnValue, rawInput) {
  if (askOutcome(returnValue) !== "confirm") return null;
  return typeof rawInput === "string" ? rawInput.trim() : "";
}

// See focusTerminal: a nested dialog closing must not hand focus to the pane
// underneath while another dialog is still on screen.
function shouldRefocusTerminal(openDialogCount) {
  return openDialogCount === 0;
}

// askIsBusy refuses a second question while one is on screen. There is one
// shared dialog, and a second askDialog() used to reconfigure it in place:
// overwriting the title, clearing any mustMatch gate, and leaving both `close`
// listeners attached, so a single click settled two promises. showModal() on an
// already-open dialog is a no-op rather than a throw, so nothing surfaced it.
//
// Reachable with the keyboard, not just in theory: a confirm focuses its OK
// button, which is not an <input>, so the palette hotkey still fires. Running
// "Kill session" from the palette re-labelled the live "Open this link?" dialog,
// and the next click both killed the session and opened the link.
function askIsBusy(dialogIsOpen) {
  return dialogIsOpen === true;
}

// askDialog is the single code path both public helpers use. Every close route
// (OK, Cancel, ×, Esc, backdrop) resolves the promise exactly once: a caller
// awaiting a dialog that never settles is stuck forever with no error to show.
function askDialog(opts) {
  const d = document.getElementById("ask-modal");
  // Refusing reads as a cancel, which is the safe answer: a question the user
  // never saw must never come back "confirmed".
  if (askIsBusy(d.open)) return Promise.resolve({ outcome: "cancel", value: "" });
  const input = document.getElementById("ask-input");
  const ok = document.getElementById("ask-ok");
  document.getElementById("ask-title").textContent = opts.title || "";
  document.getElementById("ask-body").textContent = opts.body || "";
  ok.textContent = opts.confirmLabel || "OK";
  d.classList.toggle("danger", !!opts.danger);

  const label = document.getElementById("ask-label");
  label.hidden = input.hidden = !opts.input;
  label.textContent = opts.label || "";
  input.value = opts.input ? (opts.value || "") : "";
  input.placeholder = opts.placeholder || "";

  // The gate on protected folders: OK stays disabled until the typed name is
  // exactly right. Native prompt() could not do this, so the code it replaces
  // could only compare after the dialog was already gone.
  const gate = () => {
    ok.disabled = opts.mustMatch ? !matchesRequired(input.value, opts.mustMatch) : false;
  };
  input.oninput = gate;
  gate();

  // The body is a plain <div>, not <form method="dialog">, so there is no
  // implicit submission. Without this, Enter after typing a folder name does
  // nothing: a regression against the native prompt() being replaced.
  input.onkeydown = (e) => {
    if (e.key === "Enter" && !ok.disabled) { e.preventDefault(); d.close("confirm"); }
  };

  return new Promise((resolve) => {
    let done = false;
    const settle = () => {
      if (done) return;               // `close` also fires after a button closed it
      done = true;
      d.removeEventListener("close", settle);
      input.oninput = null;
      input.onkeydown = null;
      resolve({ outcome: askOutcome(d.returnValue), value: input.value });
    };
    d.addEventListener("close", settle);
    ok.onclick = () => d.close("confirm");
    document.getElementById("ask-cancel").onclick = () => d.close("cancel");
    document.getElementById("ask-x").onclick = () => d.close("cancel");
    d.returnValue = "";               // clear the previous answer before showing
    d.showModal();
    // Focus explicitly: showModal() otherwise lands on the first focusable
    // descendant, which is the ×, making Enter and Space a cancel.
    if (opts.input) input.focus(); else ok.focus();
  });
}

// askConfirm resolves true only on an explicit confirmation.
async function askConfirm(opts) {
  const r = await askDialog(Object.assign({}, opts, { input: false }));
  return r.outcome === "confirm";
}

// askPrompt resolves the trimmed text, or null when dismissed. The two are
// distinct: "" means the user submitted an empty box.
async function askPrompt(opts) {
  const r = await askDialog(Object.assign({}, opts, { input: true }));
  return promptValue(r.outcome, r.value);
}

// shortcutsBlocked answers "should a document-level shortcut stand down".
//
// Two reasons, and the second is not obvious: showModal() makes the rest of the
// page inert to POINTER input, but a keydown from a focused descendant of the
// dialog still travels to document, and every global handler here listens in the
// capture phase. So with Settings open and focus on a button or a select, a jump
// or a zoom combination acted on the app underneath the backdrop.
//
// The activeElement half is the pre-existing rule, kept identical: never steal a
// combination from a real text field, but xterm's own focus target IS a textarea
// and must not count as one.
function shortcutsBlocked(activeElement, openDialogCount) {
  if (openDialogCount > 0) return true;
  const ae = activeElement;
  if (!ae) return false;
  if (ae.tagName === "INPUT") return true;
  if (ae.tagName === "TEXTAREA" && !ae.classList.contains("xterm-helper-textarea")) return true;
  return false;
}

// setDialogError writes into a dialog's inline error line. Errors raised while a
// dialog is open cannot use the toast, which paints beneath the backdrop.
function setDialogError(dialogId, msg) {
  const el = document.querySelector("#" + dialogId + " .dlg-err");
  if (el) el.textContent = msg || "";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    askOutcome,
    matchesRequired,
    promptValue,
    shouldRefocusTerminal,
    askIsBusy,
    shortcutsBlocked,
  };
}
