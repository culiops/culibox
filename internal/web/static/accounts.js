"use strict";
// Agent accounts: pure helpers (node-tested) + the Settings tab (DOM, below).
// Names are ACCT_/acct-prefixed: classic scripts share one global scope.

const ACCT_DEFAULT = "default";
const ACCT_STALE_SEC = 5 * 3600;

// acctLimitsMap accepts /api/meta.rate_limits in either shape: the per-account
// map, or the pre-accounts flat object (one poll can straddle a deploy).
function acctLimitsMap(rl) {
  if (!rl) return {};
  if (rl.five_hour || rl.seven_day || rl.updated_at) return { default: rl };
  return rl;
}

function acctFor(session) {
  return (session && session.account) || ACCT_DEFAULT;
}

function acctLimitsAge(updatedAt, nowSec) {
  if (!updatedAt) return { text: "no data", stale: true };
  const age = Math.max(0, nowSec - updatedAt);
  const stale = age > ACCT_STALE_SEC;
  if (age < 60) return { text: "just now", stale };
  if (age < 3600) return { text: Math.floor(age / 60) + "m ago", stale };
  return { text: Math.floor(age / 3600) + "h ago", stale };
}

function acctBadge(session) {
  const a = acctFor(session);
  if (a === ACCT_DEFAULT) return null;
  return { text: a, title: "Agent account: " + a };
}

// acctOptionLabel: the spawn select's text for one account. Quota numbers
// older than ACCT_STALE_SEC (or with no timestamp) carry their age, so a
// stale reading is never mistaken for a current one.
function acctOptionLabel(name, limits, nowSec) {
  const parts = [];
  if (limits && limits.five_hour) parts.push("5h " + Math.round(limits.five_hour.used_percentage) + "%");
  if (limits && limits.seven_day) parts.push("wk " + Math.round(limits.seven_day.used_percentage) + "%");
  if (!parts.length) return name + " · no quota data";
  let label = name + " · " + parts.join(" · ");
  const age = acctLimitsAge(limits.updated_at, nowSec);
  if (age.stale) label += " (" + age.text.replace(" ago", " old") + ")";
  return label;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = { ACCT_DEFAULT, ACCT_STALE_SEC, acctLimitsMap, acctFor, acctLimitsAge, acctBadge, acctOptionLabel };
}
