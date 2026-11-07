"use strict";

// The one "how long" formatter (F25). Callers used to carry their own: actAge
// stopped at hours ("277h 53m") and startedText did its own day maths from 24h.
//
//   under 60 s    "45s"
//   under 1 h     "12m"
//   under 48 h    "30h 0m"      the hour count stays readable well past one day
//   48 h or more  "11d 13h"
//
// Whole seconds in. A negative, NaN or non-number input is "0s": the skew is the
// caller's clock, never something to print. Classic script: pure, no DOM.

function fmtDuration(sec) {
  const s = typeof sec === "number" && Number.isFinite(sec) ? Math.max(0, Math.floor(sec)) : 0;
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 48 * 3600) return Math.floor(s / 3600) + "h " + Math.floor((s % 3600) / 60) + "m";
  return Math.floor(s / 86400) + "d " + Math.floor((s % 86400) / 3600) + "h";
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { fmtDuration };
}
