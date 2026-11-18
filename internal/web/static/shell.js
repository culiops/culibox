"use strict";

// UI shell primitives: transient toast, <dialog> plumbing, and the sidebar
// splitter. Deliberately free of app state and API calls: everything here is
// generic chrome that app.js and palette.js drive. Loaded before app.js, so it
// only DEFINES functions; boot wiring happens from app.js.

// --- toast -------------------------------------------------------------------
// One call, showToast(msg, opts), and two homes, picked at each call by
// toastStacked(). Above 700px the toasts are a stack (F60 F61), rendered from
// toaststack.js's model further down. The phone keeps its one slot (#toast): a
// second toast replaces the first, which on a small screen beats a pile of
// them over the terminal. The slot code is today's, byte for byte, plus its role.
let toastTimer = null;
// Bumped by every showToast call. A delayed writer (pendingToast) records it
// when it starts and stays silent if it moved: someone else owns the slot now.
let toastSeq = 0;
function toastSeqNow() { return toastSeq; }
// slotHandle: the phone toast's handle. Negative, because stack ids count up from 1: the two never share a
// value, so a phone handle cannot reach a stack entry (#183).
function slotHandle() { return -toastSeq; }

// placeToast keeps the toast clear of the phone's bottom chrome (#111). On the
// phone layout the Terminal screen stacks the snippet row, the key bar and the
// nav at the bottom, a 3-line toast at the CSS default covered the whole key bar,
// and which of them is showing changes with the screen and with the soft
// keyboard (it hides the nav). So the offset is read from what is displayed
// right now (mobileToastBottom, mobileshell.js). Anywhere else the inline value
// is cleared and the stylesheet's own bottom applies. mobileshell.js loads after
// this file, hence the typeof guards.
const TOAST_CHROME_IDS = ["term-snips", "term-keybar", "mobile-nav"];
const TOAST_CHROME_GAP = 8;
function placeToast(box) {
  box.style.bottom = "";
  if (typeof mobileActive !== "function" || typeof mobileToastBottom !== "function") return;
  if (!mobileActive(window.innerWidth)) return;
  const tops = [];
  for (const id of TOAST_CHROME_IDS) {
    const el = document.getElementById(id);
    // display: none has no client rects; a zero-height box is not in the way either.
    if (!el || el.getClientRects().length === 0) continue;
    const r = el.getBoundingClientRect();
    if (r.height > 0) tops.push(r.top);
  }
  const px = mobileToastBottom(window.innerHeight, tops, TOAST_CHROME_GAP);
  if (px !== null) box.style.bottom = px + "px";
}

// showToast(msg, opts) -> handle. opts:
//   error, warn   the tint (error wins). An error is role=alert, the rest role=status (F62).
//   undo          a callback: reveals Undo, which dismisses the toast, then runs it
//   ms            the auto-hide delay (6000)
//   sticky        no auto-hide: a pending toast, which its owner resolves or hides
//   progress      a number 0..1 reveals the progress rail
//   human         desktop only: the sentence shown instead of msg (F64)
//   detail        desktop only: an error's raw text, behind "Copy details" (F64)
//   replace       desktop only: a listed toast's handle to update in place
//   desktopOnly   the phone shows nothing (returns null)
// The handle is the stack entry's id above 700px (1, 2, 3, ...) and, on the phone, the slot's
// sequence number made negative (slotHandle): a phone handle used after the window widens (an
// upload's `replace`) cannot reach an unrelated stack entry. dismissToast and resolveToast take it.
function showToast(msg, opts) {
  opts = opts || {};
  if (toastStacked()) return stackToast(msg, opts);
  if (opts.desktopOnly) return null;
  toastSeq++;
  const box = document.getElementById("toast");
  const undoBtn = document.getElementById("toast-undo");
  if (!box) return slotHandle();
  // The slot is one element for every kind, so the role follows the message.
  // index.html makes it polite-live from load (a region that turns live with its
  // first content is often not announced); that explicit aria-live would override
  // role=alert's implicit assertive, so it follows the message too.
  // (Test fakes of the slot carry no setAttribute: pending.test.js, mobileshell.test.js.)
  if (typeof box.setAttribute === "function") {
    box.setAttribute("role", opts.error ? "alert" : "status");
    box.setAttribute("aria-live", opts.error ? "assertive" : "polite");
  }
  document.getElementById("toast-msg").textContent = msg;
  box.classList.toggle("err", !!opts.error);
  box.classList.toggle("warn", !!opts.warn && !opts.error);
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
  placeToast(box);
  box.classList.add("show");
  clearTimeout(toastTimer);
  if (!opts.sticky) toastTimer = setTimeout(hideToast, opts.ms || 6000);
  return slotHandle();
}

