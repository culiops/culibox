"use strict";

// Client-side formatting of a session's transcript usage (GET
// /api/sessions/{id}/usage). Raw tokens are always shown; a percentage only
// when the server supplied one (it omits it when the context max is unknown or
// exceeded — see usage.go). All helpers tolerate null/empty and never throw.

// fmtTokens: 0..999 as-is, thousands as "Nk", millions as "N.NM".
function fmtTokens(n) {
  if (!n || n < 0) return "0";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return Math.round(n / 1000) + "k";
  return (n / 1_000_000).toFixed(1) + "M";
}

// withCommas: deterministic thousands separators (not toLocaleString, whose
// output varies by the runtime's default locale).
function withCommas(n) {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

// usageTab: the compact string shown on the tab — "597k" or "600k · 60%".
function usageTab(u) {
  if (!u || !u.contextTokens) return "";
  let s = fmtTokens(u.contextTokens);
  if (typeof u.pct === "number") s += " · " + u.pct + "%";
  return s;
}

// usageTitle: the hover tooltip — "model · effort · N tokens".
function usageTitle(u) {
  if (!u || !u.contextTokens) return "";
  const parts = [];
  if (u.model) parts.push(u.model);
  if (u.effort) parts.push(u.effort);
  parts.push(withCommas(u.contextTokens) + " tokens");
  return parts.join(" · ");
}

// fmtCountdown: seconds-until-reset (resetsAt - now) as "5d 7h" / "2h 50m" /
// "13m" / "now". nowSec is passed in so the pure fn is testable without a clock.
function fmtCountdown(resetsAtSec, nowSec) {
  let d = (resetsAtSec || 0) - (nowSec || 0);
  if (d <= 0) return "now";
  const days = Math.floor(d / 86400); d -= days * 86400;
  const hours = Math.floor(d / 3600); d -= hours * 3600;
  const mins = Math.floor(d / 60);
  if (days > 0) return days + "d " + hours + "h";
  if (hours > 0) return hours + "h " + mins + "m";
  return mins + "m";
}

// rateLimitText: one account window as "<label> <pct>% · <countdown>", or "" when
// the window is absent (so the bottom bar renders nothing).
function rateLimitText(label, win, nowSec) {
  if (!win || typeof win.used_percentage !== "number") return "";
  return label + " " + win.used_percentage + "% · " + fmtCountdown(win.resets_at, nowSec);
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { fmtTokens, usageTab, usageTitle, fmtCountdown, rateLimitText };
}
