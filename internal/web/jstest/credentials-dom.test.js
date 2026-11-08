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
    this.className = ""; this.attrs = {}; this.dataset = {}; this.disabled = false; this.colSpan = 1; this.handlers = {};
    const self = this;
    this.classList = {
      contains: (c) => self.className.split(/\s+/).includes(c),
      add: (c) => { if (!self.className.split(/\s+/).includes(c)) self.className = (self.className + " " + c).trim(); },
      remove: (c) => { self.className = self.className.split(/\s+/).filter((x) => x && x !== c).join(" "); },
      toggle: (c, on) => { const has = self.className.split(/\s+/).includes(c); if (on === undefined ? !has : on) self.classList.add(c); else if (has) self.classList.remove(c); },
    };
  }
  get textContent() { return this.own + this.children.map((c) => c.textContent).join(""); }
  set textContent(v) { this.children.forEach((c) => { c.parent = null; }); this.children = []; this.own = String(v); }
  get parentElement() { return this.parent; }
  get firstChild() { return this.children[0] || null; }
  get lastChild() { return this.children[this.children.length - 1] || null; }
  get firstElementChild() { return this.children.find((c) => c instanceof Node) || null; }
  // A table's rows; a textarea's `rows` is a plain number the form sets.
  get rows() { return this._rows !== undefined ? this._rows : this.children.filter((c) => c.tag === "tr"); }
  set rows(v) { this._rows = v; }
  get cells() { return this.children.filter((c) => c.tag === "td" || c.tag === "th"); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  // Listeners are kept, so a test can fire the keydown the menu wrapper listens to (`fire`).
  addEventListener(t, f) { (this.handlers[t] = this.handlers[t] || []).push(f); }
  fire(t, ev) { for (const f of this.handlers[t] || []) f(ev); }
  contains(n) { for (let x = n; x; x = x.parent) if (x === this) return true; return false; }
  focus() { Node.active = this; }
  insertBefore(c, ref) {
    if (c.parent) c.parent.children.splice(c.parent.children.indexOf(c), 1);
    c.parent = this;
    const at = ref ? this.children.indexOf(ref) : -1;
    if (at < 0) this.children.push(c); else this.children.splice(at, 0, c);
    return c;
  }
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

// The Credentials tab's own footers and buttons, by id (index.html:584-598). Left out of a page unless
// `tab` is set: most tests here never touch them, and a missing id is what the code treats as "no footer".
const TAB_FOOT_IDS = ["foot-creds", "foot-creds-add", "foot-creds-form"];
const TAB_BUTTON_IDS = ["aws-check-all", "aws-cancel", "aws-save", "cred-form-cancel", "cred-form-save"];

// One page. `width` is what #creds-body measures; `asPage` is the desktop Settings page.
function page({ asPage = true, width = 800, tab = false } = {}) {
  const calls = [];
  const byId = {
    "settings-modal": Object.assign(new Node("dialog"), { className: asPage ? "as-page" : "" }),
    "creds-body": Object.assign(new Node("div"), { clientWidth: width }),
  };
  if (tab) {
    for (const id of TAB_FOOT_IDS) byId[id] = Object.assign(new Node("div"), { hidden: id !== "foot-creds" });
    for (const id of TAB_BUTTON_IDS) byId[id] = new Node("button");
    byId["aws-foot-note"] = new Node("span");
    byId["creds-err"] = new Node("div");
  }
  // `listeners` are the document's own (a click elsewhere closes the header menu); `activeElement` is whatever last took focus.
  const listeners = {};
  Node.active = null;
  const doc = {
    createElement: (t) => new Node(t), createElementNS: (ns, t) => new Node(t), createTextNode: (t) => new Text(t),
    getElementById: (id) => byId[id] || null,
    addEventListener: (t, f) => { (listeners[t] = listeners[t] || []).push(f); },
    get activeElement() { return Node.active; },
  };
  const box = {
    document: doc, console, Promise, JSON, Date, setTimeout, clearTimeout, reloaded: 0,
    // `reply(path, opt)` answers a call; the default is an empty object.
    api: async (p, opt) => {
      const call = { path: p, method: (opt && opt.method) || "GET" };
      if (opt && opt.body) call.body = JSON.parse(opt.body);
      calls.push(call);
      if (box.apiFail) throw box.apiFail;
      return box.reply ? box.reply(p, opt) : {};
    },
  };
  box.globalThis = box;
  const ctx = vm.createContext(box);
  // svgicon.js and kitmenu.js load before credentials.js on the page (index.html): the Stored values chevron and the header menu use them.
  for (const f of ["pending.js", "store.js", "svgicon.js", "kitmenu.js", "credentials.js"]) {
    new vm.Script(fs.readFileSync(path.join(STATIC, f), "utf8"), { filename: f }).runInContext(ctx);
  }
  // The reload every action ends in repaints the whole screen; here it only counts.
  box.realLoadCredentials = box.loadCredentials;
  box.loadCredentials = () => { box.reloaded++; };
  return { box, doc, calls, byId, listeners };
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

// F11 (#171): Close and secondary actions are never primary, and a primary is never restyled as a
// secondary by CSS. The tables have no add button at all (UI plan 4: the page head's "Add credential"
// menu is the one add, frame M2.3); the cards keep the AWS add as the primary.
test("the AWS add is the primary in the cards and absent from the tables", () => {
  const view = { store: { initialized: true, locked: false, platform: "linux" }, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] };
  const add = (width) => {
    const p = page({ width });
    p.box.renderCredentials(view);
    return p.byId["creds-body"].querySelectorAll("button").find((b) => b.textContent === "Add AWS accounts");
  };
  assert.strictEqual(add(900), undefined, "tables: the header menu adds");
  assert.strictEqual(add(500).className, "btn pri", "cards");
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

test("strip, healthy darwin Keychain: the page says it once (dot, name, Keychain), no Lock now", () => {
  const p = page();
  const wrap = stripOf(p, kcStore());
  const strip = wrap.querySelector(".strip");
  assert.strictEqual(strip.querySelectorAll(".st").length, 0, "no Unlocked/Keychain chip on top of the line below");
  assert.strictEqual(strip.querySelector("b").textContent, "Encrypted store");
  assert.strictEqual(strip.querySelector("span.muted").textContent, "Keychain (macOS)");
  assert.deepEqual(btnTexts(wrap), []);
  // The phone's modal keeps the chip and its sentence.
  const phone = stripOf(page({ asPage: false }), kcStore());
  assert.strictEqual(phone.querySelector(".st").textContent, "Keychain");
  assert.ok(phone.querySelector(".st").className.split(/\s+/).includes("ready"));
  assert.match(phone.textContent, /Keychain \(macOS\)/);
});

test("strip, Linux: locked shows the unlock form, unlocked shows Lock now, not set up offers Create (unchanged states)", () => {
  const p = page();
  const locked = stripOf(p, { initialized: true, locked: true, platform: "linux", passphrase_mode: true });
  assert.strictEqual(locked.querySelector(".st").textContent, "Locked");
  assert.strictEqual(locked.querySelectorAll(".storeslot").length, 1);
  assert.deepEqual(btnTexts(locked), ["Unlock"]);
  const open = stripOf(p, { initialized: true, locked: false, platform: "linux", passphrase_mode: true });
  assert.deepEqual(btnTexts(open), ["Lock now"]);
  const none = stripOf(p, { initialized: false, locked: false, platform: "linux" });
  assert.deepEqual(btnTexts(none), ["Create the encrypted store"]);
  // The phone's modal keeps the chip.
  const phoneOpen = stripOf(page({ asPage: false }), { initialized: true, locked: false, platform: "linux", passphrase_mode: true });
  assert.strictEqual(phoneOpen.querySelector(".st").textContent, "Unlocked");
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

// A repaint held for a DATA change (the unlock request was in flight) while a Delete
// confirmation was open: Cancel must let it through even though the width did not change.
test("Cancel on a Delete confirmation makes a repaint held for new data, not only one for a new width", () => {
  const p = livePage(900);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  deleteBtn(p).onclick();
  p.box.storeUnlockFocused = () => true; // an unlock is in flight: the body holds its repaint
  p.box.renderCredentials({ ...liveView, tokens: [tok("sentry-prod"), tok("sentry-added")] });
  assert.ok(!/sentry-added/.test(body.textContent), "held: the old body is still on screen");
  p.box.storeUnlockFocused = () => false;
  cancelBtn(p).onclick();
  assert.ok(/sentry-added/.test(body.textContent), "the new data is painted once the confirmation is gone");
  assert.strictEqual(body.querySelectorAll(".confirm").length, 0);
});

// --- #27: a table is drawn only when it fits, measured from its content ---------------
//
// The browser knows what the fake DOM cannot: how wide a table section's content is. A
// section here reports it the way a browser does: `clientWidth` is its box (the body's
// width), `scrollWidth` is the larger of that and what `p.need()` says the content needs.
// `paints` counts how often the body is replaced.

function fitPage(width, need, opts) {
  const p = livePage(width, opts);
  const body = p.byId["creds-body"];
  p.need = typeof need === "function" ? need : () => need;
  const make = p.doc.createElement;
  p.doc.createElement = (t) => {
    const n = make(t);
    if (t === "section") {
      Object.defineProperty(n, "clientWidth", { get: () => body.clientWidth });
      Object.defineProperty(n, "scrollWidth", { get: () => Math.max(body.clientWidth, p.need(body.clientWidth)) });
    }
    return n;
  };
  p.paints = 0;
  const replace = body.replaceChildren.bind(body);
  body.replaceChildren = (...a) => { p.paints++; return replace(...a); };
  p.needW = () => vm.runInContext("credNeedW", p.box);
  return p;
}

test("a table paint that overflows its section ends in cards, and remembers how much room the table needs", () => {
  const p = fitPage(800, 823);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "cards: the table did not fit");
  assert.strictEqual(p.needW(), 800 + 23 + 2, "the body's width, plus the overflow, plus 2px to spare");
  assert.ok(p.paints <= 2, "at most one extra paint, not a loop: " + p.paints);
  assert.strictEqual(p.paints, 2);
});

test("a table that fits stays a table and learns nothing", () => {
  const p = fitPage(800, 725);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0);
  assert.strictEqual(p.needW(), 0);
  assert.strictEqual(p.paints, 1);
});

test("a width repaint from the cached view uses the need it learned: tables from it up, cards below, no measuring", () => {
  const p = fitPage(800, 823);
  p.box.renderCredentials(liveView);
  p.paints = 0;
  p.resizeTo(824);
  assert.strictEqual(p.shape().tables, 0, "824 is below the learned 825: still cards, nothing repainted");
  assert.strictEqual(p.paints, 0);
  p.resizeTo(825);
  assert.ok(p.shape().tables > 0, "825 draws tables again");
  assert.strictEqual(p.paints, 1, "one paint: it fits, so there is nothing to measure twice");
  p.resizeTo(1400);
  assert.ok(p.shape().tables > 0);
  p.resizeTo(700);
  assert.strictEqual(p.shape().tables, 0, "and cards below it again, as the floor says");
  assert.strictEqual(p.needW(), 825, "the need was kept across the width repaints");
});

test("new data (a new view object) forgets the need, so tables are tried again at the current width", () => {
  const p = fitPage(800, 823);
  p.box.renderCredentials(liveView);
  assert.strictEqual(p.needW(), 825);
  p.need = () => 700; // the long row is gone from the new data
  p.paints = 0;
  p.box.renderCredentials({ ...liveView });
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "tables again: the cache of what did not fit is not kept for other data");
  assert.strictEqual(p.needW(), 0);
  assert.strictEqual(p.paints, 1);
});

test("repainting the same view object keeps the need (a width repaint is not new data)", () => {
  const p = fitPage(800, 823);
  p.box.renderCredentials(liveView);
  p.paints = 0;
  p.box.renderCredentials(liveView);
  assert.strictEqual(p.needW(), 825);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0);
  assert.strictEqual(p.paints, 1, "cards straight away, no table to measure first");
});

test("content that overflows at every width costs one extra paint per call, never a loop", () => {
  const p = fitPage(900, (w) => w + 50);
  for (const w of [900, 1400, 5000, 701]) {
    p.byId["creds-body"].clientWidth = w;
    p.paints = 0;
    p.box.renderCredentials({ ...liveView }); // new data each time: a fresh try
    assert.strictEqual(p.paints, 2, "table, then cards, at " + w);
    assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "cards at " + w);
    assert.strictEqual(p.needW(), w + 52);
  }
  // The last paint learned 753. Dragging back and forth: one try at 900 (a table, then cards,
  // which raises the need to 952), and every later flip is decided by what it learned.
  p.paints = 0;
  for (const w of [900, 400, 900, 400, 900]) p.resizeTo(w);
  assert.strictEqual(p.paints, 2, "one table-then-cards try, then no repaint at all");
  assert.strictEqual(p.needW(), 952);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0);
});

test("a hidden tab (0 wide) is not measured and keeps tables, as before", () => {
  const p = fitPage(0, (w) => w + 50);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0);
  assert.strictEqual(p.needW(), 0, "an overflow read against a 0 wide body means nothing");
  assert.strictEqual(p.paints, 1);
});

test("a modal draws cards without measuring anything", () => {
  const p = fitPage(2000, (w) => w + 50, { asPage: false });
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0);
  assert.strictEqual(p.needW(), 0);
  assert.strictEqual(p.paints, 1);
});

