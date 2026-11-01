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
    this.classList = { contains: (c) => self.className.split(/\s+/).includes(c) };
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
