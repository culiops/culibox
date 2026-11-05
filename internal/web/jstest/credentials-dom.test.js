"use strict";
// credentials.js on the desktop page: the table-mode delete confirm and the width
// that picks tables over cards, against a minimal fake DOM (the repo has no jsdom,
// see activitytab.test.js). The pure decisions live in credentials.test.js; this
// covers what only the DOM can show: where a confirmation is put, and what is left
// behind when it is cancelled.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const STATIC = path.join(__dirname, "..", "static");

// "tag", ".cls" or "tag.cls": every selector credentials.js asks this DOM for.
function matcher(sel) {
  const m = /^([a-z0-9]*)(?:\.([\w-]+))?$/i.exec(sel);
  assert.ok(m, "the fake DOM does not support the selector " + sel);
  return (n) => (!m[1] || n.tag === m[1]) && (!m[2] || n.className.split(/\s+/).includes(m[2]));
}

class Text { constructor(t) { this.text = t; this.parent = null; } get textContent() { return this.text; } }
class Node {
  constructor(tag) {
    this.tag = tag; this.children = []; this.parent = null; this.own = "";
    this.className = ""; this.attrs = {}; this.dataset = {}; this.disabled = false; this.colSpan = 1;
    const self = this;
    this.classList = {
      contains: (c) => self.className.split(/\s+/).includes(c),
      add: (c) => { if (!self.className.split(/\s+/).includes(c)) self.className = (self.className + " " + c).trim(); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  get parentElement() { return this.parent; }
  get firstElementChild() { return this.children.find((c) => c instanceof Node) || null; }
  get rows() { return this.children.filter((c) => c.tag === "tr"); }
  get cells() { return this.children.filter((c) => c.tag === "td" || c.tag === "th"); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener() {}
  appendChild(c) { if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1); c.parent = this; this.children.push(c); return c; }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  replaceChildren(...cs) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = ""; cs.forEach((c) => this.appendChild(c)); }
  remove() { if (this.parent) { this.parent.children.splice(this.parent.children.indexOf(this), 1); this.parent = null; } }
  closest(sel) { const hit = matcher(sel); for (let n = this; n; n = n.parent) if (n instanceof Node && hit(n)) return n; return null; }
  querySelectorAll(sel) {
    const hit = matcher(sel), out = [];
    const walk = (n) => { for (const c of n.children) { if (!(c instanceof Node)) continue; if (hit(c)) out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

// One page. `width` is what #creds-body measures; `asPage` is the desktop Settings page.
function page({ asPage = true, width = 800 } = {}) {
  const calls = [];
  const byId = {
    "settings-modal": Object.assign(new Node("dialog"), { className: asPage ? "as-page" : "" }),
    "creds-body": Object.assign(new Node("div"), { clientWidth: width }),
  };
  const doc = { createElement: (t) => new Node(t), createTextNode: (t) => new Text(t), getElementById: (id) => byId[id] || null };
  const box = {
    document: doc, console, Promise, JSON, Date, setTimeout, clearTimeout, reloaded: 0,
    api: async (p, opt) => { calls.push({ path: p, method: (opt && opt.method) || "GET" }); if (box.apiFail) throw box.apiFail; return {}; },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  for (const f of ["pending.js", "store.js", "credentials.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  // The reload every action ends in repaints the whole screen; here it only counts.
  box.loadCredentials = () => { box.reloaded++; };
  return { box, doc, calls, byId };
}

const tok = (name, over) => ({ kind: "token", name, provider: "sentry", label: "", description: "", prod: false, env: ["SENTRY_AUTH_TOKEN"], hosts: ["sentry.io"], value_kind: "store", source: "web", status: "ok", used_by: [], ...over });
const group = (entries) => ({ key: "token:sentry", title: "Sentry", entries });

// A token table as the page builds it, and the Delete button of its first entry.
function tokenTable(p, entries) {
  const table = p.box.credTable(group(entries || [tok("sentry-prod")]));
  const tb = table.querySelectorAll("tbody")[0];
  const del = tb.querySelectorAll("button").find((b) => b.textContent === "Delete");
  return { table, tb, del };
}

test("table mode: a Delete button's confirmation goes in a full-width row under its entry", () => {
  const p = page();
  const { tb, del } = tokenTable(p);
  del.onclick();
  const x = tb.querySelector("tr.ctab-x");
  assert.ok(x, "a ctab-x row is added to the entry's tbody");
  assert.strictEqual(tb.rows[0].cells.length, 5, "the entry row has five cells");
  assert.strictEqual(x.cells[0].colSpan, 5, "the row spans the real columns, not a larger number");
  assert.ok(x.querySelector(".confirm"), "the confirm box is in that row");
  assert.match(x.querySelector(".confirm").textContent, /^Delete the token sentry-prod\?/);
});

test("table mode: pressing Delete twice does not stack a second confirmation", () => {
  const p = page();
  const { tb, del } = tokenTable(p);
  del.onclick();
  del.onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 1);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1);
});

test("table mode: Cancel removes the confirm box and the row that held it", () => {
  const p = page();
  const { tb, del } = tokenTable(p);
  del.onclick();
  const cancel = tb.querySelectorAll("button").find((b) => b.textContent === "Cancel");
  cancel.onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 0);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 0, "no empty tr.ctab-x is left behind");
  assert.strictEqual(tb.rows.length, 1, "the tbody is back to its one entry row");
  assert.strictEqual(p.calls.length, 0, "Cancel sends nothing");
});

test("table mode: after Cancel the confirmation can be opened again", () => {
  const p = page();
  const { tb, del } = tokenTable(p);
  del.onclick();
  tb.querySelectorAll("button").find((b) => b.textContent === "Cancel").onclick();
  del.onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 1);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1);
});

test("table mode: Cancel keeps a row that still holds the entry's error", () => {
  const p = page();
  const { tb, del } = tokenTable(p, [tok("sentry-prod", { status: "missing", error: "could not read the store" })]);
  assert.ok(tb.querySelector("tr.ctab-x"), "a broken entry has its error row from the start");
  del.onclick();
  tb.querySelectorAll("button").find((b) => b.textContent === "Cancel").onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 0);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1, "the error row stays");
  assert.match(tb.querySelector("tr.ctab-x").textContent, /could not read the store/);
});

test("table mode: Delete sends DELETE for that token, then reloads", async () => {
  const p = page();
  const { tb, del } = tokenTable(p, [tok("sentry prod/1")]);
  del.onclick();
  const go = tb.querySelectorAll("button").find((b) => b.textContent === "Delete" && b !== del);
  go.onclick();
  await new Promise((r) => setImmediate(r));
  assert.deepStrictEqual(p.calls, [{ path: "/api/credentials/tokens/sentry%20prod%2F1", method: "DELETE" }]);
  assert.strictEqual(p.box.reloaded, 1);
  assert.strictEqual(go.disabled, true, "the button stays disabled while the table repaints");
});

test("table mode: a refused Delete keeps the confirmation, shows why, and re-enables the button", async () => {
  const p = page();
  const { tb, del } = tokenTable(p);
  p.box.apiFail = Object.assign(new Error("in use"), { code: "in_use", body: { used_by: ["ops"] } });
  del.onclick();
  const go = tb.querySelectorAll("button").find((b) => b.textContent === "Delete" && b !== del);
  go.onclick();
  await new Promise((r) => setImmediate(r));
  assert.strictEqual(p.box.reloaded, 0);
  assert.strictEqual(go.disabled, false);
  assert.match(tb.querySelector(".confirm").querySelector(".ent-err").textContent, /Used by profile ops/);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1, "the row stays while the confirmation is open");
});

