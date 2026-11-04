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

const { acctChoices, acctPutBody, acctSpawnPick, acctLoginProfile, acctLoginText } = require("../static/accounts.js");

test("acctChoices: default first, list order kept, a kept value never dropped", () => {
  const list = [{ name: "default", default: true }, { name: "work" }, { name: "lab" }];
  assert.deepEqual(acctChoices(list, ""), ["default", "work", "lab"]);
  assert.deepEqual(acctChoices(list, "work"), ["default", "work", "lab"]);
  // A profile pinned to an account the list does not (yet) carry keeps it:
  // an empty or failed fetch must not silently re-save the profile as default.
  assert.deepEqual(acctChoices([], "work"), ["default", "work"]);
  assert.deepEqual(acctChoices(null, ""), ["default"]);
  assert.deepEqual(acctChoices(list, "default"), ["default", "work", "lab"]);
});

test("acctPutBody always carries both replaced fields", () => {
  const a = { name: "work", token_secret: "WORK_TOKEN", inherit_personal: true };
  assert.deepEqual(acctPutBody(a, { inherit_personal: false }), { token_secret: "WORK_TOKEN", inherit_personal: false });
  assert.deepEqual(acctPutBody({ name: "x" }, {}), { token_secret: "", inherit_personal: false });
  assert.deepEqual(acctPutBody({ name: "x" }, { token_secret: "T" }), { token_secret: "T", inherit_personal: false });
});

test("acctSpawnPick: prefill wins, else the profile's, else default", () => {
  assert.equal(acctSpawnPick(undefined, ""), "default");
  assert.equal(acctSpawnPick(undefined, undefined), "default");
  assert.equal(acctSpawnPick(undefined, "work"), "work");
  // Duplicate of a default-account session over a work-pinned profile: the
  // explicit default must survive (ruling C2).
  assert.equal(acctSpawnPick("", "work"), "default");
  assert.equal(acctSpawnPick("lab", "work"), "lab");
});

test("acctLoginProfile prefers the bare claude profile, else any unpinned claude", () => {
  const ps = [{ name: "bash" }, { name: "pinned", claude: true, account: "work" }, { name: "c2", claude: true }, { name: "claude", claude: true }];
  assert.equal(acctLoginProfile(ps).name, "claude");
  assert.equal(acctLoginProfile(ps.slice(0, 3)).name, "c2");
  assert.equal(acctLoginProfile(ps.slice(0, 2)), null);
  assert.equal(acctLoginProfile(null), null);
});

test("acctLoginText names each state", () => {
  assert.equal(acctLoginText("token"), "token");
  assert.equal(acctLoginText("logged_in"), "logged in");
  assert.equal(acctLoginText("unknown"), "login unknown");
  assert.equal(acctLoginText("weird"), "weird");
  assert.equal(acctLoginText(undefined), "login unknown");
});

const { acctTokenId, acctTokenOwned, acctTokenMode } = require("../static/accounts.js");

test("acctTokenId mirrors account.TokenID", () => {
  assert.equal(acctTokenId("work"), "ACCT_WORK_TOKEN");
  assert.equal(acctTokenId("client-a"), "ACCT_CLIENT_A_TOKEN");
  assert.equal(acctTokenId("0x"), "ACCT_0X_TOKEN");
});

test("acctTokenOwned: only the account's own id is ours to delete", () => {
  assert.equal(acctTokenOwned({ name: "client-a", token_secret: "ACCT_CLIENT_A_TOKEN" }), true);
  assert.equal(acctTokenOwned({ name: "work", token_secret: "WORK_TOK" }), false);
  assert.equal(acctTokenOwned({ name: "work", token_secret: "ACCT_OTHER_TOKEN" }), false);
  assert.equal(acctTokenOwned({ name: "work" }), false);
  assert.equal(acctTokenOwned(null), false);
});

test("acctTokenMode: set wins, then the store gate, then paste", () => {
  const ready = { initialized: true, locked: true };
  assert.equal(acctTokenMode({ name: "w", token_secret: "ACCT_W_TOKEN" }, null), "set");
  assert.equal(acctTokenMode({ name: "w" }, ready), "paste"); // a locked store still takes a value
  assert.equal(acctTokenMode({ name: "w" }, { initialized: false }), "store");
  assert.equal(acctTokenMode({ name: "w" }, null), "store");
});

test("acctLoginText covers the token states", () => {
  assert.equal(acctLoginText("token"), "token");
  assert.equal(acctLoginText("token_locked"), "token (store locked)");
  assert.equal(acctLoginText("token_missing"), "token not found");
  assert.equal(acctLoginText("logged_in"), "logged in");
});