function hideToast() {
  clearTimeout(toastTimer);
  const box = document.getElementById("toast");
  if (box) box.classList.remove("show");
}

// dismissToast(handle): takes one toast away. A stack entry by its id (on any
// width: a pending entry shown before the window narrowed must not linger in the
// hidden stack); the phone slot only while it still shows that toast.
function dismissToast(handle) {
  if (toastStack && toastStack.remove(handle)) toastChanged();
  if (!toastStacked() && handle === slotHandle()) hideToast();
}

// resolveToast(handle, msg, opts) -> handle: a toast (a pending one) becomes its
// result. On the desktop in place, keeping its spot in the stack; when the entry
// is gone (evicted) the result is pushed as a new toast. On the phone the result
// takes the slot (and a stack entry from before the window narrowed is dropped).
function resolveToast(handle, msg, opts) {
  opts = opts || {};
  if (toastStacked()) return showToast(msg, Object.assign({}, opts, { replace: handle }));
  if (toastStack && toastStack.remove(handle)) toastChanged();
  return showToast(msg, opts);
}

// toastStacked: whether toasts go to the desktop stack, read at each call so a
// window dragged across 700px sends the next toast to the right home. A page
// without the #toasts host keeps the slot at any width.
function toastStacked() {
  if (!document.getElementById("toasts")) return false;
  return !(typeof mobileActive === "function" && mobileActive(window.innerWidth));
}

// --- toast stack (desktop: F60 F61 F62 F64) -----------------------------------
// toaststack.js owns the rules (cap 3, what is evicted, dedupe, pause); this
// renders its list() into #toasts and keeps ONE timer armed at nextDeadline(),
// re-armed after every change. Hover, or keyboard focus, inside the stack
// pauses every timer. Nodes are kept per entry id and patched, so a progress
// tick or the "paused" label never rebuilds a toast under the pointer.
let toastStack = null;
let toastStackTimer = null;
const toastUndos = new Map(); // entry id -> the caller's Undo callback
const toastNodes = new Map(); // entry id -> { node, shape }
let toastHover = false;
let toastFocus = false;
let toastBusy = false; // toastChanged is running
let toastAgain = false; // ...and something changed meanwhile

// toastView(entry): what one stack entry looks like. Pure. An error is the only
// role=alert; a pending entry has the spinner; the one action is Undo, else "Copy
// details" on an error that has raw text. Every entry has its ×, a pending one too
// (#183, founder 2026-11-13, option A): its request can hang, and with no way to
// remove it the entry stayed until a reload. Removing it does not lose the result: the
// owner's update then fails and the result is pushed as a new toast.
function toastView(entry) {
  const kind = entry.kind;
  const cls = ["toast"];
  if (kind === "error") cls.push("is-error");
  else if (kind === "warn") cls.push("is-warn");
  return {
    role: kind === "error" ? "alert" : "status",
    cls: cls,
    dot: kind === "error" ? "crashed" : kind === "warn" ? "needs" : null,
    spin: kind === "pending",
    action: entry.undo ? "undo" : kind === "error" && entry.detail ? "copy" : null,
  };
}

// showToast's opts as a model spec. `sticky` is the pending kind: it stays until
// its owner resolves it, which is exactly what a sticky toast was for.
function toastSpec(msg, opts) {
  return {
    text: opts.human || msg,
    kind: opts.error ? "error" : opts.sticky ? "pending" : opts.warn ? "warn" : "info",
    ms: opts.ms,
    undo: !!opts.undo,
    detail: opts.detail,
    progress: opts.progress,
  };
}

function stackToast(msg, opts) {
  if (!toastStack) toastStack = createToastStack({ max: 3, ms: 6000 });
  const now = Date.now();
  const spec = toastSpec(msg, opts);
  const id = opts.replace != null && toastStack.update(opts.replace, spec, now) ? opts.replace : toastStack.push(spec, now);
  if (opts.undo) toastUndos.set(id, opts.undo);
  else toastUndos.delete(id);
  toastChanged();
  return id;
}

