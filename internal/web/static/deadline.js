"use strict";

// deadlineSignal returns an AbortSignal that fires after ms. AbortSignal.timeout
// is not in every browser this may be opened from, so fall back to an
// AbortController + timer (left to expire on its own: aborting a request that
// already finished is a no-op). Shared by every request that needs a deadline
// so none of them can throw on a browser without it. `g` is the global object,
// injectable so the fallback is testable under node.
function deadlineSignal(ms, g = globalThis) {
  if (g.AbortSignal && g.AbortSignal.timeout) return g.AbortSignal.timeout(ms);
  const ac = new g.AbortController();
  g.setTimeout(() => ac.abort(), ms);
  return ac.signal;
}

// How long a session action (End, Restart, Interrupt, Bring work home, + Shell) may wait for its answer. An action that
// never answers left its row "ending…" and its pending toast up until a reload (#257, #183). The server finishes what it
// was asked whatever the browser does (Kill and Restart take no request context), so giving up only frees the page: a late
// success is what the next poll shows. End, interrupt and bring-home answer in seconds (docker stop); a restart or a new
// shell may first start the box, which the New session dialog itself says can take a minute after an idle spell.
const ACTION_TIMEOUT_MS = 60000;
const ACTION_SLOW_TIMEOUT_MS = 120000;

// actionTimeoutError: what a request that got no answer in time rejects with. `timedOut` lets a caller tell it from a refusal;
// it has no `status`, so toastFailText gives the standard "Could not <what>." and the sentence below is the raw text behind
// "Copy details".
function actionTimeoutError(ms) {
  const err = new Error("The server did not answer within " + Math.round(ms / 1000) + " s.");
  err.timedOut = true;
  return err;
}

// withDeadline(ms, run) -> the promise of run(signal), which gives up after ms: the AbortController's signal is aborted
// (a fetch that honours it stops) and the result rejects with actionTimeoutError, even when run ignores the signal and never
// settles, so the caller is always released. The timer ends with the request. `g` is the global object, injectable for tests.
function withDeadline(ms, run, g = globalThis) {
  const ac = new g.AbortController();
  let timer;
  const late = new Promise((_, reject) => {
    // Rejected before the abort: a fetch rejects with its own AbortError the moment it is aborted, and the caller must see
    // the timeout, not that.
    timer = g.setTimeout(() => { reject(actionTimeoutError(ms)); ac.abort(); }, ms);
  });
  let started;
  try { started = Promise.resolve(run(ac.signal)); } catch (e) { started = Promise.reject(e); }
  return Promise.race([started, late]).finally(() => g.clearTimeout(timer));
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deadlineSignal, ACTION_TIMEOUT_MS, ACTION_SLOW_TIMEOUT_MS, actionTimeoutError, withDeadline };
}
