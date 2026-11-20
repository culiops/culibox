"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const { storeBannerVisible, storeExpiryText, storeUnlockOutcome, STORE_HONEST_STATEMENT, storePlaintextText, storeLocksSoonText, paintSideStore } = require("../static/store.js");

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

test("banner stays hidden on darwin for every Keychain state, including an unavailable one", () => {
  for (const kind of ["access", "key_lost", "store_unreadable"]) {
    assert.strictEqual(storeBannerVisible({ ...base, platform: "darwin", locked: false, unavailable: { kind, reason: "r", fix: "f" } }), false, kind);
  }
  assert.strictEqual(storeBannerVisible({ ...base, platform: "darwin", locked: true }), false);
});

test("expiry text names the Keychain failure by kind", () => {
  const dar = { ...base, platform: "darwin", locked: false };
  const un = (kind) => ({ ...dar, unavailable: { kind, reason: "r", fix: "f" } });
  assert.strictEqual(storeExpiryText(un("access"), 0), "Keychain access failed");
  assert.strictEqual(storeExpiryText(un("key_lost"), 0), "Keychain key lost");
  assert.strictEqual(storeExpiryText(un("store_unreadable"), 0), "Store unreadable");
  // An unknown kind still says it is unavailable, never a healthy Keychain.
  assert.match(storeExpiryText(un("surprise"), 0), /unavailable/i);
  assert.strictEqual(storeExpiryText(dar, 0), "Keychain (macOS)");
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

// A failed lock must report false (the Credentials strip reloads only on true,
// because a repaint would wipe the error lockStore just wrote into #store-err).
test("lockStore returns false and keeps the reason on failure, true on 204", async () => {
  const { lockStore } = require("../static/store.js");
  const err = { textContent: "" };
  const saved = { document: global.document, fetch: global.fetch };
  global.document = { getElementById: (id) => (id === "store-err" ? err : null) };
  try {
    global.fetch = async () => ({ status: 500, json: async () => ({ error: "boom" }) });
    assert.strictEqual(await lockStore(), false);
    assert.strictEqual(err.textContent, "boom");
    global.fetch = async () => ({ status: 502, json: async () => { throw new Error("not json"); } });
    assert.strictEqual(await lockStore(), false);
    assert.strictEqual(err.textContent, "Could not lock the store (502).");
    global.fetch = async () => { throw new Error("offline"); };
    assert.strictEqual(await lockStore(), false);
    assert.strictEqual(err.textContent, "Could not lock the store: no connection.");
    global.fetch = async (url) => (url === "/api/store/lock" ? { status: 204 } : { status: 404 });
    assert.strictEqual(await lockStore(), true);
    assert.strictEqual(err.textContent, "");
  } finally {
    global.document = saved.document;
    global.fetch = saved.fetch;
  }
});

// --- F77: "Store locks in N m" in the sidebar footer, the store's last hour only (#174, Task 9) ---

const LOCKS_NOW = Date.parse("2026-11-08T12:00:00Z");
// An unlocked Linux store that expires `ms` after LOCKS_NOW.
const locksIn = (ms, over) => ({ ...base, locked: false, expires_at: iso(LOCKS_NOW + ms), ...over });
const MIN = 60000;

test("locks-soon text: 42 min 30 s left reads 'Store locks in 42 m' (the minutes are floored)", () => {
  assert.strictEqual(storeLocksSoonText(locksIn(42 * MIN + 30000), LOCKS_NOW), "Store locks in 42 m");
  assert.strictEqual(storeLocksSoonText(locksIn(1 * MIN), LOCKS_NOW), "Store locks in 1 m");
  assert.strictEqual(storeLocksSoonText(locksIn(59 * MIN + 59000), LOCKS_NOW), "Store locks in 59 m");
});

test("locks-soon text: the line starts when 60 minutes remain: 60:00 shows, 60:01 does not", () => {
  assert.strictEqual(storeLocksSoonText(locksIn(60 * MIN), LOCKS_NOW), "Store locks in 60 m", "60:00 is inside the last hour");
  assert.strictEqual(storeLocksSoonText(locksIn(60 * MIN + 1000), LOCKS_NOW), "", "60:01 is not");
  assert.strictEqual(storeLocksSoonText(locksIn(2 * 3600 * 1000), LOCKS_NOW), "");
  assert.strictEqual(storeLocksSoonText(locksIn(6 * 24 * 3600 * 1000), LOCKS_NOW), "");
});

test("locks-soon text: under a minute says so, and nothing is shown at zero or once expired", () => {
  assert.strictEqual(storeLocksSoonText(locksIn(59000), LOCKS_NOW), "Store locks in under a minute");
  assert.strictEqual(storeLocksSoonText(locksIn(1), LOCKS_NOW), "Store locks in under a minute");
  assert.strictEqual(storeLocksSoonText(locksIn(0), LOCKS_NOW), "");
  assert.strictEqual(storeLocksSoonText(locksIn(-1000), LOCKS_NOW), "");
  assert.strictEqual(storeLocksSoonText(locksIn(-3600 * 1000), LOCKS_NOW), "");
});

test("locks-soon text: nothing for a locked, never-created, TTL-less, macOS, unavailable or missing store", () => {
  const soon = 30 * MIN;
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { locked: true }), LOCKS_NOW), "", "locked");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { initialized: false }), LOCKS_NOW), "", "not set up");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { expires_at: null }), LOCKS_NOW), "", "no expiry");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { expires_at: undefined }), LOCKS_NOW), "", "expiry absent");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { platform: "darwin" }), LOCKS_NOW), "", "macOS has no TTL");
  assert.strictEqual(storeLocksSoonText({ ...base, platform: "darwin", locked: false, expires_at: null }, LOCKS_NOW), "", "macOS, expires_at null");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { unavailable: { kind: "access", reason: "r", fix: "f" } }), LOCKS_NOW), "", "unavailable");
  assert.strictEqual(storeLocksSoonText(locksIn(soon, { expires_at: "not a date" }), LOCKS_NOW), "", "an unparseable expiry");
  assert.strictEqual(storeLocksSoonText(null, LOCKS_NOW), "", "no store (404)");
  assert.strictEqual(storeLocksSoonText(undefined, LOCKS_NOW), "", "no answer yet");
});