test("card mode: the card is its own host, and Cancel removes only the box", () => {
  const p = page({ asPage: false });
  const card = p.box.credTokenCard(tok("sentry-prod"));
  const del = card.querySelectorAll("button").find((b) => b.textContent === "Delete");
  del.onclick();
  assert.strictEqual(card.querySelectorAll(".confirm").length, 1);
  assert.strictEqual(card.querySelectorAll("tr").length, 0, "no table row in card mode");
  card.querySelectorAll("button").find((b) => b.textContent === "Cancel").onclick();
  assert.strictEqual(card.querySelectorAll(".confirm").length, 0);
});

test("credEntryHost: a card button gives its card; a button outside any entry gives null", () => {
  const p = page();
  const card = new Node("article"); card.className = "ent";
  const b = new Node("button"); card.appendChild(b);
  assert.strictEqual(p.box.credEntryHost(b), card);
  assert.strictEqual(p.box.credEntryHost(new Node("button")), null);
  assert.strictEqual(p.box.credEntryHost(null), null);
});

test("credEntryHost: one extra row per entry, reused while it exists", () => {
  const p = page();
  const { tb } = tokenTable(p);
  const btn = tb.querySelectorAll("button")[0];
  const a = p.box.credEntryHost(btn);
  assert.strictEqual(a.className, "ctab-ex");
  assert.strictEqual(p.box.credEntryHost(btn), a);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1);
});

