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

if (typeof module !== "undefined" && module.exports) {
  module.exports = { deadlineSignal };
}