// A window dragged narrower after a table was drawn: the body stays above the 700 floor,
// so the rule alone still says "tables", yet the content no longer fits. Measured in a
// browser (2026-11-06): opened at 1440 and dragged to 1225, the AWS section stuck out 23px
// with Delete 6px past it. The frame that follows a resize measures a table it keeps.

test("a live drag below what a drawn table needs (but above the floor) turns it into cards", () => {
  const p = fitPage(900, 725);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0 && p.needW() === 0, "fits at 900: a table, nothing learned");
  p.paints = 0;
  p.resizeTo(720);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "720 is above the floor but under the 725 the table needs");
  assert.strictEqual(p.needW(), 720 + 5 + 2);
  assert.strictEqual(p.paints, 1, "cards painted straight away, the table is not drawn again to be measured");
  p.resizeTo(726);
  assert.strictEqual(p.shape().tables, 0, "726 is under the learned 727");
  assert.strictEqual(p.paints, 1, "no repaint inside the band");
  p.resizeTo(727);
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "tables again once the body has the room");
  assert.strictEqual(p.paints, 2);
});

test("a live drag that still leaves the table room repaints nothing and learns nothing", () => {
  const p = fitPage(900, 725);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  const kept = body.children.slice();
  p.paints = 0;
  for (const w of [880, 800, 730, 725, 1000]) p.resizeTo(w);
  assert.strictEqual(p.paints, 0);
  assert.ok(sameNodes(body.children, kept), "the same nodes");
  assert.strictEqual(p.needW(), 0);
});

test("a table that no longer fits while a Delete confirmation is open is replaced after Cancel, not before", () => {
  const p = fitPage(900, 725);
  const body = p.byId["creds-body"];
  p.box.renderCredentials(liveView);
  deleteBtn(p).onclick();
  const kept = body.children.slice();
  p.paints = 0;
  p.resizeTo(720);
  assert.ok(sameNodes(body.children, kept), "the question is not wiped");
  assert.strictEqual(body.querySelectorAll(".confirm").length, 1);
  assert.strictEqual(p.paints, 0);
  cancelBtn(p).onclick();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "cards once the question is answered");
  assert.strictEqual(p.needW(), 727);
});

test("a hidden tab is not measured on a resize either", () => {
  const p = fitPage(900, 725);
  p.box.renderCredentials(liveView);
  p.paints = 0;
  p.resizeTo(0, 500); // the body reads 0 wide when the frame runs
  assert.strictEqual(p.paints, 0);
  assert.strictEqual(p.needW(), 0, "an overflow against a 0 wide body means nothing");
  assert.ok(p.shape().tables > 0);
});

test("a modal that is resized is never measured", () => {
  const p = fitPage(500, (w) => w + 50, { asPage: false });
  p.box.renderCredentials(liveView);
  p.paints = 0;
  p.resizeTo(2000);
  p.resizeTo(300);
  assert.strictEqual(p.paints, 0);
  assert.strictEqual(p.needW(), 0);
});

// A repaint held for the unlock field means the NEW data is waiting; credNeedW was
// already forgotten for it. A resize meanwhile must not measure the OLD table on screen
// and hand its need to the new view.
test("a resize while a repaint is held does not learn the old table's width for the new data", () => {
  const p = fitPage(900, 725);
  p.box.renderCredentials(liveView);
  p.box.storeUnlockFocused = () => true; // new data arrives while the unlock field has focus: held
  p.box.renderCredentials({ ...liveView });
  p.paints = 0;
  p.resizeTo(720); // the old table needs 725: it would overflow here
  assert.strictEqual(p.needW(), 0, "nothing learned from the table that is about to be replaced");
  assert.strictEqual(p.paints, 0, "and nothing painted while the field holds");
  p.need = () => 700; // the new data's table is narrower
  p.box.storeUnlockFocused = () => false;
  p.box.credFocusLeft(); // the hold ends: the new view is painted and measured
  assert.ok(p.shape().tables > 0 && p.shape().cards === 0, "the new data fits at 720: tables");
  assert.strictEqual(p.needW(), 0);
});

// font-display: swap (desktop.css): the first table may be drawn in the fallback font and
// Inter then widens it, which no resize reports. The fonts promise asks for one more look.
function fontsPage(width, need) {
  const p = fitPage(width, need);
  const thens = [];
  p.doc.fonts = { ready: { then: (f) => { thens.push(f); } } };
  p.fontsReady = () => thens.slice();
  return p;
}

test("the web font loading re-checks a table that was drawn before it (wired once)", () => {
  const p = fontsPage(750, 725);
  p.box.renderCredentials(liveView);
  p.box.renderCredentials(liveView);
  p.box.renderCredentials({ ...liveView });
  assert.strictEqual(p.fontsReady().length, 1, "asked once, however often it paints");
  assert.ok(p.shape().tables > 0, "fits in the fallback font");
  p.need = () => 760; // Inter is wider
  p.paints = 0;
  p.fontsReady()[0]();
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "the table no longer fits: cards");
  assert.strictEqual(p.needW(), 750 + 10 + 2);
  assert.strictEqual(p.paints, 1);
});

test("the web font loading repaints nothing when the table still fits", () => {
  const p = fontsPage(750, 725);
  p.box.renderCredentials(liveView);
  p.paints = 0;
  p.fontsReady()[0]();
  assert.strictEqual(p.paints, 0);
  assert.strictEqual(p.needW(), 0);
});

test("no document.fonts (an old browser, the fake DOM): painting still works", () => {
  const p = fitPage(750, 725);
  assert.strictEqual(p.doc.fonts, undefined);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0);
});

test("painted while hidden (0 wide, unmeasured), then shown narrower than the table needs: cards", () => {
  const p = fitPage(0, 725);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0 && p.needW() === 0, "hidden: tables, nothing learned");
  p.resizeTo(720);
  assert.ok(p.shape().tables === 0 && p.shape().cards > 0, "shown at 720: the table does not fit");
  assert.strictEqual(p.needW(), 727);
});

// --- the add flows in any host (spec 5.7) ---------------------------------------------------
//
// credTokenAdd, credAppForm, credGitHubChoose and awsOpenAdd take `opts = { host, foot, onCreated,
// onClose }`: they draw into opts.host and opts.foot, never touch the Credentials tab (its body, its
// footers, loadCredentials), and report what they created once, on the way out.

const vmEval = (p, expr) => require("node:vm").runInContext(expr, p.box);
const btnIn = (root, label) => root.querySelectorAll("button").find((b) => b.textContent === label);
const fieldIn = (root, id) => [...root.querySelectorAll("input"), ...root.querySelectorAll("textarea")].find((i) => i.id === id);

// A host and a foot the flow was not born with, and what it reports through them.
function hostOpts() {
  const events = [];
  const opts = {
    host: new Node("div"), foot: new Node("div"),
    onCreated: (names, kind) => events.push(["created", [...names], kind]),
    onClose: () => events.push(["close"]),
  };
  return { opts, events };
}

// What the Credentials tab shows for the footers: unchanged by a flow that is not in the tab.
const tabFooters = (p) => TAB_FOOT_IDS.map((id) => p.byId[id].hidden);

const storeOK = { initialized: true, locked: false, platform: "linux" };
const sentryTpl = { id: "sentry", provider: "sentry", label: "Sentry", group: "tokens", values: ["Auth token"], fields: [{ key: "region", label: "Region", kind: "select", options: [{ value: "us", label: "US" }, { value: "eu", label: "EU" }] }] };
const ghPatTpl = { id: "github-pat", provider: "github", label: "GitHub personal access token", group: "github", values: ["Personal access token"], fields: [] };

// The server as the token and App flows meet it.
function credReply({ store = storeOK, names = ["sentry-eu-prod"], templates = [sentryTpl] } = {}) {
  return (path) => {
    if (path === "/api/credentials") return { store, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] };
    if (path === "/api/credentials/templates") return { templates };
    if (path === "/api/credentials/tokens/from-template") return { names };
    return {};
  };
}

test("token add calls onCreated with the server's names", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply({ names: ["sentry-eu-prod", "sentry-eu-prod-2"] });
  const before = tabFooters(p);
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.deepStrictEqual(p.calls.map((c) => c.path), ["/api/credentials", "/api/credentials/templates"], "credView is primed first (the store state, the taken names), then the providers");
  assert.ok(opts.host.textContent.includes("Add token"), "the flow drew itself into opts.host");
  assert.strictEqual(p.byId["creds-body"].children.length, 0, "and left the Credentials tab's body alone");
  assert.deepStrictEqual(opts.foot.querySelectorAll("button").map((b) => b.textContent), ["Cancel", "Save"], "its buttons are in opts.foot");
  fieldIn(opts.host, "ct-sentry-name").value = "sentry-prod";
  fieldIn(opts.host, "ct-sentry-v0").value = "tok-123";
  btnIn(opts.foot, "Save").onclick();
  await settle();
  const post = p.calls.find((c) => c.method === "POST");
  assert.strictEqual(post.path, "/api/credentials/tokens/from-template");
  assert.strictEqual(post.body.name, "sentry-prod");
  assert.deepStrictEqual(events, [["created", ["sentry-eu-prod", "sentry-eu-prod-2"], "token"], ["close"]], "the server's names, not the typed one, then close");
  assert.strictEqual(p.box.reloaded, 0, "no loadCredentials from a flow outside the tab");
  assert.deepStrictEqual(tabFooters(p), before, "the tab footers were not touched");
  assert.strictEqual(p.box.credState().form, null, "the singleton is reset");
});

test("token add: Cancel closes without reporting, and the singleton is reset", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  btnIn(opts.foot, "Cancel").onclick();
  await settle();
  assert.deepStrictEqual(events, [["close"]]);
  assert.strictEqual(p.box.reloaded, 0);
  assert.strictEqual(p.box.credState().form, null);
  assert.deepStrictEqual(p.calls.filter((c) => c.method === "POST"), []);
});

test("token add: a refused save keeps the flow open and reports nothing", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  fieldIn(opts.host, "ct-sentry-name").value = "sentry-prod";
  fieldIn(opts.host, "ct-sentry-v0").value = "tok-123";
  p.box.apiFail = Object.assign(new Error("that name is reserved"), { status: 400 });
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.deepStrictEqual(events, []);
  assert.ok(opts.host.textContent.includes("that name is reserved"), "the refusal shows in the host");
  assert.strictEqual(btnIn(opts.foot, "Save").disabled, false, "and Save can be pressed again");
  assert.notStrictEqual(p.box.credState().form, null);
});

test("token add with no store: the host shows today's create-the-store step, and Save says why it cannot", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  let made = false;
  const store = () => ({ initialized: made, locked: false, platform: "darwin", passphrase_mode: false });
  const base = credReply();
  p.box.reply = (path) => {
    if (path === "/api/store/init") { made = true; return {}; }
    if (path === "/api/credentials") return { ...base(path), store: store() };
    return base(path);
  };
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.ok(opts.host.querySelector(".storecreate"), "the inline store step is in opts.host");
  assert.strictEqual(fieldIn(opts.host, "ct-sentry-v0"), undefined, "no value field before the store exists");
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.ok(opts.host.textContent.includes("Create the encrypted store first. The value is stored in it."));
  assert.deepStrictEqual(events, []);
  btnIn(opts.host, "Create the encrypted store").onclick();
  await settle();
  assert.ok(p.calls.some((c) => c.path === "/api/store/init" && c.method === "POST"));
  assert.ok(fieldIn(opts.host, "ct-sentry-v0"), "once it exists the value field takes its place");
  assert.strictEqual(opts.host.querySelector(".storecreate"), null);
});

test("token add with a locked store does not stop to unlock it", async () => {
  const p = page({ tab: true });
  const { opts } = hostOpts();
  p.box.reply = credReply({ store: { initialized: true, locked: true, platform: "linux" } });
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.ok(fieldIn(opts.host, "ct-sentry-v0"), "the value field is there while the store is locked");
  assert.strictEqual(opts.host.querySelector(".storecreate"), null);
});

test("token add: providers that cannot load are said in the host, not in the tab's error line", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = (path) => { if (path === "/api/credentials/templates") throw new Error("boom"); return credReply()(path); };
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.ok(opts.host.textContent.includes("Could not load the token providers"));
  assert.strictEqual(p.byId["creds-err"].textContent, "");
  assert.deepStrictEqual(events, []);
  assert.strictEqual(p.box.credState().form, null);
});

test("GitHub App add calls onCreated with the typed name", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  const before = tabFooters(p);
  await p.box.credAppForm(null, opts);
  assert.ok(p.calls.some((c) => c.path === "/api/credentials"), "credView is primed before the form paints");
  assert.ok(opts.host.textContent.includes("Add GitHub App"));
  assert.strictEqual(p.byId["creds-body"].children.length, 0);
  fieldIn(opts.host, "cga-name").value = "  my-bot ";
  fieldIn(opts.host, "cga-cid").value = "Iv1.abc";
  fieldIn(opts.host, "cga-key").value = "-----BEGIN PRIVATE KEY-----";
  btnIn(opts.foot, "Save").onclick();
  await settle();
  const post = p.calls.find((c) => c.method === "POST");
  assert.strictEqual(post.path, "/api/credentials/github-apps");
  assert.strictEqual(post.body.app.name, "my-bot");
  assert.deepStrictEqual(events, [["created", ["my-bot"], "app"], ["close"]], "the trimmed typed name");
  assert.strictEqual(p.box.reloaded, 0);
  assert.deepStrictEqual(tabFooters(p), before);
  assert.strictEqual(p.box.credState().form, null);
});