test("table headers: the empty actions column has a visually hidden label", () => {
  const p = page();
  const { table } = tokenTable(p);
  const ths = table.querySelectorAll("th");
  const last = ths[ths.length - 1];
  assert.strictEqual(last.textContent, "Actions");
  assert.ok(last.querySelector(".sr-only"), "the label is hidden from sight, not from a screen reader");
  assert.strictEqual(ths.filter((t) => t.textContent === "").length, 0, "no header cell is left without a name");
});

// --- AWS connections and GitHub Apps: the same confirm, their own paths ------------

const aws = (name, over) => ({ kind: "aws", name, account_id: "226198813800", role_arn: "arn:aws:iam::226198813800:role/agentbox-agent", mode: "assume", status: "ok", source: "web", prod: false, used_by: [], base: { state: "none_found" }, elevate: null, ...over });
const ghApp = (name, over) => ({ kind: "github", name, client_id: "Iv23liAbCdEf0123", key_ref_kind: "store", status: "ok", source: "web", used_by: [], ...over });

// A table of `entries` for `key` ("aws" or "github"), the tbody of the one named `name`,
// and the Delete button on its row.
function entryTable(p, key, entries, name) {
  const table = p.box.credTable({ key, title: key, entries });
  const tb = table.querySelectorAll("tbody").find((t) => t.rows[0].cells[0].textContent.startsWith(name));
  assert.ok(tb, "no row for " + name);
  const del = tb.rows[0].querySelectorAll("button").find((b) => b.textContent === "Delete");
  return { table, tb, del };
}
const press = (tb, label, not) => tb.querySelectorAll("button").find((b) => b.textContent === label && b !== not);
const settle = () => new Promise((r) => setImmediate(r));

test("AWS table: Delete puts the confirmation in a row under the entry, naming the connection", () => {
  const p = page();
  const { tb, del } = entryTable(p, "aws", [aws("sandbox-agent")], "sandbox-agent");
  del.onclick();
  const x = tb.querySelector("tr.ctab-x");
  assert.ok(x && x.querySelector(".confirm"));
  assert.strictEqual(x.cells[0].colSpan, tb.rows[0].cells.length);
  assert.match(x.querySelector(".confirm").textContent, /^Delete the connection sandbox-agent\?/);
});

test("AWS table: Cancel removes the confirm box and the row that held it", () => {
  const p = page();
  const { tb, del } = entryTable(p, "aws", [aws("sandbox-agent")], "sandbox-agent");
  del.onclick();
  press(tb, "Cancel").onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 0);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 0, "no empty tr.ctab-x is left behind");
  assert.strictEqual(tb.rows.length, 1);
  assert.strictEqual(p.calls.length, 0, "Cancel sends nothing");
  del.onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 1, "and it can be opened again");
});

test("AWS table: Cancel keeps the write warning that shares the row", () => {
  const p = page();
  const writes = aws("prod-writer", { base: { state: "writes", actions: ["s3:PutObject"] } });
  const { tb, del } = entryTable(p, "aws", [writes], "prod-writer");
  assert.match(tb.querySelector("tr.ctab-x").textContent, /Allowed here: s3:PutObject/);
  del.onclick();
  press(tb, "Cancel").onclick();
  assert.strictEqual(tb.querySelectorAll(".confirm").length, 0);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1, "the warning's row stays");
  assert.match(tb.querySelector("tr.ctab-x").textContent, /Allowed here: s3:PutObject/);
});

