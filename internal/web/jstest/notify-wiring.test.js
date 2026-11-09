"use strict";
// The impure shell around the rule table (#177, DC§1): prefs storage, endedByMe, delivery through a
// fake Notification, and app.js's notifyTransitions lifted into a vm with fake globals.
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const m = require("../static/notifymodel.js");

const read = (f) => fs.readFileSync(path.join(__dirname, "..", "static", f), "utf8");
function lift(name) {
  const src = read("app.js");
  const at = src.indexOf("function " + name + "(");
  assert.ok(at >= 0, name + " is in app.js");
  return src.slice(at, src.indexOf("\n}\n", at) + 2);
}
function sess(id, over) { return Object.assign({ id, name: "n-" + id, activity: "working" }, over || {}); }
function ask(id) { return sess(id, { activity: "waiting", need: "ask", ask: "Bash(rm -rf build)", attention: "block" }); }
function sso(id) { return sess(id, { activity: "waiting", need: "sso", aws_connections: [{ sso_expired: "prod-sso" }], attention: "block" }); }

function makeStore(init) {
  const map = new Map(Object.entries(init || {}));
  return { map, getItem: (k) => (map.has(k) ? map.get(k) : null), setItem: (k, v) => map.set(k, String(v)), removeItem: (k) => map.delete(k) };
}
function withStorage(st, fn) {
  const had = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { value: st, configurable: true, writable: true });
  try { return fn(); } finally { if (had) Object.defineProperty(globalThis, "localStorage", had); else delete globalThis.localStorage; }
}

test("prefs: defaults are block/done/ended on, message/chime off; stored 0/1 override; junk is ignored", () => {
  assert.deepStrictEqual(withStorage(makeStore(), () => m.readPrefs()), { block: true, done: true, ended: true, message: false, chime: false });
  const got = withStorage(makeStore({ "agent-box:notify:block": "0", "agent-box:notify:message": "1", "agent-box:notify:chime": "1", "agent-box:notify:done": "yes" }), () => m.readPrefs());
  assert.deepStrictEqual(got, { block: false, done: true, ended: true, message: true, chime: true });
  assert.deepStrictEqual(m.NOTIFY_PREFS, {
    block: "agent-box:notify:block", done: "agent-box:notify:done", ended: "agent-box:notify:ended",
    message: "agent-box:notify:message", chime: "agent-box:notify:chime",
  });
});

test("prefs: writePref round-trips; an unknown name is refused; unavailable storage never throws and keeps the defaults", () => {
  const st = makeStore();
  withStorage(st, () => {
    assert.strictEqual(m.writePref("message", true), true);
    assert.strictEqual(st.map.get("agent-box:notify:message"), "1");
    assert.strictEqual(m.writePref("block", false), true);
    assert.strictEqual(m.readPrefs().block, false);
    assert.strictEqual(m.writePref("master", true), false);
    assert.strictEqual(m.writePref("nope", true), false);
  });
  const boom = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("denied"); } };
  withStorage(boom, () => {
    assert.strictEqual(m.writePref("done", false), false);
    assert.deepStrictEqual(m.readPrefs(), { block: true, done: true, ended: true, message: false, chime: false });
  });
  assert.deepStrictEqual(withStorage(undefined, () => m.readPrefs()), { block: true, done: true, ended: true, message: false, chime: false });
});

test("endedByMe: an id stays 10 minutes, then is pruned", () => {
  const map = new Map();
  m.endedMark(map, "a", 1000);
  m.endedMark(map, "b", 1000 + 5 * 60000);
  assert.deepStrictEqual(m.endedLive(map, 1000 + 9 * 60000), ["a", "b"]);
  assert.deepStrictEqual(m.endedLive(map, 1000 + m.ENDED_BY_ME_MS), ["b"]);
  assert.strictEqual(map.has("a"), false);
});

function fakeNotification(opts) {
  const made = [];
  function N(title, o) {
    if (opts && opts.throws) throw new Error("refused");
    this.title = title; this.options = o; this.closed = false;
    this.close = () => { this.closed = true; };
    made.push(this);
  }
  return { N, made };
}