test("locks-soon text floors the minutes exactly as the Credentials strip does", () => {
  for (const ms of [MIN, MIN + 1, 9 * MIN + 59999, 10 * MIN, 42 * MIN + 30000, 59 * MIN, 59 * MIN + 59999]) {
    const st = locksIn(ms);
    const strip = /locks in (\d+) m$/.exec(storeExpiryText(st, LOCKS_NOW));
    assert.ok(strip, "the strip shows plain minutes below an hour: " + ms);
    assert.strictEqual(storeLocksSoonText(st, LOCKS_NOW), "Store locks in " + strip[1] + " m", String(ms));
  }
});

// A stand-in for #side-store: paintSideStore only touches hidden and textContent.
function sideStoreDoc(el) {
  return { getElementById: (id) => (id === "side-store" ? el : null) };
}

test("paintSideStore shows the line inside the last hour and hides it, empty, otherwise", () => {
  const el = { hidden: true, textContent: "" };
  const saved = global.document;
  global.document = sideStoreDoc(el);
  try {
    paintSideStore(locksIn(42 * MIN + 30000), LOCKS_NOW);
    assert.strictEqual(el.hidden, false);
    assert.strictEqual(el.textContent, "Store locks in 42 m");
    paintSideStore(locksIn(3 * 3600 * 1000), LOCKS_NOW); // a poll later the store was re-unlocked: a fresh 10 h
    assert.strictEqual(el.hidden, true);
    assert.strictEqual(el.textContent, "");
    paintSideStore(locksIn(20000), LOCKS_NOW);
    assert.strictEqual(el.textContent, "Store locks in under a minute");
    assert.strictEqual(el.hidden, false);
    paintSideStore(null, LOCKS_NOW); // 404: no store here
    assert.strictEqual(el.hidden, true);
    assert.strictEqual(el.textContent, "");
    paintSideStore(locksIn(10 * MIN), LOCKS_NOW);
    paintSideStore(locksIn(10 * MIN, { locked: true }), LOCKS_NOW); // it locked: the line goes
    assert.strictEqual(el.hidden, true);
    // A page without the node (the phone's markup never paints it, and a test page has none) is not an error.
    global.document = sideStoreDoc(null);
    assert.doesNotThrow(() => paintSideStore(locksIn(10 * MIN), LOCKS_NOW));
  } finally {
    global.document = saved;
  }
});

