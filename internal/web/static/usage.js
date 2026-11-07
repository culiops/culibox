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
  parts.push(withCommas(u.contextTokens) + " tokens" + (u.postCompact ? " (after compact)" : ""));
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

// meterModel turns one account window into everything the header meter needs.
// Returns null when the window is absent — the caller then renders nothing at
// all, rather than a 0% bar that reads as "plenty left" when the truth is
// "unknown". The design board states that rule for the whole block; applying it
// per window is OUR decision, since five_hour and seven_day are independently
// nullable and the board does not cover the one-present-one-absent case.
//
// pct is rounded for the label but the bar keeps the precise value: rounding the
// bar too would redraw a 42.5% bar at 43% for no reason.
//
// HOT is "past 75", so exactly 75 is not hot. Hot is peach (--warn), never the
// yellow reserved for "an agent needs you" — a quota threshold borrowing that
// yellow makes the header cry wolf.
const METER_HOT_PCT = 75;

function meterModel(label, win, nowSec) {
  if (!win || typeof win.used_percentage !== "number") return null;
  // resets_at in the past: the window has rolled over, so the stored percentage
  // describes the OLD window. The new usage is unknown, so show "reset" with no
  // number and an empty bar, never the old % (and never a 0% that would claim
  // "plenty left"). resets_at 0/missing is "unknown reset time", not a reset.
  if (win.resets_at > 0 && win.resets_at <= nowSec) {
    return { label: label, pct: null, width: "0%", hot: false, unknown: true, reset: "reset", limit: false, limitText: "" };
  }
  const raw = win.used_percentage;
  const clamped = Math.min(100, Math.max(0, raw));
  // fmtCountdown returns the word "now" (not a duration) when the reset time is
  // missing — resets_at has no omitempty server-side. Appending " left" to that
  // word would read as the nonsensical "now left".
  const c = fmtCountdown(win.resets_at, nowSec);
  const pct = Math.round(raw);
  // limit: the window is used up. It follows the rounded label, so a number the
  // label would draw as "100%" or more is never printed as a plain percentage that
  // reads like 78%. limitText is the sidebar's wording for it: the reset in the
  // words, or just "limit reached" when no reset time is known (c is "now" then).
  const limit = pct >= 100;
  return {
    label: label,
    pct: pct,
    width: clamped + "%",
    hot: raw > METER_HOT_PCT,
    unknown: false,
    reset: c === "now" ? "resets now" : c + " left",
    limit: limit,
    limitText: limit ? (c === "now" ? "limit reached" : "limit reached \u00b7 resets " + c) : "",
  };
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { withCommas, fmtTokens, usageTab, usageTitle, fmtCountdown, meterModel };
}
