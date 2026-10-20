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

// --- DOM-free unit tests of the unlock UI against a minimal fake document ------

function fakeEl(tag) {
  const e = {
    tagName: String(tag).toUpperCase(), className: "", textContent: "", value: "", children: [], handlers: {}, attrs: {}, isConnected: true,
    disabled: false,
    setAttribute(k, v) { this.attrs[k] = v; },
    addEventListener(t, f) { this.handlers[t] = f; },
    append(...c) { this.children.push(...c); },
    appendChild(c) { this.children.push(c); },
    replaceChildren(...c) { this.children = c; },
    focus() { fakeDoc.activeElement = this; },
    blur() { if (fakeDoc.activeElement === this) fakeDoc.activeElement = null; },
    classList: null,
  };
  e.classList = { contains: (c) => e.className.split(/\s+/).includes(c) };
  return e;
}
const fakeDoc = {
  activeElement: null,
  created: [],
  createElement(t) { const e = fakeEl(t); this.created.push(e); return e; },
  createTextNode(t) { return { text: t }; },
};
globalThis.document = fakeDoc;

function mount() {
  fakeDoc.created.length = 0;
  const { renderUnlockForm } = require("../static/store.js");
  const slot = fakeEl("div");
  renderUnlockForm(slot);
  const field = fakeDoc.created.find((e) => e.tagName === "INPUT");
  const btn = fakeDoc.created.find((e) => e.tagName === "BUTTON");
  const err = fakeDoc.created.find((e) => e.className.includes("storeerr"));
  return { slot, field, btn, err };
}
const flush = () => new Promise((r) => setImmediate(r));
const resp = (status, body) => ({ status, headers: { get: () => "" }, text: async () => body });

test("the unlock UI contains no <form>: nothing can bubble a submit to the spawn form", () => {
  const { field, btn } = mount();
  assert.strictEqual(fakeDoc.created.some((e) => e.tagName === "FORM"), false);
  assert.strictEqual(field.type, "password");
  assert.strictEqual(field.attrs.autocomplete, "off");
  assert.strictEqual(btn.type, "button");
});

test("Enter in the field unlocks, and the event is stopped and defaulted", async () => {
  const { field, err } = mount();
  let calls = 0;
  globalThis.fetch = async (url, opts) => { calls++; assert.strictEqual(url, "/api/store/unlock"); assert.deepEqual(JSON.parse(opts.body), { passphrase: "hunter2hunter2" }); return resp(401, '{"error":"wrong passphrase"}'); };
  field.value = "hunter2hunter2";
  let prevented = false, stopped = false;
  field.handlers.keydown({ key: "Enter", preventDefault() { prevented = true; }, stopPropagation() { stopped = true; } });
  assert.strictEqual(field.value, "", "cleared at once, before the response");
  await flush();
  assert.strictEqual(prevented && stopped, true);
  assert.strictEqual(calls, 1);
  assert.strictEqual(err.textContent, "Wrong passphrase.");
});

test("a non-Enter key does nothing; an empty Enter makes no request", async () => {
  const { field, err } = mount();
  let calls = 0;
  globalThis.fetch = async () => { calls++; return resp(204, ""); };
  field.handlers.keydown({ key: "a", preventDefault() {}, stopPropagation() {} });
  field.handlers.keydown({ key: "Enter", preventDefault() {}, stopPropagation() {} });
  await flush();
  assert.strictEqual(calls, 0);
  assert.match(err.textContent, /Type the store passphrase/);
});

test("the list is held while an unlock request is in flight, and released after", async () => {
  const { storeUnlockFocused } = require("../static/store.js");
  const { field, btn } = mount();
  let release;
  globalThis.fetch = () => new Promise((r) => { release = () => r(resp(401, '{"error":"wrong passphrase"}')); });
  fakeDoc.activeElement = null;
  assert.strictEqual(storeUnlockFocused(), false);
  field.value = "x";
  btn.handlers.click({ stopPropagation() {} });
  assert.strictEqual(storeUnlockFocused(), true, "focus left the field on click; the request still holds the list");
  release();
  await flush();
  fakeDoc.activeElement = null;
  assert.strictEqual(storeUnlockFocused(), false);
});

test("focus is read from the DOM: a removed focused field cannot leave the list held", () => {
  const { storeUnlockFocused } = require("../static/store.js");
  const { field } = mount();
  fakeDoc.activeElement = field;
  assert.strictEqual(storeUnlockFocused(), true);
  field.isConnected = false; // replaceChildren removed it; no blur fired
  assert.strictEqual(storeUnlockFocused(), false);
  fakeDoc.activeElement = fakeEl("input"); // some other field
  assert.strictEqual(storeUnlockFocused(), false);
});

test("success drops focus from the field so nothing stays held", async () => {
  const { storeUnlockFocused } = require("../static/store.js");
  const { field } = mount();
  globalThis.fetch = async (url) => (url === "/api/store/unlock" ? resp(204, "") : resp(404, ""));
  globalThis.refreshSessions = () => {};
  fakeDoc.getElementById = () => null;
  fakeDoc.querySelector = () => null;
  fakeEl("input").focus.call(field);
  field.value = "right-passphrase";
  field.handlers.keydown({ key: "Enter", preventDefault() {}, stopPropagation() {} });
  await flush(); await flush();
  assert.notStrictEqual(fakeDoc.activeElement, field);
  assert.strictEqual(storeUnlockFocused(), false);
});

test("storeRecentMsg keeps a failure line for 15 s, then forgets it", () => {
  const { storeRecentMsg } = require("../static/store.js");
  assert.strictEqual(storeRecentMsg("Wrong passphrase.", 1000, 2000), "Wrong passphrase.");
  assert.strictEqual(storeRecentMsg("Wrong passphrase.", 1000, 16001), "");
  assert.strictEqual(storeRecentMsg("", 1000, 2000), "");
});

test("a rebuilt form shows the failure that just happened", async () => {
  const first = mount();
  globalThis.fetch = async () => resp(429, '{"error":"too many wrong passphrases — try again in 9 minutes"}');
  first.field.value = "x";
  first.field.handlers.keydown({ key: "Enter", preventDefault() {}, stopPropagation() {} });
  await flush();
  const rebuilt = mount(); // the poll replaced the row
  assert.match(rebuilt.err.textContent, /too many wrong passphrases/);
});
