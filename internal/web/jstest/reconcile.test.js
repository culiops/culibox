"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { reconcileRows, rowSig } = require("../static/reconcile.js");

// A minimal DOM: just what reconcileRows touches. `detaches` counts every time a
// node leaves its parent — in a browser that is exactly when focus inside it is
// lost, so "detaches === 0" is the jsdom-free proxy for "focus survived".
class El {
  constructor(sid, cls) {
    this.dataset = sid ? { sid } : {};
    this._cls = new Set(cls ? cls.split(" ") : []);
    this.classList = { contains: (c) => this._cls.has(c) };
    this.parentNode = null;
    this.children = [];
    this.detaches = 0;
  }
  get nextElementSibling() {
    const p = this.parentNode;
    if (!p) return null;
    return p.children[p.children.indexOf(this) + 1] || null;
  }
  get firstElementChild() { return this.children[0] || null; }
  _detach(n) {
    const i = this.children.indexOf(n);
    if (i >= 0) { this.children.splice(i, 1); n.parentNode = null; n.detaches++; }
  }
  remove() { if (this.parentNode) this.parentNode._detach(this); }
  appendChild(n) { return this.insertBefore(n, null); }
  insertBefore(n, ref) {
    if (n.parentNode) n.parentNode._detach(n);
    const i = ref ? this.children.indexOf(ref) : -1;
    if (i < 0) this.children.push(n); else this.children.splice(i, 0, n);
    n.parentNode = this;
    return n;
  }
  replaceChild(n, old) {
    if (n.parentNode) n.parentNode._detach(n);
    const i = this.children.indexOf(old);
    this.children.splice(i, 1, n);
    old.parentNode = null; old.detaches++;
    n.parentNode = this;
    return old;
  }
}

const render = (s) => new El(s.id, "srow");
const sig = (s) => s.id + ":" + s.activity;
const ids = (ul) => ul.children.map((n) => (n.classList.contains("peek") ? "peek" : n.dataset.sid || "?"));

function seeded(sessions) {
  const ul = new El();
  reconcileRows(ul, sessions, render, sig);
  return ul;
}

test("first run renders every session in order and returns the row map", () => {
  const ul = new El();
  const map = reconcileRows(ul, [{ id: "a", activity: "idle" }, { id: "b", activity: "idle" }], render, sig);
  assert.deepEqual(ids(ul), ["a", "b"]);
  assert.strictEqual(map.get("a"), ul.children[0]);
});

test("an unchanged signature keeps the node and never detaches it (focus survives)", () => {
  const list = [{ id: "a", activity: "idle" }, { id: "b", activity: "working" }];
  const ul = seeded(list);
  const [a, b] = ul.children;
  reconcileRows(ul, list, render, sig);
  assert.strictEqual(ul.children[0], a);
  assert.strictEqual(ul.children[1], b);
  assert.strictEqual(a.detaches + b.detaches, 0);
});

test("one changed session replaces exactly one node", () => {
  const ul = seeded([{ id: "a", activity: "idle" }, { id: "b", activity: "working" }]);
  const [a, b] = ul.children;
  reconcileRows(ul, [{ id: "a", activity: "idle" }, { id: "b", activity: "waiting" }], render, sig);
  assert.strictEqual(ul.children[0], a);
  assert.notStrictEqual(ul.children[1], b);
  assert.strictEqual(a.detaches, 0);
  assert.strictEqual(ul.children[1].dataset.sig, "b:waiting");
});

test("reorder moves nodes without recreating them", () => {
  const ul = seeded([{ id: "a", activity: "idle" }, { id: "b", activity: "idle" }, { id: "c", activity: "idle" }]);
  const [a, b, c] = ul.children;
  reconcileRows(ul, [{ id: "c", activity: "idle" }, { id: "a", activity: "idle" }, { id: "b", activity: "idle" }], render, sig);
  assert.deepEqual(ids(ul), ["c", "a", "b"]);
  assert.strictEqual(ul.children[0], c);
  assert.strictEqual(ul.children[1], a);
  assert.strictEqual(ul.children[2], b);
});

test("a removed session's row goes, with its peek", () => {
  const ul = seeded([{ id: "a", activity: "idle" }, { id: "b", activity: "idle" }]);
  ul.insertBefore(new El(null, "peek"), ul.children[1]); // a's open peek
  reconcileRows(ul, [{ id: "b", activity: "idle" }], render, sig);
  assert.deepEqual(ids(ul), ["b"]);
});

test("an open peek travels with its row on a reorder", () => {
  const ul = seeded([{ id: "a", activity: "idle" }, { id: "b", activity: "idle" }]);
  const peek = new El(null, "peek");
  ul.insertBefore(peek, ul.children[1]); // a, peek, b
  reconcileRows(ul, [{ id: "b", activity: "idle" }, { id: "a", activity: "idle" }], render, sig);
  assert.deepEqual(ids(ul), ["b", "a", "peek"]);
  assert.strictEqual(ul.children[2], peek);
});

test("a replaced row drops its peek (app.js reopens it on the fresh row)", () => {
  const ul = seeded([{ id: "a", activity: "idle" }]);
  ul.appendChild(new El(null, "peek"));
  reconcileRows(ul, [{ id: "a", activity: "working" }], render, sig);
  assert.deepEqual(ids(ul), ["a"]);
});

test("foreign nodes are purged: placeholder, mobile headers, orphan peeks", () => {
  const ul = new El();
  ul.appendChild(new El(null, "peek"));       // orphan peek at the top
  ul.appendChild(new El(null, "empty"));      // "No sessions yet"
  ul.appendChild(new El(null, "mhd"));        // mobile group header
  reconcileRows(ul, [{ id: "a", activity: "idle" }], render, sig);
  assert.deepEqual(ids(ul), ["a"]);
});