test("refreshStore repaints the sidebar line from the state it just read, on the existing poll (no new timer)", () => {
  const src = fs.readFileSync(path.join(__dirname, "..", "static", "store.js"), "utf8");
  const start = src.indexOf("async function refreshStore(");
  assert.ok(start >= 0, "refreshStore not found");
  const body = src.slice(start, src.indexOf("\n}\n", start) + 3);
  assert.match(body, /paintStoreBanner\(\);/);
  assert.match(body, /paintSideStore\(storeState, Date\.now\(\)\);/);
  assert.ok(!/set(Interval|Timeout)\(/.test(src.slice(src.indexOf("function paintSideStore("))), "the line rides the 30 s store poll: store.js adds no timer for it");
});

test("the sidebar footer has div#side-store[hidden] on its own row, between the elevated line and the links", () => {
  const html = fs.readFileSync(path.join(__dirname, "..", "static", "index.html"), "utf8");
  const foot = html.slice(html.indexOf('<div id="side-foot">'));
  const foot2 = foot.slice(0, foot.indexOf("</aside>"));
  assert.match(foot2, /<div id="side-store" hidden><\/div>/);
  assert.ok(foot2.indexOf('id="side-elevated"') > 0, "the elevated line is in the footer");
  assert.ok(foot2.indexOf('id="side-store"') > foot2.indexOf('id="side-elevated"'), "below the elevated line");
  assert.ok(foot2.indexOf('id="side-store"') < foot2.indexOf('class="xlinks"'), "above the cost and links line");
});

// --- Plan 7 Task 9 (#177): the store, one form (DD§A9, §B5, DC§7; R6, R19) ----------------------------
// The real store.js, notifymodel.js, attention.js and sysbanner.js in one vm context (one shared page scope,
// as in the browser) over fakedom. Fixtures use made-up names.
const vm = require("node:vm");
const { Node: FNode, Text: FText } = require("./fakedom.js");
const readStatic = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");

async function storePage(opts) {
  const o = Object.assign({ phone: false, signedOut: false, hidden: false, state: { ...base }, sessions: [{ id: "s1", need: "store_locked" }, { id: "s2", need: "store_locked" }] }, opts || {});
  const doc = { activeElement: null };
  doc.createElement = (t) => new FNode(t, doc);
  doc.createTextNode = (t) => new FText(t);
  doc.documentElement = new FNode("html", doc);
  if (o.hidden) doc.documentElement.className = "sidebar-hidden";
  const els = {};
  for (const id of ["store-banner-slot", "store-banner", "sys-banner-slot", "sessions", "rail48"]) { els[id] = new FNode("div", doc); els[id].hidden = true; }
  doc.getElementById = (id) => els[id] || null;
  const box = {
    document: doc, window: { innerWidth: o.phone ? 390 : 1280, location: { reload() {} } }, console, Date, Math, JSON, Promise, setImmediate,
    mobileActive: () => box.phone, phone: o.phone,
    sys: { lastOkAt: Date.now(), failSince: null, signedOut: o.signedOut, hiddenSince: null, paused: false, bootVersion: "v1", version: "v1", dismissedVersion: "" },
    lastSessions: o.sessions, versionLabel: (v) => v, refreshSessions() {}, renderRail48() {},
    calls: { toggle: 0, signedOut: 0, fetched: [] },
    fetch: async (url, init) => {
      box.calls.fetched.push(url);
      if (url === "/api/store") return { ok: true, status: 200, json: async () => box.state };
      return box.unlockResp;
    },
    state: o.state,
  };
  box.toggleSidebar = () => { box.calls.toggle++; doc.documentElement.className = ""; };
  box.sysNoteSignedOut = () => { box.calls.signedOut++; box.sys.signedOut = true; };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  FNode.prototype.addEventListener = FNode.prototype.addEventListener || function (t, f) { (this.handlers = this.handlers || {})[t] = f; };
  FNode.prototype.blur = FNode.prototype.blur || function () { if (this.doc.activeElement === this) this.doc.activeElement = null; };
  for (const f of ["notifymodel.js", "attention.js", "store.js", "sysbanner.js"]) new vm.Script(readStatic(f), { filename: f }).runInContext(ctx);
  // The real card builders from app.js (a classic script that cannot be required): cut out by name.
  const appSrc = readStatic("app.js");
  const cut = (name) => { const m = new RegExp("function " + name + "\\s*\\(").exec(appSrc); let d = 0; for (let i = appSrc.indexOf("{", appSrc.indexOf(")", m.index)); i < appSrc.length; i++) { if (appSrc[i] === "{") d++; else if (appSrc[i] === "}" && --d === 0) return appSrc.slice(m.index, i + 1); } };
  new vm.Script(cut("stripOpenButton") + "\n" + cut("renderNeedsBody"), { filename: "app.js(cut)" }).runInContext(ctx);
  await box.refreshStore();
  return { box, doc, els, ctx };
}
const sidebarForm = (page) => {
  const slot = page.els["store-banner-slot"];
  return { slot, field: slot.querySelector(".storepass"), btn: slot.querySelector(".btn-primary"), err: slot.querySelector(".storeerr") };
};
const submitKit = async (page, status, body) => {
  const { field, btn } = sidebarForm(page);
  page.box.unlockResp = resp(status, body);
  field.value = "pass-phrase-xyz";
  btn.handlers.click({ stopPropagation() {} });
  await flush(); await flush();
};

test("T9 desktop: a locked store paints ONE amber banner with ONE field in the sidebar slot; the full-width banner stays empty", async () => {
  const p = await storePage();
  const { slot, field, btn } = sidebarForm(p);
  assert.strictEqual(slot.hidden, false);
  const b = slot.children[0];
  assert.strictEqual(b.className, "banner is-needs");
  assert.strictEqual(b.querySelector(".banner-title").textContent, "Store locked");
  assert.strictEqual(b.querySelector(".dot").className, "dot dot-needs");
  assert.strictEqual(b.querySelector("p").textContent, "2 sessions can’t fetch credentials from the store.");
  assert.strictEqual(slot.querySelectorAll("input").length, 1);
  assert.strictEqual(field.attrs.placeholder === undefined ? field.placeholder : field.attrs.placeholder, "Store passphrase");
  assert.strictEqual(btn.textContent, "Unlock");
  assert.doesNotMatch(slot.textContent, /Stays unlocked/);
  assert.strictEqual(p.els["store-banner"].hidden, true);
  assert.strictEqual(p.els["store-banner"].children.length, 0);
});

test("T9 desktop: the session card strip is the frame sentence + Unlock…, with no field", async () => {
  const p = await storePage();
  const body = p.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: false, strip: true });
  assert.strictEqual(body.querySelectorAll("input").length, 0);
  assert.strictEqual(body.querySelector(".storetxt").textContent, "Credentials from the store can’t be fetched. Unlock it in the sidebar.");
  const go = body.querySelector(".btn");
  assert.strictEqual(go.textContent, "Unlock…");
  assert.strictEqual(go.title, "Focus the passphrase field in the sidebar");
  assert.strictEqual(body.querySelector(".storetitle"), null, "the strip label already says Store locked");
  const card = p.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: false });
  assert.strictEqual(card.querySelector(".storetitle").textContent, "Store locked");
});