test("deliverNotes: title + {body, ...options}; click opens the note and closes it; a refusing browser stays silent", () => {
  const { N, made } = fakeNotification();
  const opened = [];
  const notes = [{ title: "n-a", body: "Wants to run Bash", tag: "ab:a:block", sid: "a", options: { tag: "ab:a:block", renotify: true, requireInteraction: true, silent: false } }];
  assert.strictEqual(m.deliverNotes(notes, N, (n) => opened.push(n.sid)), 1);
  assert.strictEqual(made[0].title, "n-a");
  assert.deepStrictEqual(made[0].options, { body: "Wants to run Bash", tag: "ab:a:block", renotify: true, requireInteraction: true, silent: false });
  made[0].onclick();
  assert.deepStrictEqual(opened, ["a"]);
  assert.strictEqual(made[0].closed, true);
  assert.strictEqual(m.deliverNotes(notes, fakeNotification({ throws: true }).N, () => {}), 0);
  assert.strictEqual(m.deliverNotes(notes, undefined, () => {}), 0);
});

// app.js's notifyTransitions + notifyOpen on a fake page.
function page(over) {
  const f = fakeNotification();
  const calls = { focus: 0, opened: [], sidebar: 0 };
  const store = makeStore({ "agent-box:notify": "1" });
  const ctx = Object.assign({
    Date, Array, Map, Object,
    Notification: Object.assign(f.N, { permission: "granted" }),
    document: { hasFocus: () => false, documentElement: { classList: { contains: (c) => c === "sidebar-hidden" && !!ctx.hidden } } },
    window: { focus: () => { calls.focus++; } },
    localStorage: store,
    openTerminal: (id, name) => calls.opened.push([id, name]),
    toggleSidebar: () => { calls.sidebar++; },
    lastSessions: [], sessionsLoaded: true, hidden: false,
  }, over || {});
  vm.createContext(ctx);
  new vm.Script(read("attention.js"), { filename: "attention.js" }).runInContext(ctx);
  new vm.Script(read("notifymodel.js"), { filename: "notifymodel.js" }).runInContext(ctx);
  new vm.Script("let notifyPrev = null; const endedByMe = new Map(); const restartedByMe = new Set();\n" + lift("notifyOpen") + lift("notifyTransitions"), { filename: "app.js:notify" }).runInContext(ctx);
  return { ctx, made: f.made, calls, store };
}
const tick = (p, list) => { p.ctx.lastSessions = list; vm.runInContext("notifyTransitions()", p.ctx); };

test("one tick gives one notification per cause with the right tag and options; the first poll only seeds", () => {
  const p = page();
  tick(p, [sess("a"), sess("b"), sess("c")]);
  assert.strictEqual(p.made.length, 0, "first poll seeds");
  tick(p, [ask("a"), sso("b"), sso("c")]);
  assert.deepStrictEqual(p.made.map((n) => n.options.tag), ["ab:a:block", "ab:sso:prod-sso"], "ask + one shared AWS cause, not one per session");
  assert.strictEqual(p.made[0].title, "n-a");
  assert.strictEqual(p.made[0].options.body, "Wants to run Bash");
  assert.strictEqual(p.made[0].options.requireInteraction, true);
  assert.strictEqual(p.made[1].title, "CuliBox");
  tick(p, [ask("a"), sso("b"), sso("c")]);
  assert.strictEqual(p.made.length, 2, "an unchanged state announces nothing");
});

test("a focused window, a missing permission and the master switch off give none, and the state still advances", () => {
  const p = page({ document: { hasFocus: () => true, documentElement: { classList: { contains: () => false } } } });
  tick(p, [sess("a")]);
  tick(p, [ask("a")]);
  assert.strictEqual(p.made.length, 0);
  p.ctx.document.hasFocus = () => false;
  tick(p, [ask("a")]);
  assert.strictEqual(p.made.length, 0, "the ask began while focused: not replayed on blur");
  const q = page();
  q.ctx.Notification.permission = "denied";
  tick(q, [sess("a")]); tick(q, [ask("a")]);
  assert.strictEqual(q.made.length, 0);
  const r = page();
  r.store.removeItem("agent-box:notify");
  tick(r, [sess("a")]); tick(r, [ask("a")]);
  assert.strictEqual(r.made.length, 0, "master switch off");
});

test("a class pref switched off silences that class", () => {
  const p = page();
  p.store.setItem("agent-box:notify:block", "0");
  tick(p, [sess("a")]); tick(p, [ask("a")]);
  assert.strictEqual(p.made.length, 0);
});

test("a session this client ended is not announced; one that vanished on its own is", () => {
  const p = page();
  tick(p, [sess("a"), sess("b")]);
  vm.runInContext('endedMark(endedByMe, "a", Date.now())', p.ctx);
  tick(p, []);
  assert.deepStrictEqual(p.made.map((n) => n.options.tag), ["ab:b:end"]);
});