// toastChanged: after anything that changes the stack or the pause. Removing the
// toast under the pointer (its ×, its Undo) fires no mouseleave, so an empty list
// ends the hover, and focus counts only while it is still inside the stack.
//
// Removing a toast that holds focus makes the browser fire focusout from inside
// node.remove(), before the node is detached; the handler lands back here. A
// nested render would detach that same node and the outer remove() would throw
// (NotFoundError), so a call made while one runs only asks for another pass.
function toastChanged() {
  if (!toastStack) return;
  if (toastBusy) { toastAgain = true; return; }
  toastBusy = true;
  try {
    do { toastAgain = false; toastSync(); } while (toastAgain);
  } finally {
    toastBusy = false;
  }
}

function toastSync() {
  const host = toastHost();
  if (toastStack.list().length === 0) toastHover = false;
  if (toastFocus && !(host && host.contains(document.activeElement))) toastFocus = false;
  const now = Date.now();
  if (toastHover || toastFocus) toastStack.pause(now);
  else toastStack.resume(now);
  renderToasts();
  toastPlace(); // the slot a strip must fall in is the stack's own height, which just changed (R28)
  // The render may have removed the focused toast (its Undo or ×, by keyboard).
  // Chrome fires focusout from inside remove(); Firefox and Safari fire nothing
  // and just move focus to the body. So look again: focus that left with its
  // toast ends the pause, and one more pass resumes and re-renders. That pass
  // starts with toastFocus false, so it cannot come back here.
  if (toastFocus && !(host && host.contains(document.activeElement))) {
    toastFocus = false;
    toastAgain = true;
    return;
  }
  clearTimeout(toastStackTimer);
  toastStackTimer = null;
  const at = toastStack.nextDeadline();
  if (at === null) return;
  toastStackTimer = setTimeout(() => {
    toastStackTimer = null;
    toastStack.expire(Date.now());
    toastChanged();
  }, Math.max(0, at - now));
}

// toastPlace: the stack's top edge (Plan 7 call 10). Desktop only; the phone slot is not this element. The stack is
// 420px at most and 16px (--sp-4) from the right, so that x-range is checked against every ask strip and write
// banner whose top falls in the stack's own slot (R28); toaststack.js's toastStackTop does the arithmetic. Called where a strip or banner appears, leaves or
// moves (positionDrawer, which the grid's ResizeObserver also drives), never per poll. No strip: the CSS's 48.
function toastPlace() {
  const host = document.getElementById("toasts");
  if (!host) return;
  if (!toastStacked()) { host.style.top = ""; return; }
  if (typeof document.querySelectorAll !== "function") return; // a page host without layout (the jstest fakes)
  const right = window.innerWidth - 16;
  const rects = Array.from(document.querySelectorAll(".needstrip, .elev"), (n) => n.getBoundingClientRect());
  const top = toastStackTop({ left: right - 420, right }, rects, host.offsetHeight);
  host.style.top = top === 48 ? "" : top + "px";
}

// toastHost: #toasts, with its pause listeners wired on first use. mouseenter and
// mouseleave fire on the container for its toasts (the container itself takes no
// pointer). The document's mouseover catches the pointer leaving after the toast
// under it was removed, which fires no mouseleave. A click focuses a button
// without :focus-visible; only keyboard focus pauses, or clicking "Copy details"
// would hold every timer until focus moved elsewhere.
function toastHost() {
  const host = document.getElementById("toasts");
  if (!host || host.__toastWired) return host;
  host.__toastWired = true;
  const hover = (on) => { if (toastHover !== on) { toastHover = on; toastChanged(); } };
  host.addEventListener("mouseenter", () => hover(true));
  host.addEventListener("mouseleave", () => hover(false));
  document.addEventListener("mouseover", (e) => { if (toastHover && !host.contains(e.target)) hover(false); });
  host.addEventListener("focusin", (e) => {
    toastFocus = !!(e.target && e.target.matches && e.target.matches(":focus-visible"));
    toastChanged();
  });
  host.addEventListener("focusout", (e) => {
    if (host.contains(e.relatedTarget)) return;
    toastFocus = false;
    toastChanged();
  });
  return host;
}

