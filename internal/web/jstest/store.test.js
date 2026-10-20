"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { storeBannerVisible, storeExpiryText, storeUnlockOutcome, STORE_HONEST_STATEMENT, storePlaintextText } = require("../static/store.js");

const base = { initialized: true, locked: true, in_use: true, platform: "linux", expires_at: null, values: 2, plaintext_copies: [] };

test("banner shows only for an initialized, locked, in-use, non-darwin store", () => {
  assert.strictEqual(storeBannerVisible(base), true);
  assert.strictEqual(storeBannerVisible({ ...base, platform: "darwin" }), false);
  assert.strictEqual(storeBannerVisible({ ...base, in_use: false }), false);
  assert.strictEqual(storeBannerVisible({ ...base, locked: false }), false);
  assert.strictEqual(storeBannerVisible({ ...base, initialized: false }), false);
  assert.strictEqual(storeBannerVisible(null), false);
  assert.strictEqual(storeBannerVisible(undefined), false);
});

const H = 3600 * 1000;
const iso = (ms) => new Date(ms).toISOString();

test("expiry text: days and hours, minutes, locked, Keychain", () => {
  const now = Date.parse("2026-10-20T00:00:00Z");
  const un = (ms) => ({ ...base, locked: false, expires_at: iso(now + ms) });
  assert.strictEqual(storeExpiryText(un(6 * 24 * H + 23 * H + 10 * 60000), now), "unlocked · locks in 6 d 23 h");
  assert.strictEqual(storeExpiryText(un(59 * 60000 + 20000), now), "unlocked · locks in 59 m");
  assert.strictEqual(storeExpiryText(un(5 * H + 7 * 60000), now), "unlocked · locks in 5 h 7 m");
  assert.strictEqual(storeExpiryText({ ...base, platform: "darwin", locked: false }, now), "Keychain (macOS)");
  assert.strictEqual(storeExpiryText({ ...base, platform: "darwin", locked: true }, now), "Keychain (macOS)");
  assert.strictEqual(storeExpiryText(base, now), "locked");
  assert.strictEqual(storeExpiryText(un(-1000), now), "locked");
  assert.strictEqual(storeExpiryText(un(20000), now), "unlocked · locks in under a minute");
  assert.strictEqual(storeExpiryText({ ...base, locked: false, expires_at: null }, now), "unlocked");
  assert.strictEqual(storeExpiryText({ ...base, initialized: false }, now), "not set up");
});

test("unlock outcome maps each status to one honest line", () => {
  assert.deepEqual(storeUnlockOutcome(204, "", ""), { ok: true, msg: "", signedOut: false });
  assert.deepEqual(storeUnlockOutcome(401, "wrong passphrase", ""), { ok: false, msg: "Wrong passphrase.", signedOut: false });
  // authGuard's 401 is plain text "unauthorized": a signed-out page, not a bad passphrase.
  const out = storeUnlockOutcome(401, "unauthorized", "");
  assert.strictEqual(out.ok, false);
  assert.strictEqual(out.signedOut, true);
  assert.match(out.msg, /Signed out/);
  assert.strictEqual(storeUnlockOutcome(429, "too many wrong passphrases — try again in 9 minutes", "540").msg, "too many wrong passphrases — try again in 9 minutes");
  assert.match(storeUnlockOutcome(429, "", "").msg, /Too many/);
  assert.match(storeUnlockOutcome(403, "", "").msg, /signed-in browser session/);
  assert.strictEqual(storeUnlockOutcome(400, "this platform's store unlocks with the Keychain, not a passphrase", "").msg, "this platform's store unlocks with the Keychain, not a passphrase");
  assert.match(storeUnlockOutcome(400, "", "").msg, /Could not unlock/);
  assert.match(storeUnlockOutcome(0, "", "").msg, /no connection/i);
  assert.match(storeUnlockOutcome(500, "could not unlock the store", "").msg, /could not unlock the store/);
});

test("the honest statement is the exact approved copy", () => {
  assert.strictEqual(STORE_HONEST_STATEMENT,
    "Values are encrypted at rest, against disk snapshots and backups. While unlocked, any process running as your user — host-tier agents included — can read them. Boxed agents cannot.");
});

test("plaintext copies line names them, and is empty without any", () => {
  assert.strictEqual(storePlaintextText([]), "");
  assert.strictEqual(storePlaintextText(null), "");
  assert.strictEqual(storePlaintextText(["GH_TOKEN", "CLAUDE_TOKEN"]),
    "Plaintext copy still in the daemon environment: GH_TOKEN, CLAUDE_TOKEN — delete it there to get the protection");
});