test("T9 Unlock… focuses the field, and opens the sidebar first when it is collapsed to the rail", async () => {
  const p = await storePage({ hidden: true });
  const body = p.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: false, strip: true });
  body.querySelector(".btn").click();
  assert.strictEqual(p.box.calls.toggle, 1);
  assert.strictEqual(p.doc.activeElement, sidebarForm(p).field);
  const open = await storePage();
  open.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: false, strip: true }).querySelector(".btn").click();
  assert.strictEqual(open.box.calls.toggle, 0);
  assert.strictEqual(open.doc.activeElement, sidebarForm(open).field);
});

test("T9 wrong passphrase: red .field-error under the field and .is-invalid on it", async () => {
  const p = await storePage();
  await submitKit(p, 401, '{"error":"wrong passphrase"}');
  const { slot, field, err } = sidebarForm(p);
  assert.strictEqual(err.textContent, "Wrong passphrase.");
  assert.ok(err.classList.contains("field-error"));
  assert.ok(field.classList.contains("is-invalid"));
  assert.strictEqual(field.attrs["aria-invalid"], "true");
  const form = slot.querySelector(".storeform");
  assert.ok(form.children.indexOf(err) === form.children.indexOf(field) + 1, "the error sits right under the field");
  assert.strictEqual(field.disabled, false);
  assert.strictEqual(field.value, "");
});