test("AWS table: Delete sends DELETE to that connection's path, then reloads", async () => {
  const p = page();
  const { tb, del } = entryTable(p, "aws", [aws("other-one"), aws("sandbox agent/1")], "sandbox agent/1");
  del.onclick();
  const go = press(tb, "Delete", del);
  go.onclick();
  await settle();
  assert.deepStrictEqual(p.calls, [{ path: "/api/credentials/aws/sandbox%20agent%2F1", method: "DELETE" }]);
  assert.strictEqual(p.box.reloaded, 1);
  assert.strictEqual(go.disabled, true);
});

test("AWS table: a refused Delete keeps the confirmation, shows why, and re-enables the button", async () => {
  const p = page();
  const { tb, del } = entryTable(p, "aws", [aws("sandbox-agent")], "sandbox-agent");
  p.box.apiFail = Object.assign(new Error("in use"), { code: "in_use", body: { used_by: ["ops"] } });
  del.onclick();
  const go = press(tb, "Delete", del);
  go.onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 0);
  assert.strictEqual(go.disabled, false);
  assert.match(tb.querySelector(".confirm").querySelector(".ent-err").textContent, /Used by profile ops/);
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 1);
});

test("GitHub App table: Delete sends DELETE to the GitHub Apps path, not the tokens one", async () => {
  const p = page();
  const { tb, del } = entryTable(p, "github", [ghApp("release-bot"), ghApp("culibox agents")], "culibox agents");
  del.onclick();
  assert.match(tb.querySelector(".confirm").textContent, /^Delete the GitHub App culibox agents\?/);
  press(tb, "Delete", del).onclick();
  await settle();
  assert.deepStrictEqual(p.calls, [{ path: "/api/credentials/github-apps/culibox%20agents", method: "DELETE" }]);
  assert.strictEqual(p.box.reloaded, 1);
});

test("GitHub App table: Cancel removes the box and its row, and sends nothing", () => {
  const p = page();
  const { tb, del } = entryTable(p, "github", [ghApp("release-bot")], "release-bot");
  del.onclick();
  press(tb, "Cancel").onclick();
  assert.strictEqual(tb.querySelectorAll("tr.ctab-x").length, 0);
  assert.strictEqual(p.calls.length, 0);
});

test("a token's Delete goes to the tokens path (each kind has its own)", async () => {
  const p = page();
  const { tb, del } = tokenTable(p, [tok("sentry-prod")]);
  del.onclick();
  press(tb, "Delete", del).onclick();
  await settle();
  assert.deepStrictEqual(p.calls, [{ path: "/api/credentials/tokens/sentry-prod", method: "DELETE" }]);
});

// --- tables or cards -----------------------------------------------------------

test("credTableMode: the page draws tables once #creds-body is wide enough, cards below", () => {
  const { CRED_TABLE_MIN_WIDTH } = require("../static/credentials.js");
  assert.ok(CRED_TABLE_MIN_WIDTH > 0);
  assert.strictEqual(page({ width: CRED_TABLE_MIN_WIDTH }).box.credTableMode(), true);
  assert.strictEqual(page({ width: CRED_TABLE_MIN_WIDTH - 1 }).box.credTableMode(), false);
});

test("credTableMode: a modal (not the page) keeps cards at any width", () => {
  assert.strictEqual(page({ asPage: false, width: 2000 }).box.credTableMode(), false);
});

test("credTableMode: an unmeasured body (0 wide: the tab is hidden) keeps tables, as before", () => {
  assert.strictEqual(page({ width: 0 }).box.credTableMode(), true);
});