test("a failed fetch is never a poll: not loaded, or a non-array list, announces nothing and leaves the state alone", () => {
  const p = page();
  tick(p, [sess("a")]);
  p.ctx.sessionsLoaded = false;
  tick(p, []);
  p.ctx.sessionsLoaded = true;
  tick(p, null);
  assert.strictEqual(p.made.length, 0);
  tick(p, [sess("a")]);
  assert.strictEqual(p.made.length, 0, "a still live: never ended");
});

test("click: a session note focuses the window and opens it; a batch only shows a hidden sidebar", () => {
  const p = page();
  tick(p, [sess("a")]); tick(p, [ask("a")]);
  p.made[0].onclick();
  assert.strictEqual(p.calls.focus, 1);
  assert.deepStrictEqual(p.calls.opened, [["a", "n-a"]]);
  const b = page();
  b.ctx.hidden = true;
  tick(b, [sess("a"), sess("b"), sess("c")]);
  const d = (id) => sess(id, { activity: "waiting", need: "turn", summary: "x", attention: "done" });
  tick(b, [d("a"), d("b"), d("c")]);
  assert.strictEqual(b.made.length, 1);
  assert.strictEqual(b.made[0].options.tag, "ab:done-batch");
  b.made[0].onclick();
  assert.strictEqual(b.calls.opened.length, 0);
  assert.strictEqual(b.calls.sidebar, 1);
});

test("app.js wiring: killSession and restartSession mark endedByMe; no innerHTML in the new code", () => {
  const app = read("app.js");
  assert.match(lift("killSession"), /endedMark\(endedByMe, id/);
  assert.match(lift("restartSessionRun"), /endedMark\(endedByMe, s\.id/);
  for (const fn of ["notifyOpen", "notifyTransitions"]) assert.doesNotMatch(lift(fn), /innerHTML/);
  assert.doesNotMatch(app, /\bnotifyBody\(/);
});

test("click: an SSO cause note and a store note open the first affected session under its own name, never \"CuliBox\"", () => {
  const p = page();
  tick(p, [sess("a"), sess("b")]);
  tick(p, [sso("a"), sso("b")]);
  assert.strictEqual(p.made[0].title, "CuliBox");
  p.made[0].onclick();
  assert.deepStrictEqual(p.calls.opened, [["a", "n-a"]]);
  const q = page();
  const lock = (id) => sess(id, { activity: "waiting", need: "store_locked", attention: "block" });
  tick(q, [sess("x")]);
  tick(q, [lock("x")]);
  const store = q.made.find((n) => n.options.tag === "ab:store");
  assert.ok(store, "a store note");
  store.onclick();
  assert.deepStrictEqual(q.calls.opened, [["x", "n-x"]]);
});

test("restart: the mark holds until a poll sees the id live again, then a real crash still notifies Ended", () => {
  const p = page();
  tick(p, [sess("a")]);
  vm.runInContext('endedMark(endedByMe, "a", Date.now()); restartedByMe.add("a")', p.ctx);
  tick(p, [sess("a")]); // restarted and live again: the mark is dropped
  assert.strictEqual(vm.runInContext("endedByMe.has('a') || restartedByMe.has('a')", p.ctx), false);
  tick(p, [sess("a", { activity: "crashed" })]);
  assert.deepStrictEqual(p.made.map((n) => n.options.tag), ["ab:a:end"]);
});

test("restart: a restart that gets a new id is silent for the old one; a kill mark is never cleared by a poll", () => {
  const p = page();
  tick(p, [sess("a"), sess("k")]);
  vm.runInContext('endedMark(endedByMe, "a", Date.now()); restartedByMe.add("a"); endedMark(endedByMe, "k", Date.now())', p.ctx);
  tick(p, [sess("k")]);
  tick(p, []);
  assert.strictEqual(p.made.length, 0);
  const q = page();
  tick(q, [sess("k")]);
  vm.runInContext('endedMark(endedByMe, "k", Date.now())', q.ctx);
  tick(q, [sess("k")]); // still listed while the DELETE is in flight
  tick(q, [sess("k", { exited: true })]);
  assert.strictEqual(q.made.length, 0);
});

test("app.js: a restart request that throws clears the mark; a null /api/sessions body is not a list", () => {
  assert.match(lift("restartSessionRun"), /catch \(e\) \{ endedByMe\.delete\(s\.id\); throw e; \}/);
  const src = lift("refreshSessions");
  assert.match(src, /if \(!Array\.isArray\(sessions\)\) return null;\n\s+lastSessions = sessions;/);
  assert.doesNotMatch(src, /sessions \|\| \[\]/);
});
