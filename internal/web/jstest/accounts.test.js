"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { ACCT_DEFAULT, acctLimitsMap, acctFor, acctLimitsAge, acctBadge, acctOptionLabel } = require("../static/accounts.js");

test("acctLimitsMap tolerates the pre-accounts flat shape", () => {
  const flat = { updated_at: 1, five_hour: { used_percentage: 5, resets_at: 9 } };
  assert.deepEqual(acctLimitsMap(flat), { default: flat });
  const map = { default: flat, work: { updated_at: 2 } };
  assert.equal(acctLimitsMap(map), map);
  assert.deepEqual(acctLimitsMap(undefined), {});
  assert.deepEqual(acctLimitsMap(null), {});
});

test("acctFor defaults", () => {
  assert.equal(acctFor({}), ACCT_DEFAULT);
  assert.equal(acctFor(null), ACCT_DEFAULT);
  assert.equal(acctFor({ account: "work" }), "work");
});

test("acctLimitsAge: fresh, minutes, hours, stale past 5h, missing", () => {
  const now = 100000;
  assert.deepEqual(acctLimitsAge(now - 30, now), { text: "just now", stale: false });
  assert.deepEqual(acctLimitsAge(now - 12 * 60, now), { text: "12m ago", stale: false });
  assert.deepEqual(acctLimitsAge(now - 3 * 3600, now), { text: "3h ago", stale: false });
  assert.deepEqual(acctLimitsAge(now - 6 * 3600, now), { text: "6h ago", stale: true });
  assert.deepEqual(acctLimitsAge(0, now), { text: "no data", stale: true });
});

test("acctBadge only for non-default", () => {
  assert.equal(acctBadge({}), null);
  assert.equal(acctBadge({ account: "default" }), null);
  assert.deepEqual(acctBadge({ account: "work" }), { text: "work", title: "Agent account: work" });
});

test("acctOptionLabel shows both windows when present", () => {
  const now = 1000;
  assert.equal(acctOptionLabel("work", { updated_at: now - 30, five_hour: { used_percentage: 42.4 }, seven_day: { used_percentage: 7 } }, now), "work · 5h 42% · wk 7%");
  assert.equal(acctOptionLabel("idle", undefined, now), "idle · no quota data");
});

test("acctOptionLabel never presents stale quota as current", () => {
  const now = 100000;
  const stale = { updated_at: now - 6 * 3600, five_hour: { used_percentage: 42.4 }, seven_day: { used_percentage: 7 } };
  assert.equal(acctOptionLabel("work", stale, now), "work · 5h 42% · wk 7% (6h old)");
  const fresh = { updated_at: now - 60, five_hour: { used_percentage: 10 } };
  assert.equal(acctOptionLabel("work", fresh, now), "work · 5h 10%");
  // numbers with no timestamp at all are also unverifiable
  assert.equal(acctOptionLabel("work", { five_hour: { used_percentage: 10 } }, now), "work · 5h 10% (no data)");
});