test("a narrow page paints cards, a wide page paints tables, from the same view", () => {
  const view = { store: { initialized: true, locked: false, platform: "linux" }, aws: [], github_apps: [], tokens: [tok("sentry-prod")], values: [], legacy_secrets: [], invalid: [] };
  const paint = (width) => {
    const p = page({ width });
    const body = p.byId["creds-body"];
    p.box.renderCredentials(view);
    return body;
  };
  const wide = paint(900), narrow = paint(500);
  assert.strictEqual(wide.querySelectorAll("table").length > 0, true);
  assert.strictEqual(wide.querySelectorAll(".ent").length, 0);
  assert.strictEqual(narrow.querySelectorAll("table").length, 0);
  assert.strictEqual(narrow.querySelectorAll(".ent").length > 0, true);
  assert.strictEqual(narrow.querySelectorAll(".cstrip").length, 1, "the store strip is the page's quiet one in both");
  assert.strictEqual(wide.querySelectorAll(".cstrip").length, 1);
});

test("a modal keeps its own amber strip", () => {
  const p = page({ asPage: false, width: 900 });
  p.box.renderCredentials({ store: { initialized: true, locked: false, platform: "linux" }, aws: [], github_apps: [], tokens: [tok("sentry-prod")], values: [], legacy_secrets: [], invalid: [] });
  assert.strictEqual(p.byId["creds-body"].querySelectorAll(".cstrip").length, 0);
  assert.strictEqual(p.byId["creds-body"].querySelectorAll(".strip").length, 1);
});

// --- the store strip on macOS: a Keychain failure shows its fix, never an unlock form (#65) ---

const kcStore = (over) => ({ initialized: true, locked: false, platform: "darwin", passphrase_mode: false, ...over });
const unavailable = (kind) => ({ kind, reason: "The Keychain would not give the key.", fix: "Open Keychain Access and allow agent-box." });
const stripOf = (p, store) => p.box.credStrip({ store });
const btnTexts = (n) => n.querySelectorAll("button").map((b) => b.textContent);

test("strip, darwin store unavailable: Keychain chip, the server's reason and fix, no unlock form, no Lock now", () => {
  for (const kind of ["access", "key_lost", "store_unreadable"]) {
    const p = page();
    const wrap = stripOf(p, kcStore({ unavailable: unavailable(kind) }));
    const strip = wrap.querySelector(".strip");
    const chip = strip.querySelector(".st");
    assert.strictEqual(chip.textContent, "Keychain", kind);
    assert.ok(chip.className.split(/\s+/).includes("locked"), kind + ": amber chip class");
    assert.ok(strip.className.split(/\s+/).includes("need"), kind + ": the strip asks for attention");
    assert.match(strip.textContent, /The Keychain would not give the key\. Open Keychain Access and allow agent-box\./, kind);
    assert.ok(strip.textContent.includes(p.box.storeUnavailableLabel(kind)), kind + ": the kind label");
    assert.strictEqual(wrap.querySelectorAll(".storeslot").length, 0, kind + ": no unlock slot");
    assert.strictEqual(wrap.querySelectorAll("input").length, 0, kind + ": no passphrase field");
    assert.deepEqual(btnTexts(wrap), [], kind + ": no Lock now, no Unlock, no Create");
  }
});

test("strip, darwin store unavailable and not initialized: still no Create button (never offer to overwrite)", () => {
  const p = page();
  const wrap = stripOf(p, kcStore({ initialized: false, unavailable: unavailable("key_lost") }));
  assert.deepEqual(btnTexts(wrap), []);
  assert.match(wrap.textContent, /Open Keychain Access/);
});

test("strip, healthy darwin Keychain: unchanged, no Lock now", () => {
  const p = page();
  const wrap = stripOf(p, kcStore());
  const strip = wrap.querySelector(".strip");
  assert.strictEqual(strip.querySelector(".st").textContent, "Keychain");
  assert.ok(strip.querySelector(".st").className.split(/\s+/).includes("ready"));
  assert.match(strip.textContent, /Keychain \(macOS\)/);
  assert.deepEqual(btnTexts(wrap), []);
});