// renderToasts: list() into #toasts, newest first, diffed by id. A node is rebuilt
// only when its shape changes (pending -> result, a meter coming or going); the
// text, the "paused" label and the meter width are patched in place.
function renderToasts() {
  const host = toastHost();
  if (!host || !toastStack) return;
  const list = toastStack.list();
  const paused = toastStack.paused;
  const live = new Set(list.map((e) => e.id));
  for (const [id, rec] of toastNodes) {
    if (live.has(id)) continue;
    toastNodes.delete(id);
    rec.node.remove();
  }
  for (const id of toastUndos.keys()) if (!live.has(id)) toastUndos.delete(id);
  let prev = null;
  for (const e of list) {
    const v = toastView(e);
    const shape = [v.cls.join(" "), v.role, v.dot, v.spin, v.action, e.kind, e.progress !== null].join("|");
    let rec = toastNodes.get(e.id);
    if (!rec || rec.shape !== shape) {
      const node = toastBuild(e, v);
      if (rec) rec.node.replaceWith(node);
      rec = { node: node, shape: shape };
      toastNodes.set(e.id, rec);
    }
    toastPatch(rec.node, e, paused);
    const at = prev ? prev.nextSibling : host.firstChild;
    if (at !== rec.node) host.insertBefore(rec.node, at);
    prev = rec.node;
  }
}

function toastEl(tag, cls, text) {
  const el = document.createElement(tag);
  el.className = cls;
  if (text != null) el.textContent = text;
  return el;
}

// toastBuild: one entry's markup, in the kit's classes (M7.5, M8.5). Text only
// ever goes in through textContent.
function toastBuild(e, v) {
  const node = toastEl("div", v.cls.join(" "));
  node.setAttribute("role", v.role);
  node.dataset.id = String(e.id);
  const parts = {};
  if (v.dot) node.appendChild(toastEl("span", "dot dot-" + v.dot)).setAttribute("aria-hidden", "true");
  if (v.spin) node.appendChild(toastEl("span", "spin")).setAttribute("aria-hidden", "true");
  parts.text = node.appendChild(toastEl("span", "toast-text"));
  if (v.action) {
    const b = toastEl("button", "btn btn-ghost btn-sm toast-act", v.action === "undo" ? "Undo" : "Copy details");
    b.type = "button";
    b.dataset.act = v.action;
    b.onclick = v.action === "undo" ? () => toastUndoClick(e.id) : () => toastCopyClick(e.id, b);
    node.appendChild(b);
  }
  const x = toastEl("button", "btn btn-ghost btn-icon btn-sm toast-x", "×");
  x.type = "button";
  x.title = "Dismiss";
  x.setAttribute("aria-label", "Dismiss");
  x.onclick = () => dismissToast(e.id);
  node.appendChild(x);
  // The meter takes its own full-width row under the text (desktop.css).
  if (e.progress !== null) parts.bar = node.appendChild(toastEl("div", "meter")).appendChild(document.createElement("i"));
  node._toast = parts;
  return node;
}

// toastPatch: what changes without a rebuild. "paused" shows on the entries that
// have a timer to pause (info, warn) while the stack is paused (M8.5).
function toastPatch(node, e, paused) {
  const parts = node._toast;
  if (parts.text.textContent !== e.text) parts.text.textContent = e.text;
  const want = paused && (e.kind === "info" || e.kind === "warn");
  if (want && !parts.paused) parts.paused = node.insertBefore(toastEl("span", "toast-paused", "paused"), parts.text.nextSibling);
  else if (!want && parts.paused) { parts.paused.remove(); parts.paused = null; }
  if (parts.bar) parts.bar.style.width = e.progress * 100 + "%";
}

// Undo: the toast goes first, then the callback runs (the slot's order).
function toastUndoClick(id) {
  const fn = toastUndos.get(id);
  dismissToast(id);
  if (fn) fn();
}