test("GitHub App add with no store shows the store step in the host", async () => {
  const p = page({ tab: true });
  const { opts } = hostOpts();
  p.box.reply = credReply({ store: { initialized: false, locked: false, platform: "darwin", passphrase_mode: false } });
  await p.box.credAppForm(null, opts);
  assert.ok(opts.host.querySelector(".storecreate"));
  assert.strictEqual(fieldIn(opts.host, "cga-key"), undefined);
});

test("GitHub choose passes opts to the route picked: a token reports the server's names, an App the typed name", async () => {
  for (const route of ["token", "app"]) {
    const p = page({ tab: true });
    const { opts, events } = hostOpts();
    p.box.reply = credReply({ names: ["gh-pat"], templates: [sentryTpl, ghPatTpl] });
    p.box.credGitHubChoose(opts);
    assert.ok(opts.host.textContent.includes("How should the agent reach GitHub?"), "the choice is drawn in opts.host");
    assert.strictEqual(btnIn(opts.foot, "Save").disabled, true, "nothing to save until a route is chosen");
    const tiles = opts.host.querySelectorAll("button.tile");
    assert.strictEqual(tiles.length, 2);
    const tile = tiles.find((t) => (route === "token" ? /token/i : /App/).test(t.textContent));
    assert.ok(tile, "a tile for the " + route + " route");
    tile.onclick();
    await settle();
    assert.strictEqual(btnIn(opts.foot, "Save").disabled, false, "the form's own Save is live again");
    if (route === "token") {
      fieldIn(opts.host, "ct-github-pat-name").value = "gh-pat";
      fieldIn(opts.host, "ct-github-pat-v0").value = "ghp_abc";
    } else {
      fieldIn(opts.host, "cga-name").value = "my-bot";
      fieldIn(opts.host, "cga-cid").value = "Iv1.abc";
      fieldIn(opts.host, "cga-key").value = "-----BEGIN PRIVATE KEY-----";
    }
    btnIn(opts.foot, "Save").onclick();
    await settle();
    assert.deepStrictEqual(events, [["created", route === "token" ? ["gh-pat"] : ["my-bot"], route], ["close"]], route);
    assert.strictEqual(p.box.reloaded, 0);
  }
});

// AWS: two host profiles that carry their own role, so a row needs only a Check.
const awsHostProfiles = [
  { name: "acme-dev", kind: "assume-role", role_arn: "arn:aws:iam::111111111111:role/agentbox-dev", source_profile: "base", region: "eu-west-1", selectable: true },
  { name: "acme-ops", kind: "assume-role", role_arn: "arn:aws:iam::222222222222:role/agentbox-ops", source_profile: "base", region: "eu-west-1", selectable: true },
];
function awsReply(post) {
  return (path, opt) => {
    if (path === "/api/credentials/aws/host-profiles") return { profiles: awsHostProfiles };
    if (path === "/api/credentials/aws/check") return { assume: "ok", verdict: "read_only" };
    if (path === "/api/credentials/aws" && post) return post(JSON.parse(opt.body).connection);
    return {};
  };
}
// Opens the add view in `opts` and ticks the named host profiles.
async function awsTick(p, opts, ...names) {
  p.box.awsOpenAdd(opts);
  await settle();
  const ticks = opts.host.querySelectorAll("input").filter((i) => i.type === "checkbox");
  awsHostProfiles.forEach((hp, i) => { if (names.includes(hp.name)) { ticks[i].checked = true; ticks[i].onchange(); } });
}
// ... and checks them all.
async function awsReady(p, opts, ...names) {
  await awsTick(p, opts, ...names);
  btnIn(opts.foot, "Check all").onclick();
  await settle();
}

test("AWS add in a custom host calls onCreated with every saved connection name", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = awsReply();
  const before = tabFooters(p);
  await awsReady(p, opts, "acme-dev", "acme-ops");
  assert.ok(opts.host.textContent.includes("Add AWS accounts"), "the flow drew itself into opts.host");
  assert.strictEqual(p.byId["creds-body"].children.length, 0, "and left the Credentials tab's body alone");
  assert.deepStrictEqual(opts.foot.querySelectorAll("button").map((b) => b.textContent), ["Check all", "Cancel", "Save all"], "its buttons are in opts.foot");
  assert.deepStrictEqual(tabFooters(p), before, "the tab footers are not shown for it");
  assert.strictEqual(btnIn(opts.foot, "Save all").disabled, false, "both rows passed the check");
  assert.ok(opts.foot.textContent.includes("2 of 2 ready to save."), "the footer note is in opts.foot");
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.deepStrictEqual(p.calls.filter((c) => c.path === "/api/credentials/aws").map((c) => c.body.connection.name), ["acme-dev", "acme-ops"]);
  assert.deepStrictEqual(events, [["created", ["acme-dev", "acme-ops"], "aws"], ["close"]], "every saved name, once, then close");
  assert.strictEqual(p.box.reloaded, 0, "no loadCredentials from a flow outside the tab");
  assert.deepStrictEqual(tabFooters(p), before);
  assert.strictEqual(vmEval(p, "awsFlow"), null, "the singleton is reset");
});

test("AWS add: Cancel or the crumb after a partial save reports what was saved, once", async () => {
  for (const how of ["Cancel", "crumb"]) {
    const p = page({ tab: true });
    const { opts, events } = hostOpts();
    p.box.reply = awsReply((conn) => { if (conn.name === "acme-ops") throw Object.assign(new Error("A connection with this name already exists."), { status: 409, code: "exists" }); return {}; });
    await awsReady(p, opts, "acme-dev", "acme-ops");
    btnIn(opts.foot, "Save all").onclick();
    await settle();
    assert.deepStrictEqual(events, [], how + ": still open while a row needs attention");
    assert.ok(opts.host.textContent.includes("Saved 1 connection. The rest need attention."));
    if (how === "Cancel") btnIn(opts.foot, "Cancel").onclick(); else opts.host.querySelector("a.lnk").onclick();
    await settle();
    assert.deepStrictEqual(events, [["created", ["acme-dev"], "aws"], ["close"]], how);
    assert.strictEqual(p.box.reloaded, 0);
    assert.strictEqual(vmEval(p, "awsFlow"), null);
  }
});

test("AWS add: Cancel with nothing saved only closes", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = awsReply();
  await awsReady(p, opts, "acme-dev");
  btnIn(opts.foot, "Cancel").onclick();
  await settle();
  assert.deepStrictEqual(events, [["close"]]);
  assert.deepStrictEqual(p.calls.filter((c) => c.path === "/api/credentials/aws"), []);
  assert.strictEqual(p.box.reloaded, 0);
  assert.strictEqual(vmEval(p, "awsFlow"), null);
});

test("AWS add: leaving while a save is in flight waits for it, so the name is not lost", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  let release;
  p.box.reply = awsReply(() => new Promise((r) => { release = () => r({}); }));
  await awsReady(p, opts, "acme-dev");
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.strictEqual(btnIn(opts.foot, "Cancel").disabled, true, "Cancel is off while it saves");
  opts.host.querySelector("a.lnk").onclick();
  await settle();
  assert.deepStrictEqual(events, [], "the crumb does not leave mid-save");
  release();
  await settle();
  assert.deepStrictEqual(events, [["created", ["acme-dev"], "aws"], ["close"]]);
});

test("token add in a host: leaving while the save is in flight waits for it, and the crumb and Cancel are held", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  let release;
  const base = credReply();
  p.box.reply = (path) => (path === "/api/credentials/tokens/from-template" ? new Promise((r) => { release = () => r({ names: ["sentry-prod"] }); }) : base(path));
  await p.box.credTokenAdd(null, "sentry", opts);
  fieldIn(opts.host, "ct-sentry-name").value = "sentry-prod";
  fieldIn(opts.host, "ct-sentry-v0").value = "tok-123";
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.strictEqual(btnIn(opts.foot, "Cancel").disabled, true, "Cancel is off while it saves");
  btnIn(opts.foot, "Cancel").onclick();
  opts.host.querySelector("a.lnk").onclick();
  await settle();
  assert.deepStrictEqual(events, [], "neither way out leaves mid-save");
  release();
  await settle();
  assert.deepStrictEqual(events, [["created", ["sentry-prod"], "token"], ["close"]]);
});

test("a host that goes away mid-save (credFlowDrop) hears nothing, and a newer flow is not closed by the late answer", async () => {
  const p = page({ tab: true });
  const first = hostOpts(), second = hostOpts();
  let release;
  const base = credReply();
  p.box.reply = (path) => (path === "/api/credentials/tokens/from-template" ? new Promise((r) => { release = () => r({ names: ["sentry-prod"] }); }) : base(path));
  await p.box.credTokenAdd(null, "sentry", first.opts);
  fieldIn(first.opts.host, "ct-sentry-name").value = "sentry-prod";
  fieldIn(first.opts.host, "ct-sentry-v0").value = "tok-123";
  btnIn(first.opts.foot, "Save").onclick();
  await settle();
  p.box.credFlowDrop();
  p.box.reply = base;
  await p.box.credAppForm(null, second.opts);
  release();
  await settle();
  assert.deepStrictEqual(first.events, []);
  assert.deepStrictEqual(second.events, [], "the second flow was not told, or closed");
  assert.notStrictEqual(p.box.credState().form, null, "the second flow is still open");
  assert.ok(second.opts.host.textContent.includes("Add GitHub App"));
});

test("a flow that was still waiting for the server when its host went away opens nothing", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  let release;
  p.box.reply = (path) => (path === "/api/credentials" ? new Promise((r) => { release = () => r({ store: storeOK }); }) : credReply()(path));
  const started = p.box.credTokenAdd(null, "sentry", opts);
  await settle();
  p.box.credFlowDrop();
  release();
  await started;
  assert.strictEqual(opts.host.children.length, 0, "nothing was drawn into the closed host");
  assert.strictEqual(p.box.credState().form, null);
  assert.deepStrictEqual(events, []);
});

test("loadCredentials re-shows the tab's Add / Edit footer for a view in the tab, and leaves a view in a host alone", async () => {
  const view = { store: storeOK, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] };
  const p = page({ tab: true });
  p.box.reply = (path) => (path === "/api/credentials" ? view : awsReply()(path));
  const { opts } = hostOpts();
  await awsReady(p, opts, "acme-dev");
  const before = tabFooters(p);
  await p.box.realLoadCredentials();
  assert.deepStrictEqual(tabFooters(p), before, "a drawer's view does not turn the tab's AWS footer on");
  p.box.credFlowDrop();
  p.box.awsOpenAdd();
  await settle();
  p.byId["foot-creds-add"].hidden = true; // selecting the tab hides every footer
  p.byId["foot-creds"].hidden = false;
  await p.box.realLoadCredentials();
  assert.strictEqual(p.byId["foot-creds-add"].hidden, false, "a view in the tab gets its footer back");
});

test("a flow in a host drops a tab flow left open, so the two never share one set of footers", async () => {
  const p = page({ tab: true });
  p.box.reply = awsReply();
  p.box.awsOpenAdd();
  await settle();
  assert.strictEqual(p.byId["foot-creds-add"].hidden, false, "the tab's AWS footer is up");
  const { opts } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.strictEqual(vmEval(p, "awsFlow"), null);
  // Its Add footer is gone. The list footer is not put on show: this ran on another tab (the Profiles page), and
  // selecting Credentials again shows it and reloads the list.
  assert.deepStrictEqual(tabFooters(p), [true, true, true]);
  assert.ok(opts.host.textContent.includes("Add token"), "and the flow went on in its host");
});

test("credFlowDrop forgets a flow in a host without reporting anything (the drawer closed itself)", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = awsReply();
  await awsReady(p, opts, "acme-dev");
  p.box.credFlowDrop();
  assert.strictEqual(vmEval(p, "awsFlow"), null);
  assert.deepStrictEqual(events, []);
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.deepStrictEqual(p.calls.filter((c) => c.path === "/api/credentials/aws"), [], "a stale button does nothing");
  p.box.reply = credReply();
  await p.box.credAppForm(null, opts);
  p.box.credFlowDrop();
  assert.strictEqual(p.box.credState().form, null);
  assert.deepStrictEqual(events, []);
});

test("a flow open in a host does not stop the Credentials tab painting its list", async () => {
  const p = livePage(900, { tab: true });
  const { opts } = hostOpts();
  p.box.reply = awsReply();
  await awsReady(p, opts, "acme-dev");
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0, "the tab painted its list beside the AWS view in the host");
  p.box.credFlowDrop();
  p.byId["creds-body"].replaceChildren();
  p.box.reply = credReply();
  await p.box.credAppForm(null, hostOpts().opts);
  p.box.renderCredentials(liveView);
  assert.ok(p.shape().tables > 0, "an App form in a host does not hold it either");
  // A view open in the tab still does (the existing rule).
  p.box.credFlowDrop();
  p.byId["creds-body"].replaceChildren();
  p.box.reply = credReply();
  await p.box.credAppForm(null);
  const form = p.byId["creds-body"].children.slice();
  p.box.renderCredentials(liveView);
  assert.deepStrictEqual(p.byId["creds-body"].children, form, "the tab's own form keeps the body");
});