test("T9 429: a muted line, not red, and the field is not marked invalid", async () => {
  const p = await storePage();
  await submitKit(p, 429, '{"error":"too many wrong passphrases — try again in 5 minutes"}');
  const { field, err } = sidebarForm(p);
  assert.match(err.textContent, /try again in 5 minutes/);
  assert.ok(err.classList.contains("muted") && err.classList.contains("t-sm"));
  assert.ok(!err.classList.contains("field-error"));
  assert.ok(!field.classList.contains("is-invalid"));
});

test("T9 401 (signed out): no wording of its own and no Reload button; it defers to the signed-out banner via sys", async () => {
  const p = await storePage();
  await submitKit(p, 401, "unauthorized");
  const { slot, err } = sidebarForm(p);
  assert.strictEqual(err.textContent, "");
  assert.strictEqual(slot.querySelectorAll(".btn").length, 1, "Unlock only, no Reload");
  assert.doesNotMatch(slot.textContent, /Signed out|reload/i);
  assert.strictEqual(p.box.calls.signedOut, 1);
});

test("T9 darwin (R6): no field anywhere; the card names the Keychain failure from storeState.unavailable; no banner", async () => {
  const un = { kind: "access", reason: "the login keychain is locked", fix: "Unlock the login keychain, then retry." };
  const p = await storePage({ state: { ...base, platform: "darwin", locked: false, unavailable: un } });
  assert.strictEqual(p.els["store-banner-slot"].hidden, true);
  assert.strictEqual(p.els["store-banner-slot"].children.length, 0);
  for (const need of ["store_locked", "ask"]) {
    const body = p.box.renderStoreBody({ id: "s1", need }, { mobile: false, strip: true });
    assert.strictEqual(body.querySelectorAll("input").length, 0);
    assert.strictEqual(body.querySelector(".storetitle").textContent, "Keychain access failed");
    assert.strictEqual(body.querySelector(".storetxt").textContent, "Unlock the login keychain, then retry.");
    assert.doesNotMatch(body.textContent, /passphrase|Unlock…/i);
  }
});

test("T9 phone is unchanged: the full-width banner with its form, the card with its form, no sidebar banner", async () => {
  const p = await storePage({ phone: true });
  assert.strictEqual(p.els["store-banner-slot"].hidden, true);
  assert.strictEqual(p.els["store-banner-slot"].children.length, 0);
  const el = p.els["store-banner"];
  assert.strictEqual(el.hidden, false);
  assert.strictEqual(el.children[0].className, "storebanner-msg");
  assert.strictEqual(el.children[0].textContent, "Store locked — tokens from the store can't be used");
  const f = el.querySelector(".storepass");
  assert.strictEqual(f.className, "fld storepass");
  assert.strictEqual(el.querySelector(".btn").className, "btn pri");
  assert.strictEqual(el.querySelector(".storeerr").className, "dlg-err storeerr");
  const form = el.querySelector(".storeform");
  assert.deepStrictEqual(form.children.map((c) => c.tag), ["input", "button", "div"], "field, button, error: the legacy order");
  const card = p.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: true });
  assert.strictEqual(card.className, "storecard mstorecard");
  assert.strictEqual(card.querySelector(".storetitle").textContent, "Store locked");
  assert.strictEqual(card.querySelectorAll("input").length, 1);
});

test("T9 signed out hides the store banner (M8.6b); the strip sentence stays; coming back brings the banner back", async () => {
  const p = await storePage({ signedOut: true });
  assert.strictEqual(p.els["store-banner-slot"].hidden, true);
  assert.strictEqual(p.els["store-banner-slot"].children.length, 0);
  assert.strictEqual(p.box.renderStoreBody({ id: "s1" }, { mobile: false, strip: true }).querySelectorAll("input").length, 0);
  p.box.sys.signedOut = false;
  p.box.paintStoreBanner();
  assert.strictEqual(p.els["store-banner-slot"].hidden, false);
  assert.strictEqual(p.els["store-banner-slot"].querySelectorAll("input").length, 1);
});