test("a node without a signature (mobile card) is replaced, and duplicates are dropped", () => {
  const ul = new El();
  const mobileCard = new El("a", "card");
  ul.appendChild(mobileCard);
  ul.appendChild(new El("a", "srow")); // duplicate sid
  reconcileRows(ul, [{ id: "a", activity: "waiting" }], render, sig);
  assert.deepEqual(ids(ul), ["a"]);
  assert.notStrictEqual(ul.children[0], mobileCard);
  assert.strictEqual(ul.children[0].dataset.sig, "a:waiting");
});

test("rowSig changes on every field a row or card renders, and only those", () => {
  const base = { id: "a", name: "n", agent: "claude", cwd: "/x", runtime: "host", activity: "waiting",
    need: "turn", ask: "", ask_full: "", summary: "hi", replyable: false, attached: false, claude: true, exited: false };
  const local = { killing: false, heldHere: false, pct: 10, boxRuntime: "runc", sending: false, expanded: false, answering: false };
  const s0 = rowSig(base, local);
  assert.strictEqual(rowSig(Object.assign({}, base), Object.assign({}, local)), s0);
  for (const [k, v] of Object.entries({ name: "m", activity: "working", need: "ask", ask: "Bash(x)",
    ask_full: "Bash(x y)", summary: "bye", replyable: true, attached: true, cwd: "/y" })) {
    assert.notStrictEqual(rowSig(Object.assign({}, base, { [k]: v }), local), s0, k);
  }
  for (const [k, v] of Object.entries({ killing: true, heldHere: true, pct: 11, replies: true, sending: true, expanded: true, answering: true, elev: true })) {
    assert.notStrictEqual(rowSig(base, Object.assign({}, local, { [k]: v })), s0, k);
  }
  assert.strictEqual(rowSig(Object.assign({}, base, { created: "later" }), local), s0, "unrendered field");
});

test("rowSig moves with reply_ready: the card's composer opens once the server is ready (#77)", () => {
  // In the seconds after a turn ends the card renders with reply_ready=false;
  // without this, nothing else changes when it turns true and Send stays shut.
  const base = { id: "a", name: "n", activity: "waiting", need: "turn", replyable: true, reply_ready: false };
  const s0 = rowSig(base, {});
  assert.notStrictEqual(rowSig(Object.assign({}, base, { reply_ready: true }), {}), s0);
  assert.strictEqual(rowSig(Object.assign({}, base, { reply_ready: undefined }), {}), s0, "absent reads as not ready");
});

test("rowSig moves with cost_usd, and an absent cost differs from a zero cost", () => {
  const base = { id: "a", name: "n", activity: "working" };
  const s0 = rowSig(base, {});
  assert.notStrictEqual(rowSig(Object.assign({}, base, { cost_usd: 1.5 }), {}), s0, "a cost appearing repaints");
  assert.notStrictEqual(rowSig(Object.assign({}, base, { cost_usd: 0 }), {}), s0, "0 is shown as $0.00, absent is not");
  assert.notStrictEqual(rowSig(Object.assign({}, base, { cost_usd: 1.5 }), {}), rowSig(Object.assign({}, base, { cost_usd: 1.6 }), {}));
});

test("rowSig moves with the AWS binding and with the SSO card's local state", () => {
  const base = { id: "a", name: "n", activity: "working", need: "sso", aws: { label: "sandbox", account_id: "1", sso_expired: "corp" } };
  const local = { sso: "" };
  const s0 = rowSig(base, local);
  const elevated = Object.assign({}, base, { aws: Object.assign({}, base.aws, { elevated_until: "2026-10-17T10:00:00Z" }) });
  assert.notStrictEqual(rowSig(elevated, local), s0, "a grant repaints the write badge");
  assert.notStrictEqual(rowSig(base, { sso: "waiting~ABCD-EFGH" }), s0, "a sign-in flow moving on repaints the card");
});

test("rowSig moves with the AWS connections (badge, SSO flag, write window)", () => {
  const base = { id: "a", name: "n", activity: "working", aws_connections: [{ name: "a", label: "A", account_id: "1", prod: false }] };
  const s0 = rowSig(base, {});
  assert.notStrictEqual(rowSig(Object.assign({}, base, { aws_connections: [{ ...base.aws_connections[0], elevated_until: "2026-10-17T10:00:00Z" }] }), {}), s0);
  assert.notStrictEqual(rowSig(Object.assign({}, base, { aws_connections: [{ ...base.aws_connections[0], sso_expired: "corp" }] }), {}), s0);
  assert.notStrictEqual(rowSig(Object.assign({}, base, { aws_connections: [] }), {}), s0);
});

test("rowSig changes when a shell's label changes, and when its parent does", () => {
  const sh = { id: "s1", name: "sh", activity: "idle", parent_id: "p" };
  const s0 = rowSig(sh, { shellLabel: "shell 1" });
  assert.notStrictEqual(rowSig(sh, { shellLabel: "shell 2" }), s0, "renumbering repaints the row");
  assert.notStrictEqual(rowSig(sh, { shellLabel: "" }), s0, "un-nesting repaints the row");
  assert.notStrictEqual(rowSig(Object.assign({}, sh, { parent_id: "q" }), { shellLabel: "shell 1" }), s0, "re-parenting repaints the row");
  assert.notStrictEqual(rowSig(Object.assign({}, sh, { parent_id: "" }), { shellLabel: "shell 1" }), s0);
  assert.strictEqual(rowSig(Object.assign({}, sh), { shellLabel: "shell 1" }), s0);
});