// Without opts the flows are what they were: the tab's body, the tab's footers, loadCredentials on the way out.
test("without opts the Credentials tab behaves as before: AWS add", async () => {
  const p = page({ tab: true });
  p.box.reply = awsReply();
  p.box.awsOpenAdd();
  await settle();
  assert.ok(p.byId["creds-body"].textContent.includes("Add AWS accounts"));
  assert.deepStrictEqual(tabFooters(p), [true, false, true], "the AWS footer replaces the list footer");
  assert.strictEqual(p.byId["aws-save"].textContent, "Save all");
  // On the desktop layout (this page has no mobileActive, the phone's switch) a button that cannot act yet is
  // aria-disabled with its reason as the title, not disabled (disabled-reason.test.js has the rest, and the phone).
  assert.strictEqual(p.byId["aws-save"].getAttribute("aria-disabled"), "true");
  assert.strictEqual(p.byId["aws-save"].getAttribute("title"), "Nothing ready to save yet");
  assert.strictEqual(p.byId["aws-save"].disabled, false);
  assert.ok(p.byId["aws-foot-note"].textContent === "" || /ready to save/.test(p.byId["aws-foot-note"].textContent));
  p.byId["aws-cancel"].onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 1, "leaving reloads the list");
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
  assert.strictEqual(vmEval(p, "awsFlow"), null);
});

test("without opts the Credentials tab behaves as before: token add", async () => {
  const p = page({ tab: true });
  p.box.reply = credReply();
  vmEval(p, "credView = " + JSON.stringify({ store: storeOK, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] })); // what loadCredentials left behind
  await p.box.credTokenAdd(null, "sentry");
  assert.ok(p.byId["creds-body"].textContent.includes("Add token"));
  assert.deepStrictEqual(tabFooters(p), [true, true, false], "the form footer replaces the list footer");
  assert.strictEqual(p.byId["cred-form-save"].textContent, "Save");
  fieldIn(p.byId["creds-body"], "ct-sentry-name").value = "sentry-prod";
  fieldIn(p.byId["creds-body"], "ct-sentry-v0").value = "tok-123";
  p.byId["cred-form-save"].onclick();
  await settle();
  assert.strictEqual(p.calls.filter((c) => c.method === "POST").length, 1);
  assert.strictEqual(p.box.reloaded, 1, "a saved token reloads the list");
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
  assert.strictEqual(p.box.credState().form, null);
});

test("without opts the Credentials tab behaves as before: GitHub choose, then an App", async () => {
  const p = page({ tab: true });
  p.box.reply = credReply();
  vmEval(p, "credView = " + JSON.stringify({ store: storeOK, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] })); // what loadCredentials left behind
  p.box.credGitHubChoose();
  assert.ok(p.byId["creds-body"].textContent.includes("How should the agent reach GitHub?"));
  assert.strictEqual(p.byId["cred-form-save"].disabled, true);
  p.byId["creds-body"].querySelectorAll("button.tile").find((t) => /App/.test(t.textContent)).onclick();
  assert.strictEqual(p.byId["cred-form-save"].disabled, false);
  fieldIn(p.byId["creds-body"], "cga-name").value = "my-bot";
  fieldIn(p.byId["creds-body"], "cga-cid").value = "Iv1.abc";
  fieldIn(p.byId["creds-body"], "cga-key").value = "-----BEGIN PRIVATE KEY-----";
  p.byId["cred-form-save"].onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 1);
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
});

test("without opts the Cancel button and the crumb leave a token form for the list", async () => {
  const p = page({ tab: true });
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry");
  p.byId["cred-form-cancel"].onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 1);
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
});

// --- fix round 1: the tab stays as it was, host controls end their own flow only ----------------

// A token save that waits for the test to answer it: { p, opts, events, release, openForm() }.
async function pendingTokenSave(p, opts) {
  let release;
  const base = credReply();
  p.box.reply = (path) => (path === "/api/credentials/tokens/from-template" ? new Promise((r) => { release = () => r({ names: ["sentry-prod"] }); }) : base(path));
  await p.box.credTokenAdd(null, "sentry", opts);
  fieldIn(opts ? opts.host : p.byId["creds-body"], "ct-sentry-name").value = "sentry-prod";
  fieldIn(opts ? opts.host : p.byId["creds-body"], "ct-sentry-v0").value = "tok-123";
  return () => release();
}

test("tab: Cancel pressed while a token saves reloads, and the save finishing reloads again (the new item shows)", async () => {
  const p = page({ tab: true });
  vmEval(p, "credView = " + JSON.stringify({ store: storeOK, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] }));
  const release = await pendingTokenSave(p, null);
  p.byId["cred-form-save"].onclick();
  await settle();
  p.byId["cred-form-cancel"].onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 1, "Cancel left for the list");
  release();
  await settle();
  assert.strictEqual(p.box.reloaded, 2, "and the save finishing reloads the list once more, so it holds the new token");
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
});

test("a host's Cancel, Save and crumb left over after its flow was dropped do nothing", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  fieldIn(opts.host, "ct-sentry-name").value = "sentry-prod";
  fieldIn(opts.host, "ct-sentry-v0").value = "tok-123";
  const cancel = btnIn(opts.foot, "Cancel"), save = btnIn(opts.foot, "Save"), crumb = opts.host.querySelector("a.lnk");
  p.box.credFlowDrop();
  cancel.onclick(); save.onclick(); crumb.onclick();
  await settle();
  assert.strictEqual(p.box.reloaded, 0, "no tab exit, no loadCredentials");
  assert.deepStrictEqual(events, []);
  assert.deepStrictEqual(p.calls.filter((c) => c.method === "POST"), [], "and nothing is saved");
  assert.deepStrictEqual(tabFooters(p), [false, true, true]);
  // ... nor do they end a newer flow that is open by now
  const second = hostOpts();
  await p.box.credAppForm(null, second.opts);
  cancel.onclick(); save.onclick(); crumb.onclick();
  await settle();
  assert.deepStrictEqual(second.events, []);
  assert.notStrictEqual(p.box.credState().form, null, "the newer flow is still open");
  assert.strictEqual(p.box.reloaded, 0);
});

test("a host's Cancel left over after a normal exit does nothing more", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  const cancel = btnIn(opts.foot, "Cancel");
  cancel.onclick();
  await settle();
  cancel.onclick();
  await settle();
  assert.deepStrictEqual(events, [["close"]], "closed once");
  assert.strictEqual(p.box.reloaded, 0);
});

test("an AWS host's Cancel, Check all, Save all and crumb left over after a drop do nothing, to it or to a newer flow", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = awsReply();
  await awsReady(p, opts, "acme-dev");
  const old = ["Cancel", "Check all", "Save all"].map((l) => btnIn(opts.foot, l)).concat(opts.host.querySelector("a.lnk"));
  p.box.credFlowDrop();
  const before = p.calls.length;
  old.forEach((b) => b.onclick());
  await settle();
  assert.strictEqual(p.calls.length, before, "no request");
  assert.strictEqual(p.box.reloaded, 0);
  assert.deepStrictEqual(events, []);
  const second = hostOpts();
  await awsTick(p, second.opts, "acme-ops"); // ticked, not yet checked: a stray Check all would show
  const calls = p.calls.length;
  old.forEach((b) => b.onclick());
  await settle();
  assert.strictEqual(p.calls.length, calls, "the newer flow was not checked, saved or closed by them");
  assert.deepStrictEqual(second.events, []);
  assert.notStrictEqual(vmEval(p, "awsFlow"), null);
  assert.strictEqual(p.box.reloaded, 0);
});

test("the Cancel of a host's start error closes once, and a second click does nothing", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = (path) => { if (path === "/api/credentials/templates") throw new Error("boom"); return credReply()(path); };
  await p.box.credTokenAdd(null, "sentry", opts);
  const cancel = btnIn(opts.foot, "Cancel");
  cancel.onclick();
  cancel.onclick();
  assert.deepStrictEqual(events, [["close"]]);
});

test("onClose runs even when onCreated throws, and the error still surfaces (token and AWS)", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  opts.onCreated = () => { throw new Error("consumer broke"); };
  const release = await pendingTokenSave(p, opts);
  const run = btnIn(opts.foot, "Save").onclick();
  await settle();
  release();
  await assert.rejects(run, /consumer broke/);
  assert.deepStrictEqual(events, [["close"]], "closed all the same");
  assert.strictEqual(p.box.credState().form, null, "and the flow was reset");
  assert.strictEqual(p.box.reloaded, 0);

  const q = page({ tab: true });
  const a = hostOpts();
  a.opts.onCreated = () => { throw new Error("consumer broke"); };
  q.box.reply = awsReply();
  await awsReady(q, a.opts, "acme-dev");
  const saved = btnIn(a.opts.foot, "Save all").onclick();
  await assert.rejects(saved, /consumer broke/);
  assert.deepStrictEqual(a.events, [["close"]]);
  assert.strictEqual(vmEval(q, "awsFlow"), null);
});

test("a host flow whose credentials read failed says so, instead of offering to create a store that may exist", async () => {
  for (const start of ["token", "app"]) {
    const p = page({ tab: true });
    const { opts, events } = hostOpts();
    p.box.reply = (path) => { if (path === "/api/credentials") throw new Error("down"); return credReply()(path); };
    if (start === "token") await p.box.credTokenAdd(null, "sentry", opts); else await p.box.credAppForm(null, opts);
    assert.ok(opts.host.textContent.includes("Could not read the credentials"), start + ": the sentence is in the host");
    assert.strictEqual(opts.host.querySelector(".storecreate"), null, start + ": no create-the-store step");
    assert.strictEqual(p.box.credState().form, null, start + ": no form opened");
    assert.deepStrictEqual(btnLabels(opts.foot), ["Cancel"]);
    btnIn(opts.foot, "Cancel").onclick();
    assert.deepStrictEqual(events, [["close"]], start);
  }
});

const btnLabels = (root) => root.querySelectorAll("button").map((b) => b.textContent);

test("credRefreshView answers whether the read worked, and keeps the last view when it did not", async () => {
  const p = page();
  p.box.reply = credReply();
  assert.strictEqual(await p.box.credRefreshView(), true);
  const view = p.box.credState().view;
  assert.ok(view);
  p.box.apiFail = new Error("down");
  assert.strictEqual(await p.box.credRefreshView(), false);
  assert.strictEqual(p.box.credState().view, view);
});

test("credFlowBusy is true while a flow in a host saves, and only then", async () => {
  const p = page({ tab: true });
  assert.strictEqual(p.box.credFlowBusy(), false, "nothing open");
  // an AWS visit
  const { opts } = hostOpts();
  let release;
  p.box.reply = awsReply(() => new Promise((r) => { release = () => r({}); }));
  await awsReady(p, opts, "acme-dev");
  assert.strictEqual(p.box.credFlowBusy(), false, "open, not saving");
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.strictEqual(p.box.credFlowBusy(), true, "saving");
  release();
  await settle();
  assert.strictEqual(p.box.credFlowBusy(), false, "closed");
  // a form in a host
  const second = hostOpts();
  const answer = await pendingTokenSave(p, second.opts);
  assert.strictEqual(p.box.credFlowBusy(), false);
  btnIn(second.opts.foot, "Save").onclick();
  await settle();
  assert.strictEqual(p.box.credFlowBusy(), true);
  answer();
  await settle();
  assert.strictEqual(p.box.credFlowBusy(), false);
  // a form in the tab is not the host's business
  p.box.credFlowDrop();
  vmEval(p, "credView = " + JSON.stringify({ store: storeOK, aws: [], github_apps: [], tokens: [], values: [], legacy_secrets: [], invalid: [] }));
  const tabAnswer = await pendingTokenSave(p, null);
  p.byId["cred-form-save"].onclick();
  await settle();
  assert.strictEqual(p.box.credFlowBusy(), false, "a save in the tab does not count");
  tabAnswer();
  await settle();
});

// --- Task 15: what the drawer needs from the flows: a label for the primary button, and a way to end the open flow ---------

test("opts.saveLabel names the primary button of a flow in a host; without it the labels are as before", async () => {
  // AWS: the label survives every repaint of the footer (ticking a row repaints it).
  const p = page({ tab: true });
  const a = hostOpts();
  a.opts.saveLabel = "Save all and add";
  p.box.reply = awsReply();
  await awsTick(p, a.opts, "acme-dev");
  assert.deepStrictEqual(btnLabels(a.opts.foot), ["Check all", "Cancel", "Save all and add"]);
  btnIn(a.opts.foot, "Check all").onclick();
  await settle();
  assert.deepStrictEqual(btnLabels(a.opts.foot), ["Check all", "Cancel", "Save all and add"], "still, after a check");
  p.box.credFlowDrop();
  const plain = hostOpts();
  await awsTick(p, plain.opts, "acme-dev");
  assert.deepStrictEqual(btnLabels(plain.opts.foot), ["Check all", "Cancel", "Save all"], "no label given: the tab's own");
  p.box.credFlowDrop();
  // A form: the token form, the GitHub choice and the App form.
  const tokenHost = hostOpts();
  tokenHost.opts.saveLabel = "Save and add";
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", tokenHost.opts);
  assert.deepStrictEqual(btnLabels(tokenHost.opts.foot), ["Cancel", "Save and add"]);
  p.box.credFlowDrop();
  const gh = hostOpts();
  gh.opts.saveLabel = "Save and add";
  p.box.credGitHubChoose(gh.opts);
  assert.deepStrictEqual(btnLabels(gh.opts.foot), ["Cancel", "Save and add"], "the choice");
  gh.opts.host.querySelectorAll("button").find((b) => /GitHub App/.test(b.textContent)).onclick();
  await settle();
  assert.ok(gh.opts.host.textContent.includes("Add GitHub App"));
  assert.deepStrictEqual(btnLabels(gh.opts.foot), ["Cancel", "Save and add"], "the route it moved on to keeps the label");
  p.box.credFlowDrop();
  const none = hostOpts();
  await p.box.credTokenAdd(null, "sentry", none.opts);
  assert.deepStrictEqual(btnLabels(none.opts.foot), ["Cancel", "Save"], "no label given: today's");
});