test("strip, Linux: locked shows the unlock form, unlocked shows Lock now (unchanged)", () => {
  const p = page();
  const locked = stripOf(p, { initialized: true, locked: true, platform: "linux", passphrase_mode: true });
  assert.strictEqual(locked.querySelector(".st").textContent, "Locked");
  assert.strictEqual(locked.querySelectorAll(".storeslot").length, 1);
  assert.deepEqual(btnTexts(locked), ["Unlock"]);
  const open = stripOf(p, { initialized: true, locked: false, platform: "linux", passphrase_mode: true });
  assert.strictEqual(open.querySelector(".st").textContent, "Unlocked");
  assert.deepEqual(btnTexts(open), ["Lock now"]);
  const none = stripOf(p, { initialized: false, locked: false, platform: "linux" });
  assert.deepEqual(btnTexts(none), ["Create the encrypted store"]);
});

// --- #27: a live window crossing the table/card width repaints ---------------------

const liveView = { store: { initialized: true, locked: false, platform: "linux" }, aws: [], github_apps: [], tokens: [tok("sentry-prod")], values: [], legacy_secrets: [], invalid: [] };

// A page whose ResizeObserver is fake: `resizeTo` moves #creds-body and delivers the entry.
function livePage(width, opts) {
  const p = page({ width, ...(opts || {}) });
  const observers = [];
  p.box.ResizeObserver = class {
    constructor(cb) { this.cb = cb; this.targets = []; observers.push(this); }
    observe(t) { this.targets.push(t); }
    disconnect() {}
  };
  p.observers = observers;
  // The repaint waits for the next frame (see credWatchWidth); `frame()` is that frame.
  const frames = [];
  p.box.requestAnimationFrame = (f) => { frames.push(f); return frames.length; };
  p.frame = () => { while (frames.length) frames.shift()(); };
  p.queued = () => frames.length;
  p.deliver = (w, entryWidth) => {
    p.byId["creds-body"].clientWidth = w;
    for (const o of observers) o.cb([{ contentRect: { width: entryWidth === undefined ? w : entryWidth } }]);
  };
  p.resizeTo = (w, entryWidth) => { p.deliver(w, entryWidth); p.frame(); };
  p.shape = () => ({ tables: p.byId["creds-body"].querySelectorAll("table").length, cards: p.byId["creds-body"].querySelectorAll(".ent").length });
  return p;
}

test("the resize observer is wired once, to #creds-body, however often it paints", () => {
  const p = livePage(900);
  p.box.renderCredentials(liveView);
  p.box.renderCredentials(liveView);
  p.box.renderCredentials(liveView);
  assert.strictEqual(p.observers.length, 1);
  assert.deepStrictEqual(p.observers[0].targets, [p.byId["creds-body"]]);
});

test("narrowing a live page repaints the cards; widening it again brings the tables back", () => {
  const p = livePage(900);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "opened wide: tables");
  p.resizeTo(500);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "narrowed: cards, no table left to clip Edit and Delete");
  p.resizeTo(900);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "widened: tables again");
});

test("the repaint is not made inside the observer callback (that raises the ResizeObserver loop error)", () => {
  const p = livePage(900);
  p.box.renderCredentials(liveView);
  p.deliver(500);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "still the old paint while the callback runs");
  assert.strictEqual(p.queued(), 1);
  p.frame();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "painted on the frame");
});

test("a burst of observer callbacks costs one repaint per frame, and the last width wins", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  p.deliver(650); p.deliver(500); p.deliver(450);
  assert.strictEqual(p.queued(), 1, "one frame requested, not three");
  p.frame();
  assert.ok(p.shape().cards > 0);
  p.deliver(900); p.deliver(699); p.deliver(900); // ends where it started: nothing to change
  p.frame();
  assert.ok(p.shape().tables > 0, "ended wide: tables");
  assert.ok(body.children.length > 0);
});

test("without requestAnimationFrame (a test, an old shell) the repaint still happens, on a timer", async () => {
  const p = livePage(900);
  p.box.requestAnimationFrame = undefined;
  p.box.renderCredentials(liveView);
  p.deliver(500);
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0);
});