// Copy details: the entry's raw text to the clipboard (app.js copyText), and the
// label says how that went for 1.5 s.
function toastCopyClick(id, btn) {
  const e = toastStack && toastStack.list().find((x) => x.id === id);
  if (!e || typeof copyText !== "function") return;
  copyText(e.detail, (ok) => {
    btn.textContent = ok ? "Copied" : "Copy failed";
    clearTimeout(btn._toastReset);
    btn._toastReset = setTimeout(() => { btn.textContent = "Copy details"; }, 1500);
  });
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

// openDialog: on desktop, Settings, Analytics and Audit open as pages in the
// pane area (pagehost.js openPage); everything else, and every dialog on a
// phone, opens modally as before.
function openDialog(id) {
  const d = document.getElementById(id);
  if (typeof openPage === "function" && openPage(id)) return d;
  if (d && !d.open) d.showModal();
  return d;
}

function closeDialog(id) {
  const d = document.getElementById(id);
  if (d && d.open) d.close();
}

// closeDialogFromUI: a ✕ or Done button. The open page goes through closePage so its leave hook can ask first
// (unsaved profile changes); every other dialog, and every dialog on a phone, closes as closeDialog does.
function closeDialogFromUI(id) {
  if (typeof currentPage === "function" && currentPage() === id && typeof closePage === "function") closePage();
  else closeDialog(id);
}

// openSheet/closeSheet: the mobile bottom-sheet primitive. One #sheet-modal,
// body rebuilt per open (like #ask-modal). `build(bodyEl)` fills the sheet;
// `opts.height` sets the --sheet-h CSS var (default from app.css); `opts.onClose`
// runs once, from the generic dialog `close` wiring in initDialogs, however the
// sheet closes (Esc, backdrop, or a caller calling closeSheet()).
function openSheet(build, opts) {
  opts = opts || {};
  const dlg = document.getElementById("sheet-modal");
  if (!dlg || askIsBusy(document.querySelector("dialog[open]") != null)) return false;
  const body = document.getElementById("sheet-body");
  body.replaceChildren();
  if (opts.height) dlg.style.setProperty("--sheet-h", opts.height);
  else dlg.style.removeProperty("--sheet-h");
  build(body);
  dlg.__onClose = opts.onClose || null;
  dlg.showModal();
  return true;
}

function closeSheet() {
  const dlg = document.getElementById("sheet-modal");
  if (dlg && dlg.open) dlg.close();
}

// openSettings shows the settings dialog with one tab selected ("manage" =
// profiles, "settings" = font sizes: the ids are the pre-existing panel ids,
// kept so every handler that already targets them keeps working).
function openSettings(tab, group) {
  // The profile page asks before another tab replaces it, when it holds unsaved changes (settingsLeave). It is
  // opened again once the question is answered with Discard.
  const leave = settingsLeave(tab);
  if (leave !== true) {
    leave.then((ok) => { if (ok) openSettings(tab, group); });
    return;
  }
  // "creds" can be opened on one group ("aws", "github", "tokens"): it scrolls
  // there once the Credentials view has painted (credFocusGroup).
  if (tab === "creds" && group && typeof credFocusGroup === "function") credFocusGroup(group);
  selectSettingsTab(tab || "manage");
  openDialog("settings-modal");
  // The page is open now (selectSettingsTab ran before it was), so the profile overview can draw, and so can the
  // page head of the others (settingshead.js draws nothing until the dialog is the page).
  if ((tab || "manage") === "manage" && typeof pfvShow === "function") pfvShow();
  if (typeof paintSettingsHead === "function") paintSettingsHead(tab || "manage");
  // shell.js loads before app.js, so initNotifyToggle may not exist yet on the
  // very first paint — guard it. Reopening Settings must reflect a permission
  // revoked in the browser meanwhile, so this re-reads on every open.
  if (typeof initNotifyToggle === "function") initNotifyToggle();
}

// settingsLeave: may Settings go to tab `tab`? true, itself and not a promise, when it may at once. The profile page
// (profilepage.js pfvTabLeave) answers a promise when it holds unsaved changes and the user is asked; on a phone, or
// with nothing changed, it is true, so the modal never sees a question.
function settingsLeave(tab) {
  return typeof pfvTabLeave === "function" ? pfvTabLeave(tab || "manage") : true;
}

function selectSettingsTab(name) {
  for (const b of document.querySelectorAll("#settings-tabs .t")) {
    const on = b.dataset.tab === name;
    b.classList.toggle("on", on);
    b.setAttribute("aria-selected", on ? "true" : "false");
    // On a phone the strip scrolls on its own; keep the chosen tab in view.
    if (on && typeof b.scrollIntoView === "function") b.scrollIntoView({ block: "nearest", inline: "nearest" });
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
  // Leaving it takes a one-time token reveal off the page (tokens.js); coming back loads the tab afresh.
  if (name !== "tokens" && typeof tokLeave === "function") tokLeave();
  if (name === "accounts" && typeof loadAccounts === "function") loadAccounts();
  if (name === "creds" && typeof loadCredentials === "function") loadCredentials();
  if (name === "vmfolders" && typeof loadVMFolders === "function") loadVMFolders();
  if (name === "about" && typeof loadAboutInfo === "function") loadAboutInfo();
  // The profile form's pickers list what Credentials holds now, not what it held at page load.
  if (name === "manage" && typeof refreshCredLists === "function") refreshCredLists();
  // And the profile list what the server holds now (a profile made through the
  // API or in another tab). loadProfiles repaints the list, never the editor,
  // so an open form keeps its unsaved edits.
  if (name === "manage" && typeof loadProfiles === "function") loadProfiles(false);
  // The desktop page's profile overview (profilepage.js); a no-op on a phone's modal.
  if (name === "manage" && typeof pfvShow === "function") pfvShow();
  // The other pages' head (title, one line, its one action, Esc ✕); also a no-op on a phone's modal.
  if (typeof paintSettingsHead === "function") paintSettingsHead(name);
}

function initDialogs() {
  for (const btn of document.querySelectorAll("[data-close]")) {
    btn.onclick = () => closeDialogFromUI(btn.dataset.close);
  }
  // Tabs only: the nav also holds Sign out… (a desktop-only item; tokens.js wires its click), and it is not a tab.
  for (const b of document.querySelectorAll("#settings-tabs button[data-tab]")) {
    b.onclick = () => {
      const leave = settingsLeave(b.dataset.tab);
      if (leave === true) selectSettingsTab(b.dataset.tab);
      else leave.then((ok) => { if (ok) selectSettingsTab(b.dataset.tab); });
    };
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
    // A page (pagehost.js) has no backdrop: its empty area is part of the page.
    d.addEventListener("click", (e) => { if (pressedBackdrop && e.target === d && !d.classList.contains("as-page")) d.close(); });
    // Fires for Esc, ×, backdrop and programmatic close alike. A page goes back
    // to its home first: focusTerminal cannot focus a pane while #grid is hidden.
    d.addEventListener("close", () => { if (typeof pageClosed === "function") pageClosed(d); });
    d.addEventListener("close", focusTerminal);
    // Settings closing is leaving the Access-tokens tab: a one-time token reveal goes with it.
    d.addEventListener("close", () => { if (d.id === "settings-modal" && typeof tokLeave === "function") tokLeave(); });
    // Sheets attach a one-shot onClose callback (openSheet's opts.onClose);
    // every other dialog leaves __onClose unset, so this is a no-op for them.
    d.addEventListener("close", () => {
      if (d.__onClose) {
        const cb = d.__onClose;
        d.__onClose = null;
        cb();
      }
    });
  }
  const settingsOpen = document.getElementById("settings-open");
  if (settingsOpen) settingsOpen.onclick = () => openSettings("manage");
  const sideSettings = document.getElementById("side-settings");
  if (sideSettings) sideSettings.onclick = () => openSettings("manage");
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
const SIDEBAR_MIN = 200, SIDEBAR_MAX = 560; // also inlined in index.html's pre-paint script and #split-v's aria-valuemin/max
const SIDEBAR_STEP = 16; // one arrow-key press on #split-v

function setSidebarWidth(px) {
  px = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, px));
  document.documentElement.style.setProperty("--sidebar-w", px + "px");
  // The splitter's separator role reports the width it is at (its min/max are in index.html).
  const sep = document.getElementById("split-v");
  if (sep) sep.setAttribute("aria-valuenow", String(Math.round(px)));
  return px;
}

// syncSplitterAria: aria-valuenow follows the width the sidebar is drawn at. A page loaded with the
// sidebar hidden paints no width (0), so the static 290 in index.html would be announced until
// the first step: this re-reads it when the sidebar is shown and when the handle takes focus.
function syncSplitterAria() {
  const sidebar = document.getElementById("sidebar");
  const sep = document.getElementById("split-v");
  if (!sidebar || !sep) return;
  const drawn = Math.round(sidebar.getBoundingClientRect().width);
  if (drawn > 0) sep.setAttribute("aria-valuenow", String(drawn));
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
  // hatch from a drag that went wrong. 290 mirrors desktop.css's --sidebar-w
  // default; the two must move together.
  handle.addEventListener("dblclick", () => { persistSidebarWidth(setSidebarWidth(290)); fitAll(); });

  // The keyboard way: ArrowRight widens, ArrowLeft narrows, 16px a press, kept like a drag
  // (and the terminals refit, as after one). Not with a modifier: Ctrl+Arrow and the like
  // belong to the browser and the page. The first press steps from the width it is drawn at.
  handle.addEventListener("keydown", (e) => {
    const dir = widthKeyDir(e.key);
    if (!dir || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
    e.preventDefault();
    const cur = sidebar.getBoundingClientRect().width;
    const next = stepWidth(cur, dir, SIDEBAR_MIN, SIDEBAR_MAX, SIDEBAR_STEP);
    // At a clamp (or a held key at one) the step lands where the sidebar already is: no
    // localStorage write and no refit per repeat.
    if (next === cur) return;
    persistSidebarWidth(setSidebarWidth(next));
    fitAll();
  });
  // aria-valuenow starts at the width the page was painted with (the stored one, applied
  // pre-paint), and is re-read when the handle takes focus (it may have been hidden at load).
  handle.addEventListener("focus", syncSplitterAria);
  syncSplitterAria();
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
  if (returnValue === "confirm") return "confirm";
  if (returnValue === "alt") return "alt"; // only #ask-alt sets it
  return "cancel";
}

// askAltVisible: the optional third button exists only when a dialog names it,
// so every dialog written before it keeps exactly its two buttons.
function askAltVisible(altLabel) {
  return typeof altLabel === "string" && altLabel !== "";
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

// askNoteNodes: opts.notes as paragraphs. A note is a list of parts, a part a string or
// { mono: "…" } (a path or a command, set in the mono face). Built with createElement and
// text nodes, never innerHTML: a path in a note is text.
function askNoteNodes(notes) {
  return (Array.isArray(notes) ? notes : []).map((parts) => {
    const p = document.createElement("p");
    p.className = "ask-para";
    for (const part of Array.isArray(parts) ? parts : [parts]) {
      if (part && typeof part === "object") {
        const m = document.createElement("span");
        m.className = "mono";
        m.textContent = part.mono;
        p.appendChild(m);
      } else p.appendChild(document.createTextNode(String(part)));
    }
    return p;
  });
}

// The class the last ask put on the dialog (opts.cls), taken off before the next one's goes on.
let askAppliedCls = "";

// askFitHint: an ellipsized "E…" is worse than no hint. The desktop footer's "Esc to cancel" is the elastic part of the row
// (desktop.css gives way to the buttons down to nothing, and the alt button only ellipsizes once it has nothing left), so once
// the dialog is open and laid out a hint whose text is wider than its box is hidden (.is-clipped: visibility, the row does not
// move). scrollWidth is rounded, hence the pixel of slack. Reset on every ask. The phone never shows the hint (display:none in
// app.css) and is not measured.
function askFitHint(hint) {
  if (!hint) return;
  hint.classList.toggle("is-clipped", false);
  if (typeof mobileActive === "function" && mobileActive(window.innerWidth)) return;
  if (hint.scrollWidth > hint.clientWidth + 1) hint.classList.toggle("is-clipped", true);
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
  // Optional nodes under the body (the host switch lists what it removes), then opts.notes as muted paragraphs.
  // Reset on every ask, like the cancel label. The card widens for opts.extra (a list needs the room) unless
  // opts.wide says otherwise; notes are prose and keep the card's width.
  const extra = document.getElementById("ask-extra");
  if (extra) {
    const nodes = Array.isArray(opts.extra) ? opts.extra : opts.extra ? [opts.extra] : [];
    extra.replaceChildren(...nodes, ...askNoteNodes(opts.notes));
    extra.hidden = !extra.children.length;
    d.classList.toggle("wide", opts.wide === undefined ? nodes.length > 0 : !!opts.wide);
  }
  // One class for this ask only (the End session card is .end), and the footer's "Esc to cancel": an info
  // dialog has no Cancel to name. footless drops the footer for this ask; Esc and the X still close it.
  if (askAppliedCls) d.classList.toggle(askAppliedCls, false);
  askAppliedCls = opts.cls || "";
  if (askAppliedCls) d.classList.toggle(askAppliedCls, true);
  d.classList.toggle("footless", !!opts.footless);
  const hint = document.getElementById("ask-hint");
  if (hint) hint.textContent = opts.hint !== undefined ? opts.hint : opts.info ? "" : "Esc to cancel";
  ok.textContent = opts.confirmLabel || "OK";
  // Cancel is reset on every ask, so a label one question gave (the profile page's
  // "Keep editing") never leaks into the next.
  document.getElementById("ask-cancel").textContent = opts.cancelLabel || "Cancel";
  const alt = document.getElementById("ask-alt");
  alt.hidden = !askAltVisible(opts.altLabel);
  alt.textContent = opts.altLabel || "";
  // The desktop End session footer is one row in a 480px card, so a long name's button ends in an ellipsis: the dialog that
  // can do that passes the whole label as altTitle. Reset on every ask, like the label.
  // The whole label is also the button's name: a title only describes it, and the shortened text is what a screen reader
  // would otherwise read. Removed on an ask with none, so the visible text is the name again.
  alt.title = opts.altTitle || "";
  if (opts.altTitle) alt.setAttribute("aria-label", opts.altTitle);
  else alt.removeAttribute("aria-label");
  d.classList.toggle("danger", !!opts.danger);
  d.classList.toggle("info", !!opts.info); // read-only: desktop.css hides Cancel

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
    alt.onclick = () => d.close("alt");
    document.getElementById("ask-cancel").onclick = () => d.close("cancel");
    document.getElementById("ask-x").onclick = () => d.close("cancel");
    d.returnValue = "";               // clear the previous answer before showing
    d.showModal();
    askFitHint(hint);                 // laid out now: does "Esc to cancel" fit beside the buttons?
    // Focus explicitly: showModal() otherwise lands on the first focusable
    // descendant, which is the ×, making Enter and Space a cancel. focusCancel
    // puts it on Cancel instead, for a question whose "yes" must never be one
    // keypress away (the host switch, discarding unsaved profile changes).
    if (opts.input) input.focus();
    else if (opts.focusCancel) document.getElementById("ask-cancel").focus();
    else if (opts.footless) document.getElementById("ask-x").focus(); // no footer to land in
    else ok.focus();
  });
}

// askChoice resolves "confirm", "alt" (the optional third button) or "cancel".
async function askChoice(opts) {
  const r = await askDialog(Object.assign({}, opts, { input: false }));
  return r.outcome;
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

// shortcutContext builds the one ctx object shortcutFor (shortcuts.js) reads, so
// no handler re-derives "where is focus" on its own. The three focus flags are
// mutually exclusive: xterm's helper textarea is the terminal, a textarea inside
// CodeMirror or Monaco (the dock's editor, ruling R2) is the editor, and only then
// does a real text control count as an input. SELECT is an input too: arrow keys in a select belong to the user.
// Every field is a strict boolean; a missing dialog count is 0.
function shortcutContext(activeElement, openDialogCount, pageOpen, mobile) {
  const ae = activeElement;
  const inTerminal = !!(ae && ae.classList && ae.classList.contains("xterm-helper-textarea"));
  const inEditor = !inTerminal && !!(ae && ae.closest && (ae.closest(".CodeMirror") || ae.closest(".monaco-editor")));
  const inInput =
    !inTerminal &&
    !inEditor &&
    !!ae &&
    (ae.tagName === "INPUT" || ae.tagName === "TEXTAREA" || ae.tagName === "SELECT" || !!ae.isContentEditable);
  return {
    dialogOpen: (openDialogCount || 0) > 0,
    pageOpen: !!pageOpen,
    inTerminal,
    inEditor,
    inInput,
    mobile: !!mobile,
  };
}

// setDialogError writes into a dialog's inline error line. Errors raised while a
// dialog is open cannot use the toast, which paints beneath the backdrop.
function setDialogError(dialogId, msg) {
  const el = document.querySelector("#" + dialogId + " .dlg-err");
  if (!el) return;
  el.textContent = msg || "";
  // The line sits under a scrolling form (the profile editor's is below the
  // fold), so a failure written there is invisible. "nearest" moves nothing
  // when it is already on screen. Clearing never scrolls.
  if (msg && typeof el.scrollIntoView === "function") el.scrollIntoView({ block: "nearest" });
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    showToast,
    hideToast,
    toastSeqNow,
    dismissToast,
    resolveToast,
    toastStacked,
    toastView,
    askOutcome,
    askAltVisible,
    matchesRequired,
    promptValue,
    shouldRefocusTerminal,
    askIsBusy,
    shortcutContext,
    setDialogError,
  };
}