test("credFlowCancel ends the open flow in a host the way its Cancel does: names saved so far are reported, then it closes", async () => {
  const p = page({ tab: true });
  assert.strictEqual(p.box.credFlowCancel({}), false, "nothing is open");
  const { opts, events } = hostOpts();
  p.box.reply = awsReply((conn) => { if (conn.name === "acme-ops") throw Object.assign(new Error("A connection with this name already exists."), { status: 409, code: "exists" }); return {}; });
  await awsReady(p, opts, "acme-dev", "acme-ops");
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.deepStrictEqual(events, [], "still open: one row needs attention");
  const other = hostOpts();
  assert.strictEqual(p.box.credFlowCancel(other.opts), false, "another host's flow is not ended by this one's close");
  assert.deepStrictEqual(events, []);
  await p.box.credFlowCancel(opts);
  assert.deepStrictEqual(events, [["created", ["acme-dev"], "aws"], ["close"]], "one visit, saved 1 of 2: reported, then closed");
  assert.strictEqual(vmEval(p, "awsFlow"), null);
  assert.strictEqual(p.box.reloaded, 0);
  assert.strictEqual(p.box.credFlowCancel(opts), false, "and nothing is left to end");
});

test("credFlowCancel on a form: closes without reporting, and the GitHub choice and its route are one flow", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.ok(p.box.credFlowCancel(opts));
  assert.deepStrictEqual(events, [["close"]]);
  assert.strictEqual(p.box.credState().form, null);
  const gh = hostOpts();
  p.box.credGitHubChoose(gh.opts);
  gh.opts.host.querySelectorAll("button").find((b) => /GitHub App/.test(b.textContent)).onclick();
  await settle();
  assert.ok(p.box.credFlowCancel(gh.opts));
  assert.deepStrictEqual(gh.events, [["close"]], "closed once, from the route it had moved on to");
  assert.strictEqual(p.box.credState().form, null);
});

test("credFlowCancel refuses while a flow in a host saves, so the name being saved is reported when the answer is in", async () => {
  const p = page({ tab: true });
  const { opts, events } = hostOpts();
  let release;
  p.box.reply = awsReply(() => new Promise((r) => { release = () => r({}); }));
  await awsReady(p, opts, "acme-dev");
  btnIn(opts.foot, "Save all").onclick();
  await settle();
  assert.strictEqual(p.box.credFlowCancel(opts), false, "busy: it did not end the flow");
  assert.deepStrictEqual(events, []);
  release();
  await settle();
  assert.deepStrictEqual(events, [["created", ["acme-dev"], "aws"], ["close"]], "the save ended it by itself, and reported");
});

// --- Task 15 gate fix (R67): a framed host (opts.head) draws the drawer's header band, form, and foot the way the mockup does ----

// A host with a header of its own as well: the crumb and the intro go in `head`, the form in `host`, and a refused Save is said in `foot`.
function framedOpts() {
  const events = [];
  const opts = {
    head: new Node("div"), host: new Node("div"), foot: new Node("div"),
    onCreated: (names, kind) => events.push(["created", [...names], kind]),
    onClose: () => events.push(["close"]),
    onIdle: () => events.push(["idle"]),
  };
  return { opts, events };
}
const TOKEN_INTRO = "The agent can read this token. It is long-lived and is not revoked when the session ends.";
const crumbParts = (head) => head.querySelector("div.crumb").children.map((c) => c.textContent);
const checkReply = (path, opt) => (path === "/api/credentials/aws/check" ? { assume: "ok", verdict: "none_found", actions: [] } : awsReply()(path, opt));

test("framed AWS add: the crumb and intro are the header, the form stays in the host, and the tick line says the rows join the profile", async () => {
  const p = page({ tab: true });
  const { opts } = framedOpts();
  p.box.reply = awsReply();
  p.box.awsOpenAdd(opts);
  await settle();
  assert.deepStrictEqual(crumbParts(opts.head), ["Credentials", " › AWS", "Add AWS accounts"]);
  assert.strictEqual(opts.head.querySelector("p.intro").textContent, "The agent never gets these profiles. It gets 15-minute credentials for the role you choose below.");
  assert.strictEqual(opts.host.querySelector("div.crumb"), null, "not in the body that scrolls");
  assert.strictEqual(opts.host.querySelector("p.intro"), null);
  const tick = opts.host.querySelectorAll("p.hint").find((h) => h.textContent.startsWith("Tick several"));
  assert.ok(tick, "the mockup's line");
  assert.strictEqual(tick.textContent, "Tick several to create several. Each one you save joins this profile as an unsaved row.");
  assert.strictEqual(tick.hidden, true, "with no row there is the flow's own 'Tick a profile above' line instead");
  const ticks = opts.host.querySelectorAll("input").filter((i) => i.type === "checkbox");
  ticks[0].checked = true; ticks[0].onchange();
  assert.strictEqual(tick.hidden, false);
  ticks[0].checked = false; ticks[0].onchange();
  assert.strictEqual(tick.hidden, true, "and it goes again with the last row");
});

test("framed AWS add: a passed Check is one line, 'Assume worked. No broad write access found'; the tab keeps its two", async () => {
  const framed = page({ tab: true });
  const f = framedOpts();
  framed.box.reply = checkReply;
  await awsReady(framed, f.opts, "acme-dev");
  const lines = f.opts.host.querySelectorAll("div.l").filter((l) => /Assume worked/.test(l.textContent));
  assert.strictEqual(lines.length, 1);
  assert.strictEqual(lines[0].textContent, "✓Assume worked. No broad write access found");
  assert.strictEqual(f.opts.host.querySelector("div.verdict"), null, "no second verdict line");
  // The Credentials tab, same answers: the flow's own two lines.
  const tab = page({ tab: true });
  tab.box.reply = checkReply;
  tab.box.awsOpenAdd();
  await settle();
  const body = tab.byId["creds-body"];
  const tticks = body.querySelectorAll("input").filter((i) => i.type === "checkbox");
  tticks[0].checked = true; tticks[0].onchange();
  btnIn(body, "Check").onclick();
  await settle();
  const tl = body.querySelectorAll("div.l").map((l) => l.textContent);
  assert.ok(tl.includes("✓Assume worked."), "the tab: " + JSON.stringify(tl));
  assert.ok(body.querySelector("div.verdict"), "and its verdict line");
});

test("framed AWS add: a row with an elevate role says the check covered the base role only; a row without one stays as it was", async () => {
  const framed = page({ tab: true });
  const f = framedOpts();
  framed.box.reply = checkReply;
  await awsTick(framed, f.opts, "acme-dev", "acme-ops");
  const elev = f.opts.host.querySelectorAll("input").filter((i) => i.placeholder === "arn:aws:iam::<account>:role/<write role>");
  assert.strictEqual(elev.length, 2, "one elevate role field per ticked row");
  elev[0].value = "arn:aws:iam::226198813800:role/agentbox-write";
  elev[0].oninput();
  btnIn(f.opts.foot, "Check all").onclick();
  await settle();
  const lines = () => f.opts.host.querySelectorAll("div.l").filter((l) => /Assume worked/.test(l.textContent)).map((l) => l.textContent);
  assert.deepStrictEqual(lines(), ["✓Assume worked. No broad write access found base role only", "✓Assume worked. No broad write access found"]);
  const when = f.opts.host.querySelectorAll("span.when");
  assert.deepStrictEqual(when.map((w) => w.textContent), [" base role only"], "set apart from the verdict the way the tab sets it");
  assert.strictEqual(f.opts.host.querySelector("div.verdict"), null, "still one line, no second verdict line");
  // Typing the role after the check, or taking it away, moves the qualifier with it.
  elev[1].value = "arn:aws:iam::226198813800:role/agentbox-write";
  elev[1].oninput();
  assert.deepStrictEqual(lines(), ["✓Assume worked. No broad write access found base role only", "✓Assume worked. No broad write access found base role only"]);
  elev[0].value = "";
  elev[0].oninput();
  assert.deepStrictEqual(lines(), ["✓Assume worked. No broad write access found", "✓Assume worked. No broad write access found base role only"]);
});

test("the Credentials tab's verdict line is the same with or without an elevate role in the row", async () => {
  const tab = page({ tab: true });
  tab.box.reply = checkReply;
  tab.box.awsOpenAdd();
  await settle();
  const body = tab.byId["creds-body"];
  const ticks = body.querySelectorAll("input").filter((i) => i.type === "checkbox");
  ticks[0].checked = true; ticks[0].onchange();
  const elev = body.querySelectorAll("input").find((i) => i.placeholder === "arn:aws:iam::<account>:role/<write role>");
  elev.value = "arn:aws:iam::226198813800:role/agentbox-write";
  elev.oninput();
  btnIn(body, "Check").onclick();
  await settle();
  assert.deepStrictEqual(body.querySelectorAll("div.l").filter((l) => /Assume worked/.test(l.textContent)).map((l) => l.textContent), ["✓Assume worked."]);
  assert.deepStrictEqual(body.querySelectorAll("div.verdict").map((v) => v.textContent), ["✓No broad write access found base role only"]);
});

test("framed token add: the crumb says Tokens, the intro is in the header, no trailing sentence, and a refused Save is said in the foot", async () => {
  const p = page({ tab: true });
  const { opts } = framedOpts();
  p.box.reply = credReply();
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.deepStrictEqual(crumbParts(opts.head), ["Credentials", " › Tokens", "Add token"]);
  assert.strictEqual(opts.head.querySelector("p.intro").textContent, TOKEN_INTRO);
  assert.strictEqual(opts.host.querySelector("p.honest"), null, "the sentence moved to the header");
  assert.strictEqual(opts.host.querySelector("div.crumb"), null);
  assert.strictEqual(opts.host.querySelector("div.ent-err"), null, "no error line under the form");
  const msg = opts.foot.querySelector("span.ent-err");
  assert.ok(msg, "the foot has the line, left of Cancel");
  assert.strictEqual(msg.attrs.role, "alert");
  assert.deepStrictEqual(opts.foot.children.map((c) => c.tag), ["span", "button", "button"]);
  assert.strictEqual(opts.foot.children[0], msg);
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.strictEqual(msg.textContent, "Paste the value to store.", "the refusal is in the foot");
  assert.strictEqual(opts.host.textContent.includes("Paste the value to store."), false);
});

// F11 (#171): one primary per view. A host with its own foot (the profile page's drawer) has Save
// there as the primary, so the store step's Create is a secondary in it; the tab (the phone's path
// too) keeps Create primary.
test("the store step's Create is a secondary beside a host's Save, the primary in the tab", async () => {
  const noStore = { initialized: false, locked: false, platform: "linux", passphrase_mode: true };
  const p = page({ tab: true });
  const { opts } = framedOpts();
  const base = credReply();
  p.box.reply = (path) => (path === "/api/credentials" ? { ...base(path), store: noStore } : base(path));
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.strictEqual(btnIn(opts.host, "Create the encrypted store").className, "btn", "token add in the drawer");
  assert.strictEqual(btnIn(opts.foot, "Save").className, "btn pri", "the foot's Save is the view's primary");
  const a = page({ tab: true });
  const app = hostOpts();
  a.box.reply = credReply({ store: { initialized: false, locked: false, platform: "darwin", passphrase_mode: false } });
  await a.box.credAppForm(null, app.opts);
  assert.strictEqual(btnIn(app.opts.host, "Create the encrypted store").className, "btn", "GitHub App add in a host");
  const q = page({ tab: true });
  q.box.reply = credReply({ store: noStore });
  await q.box.credTokenAdd(null, "sentry");
  assert.strictEqual(btnIn(q.byId["creds-body"], "Create the encrypted store").className, "btn pri", "the tab");
});

test("framed token add with no store: the card opens with the lead, and Save's refusal is the same sentence in the foot", async () => {
  const p = page({ tab: true });
  const { opts } = framedOpts();
  const base = credReply();
  p.box.reply = (path) => (path === "/api/credentials" ? { ...base(path), store: { initialized: false, locked: false, platform: "linux", passphrase_mode: true } } : base(path));
  await p.box.credTokenAdd(null, "sentry", opts);
  const card = opts.host.querySelector("div.storecreate");
  assert.ok(card);
  assert.strictEqual(card.children[0].tag, "p");
  assert.strictEqual(card.children[0].className, "lead");
  assert.strictEqual(card.children[0].textContent, "Create the encrypted store first. The value is stored in it.");
  assert.strictEqual(card.children[1].className, "hint", "then the hint");
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.strictEqual(opts.foot.querySelector("span.ent-err").textContent, "Create the encrypted store first. The value is stored in it.");
});