test("the threshold is the painter's: 699 is cards, 700 is tables", () => {
  const p = livePage(900);
  p.box.renderCredentials(liveView);
  p.resizeTo(699);
  assert.strictEqual(p.shape().tables, 0);
  p.resizeTo(700);
  assert.ok(p.shape().tables > 0);
});

test("a resize that stays on one side of the threshold repaints nothing", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const first = body.children.slice();
  for (const w of [901, 950, 1200, 705, 700]) p.resizeTo(w);
  assert.strictEqual(body.children.length, first.length);
  assert.ok(body.children.every((c, i) => c === first[i]), "the same nodes, not repainted");
  p.resizeTo(400);
  const cards = body.children.slice();
  for (const w of [399, 300, 650, 699]) p.resizeTo(w);
  assert.ok(body.children.every((c, i) => c === cards[i]), "cards stay as they were");
});

test("an unmeasurable entry (width 0: the tab is hidden) changes nothing", () => {
  const p = livePage(500);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const first = body.children.slice();
  p.resizeTo(0);
  assert.ok(body.children.every((c, i) => c === first[i]));
  assert.strictEqual(p.shape().tables, 0, "and not turned into tables by the 0");
});

test("a modal never repaints into tables, however wide it is dragged", () => {
  const p = livePage(500, { asPage: false });
  p.box.renderCredentials(liveView);
  const first = p.byId["creds-body"].children.slice();
  p.resizeTo(2000);
  assert.ok(p.byId["creds-body"].children.every((c, i) => c === first[i]));
  assert.strictEqual(p.shape().tables, 0);
});

test("with the Add / Edit view open, crossing the width does not replace it; leaving it paints the right mode", () => {
  const vm = require("node:vm");
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const form = p.box.credEl("form", "add-token");
  body.replaceChildren(form); // what the open view looks like: it owns the body
  vm.runInContext('credForm = { kind: "token" }', p.box);
  p.resizeTo(500);
  assert.deepStrictEqual(body.children, [form], "the form is still the only thing there");
  p.resizeTo(900);
  p.resizeTo(500);
  assert.deepStrictEqual(body.children, [form]);
  vm.runInContext("credForm = null", p.box);
  p.box.renderCredentials(liveView); // closing the form repaints from the cached view
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "closed at 500 wide: cards");
});

test("a held repaint (unlock field focused) is not forced by a resize, and still lands after", () => {
  const vm = require("node:vm");
  const p = livePage(900);
  p.box.storeUnlockFocused = () => true;
  p.box.renderCredentials(liveView);
  assert.strictEqual(p.shape().tables, 0, "nothing painted yet: the repaint is held");
  p.box.storeUnlockFocused = () => false;
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0);
  p.box.storeUnlockFocused = () => true;
  p.resizeTo(500);
  assert.ok(p.shape().tables > 0, "held: the table the user is typing beside stays");
  p.box.storeUnlockFocused = () => false;
  p.resizeTo(480);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "released: the next resize paints the right mode");
});

test("no ResizeObserver (an old browser): painting still works and nothing throws", () => {
  const p = page({ width: 900 });
  p.box.renderCredentials(liveView);
  assert.ok(p.byId["creds-body"].querySelectorAll("table").length > 0);
});

// --- #123: a width change never repaints away what the user is in the middle of ------

const deleteBtn = (p) => p.byId["creds-body"].querySelectorAll("button").find((b) => b.textContent === "Delete");
const cancelBtn = (p) => p.byId["creds-body"].querySelectorAll("button").find((b) => b.textContent === "Cancel");
const sameNodes = (a, b) => a.length === b.length && a.every((c, i) => c === b[i]);

test("an open Delete confirmation survives a resize across the threshold; Cancel then paints the right mode", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  deleteBtn(p).onclick();
  assert.strictEqual(body.querySelectorAll(".confirm").length, 1);
  const kept = body.children.slice();
  p.resizeTo(500);
  assert.ok(sameNodes(body.children, kept), "the body was not replaced");
  assert.strictEqual(body.querySelectorAll(".confirm").length, 1, "the confirmation is still there to answer");
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "still the table it was opened in");
  cancelBtn(p).onclick();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "Cancel lets the held repaint through: cards at 500");
  assert.strictEqual(body.querySelectorAll(".confirm").length, 0);
});