test("T9 a repaint keeps the field being typed into; only the count moves", async () => {
  const p = await storePage();
  const f1 = sidebarForm(p).field;
  p.box.lastSessions = [{ id: "s1", need: "store_locked" }];
  p.box.paintStoreBanner();
  assert.strictEqual(sidebarForm(p).field, f1);
  assert.strictEqual(p.els["store-banner-slot"].querySelector("p").textContent, "1 session can’t fetch credentials from the store.");
});

test("T9 rail marker priority (R19): a system banner wins, else the store (amber), else none; one marker", async () => {
  const p = await storePage();
  const m = p.box.sysRailMarkerModel();
  assert.deepStrictEqual({ t: m.title, tone: m.tone, key: m.key }, { t: "Store locked", tone: "amber", key: "store" });
  // can't reach CuliBox is a system banner: it takes the single marker, neutral.
  p.box.sys.lastOkAt = Date.now() - 20000; p.box.sys.failSince = Date.now() - 15000;
  const s = p.box.sysRailMarkerModel();
  assert.strictEqual(s.tone, "neutral");
  assert.notStrictEqual(s.key, "store");
  // signed out: system banner again, and the store has gone from the sidebar.
  const q = await storePage({ signedOut: true });
  assert.strictEqual(q.box.sysRailMarkerModel().tone, "neutral");
  // an unlocked store and a healthy poll: no marker.
  const r = await storePage({ state: { ...base, locked: false } });
  assert.strictEqual(r.box.sysRailMarkerModel(), null);
  // the phone has no rail marker either.
  const ph = await storePage({ phone: true });
  assert.strictEqual(ph.box.sysRailMarkerModel(), null);
});

test("T9 the REAL strip path (renderNeedsBody, inStrip) drops the repeated title and keeps Open terminal; the card keeps its title; the phone card is unchanged", async () => {
  const p = await storePage();
  const strip = p.box.renderNeedsBody({ id: "s1", name: "gap-sh2", need: "store_locked" }, { strip: true });
  assert.strictEqual(strip.querySelector(".storetitle"), null);
  assert.strictEqual(strip.querySelector(".storetxt").textContent, "Credentials from the store can\u2019t be fetched. Unlock it in the sidebar.");
  assert.deepStrictEqual(strip.querySelectorAll(".btn").map((b) => b.textContent), ["Unlock\u2026", "Open terminal"]);
  assert.strictEqual(strip.querySelectorAll("input").length, 0);
  const card = p.box.renderNeedsBody({ id: "s1", name: "gap-sh2", need: "store_locked" }, {});
  assert.strictEqual(card.querySelector(".storetitle").textContent, "Store locked");
  // darwin through the real strip: the Keychain title stays (the label says Store locked, not the failure).
  const d = await storePage({ state: { ...base, platform: "darwin", locked: false, unavailable: { kind: "access", reason: "r", fix: "Retry." } } });
  const ds = d.box.renderNeedsBody({ id: "s1", name: "gap-sh2", need: "store_locked" }, { strip: true });
  assert.strictEqual(ds.querySelector(".storetitle").textContent, "Keychain access failed");
  assert.strictEqual(ds.querySelectorAll("input").length, 0);
  // phone card: still title + sentence + form.
  const ph = await storePage({ phone: true });
  const mc = ph.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: true });
  assert.strictEqual(mc.querySelector(".storetitle").textContent, "Store locked");
  assert.strictEqual(mc.querySelectorAll("input").length, 1);
});

test("T9 R23: signed out, Unlock… opens a collapsed sidebar and focuses the signed-out banner's Sign in again", async () => {
  const p = await storePage({ signedOut: true, hidden: true });
  p.box.sysBannerSync();
  const body = p.box.renderStoreBody({ id: "s1", need: "store_locked" }, { mobile: false, strip: true });
  body.querySelector(".btn").click();
  assert.strictEqual(p.box.calls.toggle, 1);
  const act = p.els["sys-banner-slot"].querySelector('[data-act="signin"]');
  assert.ok(act);
  assert.strictEqual(act.textContent, "Sign in again");
  assert.strictEqual(p.doc.activeElement, act);
});