test("framed GitHub choice and App form: the crumb is the link and the title, the form is in the host, the refusal in the foot", async () => {
  const p = page({ tab: true });
  const { opts } = framedOpts();
  p.box.reply = credReply();
  p.box.credGitHubChoose(opts);
  assert.deepStrictEqual(crumbParts(opts.head), ["Credentials", "Add GitHub"], "the mockup draws no GitHub crumb, so there is no tail");
  assert.strictEqual(opts.head.querySelector("p.intro"), null);
  opts.host.querySelectorAll("button").find((b) => /GitHub App/.test(b.textContent)).onclick();
  await settle();
  assert.deepStrictEqual(crumbParts(opts.head), ["Credentials", "Add GitHub App"]);
  assert.strictEqual(opts.host.querySelector("div.crumb"), null);
  fieldIn(opts.host, "cga-name").value = "not a name!";
  fieldIn(opts.host, "cga-cid").value = "";
  fieldIn(opts.host, "cga-key").value = "";
  btnIn(opts.foot, "Save").onclick();
  await settle();
  assert.strictEqual(opts.foot.querySelector("span.ent-err").textContent, "The name may use letters, digits, . _ and -, up to 64 characters.");
});

test("framed: a start error empties the header, and the error is said in the host", async () => {
  const p = page({ tab: true });
  const { opts } = framedOpts();
  opts.head.append(new Node("div"));
  p.box.reply = (path) => { if (path === "/api/credentials/templates") throw new Error("boom"); return credReply()(path); };
  await p.box.credTokenAdd(null, "sentry", opts);
  assert.strictEqual(opts.head.children.length, 0);
  assert.ok(opts.host.textContent.includes("Could not load the token providers"));
});

test("onIdle: a save that answered and left the flow open says so; one that ended the flow only closes", async () => {
  // AWS: 1 of 2 saved, the other refused: the flow stays open and tells the host the save is over.
  const p = page({ tab: true });
  const { opts, events } = framedOpts();
  p.box.reply = awsReply((conn) => { if (conn.name === "acme-ops") throw Object.assign(new Error("A connection with this name already exists."), { status: 409, code: "exists" }); return {}; });
  await awsReady(p, opts, "acme-dev", "acme-ops");
  btnIn(opts.foot, "Save all").onclick();
  assert.deepStrictEqual(events, [], "not while it saves");
  await settle();
  assert.deepStrictEqual(events, [["idle"]], "once, when the answer is in");
  btnIn(opts.foot, "Cancel").onclick();
  await settle();
  assert.deepStrictEqual(events, [["idle"], ["created", ["acme-dev"], "aws"], ["close"]]);
  // AWS, everything saved: the flow closes, nothing to say.
  const q = page({ tab: true });
  const all = framedOpts();
  q.box.reply = awsReply();
  await awsReady(q, all.opts, "acme-dev");
  btnIn(all.opts.foot, "Save all").onclick();
  await settle();
  assert.deepStrictEqual(all.events, [["created", ["acme-dev"], "aws"], ["close"]]);
  // A form: a refused Save is idle, a made one closes.
  const r = page({ tab: true });
  const form = framedOpts();
  r.box.reply = credReply();
  await r.box.credTokenAdd(null, "sentry", form.opts);
  btnIn(form.opts.foot, "Save").onclick();
  await settle();
  assert.deepStrictEqual(form.events, [["idle"]], "the refusal for a missing value");
  fieldIn(form.opts.host, "ct-sentry-v0").value = "tok-1";
  btnIn(form.opts.foot, "Save").onclick();
  await settle();
  assert.deepStrictEqual(form.events, [["idle"], ["created", ["sentry-eu-prod"], "token"], ["close"]], "idle after the first refusal only; the made token closes");
});

test("the Credentials tab still renders the old DOM: ' / ' crumb, the error under the form, the sentence at the foot of the form, no lead, two check lines", async () => {
  const p = page({ tab: true });
  p.box.reply = credReply({ store: { initialized: false, locked: false, platform: "linux", passphrase_mode: true } });
  await p.box.credTokenAdd(null, "sentry");
  const body = p.byId["creds-body"];
  assert.deepStrictEqual(body.firstElementChild.children.map((c) => c.textContent), ["Credentials", " / ", "Add token"], "the crumb");
  assert.ok(body.querySelector("p.honest"), "the sentence is the trailing one");
  assert.strictEqual(body.querySelector("p.honest").textContent, TOKEN_INTRO);
  assert.strictEqual(body.lastChild.className, "ent-err", "the error line is the last thing in the body");
  assert.strictEqual(body.querySelector("p.intro"), null);
  const card = body.querySelector("div.storecreate");
  assert.ok(card);
  assert.strictEqual(card.querySelector("p.lead"), null, "no lead in the tab's card");
  assert.strictEqual(card.children[0].className, "hint");
  // AWS in the tab: the crumb and the intro are in the body, with the flow's own empty-state line only.
  const q = page({ tab: true });
  q.box.reply = awsReply();
  q.box.awsOpenAdd();
  await settle();
  const qb = q.byId["creds-body"];
  assert.deepStrictEqual(qb.firstElementChild.children.map((c) => c.textContent), ["Credentials", " / ", "Add AWS accounts"]);
  assert.strictEqual(qb.querySelectorAll("p.hint").some((h) => h.textContent.startsWith("Tick several")), false, "no tick line in the tab");
  // A host with no header (Task 14's) keeps its crumb in the host and the error under the form.
  const h = page({ tab: true });
  const { opts } = hostOpts();
  h.box.reply = credReply();
  await h.box.credTokenAdd(null, "sentry", opts);
  assert.deepStrictEqual(opts.host.firstElementChild.children.map((c) => c.textContent), ["Credentials", " / ", "Add token"]);
  assert.strictEqual(opts.host.lastChild.className, "ent-err");
  assert.ok(opts.host.querySelector("p.honest"));
});

// Fix wave (#171, VERIFY-A section 7): a host that does not fit its column broke inside a label
// ("artifactory.acme.example.co" / "m"). The mono sub-line puts a <wbr> after each dot, so where it
// must wrap it wraps after a dot; its text, what is read and copied, is unchanged.
test("a host sub-line breaks after a dot, never inside a label", () => {
  const p = page({ width: 900 });
  const table = p.box.credTable(group([tok("artifactory", { hosts: ["artifactory.acme.example.com", "cdn.example.io"] })]));
  const sub = table.querySelectorAll(".c-sub")[0];
  assert.ok(sub.classList.contains("mono"));
  assert.strictEqual(sub.textContent, "artifactory.acme.example.com, cdn.example.io");
  const kinds = sub.children.map((c) => (c instanceof Node ? c.tag : JSON.stringify(c.textContent)));
  assert.deepStrictEqual(kinds, ['"artifactory."', "wbr", '"acme."', "wbr", '"example."', "wbr", '"com, cdn."', "wbr", '"example."', "wbr", '"io"']);
});

// --- the Credentials page as frame M2.3 draws it (UI plan 4, #174, Task 5; digest-design §A6, §B4) ----------------------------
//
// One strip that says the store once, one header menu that adds, counts in chips, "Missing" a neutral chip, Stored values a
// chevron disclosure, ghost Edit and danger Delete. The page is table mode (desktop, room for tables); the phone's modal and a
// narrow desktop page are card mode and keep their markup.

const m23View = (over) => ({
  store: { initialized: true, locked: false, platform: "linux", expires_at: new Date(Date.now() + ((10 * 60 + 20) * 60 + 30) * 1000).toISOString() },
  aws: [aws("culipulse-prod", { prod: true, mode: "sso_direct" })],
  github_apps: [ghApp("test-app")],
  tokens: [tok("sentry-staging")],
  values: [{ id: "ANTHROPIC_KEY", updated: "2026-11-01T10:00:00Z", referenced_by: [] }, { id: "SENTRY_KEY", updated: "2026-11-02T10:00:00Z", referenced_by: ["static_tokens/sentry-staging"] }],
  legacy_secrets: [{ name: "ANTHROPIC_API_KEY", status: "missing" }, { name: "CLAUDE_CODE_OAUTH_TOKEN", status: "ok" }],
  invalid: [],
  ...over,
});
const paintPage = (view, opts) => {
  const p = page({ width: 900, ...opts });
  p.box.renderCredentials(view || m23View());
  return { p, body: p.byId["creds-body"] };
};
const sectionOf = (body, group) => body.querySelectorAll("section").find((s) => s.dataset.group === group);
const classesOf = (n) => n.className.split(/\s+/);

test("table mode: section headers carry a count chip and no add button", () => {
  const { body } = paintPage();
  const heads = body.querySelectorAll("div.sec-h");
  assert.deepStrictEqual(heads.map((h) => h.querySelector("h2").textContent), ["AWS", "GitHub Apps", "Sentry", "Legacy secrets"]);
  assert.deepStrictEqual(heads.map((h) => h.querySelector("span.chip").textContent), ["1", "1", "1", "2"]);
  for (const h of heads) {
    assert.ok(classesOf(h).includes("card-head"), "the kit's .card-head (frame: 12px gap, 12/16 padding, a hairline under it)");
    assert.ok(classesOf(h.querySelector("span.chip")).includes("tnum"), "the count is `chip tnum`");
    assert.strictEqual(h.querySelectorAll("button").length, 0, h.textContent + ": no button in the header");
    assert.strictEqual(h.querySelectorAll("span.count").length, 0, "the count is a chip, not a glued span");
  }
  const labels = body.querySelectorAll("button").map((b) => b.textContent);
  for (const l of ["Add AWS accounts", "Add GitHub", "Add token"]) assert.ok(!labels.includes(l), l + " is the header menu's now");
  assert.ok(classesOf(sectionOf(body, "aws")).includes("card"), "a section is the kit's .card");
});

test("table mode: the empty Tokens section keeps its line, a 0 chip and no add button", () => {
  const { body } = paintPage(m23View({ tokens: [] }));
  const sec = sectionOf(body, "tokens");
  assert.ok(sec, "the Tokens section is there so the empty state can say how to fill it");
  assert.strictEqual(sec.querySelector("div.sec-h").querySelector("span.chip").textContent, "0");
  assert.strictEqual(sec.querySelectorAll("button").length, 0);
  assert.match(sec.textContent, /No tokens yet\./);
});

test("card mode (phone) keeps its add buttons and count spans", () => {
  for (const [name, opts] of [["the phone's modal", { asPage: false, width: 900 }], ["a narrow desktop page", { width: 500 }]]) {
    const { body } = paintPage(m23View(), opts);
    const heads = body.querySelectorAll("div.sec-h");
    assert.deepStrictEqual(heads.map((h) => h.querySelector("span.grow").textContent), ["AWS 1", "GitHub Apps 1", "Sentry 1", "Stored values 2", "Legacy secrets 2"], name);
    assert.deepStrictEqual(heads.map((h) => h.querySelectorAll("button").map((b) => b.textContent)), [["Add AWS accounts"], ["Add GitHub"], ["Add token"], ["Set a value"], []], name);
    assert.strictEqual(body.querySelectorAll("span.chip").length, 0, name + ": no chip");
    assert.strictEqual(body.querySelectorAll("h2").length, 0, name + ": the header is the span it always was");
    assert.strictEqual(body.querySelectorAll("div.leg").length, 2, name + ": legacy secrets are rows, not a table");
  }
});

// A page head the way settingshead.js paints it: the trigger, a divider, then Esc ✕ (all in `.page-actions`).
function headWithTrigger(p) {
  const actions = new Node("div");
  actions.className = "page-actions";
  const btn = new Node("button");
  btn.id = "set-act-creds";
  btn.className = "btn";
  btn.setAttribute("aria-haspopup", "menu");
  btn.setAttribute("aria-expanded", "false");
  const sep = new Node("span");
  sep.className = "set-sep";
  actions.append(btn, sep);
  p.box.credAddMenuAttach(btn);
  const wrap = btn.parent;
  return { actions, btn, sep, wrap, pop: wrap.querySelector("div.menu") };
}
const menuRows = (pop) => pop.querySelectorAll("button");
// kitMenuKeys asks for `.menu-item:not([aria-disabled="true"]), .mi`, which the fake DOM's selectors cannot read.
function teachMenuKeys(pop) {
  const plain = pop.querySelectorAll.bind(pop);
  pop.querySelectorAll = (sel) => (sel.startsWith(".menu-item") ? plain("button").filter((n) => n.getAttribute("aria-disabled") !== "true") : plain(sel));
}
const keyEv = (key) => { const ev = { key, defaultPrevented: false, preventDefault() { ev.defaultPrevented = true; } }; return ev; };

test("the header menu offers today's three adds", () => {
  const p = page();
  assert.deepStrictEqual(Array.from(p.box.credAddItems(), (i) => [i.label, i.kind]), [["Add AWS accounts", "aws"], ["Add GitHub", "github"], ["Add token", "token"]]);
  assert.strictEqual(typeof p.box.credAddMenuAttach, "function", "settingshead.js finds it on the global object");
  const { actions, sep, wrap, pop } = headWithTrigger(p);
  assert.strictEqual(wrap.className, "cred-add");
  assert.deepStrictEqual(actions.children, [wrap, sep], "the wrapper takes the trigger's place in the head");
  assert.strictEqual(pop.getAttribute("role"), "menu");
  assert.strictEqual(pop.hidden, true, "closed until pressed");
  const rows = menuRows(pop);
  assert.deepStrictEqual(rows.map((r) => r.querySelector("span.menu-text").textContent), ["Add AWS accounts", "Add GitHub", "Add token"]);
  for (const r of rows) {
    assert.ok(classesOf(r).includes("menu-item"), "a kitMenuButton row");
    assert.strictEqual(r.getAttribute("role"), "menuitem");
    assert.ok(r.querySelector("span.menu-icon"), "with the kit's icon slot");
  }
});