test("a confirmation opened in a card survives widening to tables the same way", () => {
  const p = livePage(500);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  deleteBtn(p).onclick();
  p.resizeTo(900);
  assert.strictEqual(body.querySelectorAll(".confirm").length, 1);
  assert.ok(p.shape().cards > 0 && p.shape().tables === 0);
  cancelBtn(p).onclick();
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "after Cancel: tables at 900");
});

test("a held repaint that is no longer needed is not made when the confirmation is cancelled", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  deleteBtn(p).onclick();
  p.resizeTo(500);
  p.resizeTo(900); // back where it was painted
  const kept = body.children.slice();
  cancelBtn(p).onclick();
  assert.ok(sameNodes(body.children, kept), "same mode as the paint: nothing to redo");
});

test("a part-typed store passphrase survives a resize; emptied, the next focus change paints the right mode", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials({ ...liveView, store: { initialized: false, locked: false, platform: "linux", passphrase_mode: true } });
  body.querySelectorAll("button").find((b) => b.id === "store-create").onclick();
  const typed = body.querySelectorAll(".storepass")[0];
  assert.ok(typed, "the create form's passphrase fields are marked");
  typed.value = "part-typed";
  p.resizeTo(500);
  assert.ok(p.shape().tables > 0, "not repainted while a passphrase is typed (the field need not have focus)");
  assert.strictEqual(body.querySelectorAll(".storepass")[0], typed);
  assert.strictEqual(typed.value, "part-typed");
  typed.value = "";
  p.box.credFocusLeft(); // what the body's focusout does
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "nothing left to lose: the right mode is painted");
});

test("a focus change with nothing held repaints nothing", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const kept = body.children.slice();
  p.box.credFocusLeft();
  assert.ok(sameNodes(body.children, kept));
});

test("a frame that finds the body unmeasurable (the tab was hidden after the observer fired) repaints nothing", () => {
  const p = livePage(500); // painted as cards
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const kept = body.children.slice();
  p.deliver(520); // a measurable entry: a frame is queued
  body.clientWidth = 0; // the tab is hidden before the frame
  p.frame();
  assert.ok(sameNodes(body.children, kept), "0 wide reads as tables, and must not flip a hidden tab");
  assert.strictEqual(p.shape().tables, 0);
});

// A repaint held for the unlock field is made when focus leaves it, but not over what was
// typed there: clicking Unlock moves focus first (mousedown), and the repaint, running
// before the click, wiped the passphrase and the button along with it.
test("a repaint held for the unlock field does not wipe a passphrase typed while it was held", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials({ ...liveView, store: { initialized: true, locked: true, platform: "linux", passphrase_mode: true } });
  const field = body.querySelectorAll(".storepass")[0];
  assert.ok(field, "the unlock form is painted");
  p.box.storeUnlockFocused = () => true; // the user clicked into the field
  p.resizeTo(500); // dragged the window across the threshold: the repaint is held
  assert.ok(p.shape().tables > 0, "held");
  field.value = "typed-passphrase";
  p.box.storeUnlockFocused = () => false; // mousedown on Unlock: focus has left the field
  const kept = body.children.slice();
  p.box.credFocusLeft(); // the body's focusout, a tick later
  assert.ok(sameNodes(body.children, kept), "the form and its Unlock button are still the ones on screen");
  assert.strictEqual(field.value, "typed-passphrase");
  field.value = ""; // submitted (the field is cleared on every attempt), or the user cleared it
  p.box.credFocusLeft();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "nothing left to lose: the held repaint is made");
});

test("a repaint held for the unlock field is still made when nothing was typed", () => {
  const p = livePage(900);
  p.box.renderCredentials({ ...liveView, store: { initialized: true, locked: true, platform: "linux", passphrase_mode: true } });
  p.box.storeUnlockFocused = () => true;
  p.resizeTo(500);
  assert.ok(p.shape().tables > 0, "held");
  p.box.storeUnlockFocused = () => false;
  p.box.credFocusLeft();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "focus left an empty field: repainted");
});
