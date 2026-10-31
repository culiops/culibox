"use strict";

// Shared "working on it" state for slow actions. A fast local read must show
// nothing (no flicker), so every indicator waits PENDING_DELAY_MS and is only
// painted if the work is still running. Pending is neutral by design: muted
// text and the accent pulse, never amber or red (those mean warning/error).
// Classic script, one shared global scope: showToast/hideToast come from
// shell.js and are only called at run time, so load order does not matter.

const PENDING_DELAY_MS = 300;

// el -> the in-flight promise, so a second call on the same control is ignored.
const pendingInFlight = new WeakMap();

// withPending(el, label, fn): disables `el` and marks it aria-busy while fn()
// runs; if fn is still pending after PENDING_DELAY_MS, it also adds `.busy` and
// swaps the text to `label`. Returns fn's result, rethrows its error. A call on
// an `el` that is already pending returns the in-flight promise and does not
// call fn again.
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
  const timer = setTimeout(() => {
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

// pendingToast(text) -> done: for waits with no container of their own. After
// the delay it shows a sticky toast, unless some other showToast call has
// claimed the slot since pendingToast started (a fast error must not be
// overwritten by "Working…" while cleanup is still running). done() hides it
// only if the toast still reads `text`, so an error toast that replaced it is
// left alone. toastSeqNow comes from shell.js and is read at run time only; the
// typeof guard covers test harnesses that stub showToast without it.
function pendingToast(text) {
  const seq = typeof toastSeqNow === "function" ? toastSeqNow() : null;
  let shown = false;
  const timer = setTimeout(() => {
    if (seq !== null && toastSeqNow() !== seq) return;
    shown = true;
    showToast(text, { sticky: true });
  }, PENDING_DELAY_MS);
  return () => {
    clearTimeout(timer);
    if (!shown) return;
    shown = false;
    const msg = document.getElementById("toast-msg");
    if (msg && msg.textContent === text) hideToast();
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { PENDING_DELAY_MS, withPending, pendingRegion, pendingToast };
}
