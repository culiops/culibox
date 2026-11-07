"use strict";

// Shared "working on it" state for slow actions. A fast local read must show
// nothing (no flicker), so every indicator waits PENDING_DELAY_MS and is only
// painted if the work is still running. Pending is neutral by design: muted
// text and the accent pulse, never amber or red (those mean warning/error).
// Classic script, one shared global scope: showToast, hideToast, toastStacked,
// resolveToast and dismissToast come from shell.js and are only called at run
// time, so load order does not matter.

const PENDING_DELAY_MS = 300;

// el -> the in-flight promise, so a second call on the same control is ignored.
const pendingInFlight = new WeakMap();

// withPending(el, label, fn): disables `el` and marks it aria-busy while fn()
// runs; if fn is still pending after PENDING_DELAY_MS, it also adds `.busy` and
// swaps the text to `label`. Returns fn's result, rethrows its error. A call on
// an `el` that is already pending returns the in-flight promise and does not
// call fn again.
//
// If `el` has been detached by the time the delay fires (its container was
// re-rendered), the label is shown as a pending toast instead of on the node, and
// taken away again in the finally as pendingToast's done() does with no result.
//
// Restores only what is still its own: the text if it still reads `label`,
// `disabled` if it is still true as we set it. If the owner rewrote either
// while we waited (e.g. "Saved", or re-enabled), the owner wins.
function withPending(el, label, fn) {
  const running = pendingInFlight.get(el);
  if (running) return running;

  const wasDisabled = el.disabled;
  el.disabled = true;
  el.setAttribute("aria-busy", "true");
  let original = null; // the text to put back; set only once we swap in `label`
  let toastShown = null; // set when `el` was detached and the label went to a toast
  const seq = pendingToastSeq();
  const timer = setTimeout(() => {
    // A click can re-render the control's container, leaving `el` detached:
    // relabelling it would be invisible, so the same feedback goes to the toast.
    // (typeof guard: plain test fakes carry no isConnected and keep the in-place path.)
    if (typeof el.isConnected === "boolean" && !el.isConnected) {
      toastShown = showPendingToast(label, seq);
      return;
    }
    original = el.textContent;
    el.classList.add("busy");
    el.textContent = label;
  }, PENDING_DELAY_MS);

  const run = async () => {
    try {
      return await fn();
    } finally {
      clearTimeout(timer);
      el.removeAttribute("aria-busy");
      el.classList.remove("busy");
      if (original !== null && el.textContent === label) el.textContent = original;
      if (el.disabled === true) el.disabled = wasDisabled;
      clearPendingToast(toastShown, label);
    }
  };
  const p = run();
  pendingInFlight.set(el, p);
  const clear = () => { if (pendingInFlight.get(el) === p) pendingInFlight.delete(el); };
  p.then(clear, clear);
  return p;
}

// pendingRegion(container, text) -> done: for content areas. After the delay it
// prepends a muted `.pending-line` to `container`; existing children stay, so
// stale content is never blanked. done() clears the timer and removes the line;
// calling it again is safe.
function pendingRegion(container, text) {
  let line = null;
  const timer = setTimeout(() => {
    line = document.createElement("div");
    line.className = "pending-line";
    line.textContent = text;
    container.prepend(line);
  }, PENDING_DELAY_MS);
  return () => {
    clearTimeout(timer);
    if (line) { line.remove(); line = null; }
  };
}

// pendingToast(text) -> done(result?): for waits with no container of their
// own. After the delay it shows a pending toast. done() clears the timer and
// takes the toast away; done(result), result = { msg, opts }, turns it into the
// result instead. Calling done again does nothing.
//
// Desktop (the stack): the pending entry becomes the result in place
// (resolveToast), or, when it never showed or was evicted, the result is a new
// toast. Phone (one slot): the pending toast shows only if no other showToast
// claimed the slot since pendingToast started (a fast error must not be
// overwritten by "Working…" while cleanup is still running), and done() hides it
// only while the slot still reads `text`, so an error that replaced it is left
// alone. Then the result is shown, unless it is result.opts.desktopOnly.
function pendingToast(text) {
  const seq = pendingToastSeq();
  let shown = null;
  let finished = false;
  const timer = setTimeout(() => { shown = showPendingToast(text, seq); }, PENDING_DELAY_MS);
  return (result) => {
    if (finished) return;
    finished = true;
    clearTimeout(timer);
    const was = shown;
    shown = null;
    if (result && was && was.handle != null) {
      resolveToast(was.handle, result.msg, result.opts);
      return;
    }
    clearPendingToast(was, text);
    if (!result) return;
    if (pendingStacked() || !(result.opts && result.opts.desktopOnly)) showToast(result.msg, result.opts);
  };
}

// Whether toasts go to the desktop stack right now. Without shell.js's
// toastStacked (test harnesses that stub showToast alone) it is the slot.
function pendingStacked() {
  return typeof toastStacked === "function" && toastStacked();
}

// The toast-slot counter at this moment, or null when shell.js's is not present.
// toastSeqNow is read at run time only; the typeof guard covers test harnesses
// that stub showToast without it.
function pendingToastSeq() {
  return typeof toastSeqNow === "function" ? toastSeqNow() : null;
}

// Paints the pending toast. Desktop: a stack entry, { handle }. Phone: the slot,
// { own: true }, unless another showToast has claimed it since `seq` was read
// (null: not shown).
function showPendingToast(text, seq) {
  if (pendingStacked()) return { handle: showToast(text, { sticky: true }) };
  if (seq !== null && toastSeqNow() !== seq) return null;
  showToast(text, { sticky: true });
  return { own: true };
}

// Takes away what showPendingToast painted: the stack entry by its handle, or the
// slot while it still reads `text` (an error that replaced it stays).
function clearPendingToast(shown, text) {
  if (!shown) return;
  if (shown.own) hidePendingToast(text);
  else if (shown.handle != null) dismissToast(shown.handle);
}

// Hides the toast only while it still reads `text`: an error that replaced it stays.
function hidePendingToast(text) {
  const msg = document.getElementById("toast-msg");
  if (msg && msg.textContent === text) hideToast();
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { PENDING_DELAY_MS, withPending, pendingRegion, pendingToast };
}