test("the menu opens on the trigger, walks with the arrows, closes on Esc and click-away, and each row runs its add with its group", () => {
  const p = page();
  const seen = [];
  const handlers = vmEval(p, "credActionHandlers");
  for (const kind of ["aws", "github", "token"]) handlers[kind] = { add: (g, reload) => seen.push([kind, g.key, reload === p.box.loadCredentials]) };
  p.box.renderCredentials(m23View({ tokens: [tok("sentry-staging"), tok("dd", { provider: "datadog" })] }));
  const { btn, wrap, pop } = headWithTrigger(p);
  teachMenuKeys(pop);
  const rows = menuRows(pop);

  btn.onclick();
  assert.strictEqual(pop.hidden, false);
  assert.strictEqual(btn.getAttribute("aria-expanded"), "true");
  assert.strictEqual(p.doc.activeElement, rows[0], "opening puts focus on the first row");
  const down = keyEv("ArrowDown");
  wrap.fire("keydown", down);
  assert.strictEqual(p.doc.activeElement, rows[1], "ArrowDown walks the rows");
  assert.strictEqual(down.defaultPrevented, true);
  const esc = keyEv("Escape");
  wrap.fire("keydown", esc);
  assert.strictEqual(pop.hidden, true, "Esc closes");
  assert.strictEqual(btn.getAttribute("aria-expanded"), "false");
  assert.strictEqual(p.doc.activeElement, btn, "and gives focus back to the trigger");
  assert.strictEqual(esc.defaultPrevented, true, "so the page itself does not close on that Esc");
  wrap.fire("keydown", keyEv("Escape"));
  assert.strictEqual(p.doc.activeElement, btn, "Esc on a closed menu does nothing");

  btn.onclick();
  btn.onclick();
  assert.strictEqual(pop.hidden, true, "a second press closes");
  btn.onclick();
  p.listeners.click[0]({ target: rows[2] });
  assert.strictEqual(pop.hidden, false, "a click inside the menu is not a click away");
  p.listeners.click[0]({ target: new Node("div") });
  assert.strictEqual(pop.hidden, true, "a click elsewhere closes");

  btn.onclick();
  rows[0].onclick();
  assert.strictEqual(pop.hidden, true, "a row closes the menu first");
  assert.strictEqual(p.doc.activeElement, btn);
  rows[1].onclick();
  rows[2].onclick();
  assert.deepStrictEqual(seen, [["aws", "aws", true], ["github", "github", true], ["token", "token:datadog", true]], "each add gets its group and the reload; the token one is the first token group");
});

test("the token add starts from { key: \"token:\" } when there is no token group yet", () => {
  const p = page();
  const seen = [];
  vmEval(p, "credActionHandlers").token = { add: (g) => seen.push(g.key) };
  p.box.renderCredentials(m23View({ tokens: [] }));
  const { pop } = headWithTrigger(p);
  menuRows(pop)[2].onclick();
  assert.deepStrictEqual(seen, ["token:"]);
});

test("attaching again never doubles the menu or its listeners; a repaint's new trigger drops the old open menu", () => {
  const p = page();
  const first = headWithTrigger(p);
  p.box.credAddMenuAttach(first.btn);
  assert.strictEqual(first.actions.querySelectorAll("div.menu").length, 1, "the same trigger gets one menu");
  assert.strictEqual(first.actions.querySelectorAll("div.cred-add").length, 1);
  first.btn.onclick();
  assert.strictEqual(first.pop.hidden, false);
  const second = headWithTrigger(p);
  assert.strictEqual(second.actions.querySelectorAll("div.menu").length, 1);
  assert.strictEqual(first.pop.hidden, true, "a menu the last paint left open is closed with its button");
  assert.strictEqual(p.listeners.click.length, 1, "one click-away listener on the document, however often the head is painted");
  second.btn.onclick();
  assert.strictEqual(second.pop.hidden, false, "the new menu works");
});

test("an Add / Edit view open in the tab hides the trigger; closing it brings it back", () => {
  const p = page();
  const { btn, wrap, sep, pop } = headWithTrigger(p);
  btn.onclick();
  vmEval(p, "awsFlow = { mode: 'add', opts: null }");
  p.box.credAddMenuSync();
  assert.strictEqual(wrap.hidden, true, "no second add on top of the open one");
  assert.strictEqual(sep.hidden, true, "and no divider left with nothing before it");
  assert.strictEqual(pop.hidden, true, "an open menu is closed with it");
  vmEval(p, "awsFlow = null");
  p.box.credAddMenuSync();
  assert.strictEqual(wrap.hidden, false);
  assert.strictEqual(sep.hidden, false);
});

test("the strip says it once on the page: dot, Encrypted store, muted unlocked · locks in …, Lock now, then the honest note once", () => {
  const p = page();
  const wrap = stripOf(p, m23View().store);
  const strip = wrap.querySelector(".cstrip");
  assert.deepStrictEqual(strip.children.map((c) => c.tag), ["span", "b", "span", "button"]);
  const [dot, name, when, lock] = strip.children;
  assert.deepStrictEqual(classesOf(dot).slice(0, 2), ["dot", "dot-run"], "the kit's green dot");
  assert.strictEqual(dot.getAttribute("aria-hidden"), "true");
  assert.strictEqual(name.textContent, "Encrypted store");
  assert.ok(classesOf(when).includes("muted"));
  assert.strictEqual(when.textContent, p.box.storeExpiryText(m23View().store, Date.now()), "the words are storeExpiryText's");
  assert.match(when.textContent, /^unlocked · locks in 10 h 2\d m$/);
  assert.strictEqual(lock.textContent, "Lock now");
  assert.strictEqual(lock.className, "btn btn-sm");
  assert.strictEqual(lock.id, "store-lock");
  assert.ok(!wrap.textContent.includes("Values you add here are encrypted at rest."), "the sentence the strip used to add is not drawn");
  assert.ok(!strip.textContent.includes("Unlocked"), "no chip repeating what the muted line says");
  const honest = vmEval(p, "STORE_HONEST_STATEMENT");
  assert.match(honest, /^Values are encrypted at rest, against disk snapshots and backups\./);
  const note = wrap.querySelectorAll("p").filter((n) => n.textContent === honest);
  assert.strictEqual(note.length, 1, "the honest statement follows once");
  assert.strictEqual(wrap.textContent.split(honest).length - 1, 1);
  assert.ok(classesOf(wrap).includes("cstrip-wrap"));
});

test("the phone's strip keeps its chip, its sentence and its plain Lock now", () => {
  const p = page({ asPage: false });
  const wrap = stripOf(p, m23View().store);
  const strip = wrap.querySelector(".strip");
  assert.strictEqual(strip.querySelector(".st").textContent, "Unlocked");
  assert.match(strip.textContent, /Encrypted store: unlocked · locks in 10 h 2\d m\. Values you add here are encrypted at rest\./);
  assert.strictEqual(strip.querySelector("button").className, "btn");
});

test("the strip's states that need the user keep their words and stay marked `need` (amber) on the page", () => {
  const p = page();
  const locked = stripOf(p, { initialized: true, locked: true, platform: "linux" }).querySelector(".cstrip");
  assert.ok(classesOf(locked).includes("need"));
  assert.strictEqual(locked.querySelector(".st").textContent, "Locked");
  assert.match(locked.textContent, /Encrypted store: locked\. You can add and replace values while it is locked\. Unlock it so new sessions can use them\./);
  const none = stripOf(p, { initialized: false, locked: false, platform: "linux" }).querySelector(".cstrip");
  assert.ok(classesOf(none).includes("need"));
  assert.strictEqual(none.querySelector(".st").textContent, "Not set up");
  assert.match(none.textContent, /There is no encrypted store yet\./);
  const bad = stripOf(p, kcStore({ unavailable: unavailable("key_lost") })).querySelector(".cstrip");
  assert.ok(classesOf(bad).includes("need"));
  assert.strictEqual(bad.querySelector(".st").textContent, "Keychain");
});

test("Missing is a neutral chip and Ready is green text, in the tables and in Legacy secrets", () => {
  const { body } = paintPage(m23View({ tokens: [tok("fine"), tok("gone", { status: "missing" })] }));
  const rows = sectionOf(body, "token:sentry").querySelectorAll("tbody");
  const status = (tb) => tb.rows[0].cells[2].querySelector("span");
  assert.strictEqual(status(rows[0]).textContent, "Ready");
  assert.deepStrictEqual(classesOf(status(rows[0])), ["st", "ready"], "Ready is the green text (.st.ready), no chip");
  assert.strictEqual(status(rows[1]).textContent, "Missing");
  assert.deepStrictEqual(classesOf(status(rows[1])), ["chip"], "Missing is a plain neutral chip: not .st (amber), not red");
  const legacy = sectionOf(body, "legacy").querySelectorAll("tr");
  assert.deepStrictEqual(legacy.map((r) => r.cells[1].querySelector("span").textContent), ["Missing", "Ready"]);
  assert.deepStrictEqual(classesOf(legacy[0].cells[1].querySelector("span")), ["chip"]);
  assert.deepStrictEqual(classesOf(legacy[1].cells[1].querySelector("span")), ["st", "ready"]);
});

// The stylesheet half: read off desktop.css like settings-shell.test.js does (a browser draws it, a test cannot).
const stripCss = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const DESK = stripCss(fs.readFileSync(path.join(STATIC, "desktop.css"), "utf8"));
function parseCss(src) {
  const rules = [];
  const stack = [];
  let buf = "", paren = 0, quote = null;
  const flush = () => {
    const d = buf.trim();
    buf = "";
    if (!d || !stack.length) return;
    const i = d.indexOf(":");
    if (i > 0) stack[stack.length - 1].decls.set(d.slice(0, i).trim().toLowerCase(), d.slice(i + 1).replace(/\s+/g, " ").trim());
  };
  for (const ch of src) {
    if (quote) { buf += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; buf += ch; continue; }
    if (ch === "(") paren++;
    else if (ch === ")") paren--;
    if (paren === 0 && ch === "{") { stack.push({ selector: buf.trim().replace(/\s+/g, " "), decls: new Map() }); buf = ""; }
    else if (paren === 0 && ch === ";") flush();
    else if (paren === 0 && ch === "}") { flush(); const done = stack.pop(); if (done && !done.selector.startsWith("@")) rules.push(done); }
    else buf += ch;
  }
  return rules;
}
function splitSel(list) {
  const out = [];
  let depth = 0, cur = "";
  for (const ch of list) {
    if (ch === "(" || ch === "[") depth++;
    else if (ch === ")" || ch === "]") depth--;
    if (ch === "," && depth === 0) { out.push(cur.trim()); cur = ""; } else cur += ch;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}
const CSS_RULES = parseCss(DESK);
const cssDecls = (sel) => {
  const out = new Map();
  for (const r of CSS_RULES) if (splitSel(r.selector).includes(sel)) for (const [k, v] of r.decls) out.set(k, v);
  return out;
};
const PG = "#page-host > #settings-modal.as-page";
const CR = PG + " :is(#creds, #cred-drawer)";
const rulesNaming = (re) => CSS_RULES.filter((r) => splitSel(r.selector).some((s) => re.test(s)));
const asText = (r) => [...r.decls].map(([k, v]) => k + ": " + v).join("; ");

test("CSS: Missing and not-set are not amber; locked and the needy strip are; Ready is green; a chip is neutral", () => {
  const missing = rulesNaming(/\.st\.(missing|notset)(?![\w-])/).filter((r) => !splitSel(r.selector).every((s) => /\.cstrip/.test(s)));
  assert.ok(missing.length, "a rule says what a missing status looks like on the page");
  for (const r of missing) assert.doesNotMatch(asText(r), /--amber|--warn/, r.selector);
  assert.strictEqual(cssDecls(CR + " .st.missing").get("color"), "var(--muted)");
  assert.strictEqual(cssDecls(CR + " .st.locked").get("color"), "var(--amber)", "a locked store blocks spawns: it needs you");
  assert.strictEqual(cssDecls(CR + " .cstrip .st.notset").get("color"), "var(--amber)", "'Not set up' (no store at all) needs you too");
  const need = cssDecls(CR + " .cstrip.need");
  assert.match(need.get("border"), /var\(--warn\)/);
  assert.strictEqual(need.get("background"), "var(--warn-bg)");
  assert.strictEqual(cssDecls(CR + " .cstrip.need .txt b").get("color"), "var(--amber)");
  assert.strictEqual(cssDecls(CR + " .st.ready").get("color"), "var(--ok)");
  const chip = cssDecls(".chip");
  assert.strictEqual(chip.get("background"), "var(--raise)");
  assert.strictEqual(chip.get("color"), "var(--muted)");
  for (const r of rulesNaming(/#creds[^ ]* .*\.chip(?![\w-])/)) assert.doesNotMatch(asText(r), /--amber|--warn|--red/, "nothing on the page recolours the neutral chip: " + r.selector);
});

test("Stored values is a chevron disclosure: a ghost button with aria-expanded, the chevron, the name, a count chip and the muted line", () => {
  const { p, body } = paintPage();
  const fold = sectionOf(body, "values");
  assert.strictEqual(fold.querySelectorAll("details").length, 0, "no native details");
  assert.strictEqual(fold.querySelectorAll("summary").length, 0);
  const row = fold.querySelector("div.vfold-h");
  const toggle = row.querySelector("button");
  assert.deepStrictEqual(classesOf(toggle).slice(0, 3), ["btn", "btn-ghost", "btn-sm"]);
  assert.strictEqual(toggle.getAttribute("aria-expanded"), "false", "drawn closed");
  const chev = toggle.querySelector("svg");
  assert.ok(chev, "the chevron is an inline svg");
  assert.strictEqual(chev.getAttribute("aria-hidden"), "true");
  assert.strictEqual(toggle.textContent, "Stored values", "the name is the button's text");
  assert.strictEqual(row.querySelector("span.chip").textContent, "2");
  assert.ok(classesOf(row.querySelector("span.chip")).includes("tnum"));
  const note = row.querySelectorAll("span").find((s) => classesOf(s).includes("muted"));
  assert.strictEqual(note.textContent, "Write-only: Replace or Delete");
  const panel = fold.querySelector("div.vfold-b");
  assert.strictEqual(panel.hidden, true, "closed hides the body");
  toggle.onclick();
  assert.strictEqual(toggle.getAttribute("aria-expanded"), "true");
  assert.strictEqual(panel.hidden, false);
  const set = btnIn(panel, "Set a value");
  assert.strictEqual(set.className, "link link-add", "Set a value is the accent + link inside the block");
  assert.deepStrictEqual(panel.querySelectorAll("button").map((b) => b.textContent), ["Set a value", "Replace", "Delete", "Replace", "Delete"]);
  // A repaint (Replace and Delete both reload) keeps it open.
  p.box.renderCredentials(m23View());
  const again = sectionOf(p.byId["creds-body"], "values");
  assert.strictEqual(again.querySelector("button").getAttribute("aria-expanded"), "true");
  assert.strictEqual(again.querySelector("div.vfold-b").hidden, false);
  // Closing it again is remembered the same way.
  again.querySelector("button").onclick();
  p.box.renderCredentials(m23View());
  assert.strictEqual(sectionOf(p.byId["creds-body"], "values").querySelector("div.vfold-b").hidden, true);
});

test("CSS: the Stored values disclosure draws no ▸/▾ and sizes and turns its svg chevron", () => {
  assert.deepStrictEqual(CSS_RULES.filter((r) => /#creds|#cred-drawer/.test(r.selector) && /[▸▾]/.test(asText(r))).map((r) => r.selector), [], "no triangle glyph on the Credentials page");
  assert.deepStrictEqual(rulesNaming(/vfold/).filter((r) => r.decls.has("content")).map((r) => r.selector), [], "no pseudo-content on the disclosure");
  const svg = cssDecls(CR + " .vfold-t svg");
  assert.strictEqual(svg.get("width"), "12px", "§B4: a 12px chevron");
  assert.strictEqual(svg.get("height"), "12px");
  assert.strictEqual(cssDecls(CR + ' .vfold-t[aria-expanded="true"] svg').get("transform"), "rotate(90deg)");
  assert.strictEqual(cssDecls(CR + " .vfold-b[hidden]").get("display"), "none", "the body's own display would otherwise show through [hidden]");
});

test("row actions: ghost Edit, danger Delete, each named for its row", () => {
  const p = page();
  const { tb } = tokenTable(p);
  const [edit, del] = tb.rows[0].querySelectorAll("button");
  assert.deepStrictEqual([edit.textContent, edit.className], ["Edit", "btn btn-ghost btn-sm"]);
  assert.deepStrictEqual([del.textContent, del.className], ["Delete", "btn btn-danger btn-sm"]);
  assert.strictEqual(edit.getAttribute("aria-label"), "Edit sentry-prod");
  assert.strictEqual(del.getAttribute("aria-label"), "Delete sentry-prod");
  // The cards (the phone) keep today's buttons.
  const card = p.box.credTokenCard(tok("sentry-prod"));
  assert.deepStrictEqual(card.querySelectorAll("button").map((b) => [b.textContent, b.className]), [["Edit", "btn"], ["Delete", "btn dgr"]]);
});

test("CSS: the kit's ghost, danger and small buttons get their values back from the page's `.btn` rules", () => {
  const legacy = cssDecls(CR + " .btn");
  assert.strictEqual(legacy.get("padding"), "5px 11px", "the rule being undone is still there (credentials.test.js, settings-shell.test.js pin it)");
  const sm = cssDecls(CR + " .btn.btn-sm");
  assert.strictEqual(sm.get("padding"), "0 8px");
  assert.strictEqual(sm.get("border-radius"), "var(--r-chip)");
  assert.strictEqual(sm.get("font-size"), "var(--t-sm)");
  const ghost = cssDecls(CR + " .btn.btn-ghost:where(:not(.pri))");
  assert.strictEqual(ghost.get("background"), "transparent");
  assert.strictEqual(ghost.get("color"), "var(--muted)");
  assert.strictEqual(ghost.get("border-color"), "transparent");
  // The hovers skip a disabled button (a Delete that is still referenced must not light up).
  const ghostHover = cssDecls(CR + " .btn.btn-ghost:where(:not(.pri)):where(:not(:disabled)):hover");
  assert.strictEqual(ghostHover.get("background"), "var(--hover)");
  assert.strictEqual(ghostHover.get("color"), "var(--fg)");
  const danger = cssDecls(CR + " .btn.btn-danger:where(:not(.pri))");
  assert.strictEqual(danger.get("background"), "transparent");
  assert.strictEqual(danger.get("color"), "var(--red)");
  assert.strictEqual(danger.get("border-color"), "rgba(240,106,106,.45)", "§B4: the Delete ring");
  const dangerHover = cssDecls(CR + " .btn.btn-danger:where(:not(.pri)):where(:not(:disabled)):hover");
  assert.strictEqual(dangerHover.get("background"), "var(--red-tint)");
  assert.strictEqual(dangerHover.get("border-color"), "var(--red)");
  const at = (sel) => DESK.indexOf(sel + " {");
  assert.ok(at(CR + " .btn.btn-danger:where(:not(.pri))") > at(CR + " .btn.dgr:hover"), "after the page's own button rules, or they would win");
  assert.ok(at(CR + " .btn.btn-ghost:where(:not(.pri))") > at(CR + " .btn:where(:not(.pri))"));
});

test("legacy ENV names are mono, in a two-column table with no header row", () => {
  const { body } = paintPage();
  const sec = sectionOf(body, "legacy");
  const table = sec.querySelector("table");
  assert.ok(table, "a table in table mode");
  assert.strictEqual(table.querySelectorAll("th").length, 0, "no header row (§A6)");
  assert.strictEqual(table.querySelector("colgroup").children.length, 2);
  const names = table.querySelectorAll("tr").map((r) => r.cells[0]);
  assert.deepStrictEqual(names.map((c) => c.textContent), ["ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"]);
  for (const c of names) assert.ok(classesOf(c.querySelector("span")).includes("mono"), c.textContent + " is mono");
  const note = sec.querySelector("p.ctab-note-top");
  assert.strictEqual(note.textContent, "Defined in config.json. Edit the file to change them, or move the value into the store.");
  assert.ok(sec.children.indexOf(note) < sec.children.indexOf(table), "the note sits above the table");
  // The phone's rows are as they were: the name is a <b>.
  const phone = paintPage(m23View(), { asPage: false }).body;
  assert.deepStrictEqual(phone.querySelectorAll("div.leg").map((r) => r.querySelector("b").textContent), ["ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"]);
});

test("names are mono, 'nothing' is muted, PROD and the seed mark are chips (§A6 rows)", () => {
  const { body } = paintPage(m23View({
    aws: [aws("culipulse-prod", { prod: true, mode: "sso_direct" }), aws("from-file", { source: "config" })],
    github_apps: [ghApp("test-app")],
  }));
  const awsRows = sectionOf(body, "aws").querySelectorAll("tbody");
  const first = awsRows[0].rows[0].cells[0];
  assert.ok(classesOf(first.querySelector("div.c-main")).includes("mono"), "the connection name is mono");
  assert.deepStrictEqual(first.querySelectorAll("span").map((s) => [s.textContent, s.className]).filter((x) => x[1].includes("chip")), [["PROD", "chip chip-prod"]]);
  assert.strictEqual(first.querySelector("div.c-sub").textContent, "no elevation", "the sub-line is plain muted, not mono");
  assert.ok(awsRows[1].rows[0].cells[0].querySelectorAll("span").some((s) => s.className === "chip" && s.textContent === "from config.json"), "the seed mark is a neutral chip");
  const gh = sectionOf(body, "github").querySelector("tbody").rows[0];
  assert.ok(classesOf(gh.cells[0].querySelector("div.c-main")).includes("mono"));
  assert.ok(classesOf(gh.cells[3]).includes("muted"), "'nothing' is muted");
  assert.ok(!classesOf(awsRows[0].rows[0].cells[4]).includes("muted") || awsRows[0].rows[0].cells[4].textContent === "nothing");
});

test("table head and body share one colgroup (G16), and the head cells are the kit's .tbl ones", () => {
  const p = page();
  for (const [key, cols] of [["aws", 6], ["github", 5], ["token:sentry", 5]]) {
    const table = p.box.credTable({ key, title: key, entries: [] });
    assert.ok(classesOf(table).includes("tbl"), key + ": the kit's .tbl");
    assert.strictEqual(table.children[0].tag, "colgroup", key + ": the colgroup comes first");
    assert.strictEqual(table.children[0].children.length, cols);
    assert.strictEqual(table.querySelectorAll("th").length, cols, key + ": one column for each head cell");
  }
});

test("CSS: one set of column shares on the colgroup, the frame's head cells (12/500 muted, sentence case) and 7px rows", () => {
  const shares = [1, 2, 3, 5].map((n) => cssDecls(CR + ' .ctab-sec:not([data-group="aws"]) .ctab col:nth-child(' + n + ")").get("width"));
  for (const w of shares) assert.match(w, /^\d+(\.\d+)?%$/);
  assert.ok(shares.reduce((a, w) => a + parseFloat(w), 0) < 100, "Used by keeps a share");
  assert.deepStrictEqual(rulesNaming(/\.ctab(?:-sec[^ ]*)?(?: \.ctab)? th:nth-child/).map((r) => r.selector), [], "no second set of shares on the head cells");
  const th = cssDecls(CR + " .ctab th");
  for (const k of ["text-transform", "letter-spacing", "font-size", "font-weight", "color"]) assert.strictEqual(th.get(k), undefined, "the kit's .tbl th stands: " + k);
  assert.strictEqual(cssDecls(".tbl th").get("font-size"), "var(--t-sm)");
  assert.strictEqual(cssDecls(".tbl th").get("font-weight"), "var(--w-medium)");
  assert.strictEqual(cssDecls(".tbl th").get("color"), "var(--muted)");
  const td = cssDecls(CR + " .ctab td");
  assert.strictEqual(td.get("padding-top"), "7px", "§B4: 7px rows");
  assert.strictEqual(td.get("padding-bottom"), "7px");
  assert.strictEqual(cssDecls(CR + " .ctab td:first-child").get("padding-left"), "16px");
  assert.strictEqual(cssDecls(CR + " .ctab td:last-child").get("padding-right"), "12px");
  const legacy = [1, 2].map((n) => cssDecls(CR + ' .ctab-sec[data-group="legacy"] .ctab col:nth-child(' + n + ")").get("width"));
  assert.deepStrictEqual(legacy, ["45%", "55%"], "§B4: the legacy table's colgroup");
});

test("CSS: the strip, the section head and the section are the frame's (§B4)", () => {
  const strip = cssDecls(CR + " .cstrip");
  assert.strictEqual(strip.get("padding"), "8px 8px 8px 14px");
  assert.strictEqual(strip.get("gap"), "8px");
  assert.strictEqual(strip.get("border"), "0");
  assert.strictEqual(strip.get("background"), "var(--panel)", "the app.css .strip is amber: the page sets its own surface");
  assert.strictEqual(cssDecls(CR + " .cstrip > #store-lock").get("margin-left"), "auto", "Lock now sits at the right (the frame's spacer)");
  const head = cssDecls(CR + " .ctab-sec > .sec-h");
  assert.strictEqual(head.get("padding"), "12px 16px");
  assert.strictEqual(head.get("gap"), "12px");
  assert.strictEqual(head.get("border-bottom"), "1px solid var(--line)");
  assert.strictEqual(head.get("text-transform"), "none");
  assert.strictEqual(cssDecls(PG + " #creds-body").get("gap"), "10px", "§B4 `.m2-body-tight`");
  const under = cssDecls(CR + " .cstrip-wrap > .hint");
  assert.strictEqual(under.get("margin-top"), "-4px");
  assert.strictEqual(under.get("padding"), "0 2px");
});

test("CSS: the header menu is positioned under its trigger, above the page, and the trigger shows pressed while it is open", () => {
  const wrap = cssDecls(CR + " .cred-add");
  assert.strictEqual(wrap.get("position"), "relative");
  const pop = cssDecls(CR + " .cred-add > .menu");
  assert.strictEqual(pop.get("position"), "absolute");
  assert.strictEqual(pop.get("right"), "0");
  assert.strictEqual(pop.get("top"), "calc(100% + var(--sp-1))");
  assert.ok(Number(pop.get("z-index")) >= 30);
  assert.strictEqual(cssDecls(CR + ' .cred-add > .btn:where(:not(.pri))[aria-expanded="true"]').get("background"), "var(--raise)");
});
